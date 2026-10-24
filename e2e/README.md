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
* **A Chromium.** The default is the newest headless shell Playwright installed
  under `/root/.cache/ms-playwright/chromium_headless_shell-<build>/`, launched
  with `--no-sandbox` because the suite runs as root. Point
  `SOCRATES_E2E_CHROME` at another binary to use one. The headless shell
  always reports the notification permission as denied, so the `notify`
  scenario stubs both `Notification` and the worker registration's
  `showNotification`; a real notification needs the full `chrome` binary.
* **Ports 5000 and up**, one per scenario.

Pages are always waited for with `waitUntil: 'domcontentloaded'`. Never
`networkidle`: a terminal socket never idles, so `networkidle` would hang until
it timed out on every single navigation.

## The bucket

Socrates runs without a bucket - the data directory is its only store, and S3
holds a backup of it - so a scenario starts with none, and a server is started
with `SOCRATES_S3_BUCKET` empty so that the environment the suite runs in can
never hand it one. The `backup` and `restore` scenarios want one:
`e2e/fakebin/fakes3` - `internal/s3/s3fake`, the in-memory store the backup's
unit tests use, with real SigV4 verification - is built and started on a
loopback port the first time a scenario asks for it (`backupForm(prefix)` in the
harness), and each scenario backs up under a prefix of its own
(`e2e/<data directory name>/`). `/_fakes3/down?status=503` and `/_fakes3/up`
break the bucket on purpose (`fakeS3Switch`); nothing about the fake is secret
and it dies with the run. `SOCRATES_BACKUP_WARN_AFTER` shortens the day without
a backup after which the warning shows, for the `backup` scenario.

## The fake CLIs

A suite that needed three logged-in accounts would not be a suite, so the
scripted CLIs - `e2e/fakebin/fakeclaude`, `fakecodex` and `fakeopencode`, each
speaking its agent's own protocol as the chats drive it (stream-json, the app
server, the HTTP API) - are built once per run and put on `PATH` as `claude`,
`codex` and `opencode`. They answer `--version` and the model listings the
catalogue asks for, so the sheet offers all three agents with their models.
The chat scenarios also use the scripted agent (`harness: "fake"`,
`SOCRATES_FAKE_AGENT=1`, docs/design/AGENT-HOST.md §8).

The agents do not run in a terminal any more (REBUILD.md §7), and the PTY
stand-in that played them there, `faketui`, went with them (WP10). The
scenarios written for the agents' terminals are being moved onto the chats
(WP11); until then the ones that still start an agent's terminal fail where
the server refuses it.

The Shell needs no fake at all: `/bin/sh` is the thing under test.

Every CLI's own state — `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `XDG_DATA_HOME`,
`HOME` — is redirected under the run's data directory, so a run can neither read
nor write the machine's real credentials, and `FAKE_LOG` records what the fakes
were asked.

## The chat API (no browser yet)

