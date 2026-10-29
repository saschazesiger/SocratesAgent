---
title: The sign-in form refuses an invalid email address
priority: P1
tags: [eval]
device: [pixel, win-chrome]
---
## Steps

1. Open the sign-in page from the header (on a phone through the menu).
   Expect: the sign-in page with an email field, a continue button and the Google, Microsoft and passkey options.
2. Type "not-an-email" into the email field and continue.
   Expect: the form stays and says the address is not valid, in the cell's locale; no code is sent.
3. Clear the field and continue with it empty.
   Expect: the form stays and asks for an address.
