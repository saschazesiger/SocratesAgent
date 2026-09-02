# The end-to-end suite

Twenty-one scenarios that drive the real `socrates` binary through a real browser.
Each one boots a server on its own port with its own temporary data directory,
completes setup the way a person would, and then asserts against what the page
actually shows — printing the value it measured beside every verdict.

```
make e2e                     # the whole suite
node e2e/run.mjs             # the same thing
node e2e/run.mjs streaming   # one scenario, by name
node e2e/run.mjs offline blankchat
```

`make e2e` is deliberately not part of `make check` and not part of CI: it wants
node and a Chromium, and it starts detached host processes, which is exactly
what it is there to test.

## What it needs

* **Go and node.** Nothing is installed from npm; Playwright is used straight
  from the machine's own copy at
  `/opt/browser-testing/node_modules/playwright-core/index.mjs`.
* **A Chromium.** The default is this machine's headless shell at
  `/root/.cache/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell`,
  launched with `--no-sandbox` because the suite runs as root in a container.
  Point `SOCRATES_E2E_CHROME` at another binary to use one.
* **Ports 5000 and up**, one per scenario (a couple use two).
* **`pgrep` and `pkill`**, which is how the leak check and its backstop work.

Pages are always waited for with `waitUntil: 'domcontentloaded'`. Never
`networkidle`: the SSE stream never idles, so `networkidle` would hang until it
timed out on every single navigation.

## How the turns are produced

The three protocol adapters talk to real CLIs, and a suite that needed three
logged-in accounts would not be a suite. So `harness.mjs` builds the binary with
`go build -overlay`, mapping in one Go file that points the `claude`, `codex`
and `opencode` ids at `internal/agenthost/hosttest` — the scripted, in-process
adapter. The overlay file lives in a temporary directory and never enters the
worktree, so a hard kill of the run cannot leave a generated source file behind.

The turn each scenario gets is the `SCRIPT` constant in `harness.mjs`, replayed
identically on every turn: a sentence, a reasoning step, a `Bash` tool card, a
subagent, a notice, a closing question and the usage numbers. Scenarios that
need something else pass their own `script`.

The three fake CLIs from `internal/harness/fakes` go on `PATH` under their real
names, because `/api/agents` reports an agent as installed only when its binary
answers `--version` — and the sheet and the dashboard's Agents card are drawn
from that.

## The live scenario

`liveclaude` is the one scenario that talks to a real CLI: a plain build with no
overlay and no fakes on `PATH`, a `claude`/`haiku` chat, and one tiny tool-using
turn in the browser. It is gated exactly like the Go live tests in §10.2 —

```
SOCRATES_LIVE_AGENTS=1 node e2e/run.mjs liveclaude
```

— and skipped, loudly, otherwise. It costs money and needs a logged-in account,
so it never runs in CI.

**What it caught, and why it is worth its cost.** Every chat's working directory
is `engine.Workspace()` = `<workspace_root>/<chat id>`, and nothing used to
create it. The adapter started the CLI with a `cmd.Dir` that did not exist, and
Go reports that as

```
claude: starting claude: fork/exec /root/.local/bin/claude: no such file or directory
```

— which reads as a missing binary and is not one. Every real chat died at its
first message. Nothing else could have seen it: the hermetic Go tests set
`spec.Cwd` to a `t.TempDir()` themselves, and the scripted adapter the rest of
this suite uses never execs anything. `engine.specFor` now creates the directory
and the host refuses a spec whose `Cwd` is missing, so a regression surfaces as
that sentence rather than as a phantom missing binary — and the scenario still
names the cause in its own output if it ever sees that error text again.

## Every scenario leaves nothing behind

§10.3's rule: killing the server is *not* enough. `SIGTERM` deliberately leaves
the agent hosts running — that is the feature `sigterm` exists to test — so a
scenario that only kills `socrates serve` leaks one detached `agent-host`
process per chat it created, and a full run would leave a pile of them on the
machine.

`stop()` therefore deletes every chat the scenario made (which is what calls
`engine.CloseChat` and ends the host), *then* takes the server down, and then
**asserts** that no chat and no `agent-host --dir <its data dir>` process
survives. Both are ordinary assertions and appear in every scenario's count, so
a scenario that starts leaking fails rather than quietly littering. A `pkill`
backstop runs afterwards regardless, and `finish()` sweeps once more at the end
of the run.

`sigterm` is the named exception to closing early: it needs its host to outlive
the server while it measures. It closes its chats at the very end, through the
same `stop()`.

## The one tolerated console error

