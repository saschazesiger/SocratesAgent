# Test mode: agent-run test catalogs per repository (plan, 2026-10-29)

Status: planned with the owner on 2026-10-29 and built the same day. This note is the specification
the implementation follows, including the conversion of browser.lol's Device QA (BrowserStack) into
the new format. Where the build settled a point differently from the first plan, this note says what
was built.

## What it is

A repository connected to Socrates can carry a test catalog in `.socrates/tests/`: general
instructions, one folder per area under test with the area's own instructions, one Markdown file per
test that describes the flow step by step, and the tools the tests use (such as a BrowserStack driver
with its pool of accounts). Socrates runs the catalog: every **cell** (a test on one device, in one
locale, ...) is its own agent session with the harness, model and account chosen for the run, as many
at once as configured. A cell that needs a tool account no one has free waits in a queue and starts
by itself once one is free. Every step is judged on evidence (screenshots), a reviewer checks the
doubtful cells, an analyst turns the findings into one bug per root cause, and everything lands in the
data directory, searchable in a table that shows at a glance which tests have problems and opens every
cell down to its screenshots.

It generalises what `tests/devices` (the `qa` driver) does in the browser.lol repository: the catalog,
the slot pool, the judgement per step, the review and the report move into Socrates; what stays in
the repository is the catalog and a tool that drives the devices.

### Not in scope

