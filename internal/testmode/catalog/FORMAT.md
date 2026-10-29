# The test catalog format

A repository tells Socrates what to test in `.socrates/tests/`. Socrates runs that catalog: every
**cell** (one test on one device, in one locale ...) is worked by an agent of its own, which carries
out the steps with the tools the catalog names and judges each step on evidence (screenshots). A
reviewer checks the doubtful cells, an analyst turns the findings into bugs, and the results land on
the Tests page.

This is the reference for people and agents who write a catalog. The checker
(`socrates tests check`, or the chat tool `tests_check`) is the final word: it reads the files the
way a run does and names every problem with its file and line.

## Folders

```
.socrates/tests/
  TESTS.md                       required: what every test of the repository must know
  README.md                      for people only; any README.md is ignored
  tools/                         optional: command-line tools the tests use
    devices/
      TOOL.md                    the tool's configuration and instructions
      bin/shot                   bin/ goes on the PATH of the cells that use the tool
  web/                           an area: a top-level folder with an AREA.md
    AREA.md
    checkout.md                  a test: id web/checkout
    sign-in/                     sub-folders only group tests (no AREA.md in them)
      validation.md              id web/sign-in/validation
```

- An **area** is every top-level folder except `tools/`. It needs an `AREA.md`: a folder without one
  is an error (its tests would silently be lost).
- A **test** is every `*.md` below an area except `AREA.md` and `README.md`. Its **id** is its path
  without `.md` (`web/sign-in/validation`). History is kept by id: a renamed test names its old id in
  `previous:` to keep its history.
- Folder and file names are letters, digits, `-`, `_` and `.`. Hidden files and symbolic links are not
  part of the catalog.
- Every file is UTF-8 with an optional **front matter** at the very top, between two `---` lines.
  Unknown keys are errors, so a typo is never ignored.
- The front matter is a strict subset of YAML: block and flow mappings and lists (`[a, b]`,
  `{except: [a]}`), plain and quoted strings, comments. Not allowed: anchors, aliases, tags, block
  scalars (`|`, `>`), several documents, tabs for indentation, a key given twice. Every value is read
  as a string; the catalog decides what it means.
- A run copies the catalog (without `tools/`) into the run, with the commit it ran against: a result
  always shows the text it was judged against.

## `TESTS.md`

```markdown
---
title: The shop             # shown in Socrates; default: the repository folder's name
---
Everything every test must know: the environment under test (staging only), the rules for test
data, what never to do (production, real payments), how secrets are typed.
```

| Key | Meaning |
|---|---|
| `title` | The repository's name on the Tests page |

The body goes into the instructions of every cell agent, reviewer and analyst of the repository.

## `AREA.md`

```markdown
---
title: The web shop (apps/web)
tools: [devices]               # every test of the area gets these
mcp: [browserstack]            # MCP servers the cells use
columns: device                # the dimension the results table shows as columns
dimensions:
  device:
    values:                    # ordered: the column order
      iphone: iPhone 15 · Safari
      pixel: Pixel 9 · Chrome
      win-chrome: Windows · Chrome
    extra:                     # run only by a test that names them
      win-edge: Windows · Edge
    default: all
  locale:
    values: {en: English, de: German, fr: French}
    default: rotate
params:                        # handed to the tools; a test may override them
  consent: accept
evidence: screenshot
timeout: 45m
---
How to work a cell in this area: what is and is not a bug here, how the devices behave.
```

