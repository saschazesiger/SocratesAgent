// net.js is the part of the frontend that assumes the network will fail.
//
// Socrates is used from a phone in a moving car, so a dropped connection is
// not an exception here, it is the normal case. Three rules follow from that:
//
//   1. Losing the connection is always visible. There is one status bar, it is
//      on every page, and it says what is wrong and what is being done about it.
//   2. Nothing is ever silently stale. A live stream that stops delivering is
//      treated as broken even when the browser has not noticed yet, and the UI
//      is told to stop pretending that what it shows is current.
//   3. Nothing the user did is lost. Anything they send goes into a queue that
//      survives a reload and is retried until the server confirms it.

import { M, iconEl } from './ui.js';

/* ---------------------------------------------------------------- helpers */

// clientKey mints the idempotency key that makes a request safe to repeat.
export function clientKey() {
  if (window.crypto && crypto.randomUUID) return crypto.randomUUID();
  return 'k-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10);
}

// setClass turns one class on or off and touches the element only when that
// actually changes something.
//
// To be clear about what this does and does not fix: writing the identical
// class list back onto an element does *not* restart a CSS animation - that
// was measured, not assumed. What does restart one is taking a connected node
// out of the page and putting it back, which is what append() and
// insertBefore() do even when the node lands in the position it was already
// in. (moveBefore() is the exception; it keeps the animation running.)
//
// So the load-bearing part of keeping this page's animations smooth is
// elsewhere: the lists that are keyed and patched in place rather than
// rebuilt, and the nodes that are only ever moved when they are genuinely in
// the wrong place. This helper is hygiene on top of that - it keeps class
// writes honest, makes "only touch it when it changed" the obvious shape at
// every call site, and costs nothing.
//
// It lives in net.js because this is the file every other one imports, and
// api.js re-exports it so callers can take it from the same place as el().
export function setClass(node, name, on) {
  if (!node || !name) return;
  const want = !!on;
  if (node.classList.contains(name) === want) return;
  node.classList.toggle(name, want);
}

// backoffDelay grows the wait between attempts and adds jitter, so a server
// coming back up is not hit by every tab at the same instant.
function backoffDelay(attempt, { base = 600, max = 20000 } = {}) {
  const raw = Math.min(max, base * Math.pow(2, Math.max(0, attempt - 1)));
  return Math.round(raw * (0.75 + Math.random() * 0.5));
}

function storageSet(key, value) {
  try { localStorage.setItem(key, value); } catch { /* private mode, quota */ }
}

function storageRemove(key) {
  try { localStorage.removeItem(key); } catch { /* ignore */ }
}

/* ------------------------------------------------------------ wake signals */

// The browser gives several hints that it is worth trying again: the network
// came back, the tab became visible, the page was restored from the back
// forward cache. On a phone the important one is visibility - the radio is off
// while the screen is, and everything on screen is stale the moment it wakes.
const wakeHandlers = new Set();

export function onWake(handler) {
  wakeHandlers.add(handler);
  return () => wakeHandlers.delete(handler);
}

function wake(reason) {
  for (const handler of wakeHandlers) {
    try { handler(reason); } catch { /* a broken listener must not stop the rest */ }
  }
}

window.addEventListener('online', () => wake('online'));
window.addEventListener('focus', () => wake('focus'));
window.addEventListener('pageshow', (event) => wake(event.persisted ? 'restore' : 'load'));
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') wake('visible');
});

/* --------------------------------------------------------- connection state */

// One place decides what the app believes about its connection. Streams report
// what they see, plain requests report whether they got through, and the bar
// below renders the result.
const OFFLINE = 'offline';       // the device says it has no network
const CONNECTING = 'connecting'; // trying, and not succeeding yet
const LIVE = 'live';             // events are arriving

// Every running stream, and the subset of them that speaks for the page as a
// whole. A plain request getting through is not proof that the live view is
// healthy, so the two are weighed together.
const liveStreams = new Set();
const globalStreams = new Set();

