# Harness launch policy (2026-09-02)

The admin dashboard exposes the basics only: whether a program is offered,
where its binary lives, the short list of models its picker shows, and the
model and effort a session starts on when the person starting it did not
choose. Everything else about a launch — permissions, sandbox, approval,
remote control, theme, terminal environment, generated configuration — is
fixed policy compiled into `internal/harnesses/`.

This document is that policy, and where each flag in it was verified. It
supersedes the "Admin (everything configurable)" section of `DECISIONS.md`.

## Why

A Socrates session is a terminal on a phone, often in a car, often offline. A
permission prompt in it is a prompt nobody is watching, and a session that has
stopped on one is a session that has failed. The three coding CLIs are
therefore started with their approvals bypassed, deliberately and always — see
the Security section of the README for what that means and does not mean.

The second reason is narrower: an option that can be set wrong is an option
that will be, and a wrong `-c` key or an unknown flag is a pane that dies
before the person can read why.

## How it was verified

Against the binaries installed on the build machine, not against
documentation, because the documentation for all three lags the shipped
builds:

| Program | Version | How |
| --- | --- | --- |
| Claude Code | 2.1.258 | `claude --help`; `strings` over the native binary for settings and global-config keys |
| Codex CLI | 0.152.1 | `codex --help`, plus `codex --yolo --version` to confirm the hidden alias |
| OpenCode | 1.17.13 | `opencode --help`; `strings` over the binary for the `OPENCODE_PERMISSION` merge |

Upstream references:

- Claude Code CLI reference — https://docs.claude.com/en/docs/claude-code/cli-reference
- Claude Code settings — https://docs.claude.com/en/docs/claude-code/settings
- Codex CLI reference — https://developers.openai.com/codex/cli/reference
- Codex sandbox and approvals — https://developers.openai.com/codex/local-config
- OpenCode permissions — https://opencode.ai/docs/permissions/
- OpenCode configuration — https://opencode.ai/docs/config/

## Claude Code

Command line, in order:

```
<binary> --session-id <uuid> | --resume <id>
         [--name <session title>]
         [--model <model>] [--effort <level>]
         --dangerously-skip-permissions
         --settings <session dir>/claude-settings.json
```

- `--dangerously-skip-permissions` — "Bypass all permission checks"
  (`claude --help`). Not `--permission-mode bypassPermissions`, which is the
  same thing said less plainly, and not `--allow-dangerously-skip-permissions`,
  which only makes the bypass *available* rather than active.
- `--effort` takes `low medium high xhigh max`. `minimal` and `ultra` are
  levels other harnesses name and this flag rejects, so they are filtered out
  rather than passed through.
- `--remote-control` is **never** passed. Not passing it is not enough on its
  own, so two more things are done:
  - `disableRemoteControl: true` in the generated settings file. The binary
    reports on this key directly — `Disabled by org policy
    (disableRemoteControl)` / `Remote Control is disabled by your
    organization's policy (managed setting \`disableRemoteControl\`)`.
  - `remoteControlAtStartup: false` in `~/.claude.json` (or
    `$CLAUDE_CONFIG_DIR/.claude.json`), at the top level and inside the
    `projects[<cwd>]` entry. The binary logs
    `remoteControlAtStartup: true in …` for `project and local` and for
    `legacy_global_config`, so a person who once turned Remote Control on by
    hand has a stored preference that would start it again with no flag at all.

