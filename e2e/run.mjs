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
  readFakeLog, killTmux, openRouterStub, sessionsOn, windowSize, PASSWORD, LIVE,
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
//
// The click is not enough on its own: xterm moves the focus into its hidden
// textarea itself, and a key pressed before it has landed there goes to the
// document and is never sent. So the focus is waited for, which is what a
// person's own hand does without thinking about it.
async function typeLine(page, text) {
  await page.click('#term .xterm-screen');
  await focusTerm(page);
  await page.keyboard.type(text);
  await page.keyboard.press('Enter');
}

// focusTerm waits until the keystrokes will actually reach the pane.
async function focusTerm(page, timeout = 5000) {
  await page.waitForFunction(() => {
    const area = document.querySelector('#term .xterm-helper-textarea');
    return !!area && document.activeElement === area;
  }, null, { timeout }).catch(() => {});
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

// The dangerous options confirm twice, and the second dialog is the one a
// person actually reads. These drive the pair the way a person would.
const dialogTitle = (page) => page.$eval('dialog.modal .modal-title', (n) => n.textContent)
  .catch(() => '');
const acceptDialog = (page) => page.click('dialog.modal .modal-actions button.danger');
const cancelDialog = (page) => page.click('dialog.modal .modal-actions button:not(.danger)');

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

    // The verified built-in theme list, not the third of it the docs name.
    const themes = await s.page.$$eval('#opt-opencode-tui_theme option', (ns) => ns.map((n) => n.value));
    ok(['opencode', 'dracula', 'carbonfox', 'catppuccin-frappe', 'system'].every((t) => themes.includes(t)),
      'the OpenCode theme list is the verified built-in one', themes.length + ' themes');

    await s.page.click('#saveTop');
    await s.page.waitForSelector('.toast', { timeout: 15000 });
    await wait(400);
    await shot(s.page, 'admin-options');

    // A save rebuilds the cards from what the server normalised, and must not
    // close the group somebody was editing while it does.
    const stillOpen = await s.page.$eval(
      '#harness-claude details.group[data-group="Diagnostics"]', (n) => n.open);
    ok(stillOpen, 'saving leaves the option group that was open, open', String(stillOpen));

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

    // Confirm-twice, on an option that turns a sandbox off. Two dialogs, and a
    // refusal at the second leaves the switch exactly as it was.
    await s.page.click('#opt-codex-bypass + .track');
    await s.page.waitForSelector('dialog.modal', { timeout: 5000 });
    const firstTitle = await dialogTitle(s.page);
    await acceptDialog(s.page);
    await s.page.waitForFunction(
      () => /Once more/.test(document.querySelector('dialog.modal .modal-title')?.textContent || ''),
      null, { timeout: 5000 });
    const secondTitle = await dialogTitle(s.page);
    ok(/sandbox/i.test(firstTitle) && /Once more/i.test(secondTitle),
      'a dangerous switch asks twice, and the second dialog is its own question',
      firstTitle + ' → ' + secondTitle);
    await cancelDialog(s.page);
    await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 5000 });
    const bypass = await s.page.$eval('#opt-codex-bypass', (n) => n.checked);
    ok(bypass === false, 'and cancelling the second dialog leaves it off', String(bypass));

    // The two options the review found unguarded.
    for (const [name, action] of [
      ['danger-full-access', () => s.page.selectOption('#opt-codex-sandbox', 'danger-full-access')],
      ['remote control', () => s.page.click('#opt-claude-remote_control + .track')],
    ]) {
      await action();
      const asked = await s.page.waitForSelector('dialog.modal', { timeout: 5000 })
        .then(() => true).catch(() => false);
      ok(asked, name + ' asks before it is turned on', String(asked));
      if (asked) {
        await cancelDialog(s.page);
        await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 5000 });
      }
    }
    const reverted = await s.page.evaluate(() => ({
      sandbox: document.getElementById('opt-codex-sandbox').value,
      remote: document.getElementById('opt-claude-remote_control').checked,
    }));
    ok(reverted.sandbox === 'read-only' && reverted.remote === false,
      'and a refusal puts both back', JSON.stringify(reverted));

    // §E.10 rule 3, in the setup check: the row says a verdict, the path and
    // the version are behind its "i".
    await s.page.click('#runChecks');
    await s.page.waitForSelector('#checks .check', { timeout: 60000 });
    const rows = await s.page.$$eval('#checks .check', (nodes) => nodes.map((node) => ({
      name: node.querySelector('.nm').textContent.trim(),
      visible: node.querySelector('.dt').textContent.trim(),
      tip: !!node.querySelector('.tip'),
    })));
    const leaking = rows.filter((row) => row.visible.includes('/'));
    ok(rows.length > 0 && leaking.length === 0,
      'no diagnostics row shows a path in the line itself',
      leaking.map((row) => row.name + ': ' + row.visible).join(' | ') || rows.length + ' rows');
    ok(rows.some((row) => row.tip), 'and the technical detail is behind the row\u2019s "i"',
      rows.filter((row) => row.tip).length + ' of ' + rows.length + ' rows have one');

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
    // The exit code is half the result: a log with no verdict makes somebody
    // read package-manager output to find out whether it worked.
    const verdict = await s.page.$eval('#tmuxInstallResult',
      (n) => (n.hidden ? '' : n.textContent));
    ok(/The last install finished/.test(verdict),
      'and the reload says what the last install did, and when', verdict || 'nothing shown');

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

// recordSockets counts handshakes and the codes they close with, and keeps
// every toast the page raised - a toast fades, and an assertion that has to
// catch it while it is on screen is an assertion that measures the clock.
async function recordSockets(page) {
  await page.addInitScript(() => {
    window.__opens = 0;
    window.__closed = [];
    window.__toasts = [];
    window.WebSocket = new Proxy(window.WebSocket, {
      construct(target, args) {
        window.__opens += 1;
        const ws = new target(...args);
        ws.addEventListener('close', (event) => window.__closed.push(event.code));
        return ws;
      },
    });
    addEventListener('DOMContentLoaded', () => {
      const host = document.getElementById('toasts');
      if (!host) return;
      new MutationObserver((records) => {
        for (const record of records) {
          for (const node of record.addedNodes) {
            if (node.textContent) window.__toasts.push(node.textContent);
          }
        }
      }).observe(host, { childList: true });
    });
  });
}

