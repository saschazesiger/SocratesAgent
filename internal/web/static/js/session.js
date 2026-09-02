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
  api, el, toast, infoTip, confirmDialog, errorMessage, isOffline, isBusyConflict,
  setClass, onWake, clientKey, CONNECTION_GRACE,
} from './api.js';
import { connectionSource } from './net.js';
import { agentMark } from './logos.js';
import * as harnesses from './harnesses.js';
import { createTerm, measurePane } from './term.js';
import { mountAssist, audioWanted } from './assist.js';
import {
  mountKeyBar, mountComposer, keyBarWanted, setKeyBarWanted, onKeyBarWanted, followViewport,
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

// How long a socket may stay open without being told `hello`, and how long a
// burst of wake events is gathered into one attempt. A phone raises `online`,
// `visibilitychange` and `focus` in the same tick.
const HELLO_TIMEOUT = 10000;
const WAKE_COALESCE = 60;

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
    // anchored says whether this socket has heard its `hello` yet. Until it
    // has, the counter is whatever the last connection left behind and the
    // server's is something else entirely, so input is held rather than sent:
    // a frame numbered below the server's `lastInputSeq` is discarded there as
    // a resend, and the keystroke is gone without a word. hello renumbers what
    // is held from `input_ack + 1` and releases it, which is the same rule
    // §D.6 states for a reconnect - it simply has to hold for the first
    // connection too.
    this.anchored = false;

    this.pingTimer = null;
    this.pingDeadline = null;
    // helloDeadline is the guard against a socket that opens and is never
    // spoken to: the ping watchdog only starts once `hello` has arrived, so
    // without this a silent server parks the page on "Reconnecting…" for ever.
    this.helloDeadline = null;
    this.wakeTimer = null;
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
      //
      // A phone regaining signal raises `online`, `visibilitychange` and
      // `focus` within a few milliseconds of each other, so the storm is
      // coalesced into one attempt, and a handshake that is already in flight
      // is left alone: tearing down a CONNECTING socket abandons a viewer
      // slot the server has already begun to fill, and the second handshake
      // is then refused while the first is cleaned up.
      this.unwake = onWake(() => this.resume());
    }
    this.open();
  }

  /**
   * resume is a wake: the network came back, or the screen was looked at.
   *
   * It is deliberately conservative. A socket that is open, or one that is
   * still shaking hands, is already the best answer to "try again"; only a
   * socket that is waiting out a backoff is worth interrupting, and a burst of
   * wake events is one interruption.
   */
  resume() {
    if (this.stopped || this.connecting() || this.isOpen()) return;
    if (this.wakeTimer) return;
    this.wakeTimer = setTimeout(() => {
      this.wakeTimer = null;
      if (this.stopped || this.connecting() || this.isOpen()) return;
      this.attempt = 0;
      this.reconnect(0);
    }, WAKE_COALESCE);
  }

  connecting() { return !!this.ws && this.ws.readyState === WebSocket.CONNECTING; }

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
    if (this.wakeTimer) { clearTimeout(this.wakeTimer); this.wakeTimer = null; }
    this.stopTimers();
    this.anchored = false;
    if (this.ws) {
      const ws = this.ws;
      this.ws = null;
      ws.onopen = null; ws.onmessage = null; ws.onerror = null; ws.onclose = null;
      try { ws.close(); } catch { /* already gone */ }
    }
  }

  stopTimers() {
    if (this.helloDeadline) { clearTimeout(this.helloDeadline); this.helloDeadline = null; }
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
    ws.onopen = () => {
      this.attempt = 0;
      // An open socket is not a working one. Nothing on this connection means
      // anything until `hello` arrives, so it is given a deadline of its own.
      if (this.helloDeadline) clearTimeout(this.helloDeadline);
      this.helloDeadline = setTimeout(() => {
        this.helloDeadline = null;
        this.reconnect();
      }, HELLO_TIMEOUT);
    };
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
    if (this.helloDeadline) { clearTimeout(this.helloDeadline); this.helloDeadline = null; }
    const ack = Number(frame.input_ack) || 0;
    const fresh = !!frame.viewer_fresh;
    // A replay from zero means the server could not fill the gap and attached
    // afresh: everything on screen is about to be repainted.
    if (!Number(frame.replay_from)) this.rendered = 0;

    // Anchor: what the server says it has written is the truth, and this
    // client's own counter never survives a connect.
    //
    // Only what has been on a wire can have been written. A frame that was
    // typed while this socket was still shaking hands carries a number from a
    // counter the server has never seen - after a reload it starts at zero
    // again - and dropping it because that number is below the server's ack
    // would throw away, in silence, every keystroke made in the round trip
    // that `hello` took to arrive.
    this.held = this.held.filter((f) => !f.sent || f.seq > ack);
    this.inputSeq = ack;
    // From here the counter means what the server means by it, so input may
    // go out again.
    this.anchored = true;

    if (fresh) {
      // The server has no memory of this viewer, so it cannot tell a resend
      // from new input - but only for frames that have actually been on a
      // wire. What was typed while this socket was still shaking hands has
      // never left the tab, and a first attach is always `fresh`: those are
      // new input, not resends, and discarding them would throw away every
      // keystroke made between the socket opening and hello arriving.
      const lost = this.held.filter((f) => f.sent);
      this.held = this.held.filter((f) => !f.sent);
      if (lost.length) {
        // Nothing is duplicated, and the person is told: loose keystrokes are
        // counted, and a composed line goes back into the field it was
        // written in, unsent.
        const lines = [];
        let keystrokes = 0;
        for (const held of lost) {
          if (held.text !== undefined) lines.push(held.text);
          // The replies to the questions tmux asks on every attach travel the
          // same path as typed bytes. Counting them would send somebody
          // looking for six characters they never typed.
          else if (held.bytes[0] !== 0x1b) keystrokes += 1;
          if (held.onLost) held.onLost();
        }
        if (keystrokes || lines.length) this.onControl({ t: 'input_lost', keystrokes, lines });
      }
    }
    // Renumbering is safe because the content is what matters: the numbers
    // exist only for dedupe, and hello carries the server's current
    // lastInputSeq rather than a stale batched ack.
    for (const held of this.held) {
      this.inputSeq += 1;
      held.seq = this.inputSeq;
      held.frame = inputFrame(held.seq, held.bytes);
      held.sent = this.write(held.frame) || held.sent;
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
    // An ack that leaves a hole in front of what is still held is the server
    // refusing a gap (§D.6): it wrote nothing and it is saying where to start
    // again. Renumbering from there and resending is the re-anchor it asks
    // for; without it those frames would sit here unsent until the next
    // connect, which is a keystroke lost while the socket is up.
    if (kept.length && kept[0].seq > seq + 1) {
      this.inputSeq = seq;
      for (const held of kept) {
        this.inputSeq += 1;
        held.seq = this.inputSeq;
        held.frame = inputFrame(held.seq, held.bytes);
        held.sent = this.write(held.frame) || held.sent;
      }
    }
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
      // sent records whether these bytes have ever been on a wire, which is
      // what decides their fate when the server turns out not to remember
      // this viewer: only what was sent can be a resend.
      sent: false,
    };
    this.held.push(entry);
    // Only a socket that has been anchored by its hello may send: before that
    // the number on the frame means nothing to the server, and a number below
    // its own is a keystroke silently thrown away.
    if (this.anchored) entry.sent = this.write(entry.frame);
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

  /**
   * read clears the unread mark of a session - not necessarily this one.
   *
   * The id is explicit because the mark is a fact about a row in the sidebar,
   * and the row that is being opened is by definition not the one this socket
   * is attached to yet. Any open socket carries it.
   */
  read(id) {
    return this.write(JSON.stringify({ t: 'read', id }));
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
  // Whether the last attempt to read the list reached Socrates at all. A page
  // that cannot ask must not answer "no sessions": that is a fact it does not
  // have, and showing it as one is the failure this whole design is against.
  reachable: true,
  // The session the URL asks for. It outlives a failed list: an offline
  // reload still knows which session this tab was looking at, and reopens it
  // the moment the list can be read.
  wanted: '',
  // The overflow menu a row or the header last opened, so a second one
  // replaces it rather than stacking on it.
  menu: null,
  // The session whose relaunch this tab has asked for and not yet been
  // answered about. It is what keeps the pressed Restart pressed across a list
  // refresh, and what stops a second press starting a second relaunch.
  restarting: '',
  // Whether this tab is waiting for a session it opened to be relaunched.
  // §C.8's resume happens inside the handshake and the row says needs_resume
  // for the whole of it, so the pane's own overlay is the only place that
  // knows.
  resuming: false,
  // What every session is doing, keyed by id: the busy ring, the waiting
  // ring and the unread mark are all drawn from this and from nothing else.
  // It is in memory only - the server re-derives it from a live pane every
  // second, so a copy that outlived a reload could only ever be wrong.
  activity: new Map(),
  // Status, Agent and audio mode, mounted once in boot().
  assist: null,
  // How the terminal is drawn, from the dashboard. The defaults here are what
  // a page that could not ask uses, and they are the same numbers the settings
  // document ships with.
  terminal: { scrollback: 2000, font_size: 14, webgl: true },
};

const dom = {};
const ids = ['sidebar', 'navScrim', 'menuBtn', 'newSession', 'sessionScope', 'sessionList',
  'activityLive', 'sessionHarness', 'sessionTitle', 'sessionArchived', 'termSize',
  'statusBtn', 'agentBtn', 'audioModeBtn', 'sessionMenu',
  'termWrap', 'term', 'termOverlay', 'termNotice', 'termEmpty',
  'audioBar', 'audioStatus', 'audioAgent', 'keybar', 'composer',
  'lineInput', 'micBtn', 'recTime', 'logout'];
for (const id of ids) dom[id] = document.getElementById(id);

const STATE_WORDS = {
  running: 'Running', starting: 'Starting', resuming: 'Resuming',
  needs_resume: 'Not running', exited: 'Ended', failed: 'Failed',
};

// What a session is doing, in the words the "i" uses. `unknown` has no word:
// a row with no evidence yet says nothing rather than guessing.
const ACTIVITY_WORDS = { busy: 'Working', idle: 'Waiting for an instruction', waiting: 'Needs an answer' };

function sessionOf(id) { return state.sessions.find((s) => s.id === id) || null; }

/* -------------------------------------------------------- the session list */

function renderList() {
  const host = dom.sessionList;
  const wanted = state.sessions.filter((s) => state.scope === 'all' || !s.archived);
  if (!wanted.length) {
    host.innerHTML = '';
    let empty = 'No sessions yet.';
    if (state.loading) empty = 'Loading…';
    else if (!state.reachable) empty = 'Can\u2019t reach Socrates.';
    host.append(el('div', { class: 'list-empty', text: empty }));
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

// The technical detail of a session - what it runs, where it works, what it
// runs on, which conversation it is holding, how often it has been brought
// back. It is never in the words of a row: the "Info" item of the row's menu
// is the one place it is shown, for the list and for the header alike.
function rowFacts(session) {
  const facts = [
    harnesses.label(session.harness),
    STATE_WORDS[session.state] || session.state,
    session.workdir,
  ];
  const act = state.activity.get(session.id);
  if (act && ACTIVITY_WORDS[act.state]) {
    // What it is doing, why the detector thinks so, and since when. All three
    // are facts about the machine, so all three live behind the "i".
    facts.push(ACTIVITY_WORDS[act.state]
      + (act.note ? ' · ' + act.note : '')
      + (act.source ? ' · ' + act.source : '')
      + (act.since ? ' · since ' + clockOf(act.since) : ''));
  }
  if (session.model) facts.push(session.model + (session.effort ? ' · ' + session.effort : ''));
  if (session.cli_session_state && session.cli_session_state !== 'none') {
    facts.push('conversation ' + session.cli_session_state);
  }
  if (session.resume_count) facts.push('resumed ' + session.resume_count + '×');
  return facts.filter(Boolean);
}

// clockOf is when a state was committed, as a time of day.
//
// A duration would read better and is what a person would say, but it would be
// a different string every second. The moment it happened is the same fact,
// and it holds still.
function clockOf(at) {
  const when = new Date(Number(at));
  if (Number.isNaN(when.getTime())) return '';
  return String(when.getHours()).padStart(2, '0') + ':'
    + String(when.getMinutes()).padStart(2, '0') + ':'
    + String(when.getSeconds()).padStart(2, '0');
}

function updateRow(row, session) {
  row.querySelector('.label').textContent = session.title;
  const attached = !!(state.current && state.current.id === session.id);
  setClass(row, 'active', attached);
  setClass(row, 'archived', !!session.archived);

  // The activity of a row is two marks and no words: the agent's own mark
  // turns into a ring that spins while the harness is working and stands
  // still, in amber, while it is waiting for an answer, and a name that has
  // finished something nobody has seen is bold. The session being looked at
  // is never bold - it is not unread, it is open.
  const act = state.activity.get(session.id) || null;
  const mark = row.querySelector('.row-mark');
  const live = session.state === 'running' || session.state === 'starting' || session.state === 'resuming';
  const busy = live && !!act && act.state === 'busy';
  const waiting = live && !!act && act.state === 'waiting';
  setClass(mark, 'busy', busy);
  setClass(mark, 'waiting', waiting);
  let word = STATE_WORDS[session.state] || session.state;
  if (busy) word = ACTIVITY_WORDS.busy;
  if (waiting) word = ACTIVITY_WORDS.waiting;
  mark.title = word;
  setClass(row, 'unread', !!act && !!act.unread && !attached);
}

/**
 * mergeActivity takes a map of committed changes and draws them.
 *
 * It is the one door: the poll, the handshake and the broadcast frame all
 * come through here, so the sidebar, the announcement and audio mode's own
 * rule cannot disagree about what changed.
 */
function mergeActivity(sessions) {
  if (!sessions) return;
  let changed = false;
  for (const [id, next] of Object.entries(sessions)) {
    if (!next || typeof next !== 'object') continue;
    const prev = state.activity.get(id) || null;
    state.activity.set(id, next);
    if (prev && prev.state === next.state && prev.unread === next.unread && prev.note === next.note) continue;
    changed = true;
    announce(id, next, prev);
    if (state.assist) state.assist.activity(id, next, prev);
  }
  if (changed) renderList();
}

// announce is the sidebar said out loud, for a reader who cannot see it. Only
// the two changes that are worth interrupting somebody for are said, and at
// most one every two seconds - a machine with six sessions on it would
// otherwise talk continuously.
const ANNOUNCE_EVERY = 2000;
let announcedAt = 0;
let announceTimer = null;
let announcePending = '';

function announce(id, next, prev) {
  const session = sessionOf(id);
  if (!session || !dom.activityLive) return;
  const who = harnesses.label(session.harness) || session.title;
  let line = '';
  if (prev && prev.state === 'busy' && next.state !== 'busy' && next.state !== 'waiting') line = who + ' finished';
  if (next.state === 'waiting' && (!prev || prev.state !== 'waiting')) line = who + ' needs an answer';
  if (!line) return;
  announcePending = line;
  if (announceTimer) return;
  const wait = Math.max(0, ANNOUNCE_EVERY - (Date.now() - announcedAt));
  announceTimer = setTimeout(() => {
    announceTimer = null;
    announcedAt = Date.now();
    dom.activityLive.textContent = announcePending;
    announcePending = '';
  }, wait);
}

async function refreshList() {
  try {
    const data = await api('/api/sessions?scope=' + (state.scope === 'all' ? 'all' : 'active'),
      { attempts: 2, timeout: 12000 });
    state.sessions = data.sessions || [];
    // The list is the catch-up path for a sidebar nobody has a socket open
    // for: every view carries the activity of its own session.
    const seen = {};
    for (const row of state.sessions) if (row && row.activity) seen[row.id] = row.activity;
    mergeActivity(seen);
    state.loading = false;
    state.reachable = true;
    if (state.current) {
      const fresh = sessionOf(state.current.id);
      if (fresh) applySession(fresh);
    }
    renderList();
    // The tab was looking at a session before it lost the network, and the
    // list is the first thing that can prove the session is still there. This
    // is what makes an offline reload come back to the pane it was on -
    // together with the line that was typed into it and never sent.
    openWanted();
    return state.sessions;
  } catch (err) {
    state.loading = false;
    state.reachable = false;
    renderList();
    if (!isOffline(err)) toast(errorMessage(err), 'error');
    return state.sessions;
  }
}

/**
 * openWanted attaches the session this tab is meant to be on, if the list now
 * has it and nothing is attached.
 *
 * This is the only place that attaches without being asked, and the guard is
 * why: "New session" is clickable from the first line of boot, while the list
 * is still being fetched, and a session started in that window has already
 * been attached. Attaching it a second time would tear its terminal and its
 * socket down under the person's fingers - the pane reset by the new hello,
 * and the keystrokes in between reaching no socket at all.
 */
function openWanted() {
  if (state.current || state.socket || state.pendingAttach) return;
  const session = state.wanted && sessionOf(state.wanted);
  if (session) attach(session);
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

// infoDialog is the "Info" item of the row menu: everything the machine knows
// about one session, in one place and in one shape. It is built the way
// confirmDialog and promptTitle are, and it is the only place these facts are
// drawn - a row is a mark and a name, and nothing else.
function infoDialog(session) {
  const dialog = el('dialog', { class: 'modal' });
  const close = el('button', { class: 'btn sm primary', type: 'button', text: 'Close' });
  dialog.append(
    el('h2', { class: 'modal-title', text: session.title }),
    el('div', { class: 'facts mono' },
      ...rowFacts(session).map((line) => el('div', { class: 'fact', text: line }))),
    el('div', { class: 'modal-actions' }, close),
  );
  close.addEventListener('click', () => dialog.close());
  dialog.addEventListener('click', (event) => { if (event.target === dialog) dialog.close(); });
  dialog.addEventListener('close', () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  close.focus();
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
  } catch (err) {
    toast(actionFailed(err, 'That session could not be deleted.'), 'error');
  }
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
    item('Info', () => infoDialog(session)),
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
  // Unreachable is not empty. A page that could not read the list says so and
  // keeps the session it was asked for, because the signal is what is
  // missing - not the session.
  const unreachable = !state.reachable;
  dom.termEmpty.append(
    el('h2', { class: 'empty-title', text: unreachable ? 'No connection' : 'No session open' }),
    el('p', {
      class: 'empty-body',
      text: unreachable
        ? 'This session opens again as soon as there is signal.'
        : 'Start one and it opens here as a terminal.',
    }),
    unreachable ? null : el('button', { class: 'btn primary', type: 'button', text: 'New session', onclick: newSession }),
  );
  dom.sessionTitle.textContent = 'Socrates';
  dom.sessionHarness.hidden = true;
  dom.sessionMenu.hidden = true;
  if (state.assist) state.assist.attached();
  dom.termSize.hidden = true;
  dom.sessionArchived.hidden = true;
  dom.termOverlay.hidden = true;
  dom.termNotice.hidden = true;
  if (state.reachable) { state.wanted = ''; location.hash = ''; }
}

// applySession draws everything about a session that is not the pane itself.
function applySession(session) {
  state.current = session;
  dom.sessionTitle.textContent = session.title;
  dom.sessionArchived.hidden = !session.archived;
  dom.sessionMenu.hidden = false;
  dom.termEmpty.hidden = true;

  // The mark and the name of what it runs. Everything else the machine knows
  // about this session is one place only: "Info" in the session menu.
  dom.sessionHarness.hidden = false;
  dom.sessionHarness.innerHTML = '';
  dom.sessionHarness.append(
    agentMark(session.harness, 16),
    el('span', { class: 'b-model', text: session.model || harnesses.label(session.harness) }),
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
  // `resuming` is the one state the store does not have: it is the window
  // between this tab asking for a session that is not running and the server
  // answering with what it became. The row still says needs_resume for all of
  // it, and a list refresh in the middle must not redraw the button that has
  // already been pressed.
  let drawn = session.state;
  if (state.resuming) {
    if (drawn === 'needs_resume' || drawn === 'starting') drawn = 'resuming';
    else state.resuming = false;
  }
  switch (drawn) {
    case 'exited':
      show(
        el('p', { class: 'overlay-title', text: 'The session ended.' }),
        el('p', { class: 'overlay-detail mono', text: 'Exit status ' + session.exit_status }),
        el('div', { class: 'overlay-actions' },
          actionButton(session, 'Restart', 'Restarting…'),
          el('button', { class: 'btn', type: 'button', text: 'Delete', onclick: () => deleteSession(session) })),
      );
      break;
    case 'failed':
      show(
        el('p', { class: 'overlay-title', text: 'The session could not start.' }),
        session.fail_reason
          ? el('p', { class: 'overlay-detail mono', text: session.fail_reason })
          : null,
        el('div', { class: 'overlay-actions' },
          actionButton(session, 'Try again', 'Trying again…')),
      );
      break;
    case 'resuming':
      show(el('div', { class: 'spinner' }), el('p', { class: 'overlay-title', text: 'Resuming after a restart…' }));
      break;
    case 'needs_resume':
      show(
        el('p', { class: 'overlay-title', text: 'This session is not running.' }),
        el('div', { class: 'overlay-actions' },
          el('button', {
            class: 'btn primary', type: 'button', text: 'Open',
            onclick: () => { if (!state.resuming) attach(session); },
          })),
      );
      break;
    default:
      host.hidden = true;
      host.innerHTML = '';
  }
}

// actionButton is the overlay's own button, drawn from `state.restarting` so
// that a redraw in the middle of a relaunch keeps saying what is happening
// instead of offering to start it again.
function actionButton(session, label, busyLabel) {
  const busy = state.restarting === session.id;
  return el('button', {
    class: 'btn primary', type: 'button', id: 'termRestart',
    text: busy ? busyLabel : label,
    disabled: busy,
    onclick: () => restart(session),
  });
}

// notice draws the thin line at the top of the pane. It never blocks and it is
// always dismissible; `onDismiss` is what a notice does when it is put away.
//
// `facts` is the technical half of what the notice knows - the conversation a
// resume came from, and anything else that is an identifier rather than a
// sentence. §E.10 rule 3: it goes behind the "i" and never into the line
// itself.
function notice(kind, text, onDismiss, facts, extra) {
  const host = dom.termNotice;
  host.innerHTML = '';
  host.dataset.kind = kind;
  const close = el('button', {
    class: 'icon-btn notice-close', type: 'button', 'aria-label': 'Dismiss',
    html: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>',
    onclick: () => { host.hidden = true; if (onDismiss) onDismiss(); },
  });
  // `host` is a plain element, and `ParentNode.append` writes a null argument
  // into the line as the word "null" - only `el()` filters. So the tip is
  // appended when there is one, and not otherwise.
  host.append(el('span', { class: 'notice-text', text }));
  if (facts && facts.length) host.append(infoTip(facts, { label: 'Details', bubbleClass: 'mono' }));
  // `extra` is the one control a notice may carry beside its "i" - the Cancel
  // of a run in progress. Everything else a notice can say, it says in words.
  if (extra) host.append(extra);
  host.append(close);
  host.hidden = false;
  return host;
}

/**
 * restart is the exit and failure overlays' button.
 *
 * The pressed state lives in `state.restarting` rather than on the button,
 * because the button does not survive: the list refreshes on a wake and every
 * fifteen seconds, and the row still says `exited` while the POST is in
 * flight, so the card - and a fresh, enabled Restart with it - is redrawn
 * under the finger that has just pressed it. A second press starts a second
 * relaunch, and the loser of that race is a 409. So the flag is what
 * `drawOverlay` reads, and it is also the guard: one relaunch per session at a
 * time.
 */
async function restart(session) {
  if (state.restarting === session.id) return;
  state.restarting = session.id;
  drawOverlay(session);
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/restart', {
      method: 'POST', attempts: 1, timeout: 60000,
    });
    state.restarting = '';
    replaceSession(data.session);
    // The reason is on the overlay, behind its "i". A toast that repeated it
    // would put a path or a tmux name in visible text (§E.10 rule 3).
    if (data.error) toast('The session could not start.', 'error');
    else attach(data.session);
  } catch (err) {
    state.restarting = '';
    toast(actionFailed(err, 'The session could not be started again.'), 'error');
    drawOverlay(sessionOf(session.id) || session);
  }
}

// actionFailed is what a refused action says out loud. A lost connection says
// so in its own words, because that is something the person can act on;
// anything the server said is a sentence with a path or a tmux session name in
// it, and those belong behind an "i" or nowhere.
function actionFailed(err, sentence) {
  if (isOffline(err)) return errorMessage(err);
  if (isBusyConflict(err)) return 'That session is already running.';
  return sentence;
}

function detach() {
  if (state.assist) state.assist.attached();
  if (state.composer) { state.composer.dispose(); state.composer = null; }
  if (state.keybar) { state.keybar.dispose(); state.keybar = null; }
  dom.keybar.hidden = true;
  dom.composer.hidden = true;
  if (state.socket) { state.socket.stop(); state.socket = null; }
  if (state.term) { state.term.dispose(); state.term = null; }
  dom.term.innerHTML = '';
  setClass(dom.termWrap, 'stale', false);
  state.live = false;
  state.resuming = false;
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

  // Opening a session that is not running is what relaunches it (§C.8), and
  // the relaunch happens inside the handshake: the program has to be started,
  // its conversation verified and its pane made before the socket is answered.
  // "This session is not running" would be the last thing said for those
  // seconds, and it would read as nothing having happened - so from the moment
  // it is opened until the server says what it became, the session is
  // resuming.
  if (session.state === 'needs_resume' || session.state === 'starting') {
    state.resuming = true;
    drawOverlay(session);
  }

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
  // The three buttons and, when this device is in audio mode, the bar under
  // the pane. It is done before the size is read, because the bar takes rows
  // off the terminal and the size the session is told is the size it gets.
  if (state.assist) state.assist.attached();

  const size = state.term.size();
  socket.start(size.cols, size.rows);
  state.term.focus();
}

// The key bar stands beside a real keyboard rather than instead of one, so it
// comes up on its own where there is one and stays away on a touch screen -
// and the session menu can say otherwise on any device.
function showKeyBar(on) {
  dom.keybar.hidden = !on;
  // A modifier armed on a bar nobody can see would transform the next key
  // typed with nothing on screen to say why.
  if (!on && state.keybar) state.keybar.clear();
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
      // A handshake carries every running session's activity and this
      // session's run, so a reconnect and a reload both re-draw the sidebar
      // and the progress line without asking for anything.
      mergeActivity(frame.activity);
      if (state.assist) state.assist.helloAgent(frame.agent);
      if (!Number(frame.replay_from) && state.term) {
        state.term.reset();
        // Only worth saying when there was something to lose: a first attach
        // also replays from zero and is not a desync.
        if (frame.viewer_fresh === false) notice('desync', 'Reconnected — the screen was redrawn.');
      }
      break;
    case 'activity':
      mergeActivity(frame.sessions);
      break;
    case 'agent':
      if (state.assist) state.assist.agentFrame(frame);
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
        }, frame.resumed_from ? ['conversation ' + frame.resumed_from] : null);
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
  if (state.assist) state.assist.live();
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
  markRead(id);
  state.wanted = id;
  location.hash = '#' + id;
  closeNav();
  if (state.pendingAttach) clearTimeout(state.pendingAttach);
  state.pendingAttach = setTimeout(() => {
    state.pendingAttach = null;
    const fresh = sessionOf(id);
    if (fresh) attach(fresh);
  }, ATTACH_DEBOUNCE);
}

