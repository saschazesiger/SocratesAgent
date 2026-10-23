// The browser harness: one real `socrates` binary, one real server, real tmux
// sessions, and Playwright driving the page.
//
// One thing has to be arranged before a browser can see a session: the CLIs.
// A suite that needed three logged-in accounts would not be a suite, so
// `e2e/fakebin/faketui` is built once and linked onto PATH under all three
// names. It is a terminal program, not a protocol speaker - which is what the
// product now runs - and it writes the same state files the real ones write,
// so the discovery and resume paths are exercised rather than mocked. The
// Shell harness needs no fake at all: /bin/sh is the thing under test.
//
// With SOCRATES_LIVE_AGENTS=1 a scenario can ask for `live: true` instead, and
// then the fakes stay off PATH and the real, logged-in CLIs are used.

import { chromium } from '/opt/browser-testing/node_modules/playwright-core/index.mjs';
import { createServer } from 'node:http';
import { createServer as createSocketServer } from 'node:net';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, realpathSync, rmSync, mkdirSync, symlinkSync, readdirSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
export const REPO = dirname(HERE);

// Everything this suite writes goes under e2e/out/, which is gitignored: the
// worktree is what gets committed and a stray artefact directory there is one
// `git add -A` away from being part of the change.
export const OUT = join(HERE, 'out');
export const SHOTS = join(OUT, 'shots');
// Everything a run makes outside e2e/out is tracked here, so that an
// interrupted run - Ctrl-C, a killed CI job, a scenario that throws past its
// own stop() - takes its directories and its tmux servers with it. Sixty
// leaked build and data directories once filled this machine's tmpfs, and a
// leaked server went on answering on the port the next run wanted.
const litter = new Set();
// The servers this run started. Node does not take its children with it, so
// an interrupted run leaves one listening on the port the next run wants -
// which is how a suite ends up talking to a server from twenty minutes ago.
const servers = new Set();

export function scratchDir(prefix) {
  // Resolved, because on macOS os.tmpdir() is /var/folders/..., a symlink to
  // /private/var/... The server reports the paths it made as real ones, and a
  // scenario comparing them against this would be comparing two spellings of
  // the same directory.
  let dir = mkdtempSync(join(tmpdir(), prefix));
  try { dir = realpathSync(dir); } catch { /* the unresolved name still works */ }
  litter.add(dir);
  return dir;
}

// sweep removes one tracked directory and whatever tmux server belongs to it.
function sweep(dir) {
  killTmux(dir);
  rmSync(dir, { recursive: true, force: true });
  litter.delete(dir);
}

let sweeping = false;
function sweepAll() {
  if (sweeping) return;
  sweeping = true;
  for (const child of servers) {
    try { child.kill('SIGKILL'); } catch { /* already gone */ }
  }
  servers.clear();
  for (const dir of [...litter]) {
    try { sweep(dir); } catch { /* the process is on its way out */ }
  }
  sweeping = false;
}
process.on('exit', sweepAll);
for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(signal, () => { sweepAll(); process.exit(130); });
}

// The headless shell Playwright installed. Its directory carries the build
// number, which changes whenever the machine's Playwright is updated, so the
// newest one present is used rather than one pinned build that is soon gone.
function defaultChrome() {
  const root = '/root/.cache/ms-playwright';
  const tail = 'chrome-headless-shell-linux64/chrome-headless-shell';
  let builds = [];
  try {
    builds = readdirSync(root).filter((d) => d.startsWith('chromium_headless_shell-'))
      .sort((a, b) => Number(b.split('-')[1]) - Number(a.split('-')[1]));
  } catch { /* no Playwright cache: the path below says what was expected */ }
  for (const dir of builds) {
    if (existsSync(join(root, dir, tail))) return join(root, dir, tail);
  }
  return join(root, 'chromium_headless_shell-1234', tail);
}
const CHROME = process.env.SOCRATES_E2E_CHROME || defaultChrome();

export const PASSWORD = 'socrates-e2e';
export const LIVE = process.env.SOCRATES_LIVE_AGENTS === '1';

