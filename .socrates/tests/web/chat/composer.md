---
title: The composer keeps a draft, sends the way the device expects and offers commands
priority: P1
tags: [chat, composer, touch]
---
On a touch device Enter adds a new line and Send sends; at a desk Enter sends and Shift+Enter adds a
line; Ctrl+Enter (Cmd+Enter on a Mac) sends everywhere. A draft is kept on the server while typing.

## Steps

1. Sign in, start a Scripted agent chat in demo and rename it `QA composer <device>`.
   Expect: the empty chat with the composer "Message Scripted agent", Attach files, the model button, Send.
2. Type `first line`, press Enter, type `second line` (touch devices through the device keyboard; at a desk use Shift+Enter instead of Enter).
   Expect: two lines in the composer, nothing sent yet; the composer grows and stays above the keyboard.
3. Send it (Send on a touch device; Enter at a desk).
   Expect: one message with both lines; the answer "you said: first line ..." follows.
4. Type `draft survives` without sending and reload the page, then open the chat again from the list.
   Expect: `draft survives` is back in the composer.
5. Clear the composer, type `/` alone.
   Expect: the command list "Commands" opens (compact and context for the Scripted agent) and closes with Escape or by deleting the slash.
6. On a desktop send `ctrl enter works` with Ctrl+Enter (Cmd+Enter on the Mac) (na on phones and tablets).
   Expect: the message is sent and answered.
7. Press Attach files and cancel the file picker (do not upload anything).
   Expect: the device's file picker or a menu opens and closes again; no file chip stays behind.
8. Delete `QA composer <device>`.
   Expect: the confirmation, then the toast; the session is gone.
