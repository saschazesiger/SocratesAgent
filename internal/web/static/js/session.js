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
  api, el, toast, infoTip, confirmDialog, alertDialog, promptDialog, errorMessage, isOffline, isBusyConflict,
  setClass, onWake, clientKey, CONNECTION_GRACE, restoreFocus, setFocusFallback, toastsStay, onToastsStay,
} from './api.js';
import { connectionSource, deadline } from './net.js';
import { agentMark, agentMarkHtml } from './logos.js';
import {
  MORE_KEY, FINISHED_KEY, FINISHED_LABEL, SHELVES, IDE, foldersOf, folderKeyOf, ideOf, momentOf, movesRow, notStarted,
} from './folders.js';
import * as harnesses from './harnesses.js';
import * as accounts from './accounts.js';
import { createTerm, measurePane, loadTerminalFonts, terminalFontsSettled } from './term.js';
import { mountAssist } from './assist.js';
import { mountNotify } from './notify.js';
import { mountTransfer } from './transfer.js';
import { mountKeyBar, keyBarWanted, setKeyBarWanted, followViewport } from './keybar.js';
import { forgetChat, forgetAll } from './chatcache.js';
import {
  initKeyboard, guard, watchFrame, dictated, touchScreen, setTouchMode, showKeyboardSwitch, typeHere,
} from './keyboard.js';
import { mountEvents } from './events.js';
import { forgetDraft } from './composer.js';
import * as mcpView from './mcpview.js';
import { mountHome, questionQueue, connectionLine } from './home.js';
import {
  OUTCOME_WORDS, CHECKING_WORDS, outcomeCurrent, outcomeMark, outcomeLine, outcomeIconHtml,
} from './outcome.js';
import {
  DS, icon, html, raw, morph, flipMorph, showOverlay, hideOverlay, overlayOpen, M,
  faces, iconEl,
} from './ui.js';
import { mountBackupNotice } from './backup.js';
import { setVoiceKey, voiceKey, onVoiceKey, keyMissing, setTtsKey } from './voice.js';
import { explainNoKey } from './dictate.js';
import { mountChat } from './chatview.js';
import { chatOutbox, queueCommand, settled, forgetAllSent } from './chatoutbox.js';
import { openChatAsks, askDialogOpen, closeAskDialog, refreshAskDialog } from './askdialog.js';

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
    if (this.pingDeadline) { this.pingDeadline.clear(); this.pingDeadline = null; }
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
        if (this.pingDeadline) { this.pingDeadline.clear(); this.pingDeadline = null; }
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
      if (this.pingDeadline) this.pingDeadline.clear();
      // A deadline (net.js): a pong that came while the page was paused is
      // read before the deadline counts it as missed.
      this.pingDeadline = deadline(PING_TIMEOUT, () => {
        // Silence is not failure on a terminal - a pane is legitimately quiet
        // for hours - but a missed pong is.
        this.pingDeadline = null;
        this.report('connecting', { immediate: true });
        this.reconnect(0);
      });
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
  // The store revision of the list on screen. A list read over HTTP that is
  // older than what the stream has already drawn is dropped rather than
  // drawn over it: see applyList.
  listRev: null,
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
  // The sessions this tab is deleting or marking done right now. The server
  // closes their terminals as part of the request and tells every viewer so
  // in a fatal frame, which reaches this tab before the answer to its own
  // request does; that news is for the other devices watching, not for the
  // person who asked, whose own toast says what happened.
  ending: new Set(),
  // Of those, the ones this page asked for itself (retitleSession), which
  // its own request settles; the others are only settled by a title frame,
  // and a frame can be lost (see forgetTitling).
  namingHere: new Set(),
  // The number of the newest title frame drawn. Every title frame comes
  // twice - on the terminal socket and on the event stream - and the second
  // is the same news, not a second failure to open a second dialog for.
  titleSeq: 0,
  // The seq of the start frame of each naming in progress, so the ceiling of
  // one naming does not end the next one of the same session.
  titleStarts: new Map(),
  // Status and the microphone, mounted once in boot().
  assist: null,
  // Copy, paste and upload, mounted once in boot().
  transfer: null,
  // How the terminal is drawn, from the dashboard. The defaults here are what
  // a page that could not ask uses, and they are the same numbers the settings
  // document ships with.
  terminal: { scrollback: 2000, font_size: 14, webgl: true },
  // The chat view, which stands where the pane does for a session that is a
  // chat (chatview.js), mounted once in boot().
  chat: null,
  // Home, the page the stage shows when no session is open, and the event
  // stream that keeps it - and the sidebar - live whatever is on the stage.
  // Both mounted once in boot().
  home: null,
  events: null,
  // Each chat's reading (HOME.md §1.3): what it is doing and what it asks,
  // keyed by session id. In memory only, like the activity: the server
  // makes it from the chat's journal, and a hello carries it again.
  digests: new Map(),
  // The verdict on each session's last turn (outcome.js), keyed by id: the
  // same kind of thing, re-derived by the server and carried by every hello.
  outcomes: new Map(),
  // Whether a hello or a poll has handed over the digests since this page
  // loaded. Until one has, a session the page holds no digest for has not
  // been read for this page, rather than read and found asking nothing.
  digestsHeard: false,
  // The app's badge may say something other than what the page last told it
  // (tellAsking): the page was away, and a push wrote it meanwhile, or a push
  // has just written it. It is written again from the first state the page
  // holds that is the server's as of after staleSince (badgeWrittenElsewhere).
  badgeStale: true,
  staleSince: 0,
  // `#questions` - the app's "Questions" shortcut - asked for the dialog at
  // the first question, and the page does not yet know whether there is one
  // (questionsWhenKnown).
  questionsWanted: false,
  // A question asked for from outside the page that could not be shown yet
  // (questionWhenKnown): {id, since, tries}, or null.
  questionWanted: null,
};

const dom = {};
const ids = ['sidebar', 'navScrim', 'menuBtn', 'blankIDE', 'homeBtn', 'sideCollapse', 'newSession', 'sessionList',
  'activityLive', 'sessionTitle', 'titleRegen', 'sessionStatus', 'sessionOutcome',
  'transfer', 'copyBtn', 'pasteBtn', 'uploadBtn',
  'statusBtn', 'kbBtn', 'optionsBtn',
  'stage', 'termWrap', 'term', 'termOverlay', 'termLines', 'termNotice',
  'termTicker', 'tickerWindow',
  'micBtn',
  'recSheet', 'recMeter', 'recTime', 'recSend', 'recCancel',
  'keybar', 'logout', 'chat', 'home'];
for (const id of ids) dom[id] = document.getElementById(id);
// The frame's own parts: the app, the bar's contents, the row menu and the
// options dialog of the bar.
for (const id of ['app', 'topbar', 'sessionTitleText', 'sessionMenu', 'optionsDialog']) dom[id] = document.getElementById(id);

const STATE_WORDS = {
  running: 'Running', starting: 'Starting', resuming: 'Resuming',
  needs_resume: 'Not running', exited: 'Ended', failed: 'Failed',
};


// What a session is doing, in the words the "i" uses. `unknown` has no word:
// a row with no evidence yet says nothing rather than guessing.
// A session waiting for an answer, where a glyph says it (a shut folder's
// head): the Status modal's "Waiting for you" glyph.
const WAITING_GLYPH = 'chat';

const ACTIVITY_WORDS = {
  busy: 'Working',
  idle: 'Waiting for an instruction',
  waiting: 'Needs an answer',
  tasks: 'Turn finished · a background task is still running',
};

// The hourglass a row's mark carries while the agent's turn is over and
// something it started - a background shell, a monitor, a sub-agent - is
// still running: not working, and not done either. The same hourglass says
// that a finished turn's verdict is being read (outcome.js).
const TASKS_SVG = icon('waiting', 16);

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

/**
 * outcomeOf is the verdict on the turn a session is resting after, or null:
 * one made for another turn is not drawn (outcomeCurrent), and a session that
 * is not running has no turn to rest after.
 */
function outcomeOf(session) {
  if (!session || session.state !== 'running') return null;
  return outcomeCurrent(state.outcomes.get(session.id), activityOf(session));
}

