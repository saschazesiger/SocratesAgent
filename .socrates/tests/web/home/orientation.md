---
title: Rotating a phone or tablet keeps the chat, the dialog and the typed text
priority: P1
tags: [layout, touch]
device: [iphone, ipad, pixel, tesla]
---
Only on devices that rotate. The car screen (`tesla`) starts in landscape: where a step says landscape
rotate it to portrait, and back to landscape at the end. A draft in a composer is shared with every
device that opens the session, so type only into your own chat.

## Steps

1. Sign in, start a Scripted agent chat in the demo folder (New session, Scripted agent, demo, Start session), send `/md`, and rename it `QA rotate <device>` (⋮ of the session, Rename).
   Expect: the Markdown answer arrives (headings, lists, a table, code blocks); a wide table or code block scrolls inside itself, the page does not scroll sideways; the new name shows in the top bar.
2. Rotate to landscape (the car screen: to portrait).
   Expect: the conversation reflows to the new width; the top bar and the composer stay usable; on a short screen the logo hides; nothing overlaps.
3. Type `rotation keeps this` into the composer (through the device keyboard), without sending, and rotate back.
   Expect: the typed text is still in the composer after the rotation; the keyboard does not hide the composer.
4. Clear the composer, send `/askmany` in your chat, press Answer on its question card, then rotate and rotate back with the dialog open.
   Expect: the dialog stays open in both orientations (full screen on these touch devices), on the same question, its buttons reachable; closing it leaves the questions unanswered.
5. Open Admin, rotate once more and back.
   Expect: Admin's navigation and cards follow the rotation without sideways scrolling.
6. Delete `QA rotate <device>` (⋮ of the session in the list, Delete, confirm).
   Expect: a confirmation names the session; after confirming, a toast says it was deleted and it is gone from the list.
