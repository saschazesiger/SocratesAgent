# Activity, Status, Agent and Audio mode — binding specification

Extends `docs/design/DESIGN.md` §A/§B/§D/§E/§F; nothing here contradicts `DECISIONS.md`. Where this file and DESIGN.md differ, this file wins for the four features below. Everything marked **[V]** was verified on tmux 3.6 on this machine while writing this.

(a) per-session busy/idle in the sidebar with an unread mark; (b) a **Status** button that has a model describe the screen and Piper read it out; (c) an **Agent** button running a server-side operator loop that types into the pane; (d) an **Audio mode** for a phone in a car.

---

# A. The activity detector

## A.0 States, ownership, persistence

```go
// internal/termux/activity.go
type State string
const (
    StateUnknown State = "unknown" // no usable evidence yet
    StateBusy    State = "busy"    // the harness is working
    StateIdle    State = "idle"    // waiting for a new instruction
    StateWaiting State = "waiting" // needs the user: permission, question, menu
)
type Activity struct {
    State  State  `json:"state"`
    Unread bool   `json:"unread"`
    Since  int64  `json:"since"`          // unix ms of the last committed change
    Source string `json:"source"`         // "exact"|"screen"|"quiet"|"pane" — hover-only detail
    Note   string `json:"note,omitempty"` // e.g. "permission prompt" — hover-only
}
```

`State` lives **only in memory** on the `Manager`, like `sizeOwner`: it is re-derived from a live pane every second, so a persisted copy could only ever be stale. No store migration.

`Unread` **is** persisted — also without a migration, in the existing kv: key `activity.unread`, value `{"<sessionID>": <unixMilli>}`, written through `store.SetJSON` on every change (rare: one per finished turn) and read back in `StartActivity`, dropping ids that no longer exist.

## A.1 The tick

Own goroutine and ticker, started from `Manager.Start` after `Adopt`, stopped with the manager. `ActivityInterval = 1 * time.Second`. One tmux command per tick for **all** sessions — a second `list-panes -a`, kept apart from the lifecycle poll (§A.9) so neither can break the other's parsing:

```
tmux -S sock list-panes -a -F '#{session_name}|#{pane_dead}|#{pane_pid}|#{window_activity}|#{pane_current_command}|#{pane_title}'
```

parsed with `strings.SplitN(line, "|", 6)`. Every field before the title is `|`-free by construction; **the title is last because Codex's approval title contains a pipe** (`[ . ] Action Required | SocratesAgent`) and `SplitN` hands it back whole. **[V]** all six formats populate on 3.6. `#{pane_activity}` does **not** exist; `#{window_activity}` does, is the epoch second of the pane's last output, and is exact per session because a Socrates session is one window with one pane **[V]**.

Everything else a layer needs is free (a small `os.ReadFile`, a goroutine already running) or is only paid when the layer above is unavailable (`capture-pane`, at most every 2 s per session). A hundred sessions cost one fork per second.

## A.2 Layers, per harness

`L1` exact, `L2` output quiescence (harness-independent, always available), `L3` screen scrape. Confidence: `exact` 3, `screen` 2, `quiet` 1.

| Harness | L1 (exact) | L3 (screen, `capture-pane -p -J -S -40`) |
|---|---|---|
| **Shell** | idle ⇔ `#{pane_current_command}` == the launched shell's basename **and the shell has no child process** (`/proc/<pane_pid>/task/*/children` all empty; `ps -eo pid=,ppid=` elsewhere); anything else → busy. Never `waiting`. **[V]** (`bash` at the prompt, `sleep` while `sleep 4` runs). <!-- rev: fable --> The children check is not optional: `bash script.sh`, `sh -c …`, a `#!/bin/bash` script or a nested interactive shell all report `bash` as the foreground command and would otherwise read as idle for their whole run. A nested idle shell reads as busy under this rule; that is the cheaper mistake. | none needed; L2 only |
| **Claude** | `<home>/.claude/sessions/<pid>.json` → `.status` ∈ `idle`\|`busy`\|`waiting`, `.waitingFor` → `Note` | busy `esc to interrupt`; waiting `Do you want to proceed\?`; idle: neither, and a line `^\s*❯\s*$` or `⏵⏵ ` present |
| **Codex** | `#{pane_title}`: contains `Action Required` → waiting; first rune in U+2800–U+28FF (Braille spinner) → busy; **equal to `filepath.Base(plan.Cwd)`** → idle; anything else (empty, the hostname tmux shows before the first OSC title, an unexpected format) → unavailable <!-- rev: fable --> — an unrecognised title must never *assert* idle, it falls through to L3 | busy `esc to interrupt`; waiting `Press enter to confirm or esc to cancel`, `Would you like to make the following edits\?`, `Press enter to continue`; idle `Ask Codex to do anything` |
| **OpenCode** | SSE `GET http://127.0.0.1:<port>/event` (Basic auth): `session.status` → `.status.type` `busy`\|`retry` → busy, `idle` → idle; `permission.v2.asked` → waiting; `permission.v2.replied` → busy. If SSE will not connect: `GET /session/status?directory=<workdir>` per tick — any `"busy"` entry → busy, empty map → idle **[V]**. <!-- rev: fable --> The port is per Socrates session (`OpenCodeAccessOf(id)`), so several OpenCode instances never share a stream. Within one stream keep a **set of busy `sessionID`s** (parent and sub-agent sessions both emit `session.status`): busy while the set is non-empty, idle when it empties — an `idle` for one id must not clear another's `busy`. On every (re)connect seed the set from one `GET /session/status` first, because the stream carries deltas only and a status change during the gap would otherwise be missed. `permission.v2.asked` → waiting until the matching `requestID` is `replied` | busy `esc interrupt` (no "to" — a different literal from the other two); idle `ctrl+p commands` present and `esc interrupt` absent. **Never infer `waiting` from an OpenCode screen** — no verified pattern exists |

**Claude pid resolution.** `#{pane_pid}` first: tmux `exec`s our argv directly (`new-session … -- argv…`, manager.go:596), so it is the harness's own pid. If `<home>/.claude/sessions/<pane_pid>.json` is absent, walk the pane's descendants once (Linux `/proc/<pid>/task/*/children`, depth ≤ 3, ≤ 64 pids; otherwise `ps -eo pid=,ppid=`) and take the descendant that has such a file, newest mtime. Cache per session; drop it when the file has been absent for 3 ticks, when `pane_pid` changes, or on `ResetActivity`. `<home>` is `plan.Env["HOME"]`, else `os.Getenv("HOME")`. <!-- rev: fable --> A file only counts when its `.pid` equals the candidate pid **and** its mtime is not older than that process's start time (`/proc/<pid>/stat` field 22 against btime; elsewhere `ps -o lstart=`): `~/.claude/sessions/` is never garbage-collected, so after a reboot a recycled pid can point at a dead session's file that still says `busy`. `claude --resume` after re-adoption is the ordinary case — a new pid, a new file, the old one ignored because it is keyed by a pid that is not in the pane's tree. Codex's title needs tmux's `allow-set-title` at its default `on`; the generated conf sets `set-titles off`, which is a different option (the outer terminal's title) and does not interfere.

