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
| `<data>/chats/<id>/host.json` | server, before every launch | launch spec: harness, workdir, model, effort, account, env, `binary` (the harness's binary setting, `StartSpec.Binary`), `conversation` (the row's `cli_session_id`: resumed, and rebuilt into an empty journal, when the journal names none), `park_after_ms` |
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
| `message.state` | `message_id`, `state` | user: `pending` (journaled by the host) → `queued` (the agent holds it behind a running turn) → `delivered` (taken up), or `cancelled` (the agent dropped it unread; not handed to it again). Assistant: `streaming` → `done`\|`interrupted` |
| `part` | `message_id`, `part_id`, `parent_part_id`?, `kind`, `state`, `text`, `content` | creates **or replaces whole** the part with that id (kind, state, text, content); `parent_part_id` nests it (below) |
| `part.delta` | `message_id`, `part_id`, `offset`, `text` | appends `text` to the part's text; `offset` is where it starts, in **UTF-16 code units** |
| `ask` | `ask_id`, `turn_id`, `payload` (WP6's `ask_user` schema, opaque here), `via`? | a question the agent waits for. `via: "message"`: the call already returned by design (Codex code mode, REBUILD §5.3) and the answer is to be sent as the next user message (§4.4) |
| `ask.answered` | `ask_id`, `answer`, `by_device`, `client_id` | the first answer; later ones are refused (§4.4) |
| `ask.delivered` | `ask_id`, `via` (`tool`\|`message`) | how the answer reached the agent |
| `account.switched` | `from`, `to`, `reason` | divider (WP7) |
| `model.changed` | `model`, `effort` | for the turns that follow |
| `rate_limit` | `window`, `used` (0..1), `resets_at` (Unix s) | one usage window |
| `usage` | `usage.context`, `usage.window` | context in use, outside a turn's end |
| `process` | `state` (`starting`\|`ready`\|`parked`\|`exited`\|`failed`), `detail`, `pid`, `version` | host/CLI life. `starting` carries the host pid, `ready` the CLI pid |
| `conversation` | `conversation_id` | the CLI's own id, needed to resume |
| `tasks` | `count` | background tasks still running |
| `agent.info` | `content`: an `agent.AgentInfo` `{agent, version?, commands: [{name, args?, description?}]}` plus the adapter's own fields (Claude's in §5.1) | what the CLI says about itself; `commands` is **the** composer palette (`Projection.Palette()`), each name what `Adapter.Command` takes. The newest wins (`Projection.Agent`) |
| `command` | `op` (`interrupt`\|`set_model`\|`command`\|`park`\|`stop`), `client_id`, `model`/`effort`, `text` (command name), `args` | a command the host accepted; recorded so its `client_id` is acted on once |
| `notice` | `code`, `text`, `ask_id`? | a line of its own. Host codes: `resumed`, `answer_as_message`, `conversation_lost`; every adapter's rebuild: `rebuilt`, `history_unavailable` |
| `error` | `code`, `text`, `message_id`? | a failure that is not a turn's end (e.g. `send_failed`, `rate_limit`) |

**Nesting.** A part with `parent_part_id` belongs inside that part: the parts a subagent or a
workflow produced go inside the tool part that started it (Claude `Task`/`Agent` and
`parent_tool_use_id`, Codex sub-threads, OpenCode `task` child sessions, REBUILD §3.1/§7.1). Nesting
is by part, independent of the message; a nested part without `message_id` takes its parent's. The
projection puts it in the parent's `children` (in creation order, any depth) and **not** in the
message's `parts`. The parent must be emitted first; a part whose parent is unknown stays at the top
of its message, keeping `parent_part_id`. A replacing `part` event keeps the part where it is.

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
`answered_elsewhere:true, by_device`. Otherwise `ask.answered` is journaled and synced, then:

- an ask with `via: "message"` is not passed to `Adapter.Answer`: the answer goes as a user message
  (`message{user, pending, answer_to, text: agent.AnswerText(…)}`, `ask.delivered{via:message}`,
  then `Adapter.Send`) **without** a notice — this is the designed path;
- otherwise `Adapter.Answer`: success → `ask.delivered{via:tool}`; `ErrAskDeferred` → as the line
  above, without a notice (an adapter that only learns at answer time); `ErrAskGone` (or any other
  error) → the same message plus `notice{answer_as_message}`, because the call was lost. An open ask never
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
3. Emit `conversation` as soon as the CLI says its id; resume `StartSpec.ResumeID` when set, and
   when the CLI does not know it fail `Start` with an error that wraps
   `agent.ErrConversationNotFound` (the host then starts fresh).
