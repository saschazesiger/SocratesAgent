# Harness launch policy (2026-09-02)

> Since the rebuild (REBUILD.md §3, §7; WP10) Claude Code, Codex and OpenCode run only as chats,
> through their own protocols, and their terminal launches below are gone; what still runs in a
> terminal is the Shell (and a folder's VS Code). The agents' fixed policy now lives in their
> adapters (`internal/agent/*`, AGENT-HOST.md).

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

> Superseded: this agent runs as a chat (REBUILD.md §3); its terminal launch is gone.

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
  - As root it is refused: `--dangerously-skip-permissions cannot be used with
    root/sudo privileges for security reasons`, exit 1, and the pane is dead
    before anybody reads it. The binary's condition is
    `getuid() === 0 && IS_SANDBOX !== "1" && !CLAUDE_CODE_BUBBLEWRAP`, so
    **`IS_SANDBOX=1` is in the pane's environment whenever Socrates is running
    as root** — which the installed systemd service is. It is not set for any other
    uid: no such check applies there, and the variable would be a claim about a
    machine nobody asked about. The pane's environment is built rather than
    inherited, so this cannot be left to whatever the server was started from.
- **`ANTHROPIC_API_KEY` is set to the empty string** in the pane. A key
  exported into Socrates' own environment would otherwise reach the session
  through tmux and stop it on `Detected a custom API key in your environment …
  ❯ No (recommended)`. Every Socrates session is a signed-in Claude Code, which
  is what the settings file and the global config are written for; an ambient
  key is not part of that. tmux's `new-session -e` has no "unset", and an empty
  value is read by the binary as no key at all (verified against 2.1.258).
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

Working-directory trust: **`projects[<realpath of cwd>].hasTrustDialogAccepted:
true`** in
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

Both entries are keyed by the **resolved** working directory — symlinks gone.
Each program looks the entry up under the directory it reads back from its own
process, which the kernel has already resolved, so a workspace root reached
through a link (`/tmp` on macOS, a linked projects directory, a home that is a
symlink) would otherwise be trusted under a name neither program ever asks
about.

Generated settings file (`<session dir>/claude-settings.json`, mode 0600):

```json
{
  "skipDangerousModePermissionPrompt": true,
  "disableRemoteControl": true,
  "cleanupPeriodDays": 90,
  "ultracode": false,
  "env": { … the pane's environment … }
}
```

`skipDangerousModePermissionPrompt` is what stops the bypass launch sitting on
a confirmation dialog before the user can type. `cleanupPeriodDays` is 90 so a
transcript outlives the offline stretch it may have to be resumed after.
`ultracode` is the one key the sheet decides: off unless the person switched
Ultracode on when creating the session, then `true` for that session's whole
life (kept in its options as `"ultracode": true`, so a resume keeps it).
Verified against 2.1.296: the binary documents the key as "set per session via
the `ultracode` settings key (--settings …)", and `/effort` in a pane started
with it reads "Ultracode on". `env`
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

> Superseded: this agent runs as a chat (REBUILD.md §3); its terminal launch is gone.

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

> Superseded: this agent runs as a chat (REBUILD.md §3); its terminal launch is gone.

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

## MCP servers of a folder

A chat gets the MCP servers its folder names, whatever the harness: the
folder's `.mcp.json`, in the shape Claude Code and most other tools read, and
those of the folders above it up to the repository's root.

```json
{"mcpServers": {
  "db":   {"command": "db-mcp", "args": ["--ro"], "env": {"TOKEN": "${DB_TOKEN}"}},
  "docs": {"type": "http", "url": "https://docs.example/mcp", "headers": {"Authorization": "Bearer ${DOCS_KEY}"}}
}}
```

- **One source.** Socrates reads the file (`agent.ReadFolderMCP`) and hands
  the servers to the CLI in its own form: Claude Code `--mcp-config`, Codex
  `mcp_servers` in the thread's config, OpenCode `mcp` in
  `OPENCODE_CONFIG_CONTENT`. MCP servers the CLIs would load from the folder
  by themselves are kept off: Claude Code keeps `--strict-mcp-config`, and
  Codex (`.codex/config.toml`) and OpenCode (`opencode.json[c]`,
  `.opencode/`) get every such server turned off by name. Servers of an
  account's own Codex `config.toml` stay.
- **Started without asking.** Like every tool of a chat, the servers run with
  no approval: a folder's `.mcp.json` is trusted as its code is. A repository
  checked out into a chat's folder starts the commands its `.mcp.json` names.
- **Variables.** `${VAR}` and `${VAR:-default}` take the chat's environment
  (the account's); a server that needs a variable the chat does not have is
  left out, and the chat says so.
- **Types.** `stdio` (the default with a `command`), `http` (the default with
  only a `url`) and `sse`. Codex has no SSE: such a server is left out there.
  A name is letters, digits, `-` and `_`; `socrates` is ask_user's.
- **When.** The file is read at every start of the CLI, so a change counts
  from the chat's next start (after it was parked, or an account switch).
  The chat's Info lists the servers.

### MCP servers of Admin, and a chat's own choice

Admin → **MCP servers** sets up servers for every chat (`settings.mcp.servers`:
name, type, command, arguments and environment, or URL and headers, the same
fields as a `.mcp.json` entry; a JSON paste takes `{"mcpServers": {...}}`).
Each has **In every chat**: on, every chat has it; off, a chat has it only
once switched on there.

- **Merged with the folder's.** A chat may have Admin's servers and its
  folder's (`agent.ChatMCP`); a folder's server wins a name both have, as the
  nearer `.mcp.json` does.
- **Per chat.** A chat's ⋮ → *MCP servers…* has a switch for each, Admin's
  and the folder's alike. A switch put where the default is gives the choice
  back to Admin; any other is kept in the chat's options
  (`"mcp": {name: on}`), so a chat nobody switched follows Admin's default.
  Like Ultracode, a change parks the idle agent and counts from the next
  message.
- **From the start.** The new session sheet has the same switches under
  **MCP servers**, for the folder chosen there (`GET /api/mcp?workdir_mode=…&workdir=…`;
  a dynamic folder is looked at as the new folder it will be). A chat is made
  with those that differ from their default (`POST /api/chats` `"mcp"`),
  kept as a switch in its dialog would be.
- **Secrets.** Admin's servers are kept in the settings document, as the
  other keys of the dashboard are, and reach a chat's host sealed for that
  chat (`Spec.SharedMCP`, `agenthost.SealMCP`). `${VAR}` and
  `${VAR:-default}` are filled in from the chat's environment as for a
  folder's, so a token can stay in the account's environment instead.
- **Checked on save.** A name other than `[A-Za-z0-9_-]+`, `socrates`, two
  servers of one name, stdio without a command or http/sse without a URL is
  refused when Admin saves.

## Chats of one repository

The agents of the chats that work in one repository reach each other,
whatever their harness and account: three tools beside `ask_user` on
Socrates' own MCP server (`internal/agent/chattools`).

| Tool | What it does |
|---|---|
| `list_chats` | the other chats of the repository: id, title, agent, model, working or waiting, folder |
| `read_chat` | the last messages of one (10 unless `count` says, at most 50; 4000 characters each) |
| `send_to_chat` | a message into one, at most 20 000 characters |

- **The repository** of a chat is the nearest folder at or above its own
  with a `.git`, else its folder alone (`chatScope`). A dynamic folder is a
  chat's own, so such a chat reaches nobody; preset and typed-in folders do.
- **A message** goes into the other chat as the person's messages do
- **Only the chats at work.** A chat filed in the background or done, or
  one nobody has sent anything yet (its folder's Not started shelf), is not
  there for the others: `list_chats` leaves it out, and `read_chat` and
  `send_to_chat` answer as for a chat that does not exist (`awake`). Filed
  back in progress, or sent its first message, it is there again.
  (`sendMessage`), after a `chat_message` notice that names the chat it came
  from ("A message from the chat “API work” (Claude Code)."). It opens with
  a line for the agent - which chat, its id, answer with `send_to_chat` -
  that the chat's bubble leaves out. It wakes a parked agent at once and
  waits behind a running turn; the other agent answers, if it does, the same
  way. Each message costs the receiving chat's account.
- **The brake.** A chat sends at most 40 messages an hour
  (`chatSendsPerHour`, counted in the server's memory); past that the tool
  says so, so that two agents cannot answer each other for ever.
- **How a call gets there.** The server serves the tools on its own socket,
  `<data>/run/tools.sock` (`agenthost.ToolsSocketPath`, 0600), and puts it
  into every chat's launch spec with a token: the chat's id sealed with
  `secrets.key` (`chat_tools` in `host.json`, not backed up). Claude Code and
  Codex call it from the host (in-process MCP server; dynamic tools of a new
  thread - a Codex thread from before keeps the tools it started with);
  OpenCode's `socrates mcp-ask` gets it in its environment
  (`SOCRATES_CHAT_TOOLS_SOCKET`, `SOCRATES_CHAT_TOOLS_TOKEN`). The tools and
  their guidance are only there when the spec has the endpoint.
- **The only way.** Claude Code's own messaging between the sessions of an
  account (`ListAgents`/`SendMessage` to other sessions, its inbox under
  `/tmp/cc-socks`) is off in every chat (`CLAUDE_CODE_HARBOR_KITE=0`, an
  undocumented switch of the CLI, verified against 2.1.312: "[uds-messaging]
  Skipped: cross-session messaging gate off"). Codex and OpenCode have no such
  thing. A new CLI release can rename the switch: `claude -p --debug` with an
  empty `CLAUDE_CONFIG_DIR` shows whether `[uds-messaging] Listening` is back.
- **When the agents talk** is in their instructions (`chattools.Guidance`:
  Claude Code's system prompt, Codex's developer instructions, the socrates
  server's MCP instructions for OpenCode), and in short in the tools'
  descriptions. They coordinate only where one's work affects another's:
  with changes in the working tree they did not make, they commit only their
  own files, by path; before pushing a branch another chat works on, or before
  what would touch, move or hide its work (pull, rebase, branch switch, stash,
  discard, mass moves, dependencies, migrations, shared services), they check
  quietly (`list_chats`, `read_chat`) and ask only when its uncommitted files,
  branch or service would change, then wait for the answer by ending their
  turn; with nobody there, or unrelated work, they go ahead without asking.
  A change another chat relies on gets one short note. Nothing else is sent
  (no status, greetings, thanks, or answers that are not needed); whoever is
  waited for says once when it is done; a missing answer is not asked again;
  and a message that conflicts with what the person asked goes to the person.
  `list_chats` names the branch the shared working tree is on (read from
  `.git/HEAD`, never by running git in a folder the agents can configure).
  The rules were checked against twelve situations by two independent
  reviews (2026-10-28) before they shipped.

## Instructions per model

Each favourite model of a harness in Admin has an **Instructions** button: a
dialog with one text field (`models[].instructions` in the harness's
settings, trimmed, at most 32 KiB because Claude Code takes it as one
argument). A chat is given the instructions of the model it runs on, and
only those (`agent.StartSpec.Instructions`, `internal/agent/instructions.go`).
The server hands them to the host when it launches it, so a change counts from
a chat's next start.

| | At the start | After a switch of model |
|---|---|---|
| Claude Code | `--append-system-prompt`, after the ask_user guidance, at every start (and so after every park) | the next message carries them, in `<model-instructions>` |
| Codex | the developer instructions of a **new** thread | the next message, as for Claude Code. A thread keeps the developer instructions it was started with for good: `developerInstructions` on `thread/resume` is ignored (verified with codex 0.164, `TestRealModelInstructionsFollowTheModel`). So the adapter keeps which instructions the thread has in its state directory (`model-instructions.json`), and a resume on another model, or after the instructions changed, says so in the next message, once |
| OpenCode | `system` on every `prompt_async`, which OpenCode adds after its own system prompt for that prompt's turn (1.19.4) | nothing more: every prompt carries its model's |

The note before a message says which model the chat now runs on, that the
earlier model's instructions no longer apply, and the new ones. A transcript
read back (a rebuilt journal) shows the person's message without it.

## Shell

`<binary> -l`, in the working directory, with the shared terminal environment.
A login shell reads the profile that sets up PATH and the prompt, which is
what makes the pane look like the machine's own terminal — a shell that did
not read it is a shell missing the very programs it was opened for.

The shell is the dashboard's binary when one is set, and otherwise `$SHELL`,
then `/bin/bash`, then `/bin/sh`. The profiles a Shell once ran in are gone
with the profiles (REBUILD.md §4): an account signs in from a Shell of its own
(AGENT-HOST.md §11).

Shells and agents alike run directly on the machine, as the user the service
runs as: the machine's files, services and tools are simply there, and nothing
has to be told how to reach them.

## What is left in the dashboard

Per harness: **enabled**, **binary path** (with discovery), the **model short
list** (each with its effort and its instructions), **default model** and — for Claude Code and Codex — **default
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