function streamsHealthy() {
  for (const stream of globalStreams) {
    if (stream.status !== 'live' && stream.status !== 'idle') return false;
  }
  return true;
}

// OFFLINE_GRACE is how long a stream may be between connections before the
// page says anything about it.
//
// Every switch from one session to another closes one stream and opens another, and so does arriving
// from another page: for a few hundred milliseconds there is genuinely no live
// stream, and saying "connection lost" then is a lie that flashes on screen
// several times a minute. A real loss is still announced - it just has to last
// longer than a reconnect does. The two cases that need no waiting at all skip
// the grace period: the device saying it has no network, and an attempt that
// actually failed.
const OFFLINE_GRACE = 1800;

const connection = {
  status: LIVE,
  // degraded is what the page renders. It follows status, but only after the
  // grace period above, so an intentional stream swap never shows as an outage.
  degraded: false,
  lastContact: Date.now(),
  retryAt: 0,
  retryNow: null,
  graceTimer: null,
  listeners: new Set(),
};

function onConnectionChange(handler) {
  connection.listeners.add(handler);
  handler(connectionState());
  return () => connection.listeners.delete(handler);
}

function connectionState() {
  return {
    status: connection.status,
    degraded: connection.degraded,
    lastContact: connection.lastContact,
    retryAt: connection.retryAt,
    online: navigator.onLine !== false,
  };
}

function clearGrace() {
  if (connection.graceTimer) { clearTimeout(connection.graceTimer); connection.graceTimer = null; }
}

function emitConnection() {
  const snapshot = connectionState();
  for (const handler of connection.listeners) {
    try { handler(snapshot); } catch { /* ignore */ }
  }
}

// setConnection is how a stream or a request reports what it just saw.
//
// What it saw and what the page shows are deliberately two different things:
// anything short of the grace period above is a reconnect, not an outage.
// options.immediate is how a caller says it already knows better - the attempt
// failed, there is nothing left to wait for.
function setConnection(status, options = {}) {
  connection.status = navigator.onLine === false && status !== LIVE ? OFFLINE : status;
  if (connection.status === LIVE) {
    connection.lastContact = Date.now();
    connection.retryAt = 0;
    clearGrace();
    connection.degraded = false;
  } else {
    if (options.retryAt !== undefined) connection.retryAt = options.retryAt;
    if (options.immediate || navigator.onLine === false) {
      clearGrace();
      connection.degraded = true;
    } else if (!connection.degraded && !connection.graceTimer) {
      connection.graceTimer = setTimeout(() => {
        connection.graceTimer = null;
        if (connection.status === LIVE) return;
        connection.degraded = true;
        emitConnection();
      }, OFFLINE_GRACE);
    }
  }
  if (options.retryNow) connection.retryNow = options.retryNow;
  emitConnection();
}

// The session page keeps its own "this is stale" visuals in step with the bar, so
// the wait before either of them appears is defined in one place.
export const CONNECTION_GRACE = OFFLINE_GRACE;

/**
 * connectionSource lets something that is not a LiveStream report to the same
 * status bar.
 *
 * The terminal's transport is a WebSocket, and setConnection is private to
 * this file for a good reason: one place decides what the app believes about
 * its connection. So instead of exporting that decision, this hands out a
 * reporter backed by a pseudo-stream registered in the same two sets the
 * EventSource wrapper uses. streamsHealthy() weighs it like any other stream,
 * the window's offline listener reaches it through goOffline(), and the bar,
 * body[data-conn-lost] and body[data-stale] keep working with no change at all.
 *
 * report(status, extra) takes 'live' | 'connecting' | 'offline' | 'idle', with
 * the same extra fields LiveStream reports: retryAt, immediate, retryNow.
 * release() takes the pseudo-stream out again, which a page must do when it
 * closes its socket for good - a source left behind would hold the bar down
 * for ever.
 */
