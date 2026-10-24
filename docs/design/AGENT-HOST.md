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
   │  OpenCode only: its MCP server `socrates mcp-ask` (ask_user, §5.3), which reaches the host
   ▼  back over the socket
socrates mcp-ask --socket <the host's socket>
```

A chat is a **session row** with `kind = 'chat'` (schema 10). It shares the sessions table with
terminals, so the list, shelves, unread mark, ordering, push and Done sweep apply unchanged. The
CLI's conversation id is stored in `cli_session_id` (`cli_session_state = known`). Terminal loops
(adoption, lifecycle poll, the detector tick, digests, titles, the operator's routes) skip chat
rows; the terminal-only routes (`…/ws`, `…/journal`, `…/resume`, `…/restart`) answer 409.

| Path | Written by | What |
|---|---|---|
| `<data>/chats/<id>/events.jsonl` | host | the journal (§3); the chat history Socrates renders and backs up |
| `<data>/chats/<id>/host.json` | server, before every launch | launch spec: harness, workdir, model, effort, `account` (its id), `env` (the account's environment without any key, §11), `secret_env` (the account's key variables, sealed for this chat, §11), `switch_reason` (why the account differs from the journal's, when a switch was asked for while no host ran), `binary` (the harness's binary setting, `StartSpec.Binary`), `conversation` (the row's `cli_session_id`: resumed, and rebuilt into an empty journal, when the journal names none), `ultracode` (the chat's option; the host puts `SOCRATES_CLAUDE_ULTRACODE=1` into the CLI's environment exactly when it is set, on top of the account's environment, at the start and at every switch, §5.1), `park_after_ms` |
| `<data>/chats/<id>/summary.json` | server | the chat's brief (§6): its `Summary`, Home's line and question, the words of its notification, and the seq they are true at; believed only while the journal still ends at that seq; not backed up |
| `<data>/chats/<id>/host.lock` | host | `flock`ed for the host's whole life: **the** evidence a host is alive |
| `<data>/chats/<id>/host.log` | host (and `systemd-run`) | the host's stdout/stderr; a file, never a pipe |
| `<data>/chats/<id>/agent/` | adapter | the adapter's own state (the fake keeps its conversations here) |
| `<data>/secrets.key` | server, once (0600) | the key every API key and every chat's `secret_env` is sealed with (§11) |
| `<data>/accounts/<acct>/` | server (folder), the agent's own sign-in (login) | a Claude Code or Codex account's `CLAUDE_CONFIG_DIR` / `CODEX_HOME`; an OpenCode account's is `<data>/agents/opencode/accounts/<acct>/` (§5.3, §11) |
| `<data>/run/chats/<id>.sock` | host | the socket; if that path is ≥ 104 bytes, `$TMPDIR/socrates-<uid>/<sha256(data)[:6]>/<id>.sock` (both sides compute it) |

Chat ids are `[A-Za-z0-9_-]{1,64}` (the server uses 32 hex characters); they become file, socket
and unit names.

## 2. Event model

Every journal line, every `ev` frame on the host socket and every `ev` frame on the browser stream
is one `agent.Event`: a flat JSON object, all fields optional except `seq`, `at` (Unix ms) and
`type`. `seq` and `at` are stamped by the journal; adapters never set them.

| type | fields | meaning |
|---|---|---|
| `turn.started` | `turn_id`, `message_id`?, `account`? | the agent took up a user message (or started on its own). `account` is the account the turn runs on (WP7): the host stamps it on every turn, so a turn's cost belongs to the account that paid for it (the cost ledger, WP9). A journal written before accounts has none |
| `turn.ended` | `turn_id`, `outcome` (`completed`\|`interrupted`\|`failed`\|`limited`), `usage`, `cost_usd`, `text` (why) | end of the turn |
| `message` | `message_id`, `role` (`user`\|`assistant`), `state`, `client_id`?, `text`?, `attachments`?, `answer_to`?, `turn_id`?, `model`? | creates a message. **User messages carry text and attachments inline**; assistant messages are containers for parts |
| `message.state` | `message_id`, `state` | user: `pending` (journaled by the host) → `queued` (the agent holds it behind a running turn) → `delivered` (taken up), or `cancelled` (the agent dropped it unread; not handed to it again). Assistant: `streaming` → `done`\|`interrupted` |
| `part` | `message_id`, `part_id`, `parent_part_id`?, `kind`, `state`, `text`, `content` | creates **or replaces whole** the part with that id (kind, state, text, content); `parent_part_id` nests it (below) |
| `part.delta` | `message_id`, `part_id`, `offset`, `text` | appends `text` to the part's text; `offset` is where it starts, in **UTF-16 code units** |
| `ask` | `ask_id`, `turn_id`, `payload` (an `ask_user` payload, below, or Claude's plan card, §5.1), `via`? | a question the agent waits for. `via: "message"`: the call already returned by design (Codex code mode, REBUILD §5.3) and the answer is to be sent as the next user message (§4.4) |
| `ask.answered` | `ask_id`, `answer`, `by_device`, `client_id` | the first answer; later ones are refused (§4.4) |
| `ask.delivered` | `ask_id`, `via` (`tool`\|`message`) | how the answer reached the agent |
| `ask.closed` | `ask_id` | nobody waits for the answer any more: the person sent a message after the call was gone (§4.4). It is no longer open, and can still be answered (as a message) |
| `account.switched` | `from`, `to` (account ids), `reason` | the chat runs on another account from here on (WP7, §11): a line between the turns. `from` is empty for the first account a journal names |
| `model.changed` | `model`, `effort` | for the turns that follow |
| `rate_limit` | `window`, `used` (0..1), `resets_at` (Unix s) | one usage window |
| `usage` | `usage.context`, `usage.window` | context in use, outside a turn's end |
| `process` | `state` (`starting`\|`ready`\|`parked`\|`exited`\|`failed`), `detail`, `pid`, `version` | host/CLI life. `starting` carries the host pid, `ready` the CLI pid |
| `conversation` | `conversation_id` | the CLI's own id, needed to resume |
| `tasks` | `count` | background tasks still running |
| `agent.info` | `content`: an `agent.AgentInfo` `{agent, version?, commands: [{name, args?, description?}]}` plus the adapter's own fields (Claude's in §5.1) | what the CLI says about itself; `commands` is **the** composer palette (`Projection.Palette()`), each name what `Adapter.Command` takes. The newest wins (`Projection.Agent`) |
| `command` | `op` (`interrupt`\|`set_model`\|`command`\|`park`\|`stop`\|`switch_account`), `client_id`, `model`/`effort`, `text` (command name), `args` (a slash command's arguments; a switch's `when`), `to`/`reason` (a switch's account and why) | a command the host accepted; recorded so its `client_id` is acted on once. An `interrupt`, a `stop` or a `switch_account` that does not wait for the turn (`args` other than `after_turn`) marks the running turn as stopped by somebody (`Turn.Stopped`: not pushed, not judged) |
| `notice` | `code`, `text`, `ask_id`? | a line of its own. Host codes: `resumed`, `answer_as_message`, `conversation_lost`; every adapter's rebuild: `rebuilt`, `history_unavailable` |
| `error` | `code`, `text`, `message_id`? | a failure that is not a turn's end (e.g. `send_failed`, `rate_limit`, `switch_failed`: the other account's agent did not start, and the chat is back on its account) |

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
Each token is counted once, the same way for every agent: `input` is the input neither read from
the cache nor written to it, `output` counts the reasoning in, and `reasoning` says how much of it
was reasoning.
Attachment: `name`, `path` (absolute), `mime`, `size`.

**Asks** (WP6, `internal/agent/askuser`). Every agent asks with Socrates' own tool, `ask_user`
(REBUILD §5): one definition (`askuser.Schema()`, what the model sees; `askuser.Parse`, which
checks a call and sends a bad one back to the model with everything that is wrong with it), the
same in all three agents. The `ask` payload is the call normalised (`askuser.Payload`):
`{title?, context?, questions: [{id, header (≤ 24, filled from the question when missing),
question, context?, kind: single|multi|text|confirm|number, options?: [{id, label, description?,
details?, recommended?}], min?, max?, allow_other, allow_delegate, default?, required, secret?}],
part_id?, source?}`. Defaults are applied (`allow_other`, `allow_delegate`, `required`: true), ids
filled in (`q<n>` by place, never an id the model gave another question; an option's from its
label), the kind inferred from the options, a `default` that fits nothing dropped. `part_id` is the
tool part the call belongs to, when the adapter knows it. `source` is set when the questions came
from one of the agent's own mechanisms, converted into the same shape: `request_user_input` (Codex
Plan mode), `async_question` (Codex code mode), `elicitation` (an MCP elicitation form: one
question per field, no delegating; a form without fields is one `confirm`), `question` (OpenCode's
own tool). A converted payload keeps every question and option its mechanism sent: the limits of
§5.1 (20 questions, 12 options) bind the model's calls only. The `answer` of `ask.answered` is what
the browser sends; the shape is REBUILD §5.2, `{answers: {<id>: {selected, other, note, delegated,
skipped, value?}}, general_note, follow_up}` (`askuser.ReadAnswer` normalises it: every question
gets an entry, selections name only the question's options, a confirm is `selected: ["yes"|"no"]`
with `value: true|false`, a number its `value`; a bare JSON string is taken as the general note).
The model gets that JSON as the tool's result, followed by a line for a follow-up, delegated or
skipped questions (`askuser.ToolResult`); a converted question's answer goes back in its
mechanism's own shape. An elicitation form has no place for the person's own words: when the
answer has a note, a general note, a question back, a choice in their own words or a delegation
(`askuser.SaysMore`), the form gets the values it takes and `Answer` reports `ErrAskDeferred`, so
that the whole answer also goes as a message (§4.4). An answer that arrives as a message is
`askuser.MessageText`: the questions and the answers in words, then the same JSON.

**Waiting calls** (`agent.Waits`). The Claude, Codex and fake adapters keep the calls that wait on
the person in one `agent.Waits`: `Wait` emits the call's `ask` and blocks until the answer or the
end of its context (the CLI withdrew the call, or exited); `Answer` hands the answer over. The
waiting call alone decides: `Answer` reports success only when the call took the answer while its
context still ran, otherwise `ErrAskGone`, so an answer is never journaled as delivered through
the tool and then dropped. `Withdraw` marks calls gone before their context ends (an interrupt the
CLI acknowledged); a call withdrawn before it waited asks nothing.

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

**Asks** in the projection: `OpenAsks()` are the asks neither answered nor closed. `Unwaited()` are
the open asks no call waits on: the asks of a turn that has ended (an interrupt, a host that died)
and those with `via: "message"`; an ask without a `turn_id` is never among them, because a call
may still wait on it.

**Summary / activity** (`Projection.Summary()`): `activity` is `waiting` while an open ask belongs
to the running turn (or has no turn), or while any ask is open and no turn runs; else `busy` while
a turn runs (an open ask of an earlier turn does not hide it), else `tasks` while background tasks
run, else `idle`. `open_asks` counts every open ask.
The server reports it to the terminal detector with `termux.ReportActivity` (committed exactly, no
debounce; note `question` for waiting; `asked` = the chat has a user message). The unread mark, row
ordering (`MovesRow`), refiling into in-progress, the `activity` frames and push are the terminals'.
Answering a chat's question and stopping its turn mark it read, as a message does (typing at a
terminal does the same), so the turn an answer lets go on ends unread again.

**What the rest of Socrates reads** (WP9, `internal/server/chatfeed.go`, `chatbrief.go`): none of it
asks a model to read a screen; it is the projection.

- **Home's line** (`chatLine`): while the chat waits, the first open question's text; while a turn
  runs, the newest running top-level tool part as a person says it (`toolLine`: "Running go test",
  "Editing main.go", "Subagent: …", "Workflow: …", from Claude's `summary`/`command`/`path`, Codex's
  `command`/`changes`, OpenCode's `title`/`input`), else the first line of the newest text part,
  else the person's message; between turns the first line of the last turn's answer, "Failed: …",
  "Limit reached: …" or "Stopped". Markdown is taken off; a subagent's own words never are the line.
- **The reading**: every chat is a `digest` on the event stream, the hello and `GET /api/home`,
  beside the terminals': `phase: ready`, `state` = the activity, `summary` = the line, `question` =
  the open ask the chat waits on as a digest question (`source: "ask"`, `kind` single→`choice`,
  multi, confirm, text/number→`text`, plan; one item per question with its options), and `news`
  (below). It is published when it changes - before the activity report, so a page told that a
  chat waits has its question - and a line whose words alone changed at most once a second.
- **Badge and push**: the asking set counts a chat whose brief says `waiting` (`askable` rows, the
  rule Home's Questions card uses). A question is pushed the moment the chat starts asking, with the
  first question's words; a finished turn with how it ended and a line of its answer ("Finished ·
  …", "Failed · …", "Limit reached · …", "Stopped · …" for a turn a dead host left), and not at all
  for a turn the person stopped (`Turn.Stopped`, an `interrupt`/`stop` command journaled while it
  ran). That sentence is the brief's `news` and the reading's `news` field, which the page's own
  notification shows too.
- **Titles**: at the first turn somebody asked for, the title runner names the chat from its first
  message and what its first answer has said so far; "name again" uses the first message and the
  latest exchange (never the composer's draft).
- **Spoken Status**: `POST /api/sessions/{id}/status` tells the status model the chat from its
  journal (`chatNow`): the last turn - message, answer, calls and how they went, task list - what
  runs now, the question it waits on with its choices, and its background tasks.
- **Outcome judge** (`chatjudge.go`): with outcomes on, a chat's turn end puts the outcome on
  pending exactly as a terminal's does - except a turn that is no news (stopped by the person),
  which gets no verdict - and `judgeChat` asks the home model in a call of its own
  (its system prompt opens with `You judge the turn a coding agent has just ended`) with the turn as
  `chatTurnPrompt` writes it; Read again judges it again.
- **Costs** (`chatcosts.go`): the journal is the one source of a chat's cost - Claude's `cost_usd`,
  Codex's tokens priced by the ledger's Codex rates, OpenCode's own price - in a ledger named by the
  chat and the account: `chat-<id>@<account>`, the account a turn's `turn.started` names (WP7), so
  a chat that never switched is on its account from its first turn; the account's id is the
  ledger's profile, which the costs page names after the account. A turn of a journal from before
  accounts names none: it is on the account the first `account.switched` came from, and on
  `chat-<id>` until there is one. Each pass that reads a journal
  writes its ledgers and removes the ones of that journal it no longer makes. A journal rebuilt
  after its folder was lost (`notice{rebuilt|history_unavailable}`) does not count the history it
  got back, which was counted when it ran, and leaves the lost journal's ledgers as they are; what it
  adds goes into `chat-<id>~<the notice's at>[@<account>]`. Every conversation id the journal
  names, and the row's, is the chat's for good (kv `costs.chat_conversations`): the transcript
  scanner skips it, and a ledger it made of one before is removed. The agents' shared folders
  (`agents/claude|codex|opencode`) are a root of the scanner like the profiles'.