**OpenCode access.** `harnesses.OpenCodeAccess(id)` falling back to `plan.json` — the existing `termux.openCodeAccess(id, plan)` (ensure.go:423) already does this and is why port and password survive a server restart. Promote it to `func (m *Manager) OpenCodeAccessOf(id string) (harnesses.ServerAccess, bool)` reading `readPlan(id)` itself. One SSE goroutine per running OpenCode session, reconnect backoff 1 s → 15 s, started on launch/adopt, cancelled on exit, delete and `ResetActivity`.

**L2, quiescence** (all harnesses, from `#{window_activity}`): last output ≤ `QuietBusy` (2 s) → busy; ≥ `HardQuiet` (30 s) → idle; between → no answer. This works because all four TUIs repaint a spinner line several times a second while working, so "no bytes for thirty seconds" is a real fact, and a hung harness emits nothing at all.

**No hook or notify injection.** Claude's `Stop`/`Notification` hooks, Codex's `notify` and OpenCode's plugins are deliberately **not** used: each adds a config surface, an argv/TOML quoting risk and a child process running inside the harness, in exchange for confirming what L1 already reports exactly and for free. Claude's file carries `waiting`, which no hook does; Codex's title carries approval, which `notify` does not. The budget buys L3 instead, which is needed regardless.

## A.3 Arbitration

Per session, per tick, in order:

```
1. pane missing or #{pane_dead}=1 -> commit(unknown), source "pane"; stop this session.
2. obs := L1()
   - unavailable but seen within the last ExactMissTicks(3) ticks -> reuse the last L1 answer.
   - Claude's file is NOT judged by statusUpdatedAt: it is only rewritten on a state change, so a
     long turn legitimately carries an old timestamp. It is fresh while the file exists and the
     pid is alive; it is unavailable when the file is gone.
3. obs unavailable -> obs := L3()   (scrape, at most ScrapeEvery(2s) per session)
4. obs unavailable -> obs := L2()
5. obs unavailable -> obs := {unknown}
6. Runaway guard. Committed busy with no L1 answer for BusyCeiling(120s) -> L1 is dropped for this
   session until it answers again, and 3/4 decide. If L2 then says idle (HardQuiet: 30 s with no
   output at all), commit idle whatever L3 thinks. This is what makes "spins for ever" impossible:
   a pane that is alive and silent for thirty seconds with no exact signal is idle.
   <!-- rev: fable --> Quiescence never overrides an exact answer. A live L1 `busy` wins over any
   amount of silence, without a ceiling: a five-minute test suite that prints nothing is `busy`
   for Claude (file says busy), Codex (spinner title keeps animating), OpenCode (SSE busy) and the
   Shell (foreground command is not the shell / the shell has a child). Step 6 exists only for a
   session whose exact source is gone; and since all three TUIs repaint a spinner or a running
   timer several times a second while a tool runs, thirty seconds of no bytes from a TUI with no
   exact signal is idle in practice too. `BusyCeiling` counts from the tick L1 last answered, not
   from the busy commit.
7. Waiting is sticky against quiescence. Once waiting is committed L2 may not move it — a
   permission prompt is silent by nature and may sit for an hour while the user drives; L1, L3
   (a recognised non-waiting screen) and pane death may. <!-- rev: fable --> When L1 and L3 are
   both unavailable, L2 idle releases waiting only after an input frame has been accepted for the
   session since the waiting commit (`NoteInput` records the time): the prompt was answered and
   thirty silent seconds followed. Without such an input, waiting stays — there is no
   `WaitCeiling` to `unknown`, because a prompt that is still on screen is more useful shown than
   blanked, and a static ring cannot "spin for ever".
```

`commit(next)` debounces: `busy` immediately (the spinner must appear at once); `waiting` immediately; `idle` after **2 consecutive ticks** (`IdleSettle`) — this is the anti-flicker between two tool calls; `unknown` after 3 consecutive ticks (`UnknownSettle`) so a transient read failure never blanks a row.

Constants, all in `activity.go`: `ActivityInterval 1s · ScrapeEvery 2s · IdleSettle 2 ticks · UnknownSettle 3 ticks · ExactMissTicks 3 · BusyCeiling 120s · HardQuiet 30s · QuietBusy 2s`. <!-- rev: fable --> (`WaitCeiling` removed, see step 7.)

On a committed change (state, note or unread) the manager fires `Config.OnActivity(sessionID string, a Activity)` — a third callback beside `OnExit`/`OnSize`, wired in `server.go` the same way.

## A.4 Unread

One server-side rule: `markUnread = (prev == busy && next != busy) || (prev != waiting && next == waiting)` — work finished, or the agent started needing you. `unknown` counts as "not busy", so a pane that dies mid-turn also marks unread: something ended and nobody saw it.

Cleared by, and only by: any input frame accepted for that session (`termConn.onInput`, after the write succeeds → `manager.NoteInput(id)`), the `read` control frame, or `POST /api/sessions/{id}/read`. Focus alone does not clear it; the frontend never bolds the attached session (§D.1), so "focus it and type" is covered by the input rule without the server knowing about focus.

## A.5 Lifecycle edges

- **Pane exits / process dies** — step 1: `unknown`, unread per §A.4, OpenCode watcher cancelled, Claude pid cache dropped. The existing `exit` frame is unchanged and still owns the overlay.
- **Harness restart / resume / relaunch** — `Manager.ResetActivity(id)` from `Relaunch` and `resume`: state → `unknown` (uncommitted, so it re-settles), caches dropped, OpenCode watcher restarted. Unread is **not** cleared: work that finished before the restart is still unread.
- **Server restart / re-adoption** — nothing to restore: every state is `unknown` until the first tick, one second later. Unread comes back from kv. `StartActivity` starts an SSE watcher for every adopted OpenCode session.
- **Session created before this feature existed** — no special case: every input comes from the live pane or `plan.json`, and both exist for every session ever created. If `plan.json` is unreadable, Shell falls back to the set `{sh,bash,zsh,fish,dash,ksh,ash,busybox,login}` for "this is the shell itself".
- **Signal source unavailable** (no Claude session file, OpenCode refusing, empty Codex title) — steps 3–6: scrape, then quiescence, then the runaway guard. A session never sticks.
- **tmux unreachable** — the tick returns without touching any state. `unknown` settles only when the command *succeeded* and the session was absent, never on an error.

## A.6 Go surface

New `internal/termux/activity.go`:

```go
func (m *Manager) StartActivity(ctx context.Context)
func (m *Manager) ActivityOf(id string) Activity
func (m *Manager) Activities() map[string]Activity // running sessions only
func (m *Manager) NoteInput(id string)             // clears unread, fires OnActivity
func (m *Manager) MarkRead(id string)              // same, from an explicit read
func (m *Manager) ResetActivity(id string)
```