async function keybar() {
  const s = await start({ viewport: { width: 390, height: 844 }, touch: true });
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

    // A modifier is for the next key the person presses, and the terminal's
    // own reports are not keys. Leaving the pane for the field, and coming
    // back to it through the keyboard button, both make xterm send a focus
    // report on the same channel - and neither may spend an armed Ctrl.
    await s.page.click('#lineInput');
    await s.page.click('#keybar .key[data-key="Control"]');
    const beforeKeyboard = (await sentBytes(s.page)).length;
    await s.page.click('#keybar .key[data-key="Keyboard"]');
    await wait(200);
    const stillArmed = await s.page.$eval('#keybar .key[data-key="Control"]', (n) => n.className);
    ok(/\bon\b/.test(stillArmed), 'raising the keyboard does not spend the armed Ctrl', stillArmed);
    await s.page.keyboard.type('c');
    await wait(250);
    const throughFocus = (await sentBytes(s.page)).slice(beforeKeyboard);
    ok(throughFocus.includes('03'), 'and the letter after it is still sent as Ctrl-C',
      throughFocus.join(',') || 'nothing');

    // A locked Alt must not put an ESC in front of what the terminal answers.
    await s.page.click('#term .xterm-screen');
    await s.page.click('#keybar .key[data-key="Alt"]');
    await s.page.click('#keybar .key[data-key="Alt"]');
    const beforeBlur = (await sentBytes(s.page)).length;
    await s.page.click('#lineInput');
    await wait(250);
    const reports = (await sentBytes(s.page)).slice(beforeBlur);
    ok(!reports.some((hex) => hex.startsWith('1b1b')),
      'a locked Alt leaves the terminal\u2019s own reports alone', reports.join(',') || 'nothing');

    // And a bar that is put away takes its modifiers with it, rather than
    // transforming keys with nothing on screen to say why.
    await s.page.click('#sessionMenu');
    await s.page.click('.menu-item:has-text("Hide key bar")');
    await wait(200);
    const hidden = await s.page.evaluate(() => ({
      bar: document.getElementById('keybar').hidden,
      armed: [...document.querySelectorAll('#keybar .key.on, #keybar .key.lock')].length,
    }));
    ok(hidden.bar && hidden.armed === 0, 'hiding the key bar disarms it', JSON.stringify(hidden));
    await s.page.click('#sessionMenu');
    await s.page.click('.menu-item:has-text("Show key bar")');
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 5000 });

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

    // A line that came back from storage is a draft, not something in flight.
    // Sending it must leave nothing behind: a copy kept in localStorage would
    // be handed back on every later reload of this session, for ever.
    await s.page.fill('#lineInput', 'echo ghost-' + marker);
    await s.page.dispatchEvent('#lineInput', 'input');
    await s.page.click('#sendLine');
    await wait(600);
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(600);
    const ghost = await s.page.evaluate((id) => ({
      field: document.getElementById('lineInput').value,
      stored: localStorage.getItem('socrates.term.' + id + '.draft'),
    }), await s.page.evaluate(() => location.hash.slice(1)));
    ok(ghost.field === '' && !ghost.stored,
      'a line that was sent leaves nothing behind in storage', JSON.stringify(ghost));

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
    touch: true,
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
  const s = await start({ viewport: { width: 390, height: 844 } });
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
    await focusTerm(s.page);
    await s.page.keyboard.type(command, { delay: 25 });
    await s.page.keyboard.press('Enter');
    await wait(1200);
    // Held means not delivered: with no network the shell cannot have echoed
    // a character of it, and the screen must not pretend otherwise.
    const whileOffline = await screen(s.page);
    ok(!whileOffline.includes(command),
      'nothing typed with no network reached the pane', oneLine(whileOffline).slice(-90));

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
    // A line composed but never sent, and then the tab killed with no signal:
    // this is the ordinary iOS morning, and everything about it has to
    // survive.
    const back = 'echo back-' + tag;
    await s.context.setOffline(true);
    await s.page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await s.page.fill('#lineInput', back);
    await s.page.dispatchEvent('#lineInput', 'input');
    await wait(200);

    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newSession', { timeout: 20000 });
    await wait(1500);
    const offlineShell = await s.page.evaluate(() => ({
      terminal: typeof window.Terminal === 'function',
      hash: location.hash.slice(1),
      list: (document.querySelector('.list-empty') || {}).textContent || 'rows',
      empty: (document.querySelector('.empty-title') || {}).textContent || '',
    }));
    ok(offlineShell.terminal, 'the app shell opened with no network at all', JSON.stringify(offlineShell));
    ok(offlineShell.hash === id, 'and it still knows which session this tab was on', offlineShell.hash);
    ok(!/No sessions yet/.test(offlineShell.list) && !/No session open/.test(offlineShell.empty),
      'it does not claim there are no sessions when it could not ask',
      offlineShell.list + ' / ' + offlineShell.empty);

    await s.context.setOffline(false);
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForSelector('#term .xterm', { timeout: 30000 });
    await s.page.waitForFunction(() => document.getElementById('lineInput').value.length > 0,
      null, { timeout: 15000 }).catch(() => {});
    const restored = await s.page.evaluate(() => ({
      composer: !document.getElementById('composer').hidden,
      field: document.getElementById('lineInput').value,
    }));
    ok(restored.composer && restored.field === back,
      'the session reopened by itself and the unsent line is in the field', JSON.stringify(restored));

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
    await focusTerm(second);
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

/* ---------------------------------------------------- 17. offlinerestart */

