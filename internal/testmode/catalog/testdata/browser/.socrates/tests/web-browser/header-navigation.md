---
title: The header leads from the home page to the launcher and the plans
priority: P0
tags: [eval, probe]
device: [iphone, pixel, win-chrome]
---
Signed out, no running session. Edge cases: the consent dialog appears late; an ad sits in the layout.

## Steps

1. Open / (the home page in the cell's locale).
   Expect: hero heading and the header with the logo; on a phone the menu button instead of the links.
2. Open New Session from the header (on a phone through the menu).
   Expect: /create opens with the launcher (start URL field, browser grid, Start session).
3. Go back, then open the plans from the header (on a phone through the menu).
   Expect: /upgrade with the plans and their prices.
