# Moving the front end onto Dashboard-Style

The web interface is built on [Dashboard-Style](https://github.com/saschazesiger/Dashboard-Style) (DS), vendored
in `internal/web/static/vendor/dashboard-style` (version and commit in its `VERSION`). This file is the plan of
that move and the rules every page follows once it is done. It replaces the first plan, which was lost with the
data directory it was written in.

## The rules

These are DS's own rules for a project (its `AGENTS.md`, "Using Dashboard-Style in a project", as of 2.0.0 and
unchanged in 2.1.0), filled in for Socrates:

- **The UI is built on Dashboard-Style 2.x**, vendored in `internal/web/static/vendor/dashboard-style` (tokens
  `--ds-*`, components `.ds-*`, `DS.motion`, `DS.indicator`, `DS.dom`, `DS.ripple`, `DS.icons`, `DS.overlay`).
  `ui.js` `startUI()` calls `DS.ripple.attach(document)` (the press ripple, and DS's guard: a busy, done or
  `aria-disabled` action takes no input, so no page guards its own) and `DS.overlay.tooltips(document)` once per
  page. Dialogs, side sheets, the modal drawer, menus and the combobox list open and close through
  `DS.overlay.open(el, { opener })` / `DS.overlay.close(el)`; snackbars are `api.js` `toast()` on
  `DS.overlay.toast`; questions with yes or no and one field are `DS.overlay.confirm()` / `prompt()` behind
  `confirmDialog` / `promptDialog`. Every overlay sits inside the `.ds-theme` element (`<html>`).
- **Socrates wears DS's own look.** No DS role is overridden. The theme (`css/theme.css`, loaded after `ds.css`,
  unlayered, on `.ds-theme`) holds only the project's `--so-*` tokens, documented component hooks and the terminal
  font. Never a motion token or hook (`--ds-dur-*`, `--ds-ease-*`, `--ds-distance-*`, `--ds-scale-*`, `--ds-loop-*`,
  `--ds-delay-*`, `--ds-hold-*`, `--ds-stagger`, `--ds-time-scale`, `--ds-motion-*`, `--ds-enter-*`, `--ds-leave-*`,
  `--ds-clip-from`, `--ds-draw-length`, `--ds-scan-*`, `--ds-indicator-lag`, `--ds-indicator-dur`), never in
  `@layer`. Charts take DS's chart roles (`--ds-chart-1` to `--ds-chart-4`, `--ds-chart-neutral`).
- **Never edit the vendored copy.** It is checksummed (`SHA256SUMS`, checked by `internal/web/embed_test.go` and
  `make ds-check`) and replaced by `.github/workflows/update-dashboard-style.yml` every night (by hand:
  `make vendor-ds`, or `make vendor-ds DS_REF=<commit>` for a release not on main yet). When the nightly update
  fails at "Check" or "Verify", nothing was committed: run `make vendor-ds` and `make ds-check` locally, read DS's
  `CHANGELOG.md` for deprecations and removals (`vendor/dashboard-style/deprecations.json` lists them as data),
  and commit the new copy together with the fix. Something every dashboard could use goes to Dashboard-Style
  itself.
- **The project check.** `ds-check.json` configures DS's `tools/check-project.mjs` (prefix `so-`); CI, the nightly
  update and `make ds-check` run it. Every deliberate placement or wrapping rule on a `.ds-*` part is an exception
  there, with its reason; a new one needs one too, or a hook.
- **Reach for a DS component first**, and copy its documented markup exactly: the table "Which component for which
  need" and the markup of every component are in `docs/components.md` of Dashboard-Style, the patterns in
  `docs/patterns.md`, the choreography in `docs/motion.md`; its styleguide and Motion Lab (`styleguide/`) and its
  reference app (`example/`: `app.css` with the prefix `ex-`, `app.js`) show them live. Behaviour is the page's
  own script.
