---
title: Admin shows every setting card, readable and reachable on the device
priority: P0
tags: [smoke, admin, layout]
---
A look through Admin without saving anything. Admin's navigation groups the cards (Agents, New
sessions, Voice and models, Access, This machine). Never press Sign in, Delete, Stop tunnel, Update now,
Back up now, Turn off backups or Save changes here, and never press Ctrl+S or Cmd+S.

## Steps

1. Sign in and open Admin (the session list's Admin).
   Expect: the heading Admin with its navigation and the first cards; on a phone the navigation is reachable without covering the cards for good.
2. Look at Accounts.
   Expect: the accounts grouped by agent: Claude QA, Codex QA, OpenRouter QA and Scripted QA, each with its kind and "Signed in", or "Not signed in" with Sign in; each group has its own "Add a ... account" button.
3. Look at Programs.
   Expect: a card per program (Claude Code, Codex, OpenCode, Shell, VS Code where present), none marked "Not installed" (the version is behind the card's i tip); the default models haiku, gpt-6-luna and openrouter/deepseek/deepseek-v4-flash.
4. Look at Workspace and Terminal behaviour.
   Expect: the workspace root, the preset directory "demo", the terminal's window size policy, scrollback and font size; every field fits the screen.
5. Look at Voice, Status & naming and Remote access.
   Expect: the OpenRouter key present (masked, never shown in clear), the spoken language, the models; Remote access says the tunnel runs with the public hostname.
6. Look at Password, Updates, Backup and Setup check, and press Run checks in Setup check.
   Expect: the cards are readable; Run checks lists its checks with a result each (passed or a reason, not by colour alone).
7. Use Admin's navigation to jump to Voice and then back to Accounts.
   Expect: the navigation scrolls to each card and shows where it is.
