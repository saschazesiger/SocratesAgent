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
// The key bar lives in keybar.js and is mounted here, because it sends through
// the same numbered input path everything else does and there is exactly one
// of it per session.

import {
  api, el, toast, infoTip, confirmDialog, alertDialog, errorMessage, isOffline, isBusyConflict,
  setClass, onWake, clientKey, CONNECTION_GRACE, restoreFocus, setFocusFallback,
} from './api.js';
import { connectionSource } from './net.js';
import { agentMark } from './logos.js';
import { MORE_KEY, SHELVES, IDE, foldersOf, folderKeyOf, ideOf, momentOf, movesRow } from './folders.js';
import * as harnesses from './harnesses.js';
import { createTerm, measurePane, loadTerminalFonts } from './term.js';
import { mountAssist } from './assist.js';
import { mountNotify } from './notify.js';
import { mountTransfer } from './transfer.js';
import { mountKeyBar, keyBarWanted, setKeyBarWanted, followViewport } from './keybar.js';
import { initKeyboard, guard, watchFrame, dictated } from './keyboard.js';
import { mountPrestart, forgetDraft, whenShort, PRESTART_SVG, CLOCK_SVG } from './prestart.js';
import { mountStorageBanner } from './storage.js';

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

// The input watchdog. The server acknowledges input every hundred
// milliseconds, so a keystroke that has been on a wire and is still
// unacknowledged after this is not a slow link, it is a socket that is open
// and dead - the half-open case a phone produces several times a day, and the
// one the person reports as "I cannot type any more". It reconnects the
// socket, and only the socket: a program that prints nothing for an hour is a
// normal program, and nothing here ever touches the tmux session.
const INPUT_ACK_TIMEOUT = 8000;
const INPUT_WATCH_EVERY = 2000;

// How long a browser that says it has no network is left alone before a
// handshake is tried anyway. `online` is the signal this waits for, and iOS
// after a lock does not always raise one - so it is a floor, not the only way
// back.
const OFFLINE_RETRY = 5000;

// What may be held for a socket that is not open. It is a bound on memory and
// on how much of a burst a person can be handed back, not a policy: the
// ordinary outage holds a handful of keystrokes. What is over the bound is the
// oldest, and it is said out loud rather than dropped in silence.
const MAX_HELD_FRAMES = 512;
const MAX_HELD_BYTES = 128 * 1024;

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
    this.inputTimer = null;
    this.lastLag = 0;
    // heldBytes is what the queue below is bounded by, kept beside it rather
    // than summed on every keystroke.
    this.heldBytes = 0;

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
    if (this.inputTimer) { clearInterval(this.inputTimer); this.inputTimer = null; }
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
      // A wake event is the good reason to try again, but it is not a
      // guarantee: an iPhone coming back from a lock screen sometimes raises
      // none at all, and a socket that waits for a signal that never comes is
      // a session that can never be typed into again.
      this.report('offline', { immediate: true, retryAt: Date.now() + OFFLINE_RETRY, retryNow: () => this.reconnect(0) });
      this.retryTimer = setTimeout(() => { this.retryTimer = null; this.open(); }, OFFLINE_RETRY);
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
    this.countHeld();
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
      this.countHeld();
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
      held.at = Date.now();
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
    this.countHeld();
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
        held.at = Date.now();
        held.sent = this.write(held.frame) || held.sent;
      }
    }
  }

  /** countHeld re-derives the queue's size after it has been filtered. */
  countHeld() {
    let bytes = 0;
    for (const held of this.held) bytes += held.bytes.length;
    this.heldBytes = bytes;
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
    // The input watchdog. A socket that is open, anchored and answering
    // nothing about what was typed into it is a socket that has to be made
    // again - and the frames are still held, so making it again delivers
    // them.
    this.inputTimer = setInterval(() => {
      if (!this.isOpen() || !this.anchored) return;
      const now = Date.now();
      for (const held of this.held) {
        if (held.sent && now - (held.at || now) > INPUT_ACK_TIMEOUT) {
          this.report('connecting', { immediate: true });
          this.reconnect(0);
          return;
        }
      }
    }, INPUT_WATCH_EVERY);
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
      // at is when these bytes were last put on a wire, which is what the
      // input watchdog measures.
      at: 0,
      // sent records whether these bytes have ever been on a wire, which is
      // what decides their fate when the server turns out not to remember
      // this viewer: only what was sent can be a resend.
      sent: false,
    };
    this.held.push(entry);
    this.heldBytes += bytes.length;
    this.trimHeld();
    // Only a socket that has been anchored by its hello may send: before that
    // the number on the frame means nothing to the server, and a number below
    // its own is a keystroke silently thrown away.
    if (this.anchored) {
      entry.at = Date.now();
      entry.sent = this.write(entry.frame);
    }
  }

  /**
   * trimHeld keeps the queue of undelivered input bounded.
   *
   * An outage that lasts is still an outage: the queue may not grow until the
   * tab dies of it. What goes is the oldest, because the newest is what the
   * person is typing now - and what goes is counted and said, which is the
   * whole rule: input is either delivered or reported, never quietly gone.
   */
  trimHeld() {
    if (this.held.length <= MAX_HELD_FRAMES && this.heldBytes <= MAX_HELD_BYTES) return;
    const lines = [];
    let keystrokes = 0;
    while (this.held.length > 1
      && (this.held.length > MAX_HELD_FRAMES || this.heldBytes > MAX_HELD_BYTES)) {
      const gone = this.held.shift();
      this.heldBytes -= gone.bytes.length;
      if (gone.text !== undefined) lines.push(gone.text);
      else if (gone.bytes[0] !== 0x1b) keystrokes += 1;
      if (gone.onLost) gone.onLost();
    }
    if (keystrokes || lines.length) this.onControl({ t: 'input_lost', keystrokes, lines });
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
  current: null,      // the session row being shown
  socket: null,
  term: null,
  keybar: null,
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
  // replaces it rather than stacking on it, and the button it hangs from.
  menu: null,
  menuAnchor: null,
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
  // The sessions whose automatic name is being asked for right now. Their
  // name pulses, in the list and in the header, until the answer is in.
  titling: new Set(),
  // The current session's quota/cost poll. A generation number makes a slow
  // answer for the previous session harmless after a quick row switch.
  usageTimer: null,
  usageGeneration: 0,
  // The session that poll is for, so a newer row of the same session keeps it.
  usageFor: '',
  usageRefresh: null,
  // Status and the microphone, mounted once in boot().
  assist: null,
  // Copy, paste and upload, mounted once in boot().
  transfer: null,
  // How the terminal is drawn, from the dashboard. The defaults here are what
  // a page that could not ask uses, and they are the same numbers the settings
  // document ships with.
  terminal: { scrollback: 2000, font_size: 14, webgl: true },
  // The panel a prepared session is shown in instead of a terminal,
  // mounted once in boot().
  prestart: null,
};

const dom = {};
const ids = ['sidebar', 'navScrim', 'menuBtn', 'sideCollapse', 'newSession', 'sessionList',
  'activityLive', 'sessionTitle', 'sessionStatus', 'sessionUsage',
  'transfer', 'copyBtn', 'pasteBtn', 'uploadBtn',
  'statusBtn', 'kbBtn', 'soundBtn', 'notifyBtn', 'sessionMenu',
  'stage', 'termWrap', 'term', 'termOverlay', 'termLines', 'termNotice',
  'termTicker', 'tickerWindow', 'termEmpty',
  'micBtn', 'micText',
  'recSheet', 'recMeter', 'recTime', 'recSend', 'recCancel',
  'keybar', 'logout', 'prestart'];
for (const id of ids) dom[id] = document.getElementById(id);

const STATE_WORDS = {
  running: 'Running', starting: 'Starting', resuming: 'Resuming',
  needs_resume: 'Not running', exited: 'Ended', failed: 'Failed',
  prestart: 'Prepared',
};

// isPrepared is a session that has its settings and its first prompt and has
// not been started: no terminal, no program, nothing to attach to.
function isPrepared(session) { return !!session && session.state === 'prestart'; }

// The group prepared sessions are listed in, at the very top of the list.
// Like MORE_KEY it is not a path, so it cannot be any project's folder.
const PRESTART_KEY = '#prestart';

// listKeyOf is the group a row is drawn in: its project's folder, or the
// prepared sessions' own group while it has not been started.
function listKeyOf(session) { return isPrepared(session) ? PRESTART_KEY : folderKeyOf(session); }

// What a session is doing, in the words the "i" uses. `unknown` has no word:
// a row with no evidence yet says nothing rather than guessing.
const ACTIVITY_WORDS = {
  busy: 'Working',
  idle: 'Waiting for an instruction',
  waiting: 'Needs an answer',
  tasks: 'Turn finished · a background task is still running',
};

// The hourglass a row's mark carries while the agent's turn is over and
// something it started - a background shell, a monitor, a sub-agent - is
// still running: not working, and not done either.
const TASKS_SVG = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
  + '<path d="M5 2.5h14M5 21.5h14"/><path d="M7 2.5v2.2c0 2.6 5 4.6 5 7.3 0 2.7-5 4.7-5 7.3v2.2"/>'
  + '<path d="M17 2.5v2.2c0 2.6-5 4.6-5 7.3 0 2.7 5 4.7 5 7.3v2.2"/>'
  + '<path d="M8.5 20.5c0-1.6 3.5-3 3.5-4.5 0 1.5 3.5 2.9 3.5 4.5z" fill="currentColor" stroke-width="1"/></svg>';

