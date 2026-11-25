# The HTTP foundation: the Z-Dash server

Socrates' listeners, password, sessions, request guards and Cloudflare tunnel are the Z-Dash server module,
`github.com/saschazesiger/Z-Dash/server` (its `docs/server.md`), shared with the other apps on Z-Dash. Socrates is a
`server.Config`, its routes and its pages; `internal/server/app.go` holds the configuration, `routes()` in
`internal/server/server.go` the routes. A fix to anything below "The module's" goes to Z-Dash (its `server/`), then
`go get github.com/saschazesiger/Z-Dash/server@main && go mod tidy` here.

## Who does what

| | The module's | Socrates' own |
| --- | --- | --- |
| Listeners | the local TCP address (`SOCRATES_ADDR` / `-addr`, retried while a restart still holds the port) and the tunnel's Unix socket `<data>/run/tunnel.sock`; panics answered with 500; a graceful stop | the data directory's lock, the staged restore before the store opens, the systemd watchdog on `GET /api/health`, the re-exec after a restore, the last backup and the store's close after `Run` returns (`main.go`) |
| Password | Argon2id under `auth.password`; `POST /api/login` (five tries, then a wait that doubles from 30 seconds to 15 minutes), `POST /api/password` `{current, password}` (signs every other browser out, keeps this one; through the tunnel `current` is not asked), `SOCRATES_ADMIN_PASSWORD` | `POST /api/setup` (the first password with the OpenRouter key, through `app.SetFirstPassword` and `app.StartSession`), the restore on the setup page, the pages `login.html`, `setup.html`, Admin's password card |
| Sessions | `auth.sessions`: the hex SHA-256 of each cookie value → `{"expires": <Unix ms>}`, 30 days; the cookie `socrates_session` (HttpOnly, `SameSite=Lax` by `Config.SameSite`, Secure through the tunnel); `POST /api/logout` → `{next}` | `GET /api/state`; the WebSockets check `app.StillSignedIn(r)` on every tick and end in `Config.OnSignOut` (`eventHub.revoke`, `chatHub.revoke`, close code 4401) |
| Guards | the Host check on the local address (`SOCRATES_HOSTS`), the CSRF guard on every route registered with `app.Handle` or `app.Public`: a request that is not a GET carries `X-Z-Dash: 1` and, when the browser sends one, an `Origin` that is this host | the WebSockets' origin check (`websocket.Accept`), the editor's proxy (`/ide/{id}/`, below), the per-login handshake ceilings |
| Tunnel | the `cloudflare` service built from the `Config`, the Access token checked in front of every route but the bypass paths, `/api/cloudflare*` ([CLOUDFLARE.md](CLOUDFLARE.md)) | the bypass handlers (`/docs/`, `/artifact-kit/`), `cloudflare.js` |
| Also there, unused by the pages | `/z-dash/` (the embedded Z-Dash and the module's shell), `GET /api/session`, `GET /api/events` (server-sent events), `POST /api/setup/password`, `GET /healthz` | Socrates' pages load their own vendored bundle (`/static/`), and its event stream is the WebSocket `GET /api/stream` |

## Routes

`routes()` registers every route one of four ways:

| | Checks | What |
| --- | --- | --- |
| `private(pattern, h)` → `app.HandleFunc` | Host, CSRF, signed in, and Socrates' own "setup is finished" (403 before there is a password) | the API |
| `public(pattern, h)` → `app.Public` | Host, CSRF | `GET /api/state`, `POST /api/setup`, `/api/restore*` (each refuses once a password is set), `GET /api/github/app/installed` |
| `pages` → `Config.Pages` (everything at `/` no other pattern takes) | Host; each handler gates itself | the pages (they redirect to `/login` and `/setup`), `/static/`, `sw.js`, the icons, the editor's proxy `/ide/{id}/` (its own requests carry no CSRF header: `signedIn` checks the session and the origin), `/mcp-proxy/{name}` (called by the agents on the loopback address, with a token of its own) |
| `sandboxed` → `Config.Bypass` | none: Cloudflare Access lets them through, no Host check; each guards its files with a token | `/docs/{token}/…`, `/artifact-kit/{name}` |

`GET /api/health` is `app.Unguarded`: it answers by any name, for `scripts/update.sh` and the watchdog.

The module marks every `/api/` answer `Cache-Control: no-store`; the `private` and `public` wrappers take that off
again, because Socrates' handlers say themselves what may be kept (a chat's images, its files).

