---
title: The Tests page lists the Demo shop's tests with their history, and its bugs
priority: P1
tags: [pages, tests, layout]
---
The Demo shop's tests, runs and bugs are shared by every cell: read them only. Never start, stop or
retry a run here, never change a bug's status, severity or note and never make a GitHub issue
(TESTS.md).

## Steps

1. Sign in, open Tests from the session list, then the Tests tab.
   Expect: the catalog's size ("10 tests in 2 areas"), Read again and Write a test with an agent, a search, the chips Problems, Flaky and the priorities; per area a card with "Latest: Run 4" in its head (or the newest run that ran it) and its tests in catalog order, P0 first. Each test shows its id and how it stands ("failing since Run 4", "an issue since Run 1", "flaky"), and its run only where it is not the area's latest.
2. Look at the tables (on a phone, the lists of cells) from top to bottom.
   Expect: on a desktop or tablet the two areas' columns (phone, tablet, desktop) line up from card to card and no row has a stray line under its first column only; a cell is its verdict with the locale in a quieter line under it. No sideways page scroll; a wide table scrolls inside itself.
3. Scroll to the end of the Tests view.
   Expect: the last area's card ends the view. The Demo shop's catalog has no tools, so there is no Tools card (a catalog with tools lists them here); no secret value or secrets command is printed anywhere on the page.
4. Open the test "The search finds a product".
   Expect: its head (file, cells, "Fail in Run 4, since Run 4", P1 and its tags) with Change it with an agent and Run this test; History as a grid, the newest run first (on a phone a row per cell with its last runs as glyphs), a legend in the order Fail, Issue, Blocked, Pass; What it does with its steps; its open bug.
5. Press a verdict in the history, look, and close the viewer.
   Expect: the cell viewer opens on that run's attempt of the cell; closing it comes back to the test.
6. Open the Bugs tab.
   Expect: the chips Open, Known, Fixed, Not a bug and All with counts, Open chosen; the bugs worst first, each with its severity, id and title, its status ("Open", or "Open · came back" for one that returned), the runs it was seen in and its cells (on a phone as a list with the severity and status under the title).
7. Open the first open bug and read it to the end, without changing anything.
   Expect: its head with Create GitHub issue (unavailable on the test instance, with the reason written under the head, not only in a tooltip), Run its cells again and Fix in a chat; What the analyst found; its cells with their screenshots; Triage with the line that every change saves by itself and a note saved when the field is left; Seen in and History. Nothing is cut off on the device.
