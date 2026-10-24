# 04 — Driving Codex CLI through `codex app-server` (codex-cli 0.164.0)

Research date 2026-10-23, binary `/root/.local/bin/codex` = `codex-cli 0.164.0` (released the same day, tag `rust-v0.164.0`).
Goal: replace the tmux/TUI screen-scrape with the structured protocol so the whole UI lives in the browser, driven by the Go server.

Labels used throughout:

- **VERIFIED** — observed in this session against the installed binary (transcripts under `codex-lab/logs/`).
- **DOCUMENTED** — read in the generated schema, the official docs (`learn.chatgpt.com/docs/app-server`, auth, config reference), the `openai/codex` source on `main`, or GitHub issues.
- **INFERRED** — my conclusion, not observed.

Lab: `/tmp/claude-0/-root-SocratesAgent/d612e239-e189-4f08-9945-19100f29fc4c/scratchpad/codex-lab/`
(`schema/` generated bindings, `docs/` downloaded docs + source excerpts, `logs/` every JSON-RPC line both ways, `exp*.py` experiments, `mockresponses.py`, `mcp_lab.py`, `rpc.py`, `wsrpc.py`).

---

## 0. Important caveat: no working login on this machine — a mock model was used

**VERIFIED.** `~/.codex/auth.json` is a ChatGPT login (plan `prolite`) whose access token expired ~35 days ago (`last_refresh` 2026-09-08). `codex login status` still prints "Logged in using ChatGPT" (it only reads the file), but app-server's `account/read {"refreshToken":true}` fails and returns `{"account":null,"requiresOpenaiAuth":true}`. Stderr: `Failed to refresh token: Your access token could not be refreshed. Please log out and sign in again.` The same check on a temp copy of one `/root/.codex-multi/*` login (team plan, August) also failed. **`~/.codex/auth.json` was not changed** (byte-compare before and after; mtime still 2026-09-08). Temp copies of credentials were deleted afterwards.

So every model-backed experiment ran against **`mockresponses.py`**, a scripted stand-in for the OpenAI Responses API (SSE), registered as a custom provider in temp CODEX_HOMEs:

```toml
model = "gpt-6-astra"
model_provider = "mock"
[model_providers.mock]
name = "mock"
base_url = "http://127.0.0.1:18080/v1"
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
```

The mock picks its "model output" (text, reasoning, `exec_command`, `apply_patch`, `request_user_input`, dynamic or MCP tool calls, `tool_search`, slow streaming) from a keyword in the user message. It also sends ChatGPT-style rate-limit headers (`x-codex-primary-used-percent`, …). This makes the app-server side real: approvals, sandboxing, MCP, persistence, resume and the socket transport all ran in the real binary. Two things stayed unverified: **real-model behaviour**, and **everything that needs a live OpenAI account** (real rate limits, a completed login, and cross-account replay of encrypted reasoning).

A side effect of the mock: it logs every request codex sends to the model. So the exact tool set each model sees is VERIFIED (see §3.0).

To get a working real login, the device-code flow below is the right one for a phone-driven headless box. The user has to complete it; the shapes are in §5.

---

## 1. Transport, handshake, threads, turns, streaming

### 1.1 Transport — VERIFIED + DOCUMENTED

- Default `codex app-server` = **stdio, newline-delimited JSON**, JSON-RPC 2.0 **without** the `"jsonrpc":"2.0"` field (VERIFIED, every log).
- `--listen ws://IP:PORT` (one JSON message per text frame; also `GET /readyz`, `/healthz`), `--listen unix://` (default control socket) or `--listen unix://PATH`. Both carry **WebSocket over the socket** (HTTP Upgrade). `--listen off` exposes no local transport. The `--ws-auth capability-token|signed-bearer-token` flags secure non-loopback WS. The docs say the app-server and WS transport are "experimental and aren't supported for production workloads". When the WS ingress queue is full you get error `-32001 "Server overloaded; retry later."` (DOCUMENTED).
- VERIFIED: `--listen unix://<long path>` creates a **symlink** at PATH pointing to `/tmp/codex-daemon-<uid>/<sha256>`, which keeps it under the 108-byte AF_UNIX limit. The upgrade answer was `HTTP/1.1 101 Switching Protocols … x-codex-websocket-max-unfragmented-message-bytes: 16777216`.
- VERIFIED: `codex app-server proxy --sock PATH` is a **raw byte pipe**: `codex_stdio_to_uds::run`, per `cli/src/main.rs`. Sending JSONL through it gets no answer, because the socket expects a WebSocket handshake. A Go server should dial the unix socket itself with a WS client (`wsrpc.py` is a 120-line reference client). The proxy only helps a client that already speaks WebSocket over stdio.
- VERIFIED: **with stdio, closing stdin kills the server instantly** (`exited 0.0s after stdin EOF`), including a turn in flight. That is the decisive argument for the socket transport (§4).

### 1.2 Handshake — VERIFIED

```json
>> {"id":1,"method":"initialize","params":{"clientInfo":{"name":"socrates-lab","title":null,"version":"0.0.1"},
     "capabilities":{"experimentalApi":true,"requestAttestation":false}}}
<< {"id":1,"result":{"userAgent":"socrates-lab/0.164.0 (Ubuntu 26.4.0; x86_64) tmux-256color (socrates-lab; 0.0.1)",
     "codexHome":"/…/homes/A","platformFamily":"unix","platformOs":"linux"}}
>> {"method":"initialized"}
```

- `capabilities` also accepts `optOutNotificationMethods: [..]` (suppress noisy notifications per connection), `extensions`, `mcpServerOpenaiFormElicitation`, and `explicitGatewayOauth` (DOCUMENTED, schema).
- **`experimentalApi: true` is required** for `dynamicTools`, `collaborationMode`, `thread/backgroundTerminals/*`, `chatgptAuthTokens` and the others; without it they are rejected with `<descriptor> requires experimentalApi capability` (DOCUMENTED). `request_user_input` itself is marked EXPERIMENTAL in the schema.
- Methods with no params still need `"params":{}`. VERIFIED: `thread/loaded/list` without params → `-32600 Invalid request: missing field 'params'`.
- `clientInfo.name` becomes the `originator` header and the thread `originator` field. On a shared socket server the **first** client's name sticks in `userAgent` (VERIFIED: client-two got `client-one/0.164.0 …`).