// The worst ordinary case: the signal goes, something is typed, Socrates is
// restarted while there is no network, and then the phone comes back - which
// it does by raising `online`, `focus` and `visibilitychange` within the same
// few milliseconds. The page must reconnect on one handshake, tell the person
// what could not be delivered, and hand the composed line back rather than
// sitting behind "Reconnecting…" for ever.
async function offlinerestart() {
  const s = await start({ viewport: { width: 390, height: 844 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await recordSockets(s.page);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    await s.context.setOffline(true);
    await s.page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await s.page.waitForFunction(() => document.body.classList.contains('conn-lost'),
      null, { timeout: 20000 });

    // Both commands write to a file, because the pane cannot be counted on:
    // a burst delivered into bash is echoed by the tty and then redisplayed by
    // readline, so one delivery can appear three times on screen. The file is
    // what the shell actually did.
    const tag = Math.random().toString(36).slice(2, 8);
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    await s.page.keyboard.type('echo k >> ' + tag + '.txt', { delay: 20 });
    await s.page.keyboard.press('Enter');
    await s.page.fill('#lineInput', 'echo l >> ' + tag + '.txt');
    await s.page.click('#sendLine');
    await wait(300);


    // The server goes away and comes back while the browser cannot see it, so
    // the viewer it knew about is gone: this is the `viewer_fresh` path.
    const outcome = await s.restart();
    ok(outcome !== 'timeout', 'the server was restarted during the outage', JSON.stringify(outcome));

    const opensBefore = await s.page.evaluate(() => window.__opens);
    await s.context.setOffline(false);
    // The wake storm, in one tick, exactly as a phone produces it.
    await s.page.evaluate(() => {
      window.dispatchEvent(new Event('online'));
      window.dispatchEvent(new Event('focus'));
      document.dispatchEvent(new Event('visibilitychange'));
    });

    const live = await s.page.waitForFunction(() => !document.body.classList.contains('conn-lost'),
      null, { timeout: 20000 }).then(() => true).catch(() => false);
    ok(live, 'the page reconnected after the wake storm', live ? 'conn-lost cleared' : 'still reconnecting');

    const opened = await s.page.evaluate(() => window.__opens) - opensBefore;
    ok(opened <= 3, 'a storm of wake events is not a storm of handshakes', opened + ' handshakes');

    // Nothing may be lost in silence. Bytes that never reached a wire are
    // delivered once the counter is anchored; bytes that were on a wire when
    // the server forgot this viewer cannot be told apart from a resend, so
    // they are dropped and said out loud - keystrokes counted in a toast, a
    // composed line handed back to the field. Either outcome is honest; a
    // keystroke that is neither delivered nor mentioned is not, and neither
    // may run twice.
    await wait(2500);
    await typeLine(s.page, 'echo K=$(grep -c k ' + tag + '.txt) L=$(grep -c l ' + tag + '.txt)');
    const counted = await s.page.waitForFunction(() => {
      const rows = document.querySelector('#term .xterm-rows');
      const found = rows && rows.innerText.match(/K=(\d+) L=(\d+)/);
      return found ? found[1] + ',' + found[2] : null;
    }, null, { timeout: 25000 }).then((h) => h.jsonValue()).catch(() => 'no answer');
    const [ranK, ranL] = String(counted).split(',');
    const said = await s.page.evaluate(() => ({
      toasts: window.__toasts.slice(),
      field: document.getElementById('lineInput').value,
    }));
    ok(ranK === '1' || said.toasts.some((t) => /keystrokes? may not have been delivered/.test(t)),
      'the keystrokes typed in the outage ran once, or were said to be lost',
      'K=' + ranK + ' toasts=' + JSON.stringify(said.toasts));
    ok(ranL === '1' || said.field === 'echo l >> ' + tag + '.txt',
      'and the composed line ran once, or was handed back to the field',
      'L=' + ranL + ' field=' + JSON.stringify(said.field));
    ok(ranK !== undefined && Number(ranK) <= 1 && Number(ranL) <= 1,
      'and neither of them ran twice', 'K=' + ranK + ' L=' + ranL);

    // The session is still usable, which is what says the socket that came
    // back is a working one and not merely an open one.
    const after = 'after-' + tag;
    await typeLine(s.page, 'echo ' + after);
    ok(await awaitScreen(s.page, after, 25000), 'and the session works again',
      oneLine(await screen(s.page)));

    await shot(s.page, 'offlinerestart');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* -------------------------------------------------------- 18. latehello */

// A reload does not stop a person typing. The terminal is on screen before the
// socket has been told `hello`, and on a phone that round trip is not
// milliseconds - it is the tunnel. Everything typed in that window is held,
// and the anchor that arrives afterwards must renumber it and send it, not
// throw it away because the numbers it was given are below the ones the server
// has already written for this tab.
async function latehello() {
  const s = await start({ viewport: { width: 390, height: 844 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    // Type first, so that the server has a `lastInputSeq` well above zero for
    // this viewer. That is what makes the reload interesting: the counter in
    // the page starts again at nothing while the server's does not.
    const tag = Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo warm >> ' + tag + '.txt');
    ok(await awaitScreen(s.page, 'warm'), 'the session has taken input before the reload',
      oneLine(await screen(s.page)));

    // Every frame the server sends is held back for a second and a half, which
    // is what a bad line does to a handshake.
    await s.page.routeWebSocket(/\/ws\?/, (ws) => {
      const server = ws.connectToServer();
      ws.onMessage((message) => server.send(message));
      server.onMessage(async (message) => {
        await new Promise((resolve) => setTimeout(resolve, 1500));
        ws.send(message);
      });
    });

    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    // Before `hello`: the pane is there, the person types, and the socket has
    // no idea yet what number anything should carry.
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    await s.page.keyboard.type('echo k >> ' + tag + '.txt', { delay: 10 });
    await s.page.keyboard.press('Enter');
    await s.page.fill('#lineInput', 'echo l >> ' + tag + '.txt');
    await s.page.click('#sendLine');

    await s.page.waitForFunction(() => !document.body.classList.contains('conn-lost'),
      null, { timeout: 30000 }).catch(() => {});
    await wait(3000);

    // The shell counts, because the pane echoes a burst more than once.
    await typeLine(s.page, 'echo K=$(grep -c k ' + tag + '.txt) L=$(grep -c l ' + tag + '.txt)');
    const counted = await s.page.waitForFunction(() => {
      const rows = document.querySelector('#term .xterm-rows');
      const found = rows && rows.innerText.match(/K=(\d+) L=(\d+)/);
      return found ? found[1] + ',' + found[2] : null;
    }, null, { timeout: 25000 }).then((h) => h.jsonValue()).catch(() => 'no answer');
    ok(counted === '1,1', 'what was typed before hello ran, exactly once', String(counted));

    const left = await s.page.evaluate((id) => ({
      field: document.getElementById('lineInput').value,
      stored: localStorage.getItem('socrates.term.' + id + '.draft'),
      toasts: [...document.querySelectorAll('#toasts .toast')].map((n) => n.textContent),
    }), await s.page.evaluate(() => location.hash.slice(1)));
    ok(left.field === '' && !left.stored,
      'and the composed line left nothing behind in the field or in storage',
      JSON.stringify(left));

    await shot(s.page, 'latehello');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}


/* ------------------------------------------- §H.3 13-16: harness lifecycle */

// The four scenarios below are §H.3 rows 13 to 16. They are about what a
// harness *is* rather than about the terminal it draws in: the flags the
// launcher built, the conversation id each CLI keeps in a different place, and
// the resume that a reboot makes necessary. Everything they assert is read
// from the fake's own launch log or from the API, never from a screen - a
// banner can only say what the fake was told, and the point of these is what
// it was told with.

// sessionRow is the API's own answer about one session, which is the record
// the browser and the launcher have to agree with.
async function sessionRow(s, id) {
  const res = await s.context.request.get(s.url + '/api/sessions/' + encodeURIComponent(id));
  return (await res.json()).session;
}

// launchesOf is every time one fake CLI was started, oldest first.
const launchesOf = (s, name) => readFakeLog(s.data).filter((entry) => entry.name === name);
const lastLaunch = (s, name) => launchesOf(s, name).slice(-1)[0] || null;
const argvOf = (entry) => (entry && entry.argv ? entry.argv.join(' ') : '');

// journalOf is every byte the pane has printed, which is what a scenario about
// a launch has to read: the banner is written before the first viewer has
// resized the window, so the screen may have reflowed it away, while the
// journal is the record and cannot.
async function journalOf(s, id) {
  const res = await s.context.request.get(s.url + '/api/sessions/' + encodeURIComponent(id) + '/journal');
  return res.ok() ? res.text() : '';
}

// conversationOf asks the program itself which conversation it is in. The fake
// answers `/id` the way each real CLI knows its own session, and it is the only
// answer in this suite that does not come from Socrates - which is what makes
// it worth comparing a resume against.
async function conversationOf(page) {
  await typeLine(page, '/id');
  await awaitScreen(page, 'session ', 20000);
  const text = await screen(page);
  const found = /session (\S+)/.exec(text);
  return found ? found[1] : '';
}

// startWithModel drives the sheet the way `startSession` does and picks a
// model on the way through, which is the step §E.3 hides for Shell and which
// no other scenario exercises.
async function startWithModel(page, harness, modelLabel) {
  // The hash already names whatever session this tab is on, so the wait below
  // is for it to become a different one - a second session started in the same
  // tab is otherwise "finished" before it has begun.
  const was = await page.evaluate(() => location.hash.slice(1));
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.waitForSelector('#nsHarness .seg[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#nsHarness .seg[data-value="' + harness + '"]');
  if (modelLabel) {
    await page.click('#nsModel .combo-input');
    await page.waitForSelector('#nsModel .combo-option', { timeout: 5000 });
    await page.click('#nsModel .combo-option:has(.combo-label:text-is("' + modelLabel + '"))');
  }
  await page.click('#nsStart');
  await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
  await page.waitForFunction((before) => location.hash.length > 1 && location.hash.slice(1) !== before,
    was, { timeout: 30000 });
  return page.evaluate(() => location.hash.slice(1));
}

// waitForCLISession polls until the discoverer has learned which conversation
// this pane is holding. Codex and OpenCode both write nothing until a turn has
// happened, so the caller types one first and this is the wait afterwards.
//
// The id itself is deliberately not in the API - `cli_session_id` is
// `json:"-"` on the row - so `cli_session_state` reaching `known` is how the
// browser's side of the world learns that a conversation was found, and the
// resume argv is where the id itself is checked. `pending` is the watcher
// still looking, which is where a session sits until its first turn.
const CLI_FOUND = ['known', 'verified'];

async function waitForCLISession(s, id, timeout = 30000) {
  const until = Date.now() + timeout;
  let row = await sessionRow(s, id);
  while (Date.now() < until && !(row && CLI_FOUND.includes(row.cli_session_state))) {
    await wait(500);
    row = await sessionRow(s, id);
  }
  return row;
}

// waitForState polls the API until a session reaches one of the states asked
// for, and answers with the last row it saw either way.
async function waitForState(s, id, states, timeout = 40000) {
  const until = Date.now() + timeout;
  let row = await sessionRow(s, id);
  while (Date.now() < until && !(row && states.includes(row.state))) {
    await wait(500);
    row = await sessionRow(s, id);
  }
  return row;
}

// waitForAck polls until the server has lowered the resumed flag, which is
// what dismissing the banner asks it to do.
async function waitForAck(s, id, timeout = 10000) {
  const until = Date.now() + timeout;
  let row = await sessionRow(s, id);
  while (Date.now() < until && row && row.resumed !== false) {
    await wait(300);
    row = await sessionRow(s, id);
  }
  return row;
}

// endAndRestart is §E.7's exit overlay driven from the browser: the program is
// told to end, the overlay comes up, and Restart is pressed. It answers with
// the number of launches the fake had recorded before the restart, so the
// caller can say which record is the relaunch.
async function endAndRestart(s, name, code) {
  const before = launchesOf(s, name).length;
  await typeLine(s.page, '/exit ' + code);
  await s.page.waitForSelector('#termOverlay .overlay-card', { timeout: 25000 });
  const words = await s.page.$eval('#termOverlay .overlay-title', (n) => n.textContent);
  ok(/The session ended/.test(words), name + ': the overlay says the session ended', oneLine(words));
  await s.page.click('#termRestart');
  await s.page.waitForFunction(() => {
    const node = document.getElementById('termOverlay');
    return node && node.hidden;
  }, null, { timeout: 40000 });
  return before;
}

/* --------------------------------------------------------- 13. createclaude */

// Claude Code start to finish: the sheet offers the models the API named, the
// launcher is given the one that was picked, the uuid it fixes at creation is
// the uuid in the store, and the header says what this session runs. Then the
// program ends and Restart brings it back on the conversation it already had.
async function createclaude() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    // §E.3 step 3: the model list is the catalogue's, not a list in the page.
    // The catalogue is asked again first - the dashboard's Refresh - so what
    // the sheet shows is this machine's current answer.
    const refreshed = await s.context.request.post(s.url + '/api/harnesses/refresh');
    ok(refreshed.ok(), 'the catalogue can be refreshed on demand', String(refreshed.status()));
    const catalogue = await (await s.context.request.get(s.url + '/api/harnesses')).json();
    const claude = (catalogue.harnesses || []).find((h) => h.id === 'claude') || {};
    const offered = (claude.picks && claude.picks.length ? claude.picks : claude.models || [])
      .map((m) => m.label || m.id);

    await ensureNav(s.page);
    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await s.page.click('#nsHarness .seg[data-value="claude"]');
    await s.page.click('#nsModel .combo-input');
    await s.page.waitForSelector('#nsModel .combo-option', { timeout: 5000 });
    const shown = await s.page.$$eval('#nsModel .combo-option .combo-label', (nodes) => nodes.map((n) => n.textContent));
    ok(shown.length > 0 && shown.join(',') === offered.join(','),
      'the sheet offers exactly the models /api/harnesses reported for Claude Code',
      shown.join(',') + ' vs ' + offered.join(','));
    await s.page.click('#nsModel .combo-option:has(.combo-label:text-is("Haiku"))');
    await s.page.click('#nsStart');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 30000 });
    const id = await s.page.evaluate(() => location.hash.slice(1));

    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const row = await sessionRow(s, id);
    const banner = await journalOf(s, id);
    ok(/FAKE claude/.test(banner), 'the session is up', oneLine(banner));
    ok(/theme=light/.test(banner), 'and it was told the terminal is light',
      oneLine((/FAKE claude[^\r\n]*/.exec(banner) || [''])[0]));

    const launch = lastLaunch(s, 'claude');
    ok(!!launch, 'the fake recorded its launch', launch ? 'one record' : 'nothing in fake.log');
    ok(launch.cwd === row.workdir, 'it was started in the session’s own working directory',
      launch.cwd + ' vs ' + row.workdir);
    ok(/(^| )--model haiku( |$)/.test(argvOf(launch)), 'with the model that was picked in the sheet',
      argvOf(launch));
    const fixed = launch.argv[launch.argv.indexOf('--session-id') + 1];
    ok(launch.argv.includes('--session-id') && /^[0-9a-f-]{36}$/.test(fixed || ''),
      'and with a --session-id fixed at creation', fixed || 'none');
    const conversation = await conversationOf(s.page);
    ok(conversation === fixed, 'which is the conversation the program says it is in',
      conversation + ' vs ' + fixed);
    ok(row.cli_session_state === 'known' || row.cli_session_state === 'verified',
      'and the row knows it is holding one', row.cli_session_state);

    // §E.10 rule 2 and 3: the header names the model beside the mark of what
    // runs it, and the path is behind the "i".
    const badge = await s.page.evaluate(() => {
      const host = document.getElementById('sessionHarness');
      return {
        mark: (host.querySelector('.agent-mark') || { dataset: {} }).dataset.agent,
        model: (host.querySelector('.b-model') || {}).textContent,
        bubble: (host.querySelector('.tip-bubble') || {}).textContent || '',
        bubbleShown: host.querySelector('.tip-bubble')
          ? getComputedStyle(host.querySelector('.tip-bubble')).visibility : 'none',
      };
    });
    ok(badge.mark === 'claude' && badge.model === 'haiku',
      'the header badge carries the mark and the model', JSON.stringify(badge));
    ok(badge.bubble.includes(row.workdir) && badge.bubbleShown === 'hidden',
      'and the working directory only in the bubble', badge.bubbleShown + ' ' + oneLine(badge.bubble));
    await shot(s.page, 'createclaude');

    // The program ends and is started again from the browser. A restart is a
    // resume (§C.8), so the conversation it had is the conversation it gets.
    const before = await endAndRestart(s, 'claude', 3);
    ok(await awaitScreen(s.page, 'FAKE claude', 25000), 'Restart brought Claude Code back',
      oneLine(await screen(s.page)));
    const again = launchesOf(s, 'claude').slice(before);
    ok(again.length === 1 && argvOf(again[0]).includes('--resume ' + fixed),
      'and it was resumed on the conversation it already had', argvOf(again[0]));
    const back = await sessionRow(s, id);
    ok(back.state === 'running', 'the row is running again', back.state);

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 14. createcodex */

// Codex is the harness whose launch is mostly configuration: --strict-config
// makes an unknown key an error rather than a shrug, and the working directory
// has to be trusted in the same breath or the TUI opens on a prompt nobody can
// answer from a browser. Its conversation id exists only after a turn, so the
// scenario types one and waits for the watcher to find it.
async function createcodex() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startWithModel(s.page, 'codex', null);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const banner = await journalOf(s, id);
    ok(/FAKE codex/.test(banner), 'the session is up', oneLine(banner));
    ok(/theme=light/.test(banner), 'and it was told the terminal is light',
      oneLine((/FAKE codex[^\r\n]*/.exec(banner) || [''])[0]));

    const row = await sessionRow(s, id);
    const launch = lastLaunch(s, 'codex');
    ok(!!launch && launch.argv.includes('--strict-config'),
      'it was started with --strict-config', argvOf(launch));
    const trust = (launch.argv || []).find((arg) => arg.includes('trust_level')) || '';
    ok(trust.includes('trusted') && trust.includes(row.workdir),
      'and with this working directory trusted in the same command line', trust);
    ok(launch.cwd === row.workdir, 'and it is working where the row says it is',
      launch.cwd + ' vs ' + row.workdir);

    // A turn is what makes Codex write its rollout down; before one there is
    // nothing to discover and that is not a failure.
    ok(row.cli_session_state === 'pending', 'before a turn there is nothing to discover yet',
      row.cli_session_state);
    await typeLine(s.page, 'hello codex');
    ok(await awaitScreen(s.page, 'you said: hello codex'), 'the turn was taken',
      oneLine(await screen(s.page)));
    const found = await waitForCLISession(s, id, 30000);
    ok(CLI_FOUND.includes(found.cli_session_state),
      'and the conversation it wrote down was discovered', found.cli_session_state);
    const conversation = await conversationOf(s.page);
    ok(/^[0-9a-f-]{36}$/.test(conversation), 'the program names the conversation it is in',
      conversation || 'none');
    await shot(s.page, 'createcodex');

    const before = await endAndRestart(s, 'codex', 0);
    ok(await awaitScreen(s.page, 'FAKE codex', 25000), 'Restart brought Codex back',
      oneLine(await screen(s.page)));
    const again = launchesOf(s, 'codex').slice(before);
    ok(again.length === 1 && argvOf(again[0]).startsWith('resume ' + conversation),
      'and it was resumed on that same conversation', argvOf(again[0]));

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------- 15. createopencode */

// OpenCode keeps its session id nowhere a file watcher can reach it: the only
// way to learn it is to ask the TUI's own HTTP server, on the port Socrates
// chose, with the password Socrates generated. The fake answers 401 without
// it, so an id here is proof that the whole authenticated path worked.
async function createopencode() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startWithModel(s.page, 'opencode', null);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const banner = await journalOf(s, id);
    ok(/FAKE opencode/.test(banner), 'the session is up', oneLine(banner));
    ok(/theme=light/.test(banner), 'and it was told the terminal is light',
      oneLine((/FAKE opencode[^\r\n]*/.exec(banner) || [''])[0]));

    const row = await sessionRow(s, id);
    const launch = lastLaunch(s, 'opencode');
    const port = launch ? launch.argv[launch.argv.indexOf('--port') + 1] : '';
    ok(!!launch && launch.argv.includes('--port') && /^\d+$/.test(port || ''),
      'it was given a port of its own', argvOf(launch));
    const password = (launch.env || {}).OPENCODE_SERVER_PASSWORD || '';
    ok(password.length >= 16, 'and a password for the server on it',
      password ? password.length + ' characters' : 'not set');
    ok(launch.cwd === row.workdir, 'and it is working where the row says it is',
      launch.cwd + ' vs ' + row.workdir);

    await typeLine(s.page, 'hello opencode');
    ok(await awaitScreen(s.page, 'you said: hello opencode'), 'the turn was taken',
      oneLine(await screen(s.page)));
    const found = await waitForCLISession(s, id, 40000);
    ok(CLI_FOUND.includes(found.cli_session_state),
      'and the discoverer read the conversation over the authenticated HTTP server',
      found.cli_session_state);
    const conversation = await conversationOf(s.page);
    ok(/^ses_/.test(conversation), 'which is the session the program itself is in',
      conversation || 'none');
    await shot(s.page, 'createopencode');

    const before = await endAndRestart(s, 'opencode', 0);
    ok(await awaitScreen(s.page, 'FAKE opencode', 25000), 'Restart brought OpenCode back',
      oneLine(await screen(s.page)));
    const again = launchesOf(s, 'opencode').slice(before);
    ok(again.length === 1 && argvOf(again[0]).includes('--session ' + conversation),
      'and it was resumed on the session the server had named', argvOf(again[0]));

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------- 16. rebootresume */

// A reboot, without rebooting: the tmux server is killed behind Socrates' back
// and everything it was running goes with it. Nothing is relaunched eagerly -
// forty stored sessions must not become forty programs - so a session sits in
// needs_resume until somebody opens one, and opening one is what this scenario
// does.
//
// Claude Code is the harness it is done with, because it is the one that has a
// conversation on disk: coming back means coming back with **--resume** on the
// command line and not merely coming back. The banner that says so has to be
// shown, the conversation it names has to be behind the "i", and putting it
// away has to be remembered across a reload.
async function rebootresume() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    const claudeId = await startWithModel(s.page, 'claude', 'Sonnet');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(/FAKE claude/.test(await journalOf(s, claudeId)), 'the session is up',
      oneLine(await journalOf(s, claudeId)));
    const conversation = await conversationOf(s.page);
    ok(/^[0-9a-f-]{36}$/.test(conversation), 'which is holding a conversation',
      conversation || 'none');

    // The tab is put away first. With a page attached, the socket's own
    // reconnect would open the session again the moment the row flipped, and
    // the scenario would be measuring its own browser rather than the state a
    // rebooted machine is found in.
    await s.page.goto('about:blank', { waitUntil: 'domcontentloaded' });
    killTmux(s.data);

    const waiting = await waitForState(s, claudeId, ['needs_resume'], 40000);
    ok(waiting.state === 'needs_resume',
      'the session is waiting to be resumed, not running and not dead', waiting.state);

    // The reload. Opening the session is what resumes it, and the resume is
    // the whole of the handshake - so what the pane says while it waits is
    // "Resuming after a restart…", never "this session is not running".
    const beforeClaude = launchesOf(s, 'claude').length;
    await s.page.goto(s.url + '/#' + claudeId, { waitUntil: 'domcontentloaded' });
    await waitForState(s, claudeId, ['running'], 40000);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(/FAKE claude/.test((await journalOf(s, claudeId)).slice(-4000)),
      'opening it brought Claude Code back', oneLine((await journalOf(s, claudeId)).slice(-200)));

    const relaunch = launchesOf(s, 'claude').slice(beforeClaude);
    ok(relaunch.length === 1 && argvOf(relaunch[0]).includes('--resume ' + conversation),
      'and it came back with a resume on its command line, not a new conversation',
      argvOf(relaunch[0]));
    ok(!argvOf(relaunch[0]).includes('--session-id'),
      'the uuid it was created with was not handed to it a second time', argvOf(relaunch[0]));

    // §E.7: the banner is a thin line above the pane, it says what happened,
    // the conversation it names is behind the "i", and it never blocks.
    await s.page.waitForSelector('#termNotice:not([hidden])', { timeout: 20000 });
    const banner = await s.page.evaluate(() => {
      const host = document.getElementById('termNotice');
      const bubble = host.querySelector('.tip-bubble');
      return {
        kind: host.dataset.kind,
        words: host.querySelector('.notice-text').textContent,
        bubble: bubble ? bubble.textContent : '',
        bubbleShown: bubble ? getComputedStyle(bubble).visibility : 'none',
      };
    });
    ok(banner.kind === 'resumed' && /Resumed after a restart/.test(banner.words),
      'the banner says the session was resumed', JSON.stringify(banner));
    ok(!/could not be resumed/.test(banner.words),
      'and it does not claim the conversation was lost, because it was not', oneLine(banner.words));
    ok(banner.bubble.includes(conversation) && banner.bubbleShown === 'hidden',
      'the conversation it came back on is behind the "i"',
      banner.bubbleShown + ' ' + oneLine(banner.bubble));
    await shot(s.page, 'rebootresume');

    // Dismissing it is a decision the server keeps: the banner does not come
    // back on the next reload.
    await s.page.click('#termNotice .notice-close');
    await s.page.waitForFunction(() => document.getElementById('termNotice').hidden,
      null, { timeout: 5000 });
    const acked = await waitForAck(s, claudeId);
    ok(acked.resumed === false, 'putting the banner away cleared the flag behind it',
      JSON.stringify({ resumed: acked.resumed, resume_count: acked.resume_count }));
    ok(acked.resume_count === 1, 'and the resume was counted once', String(acked.resume_count));
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(1500);
    // A reload of a tab the server still remembers can raise the *desync*
    // notice, which is a different sentence about a different thing. What must
    // not come back is the resumed banner.
    const still = await s.page.$eval('#termNotice',
      (n) => (n.hidden ? 'hidden' : n.dataset.kind));
    ok(still !== 'resumed', 'and a reload does not show it again', still);

    // And the session is a working session, not a screen that came back: the
    // pane it was given is a new program, and it answers.
    await typeLine(s.page, 'hello again');
    ok(await awaitScreen(s.page, 'you said: hello again', 25000),
      'and the session that came back is one that works', oneLine(await screen(s.page)));

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 18. twoviewers */

// recordNotices keeps every notice the thin line above the terminal has shown,
// in order. It is an init script rather than a poll because the resized notice
// puts itself away after four seconds, and a scenario that only looked at the
// end would see nothing at all - or, worse, would count two of them as one.
function recordNotices() {
  window.__notices = [];
  const arm = () => {
    const host = document.getElementById('termNotice');
    if (!host) { setTimeout(arm, 50); return; }
    let showing = host.hidden ? '' : (host.dataset.kind || '') + '|' + host.textContent.trim();
    const look = () => {
      const now = host.hidden ? '' : (host.dataset.kind || '') + '|' + host.textContent.trim();
      if (now && now !== showing) {
        window.__notices.push({ kind: now.split('|')[0], text: host.textContent.trim() });
      }
      showing = now;
    };
    new MutationObserver(look).observe(host,
      { attributes: true, childList: true, subtree: true, characterData: true });
    look();
  };
  arm();
}

const noticeKinds = (page, kind) => page.evaluate((want) =>
  (window.__notices || []).filter((n) => n.kind === want), kind);

// Two devices on one session, which is the ordinary case for the person this
// is built for: a phone in a car and a laptop on a desk. Both see the same
// pane, the window is sized to whoever connected last, and the other one is
// told once - not on every keystroke, which is what tmux's own `latest`
// policy would have done (§A.7).
async function twoviewers() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await s.page.addInitScript(recordNotices);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    // The window is tmux's own answer rather than anything the page reports:
    // the browser is told the size, but only tmux knows what the window did.
    const name = 'soc_' + id;
    await wait(1200);
    const first = windowSize(s.data, name);
    ok(/^\d+x\d+$/.test(first), 'the window is sized to the viewer that opened it', first);

    // The second device is a different shape, so the window really moves.
    const second = await s.context.newPage();
    await second.setViewportSize({ width: 640, height: 480 });
    await second.goto(s.url + '/#' + id, { waitUntil: 'domcontentloaded' });
    await second.waitForSelector('#term .xterm', { timeout: 20000 });
    // Six seconds of watching what the window actually did. One device
    // connecting is one size change, and anything more would be a notice the
    // person did not need: the composer coming up, a font settling, a fit
    // arriving late.
    const seq = [first];
    for (let i = 0; i < 60; i += 1) {
      await wait(100);
      const now = windowSize(s.data, name);
      if (now !== seq[seq.length - 1]) seq.push(now);
    }
    const moved = seq[seq.length - 1];
    ok(seq.length === 2 && moved !== first,
      'the window moved exactly once, to the viewer that connected last', seq.join(' -> '));
    await s.page.waitForFunction(() => (window.__notices || [])
      .some((n) => n.kind === 'resized'), null, { timeout: 15000 }).catch(() => {});
    let notices = await noticeKinds(s.page, 'resized');
    ok(notices.length === 1, 'the first viewer was told once that the size moved',
      JSON.stringify(notices));
    ok(notices.length === 1 && notices[0].text.includes(moved.replace('x', '×')),
      'and told which size it moved to', notices.length ? notices[0].text : 'nothing');
    ok(await s.page.$eval('#termSize', (n) => n.textContent) === moved.replace('x', '×'),
      'the first viewer now shows the window the second one set',
      await s.page.$eval('#termSize', (n) => n.textContent));

    // One pane, two windows onto it: what either types, both see.
    const fromSecond = 'second-' + Math.random().toString(36).slice(2, 8);
    await second.click('#term .xterm-screen');
    await focusTerm(second);
    await second.keyboard.type('echo ' + fromSecond);
    await second.keyboard.press('Enter');
    ok(await awaitScreen(second, fromSecond, 20000), 'the second viewer drives the pane',
      oneLine(await screen(second)));
    ok(await awaitScreen(s.page, fromSecond, 20000), 'and the first one sees it too',
      oneLine(await screen(s.page)));

    const fromFirst = 'first-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + fromFirst);
    ok(await awaitScreen(s.page, fromFirst, 20000), 'the first viewer drives the pane',
      oneLine(await screen(s.page)));
    ok(await awaitScreen(second, fromFirst, 20000), 'and the second one sees it too',
      oneLine(await screen(second)));

    // §A.7 and decision J4 in two assertions: typing is not an explicit act,
    // so it moves neither the window nor anybody's notice line. Under tmux's
    // own `latest` policy the window would have flipped on every one of those
    // keystrokes.
    await wait(2500);
    ok(windowSize(s.data, name) === moved, 'typing on either device left the window alone',
      windowSize(s.data, name) + ', want ' + moved);
    notices = await noticeKinds(s.page, 'resized');
    ok(notices.length === 1, 'no keystroke produced a second resize notice',
      JSON.stringify(notices));
    const theirNotices = await noticeKinds(second, 'resized');
    ok(theirNotices.length === 0, 'and the viewer that owns the size was told nothing',
      JSON.stringify(theirNotices));

    await shot(s.page, 'twoviewers');
    await second.close();
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* -------------------------------------------------------- 19. backpressure */

// Two hundred lines as fast as a program can write them. The ring, the socket
// and the browser all have to agree about what was printed: a hole in the
// middle would mean the replay window moved under a reader that was behind,
// and a terminal that silently loses output is worse than one that stops.
async function backpressure() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(await awaitScreen(s.page, 'FAKE claude'), 'the fake CLI is at its prompt',
      oneLine(await screen(s.page)));

    await typeLine(s.page, '/spin');
    ok(await awaitScreen(s.page, 'spin 200', 40000), 'the last of the two hundred lines arrived',
      oneLine(await screen(s.page)));

    // What the pane printed, as the journal recorded it: the fake's own
    // numbering makes a hole impossible to miss.
    const { readFileSync } = await import('node:fs');
    const journal = join(s.data, 'sessions', id, 'journal.raw');
    let numbers = [];
    for (let i = 0; i < 40; i += 1) {
      const text = readFileSync(journal, 'latin1');
      numbers = [...text.matchAll(/spin (\d+)/g)].map((m) => Number(m[1]));
      if (numbers.length >= 200) break;
      await wait(250);
    }
    const missing = [];
    for (let n = 1; n <= 200; n += 1) if (numbers[n - 1] !== n) missing.push(n);
    ok(numbers.length === 200 && missing.length === 0,
      'the journal holds all two hundred lines, in order and once each',
      numbers.length + ' lines, first gap at ' + (missing[0] || 'none'));

    // And the screen is the tail of that same stream, with nothing skipped.
    const shown = [...(await screen(s.page)).matchAll(/spin (\d+)/g)].map((m) => Number(m[1]));
    const contiguous = shown.length > 1
      && shown.every((n, i) => i === 0 || n === shown[i - 1] + 1);
    ok(contiguous && shown[shown.length - 1] === 200,
      'the pane ends on line two hundred with no holes above it',
      shown.length ? shown[0] + '…' + shown[shown.length - 1] : 'nothing on screen');

    await shot(s.page, 'backpressure');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------ 20. deletekeepsdir */

// Delete is the only thing in Socrates that kills a tmux session, and the only
// thing it may take with it is the session: the work stays on disk. This is
// that promise measured from all three sides - the row, the tmux server and
// the file the person made - plus the journal they can take away first.
async function deletekeepsdir() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const { existsSync } = await import('node:fs');
    const row = await (await s.context.request.get(s.url + '/api/sessions/' + id)).json();
    const workdir = row.session.workdir;
    const kept = join(workdir, 'kept.txt');
    await typeLine(s.page, 'echo work-that-stays > kept.txt');
    for (let i = 0; i < 40 && !existsSync(kept); i += 1) await wait(250);
    ok(existsSync(kept), 'the session did some work in its directory', kept);
    ok(sessionsOn(s.data).includes('soc_' + id), 'and it has a tmux session',
      sessionsOn(s.data).join(',') || 'none');

    // The scrollback is downloadable while the session is there. The menu
    // item opens the endpoint in a tab, which is a browser download rather
    // than something a page can be asked about, so the item is checked on
    // screen and the bytes are fetched over the same signed-in context.
    const journal = await s.context.request.get(s.url + '/api/sessions/' + id + '/journal');
    const body = await journal.text();
    ok(journal.ok() && body.includes('kept.txt'),
      'the scrollback can be taken away before the session goes',
      journal.status() + ', ' + body.length + ' bytes');
    ok(/attachment/.test(journal.headers()['content-disposition'] || ''),
      'as an attachment', journal.headers()['content-disposition'] || 'no disposition');

    await s.page.click('#sessionList .chat-item[data-id="' + id + '"] .act');
    await s.page.waitForSelector('.menu', { timeout: 5000 });
    const items = await s.page.$$eval('.menu .menu-item', (nodes) =>
      nodes.map((n) => n.textContent.trim()));
    ok(items.includes('Download scrollback'), 'and the row is where it is offered',
      items.join(' | '));
    await s.page.click('.menu .menu-item:text-is("Delete")');
    await s.page.waitForSelector('.modal[open] .btn.danger', { timeout: 5000 });
    await s.page.click('.modal[open] .btn.danger');
    await s.page.waitForFunction((sel) => !document.querySelector(sel),
      '#sessionList .chat-item[data-id="' + id + '"]', { timeout: 10000 });

    const left = await (await s.context.request.get(s.url + '/api/sessions?scope=all')).json();
    ok((left.sessions || []).length === 0, 'the row is gone', (left.sessions || []).length + ' left');
    let live = sessionsOn(s.data);
    for (let i = 0; i < 20 && live.includes('soc_' + id); i += 1) {
      await wait(250);
      live = sessionsOn(s.data);
    }
    ok(!live.includes('soc_' + id), 'the tmux session was killed with it',
      live.join(',') || 'no tmux session left');
    ok(existsSync(workdir) && existsSync(kept), 'and the working directory was kept', kept);
    ok(!existsSync(join(s.data, 'sessions', id)),
      'while what Socrates itself wrote for the session is gone',
      join('sessions', id));

    await shot(s.page, 'deletekeepsdir');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------- 21. recoveredsession */

// A tmux session of ours with no row behind it - a restored database, a failed
// migration, a crash in the moment between the session appearing and the row
// being written. DECISIONS.md is explicit that it is never killed, so it is
// taken in instead, and the person can see it and decide (§A.8 step 5).
async function recoveredsession() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    // One ordinary session first, so the substrate is up and the hand made
    // one lands on the same server a person's would.
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const { execFileSync } = await import('node:child_process');
    const { mkdirSync: mkdir } = await import('node:fs');
    const stray = [...Array(32)].map(() => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');
    const dir = join(s.data, 'workspaces', 'stray');
    mkdir(dir, { recursive: true });
    execFileSync('tmux', ['-S', join(s.data, 'tmux.sock'), 'new-session', '-d',
      '-s', 'soc_' + stray, '-c', dir, 'sleep 600']);
    ok(sessionsOn(s.data).includes('soc_' + stray), 'a tmux session of ours has no row',
      sessionsOn(s.data).join(','));

    await s.restart();
    await open(s);
    await s.page.waitForFunction((want) => [...document.querySelectorAll('#sessionList .chat-item')]
      .some((n) => n.dataset.id === want), stray, { timeout: 20000 }).catch(() => {});

    const listed = await (await s.context.request.get(s.url + '/api/sessions?scope=all')).json();
    const found = (listed.sessions || []).find((row) => row.id === stray);
    ok(!!found, 'the orphan was taken in rather than killed', found ? found.id : 'not listed');
    ok(!!found && found.title === 'Recovered session', 'and it is called what it is',
      found ? found.title : 'no row');
    ok(!!found && found.state === 'running' && found.harness === 'shell',
      'running, as a shell, because nothing else can be known about it',
      found ? found.state + '/' + found.harness : 'no row');
    ok(!!found && found.workdir === dir, 'with the directory its pane is in',
      found ? found.workdir : 'no row');
    ok(sessionsOn(s.data).includes('soc_' + stray), 'and it was never killed',
      sessionsOn(s.data).join(','));
    ok(s.log.join('').includes('took in the tmux session'),
      'the start-up log says what happened', oneLine(s.log.join('')).slice(-140));

    const label = await s.page.$eval('#sessionList .chat-item[data-id="' + stray + '"] .label',
      (n) => n.textContent).catch(() => 'no row on screen');
    ok(label === 'Recovered session', 'the browser shows it in the list', label);
    ok(await s.page.$('#sessionList .chat-item[data-id="' + id + '"]') !== null,
      'beside the session that was there all along', id);

    await shot(s.page, 'recoveredsession');
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
  ['offlinerestart', 'a restart during an outage, a wake storm, and nothing lost in silence', offlinerestart],
  ['latehello', 'what is typed before a late hello is delivered, exactly once', latehello],
  ['adminoptions', 'every harness option round-trips and reaches the command line', adminoptions],
  ['tmuxinstaller', 'the engine card, and an install that streams and survives a reload', tmuxinstaller],
  ['twoviewers', 'two devices on one session, and one notice about the size', twoviewers],
  ['backpressure', 'two hundred lines arrive whole, on screen and in the journal', backpressure],
  ['deletekeepsdir', 'delete kills the tmux session and keeps the work', deletekeepsdir],
  ['recoveredsession', 'a tmux session with no row is taken in, never killed', recoveredsession],
  ['createclaude', 'Claude Code from the sheet to the command line, and back after an exit', createclaude],
  ['createcodex', 'Codex is trusted where it works, and its conversation is found', createcodex],
  ['createopencode', 'OpenCode names its session over its own authenticated server', createopencode],
  ['rebootresume', 'a machine that rebooted, and the session that comes back with its conversation', rebootresume],
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
