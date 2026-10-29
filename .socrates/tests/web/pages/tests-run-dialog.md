---
title: The Tests page's Run dialog, its quick runs and the Settings fit the device
priority: P2
tags: [pages, tests, layout]
---
Look only: never start a run in this test, never change the roles and never save the Settings
(TESTS.md). Cancel every dialog.

## Steps

1. Sign in, open Tests from the session list, and open the arrow beside Run.
   Expect: a menu headed "Start now" with Everything, The problems of Run 4 and Only P0, P1, P2, each with its number of cells, and Choose what to run…; close it with Escape or a tap outside, without choosing.
2. Press Run.
   Expect: the sheet "Run tests" (the whole screen on a phone) with What to run as cards with their cell counts (Everything chosen), the settings of this run as one line naming every role ("Cells: … · Review: … · Analysis: …") with Change for this run, an optional name whose hint reads like Run 5 “Before the release”, and Start with the cell count in the foot.
3. Choose "Choose…".
   Expect: the tree of areas and tests with nothing ticked, Select all and Select none, the chips of priorities, tags and values; the foot says "Pick at least one test" and Start is unavailable.
4. Tick one test, then press Select all, then Select none.
   Expect: the counts follow (one test, then every test, then none); Start says how many cells it would run and is unavailable again after Select none.
5. Press Change for this run, look at the roles, then press Use the defaults, then Cancel.
   Expect: the roles' program, model, effort and accounts open under the line; a role without a signed-in account is said in a warning at the top with a link to Admin; Use the defaults folds them away; Cancel closes the sheet and nothing started (no new run in the Runs tab).
6. Open the Settings tab and scroll through it without changing anything.
   Expect: a warning at the top if a role has no signed-in account (with "Accounts in Admin"); Agents with the three roles, How runs go, MCP servers for test cells, Keeping runs and This server; every field fits the screen and no save bar shows.
