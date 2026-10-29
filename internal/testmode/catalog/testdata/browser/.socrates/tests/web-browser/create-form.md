---
title: The launcher form takes a start URL through the device keyboard and opens its advanced settings
priority: P0
tags: [eval, probe]
device: [iphone, pixel, win-chrome]
---
Signed out, no running session. Never start a session: the launcher is looked at, not used.

## Steps

1. Open /create.
   Expect: the launcher; the browser grid has loaded (no skeleton); Start session is enabled (find it, do not
   start a session).
2. Type example.com into the start URL field (on phones and tablets through the device keyboard).
   Expect: the field shows example.com; the page does not jump or zoom.
3. Open the advanced settings.
   Expect: the section expands and lists its options; locked options say which plan or sign-in they need.
4. Rotate to landscape and back (na on desktops).
   Expect: the form reflows without overlap and keeps the typed URL.
