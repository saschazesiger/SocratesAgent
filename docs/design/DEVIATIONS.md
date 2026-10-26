# Deviations from the design specification

Appended per work package: WP, spec section, what, why.

> **Superseded (2026-09-02, branch `google-tts`):** the local Piper voice and
> `internal/piper` are gone. Reading an answer out loud is now Google Cloud
> Text-to-Speech (`internal/googletts`), called with an API key from
> Admin → Voice; `GET /api/voice/status` was replaced by `POST
> /api/voice/check`. Passages below that describe the local engine are
> history.

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

## WP7 — the review's findings

**WP7 review F1 / §E.5 — a wake is coalesced, a handshake in flight is left
alone, and an open socket has a deadline of its own.** A phone regaining signal
raises `online`, `focus` and `visibilitychange` in the same tick. The wake
handler used to tear down whatever was there and reconnect, which abandoned a
handshake the server had already begun to answer; the second handshake was then
refused while the first was cleaned up, and the third could open and be told
nothing at all - with the ping watchdog starting only at `hello`, the page sat
behind "Reconnecting…" for ever. `resume()` now skips a socket that is
`CONNECTING`, gathers a storm into one attempt (60 ms), and every open socket
is given ten seconds to be told `hello` before it is given up on. The server
half of that wedge was fixed separately in `internal/server/ws.go`.

**WP7 review F2 / §E.9 — an offline page keeps the session it was on and says
what it does not know.** A reload with no signal used to report "No sessions
yet." (a fact the page cannot have), erase the session id from the URL and, when
the signal returned, leave the person in front of an empty pane with the
composer - and the line they had typed into it - hidden. The page now
distinguishes "no sessions" from "could not ask": the list says
"Can't reach Socrates.", the pane says "No connection", the id stays in the
hash, and the first list that arrives reopens it. `openWanted()` is the only
thing that attaches on its own, and the guard against attaching twice lives
there and nowhere else.

**WP7 review F3 / §E.6 — a modifier is applied to keys, not to what the
terminal answers.** `onData` carries focus reports and the replies to the
questions tmux asks on every attach. Treating those as "the next key" meant
tapping the keyboard button spent the Ctrl that had just been armed - the
focus-in report got it - and a locked Alt put an ESC in front of every reply.
Only a single printable character is transformed now; everything else passes
through and leaves the modifier armed.

**WP7 review F4 / §E.6 — a restored line is a draft, not something in flight.**
On mount, whatever was pending in storage goes into the field and the pending
list starts empty. Nothing holds those bytes any more, so keeping them as
"in flight" left a copy in `localStorage` for ever, handed back on every later
reload of that session.

**WP7 review F5, F6, F9 — the minors.** Paste spends a one-shot modifier and
hiding the key bar disarms it; a transcription failure says "The recording
could not be transcribed. Try again." instead of the gateway's status line; and
the service worker's comment now says that its 807 KB / 206 KB is the vendored
terminal alone, with the measurement for the whole list in the README.

**WP7 review F7, F8 — what the scenarios measure.** `offlineonce`'s "held"
assertion counted frames that had been handed to `WebSocket.send`, which held
frames never are; it now asserts that nothing typed during the outage reached
the pane at all, which is what "held" means from the outside. The mobile
scenarios run at 390×844, and `start({touch:true})` gives Chromium a coarse
pointer so `keyBarWanted()`'s own rule is the one under test. `offlinerestart`
is new: an outage, a restart inside it, a real wake storm, and the rule that
nothing may be lost in silence - every byte typed during the outage is either
delivered exactly once or handed back with a toast.

**WP7 review F1(b) — the handshake deadline is not covered by a scenario.** It
needs a server that accepts a socket and never speaks, which cannot be arranged
without changing `internal/server`. `offlinerestart` covers the path that used
to reach it, and the deadline is the belt to that braces.

**WP9b / §A.7, §H.3 #17 — one device connecting must be one size change, and
that had to be fixed in `term.js`.** The notice is specified to fire once per
real size change, and it fired twice for every attach: the socket is opened
with whatever `state.term.size()` answers, and `attach()` reveals the composer
and the key bar immediately before asking - so the answer was one layout
behind. The session attached at the pre-composer size and resized to the real
one a moment later, which is two `resize-window` calls, two re-layouts of the
TUI, and two "another viewer resized this session" notices on every other
device. `createTerm` now measures once, synchronously, before the observer is
armed, and `size()` takes a fit that is still on its 80 ms debounce rather than
answering from before it. Nothing in `session.js` changed. `twoviewers` asserts
the window moved exactly once, from tmux's own `#{window_width}x#{window_height}`
rather than from anything the page reports.

**WP9b / §H.3 #18 — `backpressure` measures against the journal, not against a
record the fake keeps.** The spec asks for "the fake's own record of what it
printed". `faketui` writes no such file; what it has is `/spin`'s own numbering,
1 to 200, which is a record a hole cannot hide in. The scenario asserts that
the journal holds all two hundred lines in order and exactly once each, and
that the screen is a contiguous tail of that same stream ending on line 200.

**WP9b / backlog — the journal sink is not allowed to outlive its server.** A
sink orphaned by a killed tmux server holds a journal open and a copy of the
Socrates binary resident until the machine is rebooted, and a run that left
twenty-two of them behind filled this machine's tmpfs. `PipeCommand` now begins
with `exec`, so the shell tmux spawns becomes the sink rather than waiting on
it - one process instead of two, and the sink is a direct child of the server.
`RunJournalSink` watches for the reparenting that says its server has gone.
Both ends are tested against the process table: a session deleted and a server
killed with SIGKILL. The servers themselves are guarded in the tests, where the
leak came from: a package-level timeout panics before any cleanup runs, so each
lab arms a small shell loop that outlives that panic and takes the server down.

## WP9a — Harness lifecycle in the browser

**WP9a / §H.3 rows 13-15, §E.8 — the scenarios prove the conversation id through
`cli_session_state` and the resume argv, because the id itself is not in the
API.** `store.Session.CLISessionID` is `json:"-"`, so no endpoint hands the CLI's
own session id to the browser; §H.3 asks the three create scenarios to assert on
`cli_session_id`, and §E.8 lists it among a row's technical detail. The
scenarios assert the same facts through what is reachable: the `--session-id`
Claude Code was given at creation, the id the program itself answers `/id` with,
`cli_session_state` leaving `pending` for `known` once the discoverer has found
the conversation, and the id on the **resume** command line - which can only
have come from the store, since the process that knew it was gone. `session.js`
shows `cli_session_state` in a row's tip rather than the id, for the same
reason. Exposing the id is a server change and belongs to whoever owns
`internal/store` and `internal/server`.

**WP9a / §C.8 — after a reboot only the first session can be resumed;
`clearDeadSession` refuses the rest.** Found by `rebootresume` and reproduced
without a browser. `Manager.clearDeadSession` decides whether a stale tmux
session is a husk by running `display-message -p -t <name> -F '#{pane_dead}'`
and treating any answer other than `1` as "still running". On tmux 3.6 a
missing target is **not** an error: the command exits 0 and prints an empty
line, so `!= "1"` is true and the relaunch is refused with `ErrStillRunning`.
While the tmux server is gone entirely the call fails with `serverGone` and the
resume works, which is why the first session after a reboot comes back and every
later one is answered `409 the session is still running` - with its pane sitting
under "Resuming after a restart…" for ever. The fix is in
`internal/termux/manager.go`, which this package does not own, so `rebootresume`
resumes one session (the Claude Code one, which is what §H.3 row 16 asks for)
and the defect is reported to the coordinator instead of worked around.

**WP9a / §E.7 — `resuming` is a client-side state, because the store has none.**
The overlay table lists `resuming`, but `store` has only starting, running,
exited, needs_resume and failed, and the row says `needs_resume` for the whole
of a resume - which happens inside the WebSocket handshake. Opening such a
session would otherwise leave "This session is not running." on screen for the
seconds the relaunch takes, and a list refresh in the middle would redraw the
button that had just been pressed. `session.js` therefore keeps
`state.resuming`, set when this tab opens a session that is not running and
cleared as soon as the server says what it became.

**WP9a / §E.7 — the resumed banner carries `resumed_from` behind an `infoTip`.**
§E.7 gives the banner two sentences and §E.10 rule 3 says every identifier is
hover-only; the notice had no place for one. `notice()` now takes the facts and
renders the same "i" the overlays use, so "Resumed after a restart." keeps the
conversation it came back on behind it rather than in the line.

**WP9b / §C.8 — a missing tmux session is not a running one (the WP9a
blocker).** After a reboot only the first session could be resumed. The first
resume starts the tmux server again, and from that moment every session still
waiting has a live server with no session of its own in it - and tmux 3.6
answers `display-message -p -t <missing> -F '#{pane_dead}'` with **success and
an empty line** [V] rather than with an error. Read as "pane_dead is not 1",
that meant "still running": `clearDeadSession` refused the relaunch, the API
answered 409, and the session sat under "Resuming after a restart…" for ever.
`clearDeadSession` now asks `has-session` first, which fails properly on a
target that is not there, and `paneIsDead` treats an empty answer as a session
that is gone. `TestRebootResumesEverySession` resumes two sessions after a
killed server - two is the smallest number that can show it - and
`rebootresume` now creates two and resumes both in the browser.

**WP10 / §H.3 row 21 — `lighttheme` proves the theme by measurement; the
by-eye half was not performed.** The row asks for "a screenshot of a real Codex
session inspected by eye to confirm `tui.theme` applied". A real Codex session
cannot be started here: it needs an account and would spend tokens, and the
implementer's brief forbids it outright. What the scenario asserts instead is
every link of the same chain that a pair of eyes would have checked: the
`-c tui.theme=…` Socrates built, the `theme=light` the program read back
through tmux and the PTY, the `rgb(255,255,255)` the pane is painted, and all
sixteen ANSI colours drawn at 4.5:1 or better on it. The screenshot is written
to `e2e/out/shots/lighttheme.png` and is of the fake wearing the same terminal.

**WP10 / §E.4 — the `design` scenario measures the drawn colour, as DEVIATIONS
said it should.** Following the WP6 entry above: the palette table is not the
claim, `minimumContrastRatio: 4.5` is. `lighttheme` prints all sixteen ANSI
colours through a real pane and reads what the renderer drew; `design` asserts
the four §E.10 rules. `term.js`'s comment, which claimed every colour in
`LIGHT_THEME` is ≥ 4.5:1 against white, was corrected to say what is true.

**WP10 / §E.10 rule 3 — two things were named in visible words, one was moved
and one was left.** The voice status line printed the Piper binary's path in
its sentence; `admin.js` now splits the path out and puts it behind the "i",
without touching `piper.Status.Detail`, which is also a setup-check row and is
already behind an "i" there. The Terminal engine card's `tmux 3.6` was left
visible on purpose: that card's whole subject is whether tmux is usable and
which version answered, so the version is the finding, not a detail about it.
`design` asserts on paths, the workdir and the tmux session name, not on that.

**WP10 / §G.1 — `openrouter.title_model` lost its controls, not its key.** The
setting named a chat, and nothing generates a name any more: a session is named
after its program and the moment it started, and renamed by hand. `config` keeps
the field (§G.2 keeps `OpenRouter` verbatim, and a stored key nobody reads costs
nothing), but the dashboard's "Title model" field and the two "Read answers"
switches were removed and the API-key and setup hints rewritten: a control that
promises behaviour the build does not have is worse than no control.

**WP10 / the /tmp litter — `TestMain`, not `t.TempDir`.** The backlog asked for
`t.TempDir()`. It does not fit: the fake CLI is built once per package through a
`sync.Once` and shared by every test, so no single test owns the directory and
`t.TempDir`'s cleanup would delete it out from under the next one. Both packages
remove it in `TestMain` after `m.Run()` instead, which is the first moment the
last test that needed it has finished. The `TestXxx…` directories in `/tmp` come
from runs that were killed; they are `t.TempDir()` already.

**WP10 / §H.2 — the fake CLI answers `--version` and the model listings.** It
opened a pane for every question Socrates asked it, so `probeVersion` timed out
against a TUI and the Codex and OpenCode catalogues were empty for the whole
suite. `faketui` now answers `--version`, `codex debug models` and
`opencode models [--json]` as plain subprocesses and exits.

**WP10 / docs — the screenshots were renamed.** `screenshot-chat.png` and
`screenshot-auto.png` were of the deleted chat product; they are replaced by
`screenshot-session.png` (a session at 1280×720) and `screenshot-phone.png` (the
key bar and the line input at 390×844). `screenshot-admin.png` is regenerated and
`screenshot-tunnel.png` is kept untouched, as §G.2 requires.

## WP9a — the review's findings

**WP9a review F1 / §E.7 — a notice appends its tip only when it has one.**
`ParentNode.append` is not `el()`: a `null` argument is stringified, so every
notice without facts - `resized` and `desync` among them - carried the word
"null" beside its close button. The tip is appended in its own statement now,
and `rebootresume`, `createclaude` and `twoviewers` read the **whole**
`#termNotice` text and require it to equal §E.7's sentence exactly. Reading
`.notice-text` alone is what let the defect through.

**WP9a review F2 / §E.7 — a pressed Restart is a fact about the session, not
about the button.** The list refreshes on a wake and every fifteen seconds, and
the row still says `exited` while the POST is in flight, so the overlay was
rebuilt with a fresh, enabled button under the finger that had just pressed it -
and a second press was a second relaunch, a 409 and a toast. `state.restarting`
now holds the session whose relaunch is in flight; `drawOverlay` draws the
button from it and `restart()` refuses a second call for the same session. The
needs_resume **Open** button ignores a press while `state.resuming` is set.
`createclaude` holds the POST on the wire, forces a refresh with an `online`
event, and asserts the button is still pressed and that one press was one POST.

**WP9a review F3 / §E.10 rule 3 — a refusal is a sentence, not a server
message.** A failed relaunch or create toasted the reason verbatim - a plan
path, a tmux session name - while the same string sat correctly behind the
overlay's "i". Those paths now toast "The session could not start."; a 409 says
the session is already running, a delete that failed says so, and only a lost
connection still speaks in its own words, because that is the one a person can
act on.

**WP9a review F4 — `e2e/README.md` lists the four scenarios**, and the sentence
about rows 13-16 arriving later is gone.

**Fix-up / §E.3 — the sheet waits for a model where the CLI names none.**
OpenCode now reports models and no default, so `#nsStart` stays disabled until
one is picked - correct behaviour, and it broke `createopencode`, which never
chose one. `startWithModel` asks the sheet whether the step is shown and picks
the first entry when nothing is pre-filled, which is what a person does.

**WP10 / §H.3 — three scenarios were reading a screen that tmux is allowed to
repaint.** `latehello`'s warm-up, `exitoverlay`'s two banner assertions and
`harnesses`' eight now read the journal instead, through `journalHas` and
`journalSays`. A banner is printed before the first viewer has resized the
window, so an attach redraw can reflow it away; `latehello`'s warm-up wrote to
a file, so the only trace of it on screen was the echo of the typed characters.
All three had failed intermittently. `journalSays` counts occurrences, because
the journal is one file across a restart and a banner that was already there
proves nothing about the program that has just been started.

**WP10 — `startSession` handed back the previous session's id.** It waited for
`location.hash` to be non-empty, which is already true for every session after
the first, so a scenario that starts several in one tab was reading the wrong
journal. It now waits for the hash to *change*, as `startWithModel` already did.

**WP10 / §E.3 — the suite now has to pick a model for OpenCode.** With the fake
answering `opencode models`, OpenCode has a catalogue and no default in it -
which is what the real `opencode models` gives - so **Start session** stays
disabled until a model is chosen and the sheet's hint says so. That is the
design (§E.3), so `startSession` does what the hint tells a person to do rather
than the sheet being changed.

**WP10 / §B.6 — the journal sink no longer creates the directory it writes
into.** `journalSink` began with `MkdirAll`, so a sink started for a session
that had already been deleted recreated the tree - which is how 47 of one
`go test -race ./...` run's `t.TempDir()` directories came back after their
tests had removed them, each holding nothing but a `sessions/<id>/journal.raw`.
The directory is made when the session is made; a sink that has to make it is
writing for something that is gone, and now fails instead. One leftover remains
after a full run (`TestTmuxInstallRefusesASecondOne`, which comes back with a
`tmux.conf` and a `hook.sock`): a late `Manager.Start` on a data directory the
test has already removed. It is one directory of a few hundred bytes, and the
fix belongs where that goroutine is owned.

**WP10 / §G.1 — the chat product's CSS class names were left alone.** The
session list is still `.chat-list` holding `.chat-item` rows, and the button is
still `.new-chat`. The rules are live and correctly retargeted, so this is a
rename and nothing more; it would touch `app.css`, `index.html`, `session.js`
and about forty selectors across `e2e/run.mjs`, in files two work packages were
being reviewed in. It is recorded here rather than done blind at the end.

**WP9b review F1 / §A.5, §E.3 — a session is created at the size the pane is
about to be.** The sheet posted `cols: 0, rows: 0` whenever no terminal was on
screen, which is every first session: the store's 120x40 became the window, and
the first viewer's attach immediately resized it to what the pane really is. A
tmux window that shrinks reflows, and on 3.6 that pushes the head of the
program's first wrapped line into the scrollback before anybody has read it -
the banner a CLI prints on start-up loses its first row, which is how
`backpressure` failed five times out of five. `term.js` gained `measurePane`,
which fits a throwaway terminal in the real host and disposes of it, and
`newSession` measures with the chrome in the state the attach will leave it in
(the composer up, the key bar where this device wants one). The first attach is
now a same-size retake and nothing reflows. `harnesses` asserts, for all four
session types, that what the program printed first is what the pane shows
first.

