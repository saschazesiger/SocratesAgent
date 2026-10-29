---
title: The checkout takes an order
priority: P0
tags: [smoke, payments]
params: {fake: slow}
---
Signed in as the fixture customer, with the reading lamp in the cart.

## Steps

1. Open the cart and go to the checkout.
   Expect: the delivery address of the fixture customer and the order's total.
2. Pay with the test card.
   Expect: the order confirmation with an order number.
