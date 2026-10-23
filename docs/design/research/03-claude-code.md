# 03 — Driving Claude Code 2.1.309 headlessly from a Go web server

Date: 2026-10-23. Binary: `/root/.local/bin/claude` → `/root/.local/share/claude/versions/2.1.309`
(build 2026-10-22T16:51:07Z), logged in with a claude.ai **Team** subscription (rate tier
`default_claude_max_5x`). Agent SDK reference: `@anthropic-ai/claude-agent-sdk@0.3.309` (the
version that ships against this CLI), unpacked read-only under `scratchpad/sdk-pkg/package/`
(`sdk.d.ts`, `sdk-tools.d.ts`, `sdk.mjs`).

Evidence labels used throughout:

* **VERIFIED** — observed in a lab run in this session; the raw stdin/stdout of every run is
  in `scratchpad/cc-lab/logs/<exp>.jsonl` (`{"dir":"in"|"out"|"err"|"argv"|"eof", "t":secs, "msg":…}`).
* **DOCUMENTED** — read in the SDK's type definitions / bundled source, the binary's strings,
  or Anthropic's docs (URL given).
* **INFERRED** — my conclusion, not directly observed.

Lab layout (`/tmp/claude-0/-root-SocratesAgent/d612e239-e189-4f08-9945-19100f29fc4c/scratchpad/cc-lab/`):

| file | what |
|---|---|
| `cclab.py` | tiny Python driver: spawns `claude -p --input-format stream-json --output-format stream-json --verbose …` with a **clean env** (all `CLAUDE*`/`ANTHROPIC*` vars stripped — the parent shell is itself a Claude Code session), logs every line, routes `control_request`s to a callback |
| `sdkmcp.py` | an in-process ("sdk") MCP server answered over the control protocol |
| `mcp_askuser_stdio.py` | external stdio MCP server with a rich `ask_user` tool that blocks until an answer file appears, optional progress notifications |
| `mcp_elicit_stdio.py` | stdio MCP server whose tool asks the user via MCP **elicitation** |
| `proxy.py` | logging reverse proxy for `ANTHROPIC_BASE_URL` (captures the exact Messages API payload: system prompt + tool schemas; auth headers dropped) |
| `rebuild_history.py` | rebuilds a chat history from a transcript `.jsonl` |
| `exp01_basic.py` … `exp13_stdin_eof.py` | the experiments cited below |

Every run used `--model haiku` (resolves to `claude-haiku-5-5`); total spend well under $1.

---

## 0. Executive summary

* The stream-json protocol is alive and richer than in 2.1.252. One long-lived
  `claude -p --input-format stream-json --output-format stream-json --verbose
  --include-partial-messages --permission-prompt-tool stdio` process per chat handles many turns,
  with a bidirectional **control protocol** (`control_request` / `control_response` /
  `control_cancel_request`) on the same pipes. Everything the TUI does that a web UI needs is
  reachable: permission prompts, AskUserQuestion, plan approval, interrupt, model / permission-mode
  switching, usage & rate limits, context usage, subagent/task events, slash commands, images.
* **AskUserQuestion** (1–4 questions, 2–4 options each, `multiSelect`, `header` ≤12 chars, optional
  `preview`; "Other" free text implied) is answered by the host through `can_use_tool`
  → `{"behavior":"allow","updatedInput":{…input, "answers":{question→label}, "annotations":{question→{notes}}}}`.
  A prompt held open for **10 minutes** was answered fine. It only exists when a permission host is
  attached (`--permission-prompt-tool stdio`); `--disallowedTools AskUserQuestion` removes it.
* A **custom `ask_user` tool** with an arbitrary schema works three ways, all verified:
  in-process SDK MCP server over the control protocol, external stdio MCP server via `--mcp-config`,
  and MCP **elicitation** from any MCP server (surfaces as `control_request{subtype:"elicitation"}`).
  `--append-system-prompt` + `--disallowedTools AskUserQuestion` steered Haiku to use it every time.
  Timeouts that matter: external MCP servers have an **idle timeout** (stdio default 30 min, HTTP/SSE
  5 min; `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`, per-server `timeout`) that **progress notifications
  reset** (verified) and that a **pending elicitation pauses** (verified); SDK servers have **no idle
  timeout** and only the wall-clock tool timeout (default ≈27.8 h, `MCP_TOOL_TIMEOUT` / per-server
  `timeout`). Results of the 20-min / 40-min runs: see §4.4.
* **Durability**: transcripts are append-only JSONL at
  `$CLAUDE_CONFIG_DIR/projects/<cwd, every non-alnum char → '->/<session-id>.jsonl`, subagents under
  `<session-id>/subagents/agent-<id>.jsonl`. `--resume <id>` continues the same id in the same file;
  `--fork-session` copies into a new id. After SIGKILL: a half-streamed assistant message is **lost**
  (resume inserts a synthetic `"No response requested."`), a pending permission / tool call gets a
  synthetic `tool_result` `"[Tool call interrupted: the session ended …]"`. A web UI can rebuild its
  history from the JSONL (`rebuild_history.py`).
* **Account switching**: a session moves between config dirs by copying (or symlink-sharing)
  `projects/<escaped-cwd>/<id>.jsonl`; nothing else was needed for a resume. Better: one shared
  config dir (or shared `projects/`) and pick the account per process with `CLAUDE_CODE_OAUTH_TOKEN`
  (verified working with an empty config dir; `claude setup-token` mints 1-year tokens).
* **Policy**: Anthropic's docs say third-party developers may not offer claude.ai login / route
  requests through subscription credentials for their products, but explicitly do not prevent "an
  end user from signing in to the unmodified Claude Code binary with their own Claude subscription".
  A self-hosted tool where each user runs the unmodified binary under their own login fits the latter
  reading; one that lets others use *your* login or collects their tokens does not. Quotes in §8.

---

## 1. stream-json I/O

### 1.1 Launch and the argv that worked (VERIFIED)

```
claude -p --input-format stream-json --output-format stream-json --verbose
       --include-partial-messages            # stream_event deltas
       --permission-prompt-tool stdio        # can_use_tool control requests go to the host
       --permission-mode default             # (CLI also accepts: acceptEdits auto bypassPermissions manual dontAsk plan)
       --setting-sources project,local       # do NOT inherit ~/.claude/settings.json (see below)
       --strict-mcp-config                   # do NOT load the account's claude.ai connectors
       --model haiku  [--session-id <uuid> | --resume <uuid>]  [--mcp-config <json>] [--append-system-prompt …]
```

