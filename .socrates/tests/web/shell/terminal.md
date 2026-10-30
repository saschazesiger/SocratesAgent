---
title: A Shell session runs commands, takes Ctrl-C and ends and restarts
priority: P0
tags: [smoke, shell, touch]
---
The Shell is a real shell (bash) in a terminal on the page. On touch devices the keyboard comes only
through the top bar's Show the keyboard button, and the key bar above it gives the arrows, Enter, Esc,
Tab, Ctrl, Alt, ^C, ^D, ^Z and Paste. At a desk click the terminal and type.

## Steps

1. Sign in, press New session, pick Shell and the demo folder, start it, and rename it `QA shell <device>`.
   Expect: a terminal fills the stage with a prompt in ~/work/demo; on a touch device the key bar shows above the keyboard area and Show the keyboard is in the top bar.
2. Run `ls` (type it and press Enter; on a touch device open the keyboard first).
   Expect: README.md, hello.py and notes are listed; what was typed shows at the prompt as it is typed; the keyboard does not cover the prompt line.
3. Run `python3 hello.py`.
   Expect: "Hello, Socrates!" is printed.
4. Run `sleep 300`, then stop it with Ctrl-C (the key bar's ^C on a touch device, or its Ctrl then c; Ctrl+C at a desk).
   Expect: the sleep ends at once with ^C and a fresh prompt.
5. Run `seq 1 400`, then scroll the terminal up and down (one finger on a touch device, the wheel at a desk).
   Expect: the output scrolls back to the early numbers and down again; the page itself does not scroll or zoom.
6. Run `exit`.
   Expect: an overlay says the session ended (normally) with Restart.
7. Press Restart.
   Expect: a fresh prompt in the same folder.
8. Delete `QA shell <device>`.
   Expect: the confirmation, then the toast; the session is gone and its terminal closed.
