# Socrates rebuild: web-native agents, local-first storage, accounts (plan, 2026-10-23)

Status: **binding** (product owner's answers of 2026-10-23 in §1). This document is binding for the rebuild, the same way DECISIONS.md was for the terminal rewrite.
Research behind every claim (verified by experiment against claude 2.1.309, codex-cli 0.164.0,
opencode 1.19.4) is in `docs/design/research/01..05-*.md`.

## 0. What changes, in one paragraph

Agent sessions stop being terminals. Claude Code, Codex and OpenCode are driven through their
structured protocols (`claude -p` stream-json + control protocol, `codex app-server` JSON-RPC,
`opencode serve` HTTP+SSE), and the whole conversation is rendered by Socrates' own web components:
messages, tool calls, diffs, plans, todos, and questions. The answer operator and the screen digest
go away. A plain **Shell** terminal stays (tmux, as today), and so does VS Code. All state lives in
**one local data folder**; S3 becomes a **periodic encrypted backup** of that folder and is never
read at runtime. **Accounts** (subscription logins and API keys, per agent) become first-class and
a running chat can switch account. Questions use a Socrates tool, **`ask_user`**, that is richer
than any agent's built-in one.

## 1. Fixed decisions (from the product owner, 2026-10-23)

| Topic | Decision |
|---|---|
| Agent UI | Fully web-native for Claude Code, Codex, OpenCode. No agent TUI in the browser any more. |
| Terminal | Stays, **Shell only** (plus VS Code, which rides the same tmux substrate). No "open chat in CLI" escape hatch. |
| Accounts | Several accounts per agent: **subscription logins and API keys**. Switchable while a chat is open. |
| Storage | One local folder holds everything incl. all chats; S3 = periodic backup only, never used live. |
| Backup scope | Everything **except working directories** (code lives in git). |
| Permissions | **Always allow everything** (as today's HARNESS-POLICY). No approval UI for tool use. |
| Limit reached | Per-chat option "Switch account automatically when a limit is reached", **off by default**. Off: the chat stops with a one-tap "Continue with <account>" card and a push. |
| Existing agent sessions | **Clean cut.** Claude/Codex/OpenCode terminal sessions are not carried over; Shell sessions stay. |
| `ask_user` | **Always waits.** The agent stops until the person answers; there is no "keep working" mode. |
| Outside multi-account setups | `~/.codex-multi` and `~/.config/opencode-multi-auth` are **ignored**; accounts are created in Socrates. |

Unchanged from earlier decisions: English everywhere in the product, phone-in-a-car first (offline
resilience, nothing typed is lost, push, voice), Dashboard-Style design system, auth/tunnel/PWA/push
as they are, one implementer pass + one Fable review per package.

## 2. Architecture overview

```
browser ── /api/events (WS, as today: list, activity, home, push badge)
        ── /api/chats/{id}/stream (WS, new: one open chat, seq replay)
        ── /api/terminals/{id}/ws (socrates.term.v1, unchanged, Shell only)
   │
socrates serve (systemd: socrates.service)  ── store (SQLite), backup, accounts, push, voice, ...
   │  unix socket per chat, versioned JSON protocol
   ▼
socrates agent-host --chat <id>   (one per *active* chat; own systemd scope socrates-agents-<id>,
   │                               survives service restarts and updates, like tmux today)
   │  owns: the CLI child, the adapter, the chat's event journal, ask_user
   ▼
claude -p … | codex app-server | opencode serve      (child of the host, env = the chat's account)
```

### 2.1 The agent host (durability)

The terminal rewrite got durability from tmux. Stdio protocols die with their pipe, so each active
chat gets a small holder process, `socrates agent-host` (same binary, new subcommand; prior art:
`git show 5d7b420^:internal/agenthost/`).

- Started via `systemd-run --scope --unit=socrates-agents-<chat>` (same pattern and OOM policy as
  the tmux scope), so `systemctl restart socrates` and nightly updates never kill a running turn.
- Holds the CLI child and its stdio, runs the harness **adapter** (§3), writes the chat's
  normalized **event journal** (§2.2), owns pending `ask_user` calls (§5).
- Listens on `<data>/run/chats/<id>.sock`. The server is a client: `hello{since: seq}` → replay
  from the journal, then live events; commands: `send`, `interrupt`, `answer`, `set_model`,
  `switch_account`, `compact`, `command`, `park`, `stop`. Commands carry a `client_id`; the host is
  idempotent on it (exactly-once sends across reconnects).
- Host protocol is versioned. After an update an idle host is **parked** (CLI stopped cleanly,
  resumed on the next message, +0.2–0.5 s); a busy one finishes first. "Idle" excludes a pending
  `ask_user` and CLI background tasks, so a host of the previous protocol version can live for
  about a day and the server keeps speaking that version (spike S4: re-exec keeping the child
  works but does not pay off yet).
- Process hygiene (spike S4, research/07): one scope per chat; the CLI is started from a locked
  OS thread with `Pdeathsig=SIGKILL` in its own process group, so a killed host takes the CLI
  along; tool children that escape are ended by stopping that chat's scope — only after the host
  is confirmed dead (stopping a "stale" scope would kill a live host). Dead-host recovery: confirm
  dead → stop scope → record the turn as interrupted → resume.
- Exactly-once (verified across server restarts and a new host): the server keeps an outbox of
  unacknowledged commands, the host journals each `client_id` before acting and rebuilds its
  dedupe set from the journal; Claude's `--replay-user-messages` serves as the delivery receipt
  for the remaining window (host dies between journaling and writing to the CLI).
- **Parking:** a chat idle for 20 min (setting) stops its CLI child and then the host. The next
  message starts a host again and resumes the CLI conversation (`--resume`, `thread/resume`,
  OpenCode session id). This keeps process count proportional to *active* chats and makes "resume
  after reboot" the same code path as "resume after idle".
- On server start: reattach to every live host socket; chats whose host is gone and that were busy
  show the existing "resumed after restart" notice when they are opened.

### 2.2 The normalized event model and journal

One model for all three agents, so the UI, Home, push, titles and outcomes only know one shape.

- **Journal:** `<data>/chats/<id>/events.jsonl`, append-only, every line `{seq, at, type, ...}`.
  Deltas are coalesced before they are written (one line per ~250 ms per part, plus the final
  state). This journal **is** the chat history Socrates renders and backs up; the CLIs' own
  transcripts remain the source for *resuming the model's context*.
- **Types:** `turn.started`, `turn.ended{outcome, usage, cost}`, `message{role, id}`,
  `part{message_id, part_id, kind: text|reasoning|tool|diff|plan|todo|image|file|notice, state,
  content}` (a part is replaced by later events with the same id), `ask{ask_id, payload}`,
  `ask.answered{ask_id, answer, by_device}`, `account.switched{from, to}`, `model.changed`,
  `rate_limit{window, used, resets_at}`, `process{state: starting|ready|parked|exited|failed,
  detail}`, `error`.
- **Rebuild:** a chat whose journal is missing or damaged is rebuilt once from the CLI's own
  transcript (Claude jsonl, Codex `thread/items/list`,
  OpenCode `GET /session/{id}/message`).

### 2.3 Browser transport

- `/api/chats/{id}/stream`: WebSocket, `hello{since}` → journal replay → live. Same resilience
  rules as `/api/events` (handshake deadline, quiet ping, hidden grace, 4401 on revoked login).
  Live text deltas are sent uncoalesced to an attached browser.
- Sending: the composer uses the existing (currently unused) `Outbox` in `net.js`. Every message
  has a `client_id`; `POST /api/chats/{id}/messages` is idempotent on it. Messages typed offline
  are queued and delivered exactly once; a message sent while the agent is busy is queued by the
  agent (all three support it) and shown as "queued".
- Answers to `ask_user` are idempotent on `(ask_id, client_id)`; the second device sees
  "Answered on another device" (existing UX contract).

## 3. Harness adapters

Every adapter maps its protocol onto §2.2 and implements the host commands. Launch policy stays
fixed (HARNESS-POLICY.md, minus everything TUI-related).

### 3.1 Claude Code

- Argv: `claude -p --input-format stream-json --output-format stream-json --verbose
  --include-partial-messages --permission-prompt-tool stdio --setting-sources project,local
  --strict-mcp-config --dangerously-skip-permissions --disallowedTools AskUserQuestion
  --append-system-prompt <ask_user guidance> [--resume <id>] --model <m>`; env built from scratch:
  `CLAUDE_CONFIG_DIR=<account dir>`, `IS_SANDBOX=1` as root, no `ANTHROPIC_*` unless the account is
  an API key, `CLAUDE_CODE_ENABLE_TODO_TOOLS=1`.
- `initialize` control request registers the in-process MCP server `socrates` with `ask_user`
  (verified: blocks 20 min without timeout; wall clock limit ~27.8 h).
- Commands: `interrupt`, `set_model`, `set_permission_mode` (plan mode), slash commands as user
  text (`/compact`, custom commands verified headless).
- Subagents and workflows: headless 2.1.309 offers `Task` (subagents; `task_*` events in the
  stream), `Workflow` (dynamic multi-agent workflows), background agents, `Monitor` and
  `ScheduleWakeup` (verified in `system/init` 2026-10-23). They must work exactly as in the TUI:
  the adapter maps subagent and workflow progress to nested parts (one group per agent with its
  own tool rows and status), a background task that finishes may start a turn by itself, and the
  session option "Ultracode" stays. WP3 captures the exact event shapes of a real workflow run.
  WP3 also proves a running workflow survives `systemctl restart socrates` (S4 proved it for a
  plain streamed turn only) and records what a machine reboot does to one in flight.
- Plan mode: `ExitPlanMode` arrives as a permission request carrying the plan → rendered as a plan
  card with Approve / Keep planning (the one approval that stays, because it is a question, not a
  permission).
- Shared transcripts: every account's `CLAUDE_CONFIG_DIR/projects` is a relative symlink to
  `<data>/agents/claude/projects` (verified: resume under a second config dir via shared
  `projects/` works). Same for `tasks/` (TaskCreate/TaskUpdate state), `plans/`. Per account
  stay: `.credentials.json`, `.claude.json`, `sessions/`, caches and policy files (spike S1,
  research/08).
- Spike S1 (two real subscriptions in different orgs): a conversation moves A → B → A with full
  context between turns, after an interrupt, after `/compact` and with signed thinking. Claude
  Code drops the other org's thinking itself (stream warning, no 400s), so no stripping is needed.
  A switch therefore always **interrupts gracefully** before relaunching: after a SIGKILL the
  resumed model may repeat the interrupted tool call.

### 3.2 Codex

- One `codex app-server` (stdio) per active chat, child of the host, `CODEX_HOME=<account dir>`,
  `capabilities.experimentalApi`, `approvalPolicy: never`, `sandbox: danger-full-access`.
- `ask_user` as a **dynamic tool** on `thread/start` (verified: no timeout; persisted and restored
  on `thread/resume`). Also handled because the model uses them on its own: `request_user_input`
  (plan mode), async questions (`delivery: "async"`), MCP elicitations — all rendered with the
  same ask component.
- gpt-6 "code mode" models poll a blocking tool every ~30 s (one cached model request each). For
  those models `ask_user` runs in **async mode** (§5.3).
- Shared rollouts: every account's `CODEX_HOME/sessions` and `archived_sessions` symlink to
  `<data>/agents/codex/…` (verified: thread found and resumed under a second CODEX_HOME).
- Commands: `turn/interrupt`, `turn/steer`, model/effort per turn, `thread/compact/start`,
  `review/start`, `thread/fork`, `thread/shellCommand`.

### 3.3 OpenCode

- One `opencode serve --hostname 127.0.0.1 --port <p>` per active chat, child of the host,
  `OPENCODE_SERVER_PASSWORD`, `OPENCODE_CONFIG_CONTENT` (share disabled, autoupdate off,
  `permission: allow` incl. `external_directory`/`doom_loop`/`.env` reads, `question: deny`, MCP
  `socrates` = local command `socrates mcp-ask --socket <host sock>` with a 24 h timeout and
  progress notifications), skills import off.
- Shared data dir `<data>/agents/opencode` (sessions in `opencode.db`), account credentials passed
  per process (`OPENCODE_AUTH_CONTENT`). **Spike S3** must confirm that several serve processes on
  one data dir are safe and that OAuth refreshes do not leak between accounts; fallback: one serve
  per account with `PUT /auth` + `instance/dispose` only while the chat is idle.
- Events from `/event`; recovery after reconnect via `GET …/message`, `/session/status`.
- Commands: `prompt_async` (queues while busy), `abort`, `command`, `summarize`, `revert`, `fork`,
  model + variant + agent per prompt.

## 4. Accounts

### 4.1 Model

An **account** belongs to one agent and is one of:

| Kind | Claude Code | Codex | OpenCode |
|---|---|---|---|
| Subscription login | own `CLAUDE_CONFIG_DIR`, signed in through Claude's own flow | own `CODEX_HOME`, ChatGPT device-code login | provider OAuth (e.g. ChatGPT, Anthropic) via `/provider/{id}/oauth/*` |
| API key | key stored encrypted in the store, passed as `ANTHROPIC_API_KEY` | `account/login/start {type: apiKey}` into its `CODEX_HOME` | `PUT /auth/{provider}` content per process |

Accounts replace today's profiles (a profile was one directory per agent; an account is one agent
each, which is what switching needs). Fields: id, agent, label, kind, created, order, last known
plan/email (read from the agent, shown behind the info tip), limits snapshot.

### 4.2 Signing in from the web

- **Claude subscription:** "Sign in" opens a Shell terminal with `CLAUDE_CONFIG_DIR` set to the new
  account and runs `claude auth login`. The login completes through Anthropic's own flow and the
  credentials are written by Claude Code itself; Socrates never sees or stores them. This keeps
  Socrates inside Anthropic's rules for third-party tools (no collecting or intermediating claude.ai
  tokens; an end user signing in to the unmodified binary is allowed).
