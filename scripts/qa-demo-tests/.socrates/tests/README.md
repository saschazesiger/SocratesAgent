# The demo shop's test catalog

For people; Socrates ignores this file. A catalog for the Scripted agent of Socrates' test instance:
every test's parameter `fake` decides its outcome, over the four runs `scripts/qa-instance.sh reset`
makes (runs 1 to 4):

| Test | `fake` | Outcome |
|---|---|---|
| `shop/home` | none | passes |
| `shop/search` | `failfrom4` | a regression in run 4 |
| `shop/product-page` | `issue` | an issue in every run: a bug that stays |
| `shop/cart` | `failuntil3` | fails in runs 1 and 2, fixed in run 3 |
| `shop/checkout` | `slow` | passes, about 2 s a step |
| `shop/wishlist` | `flaky` | fails in odd runs |
| `account/sign-in` | none | passes |
| `account/password-reset` | `blocked` | blocked, retried once |
| `account/profile` | `issueuntil4` | an issue fixed in run 4 |
| `account/order-history` | `misjudged` | the agent passes it, the reviewer makes it an issue |
