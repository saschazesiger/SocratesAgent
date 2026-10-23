# Agent host and chat event model (WP2, 2026-10-23)

Status: **implemented** in `internal/agent`, `internal/agenthost`, `internal/server/chats.go` and
`chatstream.go`. This is the contract WP3–WP8 build on. REBUILD.md §2 is the plan; this document
is what the code does, field by field. Where the two differ, DEVIATIONS.md → "Rebuild" says why.

## 1. Processes and files

```
browser ── POST /api/chats…, GET /api/chats/{id}/stream (socrates.chat.v1, §7)
   │
socrates serve ── chatHub: one chatPeer per chat (journal copy in memory, projection,
   │               host connection, pump); outbox in SQLite (chat_outbox)
   │  unix socket, line JSON, protocol 1 (§4)
   ▼
socrates agent-host --data <data> --chat <id>      one per active chat, own scope
   │  owns: the adapter, the CLI child, the journal (only writer), parking
   ▼
CLI child (WP3–5) / the fake adapter's stand-in child (`sleep`)
```

A chat is a **session row** with `kind = 'chat'` (schema 10). It shares the sessions table with
terminals, so the list, shelves, unread mark, ordering, push and Done sweep apply unchanged. The
CLI's conversation id is stored in `cli_session_id` (`cli_session_state = known`). Terminal loops
(adoption, lifecycle poll, the detector tick, digests, titles, the operator's routes) skip chat
rows; the terminal-only routes (`…/ws`, `…/journal`, `…/resume`, `…/restart`) answer 409.

| Path | Written by | What |
|---|---|---|
| `<data>/chats/<id>/events.jsonl` | host | the journal (§3); the chat history Socrates renders and backs up |
| `<data>/chats/<id>/host.json` | server, before every launch | launch spec: harness, workdir, model, effort, account, env, `park_after_ms` |
| `<data>/chats/<id>/host.lock` | host | `flock`ed for the host's whole life: **the** evidence a host is alive |
| `<data>/chats/<id>/host.log` | host (and `systemd-run`) | the host's stdout/stderr; a file, never a pipe |
| `<data>/chats/<id>/agent/` | adapter | the adapter's own state (the fake keeps its conversations here) |
| `<data>/run/chats/<id>.sock` | host | the socket; if that path is ≥ 104 bytes, `$TMPDIR/socrates-<uid>/<sha256(data)[:6]>/<id>.sock` (both sides compute it) |

Chat ids are `[A-Za-z0-9_-]{1,64}` (the server uses 32 hex characters); they become file, socket
and unit names.

## 2. Event model

Every journal line, every `ev` frame on the host socket and every `ev` frame on the browser stream
is one `agent.Event`: a flat JSON object, all fields optional except `seq`, `at` (Unix ms) and
`type`. `seq` and `at` are stamped by the journal; adapters never set them.

| type | fields | meaning |
|---|---|---|
| `turn.started` | `turn_id`, `message_id`? | the agent took up a user message (or started on its own) |
| `turn.ended` | `turn_id`, `outcome` (`completed`\|`interrupted`\|`failed`\|`limited`), `usage`, `cost_usd`, `text` (why) | end of the turn |
| `message` | `message_id`, `role` (`user`\|`assistant`), `state`, `client_id`?, `text`?, `attachments`?, `answer_to`?, `turn_id`?, `model`? | creates a message. **User messages carry text and attachments inline**; assistant messages are containers for parts |
| `message.state` | `message_id`, `state` | user: `pending` (journaled by the host) → `queued` (the agent holds it behind a running turn) → `delivered` (taken up). Assistant: `streaming` → `done`\|`interrupted` |
| `part` | `message_id`, `part_id`, `kind`, `state`, `text`, `content` | creates **or replaces whole** the part with that id (kind, state, text, content) |
| `part.delta` | `message_id`, `part_id`, `offset`, `text` | appends `text` to the part's text; `offset` is where it starts, in **UTF-16 code units** |
| `ask` | `ask_id`, `turn_id`, `payload` (WP6's `ask_user` schema, opaque here) | a question the agent is blocked on |
| `ask.answered` | `ask_id`, `answer`, `by_device`, `client_id` | the first answer; later ones are refused (§4.4) |
| `ask.delivered` | `ask_id`, `via` (`tool`\|`message`) | how the answer reached the agent |
| `account.switched` | `from`, `to`, `reason` | divider (WP7) |
| `model.changed` | `model`, `effort` | for the turns that follow |
| `rate_limit` | `window`, `used` (0..1), `resets_at` (Unix s) | one usage window |
| `usage` | `usage.context`, `usage.window` | context in use, outside a turn's end |
| `process` | `state` (`starting`\|`ready`\|`parked`\|`exited`\|`failed`), `detail`, `pid`, `version` | host/CLI life. `starting` carries the host pid, `ready` the CLI pid |
| `conversation` | `conversation_id` | the CLI's own id, needed to resume |
| `tasks` | `count` | background tasks still running |
| `command` | `op` (`interrupt`\|`set_model`\|`command`\|`park`\|`stop`), `client_id`, `model`/`effort`, `text` (command name), `args` | a command the host accepted; recorded so its `client_id` is acted on once |
| `notice` | `code`, `text`, `ask_id`? | a line of its own. Host codes: `resumed`, `answer_as_message` |
| `error` | `code`, `text`, `message_id`? | a failure that is not a turn's end (e.g. `send_failed`, `rate_limit`) |

Part kinds: `text`, `reasoning`, `tool`, `diff`, `plan`, `todo`, `image`, `file`, `notice`. Part
states: `streaming`, `running`, `done`, `error`, `interrupted`. `content` is kind-specific JSON
whose shape the adapters define (WP3–5 must document theirs here); a tool part's **streamed output
goes into `text`** so that every stream is a `part.delta`.

`usage` object: `input`, `output`, `cache_read`, `cache_write`, `reasoning`, `context`, `window`.
Attachment: `name`, `path` (absolute), `mime`, `size`.

## 3. Journal, coalescing, projection

**Format.** `events.jsonl`, one event per line, `seq` from 1 without gaps, append-only, one writer
(the host, guaranteed by `host.lock`).

**Coalescing** (`agent.Recorder`). `Emitter.Delta(message, part, text)` goes out **at once,
uncoalesced** to every connected reader as a `delta` frame (`message_id`, `part_id`, `offset`,
`text`), and into a per-part buffer that becomes one `part.delta` line 250 ms after its first
delta. Before any other event is journaled, all pending deltas are written first (the journal is
always the order things happened in); a `part` event for a part drops that part's pending deltas,
because it replaces the part whole. The adapter always ends a part with a `part` event carrying the
final state and full text.

**Durability.** Lines are written with one `write` per batch. Events that acknowledge a command are
synced (`fsync`) before the ack; everything else is synced at most 250 ms after it was written. A
power loss can therefore take only unacknowledged tail lines, and at worst leaves a torn last line.

**Reading.** `ReadJournal` (anyone, while the host writes) skips any line that is not a whole event
or whose seq does not move forward. `OpenJournal` (the host) additionally cuts a torn tail off and
adds a missing final newline, so the next append starts clean.

**Projection** (`agent.Projection`, `Fold(events)`, `Apply(ev)`): messages in order with their
parts; asks; the running turn and the last one; model, effort, account, process state,
conversation id, tasks, summed usage and cost, rate limits; the `client_id → first seq` map (the
host's dedupe set). Rules:

- An event whose seq is ≤ the last applied is ignored.
- `part.delta` is idempotent by offset (`agent.ApplyDelta`): text the part already has is not
  appended again; a delta that starts beyond the end is a gap and ignored. Live `delta` frames and
  the coalesced `part.delta` that repeats them can therefore both be applied, in any order, and a
  browser does the same.
- `turn.ended` with an outcome other than `completed` sets every `streaming`/`running` part to
  `interrupted` and every streaming assistant message to `interrupted`.
- `Undelivered()`: user messages not `delivered`. `UnsentAnswers()`: answered asks with no
  `ask.delivered`. A new host hands both to the agent (§6).

**Summary / activity** (`Projection.Summary()`): `activity` is `waiting` while any ask is
unanswered, else `busy` while a turn runs, else `tasks` while background tasks run, else `idle`.
The server reports it to the terminal detector with `termux.ReportActivity` (committed exactly, no
debounce; note `question` for waiting; `asked` = the chat has a user message). The unread mark, row
ordering (`MovesRow`), refiling into in-progress, the `activity` frames and push are the terminals'.

## 4. Host protocol (version 1)

Unix socket, mode 0600, one JSON object per line in each direction, lines ≤ 16 MiB.

### 4.1 Handshake

Client's first line, within 10 s: `{"op":"hello","proto":1,"since":<last seq it has>}`.
Host: `{"t":"hello","proto":1,"version","chat","harness","host_pid","cli_pid","last_seq","process"}`
(`proto` = min of both), or `{"t":"err","code":"protocol",…}` and close when the client's `proto` is
below the host's minimum. The server accepts hosts speaking `MinProtocol..Protocol` (currently
1..1); when the protocol changes, keep the old version readable for a day (a busy host of the
previous build lives on, REBUILD §2.1).

### 4.2 Host → client

- `{"t":"ev", …event}` – every journaled event with seq > `since`, then every new one. Replay and
  live tail are **one pull cursor** over the host's in-memory journal: no gap, no duplicate, and a
  slow client only slows itself (write deadline 30 s, then dropped; reconnect with `since`).
- `{"t":"delta","message_id","part_id","offset","text"}` – live, never journaled, never ahead of
  the event that created its part; dropped for a client more than 4096 behind.
- `{"t":"ack","client_id","op","seq","dup","answered_elsewhere","by_device"}` – the command's
  events are journaled **and synced**. `seq` is the first event it produced; a repeat gets the same
  seq with `dup:true`. An ack is written after every event before it.
- `{"t":"err","client_id","op","code","msg"}` – codes: `bad_request`, `protocol`, `not_running`,
  `unknown_ask`, `unsupported`, `failed`, `stopping`. The server keeps `not_running`/`stopping`
  commands in its outbox (they go to the next host) and drops the rest.

### 4.3 Client → host

| op | fields | host does |
|---|---|---|
| `send` | `client_id` (required), `text`, `attachments` | journals `message{user, pending}`, hands it to the adapter, acks |
| `interrupt` | `client_id`? | journals `command`, `Adapter.Interrupt` |
| `answer` | `client_id`, `ask_id`, `answer` (JSON), `device` | §4.4 |
| `set_model` | `client_id`?, `model`, `effort` | journals `command`, `Adapter.SetModel` (which emits `model.changed`) |
| `command` | `client_id`?, `name`, `args` | journals `command`, `Adapter.Command` (`compact`, or a slash command); unknown → `unsupported` |
| `park` | `client_id`? | parks as soon as idle (§6) |
| `stop` | `client_id`? | stops the CLI now, records an interrupted turn, exits |

Reserved for WP7: `switch_account` (park + relaunch with the other account's env, `account.switched`).

Commands are processed one at a time per host, in arrival order. **Idempotency:** a `client_id`
the projection has already seen is acked `dup` and not acted on. The set is rebuilt from the
journal on every start, so it holds across reconnects and across host generations.

### 4.4 Answers

Unknown `ask_id` → `unknown_ask`. Already answered with another `client_id` → ack with
`answered_elsewhere:true, by_device`. Otherwise `ask.answered` is journaled and synced, then
`Adapter.Answer`: success → `ask.delivered{via:tool}`; `ErrAskGone` (or any error) → the answer is
sent as a user message: `message{user, pending, answer_to, text: agent.AnswerText(…)}`,
`ask.delivered{via:message}`, `notice{answer_as_message}`, then `Adapter.Send`. An open ask never
expires; it survives parks, host deaths and reboots, and an answer to it after any of those arrives
as a message.

## 5. Adapters (WP3–WP5)

`agent.Adapter` (see `internal/agent/adapter.go`, which also has the per-CLI command table):
`Start(ctx, StartSpec, Emitter)`, `Send(ctx, Outgoing)`, `Interrupt`, `Answer(askID, answer)`,
`SetModel(model, effort)`, `Command(name, args)`, `Park`, `Stop`, `Background() int`, `PID() int`.
Register with `agent.Register(harness, factory)`; the server offers a chat for exactly the
registered harnesses.

Rules every adapter keeps:

1. Start its CLI with `StartSpec.Spawn(cmd)`, never `cmd.Start()`: Spawn forks on the host's
   locked main thread with `Setpgid` and `Pdeathsig=SIGKILL`.
2. Use `StartSpec.Env` as the CLI's whole environment (WP7 builds it per account; empty today means
   the host's scrubbed environment — the host has already removed `INVOCATION_ID`, `NOTIFY_SOCKET`,
   `WATCHDOG_*`, `JOURNAL_STREAM`, `LISTEN_*`).
3. Emit `conversation` as soon as the CLI says its id; resume `StartSpec.ResumeID` when set and fail
   `Start` when the CLI does not know it.
4. Move each user message through `message.state` `queued`/`delivered` (Claude: `--replay-user-
   messages` echo = delivered; Codex: `turn/started` or the steer; OpenCode: the user message
   event). A message left `pending` or `queued` when a host dies is handed to the next one.
5. For every turn: `turn.started` (with the user `message_id`), an assistant `message`, parts, and
   `turn.ended`. End every part with a full `part` event. Tool output streams as `part.delta`.
6. Report `ErrAskGone` from `Answer` when the blocking call is not there; report CLI death through
   `Emitter.Exited` (not for exits Park/Stop caused).
7. `Background()` counts what parking would kill (Claude `task_*`, Codex background terminals).

| | Claude Code (WP3) | Codex (WP4) | OpenCode (WP5) |
|---|---|---|---|
| text / reasoning | `stream_event` `text_delta` / `thinking_delta` → `part.delta`; `assistant` block → `part` done | `item/agentMessage/delta`, `item/reasoning/*Delta` → `part.delta`; `item/completed` → `part` | `message.part.delta` → `part.delta`; `message.part.updated` → `part` |
| tool | `tool_use` → `part{tool, running}`; `tool_result` + `tool_use_result` → `part{done}` | `commandExecution` / `fileChange` / `mcpToolCall` items; `outputDelta` → `part.delta` | `tool` part state pending→running→completed\|error |
| diff / plan / todo | Edit `structuredPatch` → `diff`; `ExitPlanMode` → `plan`; Task* → `todo` | `turn/diff/updated` → `diff`; `plan` item | `edit` metadata diff; `todo.updated` |
| turn end | `result` (`is_error`, `terminal_reason`) | `turn/completed` status | `session.idle` / `session.error` |
| limits | `rate_limit_event` → `rate_limit` | `account/rateLimits/updated` | assistant `info.error` / headers |
| usage | `result.usage`, `total_cost_usd` delta | `thread/tokenUsage/updated` | `step-finish` tokens/cost |

## 6. Lifecycle

**Launch** (server, `agenthost.Launcher.Launch`): writes `host.json`, then starts
`<socrates> agent-host --data <data> --chat <id>` with stdout/stderr on `host.log`, `Setsid`, and
the environment minus `NOTIFY_SOCKET`/`INVOCATION_ID`/`WATCHDOG_*`. Under systemd
(`ServiceScope()`: `INVOCATION_ID` set and parent PID 1, as termux decides for tmux) it is
`systemd-run --scope --quiet --collect --unit=socrates-agents-<id> --description=… -p
OOMPolicy=continue`, with termux's fallbacks (no OOMPolicy if refused; `-<unix>` suffix if the name
is refused). The launcher returns once the socket accepts, or fails with the tail of `host.log`.

**Host start:** take `host.lock` (wait ≤ 1 s, else exit 0: another host runs) → read the spec →
open the journal → **recover**: a turn still open in the journal is closed with
`turn.ended{interrupted}` + `notice{resumed}` → listen → boot (holding the command lock, so nothing
new reaches the agent first): `process{starting}`, `Adapter.Start` with `ResumeID =` the
journal's conversation id (`First` = the first undelivered message when there is no conversation
yet), `process{ready}`, re-send undelivered messages, deliver unsent answers as messages. A start
that fails journals `process{failed, detail}` and the host exits 2 s later.

