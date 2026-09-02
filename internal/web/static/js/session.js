// The session page: the list on the left, one terminal on the right, and the
// socket between the browser and a tmux pane.
//
// Three things live here, and they are here together because each is a few
// dozen lines and all three are one screen:
//
//   * TermSocket, the client half of §D - binary output frames with a byte
//     sequence number, binary input frames with their own, and JSON control
//     frames for everything that is not bytes.
//   * The session list and what a row can do to a session.
//   * The overlays and notices: a pane that ended, a session that came back
//     after a restart, a size somebody else chose.
//
// The key bar, the line input and dictation live in keybar.js and are mounted
// here, because they send through the same numbered input path everything else
// does and there is exactly one of it per session.

import {
  api, el, toast, infoTip, confirmDialog, errorMessage, isOffline,
  setClass, onWake, clientKey, CONNECTION_GRACE,
} from './api.js';
import { connectionSource } from './net.js';
import { agentMark } from './logos.js';
import * as harnesses from './harnesses.js';
import { createTerm } from './term.js';
import {
  mountKeyBar, mountComposer, keyBarWanted, setKeyBarWanted, followViewport,
} from './keybar.js';

/* ------------------------------------------------------------- transport */

// The frame kinds, which are the first byte of every binary frame.
const OUTPUT = 0x01;
const INPUT = 0x02;

const encoder = new TextEncoder();

// The backoff LiveStream uses, so a dropped socket and a dropped event stream
// come back on the same rhythm.
const BACKOFF_BASE = 700;
const BACKOFF_MAX = 15000;

// The client half of the watchdog, offset from the server's fifteen seconds by
// seven so the two never ping into each other.
const PING_EVERY = 15000;
const PING_TIMEOUT = 10000;
const PING_OFFSET = 7000;

/**
 * viewerId is this tab's identity for as long as the tab lives.
 *
 * It is in sessionStorage rather than localStorage on purpose: two tabs are
 * two viewers of the same session and must not fight over one replay ring,
 * and a reload is the same tab, which is exactly what makes a reload a gap in
 * a byte stream instead of a repaint.
 */
function viewerId() {
  const key = 'socrates.viewer';
  try {
    let id = sessionStorage.getItem(key);
    if (!id) { id = clientKey(); sessionStorage.setItem(key, id); }
    return id;
  } catch { return clientKey(); }
}

/**
 * TermSocket is one viewer's connection to one session.
 *
 * The whole of the "nothing typed is lost, nothing arrives twice" promise is
 * in three rules, and they are all in `hello`:
 *
 *   * the input counter is re-anchored to the server's `input_ack` on every
 *     connect, and never persisted anywhere;
 *   * frames the server has already written are dropped, and what remains is
 *     renumbered contiguously from `input_ack + 1` before anything new is
 *     sent - which is what makes a reload with frames in flight work;
 *   * `viewer_fresh` means the server cannot tell a resend from new input, so
 *     held frames are discarded and the person is told, rather than
 *     duplicated into a shell.
 */
class TermSocket {
  constructor({ sessionId, viewer, onOutput, onControl, onStatus }) {
    this.sessionId = sessionId;
    this.viewer = viewer;
    this.onOutput = onOutput || (() => {});
    this.onControl = onControl || (() => {});
    this.onStatus = onStatus || (() => {});

    this.ws = null;
    this.stopped = true;
    this.attempt = 0;
    this.retryTimer = null;

    // rendered is the last output byte this client has drawn, and the `since`
    // of the next handshake.
    this.rendered = 0;
    // inputSeq is the number of the last input frame handed to the socket,
    // and held is every frame the server has not acknowledged.
    this.inputSeq = 0;
    this.held = [];

    this.pingTimer = null;
    this.pingDeadline = null;
    this.pingId = 0;
    this.lagTimer = null;
    this.lastLag = 0;

    this.size = { cols: 0, rows: 0 };
    this.conn = connectionSource({ global: true });
    // A socket that is open when the radio goes does not error; it simply
    // stops delivering. The device's own signal is the only warning there is.
    this.conn.onOffline(() => { if (!this.stopped) this.reconnect(0); });
    this.unwake = null;
  }

  start(cols, rows) {
    this.stopped = false;
    if (cols && rows) this.size = { cols, rows };
    if (!this.unwake) {
      // The radio coming back, or the screen being looked at, is a better
      // reason to try than any timer - and it starts the backoff again from
      // the beginning, because what failed was the network being gone rather
      // than this server refusing.
      this.unwake = onWake(() => {
        if (this.stopped || this.isOpen()) return;
        this.attempt = 0;
        this.reconnect(0);
      });
    }
    this.open();
  }

  stop() {
    this.stopped = true;
    if (this.isOpen()) {
      try { this.ws.send(JSON.stringify({ t: 'bye' })); } catch { /* going anyway */ }
    }
    this.close();
    if (this.unwake) { this.unwake(); this.unwake = null; }
    this.conn.release();
    this.onStatus('idle');
  }

