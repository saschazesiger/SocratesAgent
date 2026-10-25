// The README screenshots, taken from the real running app through the same
// harness the suite uses.
//
//   node e2e/shots.mjs
//
// It writes docs/screenshot-session.png (a chat at a desk),
// docs/screenshot-home.png (Home), docs/screenshot-phone.png (a question on
// a phone), docs/screenshot-admin.png (Admin's accounts) and
// docs/screenshot-backup.png (Admin's backup), and nothing else -
// docs/screenshot-tunnel.png is hand-made and stays where it is. It is not
// part of `make e2e`: the pictures are committed, so this is only run when
// they need redoing.
//
// The agents are the suite's protocol fakes (e2e/fakebin) under the three
// names, because a picture of the interface should show the same thing
// every time it is taken rather than whatever a real model happened to say
// that afternoon - and because a run against the machine's own HOME must
// never happen by accident. What is real is everything around them: the
// server, the agent hosts, the chat stream, the page. The people in the
// pictures write in words; claude.scripts.json, written beside the fake, has
// it run a script for each of those messages. With SOCRATES_LIVE_AGENTS=1,
// and only then, the
// Admin pictures are taken against the real CLIs instead; they start no chat
// with them.

import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  start, setup, wait, ensureNav, REPO, cleanupBuild, LIVE, backupForm, prefixFor, binaries,
} from './harness.mjs';

const DOCS = join(REPO, 'docs');
const DESKTOP = { width: 1280, height: 720 };
// The chat, Home and the accounts are taller than a laptop's screen: the cards and the
// chats under them, and every agent's accounts, are one picture each, and so
// is a turn from the message that asked for it to its last word.
const CHAT = { width: 1280, height: 900 };
const TALL = { width: 1280, height: 840 };
const PHONE = { width: 390, height: 844 };

// What the people in the pictures write, and the script each runs.
const FIX = 'The retention window rounds down. Fix it and run the store tests.';
const WORKING = 'Make the retention window round up, and run the store tests.';
const STORAGE = 'Set up storage for the first release.';
const STORAGE_ASK = {
  title: 'Storage',
  context: 'The shop needs a database before its first release, and nothing is set up yet.',
  questions: [{
    id: 'db', header: 'Database', question: 'Which database should the shop use?', kind: 'single',
    options: [
      { id: 'postgres', label: 'Postgres', description: 'Relational and robust; it needs a server of its own', recommended: true },
      { id: 'sqlite', label: 'SQLite', description: 'One file beside the binary, nothing to run' },
      { id: 'mysql', label: 'MySQL', description: 'What the hosting offers by default' },
    ],
  }],
};
const SCRIPTS = { [FIX]: '/readme', [WORKING]: '/readme 600', [STORAGE]: '/askuser ' + JSON.stringify(STORAGE_ASK) };

writeFileSync(join(binaries().bin, 'claude.scripts.json'), JSON.stringify(SCRIPTS));

const boot = (viewport, options = {}) => start({ viewport, ...options });

async function chat(s, harness, title, extra = {}) {
  const res = await s.context.request.post(s.url + '/api/chats', {
    data: { client_id: 'shots-' + Math.random().toString(36).slice(2), harness, workdir_mode: 'dynamic', title, ...extra },
  });
  if (res.status() !== 201) throw new Error('no ' + harness + ' chat: ' + res.status() + ' ' + await res.text());
  return (await res.json()).session.id;
}

async function until(s, id, test, timeout = 30000) {
  const end = Date.now() + timeout;
  let body = null;
  while (Date.now() < end) {
    body = await (await s.context.request.get(s.url + '/api/chats/' + id)).json();
    if (body.chat && test(body)) return body;
    await wait(200);
  }
  throw new Error('chat ' + id + ' never got there: ' + JSON.stringify(body && body.chat));
}

// tell sends a chat a message as another device would, once its agent is up.
async function tell(s, id, text) {
  await until(s, id, (b) => b.chat.process === 'ready' && !b.chat.turn_running);
  await s.context.request.post(s.url + '/api/chats/' + id + '/messages', {
    data: { client_id: 'shots-' + Math.random().toString(36).slice(2), text },
  });
}

// shell starts a Shell from the sheet, the way a person does, and names it.
async function shell(s, title) {
  const page = s.page;
  const was = await page.evaluate(() => location.hash.slice(1));
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.click('#newSessionSheet .so-settings__program[data-value="shell"]');
  await page.click('#nsStart');
  await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
  await page.waitForFunction((before) => location.hash.length > 1 && location.hash.slice(1) !== before, was, { timeout: 30000 });
  const id = await page.evaluate(() => location.hash.slice(1));
  await s.context.request.patch(s.url + '/api/sessions/' + id, { data: { title } });
  return id;
}

