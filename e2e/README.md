# The end-to-end suite

Every scenario drives the real `socrates` binary through a real browser, against
real tmux sessions. Each one boots a server on its own port with its own
temporary data directory, completes setup the way a person would, and then
asserts against what the page actually shows — printing the value it measured
beside every verdict.

```
make e2e                     # the whole suite
node e2e/run.mjs             # the same thing
node e2e/run.mjs createshell # one scenario, by name
node e2e/run.mjs pages harnesses
```

`make e2e` is deliberately not part of `make check` and not part of CI: it wants
node, a Chromium and tmux, and it starts real terminal sessions, which is
exactly what it is there to test.

## What it needs

* **Go, node and tmux ≥ 3.3.** Nothing is installed from npm; Playwright is used
  straight from the machine's own copy at
  `/opt/browser-testing/node_modules/playwright-core/index.mjs`.
* **A Chromium.** The default is this machine's headless shell at
  `/root/.cache/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell`,
  launched with `--no-sandbox` because the suite runs as root in a container.
  Point `SOCRATES_E2E_CHROME` at another binary to use one.
* **Ports 5000 and up**, one per scenario.

Pages are always waited for with `waitUntil: 'domcontentloaded'`. Never
`networkidle`: a terminal socket never idles, so `networkidle` would hang until
it timed out on every single navigation.

## The fake CLIs

A suite that needed three logged-in accounts would not be a suite, so
`e2e/fakebin/faketui` is built once per run and linked onto `PATH` as `claude`,
`codex` and `opencode`. It is a **PTY TUI**, not a protocol speaker — which is
what the product now runs. It prints a banner naming itself, its working
directory, its `--model` and the background colour it was told about, echoes
every line back as `you said: …`, and understands `/exit <n>`, `/spin`, `/alt`
and `/id`. It also writes the same state files the real binaries write and
refuses the same way they refuse, so the discovery and resume paths are
exercised rather than mocked.

The Shell harness needs no fake at all: `/bin/sh` is the thing under test.

Every CLI's own state — `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `XDG_DATA_HOME`,
`HOME` — is redirected under the run's data directory, so a run can neither read
nor write the machine's real credentials, and `FAKE_LOG` records every launch as
one JSON object per line for the scenarios that assert on argv.

## Reading the terminal

The suite reads the pane out of the DOM, so the scenarios that do it first turn
the WebGL renderer off through the dashboard's own setting
(`terminal.webgl: false`) — this machine's headless Chromium has a software
WebGL context, so the shipped default would paint into a canvas nothing can read
back. `webglrenders` keeps the default and asserts that it really does paint, so
the shipped path is not the untested one.

## Every scenario leaves nothing behind

Killing the server is *not* enough: a Socrates restart deliberately leaves its
tmux sessions running — that is the durability feature the product exists for —
so a scenario that only kills the server leaks one tmux session per session it
created.

`stop()` therefore deletes every session the scenario made, *then* takes the
server down, and then **asserts** that no session row and no tmux session or
tmux server survives on `<data>/tmux.sock`. Both are ordinary assertions and
appear in every scenario's count, so a scenario that starts leaking fails rather
than quietly littering. A `tmux kill-server` backstop runs afterwards regardless.

The suite still has the machinery for a quarantined scenario, and nothing uses
it. `scenario(name, title, fn, { quarantine: 'why' })` lets a scenario print
every verdict without failing the run, for a defect that is understood and
reproduced but lives in a file the work package does not own.
`SOCRATES_E2E_STRICT=1` takes that exemption away.

## The live scenario

`livesession` is the one scenario that talks to a real CLI: a plain build with
no fakes on `PATH`, a real Claude Code session, and `/status` typed into it. It
must never spend tokens on a model turn. It is gated —

```
SOCRATES_LIVE_AGENTS=1 node e2e/run.mjs livesession
```

— and skipped, loudly, otherwise.

## Artefacts

Screenshots and anything else the run writes go to `e2e/out/`, which is
gitignored. `e2e/out/voice-cache/` is a Piper install shared by every server the
suite starts, so a fresh data directory does not mean a 25 MB download per
scenario. Deleting `e2e/out/` is always safe.

## The scenarios

| name | what it proves |
| --- | --- |
| `createshell` | the sheet makes a Shell session, the shell answers, and the page and the pane are the same white — with the dimmest ANSI colour still legible on it |
| `typeandsee` | keystrokes reach the pane, output comes back in order, and the journal on disk holds the same bytes |
| `reloadkeepsscreen` | type, reload: the same tab, the same session, the same screen, and typing still works |
| `pages` | `/`, `/admin`, `/login` and `/setup` are clean at 390×844 and 1280×720, and the sheet is a bottom sheet on one and a dialog on the other |
| `harnesses` | all four session types start and are seen in the browser, each with its own mark and its detail behind an "i" |
| `sessionlist` | rename, archive, unarchive and delete — and the working directory survives the delete |
| `exitoverlay` | `/exit 7` raises the overlay with its status behind the "i", and **Restart** brings the session back |
| `webglrenders` | the shipped renderer paints the terminal |
| `keybar` | at 390×844 the key bar sends the right bytes, a sticky `Ctrl` turns the next letter into a control code, and the line input sends a whole line with one `\r` |
| `dictation` | the microphone records, the server transcribes through a stubbed gateway, and the words land in `#lineInput` — unsent |
| `offlineonce` | a whole command typed with the network off arrives **exactly once** when it comes back, the lost connection is visible while it is gone, and the app shell still opens offline |
| `sigtermreattach` | the server is killed mid-session and restarted; the pane still holds what was typed and the session is running |
| `takeover` | a second tab with the same viewer id closes the first socket with 1012 and drives the session |
| `offlinerestart` | the server is restarted inside an outage and the phone wakes with `online`, `focus` and `visibilitychange` at once: one handshake, and nothing typed is lost in silence |
| `adminoptions` | every harness option round-trips and reaches the command line |
| `tmuxinstaller` | the engine card, and an install that streams and survives a reload |
| `livesession` | one real session against the real Claude Code CLI (gated) |

Scenarios 13–22 of the specification's table — the per-CLI creates, the
multi-viewer and recovery cases, the theme and design measurements — arrive with
the work packages that build what they measure.

## Dictation needs a microphone and a gateway

`dictation` asks Chromium for its own fake microphone
(`--use-fake-device-for-media-stream`) and points `openrouter.base_url` at a
small HTTP stub the scenario starts. It has to be a real server rather than a
Playwright route: the browser posts the recording to `/api/voice/transcribe`
and the **server** is what calls the gateway, which a route in the page cannot
see.

## Screenshots for the README

`node e2e/shots.mjs` regenerates the screenshots in `docs/`. It still drives the
chat app and is rewritten with the rest of the documentation in WP10; it is not
part of `make e2e`.