**WP9b review F2 — commit f92f43e swept about four hundred lines of WP9a's
uncommitted `e2e/run.mjs` into a WP9b commit.** Three agents shared one
worktree, and the staged copy of `run.mjs` that commit was built from was a
snapshot older than WP9a's own work in the same file. Nothing was lost - the
worktree kept every line, and WP9a's commits that followed carry them - and no
history has been rewritten, because a rewrite would break every branch built on
it. Recorded here because the commit's authorship of those lines is wrong, and
because the lesson is the one that follows: in a shared worktree, stage by path
and rebuild the file from `HEAD` plus your own edits rather than from an index
somebody else is holding.

**WP9b review F4 / §H.3 #18 — what `backpressure` may assert about the
delivered stream.** A viewer is sent a window, not a transcript: tmux repaints
a pane that is producing faster than a client can draw, so of two hundred lines
about a hundred and eighty cross the wire, out of order where a repaint
overlaps. Neither the count nor the order is the product's promise, so the
scenario asserts the three things that are - the end of the burst reached the
browser, the reader was never overrun by the ring (a `replay_from: 0` hello,
which the page shows as the desync notice), and the socket carried it without
being closed - alongside the journal, which does hold every line in order.

**WP9b backlog — `TestTerminalSizeOwnership` and the 600 s package timeout.**
Not reproduced: forty runs alone, then sixteen more with two `-race` runs of
`internal/server` and `internal/termux` in parallel, all green. `termHub.release`
was named in the one panic trace, but its only blocking call - the hand-over of
the window through `Manager.own` - is bounded by a five second context, so it
cannot be where six hundred seconds went. Under 2x load the package takes about
eighty seconds against a budget that covers the whole binary; the conclusion is
a package that ran long under whole-repo parallelism, not a deadlock. One later
run did hit the timeout again while another agent's suite was running; the
`tail -5` in use kept only the last lines, so the dump was lost. If it returns,
capture the whole panic.

**WP10 / final review F5 — a temporary directory is compared resolved.**
`TestOrphanSessionIsAdopted` compared `t.TempDir()` with the row's working
directory, which comes back from tmux as `#{pane_current_path}` and is always
the real path. On macOS `t.TempDir()` is under `/var/folders/…`, a symlink to
`/private/var/…`, so the test would have failed on `macos-latest` the moment CI
started installing tmux there. `realDir` in `testing_test.go` resolves it, and
the failure was reproduced on Linux with a symlinked TMPDIR before and after.
It is the only such comparison: production code stores what tmux reports and
never matches it against a path of its own.

**WP10 / final review F7 — an over-long data directory is refused, in words.**
`termux.CheckSocketPaths` rejects a `-data` whose `tmux.sock` or `hook.sock`
would be at or over 104 bytes (the smaller of the Linux and BSD `sun_path`
limits, so a directory that works here works there). It is the first thing
`New` decides, so `Available()` carries it into the 503 on `POST /api/sessions`
and into the engine card, which no longer answers `ok: true` while every
session dies; `Start` refuses before the hook listener can fail with `bind:
invalid argument`; and the server logs the sentence at start-up.
`TestALongDataDirectoryIsDiagnosed` covers all three, and the README's `-data`
row says to keep it short.

**WP10 / final review F6 — the `opencode` directory in TMPDIR is the real CLI's
own.** Reproduced outside Go entirely: `TMPDIR=… opencode --version` creates
`$TMPDIR/opencode`. It appears in a test run only on a machine that has OpenCode
installed, when the catalogue probes it, and nothing in Socrates can stop a
third-party binary writing into TMPDIR. The e2e stub directory
(`socrates-e2e-stub-*`) was ours and is now made through `harness.mjs`'s
`scratchDir`, so the run's own cleanup takes it away.

**WP10 / final review F9 — the two remaining dangerous values ask twice.**
Claude Code's `permission_mode = bypassPermissions` and Codex's
`approval = never` now carry the same confirm-twice as the "Dangerous skip" and
the sandbox switches beside them. `bypassPermissions` is the value the launcher
suppresses Claude Code's own safety dialog for, so it was the one place where
Socrates turned that dialog off without ever showing one of its own.

**WP10 / final review F10, F11 and the WP9b leftover.** `server.go`'s comment
said the cookie is `SameSite=Strict`; it is Lax, and the comment now says so and
where the reasoning is. The README's layout block said the tmux socket is 0600
while the security section said 0700; measured, it is 0700 in a 0700 directory.
And the Setup check gained a **Recovered sessions** row: how many sessions
Socrates found on its socket with no row and took in, their ids behind the "i",
and none once somebody has renamed one - a renamed recovered session is one that
has been dealt with.

### WP5 — the final review's F3

**WP5 / §C.1.1 — the responder answers DA1, DA2 and the two window reports, and swallows
XTVERSION and DSR.** §C.1.1 answers only the colours and the theme mode and passes the rest to
the browser. Measured on tmux 3.6, an attach asks a client six things, and a reply is only a
reply while tmux is still waiting for it: one that arrives after an outage is typed into the
pane, so the person's next command runs as `1;2c0;276;0cecho …`. DA1 and DA2 are answered with
the bytes xterm.js would have sent, the window reports from the size Socrates owns (a cell of
8x18 for the pixel form, which nothing on this side can know), and XTVERSION and DSR are taken
out of the stream and left unanswered - nothing answers them today either, and an invented
answer would be worse than tmux's own timeout.
tmux 3.8 adds a seventh, DECRQM for synchronized output (`CSI ? 2026 $ p`): it is answered
`CSI ? 2026 ; 2 $ y`, what xterm.js 6 says before anything sets the mode, so tmux wraps its
redraws in synchronized updates as it would for the browser; any other mode is answered 0.

**WP5 / §D.2 — reply-shaped client input is dropped.** Belt and braces for a page loaded
against an older Socrates or a reply held across a reload: a device attributes reply, a three
number window report, a cursor position report, a mode report (`CSI ? Pd ; Ps $ y` and
`CSI Pd ; Ps $ y`) and an XTVERSION answer are dropped on the input path, everything else is
passed through byte for byte, and bracketed paste is respected so that pasted text is
delivered as it was pasted. The one keystroke this cannot tell from a report is
shift with F3 under xterm's modified function key encoding, which is the same `CSI 1 ; 2 R` as
a cursor at the top left; a corrupted command line is the worse of the two to keep.

## Activity/assist features (2026-09-02)

The four features of `ACTIVITY.md` — the per-session activity detector, the
**Status** button, the **Agent** loop and **audio mode** — built as WP1 (the
detector), WP2 (the server routes) and WP3 (the page and the e2e). Each item
below is a deliberate departure from that specification, accepted at review.

**WP1 / §A — the detector.**

1. `StartActivity` is called from `Server.StartSessions` after `Adopt`, not from
   `Manager.Start`: `Start` runs *before* `Adopt` and starts no goroutines, so
   "in Start, after Adopt" could not both be true. `StartPoll` is the precedent.
2. The source registry is keyed by `harnesses.Kind`, not `config.Harness` —
   there is no such type; harness ids are plain strings in `config`.
3. OpenCode watchers start lazily on the first tick that reads an OpenCode
   session rather than on launch, adopt and `StartActivity` separately: the same
   outcome one second later, through one code path instead of four.
4. `ResetActivity` is called from `Relaunch` only. `resume` reaches a new pane
   exclusively through `Relaunch`, so the second call was duplication.
5. Arbitration step 6 is implemented as *no live L1 answer this tick + L2 idle ⇒
   idle, overriding L3* — the sentence the step ends with, and what makes a
   fallback session leave busy inside 35 s. `BusyCeiling` drops only a
   *remembered* L1 answer; a live L1 busy wins over any amount of silence.
6. `Activity.Source` is `""` when no layer answered. The spec names four values
   and has no name for silence.
7. The Shell layer's `Note` carries the foreground command, which is hover-only
   detail and free.

**WP2 / §B, §C — the routes and the operator loop.**

1. `DefaultAgentModel` is `anthropic/claude-sonnet-5`, not `-4.5`: the spec said
   to prefer a newer Sonnet if OpenRouter lists one, and sonnet-5 is newer and
   cheaper at the same context. `DefaultStatusModel` stays
   `google/gemini-2.5-flash`.
2. `e2e/harness.mjs` was left to WP3, which owns `e2e/**`; the Go side proves
   the same scripted-model behaviour with `scriptedGateway` in `assist_test.go`.
3. Hitting `max_steps` ends the run with `phase:"done"`, not `"error"` — a bound
   that was reached is not a failure, and audio mode still has a sentence.
4. Key names are matched case-insensitively with a few aliases (`Ctrl-C`, `^C` →
   `C-c`, `Esc`, `Return`, …). The vocabulary is still the spec's fixed 17.
5. A `wait_ms` action does not reset "the last action was an interrupt", so
   `C-c`, wait, `C-c` is still two in a row and the second is dropped.
6. `GET …/agent` keeps a finished run for two minutes and answers `null` after
   that: the only way to have both "answers null afterwards" and "a phone that
   locks mid-run comes back to a finished run".
7. The status endpoint calls the model even while a run is acting; refusing is a
   UI decision, and WP3 makes it (below).

**WP3 / §D, §E — the page.**

1. `#audioModeBtn` stays enabled while the socket is down. §D.2 disables all
   three header buttons, §D.5 only the four that start a request; asking a phone
   in a tunnel to stay in a mode it cannot leave is the wrong half of that
   contradiction to obey. The four that ask the server are disabled.
2. `busyButton` was not reused: it lives in `admin.js`, is not exported, and
   swaps a button's *text*, which an icon button has none of.
3. `dictateOnce` takes `onReady` as well as `onTime` — "a second tap stops it"
   cannot be expressed by a signature that only resolves with the transcript.
4. No `GET …/agent` on attach: `attach()` always opens a socket, so
   `hello.agent` is the only seed, and a `hello` carrying `agent: null` drops a
   stale progress line and says so.
5. `Activity.since` is shown as a time of day, not a duration: a duration is a
   different string every second and the row's "i" is rebuilt whenever its facts
   change. `Activity.source` is omitted when empty.
6. The Status button refuses while a run is acting, per the WP2 reviewer's note:
   it says the agent is typing rather than describing a half-typed screen.
7. No `agent-cancel` scenario of its own; `agent-run` asserts the Cancel control
   is on the progress line and `assist_test.go` covers the endpoint.

**The final review's non-blocking notes** (`APPROVED WITH FIXES`, 2026-09-02;
its three fixes F1–F3 and the Claude L3 idle pattern landed in the commit before
this one, and are not deviations):

- A `codex` update interstitial reads as `waiting` through L3 — the right state;
  only the note's wording ("approval required") is slightly off.
- Poll versus frame: `refreshList` can deliver a snapshot older than the last
  `activity` frame, and audio mode would then hear one extra idle→busy→idle and
  speak once more than the session deserved. Rare, and the cure is to ignore a
  view whose `since` predates the map's entry.
- `runStatus` captures the session at the start but writes its answer into
  whatever is on screen when it returns; a switch mid-request should drop the
  answer rather than show it against the new session.
- A Restart the user pressed still leaves the row unread, per §A.5.
- `agentDriver.runs` keeps one entry per session ever driven.

## Sidebar, sheet and key bar (2026-09-02)

A pass over what the page shows for its own sake rather than the person's.
Everything here is a deliberate departure from §E's text, not a defect in it.

1. **The state dot is gone.** §E.10's row was a mark, a name, a dot and an "i";
   the dot repeated the state word and the mark's own ring, and said nothing
   else. The amber it carried for `waiting` — the one signal that was only on
   the dot — moved onto the ring: a complete amber circle, standing still,
   against the hairline arc that turns for `busy`.
2. **The row's "i" became `Info` in the row menu.** The same facts, in a dialog
   built like `confirmDialog`, and the header's "What this session runs" tip is
   folded into it, so a session's facts exist in exactly one place. The exit
   overlay's status and fail reason are plain lines under their sentence.
   `notice()` keeps its "i": what it holds is a fact about *that* notice — the
   conversation a resume came from, the model a Status answer used — and not a
   fact about the session.
3. **The new-session sheet lost its per-harness "i", its `Advanced`
   disclosure and the catalogue's `notes` sentence.** The notes still describe
   each program on the dashboard, where a program is chosen once; the sheet is
   read by somebody about to start work.
4. **A row of choices is one control.** `.seg-row` is a single hairline with
   equal parts inside it that together are the width of the row, instead of
   pill buttons that wrapped.
5. **The key bar follows the keyboard, not the phone.** §E.6 drew it wherever
   the pointer was coarse; it stands in for keys a *keyboard* is missing, so it
   is now drawn where there plausibly is one — `(hover: hover) and (pointer:
   fine)`, an iPadOS Safari that reports itself as a Mac with a touch screen, or
   a physical key actually seen (`keyboardLikely`/`isPhysicalKeyEvent`, both
   pure and both asserted in `keybar`). A phone gets the line input and the
   microphone, and the session menu still turns the bar on anywhere.

## Input durability (2026-09-02)

The report was "sometimes I can no longer type anything into the session": the
pane keeps showing, the socket keeps saying live, and nothing typed arrives.
Five causes, and where the fix departs from what §D says:

1. **§D.6 — a reconnect that finds a dead terminal.** A `tmux` client can end
   without the browser hearing anything (`detach-client` from elsewhere, a tmux
   server restart, a killed window). The hub's viewer entry only asked whether
   its `*termux.Viewer` was non-nil, so every reconnect chose the same closed
   pseudo terminal for ever. `acquire` now asks `Viewer.Ended()` and attaches a
   replacement **under the same entry**: the hello says `viewer_fresh: false`
   (the input counter survives, so what the tab holds is delivered exactly once)
   together with `replay_from: 0` (the screen is redrawn). §D.6 describes those
   two as going together; they do not have to.
2. **§D.5 — an input frame the pane refuses.** It closed the connection with a
   `fatal` error. A frame that failed is not a connection that failed: the
   client is now given `input_ack` with the last number that did reach the pane
   - the same "start again from here" a gap is answered with, so it renumbers
   and resends - and the socket stays up. `termux.ErrClosed`, or three refusals
   in a row, still ends the socket, because only a new handshake can attach a
   new terminal.
3. Bracketed-paste state is reset on every takeover: a paste cut in half by a
   lost socket left the viewer reading every terminal report as pasted text.
4. **Client.** The held-input queue is bounded (512 frames / 128 KB, oldest
   dropped and reported, never dropped in silence); an input watchdog reconnects
   a socket that is open and anchored but has not acknowledged input for eight
   seconds; a browser that says it is offline retries every five seconds rather
   than waiting only for an `online` event iOS does not always raise.
5. **Focus.** A closed `<dialog>` that stays in the page keeps the focus on its
   own button, and a page whose focus is on the body swallows every key. Dialogs
   now remove themselves and restore the focus in the call that ends them rather
   than only in a `close` listener, the ⋯ menu hands the focus back, and the
   session page has one rule: if a session is open and the focus has landed on
   nothing, it belongs to the pane.

`sw.js` was checked and left alone: it is network-first and stamps every shell
address with the build, so it can neither serve an old script to a new page nor
touch a WebSocket.

## The chat, the ticker and the Auto switch

Against `docs/design/ACTIVITY.md` §B.4–B.6, §D.2–D.5, which described a Status
button, a one-field Agent dialog and a headphones toggle. What is built is the
same three features with the middle one turned into a conversation.

1. **§D.3 — the Agent dialog is gone.** The one-field "What should it do?"
   modal and the `#termNotice` line with `kind:'agent'` are removed. The spark
   button (tooltip "What should I do?") opens the chat panel instead, and the
   run a chat starts is rendered inside the message that started it, Cancel
   included. Nothing else could be said about a run in a car, and a form is a
   poor way to ask a question that has an answer.
2. **§D.4 — the headphones button is a switch.** Auto mode is a state, so it
   wears `role="switch"` with `aria-checked` and a 44 px track, not a pressed
   icon button. The `localStorage` key and its `'on'|'off'` values are
   unchanged. It carries the word "Auto", which is the one word in the top
   bar's row of marks; the `design` scenario's assertion that all three header
   controls are wordless was narrowed to the two that still are.
3. **§B.4 — how the chat decides to act.** The assistant answers with one bare
   JSON object, `{"reply":"…","act":"<goal>"|null}`, and `act` non-null starts
   the existing operator run with that goal. OpenRouter tool calling was not
   used: `openrouter.Client` has no tools field, no `tool_calls` on the way
   back and no second round trip, and adding all three for one call site is a
   protocol for nothing. An answer that is not an object at all is taken as the
   reply with `act` null — a model that wrote prose answered the question, and
   only the half that must never be guessed at is lost.
4. **The run's progress is not stored in the chat.** §B.4 has a frame per phase;
   replaying twelve of those into a persisted conversation would make a reload
   read like a keystroke log. What is stored is the reply that started the run
   (carrying `run_id`) and the run's ending; the steps in between are the
   `agent` frames the page already receives, drawn inside that message and in
   the ticker. A reload after the run finishes shows the reply and the ending.
