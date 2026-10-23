# 05 – Driving OpenCode 1.19.4 from a Go web server (`opencode serve`, HTTP + SSE)

Date: 2026-10-23. Binary: `/root/.local/bin/opencode` -> `/opt/socrates/opencode/1.19.4/opencode`.
Lab: `/tmp/claude-0/-root-SocratesAgent/d612e239-e189-4f08-9945-19100f29fc4c/scratchpad/opencode-lab/`
(spec: `opencode-lab/openapi.json`, raw SSE captures `sse_*.log`, compact traces `trace*.log`, server logs `serve*.log`).

Tags used throughout:

- **VERIFIED**: I observed it against the installed binary in an isolated lab server.
- **DOCUMENTED**: I read it in the OpenAPI spec served by the binary (`GET /doc`), or in the bundled JS source pulled out with `strings`.
- **INFERRED**: my conclusion from the above. It was not tested directly.

## 0. Lab setup and safety

- I ran isolated servers with `XDG_DATA_HOME/XDG_CONFIG_HOME/XDG_CACHE_HOME/XDG_STATE_HOME` set to temp dirs under `opencode-lab/xdg/`. These held a **copy** of `auth.json` and `models.json`, plus `OPENCODE_DISABLE_AUTOUPDATE=1` and `OPENCODE_DISABLE_MODELS_FETCH=1`. The servers listened on port 47391, with a second one on 47393. The project was a throwaway git repo at `opencode-lab/proj`.
- I did not touch the systemd service or tmux. The real `~/.local/share/opencode/opencode.db*` timestamps stayed unchanged (11:06/11:09, before the lab started). The real auth and config are unmodified.
- The OpenRouter key was switched in the temp copy only and then restored. I checked the restore byte for byte against the backup.
- **Configured providers** (`auth.json`, key names only): `openai` (type `oauth` = ChatGPT login), `openrouter` (`api`), `cloudflare-workers-ai` (`api` + metadata), `kimi-for-coding` (`api`). `GET /provider` reports connected = `openrouter, cloudflare-workers-ai, openai, opencode`. `opencode` = OpenCode Zen, whose free models need no key.
- **Model choice.** All experiments used **free** models: `opencode/deepseek-v4.1-flash-free` (tools, image input, reasoning) and `openrouter/nvidia/nemotron-3.5-lightning:free`. Total cost was 0.
- I deliberately **did not use `openai` (ChatGPT OAuth)**. A refresh in the temp copy could rotate the refresh token and invalidate the real login.
- Side observation: the real `~/.config/opencode/opencode.jsonc` contains a plaintext Exa API key in the `mcp.exa.environment` block. I have not reproduced it here. The real config also loads the plugin `@guard22/opencode-multi-auth-codex@latest`.

## 1. There are two APIs in 1.19.4 – use the classic one

The spec (`openapi.json`, 162 paths, 472 schemas) contains two parallel APIs:

| | "v1" / classic (`/session`, `/event`, `/permission`, `/question`, …) | "v2" (`/api/*`) |
|---|---|---|
| Runner | Mature loop. This is what the TUI and `opencode run` use. | New `SessionRunner`. Durable event log with per-session `seq`. |
| Providers | All of `auth.json` (4 connected here) | Only the v2 credential store (`credential` table, empty here). `GET /api/model` lists **46 models, all `opencode/*`**. |
| Works here? | **VERIFIED**: everything below | **VERIFIED broken for practical use**. `POST /api/session/{id}/wait` returns `503 "Session wait is not available yet"`. Zen "native" models fail with `SessionRunnerModel.UnsupportedApiError: Unsupported API for opencode/deepseek-v4.1-flash-free: native`. The 4 free `aisdk` models failed upstream ("Model is unavailable"). **DOCUMENTED** (source): the v2 runner supports only `@ai-sdk/openai`, `@ai-sdk/anthropic` and `@ai-sdk/openai-compatible`. |

**Recommendation.** Build on the v1 API. Treat v2 as something to watch: it has steer/queue delivery, durable replay via `GET /api/session/{id}/event?after=<seq>` and `/history`, and labelled multi-credentials per integration.

The deleted WP4 adapter (`git show ec59db7`) targeted v2 (`/api/session/*`, `/api/event`) on 1.17.13. Its notes about the missing idle event and the 503 `/wait` still apply to v2. On v1 there **is** an idle event (see below).

