---
title: The Tests page shows how the Demo shop stands, a run's results and a cell in the viewer
priority: P1
tags: [pages, tests, layout]
---
The Demo shop and its four runs are shared by every cell: read them, never stop, retry, override,
pin, analyse or delete anything (TESTS.md). Another cell's run of `shop/home` may be running: then the
Overview starts with a Live card for it, which is fine; never press its Stop.

## Steps

1. Sign in and open Tests from the session list.
   Expect: the page "Demo shop" with the Run button beside the title (on a phone too, on the same line), the tabs Overview, Runs, Tests, Bugs (with a count) and Settings; on a desktop the repository's path under the title. No banner above the tabs unless another cell's run is active (then one line on a phone: "Run N · x of y done" with Watch and Stop).
2. On a phone or a narrow tablet, open the title's menu (the arrow beside "Demo shop"), then close it with Escape or a tap outside.
   Expect: the menu shows the repository's path and the latest run's commit at its top, then Repositories with Demo shop ticked, Add repository and Remove from the list; it fits the screen and closes without changing anything. On a desktop `na` when the path is already shown under the title (open the menu anyway and say what it shows).
3. Read the Overview from top to bottom.
   Expect: "Latest run: Run 4 “Demo run 4”" with four tiles in the order Fail, Issue, Blocked, Pass, each with its change against Run 3 as an arrow and a word ("1 more than Run 3 · worse", "same as Run 3"); Needs attention with "Broke since Run 3" (The search finds a product), "Still with issues" (the product page, "an issue since Run 1"), never an Issue cell under "Still failing"; a trend of the last runs with its legend Fail, Issue, Blocked, Pass. Nothing overlaps or is cut off; no sideways scroll.
4. Open the Runs tab and then Run 4.
   Expect: the runs newest first with a result bar, its counts (fail, issue, blocked, pass in that order) and their state (on a phone too, with "↓ 1 broke" or "↑ 1 fixed" where a run changed); Run 4's view has its head (Done, "broke since Run 3", commit, when, how long), the line "Cells: … · Review: … · Analysis: …", and Results with a table per area (on a phone: per test a list of its cells). The areas' columns line up from table to table on a desktop or tablet; under each verdict the other values (the locale) in a quieter line; a line explains the marks (broke, reviewed, attempt).
5. Tap a cell whose verdict is Fail (The search finds a product).
   Expect: the cell viewer fills the window: the test's title and the cell's values at the top with "x of y" and previous/next; the verdict, "Regressed" and who gave it ("The agent said Fail · the reviewer kept it"); a bug chip such as "B-n · Major · open"; the actions Override verdict, Retry and Fix in a chat, all with their words (also on a phone), and More. The steps say "Step 1 of 2: …" with the step's own action, not a raw caption; on a phone each step has its screenshot under it.
6. Open More, look at it, and close it without choosing anything.
   Expect: on a desktop a menu, on a phone an action sheet from the bottom, with "Open the agent’s transcript" and "Run this cell in a new run"; Escape or Cancel closes it and the viewer stays open. Never choose an item.
7. On a desktop, use the keys: press ↓ in the list of steps, → for the next screenshot, `?` for the keys, then Escape twice.
   Expect: ↓ selects the next step and its screenshot; → moves to the next screenshot; `?` opens "Keys of the cell viewer" listing ↑ ↓ for the steps; the first Escape closes the keys, the second the viewer (one Escape is enough even while a tooltip shows). On a phone or tablet without a keyboard `na`: close the viewer with its × instead.
8. Open a cell whose verdict is Pass, then close the viewer.
   Expect: Override verdict and Retry, but no Fix in a chat (a pass has nothing to fix); the viewer closes and the run's results are where they were.