Every scenario asserts that the page logged no console errors. The single
exception is a `503` from `POST /api/voice/speak`, which is what a machine with
no Piper answers when the page prefetches the spoken offline notice. It is
tolerated by **where it came from**, not by its wording:

```js
/503 \(Service Unavailable\) @ .*\/api\/voice\/speak$/
```

so a `503` from `/messages` during a shutdown drain can never hide behind it.
The `streaming` scenario additionally asserts that every console error it saw
really did come from `/api/voice/speak`, so the filter cannot quietly widen.

Scenarios that switch the browser offline also tolerate the browser's own
reports of the requests that then failed (`ERR_INTERNET_DISCONNECTED` and
friends) — those are the point of those scenarios, not a defect in the page.

## Picking a model with the mouse

`modelpick` is an ordinary scenario now, and it is here because tapping a model
in the new-chat sheet used to throw the chat away. The mechanism was exact, and
each half was reasonable on its own:

* `combobox.js` chooses an option on **mousedown** ("the input must not lose
  focus first") and hides the list in the same handler;
* `agents.js` treated a click whose `event.target` is the `<dialog>` itself as a
  tap on the backdrop, and answered it with `finish(null)`.

By the time the browser dispatched the `click` that followed that mousedown, the
option it started on had been hidden, so the event retargeted to the nearest
element still under the pointer — the dialog — and the sheet cancelled itself.
Keyboard selection never produced the retarget, which is why nothing caught it
until a scenario tapped a model with a real pointer.

`agents.js` now measures such a click against the sheet's own rectangle and
treats it as the backdrop only when the pointer really was outside. `modelpick`
holds both halves of that: a tapped model keeps the sheet and reaches the chat,
and a click genuinely beside the sheet still closes it — a rule that kept the
sheet open by never closing it at all would be worse than the bug.

The suite still has the machinery for a quarantined scenario, and nothing uses
it. `scenario(name, title, fn, { quarantine: 'why' })` lets a scenario print
every verdict without failing the run, for a defect that is understood and
reproduced but lives in a file the work package does not own.
`SOCRATES_E2E_STRICT=1` takes that exemption away.

## Artefacts

Screenshots and anything else the run writes go to `e2e/out/`, which is
gitignored. `e2e/out/voice-cache/` is a Piper install shared by every server the
suite starts, so a fresh data directory does not mean a 25 MB download per
scenario. Deleting `e2e/out/` is always safe.

## The scenarios

| name | what it proves |
| --- | --- |
| `newchat` | the sheet binds a chat to an agent, a model and an effort; the header badge and the title share 390 px |
| `streaming` | a draft that grows and is removed, a tool card with its output, one assistant message, and the shape of the tolerated console error |
| `twoturns` | a question, an answer, and a composer that comes back between turns |
| `audioturns` | two turns in the Audio view: one spoken answer each, nothing intermediate ever spoken |
| `modelchange` | the model moves between turns and is refused with a 409 during one |
| `errorstep` | a turn that dies says so, in the transcript and in the working row |
| `stoptool` | Stop while a tool card is still open closes it, in the DOM and on the server |
| `dropconn` | the connection drops mid-stream: nothing duplicated, DOM steps equal server steps |
| `sigterm` | the server dies mid-turn and comes back on the same port; the host outlived it |
| `retry503` | a message answered 503 once is retried and delivered exactly once |
| `offline` | the connection bar, a queued message delivered once, and a whole chat started with no network |
| `blankchat` | a chat that does not exist yet survives a reload and stays the chat you are looking at |
| `queuedchat` | a chat started offline survives a reload and is created once, with its binding |
| `queuedchatbeside` | the same, beside a chat that already exists — and nothing leaks into it |
| `refused422` | a queued message whose agent was switched off underneath it fails once and never retries |
| `sheetphone` | the sheet at 390x500 with the keyboard up, combobox and focus trap included |
| `admin` | the Agents card, refresh, save and the diagnostics rows |
| `pages` | every page is clean, at a phone and at a desk |
| `modelpick` | a model tapped with the mouse keeps the sheet and reaches the chat; a tap beside it still closes it |
| `design` | every surface is white, every agent is its own mark, and the build and path are behind an "i" that a hover, a tap and Escape all drive |
| `liveclaude` | one real turn against the real Claude Code CLI (gated) |

## Screenshots for the README

`node e2e/shots.mjs` regenerates `docs/screenshot-chat.png`,
`docs/screenshot-auto.png` and `docs/screenshot-admin.png` from the real running
app, using the same harness. It is not part of `make e2e`.