async function openChat(page, id) {
  await page.waitForSelector('#sessionList [data-id="' + id + '"]', { state: 'attached', timeout: 20000 });
  await page.evaluate((target) => { location.hash = '#' + target; }, id);
  await page.waitForFunction((want) => location.hash.slice(1) === want
    && !!document.querySelector('#chat:not([hidden]) .so-composer__input'), id, { timeout: 20000 });
}

const logShows = (page, text, timeout = 20000) => page.waitForFunction((want) => {
  const log = document.querySelector('#chat .so-chat__log');
  return !!log && log.innerText.includes(want);
}, text, { timeout });

async function goHome(page) {
  await ensureNav(page);
  await page.click('#homeBtn');
  await page.waitForSelector('#home:not([hidden])', { timeout: 10000 });
}

// still leaves the picture without a hovered control or a focus ring, and
// with the animations that end ended: one taken mid-way shows a card half in.
async function still(page) {
  await page.evaluate(() => { if (document.activeElement && document.activeElement.blur) document.activeElement.blur(); });
  await page.mouse.move(4, 4);
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'
    || a.effect.getComputedTiming().iterations === Infinity), null, { timeout: 5000 }).catch(() => {});
  await wait(400);
}

async function write(page, name, size, clip) {
  mkdirSync(DOCS, { recursive: true });
  await page.screenshot({ path: join(DOCS, name), ...(clip ? { clip } : {}) });
  const shown = clip || size;
  console.log('wrote docs/' + name + ' (' + shown.width + 'x' + shown.height + ')');
}

// The desk: the chats of the three agents and a Shell in the sidebar, and a
// Claude Code chat open on a turn that fixed something - what it was asked,
// its words, its edit, the tests it ran. The chat works in a folder of its
// own with a short path, which its tool rows say in full.
async function sessionShot() {
  const s = await boot(CHAT);
  const scratch = mkdtempSync(join(tmpdir(), 'shots-'));
  const shop = join(scratch, 'shop');
  mkdirSync(shop);
  try {
    await setup(s.page, s.url);
    await shell(s, 'make check');
    const codex = await chat(s, 'codex', 'The flaky store test');
    const opencode = await chat(s, 'opencode', 'API docs for v2');
    const claude = await chat(s, 'claude', 'Fix the retention window', { model: 'sonnet', workdir_mode: 'custom', workdir: shop });
    await openChat(s.page, claude);
    await s.page.fill('#chat .so-composer__input', FIX);
    await s.page.press('#chat .so-composer__input', 'Enter');
    await logShows(s.page, 'The store tests pass.');
    await until(s, codex, (b) => b.chat.process === 'ready');
    await until(s, opencode, (b) => b.chat.process === 'ready');
    // The turn's work is folded behind one line; the picture opens it, so
    // that its edit and its tests are in it.
    for (const key of await s.page.$$eval('#chat .so-work:not([open])', (n) => n.map((one) => one.dataset.openKey))) {
      await s.page.click('#chat .so-work[data-open-key="' + key + '"] > summary');
      await s.page.waitForSelector('#chat .so-work[data-open-key="' + key + '"][open] > .so-work__body', { timeout: 5000 });
    }
    await still(s.page);
    // From the person's message down: the turn is the picture, read from
    // the top.
    await s.page.evaluate(() => {
      let node = document.querySelector('#chat .so-chat__log');
      while (node && node.scrollHeight <= node.clientHeight) node = node.parentElement;
      if (node) node.scrollTop = 0;
    });
    await wait(400);
    await write(s.page, 'screenshot-session.png', CHAT);
  } finally {
    await s.stop();
    rmSync(scratch, { recursive: true, force: true });
  }
}

