# Deviations from the design specification

Appended per work package: WP, spec section, what, why.

## WP1 — Store, config and the migration

**WP1 / §B.5, WP1 scope — the harness option catalogue is a second file in the same
package.** The scope names `internal/config/config.go`. The four option structs of §C.9–C.12,
their defaults and their normalisation live in `internal/config/harnesses.go` instead; the
document type, the kept sections and `Normalize` stay in `config.go`. One file of nearly
seven hundred lines would have buried the settings document in the catalogue, and a second
file in the same package changes no API.

**WP1 / §B.2 — the settings migration is spelled out here, because the section it points at
does not describe one.** §B.2 says "the `agents` sub-document of the settings JSON is migrated
in place (§C.7)", and §C.7 is the OpenCode launcher. Implemented the obvious reading, in
`store.migrateSettingsDocument`: the three entries under `agents` become three of the four
entries under `harnesses` (every field of the old entry - `enabled`, `binary`, `extra_args`,
`models` - is a field of the new one under the same name, so they move across whole), and
`agent.workspace_root` becomes `workspace.root`. Both old keys are then removed. The transform
works on the raw JSON, so a field this build has never heard of survives it.

**WP1 / §G.1, WP3 — `internal/catalog` was touched three lines early.** WP1 deletes
`config.AgentsSettings`/`config.AgentEntry`, which `internal/catalog` reads; the package's real
retarget belongs to WP3. To keep the tree building, `HarnessSettings` gained
`Entry(id) (Common, bool)` - the same shape `AgentsSettings.Entry` had - and the catalogue's two
call sites and one parameter type now name it. Nothing else in that package changed.

**WP1 / §G.1 — `ResumeAgents`/`DetachAgents` were removed rather than renamed.** §G.1 says they
become `AdoptSessions`/`DetachViewers`. Those need the tmux Manager, which is WP2, and there is
nothing to adopt in between; the methods and their calls in `main.go` are gone, and WP4 adds the
`Adopt` call the spec puts in its own scope.

**WP1 / §G.1 — `handleModels` and `GET /api/models` lived in `admin.go`, not `agents.go`.** Both
are deleted as the section requires, together with the OpenRouter model cache they owned.
`randomHex` and `stripControl` went with the deleted handlers, being dead once `agents.go` was
gone. `StripANSI` is kept: it has its own test and the version-banner probe of §C.13 needs it.

### WP1 follow-up (review findings)

**WP1 / §B.4 — `Store.Rev()` is monotonic across restarts, and how.** The spec only says every
write bumps it. The counter is seeded from the largest of wall-clock time, the newest
`updated_at`, and a mark reserved in `kv` under `rev_high_water`: `bump` reserves the next
thousand revisions before handing any of them out, so a crash can never have used a number the
next start would repeat. One extra statement per thousand writes buys it.

**WP1 / §C.10, §C.12 — save-time JSON validation is `config.Settings.Validate`.** It checks
`json.Valid` on `claude.settings_overrides`, `opencode.permission_json`, `opencode.config_content`
and `opencode.tui_config`, and the `key=value` shape of `codex.config_overrides` (which reaches
Codex under `--strict-config`). `PUT /api/settings` calls it after `Normalize` and answers 400.
The remaining fields the review flagged as only trimmed - `claude.setting_sources` (a closed
multi in §C.10) and `claude.autocompact` (`auto` or `100k`…`1M`) - are left to the WP8 admin card,
which owns the controls that produce them.

**WP1 / §C.12 — `opencode.mouse` defaults to on.** §C.12 states no default for it. On matches
the terminal's own `mouse` default, and a TUI whose mouse is off by default reads as broken.

**WP1 / §B.5, §C.10 — `claude.default_effort` goes through `NormalizeEffort`,** which §B.5
requires be kept verbatim and which therefore also admits `minimal` and `ultra` - two levels
§C.10 does not list for Claude. The WP3 launcher must not pass those to `--effort`; the closed
list belongs in the admin control.

## WP2 — The tmux substrate

**WP2 / §A.11 — `internal/termux` imports `internal/store`.** §A.11 says the package "knows
nothing about HTTP or the store", but §A.5 step 3 requires the row to be written before any tmux
command runs, §A.8 requires `Adopt` to move rows between states and to insert a recovered one,
and half of §H.1's required tests assert on `state` and `exit_status`. The two cannot both be
true. The Manager therefore holds a `*store.Store`; nothing else in the package touches it, and
the HTTP half of the sentence still holds. Passing a narrow interface instead was considered and
rejected: it would have had `store.Session` in every signature, so it would have bought the same
import back without buying any decoupling.

**WP2 / §A.11 — `adopt.go` and `hooks.go` sit beside `manager.go`.** The file list names
`manager.go` for "Create, Attach, Adopt, Ensure, Kill, Resize, Poll". Creating and attaching are
there; adoption with its poller, and the hook socket with the two hook bodies, are two files of
their own. One file of nine hundred lines would have buried the create path in the
reconciliation. No API changed, and `Ensure` is WP4's by its own scope.

**WP2 / §A.3 — the hook bodies carry no paths; they read two variables out of the tmux server's
environment.** The spec's form interpolates `<socratesBin>` and `<sock>` into the `run-shell`
string through `ShellQuote`. That is one level of quoting short: a hook body is parsed by tmux
when the hook fires and *then* by `/bin/sh`, so the shell-quoted path has to survive tmux's
parser first. Verified on tmux 3.6 (`scratchpad/hooklab`): with a data directory called
`a b/it's $w#d`, the spec's form fires the hook and the command does nothing - tmux expands the
`$` inside its double quotes and eats the backslashes - while
`run-shell -b '"$SOCRATES_BIN" tmux-hook --sock "$SOCRATES_HOOK_SOCK" … #{session_name}'`
delivers the right argv, and `run-shell` still expands the `#{}` formats inside a single-quoted
body. The two variables are inherited by the server from Socrates and refreshed with
`set-environment -g` in `Adopt`, so an upgrade that moved the binary is picked up. `pipe-pane`
keeps `ShellQuote` as specified: its command is passed to tmux as one argument and is only ever
parsed by the shell.

