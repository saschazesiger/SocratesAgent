---
title: Home shows what needs the person and filters by its tiles
priority: P0
tags: [smoke, home, layout]
---
The instance holds the seed sessions (TESTS.md): one of them, "Seed: a plan waits", waits for its plan
to be approved, so Home has a question. Look at it, never answer it. Other cells' sessions may come and
go while you look.

## Steps

1. Sign in.
   Expect: Home with its heading, the connection badge "Live", a New session button, the questions card (a count, "Approve the plan" or the first question's words, the session "Seed: a plan waits", an Answer button) and the tiles (Running, Background tasks, Finished, Ready, Review pending, Pending, Not started, Not running, Done), each with a count and a short text.
2. Scroll through the whole of Home, top to bottom.
   Expect: every card and tile is whole and readable, nothing overlaps or is cut off, the page never scrolls sideways; below the tiles the list with the segments Needs you and All.
3. Switch the list to All, then back to Needs you.
   Expect: All lists the sessions, the seeds among them (title, agent mark, status); Needs you lists "Seed: a plan waits" (or says what needs the person); the switch is immediate.
4. Tap the Finished tile (or another tile whose count is not 0).
   Expect: the list below shows only that tile's sessions and its title says so; tapping the tile again (or the segment) brings the full list back.
5. Tap Answer on the questions card, look at what opens, and close it without answering (its close button, Escape or Back; never Approve).
   Expect: the oldest waiting question opens, normally "Seed: a plan waits" on its plan card (headed Plan, "Waiting for you", Approve and Keep planning) or a dialog with the plan; another cell's question may be older: then that one opens, close it without answering (by design); never a message that nothing is waiting; going back to Home, the card is still there.
6. Open one seed session from Home's list (for example "Seed: Markdown answer"), then come back to Home (the Home entry of the session list, or the logo).
   Expect: the session opens with its conversation; Home comes back (a seed just read may move from Finished to Ready).