## 4. Host protocol (version 2)

Unix socket, mode 0600, one JSON object per line in each direction, lines ≤ 16 MiB.

### 4.1 Handshake

Client's first line, within 10 s: `{"op":"hello","proto":2,"since":<last seq it has>}`.
Host: `{"t":"hello","proto":2,"version","chat","harness","host_pid","cli_pid","last_seq","process"}`
(`proto` = min of both), or `{"t":"err","code":"protocol",…}` and close when the client's `proto` is
below the host's minimum. The server accepts hosts speaking `MinProtocol..Protocol` (currently
1..2); when the protocol changes, keep the old version readable for a day (a busy host of the
previous build lives on, REBUILD §2.1). Version 2 (WP6) added the `ask` op of a local tool and its
`answer` frame; the server's own ops are those of version 1, so a server and a host of either
version understand each other. A connection asks for no events with `since` = `NoEvents`
(`1<<62`, beyond any journal), which is what a tool's connection does; it gets no deltas either.

### 4.2 Host → client

- `{"t":"ev", …event}` – every journaled event with seq > `since`, then every new one. Replay and
  live tail are **one pull cursor** over the host's in-memory journal: no gap, no duplicate, and a
  slow client only slows itself (write deadline 30 s, then dropped; reconnect with `since`).
- `{"t":"delta","message_id","part_id","offset","text"}` – live, never journaled, never ahead of
  the event that created its part; dropped for a client more than 4096 behind.
- `{"t":"ack","client_id","op","seq","dup","answered_elsewhere","by_device","ask_id"}` – the
  command's events are journaled **and synced**. `seq` is the first event it produced; a repeat
  gets the same seq with `dup:true`. An ack is written after every event before it. `ask_id` is
  the id an `ask` op's question was journaled under.
- `{"t":"answer","ask_id","answer"}` (version 2) – the answer to the connection's own `ask`, sent
  on that connection only, possibly before the ask's ack.
- `{"t":"err","client_id","op","code","msg"}` – codes: `bad_request`, `protocol`, `not_running`,
  `unknown_ask`, `unsupported`, `failed`, `stopping`. The server keeps `not_running`/`stopping`
  commands in its outbox (they go to the next host) and drops the rest.

### 4.3 Client → host

| op | fields | host does |
|---|---|---|
| `send` | `client_id` (required), `text`, `attachments` | journals `message{user, pending}` and, in the same batch, `ask.closed` for every `Unwaited()` ask (§4.4), hands it to the adapter, acks with the message's seq |
| `interrupt` | `client_id`? | journals `command`, `Adapter.Interrupt` |
| `answer` | `client_id`, `ask_id`, `answer` (JSON), `device` | §4.4 |
| `set_model` | `client_id`?, `model`, `effort` | journals `command`, `Adapter.SetModel` (which emits `model.changed`) |
| `command` | `client_id`?, `name`, `args` | journals `command`, `Adapter.Command` (`compact`, or a slash command); unknown → `unsupported` |
| `park` | `client_id`? | parks as soon as idle (§6) |
| `stop` | `client_id`? | stops the CLI now, records an interrupted turn, exits |
| `ask` (version 2) | `payload` (an `ask_user` payload) | a local tool's question (`socrates mcp-ask`, §5.3): journals `ask{turn_id: the running turn}` and acks with its `ask_id`; the answer comes back on this connection as an `answer` frame (§4.4). Refused (`bad_request`) on a connection that said hello with protocol 1, without a payload, or while no turn runs (the CLI calls tools only inside a turn, so such a question comes from a call already withdrawn), `not_running` without an agent |

| `switch_account` | `client_id`, `account`, `env` (required), `secret_env`, `reason`?, `when` (`now`\|`after_turn`), `continue`? | WP7, §11: journals `command{to, reason}`; `after_turn` while a turn runs (or a message waits that a limit does not hold) keeps it until the turn ends and acks it `pending` at once, then once more without `pending` when it is made or a newer switch replaces it; otherwise interrupts a running turn and waits for its end (30 s), moves the agent to the account, journals `account.switched` and acks. Without `env`: `bad_request` |

