# 09 — Subagents and dynamic workflows in Claude Code 2.1.309, headless

Date 2026-10-23, `claude` 2.1.309, account `/root/socrates-spike/claude-a` (Team), haiku. Driven
with the production argv of the Claude adapter (AGENT-HOST.md §5.1): `-p --input-format stream-json
--output-format stream-json --verbose --include-partial-messages --permission-prompt-tool stdio
--setting-sources project,local --strict-mcp-config --dangerously-skip-permissions
--disallowedTools AskUserQuestion --replay-user-messages`, `CLAUDE_CODE_ENABLE_TODO_TOOLS=1`,
`IS_SANDBOX=1`.

Labels as in research/03: **VERIFIED** observed in a run of this work package, **INFERRED** reasoned.
The raw stream captures of R1–R3 were lost with the session's scratchpad; their transcripts stay
in `/root/socrates-spike/shared/projects/-tmp-claude-0--root-SocratesAgent-…-wp3-runs-r1-140101/`
(R1, session `115212bf…`) and `…-r2-140201/` (R2, session `04419ce9…`), and the frames below are
copied from the captures as they were read.

| run | what | turns, cost (list price) |
|---|---|---|
| R1 | TaskCreate, Edit, Write, Bash, a background `Agent` subagent, TaskUpdate, a background Bash | 2 turns (the second started by the CLI), $0.0068 |
| R2 | one `Workflow` call: two phases, three agents | 2 turns (the second started by the CLI), ≈ $0.01 |
| R3, R3b | plan mode (ExitPlanMode denied once, then approved), interrupt, `/model`, `/compact` | $0.13 |
| live tests | `go test -run TestLive` (tool turn, park/resume, unknown id, plan card, foreground subagent) | ≈ $0.05 |
| restart proof | §5: a workflow across a server kill, and a workflow whose host was killed | 5 turns, $0.007 main loop |

All runs together: $0.145 list price by the transcripts' `cost-state` (nine sessions), Team
subscription.

## 1. Task subagents (`Agent` tool) — VERIFIED (R1, live test)

The tool is named `Agent` in the stream (the init's tool list says `Task`; treat both alike). A
subagent the model does not ask to wait for is **backgrounded** (`is_backgrounded: true`): the
tool call returns at once and the agent works on.

```text
assistant  tool_use {id: toolu_01Wz…, name: "Agent", input: {description, subagent_type: "general-purpose", model: "haiku", prompt}}
system/background_tasks_changed {tasks: [{task_id: "a0fcbe943b5912f31", run_id, task_type: "local_agent", subagent_type, description}]}
system/task_started {task_id, run_id, tool_use_id: toolu_01Wz…, description, subagent_type, is_backgrounded: true, spawn_depth: 1, task_type: "local_agent", prompt}
user       tool_result for toolu_01Wz…: "Async agent launched successfully. … agentId: a0fcbe943b5912f31 …"
           tool_use_result {isAsync: true, status: "async_launched", agentId, description, resolvedModel, prompt, outputFile, canReadOutputFile, canContinueAgent}
assistant  {…tool_use Bash "echo sub-ok"} parent_tool_use_id: toolu_01Wz…, agent_id: a0fcbe…, subagent_type, task_description
system/task_progress {task_id, tool_use_id, description: "Running Print sub-ok via echo", usage: {total_tokens, tool_uses, duration_ms}, last_tool_name: "Bash"}
user       tool_result "sub-ok"  parent_tool_use_id: toolu_01Wz…, agent_id
assistant  text "sub-ok"         parent_tool_use_id: toolu_01Wz…
system/task_updated {task_id, patch: {status: "completed", end_time}}
system/task_notification {task_id, tool_use_id, status: "completed", output_file, summary: "sub-ok", usage}
system/background_tasks_changed {tasks: []}
user (isReplay: true, origin: {kind: "task-notification", producer: "session-task", runId}) "<task-notification>…<result>sub-ok</result>…</task-notification>"
```

- A subagent's own frames are **not streamed** as partial messages (every `stream_event` has
  `parent_tool_use_id: null`); they arrive as whole `assistant`/`user` frames with
  `parent_tool_use_id` = the `Agent` call and `agent_id`. Nested subagents chain the same way
  (`spawn_depth`).
