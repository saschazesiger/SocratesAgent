---
title: A changed setting is saved, survives a reload and is put back
priority: P1
tags: [admin, settings]
exclusive: [admin-settings]
---
Changes the terminal font size and puts it back. Other cells use the instance at the same time; only
this test saves Admin settings, so put the old value back even when a step fails.

## Steps

1. Sign in, open Admin, Terminal behaviour, and note the font size (14 unless someone changed it).
   Expect: the font size field with its value.
2. Change the font size to 16.
   Expect: the field takes 16; Save changes (in the top bar and at the foot of the page) stays reachable and does not cover the field.
3. Press Save changes.
   Expect: the button shows Saving, then Saved, and the toast "Settings saved" appears.
4. Reload Admin.
   Expect: the font size shows 16.
5. Put the font size back to the noted value and save.
   Expect: "Settings saved" again; after a reload the old value shows.
