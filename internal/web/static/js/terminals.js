// The terminal of the open chat: one session, in one panel beside the
// conversation - or, in Terminal Mode, filling the pane the conversation
// usually has.
//
// A terminal used to live inside the transcript, which read badly and scrolled
// worse: the screen a program paints is not a step in a story, it is a place
// you look at while the story goes on next to it. So the transcript keeps one
// line saying a session was opened, and the session itself lives here - docked
// on the right on a desktop, a full height drawer on a phone, or the whole
// pane when the chat is being used as a terminal.
//
// A chat has at most one running session, so there is nothing to choose
// between: no tabs, no count, no input line. The screen is the session. You
// tap it and type into the program, and Socrates is at the same keyboard.

import { api, el, toast, confirmDialog, isOffline, errorMessage, setClass, LiveStream, HttpError } from './api.js';

const $ = (id) => document.getElementById(id);

// Keys the browser reports that the server already has a name for. Anything
// not in here and not a single printable character is left to the browser.
const KEY_NAMES = {
  Enter: 'enter',
  Tab: 'tab',
  Backspace: 'backspace',
  Delete: 'delete',
  Escape: 'escape',
  ArrowUp: 'up',
  ArrowDown: 'down',
  ArrowRight: 'right',
  ArrowLeft: 'left',
  Home: 'home',
  End: 'end',
  PageUp: 'pageup',
  PageDown: 'pagedown',
  Insert: 'insert',
};

// The characters the server can turn into a control code. Anything else after
// ctrl is refused there, so it is never sent from here.
const CTRL_CHARS = /^[a-z\[\]\\@ ]$/;