  isOpen() { return this.ws && this.ws.readyState === WebSocket.OPEN; }

  close() {
    if (this.retryTimer) { clearTimeout(this.retryTimer); this.retryTimer = null; }
    this.stopTimers();
    if (this.ws) {
      const ws = this.ws;
      this.ws = null;
      ws.onopen = null; ws.onmessage = null; ws.onerror = null; ws.onclose = null;
      try { ws.close(); } catch { /* already gone */ }
    }
  }

  stopTimers() {
    if (this.pingTimer) { clearInterval(this.pingTimer); this.pingTimer = null; }
    if (this.pingDeadline) { clearTimeout(this.pingDeadline); this.pingDeadline = null; }
    if (this.lagTimer) { clearInterval(this.lagTimer); this.lagTimer = null; }
  }

  url() {
    const base = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const q = new URLSearchParams({
      viewer: this.viewer,
      since: String(this.rendered),
    });
    if (this.size.cols && this.size.rows) {
      q.set('cols', String(this.size.cols));
      q.set('rows', String(this.size.rows));
    }
    return base + location.host + '/api/sessions/' + encodeURIComponent(this.sessionId) + '/ws?' + q;
  }

  open() {
    this.close();
    if (this.stopped) return;
    // A browser that knows it has no network should wait for the radio rather
    // than burn battery on a handshake that cannot succeed.
    if (navigator.onLine === false) {
      this.report('offline', { immediate: true });
      return;
    }
    this.report('connecting');
    let ws;
    try {
      ws = new WebSocket(this.url(), ['socrates.term.v1']);
    } catch {
      this.reconnect();
      return;
    }
    ws.binaryType = 'arraybuffer';
    this.ws = ws;
    ws.onopen = () => { this.attempt = 0; };
    ws.onmessage = (event) => this.receive(event.data);
    ws.onerror = () => { /* onclose carries the outcome */ };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.stopTimers();
      if (!this.stopped) this.reconnect();
    };
  }

  reconnect(delay) {
    this.close();
    if (this.stopped) return;
    this.attempt += 1;
    const wait = delay === undefined
      ? Math.min(BACKOFF_MAX, BACKOFF_BASE * 2 ** (this.attempt - 1)) * (0.7 + Math.random() * 0.6)
      : delay;
    this.report(navigator.onLine === false ? 'offline' : 'connecting', {
      retryAt: Date.now() + wait,
      retryNow: () => this.reconnect(0),
    });
    this.retryTimer = setTimeout(() => { this.retryTimer = null; this.open(); }, wait);
  }

  report(status, extra) {
    this.conn.report(status, extra);
    this.onStatus(status, extra);
  }

  receive(data) {
    if (typeof data === 'string') {
      let frame;
      try { frame = JSON.parse(data); } catch { return; }
      this.control(frame);
      return;
    }
    const bytes = new Uint8Array(data);
    if (bytes.length < 9 || bytes[0] !== OUTPUT) return;
    const seq = Number(readSeq(bytes, 1));
    const payload = bytes.subarray(9);
    // The header carries the sequence number of the first byte in the frame,
    // so the last byte this client has drawn is that one plus what the frame
    // holds. Sequence numbers are one-based, which is why the -1 is here and
    // `since: 0` means "I have nothing".
    if (payload.length) this.rendered = seq + payload.length - 1;
    this.onOutput(payload);
  }

  control(frame) {
    switch (frame.t) {
      case 'hello':
        this.hello(frame);
        break;
      case 'input_ack':
        this.acked(Number(frame.seq) || 0);
        break;
      case 'pong':
        if (this.pingDeadline) { clearTimeout(this.pingDeadline); this.pingDeadline = null; }
        break;
      default:
        break;
    }
    this.onControl(frame);
  }

  hello(frame) {
    const ack = Number(frame.input_ack) || 0;
    const fresh = !!frame.viewer_fresh;
    // A replay from zero means the server could not fill the gap and attached
    // afresh: everything on screen is about to be repainted.
    if (!Number(frame.replay_from)) this.rendered = 0;

    // Anchor: what the server says it has written is the truth, and this
    // client's own counter never survives a connect.
    this.held = this.held.filter((f) => f.seq > ack);
    this.inputSeq = ack;

    if (fresh && this.held.length) {
      // The server has no memory of this viewer, so it cannot tell a resend
      // from new input. Nothing is duplicated, and the person is told: loose
      // keystrokes are counted, and a composed line goes back into the field
      // it was written in, unsent.
      const lost = this.held;
      this.held = [];
      const lines = [];
      let keystrokes = 0;
      for (const held of lost) {
        if (held.text !== undefined) lines.push(held.text); else keystrokes += 1;
        if (held.onLost) held.onLost();
      }
      this.onControl({ t: 'input_lost', keystrokes, lines });
    } else {
      // Renumbering is safe because the content is what matters: the numbers
      // exist only for dedupe, and hello carries the server's current
      // lastInputSeq rather than a stale batched ack.
      for (const held of this.held) {
        this.inputSeq += 1;
        held.seq = this.inputSeq;
        held.frame = inputFrame(held.seq, held.bytes);
        this.write(held.frame);
      }
    }

    this.attempt = 0;
    this.report('live');
    this.startTimers();
  }

  acked(seq) {
    const kept = [];
    for (const held of this.held) {
      if (held.seq > seq) { kept.push(held); continue; }
      if (held.onDelivered) held.onDelivered();
    }
    this.held = kept;
  }

  startTimers() {
    this.stopTimers();
    const ping = () => {
      if (!this.isOpen()) return;
      this.pingId += 1;
      this.write(JSON.stringify({ t: 'ping', id: this.pingId }));
      if (this.pingDeadline) clearTimeout(this.pingDeadline);
      this.pingDeadline = setTimeout(() => {
        // Silence is not failure on a terminal - a pane is legitimately quiet
        // for hours - but a missed pong is.
        this.pingDeadline = null;
        this.report('connecting', { immediate: true });
        this.reconnect(0);
      }, PING_TIMEOUT);
    };
    setTimeout(() => {
      if (this.stopped || !this.isOpen()) return;
      ping();
      this.pingTimer = setInterval(ping, PING_EVERY);
    }, PING_OFFSET);
    // Diagnostics only: how far behind this viewer is. Nothing in the
    // transport depends on it.
    this.lagTimer = setInterval(() => {
      if (!this.isOpen() || this.rendered === this.lastLag) return;
      this.lastLag = this.rendered;
      this.lag(this.rendered);
    }, 1000);
  }

  write(payload) {
    if (!this.isOpen()) return false;
    try { this.ws.send(payload); return true; } catch { return false; }
  }

  /**
   * sendInput queues one piece of input and sends it if there is a socket.
   *
   * `opts.text` marks the frame as a composed line rather than a keystroke,
   * which is what decides its fate when the server turns out to have
   * forgotten this viewer: keystrokes are counted and dropped, a line is
   * handed back to the person who wrote it. `onDelivered` fires when the
   * server has acknowledged the bytes, `onLost` when they were discarded.
   */
  sendInput(data, opts = {}) {
    const bytes = typeof data === 'string' ? encoder.encode(data) : data;
    if (!bytes.length) return;
    this.inputSeq += 1;
    const entry = {
      seq: this.inputSeq, bytes, frame: inputFrame(this.inputSeq, bytes),
      text: opts.text, onDelivered: opts.onDelivered, onLost: opts.onLost,
    };
    this.held.push(entry);
    this.write(entry.frame);
  }

  resize(cols, rows) {
    if (!cols || !rows) return;
    this.size = { cols, rows };
    this.write(JSON.stringify({ t: 'resize', cols, rows }));
  }

  /** lag reports the last output byte rendered, for the Diagnostics panel. */
  lag(seq) {
    this.write(JSON.stringify({ t: 'lag', seq }));
  }
}

