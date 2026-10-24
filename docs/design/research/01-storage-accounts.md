# 01 — Persistence, S3 and accounts/profiles (research, read-only)

Repo: `/root/SocratesAgent` at `225a737`. Live host inspected with `ls`/read-only SQLite only.
Anything not read directly from code or disk is marked **(inferred)**.

---

## 1. On-disk layout today

### 1.1 Socrates' data directory

- Path: `--data` flag, default `config.DataDir()` = `$SOCRATES_DATA_DIR` or `/var/lib/socrates`
  (`internal/config/config.go:466-474`, `main.go:55`). Created 0700 (`main.go:88`).
- Default workspace root: `$SOCRATES_WORKSPACE_ROOT` or `<data>/workspaces`
  (`internal/config/config.go:478-483`), but the **stored settings document wins** once it has a value
  (`config.go:388-392`).

| Path under `<data>` | What | Written by |
| --- | --- | --- |
| `socrates.db` (+ `-wal`, `-shm`) | SQLite, WAL, single connection. Schema v9 | `main.go:91`, `internal/store/store.go:171-197`, schema `store.go:64-167` |
| `socrates.db.pre-vN.bak` | VACUUM INTO copy before every schema migration | `store.go:281-299, 442-476` |
| `socrates.db.pre-connect.bak` | copy before the connect page replaces local state | `internal/server/connect.go:576` |
| `s3.json` (0600) | bootstrap: endpoint, region, bucket, prefix, access key, **secret key, passphrase** | `internal/mirror/config.go:28, 167-252` |
| `sessions/<id>/` | per-session generated files: `plan.json` (0600; argv, env, cwd, files, CLI session id, port — includes OpenCode's server password), `journal.raw` (pane output, rotated at 64 MiB, keep 1), `claude-settings.json`, `initial-prompt`, OpenCode `tui.json`, VS Code `ide.sock`/`ipc.sock`/`code-server.yaml`/`vscode-data` | `internal/harnesses/harness.go:79-87`, `internal/termux/journal.go:16-28`, `internal/termux/manager.go:682-698`, `harnesses/claude.go:39,100`, `harnesses/prompt.go:33,69`, `harnesses/opencode.go:36,154`, `harnesses/vscode.go:29,82-105` |
| `profiles/<id>/{claude,codex,xdg-data}` | per-profile CLI state dirs (see §3) | `internal/harnesses/profiles.go:50-86` |
| `vscode/<sha256(folder)[:8]>` | code-server user-data-dir per folder | `harnesses/vscode.go:35-38` |
| `vscode-extensions/` | code-server extensions dir | `harnesses/vscode.go:105` |
| `workspaces/` | default root for "dynamic" workdirs | `config.go:478-483` |
| `tmux.sock`, `tmux.conf` (regenerated every start) | Socrates' private tmux server | `internal/termux/manager.go:193-194` |
| `hook.sock` | tmux hook socket | `internal/termux/hooks.go:35` |
| `bin/cloudflared` | tunnel binary | `internal/server/server.go:186` |
| `updates.json`, `updates.log`, `update-state/` (incl. `backup/socrates.db`, `units.previous/`) | updater state | `internal/server/updates.go:134,149,314`, `scripts/update.sh` |

Live `/var/lib/socrates` (ls, 2026-10-23): `bin/ hook.sock s3.json sessions/ socrates.db{,-shm,-wal} socrates.db.pre-v9.bak tmux.conf tmux.sock update-state/ updates.json updates.log vscode/ vscode-extensions/ workspaces/` — total ~47 MB.
**There is no `profiles/` directory** (see §3.4) and only **one** `sessions/<id>/` dir although the DB has 33 session rows.

### 1.2 What is inside `socrates.db` (schema v9, `store.go:64-167`)

- `kv(key,value)` — settings document (`settings`), `password_hash`, `profiles`, `profiles.order`,
  `push.vapid`, `push.subscriptions`, `activity.unread`, `activity.since`, `outcomes`,
  `resume_note:<id>` (641 rows live), `agents_catalog` (cache), `rev_high_water`,
  `mirror.instance|bucket_id|identity|meta`.
  Settings document holds secrets too: OpenRouter API key, Google TTS key, Cloudflare tunnel token
  (`internal/config/config.go:44-60,143-147,260-261`).
- `logins(token, created_at, expires_at)` — browser login cookies (`internal/store/logins.go`).
- `sessions(...)` — one row per terminal session incl. `harness, model, effort, workdir, workdir_mode,
  options(JSON, incl. "profile", "profile_auto", "ultracode"), tmux_name, cli_session_id,
  cli_session_state, state, status, start_at …` (`store.go:76-104`).
- `drafts(session_id, text, rev, updated_at)` — PreStart prompts.
- `mirror_pending(object, at)` — upload queue for the bucket.
- `cost_convs`, `cost_hours` — cost ledger (mirrored); `cost_seen` — local fingerprints (not mirrored).

Live counts (read-only): sessions = 27 claude + 6 vscode; logins = 11; drafts = 10; cost_convs = 84;
mirror_pending = 0.

### 1.3 Harness state (outside the data dir; HOME=/root from `deploy/socrates.service`)

| Harness | Default profile | Non-default profile | Code |
| --- | --- | --- | --- |
| Claude Code | `$CLAUDE_CONFIG_DIR` or `~/.claude` (transcripts `projects/<mangled-cwd>/<id>.jsonl`, `.credentials.json`, `history.jsonl`, settings, skills, plugins…) **and** global config `~/.claude.json` (sibling of `~/.claude` when the var is unset, inside the dir when set) | `<data>/profiles/<id>/claude` via `CLAUDE_CONFIG_DIR`; global config at `<dir>/.claude.json` | `harnesses/claude.go:285-318`, `profiles.go:62-69,126-131` |
| Codex | `$CODEX_HOME` or `~/.codex` (`auth.json`, `config.toml`, `sessions/YYYY/MM/DD/rollout-*.jsonl`, `archived_sessions/`, several `*.sqlite`) | `<data>/profiles/<id>/codex` via `CODEX_HOME` | `harnesses/codex.go:161-170`, `profiles.go:71-77,132-137` |
| OpenCode | `$XDG_DATA_HOME/opencode` or `~/.local/share/opencode` (`auth.json`, `opencode.db`, `storage/`, …) | `XDG_DATA_HOME=<data>/profiles/<id>/xdg-data` → `…/xdg-data/opencode` | `harnesses/opencode.go:283-291`, `profiles.go:79-92,138-155` |
| VS Code | `<data>/vscode/<hash>`, `<data>/vscode-extensions` | n/a | `harnesses/vscode.go` |

Live sizes: `~/.claude` 111 MB, `~/.claude.json` 140 KB, `~/.codex` **1.5 GB** (one `thread_history_1.sqlite` is 125 MB), `~/.local/share/opencode` 157 MB.

Also on the host but **not Socrates code**: `/root/.codex-multi/<6 account names>/` (full CODEX_HOME copies with `auth.json`) and `/root/.config/opencode-multi-auth/accounts.json` — a user-managed multi-account setup that Socrates does not reference (grep finds nothing).

### 1.4 Uploads, other state

- Browser uploads land **inside the working directory**: `<workdir>/.socrates/uploads` (`internal/server/upload.go:24-26,88`).
- `/etc/socrates.env` — listen address, update policy, optionally `SOCRATES_S3_*` (`deploy/socrates.service: EnvironmentFile`).
- Systemd: `socrates.service` (HOME=/root, WorkingDirectory=/root, `TimeoutStopSec=60` sized for the final bucket flush), `socrates-update.{service,timer}`; tmux lives in `socrates-tmux.scope` so sessions survive restarts (`deploy/socrates.service`).

---

## 2. How S3 is used today

### 2.1 Role: store of record, mandatory

- `storage.go:1-20` (package doc): *"S3 is the store of record and it is mandatory: a Socrates that has no bucket it can use serves the connect page and nothing else."*
- `main.go:135-143`: `openStorage` runs **before** `server.New`; the listener is opened late on a normal start (after hydrate) and early in connect mode (`main.go:104-133`).
- DECISIONS.md Revision 6 (`docs/design/DECISIONS.md:191-251`); README "Storage: an S3 bucket is required" (`README.md:129-169`); commit `17523a3` message ("S3 as the store of record … A bucket is mandatory").

### 2.2 Startup decision (`storage.go:342-461`)

1. No config (no `s3.json`, no `SOCRATES_S3_BUCKET`) → **connect mode**: only setup page + `/api/health` + `/api/storage/{connect,takeover,retry}` (`internal/server/connect.go:140-160`). Connecting writes `s3.json` and **re-execs** the process (`storage.go:494-499`, `reexec_unix.go`).
2. Config present → `mirror.Connect` (`internal/mirror/connect.go:23-82`): read `meta.json` (or create it for an empty place), derive key (PBKDF2 600k), probe conditional writes, **acquire lease**, wait out a takeover, **hydrate**, cache meta, enable tracking.
3. Bucket unreachable **and** this data dir was synced with exactly this bucket (`kv mirror.bucket_id/identity/meta`) → **Degraded** start from local copy (`connect.go:189-249`), writes queue as pending.
4. Anything else (wrong passphrase, lease held by another live host, access denied, unreachable with no local copy) → connect mode with a retry loop every 15 s/20 s/60 s (`storage.go:383-474`).

Config source: env (`SOCRATES_S3_*`, `SOCRATES_S3_BUCKET` is the switch) wins over `<data>/s3.json` (`internal/mirror/config.go:58-70,176-215`).

### 2.3 Sync direction and cadence

- **Write path (local → S3)**: every store write calls `s.touch(objectName)` under the write lock (16 call sites: `store/logins.go:23,67`, `store/sessions.go:262,327,368,396,446,531`, `store/prestart.go:114,237,314,337`, `store/costs.go:169,321`, `store/store.go:650,667`), which inserts into `mirror_pending` (`store/mirror.go:155-191`) and wakes the observer (`store/mirror.go:141-153, 217-223`).
  The mirror loop debounces **1 s, max 5 s latency**, exponential backoff up to 60 s on failure (`internal/mirror/mirror.go:93-144, 449-516`), uploads the current local value of each pending object sealed with the vault key, or deletes it if the row is gone (`mirror/sync.go:83-140`, seal at `sync.go:114`), up to 4 parallel requests.
- **Read path (S3 → local)**: **only at startup** (`hydrate`, `mirror/sync.go:185-276`) and on the connect page (`Attach`, `connect.go:300-401`). After start **no app data is read from S3 live**; the server reads/writes SQLite only. Live S3 reads are only lease heartbeats/read-backs (every 30 s), `meta.json` on reconnect, admin "Test connection" (`mirror.go:733-760`), and the password object during a takeover (`connect.go:447-466`).
- Hydrate semantics (`sync.go:185-276`): same installation and last writer was us → upload pending, then download everything (local := bucket); empty bucket → upload everything; otherwise → **bucket wins**, local pending is dropped, `DeleteLocalAbsent` removes local rows absent from the bucket (`store/mirror.go:613-643`).
- Shutdown: final flush + lease release, bounded 10 s (`main.go:188-197`, `mirror.go:671-717`).

### 2.4 Object layout in the bucket (`store/mirror.go:26-33`, `mirror/bucket.go:18-31`, `store/costs.go:21-34`)

```
<prefix>/meta.json          plaintext: format, bucket_id, kdf, iterations, salt, check
<prefix>/lease.json         plaintext: instance, host, version, acquired_at, heartbeat_at
<prefix>/kv/<escaped key>   sealed, one per mirrored kv row (deny-list: store/mirror.go:125-133)
<prefix>/logins.json        sealed, whole logins table
<prefix>/sessions/<id>.json sealed, session row incl. tmux_name, cli_session_id, cli_session_state
<prefix>/drafts/<id>.json   sealed
<prefix>/costs/<harness>/<conv>.json  sealed cost ledger
```

Not mirrored (`store/mirror.go:125-133`): `agents_catalog`, `rev_high_water`, `activity.since`, `outcomes`, `resume_note:*`, `mirror.*`. Not in the bucket at all (`mirror/mirror.go:19-22`, README table `README.md:137-139`): **HOME, CLI logins and transcripts, workspaces, tmux, journals, plan.json, VS Code, s3.json, profile directories**.

### 2.5 Encryption (`internal/vault/vault.go`)

AES-256-GCM, PBKDF2-SHA256 600 000 rounds, random 96-bit nonce, object name as AAD, magic `SOC1` (`vault.go:1-80`). Passphrase lives in `s3.json`/env only.

### 2.6 Leases / locking (`internal/mirror/lease.go`, doc at `lease.go:14-58`)

- `lease.json`, TTL 90 s, heartbeat 30 s, every write must start/end within 10 s (`VerifyEvery`) of a lease confirmation; takeover waits `VerifyEvery+2s`.
- Conditional writes (`If-None-Match: *`, `If-Match: <etag>`) when the store supports them (probed, `mirror.go:232-257`, `s3/probe.go`), else write + read-back.
- Takeover from another live host needs the Socrates password (`connect.go:440-466`).
- Fencing: a host that loses the lease stops uploading, forgets `mirror.bucket_id`, shows a banner, and **scheduled starts are blocked** via `AutoStartAllowed` (`mirror.go:363-413`, `server/storage.go:32-39`, `server/schedule.go:64-69`).
- Graceful release keeps the instance and zeroes the heartbeat, so "who wrote last" is known (`lease.go:51-57`, `sync.go:203-213`).

### 2.7 S3 client

Hand-written, stdlib only, SigV4, **no multipart/streaming**, bodies are byte slices, `maxBody` 256 MiB (`internal/s3/s3.go:1-40`). Fine for KB-sized rows; **(inferred)** unsuitable as-is for backing up GB-sized folders (e.g. `~/.codex` is 1.5 GB) without adding multipart or using an external tool.
`internal/s3/s3fake` is an in-memory S3 used by Go tests and by the e2e harness via `e2e/fakebin/fakes3` (`e2e/README.md:38-52`).

### 2.8 What would disappear if S3 became a pure periodic backup of a local folder

Code (≈10.7k lines incl. tests across the files below):

- `storage.go` + `storage_test.go` (connect-mode state machine, retry loop, re-exec), `reexec_*.go` (only needed for connect → restart), the late-listener ordering in `main.go:104-166`, the `m.Close` flush in `main.go:188-197`.
- `internal/mirror/*` almost entirely: `lease.go` (525), `connect.go` (Connect/Degraded/Attach/Peek/metaForEmptyPlace), `sync.go` (hydrate, pushAll, uploadPending, keepAlive), `mirror.go` (flusher loop, fencing, AutoStartAllowed, UpdateCredentials), `errors.go`, most of `bucket.go`. `config.go` (bootstrap config) would shrink to backup-target config.
- `internal/store/mirror.go`: object naming, `touch`/`recordPending`/observer, `mirror_pending` table, `ExportObject`/`ImportObject`/`DeleteObject`/`DeleteLocalAbsent`, `IsBackedUp`, `SetMirrorFenced`, plus the 16 `touch()` calls and the `tracking`/`fenced`/`observer`/`changed`/`pendingAt` fields in `store.go:49-61`.
- `internal/server/connect.go` (583 lines: unauthenticated connect page, takeover), `internal/server/storage.go` (status/test/sync/credentials endpoints), `setup.html` storage step, `js/storage.js` banner ("Storage unreachable", "Another Socrates took over"), draft `backed_up` flag (`server/prestart.go:337,392`), the scheduler's auto-start gate (`server/schedule.go:64-69`).
- `mirror.*` kv keys; `scripts/update.sh:225-260,650-651` health logic that distinguishes "connect mode".
- e2e: `fakes3`, scenarios `s3restore`, `storagebanner` (`e2e/README.md:291-292`).
- Could be kept/reused: `internal/s3` client (needs multipart for large files) and `internal/vault` (if backups stay client-side encrypted) — or replaced by restic/rclone **(inferred option)**.

---

## 3. Profiles / accounts

### 3.1 What a profile is

- A profile = **one directory per CLI + the env var that points the CLI at it** (`internal/harnesses/profiles.go:15-37`):
  `CLAUDE_CONFIG_DIR=<data>/profiles/<id>/claude`, `CODEX_HOME=<data>/profiles/<id>/codex`, `XDG_DATA_HOME=<data>/profiles/<id>/xdg-data`.
- The "Default" profile is id `""` and uses the CLIs' normal dirs under HOME (`profiles.go:62-92`, `server/profiles.go:45-47`).
- Shared config is **symlinked** from the default dirs into each profile on every launch (`profiles.go:94-104, 161-189`): Claude `settings.json, CLAUDE.md, agents, commands, skills, plugins, hooks, output-styles, keybindings.json`; Codex `config.toml, AGENTS.md, prompts, skills, rules`; OpenCode: every entry of the default XDG data home except `opencode/`. Claude MCP servers are copied from the default `~/.claude.json` into the profile's global config (`harnesses/claude.go:325-340`).
- The list is metadata only: kv `profiles` = `[{id:"p-<12 hex>", name, created_at}]`, kv `profiles.order` (`server/profiles.go:37-57, 401-409`). Both are mirrored to S3; **the directories are not**.
- Routes: `GET/POST /api/profiles`, `PATCH/DELETE /api/profiles/{id}`, `PUT /api/profiles/order` (`server/profiles.go:20-31`, `server/server.go:415-419`). Delete refuses while a session uses it and `rm -rf`s the dir (`server/profiles.go:327-366`).

### 3.2 Credentials and selection

- **Socrates never handles a credential** (`profiles.go:28-32`). Sign-in is done by the CLI inside a session in that profile: Claude asks by itself; a Shell session in a profile gets all three env vars for `codex login --device-auth` / `opencode auth login` (`harnesses/shell.go:53`, `README.md:171-186`).
- Detection (read-only, labels only): Claude → `<cfg>/.claude.json` `hasCompletedOnboarding` + `oauthAccount.emailAddress` (`harnesses/claude.go:262-277`); Codex → `<home>/auth.json` id_token email claim or `OPENAI_API_KEY` (`profiles.go:231-269`); OpenCode → keys of `<data>/opencode/auth.json` (`profiles.go:271-292`). Claude tokens live in `<cfg>/.credentials.json` (read by usage, §4).
- Per session: `sessions.options.profile` (`server/sessions.go:160-162,336`; read back by `sessionProfile`, `server/profiles.go:214-228`). Validated by `checkProfile` (`server/profiles.go:411-434`). Passed as `PlanRequest.Profile` (`harnesses/plan.go:78-80`) → `profileEnv` merged into the pane env (`claude.go:161`, `codex.go:106`, `opencode.go:188`, `shell.go:53`).
- **A session keeps its profile for life** (`README.md:186`); transcripts are per profile dir, so resume looks in that profile's dir (`claude.go:229-245`).

### 3.3 Anything like account switching

- **Automatic choice at start only**: `profile_auto` on a session/PreStart → `autoProfile` (`server/prestart.go:547-549, 557-585`): first a "companion" profile (an active session of the same harness in the same folder — commit `dfcc95f`: "joins the profile an active session of the same program in the same folder runs in, so the two can reach each other"), else `recommendProfiles` = most headroom (`server/headroom.go:126-168`): rate = 7-day left ÷ hours to reset, scaled by 5-hour left; accounts at a limit are skipped; Claude and Codex only (OpenCode/shell have no limits).
- No switching of a **running** session's account; no credential pool/rotation inside Socrates. The user's own `~/.codex-multi/*` and `~/.config/opencode-multi-auth` are external tools.

### 3.4 Live state: profiles exist in the DB, their directories do not

- kv `profiles` lists 5 profiles: `Team Sascha`, `Team André`, `Team Karin`, `Claude Campus`, `Campus`; 24 of 33 session rows reference one of them.
- `/var/lib/socrates/profiles/` does **not exist**, and `find / -name p-7c628310529c` finds nothing. **(inferred)** The profile dirs (all five accounts' logins and their transcripts) lived in the Docker volume (`/data`) and were not carried over by the Docker → host move (`d445348`), because only the DB rows came back from S3 — exactly the class of loss a "single folder backed up to S3" should prevent. The settings document also still carries the Docker-era `workspace.root = "/data/workspaces"`, and session workdirs `/data/workspaces/claude-…` (restored from the bucket); `/data/workspaces` now exists on the host with one new vscode workspace.
- Consequence today: resuming such a session finds no transcript → `cli_session_state = lost`, starts a **fresh** conversation (`internal/termux/ensure.go:176-205`), and `profileEnv` silently recreates empty profile dirs (`profiles.go:168-171`) → signed out.

---

## 4. Usage / limits / costs — data sources

All are per profile directory, i.e. per account:

| What | Source | Code |
| --- | --- | --- |
| Claude limits (5h/7d windows) | `GET https://api.anthropic.com/api/oauth/usage` with the access token from `<CLAUDE_CONFIG_DIR>/.credentials.json` (`claudeAiOauth.accessToken`), headers `anthropic-beta: oauth-2025-04-20`; cached 1 min **per config dir**; expired token → no reading, never refreshes/writes credentials | `server/usage.go:82-140`, URL `server/server.go:139` |
| Claude session cost | last `"type":"cost-state"` line in `<cfg>/projects/*/<cli_session_id>.jsonl` | `server/usage.go:183-204` |
| Codex limits + session cost | rate-limits/token records in the rollout `<CODEX_HOME>/sessions/**/…<id>….jsonl`; account limits = newest of up to 5 rollouts with data | `server/usage.go:206-300`, `server/headroom.go:170-210` |
| OpenCode session cost | live HTTP to the session's own OpenCode server `127.0.0.1:<port>/session/<id>` | `server/usage.go:303-337` |
| Cost ledger (`/costs` page) | background scanner every 2 min (first after 20 s) over **roots** = default dirs + every `<data>/profiles/*` on disk (deleted profiles still count): Claude `projects/**.jsonl` (incl. subagents), Codex `sessions/` + `archived_sessions/`, OpenCode `opencode.db`; results stored in `cost_convs/cost_hours` (with `profile` column) and mirrored to S3 so costs survive loss of transcripts | `server/costs.go:1-110`, `internal/costs/{scanner,claude,codex,opencode,prices}.go` |
| Sheet meters / recommendation | `fillLimits` over all profiles in parallel | `server/headroom.go:41-61`, `server/profiles.go:230-241` |

Implication for account switching **(inferred)**: limits and costs are keyed by *config directory*, not by account identity. If a rebuild moves accounts between sessions or makes them pooled, the readers need an account → dir mapping; moving a conversation to another account means moving/copying its transcript into the other account's dir for `--resume` to find it (`claude.go:241` globs only the session's own profile dir).

---

## 5. Everything stateful a "single local config folder, backed up to S3, restorable on a new server" must include

Socrates-owned (today in `<data>` / SQLite):
1. `socrates.db` content: settings (incl. OpenRouter key, Google TTS key, tunnel token, workspace presets, harness defaults), password hash, browser logins, push VAPID keys + subscriptions, unread marks, session rows (incl. `cli_session_id`, `tmux_name`, options/profile), PreStart drafts and `start_at` schedules, cost ledger, profile list/order. Back up with a consistent snapshot (`VACUUM INTO`, already used: `store.go:454-476`), not a raw copy of a WAL DB **(inferred)**.
2. `sessions/<id>/` — `plan.json` (OpenCode server password, port), `journal.raw` (pane history used for replay), `claude-settings.json`, `initial-prompt`. Journals are up to 64 MiB each ×2.
3. `vscode/`, `vscode-extensions/` (editor state per folder).
4. Backup target config + encryption passphrase (today `s3.json`); must stay outside or be the one thing typed in on restore.

Harness-owned (today in HOME, **not backed up at all**) — required for "all chats" and logins:
5. Claude: `~/.claude/` (`projects/` transcripts, `.credentials.json`, `history.jsonl`, `file-history/`, `plans`, `todos`, settings/skills/plugins/agents/commands/hooks) **and `~/.claude.json`** (global config: onboarding, oauthAccount, per-project trust, MCP servers). Note the path quirk: without `CLAUDE_CONFIG_DIR` the global file is `$HOME/.claude.json`, with it `$CLAUDE_CONFIG_DIR/.claude.json` (`claude.go:296-318`) — a rebuild that sets `CLAUDE_CONFIG_DIR` for every account (including default) puts everything in one folder.
6. Codex: `CODEX_HOME` (`auth.json`, `config.toml`, `sessions/`, `archived_sessions/`, `*.sqlite` state/thread DBs, `history.jsonl`). Large (1.5 GB here).
7. OpenCode: `$XDG_DATA_HOME/opencode` (`auth.json`, `opencode.db`, `storage/`); its config is in `~/.config/opencode` (XDG_CONFIG_HOME, not moved by profiles today).
8. Every profile's equivalents of 5-7 (today `<data>/profiles/<id>/…`; lost on this host).
9. Symlinks in profile dirs point at absolute default paths (`profiles.go:175-186`) — a restore to another HOME/data path breaks them unless relative or recreated **(inferred)**.

Not stateful / regenerable: `tmux.conf` (rewritten each start), sockets, `bin/cloudflared`, `agents_catalog`, `updates.*`, `update-state/`.

Outside any single folder today, worth deciding on:
- Workspaces: dynamic ones under `settings.workspace.root`; preset folders anywhere (`/root/<project>`), and uploads go to `<workdir>/.socrates/uploads`. Session rows store absolute workdirs, so a restore to a different layout leaves dangling paths (as seen with `/data/workspaces`).
- Running tmux sessions themselves cannot be restored; only rows + transcripts allow `--resume`.
- `/etc/socrates.env`, systemd units (installed from repo).
- Live DB is SQLite in WAL; the CLIs' own SQLite DBs (`opencode.db`, Codex `*.sqlite`) are written while sessions run, so a periodic file-level backup needs snapshot semantics or tolerance for torn copies **(inferred)**.