Every v1 call takes `?directory=<abs project dir>` (or `workspace`). One server process serves many directories, with one "instance" per directory. **VERIFIED**: all calls in this report used `?directory=`.

## 2. Sessions (question 1)

### 2.1 Endpoints – all VERIFIED unless marked

| Purpose | Call | Notes |
|---|---|---|
| Create | `POST /session` body `{title?, parentID?, agent?, model?{id,providerID,variant?}, permission?[{permission,pattern,action}], metadata?}` | Returns `Session` (`id` like `ses_…`, `slug`, `directory`, `title`, `cost`, `tokens`, `time`). **VERIFIED**: a per-session permission ruleset (`bash:*:ask`) is honoured. |
| List | `GET /session?directory=&roots=&search=&limit=&start=` | Sorted by updated time. Forks appear as their own sessions. v2-created sessions also show up here. |
| Get / update / delete | `GET`/`PATCH`/`DELETE /session/{id}` | `PATCH` accepts `title`, `metadata`, `permission`, `time.archived`. |
| Messages | `GET /session/{id}/message?limit=&before=` | `[{info: UserMessage\|AssistantMessage, parts: Part[]}]`. `GET /session/{id}/message/{mid}` returns one. |
| Prompt, sync | `POST /session/{id}/message` | Body `{parts[], model?{providerID,modelID}, agent?, variant?, system?, tools?{name:bool}, noReply?, format?(text\|json_schema), messageID?}`. Blocks until the turn ends and returns the last assistant message plus its parts. A trivial "pong" took 2.5 s. |
| Prompt, async | `POST /session/{id}/prompt_async` (same body) | Returns `204` immediately. Follow progress over SSE. **VERIFIED**: a second `prompt_async` while busy also returns 204 and is **queued**; it ran after the first turn finished. |
| Abort | `POST /session/{id}/abort` | Returns `true`. **VERIFIED** during a running `sleep 30`: `session.error {name:"MessageAbortedError"}`, then `session.status idle` and `session.idle`. The bash part completed with output `User aborted the command`, and `info.error` is set on the assistant message. |
| Fork | `POST /session/{id}/fork` body `{messageID?}` | **VERIFIED**: new session `"edit (fork #1)"` with the full history copied. Note that `parentID` is **not** set on forks; `parentID` is used for sub-agent child sessions (`GET /session/{id}/children`). |
| Revert | `POST /session/{id}/revert` body `{messageID, partID?}` | **VERIFIED**: the files on disk are restored (notes.txt was deleted). The session gets `revert{messageID, snapshot, diff}`. The messages are still returned by `/message` (7 of them), so the UI must hide everything from `revert.messageID` on. **INFERRED** from the TUI behaviour: they are dropped when the next prompt is sent. |
| Unrevert | `POST /session/{id}/unrevert` | **VERIFIED**: the file came back and `revert` is null again. |
| Share | `POST`/`DELETE /session/{id}/share` | **VERIFIED** only with `"share":"disabled"`: it returns `{"_tag":"InternalServerError"}`. I did not publish anything to opencode.ai. When enabled, `Session.share.url` holds the link (**DOCUMENTED**). |
| Diff for one turn | `GET /session/{id}/diff?messageID=<user msg>` | **VERIFIED**: `[{file, patch (unified), additions, deletions, status}]`. The same data sits in the user message's `info.summary.diffs`. |
| Todos | `GET /session/{id}/todo` | `[{content,status,priority}]` |
| Shell (the `!cmd` of the TUI) | `POST /session/{id}/shell` body `{agent, command, model?}` | **VERIFIED**: produces an assistant message with a `bash` tool part. |
| Slash command | `POST /session/{id}/command` body `{command, arguments, agent?, model?:"provider/model", variant?, parts?}` | See section 7. |
| Pending prompts | `GET /permission`, `GET /question` | These cover every session of the directory and are useful after a reconnect. |
| Status map | `GET /session/status` | `{ses_…:{type:"busy"\|"retry"\|"idle"}}`. Idle sessions are absent; `{}` means everything is idle. **VERIFIED**: a session waiting on a permission or question still reports `busy`, so combine this with `/permission` and `/question`. |

### 2.2 Event streams

