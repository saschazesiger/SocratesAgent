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

// How often a drag is allowed to turn into traffic. A finger at sixty frames a
// second would otherwise be sixty batches a second on the link this product
// exists for; twenty is smooth and is a third of that.
const SCROLL_BATCH_MS = 50;

// The pixel delta each synthesised wheel event carries on the mouse-report
// route. Anything under 50 is damped to 30% by xterm before it decides whether
// the event is worth a report at all; the magnitude itself is discarded by the
// report, so the only thing this number has to do is clear that threshold.
const WHEEL_DELTA = 60;

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
 * There are two histories a pane can have, and only one of them is tmux's.
 * Which one this pane has is decided by the **server**, because xterm cannot
 * tell: a tmux client draws every pane on the alternate screen and turns mouse
 * tracking on for all of them, whatever the program inside is doing, so
 * `buffer.active.type` and `modes.mouseTrackingMode` read the same for a shell
 * and for Claude Code. `wheelPane()` is what the server answered from
 * `#{alternate_on}`.
 *
 * A **pane on the normal buffer** is the Shell harness, and that is the whole
 * of the list. The drag goes to the server as a `scroll` control frame and
 * moves tmux's own copy mode - the same history a person at a desk reaches
 * with a prefix and PageUp - which works whatever the program thinks about the
 * mouse and whatever `terminal.mouse` is set to, because none of them is
 * consulted.
 *
 * A **pane on the alternate screen** is all three CLIs. Codex and OpenCode
 * draw a full screen; Claude Code is one too **as this product launches it**,
 * because the fixed policy sets `CLAUDE_CODE_NO_FLICKER=1`
 * (`internal/harnesses/claude.go`) and with it claude takes the alternate
 * screen and tracks the mouse from its idle prompt onward. A bare `claude`
 * without that variable stays on the normal buffer, which is why this was easy
 * to get wrong. tmux keeps no history for an alternate screen - verified,
 * `copy-mode` plus twenty `scroll-up` leaves `capture-pane` returning the same
 * three lines, because tmux's scrollback belongs to the normal buffer and the
 * alternate screen is a window over it with nothing above. The only thing that
 * can scroll there is the program's own view, and a program that draws a full
 * screen tracks the mouse in order to be asked - so the drag becomes the wheel
 * it asked for, which tmux forwards to it as a mouse report. With nobody
 * tracking the mouse at all there is nothing to send and nothing to move, and
 * the drag is swallowed, as it always was.
 *
 * Momentum is the other half of the gesture: a phone that stops dead the
 * instant a finger leaves the glass does not read as scrolling. The last
 * moves give a velocity, and it is decayed frame by frame for about half a
 * second, cancelled by the next touch - and by the next keystroke, which is
 * what `cancel()` is for.
 */