// The server sends the screen twice: as text, and - when anything on it is
// coloured - as lines of runs that share one appearance. The sixteen ANSI
// colours arrive as names the page resolves itself, so the palette is the
// dock's to choose; the 256 colour cube and 24 bit colours arrive ready made.
const COLOR_TOKEN = /^(?:a(?:[0-9]|1[0-5])|fg|bg|#[0-9a-f]{6})$/i;

function cssColor(token) {
  if (typeof token !== 'string' || !COLOR_TOKEN.test(token)) return '';
  if (token[0] === '#') return token;
  if (token === 'fg') return 'var(--term-fg)';
  if (token === 'bg') return 'var(--term-bg)';
  return 'var(--t' + token.slice(1) + ')';
}

function runClass(run) {
  let cls = '';
  if (run.b) cls += ' tb';
  if (run.d) cls += ' td';
  if (run.i) cls += ' ti';
  if (run.u) cls += ' tu';
  return cls.slice(1);
}

// Text is never handed to the browser as markup: a span is built and the
// characters go in as text, whatever the program painted.
function runNode(text, run) {
  const fg = cssColor(run.fg);
  const bg = cssColor(run.bg);
  const cls = runClass(run);
  if (!fg && !bg && !cls) return document.createTextNode(text);
  const span = document.createElement('span');
  if (cls) span.className = cls;
  if (fg) span.style.color = fg;
  if (bg) span.style.background = bg;
  span.textContent = text;
  return span;
}

function caretNode(text) {
  const span = document.createElement('span');
  span.className = 'tcur';
  span.textContent = text || ' ';
  return span;
}

// The screen is replaced in one go, from a fragment built off the page: the
// stream fires on every change, and touching the live document run by run is
// what makes a busy terminal stutter.
function styledFragment(styled, caret) {
  const frag = document.createDocumentFragment();
  for (let y = 0; y < styled.length; y += 1) {
    const line = styled[y] || [];
    let col = 0;
    for (const run of line) {
      const text = typeof run.t === 'string' ? run.t : '';
      const at = caret && caret.row === y ? caret.col - col : -1;
      if (at >= 0 && at < text.length) {
        if (at > 0) frag.append(runNode(text.slice(0, at), run));
        frag.append(caretNode(text.charAt(at)));
        if (at + 1 < text.length) frag.append(runNode(text.slice(at + 1), run));
      } else if (text) {
        frag.append(runNode(text, run));
      }
      col += text.length;
    }
    // A caret past the end of the line is where the next character will go,
    // which on a fresh prompt is the only place it ever is.
    if (caret && caret.row === y && caret.col >= col) {
      if (caret.col > col) frag.append(document.createTextNode(' '.repeat(caret.col - col)));
      frag.append(caretNode(' '));
    }
    if (y < styled.length - 1) frag.append(document.createTextNode('\n'));
  }
  return frag;
}

// A repaint throws away the selection and the scroll position, so it only
// happens when the screen really looks different. The text is compared as
// text; this covers everything else about it in a few hundred characters.
function styleSignature(styled, caret) {
  if (!styled) return '';
  let sig = caret ? caret.row + ',' + caret.col : '';
  for (const line of styled) {
    for (const run of (line || [])) {
      sig += '|' + (typeof run.t === 'string' ? run.t.length : 0)
        + (run.fg || '') + ':' + (run.bg || '')
        + (run.b ? 'b' : '') + (run.d ? 'd' : '') + (run.i ? 'i' : '')
        + (run.u ? 'u' : '');
    }
    sig += ';';
  }
  return sig;
}

// Local storage throws in private windows and with site data blocked, and
// neither is a reason for the dock to stop working.
function readValue(key) {
  try { return localStorage.getItem(key); } catch { return null; }
}

function writeValue(key, value) {
  try {
    if (value === null || value === '') localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch { /* private mode, quota */ }
}

// The one control that says what the pane is doing lives in the top bar, so
// the dock does not own its own openness: it is told, and it reports back the
// times it closes or opens itself - from its own corner, from escape on a
// phone, or from a line in the transcript asking for a session by name.
export function mountTerminalDock(options = {}) {
  const dom = {
    dock: $('termDock'),
    name: $('dockName'),
    close: $('dockClose'),
    grip: $('dockGrip'),
  };
  if (!dom.dock) return null;

  // Two frames, one session. Everything below paints into whichever of them is
  // showing, so the fullscreen terminal is the dock's screen in another box
  // rather than a second copy of the same code.
  const views = {
    dock: {
      root: dom.dock,
      screen: $('dockScreen'),
      status: $('dockStatus'),
      meta: $('dockMeta'),
      empty: $('dockEmpty'),
      error: $('dockError'),
      dismiss: $('dockDismiss'),
      capture: $('dockCapture'),
    },
    full: {
      root: $('termFull'),
      screen: $('fullScreen'),
      status: $('fullStatus'),
      meta: $('fullMeta'),
      empty: $('fullEmpty'),
      error: $('fullError'),
      dismiss: $('fullDismiss'),
      capture: $('fullCapture'),
    },
  };

  const state = {
    chatId: null,
    // session id -> record. Insertion order is the order they were opened in,
    // which is how "the most recent one" is found when none is running.
    sessions: new Map(),
    open: false,
    // Terminal Mode: the session has the whole pane instead of the column.
    full: false,
    // The last screen painted, and the frame it was painted into, so a redraw
    // several times a second does not throw away a selection or reset the
    // scroll position for nothing.
    painted: '',
    paintedIn: null,
    resizeTimer: null,
    lastError: null,
    // Sessions the person took off the shelf. A finished session must not come
    // back the next time the transcript repeats itself.
    dismissed: new Set(),
    // The request that opens the chat's shell, so two taps do not open two.
    opening: null,
  };

  function view() { return state.full ? views.full : views.dock; }
  function other() { return state.full ? views.dock : views.full; }

  /* ------------------------------------------------------------ sessions */

  function record(id) {
    let session = state.sessions.get(id);
    if (session) return session;
    if (state.dismissed.has(id)) return null;
    session = {
      id,
      name: 'terminal',
      command: '',
      running: true,
      exitCode: 0,
      screen: '',
      // The coloured screen and the caret, both from the session's own stream.
      styled: null,
      cursor: null,
      rows: 0,
      stream: null,
      gone: false,
      ended: false,
      sentCols: 0,
    };
    state.sessions.set(id, session);
    return session;
  }

  // note folds in whatever a source knows. The chat steps and the session list
  // both describe the same thing from different angles, and neither is allowed
  // to blank out a field the other filled in.
  function note(id, patch) {
    if (!id) return null;
    const session = record(id);
    if (!session) return null;
    for (const [key, value] of Object.entries(patch)) {
      if (value === undefined || value === null || value === '') continue;
      session[key] = value;
    }
    if (patch.running !== undefined) session.running = !!patch.running;
    if (patch.exitCode !== undefined) session.exitCode = patch.exitCode || 0;
    if (session.running) watch(session);
    else stopStream(session);
    return session;
  }

  // noteStep is a terminal step from the transcript. The step carries the last
  // screen the chat stream delivered, which is what an exited session that the
  // server no longer holds has to fall back on.
  function noteStep(step, detail) {
    const id = detail && detail.session;
    if (!id) return;
    const running = detail.running !== false && step.status === 'running';
    const session = note(id, {
      name: step.title || detail.skill || 'terminal',
      command: detail.command || '',
      rows: detail.rows || 0,
    });
    if (!session) return;
    // A step only ever moves a session from running to finished, never back:
    // the session's own stream is quicker and more truthful than the once a
    // second copy in the transcript.
    if (!running && session.running) {
      session.running = false;
      session.exitCode = detail.exit_code || 0;
      stopStream(session);
    }
    if (!session.screen && step.body) session.screen = step.body;
    render();
  }

  async function loadList(chatId) {
    let data;
    try {
      data = await api('/api/chats/' + encodeURIComponent(chatId) + '/terminals', { attempts: 2 });
    } catch {
      // The transcript already named the sessions; the list only adds their
      // live screens, and it will be tried again when the page wakes.
      return [];
    }
    if (state.chatId !== chatId) return [];
    // Oldest first, so "the most recent session" means the last one here
    // whichever source got to it first.
    const list = [...((data && data.terminals) || [])].sort((a, b) => (a.started_at || 0) - (b.started_at || 0));
    for (const term of list) {
      note(term.id, {
        name: term.name || 'terminal',
        command: term.command || '',
        screen: term.screen || '',
        rows: term.rows || 0,
        running: term.running,
        exitCode: term.exit_code || 0,
      });
    }
    render();
    return list;
  }

  function setChat(chatId) {
    if (state.chatId === chatId) return;
    for (const session of state.sessions.values()) stopStream(session);
    state.sessions.clear();
    state.dismissed.clear();
    state.opening = null;
    state.painted = '';
    state.lastError = null;
    state.chatId = chatId || null;
    state.open = chatId ? readValue(openKey(chatId)) === '1' : false;
    clearError();
    applyWidth();
    render();
    if (chatId) loadList(chatId);
  }

  // ensureTerminal is Terminal Mode's one demand of the server: this chat has
  // a shell running, whether or not it had one a moment ago. One session per
  // chat is the server's rule, so a 409 is not a failure - it is the answer.
  async function ensureTerminal(chatId) {
    const id = chatId || state.chatId;
    if (!id) return null;
    const live = liveSession();
    if (live) return live;
    if (state.opening) return state.opening;
    const attempt = (async () => {
      try {
        const data = await api('/api/chats/' + encodeURIComponent(id) + '/terminals', {
          method: 'POST', attempts: 1, body: {},
        });
        const term = data && data.terminal;
        if (!term || !term.id) return null;
        note(term.id, {
          name: term.name || 'terminal',
          command: term.command || '',
          screen: term.screen || '',
          rows: term.rows || 0,
          running: term.running !== false,
          exitCode: term.exit_code || 0,
        });
        render();
        return state.sessions.get(term.id) || null;
      } catch (err) {
        // The chat already has one. Whatever this page thought, the list is
        // the truth, so it is fetched rather than guessed at.
        if (err instanceof HttpError && err.status === 409) {
          await loadList(id);
          return liveSession() || active();
        }
        throw err;
      } finally {
        state.opening = null;
      }
    })();
    state.opening = attempt;
    return attempt;
  }

  /* ------------------------------------------------------------- streams */

  // watch subscribes to the session's own stream, which is far quicker than
  // the once a second screen that arrives with the chat events.
  //
  // A terminal is the easiest place in the app to be fooled by a stale screen:
  // it looks alive whether or not anything is still arriving. So the stream
  // reconnects itself, the dock says plainly whether the screen is live, and a
  // session that has really gone is reported as gone rather than retried
  // forever.
  function watch(session) {
    if (session.stream || !session.running || session.ended || session.gone) return;
    const stream = new LiveStream({
      url: () => '/api/terminals/' + encodeURIComponent(session.id) + '/events',
      // The chat stream already speaks for the app as a whole; a terminal that
      // ended should not put an alarm at the top of the page.
      reportsGlobal: false,
      onMessage: (payload) => {
        if (payload && payload.type === 'closed') {
          session.ended = true;
          session.running = false;
          stopStream(session);
          render();
          return;
        }
        const term = payload && payload.terminal;
        if (!term) return;
        session.screen = term.screen || '';
        // Assigned rather than folded in: a screen that stopped being coloured
        // has to lose its colours, not keep the last ones it had.
        session.styled = Array.isArray(term.styled) ? term.styled : null;
        session.cursor = term.cursor || null;
        session.running = !!term.running;
        session.exitCode = term.exit_code || 0;
        session.rows = term.rows || session.rows;
        if (term.name) session.name = term.name;
        if (term.command) session.command = term.command;
        if (!session.running) {
          session.ended = true;
          stopStream(session);
        }
        render();
      },
      onStatus: () => {
        paintStatus();
        // Repeated failures may mean the session is simply gone - after a
        // restart, or once it was cleaned up. Asking once settles it instead
        // of reconnecting to nothing until the page is closed.
        if (stream.attempt === 3) probe(session);
      },
    });
    session.stream = stream;
    stream.start();
  }

  async function probe(session) {
    try {
      await api('/api/terminals/' + encodeURIComponent(session.id), { attempts: 1 });
    } catch (err) {
      if (err instanceof HttpError && err.status === 404) {
        session.gone = true;
        session.running = false;
        stopStream(session);
        render();
      }
    }
  }

  function stopStream(session) {
    if (!session.stream) return;
    session.stream.stop();
    session.stream = null;
  }

  function stopAll() {
    for (const session of state.sessions.values()) stopStream(session);
  }

  /* -------------------------------------------------------------- typing */

  // Typing into a session is the one thing here that must not be retried
  // blindly: a keystroke that arrives twice is a different keystroke. So it is
  // attempted once and, if it did not get through, the panel says so and keeps
  // what was typed within reach rather than swallowing it.
  function send(text, keys, describe) {
    const session = active();
    if (!session) return Promise.resolve();
    return api('/api/terminals/' + encodeURIComponent(session.id) + '/input', {
      method: 'POST',
      attempts: 1,
      body: { text: text || '', keys: keys || [] },
    }).then((result) => {
      // The notice stays up until something actually lands. Clearing it on
      // every attempt meant the next keystroke quietly erased the record of
      // the one that never arrived.
      clearError();
      return result;
    }).catch((err) => {
      showError(isOffline(err)
        ? 'No connection — that never reached the terminal.'
        : errorMessage(err), describe || (text || (keys || []).join(' ')), text, keys);
      throw err;
    });
  }

  function showError(message, label, text, keys) {
    state.lastError = { text, keys };
    const box = view().error;
    box.innerHTML = '';
    box.append(
      el('span', { class: 'msg', text: message + (label ? ' (' + label + ')' : '') }),
      el('button', {
        class: 'again', type: 'button', text: 'Send again',
        onclick: () => {
          const pending = state.lastError;
          if (!pending) return;
          send(pending.text, pending.keys, label).catch(() => {});
        },
      }),
      el('button', {
        class: 'again', type: 'button', text: 'Dismiss', onclick: clearError,
      }),
    );
    box.hidden = false;
  }

  function clearError() {
    state.lastError = null;
    for (const box of [views.dock.error, views.full.error]) {
      if (!box) continue;
      box.hidden = true;
      box.innerHTML = '';
    }
  }

  /* --------------------------------------------------------------- sizes */

  // The server renders the screen at a fixed width, so a session opened at 160
  // columns wraps into nonsense in a 60 column drawer on a phone. Measuring the
  // real character width and telling the server about it is what makes the two
  // agree - and it has to be measured in whichever frame is on screen, because
  // a full pane and a column are not the same width.
  function measureCols() {
    const screen = view().screen;
    const probeEl = el('span', {
      class: 'term-measure',
      text: 'MMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMM',
    });
    screen.append(probeEl);
    const width = probeEl.getBoundingClientRect().width / 40;
    probeEl.remove();
    if (!width || !isFinite(width)) return 0;
    const styles = getComputedStyle(screen);
    const padding = parseFloat(styles.paddingLeft || '0') + parseFloat(styles.paddingRight || '0');
    const usable = screen.clientWidth - padding;
    if (usable <= 0) return 0;
    return Math.max(40, Math.min(400, Math.floor(usable / width)));
  }

  function syncSize() {
    clearTimeout(state.resizeTimer);
    state.resizeTimer = setTimeout(() => {
      const session = active();
      if (!showing() || !session || !session.running) return;
      const cols = measureCols();
      // Remembered per session: each one has to be told the width of the frame
      // it is being shown in, and moving between the dock and the full pane
      // must not let the session think it already knows.
      if (!cols || cols === session.sentCols) return;
      session.sentCols = cols;
      api('/api/terminals/' + encodeURIComponent(session.id) + '/resize', {
        method: 'POST',
        attempts: 1,
        body: { cols, rows: session.rows || 48 },
      }).catch(() => { session.sentCols = 0; /* try again on the next change */ });
    }, 350);
  }

  /* ------------------------------------------------------------ rendering */

  // One chat, one terminal. The running session is the one that matters; when
  // none is running the most recent one is the picture of what happened.
  function active() {
    let live = null;
    let latest = null;
    for (const session of state.sessions.values()) {
      if (!live && session.running) live = session;
      latest = session;
    }
    return live || latest;
  }

  function liveSession() {
    for (const session of state.sessions.values()) if (session.running) return session;
    return null;
  }

  // showing answers "is a terminal actually on screen right now", which is what
  // resizing and the status ticker both depend on.
  function showing() {
    return state.full || state.open;
  }

  function render() {
    if (views.full.root) views.full.root.hidden = !state.full;
    // A column that was asked for is shown while the shell is still being
    // started: an empty panel saying so is the truth, and a panel that only
    // appears once the server answers looks like the tap did nothing.
    dom.dock.hidden = state.full || !state.open;
    if (!showing()) return;

    const session = active();
    renderName(session);
    const frame = view();
    // Moving between the dock and the full pane means the screen in hand has
    // never been painted, whatever the last signature said.
    if (state.paintedIn !== frame) {
      state.painted = '';
      state.paintedIn = frame;
    }
    if (!session) {
      if (state.painted !== '\0none') {
        frame.screen.textContent = '';
        state.painted = '\0none';
      }
      frame.empty.hidden = false;
      frame.empty.textContent = 'Starting a shell…';
      frame.meta.textContent = '';
      frame.status.className = 'dock-status';
      frame.status.textContent = '';
      frame.dismiss.hidden = true;
      return;
    }
    frame.dismiss.hidden = false;

    frame.empty.hidden = !!session.screen;
    frame.empty.textContent = 'Waiting for the program to paint its first screen…';
    const text = session.screen || '';
    const styled = session.styled;
    // The caret belongs to a program that is still running; a finished session
    // is a picture of what happened, not a place to type.
    const caret = session.running && session.cursor && session.cursor.visible ? session.cursor : null;
    const signature = text + '\0' + styleSignature(styled, caret);
    if (state.painted !== signature) {
      // Auto scroll only while the person is already at the bottom: someone
      // reading back through what happened must not be yanked to the end by
      // the next redraw.
      const atBottom = frame.screen.scrollHeight - frame.screen.scrollTop - frame.screen.clientHeight < 24;
      // Colours when the server sent them, plain text when it did not, which
      // is what an uncoloured program looks like.
      if (styled) frame.screen.replaceChildren(styledFragment(styled, caret));
      else frame.screen.textContent = text;
      state.painted = signature;
      if (atBottom) frame.screen.scrollTop = frame.screen.scrollHeight;
    }

    frame.meta.textContent = session.command || '';
    paintStatus();
    syncSize();
  }

  // The dock head says which session this is, and nothing more: there is only
  // ever one, so there is nothing to pick between. The dot is patched rather
  // than replaced, because it is the one mark that says the program is alive.
  function renderName(session) {
    if (!dom.name) return;
    const dot = dom.name.querySelector('.dot');
    const label = dom.name.querySelector('.nm');
    const name = (session && session.name) || 'terminal';
    if (label && label.textContent !== name) label.textContent = name;
    if (dot) {
      setClass(dot, 'live', !!(session && session.running));
      setClass(dot, 'failed', !!(session && !session.running && session.exitCode > 0));
    }
    dom.name.title = (session && (session.command || session.name)) || 'terminal';
  }

  // paintStatus is the panel's honesty: it says whether what is on screen is
  // still arriving, and for how long it has been standing still if it is not.
  function paintStatus() {
    const session = active();
    if (!session) return;
    const frame = view();
    let label = '';
    let kind = '';
    if (session.gone) {
      label = 'session gone';
      kind = 'gone';
    } else if (!session.running) {
      // A session Socrates or the user ended carries no exit code of its own,
      // and "exited (code -1)" reads like a fault rather than a decision.
      if (session.exitCode < 0) label = 'ended';
      else if (session.exitCode) label = 'exited (code ' + session.exitCode + ')';
      else label = 'exited';
      kind = session.exitCode > 0 ? 'failed' : 'done';
    } else if (!session.stream || session.stream.status === 'live') {
      label = 'running';
      kind = 'live';
    } else if (session.stream.status === 'offline') {
      label = 'offline — screen frozen';
      kind = 'lost';
    } else {
      const age = session.stream.secondsSinceData;
      label = age > 2 ? 'reconnecting — frozen ' + age + 's' : 'reconnecting…';
      kind = 'lost';
    }
    setClass(frame.root, 'stale', kind === 'lost' || kind === 'gone');
    const cls = 'dock-status ' + kind;
    if (frame.status.className !== cls) frame.status.className = cls;
    if (frame.status.textContent !== label) frame.status.textContent = label;
    // One control, two meanings: end a session that is still going, take a
    // finished one off the shelf. Both are what a person means by "close".
    const text = session.running ? 'End session' : 'Dismiss';
    if (frame.dismiss.textContent !== text) frame.dismiss.textContent = text;
  }

  // dismiss ends a running session, or clears away a finished one. A finished
  // session is only removed from this panel; the line in the transcript that
  // says it happened stays where it is.
  //
  // Ending is done on screen first and asked for afterwards. Waiting for the
  // server meant a tap that looked like nothing had happened for as long as
  // the program took to die - and the answer was never in doubt.
  async function dismissActive() {
    const session = active();
    if (!session) return;
    if (session.running) {
      const ok = await confirmDialog({
        title: 'End this terminal session?',
        body: 'The program in ' + (session.name || 'this session') + ' is asked to quit. Socrates loses it too.',
        confirmLabel: 'End session',
        danger: true,
      });
      if (!ok) return;
      session.running = false;
      session.ended = true;
      session.exitCode = -1;
      stopStream(session);
      render();
      api('/api/terminals/' + encodeURIComponent(session.id) + '/close', { method: 'POST', attempts: 1 })
        .catch((err) => {
          toast(isOffline(err)
            ? 'No connection — the session may still be running.'
            : errorMessage(err), 'error');
        });
      return;
    }
    stopStream(session);
    state.dismissed.add(session.id);
    state.sessions.delete(session.id);
    state.painted = '';
    clearError();
    render();
  }

  /* ------------------------------------------------------- open and close */

  function openKey(chatId) { return 'socrates.term.open.' + chatId; }
  function widthKey(chatId) { return 'socrates.term.width.' + chatId; }

  // notify is off for the one caller that is already the top bar telling the
  // dock what to do; anything else is news the top bar has to hear.
  function setOpen(open, remember = true, notify = true) {
    const next = !!open;
    const changed = state.open !== next;
    state.open = next;
    if (remember && state.chatId) writeValue(openKey(state.chatId), state.open ? '1' : '0');
    state.painted = '';
    render();
    if (state.open) focusScreen();
    if (notify && changed && typeof options.onOpenChange === 'function') options.onOpenChange(next);
  }

  // setFullscreen hands the same session to the other frame. Nothing about the
  // session changes: the stream stays up, and only the box around it moves.
  function setFullscreen(on) {
    const next = !!on;
    if (state.full === next) return;
    state.full = next;
    state.painted = '';
    render();
    // The width the session was told about belongs to the frame it left.
    const session = active();
    if (session) session.sentCols = 0;
    syncSize();
    if (state.full) focusScreen();
  }

  // A phone will not open its keyboard for anything it was not asked to focus
  // during a tap, so this is only ever a nicety on a desktop.
  function focusScreen() {
    const session = active();
    if (!session || !session.running) return;
    if (window.matchMedia('(pointer: coarse)').matches) return;
    setTimeout(() => { view().capture.focus({ preventScroll: true }); }, 0);
  }

  // One definition of "there is no room for a column beside the chat", shared
  // by the drawer's behaviour and the CSS that draws it.
  function isNarrow() {
    return window.matchMedia('(max-width: 900px)').matches;
  }

  function applyWidth() {
    const stored = state.chatId ? Number(readValue(widthKey(state.chatId))) : 0;
    if (stored > 0) document.documentElement.style.setProperty('--dock-w', stored + 'px');
    else document.documentElement.style.removeProperty('--dock-w');
  }

  /* ---------------------------------------------------------------- wiring */

  dom.close.addEventListener('click', () => setOpen(false));

  // A focused screen is a real keyboard. Arrows, control combinations and
  // plain characters all go straight through, so a menu can be answered
  // without any input line at all.
  function onKeydown(event) {
    const session = active();
    if (!session || !session.running) return;
    // A phone keyboard composes before it commits; the half finished syllable
    // is not a keystroke for the program. It arrives as text below instead.
    if (event.metaKey || event.isComposing || event.keyCode === 229) return;
    // Ctrl+Shift+C and Ctrl+Shift+V are copy and paste, and the browser owns
    // them. Folding the shift away turned a copy into a ctrl+c, which killed
    // the session under review.
    if (event.ctrlKey && event.shiftKey) return;
    let name = null;
    if (event.ctrlKey) {
      // The server has a control code for the letters and a handful of
      // punctuation, and refuses everything else. Ctrl+1 and Ctrl+ArrowUp are
      // left to the browser rather than sent and rejected.
      const ch = event.key.length === 1 ? event.key.toLowerCase() : '';
      if (!CTRL_CHARS.test(ch)) return;
      name = 'ctrl+' + ch;
    } else if (event.altKey) {
      if (event.key.length !== 1) return;
      name = 'alt+' + event.key.toLowerCase();
    } else if (event.key === 'Enter' && event.shiftKey) {
      // A newline inside a prompt box rather than "send", which is how a
      // multi line brief is typed into Claude Code.
      name = 'shift+enter';
    } else if (KEY_NAMES[event.key]) {
      name = event.key === 'Tab' && event.shiftKey ? 'shift+tab' : KEY_NAMES[event.key];
    } else if (/^F([1-9]|1[0-2])$/.test(event.key)) {
      name = event.key.toLowerCase();
    }
    if (name) {
      event.preventDefault();
      send('', [name], name).catch(() => {});
      return;
    }
    if (event.ctrlKey || event.altKey) return;
    if (event.key.length === 1) {
      event.preventDefault();
      send(event.key, [], event.key).catch(() => {});
    }
  }

  // wireView is everything a frame needs to behave like a terminal: a screen
  // that takes the keyboard, a hidden box that makes a phone offer one, and
  // the control that ends the session.
  function wireView(frame) {
    if (!frame.root || !frame.screen) return;
    frame.dismiss.addEventListener('click', () => { dismissActive().catch(() => {}); });
    frame.screen.addEventListener('keydown', onKeydown);

    // The screen is a <pre>, and no phone opens its keyboard for one. Tapping
    // it focuses an invisible box instead, which is what the keyboard actually
    // belongs to; the caret the person watches stays in the screen.
    frame.screen.addEventListener('click', () => {
      const session = active();
      if (!session || !session.running) return;
      // Someone selecting text to copy is not asking for a keyboard.
      const selection = window.getSelection();
      if (selection && String(selection).length) return;
      frame.capture.focus({ preventScroll: true });
    });

    const capture = frame.capture;
    if (!capture) return;
    let composing = false;

    const flush = () => {
      const typed = capture.value;
      capture.value = '';
      if (!typed) return;
      // Autocorrect and dictation commit whole words, sometimes with the
      // return that finished them. The text goes as text, the return as a key.
      const enter = /\n$/.test(typed);
      const text = typed.replace(/\n/g, '');
      if (text) send(text, enter ? ['enter'] : [], text).catch(() => {});
      else if (enter) send('', ['enter'], 'enter').catch(() => {});
    };

    capture.addEventListener('keydown', onKeydown);
    // The keys a phone keyboard refuses to name arrive as edits instead:
    // backspace deletes, return inserts a line break. Both are keystrokes.
    capture.addEventListener('beforeinput', (event) => {
      const kind = event.inputType || '';
      if (kind === 'deleteContentBackward' || kind === 'deleteWordBackward' || kind === 'deleteSoftLineBackward') {
        event.preventDefault();
        send('', ['backspace'], 'backspace').catch(() => {});
        return;
      }
      if (kind === 'insertLineBreak' || kind === 'insertParagraph') {
        event.preventDefault();
        send('', ['enter'], 'enter').catch(() => {});
      }
    });
    capture.addEventListener('compositionstart', () => { composing = true; });
    capture.addEventListener('compositionend', () => { composing = false; flush(); });
    capture.addEventListener('input', (event) => {
      if (composing || event.isComposing) return;
      flush();
    });
    // The caret is drawn in the screen, so the screen has to look focused
    // while the keyboard is really pointed at the box beside it.
    capture.addEventListener('focus', () => setClass(frame.screen, 'focused', true));
    capture.addEventListener('blur', () => setClass(frame.screen, 'focused', false));
  }

  wireView(views.dock);
  wireView(views.full);

  // Dragging the edge is a desktop nicety; it is measured against the window
  // so it behaves the same whether or not the chat sidebar is open.
  if (dom.grip) {
    dom.grip.addEventListener('pointerdown', (event) => {
      event.preventDefault();
      dom.grip.setPointerCapture(event.pointerId);
      const move = (moveEvent) => {
        const width = Math.max(360, Math.min(window.innerWidth - 320, window.innerWidth - moveEvent.clientX));
        document.documentElement.style.setProperty('--dock-w', width + 'px');
      };
      const up = () => {
        dom.grip.removeEventListener('pointermove', move);
        dom.grip.removeEventListener('pointerup', up);
        const width = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--dock-w'), 10);
        if (state.chatId && width > 0) writeValue(widthKey(state.chatId), String(width));
        syncSize();
      };
      dom.grip.addEventListener('pointermove', move);
      dom.grip.addEventListener('pointerup', up);
    });
  }

  window.addEventListener('resize', () => syncSize());
  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || !state.open || state.full) return;
    // Escape on the screen belongs to the program - it is how a menu is
    // dismissed - and must not close the drawer out from under it.
    if (event.defaultPrevented || event.target === views.dock.screen || event.target === views.dock.capture) return;
    // On a phone the drawer covers the chat, so escape has to get out of it
    // before anything else claims the key.
    if (isNarrow()) setOpen(false);
  });

  // The streams are the page's, and they leave with it.
  window.addEventListener('beforeunload', stopAll);
  window.addEventListener('pagehide', stopAll);

  return {
    setChat,
    noteStep,
    setFullscreen,
    ensureTerminal,
    // The top bar deciding what the pane shows. It is not news to it, so it is
    // not reported back.
    setOpen: (open) => setOpen(open, true, false),
    // Whether a program is running at this chat's terminal, which is what the
    // dot on the slider says from the other three stops.
    isLive: () => !!liveSession(),
    // open is the transcript asking for a session by name. It carries the step
    // with it so a session that was dismissed earlier comes back rather than
    // leaving the link pointing at nothing.
    open: (id, step, detail) => {
      if (id) state.dismissed.delete(id);
      if (step) noteStep(step, detail);
      setOpen(true);
    },
    // tick keeps the "frozen for Ns" line moving even when nothing arrives,
    // which is exactly when it matters.
    tick: () => { if (showing()) paintStatus(); },
  };
}