Pitfalls observed:

* Without `--setting-sources` excluding `user`, `~/.claude/settings.json` applies: exp01 ran in
  `"permissionMode":"auto"` because the user settings say `defaultMode:"auto"`, and a `Write` ran with
  no prompt (`"permission_decision":{"decision":"accept","source":"config","reason_type":"mode"}`).
* Without `--strict-mcp-config` the session loads the claude.ai account's connectors (exp01 `init`
  listed 26 `"source":"claudeai"` servers incl. Gmail, 92 tools).
* The parent environment matters: this machine's shell exports `ANTHROPIC_API_KEY` (empty) and many
  `CLAUDE_CODE_*` variables. The Go server must build the child env explicitly.

### 1.2 Sending user messages (VERIFIED)

One JSON object per line on stdin:

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly: pong"}]},
 "uuid":"11111111-1111-4111-8111-111111111111"}
```

* `uuid` is optional; when given, the CLI echoes it as `user_message_uuid(s)` on stream frames and
  `result`, and emits `command_lifecycle` frames for it:
  `{"type":"command_lifecycle","command_uuid":"1111…","state":"queued"|"started"|"completed",…}`.
* Images: a base64 block in the same content array works (exp09: red 32×32 PNG → "Red"):
  `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"…"}}`.
  The transcript keeps the image and adds a text line `[Image: source: /tmp/claude-0/<escaped-cwd>/<sid>/…]`.
* Sending a second message while a turn runs **queues** it: exp09 sent two messages 1 s apart and
  got **two** `result`s (the second turn ran after the first). `result.queued_turn_count` reports
  pending sends (DOCUMENTED).
* `--replay-user-messages` echoes each stdin user message back with `"isReplay":true` (useful as an ack).
* Closing stdin after a send: the turn still completes, `result` is written, process exits 0 (exp13).

### 1.3 Message types emitted (VERIFIED unless marked)

Order for a simple turn (exp01): `command_lifecycle(queued)` → `command_lifecycle(started)` →
**`system/init`** → `system/status{status:"requesting"}` → `user`(replay) → `stream_event`s +
`assistant` → `rate_limit_event` → **`result`** → `command_lifecycle(completed)`.

* **`system/init` is emitted per turn, not at process start**: nothing at all is written while the
  process idles (exp01: 0 lines in 6 s), then one `init` before every turn. Fields observed:
  `cwd, session_id, tools[], mcp_servers[{name,status,source}], model, permissionMode,
  slash_commands[], terminal_slash_commands[], apiKeySource, claude_code_version, output_style,
  agents[], skills[], plugins[], capabilities[], memory_paths, original_cwd, additional_directories,
  fast_mode_state, per_turn_effort_active, view_mode, uuid`. Capabilities in 2.1.309:
  `interrupt_receipt_v1, interrupt_cancel_queued_v1, interrupt_send_now_v1, interrupt_requeue_taken_v1,
  msg_lifecycle_v1, request_marker_lists_v1, lifecycle_wakeup_source_v1, sdk_mcp_tools_list_changed,
  sdk_mcp_manifests, mcp_read_resource_v1, mcp_tool_ui_meta_v1, ui_surface_v1, mcp_call_result_not_for_model_v1`.
  (The `initialize` control request answers immediately, before any turn — §2.1 — so use it for
  "process is ready".)
* **`stream_event`** (with `--include-partial-messages`) wraps raw Messages-API SSE events:
  ```json
  {"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}},
   "session_id":"…","parent_tool_use_id":null,"uuid":"…","api_message_id":"msg_011Cg…"}
  ```
  Event types seen: `message_start` (carries model, usage, plus top-level `ttft_ms`, `thinking_display:"updates"`),
  `content_block_start`, `content_block_delta` (`text_delta`, `thinking_delta`, `signature_delta`,
  `input_json_delta`), `content_block_stop`, `message_delta` (stop_reason, usage), `message_stop`.
* **`assistant`**: one per *completed content block* (several share `message.id`), e.g.
  `{"type":"assistant","message":{"model":"claude-haiku-5-5","id":"msg_…","content":[{"type":"tool_use","id":"toolu_…","name":"Write","input":{…},"caller":{"type":"direct"}}],"stop_reason":null,…},"parent_tool_use_id":null,"session_id":"…","uuid":"…","request_id":"req_…","wire_tool_inputs":{…}}`.
  **Thinking blocks arrive with `"thinking":""`** (only a signature) because the session runs with
  `thinking.display:"updates"` — a UI gets no thinking text by default (DOCUMENTED: `--thinking-display`
  / `set_max_thinking_tokens{thinking_display:"summarized"}`; not tested).
* **`user`** carrying tool results:
  ```json
  {"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01Gk…","type":"tool_result","content":"File created successfully at: …"}]},
   "parent_tool_use_id":null,"session_id":"…","uuid":"…","timestamp":"…",
   "tool_use_result":{"type":"create","filePath":"…","content":"x","structuredPatch":[],"originalFile":null,"userModified":false},
   "tool_result_meta":[{"id":"toolu_01Gk…","permission_decision":{"decision":"accept","source":"config","reason_type":"mode"}}]}
  ```
  `tool_use_result` is the structured per-tool output (diffs for Edit, stdout/stderr for Bash, …).
* **`result`** (one per turn). Key fields: `type:"result"`, `subtype:"success"|"error_during_execution"|…`,
  `is_error`, `terminal_reason` (`completed`, `aborted_streaming`, `aborted_tools`), `stop_reason`,
  `result` (final text), `num_turns`, `duration_ms`, `duration_api_ms`, `ttft_ms`,
  `total_cost_usd` (**cumulative for the process**: 0.0028 then 0.0056), `usage` (this turn's main loop),
  `modelUsage` (cumulative per model incl. `contextWindow:1000000`, `maxOutputTokens`, `costUSD`),
  `model_usage_since_last_result` (per turn), `permission_denials[]`, `subagent_stats`,
  `fast_mode_state`, `queued_turn_count`, `result_index`, `user_message_uuid(s)`, `errors[]` (on errors).
  Still true from 2.1.252: discriminate on `is_error`, not on `subtype`.
* **`rate_limit_event`** (after a request):
  ```json
  {"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1792770000,"rateLimitType":"five_hour",
   "overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,
   "unifiedWindows":{"five_hour":{"utilization":0.04,"resetsAt":1792770000}}},"uuid":"…","session_id":"…"}
  ```
  DOCUMENTED values: `status: allowed|allowed_warning|rejected`, `rateLimitType: five_hour|seven_day|seven_day_opus|seven_day_sonnet|…|overage`.
* Other `system` subtypes seen: `status` (`requesting`, `compacting`, `null` with `compact_result` or
  `permissionMode` change), `thinking_tokens` (`estimated_tokens`), `task_started`, `task_progress`,
  `task_updated`, `task_notification`, `background_tasks_changed`, `compact_boundary`. Plus top-level
  `conversation_reset` and `control_cancel_request`. The full union (`SDKMessage` in sdk.d.ts) also
  has `api_retry`, `hook_started/progress/response` (with `--include-hook-events`), `tool_progress`,
  `auth_status`, `session_state_changed`, `notification`, `permission_denied`, `prompt_suggestion`
  (`--prompt-suggestions`), `local_command_output`, `elicitation_complete`, `model_refusal_*`,
  `files_persisted`, `memory_recall`, `informational` (DOCUMENTED).
* **Turns without user input**: a backgrounded Bash task finished after the turn ended, emitted
  `task_updated`/`task_notification`, and the CLI **started a new turn by itself** (new `init`,
  assistant output, `result`) — exp02. The UI must accept results it did not ask for.

### 1.4 Multi-turn on one process (VERIFIED)

exp01/02/09: up to 11 turns on one process, same `session_id`, prompt cache reused
(`cache_read_input_tokens` grows). Exceptions: `/clear` switches to a **new session id** (§7).

---

## 2. Control protocol

Envelope (DOCUMENTED `SDKControlRequest/Response`, all shapes below VERIFIED unless noted):

```json
→ {"type":"control_request","request_id":"req_99129b1e","request":{"subtype":"…", …}}
← {"type":"control_response","response":{"subtype":"success","request_id":"req_99129b1e","response":{…}}}
← {"type":"control_response","response":{"subtype":"error","request_id":"…","error":"…"}}
   {"type":"control_cancel_request","request_id":"…"}     // either side withdraws its own request
