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

// How far a finger has to travel before the drag is a scroll rather than a tap.
// Under it nothing is sent and nothing is prevented, so a tap still reaches the
// pane as the click it is.
const TOUCH_SLOP = 8;

/**
 * touchScroll makes a finger scroll the pane, and returns the way to undo it.
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
 * The one case a finger is not given to xterm is `scrollable` below.
 */
function touchScroll(host, term) {
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
  let originY = 0;        // where it went down, for the slop
  let lastY = 0;          // where it was last seen, for the delta
  let scrolling = false;  // past the slop: the drag is ours

  const followed = (touches) => {
    for (const touch of touches) if (touch.identifier === id) return touch;
    return null;
  };

  const start = (ev) => {
    // A second finger is a pinch, and a pinch is the browser's.
    if (ev.touches.length !== 1) { id = null; return; }
    id = ev.touches[0].identifier;
    originY = lastY = ev.touches[0].clientY;
    scrolling = false;
  };

  const move = (ev) => {
    if (id === null) return;
    const touch = followed(ev.touches);
    if (!touch) return;
    if (!scrolling && Math.abs(touch.clientY - originY) < TOUCH_SLOP) return;
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
    id = null;
    scrolling = false;
  };

  host.addEventListener('touchstart', start, { passive: true });
  host.addEventListener('touchmove', move, { passive: false });
  host.addEventListener('touchend', end, { passive: true });
  host.addEventListener('touchcancel', end, { passive: true });
  return () => {
    host.removeEventListener('touchstart', start);
    host.removeEventListener('touchmove', move);
    host.removeEventListener('touchend', end);
    host.removeEventListener('touchcancel', end);
  };
}

/* ------------------------------------------------------------ clipboard */

// Whether this device's copy-and-paste key is Cmd rather than Ctrl. iPadOS
// says "MacIntel" and an iPhone says "iPhone", and both of them are Cmd on
// the keyboard somebody attaches to them.
const IS_MAC = /Mac|iPhone|iPad|iPod/.test(
  (navigator.platform || '') + ' ' + (navigator.userAgent || ''),
);

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
 * between the two is made synchronously.
 */
function writeClipboard(text) {
  if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).catch((err) => console.error('copy', err));
    return true;
  }
  const field = document.createElement('textarea');
  field.value = text;
  field.setAttribute('aria-hidden', 'true');
  field.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;';
  document.body.append(field);
  field.select();
  let done = false;
  try { done = document.execCommand('copy'); } catch { /* answered below */ }
  field.remove();
  return done;
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
 * Copying is the other half, and it was not there at all: xterm 6 keeps its
 * selection in its own model and never puts one in the DOM, so the browser
 * has nothing to copy and the `copy` event it fires is empty. Ctrl-C is
 * therefore copy when there is a selection to copy, and the interrupt it has
 * always been when there is not - the rule Windows Terminal and VS Code both
 * settled on - and the selection is dropped once it is taken, so that the
 * next Ctrl-C is an interrupt again. A copy that fails falls through to the
 * interrupt rather than swallowing it.
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
    windowOptions: { getWinSizeChars: true, getWinSizePixels: true },
    theme: LIGHT_THEME,
  });

  term.loadAddon(new Unicode11Addon.Unicode11Addon());
  term.unicode.activeVersion = '11';
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new WebLinksAddon.WebLinksAddon());
  term.loadAddon(new ClipboardAddon.ClipboardAddon());   // OSC 52
  term.open(host);

  // Ctrl-C and Ctrl-V, which xterm on its own gets wrong in both directions.
  wireClipboard(term);

  if (opts.webgl !== false) {
    try {
      const gl = new WebglAddon.WebglAddon();
      // iOS drops the WebGL context whenever the tab goes to the background,
      // and a lost context that is not disposed of paints nothing at all.
      gl.onContextLoss(() => { gl.dispose(); });
      term.loadAddon(gl);
    } catch { /* the DOM renderer; 6.x has no canvas renderer to fall back to */ }
  }

  // A finger, which xterm 6 does nothing with on its own.
  const unwireTouch = touchScroll(host, term);

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