// The three fake CLIs are one program under three names, built once per run.
// The Go-overlay trick the old suite used is gone with the adapter registry it
// pointed at: `socrates` is now built plainly.
function buildFakes() {
  const dir = scratchDir('socrates-e2e-');
  const bin = join(dir, 'bin');
  mkdirSync(bin);
  const faketui = join(bin, 'faketui');
  execFileSync('go', ['build', '-o', faketui, './e2e/fakebin/faketui'], { cwd: REPO });
  for (const name of ['claude', 'codex', 'opencode']) symlinkSync(faketui, join(bin, name));
  execFileSync('go', ['build', '-o', join(dir, 'socrates'), '.'], { cwd: REPO });
  return { dir, bin, exe: join(dir, 'socrates') };
}

// The live build is the shipped one: no fakes on PATH, the real CLIs.
function buildLive() {
  const dir = scratchDir('socrates-e2e-live-');
  execFileSync('go', ['build', '-o', join(dir, 'socrates'), '.'], { cwd: REPO });
  return { dir, bin: null, exe: join(dir, 'socrates') };
}

const builds = {};
export function binaries(live = false) {
  const key = live ? 'live' : 'fake';
  if (!builds[key]) builds[key] = live ? buildLive() : buildFakes();
  return builds[key];
}

export function cleanupBuild() {
  stopFakeS3();
  for (const key of Object.keys(builds)) {
    sweep(builds[key].dir);
    delete builds[key];
  }
}

// ------------------------------------------------------------- the bucket
//
// S3 is mandatory: a Socrates with no bucket serves the connect page and
// nothing else, so every scenario needs one. e2e/fakebin/fakes3 is the
// in-memory store the mirror's unit tests use (internal/s3/s3fake), served on
// a real port. It is started once per run, and every server gets its own
// prefix on it - by default one named after its data directory, so that a
// scenario's restart finds its own state again and no two scenarios ever see
// each other's. A scenario that wants a second data directory on the same
// state (`s3restore`) passes the first one's prefix.
//
// The credentials and the region are s3fake's defaults and the passphrase is
// the suite's own. None of it is a secret: the store dies with the run.
export const S3 = {
  bucket: 'socrates-e2e',
  accessKeyID: 'FAKEACCESSKEY',
  secretAccessKey: 'fake-secret-key',
  region: 'fake-1',
  passphrase: 'socrates-e2e-passphrase',
};
let fakeS3 = null;

// ensureFakeS3 builds and starts the fake once, and resolves to its URL.
export function ensureFakeS3() {
  if (fakeS3) return fakeS3.ready;
  const dir = scratchDir('socrates-e2e-s3-');
  const exe = join(dir, 'fakes3');
  execFileSync('go', ['build', '-o', exe, './e2e/fakebin/fakes3'], { cwd: REPO });
  const child = spawn(exe, ['-addr', '127.0.0.1:0'], { stdio: ['ignore', 'pipe', 'pipe'] });
  servers.add(child);
  child.on('exit', () => servers.delete(child));
  const state = { dir, child, url: '' };
  fakeS3 = state;
  state.ready = new Promise((resolve, reject) => {
    let out = '';
    const timer = setTimeout(() => reject(new Error('fakes3 never said where it listens: ' + out)), 15000);
    child.stdout.on('data', (d) => {
      out += String(d);
      const m = out.match(/listening on (http:\/\/\S+)/);
      if (m && !state.url) {
        clearTimeout(timer);
        state.url = m[1];
        resolve(state.url);
      }
    });
    child.stderr.on('data', (d) => { out += String(d); });
    child.on('exit', (code) => {
      clearTimeout(timer);
      if (!state.url) reject(new Error('fakes3 exited (' + code + '): ' + out));
    });
  });
  return state.ready;
}

function stopFakeS3() {
  if (!fakeS3) return;
  try { fakeS3.child.kill('SIGKILL'); } catch { /* already gone */ }
  sweep(fakeS3.dir);
  fakeS3 = null;
}

// fakeS3Switch flips the fake's own switches: 'down?status=503' makes every S3
// request fail until 'up'. Only the storage banner scenario breaks the bucket
// on purpose, and it puts it back in a finally.
export async function fakeS3Switch(what) {
  const url = await ensureFakeS3();
  const res = await fetch(url + '/_fakes3/' + what, { method: 'POST' });
  if (res.status !== 204) throw new Error('fakes3 refused ' + what + ': ' + res.status);
}