`node e2e/chats.mjs` drives the rebuild's chat routes and the chat stream against
the real binary, with real agent host processes running the scripted agent
(`SOCRATES_FAKE_AGENT=1`, docs/design/AGENT-HOST.md §8). It kills the server
with SIGKILL in the middle of a turn, starts it again and checks that the stream
continues exactly where it stopped, then covers duplicate sends and two devices
answering one question. It is not part of `run.mjs` until the chat view (WP8)
gives it a page to look at.

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
| `reloadkeepsscreen` | type, reload: Home, as every start is, and the session opened again is the same viewer with the same screen, and typing still works |
| `pages` | `/`, `/admin`, `/login` and `/setup` are clean at 390×844 and 1280×720, and the sheet is a bottom sheet on one and a dialog on the other |
| `harnesses` | all four session types start and are seen in the browser, each with its own mark and its detail behind **Info** in the row menu |
| `sessionlist` | rename, flag and unflag, archive, unarchive and delete — and the working directory survives the delete |
| `browserlayout` | row menus flip upwards near the bottom and stay scrollable in short viewports; delayed local fonts give DOM and WebGL equal-width cells and matching initial pane sizes |
| `exitoverlay` | a Shell's `exit 7` raises the overlay with its status in a plain line under it, and **Restart** brings the session back |
| `webglrenders` | the shipped renderer paints the terminal |
| `touchscroll` | a finger dragged down the pane reaches what scrolled off it, a drag back up returns to the live bottom, a tap is still the tap that puts the keyboard on the pane, and with tmux's mouse off the drag types nothing |
| `clipboard` | Ctrl-V pastes the clipboard instead of sending 0x16 — the byte Claude Code answers with "No image found in clipboard" — bracketed when the program asked for it; a Shift-drag selects and Ctrl-C copies it without interrupting; with nothing selected Ctrl-C is still 0x03; the key bar's **Paste** sends the same text; a plain drag sends tmux no mouse report, selects in the browser and is on the clipboard the moment the button is released; a plain click is still reported to the program; a double click copies the word; a right click is not reported; a middle click pastes the last selection exactly once, not once from us and once from the browser; Ctrl-V and Ctrl-C with the focus on a header button still reach the pane; an OSC 52 printed by the program, through tmux, sets the clipboard; and a drag down two rows copies both of them |
| `touchcopy` | a finger held on the pane selects the word under it and lifting it copies, with a **Copied** toast and no click sent; held then moved it grows the selection; a short tap is still a tap |
| `transfer` | the **Upload** button writes a file into the session's working directory under `.socrates/uploads/`, with a `.gitignore`, and puts its path on the prompt; an image pasted into the pane does the same; **Paste** puts the clipboard on the prompt; **Copy** with nothing selected opens the pane's text in a sheet and **Copy all** puts it on the clipboard; on a phone the three float over the pane and the top bar still fits |
| `keybar` | a touch screen gets the key bar before anything asks for it and a desk does not, the ⋯ menu hides and shows it and is remembered per device; it sends the right bytes, a sticky `Ctrl` turns the next letter into a control code, and a physical keystroke does not take it away again |
| `offlineonce` | a whole command typed with the network off arrives **exactly once** when it comes back, the lost connection is visible while it is gone, and the app shell still opens offline, on Home, without claiming there are no sessions |
| `sigtermreattach` | the server is killed mid-session and restarted; the pane still holds what was typed and the session is running |
| `takeover` | a second tab with the same viewer id closes the first socket with 1012 and drives the session |
| `offlinerestart` | the server is restarted inside an outage and the phone wakes with `online`, `focus` and `visibilitychange` at once: one handshake, and nothing typed is lost in silence |
| `latehello` | what is typed before a slow `hello` arrives is delivered, exactly once, and leaves nothing behind |
| `adminoptions` | every harness option round-trips and reaches the command line |
| `tmuxcard` | the engine card on a machine whose tmux is too old |
| `twoviewers` | two devices on one session: both see the same pane, and the size notice is shown once |
| `backpressure` | two hundred lines printed as fast as they can be arrive whole, on screen and in the journal |
| `deletekeepsdir` | delete kills the tmux session and removes the row, and leaves the work on disk |
| `recoveredsession` | a `soc_*` tmux session with no row is taken in as "Recovered session", never killed |
| `createclaude` | the sheet's model list is the catalogue's, the picked model and the `--session-id` reach Claude Code, and **Restart** resumes the conversation it already had — one press, one relaunch |
| `createcodex` | Codex is launched with `--strict-config` and its working directory trusted in the same command line, and the conversation it writes on its first turn is discovered and resumed |
| `createopencode` | OpenCode gets a port and a password of its own, and the discoverer reads the session id over its authenticated HTTP server |
| `rebootresume` | the tmux server is killed behind Socrates' back: the row goes to `needs_resume`, opening it relaunches with **--resume**, and the banner says so once and stays away after it is dismissed |
| `lighttheme` | the white pane, and all sixteen ANSI colours a Shell prints drawn at 4.5:1 or better |
| `activity-claude`, `activity-codex`, `activity-opencode` | each agent working, finishing while nobody is looking, and the row that says so. There is no `activity-shell`: a shell is never given a turn, so its row carries no status at all |
| `activity-waiting` | a permission prompt: a still amber ring, and no timeout |
| `unread` | bold when nobody saw it, gone when the row is opened or typed into |
| `session-title` | a session that names itself the first time it answers, exactly once; the arrow beside the name names it again from its screen; a prepared session is named after its prompt the first time its field is left, and only then |
| `status-speak` | **Status** wears a turning ring around its mark — never the mark itself, and still turning under `prefers-reduced-motion` — while it asks, the answer lands on screen as words, and the same words go to `/api/voice/speak` once — the request is intercepted in the browser, so no scenario ever calls Google |
| `status-ticker` | the phases of a status — reading the screen, asking the model, speaking, the answer — arrive in order in one line |
| `agent-run` | a request that needs the keyboard: a run inside the message that asked for it, a **Cancel** beside it, real keystrokes in the pane, and an ending in the conversation |
| `chat-text` | the chat as a column beside the terminal: a question answered in words with markdown-lite, a reload that comes back to it, and a request that types |
| `chat-dictate` | the chat's microphone: **Send recording** and **Discard recording** in place of the mic while it runs, a discarded one that transcribes nothing, a spoken question posted with `auto:true` and its answer read out loud, a typed one with `auto:false` and silence, and any answer read again by double-tapping it |
| `no-overlap` | speech-to-text and text-to-speech are never open at once: **Status** while the microphone runs says nothing out loud, the same press with it closed does, and a recording that starts silences what is playing |
| `typeafteroutage` | a cut socket, a locked phone, and a pane that still takes keystrokes |
| `typekeepsfocus` | a dialog, the ⋯ menu and two sessions: the keys still land in the pane |
| `keyboard` | on a touch screen only the top bar's button raises the on-screen keyboard: the pane and every field muted with `inputmode="none"` **and** `readonly` until it is pressed, a second press puts it away, the focus leaving every field puts the switch back, a copy of the button laid over the same spot in every dialog raises it for that dialog's field, a muted field still filled by speaking into it, and no button and nothing muted on a device with a mouse |
| `design` | white surfaces, a mark wherever a program is named, technical strings only in **Info**, and an animation that does not restart when a row re-renders |
| `folders` | the session list read by project: one folder per working directory, named after it with the whole path on hover, ordered by the project worked in most recently, `More Chats` last for the sessions Socrates housed itself; a folder shuts, says how many are behind it, is found shut after a reload and is opened again by attaching a session inside it; a folder with nothing in it is not drawn, and a row keeps its element across a re-render |
| `notify` | a session that stops working while nobody is looking: one chime and one notification named after it, a speaker and a bell in the header that each turn one of them off and are remembered on this device, and nothing fired for a handshake replay or for a session that has only just been started |
| `prestart` | the sheet's PreStart switch (offered for an agent, not a shell) makes a prepared session and nothing runs; its prompt saves itself, reaches the server as typed and says **Saved**; a model picked in the panel is saved; a reload keeps both; **Start** turns the panel into a live terminal whose program got the prompt byte for byte with the chosen model; the prompt stays, read-only; a time picked in the panel is saved and shown as a clock on the row, and a start set five seconds ahead happens by itself and flips the open panel to the terminal |
| `backup` | the Backup card: a bucket configured from it (Test connection, the secrets never shown again), **Back up now** ending in "Backed up" with its snapshot listed as this machine's; with the bucket down and something changed, a run that says it failed with the bucket's words behind the "i"; the day without a backup (20 s here) warned about in `/api/state`, on the card and on the session page; the next good run takes the warning away |
| `restore` | server A is backed up and then lost with its data directory and tmux; server B, a new data directory, offers **Restore from a backup** on its setup page, refuses a wrong passphrase at its field, lists the snapshot, restores it, starts again on it and goes to the sign-in, where A's password works; both sessions are there under their ids and names (the one whose pane was on A not taken for running), with the prompt and a setting intact, and B backs up into the same bucket under a machine id of its own |
| `home-landing` | every start of the page is Home, a reload of a session's address too; the Home button beside the collapse control (and in the phone's drawer) brings it back and says so with `aria-pressed`; Back and Forward move between `#home` and `#<id>` |
| `home-live` | sessions made on another device are counted, and one that starts working, one that asks, one that ends and one that finishes unseen move Home's numbers within seconds - with no reload and no terminal socket at all; a card filters the list, a second tap shows everything; unread work is Finished even on a stopped session, and a background session is Ready but still asks |
| `outcome` | with the outcomes on, a turn's end carries the hourglass and sits under Background tasks while its verdict is read, and the chime and the notification wait; the read is given Claude's instruction and task list; the verdict lands on the row, on Home with what is still open, in the notification and in the header's tag, survives a reload, goes with the next turn, and goes when the outcomes are switched off |
| `home-question` | the Questions card opens the dialog at the question the digest read off the screen: context, the question and its options as 64 px buttons in 20 px type; a tap and Send reach the pane through the operator, the session leaves `waiting`, and the dialog says so and closes |
| `home-own-answer` | an answer of one's own replaces the chosen option and is typed on the menu's "Type something." row |
| `home-queue` | two questions walked oldest first ("1 of 2", Next and Previous), a second tap that sends, and the next question taking the dialog; a `#q=` link followed while "All questions answered" is shown opens its session instead of being dropped; one button is called Next question, options are toggle buttons, and a Home row is a title button beside its Answer button |
| `home-elsewhere` | a question answered in the terminal while the dialog shows it: "answered elsewhere", and no operator run; an answer to a session that asks nothing is refused (409) and nothing is typed; a question answered at the machine while the dialog's answer is on its way ends on "answered elsewhere" with no field for the idle prompt; a program killed under its open question says it stopped before it was answered, offers Open session and stays |
| `home-deeplink` | `#q=<id>` opens the question on boot, `#open=<id>` the session, and a plain `#<id>` lands on Home; Back after either leaves the app; `#q=` for an idle or unknown session says so; a notification tapped on a tab offline since before the question says it is looking (never "answered elsewhere") and shows the question once the network is back; signed out, `#q=` goes through the sign-in and opens the question after it |
| `home-reconnect` | the server restarted under an open Home: "Reconnecting…", then "Live", the right numbers and the question read again, on the same page; a page hidden for 3 s keeps its stream; signing out closes that login's other tab with 4401 and sends it to the sign-in while a device with its own login is untouched; a new password does the same to every other login; a longer outage (several refused handshakes) with the question answered at the machine meanwhile says "Reconnecting…" then "Live", never "Updated every 5 s", and the answered question is no longer counted |
| `home-touch` | a Tesla's user agent at 1280×800 and a phone at 390×844: touch mode, a dialog that is the whole screen, options at least 64 px high, the keyboard button's copy in the dialog, and nothing wider than the phone; Read aloud keeps its name on a phone, and on a phone on its side (844×390, 740×360) the first option is whole on screen; a double tap on Send, whose second tap lands where Cancel appears, sends the answer once and cancels nothing; in the car a double tap on the Questions card (its first option is drawn under the finger) chooses nothing and a triple tap sends nothing, while a deliberate tap a moment later chooses |
| `home-focus` | the field of one's own answer keeps the keyboard and its words while a third session asks, the session is renamed and the question is read again; Enter sends the words; a Read aloud or dictation that fails is said in the dialog and stays said |
| `home-retry` | an operator error shows Try again, which sends once and answers; a session deleted mid-run keeps the run's error with no Try again and is never "answered elsewhere"; one deleted while the answer is on its way says it no longer exists |
| `home-runlost` | the dialog opened again mid-answer shows the answer going in (progress, Cancel, options locked) and ends on "All questions answered"; a server restart mid-run ends in "interrupted" with Try again, never on "Answering…"; an answer whose reply is lost on the way back (the server took it) is followed to "All questions answered" and never called "Nothing was answered"; a dialog shut before the reply to its Send, opened again, follows the answer as its own, never "another device" |
| `home-opencode` | OpenCode's question tool (`question.asked`) reaches Home and is answered from the dialog; the card numbers its rows and shows its cursor in colour only, as OpenCode's does, and an answer of one's own goes in by the last row's digit; its permission card (`/question permit`: Allow once, Allow always and Reject side by side, the highlight in colour only) is answered Allow always from the dialog and Reject over the API, each by Right and Enter |
| `home-stream` | a list that lands late does not bring back a deleted session or a finished turn; through a proxy, a network that goes away under a live stream is "Reconnecting…" then "Offline", never "Updated every 5 s", and Live again after; a handshake that hangs is retried and recovers, a refused upgrade leaves Home polled ("Updated every 5 s", not dimmed), and offline says its age in seconds |
| `home-quiet` | a notification tapped on a stream gone half-open (sockets open, nothing passing) waits for the server and shows the question, never "no question"; a page left alone on that stream says "Checking…" with its numbers dimmed 2 s after its unanswered ping and within 15 s of the silence, and is Live with the question within 25 s; a page that can only poll gets a question asked in prose read again after a server restart, and answerable; a page shown again after 33 s hidden, on an 800 ms link, says Reconnecting…, never Offline; a session being named when the server restarted stops pulsing; a page started from the worker's cache with the server gone says Offline, not "Connecting…" for ever, and is Live once it is back |
| `home-kinds` | every kind of question end to end through the dialog, the operator and the fake: a multi-select (read as a menu even when Home is polled in the moment before its status says waiting - never as a question in prose; taps toggle, Send submits), `/question multiq` ("Question 1 of 2", a second tap goes on, the review is submitted), Read aloud on a plan approval saying the plan (the digest's detail) after the question, a plan sent back with feedback that comes back revised and is approved only by the person, and a question asked in prose that Home counts and whose "Finished" notification opens the dialog |
| `home-unread` | a turn that ends on a question in prose while the gateway refuses is read again once it answers, with Home open and no reload, and counted; with no key it is not read, and a key saved from another device gets it read and counted on the open Home |
| `home-honest` | `/api/home` and `/api/sessions` come gzipped; on a device whose clock is ten minutes fast a question asked a moment ago says "just now"; the dialog says nothing about its connection while live, and offline says so, dims the question and says it may already have been answered, until it is live again |
| `home-language` | with `voice.language` German, a `/question yesno` menu (two tabs that share Yes and No) is read in German while each tab keeps the screen's own words (`screen_text`, from Claude's transcript), and Yes on one tab and No on the other go in, each on its own tab |
| `home-restart` | a server restart under an open dialog on three questions (two menus, one asked in prose): Home counts all three throughout, the walk stays "of 3" and never says "answered elsewhere", each question keeps the time it started waiting, and a choice made before the restart is still chosen and goes in; a question in prose answered at the machine while the server was down, read slowly by the new server, says "Checking whether it still asks…" with no Answer while it is read, and then no longer asks |
| `home-devices` | two devices on one question: the second's dialog follows the first's answer (options locked, "Another device is answering: Postgres", "Answered on another device"), a Send that crossed it is told another device is already answering, only one answer goes in, and neither is counted as the second's; a second tab of the same browser is never told another device answered; a second device whose Send is delayed 2 s on its uplink, overtaken by the first device's Send 300 ms later, says another device answered and never "Answered" or "All questions answered" |
| `home-followup` | the program asks its next question right after the dialog's answer, and each read takes 2.5 s: the open dialog takes the follow-up up once it is read, never closes while Home counts it, keeps Send off while it is being read, and the follow-up gets only the answer given to it |
| `livesession` | one real session against the real Claude Code CLI (gated) |