4. Move each user message through `message.state` `queued`/`delivered` (Claude: `--replay-user-
   messages` echo = delivered; Codex: the `turn/start` answer, or for a steered message its
   `userMessage` item, §5.2; OpenCode: the user message
   event). A message left `pending` or `queued` when a host dies is handed to the next one.
5. For every turn: `turn.started` (with the user `message_id`), an assistant `message`, parts, and
   `turn.ended`. End every part with a full `part` event. Tool output streams as `part.delta`.
6. Emit `ask{via:"message"}` (or return `ErrAskDeferred` from `Answer`) for an ask whose answer is
   meant to start the next turn. Set `parent_part_id` on everything a subagent or workflow
   produces. Report `ErrAskGone` from `Answer` when the blocking call is not there; report CLI death through
   `Emitter.Exited` (not for exits Park/Stop caused).
7. `Background()` counts what parking would kill (Claude `task_*`, Codex background terminals).

| | Claude Code (WP3) | Codex (WP4) | OpenCode (WP5) |
|---|---|---|---|
| text / reasoning | `stream_event` `text_delta` / `thinking_delta` → `part.delta`; `assistant` block → `part` done | `item/agentMessage/delta`, `item/reasoning/*Delta` → `part.delta`; `item/completed` → `part` | `message.part.delta` → `part.delta`; `message.part.updated` → `part` |
| tool | `tool_use` → `part{tool, running}`; `tool_result` + `tool_use_result` → `part{done}` | `commandExecution` / `fileChange` / `mcpToolCall` items; `outputDelta` → `part.delta` | `tool` part state pending→running→completed\|error |
| diff / plan / todo | Edit `structuredPatch` → `diff`; `ExitPlanMode` → `plan`; Task* → `todo` | `turn/diff/updated` → `diff`; `plan` item | edit/apply_patch/write metadata → `diff`; `todowrite` → `todo` |
| turn end | `result` (`is_error`, `terminal_reason`) | `turn/completed` status | `session.idle` (outcome from `session.error` / `info.error` / the limit) |
| limits | `rate_limit_event` → `rate_limit` | `account/rateLimits/updated` | `session.status retry` with `action.reason`, or a 429 error |
| usage | `result.usage`, `total_cost_usd` delta | `thread/tokenUsage/updated` | `step-finish` → `usage`; assistant `info.tokens`/`cost` summed per turn |

**Rebuild** (REBUILD §2.2). `Start` with `StartSpec.Rebuild` resumes `ResumeID` and, before
returning, emits the conversation's history read from the CLI's own transcript — user messages,
turns (every one closed), assistant messages and their final parts — then `notice{rebuilt}`. A
history that cannot be read is `notice{history_unavailable}`, and the start goes on; an unknown
conversation is `ErrConversationNotFound` as for any resume.

**Command palette.** Every adapter emits `agent.info` at start with `commands`: what its CLI reported
(custom commands and skills included) and the commands the adapter itself implements.

### 5.1 Claude Code (`internal/agent/claude`)

One `claude -p` per chat, protocol facts in research/03 and research/09 (subagents and workflows,
verified against 2.1.309). Fixed argv: `--input-format stream-json --output-format stream-json
--verbose --include-partial-messages --replay-user-messages --forward-subagent-text
--permission-prompt-tool stdio --setting-sources project,local --strict-mcp-config
--dangerously-skip-permissions --disallowedTools AskUserQuestion --settings
{skipDangerousModePermissionPrompt, disableRemoteControl, cleanupPeriodDays: 90, ultracode}`,
`--model`, `--effort`, and `--session-id <new uuid>` or `--resume <id>`.

**Environment.** `StartSpec.Env` minus every `CLAUDE*`/`ANTHROPIC*`/`IS_SANDBOX` variable except
`CLAUDE_CONFIG_DIR`; plus `IS_SANDBOX=1` as root and `CLAUDE_CODE_ENABLE_TODO_TOOLS=1`. Two
variables of the spec are instructions to the adapter, not passed on: `SOCRATES_CLAUDE_API_KEY`
(an API-key account; becomes `ANTHROPIC_API_KEY`) and `SOCRATES_CLAUDE_ULTRACODE=1` (the session
option). `claude.PrepareConfigDir(accountDir, <data>/agents/claude)` links `projects/`, `tasks/`,
`plans/`, `todos/` of an account's config dir into the shared folder (relative symlinks); WP7 calls
it before an account's first run. An account switch is `Interrupt` (when busy), `Park`, `Start`
with the other account's env and the same `ResumeID` (tested against fakeclaude).

