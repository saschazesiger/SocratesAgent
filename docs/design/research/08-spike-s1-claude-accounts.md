# Spike S1: one Claude Code conversation across two real subscription accounts

Date 2026-10-23, Claude Code 2.1.309, headless (`claude -p --input-format stream-json
--output-format stream-json --verbose --permission-prompt-tool stdio --setting-sources
project,local --strict-mcp-config --dangerously-skip-permissions`, `IS_SANDBOX=1`, env stripped of
every `CLAUDE*`/`ANTHROPIC*` variable, control `initialize` first, `--session-id` / `--resume <id>`).

Labels: **VERIFIED** = observed in this spike (logs and transcripts below). **INFERRED** = read
from the binary, from numbers, or reasoned, not exercised end to end.

## Setup

| | config dir | plan | org |
|---|---|---|---|
| A | `/root/socrates-spike/claude-a` | Team (`subscriptionType: team`, seat `team_tier_1`) | "Zesiger.net" (`c9a8…`) |
| B | `/root/socrates-spike/claude-b` | Max 20x (`subscriptionType: max`) | another personal org (`7e54…`) |

Shared layout (left in place):

```
/root/socrates-spike/shared/{projects,plans,tasks,todos}
claude-a/projects -> ../shared/projects     claude-b/projects -> ../shared/projects
claude-a/plans    -> ../shared/plans        claude-b/plans    -> ../shared/plans
claude-a/tasks    -> ../shared/tasks        claude-b/tasks    -> ../shared/tasks
claude-a/todos    -> ../shared/todos        claude-b/todos    -> ../shared/todos
```

Lab scripts and raw stream logs:
`/tmp/claude-0/-root-SocratesAgent/d612e239-e189-4f08-9945-19100f29fc4c/scratchpad/s1-lab/`
(`s1lab.py` driver on top of `cc-lab/cclab.py`; `exp1.py` … `exp7b.py`; `logs/e*.jsonl`;
`strace-b.txt`). Transcripts are under `shared/projects/<escaped cwd>/<sid>.jsonl`.

Cost of the whole spike: about $0.22 list price (sum of the transcripts' `cost-state`; mostly haiku, seven sonnet turns).

## Answer in one line

**Yes.** A conversation moves A → B → A (between turns, mid-turn after an interrupt, after a
SIGKILL, after `/compact`, with signed thinking) with full context, one shared transcript file and
no 400s. Claude Code 2.1.309 handles cross-org thinking itself. The only change to the planned
layout: **`tasks/` must be shared too**.

## 1. Switch between turns (VERIFIED, `exp1.py`, sid `8dbb31fa…`)

1. A, new session: "secret 58213", Bash `echo first-from-A > notes.txt` → `ok`.
   `initialize.account = {"email":"jan***","organization":"Zesiger.net","subscriptionType":"Claude Team","apiProvider":"firstParty"}`.
2. Process ended (stdin EOF). `--resume` under B:
   `initialize.account = {"email":"inf***","organization":"…'s Organization","subscriptionType":"Claude Max",…}`.
   "What is the secret?" → **58213**, Bash appended `second-from-B`, `cat` showed both lines.
   Second turn under B changed the secret to 58214.
3. `--resume` under A again: "current secret" → **58214**, and it listed all four of my
   messages, including both B turns.

Evidence that B's account was really used: `initialize.account` per process, `rate_limit_event`
per process is B's (seven-day window, see §5), `get_usage` returns `subscription_type:"max"`,
the transcript gets a `credential_org` attachment with B's org uuid at every process start, and
B's org applies its own tool policy (B's start writes a `deferred_tools_delta` that removes
`WebFetch` and `PublishPlugin`; going back to A re-adds them).

Transcript: exactly **one** file (`shared/projects/<esc>/8dbb31fa….jsonl`), the same path seen
through `claude-a/projects/…` and `claude-b/projects/…`; it grew 33 → 66 → 83 lines; the
`parentUuid` chain has **no forks** (no parent with two children). `claude auth status --json`
reports `projectsDirectory: <config dir>/projects` (the symlink), `orgId`, `orgName`, `email`,
`subscriptionType`.

