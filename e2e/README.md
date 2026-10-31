# The end-to-end suite

Every scenario drives the real `socrates` binary through a real browser: its
chats run real agent host processes against the protocol fakes of Claude Code,
Codex and OpenCode, and its Shell is a real shell in a real tmux pane. Each
scenario boots a server on its own port with its own temporary data directory,
completes setup the way a person would, and then asserts against what the page
actually shows and what the fakes were told — printing the value it measured
beside every verdict.

```
make e2e                       # the whole suite
node e2e/run.mjs               # the same thing
node e2e/run.mjs createshell   # one scenario, by name
node e2e/run.mjs pages harnesses
node e2e/chats.mjs             # the chat API and stream, no browser
node e2e/asks.mjs              # ask_user for each agent, no browser
```

`make e2e` is deliberately not part of `make check` and not part of CI: it wants
node, a Chromium and tmux, and it starts real agent hosts and terminal sessions,
which is exactly what it is there to test. The whole suite takes about twenty
minutes on four CPUs.

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
* **Ports 5000 to 5099.** Each server claims a port by listening on it first,
  starting at an offset taken from the run's process id, so two suites on one
  machine (another worktree's, say) do not walk the range in step. A run also
  tells its own server from someone else's: a health check counts only once the
  server it started has said it listens there, and a server that exits on
  "address already in use" is started again on another port. 5060 and 5061 are
  skipped: the Fetch standard calls them bad ports, and Node's fetch and the
  browser refuse to talk to them.

Pages are always waited for with `waitUntil: 'domcontentloaded'`. Never
`networkidle`: a chat stream and a terminal socket never idle, so `networkidle`
would hang until it timed out on every single navigation.

## The fake agents

A suite that needed three signed-in accounts would not be a suite, so the
scripted CLIs — `e2e/fakebin/fakeclaude`, `fakecodex` and `fakeopencode` — are
built once per run and put on `PATH` as `claude`, `codex` and `opencode`. Each
speaks its agent's own protocol as the chats drive it: `claude -p` stream-json
with the control protocol and the in-process MCP server that carries ask_user,
`codex app-server` JSON-RPC with its dynamic tool, and `opencode serve` over
authenticated HTTP and its event stream with the `socrates mcp-ask` server.
What a message makes them do is the script in each package's comment (and
docs/design/AGENT-HOST.md §5 and §8): `/stream`, `/slow`, `/tool`, `/edit`,
`/todo`, `/bg`, `/plan`, `/askuser`, `/whoami`, `/crash`, `/argv` and the rest;
anything else is answered "you said: …". They answer `--version`; fakeclaude's
models come from the catalogue, fakeopencode lists twenty-three with `opencode
models --verbose` (the `fake` provider's three, one of which calls no tools, and
twenty of a `router` provider, more than a menu holds), while fakecodex lists
none (`codex debug models` exits 2), so in the suite the sheet offers Codex with
an empty model field: an id typed in, or nothing for the CLI's own default, with
Admin and the sheet saying the listing failed (the program's own error behind
the "i"). The scenarios assert against what the fakes say back — the command line
they were started with, the account they found, the answer ask_user returned.

The chat scenarios that need a richer script (Markdown of every kind, a
workflow, a counter-question) use the scripted agent instead (`harness: "fake"`,
`SOCRATES_FAKE_AGENT=1`, AGENT-HOST.md §8), which the server registers when that
variable is set. Its "you said: …" echoes the person's text without Socrates'
note to the agent; `/note` tells the note the message before it came with.

The agents do not run in a terminal any more (REBUILD.md §7); the Shell needs no
fake at all: `/bin/sh` is the thing under test.

## Nothing reaches the machine's own agents

A fake run can neither read nor write the machine's real sign-ins, transcripts
or session databases, and never hands a child one of this machine's keys:

* `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `XDG_DATA_HOME` point under the
  run's data directory, and every chat's agent gets its account's folders there
  from the server on top of that;
* `ANTHROPIC_*`, `CLAUDE_CODE_*`, `OPENCODE_*`, `SOCRATES_S3_*`,
  `OPENAI_API_KEY`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME` and `XDG_STATE_HOME`
  are taken out of the environment the suite passes on, and
  `SOCRATES_S3_BUCKET` is set empty so that the environment cannot hand a server
  a bucket;