```

Both directions use it: host→CLI (initialize, interrupt, set_model, …) and CLI→host (`can_use_tool`,
`hook_callback`, `mcp_message`, `elicitation`, `request_user_dialog`). The SDK spawns the CLI with
`--output-format stream-json --verbose --input-format stream-json` and adds
`--permission-prompt-tool=stdio` when a `canUseTool` callback exists (DOCUMENTED, sdk.mjs).

### 2.1 `initialize` (VERIFIED, exp02)

```json
{"subtype":"initialize",
 "hooks":{"PreToolUse":[{"matcher":"Bash","hookCallbackIds":["hk_pre_bash"]}],"Stop":[{"hookCallbackIds":["hk_stop"]}]},
 "sdkMcpServers":["host"],
 "sdkMcpServerConfigs":{"host":{"timeout":60000,"disableAutoBackground":true}},
 "agents":{"echoer":{"description":"Echoes things","prompt":"You echo.","model":"haiku"}},
 "appendSystemPrompt":"Always end replies with the word BANANA."}
```

Response keys (2.1.309): `commands[]` (54 `{name,description,argumentHint,builtin}`), `agents[]`
(includes the injected `echoer`), `models[]` (13 `{value,resolvedModel,displayName,description,
supportsEffort,supportedEffortLevels,…}` — `default, opus, fable, sonnet, haiku, …`), `account`
(`{"email":"…","organization":"Zesiger.net","subscriptionType":"Claude Team","apiProvider":"firstParty"}`),
`output_style`, `available_output_styles`, `current_permission_mode`, `hooks_applied:true`,
`session_state:"idle"`, `pid`, `claude_code_version`, `fast_mode_state`, `capabilities`, … .
Answered in ~1 s **before** any turn, so it is the readiness signal.

Other initialize fields (DOCUMENTED): `systemPrompt[]`, `planModeInstructions`, `toolAliases`,
`jsonSchema`, `title`, `skills[]`, `promptSuggestions`, `agentProgressSummaries`,
`forwardSubagentText`, `supportedDialogKinds[]`, `perTaskStopAffordance`, `plugins[]` (with
`--await-initialize`), `sdkMcpServerManifests`, `excludeDynamicSections`, `systemPromptSnapshot`.

### 2.2 `can_use_tool` (VERIFIED)

CLI → host (exp02, an SDK-MCP tool under `--permission-mode default`):

```json
{"type":"control_request","request_id":"e84ddd9b-…","request":{"subtype":"can_use_tool",
 "tool_name":"mcp__host__echo_back","mcp_server":{"name":"host","source":"sdk"},"display_name":"Echo Back",
 "input":{"s":"hi"},
 "permission_suggestions":[{"type":"addRules","rules":[{"toolName":"mcp__host__echo_back"}],"behavior":"allow","destination":"localSettings"}],
 "server_policy_ask_only":true,"tool_use_id":"toolu_011FX6F4ADxz5t6PDJaUPy2f"}}
```

Host → CLI: `{"behavior":"allow","updatedInput":{…}}` (may add `updatedPermissions`), or
`{"behavior":"deny","message":"…"}` → the model sees `tool_result{is_error:true,content:message}`
and the result lists it in `permission_denials`. DOCUMENTED extra request fields:
`decision_reason(_type)`, `blocked_path`, `title`, `description`, `agent_id` (subagent asks),
`requires_user_interaction`, `default_to_no`, `suppress_always_allow_rule`, `classifier_approvable`.

Read-only Bash (`echo hello`) was auto-allowed without a prompt; `Write` and MCP tools prompted.
`--permission-prompts none` (no host) auto-denied the Write (`permission_denials` filled; exp10).

### 2.3 Hooks via the control channel (VERIFIED)

```json
{"subtype":"hook_callback","callback_id":"hk_pre_bash","input":{"session_id":"…","transcript_path":"/root/.claude/projects/…/<sid>.jsonl",
 "cwd":"…","prompt_id":"…","permission_mode":"default","effort":{"level":"medium"},"hook_event_name":"PreToolUse",
 "tool_name":"Bash","tool_input":{"command":"echo hello","description":"Print hello via echo"},"tool_use_id":"toolu_…"},"tool_use_id":"toolu_…"}