/**
 * markRead clears the unread mark of the session that is being opened.
 *
 * Opening a row is seeing it, which is the whole of what the mark means. The
 * socket carries it when there is one, because a control frame is free and
 * already open; a page with no socket at all - the moment before the first
 * attach - asks over HTTP instead. Either way the server broadcasts the
 * change, so every other tab stops bolding it too.
 */
function markRead(id) {
  const act = state.activity.get(id);
  if (!act || !act.unread) return;
  state.activity.set(id, { ...act, unread: false });
  renderList();
  if (state.socket && state.socket.read(id)) return;
  api('/api/sessions/' + encodeURIComponent(id) + '/read', { method: 'POST', attempts: 1 })
    .catch(() => { /* the next frame or poll says what the server thinks */ });
}

// measureNewPane is what the pane is about to be, measured before it exists.
//
// The size asked for at create is the size tmux gives the window, and the
// first viewer's attach corrects it - so a size nobody measured means every
// new session starts with a resize, and a tmux window that shrinks reflows:
// on 3.6 the first wrapped line of the program's banner goes into the
// scrollback before it has been read. The chrome is put into the state the
// attach will leave it in - the composer up, the key bar where this device
// wants one - so that what is measured is the pane the session will get.
function measureNewPane() {
  const composerWas = dom.composer.hidden;
  const keybarWas = dom.keybar.hidden;
  const audioWas = dom.audioBar.hidden;
  dom.audioBar.hidden = !audioWanted();
  // The real bar, because its height is its buttons. Nothing is wired to it:
  // it exists for the length of one measurement.
  const bar = mountKeyBar(dom.keybar, null, null);
  dom.composer.hidden = false;
  dom.keybar.hidden = !keyBarWanted();
  const size = measurePane(dom.term, { fontSize: state.terminal.font_size });
  bar.dispose();
  dom.composer.hidden = composerWas;
  dom.keybar.hidden = keybarWas;
  dom.audioBar.hidden = audioWas;
  return size;
}