**switch_account.** The account's whole environment comes with the command: `env` without any
key, and the key variables in `secret_env`, sealed for this chat (§11), which the host opens with
the data folder's key and joins only in memory. A command without `env` is refused: the
environment the host runs on holds the current account's key. A switch kept for after the turn is
acked `{"pending": true}`; the server answers the request but keeps the command in its outbox, so
a host that dies before the turn ends is followed by one launched on that account (§11, the
newest `switch_account` in the outbox goes into `host.json`), and the repeat the server sends on
every connect is acked `dup` and `pending` while the host still holds it. Messages a limit holds
(§5.2, §5.3) do not count as a turn to wait for. The move is `AccountSwitcher.SwitchAccount` where
the adapter has one (OpenCode, §5.3), else `Park` and `Start` of a new adapter with the
environment and the same `ResumeID` (`agent.ErrConversationNotFound` → a fresh conversation with
`notice{conversation_lost}`). The divider is journaled before the new agent starts, so the events
of the next turn come after it; a start that fails journals `error{switch_failed}`, a divider back
and restarts the old account. Messages the agent had not taken up are handed to the new one. With
`continue`, the new account is then sent `agenthost.ContinueText` ("Continue where you left off.")
as a user message with `client_id` `<the switch's client_id>:continue`: what "Continue with" and the
automatic switch at a limit send. A switch to the account the chat is on journals nothing, and
continues only when the last turn ended `limited` or `interrupted` and the agent has taken nothing
up since: a second "Continue with", one from another device or an automatic switch on top of one
by hand does not make the agent continue twice. A host that boots with a spec whose `account` differs from the journal's last
(the switch was asked for while no host ran) journals `account.switched{reason: switch_reason}` first.

Commands are processed one at a time per host, in arrival order. **Idempotency:** a `client_id`
the projection has already seen is acked `dup` and not acted on. The set is rebuilt from the
journal on every start, so it holds across reconnects and across host generations.

### 4.4 Answers

Unknown `ask_id` → `unknown_ask`. Already answered with another `client_id` → ack with
`answered_elsewhere:true, by_device`. Otherwise `ask.answered` is journaled and synced, then:

- an ask with `via: "message"` is not passed to `Adapter.Answer`: the answer goes as a user message
  (`message{user, pending, answer_to, text: agent.AnswerText(…)}`, `ask.delivered{via:message}`,
  then `Adapter.Send`) **without** a notice — this is the designed path;
- an ask a local tool's connection waits on (`ask` op) gets the answer as an `answer` frame on that
  connection, and `ask.delivered{via:tool}` once it was written — but only while that connection
  is open **and** the ask's turn still runs: a turn that ended (an interrupt) withdrew the call
  even if the tool has not let go of the connection yet. Otherwise it takes the next line's path,
  where the adapter does not know it: `ErrAskGone`;
- otherwise `Adapter.Answer`: success → `ask.delivered{via:tool}`; `ErrAskDeferred` → as the line
  above, without a notice (an adapter that only learns at answer time); `ErrAskGone` (or any other
  error) → the same message plus `notice{answer_as_message}`, because the call was lost. An open ask never
expires; it survives parks, host deaths and reboots, and an answer to it after any of those arrives
as a message.

**Closing.** Interrupts and recovery leave asks open. A user message (`send`) closes every ask no
call waits on any more (`Unwaited()`, journaled as `ask.closed` with the message): the person moved
on, having pressed Stop and written something else, or written instead of answering a question
meant for the next message. A message sent while the call still waits closes nothing (it queues
behind the turn). A closed ask is no longer open — not `waiting`, not on the badge or Home — but it
takes an answer as before (§4.4: as a message, with the notice where the call was lost); nothing
the person typed is refused.

## 5. Adapters (WP3–WP5)

`agent.Adapter` (see `internal/agent/adapter.go`, which also has the per-CLI command table):
`Start(ctx, StartSpec, Emitter)`, `Send(ctx, Outgoing)`, `Interrupt`, `Answer(askID, answer)`,
`SetModel(model, effort)`, `Command(name, args)`, `Park`, `Stop`, `Background() int`, `PID() int`.
Register with `agent.Register(harness, factory)`; the server offers a chat for exactly the
registered harnesses.

Rules every adapter keeps:

1. Start its CLI with `StartSpec.Spawn(cmd)`, never `cmd.Start()`: Spawn forks on the host's
   locked main thread with `Setpgid` and `Pdeathsig=SIGKILL`.
2. Use `StartSpec.Env` as the CLI's whole environment (the server builds it per account, §11; empty
   means the host's scrubbed environment — the host has already removed `INVOCATION_ID`, `NOTIFY_SOCKET`,
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
6. Offer `ask_user` (§2, Asks) and emit `ask{turn_id}` for each call, with the payload the call
   normalised to; emit `ask{via:"message"}` (or return `ErrAskDeferred` from `Answer`) for an ask
   whose answer is meant to start the next turn. Set `parent_part_id` on everything a subagent or
   workflow produces. Report `ErrAskGone` from `Answer` when the blocking call is not there — and
   once the CLI has withdrawn it (an interrupt, a timeout, its exit), even if a goroutine of the
   adapter still holds it (`agent.Waits` does this); a carrier of MCP calls ends a call's context
   when `notifications/cancelled` names it, in the order it reads them, so that a cancel right
   behind its call is never missed. Report CLI death through `Emitter.Exited` (not for exits
   Park/Stop caused).
7. `Background()` counts what parking would kill (Claude `task_*`, Codex background terminals).