- **`GET /event?directory=…`** carries one directory's instance. Frames are `data: {"id":"evt_…","type":…,"properties":{…}}`. There are no `id:` SSE lines, and **no replay**: I sent `Last-Event-ID` and it was ignored.
  - The first frame is `server.connected`.
  - `server.heartbeat` arrives every **10 s** (**DOCUMENTED** in the source).
  - The stream **ends** after `server.instance.disposed`, for example after `POST /instance/dispose`, so the client must reconnect.
- **`GET /global/event`** carries every directory. Frames are `{"directory","project","payload":{…same event…}}`.
  - It additionally carries `payload.type:"sync"` frames: `{"syncEvent":{id,type:"message.part.updated.1",seq,aggregateID:<sessionID>,data}}`. Those are the durable, sequence-numbered versions of `session.*`, `message.*` and `message.part.*`.
  - It is the better choice for one Go process that multiplexes all projects.
- **`GET /api/event`** (v2) carries durable events plus `session.next.*`. For v1 runs it carried no deltas in my capture, so don't use it for v1.

Replay after a disconnect (important for the phone and car use case):

- **VERIFIED.** `POST /sync/history` with body `{"<sessionID>": lastSeq, …}` returns every durable sync event with `seq > lastSeq`, ordered `[{id, aggregate_id, seq, type, data}]`. Aggregates you don't list come back in full: 435 events for the lab.
- These events cover session, message and part updates. They do not cover deltas, status, permission or question events.
- Recovery recipe:
  1. Keep `lastSeq` per session from the `sync` frames on `/global/event`.
  2. On reconnect, call `/sync/history`.
  3. Then call `GET /session/status`, `GET /permission` and `GET /question`.

  Or simply re-GET `/session/{id}/message`.
- **INFERRED**: `/sync/*` is meant for workspace sync, so it is semi-internal and could change between versions. Re-fetching messages is the robust fallback.

### 2.3 Event types seen and their meaning (VERIFIED in `sse_event.log` and `trace*.log`)

**Turn lifecycle**

- `session.status {status:{type:"busy"}}` arrives several times per turn. `retry` carries `{attempt, message, next, action?}` (**DOCUMENTED**).
- The end of a turn is `session.status {type:"idle"}` immediately followed by **`session.idle {sessionID}`**. That is the reliable "turn finished" signal.
- `session.error {sessionID, error:{name, data}}` carries one of these error names: `ProviderAuthError`, `UnknownError`, `MessageOutputLengthError`, `MessageAbortedError`, `StructuredOutputError`, `ContextOverflowError`, `ContentFilterError` or `APIError`.
  - **VERIFIED**: a bad key gave `APIError{message:"User not found.", statusCode:401, isRetryable:false, responseHeaders…}` on the assistant `info.error`.

**Messages**

- `message.updated {info}` is the whole message object, sent again and again with growing `tokens`/`cost` and with `time.completed` and `finish` set at the end.
- `message.removed` also exists.

**Parts**

`message.part.updated {part}` carries the whole part every time. `message.part.delta {messageID, partID, field:"text", delta}` carries incremental text. Deltas are used for **both** `text` and `reasoning` parts; tell them apart through the part type received in the preceding `part.updated`.

Part types:

- `text`: has `time.start` and `time.end`. The user's own text is also a part, and file-read context can appear as `synthetic: true` text.
- `reasoning`
- `tool`: `{tool, callID, state}`.
  - State moves through `pending {input:{}, raw:""}` → `running {input, time.start, metadata?}` → `completed {input, output, title, metadata, time, attachments?}` | `error {input, error, time}`.
  - `bash` emits repeated `running` updates with `metadata.output` while it streams.
  - `edit` puts a unified diff in `state.metadata.diff`.
  - `todowrite` and `read` behave as you'd expect.
- `step-start {snapshot}`
- `step-finish {reason:"tool-calls"|"stop", snapshot, cost, tokens{total,input,output,reasoning,cache{read,write}}}`
- `patch {hash, files[]}`: the files changed in this step.
- `file` (attachments)
- `compaction {auto}`
- `subtask` and `agent` (**DOCUMENTED**)
- `retry {attempt, error}` (**DOCUMENTED**)

**Files and diffs**

- `file.edited {file}` arrives once per write or edit.
- `session.diff {diff:[]}` is emitted a lot, but every one I saw was empty, even after edits. Use `GET /session/{id}/diff?messageID=` or the user message's `summary.diffs` instead. **INFERRED**: `session.diff` is only filled in some situations.

