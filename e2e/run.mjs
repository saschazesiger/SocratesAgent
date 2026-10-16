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
  readFakeLog, killTmux, openRouterStub, fakeS3Switch, sessionsOn, windowSize, scratchDir, PASSWORD, LIVE, SHOTS,
} from './harness.mjs';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
// The one thing the harness cannot hand over: when a session was last used is
// the store's own record of what happened to it, so `folders` writes it in
// the store itself.
import { DatabaseSync } from 'node:sqlite';

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
async function startSession(page, harness, shellExecution = '') {
  // The hash already names whatever session this tab is on, so the wait below
  // is for it to become a different one: a second session started in the same
  // tab is otherwise "finished" before it has begun, and the id handed back is
  // the previous session's.
  const was = await page.evaluate(() => location.hash.slice(1));
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.waitForSelector('#newSessionSheet .seg[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#newSessionSheet .seg[data-value="' + harness + '"]');
  if (harness === 'shell' && shellExecution) {
    await page.click('#nsShellExecution .seg[data-value="' + shellExecution + '"]');
  }
  await pickModelIfNeeded(page);
  await page.click('#nsStart');
  await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
  await page.waitForFunction((before) => location.hash.length > 1 && location.hash.slice(1) !== before,
    was, { timeout: 30000 });
  return page.evaluate(() => location.hash.slice(1));
}

// pickModelIfNeeded does what the sheet's own hint tells a person to do when
// the chosen program does not name a default model: pick one. OpenCode is the
// case - `opencode models` is a list of ids and nothing in it is marked - and
// Start stays disabled until something is chosen, which is the design.
async function pickModelIfNeeded(page) {
  const stuck = await page.evaluate(() => {
    const start = document.getElementById('nsStart');
    const field = document.getElementById('nsModelField');
    return !!start && start.disabled && !!field && !field.hidden;
  });
  if (!stuck) return;
  await page.click('#nsModel .combo-input');
  await page.waitForSelector('#nsModel .combo-option', { timeout: 5000 });
  await page.click('#nsModel .combo-option');
  await page.waitForFunction(() => !document.getElementById('nsStart').disabled, null, { timeout: 5000 });
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

// packed is the pane with every space and line break taken out. A narrow pane
// wraps a long line wherever the column runs out, so a scenario that is asking
// which characters arrived - rather than how they were laid out - has to read
// it this way or it is asserting about the width of a phone.
const packed = async (page) => (await screen(page)).replace(/\s+/g, '');

// awaitPacked is awaitScreen against that reading.
async function awaitPacked(page, needle, timeout = 20000) {
  const want = needle.replace(/\s+/g, '');
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if ((await packed(page)).includes(want)) return true;
    await wait(200);
  }
  return false;
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

// The top bar's ⋮ is this device's options: switches, in a menu that stays
// open while they are pressed. openOptions opens it when it is not open.
async function openOptions(page) {
  if (await page.$('.menu [data-option]')) return;
  await page.click('#optionsBtn');
  await page.waitForSelector('.menu [data-option]', { timeout: 5000 });
}

// option presses one switch of the options menu and answers where it now is.
// A switch that waits on the browser - notifications - is waited for by the
// caller.
async function option(page, name) {
  await openOptions(page);
  await page.click('.menu [data-option="' + name + '"]');
  return page.$eval('.menu [data-option="' + name + '"]', (n) => n.getAttribute('aria-checked') === 'true');
}

// options is every switch of the menu as it is drawn: on or off, and whether
// it can be pressed.
async function options(page) {
  await openOptions(page);
  return page.$$eval('.menu [data-option]', (nodes) => Object.fromEntries(nodes.map((n) => [n.dataset.option, {
    on: n.getAttribute('aria-checked') === 'true',
    disabled: n.disabled,
    text: n.textContent.trim(),
  }])));
}

// closeOptions puts the menu away the way a key does.
async function closeOptions(page) {
  if (!(await page.$('.menu'))) return;
  await page.keyboard.press('Escape');
  await page.waitForSelector('.menu', { state: 'detached', timeout: 5000 });
}

// rowMenu opens the "…" of the session on screen, in the list - where what
// can be done to a session is, now that the top bar's ⋮ is this device's.
async function rowMenu(page) {
  await ensureNav(page);
  const id = await page.evaluate(() => location.hash.slice(1));
  await page.click('#sessionList .chat-item[data-id="' + id + '"] .act');
  await page.waitForSelector('.menu .menu-item', { timeout: 5000 });
}

// contrastOf is the WCAG ratio between two colours as the browser reports
// them - `rgb(r, g, b)` - which is term.js's contrast() reading computed style
// instead of hex. A ratio is the only honest way to assert that a mark can be
// seen: "it is a light grey" is a fact about the token, not about the eye.
const contrastOf = (a, b) => {
  const lum = (colour) => {
    const [r, g, b] = (colour.match(/[\d.]+/g) || ['0', '0', '0']).slice(0, 3)
      .map((n) => Number(n) / 255)
      .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
};

// firstRow is the topmost line the pane is showing. It is the assertion that
// catches a window that was resized under the program: tmux reflows on a
// shrink, and on 3.6 that puts the head of the first wrapped line into the
// scrollback, so what is left at the top is the middle of a sentence.
const firstRow = (page) => page.evaluate(() => {
  const rows = document.querySelector('#term .xterm-rows');
  if (!rows) return '';
  for (const row of rows.children) {
    const text = row.innerText.replace(/\u00a0/g, ' ').trimEnd();
    if (text.trim()) return text;
  }
  return '';
});

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

    const id = await startSession(s.page, 'shell', 'container');
    ok(!!id, 'the sheet made a session and opened it', id || 'no id in the hash');
    const created = await (await s.context.request.get(s.url + '/api/sessions/' + id)).json();
    ok(created.session?.options?.shell_execution === 'container',
      'the shell toggle saved container execution', JSON.stringify(created.session?.options));

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

    // The header is the session's name: what it runs is the list row's mark.
    const header = await s.page.evaluate(() => ({
      mark: !!document.querySelector('.topbar .agent-mark'),
      title: (document.getElementById('sessionTitle') || {}).textContent,
      size: !!document.getElementById('termSize'),
    }));
    ok(!header.mark, 'the header carries no harness mark', String(header.mark));
    ok(/Shell/.test(header.title || ''), 'the session is named after what it runs', header.title);
    ok(!header.size, 'and no size: the pane\'s columns and rows are not the person\'s business', String(header.size));

    await shot(s.page, 'createshell');
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
      // Every harness with its own mark: §E.10 rule 2. The agents come first
      // and the shell, which is not one, is the row beneath them. VS Code is
      // not on the sheet: a folder's editor is opened from the folder.
      const marks = await s.page.$$eval('#nsHarness .seg .agent-mark, #nsTools .seg .agent-mark',
        (nodes) => nodes.map((n) => n.dataset.agent));
      ok(marks.join(',') === 'claude,codex,opencode,shell',
        `the sheet offers the agents and then the tools, each with its mark at ${tag}`,
        marks.join(',') || 'none');

      // Nothing on the sheet is hover-only any more: a choice is a mark and a
      // name, and the prose that explained the choice is gone with it.
      const bare = await s.page.evaluate(() => ({
        tips: document.querySelectorAll('#newSessionSheet .tip').length,
        advanced: !!document.getElementById('nsAdvanced'),
        prose: (/Advanced|runs with the options|resumed by the session|launched with its workspace|password-protected/i
          .exec(document.getElementById('newSessionSheet').textContent) || [''])[0],
      }));
      ok(bare.tips === 0 && !bare.advanced && bare.prose === '',
        `the sheet has no "i", no Advanced and no explanations at ${tag}`, JSON.stringify(bare));

      // Where a session works is a dropdown, not a row of cells: a machine with
      // a dozen preset directories on it still has one control the width of
      // the sheet, and the field says which place is chosen.
      const dir = await s.page.evaluate(() => {
        const host = document.getElementById('nsDir');
        const input = host.querySelector('.combo-input');
        const box = host.getBoundingClientRect();
        return {
          combo: !!host.querySelector('.combo'),
          segs: host.querySelectorAll('.seg').length,
          value: input ? input.value : '',
          width: Math.round(box.width),
          sheet: Math.round(document.getElementById('newSessionSheet').getBoundingClientRect().width),
        };
      });
      ok(dir.combo && dir.segs === 0 && dir.value === 'Dynamic',
        `the working directory is a dropdown, starting on Dynamic at ${tag}`, JSON.stringify(dir));
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
      const session = await startSession(s.page, id);
      await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
      // The first line the program printed is still the first line on screen.
      // A session created at a size nobody measured is resized by its own
      // first viewer, and a tmux window that shrinks reflows: the head of the
      // banner used to go into the scrollback before anybody had read it.
      await awaitScreen(s.page, expect || '$', 20000);
      const top = await firstRow(s.page);
      ok(expect ? top.startsWith(expect) : /[$#>]\s*$/.test(top),
        'what ' + id + ' printed first is what the pane shows first', top);
      if (expect) {
        // From the journal, not the screen: the banner is printed before the
        // first viewer has resized the window, so an attach redraw can reflow
        // it away while the record of it cannot be lost.
        ok(await journalSays(s, session, expect), id + ' started and printed its banner',
          oneLine(await journalOf(s, session)));
        ok(await journalSays(s, session, 'theme=light'),
          id + ' was told the terminal is light', oneLine(await journalOf(s, session)));
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
      dots: n.querySelectorAll('.dot').length,
      tips: n.querySelectorAll('.tip').length,
      words: n.querySelector('.label').textContent,
    })));
    ok(rows.length === 4, 'all four sessions are in the list', rows.length + ' rows');
    ok(rows.every((r) => r.mark), 'every row carries the mark of what it runs',
      rows.map((r) => r.mark).join(','));
    // A row is a mark, a name and its menu. The state dot said nothing that
    // the mark and the words do not, and the "i" moved into the menu.
    ok(rows.every((r) => r.dots === 0 && r.tips === 0),
      'no row carries a state dot or an "i"',
      rows.map((r) => r.dots + '/' + r.tips).join(','));
    // §E.10 rule 3: the technical detail is behind Info, never in the words.
    ok(rows.every((r) => !r.words.includes('/')), 'no row spells out a path in its words',
      rows.map((r) => r.words).join(' | '));

    // Info is where the facts of a session are: one dialog, opened from the
    // row's own menu.
    await s.page.click('#sessionList .chat-item .act');
    await s.page.click('.menu-item:has-text("Info")');
    await s.page.waitForSelector('dialog.modal .facts', { timeout: 5000 });
    const detail = await s.page.$eval('dialog.modal', (n) => ({
      title: n.querySelector('.modal-title').textContent,
      facts: [...n.querySelectorAll('.fact')].map((f) => f.textContent),
    }));
    ok(detail.facts.some((f) => f.includes('/')),
      'the working directory is in the Info dialog', JSON.stringify(detail));
    await s.page.click('dialog.modal .modal-actions .btn');
    await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 5000 });

    await shot(s.page, 'harnesses');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 6. sessionlist */

// What a row can do to a session: rename it, put it away, take it back out,
// and delete it - which is the only thing in Socrates that kills a tmux
// session, and which keeps the working directory.
// Exercise real popup geometry and font measurements independently of the
// fonts installed in the host browser. This is not a Tesla firmware emulator.
async function browserlayout() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm');
    const row = '#sessionList .chat-item[data-id="' + id + '"]';
    const geometry = () => s.page.evaluate((sel) => {
      const box = document.querySelector(sel + ' .act').getBoundingClientRect();
      const menu = document.querySelector('.menu');
      const rect = menu.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right,
        anchorTop: box.top, anchorBottom: box.bottom, height: innerHeight, width: innerWidth,
        scrollable: menu.scrollHeight > menu.clientHeight };
    }, row);
    await s.page.click(row + ' .act');
    await wait(180);
    let g = await geometry();
    ok(g.top >= g.anchorBottom && g.bottom <= g.height - 9,
      'an upper row opens downwards inside the viewport', JSON.stringify(g));
    await s.page.mouse.click(750, 75);
    await s.page.$eval(row, (node) => {
      node.style.marginTop = (document.querySelector('#sessionList').clientHeight - node.offsetHeight - 40) + 'px';
    });
    await s.page.click(row + ' .act');
    await wait(180);
    g = await geometry();
    ok(g.bottom <= g.anchorTop && g.top >= 10,
      'a lower row opens upwards inside the viewport', JSON.stringify(g));
    await s.page.setViewportSize({ width: 800, height: 240 });
    await s.page.$eval(row, (node) => { node.style.marginTop = '0px'; });
    await wait(200);
    g = await geometry();
    ok(g.top >= 9 && g.bottom <= g.height - 9 && g.left >= 9 && g.right <= g.width - 9 && g.scrollable,
      'an open menu stays visible and scrollable in a short viewport', JSON.stringify(g));
    await s.page.$eval('.menu', (node) => { node.scrollTop = node.scrollHeight; });
    await wait(100);
    const scrolled = await s.page.$eval('.menu', (node) => node.scrollTop > 0
      && node.lastElementChild.getBoundingClientRect().bottom <= node.getBoundingClientRect().bottom);
    ok(scrolled, 'the last menu action remains reachable by scrolling', String(scrolled));
    await s.page.mouse.click(750, 75);
    await s.page.setViewportSize({ width: 1280, height: 720 });

    // Delay both webfonts on reload: the existing session must only attach
    // after loading, otherwise xterm measures the system fallback first.
    await s.page.evaluate(async () => {
      for (const registration of await navigator.serviceWorker.getRegistrations()) await registration.unregister();
    });
    let fontResponses = 0;
    await s.page.route('**/*.woff2*', async (route) => {
      await wait(400);
      await route.continue();
      fontResponses++;
    });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm');
    const fonts = await s.page.evaluate(async () => {
      const url = document.querySelector('script[type="module"]').src.replace('session.js', 'term.js');
      const { createTerm, measurePane } = await import(url);
      const host = document.createElement('div');
      host.style.cssText = 'position:fixed;width:600px;height:300px;top:0;left:0';
      document.body.append(host);
      const results = [];
      for (const webgl of [false, true]) {
        const initial = measurePane(host, { webgl });
        const pane = createTerm(host, { webgl });
        const ctx = document.createElement('canvas').getContext('2d');
        ctx.font = '14px ' + pane.term.options.fontFamily;
        const widths = [...'iWm0 .'].map((ch) => ctx.measureText(ch).width);
        results.push({ family: pane.term.options.fontFamily, widths,
          cell: pane.term._core._renderService.dimensions.css.cell.width,
          sameSize: initial.cols === pane.term.cols && initial.rows === pane.term.rows });
        pane.dispose();
      }
      host.remove();
      return results;
    });
    ok(fontResponses === 2 && fonts.every((f) => f.family.startsWith('"Source Code Pro"') && f.sameSize
      && Math.max(...f.widths) - Math.min(...f.widths) < 0.01 && Math.abs(f.cell - f.widths[0]) < 1),
      'delayed local fonts give DOM and WebGL matching monospaced cells and initial sizes', JSON.stringify({ fontResponses, fonts }));
    await typeLine(s.page, 'echo font-layout-ok');
    ok(await awaitScreen(s.page, 'font-layout-ok'), 'the terminal still accepts input after reload', 'font-layout-ok');
    await shot(s.page, 'browserlayout');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

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

    // The same menu for a row that has been filed onto a shelf, which starts
    // shut: the shelf is opened first, the way a person would find the row.
    const headMenu = async (label) => {
      const shelf = await s.page.$eval(rowSelector, (row) => (row.checkVisibility()
        ? '' : (row.closest('.shelf') || { dataset: {} }).dataset.shelf || ''));
      if (shelf) {
        await s.page.click('.shelf[data-shelf="' + shelf + '"] > .folder-bar > .folder-head');
        await s.page.waitForFunction((sel) => document.querySelector(sel)?.checkVisibility(),
          rowSelector, { timeout: 5000 });
      }
      await menu(label);
    };
    const statusShown = (sel) => s.page.evaluate((sel) => {
      const row = document.querySelector(sel);
      const shown = (node) => !!node && !node.hidden && getComputedStyle(node).display !== 'none';
      const mark = row && row.querySelector('.row-status');
      const head = document.getElementById('sessionStatus');
      return {
        row: shown(mark) ? mark.dataset.status : '',
        head: shown(head) ? head.textContent : '',
        folder: row ? row.closest('.folder').dataset.folder || '' : '',
        shelf: row ? (row.closest('.shelf') || { dataset: {} }).dataset.shelf || '' : '',
        folderOpen: !!row && row.checkVisibility(),
      };
    }, sel);
    const current = async () => (await (await s.context.request.get(s.url + '/api/sessions/' + id)).json()).session;

    // A session is born in progress, which carries no mark at all.
    const born = await statusShown(rowSelector);
    ok(!born.row && !born.head, 'a session is born in progress, with no mark', JSON.stringify(born));

    // Review pending: a mark after the name and a word in the header.
    await menu('Review pending');
    await s.page.waitForFunction((sel) => document.querySelector(sel + ' .row-status')?.dataset.status === 'review',
      rowSelector, { timeout: 8000 });
    const review = await statusShown(rowSelector);
    ok(review.row === 'review' && review.head === 'Review pending',
      'Review pending marks the row and the header', JSON.stringify(review));
    await shot(s.page, 'sessionlist-review');
    const filed = await current();
    ok(filed.status === 'review', 'the status was stored', filed.status);

    // Background: out of the way on a shelf at the bottom of its own folder
    // that starts shut, and still running.
    await headMenu('Background');
    await s.page.waitForFunction((sel) => document.querySelector(sel)?.closest('.shelf')?.dataset.shelf === 'background',
      rowSelector, { timeout: 8000 });
    const background = await statusShown(rowSelector);
    ok(background.shelf === 'background' && !background.folderOpen,
      'a background session goes onto its folder’s Background shelf, shut', JSON.stringify(background));
    const running = await current();
    ok(running.status === 'background' && running.state === 'running',
      'it is filed as background and still running', running.state);

    // Done: the terminal is closed, the session stays, and says when it goes.
    await headMenu('Done');
    await s.page.waitForFunction(() => /is done/.test(document.getElementById('termOverlay')?.textContent || ''),
      null, { timeout: 10000 });
    const done = await current();
    ok(done.status === 'done' && done.state === 'needs_resume' && done.delete_at > Date.now(),
      'Done closes the terminal and keeps the session for a while', done.state + ' until ' + done.delete_at);
    const overlay = await s.page.$eval('#termOverlay', (n) => n.textContent);
    ok(/Deleted in/.test(overlay), 'the page says when a done session is deleted', oneLine(overlay));
    await shot(s.page, 'sessionlist-done');

    // Picked up again, it resumes on the same row.
    await s.page.click('#termOverlay .btn.primary');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const back = await current();
    ok(back.status === 'in_progress', 'picking a done session up puts it back in progress', back.status);

    // Delete: the row goes, the tmux session goes, the directory stays.
    const workdir = back.workdir;
    await menu('Delete');
    await s.page.waitForSelector('.modal[open] .btn.danger', { timeout: 5000 });
    const body = await s.page.$eval('.modal[open] .modal-body', (n) => n.textContent);
    ok(/working directory/i.test(body), 'the dialog says the working directory is kept', oneLine(body));
    await s.page.click('.modal[open] .btn.danger');
    await s.page.waitForFunction((sel) => !document.querySelector(sel), rowSelector, { timeout: 10000 });
    const left = await (await s.context.request.get(s.url + '/api/sessions')).json();
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
    // The banner is read out of the journal rather than off the screen: it is
    // written before the first viewer has resized the window, so tmux's
    // attach redraw can reflow it away, and this assertion failed that way
    // intermittently.
    ok(await journalSays(s, id, 'FAKE claude', 1), 'the session is up',
      oneLine(await journalOf(s, id)));

    await typeLine(s.page, '/exit 7');
    await s.page.waitForSelector('#termOverlay .overlay-card', { timeout: 20000 });
    const overlay = await s.page.evaluate(() => {
      const card = document.querySelector('#termOverlay .overlay-card');
      return {
        words: card.querySelector('.overlay-title').textContent,
        detail: (card.querySelector('.overlay-detail') || {}).textContent || '',
        tips: card.querySelectorAll('.tip').length,
        buttons: [...card.querySelectorAll('.overlay-actions .btn')].map((b) => b.textContent),
      };
    });
    ok(/The session ended/.test(overlay.words), 'the overlay says the session ended', oneLine(overlay.words));
    ok(/Exit status 7/.test(overlay.detail) && overlay.tips === 0,
      'the exit status is a plain line under it, and there is no "i" left',
      JSON.stringify(overlay));
    ok(overlay.buttons.join(',') === 'Restart,Delete', 'the overlay offers Restart and Delete',
      overlay.buttons.join(','));
    await shot(s.page, 'exitoverlay');

    const said = await s.page.$eval('#sessionList .chat-item .row-mark', (n) => n.title);
    ok(said === 'Ended', 'the row says the session ended', said);

    await s.page.click('#termRestart');
    await s.page.waitForFunction(() => {
      const overlayNode = document.getElementById('termOverlay');
      return overlayNode && overlayNode.hidden;
    }, null, { timeout: 30000 });
    // Twice now: the journal is one file across the restart, so the second
    // banner is the proof that a second program was started into the pane.
    ok(await journalSays(s, id, 'FAKE claude', 2), 'Restart brought the session back',
      oneLine(await journalOf(s, id)));
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

/* -------------------------------------------------------- 8b. touchscroll */

// A phone has no wheel, and xterm 6 has no gesture of its own: it draws its
// viewport with VS Code's scrollable element, which listens for `wheel` and for
// nothing else. So a finger dragged down the pane used to move nothing at all,
// and everything that had scrolled past the top of a session was out of reach
// from the only device this product is carried on.
//
// The scrollback under test here is tmux's rather than xterm's - `mouse on` is
// the default, so the pane is an alternate screen and the wheel is a mouse
// report - which is exactly the path a desk with a real wheel already takes.
async function touchscroll() {
  const s = await start({ viewport: { width: 390, height: 844 }, touch: true });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    // The browser is told the vertical axis is not its own, or it takes the
    // drag for a pan of the page before the first touchmove is delivered.
    const axis = await s.page.$eval('#term', (n) => getComputedStyle(n).touchAction);
    ok(/pan-x/.test(axis) && !/pan-y/.test(axis),
      'the browser does not take a vertical drag over the pane for itself', axis);

    // More lines than the pane is tall, so the early ones are only reachable by
    // scrolling. `mark-1` is written as its own row, and matched as one, so it
    // is never confused with `mark-10`.
    await typeLine(s.page, 'i=1; while [ $i -le 120 ]; do echo "mark-$i"; i=$((i+1)); done');
    ok(await awaitScreen(s.page, 'mark-120'), 'the shell printed past the bottom of the pane',
      oneLine(await screen(s.page)));
    const before = await screen(s.page);
    ok(!/^mark-1$/m.test(before), 'and the first line has gone off the top',
      oneLine(before.split('\n').filter((l) => l.trim())[0] || ''));

    const box = await s.page.$eval('#term .xterm-screen', (n) => {
      const r = n.getBoundingClientRect();
      return { x: Math.round(r.x + r.width / 2), top: Math.round(r.y + 60), bottom: Math.round(r.bottom - 60) };
    });
    const cdp = await s.context.newCDPSession(s.page);
    // A finger, delivered by Chrome's own touch pipeline rather than made up in
    // the page: `touch-action` and any gesture the browser might have claimed
    // are part of what this proves.
    const drag = async (from, to, steps = 15) => {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x, y: from }] });
      for (let i = 1; i <= steps; i++) {
        const y = Math.round(from + ((to - from) * i) / steps);
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: box.x, y }] });
        await wait(16);
      }
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
      await wait(350);
    };

    // A tap is not a drag. Under the slop nothing is sent, nothing is
    // prevented, and the tap is still the click that puts the keyboard on the
    // pane - which is the one thing a scroll must not cost.
    await drag(box.top, box.top + 4, 4);
    const tapped = await s.page.evaluate(() => {
      const rows = document.querySelector('#term .xterm-rows');
      return { screen: rows ? rows.innerText : '', focused: document.activeElement === document.querySelector('#term .xterm-helper-textarea') };
    });
    ok(tapped.screen === before && tapped.focused, 'a tap still lands as a tap and scrolls nothing',
      'focused=' + tapped.focused + ' moved=' + (tapped.screen !== before));

    // Down the screen: earlier lines come down with the finger.
    await drag(box.top, box.bottom);
    const back = await screen(s.page);
    const top = (text) => Number((text.match(/^mark-(\d+)/m) || [0, 0])[1]);
    ok(top(back) > 0 && top(back) < top(before),
      'a finger dragged down the pane reaches what had scrolled off',
      'mark-' + top(before) + ' -> mark-' + top(back));
    ok(Math.abs((top(before) - top(back)) - Math.round((box.bottom - box.top) / 16)) < 30,
      'and it moves about as far as the finger did',
      (top(before) - top(back)) + ' lines for ' + (box.bottom - box.top) + 'px');

    // And up again: the same gesture the other way ends back at the bottom,
    // which is also how tmux leaves copy mode.
    await drag(box.bottom, box.top);
    await drag(box.bottom, box.top);
    const bottom = await screen(s.page);
    ok(/^mark-120/m.test(bottom), 'and dragging back up returns to the live bottom of the pane',
      oneLine(bottom.split('\n').filter((l) => l.trim()).slice(-1)[0] || ''));

    // And the one gesture that is deliberately swallowed. With tmux's mouse
    // turned off in the dashboard there is no mouse report to send and no
    // scrollback of xterm's own to move - a pane under tmux is an alternate
    // screen - so a wheel is arrow keys, which is a fair reading of a wheel
    // deliberately turned in `less` and a terrible one of the gesture a phone
    // scrolls everything with. A drag must not walk the shell's history onto
    // the prompt.
    const off = await s.context.request.put(s.url + '/api/settings', {
      data: { settings: { terminal: { webgl: false, mouse: false } } },
    });
    ok(off.ok(), 'the dashboard turned tmux\'s mouse off', String(off.status()));
    await s.page.waitForFunction(
      () => !document.querySelector('#term .xterm').classList.contains('enable-mouse-events'),
      null, { timeout: 10000 });
    const settled = await screen(s.page);
    await drag(box.top, box.bottom);
    const quiet = await screen(s.page);
    ok(quiet === settled, 'with no mouse to report to, a drag scrolls nothing and types nothing',
      oneLine(quiet.split('\n').filter((l) => l.trim()).slice(-1)[0] || ''));

    await cdp.detach();
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------------- 9. live */

// One real session against a real, logged in CLI. It types `/status`, which
// costs nothing: it must never spend tokens on a model turn.
//
// "Something was rendered" is not enough, and was not: Claude Code used to
// come up on its own blocking workspace question - "Accessing workspace: … Is
// this a project you created or one you trust?", highlighted answer **No,
// exit** - because a dynamic workspace directory is one it has never been
// opened in and nothing had marked it trusted. The pane was full of text and
// the row said `running`, and the session was unusable. So this asserts on the
// things that tell a started Claude Code from a stopped one: its own banner,
// and the absence of every full-screen question that can stand in front of it.
//
// The other two screens of the same shape are asserted with it. They are not
// bugs Socrates can fix - a fresh HOME has to be signed in by hand - but they
// are the same failure to a person, so a run on a machine that was never
// logged in says which one it stopped on instead of "something was rendered".
// "Detected a custom API key" is a bug Socrates can fix, and does: the pane's
// ANTHROPIC_API_KEY is emptied so an ambient key cannot stop a session.
const CLAUDE_BLOCKING_SCREENS = [
  'Accessing workspace',        // the workspace-trust question
  'Detected a custom API key',  // an ambient ANTHROPIC_API_KEY
  'Choose the text style',      // first-run onboarding
];

async function livesession() {
  const s = await start({ viewport: { width: 1280, height: 720 }, live: true });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 60000 });
    const started = await awaitScreen(s.page, 'Claude Code v', 45000);
    const text = await screen(s.page);
    ok(text.trim().length > 0, 'the real CLI rendered something in the browser', oneLine(text));
    ok(started, 'Claude Code reached its own prompt in a directory it has never seen',
      oneLine(text.split('\n').find((l) => l.includes('Claude Code v')) || text));
    const blocked = CLAUDE_BLOCKING_SCREENS.filter((question) => text.includes(question));
    ok(blocked.length === 0,
      'and no full-screen question stood in front of it - trust, an ambient API key, or onboarding',
      blocked.join(', ') || 'none of ' + CLAUDE_BLOCKING_SCREENS.length);
    await typeLine(s.page, '/status');
    await wait(4000);
    const after = await screen(s.page);
    ok(after.trim().length > 0, 'and it answered a slash command that costs nothing', oneLine(after));
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}


/* ------------------------------------------------------- 10. adminoptions */

// The whole of what a harness card offers: the binary path, the model short
// list, and the model and effort a session starts on. Set it in the page,
// save, reload, and find it still there - then start a session and read the
// command line the launcher actually built out of FAKE_LOG.
//
// The controls are addressed by the id the dashboard derives from the storage
// key, `opt-<harness>-<key>`.
const ADMIN_OPTIONS = [
  ['claude', 'default_model', 'text', 'opus'],
  ['claude', 'default_effort', 'select', 'high'],
  ['codex', 'default_model', 'text', 'gpt-5.6-terra'],
  ['codex', 'default_effort', 'select', 'xhigh'],
  ['opencode', 'default_model', 'text', 'anthropic/claude-sonnet-4-5'],
];

// The flags every session of a program is started with, whatever anybody
// configured, and the ones no session may ever carry. This is the browser-side
// half of docs/design/HARNESS-POLICY.md.
const FIXED_POLICY = {
  claude: {
    must: ['--dangerously-skip-permissions', '--settings'],
    never: ['--remote-control', '--permission-mode', '--allow-dangerously-skip-permissions', '--verbose'],
  },
  codex: {
    must: ['--strict-config', '--no-alt-screen', '--dangerously-bypass-approvals-and-sandbox'],
    never: ['-s', '-a', '--remote', '--search'],
  },
  opencode: {
    must: ['--port', '--hostname'],
    never: ['--auto', '--pure', '--print-logs', '--log-level'],
  },
};

async function setOption(page, [harness, key, kind, value]) {
  const selector = '#opt-' + harness + '-' + key;
  if (kind === 'select') {
    await page.selectOption(selector, value);
    return;
  }
  await page.fill(selector, value);
}

async function readOption(page, [harness, key]) {
  return page.$eval('#opt-' + harness + '-' + key, (n) => n.value);
}

