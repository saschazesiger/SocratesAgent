// The terminal itself: xterm.js, wired the way a white page needs it.
//
// The vendored bundles are loaded by index.html as plain scripts and define
// browser globals, so nothing here imports them - there is no module build of
// them to import, and adding one would mean a bundler.
//
// Two settings carry the whole design. `minimumContrastRatio: 4.5` re-derives
// a colour at draw time when it would be unreadable against the background,
// which is what makes the dim greys every CLI emits - all of them written for
// a dark terminal - legible on white. And LIGHT_THEME's own `white` and
// `brightWhite` are greys, so that a program printing "white on default" is
// still there to read; the runtime correction then has less to do.

import { toast } from './api.js';

// LIGHT_THEME is the palette, drawn from the app's own tokens. Its ground is
// white and its ink is near black; the rest are the colours a program means
// when it says "yellow", and several of them are deliberately below 4.5:1 on
// white, because a yellow that clears 4.5:1 on white is brown. What makes them
// legible is `minimumContrastRatio` below, which re-derives a colour at draw
// time - so `lighttheme` measures what the renderer actually drew, and
// contrast() is what it measures with.
export const LIGHT_THEME = {
  background: '#ffffff', foreground: '#17181b',
  cursor: '#17181b', cursorAccent: '#ffffff',
  selectionBackground: '#d7e3ff', selectionForeground: '#17181b',
  black: '#17181b', red: '#cf3f3f', green: '#1a9a63', yellow: '#b8811a',
  blue: '#2f6df6', magenta: '#8a4fd0', cyan: '#0e8a94', white: '#dcdde1',
  brightBlack: '#63666d', brightRed: '#e05555', brightGreen: '#22b273',
  brightYellow: '#cf9526', brightBlue: '#4c82ff', brightMagenta: '#a066e0',
  brightCyan: '#22a4ae', brightWhite: '#9b9ea6',
};

// The stack the terminal is drawn in. xterm.js measures a character cell, so
// it needs a resolved font list rather than a var() that means nothing to its
// measuring canvas.
const MONO = 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace';

