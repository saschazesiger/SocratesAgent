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

Before it opens a pane at all it answers the two questions Socrates asks a CLI
as a plain subprocess: `--version`, under every name, and the model listing —
`codex debug models` with the document Codex prints, `opencode models` (and
`models --json`) with the ids OpenCode prints. Without those every fake would
have an empty catalogue and the sheet's model picker would go untested.

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
gitignored. Deleting `e2e/out/` is always safe.

## The scenarios

| name | what it proves |
| --- | --- |
| `createshell` | the sheet makes a Shell session, the shell answers, and the page and the pane are the same white — with the dimmest ANSI colour still legible on it |
| `typeandsee` | keystrokes reach the pane, output comes back in order, and the journal on disk holds the same bytes |
| `reloadkeepsscreen` | type, reload: the same tab, the same session, the same screen, and typing still works |
| `pages` | `/`, `/admin`, `/login` and `/setup` are clean at 390×844 and 1280×720, and the sheet is a bottom sheet on one and a dialog on the other |
| `harnesses` | all four session types start and are seen in the browser, each with its own mark and its detail behind **Info** in the row menu |
| `sessionlist` | rename, archive, unarchive and delete — and the working directory survives the delete |
| `exitoverlay` | `/exit 7` raises the overlay with its status in a plain line under it, and **Restart** brings the session back |
| `webglrenders` | the shipped renderer paints the terminal |
| `touchscroll` | a finger dragged down the pane reaches what scrolled off it, a drag back up returns to the live bottom, a tap is still the tap that puts the keyboard on the pane, and with tmux's mouse off the drag types nothing |
| `clipboard` | Ctrl-V pastes the clipboard instead of sending 0x16 — the byte Claude Code answers with "No image found in clipboard" — bracketed when the program asked for it; a Shift-drag selects and Ctrl-C copies it without interrupting; with nothing selected Ctrl-C is still 0x03; the key bar's **Paste** sends the same text; a plain drag sends tmux no mouse report, selects in the browser and is on the clipboard the moment the button is released; a plain click is still reported to the program; a double click copies the word; a right click is not reported; a middle click pastes the last selection exactly once, not once from us and once from the browser; Ctrl-V and Ctrl-C with the focus on a header button still reach the pane; an OSC 52 printed by the program, through tmux, sets the clipboard; and a drag down two rows copies both of them |
| `touchcopy` | a finger held on the pane selects the word under it and lifting it copies, with a **Copied** toast and no click sent; held then moved it grows the selection; a short tap is still a tap |
| `keybar` | no device gets the key bar until the ⋯ menu asks for it, on a phone or at a desk; asked for, it sends the right bytes, a sticky `Ctrl` turns the next letter into a control code, and a physical keystroke does not take it away again |
| `offlineonce` | a whole command typed with the network off arrives **exactly once** when it comes back, the lost connection is visible while it is gone, and the app shell still opens offline |
| `sigtermreattach` | the server is killed mid-session and restarted; the pane still holds what was typed and the session is running |
| `takeover` | a second tab with the same viewer id closes the first socket with 1012 and drives the session |
| `offlinerestart` | the server is restarted inside an outage and the phone wakes with `online`, `focus` and `visibilitychange` at once: one handshake, and nothing typed is lost in silence |
| `latehello` | what is typed before a slow `hello` arrives is delivered, exactly once, and leaves nothing behind |
| `adminoptions` | every harness option round-trips and reaches the command line |
| `tmuxinstaller` | the engine card, and an install that streams and survives a reload |
| `twoviewers` | two devices on one session: both see the same pane, and the size notice is shown once |
| `backpressure` | two hundred lines printed as fast as they can be arrive whole, on screen and in the journal |
| `deletekeepsdir` | delete kills the tmux session and removes the row, and leaves the work on disk |
| `recoveredsession` | a `soc_*` tmux session with no row is taken in as "Recovered session", never killed |
| `createclaude` | the sheet's model list is the catalogue's, the picked model and the `--session-id` reach Claude Code, and **Restart** resumes the conversation it already had — one press, one relaunch |
| `createcodex` | Codex is launched with `--strict-config` and its working directory trusted in the same command line, and the conversation it writes on its first turn is discovered and resumed |
| `createopencode` | OpenCode gets a port and a password of its own, and the discoverer reads the session id over its authenticated HTTP server |
| `rebootresume` | the tmux server is killed behind Socrates' back: the row goes to `needs_resume`, opening it relaunches with **--resume**, and the banner says so once and stays away after it is dismissed |
| `lighttheme` | the theme Codex was told to wear, the `theme=light` it read back through tmux, the white pane, and all sixteen ANSI colours drawn at 4.5:1 or better |
| `activity-claude`, `activity-codex`, `activity-opencode`, `activity-shell` | each harness working, finishing while nobody is looking, and the row that says so |
| `activity-waiting` | a permission prompt: a still amber ring, and no timeout |
| `activity-fallback` | a harness that hangs, and the row that leaves busy anyway |
| `unread` | bold when nobody saw it, gone when the row is opened or typed into |
| `session-title` | a session that names itself the first time it answers, exactly once |
| `status-speak` | **Status** spins while it asks, the answer lands on screen as words, and the same words go to `/api/voice/speak` once — the request is intercepted in the browser, so no scenario ever calls Google |
| `status-ticker` | the phases of a status — reading the screen, asking the model, speaking, the answer — arrive in order in one line |
| `agent-run` | a request that needs the keyboard: a run inside the message that asked for it, a **Cancel** beside it, real keystrokes in the pane, and an ending in the conversation |
| `chat-text` | the chat as a column beside the terminal: a question answered in words with markdown-lite, a reload that comes back to it, and a request that types |
| `chat-dictate` | the chat's microphone: **Send recording** and **Discard recording** in place of the mic while it runs, a discarded one that transcribes nothing, a spoken question posted with `auto:true` and its answer read out loud, a typed one with `auto:false` and silence, and any answer read again by double-tapping it |
| `no-overlap` | speech-to-text and text-to-speech are never open at once: **Status** while the microphone runs says nothing out loud, the same press with it closed does, and a recording that starts silences what is playing |
| `typeafteroutage` | a cut socket, a locked phone, and a pane that still takes keystrokes |
| `typekeepsfocus` | a dialog, the ⋯ menu and two sessions: the keys still land in the pane |
| `design` | white surfaces, a mark wherever a program is named, technical strings only in **Info**, and an animation that does not restart when a row re-renders |
| `daygroups` | the session list read by day: Today, Yesterday, This week, This month, Older — a header only over a group with something in it, and a row that keeps its element when it moves to another day |
| `notify` | a session that stops working while nobody is looking: one chime and one notification named after it, a speaker and a bell in the header that each turn one of them off and are remembered on this device, and nothing fired for a handshake replay |
| `livesession` | one real session against the real Claude Code CLI (gated) |

`lighttheme` measures the
colours the renderer actually **drew** rather than the palette they came from:
eleven of `LIGHT_THEME`'s eighteen values are deliberately not 4.5:1 against
white, and what makes them legible is `minimumContrastRatio: 4.5`, which
re-derives them at draw time.

## Dictation needs a microphone and a gateway

`chat-dictate` asks Chromium for its own fake microphone
(`--use-fake-device-for-media-stream`) and points `openrouter.base_url` at a
small HTTP stub the scenario starts. It has to be a real server rather than a
Playwright route: the browser posts the recording to `/api/voice/transcribe`
and the **server** is what calls the gateway, which a route in the page cannot
see.

## Screenshots for the README

`node e2e/shots.mjs` regenerates `docs/screenshot-session.png`,
`docs/screenshot-phone.png` and `docs/screenshot-admin.png` — real sessions in a
real browser, with the fake CLI standing in for the three programs.
`docs/screenshot-tunnel.png` is hand-made and is not regenerated. It is not part
of `make e2e`: the pictures are committed, so this is only run when they need
redoing.