| | Claude Code (WP3) | Codex (WP4) | OpenCode (WP5) |
|---|---|---|---|
| text / reasoning | `stream_event` `text_delta` / `thinking_delta` → `part.delta`; `assistant` block → `part` done | `item/agentMessage/delta`, `item/reasoning/*Delta` → `part.delta`; `item/completed` → `part` | `message.part.delta` → `part.delta`; `message.part.updated` → `part` |
| tool | `tool_use` → `part{tool, running}`; `tool_result` + `tool_use_result` → `part{done}` | `commandExecution` / `fileChange` / `mcpToolCall` items; `outputDelta` → `part.delta` | `tool` part state pending→running→completed\|error |
| diff / plan / todo | Edit `structuredPatch` → `diff`; `ExitPlanMode` → `plan`; Task* → `todo` | `turn/diff/updated` → `diff`; `plan` item | edit/apply_patch/write metadata → `diff`; `todowrite` → `todo` |
| turn end | `result` (`is_error`, `terminal_reason`) | `turn/completed` status | `session.idle` (outcome from `session.error` / `info.error` / the limit) |
| limits | `rate_limit_event` → `rate_limit` | `account/rateLimits/updated` | `session.status retry` with `action.reason`, or a 429 error |
| usage | `result.usage`, `total_cost_usd` delta | `thread/tokenUsage/updated` | `step-finish` → `usage`; assistant `info.tokens`/`cost` summed per turn (`output` = OpenCode's output + reasoning, which it counts apart) |
| `ask_user` (WP6) | in-process MCP server `socrates` over the control protocol (`mcp__socrates__ask_user`), blocking | dynamic tool `ask_user`, blocking; code-mode models: returns at once, `via:"message"` | local MCP server `socrates mcp-ask` (`socrates_ask_user`), waiting in the host (§4.4), with progress |
| guidance | `--append-system-prompt` | the thread's developer instructions | the MCP server's `instructions` (OpenCode puts them into the system prompt) |
| own questions | `AskUserQuestion` disallowed; MCP elicitations asked as forms | `request_user_input`, async questions, MCP elicitation forms asked; tool-call approvals accepted | `question` denied; one that arrives is asked |

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
`--append-system-prompt <the ask_user guidance, then Options.AppendSystemPrompt>`, `--model`,
`--effort`, and `--session-id <new uuid>` or `--resume <id>`.

**Environment.** `StartSpec.Env` minus every `CLAUDE*`/`ANTHROPIC*`/`IS_SANDBOX` variable except
`CLAUDE_CONFIG_DIR`; plus `IS_SANDBOX=1` as root and `CLAUDE_CODE_ENABLE_TODO_TOOLS=1`. Two
variables of the spec are instructions to the adapter, not passed on: `SOCRATES_CLAUDE_API_KEY`
(an API-key account; becomes `ANTHROPIC_API_KEY`) and `SOCRATES_CLAUDE_ULTRACODE=1` (the session
option, which the host sets from `host.json`'s `ultracode` and takes away otherwise,
`agenthost.UltracodeEnv`). `claude.PrepareConfigDir(accountDir, <data>/agents/claude)` links `projects/`, `tasks/`,
`plans/`, `todos/` of an account's config dir into the shared folder (relative symlinks); WP7 calls
it before an account's first run. An account switch is `Interrupt` (when busy), `Park`, `Start`
with the other account's env and the same `ResumeID` (tested against fakeclaude).

**Start.** `--resume` of an id with no transcript under the config dir fails before spawning, with
`agent.ErrConversationNotFound`. `initialize` (with `sdkMcpServers: ["socrates", …]`: ask_user,
below, and whatever `Options.Servers` adds) is the readiness signal; then `set_max_thinking_tokens{thinking_display:
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

**ask_user** (`askuser.go`). Every chat has the in-process MCP server `socrates`
(`sdkMcpServerConfigs.socrates = {timeout: 2147483647, disableAutoBackground: true}`: an SDK
server has no idle timeout, and the wall clock is set as high as the CLI takes, almost 25 days);
the model knows the tool as `mcp__socrates__ask_user` (deferred: it loads it with ToolSearch, which
the guidance mentions). The CLI does the MCP handshake over `mcp_message` control requests right
after `initialize`; a `tools/call` is answered by `askuser.MCP` on a goroutine of its own: a call
`askuser.Parse` refuses goes back to the model as a tool error, saying what to fix; a good one is
`ask{ask_id: "ask_<tool_use_id>", turn_id, payload: {…, part_id: <tool_use_id>}}` and blocks until
`Answer`, and the model gets REBUILD §5.2 (`askuser.ToolResult`). Every handler of the CLI's
requests runs under a context that ends when the CLI withdraws the request
(`control_cancel_request`) and when the CLI exits (WP3 review); MCP notifications, among them
`notifications/cancelled` (an interrupt, a timeout), are handled in order with the stream: the
adapter ends the context of the call they name (by server and JSON-RPC id, registered before the
next frame is read), so a call the CLI withdrew is gone before the turn's end that follows is read,
even one withdrawn right behind its `tools/call`: an answer after it is `ErrAskGone` and goes as a
message. Once the CLI has acknowledged an `interrupt`, the main agent's ask_user calls of that turn
are withdrawn (`agent.Waits.Withdraw`) without waiting for their cancels; a subagent's call is left
to its own cancel, because a background subagent outlives the turn. An MCP elicitation
(`control_request{subtype: elicitation}`) is asked as a form (`source: elicitation`) and answered
`{action, content}`; one that is not a form, or cannot be read, is declined with
`notice{elicitation_declined}`.

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
`StartSpec.Env` as its whole environment. The binary is `StartSpec.Binary` (the dashboard's
setting: a path, or a name looked up on `Env`'s `PATH`), else `SOCRATES_CODEX_BIN` from `Env`,
else `codex` on `Env`'s `PATH` (`agent.ResolveBinary`). An empty `Env` is refused (exec would
otherwise pass the host's own); the server fills it per account (§11). What is in it: `PATH`, `HOME`, `LANG`/`LC_*` and `TZ` as the host has them,
`CODEX_HOME` = the account's home prepared with `codex.PrepareHome` (its `auth.json` is the
login; an API-key account is logged in there with `account/login/start {type: apiKey}`, so no
`OPENAI_API_KEY` is needed), and nothing of the service's (`INVOCATION_ID`, `NOTIFY_SOCKET`,
other accounts' keys). `initialize` with
`capabilities.experimentalApi`, then `thread/resume {excludeTurns: true}` or `thread/start`, both with
`approvalPolicy: {granular: {sandbox_approval: false, rules: false, skill_approval: false,
request_permissions: false, mcp_elicitations: true}}` (every permission category rejected, as
`"never"` does, but MCP elicitations let through to be asked: `"never"` declines them inside codex,
verified), `sandbox: "danger-full-access"`, `cwd`; `thread/start` also carries `model`,
`config.model_reasoning_effort`, the registered dynamic tools (ask_user) and
`developerInstructions` (the ask_user guidance, then a handoff's transcript). Park closes
stdin (the app-server exits at once; the thread is on disk); Stop kills the process group.

**Messages.** No turn running: `turn/start` with `clientUserMessageId = message_id`, `model` and
`effort` on every turn (and `collaborationMode: plan` while `/plan` is on) → `delivered` and
`turn.started{message_id}` when codex answers. A turn running: `turn/steer` with the same id →
`queued`; codex takes it up at its next step (0.164 cuts a streaming answer short for it) and the
`userMessage` item carrying that id → `delivered`. A turn that cannot be steered (compaction,
review: `activeTurnNotSteerable`) or a `!` line during a turn → held (`queued`) and sent when the turn
ends; so is a steered message a turn ended without taking up — except after a `limited` turn, when
the adapter **holds** its queue (as OpenCode's, §5.3) until the next `Send` lets it go (the person
asked for it); a switch starts another adapter, which the host hands what was never taken up. A message whose text is one line
starting with `!` runs `thread/shellCommand` as a turn of its own.

**Turns.** A journal turn is a root turn of the chat's thread (ids are the adapter's own, `t_…`, so
that a recovery can continue the same journal turn under another codex turn). Turns codex starts
for a command (compact, review, shell) have no `message_id`. `turn/completed` status → outcome:
`completed`, `interrupted`, `failed` (`usageLimitExceeded` → `limited`, plus `error{rate_limit}`); an
`error` notification with `willRetry` → `notice{retrying}`, without it the turn ends failed if
`turn/completed` does not follow within 30 s. `turn.ended.usage` sums the turn's model calls
(`tokenUsage.last`; codex counts the cached and the cache-written input inside the input, the
journal apart: `input` = input − cached − cache-written, `cache_read` = cached, `cache_write` =
cache-written; its output counts the reasoning in, as the journal's does), `context`/`window` from
the last call;
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
the requested permissions, for the session; an MCP elicitation that is a tool-call approval
(`_meta.codex_approval_kind`) → accept; `item/tool/call` for a tool nobody registered →
`success: false`; `currentTime/read` → now; anything else → error + `notice{codex_request}`.
**Hooks:** `codex.RegisterTool(Tool{Name, Spec, Handle})` offers a dynamic tool on every new
thread (codex keeps it with the thread) and routes its `item/tool/call` to `Handle`;
`codex.HandleRequest(method, handler)` replaces a built-in answer. A handler gets a `*codex.Call`
with `Model` (the thread's model, a subagent's own), `PartID` (the part of the item it is about),
`Ask(ctx, payload)` (emits `ask`, blocks until `Answer`, through `agent.Waits`; `ErrAskGone` when
the turn ends, codex withdraws the request (`serverRequest/resolved`) or the app-server goes —
`Answer` reports it gone as soon as the turn's end cancelled it, and as soon as codex has taken a
`turn/interrupt`, which cancels the turn's requests at once), `Defer(payload)` (emits
`ask{via:"message"}`) and `Notice`. An elicitation answer with words the form has no place for is
answered in the form's shape and reported `ErrAskDeferred` (§2, Asks).

**Questions** (`askuser.go`, registered by the package's `init`, so before any `Start`):

| what | asked as | the answer goes back as |
|---|---|---|
| dynamic tool `ask_user` (`item/tool/call`) | `ask{payload, part_id}` of the turn, blocking; a call `askuser.Parse` refuses → `success: false` with what to fix | `contentItems: [{inputText: askuser.ToolResult}]` |
| the same on a code-mode model (`gpt-6*`, whose tools run inside `exec` and would be polled every ~30 s) | `ask{via: "message"}`; the tool returns at once `"Questions delivered. End your turn now; the answers arrive as the next message."` | the next user message (§4.4) |
| `item/tool/requestUserInput` (Plan mode) | `source: request_user_input`, labels as option ids, `isSecret` → `secret` | `{answers: {<id>: {answers: [labels, own words, "Note: …"]}}}` |
| an agent message with `delivery: "async"` and `questions` (`request_user_input_async`) | `ask{via: "message", source: async_question}` once the item completes; the turn goes on | the next user message |
| `mcpServer/elicitation/request`, mode `form` | `source: elicitation`, one question per field | `{action: accept, content}`, or `decline` when nothing was answered; a URL or device verification → `decline` + `notice{elicitation_declined}` |

**Commands** (`codex.Commands`): `compact` → `thread/compact/start`; `review [base <branch> | commit
<sha> | instructions]` → `review/start`; `plan [off]` → Plan collaboration mode for the turns that
follow (`notice{plan_mode}`); `shell <command>` → `thread/shellCommand`.

**Lost journal** (`Start` with `Rebuild`, §5): the history is written back first from `thread/turns/list` + `thread/items/list` (paginated): per
turn the user messages (`delivered`, the codex `clientId` as `message_id`), `turn.started`, the
assistant message with its items as parts, `turn.ended` (a turn codex died in is `interrupted`),
then `notice{rebuilt}` (also for a thread with no turn yet). Subagent threads are not replayed. `agent.info` carries `codex.Commands`.

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
is `StartSpec.Binary` (a path, or a name looked up on `StartSpec.Env`'s `PATH`), else
`SOCRATES_OPENCODE_BIN` from `StartSpec.Env`, else `opencode` on that environment's `PATH`. Its
environment is `StartSpec.Env` without `OPENCODE_*`/`XDG_*` (except `OPENCODE_AUTH_CONTENT`), plus
the layout, `OPENCODE_SERVER_USERNAME/PASSWORD` (32 random bytes per process),
`OPENCODE_CONFIG_CONTENT` (share disabled, autoupdate off, `permission`: `external_directory`,
`doom_loop`, `read` allow and `question` deny over OpenCode's own `"*": allow` — no `"*"` of ours,
which would also lift plan mode's edit deny — plus `mcp`: ask_user's server, below, and whatever
the `MCPServers` hook adds),
`OPENCODE_DISABLE_AUTOUPDATE`, `OPENCODE_DISABLE_MODELS_FETCH`, `OPENCODE_DISABLE_EXTERNAL_SKILLS`.
Park and Stop: abort if a turn runs, SIGTERM to the process group, SIGKILL after 5 s.

**Layout** (`opencode.Layout`, `NewLayout(dataDir)`): `<data>/agents/opencode/opencode.db`
(`OPENCODE_DB` of every process), shared `snapshot/ storage/ tool-output/ plans/ worktree/ repos/
config/ cache/`, and per account `accounts/<acct>/` as `XDG_DATA_HOME` (its `opencode/auth.json`,
the shared folders linked in by relative symlinks) with `state/` as `XDG_STATE_HOME`. The tools a
chat runs inherit these; the backup leaves out `cache/`, and of an account's folder takes only
`opencode/` and `state/opencode/` (`paths.BackedUpOnly`).
`PrepareAccount(acct)` makes it (idempotent; every start calls it, WP7 calls it on creating an
account). `Migrate` runs `opencode db "select 1"` through Spawn once per binary build (stamp
`migrated`, under `migrate.lock`) before any serve. `StartSpec.Account` empty is `default`.

**Session.** New: `POST /session`. Resume: `GET /session/{id}`; 404 (or a malformed id) wraps
`ErrConversationNotFound`. On resume, an assistant message that never completed (a dead process)
has its running tool parts and itself written as `interrupted`, once (`agent/opencode.json` keeps
which); its text and reasoning stay as the journal streamed them (OpenCode stores a part's text only
when the part ends), and the host's recovery interrupts them. `Rebuild` reads the whole history
from `GET /session/{id}/message` (and the task children's: each task part gets what its subagent
did while it ran), and writes the `conversation` after it,
so that a host that dies while it reads leaves a journal the next host rebuilds again.

**Turns.** A message is posted with `prompt_async` and a message id of OpenCode's own form chosen by
the adapter (`msg_` + 48-bit ms×4096+counter + 14 base-62), kept in `agent/opencode.json` (journal
id → OpenCode id) so that revert/fork can name it and a message the previous host already handed
over is recognised (`delivered` without a second turn). `delivered` + `turn.started` when the post
is accepted; one turn per user message; every step is an assistant `message`. `turn.ended`'s usage
and cost add up the turn's steps and its subagents' (OpenCode keeps those with the child session),
live and in a rebuild. **While a turn runs,
messages wait in the adapter (`queued`) and go one by one after `session.idle`** (DEVIATIONS).
A `!line` is `POST /shell`. Turn outcome: `limited` (a `retry` status with `action.reason`, which is
then aborted, or a 429), else `interrupted` (`MessageAbortedError`), else `failed` (any other error;
`error{code: auth}` for 401/ProviderAuthError with "sign in again"), else `completed`. The
`session.error` OpenCode 1.19.4 repeats after a failed turn's `session.idle` (the rejected prompt,
with a stack trace) is dropped: the turn's error and end said it. Other
retries: `notice{retry}` per attempt. `permission.asked` → replied `always` + `notice{permission_allowed}`.
`question.asked` (OpenCode's own tool, denied, should one still arrive) → `ask{ask_id:
"ask_<que id>", source: question}`, answered by `POST /question/{id}/reply` with one list of labels
per question (`askuser.OpenCodeAnswers`); `question.replied`/`question.rejected`, a reply OpenCode
refuses and a process started since make it `ErrAskGone`; one it cannot read is rejected +
`notice{question_rejected}`. After `/event` drops, it is reopened and caught up from
`GET …/message` (the session's, and its subagents' since this process started or resumed them),
`GET /permission` (each still pending is answered `always`), `GET /question` (a question asked
meanwhile is asked then; each question is asked once, however often the event and the list bring
it) and `/session/status`. A catch-up never takes a part back from its end, and never replaces
streamed text with the shorter text OpenCode stored; a part always ends with the part whole,
never a delta.

**ask_user.** The config's `mcp.socrates = {type: "local", command: StartSpec.AskCommand, timeout:
86400000}`: the host's `<socrates> mcp-ask --socket <its socket>` (`agenthost.MCPAskMain`, the same
binary; empty outside a host, and then there is no such server). OpenCode starts it with the serve
and does the handshake (protocol 2025-11-25, verified against 1.19.4); the model knows the tool as
`socrates_ask_user`, and the server's `instructions` (the guidance) go into the system prompt.
`mcp-ask` serves `askuser.MCP` on stdio (`serverInfo.version` is the Socrates version): a bad call
goes back to the model; a good one opens a connection to the host (`hello{proto: 2, since:
NoEvents}`, then the `ask` op, §4.3) and waits for its `answer` frame, sending
`notifications/progress` every 30 s for a call with a progress token (each resets OpenCode's
timeout; none after the call's response); `notifications/cancelled` (an abort; each request's
context is registered by JSON-RPC id before the next line is read, so a cancel right behind its
call finds it) or the end of its stdin closes the connection, and the ask is answered as a message
from then on. The adapter itself never
sees an ask_user answer: the host routes it (§4.4).

**Parts and content** (exported types in `convert.go`):

| OpenCode part | kind | text | content |
|---|---|---|---|
| `text`, `reasoning` (not synthetic) | `text`, `reasoning` | the text (deltas) | – |
| `tool` | `tool` | output (bash streams `metadata.output`), or the error | `ToolContent{tool, call_id, title, input, error, started_at, ended_at, duration_ms, exit, session_id, agent}` |
| `tool` todowrite | `todo` | – | `TodoContent{todos:[{content, status, priority}]}` |
| completed edit / apply_patch / write of a new file | `diff`, id `<tool part>-diff` | – | `DiffContent{tool_part, files:[{path, patch, additions, deletions, status}]}` |
| `file` | `image` / `file` | – | `FileContent{mime, filename, url (file:// only)}` |
| a task child session's parts | as above | | with `parent_part_id` = the task part running it (the newest to name the child: a task with `task_id` resumes an earlier task's) |

**Commands** (`Command(name, args)`): `compact` (`/summarize`, a turn), `shell <cmd>`, `agent
build|plan` (for every following prompt, kept across hosts; `notice{agent}`), `revert <message>`
(`notice{reverted, message_id}`), `unrevert` (`notice{unreverted}`), `fork [<message>]`
(`notice{forked, conversation_id}` — a new chat made with it as its `cli_session_id` resumes it,
its history rebuilt), and any name from `GET /command` (a turn whose user message is `/name args`). `agent.info`
(`{agent, version, commands}`) lists `compact`, `shell`, `agent` and OpenCode's commands (`hints`
as `args`, `$ARGUMENTS` shown as `<arguments>` and `$1` as `<arg1>`; one named like a command the
adapter handles itself is left out) at start. `SetModel`: `provider/model`
and the effort as `variant`, both sent with every prompt.

**Account switch** (mechanics for WP7): `(*opencode.Adapter).SwitchAccount(ctx, account, env)`
interrupts a running turn and waits for it (30 s, then the stop ends it as `interrupted`), stops the
process, and starts one for the other account's data home and `env` on the same session; the queue
holds meanwhile, so queued messages and those sent during the switch run on the new account. When the
new process does not come up, the adapter reports `Exited` and refuses further sends (the host records
the failure; the next host resumes and re-sends what is undelivered). The caller journals
`account.switched`. API-key
accounts may pass `OPENCODE_AUTH_CONTENT` in `env`; OAuth accounts keep `auth.json` in their data
home (research/06 §3.2). After a `limited` turn the adapter **holds** its queue: the messages
queued behind it are not posted into the account at its limit, until `SwitchAccount` (they run on
the new account) or the next `Send` (the person asked for it) lets it go.

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
and `notice{resumed}` says so; open asks are left open (a question survives everything, REBUILD
§5.3: its answer, whenever it comes, reaches the agent as a message, §4.4) → listen → boot (holding the command lock, so nothing
new reaches the agent first): `process{starting}`, `Adapter.Start` with `ResumeID =` the
journal's conversation id — or, when the journal names none, the spec's `conversation` (the journal
was lost, or the chat was made from an existing conversation such as a fork), with
`StartSpec.Rebuild` set when the journal has no message at all (§5) — (`First` = the first
undelivered message when there is no conversation yet), `StartSpec.DataDir` = the data folder,
`StartSpec.AskCommand` = `<this binary> mcp-ask --socket <the host's socket>` (§5.3), `process{ready}`, re-send undelivered messages, deliver unsent answers as messages. A start
that fails journals `process{failed, detail}` and the host exits 2 s later. A start that fails
with an error wrapping `agent.ErrConversationNotFound` (the CLI does not know the conversation to
resume) is retried once as a new conversation (no rebuild) with `notice{conversation_lost}`: the history stays in
the journal, the model's context starts over.

**Idle and parking:** idle = adapter up, no turn, `Background() == 0`, and no user message the
agent has not taken up (one acknowledged a moment ago is still on its way in). A call that waits on
the person is always inside a running turn (or a background task, which `Background()` counts) —
a local tool's (`ask` op) too, whose call does not outlive its turn — so that is what keeps a host
while a question is being waited on. An open ask **outside** a running turn does not: its call is gone (the turn was
interrupted, or the host before this one died) or never waited (`via: "message"`), so its answer
goes to the agent as a message whenever it comes, through whichever host is up then (WP6; the WP2
review found that such an ask kept its host alive for ever). After
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

- At start, every chat is known by its **brief** (`summary.json`) when the journal still ends at
  the brief's seq (`agent.LastSeq`, read from the file's end), and its journal is read and folded
  only otherwise (and the brief written); its activity is reported, and a pump started for every
  chat whose host is alive or that needs one (turn running, or outbox non-empty). The resume point
  is the brief's seq. The history - feed and projection - is read when something needs it (a
  stream, a command, the pump, `GET /api/chats/{id}`, the namer, Status, the judge) and let go
  (brief kept) once nothing has used it for ten minutes; a closing server keeps the brief of every
  chat it holds. Measured on 300 chats of 200 turns: folding every journal took 4.2 s and 535 MB
  of heap; starting from the briefs 137 ms and 0.7 MB.