- The task-notification user line is the CLI telling the model; it is not the person's message.
  When the agent finishes while the turn still runs, the turn simply goes on; when it finishes
  after the turn, the CLI **starts a turn of its own** (new `system/init`, no
  `command_lifecycle`, `result.origin.kind: "task-notification"`, no `user_message_uuids`).
- A **foreground** subagent (the model told to wait, live test) has `is_backgrounded: false`, and
  its `task_notification` arrives **before** the `Agent` call's `tool_result` (which carries the
  hand-back text "[Subagent hand-back] …"). The adapter keeps the group until both are in.
- The subagent's transcript is `projects/<cwd>/<session>/subagents/agent-<agentId>.jsonl` (+
  `.meta.json`), lines with `isSidechain: true`.
- `result.subagent_stats` summarises: `{spawned, started_in_background, completed, failed,
  killed: {parent, user, system}, by_type, max_depth}`.

## 2. Background shells — VERIFIED (R1)

`Bash {run_in_background: true}` → `background_tasks_changed` + `task_started{task_type:
"local_bash"}`; the call's `tool_use_result.backgroundTaskId` names the task. When it ends:
`task_updated{patch.status}`, `task_notification{summary: "Background command … completed (exit
code 0)"}`, and a turn of the CLI's own as in §1.

## 3. Dynamic workflows (`Workflow` tool) — VERIFIED (R2)

The model calls `Workflow {script}` (or `{scriptPath, resumeFromRunId}`); a workflow always runs
in the background:

```text
assistant  tool_use {id: toolu_01Kk…, name: "Workflow", input: {script: "export const meta = {…}…"}}
system/background_tasks_changed {tasks: [{task_id: "wcruo9hu2", task_type: "local_workflow", description}]}
system/task_started {task_id, tool_use_id, description, task_type: "local_workflow", workflow_name: "tiny-check", prompt: <the script>}
user       tool_result "Workflow launched in background. Task ID: … Transcript dir: … Script file: … Run ID: wf_b5d8f22f-f12 …"
           tool_use_result {status: "async_launched", taskId, taskType: "local_workflow", workflowName, runId, summary, transcriptDir, scriptPath}
system/task_progress {task_id, tool_use_id, description: "Gather: echo apple", usage, last_tool_name: "echo apple", summary,
                      workflow_progress: [
                        {type: "workflow_phase", index: 1, title: "Gather"}, {type: "workflow_phase", index: 2, title: "Combine"},
                        {type: "workflow_agent", index: 1, label: "echo apple", phaseIndex: 1, phaseTitle: "Gather", agentId, model,
                         state: "start"|"progress"|"done", startedAt, queuedAt, attempt, promptPreview, lastProgressAt,
                         lastToolName, lastToolSummary, tokens, toolCalls, durationMs, resultPreview}, …]}
… more task_progress (some carry only usage, no workflow_progress) …
system/task_updated {patch: {status: "completed"}}
system/task_notification {status: "completed", summary: "Dynamic workflow \"…\" completed", usage, output_file}
system/background_tasks_changed {tasks: []}
system/init …  (a turn of the CLI's own reports the result: "apple+pear")
```

- `workflow_progress` is the **whole** list every time: all phases, every agent seen so far
  (queued agents first appear without `agentId`, `model` unresolved). It is the stream's only view
  of a workflow; the agents' messages and calls are **not** in the stream.
- Each agent's transcript is `<transcriptDir>/agent-<agentId>.jsonl` (`isSidechain: true`, normal
  `assistant`/`user` lines with `tool_use` / `tool_result`), with `.meta.json`
  (`agentType: "workflow-subagent", workflowPhase, model`). The run journal
  `<transcriptDir>/journal.jsonl` has `{type: launched}`, `{type: started, agentId, label, phase,
  key}`, `{type: result, agentId, key, result}` — what `resumeFromRunId` replays from. The script
  is saved under `<session>/workflows/scripts/<name>-<runId>.js`, the run state in
  `<session>/workflows/<runId>.json`.
