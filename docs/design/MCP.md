# MCP servers: Admin's, a folder's, and Socrates' own (2026-10-29)

Status: built on 2026-10-29 at the owner's request. It reworks how Socrates manages MCP servers and
adds MCP servers built into Socrates, the first of which is BrowserStack: browser.lol's `qa` device
driver (a command-line tool of its test catalog) moved into Socrates as a component.

## What a chat can have

| From | Where it is set up | On in a chat |
|---|---|---|
| **Socrates** | built into the binary (`internal/mcpserver`), its accounts in Admin | off unless Admin switches it on for every chat, or the chat does |
| **Admin** | Admin → MCP servers (`settings.mcp.servers`) | as Admin's *In every chat* says, unless the chat says otherwise |
| **Folder** | the chat folder's `.mcp.json` (and those above it in the repository) | on unless the chat switches it off |

A chat switches any of them in ⋮ → *MCP servers…*, and the new session sheet has the same switches
(HARNESS-POLICY.md, "MCP servers of a folder"). A folder's server wins a name of Admin's; no server of
a folder or of Admin may take the name of a built-in one (`socrates` stays ask_user's).

A test session has only the servers its area names in `AREA.md` (`mcp:`) and those the repository's
Tests settings switch on for cells (`mcp` in the settings, TEST-MODE.md); every other is off.

## Servers built into Socrates

A built-in server is a Go component that runs **inside the Socrates server process**, so what it holds
(a device session, a lease of an account) outlives the agent's CLI, which is parked when idle and
started again. The agent reaches it like any stdio MCP server:

```
agent CLI ──stdio──▶ socrates mcp-server <name> ──tools socket (POST /mcp, chat token)──▶ Registry ──▶ Server.Call
```

- `socrates mcp-server <name>` (`mcpserver.ServeStdio`) is a thin MCP server: `initialize` carries the
  server's instructions, `tools/list` its tools, `tools/call` goes to the Socrates server, which runs
  the tool for the calling session. A long call sends `notifications/progress` every 20 s (OpenCode
  restarts its call timeout on them); Codex and OpenCode are given a tool timeout of an hour, Claude
  Code's default is a day. A `notifications/cancelled` withdraws the call.
- The token is the chat-tools token (`chatToolsEndpoint`), sealed in the launch spec with Admin's
  servers (`agenthost.SealMCP`). A call knows its session (`mcpserver.Session`): a chat, or a test
  cell with its run, cell key, attempt, dimensions, parameters and evidence folder (`Runner.MCPCell`).
- A test cell's prompt already carries the instructions of the servers its area names, so
  `describe` leaves them out there.

### The component model (`internal/mcpserver`)

A server implements `mcpserver.Server` (name, title, description, instructions, tools, `Call`) and
any of these:

| Interface | What it adds |
|---|---|
| `Accounts` | accounts kept by Admin: their fields (secret ones masked in the browser), label and check against the service; stored sealed with the data directory's key in the kv table (`mcp.<name>.accounts`) |
| `Pool` | leases of those accounts, one holder each: a chat takes one when it starts using one, a test cell gets one from the runner before its session starts (it waits, spending nothing, until one is free) |
| `Ender` | what it keeps for a session is closed when a test attempt ends or the chat is deleted |
| `Reporter` | what is in use, for Admin |
| `AreaChecker` | checks of a test catalog area that names it (`socrates tests check`, and before every run) |

A new server is one package that implements it and one line in `internal/mcpserver/builtin`
(`servers`). Admin, the chats, the new session sheet, the Tests settings and the test runner pick it
up from the registry: its row in Admin with its accounts, its switch in every chat, its pool in test
runs.

## BrowserStack (`internal/mcpserver/browserstack`)

browser.lol's `qa` driver (`.socrates/tests/tools/browserstack`, TypeScript) ported to Go and moved
into Socrates; it behaves as the CLI did.

| `qa` | Now |
|---|---|
| `qa start`, `qa stop`, `qa open`, `find`, `tap`, `type`, `key`, `keyboard`, `scroll`, `rotate`, `back`, `forward`, `reload`, `wait`, `look`, `shot`, `env`, `address`, `mail`, `logs` | tools of the same names, with the same options as arguments (`tap {text, role, nth, exact, any, css, native, force}` …), the same answers and the same evidence; every action's answer also carries its screenshot (a JPEG at most 1568 px, the PNG stays in the evidence) |
| the daemon per cell (`qa.sock`), keep-alive every minute, 20 min idle stop, 8 min `start` wait then "still queued", 40 min device start limit, 2 h warning | a device per session in the server (`device.go`), same limits |
| `BROWSERSTACK_ACCOUNTS` in the vault, the test pool, `qa pool-status` | Admin's accounts, the server's pool (busy accounts read from Automate's plan once a minute go last), the runner's lease through `Pool` |
| cell from `SOCRATES_TEST_DIM_DEVICE`/`_LOCALE`, `SOCRATES_TEST_PARAM_CONSENT` | the same values from the cell (`TestCell.Dims`, `Params`); in a chat `start {device, locale, consent, base_url}` |
| `QA_BASE_URL`, locales, `QA_*` logins from `secretctl`, the mailbox (`bin/bp beta db`), OneUptime logs (`infra/logs`), project name | the repository's `.socrates/mcp/browserstack.json` (below) |
| `qa stop` as the cell's end hook, closing an orphaned session | `Ender.End` after the attempt, before its lease is released; sessions open at a restart are written to `<data>/run/mcp/browserstack/sessions.json` and closed when the server starts again |
| evidence in `$SOCRATES_TEST_EVIDENCE` | the attempt's evidence folder; a chat's in `<data>/mcp/browserstack/chats/<chat>/`, removed with the chat |

The device list is the driver's (`driver.Devices`, the ids of browser.lol's `AREA.md`); an area that
names `browserstack` is checked against it.

### `.socrates/mcp/browserstack.json`

What the server needs to know about a repository, all optional (without it, `open` takes full URLs):

```json
{
  "base_url": "https://beta.browser.lol",
  "locales": ["en", "de", "fr", "es", "it", "pt", "ja"],
  "project": "browser.lol device QA",
  "secrets": {"command": "infra/secretctl show beta device-qa", "names": ["QA_GOOGLE_EMAIL?", "QA_GOOGLE_PASSWORD?"]},
  "totp": {"google": "QA_GOOGLE_TOTP_SECRET", "ms": "QA_MS_TOTP_SECRET"},
  "mail": {"command": ".socrates/mcp/browserstack-mail", "address": "qa{stamp}m{member}-beta@goog.re", "link_hosts": ["browser.lol", "goog.re"]},
  "logs": {"command": ".socrates/mcp/browserstack-logs", "name": "oneuptime", "note": "other sessions may use beta at the same time; a line may be theirs"}
}
```

- `secrets.command` prints `NAME=value` lines and runs in the repository root when a device starts;
  the values stay in the server's memory and are typed with `type {secret: NAME}` only, never shown.
- `mail.command` is run as `<command> --to <address|%> --since <seconds> --limit <n>` and prints a JSON
  list of `{received, from, to, title, code, text, html}`; `logs.command` as
  `<command> --from <RFC 3339> --to <RFC 3339>` and prints one line each.
- `base_url` may be overridden per test area with the parameter `base_url`, and in a chat by `start`.

## Socrates Tests (`internal/mcpserver/tests`)

Added on 2026-10-30 at the owner's request: everything the Tests page does with a repository's test
catalog, as tools of a chat's agent. It needs no account and no configuration; like every built-in
server it is in every chat's list and **off** until Admin switches it on for every chat or the chat
does. It works on the repository of the chat's folder (`testmode.FindRoot`), never on another.

| Tool | Does |
|---|---|
| `tests_format`, `tests_check`, `run_tests`, `test_results` | as the chat tools of the same names (TEST-MODE.md, "Runs started by chat agents"): a run it starts is the chat's, and its results come to the chat as a message |
| `catalog_read {path?}` | a file of `.socrates/tests/`, or the list of its files |
| `catalog_write {path, content, executable?}`, `catalog_delete {path}` | writes or deletes a file of the catalog (folders made, empty ones removed), then checks the catalog: this file's problems and the totals. Only inside `.socrates/tests/`, names of letters, digits, `-`, `_`, `.` that do not start with a dot, never through a symbolic link, at most 1 MB |
| `test_read {test}` | a test's file, how each of its cells stood in the latest runs, its bugs |
| `tests_estimate {selection}` | cells, tests, time and cost of a selection (`Runner.Estimate`) |
| `test_runs {limit?}`, `test_run {run?}` | the runs, newest first; one run with every cell's state and verdict |
| `test_cell {run?, cell}` | a cell (or an attempt) in full: steps, the agent's, reviewer's and person's verdicts, actions, screenshot paths, other attempts (`Runner.CellText`) |
| `test_run_control {run, action}` | `stop`, `resume`, `analyse`, `pin`, `unpin`, `delete` |
| `test_retry {run?, cell}`, `test_override {run?, cell, verdict, note?}` | another attempt of a cell; the person's verdict of it |
| `test_bugs {status?}`, `test_bug {id}`, `test_bug_update {id, status?, severity?, note?, issue?}`, `test_bug_issue {id, title, body}` | the bug list, a bug in full, triage, a GitHub issue |
| `test_search {query, limit?}` | cells of the runs that hold every word |
| `test_settings {set?}` | the repository's test settings; `set` changes what it names (checked as the Tests page's are) |