- A chat whose journal is empty while its row knows a conversation (its folder was lost, or a backup
  restored without it) gets a host when its stream is opened, which rebuilds the history from the
  CLI's transcript (`host.json` carries the conversation); not at start, so a restore of many such
  chats starts no agent for each. The turns it writes back are not news: the server tells the
  detector the chat's activity once the history is in (its `notice{rebuilt|history_unavailable}`,
  or the agent up without one), so they ring no chime and get no push, unread mark or verdict.
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
| `POST /api/chats` | `client_id`, `harness`, `model`, `effort`, `workdir_mode`, `workdir`, `title`, `ultracode` (Claude Code only; dropped for any other agent), `account`?, `auto_switch`?, `message`?{`client_id`,`text`,`attachments`} | 201 `{session, chat: <summary>, message?}`; 200 the same for a repeated `client_id`; 400 for a harness without an adapter, or an account of another agent. No `account`: the recommended one when the host launches (§11). The host is launched at once |
| `GET /api/chats/{id}` | – | `{session, chat: <summary>, asks: [open asks], host_error?}`; `?history=1` adds `messages` (the whole projection) |
| `POST /api/chats/{id}/messages` | `client_id`, `text`, `attachments` | 200 `{client_id, seq, dup}`; 202 `{client_id, queued: true}` when the host has not acked within 10 s (it will receive it once) |
| `POST /api/chats/{id}/interrupt` | `client_id` | as messages; 200 `{idle: true}` when nothing runs and no host is up |
| `POST /api/chats/{id}/asks/{ask}/answer` | `client_id`, `answer` (any JSON), `device` | 200 / 202 as messages; **409 `{answered_elsewhere: true, by_device}`** when another device answered; 404 for an unknown ask |
| `POST /api/chats/{id}/model` | `client_id`, `model`, `effort` | as messages (a `set_model` command) |
| `POST /api/chats/{id}/command` | `client_id`, `name`, `args` | as messages; 409 `{code: "unsupported"}` for a command the agent does not have |
| `POST /api/chats/{id}/account` | `client_id`, `account`, `when`? (`now`\|`after_turn`), `continue`? | as messages (a `switch_account` command, §4.3); 409 for an account of another agent or one not signed in, 404 for none |
| `PUT /api/chats/{id}/auto-switch` | `on` | `{session}`: the chat's switch at a limit (§11), kept in the row's options |
| `/api/accounts…` | §11 | the accounts |
| `POST /api/chats/{id}/attachments` | multipart, one or more `file` parts | `{files: [attachment]}`: stored under `chats/<id>/attachments/` (a copy in the working directory's uploads); a message may only name paths this route handed out, else 400 |
| `POST /api/chats/{id}/options` | `ultracode` | the row's options changed (400 for Ultracode on another agent), and the agent parked as soon as it is idle so the next message resumes with them; `{session}` |
| `POST /api/sessions/{id}/status`, `…/title`, `…/digest` | – | the spoken Status, naming again and Read again of every session; for a chat read from its journal (§3), Read again judging its last turn again |
| `GET`/`PUT /api/sessions/{id}/draft` | `text`, `base_rev` | the composer's draft, with PreStart's revision rule (409 `{conflict, text, rev}` when another device saved first); a prepared chat's draft is its first message |
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

**The chat view** (WP8, `internal/web/static/js/chat*.js`, `composer.js`, `askdialog.js`) folds
the stream with the projection's rules (`chatfold.js`) and sends everything through one `Outbox`
(`chatoutbox.js`, localStorage `socrates.outbox.chats`) with a fresh `client_id` per message,
command, model change and answer, strictly in order (a model chosen offline is the model of the
message written after it). Every tab of a browser shares that queue: each change is made to the
queue as storage holds it at that moment and other tabs take it in on `storage`, so two tabs may
both send an item, which its `client_id` makes one. Stop is the one action sent directly
(DEVIATIONS, WP8). What the Outbox holds is on screen until the journal has it: a message as "Not
sent yet" / "Sending", an answer on its question's card (and in the dialog) instead of a new
"Answer", a plan answer on the plan card with its buttons held; an item the server refused for
good says so with Try again (a command the journal has but the agent refused shows as "Not run"
on its line, to the browser that sent it, until dismissed). An answer to `ask_user` is REBUILD §5.2's object, every question
present: `selected` holds option ids (`"yes"`/`"no"` for a confirm question), `other` the person's
own words (a text question's answer, a number question's number as typed, or "Other"), `note`,
`delegated` ("You decide") and `skipped` (neither answered nor delegated); `general_note` and
`follow_up` (a counter-question instead of answers) are `null` when empty. A plan
(`payload.kind: "plan"`) is answered from its card, `{decision: "approve"}` or `{decision:
"keep_planning", note}`, never from the question dialog. The account picker (WP7,
`accountpicker.js`) plugs into the composer through `useAccountPicker(draw)` in `composer.js`:
the account the chat runs on (the journal's, else the row's), a switch on its way ("A → B"), and
a menu of the agent's accounts with their usage; during a turn it asks "Switch now" (interrupt and
continue) or "After this turn". The fold keeps `account` from `turn.started` and
`account.switched`, and the switch asked for (`command{switch_account}`) until it happens.

