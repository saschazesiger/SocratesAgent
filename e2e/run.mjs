// The end-to-end suite. Every scenario drives the real binary through a real
// browser, against real tmux sessions, and every assertion prints the value it
// measured beside its verdict.
//
//   make e2e                     the whole suite
//   node e2e/run.mjs             the same
//   node e2e/run.mjs createshell one scenario, by name
//
// See e2e/README.md for what it needs and where the artefacts land.

import {
  start, setup, shot, ok, scenario, skipScenario, finish, ensureNav, wait,
  readFakeLog, openRouterStub, PASSWORD, LIVE,
} from './harness.mjs';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// A browser that has been switched offline reports its own failed requests.
// They are the point of the scenarios that do it, not a defect in the page.
const OFFLINE_NOISE = /ERR_INTERNET_DISCONNECTED|ERR_NETWORK_CHANGED|ERR_FAILED|Failed to fetch/;
// A server taken away mid-session leaves its socket refused and its last
// response truncated. Only the restart scenarios tolerate these.
const RESTART_NOISE = new RegExp(OFFLINE_NOISE.source
  + '|ERR_INCOMPLETE_CHUNKED_ENCODING|ERR_CONNECTION_REFUSED|ERR_EMPTY_RESPONSE|ERR_CONNECTION_RESET'
  + '|WebSocket');
const unexpected = (errors, extra) => errors.filter((e) => !(extra && extra.test(e)));

const WHITE = 'rgb(255, 255, 255)';

/* ------------------------------------------------------------- the helpers */

// The suite reads the terminal out of the DOM, which means the DOM renderer:
// this machine's headless Chromium does have a (software) WebGL context, so
// the shipped default would paint into a canvas nothing can read back. The
// renderer is a setting on the dashboard, so turning it off is what a person
// would do rather than something only a test can reach - and `webglrenders`
// below still proves the shipped default draws.
async function useDomRenderer(s) {
  const res = await s.context.request.put(s.url + '/api/settings', {
    data: { settings: { terminal: { webgl: false } } },
  });
  if (!res.ok()) throw new Error('could not turn the WebGL renderer off: ' + res.status());
}

// open lands on the session page with everything it needs loaded.
async function open(s) {
  await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
  await s.page.waitForSelector('#newSession', { timeout: 15000 });
}

// startSession drives the sheet the way a person would and waits until the
// session it made is the one on screen.
async function startSession(page, harness) {
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.waitForSelector('#nsHarness .seg[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#nsHarness .seg[data-value="' + harness + '"]');
  await page.click('#nsStart');
  await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
  await page.waitForFunction(() => location.hash.length > 1, null, { timeout: 30000 });
  return page.evaluate(() => location.hash.slice(1));
}

// screen is what the terminal is showing, as text. The DOM renderer keeps one
// element per row, which is why `useDomRenderer` is a precondition of every
// scenario that calls this.
const screen = (page) => page.evaluate(() => {
  const rows = document.querySelector('#term .xterm-rows');
  return rows ? rows.innerText : '';
});

// awaitScreen waits until the pane shows something, and reports what it saw.
async function awaitScreen(page, needle, timeout = 20000) {
  try {
    await page.waitForFunction((want) => {
      const rows = document.querySelector('#term .xterm-rows');
      return !!rows && rows.innerText.includes(want);
    }, needle, { timeout });
    return true;
  } catch {
    return false;
  }
}

// typeLine types into the terminal itself - not into a field beside it - which
// is the path every keystroke on a laptop takes.
async function typeLine(page, text) {
  await page.click('#term .xterm-screen');
  await page.keyboard.type(text);
  await page.keyboard.press('Enter');
}

const oneLine = (text) => text.replace(/\s+/g, ' ').trim().slice(0, 200);

/* --------------------------------------------------------- 1. createshell */

// The sheet makes a Shell session, the pane comes up with a prompt in it, and
// a command typed into the browser is run by a real shell in a real tmux pane.
// Everything else in this suite stands on this one working.
async function createshell() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    const id = await startSession(s.page, 'shell');
    ok(!!id, 'the sheet made a session and opened it', id || 'no id in the hash');

    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const marker = 'socrates-' + Date.now();
    await typeLine(s.page, 'echo ' + marker);
    const echoed = await awaitScreen(s.page, marker);
    ok(echoed, 'a command typed in the browser was run by the shell', oneLine(await screen(s.page)));

    // §E.10 rule 1: every surface is the same white, the terminal included.
    const colours = await s.page.evaluate(() => {
      const bg = (sel) => {
        const node = document.querySelector(sel);
        return node ? getComputedStyle(node).backgroundColor : 'missing';
      };
      return {
        body: bg('body'),
        sidebar: bg('.sidebar'),
        wrap: bg('.term-wrap'),
        viewport: bg('#term .xterm-viewport'),
        screen: bg('#term .xterm-screen'),
      };
    });
    ok(Object.values(colours).every((c) => c === WHITE),
      'the page and the terminal are the same white', JSON.stringify(colours));

    // Readability is checked by measurement, not by eye - and on what was
    // actually drawn rather than on the table it was drawn from. ANSI "white"
    // is #dcdde1 in the palette, which is 1.36:1 against a white page; what
    // makes it legible is `minimumContrastRatio: 4.5`, which re-derives it at
    // draw time. So the thing worth asserting is the drawn colour.
    await typeLine(s.page, "printf '\\033[37mDIMWHITE\\033[0m\\n'");
    ok(await awaitScreen(s.page, 'DIMWHITE'), 'the pane printed a line in ANSI white',
      oneLine(await screen(s.page)));
    const drawn = await s.page.evaluate(() => {
      const ratio = (rgb) => {
        const [r, g, b] = rgb.match(/\d+/g).map(Number).map((c) => {
          const v = c / 255;
          return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
        });
        const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b;
        return 1.05 / (lum + 0.05);
      };
      // The DOM renderer groups a row into one span per style, so the printed
      // line is a span of exactly that word; in the echo of the command it is
      // part of a longer, default-styled run.
      const spans = [...document.querySelectorAll('#term .xterm-rows span')];
      const node = spans.find((n) => n.textContent.trim() === 'DIMWHITE');
      const colour = node ? getComputedStyle(node).color : 'none';
      return { colour, ratio: node ? Math.round(ratio(colour) * 100) / 100 : 0 };
    });
    ok(drawn.ratio >= 4.5, 'a CLI\'s dimmest white is drawn legibly on the white page',
      drawn.colour + ' = ' + drawn.ratio + ':1');
    const contrastFloor = await s.page.evaluate(async () => {
      const mod = await import('/static/js/term.js');
      return { background: mod.LIGHT_THEME.background, black: mod.contrast('#17181b', '#ffffff') };
    });
    ok(contrastFloor.background === '#ffffff' && contrastFloor.black > 15,
      'the theme paints on white and its ink is nearly black',
      JSON.stringify({ ...contrastFloor, black: Math.round(contrastFloor.black * 10) / 10 }));

    // The header says what this session runs, with the harness's own mark.
    const header = await s.page.evaluate(() => ({
      mark: (document.querySelector('#sessionHarness .agent-mark') || { dataset: {} }).dataset.agent,
      title: (document.getElementById('sessionTitle') || {}).textContent,
      size: (document.getElementById('termSize') || {}).textContent,
    }));
    ok(header.mark === 'shell', 'the header carries the harness mark', header.mark || 'none');
    ok(/Shell/.test(header.title || ''), 'the session is named after what it runs', header.title);
    ok(/^\d+×\d+$/.test(header.size || ''), 'the header shows the size the pane is wearing', header.size);

    await shot(s.page, 'createshell');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 2. typeandsee */