export function connectionSource({ global = true } = {}) {
  const source = {
    status: 'idle',
    reportsGlobal: global,
    // The offline listener calls this on every registered stream. A socket
    // that is still open when the radio goes simply stops delivering, so the
    // owner is told to give up on it rather than left looking live.
    goOffline() { source.onOffline(); },
    onOffline: () => {},
  };
  liveStreams.add(source);
  if (global) globalStreams.add(source);
  return {
    report(status, extra = {}) {
      source.status = status;
      if (!global) return;
      if (status === 'live') setConnection(LIVE);
      else if (status !== 'idle') {
        setConnection(navigator.onLine === false ? OFFLINE : CONNECTING, {
          retryAt: extra.retryAt || 0,
          immediate: !!extra.immediate,
          retryNow: extra.retryNow,
        });
      }
    },
    // onOffline is how the owner hears the device losing its network, which is
    // the one signal a socket does not produce on its own.
    onOffline(handler) { source.onOffline = handler; },
    release() {
      source.status = 'idle';
      liveStreams.delete(source);
      globalStreams.delete(source);
      // The last stream going is no news from the server: a device with no
      // network stays offline (a chat left for Home while in a tunnel).
      if (navigator.onLine !== false && streamsHealthy() && connection.status !== LIVE) setConnection(LIVE);
    },
  };
}

// noteContact records that the server answered something. That proves the
// connection works, but not that the live view is current - a stream that is
// still down keeps the bar up, because the thread on screen is what the person
// is actually reading.
function noteContact() {
  connection.lastContact = Date.now();
  if (connection.status !== LIVE && streamsHealthy()) setConnection(LIVE);
}

// Going offline has to reach the streams, not just the status bar. A socket
// that was already open does not necessarily error when the radio goes: it
// simply stops delivering, and a view that keeps looking live while that
// happens is the exact failure this whole file exists to prevent.
window.addEventListener('offline', () => {
  setConnection(OFFLINE);
  for (const stream of liveStreams) stream.goOffline();
});
window.addEventListener('online', () => {
  if (connection.status === OFFLINE) setConnection(CONNECTING);
});

/* ------------------------------------------------------------- status bar */

// The line counts the seconds to the next attempt, so it is redrawn this often
// while it shows. Data cadence, not motion.
const CONN_TICK_MS = 1000;

// A view that says the connection where the person is looking - a chat's
// dock, right over its composer - stands in for the bar while it does, so
// the same outage is not said twice on one screen.
let saidElsewhere = false;

/** connectionSaidElsewhere says whether such a view says it right now. */
export function connectionSaidElsewhere(on) {
  if (saidElsewhere === !!on) return;
  saidElsewhere = !!on;
  emitConnection();
}

/** retryConnection is the bar's Retry, for a view that stands in for it. */
export function retryConnection() {
  if (connection.retryNow) connection.retryNow();
  wake('manual');
}

// A phone's line keeps to what is wrong and how old the screen is.
const narrow = window.matchMedia('(max-width: 600px)');

