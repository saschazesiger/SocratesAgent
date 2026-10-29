---
title: A forgotten password is reset by mail
priority: P1
tags: [auth]
device: [phone]
params: {fake: blocked}
---
Signed out. The mail goes to the staging inbox.

## Steps

1. Ask for a reset link on the sign-in form.
   Expect: a message that a mail was sent.
2. Open the link in the mail.
   Expect: the form for a new password.