**Todos**: `todo.updated {sessionID, todos:[{content,status,priority}]}`.

**Permissions and questions**: `permission.asked`, `permission.replied`, `question.asked`, `question.replied` and `question.rejected` (section 3).

**Noise** to ignore: `plugin.added` (45 at instance boot), `catalog.updated`, `reference.updated` and `integration.updated`.

The full list has 89 event types in the `Event` schema. Others worth handling are `session.compacted`, `session.created`, `session.updated`, `session.deleted`, `mcp.tools.changed`, `command.executed`, `vcs.branch.updated`, `pty.*`, `lsp.updated`, `installation.update-available` and `server.instance.disposed`.

## 3. Permissions (question 2) – VERIFIED end to end

**Event.** `permission.asked`:

```json
{"id":"per_…","sessionID":"ses_…","permission":"bash","patterns":["echo hi-from-bash"],
 "metadata":{"command":"echo hi-from-bash"},"always":["echo *"],
 "tool":{"messageID":"msg_…","callID":"call_…"}}
```

- The matching tool part is already in `running` state, with its input visible.
- The `always` field is the pattern that "always" would whitelist.

**Listing.** `GET /permission?directory=` returns the same objects. It covers all sessions and is what you need after a reconnect.

**Reply.** `POST /permission/{requestID}/reply?directory=` with body `{"reply":"once"|"always"|"reject", "message"?: "feedback"}` returns `true` and emits `permission.replied {sessionID, requestID, reply}`.

What each reply did:

- `once`: the tool ran (`hi-from-bash`) and the loop continued.
- `reject` with a `message`: the tool part goes to `error`, with the text "The user rejected permission to use this specific tool call with the following feedback: Use printf instead of echo". **The loop continues** and the model sees the feedback.
- `reject` without a message stops the turn. **DOCUMENTED**: the flag `experimental.continue_loop_on_deny` exists. **INFERRED** from the question-reject behaviour below.
- `always`: `echo third` was asked and approved with `always`, and then `echo fourth` ran **without asking**.
  - The always-grant is **not** stored in SQLite: the `permission` table stayed at 0 rows and `GET /api/permission/saved` returned `[]`.
  - **INFERRED**: the grant lives in memory per instance and is lost on restart or dispose. Persist your own allow-list and pass it as session `permission` rules.

**Legacy route.** `POST /session/{id}/permissions/{permissionID}` with body `{response}` also exists.

**Default asks** to expect, even with `"*":"allow"`: `external_directory` (ask, with allow-lists for the tool-output dir and skill dirs), `doom_loop` (ask), and `read` of `*.env` / `*.env.*` (ask). These are visible in `GET /agent`.

The current tmux harness sets `OPENCODE_PERMISSION` to allow everything. With a web UI we can switch to `ask` for selected tools and render cards.

## 4. Questions (question 3)

### 4.1 Native `question` tool – VERIFIED

- **Enablement.** The tool is present when `OPENCODE_CLIENT` is in `app`, `cli` or `desktop` (**the default is `cli`**), or when `OPENCODE_ENABLE_QUESTION_TOOL` is set. Both are **DOCUMENTED** in the source:

  ```
  ["app","cli","desktop"].includes(s.client)||s.enableQuestionTool
  ```

  It shows up in `GET /experimental/tool/ids` and is allowed for the `build` and `plan` agents. Sub-agents deny it.
- **Schema the model fills** (`GET /experimental/tool?provider=&model=`):

  ```json
  {"questions":[{"question":"Complete question","header":"≤30 chars",
    "options":[{"label":"1-5 words","description":"…"}],"multiple":false}]}
  ```

  `custom` defaults to true, which means "type your own answer" is allowed. The answers are arrays of labels.
- **Event.** `question.asked`, also available from `GET /question`:

  ```json
  {"id":"que_…","sessionID":"ses_…","questions":[{"question","header","options":[{"label","description"}],"multiple"}],"tool":{"messageID","callID"}}
  ```

  **VERIFIED** with two questions, one single-choice and one multi-choice.
- **Reply.** `POST /question/{id}/reply` with body `{"answers":[["Green"],["Apple","My own: mango"]]}`. There is one array per question, and free text is accepted as a label.
  - The tool output became: `User has answered your questions: "What is your favourite colour?"="Green", "Which fruits do you like?"="Apple, My own: mango". …`
  - `state.metadata.answers` keeps the raw arrays.
  - Event: `question.replied {requestID, answers}`.
