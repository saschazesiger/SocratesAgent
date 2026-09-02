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
