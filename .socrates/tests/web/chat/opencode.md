---
title: An OpenCode chat on OpenRouter answers and lists the folder
priority: P1
tags: [chat, real-agent, opencode]
---
A real OpenCode with DeepSeek V4 Flash through OpenRouter (account "OpenRouter QA", an API key).

## Steps

1. Sign in, press New session, pick OpenCode with the DeepSeek model through OpenRouter and the demo folder, and start it; rename it `QA opencode <device>`.
   Expect: the chat opens on "What should OpenCode do?" with the account "OpenRouter QA".
2. Send: `List the files in this folder and say in one sentence what the project is. Do not change any file.`
   Expect: a tool row listing or reading files, then an answer naming README.md, hello.py and notes and calling it a small demo project.
3. Open the composer's model button, then "All ... models..." in its menu, look at the dialog and cancel it.
   Expect: a menu of models and efforts; the dialog "Model" with a search field ("Search by name, provider or id"), the models, Use this model and Cancel; Cancel closes it and the model stays.
4. Delete `QA opencode <device>`.
   Expect: the confirmation, then the toast; the session is gone.
