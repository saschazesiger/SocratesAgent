---
title: The options for this device switch touch mode, the key bar, sound and notifications
priority: P1
tags: [settings, touch]
---
The ⋮ button at the right of the top bar opens "Options for this device". These options are kept in
this browser only, so other cells are not affected. Put touch mode back as it was before the step that
changes it.

## Steps

1. Sign in and open the options for this device (⋮ at the top right).
   Expect: a dialog "This device" with switches Key bar, Touch mode, Sound and Notifications, each with a hint, and Done; on a phone, tablet or the car screen Touch mode is already on, on a desktop off.
2. Read the Notifications hint.
   Expect: a hint that fits the device: "When a session finishes or needs an answer" where notifications work, on an iPhone or iPad in Safari "Add to Home Screen for notifications" ("Needs iOS 16.4 or later" on older iOS), or "Not available in this browser"; never an empty or cut-off hint.
3. Switch Touch mode the other way and close with Done.
   Expect: the buttons of the page change size at once (bigger with touch mode on, smaller without), and nothing overlaps either way.
4. Open the options again and switch Touch mode back to how it was; close with Done.
   Expect: the page looks as at the start; the switch shows the restored state.
5. Reload the page and open the options once more.
   Expect: the switches show the same state as before the reload (kept in this browser).