- **Reject.** `POST /question/{id}/reject`. **VERIFIED**: the tool part ends with `error: "The user dismissed this question"` and **the turn stops**. The model wrote no further text.
- **Waiting.** The question blocks indefinitely, and the session stays `busy`.
- **Schema limits.** The native schema has only options plus free text. There is no number, date, form or validation.

### 4.2 Disabling it – VERIFIED

- With `"permission":{"question":"deny"}` in the config, the model's tool list no longer contained `question`. The model said so itself and listed its tools.
- A session-level `permission:[{question:*:allow}]` did **not** re-enable it.
- Alternatives:
  - **DOCUMENTED**: set `OPENCODE_CLIENT` to some other value. **INFERRED**: this also changes the client tag in the user agent and telemetry.
  - **VERIFIED**: pass `tools:{"question":false}` per prompt. I tested the same mechanism with `ask_user:false`.

### 4.3 Replacing it with our own tool

**Option A: MCP tool.** VERIFIED that it can block for long periods.

- The config fields are `mcp.<name> = {type:"local", command[], environment?, cwd?, enabled?, timeout?}` or `{type:"remote", url, headers?, oauth?, enabled?, timeout?}`, plus the global `experimental.mcp_timeout`.
- MCP tools reach the model as `<server>_<tool>`, for example `slowlong_wait`.
- **DOCUMENTED** from the source: every MCP tool call is

  ```
  client.callTool({name,arguments}, schema, {resetTimeoutOnProgress:true, signal, timeout: server.timeout ?? experimental.mcp_timeout ?? SDK default})
  ```

  There is no `maxTotalTimeout`.
- **VERIFIED** with three concurrent 75-second calls to my stdio MCP server (`scratchpad/tools/slow_mcp.py`):

  | Server config | Result |
  |---|---|
  | no `timeout` | **failed at 60.0 s** with `MCP error -32001: Request timed out` (the MCP SDK default is 60 s) |
  | `"timeout": 600000` | completed after 75 s |
  | `"timeout": 30000`, server sends `notifications/progress` every 10 s | completed after 75 s, because progress resets the timer |

- Consequence:
  - An MCP `ask_user` tool can block for minutes or hours, either with a huge `timeout` (an int in ms; 86 400 000 is presumably fine) or, more robustly, by **emitting a progress notification at least every `timeout` ms**.
  - Abort (`/session/abort`) is propagated as `notifications/cancelled` (**VERIFIED**: I saw `AbortError` cancellations).
  - The schema can be arbitrarily rich, because it is our JSON Schema.
- **Remote MCP.** It uses the same `callTool` path (**DOCUMENTED**). Our Go server can therefore expose a streamable-HTTP MCP endpoint (`type:"remote", url:"http://127.0.0.1:<port>/mcp", headers:{Authorization:…}`) whose `ask_user` handler parks until the web UI answers.
  - **INFERRED**: the long-poll must survive OpenCode instance disposes and restarts. On a restart the tool call is lost, and the model sees an error the next time it is resumed.

**Option B: custom tool.** VERIFIED.

- I wrote `xdg/config/opencode/tool/ask_user.ts`, using `tool()` from `@opencode-ai/plugin` and a Zod schema with `kind: single|multi|text|number|date`.
- It `fetch`es a long-poll URL (`scratchpad/tools/answer_server.py`). It **blocked 90 s with no timeout** and returned the answer, and the model used it.
- The question payload is visible to the UI as the tool part's `state.input` once it is `running`.
- `ctx.metadata({title, metadata})` did **not** produce a visible SSE update in my run. **INFERRED**: don't rely on it; use `state.input`.
- `ctx.abort` is the abort signal.
- Requirements: `@opencode-ai/plugin` must be resolvable from the config dir (I copied `node_modules`). Custom tools live in `~/.config/opencode/tool/*.ts` or `.opencode/tool/`, and plugins can also contribute `tool` entries (**DOCUMENTED**).
- **Recommendation**: prefer a remote MCP tool served by our Go server. It needs no TS or npm in OpenCode's config dir, the same tool works for Claude and Codex harnesses, and the timeout rules are understood. Use a custom tool only if we want zero network hops.

