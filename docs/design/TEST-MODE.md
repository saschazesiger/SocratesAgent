# Test mode: agent-run test catalogs per repository (plan, 2026-10-29)

Status: planned with the owner on 2026-10-29; not built yet. This note is the specification the
implementation follows, including the conversion of browser.lol's Device QA (BrowserStack) into the
new format.

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

- Scheduled runs, runs started by a chat agent, GitHub issues from the bug list (later, if wanted).
- Record and replay: every cell is run live by an agent (Device QA decided the same on 2026-10-28).
- Tools as MCP servers: tools are command-line programs (decided below); the only MCP tools of a test
  session are Socrates' own.

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
`‹NAME›` in records, ingested evidence and links, and, for test sessions, in the host's journal (the
redaction list goes to the host sealed like `SecretEnv`, and its journal writer applies it). The
harness CLI's own session files (Claude's `projects/*.jsonl`, Codex's rollouts) stay as the CLI writes
them; they are deleted with the run.

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

A cell session is a chat whose row carries the new column `test` (the attempt id; reviewer and analyst
sessions carry the run id and their role). `Kind` stays `chat`, so `IsChat()` still holds and every
chat path (`chatHub.start`, `chatOf`, parking, the host) works unchanged; the pane loops never take it
for a terminal. `IsTest()` is the one check, and each exclusion is written out where it applies:

- **Hidden.** Not in `/api/sessions` or the sessions frame (Home, the sidebar), `chatCounts`, push
  notifications, the title and outcome judges, the clock's auto-continue (`schedule.go`), `onSessionActivity`'s
  activity broadcast, or `DoneRetention`'s sweep: it lives as long as its run. Costs count it (under the
  run). Its transcript opens read-only from the cell page (the chat view with the composer hidden).
- **Relaunch.** A host is rebuilt from the row on every launch (after parking too, `chats.go`): the
  launch reads the attempt from the `test` column and rebuilds instructions, `PATH`, the
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

1. Fewer running cells than the run's `parallel` and than Admin's server-wide `tests.max_sessions`
   (default 8, protects the host).
2. A free lease on every pool its tools have (`per_member` sessions per member; members the `status`
   hook calls busy go last). Leases are rows in the store, so a restart keeps them.
3. No running cell naming one of its `exclusive` names.
4. A harness account that is not at its limit (or automatic with one left).

Then: lease, write the attempt's vault, create the `test` session, send the first message. The lease is
released after the `end` hooks of the attempt ran, whatever ended it.

**Limits**: an attempt longer than its `timeout`, or idle for 20 min (no event from its agent and no
tool call of it in progress, so a `qa start` waiting in BrowserStack's own queue for a member a team
member uses does not count as idle), is aborted (the steps not judged become `blocked`, "not reached: timeout"). A turn that ends without
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

The files are the truth; the store holds an index that can be rebuilt from them (`socrates tests
reindex`, and automatically when its schema changes).

```
<data>/tests/<repo-key>/                    repo-key: the folder's name and a short hash of its path
  runs/<run-id>/                            run-id: r-20261029-0915-abcd
    run.json                                selection, settings, commit, catalog snapshot hash, state, counts, times
    catalog/                                the catalog as it was run (without tools/)
    cells/<test id>/<cell>/<attempt>/       cell: the dimension values joined, pixel.es
      record.json
      evidence/                             $SOCRATES_TEST_EVIDENCE: actions.jsonl, links.json, screenshots, logs/
    bugs.json
    conclusions.md
<data>/run/tests/<run>/vault                   the run's secrets while it is active (run/ is never backed up)
<data>/run/tests/<run>/tools/                the run's copy of the tools, installed
<data>/run/tests/<attempt>/                  SOCRATES_TEST_RUNTIME: the attempt's vault, sockets; removed at its end
<data>/cache/tests/tools/<hash>/             installed tool copies, reused while a tool is unchanged
```

`record.json` of an attempt:

```json
{
  "run": "r-20261029-0915-abcd", "test": "web-browser/header-navigation", "area": "web-browser",
  "cell": "pixel.es", "attempt": 1, "dims": {"device": "pixel", "locale": "es"}, "params": {"consent": "accept"},
  "state": "ended", "endReason": "", "chat": "s-…",
  "role": {"harness": "claude", "model": "claude-sonnet-5-5", "effort": "medium", "account": "work"},
  "leases": [{"tool": "browserstack", "member": 3, "label": "silvansutter_VeTcMj"}],
  "queuedAt": "…", "startedAt": "…", "endedAt": "…", "waited": {"browserstack": 182000},
  "steps": [{"n": 1, "do": "Open / …", "expect": "hero heading …", "verdict": "pass",
             "observed": "Spanish home page …", "actions": [1, 2], "attach": [],
             "review": {"verdict": "issue", "note": "the menu overlaps the logo on the first screenshot", "at": "…"}}],
  "actions": [{"n": 1, "at": "…", "tool": "browserstack", "action": "open", "summary": "opened /es",
               "url": "…", "screenshot": "01-open.png", "ms": 673, "error": null, "details": {}}],
  "verdict": "pass", "reviewedVerdict": "issue", "summary": "…",
  "review": {"at": "…", "note": "…", "chat": "s-…"},
  "bugs": ["B1"], "links": {"Video": "…"}, "usage": {"input": 0, "output": 0, "costUSD": 0}
}
```

Index tables (schema version 13, `internal/store`; plus the `test` column on `sessions`): `test_repos` (path, key, settings JSON),
`test_runs`, `test_cells` (one row per attempt: run, test, area, cell, dims, priority, tags, state,
wait reason, verdict, reviewed verdict, chat, times), `test_leases` (repo, tool, member, attempt) and an
FTS5 table over test titles, observations, summaries, review notes and bug texts (FTS5 is in
`modernc.org/sqlite`, checked).

