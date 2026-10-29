---
title: Markdown, tool rows, diffs, task lists and workflows render inside the screen
priority: P1
tags: [chat, rendering, layout]
---
The Scripted agent renders every kind of part a real agent can produce. Wide content (tables, code,
diffs, long output) must scroll inside its own box, never make the page scroll sideways.

## Steps

1. Sign in, start a Scripted agent chat in demo, rename it `QA rich <device>` and send `/md`.
   Expect: a live "Thinking" line while it works (afterwards folded into "Worked for ... · Thought it through"), then the Markdown answer: headings, lists, a table and code blocks, readable; wide parts scroll inside themselves.
2. Send `/tools`.
   Expect: after the turn its work folds into one line "Worked for ..."; opened, it shows the rows: a Read, a command, an Edit and a failed Grep marked Failed (not by colour alone); opening the Edit shows its diff readable inside the screen.
3. Send `/todo 3000`.
   Expect: while the turn runs a task list is pinned above the composer ("n of 3 done", ticking off); it never covers the composer's buttons; the turn ends with "All three tasks are done." and the pinned list goes away.
4. Send `/workflow`.
   Expect: in the turn's "Worked for ..." fold a workflow row "Ship the feature" with the phases Plan, Build and Review and their nested agents, opening and closing; nested levels stay readable on a narrow screen.
5. Send `/bigtool`, open the turn's fold and then its `seq 100` row, and scroll its output.
   Expect: 100 long lines inside the tool's box, scrollable; the page stays usable and does not freeze.
6. Scroll to the top of the conversation and back down with the Jump to the latest message button (it shows once you scrolled up).
   Expect: the button appears when scrolled up and brings the latest message back into view.
7. Delete `QA rich <device>`.
   Expect: the confirmation, then the toast; the session is gone.