function touchScroll(host, term, onScroll, wheelPane) {
  // Which history this drag is moving, decided when it starts so that the
  // glide after it cannot change its mind half way.
  //   'tmux'  - the server scrolls tmux's copy mode
  //   'wheel' - the program's own view, through the mouse report it asked for
  //   'none'  - there is nothing to scroll, and the drag is swallowed
  const pathFor = () => {
    if (onScroll && !(wheelPane && wheelPane())) return 'tmux';
    return term.modes.mouseTrackingMode === 'none' ? 'none' : 'wheel';
  };
  // The route the current batch is taking. It is not settled once per drag:
  // emit() asks again for every batch, because a program that goes to the
  // alternate screen changes the answer half way through a gesture.
  let path = 'none';
  let emitting = false;   // inside emit's own dispatch, so cancel() ignores it
  let id = null;          // the finger being followed, or none
  let originY = 0;        // where it went down, for the slop
  let lastY = 0;          // where it was last seen, for the delta
  let scrolling = false;  // past the slop: the drag is ours
  let pending = 0;        // lines asked for and not yet sent
  let sentAt = 0;         // when the last batch went, for the throttle
  let flushTimer = null;
  let glide = null;       // the momentum animation
  const samples = [];     // {t, y} of the last moves, for the velocity

  // A line, in pixels. Asked of the pane rather than of a private field, so a
  // font-size change is picked up without anything having to be told.
  const lineHeight = () => {
    const screen = host.querySelector('.xterm-screen');
    const rows = term.rows || 24;
    const h = screen ? screen.clientHeight / rows : 0;
    return h > 2 ? h : 17;
  };

  // emit is the one place a measured number of lines becomes movement, so the
  // glide and the drag travel by exactly the same route.
  //
  // On the wheel route it is one event per line, and deliberately not one
  // event carrying the whole distance. A program tracking the mouse is sent a
  // mouse *report* per wheel event, and a report has no magnitude:
  // `ESC [ < 64 ; 40 ; 12 M` says "the wheel turned up", once, whether the
  // event carried seventeen pixels or six hundred. One batched event every
  // fifty milliseconds therefore moved a Claude Code transcript two or three
  // lines for a drag the length of the screen - measured. So the batch is
  // unrolled, and the distance the finger travelled is the distance the
  // program is asked for.
  //
  // WHEEL_DELTA is why the events are not one line tall. xterm decides whether
  // a wheel event is worth a report in `consumeWheelEvent`, and that function
  // damps a pixel delta under 50 to **thirty per cent** before accumulating it
  // - a trackpad's inertia is meant to be gentler than a notched wheel - so a
  // one-line event is worth 0.3 of a line and only every fourth one produces a
  // report. Measured: 37 events, 11 reports. Over 50 there is no damping, the
  // magnitude is thrown away by the report anyway, and one event is one line.
  const emit = (lines) => {
    // Asked again for every batch rather than once for the drag: a program
    // that goes to the alternate screen changes the answer, and the server
    // says so in the reply to the first batch. Re-reading it here is what
    // makes the rest of the same drag land the right way round.
    path = pathFor();
    if (path === 'tmux') { onScroll(lines); return; }
    if (path !== 'wheel') return;
    const screen = host.querySelector('.xterm-screen');
    if (!screen) return;
    const box = screen.getBoundingClientRect();
    const step = lineHeight();
    // Big enough that xterm neither damps it nor rounds it away, whatever the
    // font size is. What it is worth is one report either way.
    const delta = Math.max(WHEEL_DELTA, step * 2);
    const clientX = Math.round(box.left + box.width / 2);
    const clientY = Math.round(box.top + box.height / 2);
    // A finger moving down brings earlier lines down with it, which is a wheel
    // turned up. The cap is a guard against a pathological batch rather than a
    // policy: a whole screen is more than one batch ever holds.
    const count = Math.min(Math.abs(lines), 120);
    const up = lines > 0;
    // What this dispatch produces is the terminal answering a gesture, not
    // somebody typing, so cancel() must not read it as a keystroke and call
    // off the very glide that is producing it.
    emitting = true;
    try {
      for (let i = 0; i < count; i += 1) {
        screen.dispatchEvent(new WheelEvent('wheel', {
          deltaY: up ? -delta : delta,
          deltaMode: 0,
          clientX,
          clientY,
          bubbles: true,
          cancelable: true,
        }));
      }
    } finally {
      emitting = false;
    }
  };

  const flush = () => {
    if (flushTimer) { clearTimeout(flushTimer); flushTimer = null; }
    const whole = pending > 0 ? Math.floor(pending) : Math.ceil(pending);
    if (!whole) return;
    pending -= whole;
    sentAt = Date.now();
    emit(whole);
  };
  const schedule = () => {
    if (Math.abs(pending) < 1) return;
    const since = Date.now() - sentAt;
    if (since >= SCROLL_BATCH_MS) { flush(); return; }
    if (!flushTimer) flushTimer = setTimeout(flush, SCROLL_BATCH_MS - since);
  };

  const stopGlide = () => { if (glide) { cancelAnimationFrame(glide); glide = null; } };

  /**
   * cancel calls the whole gesture off, including whatever is still in flight.
   *
   * It exists because of what a glide is: a scroll that outlives the finger.
   * Type a key while one is still running and the pane leaves copy mode for
   * the keystroke - correctly - and the next glide batch puts it straight back
   * in, so the screen jumps away from what was just typed and the pill comes
   * back. A drag as ordinary as fifteen steps glides, so this was every
   * drag-then-type.
   *
   * Everything that puts input into the pane calls it: the terminal's own
   * keystrokes, the key bar, a paste, a dictated line, an operator run, and
   * the "Jump to live" pill.
   */
  const cancel = () => {
    if (emitting) return;
    stopGlide();
    pending = 0;
    if (flushTimer) { clearTimeout(flushTimer); flushTimer = null; }
  };

  // A drag that ends with the finger moving keeps moving, and slows down.
  // 0.9 a frame is about half a second of travel at sixty frames a second,
  // which is what a phone's own lists feel like.
  const startGlide = () => {
    stopGlide();
    const now = performance.now();
    const recent = samples.filter((s) => now - s.t < 120);
    if (recent.length < 2) return;
    const first = recent[0];
    const last = recent[recent.length - 1];
    const dt = last.t - first.t;
    if (dt <= 0) return;
    let velocity = (last.y - first.y) / dt;   // pixels per millisecond
    if (Math.abs(velocity) < 0.15) return;
    if (Math.abs(velocity) > 6) velocity = Math.sign(velocity) * 6;
    let previous = now;
    const step = (t) => {
      const dtms = Math.min(t - previous, 40);
      previous = t;
      pending += (velocity * dtms) / lineHeight();
      schedule();
      velocity *= 0.9 ** (dtms / 16.7);
      if (Math.abs(velocity) < 0.02) { glide = null; flush(); return; }
      glide = requestAnimationFrame(step);
    };
    glide = requestAnimationFrame(step);
  };

  const followed = (touches) => {
    for (const touch of touches) if (touch.identifier === id) return touch;
    return null;
  };

  const start = (ev) => {
    // A finger on a pane that is still gliding stops it, the way it does
    // everywhere else on a phone.
    stopGlide();
    pending = 0;
    samples.length = 0;
    // A second finger is a pinch, and a pinch is the browser's.
    if (ev.touches.length !== 1) { id = null; return; }
    id = ev.touches[0].identifier;
    originY = lastY = ev.touches[0].clientY;
    samples.push({ t: performance.now(), y: originY });
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
    samples.push({ t: performance.now(), y: touch.clientY });
    if (samples.length > 8) samples.shift();
    // Whatever this drag turns out to scroll, it is not the page: taken before
    // the browser can rubber-band, and before a release can synthesise the
    // mouse events of a tap the pane would send on to the program.
    if (ev.cancelable) ev.preventDefault();
    if (!dy) return;
    // A finger moving down brings earlier lines down with it, which is a step
    // back into whichever history this drag is moving.
    pending += dy / lineHeight();
    schedule();
  };

  const end = (ev) => {
    if (id === null || !followed(ev.changedTouches)) return;
    id = null;
    if (scrolling) { flush(); startGlide(); }
    scrolling = false;
  };

  host.addEventListener('touchstart', start, { passive: true });
  host.addEventListener('touchmove', move, { passive: false });
  host.addEventListener('touchend', end, { passive: true });
  host.addEventListener('touchcancel', end, { passive: true });
  return {
    cancel,
    dispose() {
      stopGlide();
      if (flushTimer) clearTimeout(flushTimer);
      host.removeEventListener('touchstart', start);
      host.removeEventListener('touchmove', move);
      host.removeEventListener('touchend', end);
      host.removeEventListener('touchcancel', end);
    },
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

  // A finger, which xterm 6 does nothing with on its own.
  const touch = touchScroll(host, term, opts.onScroll || null, opts.wheelPane || null);

  // The hidden textarea is a text field like any other as far as a phone is
  // concerned, and a terminal is the one place autocorrect must never reach.
  if (term.textarea) {
    // Sixteen pixels, whatever the terminal is set to. iOS Safari zooms the
    // whole page in when a field under 16px takes focus, and this field takes
    // focus every time somebody taps the pane to type - so a 14px terminal
    // meant a zoomed, sideways-scrolling page on every single tap. Nothing is
    // drawn from this element; it is xterm's hidden input.
    term.textarea.style.fontSize = '16px';
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
    // Anything typed calls the gesture off first. A glide outlives the finger,
    // and a glide still running when a key lands puts the pane straight back
    // into the copy mode the keystroke had just taken it out of.
    touch.cancel();
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
    /**
     * cancelScroll calls off a scroll gesture that is still in flight.
     *
     * Everything that puts input into this pane from outside the terminal -
     * the key bar, a paste, a dictated line, an operator run - goes through
     * the socket rather than through xterm, so the socket calls this before
     * every frame it sends. The pill calls it too.
     */
    cancelScroll: () => touch.cancel(),
    focus: () => term.focus(),
    dispose() {
      touch.dispose();
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