async function adminoptions() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  try {
    await setup(s.page, s.url);
    const preset = join(s.data, 'preset-dir');
    mkdirSync(preset, { recursive: true });

    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#harness-opencode', { timeout: 20000 });

    for (const option of ADMIN_OPTIONS) await setOption(s.page, option);

    // The binary path is the other thing a card offers, and it is the one an
    // installation genuinely differs on.
    await s.page.fill('#opt-shell-binary', '/bin/sh');

    // The preset row, typed the way a person types it.
    await s.page.click('#presetAdd');
    const row = '#presetDirs .preset-row:last-child';
    await s.page.fill(row + ' input:nth-child(1)', 'Projects');
    await s.page.fill(row + ' input:nth-child(2)', preset);
    await s.page.selectOption('#windowSize', 'largest');
    await s.page.fill('#historyLimit', '31000');

    // Nothing the owner took out of the dashboard may still be in it: the page
    // is the promise, and a leftover control is a promise the app breaks.
    const withdrawn = await s.page.evaluate(() => [
      'opt-claude-permission_mode', 'opt-claude-skip_permissions', 'opt-claude-remote_control',
      'opt-claude-extra_args', 'opt-claude-extra_env', 'opt-claude-settings_overrides',
      'opt-codex-sandbox', 'opt-codex-approval', 'opt-codex-bypass', 'opt-codex-remote_addr',
      'opt-codex-config_overrides', 'opt-opencode-auto', 'opt-opencode-permission_json',
      'opt-opencode-tui_theme', 'opt-shell-login',
    ].filter((id) => document.getElementById(id)));
    ok(withdrawn.length === 0, 'the dashboard offers no control the app no longer honours',
      withdrawn.join(', ') || 'none of the 15 withdrawn controls is on the page');

    await s.page.click('#saveTop');
    await s.page.waitForSelector('.toast', { timeout: 15000 });
    await wait(400);
    await shot(s.page, 'admin-options');

    // The reload is the assertion: everything above has to come back out of
    // the database and into the same controls.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#harness-opencode', { timeout: 20000 });

    const wrong = [];
    for (const option of ADMIN_OPTIONS) {
      const got = await readOption(s.page, option);
      if (String(got) !== String(option[3])) {
        wrong.push(option[0] + '.' + option[1] + '=' + got + ' (want ' + option[3] + ')');
      }
    }
    const shellBinary = await s.page.$eval('#opt-shell-binary', (n) => n.value);
    if (shellBinary !== '/bin/sh') wrong.push('shell.binary=' + shellBinary);
    ok(wrong.length === 0, 'every setting a harness card offers survived a save and a reload',
      wrong.join(' | ') || (ADMIN_OPTIONS.length + 1) + ' settings');

    const terminal = await s.page.evaluate(() => ({
      windowSize: document.getElementById('windowSize').value,
      history: document.getElementById('historyLimit').value,
      preset: document.querySelector('#presetDirs .preset-row input:nth-child(2)')?.value || '',
    }));
    ok(terminal.windowSize === 'largest' && terminal.history === '31000',
      'the terminal card round-trips too', JSON.stringify(terminal));
    ok(terminal.preset === preset, 'the preset directory was stored', terminal.preset);

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
    // The directory is picked out of the dropdown: open it, and take the entry
    // whose value is this preset. Its own label is the directory's name and
    // the full path is the sub-text under it.
    await s.page.click('#nsDir .combo-input');
    const cell = '#nsDir .combo-option[data-value="preset:' + preset + '"]';
    await s.page.waitForSelector(cell, { timeout: 5000 }).catch(() => {});
    const offered = await s.page.$(cell);
    ok(!!offered, 'the new-session sheet offers the preset directory', cell);
    if (offered) {
      const hint = await s.page.$eval(cell, (n) => (n.querySelector('.combo-hint') || {}).textContent || '');
      ok(hint === preset, 'and its full path is the sub-text under its name', hint);
      await s.page.click(cell);
      await s.page.waitForFunction(() =>
        document.getElementById('nsDirHint').textContent.length > 0, null, { timeout: 5000 });
    }
    await s.page.click('#newSessionSheet .seg[data-value="claude"]');
    await s.page.click('#nsStart');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 30000 });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    const launches = readFakeLog(s.data).filter((entry) => entry.name === 'claude');
    const launch = launches[launches.length - 1] || { argv: [], env: {}, cwd: '' };
    const policy = FIXED_POLICY.claude;
    const missing = policy.must.filter((flag) => !launch.argv.includes(flag));
    ok(missing.length === 0, 'the fixed policy reached the command line',
      missing.join(', ') || policy.must.join(' '));
    const forbidden = policy.never.filter((flag) => launch.argv.includes(flag));
    ok(forbidden.length === 0, 'and nothing the dashboard no longer offers reached it either',
      forbidden.join(', ') || 'none of ' + policy.never.join(' '));
    ok(launch.env.CLAUDE_CODE_NO_FLICKER === '1'
      && launch.env.CLAUDE_CODE_DISABLE_TERMINAL_TITLE === '1',
      'the terminal environment is hard-coded, not configured',
      JSON.stringify({ flicker: launch.env.CLAUDE_CODE_NO_FLICKER,
        title: launch.env.CLAUDE_CODE_DISABLE_TERMINAL_TITLE }));
    ok(!('CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX' in (launch.env || {})),
      'and the remote-control variable is not set at all',
      String(launch.env.CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX));
    ok(launch.cwd === preset, 'the session was started in the preset directory', launch.cwd);

    // The generated settings file is the other half of turning Remote Control
    // off: the flag is never passed, and the key says so where the flag cannot.
    const settingsPath = launch.argv[launch.argv.indexOf('--settings') + 1];
    let claudeSettings = {};
    try { claudeSettings = JSON.parse(readFileSync(settingsPath, 'utf8')); } catch { /* reported below */ }
    ok(claudeSettings.disableRemoteControl === true
      && claudeSettings.skipDangerousModePermissionPrompt === true,
      'the generated settings file disables Remote Control and the bypass dialog',
      JSON.stringify({ remote: claudeSettings.disableRemoteControl,
        dialog: claudeSettings.skipDangerousModePermissionPrompt }));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ----------------------------------------------------------- 11. tmuxcard */

// The engine card against a machine whose tmux is too old. There is no
// installer any more - Socrates runs as the Docker image and the image ships
// tmux - so all the card owes anybody is an honest, actionable sentence.
function stubMachine() {
  // Through the harness, so that the run's own cleanup takes it away.
  const dir = scratchDir('socrates-e2e-stub-');
  writeFileSync(join(dir, 'tmux'), '#!/bin/sh\necho "tmux 3.2a"\n', { mode: 0o755 });
  return dir;
}

async function tmuxcard() {
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
      reason: document.getElementById('tmuxReason').textContent,
    }));
    // The checklist item: 3.2a is amber and "too old", never green and "ok".
    ok(/\bold\b/.test(card.dot) && /too old/.test(card.label),
      'tmux 3.2a is reported as too old, not as ok', card.dot + ' \u00b7 ' + card.label);
    ok(card.detail.includes('3.2a') && card.detail.includes('3.3'),
      'the card shows the version it found and the one it needs', oneLine(card.detail));
    ok(/too old/.test(card.reason),
      'and says in a sentence what is wrong with it', oneLine(card.reason));

    await shot(s.page, 'tmux-card');

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

    // A phone has no Escape and no arrows, so on a touch screen the bar is
    // there before anybody asks for it. There is no line input beside the
    // terminal at all any more.
    await wait(400);
    const untouched = await s.page.evaluate(() => ({
      bar: document.getElementById('keybar').hidden,
      composer: !!document.getElementById('composer') || !!document.getElementById('lineInput'),
      touch: matchMedia('(hover: none) and (pointer: coarse)').matches,
    }));
    ok(!untouched.bar && untouched.touch && !untouched.composer,
      'a session on a touch screen opens with the key bar and no line input', JSON.stringify(untouched));

    // The whole of the rule, as the module itself: the device's answer once it
    // has given one, and the touch screen until then.
    const decided = await s.page.evaluate(async () => {
      const mod = await import('/static/js/keybar.js');
      const fresh = mod.keyBarWanted();
      mod.setKeyBarWanted(false);
      const refused = mod.keyBarWanted();
      mod.setKeyBarWanted(true);
      const asked = mod.keyBarWanted();
      localStorage.removeItem('socrates.term.keybar');
      return { fresh, asked, refused, exports: Object.keys(mod).sort().join(',') };
    });
    ok(decided.fresh && decided.asked && !decided.refused,
      'touch decides until the menu has answered, and the answer wins after that', JSON.stringify(decided));
    ok(decided.exports === 'followViewport,keyBarWanted,mountKeyBar,setKeyBarWanted',
      'and the module exports nothing else', decided.exports);

    // A desk has keys: a pointer that hovers is not a request for a row of
    // buttons.
    const desk = await s.browser.newContext({
      viewport: { width: 1280, height: 720 },
      colorScheme: 'light',
    });
    await desk.addCookies(await s.context.cookies());
    const deskPage = await desk.newPage();
    const openId = await s.page.evaluate(() => location.hash.slice(1));
    await deskPage.goto(s.url + '/#' + openId, { waitUntil: 'domcontentloaded' });
    await deskPage.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(600);
    const atDesk = await deskPage.evaluate(() => ({
      bar: document.getElementById('keybar').hidden,
      hover: matchMedia('(hover: hover) and (pointer: fine)').matches,
    }));
    ok(atDesk.bar && atDesk.hover, 'a device with a pointer that hovers does not get it',
      JSON.stringify(atDesk));
    await deskPage.close();
    await desk.close();

    // The options menu puts it away and brings it back, on any device, and
    // stays open between the two.
    ok(!(await option(s.page, 'keybar')), 'the options menu switches the key bar off', 'off');
    await s.page.waitForSelector('#keybar[hidden]', { state: 'attached', timeout: 10000 });
    ok(await option(s.page, 'keybar'), 'and on again, from the menu still open', 'on');
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 10000 });
    await closeOptions(s.page);

    const keys = await s.page.$$eval('#keybar .key', (nodes) => nodes.map((n) => n.dataset.key));
    ok(keys.join(',') === 'Escape,Tab,Control,Alt,Left,Down,Up,Right,Enter,Ctrl-C,Ctrl-D,Ctrl-Z,Paste',
      'the key bar carries every key of §E.6, in order', keys.join(','));
    const placed = await s.page.evaluate(() => ({
      keybarTop: document.getElementById('keybar').getBoundingClientRect().top,
      wrapBottom: document.getElementById('termWrap').getBoundingClientRect().bottom,
    }));
    ok(placed.keybarTop >= placed.wrapBottom - 1, 'the key bar sits under the terminal, not over it',
      Math.round(placed.keybarTop) + ' vs ' + Math.round(placed.wrapBottom));

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

    // A physical key was just pressed, and that is not a decision about the
    // bar any more: it stays exactly where it was asked to be.
    ok(!(await s.page.$eval('#keybar', (n) => n.hidden)),
      'a keystroke on a real keyboard does not take the bar away', 'still on screen');

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

    // A modifier is for the next key the person presses, and the terminal's
    // own reports are not keys. Leaving the pane for a control in the bar -
    // the options menu, which is about this device and nothing about the pane
    // - and coming back to it through the top bar's keyboard button, both
    // make xterm send a focus report on the same channel, and neither may
    // spend an armed Ctrl.
    await s.page.click('#optionsBtn');
    await s.page.waitForSelector('.menu [data-option]', { timeout: 5000 });
    await s.page.click('#keybar .key[data-key="Control"]');
    await s.page.waitForSelector('.menu', { state: 'detached', timeout: 5000 });
    const beforeKeyboard = (await sentBytes(s.page)).length;
    await s.page.click('#kbBtn');
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
    await focusTerm(s.page);
    await s.page.click('#keybar .key[data-key="Alt"]');
    await s.page.click('#keybar .key[data-key="Alt"]');
    const beforeBlur = (await sentBytes(s.page)).length;
    // Out of the pane to the options menu and back to it with Escape, which
    // is the menu's and not the pane's.
    await s.page.click('#optionsBtn');
    await s.page.waitForSelector('.menu [data-option]', { timeout: 5000 });
    await closeOptions(s.page);
    await wait(250);
    const reports = (await sentBytes(s.page)).slice(beforeBlur);
    ok(!reports.some((hex) => hex.startsWith('1b1b')),
      'a locked Alt leaves the terminal\u2019s own reports alone', reports.join(',') || 'nothing');

    // Escape went to the menu, not to the pane: no ESC of its own was sent.
    ok(!reports.includes('1b'), 'and the Escape that closed the menu never reached the pane',
      reports.join(',') || 'nothing');

    // And a bar that is put away takes its modifiers with it, rather than
    // transforming keys with nothing on screen to say why.
    await option(s.page, 'keybar');
    await closeOptions(s.page);
    await wait(200);
    const hidden = await s.page.evaluate(() => ({
      bar: document.getElementById('keybar').hidden,
      armed: [...document.querySelectorAll('#keybar .key.on, #keybar .key.lock')].length,
      stored: localStorage.getItem('socrates.term.keybar'),
    }));
    ok(hidden.bar && hidden.armed === 0, 'hiding the key bar disarms it', JSON.stringify(hidden));
    ok(hidden.stored === 'off', 'and the answer is written down for this device', String(hidden.stored));

    // Asked for, it is asked for until it is not: a reload comes back to a
    // bar rather than to a decision made again.
    await option(s.page, 'keybar');
    await closeOptions(s.page);
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 5000 });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const kept = await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 10000 })
      .then(() => true).catch(() => false);
    ok(kept, 'the bar is still there after a reload, because this device asked for it',
      kept ? 'on screen' : 'gone');

    // The pane keeps the keyboard through all of it, which is what says the
    // bar is a row of buttons and not a second place to type.
    const typed = 'kb-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + typed);
    ok(await awaitScreen(s.page, typed, 25000), 'and the shell still takes a line typed into the pane',
      oneLine(await screen(s.page)).slice(-60));

    await shot(s.page, 'keybar');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
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
    // And then the tab is killed with no signal at all: this is the ordinary
    // iOS morning, and everything about it has to survive.
    await s.context.setOffline(true);
    await s.page.evaluate(() => window.dispatchEvent(new Event('offline')));
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
    const reopened = await s.page.waitForSelector('#term .xterm', { timeout: 30000 })
      .then(() => true).catch(() => false);
    ok(reopened, 'and the session it was on reopened by itself when the signal came back',
      reopened ? 'the pane is back' : 'still empty');

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
    // they are dropped and said out loud, counted in a toast. Either outcome
    // is honest; a keystroke that is neither delivered nor mentioned is not,
    // and it may not run twice either.
    await wait(2500);
    await typeLine(s.page, 'echo K=$(grep -c k ' + tag + '.txt)');
    const counted = await s.page.waitForFunction(() => {
      const rows = document.querySelector('#term .xterm-rows');
      const found = rows && rows.innerText.match(/K=(\d+)/);
      return found ? found[1] : null;
    }, null, { timeout: 25000 }).then((h) => h.jsonValue()).catch(() => 'no answer');
    const toasts = await s.page.evaluate(() => window.__toasts.slice());
    ok(counted === '1' || toasts.some((t) => /keystrokes? may not have been delivered/.test(t)),
      'the keystrokes typed in the outage ran once, or were said to be lost',
      'K=' + counted + ' toasts=' + JSON.stringify(toasts));
    ok(Number(counted) <= 1, 'and they did not run twice', 'K=' + counted);

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
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    // Type first, so that the server has a `lastInputSeq` well above zero for
    // this viewer. That is what makes the reload interesting: the counter in
    // the page starts again at nothing while the server's does not.
    const tag = Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo warm >> ' + tag + '.txt');
    // The warm-up is read out of the journal, not off the screen. The line
    // writes to a file, so the only thing the pane ever shows of it is the
    // echo of the typed characters - and tmux's attach redraw can repaint the
    // screen from the pane's own state before that echo has been rendered,
    // which failed this assertion once in a full run. The journal is every
    // byte the pane produced and no redraw can take it away.
    const warmed = await journalHas(s, id, 'warm', 20000);
    ok(warmed, 'the session has taken input before the reload',
      warmed ? 'the journal carries the line' : oneLine(await journalOf(s, id)));

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

    await s.page.waitForFunction(() => !document.body.classList.contains('conn-lost'),
      null, { timeout: 30000 }).catch(() => {});
    await wait(3000);

    // The shell counts, because the pane echoes a burst more than once.
    await typeLine(s.page, 'echo K=$(grep -c k ' + tag + '.txt)');
    const counted = await s.page.waitForFunction(() => {
      const rows = document.querySelector('#term .xterm-rows');
      const found = rows && rows.innerText.match(/K=(\d+)/);
      return found ? found[1] : null;
    }, null, { timeout: 25000 }).then((h) => h.jsonValue()).catch(() => 'no answer');
    ok(counted === '1', 'what was typed before hello ran, exactly once', String(counted));

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

// journalSays waits until the journal carries something at least `times`
// over. Counting matters after a restart: the journal is one file for the life
// of the session, so a banner that was already there proves nothing about the
// program that has just been started into the pane.
async function journalSays(s, id, needle, times = 1, timeout = 25000) {
  const until = Date.now() + timeout;
  for (;;) {
    const text = await journalOf(s, id);
    if (text.split(needle).length - 1 >= times) return true;
    if (Date.now() > until) return false;
    await wait(200);
  }
}

// journalHas polls the journal until it carries something, which is the
// deterministic form of "wait until the pane has done it": a screen can be
// repainted by an attach, and the journal only ever grows.
async function journalHas(s, id, needle, timeout = 20000) {
  const until = Date.now() + timeout;
  for (;;) {
    if ((await journalOf(s, id)).includes(needle)) return true;
    if (Date.now() > until) return false;
    await wait(200);
  }
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
  await page.waitForSelector('#newSessionSheet .seg[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#newSessionSheet .seg[data-value="' + harness + '"]');
  // The model step is hidden for Shell and pre-filled for a harness that names
  // a default. Where it is neither - OpenCode reports models and no default -
  // the sheet waits for a choice, which is what a person would make.
  const wantsModel = await page.$eval('#nsModelField', (n) => !n.hidden);
  const chosen = await page.$eval('#nsModel .combo-input', (n) => n.value.trim());
  if (wantsModel && (modelLabel || !chosen)) {
    await page.click('#nsModel .combo-input');
    await page.waitForSelector('#nsModel .combo-option', { timeout: 5000 });
    await page.click(modelLabel
      ? '#nsModel .combo-option:has(.combo-label:text-is("' + modelLabel + '"))'
      : '#nsModel .combo-option');
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
    await s.page.click('#newSessionSheet .seg[data-value="claude"]');
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
    // A terminal on the page says nothing about whether the program has
    // printed yet; the journal is waited for, then read.
    await journalHas(s, id, 'FAKE claude');
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

    // §E.10 rule 2 and 3: the header is the name and nothing else - neither
    // the mark nor the model; those and the path are in Info.
    const badge = await s.page.evaluate(() => {
      const bar = document.querySelector('.topbar');
      return {
        mark: !!bar.querySelector('.agent-mark'),
        model: /haiku/i.test(bar.textContent),
      };
    });
    ok(!badge.mark && !badge.model, 'the header names neither the harness nor the model',
      JSON.stringify(badge));
    await rowMenu(s.page);
    await s.page.click('.menu-item:has-text("Info")');
    await s.page.waitForSelector('dialog.modal .facts', { timeout: 5000 });
    const facts = await s.page.$$eval('dialog.modal .fact', (nodes) => nodes.map((n) => n.textContent));
    ok(facts.some((f) => f === row.workdir),
      'and the working directory is one line of Info', oneLine(facts.join(' · ')));
    await s.page.click('dialog.modal .modal-actions .btn');
    await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 5000 });
    await shot(s.page, 'createclaude');

    // The program ends and is started again from the browser. A restart is a
    // resume (§C.8), so the conversation it had is the conversation it gets.
    //
    // The relaunch is held for a moment on the wire, because the interesting
    // part is what the page does while it waits: the list refreshes on a wake
    // and every fifteen seconds, and the button that was just pressed must not
    // come back offering to press it again. One press is one relaunch.
    const before = launchesOf(s, 'claude').length;
    let posts = 0;
    await s.page.route('**/api/sessions/*/restart', async (route) => {
      posts += 1;
      await new Promise((done) => setTimeout(done, 2500));
      await route.continue();
    });
    await typeLine(s.page, '/exit 3');
    await s.page.waitForSelector('#termOverlay .overlay-card', { timeout: 25000 });
    const ended = await s.page.$eval('#termOverlay .overlay-title', (n) => n.textContent);
    ok(/The session ended/.test(ended), 'the overlay says the session ended', oneLine(ended));
    await s.page.click('#termRestart');
    await s.page.waitForFunction(() => {
      const button = document.getElementById('termRestart');
      return !!button && button.disabled;
    }, null, { timeout: 8000 });
    // A wake is what a phone does when it comes back, and it refreshes the
    // list - with the row still saying `exited`, because the POST is in flight.
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await wait(1200);
    const pressed = await s.page.$eval('#termRestart',
      (n) => ({ disabled: n.disabled, text: n.textContent.trim() }));
    ok(pressed.disabled && /Restarting/.test(pressed.text),
      'a list refresh in the middle does not un-press Restart', JSON.stringify(pressed));
    await s.page.waitForFunction(() => {
      const node = document.getElementById('termOverlay');
      return node && node.hidden;
    }, null, { timeout: 40000 });
    await s.page.unroute('**/api/sessions/*/restart');
    ok(posts === 1, 'and one press was one relaunch', posts + ' POSTs');

    ok(await awaitScreen(s.page, 'FAKE claude', 25000), 'Restart brought Claude Code back',
      oneLine(await screen(s.page)));
    const again = launchesOf(s, 'claude').slice(before);
    ok(again.length === 1 && argvOf(again[0]).includes('--resume ' + fixed),
      'and it was resumed on the conversation it already had', argvOf(again[0]));
    const back = await sessionRow(s, id);
    ok(back.state === 'running', 'the row is running again', back.state);

    // §E.7 again, and the whole line this time: a notice is one sentence and a
    // close button, and the conversation it names is behind the "i" - never a
    // second word in the line.
    await s.page.waitForSelector('#termNotice:not([hidden])', { timeout: 20000 });
    const line = await s.page.$eval('#termNotice', (n) => n.innerText.trim());
    ok(line === 'Resumed after a restart.', 'the banner is that sentence and nothing else',
      JSON.stringify(line));

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* -------------------------------------------------------------- profiles */

// A profile is a second set of sign-ins. It is added in the dashboard, offered
// by the sheet with the account each choice means, and reaches the launched
// program as the directory its CLI keeps the account in - and it cannot be
// deleted while a session still runs in it.
async function profiles() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#profileList .profile-row', { timeout: 20000 });
    await s.page.fill('#profileName', 'Business');
    await s.page.click('#profileAdd');
    await s.page.waitForFunction(() => document.querySelectorAll('#profileList .profile-row').length === 2,
      null, { timeout: 10000 });
    const listed = await (await s.context.request.get(s.url + '/api/profiles')).json();
    const business = (listed.profiles || []).find((p) => p.name === 'Business');
    ok(!!business && /^p-[0-9a-f]{12}$/.test(business.id), 'the dashboard added the profile',
      JSON.stringify(listed.profiles));
    await shot(s.page, 'profiles-admin');

    await open(s);
    await ensureNav(s.page);
    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await s.page.click('#newSessionSheet .seg[data-value="claude"]');
    await s.page.waitForSelector('#nsProfileField:not([hidden]) #nsProfile .drop-button', { timeout: 10000 });
    await s.page.click('#nsProfile .drop-button');
    const choice = '#nsProfile .drop-option[data-value="' + business.id + '"]';
    await s.page.waitForSelector(choice, { timeout: 5000 });
    const offered = await s.page.$$eval('#nsProfile .drop-option .profile-entry-name', (nodes) => nodes.map((n) => n.textContent));
    ok(offered.join(',') === 'Default,Business', 'the sheet offers the default profile and the new one',
      offered.join(','));
    await s.page.click(choice);
    const face = await s.page.$eval('#nsProfile .drop-face .profile-entry-name', (n) => n.textContent);
    ok(face === 'Business', 'the dropdown shows the profile chosen', face);
    const hint = await s.page.$eval('#nsProfileHint', (n) => n.textContent);
    ok(/not signed in/.test(hint), 'and says that Claude Code still has to sign in there', hint);
    await s.page.click('#nsModel .combo-input');
    await s.page.waitForSelector('#nsModel .combo-option', { timeout: 5000 });
    await s.page.click('#nsModel .combo-option:has(.combo-label:text-is("Haiku"))');
    // Ultracode is Claude Code's alone, and off until it is switched on.
    const ultra = await s.page.$eval('#nsUltracodeField', (n) => ({
      hidden: n.hidden, off: n.querySelector('.seg[data-value="off"]').getAttribute('aria-pressed'),
    }));
    ok(!ultra.hidden && ultra.off === 'true', 'the sheet offers Ultracode for Claude Code, off', JSON.stringify(ultra));
    await s.page.click('#nsUltracode .seg[data-value="on"]');
    await shot(s.page, 'profiles-sheet');
    await s.page.click('#nsStart');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 30000 });
    const id = await s.page.evaluate(() => location.hash.slice(1));
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(await awaitScreen(s.page, 'FAKE claude', 25000), 'the session is up', oneLine(await screen(s.page)));

    const launch = lastLaunch(s, 'claude') || { env: {} };
    const dir = (launch.env || {}).CLAUDE_CONFIG_DIR || '';
    ok(dir.endsWith('/profiles/' + business.id + '/claude'),
      'Claude Code was started on the profile\'s own directory', dir);
    const row = await sessionRow(s, id);
    ok(row.options && row.options.profile === business.id, 'and the session remembers its profile',
      JSON.stringify(row.options));
    const settingsFile = (launch.argv || [])[(launch.argv || []).indexOf('--settings') + 1] || '';
    let ultracode = 'unreadable';
    try { ultracode = JSON.parse(readFileSync(settingsFile, 'utf8')).ultracode; } catch { /* reported below */ }
    ok(row.options.ultracode === true && ultracode === true,
      'and Claude Code was started with Ultracode on', JSON.stringify({ options: row.options, ultracode }));

    await rowMenu(s.page);
    await s.page.click('.menu-item:has-text("Info")');
    await s.page.waitForSelector('dialog.modal .facts', { timeout: 5000 });
    const facts = await s.page.$$eval('dialog.modal .fact', (nodes) => nodes.map((n) => n.textContent));
    ok(facts.includes('Profile: Business') && facts.includes('Ultracode on'),
      'Info names the profile and Ultracode', oneLine(facts.join(' · ')));
    await s.page.click('dialog.modal .modal-actions .btn');
    await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 5000 });

    const refused = await s.context.request.delete(s.url + '/api/profiles/' + business.id);
    ok(refused.status() === 409, 'a profile a session runs in cannot be deleted', String(refused.status()));

    // A session keeps the profile it was started in: its menu offers none.
    await rowMenu(s.page);
    const menuItems = await s.page.$$eval('.menu .menu-item', (nodes) => nodes.map((n) => n.textContent));
    ok(!menuItems.includes('Default') && !menuItems.includes('Business'),
      'the session menu offers no profile to move to', menuItems.join(','));
    await s.page.click('#term', { position: { x: 40, y: 40 } });
    await s.page.waitForSelector('.menu', { state: 'detached', timeout: 10000 });

    // The next sheet starts on the profile the last session was started in.
    await ensureNav(s.page);
    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await s.page.click('#newSessionSheet .seg[data-value="claude"]');
    await s.page.waitForSelector('#nsProfileField:not([hidden]) #nsProfile .drop-face', { timeout: 10000 });
    const next = await s.page.$eval('#nsProfile .drop-face .profile-entry-name', (n) => n.textContent);
    ok(next === 'Business', 'the next sheet starts on the same profile', next);
    const ultraAgain = await s.page.$eval('#nsUltracode .seg[data-value="off"]', (n) => n.getAttribute('aria-pressed'));
    ok(ultraAgain === 'true', 'but with Ultracode off again', ultraAgain);
    await s.page.click('#newSessionSheet .seg[data-value="codex"]');
    const ultraCodex = await s.page.$eval('#nsUltracodeField', (n) => n.hidden);
    ok(ultraCodex, 'and Codex is not offered it', String(ultraCodex));
    await s.page.click('#nsCancel');

    // The dashboard's order is the sheet's: a row moves by its grip, with the
    // arrow keys or dragged, and the order is saved as soon as it is let go.
    const order = async () => ((await (await s.context.request.get(s.url + '/api/profiles')).json()).profiles || [])
      .map((p) => p.name).join(',');
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForFunction(() => document.querySelectorAll('#profileList .profile-row').length === 2,
      null, { timeout: 20000 });
    await s.page.focus('#profileList .profile-row[data-id="' + business.id + '"] .profile-grip');
    await s.page.keyboard.press('ArrowUp');
    let moved = '';
    for (let i = 0; i < 40 && moved !== 'Business,Default'; i++) { await s.page.waitForTimeout(100); moved = await order(); }
    ok(moved === 'Business,Default', 'the arrow keys move a profile up the list', moved);
    const grip = await s.page.$eval('#profileList .profile-row[data-id="' + business.id + '"] .profile-grip', (n) => {
      const box = n.getBoundingClientRect();
      return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
    });
    const last = await s.page.$eval('#profileList .profile-row:last-child', (n) => n.getBoundingClientRect().bottom);
    await s.page.mouse.move(grip.x, grip.y);
    await s.page.mouse.down();
    await s.page.mouse.move(grip.x, last - 4, { steps: 8 });
    await s.page.mouse.up();
    for (let i = 0; i < 40 && moved !== 'Default,Business'; i++) { await s.page.waitForTimeout(100); moved = await order(); }
    ok(moved === 'Default,Business', 'and dragging puts it back', moved);
    const rows = await s.page.$$eval('#profileList .profile-row', (nodes) => nodes.map((n) => n.dataset.id));
    ok(rows.join(',') === ',' + business.id, 'where the list shows it', rows.join(','));

    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
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
    await journalHas(s, id, 'FAKE codex');
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
    await journalHas(s, id, 'FAKE opencode');
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
    await s.page.waitForFunction(() => document.getElementById('sessionUsage')?.innerText.includes('~$1.25'),
      null, { timeout: 20000 });
    const cost = await s.page.$eval('#sessionUsage', (node) => node.innerText.trim());
    ok(cost.includes('~$1.25'), 'its estimated cost is in the header', cost);
    await shot(s.page, 'createopencode');

    await s.page.setViewportSize({ width: 390, height: 844 });
    await wait(300);
    const mobileHeader = await s.page.evaluate(() => {
      const bar = document.querySelector('.topbar');
      const speak = document.getElementById('micBtn');
      const status = document.getElementById('statusBtn');
      return {
        height: Math.round(bar.getBoundingClientRect().height),
        usage: document.getElementById('sessionUsage').innerText.trim(),
        speakBorder: getComputedStyle(speak).borderTopWidth,
        separator: getComputedStyle(status, '::before').width,
        overflow: document.documentElement.scrollWidth - innerWidth,
      };
    });
    ok(mobileHeader.usage.includes('~$1.25') && mobileHeader.height === 82,
      'the borderless usage line stays visible in the narrow header', JSON.stringify(mobileHeader));
    ok(mobileHeader.speakBorder === '0px' && mobileHeader.separator === '1px' && mobileHeader.overflow <= 0,
      'header actions use separators without overflowing', JSON.stringify(mobileHeader));
    await shot(s.page, 'createopencode-phone');
    await s.page.setViewportSize({ width: 1280, height: 720 });

    const before = await endAndRestart(s, 'opencode', 0);
    ok(await awaitScreen(s.page, 'FAKE opencode', 25000), 'Restart brought OpenCode back',
      oneLine(await screen(s.page)));
    const again = launchesOf(s, 'opencode').slice(before);
    ok(again.length === 1 && argvOf(again[0]).includes('--session ' + conversation),
      'and it was resumed on the session the server had named', argvOf(again[0]));

    // A limit window says how long it has until it renews, not only how full
    // it is. The fake programs keep no limits, so the reading is the page's.
    const renews = new Date(Date.now() + (2 * 60 + 36) * 60000 + 30000).toISOString();
    await s.page.route('**/api/sessions/*/usage', (route) => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ windows: [{ label: '5h', used_percent: 42, resets_at: renews }], cost_usd: 1.25 }),
    }));
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForFunction(() => /5h\s*42%/.test(document.getElementById('sessionUsage')?.innerText || ''),
      null, { timeout: 20000 });
    const limit = await s.page.$eval('#sessionUsage', (node) => ({ text: node.innerText.trim(), title: node.title }));
    ok(/\(2h 3[67]m left\)/.test(limit.text) && /resets in 2h 3[67]m/.test(limit.title),
      'the header says how long the limit has until it renews', JSON.stringify(limit));
    await s.page.unroute('**/api/sessions/*/usage');

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

    // Two sessions, because one cannot show what this scenario was extended
    // for: the first resume starts the tmux server again, and every session
    // after it used to be refused as "the session is still running".
    const shellId = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const claudeId = await startWithModel(s.page, 'claude', 'Sonnet');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(await journalSays(s, claudeId, 'FAKE claude', 1), 'the session is up',
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
    const waitingShell = await waitForState(s, shellId, ['needs_resume'], 40000);
    ok(waitingShell.state === 'needs_resume', 'and so is the one behind it', waitingShell.state);

    // The reload. Opening the session is what resumes it, and the resume is
    // the whole of the handshake - so what the pane says while it waits is
    // "Resuming after a restart…", never "this session is not running".
    const beforeClaude = launchesOf(s, 'claude').length;
    await s.page.goto(s.url + '/#' + claudeId, { waitUntil: 'domcontentloaded' });
    await waitForState(s, claudeId, ['running'], 40000);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    // The journal is one file for the life of the session, so it is the
    // second banner that proves a program was started into it again.
    ok(await journalSays(s, claudeId, 'FAKE claude', 2),
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
        whole: host.innerText.trim(),
        bubble: bubble ? bubble.textContent : '',
        bubbleShown: bubble ? getComputedStyle(bubble).visibility : 'none',
      };
    });
    ok(banner.kind === 'resumed' && /Resumed after a restart/.test(banner.words),
      'the banner says the session was resumed', JSON.stringify(banner));
    ok(banner.whole === 'Resumed after a restart.',
      'and the line holds that sentence and nothing else', JSON.stringify(banner.whole));
    ok(!/could not be resumed/.test(banner.words),
      'and it does not claim the conversation was lost, because it was not', oneLine(banner.words));
    ok(banner.bubble.includes(conversation) && banner.bubbleShown === 'hidden',
      'the conversation it came back on is behind the "i"',
      banner.bubbleShown + ' ' + oneLine(banner.bubble));
    await shot(s.page, 'rebootresume');

    // A notice is news, and news that has been read is in the way: it goes on
    // its own once it has been up long enough to read, and going is the same
    // decision the close button makes - the flag behind it is cleared either
    // way, and the banner does not come back on the next reload.
    const went = await s.page.waitForFunction(() => document.getElementById('termNotice').hidden,
      null, { timeout: 9000 }).then(() => true).catch(() => false);
    ok(went, 'the banner puts itself away without being dismissed',
      went ? 'gone' : 'still up after 9 s');
    const acked = await waitForAck(s, claudeId);
    ok(acked.resumed === false, 'and the line going cleared the flag behind it',
      JSON.stringify({ resumed: acked.resumed, resume_count: acked.resume_count }));
    ok(acked.resume_count === 1, 'and the resume was counted once', String(acked.resume_count));
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(1500);
    // A reload of a tab the server still remembers can raise the *desync*
    // notice, which is a different sentence about a different thing. What must
    // not come back is the resumed banner.
    const still = await s.page.$eval('#termNotice',
      (n) => (n.hidden ? { kind: 'hidden', whole: '' } : { kind: n.dataset.kind, whole: n.innerText.trim() }));
    ok(still.kind !== 'resumed', 'and a reload does not show it again', still.kind);
    // A notice with nothing behind an "i" - the desync line has no facts - is
    // the line and the close button, and no stray word from a null child.
    ok(still.kind !== 'desync' || still.whole === 'Reconnected — the screen was redrawn.',
      'a notice with no detail is still just its sentence', JSON.stringify(still.whole));

    // And the session is a working session, not a screen that came back: the
    // pane it was given is a new program, and it answers.
    await typeLine(s.page, 'hello again');
    ok(await awaitScreen(s.page, 'you said: hello again', 25000),
      'and the session that came back is one that works', oneLine(await screen(s.page)));

    // And now the session behind it. Resuming the first one started the tmux
    // server again, which is what used to make this one answer 409 and sit
    // under "Resuming after a restart…" for ever.
    await s.page.goto(s.url + '/#' + shellId, { waitUntil: 'domcontentloaded' });
    const behind = await waitForState(s, shellId, ['running'], 40000);
    ok(behind.state === 'running', 'the second session resumes as well as the first',
      behind.state + (behind.fail_reason ? ' - ' + behind.fail_reason : ''));
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const alive = 'again-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + alive);
    ok(await awaitScreen(s.page, alive, 25000), 'and it is a session that works',
      oneLine(await screen(s.page)));

    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 18. twoviewers */

