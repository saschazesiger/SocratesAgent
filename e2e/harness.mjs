// The browser harness: one real `socrates` binary, one real server with its
// agent hosts, real tmux for the Shell, and Playwright driving the page.
//
// One thing has to be arranged before a browser can see a session: the CLIs.
// A suite that needed three logged-in accounts would not be a suite, so the
// scripted CLIs - e2e/fakebin/fakeclaude, fakecodex and fakeopencode, which
// speak each agent's own protocol - are built once and put on PATH under the
// names the chats run. The Shell needs no fake at all: /bin/sh is the thing
// under test.
//
// With SOCRATES_LIVE_AGENTS=1 a scenario can ask for `live: true` instead, and
// then the fakes stay off PATH and the real, logged-in CLIs are used.

import { chromium } from '/opt/browser-testing/node_modules/playwright-core/index.mjs';
import { createServer } from 'node:http';
import { createServer as createSocketServer } from 'node:net';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, realpathSync, rmSync, mkdirSync, readdirSync, existsSync } from 'node:fs';
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
  killHosts(dir);
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

// The three scripted CLIs, built once per run under the names the chats
// run, and `socrates` itself, built plainly.
function buildFakes() {
  const dir = scratchDir('socrates-e2e-');
  const bin = join(dir, 'bin');
  mkdirSync(bin);
  for (const [name, pkg] of [['claude', 'fakeclaude'], ['codex', 'fakecodex'], ['opencode', 'fakeopencode']]) {
    execFileSync('go', ['build', '-o', join(bin, name), './e2e/fakebin/' + pkg], { cwd: REPO });
  }
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
// Socrates runs without a bucket: the data directory is its only store, and
// S3 holds a backup of it. Only the scenarios about that backup want one, so
// e2e/fakebin/fakes3 - the in-memory store the backup's unit tests use
// (internal/s3/s3fake), served on a real port - is started the first time one
// asks for it, and each of them backs up under a prefix of its own.
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
// request fail until 'up'. Only the backup scenario breaks the bucket on
// purpose, and it puts it back in a finally.
export async function fakeS3Switch(what) {
  const url = await ensureFakeS3();
  const res = await fetch(url + '/_fakes3/' + what, { method: 'POST' });
  if (res.status !== 204) throw new Error('fakes3 refused ' + what + ': ' + res.status);
}

// backupForm is the Backup card's (and the restore form's) fields for a
// prefix on the fake, as the API takes them.
export async function backupForm(prefix) {
  const url = await ensureFakeS3();
  return {
    endpoint: url, region: S3.region, bucket: S3.bucket, prefix,
    access_key_id: S3.accessKeyID, secret_access_key: S3.secretAccessKey,
    path_style: true, passphrase: S3.passphrase, interval_minutes: 10,
  };
}

// prefixFor is the backup prefix of a data directory.
export const prefixFor = (data) => 'e2e/' + basename(data) + '/';

// STARTUP is how long a server may take to answer its first health check.
export const STARTUP = 25000;

/**
 * waitForHealth waits until the server at url answers its health check.
 *
 * Handed the server it started (`spawned`), it also waits until that server
 * says it is listening there, and gives up as soon as it has exited: another
 * suite on this machine - another worktree's, say - may hold the port, and
 * then a healthy answer is that suite's server, not this one. An error for a
 * port that was taken says so (`taken`), so a start can try another one.
 */
export async function waitForHealth(url, deadlineMs = STARTUP, spawned = null) {
  const until = Date.now() + deadlineMs;
  const mine = spawned ? 'open ' + url + ' in your browser' : '';
  while (Date.now() < until) {
    if (spawned && (spawned.server.exitCode !== null || spawned.server.signalCode !== null)) {
      const said = oneLog(spawned.log);
      const err = new Error('the server at ' + url + ' exited before it served: ' + said);
      err.taken = /address already in use/.test(said);
      throw err;
    }
    if (!spawned || spawned.log.join('').includes(mine)) {
      // Each look is bounded: one request that hangs must not spend the
      // whole wait.
      try {
        const res = await fetch(url + '/api/health', { signal: AbortSignal.timeout(3000) });
        if (res.ok) return;
      } catch { /* not up yet */ }
    }
    await new Promise((r) => setTimeout(r, 120));
  }
  throw new Error('the server never became healthy at ' + url + ' in ' + Math.round(deadlineMs / 1000) + ' s');
}

// Ports 5000-5099 are this machine's budget for browser runs. Which of them
// is free is not something a counter can know: an interrupted run leaves a
// server behind, and a second suite on the same machine wants ports too. So
// each one is claimed by listening on it first, and a run with nothing free
// says so instead of quietly talking to somebody else's server.
const PORT_LOW = 5000;
const PORT_HIGH = 5099;
const claimed = new Set();
// Where this run starts looking. Another suite on the machine walks the same
// range, and two that both start at the bottom take the same ports at the
// same moments; one that loses the race finds out from its server's own log
// (waitForHealth) and moves on.
const PORT_FIRST = process.pid % (PORT_HIGH - PORT_LOW + 1);

function canListen(port) {
  return new Promise((resolve) => {
    const probe = createSocketServer();
    probe.once('error', () => resolve(false));
    probe.listen(port, '127.0.0.1', () => probe.close(() => resolve(true)));
  });
}

// The ports of the range that the Fetch standard calls bad (SIP's): Node's
// fetch and the browser both refuse to talk to them, so a server there is
// never reached, however healthy it is.
const BAD_PORTS = new Set([5060, 5061]);

export async function freePort() {
  for (let i = 0; i <= PORT_HIGH - PORT_LOW; i += 1) {
    const port = PORT_LOW + ((PORT_FIRST + i) % (PORT_HIGH - PORT_LOW + 1));
    if (claimed.has(port) || BAD_PORTS.has(port)) continue;
    if (await canListen(port)) {
      claimed.add(port);
      return port;
    }
  }
  throw new Error('every port from ' + PORT_LOW + ' to ' + PORT_HIGH
    + ' is in use; something from an earlier run is still listening');
}

// ambient is the suite's own environment without what names an agent's
// folders or carries a credential: a fake run must never find the machine's
// real Claude Code, Codex or OpenCode state through a variable the scenario
// did not set, and must never hand one of this machine's keys to a child.
function ambient(live) {
  const out = { ...process.env };
  if (live) return out;
  for (const name of Object.keys(out)) {
    if (/^(ANTHROPIC_|CLAUDE_CODE_|OPENCODE_|SOCRATES_S3_)/.test(name)
      || ['OPENAI_API_KEY', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_STATE_HOME'].includes(name)) {
      delete out[name];
    }
  }
  return out;
}

// pathFirst is a directory a scenario puts in front of the fakes - a stub
// tmux - and never instead of them: a PATH of its own in env would hand the
// server the machine's real CLIs, which a fake run must never find.
export function spawnServer({ data, port, live = false, env = {}, pathFirst = '' }) {
  if ('PATH' in env) throw new Error('a scenario puts a directory on PATH with pathFirst, never a PATH of its own');
  const { exe, bin } = binaries(live);
  const server = spawn(exe, ['-addr', '127.0.0.1:' + port, '-data', data], {
    env: {
      ...ambient(live),
      // The environment the suite was started from must not hand a server
      // the bucket of the mirror Socrates once had, which a start would
      // carry over into its backup.json.
      SOCRATES_S3_BUCKET: '',
      // A live run must find the real CLIs, so the fakes stay off its PATH.
      PATH: [pathFirst, bin, process.env.PATH].filter(Boolean).join(':'),
      // Sessions get their working directory under the run's own data
      // directory. Without this the workspace root is derived from HOME, which
      // a live run does not override - and a real agent turned loose in
      // whatever HOME happens to be is both wrong and, when that directory
      // does not exist, an exec failure that reads as "claude: no such file or
      // directory".
      SOCRATES_WORKSPACE_ROOT: join(data, 'workspaces'),
      // Every CLI's own state goes under the run's data directory, so that a
      // fake run can neither read nor write the machine's real credentials,
      // transcripts or session databases. (A chat's agent gets its account's
      // folders from the server on top of these.)
      ...(live ? {} : {
        HOME: data,
        CODEX_HOME: join(data, 'codex'),
        XDG_DATA_HOME: join(data, 'xdg'),
        CLAUDE_CONFIG_DIR: join(data, 'claude'),
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

// oneLog is the end of a server's log on one line.
const oneLog = (log) => log.join('').replace(/\s+/g, ' ').slice(-1500);

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

// agentProcessesOf lists the agent hosts still running for a run's data
// directory, and the CLIs under them: a chat's host is started in a session
// of its own and outlives its server by design, so neither killing the server
// nor deleting the folder ends it.
export function agentProcessesOf(data) {
  let out = '';
  try {
    out = execFileSync('ps', ['-eo', 'pid=,ppid=,args='], { stdio: ['ignore', 'pipe', 'ignore'] }).toString();
  } catch { return []; }
  const procs = out.split('\n').map((line) => /^\s*(\d+)\s+(\d+)\s+(.*)$/.exec(line)).filter(Boolean)
    .map(([, pid, ppid, args]) => ({ pid: Number(pid), ppid: Number(ppid), args }));
  const hosts = procs.filter((p) => / agent-host /.test(' ' + p.args + ' ')
    && (' ' + p.args + ' ').includes(' --data ' + data + ' '));
  const ids = new Set(hosts.map((p) => p.pid));
  return [...hosts, ...procs.filter((p) => ids.has(p.ppid))];
}

// killHosts is the same backstop for the agent hosts.
function killHosts(data) {
  for (const p of agentProcessesOf(data)) {
    try { process.kill(p.pid, 'SIGKILL'); } catch { /* already gone */ }
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
 * `replies` is a queue for a caller that needs a different answer each time
 * (a chat named again gets a new name). Each completion takes the next entry;
 * when the queue is empty the fixed `text` is the answer again, so a scenario
 * only has to script the answers it cares about.
 *
 * `delay` holds every answer back for that many milliseconds, for a scenario
 * that has to see what the page does while it waits. `refuse`, when set on
 * the returned stub, is an HTTP status every completion is refused with.
 *
 * `answer` is for a scenario whose calls arrive in an order no queue can
 * predict - a chat's name and the verdict on its turn, asked for by the same
 * server at moments no scenario chooses. It is handed each completion's
 * request as JSON and returns the reply, or undefined to leave it to the
 * queue and `text`. Every call keeps its request
 * as `json`, so a scenario can read what the server asked. It may return a
 * promise, to hold one call back for as long as a scenario needs, and
 * `{ status, message }` refuses that one call with that HTTP status.
 */
export async function openRouterStub({ text = 'hello from the microphone', replies = [], delay = 0, answer = null } = {}) {
  const calls = [];
  const queue = [...replies];
  const stub = { calls, text, replies: queue, refuse: 0 };
  const server = createServer((req, res) => {
    const chunks = [];
    req.on('data', (chunk) => { chunks.push(chunk); });
    req.on('end', () => setTimeout(async () => {
      const raw = Buffer.concat(chunks);
      let json = null;
      try { json = JSON.parse(raw.toString('utf8')); } catch { /* a recording, not JSON */ }
      const completion = String(req.url || '').includes('chat/completions');
      calls.push({ path: req.url, bytes: raw.length, json });
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
      if (reply === undefined) reply = completion && queue.length ? queue.shift() : text;
      // A chat's Status asks for an analysis object (chatstatus.go); a
      // stub told what to say says it as every part of one.
      const system = json && Array.isArray(json.messages) && json.messages[0] ? String(json.messages[0].content || '') : '';
      if (completion && system.startsWith('You analyse a coding session') && typeof reply === 'string' && !reply.trim().startsWith('{')) {
        reply = JSON.stringify({ goal: 'The task', now: reply, remaining: [], progress: 100, eta_minutes: null,
          eta_reason: 'Nothing is left to do.', spoken: reply });
      }
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
  let port = options.port || await freePort();
  let url = 'http://127.0.0.1:' + port;
  let spawned = null;
  for (let attempt = 1; ; attempt += 1) {
    spawned = spawnServer({ data, port, live, env: options.env, pathFirst: options.pathFirst });
    const failed = await waitForHealth(url, STARTUP, spawned).then(() => null, (err) => err);
    if (!failed) break;
    // A server that never served is not left running for the rest of the
    // run, where its pipes would keep the run from ever ending.
    spawned.server.kill('SIGKILL');
    if (failed.taken && !options.port && attempt < 5) {
      port = await freePort();
      url = 'http://127.0.0.1:' + port;
      continue;
    }
    // A server that never answered says why in its own log, and the run is
    // the only place that log is ever seen.
    throw new Error(failed.message + '; its log ends: ' + oneLog(spawned.log));
  }

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
    page, context, browser, url, port, data, errors, live,
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
    const again = spawnServer({ data, port, live, env: { ...(options.env || {}), ...env }, pathFirst: options.pathFirst });
    s.server = again.server; s.log = again.log; s.exited = again.exited;
    await waitForHealth(url, STARTUP, again);
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
    // A server that went away by itself took the reason with it into its
    // log, which nothing else ever shows.
    if (s.server.exitCode !== null || s.server.signalCode !== null) {
      console.log('  NOTE the server had exited (' + (s.server.signalCode || s.server.exitCode)
        + ') before the scenario stopped it; its log ends: ' + oneLog(s.log));
    }
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
    // A deleted chat's host is stopped by the server before it answers; one
    // still running is a leak, which the sweep below then ends.
    let hosts = agentProcessesOf(data);
    for (let i = 0; i < 20 && hosts.length; i += 1) {
      await wait(250);
      hosts = agentProcessesOf(data);
    }
    ok(hosts.length === 0, 'the scenario left no agent host behind',
      hosts.length + ' processes' + (hosts.length ? ' (' + hosts.map((p) => p.pid + ' ' + p.args.split(' ').slice(0, 2).join(' ')).join(', ') + ')' : ''));
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
//
// A drawer that is open is waited for, never tapped again: under load it is
// still sliding in when a fixed pause has passed, its panel is over the menu
// button by then, and a second tap lands in the panel and never comes back.
export async function ensureNav(page) {
  const onScreen = () => document.getElementById('sidebar').getBoundingClientRect().left > -10;
  for (let i = 0; i < 3; i += 1) {
    if (await page.evaluate(onScreen)) return;
    if (!(await page.evaluate(() => document.getElementById('app').hasAttribute('data-drawer-open')))) {
      await page.click('#menuBtn');
    }
    if (await page.waitForFunction(onScreen, null, { timeout: 10000 }).then(() => true, () => false)) return;
  }
  throw new Error('the session list never came out');
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
  // A server still running here belongs to a scenario that is over; left
  // alone, its pipes would keep the run from ending. The fake bucket is the
  // run's own, and cleanupBuild stops it.
  for (const child of servers) {
    if (fakeS3 && child === fakeS3.child) continue;
    console.log('NOTE a server was still running at the end of the run (' + child.spawnargs.slice(1).join(' ') + ')');
    try { child.kill('SIGKILL'); } catch { /* already gone */ }
  }
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