async function newSession() {
  const pick = await harnesses.openNewSessionSheet();
  if (!pick) return;
  // On a phone the drawer is what "New session" was tapped in, and the thing
  // being started is behind it. Leaving it open puts a list over the terminal
  // that was just asked for.
  closeNav();
  const size = state.term ? state.term.size() : measureNewPane();
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
    if (data.error) toast('The session could not start.', 'error');
    state.wanted = data.session.id;
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
    if (id) state.wanted = id;
    if (id && (!state.current || state.current.id !== id)) selectSession(id);
  });
  window.addEventListener('online', updateStale);
  window.addEventListener('offline', updateStale);
  setInterval(updateStale, 1000);
  // The list is state, not a stream: it is re-read when the page wakes and
  // slowly while it is being looked at, so a session another tab started or
  // deleted does not sit there being wrong.
  onWake(() => refreshList());
  // A keyboard can arrive after the page did - a tablet put in its case, a
  // window dragged onto a desk - and the bar follows it. Nothing is done to a
  // session that is not open: the bar is drawn on attach from the same answer.
  onKeyBarWanted((on) => { if (state.current) showKeyBar(on); });
  setInterval(() => {
    if (document.visibilityState === 'visible') refreshList();
  }, 15000);
}

async function boot() {
  wire();
  // Status, Agent and audio mode. Mounted before anything is measured,
  // because whether this device is in audio mode decides how tall the pane
  // is, and a layout applied afterwards resizes a terminal somebody is
  // already looking at.
  state.assist = mountAssist({
    dom,
    notice,
    refit: () => { if (state.term) state.term.refit(); },
    current: () => state.current,
    live: () => state.live,
  });
  followViewport();
  // Read before anything is fetched: on an offline reload this is the only
  // thing the page knows about what it was doing.
  state.wanted = location.hash.replace(/^#/, '');
  state.loading = true;
  renderList();
  harnesses.load().catch(() => { /* the sheet says so when it is opened */ });
  try {
    const prefs = await api('/api/preferences', { attempts: 1, timeout: 8000 });
    if (prefs && prefs.terminal) state.terminal = { ...state.terminal, ...prefs.terminal };
  } catch { /* the defaults above are the shipped ones */ }
  await refreshList();
  // A tab with no session in its URL opens the newest one. From here on
  // `openWanted` is the only thing that attaches on its own, so there is one
  // rule about when that is allowed and one place it lives.
  if (!state.wanted || !sessionOf(state.wanted)) {
    const first = state.sessions.find((s) => !s.archived) || state.sessions[0];
    state.wanted = first ? first.id : state.wanted;
  }
  openWanted();
  if (!state.current && !state.socket && !state.pendingAttach) showEmpty();
}

boot();