// mountConnectionBar puts one honest line at the top of the page. It is the
// answer to "is what I am looking at still true?", and it is never hidden
// behind a menu or a toast that has already faded away.
//
// It is a Dashboard-Style alert in the page's own slot (#connSlot), not a bar
// laid over the page: the slot sits where the page put it, so nothing has to
// move out from under it. The alert is built once and only its words change
// afterwards, because a live region that is replaced together with its text
// is one a screen reader may never read.
export function mountConnectionBar() {
  let slot = document.getElementById('connSlot');
  if (!slot) {
    slot = document.createElement('div');
    slot.className = 'so-conn';
    slot.id = 'connSlot';
    slot.hidden = true;
    const host = document.querySelector('.so-main') || document.getElementById('main')
      || document.querySelector('.so-auth') || document.body;
    host.prepend(slot);
  }

  const alert = document.createElement('div');
  alert.className = 'ds-alert ds-alert--warning';
  const label = document.createElement('span');
  label.className = 'ds-alert__text';
  label.setAttribute('role', 'status');
  label.setAttribute('aria-live', 'polite');
  // The countdown changes every second; it is not a live region, or a screen
  // reader would read nothing else.
  const countdown = document.createElement('span');
  countdown.className = 'ds-meta';
  const retry = document.createElement('button');
  retry.type = 'button';
  retry.className = 'ds-btn ds-btn--quiet ds-btn--icon';
  retry.setAttribute('aria-label', 'Try to connect now');
  retry.dataset.tip = 'Retry';
  retry.append(iconEl('sync', 18));
  retry.addEventListener('click', () => retryConnection());
  alert.append(iconEl('wifiOff', 20), label, countdown, retry);
  slot.replaceChildren(alert);

  let ticker = null;
  let shown = false;

  const render = () => {
    const state = connectionState();
    const offline = state.status === OFFLINE || !state.online;
    // Only a loss that outlived the grace period is worth a line; a stream
    // being swapped for another one is not something anyone needs to read.
    const live = !state.degraded;
    document.body.toggleAttribute('data-conn-lost', !live);
    if (live || saidElsewhere) {
      if (ticker) { clearInterval(ticker); ticker = null; }
      if (shown) {
        shown = false;
        M.leave(slot, '', { then: 'hide' });
      }
      return;
    }
    setClass(alert, 'ds-alert--warning', !offline);
    setClass(alert, 'ds-alert--error', offline);

    // Read at a glance, possibly from behind a steering wheel: what is wrong,
    // and how old what is on screen is.
    const away = Math.round((Date.now() - state.lastContact) / 1000);
    const headline = offline ? 'No network.' : 'Connection lost.';
    setText(label, away > 4
      ? headline + ' What you see is ' + agoLabel(away) + '.'
      : headline + ' Nothing on this screen is updating.');
    const wait = state.retryAt ? Math.max(0, Math.round((state.retryAt - Date.now()) / 1000)) : 0;
    setText(countdown, offline
      ? (narrow.matches ? '' : 'Waiting for signal')
      : (wait > 0 ? 'Retrying in ' + wait + 's' : 'Reconnecting…'));

    if (!shown) {
      shown = true;
      slot.hidden = false;
      M.enter(slot);
    }
    if (!ticker) ticker = setInterval(render, CONN_TICK_MS);
  };

  onConnectionChange(render);
  render();
}

// setText writes a text only when it changed, so the live region is not told
// the same sentence again every second.
function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
}

function agoLabel(seconds) {
  if (seconds < 60) return seconds + 's old';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return minutes + ' minutes old';
  return Math.floor(minutes / 60) + ' hours old';
}

/* --------------------------------------------------------------- requests */

// HttpError carries the status so callers can tell "the server said no" from
// "the server never heard the question".
export class HttpError extends Error {
  constructor(message, status) {
    super(message);
    this.name = 'HttpError';
    this.status = status;
  }
}

// NetworkError is the second kind: nothing came back at all.
export class NetworkError extends Error {
  constructor(message) {
    super(message || 'The connection dropped before the server answered.');
    this.name = 'NetworkError';
  }
}

// RetryLater is thrown by a queued action that the server refused for a reason
// that will pass on its own - a run that is still going, say. It is not a
// failure, it is a "not yet".
export class RetryLater extends Error {
  constructor(message) {
    super(message || 'Not ready yet.');
    this.name = 'RetryLater';
  }
}

const RETRYABLE_STATUS = new Set([408, 425, 429, 500, 502, 503, 504, 522, 523, 524]);

function shouldRetry(err) {
  if (err instanceof NetworkError) return true;
  if (err instanceof RetryLater) return true;
  if (err instanceof HttpError) return RETRYABLE_STATUS.has(err.status);
  return false;
}

