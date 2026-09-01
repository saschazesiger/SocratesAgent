// The three README screenshots, taken from the real running app through the
// same harness the suite uses.
//
//   node e2e/shots.mjs
//
// It writes docs/screenshot-chat.png, docs/screenshot-auto.png and
// docs/screenshot-admin.png, and nothing else. It is not part of `make e2e`:
// the pictures are committed, so this is only run when they need redoing.
//
// The turns come from the scripted adapter, the same as the suite's, because a
// picture of the interface should show a full turn every time it is taken -
// tool card, reasoning, usage and answer - rather than whatever a real model
// happened to do that afternoon.

import { execFileSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { start, setup, ensureNav, wait, REPO, cleanupBuild } from './harness.mjs';

const DOCS = join(REPO, 'docs');
const DESKTOP = { width: 1280, height: 720 };
const PHONE = { width: 390, height: 844 };

// The dashboard picture is of what Socrates found on the machine, so it is
// taken against the real CLIs when they are installed: a screenshot showing
// three fake binaries in a /tmp directory is a picture of the test harness,
// not of the product. With no CLIs installed it falls back to the fakes.
const REAL_AGENTS = ['claude', 'codex', 'opencode'].every((name) => {
  try { execFileSync('which', [name], { stdio: 'ignore' }); return true; } catch { return false; }
});

// A turn that looks like the work people actually ask for.
const SCRIPT = JSON.stringify([
  { do: 'text', text: 'Let me look at the failing test first.' },
  { do: 'sleep', ms: 300 },
  { do: 'reason', text: 'The assertion compares a formatted duration, so the failure is probably a rounding change rather than the store itself.' },
  { do: 'tool', name: 'Bash', input: 'go test ./internal/store/', output: '--- FAIL: TestRetentionWindow (0.01s)\n    store_test.go:214: got 29m59s, want 30m\nFAIL\n' },
  { do: 'sleep', ms: 300 },
  { do: 'tool', name: 'Edit', input: 'internal/store/store.go', output: 'applied 1 change' },
  { do: 'sleep', ms: 300 },
  { do: 'tool', name: 'Bash', input: 'go test ./internal/store/', output: 'ok  \tgithub.com/saschazesiger/SocratesAgent/internal/store\t0.42s\n' },
  { do: 'text', text: 'The retention window was rounding down instead of up, so a chat that was exactly thirty minutes old fell outside it. One line in `store.go`, and the package is green again. Shall I commit it?' },
  { do: 'usage' },
  { do: 'end', outcome: 'ok' },
]);

async function turn(page, url, text) {
  await ensureNav(page);
  await page.click('#newChat');
  await page.waitForSelector('#newChatSheet[open]');
  await page.click('#ncEffort .seg[data-value="medium"]').catch(() => {});
  await page.click('#ncStart');
  await page.waitForSelector('#newChatSheet[open]', { state: 'detached', timeout: 5000 }).catch(() => {});
  await page.fill('#input', text);
  await page.click('#sendBtn');
  await page.waitForSelector('.msg.assistant', { timeout: 30000 });
  await page.waitForFunction(() => !document.body.classList.contains('busy'), null, { timeout: 30000 });
  await wait(800);
  return page.evaluate(() => location.hash.slice(1));
}

async function chatShot() {
  const s = await start({ script: SCRIPT, viewport: DESKTOP });
  try {
    await setup(s.page, s.url);
    const id = await turn(s.page, s.url, 'The store tests are failing on main. Have a look and fix it.');
    // A real name, the way the title generator would have given it one.
    await s.page.evaluate(async (chatId) => {
      await fetch('/api/chats/' + chatId, {
        method: 'PATCH', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ title: 'Fix the failing store tests' }),
      });
    }, id);
    await s.page.waitForFunction(() => document.getElementById('chatTitle').textContent.startsWith('Fix the'),
      null, { timeout: 10000 });
    // One tool card open, so the picture shows what a card holds.
    await s.page.evaluate(() => {
      const head = document.querySelectorAll('.step.tool-step > .head')[0];
      if (head) head.click();
    });
    await wait(500);
    await s.page.evaluate(() => { const t = document.getElementById('thread'); if (t) t.scrollTop = t.scrollHeight; });
    await wait(400);
    mkdirSync(DOCS, { recursive: true });
    await s.page.screenshot({ path: join(DOCS, 'screenshot-chat.png') });
    console.log('wrote docs/screenshot-chat.png (' + DESKTOP.width + 'x' + DESKTOP.height + ')');
  } finally { await s.stop(); }
}

async function autoShot() {
  const s = await start({ script: SCRIPT, viewport: PHONE });
  try {
    await setup(s.page, s.url);
    const id = await turn(s.page, s.url, 'The store tests are failing on main. Have a look and fix it.');
    await s.page.evaluate(async (chatId) => {
      await fetch('/api/chats/' + chatId, {
        method: 'PATCH', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ title: 'Fix the failing store tests' }),
      });
    }, id);
    await s.page.waitForFunction(() => document.getElementById('chatTitle').textContent.startsWith('Fix the'),
      null, { timeout: 10000 });
    await s.page.click('.view-slider .stop[data-view="auto"]');
    await s.page.waitForFunction(() => document.body.classList.contains('auto'));
    await s.page.waitForSelector('#autoAnswer:not([hidden])', { timeout: 15000 });
    await wait(1200);
    mkdirSync(DOCS, { recursive: true });
    await s.page.screenshot({ path: join(DOCS, 'screenshot-auto.png') });
    console.log('wrote docs/screenshot-auto.png (' + PHONE.width + 'x' + PHONE.height + ')');
  } finally { await s.stop(); }
}

async function adminShot() {
  const s = await start({ script: SCRIPT, viewport: DESKTOP, live: REAL_AGENTS });
  try {
    await setup(s.page, s.url);
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('.agent-card', { timeout: 20000 });
    await wait(1200);
    // Model discovery races the dashboard's first paint on a cold cache, so an
    // agent can be showing a timed-out handshake. One refresh each settles it.
    for (let i = 0; i < 3; i += 1) {
      const button = s.page.locator('.agent-card button:has-text("Refresh models")').nth(i);
      await button.click().catch(() => {});
      await wait(4000);
    }
    await wait(1200);
    // Put the Agents heading just below the sticky top bar: the card is what
    // the picture is of, and it is not the first one on the page.
    await s.page.evaluate(() => {
      if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
      const heading = [...document.querySelectorAll('h2')].find((h) => /Agents/.test(h.textContent));
      const bar = document.querySelector('header, .topbar, .top-bar');
      const clearance = (bar ? bar.getBoundingClientRect().height : 56) + 16;
      if (heading) scrollBy(0, heading.getBoundingClientRect().top - clearance);
    });
    await wait(600);
    // The pointer is left wherever the last click put it, and a hovered button
    // in a documentation picture reads as a state rather than a control.
    await s.page.mouse.move(4, 4);
    await wait(300);
    mkdirSync(DOCS, { recursive: true });
    await s.page.screenshot({ path: join(DOCS, 'screenshot-admin.png') });
    console.log('wrote docs/screenshot-admin.png (' + DESKTOP.width + 'x' + DESKTOP.height + ')');
  } finally { await s.stop(); }
}

try {
  await chatShot();
  await autoShot();
  await adminShot();
} finally {
  cleanupBuild();
}
