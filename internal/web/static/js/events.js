// The event stream: the one live channel of a page that has no terminal
// attached (HOME.md §1.2, §2.2).
//
// A terminal socket already carries every session's activity and every
// automatic name, but only while a terminal is open - and Home, a prepared
// session's panel and the moment before the first attach have none. Without
// this, those pages are as current as the last poll, which is the sidebar
// lying for fifteen seconds at a time. So the page keeps one more socket open
// while it is visible (and for half a minute after it is hidden), whatever is
// on the stage, and everything that is about the whole machine rather than
// one pane comes through it: the list, the activity, the names, the digests
// and the answer runs.
//
// The rules are the terminal socket's, scaled down to a stream nobody types
// into: a connection is not live until it has said `hello`; a connection that
// has said nothing for long enough is dead even if the browser has not
// noticed; the backoff starts again from the beginning whenever the radio or
// the screen comes back. And while it is down the page does not go blind:
// it polls the same content as a plain request, faster while Home or the
// question dialog is what the person is looking at.
//
// This module knows nothing about the page. It is handed what to do with each
// kind of frame, and it says how the connection is.

import { api } from './api.js';
import { connectionSource, onWake, HttpError } from './net.js';

const PROTOCOL = 'socrates.events.v1';

// The close code the server ends a stream with once its login is no longer
// valid: signed out, every login revoked by a new password, or expired
// (internal/server/events.go).
const SIGNED_OUT = 4401;

// The backoff, from HOME.md §2.2: half a second, doubling, never more than
// eight. A stream this cheap can afford to come back quickly.
const BACKOFF_BASE = 500;
const BACKOFF_MAX = 8000;

// The server sends a tick every fifteen seconds, so two missed ticks and a
// margin is a connection that is open and dead - the half-open socket a phone
// leaves behind after a tunnel.
const SILENCE = 35000;
const WATCH_EVERY = 2000;

// The silence is not waited out, though: a socket that has said nothing for
// longer than this is asked with a ping, which a live server answers within
// a moment, and one that does not is opened again PONG_TIMEOUT later. A
// half-open socket is found out in about a quarter of a minute rather than
// after two missed ticks, and a ping that has gone unanswered for QUIET_AFTER
// already makes the page say it is checking (quiet) rather than "Live" over
// numbers that may be old.
const PROBE_AFTER = 10000;
const QUIET_AFTER = 2000;

// How long a socket may go without its `hello` before it is given up on, as
// the terminal socket does. The clock starts when the socket is made, not when
// it opens: a handshake a proxy accepts and never answers leaves a socket in
// CONNECTING, which raises nothing at all until the browser's own timeout -
// minutes - and a page waiting for that is a page that stopped being live.
const HELLO_TIMEOUT = 10000;

// How many handshakes in a row may fail before the page stops calling itself
// "Reconnecting…": something between here and the server refuses the stream
// (a proxy without WebSocket support), and the fallback poll is how this page
// is current for as long as that lasts.
const BLOCKED_AFTER = 3;

// A wake on a socket that looks open is checked with a ping rather than torn
// down: tearing it down on every focus of a desktop window would be a
// reconnect every few seconds for nothing. One that has heard from the server
// this recently needs no check at all.
const WAKE_FRESH = 20000;
const PONG_TIMEOUT = 5000;

// How long a hidden page keeps its stream. Switching to another app and back,
// or to another tab to copy something, is seconds; a stream closed for each of
// them is a handshake and a whole hello every time, and a Home that says
// "Reconnecting…" the moment it is looked at again. Past this the page is not
// being looked at, and holds nothing open for nobody.
const HIDDEN_GRACE = 30000;

// The fallback poll while the stream is down: quickly while the person is
// looking at the live numbers, slowly otherwise.
const POLL_FAST = 5000;
const POLL_SLOW = 15000;