// s3Env is the environment that points a server at its prefix on the fake.
// A null prefix is a server with no bucket at all, which serves the connect
// page.
export function s3Env(prefix) {
  // An empty bucket name, not a missing one: the environment the suite was
  // started from must not hand a bucket to a server that is meant to have none.
  if (prefix === null) return { SOCRATES_S3_BUCKET: '' };
  if (!fakeS3 || !fakeS3.url) throw new Error('the fake bucket is not running: await ensureFakeS3() first');
  return {
    SOCRATES_S3_ENDPOINT: fakeS3.url,
    SOCRATES_S3_REGION: S3.region,
    SOCRATES_S3_BUCKET: S3.bucket,
    SOCRATES_S3_PREFIX: prefix,
    SOCRATES_S3_ACCESS_KEY_ID: S3.accessKeyID,
    SOCRATES_S3_SECRET_ACCESS_KEY: S3.secretAccessKey,
    SOCRATES_S3_PATH_STYLE: 'true',
    SOCRATES_S3_PASSPHRASE: S3.passphrase,
  };
}

// prefixFor is the prefix a data directory gets when the scenario does not
// name one.
export const prefixFor = (data) => 'e2e/' + basename(data) + '/';

export async function waitForHealth(url, deadlineMs = 25000) {
  const until = Date.now() + deadlineMs;
  while (Date.now() < until) {
    try {
      const res = await fetch(url + '/api/health');
      if (res.ok) return;
    } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 120));
  }
  throw new Error('the server never became healthy at ' + url);
}

// Ports 5000-5099 are this machine's budget for browser runs. Which of them
// is free is not something a counter can know: an interrupted run leaves a
// server behind, and a second suite on the same machine wants ports too. So
// each one is claimed by listening on it first, and a run with nothing free
// says so instead of quietly talking to somebody else's server.
const PORT_LOW = 5000;
const PORT_HIGH = 5099;
const claimed = new Set();

function canListen(port) {
  return new Promise((resolve) => {
    const probe = createSocketServer();
    probe.once('error', () => resolve(false));
    probe.listen(port, '127.0.0.1', () => probe.close(() => resolve(true)));
  });
}

export async function freePort() {
  for (let port = PORT_LOW; port <= PORT_HIGH; port += 1) {
    if (claimed.has(port)) continue;
    if (await canListen(port)) {
      claimed.add(port);
      return port;
    }
  }
  throw new Error('every port from ' + PORT_LOW + ' to ' + PORT_HIGH
    + ' is in use; something from an earlier run is still listening');
}