A new session is a chat for every agent with an adapter, from the sheet and from PreStart; the
server lists them as the catalogue's `chats`. `SOCRATES_AGENT_TERMINALS=1` keeps Claude Code,
Codex and OpenCode terminals there instead (the browser suite sets it while its agents are the
faketui stand-in; DEVIATIONS, WP8).

## 8. The fake adapter

`harness: "fake"`, registered only when `SOCRATES_FAKE_AGENT=1` is in the environment of the
server (and therefore its hosts). Its "CLI" is a real `sleep` child started through Spawn. It keeps
its conversations in `agent/fake/<id>.json` (with each turn's text, its "transcript") and resumes
them; an unknown id fails like a real CLI, and `Rebuild` writes the turns back. Its `agent.info`
is `{agent: "fake", version, commands: [compact, context], models: [fake-small, fake-large, each
with low/medium/high]}`, and the catalogue offers it in the new-session sheet with the same two
models. Its parts use Claude's content shapes (§5.1) and, for a change to several files, Codex's
(§5.2), so the chat view and its browser scenarios are measured on the real shapes. Script, by
message text:

| text | turn |
|---|---|
| `/stream N [ms]` | N deltas `wI ` every ms (default 20 × 10 ms) |
| `/tool` | a `tool` part, three streamed output lines, then text |
| `/ask` | an `ask`; blocks until answered (`you answered: <json>`) or interrupted |
| `/asknext` | an `ask{via:"message"}`, and the turn ends; the answer starts the next turn |
| `/askmany` | an `ask` with the full `ask_user` payload: a title, a context, and one question of every kind (single with details and a recommended option, multi 1–3, text, confirm, number 1–10 with defaults) |
| `/plan` | a `plan` part and an `ask{payload.kind: "plan"}`; `{"decision":"approve"}` approves it, `keep_planning` with a `note` sends it back (`plan.decision`, `plan.note` on the part) |
| `/md [ms]` | a streamed `reasoning` part, then a long Markdown answer (a heading, a list, Go code, a table, a quote, a link, a very long line) |
| `/tools` | Read, a Bash call with streamed output, an Edit `diff` part, a two-file `diff` in Codex's `changes` shape, and a Grep that fails |
| `/todo [ms]` | a `todo` part replaced as each of three tasks is done |
| `/workflow [ms]` | a `Workflow` part with three phases and four `WorkflowAgent` parts nested in it, each with a text part of its own |
| `/dividers` | `notice{compacted}`, `account.switched`, `model.changed`, `error{send_failed}` |
| `/subagent` | a `Task` tool part with a text and a tool part nested inside it |
| `/sleep S` | works for S seconds |
| `/bg S` | ends at once, leaving a background task for S seconds (`tasks`) |
| `/limit` | `rate_limit`, `error`, `turn.ended{limited}` |
| `/crash` | the CLI child is killed mid-turn |
| `/orphan` | starts a `setsid sleep 600` outside the process group (pid in `agent/fake/orphan.pid`) |
| anything else | `you said: <text> (turn N of <conversation>)` |

`Command("compact")` emits `notice{compacted}`, `Command("context")` a `usage` and
`notice{context}`; other commands are `unsupported`. Attachments are named in the reply (`with N
attachment(s): …`).

## 9. Tests

- `internal/agent`: journal reopen, torn/zero-filled/damaged tail, missing newline; coalescing per
  part (110 deltas → 2 lines, all 110 live); final state; projection folding incl. part
  replacement, overlapping/gapped deltas, first answer wins, interrupted turn; UTF-16 offsets; feed
  ordering.
- `internal/agenthost` (real host processes; Linux): dedupe; reconnect with `since` gets exactly the
  missing events (byte-identical to the journal); SIGKILL mid-turn → CLI child dead (Pdeathsig),
  recovery, resume, dedupe across hosts; park on idle → resume; no park while a question is
  waited on (in its turn) or with a background task; **systemd scope** (root + systemd only): host in `socrates-test-agents-<id>.scope`
  with `OOMPolicy=continue`, `Reap` refuses a live host, an orphan keeps the scope after SIGKILL and
  `Reap` ends it.
- `internal/server` (real hosts; Linux): create idempotency, terminal routes refused, conversation
  id recorded, activity + unread; server restart mid-turn; server down 30 s while the turn ends;
  host SIGKILLed mid-turn; exactly-once across a server crash after sending; two devices on one
  stream; two devices answering; answer after the call is gone; park and resume; queued messages;
  hello and 4401. WP9: the line, the question and the reading on Home (and its order before the
  activity), the briefs (300-chat start measured, a stale brief read again, the history let go and
  read back), the badge and every push wording (a Stop pushed to nobody, a killed host's turn pushed
  as "Stopped" and unread), the title from the first
  message and again from the journal (never over a name the person chose), Status from the journal,
  the outcome judge (verdict, held push, Read again, a judge that fails, a stopped turn not judged),
  the cost ledger (each agent's pricing with every token once, the account split with no ledger
  left behind, a rebuilt journal's own ledgers, the transcript of a chat's conversation never
  counted, also after the chat is deleted, the shared folders scanned), Ultracode reaching a Claude
  chat's CLI and its change from the ⋮, a prepared chat started by the clock, and a lost chat folder
  rebuilt when opened with no push for the history it gets back - the last three through real hosts with fakeclaude, fakecodex and fakeopencode, whose
  adapters the server's test binary registers.
- e2e (`run.mjs`, the scripted agent): `chat-home`, `chat-push`, `chat-title`, `chat-status`,
  `chat-outcome`, `chat-prestart`, `chat-ultracode`.
- `e2e/chats.mjs`: the real binary, SIGKILL of the server mid-turn, replay, duplicates, two-device
  answers, the session list.
- ask_user (WP6): `internal/agent/askuser` (schema limits, the checks and their messages, the
  shorthands, answers read back, the message text, every converter both ways, the MCP server:
  handshake, a blocking call, cancellation, progress never after the response, converted forms
  beyond the limits, `SaysMore`); `internal/agent`: `agent.Waits` (an answer racing the call's
  end decided once, withdrawn calls, an elicitation answer deferred), `Unwaited` and the activity
  of an ask of another turn; each adapter against its fake (`fakeclaude` `/askuser` `/askdrop`
  `/elicit [url]` `/mcp`; `fakecodex` `/tool ask_user`, `/input`, `/async`, `/elicit`;
  `fakeopencode` starts the config's local MCP servers, `/askuser` `/mcp` `/question unseen`): the
  ask, the answer back as §5.2, a bad call sent back, an interrupt (at once, before the call's own
  cancel) or a CLI exit making it `ErrAskGone`, a call withdrawn right behind its `tools/call`,
  Codex code mode deferred, an elicitation answer with a note or a question back deferred, an
  OpenCode question asked while `/event` was down; `internal/agenthost` through real hosts: two
  devices, an ask left by an interrupted turn lets the host park and is answered as a message by
  the next one, an ask survives a SIGKILL of its host, a message after the call is gone closes the
  ask (one sent while it waits does not), the `ask` op of a local tool (its turn, its answer
  frame, an interrupted call that does not keep the host, refused outside a turn and on protocol
  1, no events or deltas on its connection), and `ServeMCPAsk` end to end; OpenCode in a real
  host: `mcp-ask` answered through the host, and an aborted call answered as a message.
  `e2e/asks.mjs`: the real binary with the three fakes as the CLIs, the answer route with the
  dialog's §5.2 body for each agent, a second device, the answer after an interrupt, Codex code
  mode. Real binaries: `SOCRATES_REAL_CODEX=1` (ask_user blocking, code mode deferred, Plan mode
  `request_user_input`, `request_user_input_async`, an MCP server's elicitation through the
  granular policy, the guidance in the developer instructions, kept once by a thread resumed
  twice), `SOCRATES_OPENCODE_REAL=1` (serve
  starts `mcp-ask` and lists ask_user with its instructions; a free model of the `opencode`
  provider asks in a real host and goes on from the answer; skipped when its quota is used up).
- `internal/agent/codex` against `e2e/fakebin/fakecodex` (built by the tests; a scripted app-server
  that keeps real-format rollouts, see its package comment): every mapping, steering and holding,
  shell lines, interrupt, limit, crash, answered requests, the dynamic-tool ask hook, park, resume,
  rebuild, the account-switch chain, and the adapter inside a real host process (SIGKILL takes
  codex along, the next host resumes). With `SOCRATES_REAL_CODEX=1` the same scenarios run against
  the installed codex with `internal/agent/codex/fakemodel` as its model provider (a scripted
  Responses API whose encrypted reasoning is bound to an account; `e2e/fakebin/fakemodel` serves
  it on a port).
- Accounts (WP7): `internal/store` (the table, the order, origin once, the upgrade of a schema-9
  database through 10, 11 and 12); `internal/vault` (the key file made once); `internal/accounts`
  (the environment of each kind: the allow-list, no service variable, no key; the sealed key
  variables; Codex's device code and key against `fakecodex`; the migration rehearsed on a
  synthetic HOME and a schema-9 store: folders renamed with their inodes, links back, writes
  through a link landing in the one file, no second copy of any login, the tar, run twice, and
  cut short after each of its steps - between a move and its link, and inside a swap - and run again
  to the same result; a new data folder and a temporary one leave HOME alone; the multi-account
  folders stay; one folder that cannot move holds up nothing; the old path is never missing while
  it moves); `internal/agenthost`
  through real hosts with the fake (A → B → A with the keys in no file, a switch during a turn
  interrupting it and continuing in order, after the turn acked pending then final, the divider at
  boot, a key sealed for another chat refused, a switch without an environment refused, a second
  Continue to the same account saying nothing); each adapter's switch A → B → A in a real host
  (`fakeclaude`, `fakecodex`, `fakeopencode`) keeping its conversation; Codex's and OpenCode's held
  queue after a limit, and an "after this turn" switch made while OpenCode holds it; Claude's
  foreign-thinking notice; `internal/server` (the routes and that no answer, `host.json`, journal,
  log or outbox holds a key, a switch while running and while parked, an "after this turn" switch
  surviving a killed host, a limit stopping the chat or switching it and pushed once, Codex's device
  code, Claude's sign-in in a Shell, OpenCode's provider sign-in, a refused sign-in until the next
  one); `internal/web` (the foreign-thinking hint folded into the divider); `e2e/run.mjs`
  `accounts` (touch mode on a phone) and `chat-account` ("Switch now" on a phone) in the browser.
- Accounts and the rest together (WP7 + WP9): Ultracode kept across a switch through a real host;
  a chat that never switched on its account's ledger from its first turn, and the costs page naming
  the account; a chat's limit pushed once, in the accounts' words; a turn a switch made now stopped
  neither pushed nor judged, one a switch after the turn waited for still news; a lost folder's
  rebuilt history leaving its account's limit as it was.

## 10. Known limits, for the packages that follow

- A chat folder lost while the server runs and holds the chat's history is not noticed: the next
  host's journal starts again at seq 1, which the server's copy (further along) ignores. A lost
  folder is found at the next start (the brief is in the folder too) and rebuilt when opened.
- ask_user (WP6) for the views (WP8): an `ask` payload is `askuser.Payload` (§2, Asks) or Claude's
  plan card (`kind: "plan"`); `source` says when the questions came from one of the agent's own
  mechanisms (an elicitation offers no "You decide", `allow_delegate: false`); `part_id` names the
  tool part the call belongs to, which the view can fold into the ask card; an ask with `closed`
  (`ask.closed`) is shown as not answered, and can still be answered. For the feeds (WP9): read
  `OpenAsks()` and `Summary()` — an open ask outside a running turn keeps the chat `waiting` while
  no turn runs, until it is answered or the person sends a message, which closes it (§4.4).
  Claude's ask_user is unverified against a real CLI here (no Claude login for tests): the
  in-process MCP path is research/03 §4.1, and the cancel on an interrupt is taken from
  `notifications/cancelled` and `control_cancel_request`, both handled. `TestLiveAskUser`
  (`SOCRATES_CLAUDE_LIVE=1`) checks the handshake, `_meta claudecode/toolUseId`, the blocking call
  and the cancel on an interrupt against the real binary; it has not run yet.
- The Claude adapter's replay receipt is in (§5.1). An API-key account reaches it through
  `SOCRATES_CLAUDE_API_KEY` in the sealed `secret_env` (§11); Ultracode (`SOCRATES_CLAUDE_ULTRACODE`)
  is the chat's option (`host.json` `ultracode`), which the host adds on top of the account's
  environment when it starts the CLI and again with every `switch_account`.
- For the feeds and the cost ledger (WP9): every `turn.started` names its `account` (an id of
  `GET /api/accounts`), and `account.switched{from, to}` marks each move. The ledger puts each turn
  on the account its `turn.started` names (§3, Costs), and the costs page names the accounts. A
  journal from before WP7 names none: its turns are on the account its first switch's `from` says,
  and until there is one on `chat-<id>`, with no account. The server keeps a chat's account in its
  row's options (`account`, `auto_switch`).
- For WP10: the terminal-only profiles (`/api/profiles`, `harnesses/profiles.go`, the sheet's
  profile field for terminals) stay as they were; the dashboard's Profiles card is gone, and the
  migration took every profile's logins into accounts (§11).

## 11. Accounts (WP7)

A chat runs on an **account**: one agent's subscription or API key (REBUILD.md §4). The code is
`internal/accounts` (model rules, environment, folders, sealing, sign-in flows, migration),
`internal/store/accounts.go` (schema 12), `internal/server/accounts.go` and `chataccount.go`, and
`internal/agenthost/switch.go` / `secretenv.go`.

**Model.** Table `accounts`: `id` (`a-` and 12 hex digits, also the folder's name), `agent`
(`claude`, `codex`, `opencode`; `fake` when the scripted agent is registered), `label` (≤ 40),
`kind` (`subscription` | `api_key`), `sort_order` (one order across all agents, the person's),
`state` (`signed_in` | `needs_sign_in` | `error`) and `state_detail`, `email`/`plan` (what the
agent last said about the login), `provider` (an OpenCode key's provider), `origin` (the folder
the migration moved the login out of; unique), `limits`/`limits_at` (the last usage reading),
`limited_until` (when a limit the account ran into renews), `secret` (the sealed key). The state is
read again from the folder whenever the list is read (`accounts.Detect`: Claude's
`.credentials.json` or `primaryApiKey`, Codex's `auth.json`, OpenCode's `auth.json` providers, a
key for an API-key account), so a login that appears or goes is seen without Socrates touching it.

**Folders.** Claude Code: `<data>/accounts/<acct>/` is `CLAUDE_CONFIG_DIR`, prepared with
`claude.PrepareConfigDir` (its conversations shared through `<data>/agents/claude`). Codex: the same
path is `CODEX_HOME`, prepared with `codex.PrepareHome` (`<data>/agents/codex`). OpenCode:
`<data>/agents/opencode/accounts/<acct>/` is `XDG_DATA_HOME` (§5.3, `Layout.PrepareAccount`). The
login in a folder is written by the agent's own sign-in, and read, never written, refreshed or
deleted, by Socrates — except that deleting an account removes its folder, which the server refuses
while a chat runs on it and for an account the migration moved in (a link in HOME points at it).

**Environment** (`accounts.Env`, what `host.json`'s `env` holds). From the server's own
environment only an allow-list passes: `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`,
`LANGUAGE`, `LC_*`, `TZ`, `TMPDIR`, the CA bundle and proxy variables, `SOCRATES_CODEX_BIN` and
`SOCRATES_OPENCODE_BIN` (the binary overrides). Never a service's variables (`INVOCATION_ID`,
`NOTIFY_SOCKET`, `JOURNAL_STREAM`, …) and never any key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY` of
the server's environment do not pass). Then the account's folder variable (`CLAUDE_CONFIG_DIR`,
`CODEX_HOME`, OpenCode's `XDG_DATA_HOME` and its tool configuration).

**Secrets.** An API key is sealed (`internal/vault`, AES-256-GCM, additional data
`account:<acct>`) with `<data>/secrets.key` (32 random bytes, 0600, made once by a link so two
starts cannot make two) and kept in `accounts.secret`; no answer, page, log or journal carries it
(the store's `Account.Secret` is never encoded). For a chat the server opens it and seals the
variables that carry it — `SOCRATES_CLAUDE_API_KEY` (the Claude adapter passes it as
`ANTHROPIC_API_KEY`), `OPENCODE_AUTH_CONTENT` (`{"<provider>":{"type":"api","key":…}}`),
`SOCRATES_FAKE_API_KEY` — again, for that chat only (`agenthost.SealEnv`, additional data
`chat-env:<chat>`, base64): `host.json`'s `secret_env`, and a `switch_account` command's (so the
outbox in SQLite holds only ciphertext). **How the host gets the environment:** it reads `env` and
`secret_env` from `host.json` (or the command), opens `secret_env` with `secrets.key` of its
`--data` folder, refuses one sealed for another chat, and joins the two in memory for
`StartSpec.Env`. A Codex key is also logged in by codex itself (`account/login/start {type:
apiKey}` into the account's `CODEX_HOME`), so no Codex chat needs a key variable. The backup leaves
out `host.json`'s `env` and `secret_env` (`paths.Redacted`), takes `secrets.key` with the store
(both are needed to read the keys back), and leaves out the accounts' logs.

**Sign-in** (`POST /api/accounts/{id}/signin`; a flow lives 15 minutes, one per account):

- Claude Code: a Shell terminal session (dynamic folder) that runs `claude auth login --claudeai`
  with the account's `CLAUDE_CONFIG_DIR`, then stays a shell; the answer is `{session}` and the
  page goes there. The account is signed in once its folder has the login. (Detection reads
  `.credentials.json`; on macOS Claude Code keeps the login in the Keychain, which is not read, so
  such an account shows as not signed in there.)
- Codex: a short-lived `codex app-server` with the account's environment:
  `account/login/start {type: chatgptDeviceCode}` → `{verificationUrl, userCode}` shown with a Copy,
  `account/login/completed` ends it; Cancel sends `account/login/cancel`. The identity is
  `account/read`.
- OpenCode: `opencode serve` on the account's data home; `GET /provider/auth` lists the providers
  and their OAuth methods (a key is an account of its own), `POST /provider/{p}/oauth/authorize
  {method, inputs}` → the page and either `auto` (the provider calls back; the flow waits) or `code`
  (the person pastes it: `POST …/signin/code`), then `/provider/{p}/oauth/callback`.
- An API key: the form; `PATCH /api/accounts/{id} {key}` replaces it.

**Routes.**

| Route | Body | Answers |
|---|---|---|
| `GET /api/accounts` | – | `{accounts: [<account>], recommended: {<agent>: <id>}, agents}`; each with `limits` (5h/7d windows), `chats` (how many run on it), `recommended`, `signin` (a flow under way) |
| `POST /api/accounts` | `agent`, `kind`, `label`, `key`?, `provider`? | 201 `{account}` |
| `PATCH /api/accounts/{id}` | `label`?, `key`?, `provider`? | `{account}` |
| `DELETE /api/accounts/{id}` | – | `{ok}`; 409 while a chat runs on it or for one that holds an agent's own sign-in from HOME (`origin` `~/.claude`, `~/.codex`, `~/.local/share/opencode`); one made of a profile goes, with the link left in the profile's place |
| `PUT /api/accounts/order` | `order`: every id | `{ok}`; 409 when the list changed |
| `POST`/`GET`/`DELETE /api/accounts/{id}/signin`, `POST …/signin/code` | above | `{signin: <flow>}` or `{session}` |

**A chat's account.** Kept in its row's options (`account`, `auto_switch`). A chat that names none
(or one deleted since) gets the recommended account of its agent at launch, else its first, else a
new "Default" subscription; the choice is written back. The launch puts the newest
`switch_account` still in the outbox into `host.json` (§4.3: a switch asked for while parked).

**Limits and the recommendation.** Claude Code's windows are read for the account's folder as the
dashboard reads usage; Codex's with `account/rateLimits/read` (a short-lived app-server, every 5
minutes at most); every other account's are the last `rate_limit` events its chats reported. The
recommended account of an agent is, of its signed-in accounts that have not run into a limit,
the one with the most headroom: what is left of the week per hour until it renews, times what is
left of the five hours (`headroom`); one with no reading is recommended only when every account
with one is at its limit.

**At a limit** (`turn.ended{limited}`): the account's `limited_until` is the latest renewal of the
turn's windows (an hour when none was said). A chat with `auto_switch` (off unless set, from the
sheet or the chat's ⋮) is switched by the server to the recommended other account, `when: now`,
`continue: true`, `client_id` `auto-<turn>`, reason "The limit of <label> was reached."; without
it, or with no account to go to, the person is told by push ("Limit reached — renews in …") and the
limit card offers "Continue with <account>" for each account with room (the same switch, sent by
the browser). Codex and OpenCode hold their queue meanwhile (§5.2, §5.3), so the messages queued
behind the limit do not each run into it. A chat is pushed once per account it is stopped on,
until a turn gets through again.

**A refused sign-in** (`error{code: auth}`, `agent.ErrorAuth`: OpenCode's `ProviderAuthError` or a
401, among them a refresh token two chats of one OAuth account renewed at the same moment,
research/06): the account's state becomes `error` with "Sign in again" (an API key: "Replace it"),
although its login is still on disk; a switch to it is refused until a sign-in, a new key or a
turn on it that completes brings it back.

**The divider's "i"** carries the reason, who the new account is (`email`, `plan`, as last
reported), what Claude set aside (`notice{foreign_thinking}`: resumed on another organization's
account, Claude Code goes on without the earlier thinking and keeps it for the way back,
research/08; the chat folds it into the newest divider) and that the next reply may be slower (the
new account has none of the conversation's cache).

**Migration** (`accounts.Migrate`, at server start while the store marks it due): it runs only on
a data folder of the build before accounts. The store writes `kv accounts.migration = pending` in
the same upgrade that brings schema 12 to an older database (before the new version is written),
and the migration clears it when every source is done; a data folder this build makes has no mark
and never takes anything from HOME — a new installation, `socrates -data /tmp/x`, an e2e run in
live mode. Nor is a folder outside the temporary directory moved into a data folder inside it (a
rehearsal on a copy). The logins of the machine's HOME (`~/.claude` with `~/.claude.json`,
`~/.codex`, `~/.local/share/opencode` with `~/.config/opencode`) and of every profile become
accounts by **renaming** their folders into the account folders (the same inodes: never a copy,
never two copies of a login), and the old path becomes a link to the new one, so a terminal or a
CLI started by hand still finds the same login and writes go to the one file. The rename and the
link are **one step** on Linux: a link is made at the new place and exchanged with the folder
(`renameat2(RENAME_EXCHANGE)`), so the old path is never missing and an agent writing by path
meanwhile never makes a second folder there; a file system that cannot exchange gets a rename and
then the link. The shared folders inside (`projects/`, `sessions/`, …) are swapped for their links
the same way before their content is merged into `<data>/agents/<agent>/` (what both have stays
aside as `<name>.pre-rebuild`). First a tar of the data folder and the moved folders,
`<data>.pre-rebuild.tar` (0600): it holds the logins **unencrypted**, and the agents' live SQLite
files as they were read (a running agent may leave a torn copy; the moved originals are whole) —
delete it once the accounts work. Idempotent and safe to interrupt: each account is made once per
`origin`, a source is planned from its account row as well as from the disk (a run cut short
between a rename and its link is finished), and every step looks at the disk before acting. A
source that fails is logged and tried again at the next start while the others go on. A folder on
another file system (EXDEV), or one that is a link of the person's own to elsewhere, is left where
it is, and its account says why. A lost profile's account has the origin `lost:<profile>:<agent>`.
`~/.codex-multi` and `~/.config/opencode-multi-auth` are left alone, also when `CODEX_HOME` points
into one. WP10 runs the clean cut (§8.3, stopping the old agent terminals) before it.

