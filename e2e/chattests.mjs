// chattests: a chat's own tests on its page (docs/design/CHAT-TESTS.md, "The
// chat page"). The fake Claude Code writes and runs tests through the real
// socrates-tests server (/mcpcall, one tool call per line): four tests, one
// failed with two screenshots, one still running at its second step, one
// passed and one draft. The page shows them live - the button and its
// badge, the column with its rows, the viewer with the steps, what was
// observed and the screenshots, the lightbox - and the person overrides a
// result, edits a test (a step added: changed since the last run), asks for
// a run again (a message in the chat) and deletes one. On a phone the list
// is a side sheet and the viewer the whole screen, in touch mode too.

import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { deflateSync, crc32 } from 'node:zlib';
import { setup, start, ok, wait, shot } from './harness.mjs';

/* ------------------------------------------------------------ the helpers */

// png is a small RGB picture, painted pixel by pixel.
export function png(width, height, paint) {
  const row = width * 3 + 1;
  const pixels = Buffer.alloc(row * height);
  for (let y = 0; y < height; y += 1) {
    pixels[y * row] = 0;
    for (let x = 0; x < width; x += 1) {
      const [r, g, b] = paint(x, y);
      const at = y * row + 1 + x * 3;
      pixels[at] = r;
      pixels[at + 1] = g;
      pixels[at + 2] = b;
    }
  }
  const chunk = (type, data) => {
    const head = Buffer.alloc(8);
    head.writeUInt32BE(data.length, 0);
    head.write(type, 4, 'latin1');
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])) >>> 0, 0);
    return Buffer.concat([head, data, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr), chunk('IDAT', deflateSync(pixels)), chunk('IEND', Buffer.alloc(0))]);
}

// screenPng is a phone's screen as a sketch: a bar, a card with two fields
// and a button, in the tint given; with `error` a red line under the
// fields.
function screenPng(tint, { error = false } = {}) {
  const w = 360;
  const h = 640;
  return png(w, h, (x, y) => {
    if (y < 56) return tint;
    const inCard = x > 24 && x < w - 24 && y > 120 && y < 460;
    if (!inCard) return [243, 245, 249];
    const field = (top) => y > top && y < top + 44 && x > 44 && x < w - 44;
    if (field(170) || field(240)) return (x === 45 || x === w - 45 || y === 171 || y === 213 || y === 241 || y === 283) ? [160, 166, 176] : [255, 255, 255];
    if (error && y > 300 && y < 312 && x > 44 && x < 260) return [200, 40, 40];
    if (y > 380 && y < 424 && x > 44 && x < w - 44) return tint;
    return [255, 255, 255];
  });
}

/** evidence writes the pictures the fake agent attaches, and answers their paths. */
export function evidence(dir) {
  mkdirSync(dir, { recursive: true });
  const files = {
    login: join(dir, '01-login.png'),
    error: join(dir, '02-wrong-password.png'),
    cart: join(dir, '01-cart.png'),
    results: join(dir, '01-results.png'),
  };
  writeFileSync(files.login, screenPng([26, 115, 232]));
  writeFileSync(files.error, screenPng([26, 115, 232], { error: true }));
  writeFileSync(files.cart, screenPng([24, 128, 56]));
  writeFileSync(files.results, screenPng([130, 80, 200]));
  return files;
}

const call = (tool, args) => '/mcpcall socrates-tests ' + tool + ' ' + JSON.stringify(args);