**Start.** `--resume` of an id with no transcript under the config dir fails before spawning, with
`agent.ErrConversationNotFound`. `initialize` (with `sdkMcpServers` when `Options.Servers` gives
any: the WP6 hook) is the readiness signal; then `set_max_thinking_tokens{thinking_display:
"summarized"}`, without which thinking arrives with no text. The conversation id is emitted on the
first `system/init` (a new chat) or at start (a resume); `/clear` changes it, and the next init
says so.

**Delivery.** A message's CLI uuid is `uuidv5(message_id)`. `command_lifecycle started` or the
replay echo = `delivered`; a send during a turn = `queued`. On resume, a pending message whose
uuid the transcript already has is reported delivered and not sent again: the receipt that closes
the last delivery window (§6).

**Turns.** Every `system/init` without a running turn opens one, bound to the message the CLI
last said it started, or to none (a background task reporting back starts a turn of the CLI's
own: `turn.started` without `message_id`). One assistant message per turn. `result` ends it:
`completed`; `interrupted` for `terminal_reason aborted_*`; `limited` after a rejected
`rate_limit_event`, an assistant `error: rate_limit` or `api_error_status 429`; else `failed`.
`cost_usd` = this result's `total_cost_usd` minus the previous one of the process (it is
cumulative per process). `usage.context` = the last request's prompt + answer, `window` =
`modelUsage[*].contextWindow`.

**Parts and content** (all `content` JSON):

| part | id | kind | content |
|---|---|---|---|
| text / thinking block | `t_<api msg>_<index>` / `r_…` (streamed); `t_<frame uuid>_<i>` (subagent) | `text` / `reasoning` | reasoning: `{duration_ms, redacted?}` |
| tool call | `tool_use` id | `tool`; `diff` for Edit/Write/MultiEdit; `plan` for ExitPlanMode | `{tool, tool_use_id, input, summary, command?, path?, diff?, added?, removed?, is_error?, duration_ms, result?, task?, agent?, workflow?, plan?}`; the output is the part's `text` |
| subagent (Task/Agent) | its `tool_use` id | `tool`, stays `running` until `task_notification` | `agent: {id, type, description, prompt, model, status: running\|completed\|failed\|killed, background, tokens, tool_uses, duration_ms, last_tool, activity, summary}`; its blocks are parts with `parent_part_id` = this part |
| workflow (Workflow) | its `tool_use` id | `tool`, `running` until `task_notification` | `workflow: {name, description, task_id, run_id, status, phases: [{index, title}], agents, agents_done, tokens, duration_ms, summary}` |
| workflow agent | `<workflow part>_a<index>` | `tool` (`tool: "WorkflowAgent"`), nested in the workflow | `agent: {…, label, phase}`; its calls (read from `<transcriptDir>/agent-<id>.jsonl`) are nested in it |
| background shell | the Bash call | `tool` | `task: {id, status}` |
| task list | `todo_<assistant message>` | `todo` | `{items: [{id, subject, description, active_form, status}]}`, the whole list; TaskCreate/TaskUpdate make no rows of their own |
| image from a tool | `<call>_img<n>`, nested in the call | `image` | `{mime, path, size}`; the file is under `chats/<id>/agent/images/` |
| local command output, API error text | `n_<uuid>` | `notice` | `{code: command_output\|<error>\|claude}` |

**Plan card.** In plan mode (`Command("plan")`, `Command("plan", "off")`), `ExitPlanMode` arrives
as `can_use_tool`. The adapter emits the plan part and `ask{ask_id: "plan_<tool_use_id>", payload:
{kind: "plan", title, plan, plan_file, part_id, options: ["approve", "keep_planning"]}}` and holds
the request. The answer `{"decision":"approve"}` allows it; `{"decision":"keep_planning","note":
"…"}` denies it with the note as the model's message (`plan.decision`, `plan.note` on the part). An
interrupt withdraws the request (`control_cancel_request`); a later answer is `ErrAskGone`. Every
other `can_use_tool` is allowed at once.

