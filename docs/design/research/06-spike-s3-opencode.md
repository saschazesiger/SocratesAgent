# 06 – Spike S3: one `opencode serve` per active chat on a shared OpenCode store

Date: 2026-10-23. Binary: opencode 1.19.4 (`/root/.local/bin/opencode`). Source read: `sst/opencode` tag
`v1.19.4` (commit `148842ef`, 2026-10-22), cloned to `scratchpad/s3-lab/src/oc`.
Plan context: REBUILD.md §2.1, §3.3, §4.3. Earlier research: `05-opencode.md`.

Tags, as in 05:

- **VERIFIED**: I observed it against the installed binary in an isolated lab.
- **DOCUMENTED**: I read it in the v1.19.4 source.
- **INFERRED**: my conclusion. I did not test it directly.

## 0. Short answer

**Yes, with one change to the plan.** Several `opencode serve` processes can share one session store. Each process can have its own account. A chat can move between processes, and history, model context, events and revert all survive the move (all VERIFIED).

The change: **do not pass OAuth accounts in `OPENCODE_AUTH_CONTENT`.** That variable is read-only. Writes, including every OAuth token refresh, go to `<XDG_DATA_HOME>/opencode/auth.json`, and the process never reads them back.

Instead, give every account its own `XDG_DATA_HOME`. That directory holds the account's own `auth.json`. Point every process at the one shared database with `OPENCODE_DB=<abs path>`, and symlink the few shared sub-folders. This layout passed every test (§3).

## 1. Lab and safety

- **Location.** Everything ran under `scratchpad/s3-lab/`, with `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME` and `XDG_STATE_HOME` pointing into it.
- **Setup.** A throwaway git project sat at `s3-lab/proj`. Config came from `OPENCODE_CONFIG_CONTENT` (allow everything, `question: deny`, share disabled). `OPENCODE_DISABLE_AUTOUPDATE`, `OPENCODE_DISABLE_MODELS_FETCH` and `OPENCODE_DISABLE_CLAUDE_CODE` were set. The scripts are in `scratchpad/s3-tools/` (`oc.py` holds the helpers, `exp*.py` the experiments), and their raw output is in `s3-lab/exp*.out` and `s3-lab/logs/*.log`.
- **Models.** I used free models only: `opencode/deepseek-v4.1-flash-free` (Zen) and `openrouter/nvidia/nemotron-3.5-lightning:free`. Total cost was 0.
- **Zen limit.** Zen's free quota ran out during the stress test ("Free usage limit reached"). Everything after that ran on OpenRouter.
- **Credentials.** I copied only the `openrouter` entry of the real `auth.json`, into a 0600 temp file. I also made a bogus key for the isolation test. All copies were deleted at the end; `find s3-lab -name auth.json` comes back empty. The key never appeared in a log, the DB or `export.json` (checked with grep).
- **Real installation.** The real `~/.local/share/opencode/*` and `~/.config/opencode/*` have the same mtimes and sizes as before the spike (`s3-lab/real-baseline.txt`, diffed at the end). I did not touch the systemd service or tmux.

## 2. How the paths are resolved (DOCUMENTED, v1.19.4 source)