New `internal/termux/screen.go` — the primitives (b) and (c) also need:

```go
// CapturePane returns the last n lines of the pane, joined, free of escapes.
// tmux: capture-pane -p -J -t soc_<id> -S -<n>   [V]  (trailing blank lines trimmed)
func (m *Manager) CapturePane(ctx context.Context, id string, lines int) (string, error)

type Key struct {
    Text string        // literal UTF-8; sent as send-keys -H <hex bytes…>  [V]
    Name string        // a tmux key name: Enter, Escape, Tab, Up, C-c, …   [V]
    Wait time.Duration // sleep after this action, capped at 10s
}
// SendKeys types into the pane with `tmux send-keys`, which needs no attached viewer — an
// operator run keeps working with every browser closed.
func (m *Manager) SendKeys(ctx context.Context, id string, keys []Key) error
```

`-H` (hex) rather than `-l` for text: byte-exact for UTF-8 and immune to version differences in how `-l` treats backslashes; **[V]** both forms tested.

## A.7 Tests

**Unit, `internal/termux/activity_test.go`, no tmux.** The layers sit behind

```go
type snapshot struct{ row store.Session; pane paneLine; plan harnesses.LaunchPlan; now time.Time }
type observation struct{ state State; note string; ok bool }
type source interface{ Read(ctx context.Context, s snapshot) observation }
```

with a registry `map[config.Harness]source` and a test-only `m.setSource(kind, src)`. A `fakeSource` plus a fake clock drive table tests over tick sequences, asserting the committed state **and** the exact `OnActivity` calls. Required rows: busy→idle debounce (one stray idle tick does not commit); idle→busy immediate; waiting sticky against quiescence; L1 vanishing → L3 → L2 ladder; `BusyCeiling`+`HardQuiet` releasing a stuck busy; **L1 busy with 5 minutes of silence stays busy** (the long silent tool run) <!-- rev: fable -->; waiting with L1/L3 gone is released by L2 only after a `NoteInput`, and never without one; Shell with `pane_current_command == bash` but a child process is busy; Codex with a hostname title is unavailable, not idle; a Claude file older than its process start is ignored; OpenCode two busy ids and one idle keep busy; pane death mid-busy sets unread; `NoteInput` clears it; unread survives a manager rebuild through kv; `ResetActivity` keeps unread and drops state.

**Parsing**: `parseActivityPanes` against a literal Codex line whose title contains a pipe; each harness's L3 regex set against the verbatim busy/idle/prompt screens in the research file, all four harnesses.

**Against real tmux** (existing `main_test.go` harness, isolated socket): `#{pane_current_command}` flipping `bash`→`sleep`→`bash`; `CapturePane` returning what was echoed; `SendKeys` delivering text and named keys with no viewer attached; the six-field format parsing.

**e2e fake — `e2e/fakebin/faketui/main.go`.** New commands, behaviour chosen from `os.Args[0]` so one binary tests all four harnesses:

- `/busy <ms>` — paint that harness's real busy furniture (`✻ …esc to interrupt` for claude, `Working (Ns • esc to interrupt)` for codex, `⠏ Thinking` + `esc interrupt` for opencode), repaint every 100 ms, **and** drive the exact signal: write `~/.claude/sessions/<pid>.json` with `{"pid":…,"status":"busy","statusUpdatedAt":…}`, emit a Braille-prefixed OSC 2 title for codex, emit `session.status`/busy on the opencode SSE stream. After `ms`, revert everything to idle.
- `/ask` — the verbatim permission screen plus `"status":"waiting","waitingFor":"permission prompt"`, the `[ . ] Action Required | <dir>` title and `permission.v2.asked`. Cleared by Enter, `1` or `y` (which also emits `permission.v2.replied`).
- `/hang <ms>` — busy furniture on screen, then **no output and no signal updates at all**: the `BusyCeiling`/`HardQuiet` test.
- `/nofile` — stop maintaining the Claude session file: the "signal source unavailable → scrape" test.
- `/title <text>` — raw `ESC]2;<text>BEL`.
- opencode mode gains, on the HTTP server it already runs: `GET /event` (SSE, heartbeat every 10 s, emitting the events above) and `GET /session/status` (the map form) — the fake OpenCode server.

---

# B. Server API

## B.1 WS frames

Server→client on the per-session socket, `map[string]any` with key `t`, like every existing frame:

```json
{"t":"activity","sessions":{"<id>":{"state":"busy","unread":false,"since":1788362216000,"source":"exact","note":""}}}
```

Sent to **every open `termConn`, whatever session it is attached to** — a new `Server.broadcastAll(frame func(*termViewer) any)` beside `broadcast`. Only changed sessions are in the map. This is what keeps the sidebar live for sessions nobody is watching without a poll; `refreshList()` and `hello` are the catch-up path when no socket is open at all.

`helloFrame` gains `"activity": {"<id>": {…}}` (the full map, every running session) and `"agent": {…}|null` (§B.4), so a reconnect or reload re-renders both without a request.

Client→server, in `onControl`'s switch: `{"t":"read","id":"<sessionID>"}` → `manager.MarkRead(id)`. The id is explicit because the sidebar clears unread for rows that are not the attached session.

## B.2 Session list

`sessionView` (sessions.go:75) gains one field, so the poll path carries everything: `Activity termux.Activity \`json:"activity"\``, populated by `s.view` from `s.manager.ActivityOf(row.ID)`. `GET /api/sessions` and `GET /api/sessions/{id}` are otherwise unchanged.

## B.3 REST

```
POST /api/sessions/{id}/read         -> 200 {"session":<view>}
POST /api/sessions/{id}/status       -> 200 {"text":"…","language":"de","state":"idle","model":"…"}
POST /api/sessions/{id}/agent        {"prompt":"…"} -> 202 {"run_id":"…"} | 409 while a run is live
POST /api/sessions/{id}/agent/cancel -> 200 {"ok":true}
GET  /api/sessions/{id}/agent        -> 200 {"run":<run>|null}
```

All under `s.auth`, all resolving the row with the existing `s.session(w, r)` helper. New file `internal/server/assist.go`.

**TTS is not in the status response.** The browser takes `text` and calls the existing `POST /api/voice/speak` through `voice.js`'s `speak()`. That path already owns the length-scaled deadline, the "Piper is still installing" 503 and the stop/generation logic; the audio is ~150 KB with no business in a JSON body; and the same text is shown as well as spoken. `/api/voice/speak` needs no change. `language` is echoed from `settings.voice.language` so the page can label it.

## B.4 The operator run

One run per session, in a goroutine owned by the `Server` (`agentRuns map[string]*agentRun`, mutex-guarded), started by the POST and **outliving the browser** — a phone that locks mid-run comes back to a finished run. `agent/cancel` cancels its context; so does deleting the session.