/** script is the message that has the fake agent write and run the four tests. */
export function script(files) {
  return [
    call('test_create', {
      title: 'Sign in with a wrong password', environment: 'iPhone 15 · Safari', tags: ['auth'],
      goal: 'A person who mistypes the password is told so **at once**, and nothing else changes.',
      steps: [{ action: 'Open /login', expect: 'The form with email and password' },
        { action: 'Sign in with a wrong password', expect: 'An error under the fields, the email kept' }],
    }),
    call('test_create', {
      title: 'Checkout with a saved card', environment: 'Pixel 8 · Chrome', tags: ['shop', 'payments'],
      steps: [{ action: 'Open the cart with two items', expect: 'Both items and the total' },
        { action: 'Pay with the saved card', expect: 'The payment sheet with the card' },
        { action: 'Confirm', expect: 'The thank-you page with the order number' }],
    }),
    call('test_create', {
      title: 'Search finds a product by its name', environment: 'Desktop · Firefox',
      steps: [{ action: 'Search for “lamp”', expect: 'The lamps, the best match first' }],
    }),
    call('test_create', {
      title: 'Profile picture upload',
      steps: [{ action: 'Upload a picture in the profile', expect: 'The new picture in the header' }],
    }),
    call('test_start', { test: 'T1' }),
    call('test_step', { test: 'T1', step: 1, verdict: 'pass', observed: 'The form shows email and password, Sign in below.', files: [files.login] }),
    call('test_step', {
      test: 'T1', step: 2, verdict: 'fail', observed: 'The page reloads and the email field is empty; no error is shown.',
      note: 'The red line under the fields flashes for a moment.', files: [files.error], output: 'POST /api/login 401\n{"error":"wrong password"}',
    }),
    call('test_finish', { test: 'T1', summary: 'Signing in with a wrong password loses the email and says nothing.', findings: '- **Bug:** the form is reloaded after a 401 instead of showing the error.\n- Suspect: `login.js` submits natively.' }),
    call('test_start', { test: 'T3' }),
    call('test_step', { test: 'T3', step: 1, verdict: 'pass', observed: 'Three lamps, “Desk lamp” first.', files: [files.results] }),
    call('test_finish', { test: 'T3', summary: 'Search works.' }),
    call('test_start', { test: 'T2' }),
    call('test_step', { test: 'T2', step: 1, verdict: 'pass', observed: 'Two items, total 54.80.', files: [files.cart] }),
  ].join('\n');
}

/** testsChat makes a chat with Socrates Tests on, over the API. */
export async function testsChat(s, title = 'Shop: sign-in and checkout') {
  const res = await s.context.request.post(s.url + '/api/chats', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), harness: 'claude', workdir_mode: 'dynamic', title, mcp: { 'socrates-tests': true } },
  });
  if (res.status() !== 201) throw new Error('could not make the chat: ' + res.status() + ' ' + await res.text());
  return (await res.json()).session.id;
}

/** signIn gives the fake Claude Code an account (an API key). */
export async function signIn(s) {
  const res = await s.context.request.post(s.url + '/api/accounts', {
    data: { agent: 'claude', kind: 'api_key', label: 'Work', key: 'sk-e2e-claude-' + 'k'.repeat(16) },
  });
  if (!res.ok()) throw new Error('no account: ' + res.status());
}

async function chatOf(s, id) {
  return (await s.context.request.get(s.url + '/api/chats/' + encodeURIComponent(id))).json();
}

/** tell sends the chat a message as another device would, once its agent is up and idle. */
export async function tell(s, id, text, timeout = 30000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const body = await chatOf(s, id);
    if (body.chat && body.chat.process === 'ready' && !body.chat.turn_running) break;
    await wait(200);
  }
  const res = await s.context.request.post(s.url + '/api/chats/' + encodeURIComponent(id) + '/messages', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), text },
  });
  if (!res.ok()) throw new Error('the message was not taken: ' + res.status() + ' ' + await res.text());
}

/** testsOf is the chat's tests as the API has them. */
export async function testsOf(s, id) {
  return (await (await s.context.request.get(s.url + '/api/chats/' + encodeURIComponent(id) + '/tests')).json()).tests || [];
}

/** untilTests waits for the API to show the tests in the statuses wanted. */
export async function untilTests(s, id, want, timeout = 40000) {
  const end = Date.now() + timeout;
  let list = [];
  while (Date.now() < end) {
    list = await testsOf(s, id);
    if (Object.entries(want).every(([t, status]) => list.some((x) => x.id === t && x.status === status))) return list;
    await wait(250);
  }
  return list;
}