// recordNotices keeps every notice the thin line above the terminal has shown,
// in order. It is an init script rather than a poll because every notice puts
// itself away once it has been read, and a scenario that only looked at the
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
    // person did not need: the key bar coming up, a font settling, a fit
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
    // The whole line, not only the part that carries the numbers: a notice is
    // one sentence and a close button, and nothing else may creep into it.
    ok(notices.length === 1
      && notices[0].text === 'Another viewer resized this session to ' + moved.replace('x', '×') + '.',
      'and the line holds that sentence and nothing else',
      JSON.stringify(notices.length ? notices[0].text : ''));

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
    await s.page.addInitScript(recordNotices);
    // Everything the socket delivered, kept as it arrives. The page sets
    // binaryType itself, so the listener takes an ArrayBuffer and decodes it
    // whole: a frame header is not text and cannot match what is counted.
    await s.page.addInitScript(() => {
      window.__closed = [];
      window.__rx = '';
      const decoder = new TextDecoder();
      window.WebSocket = new Proxy(window.WebSocket, {
        construct(target, args) {
          const ws = new target(...args);
          ws.addEventListener('message', (event) => {
            if (typeof event.data === 'string') window.__rx += event.data;
            else if (event.data instanceof ArrayBuffer) {
              window.__rx += decoder.decode(new Uint8Array(event.data));
            }
          });
          ws.addEventListener('close', (event) => window.__closed.push(event.code));
          return ws;
        },
      });
    });
    await open(s);
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(await awaitScreen(s.page, 'FAKE claude'), 'the fake CLI is at its prompt',
      oneLine(await screen(s.page)));

    // What the client ends up holding is the measure that matters: the ring
    // is what a viewer reads from, and a reader that falls behind it is told
    // so with a replay from zero. So the sockets are watched for a close and
    // the notices for a redraw, and the pane's own scrollback is read back
    // afterwards - not the journal, which the server writes whether or not a
    // byte ever reached a browser.
    await s.page.evaluate(() => { window.__notices = []; });
    await typeLine(s.page, '/spin');
    ok(await awaitScreen(s.page, 'spin 200', 40000), 'the last of the two hundred lines arrived',
      oneLine(await screen(s.page)));

    // What the pane printed, as the journal recorded it: the fake's own
    // numbering makes a hole impossible to miss.
    // readFileSync is imported at the top of this file.
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

    // The ring and the socket measured from the far end: what the browser was
    // actually sent. A viewer is sent a window, not a transcript - tmux
    // repaints a pane that is producing faster than a client can draw, so
    // neither the count nor the order of what crosses the wire is the
    // product's promise, and asserting on them would be asserting on tmux's
    // redraw. The promises are these three: the end of the burst arrived, the
    // reader was never overrun by the ring - which the server answers with a
    // replay from zero, and the page with the desync notice - and the socket
    // carried it without being closed.
    const delivered = await s.page.evaluate(() =>
      [...(window.__rx || '').matchAll(/spin (\d+)/g)].map((m) => Number(m[1])));
    ok(delivered.length > 0 && delivered[delivered.length - 1] === 200,
      'the burst reached the browser over the socket, down to its last line',
      delivered.length + ' lines, ending at ' + (delivered[delivered.length - 1] || 'nothing'));

    // Nothing in that burst cost the viewer its place: a reader the ring had
    // overrun would have been sent a replay from zero, and the page says so
    // with the desync notice.
    const redraws = await s.page.evaluate(() => (window.__notices || [])
      .filter((n) => n.kind === 'desync'));
    ok(redraws.length === 0, 'the viewer never fell behind the ring',
      JSON.stringify(redraws));
    const closes = await s.page.evaluate(() => (window.__closed || []).slice());
    ok(closes.length === 0, 'and the socket carried it without being closed',
      JSON.stringify(closes));

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

    const left = await (await s.context.request.get(s.url + '/api/sessions')).json();
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

    const listed = await (await s.context.request.get(s.url + '/api/sessions')).json();
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

/* ----------------------------------------------------------- 22. lighttheme */

// The white terminal, proved through the whole stack rather than in the
// palette file: a CLI that asks the terminal what colour it is on gets "light"
// back through tmux, the PTY and the WebSocket, the pane is painted #ffffff,
// and every ANSI colour a program can emit is *drawn* legibly on it.
//
// The last part is the one worth being careful about. Eleven of LIGHT_THEME's
// eighteen colours are not 4.5:1 against white and are not meant to be - a
// yellow that is 4.5:1 on white is brown. What keeps them readable is
// `minimumContrastRatio: 4.5`, which re-derives a colour at draw time, so the
// assertion is on what the renderer actually painted, never on the table.
async function lighttheme() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    // Codex is the harness whose theme is a launch-time decision: Socrates
    // writes `-c tui.theme=<name>` into its command line, and a theme name is
    // not validated at config load, so the flag being built is only half the
    // claim. The other half is the banner, which is what the program itself
    // saw when it asked the terminal.
    const id = await startWithModel(s.page, 'codex', null);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    const launch = lastLaunch(s, 'codex');
    const theme = (launch.argv || []).find((arg) => arg.startsWith('tui.theme')) || '';
    ok(/tui\.theme\s*=\s*"?\w/.test(theme), 'Codex was told which theme to wear', theme || 'no tui.theme');

    await journalHas(s, id, 'FAKE codex');
    const banner = await journalOf(s, id);
    ok(/theme=light/.test(banner), 'and the program asked the terminal and was told "light"',
      oneLine((/FAKE codex[^\r\n]*/.exec(banner) || [''])[0]));

    const painted = await s.page.evaluate(() => {
      const bg = (sel) => {
        const node = document.querySelector(sel);
        return node ? getComputedStyle(node).backgroundColor : 'missing';
      };
      return { viewport: bg('#term .xterm-viewport'), screen: bg('#term .xterm-screen') };
    });
    ok(painted.viewport === WHITE && painted.screen === WHITE,
      'the pane is painted pure white', JSON.stringify(painted));

    // The sixteen ANSI colours, printed by the program and measured where they
    // landed. `/exit` first: the fake echoes input, and a pane running a shell
    // is the only way to make a program emit arbitrary escape sequences.
    await typeLine(s.page, '/exit 0');
    await s.page.waitForSelector('#termOverlay:not([hidden])', { timeout: 20000 });
    const shellId = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    ok(!!shellId, 'a shell beside it, to print with', shellId);

    const words = [];
    for (let code = 30; code <= 37; code += 1) words.push([code, 'C' + code]);
    for (let code = 90; code <= 97; code += 1) words.push([code, 'C' + code]);
    await typeLine(s.page, "printf '" + words.map(([code, word]) =>
      '\\033[' + code + 'm' + word + '\\033[0m\\n').join('') + "'");
    ok(await awaitScreen(s.page, 'C97', 20000), 'the pane printed all sixteen ANSI colours',
      oneLine(await screen(s.page)));

    const drawn = await s.page.evaluate((wanted) => {
      const ratio = (rgb) => {
        const [r, g, b] = rgb.match(/\d+/g).map(Number).map((c) => {
          const v = c / 255;
          return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
        });
        return 1.05 / (0.2126 * r + 0.7152 * g + 0.0722 * b + 0.05);
      };
      const spans = [...document.querySelectorAll('#term .xterm-rows span')];
      return wanted.map((word) => {
        const node = spans.find((n) => n.textContent.trim() === word);
        const colour = node ? getComputedStyle(node).color : 'none';
        return { word, colour, ratio: node ? Math.round(ratio(colour) * 100) / 100 : 0 };
      });
    }, words.map(([, word]) => word));
    const worst = drawn.reduce((low, one) => (one.ratio < low.ratio ? one : low), drawn[0]);
    ok(drawn.every((one) => one.ratio >= 4.5),
      'every one of them was drawn at 4.5:1 or better on the white page',
      'worst: ' + worst.word + ' ' + worst.colour + ' = ' + worst.ratio + ':1');

    // And the table itself, for the two values that are not re-derived: the
    // ground the renderer paints on and the ink it writes with.
    const table = await s.page.evaluate(async () => {
      const mod = await import('/static/js/term.js');
      const entries = Object.entries(mod.LIGHT_THEME).filter(([, v]) => /^#/.test(v));
      return {
        background: mod.LIGHT_THEME.background,
        foreground: mod.contrast(mod.LIGHT_THEME.foreground, '#ffffff'),
        count: entries.length,
      };
    });
    ok(table.background === '#ffffff' && table.foreground > 15,
      'the palette itself paints on white and writes in near black',
      JSON.stringify({ ...table, foreground: Math.round(table.foreground * 10) / 10 }));

    // The picture is the record of what the eye would confirm. A real Codex
    // cannot be started here - it would spend tokens and needs an account - so
    // what is on it is the fake wearing the same terminal.
    await shot(s.page, 'lighttheme');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------------- 23. design */

// The four design laws of §E.10, measured rather than looked at: white
// surfaces, a mark wherever a harness is named, technical strings only behind
// an "i", and motion that does not restart when a list re-renders.
async function design() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const row = await sessionRow(s, id);

    // Rule 1 - every surface is the same white.
    const surfaces = await s.page.evaluate(() => {
      const bg = (sel) => {
        const node = document.querySelector(sel);
        return node ? getComputedStyle(node).backgroundColor : 'missing';
      };
      return {
        body: bg('body'), sidebar: bg('.sidebar'), topbar: bg('.topbar'),
        list: bg('#sessionList'), wrap: bg('.term-wrap'),
        viewport: bg('#term .xterm-viewport'), screen: bg('#term .xterm-screen'),
      };
    });
    // A transparent element is the white underneath it; what a design rule
    // forbids is a fill of its own.
    const white = (colour) => colour === WHITE || colour === 'rgba(0, 0, 0, 0)';
    ok(Object.values(surfaces).every(white),
      'every surface on the session page is the same white', JSON.stringify(surfaces));

    // Rule 2 - a mark wherever a harness is named. The sheet names them all,
    // and the row in the list names this session's.
    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    const sheetMarks = await s.page.$$eval('#nsHarness .seg, #nsTools .seg', (nodes) =>
      nodes.map((n) => ({
        harness: n.dataset.value,
        mark: (n.querySelector('.agent-mark') || { dataset: {} }).dataset.agent || 'none',
      })));
    // VS Code is opened from a folder's head in the list, not from the sheet
    // (`pages` pins the same four), so the sheet names four programs.
    ok(sheetMarks.map((one) => one.harness).join(',') === 'claude,codex,opencode,shell'
      && sheetMarks.every((one) => one.mark === one.harness),
      'the sheet gives every harness its own mark', JSON.stringify(sheetMarks));

    // Where a session works is a dropdown, and it opens on a list of places
    // with the full path as the sub-text - never a row of cells three
    // characters wide.
    await s.page.click('#nsDir .combo-input');
    await s.page.waitForSelector('#nsDir .combo-option', { timeout: 5000 });
    const dir = await s.page.evaluate(() => {
      const host = document.getElementById('nsDir');
      return {
        combo: !!host.querySelector('.combo'),
        segs: host.querySelectorAll('.seg').length,
        options: [...host.querySelectorAll('.combo-option')].map((n) => n.dataset.value),
        value: host.querySelector('.combo-input').value,
      };
    });
    ok(dir.combo && dir.segs === 0 && dir.options.includes('dynamic') && dir.value === 'Dynamic',
      'the working directory is a dropdown, not a row of cells', JSON.stringify(dir));
    await shot(s.page, 'design-dir-open');
    await s.page.keyboard.press('Escape');
    await s.page.keyboard.press('Escape');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 10000 })
      .catch(() => {});

    const marks = await s.page.evaluate((sessionId) => ({
      row: (document.querySelector('#sessionList .chat-item[data-id="' + sessionId + '"] .agent-mark')
        || { dataset: {} }).dataset.agent,
    }), id);
    ok(marks.row === 'shell', 'and so does the row in the list', JSON.stringify(marks));

    // Rule 3 - technical strings are hover-only. The working directory, the
    // tmux session name and the state word are facts about the machine; what
    // the page shows in words is the session's name.
    const visible = await s.page.evaluate(() => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      const parts = [];
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const host = node.parentElement;
        if (!host || host.closest('.tip-bubble')) continue;
        if (host.closest('#term')) continue;          // the pane is the program's, not the page's
        if (!host.checkVisibility || !host.checkVisibility()) continue;
        parts.push(node.textContent);
      }
      return parts.join(' ');
    });
    const hidden = {
      workdir: !visible.includes(row.workdir),
      tmux: !/soc_/.test(visible),
      title: visible.includes(row.title),
    };
    ok(hidden.workdir && hidden.tmux && hidden.title,
      'the workdir and the tmux name are behind the "i", and the name is what is read',
      JSON.stringify(hidden));
    // The facts are in one place, and that place is the Info item of the
    // row's own menu.
    await s.page.click(rowSel(id) + ' .act');
    await s.page.click('.menu-item:has-text("Info")');
    await s.page.waitForSelector('dialog.modal .facts', { timeout: 5000 });
    const inInfo = await s.page.$$eval('dialog.modal .fact', (nodes) =>
      nodes.map((n) => n.textContent).join(' '));
    ok(inInfo.includes(row.workdir), 'and Info is where the workdir actually is',
      oneLine(inInfo));
    await s.page.click('dialog.modal .modal-actions .btn');
    await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 5000 });

    // Rule 4 - motion is subtle, and it does not restart. A list that
    // re-renders must update the row it already has rather than build a new
    // one, or every mark on it starts again on every poll.
    await s.page.evaluate((sel) => {
      document.querySelector(sel).dataset.stamp = 'before';
    }, rowSel(id));

    const renamed = row.title + ' renamed';
    await s.context.request.patch(s.url + '/api/sessions/' + id, { data: { title: renamed } });
    // A wake is how the page is told to look again - the same event a phone
    // sends when it comes back. Importing the module to call its own refresh
    // would load a second copy of it under an unstamped URL and boot the whole
    // application again, which really would rebuild the list.
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((want) =>
      [...document.querySelectorAll('#sessionList .chat-item .label')].some((n) => n.textContent === want),
    renamed, { timeout: 20000 });
    const after = await s.page.evaluate((sel) => {
      const node = document.querySelector(sel);
      return { sameRow: !!node && node.dataset.stamp === 'before' };
    }, rowSel(id));
    ok(after.sameRow, 'and a rename is drawn into the row that is already there',
      after.sameRow ? 'the same row' : 'a new row');

    // Rule 4 again, on the mark that says a session is working. It is the
    // agent's own mark with a hairline ring around it, one arc in the page's
    // own ink, and it turns once every 900 ms - the same beat as the resume
    // spinner. The arc is the ink and not a grey, because the whole of the
    // motion is that one arc: drawn in the faintest text colour on a ring in
    // the faintest line, it turned and nobody could see it turning, which is
    // the same thing as not turning at all.
    //
    // It takes an agent session: a shell and an editor are the two programs
    // nothing is asked of and nothing is reported back by, so their rows carry
    // no status at all and there is no ring on them to look at. The shell is
    // stepped back onto afterwards, because the rest of this scenario is about
    // the pane it owns.
    const working = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await awaitRow(s.page, working, (r) => !r.busy, 20000);
    const workingRow = await sessionRow(s, working);
    await typeLine(s.page, '/busy 20000');
    await clickRow(s.page, id);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const spinning = await awaitRow(s.page, working, (r) => r.busy, 10000);
    ok(spinning.ok, 'a session that is working turns its own mark into a ring', took(spinning));
    const ring = spinning.seen ? spinning.seen.ring : null;
    ok(!!ring && ring.colour === 'rgb(23, 24, 27)' && ring.rest === 'rgb(220, 221, 225)',
      'the arc that moves is the page\u2019s ink, on a ring a shade under it',
      ring ? ring.colour + ' on ' + ring.rest : 'no ring');
    ok(!!ring && contrastOf(ring.colour, ring.rest) >= 3,
      'so the arc is legible against the ring it turns on',
      ring ? contrastOf(ring.colour, ring.rest).toFixed(1) + ':1' : 'no ring');
    const beat = ring ? Math.round(parseFloat(ring.motion) * 1000) : 0;
    ok(beat >= 120 && beat <= 900, 'and it turns at the app\u2019s own pace', beat + ' ms');

    // And it keeps turning through a re-render: the ring is drawn on the mark
    // the row already has, so a list refresh does not start it again.
    const spunBefore = await s.page.evaluate((sel) => {
      const mark = document.querySelector(sel + ' .row-mark');
      const anim = mark ? mark.getAnimations({ subtree: true })[0] : null;
      return anim ? Number(anim.currentTime) : -1;
    }, rowSel(working));
    const spinnerName = workingRow.title + ' renamed';
    await s.context.request.patch(s.url + '/api/sessions/' + working, { data: { title: spinnerName } });
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((want) =>
      [...document.querySelectorAll('#sessionList .chat-item .label')].some((n) => n.textContent === want),
    spinnerName, { timeout: 20000 });
    const spunAfter = await s.page.evaluate((sel) => {
      const mark = document.querySelector(sel + ' .row-mark');
      const anim = mark ? mark.getAnimations({ subtree: true })[0] : null;
      return anim ? Number(anim.currentTime) : -1;
    }, rowSel(working));
    ok(spunBefore >= 0 && spunAfter > spunBefore,
      'and a re-render of its row does not restart it',
      Math.round(spunBefore) + ' ms -> ' + Math.round(spunAfter) + ' ms');

    // Turning decoration off must not turn off the one thing that says work is
    // still happening: the ring has no reduced-motion behaviour of its own and
    // turns at the same pace here as everywhere else. Drawn still it reads as
    // a session that has stopped - which is exactly how it was read, from a
    // desk whose system had "reduce motion" on.
    await s.page.emulateMedia({ reducedMotion: 'reduce' });
    await wait(200);
    const still = await rowActivity(s.page, working);
    ok(!!still.ring && still.ring.name === 'spin' && still.ring.motion === '0.9s',
      'with reduced motion the ring turns at the pace it always turns at',
      still.ring ? still.ring.name + ' ' + still.ring.motion : 'no ring');
    const slow = await s.page.evaluate((sel) => {
      const mark = document.querySelector(sel + ' .row-mark');
      const anim = mark ? mark.getAnimations({ subtree: true })[0] : null;
      if (!anim) return -1;
      const was = Number(anim.currentTime);
      return new Promise((done) => setTimeout(() =>
        done(Number(anim.currentTime) - was), 400));
    }, rowSel(working));
    ok(slow > 100, 'and it is really running, not a still frame with a name on it',
      Math.round(slow) + ' ms of turn in 400 ms');
    await s.page.emulateMedia({ reducedMotion: null });

    // Rule 2 and rule 3 in the top bar: Status is a mark with its words in
    // the label a reader gets, and the microphone beside it is the one control
    // that carries a word, because it is the one a thumb has to find.
    const header = await s.page.evaluate(() => {
      const read = (id) => {
        const node = document.getElementById(id);
        return {
          text: (node.textContent || '').trim(),
          label: node.getAttribute('aria-label'),
          title: node.title,
          svg: node.querySelectorAll('svg').length,
          height: Math.round(node.getBoundingClientRect().height),
        };
      };
      return { status: read('statusBtn'), mic: read('micBtn') };
    });
    ok(header.status.text === '' && header.status.svg === 1
      && header.status.label === header.status.title,
      'Status is a mark, with its words in the label', JSON.stringify(header.status));
    ok(header.status.label === 'Summarize this session' && header.mic.label === 'Speak',
      'and the words say what pressing them does',
      header.status.label + ' / ' + header.mic.label);
    ok(header.mic.text === 'Speak' && header.mic.height >= 44,
      'the microphone is the one that carries its word, at a size a thumb can hit',
      JSON.stringify(header.mic));
    const gone = await s.page.evaluate(() => ({
      auto: document.querySelectorAll('.auto-switch').length,
      bar: !!document.getElementById('audioBar'),
      composer: !!document.getElementById('composer'),
    }));
    ok(gone.auto === 0 && !gone.bar && !gone.composer,
      'there is no mode switch, no audio bar and no line input on the page',
      JSON.stringify(gone));

    const spoken = await s.page.evaluate(() => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      const parts = [];
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const host = node.parentElement;
        if (!host || host.closest('.tip-bubble') || host.closest('.sr-only')) continue;
        if (host.closest('#term')) continue;
        if (!host.checkVisibility || !host.checkVisibility()) continue;
        parts.push(node.textContent);
      }
      return parts.join(' ');
    });
    ok(!/\b(exact|quiet|unknown|run_id|phase)\b/.test(spoken)
      && !spoken.includes(id) && !spoken.includes(working),
      'nothing the detector knows is written on the page in words',
      oneLine((/\b(exact|quiet|unknown|run_id|phase)\b/.exec(spoken) || ['none'])[0]));

    /* --------------------------------------------- the sidebar as a rail */

    // A column of names is what a desk wants and a laptop cannot spare. The
    // control beside the brand takes it down to a rail of marks, this device
    // remembers the answer, and the pane is re-measured for the width it has
    // actually been given.
    const wide = await s.page.evaluate(() =>
      Math.round(document.getElementById('sidebar').getBoundingClientRect().width));
    await s.page.click('#sideCollapse');
    await wait(400);
    const rail = await s.page.evaluate(() => {
      const bar = document.getElementById('sidebar');
      const seen = (sel) => {
        const node = document.querySelector(sel);
        return !!node && node.checkVisibility();
      };
      const row = document.querySelector('#sessionList .chat-item');
      return {
        width: Math.round(bar.getBoundingClientRect().width),
        brand: seen('.brand-name'),
        label: seen('#sessionList .chat-item .label'),
        newWords: seen('#newSession .btn-label'),
        foot: seen('#logout .btn-label'),
        mark: !!row && !!row.querySelector('.agent-mark'),
        named: row ? row.title : '',
        fill: getComputedStyle(bar).backgroundColor,
      };
    });
    ok(wide > 200 && rail.width <= 72 && rail.width < wide,
      'the collapse control takes the sidebar down to a rail', wide + ' -> ' + rail.width + ' px');
    ok(!rail.brand && !rail.label && !rail.newWords && !rail.foot
      && rail.mark && rail.named.length > 0,
    'the rail is marks and no words, and a row is named in its tooltip', JSON.stringify(rail));
    ok(white(rail.fill), 'and the rail is the same white as everything else', rail.fill);
    await shot(s.page, 'design-rail');

    // Per device, and it survives the reload it is there to be useful across.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(500);
    const kept = await s.page.evaluate(() => ({
      collapsed: document.body.classList.contains('side-collapsed'),
      width: Math.round(document.getElementById('sidebar').getBoundingClientRect().width),
    }));
    ok(kept.collapsed && kept.width <= 72, 'and a reload comes back to the rail',
      JSON.stringify(kept));

    // The rail gives its width to the pane, which is measured in characters
    // and so has to be measured again: expanding the sidebar takes those
    // columns back. The drawn screen is whole columns wide, so its width is
    // the column count in pixels.
    const screenWidth = () => s.page.$eval('#term .xterm-screen',
      (n) => Math.round(n.getBoundingClientRect().width));
    const railCols = await screenWidth();
    await s.page.click('#sideCollapse');
    await wait(600);
    const wideCols = await screenWidth();
    ok(railCols > wideCols,
      'and the pane is re-measured when the stage changes width',
      railCols + ' px (rail) -> ' + wideCols + ' px (column)');

    /* ----------------------------------- a line that puts itself away */

    // A notice is news. It never blocks, it can always be closed by hand, and
    // it goes on its own once it has been up long enough to read - otherwise
    // the top of the pane fills with lines about things that are over.
    const other = await s.context.newPage();
    const otherErrors = [];
    other.on('console', (msg) => { if (msg.type() === 'error') otherErrors.push(msg.text()); });
    other.on('pageerror', (err) => otherErrors.push(String(err)));
    await other.setViewportSize({ width: 640, height: 480 });
    await other.goto(s.url + '/#' + id, { waitUntil: 'domcontentloaded' });
    const otherUp = await other.waitForSelector('#term .xterm', { timeout: 20000 })
      .then(() => true).catch(() => false);
    ok(otherUp, 'the second viewer has the same session open',
      otherUp ? 'attached' : 'hash=' + await other.evaluate(() => location.hash)
        + ' errors=' + otherErrors.join(' | '));
    const raised = await s.page.waitForSelector('#termNotice:not([hidden])', { timeout: 20000 })
      .then(() => true).catch(() => false);
    ok(raised, 'a second viewer puts a line above the pane',
      raised ? await s.page.$eval('#termNotice', (n) => n.innerText.trim()) : 'nothing appeared');
    const closes = await s.page.waitForFunction(
      () => document.getElementById('termNotice').hidden, null, { timeout: 9000 })
      .then(() => true).catch(() => false);
    ok(closes, 'and it puts itself away without being dismissed',
      closes ? 'gone' : 'still up after 9 s');
    await other.close();

    // Rule 1 again, on the other page, and rule 2 on the admin cards.
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('.harness-card', { timeout: 20000 });
    const admin = await s.page.evaluate(() => {
      const cards = [...document.querySelectorAll('.card')];
      const fills = new Set(cards.map((c) => getComputedStyle(c).backgroundColor));
      return {
        fills: [...fills],
        body: getComputedStyle(document.body).backgroundColor,
        marks: [...document.querySelectorAll('.harness-card')].map((c) =>
          (c.querySelector('.agent-mark') || { dataset: {} }).dataset.agent || 'none'),
      };
    });
    ok(admin.body === WHITE && admin.fills.every(white),
      'the dashboard is white too, cards included', JSON.stringify(admin.fills));
    // Every harness the catalogue has gets a card - VS Code's included - and
    // every card its own mark.
    ok(['shell', 'claude', 'codex', 'opencode'].every((h) => admin.marks.includes(h))
      && admin.marks.every((m) => m !== 'none'),
      'and every harness card carries its mark', JSON.stringify(admin.marks));

    const paths = await s.page.evaluate(() => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      const parts = [];
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const host = node.parentElement;
        if (!host || host.closest('.tip-bubble')) continue;
        if (!host.checkVisibility || !host.checkVisibility()) continue;
        parts.push(node.textContent);
      }
      return parts.join(' ');
    });
    ok(!/\/(usr|bin|tmp|home|root)\//.test(paths), 'and no binary path is written on it in words',
      oneLine((/[^\s]*\/(usr|bin|tmp|home|root)\/[^\s]*/.exec(paths) || ['none'])[0]));

    await shot(s.page, 'design');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------ 28-33. activity, status, agent, audio */

// Everything below is about a session saying what it is doing without being
// asked, and about the two buttons that ask it something the terminal cannot
// answer. The detector runs on the server and ticks once a second, so every
// assertion here waits for a fact rather than for a fixed number of
// milliseconds, and reports how long it actually took.

const rowSel = (id) => '#sessionList .chat-item[data-id="' + id + '"]';