### 1.3 Protocol surface (authoritative, generated)

`codex app-server generate-ts --out` and `generate-json-schema --out`, each with and without `--experimental`. Output is in `codex-lab/schema/{ts,ts-exp,json,json-exp}` (2433 files). Method lists, from `ClientRequest.ts`, `ServerRequest.ts` and `ServerNotification.ts`:

- **Client→server (stable, 113)**: `initialize`;
  - threads: `thread/start|resume|fork|archive|unarchive|delete|unsubscribe|read|list|turns/list|items/list|loaded/list|name/set|metadata/update|compact/start|shellCommand|revert|inject_items|goal/*|attachment/*`;
  - turns: `turn/start|steer|interrupt`, `review/start`;
  - discovery: `model/list`, `modelProvider/capabilities/read`, `experimentalFeature/list`, `permissionProfile/list`;
  - account: `account/read|login/start|login/cancel|logout|rateLimits/read|usage/read|rateLimitResetCredit/consume|workspaceMessages/read|sendAddCreditsNudgeEmail`, `getAuthStatus`;
  - config: `config/read|value/write|batchWrite`, `configRequirements/read`;
  - MCP: `mcpServerStatus/list`, `config/mcpServer/reload`, `mcpServer/tool/call|resource/read|oauth/login`;
  - other: `skills/*`, `hooks/list`, `plugin/*`, `app/*`, `fs/*`, `command/exec*`, `fuzzyFileSearch`, `feedback/*`, …
- **Experimental-only extras**: `collaborationMode/list`, `thread/queue/*`, `thread/search`, `thread/settings/update`, `turn/settings/update`, `process/*`, `project/*`, `remoteControl/*`, `memory/*`, `thread/backgroundTerminals/*`, `rollout/compress`, `thread/realtime/*`, `userVerification/*`, …
- **Server→client requests**: `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/tool/requestUserInput`, `mcpServer/elicitation/request`, `item/permissions/requestApproval`, `item/tool/call` (dynamic tools), `account/chatgptAuthTokens/refresh`, `attestation/generate`. Also the legacy v1 `applyPatchApproval` / `execCommandApproval`, and (experimental) `currentTime/read`.
- **Notifications (88)**:
  - thread: `thread/started|status/changed|readState/changed|archived|closed|name/updated|settings/updated|tokenUsage/updated|compacted|goal/*`;
  - turn: `turn/started|completed|diff/updated|plan/updated`;
  - item: `item/started|completed`, `item/agentMessage/delta`, `item/plan/delta`, `item/reasoning/summaryTextDelta|summaryPartAdded|textDelta`, `item/commandExecution/outputDelta|terminalInteraction`, `item/fileChange/outputDelta|patchUpdated`, `item/mcpToolCall/progress`;
  - other: `serverRequest/resolved`, `account/updated|rateLimits/updated|login/completed`, `mcpServer/startupStatus/updated`, `model/rerouted`, `error`, `warning`, `deprecationNotice`, `configWarning`, …

### 1.4 thread/start — VERIFIED

```json
>> {"id":3,"method":"thread/start","params":{"cwd":"/…/work/a","approvalPolicy":"on-request","sandbox":"workspace-write"}}
<< {"id":3,"result":{"thread":{"id":"01a1687f-1a70-…","sessionId":"01a1687f-1a70-…","preview":"","ephemeral":false,
     "historyMode":"paginated","modelProvider":"mock","model":"gpt-6-astra","reasoningEffort":"low",
     "status":{"type":"idle"},"path":"/…/homes/A/sessions/2026/10/23/rollout-2026-10-23T11-22-02-01a1687f-….jsonl",
     "cwd":"/…/work/a","cliVersion":"0.164.0","originator":"socrates-lab","source":"vscode","name":null,"turns":[],…},
   "model":"gpt-6-astra","modelProvider":"mock","approvalPolicy":"on-request","approvalsReviewer":"user",
   "sandbox":{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":false},
   "reasoningEffort":"low","multiAgentMode":"explicitRequestOnly","cwd":"…",…}}
<< {"method":"thread/started","params":{"thread":{…same…}}}
```

- Params (schema): `model`, `modelProvider`, `serviceTier`, `cwd`, `approvalPolicy`, `approvalsReviewer`, `sandbox` (`read-only|workspace-write|danger-full-access`), `permissions` (named profile), `config` (arbitrary config.toml overrides as JSON), `baseInstructions`, `developerInstructions`, `ephemeral`, `dynamicTools` (experimental), `environments`, `projectId`, …
- VERIFIED: `thread/start` **writes `[projects."<cwd>"] trust_level = "trusted"` into `$CODEX_HOME/config.toml`**. That is why the real `~/.codex/config.toml` has dozens of such entries. Expect config.toml churn per workspace.
- VERIFIED: `"config":{"model_reasoning_summary":"detailed"}` on thread/start showed up in the model request as `reasoning.summary:"detailed"`.

### 1.5 turn/start and the event stream — VERIFIED

```json
>> {"id":4,"method":"turn/start","params":{"threadId":"…","input":[{"type":"text","text":"SAY ping","text_elements":[]}]}}
<< {"id":4,"result":{"turn":{"id":"01a1687f-1a84-…","rootTurnId":"…","items":[],"itemsView":"notLoaded","status":"inProgress",…}}}
```

Observed order for a plain answer (`logs/01-basic.jsonl`):