| Key | Meaning | Default |
|---|---|---|
| `title` | The area's name | the folder's name |
| `tools` | Tools (folders of `tools/`) every test of the area gets | none |
| `mcp` | MCP servers the cells may use: one built into Socrates (`browserstack`) or one of the repository's `.mcp.json` | none |
| `columns` | The dimension the results table shows as columns | the first dimension |
| `dimensions` | The axes the tests are expanded over (below) | none: one cell per test |
| `params` | `NAME: value` pairs handed to the tools (`SOCRATES_TEST_PARAM_<NAME>`) and to the built-in MCP servers | none |
| `evidence` | What a `pass`, `issue` or `fail` needs: `screenshot` (an image among the step's evidence), `any` (any evidence) or `none` | `screenshot` |
| `timeout` | How long one attempt of a cell may take (`30m`, `1h30m`) | `45m` |

The body goes into the instructions of the area's cells, after `TESTS.md`.

### Dimensions

A dimension has a name (lower-case letters, digits and `_`, starting with a letter; not one of a
test's own keys) and three keys:

| Key | Meaning |
|---|---|
| `values` | The values, in order: `id: label` lines, a flow mapping `{en: English}` or a list of ids (each its own label). Ids are letters, digits, `-` and `_` |
| `extra` | Values that run only for a test that names them |
| `default` | The selection a test gets when it does not choose: see below (default `all`) |

A selection, as `default` in `AREA.md` or under the dimension's name in a test:

| Value | Cells |
|---|---|
| `all` | one per value (`extra` values only when named) |
| `[a, b]` or `a` | those values (an `extra` value may be named) |
| `{except: [a]}` | every value but those |
| `rotate` | one value per combination of the other dimensions, rotated over the area's tests: the value at (the test's position in the area + the combination's position) modulo the number of values |

At most one dimension rotates for a test. A cell's id is the test id, `@`, and its values joined by
`.`: `web/checkout@pixel.de`.

### Built-in MCP servers

`mcp: [browserstack]` gives the cells Socrates' BrowserStack server, which drives one real device per
cell. The area then needs a dimension `device` whose values are BrowserStack's devices (`iphone`,
`iphone-old`, `ipad`, `pixel`, `moto`, `samsung`, `win-chrome`, `win-firefox`, `mac-safari`, and the
extra `win-edge`); a `locale` dimension is used as the site's locale, and `params: {consent: accept}`
(`accept`, `reject` or `leave`) says how the ad consent dialog is answered. Its accounts are leased to
the cells as a pool, and every action is recorded with its screenshot as evidence. What the server
needs to know of the site (base URL, locales, logins) is in `.socrates/mcp/browserstack.json`.

## A test

```markdown
---
title: The checkout takes an order
priority: P0
tags: [smoke, checkout]
device: [iphone, pixel, win-chrome]
locale: rotate
exclusive: [demo-customer]
params: {consent: reject}
timeout: 30m
---
Preconditions and context in prose: signed in as the demo customer, one item in the cart.
The consent dialog may appear late.

## Steps

1. Open the cart from the header (on a phone through the menu).
   Expect: the cart with one item and its price.
2. Pay with the test card.
   Expect: the order confirmation with an order number.
```

| Key | Meaning | Default |
|---|---|---|
| `title` | What the test checks, in a sentence | required |
| `priority` | `P0` core, `P1` important, `P2` edge case | `P1` (with a warning) |
| `tags` | Free words to select tests by | none |
| `tools` | Tools on top of the area's | none |
| `exclusive` | Names of shared test data the test changes: no two cells naming one run at once | none |
| `params` | On top of the area's `params` | the area's |
| `timeout` | One attempt at most | the area's |
| `previous` | The id the test had before a rename, whose history it carries on | none |
| `<dimension>` | The selection of an area's dimension for this test (`device: [pixel]`) | the dimension's `default` |

The body:

- `## Steps` is required and holds one numbered list (`1.` or `1)`), nothing else. Each item is the
  action, then one line starting with `Expect:` and what must be seen. Both may go on over the
  following lines (indented under the item). Steps count by their position.
- Everything outside `## Steps` is context: the cell's agent gets it as it is.

### Writing good steps

- **Describe the person's goal, not one device's controls**: "open the plans from the header (on a
  phone through the menu)", not "tap Upgrade". The same test runs on a phone and a desktop.
- **One step, one thing to see.** An `Expect:` names what is on screen when it worked: a heading, a
  message, a number. "It works" cannot be judged.
- **Say what is not a bug** in the context or in `AREA.md` (a browser's own validation bubble, a late
  ad), so the agent does not report it.
- **Keep test data safe**: name shared accounts in `exclusive`, and say how the test cleans up.
- Secrets never go into a test. A tool or an MCP server types them.

## `TOOL.md`

A tool is a folder of `tools/` (its name: lower-case letters, digits and `-`) with a `TOOL.md`.

```markdown
---
title: Fake devices
bin: bin                         # put first on the cell's PATH (relative to the tool)
setup: npm ci --no-audit --no-fund   # once per run, in the run's copy of the tool
end: bin/stop                    # after every attempt that used the tool, whatever happened
secrets:
  command: infra/secretctl show staging devices   # run in the repository root; prints NAME=value lines
  names: [DEVICE_ACCOUNTS, LOGIN_PASSWORD?]       # ? = optional
pool:
  secret: DEVICE_ACCOUNTS        # one member per entry
  separator: ","
  per_member: 1                  # sessions one member carries at once
  status: bin/status             # optional: prints {"busy": [2]} for members busy outside Socrates
---
Instructions for the agent: every command, what it prints, how to type a secret.
```

| Key | Meaning | Default |
|---|---|---|
| `title` | The tool's name in Socrates | the folder's name |
| `bin` | The folder put first on `PATH`, inside the tool's folder | `bin` |
| `setup` | A shell command run once per run in the run's copy of the tool (10 min at most; a failure stops the run) | none |
| `end` | A shell command run after every attempt that used the tool, before its lease is released (10 min at most) | none |
| `secrets.command` | A command run in the repository root that prints `NAME=value` lines | required with `secrets` |
| `secrets.names` | The names the tool takes; `NAME?` may be missing. Other lines are dropped | required with `secrets` |
| `pool.secret` | A secret in `secrets.names` (not optional) whose entries are the pool's members | required with `pool` |
| `pool.separator` | What separates the entries | `,` |
| `pool.per_member` | Sessions one member carries at once | `1` |
| `pool.status` | A command that prints `{"busy": [n, ...]}` (1-based members busy outside Socrates) | none |

