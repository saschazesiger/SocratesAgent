# Demo shop

The repository of a fictional web shop, without the shop: it holds only a test catalog
(`.socrates/tests`), so that the test instance of Socrates has a repository with a history of test
runs to show on its Tests page.

`scripts/qa-instance.sh reset` copies this folder to the instance (`/var/lib/socrates-qa/work/demo-shop`),
makes it a Git repository and runs its catalog four times with the Scripted agent, which costs
nothing. The `fake` parameter of a test says what the Scripted agent makes of it
(`internal/agent/fake_tests.go`).