```
thread/status/changed {"status":{"type":"active","activeFlags":[]}}
turn/started          {"turn":{"id":…,"status":"inProgress",…}}
item/started          {"item":{"type":"userMessage","id":…,"content":[{"type":"text","text":"SAY ping",…}]},"threadId","turnId","startedAtMs"}
item/completed        (same userMessage)
item/started          {"item":{"type":"reasoning","id":"rs_1","summary":[],"content":[]}}
item/reasoning/summaryPartAdded {"itemId":"rs_1","summaryIndex":0,…}
item/reasoning/summaryTextDelta {"itemId":"rs_1","delta":"**Thinking** ","summaryIndex":0,…}   (xN)
item/completed        {"item":{"type":"reasoning","id":"rs_1","summary":["**Thinking** about pong"],"content":[]}}
item/started          {"item":{"type":"agentMessage","id":"msg_1","text":"","phase":null,"delivery":null,"questions":null,…}}
item/agentMessage/delta {"threadId","turnId","itemId":"msg_1","delta":"pong"}
item/completed        {"item":{"type":"agentMessage","id":"msg_1","text":"pong",…}}
thread/tokenUsage/updated {"threadId","turnId","tokenUsage":{"total":{"totalTokens":1230,"inputTokens":1200,"cachedInputTokens":1000,
                         "cacheWriteInputTokens":0,"outputTokens":30,"reasoningOutputTokens":12},"last":{…},"modelContextWindow":258400}}
account/rateLimits/updated {"rateLimits":{"limitId":"codex","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1792758122},
                         "secondary":{"usedPercent":42,"windowDurationMins":10080,"resetsAt":…},"credits":null,"planType":null,…}}
thread/readState/changed, thread/status/changed {"status":{"type":"idle"}}
turn/completed        {"threadId","turn":{"id","items":[<summary items>],"itemsView":"summary","status":"completed","error":null,
                         "startedAt","completedAt","durationMs":84}}
```

- Every notification also carries `emittedAtMs`.
- `turn/completed.turn.items` holds only a **summary** (the final agentMessage). Use the `item/*` stream or `thread/turns/list itemsView:"full"` for the full transcript.
- Turn statuses: `completed | interrupted | failed | inProgress`. A failure is `turn.error = {message, codexErrorInfo, additionalDetails}`. `codexErrorInfo` is one of `usageLimitExceeded`, `rateLimitExceeded`, `contextWindowExceeded`, `unauthorized`, `serverOverloaded`, `{httpConnectionFailed:{httpStatusCode}}`, `{responseStreamDisconnected:{…}}`, … There is also a separate `error` notification `{error, willRetry, threadId, turnId}` (schema).
- Thread status: `notLoaded | idle | systemError | active{activeFlags:["waitingOnApproval"|"waitingOnUserInput"]}`. VERIFIED: `waitingOnApproval` is set while an approval is pending. This is the "needs you" badge.
- **Rate limits arrive for free on every model response** as `account/rateLimits/updated`, parsed from response headers. VERIFIED with the mock's headers. `account/rateLimits/read` needs ChatGPT auth: with the mock provider it answered `-32600 codex account authentication required to read rate limits`, and with an API key `chatgpt authentication required to read rate limits` (VERIFIED).

Command execution (VERIFIED, classic tool set). The live output arrives as `item/commandExecution/outputDelta {"itemId":"call_…","delta":"line2\n"}`, and the final item is:

```json
{"type":"commandExecution","id":"exec-…","command":"/bin/bash -lc 'touch approved.txt && echo made-it'","cwd":"…",
 "processId":"6784","source":"unifiedExecStartup","status":"completed",
 "commandActions":[{"type":"unknown","command":"touch approved.txt && echo made-it"}],
 "aggregatedOutput":"made-it\n","exitCode":0,"durationMs":0}
```

The first chunk of output can come only in `aggregatedOutput` (VERIFIED: `line1` was missing from the deltas but present in the final item).

File changes / diffs (VERIFIED):

```json
item/started   {"item":{"type":"fileChange","id":"exec-…","changes":[{"path":"/…/patched.txt","kind":{"type":"add"},"diff":"hello from mock\n"}],"status":"inProgress"}}
item/completed {… "status":"completed"}
turn/diff/updated {"threadId","turnId","diff":"diff --git a/patched.txt b/patched.txt\nnew file mode 100644\n…\n+hello from mock\n"}
```

`kind` is one of `add | delete | update{move_path}`. `turn/diff/updated` is the **aggregated unified diff of the whole turn**, re-sent as it changes.

Plans / todos:

- VERIFIED: in Plan collaboration mode, a `<proposed_plan>…</proposed_plan>` block in the model text becomes a separate item: `item/started {"type":"plan","id":"<turnId>-plan","text":""}`, then `item/plan/delta` ×N, then `item/completed {"type":"plan","text":"# Rename button\n- Step one…"}`. The agentMessage keeps only the prose outside the block.
- VERIFIED: **no `update_plan` tool is offered to the model in 0.164** (`gpt-5.5`, `gpt-6-astra` and `gpt-6.1-sol` were all checked). The `turn/plan/updated {threadId,turnId,explanation,plan:[{step,status}]}` notification exists in the schema but was never emitted. INFERRED: it is legacy, or only fires for models that still get `update_plan`.
- Goals (`thread/goal/*`, plus the `create_goal|update_goal|get_goal` model tools) are the current persistent-objective feature.

### 1.6 Interrupt, steer — VERIFIED

- `turn/interrupt {"threadId","turnId"}` → `{}`; then `turn/completed` with `status:"interrupted"`. The rollout records `turn_aborted {"reason":"interrupted"}` and a developer message `<turn_aborted>` for the model.
- Interrupting while a **dynamic tool call is pending** completes it as `{"type":"dynamicToolCall","status":"failed","success":false,…}`. A late client reply to that request is silently ignored. No `serverRequest/resolved` was emitted for this cleanup (the docs promise one only for `requestUserInput`).
- `turn/steer {"threadId","input":[…],"expectedTurnId":"<active turn>"}` → `{"turnId":…}`. It adds user input to the running turn; in the test the steered turn finished right after.

---

## 2. Server→client requests: approvals and asking the user

### 2.1 Command approval, end to end — VERIFIED (`logs/02-approval.jsonl`)

Setup: thread with `approvalPolicy:"untrusted"`, `sandbox:"read-only"`; the mock asks to run `touch approved.txt && echo made-it`.

