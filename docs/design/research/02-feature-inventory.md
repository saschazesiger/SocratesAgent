# 02 — Feature inventory of Socrates (as of `225a737`, 2026-10-23)

Purpose: a complete list of what exists today, so a rebuild that makes the agent UI web-native
(Claude Code `stream-json`, `codex app-server`, `opencode serve`) and keeps a plain terminal loses
nothing by accident.

Read-only research. Sources: `internal/server/*.go` (non-test), `internal/termux`, `internal/harnesses`,
`internal/catalog`, `internal/openrouter`, `internal/googletts`, `internal/webpush`, `internal/tunnel`,
`internal/proc`, `internal/config`, `internal/store`, `internal/mirror`, `internal/costs`, `internal/web`
(html/js/css, `sw.js`, `manifest.json`), `main.go`, `storage.go`, `docs/design/*.md`, `README.md`,
`e2e/README.md`, `e2e/run.mjs`, `e2e/harness.mjs`, `e2e/fakebin/*`, plus `git` history where noted.
Anything marked **(inferred)** is my reading, not stated in code or docs.

## Legend

| Tag | Meaning |
|---|---|
| **KEEP** | independent of terminal vs. web; carry over as is (maybe re-skinned) |
| **ADAPT** | the concept stays, the implementation changes (how is said) |
| **OBSOLETE** | not needed once agents are protocol-driven (why is said) |
| **TERMINAL-ONLY** | still needed, but only for the plain terminal (Shell) feature (and VS Code, which also rides tmux) |

Harness ids: `shell`, `claude`, `codex`, `opencode`, `vscode` (code-server, a "tool" like Shell).
`harnesses.IsTool` / `js/harnesses.js TOOLS = ['shell','vscode']`: tools have no model, no activity,
no digest, no outcome, no PreStart.

---

## 1. Executive summary

- The app today is: tmux substrate (`internal/termux`) + per-viewer `tmux attach` PTY + a custom
  binary WebSocket (`socrates.term.v1`) + xterm.js. Everything "smart" about agents is **screen
  scraping or side-channel reading**: an activity detector (Claude's `~/.claude/sessions/<pid>.json`,
  Codex's pane title, OpenCode's SSE `/event`, screen regexes, output quiescence), a digester
  (LLM reads `capture-pane` text and extracts summary + question), an **answer operator** (LLM
  drives keystrokes into a TUI menu, with a large server-side policy to keep it honest), a spoken
  Status (LLM summary of the screen), a titler (LLM names from the screen), and a turn-outcome
  judge.
- With structured protocols most of the scraping becomes **events**: activity = turn lifecycle
  events; questions/permissions = structured requests answered directly; the operator and most of
  `digest.go`'s question extraction disappear. The ~4.3 k-line `operator.go`, ~1.6 k `digest.go`
  question part, ~2.7 k `activity.go` screen layers, `osc.go`, theme forcing and the keyboard
  plumbing for agent panes are the big OBSOLETE blocks.
- Everything around it is KEEP: auth, S3 mirror + vault + lease, connect/setup, tunnel, Web Push +
  badge, PWA/manifest/sw.js, profiles + headroom, usage limits, cost ledger, PreStart + scheduler,
  session filing (status shelves), folders sidebar, Home cards, events stream, voice (STT + TTS),
  admin, updates, diagnostics, watchdog.
