---
title: Dictation opens its recorder, asks for the microphone and can be cancelled
priority: P2
tags: [pages, voice]
---
The Speak button (microphone in the top bar, tip "Dictate into this chat") records and transcribes
through OpenRouter. The browser asks for the microphone first: answer Safari's or Chrome's prompt with
`qa prompt allow`. A desktop's prompt is out of reach: on Windows Chrome start the device with
`qa start --grant microphone` (a fake microphone, the browser asks nothing); on the Mac's Safari the
step is `blocked`, unless the page shows no explanation at all. A device with no microphone may give
a clear message instead of the recorder.

## Steps

1. Sign in, start a Scripted agent chat in demo and rename it `QA voice <device>`.
   Expect: the empty chat with the Speak button in the top bar and the composer's microphone.
2. Press Speak.
   Expect: the "Listening..." dialog with a running timer, Cancel and Send; or a clear message (microphone denied, no microphone found); never nothing at all.
3. Press Cancel (or close the message).
   Expect: the dialog closes and nothing is sent.
4. Delete `QA voice <device>`.
   Expect: the confirmation, then the toast; the session is gone.
