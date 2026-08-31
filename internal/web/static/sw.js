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

const CACHE = 'socrates-shell-v2';

// The API is state, not the app. It is never cached: a stale answer presented
// as current is exactly the thing this whole change is against.
function isShellRequest(request) {
  if (request.method !== 'GET') return false;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return false;
  if (url.pathname.startsWith('/api/')) return false;
  return true;
}

self.addEventListener('install', (event) => {
  // The shell is small and entirely local, so it is worth having in full
  // before the first bad connection rather than only what happened to load.
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    // One at a time: signing in is a redirect, and a single file that cannot
    // be fetched right now must not cost the rest of the shell.
    for (const path of [
      '/',
      '/static/css/app.css',
      '/static/js/net.js',
      '/static/js/api.js',
      '/static/js/chat.js',
      '/static/js/markdown.js',
      '/static/js/models.js',
      '/static/js/combobox.js',
      '/static/js/voice.js',
      '/favicon.png',
      '/static/img/logo.png',
    ]) {
      try {
        const response = await fetch(path, { credentials: 'same-origin' });
        if (response.ok && !response.redirected) await cache.put(path, response);
      } catch { /* offline while installing, the fetch handler will fill it in */ }
    }
    await self.skipWaiting();
  })());
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    for (const name of await caches.keys()) {
      if (name !== CACHE) await caches.delete(name);
    }
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
      }
      return response;
    } catch (err) {
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