```go
type agentRun struct {
    RunID, Prompt, Phase, Action, Note, Summary, Error string // phase: thinking|acting|waiting|done|error
    Step int; Done bool; Started int64                        // Action is a short human sentence
}
```

Every phase change is broadcast with `s.broadcast(sessionID, …)`:

```json
{"t":"agent","run_id":"…","step":3,"phase":"acting","action":"pressed Enter","note":"…","done":false,"error":"","prompt":"…","summary":"","started":1788362216000}
```

<!-- rev: fable --> The `<run>` object in `GET …/agent` and in `hello.agent` is this frame without `t` — the same keys (`run_id, step, phase, action, note, done, error, prompt, summary, started`), so the page renders a run from any of the three sources with one function. `prompt` is included because in audio mode it is the transcript the user never saw.

WP1 provides the transport (`broadcast`, `broadcastAll`) and a pass-through `func (s *Server) emitAgent(sessionID string, payload map[string]any)`; WP2 only fills the payload.

**The loop.** Budget: `MaxSteps 12 · MaxWall 10m · MaxActionsPerStep 8 · MaxTextRunes 2000` per text action.

```
1. wait until ActivityOf(id).State != busy, up to 120s          (phase "waiting")
2. screen := CapturePane(id, 200)
3. ask the model: temperature 0, max_tokens 800, Chat(ctx, req, nil)   (phase "thinking")
4. validate and execute: one send-keys call per action, 120ms apart    (phase "acting")
5. wait for the state to leave busy again — as long as it takes within MaxWall (phase "waiting");
   the 90s bound applies only while the state is `unknown` (no detector answer to wait for)
6. step++; stop on done, on a cap, or when the pane dies
```

<!-- rev: fable --> Step 5 is bounded by `MaxWall`, not by a per-step timeout, so a single long tool run (a test suite, an install) costs one step and not one step per 90 s: with a 90 s cap a ten-minute build would have exhausted `MaxSteps` on screens that all said "still working". A run that is cancelled or whose pane dies mid-wait ends with `phase:"error"` and a plain `error` string. The run lives only in memory: after a server restart `GET …/agent` answers `null` and `hello.agent` is `null`; the page drops its progress line and says so in a toast.

**Action schema** — a bare JSON object, nothing else:

```json
{"actions":[{"text":"select the fable model"},{"key":"Enter"},{"wait_ms":500}],
 "done":false,"summary":"picked the model, now sending the prompt","note":""}
```

`key` is a fixed vocabulary of tmux key names, a superset of the key bar's (keybar.js `KEYS` has `Escape · Tab · Left · Down · Up · Right · Enter · Ctrl-C · Ctrl-D · Ctrl-Z` — no BSpace/Space/Home/End/Page keys; the operator needs those for TUI menus): `Enter · Escape · Tab · BSpace · Space · Up · Down · Left · Right · Home · End · PageUp · PageDown · C-c · C-d · C-z · C-l`. <!-- rev: fable --> Digits and letters are `text`, never `key`; anything else is a rejected action.

**Guards.** A `text` action containing a newline has it stripped and is noted back to the model — submitting is always an explicit `{"key":"Enter"}`, so a multi-line paste can never fire a turn half-typed. `C-c` may never be sent twice in a row; a second consecutive one is dropped with a note, and the third anywhere in a run aborts it. `C-d` is dropped for a Shell session (it closes the pane). A rejected or dropped action does not abort the step; the note reaches the model on the next one.

**No destructive-command blocklist.** A regex over `rm -rf` would be false safety: the operator types into a terminal that already runs as the user, and a model that wanted to could type the same bytes across two actions. The real guards are the ones above plus a bounded, cancellable run whose every action is visible in the UI. The one policy lever is `settings.agent.allow_shell` (default **true**): with it off, a run on a Shell session is refused with 400, so a shared deployment can restrict the operator to the three coding harnesses.

**Invalid JSON**: strip a ```json fence and parse; on failure retry the same step **once** with the parser's error appended as a user message; a second failure ends the run with `error: "the operator model did not answer with usable JSON"`. No OpenRouter key → 400 "open /admin and pick an agent model".

**System prompt outline** (assembled in `assist.go`, English, one string):

```
You drive a terminal that runs <harness label>. You are given the last 200 lines of the screen and
a goal from the user. Answer with one JSON object and nothing else:
{"actions":[…],"done":bool,"summary":"…","note":"…"}.
An action is {"text":"…"} to type characters, {"key":"NAME"} for one of Enter,Escape,Tab,BSpace,
Space,Up,Down,Left,Right,Home,End,PageUp,PageDown,C-c,C-d,C-z,C-l, or {"wait_ms":N} to pause up to
10000 ms. Text never contains a newline; to submit, send Enter.
Take the smallest step that makes progress, then look again — at most 8 actions.
The screen may show a menu, a model picker or a permission prompt; answer it the way the goal
implies. Set done when the goal is reached or cannot be reached, and say why in summary.
Goal: <the user's prompt>
Screen:
<screen>
```

Each later step appends the previous decision and the new screen as further user messages; the conversation is capped at the last 4 steps to bound the tokens.

## B.5 The status prompt

Input: the harness label, the committed `State`, and `CapturePane(id, 120)` — escape-free by construction, so nothing has to strip ANSI. `Temperature 0.2`, `MaxTokens 300`, `Chat(ctx, req, nil)`.

```
You are told what a terminal running <harness label> currently shows. It is <busy|idle|waiting for
the user|in an unknown state>. In one to three short sentences, say what a person needs to know: if
it is working, what it is working on; if it has finished, what the answer is; if it is waiting, what
it is asking and what the choices are. Speak plainly — this is read out loud, so no markdown, no
code, no file paths unless they are the point. Answer in <English|German>.
Screen:
<screen>
```

**The phases.** The handler broadcasts `{"t":"status","id":"<sessionID>",
"phase":"capturing|asking|speaking|done|error","text":"…"}` to the session's
viewers as it goes: `capturing`/"Reading the screen", `asking`/"Asking <model>",
`speaking`/"Speaking" and `done` carrying the final text, or `error` carrying a
sentence. `speaking` is emitted immediately before the response, because the
browser hands the text to Piper the moment it has it and a second round trip to
announce that would arrive after the voice did. The page shows them in the
ticker (§D.3); the response body is unchanged.

**Language is `settings.voice.language`, not the language of the screen.** Piper renders with the voice of that one setting (voice.go `handleSpeak`), so German text in the English voice would be worse than English text; `config.LanguageName()` already exists to put the name into a prompt, exactly as `transcriptionHint` does. One setting, three sides, per the comment on `VoiceSettings.Language`.

## B.6 The title run

A session names itself the first time it has answered anything, so that the sidebar stops being a list of `Claude Code · 2 Sep 17:42`.

**The moment.** The first committed edge **out of `busy`** for that session — the same edge §A.4 marks unread on. `onSessionActivity` hands every committed state to `titleDriver.observe`, which keeps the previous state per session (the callback carries only the new one) and fires on `busy → idle|waiting|unknown`. It fires whether or not anybody is watching: the point of it is the sidebar of the browser that is looking at a different session. A session first seen while already idle has no edge and is not named — this server never saw an answer arrive.

**The run.** A goroutine owned by the `Server`, `context.WithTimeout` of 20 s, one entry per session in `titleDriver.live` so that two edges cannot start two runs, cancelled by `titleDriver.forget(id)` when the session is deleted. The tick is never made to wait for a gateway. It captures the pane (`CapturePane`, 200 lines) and asks the **agent model** (`openrouter.agent_model`, `config.DefaultAgentModel` when unset) with `temperature 0.3`, `max_tokens 60`. The prompt is assembled in English in `assist.go` beside the others (`titlePrompt`): 3 to 7 words, no quotes, no full stop, no markdown, **in the language the person on the screen is evidently writing in**, English when that is unclear. This is the one prompt whose language is the screen's and not `settings.voice.language`: a title is read, not spoken.

**Who may be renamed.** Only the coding harnesses — a Shell's screen is a prompt and a directory, and `cd` is not a subject. Only a session still carrying the placeholder: `store.Session.TitleSource` is `""` (nameless), `user` (typed at creation or renamed since — `UpdateSessionTitle` sets it, so the rename endpoint does) or `auto` (Socrates has had its go). The column is `sessions.title_source`, **schema 4**, added by `addSessionColumns` because `CREATE TABLE IF NOT EXISTS` does nothing to a table that exists. Persisted, so a restart does not retitle.

**Exactly once.** The turn is spent on the attempt, not on the answer: a model that refuses, answers with whitespace or is not a model at all still marks the session `auto`, or a wrong model id would be paid for at the end of every turn for ever. A missing API key is the exception — nothing was asked, nothing is marked, and the session is named the first time it answers after a key is added. No key, no model, a pane that cannot be read: all silent, nobody pressed anything.

**Sanitising.** `cleanTitle` strips a fence, keeps the first line, drops `**`/`` ` ``/`#`, collapses whitespace, takes quotes and trailing punctuation off in as many passes as it takes (a quoted title that ends in a full stop hides its closing quote behind it), and caps at 60 runes on a word boundary. Empty is a refusal and the old name stays.