function inputFrame(seq, bytes) {
  const frame = new Uint8Array(9 + bytes.length);
  frame[0] = INPUT;
  writeSeq(frame, 1, seq);
  frame.set(bytes, 9);
  return frame;
}

function readSeq(bytes, at) {
  let n = 0n;
  for (let i = 0; i < 8; i += 1) n = (n << 8n) | BigInt(bytes[at + i]);
  return n;
}

function writeSeq(bytes, at, value) {
  let n = BigInt(value);
  for (let i = 7; i >= 0; i -= 1) { bytes[at + i] = Number(n & 0xffn); n >>= 8n; }
}

/* ------------------------------------------------------------- the page */

// How long a tap waits to see whether it was the last one. Long enough to
// swallow a run down the list, short enough that a single tap is not felt.
const ATTACH_DEBOUNCE = 120;

const state = {
  sessions: [],
  scope: 'active',
  current: null,      // the session row being shown
  socket: null,
  term: null,
  keybar: null,
  composer: null,
  // The attach a rapid run of taps down the list has scheduled, so that the
  // last session tapped is the only one a socket is opened for.
  pendingAttach: null,
  live: false,
  lostAt: 0,
  loading: false,
  // The overflow menu a row or the header last opened, so a second one
  // replaces it rather than stacking on it.
  menu: null,
  // How the terminal is drawn, from the dashboard. The defaults here are what
  // a page that could not ask uses, and they are the same numbers the settings
  // document ships with.
  terminal: { scrollback: 2000, font_size: 14, webgl: true },
};