/** openChat goes to a chat through its address once the list has its row. */
export async function openChat(page, id) {
  await page.waitForSelector('#sessionList [data-id="' + id + '"]', { state: 'attached', timeout: 20000 });
  await page.evaluate((target) => { location.hash = '#' + target; }, id);
  await page.waitForFunction((want) => location.hash.slice(1) === want
    && !!document.querySelector('#chat:not([hidden]) .so-composer__input'), id, { timeout: 20000 });
}

// rows is what the list in a host shows: id, status, meta, marks, a picture.
const rows = (page, host) => page.$$eval(host + ' .so-test', (nodes) => nodes.map((n) => ({
  id: n.dataset.test,
  status: n.dataset.status,
  chip: (n.querySelector('.so-test__status') || {}).textContent?.trim() || '',
  meta: (n.querySelector('.so-test__meta') || {}).textContent?.trim() || '',
  marks: [...n.querySelectorAll('.so-test__mark')].map((m) => m.textContent.trim()),
  shot: !!n.querySelector('img.so-test__shot'),
})));

const button = (page) => page.evaluate(() => {
  const b = document.getElementById('testsBtn');
  const badge = b.querySelector('.ds-badge');
  return {
    shown: !b.hidden && b.offsetWidth > 0,
    label: b.getAttribute('aria-label'),
    badge: badge && !badge.hidden ? badge.textContent : '',
    error: !!badge && badge.classList.contains('ds-badge--error'),
    expanded: b.getAttribute('aria-expanded'),
  };
});

// sideways is whether anything on the page, or in the overlay given, is
// wider than the window.
const sideways = (page, inside = '') => page.evaluate((sel) => {
  const doc = document.scrollingElement || document.documentElement;
  const out = { page: doc.scrollWidth > innerWidth + 1 };
  if (sel) {
    const node = document.querySelector(sel);
    out.overlay = !!node && [...node.querySelectorAll('*')].some((n) => n.scrollWidth > n.clientWidth + 1
      && getComputedStyle(n).overflowX !== 'hidden' && getComputedStyle(n).overflowX !== 'clip' && n.clientWidth > 0
      && !['SELECT', 'TEXTAREA', 'INPUT'].includes(n.tagName) && !n.closest('.so-code__pre'));
  }
  return out;
}, inside);

// still waits until what plays has played (a dialog coming in, a row
// fading in), so a picture is of the page at rest.
export async function still(page) {
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'
    || a.effect.getComputedTiming().iterations === Infinity), null, { timeout: 5000 }).catch(() => {});
  await wait(250);
}

/* -------------------------------------------------------------- the test */

