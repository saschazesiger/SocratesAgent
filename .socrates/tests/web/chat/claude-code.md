---
title: A Claude Code chat reads a file, streams its answer and can be stopped
priority: P0
tags: [smoke, chat, real-agent, claude]
---
A real Claude Code (Haiku) in the demo project. Its words vary; judge what the interface shows. When
the account picker says "Claude QA" is not signed in, the steps that need it are `blocked`. Haiku
writes a long answer faster than the tool can press Stop (an action takes 5 to 35 s), so the turn
that is stopped waits instead: six `sleep 20` calls in a row are two minutes or more of work for a
few hundred tokens, and nobody waits for their end.

## Steps

1. Sign in, press New session, pick Claude Code with model Haiku and the demo folder, and start it; rename it `QA claude <device>`.
   Expect: the chat opens on "What should Claude Code do?"; under the top bar the account "Claude QA" (with its limit bars once its usage is known).
2. Send: `Read hello.py and tell me in one sentence what greet("Ada") returns. Do not change any file.` (on a touch device Enter adds a line: press Send).
   Expect: the message shows as sent; "Starting the agent..." or thinking, then a tool row reading hello.py, then the answer streams in and names "Hello, Ada!"; the page follows the answer to its end.
3. Look at the finished turn: the work folded into one line ("Worked for ..."), open it and close it.
   Expect: the fold opens to the tool calls and closes again; the answer stays readable; nothing overlaps the composer.
4. Send: `Run the shell command sleep 20 six times, as six separate Bash calls, each one only after the previous one has finished. Then reply with the word: done.` and press Stop the agent while it is still working (a sleep 20 tool row runs). If the turn ended ("done") before Stop could be pressed, send it once more and press Stop sooner; only if that one also ends first, judge it `blocked` (too fast to stop), not `fail`.
   Expect: Stop is there while the turn runs; after it the turn stops (no "done"), the running tool row ends and an "Interrupted" line shows; the composer is usable again.
5. Send: `Reply with just the word: ready`.
   Expect: a new turn starts after the interruption and answers "ready".
6. Delete `QA claude <device>`.
   Expect: the confirmation, then the toast; the session is gone.
