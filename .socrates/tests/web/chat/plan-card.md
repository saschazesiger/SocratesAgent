---
title: A plan card is approved or sent back with a note
priority: P1
tags: [chat, plan]
---
The Scripted agent's `/plan` shows a plan card with Approve and Keep planning.

## Steps

1. Sign in, start a Scripted agent chat in demo, rename it `QA plan <device>` and send `/plan`.
   Expect: a card headed Plan with the chip "Waiting for you", the plan's text, Approve and Keep planning.
2. Press Keep planning.
   Expect: a note field "What should change?" with Send back; on a phone the keyboard leaves the field and Send back visible.
3. Type `Please add a test step.` and press Send back.
   Expect: the card's chip says "Sent back to planning" with "Note: Please add a test step.", and the agent answers "Planning on: ...".
4. Send `/plan` again and press Approve.
   Expect: the new card's chip says "Approved" and the agent answers "The plan is approved; starting on it."
5. Delete `QA plan <device>`.
   Expect: the confirmation, then the toast; the session is gone.