const dom = {};
const ids = ['sidebar', 'navScrim', 'menuBtn', 'newSession', 'sessionScope', 'sessionList',
  'sessionHarness', 'sessionTitle', 'sessionArchived', 'termSize', 'sessionMenu',
  'termWrap', 'term', 'termOverlay', 'termNotice', 'termEmpty', 'keybar', 'composer',
  'lineInput', 'micBtn', 'recTime', 'logout'];
for (const id of ids) dom[id] = document.getElementById(id);

// The state dot is the only colour in a row, and each state has exactly one.
const DOT = {
  running: 'green', starting: 'green', resuming: 'green',
  needs_resume: 'faint', exited: 'amber', failed: 'red',
};

const STATE_WORDS = {
  running: 'Running', starting: 'Starting', resuming: 'Resuming',
  needs_resume: 'Not running', exited: 'Ended', failed: 'Failed',
};

function sessionOf(id) { return state.sessions.find((s) => s.id === id) || null; }

/* -------------------------------------------------------- the session list */

function renderList() {
  const host = dom.sessionList;
  const wanted = state.sessions.filter((s) => state.scope === 'all' || !s.archived);
  if (!wanted.length) {
    host.innerHTML = '';
    host.append(el('div', { class: 'list-empty', text: state.loading ? 'Loading…' : 'No sessions yet.' }));
    return;
  }
  // The list is patched rather than rebuilt: taking a connected node out of
  // the page and putting it back restarts its animations, and a row that
  // fades in again every few seconds is a page that looks broken.
  const seen = new Set();
  let previous = null;
  for (const session of wanted) {
    seen.add(session.id);
    let row = host.querySelector('[data-id="' + cssId(session.id) + '"]');
    if (!row) {
      row = buildRow(session);
      if (previous) previous.after(row); else host.prepend(row);
    } else {
      updateRow(row, session);
      if (previous ? previous.nextElementSibling !== row : host.firstElementChild !== row) {
        if (previous) previous.after(row); else host.prepend(row);
      }
    }
    previous = row;
  }
  for (const row of [...host.children]) {
    if (row.dataset.id && !seen.has(row.dataset.id)) row.remove();
    else if (!row.dataset.id) row.remove();
  }
}

function cssId(id) { return String(id).replace(/"/g, '\\"'); }

function buildRow(session) {
  const row = el('div', {
    class: 'chat-item',
    'data-id': session.id,
    role: 'button',
    tabindex: '0',
  },
  el('span', { class: 'row-mark' }, agentMark(session.harness, 18)),
  el('span', { class: 'label' }),
  el('span', { class: 'row-tip' }),
  el('span', { class: 'dot' }),
  el('button', {
    class: 'icon-btn act', type: 'button', 'aria-label': 'Session actions',
    html: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><circle cx="12" cy="5" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="12" cy="19" r="1"/></svg>',
    onclick: (event) => { event.stopPropagation(); openRowMenu(event.currentTarget, session.id); },
  }));
  row.addEventListener('click', () => selectSession(session.id));
  row.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); selectSession(session.id); }
  });
  updateRow(row, session);
  return row;
}

// The technical detail of a row - where it works, what it runs on, which
// conversation it is holding, how often it has been brought back - is behind
// an "i" and never in the words of the row.
function rowFacts(session) {
  const facts = [STATE_WORDS[session.state] || session.state, session.workdir];
  if (session.model) facts.push(session.model + (session.effort ? ' · ' + session.effort : ''));
  if (session.cli_session_state && session.cli_session_state !== 'none') {
    facts.push('conversation ' + session.cli_session_state);
  }
  if (session.resume_count) facts.push('resumed ' + session.resume_count + '×');
  return facts.filter(Boolean);
}

function updateRow(row, session) {
  row.querySelector('.label').textContent = session.title;
  setClass(row, 'active', state.current && state.current.id === session.id);
  setClass(row, 'archived', !!session.archived);
  const dot = row.querySelector('.dot');
  dot.className = 'dot ' + (DOT[session.state] || 'faint');
  dot.title = STATE_WORDS[session.state] || session.state;
  const tipHost = row.querySelector('.row-tip');
  const facts = rowFacts(session).join('\n');
  if (tipHost.dataset.facts !== facts) {
    tipHost.dataset.facts = facts;
    tipHost.innerHTML = '';
    tipHost.append(infoTip(rowFacts(session), { label: session.title + ' details', bubbleClass: 'mono' }));
  }
}

async function refreshList() {
  try {
    const data = await api('/api/sessions?scope=' + (state.scope === 'all' ? 'all' : 'active'),
      { attempts: 2, timeout: 12000 });
    state.sessions = data.sessions || [];
    state.loading = false;
    if (state.current) {
      const fresh = sessionOf(state.current.id);
      if (fresh) applySession(fresh);
    }
    renderList();
    return state.sessions;
  } catch (err) {
    state.loading = false;
    renderList();
    if (!isOffline(err)) toast(errorMessage(err), 'error');
    return state.sessions;
  }
}