5. **The chat lives in the key/value store**, key `chat.<sessionID>`, capped at
   the last 50 messages and deleted with the session — the pattern
   `activity.unread` already uses, and no migration for a small per-session
   document. `Store.DeleteKV` is new.
6. **§D.3 — one line, not two.** The `#termNotice` progress line is replaced by
   a ticker: one window in which each new line rises in and the old one leaves
   upwards. It is the only place a status's phases, a run's steps and — in auto
   mode, continuously — the session's activity are shown. `#termNotice` keeps
   the dismissible status text, and the two now sit in a `.term-lines` column
   rather than both claiming `top: 10px`.
7. **§B.5 — the status streams.** `POST …/status` broadcasts
   `{"t":"status","id","phase","text"}` for `capturing → asking → speaking →
   done`, or `error`. "Speaking" is emitted by the server immediately before it
   answers, because the browser hands the text to Piper the moment it has it and
   a second round trip to announce that would arrive after the voice did.
8. **Auto mode blocks typing everywhere, physical keyboards included.** The
   composer and the key bar are removed from the layout, the chat panel builds
   a microphone instead of a field, and `term.setTyping(false)` puts xterm's
   hidden textarea into `readonly`, takes it out of the tab order and blurs it
   on every focus attempt. That last one is not conditional on a touch screen:
   distinguishing "trivially safe" from "not" would have meant trusting a
   pointer media query with the one promise this mode makes. Leaving auto mode
   restores all three and refocuses the pane.
9. **§D.5 — the Auto switch is disabled offline only on the way *in*.** The
   section lists `#audioModeBtn` among the controls that go `disabled` while
   `!state.live`, and entering auto mode with the socket down does deserve
   that: the two buttons the mode leaves you with, Status and Agent, are both
   dead without a server. Leaving it is the opposite case. Auto mode is the
   mode in which nothing on the page can be typed, so a switch that also
   locked on an outage would shut somebody inside a terminal they cannot use
   until the connection comes back — on the one device this product exists
   for, a phone that loses signal in a car. `audioModeBtn.disabled` is
   therefore `!usable && !audio`, which is the rule the Stop on `#statusBtn`
   and `#audioStatus` already follow: what asks the server nothing stays
   available.

## Review of the chat, the ticker and the Auto switch (2026-09-02)

Three defects found reviewing the batch above, and fixed in place. Everything
else in it was checked and left alone.

1. **A "Thinking…" placeholder that never went away.** `chat.js` cleared it on
   an assistant message arriving, and nowhere else - so a socket that dropped
   between the question and the answer came back with the answer re-seeded from
   `hello.chat` and the placeholder still under it, for the life of the tab.
   `replace()` now settles it from the stored conversation: a history that ends
   with an assistant turn is a history with no answer outstanding.
2. **A dictated message lost in silence.** `submit()` returned without a word
   when the socket was down or a question was already in flight. With a
   keyboard that leaves the words in the field; with the microphone - auto
   mode, a phone in a car - the words existed only in that call and went with
   it. The message and the reason it did not go are now put in the log, which
   is the rule the rest of the page's input already follows.
3. **A deleted session's conversation could come back.** `handleDeleteSession`
   calls `chats.forget`, but the goroutine that answers outlives the request by
   design: an answer landing after the delete wrote `chat.<id>` back, and
   nothing would ever have removed it again. `chatDriver.append` now drops a
   message for a session that is no longer in the store.

## The title run waits half a minute (2026-09-02)

**§B.6 — the moment is the first edge out of `busy` *after the session has been
alive for 30 seconds*.** The section says "the first committed edge out of
`busy` for that session", full stop, and that is what shipped. In use it names
the session after the harness rather than after the work: a CLI starting up is
work — it paints its box, reads its config, discovers its models and settles —
and the detector commits `busy → idle` from that, seconds after the session
exists and before anybody has typed anything. What is on the screen at that
moment is a welcome box, so the name is "Claude Code in a directory", and
because the turn is spent on the attempt (§B.6, *exactly once*) that is the
name the session keeps.

`titleDriver.run` therefore returns early, silently and **without marking the
session**, while `now - sessions.created_at < titleMinAge` (30 s). Nothing is
asked and nothing is spent, so the next turn to finish is still the one that
names it — which is the whole of the rule: earliest after half a minute, and
from there the first turn to finish. The clock is a field on the driver
(`now func() time.Time`, moved with `setClock`, read under the driver's lock
because the run that reads it is a goroutine) so that the gate can be proven
against the real 30 s constant without a unit test standing still for it.

The cost is the case where a session's very first turn both starts and finishes
inside that half minute: it keeps its placeholder until the next turn ends.
That is the right way round — a row that is briefly still called
`Claude Code · 2 Sep 17:42` is a smaller loss than a row permanently called
after the program running in it, and on a phone the first turn of real work
rarely finishes in under thirty seconds.

`session-title` in the e2e suite proves both halves in the browser: a first
turn driven and finished while the session is young leaves the placeholder and
the gateway untouched, and the turn after the mark renames the row and the
header with no reload. It is why that scenario now takes about a minute.

## The controls those entries argue about are gone (2026-09-02)

A closing note rather than a deviation. The line composer under the pane, the
rule that decided by itself where the key bar was drawn, and Auto mode — the
entries around lines 1194, 1257 and 1349–1364 above, and every other passage
here that reasons about `#lineInput`, a draft that survives a reload,
`keyboardLikely`, or a switch that has to stay usable offline — describe
controls that were **removed on 2026-09-02**.

They stay where they are because they are the reasoning that led to the
removal, not because they still hold. What is true now: there is no line input
on any device, the key bar is off until the ⋯ session menu asks for it and the
answer is remembered per device, and being read to is a property of a question
rather than a mode the page is in.

`docs/design/ACTIVITY.md` §D.2, §D.3, §D.4 and §D.6 say what is there instead,
and `docs/design/DESIGN.md` §E.6 (revision 4) is the binding text.

## The terminal takes a finger (2026-09-02)

**§E.4 — `term.js` turns a touch drag into wheel events, which the section does
not ask for and the vendored bundle does not do.** §E.4 lists the options, the
addons, the fitting and the input path, and says nothing about touch, because
until xterm 6 there was nothing to say: the viewport was a `overflow-y: scroll`
div and a phone scrolled it the way it scrolls anything else.

`@xterm/xterm@6.0.0` replaced that viewport with VS Code's scrollable element,
which moves its content itself and listens for `wheel` and for nothing else. The
bundle even carries VS Code's touch gesture recogniser — `Gesture.addTarget` is
in `xterm.js` — and never calls it on the viewport. The consequence on the
device this product exists to be used from: a finger dragged down the pane moved
nothing at all, and everything that had scrolled off the top of a session was
unreachable. There is no option, no addon and no public API for it.

So `touchScroll(host, term)` in `term.js` follows one finger and dispatches the wheel
events a trackpad would have sent, in CSS pixels, on `.xterm-screen` — the
element a real wheel lands on. Nothing downstream is reimplemented: xterm
decides what a wheel means the way it always has. With `mouse on` in the tmux
conf (§A.3, and the default) the pane is an alternate screen and the wheel
becomes a mouse report, so it is **tmux's** history that scrolls, in copy mode,
exactly as it does for a wheel at a desk; a program tracking the mouse itself
gets the report instead; and a plain buffer scrolls xterm's own scrollback.

Three details are load-bearing:

* **A tap must survive.** Nothing is sent and nothing is prevented until the
  finger has moved 8px, so a tap is still the click that focuses the pane and
  raises the keyboard. Past the threshold `preventDefault()` is taken on every
  `touchmove`, which is also what stops the browser synthesising the tap's mouse
  events on release — a drag must not arrive at the program as a click.
* **`touch-action: pan-x pinch-zoom` on `.term-host`** (`app.css`). Without it
  the browser can claim the vertical drag as a pan of the page before the first
  `touchmove` is delivered. `pinch-zoom` is kept in the list deliberately:
  zooming stays available to whoever needs it.
* **One drag is swallowed rather than passed on.** A pane under tmux is an
  alternate screen, so with `terminal.mouse` off in the dashboard there is no
  report to send and no scrollback of xterm's own to move, and xterm answers a
  wheel with arrow keys. That is a fair reading of a wheel deliberately turned
  in `less` and a terrible reading of the gesture a phone scrolls everything
  with: measured, a downward drag walked the shell's last command back onto the
  prompt. So when the buffer is alternate and `modes.mouseTrackingMode` is
  `none`, the drag is taken from the browser and handed to nobody. It scrolls
  nothing, exactly as it did before any of this, and it types nothing.

A second finger hands the gesture back to the browser, and the listeners are
removed in `dispose()`.

The `touchscroll` e2e scenario proves it through Chrome's own touch pipeline
(`Input.dispatchTouchEvent` over CDP, so `touch-action` is part of what is
tested): a full-height drag on a 390×844 phone moves the pane 45 lines back
through a 120-line history — about what the finger travelled — dragging up
twice returns to the live bottom, and a tap in between moves nothing while
still landing the focus in the pane. It then turns `terminal.mouse` off through
the dashboard's own API and drags again, to hold the swallowed case where it is.

## Bug fix — OpenCode permission events

**§F.2 / ACTIVITY.md — `permission.v2.asked` / `permission.v2.replied` were never real.**
The OpenCode source listened for those two event types, so `StateWaiting` was never once
reported from layer 1: OpenCode 1.17.13 emits `permission.asked` and `permission.replied`
(the payload shapes were right — only the names were wrong). Both names are now accepted,
the real ones first and the `v2` spelling kept in case an older server ever answers with it.
Two things follow from the same fix. The poll now also reads `GET /permission?directory=…`
and seeds the waiting set from it, because a session holding a prompt reports `busy` in
`/session/status` and a prompt raised while the stream was down would otherwise read as work
in progress for ever; a `/permission` that fails is ignored so it cannot cost the busy seed.
Because the endpoint can now clear prompts as well as seed them, three rules come with it.
A successful `/permission` answer is authoritative and *replaces* the waiting set, and it is
re-read every 12 s while the stream is up (under the same mutex as the event handling), because
a prompt can vanish with no reply anybody can match — session aborted or deleted, a lost frame,
a `requestID` that is not the id it was asked under — and on a live stream the reply event was
otherwise the only thing that could retire one. Prompts are keyed by session, and an idle
session drops that session's prompts: a session holding a prompt always reports busy, so idle is
proof it is gone. And a failing endpoint is not
believed either way — it gets its own 5 s budget, one failure changes nothing, and three
consecutive failures drop the set, so an unconfirmable prompt cannot pin a row to `waiting` for
the life of the session.

**Third review — what "idle" means, on both sides.** Four smaller corrections, all in the
OpenCode watcher. (1) The poll's second witness — a prompt whose session is not in the status
map is gone — ran only on a `failed` `/permission` answer, so a server *without* the endpoint
never got it: an events-only watcher that missed the `permission.replied` while its stream was
down had nothing left that could retire the prompt, and the row said `waiting` for the life of
the session. The guard is now "not `ok`", which covers `absent` as well; an `ok` answer needs no
witness because it replaces the set outright. The limitation that remains is stated rather than
papered over: a prompt whose `permission.asked` carried no `sessionID` has nothing to look up in
the map, and is left to the reply event, to ageing out against an empty `[]`, or to the failure
budget. (2) The empty-answer carve-out read `len(waiting) == 0` inside the loop that puts
prompts back into `waiting`, so with two young event-only prompts against one `[]` the first
one carried over made the set non-empty and the second was dropped — and which one that was
depended on Go's map iteration order. Whether the *answer* was empty is now taken once, before
the loop. (3) A prompt asked in the same nanosecond the request was sent was dropped by
`seen.After(start)`; the comparison is now `!seen.Before(start)`. (4) On the stream, anything
that was not `busy`/`retry` retired that session's prompts — an unrecognised or future status
type included. OpenCode emits `busy`, `retry` and `idle`, and only `idle` is proof; an unknown
type still leaves the busy set, because it is not busy, but takes no prompt with it. The fake
TUI carried the mirror image of the same mistake: `/ask` published `session.status` `idle`
before `permission.asked` and dropped the session from its own `/session/status`, which is the
opposite of what a real OpenCode holding a prompt reports. It now reports busy for as long as
the prompt is open, and goes idle when the reply is published.

And `scrapeScreen` gained an OpenCode waiting pattern, which the earlier "**Never infer
`waiting` from an OpenCode screen** — no verified pattern exists" note forbade: the card is
now verified. The pattern is the card's own furniture — `△ Permission required`, or the
`Allow once   Allow always   Reject … ctrl+f` button row — not the words `Permission required`
alone. That looser first attempt was wrong twice over: `capture-pane -S -40` reads 40 rows of
scrollback as well as the pane, and the composer paints the same `┃` gutter the card does, so
a captured OpenCode screen with those two words typed into the prompt box scraped as waiting
(the capture is kept as the negative case in `TestScrapeScreens`). Waiting is still asked
before busy and idle: at 120×40 the card covers the bottom bar, but at taller sizes the bar
survives beside it with `ctrl+p commands` — the idle literal — on it.

**Second review of the same fix — `/permission` is three answers, not two.** Five things
followed from reading the reconcile again. (1) A `permission.asked` that arrives *while* a
`GET /permission` is in flight was replaced away by the answer, which describes the moment the
server computed it: the request is now timed from before it is sent, and any prompt seen after
that is carried over the replace, in the reconcile and in the poll both. (2) Stopping a watcher
cancels its open request, and that cancellation was counted as an endpoint failure — a third of
the budget that drops the whole waiting set, spent on the watcher's own shutdown; the outcome is
now dropped before the lock whenever the context is done. (3) A server *without* the endpoint
answered 404 and the 404 was read as a failure, so three of them destroyed waiting the event
stream had reported correctly. `pendingPermissions` now returns a three-way outcome — `ok`,
`absent` (404/405), `failed` — and `absent` takes nothing: the set is untouched, no failure is
counted, the reconcile goroutine returns and the endpoint is never asked again, logged once as
events-only. The same reasoning covers a subtler case of the same shape: `?directory=` filters
by directory, so a workdir that does not match the server's answers `[]` to a prompt that is
genuinely open. `openCodePrompt` gained `confirmed`, and an empty answer is authoritative only
against a prompt the endpoint has listed at least once, or one older than a reconcile interval —
a just-asked event-only prompt survives `[]`, an old one does not (otherwise a real mismatch
would pin the row for the life of the session, and the session-idle rule covers it anyway).
(4) The 60 s age-out under a failing endpoint was dead — three failures at 12 s apart clear the
set in 36 s, so nothing ever reached 60 s — and is removed; the three-failure rule stays.
(5) In the poll, the status map is used as the second witness over the waiting set only on a
`failed` answer, and only past the point where the map itself was fetched *and decoded*, which
is also where the decode error path now marks the watcher unusable instead of silently keeping
the last state. The live test now asserts the assumption underneath that witness — at the
waiting point, every pending prompt's session is `busy` in `/session/status` — rather than
taking it on trust. On the screen side, captures at 45/60/80/100/120 columns show the button
row wrapping onto its own `┃` line below about 80 columns: the `Allow once … Reject … ctrl+f`
alternative fails at 60 and 45 while `△ Permission required` matches at every width, so the
regexp is unchanged and the two narrow cards are now verbatim rows in `TestScrapeScreens`.

## Copy and paste that a person can rely on (2026-09-04)

rev 6 fixed the two things xterm.js gets wrong about the copy and paste **keys**, and left the
rest of the path resting on tmux. That path is where copy and paste actually broke, every day,
and none of the four ways it broke were the keys.

**A plain drag belonged to tmux.** tmux tracks the mouse by default, so as far as xterm was
concerned the pointer was the program's: a drag was a stream of mouse reports, tmux answered them
by opening its own copy mode, and on release it copied into a tmux buffer. Whether that ever
reached the browser's clipboard depended on tmux emitting OSC 52, on the terminal admitting to
the `clipboard` capability, and on an async `navigator.clipboard.writeText` with **no user
gesture behind it** — which Safari refuses outright, which plain http has not got at all, and
which Chrome refuses unless the page happens to hold the permission. The documented way round it
was `Shift`+drag, and on a Mac there was no way round it: xterm 6 hands `Shift` to the program
there and forces a selection on `Alt` only behind `macOptionClickForcesSelection`, which was off.
So the pointer is now the person's. `wireMouse` holds every unmodified left press back from xterm
with a capturing `mousedown` on the host; past `SLOP` it is a selection made through the public
`term.select()` from the `.xterm-screen` rectangle, which is exactly `cols × rows` cells;
released where it was pressed it is a click, and the press and release are **replayed** to xterm
then (untrusted synthetic events, which the capture listener ignores) so a program waiting for a
click still gets one. A double click takes the word, a triple the logical line, and dragging on
from either grows by that unit. **The release copies**, inside the gesture — the one place every
browser allows a clipboard write without asking, and what every native terminal does. `Shift`,
`Ctrl`, `Alt` and `Cmd` with the button still go to xterm and the program, and
`macOptionClickForcesSelection: true` makes `Alt`+drag on a Mac what `Shift`+drag is elsewhere.

**A phone could not copy at all.** xterm 6 selects with a mouse and nothing else, and rev 4's
finger gesture was scrolling. A finger held still for `HOLD_MS` now selects the word under it,
grows the selection when it moves on, and copies when it lifts — with a **Copied** toast, because
a phone shows nothing else, and with the browser's own long-press callout and Android's context
menu cancelled so they do not sit on top of it. The release is cancelled too, so no click is
made up from a hold; a short tap is still the tap it was.