`run` is a run's id or `latest` (the default). Unknown arguments are refused. The instructions tell the
agent to run as narrow as the question, to end its turn after `run_tests` rather than poll, and to
leave stopping and deleting runs, overrides, triage, issues and settings to the person's word.

- **Not twice.** A chat that has it on is not given the chat tools' four test tools
  (`Server.testsMCPOn`, `spec.ChatTools.Tests`), which it has here under the same names.
- **Chats only** (`mcpserver.ChatsOnly`). A test session never has it on, whatever the area or the
  Tests settings say; its calls are refused there, an area that names it under `mcp:` is an error
  of the catalog (`CheckArea`), and the Tests page does not offer it for the cells (`chat_only` in
  the MCP views).
- **The runner** comes from the server (`builtin.Deps`); without one the file tools and
  `tests_check` work and the rest say the runner is not running.

## Admin's servers

Admin → **MCP servers** has the built-in servers (with their accounts, their tools and what is in use)
and Admin's own. An own server has, beside its transport fields:

| Field | |
|---|---|
| `description` | shown in Admin and beside its switch in a chat |
| `auth` | none, or `oauth` (remote servers) |
| `scope` | the OAuth scope asked for |
| `tool_timeout` | seconds one call may take: Claude Code `timeout`, Codex `tool_timeout_sec`, OpenCode `timeout` |
| `disabled_tools` | tools no agent gets: Claude Code `--disallowedTools mcp__<server>__<tool>`, Codex `disabled_tools`, OpenCode `tools: {"<server>_<tool>": false}` |