// Keystrokes reach the pane, the output comes back, and the journal on disk
// holds the same bytes the screen does. The journal is the reconnect and audit
// path, and a journal that disagrees with the screen is worse than none.
async function typeandsee() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const marker = 'seen-' + Math.random().toString(36).slice(2, 10);
    await typeLine(s.page, 'printf "%s\\n" ' + marker);
    ok(await awaitScreen(s.page, marker), 'the output came back to the browser',
      oneLine(await screen(s.page)));

    // A second line, to prove the path is not a one-off and that the two
    // arrive in the order they were typed.
    const second = marker + '-again';
    await typeLine(s.page, 'printf "%s\\n" ' + second);
    ok(await awaitScreen(s.page, second), 'a second command reached the same pane',
      oneLine(await screen(s.page)));

    const text = await screen(s.page);
    ok(text.indexOf(marker) < text.indexOf(second),
      'the two lines are on screen in the order they were typed',
      text.indexOf(marker) + ' then ' + text.indexOf(second));

    const res = await s.context.request.get(s.url + '/api/sessions/' + id + '/journal');
    const journal = await res.text();
    ok(res.ok() && journal.includes(marker) && journal.includes(second),
      'the journal holds the same bytes the screen does',
      res.status() + ', ' + journal.length + ' bytes');

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------- 3. reloadkeepsscreen */

// A reload is the ordinary phone event: iOS kills the tab and it comes back.
// The tab keeps its viewer id, so the server has its ring and its input state,
// and what was on the screen is on the screen again.
async function reloadkeepsscreen() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const marker = 'kept-' + Math.random().toString(36).slice(2, 10);
    await typeLine(s.page, 'echo ' + marker);
    ok(await awaitScreen(s.page, marker), 'the marker is on screen before the reload',
      oneLine(await screen(s.page)));

    const before = await s.page.evaluate(() => sessionStorage.getItem('socrates.viewer'));
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const after = await s.page.evaluate(() => sessionStorage.getItem('socrates.viewer'));
    ok(before && before === after, 'the reloaded tab is the same viewer', before === after ? before : before + ' -> ' + after);

    ok(await awaitScreen(s.page, marker), 'the same screen came back after the reload',
      oneLine(await screen(s.page)));
    const hash = await s.page.evaluate(() => location.hash.slice(1));
    ok(hash === id, 'the reload opened the same session', hash);

    const list = await s.context.request.get(s.url + '/api/sessions');
    const row = ((await list.json()).sessions || []).find((one) => one.id === id);
    ok(row && row.state === 'running', 'the session never stopped running', row && row.state);

    // The pane is still usable afterwards, which is what says the input path
    // survived the reconnect and not only the output one.
    const again = marker + '-after';
    await typeLine(s.page, 'echo ' + again);
    ok(await awaitScreen(s.page, again), 'typing works again after the reload',
      oneLine(await screen(s.page)));

    await shot(s.page, 'reloadkeepsscreen');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------------- 4. pages */

// Every page is clean at a phone's width and at a desk's: no console error, no
// sideways scroll, and the sheet is a bottom sheet on one and a dialog on the
// other.
async function pages() {
  for (const viewport of [{ width: 390, height: 844 }, { width: 1280, height: 720 }]) {
    const tag = viewport.width + 'x' + viewport.height;
    const s = await start({ viewport });
    try {
      // /setup is only itself before there is a password, so it is looked at
      // first - and afterwards it is expected to hand over to /login.
      await s.page.goto(s.url + '/setup', { waitUntil: 'domcontentloaded' });
      await wait(1200);
      const setupBad = unexpected(s.errors);
      ok(setupBad.length === 0, `/setup at ${tag} has no console errors`, setupBad.join(' | ') || '0 errors');
      const setupOverflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      ok(setupOverflow <= 1, `/setup at ${tag} does not scroll sideways`, setupOverflow + 'px');
      await shot(s.page, 'pages-setup-' + tag);

      await setup(s.page, s.url);
      for (const path of ['/', '/admin']) {
        s.errors.length = 0;
        await s.page.goto(s.url + path, { waitUntil: 'domcontentloaded' });
        await wait(1800);
        const bad = unexpected(s.errors);
        ok(bad.length === 0, `${path} at ${tag} has no console errors`, bad.join(' | ') || '0 errors');
        const overflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        ok(overflow <= 1, `${path} at ${tag} does not scroll sideways`, overflow + 'px');
        await shot(s.page, 'pages-' + (path === '/' ? 'session' : path.slice(1)) + '-' + tag);
      }

      // The sheet is a bottom sheet on a phone and a centred dialog on a desk.
      await open(s);
      await ensureNav(s.page);
      await s.page.click('#newSession');
      await s.page.waitForSelector('#newSessionSheet[open]');
      await wait(300);
      const rect = await s.page.$eval('#newSessionSheet', (n) => n.getBoundingClientRect().toJSON());
      if (viewport.width >= 1000) {
        const centred = Math.abs((rect.left + rect.right) / 2 - viewport.width / 2) < 2;
        ok(rect.left > 200 && rect.top > 8 && centred,
          `the sheet is a centred dialog at ${tag}`, JSON.stringify(rect));
      } else {
        ok(rect.left <= 1 && Math.round(rect.width) >= viewport.width - 2,
          `the sheet is a full-width bottom sheet at ${tag}`, JSON.stringify(rect));
      }
      // Four harnesses, each with its own mark: §E.10 rule 2.
      const marks = await s.page.$$eval('#nsHarness .seg .agent-mark', (nodes) => nodes.map((n) => n.dataset.agent));
      ok(marks.join(',') === 'shell,claude,codex,opencode',
        `the sheet offers all four harnesses, each with its mark at ${tag}`, marks.join(',') || 'none');
      await shot(s.page, 'pages-sheet-' + tag);
      await s.page.click('#nsCancel');

      // Signed in, /login and /setup both redirect to the session page, so
      // they are only themselves once the session is gone.
      await s.context.clearCookies();
      s.errors.length = 0;
      await s.page.goto(s.url + '/login', { waitUntil: 'domcontentloaded' });
      await wait(1500);
      const here = await s.page.evaluate(() => location.pathname);
      ok(here === '/login', `/login at ${tag} renders itself when signed out`, here);
      const loginBad = unexpected(s.errors);
      ok(loginBad.length === 0, `/login at ${tag} has no console errors`, loginBad.join(' | ') || '0 errors');
      const loginOverflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      ok(loginOverflow <= 1, `/login at ${tag} does not scroll sideways`, loginOverflow + 'px');
      await shot(s.page, 'pages-login-' + tag);

      await s.page.goto(s.url + '/setup', { waitUntil: 'domcontentloaded' });
      await wait(800);
      const landed = await s.page.evaluate(() => location.pathname);
      ok(landed === '/login', `/setup at ${tag} hands over once a password exists`, landed);

      // Signed back in, so stop() can sweep with the context's own cookies.
      await s.page.goto(s.url + '/login', { waitUntil: 'domcontentloaded' });
      await s.page.fill('#password', PASSWORD);
      await s.page.click('#submit');
      await s.page.waitForFunction(() => !location.pathname.startsWith('/login'), null, { timeout: 15000 });
    } finally { await s.stop(); }
  }
}