/**
 * mountEvents opens the stream and keeps it open while the page is visible.
 *
 * `ctx`:
 *   hello(state)         a full resync: {sessions, rev, digests, outcomes,
 *                        answers, now}.
 *                        Every hello and every successful fallback poll is
 *                        one; a poll of a server too old for /api/home has
 *                        only `sessions`.
 *   sessions(rows)       the whole list again
 *   activity(map)        committed activity changes, {id: activity}
 *   title(frame)         the automatic naming of a session
 *   digest(id, digest)   a digest changed, or went (null)
 *   outcome(id, outcome) the verdict on a session's last turn changed, or
 *                        went (null)
 *   answer(id, run)      an answer run changed, or went (null)
 *   fresh()              a pong came back: the state on the page is the
 *                        server's as of the ping (stateAt)
 *   status(status)       'live' | 'connecting' | 'offline', whenever it moves,
 *                        and when a live stream turns quiet or stops being
 *                        so (quiet)
 *   unreachable(err)     a fallback poll that could not be answered
 *   fast()               whether the fast poll applies right now
 *
 * It hands back start, how the connection is (`status`, `updatedAt` - the
 * last moment anything was heard from the server), `poll` for a page that
 * wants the state now, and `stop`.
 */
export function mountEvents(ctx) {
  let ws = null;
  let stopped = true;
  let attempt = 0;
  let status = 'connecting';
  let updatedAt = 0;
  let heardAt = 0;
  // The moment the state on the page is known to be the server's as of: a
  // hello when it arrives, a poll or a sign-in check when it was asked, a
  // pong when its ping was sent - every frame before a pong has arrived, so
  // nothing the server had said by then is missing. A socket that calls
  // itself live does not move it: a half-open one is live and carries
  // nothing. What has been sent and not answered yet: ping id to when.
  let stateAt = 0;
  const pings = new Map();
  // When the ping that is waiting for its pong was sent, and whether the page
  // has been told the stream is quiet (a ping unanswered for QUIET_AFTER).
  let probeAt = 0;
  let quietSaid = false;
  // The server's clock less this device's, as of the last frame or state that
  // carried it (a hello, a tick, a poll): the ages on the page are worked out
  // from the server's timestamps, so they are measured on the server's clock.
  let skew = 0;
  // The stream was closed because the page was hidden, and nothing has been
  // tried since it was shown again: the page is not offline, it is coming
  // back (resuming).
  let rested = false;
  let retryTimer = null;
  let helloTimer = null;
  let pongTimer = null;
  // Fires QUIET_AFTER after the ping the silence is measured from, so the page
  // says it is checking then, and not at the next watch tick after it.
  let quietTimer = null;
  let watchTimer = null;
  let pollTimer = null;
  let polling = false;
  // How long the last poll's read took: the next poll is scheduled only once
  // it is in, so on a slow link the page is updated that much less often
  // than the poll's own interval.
  let readTook = 0;
  // The grace a hidden page keeps its stream for, and whether a stream that
  // said it was signed out is having its login looked at.
  let hiddenTimer = null;
  let checking = false;
  // A confirm asked for while the page was hidden (confirmAsked): it is
  // carried out the moment the page is shown.
  let confirmWanted = false;
  let pingId = 0;
  let unwake = null;
  // The handshakes that ended without a hello since the last one that did,
  // and when the socket being tried was made.
  let failed = 0;
  let openedAt = 0;
  // When the stream last stopped being live, and when a plain request last
  // read the state. A stream refused again and again is only "kept current by
  // the poll" once a poll has actually got through since it was lost: until
  // then nothing says the server can be reached at all, and three refused
  // handshakes are as much a server that is down as a proxy that blocks.
  // So are the refusals before that read: only those after it count (noteRead).
  let lostAt = 0;
  let readAt = 0;
  // The poll's address: the whole state where the server has it, and the
  // list alone from a server that predates it. Learnt once, from a 404.
  let homePath = '/api/home';
  // Not the page's: the terminal socket speaks for the connection bar, and
  // Home says what this stream is doing in its own words. A source is still
  // what hears the device losing its network, which a socket does not.
  const conn = connectionSource({ global: false });
  conn.onOffline(() => { if (!stopped) reconnect(0); });

  function setStatus(next) {
    const value = navigator.onLine === false && next !== 'live' ? 'offline' : next;
    if (value === status) return;
    if (status === 'live') lostAt = Date.now();
    status = value;
    quietSaid = false;
    conn.report(value === 'live' ? 'live' : 'connecting');
    try { ctx.status(value); } catch { /* a page that is drawing must not stop the stream */ }
    schedulePoll();
  }

  // noteRead is a plain request that read the state. The first since the
  // stream was lost says the server can be reached again, and nothing more:
  // the handshakes refused before it were as likely a server that was down
  // - restarting, say - as a proxy that does not pass WebSockets, so they are
  // not counted towards blocked, and the stream is tried again at once
  // rather than at the end of its backoff. A server that is back takes it; a
  // proxy that blocks refuses it again, now with a read behind it.
  function noteRead() {
    const first = readAt <= lostAt;
    readAt = Date.now();
    if (!first || status === 'live') return;
    failed = 0;
    if (!ws && !stopped && !checking && document.visibilityState === 'visible') { attempt = 0; reconnect(0); }
  }

  function url() {
    const base = location.protocol === 'https:' ? 'wss://' : 'ws://';
    return base + location.host + '/api/events';
  }

  function open() {
    close();
    if (stopped || document.visibilityState !== 'visible') return;
    if (navigator.onLine === false) {
      setStatus('offline');
      // The browser's word for having no network is not always followed by
      // one for having it back - an iPhone after a lock raises none - so it
      // is tried again on a floor of its own.
      retryTimer = setTimeout(() => { retryTimer = null; open(); }, BACKOFF_MAX);
      return;
    }
    let socket;
    try {
      socket = new WebSocket(url(), [PROTOCOL]);
    } catch {
      reconnect();
      return;
    }
    ws = socket;
    openedAt = Date.now();
    helloTimer = setTimeout(() => { helloTimer = null; giveUp(); }, HELLO_TIMEOUT);
    socket.onmessage = (event) => receive(event.data);
    socket.onerror = () => { /* onclose carries the outcome */ };
    socket.onclose = (event) => {
      if (ws !== socket) return;
      const helloed = status === 'live';
      ws = null;
      clearTimers();
      if (event && event.code === SIGNED_OUT) {
        // The login this stream was opened with is gone: signed out, every
        // login revoked by a new password, or expired (the server looks
        // again at every tick).
        if (!stopped) checkLogin();
        return;
      }
      if (!helloed) { failed += 1; rested = false; }
      if (!stopped) reconnect();
    };
  }

  // giveUp is a socket that has not said hello in time, whether it opened or
  // is still hanging in its handshake.
  function giveUp() {
    if (stopped || !ws) return;
    failed += 1;
    rested = false;
    reconnect();
  }

  // checkLogin follows a stream the server ended as signed out. Whether this
  // page is signed out too is the server's to say, and a plain request asks
  // it the way every other request of the app does: a 401 - and api() takes
  // the page to the sign-in, as it does for an expired login anywhere else -
  // or, on the device that changed the password and was handed a new login,
  // the state, which is drawn, and the stream is opened again. Nothing is
  // reconnected before the answer: a handshake with a login that is gone is
  // refused, and refusals in a row are what "the stream is blocked" means.
  async function checkLogin() {
    close();
    setStatus('connecting');
    if (checking) return;
    checking = true;
    let ok = false;
    const asked = Date.now();
    try {
      const data = await readState();
      ok = true;
      noteRead();
      if (!stopped && status !== 'live') {
        updatedAt = Date.now();
        stateAt = Math.max(stateAt, asked);
        clock(data && data.now, updatedAt, asked);
        hand('hello', data || {});
      }
    } catch (err) {
      // Signed out: the page is on its way to the sign-in, and nothing is
      // opened again in the moment before it goes.
      if (err instanceof HttpError && err.status === 401) return;
      hand('unreachable', err);
    } finally {
      checking = false;
    }
    if (stopped) return;
    // Reached, so the login is good: at once. Not reached: the backoff, and
    // the poll keeps asking meanwhile.
    if (ok) { attempt = 0; reconnect(0); } else reconnect();
  }

  // readState is the whole state as a plain request: /api/home, or the list
  // alone from a server too old to have it.
  async function readState() {
    try {
      return await api(homePath, { attempts: 1, timeout: 12000 });
    } catch (err) {
      // A server from before Home answers the list and nothing else, which
      // is still everything the sidebar and the counts are drawn from.
      if (!(err instanceof HttpError) || err.status !== 404 || homePath !== '/api/home') throw err;
      homePath = '/api/sessions';
      return api(homePath, { attempts: 1, timeout: 12000 });
    }
  }

  // stuck says whether the socket being tried has been in its handshake for
  // longer than any handshake takes.
  function stuck() {
    return !!ws && ws.readyState === WebSocket.CONNECTING && Date.now() - openedAt > HELLO_TIMEOUT;
  }

  function close() {
    if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
    clearTimers();
    pings.clear();
    if (!ws) return;
    const socket = ws;
    ws = null;
    socket.onopen = null; socket.onmessage = null; socket.onerror = null; socket.onclose = null;
    try { socket.close(); } catch { /* already gone */ }
  }

  function clearTimers() {
    if (helloTimer) { clearTimeout(helloTimer); helloTimer = null; }
    if (pongTimer) { clearTimeout(pongTimer); pongTimer = null; }
    clearQuiet();
  }

  function clearQuiet() {
    if (quietTimer) { clearTimeout(quietTimer); quietTimer = null; }
    probeAt = 0;
  }

  // quiet is a stream that calls itself live and has not answered a ping for
  // QUIET_AFTER: it may be half open, and is about to be opened again. The
  // ping's own quiet timer having fired is that moment, whatever the clock
  // reads to the millisecond.
  function quiet() {
    if (status !== 'live' || !pongTimer || !probeAt) return false;
    return !quietTimer || Date.now() - probeAt >= QUIET_AFTER;
  }

  // sayQuiet tells the page when quiet starts or ends, which no status change
  // does: the stream is still live until its ping is given up on.
  function sayQuiet() {
    const now = quiet();
    if (now === quietSaid) return;
    quietSaid = now;
    try { ctx.status(status); } catch { /* a page that is drawing must not stop the stream */ }
  }

  // clock takes the server's clock from a frame or a state that carries it.
  // `sentAt` is when the request was sent, for a state read over HTTP: the
  // moment it was stamped is taken as half way between asking and hearing.
  function clock(now, heard, sentAt) {
    const at = Number(now);
    if (!Number.isFinite(at) || at <= 0) return;
    skew = at - (sentAt ? (sentAt + heard) / 2 : heard);
  }

  function reconnect(delay) {
    close();
    if (stopped) return;
    attempt += 1;
    const wait = delay === undefined
      ? Math.min(BACKOFF_MAX, BACKOFF_BASE * 2 ** (attempt - 1)) * (0.75 + Math.random() * 0.5)
      : delay;
    setStatus('connecting');
    if (document.visibilityState !== 'visible') return;
    retryTimer = setTimeout(() => { retryTimer = null; open(); }, wait);
  }

  // Each handler is guarded on its own: a frame the page cannot draw is a
  // bug in the page, and it must not take the stream - and every frame after
  // it - down with it.
  function hand(name, ...args) {
    try { ctx[name](...args); } catch (err) { setTimeout(() => { throw err; }, 0); }
  }

  function receive(data) {
    if (typeof data !== 'string') return;
    let frame;
    try { frame = JSON.parse(data); } catch { return; }
    if (!frame || typeof frame !== 'object') return;
    heardAt = Date.now();
    if (pongTimer) { clearTimeout(pongTimer); pongTimer = null; }
    clearQuiet();
    switch (frame.t) {
      case 'hello':
        if (helloTimer) { clearTimeout(helloTimer); helloTimer = null; }
        clock(frame.now, heardAt);
        attempt = 0;
        failed = 0;
        rested = false;
        updatedAt = heardAt;
        stateAt = Math.max(stateAt, heardAt);
        hand('hello', frame);
        setStatus('live');
        break;
      case 'sessions':
        updatedAt = heardAt;
        hand('sessions', Array.isArray(frame.sessions) ? frame.sessions : [], frame.rev);
        break;
      case 'activity':
        updatedAt = heardAt;
        hand('activity', frame.sessions);
        break;
      case 'title':
        hand('title', frame);
        break;
      case 'digest':
        if (frame.id) hand('digest', frame.id, frame.digest || null);
        break;
      case 'outcome':
        if (frame.id) hand('outcome', frame.id, frame.outcome || null);
        break;
      case 'answer':
        if (frame.id) hand('answer', frame.id, frame.run || null);
        break;
      case 'pong': {
        const sent = pings.get(frame.id);
        for (const id of [...pings.keys()]) if (id <= frame.id) pings.delete(id);
        if (sent && status === 'live') {
          stateAt = Math.max(stateAt, sent);
          hand('fresh');
        }
        break;
      }
      case 'tick':
        // A quiet machine is still a machine being watched: the numbers on
        // screen are as current as this moment.
        if (status === 'live') updatedAt = heardAt;
        clock(frame.now, heardAt);
        break;
      default:
        break;
    }
    // Any frame at all is a socket that carries: whatever was said about it
    // being quiet is over.
    if (quietSaid) sayQuiet();
  }

  function ping() {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    pingId += 1;
    pings.set(pingId, Date.now());
    try { ws.send(JSON.stringify({ t: 'ping', id: pingId })); } catch { reconnect(0); return; }
    // The first ping still waiting is the one the silence is measured from:
    // another sent meanwhile (a wake, a confirm) does not move it.
    if (!pongTimer) {
      clearQuiet();
      probeAt = Date.now();
      quietTimer = setTimeout(() => { quietTimer = null; sayQuiet(); }, QUIET_AFTER);
    }
    if (pongTimer) clearTimeout(pongTimer);
    pongTimer = setTimeout(() => { pongTimer = null; clearQuiet(); reconnect(0); }, PONG_TIMEOUT);
  }

  // wake is the radio coming back or the screen being looked at again. A
  // socket that is down is tried at once and from the start of the backoff;
  // one that looks open and has been quiet is asked whether it still is.
  function wake() {
    if (stopped || checking) return;
    if (document.visibilityState !== 'visible') return;
    if (!ws || stuck()) {
      attempt = 0;
      reconnect(0);
      if (status !== 'live') poll();
      return;
    }
    if (ws.readyState === WebSocket.OPEN && Date.now() - heardAt > WAKE_FRESH) ping();
  }

  // visibility follows the page. A hidden page keeps its stream for
  // HIDDEN_GRACE, so that a quick look at another app comes back to a page
  // that never stopped being live; after that it holds no socket and polls
  // nothing, and a page that is shown again starts over.
  //
  // A page shown again within the grace is only as fresh as what it heard:
  // a phone freezes a hidden page, and its socket can look open and have
  // carried nothing. A live stream says something at least every tick, so
  // one that has been quiet for longer than WAKE_FRESH is not trusted - it is
  // opened again at once, and its hello is the whole state, now - instead of
  // being asked with a ping while the page shows what it held.
  function visibility() {
    if (stopped) return;
    if (document.visibilityState === 'visible') {
      if (hiddenTimer) { clearTimeout(hiddenTimer); hiddenTimer = null; }
      if (!checking && status === 'live' && ws && ws.readyState === WebSocket.OPEN
        && Date.now() - heardAt > WAKE_FRESH) {
        confirmWanted = false;
        attempt = 0;
        rested = true;
        reconnect(0);
        return;
      }
      if (confirmWanted) { confirmAsked(); return; }
      wake();
      return;
    }
    if (hiddenTimer) return;
    hiddenTimer = setTimeout(() => {
      hiddenTimer = null;
      if (stopped || document.visibilityState === 'visible') return;
      close();
      rested = true;
      setStatus('connecting');
    }, HIDDEN_GRACE);
  }

  // confirmAsked is confirm. A page that is hidden sends nothing - a ping
  // from it is a ping a frozen page never hears answered - so it is kept
  // until the page is shown (visibility), where a stream that is only as
  // fresh as its last frame would otherwise go unasked: a tap on a
  // notification reaches the page by message before the page is brought
  // forward, and the question it opened waits for exactly this.
  function confirmAsked() {
    if (stopped || checking) return;
    if (document.visibilityState !== 'visible') { confirmWanted = true; return; }
    confirmWanted = false;
    if (status === 'live' && ws && ws.readyState === WebSocket.OPEN) { ping(); return; }
    if (!ws || stuck()) { attempt = 0; reconnect(0); }
    poll();
  }

  function watch() {
    if (stopped || !ws) return;
    if (stuck()) { giveUp(); return; }
    if (ws.readyState !== WebSocket.OPEN || status !== 'live') return;
    if (Date.now() - heardAt > SILENCE) { reconnect(0); return; }
    // Quiet for longer than a live stream is between frames: asked, rather
    // than trusted until it has missed two ticks.
    if (!pongTimer && Date.now() - heardAt > PROBE_AFTER) ping();
    sayQuiet();
  }

  /* ----------------------------------------------------- the fallback poll */

  function schedulePoll() {
    if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
    if (stopped || status === 'live') return;
    let fast = false;
    try { fast = !!ctx.fast(); } catch { /* the slow one, then */ }
    pollTimer = setTimeout(() => { pollTimer = null; poll(); }, fast ? POLL_FAST : POLL_SLOW);
  }

  async function poll() {
    if (stopped || polling || document.visibilityState !== 'visible') { schedulePoll(); return; }
    polling = true;
    const asked = Date.now();
    try {
      const data = await readState();
      noteRead();
      readTook = readAt - asked;
      rested = false;
      // The stream came up while this was in flight, and its hello is newer.
      if (status === 'live') return;
      updatedAt = Date.now();
      stateAt = Math.max(stateAt, asked);
      clock(data && data.now, updatedAt, asked);
      hand('hello', data || {});
    } catch (err) {
      rested = false;
      hand('unreachable', err);
    } finally {
      polling = false;
      schedulePoll();
    }
  }

  return {
    start() {
      if (!stopped) return;
      stopped = false;
      unwake = onWake(wake);
      document.addEventListener('visibilitychange', visibility);
      watchTimer = setInterval(watch, WATCH_EVERY);
      open();
      schedulePoll();
    },
    stop() {
      stopped = true;
      close();
      if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
      if (hiddenTimer) { clearTimeout(hiddenTimer); hiddenTimer = null; }
      if (watchTimer) { clearInterval(watchTimer); watchTimer = null; }
      if (unwake) { unwake(); unwake = null; }
      document.removeEventListener('visibilitychange', visibility);
      conn.release();
    },
    /** status is 'live', 'connecting' or 'offline'. */
    status: () => status,
    /** live says whether frames are arriving, so nothing needs to be asked. */
    live: () => status === 'live',
    /**
     * quiet says the stream calls itself live and has not answered a ping for
     * a moment: what the page holds may be old, and it is being checked. It
     * is opened again if the pong does not come.
     */
    quiet: () => quiet(),
    /** serverNow is the server's clock now, as far as the page knows it. */
    serverNow: () => Date.now() + skew,
    /**
     * blocked says whether the stream has been refused several times in a
     * row while a plain request still gets through, so the page is kept
     * current by the poll alone. Refusals with no read since the stream was
     * lost may be a server that is down, which is not current at all.
     */
    blocked: () => status !== 'live' && failed >= BLOCKED_AFTER && readAt > lostAt,
    /**
     * pollEvery is how often the fallback poll brings the state right now:
     * its interval, and the time its last read took on top of it.
     */
    pollEvery: () => {
      let every = POLL_SLOW;
      try { every = ctx.fast() ? POLL_FAST : POLL_SLOW; } catch { /* the slow one, then */ }
      return every + readTook;
    },
    /** updatedAt is when the state on the page was last known to be current. */
    updatedAt: () => updatedAt,
    /** noteFresh records a state this page read itself, from a full list. */
    noteFresh() { updatedAt = Date.now(); noteRead(); },
    /** poll reads the whole state now, while the stream is down. */
    poll() { if (status !== 'live') poll(); },
    /**
     * stateAt is the moment the state on the page is known to be the
     * server's as of (a hello, a poll, a pong).
     */
    stateAt: () => stateAt,
    /**
     * confirm makes stateAt move past this moment as soon as it can: a ping
     * on a live stream (a half-open one misses its pong and is opened again,
     * and its hello moves it), a reconnect and a poll on one that is not.
     * Asked while the page is hidden, it is done when the page is shown.
     */
    confirm: () => confirmAsked(),
    /**
     * resuming says the stream was closed because the page was hidden and
     * nothing has been tried and failed since it was shown again: what the
     * page holds is old, and it is on its way back, which is not offline.
     */
    resuming: () => rested && status !== 'live' && navigator.onLine !== false,
    /** refresh re-arms the poll, for a page whose fast/slow answer changed. */
    refresh: () => schedulePoll(),
  };
}