**WP2 / §A.3, §A.5 — the tmux server is started by an explicit `start-server`, not by the first
`new-session`.** The hooks are "set once, globally, at server start"; if the server is started by
the first session, a program that exits immediately dies before the hooks exist and its
`pane-died` is lost. `ensureServer` runs `tmux -f <conf> -S <sock> -u start-server`, sets the
hooks, and only then creates the session. The start-up guard covers both commands: on a refusal
it rewrites the minimal conf, restarts the server and retries the create once.
`TestBadConfFallsBack` poisons the conf with the real `set -g window-size manual` and passes.

**WP2 / §A.9 — the reboot case is decided by two failed polls, not by the missing socket.**
`kill-server` on tmux 3.6 leaves the socket file behind, and the socket lives in the data
directory, which survives a reboot. The absent-socket branch is kept because it is free, but the
operative rule is the two consecutive failures, and the tests exercise that one.

**WP2 / §A.5, §H.1 — `Create` returns the store row.** §A.5 writes
`Create(ctx, spec) (*Session, error)` without saying which `Session`; it returns `*store.Session`,
which is what the caller in WP4 needs. The Manager's own per-session state is unexported.

**WP2 / §H.1 — `TestCreateFailureMarksRowFailed` fails the create with a duplicate session name.**
The obvious way to make `new-session` fail, a working directory that does not exist, does not
fail on tmux 3.6: it starts the session in the home directory instead. A name that is already
taken is both a real failure mode (a create retried after a partial one) and a reliable one.

**WP2 / §A.3 — the fallback configuration keeps `exit-empty off` and
`destroy-unattached off`.** §A.3 lists four lines for the minimal conf. WP2's own deviation - an
explicit `start-server` before the first session - is what makes `exit-empty off` load-bearing:
a server started with no session exits at once without it, so the fallback would set its hooks on
a server that was already gone and every pane death for the rest of the process would be seen
only by the poll. `TestBadConfFallsBack` now asserts both hooks are in place after the fallback
and that a session created afterwards reports its exit status with nothing polling.

**WP2 / §A.3 — the hook bodies quote their expansions and carry `--signal`.** A pane killed by a
signal has an *empty* `#{pane_dead_status}` and a `#{pane_dead_signal}` instead (verified on 3.6),
so an unquoted expansion left `--status` without a value and the subcommand exited on its own
arguments - exactly when a program crashed. The status is `128+signal` in that case, the way a
shell reports it, in the hook and in the poll alike.

**WP2 / §F.1 — version detection is in `tmux.go`; `install.go` is not part of this package.**
WP2's file list does not name `install.go` and WP8's does. `ParseVersion`, `BinaryVersion`,
`MinMajor`/`MinMinor` = 3.3 and `Version.OK` live in `tmux.go` for `requireTmux` and the
Manager; the `/api/tmux` payload, the package-manager matrix and the SSE installer are left to
WP8 to build on them.

## WP3 — Harness launchers and the e2e fakes

**WP3 / §C.6, §H.1 — the Codex trust override is an inline table, not a dotted key.** The
spec (and the `TestCodexTrustLevelQuoting` it prescribes) require
`-c projects."<cwd>".trust_level="trusted"`. Verified against codex-cli 0.152.0: that form is
**rejected** whenever the path contains a dot - `codex exec --strict-config -c
'projects."/a.d".trust_level="trusted"'` answers ``unknown configuration field
`projects."/a.d"` `` - because the override parser splits on every `.` without respecting the
quotes. `projects."/a b".trust_level` is accepted, so it is the dot and not the space. This is
not an edge case: the default workspace root is `~/.socrates/workspaces`, so *every* dynamic
working directory carries a dot and *every* Codex launch would have failed at start-up.
Socrates therefore emits `-c projects={"<cwd>"={trust_level="trusted"}}`, which passes
`--strict-config` with dots and spaces in the path, and which was confirmed by eye in a tmux
pane: a Codex TUI started in a directory it had never seen opened with no trust picker. The
test asserts the new string, with the same space-and-dot path.

**WP3 / §C.13 — `codex debug models` has no `--json` flag; it already prints JSON.** The
subcommand's own help says "Render the raw model catalog as JSON", and `--json` is an
`unexpected argument`. The answer is `{"models":[{slug, display_name, description,
default_reasoning_level, supported_reasoning_levels:[{effort,…}], visibility, priority, …}]}`;
entries whose `visibility` is `hide` are dropped and the rest are ordered by `priority`, which
is the order Codex itself offers them in.

**WP3 / §C.13 — `opencode models --json` does not exist in 1.17.13 either.** It is still tried
first, exactly as the section says, and anything that is not JSON falls through to parsing the
plain listing, which is one `provider/model` per line. Consequently a model id in the catalogue
is now `provider/model` - the id `-m` takes - and not the old adapter's `provider|model`.

**WP3 / §C.0 — `DiscoverModels` cannot be nil on an interface, so the shell answers
`ErrNoModels`.** The spec's struct-of-functions form allowed a nil `Discover`; the interface it
also specifies does not. The catalogue treats that error as "there is no model step here"
rather than as a failure. For the same reason `Descriptor.Notes` is gone: the one sentence the
dashboard shows under each harness is presentation and now lives in `internal/catalog`.

**WP3 / §C.1.5 — the TERM value is computed twice.** `harnesses.defaultTerm` is a copy of
`termux.DefaultTerminal` (an `infocmp tmux-256color` probe behind a `sync.Once`). termux is
built on this package, so it cannot be imported back, and moving the function here would have
meant editing a WP2 file. The same applies to `sessionDir`, which mirrors `termux.SessionDir`.

