---
title: The Tests page shows its empty state and its Add a repository dialog
priority: P2
tags: [pages, tests, layout]
---
The test instance has no repository with a test catalog. Do not add one for good.

## Steps

1. Sign in and open Tests from the session list.
   Expect: the page "The test catalogs of your repositories, each cell run by an agent." with the empty state "No repository with tests yet" and Add repository.
2. Press Add repository, look at the dialog, and cancel it.
   Expect: the dialog "Add a repository" with "Path of the repository", Add and Cancel; it fits the screen and Cancel closes it without adding anything.
3. Go back to the app.
   Expect: Home again; no test run card on Home.