* `SOCRATES_WORKSPACE_ROOT` puts every working directory under the run's data
  directory.

The real `claude` binary reaches a real, paid model even with an empty
environment, so nothing in the suite runs it except the opt-in `livesession`.

## The bucket

Socrates runs without a bucket - the data directory is its only store, and S3
holds a backup of it - so a scenario starts with none. The `backup` and
`restore` scenarios want one: `e2e/fakebin/fakes3` - `internal/s3/s3fake`, the
in-memory store the backup's unit tests use, with real SigV4 verification - is
built and started on a loopback port the first time a scenario asks for it
(`backupForm(prefix)` in the harness), and each scenario backs up under a prefix
of its own (`e2e/<data directory name>/`). `/_fakes3/down?status=503` and
`/_fakes3/up` break the bucket on purpose (`fakeS3Switch`); nothing about the
fake is secret and it dies with the run. `SOCRATES_BACKUP_WARN_AFTER` shortens
the day without a backup after which the warning shows, for the `backup`
scenario.

## The chat API without a browser

`node e2e/chats.mjs` drives the chat routes and the chat stream against the real
binary, with real agent host processes running the scripted agent. It kills the
server with SIGKILL in the middle of a turn, starts it again and checks that the
stream continues exactly where it stopped, then covers duplicate sends and two
devices answering one question. `chat-restart` in `run.mjs` is the same restart
as the browser sees it.

`node e2e/asks.mjs` drives ask_user for each of the three agents through the
protocol fakes: the model's call, the answer through
`POST /api/chats/{id}/asks/{ask}/answer` back to the model by its own transport,
an answer after an interrupt delivered as a message, and a second device told
the question was answered.

## Reading the terminal

The Shell scenarios read the pane out of the DOM, so they first turn the WebGL
renderer off through the dashboard's own setting (`terminal.webgl: false`) —
this machine's headless Chromium has a software WebGL context, so the shipped
default would paint into a canvas nothing can read back. `webglrenders` keeps
the default and asserts that it really does paint, so the shipped path is not
the untested one.

## Every scenario leaves nothing behind

Killing the server is *not* enough: a Socrates restart deliberately leaves the
chats' agent hosts and the Shell's tmux sessions running — that is the
durability the product exists for — so a scenario that only killed the server
would leak them.

`stop()` therefore deletes every session the scenario made, *then* takes the
server down, and then **asserts** that no session row, no tmux session or
tmux server on `<data>/tmux.sock`, and no agent host of `<data>` (nor a CLI
under one) survives. All three are ordinary assertions and appear in every
scenario's count, so a scenario that starts leaking fails rather than quietly
littering. A `tmux kill-server` and a kill of the run's hosts are the backstop
afterwards regardless, and a run that ends or is interrupted kills any server it
started, and the hosts and tmux servers of its data directories, that are still
there.

The suite still has the machinery for a quarantined scenario, and nothing uses
it. `scenario(name, title, fn, { quarantine: 'why' })` lets a scenario print
every verdict without failing the run, for a defect that is understood and
reproduced but lives in a file the work package does not own.
`SOCRATES_E2E_STRICT=1` takes that exemption away.

## The live scenario

`livesession` is the one scenario that talks to a real CLI: a plain build with
no fakes on `PATH` and the machine's own `HOME`, and a Claude Code chat whose
agent comes up from the real `claude -p`. It sends no message, so it spends no
tokens: it asserts the CLI's answer to the initialize handshake (its version and
its commands), a composer to type in, and that no turn ran. It is gated —

```
SOCRATES_LIVE_AGENTS=1 node e2e/run.mjs livesession
```

— and skipped, loudly, otherwise.

## Artefacts

Screenshots and anything else the run writes go to `e2e/out/`, which is
gitignored. Deleting `e2e/out/` is always safe.

## The scenarios