**Idle and parking:** idle = adapter up, no turn, no open ask, `Background() == 0`. After
`park_after_ms` (setting `chats.park_after_minutes`, default 20) of idle with no event or command,
or as soon as idle after a `park` command: `Adapter.Park` (graceful), `process{parked}`, then the
host closes its socket, lets connected clients read to the end (≤ 2 s) and exits. The next command
launches a new host, which resumes the conversation: resume after idle, after an update and after a
reboot are one code path.

**CLI exits on its own** (`Emitter.Exited`): `turn.ended{failed}` if a turn ran,
`process{failed}`, host exits. The next command resumes.

**Dead host** (server): dial fails **and** `host.lock` is free (the kernel releases it however the
host died — no PID guessing) → `Launcher.Reap`: stop the chat's scope(s), which ends tool processes
that escaped the parent-death signal, and remove the stale socket → if the projection had a turn
running, `NoteResume` on the row → launch. `Reap` refuses (`ErrAlive`) while the lock is held:
the scope of a live host is never stopped. A host that holds the lock but does not answer is
retried with backoff (200 ms → 5 s), never replaced.

**Server side** (`chatHub`, one `chatPeer` per chat):

- At start, every chat's journal file is read (feed + projection), its activity reported, and a
  pump started for every chat whose host is alive or that needs one (turn running, or outbox
  non-empty). The resume point is the last seq in the journal file it read.