**The frame.** `{"t":"title","id":"<sessionID>","title":"…"}` on `broadcastAll` — the name is in the sidebar of *every* browser and the session naming itself is usually not the one being watched. `session.js` handles it before the "is this my session" guard in `onControl`: the row and, when it is the attached session, the header. No animation; a row quietly getting a better name is not an event.

## B.7 The chat

The Agent button was a form with one field. It is now a conversation, because
"what should I do?" is a question and a question has an answer — and because
the same model that can say what a screen means is the one that decides whether
a request needs the keyboard at all.

```
GET  /api/sessions/{id}/chat            -> 200 {"messages":[<msg>…]}
POST /api/sessions/{id}/chat {text,auto} -> 202 {"ok":true,"msg":<msg>}
```

`<msg>` is `{"role":"user"|"assistant","text":"…","ts":<unix ms>,"run_id"?,"failed"?}`.
Every message is broadcast to the session's viewers as
`{"t":"chat","id":"<sessionID>","msg":<msg>}`, and `helloFrame` gains
`"chat": [<msg>…]` so a reconnect or a reload draws the panel without a request.

**Where it lives.** The key/value store, key `chat.<sessionID>`, the last 50
messages, written through `store.SetJSON` under one mutex; `store.DeleteKV`
removes it when the session is deleted. No migration: this is a small document
per session, like `activity.unread`.

**The answer.** `POST` appends the user's message and returns 202; the answer is
written by a goroutine that outlives the browser, exactly as an operator run
does. It is given the **agent model**, `temperature 0.3`, `max_tokens 600`, a
system prompt (`chatSystemPrompt`, assembled in `chat.go` beside the others,
English) and the last 12 messages of the conversation as ordinary turns. The
system prompt names the harness, carries `CapturePane(id, 150)` and the
committed state, says to answer plainly with no markdown, and — when `auto` is
set — that the reply is read out loud and should be one or two spoken
sentences.

**How it decides to act.** One bare JSON object and nothing else:

```json
{"reply":"what you say to the person","act":"<goal for the operator>"|null}
```

`act` is set **only** when the request cannot be answered in words because
something has to be typed. A question about the screen is answered with words
and `act: null`. When `act` is set, §B.4's operator run is started with that
goal and the reply is stored carrying its `run_id`, which is what makes that
bubble the place the Cancel button lives; the run's ending — its summary, or
the reason it stopped — is appended to the conversation when it finishes, by an
`onEnd` hook on `agentRun`. The steps in between are the `agent` frames the page
already has, and are not stored.

Tool calling was not used: `openrouter.Client` carries no tools field and no
`tool_calls` on the way back, and a protocol for one call site is a protocol
for nothing. An answer that is not an object is taken as the reply with
`act: null` — a model that wrote prose answered the question.

**Refusals.** No key → 400 with the "open /admin, add your key and pick an agent
model" sentence, shown in the panel where the answer was expected. Everything
else that can go wrong — an unknown model, a shell the operator may not drive, a
session with no terminal, a run already going — is stored as an assistant
message with `failed: true`, because the phone that reloads has to find out why
nothing came back.

---

# C. Settings

```go
// internal/config/config.go
const (
    DefaultStatusModel = "google/gemini-2.5-flash"     // fast and cheap; the transcription family
    DefaultAgentModel  = "anthropic/claude-sonnet-4.5" // it has to read a TUI and decide keystrokes
)
type OpenRouterSettings struct { … ; StatusModel string `json:"status_model"`; AgentModel string `json:"agent_model"` }
type AgentSettings struct {
    AllowShell bool `json:"allow_shell"` // default true
    MaxSteps   int  `json:"max_steps"`   // default 12, clamped to 1..40
}
// Settings gains: Agent AgentSettings `json:"agent"`
```

Seeded in `Default()` and filled in `Normalize()` exactly as `TranscribeModel` is (config.go:263-270): an empty model id takes the shipped default, `MaxSteps` outside 1..40 is clamped. <!-- rev: fable --> Nothing in the code validates a chat model id at runtime (`openrouter.Client` only remembers transcription routes; `Normalize` cannot reach the network), so the defaults are the ids known to exist on OpenRouter at the time of writing: `google/gemini-2.5-flash` is already `DefaultTranscribeModel` here, and `anthropic/claude-sonnet-4.5` is OpenRouter's id for that model. The implementer of WP2 should run `Client.Models()` once against a real key before merging and, if a newer Sonnet is listed, prefer it; and a model refusal from OpenRouter (400/404 on `/chat/completions`) must surface in the status/agent response as `"unknown model <id> — open /admin"`, never as a bare 500. There is no model catalogue endpoint any more, so both ids are free-text comboboxes with the current value offered back — the pattern `MODEL_PICKERS` already uses for `orTranscribe`.