// rowActivity is everything the sidebar says about one session, read out of
// the drawn row rather than out of the API: the classes are the feature.
const rowActivity = (page, id) => page.evaluate((sel) => {
  const row = document.querySelector(sel);
  if (!row) return null;
  const mark = row.querySelector('.row-mark');
  const label = row.querySelector('.label');
  const glass = row.querySelector('.row-tasks');
  const glassBox = glass ? glass.getBoundingClientRect() : null;
  const ring = mark ? getComputedStyle(mark, '::after') : null;
  return {
    busy: !!mark && mark.classList.contains('busy'),
    waiting: !!mark && mark.classList.contains('waiting'),
    tasks: !!mark && mark.classList.contains('tasks'),
    // The hourglass, as drawn: shown at all, and how big.
    glass: glass && getComputedStyle(glass).display !== 'none' && glassBox.width > 0
      ? { w: Math.round(glassBox.width), h: Math.round(glassBox.height) }
      : null,
    unread: row.classList.contains('unread'),
    active: row.classList.contains('active'),
    dots: row.querySelectorAll('.dot').length,
    said: mark ? mark.title : '',
    weight: label ? getComputedStyle(label).fontWeight : '',
    ring: ring
      ? {
        colour: ring.borderTopColor,
        rest: ring.borderBottomColor,
        motion: ring.animationDuration,
        name: ring.animationName,
      }
      : null,
  };
}, rowSel(id));

// awaitRow waits for one fact about a row and hands back how long it took, so
// a verdict can print "spun after 1.2 s" instead of "true".
async function awaitRow(page, id, test, timeout = 15000) {
  const started = Date.now();
  let seen = null;
  while (Date.now() - started < timeout) {
    seen = await rowActivity(page, id);
    if (seen && test(seen)) return { ok: true, ms: Date.now() - started, seen };
    await wait(200);
  }
  return { ok: false, ms: Date.now() - started, seen };
}

const took = (r) => (r.ok ? (r.ms / 1000).toFixed(1) + ' s' : 'never, ' + JSON.stringify(r.seen));

// clickRow opens another session from the list, which is what makes the one
// left behind a row nobody is looking at.
//
// It waits for the row to become the active one, not merely for the address to
// change. Attaching is debounced by a tenth of a second - a run of taps down
// the list is one decision - so a second tap that lands inside that window is
// deliberately ignored, and a scenario that only waited for the hash would
// race the debounce and click into nothing.
async function clickRow(page, id) {
  await ensureNav(page);
  // The name, not the row: a row also carries an "i" and a "…", and neither of
  // them is what a person aiming at a session hits.
  await page.click(rowSel(id) + ' .label');
  await page.waitForFunction((want) => location.hash.slice(1) === want, id, { timeout: 15000 });
  await page.waitForSelector(rowSel(id) + '.active', { timeout: 15000 });
  await page.waitForSelector('#term .xterm', { timeout: 15000 });
}

/**
 * secondTab opens another tab on one session.
 *
 * It is what makes "finished and nobody saw it" testable without cheating.
 * Leaving a session by clicking another row blurs the terminal, and a blur is
 * a real escape sequence that a real program asked for - so it reaches the
 * pane as input, and input is what clears the unread mark. That is the right
 * behaviour (you were looking at it when it happened), but it means the two
 * things this suite has to prove separately - the mark appearing, and each of
 * the two ways it goes away - have to happen to a session this tab is not the
 * one typing into.
 */
async function secondTab(s, id) {
  const page = await s.context.newPage();
  page.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
  await page.goto(s.url + '/#' + id, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#term .xterm', { timeout: 20000 });
  await wait(1200);
  return page;
}

// A turn that takes a measurable amount of time, in the words each program
// understands. The three fakes carry the real furniture and the real signals.
// There is no shell here: a shell is never given a turn, so it has no status
// to watch and nothing to wait for the end of.
async function keepBusy(page, ms) {
  await typeLine(page, '/busy ' + ms);
}

// activityFor is one harness's whole story: it starts working and the row says
// so, it finishes while nobody is looking, and the name goes bold.
async function activityFor(harness) {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    // A second session to stand on. "Finished and nobody saw it" is only a
    // fact about a row that is not the one being looked at.
    const other = await startSession(s.page, 'shell');
    const id = await startSession(s.page, harness);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    // Start from a row that is not already turning, or "it spins while it is
    // working" would be a measurement of the moment before.
    const settled = await awaitRow(s.page, id, (r) => !r.busy, 20000);
    ok(settled.ok, harness + ' settles into idle before it is given anything to do', took(settled));

    await keepBusy(s.page, 8000);
    const spun = await awaitRow(s.page, id, (r) => r.busy, 8000);
    ok(spun.ok, 'the row spins while ' + harness + ' is working', took(spun));
    ok(!spun.seen || spun.seen.ring === null || spun.seen.ring.name === 'spin',
      'and the ring is what turns, not a glyph of its own',
      spun.seen && spun.seen.ring ? spun.seen.ring.name + ' ' + spun.seen.ring.motion : 'no ring');

    // Step off it while it is still working, so the end of the turn happens
    // with nobody watching - which is the whole of what unread means.
    await clickRow(s.page, other);
    const done = await awaitRow(s.page, id, (r) => !r.busy && r.unread, 30000);
    ok(done.ok, 'it stops spinning and its name goes bold when the turn ends', took(done));
    ok(!done.seen || Number(done.seen.weight) >= 600, 'the bold is real weight, not a colour',
      done.seen ? done.seen.weight : 'no row');

    await shot(s.page, 'activity-' + harness);
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// tasksFor is the turn that ends while something it started is still running:
// a background shell or a monitor under Claude Code, a background terminal
// under Codex, a background sub-agent under OpenCode. The row is neither
// working nor done - no ring, an hourglass on the mark's corner - and the bold
// and the chime wait for that work to end.
async function tasksFor(harness) {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const other = await startSession(s.page, 'shell');
    const id = await startSession(s.page, harness);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const settled = await awaitRow(s.page, id, (r) => !r.busy && !r.tasks, 20000);
    ok(settled.ok, harness + ' settles into idle first', took(settled));

    await typeLine(s.page, '/bg 9000');
    await clickRow(s.page, other);
    const pending = await awaitRow(s.page, id, (r) => r.tasks, 10000);
    ok(pending.ok, 'the turn ends with work still running, and the row says so', took(pending));
    const seen = pending.seen || {};
    ok(!seen.busy && !seen.waiting && seen.ring && seen.ring.name === 'none',
      'there is no ring: nothing is being worked on', JSON.stringify(seen.ring));
    ok(!!seen.glass && seen.glass.w >= 10 && seen.glass.w <= 14,
      'an hourglass sits on the corner of the mark', JSON.stringify(seen.glass));
    ok(seen.said === 'Turn finished · a background task is still running',
      'and the mark says what it means', seen.said);
    ok(!seen.unread, 'a turn that left work running is not unread', String(seen.unread));
    const server = await sessionRow(s, id);
    ok(server.activity && server.activity.state === 'tasks' && !server.activity.unread,
      'the server says the same', JSON.stringify(server.activity));
    await s.page.locator(rowSel(id)).screenshot({ path: join(SHOTS, 'tasks-' + harness + '-row.png') });

    const done = await awaitRow(s.page, id, (r) => !r.tasks && !r.busy && r.unread, 30000);
    ok(done.ok, 'when the work ends the hourglass goes and the name goes bold', took(done));
    ok(!done.seen || !done.seen.glass, 'no hourglass is left behind', JSON.stringify(done.seen && done.seen.glass));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

const taskclaude = () => tasksFor('claude');
const taskcodex = () => tasksFor('codex');
const taskopencode = () => tasksFor('opencode');

const activityclaude = () => activityFor('claude');
const activitycodex = () => activityFor('codex');
const activityopencode = () => activityFor('opencode');

// A permission prompt is the one state that may sit for an hour: the person it
// is waiting for is driving. So it is drawn differently from working - an amber
// ring that does not turn - and it does not time out.
async function activitywaiting() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  let asking = null;
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const other = await startSession(s.page, 'shell');
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await clickRow(s.page, other);

    // The prompt is raised from a tab of its own, so the tab that watches the
    // row never touches the session and the mark is honestly unseen. With the
    // exact layer gone the only things holding `waiting` are the screen and
    // the stickiness rule, which is the case worth testing.
    asking = await secondTab(s, id);
    await typeLine(asking, '/nofile');
    await wait(500);
    await typeLine(asking, '/ask');

    const asked = await awaitRow(s.page, id, (r) => r.waiting, 20000);
    ok(asked.ok, 'a permission prompt draws a ring that does not turn', took(asked));
    // The amber that used to be a dot beside the name is the ring itself: the
    // one colour a row carries, on the mark it belongs to.
    ok(!asked.seen || (asked.seen.ring && asked.seen.ring.colour === 'rgb(184, 129, 26)'),
      'and the ring, not a dot, is what goes amber',
      asked.seen && asked.seen.ring ? asked.seen.ring.colour : 'no ring');
    ok(!asked.seen || (asked.seen.dots === 0 && asked.seen.said === 'Needs an answer'),
      'the row has no dot, and its mark says what it is doing',
      asked.seen ? asked.seen.dots + ' dots, ' + asked.seen.said : 'no row');
    ok(!asked.seen || !asked.seen.ring || asked.seen.ring.name === 'none',
      'the waiting ring carries no animation at all',
      asked.seen && asked.seen.ring ? asked.seen.ring.name : 'no ring');
    const bold = await awaitRow(s.page, id, (r) => r.unread, 15000);
    ok(bold.ok, 'needing an answer is unread too', took(bold));

    // HardQuiet is thirty seconds of silence, and a prompt is silent by
    // nature: somebody is driving. This is the assertion the whole stickiness
    // rule exists for, so it is measured rather than reasoned about.
    await wait(34000);
    const still = await rowActivity(s.page, id);
    ok(!!still && still.waiting && still.ring && still.ring.colour === 'rgb(184, 129, 26)',
      'and thirty-four silent seconds later it is still waiting', JSON.stringify(still));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    if (asking) await asking.close().catch(() => {});
    await s.stop();
  }
}

// The runaway guard. A harness that paints the furniture of a turn and then
// stops writing anything at all - no output, no status file - must not spin
// for ever: thirty seconds of silence with no exact signal is idle.
async function activityfallback() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(1500);

    await typeLine(s.page, '/nofile');
    await wait(500);
    await typeLine(s.page, '/hang 90000');
    const spun = await awaitRow(s.page, id, (r) => r.busy, 10000);
    ok(spun.ok, 'a hung turn starts out looking like work', took(spun));

    const freed = await awaitRow(s.page, id, (r) => !r.busy, 45000);
    ok(freed.ok, 'and the row leaves busy on its own, with the harness still hung', took(freed));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// The unread mark, and the two things that take it away: opening the row, and
// typing into the session it belongs to.
async function unread() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  let working = null;
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const other = await startSession(s.page, 'shell');
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const watched = await rowActivity(s.page, id);
    ok(!watched.unread && watched.active, 'the session being looked at is never bold',
      JSON.stringify({ unread: watched.unread, active: watched.active }));

    await clickRow(s.page, other);
    working = await secondTab(s, id);
    await typeLine(working, '/busy 2500');
    const bold = await awaitRow(s.page, id, (r) => r.unread, 30000);
    ok(bold.ok, 'a turn that ends where nobody is looking makes its name bold', took(bold));
    ok(!bold.seen || Number(bold.seen.weight) >= 600, 'the bold is real weight, not a colour',
      bold.seen ? bold.seen.weight : 'no row');

    await clickRow(s.page, id);
    const opened = await awaitRow(s.page, id, (r) => !r.unread, 10000);
    ok(opened.ok, 'opening the row is seeing it, and clears the mark', took(opened));
    const server = await sessionRow(s, id);
    ok(server.activity && server.activity.unread === false,
      'and the server agrees, so every other tab stops bolding it too',
      JSON.stringify(server.activity));

    // The other way it goes away: somebody typed into that session. It is the
    // server that decides, on an input frame it accepted, so the tab that is
    // not typing hears about it in the ordinary way.
    await clickRow(s.page, other);
    await typeLine(working, '/busy 2500');
    const again = await awaitRow(s.page, id, (r) => r.unread, 30000);
    ok(again.ok, 'a second turn ends unseen and the name is bold again', took(again));
    await working.click('#term .xterm-screen');
    await focusTerm(working);
    await working.keyboard.type('x');
    const typed = await awaitRow(s.page, id, (r) => !r.unread, 15000);
    ok(typed.ok, 'and typing into that session clears it everywhere', took(typed));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    if (working) await working.close().catch(() => {});
    await s.stop();
  }
}

/* ------------------------------- a chime and a notification when work ends */

// stubNotify stands in for the two browser facilities a headless shell has no
// use for: the notification tray and the speaker.
//
// Both are recorded rather than faked away, because what is under test is not
// that this machine can ring - it is that the page rings once, for the right
// session, with the right words, and only when the two switches say so.
async function stubNotify(page) {
  await page.addInitScript(() => {
    window.__notes = [];
    window.__beeps = [];
    window.__asked = 0;
    window.__toasts = [];

    class FakeNotification {
      constructor(title, options) {
        this.title = title;
        this.options = options || {};
        window.__notes.push({ title, options: this.options });
      }

      close() { this.closed = true; }
    }
    // A permission the browser granted this origin survives a reload, so the
    // stub starts where a second visit would.
    FakeNotification.permission = 'granted';
    FakeNotification.requestPermission = () => {
      window.__asked += 1;
      FakeNotification.permission = window.__perm || 'granted';
      return Promise.resolve(FakeNotification.permission);
    };
    window.Notification = FakeNotification;

    // One chime is one oscillator with two notes scheduled on it, so one
    // entry here is one chime.
    class FakeAudioContext {
      constructor() {
        this.state = 'running';
        this.currentTime = 0;
        this.destination = { kind: 'destination' };
      }

      createOscillator() {
        const notes = [];
        return {
          type: 'sine',
          frequency: { setValueAtTime: (hz) => notes.push(hz) },
          connect() {},
          disconnect() {},
          start: () => window.__beeps.push(notes.slice()),
          stop() {},
        };
      }

      createGain() {
        const ramp = () => {};
        return {
          gain: {
            setValueAtTime: ramp,
            linearRampToValueAtTime: ramp,
            exponentialRampToValueAtTime: ramp,
          },
          connect() {},
          disconnect() {},
        };
      }

      resume() { this.state = 'running'; return Promise.resolve(); }
    }
    window.AudioContext = FakeAudioContext;
    window.webkitAudioContext = FakeAudioContext;

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

// The two switches as the options menu draws them: what they claim, what they
// say, and whether they can be pressed. The menu is left open; the next
// option() or closeOptions() is what the scenario does with it.
const toggles = async (page) => {
  const all = await options(page);
  return { sound: all.sound, notify: all.notify };
};

// awaitNotify waits for the notification switch to settle where the browser's
// answer put it - the permission is asked for asynchronously.
const awaitNotify = (page, on) => page.waitForFunction((want) => {
  const node = document.querySelector('.menu [data-option="notify"]');
  return !!node && node.getAttribute('aria-checked') === want;
}, on ? 'true' : 'false', { timeout: 5000 });

const rang = (page) => page.evaluate(() => ({
  beeps: (window.__beeps || []).length,
  notes: (window.__notes || []).slice(),
  asked: window.__asked || 0,
  toasts: (window.__toasts || []).slice(),
}));

// drive makes one turn happen and waits for it to be over, which is the edge
// the whole feature hangs off.
//
// It is a Claude session and not a shell because a shell has no turns: nothing
// is asked of it and nothing is reported back, so its row carries no status
// and it never chimes.
async function drive(s, id, ms = 2000) {
  await keepBusy(s.page, ms);
  const spun = await awaitRow(s.page, id, (r) => r.busy, 10000);
  const done = spun.ok && (await awaitRow(s.page, id, (r) => !r.busy, 20000)).ok;
  // The frame that ends the turn and the call it makes are the same tick, but
  // the assertion is about a count, so it is read a moment later.
  await wait(600);
  return done;
}

// A session that stops working while nobody is looking at the page: a chime,
// a notification, and two switches that decide whether either happens.
async function notify() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await stubNotify(s.page);
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    // Both are facts about this device, so they are in the options menu
    // before there is a session to have an opinion about - and they are no
    // longer buttons of their own in the bar.
    const bar = await s.page.evaluate(() => ({
      options: !!document.getElementById('optionsBtn')?.checkVisibility(),
      sound: !!document.getElementById('soundBtn'),
      notify: !!document.getElementById('notifyBtn'),
    }));
    ok(bar.options && !bar.sound && !bar.notify,
      'the options button is in the header with no session attached, and the two toggles are not',
      JSON.stringify(bar));
    const fresh = await toggles(s.page);
    ok(!!fresh.sound && !!fresh.notify, 'both switches are in the options menu',
      JSON.stringify(fresh));
    ok(fresh.sound.on && /^Sound/.test(fresh.sound.text),
      'sound is on by default, and says so', JSON.stringify(fresh.sound));
    ok(!fresh.notify.on && !fresh.notify.disabled && /^Notifications/.test(fresh.notify.text),
      'notifications are off by default, because a permission has to be asked for',
      JSON.stringify(fresh.notify));

    await option(s.page, 'notify');
    await awaitNotify(s.page, true);
    const asked = await rang(s.page);
    ok(asked.asked === 1, 'turning notifications on asks the browser, from the click',
      asked.asked + ' request(s)');
    const on = await toggles(s.page);
    ok(on.notify.on, 'and the switch says so, in the menu that stayed open', JSON.stringify(on.notify));
    await closeOptions(s.page);

    // Per device, so a reload finds them where they were left.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#optionsBtn', { timeout: 15000 });
    const kept = await toggles(s.page);
    ok(kept.sound.on && kept.notify.on, 'a reload keeps both choices', JSON.stringify({
      sound: kept.sound.on, notify: kept.notify.on,
    }));
    await closeOptions(s.page);

    const opened = await rang(s.page);
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    // Every harness is busy while it starts up and idle the moment it is
    // ready, and that is not a turn ending: a session that has just been made
    // has been asked nothing, so nothing it does on its way to a prompt is
    // worth a sound on a phone in somebody's pocket.
    const settled = await awaitRow(s.page, id, (r) => !r.busy, 20000);
    ok(settled.ok, 'the new session settles into idle', took(settled));
    await wait(1200);
    const started = await rang(s.page);
    ok(started.beeps === opened.beeps && started.notes.length === opened.notes.length,
      'and starting one says nothing at all, with both switches on',
      (started.beeps - opened.beeps) + ' chime(s), '
      + (started.notes.length - opened.notes.length) + ' note(s)');
    const title = await s.page.$eval(rowSel(id) + ' .label', (n) => n.textContent);

    // A phone's top bar has to fit every control with a session attached.
    // The bar gives up its spacing rather than spilling off the edge, and
    // nothing in it may become untappable doing so.
    await s.page.setViewportSize({ width: 390, height: 844 });
    await wait(500);
    const narrow = await s.page.evaluate(() => {
      const box = (id) => {
        const node = document.getElementById(id);
        if (!node || node.hidden) return 0;
        return Math.round(node.getBoundingClientRect().width);
      };
      return {
        overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        options: box('optionsBtn'),
        speak: box('statusBtn'),
        title: document.getElementById('sessionTitle').getBoundingClientRect().width,
      };
    });
    ok(narrow.overflow <= 1, 'a phone with a session attached does not scroll sideways',
      narrow.overflow + 'px');
    ok(narrow.options >= 26 && narrow.options === narrow.speak,
      'and the options button is the size of the other controls beside it',
      JSON.stringify(narrow));
    ok(narrow.title > 40, 'and the session\'s name still has room', String(narrow.title));
    await s.page.setViewportSize({ width: 1280, height: 720 });
    await wait(500);

    let before = await rang(s.page);
    ok(await drive(s, id, 3000), 'a turn starts and finishes', '/busy 3000');
    let after = await rang(s.page);
    ok(after.beeps - before.beeps === 1, 'it chimed exactly once',
      (after.beeps - before.beeps) + ' chime(s)');
    const note = after.notes[after.notes.length - 1];
    ok(after.notes.length - before.notes.length === 1 && !!note && note.title === title
      && note.options.body === 'Finished',
      'and one notification named the session and said what happened',
      JSON.stringify(note || null) + ' vs ' + JSON.stringify(title));
    ok(!!note && note.options.tag === 'socrates:' + id && !!note.options.icon,
      'tagged with the session, so a second one replaces it rather than stacking',
      note ? note.options.tag : 'none');

    // Muted: the notification is the one that survives, because it is the one
    // that can be read afterwards.
    ok(!(await option(s.page, 'sound')), 'the sound switch turns it off', 'off');
    await closeOptions(s.page);
    const muted = await toggles(s.page);
    ok(!muted.sound.on, 'and the menu says so when it is opened again', JSON.stringify(muted.sound));
    await closeOptions(s.page);
    before = await rang(s.page);
    ok(await drive(s, id, 3000), 'a second turn starts and finishes', '/busy 3000');
    after = await rang(s.page);
    ok(after.beeps - before.beeps === 0 && after.notes.length - before.notes.length === 1,
      'muted, it is silent and still notifies',
      (after.beeps - before.beeps) + ' chime(s), '
      + (after.notes.length - before.notes.length) + ' note(s)');

    await option(s.page, 'notify');
    await awaitNotify(s.page, false);
    await closeOptions(s.page);
    before = await rang(s.page);
    ok(await drive(s, id, 3000), 'a third turn starts and finishes', '/busy 3000');
    after = await rang(s.page);
    ok(after.notes.length - before.notes.length === 0 && after.beeps - before.beeps === 0,
      'with both off, a finished turn says nothing at all',
      (after.beeps - before.beeps) + ' chime(s), '
      + (after.notes.length - before.notes.length) + ' note(s)');

    // Both back on, so the reload below is measured against a page that would
    // otherwise have plenty to say. Turning sound on plays it once, which is
    // how a person hears what they have just chosen.
    const quiet = await rang(s.page);
    await option(s.page, 'sound');
    await option(s.page, 'notify');
    await awaitNotify(s.page, true);
    await closeOptions(s.page);
    const previewed = await rang(s.page);
    ok(previewed.beeps - quiet.beeps === 1, 'turning sound on plays it once as a preview',
      (previewed.beeps - quiet.beeps) + ' chime(s)');

    // The handshake replays every running session's state, and a replay is not
    // news: a page that reloads while a turn is running must not ring for a
    // turn that started before it existed.
    await keepBusy(s.page, 12000);
    const running = await awaitRow(s.page, id, (r) => r.busy, 10000);
    ok(running.ok, 'a turn is still running when the page is reloaded', took(running));
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const seeded = await awaitRow(s.page, id, (r) => r.busy, 15000);
    ok(seeded.ok, 'and the reloaded page draws it as still working', took(seeded));
    const replay = await rang(s.page);
    ok(replay.beeps === 0 && replay.notes.length === 0,
      'nothing fired on the replay, with both switches on',
      replay.beeps + ' chime(s), ' + replay.notes.length + ' note(s)');

    // A browser that says no. The switch may not claim something it cannot
    // do, and the reason belongs where the tap was.
    await option(s.page, 'notify');
    await awaitNotify(s.page, false);
    await s.page.evaluate(() => { window.__perm = 'denied'; });
    await option(s.page, 'notify');
    await wait(600);
    const denied = await toggles(s.page);
    const said = await rang(s.page);
    ok(!denied.notify.on, 'a refused permission leaves the switch off', JSON.stringify(denied.notify));
    await closeOptions(s.page);
    ok(said.toasts.some((t) => /blocked for this site/.test(t)),
      'and the page says why, where the tap was', JSON.stringify(said.toasts.slice(-2)));

    await shot(s.page, 'notify');
    // The bell subscribes to Web Push where the browser offers it. The test
    // browser is an incognito profile, where Chrome refuses and says so in the
    // console itself; the page falls back to its own notification, which is
    // what the assertions above measure.
    const PUSH_REFUSED = /Push API in incognito/;
    ok(unexpected(s.errors, PUSH_REFUSED).length === 0, 'no console errors',
      unexpected(s.errors, PUSH_REFUSED).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------ status, agent and audio helpers */

// A 44 byte WAV header and one sample of silence: enough for the browser to
// accept it as audio and play nothing. No test ever calls Google - what is
// under test is that the page asks for the render and hands it the right
// words, not that a Google project is billed for this run.
// silentWav is `seconds` of silence as a 16 kHz mono WAV - one sample when
// nothing is asked for, which is what a scenario that only counts requests
// needs, and a real length for one that has to catch the page while it is
// still reading.
function silentWav(seconds = 0) {
  const samples = Math.max(1, Math.round(16000 * seconds));
  const bytes = samples * 2;
  const data = Buffer.alloc(44 + bytes);
  data.write('RIFF', 0);
  data.writeUInt32LE(36 + bytes, 4);
  data.write('WAVEfmt ', 8);
  data.writeUInt32LE(16, 16);
  data.writeUInt16LE(1, 20);
  data.writeUInt16LE(1, 22);
  data.writeUInt32LE(16000, 24);
  data.writeUInt32LE(32000, 28);
  data.writeUInt16LE(2, 32);
  data.writeUInt16LE(16, 34);
  data.write('data', 36);
  data.writeUInt32LE(bytes, 40);
  return data;
}

/**
 * stubSpeech intercepts the one request the page makes to be read out loud and
 * records what it was asked to say.
 *
 * `delay` holds the answer back, which is how a scenario gets a window in
 * which the page is reliably "reading" without depending on a headless
 * browser's audio clock; `seconds` is how long the audio it finally gets is.
 */
async function stubSpeech(context, { seconds = 0, delay = 0 } = {}) {
  const said = [];
  await context.route('**/api/voice/speak', async (route) => {
    let text = '';
    try { text = (route.request().postDataJSON() || {}).text || ''; } catch { /* not JSON */ }
    said.push(text);
    if (delay) await wait(delay);
    await route.fulfill({ status: 200, contentType: 'audio/wav', body: silentWav(seconds) });
  });
  return said;
}

const noticeText = (page) => page.evaluate(() => {
  const host = document.getElementById('termNotice');
  if (!host || host.hidden) return '';
  const line = host.querySelector('.notice-text');
  return line ? line.textContent : '';
});

// A session names itself. The first prompt it is sent is what the sidebar row
// and the header are called from then on - once, in English, in a few words -
// and neither is reloaded to find out: the name arrives on the socket every
// browser already has open. While the model is being asked the name pulses,
// and a naming that fails says so in a dialog rather than never arriving.
async function sessiontitle() {
  const want = 'Fix failing parser test';
  // With the quotes and the full stop a model puts round a title, because
  // taking them off again is part of the feature. Held back long enough for
  // the pulse to be seen.
  const stub = await openRouterStub({ text: '"' + want + '."', delay: 1500 });
  const asked = () => stub.calls.filter((c) => String(c.path).includes('chat/completions')).length;
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await pointGateway(s, stub);
    await open(s);
    const id = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    const before = await s.page.$eval(rowSel(id) + ' .label', (n) => n.textContent.trim());
    ok(before.startsWith('Claude Code'), 'it starts life under the placeholder name', before);

    // The harness starting up is busy and then idle before anybody has typed.
    // That is not a prompt, and a name made from that screen is the name of
    // the program.
    const settled = await awaitRow(s.page, id, (r) => !r.busy, 20000);
    ok(settled.ok, 'it settles before it is given anything to do', took(settled));
    await wait(1500);
    ok(asked() === 0, 'the start-up is not named after', asked() + ' calls');

    // The first prompt: the moment it goes to work, the name is asked for and
    // pulses until it arrives.
    await keepBusy(s.page, 3000);
    const spun = await awaitRow(s.page, id, (r) => r.busy, 10000);
    ok(spun.ok, 'it goes to work on the first thing it is asked', took(spun));
    await s.page.waitForSelector(rowSel(id) + ' .label.titling', { timeout: 10000 });
    ok(true, 'the row pulses while the name is being thought of', 'label.titling');
    const pulsing = await s.page.$eval('#sessionTitle', (n) => n.classList.contains('titling'));
    ok(pulsing, 'and so does the header of the session being watched', String(pulsing));

    await s.page.waitForFunction((arg) => {
      const node = document.querySelector(arg.sel);
      return !!node && node.textContent.trim() === arg.want && !node.classList.contains('titling');
    }, { sel: rowSel(id) + ' .label', want }, { timeout: 30000 });
    ok(true, 'the row renames itself and stops pulsing, with no reload', want);

    const header = await s.page.$eval('#sessionTitle', (n) => n.textContent.trim());
    ok(header === want, 'and the header of the session being watched says the same', header);

    // Once. A second prompt is not a second name, and it is not asked for again.
    await awaitRow(s.page, id, (r) => !r.busy, 20000);
    const asks = asked();
    await keepBusy(s.page, 1500);
    await awaitRow(s.page, id, (r) => r.busy, 10000);
    await awaitRow(s.page, id, (r) => !r.busy, 20000);
    await wait(2500);
    ok(asks === 1 && asked() === 1, 'the naming happened exactly once', asks + ' then ' + asked());
    const still = await s.page.$eval(rowSel(id) + ' .label', (n) => n.textContent.trim());
    ok(still === want, 'and the name it was given stayed', still);
    await shot(s.page, 'session-title');

    // A gateway that refuses is said out loud, in a dialog, and the pulse
    // stops.
    stub.refuse = 401;
    const failing = await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const quiet = await awaitRow(s.page, failing, (r) => !r.busy, 20000);
    ok(quiet.ok, 'a second session settles', took(quiet));
    await keepBusy(s.page, 1500);
    await s.page.waitForSelector('dialog.modal[open]', { timeout: 15000 });
    const said = await s.page.$eval('dialog.modal[open]', (n) => n.textContent);
    ok(/Could not name the session/.test(said) && /API key/.test(said),
      'a failed naming opens a dialog that says why', said.trim().slice(0, 160));
    const stillPulsing = await s.page.$$eval(rowSel(failing) + ' .label.titling', (n) => n.length);
    ok(stillPulsing === 0, 'and the row stops pulsing', String(stillPulsing));
    await s.page.click('dialog.modal[open] .btn.primary');

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// pointGateway saves the key and the stub's address the way the dashboard
// would, which is the only supported way to make this app talk to a mock.
async function pointGateway(s, stub) {
  const saved = await s.context.request.put(s.url + '/api/settings', {
    data: { settings: { openrouter: { api_key: 'e2e-key', base_url: stub.url } } },
  });
  ok(saved.ok(), 'the gateway is pointed at the stub', saved.status() + ' ' + stub.url);
}

// watchTicker records every line the one-line ticker ever shows, in order. A
// line that rises in and out again inside a poll interval is exactly what this
// has to catch, so it is an observer and not a poll.
async function watchTicker(page) {
  await page.evaluate(() => {
    window.__ticker = [];
    const host = document.getElementById('tickerWindow');
    new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          const said = (node.textContent || '').trim();
          if (said) window.__ticker.push(said);
        }
      }
    }).observe(host, { childList: true });
  });
  return () => page.evaluate(() => window.__ticker.slice());
}

/* ------------------------------------------------------- 34. status-speak */

// The Status button: one request, a spinner while it is being made, the answer
// on screen as words, and the same words handed to the voice.
async function statusspeak() {
  const stub = await openRouterStub({ text: 'Claude Code has finished and is waiting for your next instruction.' });
  const s = await start({
    viewport: { width: 1280, height: 720 },
    args: ['--autoplay-policy=no-user-gesture-required'],
  });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await pointGateway(s, stub);
    const said = await stubSpeech(s.context);
    await open(s);
    await startSession(s.page, 'claude');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    // The status is held for a moment on its way out, so that what the button
    // does while it waits is measured rather than caught in passing.
    await s.page.route('**/api/sessions/*/status', async (route) => {
      await new Promise((done) => setTimeout(done, 2500));
      await route.fallback();
    });
    await s.page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 15000 });
    await s.page.click('#statusBtn');
    // The tap has to land visibly before the answer does: the button spins and
    // the one line under the top bar says what is being done.
    const spinning = await s.page.waitForFunction(
      () => document.getElementById('statusBtn').classList.contains('working'),
      null, { timeout: 5000 }).then(() => true).catch(() => false);
    ok(spinning, 'the button says the tap landed before the answer arrives',
      spinning ? 'the spinner is on it' : 'nothing happened');

    // And what it spins is a ring around the mark, never the mark itself. The
    // drawing is a screen being read out; a screen that is turning is a
    // drawing of nothing, and it was read as one. It is the same ring, the
    // same hairline, the same arc in the page's own ink and the same 900 ms as
    // the one a working session wears in the sidebar, because it means the
    // same thing in both places.
    const ring = await s.page.evaluate(() => {
      const btn = document.getElementById('statusBtn');
      const after = getComputedStyle(btn, '::after');
      const mark = getComputedStyle(btn.querySelector('svg'));
      return {
        name: after.animationName,
        beat: after.animationDuration,
        size: Math.round(parseFloat(after.width) || 0),
        arc: after.borderTopColor,
        rest: after.borderRightColor,
        onTheMark: mark.animationName,
        glyph: Math.round(parseFloat(mark.width) || 0),
      };
    });
    ok(ring.name === 'spin' && ring.onTheMark === 'none',
      'and it is a ring that turns, not the drawing',
      'ring: ' + ring.name + ', mark: ' + ring.onTheMark);
    ok(ring.size > ring.glyph,
      'drawn outside the mark, the way a working session is drawn in the sidebar',
      ring.size + 'px around a ' + ring.glyph + 'px mark');
    ok(ring.beat === '0.9s' && ring.arc === 'rgb(23, 24, 27)' && ring.rest === 'rgb(220, 221, 225)',
      'in the page\u2019s own ink, on a ring a shade under it, at the app\u2019s own pace',
      ring.beat + ' ' + ring.arc + ' on ' + ring.rest);
    ok(contrastOf(ring.arc, ring.rest) >= 3,
      'so the arc is legible against the ring it turns on',
      contrastOf(ring.arc, ring.rest).toFixed(1) + ':1');

    // Turning decoration off must not turn off the one thing that says the
    // answer is still being made, so the ring keeps its own pace here as the
    // sidebar's does. Squashed to .01ms it is not a slower ring, it is a still
    // one - and a still ring says the opposite of what it is there to say.
    await s.page.emulateMedia({ reducedMotion: 'reduce' });
    await wait(120);
    const calm = await s.page.evaluate(() => {
      const after = getComputedStyle(document.getElementById('statusBtn'), '::after');
      const node = document.getElementById('statusBtn');
      const anim = node.getAnimations({ subtree: true })[0] || null;
      return { name: after.animationName, beat: after.animationDuration, running: !!anim };
    });
    ok(calm.name === 'spin' && calm.beat === '0.9s' && calm.running,
      'with reduced motion it turns at the pace it always turns at, rather than standing still',
      calm.name + ' ' + calm.beat + (calm.running ? ', running' : ', not running'));
    await s.page.emulateMedia({ reducedMotion: null });

    await s.page.waitForFunction(() => {
      const host = document.getElementById('termNotice');
      return !!host && !host.hidden && host.dataset.kind === 'status';
    }, null, { timeout: 40000 });
    const shown = await noticeText(s.page);
    ok(shown.includes('finished') && shown === stub.text,
      'what the model said is on screen, in words', oneLine(shown));
    // The line is on a timer, so everything about it is read while it is up.
    const tip = await s.page.$eval('#termNotice .tip-bubble', (n) => n.textContent).catch(() => '');
    await shot(s.page, 'status-speak');

    for (let i = 0; i < 60 && said.length === 0; i += 1) await wait(200);
    ok(said.length === 1, 'and it was read out loud exactly once', said.length + ' render(s)');
    ok(said[0] === stub.text, 'with the words that are on the screen', oneLine(said[0] || ''));

    // The model id and the state are facts about the machine, so they are
    // behind the "i" and never in the sentence.
    ok(!shown.includes('stub/model') && !shown.includes('idle'),
      'no identifier leaked into the line that is read out loud', oneLine(shown));
    ok(tip.length > 0, 'and the details are under the "i" beside it', oneLine(tip));

    const cleared = await s.page.waitForFunction(
      () => !document.getElementById('statusBtn').classList.contains('working'),
      null, { timeout: 10000 }).then(() => true).catch(() => false);
    ok(cleared, 'and the spinner stops when there is nothing left to wait for',
      cleared ? 'stopped' : 'still turning');

    // And the line does not sit there for ever: it is one sentence, it has
    // been read, and the top of the pane is free again.
    const gone = await s.page.waitForFunction(() => document.getElementById('termNotice').hidden,
      null, { timeout: 9000 }).then(() => true).catch(() => false);
    ok(gone, 'and the answer puts itself away once it has been read',
      gone ? 'gone' : 'still up after 9 s');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