- The pump connects (launching/recovering), resends the whole outbox in order, applies frames
  (events only forward: seq ≤ last is ignored, a gap is logged), and stops when the host parked or
  exited and nothing is pending — or after a host whose agent failed to start, so a broken CLI is
  retried once per user action, not in a loop.
- **Outbox** (`chat_outbox`, keyed by chat and `client_id`): written before a command is sent,
  deleted on ack (or a permanent `err`). Exactly-once = durable outbox + resend on every connect +
  host dedupe from the journal. The remaining window (host dies between journaling a message and
  writing it to the CLI) is closed by re-delivering every message still `pending` to the next
  host; for Claude, the replay echo is the receipt (WP3).
- Filing a chat as done sends `park`; deleting it sends `stop`, stops its scope, clears its outbox
  and removes `chats/<id>/`.

## 7. Browser API

All behind the login, same-origin checks as every route. `client_id` is the browser's idempotency
key (the existing `Outbox` in `net.js`).

| Route | Body | Answers |
|---|---|---|
| `POST /api/chats` | `client_id`, `harness`, `model`, `effort`, `workdir_mode`, `workdir`, `title`, `message`?{`client_id`,`text`,`attachments`} | 201 `{session, chat: <summary>, message?}`; 200 the same for a repeated `client_id`; 400 for a harness without an adapter. The host is launched at once |
| `GET /api/chats/{id}` | – | `{session, chat: <summary>, asks: [open asks], host_error?}`; `?history=1` adds `messages` (the whole projection) |
| `POST /api/chats/{id}/messages` | `client_id`, `text`, `attachments` | 200 `{client_id, seq, dup}`; 202 `{client_id, queued: true}` when the host has not acked within 10 s (it will receive it once) |
| `POST /api/chats/{id}/interrupt` | `client_id` | as messages; 200 `{idle: true}` when nothing runs and no host is up |
| `POST /api/chats/{id}/asks/{ask}/answer` | `client_id`, `answer` (any JSON), `device` | 200 / 202 as messages; **409 `{answered_elsewhere: true, by_device}`** when another device answered; 404 for an unknown ask |
| `GET /api/chats/{id}/stream` | WebSocket | below |

