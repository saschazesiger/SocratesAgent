# Chat tests: every chat writes, runs and documents its own tests (2026-10-31)

Status: decided with the owner on 2026-10-31 and built the same day. It **replaces** test mode
(the per-repository catalog in `.socrates/tests/`, the runner with its cell, reviewer and analyst
sessions, the bug list over runs, the Tests page `/tests` and `socrates tests check`), which is
removed with its data. TEST-MODE.md and TESTS-FORMAT.md are gone; git history has them (last in
925a2e1).

## What it is

A chat whose built-in MCP server **Socrates Tests** (`socrates-tests`) is on can write tests and run
them itself. A test is a flow with numbered steps, each with what must be seen; a **run** of a test
is the chat's agent working through the steps with whatever tools it has (BrowserStack, a browser,
a shell ...) and judging every step on evidence: what it observed, with screenshots and other files.
A test can be run again (after a fix, on another device); every run is kept with its evidence.

The tests belong to the chat: there is no catalog, nothing predefined per repository, no overview
over chats. They are shown on the chat's page, live, with their status, and the person can look at
every step and screenshot, override a result, edit or delete a test and ask for a run again.

## Decisions (owner, 2026-10-31)

| Topic | Decision |
|---|---|
| Old system | Removed entirely: catalog, runner, test sessions, reviewer and analyst, bug list, Tests page, CLI |
| Old data | `<data>/tests/`, `<data>/run/tests/`, `<data>/cache/tests/` and every test session are deleted once when the new build starts |
| Who runs a test | The chat's agent itself (and its own subagents), through the tools; no hidden sessions |
| Statuses | Draft · Running · Passed · Issue · Failed · Blocked · Skipped |
| Review | The person only: an override with a note, which the agent sees |
| Bugs | Documented in the tests themselves (observations, a run's summary and findings), free-form; no separate bug list. What the agent does with them (list them in the chat, fix them) is up to it |
| Overview | None across chats; tests are shown on the chat's page only |
| Person's actions | Override, run again, delete, edit |
| Switch | As every built-in MCP server: per chat (⋮ → *MCP servers…*, the new session sheet), Admin's *In every chat* |

## Terms and statuses

| Term | Meaning |
|---|---|
| Test | `T1`, `T2` … per chat: title, goal (context in Markdown), environment, tags, steps |
| Step | An action (the person's goal, not one device's controls) and `expect`: what must be seen |
| Run | One execution of a test: `1`, `2` … per test, with a judgement per step, a summary and findings |
| Judgement | A step's verdict, what was observed, a note, its evidence |
| Evidence | Files of a run (screenshots, logs, text output), each with a caption, on a step or on the run |

Step verdicts: `pass` (everything expected happens, nothing looks wrong), `issue` (it works, but
something is wrong a person would notice), `fail` (cannot be completed, wrong result, data lost,
hangs, an error), `blocked` (not a product fault: infrastructure, a third party, test data missing,
not reached), `na` (does not apply here by design).

A run's verdict is its worst step: `fail` > `blocked` > `issue` > `pass` > `na` (all `na` is `pass`).
A run is `running` until it is finished; finishing judges every step not judged yet as `blocked`
("Not reached").

A test's **status**, in this order of precedence:

| Status | When |
|---|---|
| the person's override | the person set one (any status but Running and Draft), with a note |
| `skipped` | the agent or the person set the test aside, with a reason |
| `running` | its latest run is not finished |
| `passed` · `issue` · `failed` · `blocked` | its latest finished run's verdict |
| `draft` | it never ran |

An override and a skip stay until removed; a new run removes an override (the person's word was on
the old run) but keeps it in the test's events. The person may override at any time, a running test
too (to settle a run whose agent stopped). A skipped test does not start: the agent brings it back
first (`skip: ""`), so a person's skip is never overruled unseen. `stale` marks a test whose steps changed after its
latest run (shown as "Changed since the last run").

## Storage

`<data>/chats/<chat>/tests/` (removed with the chat, backed up with it):

```
tests.json                 {"version":1,"next":4,"tests":[...]}
files/T1/r2/03-home.png    evidence, per test and run
```

`tests.json` is written whole, atomically (temp file and rename), under one lock per chat. A test:

```json
{"id":"T1","title":"Sign in with a wrong password","goal":"Markdown context ...","environment":"iPhone 15 · Safari, beta",
 "tags":["auth"],"steps":[{"action":"Open /login","expect":"The form with email and password"}],
 "created_at":"…","updated_at":"…","steps_at":"…","skip":{"reason":"…","by":"agent","at":"…"},
 "override":{"status":"passed","note":"…","at":"…"},
 "runs":[{"n":1,"started_at":"…","ended_at":"…","environment":"…","verdict":"fail",
          "steps":[{"action":"…","expect":"…","verdict":"pass","observed":"…","note":"…","at":"…",
                    "evidence":[{"file":"r1/01-login.png","caption":"…","kind":"image","source":"agent","at":"…"}]}],
          "evidence":[...],"summary":"…","findings":"Markdown …","cursors":{"browserstack":12}}],
 "events":[{"at":"…","by":"agent|person","what":"created|edited|run|finished|override|skip|..."}]}
```

A run copies the steps it was started with, so a later edit never rewrites what a run judged.
Limits (`internal/chattests`, exported constants): 200 tests per chat, 50 steps per test, the last 20
runs per test (older ones go with their files), 40 evidence files per step, 20 MB per file, 500 MB
per chat (counted from the sizes in `tests.json`), 200 events per test, and caps on every text. A
call that attaches files keeps all of them or none; a file is copied, never moved, and must be a
regular file (no symbolic link).

## The tools (`internal/mcpserver/tests`, server `socrates-tests`)

Chats only (a call names its chat; the server is on per chat like any built-in server). `test` is a
test's id (`T3`) or its exact title.

| Tool | Does |
|---|---|
| `tests_list` | Every test with its status, steps, latest run (verdict, when, failing step), skip, override and stale marks |
| `test_create {title, goal?, environment?, tags?, steps}` | A new test (draft); answers its id. `steps`: `[{action, expect}]`, at least one |
| `test_update {test, title?, goal?, environment?, tags?, steps?, skip?}` | Changes what it names; `skip` a reason sets the test aside, `""` brings it back |
| `test_delete {test}` | Deletes the test with its runs and files |
| `test_start {test, environment?}` | Starts a run (a run of the test still open is finished first, its rest `blocked`, "superseded by run n"); answers the steps |
| `test_step {test, step, verdict, observed, note?, files?, output?}` | Judges a step of the open run (again: replaces the judgement, keeps the evidence). `files`: paths of files to attach (absolute or relative to the chat's folder; images, logs, text, JSON, HTML, video); `output`: text evidence (a command's output), kept as a file. The screenshots built-in servers recorded since the previous judgement are attached too |
| `test_attach {test, files, step?, caption?}` | Attaches files to a step or to the open run |
| `test_finish {test, summary, findings?}` | Ends the open run: steps not judged become `blocked` ("Not reached"); `summary` one or two sentences; `findings` Markdown (bugs, suspected causes, suggestions), free-form |
| `test_read {test, run?}` | A test in full: steps, every run (or one) with judgements, absolute evidence paths (to open with the agent's own file tools), override, skip and events |

Answers are Markdown text. Wrong arguments are refused with what is wrong. The server's
instructions are the protocol: write a test before testing a flow, one step per thing a person
would check, judge only what was seen, attach a screenshot to every judgement of something visible,
describe what is on screen rather than what should be, use `blocked` for what is not the product's
fault, never put secrets in observations or files, read the person's overrides and edits with
`tests_list`/`test_read` and take them as the final word, and report what the tests found in the
chat as the work calls for.

**Evidence from built-in servers.** A server that records a chat's actions (`mcpserver.Recorder`:
the folder holding its `actions.jsonl` and the files the lines name; BrowserStack's
`<data>/mcp/browserstack/chats/<chat>/`) is read on every `test_step`: the lines after the run's
cursor for that server with a screenshot are attached to the step (caption: the action's summary,
source: the server), and the cursor moves on. The cursor starts at the line count when the run
starts, so only that run's actions are taken.

## API (behind `s.auth`, `internal/server/chattests.go`)

| Route | Does |
|---|---|
| `GET /api/chats/{id}/tests` | `{tests: [...], on: bool}`: every test with its status and its latest run in full (judgements and evidence names), older runs as `{n, verdict, started_at, ended_at}`; `on` whether the chat has the server on |
| `GET /api/chats/{id}/tests/{test}` | One test with every run in full |
| `PATCH /api/chats/{id}/tests/{test}` | The person's edit: `{title?, goal?, environment?, tags?, steps?, skip?}` |
| `POST /api/chats/{id}/tests/{test}/override` | `{status, note}`; an empty status removes it |
| `POST /api/chats/{id}/tests/{test}/rerun` | `{client_id}`: sends the chat a message as the person ("Run test T3 “…” again.", with the override the person had set), through the composer's path (`sendMessage`) |
| `DELETE /api/chats/{id}/tests/{test}` | Deletes it |
| `GET /api/chats/{id}/tests/{test}/files/{file…}` | An evidence file (`Evidence.file`, a regular file under the test's `files/`): an image or a video as itself, text as `text/plain`, anything else as a download; `nosniff`, `sandbox`, `private` |

Every change (agent or person) publishes `{t: "chattests", session, rev}` on `/api/events`; the
chat page reads the tests again when the frame names the chat it shows.

## The chat page

- **Tests button** in the top bar (beside the status and microphone buttons), shown for a chat that
  has the server on or has tests, with a badge: the number of tests with a problem (failed or issue)
  in the error colour, else the running ones, else nothing. Its label says the counts ("Tests: 2
  failed, 5 passed").
- **The panel.** On a wide screen (over 1100 px) a column on the chat's right, open or shut per
  device (remembered), the chat narrowing beside it. Narrower, a side sheet (`ds-sheet`) over the
  chat. Its head: "Tests", the counts per status as a summary line, filter chips (*All · Problems* (failed or
  issue) *· Open* (draft, running, blocked, or changed since its last run)); then a list of the tests in their order (problems first is the filter's job, not the
  order's): status (`ds-status`, glyph and word), id and title, the environment, "Step 2 of 5" and
  the latest screenshot while it runs, the latest run's time, marks (overridden, skipped, changed
  since the last run). Empty: what tests are and how to ask the agent for them; with the server off,
  how to switch it on (opens the chat's MCP dialog).
- **The test viewer** (`ds-dialog`, full screen on a phone): its head the title, status (and who set
  it: "the agent", "you"), id, environment, tags, a run switcher (Run 3 of 3, the verdict of each);
  actions *Run again*, *Override* (on a finished run), *Edit*, *Delete* (in *More* on a phone). Then
  the goal, the steps (action, expectation, verdict, observation, note, evidence thumbnails), the
  run's summary and findings (Markdown), the run's own evidence and the events. A thumbnail opens
  the image large with ← → over every image of the run.
- **Edit** (`ds-dialog`): title, goal, environment, tags, steps (action and expectation per row, add,
  remove, move up and down), *Set aside* with a reason. **Override**: a status (radio) and a note.

## Code

| Where | What |
|---|---|
| `internal/chattests` | The model, the store per chat (`tests.json`, files), every operation the tools and the API share, the Markdown answers |
| `internal/mcpserver/tests` | The `socrates-tests` server: tools, instructions, the evidence of `Recorder`s |
| `internal/mcpserver` | `Recorder`; `TestCell`, `AreaChecker` and `ChatsOnly` are gone with test mode |
| `internal/server/chattests.go` | The routes, the events frame, the run-again message, the clean-up of test mode's data |
| `internal/web/static/js/chattests.js`, `css/so-chattests.css` | The button, the panel, the viewer and the dialogs |
| `e2e/chattests.mjs` | The browser scenario: a fake agent creates, runs and finishes tests through the server; the panel, the viewer, override, edit, run again, delete |