// checking is whether a session's turn has ended and its verdict is being
// read: the hourglass, as for background work, and the notification waits.
function checking(session) {
  const outcome = outcomeOf(session);
  return !!outcome && outcome.phase === 'pending';
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

/**
 * isChat says whether a session is a chat: an agent driven through its
 * protocol and drawn by the chat view, not a terminal.
 */
function isChat(session) { return !!session && session.kind === 'chat'; }

// chatShown says whether the chat view is what the stage shows.
function chatShown() { return !!state.chat && !!state.chat.shownId(); }

/* -------------------------------------------------------- the session list */

// renderList draws everything the list and the activity are shown in: the
// sidebar, and Home and the question dialog when they are on screen. They are
// drawn from the same state at the same moments, so a number on Home can
// never be ahead of, or behind, the row it counts.
function renderList() {
  releaseHolds();
  drawList();
  drawLive();
  drawOutcomeTag();
}

// drawOutcomeTag is the verdict on the open session's last turn, as a tag
// beside where it is filed: "Checking…" while it is read, then its word. A
// tap says why, and what is still open.
function drawOutcomeTag() {
  const tag = dom.sessionOutcome;
  if (!tag) return;
  const session = state.current ? sessionOf(state.current.id) || state.current : null;
  const outcome = session ? outcomeOf(session) : null;
  const kind = outcomeMark(outcome);
  const pending = !!outcome && outcome.phase === 'pending';
  tag.hidden = !pending && !kind;
  if (tag.hidden) return;
  const key = (pending ? 'pending' : kind) + '|' + (outcome.reason || '') + '|' + (outcome.error || '');
  if (tag.dataset.key === key) return;
  tag.dataset.key = key;
  tag.dataset.kind = pending ? 'checking' : kind;
  const words = pending ? 'Checking…' : (kind === 'error' ? 'Not checked' : OUTCOME_WORDS[kind]);
  morph(tag, html`${raw(pending ? TASKS_SVG : outcomeIconHtml(kind, 16))}<span>${words}</span>`);
  // Its glyph alone on a phone (so-frame.css), so the words are its name.
  tag.setAttribute('aria-label', words);
  tag.setAttribute('data-tip', outcomeLine(outcome));
}

// showOutcome is a tap on the tag: the verdict in full.
function showOutcome() {
  const session = state.current ? sessionOf(state.current.id) || state.current : null;
  const outcome = session ? outcomeOf(session) : null;
  if (!outcome || outcome.phase === 'pending') return;
  const todos = Array.isArray(outcome.todos) ? outcome.todos : [];
  const body = outcome.phase === 'error'
    ? (outcome.error || 'The outcome could not be checked.')
    : [outcome.reason, todos.length ? 'Still open: ' + todos.join(' · ') : ''].filter(Boolean).join(' ');
  alertDialog({ title: outcome.phase === 'error' ? 'Outcome not checked' : OUTCOME_WORDS[outcome.kind], body });
}

function drawLive() {
  if (state.home) state.home.render();
  tellAsking();
  questionsWhenKnown();
  questionWhenKnown();
  refreshAsking();
}

// refreshAsking takes what the chats ask now into the question dialog while
// it is open - a question asked meanwhile joins the walk, and a chat renamed
// is called by its new name - once each time that changes.
let askingSeen = '';
function refreshAsking() {
  if (!askDialogOpen()) { askingSeen = ''; return; }
  const asking = questionQueue(homeCtx()).filter((session) => isChat(session));
  const seen = asking.map((session) => session.id + '\u0000' + session.title + '\u0000' + ((activityOf(session) || {}).since || '')).join('\n');
  if (seen === askingSeen) return;
  askingSeen = seen;
  refreshAskDialog({ sessions: asking, view: state.chat }).catch(() => { /* the walk goes on as it was */ });
}

function drawList() {
  // The empty editor goes where VS Code is switched off, like a folder's mark.
  dom.blankIDE.hidden = !harnesses.ideAvailable();
  const host = dom.sessionList;
  const wanted = state.sessions;
  if (state.chat) state.chat.sessionsChanged(wanted);
  if (!wanted.length) {
    let empty = 'No sessions yet.';
    if (state.loading) empty = 'Loading…';
    else if (!state.reachable) empty = 'Can’t reach Socrates.';
    morph(host, html`<p class="so-list-empty" data-ds-key="empty">${empty}</p>`);
    return;
  }
  // Which project each session belongs to, and in what order, is
  // folders.js's answer: the server hands the list over newest first, and a
  // frame that changes a status between two fetches bumps the moment locally,
  // so the order inside a folder - and of the folders - is worked out there.
  // Chats not started yet, background and done sessions are still their
  // project's: each folder keeps them on a shelf of its own at its bottom.
  // A folder's VS Code is not a row at all: it is drawn on the folder's head.
  //
  // The list is morphed, never rebuilt: a kept row keeps its element, so its
  // turning ring does not restart every time the list is drawn, and a row that
  // moves to the top of its folder glides there. It is live data, so while
  // the pointer is over the list a reorder waits for it to leave: a row never
  // jumps out from under a finger that is about to tap it.
  //
  // A project with nothing left to do in it goes into the Finished fold at
  // the bottom, shut, so the projects being worked on are all there is.
  const all = foldersOf(wanted, placeOf);
  const finished = all.filter((folder) => folder.finished);
  const markup = all.filter((folder) => !folder.finished).map((folder) => folderHtml(folder));
  if (finished.length) {
    markup.push(groupHtml({ key: FINISHED_KEY, label: FINISHED_LABEL, path: '', sessions: [], folders: finished },
      finished.map((folder) => folderHtml(folder))));
  }
  flipMorph(host, html`${markup}`, { items: '.so-row, .so-folder__bar', live: true });
}

// placeOf is the shelf a session is on, or '' for its folder's rows: what
// is filed in the background or done by its status, else a chat nobody has
// sent anything yet.
function placeOf(session) {
  const status = statusOf(session);
  if (status === 'background' || status === 'done') return status;
  return notStarted(session, activityOf(session)) ? 'unstarted' : '';
}

// folderHtml is one project: the shelf of chats not started yet at its top,
// where what is about to be begun is found first, then its rows, then the
// shelves of background and done sessions - all inside the same folder, so
// that shutting it puts them away with everything else.
function folderHtml(folder) {
  const shelf = (status) => {
    const filed = folder.shelves[status];
    return filed.length
      ? groupHtml({ key: shelfKey(folder.key, status), status, label: SHELF_WORDS[status], path: '', sessions: filed }, filed.map(rowHtml))
      : '';
  };
  return groupHtml(folder, [
    shelf('unstarted'),
    folder.sessions.map(rowHtml),
    SHELVES.filter((status) => status !== 'unstarted').map(shelf),
  ]);
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
// about the account, for the same reason the hidden list is: a column tidied
// on a desk is not a column tidied on a phone.
const FOLDERS_KEY = 'socrates.folders';
const closedFolders = new Set();

// The shelves inside each folder: chats not started yet, background and done
// sessions below its rows. Not started is open, like a folder: a draft is
// about to be work. The other two start shut - a service is meant to run
// unseen, and what is done is finished - so for them the set records the
// opposite: being in it means the person opened it. Done is shut again at
// every start of the page: an opened one is not written down, and so is the
// Finished fold at the bottom of the list. A shelf's key
// is its folder's and its status, joined by a character no path and no
// status can hold.
function shelfKey(folderKey, status) { return folderKey + '\u0000' + status; }
function shelfOf(key) {
  const at = key.lastIndexOf('\u0000');
  return at < 0 ? '' : key.slice(at + 1);
}
const SHELF_WORDS = { unstarted: 'Not started', background: 'Background', done: 'Done' };
const FINISHED_TITLE = 'Finished: projects with nothing left to do, only done sessions';
const SHELF_TITLES = {
  unstarted: 'Not started: chats nobody has sent anything yet',
  background: 'Background sessions: services left running on purpose, never deleted',
  done: 'Done sessions: their terminals are closed, and each is deleted a day after it was marked done',
};

function shutByDefault(key) {
  if (key === FINISHED_KEY) return true;
  const shelf = shelfOf(key);
  return shelf === 'background' || shelf === 'done';
}

function folderOpen(key) {
  return shutByDefault(key) ? closedFolders.has(key) : !closedFolders.has(key);
}

// aria-controls needs something to point at, and a path is not an id. The id
// is handed out once per key, so it is the same in every drawing of the list.
// The head names its folder by that id too: a shelf's key holds a NUL, which
// an attribute cannot carry (the parser turns it into U+FFFD).
const rowsIds = new Map();
const keysOfRows = new Map();
function rowsIdOf(key) {
  if (!rowsIds.has(key)) {
    const id = 'folder-rows-' + (rowsIds.size + 1);
    rowsIds.set(key, id);
    keysOfRows.set(id, key);
  }
  return rowsIds.get(key);
}

function restoreFolders() {
  let raw = '';
  try { raw = localStorage.getItem(FOLDERS_KEY) || ''; } catch { /* private window */ }
  if (!raw) return;
  try {
    const shut = JSON.parse(raw);
    if (!Array.isArray(shut)) return;
    for (const key of shut) if (typeof key === 'string' && remembered(key)) closedFolders.add(key);
  } catch { /* a value from a version that wrote something else */ }
}

// remembered says whether a folder's state outlives the page: done and the
// Finished fold are shut again at every start.
function remembered(key) { return key !== FINISHED_KEY && shelfOf(key) !== 'done'; }

function saveFolders() {
  const kept = [...closedFolders].filter(remembered);
  try { localStorage.setItem(FOLDERS_KEY, JSON.stringify(kept)); } catch { /* private window */ }
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
 * session needs its shelf opened as well as its folder, and one in a
 * finished folder the Finished fold too; an editor is on the folder's head,
 * which is on screen whenever the folder is.
 */
function openFolder(session) {
  if (!session) return;
  const key = folderKeyOf(session);
  let changed = closedFolders.delete(key);
  const folder = foldersOf(state.sessions, placeOf).find((one) => one.key === key);
  if (folder && folder.finished && !closedFolders.has(FINISHED_KEY)) { closedFolders.add(FINISHED_KEY); changed = true; }
  const place = placeOf(session);
  if (place && !(session.harness === IDE && ideOf(state.sessions, key) === session)) {
    const shelf = shelfKey(key, place);
    if (!shutByDefault(shelf)) changed = closedFolders.delete(shelf) || changed;
    else if (!closedFolders.has(shelf)) { closedFolders.add(shelf); changed = true; }
  }
  if (!changed) return;
  saveFolders();
  renderList();
}

/**
 * groupHtml is a folder, or a shelf - the same shape one level down, inside
 * its folder's rows: a head that shuts it, and the rows. A project folder's
 * head carries one more control beside the name: the mark of VS Code, which
 * opens the folder's editor. It is a button beside the head rather than
 * inside it, because a button cannot hold another.
 */
function groupHtml(folder, children) {
  const open = folderOpen(folder.key);
  const shelf = !!folder.status;
  const fold = folder.key === FINISHED_KEY;
  const id = rowsIdOf(folder.key);
  // Shut, a folder has to say what is behind it: the two things this page
  // exists to make noticeable - an agent that needs an answer, and one that
  // finished something nobody has seen - would otherwise be hidden by the
  // person's own tidying. So it carries a count, a quiet dot while one is
  // working or the question's glyph in the ring colour of a session waiting
  // (never the colour alone, and said in words to a screen reader), and the
  // weight of one nobody has read. Open, the rows say all three themselves
  // and the head says none of them.
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
    if (act.state === 'busy' || act.state === 'tasks' || checking(session)) busy = true;
  }
  let count = folder.sessions.length;
  if (folder.shelves) for (const status of SHELVES) count += folder.shelves[status].length;
  // The Finished fold counts the projects in it, which is what it holds.
  if (fold) count = folder.folders.length;
  const attention = waiting ? 'waiting' : busy ? 'busy' : '';
  // The whole path is technical detail, so it is where every technical string
  // on this page is: in a tooltip, and nowhere in the words. "More Chats" and
  // the shelves have no path, so their tooltip says what they hold instead.
  const tip = fold ? FINISHED_TITLE : SHELF_TITLES[folder.status] || folder.path || 'Sessions in a directory of their own';
  const ide = shelf || fold || folder.key === MORE_KEY ? '' : html`${newButtonHtml(folder)}${ideButtonHtml(folder)}`;
  const kind = fold ? html`data-finished` : shelf ? html`data-shelf="${folder.status}"` : html`data-folder="${folder.key}"`;
  // A project with nothing in it but done sessions steps back as they do,
  // and so does the fold they are put away in.
  const done = fold || (!shelf && !!folder.shelves && count === folder.shelves.done.length);
  const className = fold ? 'so-folder so-folder--finished' : shelf ? 'so-folder so-folder--shelf' : 'so-folder';
  const dsKey = fold ? 'finished' : (shelf ? 'shelf:' : 'folder:') + folder.key;
  return html`<section class="${className}" data-ds-key="${dsKey}" ${kind}${open ? html` data-open` : ''}${done ? html` data-done` : ''}><div class="so-folder__bar"><button class="so-folder__head ds-pressable ds-pressable--lg ds-pressable--inset" type="button" aria-expanded="${open ? 'true' : 'false'}" aria-controls="${id}" data-toggle="${id}" data-tip="${tip}"${attention ? html` data-attention="${attention}"` : ''}${unread ? html` data-unread` : ''}>${raw(icon('chevronDown', 16, 'ds-chevron'))}<span class="so-folder__name">${folder.label}</span><span class="so-folder__dot" aria-hidden="true">${attention === 'waiting' ? raw(icon(WAITING_GLYPH, 16)) : ''}</span><span class="so-folder__count">${count}</span>${
    !open && attention ? html`<span class="ds-sr">${', ' + (attention === 'waiting' ? ACTIVITY_WORDS.waiting : ACTIVITY_WORDS.busy).toLowerCase()}</span>` : ''
  }</button>${ide}</div><div class="so-folder__rows" id="${id}"${open ? '' : html` hidden`}>${children}</div></section>`;
}

// newButtonHtml is a folder's +: the new-session sheet, opened on the
// folder. A folder the server would not start a session in (neither a preset
// nor a path the dashboard lets be typed) has none.
function newButtonHtml(folder) {
  const words = 'New session in ' + folder.label;
  return html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-folder__new" type="button" data-new="${folder.key}" aria-label="${words}" data-tip="${words}"${harnesses.folderChoice(folder.key) ? '' : html` hidden`}>${raw(icon('plus', 16))}</button>`;
}

// ideButtonHtml is the VS Code mark of a folder: in its colours while the
// folder's editor is running, quiet while there is none or it is stopped, and
// pressed while it is the one on screen. Where VS Code is switched off and
// the folder has no editor to return to, the mark is not there.
function ideButtonHtml(folder) {
  const ide = folder.ide;
  const usable = harnesses.ideAvailable();
  const running = !!ide && (ide.state === 'running' || ide.state === 'starting');
  const shown = !!ide && !!state.current && state.current.id === ide.id;
  const words = (running ? 'VS Code is open in ' : 'Open VS Code in ') + folder.label;
  return html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-folder__ide" type="button" data-ide="${folder.key}" aria-pressed="${shown ? 'true' : 'false'}" aria-label="${words}" data-tip="${words}"${running ? html` data-running` : ''}${!ide && !usable ? html` hidden` : ''}>${raw(agentMarkHtml(IDE, 16))}</button>`;
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

// Whether the empty editor is being asked for right now, so that a second
// tap waits for the first rather than opening a second tab.
let openingBlankIDE = false;

/**
 * openBlankIDE is the VS Code mark at the top of the drawer: an editor with
 * no folder open, in a new tab. There is one, shared by every tab it is
 * opened in; it is listed under "More Chats", where it can be stopped.
 */
async function openBlankIDE() {
  if (openingBlankIDE) return;
  openingBlankIDE = true;
  // The tab is opened inside the click. One opened once the editor answers
  // would be a pop-up, and browsers block those.
  const tab = window.open('', '_blank');
  if (tab) {
    tab.document.title = 'VS Code';
    tab.document.body.style.font = '15px system-ui, sans-serif';
    tab.document.body.textContent = 'Opening VS Code…';
  }
  try {
    const data = await api('/api/ide/blank', { method: 'POST', attempts: 2, timeout: 60000 });
    replaceSession(data.session);
    if (data.error) throw new Error(data.error);
    const url = (await readyIDE(data.session, () => true)) + ideQuery(data.session);
    // A tab closed while it waited was closed on purpose.
    if (tab) {
      if (!tab.closed) tab.location.replace(url);
    } else if (!window.open(url, '_blank')) {
      toast('Allow pop-ups to open VS Code in a new tab.', 'error');
    }
  } catch (err) {
    if (tab) tab.close();
    toast(errorMessage(err), 'error');
  } finally {
    openingBlankIDE = false;
  }
}

// The glyph of each status that has a mark: a flag for a review, a clock for
// pending, a server for background, a tick for done. In progress has none: it
// is what a session is unless somebody said otherwise.
const STATUS_GLYPH = { review: 'review', pending: 'clock', background: 'background', done: 'check' };

/**
 * rowHtml is one session in the list: its agent's mark, its name, and the ⋮
 * of what can be done to it. The row is a link to the session, so a middle
 * click opens it in a tab of its own; a plain click is the page's (wire).
 *
 * The activity of a row is two marks and no words: the agent's own mark wears
 * a ring that turns while the harness is working and stands still in the
 * warning colour while it is waiting for an answer, and a name that has
 * finished something nobody has seen is set in the label weight. The session
 * being looked at is never unread - it is open. A turn that is over while
 * something it started still runs has no ring - nothing is being worked on -
 * and a small hourglass on the mark's corner instead; the verdict on a turn
 * takes the same corner (outcome.js).
 */
function rowHtml(session) {
  const id = session.id;
  const attached = !!(state.current && state.current.id === id);
  // A chat that starts by itself at a time says when, on its row: the time
  // of day today, else the weekday or the date with it.
  const startAt = Number(session.start_at) || 0;
  const startWords = startAt ? 'Starts by itself ' + new Date(startAt).toLocaleString() : '';
  const status = statusOf(session);
  const said = status === 'done'
    ? [STATUS_WORDS.done, deleteWords(session)].filter(Boolean).join(' · ')
    : STATUS_WORDS[status];

  const act = activityOf(session);
  const live = session.state === 'running' || session.state === 'starting' || session.state === 'resuming';
  const busy = live && !!act && act.state === 'busy';
  const waiting = live && !!act && act.state === 'waiting';
  const tasks = live && !!act && act.state === 'tasks';
  const outcome = live ? outcomeOf(session) : null;
  const judging = !!outcome && outcome.phase === 'pending';
  const kind = outcomeMark(outcome);
  let word = STATE_WORDS[session.state] || session.state;
  if (busy) word = ACTIVITY_WORDS.busy;
  if (waiting) word = ACTIVITY_WORDS.waiting;
  if (tasks) word = ACTIVITY_WORDS.tasks;
  if (judging) word = CHECKING_WORDS;
  else if (kind) word = outcomeLine(outcome);
  const ring = busy ? 'busy' : waiting ? 'waiting' : '';
  // The corner: the verdict once it is in, else the hourglass of background
  // work or of a verdict being read.
  const badge = kind || (judging ? 'checking' : tasks ? 'tasks' : '');
  const badgeIcon = kind ? outcomeIconHtml(kind, 16) : badge ? TASKS_SVG : '';
  const unread = !!act && !!act.unread && !attached;

  return html`<div class="so-row ds-drawer__row" data-ds-key="${'row:' + id}" data-id="${id}" data-status="${status}"${unread ? html` data-unread` : ''}><a class="ds-drawer__item so-row__open" href="${'#' + id}"${attached ? html` aria-current="page"` : ''}><span class="so-mark" data-tip="${word}"${ring ? html` data-ring="${ring}"` : ''}>${raw(agentMarkHtml(session.harness, 20))}${
    badge ? html`<span class="so-mark__badge" data-kind="${badge}" aria-hidden="true">${raw(badgeIcon)}</span>` : ''
  }</span><span class="so-row__text"><span class="so-row__label" data-tip="${session.title}"${state.titling.has(id) ? html` data-titling` : ''}>${session.title}</span></span>${
    startAt ? html`<span class="so-row__when" aria-label="${startWords}" data-tip="${startWords}">${raw(icon('clock', 16))}<span class="so-row__time">${whenShort(startAt)}</span></span>` : ''
  }${
    STATUS_GLYPH[status] ? html`<span class="so-row__status" data-status="${status}" role="img" aria-label="${said}" data-tip="${said}">${raw(icon(STATUS_GLYPH[status], 16))}</span>` : ''
  }</a><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm ds-drawer__action ds-hover-actions so-row__more" type="button" aria-label="Session actions" data-tip="Session actions" aria-haspopup="menu" aria-expanded="false" aria-controls="sessionMenu">${raw(icon('moreVertical', 20))}</button></div>`;
}

// whenShort is a start time for a row: the time of day when it is today,
// the weekday and the time within the week, the date beyond that.
function whenShort(at, now = Date.now()) {
  const when = new Date(at);
  if (Number.isNaN(when.getTime())) return '';
  const clock = when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  if (when.toDateString() === new Date(now).toDateString()) return clock;
  const days = (at - now) / 86400000;
  if (days > -1 && days < 6) return when.toLocaleDateString([], { weekday: 'short' }) + ' ' + clock;
  return when.toLocaleDateString([], { day: 'numeric', month: 'short' }) + ' ' + clock;
}

// The technical detail of a session - what it runs, where it works, what it
// runs on, which conversation it is holding, how often it has been brought
// back. It is never in the words of a row: the "Info" item of the row's menu
// is the one place it is shown, for the list and for the header alike. Each
// fact is a label and its value, in plain words.
//
// A chat's model is the one its composer shows: the model and effort the
// chat runs on now, which a change from the model menu makes newer than
// the row's (`running`, from the chat's journal), else the row's.
const CONVERSATION_WORDS = {
  pending: 'Being started',
  known: 'Can be picked up again',
  verified: 'Can be picked up again',
  lost: 'Lost; the next start begins a new one',
};

function rowFacts(session, running = null, mcp = null) {
  const facts = [
    ['Program', harnesses.label(session.harness)],
    ['State', STATE_WORDS[session.state] || session.state],
    ['Folder', session.workdir],
  ];
  // The MCP servers a chat's agent has on - Admin's and its folder's
  // .mcp.json's - and what of them could not be used (GET …/mcp).
  const mcpOn = mcp && Array.isArray(mcp.servers) ? mcp.servers.filter((s) => s.on !== false) : [];
  if (mcpOn.length) {
    facts.push(['MCP servers', mcpOn.map((s) => s.name + (s.transport === 'stdio' ? '' : ' (' + s.transport + ')')).join(' · ')]);
  }
  if (mcp && Array.isArray(mcp.problems) && mcp.problems.length) facts.push(['MCP left out', mcp.problems.join(' ')]);
  const account = session.options?.account
    ? accounts.labelOf(session.options.account) || session.options.account
    : '';
  if (account || session.options?.auto_switch) {
    facts.push(['Account', [account, session.options?.auto_switch ? 'switches at a limit' : ''].filter(Boolean).join(' · ')]);
  }
  if (session.options?.ultracode) facts.push(['Ultracode', 'On']);
  if (Number(session.start_at)) facts.push(['Starts by itself', new Date(Number(session.start_at)).toLocaleString()]);
  // The model and effort by the names the sheet and the composer give them
  // ("Sonnet · High"), not the ids the CLI was started with.
  const model = (running && running.model) || session.model;
  const effort = running && running.model ? running.effort : session.effort;
  if (model) {
    const known = harnesses.modelOf(session.harness, model);
    facts.push(['Model', (known ? known.label || known.id : model) + (effort ? ' · ' + harnesses.effortLabel(effort) : '')]);
  }
  const act = activityOf(session);
  if (act && ACTIVITY_WORDS[act.state]) {
    // What it is doing and since when. How the detector came to think so
    // is no fact a person can use.
    facts.push(['Doing', ACTIVITY_WORDS[act.state]
      + (act.note ? ' · ' + act.note : '')
      + (act.since ? ' · since ' + clockOf(act.since) : '')]);
  }
  const outcome = outcomeOf(session);
  if (outcome) {
    facts.push(['Last turn', outcomeLine(outcome)]);
    if (outcome.phase === 'ready' && Array.isArray(outcome.todos) && outcome.todos.length) {
      facts.push(['Still open', outcome.todos.join(' · ')]);
    }
  }
  if (CONVERSATION_WORDS[session.cli_session_state]) {
    facts.push(['Conversation', CONVERSATION_WORDS[session.cli_session_state]]);
  }
  if (session.resume_count) {
    facts.push(['Brought back', session.resume_count === 1 ? 'Once' : session.resume_count + ' times']);
  }
  return facts.filter(([, value]) => value);
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
function mergeActivity(sessions, { full = false } = {}) {
  if (!sessions) return;
  let changed = false;
  const readHere = [];
  for (const [id, picture] of Object.entries(sessions)) {
    if (!picture || typeof picture !== 'object') continue;
    const prev = state.activity.get(id) || null;
    // A change that is older than the one held - a frame from another socket
    // that was overtaken, a snapshot taken a moment before the commit - is not
    // drawn over the newer one. A hello is the whole state at one moment and
    // is taken as it is, which is also what heals a server that restarted.
    if (!full && prev && olderActivity(picture, prev)) continue;
    // An `unknown` from a session that is running is not known yet: see heldOver.
    const next = heldOver(id, picture, prev);
    state.activity.set(id, next);
    // A chat on screen is read as it is written: the turn it finished there
    // is not news to come back to later.
    if (next.unread && chatOnScreen(id)) readHere.push(id);
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
    if (heldLine.has(id)) followAnnounce(id, next);
  }
  if (changed) renderList();
  for (const id of readHere) markRead(id);
}

// chatOnScreen says whether a chat is what the person is looking at now.
function chatOnScreen(id) {
  return chatShown() && state.chat.shownId() === id && document.visibilityState === 'visible';
}

/**
 * heldOver is what a running session's `unknown` means: not known yet. A
 * server that has just restarted reads every pane again, and until its first
 * commit every session is `unknown` - in the hello as much as in a frame - and
 * a read that found nothing for a tick says the same. Neither is news that a
 * session stopped asking or working, so the last state that was known is kept,
 * under the newer picture's order and unread mark, until a state that is
 * known replaces it. The picture as it came is kept beside it (`held`) for the
 * moment the row stops running (releaseHolds): a dead pane is `unknown` too,
 * and a session that is started again must not come back with the question it
 * was asking in its last life.
 */
function heldOver(id, next, prev) {
  if (next.state !== 'unknown' || !prev || !prev.state || prev.state === 'unknown') return next;
  const session = sessionOf(id);
  if (!session || session.state !== 'running') return next;
  return { ...prev, seq: next.seq, unread: next.unread, held: prev.held || next };
}

// releaseHolds lets go of every state heldOver kept for a session that is no
// longer running: what it holds from then on is what the server said.
function releaseHolds() {
  for (const [id, act] of state.activity) {
    if (!act || !act.held) continue;
    const session = sessionOf(id);
    if (session && session.state === 'running') continue;
    state.activity.set(id, { ...act.held, seq: act.seq, unread: act.unread });
  }
}

/**
 * olderActivity says whether `next` is a picture from before the one `prev`
 * is. The server's `seq` answers it exactly: it moves on with every picture it
 * publishes - a new state, a note, the unread mark - and starts from the clock,
 * so one from before a restart is below every one after it. A server too old
 * to send it leaves `since` - the moment of the last committed change - which
 * can only tell a strictly older commit: two pictures of the same commit are
 * never called older by it, which is why a list read over HTTP does not bring
 * its activity to sessions the live stream already speaks for (applyList).
 */
function olderActivity(next, prev) {
  const a = Number(next.seq);
  const b = Number(prev.seq);
  if (Number.isFinite(a) && Number.isFinite(b) && next.seq != null && prev.seq != null) return a < b;
  const at = Number(next.since) || 0;
  const was = Number(prev.since) || 0;
  return at > 0 && was > 0 && at < was;
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
  // A turn whose verdict is being read is said with it, as its notification
  // is, or as it is once the wait is over.
  const outcome = next.state === 'idle' ? state.outcomes.get(id) : null;
  if (outcome && outcome.phase === 'pending' && Number(outcome.since) === Number(next.since)) {
    holdAnnounce(id, who, outcome);
    return;
  }
  say(line);
}

// The lines waiting for a verdict (holdAnnounce), keyed by session.
const heldLine = new Map();

function holdAnnounce(id, who, outcome) {
  dropAnnounce(id);
  const wait = Math.max(0, (Number(outcome.due) || 0) - Date.now());
  const timer = setTimeout(() => releaseAnnounce(id, null), wait);
  heldLine.set(id, { who, since: Number(outcome.since), timer });
}

// releaseAnnounce says a held line, with its verdict when there is one.
function releaseAnnounce(id, outcome) {
  const held = heldLine.get(id);
  if (!held) return;
  dropAnnounce(id);
  const word = outcome && outcome.phase === 'ready' ? OUTCOME_WORDS[outcome.kind] : '';
  say(held.who + ' finished' + (word ? ': ' + word.toLowerCase() : ''));
}

function dropAnnounce(id) {
  const held = heldLine.get(id);
  if (!held) return;
  clearTimeout(held.timer);
  heldLine.delete(id);
}

// followAnnounce lets go of a held line whose turn turned out not to be over,
// or that somebody has read meanwhile.
function followAnnounce(id, next) {
  const held = heldLine.get(id);
  if (!held || next.state === 'unknown') return;
  if (next.state !== 'idle' || !next.unread) dropAnnounce(id);
  else held.since = Number(next.since);
}

function say(line) {
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
    applyList(data.sessions || [], { rev: data.rev, source: 'fetch' });
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
 * applyList takes the whole list of sessions as the server has it and draws
 * it. It is the one path every full list comes through - a fetch, the event
 * stream's hello and its `sessions` frames, the fallback poll - so a list
 * that arrived by any of them is drawn by the same rules.
 */
function applyList(rows, { rev, source = 'hello' } = {}) {
  const live = !!state.events && state.events.live();
  const numbered = typeof rev === 'number' && Number.isFinite(rev);
  // A list is a picture of the store at one revision. The stream's lists come
  // in order on one socket, and a hello starts that order again (a server
  // that restarted counts from the beginning). A list read over HTTP while
  // the stream is live can have been read before a change the stream has
  // already drawn - a deleted session would come back - so one older than
  // what is on screen is dropped. While the stream is down, an HTTP list is
  // the only news there is and is taken.
  if (numbered && typeof state.listRev === 'number' && rev < state.listRev
    && (source === 'stream' || (source === 'fetch' && live))) return;
  if (source === 'hello') state.listRev = numbered ? rev : null;
  else if (numbered) state.listRev = typeof state.listRev === 'number' ? Math.max(state.listRev, rev) : rev;
  if (source === 'fetch' && state.events) state.events.noteFresh();
  state.sessions = Array.isArray(rows) ? rows.filter(Boolean) : [];
  // The list is the catch-up path for a sidebar nobody has a socket open
  // for: every view carries the activity of its own session. While the
  // stream is live it carries every committed change as a frame of its own,
  // in order, so the activity a list was read with - a moment earlier - is
  // only taken for a session the page has heard nothing about yet.
  const seen = {};
  for (const row of state.sessions) {
    if (!row.activity) continue;
    if (source !== 'hello' && live && state.activity.has(row.id)) continue;
    seen[row.id] = row.activity;
  }
  mergeActivity(seen, { full: source === 'hello' });
  state.loading = false;
  state.reachable = true;
  if (state.current) {
    const fresh = sessionOf(state.current.id);
    if (fresh) applySession(fresh);
    else if (state.chat && state.chat.shownId() === state.current.id) {
      // A chat deleted on another device. A terminal finds out through its
      // socket; a chat has none, and its stream would go on asking for one.
      detachStage();
      showHome({ replace: true });
    }
  }
  // What is known about a session that is gone goes with it.
  for (const id of [...state.digests.keys()]) if (!sessionOf(id)) state.digests.delete(id);
  renderList();
  // The tab was asked for a session the list could not yet prove was there
  // - a notification opened while the network was down - and this is the
  // first list that can. Home leaves `wanted` empty, so nothing is attached
  // behind a person who is reading it.
  openWanted();
}

/* ------------------------------------------------------ the event stream */

// What the stream hands over that the list does not: the digest and the
// verdict of each session. A hello replaces both maps, because a hello is
// the whole state at that moment; a frame changes one entry.
function replaceMap(map, entries) {
  map.clear();
  if (!entries || typeof entries !== 'object') return;
  for (const [id, value] of Object.entries(entries)) if (value) map.set(id, value);
}

// carriedOver is a digest with what the page held for the session kept in
// it while the new one is still being read: a pending digest with nothing
// in it - a server that has just restarted and has not read the chat's
// journal yet - would otherwise blank Home's line until the read is in.
function carriedOver(held, digest) {
  if (!held || !digest || digest.phase !== 'pending' || digest.question || digest.summary || digest.context) return digest;
  return { ...digest, summary: held.summary || '', context: held.context || '', question: held.question || null };
}

// replaceDigests is the hello's digests. A running session the hello has no
// digest for is one a restarted server has not read yet - a server only
// forgets the digest of a session that was deleted - so what the page held
// stays, as pending, until it has: Home's line and its question would
// otherwise go for the moment of the read.
//
// Whether a session is running is what the hello's own rows say (`rows`),
// not the list the page held before it: across a reboot that list still
// says running where the hello says needs_resume. A program that is not
// running is not read again - only a running one is digested - so a pending
// digest kept for it would never be replaced, and once the session was
// resumed the question its last life asked would be counted again and shown
// as being read for good. What it was doing is kept, as the last word about
// it; the question goes with the program that asked it.
function replaceDigests(entries, rows) {
  const held = new Map(state.digests);
  const listed = Array.isArray(rows) ? new Map(rows.filter(Boolean).map((row) => [row.id, row])) : null;
  replaceMap(state.digests, entries);
  for (const [id, digest] of held) {
    const now = state.digests.get(id);
    if (now) {
      state.digests.set(id, carriedOver(digest, now));
      continue;
    }
    const session = listed ? listed.get(id) : sessionOf(id);
    if (!session) continue;
    if (session.state === 'running') state.digests.set(id, { ...digest, phase: 'pending' });
    else state.digests.set(id, { ...digest, question: null, phase: digest.phase === 'pending' ? 'ready' : digest.phase });
  }
}

function digestFrame(id, digest) {
  if (digest) state.digests.set(id, carriedOver(state.digests.get(id), digest)); else state.digests.delete(id);
  drawLive();
}

// outcomeFrame is a verdict that changed, or went (null). Every outcome frame
// comes twice - on the terminal socket and on the event stream - and one
// that is older than the one held is not drawn over it.
function outcomeFrame(id, outcome) {
  const held = state.outcomes.get(id);
  if (outcome) {
    if (held && Number(held.rev) > Number(outcome.rev)) return;
    state.outcomes.set(id, outcome);
  } else {
    state.outcomes.delete(id);
  }
  if (outcome && outcome.phase !== 'pending') {
    releaseAnnounce(id, outcome);
    if (notifier) notifier.judged(id, outcome);
  }
  renderList();
}

// newsFrame is news about a session in a notification's words, which the
// server found outside its activity: a chat stopped at its account's limit
// (notify.told). Not for a session the page cannot name, nor for one filed
// in the background, as at a turn's end (mergeActivity).
function newsFrame(id, news) {
  const session = sessionOf(id);
  if (!session || harnesses.isTool(session.harness) || statusOf(session) === 'background') return;
  if (notifier && news.body) notifier.told(id, { body: news.body, kind: news.kind === 'question' ? 'question' : 'finished' });
}

function mountStream() {
  return mountEvents({
    hello: (data) => {
      forgetTitling();
      // A frame about a test run may have been missed while the stream was down.
      if (state.home) state.home.testsFrame();
      if ('digests' in data) {
        replaceDigests(data.digests, data.sessions);
        state.digestsHeard = true;
      }
      if ('outcomes' in data) replaceMap(state.outcomes, data.outcomes);
      if (Array.isArray(data.sessions)) applyList(data.sessions, { rev: data.rev, source: 'hello' });
      else drawLive();
    },
    sessions: (rows, rev) => applyList(rows, { rev, source: 'stream' }),
    activity: (sessions) => mergeActivity(sessions),
    title: (frame) => titleFrame(frame),
    digest: digestFrame,
    outcome: outcomeFrame,
    news: newsFrame,
    // A test run changed: Home's card of it reads the runs again.
    tests: () => { if (state.home) state.home.testsFrame(); },
    // A chat's Status phases come this way; a terminal's come on its own
    // socket (onControl), and are not taken twice.
    phase: (frame) => {
      if (state.assist && isChat(state.current) && state.current.id === frame.id) state.assist.statusFrame(frame);
    },
    // A pong: the page is as current as the moment its ping was sent, which
    // is what a question asked for by a notification is waiting to know -
    // and a badge a push wrote over (badgeWrittenElsewhere).
    fresh: () => tellAsking(),
    status: () => drawLive(),
    // A poll that could not be answered while the stream is down. It is said
    // on Home and in the sidebar's empty state rather than in a toast: it
    // repeats every few seconds for as long as the outage lasts.
    unreachable: () => {
      state.loading = false;
      state.reachable = false;
      renderList();
    },
    fast: () => homeShown() || askDialogOpen(),
  });
}

// streamConnection is how the event stream is, for Home's connection line
// and the question dialog's (connectionLine).
function streamConnection() {
  const events = state.events;
  return {
    status: events ? events.status() : 'connecting',
    updatedAt: events ? events.updatedAt() : 0,
    blocked: events ? events.blocked() : false,
    pollEvery: events ? events.pollEvery() : 0,
    resuming: events ? events.resuming() : false,
    quiet: events ? events.quiet() : false,
  };
}

// The sidebar's own tests, handed to Home so that it counts with them rather
// than with copies of them.
function homeCtx() {
  return {
    sessions: () => state.sessions,
    activityOf,
    statusOf,
    digestOf: (id) => state.digests.get(id) || null,
    outcomeOf,
    checking,
  };
}

/**
 * showQuestions opens the question dialog, at one chat or the oldest
 * question. Only a chat asks: its question is a structured ask, answered in
 * the chats' dialog (askdialog.js).
 */
function showQuestions(id) {
  const target = id ? sessionOf(id) : questionQueue(homeCtx())[0];
  if (isChat(target)) showChatQuestions(target.id);
  // A Shell or an editor never asks: a link to its question has nothing to
  // open, and says so.
  else if (target) toast('Nothing is waiting for an answer there.');
  else if (id) {
    // A chat this tab does not hold yet - one made while it was offline, a
    // list that could not be read - is looked for again once the page holds
    // a current state; one the current list does not have is gone.
    if (knownCurrent() && state.questionWanted && state.questionWanted.tries > 0) {
      state.questionWanted = null;
      toast('That chat is not here any more.');
    } else wantQuestion(id);
  }
}

// wantQuestion keeps a question asked for until the page can show it.
function wantQuestion(id) {
  const kept = state.questionWanted && state.questionWanted.id === id ? state.questionWanted : null;
  state.questionWanted = { id, since: kept ? kept.since : Date.now(), tries: kept ? kept.tries : 0 };
}

/**
 * questionWhenKnown shows a question asked for from outside the page - a
 * notification tapped on a phone whose connection had gone - that could not
 * be shown when it was asked for, the moment the page holds a current state
 * again. It is tried a few times, for two minutes at most: a question that
 * is still not there by then was answered or went with its chat.
 */
function questionWhenKnown() {
  const wanted = state.questionWanted;
  if (!wanted) return;
  if (Date.now() - wanted.since > 120000 || wanted.tries >= 3 || !homeShown() || askDialogOpen()) {
    state.questionWanted = null;
    return;
  }
  if (!knownCurrent()) return;
  wanted.tries += 1;
  showQuestions(wanted.id);
}

/**
 * showChatQuestions opens the chats' question dialog over every chat that is
 * asking, oldest question first, at the one asked for. `only` keeps it to
 * that chat - the Answer of a card in the chat itself.
 */
function showChatQuestions(chatId, { askId = '', only = false } = {}) {
  const asking = only ? [] : questionQueue(homeCtx()).filter((session) => isChat(session));
  const wanted = sessionOf(chatId);
  if (wanted && !asking.some((session) => session.id === chatId)) asking.unshift(wanted);
  openChatAsks({
    sessions: asking,
    chatId,
    askId,
    view: state.chat,
    // "Open chat": the dialog has closed itself, and the chat takes the stage.
    openChat: (id) => selectSession(id),
    // What is asked while the dialog walks joins the walk.
    asking: () => questionQueue(homeCtx()).filter((session) => isChat(session)),
    // No connection to read the question over: it opens once there is one.
    unreachable: () => {
      if (!state.questionWanted || state.questionWanted.id !== chatId) toast('No connection. The question opens as soon as it is back.');
      wantQuestion(chatId);
    },
    // Home's connection line, which the dialog over it says too.
    connection: () => connectionLine(streamConnection(), state.reachable),
  });
}

/**
 * questionOnHome is a question asked for from outside the page - the address
 * `#q=<id>`, a notification tapped in this tab or in its worker's tray - and
 * every one of them comes through here, so all of them land the same way:
 * Home on the stage, and the question dialog over it at that question. Home
 * is what is under the dialog when it closes, which is what a person who
 * came for one question wants to see next - whatever else is asking.
 *
 * `replace` is for an address that asked for it: that entry is the page's
 * own and is replaced by Home. A notification adds Home as a step, so Back
 * returns to the session that was on screen.
 */
function questionOnHome(id, { replace = false } = {}) {
  if (!homeShown()) showHome({ replace });
  else if (location.hash !== '#home') history.replaceState(history.state, '', '#home');
  showQuestions(id);
}

// openOnNews is a session asked for from outside the page - a notification
// that it finished or was started - by the same rule as its address
// (`#open=<id>`): it is wanted, so a tab that does not hold the list yet
// opens it the moment the list proves it is there (openWanted). A question
// dialog still open from an earlier tap is closed first, as its own "Open
// session" closes it: a modal left in front would hold the session asked for
// inert behind it.
function openOnNews(id) {
  closeAskDialog();
  state.wanted = id;
  if (!state.current || state.current.id !== id) selectSession(id);
}

/**
 * questionsOnHome is `#questions`, the address of the app's "Questions"
 * shortcut: Home, and the dialog over it at the oldest question - or Home
 * alone when nothing is asking. Whether anything is is only known once the
 * page holds a current state (the activity a hello brings), so a cold start
 * waits for one (questionsWhenKnown).
 */
function questionsOnHome({ replace = false } = {}) {
  if (!homeShown()) showHome({ replace });
  else if (location.hash !== '#home') history.replaceState(history.state, '', '#home');
  state.questionsWanted = true;
  questionsWhenKnown();
}

function questionsWhenKnown() {
  if (!state.questionsWanted) return;
  // Gone from Home in the meantime, or opened some other way: the person
  // has moved on, and the dialog is not put in front of what they went to.
  if (!homeShown() || askDialogOpen()) { state.questionsWanted = false; return; }
  if (!knownCurrent() || !state.digestsHeard) return;
  state.questionsWanted = false;
  if (questionQueue(homeCtx()).length) showQuestions('');
}

/**
 * knownCurrent says whether what the page holds is the server's state now,
 * as far as the page can know: a hello or a poll has arrived since it
 * loaded, and the stream is live, or it is down but a poll has read the
 * state within the window Home calls current (connectionLine). A page
 * coming back from being hidden, or a live stream whose ping is unanswered,
 * holds what may be old, and old is not something to put on the app's icon.
 */
function knownCurrent() {
  const events = state.events;
  if (!events || !(events.stateAt() > 0) || !state.reachable) return false;
  const conn = streamConnection();
  const line = connectionLine(conn, state.reachable);
  if (line.kind === 'live' || line.kind === 'polling') return true;
  return line.kind === 'connecting' && conn.status !== 'live' && !conn.resuming && conn.updatedAt > 0;
}

/**
 * tellAsking hands the sessions that are asking - the Questions card's own
 * list - to the notifier, for the number on the app's icon and the
 * questions in the tray that were answered. It is called wherever Home is
 * drawn from the stream, and on the pong that follows the page coming back
 * into view or a push arriving (badgeWrittenElsewhere); a page
 * whose state is not known to be current says nothing, because a number from
 * before the phone slept is worse than the number the last push left.
 */
function tellAsking() {
  if (!notifier || !knownCurrent()) return;
  // Written over by a push: what the page held then is not what to write
  // back - it may be from before the phone slept, or behind the push by the
  // moment the push took to arrive - so only a state from after it is.
  if (state.badgeStale && !(state.events.stateAt() >= state.staleSince)) return;
  const force = state.badgeStale;
  state.badgeStale = false;
  notifier.asking(questionQueue(homeCtx()).map((session) => session.id), { force });
}

/**
 * badgeWrittenElsewhere is the app's badge and tray written by something
 * other than this page: a push while the page was away, or one that has just
 * arrived. They are written again (tellAsking, forced) from the first state
 * the server sends after this moment - a hello, a poll, or the pong to the
 * ping asked for here - and never from the one the page holds now: a page
 * that was frozen in a pocket can come back with a socket that looks live
 * and a state from before every question that was pushed meanwhile, and
 * those questions' notifications would be closed as answered.
 */
function badgeWrittenElsewhere() {
  state.badgeStale = true;
  state.staleSince = Date.now();
  if (state.events) state.events.confirm();
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
  if (!session) return;
  attach(session);
  // The address says what is on screen, which until now was Home - in place
  // of the entry the page itself wrote, so Back leaves rather than coming
  // back here through the link it was opened at.
  if (location.hash !== '#' + session.id) history.replaceState(history.state, '', '#' + session.id);
}

/* ---------------------------------------------------- the session actions */

// pointFolder points a session whose folder is not on this machine - one a
// restore brought back without its code (REBUILD §6.2) - at the folder its
// code is in now. The session starts or resumes there from then on.
async function pointFolder(session, opener) {
  const dir = await promptDialog({
    title: 'Point to folder',
    body: 'This session\'s folder is not on this machine. Where is its code now? It works in that folder from now on.',
    label: 'Folder',
    value: session.workdir,
    confirmLabel: 'Use this folder',
    opener,
  });
  if (!dir) return;
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/workdir', { method: 'PUT', body: { workdir: dir } });
    if (data.session) replaceSession(data.session);
    toast('The session works in that folder now.', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function renameSession(session, opener) {
  const title = await promptTitle(session.title, opener, session);
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
// and which blocks the page while it is open. It resolves with the new name,
// or null when it was put away. With a session, the sparkles beside the field
// ask for a name from what the session is about and put it in the field,
// saving nothing: the person still decides with Rename.
function promptTitle(current, opener, session) {
  return new Promise((resolve) => {
    const input = el('input', {
      class: 'ds-input', type: 'text', id: 'renameInput', value: current, autofocus: true,
      'aria-labelledby': 'renameTitle', enterkeyhint: 'done',
    });
    const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
    const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Rename' });
    // One row: the field, its microphone and - with a session - the
    // sparkles, side by side (DS's field row), not a row inside a row.
    const row = dictated(input, input);
    if (session) row.append(suggestButton(session, input));
    const dialog = el('dialog', { class: 'ds-dialog so-rename', 'aria-labelledby': 'renameTitle' },
      el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
      el('div', { class: 'ds-dialog__box' },
        el('h2', { class: 'ds-dialog__title', id: 'renameTitle', text: 'Rename this session' }),
        // A name is a sentence somebody says about a session, so it is one of
        // the fields that can be spoken instead of typed - and with the
        // keyboard put away it is the only way this dialog can be answered.
        el('div', { class: 'ds-field' }, row),
        el('div', { class: 'ds-dialog__actions' }, cancel, accept)));
    let answer = null;
    const was = opener && opener.isConnected ? opener : document.activeElement;
    const accepted = () => {
      answer = input.value.trim() || current;
      hideOverlay(dialog);
    };
    accept.addEventListener('click', accepted);
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' && !event.isComposing) { event.preventDefault(); accepted(); }
    });
    // The native close comes at the start of every way out - Escape, the
    // backdrop, Cancel, Rename - so it is the one place the answer is given.
    // The dialog leaves the page once its exit has played, and the focus is
    // put back where it came from: a page whose focus is on a button nobody
    // can see swallows every key that is typed at it.
    dialog.addEventListener('close', () => {
      resolve(answer);
      hideOverlay(dialog, { remove: true }).then(() => { restoreFocus(was); paneAfter(was); });
    }, { once: true });
    // Rename is asked for by a tap (the menu's item, the name itself): the
    // field the dialog opens on takes the keyboard with that focus.
    typeHere(input, { focus: false });
    showOverlay(dialog, was).then(() => input.select());
  });
}

// suggestButton is the rename dialog's sparkles: a name from what the
// session is about - its first message and where it has got to - written into
// the field, as the sparkles beside the name would choose it.
function suggestButton(session, input) {
  const button = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon so-rename__suggest', type: 'button',
    'aria-label': 'Suggest a name', 'data-tip': 'Suggest a name from what it is about',
  }, el('span', { class: 'ds-btn__faces' },
    el('span', { class: 'ds-btn__face' }, iconEl('sparkles', 18)),
    el('span', { class: 'ds-btn__face ds-btn__face--busy', 'aria-hidden': 'true' }, el('span', { class: 'ds-spinner ds-spinner--sm' }))));
  // A press while it asks never gets here: DS blocks a busy button (its guard).
  button.addEventListener('click', async () => {
    button.setAttribute('aria-busy', 'true');
    try {
      if (voiceKey() === false && await keyMissing()) {
        explainNoKey('title', button);
        return;
      }
      const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/title', {
        method: 'POST', attempts: 1, timeout: TITLE_CEILING, body: { suggest: true },
      });
      const title = String((data && data.title) || '').trim();
      if (title && input.isConnected) {
        setVoiceKey(true);
        input.value = title;
        input.focus();
        input.select();
      }
    } catch (err) {
      if (err && Number(err.status) === 409 && /openrouter api key/i.test(String(err.message || ''))) {
        setVoiceKey(false);
        explainNoKey('title', button);
      } else toast(errorMessage(err), 'error');
    } finally {
      button.removeAttribute('aria-busy');
    }
  });
  return button;
}

// infoDialog is the "Info" item of the row menu: everything the machine knows
// about one session, in one place and in one shape. It is the only place these
// facts are drawn - a row is a mark and a name, and nothing else.
function infoDialog(session, opener) {
  const close = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: true, text: 'Close' });
  const list = el('dl', { class: 'ds-facts so-facts' });
  let running = null;
  let mcp = null;
  const draw = () => list.replaceChildren(...rowFacts(session, running, mcp).map(([label, value]) => el('div', {},
    el('dt', { text: label }), el('dd', { class: 'so-fact', text: value }))));
  const dialog = el('dialog', { class: 'ds-dialog so-info-dialog', 'aria-labelledby': 'infoTitle' },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box' },
      el('h2', { class: 'ds-dialog__title so-break', id: 'infoTitle', text: session.title }),
      list,
      el('div', { class: 'ds-dialog__actions' }, close)));
  // A chat this page has read says its model at once; another one is asked.
  running = isChat(session) && state.chat ? state.chat.modelOf(session.id) : null;
  draw();
  if (isChat(session) && !running) {
    api('/api/chats/' + encodeURIComponent(session.id))
      .then((data) => {
        if (data && data.chat && data.chat.model && dialog.isConnected) {
          running = { model: data.chat.model, effort: data.chat.effort || '' };
          draw();
        }
      })
      .catch(() => {});
  }
  if (isChat(session)) {
    api('/api/sessions/' + encodeURIComponent(session.id) + '/mcp')
      .then((data) => { if (data && dialog.isConnected) { mcp = data; draw(); } })
      .catch(() => {});
  }
  dialog.addEventListener('close', () => {
    hideOverlay(dialog, { remove: true }).then(() => paneAfter(document.activeElement));
  }, { once: true });
  showOverlay(dialog, opener && opener.isConnected ? opener : document.activeElement);
}