`lighttheme` measures the
colours the renderer actually **drew** rather than the palette they came from:
eleven of `LIGHT_THEME`'s eighteen values are deliberately not 4.5:1 against
white, and what makes them legible is `minimumContrastRatio: 4.5`, which
re-derives them at draw time.

## Dictation needs a microphone and a gateway

`dictate` and `keyboard` ask Chromium for their own fake microphone
(`--use-fake-device-for-media-stream`) and points `openrouter.base_url` at a
small HTTP stub the scenario starts. It has to be a real server rather than a
Playwright route: the browser posts the recording to `/api/voice/transcribe`
and the **server** is what calls the gateway, which a route in the page cannot
see.

## Home and the gateway stub

Every start of the page lands on Home (HOME.md §2.3), a reload of a session's
address included, so a scenario that reloads a session goes back to it through
the address once the list has its row (`reloadBack`), and a new tab is opened
at `#open=<id>`.

A server with an OpenRouter key digests every coding session by itself, at
moments no scenario chooses. `openRouterStub` tells those calls apart by the
sentence a digest instruction opens with, answers them "nothing is asked",
keeps them in `stub.digests` rather than `stub.calls`, and never hands them an
entry of the reply queue - so a scenario that counts or scripts its own calls
is not thrown off by them. The Home scenarios pass `answer`, which reads each
request: the digest finds the fake's menu on the screen, and the operator
presses the digit the screen shows for the answer in its goal. The Home
scenarios type into a pane through `tmux send-keys` when the point is that no
browser is attached to it.

## Screenshots for the README

`node e2e/shots.mjs` regenerates `docs/screenshot-session.png`,
`docs/screenshot-phone.png` and `docs/screenshot-admin.png` — real sessions in a
real browser, with the fake CLI standing in for the three programs.
`docs/screenshot-tunnel.png` is hand-made and is not regenerated. It is not part
of `make e2e`: the pictures are committed, so this is only run when they need
redoing.

### Browser IDE

The optional `vscode-check.mjs` smoke test uses a real code-server installation.
It checks loading the embedded workbench, editing and saving a project file,
switching between IDE and shell, surviving a Socrates restart, and resuming
following loss of the tmux server. It creates and removes its own sessions.

```bash
CODE_SERVER_BINARY=/absolute/path/to/code-server node e2e/vscode-check.mjs
# Set SOCRATES_E2E_CHROME if Chromium is installed outside the harness default.
```