- Scheduled runs, runs started by a chat agent, GitHub issues from the bug list (later, if wanted;
  runs started by chat agents and GitHub issues came with the evening's revision below).
- Record and replay: every cell is run live by an agent (Device QA decided the same on 2026-10-28).
- Tools as MCP servers of the repository: a catalog's tools are command-line programs (decided below).
  Socrates' own MCP servers are the exception (revision below): BrowserStack is built into Socrates
  as an MCP server an area names in `mcp:`.

## Decisions (owner, 2026-10-29)

| Topic | Decision |
|---|---|
| Test file | Markdown with YAML front matter: metadata in the front matter, the steps under `## Steps` |
| Tools | Command-line programs from the catalog, put on the session's `PATH`; the agent runs them through its shell |
| Queue | A tool account is leased **before** the session starts; a cell waits (no tokens spent) until one is free, then starts by itself |
| Secrets | Read by a command the tool names (for browser.lol `infra/secretctl show beta device-qa`); they reach the tool through a file, never the agent's environment or prompt |
| Session | One per cell (test × device × locale ...), a fresh context each |
| After the cells | A reviewer agent, a bug analysis with conclusions, one automatic retry of blocked cells |
| Start | By hand in the UI only (all, an area, single tests, by priority or tag, only last run's problems) |
| Test sessions | Not on Home or in the sidebar; reachable from their cell; a running run shows as one card on Home |
| Accounts | Per role the harness accounts that may be used are ticked (none ticked: every account); within them Socrates picks for each session |
| Backup | Everything, screenshots included; a retention keeps the last N runs per repository |
| browser.lol | Converted on the `beta` branch (which holds `migration/step-1`); `tests/devices` is removed there |

## Terms

| Term | Meaning |
|---|---|
| Catalog | `.socrates/tests/` of one repository |
| Area | A folder of the catalog (`web-browser/`) with its `AREA.md` |
| Test | One Markdown file of an area: a flow with numbered steps |
| Dimension | An axis an area expands its tests over (`device`, `locale`); its values are defined in `AREA.md` |
| Cell | One test with one value per dimension (`web-browser/header-navigation` on `pixel`, `es`); one session |
| Attempt | One execution of a cell; a retry is a new attempt, the old one is kept |
| Run | A selection of cells executed with one set of settings |
| Tool | A program in `tools/<name>/` described by its `TOOL.md` |
| Pool | A tool's set of accounts; a **lease** gives one account (member) to one cell |
| Role | Cell agent, reviewer or analyst, each with its own harness, model, effort and account setting |

## The catalog

```
.socrates/tests/
  TESTS.md                       general instructions (every prompt of the repository's tests)
  README.md                      for people only, ignored by Socrates (any README.md is)
  tools/
    browserstack/
      TOOL.md                    configuration (front matter) + instructions for the agent (body)
      bin/qa                     what goes on PATH
      ...                        the tool's own code
  web-browser/
    AREA.md                      the area: dimensions, tools, instructions for all its tests
    header-navigation.md         a test
    sign-in/                     sub-folders group tests; they hold no AREA.md
      validation.md
```

- An **area** is every top-level folder except `tools/` that holds an `AREA.md`. A folder without one is
  an error (a forgotten file would otherwise silently drop its tests).
- A **test** is every `*.md` below an area except `AREA.md` and `README.md`. Its id is its path without
  `.md`: `web-browser/sign-in/validation`. The id is what history is keyed by: renaming a file starts a
  new history (the front matter may name `previous: <old id>` to carry it over).
- Files are UTF-8, front matter between `---` lines at the very top. Unknown front-matter keys are
  errors, so a typo is never ignored.
- The catalog is read from the repository's working tree when a run starts and **copied into the run**
  (everything except `tools/`), together with the commit (`git rev-parse HEAD`, dirty or not): a result
  always shows the test text it was run against, even after the test changed.
- The tools are copied too, into the run's runtime folder (`<data>/run/tests/<run>/tools/`, not backed
  up), and run from there: a tool changed or a `node_modules` reinstalled in the checkout in the middle
  of a run never reaches its cells, and the run's `setup` never touches the checkout. Cells themselves
  work in the checkout (they read, they do not build).

### `TESTS.md`

```markdown
---
title: browser.lol            # shown in Socrates; default: the repository folder's name
---
Everything every test of this repository must know: the environment under test (beta only), the
rules for test data, what never to do (production, real payments), how secrets are typed.
```

The body goes into the system prompt of every cell agent, reviewer and analyst of the repository.

### `AREA.md`

```markdown
---
title: browser.lol web app (apps/web-browser)
tools: [browserstack]          # every test of the area gets these; a test may add more
columns: device                # the dimension the results table shows as columns
dimensions:
  device:
    values:                    # ordered: the column order
      iphone: iPhone 15 · Safari
      iphone-old: iPhone 13 · iOS 16
      pixel: Pixel 9 · Chrome
      win-chrome: Windows · Chrome
    extra:                     # run only by a test that names them
      win-edge: Windows · Edge
    default: all               # all | rotate | [ids]
  locale:
    values: {en: English, de: German, fr: French, es: Spanish, it: Italian, pt: Portuguese, ja: Japanese}
    default: rotate
params:                        # free key/value defaults handed to the tools (a test may override)
  consent: accept
evidence: screenshot           # what a pass, issue or fail needs: screenshot | any | none
mcp: []                        # MCP servers of the repository's .mcp.json the cells may use (default none)
timeout: 45m                   # one attempt at most (default 45m)
---
How to work a cell in this area: the verdict rules specific to it, how the devices behave,
what is not a bug here (the device's own English validation bubbles ...).
```

Dimension selection, in `AREA.md` as `default` and per test under the dimension's name:

| Value | Cells |
|---|---|
| `all` | one per value (the `extra` values only when listed) |
| `[a, b]` | those values (an `extra` value may be named) |
| `{except: [a]}` | all but those |
| `rotate` | exactly one value per combination of the other dimensions, rotated: the value at `(test index + position) mod count`, the test index being the test's position in the area sorted by path (as `qa` did with `LOCALES[(flowIndex+position)%7]`) |

At most one dimension may be `rotate` for a test; an area may define no dimension at all (one cell per
test).

### A test

```markdown
---
title: The header leads from the home page to the launcher and the plans
priority: P0                   # P0 core, P1 important, P2 edge case (default P1)
tags: [eval, navigation]
device: [iphone, pixel, win-chrome]
locale: rotate
tools: []                      # in addition to the area's
exclusive: []                  # shared test data the test changes: no two cells naming one run at once
params: {consent: accept}
timeout: 30m
---
Preconditions, context and edge cases in prose: signed out, no running session. The consent dialog
may appear late; an ad may sit in the layout.

## Steps

1. Open / (the home page in the cell's locale).
   Expect: hero heading and the header with the logo; on a phone the menu button instead of the links.
2. Open New Session from the header (on a phone through the menu).
   Expect: /create opens with the launcher (start URL field, browser grid, Start session).
3. Go back, then open the plans from the header (on a phone through the menu).
   Expect: /upgrade with the plans and their prices.
```

- `## Steps` is required and holds one ordered list; every item is the action (the person's goal, not
  the controls of one device: "open the plans from the header", not "tap Upgrade"), and one line
  starting with `Expect:` that says what must be seen. Item text may span lines and hold inline code.
- Everything else in the body is context and goes into the cell's first message as it is.

### `TOOL.md`

```markdown
---
title: BrowserStack devices
bin: bin                       # folder put first on the session's PATH (relative to the tool)
setup: npm ci --no-audit --no-fund   # once per run, in the run's copy of the tool, before its first cell
secrets:
  command: infra/secretctl show beta device-qa   # run in the repository root; prints NAME=value lines
  names: [BROWSERSTACK_ACCOUNTS, QA_GOOGLE_EMAIL?, QA_GOOGLE_PASSWORD?, QA_GOOGLE_TOTP_SECRET?]   # ? = optional
pool:
  secret: BROWSERSTACK_ACCOUNTS  # one member per entry
  separator: ","
  per_member: 1                # sessions one member carries at once
  status: bin/qa pool-status   # optional: which members are busy outside Socrates (taken last)
end: bin/qa stop               # optional: run after every attempt of a cell that used the tool, whatever happened
---
Instructions for the agent: every command, what it prints, how to type secrets
(`qa type --secret QA_GOOGLE_PASSWORD`), native input, finding controls, ...
```

- `secrets.command` prints `NAME=value` lines, split at the first `=` and taken raw (no unquoting: a
  real secret may hold quotes; an `export ` prefix is dropped), as `secretctl show` prints them and
  `qa`'s `secrets.ts` reads them. Socrates runs it when a run starts and again when it reconciles a run
  after a restart, keeps the values in memory and in the run's vault file (below) only, and checks that
  every name in `names` without `?` came back. Names it printed but `names` does not list are dropped.
- A tool runs from the run's copy: setup, hooks and the commands on `PATH` all see the copy, and
  `SOCRATES_TEST_REPO` names the repository root for a tool that reads repository files (`qa` reads
  `infra/beta-db` and `infra/logs`).
- The installed copy is cached by a hash of the tool's files (`<data>/cache/tests/tools/<hash>/`), so
  `setup` runs only when the tool changed.
- A **pool** member is one entry of the named secret. A member whose value has the form `name:secret`
  is labelled `name` in the UI (BrowserStack's `user:key`); otherwise it is `#n`.
- `status` prints JSON `{"busy": [2, 4]}` (1-based members). Socrates calls it when it hands out a
  lease and leases a busy member only when no other is free; the cell then waits in the provider's own
  queue, as `qa start` did with an account a team member was using.

### Validation

Socrates validates the catalog when the Tests page opens it, before every run, and on
`socrates tests check <repository>` (a subcommand of the binary, usable in a terminal or a hook). A run
does not start on errors. Checked: the front matter keys and types; ids unique; `AREA.md` per area;
`## Steps` present, every step with `Expect:`; dimension selections name defined values; at most one
`rotate`; tools exist and their `TOOL.md` is valid; `bin` exists; the pool's secret is in `names`;
`mcp` names exist in `.mcp.json`; the assembled system prompt of every area stays under its cap.
Warnings (no stop): an area without tests, a test without `priority`, a tool no test uses.

## The tool contract

What Socrates gives a cell session, and through it every tool command the agent runs:

| Variable | Value |
|---|---|
| `PATH` | each tool's `bin` first, then the usual |
| `SOCRATES_TEST` | `1` |
| `SOCRATES_TEST_RUN`, `SOCRATES_TEST_ID`, `SOCRATES_TEST_CELL`, `SOCRATES_TEST_ATTEMPT` | run id, test id, cell key (`web-browser/header-navigation@pixel.es`), attempt number |
| `SOCRATES_TEST_DIM_<NAME>` | the cell's value per dimension (`SOCRATES_TEST_DIM_DEVICE=pixel`) |
| `SOCRATES_TEST_PARAM_<NAME>` | the merged `params` |
| `SOCRATES_TEST_REPO` | the repository root |
| `SOCRATES_TEST_EVIDENCE` | the attempt's evidence folder (below); the tool writes into it |
| `SOCRATES_TEST_RUNTIME` | the attempt's runtime folder, `<data>/run/tests/<attempt id>/` (short id, not backed up): sockets and scratch files go here, never into the evidence (a socket path there would pass the 108-byte `sun_path` limit and land in the backup) |
| `SOCRATES_TEST_VAULT` | the attempt's vault file in the runtime folder (mode 0600): the tool's secrets as `NAME=value` and, per leased pool, `SOCRATES_LEASE_<TOOL>=<member number>` and `SOCRATES_LEASE_<TOOL>_VALUE=<member value>` |

No variable name holds `KEY`, `SECRET` or `TOKEN`: Codex's default shell environment policy drops such
names from the commands it runs.

The agent's own environment carries no secret: it is the tool that reads the vault (as `qa` read
`secretctl` itself). The agent could still read the file through its shell; the protocol forbids it
(accepted by the owner). What Socrates stores goes through redaction: every known secret value of 6
characters or more, and each pool member and each `:`-separated part of it on its own, becomes
`‹NAME›` in records, ingested evidence, links, bug lists and conclusions. A cell's vault holds its own
pool member (the lease), never the pool's list of every member. The session's transcript (the host's
journal and the harness CLI's own session files) is not redacted: an agent that prints a secret against
the protocol leaves it there, until the run and its sessions are deleted.

**Order at the end of an attempt**: the `end` hooks run (with the vault still there), then the vault and
the runtime folder are removed, then the leases are released.

**Hook environment**: `setup` gets the run's copy and `SOCRATES_TEST_REPO`, no secrets. `status` gets
the run-level vault (the tool's secrets, no lease), `SOCRATES_TEST_VAULT` pointing at
`<data>/run/tests/<run>/vault`, which lives while the run is active.

**Evidence.** A tool records what it did as one JSON line per action in
`$SOCRATES_TEST_EVIDENCE/actions.jsonl`, with the files it names beside it:

```json
{"at":"2026-10-28T09:42:31.120Z","tool":"browserstack","action":"tap","summary":"tapped button \"Ajustes avanzados\"",
 "url":"https://beta.browser.lol/es/create","screenshot":"07-tap.png","ms":812,"error":null,
 "details":{"target":{"text":"Ajustes avanzados","native":true},"settle":{"ms":1204,"settled":true},"pageErrors":[]}}
```

`summary`, `action` and `at` are required; `screenshot` (png, jpeg or webp) and `error` are what the
judgement rules read; `details` is shown as it is; `interaction: true` marks an action that acts on the
page (a tap, typing, a key), which the review rule reads. The agent controls the evidence folder:
ingestion and serving take regular files only (`Lstat`, no symlinks), resolved inside the attempt's
folder. `links.json` (`{"Video": "https://...", ...}`) and
`logs/*.txt` are shown on the cell page. A tool that does not write evidence still works: the agent then
attaches files to a judgement itself (`attach`), and `evidence: none` areas need none.

**Hooks.** `setup` runs once per run; `end` runs after every attempt that used the tool (ended,
aborted, timed out or crashed), with the same environment, before the lease is released, so a device
session the agent left open is closed. Both have a 10 min limit; a failed `setup` stops the run with
its output, a failed `end` is logged on the attempt.

## The session of a cell

A cell session is a chat whose options carry the key `test` (`store.TestOption`): its reference, the
role, the repository, the run and the attempt or helper. No schema change: `ListSessions` leaves such
rows out in SQL (`json_extract`), `ListAllSessions` has them. `Kind` stays `chat`, so `IsChat()` still holds and every
chat path (`chatHub.start`, `chatOf`, parking, the host) works unchanged; the pane loops never take it
for a terminal. `IsTest()` is the one check, and each exclusion is written out where it applies:

- **Hidden.** Not in `/api/sessions` or the sessions frame (Home, the sidebar), `chatCounts`, the
  clock's auto-continue (`schedule.go`) or `DoneRetention`'s sweep (all of which read `ListSessions`):
  it lives as long as its run. Its activity never reaches the detector (`chatPeer.report` hands it to
  the runner instead), so no push, no title, no outcome verdict and no activity broadcast; Home is
  sent no digest of it. Costs count it (under the
  run). Its transcript opens read-only from the cell page (the chat view with the composer hidden).
- **Relaunch.** A host is rebuilt from the row on every launch (after parking too, `chats.go`): the
  launch reads the reference from the options and rebuilds instructions, `PATH`, the
  `SOCRATES_TEST_*` variables, the vault and the role's tools from the run's catalog snapshot and the
  attempt's record, so a parked cell comes back the same.
- **Workdir** the repository root, so the catalog's relative paths work; the harness reads the
  repository's `AGENTS.md`/`CLAUDE.md` as it always does.
- **Harness, model, effort, account** from the run's settings for the role. `account` automatic is the
  runner's own pick: `accountHub.pick` alone does not spread a run (it counts no chats and its limit
  readings change only when turns end, so eight cells started together would all land on one account,
  and with every account limited it falls back to the first). The runner ranks the accounts the same way
  and then by how many of the run's running cells each holds, and skips an account at its limit, which is
  what start condition 4 checks. `auto_switch` on, as for chats.
- **Socrates' tools**: the role's test tools (below), no `ask_user` (nobody watches a test cell: what
  it cannot settle is `blocked`) and no chat tools (`list_chats` never shows test sessions, and they see
  no chat). They are built the way the chat tools are, once per harness: Claude's in-process server
  (`claude/askuser.go`), Codex's dynamic tools (`codex/chattools.go`), OpenCode's `socrates mcp-ask`
  subprocess (`agenthost/mcpask.go`); `ask_user`, registered in all three today, becomes optional per
  session. Their names differ per harness (`mcp__socrates__test_judge`, `socrates_test_judge`,
  `test_judge`), so the protocol text takes the prefix, as `chattools.Guidance(prefix)` does.
- **Other MCP servers**: Admin's are off; servers of the repository's `.mcp.json` only when the area
  lists them in `mcp`.
- **Instructions** (a new per-session field `Spec.TestInstructions`, appended after the model's Admin
  instructions). Claude gets its whole system prompt as one argument (`--append-system-prompt`), and
  Linux limits one argument to 128 KiB (`MAX_ARG_STRLEN`), so the test instructions are capped at
  64 KiB, which leaves room for the model's 32 KiB and Socrates' own guidance; the validator checks the
  assembled text per area and role:
  1. Socrates' protocol for the role (compiled in): what a cell is, the test tools, the verdicts, the
     rules (judge only what was seen, never edit the repository, never read the vault, never
     paste a secret, describe what is on screen rather than what should be there).
  2. `TESTS.md`, 3. `AREA.md`, 4. every tool's `TOOL.md` body (cell agents only).
- **First message**, sent by the runner (as the scheduler's start does, `schedule.go`): the cell header
  (test id and title, every dimension with its label, attempt, the leased member's label, the params),
  the test's body with its numbered steps and expectations, and "Begin with step 1".

### Socrates' test tools (MCP, per role)

Cell agent:

| Tool | Does |
|---|---|
| `test_steps` | The steps with their expectations, the verdicts so far and the next step |
| `test_judge` | `{step \| rest, verdict, observed, attach?}`: judges a step; the evidence actions since the previous judgement become its evidence; a pass, issue or fail needs a screenshot among them when the area wants `screenshot`. Judging again replaces the verdict |
| `test_end` | `{summary}`: accepted once every step is judged; one or two sentences on what works and what does not, on this cell |
| `test_abort` | `{reason}`: the cell cannot go on (the device session died); the rest is `blocked` |

Reviewer: `review_list` (its cells), `review_evidence` (a cell's steps, expectations, actions and
screenshot paths, **without** the agent's verdicts and observations, so the reviewer decides first),
`review_agent` (the agent's verdicts, observations and summary), `review_step` (`{cell, step, verdict,
note}`), `review_done` (`{cell, note}`).

Analyst: `run_overview` (every cell with both verdicts, observations and summaries, filterable),
`run_cell` (one cell in full), `bugs_put` (the whole list: `id`, `title`, `severity` critical | major |
minor | cosmetic, `cells`, `description` with the suspected code), `conclusions_put` (Markdown: per area,
per column value, then the bugs by severity).

### Verdicts

Fixed in Socrates, the same for every repository (the area explains what they mean there):

| Verdict | When |
|---|---|
| `pass` | Everything the step expects happens, and nothing looks wrong |
| `issue` | It works, but something is wrong a person would notice |
| `fail` | It cannot be completed, gives a wrong result, loses data, hangs or shows an error |
| `blocked` | Not a product fault: infrastructure, a third party, test data missing, never reached |
| `na` | The step does not exist in this cell by design |

A cell takes its worst step: `fail` > `blocked` > `issue` > `pass` > `na`. The verdict that counts is
the reviewed one where a reviewer changed it; both are kept and shown.

## Runs and the scheduler

**Starting a run** (Tests page → *Run*): a selection (everything, areas, tests, tags, priorities, a
subset of the column values, or "the problems of run X": its cells that were not `pass`/`na`), the
settings (defaulting to the repository's, see Settings) and an optional name. Socrates validates the
catalog, runs the secrets commands and the `setup` hooks, expands the cells and queues them. One run per
repository is active at a time; a second waits until it ends.

**Order**: priority (P0 first), then area and test order, then column order. Among startable cells
the scheduler prefers a column value no running cell is on (spreads the devices over the accounts).

**Cell states**: `queued` → `waiting` (with its reason) → `starting` → `running` → `ended`, or
`aborted` (agent, timeout, crash), `stopped` (the run was stopped), `failed-to-start`. Waiting reasons
are shown as they are: *waiting for a free BrowserStack account (3 ahead)*, *waiting for the fixture
`premium-account`*, *waiting for a free session (4 of 4 running)*, *waiting: every account is at its
limit*.

**What a cell needs to start**, checked together under one lock in the runner (an in-process loop like
`scheduler` in `schedule.go`, woken by every state change):

1. Fewer running cells than the run's `parallel` (when set; otherwise the pools are the limit) and
   than the server-wide `max_sessions` of the Tests page's global settings (default 8, protects the
   host; reviewers and analysts count too).
2. A free lease on every pool its tools have (`per_member` sessions per member; members the `status`
   hook calls busy go last). A lease is part of the attempt's record, so a restart keeps it.
3. No running cell naming one of its `exclusive` names.
4. A harness account: of the ticked ones (or all), signed in and with room first, the one the fewest
   test sessions of the server run on; then, as a chat's own pick does, one Socrates does not know as
   signed in; never one at its limit. A session keeps its account (no automatic switch, which could
   take an account the run may not use); a cell that runs into a limit ends blocked and is retried.

Then: lease, write the attempt's vault, create the `test` session, send the first message. The lease is
released after the `end` hooks of the attempt ran, whatever ended it.

**Limits**: an attempt longer than its `timeout` is aborted (the steps not judged become `blocked`,
"not reached: ..."). There is no idle limit of its own: a tool call may wait long (in BrowserStack's
queue, say), and the timeout bounds the attempt. An agent that does not start aborts the attempt
at once. A turn that ends without
`test_end` gets one automatic nudge ("Judge the remaining steps and call test_end"); a second such end
aborts the attempt.

**Retry**: a cell whose attempt ended `blocked`, aborted or failed to start gets one more attempt at
the end of the queue (`attempts`, default 2). A cell a reviewer turned `blocked` is retried the same
way. *Retry* on a cell page starts another by hand at any time.

**Stop**: queued cells are dropped, running ones interrupted and aborted as `stopped`, the hooks run and
the leases are released. **Delete** removes the run, its sessions and its files.

**Restart of Socrates** (update, crash): the run, its cells and the leases are in the store; on start
the runner reconciles: it runs the secrets commands again and rewrites the run's and the running
attempts' vaults (the values were in memory only), then a running cell whose host still runs goes on,
one whose host is gone is aborted (its `end` hooks run, then it is retried by the rule above), waiting
cells go on waiting. A secrets command that fails now pauses the run with its output instead of
aborting it.

**Finish**: when every cell has its last attempt, the review is done and the analyst finished, the run
is `done`; one push notification says so with the counts ("54 cells: 41 pass, 7 issue, 3 fail, 3
blocked").

### Review and analysis

**What is reviewed** (Device QA's rule, `review.ts`; "passed" means `pass` or `na`): every cell the
agent did not pass; every passed cell with a passed step whose last interaction (an action marked
`interaction`) had an error; one passed cell in five, a stable sample by cell key; and every passed cell
of a test once a reviewed pass of that test turned out wrong. Always the cell's latest attempt; a retry
of a reviewed cell is reviewed again by the same rule. A reviewer session takes about 6 cells; one
starts whenever 6 wait (and for the rest once the cells are done), alongside the cells, never more than
`review_parallel` (default 2) at once.

The reviewer never drives a device: no test tool on its `PATH`, no lease, no vault. It works in the
run's folder (`<data>/tests/<repo-key>/runs/<run>/`), reads the screenshots with its harness' own file
tools (`review_evidence` gives their absolute paths) and writes only through the review tools. The
analyst works in the repository root, so it can read the code it names as the suspected cause, and
reads the run through its tools and the same paths.

**Analysis**: once the review list is empty, one analyst session reads the run (and the repository's
code, to name the suspected cause) and writes the bugs and the conclusions. *Analyse again* on the run
page repeats it, for example after cells were retried.

## Storage

The files are the truth, and the only store: the runner reads every run at start into memory and
lists, filters, searches and builds the history from there. The repositories and the settings are in
the store's `kv` (`tests.repos`, `tests.settings.<repo>`, `tests.global`).

```
<data>/tests/<repo-key>/                    repo-key: the folder's name and a short hash of its path
  runs/<run-id>/                            run-id: r-20261029-0915-abcd
    run.json                                selection, settings, commit, state, order of the attempts, helpers, times
    plan.json                               what the run took from the catalog: areas, dimensions, prompts, tools
    catalog/                                the catalog as it was run (without tools/)
    cells/<test id>/<cell>/<attempt>/       cell: the dimension values joined, pixel.es
      record.json
      evidence/                             $SOCRATES_TEST_EVIDENCE: actions.jsonl, links.json, screenshots, logs/
    bugs.json
    conclusions.md
<data>/run/tests/<run>/vault                   the run's secrets while it is active (run/ is never backed up)
<data>/run/tests/<attempt>/                  SOCRATES_TEST_RUNTIME: the attempt's vault, sockets; removed at its end
<data>/cache/tests/tools/<name>-<hash>/      the installed copy of a tool a run runs from, made by its
                                             setup once per version of the tool (the last 4 are kept)
```

`record.json` of an attempt is `testmode.Attempt` (`internal/testmode/record.go`): the cell (test,
dimension values, parameters, tools), its state and why it ended, the role it ran with and the account
it took, its leases, its times and how long it waited, its steps with their judgements and reviews, the
actions its tools recorded, its verdict and the reviewed one, its summary, review, bugs and links.

**Backup**: everything under `tests/` is backed up (the owner wants the screenshots too). **Retention**:
the last `keep_runs` runs per repository (default 20; a run can be pinned) are kept, older ones deleted
with their sessions, after the run that pushes them out is done.

## Settings

Per repository (in the store's `kv`, edited on the Tests page, overridable per run):

| Setting | Default |
|---|---|
| Cell agent: harness, model, effort, the accounts it may use | Claude Code, Sonnet 5.5, medium, every account |
| Reviewer: harness, model, effort, the accounts it may use | Claude Code, Opus 5.5, high, every account |
| Analyst: harness, model, effort, the accounts it may use | Claude Code, Opus 5.5, high, every account |
| `parallel` (cells at once) | 0: the pools are the limit, else 4 |
| `review_parallel` | 2 |
| `review` | on |
| `attempts` | 2 |
| `analysis` | on |
| `keep_runs` | 20 |

The model defaults follow Device QA's model evaluation (`PLAN.md`, 2026-10-28: Sonnet 5.5 judged the
steps as well as Haiku 5.5 with fewer unsupported claims and fewer missed problems; Opus reviews). Over
every repository there is one setting, `max_sessions` (default 8), on the Tests page.

## API

All behind `s.auth`, in `internal/server/tests*.go`:

| Route | Does |
|---|---|
| `GET /api/tests/repos`, `POST`, `DELETE /api/tests/repos/{repo}` | Repositories with a catalog: the found ones (Admin presets and chat folders whose git root holds `.socrates/tests/TESTS.md`) and ones added by path |
| `GET /api/tests/repos/{repo}/catalog` | The parsed catalog, its errors and warnings, the expanded cell count |
| `GET`/`PUT /api/tests/repos/{repo}/settings` | Settings |
| `GET /api/tests/repos/{repo}/tools`, `POST …/tools/{tool}/check` | Tools, pool members and their leases; the check runs the secrets command and reports the names found (never values) |
| `POST /api/tests/repos/{repo}/runs`, `GET …/runs` | Start a run; list runs |
| `GET /api/tests/runs/{run}`, `POST …/stop`, `POST …/analyse`, `POST …/pin`, `DELETE` | One run |
| `GET /api/tests/runs/{run}/cells?area=&verdict=&dim.device=&q=` | The results table's rows |
| `GET /api/tests/runs/{run}/cells/{test…}/{cell}/{attempt}`, `POST …/retry` | A cell attempt in full; retry |
| `GET /api/tests/repos/{repo}/history?test=&cell=&limit=` | Verdicts of a test or cell over the last runs |
| `GET /api/tests/repos/{repo}/search?q=` | Full-text search over every run |
| `GET /api/tests/files/{run}/{path…}` | Evidence files, on the pattern of `handleChatPartImage` (resolved on the server, regular files only, size cap, `nosniff`, `private`) |

Live updates: a `tests` frame on `/api/events` (run state and counts, cell state changes) feeds the
Tests page and the Home card.

## The Tests page

Reworked the same evening (owner: "best practices, extremely intuitive, every function it needs";
see "Revision: the reworked Tests page" for what was added on the server). The first version is
described in git history (46aa6ca); this is the page as it is now.

A page of its own, `/tests` (`tests.html`, `js/tests*.js`, `css/so-tests.css`), with a drawer link
beside Costs and Admin. Built from Dashboard-Style components only (AGENTS.md rules, `make
ds-check`). Where the person is lives in the hash, so every place can be linked to and Back undoes
every step: `#repo=<key>&view=<overview|runs|tests|bugs|settings>` and, inside a view, `run=<id>`
(a run), `test=<id>` (a test), `bug=<id>` (a bug), `cell=<attempt id>` (the cell viewer, over any
view; its run is `run=`).

**Head.** The repository's title (a menu to switch when there are several, with *Add repository*),
its path and checkout. Actions: **Run** (a split button: the main part opens the Run dialog; its
menu has *Run everything*, *Run the problems of run N again* when the latest run has any, and one
item per priority present, e.g. *Run P0*). While a run of the repository is active, a banner under
the head says how far it is (progress, "23 of 54 cells · 4 running · 3 waiting for a BrowserStack
account · about 18 min left · $3.20") with *Watch* (opens the run) and *Stop*.

**Views** (`ds-tabs`): Overview · Runs · Tests · Bugs (badge: open bugs) · Settings.

- **Overview**: how the repository stands. The latest finished run as tiles per verdict with the
  change against the run before (`+2`), its pass rate; **Needs attention**: cells that broke since
  the previous run (regressed), cells still failing (with "since run N"), flaky tests, and the open
  critical and major bugs; **Fixed since the run before**; a **trend** of the last 20 runs (a stacked
  bar per run, pass · issue · fail · blocked, a bar opens its run). While a run is active, **Live**:
  a card per running cell with its latest screenshot, its step ("Step 2 of 4: Open New Session"),
  its device and how long it runs, the waiting cells with their reasons and the pools. Empty
  repository (no run yet): the catalog's size and *Run everything*.
- **Runs**: every run, newest first: number and name, when, what it ran, a result bar with the
  counts, regressed and fixed against the run before, duration, cost, state. A run opens the **run
  view**: its head (state, commit, when, duration, cost, the roles' models) with *Stop*, *Run the
  problems again*, *Analyse again*, and a menu (*Copy report*, *Download report*, *Pin*, *Delete*);
  then **Results**, **Bugs** (this run's) and **Details** (settings, reviewers and analyst with
  their transcripts, pools, why it failed).
  - Results: a search box (filters this run's cells by text), chips *All · Problems · Changed ·
    Reviewed · Overridden*, a *Filters* button (area, priority, tag, every dimension) with the
    active filters as removable chips. Per area a table: a row per test, a column per value of the
    area's `columns` dimension; each cell its verdict (`ds-status`, glyph and word), the other
    dimensions' values as a tag, a change mark (regressed, fixed) and a review or override mark. A
    row's actions: *Run this test again*. On a phone a test is a block with its cells as a list.
- **Tests**: the catalog and how each test stands, together. Catalog errors and warnings first
  (`ds-alert`, with file and line), then per area its tests: title, priority, tags, the latest
  verdict of each of its cells (from the latest run that ran it), "failing since run N", a flaky
  mark, and *Run*. *Write a test with an agent* opens the chat dialog. A test opens the **test
  view**: its file, priority, tags, context and steps with their expectations; its history as a
  grid (a row per cell, a column per run of the last 20, each a verdict glyph that opens that
  attempt); its open bugs; *Run this test*, *Run this cell* per row, *Change it with an agent*.
  The tools (pools, *Check secrets*) sit at the end of the view.
- **Bugs**: the repository's bugs over every run (below): chips *Open · Known · Fixed · Not a bug ·
  All*, sorted by severity and then by when last seen. A row: severity, id, title, status, how many
  runs it was seen in, first and last seen, its cells, its GitHub issue. A bug opens the **bug
  view**: description, its cells (each with its latest verdict and the screenshot of its first
  failing step), the runs it was seen in, the triage (status and a note), *Fix in a chat*, *Create
  GitHub issue* (or the issue's link), *Run its cells again*.
- **Settings**: the roles (program, model, effort, accounts), how a run goes (cells at once,
  attempts, review, analysis), the MCP servers of cells, runs kept, and this server's limit; saved
  with a `ds-savebar`.

**The cell viewer** (`ds-dialog--full`, on every view, phone and desktop): its head the test's title,
the cell's dimensions, the attempt (a switcher when there are several), the verdict as it counts with
who gave it ("the agent said Pass, the reviewer made it Issue, you made it Pass"), previous and next
cell of the list it was opened from (`[` and `]`, or `j`/`k`). On a wide screen the steps on the left
(action, expectation, observation, verdict, reviewer note), the selected step's screenshots large on
the right with a film strip and ← → over every screenshot of the cell; *Compare with the last pass*
puts the same step of the cell's latest passing attempt beside it. On a phone the steps are a list,
each with its screenshots at full width. Below the steps: the summary, the actions log, links and
logs, the facts (agent, account, lease, times, cost). Actions: *Override verdict* (a verdict and a
note; *Remove override*), *Retry*, *Fix in a chat*, *Open transcript*. A running cell is live: its
steps fill as they are judged and its latest screenshot is shown.

**The Run dialog** (`ds-sheet`, wide): what to run as radio cards with their cell counts:
*Everything*, *The problems of run N*, one per priority, *Choose…* (a tree of areas and tests with
tri-state checkboxes, chips for priorities, tags and every dimension's values); the settings of this
run as one summary line with *Change* (the settings form); an optional name. The foot says the exact
number of cells, about how long it takes and what it costs (from earlier runs), and *Start run*.

**The chat dialog** (*Fix in a chat*, *Write a test with an agent*, *Change it with an agent*): the
program, model and effort (as the new-session sheet offers them), the message, prefilled and
editable (the bug or the cell with its steps, observations and screenshot paths; or what a test is
and where the format is described), the repository as the folder. *Start chat* opens the chat.

**Home** shows an active run as one card ("Tests · browser.lol · 23 of 54 cells · 4 running · 3
waiting for BrowserStack") that opens the run.

## Code layout in Socrates

| Where | What |
|---|---|
| `internal/testmode/catalog` | Parsing (front matter, a strict YAML subset of its own: no dependency, so the binary stays MIT/BSD/ISC), `## Steps`, validation, expansion into cells, snapshot, `ParseSecrets` |
| `internal/testmode` | The runner: preparation and plan (`plan.go`), the scheduler, leases and sessions (`runner.go`), review and analysis (`review.go`), the test tools' calls (`calls.go`), stop, delete, retry, retention, repositories and settings (`control.go`), the views, history and search (`view.go`), evidence (`evidence.go`), secrets, installed tools and hooks (`tools.go`), redaction |
| `internal/agent/testtools` | The test tools per role (definitions, arguments, guidance with the harness' prefix), beside `chattools` and `askuser` |
| `internal/agent/chattools` | `Endpoint.Kind`: the role of a test session, whose tools the endpoint offers instead of the chat tools |
| `internal/agent/claude`, `codex`, `opencode`, `askuser`, `agenthost/mcpask.go` | Offering the test tools and leaving out `ask_user` and the chat tools per session, in each harness' own mechanism; `TestInstructions` after the model's instructions |
| `internal/store` | `TestOption`, `IsTest()`, `TestRef()`, `ListSessions` without and `ListAllSessions` with test sessions |
| `internal/agenthost` | `Spec.TestInstructions` (also kept across an account switch) |
| `internal/server/tests.go`, `tests_api.go` | The runner's host (sessions, the account pick, events, push, kv), the launch extras, the activity hand-over, the API |
| `internal/paths` | `tests/`, `run/tests/`, `cache/tests/` |
| `testscmd.go` | `socrates tests check [repo]` |
| `internal/web/static` | `tests.html`, `js/tests*.js`, `css/so-tests.css`, the Home card, drawer links, the push target |
| `e2e/testmode.mjs`, `e2e/fakebin/fakeclaude/testcell.go` | The browser scenario `testmode`, and the fake agent's cell, review and analysis |

## Tests of the feature

- Unit (Go): the parser and validator on fixtures (the converted browser.lol catalog among them),
  expansion and `rotate`, the scheduler with a fake clock (pool capacity, `per_member`, busy members,
  exclusive names, the parallel limits, retry, stop, restart reconciliation), the secrets command
  parser, evidence ingestion and the screenshot rule, redaction, the review sample, the index rebuild.
- End to end (`e2e/run.mjs`, new scenario `tests`): a fixture repository with a catalog of two areas and
  a fake tool (`e2e/fakebin/faketool`: writes `actions.jsonl` and a generated PNG, a pool of two
  members). `fakeclaude` can already call Socrates' tools (`/mcptool`) but runs no process and its
  scripts are keyed by exact message text, so it gets a test verb: it recognises the runner's first
  message by its header, really runs `faketool` from `PATH` (the fake's first `os/exec`), then works the
  cell through the test tools (judge every step, end; one variant fails a step, one aborts). It checks the queue (three cells, two members), the
  results table, the cell sheet with its screenshots, retry, stop, the Home card, and that test
  sessions stay off Home. `/tests` joins `pages()` (no console errors, no sideways scroll, 390 and
  1280 px).
- `make check`, `make ds-check`; the README screenshot of the Tests page through `e2e/shots.mjs`.

## Documentation

- `README.md`: a feature row and a short "🧪 Tests" section with the screenshot.
- `docs/OPERATIONS.md`: `<data>/tests/`, the backup and retention, the secrets command's environment
  (it runs as the Socrates service user, in the repository root).
- `docs/design/DECISIONS.md`: a revision entry pointing here; `AGENT-HOST.md`: the `test` kind and
  `TestInstructions`; `HARNESS-POLICY.md`: the MCP and instructions of test sessions.
- A format reference for repositories, `docs/TESTS-FORMAT.md` (this note's catalog and tool contract
  sections, kept as the user-facing copy).

## Order of work

1. Catalog: parser, validator, expansion, `socrates tests check`, with the browser.lol catalog as a
   fixture (written in step 9's format first, so the format is proven on the real thing early).
2. Store and records: the `test` column, index tables, data layout, record and evidence ingestion, redaction.
3. Sessions: the `test` kind kept off every chat path, `TestInstructions`, env and `PATH`, the
   per-role MCP tools, the read-only transcript.
4. Runner: pools and leases, secrets, hooks, the scheduler, limits, retry, stop, reconciliation.
5. Review and analysis sessions.
6. API and the events frame.
7. The Tests page and the Home card.
8. The e2e scenario, docs, screenshots; `make check`, then commit to `main` and push.
9. The browser.lol conversion (below), then a first real run of the six tests with the owner.

## Converting browser.lol's Device QA

Source: the branch `device-qa-tooling` (worktree `/root/wt-device-qa`): six "Device QA" commits on top
of the `beta` line. `beta` holds all of `migration/step-1` and is the target; `main` gets it with the
step-1 merge. The work is a branch from `origin/beta`, merged into `beta` per the repository's rules.

**Carried over from the branch** (not part of `tests/devices`):
`database/seed/browser_v7/50_device_qa.sql` and its line in `database/AGENTS.md` (the QA mailbox).
`beta` already has the `device-qa` export in `infra/secrets/targets.conf` (`BROWSERSTACK_ACCOUNTS`, the
`QA_*` logins marked optional) and `wiki/test-accounts.md`; that `secretctl show beta device-qa` returns
the four accounts there is checked, and the values are set with `secretctl` if not (the `.age` file is
binary and never cherry-picked).

**The new tree:**

```
.socrates/tests/
  TESTS.md                 beta only (QA_BASE_URL never production); beta data is disposable and reset by every
                           push to beta; secrets are typed by the tool, never pasted; mail is per cell
  README.md                PLAN.md's lasting part: goal, decisions, device matrix, what the probes established,
                           the findings so far, the budget; "how to run" now points at Socrates
  tools/browserstack/
    TOOL.md                front matter as in the example above; body: the commands, "How the devices behave",
                           native input, finding controls, consent and ads, settling, errors, logins, broken sessions
    package.json …         the driver as a package of its own with its own lock (tsx, yaml, zod), not a
                           workspace: Socrates installs it in the run's copy; CI runs its checks with
                           `npm ci --prefix` in the unit-other job
    bin/qa
    src/ …                 the driver, reworked as below
    test/ …
  web-browser/
    AREA.md                tools [browserstack]; device (9 matrix values + win-edge, iphone-se-ios15 as extra,
                           default all), locale (en de fr es it pt ja, default rotate); params consent: accept;
                           evidence: screenshot; body: "Working a cell" and the area's verdict nuances
    header-navigation.md   P0, tags [eval, probe], device [iphone, pixel, win-chrome]   3 steps
    create-form.md         P0, tags [eval, probe], device [iphone, pixel, win-chrome]   4 steps
    language-switch.md     P1, tags [eval], device [iphone, win-chrome]                 3 steps
    pricing-toggle.md      P1, tags [eval], device [pixel, win-chrome]                  3 steps
    not-found.md           P2, tags [eval], device [iphone, win-chrome]                 2 steps
    sign-in-validation.md  P1, tags [eval], device [pixel, win-chrome]                  3 steps
```

`probe.yaml`'s two flows are earlier versions of `eval.header-navigation` and `eval.create-form` (P2,
devices in another order, `locales: rotate`, `edge_cases`, a few words different). Each pair becomes one
test, worded as the eval flow, with the probe's edge cases in its context and both tags; a model
evaluation is then a run selected by the tag `eval`. The eval flows name no locales, which in `qa` meant
`rotate`, the area's default here too. The real catalog (Device QA's "step 2") is written straight in
this format. `out/` (with `roles-catalog/` and the old runs) is gitignored and not converted.

**Mapping of `tests/devices/AGENTS.md` and `PLAN.md`:**

| Today | Goes to |
|---|---|
| Contracts: beta only, secrets never through the agent, mail per cell | `TESTS.md` |
| Contracts: slots, `--slot`, claiming, `--poll`, `exclusive` | Socrates (pool, queue, `exclusive` front matter); dropped from the text |
| Working a cell | `AREA.md`, with `qa judge`/`qa steps`/`qa end` replaced by `test_judge`/`test_steps`/`test_end` |
| Running in parallel, Reviewing a cell, Roles and models, Execution model | Socrates (runner, review, analysis, role settings); the reviewer's rules become Socrates' reviewer protocol, which already says the same in general terms |
| Verdicts | Socrates' fixed table; the browser.lol examples (an ad covering a control is an `issue` of the layout; Google or Microsoft challenges are `blocked`) in `AREA.md` |
| How the devices behave | `TOOL.md` (the driver's behaviour) and `AREA.md` (what counts as a finding) |
| After the run: `bugs.yaml`, `conclusions.md`, report, publish | Socrates' analyst and Tests page; `qa report`/`qa publish` and the Pages project `browser-qa` are dropped |
| Checks | the tool workspace's `type-check`, `lint`, `test` |
| PLAN.md decisions, matrix, probe findings, budget | `.socrates/tests/README.md` |

**Reworking the driver** (`tests/devices/src` → `tools/browserstack/src`):

- Kept: `cli.ts` (trimmed), `config.ts` (`QA_BASE_URL`, never production; the timeouts), `daemon.ts`,
  `webdriver.ts`, `session.ts` (actions, guards, consent, settle, collector, native taps),
  `calibrate.ts`, `src/browser/*.js`, `devices.ts` (its ids must equal `AREA.md`'s device values; a
  unit test reads `AREA.md` and checks both directions), `mail.ts`, `totp.ts`, the redaction of
  credential query values, and the cell commands `open`, `find`, `tap`, `type`, `key`, `scroll`,
  `rotate`, `back`, `forward`, `reload`, `wait`, `look`, `shot`, `keyboard`, `env`, `address`, `mail`,
  `logs` (OneUptime, with the cell's time window from the record).
- The daemon: one per cell instead of per slot, its socket in `$SOCRATES_TEST_RUNTIME/qa.sock`; it
  takes the account from `SOCRATES_LEASE_BROWSERSTACK_VALUE` and every other secret from
  `$SOCRATES_TEST_VAULT` (`secrets.ts` no longer calls `secretctl`); device and locale from
  `SOCRATES_TEST_DIM_*`, the consent policy from `SOCRATES_TEST_PARAM_CONSENT`, repository paths
  (`infra/beta-db`, `infra/logs`) from `SOCRATES_TEST_REPO`; the BrowserStack build is the run id, the
  session name the cell key. Its 20 min idle abort stays (Socrates' `end` hook also closes it).
- `record.ts` shrinks to the evidence writer: every action appends its line to `actions.jsonl` with its
  screenshot beside it (`interaction: true` for `tap`, `type`, `key`); the session's video, HAR and log
  links go to `links.json`.
- New: `qa start` (no flags: the cell is in the environment), `qa stop` (the `end` hook: closes the
  BrowserStack session and the daemon, idempotent), `qa pool-status` (the members busy outside, from
  `plan.json`).
- Removed: `slots.ts`, `catalog.ts`, `review.ts`, `report.ts`, the commands `run`, `catalog`, `slots`,
  `status`, `next`, `steps`, `judge`, `end`, `abort`, `review`, `report`, `publish`, `mailbox`, the
  `--slot`/`QA_SLOT`/`QA_CATALOG` options, `out/`, the `sharp` dependency, `slots.test.ts`,
  `catalog.test.ts`, `review.test.ts`, the `/root/.cloudflare-token` read.
- `qa totp` printed a code to the agent; it goes (`qa type --totp` stays).

**Removed from the repository:** `tests/devices/` (with `eval/` and `PLAN.md`), its entry in the root
`package.json` workspaces, `package-lock.json` and its row in `scripts/check-package-graph.mjs`, and
the check that image builds still pass without it (they took `tests/*`).
`wiki/docs/22-testing-and-qa.md`'s section "Device QA on BrowserStack" is rewritten to point at
`.socrates/tests/` and Socrates; `AGENTS.md` names `.socrates/tests/` where it lists the test suites.

**Done when:** `socrates tests check /root/browser` is clean on the branch, the tool's unit tests pass
in CI, and a run of the `eval` tag on the four BrowserStack accounts in Socrates gives the same verdicts
as the roles test of 2026-10-28 (`out/runs/roles-test-2026-10-28/conclusions.md` in the worktree,
gitignored) or explains each difference.

## Revision: built-in MCP servers (2026-10-29)

At the owner's request the BrowserStack driver left the catalog: it is an MCP server built into
Socrates (MCP.md), usable in every chat too. For test mode that means:

- **`AREA.md` `mcp:`** may name a built-in server (`mcp: [browserstack]`) as well as a server of the
  repository's `.mcp.json`. The catalog check runs the server's own check of the area (BrowserStack:
  the `device` values are its devices, `consent` is accept, reject or leave), and the server's
  instructions join the cells' prompt in place of a `TOOL.md` body.
- **Pools.** A built-in server with accounts (`mcpserver.Pool`) is leased like a tool's pool, before
  the session starts, after the harness account: the lease (`Lease.MCP`, the account's id) is kept in
  the attempt's record and taken again after a restart; the server's pool, shared with the chats,
  keeps who holds what. A cell waits "for a free BrowserStack account (n ahead)".
- **The end.** After the end hooks the runner has the server close what it kept for the session
  (`Host.EndMCP`: the device, whose links land in `links.json`), then gives the account back.
- **The cell's facts** reach the server with each call (`Runner.MCPCell`: run, cell key, attempt,
  dimensions, parameters, evidence folder), where `qa` read `SOCRATES_TEST_*`.
- **Settings.** The repository's Tests settings have `mcp`: the MCP servers cell agents get besides
  those their area names (built-in, Admin's or the repository's).

## Revision: the reworked Tests page (2026-10-29, evening)

The owner asked for the Tests page to be redone "by best practices, extremely intuitive, with every
function it needs", and chose: live cells, bugs tracked over runs, manual triage, *Fix in a chat*,
runs started by chat agents, *Write a test with an agent*, a report to copy or download, GitHub
issues from bugs; no scheduled runs. The catalog format stays as it is (nothing below needs it to
change). What the server gained for it:

**Run numbers.** `Run.Number`: 1, 2, 3 … per repository, given when a run is created (the largest
so far plus one; runs of before are numbered by their creation time when they are loaded). The page
names a run "Run 12" (and its name, if it has one).

**Selection.** `Selection.Cells`: cell ids (`web/checkout@a`) to run, narrowing like the other
lists. `ProblemsOf` may be `latest`: the repository's latest run that is not active, resolved to
its id when the run is created (the id is what is stored).

**Override.** A person may override a cell attempt's verdict: `Attempt.Override` `{verdict, note,
at}`. The verdict that counts is the override, else the reviewed one, else the agent's (`Final`),
everywhere: counts, history, changes, bug fixing, the problems of a run. Removing it puts the
verdict back.

**Changes against the run before.** Every row of a run's cells carries `prev`: the same cell's
verdict (as it counts) in the latest earlier run of the repository that has an ended attempt of it
(`{verdict, run, number, attempt}`), and `change`: `regressed` (it passed, pass or na, and now is
issue or fail), `fixed` (it was issue or fail and now passes), `new` (no earlier run had it) or
empty. A run's view counts them (`changes: {regressed, fixed}`).

**Health of a test.** Over the last 20 runs of the repository, per cell: the latest verdict and its
run, `since` (the first run of the current streak of problems: issue or fail), `runs` and `passes`,
`flips` (changes between passed and issue/fail, blocked runs skipped) and `flaky` (at least 2 flips
with both sides present). A test is flaky when one of its cells is.

**Bugs over runs.** The repository keeps one bug list, `<data>/tests/<repo>/bugs.json` (backed
up), of `TrackedBug`: `id` (`B-1`, `B-2` …, given by Socrates), `title`, `severity`, `status`
(`open`, `known`, `not_a_bug`, `fixed`), `description`, `cells` (as last reported), `runs` (the
runs that reported it), `first_run`, `last_run`, `fixed_in`, `regressed` (it came back after it
was fixed), `note` (the person's), `issue` (a URL), `updated_at`, and `events` (`{at, run, what}`:
reported, fixed, reopened, status set by hand, issue created).

- The analyst gets a new tool `bugs_known`: the list (every bug but the ones fixed more than 10
  runs ago), with id, title, severity, status, cells, description and note. Its protocol says: the
  same root cause keeps its id; a bug the person marked `not_a_bug` is not reported again unless
  the evidence shows something else; `known` bugs are reported again when seen (so they stay known).
- `bugs_put` takes an `id` per bug that is either an id from `bugs_known` or empty (a new bug; the
  analyst's own ids such as `B1` are taken as new too). The run's `bugs.json` keeps the list with
  the ids Socrates settled, so a run's page and the repository's agree.
- When the analysis of a run ends (or the run ends without one), the list is brought up to date:
  every reported bug gets the run (`last_run`, `runs`); a `fixed` one is reopened (`open`,
  `regressed`); an `open` bug the run did not report whose cells the run ran and all of which pass
  now becomes `fixed` (`fixed_in`). `known` and `not_a_bug` are only changed by the person.
- The person sets `status`, `severity` and `note` (`PATCH`); a GitHub issue made from the page sets
  `issue`.

**Live cells.** A running attempt's row has `live`: `{step, steps, activity, shot, at}` - the first
step not judged yet, the number of steps, the last action's summary, its latest screenshot and
when. The runner reads the attempt's new evidence lines for it (at most every 2 s per attempt).
The page reads the cells again every few seconds while it shows a running run.

**Times and costs.** A run's view has `cost` (USD of every session of the run: cells, reviewers,
analyst, from the costs ledger; absent when unknown) and, while active, `eta_ms` (the cells left
times the median duration of a cell in the repository's last runs, divided by how many run at
once). `POST /api/tests/repos/{repo}/estimate` `{selection, settings}` expands a selection as a run
would and answers `{cells, tests, eta_ms, cost, errors}`, which the Run dialog shows before
anything starts.

**Report.** `GET /api/tests/runs/{run}/report.md` (`?download=1` as a file): the run in Markdown -
its head, the counts and changes, the bugs by severity, every problem cell with its failing steps
and observations, and the conclusions.

**GitHub issues.** `GET /api/tests/repos/{repo}/github` says whether the repository's `origin` is on
GitHub and `gh` is signed in (`{available, repo, reason}`); `POST
/api/tests/repos/{repo}/bugs/{id}/issue` `{title, body}` runs `gh issue create` in the repository
and keeps the issue's URL on the bug.

**Runs started by chat agents.** A chat whose repository has a catalog (or that was started from
the Tests page) gets four more tools besides the chat tools: `tests_format` (the catalog format
reference), `tests_check` (validates the catalog: errors and warnings with file and line, cells),
`run_tests` (a selection - areas, tests, cells, tags, priorities, dimension values, or the problems
of the latest run - and an optional name; starts a run with the repository's settings and answers
its number, id and cell count) and `test_results` (a run - default the latest - in Markdown: counts,
changes, every problem cell with its failing step, observation and screenshot paths, the bugs). A
run remembers the chat that started it (`Run.StartedBy`); when it ends, Socrates sends that chat a
message with the results, which starts its agent, as `send_to_chat` does. The person sees the run
on the Tests page like any other ("started by <chat title>").

As built (branch `tests-b2`), where it differs from or adds to the paragraph above:

- **Which chats.** The four tools come with the chat tools on the same server and through the same
  endpoint (`chattools.Endpoint.Tests`, `internal/agent/chattools/catalogtools.go`), so every
  harness offers them its own way (Claude Code's in-process server, Codex's dynamic tools, OpenCode's
  `socrates mcp-ask`). A chat has them when its repository holds `.socrates/tests/TESTS.md` as its
  host starts (a catalog written later counts from the next launch), or when it was created with
  `tests: true` (`POST /api/chats`, the session option `test_tools`); never a test session.
- **`tests_format`** is `internal/testmode/catalog/FORMAT.md`, embedded in the binary: the
  user-facing reference (`docs/TESTS-FORMAT.md` points at it), checked against the parser's keys
  by a unit test. **`tests_check`** answers the problems with file and line, then every area with
  its dimensions and every test with its priority, tags and cell ids.
- **`run_tests`** (`Runner.StartChatRun`) refuses before anything starts: no catalog, a catalog with
  errors, a selection that names an area, test, cell, priority, dimension or value the catalog does
  not have (it says which there are), or one that takes no cell; `problems_of: "latest"` is resolved
  to the latest run that is not active. It adds the repository to the list as `POST
  /api/tests/repos` does and answers the run's id and cell count (the run's number once runs have
  one), and that a run it waits behind comes first.
- **`test_results`** is `Runner.Report` with the screenshots' absolute paths, the problems only
  unless `problems_only` is false, cut at 40 KB with a note; `run` is an id of the chat's repository
  or `latest` (its newest run, active or not).
- **The message.** When the run is done, stopped or failed, the chat gets one message through the
  path `send_to_chat` takes (`sendMessage`: it starts the agent or waits behind its turn), after a
  `test_results` notice ("The results of the test run this chat started."). It opens with a line
  for the agent ("[From Socrates Tests: ...]", left out of the bubble), then the run's state and
  counts, a line per cell that did not pass (at most 10, its first step with that verdict and what
  was observed) and "Call test_results". `StartedBy.Told` is set and saved before it goes, so a
  second end or a restart never sends it again; a run that ended without it (a restart in between)
  is told when the runner starts. A chat that is gone is not told. A cell retried by hand after the
  message does not send another.

**API additions**:

| Route | Does |
|---|---|
| `GET /api/tests/repos/{repo}/overview` | `{latest, previous, trend, regressed, failing, fixed, flaky, bugs}` |
| `GET /api/tests/repos/{repo}/catalog` | As before, and every test's `health` and every cell's latest verdict |
| `GET /api/tests/repos/{repo}/tests/{test...}` | A test: its catalog entry and its history grid over the last 20 runs |
| `POST /api/tests/repos/{repo}/estimate` | What a selection would run, how long and what it would cost |
| `GET /api/tests/repos/{repo}/bugs`, `GET …/bugs/{id}`, `PATCH …/bugs/{id}` | The bug list, a bug with its cells, triage |
| `GET /api/tests/repos/{repo}/github`, `POST …/bugs/{id}/issue` | GitHub issues |
| `POST /api/tests/runs/{run}/attempts/{attempt}/override` | `{verdict, note}`; an empty verdict removes it |
| `GET /api/tests/runs/{run}/report.md` | The report |

## Risks and open points

- **Secrets in reach of the agent.** The vault keeps them out of prompt and environment, not out of
  reach: an agent with a shell, running as the same user, can read it. Accepted by the owner;
  redaction covers what Socrates stores and shows, not the harness CLI's own session files.
- **The live working tree.** Cells work in the repository's checkout while other chats may change it;
  the catalog and the tools are snapshotted, but repository files a tool reads at run time
  (`infra/beta-db`, `infra/logs`) are the checkout's.
- **Waiting inside the agent.** A member a team member uses is leased last, but when it is leased, the
  cell waits in BrowserStack's queue inside `qa start`, with the agent's turn open (tokens are spent on
  the turn, not on waiting). A shell call has its own limit (Claude's Bash at most 10 min), so
  `TOOL.md` says to run `qa start` again when it comes back with "still queued".
- **Long lists of cells.** A few hundred test sessions per run is within AGENT-HOST.md §10's limits;
  test sessions park like chats, and retention deletes them with their run.
- **Codex and OpenCode as cell agents** are supported through the same tools and instructions, but
  only Claude Code was evaluated for judging screenshots; the role settings say so.
- **Rotation shifts** when tests are added (the test index changes); history is per cell key, so a
  test's locale history moves to another cell. Acceptable, as with `qa`.