// setStatus files a session. Done closes its terminal on the server, so a
// session that is being looked at loses its socket and shows what happened
// instead; every other status changes nothing but the mark.
async function setStatus(session, status) {
  if (statusOf(session) === status) return;
  if (status === 'done') state.ending.add(session.id);
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/file', {
      method: 'POST', body: { status },
    });
    const open = state.current && state.current.id === session.id;
    if (status === 'done' && open && !isChat(session)) detachStage();
    replaceSession(data.session);
    if (status === 'done') {
      toast(isChat(session) ? 'Done. Its agent was put to rest; the chat is deleted in 24 hours.'
        : 'Done. Its terminal was closed; the session is deleted in 24 hours.');
    }
    else if (isDone(session) && open) attach(data.session);
  } catch (err) {
    toast(actionFailed(err, 'That session could not be refiled.'), 'error');
  } finally {
    state.ending.delete(session.id);
  }
}

async function deleteSession(session) {
  const editor = session.harness === IDE;
  const yes = await confirmDialog({
    title: editor ? 'Close VS Code?' : 'Delete “' + session.title + '”?',
    body: editor
      ? 'The editor is stopped. What it remembers - open files, unsaved changes, layout - is kept for this folder and comes back the next time it is opened.'
      : isChat(session)
          ? 'The agent is stopped and the conversation is removed. The working directory and everything in it are kept.'
          : 'The terminal is closed and the session is removed. The working directory and everything in it are kept.',
    confirmLabel: editor ? 'Close' : 'Delete',
    danger: true,
  });
  if (!yes) return;
  state.ending.add(session.id);
  try {
    await api('/api/sessions/' + encodeURIComponent(session.id), { method: 'DELETE' });
    forgetDraft(session.id);
    state.sessions = state.sessions.filter((s) => s.id !== session.id);
    // The session on screen is gone, and what is left to look at is Home -
    // not whichever session happens to be next in the list, which is a
    // terminal nobody asked for.
    if (state.current && state.current.id === session.id) {
      detachStage();
      showHome({ replace: true });
    }
    // After the view let it go, which writes it one last time.
    forgetChat(session.id);
    renderList();
    toast(editor ? 'VS Code was closed. Its state was kept.'
      : 'Deleted. The working directory was kept.');
  } catch (err) {
    toast(actionFailed(err, 'That session could not be deleted.'), 'error');
  } finally {
    state.ending.delete(session.id);
  }
}

