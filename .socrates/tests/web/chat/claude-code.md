---
title: A Claude Code chat reads a file, streams its answer and can be stopped
priority: P0
tags: [smoke, chat, real-agent, claude]
---
A real Claude Code (Haiku) in the demo project. Its words vary; judge what the interface shows. When
the account picker says "Claude QA" is not signed in, the steps that need it are `blocked`.

## Steps

1. Sign in, press New session, pick Claude Code with model Haiku and the demo folder, and start it; rename it `QA claude <device>`.
   Expect: the chat opens on "What should Claude Code do?"; under the top bar the account "Claude QA" (with its limit bars once its usage is known).
2. Send: `Read hello.py and tell me in one sentence what greet("Ada") returns. Do not change any file.` (on a touch device Enter adds a line: press Send).
   Expect: the message shows as sent; "Starting the agent..." or thinking, then a tool row reading hello.py, then the answer streams in and names "Hello, Ada!"; the page follows the answer to its end.
3. Look at the finished turn: the work folded into one line ("Worked for ..."), open it and close it.
   Expect: the fold opens to the tool calls and closes again; the answer stays readable; nothing overlaps the composer.
4. Send: `Write the numbers from 1 to 2000, one per line, nothing else. Do not use any tool.` and press Stop the agent at once, while it is still answering. If the answer ended before Stop could be pressed, send it once more and press Stop sooner; only if that one also ends first, judge it `blocked` (too fast to stop), not `fail`.
   Expect: Stop is there while the turn runs; after it the answer stops and an "Interrupted" line shows; the composer is usable again.
5. Send: `Reply with just the word: ready`.
   Expect: a new turn starts after the interruption and answers "ready".
6. Delete `QA claude <device>`.
   Expect: the confirmation, then the toast; the session is gone.