// fetchOnce is a single attempt with a deadline of its own, because a stalled
// mobile connection can otherwise leave a request hanging for minutes.
async function fetchOnce(path, { method, body, headers, signal, timeout }) {
  const controller = new AbortController();
  const onAbort = () => controller.abort();
  if (signal) {
    if (signal.aborted) controller.abort();
    else signal.addEventListener('abort', onAbort, { once: true });
  }
  const timer = timeout ? setTimeout(() => controller.abort(), timeout) : null;
  try {
    return await fetch(path, {
      method,
      headers,
      body,
      signal: controller.signal,
      credentials: 'same-origin',
      cache: 'no-store',
    });
  } catch (err) {
    if (signal && signal.aborted) throw err;
    throw new NetworkError(err && err.name === 'AbortError'
      ? 'The server did not answer in time.'
      : 'Could not reach Socrates.');
  } finally {
    if (timer) clearTimeout(timer);
    if (signal) signal.removeEventListener('abort', onAbort);
  }
}

// request is the one way this app talks to the server. Reads and anything
// carrying an idempotency key are retried; everything else is attempted once,
// because repeating a write that may have landed is worse than failing.
export async function request(path, options = {}) {
  const {
    method = 'GET',
    body,
    raw = false,
    signal,
    timeout = 25000,
    attempts,
  } = options;

  const headers = {};
  let payload;
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  const idempotent = method === 'GET' || method === 'HEAD'
    || (body && typeof body === 'object' && body.client_id);
  const limit = attempts != null ? attempts : (idempotent ? 4 : 1);

  let lastErr = null;
  for (let attempt = 1; attempt <= limit; attempt++) {
    if (attempt > 1) await sleep(backoffDelay(attempt - 1), signal);
    try {
      const res = await fetchOnce(path, { method, body: payload, headers, signal, timeout });
      // Anything that came back at all proves the connection is up, even a 500.
      noteContact();
      if (res.status === 401 && path !== '/api/login') {
        location.href = '/login';
        throw new HttpError('Signed out', 401);
      }
      if (!res.ok) {
        const err = new HttpError(await errorText(res), res.status);
        if (attempt < limit && shouldRetry(err)) {
          lastErr = err;
          continue;
        }
        throw err;
      }
      if (raw) return res;
      if (res.status === 204) return null;
      const text = await res.text();
      if (!text) return null;
      try { return JSON.parse(text); } catch { return null; }
    } catch (err) {
      if (signal && signal.aborted) throw err;
      if (err instanceof NetworkError) {
        // The request never reached the server. That is a failure, not a gap
        // between two streams, so there is nothing to wait for before saying so.
        setConnection(navigator.onLine === false ? OFFLINE : CONNECTING, { immediate: true });
      }
      if (attempt < limit && shouldRetry(err)) {
        lastErr = err;
        continue;
      }
      throw err;
    }
  }
  throw lastErr || new NetworkError();
}

async function errorText(res) {
  try {
    const data = await res.clone().json();
    if (data && data.error) return data.error;
  } catch { /* not json */ }
  return res.statusText || ('HTTP ' + res.status);
}

function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(finish, ms);
    function finish() {
      clearTimeout(timer);
      if (signal) signal.removeEventListener('abort', onAbort);
      resolve();
    }
    function onAbort() {
      clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    }
    if (signal) signal.addEventListener('abort', onAbort, { once: true });
  });
}

/* ----------------------------------------------------------- live streams */

