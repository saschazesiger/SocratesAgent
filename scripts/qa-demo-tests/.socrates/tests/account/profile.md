---
title: The profile keeps a new address
priority: P2
device: [tablet, desktop]
exclusive: [fixture-customer]
params: {fake: issueuntil4}
---
Put the old street back at the end.

## Steps

1. Open the profile.
   Expect: the profile with the saved delivery address.
2. Change the street to "Long Street Name With Many Words 12" and save.
   Expect: a confirmation, and the new street in full.