| What | Path | Source |
|---|---|---|
| Data root | `$XDG_DATA_HOME/opencode` (`Global.Path.data`) | `packages/core/src/global.ts` |
| **Database** | `OPENCODE_DB` if set: either `:memory:`, an **absolute path**, or a path relative to the data root. Otherwise `<data>/opencode.db`, or `opencode-<channel>.db` on non-release channels. | `packages/core/src/database/database.ts` `path()` |
| DB pragmas | `journal_mode=WAL`, `synchronous=NORMAL`, **`busy_timeout=5000`**, `foreign_keys=ON` | same file |
| **Provider credentials** | `<data>/auth.json`, hard-wired. There is no env var for this path. | `packages/opencode/src/auth/index.ts` |
| `OPENCODE_AUTH_CONTENT` | `Auth.all()` returns `JSON.parse(env)` on **every call**, and the file is ignored completely. `Auth.set()` writes `{...all(), [id]: info}` **to the file**, so the env content is merged into `<data>/auth.json`. | same file |
| Other data-root users | `snapshot/<project>/<worktree-hash>` (revert/diff git store), `storage/` (session diffs), `tool-output/`, `plans/`, `worktree/`, `repos/`, `mcp-auth.json` (MCP OAuth), `log/` | grep for `Global.Path.data` |
| Config dir | `$XDG_CONFIG_HOME/opencode`, or `OPENCODE_CONFIG_DIR` | `global.ts` `make()` |
| v1 OAuth refresh (ChatGPT) | The fetch wrapper calls `getAuth()` before every request, which reads `Auth.get` again. If the token has expired, it refreshes and calls `client.auth.set(...)`, which lands in `Auth.set` and so in the file. Concurrent refreshes are deduplicated **only inside one process** (`refreshPromise`). | `packages/opencode/src/plugin/openai/codex.ts:330-400` |
| Migrations | An in-process semaphore, plus a "does the `session` table exist?" check **outside** any transaction. Each migration runs in its own transaction. | `packages/core/src/database/migration.ts` |
| SQL `account` / `credential` tables | These sit in the shared DB, but the v1 runner does not use them for provider credentials. `account` is the opencode.ai console login; `credential` belongs to the v2 runner (see 05 §6). | grep |

What follows from this:

- **The database path can be split from the data root**, and so from `auth.json`. This is the key fact for S3.
- **`OPENCODE_AUTH_CONTENT` is unsafe for OAuth (INFERRED from the code above).** After the access token expires, every request sees the stale token from the env and refreshes again with the **same, already-used refresh token**. The refreshed tokens go to the data-root `auth.json`, which this process never reads. ChatGPT rotates refresh tokens, so the second refresh fails, and that can invalidate the login. It also writes the account's tokens into whichever `auth.json` sits in the shared data root.

## 3. Results

### 3.1 Concurrency on one store (question 1) – VERIFIED, safe

**Stress 1** (`exp1.py`, layout A = one shared data dir):

- **Setup.** 4 servers × 5 sessions each, 2 rounds of 20 prompts in parallel.
  - Round 1: a bash tool call (`echo TOKxy >> out-….txt`) plus text.
  - Round 2: "what token did you echo?"
  - Meanwhile, 3 threads polled `GET /session` and `GET /session/status` across all servers without pause.
- **Prompts.** 40/40 returned 200. 40/40 answers were correct, so the model context survived concurrent writes.
- **Polling.** 3 438 list/status polls returned 200, with 0 errors.
- **Cross-server reads.** Every session read the same from all 4 servers (5 messages each).
- **Events.** Each server's `/event` carried 318–417 events for its own sessions and **0** for other servers' sessions.
- **Database.** `PRAGMA integrity_check` returned `ok`. Rows (25 sessions, 110 messages, 286 parts) match exactly what was sent.

**Stress 2** (`exp1b.py`, write storm):

- **Setup.** 4 servers × 8 threads ran create → `PATCH title` → `DELETE` every second session, as fast as possible. At the same time, 12 long streaming prompts ran.
- **Writes.** 1 920 creates, 1 920 patches and 960 deletes in 30 s, **0 errors**. The surviving row count matched exactly, and `integrity_check` returned `ok`.
- **Logs.** No `database is locked`, `SQLITE_BUSY`, `corrupt` or `malformed` anywhere in the server logs.
- **Earlier run.** The same storm with 3 servers, where the 12 long prompts also completed, likewise had 0 errors.
- **Side effects seen:**
  - In the second run all 12 long prompts hung in `retry` because the Zen free quota ran out. That was the provider, not the DB.
  - One server logged `cleanup failed: gc is already running` on the shared snapshot git store. This was harmless: one process ran `git gc`, and the others skipped it.

**Fresh DB race** (`exp8.py`) – VERIFIED, **must be handled**:

- **Setup.** 4 processes started at the same instant on a **new** DB.
- **Result.** 2 of the 4 died at startup with `ServeError` (`Failed query: CREATE TABLE workspace …`, table already exists). The other 2 worked, and the DB was intact. Restarting a loser succeeded.
- **INFERRED:** the same race applies to the incremental migrations after an opencode **update**.
- **Fix (VERIFIED):**
  1. After installing or updating opencode, run `opencode db "select 1"` with the shared `OPENCODE_DB` once. This applies all 38 migrations in 0.7 s.
  2. Only then start serves.
  3. Treat a startup failure as retryable once.