**Option C: piggyback on permissions** (INFERRED, not recommended). A tool calling `ctx.ask({permission:"ask_user", metadata:{…}})` raises a `permission.asked` with arbitrary metadata, but the only way to carry an answer back is the reject `message`.

## 5. Durability and the terminal escape hatch (question 4)

**Storage.** VERIFIED. `$XDG_DATA_HOME/opencode/` (real: `/root/.local/share/opencode/`) contains:

- `opencode.db`: SQLite in WAL mode. Tables include `session`, `message` (JSON `data`), `part` (JSON `data`), `todo`, `event` and `event_sequence` (the durable sync log, `message.part.updated.1` etc.), `session_input`, `permission`, `project`, `credential`, `account` and `workspace`.
- `snapshot/<projectID>/`: a git object store used for revert and diffs.
- `storage/session_diff/<ses>.json`
- `tool-output/`
- `auth.json` (mode 0600)
- `log/`

**DOCUMENTED**: `OPENCODE_DB` overrides the database path. `opencode export|import|session|db` exist on the CLI.

**Restart.** VERIFIED twice: after killing and restarting `opencode serve`, all 10 sessions, their messages, the fork and the revert state were back. `GET /session/{id}/message` was unchanged. What does **not** survive is in-memory state: pending permissions and questions, "always" grants, and the running turn.

**Two servers on one data dir.** VERIFIED:

- Server B (port 47393) created a session; server A read it, including its messages, from the shared SQLite.
- Server A received **no events** for it, and B's pending question did **not** show up in A's `GET /question`.
- Conclusion: run **one** `opencode serve` per data dir and treat other processes as read-only at best.

**TUI attach.** VERIFIED:

- I ran `opencode attach http://127.0.0.1:47391 --session <id> --dir <proj>` in a Python pty (`scratchpad/tools/ptyrun.py`, not tmux), then sent a `prompt_async` from the API while it was attached.
- The TUI rendered both the web-sent prompt and the reply (`ZEBRA-FROM-WEB`), together with the session's history.
- So "Open in terminal" works as: our server spawns `opencode attach <url> -s <sessionID> --dir <dir> -p <password>` in a tmux or PTY pane, alongside the web UI, against the same server.
- **INFERRED**: permission and question cards answered in the web UI disappear in the TUI via `permission.replied` and `question.replied`, and the other way round.
- **DOCUMENTED**: `/tui/*` endpoints (`select-session`, `submit-prompt`, `show-toast`, `control/next`…) let the server steer an attached TUI.

**Built-in PTY API.** DOCUMENTED: `POST /pty`, `GET /pty/{id}/connect` (WebSocket, plus `connect-token`), `PUT`/`DELETE` and `pty.*` events. This would let a web xterm get a server-owned shell without tmux.

**Security.** DOCUMENTED and used by the current harness: `OPENCODE_SERVER_PASSWORD` and `OPENCODE_SERVER_USERNAME` (default `opencode`) put Basic auth on every route. Without them the server logs "server is unsecured".

**Unknown routes.** VERIFIED: an unknown or malformed route (for example `/question//reject`) returns the embedded web UI HTML with 200, not a 404. Validate IDs before calling. `OPENCODE_DISABLE_EMBEDDED_WEB_UI` exists.

## 6. Accounts and auth (question 5)

**auth.json.** VERIFIED: it maps `providerID` to one of:

- `{type:"oauth", refresh, access, expires, accountId?, enterpriseUrl?}`
- `{type:"api", key, metadata?}`
- `{type:"wellknown", key, token}`

There is **one credential per provider ID**.

**DOCUMENTED.** `OPENCODE_AUTH_CONTENT` (a JSON string) replaces reading `auth.json` for the whole process. Writes such as OAuth refreshes still go to the file.

**API.** `PUT /auth/{providerID}` with an `Auth` body, and `DELETE /auth/{providerID}`. VERIFIED:

- The file changes immediately, and `GET /provider` `connected` updates immediately.
- **But a provider that has already been used keeps its cached client.**
  - I PUT a bogus OpenRouter key and the next prompt **still succeeded**.
  - After `POST /instance/dispose?directory=…`, the next prompt in the **same session** failed with 401.
  - After restoring the key and disposing again, it succeeded.
- So a mid-session account switch = `PUT /auth/{id}` + `POST /instance/dispose`.
  - Disposing ends the `/event` stream (`server.instance.disposed`) and restarts the instance's MCP servers.
  - **INFERRED**: disposing would kill an in-flight turn, so only dispose when the directory is idle.