Sending a message clears the chat's unread mark (as typing at a terminal does). Chats appear in
`GET /api/sessions` and the event stream like every session, with `kind: "chat"` and their
`activity`.

**Stream** (`socrates.chat.v1`): the same per-login handshake ceiling as `/api/events`, origin
check, compression. Client's first frame within 10 s: `{"t":"hello","since":<seq>}` (else closed
1008). Server: `{"t":"hello","chat":<summary>,"last_seq","now"}`, then `ev` frames for every event
after `since`, then `{"t":"synced","seq"}` once the replay is through, then live `ev` and `delta`
frames (same shapes as §4.2). `{"t":"ping","id"}` → `{"t":"pong","id"}` after every event published
before it; `{"t":"tick","now"}` every 15 s; server pings, two misses close. Close **4401** when the
login is gone (checked every tick and on sign-out/password change), 1001 when the server stops.
Opening the stream of a chat whose host died with a turn running starts the recovery.

## 8. The fake adapter

`harness: "fake"`, registered only when `SOCRATES_FAKE_AGENT=1` is in the environment of the
server (and therefore its hosts). Its "CLI" is a real `sleep` child started through Spawn. It keeps
its conversations in `agent/fake/<id>.json` and resumes them; an unknown id fails like a real CLI.
Script, by message text:

