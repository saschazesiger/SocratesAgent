# Rebuild handoff (final, 2026-10-24)

Read this first if you are taking Socrates over after the rebuild. It says what is done, how to
run and look after it, what was left out on purpose, what is known not to work, and what only the
product owner can still do before the merge into `main`. The plan the rebuild followed is
`REBUILD.md`; this file is where it ended.

## 1. Where it stands

The rebuild is done on branch **`integ`**, and everything the build container can run passes on
its last commit (§6). `integ` is, in order:

- `origin/rebuild` (26a3b56): WP0 to WP9 and the CI and flake fixes of §2.2;
- WP10 and WP11 (branch `wp10`, merged in 5d5a1bb), then the phone pass (branch `mobile`, merged
  in 1942e08), with WP11's scenarios brought onto the phone pass's UI;
- the fixes of the four final audits (a desk, a phone, a deploy rehearsal on a synthetic
  installation of `main`, the real binaries; `deployfix` merged in d95802d), two rounds of
  rechecks at a desk and on a phone with their fixes, and the fixes the last whole runs needed.

`integ` is pushed as `origin/rebuild` and merged into `main` through pull request #1, at the
product owner's instruction ("when everything is finished, push it to main yourself"): every push
to `main` is installed on the server by GitHub Actions, and the nightly update builds `origin/main`
too, so the merge is the deployment, and the migration of §3 runs on the server's first start of
this version (§6, item 5).

`integ`'s history from fa1ef20 on was rewritten before anything was pushed: that commit had put a
28 MB debug binary (`SocratesAgent`, what `go build` without `-o` writes in a clone named after
the repository; now in `.gitignore`) in the tree. The local branches `wp10` and `deployfix` still
hold the old commits; build on `integ`, not on them.

Binding documents, in order:

1. `REBUILD.md`: the plan and the product owner's decisions (§1).
2. `AGENT-HOST.md`: the agent host, its protocol, the chat event model, the adapters (§5), the
   browser API (§7) and the accounts (§11). Authoritative for everything that touches a chat, and
   kept accurate for the final code.
3. `DEVIATIONS.md`, sections "Rebuild" and "Phone pass": every accepted deviation from REBUILD.md,
   per package.
4. `research/01..09-*.md`: the verified facts behind the plan (the protocols of claude 2.1.309,
   codex 0.164.0, opencode 1.19.4; the spikes).
5. DECISIONS.md, DESIGN.md, HOME.md, ACTIVITY.md, HARNESS-POLICY.md hold where REBUILD.md did not
   supersede them; where it did, they say so at the place (pointers, not rewrites).
   DASHBOARD-STYLE.md is binding for all UI.
6. `e2e/README.md`: the suite, and what each scenario proves.

## 2. What is done

### 2.1 The work packages of REBUILD §10, all of them

- **WP0, the spikes**: S1 (a Claude conversation moves between two real accounts, research/08),
  S3 (one `opencode serve` per chat on a shared database, auth per account, research/06), S4 (a
  chat's host in a systemd scope outlives `systemctl restart socrates`, research/07). S2 (Codex
  across ChatGPT accounts) was waived (REBUILD §4.3, §12).
