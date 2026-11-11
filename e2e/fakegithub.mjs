// fakegithub.mjs is a small GitHub for the repository scenarios (REPOS.md §3,
// §4.5): the web pages of the manifest flow and the REST endpoints Socrates
// calls, on one port, with a real RSA key for the App so that every JWT the
// server signs is checked against its public half. The git side is not here:
// SOCRATES_GITHUB_GIT points at a folder of bare repositories (file://).
//
//   POST /settings/apps/new?state=S            the manifest form; 302 to its
//                                              redirect_url?code=…&state=S
//   POST /app-manifests/{code}/conversions     the App, with its private key
//   GET  /apps/{slug}/installations/new?state=S 302 to the manifest's
//                                              setup_url, with state=S
//   GET  /app/installations                    (App JWT) the installations: the
//                                              one on the owner, and `others`
//   POST /app/installations/{id}/access_tokens (App JWT) a token
//   DELETE /app/installations/{id}             (App JWT) uninstalls it
//   GET  /installation/repositories            (token) its repositories
//   POST /repos/{owner}/{name}/pulls           (token) a pull request
//
// `others` are more installations, each { id, account, type, repos }, as
// someone else (an organisation, a stranger) installing the public App makes
// them. `calls` is every request as "METHOD path", and `problems` every
// request that was refused (a bad JWT, an unknown token): a scenario asserts
// it is empty. `uninstalled` are the ids removed, `installStates` the state
// each visit of the install page carried.

import http from 'node:http';
import { generateKeyPairSync, createPublicKey, verify, randomBytes } from 'node:crypto';