Working-directory trust: **`projects[<cwd>].hasTrustDialogAccepted: true`** in
the same global config, written before the pane opens, creating the entry when
Claude Code has never been run in that directory — which for a dynamic
workspace directory is every single time. Without it 2.1.258 opens on a
blocking full-screen question (`Accessing workspace: <cwd>` / "Is this a
project you created or one you trust?") whose highlighted answer is **No,
exit**, and no session can be started from the browser at all. There is no
command-line flag for it in 2.1.258, `--dangerously-skip-permissions` is about
tool permissions and is decided after it, and the settings file has no key for
it. This is Claude Code's counterpart of the
`projects={"<cwd>"={trust_level="trusted"}}` override Codex is given below;
both were verified against the shipped binaries.

Generated settings file (`<session dir>/claude-settings.json`, mode 0600):

```json
{
  "skipDangerousModePermissionPrompt": true,
  "disableRemoteControl": true,
  "cleanupPeriodDays": 90,
  "env": { … the pane's environment … }
}
```

`skipDangerousModePermissionPrompt` is what stops the bypass launch sitting on
a confirmation dialog before the user can type. `cleanupPeriodDays` is 90 so a
transcript outlives the offline stretch it may have to be resumed after. `env`
reaches every subprocess Claude Code starts, which is what makes a tool's
shell look like the pane it came from.

Environment: `CLAUDE_CODE_TMUX_TRUECOLOR=1`,
`CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1`, `CLAUDE_CODE_NO_FLICKER=1`, plus the
shared terminal environment (`COLORFGBG=0;15` is what decides Claude Code's
palette). `CLAUDE_CODE_DISABLE_MOUSE` and `CLAUDE_CODE_FORCE_SYNC_OUTPUT` are
**not** set: those were off by default before this change and stay off.

Global config: `theme: "light"` in `~/.claude.json`. There is no `--theme`
flag in 2.1.258 and `theme` is not a settings.json key; it is in the global
config defaults object the binary builds
(`{numStartups:0, …, theme:"dark", …}`), and the shipped default is `dark`,
which is the unreadable case on a white page.

Session mechanics are unchanged: a new session is `--session-id <uuid>` (an id
may be used with it exactly once), a resumed one is `--resume <id>`.
`--fork-session` is no longer offered — a resume continues the conversation it
names.

## Codex

```
<binary> [resume <id>]
         --strict-config -C <cwd>
         [-m <model>] [-c model_reasoning_effort="<level>"]
         -c projects={"<cwd>"={trust_level="trusted"}}
         -c tui.theme="light-gray"
         --no-alt-screen
         --dangerously-bypass-approvals-and-sandbox
```

- `--dangerously-bypass-approvals-and-sandbox` — "Skip all confirmation
  prompts and execute commands without sandboxing" (`codex --help`, 0.152.1).
  `--yolo` is a hidden alias of the same flag: `codex --yolo --version` is
  accepted while `codex --notaflag` is `error: unexpected argument`. The long
  spelling is used because it says what it does.
- Neither `-s/--sandbox` nor `-a/--ask-for-approval` is passed beside it: the
  bypass replaces both, and naming a sandbox and then bypassing it is a
  command line Codex refuses.
- `--remote` and `--remote-auth-token-env` are never passed: a Codex TUI that
  talks to somebody else's app server is not the session this app started.
- `--strict-config` stays, because without it an unknown `-c` key is ignored
  in silence.
- The trust override stays, and stays in its inline-table form. Codex splits a
  dotted override on every `.` regardless of quoting, and every default
  Socrates workspace lives under `~/.socrates`, which has one. Without it a
  fresh directory opens on a blocking trust picker that eats the first
  keystroke.
- `--no-alt-screen` keeps the scrollback, which is the whole of what a web
  terminal has.
- Environment: `CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates`, which stamps the
  rollout file that is how the session id is found again.
  `CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT` is not set (it was off by default).

## OpenCode

```
<binary> --port <free loopback port> --hostname 127.0.0.1
         [--session <id>]
         [-m <provider/model>]
         <cwd>
```

Permissions are allowed by name rather than by `--auto`. `--auto` approves
only what is "not explicitly denied", which leaves the answer to a
configuration file the user may have; the explicit document does not.
The same object is set twice — as `OPENCODE_PERMISSION`, which OpenCode merges
over every configuration file it found (verified in the binary:
`if(D.OPENCODE_PERMISSION) Y.permission = o(Y.permission ?? {},
JSON.parse(D.OPENCODE_PERMISSION))`), and as the `permission` block of the
generated `OPENCODE_CONFIG_CONTENT`, which is merged last of the file sources:

```json
{"*":"allow","bash":"allow","edit":"allow","write":"allow","patch":"allow",
 "read":"allow","webfetch":"allow","websearch":"allow","task":"allow",
 "todowrite":"allow","external_directory":"allow"}
```

The wildcard is the form OpenCode's own built-in agents use
(`{"*":"deny", grep:"allow", …}`). The named keys are listed as well because
`external_directory` is asked for through a path of its own, and a wildcard
that did not cover it would be a blocking prompt nobody would find.

Generated `OPENCODE_CONFIG_CONTENT`: `share: "disabled"` (nothing a session
does is published anywhere), `autoupdate: false`, the permission block above,
and the model when there is one.

Generated `tui.json` (mode 0600): `theme: "github"`, `mouse: true`,
`attention.enabled: false` — a server harness has no business making a desktop
notification sound. The light or dark half of the theme is chosen by the
pane's OSC 11 answer, not by the name.

Environment: `OPENCODE_DISABLE_AUTOUPDATE=1`,
`OPENCODE_DISABLE_TERMINAL_TITLE=1`, `OPENCODE_DISABLE_MODELS_FETCH=1` (this
is what lets OpenCode start with no network at all), `OPENCODE_TUI_CONFIG`,
`OPENCODE_CONFIG_CONTENT`, and `OPENCODE_SERVER_USERNAME` /
`OPENCODE_SERVER_PASSWORD`. The password is 32 bytes of entropy per session
and is not optional: the TUI *is* the whole OpenCode HTTP server, and without
it every path on that port is open to any process on the machine.
`--print-logs` and `--log-level` are never passed — the first writes into the
pane the user is reading, and the second alone only fills a file nobody asked
for. `--fork` is not offered: a resume continues the session it names.

## Shell

`<binary> -l`, in the working directory, with the shared terminal environment.
A login shell reads the profile that sets up PATH and the prompt, which is
what makes the pane look like the machine's own terminal — a shell that did
not read it is a shell missing the very programs it was opened for.

## What is left in the dashboard

Per harness: **enabled**, **binary path** (with discovery), the **model short
list**, **default model** and — for Claude Code and Codex — **default
effort**. OpenCode has no default effort because it has no reasoning-effort
flag: the level is part of the model id.

Global, unchanged: workspace root, preset directories, default harness, tmux
status and installer, terminal behaviour, tunnel, voice, password, setup
check, and the OpenRouter key and models for dictation, Status and Agent.

## Migration

The removed settings are simply absent from the Go structs, so a stored
document that still carries them decodes past them and drops them on the next
save. Nothing is refused: a document with `permission_mode`, `autocompact`,
`config_overrides` or an unparseable `permission_json` in it saves with a 200
and comes back without them.
