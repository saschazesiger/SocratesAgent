// The chat stream: one open chat, GET /api/chats/{id}/stream
// (socrates.chat.v1, AGENT-HOST.md §7).
//
// It is the event stream's rules (events.js) for a stream that carries a
// conversation: a socket is not live until the replay it asked for with
// hello{since} is through (`synced`); a socket that has said nothing for a
// while is asked with a ping, and one that does not answer is opened again;
// the backoff starts from the beginning whenever the radio or the screen
// comes back; a hidden page keeps its socket for half a minute; and 4401 is a
// login that is gone. Every reconnect asks for exactly the events after the
// last one the page holds, so nothing is missed and nothing comes twice.
//
// It speaks for the connection bar, as the terminal socket does: what the
// person is reading is this conversation, and when it stops being current the
// page has to say so.

import { api } from './api.js';
import { connectionSource, onWake, HttpError } from './net.js';

const PROTOCOL = 'socrates.chat.v1';
const SIGNED_OUT = 4401;
const BACKOFF_BASE = 500;
const BACKOFF_MAX = 8000;
const HELLO_TIMEOUT = 10000;
// The server ticks every fifteen seconds. A socket that has been silent for
// PROBE_AFTER is pinged, and one whose ping is not answered within
// PONG_TIMEOUT is dead, whatever the browser thinks.
const PROBE_AFTER = 10000;
const PONG_TIMEOUT = 5000;
const WATCH_EVERY = 2000;
const WAKE_FRESH = 20000;
const HIDDEN_GRACE = 30000;

/**
 * ChatStream keeps one chat's stream open while it is wanted.
 *
 *   since()          the last seq the page holds, read on every connect
 *   onFrame(frame)   every frame: hello, ev, delta, synced
 *   onStatus(status) 'connecting' | 'live' | 'offline', whenever it moves
 *   lite             the replay may leave out what is drawn only once it is
 *                    opened (agent.Lighten): the page asks for it then
 */
export class ChatStream {
  constructor({ chatId, since, onFrame, onStatus, global = true, lite = false }) {
    this.chatId = chatId;
    this.since = since;
    this.lite = lite;
    this.onFrame = onFrame;
    this.onStatus = onStatus || (() => {});
    this.ws = null;
    this.stopped = true;
    this.attempt = 0;
    this.state = 'connecting';
    this.heardAt = 0;
    this.pingId = 0;
    this.timers = {};
    // A stream a dialog keeps for a question is not what the page is
    // showing, and does not speak for the connection bar (global false).
    this.conn = connectionSource({ global });
    this.conn.onOffline(() => { if (!this.stopped) this.reconnect(0); });
    this.unwake = null;
    this.onVisible = () => this.visibility();
  }

  start() {
    if (!this.stopped) return;
    this.stopped = false;
    this.unwake = onWake(() => this.wake());
    document.addEventListener('visibilitychange', this.onVisible);
    this.timers.watch = setInterval(() => this.watch(), WATCH_EVERY);
    this.open();
  }

  stop() {
    this.stopped = true;
    this.close();
    clearInterval(this.timers.watch);
    clearTimeout(this.timers.hidden);
    document.removeEventListener('visibilitychange', this.onVisible);
    if (this.unwake) { this.unwake(); this.unwake = null; }
    this.conn.release();
  }

  /** status is how the stream is: 'live' only once its replay is through. */
  status() { return this.state; }

  live() { return this.state === 'live'; }

  setStatus(next) {
    const value = navigator.onLine === false && next !== 'live' ? 'offline' : next;
    if (value === this.state) return;
    this.state = value;
    this.conn.report(value === 'live' ? 'live' : 'connecting', {
      immediate: value === 'offline', retryNow: () => this.reconnect(0),
    });
    try { this.onStatus(value); } catch (err) { setTimeout(() => { throw err; }, 0); }
  }

  url() {
    const base = location.protocol === 'https:' ? 'wss://' : 'ws://';
    return base + location.host + '/api/chats/' + encodeURIComponent(this.chatId) + '/stream';
  }

  open() {
    this.close();
    if (this.stopped) return;
    if (document.visibilityState !== 'visible') return;
    if (navigator.onLine === false) {
      this.setStatus('offline');
      this.timers.retry = setTimeout(() => this.open(), BACKOFF_MAX);
      return;
    }
    let socket;
    try {
      socket = new WebSocket(this.url(), [PROTOCOL]);
    } catch {
      this.reconnect();
      return;
    }
    this.ws = socket;
    this.timers.hello = setTimeout(() => { if (this.ws === socket) this.reconnect(); }, HELLO_TIMEOUT);
    socket.onopen = () => {
      socket.send(JSON.stringify({ t: 'hello', since: Number(this.since()) || 0, batch: true, ...(this.lite ? { lite: true } : {}) }));
    };
    socket.onmessage = (event) => this.receive(socket, event.data);
    socket.onerror = () => {};
    socket.onclose = (event) => {
      if (this.ws !== socket) return;
      this.ws = null;
      this.clearTimers();
      if (event && event.code === SIGNED_OUT) { this.checkLogin(); return; }
      if (!this.stopped) this.reconnect();
    };
  }