**Events of its own.** `notice` codes `compacted` (`compact_boundary`, a divider with the token
counts), `api_retry`, `informational` (warnings such as "can't use thinking from another
organization"); `error` codes `rate_limit`, `message_dropped` (the CLI cancelled a message it never
started), `claude` (a result error outside a turn). `Command(name, args)` sends `/name args` as user
text, except the terminal-only commands (`doctor`, `color`, `focus`, `heapdump` and the init's
`terminal_slash_commands`): `ErrUnknownCommand`. `SetModel` is `set_model` plus
`apply_flag_settings{effortLevel}`.

**agent.info content:** `{agent: "claude", version, account: {email, organization, plan,
api_provider, token_source}, commands: [{name, args, description}], models: [{id, label,
description, efforts}], tools, permission_mode}` (from `initialize`, kept current from every init).

**Rebuild** (`Start` with `Rebuild`, via `Adapter.Rebuild` / `claude.RebuildTranscript`): the main transcript in file order (a
compaction restarts the parentUuid chain); each user text opens a turn; `<command-name>` wrappers
become the command as the user's text; `<task-notification>` opens a turn of the CLI's own;
`<local-command-stdout>` becomes a notice part; the compaction summary is dropped and its boundary
becomes `notice{compacted}`; the synthetic "No response requested." and "[Request interrupted…"
mark the turn interrupted, and "[Tool call interrupted…" results mark the call interrupted; a
subagent's calls come from `subagents/agent-<id>.jsonl`, a workflow's agents from its run journal.

**fakeclaude** (`e2e/fakebin/fakeclaude`): the stand-in for tests and e2e; its script is in its
package comment.

### 5.2 Codex (`internal/agent/codex`)

**Process.** `codex app-server --listen stdio:// -c check_for_update_on_startup=false
-c projects={"<resolved workdir>"={trust_level="trusted"}}`, started through Spawn with
`StartSpec.Env` as its whole environment. An empty `Env` is refused (exec would otherwise pass
the host's own); until WP7 the host fills it with its scrubbed environment, so codex uses
`~/.codex`. What WP7 puts in it per account: `PATH` (and `SOCRATES_CODEX_BIN` for a binary
that is not the first `codex` on it), `HOME`, `LANG`/`LC_*` and `TZ` as the host has them,
`CODEX_HOME` = the account's home prepared with `codex.PrepareHome` (its `auth.json` is the
login; an API-key account is logged in there with `account/login/start {type: apiKey}`, so no
`OPENAI_API_KEY` is needed), and nothing of the service's (`INVOCATION_ID`, `NOTIFY_SOCKET`,
other accounts' keys). `initialize` with
`capabilities.experimentalApi`, then `thread/resume {excludeTurns: true}` or `thread/start`, both with
`approvalPolicy: "never"`, `sandbox: "danger-full-access"`, `cwd`; `thread/start` also carries
`model`, `config.model_reasoning_effort`, the registered dynamic tools and (handoff only)
`developerInstructions`. Park closes
stdin (the app-server exits at once; the thread is on disk); Stop kills the process group.

**Messages.** No turn running: `turn/start` with `clientUserMessageId = message_id`, `model` and
`effort` on every turn (and `collaborationMode: plan` while `/plan` is on) → `delivered` and
`turn.started{message_id}` when codex answers. A turn running: `turn/steer` with the same id →
`queued`; codex takes it up at its next step (0.164 cuts a streaming answer short for it) and the
`userMessage` item carrying that id → `delivered`. A turn that cannot be steered (compaction,
review: `activeTurnNotSteerable`) or a `!` line during a turn → held (`queued`) and sent when the turn
ends; so is a steered message a turn ended without taking up. A message whose text is one line
starting with `!` runs `thread/shellCommand` as a turn of its own.

**Turns.** A journal turn is a root turn of the chat's thread (ids are the adapter's own, `t_…`, so
that a recovery can continue the same journal turn under another codex turn). Turns codex starts
for a command (compact, review, shell) have no `message_id`. `turn/completed` status → outcome:
`completed`, `interrupted`, `failed` (`usageLimitExceeded` → `limited`, plus `error{rate_limit}`); an
`error` notification with `willRetry` → `notice{retrying}`, without it the turn ends failed if
`turn/completed` does not follow within 30 s. `turn.ended.usage` sums the turn's model calls
(`tokenUsage.last`; codex counts cached input inside the input, the journal apart: `input` =
input − cached, `cache_read` = cached), `context`/`window` from the last call;
`usage{context, window}` follows each call. `account/rateLimits/updated` → one `rate_limit` per
window that changed: `five_hour` (300 min), `weekly` (10080 min), else `<n>m`; `used` = percent/100.
Cost is not reported by codex (0).