export async function startFakeGitHub({ owner = 'acme', repos = [], appId = 4242, slug = 'socrates-e2e', installation = 7, others = [] } = {}) {
  const { privateKey } = generateKeyPairSync('rsa', {
    modulusLength: 2048,
    privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
    publicKeyEncoding: { type: 'spki', format: 'pem' },
  });
  const publicKey = createPublicKey(privateKey);
  const gh = {
    calls: [], problems: [], manifest: null, tokens: new Map(), pulls: [], url: '', uninstalled: new Set(), installStates: [],
    owner, slug, appId, installation,
  };
  // Every installation by id: the owner's first, then the others.
  const installs = new Map([[installation, { id: installation, account: owner, type: 'User', repos }]]);
  for (const one of others) installs.set(one.id, { type: 'User', repos: [], ...one });
  const live = (id) => installs.has(id) && !gh.uninstalled.has(id);
  const code = 'code-' + randomBytes(6).toString('hex');

  const json = (res, status, body) => {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(body));
  };
  const refuse = (res, req, why) => {
    gh.problems.push(req.method + ' ' + req.url + ': ' + why);
    json(res, 401, { message: why });
  };
  // appAuth checks an App JWT: RS256 by the App's key, issued by the App, not
  // expired, at most ten minutes long.
  const appAuth = (req) => {
    const m = /^Bearer (.+)$/.exec(req.headers.authorization || '');
    if (!m) return 'no JWT';
    const [head, body, sig] = m[1].split('.');
    if (!sig) return 'not a JWT';
    const ok = verify('RSA-SHA256', Buffer.from(head + '.' + body), publicKey, Buffer.from(sig, 'base64url'));
    if (!ok) return 'the JWT is not signed by the App';
    const header = JSON.parse(Buffer.from(head, 'base64url').toString());
    const claims = JSON.parse(Buffer.from(body, 'base64url').toString());
    const now = Math.floor(Date.now() / 1000);
    if (header.alg !== 'RS256') return 'alg ' + header.alg;
    if (String(claims.iss) !== String(appId)) return 'iss ' + claims.iss;
    if (!(claims.exp > now && claims.iat <= now && claims.exp - claims.iat <= 600)) return 'bad times ' + JSON.stringify(claims);
    return '';
  };
  const tokenAuth = (req) => {
    const m = /^token (.+)$/.exec(req.headers.authorization || '');
    return m && gh.tokens.has(m[1]) ? gh.tokens.get(m[1]) : null;
  };
  const readBody = (req) => new Promise((resolve) => {
    let raw = '';
    req.on('data', (c) => { raw += c; });
    req.on('end', () => resolve(raw));
  });

  const server = http.createServer(async (req, res) => {
    const u = new URL(req.url, 'http://fake');
    const path = u.pathname;
    gh.calls.push(req.method + ' ' + path);
    const raw = await readBody(req);
    let m;
    if (req.method === 'POST' && (path === '/settings/apps/new' || /^\/organizations\/[^/]+\/settings\/apps\/new$/.test(path))) {
      gh.manifest = JSON.parse(new URLSearchParams(raw).get('manifest') || 'null');
      if (!gh.manifest || !gh.manifest.redirect_url) return json(res, 422, { message: 'no manifest' });
      const back = new URL(gh.manifest.redirect_url);
      back.searchParams.set('code', code);
      back.searchParams.set('state', u.searchParams.get('state') || '');
      res.writeHead(302, { Location: back.toString() });
      return res.end();
    }
    if (req.method === 'POST' && (m = /^\/app-manifests\/([^/]+)\/conversions$/.exec(path))) {
      if (m[1] !== code) return json(res, 404, { message: 'Not Found' });
      return json(res, 201, {
        id: appId, slug, name: gh.manifest.name, html_url: gh.url + '/apps/' + slug,
        client_id: 'Iv1.e2e', client_secret: 'secret-' + randomBytes(8).toString('hex'),
        webhook_secret: 'hook-' + randomBytes(8).toString('hex'), pem: privateKey,
        owner: { login: owner, type: 'User' },
      });
    }
    if (req.method === 'GET' && path === '/apps/' + slug + '/installations/new') {
      const back = new URL(gh.manifest.setup_url);
      back.searchParams.set('installation_id', String(installation));
      back.searchParams.set('setup_action', 'install');
      // GitHub hands the install page's state back to the setup URL.
      gh.installStates.push(u.searchParams.get('state') || '');
      if (u.searchParams.has('state')) back.searchParams.set('state', u.searchParams.get('state'));
      res.writeHead(302, { Location: back.toString() });
      return res.end();
    }
    if (req.method === 'GET' && path === '/app/installations') {
      const why = appAuth(req);
      if (why) return refuse(res, req, why);
      const page = Number(u.searchParams.get('page') || 1);
      const list = [...installs.values()].filter((one) => live(one.id)).map((one) => ({ id: one.id, account: { login: one.account, type: one.type } }));
      return json(res, 200, page === 1 ? list : []);
    }
    if (req.method === 'DELETE' && (m = /^\/app\/installations\/(\d+)$/.exec(path))) {
      const why = appAuth(req);
      if (why) return refuse(res, req, why);
      if (!live(Number(m[1]))) return json(res, 404, { message: 'Not Found' });
      gh.uninstalled.add(Number(m[1]));
      res.writeHead(204);
      return res.end();
    }
    if (req.method === 'POST' && (m = /^\/app\/installations\/(\d+)\/access_tokens$/.exec(path))) {
      const why = appAuth(req);
      if (why) return refuse(res, req, why);
      if (!live(Number(m[1]))) return json(res, 404, { message: 'Not Found' });
      const asked = raw ? (JSON.parse(raw).repositories || null) : null;
      const token = 'ghs_' + randomBytes(12).toString('hex');
      gh.tokens.set(token, { installation: Number(m[1]), repositories: asked });
      return json(res, 201, { token, expires_at: new Date(Date.now() + 3600e3).toISOString() });
    }
    if (req.method === 'GET' && path === '/installation/repositories') {
      const tok = tokenAuth(req);
      if (!tok) return refuse(res, req, 'unknown token');
      const page = Number(u.searchParams.get('page') || 1);
      const granted = installs.get(tok.installation).repos;
      const list = page === 1 ? granted.map((r) => ({ full_name: r.full_name, default_branch: r.default_branch || 'main', private: !!r.private })) : [];
      return json(res, 200, { total_count: list.length, repositories: list });
    }
    if (req.method === 'POST' && (m = /^\/repos\/([^/]+)\/([^/]+)\/pulls$/.exec(path))) {
      const tok = tokenAuth(req);
      if (!tok) return refuse(res, req, 'unknown token');
      if (tok.repositories && !tok.repositories.includes(decodeURIComponent(m[2]))) return refuse(res, req, 'the token does not reach ' + m[2]);
      const body = JSON.parse(raw || '{}');
      const number = gh.pulls.length + 1;
      gh.pulls.push({ repo: decodeURIComponent(m[1]) + '/' + decodeURIComponent(m[2]), number, ...body });
      return json(res, 201, { number, html_url: gh.url + '/' + m[1] + '/' + m[2] + '/pull/' + number });
    }
    json(res, 404, { message: 'Not Found' });
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  gh.url = 'http://127.0.0.1:' + server.address().port;
  gh.close = () => new Promise((resolve) => { server.closeAllConnections?.(); server.close(resolve); });
  return gh;
}