/**
 * activityOf is what a session is doing, for the page to draw.
 *
 * A shell and an editor have none. The detector still watches them - the
 * server needs to know whether a pane is doing anything - but "working" and
 * "finished" are words about a turn, and neither of those two programs takes
 * one: a shell is busy for as long as `ls` runs and an editor is busy while it
 * boots, and a spinner, a bold name or a chime for either of those is the page
 * announcing that a window opened. So the row of a tool carries no status at
 * all, which is also the whole of why it can never chime.
 */
function activityOf(session) {
  if (!session || harnesses.isTool(session.harness)) return null;
  return state.activity.get(session.id) || null;
}

// Where the work in a session stands, as the person files it. In progress is
// what every session starts as and says nothing; the others carry a mark in
// the row and a word in the header. Done is the one that does something: the
// server closes the terminal and deletes the session a day later. Background
// is a service left running on purpose: out of the way, never refiled, never
// deleted.
const STATUSES = [
  { id: 'in_progress', label: 'In progress' },
  { id: 'review', label: 'Review pending' },
  { id: 'pending', label: 'Pending' },
  { id: 'background', label: 'Background' },
  { id: 'done', label: 'Done' },
];
const STATUS_WORDS = Object.fromEntries(STATUSES.map((s) => [s.id, s.label]));

// One small drawing per status that has a mark: an eye for a review, a clock
// for pending, stacked layers for background, a tick for done.
const STATUS_SVG = {
  review: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg>',
  pending: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>',
  background: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M12 3l9 5-9 5-9-5 9-5z"/><path d="M3 13l9 5 9-5"/></svg>',
  done: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M5 12.5l4.5 4.5L19 7.5"/></svg>',
};

// A session a row upgraded from flags and an archive may still be unsorted for
// its first seconds; until the server files it, it reads as in progress.
function statusOf(session) {
  return (session && STATUS_WORDS[session.status]) ? session.status : 'in_progress';
}

function isDone(session) { return statusOf(session) === 'done'; }

// deleteWords is when a done session goes, as a person would say it.
function deleteWords(session) {
  const at = Number(session && session.delete_at);
  if (!at) return '';
  const hours = Math.max(0, Math.round((at - Date.now()) / 3600000));
  const when = new Date(at);
  const clock = String(when.getHours()).padStart(2, '0') + ':' + String(when.getMinutes()).padStart(2, '0');
  return hours >= 1 ? 'Deleted in ' + hours + ' h, at ' + clock : 'Deleted within the hour';
}

function sessionOf(id) { return state.sessions.find((s) => s.id === id) || null; }

/* -------------------------------------------------------- the session list */

function renderList() {
  const host = dom.sessionList;
  const wanted = state.sessions;
  if (!wanted.length) {
    host.innerHTML = '';
    let empty = 'No sessions yet.';
    if (state.loading) empty = 'Loading…';
    else if (!state.reachable) empty = 'Can’t reach Socrates.';
    host.append(el('div', { class: 'list-empty', text: empty }));
    return;
  }
  // Which project each session belongs to, and in what order. The server
  // hands the list over newest first, and a frame that changes a status
  // between two fetches bumps the moment locally, so the order inside a
  // folder - and the order of the folders themselves - is worked out here,
  // from the same moment the server sorted by.
  // Background and done sessions are not the work in progress, but they are
  // still their project's: each folder keeps them on a shelf of its own at
  // its bottom, shut until it is opened - the services first, then what is
  // finished, most recently filed first, until the server deletes it. A
  // folder's VS Code is not a row at all: it is drawn on the folder's head.
  // Prepared sessions are not any project's yet - nothing has run in their
  // directory, and a dynamic one does not even exist - so they are one group
  // of their own above every folder: what is waiting to be started.
  const folders = foldersOf(wanted.filter((s) => !isPrepared(s)));
  const prepared = preparedGroup(wanted);
  if (prepared) folders.unshift(prepared);
  // The list is patched rather than rebuilt: taking a connected node out of
  // the page and putting it back restarts its animations, and a row that
  // fades in again every few seconds is a page that looks broken. The folders
  // are patched the same way and for the same reason - and because a row
  // whose session finishes a turn, and so moves to the top of its folder, has
  // to keep the element it already had while it moves.
  const seenFolders = new Set();
  const seenShelves = new Set();
  const seenRows = new Set();
  // placer walks one parent: it puts a node where the walk has got to, and
  // only if it is not there already. The list is two walks - the folders, and
  // the rows inside each - so it is a maker of walkers rather than one walk.
  const placer = (into) => {
    let previous = null;
    return (node) => {
      if (previous ? previous.nextElementSibling !== node : into.firstElementChild !== node) {
        if (previous) previous.after(node); else into.prepend(node);
      }
      previous = node;
    };
  };
  const placeFolder = placer(host);
  for (const folder of folders) {
    seenFolders.add(folder.key);
    const section = host.querySelector('.folder[data-folder="' + cssId(folder.key) + '"]')
      || buildFolder(folder);
    placeFolder(section);
    const placeRow = placer(section.querySelector(':scope > .folder-rows'));
    placeRows(host, placeRow, folder.sessions, seenRows);
    // The shelves come after the rows, inside the same folder, so shutting
    // the folder puts them away with everything else in it.
    for (const status of SHELVES) {
      const filed = folder.shelves[status];
      if (!filed.length) continue;
      const shelf = { key: shelfKey(folder.key, status), status, label: STATUS_WORDS[status], path: '', sessions: filed };
      const node = section.querySelector(':scope > .folder-rows > .shelf[data-shelf="' + status + '"]')
        || buildFolder(shelf);
      seenShelves.add(node);
      placeRow(node);
      placeRows(host, placer(node.querySelector(':scope > .folder-rows')), filed, seenRows);
      updateFolder(node, shelf);
    }
    updateFolder(section, folder);
  }
  // What is left over: a session that was deleted, and a folder or a shelf
  // whose last session went with it. The rows are swept after every folder
  // has been walked rather than folder by folder, because a row that moved
  // to a folder further down the list has not been drawn yet while the one
  // it left is being tidied, and sweeping it there would throw away the
  // element it is about to be put back into.
  for (const row of [...host.querySelectorAll('.chat-item')]) {
    if (!seenRows.has(row.dataset.id)) row.remove();
  }
  for (const shelf of [...host.querySelectorAll('.shelf')]) {
    if (!seenShelves.has(shelf)) shelf.remove();
  }
  for (const node of [...host.children]) {
    if (!node.classList.contains('folder') || !seenFolders.has(node.dataset.folder)) node.remove();
  }
}

// preparedGroup is the prepared sessions as a folder the list can draw, or
// null when there are none. The ones that start by themselves come first,
// the soonest at the top - that is the order they will happen in - and the
// rest follow newest first.
function preparedGroup(sessions) {
  const rows = sessions.filter(isPrepared);
  if (!rows.length) return null;
  rows.sort((a, b) => {
    const at = Number(a.start_at) || 0;
    const bt = Number(b.start_at) || 0;
    if (!at !== !bt) return at ? -1 : 1;
    if (at && bt && at !== bt) return at - bt;
    return momentOf(b) - momentOf(a);
  });
  return {
    key: PRESTART_KEY, label: 'preStart', path: '', sessions: rows, ide: null,
    shelves: Object.fromEntries(SHELVES.map((status) => [status, []])),
  };
}

// placeRows draws one run of rows where `place` has got to. A row is looked
// for in the whole list rather than under this folder: whichever folder it
// was drawn in last time, it is the same session, and it keeps the element -
// and the animation - it already had.
function placeRows(host, place, sessions, seen) {
  for (const session of sessions) {
    seen.add(session.id);
    const found = host.querySelector('.chat-item[data-id="' + cssId(session.id) + '"]');
    if (found) updateRow(found, session);
    place(found || buildRow(session));
  }
}