## 2. Switch mid-turn (VERIFIED, `exp2.py`)

Task: three sequential Bash steps (step 1 is `sleep 6; …`), then "done tangerine".

- **Interrupt**: control `interrupt` while step 1 ran under A → `result{subtype:"error_during_execution",
  terminal_reason:"aborted_tools"}`; transcript ends with `tool_result` "The user doesn't want to
  proceed…" + `[Request interrupted by user for tool use]`. Resume under B, user text `continue`
  → B ran steps 1–3 and answered **"done tangerine"** (the secret came from A's turn).
- **SIGKILL** of A's process during step 1 (host-crash case): the transcript ends with a dangling
  `tool_use`. On `--resume` under B, Claude Code writes a synthetic `tool_result` ("Tool call
  interrupted: the session ended before this call's result was recorded, so its outcome is
  unknown…") and a synthetic assistant "No response requested.", then the new user turn.
  `continue` → B re-ran step 1 (it said so: "outcome unknown, rerunning is safe"), then 2, 3,
  "done tangerine". Caveat for the product: a killed tool call may be re-run by the model; not
  idempotent commands are its judgement call.

## 3. Signed thinking across orgs (VERIFIED, `exp1.py`, `exp3.py`, `exp4.py`)

Thinking is on by default: every request has `thinking:{type:"adaptive",display:"updates"}`,
`context_management.edits:[{type:"clear_thinking_20251015",keep:"all"}]` (preserved thinking) and
beta `thinking-binding-controls-2026-08-01`. Even haiku 5.5 writes thinking blocks; they are stored
with `"thinking":""` and a `signature` of 1.0–1.5 kB. `exp3.py` used `--model sonnet --effort high`
with the interrupt fired right after an assistant message that had a **thinking block followed by
`tool_use`**.

What happens on every switch (both directions, all runs):

1. Process start writes attachment `{"type":"credential_org","organizationUuid":…}`.
2. If earlier thinking was written under another org, Claude Code emits a stream frame
   `{"type":"system","subtype":"informational","level":"warning","content":"Claude can't use thinking from another organization\nClaude's earlier thinking was written while you were signed in to another organization, so Claude can't use it now. It isn't deleted: resume this session signed in to that organization to use it again. Claude's next responses may be slower and use more tokens…"}`
   (also persisted as a `system/informational` transcript line).
3. The blocks are still sent; the request succeeds; Claude Code records
   `{"type":"thinking_drop","thinkingBlocksSent":2,"newlyDropped":{"blockCount":1,"reason":"other",…}}`
   — only the foreign-org block is dropped; A's own blocks are used again when the chat comes back
   to A (`sent 2 / dropped 1` on the A → B → A leg).

Results: sonnet A (thinking + tool_use, interrupted) → B `continue` → finished the job ("finished
7217") → two more B turns, one with thinking → back to A `continue` and a summary turn: all
`success`, **no 400, no `api_retry`**, answers consistent (1031 → 7217 → 17 → 289). No mitigation
(stripping thinking) is needed, and stripping would be harmful: it would lose the blocks that are
valid again when the chat returns to their org.

Note: a resume always appends a new user message (and after a kill, a synthetic tool_result), so
the API never has to continue a foreign-org assistant tool loop in place.

## 4. `/compact` then switch (VERIFIED, `exp4.py`)

`/compact` as user text under A → `system/compact_boundary{trigger:"manual",pre_tokens:22133,
post_tokens:2864,preserved_segment:{…}}`. Resume under B: "original secret, current secret, notes.txt"
→ 58213, 58214 and both file lines; a second B turn; back to A → 58214. The preserved segment
still carried one A thinking block, which B dropped (`thinking_drop`), without the informational
frame this time. No errors.

## 5. Limits and account info per account (VERIFIED)

- **OAuth usage endpoint** (`GET https://api.anthropic.com/api/oauth/usage`, headers exactly as
  `internal/server/usage.go`) with each dir's `.credentials.json` access token: HTTP 200 for both.
  A (Team): `five_hour.utilization 17`, **`seven_day: null`**, `limits:[session 17 %, weekly_scoped
  "Fable" 0 %]`. B (Max): `five_hour 0`, `seven_day 85 %` (`limits` kind `weekly_all`, severity
  `warning`). `usage.go` already tolerates the missing seven-day window. Access tokens expire ~8 h
  after the last refresh; `usage.go` skips expired tokens, so an account that no process has used
  for 8 h shows no limits until Claude Code runs under it again (INFERRED from code).
- **`rate_limit_event` differs per account** and is emitted on the first request of a process (later
  turns of the same process mostly have none):
  A `{"status":"allowed","rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","unifiedWindows":{"five_hour":{"utilization":0.09,…}}}`;
  B `{"status":"allowed_warning","rateLimitType":"seven_day","utilization":0.84,"surpassedThreshold":0.75,"unifiedWindows":{"five_hour":…,"seven_day":{"utilization":0.84,…}}}`.
- **Control `get_usage`** per process: `subscription_type` (`team` / `max`) and `rate_limits` of
  that account — same data as the endpoint, through the CLI, no token handling in Socrates.
- **Account identity headlessly**: `initialize` response `account` (email, organization,
  subscriptionType, apiProvider) — the cheapest per-process proof of which account a chat is on;
  `claude auth status --json` (email, orgId, orgName, subscriptionType, configDirectory) without a
  model call. `system/init` carries only `apiKeySource:"none"`, no account.
- **Cost**: the transcript's `cost-state.totalCostUSD` is cumulative over the whole session across
  accounts (0.00178 after A, 0.00405 after B, 0.00476 after A again), not per account (INFERRED
  from the numbers). A per-account split needs the per-turn `result` costs recorded with the
  account in the journal (as §4.4 plans).
- **Prompt cache after a switch**: the global-scope system prompt is still a cache hit (13 269
  tokens read), the conversation part is written again on the new account (~7.4–7.7 k cache-creation
  tokens in a 21 k context). For a long chat a switch costs one cache write of the whole context on
  the new account (INFERRED by extrapolation).

## 6. Credentials never cross (VERIFIED for normal runs, INFERRED for refresh)

- sha256 of both `.credentials.json` unchanged before/after all runs; mtimes unchanged (no refresh
  happened, tokens were fresh). Logins still valid afterwards (`auth status` `loggedIn:true` for
  both, correct org).
- `shared/` contains only transcripts, `memory/` dirs, one plan `.md` and task JSONs; no
  credential, token or `.json` config file; `grep refreshToken|sk-ant-o` over `shared/` finds
  nothing.
- `strace` of a full turn under B: writes go only to `claude-b/.claude.json` (+ `.lock`, `.tmp.*`,
  `backups/`), `claude-b/sessions/<pid>.json` + `<pid>.<hash>.key`, and the transcript through
  `claude-b/projects/…` (symlink). Nothing under `~/.claude` (one read of
  `~/.claude/state/unattended-serving-consent.json`, ENOENT), nothing in the other account's dir.
- Binary: credentials path is `join(<config dir>, ".credentials.json")` and the CLI watches that
  file's mtime (`lastCredentialsMtimeMs`) — the file sits in the dir root, never in a shared
  subfolder, so a refresh writes only its own dir (INFERRED; a forced refresh was not run to keep
  the logins safe).

## 7. What else lives per config dir (VERIFIED unless marked)

| path in `CLAUDE_CONFIG_DIR` | share? | why |
|---|---|---|
| `projects/` (transcripts, `<esc>/memory/`) | **share** | the conversation; auto-memory is per project, so it follows the chat across accounts (Claude Code creates `projects/<esc>/memory/` on every start) |
| `plans/` | **share** | plan mode wrote `shared/plans/make-a-very-short-foamy-pascal.md` |
| **`tasks/`** | **share (new)** | with `CLAUDE_CODE_ENABLE_TODO_TOOLS=1` the model gets `TaskCreate/TaskList/TaskUpdate` (no `TodoWrite` in 2.1.309) stored as `tasks/<sid>/{1,2,3}.json` + `.lock`. Unshared, B's `TaskList` said "No tasks found"; after symlinking, B listed and completed A's tasks. Without the env var the task tools are absent |
| `todos/` | harmless | stays empty in 2.1.309; keep the link for older behaviour |
| `.credentials.json` | **never** | the login |
| `.claude.json` | **never** | `oauthAccount`, `userID`, GrowthBook/experiment caches, `cachedUsageUtilization` (per `accountUuid`), model access caches; no `projects` trust entries are written in `-p` mode |
| `cache/model-catalog`, `policy-limits*`, `remote-settings*` | **never** | per org (the orgs have different tool and model policy) |
| `backups/` | no | backups of `.claude.json` |
| `sessions/<pid>.json` + `.key` | no | live-process registry (pid, sid, status, messaging socket). A SIGKILLed process leaves a stale entry (`claude-a/sessions/553686.json`, status `busy`) |
| `session-env/<sid>/` | not needed | empty dirs here (SessionStart-hook env; Socrates uses no hooks). Sharing would be harmless, being keyed by sid (INFERRED) |
| `shell-snapshots/` | no | per process shell snapshot |
| `file-history/` | n/a | not written in `-p` mode (confirmed again) |
| `statsig/` | n/a | not created; feature flags live in `.claude.json` now |

Outside the config dir and shared by every account anyway: `/tmp/claude-0/<esc cwd>/<sid>/tasks`
(background task output), `~/.local/state/claude/locks`.

## 8. Things to know (VERIFIED unless marked)

- **The other account's email travels with the chat.** Each process start adds a
  `session_context` attachment "The user's email address is …" for the current account; earlier
  attachments stay in history, so B's requests contain A's email and vice versa (checked in the
  `api-request-blob` hashes referenced by B's `api-request`). Fine for one person's own accounts;
  worth a line in the docs if accounts are ever shared between people.
- **System-prompt snapshot**: the prompt is recorded once (`prompt_snapshot`, `--system-prompt-snapshot
  on` default) and replayed after a switch until the next compaction. It holds nothing
  account-specific; per-account tool differences arrive as `deferred_tools_delta`.
- **One process per session id at a time** is the host's job: two live processes on one
  transcript would interleave writes (INFERRED, not tested).
- Interrupt leaves the process alive; the host can end it with stdin EOF before relaunching.

## Recommendation for REBUILD.md §3.1 / §4.3

1. Keep the planned design: one `CLAUDE_CONFIG_DIR` per account, logins written by Claude Code
   itself, switch = end the process + `--resume <sid>` with the other account's env. No transcript
   surgery, no thinking stripping, no handoff summary — Claude Code 2.1.309 drops foreign-org
   thinking on its own and keeps it for the way back. (Unlike Codex/S2.)
2. §3.1 shared set: change "Same for `todos/`, `plans/`" to **`plans/`, `tasks/` (task tools),
   `todos/`** — `tasks/` is required, without it the task list vanishes on a switch. Never share
   `.credentials.json`, `.claude.json`, `cache/`, `policy-limits*`, `remote-settings*`, `sessions/`.
   Create the links before the first run of a new account (the sign-in shell of §4.2 can create
   them; Claude Code accepts pre-existing symlinks).
3. §4.3: after relaunch, read `initialize.account` and put email/org/plan into the
   `account.switched` divider; map `system/informational` "can't use thinking from another
   organization" to a small hint on that divider (or hide it), not to an error. Mid-turn switch =
   interrupt → EOF → relaunch → send `continue`; it works and also after a crash (synthetic
   tool_result). Tell the person that a switch makes the next reply slower (cache re-created on
   the new account, thinking re-done).
4. §4.4: limits per account work both through the OAuth usage endpoint (as `usage.go` does) and
   through `get_usage` / `rate_limit_event` of a running process; a Team account has no seven-day
   window. Cost per account must come from per-turn `result` costs tagged with the account in the
   journal, because `cost-state` in the transcript is cumulative across accounts.
5. The host guarantees a single CLI process per session id.