```
Answered with `{"continue":true}`. The `Stop` hook fired at the end of every turn. Hook events
available (DOCUMENTED): PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, Notification,
UserPromptSubmit, SessionStart/End, Stop, StopFailure, SubagentStart/Stop, PreCompact/PostCompact,
PermissionRequest/Denied, TaskCreated/Completed, Elicitation/ElicitationResult, … .

### 2.4 Interrupt (VERIFIED, exp10)

`{"subtype":"interrupt"}` (optional `cancel_queued:true`) → `{"still_queued":[]}`.

* Mid-stream: `result{subtype:"error_during_execution",is_error:true,terminal_reason:"aborted_streaming",errors:["[ede_diagnostic] …"]}`;
  stdout gets a `user` text `"[Request interrupted by user]"`; the **partial text is persisted** (857 chars kept).
* With a `can_use_tool` pending: the CLI first sends **`{"type":"control_cancel_request","request_id":"<the can_use_tool id>"}`**,
  then a synthetic `tool_result` ("The user doesn't want to proceed with this tool use…"), a user
  text `"[Request interrupted by user for tool use]"`, and `result{terminal_reason:"aborted_tools",is_error:true}`.
  A late answer to the withdrawn request is ignored (file not created). The next turn works.

### 2.5 Other requests tried (VERIFIED responses)

| request | response (abridged) |
|---|---|
| `set_model {"model":"haiku"}` | `{"model":"claude-haiku-5-5"}` |
| `set_permission_mode {"mode":"default"\|"plan"}` | `{"mode":"plan"}`; a `system/status{permissionMode:"plan"}` frame follows |
| `get_context_usage` | `{"categories":[{"id":"system_prompt","tokens":3166,…},{"id":"system_tools",…},…], …}` |
| `get_session_cost` | `{"text":"Total cost: $0.0000\nTotal duration (API): …"}` |
| `get_usage` | `{"session":{…},"subscription_type":"team","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":6,"resets_at":"2026-10-23T15:39:59…"},"seven_day":null,…,"extra_usage":{…},"limits":[{"kind":"session","percent":6,"severity":"normal",…}]}}` |
| `list_models` | `{"models":[…13 entries…]}` |
| `mcp_status` | `{"mcpServers":[{"name":"host","status":"connected","transport":"sdk","tools":[{"name":"echo_back"}]}]}` |
| `get_settings`, `get_binary_version` (`{"version":"2.1.309","buildTime":"…"}`), `list_permission_rules`, `get_hooks_listing` | all answered |

Full host→CLI list in 2.1.309 (DOCUMENTED, `SDKControlRequestInner`): `interrupt, initialize,
set_permission_mode, set_model, set_max_thinking_tokens, rename_session, set_color, mcp_status,
get_context_usage, get_session_cost, list_models, get_usage, get_binary_version, mcp_call,
file_suggestions, mcp_message, rewind_files, cancel_async_message, read_file, seed_read_state,
mcp_set_servers, register_repo_root, reload_plugins, reload_skills, reload_output_styles,
mcp_reconnect, mcp_toggle, stop_task, background_tasks, get_task_output, apply_flag_settings,
get_settings, get_hooks_listing, update_settings, list_permission_rules, mcp_read_resource`;
CLI→host: `can_use_tool, hook_callback, mcp_message, elicitation, request_user_dialog` (plus
`oauth_token_refresh`/`host_auth_token_refresh` when the host declares them). The SDK source also
sends `side_question, generate_session_title, set_cwd, claim_session, remote_control,
submit_feedback, set_mcp_permission_mode_override, …` (not in the public type union; not tested).
`rewind_files` needs file checkpointing, which is off in `-p` (no `file-history/<sid>` was written);
`CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING` exists in the binary (DOCUMENTED, untested).

---

## 3. AskUserQuestion

### 3.1 Exact schema sent to the model (VERIFIED via the logging proxy, `logs/askuserquestion-tool-schema.json`)

```json
{"questions": {"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","additionalProperties":false,
   "required":["question","header","options","multiSelect"],
   "properties":{
     "question":{"type":"string"},                       // "should end with a question mark"
     "header":{"type":"string"},                         // "Very short label … (max 12 chars)" — prose only, not enforced by schema
     "options":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"object","additionalProperties":false,
        "required":["label","description"],
        "properties":{"label":{"type":"string"},"description":{"type":"string"},"preview":{"type":"string"}}}},
     "multiSelect":{"type":"boolean","default":false}}}},
 "answers":{"type":"object","additionalProperties":{"type":"string"}},          // "User answers collected by the permission component"
 "annotations":{"type":"object","additionalProperties":{"type":"object","properties":{"preview":{"type":"string"},"notes":{"type":"string"}}}},
 "metadata":{"type":"object","properties":{"source":{"type":"string"}}}}
 required: ["questions"]
```

Tool description says users can always pick "Other" (free text), options must not include "Other",
recommended option first with "(Recommended)". So: ≤4 questions, 2–4 options each, no per-question
context field beyond option `description`, free text only as the implicit "Other".

### 3.2 Answering it headlessly (VERIFIED, exp03A)

CLI → host:
```json
{"subtype":"can_use_tool","tool_name":"AskUserQuestion","display_name":"AskUserQuestion",
 "input":{"questions":[{"question":"What is your favourite colour?","header":"Colour","options":[{"label":"Red","description":"Red"},…],"multiSelect":false},
                       {"question":"Which shirt sizes do you wear?","header":"Shirt sizes","options":[…],"multiSelect":true}]},
 "tool_use_id":"toolu_01Re1SeS2B9sYXmcLzDPJvKN","requires_user_interaction":true}
