// The README screenshots, taken from the real running app through the same
// harness the suite uses.
//
//   node e2e/shots.mjs
//
// It writes docs/screenshot-session.png, docs/screenshot-phone.png and
// docs/screenshot-admin.png, and nothing else - docs/screenshot-tunnel.png is
// hand-made and stays where it is. It is not part of `make e2e`: the pictures
// are committed, so this is only run when they need redoing.
//
// The sessions are the suite's fake CLI wearing the three names, because a
// picture of the interface should show the same thing every time it is taken
// rather than whatever a real model happened to say that afternoon. What is
// real is everything around it: real tmux panes, a real WebSocket, the real
// white terminal.

import { execFileSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { start, setup, wait, ensureNav, REPO, cleanupBuild } from './harness.mjs';

const DOCS = join(REPO, 'docs');
const DESKTOP = { width: 1280, height: 720 };
const PHONE = { width: 390, height: 844 };

// The dashboard picture is of what Socrates found on the machine, so it is
// taken against the real CLIs when they are installed: a screenshot showing
// three fake binaries in a temporary directory is a picture of the test
// harness, not of the product. With no CLIs installed it falls back to the
// fakes. It never starts a session with them - the dashboard is a page about
// what is installed, and nothing on it runs a model.
const REAL_CLIS = ['claude', 'codex', 'opencode'].every((name) => {
  try { execFileSync('which', [name], { stdio: 'ignore' }); return true; } catch { return false; }
});

// The pictures are taken with the DOM renderer. It draws the same terminal as
// the shipped WebGL one, and this machine's headless Chromium has only a
// software WebGL context, which can hand back an empty canvas.
async function domRenderer(s) {
  await s.context.request.put(s.url + '/api/settings', {
    data: { settings: { terminal: { webgl: false } } },
  });
}

async function newSession(page, harness) {
  const was = await page.evaluate(() => location.hash.slice(1));
  // On a phone the list is a drawer, and the button that starts a session is
  // inside it.
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.waitForSelector('#nsHarness .seg[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#nsHarness .seg[data-value="' + harness + '"]');
  await page.click('#nsStart');
  await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
  await page.waitForFunction((before) => location.hash.length > 1 && location.hash.slice(1) !== before,
    was, { timeout: 30000 });
  await page.waitForSelector('#term .xterm', { timeout: 20000 });
  return page.evaluate(() => location.hash.slice(1));
}

async function typeLine(page, text) {
  await page.click('#term .xterm-screen');
  await page.waitForFunction(() => document.activeElement
    === document.querySelector('#term .xterm-helper-textarea'), null, { timeout: 5000 }).catch(() => {});
  await page.keyboard.type(text, { delay: 8 });
  await page.keyboard.press('Enter');
}

async function seen(page, text, timeout = 20000) {
  await page.waitForFunction((want) => {
    const rows = document.querySelector('#term .xterm-rows');
    return !!rows && rows.innerText.includes(want);
  }, text, { timeout }).catch(() => {});
}

async function rename(s, id, title) {
  await s.context.request.patch(s.url + '/api/sessions/' + id, { data: { title } });
  await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
  await s.page.waitForFunction((want) =>
    [...document.querySelectorAll('#sessionList .chat-item .label')].some((n) => n.textContent === want)
    || (document.getElementById('sessionTitle') || {}).textContent === want,
  title, { timeout: 20000 }).catch(() => {});
  // The header carries the name of the session that is open, and it is redrawn
  // from the list; on a phone that is the only place the name is visible.
  await s.page.waitForFunction((want) =>
    (document.getElementById('sessionTitle') || {}).textContent === want,
  title, { timeout: 10000 }).catch(() => {});
}

// The desktop picture: a sidebar with a session per program, a Claude Code
// pane open, and a turn in it.
async function sessionShot() {
  const s = await start({ viewport: DESKTOP });
  try {
    await setup(s.page, s.url);
    await domRenderer(s);
    await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newSession', { timeout: 15000 });

    const shell = await newSession(s.page, 'shell');
    await rename(s, shell, 'socrates · make check');
    const codex = await newSession(s.page, 'codex');
    await rename(s, codex, 'Codex · the flaky store test');
    const claude = await newSession(s.page, 'claude');
    await rename(s, claude, 'Claude Code · the retention window');

    await typeLine(s.page, 'the retention window rounds down; fix it and run the store tests');
    await seen(s.page, 'you said:');
    await typeLine(s.page, 'now run go test ./internal/store/ and show me the failure');
    await seen(s.page, 'show me the failure');
    await wait(600);

    mkdirSync(DOCS, { recursive: true });
    await s.page.screenshot({ path: join(DOCS, 'screenshot-session.png') });
    console.log('wrote docs/screenshot-session.png (' + DESKTOP.width + 'x' + DESKTOP.height + ')');
  } finally { await s.stop(); }
}

// The phone picture: the same terminal at 390x844, with the key bar and the
// line input that make a terminal usable with a thumb.
async function phoneShot() {
  const s = await start({ viewport: PHONE });
  try {
    await setup(s.page, s.url);
    await domRenderer(s);
    await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newSession', { timeout: 15000 });

    const id = await newSession(s.page, 'claude');
    await rename(s, id, 'Claude Code · on the train');
    await typeLine(s.page, 'what changed in the store package today?');
    await seen(s.page, 'you said:');
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 10000 }).catch(() => {});
    await s.page.fill('#lineInput', 'now run the tests');
    await wait(600);

    mkdirSync(DOCS, { recursive: true });
    await s.page.screenshot({ path: join(DOCS, 'screenshot-phone.png') });
    console.log('wrote docs/screenshot-phone.png (' + PHONE.width + 'x' + PHONE.height + ')');
  } finally { await s.stop(); }
}

// The dashboard picture, of the Programs cards: what Socrates found on the
// machine, what each one is allowed to do, and the mark that names it.
async function adminShot() {
  const s = await start({ viewport: DESKTOP, live: REAL_CLIS });
  try {
    await setup(s.page, s.url);
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('.harness-card', { timeout: 20000 });
    // Long enough for the cards to have been rendered and re-rendered by the
    // dashboard's own polls; scrolling before that would be undone by them.
    await wait(4000);

    // Put the Programs heading just below the sticky top bar: those cards are
    // what the picture is of, and they are not the first thing on the page.
    await s.page.evaluate(() => {
      if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
      // The cards replace the placeholder that carried the "Programs"
      // heading, so the anchor is the container they are rendered into.
      const cards = document.getElementById('harnessCards');
      if (!cards) return;
      cards.scrollIntoView({ block: 'start' });
      const bar = document.querySelector('header, .topbar, .top-bar');
      scrollBy(0, -((bar ? bar.getBoundingClientRect().height : 56) + 16));
    });
    // The pointer is left wherever the last click put it, and a hovered
    // control in a documentation picture reads as a state rather than a button.
    await s.page.mouse.move(4, 4);
    await wait(200);

    mkdirSync(DOCS, { recursive: true });
    await s.page.screenshot({ path: join(DOCS, 'screenshot-admin.png') });
    console.log('wrote docs/screenshot-admin.png (' + DESKTOP.width + 'x' + DESKTOP.height + ')');
  } finally { await s.stop(); }
}

try {
  await sessionShot();
  await phoneShot();
  await adminShot();
} finally {
  cleanupBuild();
}
