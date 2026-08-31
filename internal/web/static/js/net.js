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

function storageGet(key) {
  try { return localStorage.getItem(key); } catch { return null; }
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

const connection = {
  status: LIVE,
  lastContact: Date.now(),
  retryAt: 0,
  retryNow: null,
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
    lastContact: connection.lastContact,
    retryAt: connection.retryAt,
    online: navigator.onLine !== false,
  };
}

function emitConnection() {
  const snapshot = connectionState();
  for (const handler of connection.listeners) {
    try { handler(snapshot); } catch { /* ignore */ }
  }
}

// setConnection is how a stream or a request reports what it just saw.
function setConnection(status, options = {}) {
  connection.status = navigator.onLine === false && status !== LIVE ? OFFLINE : status;
  if (connection.status === LIVE) {
    connection.lastContact = Date.now();
    connection.retryAt = 0;
  } else if (options.retryAt !== undefined) {
    connection.retryAt = options.retryAt;
  }
  if (options.retryNow) connection.retryNow = options.retryNow;
  emitConnection();
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

// mountConnectionBar puts one honest line at the top of the page. It is the
// answer to "is what I am looking at still true?", and it is never hidden
// behind a menu or a toast that has already faded away.
export function mountConnectionBar() {
  const bar = document.createElement('div');
  bar.className = 'conn-bar';
  bar.id = 'connBar';
  bar.hidden = true;
  bar.setAttribute('role', 'status');
  bar.setAttribute('aria-live', 'polite');

  const dot = document.createElement('span');
  dot.className = 'conn-dot';
  const label = document.createElement('span');
  label.className = 'conn-label';
  const countdown = document.createElement('span');
  countdown.className = 'conn-count';
  const retry = document.createElement('button');
  retry.type = 'button';
  retry.className = 'conn-retry';
  retry.textContent = 'Retry';
  retry.addEventListener('click', () => {
    if (connection.retryNow) connection.retryNow();
    wake('manual');
  });
  bar.append(dot, label, countdown, retry);
  document.body.append(bar);

  let ticker = null;

  const render = () => {
    const state = connectionState();
    const offline = state.status === OFFLINE || !state.online;
    const live = state.status === LIVE;
    bar.hidden = live;
    setClass(document.body, 'conn-lost', !live);
    if (live) {
      if (ticker) { clearInterval(ticker); ticker = null; }
      return;
    }
    setClass(bar, 'offline', offline);

    // Read at a glance, possibly from behind a steering wheel: what is wrong,
    // and how old what is on screen is.
    const away = Math.round((Date.now() - state.lastContact) / 1000);
    const headline = offline ? 'No network.' : 'Connection lost.';
    label.textContent = away > 4
      ? headline + ' What you see is ' + agoLabel(away) + '.'
      : headline + ' Nothing on this screen is updating.';
    const wait = state.retryAt ? Math.max(0, Math.round((state.retryAt - Date.now()) / 1000)) : 0;
    countdown.textContent = offline
      ? 'Waiting for signal'
      : (wait > 0 ? 'Retrying in ' + wait + 's' : 'Reconnecting…');

    // The bar's real height is published so the page can move out from under
    // it rather than being covered: it grows with a notch and with a wrapped
    // line, so it is measured after the text is in place.
    requestAnimationFrame(() => {
      document.documentElement.style.setProperty('--conn-bar-h', bar.offsetHeight + 'px');
    });
    if (!ticker) ticker = setInterval(render, 1000);
  };

  onConnectionChange(render);
  render();
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
    onRetry,
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
    if (attempt > 1) {
      const delay = backoffDelay(attempt - 1);
      if (onRetry) onRetry(attempt, delay);
      await sleep(delay, signal);
    }
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
        setConnection(navigator.onLine === false ? OFFLINE : CONNECTING);
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
    // The server heartbeats every 10s; two and a half missed beats is a dead
    // stream, whatever the socket claims.
    this.staleAfter = options.staleAfter || 25000;
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
    if (Date.now() - this.lastData > this.staleAfter) this.reconnect(0);
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
    this.setStatus(navigator.onLine === false ? 'offline' : 'connecting', {
      retryAt: Date.now() + delay,
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
export class Outbox {
  constructor(name, send) {
    this.key = 'socrates.outbox.' + name;
    this.send = send;
    this.items = this.load();
    this.listeners = new Set();
    this.timer = null;
    this.running = false;
    onWake(() => this.pump());
  }

  load() {
    try {
      const raw = storageGet(this.key);
      const parsed = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed) ? parsed : [];
    } catch { return []; }
  }

  persist() {
    if (!this.items.length) storageRemove(this.key);
    else storageSet(this.key, JSON.stringify(this.items));
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
    const item = {
      id: clientKey(),
      payload,
      attempts: 0,
      state: 'pending',
      error: '',
      createdAt: Date.now(),
    };
    this.items.push(item);
    this.persist();
    this.pump();
    return item;
  }

  drop(id) {
    this.items = this.items.filter((item) => item.id !== id);
    this.persist();
  }

  // retry puts a failed item back in the queue, either one of them or all.
  retry(id) {
    let touched = false;
    for (const item of this.items) {
      if (id && item.id !== id) continue;
      if (item.state !== 'failed') continue;
      item.state = 'pending';
      item.attempts = 0;
      item.error = '';
      touched = true;
    }
    if (touched) this.persist();
    this.pump();
  }

  // pump sends the queue in order. One at a time and strictly in order, so the
  // conversation cannot end up with the second thing said before the first.
  async pump() {
    if (this.running) return;
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
    const next = this.items.find((item) => item.state === 'pending');
    if (!next) return;
    this.running = true;
    try {
      next.attempts += 1;
      await this.send(next.payload, next);
      this.items = this.items.filter((item) => item.id !== next.id);
      this.persist();
      this.running = false;
      this.pump();
    } catch (err) {
      this.running = false;
      const permanent = err instanceof HttpError && !shouldRetry(err);
      next.error = (err && err.message) || 'Could not send.';
      if (permanent) {
        next.state = 'failed';
        this.persist();
        this.pump();
        return;
      }
      this.persist();
      // In a dead zone there is nothing to gain from trying every few seconds,
      // and a phone in a car pays for it in battery. Waiting long is safe
      // because coming back online, or simply looking at the screen, wakes the
      // queue immediately.
      const delay = navigator.onLine === false
        ? 30000
        : backoffDelay(next.attempts, { base: 900, max: 20000 });
      this.timer = setTimeout(() => this.pump(), delay);
    }
  }
}