```json
<< {"method":"thread/status/changed","params":{"threadId":"…","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}
<< {"method":"item/started","params":{"item":{"type":"commandExecution","id":"exec-a37f…","command":"/bin/bash -lc 'touch approved.txt && echo made-it'",
     "status":"inProgress","processId":null,"source":"agent",…}}}
<< {"id":0,"method":"item/commandExecution/requestApproval","params":{"kind":"command","threadId":"…","turnId":"…","itemId":"exec-a37f…",
     "startedAtMs":1792754582037,"environmentId":"local","command":"/bin/bash -lc 'touch approved.txt && echo made-it'","cwd":"…",
     "commandActions":[{"type":"unknown","command":"touch approved.txt && echo made-it"}],
     "proposedExecpolicyAmendment":["touch","approved.txt"],
     "availableDecisions":["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["touch","approved.txt"]}},"cancel"]}}
>> {"id":0,"result":{"decision":"accept"}}                       (sent 2 s later)
<< {"method":"serverRequest/resolved","params":{"threadId":"…","requestId":0}}
<< {"method":"item/completed","params":{"item":{…,"status":"completed","aggregatedOutput":"made-it\n","exitCode":0}}}
```

- `approved.txt` was created.
- Replying `{"decision":"decline"}` gave `item/completed` with `"status":"declined"`; the model got `exec_command failed: CreateProcess { message: "Rejected(\"rejected by user\")" }` and the file was not created. Note that `decline` was **not** in `availableDecisions` but was accepted.
- Decisions (schema): `accept | acceptForSession | {acceptWithExecpolicyAmendment} | {applyNetworkPolicyAmendment} | decline | cancel`.

File-change approval — VERIFIED:

```json
<< {"id":2,"method":"item/fileChange/requestApproval","params":{"threadId","turnId","itemId":"exec-2537…","startedAtMs":…,"reason":null,"grantRoot":null}}
>> {"id":2,"result":{"decision":"accept"}}
```

The decisions are `accept | acceptForSession | decline | cancel`. The diff itself comes in the preceding `item/started` fileChange item, not in the request.

Other approval-type requests (schema/docs): `item/permissions/requestApproval` (answer with the granted subset, `scope:"turn"|"session"`), and MCP tool-call approvals. VERIFIED: with `approvalPolicy:"on-request"`, calling an MCP tool first sent `mcpServer/elicitation/request` with `"mode":"form"`, `"_meta":{"codex_approval_kind":"mcp_tool_call","persist":["session","always"],"tool_params":{…}}`, `"message":"Allow the lab MCP server to run tool \"ask_user_elicit\"?"`. With `approvalPolicy:"never"`, elicitations are **auto-declined** (VERIFIED: the tool got `{"action":"decline"}`). `approvalPolicy` can also be `{"granular":{"sandbox_approval","rules","skill_approval","request_permissions","mcp_elicitations"}}`. `approvalsReviewer:"auto_review"` routes approvals to an AI reviewer instead of the user.

Pending requests are long-lived and survive a client gap — VERIFIED; see §4.3.

### 2.2 What codex offers natively for "the model asks the user"

Three native mechanisms, all VERIFIED:

**(a) `request_user_input` → `item/tool/requestUserInput` (blocking, structured).** The model sees this tool:

```
request_user_input(questions: [{id (snake_case), header (<=12 chars), question, options: [{label (1-5 words), description}] (2-3, recommended first, "(Recommended)" suffix; no "Other")}])
"Request user input for one to three short questions and wait for the response. This tool is only available in Plan mode."
```

- In Default mode it fails with `request_user_input is unavailable in Default mode` (VERIFIED), unless app-server is started with `--enable default_mode_request_user_input` (underDevelopment flag, emits a `warning`; VERIFIED working, then `isBlocking:false`).
- In Plan mode (`turn/start … "collaborationMode":{"mode":"plan","settings":{"model":"gpt-6-astra","reasoning_effort":"low","developer_instructions":null}}`):

```json
<< {"id":0,"method":"item/tool/requestUserInput","params":{"threadId","turnId","itemId":"call_…",
     "questions":[{"id":"color","header":"Color","question":"Which color should the button be?","isOther":true,"isSecret":false,
                   "options":[{"label":"Blue (Recommended)","description":"Matches the brand"},{"label":"Red","description":"Stands out"}]}],
     "isBlocking":true,"autoResolutionMs":null}}
>> {"id":0,"result":{"answers":{"color":{"answers":["Blue (Recommended)"]}}}}
```

- The model then received `{"answers":{"color":{"answers":["Blue (Recommended)"]}}}`.
- **Answered after 180 s: worked**, with the thread flag `waitingOnUserInput` and **no extra model calls while waiting** (2 model requests total, at t+0 and t+180).

**(b) `request_user_input_async` (non-blocking; offered only to the code-mode models, gpt-6-*).**

- Schema: `questions:[{title, options?: string[]}]`, described as "returns immediately without ending the turn … any reply arrives asynchronously as a new user message". VERIFIED: it surfaces as an **agentMessage item with `"delivery":"async"`, `"phase":"final_answer"`**, `text: "Should I also update the docs?\n- Yes\n- No"`, and `"questions":[{"title":"Should I also update the docs?","options":["Yes","No"]}]`. The tool returned `{"accepted":true}` to the model and the turn continued.
- The UI answers with a normal `turn/start` (or `turn/steer` if a turn is running).

**(c) MCP elicitation (`mcpServer/elicitation/request`, mode `form`, JSON-Schema form).** VERIFIED with `mcp_lab.py`, whose tool sends `elicitation/create`:

```json
<< {"id":1,"method":"mcpServer/elicitation/request","params":{"threadId","turnId","serverName":"lab","mode":"form","_meta":null,
     "message":"Deploy now?","requestedSchema":{"type":"object","properties":{"answer":{"type":"string","title":"Answer",
     "description":"Deploy now?","enum":["yes","no"]}},"required":["answer"]}}}
>> {"id":1,"result":{"action":"accept","content":{"answer":"yes"},"_meta":null}}
```

- The MCP tool got `{"action":"accept","content":{"answer":"yes"}}`.
- **Answered after 180 s: worked.** It needs an approval policy that does not auto-decline elicitations.
- Modes in the schema: `form`, `openai/form` / `openaiForm` (opt-in capability), `url`, `openai/userVerification`.

**Recommendation.**

- For rich, Socrates-defined questions (multi-select, free text, secrets, markdown), use a **dynamic tool** (§3).
- Also keep handlers for `requestUserInput` (Plan mode) and for async questions (`delivery:"async"`), because the model will use them on its own.

