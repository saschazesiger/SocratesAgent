---
title: The cart updates its total
priority: P1
tags: [payments]
device: [phone, desktop]
params: {fake: failuntil3}
---
With the reading lamp in the cart.

## Steps

1. Raise the lamp's quantity to two.
   Expect: the total doubles.
2. Remove the lamp.
   Expect: the empty cart with a link back to the shop.