/* ---------------------------------------------------- the session actions */

async function renameSession(session) {
  const title = await promptTitle(session.title);
  if (title === null || title === session.title) return;
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id), {
      method: 'PATCH', body: { title },
    });
    replaceSession(data.session);
  } catch (err) { toast(errorMessage(err), 'error'); }
}

// promptTitle is a one field dialog, built the way confirmDialog is: the app's
// own shape rather than window.prompt, which a phone renders as a system alert
// and which blocks the page while it is open.
function promptTitle(current) {
  return new Promise((resolve) => {
    const dialog = el('dialog', { class: 'modal' });
    const input = el('input', { class: 'input', type: 'text', value: current });
    const cancel = el('button', { class: 'btn sm', type: 'button', text: 'Cancel' });
    const accept = el('button', { class: 'btn sm primary', type: 'button', text: 'Rename' });
    dialog.append(
      el('h2', { class: 'modal-title', text: 'Rename this session' }),
      el('div', { class: 'field' }, input),
      el('div', { class: 'modal-actions' }, cancel, accept),
    );
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
      dialog.close();
    };
    accept.addEventListener('click', () => finish(input.value.trim() || current));
    cancel.addEventListener('click', () => finish(null));
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') { event.preventDefault(); finish(input.value.trim() || current); }
    });
    dialog.addEventListener('cancel', (event) => { event.preventDefault(); finish(null); });
    dialog.addEventListener('close', () => { finish(null); dialog.remove(); });
    document.body.append(dialog);
    dialog.showModal();
    input.focus();
    input.select();
  });
}

async function archiveSession(session, archived) {
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/archive', {
      method: 'POST', body: { archived },
    });
    replaceSession(data.session);
    toast(archived ? 'Archived. It keeps running.' : 'Back in the active list.');
  } catch (err) { toast(errorMessage(err), 'error'); }
}

async function deleteSession(session) {
  const yes = await confirmDialog({
    title: 'Delete “' + session.title + '”?',
    body: 'The terminal is closed and the session is removed. The working directory and everything in it are kept.',
    confirmLabel: 'Delete',
    danger: true,
  });
  if (!yes) return;
  try {
    await api('/api/sessions/' + encodeURIComponent(session.id), { method: 'DELETE' });
    state.sessions = state.sessions.filter((s) => s.id !== session.id);
    if (state.current && state.current.id === session.id) {
      detach();
      const next = state.sessions.find((s) => !s.archived) || state.sessions[0];
      if (next) selectSession(next.id); else showEmpty();
    }
    renderList();
    toast('Deleted. The working directory was kept.');
  } catch (err) { toast(errorMessage(err), 'error'); }
}

function downloadJournal(session) {
  window.open('/api/sessions/' + encodeURIComponent(session.id) + '/journal', '_blank');
}

function replaceSession(next) {
  if (!next) return;
  const at = state.sessions.findIndex((s) => s.id === next.id);
  if (at >= 0) state.sessions[at] = next; else state.sessions.unshift(next);
  if (state.current && state.current.id === next.id) applySession(next);
  renderList();
}

// One menu shape for both the row's "…" and the header's, so a session can be
// renamed from wherever it is being looked at.
function openRowMenu(anchor, id) {
  const session = sessionOf(id);
  if (!session) return;
  closeMenu();
  const menu = el('div', { class: 'menu', role: 'menu' },
    item('Rename', () => renameSession(session)),
    item(dom.keybar.hidden ? 'Show key bar' : 'Hide key bar', () => {
      const on = dom.keybar.hidden;
      setKeyBarWanted(on);
      showKeyBar(on);
    }),
    item(session.archived ? 'Unarchive' : 'Archive', () => archiveSession(session, !session.archived)),
    item('Download scrollback', () => downloadJournal(session)),
    item('Delete', () => deleteSession(session), 'danger'));
  document.body.append(menu);
  const box = anchor.getBoundingClientRect();
  menu.style.top = Math.round(box.bottom + 6) + 'px';
  menu.style.left = Math.round(Math.min(box.left, window.innerWidth - menu.offsetWidth - 10)) + 'px';
  state.menu = menu;
  setTimeout(() => document.addEventListener('click', closeMenu, { once: true }), 0);

  function item(text, run, kind) {
    return el('button', {
      class: 'menu-item ' + (kind || ''), type: 'button', role: 'menuitem', text,
      onclick: () => { closeMenu(); run(); },
    });
  }
}

function closeMenu() {
  if (state.menu) { state.menu.remove(); state.menu = null; }
}

/* ------------------------------------------------------- the terminal view */