  close() {
    this.clearTimers();
    clearTimeout(this.timers.retry);
    if (!this.ws) return;
    const socket = this.ws;
    this.ws = null;
    socket.onopen = null; socket.onmessage = null; socket.onerror = null; socket.onclose = null;
    try { socket.close(); } catch { /* already gone */ }
  }

  clearTimers() {
    clearTimeout(this.timers.hello);
    clearTimeout(this.timers.pong);
    this.timers.pong = null;
  }

  reconnect(delay) {
    this.close();
    if (this.stopped) return;
    this.attempt += 1;
    const wait = delay === undefined
      ? Math.min(BACKOFF_MAX, BACKOFF_BASE * 2 ** (this.attempt - 1)) * (0.75 + Math.random() * 0.5)
      : delay;
    this.setStatus('connecting');
    if (document.visibilityState !== 'visible') return;
    this.timers.retry = setTimeout(() => this.open(), wait);
  }

  // A stream the server ended as signed out: a plain request finds out
  // whether this page still has a login (api() takes it to the sign-in when
  // not), and the stream is opened again when it does.
  async checkLogin() {
    this.setStatus('connecting');
    try {
      await api('/api/sessions/' + encodeURIComponent(this.chatId), { attempts: 1, timeout: 12000 });
      if (!this.stopped) { this.attempt = 0; this.reconnect(0); }
    } catch (err) {
      if (err instanceof HttpError && err.status === 401) return;
      if (!this.stopped) this.reconnect();
    }
  }

  receive(socket, data) {
    if (socket !== this.ws || typeof data !== 'string') return;
    let frame;
    try { frame = JSON.parse(data); } catch { return; }
    if (!frame || typeof frame !== 'object') return;
    this.heardAt = Date.now();
    if (this.timers.pong) { clearTimeout(this.timers.pong); this.timers.pong = null; }
    switch (frame.t) {
      case 'hello':
        clearTimeout(this.timers.hello);
        break;
      case 'synced':
        this.attempt = 0;
        this.setStatus('live');
        break;
      case 'pong':
      case 'tick':
        return;
      case 'evs':
        // Many events in one frame (hello{batch}): each is an ev frame.
        for (const ev of Array.isArray(frame.evs) ? frame.evs : []) {
          ev.t = 'ev';
          try { this.onFrame(ev); } catch (err) { setTimeout(() => { throw err; }, 0); }
        }
        return;
      default:
        break;
    }
    try { this.onFrame(frame); } catch (err) { setTimeout(() => { throw err; }, 0); }
  }

  // ping asks a quiet socket whether it is still there.
  ping() {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN || this.timers.pong) return;
    this.pingId += 1;
    try { this.ws.send(JSON.stringify({ t: 'ping', id: this.pingId })); } catch { this.reconnect(); return; }
    this.timers.pong = setTimeout(() => { this.timers.pong = null; this.reconnect(0); }, PONG_TIMEOUT);
  }

  watch() {
    if (this.stopped || !this.ws) return;
    if (this.state === 'live' && Date.now() - this.heardAt > PROBE_AFTER) this.ping();
  }

  /** confirm checks a stream that says it is live, now. */
  confirm() {
    if (this.ws && this.state === 'live') this.ping();
    else if (!this.ws) { this.attempt = 0; this.reconnect(0); }
  }

  wake() {
    if (this.stopped) return;
    if (this.ws && this.state === 'live') {
      if (Date.now() - this.heardAt > WAKE_FRESH) this.ping();
      return;
    }
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) { this.attempt = 0; this.reconnect(0); }
  }

  visibility() {
    if (this.stopped) return;
    clearTimeout(this.timers.hidden);
    if (document.visibilityState === 'visible') {
      this.wake();
      return;
    }
    // A page in a pocket keeps nothing open for nobody, after a grace for the
    // quick look at another app.
    this.timers.hidden = setTimeout(() => {
      if (document.visibilityState !== 'visible' && !this.stopped) {
        this.close();
        this.setStatus('connecting');
      }
    }, HIDDEN_GRACE);
  }
}
