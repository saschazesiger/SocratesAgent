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
  readFakeLog, killTmux, openRouterStub, fakeS3Switch, backupForm, prefixFor, sessionsOn, windowSize, scratchDir, PASSWORD, LIVE, SHOTS,
  tmuxSock, DIGEST_MARKER, freePort, spawnServer, waitForHealth, REPO,
} from './harness.mjs';
import http from 'node:http';
import net from 'node:net';
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import { execFileSync } from 'node:child_process';
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
  // Attached: on a phone it is in the modal drawer, hidden until opened.
  await s.page.waitForSelector('#newSession', { state: 'attached', timeout: 15000 });
}

// reloadBack reloads the tab and goes back to the session it was on.
//
// A reload is a start of the page like any other, and every start lands on
// Home (HOME.md §2.3: the owner's choice). What a scenario wants from the
// reload - the same viewer, the same screen, a setting kept - is then one
// step away, and the step is taken the way a link to the session takes it:
// through the address, once the list has the row.
async function reloadBack(page, id) {
  const want = id || await page.evaluate(() => location.hash.slice(1));
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
  await page.waitForSelector('#sessionList .so-row[data-id="' + want + '"]', { state: 'attached', timeout: 20000 });
  await page.evaluate((target) => { location.hash = '#' + target; }, want);
}

// startSession drives the sheet the way a person would and waits until the
// session it made is the one on screen.
async function startSession(page, harness) {
  // The hash already names whatever session this tab is on, so the wait below
  // is for it to become a different one: a second session started in the same
  // tab is otherwise "finished" before it has begun, and the id handed back is
  // the previous session's.
  const was = await page.evaluate(() => location.hash.slice(1));
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.waitForSelector('#newSessionSheet .so-settings__program[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#newSessionSheet .so-settings__program[data-value="' + harness + '"]');
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
  await page.click('#nsModel .ds-combo__input');
  await page.waitForSelector('#nsModel .ds-combo__option', { timeout: 5000 });
  await page.click('#nsModel .ds-combo__option');
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

// The top bar's ⋮ is this device's options: switches in a dialog, which stays
// open while they are pressed. openOptions opens it when it is not open.
const OPTIONS = '#optionsDialog[data-state="open"]';
async function openOptions(page) {
  if (await page.$(OPTIONS + ' [data-option]')) return;
  await page.click('#optionsBtn');
  await page.waitForSelector(OPTIONS + ' [data-option]', { timeout: 5000 });
}

// option presses one switch of the options dialog and answers where it now
// is. A switch that waits on the browser - notifications - is waited for by
// the caller.
async function option(page, name) {
  await openOptions(page);
  await page.click(OPTIONS + ' .ds-switch:has([data-option="' + name + '"])');
  return page.$eval(OPTIONS + ' [data-option="' + name + '"]', (n) => n.checked);
}

// options is every switch of the dialog as it is drawn: on or off, whether it
// can be pressed, and the words of its field.
async function options(page) {
  await openOptions(page);
  return page.$$eval(OPTIONS + ' [data-option]', (nodes) => Object.fromEntries(nodes.map((n) => [n.dataset.option, {
    on: n.checked,
    disabled: n.disabled,
    text: (n.closest('.ds-field') || n).textContent.replace(/\s+/g, ' ').trim(),
  }])));
}

// closeOptions puts the dialog away the way a key does.
async function closeOptions(page) {
  if (!(await page.$('#optionsDialog[open]'))) return;
  await page.keyboard.press('Escape');
  await page.waitForSelector('#optionsDialog[open]', { state: 'detached', timeout: 5000 });
}

// The row menu: one DS menu, drawn for the row that opened it.
const MENU = '#sessionMenu:not([hidden])';

// rowMenu opens the ⋮ of the session on screen, in the list - where what can
// be done to a session is, now that the top bar's ⋮ is this device's.
async function rowMenu(page) {
  await ensureNav(page);
  const id = await page.evaluate(() => location.hash.slice(1));
  await page.click('#sessionList .so-row[data-id="' + id + '"] .so-row__more', { force: true });
  await page.waitForSelector(MENU + ' .ds-menu__item', { timeout: 5000 });
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

    const id = await startSession(s.page, 'shell');
    ok(!!id, 'the sheet made a session and opened it', id || 'no id in the hash');

    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const marker = 'socrates-' + Date.now();
    await typeLine(s.page, 'echo ' + marker);
    const echoed = await awaitScreen(s.page, marker);
    ok(echoed, 'a command typed in the browser was run by the shell', oneLine(await screen(s.page)));

    // §E.10 rule 1 in DS's terms: the work sits on one white panel, the
    // terminal included (the frame around it is DS's tinted backdrop).
    const colours = await s.page.evaluate(() => {
      const bg = (sel) => {
        const node = document.querySelector(sel);
        return node ? getComputedStyle(node).backgroundColor : 'missing';
      };
      return {
        panel: bg('.ds-panel'),
        wrap: bg('.so-pane'),
        viewport: bg('#term .xterm-viewport'),
        screen: bg('#term .xterm-screen'),
      };
    });
    ok(colours.panel === WHITE && Object.values(colours).every((c) => c === WHITE || c === 'rgba(0, 0, 0, 0)'),
      'the panel and the terminal are the same white', JSON.stringify(colours));

    // Readability is checked by measurement, not by eye - and on what was
    // actually drawn rather than on the table it was drawn from. ANSI "white"
    // is #dcdde1 in the palette, which is 1.36:1 against a white page; what
    // makes it legible is `minimumContrastRatio: 4.5`, which re-derives it at
    // draw time. So the thing worth asserting is the drawn colour.
    await typeLine(s.page, "printf '\\033[37mDIMWHITE\\033[0m\\n'");
    ok(await awaitScreen(s.page, 'DIMWHITE'), 'the pane printed a line in ANSI white',
      oneLine(await screen(s.page)));
    // The echo of the command already holds the word, so the wait above can
    // end before the line the command printed is drawn: wait for that line's
    // own span (below) before measuring it.
    await s.page.waitForFunction(() => [...document.querySelectorAll('#term .xterm-rows span')]
      .some((n) => n.textContent.trim() === 'DIMWHITE'), null, { timeout: 10000 }).catch(() => {});
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
      mark: !!document.querySelector('.ds-topbar .so-agent'),
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
    // A reload lands on Home, like every start of the page; the session is
    // then opened again from the address.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    const landed = await s.page.evaluate(() => location.hash);
    ok(landed === '#home', 'the reload lands on Home', landed);
    await s.page.waitForSelector('#sessionList .so-row[data-id="' + id + '"]', { state: 'attached', timeout: 20000 });
    await s.page.evaluate((target) => { location.hash = '#' + target; }, id);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const after = await s.page.evaluate(() => sessionStorage.getItem('socrates.viewer'));
    ok(before && before === after, 'the reloaded tab is the same viewer', before === after ? before : before + ' -> ' + after);

    ok(await awaitScreen(s.page, marker), 'the same screen came back after the reload',
      oneLine(await screen(s.page)));
    const hash = await s.page.evaluate(() => location.hash.slice(1));
    ok(hash === id, 'and opening it again is the same session', hash);

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

      // A chat is the page's other kind of session: clean at both sizes too,
      // with an answer on screen.
      s.errors.length = 0;
      await open(s);
      const chat = await startChat(s.page, 'claude');
      const answered = await answerTo(s, chat, 'hello from ' + tag);
      await wait(600);
      const chatBad = unexpected(s.errors);
      const chatOverflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      ok(answered === 'you said: hello from ' + tag && chatBad.length === 0,
        `a chat at ${tag} answers, with no console errors`, chatBad.join(' | ') || answered);
      ok(chatOverflow <= 1, `a chat at ${tag} does not scroll sideways`, chatOverflow + 'px');
      await shot(s.page, 'pages-chat-' + tag);

      // The sheet is DS's side sheet: docked to the right edge at full height
      // on a desk, risen from the bottom at full width on a phone (600px and
      // below). The <dialog> always covers the window, so the panel is what
      // is measured, once its slide has settled.
      await open(s);
      await ensureNav(s.page);
      await s.page.click('#newSession');
      await s.page.waitForSelector('#newSessionSheet[open]');
      await wait(900);
      const rect = await s.page.$eval('#newSessionSheet .ds-sheet__panel', (n) => n.getBoundingClientRect().toJSON());
      if (viewport.width > 600) {
        ok(Math.abs(rect.right - viewport.width) <= 1 && rect.left > viewport.width / 3
          && Math.abs(rect.height - viewport.height) <= 2,
          `the sheet is a side sheet at the right edge at ${tag}`, JSON.stringify(rect));
      } else {
        ok(rect.left <= 1 && Math.round(rect.width) >= viewport.width - 2
          && Math.abs(rect.bottom - viewport.height) <= 2,
          `the sheet is a full-width bottom sheet at ${tag}`, JSON.stringify(rect));
      }
      // Every harness with its own mark: §E.10 rule 2. The agents come first
      // and the shell, which is not one, is the row beneath them. VS Code is
      // not on the sheet: a folder's editor is opened from the folder.
      const marks = await s.page.$$eval('#nsHarness .so-settings__program .so-agent, #nsTools .so-settings__program .so-agent',
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
        const input = host.querySelector('.ds-combo__input');
        const box = host.getBoundingClientRect();
        return {
          combo: !!host.querySelector('.ds-combo'),
          segs: host.querySelectorAll('.so-settings__program, .ds-chip').length,
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

// All four kinds of session, made through the sheet and seen in the browser:
// a Shell, which is a terminal and runs a command, and a chat with each agent
// - Claude Code, Codex and OpenCode, the scripted protocol fakes on PATH -
// whose CLI answers its first message over its own protocol. Every row wears
// the mark of what it runs, and the facts of a session are behind Info.
async function harnesses() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const marker = 'shell-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + marker);
    ok(await awaitScreen(s.page, marker), 'the Shell is a terminal, and its shell ran a command',
      oneLine(await screen(s.page)).slice(-80));

    const chats = {};
    for (const harness of AGENTS) {
      const id = await startChat(s.page, harness);
      chats[harness] = id;
      await say(s.page, 'hello ' + harness);
      const answered = await awaitLog(s.page, 'you said: hello ' + harness, 20000);
      const body = await awaitChat(s, id, (b) => !!b.chat.conversation_id && idle(b));
      ok(body.session.kind === 'chat' && body.session.harness === harness && answered,
        harness + ' is a chat, and its CLI answers the first message over its own protocol',
        JSON.stringify({ kind: body.session.kind, answer: oneLine(await lastAnswer(s.page)) }));
      ok(!!body.chat.conversation_id, 'and the chat knows the conversation the CLI keeps', body.chat.conversation_id);
    }

    // Four rows, each with the mark of what it runs: §E.10 rule 2 again, in
    // the list this time.
    const rows = await s.page.$$eval('#sessionList .so-row', (nodes) => nodes.map((n) => ({
      mark: (n.querySelector('.so-agent') || { dataset: {} }).dataset.agent,
      dots: n.querySelectorAll('.dot').length,
      tips: n.querySelectorAll('.so-info').length,
      words: n.querySelector('.so-row__label').textContent,
    })));
    ok(rows.length === 4, 'all four sessions are in the list', rows.length + ' rows');
    ok(rows.map((r) => r.mark).sort().join(',') === 'claude,codex,opencode,shell',
      'every row carries the mark of what it runs', rows.map((r) => r.mark).join(','));
    // A row is a mark, a name and its menu. The state dot said nothing that
    // the mark and the words do not, and the "i" moved into the menu.
    ok(rows.every((r) => r.dots === 0 && r.tips === 0), 'no row carries a state dot or an "i"',
      rows.map((r) => r.dots + '/' + r.tips).join(','));
    // §E.10 rule 3: the technical detail is behind Info, never in the words.
    ok(rows.every((r) => !r.words.includes('/')), 'no row spells out a path in its words',
      rows.map((r) => r.words).join(' | '));

    // Info is where the facts of a session are: one dialog, opened from the
    // row's own menu - for a chat its folder and the account it runs on.
    const row = await sessionRow(s, chats.claude);
    await s.page.click(rowSel(chats.claude) + ' .so-row__more', { force: true });
    await s.page.click('#sessionMenu [data-action="info"]');
    await s.page.waitForSelector('dialog.so-info-dialog .so-facts', { timeout: 5000 });
    const facts = await s.page.$$eval('dialog.so-info-dialog .so-fact', (nodes) => nodes.map((f) => f.textContent));
    ok(facts.includes(row.workdir) && facts.some((f) => /^Account: Default$/.test(f)),
      'the working directory and the account are in the Info dialog', oneLine(facts.join(' · ')));
    await s.page.click('dialog.so-info-dialog .ds-dialog__actions .ds-btn');
    await s.page.waitForSelector('dialog.so-info-dialog', { state: 'detached', timeout: 5000 });

    await shot(s.page, 'harnesses');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
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
    const row = '#sessionList .so-row[data-id="' + id + '"]';
    const geometry = () => s.page.evaluate((sel) => {
      const box = document.querySelector(sel + ' .so-row__more').getBoundingClientRect();
      const menu = document.getElementById('sessionMenu');
      const rect = menu.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right,
        anchorTop: box.top, anchorBottom: box.bottom, height: innerHeight, width: innerWidth,
        scrollable: menu.scrollHeight > menu.clientHeight };
    }, row);
    await s.page.click(row + ' .so-row__more');
    await wait(180);
    let g = await geometry();
    ok(g.top >= g.anchorBottom && g.bottom <= g.height,
      'an upper row opens downwards inside the viewport', JSON.stringify(g));
    await s.page.mouse.click(750, 75);
    await wait(300);
    // A row near the bottom of the window: the window is made short enough
    // that the room below the row is smaller than the room above it. (The
    // list is morphed, so a style written on a row does not last.)
    const anchor = await s.page.$eval(row + ' .so-row__more', (n) => n.getBoundingClientRect().bottom);
    await s.page.setViewportSize({ width: 1280, height: Math.round(anchor) + 60 });
    await wait(300);
    await s.page.click(row + ' .so-row__more');
    await wait(300);
    g = await geometry();
    // DS's placement: below unless it does not fit there and there is more
    // room above; either way inside the window and never over its button.
    const above = g.anchorTop, below = g.height - g.anchorBottom;
    ok(g.top >= 0 && g.bottom <= g.height
      && (above > below ? g.bottom <= g.anchorTop : g.top >= g.anchorBottom),
      'a lower row opens on the side with more room, inside the viewport', JSON.stringify(g));
    await s.page.mouse.click(750, 75);
    await wait(300);
    // Wider than the drawer breakpoint, so the row is in the docked drawer.
    await s.page.setViewportSize({ width: 1000, height: 240 });
    await wait(300);
    await s.page.click(row + ' .so-row__more');
    await wait(300);
    g = await geometry();
    ok(g.top >= 0 && g.bottom <= g.height && g.left >= 0 && g.right <= g.width && g.scrollable,
      'an open menu stays visible and scrollable in a short viewport', JSON.stringify(g));
    await s.page.$eval('#sessionMenu', (node) => { node.scrollTop = node.scrollHeight; });
    await wait(100);
    const scrolled = await s.page.$eval('#sessionMenu', (node) => node.scrollTop > 0
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
    // Only the terminal's own faces are counted: the page's DS fonts are
    // woff2 files too, and they do not decide a cell.
    await s.page.route('**/*.woff2*', async (route) => {
      await wait(400);
      await route.continue();
      if (/SourceCodePro/.test(route.request().url())) fontResponses++;
    });
    await reloadBack(s.page);
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

    const rowSelector = '#sessionList .so-row[data-id="' + id + '"]';
    const menu = async (label) => {
      await s.page.click(rowSelector + ' .so-row__more');
      await s.page.waitForSelector('#sessionMenu:not([hidden])', { timeout: 5000 });
      await s.page.click('#sessionMenu .ds-menu__item:has(.ds-menu__label:text-is("' + label + '"))');
    };

    // Rename.
    await menu('Rename');
    await s.page.waitForSelector('dialog.so-rename[open] .ds-input', { timeout: 5000 });
    await s.page.fill('dialog.so-rename[open] .ds-input', 'Renamed by the suite');
    await s.page.click('dialog.so-rename[open] .ds-btn--primary');
    await s.page.waitForFunction((sel) => {
      const row = document.querySelector(sel);
      return row && row.querySelector('.so-row__label').textContent === 'Renamed by the suite';
    }, rowSelector, { timeout: 8000 });
    const named = await s.page.$eval('#sessionTitle', (n) => n.textContent);
    ok(named === 'Renamed by the suite', 'the new name is in the row and in the header', named);
    const stored = await (await s.context.request.get(s.url + '/api/sessions/' + id)).json();
    ok(stored.session.title === 'Renamed by the suite', 'the rename was stored', stored.session.title);

    // The same menu for a row that has been filed onto a shelf, which starts
    // shut: the shelf is opened first, the way a person would find the row.
    const headMenu = async (label) => {
      const shelf = await s.page.$eval(rowSelector, (row) => (row.checkVisibility()
        ? '' : (row.closest('.so-folder--shelf') || { dataset: {} }).dataset.shelf || ''));
      if (shelf) {
        await s.page.click('.so-folder--shelf[data-shelf="' + shelf + '"] > .so-folder__bar > .so-folder__head');
        await s.page.waitForFunction((sel) => document.querySelector(sel)?.checkVisibility(),
          rowSelector, { timeout: 5000 });
      }
      await menu(label);
    };
    const statusShown = (sel) => s.page.evaluate((sel) => {
      const row = document.querySelector(sel);
      const shown = (node) => !!node && !node.hidden && getComputedStyle(node).display !== 'none';
      const mark = row && row.querySelector('.so-row__status');
      const head = document.getElementById('sessionStatus');
      return {
        row: shown(mark) ? mark.dataset.status : '',
        head: shown(head) ? head.textContent.trim() : '',
        folder: row ? row.closest('.so-folder:not(.so-folder--shelf)').dataset.folder || '' : '',
        shelf: row ? (row.closest('.so-folder--shelf') || { dataset: {} }).dataset.shelf || '' : '',
        folderOpen: !!row && row.checkVisibility(),
      };
    }, sel);
    const current = async () => (await (await s.context.request.get(s.url + '/api/sessions/' + id)).json()).session;

    // A session is born in progress, which carries no mark at all.
    const born = await statusShown(rowSelector);
    ok(!born.row && !born.head, 'a session is born in progress, with no mark', JSON.stringify(born));

    // Review pending: a mark after the name and a word in the header.
    await menu('Review pending');
    await s.page.waitForFunction((sel) => document.querySelector(sel + ' .so-row__status')?.dataset.status === 'review',
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
    await s.page.waitForFunction((sel) => document.querySelector(sel)?.closest('.so-folder--shelf')?.dataset.shelf === 'background',
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
    await s.page.click('#termOverlay .ds-btn--primary');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const back = await current();
    ok(back.status === 'in_progress', 'picking a done session up puts it back in progress', back.status);

    // Delete: the row goes, the tmux session goes, the directory stays.
    const workdir = back.workdir;
    await menu('Delete');
    await s.page.waitForSelector('dialog.ds-dialog[open] .ds-btn--primary', { timeout: 5000 });
    const body = await s.page.$eval('dialog.ds-dialog[open] .ds-dialog__text', (n) => n.textContent);
    ok(/working directory/i.test(body), 'the dialog says the working directory is kept', oneLine(body));
    await s.page.click('dialog.ds-dialog[open] .ds-btn--primary');
    await s.page.waitForFunction((sel) => !document.querySelector(sel), rowSelector, { timeout: 10000 });
    const left = await (await s.context.request.get(s.url + '/api/sessions')).json();
    ok((left.sessions || []).length === 0, 'the session is gone', (left.sessions || []).length + ' left');

    ok(existsSync(workdir), 'the working directory was kept', workdir);

    /* ------------------------------------------------------------ a chat */

    // A chat is a session like any other in the list: renamed and filed from
    // its row. Done puts its agent to rest, a message picks it up again, and
    // Delete takes the row, the agent and the chat's own folder, and leaves
    // the work it did on disk.
    const chat = await startChat(s.page, 'claude');
    const chatSel = rowSel(chat);
    const chatMenu = async (label) => {
      await s.page.click(chatSel + ' .so-row__more');
      await s.page.waitForSelector('#sessionMenu:not([hidden])', { timeout: 5000 });
      await s.page.click('#sessionMenu .ds-menu__item:has(.ds-menu__label:text-is("' + label + '"))');
    };
    await chatMenu('Rename');
    await s.page.waitForSelector('dialog.so-rename[open] .ds-input', { timeout: 5000 });
    await s.page.fill('dialog.so-rename[open] .ds-input', 'A chat renamed by the suite');
    await s.page.click('dialog.so-rename[open] .ds-btn--primary');
    const renamedChat = await s.page.waitForFunction((sel) => {
      const row = document.querySelector(sel);
      return !!row && row.querySelector('.so-row__label').textContent === 'A chat renamed by the suite'
        && document.getElementById('sessionTitle').textContent === 'A chat renamed by the suite';
    }, chatSel, { timeout: 8000 }).then(() => true, () => false);
    ok(renamedChat, 'a chat is renamed from its row, in the row and in the header', String(renamedChat));
    ok((await answerTo(s, chat, 'before it is done')) === 'you said: before it is done', 'the chat has worked', 'before it is done');
    await chatMenu('Done');
    const parked = await until(() => hostsOf(s.data).length === 0, 20000);
    const doneChat = (await (await s.context.request.get(s.url + '/api/sessions/' + chat)).json()).session;
    ok(parked && doneChat.status === 'done' && doneChat.delete_at > Date.now(),
      'Done puts the chat’s agent to rest, and keeps the chat for a while',
      JSON.stringify({ hosts: hostsOf(s.data).length, status: doneChat.status, delete_at: doneChat.delete_at }));
    ok((await answerTo(s, chat, 'picked up again')) === 'you said: picked up again', 'a message to it is answered', 'picked up again');
    const pickedUp = (await (await s.context.request.get(s.url + '/api/sessions/' + chat)).json()).session;
    ok(pickedUp.status === 'in_progress', 'and puts it back in progress', pickedUp.status);
    const chatDir = pickedUp.workdir;
    await chatMenu('Delete');
    await s.page.waitForSelector('dialog.ds-dialog[open] .ds-btn--primary', { timeout: 5000 });
    await s.page.click('dialog.ds-dialog[open] .ds-btn--primary');
    await s.page.waitForFunction((sel) => !document.querySelector(sel), chatSel, { timeout: 10000 });
    const gone = await until(() => hostsOf(s.data).length === 0 && !existsSync(join(s.data, 'chats', chat)), 15000);
    ok(gone, 'deleting it takes its agent and the chat’s own folder',
      JSON.stringify({ hosts: hostsOf(s.data).length, folder: existsSync(join(s.data, 'chats', chat)) }));
    ok(existsSync(chatDir), 'and keeps the folder it worked in', chatDir.replace(s.data, '<data>'));

    await shot(s.page, 'sessionlist');
    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ----------------------------------------------------------- 7. exitoverlay */

// A pane that ends is not a page that breaks: the overlay says so, the exit
// status is a plain line under the sentence, and Restart brings the session
// back on the same row. The program is a Shell, which a person ends with
// `exit`.
async function exitoverlay() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    // What the shell says is read out of the journal rather than off the
    // screen, which an attach redraw can reflow. The sum is only ever in
    // the shell's answer, never in what was typed.
    await typeLine(s.page, 'echo life-$((6*7))');
    ok(await journalSays(s, id, 'life-42', 1), 'the session is up',
      oneLine(await journalOf(s, id)));

    await typeLine(s.page, 'exit 7');
    await s.page.waitForSelector('#termOverlay[data-state="open"] .ds-dialog__title', { timeout: 20000 });
    const overlay = await s.page.evaluate(() => {
      const card = document.querySelector('#termOverlay .ds-dialog__box');
      return {
        words: card.querySelector('.ds-dialog__title').textContent,
        detail: (card.querySelector('.so-pane__detail') || {}).textContent || '',
        tips: card.querySelectorAll('.so-info').length,
        // A button with faces holds its busy words too; its label is the
        // resting face.
        buttons: [...card.querySelectorAll('.ds-dialog__actions .ds-btn')]
          .map((b) => (b.querySelector('.ds-btn__face') || b).textContent.trim()).sort(),
      };
    });
    ok(/The session ended/.test(overlay.words), 'the overlay says the session ended', oneLine(overlay.words));
    ok(/Exit status 7/.test(overlay.detail) && overlay.tips === 0,
      'the exit status is a plain line under it, and there is no "i" left',
      JSON.stringify(overlay));
    ok(overlay.buttons.join(',') === 'Delete,Restart', 'the overlay offers Restart and Delete',
      overlay.buttons.join(','));
    await shot(s.page, 'exitoverlay');

    const said = await s.page.$eval('#sessionList .so-row .so-mark', (n) => n.getAttribute('data-tip'));
    ok(said === 'Ended', 'the row says the session ended', said);

    await s.page.click('#termRestart');
    await s.page.waitForFunction(() => {
      const overlayNode = document.getElementById('termOverlay');
      return overlayNode && overlayNode.hidden;
    }, null, { timeout: 30000 });
    // Twice now: the journal is one file across the restart, so a second
    // answer is the proof that a second shell was started into the pane.
    await typeLine(s.page, 'echo life-$((6*7))');
    ok(await journalSays(s, id, 'life-42', 2), 'Restart brought the session back',
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
// list, and the model and effort a chat starts on. Set it in the page, save,
// reload, and find it still there - then start a Claude Code chat in a preset
// directory and ask the CLI what it was started with.
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

// The flags every chat of Claude Code is started with, whatever anybody
// configured, and the ones no chat may ever carry. This is the browser-side
// half of docs/design/HARNESS-POLICY.md, as REBUILD.md §3.1 has it for
// `claude -p`.
const FIXED_POLICY = {
  claude: {
    must: ['-p', '--input-format stream-json', '--output-format stream-json', '--permission-prompt-tool stdio',
      '--setting-sources project,local', '--strict-mcp-config', '--dangerously-skip-permissions',
      '--disallowedTools AskUserQuestion', '--settings'],
    never: ['--remote-control', '--permission-mode', '--allow-dangerously-skip-permissions', '--mcp-config', '--continue'],
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
    const row = '#presetDirs .so-preset:last-child';
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
    await s.page.waitForSelector('.ds-toast', { timeout: 15000 });
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
      preset: document.querySelector('#presetDirs .so-preset input:nth-child(2)')?.value || '',
    }));
    ok(terminal.windowSize === 'largest' && terminal.history === '31000',
      'the terminal card round-trips too', JSON.stringify(terminal));
    ok(terminal.preset === preset, 'the preset directory was stored', terminal.preset);

    // §E.10 rule 3, in the setup check: the row says a verdict, the path and
    // the version are behind its "i".
    await s.page.click('#runChecks');
    await s.page.waitForSelector('#checks .so-check', { timeout: 60000 });
    const rows = await s.page.$$eval('#checks .so-check', (nodes) => nodes.map((node) => ({
      name: node.querySelector('.so-check__label').textContent.trim(),
      visible: node.querySelector('.so-check__summary').textContent.trim(),
      tip: !!node.querySelector('.so-info'),
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
    await s.page.click('#nsDir .ds-combo__input');
    const cell = '#nsDir .ds-combo__option[data-value="preset:' + preset + '"]';
    await s.page.waitForSelector(cell, { timeout: 5000 }).catch(() => {});
    const offered = await s.page.$(cell);
    ok(!!offered, 'the new-session sheet offers the preset directory', cell);
    if (offered) {
      const hint = await s.page.$eval(cell, (n) => (n.querySelector('.ds-combo__meta') || {}).textContent || '');
      ok(hint === preset, 'and its full path is the sub-text under its name', hint);
      await s.page.click(cell);
      await s.page.waitForFunction(() =>
        document.getElementById('nsDirHint').textContent.length > 0, null, { timeout: 5000 });
    }
    await s.page.click('#newSessionSheet .so-settings__program[data-value="claude"]');
    const model = await s.page.$eval('#nsModel .ds-combo__input', (n) => n.value.trim());
    ok(/opus/i.test(model), 'the sheet starts Claude Code on the default model', model);
    await s.page.click('#nsStart');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await chatUp(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));
    const made = await sessionRow(s, id);
    ok(made.workdir === preset, 'the chat works in the preset directory', made.workdir);

    // What reached `claude -p` is asked of the CLI itself.
    const argv = await answerTo(s, id, '/argv');
    const policy = FIXED_POLICY.claude;
    const missing = policy.must.filter((flag) => !(' ' + argv + ' ').includes(' ' + flag + ' '));
    ok(missing.length === 0, 'the fixed policy reached the command line', missing.join(', ') || policy.must.join(' '));
    const forbidden = policy.never.filter((flag) => (' ' + argv + ' ').includes(' ' + flag + ' '));
    ok(forbidden.length === 0, 'and nothing the dashboard no longer offers reached it either',
      forbidden.join(', ') || 'none of ' + policy.never.join(' '));
    ok(/ --model opus /.test(' ' + argv + ' ') && / --effort high /.test(' ' + argv + ' '),
      'the default model and effort reached it', (/--model \S+ --effort \S+/.exec(argv) || ['none'])[0]);
    // The settings are on the command line, not in a file: Remote Control is
    // off where the flag cannot say it, and the bypass dialog never shows.
    let settings = {};
    try { settings = JSON.parse((/--settings (\{[^}]*\})/.exec(argv) || [])[1] || '{}'); } catch { /* reported below */ }
    ok(settings.disableRemoteControl === true && settings.skipDangerousModePermissionPrompt === true,
      'its settings disable Remote Control and the bypass dialog', JSON.stringify(settings));
    const env = await answerTo(s, id, '/env');
    ok(env.includes('CLAUDE_CODE_ENABLE_TODO_TOOLS=1') && !/CLAUDE_CODE_NO_FLICKER|CLAUDE_CODE_DISABLE_TERMINAL_TITLE|CLAUDE_REMOTE_CONTROL|ANTHROPIC_/.test(env),
      'its environment is the chat’s: the task tools on, nothing of a terminal and no key of a subscription',
      oneLine(env));
    const transcript = join(s.data, 'agents', 'claude', 'projects', preset.replace(/[^A-Za-z0-9]/g, '-'));
    ok(existsSync(transcript), 'and Claude Code ran there: its transcript is the preset’s project', transcript.replace(s.data, '<data>'));

    ok(unexpected(s.errors).length === 0, 'no console errors',
      unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ----------------------------------------------------------- 11. tmuxcard */

// The engine card against a machine whose tmux is too old. There is no
// installer in the app - scripts/install.sh installs tmux - so all the card
// owes anybody is an honest, actionable sentence.
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
    await s.page.waitForSelector('#tmuxCard .ds-status', { timeout: 20000 });
    await s.page.waitForFunction(
      () => !/Loading/.test(document.querySelector('#tmuxStatus .ds-status').textContent),
      null, { timeout: 15000 });

    const card = await s.page.evaluate(() => ({
      chip: document.querySelector('#tmuxStatus .ds-status').className
        + ' ' + document.querySelector('#tmuxStatus .ds-status').dataset.state,
      label: document.querySelector('#tmuxStatus .ds-status').textContent,
      detail: document.querySelector('#tmuxStatus').textContent,
      reason: document.getElementById('tmuxReason').textContent,
    }));
    // The checklist item: 3.2a is amber and "too old", never green and "ok".
    ok(/\bold\b/.test(card.chip) && /ds-status--warning/.test(card.chip) && /too old/i.test(card.label),
      'tmux 3.2a is reported as too old, not as ok', card.chip + ' \u00b7 ' + card.label);
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
    // The terminal's sockets only: the event stream is a socket of its own,
    // and its handshakes and closes are not what these scenarios count.
    window.WebSocket = new Proxy(window.WebSocket, {
      construct(target, args) {
        if (!String(args[0]).includes('/ws?')) return new target(...args);
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
    await deskPage.goto(s.url + '/#open=' + openId, { waitUntil: 'domcontentloaded' });
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

    const keys = await s.page.$$eval('#keybar .so-key', (nodes) => nodes.map((n) => n.dataset.key));
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
      await s.page.click('#keybar .so-key[data-key="' + name + '"]');
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
    await s.page.click('#keybar .so-key[data-key="Control"]');
    const armed = await s.page.$eval('#keybar .so-key[data-key="Control"]', (n) => (n.getAttribute('aria-pressed') === 'true' ? 'on' : 'off') + (n.hasAttribute('data-lock') ? ' lock' : ''));
    ok(/\bon\b/.test(armed) && !/\block\b/.test(armed), 'one tap arms Ctrl', armed);
    const beforeCtrl = (await sentBytes(s.page)).length;
    await s.page.keyboard.type('c');
    await wait(200);
    const afterCtrl = (await sentBytes(s.page)).slice(beforeCtrl);
    ok(afterCtrl.includes('03'), 'the next letter typed is sent as Ctrl-C', afterCtrl.join(',') || 'nothing');
    const disarmed = await s.page.$eval('#keybar .so-key[data-key="Control"]', (n) => (n.getAttribute('aria-pressed') === 'true' ? 'on' : 'off') + (n.hasAttribute('data-lock') ? ' lock' : ''));
    ok(!/\bon\b/.test(disarmed), 'and the modifier disarmed itself', disarmed);

    // A physical key was just pressed, and that is not a decision about the
    // bar any more: it stays exactly where it was asked to be.
    ok(!(await s.page.$eval('#keybar', (n) => n.hidden)),
      'a keystroke on a real keyboard does not take the bar away', 'still on screen');

    // A second tap locks it, and a locked modifier keeps transforming.
    await s.page.click('#keybar .so-key[data-key="Control"]');
    await s.page.click('#keybar .so-key[data-key="Control"]');
    const locked = await s.page.$eval('#keybar .so-key[data-key="Control"]', (n) => (n.getAttribute('aria-pressed') === 'true' ? 'on' : 'off') + (n.hasAttribute('data-lock') ? ' lock' : ''));
    ok(/\block\b/.test(locked), 'a second tap locks Ctrl', locked);
    const beforeLock = (await sentBytes(s.page)).length;
    await s.page.keyboard.type('ab');
    await wait(200);
    const two = (await sentBytes(s.page)).slice(beforeLock).filter((hex) => hex.length === 2);
    ok(two.join(',') === '01,02', 'a locked Ctrl transforms every key after it', two.join(',') || 'nothing');
    await s.page.click('#keybar .so-key[data-key="Control"]');
    ok(!/\b(on|lock)\b/.test(await s.page.$eval('#keybar .so-key[data-key="Control"]', (n) => (n.getAttribute('aria-pressed') === 'true' ? 'on' : 'off') + (n.hasAttribute('data-lock') ? ' lock' : ''))),
      'a third tap puts it away', 'off');

    // A modifier is for the next key the person presses, and the terminal's
    // own reports are not keys. Leaving the pane for a control in the bar -
    // the options menu, which is about this device and nothing about the pane
    // - and coming back to it through the top bar's keyboard button, both
    // make xterm send a focus report on the same channel, and neither may
    // spend an armed Ctrl.
    // The options are a modal dialog: it is put away first, and the Ctrl
    // armed after it.
    await openOptions(s.page);
    await closeOptions(s.page);
    await s.page.click('#keybar .so-key[data-key="Control"]');
    const beforeKeyboard = (await sentBytes(s.page)).length;
    await s.page.click('#kbBtn');
    await wait(200);
    const stillArmed = await s.page.$eval('#keybar .so-key[data-key="Control"]', (n) => (n.getAttribute('aria-pressed') === 'true' ? 'on' : 'off') + (n.hasAttribute('data-lock') ? ' lock' : ''));
    ok(/\bon\b/.test(stillArmed), 'raising the keyboard does not spend the armed Ctrl', stillArmed);
    await s.page.keyboard.type('c');
    await wait(250);
    const throughFocus = (await sentBytes(s.page)).slice(beforeKeyboard);
    ok(throughFocus.includes('03'), 'and the letter after it is still sent as Ctrl-C',
      throughFocus.join(',') || 'nothing');

    // A locked Alt must not put an ESC in front of what the terminal answers.
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    await s.page.click('#keybar .so-key[data-key="Alt"]');
    await s.page.click('#keybar .so-key[data-key="Alt"]');
    const beforeBlur = (await sentBytes(s.page)).length;
    // Out of the pane to the options menu and back to it with Escape, which
    // is the menu's and not the pane's.
    await openOptions(s.page);
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
      armed: [...document.querySelectorAll('#keybar .so-key[aria-pressed="true"], #keybar .so-key[data-lock]')].length,
      stored: localStorage.getItem('socrates.term.keybar'),
    }));
    ok(hidden.bar && hidden.armed === 0, 'hiding the key bar disarms it', JSON.stringify(hidden));
    ok(hidden.stored === 'off', 'and the answer is written down for this device', String(hidden.stored));

    // Asked for, it is asked for until it is not: a reload comes back to a
    // bar rather than to a decision made again.
    await option(s.page, 'keybar');
    await closeOptions(s.page);
    await s.page.waitForSelector('#keybar:not([hidden])', { timeout: 5000 });
    await reloadBack(s.page);
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
    await s.page.waitForFunction(() => document.body.hasAttribute('data-conn-lost'),
      null, { timeout: 20000 });
    await s.page.waitForFunction(() => document.body.hasAttribute('data-stale'),
      null, { timeout: 20000 });
    // The pane fades over 200ms, so the measurement waits for it rather than
    // catching it half way.
    await wait(400);
    const lost = await s.page.evaluate(() => ({
      bar: !!document.querySelector('#connSlot:not([hidden]) .ds-alert'),
      barText: (document.querySelector('#connSlot:not([hidden]) .ds-alert') || {}).textContent || '',
      stale: document.getElementById('termWrap').hasAttribute('data-stale'),
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
    await s.page.waitForFunction(() => !document.body.hasAttribute('data-conn-lost'),
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
    // On a phone the button is in the closed drawer: there, not shown.
    await s.page.waitForSelector('#newSession', { state: 'attached', timeout: 20000 });
    await wait(1500);
    const offlineShell = await s.page.evaluate(() => ({
      terminal: typeof window.Terminal === 'function',
      hash: location.hash.slice(1),
      list: (document.querySelector('.so-list-empty') || {}).textContent || 'rows',
      empty: (document.querySelector('.empty-title') || {}).textContent || '',
    }));
    ok(offlineShell.terminal, 'the app shell opened with no network at all', JSON.stringify(offlineShell));
    // Every start of the page lands on Home (HOME.md §2.3), an offline one
    // too - and Home must not mistake no signal for no sessions.
    ok(offlineShell.hash === 'home', 'and it lands on Home, as every start does', offlineShell.hash);
    ok(!/No sessions yet/.test(offlineShell.list) && !/No sessions yet/.test(offlineShell.empty),
      'it does not claim there are no sessions when it could not ask',
      offlineShell.list + ' / ' + offlineShell.empty);

    await s.context.setOffline(false);
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    const listed = await s.page.waitForSelector('#sessionList .so-row[data-id="' + id + '"]', { state: 'attached', timeout: 30000 })
      .then(() => true).catch(() => false);
    ok(listed, 'the signal back, the list has the session again by itself',
      listed ? 'the row is back' : 'still empty');
    await s.page.evaluate((target) => { location.hash = '#' + target; }, id);
    const reopened = await s.page.waitForSelector('#term .xterm', { timeout: 30000 })
      .then(() => true).catch(() => false);
    ok(reopened, 'and the session it was on opens again', reopened ? 'the pane is back' : 'still empty');

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
    await s.page.waitForFunction(() => !document.body.hasAttribute('data-conn-lost'),
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
      // The terminal's socket, not the event stream's.
      window.WebSocket = new Proxy(window.WebSocket, {
        construct(target, args) {
          const ws = new target(...args);
          if (!String(args[0]).includes('/ws?')) return ws;
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
    await second.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
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
    await s.page.waitForFunction(() => document.body.hasAttribute('data-conn-lost'),
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

    const live = await s.page.waitForFunction(() => !document.body.hasAttribute('data-conn-lost'),
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

    await reloadBack(s.page);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    // Before `hello`: the pane is there, the person types, and the socket has
    // no idea yet what number anything should carry.
    await s.page.click('#term .xterm-screen');
    await focusTerm(s.page);
    await s.page.keyboard.type('echo k >> ' + tag + '.txt', { delay: 10 });
    await s.page.keyboard.press('Enter');

    await s.page.waitForFunction(() => !document.body.hasAttribute('data-conn-lost'),
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
  await page.waitForSelector('#newSessionSheet .so-settings__program[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#newSessionSheet .so-settings__program[data-value="' + harness + '"]');
  // The model step is hidden for Shell and pre-filled for a harness that names
  // a default. Where it is neither - OpenCode reports models and no default -
  // the sheet waits for a choice, which is what a person would make.
  const wantsModel = await page.$eval('#nsModelField', (n) => !n.hidden);
  const chosen = await page.$eval('#nsModel .ds-combo__input', (n) => n.value.trim());
  if (wantsModel && (modelLabel || !chosen)) {
    await page.click('#nsModel .ds-combo__input');
    await page.waitForSelector('#nsModel .ds-combo__option', { timeout: 5000 });
    await page.click(modelLabel
      ? '#nsModel .ds-combo__option:has(.ds-combo__name:text-is("' + modelLabel + '"))'
      : '#nsModel .ds-combo__option');
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
  await s.page.waitForSelector('#termOverlay[data-state="open"] .ds-dialog__title', { timeout: 25000 });
  const words = await s.page.$eval('#termOverlay .ds-dialog__title', (n) => n.textContent);
  ok(/The session ended/.test(words), name + ': the overlay says the session ended', oneLine(words));
  await s.page.click('#termRestart');
  await s.page.waitForFunction(() => {
    const node = document.getElementById('termOverlay');
    return node && node.hidden;
  }, null, { timeout: 40000 });
  return before;
}

/* ------------------------------------------------- the agents, as chats */

// The agents run as chats (REBUILD.md §7). The scripted protocol fakes -
// e2e/fakebin/fakeclaude, fakecodex and fakeopencode, on PATH under the
// agents' own names - speak each agent's protocol, and what a message says
// is what its turn does: fakeclaude says its command line for /argv and its
// environment for /env, fakecodex and fakeopencode who they are signed in
// as for /whoami, fakeopencode the session a turn is in, and anything else
// is "you said: ...". What reached a CLI is therefore asked of the CLI, in
// the chat, rather than read from a log beside it.
const AGENTS = ['claude', 'codex', 'opencode'];

// work is the message that keeps an agent busy for `sec` seconds, in the
// words of its fake.
const work = (harness, sec) => (harness === 'claude' ? '/slow ' : '/sleep ') + sec;

// signIn gives an agent an account that is signed in - a key, the one kind
// a fake is signed in to without a sign-in flow of its own - for a scenario
// that starts more than one chat from the sheet: a chat made with no account
// at all runs on a Default account the server makes, which is not signed in
// yet, and the sheet starts nothing more on that until it is.
async function signIn(s, agentName = 'claude', label = 'Work') {
  const res = await s.context.request.post(s.url + '/api/accounts', {
    data: { agent: agentName, kind: 'api_key', label, key: 'sk-e2e-' + agentName + '-' + 'k'.repeat(16),
      ...(agentName === 'opencode' ? { provider: 'openrouter' } : {}) },
  });
  const made = (await res.json()).account;
  if (!made || made.state !== 'signed_in') throw new Error('no signed-in ' + agentName + ' account: ' + JSON.stringify(made));
  return made;
}

// clickChat opens a chat from the list by its name, the way a person aiming
// at it does, and waits until it is the one on screen.
async function clickChat(page, id) {
  await ensureNav(page);
  await leaveComposer(page, id);
  await page.click(rowSel(id) + ' .so-row__label');
  await page.waitForFunction((want) => location.hash.slice(1) === want, id, { timeout: 15000 });
  await page.waitForSelector(rowSel(id) + ' .so-row__open[aria-current="page"]', { timeout: 15000 });
  await chatUp(page);
}

// makeChat makes a chat the way another device would - over the API, with
// this page never attached to it - and answers its id.
async function makeChat(s, harness = 'claude', extra = {}) {
  const res = await s.context.request.post(s.url + '/api/chats', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), harness, workdir_mode: 'dynamic', ...extra },
  });
  if (res.status() !== 201) throw new Error('could not make a ' + harness + ' chat: ' + res.status() + ' ' + await res.text());
  return (await res.json()).session.id;
}

// answerTo sends a chat a message from its composer and answers what the
// agent said back, as the server holds it once the turn is over and as the
// page has drawn it.
async function answerTo(s, id, text, timeout = 20000) {
  await say(s.page, text);
  const until = Date.now() + timeout;
  let said = null;
  while (said === null && Date.now() < until) {
    const body = await chatOf(s, id);
    const messages = (await historyOf(s, id)).messages || [];
    const at = messages.map((m) => m.role === 'user' && m.text === text).lastIndexOf(true);
    const reply = at >= 0 ? messages.slice(at + 1).find((m) => m.role === 'assistant') : null;
    if (reply && body.chat && idle(body)) said = textOf(reply);
    else await wait(200);
  }
  if (said) await awaitLog(s.page, said.split('\n')[0].slice(0, 60), 10000);
  return said || '';
}

// textOf is the words of an assistant message: its text parts, in order.
const textOf = (message) => (message.parts || []).filter((p) => p.kind === 'text').map((p) => p.text || '').join('\n');

// hostsOf is every agent host process of a run's data directory: the
// holders of the chats' CLIs, which outlive the server on purpose.
function hostsOf(data) {
  try {
    return execFileSync('pgrep', ['-f', 'agent-host --data ' + data], { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString().split('\n').filter(Boolean).map(Number);
  } catch {
    return [];
  }
}

// linesOf is every line between the messages of the chat on screen - a
// notice, a divider, an error - as "kind:words".
const linesOf = (page) => page.$$eval('#chat .so-line, #chat .so-line-alert',
  (nodes) => nodes.map((n) => n.dataset.kind + ':' + n.innerText.replace(/\s+/g, ' ').trim()));

// thinkingOf is the summary line of the newest answer's reasoning, '' when
// it has none.
const thinkingOf = (page) => page.evaluate(() => {
  const all = document.querySelectorAll('#chat .so-msg[data-role="assistant"]');
  const think = all.length ? all[all.length - 1].querySelector('.so-reasoning') : null;
  return think && !think.open ? think.querySelector('summary').textContent.trim() : '';
});

// until polls a test of the machine itself - a process, a file - and says
// whether it came true in time.
async function until(test, timeout = 15000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await test()) return true;
    await wait(200);
  }
  return !!(await test());
}

// filesUnder is every file below a directory, at any depth.
function filesUnder(dir) {
  const out = [];
  const walk = (at) => {
    let entries = [];
    try { entries = readdirSync(at, { withFileTypes: true }); } catch { return; }
    for (const entry of entries) {
      const path = join(at, entry.name);
      if (entry.isDirectory()) walk(path);
      else if (entry.isFile()) out.push(path);
    }
  };
  walk(dir);
  return out;
}

// answerState is the state of the newest answer on screen ('' for one that
// ended the ordinary way).
const answerState = (page) => page.evaluate(() => {
  const all = document.querySelectorAll('#chat .so-msg[data-role="assistant"]');
  return all.length ? all[all.length - 1].dataset.state || '' : 'none';
});

/* --------------------------------------------------------- 13. createclaude */

// Claude Code start to finish, as a chat: the sheet offers the models the
// catalogue reported, the one picked reaches `claude -p`, the conversation
// fixed at creation is the one the chat knows and the transcript is written
// for the chat's own folder, and a CLI that went away mid-turn is started
// again on that conversation by the next message - one message, one relaunch,
// and the conversation says what happened.
async function createclaude() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
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
    await s.page.click('#newSessionSheet .so-settings__program[data-value="claude"]');
    await s.page.click('#nsModel .ds-combo__input');
    await s.page.waitForSelector('#nsModel .ds-combo__option', { timeout: 5000 });
    const shown = await s.page.$$eval('#nsModel .ds-combo__option .ds-combo__name', (nodes) => nodes.map((n) => n.textContent));
    ok(shown.length > 0 && shown.join(',') === offered.join(','),
      'the sheet offers exactly the models /api/harnesses reported for Claude Code',
      shown.join(',') + ' vs ' + offered.join(','));
    await s.page.click('#nsModel .ds-combo__option:has(.ds-combo__name:text-is("Haiku"))');
    await s.page.click('#nsStart');
    await s.page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await chatUp(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));
    const row = await sessionRow(s, id);
    ok(row.kind === 'chat' && row.model === 'haiku', 'the chat is made with the model picked', row.kind + ' ' + row.model);

    const argv = await answerTo(s, id, '/argv');
    ok(/(^| )--model haiku( |$)/.test(argv), 'and Claude Code was started with it', oneLine(argv.slice(argv.indexOf('--model'))));
    const fixed = (/--session-id (\S+)/.exec(argv) || [])[1] || '';
    const body = await chatOf(s, id);
    ok(/^[0-9a-f-]{36}$/.test(fixed) && body.chat.conversation_id === fixed,
      'with a --session-id fixed at creation, which is the conversation the chat knows',
      fixed + ' vs ' + body.chat.conversation_id);
    // Every account's projects/ is the shared folder (REBUILD.md §3.1), and
    // Claude Code names a project's folder after the directory it runs in.
    const transcript = join(s.data, 'agents', 'claude', 'projects', row.workdir.replace(/[^A-Za-z0-9]/g, '-'), fixed + '.jsonl');
    ok(existsSync(transcript), 'it ran in the chat’s own folder, and its transcript is in the shared projects folder',
      transcript.replace(s.data, '<data>'));

    // §E.10 rule 2 and 3: the header is the name and nothing else - neither
    // the mark nor the model, which are the row's and the composer's; the
    // path is in Info.
    const badge = await s.page.evaluate(() => {
      const bar = document.querySelector('.ds-topbar');
      return { mark: !!bar.querySelector('.so-agent'), model: /haiku/i.test(bar.textContent),
        composer: (document.querySelector('#chat .so-composer__model-text') || {}).textContent || '' };
    });
    ok(!badge.mark && !badge.model && /haiku/i.test(badge.composer),
      'the header names neither the agent nor the model; the composer says the model', JSON.stringify(badge));
    await rowMenu(s.page);
    await s.page.click('#sessionMenu [data-action="info"]');
    await s.page.waitForSelector('dialog.so-info-dialog .so-facts', { timeout: 5000 });
    const facts = await s.page.$$eval('dialog.so-info-dialog .so-fact', (nodes) => nodes.map((n) => n.textContent));
    ok(facts.includes(row.workdir), 'and the working directory is one line of Info', oneLine(facts.join(' · ')));
    await s.page.click('dialog.so-info-dialog .ds-dialog__actions .ds-btn');
    await s.page.waitForSelector('dialog.so-info-dialog', { state: 'detached', timeout: 5000 });
    await shot(s.page, 'createclaude');

    // The CLI dies in the middle of a turn. The turn says it failed, and the
    // next message starts Claude Code again on the conversation it had.
    const hosts = hostsOf(s.data).length;
    await say(s.page, '/crash');
    const failed = await awaitChat(s, id, (b) => idle(b) && b.chat.last_outcome === 'failed', 20000);
    await awaitLog(s.page, 'exited during the turn', 10000);
    const said = (await linesOf(s.page)).filter((l) => l.startsWith('failed:')).join(' | ');
    ok(failed.chat.last_outcome === 'failed' && /exited during the turn/.test(said),
      'a CLI that exits mid-turn ends the turn as failed, and the conversation says so', oneLine(said));
    const again = await answerTo(s, id, 'are you back');
    ok(again === 'you said: are you back', 'the next message is answered', again);
    const relaunched = await answerTo(s, id, '/argv');
    ok(relaunched.includes('--resume ' + fixed) && !relaunched.includes('--session-id'),
      'by Claude Code started again on the conversation it already had', oneLine(relaunched.slice(relaunched.indexOf('--resume'))));
    const hostsNow = hostsOf(s.data).length;
    ok(hosts === 1 && hostsNow === 1, 'and one host holds it, before and after', hosts + ' then ' + hostsNow);
    const history = await historyOf(s, id);
    ok(userTexts(history).join('|') === '/argv|/crash|are you back|/argv',
      'each message is in the conversation once', userTexts(history).join('|'));

    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------- accounts-agents */

// accounts-agents: an account is a second set of sign-ins for an agent
// (REBUILD.md §4, which replaced the profiles), and it reaches that agent's
// CLI as the folder the CLI keeps its sign-in in. A second Claude Code
// account is offered by the sheet, chosen with Ultracode, and the CLI runs
// with its CLAUDE_CONFIG_DIR, its key and Ultracode; Info names both, and the
// account is not deleted while the chat runs on it. A Codex key is signed in
// by codex itself into the account's own CODEX_HOME, which is the home its
// chat runs in; an OpenCode key reaches its server in the environment.
async function accountsagents() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  const { page } = s;
  const post = async (path, data) => (await s.context.request.post(s.url + path, { data })).json();
  try {
    await setup(page, s.url);
    const workKey = 'sk-ant-work-' + 'w'.repeat(16);
    const homeKey = 'sk-ant-home-' + 'h'.repeat(16);
    const work = (await post('/api/accounts', { agent: 'claude', kind: 'api_key', label: 'Work', key: workKey })).account;
    const home = (await post('/api/accounts', { agent: 'claude', kind: 'api_key', label: 'Home', key: homeKey })).account;
    ok(work && home && work.state === 'signed_in' && home.state === 'signed_in', 'two Claude Code accounts, each a key',
      JSON.stringify([work, home].map((a) => a && { label: a.label, state: a.state })));

    await page.reload({ waitUntil: 'domcontentloaded' });
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="claude"]');
    await page.waitForSelector('#nsAccountField:not([hidden]) .so-drop__field', { timeout: 10000 });
    await page.click('#nsAccount .so-drop__field');
    await page.waitForSelector('#nsAccount .ds-combo__option[data-value="' + home.id + '"]', { timeout: 5000 });
    const offered = await page.$$eval('#nsAccount .ds-combo__option .ds-combo__name', (n) => n.map((x) => x.textContent));
    ok(offered.join(',') === 'Work,Home', 'the sheet offers the agent’s accounts, in their order', offered.join(','));
    await page.click('#nsAccount .ds-combo__option[data-value="' + home.id + '"]');
    const face = await page.$eval('#nsAccount .so-drop__value', (n) => n.textContent);
    ok(face === 'Home', 'the dropdown shows the account chosen', face);
    // Ultracode is Claude Code's alone, and off until it is switched on.
    const ultra = await page.$eval('#nsUltracodeField', (n) => ({ hidden: n.hidden, off: !n.querySelector('#nsUltracode').checked }));
    ok(!ultra.hidden && ultra.off, 'the sheet offers Ultracode for Claude Code, off', JSON.stringify(ultra));
    await page.click('#nsUltracode');
    await shot(page, 'accounts-agents-sheet');
    await page.click('#nsStart');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await chatUp(page);
    const id = await page.evaluate(() => location.hash.slice(1));
    const row = await sessionRow(s, id);
    ok(row.options && row.options.account === home.id && row.options.ultracode === true,
      'the chat remembers its account and Ultracode', JSON.stringify(row.options));

    const env = await answerTo(s, id, '/env');
    const dir = (/CLAUDE_CONFIG_DIR=(\S+)/.exec(env) || [])[1] || '';
    ok(dir === join(s.data, 'accounts', home.id), 'Claude Code runs on the account’s own folder', dir.replace(s.data, '<data>'));
    ok(env.includes('ANTHROPIC_API_KEY=' + homeKey) && !env.includes(workKey) && !env.includes('SOCRATES_CLAUDE_API_KEY'),
      'with that account’s key as its own variable, and no other', JSON.stringify(env.split('\n').map((l) => l.replace(/=sk-\S+/, '=<key>'))));
    const argv = await answerTo(s, id, '/argv');
    ok(/"ultracode":true/.test(argv), 'and with Ultracode on in its settings', (/--settings \{[^}]*\}/.exec(argv) || ['none'])[0]);

    await rowMenu(page);
    await page.click('#sessionMenu [data-action="info"]');
    await page.waitForSelector('dialog.so-info-dialog .so-facts', { timeout: 5000 });
    const facts = await page.$$eval('dialog.so-info-dialog .so-fact', (nodes) => nodes.map((n) => n.textContent));
    ok(facts.includes('Account: Home') && facts.includes('Ultracode on'), 'Info names the account and Ultracode',
      oneLine(facts.join(' · ')));
    await page.click('dialog.so-info-dialog .ds-dialog__actions .ds-btn');
    await page.waitForSelector('dialog.so-info-dialog', { state: 'detached', timeout: 5000 });
    const refused = await s.context.request.delete(s.url + '/api/accounts/' + home.id);
    ok(refused.status() === 409, 'an account a chat runs on cannot be deleted', String(refused.status()));

    // Codex: the key is signed in by codex itself, into the account's home,
    // and the chat's codex runs in that home.
    const codexKey = 'sk-codex-' + 'c'.repeat(20);
    const codex = (await post('/api/accounts', { agent: 'codex', kind: 'api_key', label: 'Codex key', key: codexKey })).account;
    ok(codex && codex.state === 'signed_in', 'a Codex key is signed in', JSON.stringify(codex && { state: codex.state, problem: codex.problem }));
    const codexHome = join(s.data, 'accounts', codex.id);
    let auth = '';
    try { auth = readFileSync(join(codexHome, 'auth.json'), 'utf8'); } catch { /* reported below */ }
    ok(auth.includes(codexKey), 'by codex, into the account’s own CODEX_HOME', join(codexHome, 'auth.json').replace(s.data, '<data>'));
    const codexChat = await makeChat(s, 'codex', { account: codex.id });
    await openChat(page, codexChat);
    const whoCodex = await answerTo(s, codexChat, '/whoami');
    ok(whoCodex === 'account: key:' + codexKey + ' (home ' + codexHome + ')', 'and its chat runs in that home, signed in with it',
      whoCodex.replace(codexKey, '<key>').replace(s.data, '<data>'));

    // OpenCode: the key of its provider, handed to the server it runs.
    const openKey = 'sk-or-' + 'o'.repeat(20);
    const open = (await post('/api/accounts', { agent: 'opencode', kind: 'api_key', label: 'Router', key: openKey, provider: 'openrouter' })).account;
    const openChatId = await makeChat(s, 'opencode', { account: open.id });
    await openChat(page, openChatId);
    const whoOpen = await answerTo(s, openChatId, '/whoami');
    ok(whoOpen === 'account: openrouter:' + openKey + ' (env)', 'an OpenCode key reaches its server in the environment, not a file',
      whoOpen.replace(openKey, '<key>'));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ---------------------------------------------------------- 14. createcodex */

// Codex as a chat: one `codex app-server` per chat, spoken to in JSON-RPC.
// The thread it starts on the first turn is the chat's conversation and is
// kept as one rollout in the shared sessions folder; a chat filed as done has
// its CLI put to rest, and the next message resumes that same thread.
async function createcodex() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await open(s);
    const id = await startChat(s.page, 'codex');
    const first = await answerTo(s, id, 'the first words');
    ok(first === 'you said: the first words', 'Codex answers over its app server', first);
    const thought = await thinkingOf(s.page);
    ok(thought === 'Thought', 'and its reasoning is the line before the answer, shut', thought);
    const thread = (await chatOf(s, id)).chat.conversation_id;
    const rollouts = () => filesUnder(join(s.data, 'agents', 'codex', 'sessions')).filter((f) => f.endsWith('.jsonl'));
    const kept = rollouts();
    ok(!!thread && kept.length === 1 && basename(kept[0]).includes(thread),
      'its thread is the chat’s conversation, kept as one rollout in the shared sessions folder',
      JSON.stringify({ thread, rollouts: kept.map((f) => f.replace(s.data, '<data>')) }));

    // Done: the chat is filed away, and its CLI with its host is put to rest.
    await rowMenu(s.page);
    await s.page.click('#sessionMenu [data-action="status:done"]');
    const parked = await until(() => hostsOf(s.data).length === 0, 20000);
    const body = await awaitChat(s, id, (b) => b.chat.process === 'parked', 10000);
    ok(parked && body.chat.process === 'parked', 'a chat filed as done has its agent put to rest',
      JSON.stringify({ hosts: hostsOf(s.data).length, process: body.chat.process }));

    // The next message starts it again on the thread it had.
    const second = await answerTo(s, id, 'the second words');
    const after = rollouts();
    const again = (await chatOf(s, id)).chat.conversation_id;
    let rollout = '';
    try { rollout = readFileSync(after[0], 'utf8'); } catch { /* reported below */ }
    ok(second === 'you said: the second words' && again === thread && after.length === 1
      && rollout.includes('the first words') && rollout.includes('the second words'),
      'the next message resumes the same thread: one rollout holding both turns',
      JSON.stringify({ second, again, rollouts: after.length }));
    ok(userTexts(await historyOf(s, id)).join('|') === 'the first words|the second words',
      'and the conversation on screen is the one conversation', userTexts(await historyOf(s, id)).join('|'));
    await shot(s.page, 'createcodex');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------- 15. createopencode */

// OpenCode as a chat: one `opencode serve` per chat, on a port and with a
// password of its own (the fake refuses to serve without one, and anywhere
// but the loopback), spoken to over its authenticated HTTP API and its event
// stream. The session it keeps is the chat's conversation, and a chat filed
// as done goes on in that same session with its next message.
async function createopencode() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await open(s);
    const id = await startChat(s.page, 'opencode');
    const first = await answerTo(s, id, 'the first words');
    const session = (/\(turn 1 of (ses_\S+)\)$/.exec(first) || [])[1] || '';
    const body = await chatOf(s, id);
    ok(!!session && body.chat.conversation_id === session,
      'OpenCode answers over its own server, in the session the chat knows', JSON.stringify({ first, conversation: body.chat.conversation_id }));
    const spec = JSON.parse(readFileSync(join(s.data, 'chats', id, 'host.json'), 'utf8'));
    ok(!JSON.stringify(spec).includes('OPENCODE_SERVER_PASSWORD'),
      'its password is made for the process and kept in no file', Object.keys(spec).join(','));

    await rowMenu(s.page);
    await s.page.click('#sessionMenu [data-action="status:done"]');
    const parked = await until(() => hostsOf(s.data).length === 0, 20000);
    ok(parked, 'filed as done, its server is put to rest', hostsOf(s.data).length + ' hosts');
    const second = await answerTo(s, id, 'the second words');
    ok(second === 'you said: the second words (turn 2 of ' + session + ')',
      'and the next message is the second turn of the same session', second);
    await shot(s.page, 'createopencode');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* --------------------------------------------------------- 16. rebootresume */

// A machine that rebooted under a chat in the middle of a turn: the server,
// every agent host with its CLI and the tmux server are gone at once. The
// server that starts again finds the chat's host dead with a turn open, and
// takes it up by itself: the turn is closed as interrupted, the conversation
// says once that it was resumed after a restart, and the next message goes to
// Claude Code started again on the conversation it had. The Shell beside it
// waits to be resumed and is a working shell when it is opened.
async function rebootresume() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const shellId = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const id = await startChat(s.page, 'claude');
    const hello = await answerTo(s, id, 'before the reboot');
    const conversation = (await chatOf(s, id)).chat.conversation_id;
    ok(hello === 'you said: before the reboot' && /^[0-9a-f-]{36}$/.test(conversation),
      'the chat is up and holding a conversation', conversation || 'none');
    await say(s.page, work('claude', 60));
    const busy = await awaitChat(s, id, (b) => b.chat.turn_running, 10000);
    ok(busy.chat.turn_running, 'and it is in the middle of a turn', JSON.stringify(busy.chat.activity));

    // The tab is put away first: a page attached to the chat would open its
    // stream the moment the server is back, and the scenario would measure
    // its own browser instead of what a rebooted machine is found in.
    await s.page.goto('about:blank', { waitUntil: 'domcontentloaded' });
    const hosts = hostsOf(s.data);
    await s.restart({}, async () => {
      for (const pid of hosts) {
        try { process.kill(pid, 'SIGKILL'); } catch { /* already gone */ }
      }
      killTmux(s.data);
      await until(() => hostsOf(s.data).length === 0, 5000);
    });
    ok(hosts.length === 1, 'the reboot took the chat’s host with it', hosts.length + ' host(s) killed');

    const back = await awaitChat(s, id, (b) => !b.chat.turn_running && b.chat.last_outcome === 'interrupted', 30000);
    ok(back.chat.last_outcome === 'interrupted', 'the server takes the chat up by itself: the turn it was in ends as interrupted',
      JSON.stringify({ outcome: back.chat.last_outcome, process: back.chat.process }));
    const row = await sessionRow(s, id);
    ok(row.resume_count === 1, 'and the resume is counted once', String(row.resume_count));

    await s.page.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
    await chatUp(s.page);
    const said = await awaitLog(s.page, 'Resumed after a restart.', 20000);
    const lines = (await linesOf(s.page)).filter((l) => /Resumed after a restart/.test(l));
    ok(said && lines.length === 1 && lines[0].startsWith('notice:'),
      'the conversation says once that it was resumed after a restart', JSON.stringify(lines));
    const cut = await answerState(s.page);
    ok(cut === 'interrupted', 'and the answer the reboot cut short says it was interrupted', cut);
    await shot(s.page, 'rebootresume');

    const argv = await answerTo(s, id, '/argv');
    ok(argv.includes('--resume ' + conversation) && !argv.includes('--session-id'),
      'the next message goes to Claude Code started again on the conversation it had',
      oneLine(argv.slice(argv.indexOf('--resume'))));
    const after = await answerTo(s, id, 'after the reboot');
    ok(after === 'you said: after the reboot', 'and the chat that came back is one that works', after);
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    await openChat(s.page, id);
    await awaitLog(s.page, 'you said: after the reboot', 15000);
    const again = (await linesOf(s.page)).filter((l) => /Resumed after a restart/.test(l));
    ok(again.length === 1, 'a reload shows the line once, as the history has it', String(again.length));

    // The Shell lost its tmux with the machine: it waits to be resumed, and
    // opening it gives back a shell that works.
    const waiting = await waitForState(s, shellId, ['needs_resume'], 20000);
    ok(waiting.state === 'needs_resume', 'the Shell beside it waits to be resumed', waiting.state);
    await s.page.goto(s.url + '/#open=' + shellId, { waitUntil: 'domcontentloaded' });
    const resumed = await waitForState(s, shellId, ['running'], 40000);
    ok(resumed.state === 'running', 'and opening it resumes it', resumed.state);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const alive = 'again-' + Math.random().toString(36).slice(2, 8);
    await typeLine(s.page, 'echo ' + alive);
    ok(await awaitScreen(s.page, alive, 25000), 'as a shell that works', oneLine(await screen(s.page)).slice(-80));
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
    await second.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
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
      // The terminal's socket, not the event stream's.
      window.WebSocket = new Proxy(window.WebSocket, {
        construct(target, args) {
          const ws = new target(...args);
          if (!String(args[0]).includes('/ws?')) return ws;
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
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await typeLine(s.page, 'echo ready-$((6*7))');
    ok(await awaitScreen(s.page, 'ready-42'), 'the shell is at its prompt',
      oneLine(await screen(s.page)));

    // What the client ends up holding is the measure that matters: the ring
    // is what a viewer reads from, and a reader that falls behind it is told
    // so with a replay from zero. So the sockets are watched for a close and
    // the notices for a redraw, and the pane's own scrollback is read back
    // afterwards - not the journal, which the server writes whether or not a
    // byte ever reached a browser.
    await s.page.evaluate(() => { window.__notices = []; });
    // The loop as it is typed has no number after "spin", so only what it
    // prints is counted below.
    await typeLine(s.page, 'i=1; while [ $i -le 200 ]; do echo "spin $i"; i=$((i+1)); done');
    ok(await awaitScreen(s.page, 'spin 200', 40000), 'the last of the two hundred lines arrived',
      oneLine(await screen(s.page)));

    // What the pane printed, as the journal recorded it: the loop's own
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

    await s.page.click('#sessionList .so-row[data-id="' + id + '"] .so-row__more');
    await s.page.waitForSelector('#sessionMenu:not([hidden])', { timeout: 5000 });
    const items = await s.page.$$eval('#sessionMenu:not([hidden]) .ds-menu__item', (nodes) =>
      nodes.map((n) => n.textContent.trim()));
    ok(items.includes('Download scrollback'), 'and the row is where it is offered',
      items.join(' | '));
    await s.page.click('#sessionMenu .ds-menu__item:has(.ds-menu__label:text-is("Delete"))');
    await s.page.waitForSelector('dialog.ds-dialog[open] .ds-btn--primary', { timeout: 5000 });
    await s.page.click('dialog.ds-dialog[open] .ds-btn--primary');
    await s.page.waitForFunction((sel) => !document.querySelector(sel),
      '#sessionList .so-row[data-id="' + id + '"]', { timeout: 10000 });

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

    // A chat the same way: its agent wrote a file, its conversation can be
    // taken away from its row, and Delete stops the agent and removes what
    // Socrates kept for the chat, and nothing it worked on.
    const chat = await startChat(s.page, 'claude');
    const chatDir = (await sessionRow(s, chat)).workdir;
    const wrote = await answerTo(s, chat, '/write');
    ok(wrote === 'Wrote new.txt.' && existsSync(join(chatDir, 'new.txt')), 'a chat’s agent did some work in its directory',
      join(chatDir, 'new.txt').replace(s.data, '<data>'));
    const exported = await s.context.request.get(s.url + '/api/chats/' + chat + '?history=1');
    const conversation = await exported.text();
    ok(exported.ok() && conversation.includes('Wrote new.txt.'), 'its conversation can be taken away before it goes',
      exported.status() + ', ' + conversation.length + ' bytes');
    await s.page.click(rowSel(chat) + ' .so-row__more');
    await s.page.waitForSelector('#sessionMenu:not([hidden])', { timeout: 5000 });
    const chatItems = await s.page.$$eval('#sessionMenu:not([hidden]) .ds-menu__item', (nodes) => nodes.map((n) => n.textContent.trim()));
    ok(chatItems.includes('Download the conversation') && !chatItems.includes('Download scrollback'),
      'and its row offers the conversation, not a scrollback', chatItems.join(' | '));
    await s.page.click('#sessionMenu .ds-menu__item:has(.ds-menu__label:text-is("Delete"))');
    await s.page.waitForSelector('dialog.ds-dialog[open] .ds-btn--primary', { timeout: 5000 });
    await s.page.click('dialog.ds-dialog[open] .ds-btn--primary');
    await s.page.waitForFunction((sel) => !document.querySelector(sel), rowSel(chat), { timeout: 10000 });
    const stopped = await until(() => hostsOf(s.data).length === 0, 15000);
    ok(stopped, 'deleting the chat stops its agent', hostsOf(s.data).length + ' hosts');
    ok(!existsSync(join(s.data, 'chats', chat)) && existsSync(join(chatDir, 'new.txt')),
      'and removes the chat’s own folder, while its working directory keeps the work',
      JSON.stringify({ chatFolder: existsSync(join(s.data, 'chats', chat)), work: existsSync(join(chatDir, 'new.txt')) }));

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
    await s.page.waitForFunction((want) => [...document.querySelectorAll('#sessionList .so-row')]
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

    const label = await s.page.$eval('#sessionList .so-row[data-id="' + stray + '"] .so-row__label',
      (n) => n.textContent).catch(() => 'no row on screen');
    ok(label === 'Recovered session', 'the browser shows it in the list', label);
    ok(await s.page.$('#sessionList .so-row[data-id="' + id + '"]') !== null,
      'beside the session that was there all along', id);

    await shot(s.page, 'recoveredsession');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ----------------------------------------------------------- 22. lighttheme */

// The white terminal, proved in the browser rather than in the palette file:
// the pane is painted #ffffff, and every ANSI colour a program can emit is
// *drawn* legibly on it. (That a program asking the terminal what colour it
// is on is told "light" is termux's TestOSCRespondsWhite.)
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

    // The terminal's own palette, which is what is left of the white
    // terminal since the agents became chats: a Shell, painted white, and
    // every ANSI colour a program can emit drawn legibly on it.
    const shellId = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    ok(!!shellId, 'a shell to print with', shellId);

    const painted = await s.page.evaluate(() => {
      const bg = (sel) => {
        const node = document.querySelector(sel);
        return node ? getComputedStyle(node).backgroundColor : 'missing';
      };
      return { viewport: bg('#term .xterm-viewport'), screen: bg('#term .xterm-screen') };
    });
    ok(painted.viewport === WHITE && painted.screen === WHITE,
      'the pane is painted pure white', JSON.stringify(painted));

    // The sixteen ANSI colours, printed by the shell and measured where they
    // landed.
    const words = [];
    for (let code = 30; code <= 37; code += 1) words.push([code, 'C' + code]);
    for (let code = 90; code <= 97; code += 1) words.push([code, 'C' + code]);
    await typeLine(s.page, "printf '" + words.map(([code, word]) =>
      '\\033[' + code + 'm' + word + '\\033[0m\\n').join('') + "'");
    // The command line names every word too, so what is waited for is the
    // words drawn on cells of their own.
    const printed = await s.page.waitForFunction(() => {
      const spans = [...document.querySelectorAll('#term .xterm-rows span')].map((n) => n.textContent.trim());
      return spans.includes('C30') && spans.includes('C97');
    }, null, { timeout: 20000 }).then(() => true, () => false);
    ok(printed, 'the pane printed all sixteen ANSI colours', oneLine(await screen(s.page)));

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

    // The picture is the record of what the eye would confirm.
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
  const s = await start({ viewport: { width: 1280, height: 720 }, env: FAKE_AGENT });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const row = await sessionRow(s, id);

    // Rule 1 - Dashboard-Style's look: the frame (the bar and the drawer) on
    // one tinted backdrop, the work on one white panel.
    const surfaces = await s.page.evaluate(() => {
      const bg = (sel) => {
        const node = document.querySelector(sel);
        return node ? getComputedStyle(node).backgroundColor : 'missing';
      };
      return {
        frame: { app: bg('.ds-app'), drawer: bg('#sidebar'), list: bg('#sessionList'), topbar: bg('.ds-topbar') },
        work: { panel: bg('.ds-panel'), wrap: bg('.so-pane'),
          viewport: bg('#term .xterm-viewport'), screen: bg('#term .xterm-screen') },
      };
    });
    // A transparent element is the colour underneath it; what a design rule
    // forbids is a fill of its own.
    const clear = 'rgba(0, 0, 0, 0)';
    const white = (colour) => colour === WHITE || colour === clear;
    const backdrop = surfaces.frame.app;
    ok(backdrop !== WHITE && Object.values(surfaces.frame).every((c) => c === backdrop || c === clear)
      && Object.values(surfaces.work).every(white),
      'the frame is one tinted backdrop and the work one white panel', JSON.stringify(surfaces));

    // Rule 2 - a mark wherever a harness is named. The sheet names them all,
    // and the row in the list names this session's.
    await s.page.click('#newSession');
    await s.page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    const sheetMarks = await s.page.$$eval('#nsHarness .so-settings__program, #nsTools .so-settings__program', (nodes) =>
      nodes.map((n) => ({
        harness: n.dataset.value,
        mark: (n.querySelector('.so-agent') || { dataset: {} }).dataset.agent || 'none',
      })));
    // VS Code is opened from a folder's head in the list, not from the sheet
    // (`pages` pins the same four), so the sheet names four programs - and
    // the scripted agent, which this scenario's server offers for its chat.
    ok(sheetMarks.map((one) => one.harness).filter((h) => h !== 'fake').join(',') === 'claude,codex,opencode,shell'
      && sheetMarks.every((one) => one.mark === one.harness),
      'the sheet gives every harness its own mark', JSON.stringify(sheetMarks));

    // Where a session works is a dropdown, and it opens on a list of places
    // with the full path as the sub-text - never a row of cells three
    // characters wide.
    await s.page.click('#nsDir .ds-combo__input');
    await s.page.waitForSelector('#nsDir .ds-combo__option', { timeout: 5000 });
    const dir = await s.page.evaluate(() => {
      const host = document.getElementById('nsDir');
      return {
        combo: !!host.querySelector('.ds-combo'),
        segs: host.querySelectorAll('.so-settings__program, .ds-chip').length,
        options: [...host.querySelectorAll('.ds-combo__option')].map((n) => n.dataset.value),
        value: host.querySelector('.ds-combo__input').value,
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
      row: (document.querySelector('#sessionList .so-row[data-id="' + sessionId + '"] .so-agent')
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
        if (!host || host.closest('.ds-tooltip')) continue;
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
    await s.page.click(rowSel(id) + ' .so-row__more');
    await s.page.click('#sessionMenu [data-action="info"]');
    await s.page.waitForSelector('dialog.so-info-dialog .so-facts', { timeout: 5000 });
    const inInfo = await s.page.$$eval('dialog.so-info-dialog .so-fact', (nodes) =>
      nodes.map((n) => n.textContent).join(' '));
    ok(inInfo.includes(row.workdir), 'and Info is where the workdir actually is',
      oneLine(inInfo));
    await s.page.click('dialog.so-info-dialog .ds-dialog__actions .ds-btn');
    await s.page.waitForSelector('dialog.so-info-dialog', { state: 'detached', timeout: 5000 });

    // Rule 4 - motion is subtle, and it does not restart. A list that
    // re-renders must update the row it already has rather than build a new
    // one, or every mark on it starts again on every poll.
    // A property, not an attribute: the list is morphed, and a morph syncs
    // every attribute to what was drawn.
    await s.page.evaluate((sel) => {
      document.querySelector(sel).wp1Stamp = 'before';
    }, rowSel(id));

    const renamed = row.title + ' renamed';
    await s.context.request.patch(s.url + '/api/sessions/' + id, { data: { title: renamed } });
    // A wake is how the page is told to look again - the same event a phone
    // sends when it comes back. Importing the module to call its own refresh
    // would load a second copy of it under an unstamped URL and boot the whole
    // application again, which really would rebuild the list.
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((want) =>
      [...document.querySelectorAll('#sessionList .so-row .so-row__label')].some((n) => n.textContent === want),
    renamed, { timeout: 20000 });
    const after = await s.page.evaluate((sel) => {
      const node = document.querySelector(sel);
      return { sameRow: !!node && node.wp1Stamp === 'before' };
    }, rowSel(id));
    ok(after.sameRow, 'and a rename is drawn into the row that is already there',
      after.sameRow ? 'the same row' : 'a new row');

    // Rule 4 again, on the mark that says a session is working. It is the
    // agent's own mark with a ring around it, one arc in the primary colour
    // on the outline colour, and it turns on DS's spinner beat
    // (--ds-loop-spin). The arc has to stand out from the ring, because the
    // whole of the motion is that one arc: an arc nobody can see turning is
    // the same thing as not turning at all.
    //
    // It takes a chat: a shell and an editor are the two programs nothing is
    // asked of and nothing is reported back by, so their rows carry no status
    // at all and there is no ring on them to look at. The shell is stepped
    // back onto afterwards, because the rest of this scenario is about the
    // pane it owns.
    const working = await startChat(s.page, 'claude');
    await awaitRow(s.page, working, (r) => !r.busy, 20000);
    const workingRow = await sessionRow(s, working);
    await keepBusy(s.page, 30000);
    await clickRow(s.page, id);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const spinning = await awaitRow(s.page, working, (r) => r.busy, 10000);
    ok(spinning.ok, 'a session that is working turns its own mark into a ring', took(spinning));
    const ring = spinning.seen ? spinning.seen.ring : null;
    const tone = await s.page.evaluate(() => {
      const probe = document.createElement('span');
      document.getElementById('sessionList').append(probe);
      probe.style.color = 'var(--ds-primary)';
      probe.style.backgroundColor = 'var(--ds-line-2)';
      probe.style.animationDuration = 'var(--ds-loop-spin)';
      const css = getComputedStyle(probe);
      const out = { arc: css.color, rest: css.backgroundColor, beat: css.animationDuration };
      probe.remove();
      return out;
    });
    ok(!!ring && ring.colour === tone.arc && ring.rest === tone.rest,
      'the arc that moves is the primary colour, on a ring in the outline colour',
      ring ? ring.colour + ' on ' + ring.rest + ' (want ' + tone.arc + ' on ' + tone.rest + ')' : 'no ring');
    ok(!!ring && contrastOf(ring.colour, ring.rest) >= 3,
      'so the arc is legible against the ring it turns on',
      ring ? contrastOf(ring.colour, ring.rest).toFixed(1) + ':1' : 'no ring');
    const beat = ring ? Math.round(parseFloat(ring.motion) * (/ms$/.test(ring.motion) ? 1 : 1000)) : 0;
    const want = Math.round(parseFloat(tone.beat) * (/ms$/.test(tone.beat) ? 1 : 1000));
    ok(beat > 0 && beat === want, 'and it turns at the pace of every DS spinner', beat + ' ms (--ds-loop-spin ' + tone.beat + ')');

    // And it keeps turning through a re-render: the ring is drawn on the mark
    // the row already has, so a list refresh does not start it again.
    // The animation's start is what a restart would move; its clock is read
    // too, a frame later, to show it is the same animation still turning. The
    // rename now arrives on the event stream within a frame or two, so the
    // second reading waits for one more frame to have been drawn.
    const spinOf = (sel) => s.page.evaluate((one) => {
      const mark = document.querySelector(one + ' .so-mark');
      const anim = mark ? mark.getAnimations({ subtree: true })[0] : null;
      return anim ? { at: Number(anim.currentTime), start: Number(anim.startTime) } : { at: -1, start: NaN };
    }, sel);
    const spunBefore = await spinOf(rowSel(working));
    const spinnerName = workingRow.title + ' renamed';
    await s.context.request.patch(s.url + '/api/sessions/' + working, { data: { title: spinnerName } });
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((want) =>
      [...document.querySelectorAll('#sessionList .so-row .so-row__label')].some((n) => n.textContent === want),
    spinnerName, { timeout: 20000 });
    await s.page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const spunAfter = await spinOf(rowSel(working));
    ok(spunBefore.at >= 0 && spunAfter.start === spunBefore.start && spunAfter.at > spunBefore.at,
      'and a re-render of its row does not restart it',
      Math.round(spunBefore.at) + ' ms -> ' + Math.round(spunAfter.at) + ' ms, started at '
      + Math.round(spunBefore.start) + ' and ' + Math.round(spunAfter.start));

    // Turning decoration off must not turn off the one thing that says work is
    // still happening: the ring has no reduced-motion behaviour of its own and
    // turns at the same pace here as everywhere else. Drawn still it reads as
    // a session that has stopped - which is exactly how it was read, from a
    // desk whose system had "reduce motion" on.
    await s.page.emulateMedia({ reducedMotion: 'reduce' });
    await wait(200);
    const still = await rowActivity(s.page, working);
    ok(!!still.ring && still.ring.name === 'ds-spin' && still.ring.motion === ring.motion,
      'with reduced motion the ring turns at the pace it always turns at',
      still.ring ? still.ring.name + ' ' + still.ring.motion : 'no ring');
    const slow = await s.page.evaluate((sel) => {
      const mark = document.querySelector(sel + ' .so-mark');
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
          // The resting face: a busy face (a spinner and its word) waits
          // beside it, unseen.
          text: ((node.querySelector('.ds-btn__face:not(.ds-btn__face--busy)') || node).textContent || '').trim(),
          label: node.getAttribute('aria-label'),
          title: node.getAttribute('data-tip'),
          svg: node.querySelectorAll('svg').length,
          height: Math.round(node.getBoundingClientRect().height),
        };
      };
      return { status: read('statusBtn'), mic: read('micBtn') };
    });
    ok(header.status.text === '' && header.status.svg >= 1
      && header.status.label === header.status.title,
      'Status is a mark, with its words in the label', JSON.stringify(header.status));
    ok(header.status.label === 'Summarize this session' && header.mic.label === 'Speak',
      'and the words say what pressing them does',
      header.status.label + ' / ' + header.mic.label);
    ok(header.mic.text === 'Speak' && header.mic.height >= 40,
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
        if (!host || host.closest('.ds-tooltip') || host.closest('.ds-sr')) continue;
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

    /* ------------------------------------- the session list put away */

    // A column of names is what a desk wants and a laptop cannot spare. The
    // control beside the brand puts the list away and gives its width to the
    // panel (DS's data-drawer-hidden), this device remembers the answer, and
    // the pane is re-measured for the width it has actually been given.
    const wide = await s.page.evaluate(() =>
      Math.round(document.getElementById('sidebar').getBoundingClientRect().width));
    const panelWide = await s.page.evaluate(() =>
      Math.round(document.querySelector('.ds-panel').getBoundingClientRect().width));
    await s.page.click('#sideCollapse');
    await wait(400);
    const away = await s.page.evaluate(() => ({
      hidden: document.getElementById('app').hasAttribute('data-drawer-hidden'),
      drawer: document.getElementById('sidebar').checkVisibility(),
      panel: Math.round(document.querySelector('.ds-panel').getBoundingClientRect().width),
      toggle: document.getElementById('sideCollapse').checkVisibility(),
      expanded: document.getElementById('sideCollapse').getAttribute('aria-expanded'),
      said: document.getElementById('sideCollapse').getAttribute('aria-label'),
    }));
    ok(wide > 200 && away.hidden && !away.drawer && away.panel > panelWide,
      'the toggle puts the session list away and gives its width to the panel',
      JSON.stringify({ wide, panelWide, ...away }));
    ok(away.toggle && away.expanded === 'false' && /Show/.test(away.said || ''),
      'and stays in the bar, saying it brings the list back', JSON.stringify(away));
    await shot(s.page, 'design-rail');

    // Per device, and it survives the reload it is there to be useful across.
    await reloadBack(s.page);
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await wait(500);
    const kept = await s.page.evaluate(() => ({
      collapsed: document.getElementById('app').hasAttribute('data-drawer-hidden'),
      drawer: document.getElementById('sidebar').checkVisibility(),
    }));
    ok(kept.collapsed && !kept.drawer, 'and a reload comes back to it put away',
      JSON.stringify(kept));

    // The pane is measured in characters and so has to be measured again:
    // bringing the list back takes those columns back. The drawn screen is
    // whole columns wide, so its width is the column count in pixels.
    const screenWidth = () => s.page.$eval('#term .xterm-screen',
      (n) => Math.round(n.getBoundingClientRect().width));
    const railCols = await screenWidth();
    await s.page.click('#sideCollapse');
    await wait(600);
    const wideCols = await screenWidth();
    ok(railCols > wideCols,
      'and the pane is re-measured when the stage changes width',
      railCols + ' px (put away) -> ' + wideCols + ' px (column)');

    /* -------------------------------------------------------- the chat view */

    // The chat view keeps the same rules: it is on the white panel, its row
    // wears its agent's mark, and streaming patches only what changed - a
    // part that did not change keeps its element, and what turns keeps
    // turning while the answer streams.
    const chatId = await startChat(s.page);
    const chatSurfaces = await s.page.evaluate(() => Object.fromEntries(['#chat', '#chat .so-chat__scroll',
      '#chat .so-chat__log', '#chat .so-chat__dock', '#chat .so-composer']
      .map((sel) => [sel, getComputedStyle(document.querySelector(sel)).backgroundColor])));
    ok(Object.values(chatSurfaces).every(white), 'the chat is on the white panel too', JSON.stringify(chatSurfaces));
    const chatMark = await s.page.evaluate((sel) =>
      (document.querySelector(sel + ' .so-agent') || { dataset: {} }).dataset.agent || 'none', rowSel(chatId));
    ok(chatMark === 'fake', 'and its row wears its agent’s mark', chatMark);
    await say(s.page, '/md 50');
    await s.page.waitForFunction(() => {
      const msgs = document.querySelectorAll('#chat .so-msg[data-role="assistant"]');
      const msg = msgs[msgs.length - 1];
      const text = msg && msg.querySelector('.so-part[data-kind="text"]');
      return !!text && text.textContent.length > 40 && !!msg.querySelector('.so-reasoning[data-state="done"]');
    }, null, { timeout: 15000 });
    const streamed = (stamp) => s.page.evaluate(({ sel, stamp: mark }) => {
      const msgs = document.querySelectorAll('#chat .so-msg[data-role="assistant"]');
      const msg = msgs[msgs.length - 1];
      const users = document.querySelectorAll('#chat .so-msg[data-role="user"]');
      const nodes = {
        reasoning: msg.querySelector('.so-reasoning'), text: msg.querySelector('.so-part[data-kind="text"]'),
        message: msg, question: users[users.length - 1], stop: document.querySelector('#chat .so-composer__stop'),
      };
      if (mark) for (const node of Object.values(nodes)) node.wp8Stamp = mark;
      const ring = document.querySelector(sel + ' .so-mark');
      const anim = ring ? ring.getAnimations({ subtree: true })[0] : null;
      return {
        kept: Object.entries(nodes).filter(([, node]) => node.wp8Stamp === 'drawn once').map(([name]) => name),
        length: nodes.text.textContent.length,
        ring: anim ? { start: Number(anim.startTime), at: Number(anim.currentTime) } : null,
      };
    }, { sel: rowSel(chatId), stamp });
    const atStart = await streamed('drawn once');
    await s.page.waitForFunction((was) => {
      const msgs = document.querySelectorAll('#chat .so-msg[data-role="assistant"]');
      return msgs[msgs.length - 1].querySelector('.so-part[data-kind="text"]').textContent.length > was + 300;
    }, atStart.length, { timeout: 20000 });
    const midway = await streamed('');
    ok(midway.kept.join(',') === 'reasoning,text,message,question,stop',
      'while an answer streams, every element that was drawn is the one still there',
      midway.kept.join(',') + ' (' + atStart.length + ' -> ' + midway.length + ' characters)');
    ok(!!atStart.ring && !!midway.ring && midway.ring.start === atStart.ring.start && midway.ring.at > atStart.ring.at,
      'and the ring on its row turns on through every delta, never started again',
      JSON.stringify({ before: atStart.ring, after: midway.ring }));
    await awaitLog(s.page, 'The tests in', 30000);
    const ended = await streamed('');
    ok(['reasoning', 'text', 'message', 'question'].every((name) => ended.kept.includes(name)),
      'and the end of the turn redraws the answer in place', ended.kept.join(','));
    await shot(s.page, 'design-chat');
    await clickRow(s.page, id);

    /* ----------------------------------- a line that puts itself away */

    // A notice is news. It never blocks, it can always be closed by hand, and
    // it goes on its own once it has been up long enough to read - otherwise
    // the top of the pane fills with lines about things that are over.
    const other = await s.context.newPage();
    const otherErrors = [];
    other.on('console', (msg) => { if (msg.type() === 'error') otherErrors.push(msg.text()); });
    other.on('pageerror', (err) => otherErrors.push(String(err)));
    await other.setViewportSize({ width: 640, height: 480 });
    await other.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
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

    /* --------------------------------------------- Home's chat cards */

    // Home draws every session as a card: on the white panel too, each with
    // its agent's mark, the working chat's mark turning on DS's spinner beat,
    // and a card that is drawn again - a rename - is the same card, its ring
    // never started again.
    await goHome(s.page);
    await s.page.click('#home [data-action="seg"][data-filter="all"]');
    await s.page.waitForSelector('#home .so-home__card[data-id="' + working + '"]', { timeout: 15000 });
    const cards = await s.page.evaluate(() => ({
      home: getComputedStyle(document.getElementById('home')).backgroundColor,
      cards: [...document.querySelectorAll('#home .so-home__card')].map((card) => ({
        id: card.dataset.id, fill: getComputedStyle(card).backgroundColor,
        mark: (card.querySelector('.so-agent') || { dataset: {} }).dataset.agent || 'none',
        ring: (card.querySelector('.so-mark') || { dataset: {} }).dataset.ring || '',
      })),
    }));
    const byId = Object.fromEntries(cards.cards.map((c) => [c.id, c]));
    ok(white(cards.home) && cards.cards.every((c) => white(c.fill)), 'Home and its cards are white', JSON.stringify(cards));
    ok(byId[working] && byId[working].mark === 'claude' && byId[chatId] && byId[chatId].mark === 'fake' && byId[id].mark === 'shell',
      'and every card wears the mark of what it runs', JSON.stringify(cards.cards.map((c) => c.mark)));
    const homeRing = (one) => s.page.evaluate((cid) => {
      const card = document.querySelector('#home .so-home__card[data-id="' + cid + '"]');
      const mark = card && card.querySelector('.so-mark');
      const anim = mark ? mark.getAnimations({ subtree: true })[0] : null;
      return { ring: mark ? mark.dataset.ring || '' : '', at: anim ? Number(anim.currentTime) : -1, start: anim ? Number(anim.startTime) : NaN };
    }, one);
    // The ring is read once it is turning: an animation that has not had its
    // first frame has no start time yet.
    await s.page.waitForFunction((cid) => {
      const mark = document.querySelector('#home .so-home__card[data-id="' + cid + '"] .so-mark');
      const anim = mark ? mark.getAnimations({ subtree: true })[0] : null;
      return !!anim && anim.startTime !== null && Number(anim.currentTime) > 0;
    }, working, { timeout: 10000 }).catch(() => {});
    const cardBefore = await homeRing(working);
    await s.page.evaluate((cid) => { document.querySelector('#home .so-home__card[data-id="' + cid + '"]').wp11Stamp = 'drawn once'; }, working);
    const cardName = workingRow.title + ' on Home';
    await s.context.request.patch(s.url + '/api/sessions/' + working, { data: { title: cardName } });
    await s.page.waitForFunction((arg) => {
      const card = document.querySelector('#home .so-home__card[data-id="' + arg.cid + '"]');
      return !!card && card.querySelector('.so-home__card-title').textContent === arg.name;
    }, { cid: working, name: cardName }, { timeout: 20000 });
    const cardAfter = await homeRing(working);
    const sameCard = await s.page.evaluate((cid) => document.querySelector('#home .so-home__card[data-id="' + cid + '"]').wp11Stamp === 'drawn once', working);
    ok(cardBefore.ring === 'busy' && sameCard && cardAfter.start === cardBefore.start && cardAfter.at > cardBefore.at,
      'the working chat’s card turns, and a rename is drawn into the same card without starting it again',
      JSON.stringify({ before: cardBefore, after: cardAfter, sameCard }));
    await shot(s.page, 'design-home');
    await clickRow(s.page, id);

    /* ------------------------------------------------- the accounts */

    // The Accounts card holds to the same rules: white, a group per agent
    // under that agent's mark, and what is technical - the e-mail, the plan,
    // a key - behind the "i" or nowhere.
    const key = 'sk-design-' + 'd'.repeat(20);
    await s.context.request.post(s.url + '/api/accounts', { data: { agent: 'fake', kind: 'api_key', label: 'Design key', key } });
    await s.page.goto(s.url + '/admin#accountsCard', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#accountList .so-account-group[data-agent="fake"] .so-account', { timeout: 20000 });
    const accountsLook = await s.page.evaluate(() => {
      const card = document.getElementById('accountList').closest('.ds-card');
      return {
        fill: card ? getComputedStyle(card).backgroundColor : 'missing',
        groups: [...document.querySelectorAll('#accountList .so-account-group')].map((g) => ({
          agent: g.dataset.agent, mark: (g.querySelector('.so-account-group__title .so-agent') || { dataset: {} }).dataset.agent || 'none',
          fill: getComputedStyle(g).backgroundColor,
        })),
      };
    });
    ok(white(accountsLook.fill) && accountsLook.groups.every((g) => white(g.fill)), 'the Accounts card is white',
      JSON.stringify({ card: accountsLook.fill, groups: accountsLook.groups.map((g) => g.fill) }));
    ok(accountsLook.groups.length >= 4 && accountsLook.groups.every((g) => g.mark === g.agent),
      'and every agent’s group wears its mark', JSON.stringify(accountsLook.groups.map((g) => g.agent + ':' + g.mark)));
    const accountWords = await s.page.evaluate(() => {
      const walker = document.createTreeWalker(document.getElementById('accountList'), NodeFilter.SHOW_TEXT);
      const parts = [];
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const host = node.parentElement;
        if (!host || host.closest('.ds-tooltip') || !host.checkVisibility()) continue;
        parts.push(node.textContent);
      }
      return parts.join(' ');
    });
    ok(!accountWords.includes(key) && !/\/(tmp|root|home|var)\//.test(accountWords) && !/\ba-[0-9a-f]{12}\b/.test(accountWords),
      'and no key, folder or account id is written on it in words', oneLine(accountWords).slice(0, 200));

    // Rule 1 again, on the other page, and rule 2 on the admin cards.
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('.so-program', { timeout: 20000 });
    const admin = await s.page.evaluate(() => {
      const cards = [...document.querySelectorAll('.ds-card')];
      const fills = new Set(cards.map((c) => getComputedStyle(c).backgroundColor));
      return {
        fills: [...fills],
        body: getComputedStyle(document.querySelector('.ds-panel')).backgroundColor,
        marks: [...document.querySelectorAll('.so-program')].map((c) =>
          (c.querySelector('.so-agent') || { dataset: {} }).dataset.agent || 'none'),
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
        if (!host || host.closest('.ds-tooltip')) continue;
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

const rowSel = (id) => '#sessionList .so-row[data-id="' + id + '"]';

// rowActivity is everything the sidebar says about one session, read out of
// the drawn row rather than out of the API: the state attributes are the
// feature.
const rowActivity = (page, id) => page.evaluate((sel) => {
  const row = document.querySelector(sel);
  if (!row) return null;
  const mark = row.querySelector('.so-mark');
  const label = row.querySelector('.so-row__label');
  // The hourglass is the corner badge of background work or of a verdict
  // being read.
  const glass = row.querySelector('.so-mark__badge[data-kind="tasks"], .so-mark__badge[data-kind="checking"]');
  const glassBox = glass ? glass.getBoundingClientRect() : null;
  const ring = mark ? getComputedStyle(mark, '::after') : null;
  return {
    busy: !!mark && mark.getAttribute('data-ring') === 'busy',
    waiting: !!mark && mark.getAttribute('data-ring') === 'waiting',
    tasks: !!row.querySelector('.so-mark__badge[data-kind="tasks"]'),
    // The hourglass, as drawn: shown at all, and how big.
    glass: glass && getComputedStyle(glass).display !== 'none' && glassBox.width > 0
      ? { w: Math.round(glassBox.width), h: Math.round(glassBox.height) }
      : null,
    unread: row.hasAttribute('data-unread'),
    active: !!row.querySelector('.so-row__open[aria-current="page"]'),
    dots: row.querySelectorAll('.dot').length,
    said: mark ? mark.getAttribute('data-tip') || '' : '',
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
  await page.click(rowSel(id) + ' .so-row__label');
  await page.waitForFunction((want) => location.hash.slice(1) === want, id, { timeout: 15000 });
  await page.waitForSelector(rowSel(id) + ' .so-row__open[aria-current="page"]', { timeout: 15000 });
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
  await page.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#term .xterm', { timeout: 20000 });
  await wait(1200);
  return page;
}

// keepBusy gives the Claude Code chat on screen a turn that takes a
// measurable amount of time: fakeclaude's /slow streams for that long.
async function keepBusy(page, ms) {
  await say(page, work('claude', ms / 1000));
}

// activityFor is one agent's whole story, as a chat: it starts working and
// the row says so, it finishes while nobody is looking, and the name goes
// bold. The chat's journal is the source (REBUILD.md §7: busy is a turn
// running), so this is the same story for all three agents.
async function activityFor(harness) {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    // A second session to stand on. "Finished and nobody saw it" is only a
    // fact about a row that is not the one being looked at.
    const other = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const id = await startChat(s.page, harness);

    // Start from a row that is not already turning, or "it spins while it is
    // working" would be a measurement of the moment before.
    const settled = await awaitRow(s.page, id, (r) => !r.busy, 20000);
    ok(settled.ok, harness + ' settles into idle before it is given anything to do', took(settled));

    await say(s.page, work(harness, 8));
    const spun = await awaitRow(s.page, id, (r) => r.busy, 8000);
    ok(spun.ok, 'the row spins while ' + harness + ' is working', took(spun));
    ok(!spun.seen || spun.seen.ring === null || spun.seen.ring.name === 'ds-spin',
      'and the ring is what turns, not a glyph of its own',
      spun.seen && spun.seen.ring ? spun.seen.ring.name + ' ' + spun.seen.ring.motion : 'no ring');

    // Step off it while it is still working, so the end of the turn happens
    // with nobody watching - which is the whole of what unread means.
    await clickRow(s.page, other);
    const done = await awaitRow(s.page, id, (r) => !r.busy && r.unread, 30000);
    ok(done.ok, 'it stops spinning and its name goes bold when the turn ends', took(done));
    ok(!done.seen || Number(done.seen.weight) >= 500, 'the bold is real weight (DS’s label weight), not a colour',
      done.seen ? done.seen.weight : 'no row');

    await shot(s.page, 'activity-' + harness);
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// tasksFor is the turn that ends while something it started is still
// running: a background shell under Claude Code, a background terminal under
// Codex. The row is neither working nor done - no ring, an hourglass on the
// mark's corner - and the bold and the chime wait for that work to end.
// OpenCode has no such state: its sub-agents run inside the turn that
// started them (AGENT-HOST.md §5.3, Background() is 0).
async function tasksFor(harness) {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const other = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const id = await startChat(s.page, harness);
    const settled = await awaitRow(s.page, id, (r) => !r.busy && !r.tasks, 20000);
    ok(settled.ok, harness + ' settles into idle first', took(settled));

    await say(s.page, '/bg 9');
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
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

const taskclaude = () => tasksFor('claude');
const taskcodex = () => tasksFor('codex');

const activityclaude = () => activityFor('claude');
const activitycodex = () => activityFor('codex');
const activityopencode = () => activityFor('opencode');

// A question is the one state that may sit for an hour: the person it is
// waiting for is driving. So it is drawn differently from working - an amber
// ring that does not turn - and it does not time out. The question is Claude
// Code's ask_user, through the in-process MCP server of the protocol fake,
// sent from another device while this page stands on a Shell.
async function activitywaiting() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const other = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const id = await startChat(s.page, 'claude');
    await clickRow(s.page, other);
    ok((await tellChat(s, id, '/askuser')) === 200, 'Claude Code is told to ask, from another device', '/askuser');

    const asked = await awaitRow(s.page, id, (r) => r.waiting, 20000);
    ok(asked.ok, 'a question draws a ring that does not turn', took(asked));
    // The amber is DS's warning role, read from the page rather than written
    // here, so a new copy of Dashboard-Style does not fail this for a shade.
    const amber = await s.page.evaluate(() => {
      const probe = document.createElement('span');
      probe.style.color = 'var(--ds-warning)';
      document.body.append(probe);
      const colour = getComputedStyle(probe).color;
      probe.remove();
      return colour;
    });
    ok(!asked.seen || (asked.seen.ring && asked.seen.ring.colour === amber),
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

    // A question is silent by nature: somebody is driving. Nothing about it
    // runs out, which is measured rather than reasoned about.
    await wait(34000);
    const still = await rowActivity(s.page, id);
    const server = await chatOf(s, id);
    ok(!!still && still.waiting && still.ring && still.ring.colour === amber && server.asks.length === 1,
      'and thirty-four silent seconds later it is still waiting, its question open',
      JSON.stringify({ row: still && still.said, asks: server.asks.length }));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// The unread mark, and the two things that take it away: opening the chat,
// and sending it a message (AGENT-HOST.md §7, as typing at a terminal does).
async function unread() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);
    const other = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    const id = await startChat(s.page, 'claude');
    await say(s.page, 'hello');
    await awaitLog(s.page, 'you said: hello', 15000);
    await wait(800);
    const watched = await rowActivity(s.page, id);
    ok(!watched.unread && watched.active, 'the chat being looked at is never bold, even after its turn ends',
      JSON.stringify({ unread: watched.unread, active: watched.active }));

    await clickRow(s.page, other);
    ok((await tellChat(s, id, work('claude', 2))) === 200, 'a turn is sent from another device', work('claude', 2));
    const bold = await awaitRow(s.page, id, (r) => r.unread, 30000);
    ok(bold.ok, 'a turn that ends where nobody is looking makes its name bold', took(bold));
    ok(!bold.seen || Number(bold.seen.weight) >= 500, 'the bold is real weight (DS’s label weight), not a colour',
      bold.seen ? bold.seen.weight : 'no row');

    await clickChat(s.page, id);
    const opened = await awaitRow(s.page, id, (r) => !r.unread, 10000);
    ok(opened.ok, 'opening the chat is seeing it, and clears the mark', took(opened));
    const server = await sessionRow(s, id);
    ok(server.activity && server.activity.unread === false,
      'and the server agrees, so every other tab stops bolding it too', JSON.stringify(server.activity));

    // The other way it goes away: somebody sent that chat a message. The
    // server decides, on the message it took, so the tab that did not send
    // it hears about it in the ordinary way.
    await clickRow(s.page, other);
    await tellChat(s, id, work('claude', 2));
    const again = await awaitRow(s.page, id, (r) => r.unread, 30000);
    ok(again.ok, 'a second turn ends unseen and the name is bold again', took(again));
    // The message is a long turn, so the row is looked at while it runs:
    // the mark went with the message, and comes back only when that turn
    // ends unseen too.
    const sent = await tellChat(s, id, work('claude', 6));
    const cleared = await awaitRow(s.page, id, (r) => !r.unread && r.busy, 5000);
    ok(sent === 200 && cleared.ok, 'a message sent to it clears it everywhere', took(cleared));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
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
    // The page shows its notification through the worker's registration
    // wherever a worker is running (notify.js show), which in the suite it
    // always is - and the headless shell has the real permission denied, so
    // the real call would refuse. It is caught here in the same list.
    if (window.ServiceWorkerRegistration) {
      ServiceWorkerRegistration.prototype.showNotification = function (title, options) {
        window.__notes.push({ title, options: options || {} });
        return Promise.resolve();
      };
    }

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
  const node = document.querySelector('#optionsDialog [data-option="notify"]');
  return !!node && String(node.checked) === want;
}, on ? 'true' : 'false', { timeout: 5000 });

const rang = (page) => page.evaluate(() => ({
  beeps: (window.__beeps || []).length,
  notes: (window.__notes || []).slice(),
  asked: window.__asked || 0,
  toasts: (window.__toasts || []).slice(),
}));

// drive makes one turn happen and waits for it to be over, which is the edge
// the whole feature hangs off. It is a Claude Code chat and not a shell
// because a shell has no turns: nothing is asked of it and nothing is
// reported back, so its row carries no status and it never chimes.
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
    const id = await startChat(s.page, 'claude');
    // An agent comes up before anybody has asked it anything, and that is
    // not a turn ending: a chat that has just been made has been asked
    // nothing, so nothing it does on its way to being ready is worth a sound
    // on a phone in somebody's pocket.
    const settled = await awaitRow(s.page, id, (r) => !r.busy, 20000);
    ok(settled.ok, 'the new session settles into idle', took(settled));
    await wait(1200);
    const started = await rang(s.page);
    ok(started.beeps === opened.beeps && started.notes.length === opened.notes.length,
      'and starting one says nothing at all, with both switches on',
      (started.beeps - opened.beeps) + ' chime(s), '
      + (started.notes.length - opened.notes.length) + ' note(s)');
    const title = await s.page.$eval(rowSel(id) + ' .so-row__label', (n) => n.textContent);

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
    ok(await drive(s, id, 3000), 'a turn starts and finishes', work('claude', 3));
    let after = await rang(s.page);
    ok(after.beeps - before.beeps === 1, 'it chimed exactly once',
      (after.beeps - before.beeps) + ' chime(s)');
    const note = after.notes[after.notes.length - 1];
    ok(after.notes.length - before.notes.length === 1 && !!note && note.title === title
      && /^Finished · s1 s2/.test(note.options.body),
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
    ok(await drive(s, id, 3000), 'a second turn starts and finishes', work('claude', 3));
    after = await rang(s.page);
    ok(after.beeps - before.beeps === 0 && after.notes.length - before.notes.length === 1,
      'muted, it is silent and still notifies',
      (after.beeps - before.beeps) + ' chime(s), '
      + (after.notes.length - before.notes.length) + ' note(s)');

    await option(s.page, 'notify');
    await awaitNotify(s.page, false);
    await closeOptions(s.page);
    before = await rang(s.page);
    ok(await drive(s, id, 3000), 'a third turn starts and finishes', work('claude', 3));
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
    await reloadBack(s.page);
    await chatUp(s.page);
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

// The notice of the chat on screen is in its dock, where a terminal's is
// over its pane.
const DOCK_NOTICE = '#chat .so-chat__dock .so-notice';

const noticeText = (page) => page.evaluate((sel) => {
  const host = document.querySelector(sel);
  if (!host || host.hidden) return '';
  const line = host.querySelector('.ds-alert__text');
  return line ? line.textContent : '';
}, DOCK_NOTICE);

// A chat names itself. The first message it is sent is what the sidebar row
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
    await signIn(s);
    await open(s);
    const id = await startChat(s.page, 'claude');

    const before = await s.page.$eval(rowSel(id) + ' .so-row__label', (n) => n.textContent.trim());
    ok(before.startsWith('Claude Code'), 'it starts life under the placeholder name', before);

    // The agent coming up is no message, and a name made before the first
    // one would be the name of the program.
    const settled = await awaitRow(s.page, id, (r) => !r.busy, 20000);
    ok(settled.ok, 'it settles before it is given anything to do', took(settled));
    await wait(1500);
    ok(asked() === 0, 'the start-up is not named after', asked() + ' calls');

    // The first message: the moment it is sent, the name is asked for and
    // pulses until it arrives.
    await keepBusy(s.page, 3000);
    const spun = await awaitRow(s.page, id, (r) => r.busy, 10000);
    ok(spun.ok, 'it goes to work on the first thing it is asked', took(spun));
    await s.page.waitForSelector(rowSel(id) + ' .so-row__label[data-titling]', { timeout: 10000 });
    ok(true, 'the row pulses while the name is being thought of', 'label.titling');
    const pulsing = await s.page.$eval('#sessionTitle', (n) => n.hasAttribute('data-titling'));
    ok(pulsing, 'and so does the header of the session being watched', String(pulsing));

    await s.page.waitForFunction((arg) => {
      const node = document.querySelector(arg.sel);
      return !!node && node.textContent.trim() === arg.want && !node.hasAttribute('data-titling');
    }, { sel: rowSel(id) + ' .so-row__label', want }, { timeout: 30000 });
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
    const still = await s.page.$eval(rowSel(id) + ' .so-row__label', (n) => n.textContent.trim());
    ok(still === want, 'and the name it was given stayed', still);
    await shot(s.page, 'session-title');

    // The arrow beside the name asks again, from the chat's journal, whatever
    // it is called.
    const again = 'Parser work, named again';
    stub.replies.push(again);
    const arrow = await s.page.$eval('#titleRegen', (n) => !n.hidden);
    ok(arrow, 'the header has an arrow beside the name', String(arrow));
    await s.page.click('#titleRegen');
    const renamed = await s.page.waitForFunction((w) => document.getElementById('sessionTitle').textContent.trim() === w
      && !document.getElementById('titleRegen').disabled, again, { timeout: 15000 }).then(() => true, () => false);
    ok(renamed && asked() === 2, 'pressing it names the session again', await s.page.$eval('#sessionTitle', (n) => n.textContent.trim())
      + ', ' + asked() + ' calls');

    // A prepared session is named after its prompt the first time its field
    // is left - once.
    const made = await s.context.request.post(s.url + '/api/sessions', { data: { prestart: true, harness: 'claude' } });
    ok(made.ok() && (await made.json()).session.kind === 'chat', 'a prepared chat is made', made.status());
    const prepared = (await made.json()).session.id;
    await s.page.click(rowSel(prepared));
    await s.page.waitForSelector('#prestart:not([hidden]) #psPrompt:not([readonly])', { timeout: 15000 });
    const planned = 'Prepared login fix';
    stub.replies.push(planned);
    await s.page.fill('#psPrompt', 'Fix the login redirect after the session expires');
    await s.page.$eval('#psPrompt', (n) => n.blur());
    const named = await s.page.waitForFunction((arg) => {
      const label = document.querySelector(arg.sel);
      return !!label && label.textContent.trim() === arg.want
        && document.getElementById('sessionTitle').textContent.trim() === arg.want;
    }, { sel: rowSel(prepared) + ' .so-row__label', want: planned }, { timeout: 15000 }).then(() => true, () => false);
    ok(named, 'leaving its prompt field names a prepared session after the prompt',
      await s.page.$eval(rowSel(prepared) + ' .so-row__label', (n) => n.textContent.trim()));
    await shot(s.page, 'session-title-prestart');
    await s.page.focus('#psPrompt');
    await s.page.type('#psPrompt', ' and keep the page');
    await s.page.$eval('#psPrompt', (n) => n.blur());
    await wait(1500);
    ok(asked() === 3, 'leaving it again does not ask again', asked() + ' calls');
    await clickChat(s.page, id);

    // A gateway that refuses is said out loud, in a dialog, and the pulse
    // stops.
    stub.refuse = 401;
    const failing = await startChat(s.page, 'claude');
    const quiet = await awaitRow(s.page, failing, (r) => !r.busy, 20000);
    ok(quiet.ok, 'a second chat settles', took(quiet));
    await keepBusy(s.page, 1500);
    await s.page.waitForSelector('dialog.ds-dialog[open]', { timeout: 15000 });
    const said = await s.page.$eval('dialog.ds-dialog[open]', (n) => n.textContent);
    ok(/Could not name the session/.test(said) && /API key/.test(said),
      'a failed naming opens a dialog that says why', said.trim().slice(0, 160));
    const stillPulsing = await s.page.$$eval(rowSel(failing) + ' .so-row__label[data-titling]', (n) => n.length);
    ok(stillPulsing === 0, 'and the row stops pulsing', String(stillPulsing));
    await s.page.click('dialog.ds-dialog[open] .ds-dialog__actions .ds-btn');

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
async function watchTicker(page, sel = '#tickerWindow') {
  await page.evaluate((at) => {
    window.__ticker = [];
    const host = document.querySelector(at);
    new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          const said = (node.textContent || '').trim();
          if (said) window.__ticker.push(said);
        }
      }
    }).observe(host, { childList: true });
  }, sel);
  return () => page.evaluate(() => window.__ticker.slice());
}

/* ------------------------------------------------------- 34. status-speak */

// The Status button: one request, a spinner while it is being made, the answer
// on screen as words, and the same words handed to the voice.
async function statusspeak() {
  const stub = await openRouterStub({ text: 'Claude Code has finished and is waiting for your next message.' });
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
    const id = await startChat(s.page, 'claude');
    // A chat is told from its journal, so it has said something first.
    ok((await answerTo(s, id, 'run the tests')) === 'you said: run the tests', 'the chat has done something', 'run the tests');

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
      () => document.getElementById('statusBtn').getAttribute('aria-busy') === 'true',
      null, { timeout: 5000 }).then(() => true).catch(() => false);
    ok(spinning, 'the button says the tap landed before the answer arrives',
      spinning ? 'the spinner is on it' : 'nothing happened');

    // And what turns is the busy face's spinner, never the mark itself. The
    // drawing is a screen being read out; a screen that is turning is a
    // drawing of nothing, and it was read as one.
    const ring = await s.page.evaluate(() => {
      const btn = document.getElementById('statusBtn');
      const spinner = btn.querySelector('.ds-btn__face--busy .ds-spinner');
      const mark = btn.querySelector('.ds-btn__face:not(.ds-btn__face--busy) svg');
      return {
        spinner: !!spinner,
        turning: spinner ? spinner.getAnimations({ subtree: true }).length > 0
          || getComputedStyle(spinner).animationName !== 'none'
          || getComputedStyle(spinner, '::before').animationName !== 'none' : false,
        onTheMark: mark ? getComputedStyle(mark).animationName : 'none',
      };
    });
    ok(ring.spinner && ring.turning && ring.onTheMark === 'none',
      'and it is a spinner that turns, not the drawing', JSON.stringify(ring));

    // Turning decoration off must not turn off the one thing that says the
    // answer is still being made: DS loaders run at their token speed under
    // reduced motion too.
    await s.page.emulateMedia({ reducedMotion: 'reduce' });
    await wait(120);
    const calm = await s.page.evaluate(() => {
      const node = document.getElementById('statusBtn');
      const spinner = node.querySelector('.ds-btn__face--busy .ds-spinner');
      return { busy: node.getAttribute('aria-busy'), running: !!spinner && spinner.getAnimations({ subtree: true }).length > 0 };
    });
    ok(calm.busy !== 'true' || calm.running,
      'with reduced motion it still turns, rather than standing still', JSON.stringify(calm));
    await s.page.emulateMedia({ reducedMotion: null });

    await s.page.waitForFunction((sel) => {
      const host = document.querySelector(sel);
      return !!host && !host.hidden && host.dataset.kind === 'status';
    }, DOCK_NOTICE, { timeout: 40000 });
    const shown = await noticeText(s.page);
    ok(shown.includes('finished') && shown === stub.text,
      'what the model said is on screen, in words', oneLine(shown));
    // The line is on a timer, so everything about it is read while it is up.
    const tip = await s.page.$eval(DOCK_NOTICE + ' .so-info', (n) => n.getAttribute('data-tip')).catch(() => '');
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
      () => document.getElementById('statusBtn').getAttribute('aria-busy') !== 'true',
      null, { timeout: 10000 }).then(() => true).catch(() => false);
    ok(cleared, 'and the spinner stops when there is nothing left to wait for',
      cleared ? 'stopped' : 'still turning');

    // And the line does not sit there for ever: it is one sentence, it has
    // been read, and the dock is the composer's again.
    const gone = await s.page.waitForFunction((sel) => document.querySelector(sel).hidden,
      DOCK_NOTICE, { timeout: 9000 }).then(() => true).catch(() => false);
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
// arrive in the chat's ticker in the order they happen: the chat is read from
// its journal, the model is asked, the answer is spoken, and the answer
// itself is the last line.
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
    const id = await startChat(s.page, 'claude');
    ok((await answerTo(s, id, '/tool')) === 'The command printed two lines.', 'the chat has done something', '/tool');
    const lines = await watchTicker(s.page, '#chat .so-chat__dock .so-ticker__line');

    await s.page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 15000 });
    await s.page.click('#statusBtn');
    const arrived = await s.page.waitForFunction(
      (want) => (window.__ticker || []).some((l) => l === want),
      stub.text, { timeout: 45000 }).then(() => true).catch(() => false);
    const said = await lines();
    ok(arrived, 'the answer itself is the last thing the ticker says', oneLine(said.join(' → ')));

    const order = said.map((l) => (
      /^Reading the chat$/.test(l) ? 'capturing'
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
      const host = document.querySelector('#chat .so-chat__dock .so-ticker');
      return {
        hidden: host.hidden,
        height: Math.round(host.querySelector('.so-ticker__line').getBoundingClientRect().height),
        fill: getComputedStyle(host).backgroundColor,
      };
    });
    ok(!window.hidden && window.height <= 24, 'it is one line high and no more',
      JSON.stringify(window));
    ok(window.fill !== 'rgba(0, 0, 0, 0)' && window.fill !== 'transparent',
      'on a ground of its own, so it reads apart from the conversation', window.fill);

    // And it gives the window back: outside auto mode, with nothing happening,
    // there is nothing to say.
    const gone = await s.page.waitForFunction(() => document.querySelector('#chat .so-chat__dock .so-ticker').hidden,
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
    pillInBar: !!pill && !!pill.closest('.ds-topbar'),
    pillText: pill ? pill.textContent.trim() : '',
    pillBox: box('micBtn'),
    pillDisabled: !!pill && pill.disabled,
    panel: !!node('chatPanel'),
    open: !!sheet && sheet.open,
    keep: box('recSend'),
    drop: box('recCancel'),
    labels: ['recSend', 'recCancel'].map((id) => (node(id) || {}).textContent || '').join(','),
    bars: document.querySelectorAll('#recMeter .so-meter__bar').length,
    clock: clock ? clock.textContent.trim() : '',
  };
});

// The microphone, which is the only way into a chat that is not a keyboard:
// a pill in the top bar that opens a sheet, two endings while it records, a
// discarded recording that costs nothing, and a kept one whose words are put
// in the chat's composer - and left there, because the Send that sends them
// is the person's.
async function dictate() {
  const stub = await openRouterStub({ text: 'please run the dictated tests' });
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
    const id = await startChat(s.page, 'claude');
    const composer = () => s.page.$eval('#chat .so-composer__input', (n) => n.value);

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
      const bar = document.querySelector('.ds-topbar').getBoundingClientRect();
      const parts = [...document.querySelectorAll('.ds-topbar > *')]
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
    // DS has no microphone glyph, so the pill keeps its one short word there
    // and gives up padding instead; what makes it findable by a thumb is its
    // height.
    ok(tight.pill.w >= 44 && tight.pill.h >= 44 && tight.word !== 'none',
      'and the microphone kept its word and its size', JSON.stringify(tight));
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
      const bar = document.querySelector('.ds-topbar').getBoundingClientRect();
      const middle = (box) => (box.top + box.bottom) / 2;
      // Only what is actually on screen: a control the bar has hidden lays
      // out to nothing, and nothing is not misaligned. Copy, paste and upload
      // float under the bar on a phone, on purpose (transfer.js).
      const parts = [...document.querySelectorAll('.ds-topbar > :not(#transfer)')]
        .map((n) => n.getBoundingClientRect())
        .filter((box) => box.width > 0 && box.height > 0);
      const buttons = [...document.querySelectorAll('#recSheet .so-rec__action')]
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
    // reaches the composer.
    await s.page.click('#recCancel');
    await s.page.waitForSelector('#recSheet:not([open])', { state: 'attached', timeout: 10000 });
    await wait(800);
    const after = await micRow(s.page);
    ok(!after.open && !after.pillDisabled, 'Cancel closes it and gives the microphone back',
      JSON.stringify(after));
    ok(transcribes.length === 0, 'and it costs nothing: nothing was transcribed',
      transcribes.length + ' transcribe(s)');
    ok((await composer()) === '', 'nothing reached the composer', JSON.stringify(await composer()));

    // Send: transcribed once, and the words are put in the composer verbatim.
    await s.page.click('#micBtn');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    await wait(1300);
    await s.page.click('#recSend');
    const landed = await s.page.waitForFunction((want) => document.querySelector('#chat .so-composer__input').value === want,
      stub.text, { timeout: 60000 }).then(() => true, () => false);
    ok(landed, 'Send puts what was heard in the composer, verbatim and unconfirmed', JSON.stringify(await composer()));
    ok(transcribes.length === 1, 'and it was transcribed exactly once',
      transcribes.length + ' request(s)');

    // And it stops there. The words are in the composer, not sent: the agent
    // has been told nothing, because nothing pressed Send for it.
    await wait(1500);
    const unsent = userTexts(await historyOf(s, id));
    ok(unsent.length === 0 && (await composer()) === stub.text, 'the words wait in the composer and were not sent',
      JSON.stringify({ sent: unsent, composer: await composer() }));

    // The Send is the person's, and it is the composer's own button.
    await s.page.click('#chat .so-composer__send');
    const ran = await awaitLog(s.page, 'you said: ' + stub.text, 30000);
    ok(ran && userTexts(await historyOf(s, id)).join('|') === stub.text,
      'and pressing it sends what was dictated, once, and the agent answers', oneLine(await lastAnswer(s.page)));

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
    const id = await startChat(s.page, 'claude');
    // A phone's composer waits for its keyboard button: the message is sent
    // the way it sends it.
    await tellChat(s, id, 'run the tests');
    ok(await awaitLog(s.page, 'you said: run the tests'), 'the chat has done something', 'run the tests');

    // A recording is running, and Status is pressed: the answer arrives on
    // the screen, and it is not read into the microphone.
    await s.page.click('#micBtn');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    // The recording sheet is modal, so Status is pressed through the page
    // rather than through the pointer the backdrop is holding.
    await s.page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 15000 });
    await s.page.$eval('#statusBtn', (n) => n.click());
    const shown = await s.page.waitForFunction((sel) => {
      const host = document.querySelector(sel);
      return !!host && !host.hidden && host.dataset.kind === 'status';
    }, DOCK_NOTICE, { timeout: 45000 }).then(() => true).catch(() => false);
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
    await s.page.waitForFunction(() => document.body.hasAttribute('data-conn-lost'),
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
    const back = await s.page.waitForFunction(() => !document.body.hasAttribute('data-conn-lost'),
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
    await s.page.waitForSelector('dialog.so-rename[open]', { timeout: 10000 });
    await s.page.click('dialog.so-rename .ds-dialog__actions .ds-btn--text');
    await s.page.waitForSelector('dialog.so-rename', { state: 'detached', timeout: 10000 });
    const dialogMark = 'dialog-' + mark;
    await s.page.keyboard.type('echo ' + dialogMark);
    await s.page.keyboard.press('Enter');
    ok(await awaitScreen(s.page, dialogMark, 15000), 'typing works after a dialog was opened and closed',
      oneLine(await screen(s.page)));

    // And the ⋮ options: two of its switches pressed - the focus is on the
    // switch now, inside a dialog that stays open - and then dismissed the
    // way a stray tap dismisses it, on the pane beside it. Both presses are
    // the key bar's, so the device ends where it started.
    await option(s.page, 'keybar');
    await option(s.page, 'keybar');
    const beside = await s.page.$eval('#term .xterm-screen', (n) => {
      const r = n.getBoundingClientRect();
      return { x: r.left + 40, y: r.top + r.height - 40 };
    });
    await s.page.mouse.click(beside.x, beside.y);
    await s.page.waitForSelector('#optionsDialog[open]', { state: 'detached', timeout: 10000 });
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

    await s.page.click('.so-row[data-id="' + one + '"]');
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

    await s.page.click('.so-row[data-id="' + two + '"]');
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
    name: (node.querySelector('.so-folder__name') || {}).textContent || '',
    title: node.querySelector('.so-folder__head')?.getAttribute('data-tip') || '',
    open: (node.querySelector('.so-folder__head') || {}).getAttribute
      ? node.querySelector('.so-folder__head').getAttribute('aria-expanded') : '',
    count: (node.querySelector('.so-folder__count') || {}).textContent || '',
    shown: !!node.querySelector('.so-folder__rows') && node.querySelector('.so-folder__rows').checkVisibility(),
    stamp: node.wp1Stamp || '',
    rows: [...node.querySelectorAll('.so-row')].map((row) => ({
      id: row.dataset.id,
      row: row.querySelector('.so-row__label').textContent,
      done: row.dataset.status === 'done',
      stamp: row.wp1Stamp || '',
      seen: row.checkVisibility(),
    })),
    other: node.classList.contains('so-folder') ? '' : node.className || node.tagName,
  })));

// under is which folder each row is in, by the name the row reads.
function under(shown) {
  const out = new Map();
  for (const folder of shown) for (const row of folder.rows) out.set(row.row, folder.name);
  return out;
}

// stampAll marks every folder and row on screen, so the next render can be
// asked whether it kept them or built new ones. A property, not an
// attribute: the list is morphed, and a morph syncs every attribute to what
// was drawn.
const stampAll = (page) => page.evaluate(() => {
  for (const folder of document.getElementById('sessionList').children) {
    folder.wp1Stamp = folder.dataset.folder || 'other';
    for (const row of folder.querySelectorAll('.so-row')) row.wp1Stamp = row.dataset.id;
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
    await s.page.waitForFunction((n) => document.querySelectorAll('#sessionList .so-row').length === n,
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
    const doneShelf = '#sessionList .so-folder[data-folder="' + projects.socrates + '"] .so-folder--shelf[data-shelf="done"] .so-folder__head';
    const shelfState = () => s.page.$eval(doneShelf, (head) => ({
      open: head.getAttribute('aria-expanded'),
      shown: head.closest('.so-folder--shelf').querySelector('.so-folder__rows').checkVisibility(),
      count: head.querySelector('.so-folder__count').textContent,
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
    const browserHead = '#sessionList .so-folder[data-folder="' + projects.browser + '"] .so-folder__head';
    const caretBefore = await s.page.$eval(browserHead + ' .ds-chevron', (n) => getComputedStyle(n).transform + ' ' + getComputedStyle(n).rotate);
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'false', browserHead, { timeout: 5000 });
    const shut = await listing(s.page);
    // The caret turns rather than jumps, so it is waited for: what is asserted
    // is where it ends up, not where it was a frame after the click.
    await s.page.waitForFunction((sel) => getComputedStyle(document.querySelector(sel + ' .ds-chevron'))
      .rotate === 'none', browserHead, { timeout: 5000 });
    const caretAfter = await s.page.$eval(browserHead + ' .ds-chevron', (n) => getComputedStyle(n).transform + ' ' + getComputedStyle(n).rotate);
    ok(shut[0].open === 'false' && !shut[0].shown && shut[0].rows.every((r) => !r.seen),
      'a folder that is shut hides its sessions and says so to a reader that cannot see it',
      shut[0].open + ', rows shown: ' + shut[0].rows.filter((r) => r.seen).length);
    ok(shut[0].count === '2' && caretBefore !== caretAfter,
      'and says how many are behind it, with the caret turned', shut[0].count + ', ' + caretAfter);
    ok(shut.slice(1).every((f) => f.shown), 'the other folders are untouched',
      shut.filter((f) => f.shown).map((f) => f.name).join(' | '));
    const visibleCount = await s.page.$$eval('#sessionList .so-folder__count',
      (nodes) => nodes.filter((n) => n.checkVisibility()).length);
    ok(visibleCount === 1, 'an open folder counts nothing: the rows under it are the count', visibleCount);
    await shot(s.page, 'folders-shut');

    // It is a fact about this device, so a reload finds it the way it was
    // left - and the sessions in it are still there, not gone.
    await stampAll(s.page);
    await open(s);
    await s.page.waitForFunction((n) => document.querySelectorAll('#sessionList .so-row').length === n,
      6, { timeout: 20000 });
    const reloaded = await listing(s.page);
    ok(reloaded[0].name === 'projects/browser' && reloaded[0].open === 'false' && !reloaded[0].shown
      && reloaded[0].rows.length === 2,
      'a reload finds the folder shut, with its sessions still in it',
      reloaded[0].name + ': ' + reloaded[0].open + ', ' + reloaded[0].rows.length + ' rows');

    // Opening the session that is inside it is what opens the folder: a row
    // nobody can see is a session nobody can get back to.
    await s.page.goto(s.url + '/#open=' + ids.browserNew, { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#term .xterm', { timeout: 20000 });
    await s.page.waitForFunction((id) => {
      const row = document.querySelector('.so-row[data-id="' + id + '"]');
      return !!row && !!row.querySelector('.so-row__open[aria-current="page"]') && row.checkVisibility();
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
    await s.page.waitForFunction((id) => !!document.querySelector('.so-row[data-id="' + id + '"]'),
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
    // It takes a chat, because a shell is one of the two programs nothing is
    // asked of: its row carries no status, so there would be nothing for a
    // shut folder to roll up. A chat in the project's directory is in the
    // project's folder like any session.
    const agent = await makeChat(s, 'claude', { title: 'Browser agent', workdir_mode: 'custom', workdir: projects.browser });
    await s.page.evaluate(() => window.dispatchEvent(new Event('online')));
    await s.page.waitForFunction((id) => !!document.querySelector('.so-row[data-id="' + id + '"]'),
      agent, { timeout: 20000 });
    ok(under(await listing(s.page)).get('Browser agent') === 'projects/browser',
      'a chat is in the folder of the directory it works in', under(await listing(s.page)).get('Browser agent'));
    await clickChat(s.page, agent);
    const ready = await awaitRow(s.page, agent, (r) => !r.busy, 20000);
    ok(ready.ok, 'the chat in the project settles before it is given anything', took(ready));
    await say(s.page, work('claude', 6));
    const busyRow = await awaitRow(s.page, agent, (r) => r.busy, 10000);
    ok(busyRow.ok, 'the chat in the project is working', took(busyRow));
    await clickRow(s.page, ids.looseNew);
    await s.page.click(browserHead);
    await s.page.waitForFunction((sel) => document.querySelector(sel)
      .getAttribute('aria-expanded') === 'false', browserHead, { timeout: 5000 });
    const rollup = (page) => page.evaluate((sel) => {
      const head = document.querySelector(sel);
      const dot = head.querySelector('.so-folder__dot');
      return {
        busy: head.getAttribute('data-attention') === 'busy',
        waiting: head.getAttribute('data-attention') === 'waiting',
        unread: head.hasAttribute('data-unread'),
        dot: dot.checkVisibility(),
        count: head.querySelector('.so-folder__count').textContent,
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

    // The head is a label, not a band: no fill of its own on the drawer's
    // backdrop, and no lines - DS's drawer has none; the space between the
    // folders is what separates them.
    const style = await s.page.evaluate(() => [...document.querySelectorAll('#sessionList > .so-folder > .so-folder__bar')]
      .map((node) => {
        const css = getComputedStyle(node.querySelector('.so-folder__head'));
        return { fill: css.backgroundColor, top: getComputedStyle(node).borderTopWidth, size: css.fontSize };
      }));
    ok(style.length > 1 && style.every((one) => one.fill === 'rgba(0, 0, 0, 0)' && one.top === '0px'),
      'a head is a quiet label with no fill and no line', JSON.stringify(style));

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
    ok(thumb.height >= 44 && thumb.width >= 180 && thumb.inside,
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
    await s.page.click('#keybar .so-key[data-key="Paste"]');
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
    const toasted = await s.page.evaluate(() => [...document.querySelectorAll('.ds-toast')].map((t) => t.textContent));
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
  const copies = [...document.querySelectorAll('.so-kb-copy')];
  const copy = copies[0];
  const r = btn.getBoundingClientRect();
  const c = copy ? copy.getBoundingClientRect() : null;
  return {
    shown: !!btn.checkVisibility && btn.checkVisibility(),
    open: btn.getAttribute('aria-pressed'),
    filled: btn.getAttribute('aria-pressed') === 'true',
    label: btn.getAttribute('aria-label'),
    pane: muted(pane),
    paneOpen: !!pane && !muted(pane),
    focus: active ? (active.closest('#term') ? 'pane' : active.closest('dialog') ? 'dialog' : active.localName) : null,
    focusMuted: !!active && muted(active),
    fields: [...document.querySelectorAll('input.ds-input')]
      .filter((n) => n.checkVisibility && n.checkVisibility())
      .map((n) => ({ id: n.id || n.className, muted: muted(n) })),
    mics: [...document.querySelectorAll('.so-field-row')]
      .filter((n) => n.checkVisibility && n.checkVisibility())
      .map((row) => ({ field: !!row.querySelector('input'), mic: !!row.querySelector('.so-mic') })),
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
    // Its dialog is modal; Escape puts it away.
    await closeOptions(s.page);

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

    await s.page.click('#newSessionSheet .so-kb-copy');
    await wait(200);
    const typing = await keyboardable(s.page);
    ok(typing.open === 'true' && typing.copy && typing.copy.open === 'true'
      && typing.focus === 'dialog' && !typing.focusMuted,
      'pressed there, it raises the keyboard for the dialog\'s field',
      JSON.stringify({ open: typing.open, copy: typing.copy, focus: typing.focus, muted: typing.focusMuted }));
    await s.page.click('#newSessionSheet .so-kb-copy');
    await wait(200);
    const shut = await keyboardable(s.page);
    ok(shut.open === 'false' && shut.fields.every((f) => f.muted),
      'and pressed again, puts it away', JSON.stringify({ open: shut.open, fields: shut.fields }));

    // A field is still filled by speaking to it. What comes back is what would
    // have been typed: it goes in the field and the field is told, so the list
    // filters exactly as it does under a finger.
    await s.page.click('#nsDir .so-mic');
    await s.page.waitForSelector('#recSheet[open]', { timeout: 15000 });
    await wait(1200);
    await s.page.click('#recSend');
    const heard = await s.page.waitForFunction(
      (want) => document.querySelector('#nsDir .ds-combo__input').value === want,
      stub.text, { timeout: 60000 }).then(() => true).catch(() => false);
    ok(heard, 'a muted field is filled by speaking into it',
      await s.page.$eval('#nsDir .ds-combo__input', (n) => n.value));
    await s.page.click('#newSessionSheet .ds-sheet__title');
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
    await s.page.waitForSelector('dialog.so-rename[open]', { timeout: 10000 });
    await wait(400);
    const rename = await keyboardable(s.page);
    ok(rename.fields.length === 1 && rename.fields[0].muted && rename.mics.length === 1
      && rename.copy && rename.copy.shown && rename.copy.over,
      'so is the rename dialog: its field muted, with a microphone, and the button over it',
      JSON.stringify({ fields: rename.fields, mics: rename.mics, copy: rename.copy }));
    await s.page.click('dialog.so-rename .so-kb-copy');
    await wait(200);
    const renaming = await keyboardable(s.page);
    ok(renaming.open === 'true' && renaming.focus === 'dialog' && !renaming.focusMuted,
      'and the button raises the keyboard for it', JSON.stringify({ open: renaming.open, focus: renaming.focus }));
    await s.page.click('dialog.so-rename .so-kb-copy');
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
    await s.page.waitForSelector('dialog.so-rename', { state: 'detached', timeout: 10000 }).catch(() => {});
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
    await deskPage.goto(s.url + '/#open=' + openId, { waitUntil: 'domcontentloaded' });
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
    await carPage.goto(s.url + '/#open=' + openId, { waitUntil: 'domcontentloaded' });
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

    /* ------------------------------------------------ a chat's composer */

    // A chat's composer is a field like the pane: a tap on it does not raise
    // the keyboard, the button does - for the composer, with the focus in it
    // - and a second press puts it away again.
    const chat = await startChat(s.page, 'claude');
    await wait(400);
    const composer = () => s.page.evaluate(() => {
      const n = document.querySelector('#chat .so-composer__input');
      return {
        muted: n.getAttribute('inputmode') === 'none' && n.readOnly,
        focus: document.activeElement === n,
        open: document.getElementById('kbBtn').getAttribute('aria-pressed'),
      };
    });
    const shutComposer = await composer();
    await s.page.click('#chat .so-composer__input');
    await wait(200);
    const tappedComposer = await composer();
    ok(shutComposer.muted && shutComposer.open === 'false' && tappedComposer.muted && tappedComposer.focus,
      'a chat’s composer is muted until the button: a tap on it takes the focus with the keyboard shut',
      JSON.stringify({ shut: shutComposer, tapped: tappedComposer }));
    await s.page.click('#kbBtn');
    await wait(200);
    const upComposer = await composer();
    ok(upComposer.open === 'true' && !upComposer.muted && upComposer.focus,
      'the button raises the keyboard for the composer, with the focus in it', JSON.stringify(upComposer));
    await s.page.keyboard.type('typed on the keyboard');
    await s.page.click('#chat .so-composer__send');
    ok(await awaitLog(s.page, 'you said: typed on the keyboard', 15000), 'and what is typed there is sent',
      oneLine(await lastAnswer(s.page)));
    await s.page.click('#kbBtn');
    await wait(200);
    const downComposer = await composer();
    ok(downComposer.open === 'false' && downComposer.muted, 'a second press puts it away again', JSON.stringify(downComposer));
    void chat;

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
      const bar = document.querySelector('.ds-topbar').getBoundingClientRect();
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
    await s.page.waitForSelector('#copySheet[open]', { timeout: 5000 });
    const shown = await s.page.$eval('#copyArea', (n) => n.value);
    ok(shown.includes(line), 'with nothing selected, Copy opens the pane\'s text', shown.slice(-80));
    await s.page.click('#copyAll');
    await s.page.waitForSelector('#copySheet:not([open])', { state: 'attached', timeout: 5000 });
    const copied = await s.page.evaluate(() => navigator.clipboard.readText());
    ok(copied === shown, 'and Copy all puts all of it on the clipboard', copied.length + ' of ' + shown.length + ' chars');

    /* ------------------------------------------------------- on a phone */

    const phone = await s.browser.newContext({
      viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, colorScheme: 'light',
    });
    await phone.addCookies(await s.context.cookies());
    const phonePage = await phone.newPage();
    await phonePage.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
    await phonePage.waitForSelector('#transfer:not([hidden])', { timeout: 20000 });
    await wait(600);
    const placed = await phonePage.evaluate(() => {
      const bar = document.querySelector('.ds-topbar');
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

/* ------------------------------------------------- PreStart and the bucket */

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

// panelBecameChat waits for the panel to give way to the chat, which is what
// a start - pressed here, elsewhere, or by the clock - looks like from the
// page.
const panelBecameChat = (page, timeout) => page.waitForFunction(() => {
  const panel = document.getElementById('prestart');
  return (!panel || panel.hidden) && !document.getElementById('chat').hidden
    && !!document.querySelector('#chat .so-composer__input');
}, null, { timeout });

// The prompt a prepared chat is written with: several lines, and every
// character a shell would take for its own, quotes and a line that looks
// like a flag - it has to reach the agent as the first message exactly as it
// was written.
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

    // The sheet: PreStart is offered for an agent, not for a shell, and turns
    // the button into Prepare. A prepared session is always a chat.
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.waitForSelector('#newSessionSheet .so-settings__program[data-value="shell"]', { timeout: 10000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="shell"]');
    const forShell = await page.$eval('#nsPrestartField', (n) => n.hidden);
    await page.click('#newSessionSheet .so-settings__program[data-value="claude"]');
    const forClaude = await page.$eval('#nsPrestartField', (n) => !n.hidden);
    ok(forShell && forClaude, 'the sheet offers PreStart for Claude Code and not for a shell',
      JSON.stringify({ shellHidden: forShell, claudeShown: forClaude }));
    const offByDefault = await page.$eval('#nsPrestart', (n) => String(!n.checked));
    await page.click('#nsPrestart');
    const label = await page.$eval('#nsStart', (n) => n.textContent.trim());
    ok(offByDefault === 'true' && label === 'Prepare', 'it is off when the sheet opens, and on it makes the button Prepare',
      offByDefault + ' / ' + label);
    await page.click('#nsStart');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await panelUp(page);
    const id = await page.evaluate(() => location.hash.slice(1));

    // Nothing ran: no terminal and no chat on the page, no agent host.
    let row = await sessionRow(s, id);
    ok(row && row.state === 'prestart' && row.kind === 'chat', 'the chat is stored as prepared', row && row.kind + ' ' + row.state);
    const quiet = await page.evaluate(() => ({
      term: document.getElementById('termWrap').hidden,
      chat: document.getElementById('chat').hidden,
      keybar: document.getElementById('keybar').hidden,
      transfer: document.getElementById('transfer').hidden,
    }));
    ok(quiet.term && quiet.chat && quiet.keybar && quiet.transfer,
      'the panel stands where the chat will be, with no key bar and no transfer buttons', JSON.stringify(quiet));
    ok(sessionsOn(s.data).length === 0 && hostsOf(s.data).length === 0,
      'and nothing was started for it: no agent host, no tmux session',
      hostsOf(s.data).length + ' hosts, ' + sessionsOn(s.data).length + ' tmux sessions');
    const group = await page.evaluate((sid) => {
      const row = document.querySelector('#sessionList .so-row[data-id="' + sid + '"]');
      const shelf = row && row.closest('.so-folder--shelf');
      return {
        shelf: shelf && shelf.dataset.shelf,
        open: !!shelf && shelf.hasAttribute('data-open'),
        first: !!shelf && shelf.parentElement.firstElementChild === shelf,
        folder: !!(shelf && shelf.parentElement.closest('.so-folder[data-folder]')),
      };
    }, id);
    ok(group.shelf === 'prestart' && group.open && group.first && group.folder,
      'its row is on its folder\'s PreStart shelf, open, above the folder\'s sessions', JSON.stringify(group));

    // The prompt saves itself.
    await page.waitForFunction(() => !document.getElementById('psPrompt').disabled, null, { timeout: 10000 });
    await page.fill('#psPrompt', PREPARED_PROMPT);
    const saved = await page.waitForFunction(() => /^Saved$/.test(document.getElementById('psSave').textContent.trim()),
      null, { timeout: 10000 }).then(() => true, () => false);
    let draft = await draftOf(s, id);
    ok(draft.text === PREPARED_PROMPT, 'the prompt reached the server as typed', JSON.stringify(draft.text));
    ok(saved, 'and the panel says it is saved', await page.$eval('#psSave', (n) => n.textContent.trim()));

    // A setting changed in the panel is saved on its own.
    await page.waitForSelector('#psModel .ds-combo__input', { timeout: 10000 });
    await page.click('#psModel .ds-combo__input');
    await page.waitForSelector('#psModel .ds-combo__option', { timeout: 5000 });
    await page.click('#psModel .ds-combo__option:has(.ds-combo__name:text-is("Haiku"))');
    const until = Date.now() + 8000;
    while (Date.now() < until && (row = await sessionRow(s, id)) && row.model !== 'haiku') await wait(300);
    ok(row && row.model === 'haiku', 'a model picked in the panel is saved with the session', row && row.model);

    // A reload brings back the same text and the same choice.
    await reloadBack(page);
    await panelUp(page);
    const kept = await page.waitForFunction((want) => document.getElementById('psPrompt').value === want,
      PREPARED_PROMPT, { timeout: 10000 }).then(() => true, () => false);
    ok(kept, 'after a reload the panel shows the same prompt',
      JSON.stringify(await page.$eval('#psPrompt', (n) => n.value)));
    const shownModel = await page.$eval('#psModel .ds-combo__input', (n) => n.value.trim()).catch(() => '');
    ok(/haiku/i.test(shownModel), 'and the model that was picked', shownModel);

    // Start: the chat comes up, and the prompt is its first message.
    await page.click('#psStart');
    const live = await panelBecameChat(page, 30000).then(() => true, () => false);
    ok(live, 'Start turns the panel into the chat');
    row = await waitForState(s, id, ['running'], 20000);
    ok(row && row.state === 'running', 'and the chat is running', row && row.state);
    const answered = await awaitChat(s, id, (b) => idle(b) && b.chat.last_outcome === 'completed', 20000);
    const history = await historyOf(s, id);
    const reply = (history.messages || []).find((m) => m.role === 'assistant');
    ok(userTexts(history).length === 1 && userTexts(history)[0] === PREPARED_PROMPT && !!answered.chat,
      'the agent was sent the prompt as its first message, byte for byte, once', JSON.stringify(userTexts(history)));
    ok(!!reply && textOf(reply) === 'you said: ' + PREPARED_PROMPT, 'and Claude Code got it so', JSON.stringify(reply && textOf(reply)));
    const argv = await answerTo(s, id, '/argv');
    ok(/(^| )--model haiku( |$)/.test(argv), 'with the model chosen in the panel', oneLine(argv.slice(argv.indexOf('--model'))));
    const moved = await page.evaluate((sid) => {
      const row = document.querySelector('#sessionList .so-row[data-id="' + sid + '"]');
      return !!row && !row.closest('.so-folder--shelf[data-shelf="prestart"]');
    }, id);
    ok(moved, 'its row left the PreStart shelf for its folder\'s sessions', String(moved));
    // The draft was the first message, and from here on it is the
    // composer's: empty, for the next one.
    draft = await draftOf(s, id);
    const composer = await page.$eval('#chat .so-composer__input', (n) => n.value);
    ok(draft.text === '' && composer === '', 'the draft became the first message, and the composer is empty',
      JSON.stringify({ draft: draft.text, composer }));

    // A start by the clock. A second prepared session, opened in the panel.
    const made = await (await s.context.request.post(s.url + '/api/sessions', {
      data: { prestart: true, harness: 'codex', prompt: 'started by the clock\nsecond line' },
    })).json();
    const sid = made.session && made.session.id;
    // The page learns of a session made elsewhere from its list, which it
    // reads again when the window is looked at.
    await page.evaluate(() => window.dispatchEvent(new Event('focus')));
    await page.waitForSelector(rowSel(sid), { timeout: 20000 });
    await page.click(rowSel(sid) + ' .so-row__label');
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
    const clock = await page.waitForSelector('#sessionList .so-row[data-id="' + sid + '"] .so-row__when svg',
      { timeout: 20000 }).then(() => true, () => false);
    ok(clock, 'and its row wears a clock and the time',
      await page.$eval('#sessionList .so-row[data-id="' + sid + '"] .so-row__when', (n) => n.textContent).catch(() => 'no row'));

    // A field to the minute cannot say "five seconds from now", so the near
    // time goes through the same PATCH the panel uses.
    const due = Date.now() + 5000;
    const patched = await s.context.request.patch(s.url + '/api/sessions/' + sid + '/prestart', { data: { start_at: due } });
    ok(patched.ok(), 'a start five seconds ahead is accepted', String(patched.status()));
    await wait(Math.max(0, due - Date.now() - 1500));
    srow = await sessionRow(s, sid);
    ok(srow && srow.state === 'prestart', 'it has not started early', srow && srow.state);
    const flipped = await panelBecameChat(page, 45000).then(() => true, () => false);
    const late = ((Date.now() - due) / 1000).toFixed(1);
    ok(flipped, 'when it falls due, the open panel becomes its chat by itself', late + ' s after the time');
    srow = await sessionRow(s, sid);
    ok(srow && srow.state === 'running' && !srow.start_at, 'the session is running and no longer scheduled',
      srow && (srow.state + ', start_at ' + srow.start_at));
    const clocked = await awaitLog(page, 'you said: started by the clock', 20000);
    ok(clocked && userTexts(await historyOf(s, sid)).join('|') === 'started by the clock\nsecond line',
      'and Codex was sent its prompt as the first message', JSON.stringify(userTexts(await historyOf(s, sid))));

    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// backupStatus is GET /api/backup.
async function backupStatus(s) {
  return (await s.context.request.get(s.url + '/api/backup')).json();
}

// awaitBackup polls the status until `test` holds, and answers with the last
// one either way.
async function awaitBackup(s, test, timeout = 30000) {
  let st = await backupStatus(s);
  const until = Date.now() + timeout;
  while (!test(st) && Date.now() < until) {
    await wait(300);
    st = await backupStatus(s);
  }
  return st;
}

// chipOf is what the Backup card's status line says.
const chipOf = (page) => page.$eval('#backupStatus .so-state__line', (n) => n.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');

// backup is the Backup card: a bucket configured from it, a backup on demand
// that lists its snapshot, a run that fails while the bucket is down and says
// so, the day-without-a-backup warning (shortened for the suite) on the card,
// in /api/state and on the session page, and the warning gone after the
// next good run.
async function backup() {
  const s = await start({ viewport: { width: 1280, height: 800 }, env: { SOCRATES_BACKUP_WARN_AFTER: '20s' } });
  try {
    await setup(s.page, s.url);
    await s.page.goto(s.url + '/admin#backupCard', { waitUntil: 'domcontentloaded' });
    await s.page.waitForFunction(() => /Not configured/.test(document.querySelector('#backupStatus').textContent), null, { timeout: 15000 });
    ok(true, 'the card says backups are not configured', await chipOf(s.page));
    const before = await (await s.context.request.get(s.url + '/api/state')).json();
    ok(before.backup && !before.backup.warning, 'and an unconfigured backup warns nobody', JSON.stringify(before.backup));

    const form = await backupForm(prefixFor(s.data));
    await s.page.fill('#backupEndpoint', form.endpoint);
    await s.page.fill('#backupRegion', form.region);
    await s.page.fill('#backupBucket', form.bucket);
    await s.page.fill('#backupPrefix', form.prefix);
    await s.page.fill('#backupKey', form.access_key_id);
    await s.page.fill('#backupSecret', form.secret_access_key);
    await s.page.fill('#backupPassphrase', form.passphrase);
    await s.page.click('#backupTest');
    const tested = await s.page.waitForSelector('.ds-toast', { timeout: 15000 }).then((n) => n.textContent()).catch(() => '');
    ok(/answers/.test(tested), 'Test connection reaches the bucket', oneLine(tested));
    await s.page.click('#backupSave');
    await s.page.waitForFunction(() => /No backup yet/.test(document.querySelector('#backupStatus').textContent), null, { timeout: 20000 });
    ok(true, 'saved, it waits for its first backup', await chipOf(s.page));
    const secretShown = await s.page.$eval('#backupSecret', (n) => ({ value: n.value, placeholder: n.placeholder }));
    ok(secretShown.value === '' && secretShown.placeholder === 'Unchanged', 'the secret is never shown again', JSON.stringify(secretShown));

    await s.page.click('#backupRun');
    const done = await s.page.waitForFunction(() => /Backed up/.test(document.querySelector('#backupStatus').textContent), null, { timeout: 30000 })
      .then(() => true, () => false);
    ok(done, 'Back up now ends in "Backed up"', await chipOf(s.page));
    const rows = await s.page.waitForFunction(() => document.querySelectorAll('#backupSnapshotList tr').length > 0, null, { timeout: 20000 })
      .then(() => s.page.$$eval('#backupSnapshotList tr', (trs) => trs.map((tr) => tr.textContent.replace(/\s+/g, ' ').trim())), () => []);
    ok(rows.length === 1 && /this one/.test(rows[0]), 'and the snapshot is listed, as this machine\'s', JSON.stringify(rows));

    // The bucket goes away, and something changes.
    await fakeS3Switch('down?status=503');
    try {
      await useDomRenderer(s);
      await s.page.click('#backupRun');
      const failed = await s.page.waitForFunction(() => /Last backup failed/.test(document.querySelector('#backupStatus').textContent)
        && document.querySelector('#backupDetail .ds-alert--error'), null, { timeout: 30000 }).then(() => true, () => false);
      ok(failed, 'a run against a bucket that is down says it failed, and why', await chipOf(s.page));
      const tip = await s.page.$eval('#backupStatus .so-info', (n) => n.getAttribute('data-tip')).catch(() => '');
      ok(/503|ServiceUnavailable/.test(tip), 'with what the bucket said behind the "i"', oneLine(tip || ''));

      // A day is twenty seconds in this suite.
      const warned = await awaitBackup(s, (st) => !!st.warning, 40000);
      ok(/last successful backup/.test(warned.warning || ''), 'the status warns once the last good backup is too old', warned.warning);
      const state = await (await s.context.request.get(s.url + '/api/state')).json();
      ok(state.backup && /last successful backup/.test(state.backup.warning || ''), 'and so does /api/state', JSON.stringify(state.backup));
      await s.page.reload({ waitUntil: 'domcontentloaded' });
      const cardWarns = await s.page.waitForSelector('#backupDetail .ds-alert--warning', { timeout: 15000 }).then((n) => n.textContent(), () => '');
      ok(/last successful backup/.test(cardWarns), 'and the card', oneLine(cardWarns));
      await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
      const notice = await s.page.waitForSelector('#backupNotice:not([hidden])', { timeout: 15000 }).then((n) => n.textContent(), () => '');
      ok(/last successful backup/.test(notice), 'and the session page, with a way to the card', oneLine(notice));
    } finally {
      await fakeS3Switch('up');
    }

    await s.page.goto(s.url + '/admin#backupCard', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#backupRun:not([disabled])', { timeout: 15000 });
    await s.page.click('#backupRun');
    const healed = await awaitBackup(s, (st) => !st.running && !st.warning && !st.last_error, 30000);
    ok(!healed.warning && !healed.last_error, 'the next good run takes the warning and the error away', JSON.stringify({ warning: healed.warning, error: healed.last_error }));
    const snaps = ((await (await s.context.request.get(s.url + '/api/backup/snapshots')).json()).snapshots) || [];
    ok(snaps.length === 2, 'and there are two snapshots now', String(snaps.length));
    ok(unexpected(s.errors, /503|502|409/).length === 0, 'no console errors', unexpected(s.errors, /503|502|409/).join(' | ') || '0');
  } finally { await s.stop(); }
}

// restore is the reason the backup exists: machine A is backed up and lost,
// and machine B - a new data directory - is restored from the setup page and
// signs in with A's password to A's sessions, prompt and settings.
async function restore() {
  const a = await start({ viewport: { width: 1280, height: 720 } });
  let aStopped = false;
  let ids = {};
  let snapshot = '';
  const form = await backupForm(prefixFor(a.data));
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
    const saved = await a.context.request.put(a.url + '/api/backup/config', { data: form });
    ok(saved.ok(), 'its backup is configured', String(saved.status()));
    await a.context.request.post(a.url + '/api/backup/run', { data: {} });
    const st = await awaitBackup(a, (x) => !x.running && !!x.last_success, 30000);
    snapshot = st.last_success && st.last_success.id;
    ok(!!snapshot && !st.last_error, 'and it is backed up', JSON.stringify({ id: snapshot, error: st.last_error }));

    // Machine A goes away with everything on its disk: data directory, tmux
    // server, the panes in it. Killed, not stopped, so the snapshot taken
    // above is the one there is.
    await a.context.close().catch(() => {});
    await a.browser.close().catch(() => {});
    a.server.kill('SIGKILL');
    await a.exited;
    killTmux(a.data);
    rmSync(a.data, { recursive: true, force: true });
    aStopped = true;
  } catch (err) {
    ok(false, 'server A set the scene', String(err && err.stack || err));
  } finally {
    if (!aStopped) {
      await a.stop();
      return;
    }
  }

  const b = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await b.page.goto(b.url + '/setup', { waitUntil: 'domcontentloaded' });
    await b.page.waitForSelector('#restoreOffer:not([hidden])', { timeout: 15000 });
    ok(true, 'a new machine\'s setup page offers a restore');
    await b.page.click('#restoreOpen');
    await b.page.fill('#rEndpoint', form.endpoint);
    await b.page.fill('#rRegion', form.region);
    await b.page.fill('#rBucket', form.bucket);
    await b.page.fill('#rPrefix', form.prefix);
    await b.page.fill('#rKey', form.access_key_id);
    await b.page.fill('#rSecret', form.secret_access_key);
    await b.page.fill('#rPassphrase', 'not the passphrase');
    await b.page.click('#findBtn');
    const refused = await b.page.waitForSelector('#rPassphraseError:not([hidden])', { timeout: 20000 }).then((n) => n.textContent(), () => '');
    ok(/passphrase/i.test(refused), 'a wrong passphrase is refused at the field', oneLine(refused));
    await b.page.fill('#rPassphrase', form.passphrase);
    await b.page.click('#findBtn');
    await b.page.waitForSelector('#snapshotBox:not([hidden])', { timeout: 20000 });
    const listed = await b.page.$$eval('#snapshotList .ds-radio', (n) => n.map((x) => x.textContent.replace(/\s+/g, ' ').trim()));
    ok(listed.length === 1 && /files/.test(listed[0]), 'it lists the snapshot with its machine, size and files', JSON.stringify(listed));
    await b.page.check('#snapshotList input[value="' + snapshot + '"]');
    await b.page.click('#restoreBtn');
    const back = await b.page.waitForFunction(() => location.pathname === '/login', null, { timeout: 90000 }).then(() => true, () => false);
    ok(back, 'the restore runs, Socrates starts again on it, and the page goes to the sign-in', b.page.url());

    await b.page.fill('#password', PASSWORD);
    await b.page.click('#submit');
    await b.page.waitForFunction(() => location.pathname === '/', null, { timeout: 15000 }).catch(() => {});
    await b.page.waitForSelector('#newSession', { state: 'attached', timeout: 15000 });
    ok(true, 'machine A\'s password signs in');

    const sessions = ((await (await b.context.request.get(b.url + '/api/sessions')).json()).sessions || []);
    const byId = Object.fromEntries(sessions.map((x) => [x.id, x]));
    ok(byId[ids.shell] && byId[ids.shell].title === 'restore shell'
      && byId[ids.prepared] && byId[ids.prepared].title === 'restore draft',
      'both sessions are listed, under the same ids and names', sessions.map((x) => x.title + ':' + x.state).join(', '));
    ok(byId[ids.prepared] && byId[ids.prepared].state === 'prestart', 'the prepared one is still prepared',
      byId[ids.prepared] && byId[ids.prepared].state);
    ok(byId[ids.shell] && byId[ids.shell].state !== 'running', 'and the one whose pane was on machine A is not taken for running',
      byId[ids.shell] && byId[ids.shell].state);
    const draft = await draftOf(b, ids.prepared);
    ok(draft.text === PREPARED_PROMPT, 'the prepared prompt is intact', JSON.stringify(draft.text));
    const settings = await (await b.context.request.get(b.url + '/api/settings')).json();
    const webgl = settings && settings.settings && settings.settings.terminal && settings.settings.terminal.webgl;
    ok(webgl === false, 'and so is a setting made on machine A', JSON.stringify(webgl));
    const st = await backupStatus(b);
    ok(st.configured && st.config && st.config.prefix === form.prefix && st.config.host_id
      && !snapshot.startsWith(st.config.host_id + '/'),
      'machine B goes on backing up into the same bucket, as a machine of its own',
      JSON.stringify({ prefix: st.config && st.config.prefix, host: st.config && st.config.host_id, from: snapshot }));
    ok(unexpected(b.errors, /401|503|ERR_CONNECTION_REFUSED|Failed to fetch/).length === 0, 'no console errors',
      unexpected(b.errors, /401|503|ERR_CONNECTION_REFUSED|Failed to fetch/).join(' | ') || '0');
  } finally { await b.stop(); }
}

/* ------------------------------------------------------------- options */

// The top bar's ⋮ is this device's options and nothing about a session: the
// key bar, touch mode, sound and notifications, as switches in a menu that
// stays open while they are pressed. Each is kept per device and takes effect
// at once. And beside it, the limits: how full each window is, how long until
// it renews, and - a tap away, because a phone has no tooltip - when exactly.
async function optionsmenu() {
  const s = await start({ viewport: { width: 1280, height: 720 }, env: FAKE_AGENT });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    await open(s);

    const look = () => s.page.evaluate(() => {
      const area = document.querySelector('#term .xterm-helper-textarea');
      return {
        touch: document.documentElement.classList.contains('so-touch'),
        kb: !!document.getElementById('kbBtn')?.checkVisibility(),
        bar: document.getElementById('keybar').hidden ? 'hidden' : 'shown',
        pane: area ? { mode: area.getAttribute('inputmode') || '', ro: area.readOnly } : null,
        stored: {
          touch: localStorage.getItem('socrates.touch'),
          keybar: localStorage.getItem('socrates.term.keybar'),
        },
        expanded: document.getElementById('optionsBtn').getAttribute('aria-expanded'),
        menu: !!document.querySelector('#optionsDialog[open]'),
      };
    });

    // Before any session: the button is there and the menu holds the four.
    const empty = await s.page.evaluate(() => ({
      button: !!document.getElementById('optionsBtn')?.checkVisibility(),
      // The row menu is #sessionMenu now; the old top-bar session button was a
      // button of that name.
      gone: ['soundBtn', 'notifyBtn', 'sessionMenu'].filter((id) => document.getElementById(id)?.tagName === 'BUTTON'),
    }));
    ok(empty.button && empty.gone.length === 0,
      'the options button is in the bar before any session, and the old buttons are gone', JSON.stringify(empty));
    const first = await options(s.page);
    ok(Object.keys(first).join(',') === 'keybar,touch,sound,notify',
      'the menu holds the key bar, touch mode, sound and notifications', Object.keys(first).join(','));
    ok(!first.keybar.on && !first.touch.on && first.sound.on && !first.notify.on,
      'each where a desk starts: no key bar, no touch mode, sound on, notifications off',
      JSON.stringify(Object.fromEntries(Object.entries(first).map(([k, v]) => [k, v.on]))));
    const words = await s.page.$$eval('#optionsDialog .ds-switch__label', (nodes) => nodes.map((n) => n.textContent));
    ok(!words.some((w) => /Rename|Delete|Info|Download|Done|Background/.test(w)),
      'and nothing that is done to a session', oneLine(words.join(' | ')));
    const opened = await look();
    ok(opened.expanded === 'true' && !opened.touch && !opened.kb, 'the button says its menu is open',
      JSON.stringify(opened));

    // Three ways to put it away: Done, Escape, a press beside it. The dialog
    // is modal, so the button under it cannot be pressed meanwhile.
    const gone = () => s.page.waitForSelector('#optionsDialog[open]', { state: 'detached', timeout: 3000 })
      .then(() => true).catch(() => false);
    await s.page.click('#optionsDialog .ds-dialog__actions [data-close]');
    const byButton = await gone();
    await openOptions(s.page);
    await s.page.keyboard.press('Escape');
    const byEscape = await gone();
    await openOptions(s.page);
    await s.page.mouse.click(5, 400);
    const byPress = await gone();
    ok(byButton && byEscape && byPress, 'Done, Escape and a press beside it each close it',
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
    await reloadBack(s.page);
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
    const rowItems = await s.page.$$eval('#sessionMenu:not([hidden]) .ds-menu__item', (nodes) => nodes.map((n) => n.textContent.trim()));
    ok(rowItems.includes('Info') && rowItems.includes('Rename') && rowItems.includes('Delete')
      && !rowItems.some((w) => /key bar/i.test(w)),
      'the row menu holds the session’s actions and no device option', rowItems.join(','));
    await s.page.keyboard.press('Escape');
    await s.page.waitForSelector('#sessionMenu', { state: 'hidden', timeout: 3000 });

    /* ------------------------------------------------------- the limits */

    // A chat's limits are its account's (REBUILD.md §4.3): the account button
    // in its composer says how full the account it runs on is and, at a
    // limit, when that renews, and its menu says it for every account of the
    // agent. A reading until the button has drawn it, and then none: the
    // minutes after that are the page's own clock. The five-hour window
    // renews in 2 h 36 min and some seconds, so its countdown crosses a minute
    // while it is on screen; the spare account's renews in a few seconds, and
    // is empty again once it has.
    const post = async (path, data) => (await s.context.request.post(s.url + path, { data })).json();
    const work = (await post('/api/accounts', { agent: 'fake', kind: 'api_key', label: 'Work', key: 'sk-work-0123456789' })).account;
    const spare = (await post('/api/accounts', { agent: 'fake', kind: 'api_key', label: 'Spare', key: 'sk-spare-0123456789' })).account;
    const at = Date.now();
    const five = new Date(at + (2 * 60 + 36) * 60000 + 20000).toISOString();
    const week = new Date(at + ((3 * 24 + 4) * 60 + 30) * 60000).toISOString();
    const lapsing = new Date(at + 25000).toISOString();
    let failing = false;
    let refused = 0;
    await s.context.route('**/api/accounts', async (route) => {
      if (route.request().method() !== 'GET') return route.fallback();
      if (failing) { refused += 1; return route.abort(); }
      const res = await route.fetch();
      const body = await res.json();
      for (const one of body.accounts || []) {
        if (one.id === work.id) one.limits = [{ label: '5h', used_percent: 100, resets_at: five }, { label: '7d', used_percent: 30, resets_at: week }];
        if (one.id === spare.id) one.limits = [{ label: '5h', used_percent: 100, resets_at: lapsing }, { label: '7d', used_percent: 12, resets_at: week }];
      }
      return route.fulfill({ response: res, json: body });
    });
    const chat = await makeChat(s, 'fake', { account: work.id });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    await openChat(s.page, chat);
    const button = () => s.page.$eval('#chat .so-composer__account-btn', (n) => ({
      text: n.textContent.trim(), tip: n.getAttribute('data-tip') || '', state: n.dataset.state || '',
    })).catch(() => null);
    await s.page.waitForSelector('#chat .so-composer__account:not([hidden]) .so-composer__account-btn', { timeout: 15000 });
    const face = await button();
    ok(face && face.text === 'Work' && face.state === 'limited' && face.tip === '5h 100% · 7d 30% · renews in 2 h 37 min',
      'the composer names the account, says how full each window is and when its limit renews', JSON.stringify(face));
    const menuOf = async () => {
      await s.page.click('#chat .so-composer__account-btn');
      await s.page.waitForSelector('.so-account-menu:not([hidden]) [data-account]', { timeout: 5000 });
      const items = await s.page.$$eval('.so-account-menu [data-account]', (n) => n.map((one) => one.innerText.replace(/\s+/g, ' ').trim()));
      await s.page.keyboard.press('Escape');
      await s.page.waitForSelector('.so-account-menu', { state: 'hidden', timeout: 5000 }).catch(() => {});
      return items;
    };
    const listed = await menuOf();
    ok(/Work .*5h 100% · 7d 30% · renews in 2 h 37 min/.test(listed[0] || '') && /Spare .*renews in 1 min/.test(listed[1] || ''),
      'and its menu says it for every account of the agent', JSON.stringify(listed));
    await wait(300);
    await shot(s.page, 'options-limits-desk');
    failing = true;

    // The countdown moves with no new reading at all - every request after
    // the first fails - and the spare account's limit renews on screen.
    const ticked = await s.page.waitForFunction(() => {
      const n = document.querySelector('#chat .so-composer__account-btn');
      return !!n && /renews in 2 h 36 min$/.test(n.getAttribute('data-tip') || '');
    }, null, { timeout: 45000 }).then(() => true, () => false);
    ok(ticked, 'the countdown goes on with the network failing', JSON.stringify(await button()));
    for (let i = 0; i < 40 && Date.now() < Date.parse(lapsing) + 1000; i += 1) await wait(500);
    const renewed = await menuOf();
    ok(/Spare .*5h 0% · 7d 12%/.test(renewed[1] || '') && !/Spare .*renews in/.test(renewed[1] || ''),
      'and a limit that renewed on screen is an empty window again', JSON.stringify(renewed));
    for (let i = 0; i < 20 && refused === 0; i += 1) await wait(500);
    ok(refused >= 1, 'it went on asking all the while, and was refused', refused + ' refused request(s)');

    // A phone: the button is in the composer, nothing off the edge, and the
    // menu opens on screen.
    await s.page.setViewportSize({ width: 360, height: 740 });
    await wait(500);
    const phone = await s.page.evaluate(() => {
      const b = document.querySelector('#chat .so-composer__account-btn').getBoundingClientRect();
      return { left: Math.round(b.left), right: Math.round(b.right), vw: innerWidth,
        overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth };
    });
    ok(phone.left >= 0 && phone.right <= phone.vw && phone.overflow <= 1,
      'on a phone the account button fits the composer, and nothing scrolls sideways', JSON.stringify(phone));
    await s.page.click('#chat .so-composer__account-btn');
    await s.page.waitForSelector('.so-account-menu:not([hidden]) [data-account]', { timeout: 5000 });
    await wait(400);
    const popBox = await s.page.$eval('.so-account-menu', (n) => {
      const r = n.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, w: innerWidth, h: innerHeight };
    });
    ok(popBox.left >= 0 && popBox.right <= popBox.w && popBox.top >= 0 && popBox.bottom <= popBox.h,
      'and its menu opens inside the screen', JSON.stringify(popBox));
    await shot(s.page, 'options-limits-phone');
    await s.page.keyboard.press('Escape');
    await s.context.unroute('**/api/accounts');

    // The options menu on the phone, too.
    await openOptions(s.page);
    const menuBox = await s.page.$eval('#optionsDialog .ds-dialog__box', (n) => {
      const r = n.getBoundingClientRect();
      return { left: r.left, right: r.right, bottom: r.bottom, w: innerWidth, h: innerHeight, scroll: n.scrollHeight > n.clientHeight + 1 };
    });
    ok(menuBox.left >= 0 && menuBox.right <= menuBox.w && menuBox.bottom <= menuBox.h && !menuBox.scroll,
      'the options dialog opens whole inside a phone’s screen', JSON.stringify(menuBox));
    await wait(300);
    await shot(s.page, 'options-menu-phone');
    await closeOptions(s.page);

    ok(id && unexpected(s.errors, OFFLINE_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, OFFLINE_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* ------------------------------------------------------------- 53. Home */

// Home and its questions (HOME.md, REBUILD.md §5.4): the landing page, the
// numbers that move by themselves, and an answer that goes from a tap to the
// agent. A question is a chat's: Claude Code calling ask_user, which
// fakeclaude's /askuser hands to the in-process MCP server with the call's
// JSON, blocking until the answer comes back and saying it in the
// conversation ("ask_user returned: ..."). The chats are made the way another
// device would make them, so that the page under test is only ever on Home
// unless a scenario puts it somewhere else; the dialog is the chats' own
// (askdialog.js).

const HOME_QUESTION = 'Which database should we use?';

// askFor is an ask_user call of one question with these options, in REBUILD
// §5.1's shape.
const askFor = (question, labels, extra = {}) => ({
  title: 'Storage',
  context: 'The service is setting up storage for its first release.',
  questions: [{
    id: 'db', header: 'Database', question, kind: 'single',
    options: labels.map((label, i) => ({ id: label.toLowerCase(), label, description: ['Relational, robust', 'One file, no server', 'The one the team knows'][i] || '', recommended: i === 0 })),
    ...extra,
  }],
});
const DB_ASK = askFor(HOME_QUESTION, ['Postgres', 'SQLite']);

// homeChats is a signed-in server with nothing in it yet.
async function homeChats(options = {}) {
  const s = await start({ viewport: { width: 1280, height: 800 }, ...options });
  await setup(s.page, s.url);
  return s;
}

// askingChat makes a Claude Code chat elsewhere and has its agent ask; it
// answers the chat's id and the question's.
async function askingChat(s, title, ask = DB_ASK) {
  const id = await makeChat(s, 'claude', { title });
  const said = await tellChat(s, id, '/askuser ' + JSON.stringify(ask));
  const askId = said === 200 ? await openAskOf(s, id, 20000) : '';
  if (!askId) throw new Error(title + ' never asked (' + said + ')');
  return { id, askId };
}

// answerFrom answers a chat's question over the API, as another device does:
// option ids per question, everything else empty.
function answerFrom(s, id, askId, picks, device = 'phone') {
  const answers = {};
  for (const [q, selected] of Object.entries(picks)) {
    answers[q] = { selected, other: null, note: null, delegated: false, skipped: false };
  }
  return s.context.request.post(s.url + '/api/chats/' + encodeURIComponent(id) + '/asks/' + encodeURIComponent(askId) + '/answer', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), device, answer: { answers, general_note: null, follow_up: null } },
  });
}

// returned is what the agent said it got back from ask_user, once it has.
async function returned(s, id, timeout = 15000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const messages = (await historyOf(s, id)).messages || [];
    const said = messages.filter((m) => m.role === 'assistant').map(textOf).find((t) => t.startsWith('ask_user returned: '));
    if (said) {
      // The answer as JSON, then a line for the model on what it holds
      // besides answers (askuser.ToolResult).
      try { return JSON.parse(said.slice('ask_user returned: '.length).split('\n')[0]); } catch { return said; }
    }
    await wait(200);
  }
  return null;
}

// askLook is the question dialog as it is drawn, or null when it is shut.
const askLook = (page) => page.evaluate(() => {
  const dialog = document.querySelector('dialog.so-askdlg');
  if (!dialog || !dialog.open) return null;
  const text = (sel) => ((dialog.querySelector(sel) || {}).textContent || '').replace(/\s+/g, ' ').trim();
  // The DS dialog is a full-window layer; what is seen is its box.
  const frame = dialog.querySelector('.ds-dialog__box') || dialog;
  const box = frame.getBoundingClientRect();
  const kb = dialog.querySelector('.so-kb-copy');
  const at = document.activeElement;
  return {
    top: text('.so-q-top-title'),
    session: text('.so-q-session-title'),
    title: text('.so-ask-title'),
    context: text('.so-ask-context__body'),
    question: text('.so-q-text'),
    step: text('.so-q-step'),
    options: [...dialog.querySelectorAll('.so-q-option')].map((option) => {
      const r = option.getBoundingClientRect();
      return {
        id: option.dataset.option,
        label: ((option.querySelector('.so-q-option-label') || {}).textContent || '').trim(),
        desc: ((option.querySelector('.so-q-option-desc') || {}).textContent || '').trim(),
        pressed: option.getAttribute('aria-pressed'),
        h: Math.round(r.height), w: Math.round(r.width),
        size: getComputedStyle(option.querySelector('.so-q-option-label')).fontSize,
      };
    }),
    finished: text('.so-q-finished .ds-empty__title'),
    finishedText: text('.so-q-finished .ds-empty__text'),
    status: text('.so-q-status'),
    refused: text('.so-refused'),
    actions: [...dialog.querySelectorAll('.so-q-actions [data-action]')].map((b) => b.dataset.action),
    other: !!dialog.querySelector('[data-action="other"]'),
    open: !!dialog.querySelector('[data-action="open-chat"]'),
    read: !!dialog.querySelector('[data-action="read"]'),
    kb: kb ? kb.checkVisibility() : false,
    focus: at && dialog.contains(at) ? (at.classList.contains('so-q-own-field') ? 'own:' + at.id : at.className || at.tagName) : '',
    box: { x: Math.round(box.x), y: Math.round(box.y), w: Math.round(box.width), h: Math.round(box.height) },
    viewport: { w: innerWidth, h: innerHeight },
    overflow: frame.scrollWidth - frame.clientWidth,
  };
});

async function awaitAsk(page, test, timeout = 15000) {
  const started = Date.now();
  let seen = null;
  while (Date.now() - started < timeout) {
    seen = await askLook(page);
    if (test(seen)) return { ok: true, ms: Date.now() - started, seen };
    await wait(120);
  }
  return { ok: false, ms: Date.now() - started, seen };
}

const askShut = (page, timeout = 15000) => page.waitForFunction(() => {
  const dialog = document.querySelector('dialog.so-askdlg');
  return !dialog || !dialog.open;
}, null, { timeout }).then(() => true, () => false);

// askSettled waits for the dialog's opening animation to end: it pops in
// from a little smaller, and a box measured on the way is not its size.
const askSettled = (page) => page.waitForFunction(() => {
  const dialog = document.querySelector('dialog.so-askdlg');
  return !!dialog && dialog.open && dialog.getAnimations({ subtree: true }).every((a) => a.playState !== 'running');
}, null, { timeout: 5000 }).catch(() => {});

// watchAsk keeps every line the dialog says - its status, what it says when
// a question is settled, and a refusal - in order: "Sending your answer…"
// can come and go between two looks.
async function watchAsk(page) {
  await page.evaluate(() => {
    window.__asksaid = [];
    new MutationObserver(() => {
      const dialog = document.querySelector('dialog.so-askdlg');
      if (!dialog) return;
      for (const sel of ['.so-q-status', '.so-q-finished .ds-empty__title', '.so-refused']) {
        const node = dialog.querySelector(sel);
        const one = node ? node.textContent.replace(/\s+/g, ' ').trim() : '';
        if (one && window.__asksaid[window.__asksaid.length - 1] !== one) window.__asksaid.push(one);
      }
    }).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true });
  });
  return () => page.evaluate(() => window.__asksaid.slice());
}

// toastsOf is every toast the page has shown since watchToasts.
async function watchToasts(page) {
  await page.evaluate(() => {
    window.__toastsSeen = [];
    const host = document.getElementById('toasts');
    new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) if (node.textContent) window.__toastsSeen.push(node.textContent.trim());
      }
    }).observe(host, { childList: true, subtree: true });
  });
  return () => page.evaluate(() => window.__toastsSeen.slice());
}

// A Tesla's browser: a Chromium that says it has a mouse, at the car's size.
const TESLA_AGENT = 'Mozilla/5.0 (X11; Linux) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0 Safari/537.36 Tesla/2025.20';

// gate is a call held back until a scenario lets it go.
function gate() {
  let release = () => {};
  const promise = new Promise((resolve) => { release = resolve; });
  return { promise, release };
}

// awaitActivity waits for the server to say something about what a session
// is doing, and hands back the activity it saw (or null).
async function awaitActivity(s, id, test, timeout = 20000) {
  const until = Date.now() + timeout;
  while (Date.now() < until) {
    const row = await sessionRow(s, id);
    if (row && row.activity && test(row.activity)) return row.activity;
    await wait(250);
  }
  return null;
}

// tmuxType is a person at the machine itself, typing into the pane through
// tmux: no browser is attached to the session it types into.
function tmuxType(s, id, text, enter = true) {
  const target = ['-S', tmuxSock(s.data), 'send-keys', '-t', 'soc_' + id];
  execFileSync('tmux', [...target, '-l', text]);
  if (enter) execFileSync('tmux', [...target, 'Enter']);
}

// homeLook is Home as it is drawn: the numbers, the line about the
// connection, and the rows of the list.
const homeLook = (page) => page.evaluate(() => {
  const home = document.getElementById('home');
  const counts = {};
  // The figures count up to their value (DS.motion.countUp): the number
  // itself is the card's data-value, never a figure caught mid-count.
  for (const card of home.querySelectorAll('[data-card][data-value]')) {
    counts[card.dataset.card] = Number(card.dataset.value);
  }
  const conn = home.querySelector('.so-home__conn');
  const pressedNode = home.querySelector('[data-card][aria-pressed="true"], [data-card] [aria-pressed="true"]');
  const pressed = pressedNode && pressedNode.closest('[data-card]');
  return {
    shown: !home.hidden,
    hash: location.hash,
    title: document.getElementById('sessionTitle').textContent,
    // Home is a drawer item, current while it is on the stage.
    homeBtn: document.getElementById('homeBtn').getAttribute('aria-current') === 'page' ? 'true' : 'false',
    counts,
    asking: !!home.querySelector('.so-home__questions[data-state="asking"]'),
    conn: conn ? conn.dataset.status : '',
    connText: conn ? conn.textContent.trim() : '',
    pressed: pressed ? pressed.dataset.card : '',
    listTitle: (home.querySelector('.so-home__list-title') || {}).textContent || '',
    // A card the list let go of fades out in place (DS.motion.flip) and is
    // marked data-ds-leaving meanwhile: it is no longer in the list.
    rows: [...home.querySelectorAll('.so-home__card:not([data-ds-leaving])')].map((row) => ({
      id: row.dataset.id,
      title: row.querySelector('.so-home__card-title').textContent,
      summary: row.querySelector('.so-home__card-summary').hidden ? '' : row.querySelector('.so-home__card-summary').textContent,
      answer: !row.querySelector('.so-home__answer').hidden,
    })),
    empty: (home.querySelector('.so-home__empty:not([hidden])') || {}).textContent || '',
    overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
  };
});

async function awaitHome(page, test, timeout = 15000) {
  const started = Date.now();
  let seen = null;
  while (Date.now() - started < timeout) {
    seen = await homeLook(page);
    if (test(seen)) return { ok: true, ms: Date.now() - started, seen };
    await wait(150);
  }
  return { ok: false, ms: Date.now() - started, seen };
}

// A page in its own context: a phone or a car, signed in with the same cookie.
async function deviceOf(s, options) {
  const context = await s.browser.newContext({ colorScheme: 'light', ...options });
  await context.addCookies(await s.context.cookies());
  const page = await context.newPage();
  page.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
  return { context, page };
}

// home-landing: every start of the page is Home, the button beside the
// collapse control goes back to it, and the address follows.
async function homelanding() {
  const s = await start({ viewport: { width: 1280, height: 720 } });
  try {
    await setup(s.page, s.url);
    await useDomRenderer(s);
    const first = await awaitHome(s.page, (h) => h.shown && !!h.empty, 10000);
    ok(first.ok && first.seen.hash === '#home' && first.seen.homeBtn === 'true' && first.seen.title === 'Home',
      'a first visit lands on Home, says so in the address and the bar, and presses the button',
      JSON.stringify(first.seen && { hash: first.seen.hash, btn: first.seen.homeBtn, title: first.seen.title }));
    ok(/No sessions yet/.test(first.seen.empty) && /New session/.test(first.seen.empty),
      'an empty machine offers to start one', oneLine(first.seen.empty));
    const placed = await s.page.evaluate(() => {
      const button = document.getElementById('homeBtn');
      return {
        first: button.parentElement.id === 'drawerList' && !button.previousElementSibling,
        before: button.nextElementSibling && button.nextElementSibling.id,
        visible: button.checkVisibility(),
        termHidden: document.getElementById('termWrap').hidden,
      };
    });
    ok(placed.first && placed.before === 'sessionList' && placed.visible && placed.termHidden,
      'Home is the drawer\u2019s first item, above the sessions, and no terminal is on the stage', JSON.stringify(placed));
    await shot(s.page, 'home-landing-empty');

    const id = await startSession(s.page, 'shell');
    await s.page.waitForSelector('#term .xterm', { timeout: 15000 });
    const away = await homeLook(s.page);
    ok(!away.shown && away.homeBtn === 'false' && away.hash === '#' + id,
      'a session opened takes Home off the stage and the button out', JSON.stringify({ shown: away.shown, btn: away.homeBtn, hash: away.hash }));

    await s.page.click('#homeBtn');
    const back = await awaitHome(s.page, (h) => h.shown, 5000);
    const active = await s.page.$$eval('#sessionList .so-row__open[aria-current]', (rows) => rows.length);
    ok(back.ok && back.seen.hash === '#home' && back.seen.homeBtn === 'true' && active === 0,
      'the button brings Home back, and no row is the one on screen',
      JSON.stringify({ hash: back.seen.hash, btn: back.seen.homeBtn, active }));
    await s.page.click('.so-home__seg[data-filter="all"]');
    const all = await awaitHome(s.page, (h) => h.rows.some((r) => r.id === id), 5000);
    ok(all.ok && Object.values(all.seen.counts).every((n) => n === 0),
      'a shell is in the list of all sessions and in none of the numbers', JSON.stringify(all.seen.counts));

    await s.page.goBack();
    const previous = await s.page.waitForSelector('#term .xterm', { timeout: 10000 }).then(() => true, () => false);
    const prevLook = await homeLook(s.page);
    ok(previous && !prevLook.shown && prevLook.hash === '#' + id, 'Back returns to the session', prevLook.hash);
    await s.page.goForward();
    const forward = await awaitHome(s.page, (h) => h.shown && h.hash === '#home', 8000);
    ok(forward.ok, 'and Forward to Home again', JSON.stringify({ hash: forward.seen.hash, shown: forward.seen.shown }));

    // The owner's choice: a reload of a session's address is a start of the
    // page, and a start is Home.
    await s.page.evaluate((target) => { location.hash = '#' + target; }, id);
    await s.page.waitForSelector('#term .xterm', { timeout: 10000 });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    const reloaded = await awaitHome(s.page, (h) => h.shown, 15000);
    ok(reloaded.ok && reloaded.seen.hash === '#home', 'a reload on a session lands on Home', reloaded.seen && reloaded.seen.hash);

    // The phone: the sidebar is a drawer, and Home is in it.
    const phone = await deviceOf(s, { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
    await phone.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await awaitHome(phone.page, (h) => h.shown, 15000);
    await ensureNav(phone.page);
    const drawer = await phone.page.evaluate(() => {
      const button = document.getElementById('homeBtn');
      const r = button.getBoundingClientRect();
      return {
        home: button.checkVisibility(), collapse: document.getElementById('sideCollapse').checkVisibility(),
        w: Math.round(r.width), h: Math.round(r.height),
      };
    });
    ok(drawer.home && !drawer.collapse && drawer.w >= 32 && drawer.h >= 32,
      'on a phone the Home button is in the drawer, the collapse button is not', JSON.stringify(drawer));
    await shot(phone.page, 'home-landing-drawer');
    await phone.page.click(rowSel(id) + ' .so-row__label');
    await phone.page.waitForSelector('#term .xterm', { timeout: 15000 });
    await ensureNav(phone.page);
    await phone.page.click('#homeBtn');
    const phoneHome = await awaitHome(phone.page, (h) => h.shown, 5000);
    const shut = await phone.page.waitForFunction(() => document.getElementById('sidebar').getBoundingClientRect().left < -10,
      null, { timeout: 3000 }).then(() => true, () => false);
    ok(phoneHome.ok && shut, 'and pressing it shows Home and puts the drawer away', JSON.stringify({ shown: phoneHome.ok, shut }));
    ok(phoneHome.seen.overflow <= 1, 'Home does not scroll sideways at 390 px', String(phoneHome.seen.overflow));
    await phone.context.close();

    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-live: chats made on another device are counted, and one that starts
// working, one that asks, a Shell that ends and a chat that finishes unseen
// move Home's numbers within seconds - with no reload, and with no socket
// but the event stream. A card filters the list, a second tap shows
// everything.
async function homelive() {
  const s = await homeChats();
  try {
    // Every socket this page opens from here on, so that "nothing attached"
    // is a measurement and not a claim.
    const sockets = [];
    s.page.on('websocket', (ws) => sockets.push(ws.url()));
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await awaitHome(s.page, (h) => h.shown && h.conn === 'live', 10000);
    await s.page.evaluate(() => { window.__sameHome = true; });

    // Made elsewhere, and on Home by themselves.
    const busy = await makeChat(s, 'claude', { title: 'Refactor the mirror' });
    const asking = await makeChat(s, 'claude', { title: 'Pick a database' });
    const shellMade = await s.context.request.post(s.url + '/api/sessions', { data: { harness: 'shell', title: 'Write the docs' } });
    const shell = (await shellMade.json()).session.id;
    const prepared = await s.context.request.post(s.url + '/api/sessions',
      { data: { prestart: true, harness: 'claude', prompt: 'Later, please' } });
    ok(prepared.ok(), 'a prepared chat is made too', prepared.status());
    const arrived = await awaitHome(s.page, (h) => h.counts.ready === 2 && h.counts.prepared === 1
      && h.rows.length >= 0, 15000);
    ok(arrived.ok, 'chats made on another device are counted without a reload: two ready, one prepared',
      took(arrived) + ' ' + JSON.stringify(arrived.seen.counts));

    let sent = Date.now();
    await tellChat(s, busy, work('claude', 7));
    const running = await awaitHome(s.page, (h) => h.counts.running === 1, 8000);
    ok(running.ok && Date.now() - sent < 4000, 'a chat that starts working is counted as running within seconds',
      ((Date.now() - sent) / 1000).toFixed(1) + ' s after the message, ' + JSON.stringify(running.seen.counts));

    sent = Date.now();
    await tellChat(s, asking, '/askuser ' + JSON.stringify(DB_ASK));
    const question = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.asking, 8000);
    ok(question.ok && Date.now() - sent < 4000, 'a question is counted, and the card turns amber',
      ((Date.now() - sent) / 1000).toFixed(1) + ' s after the message, ' + JSON.stringify(question.seen.counts));
    const summary = await awaitHome(s.page, (h) => {
      const row = h.rows.find((r) => r.id === asking);
      return !!row && row.answer && row.summary === HOME_QUESTION;
    }, 15000);
    ok(summary.ok, 'its card has an Answer button and the question’s own words',
      JSON.stringify(summary.seen.rows.find((r) => r.id === asking)));

    // A Shell takes no turns: it is counted as not running once it ends.
    tmuxType(s, shell, 'exit 3');
    const stopped = await awaitHome(s.page, (h) => h.counts.stopped === 1, 15000);
    ok(stopped.ok, 'a Shell that ended is counted as not running', took(stopped));
    const finished = await awaitHome(s.page, (h) => h.counts.running === 0 && h.counts.finished === 1, 20000);
    ok(finished.ok, 'the working chat finishes while nobody looks, and is counted as finished',
      took(finished) + ' ' + JSON.stringify(finished.seen.counts));
    await shot(s.page, 'home-desktop');

    // The list under the numbers: "Needs you" by default, a card filters it,
    // a second tap shows everything. A reorder waits while the list is
    // pointed at (DS.motion.flip), so it is awaited rather than read at once.
    const needs = (await awaitHome(s.page, (h) => h.rows.length === 2, 4000)).seen;
    ok(needs.listTitle === 'Needs you' && needs.rows[0] && needs.rows[0].id === asking
      && needs.rows.some((r) => r.id === busy) && needs.rows.length === 2,
      'the list starts with what needs the person: the question first, then what finished',
      needs.rows.map((r) => r.title).join(', '));
    await s.page.click('.so-home__tile[data-card="finished"]');
    const filtered = await awaitHome(s.page, (h) => h.pressed === 'finished', 3000);
    ok(filtered.ok && filtered.seen.listTitle === 'Finished' && filtered.seen.rows.length === 1
      && filtered.seen.rows[0].id === busy, 'a card filters the list to its sessions',
      filtered.seen.listTitle + ': ' + filtered.seen.rows.map((r) => r.title).join(', '));
    await s.page.click('.so-home__tile[data-card="finished"]');
    const everything = await awaitHome(s.page, (h) => h.listTitle === 'All sessions', 3000);
    ok(everything.ok && everything.seen.rows.length === 4 && !everything.seen.pressed,
      'and a second tap shows every session', everything.seen.rows.length + ' rows');
    await s.page.click('.so-home__seg[data-filter="needs"]');
    ok((await homeLook(s.page)).listTitle === 'Needs you', 'Needs you is a tap away again', (await homeLook(s.page)).listTitle);

    const sidebar = await rowActivity(s.page, asking);
    ok(!!sidebar && sidebar.waiting, 'the sidebar says the same as Home', JSON.stringify(sidebar && { waiting: sidebar.waiting }));
    const same = await s.page.evaluate(() => window.__sameHome === true);
    ok(same, 'all of it without a reload', String(same));
    // The rule behind the cards, for the states that cannot be put on demand:
    // unread work counts as Finished whatever the session's state, and a
    // session filed in the background is never news, only Ready - though its
    // questions still count.
    const cards = await s.page.evaluate(async () => {
      const { cardsOf } = await import('/static/js/home.js');
      const ctx = (activity, status = 'in_progress') => ({ statusOf: () => status, activityOf: () => activity, digestOf: () => null });
      const row = (state) => ({ id: 'x', state, harness: 'claude', kind: 'chat' });
      return {
        stoppedUnread: cardsOf(ctx({ state: 'idle', unread: true }), row('needs_resume')).join(),
        runningUnknownUnread: cardsOf(ctx({ state: 'unknown', unread: true }), row('running')).join(),
        backgroundUnread: cardsOf(ctx({ state: 'idle', unread: true }, 'background'), row('running')).join(),
        backgroundAsks: cardsOf(ctx({ state: 'waiting' }, 'background'), row('running')).join(),
      };
    });
    ok(cards.stoppedUnread === 'stopped,finished' && cards.runningUnknownUnread === 'finished'
      && cards.backgroundUnread === 'ready' && cards.backgroundAsks === 'questions',
      'unread work is Finished even when stopped; a background chat is Ready, and still asks', JSON.stringify(cards));
    ok(sockets.length > 0 && sockets.every((url) => url.includes('/api/events')),
      'and with nothing attached: the only socket is the event stream', sockets.join(' '));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// outcome: with the outcomes on, a Claude Code chat's turn is judged from its
// journal - the instruction, the answer and the task list its TaskCreate and
// TaskUpdate calls keep. While the verdict is read the turn's end carries the
// hourglass and sits under Background tasks, and the chime and the
// notification wait for it; the verdict lands on the row, on Home with what
// is still open, in the notification and in the header's tag, survives a
// reload, goes with the next turn, and goes when the outcomes are switched off.
async function outcome() {
  // The verdict reads held back until the scenario lets them go, and every
  // instruction they were given.
  let hold = null;
  const judged = [];
  const stub = await openRouterStub({
    answer: async (json) => {
      const system = String(((json.messages || [])[0] || {}).content || '');
      if (!system.startsWith('You judge the turn a coding agent has just ended')) return 'Ship the endpoint';
      const last = String(((json.messages || []).slice(-1)[0] || {}).content || '');
      judged.push(last);
      if (hold) await hold.promise;
      const open = [...last.matchAll(/^\[ \] (.+)$/gm)].map((m) => m[1]);
      const verdict = open.length
        ? { kind: 'partial', reason: open.length + ' of the tasks are still open.', todos: open }
        : { kind: 'complete', reason: 'Every task on the list is done.', todos: [] };
      return JSON.stringify({ outcome: verdict });
    },
  });
  const s = await start({ viewport: { width: 1280, height: 800 } });
  const gate = () => {
    let release;
    const promise = new Promise((resolve) => { release = resolve; });
    hold = { promise, release };
  };
  const verdictOf = (id) => s.page.evaluate((one) => {
    const row = document.querySelector('#sessionList .so-row[data-id="' + one + '"]');
    const mark = row && row.querySelector('.so-mark');
    // The corner of the mark: the verdict once it is in, the hourglass while
    // it is read (or while background work runs).
    const corner = row && row.querySelector('.so-mark__badge');
    const kind = corner ? corner.dataset.kind : '';
    const glass = kind === 'checking' || kind === 'tasks';
    const home = document.querySelector('#home .so-home__card[data-id="' + one + '"]');
    const todos = home ? [...home.querySelectorAll('.so-home__todos li')].map((li) => li.textContent) : [];
    return {
      checking: kind === 'checking',
      glass,
      kind: glass ? '' : kind,
      said: mark ? mark.getAttribute('data-tip') || '' : '',
      homeBadge: home ? (home.querySelector('.so-mark__badge:not([hidden])') || {}).dataset?.kind || '' : '',
      homeMeta: home ? home.querySelector('.so-home__card-status').textContent : '',
      reason: home ? (home.querySelector('.so-home__reason') || {}).textContent || '' : '',
      todos,
    };
  }, id);
  const awaitVerdict = async (id, test, timeout = 20000) => {
    const started = Date.now();
    let seen = null;
    while (Date.now() - started < timeout) {
      seen = await verdictOf(id);
      if (test(seen)) return { ok: true, ms: Date.now() - started, seen };
      await wait(200);
    }
    return { ok: false, ms: Date.now() - started, seen };
  };
  try {
    // Notifications on, and no push service: the page's own notification
    // is the one this device gets.
    await s.page.addInitScript(() => {
      try { localStorage.setItem('socrates.notify', 'on'); } catch { /* the stub says so */ }
      delete window.PushManager;
    });
    await stubNotify(s.page);
    await setup(s.page, s.url);
    const saved = await s.context.request.put(s.url + '/api/settings', {
      data: { settings: { openrouter: { api_key: 'e2e-key', base_url: stub.url, outcomes: true } } },
    });
    ok(saved.ok(), 'the gateway is the stub and the outcomes are on', saved.status());
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await awaitHome(s.page, (h) => h.shown && h.conn === 'live', 10000);

    // A chat made on another device, which this page is never on.
    const id = await makeChat(s, 'claude', { title: 'Ship the endpoint' });
    await awaitHome(s.page, (h) => h.rows.some((r) => r.id === id) || h.counts.ready === 1, 10000);
    // Every session on Home, so that a card is drawn wherever it is counted;
    // and a moment for the chat that has just been made to settle, which is
    // no news (notify.js hush).
    await s.page.click('.so-home__seg[data-filter="all"]');
    await wait(5500);
    gate();
    ok((await tellChat(s, id, '/todo')) === 200, 'Claude Code is asked to make its task list', '/todo');
    const checking = await awaitVerdict(id, (v) => v.checking && v.glass, 20000);
    ok(checking.ok, 'while the verdict is read the row carries the hourglass', took(checking));
    const counted = await awaitHome(s.page, (h) => h.counts.tasks === 1 && h.counts.finished === 0, 5000);
    ok(counted.ok, 'and Home counts the chat under Background tasks', JSON.stringify(counted.seen.counts));
    ok(/Checking/.test(checking.seen.said), 'and says it is being checked', checking.seen.said + ' | ' + checking.seen.homeMeta);
    await wait(1500);
    const quiet = await rang(s.page);
    ok(quiet.notes.length === 0 && quiet.beeps === 0, 'the notification and the chime wait for it',
      JSON.stringify({ notes: quiet.notes.length, beeps: quiet.beeps }));
    const asked = judged[judged.length - 1] || '';
    ok(asked.includes('/todo') && asked.includes('[x] first step') && asked.includes('[ ] second step'),
      'the read is given the instruction and Claude Code’s task list, from the journal', oneLine(asked.slice(0, 300)));

    hold.release();
    hold = null;
    const partly = await awaitVerdict(id, (v) => v.kind === 'partial' && v.homeBadge === 'partial', 20000);
    ok(partly.ok, 'the verdict is on the row and on Home', took(partly));
    ok(partly.seen.todos.join('|') === 'second step' && /still open/.test(partly.seen.reason)
      && /Partly done/.test(partly.seen.homeMeta),
      'Home says why, and what is still open', JSON.stringify(partly.seen));
    const finished = await awaitHome(s.page, (h) => h.counts.finished === 1 && h.counts.tasks === 0, 5000);
    ok(finished.ok, 'and counts it as Finished', JSON.stringify(finished.seen.counts));
    let news = null;
    for (const end = Date.now() + 5000; Date.now() < end && !news; await wait(200)) {
      const r = await rang(s.page);
      if (r.notes.length) news = r;
    }
    ok(!!news && news.notes[0].options.body === 'Partly done · 1 open: second step' && news.beeps === 1,
      'the notification comes with the verdict, and the chime with it, once',
      JSON.stringify(news && { body: news.notes[0].options.body, beeps: news.beeps }));

    // A reload brings it back with the hello.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    const kept = await awaitVerdict(id, (v) => v.kind === 'partial', 15000);
    ok(kept.ok, 'a reload keeps the verdict', took(kept));

    // The header of the chat says it, as a tag that says why.
    await openChat(s.page, id);
    const tag = await s.page.evaluate(() => {
      const t = document.getElementById('sessionOutcome');
      return { shown: !t.hidden, text: t.textContent, kind: t.dataset.kind, title: t.getAttribute('data-tip') || '' };
    });
    ok(tag.shown && tag.text === 'Partly done' && tag.kind === 'partial' && /still open/.test(tag.title),
      'the header carries the verdict as a tag', JSON.stringify(tag));

    // The next turn finishes the list, sent from here: the verdict goes with
    // the turn it was for, and the next one is complete.
    gate();
    await say(s.page, '/todo done');
    const working = await awaitVerdict(id, (v) => !v.kind, 10000);
    ok(working.ok, 'a new turn takes the old verdict away', took(working));
    hold.release();
    hold = null;
    const complete = await awaitVerdict(id, (v) => v.kind === 'complete', 20000);
    ok(complete.ok, 'and the turn that finishes the list is complete', took(complete));

    // Switched off, the verdicts go.
    const off = await s.context.request.put(s.url + '/api/settings', {
      data: { settings: { openrouter: { outcomes: false } } },
    });
    ok(off.ok(), 'the outcomes are switched off', off.status());
    const gone = await awaitVerdict(id, (v) => !v.kind && !v.checking, 10000);
    ok(gone.ok, 'and the verdict is gone from the row', took(gone));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    if (hold) hold.release();
    await s.stop();
    await stub.close();
  }
}

// home-question: the Questions card, the dialog, a tap, and the answer at the
// agent - Claude Code's ask_user returning the person's choice.
async function homequestion() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const { id, askId } = await askingChat(s, 'Pick a database');
    const card = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.asking, 10000);
    ok(card.ok, 'the Questions card says one', JSON.stringify(card.seen.counts));
    const said = await watchAsk(s.page);

    await s.page.click('.so-home__q-action');
    const shown = await awaitAsk(s.page, (d) => !!d && d.question === HOME_QUESTION && d.options.length === 2, 10000);
    const look = shown.seen || {};
    ok(shown.ok, 'the card opens the dialog at the question', look.question);
    ok(look.session === 'Pick a database' && look.open && look.title === 'Storage' && /setting up storage/.test(look.context),
      'it names the chat, offers to open it, and says what the questions are about',
      JSON.stringify({ session: look.session, open: look.open, title: look.title, context: look.context }));
    ok(shown.ok && look.options.map((o) => o.label).join(',') === 'Postgres,SQLite'
      && look.options[0].desc === 'Relational, robust',
      'the options are the agent’s, with their descriptions', JSON.stringify(look.options && look.options.map((o) => o.label + ': ' + o.desc)));
    ok(shown.ok && look.options.every((o) => o.h >= 64 && o.size === '20px'),
      'each option is a big button: at least 64 px high, 20 px type', JSON.stringify(look.options && look.options.map((o) => o.h + 'px/' + o.size)));
    ok(look.other && look.read && look.actions.includes('send'),
      'there is Other for an answer of one’s own, Read aloud, and Send', JSON.stringify({ other: look.other, read: look.read, actions: look.actions }));
    await askSettled(s.page);
    await shot(s.page, 'home-question-desktop');

    await s.page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    const chosen = await askLook(s.page);
    ok(chosen.options.find((o) => o.id === 'sqlite').pressed === 'true' && chosen.options.find((o) => o.id === 'postgres').pressed === 'false',
      'a tap chooses SQLite', JSON.stringify(chosen.options.map((o) => o.id + '=' + o.pressed)));
    await s.page.click('dialog.so-askdlg [data-action="send"]');

    const got = await returned(s, id);
    ok(!!got && got.answers && got.answers.db && got.answers.db.selected.join(',') === 'sqlite' && got.answers.db.skipped === false,
      'Claude Code’s ask_user returned SQLite, as REBUILD §5.2 has it', JSON.stringify(got));
    const left = await awaitChat(s, id, (b) => (b.asks || []).length === 0 && idle(b), 10000);
    ok((left.asks || []).length === 0 && left.chat.activity !== 'waiting', 'and the chat is no longer waiting', left.chat.activity);
    const ask = await askOf(s, id, askId);
    ok(!!ask && ask.answered && ask.by_device !== undefined, 'the question is answered, and says from where', JSON.stringify(ask && { answered: ask.answered, by: ask.by_device }));

    const closed = await askShut(s.page, 15000);
    const story = await said();
    ok(closed, 'the dialog closes when no question is left', closed ? 'shut' : JSON.stringify(await askLook(s.page)));
    ok(story.some((line) => /^Sending your answer|^Answered$/.test(line)) && story.includes('Answered'),
      'it said the answer was on its way, and that it was answered', story.join(' | '));
    const after = await awaitHome(s.page, (h) => h.counts.questions === 0 && !h.asking, 10000);
    ok(after.ok, 'and the Questions card is back to zero', JSON.stringify(after.seen.counts));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-own-answer: an answer the options do not have, written under Other.
async function homeownanswer() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const { id } = await askingChat(s, 'Pick a database');
    const row = await awaitHome(s.page, (h) => h.rows.some((r) => r.id === id && r.answer), 10000);
    ok(row.ok, 'the card has its own Answer button', took(row));
    await s.page.click('.so-home__card[data-id="' + id + '"] .so-home__answer');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);

    await s.page.click('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await s.page.click('dialog.so-askdlg [data-action="other"]');
    await s.page.fill('#ask-db-other', 'MariaDB, please');
    const typed = await askLook(s.page);
    ok(typed.options.every((o) => o.pressed === 'false'),
      'writing an answer of one’s own takes the place of the chosen option',
      JSON.stringify(typed.options.map((o) => o.id + '=' + o.pressed)));
    await s.page.click('dialog.so-askdlg [data-action="send"]');

    const got = await returned(s, id);
    const db = (got && got.answers && got.answers.db) || {};
    ok(db.other === 'MariaDB, please' && (db.selected || []).length === 0,
      'the agent got the words, and no option', JSON.stringify(db));
    ok(await askShut(s.page, 15000), 'the dialog closes when it is done', 'shut');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-queue: two questions, one after the other, oldest first.
async function homequeue() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const first = await askingChat(s, 'First question');
    await wait(1200);
    const second = await askingChat(s, 'Second question');
    const card = await awaitHome(s.page, (h) => h.counts.questions === 2, 10000);
    ok(card.ok, 'the card counts two', JSON.stringify(card.seen.counts));

    await s.page.click('.so-home__q-action');
    const opened = await awaitAsk(s.page, (d) => !!d && d.options.length === 2 && /of 2/.test(d.top), 10000);
    ok(opened.ok && opened.seen.top === 'Questions · 1 of 2' && opened.seen.session === 'First question',
      'the dialog walks them oldest first: "1 of 2"', JSON.stringify(opened.seen && { top: opened.seen.top, session: opened.seen.session }));
    const named = await s.page.locator('dialog.so-askdlg').ariaSnapshot();
    const toggles = await s.page.$$eval('dialog.so-askdlg .so-q-option', (options) => options.map((o) => ({
      tag: o.tagName, role: o.getAttribute('role'), pressed: o.getAttribute('aria-pressed'), checked: o.getAttribute('aria-checked'),
    })));
    ok(!/\b(radio|checkbox)\b/.test(named) && toggles.length === 2
      && toggles.every((o) => o.tag === 'BUTTON' && !o.role && o.pressed === 'false' && o.checked === null),
      'the options are toggle buttons, never radios', JSON.stringify(toggles));

    await s.page.click('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const moved = await awaitAsk(s.page, (d) => !!d && d.session === 'Second question' && d.top === 'Questions · 2 of 2'
      && d.options.length === 2 && d.options.every((o) => o.pressed === 'false'), 20000);
    ok(moved.ok, 'once the first is answered the second takes the dialog', JSON.stringify(moved.seen && { session: moved.seen.session, top: moved.seen.top }));
    const toasts = await watchToasts(s.page);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    // A link followed in the moment the last word is on screen - a
    // notification tapped just then - is answered, not dropped with the
    // dialog: there is nothing left to ask, and the page says so.
    const lastWord = await s.page.waitForFunction(() => {
      const title = document.querySelector('dialog.so-askdlg .so-q-finished .ds-empty__title');
      return !!title && title.textContent === 'Answered';
    }, null, { timeout: 30000, polling: 50 }).then(() => true, () => false);
    ok(lastWord && await askShut(s.page, 20000), 'after the last one says it is answered, the dialog closes', 'shut');
    await s.page.evaluate((target) => { location.hash = '#q=' + target; }, first.id);
    const told = await s.page.waitForFunction(() => (window.__toastsSeen || []).some((t) => /Nothing is waiting for an answer/.test(t)),
      null, { timeout: 10000 }).then(() => true, () => false);
    ok(told && !(await askLook(s.page)), 'a link to a question already answered says nothing is waiting any more',
      JSON.stringify(await toasts()));
    await s.page.click('.so-home__seg[data-filter="all"]');
    await awaitHome(s.page, (h) => h.rows.length === 2, 5000);
    const row = await s.page.evaluate(() => {
      const one = document.querySelector('#home .so-home__card');
      return one && {
        role: one.getAttribute('role'),
        title: one.querySelector('.so-home__card-title').tagName,
        nested: !!one.querySelector('button button, [role=button] button'),
      };
    });
    ok(!!row && row.role === null && row.title === 'BUTTON' && !row.nested,
      'a Home card is a title button beside its Answer button, never a button in a button', JSON.stringify(row));
    const firstGot = await returned(s, first.id);
    const secondGot = await returned(s, second.id);
    ok(firstGot && secondGot && firstGot.answers.db.selected.join() === 'postgres' && secondGot.answers.db.selected.join() === 'sqlite',
      'each chat got its own answer', JSON.stringify([firstGot && firstGot.answers.db.selected, secondGot && secondGot.answers.db.selected]));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-elsewhere: a question answered on another device while the dialog
// shows it; an answer to a question already answered, or to none; an answer
// on its way when another device's lands first; and a question whose agent
// died under it, which is still there to be answered.
async function homeelsewhere() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const { id, askId } = await askingChat(s, 'Pick a database');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    const said = await watchAsk(s.page);
    await s.page.click('.so-home__q-action');
    const shown = await awaitAsk(s.page, (d) => !!d && d.question === HOME_QUESTION, 10000);
    ok(shown.ok, 'the dialog shows the question', shown.seen && shown.seen.question);

    const there = await answerFrom(s, id, askId, { db: ['sqlite'] }, 'the phone');
    const gone = await awaitAsk(s.page, (d) => !!d && /Answered on another device/.test(d.finished), 10000);
    ok(there.status() === 200 && gone.ok && /On the phone/.test(gone.seen.finishedText),
      'the dialog says the question was answered on another device, and which', JSON.stringify(gone.seen && { finished: gone.seen.finished, text: gone.seen.finishedText }));
    await s.page.click('dialog.so-askdlg [data-action="next"]');
    ok(await askShut(s.page, 10000), 'and Close puts it away', (await said()).join(' | '));
    const got = await returned(s, id);
    ok(!!got && got.answers.db.selected.join() === 'sqlite', 'the agent took the other device’s SQLite', JSON.stringify(got));

    // A question already answered takes no second answer, and one that does
    // not exist takes none at all.
    const again = await answerFrom(s, id, askId, { db: ['postgres'] }, 'the desk');
    const unknown = await answerFrom(s, id, 'ask-that-never-was', { db: ['postgres'] }, 'the desk');
    const body = await again.json().catch(() => ({}));
    ok(again.status() === 409 && body.answered_elsewhere === true && body.by_device === 'the phone' && unknown.status() === 404,
      'an answer to a question already answered is refused, saying by whom, and one to no question is not found',
      JSON.stringify({ again: again.status(), by: body.by_device, unknown: unknown.status() }));

    // The question answered on another device while this one's answer is on
    // its way: the refusal is the news that it went, never a second answer.
    const second = await askingChat(s, 'Pick a database again');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    const held = gate();
    await s.page.route('**/api/chats/*/asks/*/answer', async (route) => {
      await held.promise;
      return route.continue();
    });
    const raced = await watchAsk(s.page);
    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    await awaitAsk(s.page, (d) => !!d && /Sending your answer/.test(d.status), 5000);
    const first = await answerFrom(s, second.id, second.askId, { db: ['sqlite'] }, 'the phone');
    held.release();
    const lost = await awaitAsk(s.page, (d) => !d || /Answered on another device/.test(d.finished), 15000);
    const racedWords = await raced();
    await s.page.unroute('**/api/chats/*/asks/*/answer');
    const kept = await returned(s, second.id);
    ok(first.status() === 200 && lost.ok && racedWords.some((w) => /Answered on another device/.test(w)),
      'an answer that lost the race says the question was answered on another device', racedWords.join(' | '));
    ok(!!kept && kept.answers.db.selected.join() === 'sqlite', 'and only the other device’s SQLite went in', JSON.stringify(kept));
    await s.page.click('dialog.so-askdlg [data-action="next"]').catch(() => {});
    await askShut(s.page, 10000);

    // An agent that dies with its question open: the question survives it
    // (REBUILD §5.3), stays on Home, and its answer reaches the agent the
    // next host starts - as a message, the call being gone.
    const dies = await askingChat(s, 'Dies with a question');
    await awaitHome(s.page, (h) => h.rows.some((r) => r.id === dies.id && r.answer), 10000);
    for (const pid of hostsOf(s.data)) {
      try { process.kill(pid, 'SIGKILL'); } catch { /* gone already */ }
    }
    await until(() => hostsOf(s.data).length === 0, 10000);
    await wait(1500);
    const stays = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.rows.some((r) => r.id === dies.id && r.answer), 10000);
    ok(stays.ok, 'a question whose agent died under it is still on Home, to be answered', JSON.stringify(stays.seen.counts));
    await s.page.click('.so-home__card[data-id="' + dies.id + '"] .so-home__answer');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const settled = await awaitAsk(s.page, (d) => !d || d.finished === 'Answered', 15000);
    const delivered = await awaitChat(s, dies.id, (b) => (b.asks || []).length === 0 && idle(b) && b.chat.last_seq > 0, 30000);
    const history = await historyOf(s, dies.id);
    const asMessage = (history.messages || []).some((m) => m.role === 'user' && m.answer_to);
    ok(settled.ok && (delivered.asks || []).length === 0 && asMessage,
      'it is answered from the dialog, and the answer goes to the agent as a message',
      JSON.stringify({ settled: settled.seen && settled.seen.finished, asks: (delivered.asks || []).length, asMessage }));
    ok(unexpected(s.errors, REFUSED_NOISE).length === 0, 'no unexpected console errors', unexpected(s.errors, REFUSED_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-deeplink: the two addresses a notification opens a tab at - #q= for a
// chat's question and #open= for a session - and what each says when there
// is nothing to open: a chat that asks nothing, a Shell, a chat that is gone,
// a tab whose connection went before the question was asked, and a tab that
// was signed out.
async function homedeeplink() {
  const s = await homeChats();
  try {
    const { id } = await askingChat(s, 'Pick a database');
    const quiet = await makeChat(s, 'claude', { title: 'Asks nothing' });
    const shellMade = await s.context.request.post(s.url + '/api/sessions', { data: { harness: 'shell', title: 'Plain shell' } });
    const shell = (await shellMade.json()).session.id;
    const away = await makeChat(s, 'claude', { title: 'Asked while away' });

    const tab = await s.context.newPage();
    tab.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await tab.goto(s.url + '/#q=' + id, { waitUntil: 'domcontentloaded' });
    const shown = await awaitAsk(tab, (d) => !!d && d.question === HOME_QUESTION && d.options.length === 2, 15000);
    const behind = await homeLook(tab);
    ok(shown.ok && behind.shown, '#q= opens the question dialog on boot, over Home',
      JSON.stringify({ question: shown.seen && shown.seen.question, home: behind.shown }));
    await tab.click('dialog.so-askdlg [data-action="close"]');
    ok(await askShut(tab, 5000), 'and Close puts it away', 'shut');
    await tab.close();

    const other = await s.context.newPage();
    other.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await other.goto(s.url + '/#open=' + shell, { waitUntil: 'domcontentloaded' });
    const attached = await other.waitForSelector('#term .xterm', { timeout: 20000 }).then(() => true, () => false);
    const there = await homeLook(other);
    ok(attached && !there.shown && there.hash === '#' + shell, '#open= opens that session, and the address becomes its own',
      JSON.stringify({ attached, home: there.shown, hash: there.hash }));
    await other.goto(s.url + '/#' + id, { waitUntil: 'domcontentloaded' });
    await other.reload({ waitUntil: 'domcontentloaded' });
    const landed = await awaitHome(other, (h) => h.shown, 15000);
    ok(landed.ok && landed.seen.hash === '#home', 'a plain session address lands on Home', landed.seen && landed.seen.hash);
    await other.close();

    // Neither address leaves an entry behind: Back from the app leaves it,
    // rather than opening the question or the session again.
    const hist = await s.context.newPage();
    hist.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await hist.goto('about:blank');
    await hist.goto(s.url + '/#q=' + id, { waitUntil: 'domcontentloaded' });
    await awaitAsk(hist, (d) => !!d && d.options.length === 2, 15000);
    await hist.click('dialog.so-askdlg [data-action="close"]');
    await hist.goBack().catch(() => {});
    await wait(800);
    const afterQ = hist.url();
    await hist.goto(s.url + '/#open=' + shell, { waitUntil: 'domcontentloaded' });
    await hist.waitForFunction((target) => location.hash === '#' + target, shell, { timeout: 15000 }).catch(() => {});
    await wait(800);
    await hist.goBack().catch(() => {});
    await wait(800);
    const afterOpen = hist.url();
    ok(afterQ === 'about:blank' && afterOpen === 'about:blank', 'Back after #q= or #open= leaves the app, and opens nothing again',
      JSON.stringify({ afterQ, afterOpen }));

    // #q= with nothing to open says why, and never that it was answered
    // elsewhere: a chat that asks nothing, a Shell, a chat that is gone.
    // The toast comes while the page is still booting, so it is caught from
    // the first moment the page has a body.
    await hist.addInitScript(() => {
      window.__bootToasts = [];
      addEventListener('DOMContentLoaded', () => {
        const host = document.getElementById('toasts');
        if (!host) return;
        new MutationObserver((records) => {
          for (const record of records) {
            for (const node of record.addedNodes) if (node.textContent) window.__bootToasts.push(node.textContent.trim());
          }
        }).observe(host, { childList: true, subtree: true });
      });
    });
    const toldAt = async (address) => {
      await hist.goto('about:blank');
      await hist.goto(s.url + address, { waitUntil: 'domcontentloaded' });
      const toasts = () => hist.evaluate(() => (window.__bootToasts || []).slice());
      for (let i = 0; i < 60; i += 1) {
        if ((await toasts()).length) break;
        await wait(150);
      }
      return { toasts: await toasts(), dialog: !!(await askLook(hist)) };
    };
    const idleSaid = await toldAt('/#q=' + quiet);
    const shellSaid = await toldAt('/#q=' + shell);
    const goneSaid = await toldAt('/#q=nosuchchat1');
    ok(!idleSaid.dialog && idleSaid.toasts.some((t) => /Nothing is waiting for an answer/.test(t)),
      '#q= for a chat that asks nothing says nothing is waiting', JSON.stringify(idleSaid));
    ok(!shellSaid.dialog && shellSaid.toasts.some((t) => /Nothing is waiting for an answer there/.test(t)),
      'for a Shell, the same', JSON.stringify(shellSaid));
    ok(!goneSaid.dialog && goneSaid.toasts.some((t) => /not here any more/.test(t)),
      'and for a chat that does not exist, that it is not here', JSON.stringify(goneSaid));
    await hist.close();

    // A notification tapped on a tab whose connection went before the
    // question was asked: the tab cannot read it, says so, and shows it once
    // it can - never that it was answered.
    const stale = await deviceOf(s, { viewport: { width: 1024, height: 700 } });
    let blocked = false;
    const sockets = [];
    await stale.page.routeWebSocket(/\/api\/events/, (ws) => {
      if (blocked) { ws.close(); return; }
      ws.connectToServer();
      sockets.push(ws);
    });
    await stale.page.route(/\/api\/(home|sessions|chats\/[^/]+)(\?.*)?$/, (route) => (blocked ? route.abort('internetdisconnected') : route.continue()));
    await stale.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    const staleLive = await awaitHome(stale.page, (h) => h.shown && h.conn === 'live', 15000);
    const staleToasts = await watchToasts(stale.page);
    blocked = true;
    for (const ws of sockets.splice(0)) await ws.close().catch(() => {});
    await awaitHome(stale.page, (h) => h.conn !== 'live', 10000);
    await tellChat(s, away, '/askuser ' + JSON.stringify(DB_ASK));
    const awayAsk = await openAskOf(s, away, 20000);
    ok(staleLive.ok && !!awayAsk, 'a tab goes offline, and a question is asked while it is', JSON.stringify({ live: staleLive.ok, ask: !!awayAsk }));
    await stale.page.evaluate((target) => {
      navigator.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { t: 'open', id: target, kind: 'question' } }));
    }, away);
    await wait(3500);
    const offline = await askLook(stale.page);
    const offToasts = await staleToasts();
    ok(!offline && offToasts.some((t) => /No connection\. The question opens as soon as it is back/.test(t))
      && !offToasts.some((t) => /elsewhere|could not be loaded/.test(t)),
      'the tapped notification says there is no connection and the question opens once there is, never that it was answered',
      JSON.stringify({ dialog: !!offline, toasts: offToasts }));
    blocked = false;
    const found = await awaitAsk(stale.page, (d) => !!d && d.question === HOME_QUESTION && d.session === 'Asked while away' && d.options.length === 2, 40000);
    ok(found.ok, 'and the question appears once the network is back', JSON.stringify(found.seen && { q: found.seen.question, s: found.seen.session }));
    await stale.context.close();

    // Signed out - the login expired or was revoked - the tap goes through
    // the sign-in and on to the question, not to a bare Home.
    const signedOut = await s.browser.newContext({ colorScheme: 'light', viewport: { width: 1024, height: 700 } });
    const outPage = await signedOut.newPage();
    outPage.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await outPage.goto(s.url + '/#q=' + away, { waitUntil: 'domcontentloaded' });
    const atLogin = outPage.url();
    await outPage.fill('#password', PASSWORD);
    await outPage.click('#submit');
    await outPage.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15000 }).catch(() => {});
    const afterLogin = await awaitAsk(outPage, (d) => !!d && d.question === HOME_QUESTION && d.session === 'Asked while away', 15000);
    ok(/\/login#q=/.test(atLogin) && afterLogin.ok, 'signed out, #q= goes through the sign-in and opens the question after it',
      JSON.stringify({ atLogin: atLogin.replace(s.url, ''), now: outPage.url().replace(s.url, ''), dialog: afterLogin.seen && afterLogin.seen.session }));
    await signedOut.close();
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-reconnect: the server restarts under an open Home, which says so and
// comes back to the right numbers by itself.
async function homereconnect() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown && h.conn === 'live', 10000);
    const { id, askId } = await askingChat(s, 'Pick a database');
    const before = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.conn === 'live', 10000);
    ok(before.ok && before.seen.connText === 'Live', 'Home is live and counts the question', before.seen.connText);
    await s.page.evaluate(() => {
      window.__sameHome = true;
      window.__conn = [];
      const host = document.querySelector('#home .so-home__conn');
      new MutationObserver(() => {
        const said = host.dataset.status + ':' + host.textContent.trim();
        if (window.__conn[window.__conn.length - 1] !== said) window.__conn.push(said);
      }).observe(host, { attributes: true, childList: true, subtree: true, characterData: true });
    });

    await s.restart();
    const live = await s.page.waitForFunction(() => window.__conn.some((c) => c.startsWith('connecting'))
      && window.__conn[window.__conn.length - 1].startsWith('live'), null, { timeout: 30000 }).then(() => true, () => false);
    const story = await s.page.evaluate(() => window.__conn.slice());
    ok(live && story.some((c) => c === 'connecting:Reconnecting…'), 'Home says Reconnecting… while the server is away, then Live',
      story.join(' > '));
    const after = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.counts.ready === 0, 10000);
    ok(after.ok, 'and the numbers are right again', JSON.stringify(after.seen.counts));
    const summary = await awaitHome(s.page, (h) => h.rows.some((r) => r.id === id && r.summary === HOME_QUESTION && r.answer), 20000);
    ok(summary.ok, 'the new server has the question from the chat’s journal, its words on the card', took(summary));

    const fresh = await makeChat(s, 'claude', { title: 'Made after the restart' });
    const counted = await awaitHome(s.page, (h) => h.counts.ready === 1, 10000);
    ok(counted.ok, 'a session made after the restart is counted live', JSON.stringify(counted.seen.counts) + ' ' + fresh);
    ok(await s.page.evaluate(() => window.__sameHome === true), 'all of it without a reload', 'same page');

    // Away long enough for several handshakes to be refused, with the
    // question answered on another device the moment the server is back. A
    // poll that gets through before the next handshake is not a proxy that
    // blocks WebSockets: Home never says "Updated every 5 s" over numbers
    // nobody confirmed, and comes back Live without the answered question.
    await awaitHome(s.page, (h) => h.conn === 'live' && h.counts.questions === 1, 15000);
    await s.page.evaluate(() => { window.__conn = []; });
    await s.restart({}, async () => { await wait(3000); });
    const elsewhere = await answerFrom(s, id, askId, { db: ['postgres'] }, 'the phone');
    ok(elsewhere.status() === 200, 'the question is answered on another device as the server comes back', String(elsewhere.status()));
    const relive = await s.page.waitForFunction(() => window.__conn.length > 0
      && window.__conn[window.__conn.length - 1].startsWith('live'), null, { timeout: 30000 }).then(() => true, () => false);
    await wait(6000);
    const longStory = await s.page.evaluate(() => window.__conn.slice());
    ok(relive && longStory.some((c) => c.startsWith('connecting')) && !longStory.some((c) => c.startsWith('polling') || /Updated every/.test(c)),
      'after a longer outage Home says Reconnecting… and then Live, never "Updated every 5 s"', longStory.join(' > '));
    const recounted = await awaitHome(s.page, (h) => h.conn === 'live' && h.counts.questions === 0, 20000);
    ok(recounted.ok, 'and the question answered elsewhere while it was away is no longer counted', JSON.stringify(recounted.seen.counts));

    // A quick look at another app keeps the stream: no new handshake, and
    // Home never says Reconnecting… when it is looked at again.
    const sockets = [];
    s.page.on('websocket', (socket) => { if (socket.url().includes('/api/events')) sockets.push(socket.url()); });
    await s.page.evaluate(() => { window.__conn = []; });
    const shownAs = (state) => s.page.evaluate((hidden) => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (hidden ? 'hidden' : 'visible') });
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });
      document.dispatchEvent(new Event('visibilitychange'));
    }, state === 'hidden');
    await shownAs('hidden');
    await wait(3000);
    await shownAs('visible');
    await wait(1500);
    const glance = await s.page.evaluate(() => window.__conn.slice());
    const still = await awaitHome(s.page, (h) => h.conn === 'live', 2000);
    ok(sockets.length === 0 && still.ok && !glance.some((c) => c.startsWith('connecting')),
      'a page hidden for three seconds keeps its stream and stays Live', JSON.stringify({ sockets: sockets.length, glance }));

    // Signing out closes that login's streams with 4401, and the other tab
    // of the same login goes to the sign-in by itself; a device with a login
    // of its own is not touched.
    const leaver = await s.browser.newContext({ colorScheme: 'light', viewport: { width: 1024, height: 700 } });
    const signedIn = await leaver.request.post(s.url + '/api/login', { data: { password: PASSWORD } });
    ok(signedIn.ok(), 'a second device signs in with a login of its own', String(signedIn.status()));
    const closes = [];
    await leaver.exposeFunction('__streamClosed', (code, url) => { if (String(url).includes('/api/events')) closes.push(code); });
    await leaver.addInitScript(() => {
      const Native = window.WebSocket;
      window.WebSocket = class extends Native {
        constructor(...args) {
          super(...args);
          this.addEventListener('close', (event) => { if (window.__streamClosed) window.__streamClosed(event.code, this.url); });
        }
      };
    });
    const leaving = await leaver.newPage();
    const staying = await leaver.newPage();
    await leaving.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await staying.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    const bothLive = await awaitHome(staying, (h) => h.shown && h.conn === 'live', 15000);
    ok(bothLive.ok, 'both of its tabs show Home live', bothLive.seen && bothLive.seen.conn);
    const mine = [];
    s.page.on('websocket', (socket) => { if (socket.url().includes('/api/events')) mine.push(socket.url()); });
    const out = await leaving.evaluate(() => fetch('/api/logout', { method: 'POST' }).then((r) => r.status));
    const toLogin = await staying.waitForURL(/\/login/, { timeout: 15000 }).then(() => true, () => false);
    ok(out < 300 && toLogin && closes.includes(4401), 'signing out closes the other tab’s stream with 4401, and it goes to the sign-in',
      JSON.stringify({ logout: out, url: staying.url(), closes }));
    const untouched = await awaitHome(s.page, (h) => h.conn === 'live', 3000);
    ok(untouched.ok && mine.length === 0, 'the device with its own login keeps its stream', JSON.stringify({ conn: untouched.seen.conn, sockets: mine.length }));
    await leaver.close();

    // A new password signs every other login out. The device whose login it
    // was goes to the sign-in; the page that changed it was handed a new
    // login, and its stream - closed with the others - comes back Live.
    const other = await deviceOf(s, { viewport: { width: 1024, height: 700 } });
    await other.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    const otherLive = await awaitHome(other.page, (h) => h.shown && h.conn === 'live', 15000);
    ok(otherLive.ok, 'a second device shows Home live', otherLive.seen && otherLive.seen.conn);
    await s.page.evaluate(() => { window.__conn = []; });
    const changed = await s.page.evaluate(async (current) => {
      const res = await fetch('/api/settings/password', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current, next: current + '-2' }),
      });
      return res.status;
    }, PASSWORD);
    ok(changed === 200, 'the password is changed', String(changed));
    const signedOut = await other.page.waitForURL(/\/login/, { timeout: 15000 }).then(() => true, () => false);
    ok(signedOut, 'the signed out device goes to the sign-in by itself', other.page.url());
    const back = await s.page.waitForFunction(() => window.__conn.some((c) => c.startsWith('connecting'))
      && window.__conn[window.__conn.length - 1].startsWith('live'), null, { timeout: 15000 }).then(() => true, () => false);
    ok(back && /#home$/.test(s.page.url()), 'the device that changed it stays on Home and is Live again',
      JSON.stringify({ url: s.page.url(), conn: await s.page.evaluate(() => window.__conn.slice()) }));
    await other.context.close();
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-touch: the reference device is a car. Big buttons, a dialog that is
// the whole screen, the keyboard button's copy where the fields are, taps
// that land in a hurry and send nothing they should not - and the phone, at
// its own size and on its side.
async function hometouch() {
  const s = await homeChats();
  try {
    const first = await askingChat(s, 'Pick a database');
    const posts = [];
    const car = await deviceOf(s, { viewport: { width: 1280, height: 800 }, hasTouch: true, userAgent: TESLA_AGENT });
    car.page.on('request', (req) => { if (req.method() === 'POST' && /\/asks\/[^/]+\/answer$/.test(req.url())) posts.push(req.url()); });
    await car.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    const home = await awaitHome(car.page, (h) => h.shown && h.counts.questions === 1, 15000);
    const touch = await car.page.evaluate(() => document.documentElement.classList.contains('so-touch'));
    ok(home.ok && touch, 'the car lands on Home in touch mode, with the question counted', JSON.stringify({ touch, counts: home.seen.counts }));
    await shot(car.page, 'home-tesla');

    // A double tap on the Questions card, and then a triple one: the first
    // tap opens the dialog, whose first option is drawn right under the
    // finger, and the second must not choose it - nor a third send it -
    // before the person has read anything.
    const card = await car.page.locator('.so-home__questions').boundingBox();
    const centre = { x: card.x + card.width / 2, y: card.y + card.height / 2 };
    const tapCard = async (times) => {
      for (let i = 0; i < times; i += 1) {
        if (i) await wait(120);
        await car.page.touchscreen.tap(centre.x, centre.y);
      }
    };
    await tapCard(2);
    await wait(500);
    const underFinger = await car.page.evaluate(({ x, y }) => {
      const node = document.elementFromPoint(x, y);
      const option = node && node.closest('.so-q-option');
      return option ? 'option:' + option.dataset.option : (node ? node.className : '');
    }, centre);
    const doubled = await askLook(car.page);
    ok(!!doubled && doubled.options.length === 2 && doubled.options.every((o) => o.pressed !== 'true') && posts.length === 0,
      'a double tap on the Questions card opens the dialog and chooses nothing, though an option is drawn under the finger',
      JSON.stringify({ under: underFinger, options: doubled && doubled.options.map((o) => o.id + '=' + o.pressed), posts: posts.length }));
    await car.page.keyboard.press('Escape');
    await askShut(car.page, 5000);
    await wait(600);
    await tapCard(3);
    await wait(2000);
    const tripled = await askLook(car.page);
    const stillOpen = (await chatOf(s, first.id)).asks.length;
    ok(!!tripled && posts.length === 0 && stillOpen === 1 && tripled.options.every((o) => o.pressed !== 'true'),
      'a triple tap sends nothing: the second tap does not choose, so the third has nothing to send',
      JSON.stringify({ posts: posts.length, open: stillOpen, options: tripled && tripled.options.map((o) => o.id + '=' + o.pressed) }));
    // A deliberate tap, once the dialog has been on screen a moment, is taken.
    await wait(400);
    const optionBox = await car.page.locator('dialog.so-askdlg .so-q-option[data-option="sqlite"]').boundingBox();
    await car.page.touchscreen.tap(optionBox.x + optionBox.width / 2, optionBox.y + optionBox.height / 2);
    const deliberate = await awaitAsk(car.page, (d) => !!d && d.options.some((o) => o.id === 'sqlite' && o.pressed === 'true'), 3000);
    ok(deliberate.ok, 'a tap once the dialog has been on screen a moment chooses the option under it',
      JSON.stringify(deliberate.seen && deliberate.seen.options.map((o) => o.id + '=' + o.pressed)));
    await car.page.keyboard.press('Escape');
    await askShut(car.page, 5000);

    await car.page.tap('.so-home__questions');
    await awaitAsk(car.page, (d) => !!d && d.options.length === 2, 10000);
    await askSettled(car.page);
    const shown = await awaitAsk(car.page, (d) => !!d && d.options.length === 2, 1000);
    const look = shown.seen || {};
    ok(shown.ok && look.box.x === 0 && look.box.y === 0 && look.box.w >= look.viewport.w - 1 && look.box.h >= look.viewport.h - 1,
      'the dialog is the whole screen', JSON.stringify({ box: look.box, viewport: look.viewport }));
    ok(shown.ok && look.options.every((o) => o.h >= 64 && o.size === '20px'),
      'the options are at least 64 px high in 20 px type', JSON.stringify(look.options && look.options.map((o) => o.h + 'px/' + o.size)));
    ok(look.kb, 'the keyboard button’s copy is in the dialog', String(look.kb));
    await shot(car.page, 'home-question-tesla');
    await car.page.tap('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await car.page.tap('dialog.so-askdlg [data-action="send"]');
    const got = await returned(s, first.id);
    ok(!!got && got.answers.db.selected.join() === 'postgres' && posts.length === 1, 'a tap in the car answers, once',
      JSON.stringify({ got: got && got.answers.db.selected, posts: posts.length }));
    ok(await askShut(car.page, 15000), 'and the dialog closes', 'shut');
    await car.context.close();

    // The phone, with a question of its own.
    const second = await askingChat(s, 'Pick a database for the phone');
    const phonePosts = [];
    const phone = await deviceOf(s, { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
    phone.page.on('request', (req) => { if (req.method() === 'POST' && /\/asks\/[^/]+\/answer$/.test(req.url())) phonePosts.push(req.url()); });
    await phone.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    const onPhone = await awaitHome(phone.page, (h) => h.shown && h.counts.questions === 1, 15000);
    ok(onPhone.ok && onPhone.seen.overflow <= 1, 'Home fits a phone without scrolling sideways', String(onPhone.seen.overflow));
    await shot(phone.page, 'home-phone');
    await phone.page.tap('.so-home__questions');
    await awaitAsk(phone.page, (d) => !!d && d.options.length === 2, 10000);
    await askSettled(phone.page);
    const sheet = await awaitAsk(phone.page, (d) => !!d && d.options.length === 2, 1000);
    const small = sheet.seen || {};
    ok(sheet.ok && small.box.w >= small.viewport.w - 1 && small.box.h >= small.viewport.h - 1 && small.overflow <= 1
      && small.options.every((o) => o.h >= 64 && o.w <= small.viewport.w),
      'on a phone the dialog is the whole screen too, and nothing in it is wider than the phone',
      JSON.stringify({ box: small.box, overflow: small.overflow, options: small.options && small.options.map((o) => o.w + 'x' + o.h) }));
    await shot(phone.page, 'home-question-phone');
    const readName = await phone.page.locator('dialog.so-askdlg [data-action="read"]').ariaSnapshot();
    ok(/Read aloud/.test(readName), 'on a phone Read aloud still has a name a screen reader says', readName);

    // A double tap on Send: the second tap lands on what takes Send's place,
    // and the answer goes once.
    await phone.page.tap('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    const sendBox = await phone.page.locator('dialog.so-askdlg [data-action="send"]').boundingBox();
    const finger = { x: sendBox.x + sendBox.width / 2, y: sendBox.y + sendBox.height / 2 };
    await phone.page.touchscreen.tap(finger.x, finger.y);
    await wait(250);
    const under = await phone.page.evaluate(({ x, y }) => {
      const node = document.elementFromPoint(x, y);
      return node ? ((node.closest('button') || node).dataset.action || node.className) : '';
    }, finger);
    await phone.page.touchscreen.tap(finger.x, finger.y);
    const phoneGot = await returned(s, second.id);
    await askShut(phone.page, 15000);
    await wait(1000);
    const history = await historyOf(s, second.id);
    ok(!!phoneGot && phoneGot.answers.db.selected.join() === 'sqlite' && phonePosts.length === 1
      && (history.asks || []).filter((a) => a.answered).length === 1,
      'a double tap on Send sends the answer once', JSON.stringify({ under, posts: phonePosts.length, got: phoneGot && phoneGot.answers.db.selected }));
    await phone.context.close();

    // The phone on its side: the dialog opens with an option to tap.
    const third = await askingChat(s, 'Pick a database on its side');
    for (const [width, height] of [[844, 390], [740, 360]]) {
      const side = await deviceOf(s, { viewport: { width, height }, hasTouch: true, isMobile: true });
      await side.page.goto(s.url + '/#q=' + third.id, { waitUntil: 'domcontentloaded' });
      await awaitAsk(side.page, (d) => !!d && d.options.length === 2, 15000);
      await askSettled(side.page);
      const fit = await side.page.evaluate(() => {
        const dialog = document.querySelector('dialog.so-askdlg');
        const option = dialog.querySelector('.so-q-option').getBoundingClientRect();
        const bar = dialog.querySelector('.so-q-actions').getBoundingClientRect();
        return { top: Math.round(option.top), bottom: Math.round(option.bottom), bar: Math.round(bar.top), h: innerHeight };
      });
      ok(fit.top >= 0 && fit.bottom <= fit.bar && fit.bottom <= fit.h,
        width + 'x' + height + ': the first option is whole on screen, above the buttons', JSON.stringify(fit));
      if (width === 844) await shot(side.page, 'home-question-landscape');
      await side.context.close();
    }
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// calledAs is the title of the session a digest or operator request is about.
function calledAs(text) {
  const named = String(text).match(/session (?:is called )?"((?:[^"\\]|\\.)*)"/);
  return named ? JSON.parse('"' + named[1] + '"') : '';
}

// answerPosts keeps every POST /answer the page sends from now on.
function answerPosts(page) {
  const posts = [];
  page.on('request', (req) => {
    if (req.method() === 'POST' && /\/api\/sessions\/[^/]+\/answer$/.test(req.url())) {
      try { posts.push(JSON.parse(req.postData() || 'null')); } catch { posts.push(req.postData()); }
    }
  });
  return posts;
}

// awaitRunEnd waits for a session's answer run to end, and hands it back.
async function awaitRunEnd(s, id, timeout = 30000) {
  const until = Date.now() + timeout;
  let run = null;
  while (Date.now() < until) {
    run = await answerRunOf(s, id);
    if (run && (run.phase === 'done' || run.phase === 'error' || run.phase === 'cancelled')) return run;
    await wait(250);
  }
  return run;
}

// focusOf is what has the keyboard in the question dialog.
const focusOf = (page) => page.evaluate(() => {
  const at = document.activeElement;
  if (!at) return 'none';
  if (at.classList.contains('so-q-own-field')) return 'own';
  if (at.classList.contains('so-q-option')) return 'option:' + at.dataset.label;
  return at.className || at.tagName;
});

// Errors a page logs for a request a scenario refused on purpose.
const REFUSED_NOISE = /status of 400|status of 404|status of 500|status of 409|NotAllowedError|NotFoundError|Requested device not found|Permission denied/;

// toastOnTop is the newest toast's words, when it is on top of everything
// - the question dialog included - where it is drawn, else ''.
const toastOnTop = (page) => page.evaluate(() => {
  const all = [...document.querySelectorAll('#toasts .ds-toast')];
  const last = all[all.length - 1];
  if (!last || !last.checkVisibility()) return '';
  const r = last.getBoundingClientRect();
  const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  return hit && last.contains(hit) ? last.textContent.replace(/\s+/g, ' ').trim() : '';
});

// home-focus: the field an answer of one's own is written into keeps the
// keyboard, and its words, while another chat asks, the chat is renamed and
// the dialog is drawn again; the voice's failures are said where they can be
// seen, over the dialog.
async function homefocus() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const alpha = await askingChat(s, 'Alpha asks');
    await wait(800);
    await askingChat(s, 'Beta asks');
    const gamma = await makeChat(s, 'claude', { title: 'Gamma asks later' });
    await awaitHome(s.page, (h) => h.counts.questions === 2, 10000);
    const posts = [];
    s.page.on('request', (req) => { if (req.method() === 'POST' && /\/asks\/[^/]+\/answer$/.test(req.url())) posts.push(req.postData()); });

    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.session === 'Alpha asks' && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg [data-action="other"]');
    await s.page.waitForSelector('#ask-db-other', { timeout: 5000 });
    await s.page.keyboard.type('Use MariaDB, ');
    // A third question joins the walk: the counter changes, the field must
    // not.
    await tellChat(s, gamma, '/askuser ' + JSON.stringify(DB_ASK));
    const joined = await awaitAsk(s.page, (d) => !!d && d.top === 'Questions · 1 of 3', 30000);
    ok(joined.ok && joined.seen.focus === 'own:ask-db-other', 'a third chat asking joins the walk and leaves the keyboard in the field',
      JSON.stringify({ top: joined.seen && joined.seen.top, focus: joined.seen && joined.seen.focus }));

    // A rename, drawn into the dialog.
    await s.context.request.patch(s.url + '/api/sessions/' + alpha.id, { data: { title: 'Alpha renamed' } });
    const renamed = await awaitAsk(s.page, (d) => !!d && d.session === 'Alpha renamed', 10000);
    await s.page.keyboard.type('with 2 ');
    await wait(800);
    ok(renamed.ok && (await askLook(s.page)).focus === 'own:ask-db-other', 'a rename is drawn into the dialog and leaves the field as it was',
      JSON.stringify({ title: renamed.seen && renamed.seen.session, focus: (await askLook(s.page)).focus }));
    await s.page.keyboard.type('replicas');
    const whole = await s.page.$eval('#ask-db-other', (n) => n.value);
    ok(whole === 'Use MariaDB, with 2 replicas' && posts.length === 0,
      'every word typed is in the field, and nothing was sent on the way', JSON.stringify({ whole, posts: posts.length }));
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const got = await returned(s, alpha.id);
    ok(posts.length === 1 && !!got && got.answers.db.other === 'Use MariaDB, with 2 replicas' && got.answers.db.selected.length === 0,
      'Send sends the words, not an option, and the agent got exactly them', JSON.stringify({ posts: posts.length, got: got && got.answers.db }));

    // The next question: Read aloud fails, and the failure is said over
    // the dialog, and stays.
    const onBeta = await awaitAsk(s.page, (d) => !!d && d.session === 'Beta asks' && d.options.length === 2, 20000);
    ok(onBeta.ok, 'the dialog moves on to the next question', onBeta.seen && onBeta.seen.session);
    await s.page.route('**/api/voice/speak', (route) => route.fulfill({
      status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'no voice key is configured here' }),
    }));
    await wait(700);
    await s.page.click('dialog.so-askdlg [data-action="read"]');
    let spoken = '';
    for (let i = 0; i < 40 && !/no voice key/.test(spoken); i += 1) { spoken = await toastOnTop(s.page); await wait(200); }
    ok(/no voice key/.test(spoken), 'a Read aloud that fails says why, on top of the dialog', spoken);
    await s.context.request.patch(s.url + '/api/sessions/' + alpha.id, { data: { title: 'Alpha renamed twice' } });
    await wait(1500);
    const kept = await toastOnTop(s.page);
    ok(/no voice key/.test(kept) && !!(await askLook(s.page)), 'and it is still said after the next live frame, the dialog still up', kept);
    // Dictation on a machine with no microphone.
    await s.page.click('dialog.so-askdlg [data-action="other"]');
    await s.page.waitForSelector('dialog.so-askdlg .so-q-own .so-mic', { timeout: 5000 });
    await s.page.click('dialog.so-askdlg .so-q-own .so-mic');
    let deaf = '';
    for (let i = 0; i < 40 && (!deaf || deaf === kept); i += 1) { deaf = await toastOnTop(s.page); await wait(200); }
    ok(!!deaf && deaf !== kept, 'a dictation that cannot start is said on top of the dialog too', deaf);
    await s.page.click('dialog.so-askdlg [data-action="close"]');
    ok(unexpected(s.errors, REFUSED_NOISE).length === 0, 'no unexpected console errors', unexpected(s.errors, REFUSED_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-retry: an answer the server refuses for good gives the questions back
// with its words, and is sent again once - the one Send; one the server
// cannot take just now is tried again by itself; and an answer to a chat
// deleted while it was on its way says it was not taken, never that it was
// answered on another device.
async function homeretry() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const first = await askingChat(s, 'Try again');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    let refuse = 400;
    let posts = 0;
    await s.page.route('**/api/chats/*/asks/*/answer', (route) => {
      posts += 1;
      if (!refuse) return route.continue();
      const status = refuse;
      // A server that is down for a moment is answered once with 503, and
      // takes the next try.
      if (refuse === 503) refuse = 0;
      return route.fulfill({ status, contentType: 'application/json',
        body: JSON.stringify({ error: status === 400 ? 'the agent could not take that answer' : 'the server is restarting' }) });
    });
    const said = await watchAsk(s.page);
    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const failed = await awaitAsk(s.page, (d) => !!d && /not taken/.test(d.refused), 15000);
    ok(failed.ok && /could not take that answer/.test(failed.seen.refused) && failed.seen.options.find((o) => o.id === 'sqlite').pressed === 'true'
      && failed.seen.actions.includes('send'),
      'an answer refused for good says why, and gives the questions back as they were, to send again',
      JSON.stringify(failed.seen && { refused: failed.seen.refused, actions: failed.seen.actions }));
    ok((await chatOf(s, first.id)).asks.length === 1, 'and the question is still open', 'open');

    // The second Send: the server cannot take it just now, and then can.
    refuse = 503;
    const errorsBefore = s.errors.filter((e) => e.startsWith('pageerror')).length;
    const before = posts;
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const got = await returned(s, first.id, 30000);
    ok(!!got && got.answers.db.selected.join() === 'sqlite' && posts - before === 2,
      'sent again, an answer the server could not take just now is tried again by itself, and goes in once',
      JSON.stringify({ got: got && got.answers.db.selected, tries: posts - before }));
    ok(s.errors.filter((e) => e.startsWith('pageerror')).length === errorsBefore, 'and throws nothing',
      s.errors.filter((e) => e.startsWith('pageerror')).join(' | ') || '0');
    ok(await askShut(s.page, 15000), 'the dialog closes when it is done', (await said()).join(' | '));
    await s.page.unroute('**/api/chats/*/asks/*/answer');

    // A chat deleted while the answer is on its way to the server.
    const gone = await askingChat(s, 'Deleted while sending');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    const held = gate();
    await s.page.route('**/api/chats/*/asks/*/answer', async (route) => {
      await held.promise;
      return route.continue();
    });
    const told = await watchAsk(s.page);
    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.session === 'Deleted while sending' && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    await awaitAsk(s.page, (d) => !!d && /Sending your answer/.test(d.status), 5000);
    await s.context.request.delete(s.url + '/api/sessions/' + gone.id);
    await awaitHome(s.page, (h) => !h.rows.some((r) => r.id === gone.id), 10000);
    held.release();
    const refused = await awaitAsk(s.page, (d) => !d || /not taken/.test(d.refused), 15000);
    const toldWords = await told();
    ok(refused.ok && !!refused.seen, 'an answer to a chat deleted on its way says it was not taken', JSON.stringify(refused.seen && refused.seen.refused));
    ok(!toldWords.some((w) => /another device/.test(w)), 'and is never said to be answered on another device', toldWords.join(' | '));
    await s.page.unroute('**/api/chats/*/asks/*/answer');
    await s.page.click('dialog.so-askdlg [data-action="close"]').catch(() => {});
    // The 503 is the one this scenario answers on purpose; the browser logs
    // it like any other refused request.
    const noise = new RegExp(REFUSED_NOISE.source + '|status of 503');
    ok(unexpected(s.errors, noise).length === 0, 'no unexpected console errors', unexpected(s.errors, noise).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-runlost: an answer on its way when the dialog is put away and opened
// again is shown as on its way, never as a question to answer again; one
// sent while the server restarts goes in once when it is back; and one whose
// reply was lost on the way back - the server took it - is followed to
// "Answered", never "another device".
async function homerunlost() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const { id } = await askingChat(s, 'Lost and found');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    const held = gate();
    await s.page.route('**/api/chats/*/asks/*/answer', async (route) => {
      await held.promise;
      // The page may have given this request up while the server was away
      // and sent the answer again; a request it gave up is handled already.
      return route.continue().catch(() => {});
    });
    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    await awaitAsk(s.page, (d) => !!d && /Sending your answer/.test(d.status), 5000);
    await s.page.click('dialog.so-askdlg [data-action="close"]');
    await askShut(s.page, 5000);
    const said = await watchAsk(s.page);
    await s.page.click('.so-home__card[data-id="' + id + '"] [data-action="answer"]');
    const reopened = await awaitAsk(s.page, (d) => !!d && /Sending your answer/.test(d.status), 5000);
    ok(reopened.ok && !reopened.seen.actions.includes('send'),
      'opened again while its answer is on its way, the dialog shows the answer going, with no Send',
      JSON.stringify(reopened.seen && { status: reopened.seen.status, actions: reopened.seen.actions }));

    // The server goes away with the answer still held, and comes back.
    await s.restart();
    held.release();
    await s.page.unroute('**/api/chats/*/asks/*/answer');
    const done = await awaitAsk(s.page, (d) => !d || d.finished === 'Answered', 45000);
    const words = await said();
    const got = await returned(s, id, 20000);
    const asks = (await historyOf(s, id)).asks || [];
    ok(done.ok && !!got && got.answers.db.selected.join() === 'sqlite' && asks.filter((a) => a.answered).length === 1,
      'an answer sent across a server restart goes in once, and the dialog says it is answered',
      JSON.stringify({ got: got && got.answers.db.selected, words }));
    ok(!words.some((w) => /another device|not taken/.test(w)), 'never that another device answered, nor that it was not taken', words.join(' | '));
    await askShut(s.page, 15000);

    // The reply is lost on the way back - the server took the answer - and
    // the dialog was shut before it came: opened again, it follows the
    // answer as its own.
    const second = await askingChat(s, 'Reply lost');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    let lose = true;
    await s.page.route('**/api/chats/*/asks/*/answer', async (route) => {
      if (!lose) return route.continue();
      lose = false;
      await route.fetch();
      return route.abort('connectionreset');
    });
    const lostSaid = await watchAsk(s.page);
    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="postgres"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    await s.page.click('dialog.so-askdlg [data-action="close"]').catch(() => {});
    await askShut(s.page, 5000);
    const secondGot = await returned(s, second.id, 20000);
    await s.page.click('.so-home__card[data-id="' + second.id + '"] [data-action="answer"]').catch(() => {});
    await wait(2500);
    const lostWords = await lostSaid();
    const after = (await historyOf(s, second.id)).asks || [];
    ok(!!secondGot && secondGot.answers.db.selected.join() === 'postgres' && after.filter((a) => a.answered).length === 1,
      'an answer whose reply was lost went in once', JSON.stringify({ got: secondGot && secondGot.answers.db.selected }));
    ok(!lostWords.some((w) => /another device|not taken/.test(w)), 'and is never called another device’s, nor refused', lostWords.join(' | '));
    await s.page.unroute('**/api/chats/*/asks/*/answer');
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors', unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally { await s.stop(); }
}

// home-opencode: an OpenCode chat's questions from Home's dialog - its
// ask_user call (the MCP server OpenCode starts with the serve), answered
// with an option and with words of one's own, and its own question tool,
// should one still arrive - and a permission it asks for, which the chat
// allows by itself and never puts to anyone.
async function homeopencode() {
  const s = await homeChats();
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const id = await makeChat(s, 'opencode', { title: 'OpenCode asks' });
    const ask = async (text) => {
      await awaitChat(s, id, (b) => idle(b), 20000);
      const said = await tellChat(s, id, text);
      return said === 200 ? openAskOf(s, id, 20000) : '';
    };
    const asked = await ask('/askuser ' + JSON.stringify(DB_ASK));
    const card = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.rows.some((r) => r.id === id && r.answer), 10000);
    ok(!!asked && card.ok, 'OpenCode’s ask_user call is a question Home counts', JSON.stringify({ ask: asked, counts: card.seen.counts }));
    await s.page.click('.so-home__q-action');
    const shown = await awaitAsk(s.page, (d) => !!d && d.question === HOME_QUESTION && d.options.length === 2, 10000);
    ok(shown.ok && shown.seen.session === 'OpenCode asks', 'the dialog shows it, under the chat’s name',
      JSON.stringify(shown.seen && { q: shown.seen.question, s: shown.seen.session }));
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const got = await returned(s, id, 20000);
    ok(!!got && got.answers.db.selected.join() === 'sqlite', 'OpenCode got SQLite', JSON.stringify(got && got.answers.db));
    ok(await askShut(s.page, 15000), 'the dialog closes', 'shut');
    const after = await awaitHome(s.page, (h) => h.counts.questions === 0, 10000);
    ok(after.ok && ((await chatOf(s, id)).asks || []).length === 0, 'and the chat asks nothing any more', JSON.stringify(after.seen.counts));

    // An answer of one's own, from the chat's card.
    const OWN = 'Use DuckDB instead';
    const again = await ask('/askuser ' + JSON.stringify(DB_ASK));
    await awaitHome(s.page, (h) => h.rows.some((r) => r.id === id && r.answer), 10000);
    await s.page.click('.so-home__card[data-id="' + id + '"] .so-home__answer');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    await s.page.click('dialog.so-askdlg [data-action="other"]');
    await s.page.fill('#ask-db-other', OWN);
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const ownAt = Date.now();
    let own = null;
    while (!own && Date.now() - ownAt < 20000) {
      const said = ((await historyOf(s, id)).messages || []).filter((m) => m.role === 'assistant').map(textOf)
        .filter((t) => t.startsWith('ask_user returned: '));
      if (said.length === 2) own = JSON.parse(said[1].slice('ask_user returned: '.length).split('\n')[0]);
      else await wait(200);
    }
    ok(!!again && !!own && own.answers.db.other === OWN && own.answers.db.selected.length === 0,
      'an answer of one’s own reaches OpenCode as the words, with no option', JSON.stringify(own && own.answers.db));
    await askShut(s.page, 15000);

    // OpenCode's own question tool: its labels are the options, and what is
    // chosen goes back as OpenCode's list of labels.
    const own2 = await ask('/question');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    await s.page.click('.so-home__q-action');
    const colour = await awaitAsk(s.page, (d) => !!d && d.question === 'Which colour?', 10000);
    ok(!!own2 && colour.ok && colour.seen.options.map((o) => o.label).join() === 'Red,Blue',
      'OpenCode’s own question tool is a question too, its labels the options',
      JSON.stringify(colour.seen && colour.seen.options.map((o) => o.label)));
    await s.page.click('dialog.so-askdlg .so-q-option:has-text("Blue")');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    const blue = await awaitChat(s, id, (b) => idle(b), 20000);
    const reply = ((await historyOf(s, id)).messages || []).filter((m) => m.role === 'assistant').map(textOf).pop() || '';
    ok(idle(blue) && reply === 'question: [["Blue"]]', 'and Blue is what OpenCode got', reply);
    await askShut(s.page, 15000);

    // A permission OpenCode asks for is allowed by the chat itself: nobody
    // is asked, and the chat says so.
    await awaitChat(s, id, (b) => idle(b), 20000);
    const permitted = await tellChat(s, id, '/perm');
    let askedAny = false;
    const permAt = Date.now();
    let permEnd = null;
    while (!permEnd && Date.now() - permAt < 20000) {
      const body = await chatOf(s, id);
      if ((body.asks || []).length) askedAny = true;
      const last = ((await historyOf(s, id)).messages || []).filter((m) => m.role === 'assistant').map(textOf).pop() || '';
      if (last === 'perm: always' && idle(body)) permEnd = last;
      else await wait(150);
    }
    const quiet = await homeLook(s.page);
    ok(permitted === 200 && permEnd === 'perm: always' && !askedAny && quiet.counts.questions === 0,
      'a permission OpenCode asks for is allowed always, and puts no question to anyone',
      JSON.stringify({ reply: permEnd, asked: askedAny, questions: quiet.counts.questions }));
    await openChat(s.page, id);
    const NOTICE = 'OpenCode asked for permission to use bash (rm -rf build); it was allowed, as every permission is.';
    ok(await awaitLog(s.page, NOTICE, 10000), 'and the chat says what it allowed', NOTICE);
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// streamProxy is a reverse proxy in front of the server whose upgrades can be
// passed, held for ever (`hang`) or refused (`404`), and whose HTTP can be
// cut (`dead`): what a bad network or a proxy does to the event stream.
async function streamProxy(s) {
  const backend = new URL(s.url);
  const p = { mode: 'pass', upgrades: [], held: [], pairs: [] };
  const proxy = http.createServer((req, res) => {
    if (p.mode === 'dead') { req.socket.destroy(); return; }
    const up = http.request({ host: backend.hostname, port: backend.port, method: req.method, path: req.url, headers: req.headers },
      (r) => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
    up.on('error', () => { try { res.destroy(); } catch { /* gone */ } });
    req.pipe(up);
  });
  proxy.on('upgrade', (req, sock, head) => {
    sock.on('error', () => {});
    if (req.url.includes('/api/events')) p.upgrades.push({ at: Date.now(), mode: p.mode });
    if (p.mode === 'hang') { p.held.push(sock); return; }
    if (p.mode !== 'pass') { sock.end('HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n'); return; }
    const up = net.connect(Number(backend.port), backend.hostname, () => {
      let raw = req.method + ' ' + req.url + ' HTTP/1.1\r\n';
      for (let i = 0; i < req.rawHeaders.length; i += 2) raw += req.rawHeaders[i] + ': ' + req.rawHeaders[i + 1] + '\r\n';
      up.write(raw + '\r\n');
      if (head && head.length) up.write(head);
      up.pipe(sock);
      sock.pipe(up);
      p.held.push(sock);
      p.pairs.push({ up, sock });
    });
    up.on('error', () => sock.destroy());
  });
  const port = await freePort();
  await new Promise((resolve) => proxy.listen(port, '127.0.0.1', resolve));
  p.url = 'http://127.0.0.1:' + port;
  // mute is a half-open stream: both sockets stay open, and nothing passes
  // either way - a phone that changed networks under a live page.
  p.mute = () => {
    for (const { up, sock } of p.pairs.splice(0)) { up.unpipe(sock); sock.unpipe(up); up.pause(); sock.pause(); }
  };
  p.drop = () => { for (const sock of p.held) sock.destroy(); p.held.length = 0; };
  p.close = () => { p.drop(); proxy.closeAllConnections(); proxy.close(); };
  return p;
}

const connOf = (page) => page.evaluate(() => {
  const conn = document.querySelector('#home .so-home__conn');
  return {
    status: conn ? conn.dataset.status : '',
    text: conn ? conn.textContent.trim() : '',
    dim: document.getElementById('home').hasAttribute('data-stale'),
  };
});

// home-stream: the event stream against what networks do. A list that comes
// back late does not undo what the stream said since; a handshake that never
// completes is given up on; a proxy that refuses the upgrade leaves Home
// polled and saying so; and an offline Home says how old its numbers are.
async function homestream() {
  const s = await homeChats();
  let p = null;
  try {
    const kept = await makeChat(s, 'claude', { title: 'Kept' });
    const doomed = await makeChat(s, 'claude', { title: 'Deleted meanwhile' });
    await awaitChat(s, kept, (b) => b.chat.process === 'ready' && idle(b), 20000);
    // A chat waiting for a review that starts working may have been filed
    // as in progress again, and the page asks for the list to see: that list
    // is the one held back here.
    await s.context.request.post(s.url + '/api/sessions/' + kept + '/file', { data: { status: 'review' } });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await awaitHome(s.page, (h) => h.shown && h.conn === 'live', 15000);
    const lists = [];
    await s.page.route(/\/api\/sessions$/, async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      const res = await route.fetch();
      lists.push({ route, res, body: await res.text() });
    });
    await tellChat(s, kept, '/slow 3');
    for (let i = 0; i < 40 && !lists.length; i++) await wait(200);
    await s.context.request.delete(s.url + '/api/sessions/' + doomed);
    const moved = await awaitHome(s.page, (h) => h.counts.running === 0 && !h.rows.some((r) => r.id === doomed), 20000);
    const settled = await awaitChat(s, kept, (b) => idle(b) && b.chat.last_outcome === 'completed', 15000);
    for (const held of lists) await held.route.fulfill({ status: held.res.status(), headers: held.res.headers(), body: held.body });
    await s.page.unroute(/\/api\/sessions$/);
    await wait(3000);
    const after = await homeLook(s.page);
    const sidebar = await s.page.$$eval('#sessionList .so-row[data-id]', (rows) => rows.map((r) => r.dataset.id));
    ok(lists.length > 0 && moved.ok && idle(settled), 'a list was asked for while the turn ran, and held back', lists.length + ' held');
    ok(!sidebar.includes(doomed) && !after.rows.some((r) => r.id === doomed) && after.counts.running === 0,
      'when it lands late, the deleted chat stays gone and the finished turn is not running again',
      JSON.stringify({ sidebar: sidebar.length, counts: after.counts }));

    // The rest goes through a proxy.
    p = await streamProxy(s);
    const page = await s.context.newPage();
    page.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await page.goto(p.url + '/', { waitUntil: 'domcontentloaded' });
    const live = await awaitHome(page, (h) => h.shown && h.conn === 'live', 20000);
    ok(live.ok, 'Home through the proxy is live', live.seen && live.seen.conn);

    // The network goes away under a live stream: Reconnecting…, then
    // Offline - never "Updated every 5 s" before a single read has worked.
    await page.evaluate(() => {
      window.__connSaid = [];
      const host = document.querySelector('#home .so-home__conn');
      const keep = () => {
        const said = host.dataset.status + ':' + host.textContent.trim();
        if (window.__connSaid[window.__connSaid.length - 1] !== said) window.__connSaid.push(said);
      };
      new MutationObserver(keep).observe(host, { attributes: true, childList: true, subtree: true, characterData: true });
    });
    p.mode = 'dead';
    p.drop();
    const wentOffline = await page.waitForFunction(() => window.__connSaid.some((c) => c.startsWith('offline')),
      null, { timeout: 30000 }).then(() => true, () => false);
    await wait(3000);
    const outage = await page.evaluate(() => window.__connSaid.slice());
    ok(wentOffline && outage.some((c) => c.startsWith('connecting')) && !outage.some((c) => /Updated every/.test(c)),
      'a network that goes away is Reconnecting…, then Offline, and never said to be polled', outage.join(' > '));
    p.mode = 'pass';
    const relive = await awaitHome(page, (h) => h.conn === 'live', 60000);
    ok(relive.ok, 'and Live again once the network is back', relive.seen && relive.seen.conn);

    p.mode = 'hang';
    const hungAt = Date.now();
    p.drop();
    await wait(26000);
    const hung = p.upgrades.filter((u) => u.at > hungAt && u.mode === 'hang').length;
    ok(hung >= 2, 'a handshake that never completes is given up on and tried again', hung + ' handshakes in 26 s, ' + JSON.stringify(await connOf(page)));
    p.mode = 'pass';
    const passAt = Date.now();
    const again = await awaitHome(page, (h) => h.conn === 'live', 25000);
    ok(again.ok, 'and the stream is live again once the network passes it', ((Date.now() - passAt) / 1000).toFixed(1) + ' s');

    p.mode = '404';
    p.drop();
    let polled = null;
    for (let i = 0; i < 60 && !polled; i++) {
      await wait(1000);
      const conn = await connOf(page);
      if (conn.status === 'polling') polled = conn;
    }
    ok(!!polled && /Updated every 5 s/.test(polled.text) && !polled.dim,
      'a proxy that refuses the upgrade leaves Home polled, saying so, and not dimmed', JSON.stringify(polled || await connOf(page)));
    const readyBefore = (await homeLook(page)).counts.ready;
    const madeWhilePolled = await makeChat(s, 'claude', { title: 'Made while polled' });
    const counted = await awaitHome(page, (h) => h.rows.some((r) => r.id === madeWhilePolled) || h.counts.ready === readyBefore + 1, 15000);
    ok(counted.ok, 'and the poll keeps it current: a chat made meanwhile is counted', 'ready ' + readyBefore + ' > ' + counted.seen.counts.ready);
    // A slow link: each read takes four seconds, and the next poll is only
    // scheduled once it is in - so the page says how often it really updates.
    await page.route(/\/api\/home$/, async (route) => { await wait(4000); await route.continue().catch(() => {}); });
    let slow = null;
    for (let i = 0; i < 30 && !slow; i++) {
      await wait(1000);
      const conn = await connOf(page);
      if (/^Updated every (9|10) s$/.test(conn.text)) slow = conn;
    }
    await page.unroute(/\/api\/home$/);
    ok(!!slow, 'on a link where a poll’s read takes 4 s, Home says it is updated every 9 s, not every 5',
      JSON.stringify(slow || await connOf(page)));

    p.mode = 'dead';
    p.drop();
    const seen = [];
    for (let i = 0; i < 25; i++) {
      await wait(2000);
      const conn = await connOf(page);
      if (seen[seen.length - 1] !== conn.text) seen.push(conn.text);
    }
    ok(seen.some((t) => /^Offline · updated \d+ s ago$/.test(t)) && !seen.some((t) => /^Offline.*just now/.test(t)),
      'offline, Home says how many seconds old its numbers are, never "just now"', seen.join(' > '));
    await page.close();
    ok(unexpected(s.errors).filter((e) => e.startsWith('pageerror')).length === 0, 'no page errors',
      unexpected(s.errors).filter((e) => e.startsWith('pageerror')).join(' | ') || '0');
  } finally {
    if (p) p.close();
    await s.stop();
  }
}

// shownAs makes a page hidden or shown again, as switching apps does.
const shownAs = (page, state) => page.evaluate((hidden) => {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (hidden ? 'hidden' : 'visible') });
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });
  document.dispatchEvent(new Event('visibilitychange'));
}, state === 'hidden');

// connStory keeps every line Home says about its connection from now on.
async function connStory(page) {
  await page.evaluate(() => {
    window.__connStory = [];
    const host = document.querySelector('#home .so-home__conn');
    const keep = () => {
      const said = host.dataset.status + ':' + host.textContent.trim();
      if (window.__connStory[window.__connStory.length - 1] !== said) window.__connStory.push(said);
    };
    new MutationObserver(keep).observe(host, { attributes: true, childList: true, subtree: true, characterData: true });
    keep();
  });
  return () => page.evaluate(() => window.__connStory.slice());
}

// home-quiet: Home when nothing comes, for whatever reason. A notification
// tapped on a stream that died without a word waits for the server rather
// than saying there is no question; a page that can only poll keeps its
// chats' questions counted across a server restart, and answers one; a page
// shown again after a long look elsewhere says Reconnecting…, not Offline; a
// chat being named when the server restarted stops pulsing; and a page that
// never reached the server says it cannot, rather than "Connecting…" for
// ever.
async function homequiet() {
  // The naming held back for as long as the restart it is caught in.
  let holdNaming = false;
  const stub = await openRouterStub({
    text: 'A fine name',
    answer: async () => (holdNaming ? wait(8000).then(() => 'A fine name') : undefined),
  });
  const s = await homeChats();
  let p = null;
  let holder = null;
  try {
    await pointGateway(s, stub);
    const tapped = await makeChat(s, 'claude', { title: 'Asks on a quiet stream' });
    const second = await makeChat(s, 'claude', { title: 'Asks while polled' });
    const named = await makeChat(s, 'claude', { title: 'Being named' });
    await tellChat(s, named, 'Fix the flaky parser test');
    await awaitChat(s, named, (b) => idle(b) && !!b.chat.last_outcome, 20000);
    await awaitChat(s, tapped, (b) => b.chat.process === 'ready', 20000);
    p = await streamProxy(s);

    // A half-open stream, and a tap on "Needs an answer". A second page on
    // a stream that goes quiet with it is left alone, to see how long it
    // goes on calling numbers it no longer hears about live.
    const quiet = await s.context.newPage();
    quiet.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await quiet.goto(p.url + '/', { waitUntil: 'domcontentloaded' });
    const watcher = await s.context.newPage();
    watcher.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    // Every ping the page sends on its event stream, with the time.
    await watcher.addInitScript(() => {
      window.__pings = [];
      const Native = window.WebSocket;
      window.WebSocket = class extends Native {
        send(data) {
          if (String(data).includes('"ping"')) window.__pings.push(Date.now());
          return super.send(data);
        }
      };
    });
    await watcher.goto(p.url + '/', { waitUntil: 'domcontentloaded' });
    const live = await awaitHome(quiet, (h) => h.shown && h.conn === 'live', 20000);
    await awaitHome(watcher, (h) => h.shown && h.conn === 'live', 20000);
    await watcher.evaluate(() => {
      window.__watched = [];
      const home = document.getElementById('home');
      const keep = () => {
        const conn = home.querySelector('.so-home__conn');
        const card = home.querySelector('.so-home__questions');
        const said = (conn ? conn.textContent.trim() : '') + (home.hasAttribute('data-stale') ? ' [dim]' : '')
          + ' Q=' + (card ? card.dataset.value : '?');
        const last = window.__watched[window.__watched.length - 1];
        if (!last || last.said !== said) window.__watched.push({ at: Date.now(), said });
      };
      new MutationObserver(keep).observe(home, { attributes: true, childList: true, subtree: true, characterData: true });
      keep();
    });
    await wait(1000);
    p.mute();
    const mutedAt = Date.now();
    const said = await tellChat(s, tapped, '/askuser ' + JSON.stringify(DB_ASK));
    const asked = said === 200 ? await openAskOf(s, tapped, 20000) : '';
    const unheard = await homeLook(quiet);
    ok(live.ok && !!asked && (unheard.conn === 'live' || unheard.connText === 'Checking…') && unheard.counts.questions === 0,
      'a stream goes quiet with its sockets open; the page has not heard of the question',
      JSON.stringify({ ask: asked, conn: unheard.connText, questions: unheard.counts.questions }));
    const quietSaid = await watchAsk(quiet);
    const quietToasts = await watchToasts(quiet);
    await quiet.evaluate((target) => {
      window.dispatchEvent(new Event('focus'));
      navigator.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { t: 'open', id: target, kind: 'question' } }));
    }, tapped);
    const shown = await awaitAsk(quiet, (d) => !!d && d.question === HOME_QUESTION && d.options.length === 2, 30000);
    const quietWords = [...await quietSaid(), ...await quietToasts()];
    ok(shown.ok && shown.seen.session === 'Asks on a quiet stream' && !quietWords.some((w) => /Nothing is waiting|not here any more|another device/.test(w)),
      'the tap waits for the server - a pong or a new stream - and shows the question, never "nothing is waiting"',
      ((shown.ms / 1000).toFixed(1)) + ' s: ' + (quietWords.join(' | ') || 'nothing said'));
    await quiet.close();
    // The page nobody touched: once nothing has been heard for a while it
    // asks, says "Checking…" with its numbers dimmed while it waits, and is
    // Live again with the question counted - in seconds, not the better part
    // of a minute.
    await awaitHome(watcher, (h) => h.conn === 'live' && h.counts.questions === 1, Math.max(1000, mutedAt + 30000 - Date.now()));
    const watched = await watcher.evaluate(() => window.__watched.slice());
    const pings = await watcher.evaluate(() => window.__pings.slice());
    const sinceMute = watched.filter((w) => w.at >= mutedAt);
    const doubted = sinceMute.find((w) => !/^Live Q=0$/.test(w.said));
    const recounted = sinceMute.find((w) => /^Live Q=1$/.test(w.said));
    // The ping that went unanswered is the one before "Checking…", which
    // comes 2 s after it (QUIET_AFTER), not a watch tick later.
    const unanswered = doubted ? pings.filter((at) => at >= mutedAt - 12000 && at <= doubted.at).pop() : undefined;
    ok(!!doubted && doubted.at - mutedAt < 15000 && !!recounted && recounted.at - mutedAt < 25000
      && unanswered !== undefined && doubted.at - unanswered < 3000,
      'a page on a quiet stream says "Checking…" 2 s after its unanswered ping, within 15 s of the silence, and is live with the question within 25 s',
      'ping +' + (unanswered === undefined ? '?' : ((unanswered - mutedAt) / 1000).toFixed(1)) + 's; '
        + sinceMute.map((w) => ((w.at - mutedAt) / 1000).toFixed(1) + 's ' + w.said).join(' > '));
    await watcher.close();
    p.drop();

    // A page that can only poll, across a server restart: no stream says
    // hello to the new server, and the chats' questions are counted all the
    // same, and can be answered.
    await s.page.goto('about:blank');
    p.mode = '404';
    const polled = await s.context.newPage();
    polled.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await polled.goto(p.url + '/', { waitUntil: 'domcontentloaded' });
    const secondSaid = await tellChat(s, second, '/askuser ' + JSON.stringify(askFor('Which cache should we use?', ['Redis', 'Memcached'])));
    const secondAsk = secondSaid === 200 ? await openAskOf(s, second, 20000) : '';
    const before = await awaitHome(polled, (h) => h.conn === 'polling' && h.counts.questions === 2, 30000);
    ok(!!secondAsk && before.ok, 'a page polled through a proxy that refuses the stream counts both questions',
      JSON.stringify({ conn: before.seen.connText, counts: before.seen.counts }));
    await s.restart();
    // Counted again from a poll of the new server, the turn that finished
    // unread before the restart is still Finished.
    const after = await awaitHome(polled, (h) => h.counts.questions === 2 && h.counts.finished === 1 && h.conn === 'polling', 30000);
    const kept = (await chatOf(s, second)).asks || [];
    ok(after.ok && kept.length === 1 && kept[0].id === secondAsk,
      'after the restart the polls alone keep both questions counted, the chat’s own question still open, and the unread turn Finished',
      JSON.stringify({ conn: after.seen.connText, counts: after.seen.counts, ask: kept.map((a) => a.id) }));
    await awaitHome(polled, (h) => h.rows.some((r) => r.id === second && r.answer), 15000);
    await polled.click('.so-home__card[data-id="' + second + '"] .so-home__answer');
    const answerable = await awaitAsk(polled, (d) => !!d && d.question === 'Which cache should we use?' && d.options.length === 2, 15000);
    if (answerable.ok) {
      await polled.click('dialog.so-askdlg .so-q-option[data-option="redis"]');
      await polled.click('dialog.so-askdlg [data-action="send"]');
    }
    const got = await returned(s, second, 20000);
    ok(answerable.ok && !!got && got.answers.db.selected.join() === 'redis',
      'and its dialog answers it from the polled page', JSON.stringify(got && got.answers.db.selected));
    await polled.close();
    p.close();
    p = null;

    // A chat being named when the server restarts, and a page that is then
    // hidden past the moment it closes its stream.
    await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await awaitHome(s.page, (h) => h.shown && h.conn === 'live', 20000);
    holdNaming = true;
    const pulsing = () => s.page.evaluate((id) => {
      const row = document.querySelector('#sessionList [data-id="' + id + '"]');
      return !!row && !!row.querySelector('.so-row__label[data-titling]');
    }, named);
    const namedAt = Date.now();
    s.context.request.post(s.url + '/api/sessions/' + named + '/title', { data: {} }).catch(() => {});
    let pulsed = false;
    for (let i = 0; i < 30 && !pulsed; i++) { pulsed = await pulsing(); if (!pulsed) await wait(100); }
    await s.restart();
    holdNaming = false;
    await awaitHome(s.page, (h) => h.conn === 'live', 30000);
    await shownAs(s.page, 'hidden');
    await wait(33000);
    const cdp = await s.context.newCDPSession(s.page);
    await cdp.send('Network.enable');
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 800, downloadThroughput: -1, uploadThroughput: -1 });
    const story = await connStory(s.page);
    await shownAs(s.page, 'visible');
    const relive = await awaitHome(s.page, (h) => h.conn === 'live', 20000);
    const back = await story();
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    ok(relive.ok && !back.some((c) => /Offline/.test(c)),
      'a page shown again after half a minute away says Reconnecting…, never Offline, while the network is up', back.join(' > '));
    await wait(Math.max(0, namedAt + 48000 - Date.now()));
    const still = await pulsing();
    ok(pulsed && !still, 'a chat being named when the server restarted stops pulsing once its naming is past due',
      JSON.stringify({ pulsed, after: ((Date.now() - namedAt) / 1000).toFixed(0) + ' s', still }));

    // The server goes, the port answers nothing, and the page is started
    // again from what the worker cached.
    const controlled = await s.page.waitForFunction(() => !!(navigator.serviceWorker && navigator.serviceWorker.controller),
      null, { timeout: 15000 }).then(() => true, () => false);
    s.server.kill('SIGTERM');
    const gone = await Promise.race([s.exited, wait(9000).then(() => 'timeout')]);
    if (gone === 'timeout') { s.server.kill('SIGKILL'); await s.exited; }
    holder = net.createServer((c) => c.destroy());
    await new Promise((resolve) => holder.listen(s.port, '127.0.0.1', resolve));
    await s.page.reload({ waitUntil: 'domcontentloaded' }).catch(() => {});
    const lines = [];
    const startedAt = Date.now();
    while (Date.now() - startedAt < 16000) {
      const conn = await connOf(s.page).catch(() => ({ text: 'no home' }));
      const at = ((Date.now() - startedAt) / 1000).toFixed(0);
      if (!lines.length || lines[lines.length - 1].text !== conn.text) lines.push({ at: Number(at), text: conn.text });
      await wait(500);
    }
    const lastWord = lines[lines.length - 1];
    ok(controlled && lines.some((l) => /Offline|reach/i.test(l.text)) && !/Connecting/.test(lastWord.text)
      && !lines.some((l) => l.at >= 8 && /Connecting/.test(l.text)),
      'a page that never reached the server says it cannot, not "Connecting…" for ever',
      JSON.stringify({ controlled, lines }));
    await new Promise((resolve) => holder.close(resolve));
    holder = null;
    const again = spawnServer({ data: s.data, port: s.port });
    s.server = again.server; s.log = again.log; s.exited = again.exited;
    await waitForHealth(s.url, 25000, again);
    const recovered = await awaitHome(s.page, (h) => h.conn === 'live', 45000);
    ok(recovered.ok, 'and it is Live once the server is back', recovered.seen && recovered.seen.connText);
    ok(unexpected(s.errors, RESTART_NOISE).filter((e) => e.startsWith('pageerror')).length === 0, 'no page errors',
      unexpected(s.errors, RESTART_NOISE).filter((e) => e.startsWith('pageerror')).join(' | ') || '0');
  } finally {
    if (holder) await new Promise((resolve) => holder.close(resolve));
    if (p) p.close();
    await s.stop();
    await stub.close();
  }
}

// sheetSaid keeps every line the dialog shows - its progress line, its note
// and its last word - in order, and the moment it closed.
async function sheetSaid(page) {
  await page.evaluate(() => {
    window.__sheetSaid = [];
    const look = () => {
      const dialog = document.querySelector('dialog.so-question');
      if (!dialog || !dialog.open) return ['closed'];
      const line = dialog.querySelector('.so-q-status:not([hidden])');
      const note = dialog.querySelector('.so-q-note');
      const last = dialog.querySelector('.so-q-finished-title');
      return [line ? line.textContent.trim() : '', note ? 'note: ' + note.textContent.trim() : '',
        last ? last.textContent.trim() : ''].filter(Boolean);
    };
    const keep = () => {
      for (const one of look()) {
        if (window.__sheetSaid[window.__sheetSaid.length - 1] !== one) window.__sheetSaid.push(one);
      }
    };
    new MutationObserver(keep).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true });
    keep();
  });
  return () => page.evaluate(() => window.__sheetSaid.slice());
}

// answerVia is an answer given over the API, as a dialog gives it.
async function answerVia(s, id, rev, answers, text = '') {
  const res = await s.context.request.post(s.url + '/api/sessions/' + encodeURIComponent(id) + '/answer',
    { data: { digest_rev: Number(rev) || 0, answers, text } });
  let data = null;
  try { data = await res.json(); } catch { /* no body */ }
  return { status: res.status(), data };
}

// panePid is the process the pane runs: the program itself.
function panePid(s, id) {
  return execFileSync('tmux', ['-S', tmuxSock(s.data), 'list-panes', '-t', 'soc_' + id, '-F', '#{pane_pid}'])
    .toString().trim();
}

// askInProse ends a turn on a question in prose, the way a real one ends: the
// program works, and its answer closes on a question with the prompt open.
async function askInProse(s, id) {
  tmuxType(s, id, '/busy 8000');
  await awaitActivity(s, id, (a) => a.state === 'busy', 10000);
  tmuxType(s, id, '/textq');
  return awaitDigest(s, id, (d) => d.phase === 'ready' && !!d.question && d.question.source === 'text', 25000);
}

// home-kinds: every kind of question the dialog answers, end to end - a
// multi-select, a menu of two tabs with its review, a plan sent back and then
// approved, and a question asked in prose whose "Finished" notification opens
// the dialog.
async function homekinds() {
  const { s, stub } = await homeServer();
  try {
    const several = await makeAgent(s, 'Several at once');
    const tabs = await makeAgent(s, 'Two tabs');
    const planner = await makeAgent(s, 'Has a plan');
    const prose = await makeAgent(s, 'Asks in prose');
    await awaitHome(s.page, (h) => h.shown, 10000);

    // A multi-select: taps toggle, and Send gives all that are on.
    tmuxType(s, several, '/question multi');
    const multi = await awaitDigest(s, several, (d) => d.phase === 'ready' && !!d.question, 20000);
    ok(!!multi && multi.question.source === 'menu' && multi.question.items.length === 1 && multi.question.items[0].multi,
      'a multi-select question is read as one menu - never, in the moment before its status says waiting, as a question in prose', JSON.stringify(multi && [multi.question.source, multi.state, ...multi.question.items.map((i) => i.text + '/' + i.multi)]));
    await awaitHome(s.page, (h) => h.rows.some((r) => r.id === several && r.answer), 10000);
    await s.page.click('.so-home__card[data-id="' + several + '"] .so-home__answer');
    await awaitSheet(s.page, (d) => !!d && d.options.length === 3, 10000);
    for (const label of ['Dark mode', 'Export', 'Search', 'Search']) await s.page.click('.so-q-option[data-label="' + label + '"]');
    const ticked = await sheetLook(s.page);
    ok(ticked.options.filter((o) => o.pressed === 'true').map((o) => o.label).join(',') === 'Dark mode,Export' && !ticked.send.disabled,
      'each tap toggles an option, and a second tap takes it off again', JSON.stringify(ticked.options.map((o) => o.label + '=' + o.pressed)));
    await s.page.click('.so-q-send');
    const first = await awaitAnswerLogged(s, 1, 40000);
    ok(first[0] && first[0].answers[0].choices.join(',') === 'Dark mode,Export',
      'the fake got both, ticked and submitted through its menu', JSON.stringify(first[0] ? first[0].answers
        : { run: await answerRunOf(s, several), goal: operatorCalls(stub).map((c) => String(c.json.messages[0].content).split('Goal:').pop()).slice(-1) }));
    ok(await sheetShut(s.page, 20000), 'and the dialog closes', 'shut');

    // Two tabs: the first is answered by a second tap, the second by Send,
    // and the operator submits the review.
    tmuxType(s, tabs, '/question multiq');
    const multiq = await awaitDigest(s, tabs, (d) => d.phase === 'ready' && !!d.question, 20000);
    ok(!!multiq && multiq.question.items.length === 2, 'a menu of two tabs is read as two questions',
      JSON.stringify(multiq && multiq.question.items.map((i) => i.header)));
    await awaitHome(s.page, (h) => h.rows.some((r) => r.id === tabs && r.answer), 10000);
    await s.page.click('.so-home__card[data-id="' + tabs + '"] .so-home__answer');
    const one = await awaitSheet(s.page, (d) => !!d && d.question === COLOR_QUESTION, 10000);
    ok(one.ok && /Question 1 of 2/.test(one.seen.step), 'the dialog says "Question 1 of 2"', one.seen && one.seen.step);
    await s.page.click('.so-q-option[data-label="Blue"]');
    await s.page.click('.so-q-option[data-label="Blue"]');
    const two = await awaitSheet(s.page, (d) => !!d && d.question === FEATURES_QUESTION, 5000);
    ok(two.ok && /Question 2 of 2/.test(two.seen.step), 'a second tap goes on to "Question 2 of 2"', two.seen && two.seen.step);
    await s.page.click('.so-q-option[data-label="Search"]');
    await s.page.click('.so-q-send');
    const second = await awaitAnswerLogged(s, 2, 40000);
    const both = (second[1] || {}).answers || [];
    ok(both.length === 2 && both[0].choices.join(',') === 'Blue' && both[1].choices.join(',') === 'Search' && !second[1].declined,
      'the fake got Blue and Search, and its review was submitted', JSON.stringify(both));
    ok(await sheetShut(s.page, 20000), 'and the dialog closes', 'shut');

    // A plan sent back with feedback: the feedback goes in, the plan comes
    // back revised, and nothing approves it but the person.
    tmuxType(s, planner, '/question plan');
    const plan = await awaitDigest(s, planner, (d) => d.phase === 'ready' && !!d.question, 20000);
    ok(!!plan && plan.question.kind === 'plan', 'a plan approval is read as one', JSON.stringify(plan && plan.question.kind));
    await awaitHome(s.page, (h) => h.rows.some((r) => r.id === planner && r.answer), 10000);
    await s.page.click('.so-home__card[data-id="' + planner + '"] .so-home__answer');
    await awaitSheet(s.page, (d) => !!d && d.question === PLAN_QUESTION && d.own, 10000);
    // Read aloud says the plan too, after the question, as the dialog
    // shows it.
    const spoken = await stubSpeech(s.context);
    await s.page.click('.so-q-read');
    for (let i = 0; i < 50 && !spoken.length; i += 1) await wait(100);
    const shownDetail = await s.page.locator('dialog.so-question .so-q-detail').first().textContent().catch(() => '');
    ok(shownDetail.includes(PLAN_DETAIL) && !!spoken[0] && spoken[0].indexOf(PLAN_DETAIL) > spoken[0].indexOf(PLAN_QUESTION)
      && spoken[0].includes(PLAN_QUESTION),
      'Read aloud on a plan approval says the plan the dialog shows, after the question', JSON.stringify(spoken[0] || ''));
    await s.context.unroute('**/api/voice/speak');
    await s.page.fill('.so-q-own-field', 'Make it smaller');
    await s.page.click('.so-q-send');
    const sentBack = await awaitAnswerLogged(s, 3, 40000);
    const feedback = sentBack[2] || {};
    ok(feedback.kind === 'plan' && feedback.approved === false && feedback.answers && feedback.answers[0].text === 'Make it smaller',
      'the feedback is typed on the plan’s own row and sends it back', JSON.stringify(feedback.answers));
    const fedBack = await awaitRunEnd(s, planner, 30000);
    ok(!!fedBack && fedBack.phase === 'done' && !fedBack.error, 'that run ends as answered, not as an error',
      JSON.stringify(fedBack && { phase: fedBack.phase, error: fedBack.error }));
    const revised = await awaitDigest(s, planner, (d) => d.phase === 'ready' && !!d.question && d.rev > plan.rev
      && d.question.kind === 'plan', 30000);
    ok(!!revised, 'the revised plan is a question again', JSON.stringify(revised && { rev: revised.rev, kind: revised.question.kind }));
    await wait(3000);
    ok(answersLogged(s).length === 3, 'and the operator did not approve the revised plan on its own',
      JSON.stringify(answersLogged(s).slice(2).map((l) => ({ approved: l.approved, revision: l.revision }))));
    const planShown = (d) => !!d && d.question === PLAN_QUESTION
      && d.options.some((o) => o.label === 'Yes, and use auto mode' && !o.disabled);
    const settledSheet = await awaitSheet(s.page, (d) => !d || planShown(d), 8000);
    if (!settledSheet.seen) {
      await awaitHome(s.page, (h) => h.rows.some((r) => r.id === planner && r.answer), 10000);
      await s.page.click('.so-home__card[data-id="' + planner + '"] .so-home__answer');
    }
    const again = await awaitSheet(s.page, (d) => !!d && d.question === PLAN_QUESTION
      && d.options.some((o) => o.label === 'Yes, and use auto mode' && !o.disabled), 15000);
    ok(again.ok, 'the dialog shows the revised plan', JSON.stringify(again.seen && { q: again.seen.question, status: again.seen.status }));
    await s.page.click('.so-q-option[data-label="Yes, and use auto mode"]');
    await s.page.click('.so-q-send');
    const approved = await awaitAnswerLogged(s, 4, 40000);
    ok(approved[3] && approved[3].approved === true && approved[3].revision === 1 && approved[3].answers[0].choices[0] === 'Yes, and use auto mode',
      'and the person’s Yes approves it', JSON.stringify(approved[3] && { approved: approved[3].approved, revision: approved[3].revision }));
    await sheetShut(s.page, 20000);

    // A question asked in prose: Home counts it, its notification says
    // "Finished", and a tap on it opens the question all the same.
    const read = await askInProse(s, prose);
    ok(!!read && read.question.items[0].text === TEXT_QUESTION, 'a turn that ends on a question in prose is read as a question',
      JSON.stringify(read && read.question));
    const counted = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.rows.some((r) => r.id === prose && r.answer), 10000);
    ok(counted.ok, 'Home counts it under Questions, with an Answer button', JSON.stringify(counted.seen.counts));
    await s.page.evaluate((target) => {
      navigator.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { t: 'open', id: target, kind: 'finished' } }));
    }, prose);
    const tapped = await awaitSheet(s.page, (d) => !!d && d.question === TEXT_QUESTION && d.own, 10000);
    const stage = await homeLook(s.page);
    ok(tapped.ok && stage.title !== 'Asks in prose', 'a tap on its "Finished" notification opens the question, not the terminal',
      JSON.stringify({ q: tapped.seen && tapped.seen.question, title: stage.title }));
    await s.page.fill('.so-q-own-field', 'Green, please');
    await s.page.click('.so-q-send');
    const typed = await awaitAnswerLogged(s, 5, 40000);
    ok(typed[4] && typed[4].kind === 'text' && typed[4].answers[0].text === 'Green, please',
      'the answer is typed on its prompt', JSON.stringify(typed[4] && typed[4].answers));
    ok(await sheetShut(s.page, 20000), 'and the dialog closes', 'shut');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// home-unread: a question asked in prose that could not be read when the turn
// ended is read once it can be, on the Home that is open - no reload, and no
// next turn. A gateway that refused once is asked again, and a key saved on
// another device brings the reads it was missing.
async function homeunread() {
  const { s, stub, ctl } = await homeServer();
  let other = null;
  try {
    const failed = await makeAgent(s, 'Read failed once');
    const keyless = await makeAgent(s, 'Read once a key is in');
    const digestsOf = (title) => stub.digests.filter((c) => {
      const messages = (c.json && c.json.messages) || [];
      return messages.length > 1 && calledAs(String(messages[messages.length - 1].content)) === title;
    }).length;
    let refuse = true;
    ctl.digest = async (prompt) => (refuse && calledAs(prompt) === 'Read failed once' ? { status: 500 } : undefined);
    const open = await awaitHome(s.page, (h) => h.shown && h.conn === 'live', 15000);

    // The gateway refuses every read of a turn that ends on a question in
    // prose, and then comes back.
    tmuxType(s, failed, '/busy 3000');
    await awaitActivity(s, failed, (a) => a.state === 'busy', 10000);
    tmuxType(s, failed, '/textq');
    await awaitActivity(s, failed, (a) => a.state === 'idle', 15000);
    const broken = await awaitDigest(s, failed, (d) => d.phase === 'error' && d.state === 'idle', 30000);
    const unasked = await homeLook(s.page);
    ok(open.ok && !!broken && unasked.counts.questions === 0,
      'the read of the turn’s end fails, and Home has no question to count',
      JSON.stringify({ digest: broken && { phase: broken.phase, state: broken.state, error: broken.error }, questions: unasked.counts.questions }));
    refuse = false;
    const callsAt = digestsOf('Read failed once');
    const recovered = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.rows.some((r) => r.id === failed && r.answer), 40000);
    const read = await awaitDigest(s, failed, (d) => d.phase === 'ready' && !!d.question && d.question.source === 'text', 5000);
    ok(recovered.ok && !!read && digestsOf('Read failed once') > callsAt,
      'once the gateway answers again, the idle session is read again and its question counted, with no reload',
      took(recovered) + ', ' + (digestsOf('Read failed once') - callsAt) + ' new reads');

    // No key: the turn's end cannot be read at all. A key saved on another
    // device reaches the Home that is open.
    await answerVia(s, failed, read && read.rev, [{ item: 0, choices: [], text: 'Blue' }]);
    await awaitRunEnd(s, failed, 30000);
    const cleared = await s.context.request.put(s.url + '/api/settings', { data: { settings: { openrouter: { api_key: '' } } } });
    tmuxType(s, keyless, '/busy 3000');
    await awaitActivity(s, keyless, (a) => a.state === 'busy', 10000);
    tmuxType(s, keyless, '/textq');
    await awaitActivity(s, keyless, (a) => a.state === 'idle', 15000);
    const noKey = await awaitDigest(s, keyless, (d) => d.phase === 'error' && /no OpenRouter API key/.test(d.error || ''), 20000);
    await wait(2000);
    const before = await homeLook(s.page);
    ok(cleared.ok() && !!noKey && before.counts.questions === 0 && before.conn === 'live',
      'with no key the question in prose is not read, and Home, live, counts none',
      JSON.stringify({ digest: noKey && noKey.error, state: noKey && noKey.state, questions: before.counts.questions }));
    other = await deviceOf(s);
    const keyAt = Date.now();
    const saved = await other.context.request.put(s.url + '/api/settings', {
      data: { settings: { openrouter: { api_key: 'e2e-key', base_url: stub.url } } },
    });
    const counted = await awaitHome(s.page, (h) => h.counts.questions === 1 && h.rows.some((r) => r.id === keyless && r.answer), 20000);
    ok(saved.ok() && counted.ok,
      'a key saved on another device gets it read, and Home counts it without a reload',
      ((Date.now() - keyAt) / 1000).toFixed(1) + ' s, ' + JSON.stringify(counted.seen.counts));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    if (other) await other.context.close();
    await s.stop();
    await stub.close();
  }
}

// home-honest: what Home and the dialog say is so. An age is measured on the
// server's clock, not a device's that is minutes off; the dialog says when it
// is offline, and that its question may have been answered; and the poll a
// page falls back on comes compressed.
async function homehonest() {
  const { s, stub } = await homeServer();
  let skewed = null;
  let away = null;
  try {
    const asks = await makeAgent(s, 'Asks on a fast clock');
    const offline = await makeAgent(s, 'Asks while offline');

    // The poll and the list, compressed for a client that takes gzip.
    for (const path of ['/api/home', '/api/sessions']) {
      const res = await s.context.request.get(s.url + path, { headers: { 'Accept-Encoding': 'gzip' } });
      ok(res.ok() && res.headers()['content-encoding'] === 'gzip' && !!(await res.json()),
        path + ' is sent gzipped to a client that accepts it', JSON.stringify({ status: res.status(), encoding: res.headers()['content-encoding'] }));
    }

    // A device whose clock is ten minutes fast.
    skewed = await deviceOf(s);
    await skewed.page.addInitScript((off) => {
      const Real = Date;
      const now = () => Real.now() + off;
      class Skewed extends Real {
        constructor(...args) { if (args.length) super(...args); else super(now()); }
        static now() { return now(); }
      }
      globalThis.Date = Skewed;
    }, 10 * 60 * 1000);
    await skewed.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await awaitHome(skewed.page, (h) => h.shown && h.conn === 'live', 20000);
    await askQuestion(s, asks);
    await awaitHome(skewed.page, (h) => h.rows.some((r) => r.id === asks && r.answer), 15000);
    const meta = await skewed.page.$eval('.so-home__card[data-id="' + asks + '"] .so-home__card-status', (n) => n.textContent);
    ok(/just now/.test(meta) && !/min ago/.test(meta), 'a question asked a moment ago says so on a device ten minutes fast', meta);
    await skewed.context.close();
    skewed = null;

    // The dialog, offline.
    away = await deviceOf(s);
    await away.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await awaitHome(away.page, (h) => h.shown && h.conn === 'live', 20000);
    await askQuestion(s, offline);
    await awaitHome(away.page, (h) => h.rows.some((r) => r.id === offline && r.answer), 15000);
    await away.page.click('.so-home__card[data-id="' + offline + '"] .so-home__answer');
    await awaitSheet(away.page, (d) => !!d && d.session === 'Asks while offline' && d.options.length === 2, 10000);
    const connLine = () => away.page.evaluate(() => {
      const line = document.querySelector('dialog.so-question .so-q-conn');
      const shell = document.querySelector('dialog.so-question .so-q-shell');
      return {
        shown: !!line && !line.hidden,
        text: line ? line.textContent.trim() : '',
        dim: !!shell && shell.hasAttribute('data-stale'),
      };
    });
    const liveLine = await connLine();
    await away.context.setOffline(true);
    const offAt = Date.now();
    let said = null;
    while (Date.now() - offAt < 20000) {
      said = await connLine();
      if (said.shown && said.dim && /Offline|Reconnecting|Checking/.test(said.text) && /may already have been answered/.test(said.text)) break;
      await wait(300);
    }
    ok(!liveLine.shown && !liveLine.dim, 'live, the dialog says nothing about its connection', JSON.stringify(liveLine));
    ok(said.shown && said.dim && /may already have been answered/.test(said.text),
      'offline, the dialog says so, dims the question and says it may already have been answered',
      ((Date.now() - offAt) / 1000).toFixed(1) + ' s: ' + JSON.stringify(said));
    await away.context.setOffline(false);
    let back = null;
    const onAt = Date.now();
    while (Date.now() - onAt < 30000) {
      back = await connLine();
      if (!back.shown && !back.dim) break;
      await wait(300);
    }
    ok(!back.shown && !back.dim, 'and stops saying it once it is live again', ((Date.now() - onAt) / 1000).toFixed(1) + ' s');
    ok(unexpected(s.errors, OFFLINE_NOISE).length === 0, 'no unexpected console errors', unexpected(s.errors, OFFLINE_NOISE).join(' | ') || '0');
  } finally {
    if (skewed) await skewed.context.close();
    if (away) await away.context.close();
    await s.stop();
    await stub.close();
  }
}

// home-language: a question read in the person's language - German here -
// while the screen asks it in English. Two tabs share Yes and No, and only the
// question above each menu tells which tab a Yes belongs to: the digest keeps
// the screen's own words for that, and the answer goes in tab by tab.
async function homelanguage() {
  const { s, stub, ctl } = await homeServer();
  try {
    const saved = await s.context.request.put(s.url + '/api/settings', { data: { settings: { voice: { language: 'de' } } } });
    const id = await makeAgent(s, 'Fragt zweimal');
    const yesNo = (yes, no) => [{ label: 'Yes', description: yes, recommended: false }, { label: 'No', description: no, recommended: false }];
    ctl.digest = async (prompt) => {
      if (calledAs(prompt) !== 'Fragt zweimal' || !menuOnScreen(screenOf(prompt))) return undefined;
      // A model that writes the question in German, as it is told to, and
      // leaves the screen's words out.
      return JSON.stringify({
        summary: 'Fragt nach Tests und Push', context: 'Die Sitzung ist fast fertig.',
        question: {
          kind: 'choice', detail: '',
          items: [
            { header: 'Tests', text: 'Die Tests ausführen?', multi: false, free_text: true, options: yesNo('Run the whole suite', 'Skip them') },
            { header: 'Push', text: 'Auf main pushen?', multi: false, free_text: true, options: yesNo('Push the branch', 'Keep it local') },
          ],
        },
      });
    };
    tmuxType(s, id, '/question yesno');
    await awaitActivity(s, id, (a) => a.state === 'waiting', 15000);
    const read = await awaitDigest(s, id, (d) => d.phase === 'ready' && !!d.question && d.question.items.length === 2, 20000);
    const asked = stub.digests.map((c) => String(((c.json && c.json.messages) || [{}])[0].content || ''));
    ok(saved.ok() && asked.some((p) => /German/.test(p)), 'the digest is asked to write in German', asked.length + ' digests');
    ok(!!read && read.question.items.map((i) => i.text).join(' / ') === 'Die Tests ausführen? / Auf main pushen?'
      && read.question.items.map((i) => i.screen_text).join(' / ') === TESTS_QUESTION + ' / ' + PUSH_QUESTION,
      'the question is in German, and each tab keeps the words the screen asks it in',
      JSON.stringify(read && read.question.items.map((i) => ({ text: i.text, screen: i.screen_text }))));
    const posted = await answerVia(s, id, read && read.rev,
      [{ item: 0, choices: ['Yes'], text: '' }, { item: 1, choices: ['No'], text: '' }]);
    const run = await awaitRunEnd(s, id, 60000);
    const logged = await awaitAnswerLogged(s, 1);
    ok(posted.status === 202 && !!run && run.phase === 'done' && !run.error,
      'Yes on the first tab and No on the second are delivered', JSON.stringify(run && { phase: run.phase, error: run.error, summary: run.summary }));
    ok(logged.length === 1 && JSON.stringify(logged[0].answers.map((a) => [a.question, a.choices.join(',')]))
      === JSON.stringify([[TESTS_QUESTION, 'Yes'], [PUSH_QUESTION, 'No']]),
      'each tab got its own answer', JSON.stringify(logged.map((e) => e.answers)));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// home-restart: a server restart under an open dialog. Every question stays
// counted and in the walk, nothing is said to be answered elsewhere, a choice
// made before it still goes in after it, a question asked in prose comes back,
// and every question keeps how long it has been waiting.
async function homerestart() {
  const { s, stub, ctl } = await homeServer();
  try {
    const db = await makeAgent(s, 'Pick a database');
    const cache = await makeAgent(s, 'Pick a cache');
    const prose = await makeAgent(s, 'Asks in prose');
    await awaitHome(s.page, (h) => h.shown, 10000);
    await askQuestion(s, db);
    await wait(1200);
    await askQuestion(s, cache);
    const read = await askInProse(s, prose);
    const all = await awaitHome(s.page, (h) => h.counts.questions === 3 && h.conn === 'live', 15000);
    ok(all.ok && !!read, 'two menus and a question in prose are counted', JSON.stringify(all.seen.counts));
    const sinceOf = async () => Object.fromEntries(await Promise.all([db, cache, prose]
      .map(async (id) => [id, ((await sessionRow(s, id)).activity || {}).since || 0])));
    const before = await sinceOf();
    await wait(2500);

    await s.page.click('.so-home__questions');
    const opened = await awaitSheet(s.page, (d) => !!d && d.options.length === 2 && d.counter === '1 of 3', 10000);
    ok(opened.ok && opened.seen.session === 'Pick a database', 'the dialog walks all three, oldest first', JSON.stringify(opened.seen && { c: opened.seen.counter, s: opened.seen.session }));
    await s.page.click('.so-q-option[data-label="SQLite"]');
    await s.page.evaluate(() => {
      window.__line = [];
      const snap = () => {
        const home = document.getElementById('home');
        const count = home.querySelector('.so-home__questions');
        const conn = home.querySelector('.so-home__conn');
        const dialog = document.querySelector('dialog.so-question');
        const status = dialog && dialog.querySelector('.so-q-status:not([hidden])');
        const note = dialog && dialog.querySelector('.so-q-note');
        const line = JSON.stringify({
          q: count ? Number(count.dataset.value) : -1,
          conn: conn ? conn.dataset.status : '',
          open: !!dialog && dialog.open,
          counter: dialog ? ((dialog.querySelector('.so-q-counter') || {}).textContent || '') : '',
          said: [status ? status.textContent.trim() : '', note ? note.textContent.trim() : '',
            ((dialog && dialog.querySelector('.so-q-finished-title')) || {}).textContent || ''].filter(Boolean).join(' / '),
        });
        if (window.__line[window.__line.length - 1] !== line) window.__line.push(line);
      };
      new MutationObserver(snap).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true });
      window.__lineTimer = setInterval(snap, 25);
      snap();
    });

    await s.restart();
    const back = await awaitHome(s.page, (h) => h.conn === 'live' && h.counts.questions === 3, 30000);
    await awaitDigest(s, db, (d) => d.phase === 'ready' && !!d.question, 20000);
    await awaitDigest(s, prose, (d) => d.phase === 'ready' && !!d.question && d.question.source === 'text', 20000);
    await wait(5000);
    const lines = (await s.page.evaluate(() => { clearInterval(window.__lineTimer); return window.__line.slice(); })).map((l) => JSON.parse(l));
    const show = (l) => 'Q=' + l.q + ' ' + l.conn + ' ' + (l.open ? l.counter + ' ' + l.said : 'closed');
    ok(back.ok, 'after the restart Home is live and counts all three again', JSON.stringify(back.seen.counts));
    const dropped = lines.filter((l) => l.conn === 'live' && l.q < 3);
    ok(!dropped.length, 'a live Home counts every question all through the restart, the one asked in prose too, while the new server reads them again',
      (dropped.length ? dropped.map(show) : lines.map(show)).slice(0, 12).join(' > '));
    const told = lines.filter((l) => !l.open || /answered elsewhere|No question is waiting/.test(l.said));
    ok(!told.length && lines.every((l) => !l.counter || /of 3$/.test(l.counter)),
      'the dialog stays open on the walk, and keeps all three in it, and never says the waiting questions went', (told.length ? told : lines).map(show).slice(0, 12).join(' > '));
    const now = await awaitHome(s.page, (h) => h.counts.questions === 3, 20000);
    ok(now.ok, 'the question asked in prose is read again and counted', JSON.stringify(now.seen.counts));
    const after = await sinceOf();
    ok([db, cache, prose].every((id) => before[id] > 0 && after[id] === before[id]),
      'every question keeps the time it started waiting', JSON.stringify([db, cache, prose].map((id) => [before[id], after[id]])));

    const look = await awaitSheet(s.page, (d) => !!d && d.session === 'Pick a database' && d.send && !d.send.disabled, 10000);
    ok(look.ok && look.seen.options.find((o) => o.label === 'SQLite').pressed === 'true',
      'the choice made before the restart is still chosen', JSON.stringify(look.seen && look.seen.options.map((o) => o.label + '=' + o.pressed)));
    await s.page.click('.so-q-send');
    const logged = await awaitAnswerLogged(s, 1, 40000);
    ok(logged[0] && logged[0].answers[0].choices.join(',') === 'SQLite', 'and Send gives it', JSON.stringify(logged[0] && logged[0].answers));
    await s.page.click('.so-q-close').catch(() => {});
    await sheetShut(s.page, 5000);

    // A question in prose answered at the machine while the server is down,
    // and the new server slow to read it again: the question the page held
    // is not offered as live meanwhile - no Answer button, "Checking whether
    // it still asks…" - and once read it is no longer counted.
    const downAnswered = await makeAgent(s, 'Answered while down');
    // Every session listed, so that the row stays in the list once it no
    // longer asks.
    await s.page.click('.so-home__seg[data-filter="all"]');
    await askInProse(s, downAnswered);
    const offered = await awaitHome(s.page, (h) => h.rows.some((r) => r.id === downAnswered && r.answer && /^Asks/.test(r.summary)), 15000);
    let slowReads = false;
    ctl.digest = async (prompt) => {
      if (slowReads && calledAs(prompt) === 'Answered while down') await wait(4000);
      return undefined;
    };
    slowReads = true;
    await s.restart({}, async () => { tmuxType(s, downAnswered, 'Blue'); await wait(2500); });
    const story = [];
    let settled = null;
    for (const until = Date.now() + 40000; Date.now() < until;) {
      const h = await homeLook(s.page);
      const row = h.rows.find((r) => r.id === downAnswered);
      const meta = await s.page.$eval('.so-home__card[data-id="' + downAnswered + '"] .so-home__card-status', (n) => n.textContent).catch(() => '');
      story.push({ conn: h.conn, answer: !!row && row.answer, summary: (row ? row.summary : '') + ' · ' + meta });
      if (h.conn === 'live' && row && !row.answer && !/Asks|Checking whether/.test(row.summary + meta)) { settled = h; break; }
      await wait(150);
    }
    const liveOffered = story.filter((l) => l.conn === 'live' && l.answer);
    const compact = story.map((l) => l.conn + (l.answer ? '|A ' : ' ') + l.summary)
      .filter((l, i, all) => all[i - 1] !== l);
    ok(offered.ok && !!settled && !liveOffered.length && story.some((l) => l.conn === 'live' && /Checking whether it still asks/.test(l.summary)),
      'a question in prose answered while the server was down is shown as being checked, with no Answer, until it is read - and then no longer asks',
      (offered.ok ? '' : 'never offered before the restart: ' + JSON.stringify((offered.seen.rows || []).find((r) => r.id === downAnswered)) + ' ') + compact.slice(0, 10).join(' > '));
    ok(unexpected(s.errors, RESTART_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, RESTART_NOISE).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// home-devices: two devices on one question. The dialog on the second follows
// the answer the first gave, locks its options and says whose answer it is,
// and a Send that crossed it is told the same - and neither is counted as an
// answer of the second device's.
async function homedevices() {
  const { s, stub, ctl } = await homeServer();
  let pause = null;
  let other = null;
  try {
    const watched = await makeAgent(s, 'Watched');
    const crossed = await makeAgent(s, 'Crossed');
    ctl.operator = async () => { if (pause) await pause.promise; return undefined; };
    const deaf = { on: false };
    other = await deviceOf(s, { viewport: { width: 1280, height: 800 } });
    await other.page.routeWebSocket(/\/api\/events/, (ws) => {
      const server = ws.connectToServer();
      server.onMessage((m) => { if (deaf.on && String(m).includes('"t":"answer"')) return; ws.send(m); });
      ws.onMessage((m) => server.send(m));
    });
    await other.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await awaitHome(other.page, (h) => h.shown && h.conn === 'live', 15000);
    await awaitHome(s.page, (h) => h.shown, 10000);

    const openOn = async (page, id) => {
      await awaitHome(page, (h) => h.rows.some((r) => r.id === id && r.answer), 15000);
      await page.click('.so-home__card[data-id="' + id + '"] .so-home__answer');
      return awaitSheet(page, (d) => !!d && d.options.length === 2 && d.options.every((o) => !o.disabled), 10000);
    };
    await askQuestion(s, watched);
    await openOn(s.page, watched);
    await openOn(other.page, watched);
    const saidA = await sheetSaid(s.page);
    const saidB = await sheetSaid(other.page);
    pause = gate();
    await s.page.click('.so-q-option[data-label="Postgres"]');
    await s.page.click('.so-q-send');
    const following = await awaitSheet(other.page, (d) => !!d && !!d.status && /Another device is answering/.test(d.status.text), 10000);
    const b = following.seen || {};
    ok(following.ok && /Postgres/.test(b.status.text) && b.options.every((o) => o.disabled) && b.send && b.send.hidden,
      'the other device’s dialog follows the answer: options locked, no Send, and whose answer it is',
      JSON.stringify(b && { status: b.status, locked: b.options && b.options.map((o) => o.disabled), send: b.send }));
    pause.release();
    pause = null;
    const logged = await awaitAnswerLogged(s, 1);
    await sheetShut(s.page, 15000);
    await sheetShut(other.page, 15000);
    const a = await saidA();
    const bs = await saidB();
    ok(logged.length === 1 && logged[0].answers[0].choices.join(',') === 'Postgres', 'Postgres went in, once', JSON.stringify(logged.map((l) => l.answers)));
    ok(a.includes('All questions answered'), 'the device that answered says all its questions were answered', a.join(' | '));
    ok(bs.some((w) => /Answered on another device/.test(w)) && !bs.includes('All questions answered'),
      'the other says it was answered on another device, never that it answered it', bs.join(' | '));

    // A Send from a device that did not hear of the other's answer.
    await awaitActivity(s, watched, (act) => act.state !== 'waiting', 10000);
    await askQuestion(s, crossed);
    pause = gate();
    await openOn(s.page, crossed);
    await openOn(other.page, crossed);
    const toldB = await sheetSaid(other.page);
    deaf.on = true;
    await s.page.click('.so-q-option[data-label="Postgres"]');
    await s.page.click('.so-q-send');
    await wait(1500);
    await other.page.click('.so-q-option[data-label="SQLite"]');
    await other.page.click('.so-q-send');
    const clash = await awaitSheet(other.page, (d) => !!d && !!d.status && /Another device is already answering/.test(d.status.text), 10000);
    ok(clash.ok && /Postgres/.test(clash.seen.status.text), 'a Send that crossed it is told another device is already answering, and with what',
      JSON.stringify(clash.seen && clash.seen.status));
    deaf.on = false;
    pause.release();
    pause = null;
    const two = await awaitAnswerLogged(s, 2);
    await sheetShut(other.page, 15000);
    const words = await toldB();
    ok(two.length === 2 && two[1].answers[0].choices.join(',') === 'Postgres', 'only Postgres went in', JSON.stringify(two.map((l) => l.answers)));
    ok(!words.includes('All questions answered'), 'and the device that crossed it never says it answered it', words.join(' | '));

    // A second tab of the same browser is the same device: it is never told
    // that another device answered.
    await awaitActivity(s, watched, (act) => act.state === 'idle', 15000);
    await askQuestion(s, watched);
    const tab = await s.context.newPage();
    tab.on('pageerror', (err) => s.errors.push('pageerror: ' + err.message));
    await tab.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await awaitHome(tab, (h) => h.shown && h.conn === 'live', 15000);
    await openOn(s.page, watched);
    await openOn(tab, watched);
    const tabSaid = await sheetSaid(tab);
    await s.page.click('.so-q-option[data-label="SQLite"]');
    await s.page.click('.so-q-send');
    const three = await awaitAnswerLogged(s, 3);
    await sheetShut(s.page, 15000);
    await sheetShut(tab, 15000);
    const tabWords = await tabSaid();
    ok(three.length === 3 && three[2].answers[0].choices.join(',') === 'SQLite' && !tabWords.some((w) => /another device/i.test(w)),
      'a second tab of the same browser follows the answer without being told another device gave it', tabWords.join(' | '));
    await tab.close();

    // A Send on a slow uplink: the second device sends first, but its
    // request is still on its way when the first device's answer starts and
    // reaches it on the stream. That run is not its answer - its own Send is
    // refused for it - so it never says Answered, nor counts it as its own.
    const slow = await makeAgent(s, 'Slow uplink');
    const uplink = { slow: false };
    await other.page.route(/\/api\/sessions\/[^/]+\/answer$/, async (route) => {
      if (route.request().method() !== 'POST' || !uplink.slow) return route.continue();
      await wait(2000);
      await route.continue().catch(() => {});
    });
    await askQuestion(s, slow);
    pause = gate();
    await openOn(s.page, slow);
    await openOn(other.page, slow);
    const slowA = await sheetSaid(s.page);
    const slowB = await sheetSaid(other.page);
    uplink.slow = true;
    await other.page.click('.so-q-option[data-label="SQLite"]');
    await other.page.click('.so-q-send');
    await wait(300);
    await s.page.click('.so-q-option[data-label="Postgres"]');
    await s.page.click('.so-q-send');
    await wait(4000);
    pause.release();
    pause = null;
    const four = await awaitAnswerLogged(s, 4);
    await sheetShut(s.page, 15000);
    await sheetShut(other.page, 15000);
    uplink.slow = false;
    const slowWordsA = await slowA();
    const slowWordsB = await slowB();
    ok(four.length === 4 && four[3].answers[0].choices.join(',') === 'Postgres', 'only the first device’s Postgres went in',
      JSON.stringify(four.slice(3).map((l) => l.answers)));
    ok(slowWordsA.includes('All questions answered'), 'the device whose answer went in says so', slowWordsA.join(' | '));
    ok(slowWordsB.some((w) => /another device/i.test(w)) && !slowWordsB.includes('All questions answered')
      && !slowWordsB.some((w) => /^Answered(?! on another device)/.test(w)),
      'the device whose Send was slower says another device answered, never that it answered it itself',
      slowWordsB.join(' | '));
    ok(unexpected(s.errors, REFUSED_NOISE).length === 0, 'no unexpected console errors',
      unexpected(s.errors, REFUSED_NOISE).join(' | ') || '0');
  } finally {
    if (pause) pause.release();
    if (other) await other.context.close().catch(() => {});
    await s.stop();
    await stub.close();
  }
}

// home-followup: the agent asks its next question right after the dialog's
// answer - the next message in its queue asks again - and the open dialog
// takes the follow-up up rather than closing under it; the follow-up gets the
// answer given to it, and only that.
async function homefollowup() {
  const s = await homeChats();
  let stop = false;
  let loop = null;
  try {
    await awaitHome(s.page, (h) => h.shown, 10000);
    const { id } = await askingChat(s, 'Asks a follow-up');
    const followAsk = askFor('And which cache should it use?', ['Redis', 'Memcached']);
    // Queued behind the turn that asks, so it asks the moment that turn
    // ends: right after the answer.
    ok((await tellChat(s, id, '/askuser ' + JSON.stringify(followAsk))) === 200, 'the follow-up waits in the chat’s queue', 'queued');
    await awaitHome(s.page, (h) => h.counts.questions === 1, 10000);
    await s.page.click('.so-home__q-action');
    await awaitAsk(s.page, (d) => !!d && d.options.length === 2, 10000);
    const seen = [];
    let answered = false;
    loop = (async () => {
      while (!stop) {
        const d = await askLook(s.page).catch(() => null);
        const h = await homeLook(s.page).catch(() => ({ counts: {} }));
        seen.push({ answered, q: h.counts.questions, d: d && { question: d.question, finished: d.finished } });
        await wait(100);
      }
    })();
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    answered = true;
    const first = await returned(s, id);
    const followUp = await awaitAsk(s.page, (d) => !!d && d.question === 'And which cache should it use?' && !d.finished
      && d.options.every((o) => o.pressed !== 'true'), 15000);
    ok(!!first && first.answers.db.selected.join() === 'sqlite' && followUp.ok,
      'the first is answered, and the follow-up is taken up by the open dialog',
      JSON.stringify({ first: first && first.answers.db.selected, now: followUp.seen && { q: followUp.seen.question, finished: followUp.seen.finished } }));
    stop = true;
    await loop;
    const closedWhileAsked = seen.filter((one) => one.answered && !one.d && one.q > 0);
    ok(closedWhileAsked.length === 0, 'the dialog never closed while Home counted the follow-up', JSON.stringify(closedWhileAsked.slice(0, 3)));
    await s.page.click('dialog.so-askdlg .so-q-option[data-option="redis"]');
    await s.page.click('dialog.so-askdlg [data-action="send"]');
    let second = null;
    for (const end = Date.now() + 15000; Date.now() < end && !second; await wait(200)) {
      const said = ((await historyOf(s, id)).messages || []).filter((m) => m.role === 'assistant').map(textOf)
        .filter((t) => t.startsWith('ask_user returned: '));
      if (said.length >= 2) second = JSON.parse(said[1].slice('ask_user returned: '.length).split('\n')[0]);
    }
    const asks = (await historyOf(s, id)).asks || [];
    ok(!!second && second.answers.db.selected.join() === 'redis' && asks.length === 2 && asks.every((a) => a.answered),
      'the follow-up gets the answer given to it, Redis, and only that', JSON.stringify({ second: second && second.answers.db.selected, asks: asks.length }));
    ok(await askShut(s.page, 20000), 'and then the dialog closes', 'shut');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    stop = true;
    if (loop) await loop.catch(() => {});
    await s.stop();
  }
}

// The chat scenarios run against the scripted agent (internal/agent/fake.go,
// AGENT-HOST.md §8), which a server registers when SOCRATES_FAKE_AGENT=1 is
// in its environment: what a message says is what its turn does (/stream,
// /tools, /askmany, /plan ...), and what it sends has the shapes of Claude's
// and Codex's content, so the view is measured on what the real adapters
// send.
const FAKE_AGENT = { SOCRATES_FAKE_AGENT: '1' };

// chatServer is a signed-in server with the scripted agent.
async function chatServer(options = {}) {
  const s = await start({
    viewport: { width: 1280, height: 860 }, ...options, env: { ...FAKE_AGENT, ...(options.env || {}) },
  });
  await setup(s.page, s.url);
  return s;
}

// chatUp waits for the chat view to be what the stage shows.
const chatUp = (page, timeout = 20000) =>
  page.waitForSelector('#chat:not([hidden]) .so-composer__input:not([data-e2e-left])', { timeout });

// startChat drives the sheet to a chat - with the scripted agent unless
// another agent is named - the way a person starts one, and waits for its
// composer.
async function startChat(page, harness = 'fake') {
  const was = await page.evaluate(() => location.hash.slice(1));
  await ensureNav(page);
  await page.click('#newSession');
  await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
  await page.waitForSelector('#newSessionSheet .so-settings__program[data-value="' + harness + '"]', { timeout: 10000 });
  await page.click('#newSessionSheet .so-settings__program[data-value="' + harness + '"]');
  await pickModelIfNeeded(page);
  await leaveComposer(page, '');
  await page.click('#nsStart');
  await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
  await page.waitForFunction((before) => location.hash.length > 1 && location.hash.slice(1) !== before,
    was, { timeout: 30000 });
  await chatUp(page);
  return page.evaluate(() => location.hash.slice(1));
}

// openChat goes to a chat through its address, as a link to it would, and
// waits for that chat's own composer: the view makes one per chat, so the
// one of the chat that was on screen is marked first and waited away.
async function openChat(page, id) {
  await page.waitForSelector(rowSel(id), { state: 'attached', timeout: 20000 });
  await leaveComposer(page, id);
  await page.evaluate((target) => { location.hash = '#' + target; }, id);
  await chatUp(page);
}

// leaveComposer marks the composer of the chat on screen, when another chat
// is about to take its place, so that chatUp waits for the new one.
const leaveComposer = (page, id) => page.evaluate((next) => {
  const input = document.querySelector('#chat .so-composer__input');
  if (input && location.hash.slice(1) !== next) input.dataset.e2eLeft = '1';
}, id);

// say writes a message and sends it: Enter at a desk, Send on a touch screen.
async function say(page, text, { button = false } = {}) {
  await page.fill('#chat .so-composer__input', text);
  if (button) await page.click('#chat .so-composer__send');
  else await page.press('#chat .so-composer__input', 'Enter');
}

const logText = (page) => page.$eval('#chat .so-chat__log', (n) => n.innerText).catch(() => '');

// awaitLog waits for the conversation on screen to show something.
const awaitLog = (page, needle, timeout = 20000) => page.waitForFunction((want) => {
  const log = document.querySelector('#chat .so-chat__log');
  return !!log && log.innerText.includes(want);
}, needle, { timeout }).then(() => true, () => false);

// chatOf is GET /api/chats/{id} (the summary and the open questions), and
// historyOf the whole projection (?history=1), as the server holds them.
async function chatOf(s, id) {
  const res = await s.context.request.get(s.url + '/api/chats/' + encodeURIComponent(id));
  return res.json();
}

async function historyOf(s, id) {
  const res = await s.context.request.get(s.url + '/api/chats/' + encodeURIComponent(id) + '?history=1');
  return ((await res.json()).messages) || { messages: [] };
}

const userTexts = (history) => (history.messages || []).filter((m) => m.role === 'user').map((m) => m.text);

// awaitChat polls the summary until `test` holds, and answers the last one
// either way.
async function awaitChat(s, id, test, timeout = 20000) {
  const until = Date.now() + timeout;
  let body = await chatOf(s, id);
  while (!(body.chat && test(body)) && Date.now() < until) {
    await wait(200);
    body = await chatOf(s, id);
  }
  return body;
}

const idle = (body) => !body.chat.turn_running && !body.chat.queued;

// lastAnswer is the newest assistant message as the page shows it.
const lastAnswer = (page) => page.evaluate(() => {
  const all = [...document.querySelectorAll('#chat .so-msg[data-role="assistant"]')];
  return all.length ? all[all.length - 1].innerText : '';
});

// sideScroll is how far the page can be scrolled sideways: 0 is not at all.
const sideScroll = (page) => page.evaluate(() => {
  const root = document.scrollingElement;
  return Math.max(root.scrollWidth - root.clientWidth, document.body.scrollWidth - window.innerWidth, 0);
});

// chat-stream: a chat from the sheet; a streamed answer whose element is
// never rebuilt; Markdown and the thinking; a reload that replays the
// journal; a message queued behind a turn; Stop; the draft; the palette;
// the model picker; the lines between messages; usage behind the "i"; and a
// prepared chat whose draft becomes its first message.
async function chatstream() {
  const s = await chatServer();
  const { page } = s;
  try {
    // The sheet asks a chat for its agent, its folder and its model.
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.waitForSelector('#newSessionSheet .so-settings__program[data-value="fake"]', { timeout: 10000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="fake"]');
    // The profile field went with the profiles (WP10): a step that is not
    // there is a step not asked.
    const steps = await page.evaluate(() => Object.fromEntries(['nsHarnessField', 'nsDirField', 'nsModelField',
      'nsEffortField', 'nsProfileField', 'nsUltracodeField'].map((one) => [one.replace(/^ns|Field$/g, '').toLowerCase(),
      !!document.getElementById(one) && !document.getElementById(one).hidden])));
    ok(steps.harness && steps.dir && steps.model && !steps.profile && !steps.ultracode,
      'the sheet asks a chat for its agent, its folder and its model, and nothing else', JSON.stringify(steps));
    await page.keyboard.press('Escape');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 10000 }).catch(() => {});
    const id = await startChat(page);
    const stage = await page.evaluate(() => ({
      chat: !document.getElementById('chat').hidden,
      term: document.getElementById('termWrap').hidden,
      focus: document.activeElement && document.activeElement.classList.contains('so-composer__input'),
    }));
    const row = await sessionRow(s, id);
    ok(row.kind === 'chat' && stage.chat && stage.term, 'a chat is a session of kind chat, drawn by the page where a terminal would stand',
      JSON.stringify({ kind: row.kind, ...stage }));
    ok(stage.focus, 'and the cursor is in its composer', String(stage.focus));
    const empty = await page.$eval('#chat .so-chat__empty', (n) => n.innerText.replace(/\s+/g, ' ').trim()).catch(() => '');
    ok(/Ask Scripted agent/.test(empty), 'an empty chat says what it is for', empty);

    // A streamed answer: one element for its text from the first delta to
    // the last.
    await say(page, '/stream 60 40');
    await page.waitForFunction(() => {
      const parts = document.querySelectorAll('#chat .so-msg[data-role="assistant"] .so-part[data-kind="text"]');
      const last = parts[parts.length - 1];
      return !!last && /w3 /.test(last.textContent);
    }, null, { timeout: 15000 });
    await page.evaluate(() => {
      const parts = document.querySelectorAll('#chat .so-msg[data-role="assistant"] .so-part[data-kind="text"]');
      const last = parts[parts.length - 1];
      last.wp8Stamp = 'first';
      last.closest('.so-msg').wp8Stamp = 'first';
    });
    const user = await page.$eval('#chat .so-msg[data-role="user"]', (n) => n.innerText).catch(() => '');
    ok(/\/stream 60 40/.test(user), 'the message is in the conversation, as the person wrote it', oneLine(user));
    const done = await awaitLog(page, 'w60', 20000);
    const kept = await page.evaluate(() => {
      const parts = document.querySelectorAll('#chat .so-msg[data-role="assistant"] .so-part[data-kind="text"]');
      const last = parts[parts.length - 1];
      return { part: last && last.wp8Stamp, message: last && last.closest('.so-msg').wp8Stamp, text: last ? last.textContent.slice(-12) : '' };
    });
    ok(done && kept.part === 'first' && kept.message === 'first',
      'the answer streams into the element its first words were drawn in, to the last word', JSON.stringify(kept));
    const summary = await awaitChat(s, id, idle);
    ok(summary.chat.last_outcome === 'completed', 'and the turn ends completed', summary.chat.last_outcome);

    // Markdown, and the thinking before it, shut.
    await say(page, '/md 5');
    await awaitLog(page, 'The tests in', 20000);
    await awaitChat(s, id, idle);
    const md = await page.evaluate(() => {
      const msgs = document.querySelectorAll('#chat .so-msg[data-role="assistant"]');
      const msg = msgs[msgs.length - 1];
      const think = msg.querySelector('.so-reasoning');
      const link = msg.querySelector('.so-md a');
      return {
        h2: !!msg.querySelector('.so-md h2'), list: msg.querySelectorAll('.so-md ol li').length,
        table: msg.querySelectorAll('.so-md table td').length, code: !!msg.querySelector('.so-code code'),
        lang: (msg.querySelector('.so-code__lang') || {}).textContent, copy: !!msg.querySelector('.so-code [data-action="copy-code"]'),
        target: link && link.getAttribute('target'), rel: link && link.getAttribute('rel'),
        img: msg.querySelectorAll('img').length,
        thinking: !!think, shut: !!think && !think.open, thought: think ? think.querySelector('summary').textContent.trim() : '',
        body: think ? !!think.querySelector('.so-reasoning__body') : null,
      };
    });
    ok(md.h2 && md.list === 3 && md.table >= 6 && md.code && md.lang === 'go' && md.copy,
      'Markdown is drawn: a heading, a list, a table, and a code block with its language and Copy', JSON.stringify(md));
    ok(md.target === '_blank' && /noopener/.test(md.rel || '') && md.img === 0,
      'a link opens beside the app, and nothing is fetched that was not asked for', JSON.stringify(md));
    ok(md.thinking && md.shut && /^Thought/.test(md.thought) && md.body === false,
      'the thinking before it is one line, shut', JSON.stringify(md));
    await page.click('#chat .so-reasoning > summary');
    const opened = await page.waitForFunction(() => {
      const all = document.querySelectorAll('#chat .so-reasoning');
      const think = all[all.length - 1];
      return think.open && /A list, the code and a table/.test(think.textContent);
    }, null, { timeout: 5000 }).then(() => true, () => false);
    ok(opened, 'and opens to what the agent thought');

    // Usage and cost, behind the "i".
    await page.click('#chat .so-composer__usage');
    const usage = await page.waitForSelector('dialog .ds-dialog__title:text-is("Usage of this chat")', { timeout: 5000 })
      .then(() => page.$eval('dialog[open]', (n) => n.innerText), () => '');
    ok(/Cost so far: \$/.test(usage) && /Scripted agent 1\.0\.0/.test(usage),
      'the chat’s usage and cost are behind its "i"', oneLine(usage));
    await page.keyboard.press('Escape');

    // A reload replays the journal into the same conversation.
    const before = await logText(page);
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    await openChat(page, id);
    const replayed = await awaitLog(page, 'w60', 20000) && await awaitLog(page, 'The tests in', 5000);
    const after = await logText(page);
    ok(replayed && after.replace(/\s+/g, ' ').includes('you said') === before.replace(/\s+/g, ' ').includes('you said'),
      'after a reload the conversation is replayed from the journal', oneLine(after.slice(-160)));

    // A message sent during a turn is queued, and taken up after it.
    await say(page, '/sleep 4');
    await page.waitForSelector('#chat .so-composer__stop:not([hidden])', { timeout: 10000 });
    await say(page, 'after the sleep');
    const queued = await page.waitForSelector('#chat .so-msg[data-role="user"][data-state="queued"] .so-msg__chip', { timeout: 10000 })
      .then((n) => n.innerText(), () => '');
    ok(/Queued/.test(queued), 'a message sent while the agent works is shown queued', queued);
    const taken = await awaitLog(page, 'you said: after the sleep', 20000);
    const unqueued = await page.$$eval('#chat .so-msg[data-role="user"][data-state="queued"]', (n) => n.length);
    ok(taken && unqueued === 0, 'and the agent takes it up when it is done', String(unqueued) + ' still queued');

    // Stop while busy.
    await say(page, '/sleep 30');
    await page.waitForSelector('#chat .so-composer__stop:not([hidden])', { timeout: 10000 });
    await page.click('#chat .so-composer__stop');
    const stopped = await page.waitForSelector('#chat .so-line[data-kind="interrupted"], #chat .so-msg[data-state="interrupted"]', { timeout: 15000 })
      .then(() => true, () => false);
    const stopGone = await page.waitForSelector('#chat .so-composer__stop', { state: 'hidden', timeout: 10000 }).then(() => true, () => false);
    const outcome = (await awaitChat(s, id, idle)).chat.last_outcome;
    ok(stopped && stopGone && outcome === 'interrupted', 'Stop interrupts the turn, says so, and goes away', outcome);

    // The draft is saved as it is written, and is there after a reload.
    await page.fill('#chat .so-composer__input', 'half a thought, not sent');
    let draft = { text: '' };
    for (let i = 0; i < 20 && draft.text !== 'half a thought, not sent'; i += 1) {
      await wait(300);
      draft = await draftOf(s, id);
    }
    ok(draft.text === 'half a thought, not sent', 'the composer saves its draft on the server as it is written', JSON.stringify(draft.text));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    await openChat(page, id);
    const back = await page.waitForFunction(() => document.querySelector('#chat .so-composer__input').value === 'half a thought, not sent',
      null, { timeout: 10000 }).then(() => true, () => false);
    ok(back, 'and it is back in the composer after a reload', await page.$eval('#chat .so-composer__input', (n) => n.value));

    // Two devices writing the same draft: the second save is refused, and
    // the person chooses, as with a prepared session's prompt.
    await page.fill('#chat .so-composer__input', '');
    for (let i = 0; i < 20 && draft.text !== ''; i += 1) {
      await wait(300);
      draft = await draftOf(s, id);
    }
    const phone = await s.context.request.put(s.url + '/api/sessions/' + id + '/draft',
      { data: { text: 'written on the phone', base_rev: draft.rev } });
    await page.fill('#chat .so-composer__input', 'written at the desk');
    const conflict = await page.waitForSelector('#chat .so-composer__conflict:not([hidden])', { timeout: 10000 })
      .then((n) => n.innerText(), () => '');
    ok(phone.ok() && /changed on another device/.test(conflict) && /written on the phone/.test(conflict),
      'a draft changed on another device in between is not overwritten: both texts are shown', oneLine(conflict));
    await page.click('#chat [data-action="keep-mine"]');
    for (let i = 0; i < 20 && draft.text !== 'written at the desk'; i += 1) {
      await wait(300);
      draft = await draftOf(s, id);
    }
    ok(draft.text === 'written at the desk', 'and Keep mine saves this one over it', JSON.stringify(draft.text));
    await page.fill('#chat .so-composer__input', '');

    // The palette: the commands the agent reported, and one run from it.
    await page.fill('#chat .so-composer__input', '/');
    const palette = await page.waitForSelector('#chat .so-composer__palette:not([hidden])', { timeout: 5000 })
      .then(() => page.$$eval('#chat .so-palette__item', (n) => n.map((one) => one.dataset.command)), () => []);
    ok(palette.join(',') === 'compact,context', 'a slash opens the commands the agent reported (agent.info)', palette.join(','));
    await page.press('#chat .so-composer__input', 'Enter');
    const chosen = await page.$eval('#chat .so-composer__input', (n) => n.value);
    await page.press('#chat .so-composer__input', 'Enter');
    const compacted = await awaitLog(page, 'The conversation was compacted.', 15000);
    const line = await page.$eval('#chat .so-line[data-kind="command"]', (n) => n.innerText.trim()).catch(() => '');
    ok(chosen === '/compact ' && compacted && line === '/compact', 'Enter takes the first, and the command runs as a command',
      JSON.stringify({ chosen, line }));

    // The model and its effort.
    await page.click('#chat .so-composer__model');
    await page.waitForSelector('.so-model-menu:not([hidden]) [data-model="fake-large"]', { timeout: 5000 });
    await page.click('.so-model-menu [data-model="fake-large"]');
    await awaitLog(page, 'Model: Fake large', 15000);
    await page.click('#chat .so-composer__model');
    await page.waitForSelector('.so-model-menu:not([hidden]) [data-effort="low"]', { timeout: 5000 });
    await page.click('.so-model-menu [data-effort="low"]');
    const changed = await awaitLog(page, 'Model: Fake large · Low', 15000);
    const label = await page.$eval('#chat .so-composer__model-text', (n) => n.textContent);
    const summed = await awaitChat(s, id, (b) => b.chat.effort === 'low');
    ok(changed && label === 'Fake large · Low' && summed.chat.model === 'fake-large',
      'the model and effort picker changes them for the turns that follow, and says so',
      JSON.stringify({ label, model: summed.chat.model, effort: summed.chat.effort }));

    // The lines between messages, and a limit with when it renews.
    await say(page, '/dividers');
    await awaitLog(page, 'Every kind of line was written.', 15000);
    await say(page, '/limit');
    const limit = await page.waitForSelector('#chat [data-kind="limit"]', { timeout: 15000 })
      .then((n) => n.innerText(), () => '');
    const lines = await page.$$eval('#chat .so-line, #chat .so-line-alert', (n) => n.map((one) => one.dataset.kind + ':' + one.innerText.trim()));
    ok(lines.some((l) => /^notice:The conversation was compacted/.test(l)) && lines.some((l) => /^account:Switched from Work to Personal/.test(l))
      && lines.some((l) => /^error:A message could not be handed/.test(l)),
      'notices, an account switch and an error are lines of their own', JSON.stringify(lines));
    ok(/Limit reached — renews in (1 h 5\d min|2 h)/.test(limit), 'a limit says so, with when it renews', oneLine(limit));

    // A prepared chat: its draft is the first message when it starts.
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="fake"]');
    const offered = await page.$eval('#nsPrestartField', (n) => !n.hidden);
    await page.click('#nsPrestart');
    await page.click('#nsStart');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await page.waitForSelector('#prestart:not([hidden]) #psPrompt', { timeout: 15000 });
    const pid = await page.evaluate(() => location.hash.slice(1));
    await page.waitForFunction(() => !document.getElementById('psPrompt').disabled, null, { timeout: 10000 });
    await page.fill('#psPrompt', 'the first words, prepared');
    await page.waitForFunction(() => /^Saved$/.test(document.getElementById('psSave').textContent.trim()), null, { timeout: 10000 }).catch(() => {});
    const prow = await sessionRow(s, pid);
    ok(offered && prow.kind === 'chat' && prow.state === 'prestart', 'a chat can be prepared, and is a prepared chat',
      JSON.stringify({ offered, kind: prow.kind, state: prow.state }));
    await page.click('#psStart');
    const started = await chatUp(page, 30000).then(() => true, () => false);
    const first = await awaitLog(page, 'you said: the first words, prepared', 20000);
    const history = await historyOf(s, pid);
    const composer = await page.$eval('#chat .so-composer__input', (n) => n.value).catch(() => 'none');
    ok(started && first && userTexts(history).join('|') === 'the first words, prepared' && composer === '',
      'Start turns the panel into the chat, its draft the first message, sent once, and the composer empty',
      JSON.stringify({ started, users: userTexts(history), composer }));
    await shot(page, 'chat-stream');
    // The refused draft is the browser's own report of the conflict above.
    const refusedDraft = /status of 409 \(Conflict\) @ \S+\/draft$/;
    ok(unexpected(s.errors, refusedDraft).length === 0, 'no console errors', unexpected(s.errors, refusedDraft).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-tools: tool rows that open to their input and output, a failed one,
// the diff viewer with both content shapes, a subagent's group, a workflow's
// phases and agents, and the task list pinned above the composer while the
// turn works through it.
async function chattools() {
  const s = await chatServer();
  const { page } = s;
  try {
    const id = await startChat(page);
    await say(page, '/tools');
    await awaitLog(page, 'Done with the tools.', 20000);
    const rows = await page.$$eval('#chat .so-tool', (n) => n.map((one) => ({
      kind: one.dataset.kind, state: one.dataset.state, open: one.open,
      name: (one.querySelector('.so-tool__name') || {}).textContent,
      summary: (one.querySelector('.so-tool__summary') || {}).textContent,
      time: (one.querySelector('.so-tool__time') || {}).textContent,
    })));
    const by = (name) => rows.find((r) => r.name === name) || {};
    ok(by('Read').open === false && by('Bash').open === false && by('Bash').summary === 'go test ./server/...'
      && /s$/.test(by('Bash').time || ''),
      'a tool call is one row - its name, what it did, how long - shut', JSON.stringify(rows.slice(0, 2)));
    ok(by('Grep').state === 'error', 'a call that failed says so', JSON.stringify(by('Grep')));
    await page.click('#chat .so-tool[data-kind="tool"]:has(.so-tool__name:text-is("Bash")) > summary');
    const output = await page.waitForSelector('#chat .so-tool[open] .so-tool__out', { timeout: 5000 })
      .then((n) => n.innerText(), () => '');
    const input = await page.$eval('#chat .so-tool[open] .so-code code', (n) => n.textContent).catch(() => '');
    ok(/--- PASS: TestHandleGet/.test(output) && input === 'go test ./server/...',
      'and opens to the command it ran and what it printed', oneLine(input + ' | ' + output));

    const diffs = await page.$$eval('#chat .so-tool[data-kind="diff"]', (n) => n.map((one) => ({
      open: one.open,
      files: [...one.querySelectorAll('.so-diff__file')].map((f) => ({
        path: f.querySelector('.so-diff__path').textContent,
        added: f.querySelectorAll('.so-diff__line[data-type="add"]').length,
        removed: f.querySelectorAll('.so-diff__line[data-type="del"]').length,
      })),
      count: (one.querySelector('.so-tool__head .so-diff__count') || {}).textContent,
    })));
    ok(diffs.length === 2 && diffs.every((d) => d.open) && diffs[0].files.length === 1
      && diffs[0].files[0].added === 6 && diffs[0].files[0].removed === 2,
      'an edit is open on its diff: the file, the lines added and removed', JSON.stringify(diffs[0]));
    ok(diffs[1] && diffs[1].files.length === 2 && diffs[1].files[1].path.endsWith('handler_test.go'),
      'and a change to two files (Codex’s shape) is one viewer with both', JSON.stringify(diffs[1]));
    const numbers = await page.$eval('#chat .so-diff__line[data-type="add"]', (n) => [...n.querySelectorAll('.so-diff__no')].map((c) => c.textContent));
    ok(numbers[0] === '' && Number(numbers[1]) >= 10, 'every line has its numbers in the old and the new file', JSON.stringify(numbers));

    // A subagent: its own calls nested under its row.
    await say(page, '/subagent');
    await awaitLog(page, 'The subagent finished.', 20000);
    const group = await page.$eval('#chat .so-tool[data-group]', (n) => ({
      open: n.open, name: n.querySelector('.so-tool__name').textContent,
      nested: [...n.querySelectorAll('.so-group > .so-part')].map((p) => p.dataset.kind),
      text: (n.querySelector('.so-group .so-part[data-kind="text"]') || {}).textContent,
    })).catch(() => null);
    ok(!!group && group.open && group.name === 'Task' && group.nested.join(',') === 'text,tool' && /Looking around/.test(group.text),
      'a subagent is a group with what it said and did nested inside', JSON.stringify(group));

    // A workflow: its phases, and every agent's live state.
    await say(page, '/workflow 900');
    const live = await page.waitForFunction(() => {
      const wf = [...document.querySelectorAll('#chat .so-tool[data-group]')]
        .filter((n) => n.querySelector(':scope > summary .so-tool__name').textContent === 'Workflow').pop();
      if (!wf || !wf.querySelector('.so-phases')) return null;
      const phases = [...wf.querySelectorAll('.so-phase')].map((p) => p.dataset.state);
      const running = wf.querySelectorAll('.so-phase .so-tool[data-state="running"] .ds-spinner').length;
      return phases.length === 3 && running > 0 ? { phases, running } : null;
    }, null, { timeout: 15000 }).then((h) => h.jsonValue(), () => null);
    ok(!!live, 'a workflow shows its three phases and its agents at work, each turning', JSON.stringify(live));
    await awaitLog(page, 'The workflow finished.', 20000);
    const finished = await page.evaluate(() => {
      const wf = [...document.querySelectorAll('#chat .so-tool[data-group]')]
        .filter((n) => n.querySelector(':scope > summary .so-tool__name').textContent === 'Workflow').pop();
      return {
        phases: [...wf.querySelectorAll('.so-phase')].map((p) => p.querySelector('.so-phase__title').textContent + ':' + p.dataset.state),
        agents: wf.querySelectorAll('.so-phase .so-tool[data-state="done"]').length,
        progress: (wf.querySelector('.so-tool__progress') || {}).textContent,
      };
    });
    ok(finished.phases.join(',') === 'Plan:done,Build:done,Review:done' && finished.agents === 4 && finished.progress === '4 of 4 agents',
      'and when it is through, every phase and every agent is done', JSON.stringify(finished));

    // The task list, pinned while the turn works through it.
    await say(page, '/todo 900');
    const pinned = await page.waitForSelector('#chat .so-chat__todo:not([hidden]) .so-todo[data-pinned]', { timeout: 10000 })
      .then(() => page.$eval('#chat .so-chat__todo', (n) => ({
        items: [...n.querySelectorAll('.so-todo__item')].map((i) => i.dataset.status),
        count: n.querySelector('.so-todo__count').textContent,
        above: n.getBoundingClientRect().bottom <= document.querySelector('#chat .so-composer').getBoundingClientRect().top + 1,
      })), () => null);
    ok(!!pinned && pinned.items.length === 3 && pinned.items.includes('in_progress') && pinned.above,
      'a task list is pinned above the composer while the turn works through it', JSON.stringify(pinned));
    await awaitLog(page, 'All three tasks are done.', 20000);
    const unpinned = await page.waitForSelector('#chat .so-chat__todo', { state: 'hidden', timeout: 5000 }).then(() => true, () => false);
    const inMessage = await page.$$eval('#chat .so-msg .so-part[data-kind="todo"] .so-todo__item[data-status="completed"]', (n) => n.length);
    ok(unpinned && inMessage >= 3, 'and once the turn is over it is in the answer, not pinned', JSON.stringify({ unpinned, inMessage }));
    await shot(page, 'chat-tools');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
    void id;
  } finally { await s.stop(); }
}

// chat-offline: a message written with no network is queued visibly and
// delivered once when it comes back; a message whose answer was lost on
// the way back is sent again and still arrives once; and so is everything
// else a chat is told while it cannot be reached: what two tabs wrote, a
// model chosen offline (before the message after it), an answer to a
// question. Typing before the saved draft has loaded keeps what was typed,
// and a command the agent refused says it did not run.
async function chatoffline() {
  const s = await chatServer();
  const { page } = s;
  try {
    const id = await startChat(page);
    await say(page, 'hello');
    await awaitLog(page, 'you said: hello', 20000);

    await s.context.setOffline(true);
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await say(page, 'written in a tunnel');
    const local = await page.waitForSelector('#chat .so-msg[data-role="user"][data-state="local"] .so-msg__chip', { timeout: 15000 })
      .then((n) => n.innerText(), () => '');
    const kept = await page.evaluate(() => JSON.parse(localStorage.getItem('socrates.outbox.chats') || '[]')
      .map((item) => item.payload.text));
    ok(/Not sent yet/.test(local) && kept.includes('written in a tunnel'),
      'a message written offline is on screen as not sent yet, and kept in this browser', JSON.stringify({ local, kept }));
    const said = await page.$eval('#chat .so-chat__state', (n) => n.innerText).catch(() => '');
    ok(/Offline/.test(said), 'and the chat says it is offline', oneLine(said));
    await wait(1500);
    await s.context.setOffline(false);
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    const arrived = await awaitLog(page, 'you said: written in a tunnel', 30000);
    await awaitChat(s, id, idle);
    const once = userTexts(await historyOf(s, id)).filter((t) => t === 'written in a tunnel').length;
    const empty = await page.evaluate(() => JSON.parse(localStorage.getItem('socrates.outbox.chats') || '[]').length);
    ok(arrived && once === 1 && empty === 0, 'back online it is delivered, exactly once, and the outbox is empty',
      JSON.stringify({ arrived, once, empty }));

    // The server takes a message, and its answer never reaches the page:
    // the outbox sends it again with the same client_id, and it is one
    // message all the same.
    let cut = 0;
    await page.route('**/api/chats/*/messages', async (route) => {
      if (cut === 0) {
        cut += 1;
        await route.fetch().catch(() => null);
        await route.abort('connectionreset');
        return;
      }
      await route.continue();
    });
    await say(page, 'its answer was lost');
    const delivered = await awaitLog(page, 'you said: its answer was lost', 30000);
    await awaitChat(s, id, idle);
    const twice = userTexts(await historyOf(s, id)).filter((t) => t === 'its answer was lost').length;
    const turns = (await logText(page)).split('you said: its answer was lost').length - 1;
    ok(cut === 1 && delivered && twice === 1 && turns === 1,
      'a send whose answer was lost is repeated and is still one message and one turn',
      JSON.stringify({ cut, twice, turns }));
    await page.unroute('**/api/chats/*/messages');

    // Offline across a reload: the outbox outlives the page.
    await s.context.setOffline(true);
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await say(page, 'kept across a reload');
    await page.waitForSelector('#chat .so-msg[data-role="user"][data-state="local"]', { timeout: 15000 });
    await s.context.setOffline(false);
    await page.route('**/api/chats/*/messages', (route) => route.abort('internetdisconnected'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    const still = await page.evaluate(() => JSON.parse(localStorage.getItem('socrates.outbox.chats') || '[]')
      .map((item) => item.payload.text));
    await page.unroute('**/api/chats/*/messages');
    await openChat(page, id);
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    const later = await awaitLog(page, 'you said: kept across a reload', 30000);
    await awaitChat(s, id, idle);
    const one = userTexts(await historyOf(s, id)).filter((t) => t === 'kept across a reload').length;
    ok(still.includes('kept across a reload') && later && one === 1,
      'a message still waiting when the page is reloaded goes out after it, once', JSON.stringify({ still, one }));

    // Typing before the saved draft has come back from the server: what
    // was typed stays in the field, and it is what is saved.
    await page.route('**/api/sessions/*/draft', async (route) => {
      if (route.request().method() === 'GET') await wait(2500);
      await route.continue().catch(() => {});
    });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    await openChat(page, id);
    await page.fill('#chat .so-composer__input', 'typed while the draft was loading');
    await wait(3500);
    const typed = await page.$eval('#chat .so-composer__input', (n) => n.value);
    let early = { text: '' };
    for (let i = 0; i < 20 && early.text !== 'typed while the draft was loading'; i += 1) {
      await wait(300);
      early = await draftOf(s, id);
    }
    await page.unroute('**/api/sessions/*/draft');
    ok(typed === 'typed while the draft was loading' && early.text === typed,
      'what is typed before the saved draft has loaded stays, and is the draft that is saved',
      JSON.stringify({ typed, saved: early.text }));
    await page.fill('#chat .so-composer__input', '');

    // Two tabs of one browser, offline: each one's message is kept in the
    // one queue, and both go out once, though the tab that wrote first is
    // closed before the connection is back.
    const other = await s.context.newPage();
    await other.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
    await chatUp(other);
    await s.context.setOffline(true);
    for (const tab of [page, other]) await tab.evaluate(() => window.dispatchEvent(new Event('offline')));
    await say(other, 'from the second tab');
    await other.waitForSelector('#chat .so-msg[data-role="user"][data-state="local"]', { timeout: 15000 });
    await say(page, 'from the first tab');
    await page.waitForSelector('#chat .so-msg[data-role="user"][data-state="local"]', { timeout: 15000 });
    const queuedTabs = await page.evaluate(() => JSON.parse(localStorage.getItem('socrates.outbox.chats') || '[]')
      .map((item) => item.payload.text));
    await other.close();
    await s.context.setOffline(false);
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    const fromFirst = await awaitLog(page, 'you said: from the first tab', 30000);
    const fromSecond = await awaitLog(page, 'you said: from the second tab', 30000);
    await awaitChat(s, id, idle);
    const tabTexts = userTexts(await historyOf(s, id));
    ok(queuedTabs.includes('from the second tab') && queuedTabs.includes('from the first tab') && fromFirst && fromSecond
      && tabTexts.filter((t) => t === 'from the second tab').length === 1 && tabTexts.filter((t) => t === 'from the first tab').length === 1,
      'two tabs offline share one queue: neither drops the other’s message, and each arrives once', JSON.stringify({ queuedTabs }));

    // A model chosen offline is kept in the queue too, before the message
    // written after it, and the button shows it at once.
    const before = await page.$eval('#chat .so-composer__model-text', (n) => n.textContent);
    const target = /^Fake large/.test(before) ? 'fake-small' : 'fake-large';
    const targetLabel = target === 'fake-large' ? 'Fake large' : 'Fake small';
    await s.context.setOffline(true);
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await page.click('#chat .so-composer__model');
    await page.waitForSelector('.so-model-menu:not([hidden]) [data-model="' + target + '"]', { timeout: 5000 });
    await page.click('.so-model-menu [data-model="' + target + '"]');
    const shownModel = await page.$eval('#chat .so-composer__model-text', (n) => n.textContent);
    await say(page, 'after the model');
    const queuedKinds = await page.evaluate(() => JSON.parse(localStorage.getItem('socrates.outbox.chats') || '[]')
      .map((item) => item.payload.kind));
    await s.context.setOffline(false);
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    await awaitLog(page, 'you said: after the model', 30000);
    const modelled = await awaitChat(s, id, (b) => b.chat.model === target && idle(b));
    const order = await logText(page);
    ok(shownModel.startsWith(targetLabel) && queuedKinds.join(',') === 'model,message' && modelled.chat.model === target
      && order.lastIndexOf('Model: ' + targetLabel) < order.lastIndexOf('after the model'),
      'a model chosen offline is queued before the message after it, and changes it', JSON.stringify({ shownModel, queuedKinds, model: modelled.chat.model }));

    // An answer given offline: its card says it is on its way, not that the
    // question still waits; the dialog opened again says so as well; and
    // back online it is delivered once.
    await say(page, '/ask');
    const offAsk = await openAskCard(page);
    await s.context.setOffline(true);
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await page.click('#chat .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="a"]', { timeout: 10000 });
    await page.click('dialog.so-askdlg .so-q-option[data-option="a"]');
    await page.click('dialog.so-askdlg [data-action="send"]');
    await page.click('dialog.so-askdlg [data-action="close"]');
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 5000 });
    const cardSel = '#chat .so-ask[data-ask="' + offAsk + '"]';
    const waitingCard = await page.waitForSelector(cardSel + '[data-state="local"]', { timeout: 5000 })
      .then((n) => n.innerText(), () => '');
    const askedAgain = !!(await page.$(cardSel + ' .so-ask__open'));
    // Reloaded with the answer still unable to get out.
    await page.route('**/api/chats/*/asks/*/answer', (route) => route.abort('internetdisconnected'));
    await s.context.setOffline(false);
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#home:not([hidden])', { timeout: 20000 });
    await openChat(page, id);
    const reopened = await page.waitForSelector(cardSel + '[data-state="local"], ' + cardSel + '[data-state="sending"]', { timeout: 15000 })
      .then((n) => n.getAttribute('data-state'), () => '');
    await page.unroute('**/api/chats/*/asks/*/answer');
    await page.evaluate(() => window.dispatchEvent(new Event('online')));
    const answeredCard = await page.waitForSelector(cardSel + '[data-state="answered"]', { timeout: 30000 }).then(() => true, () => false);
    await awaitChat(s, id, idle);
    const given = await askOf(s, id, offAsk);
    ok(/Not sent yet/.test(waitingCard) && !askedAgain && !!reopened && answeredCard && !!given && given.answered
      && JSON.stringify(given.answer.answers.q1.selected) === '["a"]',
      'an answer given offline is on its card as not sent yet - after a reload too - and is delivered once',
      oneLine(JSON.stringify({ waitingCard, askedAgain, reopened, answeredCard, answer: given && given.answer })));

    // A command the agent refused after the journal had it: the line says
    // it did not run, and Dismiss lets it go.
    await page.route('**/api/chats/*/command', async (route) => {
      await route.fetch().catch(() => null);
      await route.fulfill({ status: 409, contentType: 'application/json',
        body: JSON.stringify({ error: 'the agent has no such command', code: 'unsupported' }) });
    });
    await say(page, '/context ');
    const notRun = await page.waitForSelector('#chat .so-line[data-kind="command"][data-state="failed"]', { timeout: 15000 })
      .then((n) => n.innerText(), () => '');
    await page.unroute('**/api/chats/*/command');
    await page.click('#chat .so-line[data-kind="command"][data-state="failed"] [data-action="outbox-drop"]').catch(() => {});
    const dismissed = await page.waitForSelector('#chat .so-line[data-kind="command"][data-state="failed"]', { state: 'detached', timeout: 5000 })
      .then(() => true, () => false);
    const leftOver = await page.evaluate(() => JSON.parse(localStorage.getItem('socrates.outbox.chats') || '[]').length);
    ok(/Not run/.test(notRun) && dismissed && leftOver === 0, 'a command the agent refused says it did not run, until it is dismissed',
      oneLine(JSON.stringify({ notRun, dismissed, leftOver })));
    await awaitChat(s, id, idle);

    // An answer the server refuses for good: the dialog gives the question
    // back with the reason, the card says it was not sent, and Answer again
    // opens on what was chosen.
    await say(page, '/ask');
    const refusedAsk = await openAskCard(page);
    const refusedSel = '#chat .so-ask[data-ask="' + refusedAsk + '"]';
    await page.route('**/api/chats/*/asks/*/answer', (route) => route.fulfill({ status: 404, contentType: 'application/json',
      body: JSON.stringify({ error: 'this chat never asked that question' }) }));
    await page.click(refusedSel + ' .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="b"]', { timeout: 10000 });
    await page.click('dialog.so-askdlg .so-q-option[data-option="b"]');
    await page.click('dialog.so-askdlg [data-action="send"]');
    const inDialog = await page.waitForSelector('dialog.so-askdlg .so-refused', { timeout: 10000 }).then((n) => n.innerText(), () => '');
    await page.unroute('**/api/chats/*/asks/*/answer');
    await page.click('dialog.so-askdlg [data-action="close"]');
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 5000 });
    const onCard = await page.waitForSelector(refusedSel + '[data-state="failed"]', { timeout: 5000 }).then((n) => n.innerText(), () => '');
    await page.click(refusedSel + ' [data-action="ask-again"]');
    const keptChoice = await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="b"][aria-pressed="true"]', { timeout: 10000 })
      .then(() => true, () => false);
    await page.click('dialog.so-askdlg [data-action="send"]');
    const settledCard = await page.waitForSelector(refusedSel + '[data-state="answered"]', { timeout: 20000 }).then(() => true, () => false);
    await awaitChat(s, id, idle);
    ok(/never asked that question/.test(inDialog) && /Not sent/.test(onCard) && keptChoice && settledCard,
      'an answer refused for good says why, in the dialog and on its card, and is answered again from what was chosen',
      oneLine(JSON.stringify({ inDialog, onCard, keptChoice, settledCard })));

    // The connections this scenario cut, and the refusal it made up, are its
    // own doing.
    const cutNoise = new RegExp(OFFLINE_NOISE.source + '|ERR_CONNECTION_RESET|status of 40[49]');
    ok(unexpected(s.errors, cutNoise).length === 0, 'no unexpected console errors',
      unexpected(s.errors, cutNoise).join(' | ') || '0');
  } finally { await s.stop(); }
}

// answerAll walks the dialog through the full ask_user call of the scripted
// agent (/askmany): every kind of question, with a detail opened, a note,
// Other beside two options (beside three it is one too many), a refused
// number, and a general note.
async function answerAll(page) {
  // 1. single, PostgreSQL preselected (its default): details, context, then SQLite and a note.
  await page.waitForSelector('dialog.so-askdlg .so-q-text', { timeout: 10000 });
  const first = await page.evaluate(() => ({
    title: document.querySelector('dialog.so-askdlg .so-ask-title').textContent,
    step: document.querySelector('dialog.so-askdlg .so-q-step').innerText,
    chosen: [...document.querySelectorAll('dialog.so-askdlg .so-q-option[aria-pressed="true"]')].map((n) => n.dataset.option),
    recommended: !!document.querySelector('dialog.so-askdlg .so-q-option[data-option="pg"] .so-q-recommended'),
  }));
  await page.click('dialog.so-askdlg .so-ask-more');
  const details = await page.waitForSelector('dialog.so-askdlg .so-ask-details', { timeout: 5000 }).then((n) => n.innerText(), () => '');
  await page.click('dialog.so-askdlg .so-q-body > .so-ask-context:last-of-type > summary').catch(() => {});
  await page.click('dialog.so-askdlg .so-q-option[data-option="sqlite"]');
  await page.click('dialog.so-askdlg [data-action="note"]');
  await page.fill('#ask-db-note', 'keep it small');
  await page.click('dialog.so-askdlg [data-action="forward"]');
  // 2. multi, 1 to 3: three chosen, a fourth refused.
  await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="dark"]', { timeout: 5000 });
  const hint = await page.$eval('dialog.so-askdlg .so-q-detail', (n) => n.textContent);
  for (const one of ['dark', 'search', 'export', 'offline']) await page.click('dialog.so-askdlg .so-q-option[data-option="' + one + '"]');
  const multi = await page.$$eval('dialog.so-askdlg .so-q-option[aria-pressed="true"]', (n) => n.map((one) => one.dataset.option));
  // One in the person's own words is a fourth as well, until one of the
  // three is let go.
  await page.click('dialog.so-askdlg [data-action="other"]');
  await page.fill('#ask-features-other', 'an audit log');
  await page.click('dialog.so-askdlg [data-action="forward"]');
  const otherCounted = await page.waitForSelector('dialog.so-askdlg .so-ask-problem', { timeout: 5000 }).then((n) => n.innerText(), () => '');
  await page.click('dialog.so-askdlg .so-q-option[data-option="export"]');
  await page.click('dialog.so-askdlg [data-action="forward"]');
  // 3. text.
  await page.waitForSelector('#ask-name-other', { timeout: 5000 });
  await page.fill('#ask-name-other', 'Atlas');
  await page.click('dialog.so-askdlg [data-action="forward"]');
  // 4. confirm, Yes preselected: No.
  await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="no"]', { timeout: 5000 });
  const confirmDefault = await page.$$eval('dialog.so-askdlg .so-q-option[aria-pressed="true"]', (n) => n.map((one) => one.dataset.option));
  await page.click('dialog.so-askdlg .so-q-option[data-option="no"]');
  await page.click('dialog.so-askdlg [data-action="forward"]');
  // 5. number, 1 to 10, 2 preselected: 20 is refused, 3 is taken.
  await page.waitForSelector('#ask-replicas-other', { timeout: 5000 });
  const numberDefault = await page.$eval('#ask-replicas-other', (n) => n.value);
  await page.fill('#ask-replicas-other', '20');
  await page.click('dialog.so-askdlg [data-action="send"]');
  const refused = await page.waitForSelector('dialog.so-askdlg .so-ask-problem', { timeout: 5000 }).then((n) => n.innerText(), () => '');
  await page.fill('#ask-replicas-other', '3');
  await page.click('dialog.so-askdlg [data-action="general"]');
  await page.fill('#ask-all-general', 'ship it on Friday');
  await page.click('dialog.so-askdlg [data-action="send"]');
  return { first, details, hint, multi, otherCounted, confirmDefault, numberDefault, refused };
}

// The answer the walk above gives, exactly as REBUILD.md §5.2 has it.
const ANSWER_ALL = {
  answers: {
    db: { selected: ['sqlite'], other: null, note: 'keep it small', delegated: false, skipped: false },
    features: { selected: ['dark', 'search'], other: 'an audit log', note: null, delegated: false, skipped: false },
    name: { selected: [], other: 'Atlas', note: null, delegated: false, skipped: false },
    migrate: { selected: ['no'], other: null, note: null, delegated: false, skipped: false },
    replicas: { selected: [], other: '3', note: null, delegated: false, skipped: false },
  },
  general_note: 'ship it on Friday',
  follow_up: null,
};

// askOf is one ask of a chat, as the server holds it.
async function askOf(s, id, askId) {
  const history = await historyOf(s, id);
  return (history.asks || []).find((a) => a.id === askId) || null;
}

// openAskCard waits for the newest open question card and answers its id.
async function openAskCard(page) {
  const card = await page.waitForSelector('#chat .so-ask[data-state="open"]', { timeout: 15000 });
  return card.getAttribute('data-ask');
}

// chat-ask: the question card and the dialog at a desk, every kind of
// question answered in the exact shape of REBUILD §5.2, progress kept
// across a closed dialog, a plan sent back and approved, an answer that
// goes as the next message, a counter-question, Home's Questions card, and
// two devices: the second is told it was answered on the other.
async function chatask() {
  const s = await chatServer();
  const { page } = s;
  try {
    const id = await startChat(page);
    await say(page, '/askmany');
    const askId = await openAskCard(page);
    const card = await page.$eval('#chat .so-ask[data-state="open"]', (n) => ({
      title: n.querySelector('.so-ask__title').textContent, progress: n.querySelector('.so-ask__progress').textContent,
      question: n.querySelector('.so-ask__question').textContent, status: n.querySelector('.ds-status').innerText,
    }));
    ok(card.title === 'Deployment setup' && card.progress === '0 of 5 answered' && card.question === 'Which database should the service use?'
      && /Waiting for you/.test(card.status),
      'the card in the chat has the title, how far it is, and the first open question', JSON.stringify(card));
    const ring = await awaitRow(page, id, (r) => r.waiting, 10000);
    ok(ring.ok, 'the chat’s row says it is waiting for an answer', took(ring));

    // Halfway, closed, and opened again where it was left.
    await page.click('#chat .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="pg"]', { timeout: 10000 });
    await page.click('dialog.so-askdlg .so-q-option[data-option="mysql"]');
    await page.click('dialog.so-askdlg [data-action="close"]');
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 5000 });
    const progress = await page.waitForFunction(() => {
      const n = document.querySelector('#chat .so-ask[data-state="open"] .so-ask__progress');
      return n && n.textContent === '1 of 5 answered' ? n.textContent : null;
    }, null, { timeout: 5000 }).then((h) => h.jsonValue(), () => '');
    await page.click('#chat .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="mysql"][aria-pressed="true"]', { timeout: 5000 });
    ok(progress === '1 of 5 answered', 'a dialog closed halfway leaves the card saying how far it got, and opens where it was left', progress);

    const walk = await answerAll(page);
    ok(walk.first.title === 'Deployment setup' && /Question 1 of 5/.test(walk.first.step) && walk.first.recommended,
      'one question per step, with the batch’s title and the recommended option marked', JSON.stringify(walk.first));
    ok(/JSONB/.test(walk.details), 'an option opens to its details', oneLine(walk.details));
    ok(walk.hint === 'Choose 1 to 3.' && walk.multi.join(',') === 'dark,search,export' && /Choose up to 3/.test(walk.otherCounted),
      'a multi-select says how many, and takes no more than its maximum, its own words counted as one',
      JSON.stringify({ hint: walk.hint, multi: walk.multi, other: walk.otherCounted }));
    ok(walk.confirmDefault.join(',') === 'yes' && walk.numberDefault === '2', 'a question’s default is preselected',
      JSON.stringify({ confirm: walk.confirmDefault, number: walk.numberDefault }));
    ok(/largest number it takes is 10/.test(walk.refused), 'a number out of range is refused, and says why', walk.refused);
    const answered = await page.waitForSelector('dialog.so-askdlg .so-q-finished', { timeout: 15000 }).then((n) => n.innerText(), () => '');
    ok(/Answered/.test(answered), 'Send answers, and the dialog says it is answered', oneLine(answered));
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 10000 }).catch(() => {});
    const ask = await askOf(s, id, askId);
    ok(!!ask && JSON.stringify(ask.answer) === JSON.stringify(ANSWER_ALL),
      'the agent gets the answer in exactly the shape of REBUILD §5.2', JSON.stringify(ask && ask.answer));
    const reply = await awaitLog(page, 'you answered:', 15000);
    const done = await page.$eval('#chat .so-ask[data-ask="' + askId + '"]', (n) => ({ state: n.dataset.state, text: n.innerText }));
    ok(reply && done.state === 'answered' && /Database: SQLite/.test(done.text),
      'the agent goes on, and the card says what was answered', oneLine(JSON.stringify(done)));

    // You decide, and a counter-question instead of answers.
    await say(page, '/ask');
    await openAskCard(page);
    await page.click('#chat .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg [data-action="delegate"]', { timeout: 10000 });
    await page.click('dialog.so-askdlg [data-action="delegate"]');
    const pressed = await page.$eval('dialog.so-askdlg [data-action="delegate"]', (n) => n.getAttribute('aria-pressed'));
    await page.click('dialog.so-askdlg [data-action="ask-back"]');
    await page.fill('#ask-all-followUp', 'What does A cost?');
    await page.click('dialog.so-askdlg [data-action="send"]');
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 10000 }).catch(() => {});
    const back = await awaitLog(page, 'you answered:', 15000);
    const asks = (await historyOf(s, id)).asks || [];
    const counter = asks[asks.length - 1].answer;
    ok(pressed === 'true' && back && counter.follow_up === 'What does A cost?' && counter.answers.q1.delegated === true,
      '"You decide" and a counter-question go back to the agent', JSON.stringify(counter));

    // A plan: sent back with a note, then approved.
    await say(page, '/plan');
    await page.waitForSelector('#chat .so-plan[data-decision="pending"] [data-action="plan-note"]', { timeout: 15000 });
    const planCard = await page.$eval('#chat .so-plan[data-decision="pending"]', (n) => n.innerText);
    await page.click('#chat .so-plan [data-action="plan-note"]');
    await page.fill('#chat .so-plan__input', 'add a test first');
    await page.click('#chat .so-plan [data-action="plan-send"]');
    const sentBack = await awaitLog(page, 'Planning on: add a test first', 15000);
    const decided = await page.$eval('#chat .so-plan', (n) => ({ decision: n.dataset.decision, text: n.innerText }));
    ok(/Add the `?404`? branch|Add the 404 branch/.test(planCard) && sentBack && decided.decision === 'keep_planning'
      && /Note: add a test first/.test(decided.text),
      'a plan is a card: Keep planning with a note sends it back, and the card says so', oneLine(JSON.stringify(decided)));
    await say(page, '/plan');
    await page.waitForSelector('#chat .so-plan[data-decision="pending"] [data-action="plan-approve"]', { timeout: 15000 });
    await page.click('#chat .so-plan[data-decision="pending"] [data-action="plan-approve"]');
    const approved = await awaitLog(page, 'The plan is approved; starting on it.', 15000);
    const approvedCards = await page.$$eval('#chat .so-plan[data-decision="approved"]', (n) => n.length);
    ok(approved && approvedCards === 1, 'and Approve lets the agent go ahead', String(approvedCards));

    // A question whose answer is the next message (Codex's code mode).
    await say(page, '/asknext');
    await openAskCard(page);
    await page.click('#chat .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg [data-action="delegate"]', { timeout: 10000 });
    for (let i = 0; i < 5; i += 1) {
      const last = await page.$('dialog.so-askdlg [data-action="send"]');
      if (last) break;
      await page.click('dialog.so-askdlg [data-action="forward"]');
    }
    await page.click('dialog.so-askdlg [data-action="send"]');
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 10000 }).catch(() => {});
    const asMessage = await page.waitForSelector('#chat .so-msg[data-role="user"] .so-msg__label', { timeout: 15000 })
      .then((n) => n.innerText(), () => '');
    ok(/Answer to a question/.test(asMessage), 'an answer that goes as the next message is shown as one', asMessage);
    await awaitChat(s, id, idle);

    // Home: the Questions card opens a chat's question in the dialog.
    await say(page, '/ask');
    const homeAsk = await openAskCard(page);
    await page.click('#homeBtn').catch(() => page.evaluate(() => { location.hash = '#home'; }));
    await page.waitForSelector('#home:not([hidden])', { timeout: 10000 });
    await page.waitForSelector('#home .so-home__questions[data-state="asking"]', { timeout: 15000 });
    await page.click('#home .so-home__q-action');
    const fromHome = await page.waitForSelector('dialog.so-askdlg .so-q-text', { timeout: 10000 }).then((n) => n.innerText(), () => '');
    ok(fromHome === 'Pick one', 'Home’s Questions card opens a chat’s question in the dialog', fromHome);

    // Two devices on one question. The second is a browser of its own: what
    // this one answered is this one's, and the other knows only the login.
    const signedIn = await s.context.storageState();
    const device = await s.browser.newContext({ viewport: { width: 1024, height: 800 }, storageState: { cookies: signedIn.cookies, origins: [] } });
    const other = await device.newPage();
    await other.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
    await other.waitForSelector('#chat:not([hidden]) .so-ask[data-state="open"]', { timeout: 20000 });
    await other.click('#chat .so-ask__open');
    await other.waitForSelector('dialog.so-askdlg .so-q-option[data-option="b"]', { timeout: 10000 });
    await page.click('dialog.so-askdlg .so-q-option[data-option="a"]');
    await page.click('dialog.so-askdlg [data-action="send"]');
    const elsewhere = await other.waitForSelector('dialog.so-askdlg .so-q-finished', { timeout: 15000 })
      .then((n) => n.innerText(), () => '');
    ok(/Answered on another device/.test(elsewhere), 'the other device’s open dialog says it was answered on another device', oneLine(elsewhere));
    await other.click('dialog.so-askdlg [data-action="next"]').catch(() => {});
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 10000 }).catch(() => {});
    await openChat(page, id);
    const otherCard = await other.waitForSelector('#chat .so-ask[data-ask="' + homeAsk + '"][data-state="elsewhere"]', { timeout: 10000 })
      .then((n) => n.innerText(), () => '');
    ok(/Answered on another device/.test(otherCard), 'and so does its card in the chat', oneLine(otherCard));

    // The second device answers after the first, its answer held by a
    // connection that was down: the server refuses it (409), and the device
    // says so instead of sending it twice.
    await say(page, '/ask');
    const late = await openAskCard(page);
    await other.waitForSelector('#chat .so-ask[data-ask="' + late + '"][data-state="open"]', { timeout: 15000 });
    await other.click('#chat .so-ask[data-ask="' + late + '"] .so-ask__open');
    await other.waitForSelector('dialog.so-askdlg .so-q-option[data-option="b"]', { timeout: 10000 });
    await other.route('**/asks/*/answer', (route) => route.abort('internetdisconnected'));
    await other.click('dialog.so-askdlg .so-q-option[data-option="b"]');
    await other.click('dialog.so-askdlg [data-action="send"]');
    await page.click('#chat .so-ask[data-ask="' + late + '"] .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg .so-q-option[data-option="a"]', { timeout: 10000 });
    await page.click('dialog.so-askdlg .so-q-option[data-option="a"]');
    await page.click('dialog.so-askdlg [data-action="send"]');
    await awaitChat(s, id, (b) => !(b.asks || []).some((a) => a.id === late));
    const conflict = other.waitForResponse((res) => /\/answer$/.test(res.url()) && res.status() === 409, { timeout: 30000 })
      .then(() => true, () => false);
    await other.unroute('**/asks/*/answer');
    await other.evaluate(() => window.dispatchEvent(new Event('online')));
    const refused = await conflict;
    const told = await other.waitForSelector('dialog.so-askdlg .so-q-finished', { timeout: 15000 }).then((n) => n.innerText(), () => '');
    const lateAsk = await askOf(s, id, late);
    ok(refused && /Answered on another device/.test(told) && lateAsk.answer.answers.q1.selected[0] === 'a',
      'an answer sent after another device’s is refused, and the device is told it was answered on the other',
      JSON.stringify({ refused, told: oneLine(told), kept: lateAsk.answer.answers.q1.selected }));
    await device.close();
    await shot(page, 'chat-ask');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-attach: files from the button, dropped on the chat and pasted as an
// image, each uploaded to a path the server chose, and the message that
// carries all three.
async function chatattach() {
  const s = await chatServer();
  const { page } = s;
  try {
    const id = await startChat(page);
    const chooser = page.waitForEvent('filechooser');
    await page.click('#chat .so-composer__attach');
    await (await chooser).setFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('a few notes\n') });
    await page.waitForSelector('#chat .so-composer__file[data-state="ready"]', { timeout: 15000 });

    // A file held over the chat and let go.
    await page.evaluate(() => {
      const data = new DataTransfer();
      data.items.add(new File(['x,y\n1,2\n'], 'table.csv', { type: 'text/csv' }));
      const target = document.querySelector('#chat .so-chat__log');
      target.dispatchEvent(new DragEvent('dragover', { dataTransfer: data, bubbles: true, cancelable: true }));
      target.dispatchEvent(new DragEvent('drop', { dataTransfer: data, bubbles: true, cancelable: true }));
    });
    // An image pasted into the field.
    await page.evaluate(() => {
      const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII='), (c) => c.charCodeAt(0));
      const data = new DataTransfer();
      data.items.add(new File([bytes], 'pasted.png', { type: 'image/png' }));
      const field = document.querySelector('#chat .so-composer__input');
      field.focus();
      field.dispatchEvent(new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true }));
    });
    const ready = await page.waitForFunction(() => {
      const files = [...document.querySelectorAll('#chat .so-composer__file')];
      return files.length === 3 && files.every((f) => f.dataset.state === 'ready')
        ? files.map((f) => f.querySelector('.so-ellipsis').textContent) : null;
    }, null, { timeout: 20000 }).then((h) => h.jsonValue(), () => null);
    ok(!!ready && ready.join(',') === 'notes.txt,table.csv,pasted.png',
      'a file from the button, one dropped on the chat and a pasted image are uploaded before sending', JSON.stringify(ready));

    await say(page, 'here are three files');
    const reply = await awaitLog(page, 'with 3 attachment(s)', 20000);
    const history = await historyOf(s, id);
    const message = (history.messages || []).find((m) => m.role === 'user' && m.text === 'here are three files');
    const files = (message && message.attachments) || [];
    const dir = s.data + '/chats/' + id + '/attachments/';
    ok(reply && files.length === 3 && files.every((f) => f.path.startsWith(dir)),
      'the message carries them at paths the server chose, in the chat’s own folder',
      JSON.stringify(files.map((f) => ({ name: f.name, path: f.path.replace(s.data, '<data>'), mime: f.mime }))));
    ok(files.some((f) => f.mime === 'image/png') && files.some((f) => /text\/csv|text\/plain/.test(f.mime)),
      'with their types', JSON.stringify(files.map((f) => f.mime)));
    const shown = await page.$$eval('#chat .so-msg[data-role="user"] .so-msg__files .so-attachment', (n) => n.map((one) => one.innerText.trim()));
    const cleared = await page.$$eval('#chat .so-composer__file', (n) => n.length);
    ok(shown.length === 3 && cleared === 0, 'the bubble lists the files, and the composer is empty again', JSON.stringify({ shown, cleared }));

    // A path the server did not hand out is refused.
    const forged = await s.context.request.post(s.url + '/api/chats/' + id + '/messages', {
      data: { client_id: 'forged-1', text: 'look', attachments: [{ name: 'passwd', path: '/etc/passwd' }] },
    });
    ok(forged.status() === 400, 'a message naming a file anywhere else is refused', String(forged.status()));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-phone: a chat at 390 × 844 with a finger: touch mode and its 48 px
// targets, no sideways scroll however wide the answer, a composer that
// stays above the keyboard, and the question dialog as the whole screen
// with options a thumb can hit.
async function chatphone() {
  const s = await chatServer({ viewport: { width: 390, height: 844 }, touch: true });
  const { page } = s;
  try {
    const id = await startChat(page);
    const mode = await page.evaluate(() => document.documentElement.classList.contains('so-touch'));
    const shut = await page.$eval('#chat .so-composer__input', (n) => n.readOnly);
    await page.click('#kbBtn');
    const typing = await page.waitForFunction(() => !document.querySelector('#chat .so-composer__input').readOnly, null,
      { timeout: 5000 }).then(() => true, () => false);
    ok(mode && shut && typing, 'a phone is in touch mode by itself, and its keyboard button opens the composer for typing',
      JSON.stringify({ mode, shut, typing }));

    await say(page, '/md 5', { button: true });
    await awaitLog(page, 'The tests in', 20000);
    await say(page, '/tools', { button: true });
    await awaitLog(page, 'Done with the tools.', 20000);
    const wide = await sideScroll(page);
    const codeScrolls = await page.$$eval('#chat .so-code__pre', (n) => n.some((one) => one.scrollWidth > one.clientWidth));
    ok(wide === 0, 'a long line, a code block and a diff do not make the page scroll sideways', wide + ' px');
    ok(codeScrolls, 'the code block scrolls inside itself instead', String(codeScrolls));

    const targets = await page.evaluate(() => {
      const box = (sel) => {
        const n = document.querySelector(sel);
        if (!n || n.hidden) return null;
        const r = n.getBoundingClientRect();
        return { w: Math.round(r.width), h: Math.round(r.height) };
      };
      return {
        send: box('#chat .so-composer__send'), attach: box('#chat .so-composer__attach'),
        model: box('#chat .so-composer__model'), speak: box('#micBtn'),
        usage: box('#chat .so-composer__usage'), copy: box('#chat .so-code__copy'),
        tool: box('#chat .so-tool > summary'),
      };
    });
    ok(Object.values(targets).every((t) => t && t.h >= 48), 'every control of the composer and a tool row is a 48 px target', JSON.stringify(targets));

    // The keyboard: the app is as tall as what can be seen (--so-app-h,
    // which keybar.js writes from the visual viewport), and the composer is
    // at its bottom, above the keyboard.
    await page.focus('#chat .so-composer__input');
    await page.evaluate(() => document.documentElement.style.setProperty('--so-app-h', '480px'));
    await wait(300);
    const raised = await page.evaluate(() => {
      const composer = document.querySelector('#chat .so-composer').getBoundingClientRect();
      const field = document.querySelector('#chat .so-composer__input').getBoundingClientRect();
      return { bottom: Math.round(composer.bottom), field: Math.round(field.bottom), top: Math.round(field.top) };
    });
    ok(raised.bottom <= 480 && raised.top > 0, 'with the keyboard up the composer is above it, in view', JSON.stringify(raised));
    await page.evaluate(() => document.documentElement.style.removeProperty('--so-app-h'));

    // The question dialog: the whole screen, one question, big options.
    await say(page, '/askmany', { button: true });
    await openAskCard(page);
    await page.click('#chat .so-ask__open');
    await page.waitForSelector('dialog.so-askdlg .so-q-option', { timeout: 10000 });
    await wait(400);
    const dialog = await page.evaluate(() => {
      const box = document.querySelector('dialog.so-askdlg .so-q-shell').getBoundingClientRect();
      const options = [...document.querySelectorAll('dialog.so-askdlg .so-q-option')].map((n) => Math.round(n.getBoundingClientRect().height));
      const read = document.querySelector('dialog.so-askdlg [data-action="read"]').getBoundingClientRect();
      return { w: Math.round(box.width), h: Math.round(box.height), vw: innerWidth, vh: innerHeight, options, read: Math.round(read.height) };
    });
    ok(dialog.w === dialog.vw && dialog.h >= dialog.vh - 1, 'the question dialog is the whole screen', JSON.stringify(dialog));
    ok(dialog.options.every((h) => h >= 64) && dialog.read >= 48, 'with options of at least 64 px and big actions', JSON.stringify(dialog));
    const dialogWide = await page.evaluate(() => {
      const scroll = document.querySelector('dialog.so-askdlg .so-q-scroll');
      return scroll.scrollWidth - scroll.clientWidth;
    });
    ok(dialogWide <= 0, 'and nothing in it scrolls sideways', dialogWide + ' px');
    await page.click('dialog.so-askdlg [data-action="delegate"]');
    for (let i = 0; i < 5; i += 1) {
      if (await page.$('dialog.so-askdlg [data-action="send"]')) break;
      await page.click('dialog.so-askdlg [data-action="forward"]');
    }
    await page.click('dialog.so-askdlg [data-action="send"]');
    const answered = await awaitLog(page, 'you answered:', 15000);
    ok(answered, 'and Send answers from the phone', String(answered));
    await shot(page, 'chat-phone');
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
    void id;
  } finally { await s.stop(); }
}

/* ------------------------------------------- what the rest reads of a chat */

// The rest of Socrates reads a chat from its journal (WP9): Home's line and
// question, the notification and the badge, the name, the spoken Status, the
// verdict on a turn, and a prepared chat's start by the clock.

// goHome puts Home on the stage the way the drawer's button does.
async function goHome(page) {
  await ensureNav(page);
  await page.click('#homeBtn');
  await page.waitForSelector('#home:not([hidden])', { timeout: 10000 });
}

// tellChat sends a chat a message through its route, as a composer on
// another device would: the page under test is wherever the scenario left
// it.
async function tellChat(s, id, text) {
  const res = await s.context.request.post(s.url + '/api/chats/' + encodeURIComponent(id) + '/messages', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), text },
  });
  return res.status();
}

// openAskOf is the id of a chat's first open question, from the server.
async function openAskOf(s, id, timeout = 15000) {
  const body = await awaitChat(s, id, (b) => (b.asks || []).length > 0, timeout);
  return ((body.asks || [])[0] || {}).id || '';
}

// homeCardOf is one chat's card on Home as it is drawn.
const homeCardOf = (page, id) => page.evaluate((one) => {
  // A card the list let go of fades out in place and is marked meanwhile.
  const card = document.querySelector('#home .so-home__card[data-id="' + one + '"]:not([data-ds-leaving])');
  if (!card) return null;
  const summary = card.querySelector('.so-home__card-summary');
  return {
    state: card.dataset.state || '',
    summary: summary && !summary.hidden ? summary.textContent : '',
    status: (card.querySelector('.so-home__card-status') || {}).textContent || '',
    answer: !card.querySelector('.so-home__answer').hidden,
    badge: (card.querySelector('.so-mark__badge') || { dataset: {} }).dataset.kind || '',
    reason: (card.querySelector('.so-home__reason') || {}).textContent || '',
    todos: [...card.querySelectorAll('.so-home__todos li')].map((li) => li.textContent),
  };
}, id);

async function awaitHomeCard(page, id, test, timeout = 20000) {
  const started = Date.now();
  let seen = null;
  while (Date.now() - started < timeout) {
    seen = await homeCardOf(page, id);
    if (seen && test(seen)) return { ok: true, ms: Date.now() - started, seen };
    await wait(150);
  }
  return { ok: false, ms: Date.now() - started, seen };
}

// chat-home: Home reads a chat from its journal. Its card says what it does
// - the call that runs, then what it said - and it is counted in Running
// while it works and in Finished once it is done unseen; a question it asks
// is the Questions card, the question's own words on the chat's card, and
// Answer opens the chat's dialog at it, at a desk and on a phone.
async function chathome() {
  const s = await chatServer();
  const { page } = s;
  try {
    const id = await startChat(page);
    await goHome(page);
    // Every session, not only the ones that need you: a chat at work does
    // not.
    await page.click('#home [data-action="seg"][data-filter="all"]');
    ok((await tellChat(s, id, '/workflow 1500')) === 200, 'the chat is sent a workflow while Home is open', '/workflow 1500');
    const working = await awaitHome(page, (h) => h.counts.running === 1
      && h.rows.some((r) => r.id === id && r.summary === 'Workflow: Ship the feature'), 15000);
    ok(working.ok, 'while it works, Home counts it in Running and its card says what runs', took(working));
    const finished = await awaitHome(page, (h) => h.counts.finished === 1 && h.counts.running === 0
      && h.rows.some((r) => r.id === id && r.summary === 'The workflow finished.'), 30000);
    ok(finished.ok, 'done and unseen, it is Finished, and its card says what it said', took(finished));

    ok((await tellChat(s, id, '/askmany')) === 200, 'then it is sent a batch of questions', '/askmany');
    const asking = await awaitHome(page, (h) => h.asking && h.counts.questions === 1
      && h.rows.some((r) => r.id === id && r.summary === 'Which database should the service use?' && r.answer), 15000);
    ok(asking.ok, 'it is the Questions card, its card has the first question’s words and an Answer', took(asking));
    const card = await homeCardOf(page, id);
    ok(card && card.state === 'asking' && /Needs an answer/.test(card.status), 'and the card says it needs an answer',
      JSON.stringify(card));
    await page.click('#home .so-home__card[data-id="' + id + '"] [data-action="answer"]');
    const asked = await page.waitForSelector('dialog.so-askdlg .so-q-text', { timeout: 10000 }).then((n) => n.innerText(), () => '');
    ok(asked === 'Which database should the service use?', 'Answer opens the chat’s own dialog at the question', asked);
    await page.click('dialog.so-askdlg [data-action="close"]');
    await page.waitForSelector('dialog.so-askdlg', { state: 'detached', timeout: 5000 }).catch(() => {});
    await shot(page, 'chat-home');

    // A phone: the same card and the same question, the dialog the whole
    // screen, nothing to scroll sideways.
    const signedIn = await s.context.storageState();
    const phone = await s.browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true,
      storageState: { cookies: signedIn.cookies, origins: [] } });
    const small = await phone.newPage();
    await small.goto(s.url + '/#home', { waitUntil: 'domcontentloaded' });
    const there = await awaitHome(small, (h) => h.shown && h.asking
      && h.rows.some((r) => r.id === id && r.summary === 'Which database should the service use?'), 20000);
    ok(there.ok && there.seen.overflow <= 1, 'on a phone Home shows the chat’s question, and does not scroll sideways',
      JSON.stringify({ ok: there.ok, overflow: there.seen && there.seen.overflow }));
    await small.click('#home .so-home__card[data-id="' + id + '"] [data-action="answer"]');
    const box = await small.waitForSelector('dialog.so-askdlg .ds-dialog__box', { timeout: 10000 })
      .then((n) => n.boundingBox(), () => null);
    ok(!!box && box.width >= 380, 'and Answer opens the question as the whole screen', JSON.stringify(box));
    await phone.close();
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-push: the page's own notification and the app's badge say what a
// chat's journal says - the question's words when it asks, with the badge
// counting it; how its turn ended with a line of its answer; the badge back
// to nothing once it is answered - nothing for a turn the person stopped,
// and a limit in the accounts' words, once. The browser is stubbed as in the
// notify scenario.
async function chatpush() {
  const s = await chatServer();
  const { page } = s;
  try {
    await page.addInitScript(() => {
      try { localStorage.setItem('socrates.notify', 'on'); } catch { /* the stub says so */ }
      // No push service: the page's own notification is the one this
      // device gets.
      delete window.PushManager;
      window.__badges = [];
      navigator.setAppBadge = (n) => { window.__badges.push(n); return Promise.resolve(); };
      navigator.clearAppBadge = () => { window.__badges.push(0); return Promise.resolve(); };
    });
    await stubNotify(page);
    await page.reload({ waitUntil: 'domcontentloaded' });
    const id = await startChat(page);
    const title = await page.$eval(rowSel(id) + ' .so-row__label', (n) => n.textContent.trim());
    await goHome(page);
    // A chat just opened is settling: what it does in its first seconds is
    // not announced (notify.js hush).
    await wait(5500);
    const notes = () => page.evaluate(() => (window.__notes || []).slice());
    const badges = () => page.evaluate(() => (window.__badges || []).slice());
    const noteAfter = async (count, timeout = 15000) => {
      const until = Date.now() + timeout;
      while (Date.now() < until) {
        const all = await notes();
        if (all.length > count) return all[all.length - 1];
        await wait(150);
      }
      return null;
    };

    let before = (await notes()).length;
    await tellChat(s, id, 'hello');
    const said = await noteAfter(before);
    ok(!!said && said.title === title && /^Finished · you said: hello/.test(said.options.body)
      && said.options.data && said.options.data.kind === 'finished',
      'a finished turn says so, with a line of the answer', JSON.stringify(said && { title: said.title, body: said.options.body }));

    before = (await notes()).length;
    await tellChat(s, id, '/ask');
    const asked = await noteAfter(before);
    ok(!!asked && asked.options.body === 'Pick one' && asked.options.data && asked.options.data.kind === 'question',
      'a question says what it asks', JSON.stringify(asked && asked.options.body));
    const counted = await page.waitForFunction(() => (window.__badges || []).slice(-1)[0] === 1, null, { timeout: 10000 })
      .then(() => true, () => false);
    ok(counted, 'and the badge counts it', JSON.stringify(await badges()));

    before = (await notes()).length;
    const ask = await openAskOf(s, id);
    const res = await s.context.request.post(s.url + '/api/chats/' + id + '/asks/' + ask + '/answer', {
      data: { client_id: 'e2e-answer', answer: { answers: { q1: { selected: ['a'] } } }, device: 'e2e' },
    });
    const done = await noteAfter(before);
    ok(res.status() === 200 && !!done && /^Finished · you answered:/.test(done.options.body),
      'once it is answered, the turn it lets go on is news of its own', JSON.stringify(done && done.options.body));
    const cleared = await page.waitForFunction(() => (window.__badges || []).slice(-1)[0] === 0, null, { timeout: 10000 })
      .then(() => true, () => false);
    ok(cleared, 'and the badge says nothing is asked', JSON.stringify(await badges()));

    // Stopped by the person: no news, heard or shown.
    const beeps = () => page.evaluate(() => (window.__beeps || []).length);
    await tellChat(s, id, '/sleep 20');
    await awaitChat(s, id, (b) => b.chat.turn_running, 10000);
    before = (await notes()).length;
    let rang = await beeps();
    await s.context.request.post(s.url + '/api/chats/' + id + '/interrupt', { data: { client_id: 'e2e-stop' } });
    await awaitChat(s, id, idle, 15000);
    await wait(1500);
    ok((await notes()).length === before && (await beeps()) === rang, 'a turn the person stopped is no notification and no chime',
      JSON.stringify({ notes: (await notes()).length - before, chimes: (await beeps()) - rang }));

    // A limit is the accounts' news: when it renews and on which account, as
    // its push says it - once, and not the turn's end as well.
    before = (await notes()).length;
    rang = await beeps();
    await tellChat(s, id, '/limit');
    const limited = await noteAfter(before);
    ok(!!limited && /^Limit reached — renews in .+ \(.+\)$/.test(limited.options.body)
      && limited.options.data && limited.options.data.kind === 'finished',
      'a limit says when it renews and on which account', JSON.stringify(limited && limited.options.body));
    await wait(1500);
    ok((await notes()).length === before + 1 && (await beeps()) === rang + 1, 'and is said once, with one chime',
      JSON.stringify({ notes: (await notes()).length - before, chimes: (await beeps()) - rang }));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-title: a chat names itself from its first message, through the title
// model, in the sidebar and the header, with no reload.
async function chattitle() {
  const want = 'Config loader rename';
  const stub = await openRouterStub({ text: '"' + want + '."', delay: 800 });
  const s = await chatServer();
  const { page } = s;
  try {
    await pointGateway(s, stub);
    const id = await startChat(page);
    const before = await page.$eval(rowSel(id) + ' .so-row__label', (n) => n.textContent.trim());
    ok(before.startsWith('Scripted agent'), 'it starts life under the placeholder name', before);
    await say(page, 'Please rename the config loader');
    const named = await page.waitForFunction((arg) => {
      const node = document.querySelector(arg.sel);
      const head = document.getElementById('sessionTitle');
      return !!node && node.textContent.trim() === arg.want && !!head && head.textContent.trim() === arg.want;
    }, { sel: rowSel(id) + ' .so-row__label', want }, { timeout: 30000 }).then(() => true, () => false);
    ok(named, 'the row and the header take the name, with no reload', want);
    const calls = stub.calls.filter((c) => String(c.path).includes('chat/completions'));
    const prompt = calls.length ? String(((calls[0].json || {}).messages || []).slice(-1)[0].content || '') : '';
    ok(calls.length === 1 && prompt.includes('First message:\nPlease rename the config loader'),
      'it was named after its first message, once', oneLine(prompt).slice(0, 160));
    await say(page, 'And the tests');
    await awaitLog(page, 'you said: And the tests', 15000);
    await wait(800);
    ok(stub.calls.filter((c) => String(c.path).includes('chat/completions')).length === 1,
      'a later message does not rename it', String(stub.calls.length));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// chat-status: Status is there for a chat and says it from its journal, on
// the chat's own line and out loud.
async function chatstatus() {
  const words = 'The agent ran echo and said hello.';
  const stub = await openRouterStub({ text: words });
  const s = await chatServer({ args: ['--autoplay-policy=no-user-gesture-required'] });
  const { page } = s;
  try {
    await pointGateway(s, stub);
    const spoken = await stubSpeech(s.context);
    const id = await startChat(page);
    await say(page, '/tool');
    await awaitLog(page, 'line 3', 15000);
    await awaitChat(s, id, idle);
    const button = await page.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 10000 }).then(() => true, () => false);
    ok(button, 'a chat has the Status button', String(button));
    await page.click('#statusBtn');
    const line = await page.waitForFunction((want) => {
      const box = document.querySelector('#chat .so-chat__dock .so-ticker');
      return !!box && !box.hidden && box.textContent.includes(want);
    }, words, { timeout: 30000 }).then(() => true, () => false);
    ok(line, 'what the model said is the line in the chat’s dock', words);
    // The whole answer is the notice, in the dock too: the terminal's is over
    // its pane, which a chat hides.
    const told = await page.waitForFunction((want) => {
      const box = document.querySelector('#chat .so-chat__dock .so-notice');
      if (!box || box.hidden || !box.textContent.includes(want)) return null;
      const info = box.querySelector('.so-info');
      return { height: box.getBoundingClientRect().height, tip: info ? info.getAttribute('data-tip') : '' };
    }, words, { timeout: 10000 }).then((h) => h.jsonValue(), () => null);
    ok(!!told && told.height > 0 && /state idle/.test(told.tip),
      'and the whole answer is the notice in the dock, its details behind the “i”', JSON.stringify(told));
    // The picture once the notice and the line have come in.
    await wait(800);
    await shot(page, 'chat-status');
    const read = await (async () => {
      const until = Date.now() + 15000;
      while (Date.now() < until && !spoken.includes(words)) await wait(150);
      return spoken.includes(words);
    })();
    ok(read, 'and it is read out loud', JSON.stringify(spoken));
    const calls = stub.calls.filter((c) => String(c.path).includes('chat/completions'));
    const prompt = calls.map((c) => String(((c.json || {}).messages || []).slice(-1)[0].content || ''))
      .find((p) => p.startsWith('You are told what a chat with')) || '';
    ok(prompt.includes('a chat with Scripted agent') && prompt.includes('/tool') && prompt.includes('Running echo hello'),
      'the model was told the chat from its journal: the message and the call', oneLine(prompt).slice(0, 200));

    // A phone: the button in the bar, the line in the dock, nothing to
    // scroll sideways.
    const signedIn = await s.context.storageState();
    const phone = await s.browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true,
      storageState: { cookies: signedIn.cookies, origins: [] } });
    await phone.route('**/api/voice/speak', (route) => route.fulfill({ status: 200, contentType: 'audio/wav', body: silentWav() }));
    const small = await phone.newPage();
    await small.goto(s.url + '/#open=' + id, { waitUntil: 'domcontentloaded' });
    await chatUp(small);
    await small.waitForSelector('#statusBtn:not([hidden]):not([disabled])', { timeout: 10000 });
    await small.tap('#statusBtn');
    const onPhone = await small.waitForFunction((want) => {
      const box = document.querySelector('#chat .so-chat__dock .so-ticker');
      const note = document.querySelector('#chat .so-chat__dock .so-notice');
      if (!box || box.hidden || !box.textContent.includes(want)) return null;
      if (!note || note.hidden || !note.textContent.includes(want)) return null;
      const r = box.getBoundingClientRect();
      const n = note.getBoundingClientRect();
      const text = note.querySelector('.ds-alert__text');
      const root = document.scrollingElement;
      return { left: r.left, right: r.right, noteLeft: n.left, noteRight: n.right,
        cut: text ? text.scrollWidth - text.clientWidth : -1, overflow: Math.max(root.scrollWidth - root.clientWidth, 0) };
    }, words, { timeout: 30000 }).then((h) => h.jsonValue(), () => null);
    ok(!!onPhone && onPhone.left >= 0 && onPhone.right <= 390 && onPhone.overflow <= 1,
      'on a phone the line is in the dock, inside the screen', JSON.stringify(onPhone));
    ok(!!onPhone && onPhone.noteLeft >= 0 && onPhone.noteRight <= 390 && onPhone.cut <= 1,
      'and so is the whole answer, not cut', JSON.stringify(onPhone));
    await wait(800);
    await shot(small, 'chat-status-phone');
    await phone.close();
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// chat-outcome: with outcomes on, a chat's ended turn is judged from its
// journal: the hourglass while it is read, then the verdict on the row and
// on Home, and the notification that waited for it says it. A turn the
// person stopped is not judged; one a limit stopped is, and is said as the
// limit.
async function chatoutcome() {
  const judged = [];
  const stub = await openRouterStub({
    delay: 600,
    answer: (json) => {
      const system = String(((json.messages || [])[0] || {}).content || '');
      if (!system.startsWith('You judge the turn a coding agent has just ended')) return 'Echo run';
      judged.push(String(((json.messages || []).slice(-1)[0] || {}).content || ''));
      return JSON.stringify({ outcome: { kind: 'partial', reason: 'The echo ran; the tests are not written.', todos: ['Write the tests'] } });
    },
  });
  const s = await chatServer();
  const { page } = s;
  try {
    await page.addInitScript(() => {
      try { localStorage.setItem('socrates.notify', 'on'); } catch { /* the stub says so */ }
      delete window.PushManager;
    });
    await stubNotify(page);
    const saved = await s.context.request.put(s.url + '/api/settings', {
      data: { settings: { openrouter: { api_key: 'e2e-key', base_url: stub.url, outcomes: true } } },
    });
    ok(saved.ok(), 'the gateway is the stub and the outcomes are on', saved.status());
    await page.reload({ waitUntil: 'domcontentloaded' });
    const id = await startChat(page);
    await goHome(page);
    // A chat just opened is settling (notify.js hush).
    await wait(5500);
    const before = (await page.evaluate(() => (window.__notes || []).length));
    await tellChat(s, id, '/tool');
    const glass = await awaitRow(page, id, (r) => !!r.glass, 15000);
    ok(glass.ok, 'while the turn is judged, its row has the hourglass', took(glass));
    const verdict = await awaitHomeCard(page, id, (c) => c.badge === 'partial' && c.todos.join() === 'Write the tests', 20000);
    ok(verdict.ok && /Partly done/.test(verdict.seen.status) && /tests are not written/.test(verdict.seen.reason),
      'then the verdict is on Home: partly done, why, and what is open', JSON.stringify(verdict.seen));
    const row = await awaitRow(page, id, (r) => !r.glass, 5000);
    ok(row.ok, 'and the hourglass is gone from the row', took(row));
    ok(judged.length === 1 && judged[0].includes('/tool') && judged[0].includes('Running echo hello'),
      'the judge was told the turn from the journal', oneLine(judged[0] || '').slice(0, 200));
    const note = await page.waitForFunction((count) => {
      const all = window.__notes || [];
      return all.length > count ? all[all.length - 1].options.body : null;
    }, before, { timeout: 15000 }).then((h) => h.jsonValue(), () => '');
    ok(/^Partly done · 1 open: Write the tests/.test(note), 'the notification waited for the verdict and says it', note);
    await shot(page, 'chat-outcome');

    // A turn the person stopped is no news: it is not judged, and nothing
    // waits for a verdict to say it.
    const judgedBefore = judged.length;
    const notesBefore = await page.evaluate(() => (window.__notes || []).length);
    await tellChat(s, id, '/sleep 20');
    await awaitChat(s, id, (b) => b.chat.turn_running, 10000);
    await s.context.request.post(s.url + '/api/chats/' + id + '/interrupt', { data: { client_id: 'e2e-stop' } });
    await awaitChat(s, id, idle, 15000);
    await wait(2500);
    const notesAfter = await page.evaluate(() => (window.__notes || []).length);
    ok(judged.length === judgedBefore && notesAfter === notesBefore,
      'a turn the person stopped is not judged and is no notification',
      JSON.stringify({ judged: judged.length - judgedBefore, notes: notesAfter - notesBefore }));

    // A turn a limit stopped is judged - how far it got is worth knowing -
    // and its notification is the accounts' words, not the verdict's.
    await tellChat(s, id, '/limit');
    const limitVerdict = await awaitHomeCard(page, id, (c) => c.badge === 'partial', 20000);
    ok(limitVerdict.ok && judged.length === judgedBefore + 1, 'a turn a limit stopped is judged',
      JSON.stringify({ seen: limitVerdict.seen, judged: judged.length - judgedBefore }));
    const limitNote = await page.waitForFunction((count) => {
      const all = window.__notes || [];
      return all.length > count ? all[all.length - 1].options.body : null;
    }, notesAfter, { timeout: 15000 }).then((h) => h.jsonValue(), () => '');
    await wait(1500);
    const notesLimit = await page.evaluate(() => (window.__notes || []).length);
    ok(/^Limit reached — renews in /.test(limitNote) && notesLimit === notesAfter + 1,
      'and its one notification says the limit', JSON.stringify({ note: limitNote, notes: notesLimit - notesAfter }));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally {
    await s.stop();
    await stub.close();
  }
}

// chat-prestart: a prepared chat started by the clock ("Start
// automatically"): its draft is the first message, sent once, and the panel
// that was on screen turns into the chat.
async function chatprestart() {
  const s = await chatServer();
  const { page } = s;
  try {
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="fake"]');
    await page.click('#nsPrestart');
    await page.click('#nsStart');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await page.waitForSelector('#prestart:not([hidden]) #psPrompt', { timeout: 15000 });
    const id = await page.evaluate(() => location.hash.slice(1));
    await page.waitForFunction(() => !document.getElementById('psPrompt').disabled, null, { timeout: 10000 });
    await page.fill('#psPrompt', 'start this by itself');
    await page.waitForFunction(() => /^Saved$/.test(document.getElementById('psSave').textContent.trim()), null, { timeout: 10000 }).catch(() => {});
    const at = Date.now() + 4000;
    const set = await s.context.request.patch(s.url + '/api/sessions/' + id + '/prestart', { data: { start_at: at } });
    ok(set.ok(), 'it is set to start by itself in a few seconds', set.status());
    const started = await chatUp(page, 40000).then(() => true, () => false);
    const first = await awaitLog(page, 'you said: start this by itself', 20000);
    const history = await historyOf(s, id);
    const row = await sessionRow(s, id);
    ok(started && first && userTexts(history).join('|') === 'start this by itself' && row.kind === 'chat' && row.state === 'running',
      'the clock starts it: the panel is the chat, its draft the first message, once', JSON.stringify({ started, first, users: userTexts(history), state: row.state }));
    const draft = await s.context.request.get(s.url + '/api/sessions/' + id + '/draft').then((r) => r.json());
    ok(draft.text === '', 'and the draft is empty afterwards', JSON.stringify(draft.text));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-ultracode: Ultracode is Claude Code's, a chat's as a terminal's: the
// sheet offers it for Claude Code and not for another agent, and a Claude
// chat's ⋮ switches it, where another chat's has no such item.
async function chatultracode() {
  const s = await chatServer();
  const { page } = s;
  try {
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="claude"]');
    const forClaude = await page.$eval('#nsUltracodeField', (n) => !n.hidden);
    await page.click('#newSessionSheet .so-settings__program[data-value="fake"]');
    const forFake = await page.$eval('#nsUltracodeField', (n) => !n.hidden);
    ok(forClaude && !forFake, 'the sheet offers Ultracode for Claude Code, and not for another agent',
      JSON.stringify({ forClaude, forFake }));
    await page.keyboard.press('Escape');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 10000 }).catch(() => {});
    const fake = await startChat(page);
    const made = await s.context.request.post(s.url + '/api/chats', {
      data: { client_id: 'e2e-claude-chat', harness: 'claude', workdir_mode: 'dynamic' },
    });
    const claude = (await made.json()).session.id;
    ok(made.status() === 201, 'a Claude Code chat is made', made.status());
    await page.waitForSelector(rowSel(claude), { state: 'attached', timeout: 15000 });
    const itemOf = async (id) => {
      await ensureNav(page);
      await page.waitForSelector(rowSel(id), { timeout: 15000 });
      await page.$eval(rowSel(id), (n) => n.scrollIntoView({ block: 'center' }));
      // A list that is drawn again under the tap (a new row arriving) puts
      // the menu away; it is opened again.
      for (let tries = 0; tries < 3; tries += 1) {
        await page.click(rowSel(id) + ' .so-row__more', { force: true });
        if (await page.waitForSelector(MENU + ' .ds-menu__item', { timeout: 3000 }).then(() => true, () => false)) break;
      }
      const item = await page.$eval(MENU, (menu) => {
        const node = menu.querySelector('[data-action="ultracode"]');
        return node ? { role: node.getAttribute('role'), checked: node.getAttribute('aria-checked') } : null;
      });
      return item;
    };
    const closeMenu = async () => {
      await page.keyboard.press('Escape');
      await page.waitForSelector(MENU, { state: 'detached', timeout: 5000 }).catch(() => {});
    };
    const fakeItem = await itemOf(fake);
    await closeMenu();
    const before = await itemOf(claude);
    ok(!fakeItem && before && before.role === 'menuitemcheckbox' && before.checked === 'false',
      'a Claude chat’s ⋮ has Ultracode, off, and another chat’s has none', JSON.stringify({ fakeItem, before }));
    await page.click(MENU + ' [data-action="ultracode"]');
    const toast = await page.waitForFunction(() => [...document.querySelectorAll('#toasts .ds-toast, #toasts [role="status"]')]
      .some((n) => /Ultracode is on from the next message/.test(n.textContent)), null, { timeout: 10000 }).then(() => true, () => false);
    const row = await sessionRow(s, claude);
    await page.waitForSelector(MENU, { state: 'detached', timeout: 5000 }).catch(() => {});
    const after = await itemOf(claude);
    await closeMenu();
    ok(toast && row.options && row.options.ultracode === true && after && after.checked === 'true',
      'switched on, it says from when, and the chat keeps it', JSON.stringify({ toast, options: row.options, after }));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

/* -------------------------------------------------------------- accounts */

// fakeCodexBin builds the scripted Codex app-server (e2e/fakebin/fakecodex),
// whose device-code sign-in a Codex account goes through.
let fakeCodexPath = '';
function fakeCodexBin() {
  if (fakeCodexPath) return fakeCodexPath;
  const dir = scratchDir('socrates-e2e-fakecodex-');
  fakeCodexPath = join(dir, 'codex');
  execFileSync('go', ['build', '-o', fakeCodexPath, './e2e/fakebin/fakecodex'], { cwd: REPO });
  return fakeCodexPath;
}

async function accountsOf(s) {
  const res = await s.context.request.get(s.url + '/api/accounts');
  return (await res.json()).accounts || [];
}

const accountRowSel = (id) => '#accountList .so-account[data-id="' + id + '"]';

// filesHolding is every file under dir whose bytes contain needle.
function filesHolding(dir, needle) {
  const out = [];
  const walk = (at) => {
    let entries = [];
    try { entries = readdirSync(at, { withFileTypes: true }); } catch { return; }
    for (const entry of entries) {
      const path = join(at, entry.name);
      if (entry.isDirectory()) walk(path);
      else if (entry.isFile()) {
        try { if (readFileSync(path).includes(needle)) out.push(path); } catch { /* a socket or gone */ }
      }
    }
  };
  walk(dir);
  return out;
}

// accounts: the dashboard's Accounts card - an API-key account added, a
// Codex subscription signed in by its device code against the scripted
// app-server, a rename, the order, a delete refused while a chat runs on the
// account and taken after, and the card on a phone.
async function accountsscenario() {
  const key = 'sk-e2e-' + 'k'.repeat(24);
  const s = await start({
    viewport: { width: 1280, height: 900 },
    env: { ...FAKE_AGENT, SOCRATES_CODEX_BIN: fakeCodexBin(), FAKECODEX_LOGIN_MS: '1500' },
  });
  const { page } = s;
  try {
    await setup(page, s.url);
    await page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#accountList .so-account-group[data-agent="codex"]', { timeout: 20000 });
    const groups = await page.$$eval('#accountList .so-account-group', (n) => n.map((g) => g.dataset.agent));
    ok(['claude', 'codex', 'opencode'].every((a) => groups.includes(a)), 'the card has a group per agent', groups.join(','));
    const profilesGone = await page.$('#profileList');
    ok(!profilesGone, 'and the Profiles card is gone', String(!profilesGone));

    // An API key, for the scripted agent.
    await page.click('.so-account-group[data-agent="fake"] .ds-add-row');
    await page.waitForSelector('dialog.ds-dialog .so-account-form', { timeout: 5000 });
    await page.click('dialog.ds-dialog .ds-chip:has(input[value="api_key"])');
    await page.fill('#accountLabel', 'Key one');
    await page.fill('#accountKey', key);
    await page.click('dialog.ds-dialog .ds-btn--primary');
    await page.waitForSelector('dialog.ds-dialog', { state: 'detached', timeout: 15000 });
    let list = await accountsOf(s);
    const keyed = list.find((a) => a.label === 'Key one');
    ok(!!keyed && keyed.kind === 'api_key' && keyed.state === 'signed_in', 'an API-key account is added and usable',
      JSON.stringify(keyed));
    await page.waitForSelector(accountRowSel(keyed.id) + ' [data-state="signed_in"]', { timeout: 10000 });
    const raw = await (await s.context.request.get(s.url + '/api/accounts')).text();
    ok(!raw.includes(key), 'the list never carries the key', String(raw.includes(key)));
    const html = await page.content();
    ok(!html.includes(key), 'nor does the page', String(html.includes(key)));
    const holding = filesHolding(s.data, key);
    ok(holding.length === 0, 'and no file in the data directory holds it in the clear', holding.join(',') || 'none');

    // A Codex subscription, signed in by device code.
    await page.click('.so-account-group[data-agent="codex"] .ds-add-row');
    await page.waitForSelector('dialog.ds-dialog .so-account-form', { timeout: 5000 });
    await page.fill('#accountLabel', 'Codex work');
    await page.click('dialog.ds-dialog .ds-btn--primary');
    const coded = await page.waitForSelector('dialog.ds-dialog .so-signin__code', { timeout: 20000 }).then(() => true, () => false);
    const flow = coded ? await page.evaluate(() => ({
      code: document.querySelector('dialog.ds-dialog .so-signin__value').textContent,
      url: document.querySelector('dialog.ds-dialog a[href]')?.href || '',
      waiting: !!document.querySelector('dialog.ds-dialog .so-signin__wait'),
    })) : {};
    ok(coded && flow.code && /^https?:/.test(flow.url) && flow.waiting,
      'the sign-in shows the code to type, the page to type it on, and that it waits', JSON.stringify(flow));
    await shot(page, 'accounts-device-code');
    const closed = await page.waitForSelector('dialog.ds-dialog', { state: 'detached', timeout: 20000 }).then(() => true, () => false);
    list = await accountsOf(s);
    const codex = list.find((a) => a.label === 'Codex work');
    ok(closed && codex && codex.state === 'signed_in' && codex.email === 'device@example.com',
      'once the code is entered there the account is signed in, as the address Codex reports', JSON.stringify(codex));
    await page.waitForSelector(accountRowSel(codex.id) + ' [data-state="signed_in"]', { timeout: 10000 });
    const meters = await page.$$eval(accountRowSel(codex.id) + ' .so-limits__row', (n) => n.map((r) => r.textContent));
    ok(meters.length === 2, 'and its five hours and its week are shown', meters.join(' | '));

    // Rename.
    await page.click(accountRowSel(keyed.id) + ' [data-action="rename"]');
    await page.waitForSelector('dialog.ds-dialog .ds-input', { timeout: 5000 });
    await page.fill('dialog.ds-dialog .ds-input', 'Renamed key');
    await page.click('dialog.ds-dialog .ds-btn--primary');
    await page.waitForFunction((id) => document.querySelector('#accountList .so-account[data-id="' + id + '"] .so-account__name')?.textContent.includes('Renamed key'),
      keyed.id, { timeout: 10000 });
    list = await accountsOf(s);
    ok(list.find((a) => a.id === keyed.id).label === 'Renamed key', 'a rename is saved', list.map((a) => a.label).join(','));

    // The order: a second scripted account, moved up by its grip.
    const second = await (await s.context.request.post(s.url + '/api/accounts',
      { data: { agent: 'fake', kind: 'subscription', label: 'Second' } })).json();
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector(accountRowSel(second.account.id), { timeout: 15000 });
    const fakeOrder = async () => (await accountsOf(s)).filter((a) => a.agent === 'fake').map((a) => a.label).join(',');
    ok(await fakeOrder() === 'Renamed key,Second', 'a new account goes at the end', await fakeOrder());
    await page.focus(accountRowSel(second.account.id) + ' .so-grip');
    await page.keyboard.press('ArrowUp');
    let moved = '';
    for (let i = 0; i < 40 && moved !== 'Second,Renamed key'; i++) { await wait(100); moved = await fakeOrder(); }
    ok(moved === 'Second,Renamed key', 'the grip moves an account up, and the order is saved', moved);

    // Delete: refused while a chat runs on the account, taken after.
    const chat = await (await s.context.request.post(s.url + '/api/chats',
      { data: { client_id: 'e2e-accounts-chat', harness: 'fake', account: keyed.id, workdir_mode: 'dynamic' } })).json();
    ok(chat.session && chat.session.id, 'a chat runs on the renamed account', JSON.stringify(chat).slice(0, 160));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector(accountRowSel(keyed.id), { timeout: 15000 });
    await page.click(accountRowSel(keyed.id) + ' [data-action="delete"]');
    await page.waitForSelector('dialog.ds-dialog .ds-btn--primary', { timeout: 5000 });
    await page.click('dialog.ds-dialog .ds-btn--primary');
    const toastSaid = await page.waitForFunction(() => [...document.querySelectorAll('.ds-toast, .ds-snackbar, [role="status"]')]
      .some((n) => /chat/i.test(n.textContent) && /run|use/i.test(n.textContent)), null, { timeout: 8000 }).then(() => true, () => false);
    list = await accountsOf(s);
    ok(list.some((a) => a.id === keyed.id), 'an account a chat runs on is not deleted', String(list.some((a) => a.id === keyed.id)));
    ok(toastSaid, 'and the page says why', String(toastSaid));
    await s.context.request.delete(s.url + '/api/sessions/' + encodeURIComponent(chat.session.id));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector(accountRowSel(keyed.id), { timeout: 15000 });
    await page.click(accountRowSel(keyed.id) + ' [data-action="delete"]');
    await page.waitForSelector('dialog.ds-dialog .ds-btn--primary', { timeout: 5000 });
    await page.click('dialog.ds-dialog .ds-btn--primary');
    await page.waitForSelector(accountRowSel(keyed.id), { state: 'detached', timeout: 10000 });
    list = await accountsOf(s);
    ok(!list.some((a) => a.id === keyed.id), 'with no chat on it, it is deleted', list.map((a) => a.label).join(','));
    await shot(page, 'accounts-admin');

    // The card on a phone, in touch mode.
    await page.setViewportSize({ width: 390, height: 844 });
    // Touch mode is this device's choice, read when the page loads: the
    // same address with another hash would not load it again.
    await page.evaluate(() => localStorage.setItem('socrates.touch', 'on'));
    await page.goto(s.url + '/admin#accountsCard', { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector(accountRowSel(codex.id), { timeout: 15000 });
    await wait(300);
    const side = await page.evaluate(() => Math.max(document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth, 0));
    ok(side === 0, 'on a phone the card does not scroll sideways', side + ' px');
    const actions = await page.$$eval(accountRowSel(codex.id) + ' .so-account__actions .ds-btn', (n) => n.map((b) => {
      const r = b.getBoundingClientRect();
      return { w: Math.round(r.width), h: Math.round(r.height), label: b.getAttribute('aria-label') || b.textContent };
    }));
    ok(actions.length >= 3 && actions.every((a) => a.label), 'its actions are icons with a name each', JSON.stringify(actions));
    ok(actions.every((a) => a.h >= 48 && a.w >= 48), 'in touch mode each is a finger’s target', JSON.stringify(actions));
    await shot(page, 'accounts-phone');
    // The refused delete is a 409 the browser reports by itself.
    const refusal = /409 \(Conflict\) @ .*\/api\/accounts\/a-/;
    ok(unexpected(s.errors, refusal).length === 0, 'no console errors', unexpected(s.errors, refusal).join(' | ') || '0');
  } finally { await s.stop(); }
}

// chat-account: a chat started on an account from the sheet, switched from
// the composer's account picker while idle and while a turn runs, the line
// each switch leaves, a limit's card and its Continue with, the automatic
// switch from the chat's menu, and the picker on a phone.
async function chataccount() {
  const s = await chatServer();
  const { page } = s;
  const post = async (path, data) => (await s.context.request.post(s.url + path, { data })).json();
  try {
    const alpha = (await post('/api/accounts', { agent: 'fake', kind: 'subscription', label: 'Alpha' })).account;
    const beta = (await post('/api/accounts', { agent: 'fake', kind: 'api_key', label: 'Beta', key: 'sk-beta-0123456789' })).account;
    ok(alpha && beta && beta.state === 'signed_in', 'two accounts of the scripted agent', JSON.stringify([alpha, beta]).slice(0, 200));

    // The sheet: agent, folder, model, account.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await ensureNav(page);
    await page.click('#newSession');
    await page.waitForSelector('#newSessionSheet[open]', { timeout: 15000 });
    await page.click('#newSessionSheet .so-settings__program[data-value="fake"]');
    await page.waitForSelector('#nsAccountField:not([hidden]) .so-drop__field', { timeout: 10000 });
    const order = await page.evaluate(() => [...document.querySelectorAll('#nsForm .so-settings > .ds-field:not([hidden])')].map((n) => n.id));
    const at = (id) => order.indexOf(id);
    ok(at('nsHarnessField') < at('nsDirField') && at('nsDirField') < at('nsModelField') && at('nsModelField') < at('nsAccountField'),
      'the sheet asks for the agent, the folder, the model, then the account', order.join(','));
    const auto = await page.$eval('#nsAutoSwitch', (n) => !n.checked && !n.closest('[hidden]'));
    ok(auto, 'and offers the automatic switch, off', String(auto));
    await page.click('#nsAccount .so-drop__field');
    await page.waitForSelector('#nsAccount .ds-combo__option[data-value="' + alpha.id + '"]', { timeout: 5000 });
    const offered = await page.$$eval('#nsAccount .ds-combo__option .ds-combo__name', (n) => n.map((x) => x.textContent));
    ok(offered.join(',') === 'Alpha,Beta', 'the account list is the agent’s accounts in order', offered.join(','));
    await page.click('#nsAccount .ds-combo__option[data-value="' + alpha.id + '"]');
    await shot(page, 'chat-account-sheet');
    await page.click('#nsStart');
    await page.waitForSelector('#newSessionSheet[open]', { state: 'detached', timeout: 30000 });
    await chatUp(page);
    const id = await page.evaluate(() => location.hash.slice(1));
    const row = await sessionRow(s, id);
    ok(row.options && row.options.account === alpha.id, 'the chat is made on the account chosen', JSON.stringify(row.options));
    await page.waitForSelector('#chat .so-composer__account:not([hidden]) .so-composer__account-btn', { timeout: 10000 });
    const face = await page.$eval('#chat .so-composer__account-text', (n) => n.textContent);
    ok(face === 'Alpha', 'the composer names the account', face);
    await say(page, '/account');
    ok(await awaitLog(page, 'account ' + alpha.id + ', no key'), 'and the agent runs on it', alpha.id);

    // Switched while idle.
    await page.click('#chat .so-composer__account-btn');
    await page.waitForSelector('.so-account-menu:not([hidden]) [data-account="' + beta.id + '"]', { timeout: 5000 });
    const items = await page.$$eval('.so-account-menu [data-account]', (n) => n.map((x) => x.textContent));
    ok(items.length === 2 && items.some((t) => /Alpha/.test(t)) && items.some((t) => /Beta/.test(t)),
      'the picker lists the accounts with their usage', items.join(' | '));
    await page.click('.so-account-menu [data-account="' + beta.id + '"]');
    ok(await awaitLog(page, 'Switched from Alpha to Beta'), 'a switch leaves a line in the conversation', '');
    await say(page, '/account');
    ok(await awaitLog(page, 'account ' + beta.id + ', a key of 18 characters'), 'and the agent runs on the new account, with its key', beta.id);
    const faceB = await page.$eval('#chat .so-composer__account-text', (n) => n.textContent);
    ok(faceB === 'Beta', 'the composer follows', faceB);

    // Switched while a turn runs: after this turn.
    await say(page, '/sleep 3');
    await page.waitForSelector('#chat .so-composer__stop:not([hidden])', { timeout: 10000 });
    await page.click('#chat .so-composer__account-btn');
    await page.click('.so-account-menu [data-account="' + alpha.id + '"]');
    await page.waitForSelector('dialog.ds-dialog [data-when="after_turn"]', { timeout: 5000 });
    await shot(page, 'chat-account-when');
    await page.click('dialog.ds-dialog [data-when="after_turn"]');
    const pending = await page.waitForFunction(() => /Beta → Alpha/.test(document.querySelector('#chat .so-composer__account-text')?.textContent || ''),
      null, { timeout: 8000 }).then(() => true, () => false);
    ok(pending, 'the composer says the switch waits for the turn', String(pending));
    ok(await awaitLog(page, 'slept', 15000), 'the turn finishes on Beta', '');
    ok(await awaitLog(page, 'Switched from Beta to Alpha', 15000), 'and then the chat moves to Alpha', '');

    // A limit, and Continue with.
    await say(page, '/limit');
    await page.waitForSelector('#chat [data-kind="limit"] [data-action="continue-with"]', { timeout: 15000 });
    const card = await page.$eval('#chat [data-kind="limit"]', (n) => n.innerText);
    ok(/Limit reached — renews in/.test(card) && /Continue with Beta/.test(card), 'the limit card says when it renews and offers Beta',
      oneLine(card));
    await shot(page, 'chat-account-limit');
    await page.click('#chat [data-kind="limit"] [data-action="continue-with"][data-account="' + beta.id + '"]');
    ok(await awaitLog(page, 'Switched from Alpha to Beta', 15000), 'Continue with switches', '');
    ok(await awaitLog(page, 'you said: ' + 'Continue where you left off.', 15000), 'and the new account is told to go on', '');
    const gone = await page.$('#chat [data-kind="limit"] [data-action="continue-with"]');
    ok(!gone, 'and the card offers nothing more', String(!gone));

    // The automatic switch, from the chat's menu.
    const gamma = (await post('/api/accounts', { agent: 'fake', kind: 'subscription', label: 'Gamma' })).account;
    await rowMenu(page);
    await page.waitForSelector('#sessionMenu [data-action="auto-switch"][aria-checked="false"]', { timeout: 5000 });
    await page.click('#sessionMenu [data-action="auto-switch"]');
    const on = await (async () => {
      for (let i = 0; i < 40; i += 1) {
        const r = await sessionRow(s, id);
        if (r.options && r.options.auto_switch) return true;
        await wait(100);
      }
      return false;
    })();
    ok(on, 'the menu turns the automatic switch on', String(on));
    const continues = () => page.evaluate(() => (document.querySelector('#chat .so-chat__log')?.innerText.match(/Continue where you left off\./g) || []).length);
    const before = await continues();
    await say(page, '/limit');
    ok(await awaitLog(page, 'Switched from Beta to Gamma', 20000), 'at the next limit the chat moves by itself to the account with the most left', gamma.id);
    // The message and the agent's "you said:" of it.
    const told = await page.waitForFunction((was) => (document.querySelector('#chat .so-chat__log')?.innerText.match(/Continue where you left off\./g) || []).length >= was + 2,
      before, { timeout: 15000 }).then(() => true, () => false);
    ok(told, 'and the turn is sent again there', String(told));

    // The picker on a phone, in touch mode.
    await page.setViewportSize({ width: 390, height: 844 });
    await option(page, 'touch');
    await closeOptions(page);
    await page.waitForSelector('.ds-toast--shown', { state: 'detached', timeout: 15000 }).catch(() => {});
    await wait(400);
    const phone = await page.evaluate(() => {
      const b = document.querySelector('#chat .so-composer__account-btn');
      const r = b ? b.getBoundingClientRect() : null;
      const root = document.scrollingElement;
      return {
        shown: !!r && r.width > 0, h: r ? Math.round(r.height) : 0, right: r ? Math.round(r.right) : 0, vw: innerWidth,
        side: Math.max(root.scrollWidth - root.clientWidth, 0), face: b ? b.textContent : '',
      };
    });
    ok(phone.shown && phone.h >= 48 && phone.right <= phone.vw && phone.side === 0,
      'on a phone the picker is a thumb\u2019s target in the composer, and nothing scrolls sideways', JSON.stringify(phone));
    await page.click('#chat .so-composer__account-btn');
    await page.waitForSelector('.so-account-menu:not([hidden]) [data-account="' + alpha.id + '"]', { timeout: 5000 });
    await wait(400);
    const menuBox = await page.$eval('.so-account-menu', (n) => {
      const r = n.getBoundingClientRect();
      const first = n.querySelector('[data-account]').getBoundingClientRect();
      return { l: Math.round(r.left), r: Math.round(r.right), vw: innerWidth, top: Math.round(r.top), firstTop: Math.round(first.top), scrolled: n.scrollTop };
    });
    ok(menuBox.l >= 0 && menuBox.r <= menuBox.vw && menuBox.top >= 0 && menuBox.firstTop >= menuBox.top && menuBox.scrolled === 0,
      'and its menu is on the screen, its first account in view', JSON.stringify(menuBox));
    await shot(page, 'chat-account-phone');
    await page.click('.so-account-menu [data-account="' + alpha.id + '"]');
    ok(await awaitLog(page, 'Switched from Gamma to Alpha', 15000), 'a tap there switches too', '');

    // Switched while a turn runs: now. The turn stops, the new account
    // carries it on, and the line does not call it a limit.
    const before2 = ((await logText(page)).match(/Continue where you left off\./g) || []).length;
    // Touch mode's composer types through its own keyboard: the message
    // goes in as the composer would send it.
    await post('/api/chats/' + encodeURIComponent(id) + '/messages', { client_id: 'e2e-sleep-now', text: '/sleep 30' });
    await page.waitForSelector('#chat .so-composer__stop:not([hidden])', { timeout: 10000 });
    await page.click('#chat .so-composer__account-btn');
    await page.click('.so-account-menu [data-account="' + gamma.id + '"]');
    await page.waitForSelector('dialog.ds-dialog [data-when="now"]', { timeout: 5000 });
    await page.click('dialog.ds-dialog [data-when="now"]');
    ok(await awaitLog(page, 'Switched from Alpha to Gamma', 20000), 'Switch now moves a running turn to the other account', '');
    const carried = await page.waitForFunction((was) => (document.querySelector('#chat .so-chat__log')?.innerText.match(/Continue where you left off\./g) || []).length >= was + 2,
      before2, { timeout: 15000 }).then(() => true, () => false);
    ok(carried, 'and the new account carries the turn on', String(carried));
    const why = await page.$$eval('#chat [data-kind="account"] .so-info', (n) => n[n.length - 1]?.getAttribute('data-tip') || '');
    ok(!/limit/i.test(why) && /next reply may take longer/.test(why), 'its line says nothing of a limit, and that the next reply is slower', oneLine(why));
    ok(unexpected(s.errors).length === 0, 'no console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------------- run

const ALL = [
  ['createshell', 'the sheet makes a Shell session and the shell answers', createshell],
  ['typeandsee', 'keystrokes reach the pane and the journal agrees with the screen', typeandsee],
  ['reloadkeepsscreen', 'a reload keeps the screen and the input path', reloadkeepsscreen],
  ['pages', 'every page, a chat included, is clean at a phone and at a desk', pages],
  ['harnesses', 'a Shell and a chat with each agent start, and are seen in the browser', harnesses],
  ['browserlayout', 'popup placement and bundled terminal font metrics', browserlayout],
  ['sessionlist', 'rename, file, pick up and delete: a Shell and a chat', sessionlist],
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
  ['adminoptions', 'the basics round-trip, and the defaults and the fixed policy reach the CLI', adminoptions],
  ['tmuxcard', 'the engine card on a machine whose tmux is too old', tmuxcard],
  ['twoviewers', 'two devices on one session, and one notice about the size', twoviewers],
  ['backpressure', 'two hundred lines arrive whole, on screen and in the journal', backpressure],
  ['deletekeepsdir', 'delete ends a Shell\u2019s tmux session and a chat\u2019s agent, and keeps the work', deletekeepsdir],
  ['recoveredsession', 'a tmux session with no row is taken in, never killed', recoveredsession],
  ['createclaude', 'Claude Code from the sheet to its command line, and back on its conversation after a crash', createclaude],
  ['createcodex', 'Codex over its app server: one thread, kept, and resumed after a rest', createcodex],
  ['accounts-agents', 'an account of each agent reaches its CLI: its folder, its key, its home', accountsagents],
  ['createopencode', 'OpenCode over its own authenticated server: one session, resumed after a rest', createopencode],
  ['rebootresume', 'a machine that rebooted mid-turn, and the chat that comes back on its conversation', rebootresume],
  ['lighttheme', 'the white terminal, from the painted pane to every drawn colour', lighttheme],
  ['activity-claude', 'Claude Code working, finishing, and a name that goes bold', activityclaude],
  ['activity-codex', 'Codex working, finishing, and a name that goes bold', activitycodex],
  ['activity-opencode', 'OpenCode working, finishing, and a name that goes bold', activityopencode],
  ['tasks-claude', 'Claude Code done with its turn, a background shell still running: an hourglass, then bold', taskclaude],
  ['tasks-codex', 'Codex done with its turn, a background terminal still running', taskcodex],
  ['activity-waiting', 'a question: a still amber ring, and no timeout', activitywaiting],
  ['unread', 'bold when nobody saw it, gone when the chat is opened or sent a message', unread],
  ['notify', 'a chime and a notification when a chat stops working, and two switches in the options menu', notify],
  ['session-title', 'a chat that names itself on its first message, again by the arrow, and a prepared one', sessiontitle],
  ['status-speak', 'Status says what a chat did, on the page and out loud', statusspeak],
  ['status-ticker', 'the phases of a status, in order, in one line', statusticker],
  ['dictate', 'a recording with two endings, and the words it kept in the composer', dictate],
  ['no-overlap', 'the microphone and the voice are never open at the same time', nooverlap],
  ['typeafteroutage', 'a cut socket, a locked phone, and a pane that still takes keystrokes', typeafteroutage],
  ['typekeepsfocus', 'a dialog, the ⋮ menu and two sessions: the keys still land in the pane', typekeepsfocus],
  ['keyboard', 'on a touch screen only the button raises the keyboard, in every dialog too', keyboard],
  ['options', 'the ⋮ is this device\u2019s options, and a chat\u2019s account says when its limit renews', optionsmenu],
  ['design', 'white surfaces, marks, hover-only detail and motion that does not restart', design],
  ['clipboard', 'copy and paste: the keys, a plain drag, a click, a middle click, and OSC 52', clipboard],
  ['touchcopy', 'a finger held on the pane selects, grows and copies', touchcopy],
  ['transfer', 'copy, paste and upload buttons, a pasted image, and the phone layout', transfer],
  ['prestart', 'a prepared chat: its prompt kept, Start sending it as the first message, and a start by the clock', prestart],
  ['chat-stream', 'a chat: streaming, Markdown, replay, queued, Stop, the draft, the palette, the model, the lines, PreStart', chatstream],
  ['chat-tools', 'tool rows, the diff viewer, a subagent, a workflow, and the task list pinned while it runs', chattools],
  ['chat-offline', 'what a chat is told offline (two tabs, a model, an answer), or whose answer was lost, or left across a reload: delivered once', chatoffline],
  ['chat-ask', 'questions: the card and the dialog, every kind, a plan, a counter-question, Home, two devices', chatask],
  ['chat-attach', 'files from the button, dropped and pasted, at paths the server chose', chatattach],
  ['chat-phone', 'a chat on a phone: touch targets, no sideways scroll, the keyboard, the dialog as the screen', chatphone],
  ['chat-home', 'Home reads a chat from its journal: what it does, Running and Finished, its question on the card, at a desk and on a phone', chathome],
  ['chat-push', 'a chat\u2019s notification and badge: the question\u2019s words, how a turn ended, nothing for a Stop, a limit once', chatpush],
  ['chat-title', 'a chat names itself from its first message', chattitle],
  ['chat-status', 'Status says a chat from its journal, on its line and out loud', chatstatus],
  ['chat-outcome', 'a chat\u2019s turn judged from its journal: the hourglass, the verdict, the notification', chatoutcome],
  ['chat-prestart', 'a prepared chat started by the clock, its draft the first message', chatprestart],
  ['chat-ultracode', 'Ultracode for Claude Code alone: in the sheet, and in a Claude chat\u2019s menu', chatultracode],
  ['accounts', 'the Accounts card: an API key, a Codex device code, rename, order, a delete refused then taken, a phone', accountsscenario],
  ['chat-account', 'a chat\u2019s account: from the sheet, switched idle and after a turn, a limit\u2019s Continue with, the automatic switch, a phone', chataccount],
  ['backup', 'the Backup card: a bucket configured, a backup on demand, a failure and the warning, and the next good run', backup],
  ['restore', 'a machine that is gone, and a new one restored from its backup on the setup page', restore],
  ['home-landing', 'every start of the page is Home, and the button and the address go back to it', homelanding],
  ['home-live', 'the numbers on Home move by themselves, with no terminal attached', homelive],
  ['outcome', 'a turn\'s end is judged: the hourglass while it is read, then the verdict everywhere', outcome],
  ['home-question', 'a question answered with a tap, typed into the pane by the operator', homequestion],
  ['home-own-answer', 'an answer of one\u2019s own, typed into the question dialog', homeownanswer],
  ['home-queue', 'two questions, one after the other, oldest first', homequeue],
  ['home-elsewhere', 'a question answered in the terminal while the dialog shows it', homeelsewhere],
  ['home-deeplink', '#q= opens the question and #open= the session', homedeeplink],
  ['home-reconnect', 'a server restart under an open Home: Reconnecting, then Live and the right numbers', homereconnect],
  ['home-touch', 'a Tesla and a phone: big buttons and a dialog that is the whole screen', hometouch],
  ['home-focus', 'the field of one’s own answer keeps the keyboard through every redraw, and the voice says what failed', homefocus],
  ['home-retry', 'an answer refused for good, one the server could not take just now, and one to a chat deleted on its way', homeretry],
  ['home-runlost', 'the dialog opened again mid-answer, an answer sent across a server restart, and a reply lost on the way back', homerunlost],
  ['home-opencode', 'an OpenCode chat’s questions from the dialog - ask_user with an option and an own answer, its own question tool - and a permission it allows itself', homeopencode],
  ['home-stream', 'with chats: a late list, a handshake that hangs, a refused upgrade and an offline Home', homestream],
  ['home-quiet', 'a tap on a stream that died quietly, chats’ questions polled across a restart, a long hide, a naming cut off, a server never reached', homequiet],
  ['home-kinds', 'a multi-select, two tabs and their review, a plan sent back and approved, and a question in prose', homekinds],
  ['home-unread', 'a question in prose whose read failed, or had no key, is read on the open Home once it can be', homeunread],
  ['home-honest', 'ages by the server\u2019s clock, a dialog that says it is offline, and a compressed poll', homehonest],
  ['home-language', 'a question read in German off an English screen: two tabs that share Yes and No, each given its own answer', homelanguage],
  ['home-restart', 'a server restart under an open dialog: every question kept, its choice, its age, and the one asked in prose', homerestart],
  ['home-devices', 'two devices on one question: the second follows the first\u2019s answer, and is told when its Send crossed it', homedevices],
  ['home-followup', 'a follow-up asked right after the answer, slower to read than "Answered" lasts', homefollowup],
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