/* ------------------------------------------------------ 35. status-ticker */

// Pressing Status is a question asked over a network, and the phases of it
// arrive in the ticker in the order they happen: the screen is read, the model
// is asked, the answer is spoken, and the answer itself is the last line.
async function statusticker() {
  const stub = await openRouterStub({ text: 'The tests passed and it is waiting.' });
  const s = await start({
    viewport: { width: 1280, height: 720 },
    args: ['--autoplay-policy=no-user-gesture-required'],
  });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await pointGateway(s, stub);
    await stubSpeech(s.context);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const lines = await watchTicker(s.page);

    await s.page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 15000 });
    await s.page.click('#statusBtn');
    const arrived = await s.page.waitForFunction(
      (want) => (window.__ticker || []).some((l) => l === want),
      stub.text, { timeout: 45000 }).then(() => true).catch(() => false);
    const said = await lines();
    ok(arrived, 'the answer itself is the last thing the ticker says', oneLine(said.join(' → ')));

    const order = said.map((l) => (
      /^Reading the screen$/.test(l) ? 'capturing'
        : /^Asking /.test(l) ? 'asking'
          : /^Speaking$/.test(l) ? 'speaking'
            : l === stub.text ? 'done' : 'other'));
    const wanted = ['capturing', 'asking', 'speaking', 'done'];
    // Every phase, in this order, with nothing of another kind between them.
    const kept = order.filter((o) => o !== 'other');
    ok(kept.join(',') === wanted.join(','), 'the phases appear in order and none is missing',
      kept.join(',') + '  [' + oneLine(said.join(' → ')) + ']');

    // One window, one line: the ticker never grows into a log.
    const window = await s.page.evaluate(() => {
      const host = document.getElementById('termTicker');
      return {
        hidden: host.hidden,
        height: Math.round(document.getElementById('tickerWindow').getBoundingClientRect().height),
        fill: getComputedStyle(host).backgroundColor,
      };
    });
    ok(!window.hidden && window.height <= 24, 'it is one line high and no more',
      JSON.stringify(window));
    ok(window.fill === WHITE, 'on the same white as everything else', window.fill);

    // And it gives the window back: outside auto mode, with nothing happening,
    // there is nothing to say.
    const gone = await s.page.waitForFunction(() => document.getElementById('termTicker').hidden,
      null, { timeout: 20000 }).then(() => true).catch(() => false);
    ok(gone, 'and when there is nothing happening it is not there at all',
      gone ? 'hidden again' : 'still on screen');

    await shot(s.page, 'status-ticker');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

/* ---------------------------------------------------------- 36. dictate */

// micRow is the microphone and its recording sheet as the page is actually
// drawing them: the pill in the top bar and - while one is running - the sheet
// with the two endings a recording has.
const micRow = (page) => page.evaluate(() => {
  const node = (id) => document.getElementById(id);
  const box = (id) => {
    const n = node(id);
    if (!n) return null;
    const r = n.getBoundingClientRect();
    return { w: Math.round(r.width), h: Math.round(r.height) };
  };
  const sheet = node('recSheet');
  const clock = node('recTime');
  const pill = node('micBtn');
  return {
    pillInBar: !!pill && !!pill.closest('.topbar'),
    pillText: pill ? pill.textContent.trim() : '',
    pillBox: box('micBtn'),
    pillDisabled: !!pill && pill.disabled,
    panel: !!node('chatPanel'),
    open: !!sheet && sheet.open,
    keep: box('recSend'),
    drop: box('recCancel'),
    labels: ['recSend', 'recCancel'].map((id) => (node(id) || {}).textContent || '').join(','),
    bars: document.querySelectorAll('#recMeter .rec-bar').length,
    clock: clock ? clock.textContent.trim() : '',
  };
});

// The microphone, which is the only way into a session that is not a keyboard:
// a pill in the top bar that opens a sheet, two endings while it records, a
// discarded recording that costs nothing, and a kept one whose words are put
// on the prompt of the pane - and left there, because the Enter that runs them
// is the person's.
async function dictate() {
  const stub = await openRouterStub({ text: 'echo dictated-into-the-pane' });
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
    // A dedicated transcriber, so what comes back is the stub's fixed answer
    // rather than anything the page had to reason about.
    const saved = await s.context.request.put(s.url + '/api/settings', {
      data: {
        settings: {
          openrouter: { api_key: 'e2e-key', base_url: stub.url, transcribe_model: 'openai/whisper-1' },
        },
      },
    });
    ok(saved.ok(), 'the gateway is pointed at the stub', saved.status() + ' ' + stub.url);
    const transcribes = [];
    await s.page.route('**/api/voice/transcribe', async (route) => {
      transcribes.push(route.request().method());
      await route.fallback();
    });

    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    const idle = await micRow(s.page);
    ok(idle.pillInBar && /Speak/.test(idle.pillText),
      'the microphone is a pill in the top bar', idle.pillText);
    ok(idle.pillBox && idle.pillBox.h >= 44,
      'big enough to be hit by a thumb', JSON.stringify(idle.pillBox));
    ok(!idle.panel, 'and there is no chat panel behind it any more', String(idle.panel));
    ok(!idle.open, 'no recording sheet is open', String(idle.open));

    // The bar it moved into was already the tightest row on the page, and this
    // is the narrowest width the stylesheet has a rule for. The pill gives up
    // its word there and keeps its 44px - what makes it findable by a thumb is
    // the ring - and nothing may be pushed past the edge to make room for it.
    await s.page.setViewportSize({ width: 320, height: 844 });
    await wait(400);
    const tight = await s.page.evaluate(() => {
      const bar = document.querySelector('.topbar').getBoundingClientRect();
      const parts = [...document.querySelectorAll('.topbar > *')]
        .map((n) => n.getBoundingClientRect())
        .filter((box) => box.width > 0 && box.height > 0);
      const pill = document.getElementById('micBtn').getBoundingClientRect();
      return {
        over: Math.round(Math.max(0, Math.max(...parts.map((b) => b.right)) - bar.right)),
        doc: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        pill: { w: Math.round(pill.width), h: Math.round(pill.height) },
        word: getComputedStyle(document.getElementById('micText')).display,
      };
    });
    ok(tight.over === 0 && tight.doc <= 1,
      'at 320px every control is still inside the bar', JSON.stringify(tight));
    ok(tight.pill.w === 44 && tight.pill.h >= 44 && tight.word === 'none',
      'and the microphone gave up its word rather than its size', JSON.stringify(tight.pill));
    await s.page.setViewportSize({ width: 390, height: 844 });
    await wait(400);

    // Recording: a sheet with the level it is hearing and the two things that
    // can happen to a recording, both of them large.
    await s.page.click('#micBtn');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    await wait(1300);
    const rec = await micRow(s.page);
    ok(rec.open && rec.keep && rec.drop,
      'the pill opens a sheet with both endings on it', JSON.stringify(rec));
    ok(rec.labels === 'Send,Cancel', 'and each of them says which it is', rec.labels);
    ok(rec.keep.h >= 56 && rec.drop.h >= 56,
      'both are big enough to be hit without looking',
      rec.keep.h + 'px / ' + rec.drop.h + 'px');
    ok(rec.bars >= 1, 'the sheet shows what the microphone is hearing', rec.bars + ' bar(s)');
    ok(/^\d+:\d\d$/.test(rec.clock),
      'the clock says how long it has been listening', rec.clock);
    ok(rec.pillDisabled, 'and the pill cannot start a second one', String(rec.pillDisabled));
    await shot(s.page, 'dictate-recording');
    const fits = await s.page.evaluate(() => {
      const bar = document.querySelector('.topbar').getBoundingClientRect();
      const middle = (box) => (box.top + box.bottom) / 2;
      // Only what is actually on screen: a control the bar has hidden lays
      // out to nothing, and nothing is not misaligned. Copy, paste and upload
      // float under the bar on a phone, on purpose (transfer.js).
      const parts = [...document.querySelectorAll('.topbar > :not(#transfer)')]
        .map((n) => n.getBoundingClientRect())
        .filter((box) => box.width > 0 && box.height > 0);
      const buttons = [...document.querySelectorAll('#recSheet .rec-big')]
        .map((n) => n.getBoundingClientRect());
      return {
        aligned: parts.every((box) => Math.abs(middle(box) - middle(bar)) <= 2),
        inside: parts.every((box) => box.left >= bar.left - 1 && box.right <= bar.right + 1),
        buttonsInside: buttons.every((box) => box.left >= 0 && box.right <= window.innerWidth + 1),
      };
    });
    ok(fits.aligned && fits.inside && fits.buttonsInside,
      'the bar still fits on a phone, and so does the sheet', JSON.stringify(fits));

    // Discard: the audio is thrown away. Nothing is transcribed, and nothing
    // reaches the pane.
    await s.page.click('#recCancel');
    await s.page.waitForSelector('#recSheet:not([open])', { state: 'attached', timeout: 10000 });
    await wait(800);
    const after = await micRow(s.page);
    ok(!after.open && !after.pillDisabled, 'Cancel closes it and gives the microphone back',
      JSON.stringify(after));
    ok(transcribes.length === 0, 'and it costs nothing: nothing was transcribed',
      transcribes.length + ' transcribe(s)');
    ok(!(await packed(s.page)).includes(stub.text.replace(/\s+/g, '')),
      'nothing reached the pane', oneLine(await screen(s.page)).slice(-60));

    // Send: transcribed once, and the words are put on the prompt verbatim.
    await s.page.click('#micBtn');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    await wait(1300);
    await s.page.click('#recSend');
    const landed = await awaitPacked(s.page, stub.text, 60000);
    ok(landed, 'Send types what was heard into the pane, verbatim and unconfirmed',
      oneLine(await screen(s.page)).slice(-90));
    ok(transcribes.length === 1, 'and it was transcribed exactly once',
      transcribes.length + ' request(s)');

    // And it stops there. The words are on the prompt, not run: the shell has
    // not echoed anything, because nothing pressed Enter for it.
    await wait(1500);
    const before = await packed(s.page);
    ok(before.split('dictated-into-the-pane').length === 2,
      'the line is on the prompt once and was not run', oneLine(await screen(s.page)).slice(-90));

    // The Enter is the person's, and it is the ordinary keyboard that sends it.
    // The focus is already in the pane, because that is where the words went.
    await s.page.keyboard.press('Enter');
    let ran = false;
    for (const started = Date.now(); !ran && Date.now() - started < 30000;) {
      ran = (await packed(s.page)).split('dictated-into-the-pane').length > 2;
      if (!ran) await wait(200);
    }
    ok(ran, 'and pressing it runs the line that was dictated, and the shell answers',
      oneLine(await screen(s.page)).slice(-90));

    await shot(s.page, 'dictate');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

/* -------------------------------------------------------- 37. no-overlap */

// Speech-to-text and text-to-speech are one device talking to itself, so they
// never overlap: while a microphone is open nothing is read out loud, and a
// recording that starts silences whatever is being read.
async function nooverlap() {
  const stub = await openRouterStub({ text: 'The tests passed and it is waiting.' });
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
    await pointGateway(s, stub);
    const said = await stubSpeech(s.context, { seconds: 1, delay: 1200 });
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });

    // A recording is running, and Status is pressed: the answer arrives on
    // the screen, and it is not read into the microphone.
    await s.page.click('#micBtn');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    // The recording sheet is modal, so Status is pressed through the page
    // rather than through the pointer the backdrop is holding.
    await s.page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 15000 });
    await s.page.$eval('#statusBtn', (n) => n.click());
    const shown = await s.page.waitForFunction(() => {
      const host = document.getElementById('termNotice');
      return !!host && !host.hidden && host.dataset.kind === 'status';
    }, null, { timeout: 45000 }).then(() => true).catch(() => false);
    ok(shown, 'the summary arrived while the microphone was open', oneLine(await noticeText(s.page)));
    await wait(2000);
    ok(said.length === 0, 'and nothing at all was read out loud', said.length + ' render(s)');

    // The recording is thrown away, and the same button says the same thing
    // out loud - so what stopped the voice was the microphone and nothing else.
    await s.page.click('#recCancel');
    await s.page.waitForSelector('#recSheet:not([open])', { state: 'attached', timeout: 10000 });
    await s.page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 20000 });
    await s.page.click('#statusBtn');
    for (let i = 0; i < 120 && said.length === 0; i += 1) await wait(200);
    ok(said.length === 1, 'with the microphone closed it is read out loud',
      said.length + ' render(s)');

    // And the rule itself, both ways round, in the module that owns it: a
    // recording silences what is playing, and speak() while it runs plays
    // nothing rather than queueing it.
    const rule = await s.page.evaluate(async () => {
      const mod = await import('/static/js/voice.js');
      const playing = mod.speak('an answer that is being read out loud right now');
      await new Promise((resolve) => setTimeout(resolve, 300));
      const before = mod.isSpeaking();
      const recorder = new mod.Recorder();
      await recorder.start();
      const after = mod.isSpeaking();
      const again = await mod.speak('this one must not be read out loud at all')
        .then(() => 'resolved').catch((err) => 'threw: ' + err.message);
      const during = mod.isSpeaking();
      await recorder.cancel();
      await playing.catch(() => {});
      return { before, after, again, during };
    });
    ok(rule.before && !rule.after, 'a recording that starts silences what is being read',
      JSON.stringify(rule));
    ok(rule.again === 'resolved' && !rule.during,
      'and asking for speech while it records answers at once, having played nothing',
      JSON.stringify(rule));
    await wait(500);
    ok(said.length === 2, 'the render that was interrupted was the last one asked for',
      said.length + ' render(s)');

    await shot(s.page, 'no-overlap');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

/* ------------------------------------------------- input never dies */

// typeafteroutage is the bug report: "sometimes I can no longer type anything
// into the session". The socket is cut in every way a phone cuts one - the
// radio going, the screen locking - and after each of them the pane has to
// take a keystroke again, with no reload.
async function typeafteroutage() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await recordSockets(s.page);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const first = 'before-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + first);
    ok(await awaitScreen(s.page, first), 'the pane answers before anything is cut',
      oneLine(await screen(s.page)));

    // (a) The radio goes, and comes back.
    await s.context.setOffline(true);
    await s.page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await s.page.waitForFunction(() => document.body.classList.contains('conn-lost'),
      null, { timeout: 20000 });

    // (e) A burst typed while there is no socket, one character at a time,
    // which is what a person does the moment they notice nothing is happening.
    // Every byte of it has to arrive, in the order it was typed.
    const burst = 'burst' + Math.random().toString(36).slice(2, 6);
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    await s.page.keyboard.type('printf "%s\\n" ' + burst, { delay: 8 });
    await s.page.keyboard.press('Enter');

    await s.context.setOffline(false);
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    const back = await s.page.waitForFunction(() => !document.body.classList.contains('conn-lost'),
      null, { timeout: 25000 }).then(() => true).catch(() => false);
    ok(back, 'the socket came back on its own', back ? 'conn-lost cleared' : 'still lost');

    const said = () => s.page.evaluate(() => window.__toasts.slice());
    const arrived = await awaitScreen(s.page, burst, 25000);
    ok(arrived || (await said()).some((t) => /may not have been delivered/.test(t)),
      'what was typed in the outage arrived in order, or was said to be lost',
      arrived ? burst : JSON.stringify(await said()));

    const after = 'after-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + after);
    ok(await awaitScreen(s.page, after, 25000), 'and the pane takes a keystroke again after the outage',
      oneLine(await screen(s.page)));

    // (b) The phone is locked and unlocked: no network event at all, only the
    // page being hidden and shown again.
    await s.context.setOffline(true);
    await s.page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await wait(1500);
    await s.context.setOffline(false);
    await s.page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
      document.dispatchEvent(new Event('visibilitychange'));
      window.dispatchEvent(new Event('pageshow'));
    });

    const woke = 'woke-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + woke);
    ok(await awaitScreen(s.page, woke, 30000), 'and after a lock and an unlock, without a reload',
      oneLine(await screen(s.page)));

    await shot(s.page, 'typeafteroutage');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// typekeepsfocus is the other half of the same complaint, and it has nothing to
// do with the network: a dialog, an overflow menu or a session being switched
// leaves the focus somewhere that is not the terminal, and every key after that
// is swallowed in silence. The page always gives the focus back.
async function typekeepsfocus() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const one = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const mark = Math.random().toString(36).slice(2, 6);

    await typeLine(s.page, 'echo one-' + mark);
    ok(await awaitScreen(s.page, 'one-' + mark), 'the first session answers', oneLine(await screen(s.page)));

    // (c) The rename dialog, opened and cancelled. Nothing is clicked on the
    // pane afterwards: the keys go straight to wherever the page left the
    // focus, which is exactly what a person does.
    await s.page.click('#sessionTitle');
    await s.page.waitForSelector('dialog.modal[open]', { timeout: 10000 });
    await s.page.click('dialog.modal .btn.sm:not(.primary)');
    await s.page.waitForSelector('dialog.modal[open]', { state: 'detached', timeout: 10000 });
    const dialogMark = 'dialog-' + mark;
    await s.page.keyboard.type('echo ' + dialogMark);
    await s.page.keyboard.press('Enter');
    ok(await awaitScreen(s.page, dialogMark, 15000), 'typing works after a dialog was opened and closed',
      oneLine(await screen(s.page)));

    // And the ⋮ menu: two of its switches pressed - the focus is on the
    // switch now, inside a menu that stays open - and then dismissed the way
    // a stray tap dismisses it. Both presses are the key bar's, so the
    // device ends where it started.
    await option(s.page, 'keybar');
    await option(s.page, 'keybar');
    await s.page.click('#term .xterm-screen');
    await s.page.waitForSelector('.menu', { state: 'detached', timeout: 10000 });
    const menuMark = 'menu-' + mark;
    await s.page.keyboard.type('echo ' + menuMark);
    await s.page.keyboard.press('Enter');
    ok(await awaitScreen(s.page, menuMark, 15000), 'and after the ⋯ menu was opened and closed',
      oneLine(await screen(s.page)));

    // (d) Two sessions, and a keystroke into each of them.
    const two = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await typeLine(s.page, 'echo two-' + mark);
    ok(await awaitScreen(s.page, 'two-' + mark, 20000), 'the second session takes a keystroke',
      oneLine(await screen(s.page)));

    await s.page.click('.chat-item[data-id="' + one + '"]');
    await s.page.waitForFunction((id) => location.hash.slice(1) === id, one, { timeout: 15000 });
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    // The pane really is the other session's, which is what makes the
    // keystroke below an assertion about the session that was switched to.
    ok(await awaitScreen(s.page, 'one-' + mark, 25000), 'the pane is the first session again',
      oneLine(await screen(s.page)));
    const backOne = 'back-one-' + mark;
    await typeLine(s.page, 'echo ' + backOne);
    ok(await awaitScreen(s.page, backOne, 25000), 'and the first one still does after switching back',
      oneLine(await screen(s.page)));

    await s.page.click('.chat-item[data-id="' + two + '"]');
    await s.page.waitForFunction((id) => location.hash.slice(1) === id, two, { timeout: 15000 });
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    ok(await awaitScreen(s.page, 'two-' + mark, 25000), 'and the second session\u2019s pane after switching again',
      oneLine(await screen(s.page)));
    const backTwo = 'back-two-' + mark;
    await typeLine(s.page, 'echo ' + backTwo);
    ok(await awaitScreen(s.page, backTwo, 25000), 'and so does the second, after switching again',
      oneLine(await screen(s.page)));

    await shot(s.page, 'typekeepsfocus');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ----------------------------------------------------------- 41. folders */

// The sidebar is read by project - one folder per directory sessions work in,
// "More Chats" at the bottom for the ones Socrates housed itself, and a folder
// a person can shut and find shut again tomorrow.
//
// A list with history in it cannot be waited for, so it is made: the moment a
// session was last used is written straight into the store.

// backdate rewrites when a session was last used.
//
// There is no API for it and there should not be: `active_at` is the store's
// own record of when a session's status last changed, not something a client
// gets to choose. So the scenario writes the three clocks itself, with node's
// own SQLite against the same file the server has open, which is the smallest
// thing that can produce a folder that was worked in last week.
function backdate(s, moments) {
  const db = new DatabaseSync(join(s.data, 'socrates.db'), { timeout: 10000 });
  try {
    const set = db.prepare('UPDATE sessions SET active_at = ?, updated_at = ?, created_at = ? WHERE id = ?');
    for (const [id, at] of Object.entries(moments)) {
      const done = set.run(at, at, at, id);
      if (Number(done.changes) !== 1) throw new Error('no session ' + id + ' to backdate');
    }
  } finally { db.close(); }
}

// listing is what the sidebar is actually showing: every folder in order, with
// the rows under it and whatever marker each was stamped with.
const listing = (page) => page.evaluate(() => [...document.getElementById('sessionList').children]
  .map((node) => ({
    folder: node.dataset.folder || '',
    name: (node.querySelector('.folder-name') || {}).textContent || '',
    title: (node.querySelector('.folder-head') || {}).title || '',
    open: (node.querySelector('.folder-head') || {}).getAttribute
      ? node.querySelector('.folder-head').getAttribute('aria-expanded') : '',
    count: (node.querySelector('.folder-count') || {}).textContent || '',
    shown: !!node.querySelector('.folder-rows') && node.querySelector('.folder-rows').checkVisibility(),
    stamp: node.dataset.stamp || '',
    rows: [...node.querySelectorAll('.chat-item')].map((row) => ({
      id: row.dataset.id,
      row: row.querySelector('.label').textContent,
      done: row.classList.contains('done'),
      stamp: row.dataset.stamp || '',
      seen: row.checkVisibility(),
    })),
    other: node.classList.contains('folder') ? '' : node.className || node.tagName,
  })));

// under is which folder each row is in, by the name the row reads.
function under(shown) {
  const out = new Map();
  for (const folder of shown) for (const row of folder.rows) out.set(row.row, folder.name);
  return out;
}

// stampAll marks every folder and row on screen, so the next render can be
// asked whether it kept them or built new ones.
const stampAll = (page) => page.evaluate(() => {
  for (const folder of document.getElementById('sessionList').children) {
    folder.dataset.stamp = folder.dataset.folder || 'other';
    for (const row of folder.querySelectorAll('.chat-item')) row.dataset.stamp = row.dataset.id;
  }
});

