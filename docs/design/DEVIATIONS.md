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

**WP2 / §F.1 — version detection is in `tmux.go`; `install.go` is not part of this package.**
WP2's file list does not name `install.go` and WP8's does. `ParseVersion`, `BinaryVersion`,
`MinMajor`/`MinMinor` = 3.3 and `Version.OK` live in `tmux.go` for `requireTmux` and the
Manager; the `/api/tmux` payload, the package-manager matrix and the SSE installer are left to
WP8 to build on them.
