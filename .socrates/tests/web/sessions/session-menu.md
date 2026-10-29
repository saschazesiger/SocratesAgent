---
title: A session's menu renames it, files it under a status, shows its info and deletes it
priority: P1
tags: [sessions, menu]
---
The ⋮ menu of a session (in the top bar and on its row in the list) is a menu on a wide screen and an
action sheet from the bottom on a phone.

## Steps

1. Sign in, start a Scripted agent chat in demo, send `hello` and wait for the answer.
   Expect: the answer "you said: hello ..." in the conversation.
2. Open the session's ⋮ menu.
   Expect: Info, Rename, the Status group (In progress, Review pending, Pending, Background, Done), the chat's items (MCP servers..., Compact the conversation, Download the conversation, Switch accounts at a limit, Go on when a limit renews) and Delete; on a phone an action sheet whose every item can be reached by scrolling it.
3. Choose Rename, type `QA menu <device>`, and confirm.
   Expect: the dialog "Rename this session" with the field focused (on a phone the keyboard does not hide the Rename button); the new name shows in the top bar and the list.
4. Open Info from the ⋮ menu.
   Expect: the session's facts (program, model, working directory, account ...) readable and inside the screen; it closes again.
5. Set the status to Review pending, then go Home.
   Expect: the menu closes; Home's Review pending tile counts it and filters to it when tapped.
6. Set it back to In progress from its row in the session list (⋮ on the row).
   Expect: the row's menu works like the top bar's; the Review pending count goes down again.
7. Delete it from its row (⋮, Delete, confirm).
   Expect: the confirmation "Delete "QA menu <device>"?" with Delete and Cancel; after Delete a toast says it was deleted and the row is gone.
