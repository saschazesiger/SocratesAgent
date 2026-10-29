---
title: A Codex chat runs a command and answers
priority: P1
tags: [chat, real-agent, codex]
---
A real Codex (gpt-6-luna) in the demo project. When the account picker says "Codex QA" is not signed
in, the steps that need it are `blocked`. A message sent while Codex works is steered into its turn.

## Steps

1. Sign in, press New session, pick Codex with model gpt-6-luna and the demo folder, and start it; rename it `QA codex <device>`.
   Expect: the chat opens on "What should Codex do?" with the account "Codex QA".
2. Send: `Run python3 hello.py and tell me what it printed. Do not change any file.`
   Expect: reasoning, a command row running python3 hello.py with its output "Hello, Socrates!", then the answer saying so.
3. Open the command row's details, then close them.
   Expect: the command and its output are shown in full inside the screen (long lines wrap or scroll inside the row, not the page).
4. Send: `Reply with just the word: done`.
   Expect: the answer "done"; the conversation keeps both turns in order.
5. Delete `QA codex <device>`.
   Expect: the confirmation, then the toast; the session is gone.