async function folders() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    // The grouping first, on its own. Which folder a session belongs in, what
    // that folder is called and where it sits are a pure function in a module
    // of its own, so they can be asked directly - and they have to be,
    // because two projects with the same name in different places is the case
    // the naming exists for and not one a running server can be made to have
    // cheaply.
    const pure = await s.page.evaluate(async () => {
      const mod = await import('/static/js/folders.js');
      const at = (n) => ({ active_at: n });
      const dirs = (list) => mod.foldersOf(list).map((f) => f.label + '(' + f.sessions.length + ')').join(' | ');
      return {
        keys: [
          ['a project', { workdir: '/root/browser', workdir_mode: 'preset' }],
          ['the same one with a trailing slash', { workdir: '/root/browser/', workdir_mode: 'custom' }],
          ['a directory Socrates made up', { workdir: '/data/workspaces/claude-1', workdir_mode: 'dynamic' }],
          ['a row from a server that never said', { workdir: '/root/browser' }],
          ['a row with no directory at all', { workdir: '' }],
        ].map(([what, row]) => what + ': ' + mod.folderKeyOf(row)).join(' | '),
        order: dirs([
          { ...at(30), workdir: '/root/old', workdir_mode: 'preset' },
          { ...at(90), workdir: '/data/w/x', workdir_mode: 'dynamic' },
          { ...at(50), workdir: '/root/new', workdir_mode: 'preset' },
          { ...at(70), workdir: '/root/new', workdir_mode: 'preset' },
        ]),
        clash: dirs([
          { ...at(9), workdir: '/srv/one/web', workdir_mode: 'preset' },
          { ...at(8), workdir: '/srv/two/web', workdir_mode: 'preset' },
          { ...at(7), workdir: '/srv/one/api', workdir_mode: 'preset' },
        ]),
        inside: mod.foldersOf([
          { active_at: 10, id: 'old', workdir: '/root/browser', workdir_mode: 'preset' },
          { active_at: 30, id: 'new', workdir: '/root/browser', workdir_mode: 'preset' },
          { active_at: 20, id: 'mid', workdir: '/root/browser', workdir_mode: 'preset' },
        ])[0].sessions.map((one) => one.id).join(','),
        moment: mod.momentOf({ active_at: 9, updated_at: 7, created_at: 3 }) + ','
          + mod.momentOf({ updated_at: 7, created_at: 3 }) + ',' + mod.momentOf({ created_at: 3 }),
        moves: [['idle', 'busy'], ['busy', 'idle'], ['busy', 'waiting'], ['idle', 'waiting'],
          ['unknown', 'idle'], ['waiting', 'idle'], ['busy', 'busy'], ['', 'idle']]
          .map(([from, to]) => (from || 'nothing') + '>' + to + '=' + mod.movesRow(from, to)).join(' '),
      };
    });
    ok(pure.keys === 'a project: /root/browser | the same one with a trailing slash: /root/browser'
      + ' | a directory Socrates made up: #more | a row from a server that never said: /root/browser'
      + ' | a row with no directory at all: #more',
      'a session belongs to the directory it works in, and one of Socrates’ own making to "More Chats"',
      pure.keys);
    ok(pure.order === 'new(2) | old(1) | More Chats(1)',
      'the folders are ordered by the session that moved most recently, and "More Chats" is last whatever is in it',
      pure.order);
    ok(pure.clash === 'one/web(1) | two/web(1) | api(1)',
      'a folder is named after its directory, and takes a parent when that would not tell it from another',
      pure.clash);
    ok(pure.inside === 'new,mid,old', 'inside a folder the sessions are newest first', pure.inside);
    ok(pure.moment === '9,7,3',
      'a session is ordered by its last status change, by its last write from an older server, else by its creation',
      pure.moment);
    ok(pure.moves === 'idle>busy=true busy>idle=true busy>waiting=true idle>waiting=true'
      + ' unknown>idle=false waiting>idle=false busy>busy=false nothing>idle=false',
      'starting, finishing and needing an answer move a row; a detector catching up does not', pure.moves);

    // Now the real thing: three projects and two sessions with no project at
    // all. A typed-in directory is a decision the dashboard has to allow, so
    // it is allowed the way a person would allow it.
    const allowed = await s.context.request.put(s.url + '/api/settings',
      { data: { settings: { workspace: { allow_custom: true } } } });
    ok(allowed.ok(), 'a typed-in working directory is allowed on this Socrates', allowed.status());

    const projects = {
      browser: join(s.data, 'projects', 'browser'),
      socrates: join(s.data, 'projects', 'socrates'),
      // The same name in another place, which is what makes a bare name a lie.
      other: join(s.data, 'elsewhere', 'browser'),
    };
    for (const dir of Object.values(projects)) mkdirSync(dir, { recursive: true });

    const create = async (title, workdir, harness = 'shell') => {
      const made = await s.context.request.post(s.url + '/api/sessions', {
        data: workdir
          ? { harness, title, workdir_mode: 'custom', workdir }
          : { harness, title },
      });
      if (!made.ok()) throw new Error('could not create ' + title + ': ' + made.status());
      return (await made.json()).session.id;
    };
    // Oldest first, so that the order the server lists them in is the order
    // the folders are asserted in below.
    const ids = {
      socrates: await create('Socrates one', projects.socrates),
      otherBrowser: await create('The other browser', projects.other),
      browserOld: await create('Browser one', projects.browser),
      loose: await create('No project one'),
      browserNew: await create('Browser two', projects.browser),
      looseNew: await create('No project two'),
    };
    // A week apart, so that the order of the folders is a fact about when each
    // project was last worked in rather than about the second they were made.
    const day = 24 * 3600 * 1000;
    const now = Date.now();
    backdate(s, {
      [ids.socrates]: now - 9 * day,
      [ids.otherBrowser]: now - 7 * day,
      [ids.browserOld]: now - 5 * day,
      [ids.loose]: now - 4 * day,
      [ids.browserNew]: now - 3 * day,
      [ids.looseNew]: now - 2 * day,
    });

    // The oldest project's one session is done. It stays in its project, on
    // the project's Done shelf, and the project - with no work in progress
    // left - goes under the ones that have some.
    const away = await s.context.request.post(s.url + '/api/sessions/' + ids.socrates + '/file',
      { data: { status: 'done' } });
    ok(away.ok(), 'the oldest project’s one session is done', away.status());
    backdate(s, { [ids.socrates]: now - 9 * day });

    await open(s);
    await s.page.waitForFunction((n) => document.querySelectorAll('#sessionList .chat-item').length === n,
      6, { timeout: 20000 });

    const active = await listing(s.page);
    // Two of the three projects are called `browser`, so both of them take a
    // parent: a name that is a name for one of them and a lie for the other
    // would be worse than the path either of them came from.
    ok(active.map((f) => f.name).join(' | ') === 'projects/browser | elsewhere/browser | socrates | More Chats',
      'the list is folders: the project worked in most recently first, one with only finished work after them, and "More Chats" last',
      active.map((f) => f.name).join(' | '));
    ok(active.every((f) => !f.other), 'and the list holds nothing but folders',
      active.filter((f) => f.other).map((f) => f.other).join(' | ') || 'folders');
    ok(active[0].title === projects.browser && active[3].title === 'Sessions in a directory of their own',
      'the whole path is on the folder rather than in it, and "More Chats" says what it holds instead',
      active[0].title + ' | ' + active[3].title);
    ok(!active.some((f) => f.name.startsWith('/')),
      'no folder reads as a path', active.map((f) => f.name).join(' | '));

    const where = under(active);
    ok(where.get('Browser two') === 'projects/browser' && where.get('Browser one') === 'projects/browser'
      && where.get('No project one') === 'More Chats' && where.get('No project two') === 'More Chats'
      && where.get('The other browser') === 'elsewhere/browser',
      'every session is in the directory it works in, and the ones with a directory of their own are together',
      [...where].map(([title, folder]) => folder + ': ' + title).join(' | '));
    ok(active[0].rows.map((r) => r.row).join(',') === 'Browser two,Browser one',
      'and inside a folder the newest is first', active[0].rows.map((r) => r.row).join(','));

    // The Done shelf starts shut. Opening it shows the done session, and
    // everything that was on screen is still the element it was.
    const doneShelf = '#sessionList .folder[data-folder="' + projects.socrates + '"] .shelf[data-shelf="done"] .folder-head';
    const shelfState = () => s.page.$eval(doneShelf, (head) => ({
      open: head.getAttribute('aria-expanded'),
      shown: head.closest('.shelf').querySelector('.folder-rows').checkVisibility(),
      count: head.querySelector('.folder-count').textContent,
    }));
    const shutShelf = await shelfState();
    ok(shutShelf.open === 'false' && !shutShelf.shown && shutShelf.count === '1',
      'the Done shelf of a folder starts shut, and says how many are on it', JSON.stringify(shutShelf));
    const before = new Set(active.flatMap((f) => [f.folder, ...f.rows.map((r) => r.id)]));
    await stampAll(s.page);
    await s.page.click(doneShelf);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'true', doneShelf, { timeout: 5000 });
    const all = await listing(s.page);
    const doneRow = all.flatMap((f) => f.rows).find((r) => r.id === ids.socrates);
    ok(!!doneRow && doneRow.done && doneRow.seen && under(all).get('Socrates one') === 'socrates',
      'a done session stays in its project, on the project’s Done shelf',
      doneRow ? under(all).get('Socrates one') + (doneRow.done ? ' (done)' : '') : 'no row');
    const kept = all.filter((f) => before.has(f.folder) && f.stamp).length
      + all.flatMap((f) => f.rows).filter((r) => before.has(r.id) && r.stamp).length;
    ok(kept === before.size,
      'and every folder and row that was already there survived the re-render',
      kept + ' of ' + before.size + ' elements kept');

    // Shutting a folder. The rows go, the count says how many went, and the
    // caret turns rather than the words changing.
    const browserHead = '#sessionList .folder[data-folder="' + projects.browser + '"] .folder-head';
    const caretBefore = await s.page.$eval(browserHead + ' .caret', (n) => getComputedStyle(n).transform);
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'false', browserHead, { timeout: 5000 });
    const shut = await listing(s.page);
    // The caret turns rather than jumps, so it is waited for: what is asserted
    // is where it ends up, not where it was a frame after the click.
    await s.page.waitForFunction((sel) => getComputedStyle(document.querySelector(sel + ' .caret'))
      .transform === 'none', browserHead, { timeout: 5000 });
    const caretAfter = await s.page.$eval(browserHead + ' .caret', (n) => getComputedStyle(n).transform);
    ok(shut[0].open === 'false' && !shut[0].shown && shut[0].rows.every((r) => !r.seen),
      'a folder that is shut hides its sessions and says so to a reader that cannot see it',
      shut[0].open + ', rows shown: ' + shut[0].rows.filter((r) => r.seen).length);
    ok(shut[0].count === '2' && caretBefore !== caretAfter,
      'and says how many are behind it, with the caret turned', shut[0].count + ', ' + caretAfter);
    ok(shut.slice(1).every((f) => f.shown), 'the other folders are untouched',
      shut.filter((f) => f.shown).map((f) => f.name).join(' | '));
    const visibleCount = await s.page.$$eval('#sessionList .folder-count',
      (nodes) => nodes.filter((n) => n.checkVisibility()).length);
    ok(visibleCount === 1, 'an open folder counts nothing: the rows under it are the count', visibleCount);
    await shot(s.page, 'folders-shut');

    // It is a fact about this device, so a reload finds it the way it was
    // left - and the sessions in it are still there, not gone.
    await stampAll(s.page);
    await open(s);
    await s.page.waitForFunction((n) => document.querySelectorAll('#sessionList .chat-item').length === n,
      6, { timeout: 20000 });
    const reloaded = await listing(s.page);
    ok(reloaded[0].name === 'projects/browser' && reloaded[0].open === 'false' && !reloaded[0].shown
      && reloaded[0].rows.length === 2,
      'a reload finds the folder shut, with its sessions still in it',
      reloaded[0].name + ': ' + reloaded[0].open + ', ' + reloaded[0].rows.length + ' rows');

    // Opening the session that is inside it is what opens the folder: a row
    // nobody can see is a session nobody can get back to.
    await s.page.goto(s.url + '/#' + ids.browserNew, { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await s.page.waitForFunction((id) => {
      const row = document.querySelector('.chat-item[data-id="' + id + '"]');
      return !!row && row.classList.contains('active') && row.checkVisibility();
    }, ids.browserNew, { timeout: 15000 });
    const opened = await listing(s.page);
    ok(opened[0].open === 'true' && opened[0].rows.every((r) => r.seen),
      'attaching a session inside a shut folder opens it', opened[0].name + ': ' + opened[0].open);

    // And shutting it again is one click, from the state the page is in.
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'false', browserHead, { timeout: 5000 });
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'true', browserHead, { timeout: 5000 });
    ok(true, 'and the same head shuts and opens it again', 'twice');

    // A session started in a directory nothing has used yet makes the folder
    // for it, at the top, because it is the newest thing that happened.
    const fresh = join(s.data, 'projects', 'brandnew');
    mkdirSync(fresh, { recursive: true });
    const freshId = await create('Brand new', fresh);
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((id) => !!document.querySelector('.chat-item[data-id="' + id + '"]'),
      freshId, { timeout: 20000 });
    const made = await listing(s.page);
    ok(made[0].name === 'brandnew' && made[0].open === 'true' && made[0].rows.length === 1,
      'the first session in a new directory makes its folder, open, at the top',
      made[0].name + ': ' + made[0].open);

    // A folder that is shut must not be able to hide the two things this page
    // exists to make noticeable. A session in the project is given something
    // to do, the page steps off it, and the folder is shut over the top of it
    // - and the head has to say both that something is running in there and,
    // when it ends, that nobody has seen it.
    //
    // It takes an agent session, because a shell is one of the two programs
    // nothing is asked of: its row carries no status, so there would be
    // nothing for a shut folder to roll up.
    const agent = await create('Browser agent', projects.browser, 'claude');
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((id) => !!document.querySelector('.chat-item[data-id="' + id + '"]'),
      agent, { timeout: 20000 });
    await clickRow(s.page, agent);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const ready = await awaitRow(s.page, agent, (r) => !r.busy, 20000);
    ok(ready.ok, 'the agent session in the project settles before it is given anything', took(ready));
    await typeLine(s.page, '/busy 6000');
    const busyRow = await awaitRow(s.page, agent, (r) => r.busy, 10000);
    ok(busyRow.ok, 'the session in the project is working', took(busyRow));
    await clickRow(s.page, ids.looseNew);
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'false', browserHead, { timeout: 5000 });
    const rollup = (page) => page.evaluate((sel) => {
      const head = document.querySelector(sel);
      const dot = head.querySelector('.folder-dot');
      return {
        busy: head.classList.contains('busy'),
        waiting: head.classList.contains('waiting'),
        unread: head.classList.contains('unread'),
        dot: dot.checkVisibility(),
        count: head.querySelector('.folder-count').textContent,
      };
    }, browserHead);
    const working = await rollup(s.page);
    ok(working.busy && working.dot && !working.unread && working.count === '3',
      'a shut folder says that something inside it is working', JSON.stringify(working));
    const started = Date.now();
    let finished = working;
    while (Date.now() - started < 30000) {
      finished = await rollup(s.page);
      if (finished.unread && !finished.busy) break;
      await wait(300);
    }
    ok(finished.unread && !finished.busy,
      'and when the turn ends with nobody looking, that nobody has seen it',
      JSON.stringify(finished) + ' after ' + ((Date.now() - started) / 1000).toFixed(1) + ' s');
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'true', browserHead, { timeout: 5000 });

    // The head is a label, not a band: the surface under it is the page's one
    // white, and a hairline is what separates the folders.
    const style = await s.page.evaluate(() => [...document.querySelectorAll('#sessionList > .folder > .folder-bar')]
      .map((node) => {
        const css = getComputedStyle(node.querySelector('.folder-head'));
        return { fill: css.backgroundColor, top: getComputedStyle(node).borderTopWidth, size: css.fontSize };
      }));
    ok(style.length > 1 && style.every((one) => one.fill === 'rgba(0, 0, 0, 0)')
      && style[0].top === '0px' && style.slice(1).every((one) => one.top === '1px'),
      'a head is a quiet label on white, hairline-separated below the first',
      JSON.stringify(style));

    await shot(s.page, 'folders');

    // On a phone the sidebar is a drawer, and a folder in it is tapped rather
    // than clicked: the head has to be a target a thumb can hit, and the whole
    // of it has to be inside the drawer.
    await s.page.setViewportSize({ width: 390, height: 844 });
    // The sidebar only becomes a drawer once the narrow layout has been
    // applied, and asking for the drawer before that would find the column
    // already where it belongs and never open anything.
    await wait(400);
    await ensureNav(s.page);
    // The drawer slides in over 220 ms, and a control that is still moving is
    // one Playwright refuses to click.
    await wait(600);
    const thumb = await s.page.evaluate((sel) => {
      const box = document.querySelector(sel).getBoundingClientRect();
      return {
        height: Math.round(box.height),
        width: Math.round(box.width),
        // Against the window, not against the list: a drawer that never came
        // out holds a head that is perfectly placed inside a list nobody can
        // see.
        inside: box.left >= 0 && box.right <= window.innerWidth,
      };
    }, browserHead);
    ok(thumb.height >= 30 && thumb.width >= 200 && thumb.inside,
      'on a phone a folder head is a target a thumb can hit', JSON.stringify(thumb));
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'false', browserHead, { timeout: 5000 });
    await shot(s.page, 'folders-phone');

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* -------------------------------------------------------- 46. clipboard */

// Copy and paste, which is the one thing a terminal in a browser has to
// borrow from the browser.
//
// The defect this scenario exists for: xterm.js turns Ctrl and a letter into
// that letter's control code with no exception for V, so Ctrl-V reached the
// pane as 0x16 - which Claude Code has bound to "paste an image from the
// clipboard" - and the text never left the browser at all, because a keydown
// xterm has cancelled is a paste the browser never performs. What was pasted
// was "No image found in clipboard".
//
// So: 0x16 must not be sent, the text must arrive, a paste must be bracketed
// when the program asked for bracketing, Ctrl-C over a selection must copy,
// and Ctrl-C over nothing must still be the interrupt it has always been.
async function clipboard() {
  const s = await start({
    viewport: { width: 1280, height: 720 },
    // The clipboard is the assertion, so the page is allowed to read it back.
    // Nothing in the app reads it on this path - the browser's own paste is
    // what carries the text - but the scenario has to see what was copied.
    permissions: ['clipboard-read', 'clipboard-write'],
  });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await recordInput(s.page);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);

    // Every byte the pane was sent since a moment ago, one hex pair per byte.
    const since = async (mark) => (await sentBytes(s.page)).slice(mark)
      .flatMap((frame) => frame.match(/../g) || []);
    const mark = async () => (await sentBytes(s.page)).length;
    const hexOf = (text) => Buffer.from(text, 'utf8').toString('hex').match(/../g);
    const has = (bytes, want) => bytes.join(',').includes(want.join(','));

    const line = 'paste-' + Math.random().toString(36).slice(2, 8);
    await s.page.evaluate((text) => navigator.clipboard.writeText(text), 'echo ' + line);

    const beforePaste = await mark();
    await s.page.keyboard.press('Control+V');
    await wait(400);
    const pasted = await since(beforePaste);
    ok(!pasted.includes('16'), 'Ctrl-V does not send 0x16 to the pane',
      pasted.join(',') || 'nothing');
    ok(has(pasted, hexOf('echo ' + line)), 'it sends the clipboard instead',
      pasted.join(',') || 'nothing');
    await s.page.keyboard.press('Enter');
    ok(await awaitScreen(s.page, line, 20000), 'and the shell runs the line that was pasted',
      oneLine(await screen(s.page)).slice(-60));

    // Bracketed paste is the program's own request, and it is the shell that
    // makes it here: with ?2004h set, what arrives has to be wrapped, which is
    // what stops an editor from auto-indenting every line of a paste.
    await typeLine(s.page, "printf '\\033[?2004h'");
    await wait(500);
    await s.page.evaluate(() => navigator.clipboard.writeText('bracketed'));
    const beforeBracket = await mark();
    await s.page.keyboard.press('Control+V');
    await wait(400);
    const bracketed = await since(beforeBracket);
    ok(has(bracketed, hexOf('\x1b[200~bracketed\x1b[201~')),
      'a paste is bracketed when the program asked to be told that a paste is a paste',
      bracketed.join(',') || 'nothing');
    await s.page.keyboard.press('Control+U');
    await typeLine(s.page, "printf '\\033[?2004l'");
    await wait(400);

    // The other direction. xterm 6 keeps its selection in its own model and
    // never puts one in the DOM, so the browser's copy event carries nothing:
    // without this the pane could not be copied out of at all.
    const said = 'copy-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + said);
    ok(await awaitScreen(s.page, said, 20000), 'a line to copy is on the screen',
      oneLine(await screen(s.page)).slice(-60));
    // Selected the way a person selects it: the pointer dragged across the row
    // the words are on, with Shift held - tmux tracks the mouse, so a plain
    // drag is a mouse report and Shift is what takes the pointer back from
    // the program. (A plain drag is not lost either: it puts tmux into its own
    // copy mode, and what tmux copies reaches this clipboard over OSC 52.)
    const box = await s.page.evaluate((want) => {
      const rows = document.querySelector('#term .xterm-rows');
      for (const row of rows.children) {
        const text = row.innerText.replace(/ /g, ' ');
        if (text.includes(want) && !text.includes('echo ' + want)) {
          const r = row.getBoundingClientRect();
          return { x: r.left, y: r.top + r.height / 2, right: r.right };
        }
      }
      return null;
    }, said);
    ok(!!box, 'the row it printed on is where the pointer will go',
      box ? Math.round(box.x) + ',' + Math.round(box.y) : 'not found');
    await s.page.keyboard.down('Shift');
    await s.page.mouse.move(box.x + 2, box.y);
    await s.page.mouse.down();
    await s.page.mouse.move(box.right - 2, box.y, { steps: 12 });
    await s.page.mouse.up();
    await s.page.keyboard.up('Shift');
    await wait(200);
    const selected = await s.page.evaluate(() => document
      .querySelectorAll('#term .xterm-selection div').length > 0);
    ok(selected, 'a drag across the pane selects it', selected ? 'selected' : 'no selection');

    const beforeCopy = await mark();
    await s.page.keyboard.press('Control+C');
    await wait(300);
    const onCopy = await since(beforeCopy);
    ok(!onCopy.includes('03'), 'Ctrl-C over a selection does not interrupt the program',
      onCopy.join(',') || 'nothing');
    const held = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(held.includes(said), 'it copies the selection to the clipboard',
      JSON.stringify(oneLine(held)));
    const stillSelected = await s.page.evaluate(() => document
      .querySelectorAll('#term .xterm-selection div').length > 0);
    ok(!stillSelected, 'and the selection is spent, so the next Ctrl-C is a Ctrl-C again',
      stillSelected ? 'still selected' : 'cleared');

    // Which is the assertion that matters most: an interrupt that a stray
    // selection could swallow is a terminal nobody can stop a program in.
    const beforeIntr = await mark();
    await s.page.keyboard.press('Control+C');
    await wait(300);
    const onIntr = await since(beforeIntr);
    ok(onIntr.includes('03'), 'Ctrl-C with nothing selected still sends 0x03',
      onIntr.join(',') || 'nothing');

    // And the key bar's own Paste button, which is the only way in on a phone.
    await s.page.evaluate(() => navigator.clipboard.writeText('from-the-bar'));
    ok(await option(s.page, 'keybar'), 'the options menu puts the key bar up', 'on');
    await closeOptions(s.page);
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 10000 });
    const beforeBar = await mark();
    await s.page.click('#keybar .key[data-key="Paste"]');
    await wait(400);
    const fromBar = await since(beforeBar);
    ok(has(fromBar, hexOf('from-the-bar')), 'the key bar’s Paste button pastes the same text',
      fromBar.join(',') || 'nothing');
    await s.page.keyboard.press('Control+U');


    // The pointer, without Shift. tmux tracks the mouse, so xterm on its own
    // hands a plain drag to tmux as mouse reports - and tmux's copy mode, its
    // buffer and an OSC 52 with no gesture behind it were what a person's copy
    // then depended on. On a Mac there was no Shift to fall back on either.
    // Now a plain drag is a selection in the browser, copied on release; a
    // plain click is still the click a program may be waiting for.
    // rowOf is the row a word was printed on - the output, not the command
    // that printed it - waited for, because a repaint after a resize empties
    // the rows for a frame.
    const rowOf = async (want) => {
      const find = (want) => {
        const rows = document.querySelector('#term .xterm-rows');
        for (const row of rows.children) {
          const text = row.innerText.replace(/\u00a0/g, ' ');
          if (text.includes(want) && !text.includes('echo ' + want)) {
            const r = row.getBoundingClientRect();
            return { x: r.left, y: r.top + r.height / 2, right: r.right };
          }
        }
        return null;
      };
      await s.page.waitForFunction(find, want, { timeout: 5000 }).catch(() => {});
      return s.page.evaluate(find, want);
    };
    const selectedNow = () => s.page.evaluate(() => document
      .querySelectorAll('#term .xterm-selection div').length > 0);
    const REPORT = hexOf('\x1b[<');

    await option(s.page, 'keybar');
    await closeOptions(s.page);
    await wait(300);
    const plain = 'drag-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + plain + ' second-word');
    ok(await awaitScreen(s.page, plain + ' second-word', 20000), 'a line for the pointer is on the screen',
      oneLine(await screen(s.page)).slice(-60));
    await wait(300);
    const row = await rowOf(plain);
    ok(!!row, 'and the row it is on is known', row ? Math.round(row.x) + ',' + Math.round(row.y)
      : 'not found in: ' + JSON.stringify(await s.page.evaluate((want) => [...document.querySelector('#term .xterm-rows').children]
        .map((r) => r.innerText).filter((t) => t.includes(want)), plain)));

    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    const beforeDrag = await mark();
    await s.page.mouse.move(row.x + 2, row.y);
    await s.page.mouse.down();
    await s.page.mouse.move(row.right - 2, row.y, { steps: 12 });
    await s.page.mouse.up();
    await wait(300);
    const duringDrag = await since(beforeDrag);
    ok(!has(duringDrag, REPORT), 'a plain drag - no Shift - sends no mouse report to tmux',
      duringDrag.join(',') || 'nothing');
    ok(await selectedNow(), 'it selects in the browser instead', 'selected');
    const afterDrag = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(afterDrag.includes(plain + ' second-word'),
      'and the selection is on the clipboard the moment the button is released',
      JSON.stringify(oneLine(afterDrag)));

    const beforeClick = await mark();
    await s.page.mouse.click(row.x + 2, row.y);
    await wait(300);
    const onClick = await since(beforeClick);
    ok(has(onClick, hexOf('\x1b[<0;')), 'a plain click is still reported to the program, at its release',
      onClick.join(',') || 'nothing');
    ok(!(await selectedNow()), 'and it clears the selection, as a click does', 'cleared');

    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    await s.page.mouse.dblclick(row.x + 2, row.y);
    await wait(300);
    const word = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(word === plain, 'a double click takes the word under the pointer, and copies it',
      JSON.stringify(oneLine(word)));

    const beforeRight = await mark();
    await s.page.mouse.click(row.x + 2, row.y, { button: 'right' });
    await wait(300);
    const onRight = await since(beforeRight);
    ok(!has(onRight, REPORT), 'a right click is not reported, so tmux opens no menu of its own',
      onRight.join(',') || 'nothing');

    const beforeMiddle = await mark();
    await s.page.mouse.click(row.x + 2, row.y, { button: 'middle' });
    await wait(300);
    const onMiddle = await since(beforeMiddle);
    ok(has(onMiddle, hexOf(plain)), 'a middle click pastes the last thing selected, as it does on X11',
      onMiddle.join(',') || 'nothing');
    // Once, not twice: Chrome on Linux pastes the X11 primary selection on a
    // middle click as well, and that paste is not stopped by preventDefault
    // on the press.
    const pastes = onMiddle.join(',').split(hexOf('\x1b[200~').join(',')).length - 1;
    ok(pastes === 1, 'exactly once, not once from us and once from the browser', String(pastes));
    ok(!has(onMiddle, REPORT), 'and is not reported to tmux, which would paste a buffer of its own',
      onMiddle.join(',') || 'nothing');
    await s.page.keyboard.press('Control+U');

    // The keys with the focus somewhere else on the page - after a click on
    // the sidebar, say. The browser fires paste and copy on whatever has the
    // focus, and unless that is a field of its own they are the pane's.
    await s.page.evaluate(() => document.querySelector('#optionsBtn').focus());
    ok(await s.page.evaluate(() => document.activeElement === document.querySelector('#optionsBtn')),
      'the focus is on a button in the header, not on the terminal', 'the options button');
    const away = 'away-' + Math.random().toString(36).slice(2, 8);
    await s.page.evaluate((text) => navigator.clipboard.writeText(text), away);
    const beforeAway = await mark();
    await s.page.keyboard.press('Control+V');
    await wait(400);
    const onAway = await since(beforeAway);
    ok(has(onAway, hexOf(away)), 'Ctrl-V with the focus elsewhere on the page still pastes into the pane',
      onAway.join(',') || 'nothing');
    ok(await s.page.evaluate(() => document.activeElement === document.querySelector('#term .xterm-helper-textarea')),
      'and the pane has the focus again', 'textarea');
    await s.page.keyboard.press('Control+U');

    await s.page.mouse.move(row.x + 2, row.y);
    await s.page.mouse.down();
    await s.page.mouse.move(row.right - 2, row.y, { steps: 8 });
    await s.page.mouse.up();
    await wait(200);
    await s.page.evaluate(() => document.querySelector('#optionsBtn').focus());
    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    const beforeAwayCopy = await mark();
    await s.page.keyboard.press('Control+C');
    await wait(300);
    const awayCopy = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(awayCopy.includes(plain), 'Ctrl-C with the focus elsewhere copies the pane’s selection',
      JSON.stringify(oneLine(awayCopy)));
    ok(!(await since(beforeAwayCopy)).includes('03'), 'and interrupts nothing',
      (await since(beforeAwayCopy)).join(',') || 'nothing');

    // Two rows, which is how output is actually copied: xterm's `select()`
    // takes a length in cells and wraps it across lines, so the selection has
    // to be the block between the two points and not part of one row. Both
    // rows are looked up again here, because every command typed since has
    // pushed the screen up by one.
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    const second = 'wrap-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + second);
    ok(await awaitScreen(s.page, second, 20000), 'a second row to select down to is on the screen',
      oneLine(await screen(s.page)).slice(-60));
    const upper = await rowOf(plain);
    const lower = await rowOf(second);
    ok(!!upper && !!lower && lower.y > upper.y, 'and the two rows are one above the other',
      upper && lower ? Math.round(upper.y) + ' -> ' + Math.round(lower.y) : 'not found');
    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    await s.page.mouse.move(upper.x + 2, upper.y);
    await s.page.mouse.down();
    await s.page.mouse.move(lower.right - 2, lower.y, { steps: 12 });
    await s.page.mouse.up();
    await wait(300);
    const bothRows = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(bothRows.includes(plain) && bothRows.includes(second),
      'a drag down two rows copies both of them', JSON.stringify(oneLine(bothRows)));

    // What a program copies. OSC 52 is how tmux's own copy mode reaches the
    // clipboard, and how a program inside it does; tmux forwards it with no
    // selection named - `52;;` - which is what the stock addon threw away.
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    const osc = 'osc-' + Math.random().toString(36).slice(2, 8);
    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    await typeLine(s.page, "printf '\\033]52;;" + Buffer.from(osc, 'utf8').toString('base64') + "\\a'");
    await wait(800);
    const fromProgram = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(fromProgram === osc, 'an OSC 52 from the program, through tmux, sets the clipboard',
      JSON.stringify(oneLine(fromProgram)));

    await shot(s.page, 'clipboard');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------- 46b. touchcopy */

// A phone has no pointer to drag and no Ctrl to press, and xterm 6 selects
// with a mouse and nothing else - so there was no way to copy a line out of a
// pane on the device this product is carried on. A finger held still on the
// pane now selects the word under it, grows the selection when it moves on,
// and copies when it lifts, saying so with a toast.
async function touchcopy() {
  const s = await start({
    viewport: { width: 390, height: 844 },
    touch: true,
    permissions: ['clipboard-read', 'clipboard-write'],
  });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await recordInput(s.page);
    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });

    const first = 'hold-' + Math.random().toString(36).slice(2, 6);
    const line = first + ' more-' + Math.random().toString(36).slice(2, 6);
    await typeLine(s.page, 'echo ' + line);
    ok(await awaitScreen(s.page, line, 20000), 'a line to copy is on the screen',
      oneLine(await screen(s.page)).slice(-60));
    await wait(300);
    const row = await s.page.evaluate((want) => {
      const rows = document.querySelector('#term .xterm-rows');
      for (const r of rows.children) {
        if (r.innerText.includes(want) && !r.innerText.includes('echo ' + want)) {
          const b = r.getBoundingClientRect();
          return { x: b.left, y: b.top + b.height / 2, right: b.right };
        }
      }
      return null;
    }, first);
    ok(!!row, 'and the row it is on is known', row ? Math.round(row.x) + ',' + Math.round(row.y)
      : 'not found in: ' + JSON.stringify(await s.page.evaluate((want) => [...document.querySelector('#term .xterm-rows').children]
        .map((r) => r.innerText).filter((t) => t.includes(want)), plain)));
    const cdp = await s.context.newCDPSession(s.page);
    const finger = async (x, y, type) => cdp.send('Input.dispatchTouchEvent', {
      type, touchPoints: type === 'touchEnd' ? [] : [{ x, y }],
    });
    const sent = async () => (await sentBytes(s.page)).length;
    const hexOf = (text) => Buffer.from(text, 'utf8').toString('hex').match(/../g);
    const has = (frames, want) => frames.flatMap((f) => f.match(/../g) || []).join(',').includes(want.join(','));

    // Held still: the word under the finger.
    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    const before = await sent();
    await finger(row.x + 4, row.y, 'touchStart');
    await wait(700);
    await finger(row.x + 4, row.y, 'touchEnd');
    await wait(300);
    const word = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(word === first, 'a finger held on the pane selects the word under it, and lifting it copies',
      JSON.stringify(oneLine(word)));
    const toasted = await s.page.evaluate(() => [...document.querySelectorAll('.toast')].map((t) => t.textContent));
    ok(toasted.some((t) => /Copied/.test(t)), 'and says so, because a phone shows nothing else',
      toasted.join(' | ') || 'no toast');
    const after = (await sentBytes(s.page)).slice(before);
    ok(!has(after, hexOf('\x1b[<')), 'the hold was not a tap: no click reached the program',
      after.join(',') || 'nothing');

    // Held then moved: the selection grows with the finger.
    await s.page.evaluate(() => navigator.clipboard.writeText('stale'));
    await finger(row.x + 4, row.y, 'touchStart');
    await wait(700);
    for (let i = 1; i <= 8; i++) {
      await finger(row.x + 4 + ((row.right - 8 - row.x) * i) / 8, row.y, 'touchMove');
      await wait(30);
    }
    await finger(row.right - 4, row.y, 'touchEnd');
    await wait(300);
    const grown = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(grown.startsWith(line), 'moved on, the finger grows the selection to where it lifts',
      JSON.stringify(oneLine(grown)));
    ok(await s.page.evaluate(() => window.scrollY === 0), 'and the page did not scroll under it',
      String(await s.page.evaluate(() => window.scrollY)));

    // A tap is still a tap.
    const beforeTap = await sent();
    await finger(row.x + 4, row.y, 'touchStart');
    await wait(60);
    await finger(row.x + 4, row.y, 'touchEnd');
    await wait(400);
    const onTap = (await sentBytes(s.page)).slice(beforeTap);
    ok(has(onTap, hexOf('\x1b[<0;')), 'a short tap is the click it always was', onTap.join(',') || 'nothing');

    await shot(s.page, 'touchcopy');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------- 47. keyboard */

