# Third party licences

Socrates itself is MIT — see [LICENSE](LICENSE). Nothing else here is, and this
file names every piece that travels with it: what is compiled into the
`socrates` binary, what the published Docker image contains, and what the app
downloads onto your machine the first time it needs it. Passing a binary or an
image on is exactly what obliges you to pass its notices and its source on with
it.

Three groups, and they are legally different:

- **Linked in** — Go modules compiled into the `socrates` binary. All of them
  are MIT, BSD-3-Clause or ISC, which is why a Socrates binary can be MIT.
- **Started as a child process** — `tmux`, the four session programs and
  `cloudflared`. Separate executables, found on `PATH`, downloaded at runtime or
  baked into the image, never linked into anything.
- **Shipped in the web assets** — the xterm.js terminal and its addons, MIT,
  vendored as minified bundles under `internal/web/static/vendor/` and embedded
  into the binary. Shipping the binary ships those files, so their notice
  travels here.
- **Along for the ride in the Docker image** — the base image, the handful of
  Debian packages installed on top of it (`tmux` and `tini` among them), and the
  three CLIs.

Nothing is downloaded for the voice any more: reading an answer out loud is
Google Cloud Text-to-Speech, a web API called with a key you supply, so there
is no third party code involved in it at all. What is still downloaded is
`cloudflared`: `<data>/bin/cloudflared`, with `<data>` defaulting to
`~/.socrates`, and `/usr/local/bin/cloudflared` in the Docker image.

## Compiled into the binary

These are the Go modules linked into `socrates`. Their full licence texts are in
the module cache under `$(go env GOMODCACHE)`, and `go mod download` fetches
them from the sources named here.

| Module | Licence | Source |
| --- | --- | --- |
| `modernc.org/sqlite` | BSD-3-Clause | <https://gitlab.com/cznic/sqlite> — SQLite itself is public domain; this is a translation of it into Go |
| `modernc.org/libc` | BSD-3-Clause | <https://gitlab.com/cznic/libc> |
| `modernc.org/mathutil` | BSD-3-Clause | <https://gitlab.com/cznic/mathutil> |
| `modernc.org/memory` | BSD-3-Clause | <https://gitlab.com/cznic/memory> |
| `github.com/dustin/go-humanize` | MIT, © Dustin Sallings | <https://github.com/dustin/go-humanize> |
| `github.com/google/uuid` | BSD-3-Clause, © Google | <https://github.com/google/uuid> |
| `github.com/coder/websocket` | ISC, © Coder Technologies | <https://github.com/coder/websocket> — the WebSocket the terminal is carried over |
| `github.com/creack/pty` | MIT, © Keith Rarick | <https://github.com/creack/pty> — the pseudo terminals the browser watches tmux through |
| `github.com/remyoudompheng/bigfft` | BSD-3-Clause, © The Go Authors | <https://github.com/remyoudompheng/bigfft> |
| `github.com/mattn/go-isatty` | MIT, © Yasuhiro Matsumoto | <https://github.com/mattn/go-isatty> |
| `github.com/ncruces/go-strftime` | MIT, © Nuno Cruces | <https://github.com/ncruces/go-strftime> |
| `golang.org/x/sys` | BSD-3-Clause, © The Go Authors | <https://cs.opensource.google/go/x/sys> |

## cloudflared

The Cloudflare tunnel connector. Socrates starts it as a supervised child
process, downloads Cloudflare's own build into `<data>/bin` when remote access
is turned on and it is not already there, and the Docker image fetches the same
binary at build time.

- **Licence:** Apache-2.0, © Cloudflare
- **Source:** <https://github.com/cloudflare/cloudflared>

## tmux

