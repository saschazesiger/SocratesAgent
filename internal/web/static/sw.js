// The service worker exists for one situation: opening Socrates when there is
// no signal at all.
//
// Without it, a reload in a tunnel replaces the whole app with the browser's
// error page, and everything the page was holding - the typed line, the draft,
// the screen the terminal is on - goes with it. With it, the app itself
// still loads, says it has no connection, and picks up where it left off the
// moment there is network again.
//
// It is network first, always. Whatever is fetched successfully is kept as a
// copy for the next bad moment, and the copy is only ever used when the
// network could not answer. So being online never means looking at an old
// version of the app.
//
// The copy is kept per build, and every address in it carries the build stamp
// the server put there. That is what makes the offline app whole rather than
// merely present: an old script can never be handed to a new page, because the
// new page never asks for the address the old script is kept under.

// Replaced by the server with a hash of the files it is serving.
const VERSION = '__VERSION__';
const CACHE = 'socrates-shell-' + VERSION;
// Not a build: what the pushes left on the app's icon (settle), which every
// build goes on from.
const PUSH_STATE = 'socrates-push';

// The shell is small and entirely local, so it is worth having in full before
// the first bad connection rather than only what happened to load. It is the
// session page, everything it imports, the stylesheets and fonts it wears, and
// the vendored terminal and Dashboard-Style it loads with plain tags - and
// nothing else: the dashboard's own modules can never be reached from a page
// this worker serves offline, and a file that has to arrive before anything
// can be served offline at all is a file worth not having in the list.
//
// The vendored terminal alone is 801 KB uncompressed and 210 KB gzipped -
// xterm.js is 489 KB of that and the WebGL renderer 248 KB - and both earn it,
// because a terminal that has to be fetched is a terminal that is not there in
// a tunnel. Dashboard-Style adds about 865 KB for the same reason - ds.css and
// ds.js are 399 KB and 378 KB, 94 KB and 104 KB gzipped, and the two Google
// Sans files 86 KB - because a page without its look is not one anybody can
// use. Socrates itself serves the files as they are: the compression is
// whatever is in front of it, the Cloudflare edge or a reverse proxy. The
// measurement for the whole list below is in the README, under "The app shell
// works with no network", and is where an addition should be noticed.
const SHELL = [
  '/',
  '/static/vendor/dashboard-style/fonts/fonts.css',
  '/static/vendor/dashboard-style/fonts/google-sans-latin-opsz-normal.woff2',
  '/static/vendor/dashboard-style/fonts/google-sans-latin-ext-opsz-normal.woff2',
  '/static/vendor/dashboard-style/ds.css',
  '/static/vendor/dashboard-style/ds.js',
  '/static/css/theme.css',
  '/static/css/so-shared.css',
  '/static/css/so-frame.css',
  '/static/css/so-stage.css',
  '/static/css/so-home.css',
  '/static/css/so-question.css',
  '/static/css/so-sheet.css',
  '/static/css/so-chat.css',
  '/static/fonts/SourceCodePro-Regular.woff2',
  '/static/fonts/SourceCodePro-Bold.woff2',
  '/static/vendor/xterm.css',
  '/static/vendor/xterm.js',
  '/static/vendor/addon-fit.js',
  '/static/vendor/addon-unicode11.js',
  '/static/vendor/addon-web-links.js',
  '/static/vendor/addon-webgl.js',
  '/static/js/ui.js',
  '/static/js/net.js',
  '/static/js/api.js',
  '/static/js/session.js',
  '/static/js/term.js',
  '/static/js/keybar.js',
  '/static/js/harnesses.js',
  '/static/js/logos.js',
  '/static/js/folders.js',
  '/static/js/combobox.js',
  '/static/js/voice.js',
  '/static/js/assist.js',
  '/static/js/notify.js',
  '/static/js/dictate.js',
  '/static/js/keyboard.js',
  '/static/js/transfer.js',
  '/static/js/backup.js',
  '/static/js/events.js',
  '/static/js/home.js',
  '/static/js/outcome.js',
  '/static/js/chatview.js',
  '/static/js/chatfold.js',
  '/static/js/chatstream.js',
  '/static/js/chatparts.js',
  '/static/js/chatoutbox.js',
  '/static/js/composer.js',
  '/static/js/askdialog.js',
  '/static/js/accounts.js',
  '/static/js/accountpicker.js',
  '/static/js/md.js',
  '/static/vendor/markdown/marked.esm.js',
  '/static/vendor/markdown/purify.es.js',
  '/static/manifest.json',
  '/favicon.png',
  '/static/img/logo.png',
].map((path) => (path === '/' ? path : path + '?v=' + VERSION));