// Home: a question waiting, a chat at work, a turn nobody has read yet, and
// a chat ready for its first message. The agents in the picture say only
// what a script of theirs says; a message no script names would be echoed.
async function homeShot() {
  const s = await boot(TALL);
  try {
    await setup(s.page, s.url);
    await shell(s, 'make check');
    const done = await chat(s, 'claude', 'Fix the retention window');
    await tell(s, done, FIX);
    const codex = await chat(s, 'codex', 'The flaky store test');
    const working = await chat(s, 'claude', 'Round the window up');
    await tell(s, working, WORKING);
    const asking = await chat(s, 'claude', 'Storage for the first release');
    await tell(s, asking, STORAGE);
    await until(s, asking, (b) => (b.asks || []).length > 0);
    await until(s, done, (b) => !b.chat.turn_running);
    await until(s, codex, (b) => b.chat.process === 'ready');
    await goHome(s.page);
    await s.page.waitForFunction(() => {
      const card = document.querySelector('#home .so-home__questions');
      return !!card && card.dataset.value === '1';
    }, null, { timeout: 20000 });
    await still(s.page);
    await write(s.page, 'screenshot-home.png', TALL);
  } finally { await s.stop(); }
}

// A phone: the question a chat asked, from Home, as the whole screen.
async function phoneShot() {
  const s = await boot(PHONE, { touch: true });
  try {
    await setup(s.page, s.url);
    const asking = await chat(s, 'claude', 'Storage for the first release');
    await tell(s, asking, STORAGE);
    await until(s, asking, (b) => (b.asks || []).length > 0);
    await s.page.waitForSelector('#home .so-home__q-action', { timeout: 20000 });
    await s.page.click('#home .so-home__q-action');
    await s.page.waitForSelector('dialog.so-askdlg[open] .so-q-option', { timeout: 20000 });
    await still(s.page);
    await write(s.page, 'screenshot-phone.png', PHONE);
  } finally { await s.stop(); }
}

// scrollTo puts a card of Admin at the top of the view, once the page's own
// polls have drawn it and the drawer's pill has glided to it.
async function scrollTo(page, id) {
  await page.waitForSelector('#' + id, { timeout: 20000 });
  await wait(3000);
  await page.evaluate((card) => document.getElementById(card).scrollIntoView({ block: 'start' }), id);
  await wait(1500);
}

// Admin's accounts: two Claude Code accounts beside a Codex and an OpenCode
// one, each on an API key, the one kind that needs no sign-in flow.
async function adminShot() {
  const s = await boot(TALL, { live: LIVE });
  try {
    await setup(s.page, s.url);
    for (const [agentName, label, extra] of [['claude', 'Work', {}], ['claude', 'Private', {}], ['codex', 'Team', {}],
      ['opencode', 'OpenRouter', { provider: 'openrouter' }]]) {
      await s.context.request.post(s.url + '/api/accounts', {
        data: { agent: agentName, kind: 'api_key', label, key: 'sk-shots-' + agentName + '-' + label.toLowerCase() + '-0000000000', ...extra },
      });
    }
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await scrollTo(s.page, 'accountsCard');
    await still(s.page);
    await write(s.page, 'screenshot-admin.png', TALL);
  } finally { await s.stop(); }
}

// Admin's backup: a bucket on the suite's fake S3, and a snapshot in it of a
// machine with a chat on it - the card down to its snapshots, not the form
// below them, whose fields are the fake's.
async function backupShot() {
  const s = await boot(DESKTOP, { live: LIVE });
  try {
    await setup(s.page, s.url);
    if (!LIVE) {
      const fixed = await chat(s, 'claude', 'Fix the retention window');
      await tell(s, fixed, FIX);
      await until(s, fixed, (b) => !!b.chat.last_outcome);
    }
    const saved = await s.context.request.put(s.url + '/api/backup/config', { data: await backupForm(prefixFor(s.data)) });
    if (!saved.ok()) throw new Error('the backup was not configured: ' + saved.status() + ' ' + await saved.text());
    await s.context.request.post(s.url + '/api/backup/run', { data: {} });
    const end = Date.now() + 60000;
    while (Date.now() < end) {
      const snaps = ((await (await s.context.request.get(s.url + '/api/backup/snapshots')).json()).snapshots) || [];
      if (snaps.length) break;
      await wait(500);
    }
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await scrollTo(s.page, 'backupCard');
    await still(s.page);
    const cut = await s.page.evaluate(() => {
      const bucket = document.querySelector('#backupCard .ds-subsection');
      return bucket ? Math.round(bucket.getBoundingClientRect().top) : innerHeight;
    });
    await write(s.page, 'screenshot-backup.png', DESKTOP, { x: 0, y: 0, width: DESKTOP.width, height: Math.min(DESKTOP.height, cut) });
  } finally { await s.stop(); }
}

try {
  await sessionShot();
  await homeShot();
  await phoneShot();
  await adminShot();
  await backupShot();
} finally {
  cleanupBuild();
}