```
Host → CLI:
```json
{"behavior":"allow","updatedInput":{"questions":[…unchanged…],
  "answers":{"What is your favourite colour?":"Green","Which shirt sizes do you wear?":"S, M"},
  "annotations":{"What is your favourite colour?":{"notes":"free text note from the phone: I am colour-blind"}}}}
```
Model sees: `"The user answered: \"What is your favourite colour?\"=\"Green\" notes: free text note from the phone: I am colour-blind, \"Which shirt sizes do you wear?\"=\"S, M\". Read the answers carefully …"`;
`tool_use_result` echoes `{questions, answers, annotations}`. Answers are keyed by **question text**;
multi-select = comma-joined labels; a free-text "Other" answer is just any string.

Alternative (exp03B): `{"behavior":"deny","message":"The user answered in the web UI: {…}"}` also gets
the answer to the model but is recorded as an error/denial — use `allow`.

### 3.3 Long waits (VERIFIED, exp03C)

The `can_use_tool` for AskUserQuestion was held unanswered for **600 s**, then answered: the turn
continued normally (`result` at t=603 s). No timeout on the CLI side was hit. (DOCUMENTED: the
dialog-style `request_user_dialog` has CLI-side deadlines; `can_use_tool` showed none in 10 min.)

### 3.4 Availability / disabling (VERIFIED)

* Present in `tools` only when a permission host is attached (`--permission-prompt-tool stdio`, exp03);
  absent in plain `-p` (proxy capture: 13 tools, no AskUserQuestion) and in stream-json without the
  flag (exp10).
* `--disallowedTools AskUserQuestion` removes it (exp05/06 `init.tools` has no AskUserQuestion).

---

## 4. Custom `ask_user` replacement

Rich schema used in all variants (`mcp_askuser_stdio.py::ASK_USER`): `title`, up to 20 `questions`
each with `id`, `question`, `context` (markdown), `kind: single|multi|text|confirm`, up to 12
`options{value,label,description,recommended}`, `allow_free_text`. Haiku produced exactly this shape,
e.g. (exp05-quick):

```json
{"name":"mcp__askuser__ask_user","input":{"title":"Scaffolding a tiny CLI tool: language and name",
 "questions":[{"id":"language","question":"Which language should the CLI be written in?","kind":"single",
   "context":"The working directory is not a git repo …","options":[{"value":"python","label":"Python (stdlib argparse, no dependencies)","recommended":true},{"value":"node",…},{"value":"go",…},{"value":"rust",…},{"value":"bash",…}],"allow_free_text":true},
  {"id":"name","question":"What should the tool be called? …","kind":"text","context":"…"}]}}
```

Steering (VERIFIED in every run, 8/8): `--disallowedTools AskUserQuestion` +
`--append-system-prompt "When you need any decision or information from the user, call the
mcp__askuser__ask_user tool. Never ask questions in plain text and never use AskUserQuestion. …"`
(or `appendSystemPrompt` in `initialize`). MCP tools are **deferred**: the model first calls
`ToolSearch {"query":"select:mcp__askuser__ask_user"}` and then the tool; that is normal and cheap.
Pre-allow the tool with `--allowedTools mcp__askuser__ask_user` (or answer `can_use_tool`).

### 4.1 Variant A — in-process SDK MCP server over the control protocol (VERIFIED, exp02/exp06)

Declare it in `initialize` (`sdkMcpServers:["host"]`, `sdkMcpServerConfigs:{"host":{"disableAutoBackground":true,"timeout":…}}`).
The CLI then performs the MCP handshake over `mcp_message` control requests:

```json
← {"type":"control_request","request_id":"eed84a97-…","request":{"subtype":"mcp_message","server_name":"host",
    "message":{"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"claude-code",…}},"jsonrpc":"2.0","id":0}}}
→ {"type":"control_response","response":{"subtype":"success","request_id":"eed84a97-…",
    "response":{"mcp_response":{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"host","version":"0.1"}}}}}}
   … notifications/initialized (answer {"mcp_response":{"jsonrpc":"2.0","result":{},"id":0}}), tools/list …
← {"subtype":"mcp_message","server_name":"host","message":{"method":"tools/call","params":{"name":"echo_back","arguments":{"s":"hi"},
    "_meta":{"claudecode/toolUseId":"toolu_011FX6…","progressToken":2}},"jsonrpc":"2.0","id":2}}
→ {"mcp_response":{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ECHO:hi"}]}}}
```

The host can answer the `tools/call` control request **whenever it likes** (it is just an unanswered
`request_id`). This is the most natural fit for a Go server: no extra process, the answer comes
straight from the web handler. On timeout the CLI sends
`mcp_message{"method":"notifications/cancelled","params":{"requestId":2,"reason":"SdkError: Request timed out"}}`
and the model gets `tool_result{is_error:true,"MCP server \"host\" tool \"ask_user\" timed out after 60s"}` (exp06-tmo60, `timeout:60000`).

### 4.2 Variant B — external stdio MCP server via `--mcp-config` (VERIFIED, exp05)

`--mcp-config '{"mcpServers":{"askuser":{"type":"stdio","command":"python3","args":[".../mcp_askuser_stdio.py"],"env":{…},"timeout":<ms optional>}}}'`.
The CLI's MCP client advertises `{"roots":{"listChanged":true},"elicitation":{"form":{},"url":{}},"extensions":{"io.modelcontextprotocol/tasks":{}}}`
and first probes `server/discover` (protocol `2026-07-28`); answering it with -32601 is fine.
When the result carries `structuredContent`, that (not the text content) is what the model saw.

### 4.3 Variant C — MCP elicitation (VERIFIED, exp11)

Any MCP server (stdio/HTTP/sdk) may send `elicitation/create` during a tool call; the CLI forwards it:

```json
← {"type":"control_request","request_id":"6f275a43-…","request":{"subtype":"elicitation","mcp_server_name":"elicit",
    "message":"Pick a colour. Context: testing","mode":"form",
    "requested_schema":{"type":"object","properties":{"colour":{"type":"string","title":"Colour","description":"Your pick","enum":["red","green","blue"]},"notes":{"type":"string","title":"Notes"}},"required":["colour"]},
    "property_order":["colour","notes"]}}
→ {"type":"control_response","response":{"subtype":"success","request_id":"6f275a43-…",
    "response":{"action":"accept","content":{"colour":"green","notes":"from the phone"}}}}
```
(The response body is `{"action":"accept"|"decline"|"cancel","content":{…}}`.) The server got
`{"action":"accept","content":{…}}` back as the JSON-RPC result. Elicitation schemas are flat
(primitive properties), so it suits "confirm / pick one", less so a 20-question form.