// The API is state, not the app. It is never cached: a stale answer presented
// as current is exactly the thing this whole change is against.
function isShellRequest(request) {
  if (request.method !== 'GET') return false;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return false;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ide/')) return false;
  return true;
}

// hasWholeShell says whether this build can be served on its own. Until it
// can, the build before it is left alone: a half downloaded new version that
// has thrown away the last working one is worse than no worker at all.
async function hasWholeShell() {
  const cache = await caches.open(CACHE);
  for (const path of SHELL) {
    if (!(await cache.match(path))) return false;
  }
  return true;
}

// Once this build is whole there is nothing left to check, so the walk over
// the shell happens while it is still filling in and not on every request
// after that.
let pruned = false;

async function dropOtherBuilds() {
  if (pruned || !(await hasWholeShell())) return;
  pruned = true;
  for (const name of await caches.keys()) {
    if (name !== CACHE && name !== PUSH_STATE) await caches.delete(name);
  }
}

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    // One at a time: signing in is a redirect, and a single file that cannot
    // be fetched right now must not cost the rest of the shell.
    for (const path of SHELL) {
      try {
        const response = await fetch(path, { credentials: 'same-origin' });
        if (response.ok && !response.redirected) await cache.put(path, response);
      } catch { /* offline while installing, the fetch handler will fill it in */ }
    }
    await dropOtherBuilds();
    await self.skipWaiting();
  })());
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    await dropOtherBuilds();
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', (event) => {
  const request = event.request;
  if (!isShellRequest(request)) return;

  event.respondWith((async () => {
    try {
      const response = await fetch(request);
      // A redirect to /login or /setup is an answer about this session, not a
      // piece of the app. Caching it would strand the next visit there, and a
      // replayed redirect is rejected by the browser anyway.
      if (response && response.ok && response.type === 'basic' && !response.redirected) {
        const copy = response.clone();
        caches.open(CACHE).then((cache) => cache.put(request, copy)).catch(() => {});
        // A build that was installed on a bad connection completes itself as
        // the app is used, and lets go of its predecessor once it is whole.
        const url = new URL(request.url);
        if (SHELL.includes(url.pathname + url.search)) dropOtherBuilds().catch(() => {});
      }
      return response;
    } catch (err) {
      // Across every build that is still kept: a stamped address belongs to
      // exactly one of them, so a hit here is never a mixture.
      const cached = await caches.match(request);
      if (cached) return cached;
      // A navigation with nothing cached for that exact URL still gets the
      // app: the session page reads which session to open from the hash.
      if (request.mode === 'navigate') {
        const shell = await caches.match('/');
        if (shell) return shell;
      }
      throw err;
    }
  })());
});


// Web Push: the news that a session finished, or that it needs an answer,
// for a phone whose page is not running (internal/server/push.go). What
// kind of news it was travels with it, because a tap on a question opens
// the question rather than the terminal.

// The icon a notification is drawn with, and the silhouette Android puts in
// the status bar for it. iOS uses the app's own icon and ignores both.
const NOTE_ICON = '/static/img/icon-192.png';
const NOTE_BADGE = '/static/img/badge-96.png';