// LiveStream is an EventSource that refuses to go quiet.
//
// The browser's own reconnect is not enough on a mobile connection: it does
// nothing when the socket is still open but no bytes are coming through, which
// is exactly what a tunnel through a lost cell does. So this one runs a
// watchdog on the server's heartbeat, reconnects on its own schedule, and tells
// the page - through onStatus - whenever what it shows stopped being live.
export class LiveStream {
  constructor(options) {
    this.url = options.url;                        // () => string, read fresh on every attempt
    this.onMessage = options.onMessage || (() => {});
    this.onStatus = options.onStatus || (() => {});
    this.reportsGlobal = options.reportsGlobal !== false;
    this.source = null;
    this.attempt = 0;
    this.lastData = 0;
    this.retryTimer = null;
    this.watchdog = null;
    this.status = 'idle';
    this.stopped = true;
    this.unwake = null;
    // onFail is how a page gets a chance to find out why: repeated failures
    // can mean the network, but they can also mean the session expired, and
    // only a plain request can tell the difference.
    this.onFail = options.onFail || (() => {});
  }

  start() {
    this.stopped = false;
    liveStreams.add(this);
    if (this.reportsGlobal) globalStreams.add(this);
    if (!this.unwake) {
      this.unwake = onWake(() => {
        if (this.stopped) return;
        // Anything older than a couple of heartbeats is suspect the moment the
        // page wakes up, so it is thrown away rather than trusted.
        if (this.status !== 'live' || Date.now() - this.lastData > 12000) this.reconnect(0);
      });
    }
    if (!this.watchdog) this.watchdog = setInterval(() => this.check(), 2000);
    this.open();
  }

  stop() {
    this.stopped = true;
    liveStreams.delete(this);
    globalStreams.delete(this);
    this.setStatus('idle');
    this.close();
    if (this.watchdog) { clearInterval(this.watchdog); this.watchdog = null; }
    if (this.unwake) { this.unwake(); this.unwake = null; }
  }

  close() {
    if (this.retryTimer) { clearTimeout(this.retryTimer); this.retryTimer = null; }
    if (this.source) {
      this.source.onmessage = null;
      this.source.onerror = null;
      this.source.onopen = null;
      try { this.source.close(); } catch { /* ignore */ }
      this.source = null;
    }
  }

  setStatus(status, extra = {}) {
    if (this.status === status && !extra.force) return;
    this.status = status;
    this.onStatus(status, extra);
    if (!this.reportsGlobal) return;
    if (status === 'live') setConnection(LIVE);
    else if (status !== 'idle') {
      setConnection(navigator.onLine === false ? OFFLINE : CONNECTING, {
        retryAt: extra.retryAt || 0,
        immediate: !!extra.immediate,
        retryNow: () => this.reconnect(0),
      });
    }
  }

  open() {
    this.close();
    if (this.stopped) return;
    let url;
    try { url = typeof this.url === 'function' ? this.url() : this.url; } catch { url = null; }
    if (!url) return;
    // A browser that knows it is offline should wait for the radio rather than
    // burn battery on connections that cannot succeed.
    if (navigator.onLine === false) {
      this.setStatus('offline');
      return;
    }
    if (this.status !== 'live') this.setStatus('connecting');
    let source;
    try {
      source = new EventSource(url);
    } catch {
      this.scheduleRetry();
      return;
    }
    this.source = source;
    // The headers arriving is not the same as data arriving - a proxy can hold
    // a stream open and send nothing - but it does restart the watchdog clock.
    source.onopen = () => {
      if (this.source !== source) return;
      this.lastData = Date.now();
    };
    source.onmessage = (event) => {
      if (this.source !== source) return;
      this.lastData = Date.now();
      this.attempt = 0;
      this.setStatus('live');
      let payload = null;
      try { payload = JSON.parse(event.data); } catch { return; }
      // A heartbeat carries no state; its whole job was to prove the stream is
      // still there, which receiving it already did.
      if (payload && payload.type === 'ping') return;
      this.onMessage(payload);
    };
    source.onerror = () => {
      if (this.source !== source) return;
      this.scheduleRetry();
    };
  }