function downloadJournal(session) {
  window.open('/api/sessions/' + encodeURIComponent(session.id) + '/journal', '_blank');
}

// setAutoSwitch turns a chat's switch of account at a limit on or off.
async function setAutoSwitch(session, on) {
  try {
    const data = await accounts.setAutoSwitch(session.id, on);
    if (data.session) replaceSession(data.session);
    toast(on ? 'At a limit, this chat goes on with the account that has the most left.'
      : 'At a limit, this chat stops and asks.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

// setAutoContinue turns a chat's going on after a limit renews on or off.
async function setAutoContinue(session, on) {
  try {
    const data = await accounts.setAutoContinue(session.id, on);
    if (data.session) replaceSession(data.session);
    toast(on ? 'After a limit, this chat goes on by itself once the limit has renewed.'
      : 'After a limit, this chat waits for you.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

// compactChat asks a chat's agent to compact its conversation: the palette's
// /compact, sent once by its client_id like everything a chat is told. It
// says what became of it, whether the chat is on screen or not: taken,
// refused (the chat shows it as not run until it is dismissed there), or
// kept for when the connection is back.
function compactChat(session) {
  const clientId = queueCommand(session.id, 'compact', '', '/compact');
  if (navigator.onLine === false) toast('Offline. Compact goes to the agent when the connection is back.');
  settled(clientId).then(
    () => toast('Compacting the conversation.'),
    (err) => toast('The conversation could not be compacted: ' + errorMessage(err), 'error'),
  );
}

// setUltracode switches a Claude Code chat's Ultracode on or off. Its agent
// is put to rest as soon as it is idle and takes the option up with the next
// message, so nothing that runs is cut short; the toast says so.
async function setUltracode(session, on) {
  try {
    const data = await api('/api/chats/' + encodeURIComponent(session.id) + '/options', {
      method: 'POST', attempts: 2, body: { ultracode: on },
    });
    if (data && data.session) replaceSession(data.session);
    toast(on ? 'Ultracode is on from the next message.' : 'Ultracode is off from the next message.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

// mcpDialog is a chat's MCP servers, each a switch: those built into
// Socrates and Admin's, on or off by their default until switched here, and
// the folder's .mcp.json's. A switch applies at once; the agent is put to
// rest as soon as it is idle and takes the change up with the next message,
// as with Ultracode. A switch put back to the server's default gives the
// choice back to Admin. The button beside a switch tests the server as this
// chat's agent would have it and lists its tools.
async function mcpDialog(session, opener) {
  const url = '/api/sessions/' + encodeURIComponent(session.id) + '/mcp';
  const list = el('div', { class: 'so-mcp-switches' });
  const note = el('p', { class: 'ds-dialog__text', id: 'mcpDialogText' });
  const close = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: true, text: 'Close' });
  const dialog = el('dialog', { class: 'ds-dialog ds-dialog--wide', 'aria-labelledby': 'mcpDialogTitle', 'aria-describedby': 'mcpDialogText' },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box' },
      el('h2', { class: 'ds-dialog__title', id: 'mcpDialogTitle', text: 'MCP servers' }),
      note, el('div', { class: 'ds-dialog__body' }, list),
      el('div', { class: 'ds-dialog__actions' }, close)));
  let servers = [];
  const draw = (data) => {
    servers = (data && data.servers) || [];
    note.textContent = servers.length
      ? 'What this chat\u2019s agent may use. A change counts from the next message.'
      : 'There is no MCP server yet. Add one in Admin for every chat, or in a .mcp.json in the chat\u2019s folder.';
    if (data && data.problems && data.problems.length) note.textContent += ' ' + data.problems.join(' ');
    list.hidden = !servers.length;
    morph(list, html`${servers.map((server, i) => html`<div class="so-mcp-switch" data-ds-key="${'mcp-' + server.name}" data-name="${server.name}">
      <label class="ds-switch">
        <input type="checkbox" role="switch" data-name="${server.name}" aria-labelledby="${'mcpSwitch' + i}" aria-describedby="${'mcpSwitchHint' + i}">
        <span class="ds-switch__track" aria-hidden="true"></span>
        <span class="ds-switch__label" id="${'mcpSwitch' + i}">${mcpView.labelOf(server)}</span>
        <small class="ds-switch__hint" id="${'mcpSwitchHint' + i}">${[mcpView.fromWords(server), mcpView.brief(server.description)].filter(Boolean).join('. ')}</small>
        <span class="ds-switch__state" aria-hidden="true"><span class="ds-switch__on">On</span><span class="ds-switch__off">Off</span></span>
      </label>
      <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-test="${server.name}" aria-label="${'Test ' + mcpView.labelOf(server) + ' and show its tools'}" data-tip="Test it, show its tools">${raw(icon('info', 18))}</button>
    </div>`)}`);
    // morph keeps a switch as the page left it; the server says.
    for (const input of list.querySelectorAll('input[data-name]')) {
      const server = servers.find((one) => one.name === input.dataset.name);
      input.checked = !!server && !!server.on;
    }
  };
  list.addEventListener('change', (event) => {
    const input = event.target.closest('input[data-name]');
    const server = input && servers.find((one) => one.name === input.dataset.name);
    if (server) choose(server, input);
  });
  list.addEventListener('click', (event) => {
    const button = event.target.closest('button[data-test]');
    const server = button && servers.find((one) => one.name === button.dataset.test);
    if (!server) return;
    mcpView.probeDialog({
      title: mcpView.labelOf(server),
      run: () => api(url + '/test', { method: 'POST', body: { name: server.name }, timeout: 100000, attempts: 1 }),
      opener: button,
    });
  });
  const choose = async (server, input) => {
    const on = input.checked;
    input.setAttribute('aria-busy', 'true');
    try {
      const data = await api('/api/chats/' + encodeURIComponent(session.id) + '/mcp', {
        method: 'POST', attempts: 2, body: { name: server.name, on: on === server.default ? null : on },
      });
      if (dialog.isConnected) draw(data);
      list.querySelector('input[data-name="' + CSS.escape(server.name) + '"]')?.focus();
      toast(mcpView.labelOf(server) + (on ? ' is on' : ' is off') + ' from the next message.');
    } catch (err) {
      input.checked = !on;
      toast(errorMessage(err), 'error');
    } finally {
      input.removeAttribute('aria-busy');
    }
  };
  note.textContent = 'Asking which servers this chat has\u2026';
  list.hidden = true;
  dialog.addEventListener('close', () => {
    hideOverlay(dialog, { remove: true }).then(() => paneAfter(document.activeElement));
  }, { once: true });
  showOverlay(dialog, opener && opener.isConnected ? opener : document.activeElement);
  try {
    draw(await api(url));
  } catch (err) {
    note.textContent = errorMessage(err);
  }
}

// exportChat opens the whole conversation as the server holds it: every
// message, part and question, as JSON.
function exportChat(session) {
  window.open('/api/chats/' + encodeURIComponent(session.id) + '?history=1', '_blank');
}

function replaceSession(next) {
  if (!next) return;
  const at = state.sessions.findIndex((s) => s.id === next.id);
  if (at >= 0) state.sessions[at] = next; else state.sessions.unshift(next);
  if (state.current && state.current.id === next.id) applySession(next);
  renderList();
}

// The row's ⋮: everything that can be done to one session. What belongs to
// this device rather than to a session - the key bar, touch mode, sound and
// notifications - is in the options dialog of the top bar instead.
//
// There is one menu element (#sessionMenu), drawn again for the row that
// opens it; what each of its items does is kept here, by data-action, and run
// by one listener (wire). DS.overlay closes the menu once that ran.
const menuActions = new Map();
// The actions that must run inside the tap that chose them - reading the
// clipboard, opening a file picker - rather than once the menu has gone.
const menuInTap = new Set();
// Each drawing of the menu names its status group's heading afresh: the
// menu and an action sheet may both hold one.
let menuSeq = 0;

// menuItemHtml is one item of the menu: its glyph and its words. A status is
// one of a set, so it is a radio item whose check shows where it stands. A
// switch (a checkbox item) also says On or Off at its end, because a check
// that is simply not there reads as an action, not as a switch that is off.
function menuItemHtml(action, label, glyph, checked, role = 'menuitemradio') {
  if (checked !== undefined) {
    const state = role === 'menuitemcheckbox'
      ? html`<span class="ds-menu__meta" aria-hidden="true">${checked ? 'On' : 'Off'}</span>` : '';
    return html`<button class="ds-menu__item" role="${role}" type="button" data-action="${action}" aria-checked="${checked ? 'true' : 'false'}">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">${label}</span>${state}</button>`;
  }
  return html`<button class="ds-menu__item" role="menuitem" type="button" data-action="${action}">${raw(icon(glyph, 20))}<span class="ds-menu__label">${label}</span></button>`;
}

// A phone has no room for a menu of eleven rows hanging from a button: from
// a row low in the list it would scroll inside itself with nothing to say
// so, and Delete would be below the end. There - a narrow window, or a short
// one under a finger, a phone on its side - the same items rise from the
// bottom edge in a dialog of their own (an action sheet), under the
// session's name.
const actionSheetWanted = () => window.matchMedia('(max-width: 600px)').matches
  || (touchScreen() && window.matchMedia('(max-height: 500px)').matches);

// menuFits is whether the menu, holding the items it is about to show, is
// seen whole on the screen: its natural height against what can be seen
// (the visual viewport, so a keyboard counts), less the margin DS keeps
// around a menu - its own padding. A menu that does not fit - a chat's
// fourteen items on a car's 585 px screen - would scroll inside itself
// with Delete below its end, so its items go in the action sheet instead,
// on any screen. It is measured on an unseen copy: the menu itself is DS's
// to show and hide.
function menuFits(menu) {
  const copy = menu.cloneNode(true);
  copy.removeAttribute('id');
  // An anchored menu is a popover, which the browser does not lay out
  // until it is shown.
  copy.removeAttribute('popover');
  for (const node of copy.querySelectorAll('[id]')) node.removeAttribute('id');
  copy.setAttribute('aria-hidden', 'true');
  copy.hidden = false;
  copy.style.visibility = 'hidden';
  copy.style.maxHeight = 'none';
  document.body.append(copy);
  // Its layout height: a closed menu stands scaled down for its entrance.
  const natural = copy.offsetHeight;
  const margin = parseFloat(getComputedStyle(copy).paddingTop) || 0;
  copy.remove();
  const seen = window.visualViewport ? window.visualViewport.height : window.innerHeight;
  return natural <= seen - 2 * margin;
}

// openActionSheet shows the items of a menu in that dialog (DS
// .ds-dialog--bottom, its items a .ds-dialog__list whose arrow keys DS
// gives as in a menu). An item closes it and then runs, so a dialog it
// opens (Rename, Delete) is the only one on screen.
function openActionSheet(anchor, title, items) {
  const titleId = 'actionsTitle';
  const list = el('div', { class: 'ds-dialog__list', role: 'menu', 'aria-labelledby': titleId });
  morph(list, html`${items}`);
  const close = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-close': true,
    'aria-label': 'Close', 'data-tip': 'Close',
  }, iconEl('close', 20));
  // The title is the chat's name, which can be a whole first prompt: two
  // lines of it (so-frame.css), the rest is in Info and Rename.
  const dialog = el('dialog', { class: 'ds-dialog ds-dialog--bottom so-actions', 'aria-labelledby': titleId },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box' },
      el('div', { class: 'ds-dialog__head' },
        close, el('h2', { class: 'ds-dialog__title so-break', id: titleId },
          el('span', { class: 'so-actions__title', text: title }))),
      list));
  let chosen = null;
  list.addEventListener('click', (event) => {
    const item = event.target.closest('[data-action]');
    // A blocked item's press never gets here (DS's guard).
    if (!item) return;
    const run = menuActions.get(item.dataset.action) || null;
    if (run && menuInTap.has(item.dataset.action)) run();
    else chosen = run;
    hideOverlay(dialog);
  });
  dialog.addEventListener('close', () => {
    anchor.setAttribute('aria-expanded', 'false');
    const run = chosen;
    chosen = null;
    hideOverlay(dialog, { remove: true }).then(() => { if (!run) paneAfter(anchor); });
    if (run) run();
  }, { once: true });
  anchor.setAttribute('aria-expanded', 'true');
  showOverlay(dialog, anchor).then(() => {
    const first = list.querySelector('.ds-menu__item');
    if (first && dialog.open) first.focus({ preventScroll: true });
  });
}

// afterMenu runs `fn` once the row menu has gone (DS.overlay closes it after
// an item's own handler has run).
function afterMenu(fn) {
  const menu = dom.sessionMenu;
  if (menu.hidden) { requestAnimationFrame(fn); return; }
  const watch = new MutationObserver(() => {
    if (!menu.hidden) return;
    watch.disconnect();
    fn();
  });
  watch.observe(menu, { attributes: true, attributeFilter: ['hidden'] });
}

// openRowMenu is a session's menu, from its row's ⋮ or - with `device`, and
// then with this device's options as its last item - from the top bar's.
async function openRowMenu(anchor, id, { device = false } = {}) {
  const session = sessionOf(id);
  const menu = dom.sessionMenu;
  if (!session || !menu) return;
  // A second press of the ⋮ a menu hangs from puts the menu away.
  if (anchor.getAttribute('aria-expanded') === 'true') { hideOverlay(menu); return; }
  // Open from another row: that one goes first, so the menu is never drawn
  // for one session while it hangs from another.
  if (!menu.hidden) await hideOverlay(menu);
  // A folder's editor is not work that is filed: it is open or it is not, and
  // closing it keeps everything it remembers, which is kept per folder.
  const editor = session.harness === IDE;
  // A chat has no scrollback either: its conversation is its history, and
  // compacting it is something to ask of its agent.
  const chat = isChat(session);
  menuActions.clear();
  menuInTap.clear();
  const items = [];
  const add = (action, label, glyph, run, checked, role) => {
    menuActions.set(action, run);
    items.push(menuItemHtml(action, label, glyph, checked, role));
  };
  // A phone's top bar has no room for copy, paste and upload (so-stage.css):
  // for the terminal on the stage they are the first items of its menu.
  if (state.transfer && state.term && state.current && state.current.id === session.id
    && window.matchMedia('(max-width: 600px)').matches) {
    add('copy', 'Copy', 'copy', () => state.transfer.copy(anchor));
    add('paste', 'Paste', 'paste', () => state.transfer.paste());
    add('upload', 'Upload a file', 'upload', () => state.transfer.upload());
    menuInTap.add('paste');
    menuInTap.add('upload');
    items.push(html`<div class="ds-menu__divider" role="separator"></div>`);
  }
  add('info', 'Info', 'info', () => infoDialog(session, anchor));
  if (!editor) add('rename', 'Rename', 'pencil', () => renameSession(session, anchor));
  if (!editor && session.workdir_missing) add('folder', 'Point to folder', 'folder', () => pointFolder(session, anchor));
  if (!editor) {
    // The statuses are one set: a group named by its heading (DS menu
    // groups), whose radio items the arrow keys walk past the heading to.
    const heading = 'menuStatus' + (++menuSeq);
    const group = [];
    for (const one of STATUSES) {
      menuActions.set('status:' + one.id, () => setStatus(session, one.id));
      group.push(menuItemHtml('status:' + one.id, one.label, '', statusOf(session) === one.id));
    }
    items.push(html`<div class="ds-menu__divider" role="separator"></div><div role="group" aria-labelledby="${heading}"><p class="ds-menu__heading" id="${heading}">Status</p>${group}</div>`);
  }
  items.push(html`<div class="ds-menu__divider" role="separator"></div>`);
  if (editor && state.current?.id === session.id) add('reload', 'Reload editor', 'sync', () => attach(session));
  if (chat) {
    // Claude Code's session option, which its agent takes up from the next
    // message on.
    if (session.harness === 'claude') {
      const on = !!(session.options && session.options.ultracode);
      add('ultracode', 'Ultracode', '', () => setUltracode(session, !on), on, 'menuitemcheckbox');
    }
    add('mcp', 'MCP servers\u2026', 'server', () => mcpDialog(session, anchor));
    add('compact', 'Compact the conversation', 'archive', () => compactChat(session));
    add('export', 'Download the conversation', 'download', () => exportChat(session));
    // Off unless asked for, per chat: at a limit the chat goes on with the
    // account that has the most left instead of stopping (REBUILD.md §4.3).
    const on = !!session.options?.auto_switch;
    add('auto-switch', 'Switch accounts at a limit', '', () => setAutoSwitch(session, !on), on, 'menuitemcheckbox');
    // On unless switched off, per chat: a turn stopped by a limit goes on
    // by itself once the limit has renewed.
    const goOn = session.options?.auto_continue !== false;
    add('auto-continue', 'Go on when a limit renews', '', () => setAutoContinue(session, !goOn), goOn, 'menuitemcheckbox');
  } else {
    add('journal', editor ? 'Download server log' : 'Download scrollback', 'download', () => downloadJournal(session));
  }
  add('delete', editor ? 'Close VS Code' : 'Delete', editor ? 'close' : 'trash', () => deleteSession(session));
  if (device) {
    items.push(html`<div class="ds-menu__divider" role="separator"></div>`);
    // Once the menu has gone, so that its leaving - which puts the focus
    // back on the ⋮ and says it is shut - does not happen under the dialog.
    add('device', 'This device\u2026', 'settings', () => afterMenu(() => openOptions(anchor)));
  }
  // A menu cut short (at 1280x800 a chat's twelve items stopped at
  // Ultracode) fades at its cut edge: DS.overlay watches its scroll. One
  // that would not fit the screen at all is the action sheet (menuFits).
  morph(menu, html`${items}`);
  const sheet = actionSheetWanted() || !menuFits(menu);
  anchor.setAttribute('aria-haspopup', sheet ? 'dialog' : 'menu');
  if (sheet) {
    openActionSheet(anchor, session.title, items);
    return;
  }
  anchor.setAttribute('aria-controls', 'sessionMenu');
  showOverlay(menu, anchor);
}

/* ------------------------------------------------------ the options dialog */

// The top bar's ⋮: the choices that belong to this device and not to any
// session, each a switch that applies at once, so several can be set in one
// go. The dialog is in the page; syncOptions puts every switch where its
// setting is, from the setting itself rather than from the press, so a choice
// the browser refused - a notification permission - shows as the "off" it
// really is.
function syncOptions() {
  const host = dom.optionsDialog;
  if (!host) return;
  const put = (name, on, disabled = false) => {
    const input = host.querySelector('[data-option="' + name + '"]');
    if (!input) return;
    if (input.checked !== on) input.checked = on;
    input.disabled = disabled;
  };
  put('keybar', keyBarWanted());
  put('touch', touchScreen());
  put('sound', !!notifier && notifier.soundOn());
  put('notify', !!notifier && notifier.notifyOn(), !(notifier && notifier.notifyAvailable()));
  const hint = document.getElementById('optNotifyHint');
  const words = notifier ? notifier.notifyHint() : 'Not available in this browser';
  if (hint && hint.textContent !== words) hint.textContent = words;
}

function openOptions(anchor) {
  syncOptions();
  anchor.setAttribute('aria-controls', 'optionsDialog');
  anchor.setAttribute('aria-expanded', 'true');
  showOverlay(dom.optionsDialog, anchor);
}

// barMenuSession is the session the top bar's ⋮ is the menu of: the one on
// the stage, when one is.
function barMenuSession() {
  return !homeShown() && state.current ? sessionOf(state.current.id) || state.current : null;
}

// drawBarMenu says on the top bar's ⋮ what a press opens: the menu of the
// session on the stage, or this device's options.
function drawBarMenu() {
  const button = dom.optionsBtn;
  const session = barMenuSession();
  const words = !session ? 'Options for this device' : isChat(session) ? 'Chat actions' : 'Session actions';
  if (button.getAttribute('aria-label') !== words) {
    button.setAttribute('aria-label', words);
    button.setAttribute('data-tip', words);
  }
  button.setAttribute('aria-haspopup', session && !actionSheetWanted() ? 'menu' : 'dialog');
}

// setOption is one switch of the options dialog, pressed.
function setOption(name, on) {
  if (name === 'keybar') {
    setKeyBarWanted(on);
    applyKeyBar();
  } else if (name === 'touch') {
    setTouchMode(on);
    // The key bar follows the touch screen until it has been asked for or
    // put away on its own, so a device that has only ever been told about
    // touch mode gets the bar that goes with it.
    applyKeyBar();
  } else if (name === 'sound') {
    if (notifier) notifier.setSound(on);
  } else if (name === 'notify') {
    if (notifier) notifier.setNotify(on);
  }
  syncOptions();
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
  if (!state.term || homeShown()) return;
  if (overlayOpen()) return;
  const active = document.activeElement;
  if (active && active.isConnected
    && active !== document.body && active !== document.documentElement) return;
  state.term.focus();
}

/* ------------------------------------------------------- the terminal view */

function homeShown() { return !!state.home && state.home.shown(); }

/**
 * showHome puts Home on the stage: what every session is doing, as numbers
 * and as a list. It is the landing page, and it is what is left when the
 * session on screen goes away.
 *
 * Nothing is attached while it is shown - no terminal, no socket, no panel -
 * so nothing on it can take the focus, and the top bar is reset to the state
 * it has with no session: no status, no copy and paste.
 */
function showHome({ replace = false } = {}) {
  if (state.pendingAttach) { clearTimeout(state.pendingAttach); state.pendingAttach = null; }
  detachStage();
  state.current = null;
  state.wanted = '';
  dom.keybar.hidden = true;
  dom.termWrap.hidden = true;
  setTitleText('Home');
  dom.titleRegen.hidden = true;
  if (state.assist) state.assist.attached();
  if (state.transfer) state.transfer.attached();
  dom.sessionStatus.hidden = true;
  dom.sessionOutcome.hidden = true;
  closeCover();
  clearNotice();
  // A move the person made is a step Back can undo; the page putting its
  // own address in order - a link it was opened at, a session that went - is
  // not, and replaces the entry rather than leaving one that reopens what
  // was already dealt with.
  if (location.hash !== '#home') {
    if (replace) history.replaceState(history.state, '', '#home');
    else location.hash = '#home';
  }
  closeNav();
  dom.homeBtn.setAttribute('aria-current', 'page');
  if (state.home) state.home.show();
  if (state.events) state.events.refresh();
  // No row is the one on screen any more.
  drawList();
}

// hideHome takes Home off the stage, for a session that is being put there.
function hideHome() {
  dom.homeBtn.removeAttribute('aria-current');
  if (!homeShown()) return;
  state.home.hide();
  if (state.events) state.events.refresh();
}

// applySession draws everything about a session that is not the pane itself.
function applySession(session) {
  state.current = session;
  setTitleText(session.title);
  drawTitling(session.id);
  drawStatusChip(statusOf(session));
  drawOutcomeTag();

  // What it runs and on which model is in the list's mark and in "Info" in
  // the row's menu; the bar keeps its width for the name. A chat says what
  // it used under its own "i" (chatview.js).
  if (isChat(session) && state.chat) state.chat.update(session);

  drawOverlay(session);
  renderList();
}

// drawOverlay is §E.7's table. Everything it can say is a state the session is
// actually in, and each one names the way out of it.
//
// The cover is a ds-dialog--contained over the pane (#termOverlay), opened and
// closed through DS.overlay, so it traps the focus inside the pane only and
// the drawer and the bar stay usable. It is drawn often - every list refresh
// redraws it - so the box is patched in place with morph() and a pressed,
// busy button keeps its face; the buttons carry what they do in data-cover
// and one listener answers them for whichever session is under the cover.
let coverSession = null;
let coverWired = false;

function drawOverlay(session) {
  wireCover();
  const markup = coverMarkup(session);
  if (!markup) {
    closeCover();
    return;
  }
  coverSession = session;
  const host = dom.termOverlay;
  morph(host.querySelector('.ds-dialog__box'), markup);
  // Opened once; a redraw of an open cover only patches it, and one that is
  // on its way out is taken back.
  if (host.hidden || host.getAttribute('data-state') !== 'open') showOverlay(host);
}

// closeCover takes the cover off the pane, if it is on it.
function closeCover() {
  coverSession = null;
  const host = dom.termOverlay;
  if (!host.hidden && host.getAttribute('data-state') !== 'closed') hideOverlay(host);
}

function wireCover() {
  if (coverWired) return;
  coverWired = true;
  const host = dom.termOverlay;
  host.querySelector('.ds-dialog__box').addEventListener('click', (event) => {
    const button = event.target.closest('[data-cover]');
    const session = coverSession;
    // A busy button takes no press (DS's guard).
    if (!button || !session) return;
    const action = button.getAttribute('data-cover');
    if (action === 'restart') restart(session);
    else if (action === 'delete') deleteSession(session);
    else if (action === 'pickup') setStatus(session, 'in_progress');
    else if (action === 'open' && !state.resuming) attach(session);
  });
  // The cover is a state of the session, not a question to wave away: Escape
  // inside the pane leaves it where it is (DS.overlay honours a prevented key).
  host.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') event.preventDefault();
  });
}

// coverMarkup is what the cover says for a session, or null when the pane is
// free.
function coverMarkup(session) {
  // A chat has no pane to cover: it says how it is in its own dock.
  if (isChat(session)) return null;
  const title = (words) => html`<h2 class="ds-dialog__title" id="termOverlayTitle">${words}</h2>`;
  const actions = (...buttons) => html`<div class="ds-dialog__actions">${buttons}</div>`;
  // The way out is where the focus lands when the cover comes up.
  const button = (action, label, primary = false) => html`<button class="ds-btn${primary ? ' ds-btn--primary' : ''}" type="button" data-cover="${action}"${primary ? raw(' autofocus') : ''}>${label}</button>`;
  // A done session's terminal was closed on purpose, and that is the whole of
  // what there is to say about it, whatever state the row is in.
  if (isDone(session)) {
    return html`${title('This session is done.')}
      <p class="ds-dialog__text" id="termOverlayText">${'Its terminal was closed. ' + (deleteWords(session) || 'It is deleted soon') + '.'}</p>
      ${actions(button('delete', 'Delete now'), button('pickup', 'Pick it up again', true))}`;
  }
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
    case 'exited': {
      // Why a pane ended is the one fact the cover is raised for, so it is a
      // plain line under the sentence rather than behind an "i" - when it is
      // known: -1 is the store's "tmux did not say" (a shell left with
      // `exit` sometimes ends before tmux has its status), not a status, and
      // "Exit status -1" read as a failure that never happened.
      const status = Number(session.exit_status);
      const why = !Number.isInteger(status) || status < 0 ? ''
        : status === 0 ? 'It ended normally.' : 'It ended with exit status ' + status + '.';
      return html`${title('The session ended.')}
        ${why ? html`<p class="so-pane__detail" id="termOverlayText">${why}</p>` : ''}
        ${actions(button('delete', 'Delete'), actionButton(session, 'Restart', 'Restarting'))}`;
    }
    case 'failed':
      return html`${title('The session could not start.')}
        ${session.fail_reason ? html`<p class="so-pane__detail" id="termOverlayText">${session.fail_reason}</p>` : ''}
        ${actions(actionButton(session, 'Try again', 'Trying again'))}`;
    case 'resuming':
      return html`<div class="so-pane__wait" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>${title('Resuming after a restart…')}</div>`;
    case 'needs_resume':
      return html`${title('This session is not running.')}
        ${actions(button('open', 'Open', true))}`;
    default:
      return null;
  }
}