function showEmpty() {
  state.current = null;
  dom.keybar.hidden = true;
  dom.composer.hidden = true;
  dom.termEmpty.hidden = false;
  dom.termEmpty.innerHTML = '';
  dom.termEmpty.append(
    el('h2', { class: 'empty-title', text: 'No session open' }),
    el('p', { class: 'empty-body', text: 'Start one and it opens here as a terminal.' }),
    el('button', { class: 'btn primary', type: 'button', text: 'New session', onclick: newSession }),
  );
  dom.sessionTitle.textContent = 'Socrates';
  dom.sessionHarness.hidden = true;
  dom.sessionMenu.hidden = true;
  dom.termSize.hidden = true;
  dom.sessionArchived.hidden = true;
  dom.termOverlay.hidden = true;
  dom.termNotice.hidden = true;
  location.hash = '';
}

// applySession draws everything about a session that is not the pane itself.
function applySession(session) {
  state.current = session;
  dom.sessionTitle.textContent = session.title;
  dom.sessionArchived.hidden = !session.archived;
  dom.sessionMenu.hidden = false;
  dom.termEmpty.hidden = true;

  dom.sessionHarness.hidden = false;
  dom.sessionHarness.innerHTML = '';
  const facts = [harnesses.label(session.harness), session.workdir];
  if (session.model) facts.push(session.model + (session.effort ? ' · ' + session.effort : ''));
  dom.sessionHarness.append(
    agentMark(session.harness, 16),
    el('span', { class: 'b-model', text: session.model || harnesses.label(session.harness) }),
    infoTip(facts, { label: 'What this session runs', bubbleClass: 'mono' }),
  );

  drawOverlay(session);
  renderList();
}

// drawOverlay is §E.7's table. Everything it can say is a state the session is
// actually in, and each one names the way out of it.
function drawOverlay(session) {
  const host = dom.termOverlay;
  const show = (...nodes) => {
    host.innerHTML = '';
    host.append(el('div', { class: 'overlay-card' }, ...nodes));
    host.hidden = false;
  };
  switch (session.state) {
    case 'exited':
      show(
        el('p', { class: 'overlay-title' }, 'The session ended.',
          infoTip(['Exit status ' + session.exit_status], { label: 'Exit status', bubbleClass: 'mono' })),
        el('div', { class: 'overlay-actions' },
          el('button', { class: 'btn primary', type: 'button', id: 'termRestart', text: 'Restart', onclick: () => restart(session) }),
          el('button', { class: 'btn', type: 'button', text: 'Delete', onclick: () => deleteSession(session) })),
      );
      break;
    case 'failed':
      show(
        el('p', { class: 'overlay-title' }, 'The session could not start.',
          session.fail_reason
            ? infoTip([session.fail_reason], { label: 'Why it failed', bubbleClass: 'mono' })
            : null),
        el('div', { class: 'overlay-actions' },
          el('button', { class: 'btn primary', type: 'button', id: 'termRestart', text: 'Try again', onclick: () => restart(session) })),
      );
      break;
    case 'resuming':
      show(el('div', { class: 'spinner' }), el('p', { class: 'overlay-title', text: 'Resuming after a restart…' }));
      break;
    case 'needs_resume':
      show(
        el('p', { class: 'overlay-title', text: 'This session is not running.' }),
        el('div', { class: 'overlay-actions' },
          el('button', { class: 'btn primary', type: 'button', text: 'Open', onclick: () => attach(session) })),
      );
      break;
    default:
      host.hidden = true;
      host.innerHTML = '';
  }
}

// notice draws the thin line at the top of the pane. It never blocks and it is
// always dismissible; `onDismiss` is what a notice does when it is put away.
function notice(kind, text, onDismiss) {
  const host = dom.termNotice;
  host.innerHTML = '';
  host.dataset.kind = kind;
  const close = el('button', {
    class: 'icon-btn notice-close', type: 'button', 'aria-label': 'Dismiss',
    html: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>',
    onclick: () => { host.hidden = true; if (onDismiss) onDismiss(); },
  });
  host.append(el('span', { class: 'notice-text', text }), close);
  host.hidden = false;
  return host;
}

async function restart(session) {
  const button = document.getElementById('termRestart');
  if (button) { button.disabled = true; button.textContent = 'Restarting…'; }
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/restart', {
      method: 'POST', attempts: 1, timeout: 60000,
    });
    replaceSession(data.session);
    if (data.error) toast(data.error, 'error');
    else attach(data.session);
  } catch (err) {
    toast(errorMessage(err), 'error');
    if (button) { button.disabled = false; button.textContent = 'Restart'; }
  }
}

function detach() {
  if (state.composer) { state.composer.dispose(); state.composer = null; }
  if (state.keybar) { state.keybar.dispose(); state.keybar = null; }
  dom.keybar.hidden = true;
  dom.composer.hidden = true;
  if (state.socket) { state.socket.stop(); state.socket = null; }
  if (state.term) { state.term.dispose(); state.term = null; }
  dom.term.innerHTML = '';
  setClass(dom.termWrap, 'stale', false);
  state.live = false;
}