// targetOf is the address a tap on a piece of news opens: the question for a
// question, the session for a session that finished or was started, and
// Home for anything else. The same forms a cold tab reads its route from
// (session.js `routeOf`).
function targetOf(id, kind) {
  if (!id) return '/';
  if (kind === 'question') return '/#q=' + encodeURIComponent(id);
  if (kind === 'finished' || kind === 'started') return '/#open=' + encodeURIComponent(id);
  return '/';
}

// settleBadge puts the number of questions waiting on the app's icon. A
// payload with no number - an older server - leaves whatever is there,
// because a guess on the icon is worse than an old number. iOS only draws
// a badge for an app that may notify, and a browser without the call has
// none at all, so every way this can fail is silent.
async function settleBadge(count) {
  if (typeof count !== 'number' || !Number.isFinite(count) || count < 0) return;
  const nav = self.navigator;
  try {
    if (count > 0 && nav && typeof nav.setAppBadge === 'function') await nav.setAppBadge(Math.floor(count));
    else if (count === 0 && nav && typeof nav.clearAppBadge === 'function') await nav.clearAppBadge();
  } catch { /* no badge on this device */ }
}

// dropAnswered closes the questions in the tray that are not asking any
// more: answered at the machine, on another device, or by the program
// moving on. Every push carries the whole set of sessions that are asking,
// so the tray is put right by whichever news comes next. A payload with no
// set - an older server - closes nothing. `keep` is the tag just shown,
// which is never taken away again in the same breath: a push that shows
// nothing is the thing iOS punishes.
async function dropAnswered(asking, keep) {
  if (!Array.isArray(asking)) return;
  const still = new Set(asking.map(String));
  try {
    for (const note of await self.registration.getNotifications()) {
      const data = note.data || {};
      if (data.kind !== 'question' || !data.id || still.has(data.id) || note.tag === keep) continue;
      note.close();
    }
  } catch { /* a tray that cannot be read is left as it is */ }
}

// The stamp of the newest count the icon and the tray were put right from
// (push.go pushNote.At). Pushes reach the phone in any order - a push service
// holds a "Finished" back from a dozing phone while it hands a question over
// at once - and an older count arriving last would take the newer question
// off the icon and out of the tray. A worker is stopped between pushes and
// keeps nothing in memory, so the stamp is kept in a cache of its own; one
// that cannot be read is no stamp, and the count is applied as before.
const APPLIED = '/push-applied';

async function appliedAt() {
  try {
    const held = await (await caches.open(PUSH_STATE)).match(APPLIED);
    const at = held ? Number(await held.text()) : 0;
    return Number.isFinite(at) ? at : 0;
  } catch { return 0; }
}

async function markApplied(at) {
  try { await (await caches.open(PUSH_STATE)).put(APPLIED, new Response(String(at))); } catch { /* the next one is applied as it comes */ }
}

// settle puts the icon and the tray right from one push's count, unless a
// newer count has been applied already. One at a time: two pushes handled
// together would each read the stamp before the other wrote it.
let settling = Promise.resolve();

function settle(note, keep) {
  const run = settling.then(async () => {
    const at = typeof note.at === 'number' && Number.isFinite(note.at) ? note.at : 0;
    if (at) {
      if (at < await appliedAt()) return;
      await markApplied(at);
    }
    await settleBadge(note.badge);
    await dropAnswered(note.asking, keep);
  });
  settling = run.catch(() => {});
  return run;
}

// tellPages says to every open page that a push has just written the icon
// and the tray. A page that is open knows the asking set as it is now, which
// may be newer than the push's - a question answered here in the moment the
// push was on its way - and it writes them again from that (notify.js).
async function tellPages() {
  try {
    for (const client of await self.clients.matchAll({ type: 'window', includeUncontrolled: true })) {
      client.postMessage({ t: 'pushed' });
    }
  } catch { /* no page to tell */ }
}