- The terminal transport (ring, seq replay, exactly-once input, takeover, grace, size ownership,
  journal, OSC responder, key bar, touch scroll, clipboard) is TERMINAL-ONLY and should be kept
  intact for Shell (and VS Code's host session).
- `net.js` `Outbox` and `LiveStream` are **currently dead code** (exported via `api.js`, used by
  nothing); they were the chat-era message queue and SSE client (`git show d65f276^:internal/web/static/js/chat.js`
  used `new Outbox('messages', …)` and `new LiveStream(…)`). They are exactly what a chat UI needs.
- **Git history holds the previous protocol adapters**: `git show 5d7b420^` has `internal/agenthost`
  (a detached per-session host process with journal and a socket protocol), `internal/engine`, and
  `internal/harness/{claude,codex,opencode}` (stream-json wire, codex JSON-RPC app-server client,
  OpenCode HTTP+SSE client) with protocol fakes `internal/harness/fakes/{fakeclaude,fakecodex,fakeopencode}`.
  Deleted on 2026-09-02 (commits `5d7b420`, `d65f276`). Worth mining, not resurrecting blindly
  (CLI versions moved: Claude 2.1.258→2.1.303, Codex 0.152, OpenCode 1.17/1.18).

---

## 2. Server subsystems (`internal/server` and friends)

### 2.1 Process, bootstrap, supervision

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Main / subcommands | `serve`, `journal-sink` (tmux `pipe-pane` sink with rotation), `tmux-hook` (hook body → unix socket) | `main.go`, `reexec_*.go` | KEEP (`serve`); `journal-sink`/`tmux-hook` TERMINAL-ONLY |
| Storage bootstrap | decides connect mode vs. normal vs. degraded start from bucket (`openStorage`), re-exec after connect | `storage.go` (root), `internal/server/connect.go` | KEEP |
| Connect mode server | unauthenticated (fresh dir) / password-gated page: `POST /api/storage/connect|takeover|retry`, everything else 503 / redirect `/setup` | `internal/server/connect.go`, `setup.html` | KEEP |
| systemd watchdog | `sd_notify` pings fed only while `/api/health` answers; phases Starting/Serving/Stopping | `internal/sdnotify` | KEEP |
| Updates card | reads `updates.json`/`updates.log` from `socrates-update.service`, `POST /api/updates/run` starts the unit, writes next-trigger | `internal/server/updates.go`, `scripts/update.sh`, `deploy/*` | KEEP |
| tmux scope supervision | tmux server started via `systemd-run --scope --unit=socrates-tmux` (+ `OOMPolicy=continue`) so a service restart is not a reboot | `internal/termux/tmux.go`, DESIGN §A.12 | TERMINAL-ONLY — but the **requirement** (agent processes survive a Socrates restart/update) must be re-solved for protocol agents: ADAPT into an agent-host or scope-run supervisor (inferred: Claude `stream-json` and `codex app-server` speak over stdio, so they need a holder process that outlives the server; `opencode serve` is HTTP and can simply be left running and reconnected to) |
| `internal/proc` | `Setpgid` + group SIGTERM helper | `internal/proc/proc.go` | KEEP (useful for supervising protocol children) |

### 2.2 Auth and security

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Password + logins | PBKDF2-SHA256 (210k) hash, `socrates_session` cookie (30 d, SameSite=Strict), `logins` table, login throttle per IP, `clientIP` with forwarder handling, logout, change password (revokes all logins + event streams) | `auth.go`, `store/logins.go` | KEEP |
| `s.auth` wrapper, `sameOrigin` | every API route; WS `OriginPatterns=[r.Host]` | `auth.go`, `ws.go`, `events.go` | KEEP |
| WS handshake ceilings | 20/min/IP for terminal sockets, 240/min/login for event stream | `ws.go allowHandshake/allowRate` | KEEP (apply to new chat sockets too) |
| Event-stream revocation | close code **4401** "signed out" when the login is gone (checked every tick and on revoke) | `events.go` | KEEP |

### 2.3 Sessions: rows, lifecycle, filing

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Session row | id, client_id (idempotent create), title + title_source, harness, model, effort, workdir + mode, options JSON (profile, ultracode, …), tmux_name, cli_session_id + state (`none/pending/known/verified/lost`), state (`starting/running/exited/needs_resume/failed/prestart`), exit_status, fail_reason, resumed + resume_count, cols/rows, created/updated/last_attached/active_at, status (`in_progress/review/pending/done/background/""`), status_at, start_at | `store/sessions.go`, `store/store.go` (schema v9) | ADAPT: keep almost all; cols/rows and tmux_name become terminal-only fields; add event-log cursor / conversation fields (inferred) |
| Create (idempotent on `client_id`) | `POST /api/sessions`: validate harness/workdir/model/effort/profile, resolve workdir, plan, start in tmux; `prestart:true` makes a prepared row | `sessions.go handleCreateSession/checkLaunch/launch` | ADAPT (launch goes to protocol runner for agents, tmux for shell) |
| Workdir policy | `dynamic` (fresh dir under workspace root), `preset` (admin list), `custom` (only if `allow_custom`; blocked roots); enforced server-side | `harnesses/workdir.go` | KEEP |
| Ensure / resume after reboot | lazily relaunch on open: `needs_resume` → CLI resume with stored id (`--resume`, `codex resume`, `--session`), Shell → fresh shell in same dir; resume note "resumed after restart" (`ack-resume`) | `termux/ensure.go`, `sessions.go ensure`, `ws.go resumeNotice` | ADAPT (resume through protocol: Claude `--resume` in stream-json mode, Codex `thread/resume`, OpenCode session id — inferred) |
| Restart | `POST …/restart` (from exit overlay), refuses while running | `sessions.go`, `termux/ensure.go Restart` | ADAPT |
| Exit / crash surfacing | `pane-died` hook → `exit` frame + overlay with status; `failed` with tmux stderr | `termux/hooks.go`, `ws.go onSessionExit` | ADAPT (process exit → event) / TERMINAL-ONLY for shell |
| Adoption / "Recovered session" | on start, re-adopt `soc_*` tmux sessions; one without a row becomes "Recovered session", never killed | `termux/adopt.go` | TERMINAL-ONLY (agent equivalent: re-attach to surviving agent hosts — ADAPT, inferred) |
| Lifecycle poll | `list-panes -a` every 2 s, 2 misses → `needs_resume` | `termux/adopt.go Poll` | TERMINAL-ONLY |
| Delete | kill tmux session, delete row/journal, keep workdir (`workdir_kept`); stops operator first | `sessions.go`, `done.go deleteSession`, `termux/manager.go Delete` | ADAPT |
| Status filing ("shelves") | in_progress/review/pending/done/background via `POST …/file`; `done` parks (closes terminal) and is swept after **24 h** (`DoneRetention`); background = service, never notified as Finished; a session that starts working again goes back to in_progress | `sessions.go handleSetStatus`, `done.go park/sweep` | KEEP |
| Rename | `PATCH /api/sessions/{id}` `{title}` | `sessions.go` | KEEP |
| Mark read | `POST …/read` and WS `read` frame clear unread | `sessions.go`, `ws.go` | KEEP |
| Journal download | `GET …/journal` last 16 MB of raw bytes | `sessions.go handleJournal`, `termux/journal.go` | TERMINAL-ONLY (agent equivalent: export transcript/event log — ADAPT, inferred) |
| Uploads | `POST …/upload` multipart ≤64 MB/20 files into `<workdir>/.socrates/uploads/` with `.gitignore`; page pastes paths onto the prompt | `upload.go`, `js/transfer.js` | ADAPT for agents (attach as message parts / image blocks; keep writing to workdir — inferred), KEEP for shell |
| Usage per session | `GET …/usage`: Claude 5 h/7 d windows from `https://api.anthropic.com/api/oauth/usage` with the profile's OAuth sign-in (cached), Codex windows from newest rollout, cost of the conversation | `usage.go` | KEEP (may later come from protocol rate-limit events — Codex app-server emits rate limits, inferred) |
| Size handling | `size` frames, Socrates-owned window sizing (`manual` policy: latest attached/resized viewer owns size), `cols/rows` validation | `termux/session.go`, `ws.go onResize` | TERMINAL-ONLY |

### 2.4 Harness launch (`internal/harnesses`) — see §4 for the full flag list

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Harness interface + registry | `Kind/Label/DefaultBinary/VersionArgs/Plan/ResumePlan/VerifyCLISession/DiscoverModels` → `LaunchPlan{Argv, Env, Cwd, Files, CLISession, Discover, Port}` | `harness.go`, `plan.go` | ADAPT: plans become protocol-mode argv (`claude -p --input-format stream-json --output-format stream-json …`, `codex app-server`, `opencode serve`) (inferred) |
| Fixed launch policy | permission bypass, trust pinning, remote control off, light theme, env | `claude.go`, `codex.go`, `opencode.go`, `shell.go`, HARNESS-POLICY.md | ADAPT (see §4: theme/TUI bits OBSOLETE, bypass/trust/RC KEEP) |
| Session-id discovery | Claude: preset `--session-id <uuid>`; Codex: rollout watcher (originator stamp `CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates`, 15 min budget, sqlite index fallback); OpenCode: `GET /session` on its TUI server | `codex_discover.go`, `opencode_discover.go`, `discovery.go` | OBSOLETE for Codex/OpenCode (protocols return the thread/session id directly — inferred); Claude still uses `--session-id` / `system init` event (inferred) |
| `VerifyCLISession` | transcript exists before `--resume` (Claude glob over projects; Codex rollout/index; OpenCode db/API) | same | KEEP |
| Initial prompt via file | prompt (≤100 KB) written to `<sessiondir>/initial-prompt`, `sh -c` wrapper reads, deletes, execs CLI (tmux ~16 KB message cap); Claude single-word guard (`update` subcommand trap) | `prompt.go` | OBSOLETE for agents (first prompt is just the first protocol message), keep only if shell ever needs it |
| Profiles | per-profile `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `XDG_DATA_HOME` under `<data>/profiles/<id>`; settings/skills/plugins/MCP linked from default; shared user-scope MCP servers copied into profile `.claude.json`; sign-in readers (`ClaudeSignIn`, `CodexSignIn`, `OpenCodeProviders`) | `profiles.go` | KEEP |
| Model discovery | Claude: curated static list (opus, sonnet, haiku, fable, best, opusplan, …; efforts low..max); Codex: `codex debug models`; OpenCode: `opencode models [--json]` | `claude_models.go`, `codex_models.go`, `opencode_models.go` | KEEP (could switch to `model/list` RPC on app-server and `GET /provider` on opencode — inferred) |
| VS Code (code-server) | one editor per project folder, runs in tmux like a session, socket `ide.sock`, per-folder user-data-dir | `harnesses/vscode.go`, `server/vscode.go` | KEEP (rides the terminal substrate: TERMINAL-ONLY infrastructure) |
| State paths | `ClaudeConfigDir`, `CodexHome`, `OpenCodeDBPath` for diagnostics | `statepaths.go` | KEEP |

### 2.5 Catalog

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Harness catalogue | per harness: installed?, version (`--version`, 5 s), binary path, discovered models + efforts, admin short list (`Picks`), cached in kv `agents_catalog` (schema 3, TTL 30 min), invalidated by `updates.json` change; `GET /api/harnesses` (+ workspace root, presets, allow_custom, default harness, tmux ok), `POST /api/harnesses/refresh` | `internal/catalog/catalog.go`, `server/harnesses.go` | KEEP |

### 2.6 Terminal substrate (`internal/termux`)

| Piece | What it does | Class |
|---|---|---|
| `tmux.go`, `conf.go`, `apply.go`, `detect.go` | own tmux server on `<data>/tmux.sock`, generated conf (history-limit, mouse, extended-keys, default-terminal, allow-passthrough, remain-on-exit-format), version floor 3.3, live apply of terminal settings, `window-size` per window | TERMINAL-ONLY |
| `manager.go` | Create/StartPrestart/Relaunch/Attach/Viewers/Park/Delete, `soc_<id>` names, per-session locks | TERMINAL-ONLY (agent sessions need an equivalent manager — ADAPT) |
| `session.go`, `pty.go` | per-viewer `tmux attach` on a PTY (creack/pty), viewer list ordered by recency, size owner | TERMINAL-ONLY |
| `ring.go` | 1 MiB per-viewer replay ring with byte seq; writer pulls (backpressure) | TERMINAL-ONLY (concept reused: ADAPT into a per-session event log with seq for chat replay, inferred) |
| `journal.go` | append-only byte journal via `pipe-pane` → `socrates journal-sink`, 64 MB rotation ×1, `TailJournal` | TERMINAL-ONLY |
| `hooks.go` | global tmux hooks `pane-died`, `session-closed` → `tmux-hook` → unix socket | TERMINAL-ONLY |
| `osc.go` | Responder answers tmux's attach queries (OSC 10/11 colours white/near-black, DA1/DA2/XTVERSION, CSI 996n, 14t/18t) and strips replies from input | TERMINAL-ONLY (agent TUIs gone → no theme forcing needed) |
| `screen.go` | `CapturePane`, `VisiblePane`, `StyledPane` (with colours, for OpenCode's colour-only highlight), `SendKeys` (fixed key vocabulary) | OBSOLETE for agents (only the digester/operator/status/titler use it); TERMINAL-ONLY if a shell Status/summary is kept |
| `activity.go` | 1 s detector, states `unknown/busy/idle/waiting/tasks`, layers L1 exact (Claude session file `status`/`waitingFor`, Codex pane title spinner/"Action Required", OpenCode SSE `session.status`/`permission.asked`/`question.asked`, Shell `pane_current_command`+children), L2 quiescence (`window_activity`: ≤2 s busy, ≥30 s idle), L3 screen regexes; arbitration, debounce (idle 2 ticks, unknown 3), runaway guard (120 s), waiting sticky; unread rule; `MovesRow`; `seq` counter from µs clock; persisted unread in kv `activity.unread`; `WaitIdle`, `NoteInput/NoteTyped/MarkRead/ResetActivity`, `SubscribeActivity` | ADAPT: the **state model, unread rule, seq, MovesRow, debounce semantics and persistence KEEP**; the **sources** become protocol events (Claude `system/init`, `assistant`, `result` messages; Codex `turn/started`, `turn/completed`, approval requests; OpenCode `session.status`, `permission.asked`, `question.asked` — inferred). Screen layers, Codex title parsing, Claude pid/session-file lookup OBSOLETE. Shell has no activity (tools) |
| `ensure.go` | Ensure/Restart/ResumeNote/OpenCodeAccessOf/RearmCLIWatch | ADAPT |
| `shellquote.go` | quoting for tmux-run shell strings | TERMINAL-ONLY |

### 2.7 Transport (`internal/server/ws.go`) — `socrates.term.v1`

Binary output frames (`0x01`+u64 seq+bytes), binary input frames (`0x02`+u64 input_seq+bytes), JSON
control both ways; `viewer` id per tab (sessionStorage); 90 s viewer grace keeps the PTY; same-viewer
handshake **takes over** (old socket closed 1012); `since` replay from ring, else fresh attach with
`replay_from:0`; exactly-once input with `input_ack` batching (100 ms), fresh-viewer anchoring,
gap rejection; ping 15 s / timeout 10 s; slow writer 30 s → 1013; max 8 viewers; 3 input failures
end the socket; `onlyFocusReports` filtering; the socket also carries `activity`, `title`, `status`,
`answer`, `outcome`, `digest` frames for the attached session.
**Class: TERMINAL-ONLY** (keep verbatim for Shell). Its *guarantees* (exactly-once input, visible
loss, takeover, replay) must be re-provided for chat — ADAPT (see §8).

### 2.8 Event stream (`internal/server/events.go`) — `socrates.events.v1`

WebSocket on every page of `index.html` while visible (+30 s hidden grace). Frames: `hello` (full
state: sessions, rev, digests, answers, outcomes, now), `sessions` (whole list on store `Rev()`
change, watcher 300 ms only while subscribed, coalesced 250 ms, `poke()`), `activity`, `title`,
`digest`, `answer`, `outcome`, `tick` (15 s, re-validates login), `pong`. Per-subscriber buffer
64 + 5/session, slow subscriber dropped ("too slow"), never blocks the detector. `GET /api/home` is
the same content as a poll fallback (gzipped).
**Class: KEEP** (it is terminal-independent; frames `digest`/`answer` change shape — see below).
It is the natural place to add per-session chat events or a separate per-session chat socket (inferred).

### 2.9 LLM-assisted features (all via OpenRouter, `internal/openrouter`)

| Feature | What it does | Files | Class |
|---|---|---|---|
| OpenRouter client | Chat (streamed/non-streamed), Models, CheckKey, Transcribe (audio-capable chat model or dedicated transcriber); `StatusError` mapping | `internal/openrouter/client.go` | KEEP |
| Dictation STT | `POST /api/voice/transcribe` (WAV 16 kHz mono from the page; language hint; `TranscribeModel`) | `server/voice.go`, `js/voice.js`, `js/dictate.js` | KEEP (server); the page side ADAPTs: transcript goes into the chat composer instead of being typed into the pane |
| Spoken Status | `POST /api/sessions/{id}/status`: captures 120 lines, `StatusModel` summarises in `voice.language`, phases streamed as `status` frames (reading/asking/speaking/answer) to the ticker, page reads it out via TTS | `server/assist.go handleSessionStatus`, `js/assist.js` | ADAPT: summarise from the structured transcript / last assistant message instead of the screen (or simply read the last answer out loud — inferred); keep for Shell from the screen (TERMINAL-ONLY variant) |
| Session titles | `titleDriver`: names a session once after it first answers (30 s wait), from the screen; `POST …/title` retitles from first prompt + screen (`unnamed` mode names a prepared session from its draft); `title` frames with `seq` | `server/assist.go`, `js/session.js titleFrame` | ADAPT: name from first user message + first answer text (OpenCode and Codex may supply their own titles — inferred) |
| Digests (Home summaries + question extraction) | per coding session, `HomeModel` reads `CapturePane(150)` (+ Claude transcript `AskUserQuestion` input) → `{summary, context, question{source menu|text, kind choice|multi|confirm|plan|text, items, options}}`; triggers on state edges, 4 s screen-hash looks while waiting, 60 s while busy; concurrency 3 (2 bulk); `screenHash` with tick masking; staleness clearing; rev from µs clock; no-key error; `POST …/digest` "read again" | `server/digest.go` | ADAPT/OBSOLETE split: **question extraction OBSOLETE** (questions/permissions arrive structured: Claude `AskUserQuestion` tool_use / `can_use_tool` control requests, Codex approval requests, OpenCode `question.asked`/`permission.asked` — inferred). **One-line summary for Home** ADAPT (model reads the transcript, or derive from last assistant text/todos). A prose question at turn end ("Which colour?") still needs a model or heuristic — ADAPT |
| Answer operator | `POST/GET …/answer`, `…/answer/cancel`: LLM drives keys into the TUI to enter the person's answer; budgets (8 steps, 4 min, 45 s/step, 8 actions), fixed key vocabulary, `answerPolicy` (only person's words or row digits typed), screen-hash guards, done rules, OpenCode colour-only highlight via `StyledPane`, run object broadcast | `server/operator.go` (4.3 k lines) | **OBSOLETE**: the answer is sent as a protocol reply (tool_result / approval decision / `POST /question/<id>/reply`). Keep the *UX contract* (run phases, "answered elsewhere", two-device conflict, 409 on stale question rev) — ADAPT into a thin "answer request" with idempotency |
| Turn outcome | optional (`openrouter.outcomes`): at a turn's end the digest also returns `{kind complete|partial|needs_input|blocked, reason, todos}`; Claude turn read from transcript (`readClaudeTurn`: last prompt, answer tail, TaskCreate/TaskUpdate list); notifications held ≤45 s for it; kept in kv `outcomes` (local only) | `server/outcome.go`, `js/outcome.js` | ADAPT: input from structured transcript for all three harnesses (todos from Claude Task tools / Codex plan updates / OpenCode todowrite — inferred) |
| Asking set | which sessions are asking (Home's rule, mirrored server-side) → push badge count + tray cleanup | `server/asking.go` | ADAPT (rule = "has an open structured request" + prose question) |

### 2.10 Notifications

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Web Push sender | VAPID keys (kv `push.vapid`), subscriptions (kv `push.subscriptions`, max 32), RFC 8291 encryption, stdlib only, ErrGone pruning | `internal/webpush/webpush.go` | KEEP |
| Pusher | applies the page's chime rule to committed activity changes; question push waits ≤8 s for digest words; Finished push waits ≤45 s for outcome; topic per session; payload `{title, body, id, kind question|finished|started|test, badge, asking, at}`; TTL 6 h; `/api/push`, `/subscribe`, `/unsubscribe`, `/test` | `server/push.go` | KEEP (body text for questions now comes straight from the structured question — ADAPT the wait) |
| Scheduler announcements | "Started: …" / "Scheduled start failed: …" | `server/schedule.go announce` | KEEP |

### 2.11 Profiles, limits, costs

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Profiles API | list (with logins per agent + `recommended`), create, rename, delete (409 while used), reorder (kv `profiles`, `profiles.order`) | `server/profiles.go` | KEEP |
| Headroom recommender | picks the profile with most allowance per hour-to-reset (7 d rate scaled by 5 h), used by the sheet and by `profile_auto` at PreStart start | `server/headroom.go` | KEEP |
| Cost ledger | scans every Claude/Codex/OpenCode transcript under every profile every 2 min (incremental), prices per model/hour (`internal/costs`), ledger tables `cost_convs`, `cost_hours`, `cost_seen`; mirrored per conversation to bucket; `GET /api/costs` | `server/costs.go`, `internal/costs/*`, `store/costs.go` | KEEP (independent: reads CLI files; protocol mode still writes the same transcripts — inferred, verify for `codex app-server` and `opencode serve`) |

### 2.12 PreStart and scheduler

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Prepared sessions | `state=prestart` row, settings PATCH (`/prestart`), draft with optimistic rev (`GET/PUT …/draft`, 409 conflict with other text, 413 > 100 KB), `POST …/start` idempotent, same `launch` path as create, `profile_auto`, `ultracode`; only claude/codex/opencode; `started()` wrapper refuses terminal routes for prepared rows | `server/prestart.go`, `store/prestart.go` | KEEP (start delivers the draft as first protocol message instead of the argv file — ADAPT) |
| Scheduler | loop ≤10 s, claims by clearing `start_at`, starts once, never retries, gated by mirror lease (`SetAutoStartGate`) | `server/schedule.go` | KEEP |

### 2.13 Storage of record (S3)

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| SQLite working copy | schema v9, kv table, sessions, logins, drafts, mirror_pending, cost tables; `Rev()` counter drives the event stream; pre-migration backups | `internal/store/*` | KEEP (new tables for chat events/messages would need a mirror decision — inferred) |
| Mirror | write-through to bucket within ~5 s, objects `kv/<key>`, `logins.json`, `sessions/<id>.json`, `drafts/<id>.json`, `costs/<harness>/<conv>.json`; hydrate on new machine; degraded start; lease (`lease.json`, 30 s renew, 90 s TTL, fencing, takeover with password); `meta.json` salt/check | `internal/mirror/*`, `store/mirror.go` | KEEP |
| Vault | AES-256-GCM, PBKDF2-SHA256 600k, object name as AAD | `internal/vault` | KEEP |
| S3 client | hand-written SigV4, `x-amz-content-sha256`, unquoted ETag `If-Match` | `internal/s3` | KEEP |
| Storage admin API | `GET /api/storage`, `POST /test`, `POST /sync`, `PUT /credentials`; banner summary in `/api/state` | `server/storage.go`, `js/storage.js` | KEEP |

### 2.14 Admin, diagnostics, tunnel, misc

| Subsystem | What it does | Files | Class |
|---|---|---|---|
| Settings doc | `GET/PUT /api/settings` (whole document + defaults), `Normalize`, migration "ignore old keys"; `GET /api/preferences` (language, terminal scrollback/font_size/webgl) | `server/admin.go`, `internal/config` | KEEP (terminal prefs TERMINAL-ONLY) |
| Diagnostics ("Setup check") | `POST /api/diagnostics`: tmux checks, recovered sessions, per-harness binary/version, harness state dirs writable, Claude sign-in, tunnel, disk free, voice, updates freshness | `server/admin.go`, `disk.go` | KEEP (tmux rows TERMINAL-ONLY; add protocol-mode probes — inferred) |
| tmux card | `GET /api/tmux` (installed, path, version, ok, reason; 5 s cache). No installer endpoint any more (README: install.sh installs tmux) | `server/tmux.go`, `termux/detect.go` | TERMINAL-ONLY |
| Cloudflare tunnel | quick (`trycloudflare.com`) or token tunnel, downloads cloudflared into data dir, status/start/stop/install routes | `internal/tunnel/*`, `server/tunnel.go` | KEEP |
| Google TTS | `POST /api/voice/speak` (MP3, Standard voices per language, rate clamp, byte-limit truncation), `POST /api/voice/check`; `SOCRATES_GOOGLE_TTS_URL` override for tests | `internal/googletts`, `server/voice.go` | KEEP |
| StripANSI | used by diagnostics/status text cleanup | `server/admin.go` | KEEP/TERMINAL-ONLY |
| Health | `GET /api/health` (version; update smoke test) | `server.go`, `connect.go` | KEEP |
| Static/PWA serving | `/static/*` (build-stamped), `/sw.js` (VERSION substituted), favicons, page routes `/`, `/admin`, `/costs`, `/login`, `/setup`; gzip for `/api/home`, `/api/sessions` | `server.go`, `internal/web/embed.go` | KEEP |

---

## 3. HTTP / WebSocket / SSE endpoints (complete)

All `/api/*` except state/setup/login/logout/health are behind `s.auth`. "started" = refused 409 for
a prepared row.

| Method + path | Purpose | Class |
|---|---|---|
| `GET /` `/admin` `/costs` `/login` `/setup` | pages | KEEP |
| `GET /static/*`, `/sw.js`, `/favicon.png`, `/apple-touch-icon(-precomposed).png` | assets | KEEP |
| `GET /api/health` | liveness + version | KEEP |
| `GET /api/state` | mode (normal/connect), setup needed, signed in, storage summary | KEEP |
| `POST /api/setup` | first-run password (+ optional OpenRouter key, tunnel) | KEEP |
| `POST /api/login`, `POST /api/logout` | sign in/out | KEEP |
| `POST /api/storage/connect` `/takeover` `/retry` | connect mode only | KEEP |
| `GET /api/storage`, `POST /api/storage/test`, `POST /api/storage/sync`, `PUT /api/storage/credentials` | storage card | KEEP |
| `GET /api/sessions` | list `{sessions, rev}` (sessionView adds activity, cli_session_state, resume info, draft meta) | KEEP |
| `POST /api/sessions` | create (idempotent `client_id`), `prestart:true` | ADAPT |
| `GET /api/sessions/{id}` | one | KEEP |
| `PATCH /api/sessions/{id}` | rename | KEEP |
| `POST /api/sessions/{id}/title` | retitle from prompt+screen / name prepared from draft | ADAPT |
| `DELETE /api/sessions/{id}` | delete, keep workdir | ADAPT |
| `POST /api/sessions/{id}/file` | set status (shelf) | KEEP |
| `POST /api/sessions/{id}/resume` | resume (started) | ADAPT |
| `POST /api/sessions/{id}/restart` | restart (started) | ADAPT |
| `PATCH /api/sessions/{id}/prestart` | change prepared settings | KEEP |
| `GET/PUT /api/sessions/{id}/draft` | prepared prompt draft | KEEP |
| `POST /api/sessions/{id}/start` | start prepared | KEEP (delivery ADAPT) |
| `POST /api/sessions/{id}/ack-resume` | clear "resumed" banner | KEEP |
| `POST /api/sessions/{id}/read` | clear unread | KEEP |
| `GET /api/sessions/{id}/usage` | limits + cost | KEEP |
| `POST /api/sessions/{id}/status` | spoken status | ADAPT |
| `POST /api/sessions/{id}/digest` | read again | ADAPT (summary only) |
| `POST /api/sessions/{id}/answer`, `GET …/answer`, `POST …/answer/cancel` | operator | OBSOLETE as operator; replace by a structured "reply to request" endpoint (inferred) |
| `GET /api/sessions/{id}/journal` | raw journal download | TERMINAL-ONLY |
| `POST /api/sessions/{id}/upload` | files into workdir | ADAPT/KEEP |
| `GET /api/sessions/{id}/ws` | **WS** `socrates.term.v1` | TERMINAL-ONLY |
| `GET /api/events` | **WS** `socrates.events.v1` | KEEP |
| `GET /api/home` | poll fallback of events hello | KEEP |
| `GET /api/costs` | cost ledger | KEEP |
| `GET /api/harnesses`, `POST /api/harnesses/refresh` | catalogue | KEEP |
| `GET/POST /api/profiles`, `PUT /api/profiles/order`, `PATCH/DELETE /api/profiles/{id}` | profiles | KEEP |
| `GET/PUT /api/settings`, `POST /api/settings/password` | admin | KEEP |
| `GET /api/preferences` | page runtime prefs | KEEP (terminal part TERMINAL-ONLY) |
| `POST /api/diagnostics` | setup check | KEEP |
| `GET /api/tmux` | engine card | TERMINAL-ONLY |
| `GET /api/updates`, `POST /api/updates/run` | updates card | KEEP |
| `GET /api/tunnel`, `POST /api/tunnel/start|stop|install` | tunnel | KEEP |
| `POST /api/voice/transcribe|speak|check` | voice | KEEP |
| `GET /api/push`, `POST /api/push/subscribe|unsubscribe|test` | web push | KEEP |
| `/ide/{id}/` (all methods + WS), `POST /api/ide`, `POST /api/ide/blank` | code-server proxy, open folder IDE, blank IDE | KEEP |

**SSE:** none on the server today. The tmux-installer SSE (`/api/tmux/events`, DESIGN §D.9) was
removed (the installer moved to `scripts/install.sh`). Outbound SSE is *consumed* by the server:
OpenCode's `GET /event` (activity) and OpenRouter streaming.

### 3.1 Frames, `socrates.term.v1` (TERMINAL-ONLY)

Server→client JSON: `hello{session,size,replay_from,input_ack,viewer_fresh}`, `size{cols,rows,by}`,
`state`, `exit{status,at}`, `notice{kind:resumed|…}`, `input_ack{seq}`, `pong`, `error{message,fatal}`,
plus session-scoped `activity`, `title`, `status`, `answer`, `outcome`, `digest`. Client→server:
`resize`, `lag`, `ping`, `read`, `bye`. Client handles also `input_lost`.

### 3.2 Frames, `socrates.events.v1` (KEEP)

`hello`, `sessions`, `activity`, `title`, `digest`, `answer`, `outcome`, `tick`, `pong`; client sends
`ping`. Close 4401 signed out, PolicyViolation "too slow".

---

## 4. Harness launch policy (HARNESS-POLICY.md + `internal/harnesses`)

Admin exposes only: enabled, binary path (discovered/overridable), model short list (`Models []ModelPick{id, effort}`),
default model, default effort (Claude, Codex). Everything else is compiled-in policy. Shared env
(`baseEnv`): `TERM`, `COLORTERM=truecolor`, `COLORFGBG=0;15`, `TMUX=""`, `SOCRATES_SESSION=<id>`,
`SOCRATES=1`, plus profile env. Every session runs as the service user (root) directly on the host.

### 4.1 Permissions bypass and remote control — the answer to "permissions bypass? remote control?"

- **Permissions are always bypassed, with no switch.** Rationale: a permission prompt in an unwatched
  pane on a phone in a car is a failed session. README Security: "coding CLIs that run unattended,
  as root, with approvals and sandboxes bypassed and no way to turn that off".
- **Remote control is always off.** Claude: never `--remote-control`, plus `disableRemoteControl:true`
  in generated settings and `remoteControlAtStartup:false` (top level and `projects[<cwd>]`) in
  `.claude.json`. Codex: never `--remote` / `--remote-auth-token-env`. OpenCode: `share:"disabled"`.

### 4.2 Claude Code

```
<bin> --session-id <uuid> | --resume <id>  [--name <title>] [--model <m>] [--effort low|medium|high|xhigh|max]
      --dangerously-skip-permissions --settings <sessiondir>/claude-settings.json
      [-- <initial prompt>]          (first plan only; trailing space for a one-word prompt)
```
- Settings file (0600): `skipDangerousModePermissionPrompt:true`, `disableRemoteControl:true`,
  `cleanupPeriodDays:90`, `ultracode:<bool per session>`, `env:{…pane env…}`.
- Env: `IS_SANDBOX=1` when euid 0 (else the bypass is refused as root), `ANTHROPIC_API_KEY=""`,
  `CLAUDE_CODE_TMUX_TRUECOLOR=1`, `CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1`, `CLAUDE_CODE_NO_FLICKER=1`;
  profile `CLAUDE_CONFIG_DIR`. Auto-updater left on.
- Global config pin (`~/.claude.json` or profile's): `theme:"light"`, `projects[realpath(cwd)].hasTrustDialogAccepted:true`,
  `remoteControlAtStartup:false`, shared MCP servers for non-default profiles.
- No `--fork-session`. `--resume` preferred over `-c`.
- **Rebuild:** KEEP bypass, `IS_SANDBOX`, empty API key, settings file keys (minus env-for-TUI), trust
  pin, RC off, `cleanupPeriodDays`, `ultracode`, `--session-id/--resume`, `--model/--effort`, profiles.
  OBSOLETE: `theme`, `TMUX_TRUECOLOR`, `NO_FLICKER`, `DISABLE_TERMINAL_TITLE`, `COLORFGBG`, prompt file.
  New (inferred): `-p --input-format stream-json --output-format stream-json --verbose`, possibly
  `--permission-prompt-tool`/control protocol to receive `AskUserQuestion`/plan approvals, `--include-partial-messages` for streaming.

### 4.3 Codex

```
<bin> [resume <id>] --strict-config -C <cwd> [-m <model>] [-c model_reasoning_effort="<e>"]
      -c projects={"<realpath cwd>"={trust_level="trusted"}} -c tui.theme="light-gray"
      -c check_for_update_on_startup=false --no-alt-screen --dangerously-bypass-approvals-and-sandbox
      [-- <initial prompt>]
```
- Env: `CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates` (rollout discovery), profile `CODEX_HOME`.
- Efforts: `minimal low medium high xhigh` (per-model lists from `codex debug models`).
- **Rebuild:** KEEP bypass (as app-server `approvalPolicy: never` + `sandbox: danger-full-access`
  on thread start — inferred), trust override, strict config, no update check, model/effort, no `--remote`.
  OBSOLETE: `tui.theme`, `--no-alt-screen`, rollout-watcher discovery (thread id returned by RPC — inferred).

### 4.4 OpenCode

```
<bin> --port <free loopback port> --hostname 127.0.0.1 [--session <id>] [-m <provider/model>] <cwd>
      [--prompt=<initial prompt>]
```
- Permission object (`*`, bash, edit, write, patch, read, webfetch, websearch, task, todowrite,
  external_directory = `allow`) set twice: `OPENCODE_PERMISSION` and in `OPENCODE_CONFIG_CONTENT`
  (`share:"disabled"`, `autoupdate:false`, model).
- `tui.json` (theme github, mouse, attention off) via `OPENCODE_TUI_CONFIG`.
- Env: `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_DISABLE_TERMINAL_TITLE=1`, `OPENCODE_DISABLE_MODELS_FETCH=1`,
  `OPENCODE_SERVER_USERNAME=opencode`, `OPENCODE_SERVER_PASSWORD=<32 random bytes>` (mandatory: the TUI is
  the whole HTTP server); profile `XDG_DATA_HOME`. No `--print-logs`, no `--fork`.
- **Rebuild:** KEEP permission allow-all, password-protected loopback server, disable autoupdate/models
  fetch, share disabled. OBSOLETE: `tui.json`, terminal title. `opencode serve` instead of the TUI;
  the server already used for discovery and the activity SSE is the protocol (inferred).

### 4.5 Shell (TERMINAL-ONLY) and VS Code

- Shell: `<bin> -l` in the workdir; binary = setting, else `$SHELL`, `/bin/bash`, `/bin/sh`; a profile's
  shell carries all three agents' env (that is how `codex login` / `opencode auth login` are done per profile).
- VS Code: code-server on a private unix socket, one per folder, proxied at `/ide/{id}/`.

---

## 5. Frontend pages

| Page | What | Class |
|---|---|---|
| `index.html` | the app: top bar (menu, collapse, title + regen arrow, status chip, outcome tag, usage strip, Copy/Paste/Upload, Status, Mic pill, keyboard button, ⋮ options), sidebar (New session, Blank IDE, Home, folder list, logout), `#stage` with `#termWrap` (pane, cover overlay, notice, ticker), `#prestart` panel, `#home`; key bar; dialogs: session row menu, options, usage, new-session sheet, recording sheet, copy sheet; `#connSlot`, `#storageBanner`, `#usageLine` | ADAPT: stage gains a chat view for agents; terminal stage stays for Shell |
| `admin.html` | cards: Terminal engine, Updates, Storage, Setup check, Workspace, Profiles, Terminal behaviour, Programs (per harness), Voice, Status & naming (OpenRouter models: transcribe, title, status, home, operator; outcomes switch), Remote access, Password | KEEP (Terminal engine/behaviour TERMINAL-ONLY; operator model picker OBSOLETE) |
| `costs.html` | cost ledger page with filters, tiles, chart, tables, CSV | KEEP |
| `login.html` | sign-in | KEEP |
| `setup.html` | two steps: storage connect (endpoint/region/bucket/prefix/keys/path-style/passphrase, conflict/lease/takeover) and password (+ OpenRouter key, tunnel choice) | KEEP |
| `manifest.json` | standalone PWA, icons, shortcuts **Questions** (`/#questions`) and **Home** (`/#home`) | KEEP |
| `sw.js` | network-first app shell cache per build, push handling, badge, tray cleanup, notification click routing | KEEP (shell list changes) |

Routing (`session.js routeOf/boot`): `#home`/empty → Home; `#<id>` → session (but cold start always
lands on Home); `#q=<id>` → Home + question dialog; `#open=<id>` → session; `#questions` → Home +
oldest question. Login keeps the fragment.

## 6. Frontend JS modules

| Module | Lines | What it does | Class |
|---|---|---|---|
| `ui.js` | 242 | only bridge to `window.DS`: `startUI`, `GLYPH` icon map, `icon/iconEl/fillIcons`, `html```/`esc`/`raw`, `morph`/`flipMorph`, `ms()`, `watchScrolled`, `faces`, overlays | KEEP |
| `api.js` | 271 | `api()` JSON front door (retries via net.js), `el()`, `confirmDialog`, `alertDialog`, `toast`, `infoTip`, focus restore, `fmtTokens/fmtClock`, sw registration; re-exports net.js | KEEP |
| `net.js` | 837 | `clientKey`, `onWake`, connection state + `mountConnectionBar` (one honest line, grace `OFFLINE_GRACE`), `request()` (per-attempt deadline, retries for reads and idempotent writes), `HttpError/NetworkError/RetryLater`, **`LiveStream`** (EventSource + watchdog, unused), **`Outbox`** (localStorage queue with retry, unused) | KEEP (revive LiveStream/Outbox for chat — ADAPT) |
| `session.js` | 3804 | the app shell: `TermSocket` (term.v1 client: held frames ≤512/128 KB, renumber on hello, ack watch, backoff 0.7–15 s, ping, hello timeout, wake coalescing), list (`applyList`, `mergeActivity`, digest/answer/outcome adoption, ordering rules), folders + shelves + PreStart shelf, row menu (Info, Rename, Status filing, Download, Delete), options menu (key bar, touch mode, sound, notifications), usage strip and dialog, overlays/notices (resumed/resized/desync/status, 6 s linger), attach/detach, IDE pane, retitle, screen-reader announcements, Home/questions glue, new session / prepare session, sidebar rail, boot + routing | ADAPT (split: frame/list/home glue KEEP; TermSocket/attach TERMINAL-ONLY; add chat view) |
| `term.js` | 914 | xterm.js setup: `LIGHT_THEME`, `minimumContrastRatio 4.5`, fonts, `measurePane`, WebGL/DOM renderer, clipboard (OSC 52, selection copy, right/middle click, long-press select), paste with bracketed paste, touch scroll | TERMINAL-ONLY |
| `keybar.js` | 206 | Esc/Tab/Ctrl/Alt/arrows/Enter/^C/^D/Paste row, sticky modifiers, on by default in touch mode, per-device override | TERMINAL-ONLY (agents: OBSOLETE — a chat has a composer; Stop button replaces ^C/Esc — inferred) |
| `keyboard.js` | 578 | touch mode detection + manual override (`socrates.touch`), the keyboard switch (`#kbBtn`: fields muted `inputmode=none` + readonly until pressed, copy of the button in top modal), `guard`, `micButton`, `dictated()` field microphones | ADAPT: touch mode KEEP; the keyboard switch exists because the pane steals focus — re-evaluate for a chat composer (likely simplified or OBSOLETE for chat, inferred) |
| `transfer.js` | 250 | Copy (selection or pane text sheet), Paste, Upload (button, drop, pasted image) → path typed onto prompt | ADAPT (agents: attachments in composer, copy message text) / TERMINAL-ONLY for shell |
| `dictate.js` | 325 | one recording sheet (level meter, clock, Send/Cancel), `record()`, mic pill mounts; transcript inserted into pane without Enter | KEEP sheet; ADAPT target (composer) |
| `voice.js` | 621 | `Recorder` (PCM → 16 kHz WAV), mic error wording, `speak()` via `/api/voice/speak`, playback, deadlines, `plainSpeech` | KEEP |
| `assist.js` | 258 | Status button + ticker (phases), mounts dictation | ADAPT |
| `events.js` | 659 | events.v1 client: backoff 0.5–8 s, 10 s handshake deadline, 35 s silence, 10 s idle ping + 2 s "Checking…", 30 s hidden grace, poll `/api/home` 5 s/15 s while down, "Updated every 5 s" proxy mode after 3 refusals, `serverNow()` clock skew, 4401 handling, titling timeout 45 s | KEEP |
| `home.js` | 710 | Home cards (Questions, Running, Background tasks, Finished, Ready, Review pending, Pending, Prepared, Not running, Done), filter list ("Needs you" default), rows with digest summary, `questionQueue`, `connectionLine`, ages | KEEP (card rules ADAPT to structured questions) |
| `question.js` | 2173 | question dialog: queue "n of m", option buttons (64 px), own-answer field + mic, Read aloud (TTS), run progress from operator, answered-elsewhere/other-device states, offline line, Try again, deep links | ADAPT heavily: render structured questions/permissions and post the reply directly; keep queue, touch sizing, read-aloud, two-device and offline honesty; operator-run states OBSOLETE |
| `prestart.js` | 986 | PreStart panel: `settingsForm`, draft in localStorage `socrates.draft.<id>` + PUT after 600 ms, conflict choice, "Start automatically" datetime, Start (Ctrl/⌘+Enter), name from prompt on first blur | KEEP |
| `harnesses.js` | 1022 | catalogue cache in localStorage (sheet works offline), `settingsForm` (harness, workdir combobox, model combobox, effort, profile dropdown with usage + recommendation, Ultracode, PreStart switch), limits formatting | KEEP |
| `combobox.js` | 462 | DS combobox (filterable) and dropdown | KEEP |
| `folders.js` | 255 | folder grouping by workdir, `More Chats` for dynamic dirs, shelves (background, done), PreStart shelf, IDE per folder, `movesRow` | KEEP |
| `notify.js` | 669 | chime (Web Audio), page notifications, push subscription, holds for outcomes, badge + tray via `asking`, per-device switches | KEEP |
| `outcome.js` | 112 | outcome words/glyphs/lines | KEEP |
| `storage.js` | 122 | storage banner from `/api/state` | KEEP |
| `logos.js` | 151 | inline SVG agent marks (`agentMark`, `.so-agent`) | KEEP |
| `admin.js` | 1762 | admin page (cards above, `MODEL_PICKERS`, busy buttons) | KEEP (drop operator picker; terminal cards TERMINAL-ONLY) |
| `costs.js` | 737 | costs page | KEEP |

CSS (`css/`): `theme.css` (tokens), `so-shared.css`, `so-frame.css`, `so-stage.css` (TERMINAL-ONLY parts:
pane, cover, key bar, copy sheet), `so-home.css`, `so-question.css`, `so-prestart.css`, `so-sheet.css`,
`so-admin.css`, `so-pages.css`. Vendor: xterm 5 + fit, unicode11, web-links, webgl addons
(TERMINAL-ONLY), Dashboard-Style 1.6.2 (KEEP, checksummed, never edited).

---

## 7. User-visible feature catalogue (one line each)

| # | Feature | Where | Class |
|---|---|---|---|
| 1 | Sign in / first-run password / change password / sign out everywhere | login, setup, admin | KEEP |
| 2 | Bucket connect, takeover, storage banner, Storage card | setup, banner, admin | KEEP |
| 3 | New-session sheet: harness, workdir (Dynamic/preset/custom), model, effort, profile (usage + recommended), Ultracode, PreStart switch; offline catalogue | `harnesses.js`, `session.js` | KEEP |
| 4 | Agent session as live terminal (Claude/Codex/OpenCode TUI in xterm) | stage | OBSOLETE → replaced by web-native chat view (ADAPT) |
| 5 | Shell session terminal | stage | TERMINAL-ONLY |
| 6 | VS Code per folder / blank IDE in an iframe | stage, sidebar | KEEP |
| 7 | Multi-device viewing of one session, size notice | stage | TERMINAL-ONLY (chat multi-device is natural; ADAPT) |
| 8 | Survives server restart/update; resumes after reboot with "resumed after restart" notice | everywhere | ADAPT (must hold for chat agents too) |
| 9 | Exit overlay with status and Restart | stage | ADAPT |
| 10 | Sidebar: folders per project, More Chats, shelves (Background, Done), PreStart shelf, collapse to rail, phone drawer | frame | KEEP |
| 11 | Row: agent mark, busy ring, amber waiting ring, tasks hourglass, outcome badge, unread bold, status glyph | frame | KEEP (driven by protocol activity) |
| 12 | Row menu: Info (technical detail), Rename, Status filing, Download scrollback, Delete | frame | KEEP (Download → transcript export for agents, ADAPT) |
| 13 | Auto title on first answer, retitle arrow, name prepared from prompt | top bar | ADAPT |
| 14 | Status (spoken summary) + ticker | top bar | ADAPT |
| 15 | Dictation pill → text into prompt (no Enter) | top bar | ADAPT (into composer) |
| 16 | Copy / Paste / Upload buttons, drop and paste image | top bar | ADAPT (agents) / TERMINAL-ONLY (shell) |
| 17 | Key bar | stage | TERMINAL-ONLY |
| 18 | Keyboard switch + touch mode | top bar / options | touch mode KEEP; switch ADAPT |
| 19 | Options menu: key bar, touch mode, sound, notifications | ⋮ | KEEP (key bar entry terminal-only) |
| 20 | Usage strip + dialog (5 h / weekly, renew countdown) | top bar | KEEP |
| 21 | Home: live cards, filters, "Needs you" list with summaries, connection line | Home | KEEP (sources ADAPT) |
| 22 | Question dialog: walk questions, big options, own answer, mic, Read aloud, progress | dialog | ADAPT (structured, no operator) |
| 23 | Turn outcome (optional): hourglass, verdict marks, header tag, notification text | everywhere | ADAPT |
| 24 | Chime + page notification; Web Push (finished/question/started/test), badge = asking count, tray cleanup, tap → question or session | notify, sw | KEEP |
| 25 | PreStart panel: settings, autosaving prompt with conflict handling, scheduled start, Start | stage | KEEP |
| 26 | Profiles in admin (add, rename, delete, reorder, sign-in status) | admin | KEEP |
| 27 | Costs page | /costs | KEEP |
| 28 | Admin: updates, tmux, setup check, workspace, terminal behaviour, programs, voice, Status & naming, tunnel, password | admin | KEEP (tmux/terminal TERMINAL-ONLY) |
| 29 | Connection bar on every page; offline app shell; installable PWA with shortcuts | all | KEEP |
| 30 | Screen-reader announcements of activity (`#activityLive`) | frame | KEEP |

---

## 8. Offline and resilience mechanisms

| Mechanism | Where | Assumes a terminal? | Rebuild |
|---|---|---|---|
| `net.js` connection model + bar (`OFFLINE_GRACE`, one honest line, retry countdown, `noteContact`) | net.js | No | KEEP |
| `request()` retries reads and requests with an idempotency key (`client_id` in body); writes without one attempted once | net.js | No | KEEP |
| `clientKey()` + server idempotency on `POST /api/sessions` (`SessionByClientID`) | net.js, sessions.go | No | KEEP; extend to every chat message send (`client_id` per message) |
| `Outbox` (persistent localStorage queue `socrates.outbox.<name>`, pending/failed, retry, onWake pump) | net.js | No (built for chat messages) — **currently unused** | ADAPT/revive for the chat composer: messages typed offline are queued and sent once |
| `LiveStream` (EventSource, 2 s watchdog, 25 s silence = dead, wake reconnect, global status) | net.js | No (built for SSE chat) — **currently unused** | revive if chat transport is SSE, or mirror its rules in a WS client |
| `onWake` (online, visibilitychange, pageshow/bfcache, focus) | net.js | No | KEEP |
| `TermSocket` exactly-once input (held frames, renumber to `input_ack`, `viewer_fresh` discard + toast "N keystrokes may not have been delivered"), output byte-seq replay, takeover, grace 90 s | session.js, ws.go, ring.go | **Yes** | TERMINAL-ONLY |
| Events stream resilience (handshake deadline, quiet ping, hidden grace, poll fallback, proxy mode, skew-corrected clock, rev/seq ordering, stale-drop rules) | events.js, events.go | No | KEEP; same patterns for a chat stream |
| Revision replay: `store.Rev()` → whole-list `sessions` frames; `activity.seq`; digest `rev`; title `seq`; HTTP list dropped if older `rev` | events.go, session.js | No | KEEP; chat needs per-session event seq + `since` replay (ADAPT of the ring idea onto a persisted event log, inferred) |
| PreStart draft: localStorage per keystroke, PUT with base rev, 409 conflict choice, "Backed up" once mirrored | prestart.js, store/prestart.go | No | KEEP (same pattern for composer drafts, inferred) |
| Catalogue cached in localStorage so the sheet works offline | harnesses.js | No | KEEP |
| `sw.js`: network-first, per-build cache of the whole shell (index + modules + css + fonts + xterm + DS), API never cached, old build kept until new one whole; push/badge/tray/click routing | sw.js | Partly (xterm in SHELL list) | KEEP; update SHELL list (embed_test enforces it) |
| Push as offline delivery of news; badge from server's asking set with `at` ordering | push.go, sw.js | No | KEEP |
| S3 degraded mode, pending queue, banner | mirror, storage.js | No | KEEP |
| Server durability: tmux scope survives service restart; resume after reboot | termux | Yes (mechanism) | ADAPT: agent processes need the same (detached host per session, or scope; reconnect + replay of missed events from the CLI's own transcript or a host-side journal — inferred). Prior art: `git show 5d7b420^:internal/agenthost/` |
| Watchdog / rollback update | sdnotify, scripts | No | KEEP |

---

## 9. Tests and e2e

### 9.1 Approach

- Go unit tests against **real tmux** on isolated `-S` sockets under `t.TempDir()`, skipped below 3.3;
  OpenRouter, Google TTS, S3 are stand-ins (`openrouter.BaseURL`, `SOCRATES_GOOGLE_TTS_URL`, `internal/s3/s3fake`).
- e2e (`e2e/run.mjs`, 12.7 k lines, 92 scenarios in `ALL`; `e2e/harness.mjs`): real `socrates` binary,
  real tmux, Playwright from `/opt/browser-testing/node_modules/playwright-core`, Chromium
  (`SOCRATES_E2E_CHROME=/opt/google/chrome/chrome` on this machine), own port + data dir per scenario,
  `fakes3` bucket per run with a prefix per data dir, `openRouterStub` (HTTP stub; distinguishes digest
  calls by `DIGEST_MARKER`; `answer` mode reads requests and returns operator keystrokes), fake mic flag,
  `stop()` asserts no session row/tmux session survives. Pane content is read from the DOM with WebGL off.
  `shots.mjs` regenerates README screenshots; `vscode-check.mjs` needs a real code-server; `livesession`
  gated by `SOCRATES_LIVE_AGENTS=1`.

### 9.2 Fakes

- `e2e/fakebin/faketui` (3.2 k lines): **one PTY TUI** linked as `claude`, `codex`, `opencode`. Prints banner
  (cwd, `--model`, OSC 11 background), echoes `you said: …`, prompt editing with history; slash commands
  `/exit n`, `/spin`, `/alt`, `/id`, `/busy`, `/hang`, `/bg`, `/ask`, `/asktwice`, `/nofile`, `/title`,
  `/tasks`, `/question single|cache|multi|multiq|yesno|plan|permit|permit2|permit2b`, `/textq`;
  `FAKE_REPAINT_MS`; writes the real state files (Claude `sessions/<pid>.json`, transcript jsonl with
  `AskUserQuestion`/`ExitPlanMode`/`TaskCreate`/`TaskUpdate`, Codex rollouts + title, OpenCode HTTP server
  with Basic auth, `/session`, `/event` SSE, `/question`, `/permission`, reply/reject); answers `--version`
  and model listings; refuses like the real CLIs (reused `--session-id`, unknown session); logs every
  launch and answer to `FAKE_LOG`.
- `e2e/fakebin/fakes3`: in-memory S3 with SigV4 and conditional writes, `/_fakes3/down|up`.
- History: protocol fakes `internal/harness/fakes/{fakeclaude,fakecodex,fakeopencode}` + `script` package
  at `git show 5d7b420^` / `d65f276^`.

### 9.3 How the fakes must change (inferred)

- Agents: replace (or extend) faketui with **protocol fakes**: a fake `claude` speaking stream-json on
  stdio (init/system, assistant/user/tool_use/tool_result, result, control requests for questions and
  plan approval), a fake `codex app-server` (JSON-RPC: initialize, thread/start, turn/start, item events,
  approval/elicitation requests, turn/completed), a fake `opencode serve` (HTTP + SSE: sessions, messages,
  `session.status`, `question.asked`, `permission.asked`, replies). Keep the same scripted triggers
  (`/busy`, `/tasks`, `/question …`, `/textq`, `/exit`) so scenarios port over; keep `FAKE_LOG` records and
  the "refuse like the real one" behaviour (reused session id, unknown session, auth).
- Keep faketui (or `/bin/sh`) for Shell scenarios; most terminal scenarios use `/bin/sh` already.

### 9.4 Scenario classification (names from `run.mjs ALL`)

- **TERMINAL-ONLY (keep, Shell):** createshell, typeandsee, reloadkeepsscreen, browserlayout, exitoverlay
  (shell variant), webglrenders, touchscroll, keybar, offlineonce, sigtermreattach, takeover,
  offlinerestart, latehello, twoviewers, backpressure, recoveredsession, typeafteroutage, typekeepsfocus,
  clipboard, touchcopy, transfer (shell part), tmuxcard, lighttheme (only the xterm palette part).
- **ADAPT (agent semantics stay, driven through protocol fakes):** harnesses, createclaude, createcodex,
  createopencode, profiles, rebootresume, adminoptions (argv assertions change to protocol argv),
  activity-claude/codex/opencode, tasks-claude/codex/opencode, activity-waiting, unread, notify,
  session-title, status-speak, status-ticker, dictate, no-overlap, outcome, prestart, home-landing,
  home-live, home-question, home-own-answer, home-queue, home-elsewhere, home-deeplink, home-reconnect,
  home-touch, home-focus, home-retry (as reply errors), home-runlost (reply in flight across restart),
  home-opencode, home-kinds, home-unread, home-honest, home-language, home-restart, home-devices,
  home-followup, home-stream, home-quiet, keyboard, options, design, folders, sessionlist, deletekeepsdir,
  pages, livesession.
- **OBSOLETE (operator/screen-scrape specific):** activity-fallback (screen/quiescence fallback),
  home-operator, home-guard, home-held, home-prompt, home-moved-on (partly: "question changed" survives as
  stale request id), home-machine (colour-only highlight, half-answered menu), home-tick (screen-hash
  masking), home-parts, home-next, home-words, home-late-card, home-readagain (partly), home-comeback
  (partly: Done mid-run stays relevant as "reply to a parked session").
- **KEEP unchanged:** s3restore, storagebanner, costs-related unit tests, tunnel/push/admin unit tests.

---

## 10. Design system essentials a new chat UI must follow (DASHBOARD-STYLE.md + DESIGN §E.10)

- Built on **Dashboard-Style** 1.6.2 vendored at `static/vendor/dashboard-style` (checksummed by
  `embed_test.go`, replaced nightly by a workflow) — **never edit the vendored copy**; upstream anything general.
- Socrates wears DS's own look: no DS role overridden. `css/theme.css` holds only `--so-*` tokens,
  documented component hooks (`--ds-btn-*`), touch-mode hooks and the mono font. **No motion tokens/hooks
  overridden.**
- **Reach for a DS component first**, copying its documented markup (`docs/components.md` in DS, reference
  app `example/`). Overlays (dialogs, sheets, menus, combobox list, modal drawer, snackbars) through
  `DS.overlay`.
- Project components prefix **`so-`**, tokens only (`--ds-*`, `--so-*`): no colour/radius/font/shadow/
  duration/curve literals, no own `@keyframes`; a needed literal becomes a `--so-*` token. Project CSS is
  unlayered and never restyles `.ds-*` (use hooks).
- **Every class in markup/script is `ds-*` or `so-*`** (exception: other libraries' own, e.g. `xterm-*`).
  State via attributes (`aria-pressed`, `aria-current`, `aria-expanded`, `aria-selected`, `data-state`,
  `hidden`), never bare `on/active/open` classes.
- Re-render with `morph()` / `flipMorph()` and the `html``` template — **never `innerHTML`**; key list rows
  with `data-ds-key` (a chat message list must be keyed so streaming updates do not restart animations).
  Numbers via `DS.motion.countUp`; timing from `ms()` / `DS.motion.motionDuration`, never ms literals.
- **Icons only from `DS.icons`** via `icon()/iconEl()/data-icon`; meaning→glyph map `GLYPH` in `ui.js`;
  agent brand marks only via `logos.js` / `.so-agent` (marks wherever a program is named).
- Status never by colour alone; every action is a pill; one filled button per area; no red buttons.
- `ui.js` is the only module that reads `window.DS`.
- Socrates rules: technical detail (versions, paths, ids, exit codes, stderr) behind the `i` of `infoTip()`,
  never visible text; phone-in-a-car and **touch mode** (`.so-touch`, 48 px targets; question dialog
  64 px options, full screen < 900 px or touch) are first-class; **nothing typed is ever lost**; everything
  in English; motion subtle, reduced-motion respected except the busy ring and working-button ring (keep
  turning at 900 ms). The `design` e2e scenario measures: white surfaces, marks, hover-only detail,
  animations that do not restart on re-render.
- Note (inferred): DESIGN §E.10 says "every surface is pure white", while DS's own look (manifest
  `theme_color #f0f4f9`) is not all white; DASHBOARD-STYLE.md (newer, Oct 2026) says Socrates wears DS's
  look, so DS wins for chrome; the terminal pane itself stays white (`--so-term-bg: var(--ds-surface)`).
- Done criteria per change: no non `ds-`/`so-` class; looks right at 1280×800 and 390×844 incl. touch
  mode, no console errors; `go test ./internal/web/... ./internal/server/...` and the area's e2e pass;
  README shots retaken after visible changes.

---

## 11. Persistence keys worth knowing

kv: `settings`, `password_hash`, `agents_catalog`, `profiles`, `profiles.order`, `push.vapid`,
`push.subscriptions`, `activity.unread`, `outcomes` (local only, not mirrored), `mirror.*` (local).
Tables: sessions, logins, drafts, mirror_pending, cost_convs, cost_hours, cost_seen.
Per-session dir `<data>/sessions/<id>/`: `journal.raw` (+`.1`), `plan.json` (launch plan incl. OpenCode
port/password), `claude-settings.json`, `opencode-tui.json`, `initial-prompt` (transient), `ide.sock`.
Bucket objects: `meta.json`, `lease.json`, `kv/*`, `logins.json`, `sessions/<id>.json`, `drafts/<id>.json`,
`costs/<harness>/<conv>.json`.

---

## 12. Things a rebuild could easily lose (checklist, inferred from the above)

1. Agent processes surviving `systemctl restart socrates` and nightly updates (today: tmux scope).
2. Resume after reboot on the CLI's own conversation id, lazily on open, with a visible "resumed" notice.
3. Exactly-once delivery of what the person typed, across drops and reloads (today: input seq; chat:
   `client_id` per message + Outbox).
4. Multi-device: two devices on one session, a question answered on one device closing it on the other
   ("Another device is answering", "Answered on another device").
5. Fixed launch policy: bypass, `IS_SANDBOX=1` as root, empty `ANTHROPIC_API_KEY`, trust pinning by
   realpath, Remote Control off three ways, OpenCode password + allow-all, Codex no update prompt.
6. Profiles (env per CLI, shared MCP), headroom-based profile choice, `profile_auto` at PreStart start.
7. Unread rule only after somebody typed (`Asked`), background shelf never notifies Finished, Done sweep 24 h.
8. Push payload contract (`kind`, `badge`, `asking`, `at`) that `sw.js` depends on; `#q=` / `#open=` routing.
9. Home card rules mirrored server-side in `asking.go` (badge must equal Home's Questions count).
10. Outcome hold (≤45 s) for Finished notifications; question push waits for its words (≤8 s).
11. Upload into `<workdir>/.socrates/uploads/` with `.gitignore`.
12. Cost ledger reading transcripts — verify protocol modes still write them where the scanner looks.
13. Uploads/attachments, dictation, read-aloud, spoken Status — the car use case.
14. sw.js SHELL list completeness (embed_test enforces it) and build-stamped asset URLs.
15. The `design` and `pages` e2e assertions (classes, marks, hover-only detail, phone/desk layouts).