**Admin placement**: a new card `#assistCard` in `admin.html`, immediately after `#voiceCard` and before `#tunnelCard`, headed "Status & Agent", with the same hairline layout as its neighbours and four controls: `#orStatus` (combobox → `openrouter.status_model`), `#orAgent` (combobox → `openrouter.agent_model`), `#agentAllowShell` (switch → `agent.allow_shell`), `#agentMaxSteps` (number → `agent.max_steps`). `admin.js`: two rows in `MODEL_PICKERS`, two in `FIELDS`. Nothing else in admin changes.

---

# D. Frontend

New module `internal/web/static/js/assist.js` — status, agent and audio mode — imported by `session.js`, added to `sw.js`'s `SHELL` and to the import-graph assertion in `internal/web/embed_test.go`. Sidebar and frame handling stay in `session.js`.

## D.1 Sidebar row

`buildRow` is unchanged; `updateRow` gains the activity classes.

- **busy** — `.row-mark.busy::after`: a 1 px `--line` ring around the existing agent mark with one `--text-faint` arc, `animation: spin 900ms linear infinite`. The spinner *is* the agent mark, so "agent marks everywhere a harness is named" holds and no new glyph is invented. Paused by the existing `body.stale` rule; under `prefers-reduced-motion` the ring is drawn complete and static.
- **waiting** — the same ring, static, and the state dot turns `--amber`. Distinct from busy at a glance without introducing a second colour.
- **unread** — `.chat-item.unread .label { font-weight: 600 }`, applied only when `session.id !== state.current?.id`: the session being looked at is never bold.
- `Activity.source`, `.note` and `.since` join `rowFacts()` behind the existing `infoTip` — technical strings stay hover-only.
- One visually-hidden `<div id="activityLive" aria-live="polite">` in the sidebar, written on a committed change ("Claude Code finished", "Codex needs an answer"), throttled to one message per 2 s.

`onControl` gains `case 'activity':` — merge `frame.sessions` into a new `state.activity` Map, `renderList()`, update the header, run the audio-mode trigger (§D.4). `hello` seeds the same map; `refreshList()` seeds it from each view's `activity` field. `selectSession(id)` sends `{"t":"read","id":id}` on the open socket, or `POST /api/sessions/{id}/read` when there is none.

## D.2 Header

Two `.icon-btn`s and one switch in the `.topbar` before `#sessionMenu`, hidden
until a session is attached: `#statusBtn` (speech bubble), `#agentBtn` (spark,
tooltip **"What should I do?"**) and `#audioModeBtn`. All three carry `disabled`
whenever `!state.live` — offline, nothing can be started and the control says
so by being unavailable rather than by failing.

`#audioModeBtn` is a real switch and not a pressed button: `role="switch"`,
`aria-checked`, a 44 px hairline track with a 18 px knob that fills with
`--text` when it is on, 150 ms, `prefers-reduced-motion` respected. It is
labelled **Auto** beside the track, with "Auto mode" as its accessible name.
The `localStorage['socrates.audio.mode'] = 'on'|'off'` semantics are unchanged;
**Auto mode** is what the feature is called everywhere.

New modules: `internal/web/static/js/chat.js` (the panel) beside `assist.js`
(status, the ticker, auto mode), both in `sw.js`'s `SHELL` and both reached by
the import-graph assertion in `internal/web/embed_test.go`.

## D.3 Status, the ticker and the chat

**Status** — `POST …/status`, and pressing it must visibly do something: the
button takes a spinner (`.icon-btn.working`) at once and the ticker shows the
first phase locally before the request has even left, then follows the server's
`status` frames. The final text lands in `#termNotice` (`kind:'status'`,
dismissible, model and state behind the "i") **and** in `speak()`.
`onSpeechError((msg, kind) => toast(msg, kind))` is registered once in
`mountAssist`.

**The ticker** — `#termTicker`, one window one line high, inside `.term-lines`
under `#termNotice`. Each new line is appended with `.enter`
(`translateY(100%)`, transparent), the class is dropped on the next frame and
the outgoing line takes `.leave` (`translateY(-100%)`): a departure board, 250
ms, `transform`/`opacity` only. Under `prefers-reduced-motion` the transition is
removed and the swap is plain.

There is **one** ticker and it is the only such indicator on the page. What it
says, in order of precedence:

1. a status being made, or one just finished (held 6 s);
2. the live operator run — "Step 3 · pressed Enter" — held 6 s after it ends;
3. in Auto mode only, and continuously, the attached session's activity:
   "Claude Code is working", "Claude Code is waiting for you", "Claude Code is
   idle".

Outside Auto mode, with nothing happening, it is hidden.

**The chat** — `#chatPanel`, opened by `#agentBtn`, by `#audioAgent` and by
nothing else. On a desk it is a 360 px column beside the terminal inside
`.stage`, and the pane refits when it opens or closes; under 860 px it is a
full-height sheet over the terminal with a close button. The log is user and
assistant bubbles, white with a hairline, distinguished by which edge they sit
on; assistant text is markdown-lite (paragraphs and inline code, nothing
heavier); a "Thinking…" placeholder stands where the answer will be. A message
carrying `run_id` grows a run row — the step, what it just did, and **Cancel**.
Everything arrives on the socket, so two devices watching one session see the
same conversation.

**The input row is one of two, never both.** With Auto mode **off** it is a text
field and Send: Enter sends, Shift+Enter is a newline. With Auto mode **on**
there is no text input anywhere in the panel — the row is one microphone at
least 64 px tall, tap to start, tap again to stop, `dictateOnce` from
`voice.js`, and the transcript is sent as the message with no confirmation
step. Assistant replies are spoken with `speak()`.

## D.4 Auto mode

`localStorage['socrates.audio.mode']`, per device, read in `boot()`. On:
`document.body.classList.add('audio-mode')` and the `<div class="audio-bar">`
between `#termWrap` and `#keybar` with `#audioStatus` and `#audioAgent`, two
full-width buttons at least 64 px tall. `#audioStatus` is Status (and Stop while
the voice is reading); `#audioAgent` opens the chat panel with the microphone
already recording, because opening it and then finding the button is two taps
for one sentence.

**Nothing in Auto mode may open a keyboard.** The composer and the key bar are
taken out of the layout (`body.audio-mode`), the chat panel builds a microphone
instead of a field, and the terminal's own hidden textarea is closed:
`term.setTyping(false)` makes it `readonly`, takes it out of the tab order and
blurs it on every focus attempt, so a tap on the pane cannot raise one. Output,
scrolling, selection and every path that sends bytes from somewhere else are
untouched. This is not conditional on a touch screen — a physical keyboard is
blocked too, because deciding otherwise would mean trusting a media query with
the one promise this mode makes. Leaving Auto mode gives all three back and
refocuses the pane.

