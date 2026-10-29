---
title: A crashed agent is explained, and the chat goes on
priority: P2
tags: [chat, errors]
---
The Scripted agent's `/crash` dies in the middle of a turn. (Never send `/limit`: it would mark the
shared Scripted QA account as limited for every cell.)

## Steps

1. Sign in, start a Scripted agent chat in demo, rename it `QA crash <device>` and send `hello`.
   Expect: the answer "you said: hello ...".
2. Send `/crash`.
   Expect: the turn ends with "The agent stopped during the turn. The next message starts it again." (or very close); no endless spinner.
3. Send `after the crash`.
   Expect: the agent starts again and answers "you said: after the crash ...".
4. Delete `QA crash <device>`.
   Expect: the confirmation, then the toast; the session is gone.
