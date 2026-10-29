---
title: A chat's first message can be scheduled with the device's own date and time picker
priority: P2
tags: [sessions, schedule]
---
Before its first message a chat offers "Start automatically" with a date and time field: the message
typed then goes out at that time. The picker is the device's own (by design).

## Steps

1. Sign in, start a Scripted agent chat in demo and rename it `QA later <device>`.
   Expect: the empty chat with "Start automatically" and its date and time field above or near the composer.
2. Type `scheduled hello` into the composer without sending, and set Start automatically to five minutes from now with the device's picker (if the hint says the time has to be in the future, the picker took too long: pick a later minute).
   Expect: the picker opens and can be used with a finger or the mouse; the chosen time shows in the field and the hint under it says "Sends the message above by itself in ..." with the time; Clear appears.
3. Go Home and look for the session.
   Expect: Home counts it under Not started (or shows its clock in the list) until the time.
4. Wait until the time has passed (`qa wait`), then open the chat.
   Expect: `scheduled hello` was sent by itself and answered "you said: scheduled hello ...".
5. Delete `QA later <device>`.
   Expect: it is gone from the list after the confirmation.
