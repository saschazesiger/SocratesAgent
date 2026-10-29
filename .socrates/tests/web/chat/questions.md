---
title: Five questions at once are answered in the full-screen question dialog
priority: P0
tags: [smoke, chat, questions]
---
The Scripted agent's `/askmany` asks five questions at once: Database (one choice, PostgreSQL
recommended), Features (choose 1 to 3), Name (text), Migrations (yes or no) and Replicas (a number from
1 to 10). The dialog is full screen up to 900 px wide.

## Steps

1. Sign in, start a Scripted agent chat in demo, rename it `QA ask <device>` and send `/askmany`.
   Expect: a question card "Deployment setup" with an Answer button in the conversation; Home counts a question for this session.
2. Press Answer.
   Expect: the dialog shows the first question (Database) with its options, "Recommended" on PostgreSQL, and You decide, Other and Add a note; Next and the step count; all of it inside the screen.
3. Look at the preselection, then go on with Next: on Features choose two options; on Name type `qa-<device>` (through the device keyboard); on Migrations keep or choose Yes.
   Expect: PostgreSQL is already chosen and Next moves on to Features; every question moves on; the keyboard never hides the field or the button.
4. On Replicas (the last question, 5 of 5, prefilled with 2) clear the field, type `abc` and press Send answers; then clear it again and type `3`. A phone's number pad may offer no letters: then skip `abc` and say so.
   Expect: `abc` is refused with "That is not a number." (or the field does not take it), and 3 is taken; the button on the last question is Send answers.
5. Go back with Previous question to Features, check the two choices are kept, then forward again to the end and press Send answers.
   Expect: the earlier answers are kept; after sending, the dialog closes (or offers Close) and the card says Answered.
6. Read the conversation.
   Expect: the agent echoes the answers as data: "you answered: ..." with pg, the two feature ids, the name, yes and 3; Home no longer counts this question.
7. Delete `QA ask <device>`.
   Expect: the confirmation, then the toast; the session is gone.
