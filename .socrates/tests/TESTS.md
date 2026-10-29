---
title: Socrates
---
These tests check Socrates' own web interface on real phones, tablets and desktops, step by step,
against a **test instance** of Socrates on the build server.

## The test instance only

- The site under test is the test instance the area's `base_url` names (a Socrates of its own, built
  from `main`, with its own data, user and accounts; `scripts/qa-instance.sh`). The tool opens nothing
  else. Never open, sign in to or change any other Socrates, least of all the owner's real one.
- It is password protected; the tool types the password (`qa type --secret QA_SOCRATES_PASSWORD`).
- Its data is disposable: `scripts/qa-instance.sh reset` deletes every session and seeds it again,
  and makes the Demo shop's test runs again, before a run. Still, other cells of the same run use it **at the same time** (one per BrowserStack
  account), so:
  - Lists hold other cells' sessions too. Never open, answer, rename, stop or delete a session you did
    not create in this cell. Find yours by the name you gave it.
  - Sessions whose title starts with `Seed:` are shared by every cell: open them to look, never send
    to them, answer them, approve their plan, change their status or delete them.
  - Name every session you create `QA <word> <device>` (Rename in its ⋮ menu, as soon as it exists),
    `<word>` being what the test says and `<device>` this cell's device id (`QA shell pixel`), and
    delete it at the end of the test, as the test's last step says.
  - Admin settings are shared: only a test that names `admin-settings` in `exclusive` saves a setting,
    and it puts the old value back.
- The Tests page has one repository, **Demo shop** (`/var/lib/socrates-qa/work/demo-shop`): a
  fictional shop's catalog (areas `shop` and `account`, ten tests, devices phone, tablet and desktop
  as the columns) with four runs, *Demo run 1* to *Demo run 4*, and their bugs, which reset makes
  again on the Scripted agent. Run 4 has a regression (`shop/search`) and fixes, `shop/wishlist` is
  flaky, `account/password-reset` is blocked, `account/order-history` was corrected by its reviewer.
  These runs and bugs are shared by every cell: open and read them, but never delete, stop, retry,
  override, pin or analyse them again, never triage a bug (status, severity, note, issue), never
  change the repository's settings and never remove the repository.
  - A test may start a run only of the Demo shop, only with the Scripted agent (the repository's
    settings already say so: do not change the roles in the start dialog) and only of the test
    `shop/home` (it always passes and touches no bug), named `QA <word> <device>`. It takes a few
    seconds; another cell's run may make it wait first. Delete that run at the end of the test, as
    the test's last step says, and no other.
- Never: change the password (a successful change signs every other cell out), press Sign in or
  Delete on an account, stop or restart the tunnel, press Update now, Turn off backups, Back up now or
  anything under Restore, or delete a session that is not yours.

## The agents in the test instance

The chats there run real agents on the cheapest model of each: **Claude Code** (Haiku), **Codex**
(gpt-6-luna) and **OpenCode** (DeepSeek V4 Flash through OpenRouter), one account each. Keep what you
ask them tiny and say "do not change any file" unless the test asks for a change; they answer in
seconds to a minute. An agent's wording varies: judge whether the interface shows what the agent did
(the streaming text, the tool rows, the answer), not the agent's intelligence. An account that is not
signed in (the account picker says so) makes the step `blocked`, not `fail`.

There is also the **Scripted agent** ("Scripted agent" in the program list; account "Scripted QA",
model "Fake small"): free and always the same, for what a real model does not reliably do. It answers
these messages (anything else is answered `you said: ...`):

| Message | What it does |
|---|---|
| `/md` | Thinks, then a long Markdown answer (headings, lists, a table, code blocks) |
| `/tools` | A file read, a command, an edit with a diff and a failing tool |
| `/todo` | A task list worked through while the turn runs |
| `/workflow` | Phases with nested agents |
| `/askmany` | Five questions at once (single, multi, text, confirm, number) |
| `/ask` | One question with two options |
| `/plan` | A plan card waiting for Approve or Keep planning |
| `/sleep 60` | Works for 60 seconds (to stop it) |
| `/stream 200 50` | Streams 200 words, one every 50 ms |
| `/crash` | The agent dies in the middle of the turn |
| `/bigtool` | A tool with 100 long output lines |

Never send `/limit` to it: it marks the shared "Scripted QA" account as limited for two hours, for
every cell.

In the composer a message starting with `/` may open the command list ("Commands"): finish typing
the whole message and send it; if the list took Enter, press Send.

## Secrets

Secrets never pass through you. Sign in only with `qa type --secret QA_SOCRATES_PASSWORD`; never print,
paste or type the password yourself, never read the vault file, and never put a secret into a command,
a judgement or a summary.

## The repository

You test the running test instance; you never change this repository, build it or deploy it. Read its
code (`internal/web/static`: the HTML, `js/`, `css/`) only to name precisely what you saw.