**The keys only worked while the pane held the focus.** After a click on the sidebar or a header
button, `Ctrl`+`V` produced nothing at all: the browser fires `paste` on whatever has the focus,
and that was not xterm's textarea. `document`-level `paste` and `copy` listeners now route both
to the pane unless the target is xterm's own textarea, a field of its own, or the page itself
holds a text selection. They are in the **capture** phase, which is the only place that runs
before xterm's listeners on its own textarea.

**What tmux copied never arrived.** `@xterm/addon-clipboard` 0.2.0 reads the selection parameter
of `OSC 52 ; <selection> ; <base64>` and drops anything that is not `c` — and tmux names no
selection at all, `52;;`, which the xterm spec defines as the default one. So every copy made in
tmux's own copy mode was decoded and thrown away. The addon is **no longer vendored** (6,384 B
less in the service worker's precache); `wireOSC52` registers `term.parser.registerOscHandler(52,
…)`, takes an empty selection as the clipboard, decodes UTF-8 and writes **quietly** — a write
nobody asked for must not put an error on screen when the browser refuses it, and what was copied
is kept as the middle-click buffer either way. A query (`?`) is answered with nothing: a program
does not get to read the clipboard through the pane. Two things on the tmux side had to change for
this path to carry anything: the attach client is started with `TERM=xterm-256color`, because the
terminal it is attached from *is* xterm.js and under a service manager the server's own
environment says nothing, and tmux reads the `clipboard` feature (`Ms`) from `TERM`; and
`set-clipboard on`, because the default `external` forwards tmux's own copies and silently drops
a program's. `terminal-features` gained `clipboard` for `xterm*` with it, `apply.go` sets
`set-clipboard` live when the terminal settings are saved, and `Manager.Start` sets it on a server
that is already running — the configuration file is read when the tmux *server* starts, and the
sessions outlive Socrates, so a line added to that file would otherwise reach an install that has
been up for a week only when the machine was next rebooted, and every copy made in tmux until
then would go on being thrown away.

**Two smaller ones, both found in the browser.** A right press is now held back from xterm as
well: tmux answers a right click with a menu of its own, and swallowing the report leaves the
browser's context menu — which carries Paste — to come. And a middle press pastes the last thing
selected on the page, as it has on X11 for thirty years, which turned out to arrive **twice**:
Chrome on Linux pastes the primary selection itself whenever an editable element is under the
pointer, which xterm's hidden textarea is once a right click has moved it there, and
`preventDefault` on the press does not stop it. The browser's paste lands in the same millisecond
as the click, so a 150 ms window swallows it — spent the moment it is used, and spent by any
keydown, so that a paste key pressed after a middle click is never mistaken for it.

The key bar's **Paste** button gained the same fallback, for the same reason the OSC 52 write is
kept as a buffer: `navigator.clipboard.readText()` needs a secure context and a permission a phone
over plain http has not got, and what it now falls back on is the last text copied out of the pane
itself — which is the round trip a phone actually makes, hold a line to copy it and tap **Paste**
to put it back.

The `clipboard` scenario grew from 13 assertions to 36 and `touchcopy` is new: a plain drag sends
tmux no report, selects, and is on the clipboard at release; a plain click is still reported; a
double click copies the word; a right click is not reported; a middle click pastes once; both keys
work with the focus on a header button; an OSC 52 printed by the program, through tmux, sets the
clipboard; a drag down two rows copies both; and on a phone viewport a hold selects, a moved hold grows, a
lift copies and toasts, and a tap is still a tap.

## The operator and the chat are gone; the microphone types (2026-09-04)

ACTIVITY.md §B.4 (the operator run), §D.3 (the chat panel) and the second half of §D.1 (the Agent
button) are **withdrawn**. Nothing on the session page asks a model to press keys any more, and
there is no conversation beside the terminal. What is left of those work packages is the Status
button (§D.1's first half), the ticker (§D.4, now a status line and nothing else) and the namer
(§B.5).

**Why.** The operator was the one feature here that acted on somebody's code without being told
which keys to press, and everything it needed — a step budget, a wall clock, a shell lever, a
cancel that reaches a run waiting on a busy pane, an interrupt counter — was machinery for
guessing well rather than for doing what was asked. The thing it was reached for in a car is
narrower and has none of that: say the instruction, and put the words on the prompt.

**What went.** `internal/server/chat.go` and `chat_test.go`; the operator half of `assist.go`
(≈840 lines: `agentRun`, `agentDriver`, `planActions`, the decision parser, the key vocabulary);
the four routes `POST/GET …/agent`, `POST …/agent/cancel`, `GET/POST …/chat`; the `agent` and
`chat` WebSocket frames and the two hello keys that carried them; `config.AgentSettings`,
`OpenRouterSettings.AgentModel` and the step bounds; the admin card's two agent controls;
`js/chat.js`, the chat panel and `#agentBtn`. The e2e scenarios `agent-run` and `chat-text` went
with them, and `typekeepsfocus` now leaves the pane for the sidebar row of the session it is
already on — which selects nothing and only takes the focus — instead of for the chat's field.

**The namer changed model.** It asked `openrouter.agent_model`, which no longer exists. It now
asks `openrouter.title_model`, which is the setting named for it and was until now written to the
document and never read — so the admin card offers **Title model** where it offered **Agent
model**, and the namer costs what a flash-lite call costs rather than what a Sonnet call costs.

**The microphone moved and changed what it does.** It was a pill in the chat panel's header whose
transcript was posted as a question. It is now a pill in the **top bar**, where `#agentBtn` was,
and the transcript is put into the pane through the ordinary input path — `sendInput(line, {text:
line})`, so a socket that forgot this viewer hands the words back in the words they were spoken in
rather than counting them as keystrokes, which is what `opts.text` was built for and nothing had
used yet. Line breaks in a transcript become spaces: a newline in a pane is the Enter that was
deliberately not sent. **No Enter goes with it.** A transcript is a guess about what was said, and
running a guess is the person's decision — so the words land on the prompt and wait there. The
recording sheet is unchanged, down to **Send** and **Cancel**.

`js/dictate.js` is `chat.js` with the conversation taken out: the meter, the sheet and the one
recording are what was worth keeping — plus one thing the chat did not need. A transcript takes a
moment to come back, and a session switched inside that moment would have been typed into by a
sentence that was never about it, so the recording remembers which session it was spoken to and
says so instead. On a top bar narrower than 440px the pill gives up its word and keeps its 44px,
because what makes it findable by a thumb is the ring and not the label; and at 345px the title,
which is already invisible there, gives up the 5.5em it was holding for a word nobody can read —
without which the bar overflowed by 4px at 320px, measured.

The store gained schema 5, which is a single `DELETE FROM kv WHERE key LIKE 'chat.%'`: the
conversations lived one document per session in the key/value table, and the only thing that ever
removed one was the chat.

**Verification.** `go vet ./...` and `go test -race ./...` green (the two `internal/harnesses`
Codex-rollout failures pre-date this change and are unrelated). e2e: the new `dictate` scenario,
plus `no-overlap`, `status-speak`, `status-ticker`, `design`, `typekeepsfocus`, `pages` and
`session-title`. The whole suite was run: 46 scenarios, 613 assertions, all passing.

---

## A systemd service on the host is the only deployment (2026-10-22)

Socrates runs directly on the machine as `socrates.service`, installed by `scripts/install.sh`
and updated by `scripts/update.sh`. It replaces the Docker image of 2026-09-14, whose console had
few rights on the machine and needed namespace jumps to reach it. Everything the image needed is
gone: the Dockerfile, the entrypoint, the compose file, `host-shell.sh`, `on-host.sh`, the
redeploy and migration scripts, `socrates export` / `socrates import` (`migrate.go`), the Host /
Container choice for Shell sessions (`shell_execution`, `HostShellAvailable`, the sheet's
toggle) and the on-host instructions every agent was given.

**Sessions survive an update.** Everything in a service's cgroup is stopped with the service, so
`Tmux.RunStart` starts the tmux server through `systemd-run --scope --unit=socrates-tmux` when
Socrates is a systemd service (`INVOCATION_ID` set and the parent is PID 1 — a program started
inside a session inherits the variable but not the parent). `systemctl restart socrates` then
restarts the web server alone, and the new process adopts the sessions on the socket. A scope
that cannot be made under that name is made as `socrates-tmux-<unix seconds>` (2026-10-22, see
DECISIONS revision 9); only when that fails too is it logged and the server started the plain way. The tmux server
daemonizes to PID 1, which reaps it; Socrates still installs no SIGCHLD reaper of its own, for
the ECHILD reason §A.12 gives. J7 in the risk table is answered by the scope.

**`scripts/update.sh`** and **`scripts/install.sh`** were rebuilt the same day into the
self-updating installation DECISIONS revision 9 describes: native installs of every tool, a
nightly `socrates-update.timer`, a smoke test before the swap, a version-checked health check, a
rollback that remembers the commit, and a systemd watchdog.

**Paths.** `config.DataDir()` defaults to `/var/lib/socrates` (`config.DefaultDataDir`), and
`HOME` is the service user's own (`/root`), so the CLIs keep their logins where they would
anyway. `harnesses.homeDir()` reads `HOME` and nothing else, because the e2e suite and the tests
point it at a scratch directory. The listen address defaults to `127.0.0.1:8080`.

**tmux card.** `internal/termux/detect.go` reports and installs nothing: `scripts/install.sh`
installs tmux, so a machine without it is an installation that was not finished.

**Verification.** `gofmt`, `go vet ./...`, `go mod tidy -diff` and `go test ./...` green.

## The sidebar is folders, not days (2026-09-14)

§E.10's rev 4 grouped the list by day — Today, Yesterday, This week, This
month, Older, in the browser's own local calendar — because a phone's call
list is read that way. It is the right shape for six sessions and the wrong
one for sixty: after a week of work the thing being looked for is not "the one
from Tuesday", it is "the one in the browser project", and a list sorted by
day scatters a project's sessions across five headers where none of them can
be seen together. So the day buckets are gone and the sidebar is grouped by
the directory a session works in.

1. **`daygroups.js` became `folders.js`.** `momentOf` and `movesRow` are
   unchanged and still the rule the ordering is built on; `DAY_GROUPS` and
   `bucketOf` are deleted, and `dirOf`, `folderKeyOf` and `foldersOf` take
   their place. The service-worker `SHELL` names the new file.
2. **One folder per working directory, `More Chats` for the rest.** A session
   whose directory Socrates made up for it — `workdir_mode` `dynamic`, a
   generated name under the workspace root — names no project anybody would
   recognise, so all of them share one folder instead of getting a folder each.
   A row from a server too old to have said how its directory was chosen is
   judged by whether it has one at all.
3. **A folder is named after its directory, not spelled as one.** The label is
   the directory's own name — `browser`, not `/root/browser` — because §E.10
   rule 3 keeps every technical string on this page behind a hover, and the
   whole path is on the head's `title`. Two projects with the same name in
   different places each take a parent (`one/web`, `two/web`) until they
   differ, and a folder with no more parents to take falls back to its whole
   path, which is unique by construction.
4. **The folders are ordered by the session that moved most recently in each,
   and `More Chats` is last whatever is in it.** Inside a folder the sessions
   are newest first, by the same `active_at` the rows were always ordered by,
   so nothing about "a row that jumped to the top for being looked at" changed.
5. **A folder shuts.** `.folder-head` is a button with `aria-expanded` and a
   caret that turns; the rows under it are a `.folder-rows` region that is
   `hidden` rather than removed. Shut, the head carries the count, the amber of
   a session waiting and the weight of one nobody has read — the two things
   ACTIVITY.md exists to make noticeable must not be hidden by the person's own
   tidying. Which folders are shut is kept per device in `localStorage`
   (`socrates.folders`), and only the shut ones are written down, so a project
   that appears while the browser was closed arrives open. Attaching a session
   inside a shut folder opens it: a row nobody can see is a session nobody can
   get back to.
6. **The rail keeps the hairline and the caret** and loses the name and the
   count, the way it lost the day word; the dot of a folder holding a session
   that needs an answer stays.
7. **In the drawer the head is 36px**, because it is a control now and a
   drawer is the shape a finger uses; at desk width it is the 24px the day
   header was.
8. **The e2e `daygroups` scenario became `folders`**, and asserts the grouping
   as a pure function (including the two-projects-one-name case, which a
   running server cannot be made to have cheaply) and then the drawn list:
   the order of the folders, the order inside one, an archived session in its
   own project, shutting a folder and finding it shut after a reload, opening
   it by attaching a session inside it, the folder a brand-new directory makes,
   a shut folder that says a session inside it is working and then that nobody
   has seen the end of it, and a head a thumb can hit at 390px.

**Verification.** `gofmt`, `go vet ./...` and `go test ./...` green; the e2e
suite green apart from the twelve `pages`/`design`/`twoviewers`/`dictate`
assertions this branch already failed before the change.

## Shell and VS Code are tools, not agents (2026-09-15)

§E.3 put all the harnesses in one `.seg-row` and §A.4/§D.1/§D.6 gave every one
of them the same status. Both read the five programs as five of a kind, and
they are not: three of them are agents that take a turn, and two of them are a
window onto the machine. Three changes, all of them about that one line.

1. **The sheet asks the question in two rows.** `#nsHarness` holds the agents
   and a new `#nsTools` under it holds Shell and VS Code. It is the same
   control and the same handler — one choice across both rows, so picking in
   one releases the other — and the gap between the two rows (7px) is smaller
   than the gap to the field below (16px), so they read as one question. A row
   with nothing in it is `display: none`, because an empty hairline box is a
   control that failed to draw. `harnesses.TOOLS` is the list, and
   `needsModel` already had it inline.
2. **A tool has no status.** `activityOf(session)` in `session.js` answers
   `null` for those two, and every drawing of activity goes through it: the
   ring on the row, the bold of an unread name, the roll-up on a shut folder
   head, the `Working`/`Finished` line in Info, the `aria-live` announcement
   and the chime. The detector is untouched — the server still needs to know
   whether a pane is doing anything, and `WaitIdle` still answers for a shell
   — so this is presentation and only presentation. The e2e `activity-shell`
   scenario is gone with it; `design`, `folders` and `notify` each grew an
   agent session where they used to watch a shell's row.
3. **Starting a session is not work that finished.** See the section below.

**Verification.** `go vet` and `go test ./...` could not be run in this
environment: there is no Go toolchain on the machine the change was written
on. The JavaScript was syntax-checked with `node --check`; the Go change is
three call sites and one predicate, and its table test is written.

## Starting a session is not work that finished (2026-09-15)

§A.4's rule fired on the first second of every session ever created. Every
harness is busy while it starts up and idle the moment it is ready, and that
edge is `busy → idle`: the row went bold, the chime played and the browser
raised a notification saying **Finished** about a window that had just been
opened, before anybody had asked the program for anything.

- **`track.asked()`** — has anything been typed into this pane since it was
  launched — is now a condition of the unread rule. `NoteInput` (a keystroke
  from a socket) sets it as it always did; `SendKeys` sets it through a new
  `NoteTyped`, which is `NoteInput` without the clearing of the mark, because
  an operator typing is not a person reading. `ResetActivity` clears it with
  the rest of the track: a relaunch is a new program with a new start-up.
- **The browser keys off the same mark.** `notify.completed` and the
  `aria-live` announcement both require `next.unread`, which is what the
  module's own comment always claimed they did. One rule, one place, and a
  phone in a pocket and a sidebar on a desk cannot disagree about it.
- The cost is that a turn still running across a Socrates restart finishes
  quietly, because `inputAt` lives only in memory. The alternative is
  announcing every session that starts, which is the defect.

## A white iframe is not a loading editor (2026-09-15)

`openIDE` waited for code-server's `/healthz` and then swapped the iframe in,
which is correct and is not what a person sees: `healthz` answers as soon as
the HTTP server is listening, and the workbench is about ten more seconds of
plain white after that. A white rectangle where an editor should be is the one
thing on this page that reads as broken.

The loading card — now a `.spinner` and a line of words rather than a bare
paragraph — stays **on top of** the frame instead of being replaced by it, and
`revealIDE` takes it away when there is something behind it: the frame is
served from this origin, so the test is `contentDocument.querySelector('.monaco-workbench')`,
polled every 150 ms. It fades out over 280 ms rather than vanishing, so the
workbench arrives instead of replacing something. A ceiling of 30 s uncovers
the frame whatever is in it — a page that is going to be white is better white
than behind a mark that turns for ever — and a frame that cannot be read at all
falls back to its `load` event.

## A status per session replaces the archive and the flag (2026-09-23)

§A.10's archive and the flag were two marks that said nothing about the work:
an archived session kept running for ever, and a flag meant whatever it meant
that day. Both are gone, and so is the Active/All switch. Every session has
one **status** instead, set from the row's menu under "Status":

| status | terminal | list | set by the machine |
|---|---|---|---|
| `in_progress` | runs | its project folder, no mark | on creation; when a pending or review session starts a turn somebody asked for |
| `review` | runs | its project folder, an eye | never |
| `pending` | runs | its project folder, a clock | never |
| `background` | runs, never stopped or deleted | a folder of its own, shut by default; no chime, no notification | never |
| `done` | **closed** | a folder of its own, shut by default | never; deleted `DoneRetention` (24 h) later |

