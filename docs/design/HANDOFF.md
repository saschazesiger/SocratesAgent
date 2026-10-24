# Rebuild handoff (final, 2026-10-24)

Read this first if you are taking Socrates over after the rebuild. It says what is done, how to
run and look after it, what was left out on purpose, and what is known not to work. The plan the
rebuild followed is `REBUILD.md`; this file is where it ended.

## 1. Where it stands

The rebuild is done on branch **`wp10`**: the integration of WP1–WP9 with WP10 (the clean-up and
the migration) and WP11 (the e2e suite and the docs) on top. It is **not merged into `main`**:
every push to `main` is installed on the server by GitHub Actions, and the nightly update builds
`origin/main` too, so the merge is the deployment (§6).

At the time of writing `origin/rebuild` carries CI and test fixes made beside this branch (test
flakes on macOS and under load, a backup upload that no longer cuts the others short); seven of
them are on `wp10` too, cherry-picked: three test fixes the final race run needed (the stream
reads under the lock, the killed host's child, the ack that reads the turn's end first), and from
the review pass the lost exit status of a Shell (a pane killed by a signal read by name, tmux
made to collect a status it lost, the poll that waits for it) and the backup upload. Merge the
rest with `wp10` before the final run of §6.

Binding documents, in order:

1. `REBUILD.md`: the plan and the product owner's decisions (§1).
2. `AGENT-HOST.md`: the agent host, its protocol, the chat event model, the adapters (§5), the
   browser API (§7) and the accounts (§11). Authoritative for everything that touches a chat, and
   kept accurate for the final code.
3. `DEVIATIONS.md`, section "Rebuild": every accepted deviation from REBUILD.md, per package.
4. `research/01..09-*.md`: the verified facts behind the plan (the protocols of claude 2.1.309,
   codex 0.164.0, opencode 1.19.4; the spikes).
5. DECISIONS.md, DESIGN.md, HOME.md, ACTIVITY.md, HARNESS-POLICY.md hold where REBUILD.md did not
   supersede them; where it did, they say so at the place (pointers, not rewrites).
   DASHBOARD-STYLE.md is binding for all UI.
6. `e2e/README.md`: the suite, and what each scenario proves.

## 2. What is done