- **Chats for the agents** (WP2 to WP5, WP8): Claude Code (`claude -p`, stream-json and its
  control protocol), Codex (`codex app-server`, JSON-RPC) and OpenCode (`opencode serve`, HTTP and
  its event stream) each run in an agent host of their own (`socrates agent-host`, one per active
  chat, in its own systemd scope) that owns the CLI and the chat's journal
  (`chats/<id>/events.jsonl`). The server and the page only read the journal: a server restart, a
  phone that loses its network and a reload all replay it from the last event they had, and
  nothing is sent twice (the browser's outbox, one storage key per item, and `client_id`s). A host
  that dies is noticed, its turn closed as interrupted, and the conversation resumed by the next
  host. The chat view streams Markdown, reasoning, tool rows, diffs, subagents and workflows, the
  task list, the plan card, attachments, a slash palette and the model and account pickers.
- **ask_user** (WP6): one tool for all three agents, which always waits for the answer. Its card
  stands where the agent asked, inside the answer, with what the agent said afterwards under it;
  the dialog is the whole screen on a phone and in a car. Native question tools are off; what
  still arrives (Codex's plan mode, elicitations, OpenCode's question tool) is converted to the
  same questions. Permissions are always allowed.
- **Accounts** (WP7): subscriptions and API keys per agent, signed in from Admin → Accounts, the
  recommended one picked by headroom, switched in a running chat, and an automatic switch at a
  limit (off by default). Every account of an agent shares its conversations. After a turn ends at
  its limit, Codex and OpenCode hold what the journal has from before that end, in the journal's
  order, until a switch or the person's next message (AGENT-HOST §5.2, §5.3; §2.2 item 6 here).
- **Everything else reads the journal** (WP9): activity and unread, Home's line and question, the
  push and the badge, titles, spoken Status, the verdict on a turn, the cost ledger, PreStart.
- **The Shell is the only terminal** (WP10), plus a folder's VS Code; the answer operator, the
  screen digest, the screen-reading activity layers and the profiles are gone. The migration of
  REBUILD §8 runs once on a data folder of the build before: the agents' folders become the
  Default accounts, the profiles become accounts, the old agent terminals are cut cleanly, and the
  data folder (without its working directories) and the agents' folders are written to
  `<data>.pre-rebuild.tar` first.
- **One data folder, backed up to S3** (WP1): an encrypted, incremental, deduplicated backup that
  nothing reads while Socrates runs, and a restore from the setup page or `socrates restore`.
- **Tests and docs** (WP11): the e2e suite runs on the protocol fakes (`e2e/fakebin`:
  `fakeclaude`, `fakecodex`, `fakeopencode`, and a fake S3), 91 scenarios; the agents' terminals'
  scenarios moved onto the chats (three were dropped, §4), and `chat-restart` is new. The
  README's pictures come from `node e2e/shots.mjs` with the fakes; taken again on the final UI,
  they show nothing that differs from d76ceac's.

### 2.2 The CI and flake fixes, and their causes

Each was reproduced, its cause measured, and fixed where the cause was; the commits carry the
numbers.

1. **Claude Code logouts** (f4c2a5d): a server test wrote a fake login into the machine's real
   `~/.claude`, so every test run logged out every Claude Code on the machine.
   `internal/testhome` now isolates HOME, the agents' folders and variables, their keys and the
   bucket settings for every test package.
2. **macOS**: socket paths under `/var/folders` were longer than a Unix socket allows (TMPDIR is
   `/tmp` then, 16b31da); macOS's `/bin/sh` is a shim that execs bash and tmux names a pane by its
   executable there, so a Shell was never idle (cf9e718); tmux prints a signal by name with a BSD
   libc, so a SIGKILL read as status -1 (e1d0745); tests used GNU `timeout` and `base64 -w`, and
   counted echoes that bash's readline redraws (1d51cdb, d50886f); `internal/server` ran nine and
   a half minutes against go test's ten (CI gives each package twenty, c596187).
3. **Windows** no longer compiled (`internal/proc`, `internal/termux`, the data folder's lock):
   build tags and Windows variants (95b2b8f).
4. **tmux 3.4's lost exit status**: tmux up to 3.5 built with libutempter sets SIGCHLD to SIG_DFL
   while its helper runs; a pane's SIGCHLD in that moment is lost, the program is never reaped and
   `pane-died` never fires. Socrates sends the tmux server a SIGCHLD (`collectExits`, 83e82da);
   the poll leaves a dead pane alone until tmux has its status (1a388e4); a launch no longer
   writes "running" over the exit its hook recorded (3f5ffb9).
5. **tmux 3.8's DECRQM**: tmux 3.8 asks an attaching client for the synchronized-output mode
   (`CSI ? 2026 $ p`); the question reached xterm.js, whose reply could be typed into the Shell.
   The responder answers mode questions itself and drops mode reports (ede0c82).
6. **The limit hold, by the journal's order** (Codex, OpenCode): the host journals a message and
   then hands it to the adapter; a turn that ended at its limit in between made the adapter take
   the message for the person going on, let the hold go, and run it into the same limit. Each
   message now carries the turn that had ended when it was journaled (`AfterTurn`), and only a
   message written after the limited end lets the hold go (3709ea9, e5fd94c, 008da0c); a new
   adapter on the still limited account is told the hold (a75b4fc); Codex keeps its queue in the
   journal's order (b2ba174), waits while codex starts a command's turn (d6c371e) and gives up one
   that never starts (393a9f3).
7. **OpenCode's repeated error**: OpenCode 1.19.4 says a failed turn's error again about 2 ms
   after `session.idle`, when the next queued turn has begun, which took it for its own failure.
   A repeat is now known by its words (e2652dd).
8. **The backup upload**: one failed chunk cancelled the uploads under way; chunks already in the
   bucket were not indexed and went up again. Uploads under way now finish (29f7bd3).
9. **The agent host's start**: a command that arrived before boot took the command lock was
   refused "the agent is not running" and never sent again (3ecdbd5); two activity reports could
   reach the detector out of order (3bcd4ec).
10. **Races in the tests themselves**: an ack that read the turn's end first (704169a), the killed
    host's child read from the hello (cf26c14), a send between "parked" and the host's exit
    (6210bd0), a keystroke check before the first prompt and unlocked reads of a stream (02c2ee7),
    the stalled viewer's floods (1255e27, 4de42ba); in the e2e harness a second tap on a drawer
    still sliding in (591e9a8), the ports 5060 and 5061 that fetch refuses (1341eb5), two suites
    on one machine taking one port (6de6381), an answer read between two looks at the chat
    (935175e).
11. **The last whole runs on `integ`**: the 2 s poll marked a Shell still being created as lost
    when it ran before the first session had started the tmux server (66423de); tmux 3.4 itself
    drops the output of a program that prints and exits at birth on a busy CPU (the test waits for
    its line, 0ddc4a5; §5); closing the manager left an accepted hook writing to the closed
    database (6d3c727); the chats' outbox was one list under one storage key, and a tab with a
    stale copy wrote another tab's message away (4a91ec1).

### 2.3 The phone pass and the final audits

- **The phone pass** (branch `mobile`, on `wp10`): an audit at 390, 360, 430 and on its side,
  touch mode only, found 48 things, and two more audits after the fixes found fewer each time. Icons
  rather than text buttons, with the glyphs Dashboard-Style 1.6.2 lacks taken from Lucide (`ui.js`
  `LUCIDE`, DASHBOARD-STYLE.md); a tap on a field types at once and the keyboard switch is the
  terminal's alone (`keyboard.js`); a session's menu as an action sheet on a phone; the question
  dialog as the whole screen with one footer row; Stop and Send one 48px slot; snackbars placed
  above the dock, the key bar and a keyboard; "needs an OpenRouter key" (or a Text-to-Speech key)
  said before anything is recorded or sent; Info and the chat's "i" as labelled facts with model
  names, not ids. DEVIATIONS.md, "Phone pass", lists what departs from REBUILD §7.1.
- **The four final audits** (39 items) and **two rounds of rechecks** at 390×844, 844×390,
  1280×800 and 360: every item fixed or answered (§4), among them a question's card standing
  where it was asked with the reply under it, the composer on a phone on its side, Costs' one row
  per chat, a model field with nothing to list being a plain field, a field a button brings coming
  into sight with the focus, and the migration's two defects the deploy rehearsal found (95fff34,
  cec091f).

## 3. How to operate it

- **Install and update**: `scripts/install.sh` (README "Install"); everything updates itself
  nightly and after a boot (`socrates-update.timer`), and a new Socrates that does not answer its
  health check is rolled back. `scripts/deploy-runner.sh` installs every push to `main`.
- **First start after the merge**: the migration of §2.1 runs by itself. Check the log
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
- **Accounts**: Admin → Accounts. Claude Code signs in with `claude auth login` run by the server
  (the card shows its page and takes the code), Codex with a device code, OpenCode in its own TUI in a Shell the card opens, any of them with an API key.
- **Backup and restore**: Admin → Backup; README "Storage" for the restore on a new machine.
- **Tests**: `make check` (gofmt, vet, `go mod tidy -diff`, `go test -race`, build);
  `node e2e/run.mjs` (the browser suite, about twenty minutes on four CPUs), `node e2e/chats.mjs`
  and `node e2e/asks.mjs`; `node e2e/shots.mjs` takes the README's pictures. Against real
  binaries, each gated and cheap: `SOCRATES_CLAUDE_LIVE=1` (Claude's adapter and ask_user),
  `SOCRATES_REAL_CODEX=1` (the real codex against the scripted model), `SOCRATES_OPENCODE_REAL=1`
  (a free OpenCode model), `SOCRATES_LIVE_AGENTS=1` (e2e `livesession`, and the Admin pictures of
  `e2e/shots.mjs`). The Claude ones reach a real, paid model: run them only where `claude` is
  signed in to the test accounts.

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
- **A title's 96 px minimum on a phone**: a Shell at 360 px has 80 px for its name even with no
  status chip, so the floor would push a control off the edge; at 480 px and below the chip draws
  its glyph alone instead, and the name keeps the rest.
- **A toast over a dialog still keeps its strip** at the dialog's foot (`keepDialogRoom`), on
  purpose; what raised one over the question dialog without need is gone.
- **`docs/screenshot-tunnel.png`** is hand-made and kept as it is (DESIGN.md §G.2), although it
  shows the look from before Dashboard-Style; the README does not show it.
- **Not run in the build container**, by its rules: the real `claude` binary beyond `--version`
  (there it reaches a real, paid model on another account), `livesession`, and the migration on a
  copy of `/var/lib/socrates`. §6 says what stands in for them.

## 5. Known limits

- A chat folder lost while the server runs is not noticed until the next start, when it is rebuilt
  from the CLI's transcript as it is opened (AGENT-HOST §10). OpenCode keeps neither a failed
  turn's error nor its limit on the message, so a rebuilt OpenCode chat shows such a turn as
  interrupted or completed (AGENT-HOST §5.3).
- Claude's ask_user has only run against `fakeclaude`; `TestLiveAskUser`
  (`SOCRATES_CLAUDE_LIVE=1`) checks it against the real CLI and has not run yet (§6).
- Codex on a code-mode model (gpt-6-*) is told its questions were delivered and ends its turn; the
  answer is its next message (REBUILD §5.3). For the person it is the same.
- OpenCode needs about 400 MB of memory per chat that is awake; a chat at rest has no process.
- On tmux 3.4 a Shell whose program prints and exits at once can lose that last output when the
  CPU is busy: tmux itself drops it (bare tmux lost 2 of 120 such lines under load, 0 of 120 when
  the program exited after its line was on screen; 0ddc4a5).
- The headless Chromium of the e2e suite always denies notifications, so `notify` stubs them; a
  real notification needs the full `chrome`.
- Two timeouts under load were never explained: the e2e `options` (in WP11's runs) and
  `TestGlobalHooksFireForEverySession` (before the rebuild's CI work). Neither failed in any of
  the whole runs on `integ`. Every other flake was root-caused (§2.2).
- The README pictures are taken with the protocol fakes: the agents' words in them are scripted
  (`e2e/shots.mjs`).

## 6. Definition of done, item by item

1. **`go build`, `go vet` (Linux, Windows, macOS) and `go test -race ./...`: done** on `integ`'s
   last code commit, f6c2753 (the commits after it change only docs), with gofmt and
   `go mod tidy -diff` clean. `go test -race -count=1 -p 2 -timeout 30m ./...`: 35 packages, the
   28 with tests all pass (5 min 10 s); 758 top-level tests pass, none fails, and 23 skip, each
   for want of what this container must not or cannot give: the real Claude CLI (3), the real
   codex (12), a real OpenCode (4), a systemd scope, a real S3 bucket, and a tmux newer than 3.4
   (2).
2. **The whole e2e suite in one run, at a desk and on a phone, with `chats.mjs` and `asks.mjs`:
   done** on f6c2753, alone on the machine, the first run of each: `node e2e/run.mjs`, 91
   scenarios (90 run; `livesession` waits for `SOCRATES_LIVE_AGENTS=1`), 1458 assertions, all
   passed, in 20 min 34 s; `node e2e/chats.mjs` 12 of 12 checks; `node e2e/asks.mjs` 35 of 35.
   No server, tmux server, agent host or browser was left behind.
3. **Real runs.**
   - **Codex: done** in the build container on 2a696cb, the audited commit (the adapters and the
     agent host have not changed since): `SOCRATES_REAL_CODEX=1` with the real codex-cli 0.164.0
     against the scripted model (`fakemodel`, standing in for OpenAI): the whole codex package,
     47 tests with the installed codex's schema check, passed, and its 11 real-binary tests passed
     three times more. No ChatGPT login was used.
   - **OpenCode: done in parts**, on 2a696cb, on free models: every step of `TestRealOpenCode`
     passed, but never the whole test in one run, which needs ten to twelve calls on one model
     where a free tier allowed about eight per model in a few minutes. A real OpenCode session
     rebuilt from its storage showed the limit in §5. **For the owner**: run it whole with a
     provider key in `SOCRATES_OPENCODE_REAL_AUTH`, or on a model with more quota.
   - **Claude: not done, and not possible in this build container.** The spike accounts
     (`/root/socrates-spike/claude-a` and `claude-b`) do not exist here, and the real `claude`
     here would reach a paid model on another account. **For the owner, on the machine with the
     test accounts** (never log them out, never copy their credentials, keep spend on haiku): a
     chat, a subagent, a workflow surviving a restart of a test server instance, an account
     switch A → B → A, `SOCRATES_CLAUDE_LIVE=1 go test ./internal/agent/claude/`, and
     `SOCRATES_LIVE_AGENTS=1 node e2e/run.mjs livesession`. Or accept the gap: Claude's adapter
     and ask_user are tested against `fakeclaude`, which speaks the protocol research/03 and
     research/09 measured on claude 2.1.309, and S1 moved a real conversation between the two
     accounts (research/08).
4. **The migration on a copy of `/var/lib/socrates`: not done, and not possible in this build
   container**, which has no installation of Socrates. It was rehearsed instead on synthetic old
   installations made by today's `main` build: a password, a profile with its own Claude login,
   two Shells with a process running in each, four agent terminals, a prepared session, the
   mirror's `s3.json` on a fake S3, the agents' folders in a sandboxed HOME, and the fake CLIs.
   The rehearsal went through the deploy path itself - the old build's `deploy.sh` installing this
   one, sandboxed - for a healthy deploy, a deploy that migrated and then failed its health check
   (rolled back by the old updater, then deployed again) and the nightly update: the Shells
   survived, the agent terminals were cut, the logins stayed reachable through the links, and the
   first backup took the mirror's bucket under `backup/v1/`. It found the two defects fixed in
   95fff34 and cec091f and the updater's in 920fb85. The Go suite rehearses the migration on such
   a fixture too, whole and killed half-way (`rehearsal_test.go`). **For the owner**: run it on a
   copy before the merge - the data folder and the agents' folders copied, the copy started with
   a `-data`, HOME and port of its own, never the original and never the service's tmux server;
   its `s3.json` names the real bucket, so give the copy a bucket or prefix of its own unless that
   first backup is the point - or accept the rehearsal.
5. **The merge into `main`: done**, through pull request #1, once CI was green on Linux, macOS and
   Windows, at the product owner's instruction to push to `main` when everything is finished. Items
   3 (Claude) and 4 (a copy of `/var/lib/socrates`) could not be done in the build container and
   stand as described above: the rehearsals of item 4 and the fake-protocol tests of item 3 are
   what they rest on. The push deploys it; the migration is one-way, and "A rollback across the
   migration" (§3) is the way back.

## 7. Working method that was used

One orchestrator, and workflows of implementer agents: each work package, merge, audit fix or
flake to one implementer in its own git worktree, never two whole suites at once on the machine's
four CPUs. The reviews were independent: Opus reviewers who had not written the code reviewed the
packages, the merges and the fixes, and the desk and phone audits were rechecked twice after their
fixes by fresh auditors. No Fable subagents were used for the rebuild, at the owner's request
(REBUILD §10 had planned a Fable review per package). Every flake was root-caused before it was
called fixed - reproduced under load, the cause measured, the fix shown to turn the failures into
passes - and its commit says what was measured (§2.2). An assertion prints what it measured beside
its verdict, so a failure says what it saw.

Implementers commit early and often (sessions were cut off by authentication outages), never
push, and keep to these rules: never touch the running `socrates.service`, `/var/lib/socrates`,
the user's tmux server or any real agent folder (`~/.claude`, `~/.claude.json`, `~/.codex`, the
OpenCode folders); never run the real `claude` beyond `--version` or `--help`, and never press a
Claude sign-in button in a test browser (it runs `claude auth login` in a login shell, which finds
the real `claude`); every test through `internal/testhome`, every e2e run with the fakes; English
in everything shipped; gofmt, vet and race tests green.

Environment facts worth keeping: the orchestrating Claude Code session kept being logged out when
it shared `~/.claude/.credentials.json` with other Claude processes, so run it with a
`CLAUDE_CONFIG_DIR` of its own; the five profiles' folders on the production host were lost in the
Docker → systemd move (only their rows survived), and the migration makes them accounts that say
"Sign in again"; the real `~/.config/opencode/opencode.jsonc` there holds a plaintext Exa key that
must not be copied anywhere.