**WP3 / §C.1.2 — where Claude Code keeps its theme, verified from the binary rather than by
running `/theme`.** 2.1.258 builds its global-configuration defaults as
`{numStartups:0, installMethod:undefined, autoUpdates:undefined, theme:"dark", …, diffTool:
"auto", autoConnectIde:false, …}` - the very keys `~/.claude.json` holds on this machine - and
the same object is the one it logs about as `~/.claude.json`. So the key is `theme`, the
shipped default is `dark`, and the pin is enabled. The file's path is **not** simply "inside
the config directory": the binary computes it as
`join(process.env.CLAUDE_CONFIG_DIR || homedir(), ".claude.json")`, so with the variable unset
it is a sibling of `~/.claude` and with it set it is a child of what it names.
`claudeGlobalConfigPath` does the same. It is read, changed by one key and written through a
temporary file in the same directory; a failure is logged nowhere and fails nothing, because
`COLORFGBG` is the lever that actually decides the palette.

**WP3 / acceptance (a) and (b) — not performed.** Both require starting a real Claude Code and
a real Codex session; the work package was commissioned with an explicit standing instruction
never to start a real paid CLI session, which overrides the checklist. What was done instead,
without a session: every flag this package emits was confirmed to exist in
`claude --help` (2.1.258), `codex --help` (0.152.0) and `opencode --help` (1.17.13), and the
whole generated Codex `-c` block was fed to `codex exec --strict-config` with a deliberate
`socrates_bogus_top=1` appended - the only thing it rejected was the deliberate typo, which
proves both that the generated overrides load and that `--strict-config` is doing its job.
The `AskUserQuestion` round-trip of (b) is untested and stays open.

**WP3 / §H.2 — `e2e/run.mjs` was touched, three lines.** Deleting the `SCRIPT` and
`LONG_SCRIPT` exports, which §H.2 requires, would otherwise make the file fail to load: an
ESM named import of a missing export is a hard error. The import and the two `script:`
arguments are gone; nothing else in that file, which WP6-WP10 own, was changed.

**WP3 / §H.2 — `s.stop()` sweeps `/api/sessions`, which WP4 has not built yet.** The sweep is
already best-effort (its failure is reported, not thrown), so the harness is correct as soon
as the endpoint exists and harmless until then. The leak assertion is the specified one: no
`soc_*` session and no tmux server on `<data>/tmux.sock`.

**WP3 / §H.2 — the OpenCode fake records its session on the first turn, like the Codex one.**
§H.2 does not say when. Doing it at start-up would have made the discoverer's "the user has
not typed anything yet" path - the one that leaves `cli_session_state='pending'` - unreachable
from the suite, and it is not what the real TUI does either.

### WP3 follow-up (review findings)

**WP3 / §C.7 — the OpenCode discoverer is bounded by the launch time, and both discoverers
take the ids other sessions already hold.** §C.7 step 2 says "take the newest entry whose
directory == cwd", which is only safe in a directory nothing has ever run in. `GET /session`
lists every session in the shared database, so in a preset or typed-in directory with any
history the first poll would have answered with a conversation the user never had in that
pane, and the next reboot would have opened it. `Discovery{Cwd, Since, Claimed}` is now passed
to `WatchRollout` and `DiscoverOpenCodeSession`: anything stamped more than five seconds
before the launch belongs to an earlier session, and an id another row of the same harness
already holds is skipped. The five seconds are for the two clocks - the stamp is written by
the CLI a moment after Socrates noted the launch, at a coarser resolution.

**WP3 / §C.6, §C.7 — the residual race: two sessions of one harness in one directory.**
Neither CLI offers a per-session handle. Codex has a candidate -
`CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates-<session id>` would land in
`session_meta.originator` and be exact - but whether the backend accepts an unknown originator
cannot be established without starting a real session, so it goes on the manual-check list
rather than into the code. OpenCode has no equivalent input at all. What is implemented is the
exclusion set, which prevents the *second* claim from duplicating the first; two sessions
whose first turns are both still pending can still swap ids, and the pair is only distinguished
once one of them is recorded. WP9a's `createcodex`/`createopencode` and the manual checks own
what is left.

**WP3 / §C.2 — a custom working directory is judged by what it resolves to, and `/proc`,
`/sys` and `/dev` are refused with everything under them.** §C.2 says the path "is rejected if
it resolves to" one of the blocked roots; comparing the cleaned string only would have let
`/tmp/innocent -> /etc` through, because `MkdirAll` follows the link. Both the path as written
and its symlink-resolved form are now checked - the written form still matters, because on a
merged-usr machine `/bin` resolves to `/usr/bin` and `/bin` is still not a workspace. The
three pseudo-filesystems are widened from the exact match the section lists to the whole tree:
nothing under any of them is a place to work.

**WP3 / §C.11 — the mandatory environment is applied after `extra_env`, not before.**
`CODEX_INTERNAL_ORIGINATOR_OVERRIDE`, `OPENCODE_SERVER_PASSWORD`, `OPENCODE_SERVER_USERNAME`,
`OPENCODE_TUI_CONFIG` and `OPENCODE_CONFIG_CONTENT` are the launch rather than a preference -
§C.11 lists the originator as "always applied, not user-visible" - and an `extra_env` entry
that overwrote one of them would disarm the session-id discovery or turn every discovery poll
into a 401, silently in the first case. The raw list can no longer reach them.

**WP3 / §C.5 — `--system-prompt-snapshot` is only passed when there is an appended prompt.**
WP1 defaults the setting to `off` and normalises `""` to `off`, so the launcher was flipping
Claude's own default (`on`, for the built-in prompt) for every session, at a prompt-cache cost
on every resume. The flag only decides anything when `append_system_prompt` is set - which is
how §C.5 frames it - so that is when it is emitted. The admin control keeps its two values;
WP1's normaliser is untouched.

**WP3 / §H.2 — the leak assertion checks for sessions, not for the absence of a server.**
§H.2 asks for "no `soc_*` tmux session and no tmux server on `<data>/tmux.sock`". The second
half is unattainable as the substrate is built: WP2 starts the server explicitly with
`exit-empty off`, precisely so that the global hooks exist before any pane can die, so a server
with zero sessions is the correct end state of a clean run and not a leak. `sessionsOn` answers
the same for "no server" and for "a server with no sessions", the assertion is on the session
list, and `kill-server` remains the backstop. If WP4 ever stops the server when the last
session is deleted, the stronger form becomes assertable and should be restored.

