---
title: The terminal's copy, paste, key bar and scrollback download
priority: P1
tags: [shell, clipboard, touch]
---
Copy, Paste and Upload a file sit in the top bar; up to 600 px wide they are the first items of the ⋮
menu. Copy with nothing selected opens "Copy from the terminal". A finger held still on the terminal
selects a word. Never upload a file.

## Steps

1. Sign in, start a Shell in the demo folder and rename it `QA copy <device>`; run `echo copy-me-please`.
   Expect: the line copy-me-please is printed.
2. Press Copy with nothing selected.
   Expect: the sheet "Copy from the terminal" with the terminal's text, Copy all and Close; it fits the screen and closes with Close.
3. Select `copy-me-please` in the terminal and copy it (touch: hold a finger still on it, then lift, `qa hold --css .xterm-screen --at <x>,<y>` on the word; desk: double-click it, `qa tap --double --css .xterm-screen --at <x>,<y>`, then press Copy). The output names the point in the screenshot's pixels: if the highlight is not on the word, aim again.
   Expect: a toast "Copied" (or the browser explains that it refused).
4. Type `echo ` at the prompt and press Paste, then Enter.
   Expect: the copied text is pasted after `echo ` and printed again, or a clear message says the browser would not let the clipboard be read (Chrome's own prompt on Android is answered with `qa prompt allow`; on iOS the tool refuses the read itself, its `NOTE:` says why, and the page's message counts; `blocked` only if the device's own permission prompt cannot be answered).
5. On a touch device: use the key bar: type `ech`, press Tab, then type ` keybar` and press the key bar's Enter (na on a desktop without touch).
   Expect: Tab completes `echo`, and `keybar` is printed; the sticky Ctrl and Alt keys show when they are on.
6. Open the ⋮ menu of the session.
   Expect: Download scrollback is offered (and on a phone Copy, Paste and Upload a file first); the menu closes again without choosing.
7. Delete `QA copy <device>`.
   Expect: the confirmation, then the toast; the session is gone.
