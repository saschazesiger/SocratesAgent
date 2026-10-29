---
title: Claude Code asks through ask_user and gets the answer back
priority: P1
tags: [chat, questions, real-agent, claude]
---
A real Claude Code (Haiku) is asked to use its ask_user tool. When "Claude QA" is not signed in, the
steps that need it are `blocked`.

## Steps

1. Sign in, start a Claude Code chat with Haiku in demo and rename it `QA askreal <device>`.
   Expect: the empty chat with the account "Claude QA".
2. Send: `Use the ask_user tool to ask me one question: which colour I prefer, with the options red and blue. Then reply with my choice in one sentence. Do not change any file.`
   Expect: a question card in the conversation with Answer; Home's questions card counts it.
3. Press Answer, choose blue and send the answer.
   Expect: the dialog shows the question with red and blue; after sending the card says Answered.
4. Read the end of the conversation.
   Expect: the agent's reply names blue.
5. Delete `QA askreal <device>`.
   Expect: the confirmation, then the toast; the session is gone.