**WP3 / §C.13 — a shortlist saved under the old OpenCode adapter names models the new id
scheme rejects.** WP1's `migrateSettingsDocument` moves `agents.opencode.models[]` across
verbatim and this package changed the id from the old `provider|model` to `provider/model`,
which is what `-m` takes; a migrated entry therefore reaches the sheet as a "typed in" id that
OpenCode refuses. The translation belongs to WP1's migration (one `strings.Replace` on the
opencode entry's picks), not to the launcher, and is recorded here because WP3 is what made it
necessary.

## WP4 — Session HTTP API

**WP4 / §A.11, §C.8 — `Ensure`, `Restart` and the resume flow are `internal/termux/ensure.go`,
and `termux.Config` gained a `Settings` function.** The file list puts `Ensure` in `manager.go`;
it sits beside it instead, as `adopt.go` and `hooks.go` already do. The `Settings` field is not
cosmetic: a resume builds a *new* launch plan (§C.8 step 2), a plan is built from
`config.Settings`, and the Manager had no way to read them. It is a function rather than a value
so that a session relaunched an hour later is planned from the dashboard as it is then.

**WP4 / §D.9 — `POST /api/sessions/{id}/resume` was added beside `restart`.** §C.8 makes
`Ensure` "called whenever a viewer attaches", and the viewer is WP5's WebSocket, which does not
exist yet. Without this route a `needs_resume` session could not be opened at all, which is
exactly what the work package says must not wait for a UI. `restart` is the same path with the
state forced first, as the section describes; `resume` leaves a running session alone.

**WP4 / §C.8 — the `notice` control frame is not pushed.** There is no transport in WP4. The
`resumed` flag on the row, the `resume_fresh`/`resumed_from` fields beside it and
`POST /api/sessions/{id}/ack-resume` carry everything the frame needs, and WP5 builds it from
them. (The first version of this entry claimed the flag alone carried the same fact; it did not
— see the review follow-up below.)

**WP4 / §C.6, §C.7 — running the session-id discoverers is nobody's scope, so WP4 runs them.**
`WatchRollout` and `DiscoverOpenCodeSession` were built in WP3 and no work package says who
calls them. Nothing did, so `cli_session_state` would have stayed `pending` for ever for Codex
and OpenCode, and their half of `rebootresume` (WP9a) could never pass. `Manager.launch` now
starts one detached watcher per session that needs one, ended by the Manager's own context on
shutdown. `CODEX_HOME` is read from the plan's environment, then ours, then `~/.codex`: the
launcher's own helper for it is unexported and its package was under review.

**WP4 / WP4 checklist — a create whose tmux commands fail answers 201 with the failed row.**
The checklist asks for the failure to surface as a `failed` row with tmux's stderr "not as a 500
with no trace". A 500 would make the browser discard a session that exists, so the row is
returned with its `fail_reason`, together with an `error` field, and the list shows it with the
failed overlay and a Try again button.

**WP4 / §D.9 — `GET /api/harnesses` also carries the workspace rules and whether sessions can be
created at all.** The sheet needs the presets, the root, `allow_custom` and "tmux is missing" to
draw itself, and §D.9 leaves no other endpoint for them; a second round trip for four fields
would be one more thing to keep in step.

**WP4 / §B.6 — the journal download is capped at 16 MiB from the end.** The section says the
endpoint streams the current file and the rotated one before it, which is up to 128 MB through
one response and one `[]byte`. What "download scrollback" is for is the recent past, and the
whole file remains on disk for anyone who wants it.

## WP5 — The WebSocket transport

**WP5 / §D.1 — `cols`/`rows` are optional on the handshake, and fall back to the size the
session already wears.** The section calls them required. Refusing a socket for a missing
query parameter would mean a browser that reconnected before it had measured its terminal gets
no terminal at all, and there is a better answer available: the row's own size, which the store
keeps above zero because `SetSessionSize` refuses anything else. A value that *is* given is
still validated as 1–1000 and a nonsense one is a 400.

**WP5 / §D.2 — output sequence numbers are one-based, and `replay_from` is the number of bytes
the client already has.** §D.2 says the first byte after an attach is 1; the WP2 ring counts
from 0. The transport bridges the two rather than changing the ring: the frame header carries
`ringOffset + 1`, and `since` - the last byte the client rendered - is the ring offset the
writer resumes from. `hello.replay_from` echoes that number, so `0` means "you have nothing;
reset the terminal", exactly as the section requires.

**WP5 / §D.3, §D.8 — the grace and the watchdog periods are fields of the `Server` and the
hub, not constants.** They are ninety seconds, fifteen seconds and ten seconds in production,
set once in `New` from the constants beside them. A test that proved the grace and the ping
watchdog with those values would take two and a half minutes and nobody would run it; the tests
that do prove them set milliseconds on their own server instance before any socket exists.

**WP5 / §D.7 — the handshake ceiling is its own counter beside the password throttle, not the
password throttle itself.** §D.7 says to reuse `throttle(ip)`, which only knows about failed
logins; twenty handshakes a minute is a different question, so `allowHandshake(ip)` counts
them under the same mutex. Both are applied: an address being slowed down for guessing
passwords cannot open terminals either.

**WP5 / §D.2 — a `bye` closes the viewer's terminal instead of leaving it in the grace.** The
section calls `bye` a clean detach and does not say what happens to the tmux client. A tab that
said it was leaving is not coming back, and holding its client for ninety seconds would spend
one of the eight viewer slots on nobody.

**WP5 / §C.8 — the resume `notice` carries `resumed_from` and `cli`.** §C.8 specifies
`{kind, resumed_from, fresh}` and §D.2 shows `{kind, fresh, cli}`. The frame carries the union:
`fresh` is false only when the conversation id was verified and actually resumed, `resumed_from`
is that id, and `cli` is the harness. WP4's open item - "the `notice` control frame is not
pushed" - is closed by this.