- **Done parks the session** (`Manager.Park`): the tmux session is killed and
  the row is left `needs_resume`, which is exactly what a reboot leaves — row,
  journal, working directory and conversation id kept. `Ensure` and `Restart`
  refuse a done session (`ErrDone`, 409 from `/resume` and the WebSocket), so
  opening it shows an overlay instead of starting it again. "Pick it up again"
  files it as in progress, and the next open resumes it like after a reboot.
- **A sweep** (`server.sweep`, every minute and at start-up) deletes done
  sessions past their retention through the same path as Delete, and parks any
  done session that still has a terminal. The retention is read from
  `status_at`, so a restart loses nothing. Delete itself stays in the menu.
- **Back in progress by itself.** The detector files a `pending` or `review`
  session as `in_progress` on a `busy` commit, under the same `tr.asked()`
  condition as the unread mark, so a start-up, a resume or a restarted detector
  refile nothing. `background` is excluded on purpose: a service works all the
  time. Finishing a turn does **not** file anything as `review`; the unread
  mark already says that, and review is kept for sessions a person means to
  come back to.
- **The route is `POST /api/sessions/{id}/file`**, because `/status` was
  already the spoken status of the assistant.
- **Upgrade (schema 7).** `status` and `status_at` are added, and `archived_at`
  and `flagged_at` dropped. Archived sessions become `done` with their retention
  starting at the upgrade, not at archiving, and the sweep closes their
  terminals. Every other session is left unsorted (`''`) until the detector
  sees it: busy is `in_progress`; idle, waiting, not running, or still unknown
  after `SortWithin` (30 s) is `pending`.

## A session is named from its first prompt, and says so when it cannot be (2026-09-23)

**§B.6 — the moment is the first committed edge *into* `busy` after somebody
has typed**, not the first edge out of it after half a minute. This replaces
"The title run waits half a minute" (2026-09-02): `titleMinAge`, the driver's
clock and `setClock` are gone. The start-up problem that gate solved is solved
by the same `tr.asked()` test the unread mark and the status filing already
use — every harness is busy while it starts, before anybody has typed, so that
edge is published with `Activity.Asked == false` and ignored. The first busy
that *was* asked for is the harness taking up the first prompt, with the prompt
on the screen: the name arrives while the first turn runs, not after it.

- **Always English, 2 to 5 words**, never the harness's name. The screen's
  language rule is dropped: the sidebar is one list.
- **`max_tokens` 1024, timeout 30 s.** A reasoning title model (the deployed
  one was `z-ai/glm-5.3-flash`) spends its thinking out of the same budget, and
  60 tokens was an empty answer.
- **Once, whatever happens.** A failure, a timeout and an empty answer all mark
  the session `auto`. No API key is still silent and marks nothing.
- **Nothing fails silently.** The `title` frame gains `phase`
  (`start|done|error`) and `text`; the name pulses (`.titling`) between start
  and the end, and an error opens an alert dialog with the reason — which is
  how a rejected API key (OpenRouter's 401 "User not found") shows itself
  instead of a sidebar that simply never changes.

## One VS Code per folder, and every folder keeps its own shelves (2026-09-24)

**VS Code is no longer a session the sheet starts.** It is off `#nsTools`, and
`POST /api/sessions` answers 400 for it. A project folder's head carries the
VS Code mark instead (`.folder-bar > .folder-ide`, beside the head rather than
in it, since a button cannot hold one); a tap opens the folder's editor, or
asks `POST /api/ide {"workdir"}` for one. The server answers with the editor
the folder already has (200) or makes it (201), under `ideMu`, and only for a
folder some session already works in. The editor is not a row: `foldersOf`
puts it on `folder.ide`, and its mark is grey, coloured while it runs, and
pressed while it is on screen. "More Chats" has none.

- **What the editor remembers is kept per folder.** The user-data-dir is
  `<data>/vscode/<sha256(folder)[:8]>` rather than the session's directory,
  which a delete removes; a session-era `vscode-data` is moved there once. A
  folder's first editor gets a `settings.json` with hot exit and revived
  terminals on `onExitAndWindowClose`.
- **The editor's id is the folder's** (`VSCodeSessionID`). code-server keeps
  open editors, layout and recent files in the *browser's* IndexedDB, under a
  name hashed from `location.pathname` - which is `/ide/<id>/`. A fresh id per
  editor was a browser that had never seen the folder.
- **The menu of an editor** has no status and no rename, and Delete reads
  "Close VS Code": closing loses nothing.
- **Background and Done are shelves inside each folder**, not two folders at
  the bottom of the list. A shelf is `.folder.shelf[data-shelf]` in its
  folder's rows, starts shut like the old folders did, and its open state is
  keyed `<folder>\0<status>`. A folder with nothing but shelved sessions sorts
  after the folders with work in progress, and before "More Chats".

## Home, questions and the answer operator (2026-10-19)

Against HOME.md, revision 2. What the first review changed - the equal
`digest_rev` and the other 409s, the answer policy and the fixed key
vocabulary, the run ending when the answer is taken, the step limit as `error`,
revs and activity seqs from the clock, the 4401 close and the per-login
handshake ceiling - is in HOME.md itself now, not here.

- **§1.3 — "Never for tools" is `codingHarness()`** (claude, codex, opencode) in
  `digest.go`: `harnesses.IsTool` does not exist, and a list of the three agents
  is the same rule said the other way round.
- **§1.4 — the run object has no `note`**; the model's note is parsed and not
  shown.
- **§1.3/§1.4 — `modelProblem` says "home" and "question-answering"**, so the
  sentence never reads "pick a operator model".
- **§2.2 — the stream does not drive the page-wide connection bar.** The bar is
  the terminal's; Home says how its own numbers are ("Live", "Reconnecting…",
  "Offline · updated …"), and "Connecting…" before anything was heard.
- **§2.3 — a reload of `#<id>` lands on Home**, as the boot rule says; the e2e
  scenarios that reload a session go back to it through the address
  (`reloadBack` in `e2e/run.mjs`), and a new tab opens one at `#open=<id>`.
  Deleting the open session shows Home rather than the next session.
- **§2.3 — a question notification opens Home with the dialog over it**, the
  same way the address `#q=<id>` does (the worker's message, the page's own
  notification and the address all go through one function): Home is what is
  under the dialog when it closes. (This used to open the dialog over whatever
  was on the stage.)
- **§1.5 — a question is pushed from idle as well as from work**: a session's
  committed activity entering `waiting` from `busy`, `tasks` or `idle` (not
  from `unknown`, and only with the unread mark) is pushed once per prompt, and
  waits up to 8 s for the digest to read the prompt's words. "Finished" keeps
  the chime's rule.
- **§2.5 — small additions**: a second tap on a chosen option moves to the next
  item before the last one; Enter sends from the own-answer field (Shift-Enter
  is a new line in the field, typed as a space: a terminal prompt is one line,
  §1.4); the digits pick options; Close is top left, because the
  keyboard button's copy sits top right. Relative times use the browser's
  clock, not the hello's `now`.
- **§3 — the faketui repaints from the top on every change** instead of
  redrawing in place, so a narrow pane that wraps never leaves half a menu
  behind; the Codex and OpenCode question screens are its own design, not
  captures.
- **§3 — the e2e gateway stub keeps digests apart.** A server with a key now
  digests every coding session on its own, so `openRouterStub` recognises a
  digest by its first sentence, answers it "nothing asked" unless the scenario
  says otherwise, keeps it in `stub.digests` and never lets it take an entry of
  the reply queue: the scenarios that count or script their own calls are
  unchanged.

## The outcome of a turn (2026-10-20)

- **HOME.md §5 — the task list is read from `TaskCreate`/`TaskUpdate`, not `TodoWrite`.** The
  transcripts of Claude Code 2.1.252 on this machine record the task list that way (a
  `TaskCreate` whose `tool_result` says "Task #N created", then `TaskUpdate {taskId, status}`);
  no transcript here holds a `TodoWrite`, so it is not read (HARNESS-POLICY: only what was seen
  in the real program).
- **HOME.md §5.1 — the hourglass is shared.** The verdict being read is drawn with the
  background-task hourglass, as the owner chose, under its own class (`checking`), so the e2e
  selectors and the `tasks` state are unchanged.
- **HOME.md §5.4 — a turn somebody reads before its verdict is in is not pushed**, as a question
  in prose somebody has seen is not (`askedInProse`).
- **§3 — `/tasks [done]`** in the faketui works through a three-task list and records it as
  Claude Code 2.1 does; the `outcome` scenario reads the verdict from it.


## Rebuild

Deviations from REBUILD.md (binding plan of 2026-10-23), per work package.

**WP2 / §2.2 — more event types than the list, and user messages carry their text inline.**
§2.2 lists the types a UI needs; driving three CLIs and keeping exactly-once needs a few more:
`message.state` (pending → queued → delivered, which is how "queued" chips and re-delivery after a
dead host work), `part.delta` (coalesced appends; without it a streaming part would be rewritten
whole four times a second, quadratic in its length), `ask.delivered` (whether an answer reached
the agent, so a host that died in between delivers it as a message), `conversation`, `tasks`,
`usage`, `command` (so the `client_id` of an interrupt or a model change is in the journal and
acted on once) and `notice`. A user message is one `message` event with `text` and `attachments`
rather than a message plus parts, so it can never be journaled half. AGENT-HOST.md §2 is the full
list.

**WP2 / §2.2 — delta offsets are UTF-16 code units.** Live deltas and the coalesced journal lines
overlap; both carry the offset they start at, so a client applies them idempotently. The unit is
the one a browser's string length counts in.

**WP2 / §2.1 — a dead host is proven by its lock, not its PID.** The spike proposed "socket refuses
and the host PID is gone". The host holds `flock` on `chats/<id>/host.lock` for its whole life;
the kernel releases it however the host ends, and a PID can be reused. The server stops a chat's
scope only when it can take that lock.

**WP2 / §2.1 — the server keeps no copy of the journal on disk.** The spike's server kept its own
`recv.jsonl` as its resume point. Here host and server share the data folder, so the server's
resume point is the host's journal as the server read it at start; it never writes the file.

**WP2 / §2.1 — a CLI that exits on its own ends its host.** The host records the failed turn and
`process{failed}` and exits; the next command starts a new host that resumes the conversation.
Restarting the CLI inside the same host would be a second resume path next to the one parking
already needs.

**WP2 / §2.1 — `compact` is `command{name: "compact"}`, and `switch_account` is not implemented** (superseded by WP7: `POST /api/chats/{id}/account` sends `switch_account`).
Compacting is one of the agent's commands, so it goes through the one command op. Switching
accounts needs accounts (WP7); the op name is reserved in AGENT-HOST.md §4.3.

**WP2 / spike S4 §8.8 — the socket fallback is under the temporary directory.** When
`<data>/run/chats/<id>.sock` is too long for `sun_path`, the socket is
`$TMPDIR/socrates-<uid>/<hash of the data folder>/<id>.sock` rather than `/run/socrates/chats/`,
which a non-root server could not create. Both sides compute the same path.

**WP2 / §7 — filing a chat as done parks its host.** A done terminal has its pane closed; a done
chat has its host parked (when idle) and resumes on the next message. Deleting a chat stops its
host and scope and removes `chats/<id>/`; the working directory is kept as for every session.

**WP2 — the "chat routes are gone" test lost `/api/chats`.** `TestTheChatRoutesAreGone` asserted
that nothing answers there; the rebuild's chats do (`POST /api/chats`). The other old paths are
still asserted absent.

**WP3 / §3.1 — more flags than the plan's argv.** `--replay-user-messages` (the delivery receipt
§2.1 already relies on), `--forward-subagent-text` (a subagent's text and thinking reach the
stream, so its group shows what it said), `--settings` with the terminal launcher's policy keys
(`disableRemoteControl`, `cleanupPeriodDays: 90`, `skipDangerousModePermissionPrompt`,
`ultracode`), and `--session-id <uuid>` for a new conversation. `--append-system-prompt` is wired
(`Options.AppendSystemPrompt`) but empty until WP6 writes the ask_user guidance.

**WP3 / §3.1 — thinking is asked for as a summary.** Claude Code streams thinking blocks with no
text by default (`thinking.display: "updates"`); a reasoning part would always be empty. The
adapter sends `set_max_thinking_tokens{thinking_display: "summarized"}` after `initialize`;
verified live, reasoning parts then carry the summary.

**WP3 / §2.2 — one more event type, `agent.info`.** The composer's slash-command palette, the
model picker and the account divider need what the CLI says about itself (`initialize`'s
commands, models and account, the init's version and permission mode). That is chat state, not a
part of any message, so it is an event of its own (`Projection.Agent`).

**WP3 / §2.2 — the rebuild is wired through host.json.** A host does not know a chat's
conversation when its journal is gone, so the server writes the row's `cli_session_id` into
`host.json` (`conversation`). Since the WP5 integration a host whose journal names no conversation
resumes that one, and with an empty journal asks `Start` to rebuild (`StartSpec.Rebuild`) — one
mechanism for all three adapters (below).

**WP3 / §3.1 — workflow agents' calls come from their transcripts.** The stream carries a
workflow's phases and each agent's state and last tool (`task_progress.workflow_progress`), but not
the agents' calls (research/09 §3). To give every agent its own tool rows, the adapter reads
`<transcriptDir>/agent-<id>.jsonl` every 500 ms while the workflow runs. Task subagents need no
such reading: their frames are streamed with `parent_tool_use_id`.

**WP3 / §3.1 — API key and ultracode arrive as adapter variables in the environment.**
`StartSpec` has no field for either, and the Claude adapter builds the CLI environment from
scratch, so an ambient `ANTHROPIC_API_KEY` must never pass. An API-key account puts its key in
`SOCRATES_CLAUDE_API_KEY`, and the ultracode session option is `SOCRATES_CLAUDE_ULTRACODE=1`; the
adapter turns them into `ANTHROPIC_API_KEY` and the `ultracode` setting and passes neither on.

**WP3 / §3.1 — plan mode is a command.** Switching plan mode on is `Command("plan")` (off:
`Command("plan", "off")`, back to `bypassPermissions`), through `set_permission_mode`, rather than
a host op of its own; the plan card itself goes through the generic ask path as planned.
**WP4 / §4.3 — the stripped retry is a copy under a new thread id.** "Retry with reasoning and
compaction items stripped from the rollout, on a copy" cannot be done by pointing codex at the
copy: `thread/fork`/`thread/resume` with `path` refuse a paginated thread ("stale path", verified
against 0.164). The copy, which ends before the refused turn so its message is not replayed twice, is
written next to the original under a fresh UUIDv7 and resumed by that
id, so the chat's conversation id changes (a `conversation` event says so). The original is never
rewritten; codex itself appends the refused turn to it.

**WP4 / §4.3 — the chain runs on any `invalid_encrypted_content`, not only after a switch.** The
error can only come from replayed reasoning, so the adapter does not need to know a switch
happened. Each step runs at most once per app-server process and continues the same journal turn
(the failed attempt is not shown as a turn of its own). Verified against the real binary with a
scripted model that binds its encrypted reasoning to an account; spike S2 stays waived, so real
ChatGPT accounts were not tried.

**WP4 / §3.2 — messages during a turn are steered, and "queued" until codex takes them up.** The
host's semantics ("queued by the agent") map onto `turn/steer`: the message is `queued` when codex
accepts it and `delivered` when its `userMessage` item (our id as `clientUserMessageId`) shows up,
which 0.164 does at the next step, even cutting a streaming answer short. A turn that cannot be
steered (compaction, review) holds messages and sends them when it ends, and so does a turn that
ended without taking one up. A one-line message starting with `!` runs `thread/shellCommand`
(REBUILD lists it as a command; this lets the composer need no special case).

**WP4 / §3.2 — `thread/fork` is not wired; `/plan` is.** Nothing in the UI forks a chat yet. Plan
mode (`collaborationMode: plan`) is a palette command because Codex's `request_user_input` only
exists there.

**WP4 / §5.3 — Codex's own questions are declined until WP6** (superseded by WP6: they become ask_user's questions). Approvals are accepted (always
allow). `item/tool/requestUserInput` gets a JSON-RPC error and an MCP elicitation a `decline`, each
with a notice, so no turn waits for ever; WP6 replaces them through `codex.HandleRequest` and adds
`ask_user` through `codex.RegisterTool`.