// keyboardable is the whole promise, measured: what may raise the on-screen
// keyboard, what may not, and the one button that does. `inputmode="none"` is
// the request a browser is asked to honour and `readonly` is the fact it
// cannot argue with; both are checked, because the promise is "never", and a
// promise that holds on some phones is not one.
const keyboardable = (page) => page.evaluate(() => {
  const muted = (node) => !!node && node.getAttribute('inputmode') === 'none' && node.readOnly;
  const btn = document.getElementById('kbBtn');
  const pane = document.querySelector('#term textarea');
  const active = document.activeElement;
  const copies = [...document.querySelectorAll('.kb-copy')];
  const copy = copies[0];
  const r = btn.getBoundingClientRect();
  const c = copy ? copy.getBoundingClientRect() : null;
  return {
    shown: !!btn.checkVisibility && btn.checkVisibility(),
    open: btn.getAttribute('aria-pressed'),
    filled: btn.classList.contains('on'),
    label: btn.getAttribute('aria-label'),
    pane: muted(pane),
    paneOpen: !!pane && !muted(pane),
    focus: active ? (active.closest('#term') ? 'pane' : active.closest('dialog') ? 'dialog' : active.localName) : null,
    focusMuted: !!active && muted(active),
    fields: [...document.querySelectorAll('input.input')]
      .filter((n) => n.checkVisibility && n.checkVisibility())
      .map((n) => ({ id: n.id || n.className, muted: muted(n) })),
    mics: [...document.querySelectorAll('.dictate')]
      .filter((n) => n.checkVisibility && n.checkVisibility())
      .map((row) => ({ field: !!row.querySelector('input'), mic: !!row.querySelector('.mic-btn') })),
    copies: copies.length,
    copy: copy ? {
      inDialog: !!copy.closest('dialog[open]'),
      shown: copy.checkVisibility(),
      over: Math.abs(c.left - r.left) < 2 && Math.abs(c.top - r.top) < 2,
      open: copy.getAttribute('aria-pressed'),
    } : null,
    keyboardKey: !!document.querySelector('#keybar [data-key="Keyboard"]'),
  };
});

// The keyboard switch: on a touch screen nothing raises the on-screen keyboard
// but the button in the top bar - not the pane, not a field, not a dialog - and
// a second press puts it away. The button is pressable everywhere, dialogs
// included, and on a device with a mouse none of it applies.
async function keyboard() {
  // What the stub hears is what a person would say to filter that list, so the
  // words land somewhere with a visible consequence rather than in a field
  // nobody looks at again.
  const stub = await openRouterStub({ text: 'dynamic' });
  // Wide enough that the session list is a column rather than a drawer. The
  // touch screen is the part that matters and it is on.
  const s = await start({
    viewport: { width: 1100, height: 900 },
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
    const saved = await s.context.request.put(s.url + '/api/settings', {
      data: {
        settings: {
          openrouter: { api_key: 'e2e-key', base_url: stub.url, transcribe_model: 'openai/whisper-1' },
        },
      },
    });
    ok(saved.ok(), 'the gateway is pointed at the stub', saved.status() + ' ' + stub.url);

    await open(s);
    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(400);

    /* ------------------------------------------------------ shut by default */

    const off = await keyboardable(s.page);
    ok(off.shown && off.open === 'false' && !off.filled,
      'the button is in the bar, and the keyboard starts put away',
      JSON.stringify({ shown: off.shown, open: off.open, filled: off.filled }));
    ok(/show the keyboard/i.test(off.label || ''), 'and it says what a press does', off.label);
    ok(off.pane, 'the pane cannot raise the keyboard: inputmode=none, and read-only for the phones that ignore it',
      off.pane ? 'inputmode=none and readonly' : 'an ordinary text field');
    ok(!off.keyboardKey, 'and the key bar has no keyboard key of its own any more', String(off.keyboardKey));

    // Tapping the pane is the gesture that used to open the keyboard on every
    // tap. It still puts the focus where it belongs, and that is all it does.
    await s.page.click('#term');
    await wait(200);
    const tapped = await keyboardable(s.page);
    ok(tapped.focus === 'pane' && tapped.focusMuted && tapped.open === 'false',
      'a tap on the pane takes the focus with the keyboard shut',
      JSON.stringify({ focus: tapped.focus, muted: tapped.focusMuted, open: tapped.open }));

    /* ------------------------------------------------ the pane, by the button */

    await s.page.click('#kbBtn');
    await wait(200);
    const up = await keyboardable(s.page);
    ok(up.open === 'true' && up.filled && up.paneOpen && up.focus === 'pane' && !up.focusMuted,
      'a press raises it for the pane: the pane is an ordinary field again, and has the focus',
      JSON.stringify({ open: up.open, filled: up.filled, paneOpen: up.paneOpen, focus: up.focus }));
    ok(/hide the keyboard/i.test(up.label || ''), 'and the button now says it puts it away', up.label);
    await s.page.click('#term');
    await wait(200);
    const stays = await keyboardable(s.page);
    ok(stays.open === 'true' && stays.paneOpen,
      'a tap on the pane while it is up leaves it up', JSON.stringify({ open: stays.open }));
    await s.page.click('#kbBtn');
    await wait(200);
    const down = await keyboardable(s.page);
    ok(down.open === 'false' && down.pane && down.focus === 'pane',
      'a second press puts it away, and the pane keeps the focus for a real keyboard',
      JSON.stringify({ open: down.open, pane: down.pane, focus: down.focus }));

    // The system can put the keyboard away without the page hearing of it -
    // here a viewport that shrank for it and then came back only part of the
    // way. The button goes by the keyboard it can see, not by its own word:
    // the next press raises it again rather than "putting away" nothing.
    const viewport = (gap) => s.page.evaluate((g) => {
      Object.defineProperty(window.visualViewport, 'height',
        { configurable: true, get: () => window.innerHeight - g });
      window.visualViewport.dispatchEvent(new Event('resize'));
    }, gap);
    await s.page.click('#kbBtn');
    await viewport(320);
    await wait(100);
    await viewport(110);
    await wait(1700);
    const stale = await keyboardable(s.page);
    await s.page.click('#kbBtn');
    await wait(200);
    const raised = await keyboardable(s.page);
    ok(stale.open === 'true' && raised.open === 'true' && raised.paneOpen && raised.focus === 'pane',
      'a press over a keyboard the system took away raises it again',
      JSON.stringify({ before: stale.open, after: raised.open, paneOpen: raised.paneOpen, focus: raised.focus }));
    await viewport(320);
    await wait(100);
    await s.page.click('#kbBtn');
    await wait(200);
    const lowered = await keyboardable(s.page);
    ok(lowered.open === 'false' && lowered.pane,
      'and a press over one that is really up still puts it away', JSON.stringify({ open: lowered.open }));
    await s.page.evaluate(() => {
      delete window.visualViewport.height;
      window.visualViewport.dispatchEvent(new Event('resize'));
    });

    // Leaving every field is the keyboard going away on its own, and the
    // button follows it rather than staying pressed over nothing.
    await s.page.click('#kbBtn');
    await wait(150);
    await s.page.click('#optionsBtn');
    await wait(250);
    const left = await keyboardable(s.page);
    ok(left.open === 'false' && left.pane,
      'the focus leaving every field puts the switch back', JSON.stringify({ open: left.open, pane: left.pane }));
    // The same button again puts its menu away.
    await s.page.click('#optionsBtn');
    await s.page.waitForSelector('.menu', { state: 'detached', timeout: 5000 });

    /* ------------------------------------------------------- in a dialog */

    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 10000 });
    await wait(500);
    const sheet = await keyboardable(s.page);
    ok(sheet.fields.length >= 1 && sheet.fields.every((f) => f.muted),
      'every field on the sheet is muted', JSON.stringify(sheet.fields));
    ok(sheet.mics.length >= 1 && sheet.mics.every((row) => row.field && row.mic),
      'and every one has its microphone', JSON.stringify(sheet.mics));
    ok(sheet.copies === 1 && sheet.copy && sheet.copy.inDialog && sheet.copy.shown && sheet.copy.over,
      'the button is pressable over the dialog, in the very spot the bar has it',
      JSON.stringify(sheet.copy));
    await shot(s.page, 'keyboard-sheet');

    await s.page.click('#newSessionSheet .kb-copy');
    await wait(200);
    const typing = await keyboardable(s.page);
    ok(typing.open === 'true' && typing.copy && typing.copy.open === 'true'
      && typing.focus === 'dialog' && !typing.focusMuted,
      'pressed there, it raises the keyboard for the dialog\'s field',
      JSON.stringify({ open: typing.open, copy: typing.copy, focus: typing.focus, muted: typing.focusMuted }));
    await s.page.click('#newSessionSheet .kb-copy');
    await wait(200);
    const shut = await keyboardable(s.page);
    ok(shut.open === 'false' && shut.fields.every((f) => f.muted),
      'and pressed again, puts it away', JSON.stringify({ open: shut.open, fields: shut.fields }));

    // A field is still filled by speaking to it. What comes back is what would
    // have been typed: it goes in the field and the field is told, so the list
    // filters exactly as it does under a finger.
    await s.page.click('#nsDir .mic-btn');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    await wait(1200);
    await s.page.click('#recSend');
    const heard = await s.page.waitForFunction(
      (want) => document.querySelector('#nsDir .combo-input').value === want,
      stub.text, { timeout: 60000 }).then(() => true).catch(() => false);
    ok(heard, 'a muted field is filled by speaking into it',
      await s.page.$eval('#nsDir .combo-input', (n) => n.value));
    await s.page.click('#newSessionSheet .sheet-title');
    await s.page.click('#nsCancel');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 10000 })
      .catch(() => {});

    // The key bar and touch mode are this device's choices, in the options
    // menu - and on a touch screen touch mode is on before anybody asks.
    const offered = await options(s.page);
    ok(!!offered.keybar && !!offered.touch && offered.touch.on,
      'the key bar and touch mode are in the options menu, touch mode on', JSON.stringify(offered));
    await closeOptions(s.page);

    // The dialog that asks for one line of text is a field like any other.
    await s.page.click('#sessionTitle');
    await s.page.waitForSelector('dialog.modal', { timeout: 10000 });
    await wait(400);
    const rename = await keyboardable(s.page);
    ok(rename.fields.length === 1 && rename.fields[0].muted && rename.mics.length === 1
      && rename.copy && rename.copy.shown && rename.copy.over,
      'so is the rename dialog: its field muted, with a microphone, and the button over it',
      JSON.stringify({ fields: rename.fields, mics: rename.mics, copy: rename.copy }));
    await s.page.click('dialog.modal .kb-copy');
    await wait(200);
    const renaming = await keyboardable(s.page);
    ok(renaming.open === 'true' && renaming.focus === 'dialog' && !renaming.focusMuted,
      'and the button raises the keyboard for it', JSON.stringify({ open: renaming.open, focus: renaming.focus }));
    await s.page.click('dialog.modal .kb-copy');
    await wait(200);
    // A tablet in its case types on keys of its own, and a muted field is
    // given back to them rather than swallowing the key.
    await s.page.keyboard.type('xy');
    const keyed = await s.page.evaluate(() => {
      const node = document.activeElement;
      return { value: node.value, ro: node.readOnly, dialog: !!node.closest('dialog') };
    });
    ok(keyed.dialog && keyed.value.endsWith('xy') && !keyed.ro,
      'a key pressed on real keys lands in the muted field', JSON.stringify(keyed));
    await s.page.keyboard.press('Escape');
    await s.page.waitForSelector('dialog.modal', { state: 'detached', timeout: 10000 }).catch(() => {});
    await wait(300);
    const after = await keyboardable(s.page);
    ok(after.copies === 0 && after.open === 'false' && after.pane,
      'a closed dialog takes its copy of the button with it, and the pane is muted again',
      JSON.stringify({ copies: after.copies, open: after.open, pane: after.pane }));

    /* ------------------------------------------------- not on a desk */

    const desk = await s.browser.newContext({ viewport: { width: 1280, height: 720 }, colorScheme: 'light' });
    await desk.addCookies(await s.context.cookies());
    const deskPage = await desk.newPage();
    const openId = await s.page.evaluate(() => location.hash.slice(1));
    await deskPage.goto(s.url + '/#' + openId, { waitUntil: 'domcontentloaded' });
    await deskPage.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(600);
    const atDesk = await keyboardable(deskPage);
    ok(!atDesk.shown && atDesk.paneOpen,
      'a device with a mouse has no button and no muted field', JSON.stringify({ shown: atDesk.shown, paneOpen: atDesk.paneOpen }));
    await deskPage.close();
    await desk.close();

    // Tesla's browser is a Chromium that says its pointer is fine and can
    // hover, with no touch points to give it away: its name is what does.
    const car = await s.browser.newContext({
      viewport: { width: 1280, height: 720 },
      colorScheme: 'light',
      userAgent: 'Mozilla/5.0 (X11; GNU/Linux) AppleWebKit/537.36 (KHTML, like Gecko) '
        + 'Chromium/120.0.6099.0 Chrome/120.0.6099.0 Safari/537.36 Tesla/2024.26.7',
    });
    await car.addCookies(await s.context.cookies());
    const carPage = await car.newPage();
    await carPage.goto(s.url + '/#' + openId, { waitUntil: 'domcontentloaded' });
    await carPage.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(600);
    const inCar = await keyboardable(carPage);
    ok(inCar.shown && inCar.pane && inCar.open === 'false',
      'a car that says it has a mouse still has the button, and a muted pane',
      JSON.stringify({ shown: inCar.shown, pane: inCar.pane, open: inCar.open }));
    await carPage.click('#kbBtn');
    await wait(200);
    const carUp = await keyboardable(carPage);
    ok(carUp.open === 'true' && carUp.paneOpen && carUp.focus === 'pane',
      'and its button raises the keyboard for the pane',
      JSON.stringify({ open: carUp.open, paneOpen: carUp.paneOpen, focus: carUp.focus }));
    await carPage.close();
    await car.close();

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

/* --------------------------------------------------------- 48. transfer */

// Copy, paste and upload as buttons: a file lands in the working directory
// and its path on the prompt, the clipboard reaches the pane, the pane's text
// reaches the clipboard - and on a phone the three leave the full bar and
// float over the pane instead.
async function transfer() {
  const s = await start({
    viewport: { width: 1280, height: 720 },
    permissions: ['clipboard-read', 'clipboard-write'],
  });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const hiddenBefore = await s.page.evaluate(() => document.getElementById('transfer').hidden);
    ok(hiddenBefore, 'with nothing attached there is nothing to copy into', String(hiddenBefore));
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await s.page.waitForSelector('#transfer:not([hidden])', { timeout: 10000 });
    const inBar = await s.page.evaluate(() => {
      const bar = document.querySelector('.topbar').getBoundingClientRect();
      const box = document.getElementById('transfer').getBoundingClientRect();
      return { inside: box.top >= bar.top - 1 && box.bottom <= bar.bottom + 1, width: Math.round(box.width) };
    });
    ok(inBar.inside && inBar.width > 100, 'at a desk the three buttons are cells of the bar', JSON.stringify(inBar));
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);

    const res = await s.context.request.get(s.url + '/api/sessions/' + id);
    const workdir = (await res.json()).session.workdir;

    // Upload: the picker, a file name with a space in it, and the path that
    // comes back on the prompt.
    const png = Buffer.from('89504e470d0a1a0a' + '00'.repeat(24), 'hex');
    await s.page.setInputFiles('input[type=file]', { name: 'shot one.png', mimeType: 'image/png', buffer: png });
    const where = join(workdir, '.socrates', 'uploads', 'shot-one.png');
    ok(await awaitPacked(s.page, '.socrates/uploads/shot-one.png'),
      'the uploaded file\'s path is put on the prompt', oneLine(await screen(s.page)).slice(-90));
    let stored = null;
    try { stored = readFileSync(where); } catch { /* reported below */ }
    ok(!!stored && stored.equals(png), 'the file is in the working directory, byte for byte',
      where + ' ' + (stored ? stored.length + ' bytes' : 'missing'));
    let ignore = '';
    try { ignore = readFileSync(join(workdir, '.socrates', 'uploads', '.gitignore'), 'utf8'); } catch { /* below */ }
    ok(ignore === '*\n', 'and git is told to look away', JSON.stringify(ignore));
    await s.page.keyboard.press('Control+U');

    // An image pasted into the pane takes the same road.
    await s.page.evaluate(() => {
      const data = new DataTransfer();
      data.items.add(new File([new Uint8Array([137, 80, 78, 71])], 'clip.png', { type: 'image/png' }));
      document.querySelector('#term .xterm-helper-textarea')
        .dispatchEvent(new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true }));
    });
    ok(await awaitPacked(s.page, '.socrates/uploads/clip.png'),
      'a pasted image is uploaded and its path put on the prompt', oneLine(await screen(s.page)).slice(-90));
    await s.page.keyboard.press('Control+U');

    // Paste: the clipboard, into the pane, by the button.
    const line = 'pasted-' + Math.random().toString(36).slice(2, 8);
    await s.page.evaluate((text) => navigator.clipboard.writeText(text), 'echo ' + line);
    await s.page.click('#pasteBtn');
    ok(await awaitScreen(s.page, 'echo ' + line), 'the paste button puts the clipboard on the prompt',
      oneLine(await screen(s.page)).slice(-60));
    await s.page.keyboard.press('Enter');
    await awaitScreen(s.page, line + '\n');

    // Copy with nothing selected: the pane's text in a sheet, and Copy all.
    await s.page.click('#copyBtn');
    await s.page.waitForSelector('.copy-sheet[open]', { timeout: 5000 });
    const shown = await s.page.$eval('.copy-sheet textarea', (n) => n.value);
    ok(shown.includes(line), 'with nothing selected, Copy opens the pane\'s text', shown.slice(-80));
    await s.page.click('.copy-sheet .btn.primary');
    await s.page.waitForSelector('.copy-sheet', { state: 'detached', timeout: 5000 });
    const copied = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(copied === shown, 'and Copy all puts all of it on the clipboard', copied.length + ' of ' + shown.length + ' chars');

    /* ------------------------------------------------------- on a phone */

    const phone = await s.browser.newContext({
      viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, colorScheme: 'light',
    });
    await phone.addCookies(await s.context.cookies());
    const phonePage = await phone.newPage();
    await phonePage.goto(s.url + '/#' + id, { waitUntil: 'domcontentloaded' });
    await phonePage.waitForSelector('#transfer:not([hidden])', { timeout: 20000 });
    await wait(600);
    const placed = await phonePage.evaluate(() => {
      const bar = document.querySelector('.topbar');
      const box = document.getElementById('transfer').getBoundingClientRect();
      return {
        floats: getComputedStyle(document.getElementById('transfer')).position === 'absolute',
        below: box.top >= bar.getBoundingClientRect().bottom,
        onScreen: box.right <= innerWidth && box.left >= 0,
        fits: bar.scrollWidth <= bar.clientWidth,
        clear: document.getElementById('termLines').getBoundingClientRect().top >= box.bottom,
      };
    });
    ok(placed.floats && placed.below && placed.onScreen && placed.fits && placed.clear,
      'on a phone the three float over the pane, the bar still fits, and a notice starts under them', JSON.stringify(placed));
    await phonePage.close();
    await phone.close();

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------- preStart and the bucket */

// localInput is a moment the way a datetime-local field spells it: this
// browser's local time, to the minute. The page and the suite run on the
// same machine, so "local" is the same zone on both sides.
function localInput(at) {
  const d = new Date(at);
  const two = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + two(d.getMonth() + 1) + '-' + two(d.getDate())
    + 'T' + two(d.getHours()) + ':' + two(d.getMinutes());
}

// draftOf is the server's copy of a prepared session's prompt.
async function draftOf(s, id) {
  const res = await s.context.request.get(s.url + '/api/sessions/' + encodeURIComponent(id) + '/draft');
  return res.ok() ? res.json() : { status: res.status() };
}

// panelUp waits for the panel of a prepared session to be the thing on screen.
const panelUp = (page, timeout = 15000) =>
  page.waitForSelector('#prestart:not([hidden]) #psPrompt', { timeout });

// panelBecameTerminal waits for the panel to give way to a live terminal,
// which is what a start - pressed here, elsewhere, or by the clock - looks
// like from the page.
const panelBecameTerminal = (page, timeout) => page.waitForFunction(() => {
  const panel = document.getElementById('prestart');
  return (!panel || panel.hidden) && !document.getElementById('termWrap').hidden
    && !!document.querySelector('#term .xterm');
}, null, { timeout });

// The prompt a prepared session is written with: several lines, and every
// character a shell would otherwise take for its own - which is the point,
// because it reaches the program through a wrapper script, not a terminal.
const PREPARED_PROMPT = 'Fix the "flaky" test in $HOME/it\'s\n'
  + 'second line with `ticks`, a backslash \\ and ü\n'
  + '-- and a line that looks like a flag';