/**
 * attach opens one session in the pane.
 *
 * The order matters: the terminal exists and its onData is wired before the
 * socket is opened, because the first thing tmux does on attach is ask the
 * terminal what it is, and those replies travel browser -> socket -> pane.
 */
function attach(session) {
  detach();
  applySession(session);

  const socketRef = { socket: null };
  state.term = createTerm(dom.term, {
    // Everything typed on the device's own keyboard passes the key bar on its
    // way out, because a sticky Ctrl has to reach the next ordinary key -
    // which is the only way a phone can send Ctrl-C at all.
    onData: (data) => {
      if (!socketRef.socket) return;
      socketRef.socket.sendInput(state.keybar ? state.keybar.apply(data) : data);
    },
    onBinary: (data) => {
      if (!socketRef.socket) return;
      const bytes = new Uint8Array(data.length);
      for (let i = 0; i < data.length; i += 1) bytes[i] = data.charCodeAt(i) & 0xff;
      socketRef.socket.sendInput(bytes);
    },
    onResize: (cols, rows) => {
      showSize(cols, rows);
      if (socketRef.socket) socketRef.socket.resize(cols, rows);
    },
    fitTo: dom.termWrap,
    scrollback: state.terminal.scrollback,
    fontSize: state.terminal.font_size,
    webgl: state.terminal.webgl !== false,
  });
  state.term.refit();

  const socket = new TermSocket({
    sessionId: session.id,
    viewer: viewerId(),
    onOutput: (bytes) => state.term && state.term.write(bytes),
    onControl: (frame) => onControl(session.id, frame),
    onStatus: (status) => onStatus(status),
  });
  socketRef.socket = socket;
  state.socket = socket;
  state.keybar = mountKeyBar(dom.keybar, state.term.term, socket);
  state.composer = mountComposer({
    form: dom.composer,
    input: dom.lineInput,
    mic: dom.micBtn,
    recTime: dom.recTime,
    sessionId: session.id,
    socket,
    term: state.term.term,
  });
  dom.composer.hidden = false;
  dom.micBtn.hidden = false;
  showKeyBar(keyBarWanted());

  const size = state.term.size();
  socket.start(size.cols, size.rows);
  state.term.focus();
}

// The key bar is what a phone is missing rather than a preference, so it comes
// up on its own where the keys are not there - and the session menu can say
// otherwise on any device.
function showKeyBar(on) {
  dom.keybar.hidden = !on;
  if (state.term) state.term.refit();
}

function showSize(cols, rows) {
  dom.termSize.hidden = false;
  dom.termSize.textContent = cols + '×' + rows;
}

function onControl(sessionId, frame) {
  if (!state.current || state.current.id !== sessionId) return;
  switch (frame.t) {
    case 'hello':
      if (frame.session) applySession({ ...state.current, ...frame.session });
      if (frame.size) showSize(frame.size.cols, frame.size.rows);
      if (!Number(frame.replay_from) && state.term) {
        state.term.reset();
        // Only worth saying when there was something to lose: a first attach
        // also replays from zero and is not a desync.
        if (frame.viewer_fresh === false) notice('desync', 'Reconnected — the screen was redrawn.');
      }
      break;
    case 'state':
      replaceSession({ ...state.current, state: frame.state });
      break;
    case 'exit':
      replaceSession({ ...state.current, state: 'exited', exit_status: frame.status });
      break;
    case 'size':
      if (frame.by === 'other') {
        showSize(frame.cols, frame.rows);
        const line = notice('resized', 'Another viewer resized this session to ' + frame.cols + '×' + frame.rows + '.');
        setTimeout(() => { if (line.dataset.kind === 'resized') line.hidden = true; }, 4000);
        if (state.term) state.term.refit();
      }
      break;
    case 'notice':
      if (frame.kind === 'resumed') {
        const text = 'Resumed after a restart.'
          + (frame.fresh ? ' The previous conversation could not be resumed, so this one starts fresh.' : '');
        notice('resumed', text, () => {
          api('/api/sessions/' + encodeURIComponent(sessionId) + '/ack-resume', { method: 'POST' })
            .then((data) => replaceSession(data.session))
            .catch(() => { /* the flag stays up and the banner comes back */ });
        });
      }
      break;
    case 'input_lost':
      if (frame.keystrokes) {
        toast(frame.keystrokes + ' keystroke' + (frame.keystrokes === 1 ? '' : 's')
          + ' may not have been delivered.', 'error');
      }
      if (frame.lines && frame.lines.length) {
        if (state.composer) state.composer.restore(frame.lines);
        toast('That line was not delivered — it is back in the field.', 'error');
      }
      break;
    case 'error':
      toast(frame.message, 'error');
      break;
    default:
      break;
  }
}