- **Project components use the prefix `so-`** and read tokens only (`--ds-*`, `--so-*`): no colour, radius, font,
  shadow, duration or curve literals, no `@keyframes` of their own. A literal value that is needed becomes a
  `--so-*` token in `theme.css`. Project CSS is unlayered and must never restyle a `.ds-*` component (colour,
  radius, type, motion): set its hooks instead. Placing a `.ds-*` element inside a `so-` container is fine. A
  project element that is pressed takes `.ds-pressable` (the state layer, ring snap and ripple), never a
  `::before` layer of its own.
- **Under `.ds-base`, `hidden` always hides:** never add a `[hidden]` rule.
- **Every class in markup and script is `ds-*` or `so-*`.** State is said with attributes (`aria-pressed`,
  `aria-current`, `aria-expanded`, `aria-selected`, `data-state`, `hidden`), not with bare classes such as `on`,
  `active` or `open`. The few exceptions are other libraries' own classes (`xterm-*`). The page never writes the
  attributes DS owns (`data-scrolled`, `data-ds-*`, `data-toast-reserve`).
- **Re-render with `morph()` / `flipMorph()`** (`ui.js`, DS's `morphHtml` and `flip`) and the `html```
  template, never `innerHTML`; key list rows with `data-ds-key`. Reorders go through `flip()` or
  `DS.motion.sortable()`, figures through `DS.motion.countUp`, timing from `ms()` / `DS.motion.motionDuration`,
  never millisecond literals.
- **Icons only from `DS.icons`**, through `icon()` / `iconEl()` (`ui.js`, which read `GLYPH`, the meaning-to-glyph
  map) and in markup `data-icon` with a DS glyph's name (`DS.icons.fill()`; `data-icon-place="start"` keeps the
  element's content after the glyph). Nothing is drawn by the project. Brand marks of the agents stay in `logos.js` and `.so-agent`.
- **Status never by colour alone; every action is a pill; one filled button per area; no red buttons.**
- `ui.js` is the only module that reads `window.DS`.
- Socrates' own design rules still hold (docs/design/DESIGN.md §E.10): technical detail (versions, paths) behind
  the `i` of `infoTip()`, never in visible text; the agents' real marks wherever they are named; the phone in a car
  and the touch mode (`<html class="so-touch ds-touch">`: DS's `.ds-touch` makes every DS target a finger's 48px,
  `.so-touch` the project's) are first-class; nothing typed is ever lost.

## Files

| File | What |
|---|---|
| `css/theme.css` | the theme: `--so-*` tokens by area, the project's touch-mode sizes, the terminal font |
| `css/so-shared.css` | components every page may show: `.so-logo`, `.so-agent`, `.so-mark`, `.so-break`, `.so-ellipsis`, `.so-conn` |
| `css/so-frame.css` | the session page's frame: top bar contents, drawer actions, session list and folders, banners, the options dialog, the action sheet's title, the Status modal |
| `css/so-stage.css` | the stage: terminal pane, its cover, notice and ticker, key bar, transfer buttons, recording and copy dialogs, the IDE pane |
| `css/so-home.css` | Home |
| `css/so-question.css` | the question dialog |
| `css/so-sheet.css` | the new-session sheet and its settings form |
| `css/so-chat.css` | the chat view: the conversation and its parts, the composer, the chat's question dialog (over `so-question.css`) |
| `css/so-motion.css` | `.so-wheel` (lines that roll like the face of a wheel) and `.so-shimmer` (a white wave over text at work), proposed to DS (below) |
| `css/so-admin.css` | the admin page |
| `css/so-pages.css` | login, setup and costs |
| `js/ui.js` | the bridge to DS: `startUI`, touch mode, icons, `html```, `morph`, `flipMorph`, `ms`, `faces`, overlays |
| `js/wheel.js` | the wheel's motion: `wheelHooks` for `morph()`, `wheelRoll()` without one |
| `js/api.js` | requests, `el()`, dialogs (`confirmDialog`, `promptDialog` on DS's, `alertDialog`, `openDialog`), `toast`, `infoTip` |
| `ds-check.json` (repository root) | the configuration of DS's project check, with its exceptions and their reasons |

The stylesheet from before the move, `css/app.css`, is gone; `git show 17d559d:internal/web/static/css/app.css`
shows it. `embed_test.go` fails on a page that asks for it again.

## Dashboard-Style 1.6 to 2.0 (October 2026)

2.0.0 removed names Socrates never used, so the update itself changed no markup; what it changed is behaviour and
looks, and the parts Socrates had built by hand that 2.0 ships. Moved onto DS's part:

| Before (Socrates' own) | Now (DS 2.0) |
|---|---|
| touch sizes on DS fields, rows, the combobox, `.ds-btn--sm`, `.ds-toast__close` | `.ds-touch` on `<html>` |
| Lucide glyphs in `ui.js` `LUCIDE`; `menu` as the grip | DS's glyphs (`mic`, `stopCircle`, `keyboard`, `upload`, `clipboard`, `cornerDownLeft`, `paperclip`, `volume`, `terminal`, `home`, `panelLeft`, `grip`) |
| `keepDialogRoom`, `reserveToasts`, de-duplication, the action button and the close tip of snackbars, the drawer and keyboard offsets | `toast()` options (`restart`, `key`, `action`, `reserve`), DS's placement beside the modal drawer, above its foot and above the keyboard |
| `followViewport()` writing `--so-app-h`; the top bar hidden by project CSS | `DS.dom.followViewport()`, `--ds-viewport-height`, `.ds-app--keyboard-hides-bar` (the strip of a phone on its side stays Socrates' own: `ds-check.json`) |
| `watchScrolled`, `edgeCue`, `setFaceLabel`, `overlayOpen`, the `busy()` done wrapper, own busy and `aria-disabled` guards | `DS.dom.watchScrolled`, DS's cut-edge fades (`DS.dom.scrollEdges` for the drawer list), `DS.motion.setLabel`, `DS.overlay.topmost`, `busy()`'s new default, DS's guard |
| the confirm and prompt dialogs built by hand | `DS.overlay.confirm()`, `prompt()` |
| the drawer's actions and foot, the row's ⋮ over its item, the folder head's own state layer | `.ds-drawer__actions`, `.ds-drawer__foot` (`--rail` on a short screen), `.ds-drawer__row` + `.ds-drawer__action.ds-hover-actions`, `.ds-pressable` |
| the menu's heading and On/Off, the action sheet's layout | menu groups, `.ds-menu__heading`, `.ds-menu__meta`, `.ds-dialog--bottom` with `.ds-dialog__head` and `.ds-dialog__list` |
| the question dialog restyled to the window, the option's ring | `.ds-dialog--full`, the selected interactive card |
| Home's tile ring and the card title's button reset | the selected tile, the button form of `.ds-card__action` |
| the chat's summaries and job rows, its dividers and task lists | `.ds-pressable`, `.ds-divider--label`, `.ds-tasks` |
| the Status modal's task marks and three-column tiles | `.ds-tasks`, `.ds-tiles--3` (its figures still wrap: `ds-check.json`) |
| the admin grip and its drag code, the voice guide's summary, the logs | `DS.motion.sortable()` with `.ds-grip`, `.ds-disclosure`, `.ds-code-block--wrap` |
| the dropdown's button field, the combobox without a list | `.ds-combo--select`, DS's rule for a combobox without a toggle |
| the sign-in card, "optional", the restore's share | `.ds-auth`, `.ds-card--elevated`, `.ds-field__optional`, `DS.dom.progress()` |
| Costs' series colours, swatches, share bars, `.so-costs-links` | the chart roles, `.ds-legend`, `.ds-meter`, the interactive table (rows without a link stay plain) |
| `.so-field-row`, the pane's drop outline, `[hidden]` rules | `.ds-field__row`, `DS.dom.dropzone()`, DS's `[hidden]` |
| `.so-page-head` (Home's and Costs' title with its actions), the top bar's menu button kept from shrinking, `{ done: false }` on sign-in and setup | the page head's `.ds-page-head__row`, `__text`, `__status`, `__actions`; DS's own rule; `busy()`'s new default |

Still Socrates' own, on purpose: the composer's growth (DS's `autosize()` hides the overflow of a field without a
`max-height`, which the first message's field that fills the view has), the chat's stick-to-the-end and its
"Latest" (it also lifts the snackbars and waits for a replay), the chat's Markdown (`.so-md`) and fenced
blocks with their bar (`.so-code`), the session's name in the top bar (a rename button, not a
`.ds-topbar__title`), Home's tile grid (two a row on a phone, each row filled), the focus fallback to the terminal
(`setFocusFallback`), `alertDialog` (lines and facts, which DS's `alert()` has not), the snackbar room in the session page's own
view (it does not scroll, so it keeps none of DS's room, `data-ds-toast-room="off"`, and `placeToasts` keeps the
room above the key bar, the chat's dock and at the conversation's end), and the folders of the session
list (`.so-folder`: their head has the VS Code button beside its toggle, which a `<summary>` cannot hold, shelves
nest a level inside a folder, and which folders are open is the person's, saved across reloads; DS's
`.ds-drawer__group` is a `<details>` with a summary for a toggle). A shut folder's head says what is behind it by
more than colour: a dot while one is working, the `chat` glyph while one waits for an answer, both in words for a
screen reader.

### Rules on DS parts that the check does not see

`check-project` judges a rule by the classes it names; these name only `so-` classes (or DS's in the context) and
still place or size a DS part. Each is deliberate:

| Rule | File | Why |
|---|---|---|
| `.so-row__open` (block padding, up to 900px) | `so-frame.css` | In the modal drawer a session's name takes two lines; two such rows keep a little air between them. |
| `.so-line` (min-height) | `so-chat.css` | A line between messages is DS's labelled divider as tall as a small button, so a line with an "i" is as tall as one without. A refused command is not a divider: `.so-line--row`, a project row with its chip and buttons. |
| `.so-auth__card` (grid, gap, children's margins) | `so-pages.css` | Setup's card holds sections of fields, a restore offer and a snapshot list: the card spaces them through one gap instead of each child's margin. |
| `.ds-auth > .so-conn` (sticky) | `so-shared.css` | The connection alert in the sign-in page's slot stays in sight while setup's long form scrolls on a phone. |

## Dashboard-Style 2.0 to 2.1 (October 2026)

2.1.0 (vendored at `a7a78db`) fixes what Socrates worked around after the move to 2.0 and adds the options it asked
for; it removes and renames nothing. Moved onto DS's part:

| Before (Socrates' own) | Now (DS 2.1) |
|---|---|
| `.so-touch .so-row__open`: the drawer item's end padding for a 48px ⋮ | DS keeps room for the action at its own size (`--ds-btn-sm-icon-size`) |
| the drawer list's fade at its foot (`.ds-drawer__list[data-ds-overflow-end]::after`) and the `ResizeObserver` that told `scrollEdges` about the session list | DS's shades at both cut edges of `.ds-drawer__list`; `scrollEdges()` watches every child of the list |
| `.so-view[data-toast-reserve]` taking DS's snackbar room back from the fixed view | `data-ds-toast-room="off"` on the view |
| Admin's and Costs' gutter for the last table column shown on a phone | DS's own rule |
| the margin that kept a status chip in a chat card's head from hanging like a button | DS sits a status chip on the title's line |
| `promptDialog` patching the field of DS's prompt (spellcheck, capitals, autocorrect, Enter) | `prompt({ input: { spellcheck, autocapitalize, autocorrect, enterkeyhint } })` |
| the chat's own drop code (`data-dropping`) | `DS.dom.dropzone(chat, { input: null })`, the composer's file input left to the composer |
| `fillIcons`' own loop and `data-icon-slot` | `DS.icons.fill()` with `data-icon-place="start"` |

Still Socrates' own after 2.1, with the reason that still holds:

- **The top bar in a phone's strip** (`.so-touch[data-so-short]`, `ds-check.json`): 2.1's `followViewport()` counts a
  field in VS Code's frame, but the editor's text in Chromium is an EditContext element (code-server 4.143.1), neither a
  textarea nor contenteditable, so `data-ds-keyboard` still does not come for it. A wish for DS: count an element
  with an `editContext` as a field that types.
- **The 8px row gap of a chat card's head** (`ds-check.json`): DS keeps 18px between a title and a chip that wraps under
  it, the gap a button needs; the chat keeps a chip's gap. The answer row keeps its own rule too: its words are a
  meta line, which DS still hangs out of the line like a button.
- **The composer's growth** (DS's `autosize()` hides the overflow of a field without a `max-height`), **Home's tile
  grid** (two a row on a phone, each row filled), **the Status
  modal's figures in words**, **the brand's name on a phone** and **`alertDialog`**: 2.1 changes none of these; they
  are the project's layout or content, as listed above.


## Proposed to Dashboard-Style

Parts Socrates built itself because DS has nothing like them yet, written the way a DS component is (header
comment with markup, hooks, choreography and progressive features; tokens only; nothing named after Socrates) so
that they can move into DS as they are. DS keeps the proposal; DS's repository names no project, so the link goes
from here to there.

| Part | Here | Proposal |
|---|---|---|
| The wheel and the shimmer: a running answer's live line (`.so-live`) rolls what the agent does like the face of a wheel, under a soft white wave | `css/so-motion.css`, `js/wheel.js`; the wave runs on DS's `ds-progress-wave` (no keyframes of our own) | [plans/wheel-and-shimmer.md](https://github.com/saschazesiger/Dashboard-Style/blob/main/plans/wheel-and-shimmer.md) |

When DS ships one of them, the project's copy goes and the page takes DS's names.

## Work packages

| WP | Area | Files |
|---|---|---|
| 1 | Frame: top bar, drawer, session list, folders, banners, options and usage dialogs | `session.js` (frame parts), `backup.js` (the backup notice), `so-frame.css`, `index.html` |
| 2 | Stage: pane, cover, notice, ticker, key bar, transfer, recording, copy, IDE pane | `term.js`, `keybar.js`, `keyboard.js`, `transfer.js`, `dictate.js`, `assist.js`, `session.js` (stage parts), `so-stage.css` |
| 3 | Home | `home.js`, `so-home.css` |
| 4 | Question dialog | `askdialog.js`, `so-question.css` |
| 5 | PreStart (removed 2026-10-25) | - |
| 6 | New-session sheet and settings form | `harnesses.js`, `combobox.js`, `so-sheet.css` |
| 7 | Admin | `admin.html`, `admin.js`, `so-admin.css` |
| 8 | Login, setup, costs | `login.html`, `setup.html`, `costs.html`, `costs.js`, `so-pages.css` |

Each package also brings the end-to-end scenarios that touch its area onto the new markup (`e2e/run.mjs`).

## Done means

All eight packages are done (October 2026). What this asked of each package still holds for every change to the
front end:

- No class outside `ds-*` / `so-*` in the package's markup and scripts; every `so-*` class it uses is styled in its
  stylesheet, from tokens only.
- Every view of the package looks right at 1280×800 and 390×844, in touch mode too, with no console error.
- `go test ./internal/web/... ./internal/server/...` and the package's e2e scenarios pass
  (`node e2e/run.mjs <scenario>`; set `SOCRATES_E2E_CHROME` only to use another Chromium, e2e/README.md).
- After a visible change the README pictures are taken again (`node e2e/shots.mjs`).