- **Chats for the agents** (WP2–WP5, WP8): Claude Code (`claude -p`, stream-json and its control
  protocol), Codex (`codex app-server`, JSON-RPC) and OpenCode (`opencode serve`, HTTP and its
  event stream) each run in an agent host of their own (`socrates agent-host`, one per active chat,
  in its own systemd scope) that owns the CLI and the chat's journal (`chats/<id>/events.jsonl`).
  The server and the page only read the journal: a server restart, a phone that loses its network
  and a reload all replay it from the last event they had, and nothing is sent twice (the
  browser's outbox, `client_id`s). A host that dies is noticed, its turn closed as interrupted, and
  the conversation resumed by the next host. The chat view streams Markdown, reasoning, tool rows,
  diffs, subagents and workflows, the task list, the plan card, attachments, a slash palette and
  the model and account pickers.
- **ask_user** (WP6): one tool for all three agents, which always waits for the answer; the inline
  card, and the dialog that is the whole screen on a phone and in a car. Native question tools are
  off; what still arrives (Codex's plan mode, elicitations, OpenCode's question tool) is converted
  to the same questions. Permissions are always allowed.
- **Accounts** (WP7): subscriptions and API keys per agent, signed in from Admin → Accounts, the
  recommended one picked by headroom, switched in a running chat, and an automatic switch at a
  limit (off by default). Every account of an agent shares its conversations.
- **Everything else reads the journal** (WP9): activity and unread, Home's line and question, the
  push and the badge, titles, spoken Status, the verdict on a turn, the cost ledger, PreStart.
- **The Shell is the only terminal** (WP10), plus a folder's VS Code; the answer operator, the
  screen digest, the screen-reading activity layers and the profiles are gone. The migration of
  REBUILD §8 runs once on a data folder of the build before: the agents' folders become the
  Default accounts, the profiles become accounts, the old agent terminals are cut cleanly, and
  everything is written to `<data>.pre-rebuild.tar` first.
- **One data folder, backed up to S3** (WP1): an encrypted, incremental, deduplicated backup that
  nothing reads while Socrates runs, and a restore from the setup page or `socrates restore`.
- **Tests and docs** (WP11): every e2e scenario of the agents' terminals moved onto the chats
  against the protocol fakes (`e2e/README.md`), a new `chat-restart`, the README with its
  pictures retaken, and this file.

## 3. How to operate it

- **Install and update**: `scripts/install.sh` (README "Install"); everything updates itself
  nightly and after a boot (`socrates-update.timer`), and a new Socrates that does not answer its
  health check is rolled back. `scripts/deploy-runner.sh` installs every push to `main`.
- **First start after the merge**: the migration of §2 runs by itself. Check the log
  (`journalctl -u socrates`) for each source it moved, and Admin → Accounts for the accounts it
  made; an account whose sign-in was lost says "Sign in again". The tar beside the data folder
  (`<data>.pre-rebuild.tar`) is the way back: its `data/` is the data folder as the build before
  had it (its database at that build's schema, `s3.json`, the agent terminals' journals) and its
  `home/` the agents' folders as they were; the working directories (`workspaces/` and any
  session's folder in the data folder) are not in it, since the migration does not touch them.
  To go back, stop Socrates, install the build before, move the data folder aside, remove the
  links the migration left in HOME (`~/.claude`, `~/.claude.json`, `~/.codex`,
  `~/.local/share/opencode`, `~/.config/opencode`), extract the tar into an empty folder and move
  its `data/` and `home/` entries into place, and move `workspaces/` (and any other working
  directory) back from the folder set aside. A login refreshed since the migration is newer in
  the moved folder than in the tar. Reload every page that was open across the update (a phone's
  home-screen app too): a page loaded from the build before keeps its interface - PreStart with
  a profile, agent terminals - which the new server refuses.
- **A rollback across the migration**: the first deploy of this build is run by the updater the
  build before installed (`/usr/local/lib/socrates`; the new scripts go in only once the new
  build is healthy), so if this build migrates and then fails its health check, that updater's
  rollback puts back the previous binary and database and nothing else. The build before then runs
  on what the migration left, and works (rehearsed): its database is whole, with its agent
  terminals' rows, and the agents' folders stay in the data folder, reached through the links in
  HOME and in the profiles - a login it refreshes is written there. What it does not get back: the
  agent terminals' tmux sessions, which the clean cut ended (they say "needs resume", and Resume
  starts each on its conversation again), and their scrollback journals, which are only in the
  tar. The next build's start migrates that database again and makes every account on the folder
  its link names (`movedBefore` in `internal/accounts/migrate.go`), so the accounts keep their
  logins; it neither rewrites the tar nor needs it. The tar is then from the first attempt: the
  newest database of the build before is `socrates.db.pre-v12.bak`, which every upgrade to schema
  12 takes anew.