export function spawnServer({ data, port, live = false, env = {}, s3prefix }) {
  const { exe, bin } = binaries(live);
  const server = spawn(exe, ['-addr', '127.0.0.1:' + port, '-data', data], {
    env: {
      ...process.env,
      // The bucket comes before anything the scenario passes, so that a
      // scenario can still override a single variable. undefined is "this
      // data directory's own prefix", null is "no bucket at all".
      ...s3Env(s3prefix === undefined ? prefixFor(data) : s3prefix),
      // A live run must find the real CLIs, so the fakes stay off its PATH.
      PATH: (bin ? bin + ':' : '') + process.env.PATH,
      // Sessions get their working directory under the run's own data
      // directory. Without this the workspace root is derived from HOME, which
      // a live run does not override - and a real agent turned loose in
      // whatever HOME happens to be is both wrong and, when that directory
      // does not exist, an exec failure that reads as "claude: no such file or
      // directory".
      SOCRATES_WORKSPACE_ROOT: join(data, 'workspaces'),
      // Every CLI's own state goes under the run's data directory, so that a
      // fake run can neither read nor write the machine's real credentials,
      // transcripts or session databases - and so that a scenario can assert
      // on what the fakes wrote.
      ...(live ? {} : {
        HOME: data,
        CODEX_HOME: join(data, 'codex'),
        XDG_DATA_HOME: join(data, 'xdg'),
        CLAUDE_CONFIG_DIR: join(data, 'claude'),
        FAKE_LOG: join(data, 'fake.log'),
      }),
      ...env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  servers.add(server);
  server.on('exit', () => servers.delete(server));
  const log = [];
  server.stdout.on('data', (d) => log.push(String(d)));
  server.stderr.on('data', (d) => log.push(String(d)));
  const exited = new Promise((r) => server.on('exit', (code, sig) => r({ code, sig })));
  return { server, log, exited };
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// tmuxSock is the socket a run's server puts its tmux server on.
export function tmuxSock(data) { return join(data, 'tmux.sock'); }

// sessionsOn lists the tmux sessions still on a run's socket. A non-zero exit
// means there is no server at all, which is the good case and not an error.
export function sessionsOn(data) {
  try {
    const out = execFileSync('tmux', ['-S', tmuxSock(data), 'list-sessions', '-F', '#{session_name}'],
      { stdio: ['ignore', 'pipe', 'ignore'] }).toString();
    return out.split('\n').filter(Boolean);
  } catch {
    return [];
  }
}

// windowSize is what tmux itself says the window of a session is. It is the
// ground truth under the size policy: the browser is told the size, but only
// tmux knows what the window actually did.
export function windowSize(data, tmuxName) {
  try {
    return execFileSync('tmux', ['-S', tmuxSock(data), 'display-message', '-p', '-t', tmuxName,
      '#{window_width}x#{window_height}'], { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString().trim();
  } catch {
    return '';
  }
}

// killTmux is the backstop under the assertion: whatever was left behind does
// not stay on the machine.
export function killTmux(data) {
  try {
    execFileSync('tmux', ['-S', tmuxSock(data), 'kill-server'], { stdio: 'ignore' });
  } catch { /* no server is the good case */ }
}

// readFakeLog returns every launch the fake CLIs recorded, newest last.
export function readFakeLog(data) {
  try {
    return readFileSync(join(data, 'fake.log'), 'utf8')
      .split('\n').filter(Boolean).map((line) => JSON.parse(line));
  } catch {
    return [];
  }
}

// mockOpenRouter answers every OpenRouter call with one fixed transcription,
// so the dictation scenario needs no key and spends nothing.
//
// The pattern mirrors openrouter.DefaultBaseURL, which is
// https://openrouter.ai/api/v1 - one host, no subdomain. A glob that asked for
// a subdomain (`*.openrouter.ai`) would match nothing and let the request go
// to the real API, which either spends money or fails the scenario with a 401.
export async function mockOpenRouter(context, { text = 'hello from the microphone' } = {}) {
  await context.route('**/openrouter.ai/**', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ text, choices: [{ message: { content: text } }] }),
  }));
}

// DIGEST_MARKER is the sentence every digest instruction opens with
// (digestMarker in internal/server/digest.go).
export const DIGEST_MARKER = 'You read the terminal of a coding session for Home';

// isDigest says whether a completion request is one of Home's digests.
export function isDigest(json) {
  const first = json && Array.isArray(json.messages) && json.messages[0];
  return !!first && typeof first.content === 'string' && first.content.startsWith(DIGEST_MARKER);
}

// NOTHING_ASKED is the digest of a session that asks nothing.
const NOTHING_ASKED = JSON.stringify({ summary: 'Working in the e2e workspace.', context: '', question: null });

/**
 * openRouterStub is the same fixed answer, served over HTTP.
 *
 * The browser never calls OpenRouter: it posts the recording to
 * /api/voice/transcribe and the *server* calls the gateway. A Playwright route
 * cannot see that request, so a scenario that only mocked the browser would
 * either need a real key or fail on a 401. `openrouter.base_url` exists for
 * exactly this - config.go says so - and this is a gateway to point it at.
 *
 * It answers both routes Transcribe can take: /chat/completions for an audio
 * capable chat model and /audio/transcriptions for a dedicated one.
 *
 * `replies` is a queue for the one caller that needs a different answer every
 * time: the operator loop asks the model again after every step, and a stub
 * that repeated itself would drive the same keystroke until the run ran out of
 * steps. Each completion takes the next entry; when the queue is empty the
 * fixed `text` is the answer again, so a scenario only has to script the steps
 * it cares about.
 *
 * `delay` holds every answer back for that many milliseconds, for a scenario
 * that has to see what the page does while it waits. `refuse`, when set on
 * the returned stub, is an HTTP status every completion is refused with.
 *
 * Home digests every coding session on its own (internal/server/digest.go),
 * whatever a scenario is about, so a server with a key calls the gateway for
 * those too, at moments no scenario chooses. They are told apart by the
 * sentence every digest instruction opens with, kept in `digests` rather
 * than `calls`, and never take an entry of the queue: a scenario that counts
 * what it asked for, or scripts the operator's steps, counts and scripts
 * exactly that. Unless `answer` says otherwise a digest is told that nothing
 * is being asked.
 *
 * `answer` is for a scenario that is about those calls - Home's digests and
 * the answer operator - which arrive in an order no queue can predict. It is
 * handed each completion's request as JSON and returns the reply, or
 * undefined to leave it to the queue and `text`. Every call keeps its request
 * as `json`, so a scenario can read what the server asked. It may return a
 * promise, to hold one call back for as long as a scenario needs, and
 * `{ status, message }` refuses that one call with that HTTP status.
 */
export async function openRouterStub({ text = 'hello from the microphone', replies = [], delay = 0, answer = null } = {}) {
  const calls = [];
  const digests = [];
  const queue = [...replies];
  const stub = { calls, digests, text, replies: queue, refuse: 0 };
  const server = createServer((req, res) => {
    const chunks = [];
    req.on('data', (chunk) => { chunks.push(chunk); });
    req.on('end', () => setTimeout(async () => {
      const raw = Buffer.concat(chunks);
      let json = null;
      try { json = JSON.parse(raw.toString('utf8')); } catch { /* a recording, not JSON */ }
      const completion = String(req.url || '').includes('chat/completions');
      const digest = completion && isDigest(json);
      (digest ? digests : calls).push({ path: req.url, bytes: raw.length, json });
      if (completion && stub.refuse) {
        res.writeHead(stub.refuse, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ error: { message: 'refused by the e2e stub' } }));
        return;
      }
      let reply;
      if (completion && answer && json) {
        try { reply = await answer(json); } catch (err) { reply = 'the e2e stub could not answer: ' + err.message; }
      }
      // A refusal of this one call: { status, message }.
      if (reply && typeof reply === 'object' && reply.status) {
        res.writeHead(reply.status, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ error: { message: reply.message || 'refused by the e2e stub' } }));
        return;
      }
      if (reply === undefined && digest) reply = NOTHING_ASKED;
      if (reply === undefined) reply = completion && queue.length ? queue.shift() : text;
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ text: reply, choices: [{ message: { content: reply } }] }));
    }, delay));
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  stub.url = 'http://127.0.0.1:' + server.address().port;
  stub.close = () => new Promise((resolve) => server.close(resolve));
  return stub;
}