### 4.4 Timeouts — what limits a long-blocking call

DOCUMENTED from the binary (`strings`, minified):

```js
function rr(e){let n=(e?.timeout>=1000?e.timeout:void 0)??MCP_TOOL_TIMEOUT??Gr; return Math.min(Math.max(n,1000),tc)}   // wall clock
var Jr=300000, Xr=1800000, Zr=new Set(["sse-ide","ws-ide","sdk"]);
function Qr(e){let r=e?.type??"stdio"; if(Zr.has(r))return 0;                                                          // idle: none for sdk
  let n=CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT??(r==="stdio"?Xr:Jr); if(n<=0)return 0; …; return Math.min(Math.max(n,s,1000),rr(e))}
```
with `tc=2147483647` and (most likely) `Gr=1e8` ms ≈ 27.8 h default wall clock. Auto-backgrounding
of MCP calls is off for non-interactive sessions unless `CLAUDE_AUTO_BACKGROUND_TASKS` is set, and
`disableAutoBackground:true` on an SDK server is documented as "Meant for a server whose calls wait
on a person".

| run | setup | outcome |
|---|---|---|
| exp05-idle60 | stdio, `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=60000`, no progress, answer after 150 s | **fails at 60 s**: `"MCP server \"askuser\" tool \"ask_user\" sent no response or progress for 60s; aborting. If this server is configured in your MCP settings, set a per-server \"timeout\" (ms) … otherwise set CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT (ms) globally (0 disables)."` — the model retried once, failed again, then asked in plain text |
| exp05-idle60p | same + `notifications/progress` every 20 s | **survives**, answer at 151 s delivered (VERIFIED: progress resets the idle clock) |
| exp05-tt30 | `MCP_TOOL_TIMEOUT=30000` | fails at 30 s with the *idle* message (the wall clock caps the idle timeout) |
| exp06-tmo60 | SDK server, `sdkMcpServerConfigs.timeout=60000` | fails at 60 s: `timed out after 60s` + `notifications/cancelled` |
| exp11-90 | stdio elicitation held 90 s with `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=30000` | **survives** (result at 92.5 s): a pending elicitation pauses the idle clock |
| exp03C | AskUserQuestion `can_use_tool` held 600 s | **survives** |
| exp06-hold20m | SDK server, default timeout, `disableAutoBackground`, answer after 1200 s | **survives**: tool_result at t=1205 s, `result` success at 1207.5 s (VERIFIED) |
| exp05-default40m | stdio, all defaults, no progress, answer after 2400 s | **fails at 1800 s** with `"… sent no response or progress for 1800s; aborting …"` (VERIFIED the 30-min stdio default); the model immediately re-called `ask_user` (run stopped there) |
| exp05-default40mp | stdio, defaults, progress every 60 s, answer after 2400 s | **survives** 40 min: 40 progress notifications, answer delivered at t=2406 s, `result` success (VERIFIED) |

Recommendation (INFERRED): for hours-long waits use the SDK-server variant (no idle timeout; set
`sdkMcpServerConfigs.timeout` explicitly, e.g. 24 h, and `disableAutoBackground:true`), or an
external server that sends progress every ≤60 s and has a per-server `timeout`. Mind the prompt
cache: requests use 1 h cache TTL (`ephemeral_1h_input_tokens`), so an answer after > 1 h re-creates
the cache (costlier first request, still works).

---

## 5. Durability

### 5.1 Where things live (VERIFIED)

* Transcript: `$CLAUDE_CONFIG_DIR/projects/<escaped cwd>/<session-id>.jsonl`, escaping = every
  non-alphanumeric char → `-` (`/tmp/claude-0/-root-SocratesAgent/…/exp07-112707` →
  `-tmp-claude-0--root-SocratesAgent-…-exp07-112707`). Default config dir `~/.claude`.
* Subagents: `<escaped cwd>/<session-id>/subagents/agent-<id>.jsonl` + `.meta.json`.
* Per-session misc: `session-env/<sid>/`, `sessions/<pid>.json` (live-process registry: pid,
  sessionId, cwd, status, messaging socket), `shell-snapshots/`, background task output in
  `/tmp/claude-0/<escaped cwd>/<sid>/tasks/<task>.output`, plan files in `$CLAUDE_CONFIG_DIR/plans/`.
  `file-history/` was **not** written in `-p` mode.
* The transcript holds many line types besides messages: `queue-operation, user, assistant, attachment,
  atis-latch, api-request-shape, api-request-blob, api-request, last-prompt, mode, cost-state, system`.
  Message lines carry `uuid, parentUuid, isSidechain, promptId, sessionId, cwd, version, gitBranch,
  entrypoint:"sdk-cli", permissionMode, timestamp`; one API message → one line per content block.

### 5.2 Kill / resume experiments (VERIFIED, exp07)

| scenario | what is on disk / what resume does |
|---|---|
| SIGKILL between turns | nothing lost; `--resume <id>` keeps the **same id** and appends to the same file |
| SIGKILL mid-stream (text deltas flowing) | partial assistant text **not persisted**; on next resume the CLI appends a synthetic assistant `{"model":"<synthetic>","content":[{"type":"text","text":"No response requested."}]}` |
| SIGKILL while `can_use_tool` (Write) was pending | `tool_use` is persisted; resume appends a synthetic `tool_result{is_error:true,"content":"[Tool call interrupted: the session ended before this call's result was recorded, so its outcome is unknown. Check whether it took effect before relying on it or running it again.]"}` with `"toolDenialKind":"interrupted"`; the model correctly concluded the file was not written. The pending prompt is **not** re-asked. |
| `--resume <id> --fork-session` | new id (`9b6e973f…`), new file with the copied history, original untouched |
| `--resume <unknown id>` | 2.1.309 **fails fast**: stderr `No conversation found with session ID: <id>`, `result{subtype:"error_during_execution",is_error:true,num_turns:0}`, exit 1 within ~1 s (2.1.252 stayed silent until the first turn) |
| interrupt (not kill) mid-stream | partial text **is** persisted, followed by user text `[Request interrupted by user]` |