| text | turn |
|---|---|
| `/stream N [ms]` | N deltas `wI ` every ms (default 20 × 10 ms) |
| `/tool` | a `tool` part, three streamed output lines, then text |
| `/ask` | an `ask`; blocks until answered (`you answered: <json>`) or interrupted |
| `/sleep S` | works for S seconds |
| `/bg S` | ends at once, leaving a background task for S seconds (`tasks`) |
| `/limit` | `rate_limit`, `error`, `turn.ended{limited}` |
| `/crash` | the CLI child is killed mid-turn |
| `/orphan` | starts a `setsid sleep 600` outside the process group (pid in `agent/fake/orphan.pid`) |
| anything else | `you said: <text> (turn N of <conversation>)` |

`Command("compact")` emits `notice{compacted}`; other commands are `unsupported`.

## 9. Tests

- `internal/agent`: journal reopen, torn/zero-filled/damaged tail, missing newline; coalescing per
  part (110 deltas → 2 lines, all 110 live); final state; projection folding incl. part
  replacement, overlapping/gapped deltas, first answer wins, interrupted turn; UTF-16 offsets; feed
  ordering.
- `internal/agenthost` (real host processes; Linux): dedupe; reconnect with `since` gets exactly the
  missing events (byte-identical to the journal); SIGKILL mid-turn → CLI child dead (Pdeathsig),
  recovery, resume, dedupe across hosts; park on idle → resume; no park with an open ask or a
  background task; **systemd scope** (root + systemd only): host in `socrates-test-agents-<id>.scope`
  with `OOMPolicy=continue`, `Reap` refuses a live host, an orphan keeps the scope after SIGKILL and
  `Reap` ends it.
- `internal/server` (real hosts; Linux): create idempotency, terminal routes refused, conversation
  id recorded, activity + unread; server restart mid-turn; server down 30 s while the turn ends;
  host SIGKILLed mid-turn; exactly-once across a server crash after sending; two devices on one
  stream; two devices answering; answer after the call is gone; park and resume; queued messages;
  hello and 4401.
- `e2e/chats.mjs`: the real binary, SIGKILL of the server mid-turn, replay, duplicates, two-device
  answers, the session list.

## 10. Known limits, for the packages that follow

- The server reads every chat's journal into memory at start and keeps it (feed + projection).
  Fine for tens of chats; WP9 may want a projection snapshot and lazy loading.
- `StartSpec.Env`/`Account` are passed through but nothing fills them until WP7.
- Titles, Home's question text, push wording and outcomes do not read the journal yet (WP9); a chat
  gets the generic "Finished"/"needs you" from activity alone.
- A Claude adapter must add the replay-echo receipt (§6) to close the last delivery window.