/* ----------------------------------------------------------- 5. harnesses */

// All four session types, made through the sheet and seen in the browser. The
// three CLI ones are the fake TUI, which prints a banner naming itself, its
// working directory and the theme it was told about - so this also proves the
// white background reaches the program through tmux and the socket.
async function harnesses() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    for (const [id, expect] of [['shell', null], ['claude', 'FAKE claude'],
      ['codex', 'FAKE codex'], ['opencode', 'FAKE opencode']]) {
      await startSession(s.page, id);
      await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
      if (expect) {
        ok(await awaitScreen(s.page, expect), id + ' started and printed its banner',
          oneLine(await screen(s.page)));
        ok(await awaitScreen(s.page, 'theme=light'),
          id + ' was told the terminal is light', oneLine(await screen(s.page)));
      } else {
        const marker = 'shell-' + Math.random().toString(36).slice(2, 8);
        await typeLine(s.page, 'echo ' + marker);
        ok(await awaitScreen(s.page, marker), 'shell started and ran a command',
          oneLine(await screen(s.page)));
      }
    }

    // Four rows, each with the mark of what it runs: §E.10 rule 2 again, in
    // the list this time.
    const rows = await s.page.$$eval('#sessionList .chat-item', (nodes) => nodes.map((n) => ({
      mark: (n.querySelector('.agent-mark') || { dataset: {} }).dataset.agent,
      dot: (n.querySelector('.dot') || {}).className,
      words: n.querySelector('.label').textContent,
    })));
    ok(rows.length === 4, 'all four sessions are in the list', rows.length + ' rows');
    ok(rows.every((r) => r.mark), 'every row carries the mark of what it runs',
      rows.map((r) => r.mark).join(','));
    ok(rows.every((r) => /green/.test(r.dot)), 'every session is running',
      rows.map((r) => r.dot.replace('dot ', '')).join(','));
    // §E.10 rule 3: the technical detail is behind an "i", never in the words.
    ok(rows.every((r) => !r.words.includes('/')), 'no row spells out a path in its words',
      rows.map((r) => r.words).join(' | '));

    const detail = await s.page.evaluate(() => {
      const row = document.querySelector('#sessionList .chat-item');
      const bubble = row.querySelector('.tip-bubble');
      return { hidden: getComputedStyle(bubble).visibility, text: bubble.textContent };
    });
    ok(detail.hidden === 'hidden' && detail.text.includes('/'),
      'the working directory is in the bubble, drawn only on hover', JSON.stringify(detail));

    await shot(s.page, 'harnesses');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 6. sessionlist */

// What a row can do to a session: rename it, put it away, take it back out,
// and delete it - which is the only thing in Socrates that kills a tmux
// session, and which keeps the working directory.
async function sessionlist() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const rowSelector = '#sessionList .chat-item[data-id="' + id + '"]';
    const menu = async (label) => {
      await s.page.click(rowSelector + ' .act');
      await s.page.waitForSelector('.menu', { timeout: 5000 });
      await s.page.click('.menu .menu-item:text-is("' + label + '")');
    };

    // Rename.
    await menu('Rename');
    await s.page.waitForSelector('.modal[open] .input', { timeout: 5000 });
    await s.page.fill('.modal[open] .input', 'Renamed by the suite');
    await s.page.click('.modal[open] .btn.primary');
    await s.page.waitForFunction((sel) => {
      const row = document.querySelector(sel);
      return row && row.querySelector('.label').textContent === 'Renamed by the suite';
    }, rowSelector, { timeout: 8000 });
    const named = await s.page.$eval('#sessionTitle', (n) => n.textContent);
    ok(named === 'Renamed by the suite', 'the new name is in the row and in the header', named);
    const stored = await (await s.context.request.get(s.url + '/api/sessions/' + id)).json();
    ok(stored.session.title === 'Renamed by the suite', 'the rename was stored', stored.session.title);

    // Archive: it goes out of the active list and keeps running.
    await menu('Archive');
    await s.page.waitForFunction((sel) => !document.querySelector(sel), rowSelector, { timeout: 8000 });
    ok(true, 'an archived session leaves the active list', 'gone from Active');
    await s.page.click('#sessionScope .seg[data-scope="all"]');
    await s.page.waitForSelector(rowSelector, { timeout: 8000 });
    const archived = await (await s.context.request.get(s.url + '/api/sessions/' + id)).json();
    ok(archived.session.archived === true && archived.session.state === 'running',
      'it is archived and still running', archived.session.state);

    // And back out again.
    await menu('Unarchive');
    await s.page.waitForFunction((sel) => {
      const row = document.querySelector(sel);
      return row && !row.classList.contains('archived');
    }, rowSelector, { timeout: 8000 });
    ok(true, 'unarchiving puts it back', 'not archived');

    // Delete: the row goes, the tmux session goes, the directory stays.
    const workdir = archived.session.workdir;
    await menu('Delete');
    await s.page.waitForSelector('.modal[open] .btn.danger', { timeout: 5000 });
    const body = await s.page.$eval('.modal[open] .modal-body', (n) => n.textContent);
    ok(/working directory/i.test(body), 'the dialog says the working directory is kept', oneLine(body));
    await s.page.click('.modal[open] .btn.danger');
    await s.page.waitForFunction((sel) => !document.querySelector(sel), rowSelector, { timeout: 10000 });
    const left = await (await s.context.request.get(s.url + '/api/sessions?scope=all')).json();
    ok((left.sessions || []).length === 0, 'the session is gone', (left.sessions || []).length + ' left');

    const { existsSync } = await import('node:fs');
    ok(existsSync(workdir), 'the working directory was kept', workdir);

    await shot(s.page, 'sessionlist');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ----------------------------------------------------------- 7. exitoverlay */