INFERRED: a pending AskUserQuestion / `ask_user` MCP call at kill time behaves like the Write case
(synthetic "interrupted" tool_result); the web UI must therefore keep its own record of the open
question and, after a restart, re-pose it — e.g. by sending the user's eventual answer as a normal
user message ("Answer to your earlier question: …"). Because the CLI dies with its stdout pipe, a Go
server restart kills the turn unless the claude process is decoupled from the server (a small
pipe-holding shim or a FIFO pair); closing stdin alone lets the current turn finish (exp13).

### 5.3 Rebuilding the UI history (VERIFIED)

`rebuild_history.py <jsonl>` renders all `user`/`assistant` lines in file order and marks the
`system/compact_boundary`. Rendering the parentUuid chain alone loses everything before a
`/compact` (the chain restarts at the compact summary), so render in file order. Things to filter or
style: `<command-name>…`/`<local-command-caveat>`/`<local-command-stdout>` user lines (slash
commands), `isCompactSummary` user line, `<synthetic>` assistant lines, the subagent hand-back
tool_result. Thinking text is empty (§1.3). Live state that is *not* in the jsonl in a convenient
form: pending prompts, rate limits, current model.

---

## 6. Account switching

### 6.1 Experiments (VERIFIED, exp08; A and B are copies of this machine's single login)

| step | result |
|---|---|
| 1. new session under `CLAUDE_CONFIG_DIR=A` (only `.credentials.json` copied in) | works; A gets `.claude.json`, `projects/<esc>/<sid>.jsonl`, `session-env/<sid>`, `sessions/`, `policy-limits*`, `remote-settings*`, `backups/` |
| 2. `--resume <sid>` under B without copying | fails fast: `No conversation found with session ID` |
| 3. copy **only** `projects/<esc>/<sid>.jsonl` to B, resume under B | works ("4711" remembered), B appends to **its** copy |
| 4. resume under A again | works but A's copy has **diverged** (doesn't know step 3's turn) |
| 5. `C2/projects` → symlink to `A/projects`, resume under C2 | works, shared file |
| 6. empty dir C + `CLAUDE_CODE_OAUTH_TOKEN=<access token>` | works; `initialize.account = {"tokenSource":"CLAUDE_CODE_OAUTH_TOKEN","apiProvider":"firstParty"}` (no email/org) |