// Every push is shown, whatever it carries - a browser that receives one and
// shows nothing is entitled to stop delivering them, and iOS does exactly
// that - and one per session, so a session that finishes twice while the
// phone is in a pocket leaves one line in the tray. Only once it is shown
// are the badge and the tray put right, and neither can take the
// notification with it when it fails.
self.addEventListener('push', (event) => {
  let note = {};
  try { note = event.data ? event.data.json() : {}; } catch { /* shown with the defaults */ }
  if (!note || typeof note !== 'object') note = {};
  const id = typeof note.id === 'string' ? note.id : '';
  const kind = typeof note.kind === 'string' ? note.kind : '';
  const tag = id ? 'socrates:' + id : 'socrates';
  event.waitUntil((async () => {
    try {
      await self.registration.showNotification(String(note.title || 'Socrates'), {
        body: String(note.body || ''),
        tag,
        renotify: true,
        icon: NOTE_ICON,
        badge: NOTE_BADGE,
        data: { id, kind, url: targetOf(id, kind) },
      });
    } finally {
      try { await settle(note, tag); } catch { /* the next push puts them right */ }
      await tellPages();
    }
  })());
});

// A tap opens what the news was about. A tab that already has the app is
// told by message which session and what kind of news it was - before it is
// brought forward, so a focus the browser refuses costs nothing - and it
// opens it the way its own address would (session.js). A tab that is on
// another page of Socrates, the settings, is taken to it instead, and with
// no tab at all a new one is opened at it.
//
// A tab that is taken there is brought forward first and navigated after: a
// browser lets the worker raise a window only for a moment after the tap -
// Firefox for a second - and a navigation over a slow tunnel takes longer
// than that, so a focus that waited for it would be refused and the tab left
// behind the others. Only a tab this worker controls can be navigated at all;
// any other would refuse, and the moment the refusal took would be gone from
// the window opened instead - so it is not asked.
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const data = event.notification.data || {};
  const id = typeof data.id === 'string' ? data.id : '';
  const kind = typeof data.kind === 'string' ? data.kind : '';
  const url = targetOf(id, kind);
  event.waitUntil((async () => {
    const focus = async (client) => {
      try { await client.focus(); } catch { /* it was told; it shows it when it is next looked at */ }
    };
    const tabs = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    const pathOf = (client) => { try { return new URL(client.url).pathname; } catch { return ''; } };
    const app = tabs.find((client) => pathOf(client) === '/');
    if (app) {
      if (id) app.postMessage({ t: 'open', id, kind });
      await focus(app);
      return;
    }
    const owned = new Set((await self.clients.matchAll({ type: 'window' })).map((client) => client.id));
    const other = tabs.find((client) => owned.has(client.id));
    if (other && typeof other.navigate === 'function') {
      await focus(other);
      try {
        await other.navigate(url);
        return;
      } catch { /* a new window, then */ }
    }
    await self.clients.openWindow(url);
  })());
});

// keyBytes is the server's public key, which it hands over in URL-safe
// base64, as the bytes a subscription is made with.
function keyBytes(text) {
  const b64 = String(text || '').replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

// The push service can replace a subscription on its own - it expired, or
// its keys were rotated - and says so to the worker, not to the page, which
// may not be open for days. The worker subscribes again with the same key
// and tells the server the new address in place of the old one, so a phone
// that is never opened goes on being reached.
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil((async () => {
    const old = event.oldSubscription || null;
    let sub = event.newSubscription || null;
    if (!sub) {
      let key = old && old.options ? old.options.applicationServerKey : null;
      if (!key) {
        const res = await fetch('/api/push', { credentials: 'same-origin', cache: 'no-store' });
        if (!res.ok) return;
        key = keyBytes((await res.json()).key);
      }
      sub = await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    }
    const body = { subscription: sub.toJSON() };
    if (old && old.endpoint && old.endpoint !== sub.endpoint) body.old_endpoint = old.endpoint;
    await fetch('/api/push/subscribe', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  })().catch(() => { /* the page subscribes again on its next load */ }));
});