The agents' scenarios assert against the chat view and the protocol fakes; the
Shell's against the pane. A scenario with a phone part runs it at 390×844 in
touch mode, except `options`, which measures the narrowest phone in use,
360×740, as its viewport and nothing more.

| name | what it proves |
| --- | --- |
| `createshell` | the sheet makes a Shell session, the shell answers, and the page and the pane are the same white — with the dimmest ANSI colour still legible on it |
| `typeandsee` | keystrokes reach the pane, output comes back in order, and the journal on disk holds the same bytes |
| `reloadkeepsscreen` | type, reload: Home, as every start is, and the session opened again is the same viewer with the same screen, and typing still works |
| `pages` | `/`, `/admin`, `/login`, `/setup` and a chat are clean at 390×844 and 1280×720, and the sheet is a full-width bottom sheet on one and a side sheet docked to the right edge on the other |
| `harnesses` | a Shell and a chat with each agent start from the sheet and are seen in the browser: the Shell runs a command, each agent answers its first message over its own protocol, every row wears its mark, and the facts are behind **Info** |
| `browserlayout` | row menus flip upwards near the bottom, and in a viewport too short for the whole menu its items are the action sheet; delayed local fonts give DOM and WebGL equal-width cells and matching initial pane sizes |
| `sessionlist` | rename, file, pick up again and delete, for a Shell and a chat — and the working directory survives the delete |
| `folders` | the list read by project: one folder per working directory, named after it with the whole path on hover, ordered by the project worked in most recently, `More Chats` last; a folder shuts, says how many are behind it, is found shut after a reload and opens again for a chat inside it; a row keeps its element across a re-render |
| `exitoverlay` | a Shell's `exit 7` raises the overlay with its status in a plain line under it, and **Restart** brings the session back |
| `webglrenders` | the shipped renderer paints the terminal |
| `touchscroll` | a finger dragged down the pane reaches what scrolled off it, a drag back up returns to the live bottom, a tap is still the tap that puts the keyboard on the pane, and with tmux's mouse off the drag types nothing |
| `keybar` | a touch screen gets the key bar before anything asks for it and a desk does not, the ⋮ menu hides and shows it and is remembered per device; it sends the right bytes, a sticky `Ctrl` turns the next letter into a control code, and a physical keystroke does not take it away again |
| `keyboard-pan` | an Android keyboard over the terminal (an Android user agent, a 24px bottom inset the keyboard takes away): the key bar stands that inset higher, clear of the keyboard, and a visual viewport panned down past the frame is panned back, unless the page is zoomed |
| `paused-deadline` | a page held still past a deadline: one on a running page expires on time, a late one waits a grace, and a reply that came just after the pause is read with no "Connection lost" |
| `offlineonce` | a whole command typed with the network off arrives **exactly once** when it comes back, the lost connection is visible while it is gone, and the app shell still opens offline, on Home, without claiming there are no sessions |
| `sigtermreattach` | the server is killed mid-session and restarted; the pane still holds what was typed and the session is running |
| `takeover` | a second tab with the same viewer id closes the first socket with 1012 and drives the session |
| `offlinerestart` | the server is restarted inside an outage and the phone wakes with `online`, `focus` and `visibilitychange` at once: one handshake, and nothing typed is lost in silence |
| `latehello` | what is typed before a slow `hello` arrives is delivered, exactly once, and leaves nothing behind |
| `mcpcard` | Admin's MCP card: the server built into Socrates (BrowserStack) off and without an account, its sheet; a server from the gallery prefilled; Test connection in the editor and a row's Test against `e2e/fakemcp.mjs` (a tiny MCP server on stdio), its tools as switches saved with the page, a failed test with what the server printed; Connect asking to save first and the way back from the sign-in (the start stubbed); a chat's MCP dialog naming the built-in server by its title and testing a server; the Tests page's servers for test cells |
| `adminoptions` | every harness option round-trips, the models and efforts set as defaults are what a chat starts on, and Claude Code's command line carries the fixed policy (HARNESS-POLICY.md) and never a flag it forbids |
| `tmuxcard` | the engine card on a machine whose tmux is too old |
| `twoviewers` | two devices on one Shell: both see the same pane, and the size notice is shown once |
| `backpressure` | two hundred lines printed as fast as they can be arrive whole, on screen and in the journal |
| `deletekeepsdir` | delete ends a Shell's tmux session and a chat's agent host, removes the rows, and leaves the work on disk |
| `recoveredsession` | a `soc_*` tmux session with no row is taken in as "Recovered session", never killed |
| `createclaude` | the sheet's model list is the catalogue's, the model picked reaches `claude -p`, the conversation fixed at creation is the chat's and its transcript is the chat's folder's, and a CLI that crashed mid-turn is started again on that conversation by the next message — one message, one relaunch |
| `createcodex` | one `codex app-server` per chat, its first thread the chat's conversation kept as one rollout, put to rest when the chat is filed as done and resumed on the same thread by the next message |
| `own-codex` | what Codex does by itself: a goal the model sets is a line, the turn Codex then starts by itself opens with the goal it goes on toward and its end says what it used; an image it makes is drawn from its file with its prompt; a subagent whose turn failed ends failed, with why |
| `accounts-agents` | an account reaches its agent's CLI: a second Claude Code account (with Ultracode) runs with its own `CLAUDE_CONFIG_DIR` and key, a Codex key is signed in by codex into the account's `CODEX_HOME`, an OpenCode key reaches its server; Info names the account, and an account is not deleted while a chat runs on it |
| `createopencode` | one `opencode serve` per chat on a port and with a password of its own, spoken to over its authenticated API, its session kept and gone on with after the chat is filed as done; what a shell command of a step changed shown as Changes beside the edit diffs, a warning toast said once, a subagent's retry and error inside its task |
| `opencode-models` | every model `opencode models --verbose` lists is in the sheet by its name, with its provider, context and price, and none that calls no tools; a search by provider and words finds one; a model's variants are its efforts; in a chat the model menu holds the few that matter and "All N models…" opens the search; the model, variant and agent chosen - a project's own primary agent included - are what the prompt reaches OpenCode with |
| `rebootresume` | the server, every agent host and tmux gone at once mid-turn: the turn is closed as interrupted, the chat says once that it was resumed, the next message goes to Claude Code started again on its conversation, and the Shell is a working shell again when opened |
| `lighttheme` | the white pane, and all sixteen ANSI colours a Shell prints drawn at 4.5:1 or better |
| `activity-claude`, `activity-codex`, `activity-opencode` | each agent's chat working (a turning ring), finishing while nobody is looking, and the name that goes bold |
| `tasks-claude`, `tasks-codex` | a turn that ends with a background shell (Claude Code) or a background terminal (Codex) still running: an hourglass and Background tasks while it runs, bold once it is over |
| `activity-waiting` | a chat's question: a still amber ring, and no timeout |
| `unread` | bold when nobody saw it, gone when the chat is opened or sent a message |
| `notify` | a chat that stops working while nobody is looking: one chime and one notification named after it, two switches in the options menu that each turn one of them off and are remembered on this device, and nothing fired for a replay or for a chat that has only just been started |
| `session-title` | a chat names itself from its first message, once, while its name pulses; the arrow beside the name names it again; a naming that fails says so; a first message still being written in a chat's big field asks for no name |
| `status-speak` | **Status** wears a turning ring while it asks, the answer about the chat lands on screen as words, nothing is read by itself, and Read aloud sends the same words to `/api/voice/speak` once — intercepted in the browser, so no scenario ever calls Google |
| `status-ticker` | the phases of a status — reading the chat, asking the model, speaking, the answer — arrive in order in one line |
| `dictate` | the microphone's sheet with its two endings: a discarded recording that transcribes nothing, and a kept one whose words wait in the chat's composer for the person's Send |
| `no-overlap` | speech-to-text and text-to-speech are never open at once: **Status** while the microphone runs says nothing out loud, the same press with it closed does, and a recording that starts silences what is playing |
| `typeafteroutage` | a cut socket, a locked phone, and a pane that still takes keystrokes |
| `typekeepsfocus` | a dialog, the ⋮ menu and two sessions: the keys still land in the pane |
| `keyboard` | on a touch screen only the top bar's button raises the on-screen keyboard for the pane, and a second press puts it away; a chat's composer and every field the page focuses by itself are muted with `inputmode="none"` **and** `readonly` until a finger taps them, and that tap types at once; a chat's big first field is the same, a finger that scrolls from it leaves it muted and a long press leaves it writable (for the system's Paste); the rename dialog, asked for by a tap, opens on its field with the keyboard - from the action sheet too, whose Rename runs inside the tap - and one opened by no tap opens muted, its first tap raising the keyboard; no copy of the button lies over a dialog, and a device with a mouse has no button and nothing muted |
| `options` | the ⋮ is this device's options (the key bar, touch mode, sound, notifications), each kept per device; a chat's account picker shows how full each limit window is, when it renews — on the clock — and, a tap away, exactly when |
| `design` | white surfaces, a mark wherever an agent is named (rows, Home's chat cards, the accounts), technical strings only behind an "i", and an animation that does not restart when a row re-renders |
| `clipboard` | Ctrl-V pastes the clipboard instead of sending 0x16, bracketed when the program asked for it; a Shift-drag selects and Ctrl-C copies it without interrupting; with nothing selected Ctrl-C is still 0x03; the key bar's **Paste** sends the same text; a plain drag selects in the browser and copies on release; a plain click is still reported to the program; a double click copies the word; a middle click pastes the last selection exactly once; an OSC 52 printed through tmux sets the clipboard |
| `touchcopy` | a finger held on the pane selects the word under it and lifting it copies, with a **Copied** toast and no click sent; held then moved it grows the selection; a short tap is still a tap |
| `transfer` | the **Upload** button writes a file into the Shell's working directory under `.socrates/uploads/`, with a `.gitignore`, and puts its path on the prompt; a pasted image does the same; **Paste** and **Copy** / **Copy all**; on a phone the three float over the pane and the top bar still fits |
| `chat-fresh` | a chat nobody has written to yet is one big field (`#chat[data-fresh]`): what is written is the chat's draft on the server, byte for byte, kept across a reload, found by another device, and what that device writes comes into the field; a start time picked under it is saved to its minute, said, and worn by the row, and **Clear** takes it away; a time in the past is refused; two wheel steps in a row (a slow first answer) save and say the last time picked; the minute iOS fills in as its picker opens is no error, and closing on it empties the field; on a phone the field fills the screen and nothing scrolls sideways |
| `chat-stream` | a chat: streaming, Markdown and its thinking (folded), a reload that replays the journal, a message queued during a turn, Stop, the draft, the palette, the model, the lines between messages |
| `chat-restart` | the server restarted mid-turn under a desk and a phone on the chat: neither reloads, the stream replays the rest, the turn completes, and each shows one message and one answer with every word once and in order |
| `chat-tools` | an answer's calls folded in one line that says a call failed; opened, tool rows that open to their input and output, a failed one, the diff viewer, a subagent's group, a workflow, and the task list pinned while it runs |
| `chat-fold` | an answer's work (Claude Code's `/readme`) is one shut line: while it works "Working: …", turning; then how long it worked, what it did, its steps and tokens; under it stands the final answer and nothing of the work; opened, every part, and it stays open as the next answer is drawn; a question asked after work (`/toolask`) stands under the shut line |
| `chat-watch` | nothing in a chat hangs unseen (AGENT-HOST.md §6, on a short clock: `SOCRATES_CHAT_WATCH_MS`): the work after an answered question (`/asktool`) goes on under its card in a fold of its own, which says by the clock when it was done; a background job (`/bg 120`) says since when it runs and Stop this job stops it; a turn that goes quiet (`/hang`) is said in the dock with when the agent was last heard from, and a Stop it ignores ends the agent, which takes the next message on the same conversation |
| `chat-sub` | the line under a chat's bar: its account, "No background jobs", then a background task counted (`/bg 30`) with a dialog that lists it, and after `/limit` the 5-hour window full with when it renews |
| `chat-wakeups` | what the agent starts by itself: a wakeup it scheduled (`/wakeup 6`) is a folded line at the foot of the chat - when, and why - whose row says the prompt and in how long, and the summary counts it with `wakes_at`; the turn it starts opens with "Woke up as scheduled: <prompt>" and the line at the foot goes; a Monitor's event (`/monitor 1`) opens its turn with the event |
| `chat-agents` | a subagent (`/subagent bg`) or a workflow (`/workflow`) at work beside the chat is a card of its own at the foot, not one more background job: what it was sent to do, its kind and type, a watch that goes on with no turn running, a workflow's agents done on a bar with the phase it is in; the card opens the dialog on its job, where the agents stand as cards with Stop over the other jobs; on a phone it fits and stays low, and two at once stand side by side |
| `chat-send-now` | a message written during a turn waits with **Send now**; Send now interrupts the turn and the message is answered next, once |
| `chat-auto-continue` | a limit's card says the chat goes on by itself once it renews, and the row's ⋮ **Go on when a limit renews** switches that off and on (the timing itself, in minutes, is the Go tests') |
| `chat-offline` | what a chat is told offline — from two tabs, a model switch, an answer — or whose answer was lost, or left across a reload, is delivered once |
| `chat-ask` | questions: the card and the dialog, every kind, a plan, a counter-question, Home, two devices |
| `chat-attach` | files from the button, dropped and pasted, at the paths the server chose |
| `chat-phone` | a chat on a phone: 48 px targets, no sideways scroll, the keyboard, the dialog as the whole screen |
| `chat-home` | Home reads a chat from its journal: what it does, Running and Finished, its question on the card, at a desk and on a phone |
| `chat-push` | a chat's notification and badge: the question's words, how a turn ended, nothing for a Stop, a limit once |
| `chat-title` | a chat names itself from its first message |
| `chat-status` | Status says a chat from its journal, in its modal and out loud on request |
| `chat-status-eta` | While a chat works, Status says when all of it will be done (clock time, range, share done), what runs, the steps left and why; on a phone too |
| `chat-outcome` | a chat's turn judged from its journal: the hourglass, the verdict, the notification |
| `chat-schedule` | a chat set to start a few seconds ahead (through the API: the field is to the minute) does not start early, then sends its draft as the first message, once, with the notice that the clock sent it, and its draft and start time are gone; a chat with a message takes no start time (409) |
| `menufit` | a session's ⋮ is a menu where it is seen whole and the action sheet where it is not: a chat's menu at a desk 900 px tall, and its sheet on a car's 585 px screen |
| `chat-ultracode` | Ultracode for Claude Code alone: in the sheet, and in a Claude chat's menu |
| `accounts` | the Accounts card: an API key, a Codex device code, OpenCode signing in in a Shell, rename, order, a delete refused then taken, a phone |
| `chat-account` | a chat's account: from the sheet, switched idle and after a turn, a limit's Continue with, the automatic switch, a phone |
| `backup` | the Backup card: a bucket configured from it (Test connection, the secrets never shown again), **Back up now** ending in "Backed up" with its snapshot listed as this machine's; with the bucket down, a run that says it failed with the bucket's words behind the "i"; the day without a backup warned about in `/api/state`, on the card and on the session page; the next good run takes the warning away |
| `restore` | server A is backed up and then lost; server B, a new data directory, offers **Restore from a backup** on its setup page, refuses a wrong passphrase, lists the snapshot, restores it and goes to the sign-in, where A's password works; the sessions are there under their ids and names, a chat not started yet with its first message and its start time, and B backs up into the same bucket under a machine id of its own |
| `home-landing` | every start of the page is Home, a reload of a session's address too; the Home button (and the phone's drawer) brings it back and says so with `aria-pressed`; Back and Forward move between `#home` and `#<id>` |
| `home-live` | chats made on another device are counted, and one that starts working, one that asks, a Shell that ends and a chat that finishes unseen move Home's numbers within seconds — with no reload and no socket but the event stream; a card filters the list, a second tap shows everything |
| `outcome` | with the outcomes on, a Claude Code chat's turn is judged from its journal (the instruction, the answer, its task list): the hourglass while it is read, the chime and the notification waiting for it, the verdict on the row, on Home, in the notification and the header's tag, across a reload, and gone with the next turn or the switch |
| `home-question` | the Questions card opens the dialog at a chat's question: its context, the question and its options as 64 px buttons in 20 px type; a tap and Send reach the agent (Claude Code's ask_user returns the choice), and the dialog says so and closes |
| `home-own-answer` | an answer of one's own takes the place of the chosen option, and the agent gets the words and no option |
| `home-queue` | two chats' questions walked oldest first ("1 of 2"), named as toggles and buttons, the next taking the dialog after an answer |
| `home-elsewhere` | a question answered on another device while the dialog shows it ("Answered on another device", nothing sent); an answer to a question already answered, or to none, refused; an answer on its way overtaken by another device's; a chat whose agent died under its question still asks it |
| `home-deeplink` | `#q=<id>` opens a chat's question on boot and `#open=<id>` the session; a chat that asks nothing, a Shell, a chat that is gone each say so; a tab offline since before the question waits for the connection and opens the question it was asked for; signed out, `#q=` goes through the sign-in and opens the question after it |
| `home-reconnect` | the server restarted under an open Home: "Reconnecting…", then "Live" and the right numbers on the same page; a page hidden for 3 s keeps its stream; signing out or a new password closes that login's other tabs with 4401 while another login is untouched |
| `home-touch` | a Tesla's user agent at 1280×800 and a phone at 390×844: touch mode, a dialog that is the whole screen, options at least 64 px high, no copy of the keyboard button over the dialog, nothing wider than the phone, and on a phone on its side the first option whole on screen; a double tap on Send sends once, and a double tap on the Questions card chooses nothing while a deliberate tap a moment later chooses |
| `home-focus` | the field of one's own answer keeps the keyboard and its words while another chat asks, the chat is renamed and the dialog is drawn again; Send sends the words (the field is several lines, so Enter is a new line); with no Text-to-Speech key Read aloud says where one goes and sends nothing; a Read aloud or dictation that fails is said in the dialog and stays said |
| `home-retry` | an answer refused for good says why and gives the questions back to send again; one the server could not take just now (503) is tried again by itself and goes in once; one to a chat deleted on its way says it was not taken, never "another device" |
| `home-runlost` | the dialog opened again while its answer is on its way shows it going, with no Send; an answer sent across a server restart goes in once and ends on "Answered"; one whose reply was lost on the way back is followed to "Answered", never "another device" |
| `home-opencode` | an OpenCode chat's ask_user call (through its MCP server) answered from the dialog with an option and with words of one's own, its own question tool answered with its labels, and a permission it asks for allowed by the chat itself, with the adapter's notice and nothing for anyone to answer |
| `home-stream` | a list that lands late does not bring back a deleted chat or a finished turn; through a proxy, a network that goes away under a live stream is "Reconnecting…" then "Offline", never "Updated every 5 s", and Live again after; a handshake that hangs is retried, a refused upgrade leaves Home polled ("Updated every 5 s", not dimmed) and current, and offline says its age in seconds |
| `home-quiet` | a notification tapped on a stream gone half-open waits for the server and shows the question, never "nothing is waiting"; a page on that stream says "Checking…" 2 s after its unanswered ping and is Live with the question within 25 s; a page that can only poll keeps two chats' questions and a Finished turn counted across a server restart and answers one; a page shown again after 33 s hidden says Reconnecting…, never Offline; a chat being named when the server restarted stops pulsing; a page started from the worker's cache with the server gone says Offline, and is Live once it is back |
| `home-kinds` | a multi-select whose taps toggle; two questions walked with Next and Back, each keeping its choice; a plan, which the dialog leaves to its card: Home counts it, its Answer opens the chat at the card, Keep planning with a note sends it back and Approve approves the next |
| `home-honest` | on a device whose clock is ten minutes fast a question asked a moment ago says "just now"; the dialog says nothing about its connection while live, and offline says so, dims the question and says it may already have been answered; an answer sent offline is saved, said to be, and goes in once the connection is back; `/api/home` and `/api/sessions` come gzipped |
| `home-restart` | a server restart under an open dialog on three chats' questions: Home counts all three throughout, the walk stays "of 3" and never says they went, each keeps the time it started waiting, and a choice made before the restart is still chosen and goes in |
| `home-devices` | two devices on one chat's question: the second follows the first's answer ("Answered on another device"); a Send from a device whose stream had not passed the answer on, and one on a slow uplink that the other's answer overtook, are told the same and add nothing; a second tab of the same browser says "Answered", never another device |
| `home-followup` | the agent asks its next question right after the dialog's answer: the open dialog takes the follow-up up rather than closing under it, and the follow-up gets only the answer given to it |
| `livesession` | one real chat against the real Claude Code CLI, with no model turn (gated) |