// A pane that ends is not a page that breaks: the overlay says so, the exit
// status is behind the "i" rather than in the sentence, and Restart brings the
// session back on the same row.
async function exitoverlay() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(await awaitScreen(s.page, 'FAKE claude'), 'the session is up', oneLine(await screen(s.page)));

    await typeLine(s.page, '/exit 7');
    await s.page.waitForSelector('#termOverlay .overlay-card', { timeout: 20000 });
    const overlay = await s.page.evaluate(() => {
      const card = document.querySelector('#termOverlay .overlay-card');
      return {
        words: card.querySelector('.overlay-title').textContent,
        bubble: (card.querySelector('.tip-bubble') || {}).textContent || '',
        bubbleShown: card.querySelector('.tip-bubble')
          ? getComputedStyle(card.querySelector('.tip-bubble')).visibility : 'none',
        buttons: [...card.querySelectorAll('.overlay-actions .btn')].map((b) => b.textContent),
      };
    });
    ok(/The session ended/.test(overlay.words), 'the overlay says the session ended', oneLine(overlay.words));
    ok(/Exit status 7/.test(overlay.bubble) && overlay.bubbleShown === 'hidden',
      'the exit status is behind the "i", not in the sentence', JSON.stringify(overlay));
    ok(overlay.buttons.join(',') === 'Restart,Delete', 'the overlay offers Restart and Delete',
      overlay.buttons.join(','));
    await shot(s.page, 'exitoverlay');

    const dot = await s.page.$eval('#sessionList .chat-item .dot', (n) => n.className);
    ok(/amber/.test(dot), 'the row says the session ended', dot);

    await s.page.click('#termRestart');
    await s.page.waitForFunction(() => {
      const overlayNode = document.getElementById('termOverlay');
      return overlayNode && overlayNode.hidden;
    }, null, { timeout: 30000 });
    ok(await awaitScreen(s.page, 'FAKE claude'), 'Restart brought the session back',
      oneLine(await screen(s.page)));
    const row = await (await s.context.request.get(s.url + '/api/sessions/' + id)).json();
    ok(row.session.state === 'running', 'and the row is running again', row.session.state);

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* -------------------------------------------------------- 8. webglrenders */

// The shipped default is the WebGL renderer, and every other scenario turns it
// off so that it can read the screen out of the DOM. This one keeps it, so the
// default path is not the untested one: the addon has to load, paint a canvas,
// and leave the page without an error.
async function webglrenders() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await wait(1500);
    const painted = await s.page.evaluate(() => {
      const canvases = [...document.querySelectorAll('#term canvas')];
      return { canvases: canvases.length, sized: canvases.filter((c) => c.width > 0).length };
    });
    ok(painted.canvases > 0 && painted.sized > 0,
      'the default renderer paints the terminal into a canvas', JSON.stringify(painted));
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------------- 9. live */

// One real session against a real, logged in CLI. It types `/status`, which
// costs nothing: it must never spend tokens on a model turn.
async function livesession() {
  const s = await start({ viewport: { width: 1280, height: 720 }, live: true });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 60000 });
    await wait(4000);
    await typeLine(s.page, '/status');
    await wait(4000);
    const text = await screen(s.page);
    ok(text.trim().length > 0, 'the real CLI rendered something in the browser', oneLine(text));
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}


/* ------------------------------------------------------- 10. adminoptions */

// One option in every group of every harness, plus a preset directory: set it
// in the page, save, reload, and find it all still there - then start a
// session and read the flags the launcher actually built out of FAKE_LOG.
//
// The controls are addressed by the id the dashboard derives from the storage
// key, `opt-<harness>-<key>`, which is what makes this a table rather than a
// hundred lines of clicking.
const ADMIN_OPTIONS = [
  // shell: Session, Advanced (raw)
  ['shell', 'login', 'switch', false],
  ['shell', 'extra_args', 'text', '-x'],
  // claude: every group it has
  ['claude', 'default_effort', 'select', 'high'],
  ['claude', 'autocompact', 'text', '200k'],
  ['claude', 'permission_mode', 'select', 'plan'],
  ['claude', 'allowed_tools', 'text', 'Read, Write'],
  ['claude', 'remote_control_prefix', 'text', 'socrates-'],
  ['claude', 'agent', 'text', 'reviewer'],
  ['claude', 'strict_mcp_config', 'switch', true],
  ['claude', 'disable_mouse', 'switch', true],
  ['claude', 'verbose', 'switch', true],
  ['claude', 'settings_overrides', 'text', '{"env":{"SOCRATES_E2E":"1"}}'],
  // codex: every group it has
  ['codex', 'default_effort', 'select', 'xhigh'],
  ['codex', 'sandbox', 'select', 'read-only'],
  ['codex', 'remote_auth_token_env', 'text', 'CODEX_TOKEN'],
  ['codex', 'web_search', 'switch', true],
  ['codex', 'tui_theme', 'select', 'ocean-light'],
  ['codex', 'config_overrides', 'text', 'tools.web_search=true'],
  // opencode: every group it has
  ['opencode', 'small_model', 'text', 'openai/gpt-5-mini'],
  ['opencode', 'permission_json', 'text', '{"*":"ask"}'],
  ['opencode', 'enabled_providers', 'text', 'anthropic'],
  ['opencode', 'pure', 'switch', true],
  ['opencode', 'share', 'select', 'manual'],
  ['opencode', 'tui_theme', 'select', 'nord'],
  ['opencode', 'log_level', 'select', 'WARN'],
  ['opencode', 'config_content', 'text', '{"theme":"nord"}'],
];

// Every disclosure is opened first: a control inside a shut <details> is not
// something a person could type into either.
const openGroups = (page) => page.$$eval('details.group', (nodes) => {
  for (const node of nodes) node.open = true;
});

async function setOption(page, [harness, key, kind, value]) {
  const selector = '#opt-' + harness + '-' + key;
  if (kind === 'switch') {
    // The checkbox itself is invisible by design - the switch a person sees
    // and taps is the track beside it - so that is what is clicked.
    if (await page.$eval(selector, (n) => n.checked) !== value) {
      await page.click(selector + ' + .track');
    }
    return;
  }
  if (kind === 'select') {
    await page.selectOption(selector, value);
    return;
  }
  await page.fill(selector, value);
}

async function readOption(page, [harness, key, kind]) {
  const selector = '#opt-' + harness + '-' + key;
  if (kind === 'switch') return page.$eval(selector, (n) => n.checked);
  return page.$eval(selector, (n) => n.value);
}

