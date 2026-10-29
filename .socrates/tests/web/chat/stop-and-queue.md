---
title: A long turn is stopped, and a message sent meanwhile waits or goes now
priority: P1
tags: [chat, stop, queue]
---
The Scripted agent's `/sleep 60` works for a minute. A message sent while a turn runs is queued
behind it and offers Send now.

## Steps

1. Sign in, start a Scripted agent chat in demo, rename it `QA stop <device>` and send `/sleep 60`.
   Expect: the turn runs (a working state in the top bar and the list) and Stop the agent shows in the composer.
2. Press Stop the agent.
   Expect: the turn ends within a few seconds with an "Interrupted" line; Stop disappears.
3. Send `/sleep 60` again, then while it runs type `queued hello` and send it.
   Expect: the second message shows as Queued with a Send now button; nothing is lost.
4. Press Send now.
   Expect: the running turn stops and `queued hello` is answered "you said: queued hello ...".
5. Send `/stream 1000 50` (about 50 seconds) and, while it streams, scroll up a little and then back down.
   Expect: the words stream in smoothly; scrolling up stops the page following, Jump to the latest message brings it back.
6. Delete `QA stop <device>`.
   Expect: the confirmation, then the toast; the session is gone.