**Two accounts of one provider at the same time.** VERIFIED for API keys:

- I declared a second provider ID in the config:

  ```json
  "orb": {"npm":"@ai-sdk/openai-compatible", "options":{"baseURL":"https://openrouter.ai/api/v1"}, "models":{…}}
  ```

- I then ran `PUT /auth/orb`. One session ran prompts alternately on `openrouter` and `orb`, and both succeeded.
- Config changes needed a **process restart**. Neither `PATCH /config` (which wrote `proj/config.json` without it taking effect), `/instance/dispose` nor `/global/dispose` reloaded the global config file.
- For **ChatGPT/Codex OAuth** (provider `openai`) and Anthropic OAuth, the login flow and token refresh come from an auth plugin keyed to the provider ID.
  - **INFERRED**: a second ID such as `openai-b` would not get the OAuth loader.
  - The options are:
    - the user's existing `opencode-multi-auth-codex` plugin, which rotates or pins several ChatGPT accounts;
    - swapping the `openai` entry via `PUT /auth/openai` plus dispose;
    - one serve process per account. That needs a separate data dir; see the two-servers caveat above.

**OAuth login via the API.** DOCUMENTED:

- `GET /provider/auth` lists the methods. For `openai` these are "ChatGPT Pro/Plus (browser)", "ChatGPT Pro/Plus (headless)" and "Manually enter API Key". `github-copilot` has select and text prompts.
- `POST /provider/{id}/oauth/authorize` with body `{method:index, inputs?}` returns `{url, method:"auto"|"code", instructions}`.
- `POST /provider/{id}/oauth/callback` with body `{method, code?}` completes it. A web UI can therefore drive the logins.

**v2 multi-credential store.** DOCUMENTED, not usable yet:

- `POST /api/integration/{id}/connect/key` with body `{key, label?}`, `…/connect/oauth`, `PATCH`/`DELETE /api/credential/{id}`, and the `credential` table with `label` and `active` columns.
- This is the future answer to "several labelled accounts per provider", but only the v2 runner reads it.

**Model and variant per prompt.** VERIFIED:

- The prompt body takes `model:{providerID, modelID}` and `variant` (for example `"low"`; the variants are listed per model in `GET /provider`, e.g. `low|high|max`).
- `agent` can also be set per prompt.
- The session remembers the last model and agent (`Session.model`, `Session.agent`).

## 7. Other UI needs (question 6)

**Agents.** VERIFIED:

- `GET /agent` lists `build` and `plan` (primary), `general` and `explore` (sub-agents), and the hidden `compaction`, `title` and `summary`. Each comes with its full permission ruleset.
- `agent:"plan"` per prompt: the model refused to edit. `plan` denies `edit` except `.opencode/plans/*.md`.
- Custom agents come from the `agent` config. The `task` tool spawns child sessions (with `parentID`), so render those as nested.

**Commands.** VERIFIED:

- `GET /command` returns `[{name, description, template, hints, source:"command"|"skill", subtask?}]`. Built-ins are `init` and `review`.
- **Note**: OpenCode also imports `~/.claude/skills/**` as commands (docx, pdf, morning…) and logged duplicate-skill warnings. `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS` / `OPENCODE_DISABLE_CLAUDE_CODE` / `OPENCODE_DISABLE_EXTERNAL_SKILLS` exist (**DOCUMENTED**).
- I defined `"command":{"pong":{"template":"Reply with exactly: PONG $ARGUMENTS"}}`. Then `POST /session/{id}/command {command:"pong", arguments:"42", model:"opencode/deepseek-v4.1-flash-free"}` returned "PONG 42", and the user message holds the expanded template.

**Models.** VERIFIED:

- `GET /provider` returns `{all:[{id, name, models:{id:{capabilities{reasoning, attachment, toolcall, input{text,image,pdf,audio,video}}, cost{input,output,cache}, limit{context,output}, variants, status}}}], default:{provider:model}, connected:[…]}`.
- `GET /config/providers` is the configured-only view.

**Attachments.** VERIFIED:

- `parts:[{type:"file", mime:"image/png", filename, url:"data:image/png;base64,…"}]`: the model answered "red" for an 8×8 red PNG.
- `{type:"file", mime:"text/plain", url:"file:///abs/path"}` is expanded server-side into a synthetic Read tool call plus text parts.
- Check `capabilities.input.image` before offering image upload.