**WP5 / §D.5 — a control frame that cannot be queued closes the socket rather than blocking.**
The section bounds the *output* direction only. Control frames are broadcast by the Manager's
own goroutines (a pane that died, another viewer's resize), so a viewer that has stopped
reading must not be able to hold one of them up: thirty-two queued frames is the point at which
the connection is closed with 1013 and left to its grace.

### WP4 follow-up (review findings)

**WP4 / §C.8, §B.4 — what a resume did is a key/value note, not a column.** Finding 4 is right
that `resumed:true, resume_count:1` is byte for byte the same after a resume that continued a
conversation and after one that had to start a new one, and §C.8 requires the banner to tell
them apart. The outcome is recorded under `resume_note:<id>` in `kv` - `{fresh, resumed_from,
at}` - rather than as two more columns on `sessions`: it is unread-notice state with exactly
the life of the `resumed` flag, it is cleared by the same acknowledgement and deleted with the
session, and a column would mean a schema change in WP1's file and a migration for a fact that
is only interesting between a resume and the next glance at the screen.

**WP4 / §D.9 — the session JSON carries three fields the store row does not serialise.**
`sessionView` wraps `store.Session` with `cli_session_state` (the session list's technical
detail shows whether a conversation is `verified`, `pending` or `lost`) and, after a resume,
`resume_fresh` and `resumed_from`. The row's own tags stay as WP1 wrote them; the envelope is
the API's, and the whole set is documented at the top of `sessions.go`.

**WP4 / §C.5, §C.6 — "could not tell" no longer collapses to "gone".** The first cut treated
every `VerifyCLISession` error as a lost conversation, which contradicted §C.5 and §C.6 and the
`Harness` interface's own contract, and made a missing `CODEX_HOME` in Socrates' environment
permanently discard a stored id. A verification that errors now keeps the id and attempts the
resume; only a provable absence (`false, nil`) marks it `lost` and starts fresh. OpenCode still
degrades to fresh, because its own launcher answers `false` rather than an error for a question
it cannot settle - which is where that policy belongs.

**WP4 / §A.10, §C.8 — a refusal to replace a live session is `ErrStillRunning`, and the row is
never touched.** `Relaunch` used to record every failure, including that refusal, as
`state='failed'`, and `resume` swallowed the error - so a Restart pressed on a working terminal
answered 200 with a failed row that nothing polls out of again. The refusal is now a typed
error, `Restart` asks tmux before it changes any state, `resume` propagates what `Relaunch`
returns, and the handler answers 409.

**WP4 / §C.8 — everything that relaunches one session is serialised per session.** Two viewers
opening the same rebooted session at once both planned a launch, and the second `new-session`
failed as a duplicate and marked the row failed. `Ensure` and `Restart` now take a per-session
lock and re-read the row inside it, so the second viewer is answered with the first's result:
one relaunch, one `resume_count`.

**WP4 / §A.8 — `Adopt` re-arms the session-id watchers, and `Delete` cancels them.** Taking
ownership of the discoverers (above) was only half done: the watcher was started by `launch`
alone, so a Codex or OpenCode session that outlived a Socrates restart stayed `pending` for
ever and could never be resumed - the durability case the product exists for. `Adopt` now
starts one for every adopted row still `pending`, with `since` taken from `created_at`, reading
the launch back from `plan.json`; OpenCode's server password is recovered from the same file,
which is the only copy that survives the process that generated it and the reason the file is
0600. A deleted session's watcher is cancelled rather than left to write to a row that is gone.

**WP4 / §A.5, §D.9 — client-supplied sizes are bounded, and a losing idempotent create takes
its directory back.** `cols`/`rows` outside 10-1000 are a 400 before any row or directory
exists (tmux refuses the window otherwise, and the client's mistake became a failed session and
an orphaned workspace directory). When `Create` finds the row a concurrent identical request
already made, the empty dynamic directory this one created on the way in is removed and the
answer is 200 rather than 201.

**WP4 / §A.9 — a `starting` row over a live pane is promoted by the poll.** If the last write
of a create does not land, the pane is alive and the row is behind; the poll now says so, and
`Ensure`'s ten-second grace asks tmux before it writes `failed` over a working terminal.

## WP6 — Frontend shell: page, terminal, transport

**WP6 / §D.3, §D.4 — a returning viewer that sends `since=0` gets a fresh attach, not the
whole ring.** `replayPoint` answered `since=0` by replaying the ring from its base, which is
the ordinary page reload: the tab keeps its viewer id, its terminal is empty, and it asks for
everything. That was wrong twice over. It replays the *whole session* rather than the current
screen — the thing §A.6 and §D.3 exist to avoid — and it re-delivers the device-attribute
queries tmux wrote on the first attach, which xterm.js answers a second time into a pane that
never asked; the `reloadkeepsscreen` scenario caught it as `1;2c0;276;0c;24;80t432;672t`
echoed by the shell, followed by a terminal that no longer took input. `since=0` from a viewer
that is not brand new now takes the same path as a `since` the ring can no longer serve: the
tmux client is replaced, the redraw is the screen, and `hello` says `replay_from: 0`. The
first connect is unaffected — it passes `fresh` and its own attach is its redraw. This is a
four-line change in WP5's `ws.go`.

**WP6 / §E.4 — the palette ships as specified; what the `design` scenario can assert about it
does not.** §E.4 states that every colour in `LIGHT_THEME` is ≥ 4.5:1 against `#ffffff`.
Measured, eleven of its eighteen colours are not: `white` is 1.36:1, `brightGreen` 2.73:1,
`brightYellow` 2.63:1, `yellow` 3.39:1, and so on — and a palette that did satisfy it would
have no bright colours worth the name, because a 4.5:1 yellow on white is brown. The values
are shipped exactly as §E.4 writes them, because they are a deliberate set and changing them
is changing the design. What is actually load-bearing is `minimumContrastRatio: 4.5`, which
re-derives a colour at draw time, and that is what `createshell` measures: it prints a line in
ANSI white through the real pane and reads the colour the renderer actually drew
(`rgb(116,116,117)`, 4.67:1). `contrast(a, b)` is exported from `term.js` as §E.4 requires.
WP10's `design` scenario should assert the drawn colour, not the table.

**WP6 / §E.9 — `keybar.js` is not yet in the service worker's `SHELL`.** The list is exactly
the files this build ships. A precached path that does not exist would make `hasWholeShell()`
permanently false, which stops the worker ever letting go of the previous build — the opposite
of what the list is for. WP7 adds the file and the line together.

**WP6 / §D.9, §E.4 — `GET /api/preferences` carries the terminal settings.** §B.5 has
`terminal.scrollback`, `terminal.font_size` and `terminal.webgl`, §E.4 spends them, and no
endpoint handed them to the page: `/api/settings` is the dashboard's and needs the whole
document. §D.9 lists `/api/preferences` as "kept, updated content", so the three fields were
added there. `session.js` reads them before it builds the terminal and falls back to the
shipped defaults if the call fails.

**WP6 / §G.1 — `models.js` was deleted and `admin.js` was touched, three hunks.** §G.1 says
`models.js` is "folded into harnesses.js", but the two have nothing to do with each other:
`models.js` is the *OpenRouter* catalogue behind the dashboard's two voice-model fields, and
it read `GET /api/models`, which WP4 removed. It was dead. Deleting it would have left
`admin.js` importing a missing module, which is a hard ESM error and would have failed the
`pages` scenario on `/admin` — so `admin.js` now imports `harnesses.js` in place of
`agents.js` and its two model comboboxes are searchable fields with no list until WP8 rebuilds
that card. Nothing else in `admin.js` was changed.

**WP6 / §E.2, §E.7 — the overlays and notices are built here rather than in WP7.** §E.2 puts
`#termOverlay` and `#termNotice` in the page and WP7's scope names the overlays. A terminal
whose pane has exited and says nothing at all is not a shippable first cut of the terminal, and
the "resumed after a restart" notice is the only thing WP4's `resumed` flag and WP5's `notice`
frame were built to reach. Both are implemented and covered by `exitoverlay`. WP7 keeps the
`.stale` treatment, the draft persistence and the `viewer_fresh` toast, which are the parts
that need the key bar and the line input.

**WP6 / §E.1, §H.3 — `run.mjs` is the skeleton plus seven scenarios, and three of them are not
in §H.3's table.** Scenarios 1-4 (`createshell`, `typeandsee`, `reloadkeepsscreen`, `pages`)
are the ones WP6 owes. `harnesses` (all four session types started through the sheet, each
showing the fake TUI's banner), `sessionlist` (rename, archive, unarchive, delete, and the
working directory surviving) and `exitoverlay` (`/exit 7`, the status behind the "i", Restart)
cover the rest of what this package builds and would otherwise ship unmeasured until WP9.
`webglrenders` exists because every other scenario turns the WebGL renderer off in order to
read the pane out of the DOM, and the shipped default must not be the untested path.
`liveclaude` became `livesession` as §H.3 says.

**WP6 / §H.2 — `e2e/harness.mjs` was touched, two selectors.** `setup()` waited for `#newChat`
and `ensureNav()` names the drawer it opens; both now say `#newSession`. The boot, assert and
leak machinery is unchanged.

## WP8 — Admin

**WP8 / §F.1 — the install stream sends unnamed SSE frames with a `type` field, not `line` and
`done` events.** §F.1 asks for "one `line` event per output line and a final `done` event" and
in the same breath says the page consumes it with the existing `LiveStream` from `net.js`.
Those two cannot both be true: `LiveStream` reads `source.onmessage`, which an `EventSource`
only fires for frames with no `event:` name. Naming the events would have meant a second
consumer written by hand, with its own backoff and its own watchdog, which is precisely what
§F.1 rejects. The frames are therefore `{"type":"line","line":…}` and
`{"type":"done","exit":N,"ok":bool}`, plus the `{"type":"ping"}` heartbeat `LiveStream`
measures its watchdog against - the same shape every other stream in this app uses.

**WP8 / §F.1 — the install is offered for a tmux that is too old as well as one that is
missing.** The detection in §F.1 goes looking for a package manager only "if missing". The
command it would run is the same command that upgrades 3.2a to 3.6a, and a machine whose tmux
is too old is the case the checklist is most worried about, so refusing to offer it there would
leave the one user who needs the button without it. `ok` is still the only field that decides
anything, and 3.2a is still amber and never green.

**WP8 / §F.7 — the diagnostics list keeps the "Text to speech" row §F.7 does not name.** Every
bullet in §F.7 is implemented; this one row is inherited from the dashboard as it was. The voice
card has no check of its own, dictation is a DECISIONS.md requirement, and dropping the row
would have been a regression in what the page can answer.

**WP8 / §C.10 — `advisor` lives in the "Advanced (raw)" group rather than behind a control of
its own.** §C.10 says to hide it behind "Advanced". The harness cards already have exactly one
such place - the raw group, which is where every other "you had better know why" option is - so
it went there instead of gaining a second disclosure that means the same thing.

**WP8 / §F.4 — `Extra flags (raw)`, `Extra env (raw)` and `Config overrides (raw)` are one
group, "Advanced (raw)".** §F.4 lists them as three disclosures. Two of them hold a single
field each, and a disclosure per field is a click to reach a text box. The three groups' options
are all present, in that order, inside one group.

**WP8 / §I — four files outside the named scope.** The scope names `internal/termux/install.go`;
the endpoints and their state went to a new `internal/server/tmux.go` rather than into
`admin.go`, the live application of the terminal card to `internal/termux/apply.go` (it is
Manager state, and `manager.go` belongs to WP2), the free-space probe to
`internal/server/disk_{unix,other}.go` because `syscall.Statfs` does not exist on every platform
this repo builds for, and three exported accessors for the paths §F.7 checks to
`internal/harnesses/statepaths.go`, so that the diagnostics name the same directories the
launchers use rather than a second guess at them. `internal/server/server.go` gained two hunks
only - the `tmuxAdmin` field and three route lines - because WP5 is mid-flight in that file.

**WP8 / §D.2, §D.9 — the "viewer lag" admin row is dropped and the `lag` frame stays.** §D.9
gives the frame exactly one consumer and the review note says WP8 either uses it or the frame
goes. §F describes eight admin sections and none of them is a viewer-lag row; the frame is
per-session state and the dashboard has no session list to hang it on. Put to the orchestrator,
the decision was: no row, and the frame is kept as the purely diagnostic thing §D.2 already
calls it. Nothing in `ws.go` or `session.js` changes.

## WP8 — after the review

**WP8 / §C.10–§C.12 — "confirm-twice" is two dialogs, the second asking its own question.** The
specification says confirm-twice and does not say what the second confirmation is. A second
identical dialog is a second tap and nothing more, so the second one restates the consequence in
its own words ("Once more, to be sure") and takes a different answer. A refusal at either step
puts the control back exactly as it was. The same guard was added to two options the catalogue
had left unguarded and which are in the same danger class: Codex's `sandbox` when it is set to
`danger-full-access` (no sandbox at all) and Claude Code's `remote_control` (it hands the session
to whoever can reach the channel).