/**
 * start boots a server, a browser and a page. The caller completes setup with
 * setup(); every scenario ends by awaiting stop().
 */
export async function start(options = {}) {
  const live = !!options.live;
  const data = scratchDir('socrates-data-');
  mkdirSync(join(data, 'workspaces'), { recursive: true });
  const port = options.port || await freePort();
  const url = 'http://127.0.0.1:' + port;
  await ensureFakeS3();
  const s3prefix = options.s3prefix === undefined ? prefixFor(data) : options.s3prefix;
  const spawned = spawnServer({ data, port, live, env: options.env, s3prefix });
  await waitForHealth(url);

  const browser = await chromium.launch({
    executablePath: CHROME,
    // A dictation scenario needs a microphone, and this machine has none, so
    // it asks Chromium for its own: a generated tone, granted without a
    // prompt. Nothing else changes for the scenarios that do not ask.
    args: ['--no-sandbox', ...(options.args || [])],
  });
  const context = await browser.newContext({
    viewport: options.viewport || { width: 390, height: 844 },
    // A phone is a coarse pointer, and the key bar decides whether it is
    // wanted by asking. Without this the mobile package's own rule is never
    // the one under test, and every key is clicked rather than tapped.
    ...(options.touch ? { hasTouch: true, isMobile: true } : {}),
    // The app has one palette, and it is the light one. Saying so keeps a
    // machine whose Chromium defaults to dark from rendering anything else.
    colorScheme: options.colorScheme || 'light',
    permissions: options.permissions || [],
  });
  const page = await context.newPage();
  const errors = [];
  // Chrome's console text for a failed request carries no URL, so the location
  // is kept beside it: a 400 from /api/voice/speak is this machine having no
  // Google key, and a 400 from /messages would be a real defect wearing the
  // same words.
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return;
    const at = msg.location() || {};
    errors.push(msg.text() + (at.url ? ' @ ' + at.url : ''));
  });
  page.on('pageerror', (err) => errors.push('pageerror: ' + err.message));

  const s = {
    page, context, browser, url, port, data, errors, live, s3prefix,
    server: spawned.server, log: spawned.log, exited: spawned.exited,
  };

  // restart takes the server down with SIGTERM - which deliberately leaves the
  // agent hosts running - and brings it back on the same port and data dir.
  // `between` runs while it is down: a reboot, say, that takes tmux with it.
  s.restart = async (env = {}, between = null) => {
    s.server.kill('SIGTERM');
    const outcome = await Promise.race([s.exited, wait(9000).then(() => 'timeout')]);
    if (outcome === 'timeout') { s.server.kill('SIGKILL'); await s.exited; }
    if (between) await between();
    const again = spawnServer({ data, port, live, env: { ...(options.env || {}), ...env }, s3prefix });
    s.server = again.server; s.log = again.log; s.exited = again.exited;
    await waitForHealth(url);
    return outcome;
  };

  // stop is where the leak rule is enforced. Killing the server is not
  // enough: a Socrates restart deliberately leaves its tmux sessions running -
  // that is the feature the reattach scenario tests - so a scenario that only
  // kills the server leaks one tmux session per session it created. Deleting
  // the sessions is what closes them, and what is left is asserted rather than
  // merely swept, so a scenario that starts leaking fails instead of quietly
  // littering the machine.
  s.stop = async () => {
    const left = { sessions: 'not checked', tmux: ['not checked'] };
    try {
      await s.context.setOffline(false).catch(() => {});
      if (s.context.unrouteAll) await s.context.unrouteAll().catch(() => {});
      // The context's request object shares the page's cookies, so it is
      // already signed in - and it works when the page itself is wedged.
      const listed = await s.context.request.get(url + '/api/sessions');
      const sessions = (await listed.json()).sessions || [];
      for (const session of sessions) {
        await s.context.request.delete(url + '/api/sessions/' + encodeURIComponent(session.id)).catch(() => {});
      }
      const after = await s.context.request.get(url + '/api/sessions');
      left.sessions = ((await after.json()).sessions || []).length;
    } catch (err) {
      left.sessions = 'sweep failed: ' + (err && err.message);
    }
    await s.context.close().catch(() => {});
    await s.browser.close().catch(() => {});
    s.server.kill('SIGTERM');
    const gone = await Promise.race([s.exited, wait(8000).then(() => 'timeout')]);
    if (gone === 'timeout') s.server.kill('SIGKILL');
    // A pane that was closing as the server went down needs a moment to die.
    for (let i = 0; i < 20; i += 1) {
      left.tmux = sessionsOn(data);
      if (left.tmux.length === 0) break;
      await wait(250);
    }
    ok(left.sessions === 0, 'the scenario left no sessions behind', String(left.sessions));
    ok(left.tmux.length === 0, 'the scenario left no tmux session and no tmux server behind',
      left.tmux.length + ' sessions' + (left.tmux.length ? ' (' + left.tmux.join(',') + ')' : ''));
    // The backstop: whatever the assertion just found does not stay on the
    // machine.
    sweep(data);
  };

  return s;
}