**Backup**: everything under `tests/` is backed up (the owner wants the screenshots too). **Retention**:
the last `keep_runs` runs per repository (default 20; a run can be pinned) are kept, older ones deleted
with their sessions, after the run that pushes them out is done.

## Settings

Per repository (stored in `test_repos.settings`, edited on the Tests page, overridable per run):

| Setting | Default |
|---|---|
| Cell agent: harness, model, effort, account | Claude Code, Sonnet 5.5, medium, automatic |
| Reviewer: harness, model, effort, account | Claude Code, Opus 5.5, high, automatic |
| Analyst: harness, model, effort, account | Claude Code, Opus 5.5, high, automatic |
| `parallel` (cells at once) | the sum of the pools' capacity, else 4 |
| `review_parallel` | 2 |
| `review` | on |
| `attempts` | 2 |
| `idle_timeout` | 20 min |
| `keep_runs` | 20 |

The model defaults follow Device QA's model evaluation (`PLAN.md`, 2026-10-28: Sonnet 5.5 judged the
steps as well as Haiku 5.5 with fewer unsupported claims and fewer missed problems; Opus reviews). Admin gets one field: `tests.max_sessions`.

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

A page of its own, `/tests` (`tests.html`, `js/tests.js`, `css/so-tests.css`, registered like
`/costs`), with a drawer link beside Costs and Admin; state in the hash
(`#repo=…&run=…&cell=…&tab=…`). Built from Dashboard-Style components only (AGENTS.md rules,
`make ds-check`):

- **Head**: the repository (a select when there are several), the run shown (latest by default), *Run*
  (a `ds-sheet` with the selection and the settings, the role pickers reused from `harnesses.js` and
  `accountpicker.js`), and while a run is active its progress (`ds-progress`, counts, *Stop*).
- **Tabs** (`ds-tabs`): Results · Runs · Bugs · Catalog · Settings.
- **Results**: tiles with the counts per verdict (`ds-tiles`, `DS.motion.countUp`); a toolbar with
  search (`ds-search`, full text) and filters (`ds-filters`: area, priority, tag, verdict, every
  dimension, reviewed only) and a *Problems only* chip; the table (`ds-table ds-table--dense
  ds-table--interactive ds-table--pin-first`, `--stack` on a phone): a row per test grouped by area, a
  column per value of the area's `columns` dimension, each cell its verdict as `ds-status` with icon and
  word (never colour alone) and the other dimensions' values as a tag (`es`), a running cell as
  running, a waiting one with its reason in a tooltip; the last column the test's **history**: its
  worst verdict in each of the last 10 runs, so a test that fails often stands out even when it passed
  now. A row opens the test (its text and every cell's history); a cell opens the cell.
- **Cell** (`ds-sheet` with the wide panel; full screen on a phone): facts (`ds-facts`: test, dims,
  attempt switcher, role, account, lease, times, waiting time, cost), the summary, the verdict (the
  agent's and the reviewed one, with the review note), then the steps as a timeline (`ds-timeline`):
  action and expectation, observation, verdict, the screenshots of its evidence as thumbnails, and its
  actions in a `ds-disclosure`. A screenshot opens full size in a `ds-dialog--full` with previous and
  next across the cell's screenshots. Below: links (video, network log), logs, the bugs it belongs to,
  and *Open transcript*, *Retry*.
- **Runs**: every run with its date, selection, settings, counts, duration and cost; an active run
  lists its running and waiting cells with their reasons, and the pools with who holds which member.
- **Bugs**: the run's `bugs.json` as a table (severity, title, cells as links) and `conclusions.md`
  rendered (`md.js`); *Analyse again*.
- **Catalog**: the areas and tests as the run would see them, errors as `ds-alert`, a test's text and
  its expanded cells; the tools with their pools and *Check secrets*.
- **Settings**: the table above, saved with a `ds-savebar`.

**Home** shows an active run as one card ("Tests · browser.lol · 23 of 54 cells · 4 running · 3
waiting for BrowserStack") that opens the Tests page.

## Code layout in Socrates

| Where | What |
|---|---|
| `internal/testmode/catalog` | Parsing (front matter, `## Steps`), validation, expansion into cells, snapshot |
| `internal/testmode/pool` | Secrets command, pools, leases, the `status` hook |
| `internal/testmode/runner` | The scheduler loop, cell lifecycle, hooks, timeouts, retry, review and analysis sessions, reconciliation |
| `internal/testmode/record` | `record.json`, evidence ingestion (`actions.jsonl`), redaction, review sampling, the index |
| `internal/agent/testtools` | The test tools per role (definitions, guidance with the harness' prefix), beside `chattools` and `askuser` |
| `internal/agent/claude`, `codex`, `opencode`, `askuser`, `agenthost/mcpask.go` | Offering the test tools and leaving out `ask_user` and the chat tools per session, in each harness' own mechanism |
| `internal/store` | The `test` column, `IsTest()`, the index tables, schema 13 |
| `internal/agenthost` | `Spec.TestInstructions`, the extra `PATH` and env, the sealed redaction list applied by the journal writer |
| `internal/server/tests*.go` | API, events frame, the runner's account pick, the relaunch of a test session from its row |
| `internal/server` (existing files) | `IsTest()` exclusions: sessions list and frame, `chatCounts`, push, titles, outcome, auto-continue, activity broadcast, `DoneRetention` |
| `internal/paths` | `tests/` and `run/tests/` |
| `main.go` | `socrates tests check|reindex` |
| `internal/web/static` | `tests.html`, `js/tests.js`, `css/so-tests.css`, the Home card, drawer links |

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