  // goOffline is the device telling the stream what it cannot work out for
  // itself: there is no network, so nothing more is coming.
  goOffline() {
    if (this.stopped) return;
    this.close();
    this.setStatus('offline', { force: true });
  }

  // check is the watchdog: an open socket that has gone quiet past the
  // heartbeat is broken, whatever the browser thinks.
  check() {
    if (this.stopped) return;
    if (navigator.onLine === false) {
      if (this.status !== 'offline') this.goOffline();
      return;
    }
    if (this.status === 'offline') {
      this.reconnect(0);
      return;
    }
    if (!this.source || this.status !== 'live') return;
    // Two and a half missed beats of the server's ten second heartbeat.
    if (Date.now() - this.lastData > 25000) this.reconnect(0);
  }

  reconnect(delay) {
    if (this.stopped) return;
    this.close();
    this.attempt = 0;
    if (this.status === 'live') this.setStatus('connecting');
    if (delay > 0) this.retryTimer = setTimeout(() => this.open(), delay);
    else this.open();
  }

  scheduleRetry() {
    this.close();
    if (this.stopped) return;
    this.attempt += 1;
    const delay = backoffDelay(this.attempt, { base: 700, max: 15000 });
    // This attempt did not just end, it failed. There is nothing to wait for.
    this.setStatus(navigator.onLine === false ? 'offline' : 'connecting', {
      retryAt: Date.now() + delay,
      immediate: true,
      force: true,
    });
    this.retryTimer = setTimeout(() => this.open(), delay);
    try { this.onFail(this.attempt); } catch { /* a listener must not break the retry */ }
  }

  // secondsSinceData is what a view uses to say how old what it shows is.
  get secondsSinceData() {
    return this.lastData ? Math.round((Date.now() - this.lastData) / 1000) : 0;
  }
}

/* ---------------------------------------------------------------- outbox */

// Outbox keeps what the user did until the server has confirmed it.
//
// Nothing typed or tapped is thrown away because a tunnel blinked: it is
// written to local storage first, sent after, and retried - across reloads if
// need be - until it lands. The server side of every queued action carries an
// idempotency key, so a retry that turns out to be a duplicate is a no-op
// rather than a second message.
//
// Every tab of the browser shares the queue in storage, one key per item
// (`socrates.outbox.<name>.<id>`). One array under one key was a
// read-modify-write, and a tab's view of local storage can lag a write
// another tab has just made: a tab whose send had just failed wrote its copy
// of the queue back with the attempt counted, over a message the other tab
// had queued a moment before, and that message was gone (2 of 20 messages
// queued from two tabs at once). Now a tab writes only the item it adds or
// changes and removes only the one it sent, so nothing it writes can take
// another tab's item with it. What another tab writes is taken in when the
// browser says so (`storage`). Two tabs may both send the same item, which
// its idempotency key makes one - and one of them may then put back an item
// the other has just sent, which goes out once more as the same no-op.
//
// The items go out in the order they were queued: `seq` is one more than the
// highest a tab can see when it adds one, so a tab's own items keep their
// order, and two tabs' items that were queued at the same moment go in the
// order of their times.
export class Outbox {
  constructor(name, send) {
    this.prefix = 'socrates.outbox.' + name + '.';
    this.send = send;
    this.items = this.load() || [];
    this.listeners = new Set();
    this.timer = null;
    this.running = false;
    onWake(() => this.pump());
    window.addEventListener('storage', (event) => {
      if (event.key !== null && !event.key.startsWith(this.prefix)) return;
      const stored = this.load();
      if (!stored) return;
      this.items = stored;
      this.tell();
      this.pump();
    });
  }