**Test** (`POST /api/mcp/test`, `internal/mcpclient`) connects as an agent would (stdio, streamable
HTTP or SSE; `${VAR}` filled in from Socrates' environment), runs `initialize`, lists the tools,
prompts and resources, and shows the server's name, version, protocol, instructions and latency, or
the error with the end of its stderr. A chat's dialog tests its servers in the chat's folder
(`POST /api/sessions/{id}/mcp/test`). The last test of each server is shown on its row.

### OAuth

A remote server that answers 401 is signed in to once in Admin (the MCP authorization spec):
Socrates discovers the authorization server (protected resource metadata, then the authorization
server's metadata, then the 2025-03-26 defaults), registers itself as a public client (dynamic client
registration) for the address Admin is open at (`…/api/mcp/oauth/callback`), and sends the browser
there with PKCE (S256) and the resource indicator; the callback trades the code for tokens. The
sign-ins are kept sealed in the kv table (`mcp.oauth`), keyed by the server's URL, so a renamed server
keeps its sign-in; Disconnect revokes and forgets it.

An agent never holds a token: a streamable HTTP server is reached through Socrates' proxy
(`/mcp-proxy/<name>`, with the chat's token in `X-Socrates-MCP`), which adds a fresh token to every
request and refreshes it when it runs out or is refused. An SSE server cannot be proxied (its endpoint
URLs are its own): its agent gets the token as a header when it starts, which a park and restart
refreshes.

## Code

| Where | What |
|---|---|
| `internal/mcpserver` | the component model, the registry and its accounts, `socrates mcp-server` |
| `internal/mcpserver/builtin` | the list of built-in servers |
| `internal/mcpserver/browserstack`, `…/driver` | the BrowserStack server; the device driver (WebDriver, native taps, evidence, page scripts) |
| `internal/mcpserver/tests` | Socrates Tests |
| `internal/mcpclient` | the probe (stdio, streamable HTTP, SSE) and the OAuth client |
| `internal/server/mcpbuiltin.go`, `mcpadmin.go`, `mcpoauth.go`, `mcp.go` | the registry in the server, the tools socket route, Admin's API, the test, OAuth and the proxy |
| `internal/testmode` | `Settings.MCP`, leases of built-in pools (`Lease.MCP`), `Host.MCPPool`/`EndMCP`, `Runner.MCPCell` |
| `internal/web/static/js/adminmcp.js` | Admin's MCP servers |