---

## 3. Custom tools (`ask_user`), blocking for minutes

### 3.0 What the model actually sees (VERIFIED from the requests codex sent the mock)

- **gpt-6-astra / gpt-6.1-sol run in "code mode".** The tool list is sent as an `additional_tools` input item: namespace `functions` with `exec` (custom, JavaScript in a V8 isolate), `wait`, `request_user_input`, `request_user_input_async`; `clock.sleep`; and `collaboration.*` (subagents).
  - `exec_command`, `apply_patch`, `write_stdin`, `view_image`, the goal tools, the MCP tools **and dynamic tools are not top-level**: they are `tools.<name>(…)` inside `exec`, e.g. `await tools.exec_command({cmd})` or `await tools.mcp__lab__ask_user_sleep({...})`.
  - `exec` yields to the model after `yield_time_ms` (default 30000) with `Script running with cell ID 1 …`. The model then has to call `wait`.
  - `--disable code_mode_host` did not change this; it is model-catalog-driven.
- **gpt-5.5 gets the classic tool set** in the request's `tools` field: `exec_command`, `write_stdin`, `request_user_input`, `apply_patch` (custom), `view_image`, `get_goal|create_goal|update_goal`, MCP resource tools, **dynamic tools as top-level functions**, and `tool_search` + `web_search`. MCP tools are **deferred behind `tool_search`** (the model must search first; VERIFIED end to end).
- Request facts: `store:false`, `include:["reasoning.encrypted_content"]`, `reasoning:{effort, context:"all_turns"}`, header `x-openai-internal-codex-responses-lite: true`, and `x-codex-turn-metadata` (thread/turn ids, sandbox mode, workspaces).

### 3.1 Dynamic tools (client-implemented) — VERIFIED

```json
>> thread/start {"…","dynamicTools":[{"type":"function","name":"ask_user","description":"Ask the human a structured question and wait for the answer.",
     "inputSchema":{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}},"multi":{"type":"boolean"}},"required":["question"]}}]}
<< item/started {"item":{"type":"dynamicToolCall","id":"call_…","namespace":null,"tool":"ask_user","arguments":{"question":"Deploy now?","options":["yes","no"]},"status":"inProgress","contentItems":null,"success":null,"durationMs":null}}
<< {"id":0,"method":"item/tool/call","params":{"threadId","turnId","callId":"call_…","namespace":null,"tool":"ask_user","arguments":{"question":"Deploy now?","options":["yes","no"]}}}
>> {"id":0,"result":{"contentItems":[{"type":"inputText","text":"user picked: yes"}],"success":true}}
<< item/completed {"item":{"type":"dynamicToolCall",…,"status":"completed","contentItems":[{"type":"inputText","text":"user picked: yes (after 120.0s)"}],"success":true,"durationMs":120001}}
```

- Experimental: `capabilities.experimentalApi` is required.
- Spec: `{"type":"function",name,description,inputSchema,deferLoading?}` or `{"type":"namespace",name,description,tools:[…]}`.
- The output content can be `inputText`, `inputImage{imageUrl}` or `inputAudio`.
- DOCUMENTED: dynamic tools are **persisted in the rollout metadata and restored on `thread/resume`** when you don't pass new ones. The schema also has a `thread_dynamic_tools` table in `state_5.sqlite`.

**Blocking duration: no timeout observed for dynamic tools.**

| scenario (VERIFIED) | wait | result | model requests during the wait |
|---|---|---|---|
| gpt-5.5 (classic), dynamic `ask_user` | 120 s | completed | **none** (2 total: t+0, t+120) |
| gpt-6-astra (code mode), dynamic `ask_user` | 180 s | completed | **5 polls** (t+31, 62, 93, 124, 155): `exec` yields, the model calls `wait` |
| Plan mode `request_user_input` | 180 s | completed | none |
| MCP tool, default timeout, classic | 90 s | completed | none |
| MCP tool, default timeout, classic | 320 s | **failed at 300 s**: `"error":{"message":"tool call error: tool call failed for \`lab/ask_user_sleep\`\n\nCaused by:\n    timed out awaiting tools/call after 300s"}`, `"status":"failed"`, `durationMs":300001` | none |
| MCP tool, `tool_timeout_sec=20` | 40 s | failed at 20 s (same error text) | – |
| MCP tool, `tool_timeout_sec=600` | 200 s | completed | none |
| MCP tool, code mode, default timeout | 90 s / 180 s (`tool_timeout_sec=600`) | completed | polled every ~31 s |
| MCP elicitation inside a tool (code mode, `on-request`) | 180 s approval + 180 s answer | completed | requests at t+0, 180, 360: none while either elicitation was pending (the `exec` yield/`wait` cycle paused) |

INFERRED: in code mode with a real model, each poll is a full (mostly cached) model request that the model has to decide to make. A real model might also give up, or answer "still waiting". So for **hours-long** waits a blocking tool is a poor fit on gpt-6-*. Better: let the model ask and end the turn (async question / `request_user_input_async`), and deliver the answer as the next `turn/start`. Plan-mode `request_user_input` blocks cheaply on any model.

### 3.2 MCP server as the ask_user transport — VERIFIED

- Configure per process with `-c 'mcp_servers.lab.command="python3"' -c 'mcp_servers.lab.args=["…/mcp_lab.py"]' -c 'mcp_servers.lab.env_vars=["MCP_LAB_LOG","BLOCK_SECS"]' -c 'mcp_servers.lab.tool_timeout_sec=600'`, or in config.toml, or via `thread/start`'s `config` object.
- Items: `{"type":"mcpToolCall","server":"lab","tool":"ask_user_sleep","status":"inProgress|completed|failed","arguments":…,"result":{"content":[…],"structuredContent":null,"_meta":null},"error":null|{message},"durationMs"}`. Startup status comes via `mcpServer/startupStatus/updated`.
- **Timeouts**:
  - `tool_timeout_sec` defaults to **300 s** (VERIFIED, and `codex-mcp/src/rmcp_client.rs: DEFAULT_TOOL_TIMEOUT = 300s`). The online config reference still says "default 60s per-tool timeout", which is stale.
  - `startup_timeout_sec` defaults to 30 s in source (the docs say 10 s).
  - The effective timeout is `min(server tool_timeout, requested)`.
  - `mcp_optional_startup_grace_ms` defaults to 1000.
