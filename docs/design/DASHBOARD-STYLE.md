# Moving the front end onto Dashboard-Style

The web interface is built on [Dashboard-Style](https://github.com/saschazesiger/Dashboard-Style) (DS), vendored
in `internal/web/static/vendor/dashboard-style` (version and commit in its `VERSION`). This file is the plan of
that move and the rules every page follows once it is done. It replaces the first plan, which was lost with the
data directory it was written in.

## The rules

These are DS's own rules for a project (its `AGENTS.md`, "Using Dashboard-Style in a project"), filled in for
Socrates:

- **Socrates wears DS's own look.** No DS role is overridden. The theme (`css/theme.css`, loaded after `ds.css`,
  unlayered, on `.ds-theme`) holds only the project's `--so-*` tokens, documented component hooks (`--ds-btn-*`,
  ...) and the terminal font. Never a motion token or hook (`--ds-dur-*`, `--ds-ease-*`, ...).
- **Never edit the vendored copy.** It is checksummed (`SHA256SUMS`, checked by `internal/web/embed_test.go`) and
  replaced by `.github/workflows/update-dashboard-style.yml` every night. Something every dashboard could use goes to
  Dashboard-Style itself.
- **Reach for a DS component first**, and copy its documented markup exactly: the table "Which component for which
  need" and the markup of every component are in `docs/components.md` of Dashboard-Style; its reference app
  (`example/`: `app.css` with the prefix `ex-`, `app.js`) shows how a project wires them. Behaviour is the page's
  own script; opening and closing dialogs, sheets, menus, the combobox list, the modal drawer and snackbars go
  through `DS.overlay`.
- **Project components use the prefix `so-`** and read tokens only (`--ds-*`, `--so-*`): no colour, radius, font,
  shadow, duration or curve literals, no `@keyframes` of their own (the `ds-*` keyframes and motion tokens are
  there). A literal value that is needed becomes a `--so-*` token in `theme.css`. Project CSS is unlayered and must
  never restyle a `.ds-*` component (colour, radius, type, motion): set its hooks instead. Placing a `.ds-*`
  element inside a `so-` container is fine.
- **Every class in markup and script is `ds-*` or `so-*`.** State is said with attributes (`aria-pressed`,
  `aria-current`, `aria-expanded`, `aria-selected`, `data-state`, `hidden`), not with bare classes such as `on`,
  `active` or `open`. The few exceptions are other libraries' own classes (`xterm-*`).
- **Re-render with `morph()` / `flipMorph()`** (`ui.js`, DS's `morphHtml` and `flip`) and the `html```
  template, never `innerHTML`; key list rows with `data-ds-key`. Figures through `DS.motion.countUp`, timing from
  `ms()` / `DS.motion.motionDuration`, never millisecond literals.
- **Icons only from `DS.icons`**, through `icon()` / `iconEl()` / `data-icon` (`ui.js`); the meaning-to-glyph map is
  `GLYPH`. The few glyphs a phone needs that DS 1.6.2 lacks (microphone, stop, keyboard, upload, clipboard, Enter,
  paperclip, speaker, terminal) are Lucide's - the set DS draws its own from, same grid and stroke - in `ui.js`'s
  `LUCIDE`, until DS ships them; nothing else is drawn by the project. Brand marks of the agents stay in `logos.js`
  and `.so-agent`.
- **Status never by colour alone; every action is a pill; one filled button per area; no red buttons.**
- `ui.js` is the only module that reads `window.DS`.
- Socrates' own design rules still hold (docs/design/DESIGN.md §E.10): technical detail (versions, paths) behind
  the `i` of `infoTip()`, never in visible text; the agents' real marks wherever they are named; the phone in a car
  and the touch mode (`.so-touch`, 48px targets) are first-class; nothing typed is ever lost.

## Files

| File | What |
|---|---|
| `css/theme.css` | the theme: `--so-*` tokens by area, the touch-mode hooks, the terminal font |
| `css/so-shared.css` | components every page may show: `.so-logo`, `.so-agent`, `.so-mark`, `.so-field-row`, `.so-break`, `.so-ellipsis`, `.so-conn`, `.so-page-head` |
| `css/so-frame.css` | the session page's frame: top bar contents, drawer actions and foot, session list and folders, banners, the options and usage dialogs |
| `css/so-stage.css` | the stage: terminal pane, its cover, notice and ticker, key bar, transfer buttons, recording and copy dialogs, the IDE pane |
| `css/so-home.css` | Home |
| `css/so-question.css` | the question dialog |
| `css/so-prestart.css` | the PreStart panel |
| `css/so-sheet.css` | the new-session sheet and the settings form it shares with PreStart |
| `css/so-chat.css` | the chat view: the conversation and its parts, the composer, the chat's question dialog (over `so-question.css`) |
| `css/so-admin.css` | the admin page |
| `css/so-pages.css` | login, setup and costs |
| `js/ui.js` | the bridge to DS: `startUI`, icons, `html```, `morph`, `flipMorph`, `ms`, `watchScrolled`, `faces`, overlays |
| `js/api.js` | requests, `el()`, dialogs (`confirmDialog`, `alertDialog`), `toast`, `infoTip` |

The stylesheet from before the move, `css/app.css`, is gone; `git show 17d559d:internal/web/static/css/app.css`
shows it. `embed_test.go` fails on a page that asks for it again.

## Work packages

| WP | Area | Files |
|---|---|---|
| 1 | Frame: top bar, drawer, session list, folders, banners, options and usage dialogs | `session.js` (frame parts), `storage.js`, `so-frame.css`, `index.html` |
| 2 | Stage: pane, cover, notice, ticker, key bar, transfer, recording, copy, IDE pane | `term.js`, `keybar.js`, `keyboard.js`, `transfer.js`, `dictate.js`, `assist.js`, `session.js` (stage parts), `so-stage.css` |
| 3 | Home | `home.js`, `so-home.css` |
| 4 | Question dialog | `question.js`, `so-question.css` |
| 5 | PreStart | `prestart.js`, `so-prestart.css` |
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
  (`SOCRATES_E2E_CHROME=/opt/google/chrome/chrome node e2e/run.mjs <scenario>`).
- After a visible change the README pictures are taken again (`node e2e/shots.mjs`).
