// The browser harness: one real `socrates` binary, one real server, one real
// detached agent host per chat, and Playwright driving the page.
//
// Two things have to be arranged before a browser can see a turn.
//
//   * The turns. The three protocol adapters talk to real CLIs, and a suite
//     that needed three logged-in accounts would not be a suite. So the three
//     agent ids are pointed at `internal/agenthost/hosttest` - the scripted,
//     in-process adapter - by a Go file that never enters the worktree:
//     `go build -overlay` maps it in for the length of one build. A hard kill
//     of this process cannot leave a stray file behind, which a generated
//     source file in the tree could.
//   * The catalogue. `/api/agents` reports an agent as installed only when its
//     binary answers `--version`, so the three fake CLIs from
//     `internal/harness/fakes` go on PATH under their real names. That is what
//     the new-chat sheet and the dashboard's Agents card are drawn from.
//
// With SOCRATES_LIVE_AGENTS=1 a scenario can ask for `live: true` instead, and
// then it gets a plain build with no overlay and no fakes on PATH: the real
// adapters against the real, logged-in CLI.

import { chromium } from '/opt/browser-testing/node_modules/playwright-core/index.mjs';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync, mkdirSync, symlinkSync } from 'node:fs';
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

// The turn every scenario gets unless it asks for another: a sentence, some
// reasoning, a command, a subagent, a notice, a closing question and the
// numbers. It is replayed identically on every turn of the chat.
export const SCRIPT = JSON.stringify([
  { do: 'text', text: 'Let me look at the tests first.' },
  { do: 'sleep', ms: 700 },
  { do: 'reason', text: 'The failing test is in the store package.' },
  { do: 'tool', name: 'Bash', input: 'go test ./...', output: 'ok  github.com/example/store\n' },
  { do: 'sleep', ms: 700 },
  { do: 'subagent', input: 'check the docs', output: 'nothing to change' },
  { do: 'notice', text: 'The model was restarted once.' },
  { do: 'text', text: 'The tests pass. Shall I commit this?' },
  { do: 'usage' },
  { do: 'end', outcome: 'ok' },
]);

// A longer turn, for the scenarios that have to interrupt one: four cards with
// real gaps between them, and a tool that exits non-zero.
export const LONG_SCRIPT = JSON.stringify([
  { do: 'text', text: 'Let me look at the tests first.' },
  { do: 'sleep', ms: 1200 },
  { do: 'tool', name: 'Bash', input: 'go test ./...', output: 'ok  github.com/example/store\n' },
  { do: 'sleep', ms: 1500 },
  { do: 'text', text: 'One more thing to check.' },
  { do: 'sleep', ms: 1500 },
  { do: 'subagent', input: 'check the docs', output: 'nothing to change' },
  { do: 'sleep', ms: 1500 },
  { do: 'tool', name: 'Bash', input: 'exit 3', output: 'boom\n', exit: 3 },
  { do: 'text', text: 'The tests pass. Shall I commit this?' },
  { do: 'usage' },
  { do: 'end', outcome: 'ok' },
]);

// The overlay source. The wrapper exists for one scenario: hosttest's "tool"
// step opens and closes its card in the same breath, so the only way to see a
// tool card that is still running when Stop is pressed is to drop the
// tool_finished of one named tool on its way out of the adapter.
const ADAPTER_SRC = `// Built only by the e2e suite, through \`go build -overlay\`: this file never
// exists in the repository. It points the three agent ids at the scripted test
// adapter, so a browser run gets real turns out of a real agent-host process
// without needing three logged-in CLIs.
package main

import (
	"os"
	"sync"

	_ "github.com/saschazesiger/SocratesAgent/internal/agenthost/hosttest"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// e2eWrap can drop the tool_finished of one named tool, which leaves its card
// open until something else closes it.
type e2eWrap struct {
	harness.Adapter
	drop string
	once sync.Once
	out  chan harness.Event
}

func (w *e2eWrap) Events() <-chan harness.Event {
	w.once.Do(func() {
		w.out = make(chan harness.Event, 64)
		in := w.Adapter.Events()
		go func() {
			for ev := range in {
				if w.drop != "" && ev.Kind == harness.KindToolFinished && ev.Tool != nil && ev.Tool.Name == w.drop {
					continue
				}
				w.out <- ev
			}
			close(w.out)
		}()
	})
	return w.out
}

func init() {
	base, ok := harness.Get("test")
	if !ok {
		panic("the scripted adapter is not registered")
	}
	for _, id := range []string{"claude", "codex", "opencode"} {
		d, found := harness.Get(id)
		if !found {
			continue
		}
		d.New = func() harness.Adapter {
			return &e2eWrap{Adapter: base.New(), drop: os.Getenv("SOCRATES_E2E_DROP_FINISH")}
		}
		harness.Register(d)
	}
}
`;

function buildScripted() {
  const dir = mkdtempSync(join(tmpdir(), 'socrates-e2e-'));
  const bin = join(dir, 'bin');
  mkdirSync(bin);
  for (const name of ['claude', 'codex', 'opencode']) {
    execFileSync('go', ['build', '-o', join(bin, name), './internal/harness/fakes/fake' + name], { cwd: REPO });
  }
  const src = join(dir, 'e2e_adapter.go');
  writeFileSync(src, ADAPTER_SRC);
  const overlay = join(dir, 'overlay.json');
  writeFileSync(overlay, JSON.stringify({ Replace: { [join(REPO, 'zz_e2e_adapter.go')]: src } }));
  execFileSync('go', ['build', '-overlay', overlay, '-o', join(dir, 'socrates'), '.'], { cwd: REPO });
  return { dir, bin, exe: join(dir, 'socrates') };
}