- **What runs where**: `socrates.service` (the web server), `socrates-agents-<chat>.scope` (one
  chat's host and CLI, which outlive the service), `socrates-tmux*.scope` (the Shell terminals),
  `socrates-update.timer`. Data: `/var/lib/socrates` (README "What is where"; the layout is
  `internal/paths` and REBUILD §6.1).
- **A chat that misbehaves**: `chats/<id>/host.log` (the host's own log), `chats/<id>/events.jsonl`
  (the journal, one event a line), `chats/<id>/host.json` (how it was launched; keys are sealed).
  `GET /api/chats/<id>?history=1` is the projection the page draws.
- **Accounts**: Admin → Accounts. Claude Code signs in with `claude auth login` in a Shell the card
  opens, Codex with a device code, OpenCode through its provider, any of them with an API key.
- **Backup and restore**: Admin → Backup; README "Storage" for the restore on a new machine.
- **Tests**: `make check` (gofmt, vet, `go test -race`, build); `node e2e/run.mjs` (the browser
  suite, about twenty minutes on four CPUs), `node e2e/chats.mjs` and `node e2e/asks.mjs`.
  Against real binaries, each gated and cheap: `SOCRATES_CLAUDE_LIVE=1` (Claude's adapter and
  ask_user), `SOCRATES_REAL_CODEX=1` (the real codex against the scripted model),
  `SOCRATES_OPENCODE_REAL=1` (a free OpenCode model), `SOCRATES_LIVE_AGENTS=1` (e2e
  `livesession`, and the Admin pictures of `e2e/shots.mjs`).

## 4. Deliberately not done

- **No agent in a terminal**, and no screen read by a model: the product owner's decision
  (REBUILD §1). A Shell's screen is still read for Status.
- **No permission prompts**: every agent's permissions are allowed; plan mode's approval is the
  one question an agent asks without `ask_user`.
- **Working directories are not backed up**: they live in git. `~/.codex-multi` and
  `~/.config/opencode-multi-auth` are ignored.
- **S3 is not a store**: the mirror, the lease and the connect mode are gone; an old mirror's
  objects are offered for deletion once this machine has a snapshot.
- **The scenarios that only made sense for a terminal** were dropped, not ported:
  `tasks-opencode`, `home-unread`, `home-language`, and parts of others (`e2e/README.md`, "What did
  not come over to the chats"; DEVIATIONS WP11).
- **Not run in WP11**, by its rules: the real `claude` binary (in the build container it reaches a
  real, paid model), `livesession`, the migration on a copy of `/var/lib/socrates`, and the merge.
  §6 lists them.

## 5. Known limits

- A chat folder lost while the server runs is not noticed until the next start, when it is rebuilt
  from the CLI's transcript as it is opened (AGENT-HOST §10).
- Claude's ask_user has only run against `fakeclaude`; `TestLiveAskUser`
  (`SOCRATES_CLAUDE_LIVE=1`) checks it against the real CLI and has not run yet.
- Codex on a code-mode model (gpt-6-*) is told its questions were delivered and ends its turn; the
  answer is its next message (REBUILD §5.3). For the person it is the same.
- OpenCode needs about 400 MB of memory per chat that is awake; a chat at rest has no process.
- The headless Chromium of the e2e suite always denies notifications, so `notify` stubs them; a
  real notification needs the full `chrome`.
- Under load a few tests have timed out and passed alone: `TestTerminalSlowReaderIsGivenUpOn`,
  `TestGlobalHooksFireForEverySession`, and in WP11's runs the e2e `options` and `offlinerestart`.
  `offlinerestart` opens the phone's drawer through `ensureNav`, whose second tap on a drawer still
  sliding in was the cause found for `pages`' timeout (fixed in the review pass); for `options` no
  cause was found. Neither failed in the final runs. Two e2e suites on one machine claim different
  ports and tell their servers apart (`e2e/README.md`).
- The README pictures are taken with the protocol fakes: the agents' words in them are scripted
  (`e2e/shots.mjs`).

## 6. Definition of done, and what is left before `main`

1. `go build`, `go vet` (Linux, Windows, macOS) and `go test -race ./...` pass: done on `wp10`.
2. The whole e2e suite passes in one run, at a desk and on a phone, with `chats.mjs` and
   `asks.mjs`: done on `wp10` (WP11's report has the counts).
3. Real runs, cheap, on the machine with the test accounts (never the build container):
   - **Claude:** a chat, a subagent, a workflow surviving a restart of a test server instance, an
     account switch A → B → A (the spike accounts under `/root/socrates-spike/claude-a` and
     `claude-b`: never log them out, never copy their credentials, keep spend on haiku), and
     `SOCRATES_CLAUDE_LIVE=1`, `SOCRATES_LIVE_AGENTS=1 node e2e/run.mjs livesession`.
   - **OpenCode:** `SOCRATES_OPENCODE_REAL=1`, a chat on a free model.
   - **Codex:** `SOCRATES_REAL_CODEX=1` (no working ChatGPT login on that host; the real binary
     runs against the scripted model).
4. The migration rehearsed on a **copy** of `/var/lib/socrates` (never the original): REBUILD §8,
   the clean cut, the first backup into the existing bucket under `backup/v1/`.
5. Merge `origin/rebuild`'s fixes (§1), run 1 and 2 again, then merge into `main`. Tell the product
   owner first: the migration is one-way, and the next nightly update (or Admin → Updates) deploys
   it.

## 7. Working method that was used

One orchestrator; each work package to one implementer in its own git worktree, one review, one
round of fixes. Implementers commit early and often (sessions were cut off by authentication
outages), never push, and keep to these rules: never touch the running `socrates.service`,
`/var/lib/socrates`, the user's tmux server or any real agent folder (`~/.claude`,
`~/.claude.json`, `~/.codex`, the OpenCode folders); every test through `internal/testhome`, every
e2e run with the fakes; English in everything shipped; gofmt, vet and race tests green.

Environment facts worth keeping: the orchestrating Claude Code session kept being logged out when
it shared `~/.claude/.credentials.json` with other Claude processes, so run it with a
`CLAUDE_CONFIG_DIR` of its own; the five profiles' folders on the production host were lost in the
Docker → systemd move (only their rows survived), and the migration makes them accounts that say
"Sign in again"; the real `~/.config/opencode/opencode.jsonc` there holds a plaintext Exa key that
must not be copied anywhere.