- Mapping (AGENT-HOST.md §5.1): the Workflow call is a group part (`content.workflow`: phases,
  counts, status); every agent is a part nested in it (`WorkflowAgent`, `content.agent`); the
  adapter reads the agent transcripts while the workflow runs and nests their calls in the agent's
  part.

## 4. Plan mode, compaction, local commands — VERIFIED (R3, R3b)

- With `--dangerously-skip-permissions`, `set_permission_mode{mode: "plan"}` works; in plan mode
  even the plan-file `Write` arrives as `can_use_tool` (`decision_reason_type: "mode"`), and the
  host must allow it. `ExitPlanMode` arrives as `can_use_tool{tool_name: "ExitPlanMode", input:
  {plan, planFilePath}, requires_user_interaction: true}`. Deny with a message → `tool_result
  {is_error: true, content: <message>}` and the model plans on; allow → `system/status
  {permissionMode: "bypassPermissions"}` (back to the launch mode), tool_result "User has approved
  your plan…", and the model carries the plan out in the same turn.
- A `can_use_tool` answer is the bare `{behavior, updatedInput | message}`; wrapping it in a
  second `{subtype, request_id, response}` is refused with "canUseTool callback returned an invalid
  permission result".
- `/model sonnet` as user text: a turn with `system/init`, a **synthetic** assistant frame
  (`model: "<synthetic>"`, `local_command_run: {command: "model", args}`, text "Set model to `Sonnet
  5.5` for this session only"), the command replay, `result{num_turns: 0}`. The next init carries
  the new model.
- `/compact`: `status compacting` → `status{compact_result: "success"}` → a second init →
  `compact_boundary{compact_metadata: {trigger, pre_tokens, post_tokens, preserved_segment…}}` →
  the summary user frame (`isSynthetic: true`) → **repeats of preserved frames with their original
  uuids** (dedupe by uuid) → `<local-command-stdout>Compacted </local-command-stdout>` → result.
- Interrupt mid-stream: the partial text block arrives as a whole `assistant` frame after the
  interrupt, then user `[Request interrupted by user]`, `command_lifecycle cancelled` for the
  interrupted message, `result{subtype: error_during_execution, terminal_reason:
  aborted_streaming}`.
- `set_max_thinking_tokens{thinking_display: "summarized"}` after initialize makes thinking blocks
  carry a summary (verified live: a subagent's reasoning part had text); without it they are empty.

## 5. A workflow across a server restart, and across a host kill

VERIFIED with the real binary end to end (script and result: scratchpad
`wp3/wfrestart.mjs`, `wp3/wfrestart.json`; transcript
`shared/projects/…-wp3-runs-wfrestart-1792769897524/41ea1fe2….jsonl`). A test server of this
branch (`e2e/harness.mjs` `spawnServer`, live build, own data folder and port, fake S3), a chat with
harness `claude`, model haiku, account `claude-a`; the message asks for a Workflow of four agents
in two phases, three of them running `sleep 12; echo aN` one after the other (≈ 43 s).

**Server restart (SIGKILL of `socrates serve`, down 15 s, started again).**

- The server was killed while the first workflow agent was running. The agent host (pid 35924)
  and its claude child stayed up the whole time; nothing in them noticed.
- The restarted server reconnected to the same host and the stream resumed with `since` = the
  last seq the phone had: the journal was **contiguous** (seq 1…62, no gap, no duplicate).
- The workflow finished during and after the outage: phases `Work`, `Join`; all four agent parts
  `done` (`a1`, `a2`, `a3`, `a1+a2+a3`), each with its Bash call nested in it; workflow status
  `completed` (39 819 tokens, 42.8 s). The CLI's own report-back turn followed and said
  `a1+a2+a3`.
- So a running workflow survives a server restart (or update) unchanged, as S4 proved for a
  plain turn. Under systemd the host is in its own scope; this run had no systemd (the host was
  in a session of its own), which is the same separation for `systemctl restart`.

**Host kill (SIGKILL of the agent host while a workflow agent ran).**

- The claude child died with the host (Pdeathsig): no claude process of the chat was left.
- The workflow is gone: its background run died with the CLI. Nothing finishes it.
- On the next message the server launched a new host, which resumed the conversation
  (`--resume`). Claude Code noticed by itself: it queued a task notification for the lost task,
  `<status>stopped</status><summary>Background workflow "restart-proof-b" (run wf_7e9cda73-a09)
  didn't finish before the previous session ended</summary>`, and ran it as a turn of its own
  before the person's message (a turn with no visible output). Asked "Did the last workflow
  finish?", the model answered that it was reported as stopped.
- What the chat showed before the fix: the workflow part and its agent part stayed `running` in
  the journal for ever (no host wrote their end). Since this run the new host closes every part a
  dead host left running (`part{interrupted}` + `notice{resumed}`, AGENT-HOST.md §6).
- The run can be continued by the model: the tool result names the script file and
  `Workflow({scriptPath, resumeFromRunId})`, which replays the agents that finished from the run
  journal. That is the model's (or the person's) decision; Socrates does not restart workflows.