async function prestart() {
  const s = await start({ viewport: { width: 1280, height: 860 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const { page } = s;

    // The sheet: preStart is offered for an agent, not for a shell, and turns
    // the button into Prepare.
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.waitForSelector('#newSessionSheet .seg[data-value="shell"]', { timeout: 10000 });
    await page.click('#newSessionSheet .seg[data-value="shell"]');
    const forShell = await page.$eval('#nsPrestartField', (n) => n.hidden);
    await page.click('#newSessionSheet .seg[data-value="claude"]');
    const forClaude = await page.$eval('#nsPrestartField', (n) => !n.hidden);
    ok(forShell && forClaude, 'the sheet offers preStart for Claude Code and not for a shell',
      JSON.stringify({ shellHidden: forShell, claudeShown: forClaude }));
    const offByDefault = await page.$eval('#nsPrestart .seg[data-value="off"]', (n) => n.getAttribute('aria-pressed'));
    await page.click('#nsPrestart .seg[data-value="on"]');
    const label = await page.$eval('#nsStart', (n) => n.textContent.trim());
    ok(offByDefault === 'true' && label === 'Prepare', 'it is off when the sheet opens, and on it makes the button Prepare',
      offByDefault + ' / ' + label);
    await page.click('#nsStart');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await panelUp(page);
    const id = await page.evaluate(() => location.hash.slice(1));

    // Nothing ran: no terminal on the page, no tmux session, no launch.
    let row = await sessionRow(s, id);
    ok(row && row.state === 'prestart', 'the session is stored as prepared', row && row.state);
    const quiet = await page.evaluate(() => ({
      term: document.getElementById('termWrap').hidden,
      keybar: document.getElementById('keybar').hidden,
      transfer: document.getElementById('transfer').hidden,
    }));
    ok(quiet.term && quiet.keybar && quiet.transfer, 'the panel stands where the terminal would, with no key bar and no transfer buttons',
      JSON.stringify(quiet));
    ok(sessionsOn(s.data).length === 0 && launchesOf(s, 'claude').length === 0,
      'and nothing was started for it: no tmux session, no launch',
      sessionsOn(s.data).length + ' tmux sessions, ' + launchesOf(s, 'claude').length + ' launches');
    const group = await page.evaluate((sid) => {
      const first = document.querySelector('#sessionList .folder');
      return {
        first: first && first.dataset.folder,
        inside: !!(first && first.querySelector('.chat-item[data-id="' + sid + '"]')),
      };
    }, id);
    ok(group.first === '#prestart' && group.inside, 'its row is in the preStart group, at the very top of the list',
      JSON.stringify(group));

    // The prompt saves itself, and the bucket takes it.
    await page.waitForFunction(() => !document.getElementById('psPrompt').disabled, null, { timeout: 10000 });
    await page.fill('#psPrompt', PREPARED_PROMPT);
    await page.waitForFunction(() => /Saved|Backed up/.test(document.getElementById('psSave').textContent),
      null, { timeout: 10000 });
    let draft = await draftOf(s, id);
    ok(draft.text === PREPARED_PROMPT, 'the prompt reached the server as typed', JSON.stringify(draft.text));
    const backed = await page.waitForFunction(() => /Backed up/.test(document.getElementById('psSave').textContent),
      null, { timeout: 40000 }).then(() => true, () => false);
    ok(backed, 'and the panel says it is backed up once the bucket has it',
      await page.$eval('#psSave', (n) => n.textContent.trim()));

    // A setting changed in the panel is saved on its own.
    await page.waitForSelector('#psModel .combo-input', { timeout: 10000 });
    await page.click('#psModel .combo-input');
    await page.waitForSelector('#psModel .combo-option', { timeout: 5000 });
    await page.click('#psModel .combo-option:has(.combo-label:text-is("Haiku"))');
    const until = Date.now() + 8000;
    while (Date.now() < until && (row = await sessionRow(s, id)) && row.model !== 'haiku') await wait(300);
    ok(row && row.model === 'haiku', 'a model picked in the panel is saved with the session', row && row.model);

    // A reload brings back the same text and the same choice.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await panelUp(page);
    const kept = await page.waitForFunction((want) => document.getElementById('psPrompt').value === want,
      PREPARED_PROMPT, { timeout: 10000 }).then(() => true, () => false);
    ok(kept, 'after a reload the panel shows the same prompt',
      JSON.stringify(await page.$eval('#psPrompt', (n) => n.value)));
    const shownModel = await page.$eval('#psModel .combo-input', (n) => n.value.trim()).catch(() => '');
    ok(/haiku/i.test(shownModel), 'and the model that was picked', shownModel);

    // Start: the same launch a new session takes, with the prompt as the
    // program's first one.
    await page.click('#psStart');
    const live = await panelBecameTerminal(page, 30000).then(() => true, () => false);
    ok(live, 'Start turns the panel into a live terminal');
    row = await waitForState(s, id, ['running'], 20000);
    ok(row && row.state === 'running', 'and the session is running', row && row.state);
    const firstLine = PREPARED_PROMPT.split('\n')[0];
    const onScreen = await awaitScreen(page, 'prompt: ' + firstLine.slice(0, 20), 20000).then(() => true, () => false);
    ok(onScreen, 'the program shows the prompt it was started with', oneLine(await screen(page)));
    const launch = lastLaunch(s, 'claude');
    ok(!!launch && launch.prompt === PREPARED_PROMPT, 'it received the prompt byte for byte',
      launch ? JSON.stringify(launch.prompt) + ' (' + launch.prompt_bytes + ' bytes)' : 'no launch');
    ok(!!launch && /(^| )--model haiku( |$)/.test(argvOf(launch)), 'with the model chosen in the panel', argvOf(launch));
    await typeLine(page, 'after the prompt');
    const echoed = await awaitScreen(page, 'you said: after the prompt', 15000).then(() => true, () => false);
    ok(echoed, 'and the terminal takes keystrokes like any other');
    const moved = await page.evaluate((sid) => {
      const prepared = document.querySelector('#sessionList .folder[data-folder="#prestart"]');
      return !prepared && !!document.querySelector('#sessionList .chat-item[data-id="' + sid + '"]');
    }, id);
    ok(moved, 'its row left the preStart group for the ordinary ones', String(moved));
    draft = await draftOf(s, id);
    const refused = await s.context.request.put(s.url + '/api/sessions/' + id + '/draft',
      { data: { text: 'too late', base_rev: draft.rev } });
    ok(refused.status() === 409 && draft.text === PREPARED_PROMPT, 'the prompt is kept, read-only, after the start',
      refused.status() + ' ' + JSON.stringify(draft.text));

    // A start by the clock. A second prepared session, opened in the panel.
    const made = await (await s.context.request.post(s.url + '/api/sessions', {
      data: { prestart: true, harness: 'codex', prompt: 'started by the clock\nsecond line' },
    })).json();
    const sid = made.session && made.session.id;
    // The page learns of a session made elsewhere from its list, which it
    // reads again when the window is looked at.
    await page.evaluate(() => window.dispatchEvent(new Event('focus')));
    await page.waitForSelector(rowSel(sid), { timeout: 20000 });
    await page.click(rowSel(sid) + ' .label');
    await panelUp(page);
    await page.waitForFunction((want) => document.getElementById('psPrompt').value === want,
      'started by the clock\nsecond line', { timeout: 10000 }).catch(() => {});

    // The panel's own control: a time two hours ahead, saved with the
    // settings and shown on the row as a clock.
    const later = Date.now() + 2 * 3600 * 1000 + 5 * 60 * 1000;
    await page.fill('#psStartAt', localInput(later));
    await page.dispatchEvent('#psStartAt', 'change');
    let srow = null;
    const saveBy = Date.now() + 8000;
    while (Date.now() < saveBy && !((srow = await sessionRow(s, sid)) && srow.start_at > 0)) await wait(300);
    ok(srow && Math.abs(srow.start_at - Math.floor(later / 60000) * 60000) < 1000,
      'a time picked in the panel is saved as that minute', srow && new Date(srow.start_at).toISOString());
    const hint = await page.$eval('#psStartAtHint', (n) => n.textContent.trim());
    ok(/in 2 h/.test(hint), 'the panel says how far away it is', hint);
    const clock = await page.waitForSelector('#sessionList .chat-item[data-id="' + sid + '"] .row-when svg',
      { timeout: 20000 }).then(() => true, () => false);
    ok(clock, 'and its row wears a clock and the time',
      await page.$eval('#sessionList .chat-item[data-id="' + sid + '"] .row-when', (n) => n.textContent).catch(() => 'no row'));

    // A field to the minute cannot say "five seconds from now", so the near
    // time goes through the same PATCH the panel uses.
    const due = Date.now() + 5000;
    const patched = await s.context.request.patch(s.url + '/api/sessions/' + sid + '/prestart', { data: { start_at: due } });
    ok(patched.ok(), 'a start five seconds ahead is accepted', String(patched.status()));
    await wait(Math.max(0, due - Date.now() - 1500));
    srow = await sessionRow(s, sid);
    ok(srow && srow.state === 'prestart', 'it has not started early', srow && srow.state);
    const flipped = await panelBecameTerminal(page, 45000).then(() => true, () => false);
    const late = ((Date.now() - due) / 1000).toFixed(1);
    ok(flipped, 'when it falls due, the open panel becomes its terminal by itself', late + ' s after the time');
    srow = await sessionRow(s, sid);
    ok(srow && srow.state === 'running' && !srow.start_at, 'the session is running and no longer scheduled',
      srow && (srow.state + ', start_at ' + srow.start_at));
    const clocked = lastLaunch(s, 'codex');
    ok(!!clocked && clocked.prompt === 'started by the clock\nsecond line', 'and the program got its prompt',
      clocked ? JSON.stringify(clocked.prompt) : 'no launch');

    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// s3restore is the reason the bucket exists: a machine that is gone, and a
// new one with an empty data directory that comes back with everything.
async function s3restore() {
  const a = await start({ viewport: { width: 1280, height: 720 } });
  let aStopped = false;
  let ids = {};
  let cookie = '';
  try {
    await setup(a.page, a.url);
    await useDomRenderer(a);
    const shell = await (await a.context.request.post(a.url + '/api/sessions', {
      data: { harness: 'shell', title: 'restore shell' },
    })).json();
    const prepared = await (await a.context.request.post(a.url + '/api/sessions', {
      data: { prestart: true, harness: 'claude', title: 'restore draft', prompt: PREPARED_PROMPT },
    })).json();
    ids = { shell: shell.session && shell.session.id, prepared: prepared.session && prepared.session.id };
    ok(!!ids.shell && !!ids.prepared, 'server A has a running session and a prepared one', JSON.stringify(ids));

    // Wait for the mirror to have nothing left to upload, so what is being
    // tested is the restore and not a race with the flusher.
    let status = {};
    const until = Date.now() + 20000;
    while (Date.now() < until) {
      status = await (await a.context.request.get(a.url + '/api/storage')).json();
      if (status.state === 'ok' && status.pending === 0) break;
      await wait(400);
    }
    ok(status.state === 'ok' && status.pending === 0, 'and the bucket holds all of it',
      JSON.stringify({ state: status.state, pending: status.pending, prefix: status.storage && status.storage.prefix }));
    const cookies = await a.context.cookies(a.url);
    cookie = cookies.map((c) => c.name + '=' + c.value).join('; ');

    // Machine A goes away: the server is stopped the way a container is, and
    // then everything it had on its disk is gone - data directory, tmux
    // server, the panes in it.
    await a.context.close().catch(() => {});
    await a.browser.close().catch(() => {});
    a.server.kill('SIGTERM');
    const outcome = await Promise.race([a.exited, wait(15000).then(() => 'timeout')]);
    if (outcome === 'timeout') a.server.kill('SIGKILL');
    ok(outcome !== 'timeout', 'server A stopped cleanly, letting go of the bucket', JSON.stringify(outcome));
    const released = a.log.join('');
    killTmux(a.data);
    rmSync(a.data, { recursive: true, force: true });
    aStopped = true;
    ok(!/storage: .*(failed|error)/i.test(released), 'and said nothing about failing to flush',
      oneLine((released.match(/storage: [^\n]*/g) || []).join(' | ') || 'no storage lines'));
  } catch (err) {
    ok(false, 'server A set the scene', String(err && err.stack || err));
  } finally {
    if (!aStopped) {
      // Something went wrong before A was taken down on purpose: tidy it the
      // usual way so nothing leaks, and leave B out of it.
      await a.stop();
      return;
    }
  }

  // Machine B: a new data directory, the same bucket and prefix.
  const b = await start({ viewport: { width: 1280, height: 720 }, s3prefix: a.s3prefix });
  try {
    ok(b.data !== a.data, 'server B runs on a data directory of its own', b.data);
    const state = await (await fetch(b.url + '/api/state')).json();
    ok(state.mode === 'normal' && state.setup_required === false,
      'it starts from the bucket, with no setup to do', JSON.stringify({ mode: state.mode, setup_required: state.setup_required }));
    const carried = await (await fetch(b.url + '/api/state', { headers: { cookie } })).json();
    ok(carried.authenticated === true, 'the sign-in made on server A is still valid on server B', String(carried.authenticated));

    await b.page.goto(b.url + '/login', { waitUntil: 'domcontentloaded' });
    await b.page.fill('#password', PASSWORD);
    await b.page.click('#submit');
    await b.page.waitForFunction(() => location.pathname === '/', null, { timeout: 15000 }).catch(() => {});
    await b.page.waitForSelector('#newSession', { timeout: 15000 });
    ok(true, 'the same password signs in');

    const listed = ((await (await b.context.request.get(b.url + '/api/sessions')).json()).sessions || []);
    const byId = Object.fromEntries(listed.map((x) => [x.id, x]));
    ok(byId[ids.shell] && byId[ids.shell].title === 'restore shell'
      && byId[ids.prepared] && byId[ids.prepared].title === 'restore draft',
      'both sessions are listed, under the same ids and names',
      listed.map((x) => x.title + ':' + x.state).join(', '));
    ok(byId[ids.prepared] && byId[ids.prepared].state === 'prestart', 'the prepared one is still prepared',
      byId[ids.prepared] && byId[ids.prepared].state);
    ok(byId[ids.shell] && byId[ids.shell].state !== 'running', 'and the one whose pane was on machine A is not taken for running',
      byId[ids.shell] && byId[ids.shell].state);
    const draft = await draftOf(b, ids.prepared);
    ok(draft.text === PREPARED_PROMPT, 'the prepared prompt is intact', JSON.stringify(draft.text));
    const settings = await (await b.context.request.get(b.url + '/api/settings')).json();
    const webgl = settings && settings.settings && settings.settings.terminal && settings.settings.terminal.webgl;
    ok(webgl === false, 'and so is a setting made on server A', JSON.stringify(webgl));

    await b.page.evaluate((target) => { location.hash = '#' + target; }, ids.prepared);
    await panelUp(b.page);
    const shown = await b.page.waitForFunction((want) => document.getElementById('psPrompt').value === want,
      PREPARED_PROMPT, { timeout: 10000 }).then(() => true, () => false);
    ok(shown, 'the panel on server B shows it');
    ok(unexpected(b.errors).length === 0, 'no console errors', unexpected(b.errors).join(' | ') || '0');
  } finally { await b.stop(); }
}

// storageOf is the short storage summary a signed-in page is given.
async function storageOf(s) {
  const state = await (await s.context.request.get(s.url + '/api/state')).json();
  return state.storage || {};
}

// awaitStorage polls the summary until `test` holds, and answers with the
// last one either way.
async function awaitStorage(s, test, timeout) {
  const until = Date.now() + timeout;
  let st = await storageOf(s);
  while (Date.now() < until && !test(st)) {
    await wait(500);
    st = await storageOf(s);
  }
  return st;
}

// bannerOf is what the storage banner shows, read off the page after it has
// been told to look again - the page reads the state on a wake, which is
// what a person coming back to the tab does.
async function bannerOf(page, test, timeout = 15000) {
  const read = () => page.evaluate(() => {
    const node = document.getElementById('storageBanner');
    return { shown: !!node && !node.hidden, kind: node ? node.dataset.kind || '' : '', text: node ? node.textContent.trim() : '' };
  });
  const until = Date.now() + timeout;
  let seen = await read();
  while (Date.now() < until && !test(seen)) {
    await page.evaluate(() => window.dispatchEvent(new Event('focus')));
    await wait(700);
    seen = await read();
  }
  return seen;
}

async function storagebanner() {
  const a = await start({ viewport: { width: 1280, height: 720 } });
  let b = null;
  try {
    await setup(a.page, a.url);
    await open(a);
    let banner = await bannerOf(a.page, (x) => !x.shown, 3000);
    ok(!banner.shown, 'with the bucket answering there is no storage banner', JSON.stringify(banner));

    // The bucket goes away. A change made now is kept here and queued.
    await fakeS3Switch('down?status=503');
    try {
      await a.context.request.post(a.url + '/api/sessions', {
        data: { prestart: true, harness: 'claude', title: 'written while offline', prompt: 'kept here' },
      });
      const st = await awaitStorage(a, (x) => x.state === 'offline', 20000);
      ok(st.state === 'offline' && st.pending > 0, 'the server notices and counts what is waiting',
        JSON.stringify({ state: st.state, pending: st.pending, error: st.last_error }));
      banner = await bannerOf(a.page, (x) => x.shown && x.kind === 'offline');
      ok(banner.shown && /^Storage unreachable since \d\d:\d\d\. \d+ changes? (is|are) waiting to be backed up\./.test(banner.text),
        'the page says the storage is unreachable, since when, and how much is waiting', banner.text);
    } finally {
      await fakeS3Switch('up');
    }
    const back = await awaitStorage(a, (x) => x.state === 'ok' && x.pending === 0, 90000);
    ok(back.state === 'ok' && back.pending === 0, 'once the bucket answers again the queue goes up by itself',
      JSON.stringify({ state: back.state, pending: back.pending }));
    banner = await bannerOf(a.page, (x) => !x.shown);
    ok(!banner.shown, 'and the banner leaves', JSON.stringify(banner));

    // A second Socrates on the same bucket: it may not start while this one
    // holds it, it says who does, and it can take it over with the password.
    b = await start({ viewport: { width: 1280, height: 720 }, s3prefix: a.s3prefix });
    const waiting = await (await fetch(b.url + '/api/state')).json();
    const connect = waiting.connect || {};
    ok(waiting.mode === 'connect' && connect.reason === 'leased' && !!(connect.lease && connect.lease.host),
      'a second server on the same bucket waits on the connect page, naming the holder',
      JSON.stringify({ mode: waiting.mode, reason: connect.reason, host: connect.lease && connect.lease.host }));
    await b.page.goto(b.url + '/setup', { waitUntil: 'domcontentloaded' });
    await b.page.waitForSelector('#takeoverBox:not([hidden])', { timeout: 10000 });
    const says = await b.page.$eval('#storageReason', (n) => n.textContent.trim()).catch(() => '');
    ok(/in use by/.test(says), 'its page says the bucket is in use, and by whom', says);
    await b.page.fill('#takeoverPassword', PASSWORD);
    await b.page.click('#takeoverBtn');
    const until = Date.now() + 60000;
    let mode = 'connect';
    while (Date.now() < until && mode !== 'normal') {
      await wait(500);
      try { mode = (await (await fetch(b.url + '/api/state')).json()).mode; } catch { /* restarting */ }
    }
    ok(mode === 'normal', 'the password takes the bucket over, and the second server starts', mode);

    // The first one finds out the next time it writes, and stops writing.
    await a.context.request.put(a.url + '/api/settings', { data: { settings: { terminal: { webgl: false } } } });
    const fenced = await awaitStorage(a, (x) => x.fenced, 45000);
    ok(fenced.fenced && fenced.state === 'fenced', 'the first server sees the lease is no longer its own',
      JSON.stringify({ state: fenced.state, holder: fenced.lease && fenced.lease.host }));
    banner = await bannerOf(a.page, (x) => x.shown && x.kind === 'fenced');
    ok(banner.shown && /^Another Socrates( \([^)]+\))? took over this bucket\. Changes made here are no longer backed up\./.test(banner.text),
      'and its page says another Socrates took the bucket over', banner.text);
    const bad = unexpected(a.errors, RESTART_NOISE);
    ok(bad.length === 0, 'no console errors', bad.join(' | ') || '0');
  } finally {
    if (b) await b.stop();
    await a.stop();
  }
}

/* ------------------------------------------------------------- options */

// The top bar's ⋮ is this device's options and nothing about a session: the
// key bar, touch mode, sound and notifications, as switches in a menu that
// stays open while they are pressed. Each is kept per device and takes effect
// at once. And beside it, the limits: how full each window is, how long until
// it renews, and - a tap away, because a phone has no tooltip - when exactly.
async function optionsmenu() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    const look = () => s.page.evaluate(() => {
      const area = document.querySelector('#term .xterm-helper-textarea');
      return {
        touch: document.documentElement.classList.contains('touch'),
        kb: !!document.getElementById('kbBtn')?.checkVisibility(),
        bar: document.getElementById('keybar').hidden ? 'hidden' : 'shown',
        pane: area ? { mode: area.getAttribute('inputmode') || '', ro: area.readOnly } : null,
        stored: {
          touch: localStorage.getItem('socrates.touch'),
          keybar: localStorage.getItem('socrates.term.keybar'),
        },
        expanded: document.getElementById('optionsBtn').getAttribute('aria-expanded'),
        menu: !!document.querySelector('.menu'),
      };
    });

    // Before any session: the button is there and the menu holds the four.
    const empty = await s.page.evaluate(() => ({
      button: !!document.getElementById('optionsBtn')?.checkVisibility(),
      gone: ['soundBtn', 'notifyBtn', 'sessionMenu'].filter((id) => document.getElementById(id)),
    }));
    ok(empty.button && empty.gone.length === 0,
      'the options button is in the bar before any session, and the old buttons are gone', JSON.stringify(empty));
    const first = await options(s.page);
    ok(Object.keys(first).join(',') === 'keybar,touch,sound,notify',
      'the menu holds the key bar, touch mode, sound and notifications', Object.keys(first).join(','));
    ok(!first.keybar.on && !first.touch.on && first.sound.on && !first.notify.on,
      'each where a desk starts: no key bar, no touch mode, sound on, notifications off',
      JSON.stringify(Object.fromEntries(Object.entries(first).map(([k, v]) => [k, v.on]))));
    const words = await s.page.$$eval('.menu .menu-item', (nodes) => nodes.map((n) => n.textContent));
    ok(!words.some((w) => /Rename|Delete|Info|Download|Done|Background/.test(w)),
      'and nothing that is done to a session', oneLine(words.join(' | ')));
    const opened = await look();
    ok(opened.expanded === 'true' && !opened.touch && !opened.kb, 'the button says its menu is open',
      JSON.stringify(opened));

    // Three ways to put it away: the button again, Escape, a press elsewhere.
    await s.page.click('#optionsBtn');
    const byButton = await s.page.waitForSelector('.menu', { state: 'detached', timeout: 3000 })
      .then(() => true).catch(() => false);
    await openOptions(s.page);
    await s.page.keyboard.press('Escape');
    const byEscape = await s.page.waitForSelector('.menu', { state: 'detached', timeout: 3000 })
      .then(() => true).catch(() => false);
    await openOptions(s.page);
    await s.page.click('#termEmpty .empty-title');
    const byPress = await s.page.waitForSelector('.menu', { state: 'detached', timeout: 3000 })
      .then(() => true).catch(() => false);
    ok(byButton && byEscape && byPress, 'the button again, Escape and a press elsewhere each close it',
      JSON.stringify({ byButton, byEscape, byPress }));
    ok((await look()).expanded === 'false', 'and the button says so', (await look()).expanded);

    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await wait(400);
    const desk = await look();
    ok(desk.bar === 'hidden' && desk.pane && desk.pane.mode !== 'none' && !desk.pane.ro,
      'a session at a desk: no key bar, and an ordinary pane', JSON.stringify(desk));

    // The key bar, from the menu, which stays open.
    ok(await option(s.page, 'keybar'), 'the key bar switch goes on', 'on');
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 5000 });
    const barOn = await look();
    ok(barOn.menu && barOn.bar === 'shown' && barOn.stored.keybar === 'on',
      'the bar is up at once, written down for this device, and the menu is still open', JSON.stringify(barOn));

    // Touch mode on: the keyboard button, and a muted pane.
    ok(await option(s.page, 'touch'), 'the touch mode switch goes on', 'on');
    const touchOn = await look();
    ok(touchOn.touch && touchOn.kb && touchOn.pane.mode === 'none' && touchOn.pane.ro
      && touchOn.stored.touch === 'on',
      'touch mode on: the keyboard button is in the bar and the pane waits for it', JSON.stringify(touchOn));
    await closeOptions(s.page);
    await s.page.click('#kbBtn');
    await wait(250);
    const raised = await s.page.evaluate(() => {
      const area = document.querySelector('#term .xterm-helper-textarea');
      return {
        pressed: document.getElementById('kbBtn').getAttribute('aria-pressed'),
        mode: area.getAttribute('inputmode') || '',
        focus: document.activeElement === area,
      };
    });
    ok(raised.pressed === 'true' && raised.mode !== 'none' && raised.focus,
      'and the keyboard button raises the keyboard for the pane', JSON.stringify(raised));

    // Per device: a reload comes back to both.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(500);
    const reloaded = await look();
    ok(reloaded.touch && reloaded.kb && reloaded.bar === 'shown' && reloaded.pane.mode === 'none',
      'a reload keeps touch mode and the key bar', JSON.stringify(reloaded));

    // Off, and a finger on the page does not turn it back on: the answer
    // wins over every sign.
    ok(!(await option(s.page, 'touch')), 'the touch mode switch goes off', 'off');
    await s.page.evaluate(() => document.body.dispatchEvent(
      new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true })));
    await wait(200);
    const touchOff = await look();
    ok(!touchOff.touch && !touchOff.kb && touchOff.pane.mode !== 'none' && !touchOff.pane.ro
      && touchOff.stored.touch === 'off',
      'touch mode off: no keyboard button, an ordinary pane, and a finger does not undo it', JSON.stringify(touchOff));
    ok(touchOff.bar === 'shown', 'the key bar asked for on its own stays up', touchOff.bar);

    // A device that has never said anything about the key bar gets the one
    // that goes with touch mode, at once.
    await s.page.evaluate(() => localStorage.removeItem('socrates.term.keybar'));
    await option(s.page, 'touch');
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 5000 }).catch(() => {});
    const follows = await look();
    await option(s.page, 'touch');
    await s.page.waitForSelector('#keybar[hidden]', { state: 'attached', timeout: 5000 }).catch(() => {});
    const followsOff = await look();
    ok(follows.bar === 'shown' && followsOff.bar === 'hidden' && followsOff.stored.keybar === null,
      'until it is set on its own, the key bar follows touch mode',
      JSON.stringify({ on: follows.bar, off: followsOff.bar, stored: followsOff.stored.keybar }));
    const now = await options(s.page);
    ok(!now.keybar.on && !now.touch.on, 'and the menu, drawn again, shows both where they are',
      JSON.stringify({ keybar: now.keybar.on, touch: now.touch.on }));
    await closeOptions(s.page);

    // The pane takes typing after all of it, and Escape for the menu never
    // reached it.
    const typed = 'opt-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + typed);
    ok(await awaitScreen(s.page, typed, 15000), 'the pane still takes a typed line', oneLine(await screen(s.page)).slice(-60));

    // What is done to a session is in its row, and the key bar is not.
    await rowMenu(s.page);
    const rowItems = await s.page.$$eval('.menu .menu-item', (nodes) => nodes.map((n) => n.textContent));
    ok(rowItems.includes('Info') && rowItems.includes('Rename') && rowItems.includes('Delete')
      && !rowItems.some((w) => /key bar/i.test(w)),
      'the row menu holds the session’s actions and no device option', rowItems.join(','));
    await s.page.keyboard.press('Escape');
    await s.page.waitForSelector('.menu', { state: 'detached', timeout: 3000 });

    /* ------------------------------------------------------- the limits */

    // A reading until the strip has drawn it, and then none: everything after
    // that is the page's own clock. The five-hour window renews in 2h 36m and
    // some seconds, so its countdown crosses a minute while it is on screen;
    // the Fable window renews in a few seconds, and is empty once it has.
    const at = Date.now();
    const five = new Date(at + (2 * 60 + 36) * 60000 + 20000).toISOString();
    const week = new Date(at + ((3 * 24 + 4) * 60 + 30) * 60000).toISOString();
    const lapsing = new Date(at + 25000).toISOString();
    let failing = false;
    let refused = 0;
    await s.page.route('**/api/sessions/*/usage', (route) => {
      if (failing) { refused += 1; return route.abort(); }
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          windows: [
            { label: '5h', used_percent: 42.4, resets_at: five },
            { label: '7d', used_percent: 30, resets_at: week },
            { label: 'Fable', used_percent: 90, resets_at: lapsing },
          ],
          cost_usd: 1.25,
        }),
      });
    });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForFunction(() => /5h\s*42%/.test(document.getElementById('sessionUsage')?.innerText || ''),
      null, { timeout: 20000 });
    const strip = await s.page.$eval('#sessionUsage', (n) => ({ text: n.innerText.replace(/\s+/g, ' ').trim(), title: n.title }));
    ok(/5h 42% \(2h 37m left\)/.test(strip.text) && /7d 30% \(3d 4h left\)/.test(strip.text)
      && /Fable 90% \(1m left\)/.test(strip.text) && /~\$1\.25/.test(strip.text),
      'the strip says how full each window is and how long until it renews', strip.text);
    ok(/5h: 42% used · resets in 2h 37m \((today|tomorrow) /.test(strip.title),
      'and the tooltip says when, by the clock', oneLine(strip.title));
    failing = true;

    // The reading behind a tap.
    await s.page.click('#sessionUsage');
    await s.page.waitForSelector('.usage-pop .usage-row', { timeout: 5000 });
    const pop = await s.page.evaluate((times) => ({
      rows: [...document.querySelectorAll('.usage-pop .usage-row')].map((row) => ({
        name: row.querySelector('.usage-row-name')?.textContent,
        used: row.querySelector('.usage-row-used')?.textContent,
        reset: row.querySelector('.usage-row-reset')?.textContent,
        meter: row.querySelector('.usage-meter > span')?.style.width || '',
      })),
      clocks: times.map((t) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })),
      expanded: document.getElementById('sessionUsage').getAttribute('aria-expanded'),
    }), [five, week]);
    const [r5, r7, rf, rc] = pop.rows;
    ok(pop.rows.length === 4 && r5.name === '5-hour limit' && r5.used === '42% used' && r5.meter === '42%'
      && r7.name === 'Weekly limit' && rf.name === 'Fable weekly limit' && rc.used === '~$1.25',
      'a tap opens every window by name, with its share and a meter, and the cost', JSON.stringify(pop.rows));
    ok(/^Resets in 2h 37m · (today|tomorrow) /.test(r5.reset) && r5.reset.endsWith(pop.clocks[0]),
      'the five-hour window says when exactly it renews', r5.reset + ' vs ' + pop.clocks[0]);
    ok(/^Resets in 3d 4h · /.test(r7.reset) && r7.reset.endsWith(pop.clocks[1]),
      'and so does the week, with its day', r7.reset + ' vs ' + pop.clocks[1]);
    ok(pop.expanded === 'true', 'the strip says its reading is open', pop.expanded);
    await wait(300);
    await shot(s.page, 'options-usage-desk');

    // The countdowns move with no new reading at all - every request after
    // the first fails - and the open reading moves with them. The Fable
    // window renews on screen and is empty again.
    const ticked = await s.page.waitForFunction(() => {
      const text = document.getElementById('sessionUsage').innerText.replace(/\s+/g, ' ');
      return /5h 42% \(2h 36m left\)/.test(text) && /Fable 0%/.test(text) && !/Fable 0% \(/.test(text);
    }, null, { timeout: 30000 }).then(() => true).catch(() => false);
    const afterTick = await s.page.evaluate(() => ({
      strip: document.getElementById('sessionUsage').innerText.replace(/\s+/g, ' ').trim(),
      fable: [...document.querySelectorAll('.usage-pop .usage-row')]
        .map((row) => row.textContent).find((t) => /Fable/.test(t)) || 'closed',
    }));
    ok(ticked, 'the countdown goes on with the network failing, and a renewed window reads 0%', afterTick.strip);
    ok(/0% used/.test(afterTick.fable) && /Renewed/.test(afterTick.fable),
      'and the open reading says the same', afterTick.fable);
    for (let i = 0; i < 40 && refused === 0; i += 1) await wait(500);
    ok(refused >= 1, 'it went on asking all the while, and was refused', refused + ' refused request(s)');
    await s.page.keyboard.press('Escape');
    await s.page.waitForSelector('.usage-pop', { state: 'detached', timeout: 3000 });

    // A phone: the strip is the bar's second line, every limit and its
    // countdown on it, nothing off the edge; the reading opens on screen.
    await s.page.setViewportSize({ width: 360, height: 740 });
    await wait(500);
    const phone = await s.page.evaluate(() => {
      const strip = document.getElementById('sessionUsage');
      const box = strip.getBoundingClientRect();
      const parts = [...strip.querySelectorAll('.usage-part')]
        .filter((p) => p.checkVisibility())
        .map((p) => { const r = p.getBoundingClientRect(); return { text: p.innerText.replace(/\s+/g, ' '), left: r.left, right: r.right }; });
      return {
        overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        stripLeft: box.left, stripRight: box.right,
        fits: strip.scrollWidth <= strip.clientWidth + 1,
        level: strip.className,
        parts,
        height: Math.round(document.querySelector('.topbar').getBoundingClientRect().height),
      };
    });
    const inside = phone.parts.every((p) => p.left >= phone.stripLeft - 1 && p.right <= phone.stripRight + 1);
    ok(phone.overflow <= 1 && phone.fits && inside && phone.height === 82,
      'on a phone every limit and its countdown fit on the second line', JSON.stringify(phone));
    ok(phone.parts.filter((p) => /\(\d/.test(p.text)).length >= 2,
      'with the countdowns still there', phone.parts.map((p) => p.text).join(' · '));
    await s.page.click('#sessionUsage');
    await s.page.waitForSelector('.usage-pop .usage-row', { timeout: 5000 });
    const popBox = await s.page.$eval('.usage-pop', (n) => {
      const r = n.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, w: innerWidth, h: innerHeight };
    });
    ok(popBox.left >= 0 && popBox.right <= popBox.w && popBox.bottom <= popBox.h,
      'and its reading opens inside the screen', JSON.stringify(popBox));
    await wait(300);
    await shot(s.page, 'options-usage-phone');
    await s.page.keyboard.press('Escape');

    // The worst a reading can be, on the narrowest phone in use: three full
    // windows, each days from renewing, and a cost. The strip tightens
    // itself, and the countdowns of the limits are the last thing it gives up.
    failing = false;
    const far = new Date(Date.now() + ((6 * 24 + 23) * 60 + 30) * 60000).toISOString();
    await s.page.unroute('**/api/sessions/*/usage');
    await s.page.route('**/api/sessions/*/usage', (route) => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        windows: [
          { label: '5h', used_percent: 100, resets_at: new Date(Date.now() + (4 * 60 + 59) * 60000).toISOString() },
          { label: '7d', used_percent: 100, resets_at: far },
          { label: 'Fable', used_percent: 100, resets_at: far },
        ],
        cost_usd: 1234.5,
      }),
    }));
    await s.page.setViewportSize({ width: 360, height: 740 });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForFunction(() => /Fable\s*100%/.test(document.getElementById('sessionUsage')?.innerText || ''),
      null, { timeout: 20000 });
    await wait(300);
    const worst = await s.page.evaluate(() => {
      const strip = document.getElementById('sessionUsage');
      return {
        fits: strip.scrollWidth <= strip.clientWidth + 1,
        level: strip.className,
        text: [...strip.querySelectorAll('.usage-part')].filter((p) => p.checkVisibility())
          .map((p) => p.innerText.replace(/\s+/g, ' ')).join(' · '),
        overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      };
    });
    ok(worst.fits && worst.overflow <= 1 && (worst.text.match(/\(\d/g) || []).length === 3,
      'the fullest reading still fits a 360px phone with all three countdowns', JSON.stringify(worst));
    await shot(s.page, 'options-usage-worst');

    // The options menu on the phone, too.
    await openOptions(s.page);
    const menuBox = await s.page.$eval('.menu', (n) => {
      const r = n.getBoundingClientRect();
      return { left: r.left, right: r.right, bottom: r.bottom, w: innerWidth, h: innerHeight, scroll: n.scrollHeight > n.clientHeight + 1 };
    });
    ok(menuBox.left >= 0 && menuBox.right <= menuBox.w && menuBox.bottom <= menuBox.h && !menuBox.scroll,
      'the options menu opens whole inside a phone’s screen', JSON.stringify(menuBox));
    await wait(300);
    await shot(s.page, 'options-menu-phone');
    await closeOptions(s.page);
    await s.page.unroute('**/api/sessions/*/usage');

    ok(id && unexpected(s.errors, OFFLINE_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, OFFLINE_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------------- run

const ALL = [
  ['createshell', 'the sheet makes a Shell session and the shell answers', createshell],
  ['typeandsee', 'keystrokes reach the pane and the journal agrees with the screen', typeandsee],
  ['reloadkeepsscreen', 'a reload keeps the screen and the input path', reloadkeepsscreen],
  ['pages', 'every page is clean at a phone and at a desk', pages],
  ['harnesses', 'all four session types start and are seen in the browser', harnesses],
  ['browserlayout', 'popup placement and bundled terminal font metrics', browserlayout],
  ['sessionlist', 'rename, archive, unarchive and delete', sessionlist],
  ['folders', 'the list is folders per project, shut and reopened, and a row keeps its element', folders],
  ['exitoverlay', 'a pane that ends, its status under the sentence, and Restart', exitoverlay],
  ['webglrenders', 'the shipped renderer paints the terminal', webglrenders],
  ['touchscroll', 'a finger scrolls the pane, and a tap is still a tap', touchscroll],
  ['keybar', 'a touch screen gets the key bar unasked, a desk does not, and it sends the right bytes', keybar],
  ['offlineonce', 'a line typed with no network arrives exactly once', offlineonce],
  ['sigtermreattach', 'a restarted server reattaches to the pane that kept running', sigtermreattach],
  ['takeover', 'a second tab with the same viewer id takes the pane over', takeover],
  ['offlinerestart', 'a restart during an outage, a wake storm, and nothing lost in silence', offlinerestart],
  ['latehello', 'what is typed before a late hello is delivered, exactly once', latehello],
  ['adminoptions', 'the basics round-trip and the fixed policy reaches the command line', adminoptions],
  ['tmuxcard', 'the engine card on a machine whose tmux is too old', tmuxcard],
  ['twoviewers', 'two devices on one session, and one notice about the size', twoviewers],
  ['backpressure', 'two hundred lines arrive whole, on screen and in the journal', backpressure],
  ['deletekeepsdir', 'delete kills the tmux session and keeps the work', deletekeepsdir],
  ['recoveredsession', 'a tmux session with no row is taken in, never killed', recoveredsession],
  ['createclaude', 'Claude Code from the sheet to the command line, and back after an exit', createclaude],
  ['createcodex', 'Codex is trusted where it works, and its conversation is found', createcodex],
  ['profiles', 'a second set of sign-ins, from the dashboard to the command line', profiles],
  ['createopencode', 'OpenCode names its session over its own authenticated server', createopencode],
  ['rebootresume', 'a machine that rebooted, and the session that comes back with its conversation', rebootresume],
  ['lighttheme', 'the white terminal, from the launch flag to the drawn pixel', lighttheme],
  ['activity-claude', 'Claude Code working, finishing, and a name that goes bold', activityclaude],
  ['activity-codex', 'Codex working, finishing, and a name that goes bold', activitycodex],
  ['activity-opencode', 'OpenCode working, finishing, and a name that goes bold', activityopencode],
  ['tasks-claude', 'Claude Code done with its turn, a shell still running: an hourglass, then bold', taskclaude],
  ['tasks-codex', 'Codex done with its turn, a background terminal still running', taskcodex],
  ['tasks-opencode', 'OpenCode done with its turn, a background sub-agent still running', taskopencode],
  ['activity-waiting', 'a permission prompt: a still amber ring, and no timeout', activitywaiting],
  ['activity-fallback', 'a harness that hangs, and the row that leaves busy anyway', activityfallback],
  ['unread', 'bold when nobody saw it, gone when the row is opened or typed into', unread],
  ['notify', 'a chime and a notification when a session stops working, and two switches in the options menu', notify],
  ['session-title', 'a session that names itself on its first prompt', sessiontitle],
  ['status-speak', 'Status says what the screen shows, on the page and out loud', statusspeak],
  ['status-ticker', 'the phases of a status, in order, in one line', statusticker],
  ['dictate', 'a recording with two endings, and the words it kept on the prompt', dictate],
  ['no-overlap', 'the microphone and the voice are never open at the same time', nooverlap],
  ['typeafteroutage', 'a cut socket, a locked phone, and a pane that still takes keystrokes', typeafteroutage],
  ['typekeepsfocus', 'a dialog, the ⋮ menu and two sessions: the keys still land in the pane', typekeepsfocus],
  ['keyboard', 'on a touch screen only the button raises the keyboard, in every dialog too', keyboard],
  ['options', 'the ⋮ is this device\u2019s options, and the limits say when they renew', optionsmenu],
  ['design', 'white surfaces, marks, hover-only detail and motion that does not restart', design],
  ['clipboard', 'copy and paste: the keys, a plain drag, a click, a middle click, and OSC 52', clipboard],
  ['touchcopy', 'a finger held on the pane selects, grows and copies', touchcopy],
  ['transfer', 'copy, paste and upload buttons, a pasted image, and the phone layout', transfer],
  ['prestart', 'a prepared session: its prompt kept and backed up, Start, and a start by the clock', prestart],
  ['s3restore', 'a machine that is gone, and a new data directory that comes back from the bucket', s3restore],
  ['storagebanner', 'the bucket unreachable, then taken over: what the page says, and that it leaves', storagebanner],
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