// setup drives the setup page the way a person would, and lands on the session
// page.
export async function setup(page, url) {
  await page.goto(url + '/setup', { waitUntil: 'domcontentloaded' });
  await page.fill('#password', PASSWORD);
  await page.fill('#repeat', PASSWORD);
  await page.selectOption('#tunnelMode', 'off').catch(() => {});
  await page.click('#submit');
  await page.waitForFunction(() => !location.pathname.startsWith('/setup'), null, { timeout: 25000 });
  await page.goto(url + '/', { waitUntil: 'domcontentloaded' });
  // Attached rather than visible: on a phone it is in the modal drawer,
  // which is hidden until it is opened.
  await page.waitForSelector('#newSession', { state: 'attached', timeout: 15000 });
}

// On a phone the session list is a drawer and "New session" lives in it. This
// is the tap that gets to it, and a no-op on a window wide enough to show it.
export async function ensureNav(page) {
  const onScreen = () => page.evaluate(() => {
    const side = document.getElementById('sidebar');
    return side.getBoundingClientRect().left > -10;
  });
  for (let i = 0; i < 4; i += 1) {
    if (await onScreen()) return;
    await page.click('#menuBtn');
    await wait(320);
  }
  if (!(await onScreen())) throw new Error('the session list never came out');
}