**Auto-status**: on a committed transition **out of `busy`**, for the attached
session only, run Status and speak it. If the voice is busy the new one is
queued one deep and a third replaces the queued one. A live run owns the voice:
its own busy-to-idle is the run typing, and the sentence that mode wants is the
ending the run itself posts into the chat.

## D.5 Offline

`#statusBtn`, `#agentBtn`, `#audioModeBtn`, `#audioStatus`, `#audioAgent`, the
chat's field, its Send and its microphone are all `disabled` while
`!state.live`. The conversation stays on screen — it is history, not a live
view — and a run in flight keeps running on the server: the ticker and the run
row hold the last step they knew and pick up from `hello.agent` on reconnect,
while `hello.chat` re-seeds the panel. Activity goes stale with the rest of the
page under the existing `body.stale` rule, which stops the sidebar spinner and
the Status button's.

## D.6 The chime and the notification

The sidebar is only news to somebody looking at it. There is one moment worth
interrupting a person for — the same committed edge §A.4 marks unread on — and
`internal/web/static/js/notify.js` is the whole of what happens at it.

**The moment.** `mergeActivity` is the one door every change comes through, so
`notifier.completed(id, next, prev)` is called from it, beside
`state.assist.activity`. A **completion** is `prev` existing, `prev.state ===
'busy'`, and `next.state` being `idle` or `waiting`. It fires for **every**
session, attached or not — the session nobody is watching is the point of it.
It fires for none of: a first sighting (`prev` is null, which is also what a
`hello` replay and a reload look like), `unknown → anything` (nothing
finished; the detector merely found its footing), or a state that did not
change.

**The chime.** Two sine notes on one oscillator — 880 Hz then 1175 Hz, ~120 ms
each, a gentle envelope with a dip between them, the whole of it under 400 ms —
through a lazily created `AudioContext`, resumed if suspended. One oscillator
and not two, so one chime is one voice. No audio file, nothing to fetch, and
nothing to precache. At most **one chime per 1.5 s** across all sessions: six
sessions finishing inside a second are one piece of news. Notifications are
not rate limited — the `tag` handles duplicates. Every failure is silent: a
chime that did not happen is not worth a sentence on the screen.

**The notification.** `new Notification(session.title, { body, tag:
'socrates:' + id, renotify: true, icon: '/static/img/logo.png' })`, with
`body` the sidebar's own words — "Finished" for `idle`, "Needs an answer" for
`waiting`. The tag is the session, so a session that finishes twice while the
phone is locked leaves one line in the tray. Clicking it focuses the window,
calls `selectSession(id)` and closes itself. There is no visibility rule: a
session that finishes while its own page is open and visible still notifies,
because the person who asked for notifications asked for that too.

**The two switches**, both `.icon-btn` in the `.topbar` before `#sessionMenu`
and — unlike everything else in that bar — present whether or not a session is
attached, because they are facts about the device and not about a session:

- `#soundBtn`, `localStorage['socrates.sound']`, default **on**. Off shows a
  speaker with a stroke through it and `aria-pressed="false"`; the title and
  the accessible name are "Sound on" / "Sound off". Turning it on is the
  gesture the browser was waiting for, so the context is woken and the chime
  is played once as a preview.
- `#notifyBtn`, `localStorage['socrates.notify']`, default **off**, because it
  cannot be honoured without asking and asking unprompted is how a page gets
  blocked for ever. Turning it on calls `Notification.requestPermission()`
  from inside the click and only stays on for `granted`; anything else raises
  "Notifications are blocked for this site in the browser." and leaves it off.
  With no `Notification` at all — iOS Safari outside a home-screen app — the
  button is disabled and titled "Notifications are not available in this
  browser". A permission revoked between two visits turns the stored flag off
  at boot rather than being ignored on every completion.

Both are read at boot, every `localStorage` access is wrapped, and each glyph
pair is one drawing and the same drawing with a stroke through it, at the same
stroke weight, swapped by CSS off `aria-pressed` so nothing has to be kept in
step in script.

**e2e `notify`** stubs `window.Notification` and `window.AudioContext` with
`page.addInitScript` and measures counts, not clocks: the defaults, the ask,
both choices surviving a reload, one chime and one correctly titled note per
completion, muting silencing the chime and not the note, both off saying
nothing, a reload during a running turn firing nothing on the replay, and a
refused permission leaving the switch off with the reason in a toast.

---

# E. Work packages

WP2 and WP3 run in parallel after WP1. Everything they need from each other is in §B and §C.

## WP1 — the detector, the frames, the session API

**Files.** New: `internal/termux/activity.go`, `activity_test.go`, `screen.go`, `screen_test.go`. Changed: `internal/termux/manager.go` (`Config.OnActivity`, `StartActivity` in `Start`, `ResetActivity` in `Relaunch`), `internal/termux/ensure.go` (`OpenCodeAccessOf`, `ResetActivity` in `resume`), `internal/server/ws.go` (`activity` frame, `read` control frame, `hello.activity`/`hello.agent`, `NoteInput` in `onInput`, `broadcastAll`, `emitAgent`), `internal/server/server.go` (wire `OnActivity`, route `POST …/read`), `internal/server/sessions.go` (`sessionView.Activity`, `handleMarkRead`), `internal/server/ws_test.go`, `sessions_test.go`, `e2e/fakebin/faketui/main.go`.

**Acceptance.** `make check` green. The §A.7 unit table passes, fake source and fake clock included. Real-tmux tests cover `CapturePane`, `SendKeys` with no viewer attached, and the six-field format. `GET /api/sessions` carries `activity` for every session. A `read` frame and `POST …/read` both clear unread and both broadcast. An input frame clears unread. Unread survives a manager rebuild. `faketui` answers `/busy`, `/ask`, `/hang`, `/nofile`, `/title` and serves `/event` and `/session/status` in opencode mode.

## WP2 — Status, Agent, settings

**Files.** New: `internal/server/assist.go`, `assist_test.go`. Changed: `internal/server/server.go` (four routes), `internal/config/config.go` (`StatusModel`, `AgentModel`, `AgentSettings`, `Default`, `Normalize`), `internal/config/config_test.go`, `e2e/harness.mjs` (`openRouterStub` gains a `replies: []` queue returning each in turn and falling back to `text` when empty — the operator loop needs a different answer per step).

**Acceptance.** `make check` green. `POST …/status` returns the sentence the stub produced and sends the configured `status_model` with a screen of at most 120 lines. `POST …/agent` starts a run, returns 202, refuses a second with 409, and drives the fake pane through `send-keys`. `agent/cancel` ends it within a second. `GET …/agent` mirrors the live run and answers `null` afterwards. A stub returning prose instead of JSON produces exactly one retry and then an `error` run. Two consecutive `C-c` actions produce one `send-keys`. `allow_shell:false` refuses a Shell run with 400. A stub that never says `done` stops at `max_steps`.