### 3.2 Per-process credentials (question 2)

**`OPENCODE_AUTH_CONTENT` on a shared data dir** (`exp2.py`) – VERIFIED:

- **Isolation of reads works.** Process A (real OpenRouter key) and process B (bogus key) ran on the same data dir and prompted at the same moment. A answered `PONG`; B got `APIError 401 "User not found."`.
- **Writes leak into the shared file.**
  - Before the test, the shared `auth.json` did not exist.
  - After one `PUT /auth/kimi-for-coding` on B, the shared `auth.json` contained **B's bogus openrouter key** (from B's env) plus the new entry.
  - A third process C, started **without** the env var on the same data dir, read that file and showed `openrouter` as connected.
- **Writes are never read back.**
  - I called `PUT /auth/openrouter` on B with the *good* key, then `POST /instance/dispose`.
  - B still got 401, because the env wins over the file.
  - This is exactly the OAuth refresh path from §2, so a refreshed token would be lost the same way.

**Per-account data dir with a shared DB** (`exp3.py`) – VERIFIED, the recommended layout:

```
<data>/agents/opencode/
  opencode.db (+ -wal, -shm)          <- OPENCODE_DB=<abs path> for every process
  snapshot/ storage/ tool-output/ plans/ worktree/ repos/
  accounts/<acct>/opencode/           <- XDG_DATA_HOME=<data>/agents/opencode/accounts/<acct>
      auth.json                       <- this account only (0600)
      snapshot -> ../../../snapshot   (likewise storage, tool-output, plans, worktree, repos)
      log/  mcp-auth.json             <- per account, fine
```

What the test showed:

- **No stray databases.** The `acctA` directory never got an `opencode.db`, and the snapshot git store was created in `shared/`.
- **The account's own file is used.** Account B's process got 401 with B's bogus file.
- **Writes stay with the account.** `PUT /auth/openrouter` on that process wrote **only** `acctB/auth.json`; account A's file was byte-identical afterwards.
- **Writes are read back.** After `instance/dispose`, the next prompt on B succeeded. A refreshed OAuth token therefore persists for that account (**INFERRED** for OAuth, because I used no OAuth account; the mechanism is the same file read on every request).

### 3.3 Handover A → B (question 3) – VERIFIED in both layouts

**Per-account layout** (`exp3.py`):

1. P1 (account A) ran turn 1: "remember 4711", plus a `write` tool call that created `notes.txt`.
2. P1 was stopped with SIGTERM.
3. P2 was started (account B) and listed the same 3 message IDs.
4. P2's first prompt failed with 401 (bogus key B), which proves P2 really used B's credentials.
5. I fixed the key via `PUT /auth` + dispose. The next prompt, "what is the secret number?", answered **`4711`**.
6. P2's `/event` carried the whole turn: `message.updated`, `message.part.updated`, `message.part.delta`, `session.status`, `session.idle` and more (78 events).
7. **`revert` on P2 to P1's first message deleted `notes.txt`, and `unrevert` restored it.** So the shared snapshot store via symlink works across processes.

**Shared-data-dir layout** (`exp3a.py`): A (env) → stop → B (bogus env) got 401 → stop → A again recalled **`2389`**, and events arrived on the new process.

**Startup cost of a switch:** about 1.9 s to a ready process (§3.5).

**Stopping mid-turn** (`exp4b.py`, `exp4c.py`) – VERIFIED:

- **SIGKILL or plain SIGTERM during a running bash tool.** The process exits immediately, in 0.06 s for SIGTERM. The assistant message stays `time.completed = null` with the tool part `running` **forever**. No other process repairs it.
- **Continuing afterwards.** The next process accepted a new prompt on that session; the model re-ran the command and finished.
- **Clean stop.** `POST /session/{id}/abort`, then SIGTERM. The turn was closed properly (`MessageAbortedError`, tool part completed).

### 3.4 Two processes serving one session at once (question 4) – VERIFIED, must be prevented

Run (`exp4.py`):