export async function chattests() {
  const s = await start({ viewport: { width: 1280, height: 860 } });
  const { page } = s;
  try {
    await setup(page, s.url);
    await signIn(s);
    const files = evidence(join(s.data, 'evidence'));
    // A chat without the server and without tests has no button.
    const plain = await s.context.request.post(s.url + '/api/chats', {
      data: { client_id: 'e2e-plain', harness: 'claude', workdir_mode: 'dynamic', title: 'No tests here' },
    });
    const plainId = (await plain.json()).session.id;
    const id = await testsChat(s);
    await page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await openChat(page, plainId);
    await wait(1200);
    ok(!(await button(page)).shown, 'a chat without Socrates Tests and without tests has no Tests button', JSON.stringify(await button(page)));

    await openChat(page, id);
    const shownEmpty = await page.waitForFunction(() => !document.getElementById('testsBtn').hidden, null, { timeout: 10000 }).then(() => true, () => false);
    let b = await button(page);
    ok(shownEmpty && !b.badge && b.label === 'Tests, none yet', 'a chat with the server on has the button, with no badge yet', JSON.stringify(b));
    await page.click('#testsBtn');
    const column = await page.waitForSelector('#testsPanel:not([hidden]) .so-tests__empty', { timeout: 5000 }).then(() => true, () => false);
    const emptyText = column ? await page.$eval('#testsPanel .so-tests__empty', (n) => n.innerText) : '';
    ok(column && /No tests yet/.test(emptyText) && /Ask it in the chat/.test(emptyText),
      'at a desk the button opens the column beside the chat, which says how to ask the agent for tests', emptyText.replace(/\s+/g, ' '));

    // The agent writes and runs them; the page follows on its own.
    await tell(s, id, script(files));
    const live = await page.waitForFunction(() => {
      const badge = document.querySelector('#testsBtn .ds-badge');
      return !!badge && !badge.hidden && badge.textContent === '1' && badge.classList.contains('ds-badge--error')
        && document.querySelectorAll('#testsPanel .so-test').length === 4
        && !!document.querySelector('#testsPanel .so-test[data-test="T2"][data-status="running"] img.so-test__shot');
    }, null, { timeout: 40000 }).then(() => true, () => false);
    b = await button(page);
    ok(live && /^Tests: 1 failed, 1 running, 1 passed, 1 draft$/.test(b.label),
      'with no reload, the badge counts the failed test in the error colour, and the label says every count', JSON.stringify(b));
    let list = await rows(page, '#testsPanel');
    const by = Object.fromEntries(list.map((r) => [r.id, r]));
    ok(list.map((r) => r.id).join() === 'T1,T2,T3,T4' && by.T1.status === 'failed' && by.T1.chip === 'Failed'
      && by.T2.chip === 'Running' && by.T3.chip === 'Passed' && by.T4.chip === 'Draft',
    'the column lists the tests in their order, each with its status as a glyph and a word', JSON.stringify(list.map((r) => r.id + ':' + r.chip)));
    ok(/Pixel 8 · Chrome · Step 2 of 3 · Started/.test(by.T2.meta) && by.T2.shot && !by.T1.shot,
      'the running test says where it is, "Step 2 of 3", with its newest screenshot', JSON.stringify(by.T2));
    ok(/^iPhone 15 · Safari · Ran /.test(by.T1.meta) && by.T4.meta === '', 'a finished test says when it ran; a draft says nothing', JSON.stringify([by.T1.meta, by.T4.meta]));
    const summary = await page.$eval('#testsPanel .so-tests__summary', (n) => n.innerText.replace(/\s+/g, ' ').trim());
    ok(/1 failed/.test(summary) && /1 running/.test(summary) && /1 passed/.test(summary) && /1 draft/.test(summary), 'the summary line counts them per status', summary);
    const widths = await page.evaluate(() => ({
      stage: document.getElementById('stage').getBoundingClientRect().width,
      chat: document.getElementById('chat').getBoundingClientRect().width,
      panel: document.getElementById('testsPanel').getBoundingClientRect().width,
    }));
    ok(Math.abs(widths.chat + widths.panel - widths.stage) <= 2 && widths.panel >= 300, 'the chat narrows beside the column', JSON.stringify(widths));
    await still(page);
    await shot(page, 'chattests-desk');

    // The filters.
    await page.click('#testsPanel .so-tests__filters label:has(input[value="problems"])');
    await wait(400);
    const problems = (await rows(page, '#testsPanel')).map((r) => r.id).join();
    await page.click('#testsPanel .so-tests__filters label:has(input[value="open"])');
    await wait(400);
    const opened = (await rows(page, '#testsPanel')).map((r) => r.id).join();
    await page.click('#testsPanel .so-tests__filters label:has(input[value="all"])');
    await wait(400);
    ok(problems === 'T1' && opened === 'T2,T4' && (await rows(page, '#testsPanel')).length === 4,
      'Problems shows the failed test, Open the running one and the draft, All every test', JSON.stringify({ problems, opened }));

    // The viewer.
    await page.click('#testsPanel .so-test[data-test="T1"] .ds-list__action');
    await page.waitForSelector('dialog.so-testv[open] .so-step', { timeout: 10000 });
    const viewer = await page.$eval('dialog.so-testv', (n) => ({
      title: n.querySelector('.so-testv__title').textContent,
      status: n.querySelector('.so-testv__status').textContent.trim(),
      who: n.querySelector('.so-testv__who').textContent,
      facts: n.querySelector('.so-testv__facts').innerText.replace(/\s+/g, ' '),
      tags: [...n.querySelectorAll('.so-testv__tags .ds-tag')].map((t) => t.textContent).join(),
      run: n.querySelector('.so-testv__run option:checked')?.textContent || '',
      goal: n.querySelector('.so-testv__goal')?.innerHTML || '',
      steps: [...n.querySelectorAll('.so-step')].map((st) => ({
        verdict: st.dataset.verdict, chip: st.querySelector('.so-step__verdict')?.textContent.trim(),
        observed: st.querySelector('.so-step__observed')?.textContent || '', note: st.querySelector('.so-step__note')?.textContent || '',
        shots: st.querySelectorAll('.so-shot img').length, files: [...st.querySelectorAll('.so-file')].map((f) => f.textContent.trim()),
      })),
      summary: n.querySelector('.so-testv__summary')?.textContent || '',
      findings: n.querySelector('.so-testv__findings')?.innerHTML || '',
      history: [...n.querySelectorAll('.so-testv__history .ds-timeline__title')].map((t) => t.textContent),
    }));
    ok(viewer.title === 'Sign in with a wrong password' && viewer.status === 'Failed' && viewer.who === 'Judged by the agent'
      && /T1/.test(viewer.facts) && /iPhone 15/.test(viewer.facts) && viewer.tags === 'auth' && /^1 of 1 · Failed/.test(viewer.run),
    'the viewer’s head: the title, the status and who set it, the id, the environment, the tags and the run', JSON.stringify({ ...viewer, steps: undefined, goal: undefined, findings: undefined }));
    ok(/<strong>at once<\/strong>/.test(viewer.goal), 'the goal is Markdown', viewer.goal.slice(0, 120));
    ok(viewer.steps.length === 2 && viewer.steps[0].chip === 'Passed' && viewer.steps[0].shots === 1
      && viewer.steps[1].chip === 'Failed' && /email field is empty/.test(viewer.steps[1].observed) && /flashes/.test(viewer.steps[1].note)
      && viewer.steps[1].shots === 1 && viewer.steps[1].files.length === 1,
    'every step: its verdict as a chip, what was observed, the note, its screenshot and the output as a file', JSON.stringify(viewer.steps));
    ok(/loses the email/.test(viewer.summary) && /<strong>Bug:<\/strong>/.test(viewer.findings) && viewer.history.includes('Run finished') && viewer.history.includes('Written'),
      'the run’s summary and findings (Markdown), and the history', JSON.stringify({ summary: viewer.summary, history: viewer.history }));
    const loaded = await page.waitForFunction(() => [...document.querySelectorAll('dialog.so-testv .so-shot img')].every((img) => img.complete && img.naturalWidth === 360),
      null, { timeout: 10000 }).then(() => true, () => false);
    ok(loaded, 'the screenshots are served and drawn', String(loaded));
    await still(page);
    await shot(page, 'chattests-viewer');

    // The lightbox, over every image of the run.
    await page.click('dialog.so-testv .so-step[data-step="1"] .so-shot');
    await page.waitForSelector('dialog.so-lightbox[open] img.so-lightbox__img', { timeout: 5000 });
    const first = await page.$eval('dialog.so-lightbox', (n) => n.querySelector('.ds-dialog__title').textContent);
    await page.keyboard.press('ArrowRight');
    await wait(200);
    const second = await page.$eval('dialog.so-lightbox', (n) => ({ title: n.querySelector('.ds-dialog__title').textContent, src: n.querySelector('img').getAttribute('src'), caption: n.querySelector('figcaption').textContent }));
    await page.keyboard.press('ArrowLeft');
    await wait(200);
    const back = await page.$eval('dialog.so-lightbox .ds-dialog__title', (n) => n.textContent);
    ok(first === 'Screenshot 1 of 2' && second.title === 'Screenshot 2 of 2' && /02-wrong-password\.png$/.test(second.src) && /Step 2/.test(second.caption) && back === 'Screenshot 1 of 2',
      'a thumbnail opens the image large; the arrow keys go through every image of the run', JSON.stringify({ first, second, back }));
    await still(page);
    await shot(page, 'chattests-lightbox');
    await page.keyboard.press('Escape');
    const lbClosed = await page.waitForSelector('dialog.so-lightbox', { state: 'detached', timeout: 5000 }).then(() => true, () => false);
    ok(lbClosed && !!(await page.$('dialog.so-testv[open]')), 'Escape closes the lightbox and leaves the viewer', String(lbClosed));

    // Override.
    await page.click('dialog.so-testv .so-testv__secondary[data-act="override"]');
    await page.waitForSelector('dialog.so-testform[open] .ds-radio', { timeout: 5000 });
    await page.click('dialog.so-testform .ds-radio:has(input[value="passed"])');
    await page.fill('dialog.so-testform textarea', 'The reload is how this shop shows the error; fine for now.');
    await still(page);
    await shot(page, 'chattests-override');
    await page.click('dialog.so-testform .ds-btn--primary');
    await page.waitForSelector('dialog.so-testform', { state: 'detached', timeout: 10000 });
    const overridden = await page.waitForFunction(() => {
      const v = document.querySelector('dialog.so-testv');
      return v && v.querySelector('.so-testv__status').textContent.trim() === 'Passed' && v.querySelector('.so-testv__who').textContent === 'Set by you'
        && /fine for now/.test(v.querySelector('.so-testv__override')?.textContent || '');
    }, null, { timeout: 10000 }).then(() => true, () => false);
    ok(overridden, 'an override sets the status, says the person set it, and shows the note', String(overridden));
    await page.waitForFunction(() => {
      const row = document.querySelector('#testsPanel .so-test[data-test="T1"]');
      return row && row.dataset.status === 'passed' && /Set by you/.test(row.textContent);
    }, null, { timeout: 10000 }).catch(() => {});
    list = await rows(page, '#testsPanel');
    b = await button(page);
    ok(list.find((r) => r.id === 'T1').marks.includes('Set by you') && b.badge === '1' && !b.error,
      'the row says the person set it, and the badge counts the running test, out of the error colour', JSON.stringify({ T1: list.find((r) => r.id === 'T1'), b }));
    const apiT1 = (await testsOf(s, id)).find((t) => t.id === 'T1');
    ok(apiT1.override && apiT1.override.status === 'passed' && /fine for now/.test(apiT1.override.note), 'the override is the test’s', JSON.stringify(apiT1.override));

    // Edit: a step added makes the test changed since its last run.
    await page.click('dialog.so-testv .so-testv__secondary[data-act="edit"]');
    await page.waitForSelector('dialog.so-testform[open] .so-tedit__step', { timeout: 5000 });
    const stepsBefore = await page.$$eval('dialog.so-testform .so-tedit__step', (n) => n.length);
    await page.click('dialog.so-testform .ds-add-row');
    await page.fill('dialog.so-testform .so-tedit__step:last-child .so-tedit__action', 'Sign in with the right password');
    await page.fill('dialog.so-testform .so-tedit__step:last-child .so-tedit__expect', 'The account page');
    // Moved up one: it is the second of three now.
    await page.click('dialog.so-testform .so-tedit__step:last-child [data-step-act="up"]');
    await still(page);
    await shot(page, 'chattests-edit');
    await page.click('dialog.so-testform .ds-btn--primary');
    await page.waitForSelector('dialog.so-testform', { state: 'detached', timeout: 10000 });
    const edited = (await testsOf(s, id)).find((t) => t.id === 'T1');
    ok(stepsBefore === 2 && edited.steps.length === 3 && edited.steps[1].action === 'Sign in with the right password' && edited.stale,
      'Edit adds a step where it was moved to, and the test is changed since its last run', JSON.stringify(edited.steps.map((st) => st.action)));
    const staleShown = await page.waitForFunction(() => !!document.querySelector('dialog.so-testv .so-testv__stale')
      && /Changed since the last run/.test(document.querySelector('#testsPanel .so-test[data-test="T1"]').textContent), null, { timeout: 10000 }).then(() => true, () => false);
    ok(staleShown, 'the viewer and the row say "Changed since the last run"', String(staleShown));

    // Run again: a message from the person in the chat.
    await page.click('dialog.so-testv .so-testv__rerun');
    await page.waitForSelector('dialog.so-testv', { state: 'detached', timeout: 10000 }).catch(() => {});
    let asked = '';
    const end = Date.now() + 20000;
    while (!asked && Date.now() < end) {
      const history = (await (await s.context.request.get(s.url + '/api/chats/' + id + '?history=1')).json()).messages || {};
      asked = ((history.messages || []).filter((m) => m.role === 'user').map((m) => m.text).find((t) => /^Run test T1/.test(t || ''))) || '';
      if (!asked) await wait(300);
    }
    ok(/^Run test T1 “Sign in with a wrong password” again\. I had set it to Passed: The reload/.test(asked),
      'Run again sends the chat the person’s message, with the override', asked);
    const inChat = await page.waitForFunction(() => /Run test T1/.test(document.querySelector('#chat .so-chat__log').innerText), null, { timeout: 15000 }).then(() => true, () => false);
    ok(inChat, 'and the message is in the conversation on screen', String(inChat));

    // Delete.
    await page.click('#testsPanel .so-test[data-test="T4"] .ds-list__action');
    await page.waitForSelector('dialog.so-testv[open] .so-step', { timeout: 10000 });
    await page.click('dialog.so-testv .so-testv__secondary[data-act="delete"]');
    await page.waitForSelector('dialog[open] button:has-text("Delete the test")', { timeout: 5000 });
    await page.click('dialog[open] button:has-text("Delete the test")');
    const gone = await page.waitForFunction(() => !document.querySelector('#testsPanel .so-test[data-test="T4"]') && !document.querySelector('dialog.so-testv'),
      null, { timeout: 10000 }).then(() => true, () => false);
    ok(gone && !(await testsOf(s, id)).some((t) => t.id === 'T4'), 'Delete, confirmed, removes the test from the list and the server', String(gone));

    // The column is remembered: a reload finds it open again.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await openChat(page, id);
    const kept = await page.waitForSelector('#testsPanel:not([hidden]) .so-test', { timeout: 10000 }).then(() => true, () => false);
    ok(kept, 'the column stays open on this device across a reload', String(kept));
    ok(s.errors.length === 0, 'no console errors at a desk', s.errors.join(' | ') || '0');

    // A phone: the list is a side sheet, the viewer the whole screen.
    await page.setViewportSize({ width: 390, height: 844 });
    await wait(600);
    const hiddenColumn = await page.$eval('#testsPanel', (n) => n.hidden);
    const bar = await page.evaluate(() => {
      const r = document.getElementById('testsBtn').getBoundingClientRect();
      const badge = document.querySelector('#testsBtn .ds-badge').getBoundingClientRect();
      return { left: Math.round(r.left), right: Math.round(r.right), badgeTop: Math.round(badge.top), badgeRight: Math.round(badge.right), width: innerWidth };
    });
    ok(bar.left >= 0 && bar.right <= bar.width && bar.badgeTop >= 0 && bar.badgeRight <= bar.width,
      'on a phone the button and its badge fit in the top bar', JSON.stringify(bar));
    await still(page);
    await shot(page, 'chattests-phone-chat');
    await page.click('#testsBtn');
    const sheet = await page.waitForSelector('dialog.so-tests-sheet[open] .so-test', { timeout: 5000 }).then(() => true, () => false);
    await wait(700);
    let wide = await sideways(page, 'dialog.so-tests-sheet');
    ok(hiddenColumn && sheet && !wide.page && !wide.overlay, 'at 390px the column gives way to a sheet, and nothing scrolls sideways', JSON.stringify({ hiddenColumn, sheet, wide }));
    await still(page);
    await shot(page, 'chattests-phone-sheet');
    await page.click('dialog.so-tests-sheet .so-test[data-test="T1"] .ds-list__action');
    await page.waitForSelector('dialog.so-testv[open] .so-step', { timeout: 10000 });
    await wait(700);
    const phoneViewer = await page.$eval('dialog.so-testv .ds-dialog__box', (box) => ({
      width: Math.round(box.getBoundingClientRect().width),
      more: getComputedStyle(box.querySelector('.so-testv__more')).display !== 'none',
      secondary: [...box.querySelectorAll('.so-testv__secondary')].every((n) => getComputedStyle(n).display === 'none'),
    }));
    wide = await sideways(page, 'dialog.so-testv');
    ok(phoneViewer.width === 390 && phoneViewer.more && phoneViewer.secondary && !wide.page && !wide.overlay,
      'the viewer is the whole screen, its secondary actions in More, nothing sideways', JSON.stringify({ phoneViewer, wide }));
    await page.click('dialog.so-testv .so-testv__more');
    const menu = await page.waitForSelector('.ds-menu[aria-label="Test actions"]:not([hidden]) .ds-menu__item', { timeout: 5000 })
      .then(() => page.$$eval('.ds-menu[aria-label="Test actions"] .ds-menu__label', (n) => n.map((x) => x.textContent).join())) .catch(() => '');
    ok(menu === 'Override…,Edit…,Delete…', 'More holds Override, Edit and Delete', menu);
    await still(page);
    await shot(page, 'chattests-phone-viewer');
    await page.keyboard.press('Escape');
    await wait(300);
    await page.keyboard.press('Escape');
    await page.waitForSelector('dialog.so-testv', { state: 'detached', timeout: 5000 }).catch(() => {});
    await page.keyboard.press('Escape');
    await page.waitForSelector('dialog.so-tests-sheet[open]', { state: 'detached', timeout: 5000 }).catch(() => {});

    // Touch mode: a finger's targets.
    await page.evaluate(() => localStorage.setItem('socrates.touch', 'on'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await openChat(page, id);
    await page.waitForFunction(() => !document.getElementById('testsBtn').hidden, null, { timeout: 10000 });
    await page.click('#testsBtn');
    await page.waitForSelector('dialog.so-tests-sheet[open] .so-test', { timeout: 5000 });
    await wait(700);
    const touch = await page.evaluate(() => ({
      touch: document.documentElement.classList.contains('so-touch'),
      button: Math.round(document.getElementById('testsBtn').getBoundingClientRect().height),
      rows: [...document.querySelectorAll('dialog.so-tests-sheet .so-test')].map((n) => Math.round(n.getBoundingClientRect().height)),
      chips: [...document.querySelectorAll('dialog.so-tests-sheet .ds-chip')].map((n) => Math.round(n.getBoundingClientRect().height)),
    }));
    wide = await sideways(page, 'dialog.so-tests-sheet');
    ok(touch.touch && touch.button >= 48 && touch.rows.every((h) => h >= 48) && touch.chips.every((h) => h >= 32) && !wide.page && !wide.overlay,
      'in touch mode the button and the rows are a finger’s targets, and nothing scrolls sideways', JSON.stringify({ touch, wide }));
    await still(page);
    await shot(page, 'chattests-phone-touch');
    ok(s.errors.length === 0, 'no console errors', s.errors.join(' | ') || '0');
  } finally {
    await s.stop();
  }
}