The body goes into the instructions of every cell that uses the tool. A cell waits, spending
nothing, until a member of each pool it needs is free. A member of the form `name:secret` is shown
as `name`.

### The tool contract

What a cell (and every command its agent runs) gets:

| Variable | Value |
|---|---|
| `PATH` | each tool's `bin` first |
| `SOCRATES_TEST` | `1` |
| `SOCRATES_TEST_RUN`, `SOCRATES_TEST_ID`, `SOCRATES_TEST_CELL`, `SOCRATES_TEST_ATTEMPT` | run id, test id, cell key, attempt number |
| `SOCRATES_TEST_DIM_<NAME>` | the cell's value of each dimension (`SOCRATES_TEST_DIM_DEVICE=pixel`) |
| `SOCRATES_TEST_PARAM_<NAME>` | the merged `params` |
| `SOCRATES_TEST_REPO` | the repository root |
| `SOCRATES_TEST_EVIDENCE` | the attempt's evidence folder: the tool writes into it |
| `SOCRATES_TEST_RUNTIME` | a scratch folder for sockets and temporary files, removed at the end |
| `SOCRATES_TEST_VAULT` | a file (mode 0600) with the tool's secrets as `NAME=value` and, per pool, `SOCRATES_LEASE_<TOOL>=<member number>` and `SOCRATES_LEASE_<TOOL>_VALUE=<member>` |

- The **tool** reads the vault; the agent never does, and no secret is in the agent's environment or
  prompt. What Socrates stores is redacted: a known secret becomes `‹NAME›`.
- Tools run from a copy made when the run started: a change in the checkout does not reach a running
  run. `setup` gets the copy and `SOCRATES_TEST_REPO`, no secrets.
- At the end of an attempt: the `end` hooks run (the vault still there), then the vault and the
  runtime folder are removed, then the leases are released.

**Evidence.** A tool records each action as one JSON line in `$SOCRATES_TEST_EVIDENCE/actions.jsonl`,
with the files it names beside it:

```json
{"at":"2026-10-28T09:42:31.120Z","tool":"devices","action":"tap","summary":"tapped \"Pay\"",
 "url":"https://staging.example.com/cart","screenshot":"07-tap.png","ms":812,"error":null,
 "interaction":true,"details":{"target":"Pay"}}
```

| Field | Meaning |
|---|---|
| `at`, `action`, `summary` | required: when, what kind of action, what happened in words |
| `screenshot` | a file beside `actions.jsonl` (png, jpeg, webp or gif): what the verdicts rest on |
| `error` | what failed, or null |
| `interaction` | `true` for an action on the page (a tap, typing, a key) |
| `url`, `ms`, `tool`, `details` | shown as they are |

The actions since a step's previous judgement become that step's evidence. Besides them,
`links.json` (`{"Video": "https://..."}`) and `logs/*.txt` are shown on the cell's page. Only regular
files directly in the folder (or in `logs/`) are read. A tool that writes no evidence still works:
the agent then attaches files to its judgement.

## Verdicts

The same for every repository; the area says what they mean there.

| Verdict | When |
|---|---|
| `pass` | Everything the step expects happens, and nothing looks wrong |
| `issue` | It works, but something is wrong a person would notice |
| `fail` | It cannot be completed, gives a wrong result, loses data, hangs or shows an error |
| `blocked` | Not a product fault: infrastructure, a third party, test data missing, never reached |
| `na` | The step does not exist in this cell by design |

A cell takes its worst step: `fail` > `blocked` > `issue` > `pass` > `na`. A reviewer may change a
verdict; the reviewed one counts. A blocked cell is retried once.

## Checking a catalog

- In a chat whose repository has a catalog: the tool `tests_check`.
- In a terminal or a hook: `socrates tests check [repository]` (exit status 1 on errors).
- On the Tests page: the Tests view shows the errors and warnings.

A run does not start while the catalog has errors. Checked: every front-matter key and type; `AREA.md`
in every area; `## Steps` with an `Expect:` in every step; dimension selections that name defined
values and at most one `rotate`; tools that exist with a valid `TOOL.md` and their `bin` folder; a
pool's secret in `secrets.names`; `mcp` names that exist (a built-in server also checks the area);
the size of each area's instructions (`TESTS.md`, `AREA.md` and the tools' bodies, at most 56 KiB).
Warnings (a run starts): an area without tests, a test without `priority`, a tool no test uses, a
step numbered out of order, a symbolic link.

To run tests from a chat: `run_tests` with a narrow selection (`tests`, `cells`, `areas`, `tags`,
`priorities`, `dims`, or `problems_of: "latest"`), then `test_results` when the results arrive.