**WP8 / §I — three more files outside the named scope, all of them one fix.** Finding 3 is a data
race on the two `Manager.cfg` fields `ApplyTerminal` writes, and the reads are spread over
`manager.go`, `adopt.go` and `ensure.go`; they now go through `policy()`, `confOptions()` and the
existing `Available()`, which take the manager's lock. `conf.go`'s `writeConf` became a
temp-file-and-rename, because a save of the terminal card can truncate the generated
configuration while a first `new-session` is reading it. Nothing else in those files moved.

**WP8 / §F.7 — a diagnostics row is a verdict, and its detail is behind the row's "i".** §F.7
lists the checks and says nothing about how a row is drawn; §E.10 rule 3 says a version, a path
or an error message is never in visible text. `checkResult` therefore carries `summary` (what is
shown) as well as `detail` (what the "i" holds), and a version is passed through `StripANSI` and
clamped before it is shown at all - a CLI that prints a colour query into `--version`, which the
suite's own fake does, would otherwise put escape glyphs on the dashboard.

**WP8 / §F.1 — a successful install re-probes tmux instead of asking for a restart.** Neither §F.1
nor §A.3 says what happens to the manager when the installer finishes; `termux.New` decided
`unavailable` once and never again, so the card said "tmux is ready" while the new-session sheet
still refused. `Manager.Redetect` re-runs the lookup and the version check under the manager's
lock, writes the generated configuration if the manager never got that far, and the install
goroutine calls it before it publishes `done`; the page then asks for the catalogue again. It
deliberately does not touch `Tmux.Bin`, which is the literal "tmux" on a machine that had none
and is resolved on PATH at every call.