1. P1 ran a long turn on session S. P2 then got `prompt_async` on the same S.
2. Both reported S as `busy` in their own `/session/status`. Busy state is in-memory per process, and neither process saw the other.
3. `abort` on P2 aborted only P2's turn (`MessageAbortedError`); P1 stayed busy.
4. **P1 then answered P2's prompt anyway.** After its own turn, P1's loop picked up the newer user message from the DB and produced a second, complete assistant message for it (`PONG-FROM-P2`).
5. Result: two assistant messages for one user message, one of them aborted. All of P1's work (355 events) was invisible on P2's `/event` (11 events).

Consequences:

- **Rule:** exactly one live process per session. The agent host already guarantees that (one host per chat).
- **Handover only when the session is idle in the old process** (or after `abort` + `session.idle`). A process whose loop is still running can keep consuming messages that were written through another process.

### 3.5 Startup cost and memory (question 5) – VERIFIED

| Measurement | Value |
|---|---|
| `serve` start → `/global/health` OK | **1.81–2.03 s** across every measured start (warm page cache) |
| First call for a directory (instance boot) | 0.07–0.23 s |
| First trivial prompt | 2.6–3.9 s, mostly the model |
| RSS idle, before any instance | ~240 MB |
| RSS after one turn | 460–660 MB; after the stress 470–525 MB |
| **PSS** (4 processes in parallel, after one turn each) | **~400 MB each**, ~360 MB after 60 s idle. Only ~60 MB per process is shared. |
| `opencode db "select 1"` (migrate) / `opencode export` | 0.7 s / under 1 s |

For sizing, plan on **~0.4 GB per active chat**, which makes 10 active chats ≈ 4 GB. The host has 31 GB. Parking idle chats after 20 min (§2.1) is necessary, not optional.

Stopping:

- **SIGTERM** stopped a process in 0.02–0.07 s in 31 of 33 measured stops.
- **The exceptions.** The 2 hangs (one >6 min, one >5 s, both needing SIGKILL) happened while the config dir's `node_modules` did not exist yet. Both were the first process of a run, stopped right after its first turn. The log shows the background `npm install @opencode-ai/plugin` into the config dir being cut off ("background dependency install failed … process terminated"). The process then did not exit.
- **INFERRED:** the npm install in progress is what blocks exit.
- **Fixes:**
  - Pre-populate the config dir's `node_modules` on install. Once it existed (from 12:30 on), no stop hung again.
  - Always use TERM → 5 s → KILL.

Ports:

- **Fixed ports can collide with ephemeral ports.** A fixed `--port 47444` failed with `ServeError`, because `cloudflared → socrates` was using 47444 as an ephemeral source port (range 32768–60999).
- **Use `--port 0`** (the default) and parse `opencode server listening on http://127.0.0.1:<port>` from stdout. Otherwise pick ports below 32768.
- **DOCUMENTED:** `serve` has no unix-socket option (`--port`, `--hostname`, `--mdns`, `--cors` only).

### 3.6 Fallback: one data dir per account plus export/import (question 6) – VERIFIED, not needed

- **No HTTP endpoint.** The OpenAPI spec has no session export or import endpoint; `/sync/*` is workspace sync.
- **The CLI works:**
  - `opencode export <sid>` (also with `--sanitize`) produced `{info, messages[7]}` with all parts.
  - `opencode import <file>` into a **separate, fresh** data dir took 0.9 s.
  - A server on that data dir continued the session, and the model recalled **`4711`**.
- **Drawback: snapshots are not carried over.** `revert` on the imported copy logged `file did not exist in snapshot, deleting` and deleted `notes.txt`. That happened to be correct here, but with a missing snapshot store, reverting an edit to an existing file is at least unreliable (INFERRED).
- **Other drawbacks:** every switch copies the whole session, the copies diverge, and the session ID is reused in two stores.
- **Verdict.** This is worse than the shared-DB layout in every respect. Keep it only as an emergency tool.

The REBUILD.md fallback ("one serve per account with `PUT /auth` + `instance/dispose`") is also unnecessary. `PUT /auth` + dispose works (05 §6; again in `exp3.py`), but it mutates the account's stored credential and needs a process per data dir anyway.

### 3.7 Side observation relevant to §4.3 (limits) – VERIFIED