// The live build is the shipped one: no overlay, no fakes, the real adapters.
function buildLive() {
  const dir = mkdtempSync(join(tmpdir(), 'socrates-e2e-live-'));
  execFileSync('go', ['build', '-o', join(dir, 'socrates'), '.'], { cwd: REPO });
  return { dir, bin: null, exe: join(dir, 'socrates') };
}

const builds = {};
export function binaries(live = false) {
  const key = live ? 'live' : 'scripted';
  if (!builds[key]) builds[key] = live ? buildLive() : buildScripted();
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

export function spawnServer({ data, port, script, live = false, env = {} }) {
  const { exe, bin } = binaries(live);
  const server = spawn(exe, ['-addr', '127.0.0.1:' + port, '-data', data], {
    env: {
      ...process.env,
      // A live run must find the real CLIs, so the fakes stay off its PATH.
      PATH: (bin ? bin + ':' : '') + process.env.PATH,
      SOCRATES_TEST_SCRIPT: script || SCRIPT,
      SOCRATES_PIPER_DIR: VOICE_CACHE,
      // Chats get their working directory under the run's own data directory.
      // Without this the workspace root is derived from HOME, which a live run
      // does not override - and a real agent turned loose in whatever HOME
      // happens to be is both wrong and, when that directory does not exist,
      // an exec failure that reads as "claude: no such file or directory".
      SOCRATES_WORKSPACE_ROOT: join(data, 'workspaces'),
      // HOME points at the data directory so a scripted run cannot read or
      // write the machine's real agent credentials. A live run needs them.
      ...(live ? {} : { HOME: data }),
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

// hostsFor lists the agent-host processes still serving one data directory.
function hostsFor(data) {
  try {
    const out = execFileSync('pgrep', ['-f', '--', 'agent-host --dir ' + join(data, 'agents')]).toString();
    return out.split('\n').filter(Boolean);
  } catch {
    return []; // pgrep exits 1 when nothing matches, which is the good case
  }
}

function pkill(pattern) {
  try {
    execFileSync('pkill', ['-f', '--', pattern]);
  } catch { /* nothing left to kill is the good case */ }
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
  const spawned = spawnServer({ data, port, script: options.script, live, env: options.env });
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
    const again = spawnServer({ data, port, script: options.script, live, env: { ...(options.env || {}), ...env } });
    s.server = again.server; s.log = again.log; s.exited = again.exited;
    await waitForHealth(url);
    return outcome;
  };

  // stop is where §10.3's rule is enforced. Killing the server is not enough:
  // SIGTERM deliberately detaches the agent hosts (that is the feature the
  // restart scenario tests), so a scenario that only kills `socrates serve`
  // leaks one host process per chat it created. Deleting the chats is what
  // closes them - and the count is asserted, not merely swept, so a scenario
  // that starts leaking fails instead of quietly littering the machine.
  s.stop = async () => {
    const left = { chats: 'not checked', hosts: ['not checked'] };
    try {
      await s.context.setOffline(false).catch(() => {});
      if (s.context.unrouteAll) await s.context.unrouteAll().catch(() => {});
      // The context's request object shares the page's cookies, so it is
      // already signed in - and it works when the page itself is wedged.
      const listed = await s.context.request.get(url + '/api/chats?scope=all');
      const chats = (await listed.json()).chats || [];
      for (const chat of chats) {
        await s.context.request.delete(url + '/api/chats/' + encodeURIComponent(chat.id)).catch(() => {});
      }
      const after = await s.context.request.get(url + '/api/chats?scope=all');
      left.chats = ((await after.json()).chats || []).length;
    } catch (err) {
      left.chats = 'sweep failed: ' + (err && err.message);
    }
    await s.context.close().catch(() => {});
    await s.browser.close().catch(() => {});
    s.server.kill('SIGTERM');
    const gone = await Promise.race([s.exited, wait(8000).then(() => 'timeout')]);
    if (gone === 'timeout') s.server.kill('SIGKILL');
    // A host that was closing as the server went down needs a moment to die.
    for (let i = 0; i < 20; i += 1) {
      left.hosts = hostsFor(data);
      if (left.hosts.length === 0) break;
      await wait(250);
    }
    ok(left.chats === 0, 'the scenario left no chats behind', String(left.chats));
    ok(left.hosts.length === 0, 'the scenario left no agent-host process behind',
      left.hosts.length + ' processes' + (left.hosts.length ? ' (' + left.hosts.join(',') + ')' : ''));
    // The backstop: whatever the assertion just found, it does not stay on the
    // machine. Twice, because a host that was still starting when its server
    // was killed is not in the process table on the first pass.
    for (let i = 0; i < 2; i += 1) {
      pkill('agent-host --dir ' + join(data, 'agents'));
      await wait(250);
    }
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
  for (const key of Object.keys(builds)) pkill(builds[key].exe + ' agent-host');
  pkill('agent-host --dir /tmp/socrates-data-');
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