**Parts** (part ids `<message>_p<n>`; text is the streamed text):

| item | kind | state | content |
|---|---|---|---|
| `agentMessage` | `text` | streaming → done | `{phase, delivery, questions}` when any is set (`delivery: "async"` = a code-mode question, WP6) |
| `reasoning` | `reasoning` | streaming → done | – (text = summary, then raw content) |
| `plan` | `plan` | streaming → done | – (text = the plan's markdown) |
| `commandExecution` | `tool` | running → done \| error \| interrupted | `{tool: "command", command, cwd, source, actions, status, exit_code, duration_ms, process_id}`; text = output |
| `fileChange` | `diff` | running → done \| error | `{tool: "file_change", status, changes: [{path, kind: add\|delete\|update, move_path, diff}]}` |
| `turn/diff/updated` | `diff` | running → done (at most 1/s while it changes) | `{tool: "turn_diff", diff}`: the turn's whole unified diff |
| `turn/plan/updated` | `todo` | running | `{explanation, steps: [{step, status}]}` |
| `mcpToolCall` | `tool` | running → … | `{tool: "mcp", server, name, arguments, result, error, status, duration_ms}` |
| `dynamicToolCall` | `tool` | running → … | `{tool: "dynamic", namespace, name, arguments, content_items, success, status, duration_ms}` |
| `collabAgentToolCall` | `tool` | running → … | `{tool: "collab", name, prompt, sender, receivers, model, effort, agents, status}` |
| `subAgentActivity` started | `tool` (group) | running while the subagent works, done when idle/completed, interrupted | `{tool: "subagent", thread_id, path, name, model, effort, status}`; every item of that thread (and of its own subagents) is a part with `parent_part_id` = the group |
| `imageView` / `imageGeneration` | `image` | done | `{path}` / `{item}` |
| `enteredReviewMode` / `exitedReviewMode` | `notice` / `text` | done | `{notice: "review_started"}` / `{review: true}` (text = the review) |
| `contextCompaction` | `notice` | streaming → done | `{notice: "compacted"}` |
| `webSearch`, anything else | `tool` | … | `{tool: <item type>, item: <the raw item>}` |

A subagent's parts outlive the turn that spawned it; `tasks{count}` counts subagents at work, and
`Background()` adds codex's background terminals (`thread/backgroundTerminals/list`).

**Server requests** are answered on goroutines of their own, so a question never holds up the
stream: command, file-change and legacy approvals → accept; `item/permissions/requestApproval` →
the requested permissions, for the session; an MCP elicitation that is a tool-call approval →
accept, any other → decline + `notice{elicitation_declined}`; `item/tool/requestUserInput` →
JSON-RPC error + `notice{question_declined}`; `item/tool/call` for a tool nobody registered →
`success: false`; `currentTime/read` → now; anything else → error + `notice{codex_request}`.
**WP6 hooks:** `codex.RegisterTool(Tool{Name, Spec, Handle})` offers a dynamic tool on every new
thread (codex keeps it with the thread) and routes its `item/tool/call` to `Handle`;
`codex.HandleRequest(method, handler)` replaces a built-in answer (`item/tool/requestUserInput`,
`mcpServer/elicitation/request`). A handler gets a `*codex.Call` with `Ask(ctx, payload)` (emits
`ask`, blocks until `Answer`; `ErrAskGone` when the turn ends, codex withdraws the request
(`serverRequest/resolved`) or the app-server goes), `Defer(payload)` (emits `ask{via:"message"}`,
for code mode) and `Notice`.

**Commands** (`codex.Commands`): `compact` → `thread/compact/start`; `review [base <branch> | commit
<sha> | instructions]` → `review/start`; `plan [off]` → Plan collaboration mode for the turns that
follow (`notice{plan_mode}`); `shell <command>` → `thread/shellCommand`.

**Lost journal** (`Start` with `Rebuild`, §5): the history is written back first from `thread/turns/list` + `thread/items/list` (paginated): per
turn the user messages (`delivered`, the codex `clientId` as `message_id`), `turn.started`, the
assistant message with its items as parts, `turn.ended` (a turn codex died in is `interrupted`),
then `notice{rebuilt}`. Subagent threads are not replayed. `agent.info` carries `codex.Commands`.

**Accounts** (mechanics; WP7 drives them). `codex.PrepareHome(home, <data>/agents/codex)` makes an
account's `CODEX_HOME` with `sessions/` and `archived_sessions/` as relative symlinks into the
shared folder (an account's own sessions are moved in first); a switch is a relaunch with the
other account's environment on the same thread. When a turn fails with `invalid_encrypted_content`
(another account's encrypted reasoning, REBUILD §4.3) the same journal turn goes on: (1)
`codex.StripRollout` copies the rollout under a new thread id (UUIDv7) without `reasoning` and
`compaction` items (also inside a compaction's replacement history) and ending before the refused
turn (codex records a turn's user message before it calls the model), the copy is resumed,
`conversation{new id}` + `notice{codex_reasoning_dropped}`, and the turn's messages are sent again;
(2) if that is refused too, a new thread whose developer instructions carry the visible transcript
(≤ 24 000 bytes, newest kept, cut on a whole character), `conversation` + `notice{codex_handoff}`, messages again; (3) a
third refusal in the same process fails the turn. The original rollout is never rewritten.

**Pin.** `testdata/schema` is the generated schema of codex-cli 0.164.0; `TestPinnedSchema` checks
every method, field, item type and error code the adapter uses against it, and
`TestInstalledCodexSchema` (`SOCRATES_REAL_CODEX=1`) against the installed codex
(`SOCRATES_UPDATE_CODEX_SCHEMA=1` rewrites the pin).

### 5.3 OpenCode (`internal/agent/opencode`)

Registered as `opencode` (imported by `main.go`). Research: research/05
(API), research/06 (layout, spike S3).

**Process.** One `opencode serve --hostname 127.0.0.1 --port 0` per host, started through Spawn in
the workdir; the URL is read from its `listening on` line (`--port 0` tries 4096 first). The binary
is `SOCRATES_OPENCODE_BIN` from `StartSpec.Env`, else `opencode` on that environment's `PATH`. Its
environment is `StartSpec.Env` without `OPENCODE_*`/`XDG_*` (except `OPENCODE_AUTH_CONTENT`), plus
the layout, `OPENCODE_SERVER_USERNAME/PASSWORD` (32 random bytes per process),
`OPENCODE_CONFIG_CONTENT` (share disabled, autoupdate off, `permission`: `external_directory`,
`doom_loop`, `read` allow and `question` deny over OpenCode's own `"*": allow` — no `"*"` of ours,
which would also lift plan mode's edit deny — plus the `mcp` servers of the `MCPServers` hook),
`OPENCODE_DISABLE_AUTOUPDATE`, `OPENCODE_DISABLE_MODELS_FETCH`, `OPENCODE_DISABLE_EXTERNAL_SKILLS`.
Park and Stop: abort if a turn runs, SIGTERM to the process group, SIGKILL after 5 s.

**Layout** (`opencode.Layout`, `NewLayout(dataDir)`): `<data>/agents/opencode/opencode.db`
(`OPENCODE_DB` of every process), shared `snapshot/ storage/ tool-output/ plans/ worktree/ repos/
config/ cache/`, and per account `accounts/<acct>/` as `XDG_DATA_HOME` (its `opencode/auth.json`,
the shared folders linked in by relative symlinks) with `state/` as `XDG_STATE_HOME`.
`PrepareAccount(acct)` makes it (idempotent; every start calls it, WP7 calls it on creating an
account). `Migrate` runs `opencode db "select 1"` once per binary build (stamp `migrated`, under
`migrate.lock`) before any serve. `StartSpec.Account` empty is `default`.

**Session.** New: `POST /session`. Resume: `GET /session/{id}`; 404 (or a malformed id) wraps
`ErrConversationNotFound`. On resume, an assistant message that never completed (a dead process)
has its streaming/running parts written as `interrupted`. `Rebuild` reads the whole history
from `GET /session/{id}/message` (and the task children's).

**Turns.** A message is posted with `prompt_async` and a message id of OpenCode's own form chosen by
the adapter (`msg_` + 48-bit ms×4096+counter + 14 base-62), kept in `agent/opencode.json` (journal
id → OpenCode id) so that revert/fork can name it and a message the previous host already handed
over is recognised (`delivered` without a second turn). `delivered` + `turn.started` when the post
is accepted; one turn per user message; every step is an assistant `message`. **While a turn runs,
messages wait in the adapter (`queued`) and go one by one after `session.idle`** (DEVIATIONS).
A `!line` is `POST /shell`. Turn outcome: `limited` (a `retry` status with `action.reason`, which is
then aborted, or a 429), else `interrupted` (`MessageAbortedError`), else `failed` (any other error;
`error{code: auth}` for 401/ProviderAuthError with "sign in again"), else `completed`. Other
retries: `notice{retry}` per attempt. `permission.asked` → replied `always` + `notice{permission_allowed}`.
`question.asked` → the `QuestionHandler` hook (WP6), else rejected + `notice{question_rejected}`.
`Answer` returns `ErrAskGone` until WP6. After `/event` drops, it is reopened and caught up from
`GET …/message` and `/session/status`.

**Parts and content** (exported types in `convert.go`):

| OpenCode part | kind | text | content |
|---|---|---|---|
| `text`, `reasoning` (not synthetic) | `text`, `reasoning` | the text (deltas) | – |
| `tool` | `tool` | output (bash streams `metadata.output`), or the error | `ToolContent{tool, call_id, title, input, error, started_at, ended_at, duration_ms, exit, session_id, agent}` |
| `tool` todowrite | `todo` | – | `TodoContent{todos:[{content, status, priority}]}` |
| completed edit / apply_patch / write of a new file | `diff`, id `<tool part>-diff` | – | `DiffContent{tool_part, files:[{path, patch, additions, deletions, status}]}` |
| `file` | `image` / `file` | – | `FileContent{mime, filename, url (file:// only)}` |
| a task child session's parts | as above | | with `parent_part_id` = the task part |

**Commands** (`Command(name, args)`): `compact` (`/summarize`, a turn), `shell <cmd>`, `agent
build|plan` (for every following prompt, kept across hosts; `notice{agent}`), `revert <message>`
(`notice{reverted, message_id}`), `unrevert` (`notice{unreverted}`), `fork [<message>]`
(`notice{forked, conversation_id}` — a new chat made with it as its `cli_session_id` resumes it,
its history rebuilt), and any name from `GET /command` (a turn whose user message is `/name args`). `agent.info`
(`{agent, version, commands}`) lists `compact`, `shell`, `agent` and OpenCode's commands (`hints`
as `args`) at start. `SetModel`: `provider/model`
and the effort as `variant`, both sent with every prompt.

**Account switch** (mechanics for WP7): `(*opencode.Adapter).SwitchAccount(ctx, account, env)`
interrupts a running turn and waits for it, stops the process, and starts one for the other
account's data home and `env` on the same session; the caller journals `account.switched`. API-key
accounts may pass `OPENCODE_AUTH_CONTENT` in `env`; OAuth accounts keep `auth.json` in their data
home (research/06 §3.2).

## 6. Lifecycle

**Launch** (server, `agenthost.Launcher.Launch`): writes `host.json`, then starts
`<socrates> agent-host --data <data> --chat <id>` with stdout/stderr on `host.log`, `Setsid`, and
the environment minus `NOTIFY_SOCKET`/`INVOCATION_ID`/`WATCHDOG_*`. Under systemd
(`ServiceScope()`: `INVOCATION_ID` set and parent PID 1, as termux decides for tmux) it is
`systemd-run --scope --quiet --collect --unit=socrates-agents-<id> --description=… -p
OOMPolicy=continue`, with termux's fallbacks (no OOMPolicy if refused; `-<unix>` suffix if the name
is refused). `Reap`/`Stop` stop exactly `<unit>.scope` and `<unit>-<digits>.scope`, never a
prefix match that could be another chat's. The launcher returns once the socket accepts, or fails with the tail of `host.log`.

**Host start:** take `host.lock` (wait ≤ 1 s, else exit 0: another host runs) → read the spec →
open the journal → **recover**: every part
still `running`/`streaming` (a turn's, or a subagent or workflow group that outlived its turn) gets
a `part{interrupted}`, a turn still open in the journal is closed with `turn.ended{interrupted}`,
and `notice{resumed}` says so → listen → boot (holding the command lock, so nothing
new reaches the agent first): `process{starting}`, `Adapter.Start` with `ResumeID =` the
journal's conversation id — or, when the journal names none, the spec's `conversation` (the journal
was lost, or the chat was made from an existing conversation such as a fork), with
`StartSpec.Rebuild` set when the journal has no message at all (§5) — (`First` = the first
undelivered message when there is no conversation yet), `StartSpec.DataDir` = the data folder, `process{ready}`, re-send undelivered messages, deliver unsent answers as messages. A start
that fails journals `process{failed, detail}` and the host exits 2 s later. A start that fails
with an error wrapping `agent.ErrConversationNotFound` (the CLI does not know the conversation to
resume) is retried once as a new conversation (no rebuild) with `notice{conversation_lost}`: the history stays in
the journal, the model's context starts over.

**Idle and parking:** idle = adapter up, no turn, no open ask, `Background() == 0`, and no user
message the agent has not taken up (one acknowledged a moment ago is still on its way in). After
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
  (events only forward: seq ≤ last is ignored, a gap is logged), and reconnects whenever the host
  is still alive, because a host can start a turn or report a task on its own. It stops when the
  host is gone and nothing is pending — or after a host whose agent failed to start, so a broken CLI
  is retried once per user action, not in a loop. A host is launched only for work (a turn the
  journal says runs, or the outbox) or for a new chat's warm start, never just because the pump
  runs.
- **Outbox** (`chat_outbox`, keyed by chat and `client_id`): written before a command is sent,
  deleted on ack or on an `err`, except a `send` or `answer` refused with `not_running`/`stopping`,
  which waits for the next host (an interrupt or a model change refused that way is dropped). Exactly-once = durable outbox + resend on every connect +
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
| `PUT /api/sessions/{id}/workdir` | `workdir` (a folder that exists) | 200 `{session}`; 400 for a folder that is not there or not allowed; 409 while a turn runs. A host that is up is sent `park`, not `stop`: a turn that started a moment ago, which the server has not heard of yet, finishes in the old folder, and the next message starts the agent in the new one (REBUILD §6.2: a restored chat whose folder is missing) |

Sending a message clears the chat's unread mark (as typing at a terminal does). Chats appear in
`GET /api/sessions` and the event stream like every session, with `kind: "chat"` and their
`activity`, and `workdir_missing` when their folder is not on this machine.

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
its conversations in `agent/fake/<id>.json` (with each turn's text, its "transcript") and resumes
them; an unknown id fails like a real CLI, and `Rebuild` writes the turns back. Its `agent.info`
palette is `compact`. Script, by message text:

| text | turn |
|---|---|
| `/stream N [ms]` | N deltas `wI ` every ms (default 20 × 10 ms) |
| `/tool` | a `tool` part, three streamed output lines, then text |
| `/ask` | an `ask`; blocks until answered (`you answered: <json>`) or interrupted |
| `/asknext` | an `ask{via:"message"}`, and the turn ends; the answer starts the next turn |
| `/subagent` | a `Task` tool part with a text and a tool part nested inside it |
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
- `internal/agent/codex` against `e2e/fakebin/fakecodex` (built by the tests; a scripted app-server
  that keeps real-format rollouts, see its package comment): every mapping, steering and holding,
  shell lines, interrupt, limit, crash, answered requests, the dynamic-tool ask hook, park, resume,
  rebuild, the account-switch chain, and the adapter inside a real host process (SIGKILL takes
  codex along, the next host resumes). With `SOCRATES_REAL_CODEX=1` the same scenarios run against
  the installed codex with `internal/agent/codex/fakemodel` as its model provider (a scripted
  Responses API whose encrypted reasoning is bound to an account; `e2e/fakebin/fakemodel` serves
  it on a port).

## 10. Known limits, for the packages that follow

- The server reads every chat's journal into memory at start and keeps it (feed + projection).
  Fine for tens of chats; WP9 may want a projection snapshot and lazy loading.
- `StartSpec.Env`/`Account` are passed through but nothing fills them until WP7.
- Titles, Home's question text, push wording and outcomes do not read the journal yet (WP9); a chat
  gets the generic "Finished"/"needs you" from activity alone.
- The Claude adapter's replay receipt is in (§5.1). Ultracode and an API-key account reach it
  only through `StartSpec.Env` (`SOCRATES_CLAUDE_ULTRACODE`, `SOCRATES_CLAUDE_API_KEY`); the server
  sets neither yet (WP7, WP9).