// The pane is not made read-only when the connection goes: what is typed is
// queued and delivered on reconnect. But it must be visible that what is on
// screen is old.
function onStatus(status) {
  if (status === 'live') {
    state.live = true;
    state.lostAt = 0;
  } else if (status !== 'idle' && state.live) {
    state.live = false;
    state.lostAt = Date.now();
  }
  updateStale();
}

function updateStale() {
  const stale = !state.live
    && (navigator.onLine === false || Date.now() - (state.lostAt || Date.now()) >= CONNECTION_GRACE);
  setClass(document.body, 'stale', stale);
  setClass(dom.termWrap, 'stale', stale);
}

/* --------------------------------------------------------------- routing */

// A run of taps down the list is one decision, not six. Opening a socket per
// tap leaves the last one queued behind five handshakes it no longer wants, so
// the attach waits for the tapping to stop; the row highlights immediately,
// which is what the tap was asking for.
function selectSession(id) {
  const session = sessionOf(id);
  if (!session) return;
  if (state.current && state.current.id === id && state.socket) return;
  location.hash = '#' + id;
  closeNav();
  if (state.pendingAttach) clearTimeout(state.pendingAttach);
  state.pendingAttach = setTimeout(() => {
    state.pendingAttach = null;
    const fresh = sessionOf(id);
    if (fresh) attach(fresh);
  }, ATTACH_DEBOUNCE);
}

async function newSession() {
  const pick = await harnesses.openNewSessionSheet();
  if (!pick) return;
  // On a phone the drawer is what "New session" was tapped in, and the thing
  // being started is behind it. Leaving it open puts a list over the terminal
  // that was just asked for.
  closeNav();
  const size = state.term ? state.term.size() : { cols: 0, rows: 0 };
  try {
    const data = await api('/api/sessions', {
      method: 'POST',
      attempts: 2,
      timeout: 60000,
      body: {
        client_id: clientKey(),
        harness: pick.harness,
        model: pick.model,
        effort: pick.effort,
        workdir_mode: pick.workdir_mode,
        workdir: pick.workdir,
        cols: size.cols || undefined,
        rows: size.rows || undefined,
      },
    });
    replaceSession(data.session);
    if (data.error) toast(data.error, 'error');
    location.hash = '#' + data.session.id;
    attach(data.session);
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

function openNav() { document.body.classList.add('nav-open'); }
function closeNav() { document.body.classList.remove('nav-open'); }

/* ----------------------------------------------------------------- start */

function wire() {
  dom.newSession.addEventListener('click', newSession);
  dom.menuBtn.addEventListener('click', () => {
    if (document.body.classList.contains('nav-open')) closeNav(); else openNav();
  });
  dom.navScrim.addEventListener('click', closeNav);
  dom.sessionScope.addEventListener('click', (event) => {
    const button = event.target.closest('.seg');
    if (!button) return;
    state.scope = button.dataset.scope;
    for (const seg of dom.sessionScope.querySelectorAll('.seg')) {
      const on = seg === button;
      seg.classList.toggle('on', on);
      seg.setAttribute('aria-pressed', on ? 'true' : 'false');
    }
    refreshList();
  });
  dom.sessionMenu.addEventListener('click', (event) => {
    if (state.current) openRowMenu(event.currentTarget, state.current.id);
  });
  // The name is edited where it is read.
  dom.sessionTitle.addEventListener('click', () => {
    if (state.current) renameSession(state.current);
  });
  dom.logout.addEventListener('click', async () => {
    try { await api('/api/logout', { method: 'POST' }); } catch { /* going anyway */ }
    location.href = '/login';
  });
  window.addEventListener('hashchange', () => {
    const id = location.hash.replace(/^#/, '');
    if (id && (!state.current || state.current.id !== id)) selectSession(id);
  });
  window.addEventListener('online', updateStale);
  window.addEventListener('offline', updateStale);
  setInterval(updateStale, 1000);
  // The list is state, not a stream: it is re-read when the page wakes and
  // slowly while it is being looked at, so a session another tab started or
  // deleted does not sit there being wrong.
  onWake(() => refreshList());
  setInterval(() => {
    if (document.visibilityState === 'visible') refreshList();
  }, 15000);
}

async function boot() {
  wire();
  followViewport();
  state.loading = true;
  renderList();
  harnesses.load().catch(() => { /* the sheet says so when it is opened */ });
  try {
    const prefs = await api('/api/preferences', { attempts: 1, timeout: 8000 });
    if (prefs && prefs.terminal) state.terminal = { ...state.terminal, ...prefs.terminal };
  } catch { /* the defaults above are the shipped ones */ }
  await refreshList();
  const wanted = location.hash.replace(/^#/, '');
  const first = (wanted && sessionOf(wanted))
    || state.sessions.find((s) => !s.archived)
    || state.sessions[0];
  if (first) attach(first); else showEmpty();
}

boot();