- Other relevant knobs (DOCUMENTED): `model_providers.<id>.stream_idle_timeout_ms` (300000) only matters while the model is streaming; `thread/shellCommand.timeoutMs` defaults to 1 hour; `account/chatgptAuthTokens/refresh` requests time out after ~10 s.

**Verdict.** Dynamic tools are simpler: no extra process, a typed `item/tool/call` straight to the Go server, and no timeout found. MCP needs `tool_timeout_sec` raised (e.g. 86400) for long waits and an approval policy that allows the call. Either can carry a rich schema.

---

## 4. Durability

### 4.1 Storage — VERIFIED

```
$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ISO-time>-<threadId>.jsonl
```

- The rollout file is append-only JSONL: `session_meta`, `turn_context`, `world_state`, `response_item` (messages, reasoning, `custom_tool_call`, `custom_tool_call_output`, …), `event_msg` (`task_started`, `item_completed`, `token_count`, `task_complete`, `turn_aborted`, …), `token_usage_record`.
- Plus SQLite projections:
  - `state_5.sqlite`: tables `threads`, `thread_dynamic_tools`, `thread_spawn_edges`, `thread_read_receipts`, `projects`, …
  - `thread_history_1.sqlite`: `thread_turns`, `thread_items`. `historyMode:"paginated"`.
- Also `goals_1.sqlite`, `memories_1.sqlite`, `queue_1.sqlite`, `logs_2.sqlite`, `thread-writer-locks/`, `shell_snapshots/`.
- `thread/archive` moves the file into the archived directory; `thread/delete` removes it (DOCUMENTED). `ephemeral:true` threads have `path:null` (VERIFIED).

### 4.2 App-server dies mid-turn → resume — VERIFIED (`exp04_crash.py`)

SIGKILL of app-server during a streaming turn, then a fresh process on the same CODEX_HOME:

- `thread/list {"limit":10}` → `[{id, name:"crash test", status:{"type":"notLoaded"}, preview:"SAY first", …}]`.
- `thread/read {"threadId","includeTurns":true}` → turn 1 `completed` [userMessage, reasoning, agentMessage]; turn 2 **`interrupted`** [userMessage only]. The partially streamed text of the killed turn is lost.
- `thread/turns/list` → pages `{data, nextCursor, backwardsCursor}`, newest first by default, `itemsView: notLoaded|summary|full`.
- `thread/items/list` → `{data:[{turnId, item, startedAtMs, completedAtMs}], …}`.
- `thread/resume {"threadId"}` restores `approvalPolicy:"never"`, `sandbox:{type:"dangerFullAccess"}`, model, effort and collaborationMode from storage. A new `turn/start` works, and the model request carried the whole prior history, including the interrupted user message.
- `thread/resume` without `excludeTurns:true` emits `deprecationNotice "Full-history hydration is deprecated for paginated threads; use excludeTurns: true, then page with thread/turns/list and thread/items/list."` Use `excludeTurns:true` plus the paginated APIs to rebuild a UI.
- Oddity (VERIFIED): a **declined** commandExecution item does not appear in the stored history of that turn (only userMessage and agentMessage).

### 4.3 Long-lived server, clients come and go — VERIFIED (`exp05_socket.py`)

One `codex app-server --listen unix://…/as.sock`; clients connect with WebSocket over the unix socket.

1. Client 1 starts a slow streaming turn and then drops its connection. **The turn keeps running.**
2. Client 2 connects and calls `thread/resume` → thread `status:{"type":"active"}`, the turn `inProgress`. From then on it gets the live `item/agentMessage/delta` (from `tick8` on), `item/completed` (with the **full** text) and `turn/completed`. The already-streamed part of the in-flight item is **not** replayed on resume: the turn showed only the userMessage.
3. Client 2 gets `item/commandExecution/requestApproval id 0` and disconnects without answering. Client 3 connects, `thread/resume` → `activeFlags:["waitingOnApproval"]`, and **the same request (id 0) is re-delivered**. Answering it completes the command and the turn.

DOCUMENTED:

- `thread/unsubscribe`, or a connection with no subscribers, keeps the thread loaded until there are **no subscribers and no activity for 30 minutes**. The thread then unloads, with `thread/status/changed → notLoaded` and `thread/closed`. The config key `thread_unload_delay_secs` exists.
- `thread/loaded/list` (needs `params:{}`) lists loaded thread ids.

### 4.4 `app-server daemon`, `proxy`, control socket (DOCUMENTED, help + `app-server-daemon/README.md` + source; nothing installed or started)

- `daemon start|restart|stop|version|update|enable-remote-control|disable-remote-control|bootstrap`. Every command prints one JSON object.
- `start` is idempotent and returns once app-server answers `initialize` on the **Unix control socket** (`app_server_control_socket_path(CODEX_HOME)`).
- The daemon runs a **managed package** from `CODEX_HOME/packages/app-server-daemon/current/bin/codex`, which `bootstrap` installs. It keeps `app-server.pid`, `settings.json` (`shutdownGraceSeconds` 0–300, default 60; updater `autoUpdateEnabled`, `updateIntervalMinutes`) and `daemon.lock` under `CODEX_HOME/app-server-daemon/`.
- An optional updater auto-updates and restarts the server ("may interrupt running work"). It is not reboot-persistent. `remote_control` pairs it with the ChatGPT desktop/mobile apps.
- The TUI auto-attaches to a running daemon (`daemon_auto_start` feature is stable/on; `codex --remote unix://…|ws://…`).
- `proxy` = stdio↔UDS byte pipe (`codex-stdio-to-uds`), meant for clients that already speak the socket's WebSocket protocol.

**INFERRED recommendation.** Do not use the managed daemon: it auto-updates the binary and rewrites CODEX_HOME packages. Socrates should run **its own `codex app-server --listen unix://<socrates-run-dir>/codex-<profile>.sock` per CODEX_HOME**, in its own systemd scope like today's `socrates-tmux.scope`, so that restarting socrates.service doesn't kill turns. The Go server connects as a WS-over-UDS client, re-resumes its threads on reconnect, re-answers re-delivered server requests, and keeps its own buffer of deltas for browsers. Browsers never talk to codex directly. With stdio, an app-server child of socrates.service would die on every service restart.