// actionButton is the cover's own button, drawn from `state.restarting` so
// that a redraw in the middle of a relaunch keeps its busy face instead of
// offering to start it again.
function actionButton(session, label, busyLabel) {
  const busy = state.restarting === session.id;
  return html`<button class="ds-btn ds-btn--primary" type="button" id="termRestart" data-cover="restart" autofocus${
    busy ? raw(' aria-busy="true"') : ''}>${faces(label, busyLabel)}</button>`;
}

// How long a line above the pane stays before it puts itself away. Long enough
// to read one sentence and look back at it - the same beat as the ticker's own
// linger in assist.js, because the two lines sit on top of each other and a
// reader should not have to learn two rhythms. A line that carries a control
// the person still has to press is never taken away on a timer; see `extra`
// below.
const NOTICE_LINGER = 6000;
// The timer of the line that is up, which decides when it goes. There is only
// ever one notice, so there is only ever one of it.
let noticeTimer = null;
// The element the line is up in: over the pane of a terminal, in the dock of a
// chat (where the pane is hidden), as the ticker is.
let noticeIn = null;

function noticeHost() {
  return chatShown() ? state.chat.noticeBox() : dom.termNotice;
}

// The mark each kind of notice leads with; anything else is information.
const NOTICE_ICON = { status: 'summarize', desync: 'sync', resized: 'monitor', resumed: 'history' };