**WP4 / §2.2 — a rebuilt Codex history leaves out subagent threads.** The history comes back from
`thread/turns/list` + `thread/items/list` (the WP5 integration replaced the adapter's own
`thread.json` with the host's `conversation`, so a chat whose whole folder is gone is rebuilt too).

**WP4 / HARNESS-POLICY — the app-server runs without `--strict-config` and with a per-process trust
override.** `--strict-config` would refuse to start on a `config.toml` written by a newer or older
codex, which for a chat is a host that never comes up; the two `-c` keys the adapter passes are
covered by the real-binary tests. With `approvalPolicy: never` and `danger-full-access`,
`thread/start` writes no trust entry into `config.toml` (verified, contrary to research/04 §1.4,
which used other policies), so the working directory is trusted by the same inline-table `-c`
override the TUI used.

**WP4 — token usage counts cached input apart, and there is no cost.** Codex's `inputTokens`
include the cached ones; the journal's `input` excludes them (`cache_read` has them), as Claude
reports it. Codex reports no price, so `cost_usd` stays 0 (the cost ledger keeps reading rollouts).

**WP4 — shared code.** `internal/agent/palette.go` holds `CommandInfo`, the palette entry; `main.go`
imports the adapter so that both the server and its hosts register `codex`.

**WP5 / §2.2 — queued messages wait in the OpenCode adapter, not in OpenCode.** A `prompt_async`
sent during a turn is stored by OpenCode at once and taken into the running turn at its next step,
and `abort` leaves such messages unanswered (verified against 1.19.4). The adapter therefore holds a
message sent during a turn (`queued`) and posts it after `session.idle`, one turn per message. An
interrupt ends the running turn; the queue then runs.

**WP5 / §3.3 — the adapter chooses OpenCode's message ids.** Every prompt carries a `messageID` of
OpenCode's own ascending form, which is how a journal message is matched to OpenCode's (revert and
fork by journal id, and recognising a message a dead host had already handed over). The mapping is
kept in `chats/<id>/agent/opencode.json`.

**WP5 / §3.3 — diffs come from the edit tools.** `session.diff` was always empty in research/05,
so a diff part is made per completed edit/apply_patch tool (its `metadata`), and for `write` only
when it created the file (OpenCode keeps no diff for an overwrite).

**WP5 / §3.3 — account switching is an adapter method.** `SwitchAccount(ctx, account, env)` does the
park-and-resume of research/06 inside one host; the `switch_account` host op, the
`account.switched` event and the UI are WP7's. Two API-key accounts (environment) and one
OAuth-style account (its own `auth.json`) were tested on the fake; the real binary was tested with
a real and a bogus OpenRouter key.

**WP5 / integration — one rebuild, one palette.** WP3 (host-side `agent.Rebuilder` before boot),
WP4 (Codex's own `thread.json`) and WP5 (`Start` with a flag) each wired the lost-journal rebuild;
they are now one: host.json's `conversation` (the row's `cli_session_id`), resumed when the journal
names none, with `StartSpec.Rebuild` when the journal has no message, and `Start` writes the
history (OpenCode and Codex need their live CLI to read it, Claude reads its files) followed by
`notice{rebuilt}` or `notice{history_unavailable}`. Likewise the palette: `agent.info`'s `commands`
(`agent.AgentInfo`, `Projection.Palette()`) for every adapter, the fake included, replacing
WP4's static `RegisterCommands` registry and WP5's `commands` event; Claude's `argument_hint` is
now `args`. Adapters also get the data folder (`StartSpec.DataDir`). AGENT-HOST.md §2, §5, §6.

**WP5 — `ask_user` is not wired yet** (superseded by WP6). The `MCPServers` and `QuestionHandler` hooks are in place;
until WP6, OpenCode's own question tool stays denied and a question that still arrives is rejected
with a notice, and `Answer` reports `ErrAskGone`.

**WP6 / §5.3 — Codex threads run with a granular approval policy.** §3.2 says `approvalPolicy:
never`. Verified against codex 0.164: under `never`, codex declines an MCP server's elicitation
itself, so "elicitations are converted to the same payload" could never happen. Threads now start
and resume with `{granular: {sandbox_approval: false, rules: false, skill_approval: false,
request_permissions: false, mcp_elicitations: true}}`: every permission category is rejected, as
under `never` (decision §1 holds: with `danger-full-access` nothing needs one), and only
elicitations come through, to be asked like ask_user. The model's permission instructions change
from "approval policy is never" to the list of rejected categories; the whole real-binary suite
passes under it.

**WP6 / §5.3 — the guidance is not everywhere a system prompt.** Claude gets it with
`--append-system-prompt`. A codex thread has no system prompt of Socrates': the guidance begins the
developer instructions of every thread the adapter starts (codex's equivalent), and the tool's
description repeats the rules in short for threads started before. OpenCode gets it as the MCP
server's `instructions`, which OpenCode 1.19.4 puts into the system prompt for a connected server;
no instructions file is written.

**WP6 / §2.1 — "idle excludes a pending ask_user" means a call that waits.** WP2 kept a host up
while any ask was open, so an ask left by an interrupted turn (or a `via: "message"` one) kept its
host alive for ever (the WP2 review). A call that waits on the person is always inside a running
turn or a background task, which already keep the host; a local tool's call (`mcp-ask`) does not
outlive its turn either, so its open connection does not keep the host, and the host refuses a
local tool's question asked outside a turn (it comes from a call already withdrawn). An open ask
outside them has no call to wait in, and its answer goes as a message through whichever host is
up, so it no longer keeps the host. Interrupts and recovery do not close open asks: a question
survives everything (§5.3), and the chat stays `waiting` until it is answered or the person moves
on (next entry).

**WP6 / §5.3–5.4 — a message from the person closes a question nobody waits on (`ask.closed`).**
The plan has no way for a question to stop being open other than an answer. With asks surviving
interrupts, "Stop, then write something else" left the chat `waiting` for ever, its badge and Home
card on the obsolete question, while the agent worked on what was written (the WP6 review), and
`Summary` ranked the old ask above the running turn. A user message now closes every open ask no
call waits on any more (its turn has ended, or it was meant to be answered as the next message):
the host journals `ask.closed` with the message. A message sent while the call still waits closes
nothing. A closed ask is not open (not `waiting`, not on the badge or Home), and still takes an
answer, which goes as a message: nothing typed is refused. `waiting` also now means an open ask of
the running turn, or any open ask while no turn runs: an ask of an earlier turn no longer hides a
running one. The chat card shows a closed ask as "Not answered" with "Answer anyway".

**WP6 / §2.1 — host protocol 2: one op for a local tool.** OpenCode runs its MCP servers as its own
children, so its `socrates mcp-ask` has to reach the host: the `ask` op journals an ask of the
running turn and answers it on the same connection (`answer` frame). It needs protocol 2; the
server's own ops did not change, and the server still accepts hosts of protocol 1. An answer goes
to the tool only while its connection is open and the ask's turn runs (an interrupt withdraws the
call before the tool lets go of its connection); otherwise it goes as a message. An `ask` while no
turn runs is refused. A tool's connection (`since` = `NoEvents`) gets no live deltas.

**WP6 / §5.1 and §5.2 — the shapes, made exact.** A call is checked and normalised before anyone
sees it (`askuser.Parse`): defaults applied, ids filled in, a header longer than 24 characters
clipped rather than refused, the kind inferred from the options (and the names models give kinds
mapped), option lists of plain labels taken, a `default` that fits nothing dropped; a call that
cannot be asked goes back to the model with every problem listed. §5.2 has no place for a number or
a yes/no: a confirm answer is `selected: ["yes"|"no"]` with `value: true|false`, a number answer
carries `value`, and `min`/`max` bound a number question's value too. The payload carries `part_id`
(the tool part of the call) and `source` (when the questions came from the agent's own
mechanism); an MCP elicitation's questions offer no "You decide", and a form without fields is one
yes-or-no question (accept or decline). The limits of §5.1 (20 questions, 12 options) bind the
model's calls only: a converted payload keeps every field and choice, because one cut off could not
be answered the way its mechanism requires. A question left without an id is `q<n>` by place, never
an id the model gave another one. An elicitation form has no place for a note, a general note or a
question back: when the answer has one, the form gets its values and the whole answer also goes to
the agent as a message (`ErrAskDeferred`), so the model reads what the person wrote.

**WP5 / §3.3 — `@opencode-ai/plugin` is not pre-installed.** The plan asks for the shared config
folder's `node_modules/@opencode-ai/plugin` to exist before the first serve, because a stop that
caught OpenCode's own first-start install hung (research/06 §3.5). OpenCode 1.19.4 installs it with
its embedded npm, in the background, on the first start in a fresh data folder; doing that ahead
would mean driving that installer from the install step, with network, for a package Socrates never
uses (it loads no plugin). The hang is bounded instead: every stop is SIGTERM, then SIGKILL after
5 s (`TestStopKillsAServerThatIgnoresTerm`), and a start after a cut-off install installs again.
Offline, the install fails and the server works on.

**WP5 / §3.3 — the tools an OpenCode chat runs see the layout's XDG folders.** OpenCode's shell and
bash tool inherit the server's environment, so `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`
and `XDG_CACHE_HOME` point into `<data>/agents/opencode` for them too. Caches kept there (Go's
build cache, pip's, uv's) land in the shared `cache/`, which the backup leaves out
(`paths.NotBackedUp`); npm keeps its cache in `~/.npm`, not under XDG. What tools keep in the
account's data and state homes (uv's Pythons and tools, pipx's environments) is left out too: of
`accounts/<acct>/` the backup takes only `opencode/` and `state/opencode/`, where OpenCode keeps
the account's own (`paths.BackedUpOnly`). A tool that finds its configuration only through
`XDG_CONFIG_HOME` (gh's login, git's `~/.config/git/config`) does not see the user's in an OpenCode
chat; the variables a tool has for that (`GH_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`) pass through, so
WP7, which builds the chat's environment, can set them. Giving tools the user's XDG values back
would take a plugin on OpenCode's `shell.env` hook, which was not tried.

**WP5 / §4.3 — an OpenCode gap is left to the package that needs it.** After a `limited` turn
the adapter runs its queue as after any other, so queued messages go into the account at its
limit; holding them until a switch or a "continue" belongs with the limit card and the
auto-switch (WP7). AGENT-HOST.md §10 and HANDOFF.md §4 name it. (The catch-up's `GET /question`,
the other gap the WP5 fixes found, came with WP6.)

**WP1 / REBUILD.md §6.2 — chunks are named by an HMAC of their content, not by its SHA-256.**
§6.2 says `chunks/<sha256>`. The name is HMAC-SHA256 under a key derived from the passphrase
(as the work package's brief asked), so equal content is still stored once, but the bucket's
owner cannot tell which well-known file a chunk is by hashing it. A restore checks every chunk
against its name on top of the GCM tag.

**WP1 / REBUILD.md §6.2 and §10 — no multipart upload.** §6.2 and the WP1 row ask for one. No
object is larger than a chunk (8 MiB and its envelope), so a put is one request and multipart
would be code nothing calls. What was needed is streamed downloads (`s3.Client.GetStream`),
which a restore uses; the 256 MiB cap stays on responses read into memory only.

**WP1 / REBUILD.md §10 — migration step 1 (§8) is not in WP1.** The WP1 row names it; the
brief for WP1 defers moving `~/.claude`, `~/.codex` and the OpenCode folders into the new
layout to the packages that start using it. WP1 defines the layout (`internal/paths`) and
backs up whatever is in the data directory; `profiles/` and `sessions/` stay where they are.

**WP1 / REBUILD.md §6.1 — `backup.json` holds the interval too, and `SOCRATES_S3_*` is read
once.** The interval is a field of `backup.json` rather than of the settings document, so the
Backup card is one form with one save. The `SOCRATES_S3_*` environment no longer configures
anything at runtime: it, or `s3.json` when `SOCRATES_S3_BUCKET` is unset (the mirror's own
precedence), is carried over into `backup.json` once (the bucket and the passphrase, under the
prefix `backup/v1/` inside the old one) and the card owns it from then on. "Once" is the marker
`backup.migrated`, written at the first start on a data directory that holds an installation
whatever the carry-over found, so a backup turned off stays off; a new machine that is not set
up or restored yet carries nothing over. `s3.json` is left in place for a build that is rolled
back.

**WP1 — the snapshot list reads every manifest once.** A listing of the bucket gives a
snapshot's host and time; its size and number of files are in its sealed manifest. Manifests
never change, so each is downloaded once (four at a time) and kept under
`cache/backup/manifests`, which garbage collection also reads. A new machine about to be
restored reads them all on its first listing.

**WP1 — what else the backup leaves out.** Besides §6.1's `run/`, `cache/` and `backup.json`:
today's top-level `bin/`, `tmux.sock`, `tmux.conf`, `hook.sock`, `update-state/`,
`updates.json`, `updates.log`, the schema migration copies `socrates.db.pre-*.bak`, `s3.json`,
the terminal byte journals `sessions/*/journal.raw*`, code-server's `logs` and caches under
`vscode/*/`, sockets of any name, `backup.migrated`, the working directories inside the data
directory (the workspace root, the preset directories, the default `workspaces/` that keeps a
moved root's older dynamic directories, and every session's and chat's own directory, but never
one of Socrates' own parts, `paths.Own`), and SQLite's `-wal`, `-shm` and `-journal` beside a
database (which is copied consistently instead). The list is `paths.NotBackedUp` and
`server.BackupExclusions`.

**WP1 — a notice on the session page.** The brief removes the storage banner and asks for the
24-hour warning in `/api/state` and on the Backup card; §6.2 also calls for "a notice". The
banner's slot under the top bar now shows that one warning (`js/backup.js`), with a link to the
card, and nothing else.

**WP1 — a lock on the data directory.** `socrates` and `socrates restore` take
`run/socrates.lock` (flock), so a restore cannot write under a running server and two servers
cannot share one data directory. `reexec_windows.go` went with connect mode: the tree did not
build for Windows before this change either (`internal/proc`, `internal/termux`).

**WP1 — `SOCRATES_BACKUP_WARN_AFTER`.** A Go duration that shortens the 24 hours before the
warning, for the browser suite's `backup` scenario; unset in production.

**WP1 — schema 11, not 10.** WP2 took version 10 (sessions.kind, chat_outbox) first; the
migration that drops `mirror_pending` and the `mirror.*` rows is version 11.

**WP1 — a chat's launch spec is backed up without its environment.** `chats/<id>/host.json`
will carry the environment built from the chat's account (WP7), which can hold an API key. The
backup leaves its `env` field out (`paths.Redacted`), and the host's `host.lock`, `host.log`
and socket (`run/chats/`) entirely; the journal and the adapter's `agent/` state go in whole.
The server writes the spec again before every launch, so a restored chat loses nothing. WP7:
credentials should still live in the account folders or the store, not only in the spec.

**WP8 / §7 and §9 — the browser suite keeps the agent terminals (`SOCRATES_AGENT_TERMINALS`)** (superseded by WP10: the agent terminals and the variable are gone).
Every agent with an adapter is a chat when a session is made from the sheet or prepared, so a
prepared Claude Code session in the suite became a chat run by the faketui stand-in, which speaks
no protocol (the `prestart` scenario failed on the branch before this package). The server keeps
Claude Code, Codex and OpenCode terminals for the sheet and PreStart while
`SOCRATES_AGENT_TERMINALS=1` is set, and the e2e harness sets it for every server, so the
scenarios written for agent terminals keep testing them until WP11 moves the suite to the protocol
fakes; WP10 removes the switch with the agent terminals. The scripted agent is a chat either way,
and `POST /api/chats` makes a chat of any agent with an adapter. The `design` scenario runs with
the scripted agent on, so its sheet names one program more.

**WP8 / §7.1 — the chat's controls are in its composer, not in a header of its own.** The page's
top bar already carries the session's title, the microphone and ⋮ (rename, file, Compact the
conversation, Download the conversation, delete), so a chat gets no second header: the model and
effort picker, the account slot (WP7, `useAccountPicker` in `composer.js`) and the usage "i" sit in
the composer's bar, beside what they change. Status (spoken) is hidden for a chat until it reads
the journal (WP9); it read a terminal's screen. "Export transcript" downloads the projection as
JSON (`GET /api/chats/{id}?history=1`).

**WP8 / §5.4 — the question dialog answers at a desk too.** The card in the chat shows the title,
"n of m answered" and the first open question, and its Answer opens the dialog: centred at a desk,
the whole screen on a phone and in touch mode. One place answers every kind with its details,
context, notes and counter-question; twenty questions answered inside a card would be a second
dialog. A plan's card answers the plan (Approve, Keep planning with a note) and the dialog leaves
plans out.

**WP8 / §5.2 — how kinds without options answer.** §5.2 gives `selected` and `other`; the view
puts a text question's answer and a number question's number (as typed, checked against
`min`/`max`) in `other`, a confirm question's in `selected` (`"yes"`/`"no"`), and sends every
question of the call, `skipped` when it was neither answered nor delegated. "n of m answered"
counts the questions the person had in hand (changed, or moved past); a preselected default
nobody looked at is not counted.

**WP8 / §2.3 — Stop is a request, not an outbox item.** A message or an answer written offline is
still wanted when the connection is back; a Stop is not (it would interrupt whatever turn runs
then), so Stop calls `POST /api/chats/{id}/interrupt` when it is pressed, says when that failed,
and is not kept for later.

**WP8 — an image a tool made is a file chip.** Nothing serves a chat's files to the browser yet, so
an `image` part shows its name, with the path behind the "i", rather than the picture.

**WP8 / §7.1 — the composer's draft over an empty one is not a conflict.** The composer keeps
PreStart's revision rule, with one difference: a text this browser wrote on an older revision is
saved over the server's when the server's draft is empty, rather than asking which to keep. The
composer's draft is emptied by every message sent, so "changed on another device" against an
empty text would be asked after most sends from another device, and there is nothing in it to
lose. A text typed before the saved draft has loaded is this browser's own text, by the same rule.

**WP8 / §7.1 — Home's Questions card walks one kind of question at a time.** A walk that starts at
a chat's question goes through the chats' questions in their dialog (`askdialog.js`); one that
starts at a terminal's goes through the terminals' in theirs (`question.js`). Joining the two
walks would be work for a dialog WP10 removes with the agent terminals; until then the other
kind's questions are reached from their sessions, or from the card once the oldest kind's are
answered.

**WP1 / REBUILD.md §6.2 — "point to folder" is on the session's menu, not on the restore page.**
§6.2 lists the missing working directories per chat after a restore. The restore page ends in a
restart and the sign-in, so the list is where the sessions are: `GET /api/sessions` marks a row
whose folder is not on the machine (`workdir_missing`; not a prepared session's typed-in folder,
which is made when it starts), its ⋮ menu offers **Point to folder**, and
`PUT /api/sessions/{id}/workdir` takes a folder that exists (a preset, or a typed-in one under the
new-session rules) and parks a chat's host so its next message starts there. The chat view's
dock says the same with the same action; it shows once WP8 mounts the view.

**WP1 — a backup waits for an installation, and needs the database.** Not in §6.2: a data
directory that holds no installation (no password, no sessions, nothing in its folders) or whose
restore is staged is not backed up, and a `backup.json` found on one gets a host id of its own; a
snapshot without `socrates.db` is not written, "latest" is the newest snapshot that holds it, and
one without it cannot be restored. Otherwise a new machine, started before its restore, wrote an
empty snapshot that "latest" restored and that retention counted as the old machine's newest.

**WP1 / REBUILD.md §6.2 — the warning is about more than a day without a snapshot.** §6.2 warns
after 24 h without a successful snapshot, "nothing else". A run can succeed while what it took,
or the bucket, is not well, so the same warning, after the same time, also names a database
whose earlier copy has stood in for it, and a garbage collection that has kept failing: an
object under `snapshots/` that is not named like a snapshot stops it before a chunk is deleted,
and the bucket then grows without limit while every run says "Backed up". The Backup card shows
a collection that failed at once (`gc_error`), and `warning_about` says which warning it is.

**WP9 / §7 — a chat's line on Home is its journal's words, not a model's summary.** "Read the
journal instead of the screen" could mean handing the journal to the home model. It does not: the
line is what the journal already says (`chatLine`, AGENT-HOST.md §3): the question while the chat
waits, the call that runs or the text being written while a turn runs, the first line of the last
answer between turns. It costs nothing, is current with the event that changed it, and needs no
key. The model is still asked where a judgement is wanted: the title, the spoken Status and the
outcome.

**WP9 / §7 — a chat is a digest on the event stream, with `source: "ask"` and a `news` field.**
Home, the sidebar, the page's notifier and the badge already read digests, so a chat's reading is
one (`phase: ready`, its line as `summary`, its open ask as `question`) rather than a frame of its
own: the browser contract is unchanged. Its question's `source` is `ask`, which no terminal path
reads as a prompt or as a question in prose. The optional `news` (what a notification about the
chat says now) is new; a terminal's digest has none.

**WP9 / §5.4 — a turn the person stopped is not pushed.** A terminal's turn that ends after Esc is
"Finished" like any other. A chat knows that somebody pressed Stop (an interrupt or stop command
journaled during the turn, `Turn.Stopped`), and that person has seen it end; neither the push nor
the page's own notification says so, and with outcomes on it is not judged (a verdict would be a
model call for a notification nobody sends). A turn interrupted by anything else (a host that died)
is pushed as "Stopped · …".

**WP9 — answering a chat's question and Stop mark it read.** Typing an answer into a terminal
cleared its unread mark; a chat's answer went through a route that did not, so the mark set when
the question was asked stayed, and the turn the answer let go on was never news (no chime, no
notification, no push). Both routes now mark the chat read, as sending a message does.

**WP9 / §7 — a chat's verdict is a call of its own.** A terminal's outcome is asked in the same
read as its digest; a chat has no digest read. Its turn end puts the same outcome on pending and
`judgeChat` asks the home model alone, with the outcome rules the terminal's read uses
(`outcomeRules`, factored out of `outcomeSystemPrompt` without changing that text) and a system
prompt of its own that opens with `You judge the turn a coding agent has just ended`, so a stand-in
gateway can tell it apart.

**WP9 / §4.4 — one source per chat for its cost: the journal.** REBUILD.md §4.4 says the ledger
keeps scanning the shared transcript folders and the journal's account makes the split. A shared
`projects/` folder cannot say which account wrote a request, and counting a chat from both would
count it twice, so a chat is counted from its journal alone, in a ledger named by the chat and one
per account; the conversations its journal names are the chat's for good (the scanner skips them,
and a ledger it made of one before is removed), and everything else in the shared folders - which
are now scanned as a root - is counted from its transcript as before. Codex's journal has tokens
and no price, so its tokens are priced with the ledger's Codex rates; a Claude chat's cost is the
CLI's own `cost_usd`, per turn rather than per request.