- **What a limit looks like.** When Zen's free quota ran out, the session did not fail. It stayed in `session.status {type:"retry", attempt:1, message:"Free usage exceeded, subscribe to Go", action:{reason:"free_tier_limit", provider:"opencode", title:…}}`.
- **How long it lasts.** I watched for 25 s or more, and earlier runs sat there until my 600 s timeout. `abort` ends it.
- **Effect on the server.** `/session/status` stayed responsive throughout.
- **Consequence:** the OpenCode adapter should map `retry` with a limit-type `action.reason` to the `rate_limit` / "limit reached" path. It should not wait for an error that never comes. Which `reason` values subscription providers use is not verified.

## 4. Remaining risks

- **Two chats on the same OAuth account.** These are two processes reading and writing the same `accounts/<acct>/auth.json`. The refresh dedupe is per process, so if both notice the expiry within one refresh round-trip, both refresh with the same refresh token, and ChatGPT may reject the second (INFERRED, not testable without two ChatGPT logins).
  - **Why it is narrow:** the token is read again before every request, so after one process has refreshed, the other sees the new token and does not refresh.
  - **Mitigation:**
    - Treat 401 / `refresh_token_reused` on an OAuth account as "sign in again" with a clear card.
    - Optionally, Socrates refreshes OpenCode OAuth tokens itself shortly before `expires`, by writing the file while no turn is running.
- **Orphaned `running` tool parts** after a crash or reboot (§3.3). The adapter renders them as "interrupted". The journal rebuild in §2.2 must not treat them as live.
- **Migrations after updates** (§3.1) are handled by the pre-migrate step.
- **`/sync/*` and the `event` table are global per DB.** With one process per session this was fine in every test, but I did not study it deeply.

## 5. Recommendation for REBUILD.md §3.3

Replace the "Shared data dir … `OPENCODE_AUTH_CONTENT` … Spike S3 must confirm …" bullet with:

- **One `opencode serve` per active chat, child of the agent host**, started with `--hostname 127.0.0.1 --port 0`. The adapter reads the port from the `listening on` stdout line. Env: `OPENCODE_SERVER_PASSWORD`, `OPENCODE_CONFIG_CONTENT` as planned, `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_DISABLE_MODELS_FETCH=1`.
- **Store layout:**
  - One shared DB, `OPENCODE_DB=<data>/agents/opencode/opencode.db` (an absolute path), for every process.
  - **Per account,** `XDG_DATA_HOME=<data>/agents/opencode/accounts/<acct>`, holding that account's own `opencode/auth.json` (0600).
  - `snapshot`, `storage`, `tool-output`, `plans`, `worktree` and `repos` inside it are relative symlinks to `<data>/agents/opencode/…`.
  - One shared `XDG_CONFIG_HOME`/`XDG_CACHE_HOME` for OpenCode, with `node_modules/@opencode-ai/plugin` pre-installed.
  - Verified: there were no lock errors under 4 × concurrent writers, credentials stayed isolated, writes stayed in the account's own file, and handover kept history, context, events and revert.
- **Credentials:**
  - OAuth accounts **must** live in the account's `auth.json`. **Never** put them in `OPENCODE_AUTH_CONTENT`: it is read-only, so refreshed tokens would be lost and leak into the data root.
  - API-key accounts may use `OPENCODE_AUTH_CONTENT` to keep the key off disk (verified isolated). Socrates must then never call `PUT /auth` on that process.
  - Sign-in through `/provider/{id}/oauth/*` runs on a process of the target account, so the result is written to its file.
- **Account switch** = park and resume on the same session ID:
  1. If the chat is busy: interrupt or wait for the turn (`abort` → `session.idle`).
  2. Stop the process: SIGTERM, then SIGKILL after 5 s.
  3. Start the new account's process and continue the session there.
  - Never run two processes on one session. A live loop in the old process picks up messages written through the new one (verified).
  - No `PUT /auth` or dispose dance, and no export/import.
- **Install/update step:** run `opencode db "select 1"` once with the shared `OPENCODE_DB` before any serve starts (concurrent first starts race on migrations, verified).
- **Sizing:** ~2 s per start, ~0.4 GB PSS per active chat. Parking stays mandatory.
- **Adapter details:**
  - Render a dangling `running` tool part on an incomplete assistant message as interrupted.
  - Map `session.status retry` with a limit `action.reason` to the limit-reached flow (§4.3).
  - Tell the person when a second chat on the same OAuth account hits a refresh conflict ("sign in again").
