// The terminal dock: every session of the open chat, in one panel beside the
// conversation.
//
// A terminal used to live inside the transcript, which read badly and scrolled
// worse: the screen a program paints is not a step in a story, it is a place
// you look at while the story goes on next to it. So the transcript keeps one
// line saying a session was opened, and the session itself lives here - docked
// on the right on a desktop, a full height drawer on a phone.
//
// Socrates and the person share this keyboard. Whatever either of them sends
// goes to the same program, which is the whole point of showing it at all.

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

const QUICK_KEYS = ['enter', 'escape', 'tab', 'up', 'down', 'ctrl+c', 'ctrl+d'];

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
  if (run.s) cls += ' ts';
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
        + (run.u ? 'u' : '') + (run.s ? 's' : '');
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

export function mountTerminalDock() {
  const dom = {
    btn: $('termBtn'),
    badge: $('termBadge'),
    dock: $('termDock'),
    tabs: $('dockTabs'),
    screen: $('dockScreen'),
    status: $('dockStatus'),
    form: $('dockForm'),
    input: $('dockInput'),
    keys: $('dockKeys'),
    error: $('dockError'),
    close: $('dockClose'),
    grip: $('dockGrip'),
    meta: $('dockMeta'),
    empty: $('dockEmpty'),
    dismiss: $('dockDismiss'),
  };
  if (!dom.dock || !dom.btn) return null;

  const state = {
    chatId: null,
    // session id -> record. Insertion order is the tab order, which is the
    // order the sessions were opened in.
    sessions: new Map(),
    active: null,
    open: false,
    // The last screen text painted, so a redraw several times a second does
    // not throw away a selection or reset the scroll position for nothing.
    painted: '',
    resizeTimer: null,
    lastError: null,
    // Sessions the person took off the shelf. A finished session must not come
    // back the next time the transcript repeats itself.
    dismissed: new Set(),
    // session id -> its tab. The tabs are kept and patched rather than made
    // again, because each one carries the dot that says the session is alive.
    tabs: new Map(),
  };

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
      cols: 0,
      rows: 0,
      stream: null,
      gone: false,
      ended: false,
      sentCols: 0,
    };
    state.sessions.set(id, session);
    if (!state.active) state.active = id;
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
      name: step.title || detail.skill || detail.tool || 'terminal',
      command: detail.command || '',
      cols: detail.cols || 0,
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
      return;
    }
    if (state.chatId !== chatId) return;
    // Oldest first, so the tabs are in the order the sessions were opened
    // whichever source got here first.
    const list = [...((data && data.terminals) || [])].sort((a, b) => (a.started_at || 0) - (b.started_at || 0));
    for (const term of list) {
      note(term.id, {
        name: term.name || 'terminal',
        command: term.command || '',
        screen: term.screen || '',
        cols: term.cols || 0,
        rows: term.rows || 0,
        running: term.running,
        exitCode: term.exit_code || 0,
      });
    }
    render();
  }

  function setChat(chatId) {
    if (state.chatId === chatId) return;
    for (const session of state.sessions.values()) stopStream(session);
    state.sessions.clear();
    state.dismissed.clear();
    state.tabs.clear();
    dom.tabs.innerHTML = '';
    state.active = null;
    state.painted = '';
    state.lastError = null;
    state.chatId = chatId || null;
    state.open = chatId ? readValue(openKey(chatId)) === '1' : false;
    applyWidth();
    render();
    if (chatId) loadList(chatId);
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
        session.cols = term.cols || session.cols;
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
  // attempted once and, if it did not get through, the dock says so and keeps
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
    dom.error.innerHTML = '';
    dom.error.append(
      el('span', { class: 'msg', text: message + (label ? ' (' + label + ')' : '') }),
      el('button', {
        class: 'again', type: 'button', text: 'Send again',
        onclick: () => {
          const pending = state.lastError;
          if (!pending) return;
          send(pending.text, pending.keys, label).then(() => {
            // The failed text was put back in the box so it could be edited.
            // Once the retry has landed it has to leave, or the next Enter
            // sends the same line a second time.
            if (pending.text && dom.input.value === pending.text) dom.input.value = '';
          }).catch(() => {});
        },
      }),
      el('button', {
        class: 'again', type: 'button', text: 'Dismiss', onclick: clearError,
      }),
    );
    dom.error.hidden = false;
  }

  function clearError() {
    state.lastError = null;
    dom.error.hidden = true;
    dom.error.innerHTML = '';
  }

  /* --------------------------------------------------------------- sizes */

  // The server renders the screen at a fixed width, so a session opened at 160
  // columns wraps into nonsense in a 60 column drawer on a phone. Measuring the
  // real character width and telling the server about it is what makes the two
  // agree.
  function measureCols() {
    const probeEl = el('span', {
      class: 'term-measure',
      text: 'MMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMM',
    });
    dom.screen.append(probeEl);
    const width = probeEl.getBoundingClientRect().width / 40;
    probeEl.remove();
    if (!width || !isFinite(width)) return 0;
    const styles = getComputedStyle(dom.screen);
    const padding = parseFloat(styles.paddingLeft || '0') + parseFloat(styles.paddingRight || '0');
    const usable = dom.screen.clientWidth - padding;
    if (usable <= 0) return 0;
    return Math.max(40, Math.min(400, Math.floor(usable / width)));
  }

  function syncSize() {
    clearTimeout(state.resizeTimer);
    state.resizeTimer = setTimeout(() => {
      const session = active();
      if (!state.open || !session || !session.running) return;
      const cols = measureCols();
      // Remembered per session: each one has to be told the width of the panel
      // it is being shown in, and switching tabs must not make the dock think
      // the new one already knows.
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

  function active() {
    if (state.active && state.sessions.has(state.active)) return state.sessions.get(state.active);
    const first = state.sessions.keys().next();
    state.active = first.done ? null : first.value;
    return state.active ? state.sessions.get(state.active) : null;
  }

  function running() {
    let count = 0;
    for (const session of state.sessions.values()) if (session.running) count += 1;
    return count;
  }

  function render() {
    const count = state.sessions.size;
    dom.btn.hidden = count === 0;
    // A chat that has not loaded its sessions yet is not a chat with the panel
    // closed: forgetting that here is how a reload used to lose the dock.
    const live = running();
    setClass(dom.btn, 'live', live > 0);
    dom.badge.hidden = count < 2;
    dom.badge.textContent = String(count);
    // The badge is decoration for the eye; the label is the same fact spelled
    // out, so a screen reader is not left with a bare number.
    const title = count
      ? (live ? live + ' of ' + count + ' terminal session' + (count > 1 ? 's' : '') + ' running'
        : 'Terminal sessions (all finished)')
      : 'Terminal sessions';
    dom.btn.title = title;
    dom.btn.setAttribute('aria-label', title);
    dom.dock.hidden = !state.open || !count;
    setClass(document.body, 'dock-open', !dom.dock.hidden);
    dom.btn.setAttribute('aria-expanded', dom.dock.hidden ? 'false' : 'true');
    if (dom.dock.hidden) return;

    // active() is resolved first: after a dismiss it picks the session that
    // takes over, and renderTabs has to know that to mark the right tab.
    // Doing it the other way round left the panel with no selected tab for a
    // render, and the tablist with no aria-selected="true" at all.
    const session = active();
    renderTabs();
    if (!session) return;

    dom.empty.hidden = !!session.screen;
    const text = session.screen || '';
    const styled = session.styled;
    // The caret belongs to a program that is still running; a finished session
    // is a picture of what happened, not a place to type.
    const caret = session.running && session.cursor && session.cursor.visible ? session.cursor : null;
    const signature = text + '\u0000' + styleSignature(styled, caret);
    if (state.painted !== signature) {
      // Auto scroll only while the person is already at the bottom: someone
      // reading back through what happened must not be yanked to the end by
      // the next redraw.
      const atBottom = dom.screen.scrollHeight - dom.screen.scrollTop - dom.screen.clientHeight < 24;
      // Colours when the server sent them, plain text when it did not, which
      // is what an old server and an uncoloured program both look like.
      if (styled) dom.screen.replaceChildren(styledFragment(styled, caret));
      else dom.screen.textContent = text;
      state.painted = signature;
      if (atBottom) dom.screen.scrollTop = dom.screen.scrollHeight;
    }

    dom.meta.textContent = session.command || '';
    dom.form.hidden = !session.running;
    dom.keys.hidden = !session.running;
    paintStatus();
    syncSize();
  }

  // The tabs are patched, never rebuilt. Each one carries the dot that says its
  // session is alive, and a dot that was thrown away and made again whenever
  // another session opened - or whenever the screen it belongs to repainted -
  // never got far enough into its pulse to look like anything but a blink.
  //
  // Keying by session id also means a tab finally notices its session ending:
  // the old signature only knew which sessions existed, so a finished one kept
  // a pulsing green dot until the whole panel was rebuilt for another reason.
  function renderTabs() {
    setClass(dom.tabs, 'single', state.sessions.size <= 1);

    for (const [id, tab] of state.tabs) {
      if (state.sessions.has(id)) continue;
      tab.remove();
      state.tabs.delete(id);
    }

    let previous = null;
    for (const session of state.sessions.values()) {
      let tab = state.tabs.get(session.id);
      if (!tab) {
        tab = buildTab(session.id);
        state.tabs.set(session.id, tab);
      }
      tab.update(session);
      // Only ever moved when it is genuinely in the wrong place: taking a node
      // out of the page and putting it back restarts everything animating
      // inside it, which is the very thing this is here to avoid.
      const inPlace = previous ? previous.nextElementSibling === tab : dom.tabs.firstElementChild === tab;
      if (!inPlace) {
        if (previous) previous.after(tab);
        else dom.tabs.prepend(tab);
      }
      previous = tab;
    }
  }

  function buildTab(id) {
    const dot = el('span', { class: 'dot' });
    const name = el('span', { class: 'nm' });
    const tab = el('button', {
      class: 'dock-tab',
      type: 'button',
      role: 'tab',
      onclick: () => {
        if (state.active === id) return;
        state.active = id;
        state.painted = '';
        clearError();
        render();
      },
    }, dot, name);
    tab.update = (session) => {
      const chosen = session.id === state.active;
      setClass(tab, 'active', chosen);
      const selected = chosen ? 'true' : 'false';
      if (tab.getAttribute('aria-selected') !== selected) tab.setAttribute('aria-selected', selected);
      const title = session.command || session.name || 'terminal';
      if (tab.title !== title) tab.title = title;
      const label = session.name || 'terminal';
      if (name.textContent !== label) name.textContent = label;
      // Three states, one dot: nothing here is written unless it changed.
      setClass(dot, 'live', !!session.running);
      setClass(dot, 'failed', !session.running && session.exitCode > 0);
    };
    return tab;
  }

  // paintStatus is the dock's honesty: it says whether what is on screen is
  // still arriving, and for how long it has been standing still if it is not.
  function paintStatus() {
    const session = active();
    if (!session) return;
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
    setClass(dom.dock, 'stale', kind === 'lost' || kind === 'gone');
    const cls = 'dock-status ' + kind;
    if (dom.status.className !== cls) dom.status.className = cls;
    if (dom.status.textContent !== label) dom.status.textContent = label;
    // One control, two meanings: end a session that is still going, take a
    // finished one off the shelf. Both are what a person means by "close".
    const text = session.running ? 'End session' : 'Dismiss';
    if (dom.dismiss.textContent !== text) dom.dismiss.textContent = text;
  }

  // dismiss ends a running session, or clears away a finished one. A finished
  // session is only removed from this panel; the line in the transcript that
  // says it happened stays where it is.
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
      try {
        await api('/api/terminals/' + encodeURIComponent(session.id) + '/close', { method: 'POST', attempts: 1 });
      } catch (err) {
        toast(errorMessage(err), 'error');
        return;
      }
      session.running = false;
      session.ended = true;
      stopStream(session);
      render();
      return;
    }
    stopStream(session);
    state.dismissed.add(session.id);
    state.sessions.delete(session.id);
    const tab = state.tabs.get(session.id);
    if (tab) {
      tab.remove();
      state.tabs.delete(session.id);
    }
    state.active = null;
    state.painted = '';
    clearError();
    render();
  }

  /* ------------------------------------------------------- open and close */

  function openKey(chatId) { return 'socrates.term.open.' + chatId; }
  function widthKey(chatId) { return 'socrates.term.width.' + chatId; }

  function setOpen(open, remember = true) {
    state.open = !!open;
    if (remember && state.chatId) writeValue(openKey(state.chatId), state.open ? '1' : '0');
    state.painted = '';
    render();
    if (state.open) {
      // The input is where a person is about to type; opening the dock and
      // then making them tap once more is a small insult on a phone.
      const session = active();
      if (session && session.running) setTimeout(() => dom.input.focus(), 0);
    }
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

  dom.btn.addEventListener('click', () => setOpen(!state.open));
  dom.close.addEventListener('click', () => setOpen(false));
  dom.dismiss.addEventListener('click', () => { dismissActive().catch(() => {}); });

  dom.form.addEventListener('submit', (event) => {
    event.preventDefault();
    const text = dom.input.value;
    dom.input.value = '';
    // The caret stays where the next keystroke is going, which is the whole
    // difference between a text box and a terminal.
    dom.input.focus();
    send(text, ['enter'], text).catch(() => { dom.input.value = text; });
  });

  for (const key of QUICK_KEYS) {
    dom.keys.append(el('button', {
      class: 'term-key', type: 'button', title: 'Press ' + key, text: key,
      onclick: () => {
        send('', [key], key).catch(() => {});
        dom.input.focus();
      },
    }));
  }

  // A focused screen is a real keyboard. Arrows, control combinations and
  // plain characters all go straight through, so a menu can be answered
  // without the input line below it.
  dom.screen.addEventListener('keydown', (event) => {
    const session = active();
    if (!session || !session.running) return;
    // A phone keyboard composes before it commits; the half finished syllable
    // is not a keystroke for the program.
    if (event.metaKey || event.isComposing) return;
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
    } else if (/^F\d{1,2}$/.test(event.key)) {
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
  });

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
    if (event.key !== 'Escape' || !state.open) return;
    // Escape on the screen belongs to the program - it is how a menu is
    // dismissed - and must not close the drawer out from under it.
    if (event.defaultPrevented || event.target === dom.screen) return;
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
    // open is the transcript asking for a session by name. It carries the step
    // with it so a session that was dismissed earlier comes back rather than
    // leaving the link pointing at nothing.
    open: (id, step, detail) => {
      if (id) state.dismissed.delete(id);
      if (step) noteStep(step, detail);
      if (id && state.sessions.has(id)) {
        state.active = id;
        state.painted = '';
      }
      setOpen(true);
    },
    // tick keeps the "frozen for Ns" line moving even when nothing arrives,
    // which is exactly when it matters.
    tick: () => { if (!dom.dock.hidden) paintStatus(); },
  };
}