## WP3 — Frontend, admin, e2e

**Files.** New: `internal/web/static/js/assist.js`. Changed: `js/session.js`, `js/voice.js` (`dictateOnce`), `index.html`, `css/app.css`, `sw.js`, `admin.html`, `js/admin.js`, `internal/web/embed_test.go`, `e2e/run.mjs`.

**Acceptance.** New e2e scenarios, fake CLIs on PATH and `openRouterStub` throughout: `activity-claude`, `activity-codex`, `activity-opencode`, `activity-shell` (each: `/busy 3000` → the row spins within 2 s → it stops and the title goes bold within 3 s of the end); `activity-waiting` (`/ask` → static ring, amber dot, unread); `activity-fallback` (`/nofile` + `/hang 20000` → the row still leaves busy inside 35 s, proving the ladder); `unread` (bold clears on typing, and on tapping the row from another session); `status-speak` (the text appears in the notice and `/api/voice/speak` is called with it); `agent-run` (prompt → progress line → the fake pane received the keystrokes → done); `agent-cancel`; `audio-mode` (the toggle persists across a reload, two big buttons with the terminal still visible, one busy→idle transition triggers exactly one status call); `design` (the existing scenario extended: the spinner is `--text-faint` on white, motion is 120–900 ms and off under reduced motion, no technical string is visible outside an `infoTip`).

---

## Review verdict

**APPROVED WITH CHANGES** — reviewer: fable, 2026-09-02. All edits are marked `<!-- rev: fable -->` in place.

Verified against the code: `voice.js` exports `speak`, `onSpeechError`, `isSpeaking`, `stopSpeaking`, `Recorder`, `plainSpeech` (no `dictateOnce` yet — WP3 adds it as specified); routes `POST /api/voice/transcribe|speak`, `GET /api/voice/status` exist under `s.auth`; `openrouter.Client.Chat(ctx, req, nil)` and `Models()` exist, `Temperature` is a `*float64`; `Config.OnExit/OnSize` are the only callbacks (manager.go:76-77); `new-session … -- argv` execs the harness directly (manager.go:591-597); `openCodeAccess(id, plan)` is at ensure.go:423; `s.session(w, r)` at sessions.go:268; `onControl` cases are `ping|lag|resize|bye`; faketui commands today are `/exit /spin /alt /id` plus an HTTP `/session` handler in opencode mode; `DefaultTranscribeModel` is already `google/gemini-2.5-flash`; the generated tmux conf sets `remain-on-exit on` and `set-titles off` and leaves `allow-set-title` at its default.

Changes made:

1. **Shell L1** — added the "shell has no child process" condition. Without it `bash script.sh`, `sh -c`, any `#!/bin/bash` script and a nested shell read as idle for their whole run (the 5-minute silent command case for the Shell harness).
2. **Codex L1** — idle only when the title equals `filepath.Base(plan.Cwd)`; any other title is *unavailable*, not idle. tmux reports the hostname before the first OSC title, and "non-empty → idle" would have asserted idle from it.
3. **Claude pid** — a session file counts only if `.pid` matches and its mtime is not older than the process start (pid recycling after reboot; `~/.claude/sessions/` is never cleaned).
4. **OpenCode** — per-`sessionID` busy set (sub-agent sessions emit `session.status` too), seed from `/session/status` on every (re)connect, waiting cleared by the matching `requestID`.
5. **Arbitration step 6** — made explicit that L1 always overrides quiescence, with no ceiling; `BusyCeiling` counts from the last L1 answer. Long silent tool runs are safe on every harness because every harness has an L1 that says busy; step 6 only applies when the exact source is gone.
6. **Arbitration step 7** — removed `WaitCeiling → unknown`. A permission prompt may sit for an hour while the user drives; blanking it after 5 minutes is the wrong outcome. Waiting is now released by L1, L3, pane death, or — only when both are unavailable — by L2 idle after an input frame was accepted since the commit.
7. **Operator loop step 5** — the wait for busy→not-busy is bounded by `MaxWall`, not 90 s per step; otherwise one long build burns all 12 steps. Documented that a run does not survive a server restart.
8. **Key vocabulary** — the spec claimed it equals keybar.js `KEYS`; it does not (`KEYS` lacks BSpace/Space/Home/End/Page keys and names Ctrl keys `Ctrl-C`). The vocabulary is now stated as its own fixed list.
9. **`<run>` shape** — defined for `GET …/agent` and `hello.agent` (the `agent` frame minus `t`, plus `prompt`/`summary`/`started`) so WP2 and WP3 agree without talking.
10. **Model ids** — noted that nothing validates a chat model id at runtime; keep the defaults but check `Models()` against a real key before merging and surface a model refusal as a readable 4xx.
11. **Unit table** — rows added for each of the above.

Non-blocking notes for the implementers:

- **Unread after watching it finish.** Rule §A.4 marks unread on busy→idle even when the user is looking at that very session with the tab visible; they only notice when they switch away and the row is bold. Consider having the page send `{"t":"read"}` for the attached session when a busy→non-busy frame arrives while `document.visibilityState === 'visible'` — the owner's "until the user interacts" is satisfied either way; this is a call for the owner.
- **Restart/resume by the user.** `Relaunch`/`resume` keep unread (§A.5). A restart the user clicked is an interaction; consider `MarkRead` in the two HTTP handlers (not in the manager) so a crash-driven relaunch still keeps it.
- **Claude `L1` on a slow start.** Until the file appears the ladder goes L3 → L2; a Claude that is starting up prints nothing for a few seconds and L2 will say nothing, so the row stays `unknown` (no mark) — fine, but the e2e `activity-claude` scenario should start the fake with the file already written.
- **`#{window_activity}` and the user's own keystrokes.** Echoed input updates it, so L2 sees "busy" for 2 s after typing at an idle prompt. Harmless (L2 is last resort, `IdleSettle` absorbs it), but the L2 unit rows should include that case.
- **OpenCode SSE goroutines on a server with many sessions.** One long-lived HTTP connection per running OpenCode session is fine at the expected scale; cap reconnect attempts per minute so a dead port does not log every second.
- **`SendKeys -H` and Enter.** `send-keys -H 0d` and `send-keys Enter` are both fine, but keep the operator's `Enter` as the named key so Claude's multi-line input does not see a bare CR as "newline in text".
- **Status endpoint while the run is acting.** Allowed by the spec; the summary will describe a half-typed screen. Acceptable, but say "the agent is typing" in the notice when a run is live instead of calling the model.
- **e2e `activity-fallback`** — with `WaitCeiling` gone the scenario is unchanged (`/nofile` + `/hang 20000` → leaves busy within 35 s via `HardQuiet`); add `activity-waiting-sticky`: `/ask` + `/nofile`, wait 40 s, the ring must still be amber.