// An id or a directory, as an attribute selector can carry it. Both come from
// the machine rather than from a person, but a directory is a name somebody
// chose, and a quote or a backslash in one must not be able to become part of
// the selector around it.
function cssId(id) { return String(id).replace(/\\/g, '\\\\').replace(/"/g, '\\"'); }

/* ------------------------------------------------------------- the folders

   Which project a session belongs to is folders.js's answer; what is left
   here is the drawing of it, and the one thing a person does to a folder,
   which is to shut it. */

// Which folders this device keeps shut. Only the shut ones are written down,
// so a project that appears while the browser was closed - the first session
// in a new directory - is open when it arrives, which is what a folder that
// has just been made should be. It is a fact about this screen rather than
// about the account, for the same reason the rail is: a column tidied on a
// desk is not a column tidied on a phone.
const FOLDERS_KEY = 'socrates.folders';
const closedFolders = new Set();

// The shelves of background and done sessions inside each folder. They are
// the two that start shut - a service is meant to run unseen, and what is
// done is finished - so for them the set records the opposite: being in it
// means the person opened it. A shelf's key is its folder's and its status,
// joined by a character no path and no status can hold.
function shelfKey(folderKey, status) { return folderKey + '\u0000' + status; }
function shelfOf(key) {
  const at = key.lastIndexOf('\u0000');
  return at < 0 ? '' : key.slice(at + 1);
}
const SHELF_TITLES = {
  background: 'Background sessions: services left running on purpose, never deleted',
  done: 'Done sessions: their terminals are closed, and each is deleted a day after it was marked done',
};

function shutByDefault(key) { return SHELVES.includes(shelfOf(key)); }

function folderOpen(key) {
  return shutByDefault(key) ? closedFolders.has(key) : !closedFolders.has(key);
}
// aria-controls needs something to point at, and a path is not an id.
let folderSeq = 0;

function restoreFolders() {
  let raw = '';
  try { raw = localStorage.getItem(FOLDERS_KEY) || ''; } catch { /* private window */ }
  if (!raw) return;
  try {
    const shut = JSON.parse(raw);
    if (Array.isArray(shut)) for (const key of shut) if (typeof key === 'string') closedFolders.add(key);
  } catch { /* a value from a version that wrote something else */ }
}

function saveFolders() {
  try { localStorage.setItem(FOLDERS_KEY, JSON.stringify([...closedFolders])); } catch { /* private window */ }
}

function toggleFolder(key) {
  if (closedFolders.has(key)) closedFolders.delete(key); else closedFolders.add(key);
  saveFolders();
  renderList();
}

/**
 * openFolder makes sure a session can be seen.
 *
 * It is called when one is attached rather than when one is clicked: a row
 * inside a shut folder cannot be clicked at all, and the session this tab is
 * meant to be on - the one in the address, or the one just started - has to
 * be on screen whatever the folder it landed in was left as. A shelved
 * session needs its shelf opened as well as its folder; an editor is on the
 * folder's head, which is on screen whenever the folder is.
 */
function openFolder(session) {
  if (!session) return;
  const key = listKeyOf(session);
  let changed = closedFolders.delete(key);
  const status = statusOf(session);
  if (!isPrepared(session) && SHELVES.includes(status)
    && !(session.harness === IDE && ideOf(state.sessions, key) === session)) {
    const shelf = shelfKey(key, status);
    if (!closedFolders.has(shelf)) { closedFolders.add(shelf); changed = true; }
  }
  if (!changed) return;
  saveFolders();
  renderList();
}

const CARET_SVG = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"'
  + ' stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>';

// A folder is its head and its rows; a shelf is the same shape one level
// down, inside its folder's rows. A project folder's head carries one more
// control beside the name: the mark of VS Code, which opens the folder's
// editor. It is a button beside the head rather than inside it, because a
// button cannot hold another.
function buildFolder(folder) {
  folderSeq += 1;
  const id = 'folder-rows-' + folderSeq;
  const head = el('button', {
    class: 'folder-head',
    type: 'button',
    'aria-expanded': 'true',
    'aria-controls': id,
    onclick: () => toggleFolder(folder.key),
  },
  el('span', { class: 'caret', html: CARET_SVG }),
  // The prepared sessions' group wears the mark their panel wears, so it
  // reads as a different kind of thing from a project.
  folder.key === PRESTART_KEY ? el('span', { class: 'folder-icon', html: PRESTART_SVG }) : null,
  el('span', { class: 'folder-name' }),
  el('span', { class: 'folder-dot', 'aria-hidden': 'true' }),
  el('span', { class: 'folder-count' }));
  // Nothing has run in a prepared session's directory yet, so its group has
  // no editor to open.
  const ide = folder.status || folder.key === MORE_KEY || folder.key === PRESTART_KEY ? null : el('button', {
    class: 'icon-btn folder-ide',
    type: 'button',
    onclick: () => openFolderIDE(folder.key),
  }, agentMark(IDE, 15));
  const attrs = folder.status
    ? { class: 'folder shelf', 'data-shelf': folder.status }
    : { class: 'folder' + (folder.key === PRESTART_KEY ? ' prestart-group' : ''), 'data-folder': folder.key };
  return el('section', attrs,
    el('div', { class: 'folder-bar' }, head, ide),
    el('div', { class: 'folder-rows', id }));
}

function updateFolder(section, folder) {
  const open = folderOpen(folder.key);
  const head = section.querySelector(':scope > .folder-bar > .folder-head');
  const rows = section.querySelector(':scope > .folder-rows');
  setClass(section, 'closed', !open);
  head.setAttribute('aria-expanded', open ? 'true' : 'false');
  rows.hidden = !open;
  head.querySelector('.folder-name').textContent = folder.label;
  // The whole path is technical detail, so it is where every technical string
  // on this page is: on hover, and nowhere in the words. "More Chats" and the
  // shelves have no path, so their tooltip says what they hold instead.
  head.title = SHELF_TITLES[folder.status] || folder.path
    || (folder.key === PRESTART_KEY
      ? 'Prepared sessions: settings and a first prompt, started by hand or at a time'
      : 'Sessions in a directory of their own');
  // Shut, a folder has to say what is behind it: the two things this page
  // exists to make noticeable - an agent that needs an answer, and one that
  // finished something nobody has seen - would otherwise be hidden by the
  // person's own tidying. So it carries a count, the amber of a session
  // waiting, and the weight of one nobody has read. Open, the rows say all
  // three themselves and the head says none of them.
  let waiting = false;
  let busy = false;
  let unread = false;
  for (const session of folder.sessions) {
    const act = activityOf(session);
    if (!act) continue;
    const attached = !!(state.current && state.current.id === session.id);
    if (act.unread && !attached) unread = true;
    const live = session.state === 'running' || session.state === 'starting' || session.state === 'resuming';
    if (!live) continue;
    if (act.state === 'waiting') waiting = true;
    // Work left running behind a finished turn is still something going on
    // in there, and the closed folder says so the same quiet way.
    if (act.state === 'busy' || act.state === 'tasks') busy = true;
  }
  setClass(head, 'waiting', waiting);
  setClass(head, 'busy', busy && !waiting);
  setClass(head, 'unread', unread);
  let count = folder.sessions.length;
  if (folder.shelves) for (const status of SHELVES) count += folder.shelves[status].length;
  head.querySelector('.folder-count').textContent = String(count);
  const ide = section.querySelector(':scope > .folder-bar > .folder-ide');
  if (ide) updateIDEButton(ide, folder);
}

// updateIDEButton draws the VS Code mark of a folder: coloured while the
// folder's editor is running, grey while there is none or it is stopped, and
// pressed while it is the one on screen. Where VS Code is switched off and
// the folder has no editor to return to, the mark is not there.
function updateIDEButton(button, folder) {
  const ide = folder.ide;
  const usable = harnesses.ideAvailable();
  button.hidden = !ide && !usable;
  const running = !!ide && (ide.state === 'running' || ide.state === 'starting');
  const shown = !!ide && !!state.current && state.current.id === ide.id;
  setClass(button, 'running', running);
  setClass(button, 'active', shown);
  button.setAttribute('aria-pressed', shown ? 'true' : 'false');
  const words = (running ? 'VS Code is open in ' : 'Open VS Code in ') + folder.label;
  button.title = words;
  button.setAttribute('aria-label', words);
}

// The folders a VS Code is being asked for right now, so that a second tap
// on the same mark waits for the first rather than asking again.
const openingIDE = new Set();

/**
 * openFolderIDE is the VS Code mark of a folder: the folder's editor, opened
 * in the pane. A folder that has one opens it, and one that does not asks the
 * server for it - which answers with the one it has if another tab made it in
 * the meantime, so there is still only ever one per folder.
 */
async function openFolderIDE(key) {
  const found = ideOf(state.sessions, key);
  if (found) { selectSession(found.id); return; }
  if (openingIDE.has(key)) return;
  openingIDE.add(key);
  try {
    const data = await api('/api/ide', { method: 'POST', attempts: 2, timeout: 60000, body: { workdir: key } });
    replaceSession(data.session);
    if (data.error) toast('VS Code could not start.', 'error');
    selectSession(data.session.id);
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    openingIDE.delete(key);
  }
}

function buildRow(session) {
  const row = el('div', {
    class: 'chat-item',
    'data-id': session.id,
    role: 'button',
    tabindex: '0',
  },
  el('span', { class: 'row-mark' }, agentMark(session.harness, 18),
    el('span', { class: 'row-tasks', html: TASKS_SVG })),
  // The name, and under it - for a prepared session only - the first line
  // of its prompt, which is what tells two prepared sessions apart before
  // either has a name of its own.
  el('span', { class: 'row-text' },
    el('span', { class: 'label' }),
    el('span', { class: 'row-preview' })),
  // When a prepared session starts by itself: a small clock and the time.
  el('span', { class: 'row-when' }),
  el('span', { class: 'row-status' }),
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
  if (session.harness === 'shell' && session.options?.shell_execution) {
    facts.push('Execution: ' + (session.options.shell_execution === 'host' ? 'Host' : 'Container'));
  }
  if (session.options?.profile) facts.push('Profile: ' + harnesses.profileName(session.options.profile));
  if (session.options?.ultracode) facts.push('Ultracode on');
  if (isPrepared(session)) {
    if (session.options?.profile_auto) facts.push('Profile: chosen when it starts');
    if (Number(session.start_at)) facts.push('Starts by itself ' + new Date(Number(session.start_at)).toLocaleString());
  }
  const act = activityOf(session);
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
  const label = row.querySelector('.label');
  label.textContent = session.title;
  setClass(label, 'titling', state.titling.has(session.id));
  // Collapsed, a row is one mark and no words, so the name moves into the
  // tooltip - the only place left where it can be read. Expanded it is written
  // on the row, and a tooltip repeating it would be noise.
  row.title = sidebarCollapsed() ? session.title : '';
  const attached = !!(state.current && state.current.id === session.id);
  setClass(row, 'active', attached);
  // A row is the same element before and after its session starts, so what
  // only a prepared session shows is taken away again here.
  const prepared = isPrepared(session);
  setClass(row, 'prepared', prepared);
  const preview = row.querySelector('.row-preview');
  const previewText = prepared ? String(session.draft_preview || '') : '';
  if (preview.textContent !== previewText) preview.textContent = previewText;
  const when = row.querySelector('.row-when');
  const startAt = prepared ? Number(session.start_at) || 0 : 0;
  const whenText = startAt ? whenShort(startAt) : '';
  if (when.dataset.at !== String(startAt)) {
    when.dataset.at = String(startAt);
    when.innerHTML = startAt ? CLOCK_SVG : '';
    if (startAt) when.append(el('span', { class: 'row-when-time', text: whenText }));
  } else if (startAt) {
    // The same time can read differently tomorrow ("Tue 09:00" becomes
    // "09:00"), so the words are drawn again even when the time is not.
    const time = when.querySelector('.row-when-time');
    if (time && time.textContent !== whenText) time.textContent = whenText;
  }
  if (startAt) {
    const words = 'Starts by itself ' + new Date(startAt).toLocaleString();
    when.title = words;
    when.setAttribute('aria-label', words);
  } else {
    when.removeAttribute('title');
    when.removeAttribute('aria-label');
  }
  // The status is a mark after the name, and in progress has none: it is
  // what a session is unless somebody said otherwise.
  const status = statusOf(session);
  setClass(row, 'done', status === 'done');
  const statusMark = row.querySelector('.row-status');
  if (statusMark.dataset.status !== status) {
    statusMark.dataset.status = status;
    statusMark.innerHTML = STATUS_SVG[status] || '';
  }
  const said = status === 'done'
    ? [STATUS_WORDS.done, deleteWords(session)].filter(Boolean).join(' · ')
    : STATUS_WORDS[status];
  statusMark.title = status === 'in_progress' ? '' : said;
  if (status === 'in_progress') statusMark.removeAttribute('aria-label'); else statusMark.setAttribute('aria-label', said);

  // The activity of a row is two marks and no words: the agent's own mark
  // turns into a ring that spins while the harness is working and stands
  // still, in amber, while it is waiting for an answer, and a name that has
  // finished something nobody has seen is bold. The session being looked at
  // is never bold - it is not unread, it is open. A turn that is over while
  // something it started still runs has no ring - nothing is being worked on
  // - and a small hourglass on the mark's corner instead.
  const act = activityOf(session);
  const mark = row.querySelector('.row-mark');
  const live = session.state === 'running' || session.state === 'starting' || session.state === 'resuming';
  const busy = live && !!act && act.state === 'busy';
  const waiting = live && !!act && act.state === 'waiting';
  setClass(mark, 'busy', busy);
  const tasks = live && !!act && act.state === 'tasks';
  setClass(mark, 'waiting', waiting);
  setClass(mark, 'tasks', tasks);
  let word = STATE_WORDS[session.state] || session.state;
  if (busy) word = ACTIVITY_WORDS.busy;
  if (waiting) word = ACTIVITY_WORDS.waiting;
  if (tasks) word = ACTIVITY_WORDS.tasks;
  mark.title = word;
  setClass(row, 'unread', !!act && !!act.unread && !attached);
}

// The chime and the notification, mounted once in boot(). It is not on
// `state` because it is not state: it is two switches on this device and one
// call, and nothing on the page ever asks it a question.
let notifier = null;

/**
 * mergeActivity takes a map of committed changes and draws them.
 *
 * It is the one door: the poll, the handshake and the broadcast frame all
 * come through here, so the sidebar and the announcement cannot disagree
 * about what changed.
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
    const session = sessionOf(id);
    // A status change moves the row, and it moves it now rather than at the
    // next fetch: the server wrote the same moment into the same row under
    // the same rule, so the list a fetch would bring is the list drawn here.
    if (session && movesRow(prev ? prev.state : '', next.state)) {
      session.active_at = Math.max(momentOf(session), Number(next.since) || Date.now());
    }
    // A pending session, or one waiting for a review, that starts working may
    // just have been filed as in progress again by the server. The frame does
    // not carry the status, so the list is asked for it.
    const status = statusOf(session);
    if (session && next.state === 'busy' && (!prev || prev.state !== 'busy')
      && (status === 'pending' || status === 'review')) refreshSoon();
    // Only a session the page can name is worth saying anything about, and a
    // tool has nothing to say: see activityOf. Neither does a service somebody
    // put in the background, which is there so as not to interrupt anyone.
    if (!session || harnesses.isTool(session.harness) || statusOf(session) === 'background') continue;
    announce(id, next, prev);
    if (notifier) notifier.completed(id, next, prev);
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
  // The unread mark is the server's own answer to "did something happen that
  // nobody has seen", and it is only set for a session that has been given
  // something to do. Without that test this line, the chime and the
  // notification all fire the moment a harness finishes starting up - which
  // is the page announcing that a window opened.
  //
  // And it has to be a mark this change set. A relaunch keeps the one it
  // found, so a session started with unseen work on it would otherwise have
  // its start-up read out as a turn that finished: see notify.completed.
  if (!next.unread || (prev && prev.unread)) return;
  const who = harnesses.label(session.harness) || session.title;
  let line = '';
  // A turn that ends with something left running is not finished; leaving
  // that state is (the server sets no unread mark on the way into it).
  const before = prev && (prev.state === 'busy' || prev.state === 'tasks');
  if (before && next.state !== 'busy' && next.state !== 'tasks' && next.state !== 'waiting') line = who + ' finished';
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

// refreshSoon asks for the list once, a moment from now, however many
// changes ask for it in between.
let refreshTimer = null;
function refreshSoon() {
  if (refreshTimer) return;
  refreshTimer = setTimeout(() => { refreshTimer = null; refreshList(); }, 1500);
}

async function refreshList() {
  try {
    const data = await api('/api/sessions', { attempts: 2, timeout: 12000 });
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
      else if (state.prestart && state.prestart.shownId() === state.current.id) {
        // A prepared session deleted on another device. A terminal finds
        // out through its socket; a panel has none, and would otherwise go
        // on saving into a session that is not there.
        detach();
        showEmpty();
      }
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
      // A name is a sentence somebody says about a session, so it is one of
      // the fields that can be spoken instead of typed - and with the
      // keyboard put away it is the only way this dialog can be answered.
      el('div', { class: 'field' }, dictated(input, input)),
      el('div', { class: 'modal-actions' }, cancel, accept),
    );
    let settled = false;
    const was = document.activeElement;
    // The dialog is taken out of the page and the focus is put back where it
    // came from here, in the call that ends it, rather than in the `close`
    // listener alone: a dialog that is closed but left in the document keeps
    // the focus on its own button, and a page whose focus is on a button
    // nobody can see swallows every key that is typed at it. That is one of
    // the two ways this app could stop taking input without anything looking
    // wrong.
    const dismiss = () => {
      dialog.remove();
      restoreFocus(was);
    };
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
      dialog.close();
      dismiss();
    };
    accept.addEventListener('click', () => finish(input.value.trim() || current));
    cancel.addEventListener('click', () => finish(null));
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') { event.preventDefault(); finish(input.value.trim() || current); }
    });
    dialog.addEventListener('cancel', (event) => { event.preventDefault(); finish(null); });
    dialog.addEventListener('close', () => { finish(null); dismiss(); });
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

// setStatus files a session. Done closes its terminal on the server, so a
// session that is being looked at loses its socket and shows what happened
// instead; every other status changes nothing but the mark.
async function setStatus(session, status) {
  if (statusOf(session) === status) return;
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/file', {
      method: 'POST', body: { status },
    });
    const open = state.current && state.current.id === session.id;
    if (status === 'done' && open) detach();
    replaceSession(data.session);
    if (status === 'done') toast('Done. Its terminal was closed; the session is deleted in 24 hours.');
    else if (isDone(session) && open) attach(data.session);
  } catch (err) { toast(actionFailed(err, 'That session could not be refiled.'), 'error'); }
}

async function deleteSession(session) {
  const editor = session.harness === IDE;
  const yes = await confirmDialog({
    title: editor ? 'Close VS Code?' : 'Delete “' + session.title + '”?',
    body: editor
      ? 'The editor is stopped. What it remembers - open files, unsaved changes, layout - is kept for this folder and comes back the next time it is opened.'
      : isPrepared(session)
        ? 'The prepared session and its prompt are removed. Nothing was started.'
        : 'The terminal is closed and the session is removed. The working directory and everything in it are kept.',
    confirmLabel: editor ? 'Close' : 'Delete',
    danger: true,
  });
  if (!yes) return;
  try {
    await api('/api/sessions/' + encodeURIComponent(session.id), { method: 'DELETE' });
    forgetDraft(session.id);
    state.sessions = state.sessions.filter((s) => s.id !== session.id);
    if (state.current && state.current.id === session.id) {
      detach();
      const next = state.sessions.find((s) => !isDone(s)) || state.sessions[0];
      if (next) selectSession(next.id); else showEmpty();
    }
    renderList();
    toast(editor ? 'VS Code was closed. Its state was kept.'
      : isPrepared(session) ? 'Deleted.' : 'Deleted. The working directory was kept.');
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
  // A folder's editor is not work that is filed: it is open or it is not, and
  // closing it keeps everything it remembers, which is kept per folder.
  const editor = session.harness === IDE;
  // A prepared session has no terminal, so no keys, no scrollback and no
  // status to file it under yet: it can be looked at, named and deleted.
  const prepared = isPrepared(session);
  const menu = el('div', { class: 'menu', role: 'menu' },
    item('Info', () => infoDialog(session)),
    editor ? null : item('Rename', () => renameSession(session)),
    session.harness === 'vscode' || prepared ? null : item(dom.keybar.hidden ? 'Show key bar' : 'Hide key bar', () => {
      const on = dom.keybar.hidden;
      setKeyBarWanted(on);
      showKeyBar(on);
    }),
    editor || prepared ? null : el('div', { class: 'menu-label', text: 'Status' }),
    ...(editor || prepared ? [] : STATUSES.map((one) => item(one.label, () => setStatus(session, one.id),
      statusOf(session) === one.id ? 'checked' : ''))),
    el('div', { class: 'menu-sep' }),
    editor && state.current?.id === session.id ? item('Reload editor', () => attach(session)) : null,
    prepared ? null : item(editor ? 'Download server log' : 'Download scrollback', () => downloadJournal(session)),
    item(editor ? 'Close VS Code' : 'Delete', () => deleteSession(session), 'danger'));
  document.body.append(menu);
  const position = (event) => {
    if (event?.target === menu) return;
    const box = anchor.getBoundingClientRect();
    const viewport = window.visualViewport;
    const left = (viewport?.offsetLeft || 0) + 10;
    const top = (viewport?.offsetTop || 0) + 10;
    const right = left + (viewport?.width || window.innerWidth) - 20;
    const bottom = top + (viewport?.height || window.innerHeight) - 20;
    menu.style.minWidth = Math.min(190, right - left) + 'px';
    menu.style.maxWidth = Math.max(0, right - left) + 'px';
    menu.style.maxHeight = 'none';
    const below = Math.max(0, bottom - box.bottom - 6);
    const above = Math.max(0, box.top - 6 - top);
    const upwards = menu.offsetHeight > below && above > below;
    menu.style.maxHeight = Math.max(0, Math.min(bottom - top, upwards ? above : below)) + 'px';
    const y = upwards ? box.top - 6 - menu.offsetHeight : box.bottom + 6;
    menu.style.top = Math.max(top, Math.min(y, bottom - menu.offsetHeight)) + 'px';
    menu.style.left = Math.max(left, Math.min(box.left, right - menu.offsetWidth)) + 'px';
  };
  position();
  window.addEventListener('resize', position);
  document.addEventListener('scroll', position, true);
  window.visualViewport?.addEventListener('resize', position);
  window.visualViewport?.addEventListener('scroll', position);
  state.menuCleanup = () => {
    window.removeEventListener('resize', position);
    document.removeEventListener('scroll', position, true);
    window.visualViewport?.removeEventListener('resize', position);
    window.visualViewport?.removeEventListener('scroll', position);
  };
  state.menu = menu;
  // The button that opened the menu keeps the focus while it is open, and a
  // button with the focus answers Enter and space - so it is handed back when
  // the menu goes, or the next thing typed re-opens the menu instead of
  // reaching the pane.
  state.menuAnchor = anchor;
  setTimeout(() => document.addEventListener('click', closeMenu, { once: true }), 0);

  function item(text, run, kind) {
    return el('button', {
      class: 'menu-item ' + (kind || ''), type: 'button', role: 'menuitem', text,
      onclick: () => { closeMenu(); run(); },
    });
  }
}

function closeMenu() {
  if (!state.menu) return;
  state.menuCleanup?.();
  state.menuCleanup = null;
  state.menu.remove();
  state.menu = null;
  const anchor = state.menuAnchor;
  state.menuAnchor = null;
  if (anchor && document.activeElement === anchor) anchor.blur();
  // The menu took the focus out of the pane, so the menu gives it back.
  keepTypingAlive();
}

/**
 * keepTypingAlive puts the focus back in the terminal when nothing else wants
 * it.
 *
 * A menu, a dialog or a key that has been dismissed leaves the focus on the
 * body, and a body with the focus swallows every key silently: the pane keeps
 * showing, the socket keeps saying it is live, and nothing the person types
 * arrives anywhere. That is the shape of "I can no longer type into the
 * session" that has nothing to do with the network, so it is fixed by one
 * rule - if a session is open and the focus has landed on nothing, it belongs
 * to the pane.
 *
 * It never takes the focus from something that wants it: a field, a button, a
 * dialog and an open menu all keep it.
 */
function keepTypingAlive() {
  if (!state.term) return;
  if (state.menu || document.querySelector('dialog[open]')) return;
  const active = document.activeElement;
  if (active && active.isConnected
    && active !== document.body && active !== document.documentElement) return;
  state.term.focus();
}

/* ------------------------------------------------------- the terminal view */

function showEmpty() {
  state.current = null;
  dom.keybar.hidden = true;
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
  stopUsage();
  dom.sessionMenu.hidden = true;
  if (state.assist) state.assist.attached();
  if (state.transfer) state.transfer.attached();
  dom.sessionStatus.hidden = true;
  dom.termOverlay.hidden = true;
  clearNotice();
  if (state.reachable) { state.wanted = ''; location.hash = ''; }
}

// applySession draws everything about a session that is not the pane itself.
function applySession(session) {
  // A prepared session that has been started - by its own schedule, or on
  // another device - becomes the terminal it now has, exactly as it does
  // after this page's own Start. attach takes the panel down first, so this
  // is not reached again from there.
  if (state.prestart && state.prestart.shownId() === session.id && !isPrepared(session)) {
    attach(session);
    return;
  }
  // And the other way: a start this page saw as "starting" - and so opened
  // a terminal for - that failed before anything ran, and was put back to
  // prepared with the reason. The terminal has nothing to show (the server
  // refuses a socket for a prepared row); the panel, the reason and Start
  // come back. attach takes the terminal down first, so this is not reached
  // again from there either.
  if (isPrepared(session) && !(state.prestart && state.prestart.shownId() === session.id)
    && state.current && state.current.id === session.id
    && (state.socket || state.term || document.body.classList.contains('ide-open'))) {
    attach(session);
    return;
  }
  state.current = session;
  dom.sessionTitle.textContent = session.title;
  setClass(dom.sessionTitle, 'titling', state.titling.has(session.id));
  const status = statusOf(session);
  dom.sessionStatus.hidden = status === 'in_progress';
  dom.sessionStatus.textContent = STATUS_WORDS[status];
  dom.sessionStatus.dataset.status = status;
  dom.sessionMenu.hidden = false;
  dom.termEmpty.hidden = true;

  // What it runs and on which model is in the list's mark and in "Info" in
  // the session menu; the bar keeps its width for the name. A prepared
  // session has no conversation to cost anything yet, and its panel shows
  // each profile's limits where the profile is chosen.
  if (isPrepared(session)) {
    stopUsage();
    if (state.prestart) state.prestart.update(session);
  } else {
    startUsage(session);
  }

  drawOverlay(session);
  renderList();
}

const USAGE_REFRESH = 30000;

function stopUsage() {
  state.usageGeneration += 1;
  if (state.usageTimer) clearInterval(state.usageTimer);
  state.usageTimer = null;
  state.usageFor = '';
  state.usageRefresh = null;
  dom.sessionUsage.hidden = true;
  dom.sessionUsage.innerHTML = '';
  dom.sessionUsage.title = '';
  dom.sessionUsage.closest('.topbar').classList.remove('has-usage');
}

function startUsage(session) {
  // applySession runs on every newer row of the session on screen - a list
  // refresh, a rename, a found conversation - and each is a good moment to
  // read the numbers again. But the reading on screen stays until the new one
  // is in: starting over emptied the strip first, so the limits blinked away
  // on every refresh and, on a phone, the bar lost its second line and the
  // pane was resized twice for nothing.
  if (session && state.usageTimer && state.usageFor === session.id && state.usageRefresh) {
    state.usageRefresh();
    return;
  }
  stopUsage();
  if (!session || session.harness === 'vscode') return;
  state.usageFor = session.id;
  const generation = state.usageGeneration;
  const refresh = async () => {
    try {
      const usage = await api('/api/sessions/' + encodeURIComponent(session.id) + '/usage', {
        attempts: 1, timeout: 10000,
      });
      if (generation !== state.usageGeneration || !state.current || state.current.id !== session.id) return;
      drawUsage(usage);
    } catch { /* quota is useful context, never a reason to disturb the terminal */ }
  };
  refresh();
  state.usageRefresh = refresh;
  state.usageTimer = setInterval(refresh, USAGE_REFRESH);
}

function drawUsage(usage) {
  const windows = Array.isArray(usage && usage.windows) ? usage.windows : [];
  const parts = [];
  const details = [];
  for (const window of windows) {
    const used = Math.max(0, Math.min(100, Math.round(Number(window.used_percent) || 0)));
    // How long until the window renews is shown, not only hovered: whether
    // 80% is a problem depends on whether it is gone in ten minutes.
    const left = harnesses.untilReset(window);
    parts.push(el('span', { class: 'usage-part' },
      el('span', { class: 'usage-label', text: window.label }),
      el('span', { text: used + '%' }),
      left ? el('span', { class: 'usage-reset' }, '(' + left,
        el('span', { class: 'usage-left', text: ' left' }), ')') : null));
    details.push(window.label + ': ' + used + '% used' + (left ? ' · ' + harnesses.resetDetail(window) : ''));
  }
  if (Number.isFinite(usage && usage.cost_usd)) {
    const value = Number(usage.cost_usd);
    const shown = value > 0 && value < .01 ? '<$0.01' : '$' + value.toFixed(2);
    parts.push(el('span', { class: 'usage-part usage-cost', text: '~' + shown }));
    details.push('Estimated session API-equivalent cost: $' + value.toFixed(4));
  }
  dom.sessionUsage.innerHTML = '';
  dom.sessionUsage.append(...parts);
  dom.sessionUsage.title = details.join('\n');
  dom.sessionUsage.hidden = parts.length === 0;
  dom.sessionUsage.closest('.topbar').classList.toggle('has-usage', parts.length > 0);
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
  // A done session's terminal was closed on purpose, and that is the whole of
  // what there is to say about it, whatever state the row is in.
  if (isDone(session)) {
    show(
      el('p', { class: 'overlay-title', text: 'This session is done.' }),
      el('p', { class: 'overlay-detail', text: 'Its terminal was closed. ' + (deleteWords(session) || 'It is deleted soon') + '.' }),
      el('div', { class: 'overlay-actions' },
        el('button', {
          class: 'btn primary', type: 'button', text: 'Pick it up again',
          onclick: () => setStatus(session, 'in_progress'),
        }),
        el('button', { class: 'btn', type: 'button', text: 'Delete now', onclick: () => deleteSession(session) })),
    );
    return;
  }
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

// How long a line above the pane stays before it puts itself away. Long enough
// to read one sentence and look back at it - the same beat as the ticker's own
// linger in assist.js, because the two lines sit on top of each other and a
// reader should not have to learn two rhythms. A line that carries a control
// the person still has to press is never taken away on a timer; see `extra`
// below.
const NOTICE_LINGER = 6000;
// How long the leaving line is given to fade. It is the CSS transition on
// `.term-notice.leaving` plus a frame, and under reduced motion the CSS makes
// the fade instant while this stays the same.
const NOTICE_FADE = 180;

// The timers of the line that is up: the one that decides when it goes, and
// the one that takes it off the page once it has faded. There is only ever one
// notice, so there is only ever one of each.
let noticeTimer = null;
let noticeFade = null;

// clearNotice takes the line away with no ceremony and cancels whatever it had
// running. It is the path for "there is nothing to say any more" - a session
// being detached - rather than for "this has been read".
function clearNotice() {
  if (noticeTimer) { clearTimeout(noticeTimer); noticeTimer = null; }
  if (noticeFade) { clearTimeout(noticeFade); noticeFade = null; }
  dom.termNotice.classList.remove('leaving');
  dom.termNotice.hidden = true;
}

// notice draws the thin line at the top of the pane. It never blocks, it is
// always dismissible, and it puts itself away once it has been read;
// `onDismiss` is what a notice does when it goes, however it goes - the close
// button and the timer are the same decision, so the resume flag is cleared
// either way.
//
// `facts` is the technical half of what the notice knows - the conversation a
// resume came from, and anything else that is an identifier rather than a
// sentence. §E.10 rule 3: it goes behind the "i" and never into the line
// itself.
function notice(kind, text, onDismiss, facts, extra) {
  const host = dom.termNotice;
  // A new line replaces the one that is up, so the space is free for the next
  // one and no timer from the old line reaches the new one.
  if (noticeTimer) { clearTimeout(noticeTimer); noticeTimer = null; }
  if (noticeFade) { clearTimeout(noticeFade); noticeFade = null; }
  host.classList.remove('leaving');
  host.innerHTML = '';
  host.dataset.kind = kind;
  const dismiss = () => {
    if (host.hidden) return;
    if (noticeTimer) { clearTimeout(noticeTimer); noticeTimer = null; }
    host.classList.add('leaving');
    noticeFade = setTimeout(() => {
      noticeFade = null;
      host.hidden = true;
      host.classList.remove('leaving');
    }, NOTICE_FADE);
    if (onDismiss) onDismiss();
  };
  const close = el('button', {
    class: 'icon-btn notice-close', type: 'button', 'aria-label': 'Dismiss',
    html: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>',
    onclick: dismiss,
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
  // News goes when it has been read. A line with a control on it is not news:
  // a Cancel belongs to a run that is still running, and it stays until the
  // run does not need it any more.
  if (!extra) noticeTimer = setTimeout(dismiss, NOTICE_LINGER);
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

let ideGeneration = 0;

function detach() {
  ideGeneration += 1;
  document.body.classList.remove('ide-open');
  if (state.assist) state.assist.attached();
  if (state.keybar) { state.keybar.dispose(); state.keybar = null; }
  dom.keybar.hidden = true;
  if (state.socket) { state.socket.stop(); state.socket = null; }
  if (state.term) { state.term.dispose(); state.term = null; }
  dom.term.innerHTML = '';
  setClass(dom.termWrap, 'stale', false);
  state.live = false;
  state.resuming = false;
  if (state.transfer) state.transfer.attached();
  // A prepared session's panel stands where the pane does; whatever comes
  // next - a terminal, another panel, nothing - starts from the pane.
  if (state.prestart) state.prestart.hide();
  dom.termWrap.hidden = false;
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
  // A session nobody can see is a session nobody can leave: the row is the
  // only way back to it, so the folder it is in is opened if the person had
  // shut it.
  openFolder(session);
  applySession(session);
  // A prepared session has no terminal yet: its panel is drawn instead, with
  // no socket, no key bar and nothing to copy, paste or upload into.
  if (isPrepared(session)) {
    dom.termWrap.hidden = true;
    state.prestart.show(session);
    if (state.assist) state.assist.attached();
    if (state.transfer) state.transfer.attached();
    return;
  }
  // A done session has no terminal to open, and opening it must not start one:
  // the overlay applySession drew is all there is until it is picked up.
  if (isDone(session)) return;
  // A session that was just opened is still settling: see notify.hush.
  if (notifier) notifier.hush(session.id);
  if (session.harness === 'vscode') {
    document.body.classList.add('ide-open');
    openIDE(session, ideGeneration);
    return;
  }

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
  showKeyBar(keyBarWanted());
  if (state.transfer) state.transfer.attached();
  // A pane that has just been built has an ordinary text field behind it, and
  // it is focused below in this same tick - before the keyboard switch would
  // otherwise have seen it.
  guard(state.term.term.textarea);
  // The two controls in the header. A recording that belonged to the session
  // before this one is cancelled here, so its words can never be typed into
  // the terminal that has just taken the screen.
  if (state.assist) state.assist.attached();

  const size = state.term.size();
  socket.start(size.cols, size.rows);
  state.term.focus();
}

// Start through the existing resume API, then wait for the IDE's HTTP server.
// A generation keeps a slow launch from replacing a subsequently selected pane.
async function openIDE(session, generation) {
  if (['failed', 'exited'].includes(session.state)) return;
  const current = () => generation === ideGeneration;
  const base = '/ide/' + encodeURIComponent(session.id) + '/';
  // What stands on the stage until there is an editor on it: a turning mark
  // and a line of words, the same shape the pane's own overlay uses for a
  // session that is coming back.
  const words = el('p', { text: 'Opening VS Code…' });
  const spinner = el('div', { class: 'spinner' });
  const loading = el('div', { class: 'ide-loading' }, spinner, words);
  dom.term.replaceChildren(loading);
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/resume', {
      method: 'POST', timeout: 60000,
    });
    if (!current()) return;
    replaceSession(data.session);
    if (data.error) throw new Error(data.error);
    for (let attempt = 0; attempt < 60 && current(); attempt += 1) {
      try {
        const response = await fetch(base + 'healthz', { cache: 'no-store', signal: AbortSignal.timeout(3000) });
        if (response.ok) {
          if (!current()) return;
          const frame = el('iframe', {
            class: 'ide-frame', title: 'VS Code',
            src: base + '?folder=' + encodeURIComponent(session.workdir),
            allow: 'clipboard-read; clipboard-write',
          });
          // The card stays on top of the frame rather than being replaced by
          // it. `healthz` answers as soon as code-server's HTTP server is
          // listening, and the workbench is ten more seconds of white after
          // that - which is the one thing on this page that reads as broken.
          words.textContent = 'Loading the editor…';
          dom.term.replaceChildren(frame, loading);
          // The editor's fields are under the keyboard switch like the page's.
          watchFrame(frame);
          onStatus('live');
          revealIDE(frame, loading, current);
          return;
        }
      } catch { /* code-server may still be starting */ }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }
    if (current()) throw new Error('VS Code did not become ready. Check the session log or retry.');
  } catch (err) {
    if (!current()) return;
    // A mark that turns is a promise that something is still happening, and
    // nothing is.
    spinner.remove();
    words.textContent = errorMessage(err);
    loading.append(el('button', { class: 'btn', text: 'Retry', onclick: () => attach(sessionOf(session.id) || session) }));
  }
}

// How long the card waits for a workbench that never arrives. Past this the
// frame is uncovered whatever is in it: a page that is going to be white is
// better white than behind a mark that turns for ever.
const IDE_REVEAL_CEILING = 30000;
// The fade in app.css, plus a frame to start it in.
const IDE_FADE = 400;

/**
 * revealIDE takes the loading card away once there is an editor behind it.
 *
 * The frame is served from this origin, so the honest test is the workbench's
 * own root element: `load` fires when code-server's shell has been fetched,
 * which is several seconds before it has drawn anything. A frame that cannot
 * be read at all - which should not happen, and would mean the IDE route has
 * moved - falls back to `load` rather than waiting for a document it will
 * never see.
 */
function revealIDE(frame, loading, current) {
  const started = Date.now();
  const uncover = () => {
    if (!loading.isConnected) return;
    loading.classList.add('done');
    setTimeout(() => loading.remove(), IDE_FADE);
  };
  const look = () => {
    // A session that was stepped off takes its card with it.
    if (!current() || !loading.isConnected) return;
    if (Date.now() - started >= IDE_REVEAL_CEILING) { uncover(); return; }
    let ready = false;
    try {
      const doc = frame.contentDocument;
      ready = !!(doc && doc.querySelector('.monaco-workbench'));
    } catch {
      ready = frame.dataset.loaded === 'true';
    }
    if (ready) { uncover(); return; }
    setTimeout(look, 150);
  };
  frame.addEventListener('load', () => { frame.dataset.loaded = 'true'; }, { once: true });
  look();
}

// The key bar is asked for or it is not there: no device decides for itself
// whether it has a keyboard, because the two that matter - a tablet in a
// case, a laptop with a touch screen - are the two it would get wrong. The
// session menu is the one way in, and the answer is kept per device.

function showKeyBar(on) {
  dom.keybar.hidden = !on;
  // A modifier armed on a bar nobody can see would transform the next key
  // typed with nothing on screen to say why.
  if (!on && state.keybar) state.keybar.clear();
  if (state.term) state.term.refit();
  // The rows the bar gave back belong to the pane, and so does the focus:
  // with no bar on screen the terminal is the only thing there is to type
  // into.
  if (!on) keepTypingAlive();
}

// renameRow takes the new name of a session into the list and, when it is the
// one on screen, into the header. No animation: a row quietly having a better
// name is not an event.
function renameRow(id, title) {
  if (!id || !title) return;
  const session = sessionOf(id);
  if (!session || session.title === title) return;
  session.title = title;
  if (state.current && state.current.id === id) {
    state.current.title = title;
    dom.sessionTitle.textContent = title;
  }
  renderList();
}

// titleFrame follows the automatic naming of a session: the name pulses from
// the moment the model is asked until it has answered, and an answer that is
// a failure is said in a dialog - a name that silently never arrives is a
// sidebar nobody can explain.
function titleFrame(frame) {
  const id = frame.id;
  if (!id) return;
  if (frame.phase === 'start') state.titling.add(id);
  else state.titling.delete(id);
  if (state.current && state.current.id === id) {
    setClass(dom.sessionTitle, 'titling', state.titling.has(id));
  }
  if (frame.title) renameRow(id, frame.title);
  else renderList();
  if (frame.phase === 'error') {
    const session = sessionOf(id);
    alertDialog({
      title: 'Could not name the session',
      body: (session ? '“' + session.title + '”: ' : '') + (frame.text || 'the title model failed.'),
    });
  }
}

/**
 * insertIntoTerm puts one line into the pane as if it had been typed.
 *
 * The frame carries the line as `text` rather than as loose bytes, which is
 * what decides its fate if the server turns out to have forgotten this
 * viewer: keystrokes are counted, a composed line is handed back in the words
 * it was written in. No Enter goes with it. A transcript is a guess about
 * what was said, and running a guess is the person's decision, not this
 * page's - so the words land on the prompt and wait there.
 */
function insertIntoTerm(text) {
  const line = String(text || '');
  if (!line) return;
  if (!state.socket) {
    toast('There is no session to type into.', 'error');
    return;
  }
  state.socket.sendInput(line, { text: line });
  // The cursor goes back where the words did, so the Enter that runs them is
  // the next key pressed rather than a tap on the pane first.
  if (state.term) state.term.focus();
}

function onControl(sessionId, frame) {
  // A session that names itself does it while another one is being watched,
  // so this frame is about whichever session it names and not about the one
  // this socket is attached to.
  if (frame.t === 'title') { titleFrame(frame); return; }
  if (!state.current || state.current.id !== sessionId) return;
  switch (frame.t) {
    case 'hello':
      // The handshake is where a relaunched program has just come up, so the
      // settling window starts again from here. A plain reconnect is not a
      // start, and a turn that finished while it was away is still news.
      if (notifier && state.resuming) notifier.hush(sessionId);
      if (frame.session) applySession({ ...state.current, ...frame.session });
      // A handshake carries every running session's activity, so a reconnect
      // and a reload both re-draw the sidebar without asking for anything.
      mergeActivity(frame.activity);
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
    case 'status':
      if (state.assist) state.assist.statusFrame(frame);
      break;
    case 'state':
      if (notifier && frame.state !== state.current.state) notifier.hush(sessionId);
      // The program coming up or going down is a status change, so the row
      // moves - the same moment the server wrote into it.
      replaceSession({ ...state.current, state: frame.state,
        active_at: frame.state === state.current.state ? state.current.active_at : Date.now() });
      break;
    case 'exit':
      replaceSession({ ...state.current, state: 'exited', exit_status: frame.status,
        active_at: Number(frame.at) || Date.now() });
      break;
    case 'size':
      if (frame.by === 'other') {
        notice('resized', 'Another viewer resized this session to ' + frame.cols + '×' + frame.rows + '.');
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
      // A composed line is given back in full rather than counted: it was
      // spoken once, and a person who is not looking at the screen has no
      // other way to find out that it never arrived.
      for (const line of frame.lines || []) {
        toast('That was not typed into the terminal: “' + line + '”', 'error');
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
  // Already open - unless it is a prepared session showing something other
  // than its panel, which a click is the way back from.
  const panelShown = state.prestart && state.prestart.shownId() === id;
  if (state.current && state.current.id === id && (panelShown
    || (!isPrepared(session) && (state.socket || document.body.classList.contains('ide-open'))))) return;
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
// attach will leave it in - the key bar up only where this device asked for
// one - so that what is measured is the pane the session will get.
function measureNewPane() {
  const keybarWas = dom.keybar.hidden;
  // The real bar, because its height is its buttons. Nothing is wired to it:
  // it exists for the length of one measurement.
  const bar = mountKeyBar(dom.keybar, null, null);
  dom.keybar.hidden = !keyBarWanted();
  const size = measurePane(dom.term, { fontSize: state.terminal.font_size, webgl: state.terminal.webgl });
  bar.dispose();
  dom.keybar.hidden = keybarWas;
  return size;
}

// measureForStart is measureNewPane from wherever the page is: a prepared
// session's panel stands where the pane would be, so the pane is put back
// for the length of the measurement - one synchronous step, never painted.
function measureForStart() {
  if (state.term) return state.term.size();
  const panelWas = dom.prestart.hidden;
  const wrapWas = dom.termWrap.hidden;
  dom.prestart.hidden = true;
  dom.termWrap.hidden = false;
  try {
    return measureNewPane();
  } finally {
    dom.prestart.hidden = panelWas;
    dom.termWrap.hidden = wrapWas;
  }
}

// prepareSession stores a session with its settings and nothing else, and
// opens its panel, where the first prompt is written and Start is pressed.
// The size is sent along because a scheduled start has no page to measure
// one: it starts at the size the session was prepared at.
async function prepareSession(pick) {
  const size = measureForStart();
  try {
    const data = await api('/api/sessions', {
      method: 'POST',
      attempts: 2,
      timeout: 30000,
      body: {
        client_id: clientKey(),
        prestart: true,
        harness: pick.harness,
        model: pick.model,
        effort: pick.effort,
        profile: pick.profile || undefined,
        profile_auto: pick.profile_auto || undefined,
        ultracode: pick.ultracode || undefined,
        workdir_mode: pick.workdir_mode,
        workdir: pick.workdir,
        cols: size.cols || undefined,
        rows: size.rows || undefined,
      },
    });
    replaceSession(data.session);
    state.wanted = data.session.id;
    location.hash = '#' + data.session.id;
    attach(data.session);
    // The prompt is the next thing to do, so the cursor is put there. On a
    // touch screen the field stays muted until the keyboard is asked for.
    const field = state.prestart && state.prestart.field();
    if (field) field.focus({ preventScroll: true });
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function newSession() {
  const pick = await harnesses.openNewSessionSheet();
  if (!pick) return;
  // On a phone the drawer is what "New session" was tapped in, and the thing
  // being started is behind it. Leaving it open puts a list over the terminal
  // that was just asked for.
  closeNav();
  if (pick.prestart) {
    await prepareSession(pick);
    return;
  }
  const size = measureForStart();
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
        shell_execution: pick.shell_execution,
        profile: pick.profile || undefined,
        ultracode: pick.ultracode || undefined,
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

/* ------------------------------------------------- the sidebar as a rail */

// Whether this device keeps the sidebar as a rail. It is a fact about this
// screen and not about the account: the same person wants the column on a
// desk and the width on a laptop, and a preference that travelled between them
// would be wrong on one of them every time.
const SIDE_KEY = 'socrates.sidebar';
// The width transition in app.css, plus a frame. The pane is measured in
// characters, so it has to be re-measured once the stage has stopped moving -
// too early and it fits to a width the sidebar is still in the middle of
// leaving.
const SIDE_MS = 200;

// The drawer breakpoint from app.css. Below it the sidebar is a drawer, the
// collapse control is not on the page, and the rail must never be entered:
// a drawer that is also a rail is a drawer with nothing in it to read.
const railAllowed = () => window.matchMedia('(min-width: 901px)').matches;

function sidebarCollapsed() { return document.body.classList.contains('side-collapsed'); }

function showSidebar(collapsed, remember = true) {
  const want = collapsed && railAllowed();
  setClass(document.body, 'side-collapsed', want);
  if (dom.sideCollapse) {
    dom.sideCollapse.setAttribute('aria-expanded', want ? 'false' : 'true');
    const words = want ? 'Expand the sidebar' : 'Collapse the sidebar';
    dom.sideCollapse.title = words;
    dom.sideCollapse.setAttribute('aria-label', words);
  }
  // What a row shows depends on which shape it is in, so the list is drawn
  // again rather than patched from the outside.
  renderList();
  if (remember) {
    try { localStorage.setItem(SIDE_KEY, want ? 'rail' : 'wide'); } catch { /* private window */ }
  }
  setTimeout(() => { if (state.term) state.term.refit(); }, SIDE_MS);
}

function restoreSidebar() {
  let saved = '';
  try { saved = localStorage.getItem(SIDE_KEY) || ''; } catch { /* private window */ }
  // The state is applied without writing it back: a phone that is too narrow
  // for the rail must not overwrite what the desk it syncs nothing with chose.
  showSidebar(saved === 'rail', false);
  // A window that crosses the drawer breakpoint leaves the rail behind, and
  // takes it up again on the way back if that is what this device asked for.
  window.matchMedia('(min-width: 901px)').addEventListener('change', () => {
    let want = '';
    try { want = localStorage.getItem(SIDE_KEY) || ''; } catch { /* private window */ }
    showSidebar(want === 'rail', false);
  });
}

/* ----------------------------------------------------------------- start */

function wire() {
  dom.newSession.addEventListener('click', newSession);
  dom.menuBtn.addEventListener('click', () => {
    if (document.body.classList.contains('nav-open')) closeNav(); else openNav();
  });
  dom.navScrim.addEventListener('click', closeNav);
  if (dom.sideCollapse) {
    dom.sideCollapse.addEventListener('click', () => showSidebar(!sidebarCollapsed()));
  }
  dom.sessionMenu.addEventListener('click', (event) => {
    if (state.current) openRowMenu(event.currentTarget, state.current.id);
  });
  // The name is edited where it is read.
  dom.sessionTitle.addEventListener('click', () => {
    if (state.current) renameSession(state.current);
  });
  // The on-screen keyboard, which on a touch screen this button raises and
  // puts away and nothing else does. With no field focused, it is raised for
  // the pane.
  // A prepared session has no pane; its prompt is the field there is.
  initKeyboard(dom.kbBtn, () => {
    if (state.term) return state.term.term.textarea;
    return state.prestart ? state.prestart.field() : null;
  });
  dom.logout.addEventListener('click', async () => {
    if (notifier) await notifier.signOut();
    try { await api('/api/logout', { method: 'POST' }); } catch { /* going anyway */ }
    location.href = '/login';
  });
  window.addEventListener('hashchange', () => {
    const id = location.hash.replace(/^#/, '');
    if (id) state.wanted = id;
    if (id && (!state.current || state.current.id !== id)) selectSession(id);
  });
  // Anything that ends with the focus nowhere - a dialog closing, a menu
  // item, a notice being dismissed - hands it back to the pane on the next
  // tick, once the browser has settled on where it went.
  // Where a dialog puts the focus when it has nowhere to put it back.
  setFocusFallback(keepTypingAlive);
  document.addEventListener('focusout', () => setTimeout(keepTypingAlive, 0));
  // Not only on an event: an element that is removed while it has the focus
  // takes the focus with it and raises nothing at all - which is exactly what
  // a dialog does on its way out. So the same rule is also checked on the
  // second, which is the difference between a page that cannot be typed into
  // and one that can.
  setInterval(keepTypingAlive, 1000);
  // And a tap anywhere on the pane means the pane, even when it lands on an
  // overlay, a notice or the padding around the terminal.
  dom.termWrap.addEventListener('pointerup', () => {
    if (!state.term) return;
    const active = document.activeElement;
    if (active && active.closest && active.closest('input, textarea, button, dialog, .menu')
      && !active.classList.contains('xterm-helper-textarea')) return;
    state.term.focus();
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
  await loadTerminalFonts();
  wire();
  // Which folders this device keeps shut, before the first list is drawn.
  restoreFolders();
  // Before anything is measured: the rail is a different stage width, and a
  // terminal fitted to the wide one and then narrowed is a terminal that
  // reflows in front of the person.
  restoreSidebar();
  // Status and the microphone, mounted before anything is measured so that
  // the bar is its final height before the pane is fitted into what is left.
  state.assist = mountAssist({
    dom,
    notice,
    // A prepared session has nothing to summarize and no terminal to speak
    // into; its prompt field carries a microphone of its own.
    current: () => (isPrepared(state.current) ? null : state.current),
    live: () => state.live,
    insert: (text) => insertIntoTerm(text),
  });
  // Copy, paste and upload, for whichever terminal is on screen.
  state.transfer = mountTransfer({
    dom,
    current: () => state.current,
    term: () => (state.term ? state.term.term : null),
    send: (data) => { if (state.socket) state.socket.sendInput(data); },
    focus: () => { if (state.term) state.term.focus(); },
  });
  // The chime and the notification. Mounted here rather than in mountAssist
  // because they are not about the session on screen: they fire for whichever
  // session finished, and their two switches are in the bar whether or not
  // anything is attached.
  notifier = mountNotify({
    dom,
    sessionOf,
    select: (id) => selectSession(id),
  });
  // The panel of a prepared session. Its Start ends where a new session's
  // does: the row it answers with replaces the prepared one, and
  // applySession turns the panel into that session's terminal.
  state.prestart = mountPrestart({
    host: dom.prestart,
    replace: (row) => replaceSession(row),
    started: (row, error) => {
      replaceSession(row);
      if (error) toast('The session could not start.', 'error');
    },
    refresh: () => refreshList(),
    measure: () => measureForStart(),
  });
  followViewport();
  // Whether the bucket is taking what is done here. Silent while it is.
  mountStorageBanner(document.getElementById('storageBanner'));
  // Read before anything is fetched: on an offline reload this is the only
  // thing the page knows about what it was doing.
  state.wanted = location.hash.replace(/^#/, '');
  state.loading = true;
  renderList();
  // The catalogue decides whether a folder offers VS Code, so the list is
  // drawn again once it is in.
  harnesses.load().then(() => renderList()).catch(() => { /* the sheet says so when it is opened */ });
  try {
    const prefs = await api('/api/preferences', { attempts: 1, timeout: 8000 });
    if (prefs && prefs.terminal) state.terminal = { ...state.terminal, ...prefs.terminal };
  } catch { /* the defaults above are the shipped ones */ }
  await refreshList();
  // A tab with no session in its URL opens the newest one. From here on
  // `openWanted` is the only thing that attaches on its own, so there is one
  // rule about when that is allowed and one place it lives.
  if (!state.wanted || !sessionOf(state.wanted)) {
    const first = state.sessions.find((s) => !isDone(s)) || state.sessions[0];
    state.wanted = first ? first.id : state.wanted;
  }
  openWanted();
  if (!state.current && !state.socket && !state.pendingAttach) showEmpty();
}

boot();
