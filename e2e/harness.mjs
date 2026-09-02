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
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, mkdirSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
export const REPO = dirname(HERE);

// Everything this suite writes goes under e2e/out/, which is gitignored: the
// worktree is what gets committed and a stray artefact directory there is one
// `git add -A` away from being part of the change.
export const OUT = join(HERE, 'out');
export const SHOTS = join(OUT, 'shots');
// Piper installs itself into <dataDir>/voice, and every scenario gets a fresh
// data directory - which would mean a 25 MB download per server start. One
// cache, shared by every run on this machine, symlinked into place.
const VOICE_CACHE = join(OUT, 'voice-cache');

const CHROME = process.env.SOCRATES_E2E_CHROME
  || '/root/.cache/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell';

export const PASSWORD = 'socrates-e2e';
export const LIVE = process.env.SOCRATES_LIVE_AGENTS === '1';

// The three fake CLIs are one program under three names, built once per run.
// The Go-overlay trick the old suite used is gone with the adapter registry it
// pointed at: `socrates` is now built plainly.
function buildFakes() {
  const dir = mkdtempSync(join(tmpdir(), 'socrates-e2e-'));
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
  const dir = mkdtempSync(join(tmpdir(), 'socrates-e2e-live-'));
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
  for (const key of Object.keys(builds)) {
    rmSync(builds[key].dir, { recursive: true, force: true });
    delete builds[key];
  }
}

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

// Ports 5000-5099 are this machine's budget for browser runs.
let nextPort = 5000;

export function spawnServer({ data, port, live = false, env = {} }) {
  const { exe, bin } = binaries(live);
  const server = spawn(exe, ['-addr', '127.0.0.1:' + port, '-data', data], {
    env: {
      ...process.env,
      // A live run must find the real CLIs, so the fakes stay off its PATH.
      PATH: (bin ? bin + ':' : '') + process.env.PATH,
      SOCRATES_PIPER_DIR: VOICE_CACHE,
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

/**
 * start boots a server, a browser and a page. The caller completes setup with
 * setup(); every scenario ends by awaiting stop().
 */
export async function start(options = {}) {
  const live = !!options.live;
  mkdirSync(VOICE_CACHE, { recursive: true });
  const data = mkdtempSync(join(tmpdir(), 'socrates-data-'));
  mkdirSync(join(data, 'workspaces'), { recursive: true });
  try { symlinkSync(VOICE_CACHE, join(data, 'voice')); } catch { /* best effort */ }
  const port = options.port || nextPort++;
  const url = 'http://127.0.0.1:' + port;
  const spawned = spawnServer({ data, port, live, env: options.env });
  await waitForHealth(url);

  const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox'] });
  const context = await browser.newContext({
    viewport: options.viewport || { width: 390, height: 844 },
    // The app has one palette, and it is the light one. Saying so keeps a
    // machine whose Chromium defaults to dark from rendering anything else.
    colorScheme: options.colorScheme || 'light',
    permissions: [],
  });
  const page = await context.newPage();
  const errors = [];
  // Chrome's console text for a failed request carries no URL, so the location
  // is kept beside it: a 503 from /api/voice/speak is this machine having no
  // Piper, and a 503 from /messages would be a real defect wearing the same
  // words.
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
  s.restart = async (env = {}) => {
    s.server.kill('SIGTERM');
    const outcome = await Promise.race([s.exited, wait(9000).then(() => 'timeout')]);
    if (outcome === 'timeout') { s.server.kill('SIGKILL'); await s.exited; }
    const again = spawnServer({ data, port, live, env: { ...(options.env || {}), ...env } });
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
      const listed = await s.context.request.get(url + '/api/sessions?scope=all');
      const sessions = (await listed.json()).sessions || [];
      for (const session of sessions) {
        await s.context.request.delete(url + '/api/sessions/' + encodeURIComponent(session.id)).catch(() => {});
      }
      const after = await s.context.request.get(url + '/api/sessions?scope=all');
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
    killTmux(data);
    rmSync(data, { recursive: true, force: true });
  };

  return s;
}

// setup drives the setup page the way a person would, and lands on the chat.
export async function setup(page, url) {
  await page.goto(url + '/setup', { waitUntil: 'domcontentloaded' });
  await page.fill('#password', PASSWORD);
  await page.fill('#repeat', PASSWORD);
  await page.selectOption('#tunnelMode', 'off').catch(() => {});
  await page.click('#submit');
  await page.waitForFunction(() => !location.pathname.startsWith('/setup'), null, { timeout: 25000 });
  await page.goto(url + '/', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#newChat', { timeout: 15000 });
}

// On a phone the chat list is a drawer and "New chat" lives in it. This is the
// tap that gets to it, and a no-op on a window wide enough to show it.
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
  if (!(await onScreen())) throw new Error('the chat list never came out');
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
