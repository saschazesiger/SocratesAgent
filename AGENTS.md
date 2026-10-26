# Notes for agents working on this repository

## Language: English only

Everything in this repository is written in English: code, identifiers,
comments, UI text, error messages, logs, tests, documentation and design
notes, commit messages and pull requests. This holds even when the person
you are working with writes to you in another language.

The only exceptions are deliberate:

- **Content that exists for a non-English language.** Socrates speaks
  English or German (`settings.voice.language`): the `Deutsch` option in
  Admin's language picker and the German voice sample in `admin.js` are
  meant to be German.
- **Test data about text handling.** Umlauts and German sentences in tests
  (UTF-8 cutting, the German voice, title cleanup) are fixtures, not prose.
  The comments around them are still in English.

## Documentation

- `README.md` is for people who use Socrates: short and visual, with
  emojis, tables and screenshots, and no long paragraphs.
- `docs/OPERATIONS.md` is the reference for running a server: install,
  updates, configuration, paths, backup and restore, uninstall.
- `docs/design/` holds the design notes and specifications.
- `e2e/README.md` describes the browser suite. `node e2e/shots.mjs`
  retakes the README screenshots.

## Finishing a change

Unless the task says otherwise, a change that is done and tested is
committed straight to `main` and pushed: no branch and no pull request.
Test it first (`make check`, and the browser suite or the running app
where the change is visible there).