What breaks / caveats: copies diverge (so move, don't copy — or share `projects/`); `session-env`,
`file-history` (off in -p anyway), plans and memory (`projects/<esc>/memory/`) are per dir; resume
under a different **account** was not testable (only one login exists) — the prompt cache is per
org so the first request after a switch re-creates the cache (INFERRED); server-side features tied
to the account (claude.ai connectors, Remote Control) differ.

### 6.2 Credential-only env vars (DOCUMENTED, https://code.claude.com/docs/en/authentication)

Precedence: cloud provider vars → `ANTHROPIC_AUTH_TOKEN` → `ANTHROPIC_API_KEY` (always used in `-p`
when present) → `apiKeyHelper` → **`CLAUDE_CODE_OAUTH_TOKEN`** ("A long-lived OAuth token generated
by `claude setup-token`") → Anthropic profiles → `/login` credentials. `claude setup-token` "generate[s]
a one-year OAuth token … It does not save the token anywhere". Such a token "can only make model
requests, so it can't establish Remote Control sessions or fetch claude.ai connectors". `--bare` does
not read it. No setup-token existed on this machine, so only the env mechanism was tested (step 6,
with the current access token). Docs also say: "To stay signed in to multiple accounts at once …
give each account its own configuration directory … set CLAUDE_CONFIG_DIR".

INFERRED design: one shared config dir (sessions in one place), one `CLAUDE_CODE_OAUTH_TOKEN` per
account kept by the server, chosen per spawned process; to switch account mid-chat, end the process
and `--resume <id>` with the other token. Make sure `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` are
absent from the child env, they outrank the token.

---

## 7. Other things a web UI needs (VERIFIED unless marked)

* **Slash commands in headless**: sent as plain user text. `/shout hello web` (project command in
  `.claude/commands/shout.md`) → "HELLO WEB"; `/context` → markdown table as `result`;
  `/model sonnet` → `"Set model to \`Sonnet 5.5\` for this session only"` (`num_turns:0`);
  `/compact` → `system/status{status:"compacting"}` → `system/compact_boundary{compact_metadata:{trigger:"manual",pre_tokens:25062,post_tokens:3778,…}}`
  → summary user message → `<local-command-stdout>Compacted </local-command-stdout>`;
  `/clear` → `{"type":"conversation_reset","new_conversation_id":"c0627df7-…","trigger":"clear",…}`
  and the **next `init` has a different `session_id`** (`e98ad4ba…`, new transcript file) — the host
  must re-key the chat to the id seen in `init`/`result` (note `new_conversation_id` did not equal it).
  `init.slash_commands` / `initialize.commands` list what is available; `terminal_slash_commands`
  (doctor, color, focus, reload-plugins) are TUI-only.
* **Plan mode**: `set_permission_mode{mode:"plan"}`; the model wrote a plan file to
  `$CLAUDE_CONFIG_DIR/plans/<slug>.md`, then `ExitPlanMode` arrived as
  `can_use_tool{tool_name:"ExitPlanMode","input":{"plan":"1. …","planFilePath":"…"}}`; allowing it
  switched the mode back (`system/status{permissionMode:"default"}`) with tool_result "User has
  approved your plan…". Deny = keep planning. `initialize.planModeInstructions` can replace the plan workflow (DOCUMENTED).
* **Todo list**: no `TodoWrite` by default in 2.1.309. With `CLAUDE_CODE_ENABLE_TODO_TOOLS=1` the
  model gets `TaskCreate/TaskGet/TaskList/TaskUpdate`; state is only visible as tool calls/results:
  `TaskCreate{"subject":"a","description":"…"}` → `{"task":{"id":"1","subject":"a"}}`,
  `TaskUpdate{"taskId":"1","status":"completed"}` → `{"success":true,"statusChange":{"from":"pending","to":"completed"}}`.
  (`CLAUDE_CODE_ENABLE_TASKS=1` did not expose them.)
* **Subagents**: `Agent` tool_use → `system/task_started{task_type:"local_agent",subagent_type,spawn_depth,prompt,tool_use_id}`
  → subagent messages with `parent_tool_use_id:"toolu_…"` → `task_progress{usage:{total_tokens,tool_uses,duration_ms}}`
  → `task_updated{patch:{status:"completed"}}` → `task_notification` → hand-back tool_result.
  `result.subagent_stats` summarises. Background Bash tasks use the same `task_*` family (`task_type:"local_bash"`).
* **Usage / cost**: per turn in `result` (`usage`, `model_usage_since_last_result`), cumulative in
  `total_cost_usd`/`modelUsage`, on demand via `get_usage` (subscription windows + percent) and
  `get_context_usage`. `costUSD` is list-price estimate even on a subscription (`costBasis:"list"`).
* **Rate limits**: `rate_limit_event` after requests (§1.3); `api_retry` system messages on retries (DOCUMENTED).
* **Permission modes**: `default` (CLI alias `manual`), `acceptEdits`, `auto` (classifier), `plan`,
  `dontAsk` (deny anything not pre-approved), `bypassPermissions` (needs `--allow-dangerously-skip-permissions` to switch into); switchable live.
* **Harness guards**: a foreground `sleep 40; echo` was blocked (`<tool_use_error>Blocked: sleep 40 …`)
  and the model re-ran it in the background — expect such tool errors in the stream.
* **System prompt**: in `-p` the CLI identifies as "You are a Claude agent, built on Anthropic's Claude
  Agent SDK." and states output is "displayed to the user as Github-flavored markdown in a terminal";
  `--append-system-prompt` adds to it (VERIFIED via proxy capture + behaviour).

---

## 8. Policy (DOCUMENTED, fetched 2026-10-23)

**Claude Code — Legal and compliance**, https://code.claude.com/docs/en/legal-and-compliance, section
"Authentication and credential use" (verbatim):

> **OAuth authentication** is intended exclusively for purchasers of Claude Free, Pro, Max, Team, and
> Enterprise subscription plans and is designed to support ordinary use of Claude Code and other
> native Anthropic applications.

> **Developers** building products or services that interact with Claude's capabilities, including
> those using the Agent SDK, should use API key authentication through Claude Console or a supported
> cloud provider. Anthropic does not permit third-party developers to offer Claude.ai login into their
> own applications, or to route requests through Free, Pro, or Max plan credentials on behalf of their
> users. Moreover, developers may not collect, store, or intermediate Claude.ai credentials or session
> tokens — sign-in to a Claude account must complete through Anthropic's own flow.

> This does not restrict how customers provision and manage their own API keys … Nor does it prevent
> an end user from signing in to the unmodified Claude Code binary with their own Claude subscription,
> including where a platform hosts Claude Code as described under *Can customers offer Claude Code in
> their products?* above.

> Anthropic reserves the right to take measures to enforce these restrictions and may do so without prior notice.

Same page, "Usage policy": "Advertised usage limits for Pro and Max plans assume ordinary, individual
usage of Claude Code and the Agent SDK." And "Can customers offer Claude Code in their products?":
"**The Claude Code binary must not be modified.** … customers may not remove, disable, or restrict any
authentication method built into it" and "**Customers may not pay for, resell, or intermediate Claude
usage on their end users' behalf.** Each end user must authenticate with their own Anthropic API key,
Claude subscription plan credentials, or 3P inference provider credential".

**Agent SDK overview**, https://code.claude.com/docs/en/agent-sdk/overview (verbatim note):

> Unless previously approved, Anthropic does not allow third party developers to offer claude.ai login
> or rate limits for their products, including agents built on the Claude Agent SDK. Use the API key
> authentication methods described in the Quickstart instead.

**Help center**, https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan
(the fetch tool returned only summaries/fragments; quoted fragments): June 15 2026 update — "We've
paused the previously-announced changes to Claude Agent SDK usage." and "Claude Agent SDK, `claude -p`,
and third-party app usage still draw from your subscription limits."; October 7 2026 update — "Claude
Max and Team plans now include monthly API credits, which cover the Claude Agent SDK," and "You can
still use the Claude Agent SDK, `claude -p`, and third-party apps with your subscription limits."
(Re-read this page before relying on it.)

Background: AlternativeTo reported in Feb 2026 that the policy update targets subscription OAuth use
by third-party products (https://alternativeto.net/news/2026/2/anthropic-officially-bans-using-subscription-authentication-for-third-party-claude-use).

INFERRED reading for SocratesAgent (not legal advice): running the **unmodified** `claude` binary
headlessly, on the user's own machine, signed in through Anthropic's own `/login` (or a
`claude setup-token` the user minted themselves), for that user's own individual use, matches the
explicitly permitted "end user signing in to the unmodified Claude Code binary with their own Claude
subscription". It becomes a problem when the open-source tool (a) builds its own claude.ai login
flow or harvests tokens, (b) lets other people use one person's subscription, or (c) is offered as a
hosted service. Keep: no token collection by SocratesAgent beyond what the user pastes for their own
machine, sign-in via `claude auth login` / `claude setup-token`, and say so in the README.

---

## 9. Differences from the 2.1.252 adapter in commit 9b82ce0

* `system/init`: still not at process start, and now emitted **every turn** — don't treat a second
  `init` as a new session (except after `/clear`, where `session_id` really changes).
* Failed `--resume` now fails fast with a `result{is_error:true}` and exit 1 (was: silent until first turn).
* New frames to ignore or use: `command_lifecycle`, `system/status`, `system/thinking_tokens`,
  `rate_limit_event`, `conversation_reset`, `task_*`, `background_tasks_changed`, `control_cancel_request`.
* `result` gains `terminal_reason` (`completed|aborted_streaming|aborted_tools`), `model_usage_since_last_result`,
  `queued_turn_count`, `user_message_uuids`.
* The old argv (`--permission-mode bypassPermissions --setting-sources project,local --replay-user-messages`)
  remains valid; a web UI that wants prompts should switch to `default` + `--permission-prompt-tool stdio`.
