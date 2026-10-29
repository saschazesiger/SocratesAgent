---
title: The Add an account dialog adapts to the agent and the kind, and is cancelled
priority: P2
tags: [admin, accounts]
---
Opens the dialog and cancels it: never add, sign in or delete an account, and never press Ctrl+S or
Cmd+S in Admin.

## Steps

1. Sign in, open Admin, Accounts, and press "Add an OpenCode account" (each agent's group has its own button).
   Expect: the dialog "Add an account" with OpenCode preselected, Kind (Subscription or API key) and Name.
2. Choose API key.
   Expect: an API key field and a Provider field appear.
3. Switch the agent to Claude Code and the kind to Subscription.
   Expect: the key and provider fields go away; the dialog fits the screen.
4. Cancel.
   Expect: the dialog closes; the accounts are unchanged (four accounts).