### WP5 fix-up — the review findings

**WP5 / §D.7 — the session cookie stays `SameSite=Lax`.** The section asks for `Strict`. A
WebSocket handshake is not a top level navigation, so `Lax` already withholds the cookie from a
cross site request and the origin check inside `websocket.Accept` is what actually defends the
transport; `Strict` adds nothing to that and costs the one case people meet, a link to the
tunnel opened from another app landing on the login page. The spec owner is asked to revert
§D.7.

**WP5 / §D.5 — a stalled viewer is told 1013 only if it starts reading again.** The section
says a write that blocks for thirty seconds is closed with 1013. A close frame needs room in
the same socket, so a peer whose buffer is full cannot be told anything: at the deadline the
connection is marked overdue and the writer closes it with 1013 as soon as the stuck frame is
away - which is the moment the client reads again - and one further interval later the socket
is dropped without a word. What notices that case is the client's own ping watchdog, which is
what it is for. The Go test drives the whole path over a listener with a four kilobyte send
buffer, because a loopback connection otherwise buffers several megabytes before a write
blocks at all.

**WP5 / §D.7 — the handshake ceiling counts only handshakes that would start a terminal.** A
reconnect carrying a viewer id the hub still remembers is never counted: twenty a minute is a
guard against a broken client starting tmux clients, and a phone flapping in and out of
coverage with two tabs open would otherwise lock itself out of its own terminal for a minute.
The forwarding headers (`CF-Connecting-IP`, `X-Forwarded-For`) are now believed only when the
peer itself is loopback or on a private network, so that a caller cannot choose its own
identity and rate limit nothing; the same change applies to the login throttle, which reads the
same helper.

**WP5 / §A.10 — deleting a session ends its viewers through the transport.** Step 1 says
"detach and close every viewer PTY", and the Manager only ever saw the viewers that still held
the window. A viewer whose socket dropped is now demoted rather than forgotten
(`termux.Manager.demote`), so `Delete` closes its terminal too, and the delete route is wrapped
so that every socket on a session that was actually deleted is told in a frame and closed with
1001, taking its grace timer and its ring with it.

**WP5 / §D.2 — a client that breaks the framing is closed with 1002 and loses its terminal.**
The spec does not say. A zero length or wrong-kind binary frame used to close as a normal
closure and leave the viewer in its ninety second grace, which is eight bad frames away from a
session that can hold no more viewers.

**WP5 / §D.6 — input sequence numbers start at one.** A first frame at zero is answered with
`input_ack: 0` rather than silently discarded, so a client that got it wrong is told where to
start. Sequence numbers are JSON numbers and therefore exact to 2^53, which a keystroke
counter reaches in no lifetime.

**WP5 / §D.2 — `hello.session` is the same envelope every other endpoint answers with**
(`sessionView`, WP4), so the browser has one shape for a session; and the resume `notice` reads
`Manager.ResumeNoteOf` rather than guessing from the row, so the banner and the session list
cannot disagree.

## WP7 — Mobile, offline and resilience

