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
`resumed` flag on the row and `POST /api/sessions/{id}/ack-resume` carry the same fact, and WP5
adds the frame on top of them.

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
