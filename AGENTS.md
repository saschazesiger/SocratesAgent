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

## Design system: Dashboard-Style

The web interface follows Dashboard-Style's rules for a project (its `AGENTS.md`, "Using Dashboard-Style in a
project", 2.0, unchanged in 2.1), filled in for Socrates. `docs/design/DASHBOARD-STYLE.md` has them with Socrates'
details, what moved onto DS in 2.0 and 2.1 and what is still Socrates' own on purpose. Run `make ds-check` before committing a change to
the UI.

- The UI is built on [Dashboard-Style](https://github.com/saschazesiger/Dashboard-Style) 2.x, vendored in
  `internal/web/static/vendor/dashboard-style` (tokens `--ds-*`, components `.ds-*`, `DS.motion`, `DS.indicator`,
  `DS.dom`, `DS.ripple`, `DS.icons`, `DS.overlay`). `ui.js` `startUI()` calls `DS.ripple.attach(document)` once:
  every action gets the pointer ripple, and the action guard it installs blocks busy, done and
  `aria-disabled="true"` actions for every input (click, keys, submit); a control that explains itself on click
  sits in `data-ds-guard="off"`. Open and close dialogs, side sheets, the modal drawer, menus and the combobox list
  with `DS.overlay.open(el, { opener })` / `DS.overlay.close(el)`, show snackbars with `api.js` `toast()` (on
  `DS.overlay.toast`) and tooltips with one `DS.overlay.tooltips(document)`; every overlay sits inside the
  `.ds-theme` element (`<html>`).
- Never edit `internal/web/static/vendor/dashboard-style`: it is checksummed (`SHA256SUMS`, checked by
  `make ds-check` and `make check`) and replaced by the nightly `.github/workflows/update-dashboard-style.yml` (by
  hand: `make vendor-ds`, or `make vendor-ds DS_REF=<commit>` for a release not on main yet). A fix or a component
  every dashboard could use goes to Dashboard-Style itself (project-neutral, English), then the copy is updated.
- When the nightly update fails at "Verify", nothing was committed and the site keeps the old copy: run
  `make vendor-ds` and `make ds-check` locally, read Dashboard-Style's `CHANGELOG.md` for deprecations and removals,
  and commit the new copy together with the project's fix (or fix Dashboard-Style; the next run picks it up). Then
  Actions → "Update Dashboard-Style" → *Run workflow*. More in its `docs/install.md`, "When the update fails".
- Reach for an existing component first. The docs are not part of the vendored copy: the table "Which component
  for which need" and the markup of every component are in
  https://github.com/saschazesiger/Dashboard-Style/blob/main/docs/components.md; for live examples clone the
  repository and open `styleguide/index.html` (every component) and `styleguide/motion.html` (the Motion Lab: every
  choreography, replayable and slowed down with its speed select; both work from disk). What each component plays
  is the choreography in https://github.com/saschazesiger/Dashboard-Style/blob/main/docs/motion.md. Copy the
  documented markup and attributes exactly; behaviour (wiring the triggers, filtering, choosing) is the project's
  own script, and opening and closing go through `DS.overlay`.
- Theme only through tokens in `internal/web/static/css/theme.css`, on `.ds-theme`, loaded after `ds.css`: roles
  (`--ds-primary`, `--ds-accent`, `--ds-font` ...; Socrates overrides none), component hooks (`--ds-btn-*`,
  `--ds-card-*` ...), the project's own `--so-*` tokens and `@font-face`. Never set motion tokens or hooks
  (`--ds-dur-*`, `--ds-ease-*`, `--ds-distance-*`, `--ds-scale-*`, `--ds-loop-*`, `--ds-delay-*`, `--ds-hold-*`,
  `--ds-stagger`, `--ds-time-scale`, `--ds-motion-*`, `--ds-enter-*`, `--ds-leave-*`, `--ds-clip-from`,
  `--ds-draw-length`, `--ds-scan-*`, `--ds-indicator-lag`, `--ds-indicator-dur`). Never wrap the theme in `@layer`.
- `ds.css` sits in `@layer ds.*`, so unlayered project CSS beats every component. Load resets in a layer declared
  before ds (`@layer reset;` first, or `@layer reset, ds, app;`). Under `.ds-base`, `hidden` always hides; never add
  a `[hidden]` rule.
- Never write the attributes the system owns (those the "Attributes" table in
  https://github.com/saschazesiger/Dashboard-Style/blob/main/docs/components.md lists as written by the system, such
  as `data-scrolled`, `data-ds-*`, `data-toast-reserve`); the page sets only its own ones.
- Never change the look of a `.ds-*` component in project CSS (colour, radius, type, motion); set its hooks
  instead, in the theme or on the element. Placing a `.ds-*` element inside a project container is fine; document
  any deliberate deviation with its reason (`ds-check.json`, or DASHBOARD-STYLE.md for a rule the check cannot
  see).
- Local components use the project prefix `so-` and read tokens only (`--ds-*` and `--so-*`): no colour, radius,
  font, shadow, duration or curve literals, no own `@keyframes` (use the `ds-*` keyframes and motion tokens). A
  project element that acts on a press (a row, a tile of its own) takes `.ds-pressable` (state layer, focus ring,
  ripple), never a `::before` state layer of its own.
- Bigger targets for a finger are opt-in: `.ds-touch` on the `.ds-theme` element or a container (Socrates' touch
  mode puts it on `<html>` with `.so-touch`), never a `pointer: coarse` rule of the project's own. A widget
  embedded in someone else's page never calls `DS.dom.followViewport()`: the host page owns the viewport.
- Re-render with `DS.dom.morphHtml` (`ui.js` `morph()`; key list rows with `data-ds-key`), never `innerHTML`; it
  patches in place, so bind listeners once (or mark a subtree `data-ds-replace`). Reorders go through
  `DS.motion.flip`, figures through `DS.motion.countUp`; JS timing comes from `DS.motion.motionDuration()`
  (`ui.js` `ms()`), never ms literals.
- Icons only from `DS.icons` / `js/icons.js` (through `ui.js` `icon()`); status never by colour alone; actions are
  pills.
- `make ds-check` runs `node internal/web/static/vendor/dashboard-style/tools/check-project.mjs --config
  ds-check.json` (CI and the nightly update run it too): it checks the bullets above (rules `vendor`, `classes`,
  `deprecated`, `literals`, `keyframes`, `themes`, `layers`, `hidden`, `restyle`, `attributes`, `dom`,
  `pressable`); a deliberate deviation is an exception in `ds-check.json` with its reason.