  // load is the queue as storage holds it, in order, or null when storage
  // cannot be read at all (a private window): this tab's own copy is all there
  // is then.
  load() {
    let store;
    try {
      store = window.localStorage;
      if (!store) return null;
    } catch { return null; }
    try {
      const keys = [];
      for (let i = 0; i < store.length; i += 1) {
        const key = store.key(i);
        if (key && key.startsWith(this.prefix)) keys.push(key);
      }
      const items = [];
      for (const key of keys) {
        try {
          const item = JSON.parse(store.getItem(key));
          if (item && item.id) items.push(item);
        } catch { /* a damaged entry is not an item */ }
      }
      return items.sort(inOrder);
    } catch { return null; }
  }

  // read is one item as storage holds it now, or null when it is not there
  // any more - sent, or dropped, from this tab or another.
  read(id) {
    const stored = this.load();
    return (stored || this.items).find((item) => item.id === id) || null;
  }

  // write puts one item into storage (or takes it out, for null), then takes
  // in the queue as storage holds it and tells the listeners.
  write(id, item) {
    const key = this.prefix + id;
    if (item) storageSet(key, JSON.stringify(item));
    else storageRemove(key);
    const stored = this.load();
    if (stored) this.items = stored;
    else {
      const rest = this.items.filter((one) => one.id !== id);
      this.items = item ? [...rest, item].sort(inOrder) : rest;
    }
    this.tell();
  }

  tell() {
    const snapshot = this.items.slice();
    for (const listener of this.listeners) {
      try { listener(snapshot); } catch { /* ignore */ }
    }
  }

  onChange(listener) {
    this.listeners.add(listener);
    listener(this.items.slice());
    return () => this.listeners.delete(listener);
  }

  add(payload) {
    const queued = this.load() || this.items;
    const item = {
      id: clientKey(),
      seq: queued.reduce((high, one) => Math.max(high, Number(one.seq) || 0), 0) + 1,
      payload,
      attempts: 0,
      state: 'pending',
      error: '',
      createdAt: Date.now(),
    };
    this.write(item.id, item);
    this.pump();
    return item;
  }

  drop(id) {
    this.write(id, null);
  }

  // retry puts a failed item back in the queue, either one of them or all.
  retry(id) {
    for (const item of this.load() || this.items) {
      if ((id && item.id !== id) || item.state !== 'failed') continue;
      this.write(item.id, { ...item, state: 'pending', attempts: 0, error: '' });
    }
    this.pump();
  }

  // pump sends the queue in order. One at a time and strictly in order, so the
  // conversation cannot end up with the second thing said before the first.
  async pump() {
    if (this.running) return;
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
    const stored = this.load();
    if (stored) this.items = stored;
    const next = this.items.find((item) => item.state === 'pending');
    if (!next) return;
    this.running = true;
    const attempts = (next.attempts || 0) + 1;
    let failure = null;
    try {
      await this.send(next.payload, next);
    } catch (err) {
      failure = err || new Error('Could not send.');
    }
    this.running = false;
    if (!failure) {
      this.write(next.id, null);
      this.pump();
      return;
    }
    const permanent = failure instanceof HttpError && !shouldRetry(failure);
    const error = failure.message || 'Could not send.';
    // Counted on the item as it is now: another tab may have sent it, or the
    // person dropped it, while this attempt was on its way.
    const now = this.read(next.id);
    if (now) this.write(next.id, { ...now, attempts, error, state: permanent ? 'failed' : now.state });
    if (permanent) {
      this.pump();
      return;
    }
    // In a dead zone there is nothing to gain from trying every few seconds,
    // and a phone in a car pays for it in battery. Waiting long is safe
    // because coming back online, or simply looking at the screen, wakes the
    // queue immediately.
    const delay = navigator.onLine === false
      ? 30000
      : backoffDelay(attempts, { base: 900, max: 20000 });
    this.timer = setTimeout(() => this.pump(), delay);
  }
}

// inOrder is the order an Outbox sends in: the order the items were queued.
function inOrder(a, b) {
  return ((Number(a.seq) || 0) - (Number(b.seq) || 0))
    || ((Number(a.createdAt) || 0) - (Number(b.createdAt) || 0))
    || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
}