**WP9 / AGENT-HOST.md §10 — a brief per chat, and its history only while it is used.** The server
no longer keeps every journal in memory: `chats/<id>/summary.json` (written by the server, left
out of the backup) holds what Home, the badge, the push and the pump need, and is believed only
while the journal still ends at its seq; the history is read on demand and let go after ten minutes
unused. A chat's brief is part of its folder, which the host otherwise owns: it is the one file
there the server writes besides `host.json`.

**WP9 / §2.2 — a lost chat folder is rebuilt when the chat is opened.** The rebuild path was in
place (the row's conversation in `host.json`); what was missing was a host. One is started when
the chat's stream is opened and its journal is empty while its row knows a conversation, rather
than for every such chat at start, which after a restore could be hundreds of agents at once.

**WP9 / §3.1 — Ultracode is a field of `host.json`, and changing it parks the agent.** The server
does not build a chat's environment (WP7 does), so it does not put `SOCRATES_CLAUDE_ULTRACODE` into
`env`: `host.json` says `ultracode`, and the host adds the variable to whatever environment the CLI
gets - the account's, at the start and with every `switch_account` - and takes away one the
environment brought, so an ambient value never decides. The option
is a launch setting of the CLI; switching it in a running Claude chat's ⋮ saves it and parks the
agent as soon as it is idle, so the next message resumes the conversation with it.

**WP9 / §7 — a chat is named at its first turn, from its first message.** The terminal rule (name
at the first turn somebody asked for) holds for a chat: the namer is given the first message and
whatever the first answer has said by then, which is usually nothing. Waiting for the first answer
would leave a long first turn under its placeholder name for its whole length.

**WP9 / §7 — Status for a chat has two phases on screen.** A terminal's Status reports its phases
on the terminal socket, which a chat does not have; the chat's dock shows the page's own first
phase ("Reading the chat") and then the answer, which is read out as before. The notice with the
whole answer and its details (state, model, language) is in the dock too, above the ticker: a
terminal has both over its pane, which a chat hides.

**WP9 — the server's test binary registers the three agents' adapters.** To test Ultracode, the
prepared chat's start and the lost-folder rebuild through real hosts for Claude, Codex and
OpenCode, `internal/server`'s test binary imports their adapters (with fakeclaude, fakecodex and
fakeopencode on the PATH) and, like the browser suite, sets `SOCRATES_AGENT_TERMINALS=1`, so that
its many tests of agent terminals still make terminals; a test that wants a prepared Claude chat
unsets it for itself.

**WP7 / §4.3 — the server decides an automatic switch; the host only carries it out.** §4.3 has
"the host switches to the recommended account". The host knows neither the other accounts, nor
their limits, nor their keys; the server has all three. So at a `limited` turn's end the server
picks the account (the recommender of the Accounts card) and sends the same `switch_account` a
person's "Continue with" sends, with `continue`; the mechanics (interrupt, park and relaunch, or
OpenCode's `SwitchAccount`, the divider with its reason) are the host's, as planned.

**WP7 / §4.3 — "re-send the interrupted turn" is a message that says to go on.** After the switch
the new account is sent "Continue where you left off." (`agenthost.ContinueText`), a user message
with its own `client_id`. The conversation is resumed with the stopped turn in it, so the agent has
what it was doing; sending the person's message again would put it in the conversation, the
journal and the chat twice, and would repeat whatever the turn had already done.

**WP7 / §4.3 — the account sits in the composer's bar, not in a chat header.** "The chat header
shows the account next to the model": in the chat view (WP8) the model is in the composer's bar,
so the account is beside it there, through `useAccountPicker`. It shows for an agent with two
accounts or more; with one there is nothing to pick. "Switch now" during a turn interrupts and has
the new account carry the turn on (`continue`); "After this turn" waits for it.

**WP7 / §4.1 — more fields than the list.** The accounts table also keeps `state` (signed in, needs
sign-in, failed) with why, the OpenCode provider of a key, `origin` (the folder the migration moved
the login out of; what makes the migration idempotent and what keeps such an account from being
deleted while HOME links to it) and `limited_until` (when a limit it ran into renews, so the
recommendation passes over it). A limit that does not say when it renews is held for an hour.

**WP7 / §4.2 — Claude Code signs in with `claude auth login --claudeai`, from the card.**
The flag is the subscription sign-in, which is what an account of that kind is; without it Claude
Code first asks which kind. It first ran in a Shell terminal; it now runs on a pseudo terminal of
the server's, and the card shows its page and types the code pasted there into it, like the other
agents' sign-ins. Its outcome is its exit status and last line; the account's state follows its
folder.

**WP7 / §4.2 — OpenCode signs in in its own TUI, in a Shell.** It first relayed the providers'
OAuth methods from `GET /provider/auth` of a serve of the account's own into the card; that
wrapper offered less than OpenCode itself (only some providers and methods, none of their
follow-ups), so "Sign in" now opens a Shell terminal running `opencode` on the account's data
home instead, where `/connect` signs in to any provider, and stays a shell when OpenCode is quit.
The account's state follows its `auth.json`. A key is still an account of its own kind, passed per
process as `OPENCODE_AUTH_CONTENT` (§4.1).