---

## 5. Accounts / auth / switching mid-chat

### 5.1 Auth API — VERIFIED in a throwaway CODEX_HOME (`exp07_accounts.py`)

```json
>> account/read {"refreshToken":false}   << {"account":null,"requiresOpenaiAuth":false,"workspaceRouting":null}   (mock provider ⇒ no OpenAI auth needed)
>> getAuthStatus {"includeToken":false,"refreshToken":false}  << {"authMethod":null,"authToken":null,"requiresOpenaiAuth":false}
>> account/login/start {"type":"chatgptDeviceCode"}
<< {"type":"chatgptDeviceCode","loginId":"4fa28d78-…","verificationUrl":"https://auth.openai.com/codex/device","userCode":"CYPN-FQSX0"}
>> account/login/cancel {"loginId":"…"}   << {"status":"canceled"}
<< account/login/completed {"loginId":"…","success":false,"error":"Login was not completed","onboardingEntrypoint":null}
>> account/login/start {"type":"chatgpt"}
<< {"type":"chatgpt","loginId":"…","authUrl":"https://auth.openai.com/oauth/authorize?…&redirect_uri=http%3A%2F%2F127.0.0.1%3A1455%2Fauth%2Fcallback&…&originator=socrates-lab"}
>> account/login/start {"type":"apiKey","apiKey":"sk-lab-not-a-real-key"}   << {"type":"apiKey"}
<< account/login/completed {"loginId":null,"success":true,"error":null}  ;  account/updated {"authMode":"apikey","planType":null}
   → $CODEX_HOME/auth.json = {"auth_mode":"apikey","OPENAI_API_KEY":…}   (the key is not validated at login)
>> account/rateLimits/read  << error -32600 "chatgpt authentication required to read rate limits"
>> account/logout  << {}  ;  account/updated {"authMode":null,"planType":null}  → auth.json deleted
>> account/login/start {"type":"chatgptAuthTokens","accessToken":"<jwt>","chatgptAccountId":"acct-lab","chatgptPlanType":"plus"}
<< {"type":"chatgptAuthTokens"} ; account/updated {"authMode":"chatgptAuthTokens","planType":"plus"}  → nothing written to disk
```

- The browser login's callback goes to `127.0.0.1:1455` on the **server**, so it cannot complete from a phone. **Device code is the right flow for Socrates**: show `verificationUrl` and `userCode` in the web UI, then wait for `account/login/completed`. Per the docs, device-code login must be enabled in the ChatGPT security settings.
- `account/read` reports `account:null` with a non-OpenAI provider even when logged in. With the default provider it returns `{"type":"apiKey"}` or `{"type":"chatgpt","email","planType"}` (DOCUMENTED).
- Other account methods: `account/rateLimits/read` (single and per-`limitId` buckets, `usedPercent`, `windowDurationMins`, `resetsAt`, reset credits), `account/usage/read`, `account/rateLimitResetCredit/consume`, and the `account/updated` and `account/rateLimits/updated` notifications.

API key vs ChatGPT (DOCUMENTED, auth docs):

- **API key**: billed at API rates; ChatGPT rate limits, usage, cloud and some plugin features are not available. Good for CI.
- **ChatGPT (managed)**: codex owns the OAuth tokens in `auth.json`, or the keyring per `cli_auth_credentials_store = file|keyring|auto|ephemeral`, and refreshes them automatically during use. A login unused for long enough dies, as on this machine.
- **`chatgptAuthTokens` (experimental)**: the host supplies the access token and must answer `account/chatgptAuthTokens/refresh` (≈10 s timeout); nothing is stored.
- `forced_login_method = "chatgpt"|"api"` restricts the login type.

**Per-process, not per-thread.** Auth belongs to the app-server process and its CODEX_HOME. INFERRED from the API shape: no per-thread account parameter exists. Switching accounts therefore means either `account/logout` + `account/login/start` on the same server, which affects every thread in that process, or using a different app-server/CODEX_HOME (the existing Socrates "profile" model: one CODEX_HOME per profile).

### 5.2 Moving a thread to another CODEX_HOME — VERIFIED mechanically

The rollout file was copied from `homes/A/sessions/…/rollout-…-<id>.jsonl` to the same relative path under `homes/B`. B's `thread/list` found it immediately (scan-and-repair), `thread/read` pointed at B's copy, and `thread/resume {"threadId", "excludeTurns":true}` restored the policy and sandbox. `thread/turns/list` showed all four turns, a new turn completed, and the replayed history went to the model (22 input items). A's copy stays untouched, so the two copies then diverge. `thread/resume` and `thread/fork` also accept `path` (UNSTABLE) to load from any rollout file.

**The real risk is encrypted reasoning.** DOCUMENTED in user reports, not tested because only one dead account exists here:

- History replays the `reasoning.encrypted_content` and compaction items stored in the rollout (`include:["reasoning.encrypted_content"]`, `store:false`).
- openai/codex issues #25290, #36704, #35334 and #17541 report `400 invalid_encrypted_content … could not be verified` when that content is replayed under a different provider, account or key. One comment states "old encrypted reasoning/compaction payloads are provider/account-bound replay data". Another reports "encrypted reasoning or compaction content from multiple providers".

INFERRED: resuming account A's thread under ChatGPT account B (a different workspace/org) may fail on the first turn.

**Mitigations, all untested:**

1. Keep a thread on its account.
2. When switching, start a fresh thread seeded with a summary or the visible transcript. `thread/fork` copies history including the encrypted items, so it does not help. `thread/start` + `developerInstructions` or `thread/inject_items` with plain text does.
3. Strip `reasoning` and `compaction` items from the copied rollout. This is what users do manually in those issues.

This needs a real two-account test before Socrates promises "switch account mid-chat".

---

## 6. Other UI needs