// clearNotice takes the line away with no ceremony and cancels whatever it had
// running. It is the path for "there is nothing to say any more" - a session
// being detached - rather than for "this has been read".
function clearNotice() {
  if (noticeTimer) { clearTimeout(noticeTimer); noticeTimer = null; }
  for (const host of new Set([dom.termNotice, noticeIn])) {
    if (!host) continue;
    M.cancel(host);
    host.hidden = true;
  }
}

// notice draws the thin line at the top of the pane (a ds-alert). It never
// blocks, it is always dismissible, and it puts itself away once it has been
// read; `onDismiss` is what a notice does when it goes, however it goes - the
// close button and the timer are the same decision, so the resume flag is
// cleared either way.
//
// `facts` is the technical half of what the notice knows - the conversation a
// resume came from, and anything else that is an identifier rather than a
// sentence. §E.10 rule 3: it goes behind the "i" and never into the line
// itself.
function notice(kind, text, onDismiss, facts, extra) {
  const host = noticeHost();
  // A line up in the other place goes with no ceremony: it is not on screen.
  if (noticeIn && noticeIn !== host) clearNotice();
  noticeIn = host;
  // A new line replaces the one that is up, so the space is free for the next
  // one and no timer from the old line reaches the new one.
  if (noticeTimer) { clearTimeout(noticeTimer); noticeTimer = null; }
  const wasUp = !host.hidden && !host.hasAttribute('data-ds-leaving');
  M.cancel(host);
  host.dataset.kind = kind;
  let gone = false;
  const dismiss = () => {
    if (gone) return;
    gone = true;
    if (noticeTimer) { clearTimeout(noticeTimer); noticeTimer = null; }
    // It leaves the way it came, and the space is free for the next one.
    M.leave(host, '', { then: 'hide' });
    if (onDismiss) onDismiss();
  };
  const close = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button',
    'aria-label': 'Dismiss', 'data-tip': 'Dismiss', onclick: dismiss,
  }, iconEl('close', 18));
  // `replaceChildren` writes a null argument as the word "null", so the parts
  // a notice may lack are filtered out first.
  host.replaceChildren(...[
    iconEl(NOTICE_ICON[kind] || 'info', 20),
    el('span', { class: 'ds-alert__text', text }),
    facts && facts.length ? infoTip(facts, { label: 'Details', code: true }) : null,
    // `extra` is the one control a notice may carry beside its "i" - the
    // Cancel of a run in progress. Everything else a notice can say, it says
    // in words.
    extra || null,
    close,
  ].filter(Boolean));
  // A line that replaces one already up changes in place; a new one comes in.
  if (wasUp) host.hidden = false;
  else M.enter(host);
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

