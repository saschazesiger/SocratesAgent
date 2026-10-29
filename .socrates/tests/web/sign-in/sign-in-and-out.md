---
title: Signing in and out, with the wrong and the empty password
priority: P0
tags: [smoke, auth]
---
Signed out on a fresh device. This test is about signing in, so step 1 does not sign in first. Every
wrong password counts against this device's address (five in a row lock it for a while): type a wrong
one only where a step says so, once; the real password right after clears the count. Cells may share
an outgoing address, and a lock also cuts their live connection, chats and terminals: a "too many
attempts" answer here is `blocked`, not `fail`.

## Steps

1. Open / and look at where it lands.
   Expect: the sign-in page ("Welcome back", "Enter your password to open Socrates.", a Password field and Sign in), not the app; the card is centred and fully visible, on a phone without zoom.
2. Press Sign in with the field empty.
   Expect: the page stays and says "Enter your password." (or the browser's own required-field bubble); nothing is sent.
3. Type `wrong-password-qa` into Password (on phones and tablets through the device keyboard) and press Sign in.
   Expect: an error saying the password is wrong; the field stays usable and the keyboard does not hide the error or the button.
4. Clear the field, type the real password (`qa type --secret QA_SOCRATES_PASSWORD --clear`) and sign in.
   Expect: Home opens ("Home", the Live connection badge, New session); no error.
5. Reload the page.
   Expect: still signed in, Home again (a reload always lands on Home).
6. Open /setup and then /login.
   Expect: both send a signed-in person on to the app (Home), never show the setup form or the sign-in form.
7. Open /does-not-exist, then go back to /.
   Expect: a plain "404 page not found" (by design), and / brings back Home.
8. Sign out (the drawer's Sign out; on a phone open the drawer with Sessions first; on a short landscape screen it is an icon).
   Expect: the sign-in page; opening / again shows the sign-in page, not Home.
