// The service worker exists for one situation: opening Socrates when there is
// no signal at all.
//
// Without it, a reload in a tunnel replaces the whole app with the browser's
// error page, and everything the page was holding - the queued message, the
// draft, the transcript on screen - goes with it. With it, the app itself
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

// The shell is small and entirely local, so it is worth having in full before
// the first bad connection rather than only what happened to load. It is the
// chat page and what it imports, and nothing else: the dashboard's own
// modules can never be reached from a page this worker serves offline, and a
// file that has to arrive before anything can be served offline at all is a
// file worth not having in the list.
const SHELL = [
  '/',
  '/static/css/app.css',
  '/static/js/net.js',
  '/static/js/api.js',
  '/static/js/chat.js',
  '/static/js/markdown.js',
  '/static/js/voice.js',
  '/favicon.png',
  '/static/img/logo.png',
].map((path) => (path === '/' ? path : path + '?v=' + VERSION));

// The API is state, not the app. It is never cached: a stale answer presented
// as current is exactly the thing this whole change is against.
function isShellRequest(request) {
  if (request.method !== 'GET') return false;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return false;
  if (url.pathname.startsWith('/api/')) return false;
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
    if (name !== CACHE) await caches.delete(name);
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
      // app: the chat page reads which conversation to open from the hash.
      if (request.mode === 'navigate') {
        const shell = await caches.match('/');
        if (shell) return shell;
      }
      throw err;
    }
  })());
});