**Usage and cost.** VERIFIED:

- Every assistant `info` carries `cost` and `tokens{total,input,output,reasoning,cache{read,write}}`.
- Every `step-finish` part carries per-step values.
- `Session.cost` and `Session.tokens` hold the totals, kept up to date via `session.updated`.
- The free models report cost 0.

**Compaction.** VERIFIED:

- `POST /session/{id}/summarize` with body `{providerID, modelID, auto?}` returned `true` after 2.9 s.
- It emits a user message with a `compaction` part and an assistant message with `mode:"compaction"`, `summary:true` and a markdown summary text, followed by `session.compacted`.
- Auto-compaction settings: `compaction:{auto, prune, tail_turns, preserve_recent_tokens, reserved}`, plus `OPENCODE_DISABLE_AUTOCOMPACT`.

**Structured output.** DOCUMENTED: `format:{type:"json_schema",…}` per prompt, with the result in `info.structured`.

**Titles.** OpenCode auto-titles sessions through the hidden `title` agent. `PATCH /session/{id} {title}` overrides it.

**Other.** DOCUMENTED: `/file`, `/file/content`, `/find`, `/find/file`, `/vcs/diff`, `/vcs/status`, `/lsp`, `/mcp` (status, connect, disconnect, add) and `/experimental/worktree`.

## 8. Suggested Go architecture (INFERRED from the above)

1. One `opencode serve --port <p> --hostname 127.0.0.1`, run as a child of the Go service, with:
   - `OPENCODE_SERVER_PASSWORD` set;
   - `OPENCODE_CONFIG_CONTENT` (`share:"disabled"`, `autoupdate:false`, `permission:{question:"deny", …ask rules…}`, `mcp.socrates:{type:"remote", url:<our MCP>, timeout:…}`);
   - `OPENCODE_DISABLE_AUTOUPDATE` and `OPENCODE_DISABLE_MODELS_FETCH` (offline start).

   Use the user's normal data dir so that old sessions and the TUI keep working.
2. Keep one `/global/event` subscription and fan events out to browsers by `directory` and `sessionID`.
   - Coalesce `message.part.delta` per part.
   - Use `session.idle` as the end of a turn.
   - Use `permission.asked` and `question.asked` (or our MCP `ask_user`) as the "needs input" state.
   - On reconnect, recover with `/sync/history` (cursor = seq per session) or `GET /message`, plus `/session/status`, `/permission` and `/question`.
3. Map the controls to endpoints:
   - send: `prompt_async` (queues while busy)
   - stop: `abort`
   - undo: `revert`/`unrevert`
   - branch: `fork`
   - slash commands: `command`
   - `!cmd`: `shell`
   - compact: `summarize`
   - model, variant and agent: per prompt
4. "Open in terminal" = `opencode attach <url> -s <id> --dir <dir>` in a pane, against the same server.
5. Account switching:
   - API-key accounts: separate provider IDs in the config, or `PUT /auth` plus dispose when idle.
   - ChatGPT OAuth: the multi-auth plugin, or a swap plus dispose.
   - Revisit the v2 credential store once the v2 runner works.

## 9. Artefacts

- `opencode-lab/openapi.json`: the full spec from `GET /doc`.
- `opencode-lab/sse_event.log`, `sse_global.log`, `sse_api.log`: raw SSE for one simple turn on the three streams.
- `opencode-lab/trace_perm.log`, `trace_perm2.log`, `trace3.log`, `trace4.log`: compact event traces for the permission, question, edit/todo, MCP-timeout, ask_user, compaction and queue experiments.
- `opencode-lab/mcp.log`: the MCP server's view of the timeouts and cancellations.
- `opencode-lab/answer-server.log`: the custom tool's 90-second wait.
- `opencode-lab/attach.raw`: raw TUI output from the attach test.
- `opencode-lab/xdg/config/opencode/opencode.json` and `tool/ask_user.ts`: the lab config and custom tool.
- `scratchpad/tools/`: `sch.py` (OpenAPI schema printer), `trace.py` (SSE tracer), `slow_mcp.py` (blocking MCP server), `answer_server.py`, `ptyrun.py`, `ctx.py` (source grep over `opencode-lab/src/strings.txt`).