function detachStage() {
  ideGeneration += 1;
  dom.stage.removeAttribute('data-ide');
  showKeyboardSwitch(false);
  if (state.assist) state.assist.attached();
  if (state.keybar) { state.keybar.dispose(); state.keybar = null; }
  dom.keybar.hidden = true;
  placeToasts();
  if (state.socket) { state.socket.stop(); state.socket = null; }
  if (state.term) { state.term.dispose(); state.term = null; }
  dom.term.replaceChildren();
  dom.termWrap.removeAttribute('data-stale');
  state.live = false;
  state.resuming = false;
  if (state.transfer) state.transfer.attached();
  // A chat stands where the pane does; whatever comes next - a terminal,
  // another chat, nothing - starts from the pane.
  if (state.chat) state.chat.hide();
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
  detachStage();
  hideHome();
  // A session nobody can see is a session nobody can leave: the row is the
  // only way back to it, so the folder it is in is opened if the person had
  // shut it.
  openFolder(session);
  applySession(session);
  // A chat is drawn by the page: its conversation and its composer stand
  // where the pane would, whatever it is filed as - a done chat's agent is
  // put to rest, and its conversation is still there to read.
  if (isChat(session)) {
    dom.termWrap.hidden = true;
    if (notifier) notifier.hush(session.id);
    state.chat.show(session);
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
    // The stage holds an editor: no lines over it, no inset around it. Its
    // fields wait for the keyboard switch, as the terminal's does.
    dom.stage.setAttribute('data-ide', '');
    showKeyboardSwitch(true);
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

  // The terminal is measured and drawn in its own font, so it waits for it
  // (term.js loadTerminalFonts, which gives up after a while): the session
  // is on the stage meanwhile, and its pane comes once the font is in -
  // unless the person has gone somewhere else by then.
  if (!terminalFontsSettled()) {
    loadTerminalFonts().then(() => {
      if (state.current && state.current.id === session.id && !state.term && !state.socket) attach(sessionOf(session.id) || session);
    });
    return;
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
  showKeyboardSwitch(true);
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
/**
 * readyIDE wakes an editor and waits for it to answer, and resolves with the
 * address it is served at - or with '' when current says it is no longer
 * wanted. It throws what went wrong.
 */
async function readyIDE(session, current) {
  const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/resume', {
    method: 'POST', timeout: 60000,
  });
  if (!current()) return '';
  replaceSession(data.session);
  if (data.error) throw new Error(data.error);
  const base = '/ide/' + encodeURIComponent(session.id) + '/';
  for (let attempt = 0; attempt < 60 && current(); attempt += 1) {
    try {
      const response = await fetch(base + 'healthz', { cache: 'no-store', signal: AbortSignal.timeout(3000) });
      if (response.ok) return current() ? base : '';
    } catch { /* code-server may still be starting */ }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  if (current()) throw new Error('VS Code did not become ready. Check the session log or retry.');
  return '';
}

// ideQuery is what an editor is opened on: its folder, or - for the empty
// editor, whose directory is only somewhere for its terminals to start -
// code-server's empty window.
function ideQuery(session) {
  if (session.workdir_mode === 'dynamic') return '?ew=true';
  return '?folder=' + encodeURIComponent(session.workdir);
}

async function openIDE(session, generation) {
  if (['failed', 'exited'].includes(session.state)) return;
  const current = () => generation === ideGeneration;
  // What stands on the stage until there is an editor on it: the shape loader
  // and a line of words, the same shape the pane's own cover uses for a
  // session that is coming back.
  const words = el('p', { text: 'Opening VS Code…' });
  const spinner = el('span', { class: 'ds-loader', 'aria-hidden': 'true' });
  const loading = el('div', { class: 'so-ide__loading', role: 'status' }, spinner, words);
  dom.term.replaceChildren(loading);
  try {
    const base = await readyIDE(session, current);
    if (!base) return;
    const frame = el('iframe', {
      class: 'so-ide__frame', title: 'VS Code',
      src: base + ideQuery(session),
      allow: 'clipboard-read; clipboard-write',
    });
    // The card stays on top of the frame rather than being replaced by it.
    // `healthz` answers as soon as code-server's HTTP server is listening, and
    // the workbench is ten more seconds of white after that - which is the one
    // thing on this page that reads as broken.
    words.textContent = 'Loading the editor…';
    dom.term.replaceChildren(frame, loading);
    // The editor's fields are under the keyboard switch like the page's.
    watchFrame(frame);
    onStatus('live');
    revealIDE(frame, loading, current);
  } catch (err) {
    if (!current()) return;
    // A mark that turns is a promise that something is still happening, and
    // nothing is.
    spinner.remove();
    words.textContent = errorMessage(err);
    loading.append(el('button', { class: 'ds-btn', type: 'button', text: 'Retry', onclick: () => attach(sessionOf(session.id) || session) }));
  }
}

// How long the card waits for a workbench that never arrives. Past this the
// frame is uncovered whatever is in it: a page that is going to be white is
// better white than behind a mark that turns for ever.
const IDE_REVEAL_CEILING = 30000;

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
  // Faded out rather than removed at once, so the workbench arrives instead
  // of replacing something.
  const uncover = () => {
    if (!loading.isConnected) return;
    M.leave(loading);
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

// The key bar follows touch mode until this device has said otherwise: the
// two devices a guess gets wrong - a tablet in a case, a laptop with a touch
// screen - are what the options menu is for, and the answer is kept per
// device.

// applyKeyBar shows the bar or puts it away as this device now wants it, on
// the session on screen - when that session has a bar at all: an editor and
// a chat do not, and get it when a terminal is next opened.
function applyKeyBar() {
  if (state.keybar) showKeyBar(keyBarWanted());
}

// placeToasts keeps the snackbars off what a finger needs, and an error
// stays until it is closed. DS places them in an open side sheet or dialog
// (and keeps room there for a message that stays), beside the open modal
// drawer or above its foot (.ds-drawer__foot), and above the on-screen
// keyboard (--ds-keyboard-inset, keybar.js followViewport). What stands at
// the bottom of this page is Socrates' own (DS --ds-toast-bottom):
//   - the key bar shown: above it - a message over Esc and Ctrl is a key bar
//     a thumb cannot use;
//   - a chat shown: above its dock, and above "Latest" (chatview.js says
//     how much room it needs), and while a message that stays shows, the
//     conversation keeps its height at its end, so that the last answer and
//     its Copy can scroll clear of it.
// The view of this page does not scroll, so it keeps none of DS's room
// (data-ds-toast-room="off", index.html). Above the drawer's foot on a phone the session list
// keeps the messages' height at its end, so its last rows scroll clear of
// them. Heights only, never where something stands, which a keyboard moves
// before or after this runs. Every edge holds the safe area already, which
// the region adds as its margin.
let chatToastRoom = 0;
const TOASTS_BESIDE = window.matchMedia('(min-width: 601px)');
function placeToasts() {
  const region = document.getElementById('toasts');
  if (!region) return;
  const hosted = !!region.closest('.ds-sheet, dialog.ds-dialog[open]');
  const drawer = dom.app.hasAttribute('data-drawer-open') && drawerModal();
  // The messages' height: nothing while there are none.
  const stack = region.querySelector('.ds-toast') ? region.offsetHeight : 0;
  let room = 0;
  if (hosted || drawer) room = 0;
  else if (!dom.keybar.hidden && dom.keybar.offsetHeight) room = dom.keybar.offsetHeight;
  else if (chatToastRoom) room = chatToastRoom;
  const value = room ? 'calc(' + room + 'px + var(--ds-space-3) - env(safe-area-inset-bottom, 0px))' : '';
  if (region.style.getPropertyValue('--ds-toast-bottom') !== value) {
    if (value) region.style.setProperty('--ds-toast-bottom', value);
    else region.style.removeProperty('--ds-toast-bottom');
  }
  // The drawer's list keeps its room under a dialog opened from it (a
  // row's ⋮), rather than jumping under the scrim.
  const list = drawer && !hosted && !TOASTS_BESIDE.matches ? stack : 0;
  const reserve = list ? list + 'px' : '';
  if (dom.sidebar.style.getPropertyValue('--so-drawer-reserve') !== reserve) {
    if (reserve) dom.sidebar.style.setProperty('--so-drawer-reserve', reserve);
    else dom.sidebar.style.removeProperty('--so-drawer-reserve');
  }
  // A chat on screen with a message that stays over its end: the
  // conversation is that much longer (so-chat.css --so-chat-reserve), and
  // one read at its end stays there (chatview.js).
  const chatReserve = chatToastRoom && stack && toastsStay() ? 'calc(' + stack + 'px + var(--ds-space-3))' : '';
  if (dom.chat.style.getPropertyValue('--so-chat-reserve') !== chatReserve) {
    if (chatReserve) dom.chat.style.setProperty('--so-chat-reserve', chatReserve);
    else dom.chat.style.removeProperty('--so-chat-reserve');
  }
}

function showKeyBar(on) {
  dom.keybar.hidden = !on;
  placeToasts();
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
    setTitleText(title);
  }
  renderList();
}

// Whether the server is being asked if there is a key to name with.
let regenAsking = false;

// drawTitling shows in the header whether the session on screen is being
// named: the name pulses and the arrow beside it turns. An editor is named
// after its folder, so it has no arrow.
function drawTitling(id) {
  const titling = state.titling.has(id);
  dom.sessionTitle.toggleAttribute('data-titling', titling);
  dom.titleRegen.hidden = !state.current || state.current.harness === IDE;
  // While a name is being thought of - or the server is asked whether there
  // is a key to think of one with - the sparkles turn into a spinner and a
  // second press does nothing (wire).
  if (titling || regenAsking) dom.titleRegen.setAttribute('aria-busy', 'true');
  else dom.titleRegen.removeAttribute('aria-busy');
  // A name is thought of by a model on OpenRouter: without a key the tip
  // says why a press only explains, as Status's and Speak's do.
  const tip = voiceKey() === false ? 'Needs an OpenRouter key (Admin, Voice)' : 'Name this session again from what it is about';
  if (dom.titleRegen.getAttribute('data-tip') !== tip) dom.titleRegen.setAttribute('data-tip', tip);
}

// renameAgain is the sparkles' press: a new name for the session on screen,
// or - with no OpenRouter key, asked again first - the dialog that says
// where one is added, before anything is sent.
async function renameAgain() {
  const session = state.current;
  // While a name is being thought of the button is busy, and DS's guard
  // keeps its presses from here.
  if (!session || regenAsking) return;
  if (voiceKey() === false) {
    regenAsking = true;
    drawTitling(session.id);
    let missing = false;
    try {
      missing = await keyMissing();
    } finally {
      regenAsking = false;
      if (state.current) drawTitling(state.current.id);
    }
    if (missing) {
      explainNoKey('title', dom.titleRegen);
      return;
    }
  }
  retitleSession(session);
}

// retitleSession asks for a new name for the session on screen, from what it
// is about: its first prompt and its screen. The answer is the renamed
// session; other browsers hear of it in title frames.
async function retitleSession(session) {
  if (!session || state.titling.has(session.id)) return;
  // The arrow turns from the press, not from the first frame back.
  state.titling.add(session.id);
  state.namingHere.add(session.id);
  if (state.current && state.current.id === session.id) drawTitling(session.id);
  renderList();
  try {
    const data = await api('/api/sessions/' + encodeURIComponent(session.id) + '/title', {
      method: 'POST', attempts: 1, timeout: 45000,
      body: {},
    });
    if (data && data.session) replaceSession(data.session);
  } catch (err) {
    if (err && Number(err.status) === 409 && /openrouter api key/i.test(String(err.message || ''))) {
      // The server has no key to name it with (a key taken away since the
      // page knew of one): said the way the press says it, with Admin.
      setVoiceKey(false);
      explainNoKey('title', dom.titleRegen);
    } else alertDialog({ title: 'Could not name the session', body: errorMessage(err) });
  } finally {
    state.titling.delete(session.id);
    state.namingHere.delete(session.id);
    if (state.current && state.current.id === session.id) drawTitling(session.id);
    renderList();
  }
}

// How long a naming can go without its end being heard before the page
// stops waiting for it: the server's titleTimeout (30 s) and a margin.
const TITLE_CEILING = 45000;

// titleFrame follows the automatic naming of a session: the name pulses from
// the moment the model is asked until it has answered, and an answer that is
// a failure is said in a dialog - a name that silently never arrives is a
// sidebar nobody can explain.
function titleFrame(frame) {
  const id = frame.id;
  if (!id) return;
  const seq = Number(frame.seq) || 0;
  if (seq) {
    if (seq <= state.titleSeq) return;
    state.titleSeq = seq;
  }
  if (frame.phase === 'start') {
    state.titling.add(id);
    // A naming is over within titleTimeout (30 s on the server). One whose
    // end was never heard - a server that restarted under it, a frame sent
    // while this page's socket was down - stops pulsing after that and a
    // margin, rather than for as long as the page is open.
    setTimeout(() => {
      if (state.titleStarts.get(id) === seq) forgetTitling([id]);
    }, TITLE_CEILING);
    state.titleStarts.set(id, seq);
  } else {
    state.titling.delete(id);
    state.titleStarts.delete(id);
  }
  if (state.current && state.current.id === id) drawTitling(id);
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

// forgetTitling lets go of namings whose end this page may never hear. The
// event stream's hello is the whole state again, and nothing in it says a
// session is being named: a frame lost while the stream was down, or a
// naming a restarted server dropped, would otherwise pulse - and keep the
// arrow beside the name disabled - until the page is reloaded. A naming
// still going says so again with its done frame, which renames the row.
// What this page asked for itself is settled by its own request.
function forgetTitling(ids = [...state.titling]) {
  let moved = false;
  for (const id of ids) {
    if (state.namingHere.has(id) || !state.titling.has(id)) continue;
    state.titling.delete(id);
    state.titleStarts.delete(id);
    moved = true;
  }
  if (!moved) return;
  if (state.current) drawTitling(state.current.id);
  renderList();
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
  // A verdict is about whichever session it names, as a title is.
  if (frame.t === 'outcome') { if (frame.id) outcomeFrame(frame.id, frame.outcome || null); return; }
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
        // A terminal has no conversation to lose: its resume is a new shell
        // in the same folder, and that is all the banner has to say.
        notice('resumed', 'Resumed after a restart.', () => {
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
      // A composed line is given back in full rather than counted: it was
      // spoken once, and a person who is not looking at the screen has no
      // other way to find out that it never arrived.
      for (const line of frame.lines || []) {
        toast('That was not typed into the terminal: “' + line + '”', 'error');
      }
      break;
    case 'error':
      // The terminal this tab is deleting or filing as done is closed by
      // that very request; the tab's own toast tells of it.
      if (frame.fatal && state.ending.has(sessionId)) break;
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
  // A chat keeps its own reckoning of its stream (chatview.js), by the same
  // grace.
  const stale = chatShown() ? state.chat.stale() : !state.live
    && (navigator.onLine === false || Date.now() - (state.lostAt || Date.now()) >= CONNECTION_GRACE);
  document.body.toggleAttribute('data-stale', stale);
  dom.termWrap.toggleAttribute('data-stale', stale);
}

/* --------------------------------------------------------------- routing */

// A run of taps down the list is one decision, not six. Opening a socket per
// tap leaves the last one queued behind five handshakes it no longer wants, so
// the attach waits for the tapping to stop; the row highlights immediately,
// which is what the tap was asking for.
function selectSession(id) {
  const session = sessionOf(id);
  if (!session) return;
  // Already open.
  const chatOpen = !!state.chat && state.chat.shownId() === id;
  if (state.current && state.current.id === id && (chatOpen
    || state.socket || dom.stage.hasAttribute('data-ide'))) return;
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

// measureForStart is measureNewPane from wherever the page is: a chat or
// Home stands where the pane would be, so the pane is put
// back for the length of the measurement - one synchronous step, never
// painted.
function measureForStart() {
  if (state.term) return state.term.size();
  const chatWas = dom.chat.hidden;
  const homeWas = dom.home.hidden;
  const wrapWas = dom.termWrap.hidden;
  dom.chat.hidden = true;
  dom.home.hidden = true;
  dom.termWrap.hidden = false;
  try {
    return measureNewPane();
  } finally {
    dom.chat.hidden = chatWas;
    dom.home.hidden = homeWas;
    dom.termWrap.hidden = wrapWas;
  }
}

async function newSession(workdir = '') {
  const pick = await harnesses.openNewSessionSheet({ workdir });
  if (!pick) return;
  // On a phone the drawer is what "New session" was tapped in, and the thing
  // being started is behind it. Leaving it open puts a list over the terminal
  // that was just asked for.
  closeNav();
  if (pick.chat) {
    await startChat(pick);
    return;
  }
  // A Shell: a folder, and the size of the pane it is about to be drawn in,
  // measured in the terminal's font.
  await loadTerminalFonts();
  const size = measureForStart();
  try {
    const data = await api('/api/sessions', {
      method: 'POST',
      attempts: 2,
      timeout: 60000,
      body: {
        client_id: clientKey(),
        harness: pick.harness,
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

// startChat makes a chat (AGENT-HOST.md §7): its host comes up at once, so
// the first message finds the agent warm, and the chat takes the stage with
// the cursor in its composer. The client_id makes a create retried over a
// link that dropped the same chat.
async function startChat(pick) {
  try {
    const data = await api('/api/chats', {
      method: 'POST',
      attempts: 2,
      timeout: 30000,
      body: {
        client_id: clientKey(),
        harness: pick.harness,
        model: pick.model,
        effort: pick.effort,
        ultracode: pick.ultracode || undefined,
        account: pick.account || undefined,
        auto_switch: pick.auto_switch || undefined,
        mcp: pick.mcp && Object.keys(pick.mcp).length ? pick.mcp : undefined,
        workdir_mode: pick.workdir_mode,
        workdir: pick.workdir,
      },
    });
    replaceSession(data.session);
    state.wanted = data.session.id;
    location.hash = '#' + data.session.id;
    attach(data.session);
    if (state.chat) state.chat.focus();
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

// The drawer is modal at 900px and below: DS.overlay opens and closes it,
// with its scrim, its focus trap and Escape. On a wider window it is docked
// and these do nothing.
function drawerModal() { return window.matchMedia('(max-width: 900px)').matches; }
function openNav() {
  if (drawerModal() && !dom.app.hasAttribute('data-drawer-open')) showOverlay(dom.sidebar, dom.menuBtn);
}
function closeNav() {
  if (dom.app.hasAttribute('data-drawer-open')) hideOverlay(dom.sidebar);
}

// drawerCue shades the drawer's list at a cut edge while more rows lie
// beyond it: on a phone the list was cut through the second line of a name
// above Costs, with nothing to say it scrolls. The shades are DS's
// (.ds-drawer__list); where the browser has no scroll timelines they read
// DS.dom.scrollEdges, which measures again on a scroll and whenever a child
// of the list changes size - the session list grows and shrinks with every
// session and folder opened or shut.
function drawerCue() {
  const list = document.getElementById('drawerList');
  if (list) DS.dom.scrollEdges(list, { axis: 'block' });
}

// wireList is the session list's and the row menu's listeners, bound once:
// the rows are drawn again all the time, and a kept node keeps nothing a
// listener of its own would need.
function wireList() {
  drawerCue();
  dom.sessionList.addEventListener('click', (event) => {
    const more = event.target.closest('.so-row__more');
    if (more) {
      event.preventDefault();
      openRowMenu(more, more.closest('.so-row').dataset.id);
      return;
    }
    const ide = event.target.closest('.so-folder__ide');
    if (ide) { openFolderIDE(ide.dataset.ide); return; }
    const add = event.target.closest('.so-folder__new');
    if (add) { newSession(add.dataset.new); return; }
    const head = event.target.closest('.so-folder__head');
    if (head) {
      const key = keysOfRows.get(head.dataset.toggle);
      if (key !== undefined) toggleFolder(key);
      return;
    }
    const open = event.target.closest('.so-row__open');
    // A row is a link, so a middle or modified click opens the session in a
    // tab of its own; a plain one is the page's.
    if (!open || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    selectSession(open.closest('.so-row').dataset.id);
  });
  // ArrowDown on a ⋮ opens its menu on the first item, ArrowUp on the last.
  dom.sessionList.addEventListener('keydown', (event) => {
    const more = event.target.closest('.so-row__more');
    if (!more || (event.key !== 'ArrowDown' && event.key !== 'ArrowUp')) return;
    if (more.getAttribute('aria-expanded') === 'true') return;
    event.preventDefault();
    openRowMenu(more, more.closest('.so-row').dataset.id);
  });
  // The row menu has no close event: its hidden attribute is the news.
  new MutationObserver(() => {
    if (!dom.sessionMenu.hidden) return;
    requestAnimationFrame(() => paneAfter(document.activeElement));
  }).observe(dom.sessionMenu, { attributes: true, attributeFilter: ['hidden'] });
  dom.sessionMenu.addEventListener('click', (event) => {
    const item = event.target.closest('[data-action]');
    // A blocked item's press never gets here (DS's guard).
    if (!item) return;
    const run = menuActions.get(item.dataset.action);
    if (run) run();
  });
}

/**
 * paneAfter hands the focus back to the pane once a dialog or the row menu
 * has gone. DS puts it back on the button that opened it, and with a session
 * open a button with the focus swallows the keys meant for the pane - the
 * menus did the same before they were DS's.
 */
function paneAfter(button) {
  const settle = () => {
    if (!state.term || homeShown() || overlayOpen()) return;
    const now = document.activeElement;
    if (now && now !== document.body && now !== button) return;
    if (button && now === button) {
      if (!button.matches('.so-row__more, #sessionTitle, #optionsBtn')) return;
      button.blur();
    }
    keepTypingAlive();
  };
  // Once now, and once DS has had its frame to put the focus back.
  settle();
  requestAnimationFrame(settle);
}

// setTitleText writes the name in the bar. The button holds the name in a
// span of its own, so writing the button's text would throw the span away.
function setTitleText(text) {
  const host = dom.sessionTitleText || dom.sessionTitle;
  if (host.textContent !== text) host.textContent = text;
  // The name may be cut on a phone; the tip has all of it. A tap renames it
  // (the session's menu says so too). Home has no name to say again.
  const button = dom.sessionTitle;
  if (state.current && text) {
    button.setAttribute('data-tip', text);
    button.setAttribute('aria-description', 'Rename');
  } else {
    button.removeAttribute('data-tip');
    button.removeAttribute('aria-description');
  }
  // Home has nothing to rename: its words stand in the bar as words, not as
  // a button that a tap does nothing with.
  const home = document.getElementById('homeTitle');
  if (home) {
    if (home.textContent !== text) home.textContent = text;
    home.hidden = !!state.current;
    button.hidden = !state.current;
  }
  drawBarMenu();
}

// The status chip beside the name: where the person filed the session, as an
// icon and a word. In progress says nothing, so it has no chip.
const STATUS_CHIP = { review: '', pending: 'ds-status--warning', background: '', done: 'ds-status--ok' };
function drawStatusChip(status) {
  const chip = dom.sessionStatus;
  chip.hidden = status === 'in_progress';
  chip.dataset.status = status;
  if (chip.hidden) return;
  chip.className = ('ds-status ' + (STATUS_CHIP[status] || '')).trim();
  // On a phone the chip is its glyph alone (so-frame.css): the words are its
  // name and its tip there, as on the list's rows.
  chip.setAttribute('role', 'img');
  chip.setAttribute('aria-label', STATUS_WORDS[status]);
  chip.dataset.tip = STATUS_WORDS[status];
  morph(chip, html`${raw(icon(STATUS_GLYPH[status], 16))}<span>${STATUS_WORDS[status]}</span>`);
}

/* ------------------------------------------- the session list put away */

// Whether this device keeps the session list put away at a desk. It is a fact
// about this screen and not about the account: the same person wants the
// column on a desk and the width on a laptop, and a preference that travelled
// between them would be wrong on one of them every time.
const SIDE_KEY = 'socrates.sidebar';

// Below the drawer breakpoint the list is a modal drawer, the toggle is not
// on the page, and the list is never put away: a drawer that is hidden is a
// menu button that opens nothing.
const railAllowed = () => window.matchMedia('(min-width: 901px)').matches;

function sidebarCollapsed() { return dom.app.hasAttribute('data-drawer-hidden'); }

function showSidebar(collapsed, remember = true) {
  const want = collapsed && railAllowed();
  const was = sidebarCollapsed();
  if (want !== was) {
    dom.app.toggleAttribute('data-drawer-hidden', want);
    // Coming back, the drawer slides in from the left (DS App frame).
    if (!want) M.enter(dom.sidebar);
  }
  if (dom.sideCollapse) {
    dom.sideCollapse.setAttribute('aria-expanded', want ? 'false' : 'true');
    const words = want ? 'Show the session list' : 'Hide the session list';
    dom.sideCollapse.setAttribute('data-tip', words);
    dom.sideCollapse.setAttribute('aria-label', words);
  }
  if (remember) {
    try { localStorage.setItem(SIDE_KEY, want ? 'rail' : 'wide'); } catch { /* private window */ }
  }
  // The pane is measured in characters, so it is fitted again once the
  // panel has its new width; the terminal follows its own size from there.
  if (want !== was) requestAnimationFrame(() => { if (state.term) state.term.refit(); });
}

function restoreSidebar() {
  let saved = '';
  try { saved = localStorage.getItem(SIDE_KEY) || ''; } catch { /* private window */ }
  // The state is applied without writing it back: a phone that is too narrow
  // for it must not overwrite what the desk it syncs nothing with chose.
  showSidebar(saved === 'rail', false);
  // A window that crosses the drawer breakpoint brings the list back, and
  // puts it away again on the way back if that is what this device asked for.
  window.matchMedia('(min-width: 901px)').addEventListener('change', () => {
    let want = '';
    try { want = localStorage.getItem(SIDE_KEY) || ''; } catch { /* private window */ }
    showSidebar(want === 'rail', false);
  });
}

/* ----------------------------------------------------------------- start */

function wire() {
  dom.newSession.addEventListener('click', () => newSession());
  dom.blankIDE.append(agentMark(IDE, 18));
  dom.blankIDE.addEventListener('click', openBlankIDE);
  dom.menuBtn.addEventListener('click', () => {
    if (dom.app.hasAttribute('data-drawer-open')) closeNav(); else openNav();
  });
  if (dom.sideCollapse) {
    dom.sideCollapse.addEventListener('click', () => showSidebar(!sidebarCollapsed()));
  }
  // The top bar's ⋮: the menu of the session on the stage - in the chat's or
  // the terminal's own header, where it is looked for - with this device's
  // options as its last item; on Home, this device's options.
  dom.optionsBtn.addEventListener('click', (event) => {
    const button = event.currentTarget;
    const session = barMenuSession();
    if (session) openRowMenu(button, session.id, { device: true });
    else openOptions(button);
  });
  // The two buttons say whether their dialog is open: DS.overlay keeps
  // aria-expanded for menus, and a dialog's opener says it here.
  // Once one has gone, DS has put the focus back on its button - and a
  // button with the focus swallows the keys meant for the pane, so with a
  // session open the pane takes it back, as it does after a menu.
  const backToPane = (dialog, button) => dialog.addEventListener('close', () => {
    button.setAttribute('aria-expanded', 'false');
    // At once, not after the exit has played: what is typed meanwhile is
    // for the pane too.
    paneAfter(button);
    hideOverlay(dialog).then(() => paneAfter(button));
  });
  backToPane(dom.optionsDialog, dom.optionsBtn);
  dom.optionsDialog.addEventListener('change', (event) => {
    const input = event.target.closest('[data-option]');
    if (input) setOption(input.dataset.option, input.checked);
  });
  wireList();
  // The name is edited where it is read.
  dom.sessionTitle.addEventListener('click', () => {
    if (state.current) renameSession(state.current, dom.sessionTitle);
  });
  dom.titleRegen.addEventListener('click', () => renameAgain());
  onVoiceKey(() => { if (state.current) drawTitling(state.current.id); });
  dom.sessionOutcome.addEventListener('click', () => showOutcome());
  // The terminal's on-screen keyboard, which on a touch screen this button
  // raises and puts away and nothing else does. Every other field - a chat's
  // composer, a dialog's - raises it by a tap.
  initKeyboard(dom.kbBtn, () => (state.term ? state.term.term.textarea : null));
  dom.logout.addEventListener('click', async () => {
    if (notifier) await notifier.signOut();
    // What this browser kept of the chats is not left for whoever signs in
    // next.
    await forgetAll();
    forgetAllSent();
    try { await api('/api/logout', { method: 'POST' }); } catch { /* going anyway */ }
    location.href = '/login';
  });
  // Home is a link, so a middle click opens it in a tab of its own; a plain
  // click is the page's.
  dom.homeBtn.addEventListener('click', (event) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (homeShown()) closeNav(); else showHome();
  });
  window.addEventListener('hashchange', () => {
    const route = routeOf(location.hash);
    if (route.kind === 'home') {
      if (!homeShown()) showHome();
      return;
    }
    if (route.kind === 'question') {
      questionOnHome(route.id, { replace: true });
      return;
    }
    if (route.kind === 'questions') {
      questionsOnHome({ replace: true });
      return;
    }
    openOnNews(route.id);
  });
  // The page coming back into view - unlocked, switched to, restored from
  // the back-forward cache - is where the app's badge is put right: pushes
  // may have written it while the page was away. It is written once the
  // page holds a state from after it came back, which for a page that slept
  // is the stream's next hello, and for one that did not the pong to a ping.
  const back = () => {
    if (document.visibilityState !== 'visible') return;
    badgeWrittenElsewhere();
    // A chat that finished a turn while the page was away is read now.
    if (chatShown()) markRead(state.chat.shownId());
  };
  document.addEventListener('visibilitychange', back);
  window.addEventListener('pageshow', back);
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
    if (active && active.closest && active.closest('input, textarea, button, dialog, .ds-menu')
      && !active.classList.contains('xterm-helper-textarea')) return;
    state.term.focus();
  });
  window.addEventListener('online', updateStale);
  window.addEventListener('offline', updateStale);
  setInterval(updateStale, 1000);
  // The list is not polled here any more: the event stream carries it, and
  // while the stream is down it polls the same state itself (events.js), on
  // a wake as well as on a timer.
}

/**
 * routeOf reads what an address asks for (HOME.md §2.3): Home for `#home` and
 * for nothing at all, the question dialog for `#q=<id>`, the dialog at the
 * oldest question for `#questions` (the app's shortcut), and a session for
 * `#open=<id>` - the form a notification opens a cold tab with - or `#<id>`.
 */
function routeOf(hash) {
  const raw = String(hash || '').replace(/^#/, '');
  if (!raw || raw === 'home') return { kind: 'home', id: '' };
  if (raw === 'questions') return { kind: 'questions', id: '' };
  let id = raw;
  let kind = 'session';
  if (raw.startsWith('q=')) { kind = 'question'; id = raw.slice(2); }
  else if (raw.startsWith('open=')) { kind = 'open'; id = raw.slice(5); }
  try { id = decodeURIComponent(id); } catch { /* an id is never escaped by this app */ }
  if (!id) return { kind: 'home', id: '' };
  return { kind, id };
}

async function boot() {
  // The terminal's font is on its way meanwhile: a terminal waits for it
  // (attach), the rest of the app is drawn without it (term.js).
  loadTerminalFonts();
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
    // A chat is spoken into through its composer.
    current: () => state.current,
    live: () => (chatShown() ? state.chat.live() : state.live),
    insert: (text) => (chatShown() ? state.chat.insert(text) : insertIntoTerm(text)),
    // The ticker is over the pane of a terminal and in the dock of a chat.
    ticker: () => (chatShown() ? state.chat.ticker() : { box: dom.termTicker, line: dom.tickerWindow }),
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
  // session finished, and their two switches are in the options menu whether
  // or not anything is attached.
  notifier = mountNotify({
    sessionOf,
    // "Finished" opens the session, as its address would - also in a tab
    // that does not hold the list yet.
    select: (id) => openOnNews(id),
    // "Needs an answer" opens the question itself, on Home, the way the
    // address `#q=` does: the dialog has its own way into the session.
    question: (id) => questionOnHome(id),
    // A turn's end whose verdict is being read waits for it (outcome.js).
    outcome: (id) => state.outcomes.get(id) || null,
    questionOf: (id) => {
      const digest = state.digests.get(id);
      return (digest && digest.question) || null;
    },
    // A chat's notification says what its push says, from its reading.
    chatNews: (id) => {
      if (!isChat(sessionOf(id))) return undefined;
      const digest = state.digests.get(id);
      return digest ? String(digest.news || '') : undefined;
    },
    // A permission the browser answers late lands in a menu still open.
    changed: () => syncOptions(),
    // A push wrote the icon and the tray from the set it was counted with;
    // this page may know a newer one.
    pushed: () => badgeWrittenElsewhere(),
  });
  // The chat view, for every session that is a chat. What a chat was told
  // while it was not on screen - or before a reload - is on its way from the
  // first moment, whatever is on the stage.
  state.chat = mountChat({
    host: dom.chat,
    sessionOf: (id) => sessionOf(id),
    status: () => updateStale(),
    openAsk: (chatId, askId) => showChatQuestions(chatId, { askId, only: true }),
    toastRoom: (room) => { chatToastRoom = room; placeToasts(); },
  });
  chatOutbox().pump();
  // Home, drawn from the sidebar's own state and tests.
  state.home = mountHome({
    host: dom.home,
    ...homeCtx(),
    words: { state: STATE_WORDS, activity: ACTIVITY_WORDS },
    connection: streamConnection,
    serverNow: () => (state.events ? state.events.serverNow() : Date.now()),
    reachable: () => state.reachable,
    loading: () => state.loading,
    select: (id) => selectSession(id),
    newSession: () => newSession(),
    openQuestions: (id) => showQuestions(id),
    // A chat opened on its background jobs: the dialog opens once the chat
    // has been read (chatview.js openJobs).
    openJobs: (id) => {
      selectSession(id);
      if (state.chat) state.chat.openJobs(id);
    },
  });
  followViewport();
  // On a short screen the drawer's foot is one row of its icons alone (DS
  // .ds-drawer__foot--rail), so that the session list keeps the height:
  // their names, which are their labels anyway, are their tips as well.
  const shortDrawer = window.matchMedia('(max-height: 500px)');
  const footTips = () => {
    const foot = dom.sidebar.querySelector('.ds-drawer__foot');
    if (foot) foot.classList.toggle('ds-drawer__foot--rail', shortDrawer.matches);
    for (const item of dom.sidebar.querySelectorAll('.ds-drawer__foot > .ds-drawer__item')) {
      if (shortDrawer.matches) item.setAttribute('data-tip', item.getAttribute('aria-label') || item.textContent.trim());
      else item.removeAttribute('data-tip');
    }
  };
  shortDrawer.addEventListener('change', footTips);
  footTips();
  // A key bar that changes height - a phone turned on its side - takes the
  // snackbars with it, and so do a keyboard that comes or goes, the drawer
  // opened or closed, and a message more or less (the drawer's reserve).
  if (window.ResizeObserver) {
    new ResizeObserver(() => placeToasts()).observe(dom.keybar);
    const toasts = document.getElementById('toasts');
    if (toasts) new ResizeObserver(() => placeToasts()).observe(toasts);
  }
  window.addEventListener('resize', () => placeToasts());
  if (window.visualViewport) window.visualViewport.addEventListener('resize', () => placeToasts());
  new MutationObserver(() => placeToasts()).observe(dom.app, { attributes: true, attributeFilter: ['data-drawer-open'] });
  // DS moves the region into a sheet that opens, and back; into a dialog
  // as it opens, and back once it has gone.
  new MutationObserver(() => placeToasts()).observe(document.body, { childList: true });
  new MutationObserver((records) => { if (records.some((r) => r.target.localName === 'dialog')) placeToasts(); })
    .observe(document.body, { attributes: true, attributeFilter: ['open'], subtree: true });
  // A message that stays comes or goes: the conversation's room with it.
  onToastsStay(() => placeToasts());
  // Whether the backup has worked in the last day. Silent while it has.
  mountBackupNotice(document.getElementById('backupNotice'));
  // Read before anything is fetched. Home is the landing page whatever the
  // address was (HOME.md §2.3), except for the two forms a notification
  // opens a tab with - a question, and a session - and the app's
  // "Questions" shortcut, which is Home with the dialog over it.
  const route = routeOf(location.hash);
  state.loading = true;
  showHome({ replace: true });
  // The catalogue decides whether a folder offers VS Code, so the list is
  // drawn again once it is in.
  harnesses.load().then(() => renderList()).catch(() => { /* the sheet says so when it is opened */ });
  try {
    const prefs = await api('/api/preferences', { attempts: 1, timeout: 8000 });
    if (prefs && prefs.terminal) state.terminal = { ...state.terminal, ...prefs.terminal };
    // Whether there is an OpenRouter key, for the microphones and Status to
    // say so before anything is recorded (voice.js), when the server tells.
    if (prefs && typeof prefs.openrouter === 'boolean') setVoiceKey(prefs.openrouter, { told: true });
    // And whether Text-to-Speech has one, for Read aloud (askdialog.js).
    if (prefs && typeof prefs.tts === 'boolean') setTtsKey(prefs.tts, { told: true });
  } catch { /* the defaults above are the shipped ones */ }
  // A session a notification asked for is wanted before the list is read, so
  // that an offline start opens it the moment the list can prove it exists.
  // From here on `openWanted` is the only thing that attaches on its own, so
  // there is one rule about when that is allowed and one place it lives.
  if (route.kind === 'open') state.wanted = route.id;
  await refreshList();
  // The stream from here on: the list, the activity, the digests and the
  // answers, live, whatever is on the stage.
  state.events = mountStream();
  state.events.start();
  drawLive();
  // Only if the person is still where boot put them: "New session" works
  // from the first line of boot, and a session started while the list was
  // being fetched is not torn down for the dialog (openWanted's rule).
  if (!homeShown() || state.current || state.socket || state.pendingAttach) return;
  if (route.kind === 'question') questionOnHome(route.id);
  else if (route.kind === 'questions') questionsOnHome({ replace: true });
}

boot();