Every session Socrates runs is a pane in a [tmux](https://github.com/tmux/tmux)
server that Socrates starts on a socket of its own. tmux is never linked in,
never modified and never vendored: Socrates looks for it on `PATH`, offers to
install it through the machine's own package manager, and the Docker image
installs it from Debian. It is the one dependency without which the product does
nothing at all.

- **Licence:** ISC, © Nicholas Marriott and contributors
- **Source:** <https://github.com/tmux/tmux>

`ncurses-term` travels with it in the image for the terminfo entries the CLIs
ask for: MIT-like (the ncurses licence), <https://invisible-island.net/ncurses/>.

## The session programs

A session runs one of four programs, and Socrates bundles none of them. Three
are the coding CLIs listed under the Docker image below; the fourth is the
machine's own shell — `$SHELL`, or `/bin/sh` — which is whatever the operating
system installs (`dash` under GPL-2.0 on Debian, `bash` under GPL-3.0 on most
Linux distributions, Apple's `zsh` under the zsh licence on macOS). Socrates
starts it as a child process, unmodified.

## In the Docker image

The image is a convenience: it carries the programs Socrates would otherwise
ask you to install. Each is its own separately licensed work, unmodified, and
each can be left out at build time — `INSTALL_AGENTS=0`,
`INSTALL_CLOUDFLARED=0`.

### Base image and system tools

`node:22-bookworm-slim`: Node.js under the MIT licence
(<https://github.com/nodejs/node>) on Debian bookworm, whose packages carry
their own licences — `apt-get changelog` and `/usr/share/doc/<package>/copyright`
inside the image are the authoritative list. On top of it Socrates installs
`ca-certificates` (the Mozilla CA bundle, MPL-2.0), `curl` (the curl licence, an
MIT/X derivative), `git` (GPL-2.0, <https://github.com/git/git>) and `ripgrep`
(MIT or Unlicense, <https://github.com/BurntSushi/ripgrep>).

`tmux` and `ncurses-term` are installed from Debian for the reason above, and
`tini` (MIT, © Thomas Orozco, <https://github.com/krallin/tini>) is the image's
entrypoint: it reaps the tmux server when that server is reparented to PID 1.

### The CLIs

Installed from npm, because a session bound to one of them cannot start without
it — a container with none of them can still run Shell sessions and nothing
else. Socrates starts each as an ordinary interactive program in a terminal; it
bundles, links and modifies no part of them, and it carries none of their
credentials.

| Package | Licence | Source |
| --- | --- | --- |
| `@anthropic-ai/claude-code` | proprietary — Anthropic's commercial terms, see the package's own README | <https://github.com/anthropics/claude-code> |
| `@openai/codex` | Apache-2.0 | <https://github.com/openai/codex> |
| `opencode-ai` | MIT | <https://github.com/anomalyco/opencode> |

## Shipped in the web assets

The terminal in the browser is [xterm.js](https://xtermjs.org/) and five of its
addons, vendored as the minified UMD bundles the packages publish — no CDN, so
the app loads with no network at all. They are embedded into the `socrates`
binary by `//go:embed`, which means every copy of the binary is a copy of them.

All six are **MIT**, © 2017-2019 The xterm.js authors,
© 2014-2016 SourceLair Private Company and © 2012-2013 Christopher Jeffrey,
as the file itself states. The full text is in
[`internal/web/static/vendor/LICENSE-xterm`](internal/web/static/vendor/LICENSE-xterm),
which is the licence file as `@xterm/xterm` publishes it and covers the addons
from the same repository.

| Package | Version | Licence | Registry |
| --- | --- | --- | --- |
| `@xterm/xterm` | 6.0.0 | MIT | <https://registry.npmjs.org/@xterm/xterm> |
| `@xterm/addon-fit` | 0.11.0 | MIT | <https://registry.npmjs.org/@xterm/addon-fit> |
| `@xterm/addon-unicode11` | 0.9.0 | MIT | <https://registry.npmjs.org/@xterm/addon-unicode11> |
| `@xterm/addon-web-links` | 0.12.0 | MIT | <https://registry.npmjs.org/@xterm/addon-web-links> |
| `@xterm/addon-webgl` | 0.19.0 | MIT | <https://registry.npmjs.org/@xterm/addon-webgl> |
| `@xterm/addon-clipboard` | 0.2.0 | MIT | <https://registry.npmjs.org/@xterm/addon-clipboard> |

The pin is `internal/web/static/vendor/VERSIONS` and `make vendor-xterm`
re-downloads exactly that set, checking it against `vendor/SHA256SUMS`. The
`.js.map` files the tarballs also carry are deliberately not shipped.