**WP7 / §4.1 — a Codex key is kept sealed as well.** Codex logs a key in itself (`account/login/
start {type: apiKey}` into the account's `CODEX_HOME`), and that file is what codex reads. The key
is kept sealed in the store too, like every key, so that "Replace the key" and a restore have it.

**WP7 / §6.1 — an OpenCode account's folder is `agents/opencode/accounts/<id>/`.** WP5's layout
keeps every OpenCode account's data home under OpenCode's own folder (one shared database, each
account its own `auth.json`); Claude Code's and Codex's are `accounts/<id>/` as planned.

**WP7 / §4.2 — the keys are sealed with a key file in the data folder.** "The existing vault": the
vault's AES-GCM, with a new key, `<data>/secrets.key` (0600, made once), since the vault's only key
so far was the backup's passphrase, which the server does not hold. The file is backed up with the
store (a restore needs both); `host.json`'s `env` and `secret_env` are not. A chat's key variables
are sealed again for that chat, so `host.json` and the outbox hold only ciphertext.

**WP7 / §8 step 1 — the folders are renamed and the old paths become links.** "Move" is a rename
(the same inodes, never a copy: two copies of an OAuth login rotate each other out, which is what
logged the person out every few minutes), and `~/.claude`, `~/.claude.json`, `~/.codex`,
`~/.local/share/opencode`, `~/.config/opencode` and every profile's folders become links to where
they went, so a terminal or a CLI started by hand uses the same files. Claude Code writes
`.claude.json` through the link (checked against 2.1.309). A folder on another file system is not
moved (a rename there is a copy): that account says "sign in here". The Default OpenCode account's
database becomes the shared one; a profile's own, when there is one, stays in its folder unread.
What would collide in a shared folder is kept beside it as `<name>.pre-rebuild`. Steps 3 and 4
(the agent sessions' clean cut, the old S3 objects) are WP10's.

**WP7 / §8 step 2 — a lost profile becomes an account for each agent its sessions ran.** A profile
held all three agents, an account is one; a profile whose folders are gone becomes a "Sign in
again" account for each agent its sessions used, and a Claude Code one when it has none.

**WP7 — deleting an account removes its folder.** Socrates never writes, refreshes or deletes a
login, except here: an account is its folder, and the person asked for it to go. The server
refuses while a chat runs on it, and for an account the migration moved in from HOME, which HOME's
links point at (one made of a profile goes, with the link left in the profile's place).

**WP7 — a chat that names no account gets one at launch.** A chat made by a client that does not
send `account` (the API, PreStart's "Automatic", a chat from before WP7) runs on its agent's
recommended account, else its first, else a new "Default" subscription that has to be signed in;
the choice is written to the row. A fresh install has no account until then.

**WP7 — the server's tests keep the agent terminals.** `internal/server` now registers the Claude
Code, Codex and OpenCode adapters (through `internal/accounts`), which makes those three chats in
its tests; the tests written for their terminals set `SOCRATES_AGENT_TERMINALS=1` in `TestMain`,
as the browser suite does.

**WP7 — the dashboard's Profiles card is gone; the profiles stay for terminals.** Accounts replace
profiles for chats (§4.1). The terminal-only plumbing (`/api/profiles`, the sheet's profile field
for an agent terminal, `harnesses/profiles.go`) stays until WP10 removes the agent terminals;
taking it out now would widen this package into theirs.

**WP7 review / §8 — the migration runs only on a data folder of the build before.** "On the first
start of the new version" read as "on every data folder without a marker", so a new one (a trial
`-data /tmp/x`, an e2e run in live mode, `node e2e/shots.mjs`) took the machine's real logins into a
folder that was deleted afterwards. The store now marks a database it brings up to schema 12 from an
older one (`kv accounts.migration`, written before the new version, so no crash leaves an old
database upgraded without it); only a marked data folder migrates, and finishing clears the mark,
which replaces the `accounts.migrated` file. A data folder in the temporary directory never takes a
folder from outside it, so a rehearsal on a copy of `/var/lib/socrates` there leaves HOME alone.

**WP7 review / §8 step 1 before step 3 — the moves are atomic instead of waiting for the clean
cut.** §8 lists stopping the old agent terminals (step 3, WP10) after the moves, and the tmux scope
survives a restart, so the moves run under live agents. Each move is now one step on Linux
(`renameat2(RENAME_EXCHANGE)` of the folder with a link made at its new place), so an agent writing
by path never finds the old path missing or makes a second folder - and a second login - there; the
shared folders are swapped for their links before they are merged, so a recreated `projects/` can no
longer block the account for good. WP10 should still run the clean cut before `accounts.Migrate`.
This uses `golang.org/x/sys/unix`, already in the module graph (modernc.org/sqlite needs it); go.mod
lists it as a direct requirement now.

**WP7 review / §8 — one folder that cannot move holds up nothing else.** A failing source is logged
and tried again at the next start (the mark stays) while the others move. A link of the person's own
to somewhere else (dotfiles) is left like a folder on another file system: its account is made and
says why, and `~/.claude.json` stays beside it. A lost profile's account has the origin
`lost:<profile>:<agent>` (it moved nothing in), and can be deleted.

**WP7 review / AGENT-HOST §4.3 — a switch after the turn is acknowledged twice.** It lived only in
the host and was acknowledged at once, which emptied the outbox: a host that died before the turn
ended lost it while the composer kept announcing it. The ack now says `pending`, the server keeps the
command until the host's second, final ack (made, or replaced by a newer switch), and a host launched
meanwhile starts on its account. The field is additive; the protocol version stays 2. A switch
without `env` is refused rather than run on the environment of the account the chat is on.

**WP7 review / AGENT-HOST §10 — Codex holds its queue after a limit too.** §10 named OpenCode; Codex
had the same cause (the messages it never took up were started at once, each into the account at its
limit). Held messages no longer count as a turn an "after this turn" switch waits for.

**WP7 review / research 08 §3 and 06 — the divider's "i" and a refused sign-in.** The divider says
who the new account is, that the next reply may be slower, and Claude's "can't use thinking from
another organization" as a hint (`notice{foreign_thinking}`, folded into the divider by the chat)
instead of a warning line. An `error{auth}` (OpenCode's ProviderAuthError or a 401, among them a
refresh token two chats renewed at once) marks the account "Sign in again" although its `auth.json`
is still there, until a sign-in, a new key or a completed turn on it.

**WP7 review — the e-mail stays behind the "i".** The quiet line beside an account's name in the
sheet, the picker and the Admin list said the e-mail; it says the kind now, and the e-mail and the
plan are in the account's "i" and the divider's.

**WP7 + WP9 / §4.3 — a chat's limit is one notification, the accounts'.** WP9 pushes every chat
turn's end in its journal's words, a limited one as "Limit reached · <why>"; WP7 pushes a limit
itself, with when it renews and on which account, once per account it is stopped on, and not at all
when the chat's automatic switch moved it on. Together a limit was pushed twice, and once more as
news after the automatic switch had already dealt with it. A limited turn of a chat whose journal
names its account is now no news of the turn's end (the brief's `news` is empty, `newsOf`), so
neither the turn-end push nor the page says it; the accounts push it, skipping a chat filed
Background as every turn's end does, and tell the pages in the same words by a `news` frame on the
event stream (HOME.md §1.2), which the page's own notification and chime say on a device without
Web Push. It is still judged with outcomes on (`chatHub.judgeable`): how far the turn got is worth
knowing, and the verdict pushes nothing more. A chat's turn end whose `news` is empty - a turn the
person stopped, too - is not heard on the page either: the chime and the notification are one piece
of news.

**WP7 + WP9 / §4.4 — a chat's turns are on the account they ran on from the first turn.** WP9's
ledger learnt a chat's first account from its first switch's `from`, so a chat that never switched
had its cost on a ledger with no account. Every `turn.started` names its account now (WP7), and the
ledger puts each turn there (`chat-<id>@<account>`); the costs page names the accounts. Only a
journal from before accounts still has turns with none.

**WP7 + WP9 / §5.4 — a switch made now stops the turn as a Stop does.** WP9 pushes and judges an
interrupted turn unless somebody asked it to stop (an `interrupt` or `stop` command journaled while
it ran); WP7's "Switch now" interrupts the running turn too, at the person's word, and the new
account carries it on. Such a turn was pushed as "Stopped · …" and judged, a moment before the
turn that carries it on. A `switch_account` journaled while a turn runs now marks it stopped, unless
it waits for the turn - the journaled command says its `when` in `args` (`after_turn`), an additive
field of the command event - or goes to the account the turn already runs on: the host is there and
interrupts nothing (a host launched on the account of a switch still in the outbox, which reaches it
while the first turn runs).

**WP10 / §8.3 — the lost profiles' accounts are made before the clean cut, and the cut before
the moves.** The brief puts the clean cut first; the lost profiles' accounts (a database write,
nothing moves) come before it, because which agents a lost profile ran is read off its sessions -
the very rows the cut removes - and a start killed after the cut would otherwise make every lost
profile a Claude Code account. A rerun also keeps the agents an earlier run made a lost profile's
accounts for. The backup tar is written when there is anything to cut, as it is when there is
anything to move.

**WP10 / §8.3 — "simple and lossless", spelled out.** A prepared agent terminal becomes a prepared
chat in place (`Store.MakePreparedChat`): its agent, folder, model, effort, schedule, Ultracode and
draft stay. Its profile becomes its account once the moves have made the accounts: the default
profile the agent's Default account (none: the chat makes one when it starts, as any chat does), a
named profile its `profile:` or `lost:` account, one that left the profile to its start an account
left to its start. Only a named profile with no account of that agent loses something - the
sign-in it was prepared for - and that prepared session is removed and logged.

**WP10 / §F.7 — the agents' state rows left the diagnostics.** "Claude Code state", "Codex state",
"OpenCode state" and "Claude Code sign-in" checked the folders an agent's terminal needed (a
writable `CLAUDE_CONFIG_DIR` for the theme pin, the Codex and OpenCode databases for discovery,
Claude Code's first run). A chat runs on an account's folder, and the Accounts card says each
account's state, so the rows went with the terminals; the program rows stay.

**WP10 / DESIGN rev 11 — the usage strip is gone.** Its limits were a sign-in profile's, read for
the session on screen; a Shell has no limits of its own, and a chat says its usage under its "i"
and in its account picker. The strip, its dialog and `GET /api/sessions/{id}/usage` went with the
profiles.

**WP10 / brief — the Shell's process tree is its busy, not "tasks".** The brief keeps "busy/idle
of a shell, its process tree for 'tasks', exit status". The Shell's layer reads its foreground
command and whether the shell has a child, which is busy - a script run by `bash` - as it always
was; no Shell ever reported `tasks`, which only an agent's turn has. VS Code keeps quiescence, a
dead pane is unknown with its exit status, and a terminal's resume note is never "fresh": it has
no conversation to lose.

**WP10 / e2e — three Shell scenarios no longer start an agent.** `exitoverlay` ends a Shell with
`exit 7`, `backpressure` prints its two hundred lines from a shell loop, and `lighttheme` keeps the
palette part only (the white pane and the sixteen colours drawn at 4.5:1): the Codex theme flag it
also checked is gone with the agents' terminals, and the white answer to a program's colour query
is termux's `TestOSCRespondsWhite`.

**WP10 / §8.4 — "once the first snapshot exists" is this machine's snapshot.** The Backup card
offers the mirror's objects for deletion once the backup holds a snapshot whose host is this
machine's (read off the snapshot listing, which opens and starts nothing), and only for a backup
whose prefix ends in `backup/v1/` - the one a mirror's configuration was carried over into; a
prefix of the person's own has no known mirror beside it.

**WP10 — a draft saved during a prepared chat's start is a conflict.** The test of a save racing
a start expected "started"; a prepared session is a chat now, whose draft is its composer once it
runs, and its start takes the first message out of it, so the late save is answered as the
conflict it is with what the composer now holds.

**WP11 / §9 — three scenarios did not come over to the chats.** "Port every ADAPT scenario" with
the same intent; three have none left. `tasks-opencode`: OpenCode's subagents run inside the turn
that started them, so an OpenCode turn never ends with work still running and its adapter reports
no background tasks (`tasks-claude` and `tasks-codex` cover the hourglass). `home-unread` and
`home-language`: both were about a question read off an agent's screen in prose, by the model, in
the person's language; a chat's question is its agent's own ask_user call, in the agent's words,
and nothing reads a screen. Inside ported scenarios the same reason took OpenCode's permission card
(a chat allows every permission and says so in a notice, which `home-opencode` asserts instead),
the old dialog's "Another device is answering" step (a chat's answer is taken by the server at
once, so the second device goes straight to "Answered on another device"), and the questions asked
in prose in `home-quiet`, `home-kinds` and `home-restart` (replaced there by a second chat's
ask_user question, the plan's card, and a third question).

**WP11 / §9 — `livesession` types nothing.** The terminal version typed `/status`, which cost
nothing in the TUI; in `claude -p` a slash command is a message, and whether it reaches the model
is the CLI's to decide. The chat version asserts what the CLI says without a turn - its answer to
initialize, with its version and its commands - and that no turn ran.

**WP11 / brief — the README pictures are five, and the fakes take them.** `shots.mjs` used the
real CLIs whenever all three were installed; it now uses the protocol fakes, and takes the Admin
pictures against the real CLIs only with `SOCRATES_LIVE_AGENTS=1` (it starts no chat with them).
The pictures are a chat at a desk, Home, a question on a phone, Admin's accounts and Admin's
backup (`screenshot-home.png` and `screenshot-backup.png` are new). The people in them write in
words: `fakeclaude` runs a script for a message the `claude.scripts.json` beside it names (a file,
because a chat's CLI gets no variable of the suite's), and its `/readme` script is the turn the
desk picture shows.

**WP11 — what the ported scenarios found, fixed in the product.** Each was a scenario that failed
for a reason in the code, not in the test:
- A chat started without a model ran on the CLI's own default, not Admin's default model and
  effort (DECISIONS "Admin (the basics only)"): `POST /api/chats` and a prepared chat's start fill
  them in, and the catalogue hands them to the sheet.
- Codex's background terminals were not reported, so a turn that left one running was "finished":
  the adapter polls `thread/backgroundTerminals/list` every 2 s after a turn and reports the count
  as tasks.
- Status's phases reached only a terminal's viewers; a chat's page has no terminal socket, so its
  ticker heard nothing. They go on the event stream too.
- A chat filed Done that was sent a message stayed done, and the sweep would have put its agent to
  rest mid-turn; a message picks it up again, as "Pick it up again" does a terminal.
- A chat's unread mark was lost across a server restart: its host reported before the detector had
  read the marks back. `ReportActivity` reads them first.
- A Shell's pane that tmux listed as dead before it had the exit status was marked "Exit status
  -1" by the poll on a busy machine. The cause is tmux up to 3.5 built with libutempter, which can
  lose the program's SIGCHLD; origin/rebuild's fix is cherry-picked (the review pass below): the
  poll leaves such a pane to the pane-died hook and sends the server the SIGCHLD that makes it
  collect the status.
- The account picker's renewal time was drawn once; it follows the clock (every 15 s), and a
  window whose reset has passed counts as empty.
- The question dialog: a question asked for (a tapped notification, `#q=`) while the page could
  not read it opened another chat's question or said there was none; it now waits for the
  connection (three tries in two minutes) and opens the one asked for. A double tap chose the
  option drawn under the second tap, and a phone on its side showed the first option under the
  bar: a new question takes no tap for 600 ms (a key is never a stray tap), and below 500 px the
  question is scrolled to the top. What was asked while the dialog was open joins the walk at its
  end. And the dialog had lost the connection line HOME.md gives it ("The dialog says it too"); it
  has it again, with "This question may already have been answered" - except for the page's first
  "Connecting…", which HOME.md lists too: the chats' dialog reads its questions from the server as
  it opens, so on a page that has not connected yet they are as current as they can be, and the
  line would only push the first option off a phone on its side.

**WP10 + WP11 review — what the fix pass changed against the plan and the briefs.**
- §8 "a backup of the old data folder first": the tar is that now. Its database is the copy the
  store made before it upgraded the file (`store.PreUpgradeCopy`, `socrates.db.pre-v12.bak`),
  because the build before refuses the upgraded one; and it keeps what that build reads and the
  backup engine leaves out - the mirror's `s3.json` and the agent terminals' `sessions/<id>/`
  folders with their journals, which the clean cut then deletes. HANDOFF §3 says how to go back.
- §8.1, OpenCode: `opencode.db` moves to the shared `agents/opencode/opencode.db` as before, and a
  relative link to it is left where it was, so an `opencode` run by hand in a Shell opens the same
  database (AGENT-HOST.md §11).
- §8.2: the lost profiles' accounts are made first (see above) and then put below every other
  account, so no agent's list offers a "Sign in again" account above its Default one.
- §8.3, the brief's "log what was removed, once": each agent terminal is said as it is removed,
  and a delete removes the session's folder before its row, so a start killed in the middle
  leaves a row the next start removes (and says), never a folder no row leads to.
- The sheet refused every agent on a machine whose tmux was too old; only the Shell needs tmux.
- `POST /api/sessions/{id}/digest` ("Read again" of the dialog WP11 removed) is gone with its
  helpers; the resume note's `fresh`/`resumed_from` of a terminal went with the agent terminals.
- origin/rebuild's fixes for a lost exit status (e1d0745, 83e82da, 1a388e4) and for a cut-short
  backup upload (29f7bd3) are cherry-picked; wp10's own exit-status change (785b0f6) is reverted
  in their favour, so the merge meets one version of those lines.
- DECISIONS rev 7, "a tap says exactly when": the account menu and the sheet's account list say a
  limit's renewal by the clock as well ("today at 15:42", "on Tue at 09:10"), the menu's lines
  wrap instead of being cut, and a week's countdown reads in days.
- The chat names a file a call worked on relative to the chat's folder when it is inside it (the
  whole path stays behind the call's "i"), and a diff of an absolute path keeps its leading slash.
- The default workspace root follows `-data` (DESIGN.md said `<data>/workspaces`; it was the
  environment's data directory whatever `-data` said).

## Phone pass (2026-10-24)

The phone pass (branch `mobile`, merged into `integ` after WP10 and WP11) audited every screen at
390, 360, 430 and on its side in touch mode, fixed what it found, and was audited twice again;
the four final audits (a desk, a phone, a deploy rehearsal, the real binaries) followed on
`integ`. What departs from REBUILD §7.1, DESIGN.md or DASHBOARD-STYLE.md:

- **Glyphs from Lucide.** Where Dashboard-Style 1.6.2 has no glyph a phone needs (microphone,
  stop in a circle, keyboard, upload, clipboard, Enter, paperclip, speaker, terminal), `ui.js`
  `LUCIDE` holds Lucide's, drawn by DS's own `icon()`; `GLYPH` names what each control means. Each
  goes when DS ships it (DASHBOARD-STYLE.md, "Icons").
- **Icons rather than text buttons** on the top bar, the composer, the key bar, the question
  dialog and a failed message (Status, the microphone, Keys, Read aloud, Ask back, Back, Open
  chat, Copy, Paste, Upload, retry and edit), each with its name and its tip. On a phone at 480 px
  and below the session's status and the outcome beside the name are their glyph alone too.
- **Touch mode's fields.** A tap on any field but the terminal's types at once; a focus the page
  gives by itself (a dialog opening on its field, the composer of a chat that opens) raises no
  keyboard; a button that brings a field to type in (Rename, Other, Add a note, Anything else,
  Ask the agent back, Keep planning, Custom path) gives it the keyboard with that tap and
  brings it into sight (`keyboard.js` `typeHere`). The keyboard switch is the terminal's alone.
- **A session's menu on a phone is an action sheet** under the session's name, and a menu that is
  cut short at a desk fades at its foot.
- **Snackbars** stand above the dock, the key bar and a keyboard, and a dialog keeps the strip
  they stand in.
- **What a turn cost is said once**, under its last answer (OpenCode answers a step at a time),
  and a turn's end ("Interrupted", "Failed") stands under that turn, above a message sent while it
  ran; a limit's card stays the chat's newest word.
- **A model "change" to the same model draws no line**: the CLI naming the alias's full id at a
  first turn or after a switch is not a change.
- **A program that could not list its models** keeps the sheet's model field, a plain one with
  no list's arrow, for an id typed in or nothing (its own default); Admin says so in words, the
  program's error behind the "i".
- **A question stands where it was asked.** An ask that names its tool part (`part_id`, §10 of
  AGENT-HOST.md) is drawn at that call inside the answer, and its own timeline entry draws
  nothing; one that names none cuts its turn's answers where they were, and what they say after
  it is drawn under its card (`chatfold.js` `cutAtAsk`). The reply to a question is never above
  it, and Copy and the "i" go under the last piece that says anything.
- **Costs lists a chat once**, whatever accounts it ran on; the Account table splits its spend.
- **Read aloud needs a Text-to-Speech key** and says so before anything is sent
  (`/api/preferences` `tts`), as dictation, Status and naming do for OpenRouter's.

## UI pass (2026-10-25)

Asked for by the product owner on 2026-10-25, on branch `ui-simplify`.

- **PreStart is gone** (REBUILD §7, DESIGN §L superseded). A chat that has nothing said yet is
  its first message: the composer becomes a big field that fills the chat (`.so-chat[data-fresh]`),
  its text the chat's draft as before (this browser at every keystroke, the server after a pause).
  Creating a chat creates it as before (`POST /api/chats`); there is no prepared state, panel,
  shelf, Home card or sheet switch. The server turns every prepared row into such a chat at start
  (`readyPreparedChats`): its folder resolved as a new chat's is, its draft kept, a schedule still
  in the future kept; nothing is sent and no host started.
- **The scheduled start stays, on the first-message page** (the owner: "keep that function on the
  start page with the big input field"). `PUT /api/chats/{id}/start_at`; at that time the server
  sends the draft as it stands then through the ordinary message path (client_id
  `scheduled-<id>-<start_at>`), empties the draft, journals `notice{scheduled_start}` and pushes
  "Started"/"Scheduled start failed". A failure is not retried. A message sent by hand clears it.
- **A turn's work is folded.** Thinking, calls, agents, diffs, task lists and what the agent said
  on the way are one line per answer (`.so-work`): what it did, how long, steps, tokens and cost;
  a tap opens it. The answer's last words, plans, notices and question cards stand. The pinned
  task list above the composer still shows while a turn works through one.
- **A line under the top bar** (`.so-chat__sub`): the chat's account, each limit window's use and
  reset, and the background jobs - always shown, "No background jobs" when there are none; a tap
  lists them.
- **Send now** on a message queued behind a turn stops the turn (the owner's choice over steering
  it into the running turn), so the queued message goes next. Cancelling a queued message was
  dropped: the CLI holds a message once it is handed over, and holding messages in the host
  instead would have taken away Claude Code's and Codex's own mid-turn pick-up.
- **A chat goes on after a limit renews**, on by default (`options.auto_continue`, the chat's ⋮):
  one minute after the used-up window's reset, if nothing happened in the chat since the limited
  turn, the server sends `agenthost.ContinueText` (client_id `auto-continue-<id>-<turn>`) and
  journals `notice{auto_continued}`. The limit is kept in the chat's brief, so a restart keeps it.
- **Claude Code signs in on the web** (Admin → Accounts), not in a Shell: `claude auth login
  --claudeai` runs on a server pty; the dialog shows its page and takes the code that page shows.
  The code passes through Socrates on its way to the CLI (REBUILD §4.2 updated).
- **Touch mode: a long press keeps a field writable.** iOS cancels the pointer of a long press;
  that cancel used to mute the field again (read-only), and iOS offers no Paste on a read-only
  field. Only a cancel after the finger moved past 10 px mutes again.
- **A plan's "Keep planning" note** is kept in this browser while it is written, like every other
  field's text.

### The model calls (2026-10-25)

- **Every text call is Claude Haiku 5.5** (`anthropic/claude-haiku-5.5`, `config.DefaultAssistModel`):
  titles, Status and turn outcomes. Transcription keeps its own model (it hears audio) and the voice
  stays Google Cloud TTS. A settings document saved before `openrouter.models_rev` 1 has the three
  text models replaced once, models picked by hand included (the owner: "use Haiku for
  everything"); a model picked after that is kept.
- **A chat's Status is an analysis of the whole session, in a modal of its own.** The model reads
  the chat's record - every message, call (with how long it took or has run), question and turn
  boundary, each with its time, cut from the old end past 160k runes - and the measured facts
  (`chatreport.go`), and answers a summary, what runs now and an estimate of the time left
  (`chatstatus.go`). The modal (`statusmodal.js`) opens on the press and says what is being done
  until the answer is in; then a state chip, "Right now" with the running call, three tiles (running
  for / expected / session, the times measured by the server and counted on), the task list with its
  progress and the summary, with Read aloud. Nothing of it stands in the chat's dock. A Shell's
  Status still reads its screen and says it in the notice.
- **The rename dialog suggests a name** (its sparkles): `POST /api/sessions/{id}/title
  {"suggest":true}` answers a name from the journal and saves nothing; Rename does.
- **Turn outcomes are on by default** (the owner: "always on"), and `openrouter.models_rev` 2 turns
  them on once for an installation saved before.
- **Read aloud on an answer.** Beside Copy, an answer has Read aloud: `POST /api/chats/{id}/spoken`
  says it for the ear in the voice's language (what was done, what came of it, what it asks; code,
  paths and tables told rather than read), or hands back a short plain answer as it is.