**WP7 / §E.6 — `keybar.js` exports three things, not one.** §E.6 names
`mountKeyBar(host, term, socket)`, and that function exists with that
signature. The line input and the microphone it describes in the same section
are a second control with its own lifetime, so they are `mountComposer({form,
input, mic, recTime, sessionId, socket, term})`; and the visual-viewport
handling §E asks for is `followViewport()`, mounted once for the page rather
than once per session. `keyBarWanted()`/`setKeyBarWanted()` carry the "coarse
pointer or under 900px, and toggleable from the session menu" rule so that
`session.js` does not have to know it.

**WP7 / §D.6 — the client's `input_lost` frame counts keystrokes and carries
lines, instead of handing back raw frames.** §D.6 wants two different things
done with what a `viewer_fresh` hello discards: keystrokes are counted into a
toast, a composed line goes back into `#lineInput`. `sendInput` therefore takes
an optional `{text, onDelivered, onLost}`, and the local `input_lost` control
frame is `{keystrokes, lines}`. Nothing on the wire changed.

**WP7 / §E.6 — the draft in localStorage is `{draft, pending}`, not a bare
string.** §E.6 says the field's value is persisted on every input event and
kept "until it is acked". Those are two different pieces of text once a line
has been sent and not yet acknowledged, so the key holds both: `draft` is what
is in the field, `pending` is what has been sent and not acknowledged. Both
come back into the field on a reload, which is the promise the section makes.

**WP7 / §H.3, §H.2 — `mockOpenRouter` cannot serve the dictation scenario, so
`openRouterStub` was added beside it.** §H.3 says `dictation` runs "with
`mockOpenRouter`". A Playwright route only sees the browser's own requests, and
the transcription is not one: the browser posts the recording to
`/api/voice/transcribe` and the **server** calls the gateway. The scenario
therefore starts a small HTTP server, points `openrouter.base_url` at it - the
field exists for exactly this, as `config.go` says - and asserts on the upload
the server actually made. `mockOpenRouter` is left in place for a scenario that
needs the browser side. `start()` also gained `args` and `permissions` so that
Chromium can be given its own fake microphone; nothing else in the harness
changed.

**WP7 / §E.10, WP6 review F2 — dim (SGR 2) text is a known limitation of the
white palette.** xterm.js applies SGR 2 as an opacity blend *after*
`minimumContrastRatio` has re-derived the colour, so dim white lands at 2.61:1
on the white page while normal intensity is correctly lifted above 4.5:1. There
is no xterm option that reaches the dim path, and re-deriving it would mean
patching the vendored bundle. Recorded rather than fixed: the load-bearing
claim - that ordinary output is legible - holds and is measured by
`createshell`.

**WP7 / WP6 review F3, F4, F5 — the three minors this package inherited.** The
`index.html` comment that claimed `#termSize` hid its detail behind an
`infoTip` now describes what the code does (the size is two numbers, hidden
under 720px); `selectSession` debounces by 120 ms so that a run of taps down
the list opens one socket rather than six; and the unused `.chat-list
.group-label` rule is gone.

**WP7 / §E.2 — the drawer closes when a session is started.** Not a deviation
so much as a defect the key bar scenario found: on a phone "New session" is
tapped inside the drawer, and the drawer stayed open over the terminal the tap
had just asked for. `newSession()` now closes it, exactly as `selectSession()`
does.

**WP7 / §E.5 — coming back online starts the backoff again.** Every retry
attempted while `navigator.onLine` is false increments the attempt counter
without opening a socket, so a long tunnel could leave the first attempt after
the signal returns waiting up to fifteen seconds. The wake handler now resets
the counter before reconnecting: what failed was the network being gone, not
this server refusing.

**WP7 / §E.9, §H.3 — the offline app shell is asserted inside `offlineonce`
rather than in a scenario of its own.** §H.3's table has no scenario for the
service worker, and the WP7 brief asks for one. `offlineonce` is already the
scenario with the network switched off, so it ends by listing the precached
entries (22, `keybar.js` among them) and reloading the page with no network at
all: the shell opens, the terminal engine is there, and the connection bar is
honest.

**Fix-up / §D.6 — `viewer_fresh` discards resends, not everything held.** The
client threw away its whole held list whenever `hello` said `viewer_fresh`,
and a first attach is always fresh. That is right for a frame that has been on
a wire - the server cannot tell it from new input - and wrong for one that has
never left the tab, which is simply input that has not gone yet. Each held
frame now records whether it was ever written to a socket, and only those are
discarded and reported.

**Fix-up / §D.6 — the anchoring rule holds on the *first* connect too.** §D.6
states the reset-and-renumber rule for a reconnect, and the client applied it
that way: frames typed on a socket that was open but had not yet heard `hello`
went out at whatever number the counter happened to be at. When the server
still remembered that tab, every one of them below its `lastInputSeq` was
discarded as a resend and the keystroke was gone without a word. The rule is
now what the section's own wording says — nothing is sent until `hello` has
anchored the counter — and the server's gap rejection is answered by
renumbering and resending rather than by leaving those frames held until the
next connect.

**Fix-up / §D.3 — a socket is never torn down mid-frame to give it a status.**
Both the takeover and the end of `serveTerminal` cancelled the writer's context
and then wrote the close frame. Cancelling the context a `Write` is blocked on
makes coder/websocket abort the connection, so the peer saw 1006 rather than
the 1012 or 1002 it was being given - which is what made
`TestTerminalMalformedFrameIsAProtocolError` fail under load. The writer is now
asked to stop between frames and waited for before the close frame is written,
and only a writer still stuck after the close grace is cancelled.

**Fix-up / §D.1, WP7 review F1 — a handshake always answers.** Every early
return of `serveTerminal` after the viewer was claimed - a terminal that went
during the handshake, a `hello` that could not be written - returned without
marking the writer stopped and without releasing the viewer. The writer for
those sockets was never started, so `stopped` was never closed, and the next
handshake for that tab waited in `takeOver` for a goroutine that did not
exist: the third socket of a wake-up storm opened and was never spoken to.
Those paths now go through one `abandon`, which marks the writer stopped,
closes with 1013, and releases the viewer into its grace.
