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

  // Whether this terminal takes typing at all.
  //
  // In auto mode it does not: the promise of that mode is that no keyboard
  // ever opens, and a tap on the pane is exactly what would open one - xterm
  // moves the focus into its own hidden textarea, and a phone puts a keyboard
  // under it. Output, scrolling, selection and every path that sends bytes
  // from somewhere else are untouched; the one thing that stops is the field.
  let typing = true;
  if (term.textarea) {
    term.textarea.addEventListener('focus', () => {
      if (!typing) term.textarea.blur();
    });
  }
  const setTyping = (on) => {
    typing = on !== false;
    const area = term.textarea;
    if (!area) return;
    area.readOnly = !typing;
    area.tabIndex = typing ? 0 : -1;
    if (!typing && document.activeElement === area) area.blur();
  };

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
  // needs it is the one opening the socket: it reveals the composer and the
  // key bar and then asks for the size, and an answer one layout behind
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
    focus: () => { if (typing) term.focus(); },
    /** setTyping opens or closes the one field a terminal has. */
    setTyping,
    /** typing says whether this pane currently takes keystrokes. */
    typing: () => typing,
    dispose() {
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