async function adminoptions() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  try {
    await setup(s.page, s.url);
    const preset = join(s.data, 'preset-dir');
    mkdirSync(preset, { recursive: true });

    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#harness-opencode', { timeout: 20000 });
    await openGroups(s.page);

    for (const option of ADMIN_OPTIONS) await setOption(s.page, option);

    // The preset row, typed the way a person types it.
    await s.page.click('#presetAdd');
    const row = '#presetDirs .preset-row:last-child';
    await s.page.fill(row + ' input:nth-child(1)', 'Projects');
    await s.page.fill(row + ' input:nth-child(2)', preset);
    await s.page.selectOption('#windowSize', 'largest');
    await s.page.fill('#historyLimit', '31000');

    await s.page.click('#saveTop');
    await s.page.waitForSelector('.toast', { timeout: 15000 });
    await wait(400);
    await shot(s.page, 'admin-options');

    // The reload is the assertion: everything above has to come back out of
    // the database and into the same controls.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#harness-opencode', { timeout: 20000 });
    await openGroups(s.page);

    const wrong = [];
    for (const option of ADMIN_OPTIONS) {
      const got = await readOption(s.page, option);
      const want = option[2] === 'text' ? String(option[3]) : option[3];
      // A text list is stored as a list and shown joined, so "Read, Write"
      // comes back as "Read, Write" and not as what was typed character for
      // character. Comparing without the spaces is the honest test.
      const same = String(got).replace(/\s/g, '') === String(want).replace(/\s/g, '');
      if (!same) wrong.push(option[0] + '.' + option[1] + '=' + got + ' (want ' + want + ')');
    }
    ok(wrong.length === 0, 'every option in every group survived a save and a reload',
      wrong.join(' | ') || ADMIN_OPTIONS.length + ' options');

    const terminal = await s.page.evaluate(() => ({
      windowSize: document.getElementById('windowSize').value,
      history: document.getElementById('historyLimit').value,
      preset: document.querySelector('#presetDirs .preset-row input:nth-child(2)')?.value || '',
    }));
    ok(terminal.windowSize === 'largest' && terminal.history === '31000',
      'the terminal card round-trips too', JSON.stringify(terminal));
    ok(terminal.preset === preset, 'the preset directory was stored', terminal.preset);

    // The sheet is where a preset is used, so that is where it is checked.
    await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newSession', { timeout: 15000 });
    await ensureNav(s.page);
    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    const cell = '#nsDir .seg[data-value="preset:' + preset + '"]';
    const offered = await s.page.$(cell);
    ok(!!offered, 'the new-session sheet offers the preset directory', cell);
    if (offered) await s.page.click(cell);
    await s.page.click('#nsHarness .seg[data-value="claude"]');
    await s.page.click('#nsStart');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 30000 });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    const launches = readFakeLog(s.data).filter((entry) => entry.name === 'claude');
    const launch = launches[launches.length - 1] || { argv: [], env: {}, cwd: '' };
    const argv = (launch.argv || []).join(' ');
    const flags = [
      // Not --effort: the sheet offers a model and an effort of its own, and
      // the launcher is meant to prefer what the session was started with.
      ['--autocompact 200k', argv.includes('--autocompact 200k')],
      ['--permission-mode plan', argv.includes('--permission-mode plan')],
      ['--allowedTools Read,Write', argv.includes('--allowedTools Read,Write')],
      ['--agent reviewer', argv.includes('--agent reviewer')],
      ['--strict-mcp-config', argv.includes('--strict-mcp-config')],
      ['--verbose', argv.includes('--verbose')],
    ];
    const missing = flags.filter(([, present]) => !present).map(([flag]) => flag);
    ok(missing.length === 0, 'the saved options reached the command line',
      missing.join(', ') || argv.slice(0, 220));
    ok((launch.env || {}).CLAUDE_CODE_DISABLE_MOUSE === '1',
      'and a switch that is an environment variable reached the environment',
      String((launch.env || {}).CLAUDE_CODE_DISABLE_MOUSE));
    ok((launch.env || {}).CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX === 'socrates-',
      'as did the one that is a prefix',
      String((launch.env || {}).CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX));
    ok(launch.cwd === preset, 'the session was started in the preset directory', launch.cwd);

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------ 11. tmuxinstaller */

// The engine card against a machine that has the wrong tmux and a package
// manager that only pretends. Nothing is installed: `tmux` on this run's PATH
// is a script that says 3.2a, and `apt-get` is a script that prints what
// apt-get prints and exits.
function stubMachine() {
  const dir = mkdtempSync(join(tmpdir(), 'socrates-e2e-stub-'));
  writeFileSync(join(dir, 'tmux'), '#!/bin/sh\necho "tmux 3.2a"\n', { mode: 0o755 });
  writeFileSync(join(dir, 'apt-get'), `#!/bin/sh
if [ "$1" = "update" ]; then
  echo "Reading package lists..."
  exit 0
fi
echo "Setting up tmux (3.6a-2) ..."
echo "Processing triggers for ncurses-term ..."
exit 0
`, { mode: 0o755 });
  return dir;
}

async function tmuxinstaller() {
  const stub = stubMachine();
  const s = await start({
    viewport: { width: 1280, height: 900 },
    env: { PATH: stub + ':' + process.env.PATH },
  });
  try {
    await setup(s.page, s.url);
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#tmuxCard .state-label', { timeout: 20000 });
    await s.page.waitForFunction(
      () => !/Loading/.test(document.querySelector('#tmuxStatus .state-label').textContent),
      null, { timeout: 15000 });

    const card = await s.page.evaluate(() => ({
      dot: document.querySelector('#tmuxStatus .state-dot').className,
      label: document.querySelector('#tmuxStatus .state-label').textContent,
      detail: document.querySelector('#tmuxStatus').textContent,
      install: document.getElementById('tmuxInstall').hidden
        ? '' : document.getElementById('tmuxInstall').textContent,
    }));
    // The checklist item: 3.2a is amber and "too old", never green and "ok".
    ok(/\bold\b/.test(card.dot) && /too old/.test(card.label),
      'tmux 3.2a is reported as too old, not as ok', card.dot + ' · ' + card.label);
    ok(card.detail.includes('3.2a') && card.detail.includes('3.3'),
      'the card shows the version it found and the one it needs', oneLine(card.detail));
    ok(card.install.includes('apt-get'), 'and it offers the package manager it found', card.install);

    await shot(s.page, 'tmux-card');
    await s.page.click('#tmuxInstall');
    // The output arrives over the event stream, line by line.
    await s.page.waitForFunction(
      () => /Setting up tmux/.test(document.getElementById('tmuxLog').textContent),
      null, { timeout: 30000 });
    const streamed = await s.page.$eval('#tmuxLog', (n) => n.textContent);
    ok(streamed.includes('apt-get update') && streamed.includes('Reading package lists'),
      'the installer streamed its output into the page', oneLine(streamed));

    // And it survives the page being thrown away, because it is in the
    // database and not only in the tab that watched it.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#tmuxLogToggle:not([hidden])', { timeout: 20000 });
    await s.page.click('#tmuxLogToggle');
    await s.page.waitForFunction(
      () => /Setting up tmux/.test(document.getElementById('tmuxLog').textContent),
      null, { timeout: 15000 });
    const kept = await s.page.$eval('#tmuxLog', (n) => n.textContent);
    ok(kept.includes('Setting up tmux'), 'and it is still there after a reload', oneLine(kept));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}


/* ------------------------------------------------------------ 12. keybar */

// The keys a phone keyboard does not have, measured on the bytes that leave
// the browser rather than on what the page looks like. `sentBytes` wraps
// WebSocket.send before anything loads, so every input frame this suite
// asserts on is the frame the server actually received.
async function recordInput(page) {
  await page.addInitScript(() => {
    window.__sent = [];
    const send = WebSocket.prototype.send;
    WebSocket.prototype.send = function (data) {
      if (data && data.byteLength !== undefined && typeof data !== 'string') {
        const bytes = new Uint8Array(data.buffer ? data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) : data);
        // 0x02 is an input frame: kind, an eight byte sequence number, bytes.
        if (bytes[0] === 2) {
          window.__sent.push([...bytes.subarray(9)].map((b) => b.toString(16).padStart(2, '0')).join(''));
        }
      }
      return send.call(this, data);
    };
  });
}

