---
title: The password card refuses an empty or wrong current password
priority: P2
tags: [admin, auth]
---
Only the refusals: never change the password (a change signs every other cell out), and never press
Ctrl+S or Cmd+S in Admin.

## Steps

1. Sign in, open Admin and go to the Password card.
   Expect: Current password, New password, Change password and the note that changing it signs out every other browser.
2. Press Change password with both fields empty.
   Expect: "Fill in both password fields." (or the browser's required bubble); nothing changes.
3. Type `not-the-password` as the current password and `qa-new-password-123` as the new one, and press Change password.
   Expect: a message that the current password is wrong; the fields stay; the keyboard does not hide the message on a phone.
4. Clear both fields and leave Admin; reload the app.
   Expect: still signed in with the same password (Home opens).