export function shot(page, name) {
  mkdirSync(SHOTS, { recursive: true });
  return page.screenshot({ path: join(SHOTS, name + '.png'), fullPage: false });
}

// ------------------------------------------------------ assertions and runner

let current = null;
export const totals = { pass: 0, fail: 0, scenarios: [] };

// A quarantined scenario still runs and still prints every verdict; what it
// gives up is the power to fail the run. It is for a defect that is understood
// and reproduced but lives in a file this work package does not own, so the
// evidence has to be kept without blocking everything else. SOCRATES_E2E_STRICT
// takes the exemption away, which is what the fix should be checked against.
const STRICT = process.env.SOCRATES_E2E_STRICT === '1';

export function ok(condition, message, measured) {
  const quarantined = !!(current && current.quarantine) && !STRICT;
  const verdict = condition ? '  PASS ' : (quarantined ? '  FAIL (quarantined) ' : '  FAIL ');
  console.log(verdict + message + (measured === undefined ? '' : ' [' + measured + ']'));
  if (current) { current.pass += condition ? 1 : 0; current.fail += condition ? 0 : 1; }
  if (condition) totals.pass += 1;
  else {
    totals.fail += 1;
    if (!quarantined) process.exitCode = 1;
  }
}

/** scenario runs one named scenario and counts what it asserted. */
export async function scenario(name, title, fn, options = {}) {
  console.log('\n[' + name + '] ' + title);
  if (options.quarantine) console.log('  QUARANTINED: ' + options.quarantine);
  current = { name, pass: 0, fail: 0, skipped: false, quarantine: options.quarantine || null };
  totals.scenarios.push(current);
  const started = Date.now();
  try {
    await fn();
  } catch (err) {
    ok(false, 'the scenario ran to the end', 'threw: ' + (err && (err.stack || err.message || err)));
  }
  current.ms = Date.now() - started;
  console.log('  -- ' + current.pass + ' passed, ' + current.fail + ' failed, ' + current.ms + ' ms');
  current = null;
}

export function skipScenario(name, title, why) {
  console.log('\n[' + name + '] ' + title);
  console.log('  SKIP ' + why);
  totals.scenarios.push({ name, pass: 0, fail: 0, skipped: true, why, ms: 0 });
}

/** finish sweeps anything the assertions did not catch and prints the totals. */
export function finish() {
  cleanupBuild();
  console.log('\n' + '-'.repeat(66));
  for (const sc of totals.scenarios) {
    let detail = sc.skipped ? 'skipped: ' + sc.why : sc.pass + ' passed, ' + sc.fail + ' failed';
    if (sc.quarantine && !STRICT) detail += '  (quarantined, does not fail the run)';
    console.log('  ' + sc.name.padEnd(22) + detail);
  }
  console.log('-'.repeat(66));
  console.log(totals.scenarios.length + ' scenarios, ' + (totals.pass + totals.fail)
    + ' assertions, ' + totals.pass + ' passed, ' + totals.fail + ' failed');
  console.log('artefacts: ' + OUT);
  const held = totals.scenarios.filter((sc) => sc.quarantine && sc.fail > 0 && !STRICT);
  const heldFails = held.reduce((n, sc) => n + sc.fail, 0);
  if (process.exitCode) console.log('\nSOME SCENARIOS FAILED');
  else if (held.length) {
    console.log('\nALL SCENARIOS PASSED, except ' + heldFails + ' quarantined assertion(s) in '
      + held.map((sc) => sc.name).join(', ') + ' - a known defect, not a green run');
  } else console.log('\nALL SCENARIOS PASSED');
}

export { wait };