const sentBytes = (page) => page.evaluate(() => window.__sent.slice());

async function keybar() {
  const s = await start({ viewport: { width: 390, height: 844 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await recordInput(s.page);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 10000 });

    const keys = await s.page.$$eval('#keybar .key', (nodes) => nodes.map((n) => n.dataset.key));
    ok(keys.join(',') === 'Escape,Tab,Control,Alt,Left,Down,Up,Right,Enter,Ctrl-C,Ctrl-D,Ctrl-Z,Paste,Keyboard',
      'the key bar carries every key of §E.6, in order', keys.join(','));
    const composer = await s.page.evaluate(() => ({
      composer: !document.getElementById('composer').hidden,
      mic: !document.getElementById('micBtn').hidden,
      keybarTop: document.getElementById('keybar').getBoundingClientRect().top,
      wrapBottom: document.getElementById('termWrap').getBoundingClientRect().bottom,
    }));
    ok(composer.composer && composer.mic, 'the line composer and its microphone are on screen at 390×844',
      JSON.stringify(composer));
    ok(composer.keybarTop >= composer.wrapBottom - 1, 'the key bar sits under the terminal, not over it',
      Math.round(composer.keybarTop) + ' vs ' + Math.round(composer.wrapBottom));

    // One key at a time, and the assertion is the byte. What a key adds is
    // compared against the frames sent since it was tapped rather than
    // against the last one, because the terminal answers tmux's questions -
    // a window size report, a device attribute - on the same path.
    const press = async (name) => {
      const before = (await sentBytes(s.page)).length;
      await s.page.click('#keybar .key[data-key="' + name + '"]');
      await wait(150);
      return (await sentBytes(s.page)).slice(before);
    };
    const sends = async (name, hex, what) => {
      const added = await press(name);
      ok(added.includes(hex), what, added.join(',') || 'nothing');
    };
    await sends('Escape', '1b', 'Esc sends 0x1b');
    await sends('Up', '1b5b41', 'the up arrow sends ESC [ A');
    await sends('Left', '1b5b44', 'the left arrow sends ESC [ D');
    await sends('Ctrl-C', '03', '^C sends 0x03');
    await sends('Tab', '09', 'Tab sends 0x09');

    // The sticky modifier: arm it, then type an ordinary letter on the
    // device's own keyboard. This is the only way a phone can send Ctrl-C to
    // a program at all, so it is the one that matters most.
    await s.page.click('#term .xterm-screen');
    await s.page.click('#keybar .key[data-key="Control"]');
    const armed = await s.page.$eval('#keybar .key[data-key="Control"]', (n) => n.className);
    ok(/\bon\b/.test(armed) && !/\block\b/.test(armed), 'one tap arms Ctrl', armed);
    const beforeCtrl = (await sentBytes(s.page)).length;
    await s.page.keyboard.type('c');
    await wait(200);
    const afterCtrl = (await sentBytes(s.page)).slice(beforeCtrl);
    ok(afterCtrl.includes('03'), 'the next letter typed is sent as Ctrl-C', afterCtrl.join(',') || 'nothing');
    const disarmed = await s.page.$eval('#keybar .key[data-key="Control"]', (n) => n.className);
    ok(!/\bon\b/.test(disarmed), 'and the modifier disarmed itself', disarmed);

    // A second tap locks it, and a locked modifier keeps transforming.
    await s.page.click('#keybar .key[data-key="Control"]');
    await s.page.click('#keybar .key[data-key="Control"]');
    const locked = await s.page.$eval('#keybar .key[data-key="Control"]', (n) => n.className);
    ok(/\block\b/.test(locked), 'a second tap locks Ctrl', locked);
    const beforeLock = (await sentBytes(s.page)).length;
    await s.page.keyboard.type('ab');
    await wait(200);
    const two = (await sentBytes(s.page)).slice(beforeLock).filter((hex) => hex.length === 2);
    ok(two.join(',') === '01,02', 'a locked Ctrl transforms every key after it', two.join(',') || 'nothing');
    await s.page.click('#keybar .key[data-key="Control"]');
    ok(!/\b(on|lock)\b/.test(await s.page.$eval('#keybar .key[data-key="Control"]', (n) => n.className)),
      'a third tap puts it away', 'off');

    // The line input: a whole line and exactly one carriage return, which is
    // the point of it - a phone that autocorrects a line it has already sent
    // one character at a time cannot be undone.
    const marker = 'line-' + Math.random().toString(36).slice(2, 8);
    const before = (await sentBytes(s.page)).length;
    await s.page.fill('#lineInput', 'echo ' + marker);
    await s.page.click('#sendLine');
    await wait(300);
    const after = (await sentBytes(s.page)).slice(before);
    const decoded = after.map((hex) => hex.match(/../g).map((b) => String.fromCharCode(parseInt(b, 16))).join(''));
    // Leaving the terminal for the field makes xterm report the focus change,
    // which is a frame of its own and nothing to do with the line.
    const lines = decoded.filter((d) => d.includes(marker));
    ok(lines.length === 1 && lines[0] === 'echo ' + marker + '\r',
      'the line input sends the whole line and one carriage return in one frame',
      JSON.stringify(decoded));
    ok(await awaitScreen(s.page, marker), 'and the shell ran it', oneLine(await screen(s.page)));
    ok(await s.page.$eval('#lineInput', (n) => n.value) === '', 'the field is empty again', 'empty');

    // The draft survives a reload, because a composed line is worth keeping
    // and half a keystroke is not.
    await s.page.fill('#lineInput', 'half a thought');
    await s.page.dispatchEvent('#lineInput', 'input');
    await wait(150);
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#lineInput', { timeout: 15000 });
    await s.page.waitForFunction(() => document.getElementById('lineInput').value.length > 0,
      null, { timeout: 10000 }).catch(() => {});
    ok(await s.page.$eval('#lineInput', (n) => n.value) === 'half a thought',
      'an unsent line is still in the field after a reload',
      await s.page.$eval('#lineInput', (n) => n.value));

    await shot(s.page, 'keybar');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------- 13. dictation */

// The microphone, end to end: Chromium's fake device records, the browser
// posts the WAV to the server, the server asks the gateway - a stub, so this
// costs nothing and needs no key - and the words land in the line input,
// unsent. The last part is the requirement: dictation writes a draft, it does
// not type into a shell.
async function dictation() {
  const stub = await openRouterStub({ text: 'list the files' });
  const s = await start({
    viewport: { width: 390, height: 844 },
    permissions: ['microphone'],
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required',
    ],
  });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    // base_url exists so the app can be pointed at a mock; this is the mock.
    const saved = await s.context.request.put(s.url + '/api/settings', {
      data: { settings: { openrouter: { api_key: 'e2e-key', base_url: stub.url } } },
    });
    ok(saved.ok(), 'the gateway is pointed at the stub', saved.status() + ' ' + stub.url);

    await recordInput(s.page);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await s.page.waitForSelector('#micBtn:not([hidden])', { timeout: 10000 });

    await s.page.click('#micBtn');
    await s.page.waitForSelector('#micBtn.rec', { timeout: 10000 });
    await wait(1400);
    const clock = await s.page.$eval('#recTime', (n) => ({ hidden: n.hidden, text: n.textContent }));
    ok(!clock.hidden && /^\d+:\d\d$/.test(clock.text), 'the recording clock is running', JSON.stringify(clock));
    await s.page.click('#micBtn');

    await s.page.waitForFunction(() => document.getElementById('lineInput').value.includes('list the files'),
      null, { timeout: 30000 });
    const value = await s.page.$eval('#lineInput', (n) => n.value);
    ok(value === 'list the files', 'the transcript landed in the line input', JSON.stringify(value));
    ok(stub.calls.length >= 1 && stub.calls[0].bytes > 2000,
      'the server uploaded the recording to the gateway',
      JSON.stringify(stub.calls.map((c) => c.path + ' ' + c.bytes + 'B')));

    // Unsent is the whole point: nothing went to the pane.
    const sent = await sentBytes(s.page);
    const typed = sent.map((hex) => hex.match(/../g).map((b) => String.fromCharCode(parseInt(b, 16))).join('')).join('');
    ok(!typed.includes('list the files'), 'and nothing of it was sent to the terminal',
      JSON.stringify(typed.slice(-40)));
    ok(!(await screen(s.page)).includes('list the files'), 'the pane never saw it',
      oneLine(await screen(s.page)).slice(-80));

    // It is a draft like any other, so Send is what puts it in the shell.
    await s.page.click('#sendLine');
    await wait(400);
    const after = await sentBytes(s.page);
    const line = after.map((hex) => hex.match(/../g).map((b) => String.fromCharCode(parseInt(b, 16))).join('')).join('');
    ok(line.includes('list the files\r'), 'pressing Send is what delivers it', JSON.stringify(line.slice(-30)));

    await shot(s.page, 'dictation');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

/* ------------------------------------------------------- 14. offlineonce */

// The centrepiece. A phone in a car loses its network mid-sentence; §D.6
// promises that nothing typed is lost and nothing arrives twice. So: type a
// whole command with the network down, bring it back, and let the shell
// itself count how many times the line ran. A duplicated keystroke would
// break the command; a duplicated line would run it twice.
async function offlineonce() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await recordInput(s.page);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const tag = Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'cd "$(pwd)"; rm -f ' + tag + '.txt');
    await wait(400);

    await s.context.setOffline(true);
    await s.page.evaluate(() => window.dispatchEvent(new Event('offline')));

    // The connection is down and the page says so, rather than showing an old
    // screen as if it were current.
    await s.page.waitForFunction(() => document.body.classList.contains('conn-lost'),
      null, { timeout: 20000 });
    await s.page.waitForFunction(() => document.body.classList.contains('stale'),
      null, { timeout: 20000 });
    // The pane fades over 200ms, so the measurement waits for it rather than
    // catching it half way.
    await wait(400);
    const lost = await s.page.evaluate(() => ({
      bar: !!document.querySelector('.conn-bar'),
      barText: (document.querySelector('.conn-bar') || {}).textContent || '',
      stale: document.getElementById('termWrap').classList.contains('stale'),
      opacity: getComputedStyle(document.getElementById('termWrap')).opacity,
    }));
    ok(lost.bar && lost.stale && Number(lost.opacity) < 1,
      'the lost connection is visible and the screen is marked as old', JSON.stringify(lost));

    // Twenty characters, typed one at a time into a terminal with no network.
    const command = 'echo z >> ' + tag + '.txt';
    ok(command.length === 20, 'the offline burst is twenty characters', String(command.length));
    await s.page.click('#term .xterm-screen');
    await s.page.keyboard.type(command, { delay: 25 });
    await s.page.keyboard.press('Enter');
    await wait(400);
    const held = await s.page.evaluate(() => window.__sent.length);
    ok(held >= 21, 'every keystroke was numbered and held', held + ' input frames sent so far');

    await s.context.setOffline(false);
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction(() => !document.body.classList.contains('conn-lost'),
      null, { timeout: 45000 });
    ok(true, 'the connection came back', 'conn-lost cleared');

    // The shell counts. One line in the file is exactly-once; two is the bug
    // this whole design exists to prevent, and none is a lost keystroke. The
    // answer is printed with a label so that the echo of the command asking
    // for it cannot be mistaken for the answer.
    await wait(800);
    await typeLine(s.page, 'echo COUNT=$(wc -l < ' + tag + '.txt)');
    const counted = await s.page.waitForFunction(() => {
      const rows = document.querySelector('#term .xterm-rows');
      const found = rows && rows.innerText.match(/COUNT=(\d+)/);
      return found ? found[1] : null;
    }, null, { timeout: 25000 }).then((h) => h.jsonValue()).catch(() => 'no answer');
    ok(counted === '1', 'the line typed offline ran exactly once', String(counted));

    const text = await screen(s.page);
    const onScreen = text.split(command).length - 1;
    ok(onScreen === 1, 'the command appears on the screen exactly once', String(onScreen));

    const res = await s.context.request.get(s.url + '/api/sessions/' + id + '/journal');
    const journal = await res.text();
    const inJournal = journal.split(command).length - 1;
    ok(inJournal === 1, 'and exactly once in the journal on disk', String(inJournal));

    // The app shell itself is offline-proof: the service worker has every
    // file the page needs, so a reload with no network is still Socrates and
    // is honest about the connection.
    await s.page.evaluate(() => navigator.serviceWorker.ready);
    const cached = await s.page.evaluate(async () => {
      const names = await caches.keys();
      const cache = await caches.open(names[0]);
      const keys = await cache.keys();
      return keys.map((r) => new URL(r.url).pathname);
    });
    ok(cached.includes('/static/js/keybar.js') && cached.includes('/static/js/session.js'),
      'the key bar is part of the precached shell', cached.length + ' entries');
    await s.context.setOffline(true);
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newSession', { timeout: 20000 });
    const offlineShell = await s.page.evaluate(() => ({
      terminal: typeof window.Terminal === 'function',
      bar: !!document.querySelector('.conn-bar'),
    }));
    ok(offlineShell.terminal, 'the app shell opened with no network at all', JSON.stringify(offlineShell));
    await s.context.setOffline(false);

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------- 15. sigtermreattach */

// Socrates is restarted under the session's feet - an upgrade, a crash, a
// supervisor - and the pane is not touched: tmux keeps it, the new server
// re-adopts it, and the browser reattaches to the same screen.
async function sigtermreattach() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const marker = 'kept-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + marker);
    ok(await awaitScreen(s.page, marker), 'the marker is on screen before the restart',
      oneLine(await screen(s.page)));

    const outcome = await s.restart();
    ok(outcome !== 'timeout', 'the server stopped on SIGTERM', JSON.stringify(outcome));

    // No reload: the page is the one that was open, and its socket is the one
    // that has to come back.
    await s.page.waitForFunction(() => !document.body.classList.contains('conn-lost'),
      null, { timeout: 40000 });
    ok(await awaitScreen(s.page, marker, 30000), 'the pane still holds what was typed',
      oneLine(await screen(s.page)));

    const list = await s.context.request.get(s.url + '/api/sessions');
    const row = ((await list.json()).sessions || []).find((one) => one.id === id);
    ok(row && row.state === 'running', 'and the session is running again', row && row.state);

    const again = marker + '-after';
    await typeLine(s.page, 'echo ' + again);
    ok(await awaitScreen(s.page, again, 25000), 'typing works after the reattach',
      oneLine(await screen(s.page)));

    await shot(s.page, 'sigtermreattach');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 16. takeover */

// The ordinary mobile failure is not a clean close: the old socket is half
// open and the server does not know yet. A second handshake carrying the same
// viewer id therefore takes the pane over at once, rather than leaving a
// terminal that looks connected and does nothing for forty seconds.
async function takeover() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await s.page.addInitScript(() => {
      window.__closed = [];
      window.WebSocket = new Proxy(window.WebSocket, {
        construct(target, args) {
          const ws = new target(...args);
          ws.addEventListener('close', (event) => window.__closed.push(event.code));
          return ws;
        },
      });
    });
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const viewer = await s.page.evaluate(() => sessionStorage.getItem('socrates.viewer'));
    ok(!!viewer, 'the first tab has a viewer id', viewer);
    await s.page.evaluate(() => { window.__closed.length = 0; });

    // A second tab that claims to be the same tab. This is what a reconnect
    // over a half open socket looks like from the server's side.
    const second = await s.context.newPage();
    await second.addInitScript((v) => { sessionStorage.setItem('socrates.viewer', v); }, viewer);
    await second.goto(s.url + '/#' + id, { waitUntil: 'domcontentloaded' });
    await second.waitForSelector('#term .xterm', { timeout: 20000 });

    const closedAt = Date.now();
    await s.page.waitForFunction(() => window.__closed.length > 0, null, { timeout: 3000 })
      .catch(() => {});
    const closed = await s.page.evaluate(() => window.__closed.slice());
    ok(closed.length > 0 && Date.now() - closedAt < 3000,
      'the first socket was closed by the takeover', JSON.stringify(closed));
    ok(closed.includes(1012), 'with 1012, Service Restart', JSON.stringify(closed));

    // The first tab goes away, as it would when a phone gives up on it, and
    // the tab that took the pane over has it.
    await s.page.close();
    const marker = 'took-' + Math.random().toString(36).slice(2, 8);
    await second.click('#term .xterm-screen');
    await second.keyboard.type('echo ' + marker);
    await second.keyboard.press('Enter');
    const seen = await second.waitForFunction((want) => {
      const rows = document.querySelector('#term .xterm-rows');
      return !!rows && rows.innerText.includes(want);
    }, marker, { timeout: 25000 }).then(() => true).catch(() => false);
    ok(seen, 'and the second tab drives the session',
      oneLine(await second.evaluate(() => document.querySelector('#term .xterm-rows').innerText)));

    s.page = second;
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------------- run

const ALL = [
  ['createshell', 'the sheet makes a Shell session and the shell answers', createshell],
  ['typeandsee', 'keystrokes reach the pane and the journal agrees with the screen', typeandsee],
  ['reloadkeepsscreen', 'a reload keeps the screen and the input path', reloadkeepsscreen],
  ['pages', 'every page is clean at a phone and at a desk', pages],
  ['harnesses', 'all four session types start and are seen in the browser', harnesses],
  ['sessionlist', 'rename, archive, unarchive and delete', sessionlist],
  ['exitoverlay', 'a pane that ends, its status behind the "i", and Restart', exitoverlay],
  ['webglrenders', 'the shipped renderer paints the terminal', webglrenders],
  ['keybar', 'the key bar sends the right bytes and the line input sends whole lines', keybar],
  ['dictation', 'the microphone writes a draft into the line input, unsent', dictation],
  ['offlineonce', 'a line typed with no network arrives exactly once', offlineonce],
  ['sigtermreattach', 'a restarted server reattaches to the pane that kept running', sigtermreattach],
  ['takeover', 'a second tab with the same viewer id takes the pane over', takeover],
  ['adminoptions', 'every harness option round-trips and reaches the command line', adminoptions],
  ['tmuxinstaller', 'the engine card, and an install that streams and survives a reload', tmuxinstaller],
  ['livesession', 'one real session against the real Claude Code CLI', livesession, { live: true }],
];

const wanted = process.argv.slice(2);
try {
  for (const [name, title, fn, opts] of ALL) {
    if (wanted.length && !wanted.includes(name)) continue;
    if (opts && opts.live && !LIVE) {
      skipScenario(name, title, 'set SOCRATES_LIVE_AGENTS=1 to run the live scenario');
      continue;
    }
    await scenario(name, title, fn, opts);
  }
} finally {
  finish();
}