- **Model list**, VERIFIED. `model/list {}` → `data:[{id:"gpt-6.1-sol", displayName, description, hidden, supportedReasoningEfforts:[{reasoningEffort:"low"|"medium"|"high"|"xhigh"|"max"|"ultra", description}], defaultReasoningEffort, inputModalities:["text","image"], upgrade, availabilityNux, …}]`. The listed ids here are gpt-6.1-sol, gpt-6-astra, gpt-6-sol, gpt-6-luna, gpt-5.6-sol/terra/luna and gpt-5.5, served from the cache even when logged out. `includeHidden:true` shows hidden ones.
- **Per-turn model/effort**, VERIFIED. `turn/start {…,"model":"gpt-5.5","effort":"high"}` → the request used `model:"gpt-5.5", reasoning:{effort:"high"}`, **and the next turn kept it** ("for this turn and subsequent turns"). Also per turn: `summary`, `approvalPolicy`, `sandboxPolicy`, `cwd`, `serviceTier`/`serviceTierForTurn`, `collaborationMode`, and `outputSchema` (JSON Schema for the final message). `thread/settings/updated` notifies the effective settings (VERIFIED on a collaborationMode change). DOCUMENTED: resuming with a different model emits a warning and adds a one-time model-switch instruction.
- **Collaboration modes**, VERIFIED. `collaborationMode/list` → `[{"name":"Plan","mode":"plan","reasoning_effort":"medium"},{"name":"Default","mode":"default"}]`. Plan mode injects a "# Plan Mode (Conversational)" developer prompt.
- **Sandbox/approval**, VERIFIED. `sandbox` on thread/start is a string; `sandboxPolicy` on turn/start is an object: `{"type":"dangerFullAccess"} | {"type":"readOnly","networkAccess"} | {"type":"workspaceWrite","writableRoots","networkAccess",…} | {"type":"externalSandbox",…}`. `approvalPolicy` is `untrusted|on-request|never|{granular}`. The Linux sandbox is seccomp-based (`"sandbox":"seccomp"` in the turn metadata).
- **Images**, VERIFIED. `{"type":"localImage","path":"/…/red.png"}` became `input_text <image name=[Image #1] path="…">`, `input_image data:image/png;base64,…`, `input_text </image>`. `{"type":"image","url":"data:image/png;base64,…"}` became a bare `input_image`. Also `fileId`, `localAudio`/`audio`, `skill` and `mention` inputs exist (schema).
- **Slash-command equivalents**, VERIFIED:
  - `/compact` → `thread/compact/start {threadId}` → `{}`, then `item/started` and `item/completed {"type":"contextCompaction"}`, then turn/completed. The compaction request is a normal model call with a "CONTEXT CHECKPOINT COMPACTION" prompt for custom providers; remote compaction v2 applies for OpenAI.
  - `/review` → `review/start {threadId, target:{type:"uncommittedChanges"} | {type:"baseBranch",branch} | {type:"commit",sha,title} | {type:"custom",instructions}}` → a turn with `enteredReviewMode`, the review prompt, the agent output and `exitedReviewMode` items.
  - `!cmd` → `thread/shellCommand {threadId, command, timeoutMs}` → `{}` and a `commandExecution` item. It runs **unsandboxed**.
  - `/fork` → `thread/fork {threadId[, lastTurnId|beforeTurnId]}` → a new thread with `forkedFromId`.
  - Rename → `thread/name/set`; archive → `thread/archive`.
  - Also documented but not run: `thread/revert` (undo turns), `turn/steer`, `thread/goal/set`, `skills/list`, `fuzzyFileSearch`, `command/exec` (sandboxed one-off), `fs/*`, `mcpServerStatus/list`, `config/read|value/write|batchWrite`.
- **Config overrides**:
  - process-wide: `-c key=value` / `--enable|--disable <feature>` on `codex app-server`;
  - per thread: `config:{…}` on thread/start, resume or fork;
  - persistent: `config/value/write`;
  - inspect: `config/read` returns the effective config;
  - feature flags: `experimentalFeature/list` (about 180 flags with lifecycle stage).

---

## 7. Implications for the Go adapter (INFERRED, from the above)

1. **Process topology**: one long-lived `codex app-server --listen unix://…` per CODEX_HOME/profile, outside socrates.service's cgroup. The Go server is a persistent WS-over-UDS client that reconnects, calls `thread/resume {excludeTurns:true}` for its active threads, and treats re-delivered server requests as idempotent by `(threadId, itemId)`. Do not use stdio (EOF kills turns) or the managed daemon (auto-updates).
2. **UI model = items**: render by `item.id`; stream `agentMessage`, `reasoning`, `plan` and `commandExecution` deltas; replace with the `item/completed` payload; show `turn/diff/updated` as the turn diff. Use `thread/status/changed.activeFlags` for the "waiting on you" badges, `thread/tokenUsage/updated` for the context meter, and `account/rateLimits/updated` for the limits meter.
3. **History for the UI**: `thread/turns/list` + `thread/items/list` (paginated, cursor) from the stored thread. Socrates does not need its own transcript store, but its own buffer covers deltas missed during reconnects.
4. **Questions**: implement handlers for `item/tool/requestUserInput` (structured options plus `isOther`/`isSecret`), agentMessage `delivery:"async"` + `questions`, `mcpServer/elicitation/request` (JSON-Schema form) and dynamic `item/tool/call`. For hours-long waits prefer non-blocking patterns on gpt-6 code-mode models (§3.1).
5. **Unattended mode** like the old adapter (`approvalPolicy:"never"`, `danger-full-access`) still works, but `never` auto-declines MCP elicitations. Use `granular` with `mcp_elicitations:true` if an MCP ask_user is used.
6. **Everything here is "experimental"** per the docs. Pin the codex version, regenerate the schema on upgrade (`generate-ts --experimental`), and diff it.

The old WP3 adapter (`git show 8674e80`, built against 0.152) used initialize/thread/start/resume(`excludeTurns`)/turn/start(model+effort per turn)/turn/interrupt and answered every server request with an error. Those calls still match 0.164. What's new since then: paginated history (`thread/turns/list`, `thread/items/list`), `serverRequest/resolved`, `waitingOn*` flags, code-mode tool sets, `request_user_input_async`, and the socket transport with resume-reattach.