`lighttheme` measures the colours the renderer actually **drew** rather than the
palette they came from: eleven of `LIGHT_THEME`'s eighteen values are
deliberately not 4.5:1 against white, and what makes them legible is
`minimumContrastRatio: 4.5`, which re-derives them at draw time.

### What did not come over to the chats

The scenarios written for the agents' terminals were moved onto the chats with
the same intent (WP11). What a chat has no counterpart for went:

* `tasks-opencode`: OpenCode's subagents run inside the turn that started them,
  so a turn of OpenCode never ends with work still running (its adapter reports
  no background tasks).
* `home-unread` and `home-language`: both were about a question read off an
  agent's screen in prose, by the model, in the person's language. A chat's
  question is its agent's own ask_user call, in the agent's words, and nothing
  reads a screen.
* Inside ported scenarios: OpenCode's permission card (a chat allows every
  permission and says so), the old dialog's "Another device is answering" step
  (a chat's answer is taken by the server at once) and the questions asked in
  prose in `home-quiet`, `home-kinds` and `home-restart`.

## Dictation needs a microphone and a gateway

`dictate` and `keyboard` ask Chromium for its own fake microphone
(`--use-fake-device-for-media-stream`) and point `openrouter.base_url` at a
small HTTP stub the scenario starts. It has to be a real server rather than a
Playwright route: the browser posts the recording to `/api/voice/transcribe`
and the **server** is what calls the gateway, which a route in the page cannot
see.

## Home and the gateway stub

Every start of the page lands on Home (HOME.md §2.3), a reload of a session's
address included, so a scenario that reloads a session goes back to it through
the address once the list has its row, and a new tab is opened at
`#open=<id>`.

`openRouterStub` is the gateway for everything the server asks a model: a
chat's name, the verdict on a turn, a transcription. `replies` queues answers,
`delay` holds them back, `refuse` refuses them, and `answer` reads each request
and answers it — for a scenario whose calls arrive in an order no queue can
predict, or that holds one back while it looks.

## Screenshots for the README

`node e2e/shots.mjs` regenerates `docs/screenshot-session.png` (a chat at a
desk), `docs/screenshot-home.png`, `docs/screenshot-phone.png` (a question on a
phone), `docs/screenshot-admin.png` (Admin's accounts) and
`docs/screenshot-backup.png` (Admin's backup) — the real server and page, with
the protocol fakes standing in for the three agents; the messages in the
pictures are words, which `claude.scripts.json` beside the fake maps onto its
scripts.
`SOCRATES_LIVE_AGENTS=1` takes the Admin pictures against the real CLIs, and
nothing else does. `docs/screenshot-tunnel.png` is hand-made and is not
regenerated. It is not part of `make e2e`: the pictures are committed, so this
is only run when they need redoing.

### Browser IDE

The optional `vscode-check.mjs` smoke test uses a real code-server installation.
It checks loading the embedded workbench, editing and saving a project file,
switching between IDE and shell, surviving a Socrates restart, and resuming
following loss of the tmux server. It creates and removes its own sessions.

```bash
CODE_SERVER_BINARY=/absolute/path/to/code-server node e2e/vscode-check.mjs
# Set SOCRATES_E2E_CHROME if Chromium is installed outside the harness default.
```