/** contrast is the WCAG ratio between two `#rrggbb` colours, 1 to 21. */
export function contrast(a, b) {
  const l = (hex) => {
    const v = [1, 3, 5].map((i) => {
      const c = parseInt(hex.slice(i, i + 2), 16) / 255;
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * v[0] + 0.7152 * v[1] + 0.0722 * v[2];
  };
  const [x, y] = [l(a), l(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

/**
 * createTerm builds one terminal inside `host`.
 *
 * onData is the single path for everything the browser sends: keystrokes,
 * mouse reports, and xterm.js's own replies to the DA1/DA2 queries tmux writes
 * the moment a viewer attaches. It is wired before anything can be rendered,
 * because those replies travel browser -> socket -> pane.
 *
 * onResize is called with (cols, rows) after every fit that actually changed
 * the size, and never with a zero.
 */
/**
 * measurePane answers what a terminal in `host` would be, without leaving one
 * behind.
 *
 * The new-session sheet needs it. A session is created with a size, and the
 * first viewer's attach then sets the window to what the pane really is - so
 * a size nobody measured means the first thing that happens to a new session
 * is a resize. A tmux window that shrinks reflows, and on 3.6 that pushes the
 * program's first wrapped line into the scrollback before anybody has read it:
 * the banner a CLI prints on start-up loses its head. Measured here, the
 * create and the attach ask for the same thing and nothing moves.
 */
export function measurePane(host, opts = {}) {
  const term = new Terminal({
    allowProposedApi: true,
    fontFamily: MONO,
    fontSize: opts.fontSize || 14,
    lineHeight: 1.15,
    theme: LIGHT_THEME,
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  let size = { cols: 0, rows: 0 };
  try {
    term.open(host);
    fit.fit();
    size = { cols: term.cols, rows: term.rows };
  } catch { /* an unmeasurable pane leaves the choice to the server */ }
  try { term.dispose(); } catch { /* it was never opened */ }
  return size;
}

// How far a pointer has to travel before the press is a drag rather than a
// click - for a finger, before it is a scroll rather than a tap. Under it
// nothing is selected, nothing is scrolled, and the click still reaches the
// pane as the click it is.
const SLOP = 8;

// How long a finger has to rest before it is selecting rather than tapping.
const HOLD_MS = 450;

/* ------------------------------------------------------------ clipboard */

// Whether this device's copy-and-paste key is Cmd rather than Ctrl. iPadOS
// says "MacIntel" and an iPhone says "iPhone", and both of them are Cmd on
// the keyboard somebody attaches to them.
const IS_MAC = /Mac|iPhone|iPad|iPod/.test(
  (navigator.platform || '') + ' ' + (navigator.userAgent || ''),
);

// The last text copied out of a pane on this page - what X11 calls the primary
// selection, and what a middle click pastes. Kept here rather than read back
// from the clipboard, which the browser will not hand over without asking.
let primary = '';

// When a middle click last pasted `primary` itself, and whether that paste is
// still to come.
//
// Chrome on Linux pastes the X11 primary selection on a middle click of its
// own accord, whenever an editable element is under the pointer - which xterm's
// hidden textarea is, once a right click has moved it there - and
// `preventDefault` on the press does not stop it. So one middle click pasted
// the line twice. The browser's paste arrives in the same millisecond as the
// click, so it is the one this window catches; it is spent the moment it is
// caught, and any keystroke spends it too, so that a paste key pressed after a
// middle click is never mistaken for the browser answering the click.
let middleAt = 0;

// How long the browser's own answer to a middle click may take. Measured at
// under a millisecond; a person moving from the mouse to Ctrl-V takes
// hundreds.
const MIDDLE_MS = 150;

/** isPasteKey is Ctrl-V, Ctrl-Shift-V or Shift-Insert - Cmd-V on a Mac. */
function isPasteKey(ev) {
  if (ev.altKey) return false;
  const v = ev.code === 'KeyV' || (ev.key || '').toLowerCase() === 'v';
  if (v) return IS_MAC ? ev.metaKey && !ev.ctrlKey : ev.ctrlKey && !ev.metaKey;
  return ev.key === 'Insert' && ev.shiftKey && !ev.ctrlKey && !ev.metaKey;
}

/** isCopyKey is Ctrl-C, Ctrl-Shift-C or Ctrl-Insert - Cmd-C on a Mac. */
function isCopyKey(ev) {
  if (ev.altKey) return false;
  const c = ev.code === 'KeyC' || (ev.key || '').toLowerCase() === 'c';
  if (c) return IS_MAC ? ev.metaKey && !ev.ctrlKey : ev.ctrlKey && !ev.metaKey;
  return ev.key === 'Insert' && ev.ctrlKey && !ev.metaKey && !ev.shiftKey;
}

/**
 * writeClipboard puts `text` on the clipboard, and answers whether it went.
 *
 * The async clipboard is the path, and it is not always there: it needs a
 * secure context, and this app is reached over plain http on a LAN often
 * enough - a tunnel is the usual way in, the address bar is not. So the old
 * hidden-textarea copy is the fallback rather than an error message, and
 * because it has to run inside the gesture that asked for it, the choice
 * between the two is made synchronously. The fallback borrows the focus for
 * the copy and gives it back: a terminal that has just been copied out of
 * must still take the next keystroke.
 *
 * A copy the async clipboard refuses is said so, once, rather than left to
 * look like a paste that produced the wrong thing later - unless it is
 * `quiet`, which is a copy nobody asked for: a program's OSC 52 arrives with
 * no gesture behind it, so the browser refusing it is the ordinary case and
 * not something to put on screen. The text is kept as the middle-click buffer
 * either way, which is the one place it is still reachable from.
 */
function writeClipboard(text, opts = {}) {
  if (!text) return false;
  primary = text;
  if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).catch((err) => {
      if (opts.quiet) return;
      console.error('copy', err);
      toast('The browser refused the copy — select again and press the copy key.', 'error');
    });
    return true;
  }
  const focused = document.activeElement;
  const field = document.createElement('textarea');
  field.value = text;
  field.setAttribute('aria-hidden', 'true');
  field.setAttribute('readonly', '');
  field.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;';
  document.body.append(field);
  field.select();
  let done = false;
  try { done = document.execCommand('copy'); } catch { /* answered below */ }
  field.remove();
  if (focused && focused !== document.body && typeof focused.focus === 'function') {
    try { focused.focus({ preventScroll: true }); } catch { /* it went away */ }
  }
  return done;
}

/**
 * lastCopied is the last text copied out of a pane on this page.
 *
 * It is what the key bar falls back on: a phone reaching the app over plain
 * http has no `navigator.clipboard` at all, and one that has it can still be
 * refused - so "copy a line with a long press, paste it back with the button"
 * has to work without the browser's clipboard being readable.
 */
export function lastCopied() { return primary; }

/** isEditable is whether a paste or copy aimed at `node` belongs to a field. */
function isEditable(node) {
  if (!node || node.nodeType !== 1) return false;
  const tag = node.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || node.isContentEditable;
}

/**
 * wireClipboard makes the copy and paste keys mean copy and paste.
 *
 * xterm.js turns Ctrl and a letter into that letter's control code and sends
 * it, with no exception for V - so Ctrl-V arrived in the pane as 0x16, and
 * 0x16 is what Claude Code has bound to "paste an image from the clipboard".
 * A person pasting a line of text got "No image found in clipboard" and no
 * text, because the text never left the browser: xterm cancels the keydown it
 * handled, and a cancelled Ctrl-V is a paste the browser never performs.
 *
 * So the paste keys are handed back to the browser - the custom handler
 * returning false is the one path out of xterm's key handling that does not
 * cancel the event - and the `paste` event xterm already listens for on its
 * textarea does the rest, bracketed when the program asked to be told that a
 * paste is a paste. Nothing here reads the clipboard, so nothing here asks
 * for permission to.
 *
 * Copying is the other half: xterm 6 keeps its selection in its own model and
 * never puts one in the DOM, so the browser has nothing to copy and the
 * `copy` event it fires is empty. Ctrl-C is therefore copy when there is a
 * selection to copy, and the interrupt it has always been when there is not -
 * the rule Windows Terminal and VS Code both settled on - and the selection is
 * dropped once it is taken, so that the next Ctrl-C is an interrupt again. A
 * copy that fails falls through to the interrupt rather than swallowing it.
 *
 * Both keys also work when the terminal is not the focused element - after a
 * click on the sidebar, say - because the browser then fires `paste` and
 * `copy` on whatever is, and those land here as long as it is not a field of
 * its own. That is where "I pressed Ctrl-V and nothing happened" came from.
 * Both listeners are on the document in the **capture** phase, which is the
 * only place that runs before xterm's own listeners on its textarea: a paste
 * this page has already performed - the middle click above - has to be
 * stopped before xterm sees it, or it arrives twice.
 */
function wireClipboard(term) {
  const copySelection = () => {
    const text = term.getSelection();
    if (!text) return false;
    if (!writeClipboard(text)) return false;
    term.clearSelection();
    return true;
  };
  term.attachCustomKeyEventHandler((ev) => {
    if (ev.type !== 'keydown') return true;
    // A key was pressed, so whatever paste follows is the person's, not the
    // browser's answer to a middle click.
    middleAt = 0;
    // Handed to the browser: it pastes into the textarea, which is where
    // xterm's own paste listener is.
    if (isPasteKey(ev)) return false;
    if (isCopyKey(ev)) {
      if (!term.hasSelection() || !copySelection()) return true;
      ev.preventDefault();
      return false;
    }
    return true;
  });

  // xterm's own textarea, where a paste aimed at the pane lands and where
  // xterm's listeners are: those two are left to it.
  const isTermField = (node) => !!term.textarea && node === term.textarea;

  const onPaste = (ev) => {
    if (!term.element || !term.element.isConnected) return;
    // The browser's own answer to the middle click we have already answered.
    if (middleAt && performance.now() - middleAt < MIDDLE_MS) {
      middleAt = 0;
      ev.preventDefault();
      ev.stopImmediatePropagation();
      return;
    }
    if (isTermField(ev.target) || isEditable(ev.target)) return;
    const text = ev.clipboardData ? ev.clipboardData.getData('text/plain') : '';
    if (!text) return;
    ev.preventDefault();
    term.paste(text);
    term.focus();
  };
  const onCopy = (ev) => {
    if (isTermField(ev.target) || isEditable(ev.target)
      || !term.hasSelection() || !ev.clipboardData) return;
    // A selection made on the page itself - in the chat, say - is the one the
    // person means.
    const page = window.getSelection ? String(window.getSelection() || '') : '';
    if (page.trim()) return;
    const text = term.getSelection();
    ev.clipboardData.setData('text/plain', text);
    ev.preventDefault();
    primary = text;
    term.clearSelection();
  };
  document.addEventListener('paste', onPaste, true);
  document.addEventListener('copy', onCopy, true);
  return () => {
    document.removeEventListener('paste', onPaste, true);
    document.removeEventListener('copy', onCopy, true);
  };
}

/**
 * wireOSC52 takes what a program - or tmux, on the person's behalf - copies.
 *
 * `ESC ] 52 ; <selection> ; <base64> BEL` is how a terminal is asked to set
 * the clipboard, and it is what tmux sends when something is copied in copy
 * mode, provided the terminal it is attached from admits to the capability.
 * tmux names no selection at all - `52;;` - which the stock clipboard addon
 * read as "not the clipboard" and dropped on the floor. The spec says an
 * empty selection is the default one, so it is taken here.
 *
 * A query (`?`) is answered with nothing: a program does not get to read the
 * clipboard through the pane, and the browser would ask the person about it
 * every time one tried.
 *
 * The write is quiet: nothing the person did asked for it, so a browser that
 * refuses a clipboard write with no gesture behind it - which is most of them,
 * most of the time - must not put an error on their screen for a copy they
 * made in tmux by accident. What was copied is still the middle-click buffer.
 */
function wireOSC52(term) {
  return term.parser.registerOscHandler(52, (data) => {
    const at = data.indexOf(';');
    if (at < 0) return true;
    const payload = data.slice(at + 1);
    if (!payload || payload === '?') return true;
    let text = '';
    try {
      const raw = atob(payload.replace(/\s+/g, ''));
      const bytes = new Uint8Array(raw.length);
      for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i);
      text = new TextDecoder().decode(bytes);
    } catch { return true; }
    if (text) writeClipboard(text, { quiet: true });
    return true;
  });
}

/* ------------------------------------------------------------ selection */

/**
 * selectionController is the pointer's half of copying: press, drag, release,
 * and the text is on the clipboard.
 *
 * tmux tracks the mouse, so as far as xterm is concerned the pointer belongs
 * to the program and a plain drag is a stream of mouse reports. What tmux
 * did with them was open its own copy mode, select there, and on release copy
 * into its own buffer - reaching the browser's clipboard, if at all, through
 * OSC 52 and an async clipboard call with no gesture behind it, which Safari
 * refuses and plain http has not got. On a Mac there was no way to take the
 * pointer back at all: xterm 6 gives Shift to the program there and forces a
 * selection on Alt only behind an option. So "select with the mouse, paste
 * elsewhere" - the one thing a terminal must do - worked on some desks and
 * not others.
 *
 * Here the pointer is the person's. A left press on the screen is held back
 * from xterm; if it moves past the slop it is a selection, made through
 * xterm's public select() with cells worked out from the screen's own
 * geometry; if it is released where it was pressed it is a click, and the
 * click is replayed to xterm then - so a program that asked for clicks still
 * gets them, a moment late. A double click takes the word, a triple the line
 * with the lines wrapped into it, and dragging on from either grows by the
 * same unit. On release the selection is copied, inside the gesture, which is
 * the one place every browser lets a page write the clipboard without asking
 * - and it is what a native terminal does: iTerm2, Windows Terminal and every
 * X11 terminal copy on select. The selection stays on screen, so the copy
 * key still works over it, and any key that sends something clears it.
 *
 * A right press is also held back - tmux answers it with a menu of its own
 * nobody asked for - and the browser's context menu, which carries Paste, is
 * left to come. A middle press pastes the last thing selected here, which is
 * what a middle click has meant on X11 for thirty years, and is not handed to
 * tmux, which would paste a buffer of its own. Shift, Alt, Ctrl and Cmd with
 * the button are left to xterm and the program, as before.
 *
 * A finger does the same with a long press: held still it selects the word
 * under it, moved on it grows the selection, and lifted it copies and says
 * so, because a phone gives no other sign.
 */
function selectionController(host, term) {
  const screen = () => host.querySelector('.xterm-screen');

  // cellAt is the buffer cell under a point on the page. The screen element
  // is exactly cols x rows cells, so the geometry is its own.
  const cellAt = (x, y) => {
    const el = screen();
    if (!el) return null;
    const r = el.getBoundingClientRect();
    if (!r.width || !r.height || !term.cols || !term.rows) return null;
    const col = Math.min(term.cols - 1, Math.max(0, Math.floor((x - r.left) / (r.width / term.cols))));
    const row = Math.min(term.rows - 1, Math.max(0, Math.floor((y - r.top) / (r.height / term.rows))));
    return { col, row: term.buffer.active.viewportY + row };
  };

  const separators = () => (term.options.wordSeparator || ' ()[]{}\'"`,;');
  const isBreak = (line, col) => {
    const cell = line && line.getCell(col);
    if (!cell) return true;
    if (cell.getWidth() === 0) return false;         // the tail of a wide char
    const ch = cell.getChars();
    return ch === '' || /\s/.test(ch) || separators().includes(ch);
  };
  // wordAt is the [first, last] column of the word at a cell, or the cell
  // itself when it sits on a separator.
  const wordAt = ({ col, row }) => {
    const line = term.buffer.active.getLine(row);
    if (!line || isBreak(line, col)) return [col, col];
    let a = col;
    let b = col;
    while (a > 0 && !isBreak(line, a - 1)) a -= 1;
    while (b < term.cols - 1 && !isBreak(line, b + 1)) b += 1;
    return [a, b];
  };
  // linesAt is the [first, last] buffer row of the logical line a row is in.
  const linesAt = (row) => {
    const buf = term.buffer.active;
    let a = row;
    let b = row;
    const wrapped = (r) => { const l = buf.getLine(r); return !!(l && l.isWrapped); };
    while (a > 0 && wrapped(a)) a -= 1;
    while (b + 1 < buf.length && wrapped(b + 1)) b += 1;
    return [a, b];
  };
  const before = (p, q) => p.row < q.row || (p.row === q.row && p.col < q.col);
  const span = (from, to) => {
    const [s, e] = before(to, from) ? [to, from] : [from, to];
    const length = (e.row - s.row) * term.cols + (e.col - s.col) + 1;
    if (length > 0) term.select(s.col, s.row, length);
  };

  let anchor = null;   // the cell the selection grows from
  let unit = null;     // 'cell' | 'word' | 'line', once selecting

  const grow = (cell) => {
    if (!anchor || !cell) return;
    if (unit === 'line') {
      const [a] = linesAt(Math.min(anchor.row, cell.row));
      const [, b] = linesAt(Math.max(anchor.row, cell.row));
      term.selectLines(a, b);
      return;
    }
    if (unit === 'word') {
      const [s, e] = before(cell, anchor) ? [cell, anchor] : [anchor, cell];
      const [a] = wordAt(s);
      const [, b] = wordAt(e);
      span({ col: a, row: s.row }, { col: b, row: e.row });
      return;
    }
    span(anchor, cell);
  };

  return {
    cellAt,
    /** begin starts a selection at a point, of one unit; nothing is drawn for 'cell' until it grows. */
    begin(x, y, by) {
      const cell = cellAt(x, y);
      if (!cell) return false;
      anchor = cell;
      unit = by;
      term.clearSelection();
      if (by !== 'cell') grow(cell);
      return true;
    },
    /** extend grows the selection to the point. */
    extend(x, y) { grow(cellAt(x, y)); },
    /** active is whether a selection is being made. */
    active: () => anchor !== null,
    /** finish ends it, copying what was selected; answers the text copied. */
    finish() {
      anchor = null;
      unit = null;
      const text = term.getSelection();
      if (!text) return '';
      return writeClipboard(text) ? text : '';
    },
    /** abandon ends it without copying. */
    abandon() { anchor = null; unit = null; },
  };
}

/**
 * wireMouse gives the pointer's buttons the meanings selectionController
 * describes, and returns the way to undo it.
 */
function wireMouse(host, term, sel) {
  const screen = () => host.querySelector('.xterm-screen');
  const onScreen = (ev) => {
    const el = screen();
    return !!el && (ev.target === el || el.contains(ev.target));
  };
  const stop = (ev) => { ev.stopImmediatePropagation(); ev.preventDefault(); };

  let press = null;   // { x, y } of a left press not yet a click or a drag

  // replayClick gives xterm the press and release it was held back from, at
  // the place they happened, so that the report a program asked for is sent.
  // Only these two are made up: the browser's own `click` follows the real
  // release regardless, which is what a link in the pane opens on.
  const replayClick = (x, y) => {
    const el = screen();
    if (!el) return;
    const init = { bubbles: true, cancelable: true, view: window, clientX: x, clientY: y, button: 0, detail: 1 };
    el.dispatchEvent(new MouseEvent('mousedown', { ...init, buttons: 1 }));
    el.dispatchEvent(new MouseEvent('mouseup', { ...init, buttons: 0 }));
  };

  const move = (ev) => {
    if (!ev.isTrusted) return;
    if (press) {
      if (Math.abs(ev.clientX - press.x) < SLOP && Math.abs(ev.clientY - press.y) < SLOP) return;
      const { x, y } = press;
      press = null;
      if (!sel.begin(x, y, 'cell')) { unlisten(); return; }
    }
    if (sel.active()) sel.extend(ev.clientX, ev.clientY);
  };
  const up = (ev) => {
    if (!ev.isTrusted) return;
    unlisten();
    if (press) {
      const { x, y } = press;
      press = null;
      term.clearSelection();
      replayClick(x, y);
      return;
    }
    if (sel.active()) sel.finish();
  };
  const listen = () => {
    window.addEventListener('mousemove', move, true);
    window.addEventListener('mouseup', up, true);
  };
  const unlisten = () => {
    window.removeEventListener('mousemove', move, true);
    window.removeEventListener('mouseup', up, true);
  };

  const down = (ev) => {
    // Our own replay, or a pointer on the scrollbar: xterm's to handle.
    if (!ev.isTrusted || !onScreen(ev)) return;
    if (ev.button === 2) {
      // No report, so no tmux menu; the context menu still comes, and xterm's
      // own contextmenu listener puts its textarea under it for Paste.
      stop(ev);
      term.focus();
      return;
    }
    if (ev.button === 1) {
      stop(ev);
      term.focus();
      if (!primary) return;
      middleAt = performance.now();
      term.paste(primary);
      return;
    }
    if (ev.button !== 0 || ev.shiftKey || ev.altKey || ev.ctrlKey || ev.metaKey) return;
    stop(ev);
    term.focus();
    if (sel.active()) sel.abandon();
    if (ev.detail >= 3) sel.begin(ev.clientX, ev.clientY, 'line');
    else if (ev.detail === 2) sel.begin(ev.clientX, ev.clientY, 'word');
    else press = { x: ev.clientX, y: ev.clientY };
    listen();
  };

  host.addEventListener('mousedown', down, true);
  return () => {
    host.removeEventListener('mousedown', down, true);
    unlisten();
  };
}

/**
 * wireTouch makes a finger scroll the pane or, held, select from it, and
 * returns the way to undo it.
 *
 * xterm 6 draws its viewport with VS Code's scrollable element, and that
 * element listens for `wheel` and for nothing else. The bundle carries the
 * gesture recogniser that would turn a drag into a scroll, and never registers
 * the viewport as a target for it - so on a phone a finger on the terminal
 * moved nothing at all, and the scrollback of a session was unreachable from
 * the device this product is used from.
 *
 * A drag is therefore turned into the wheel events a trackpad would have sent,
 * dispatched on the screen element, which is where a real wheel lands. What a
 * wheel then means is xterm's own decision and not ours: with tmux tracking the
 * mouse - the default - it is a mouse report, and tmux scrolls its own history
 * in copy mode; a program tracking the mouse itself is handed the same report;
 * and a plain buffer scrolls xterm's own scrollback. One path, and the same one
 * a desk with a real wheel already takes.
 *
 * The pixels are passed through as they were measured, so the screen follows
 * the finger at the speed a trackpad would have moved it.
 *
 * A finger that rests for HOLD_MS without moving is selecting instead: the
 * word under it, grown by dragging on, copied when it lifts - with a toast,
 * because nothing else on a phone says a copy happened. The browser's own
 * long-press behaviour - a context menu on Android, a callout on iOS - is
 * cancelled for the duration, and so is the click the browser would make up
 * from the release.
 *
 * The one case a finger is not given to xterm is `scrollable` below.
 */
function wireTouch(host, term, sel) {
  // What a wheel is worth here. On an alternate screen with nobody tracking the
  // mouse - a tmux pane with `mouse off` - xterm has no scrollback of its own to
  // move and turns a wheel into arrow keys instead. That is a fair reading of a
  // deliberate turn of a wheel in `less`; it is not a fair reading of a drag,
  // which is how a phone scrolls everything. It would answer the gesture by
  // walking the shell's history onto the prompt. So the drag is swallowed: it
  // scrolls nothing, exactly as it did before any of this, and it types nothing.
  const scrollable = () => term.buffer.active.type !== 'alternate'
    || term.modes.mouseTrackingMode !== 'none';
  let id = null;          // the finger being followed, or none
  let originX = 0;        // where it went down, for the slop
  let originY = 0;
  let lastY = 0;          // where it was last seen, for the delta
  let scrolling = false;  // past the slop: the drag is ours
  let holding = false;    // held still long enough: the finger is selecting
  let hold = null;        // the timer that decides that

  const followed = (touches) => {
    for (const touch of touches) if (touch.identifier === id) return touch;
    return null;
  };
  const clearHold = () => { if (hold) { clearTimeout(hold); hold = null; } };

  const start = (ev) => {
    clearHold();
    // A second finger is a pinch, and a pinch is the browser's.
    if (ev.touches.length !== 1) { id = null; return; }
    const touch = ev.touches[0];
    id = touch.identifier;
    originX = touch.clientX;
    originY = lastY = touch.clientY;
    scrolling = false;
    holding = false;
    const el = host.querySelector('.xterm-screen');
    if (!el || !(ev.target === el || el.contains(ev.target))) return;
    hold = setTimeout(() => {
      hold = null;
      if (id === null || scrolling) return;
      if (!sel.begin(originX, originY, 'word')) return;
      holding = true;
      if (navigator.vibrate) { try { navigator.vibrate(10); } catch { /* not a phone */ } }
    }, HOLD_MS);
  };

  const move = (ev) => {
    if (id === null) return;
    const touch = followed(ev.touches);
    if (!touch) return;
    if (holding) {
      if (ev.cancelable) ev.preventDefault();
      sel.extend(touch.clientX, touch.clientY);
      return;
    }
    if (!scrolling && Math.abs(touch.clientY - originY) < SLOP
      && Math.abs(touch.clientX - originX) < SLOP) return;
    clearHold();
    scrolling = true;
    const dy = touch.clientY - lastY;
    lastY = touch.clientY;
    // Whatever this drag turns out to scroll, it is not the page: taken before
    // the browser can rubber-band, and before a release can synthesise the
    // mouse events of a tap the pane would send on to the program.
    if (ev.cancelable) ev.preventDefault();
    if (!dy || !scrollable()) return;
    const screen = host.querySelector('.xterm-screen');
    if (!screen) return;
    // A finger moving down brings earlier lines down with it, which is a wheel
    // turned up. The delta is in CSS pixels, which is what a line of it is
    // worth to xterm.
    screen.dispatchEvent(new WheelEvent('wheel', {
      deltaY: -dy,
      deltaMode: 0,
      clientX: touch.clientX,
      clientY: touch.clientY,
      bubbles: true,
      cancelable: true,
    }));
  };

  const end = (ev) => {
    if (id === null || !followed(ev.changedTouches)) return;
    clearHold();
    id = null;
    scrolling = false;
    if (!holding) return;
    holding = false;
    // No click is made up from this release: the tap it would be is not one.
    if (ev.cancelable) ev.preventDefault();
    if (ev.type !== 'touchend') { sel.abandon(); return; }
    if (sel.finish()) toast('Copied');
  };

  // Android opens a context menu on a long press; the finger is selecting.
  const menu = (ev) => { if (id !== null || holding) ev.preventDefault(); };

  host.addEventListener('touchstart', start, { passive: true });
  host.addEventListener('touchmove', move, { passive: false });
  host.addEventListener('touchend', end, { passive: false });
  host.addEventListener('touchcancel', end, { passive: false });
  host.addEventListener('contextmenu', menu);
  return () => {
    clearHold();
    host.removeEventListener('touchstart', start);
    host.removeEventListener('touchmove', move);
    host.removeEventListener('touchend', end);
    host.removeEventListener('touchcancel', end);
    host.removeEventListener('contextmenu', menu);
  };
}

export function createTerm(host, opts = {}) {
  const term = new Terminal({
    allowProposedApi: true,           // unicode11 throws without it
    fontFamily: MONO,
    fontSize: opts.fontSize || 14,    // under 14 a phone zooms the page on focus
    lineHeight: 1.15,
    scrollback: opts.scrollback || 2000,
    cursorBlink: true,
    cursorStyle: 'bar',
    convertEol: false,
    screenReaderMode: false,          // a parallel live region, at a real cost on a phone
    minimumContrastRatio: 4.5,
    smoothScrollDuration: 0,
    macOptionIsMeta: true,
    macOptionClickForcesSelection: true, // Alt-drag on a Mac is xterm's own selection, as Shift-drag is elsewhere
    windowOptions: { getWinSizeChars: true, getWinSizePixels: true },
    theme: LIGHT_THEME,
  });

  term.loadAddon(new Unicode11Addon.Unicode11Addon());
  term.unicode.activeVersion = '11';
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new WebLinksAddon.WebLinksAddon());
  term.open(host);

  // Copy and paste: the keys, the pointer, a finger, and what a program
  // copies - none of which xterm on its own gets right here.
  const unwireClipboard = wireClipboard(term);
  const osc52 = wireOSC52(term);
  const sel = selectionController(host, term);
  const unwireMouse = wireMouse(host, term, sel);
  const unwireTouch = wireTouch(host, term, sel);

  if (opts.webgl !== false) {
    try {
      const gl = new WebglAddon.WebglAddon();
      // iOS drops the WebGL context whenever the tab goes to the background,
      // and a lost context that is not disposed of paints nothing at all.
      gl.onContextLoss(() => { gl.dispose(); });
      term.loadAddon(gl);
    } catch { /* the DOM renderer; 6.x has no canvas renderer to fall back to */ }
  }

  // The hidden textarea is a text field like any other as far as a phone is
  // concerned, and a terminal is the one place autocorrect must never reach.
  if (term.textarea) {
    term.textarea.setAttribute('autocorrect', 'off');
    term.textarea.setAttribute('autocapitalize', 'none');
    term.textarea.setAttribute('autocomplete', 'off');
    term.textarea.setAttribute('spellcheck', 'false');
  }

  // The one path everything typed takes. It is wrapped because an exception
  // thrown out of a listener leaves xterm's key handling half way through the
  // event it was in the middle of - and a page that has thrown once must still
  // deliver the next keystroke.
  const guarded = (handler) => (data) => {
    try { handler(data); } catch (err) { console.error('terminal input', err); }
  };
  if (opts.onData) term.onData(guarded(opts.onData));
  if (opts.onBinary) term.onBinary(guarded(opts.onBinary));

  // Fitting. A ResizeObserver catches the window, the drawer and the sidebar;
  // visualViewport catches the phone keyboard opening, which window.resize
  // does not report at all on iOS.
  let last = { cols: 0, rows: 0 };
  let timer = null;
  const measure = () => {
    timer = null;
    if (!host.isConnected || !host.clientWidth || !host.clientHeight) return;
    try { fit.fit(); } catch { return; }
    const { cols, rows } = term;
    // Zero is not a size, and a 0x0 resize is what tmux turns into a window
    // nobody can read.
    if (!cols || !rows || (cols === last.cols && rows === last.rows)) return;
    last = { cols, rows };
    if (opts.onResize) opts.onResize(cols, rows);
  };
  const refit = () => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(measure, 80);
  };
  // flush takes a fit that is still on its debounce now. The caller that
  // needs it is the one opening the socket: it reveals the key bar and then
  // asks for the size, and an answer one layout behind
  // attaches at one size and resizes to the real one a moment later - two
  // window changes, and two "another viewer resized this session" notices on
  // every other device, for one person opening one session (§A.7).
  const flush = () => { if (timer) { clearTimeout(timer); measure(); } };

  // One measurement before anything else, and a synchronous one: the socket
  // is opened with whatever size() answers, and a terminal that has not been
  // fitted yet answers with xterm's own 80x24. Attaching at that size and
  // fitting a frame later is two real size changes for one device opening a
  // session - two window re-layouts, and two "another viewer resized this
  // session" notices on every other device (§A.7).
  measure();

  const observer = new ResizeObserver(refit);
  observer.observe(opts.fitTo || host);
  const viewport = window.visualViewport;
  if (viewport) {
    viewport.addEventListener('resize', refit);
    viewport.addEventListener('scroll', refit);
  }

  return {
    term,
    fit,
    /** refit re-measures after a layout change nothing observed. */
    refit,
    /** size is what was measured, fitting first when a fit is still pending. */
    size: () => { flush(); return { cols: term.cols, rows: term.rows }; },
    write: (data) => term.write(data),
    /** reset is the full repaint a `replay_from: 0` hello asks for. */
    reset: () => { term.reset(); last = { cols: 0, rows: 0 }; },
    focus: () => term.focus(),
    dispose() {
      unwireTouch();
      unwireMouse();
      unwireClipboard();
      osc52.dispose();
      observer.disconnect();
      if (viewport) {
        viewport.removeEventListener('resize', refit);
        viewport.removeEventListener('scroll', refit);
      }
      if (timer) clearTimeout(timer);
      term.dispose();
    },
  };
}