## Security headers

The module would send a strict Content-Security-Policy, `X-Frame-Options`, `Cross-Origin-Opener-Policy` and a
`Permissions-Policy` with every answer. Socrates' pages are built from inline scripts and styles, frame the editor
and the chats' pages, run workers and use the microphone, so `Config.Headers` (`ownHeaders`) removes the module's
headers and every answer carries exactly what it did before: `X-Content-Type-Options`, `Referrer-Policy` and
`frame-ancestors 'self'` on the pages (`internal/web`), a policy of its own on each sandboxed page. A
Content-Security-Policy for the pages is a change of its own.

`Config.ReadTimeout` is unbounded, as before: uploads and recordings arrive over a phone's link.

## The web client

- `net.js` `request()` sends `X-Z-Dash: 1` with every call; the few `fetch` calls beside it (uploads, a draft's and a
  board's `call`, the setup page's `post`, the service worker's re-subscription) send `CSRF_HEADER` themselves.
  Nothing uses a form post or `sendBeacon`. A 403 *"did not come from the Socrates interface"* is a request without it.
- A wrong password answers 400 with `fields.password` (a wrong current one: `fields.current`), never 401: `net.js`
  takes a 401 outside `/api/login` for a lost session and goes to the sign-in. Too many tries answer 429.
- `POST /api/logout` answers `next`: `""` on the local address (the page goes to `/login`), Cloudflare Access's
  logout through the tunnel.

## Moving from the builds before

Until the build on the Z-Dash server, Socrates kept the password as a PBKDF2 hash (`password_hash` in `kv`), the
logins in the table `logins` (the cookie's value as it is) and built the tunnel's service itself.

- **Password.** `Config.LegacyPassword` reads `password_hash`: it still signs in, whatever its length, and counts as
  a password (no setup page). The first sign-in with it stores it as `auth.password` and deletes `password_hash`.
  New passwords need 10 characters (they needed 6).
- **Sessions.** At the start, while `auth.sessions` does not exist, `importLogins` writes it from the rows of
  `logins` that have not expired (SHA-256 of the token, `expires_at`). The cookie keeps its name and value, so
  nobody is signed out by the update. The table stays as it was and is never read again; a backup of an older
  build restored onto a new one is carried over the same way at its first start.
- **Tunnel.** The module builds the service with the names Socrates used (`Name: "Socrates"`, `Slug: "socrates"`,
  the same bypass paths and label, the socket at the same path), so the stored `cloudflare:*` state, the objects on
  Cloudflare and cloudflared's routing are unchanged.
- **Open pages.** A page loaded from the build before sends no `X-Z-Dash` and listens on the old stream address:
  its requests are refused until it is reloaded.

`SOCRATES_OPEN` (the module's switch that signs everybody in, for demos) is removed from the environment at the
start: whoever is signed in to Socrates runs commands on the machine.

## Tests

`internal/server/server_test.go`: the setup with its key, the CSRF guard, the old password signing in and moving
(`TestTheOldPasswordSignsInAndMoves`), the logins carried over (`TestTheOldLoginsCarryOver`,
`TestTheOldLoginsAreImportedOnce`); `events_test.go`: a new password and a sign-out end the streams
(`TestEventStreamEndsWithItsLogin`); `cloudflare_test.go`: the tunnel's sign-in; in the browser suite `pages` (the setup and sign-in pages),
`home-reconnect` (a sign-out and a new password under open streams), `home-deeplink` and `restore`.
