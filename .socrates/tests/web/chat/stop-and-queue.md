---
title: A long turn is stopped, and a message sent meanwhile waits or goes now
priority: P1
tags: [chat, stop, queue]
---
The Scripted agent's `/sleep 60` works for a minute. A message sent while a turn runs is queued
behind it and offers Send now. `/stream 1500 120` streams the words `w1` to `w1500`, one every 120 ms:
three minutes, so the stream is still running after the tool's slowest actions (5 to 35 s each);
nobody waits for its end.

## Steps

1. Sign in, start a Scripted agent chat in demo, rename it `QA stop <device>` and send `/sleep 60`.
   Expect: the turn runs (a working state in the top bar and the list) and Stop the agent shows in the composer.
2. Press Stop the agent.
   Expect: the turn ends within a few seconds with an "Interrupted" line; Stop disappears.
3. Send `/sleep 60` again, then while it runs type `queued hello` and send it.
   Expect: the second message shows as Queued with a Send now button; nothing is lost.
4. Press Send now.
   Expect: the running turn stops and `queued hello` is answered "you said: queued hello ...".
5. Send `/stream 1500 120` (three minutes of words). While it streams, scroll up a little, wait a few seconds,
   then press Jump to the latest message; then press Stop the agent rather than waiting for the end. Only if the
   answer had ended (`w1500` and Send back in the composer) before you could scroll, judge this step `blocked`
   (too fast for the tool), not `fail`.
   Expect: the words keep coming (the last `w` number grows from one screenshot to the next); scrolled up, the view
   stays where it was while words arrive and Jump to the latest message shows; pressed, it brings the view to the
   newest words and follows them again; Stop ends the turn with an "Interrupted" line.
6. Delete `QA stop <device>`.
   Expect: the confirmation, then the toast; the session is gone.
