---
title: Dictation opens its recorder, asks for the microphone and can be cancelled
priority: P2
tags: [pages, voice]
---
The Speak button (microphone in the top bar, tip "Dictate into this chat") records and transcribes
through OpenRouter. BrowserStack devices may have no microphone or a permission prompt the tool cannot
answer: then the step is `blocked`, unless the page shows no explanation at all.

## Steps

1. Sign in, start a Scripted agent chat in demo and rename it `QA voice <device>`.
   Expect: the empty chat with the Speak button in the top bar and the composer's microphone.
2. Press Speak.
   Expect: the "Listening..." dialog with a running timer, Cancel and Send; or a clear message (microphone denied, no microphone found); never nothing at all.
3. Press Cancel (or close the message).
   Expect: the dialog closes and nothing is sent.
4. Delete `QA voice <device>`.
   Expect: the confirmation, then the toast; the session is gone.