**Machine reboot** (INFERRED, not run): the host, the CLI and the workflow die together, exactly
as in the host kill; after the reboot the chat's next message starts a new host, which resumes the
conversation with the same "stopped" notification and the same closed parts. Parking never hits a
workflow: a host with a background task is not idle (`Background()` counts it).

## 6. What this means for the adapter

- Background work is counted from `background_tasks_changed` (`Adapter.Background()`), so a host
  with a running subagent, workflow or shell never parks.
- A turn may end while its subagent or workflow group is still `running`; the group is closed by
  its `task_notification`, possibly after the turn, and the CLI's report-back turn follows.
- Everything a subagent or workflow produced carries `parent_part_id`.

## 7. An account switch: the prompt cache and the background work — VERIFIED (2026-10-27)

Real accounts, claude 2.1.311, the live tests in `internal/agent/claude/live_switch_test.go` and
`live_host_test.go` (`SOCRATES_CLAUDE_LIVE=1`, `SOCRATES_CLAUDE_LIVE_CONFIG`, `..._CONFIG2`,
`..._MODEL`, `..._PAUSE`). A conversation of ~61k tokens (three source files read), then a park and
a `--resume` on the second account's config directory, as a switch does:

| switch | model | after the switch: cache read / write |
|---|---|---|
| Team seat → Team seat, same organization | haiku | 61 755 / 222 |
| same, opus | opus | 60 930 / 198 |
| same, 6 minutes after the last request | opus | 60 927 / 197 |
| Team seat → an account of another organization (control) | haiku | 11 863 / 50 502 |

- The prompt cache is the organization's, not the seat's: a switch between accounts of one
  organization reads it whole, although every account has its own `CLAUDE_CONFIG_DIR` (the paths
  in the prompt do not break the prefix). Into another organization only the global system-prompt
  part is read.
- Claude Code asks for the 1-hour TTL on these subscriptions: six minutes later it still reads.
  The full rewrites after switches in production chats (400k-570k tokens written) came after
  pauses of 1.5-6 h, after which the cache is gone on the same account too. Nothing for Socrates
  to change.
- Background work: a background `Bash` (`sleep 240`) and a two-phase `Workflow` whose first phase
  had finished, each switched away from mid-run through a real host. The switch took 1.7-1.9 s
  (`Release`, AGENT-HOST.md §4.3). The agent on the second account, told what stopped, started the
  command again and resumed the workflow with `Workflow({scriptPath, resumeFromRunId})`; the
  finished phase was replayed from the run journal, not run again (its Bash wrote its line once).

