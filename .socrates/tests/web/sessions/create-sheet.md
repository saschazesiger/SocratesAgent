---
title: The create sheet offers every program, folder, model, effort and account, and starts a chat
priority: P0
tags: [smoke, sessions]
---
"New session" opens the sheet "What should this session run?". It is a side sheet on a wide screen
and rises from the bottom over the whole width up to 600 px. Look at every program, then start a
Scripted agent chat (free) and delete it again.

## Steps

1. Sign in and press New session.
   Expect: the sheet "What should this session run?" with the programs Claude Code, Codex, OpenCode and Scripted agent as cards and a full-width Shell card, then Working directory, Model, Effort, Account and the buttons Start session and Cancel; on a phone it covers the width and rises from the bottom.
2. Pick Claude Code and look at Model, Effort and Account; then the same for Codex and for OpenCode.
   Expect: each shows its models (Claude Code: Haiku among them and preselected; Codex: gpt-6-luna; OpenCode: the DeepSeek model through OpenRouter), effort chips that fit the model, and its account ("Claude QA", "Codex QA", "OpenRouter QA") with its state; an account that is not signed in says so with a Sign in button (never press it).
3. Open the Working directory list.
   Expect: Dynamic ("a fresh directory for every session"), the preset "demo" and Custom path...; on a phone the list is usable with a finger and closes again.
4. Press Cancel, then open New session again.
   Expect: the sheet closes without starting anything, and opens again.
5. Pick Scripted agent, Working directory demo, and press Start session.
   Expect: the sheet closes and a chat opens on its empty state "What should Scripted agent do?" with the composer ("Message Scripted agent"); on a phone the drawer stays closed.
6. Rename it `QA create <device>` (⋮ of the session, Rename) and send `hello`.
   Expect: the rename dialog "Rename this session" takes the name and the top bar and the list show it; the message shows as sent and the answer "you said: hello ..." follows.
7. Delete it (⋮, Delete, confirm).
   Expect: a confirmation naming the session; after confirming, a toast "Deleted. The working directory was kept." and the app goes back to Home without the session.