- **Codex subscription:** device code shown natively in the web UI (`account/login/start
  {type: chatgptDeviceCode}` → URL + code → `account/login/completed`).
- **OpenCode:** the provider auth methods from `GET /provider/auth`, rendered natively.
- **API keys:** a form; stored with the existing vault (AES-GCM) in the store.

### 4.3 Switching account in a running chat

- The chat header shows the account next to the model; a tap lists the agent's accounts with their
  5 h / weekly usage and "renews in" (existing usage strip data), the recommended one marked
  (existing headroom recommender).
- Switching while busy: interrupt is offered, or "switch after this turn" (queued).
- Mechanics: the host parks the CLI and relaunches it with the other account's env and the same
  conversation (`--resume` / `thread/resume` / same OpenCode session). The journal gets an
  `account.switched` divider in the chat.
- **Codex caveat:** encrypted reasoning items may be rejected under another ChatGPT account
  (openai/codex #25290, #36704). Spike S2 decides between (a) stripping reasoning/compaction items
  from the resumed rollout and (b) a fresh thread seeded with a handoff summary. The UI tells the
  person when (b) happens.
- When an account hits its limit mid-turn (`rate_limit` event / error):
  - **Auto-switch on** (per-chat option in the chat's ⋮ and in the new-session sheet, default
    off): the host switches to the recommended account of the same agent that still has headroom
    and re-sends the interrupted turn ("continue"); the chat shows an `account.switched` divider
    with the reason. If no account has headroom it behaves as "off".
  - **Off:** the chat stops with a "Limit reached — renews in 2 h 10 min" card and one-tap
    "Continue with <account>" buttons; a push is sent.

### 4.4 Usage and costs

Limits are read per account (Claude OAuth usage endpoint per config dir, Codex
`account/rateLimits/read`, stream `rate_limit` events). The cost ledger keeps scanning the shared
transcript folders; each turn's account is recorded in the journal so costs can be split per
account.

## 5. `ask_user` — Socrates' own question tool

Yes, it has to be ours: Claude's AskUserQuestion is capped at 4 questions × 4 options with a 12-char
header and no explanation field, Codex's at 3 questions × 3 options, OpenCode's similar. One
Socrates tool, the same in all three agents, removes the limits and gives one UI.

### 5.1 Schema (what the model sees)

```jsonc
ask_user({
  "title": "Deployment setup",              // optional: what this batch is about
  "context": "markdown",                     // optional: background for all questions
  "questions": [                             // 1..20
    {
      "id": "db",                            // stable id, used in the answer
      "header": "Database",                  // short chip, ≤ 24 chars
      "question": "Which database should the service use?",
      "context": "markdown",                 // why it matters, trade-offs, code, links
      "kind": "single" | "multi" | "text" | "confirm" | "number",
      "options": [                           // 0..12 (single/multi)
        { "id": "pg", "label": "PostgreSQL", "description": "one line",
          "details": "markdown shown when expanded (snippets, diffs, pros/cons)",
          "recommended": true }
      ],
      "min": 1, "max": 3,                    // multi only
      "allow_other": true,                   // free-text answer (default true)
      "allow_delegate": true,                // "You decide" button (default true)
      "default": "pg",                       // preselected
      "required": true
    }
  ]
})
```

### 5.2 Answer (what the model gets back)

```jsonc
{ "answers": { "db": { "selected": ["pg"], "other": null, "note": "keep it on 16",
                       "delegated": false, "skipped": false } },
  "general_note": "markdown or dictated text",
  "follow_up": null      // or a counter-question from the person, which the model must answer
}
```
The person may answer some questions and skip others, delegate a question ("You decide"), add a
note to any answer, or reply with a counter-question instead of an answer.

### 5.3 Delivery

- **Claude:** in-process MCP tool over the control protocol, blocking (verified 20 min, no idle
  timeout).
- **Codex:** dynamic tool, blocking on classic models (verified, no extra model calls while
  waiting). Code-mode models (gpt-6-*) would poll a blocking tool every ~30 s, so there the tool
  returns "Questions delivered. End your turn now; the answers arrive as the next message." and
  the answer starts the next turn. For the person it is the same: the agent waits.
- **OpenCode:** local MCP server `socrates mcp-ask`, blocking with progress notifications (verified
  75 s with progress; 24 h timeout configured).
- The agent always waits for the answer (decision §1). The system prompt says so: ask everything
  needed in one call, then wait.
- **Robustness:** a question survives everything. It is stored in the journal when asked. If the
  blocking call is gone when the answer comes (host parked, reboot, agent interrupted), the answer
  is delivered as a user message instead and the agent continues from there.
- Native question tools are disabled (Claude `--disallowedTools AskUserQuestion`, OpenCode
  `question: deny`) and the system prompt points at `ask_user`. Whatever native question still
  arrives (Codex plan mode, elicitations) is converted to the same payload and rendered the same way.

### 5.4 UI

- In the chat: an inline card with the title, a progress line ("2 of 5 answered") and the first
  open question.
- Touch mode / phone: the existing full-screen question dialog, one question per step, 64 px
  options, expandable option details, context collapsible, "Read aloud" (Google TTS) for question
  and context, dictation for free text and notes, Back/Next, "Send answers" at the end.
- Home's Questions card and the push notification carry the first question's text; the badge
  counts chats with an open `ask` (replaces the screen-digest logic in `asking.go`).

## 6. Storage: one local folder, S3 as backup

### 6.1 Layout (`SOCRATES_DATA_DIR`, default `/var/lib/socrates`)

```
socrates.db                 settings, logins, chats index, drafts, accounts, push, cost ledger
chats/<id>/events.jsonl     normalized chat history (§2.2)
chats/<id>/attachments/     files sent with messages (also copied to <workdir>/.socrates/uploads)
agents/claude/projects/     shared Claude transcripts (+ todos/, plans/)
agents/codex/sessions/      shared Codex rollouts (+ archived_sessions/)
agents/opencode/            OpenCode data dir (opencode.db, storage/) and config
accounts/<id>/              per account: CLAUDE_CONFIG_DIR | CODEX_HOME | OpenCode auth
                            (shared folders above are relative symlinks)
terminals/<id>/             Shell journal
vscode/, vscode-extensions/
backup.json                 S3 target + passphrase, 0600            (not backed up)
run/                        sockets, pids                          (not backed up)
cache/                      cloudflared, catalog, codex logs       (not backed up)
```
Working directories stay outside (workspace root, presets, custom paths) and are not backed up.
HOME-level agent folders (`~/.claude`, `~/.codex`, `~/.local/share/opencode`) are no longer used by
Socrates; every child gets its paths explicitly.

### 6.2 Backup engine (`internal/backup`, replaces `internal/mirror`)

- **Snapshot:** walk the data folder minus exclusions; SQLite files (Socrates' own and the agents'
  `opencode.db`, Codex `*.sqlite`) are copied with the SQLite online-backup API so a running agent
  never produces a torn copy.
- **Chunks:** files are split into 8 MiB chunks, each encrypted (existing vault, AES-256-GCM) and
  stored content-addressed as `chunks/<sha256>`. Append-only files (journals, transcripts) only
  upload their last chunk. A snapshot is a manifest `snapshots/<host>/<time>.json` (encrypted) of
  path → chunk list, mode, mtime.
- **Cadence:** every 10 min when something changed, on clean shutdown, and "Back up now".
  Retention: all snapshots for 48 h, one per day for 30 days, one per week for 6 months; unreferenced
  chunks are garbage-collected.
- **S3 client:** add multipart upload and streamed downloads (today's client caps responses at
  256 MiB and has no multipart).
- **Never on the hot path:** the app starts and runs without S3; a failing backup shows in Admin
  and as a notice after 24 h without a successful snapshot, nothing else.
- **Restore:** on a fresh server the setup page offers "Restore from backup": bucket credentials +
  passphrase → list of snapshots (host, time, size) → restore into the data folder → start.
  Absolute workdir paths that do not exist on the new machine are shown per chat with a "point to
  folder" action (chats stay readable either way). Two servers backing up into one bucket are
  told apart by host id; Admin warns when another host wrote a snapshot in the last 24 h.

### 6.3 Removed

`internal/mirror` (lease, fencing, hydrate, pending queue), `store/mirror.go` and its `touch()`
calls, `storage.go` bootstrap/connect mode/re-exec, `server/connect.go`, `server/storage.go`, the
storage step in `setup.html`, `js/storage.js`, the "Backed up" draft flag, the scheduler's lease
gate, `e2e/fakebin/fakes3` (replaced by a simpler fake for backup tests). About 10.7k lines.

## 7. What happens to every existing feature

Full inventory: research `02-feature-inventory.md`. Summary:

| Feature | Fate |
|---|---|
| Auth, logins, tunnel, Web Push, badge, PWA/sw.js, voice (dictation, TTS), admin, updates, diagnostics, watchdog, costs page, folders sidebar, shelves, Done sweep, rename, PreStart + scheduler, Home | **Keep** |
| Activity states, unread rule, `seq`, MovesRow | **Keep the model, change the sources** to journal events (busy = turn running, waiting = open `ask`, idle = turn ended) |
| Titles, spoken Status, Home one-line summary, turn outcome | **Adapt**: read the journal instead of the screen |
| New-session sheet | **Adapt**: agent → folder → model/effort → **account**; Shell → folder only |
| PreStart | **Adapt**: the draft becomes the first message |
| Uploads, paste/drop images | **Adapt**: message attachments (images as image parts, files as paths) |
| Usage strip, headroom recommender | **Keep**, keyed per account |
| Profiles | **Replaced** by accounts (§4) |
| Answer operator (`operator.go`), question extraction in `digest.go`, screen/title activity layers, Codex/OpenCode id discovery, theme forcing, initial-prompt file, agent key bar | **Delete** |
| tmux layer, `socrates.term.v1`, journal, `term.js`, key bar | **Keep for Shell** (and VS Code) |
| S3 store of record | **Replaced** by backup (§6) |

### 7.1 The chat view (new front end, Dashboard-Style rules)

- Message list keyed by `data-ds-key` (streaming never restarts animations), `morph()` only.
- Assistant text as Markdown (vendored renderer + sanitizer, pinned and checksummed like DS).
- Reasoning collapsed by default; tool calls as compact rows (icon, one-line summary, duration)
  that expand to input/output; file edits as a diff viewer; commands with their output; todo list
  pinned above the composer while a turn runs; subagents and workflow agents as nested collapsible
  groups (a workflow shows its phases and every agent's live status, like `/workflows`); plan card;
  ask card.
- Header: agent mark, title, model + effort picker, account picker with usage, Status (spoken),
  ⋮ (rename, file to shelf, export transcript, compact, delete).
- Composer: autosaving draft (per chat, same conflict rules as PreStart), Outbox, attachments,
  dictation pill, slash-command palette (from the agent's command list), Stop button while busy,
  "queued" chips for messages sent during a turn.
- Phone: composer above the keyboard (existing `--app-h` handling), 48 px targets in touch mode.

## 8. Migration of this installation

Runs once, automatically, on the first start of the new version, with a backup of the old data
folder first (`<data>.pre-rebuild.tar`).

1. Create the new layout. Move `~/.claude` + `~/.claude.json` into a "Default" Claude account,
   `~/.codex` into a "Default" Codex account, `~/.local/share/opencode` + `~/.config/opencode` into
   `agents/opencode`; move transcripts into the shared folders and link them.
2. The five profiles whose folders were lost in the Docker → systemd move ("Team Sascha", …) become
   accounts **without** a login, marked "Sign in again".
3. Clean cut for agent sessions: their tmux TUIs are stopped and their rows removed (the CLIs'
   transcripts stay in the moved agent folders and in the backup, but Socrates does not show
   them). Shell sessions stay terminals. `~/.codex-multi` and `~/.config/opencode-multi-auth` are
   left alone.
4. The old S3 mirror objects are left untouched; the backup writes under a new prefix. Admin offers
   to delete the old objects once the first snapshot exists.

## 9. Tests

- Protocol fakes replace `faketui` for agents: `fakeclaude` (stream-json + control protocol),
  `fakecodex` (app-server JSON-RPC), `fakeopencode` (HTTP+SSE), each scriptable like `faketui`
  (slash commands drive scenarios: stream text, call tools, ask, hit a limit, crash).
- Host tests: kill the server mid-turn → turn completes, browser replays from seq; kill the host →
  resume; reboot simulation → park/resume path; two devices answering one question.
- Backup tests against a fake S3: incremental chunks, retention, GC, restore into an empty folder,
  torn-write safety of SQLite copies.
- e2e scenarios: the ~50 ADAPT scenarios ported, the operator/screen scenarios dropped, new ones
  for chat streaming, offline send, ask_user (phone + desktop), account switch, backup/restore,
  `design`. Real-binary smoke tests (one tiny prompt per agent, cheapest model) gated behind an
  env flag.

## 10. Work packages

Each package: one Opus implementer pass, one Fable review, fixes verified once (PROCESS.md).

| # | Package | Depends on | Content |
|---|---|---|---|
| WP0 | Spikes | – | S1 Claude: switch between two real logins mid-conversation. S2 Codex: resume across two ChatGPT accounts (needs fresh logins; both on this host are expired). S3 OpenCode: several `serve` on one data dir with per-process auth. S4 host in a systemd scope survives `systemctl restart socrates`. Results amend §3/§4. |
| WP1 | Data folder + backup | – | Layout, migration step 1, `internal/backup`, S3 multipart, restore on setup page, Admin backup card, removal of mirror/lease/connect |
| WP2 | Agent host + event model | WP0 (S4) | `socrates agent-host`, socket protocol, journal, parking, reattach, server client, fake adapter |
| WP3 | Claude adapter | WP2 | §3.1 incl. plan mode |
| WP4 | Codex adapter | WP2 | §3.2 |
| WP5 | OpenCode adapter | WP2 | §3.3 |
| WP6 | `ask_user` | WP3–5 | Tool, three transports, continue mode, fallback to message, native-question conversion |
| WP7 | Accounts | WP1, WP3–5 | Model, sign-in flows, API keys, switching, limits per account, recommender |
| WP8 | Chat UI | WP2 (API), then WP6/WP7 | §7.1, ask card + dialog, account picker |
| WP9 | Adapt the rest | WP8 | Activity sources, Home, push/badge, titles, Status, outcomes, PreStart, costs, new-session sheet |
| WP10 | Terminal = Shell only + cleanup | WP9 | Delete operator, digest questions, agent TUI paths, profiles; migration steps 2–4 |
| WP11 | e2e + docs | with every WP, final pass at the end | Fakes, scenarios, README, screenshots, DECISIONS/DESIGN updated |

WP1 and WP2 run in parallel; WP3–5 run in parallel.

## 11. Risks

| Risk | Mitigation |
|---|---|
| Protocols marked experimental change between CLI versions | Pin known-good versions in the catalog, generate Codex schema on update and diff it, real-binary smoke test in the update script before switching |
| Account switch breaks a Codex conversation | Spike S2; fallback handoff thread, explicit notice |
| Feature parity with the TUIs (commands the web UI does not know) | Slash-command palette passes unknown commands through; Shell terminal remains for anything else |
| Many processes | Parking idle chats; at most the active ones run |
| Anthropic's usage rules | Claude login only through Claude's own flow, unmodified binary; each subscription used by its owner |

## 12. Open questions

All questions of 2026-10-23 are answered and recorded in §1. Still open, to be settled by WP0:
the Codex cross-account mechanism (S2) and the OpenCode multi-process layout (S3). S2 needs two
working ChatGPT logins on this host (both existing ones are expired) — the product owner signs in
when WP0 starts.
