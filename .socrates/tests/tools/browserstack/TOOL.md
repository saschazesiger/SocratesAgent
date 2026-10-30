---
title: BrowserStack devices
bin: bin
setup: npm ci --no-audit --no-fund
secrets:
  command: 'cat "${SOCRATES_QA_SECRETS:-/etc/socrates-qa/secrets.env}"'
  names:
    - BROWSERSTACK_ACCOUNTS
    - QA_SOCRATES_PASSWORD
pool:
  secret: BROWSERSTACK_ACCOUNTS
  separator: ","
  per_member: 1
  status: bin/qa pool-status
end: bin/qa stop
---
`qa` drives one real device on BrowserStack for this cell, against the Socrates test instance the area's
`base_url` param names: the device, the test instance and the BrowserStack account all come from the
cell, so no command names them. A background process holds the device session between your commands
and keeps it alive while you think. Every action is written into the cell's evidence with its
screenshot; that is what your judgements rest on.

## Commands

| Command | Does |
|---|---|
| `qa start` | Starts the device and prints `device ready: ...` with the test instance's URL. When BrowserStack queues the device (the account is busy elsewhere), it returns after about 8 minutes with `still queued` (exit status 3) while it keeps waiting: run `qa start` again, as often as it says so |
| `qa open <path>` | Opens a page of the test instance: `qa open /admin` opens `<base_url>/admin`, a path may carry a hash (`qa open /#home`). A full URL is taken only on the test instance's own origin; any other site is refused |
| `qa find <text>` | Scrolls a control into view without touching it: shows that it is there, whether it is disabled and what covers it |
| `qa tap <text>` | Taps a control: a real touch on phones and tablets, a click on desktops (`--simulated` for a WebDriver click on a phone or tablet, `--native` to insist on the touch without a fallback, `--force` to tap one another element covers) |
| `qa type <value> --into <field>` | Types into a field (`--native` through the device keyboard, `--clear` first, `--submit` presses Enter after); without `--into` into the focused field |
| `qa type --secret QA_NAME --into <field>` | Types a login of the vault (`QA_SOCRATES_PASSWORD`) without it passing through you |
| `qa key <name>` | Presses enter, tab, escape, backspace, up, down, left, right or space; on a desktop also `ctrl+<letter>`, `ctrl+enter`, `meta+enter`, `shift+enter` and `shift+tab` |
| `qa keyboard` | Closes the on-screen keyboard as a person would (Done on iOS, Back on Android); does nothing when no keyboard is open |
| `qa prompt allow\|block` | Answers the prompt Chrome draws over the page on Android (the clipboard's Block / Allow, notifications, location) by pressing its button; an error when none is open or on another device |
| `qa scroll down\|up\|top\|bottom` | Scrolls what a swipe in the middle of the screen would (a dialog's body, the app's scrolling column, else the page) by most of a screen, or to its top or bottom; says which box moved and where it stands |
| `qa rotate landscape\|portrait` | Rotates a phone or tablet (refused on a desktop) |
| `qa back`, `qa forward`, `qa reload` | Browser navigation |
| `qa wait <seconds>` | Waits, keeping the session alive |
| `qa look` | Screenshot and the controls on screen: role, name, state, value (a password shows as `(hidden)`), notices, dialogs, and the scrolling box in the middle (`scroller`) when it is not the page |
| `qa shot` | A screenshot only, without waiting for the page |
| `qa env` | The device facts: user agent, viewport, pixel ratio, system language, time zone |
| `qa logs` | The test instance's server log since the device started (`journalctl` of its unit, the area's `service` param, `socrates-qa` by default), the last 200 lines at most; kept in the evidence as `logs/server.txt` |
| `qa stop` | Closes the device session. Socrates runs it after the cell; you need it only to start over with `qa start` |

Every action prints its number, its time, `ERROR ...` when it failed, what it hit, `not settled` when
the page did not come to rest, the page errors, the URL and the screenshot path. Read the screenshot.

## How the devices behave

- **The test instance only.** `qa open` refuses another origin, and a tap, typing or a key is refused
  while the page is not on the test instance (a link or a redirect took it elsewhere): the action then
  says so and the output warns `the page left the test instance`. Go back (`qa back`, `qa open`); never
  act on another site, the owner's live Socrates least of all.
- **History on desktop Safari.** Safari's driver waits for a page load after Back, Forward or a URL,
  and a step within the page (only the hash changes, as in a single-page app) brings none: it hung
  for two minutes and could break the session. There `qa back` and `qa forward` move the history from
  the page (`history.back()`, as the browser's button does) and wait until the address or the page
  changed, and `qa open` of the same page with another hash changes the hash in the page
  (`(within the page: only its hash changed)`). `nothing to go back to` means the address stayed.
- **Settling.** Every action waits until the page is quiet (no DOM change for 0.8 s, no visible
  `.ds-skeleton` or `aria-busy="true"`), at most 20 s; past that the action says `not settled` and what
  was still busy. A page that never stops changing (a chat streaming a reply, a running terminal) is
  `not settled` after 20 s: that is the page working, not a fault by itself.
- **Errors.** BrowserStack has no console log on iOS. The driver collects page errors, rejections and
  console errors and warnings after each action (`page errors:`; errors during the first load are
  missed). `qa logs` adds the server's own lines; other cells of the run use the same test instance at
  the same time, so a line may be theirs.
- **Native input.** On phones and tablets type with `--native`: the driver taps the field with a real
  touch and types through the device keyboard (iOS) or the focused field (Android). When Chrome does
  not show the field to Android (it happens, in dialogs too), the text goes in through WebDriver after
  the real touch, and the output says so. Plain typing skips the keyboard and misses zoom, IME and
  viewport effects. If a tap right after typing has no effect, repeat it with `--native` (the closing
  keyboard moves the page).
- **Native taps** go where a person's finger would: the driver marks the control in the page, finds
  the marks on the device screen and touches their middle, then checks which element the page
  received (`the touch landed on ...` is an error of the action; the page may have changed). The marks
  sit on top of everything, open dialogs, sheets and menus (the top layer) included, so a control in a
  dialog is touched like any other. A control the screen does not show (under the on-screen keyboard,
  scrolled away) is refused as `not visible`: scroll, or close the keyboard with `qa keyboard`, then
  judge whether a person could have reached it.
- **Simulated taps.** On phones and tablets `qa tap` touches natively unless `--simulated` asks
  otherwise. Appium's switch to the native layer is bounded and tried twice; when it still fails
  before the touch, the tap falls back to a WebDriver click and says why (`--native` makes that an
  error instead). A WebDriver click on a phone or tablet is reported as `simulated click, not a
  finger`: it focuses the control and can show its focus ring or tooltip where a finger would not,
  and Safari sends it as mouse events without `pointerdown`. Before calling such an effect a bug,
  repeat the tap without `--simulated`.
- **The keyboard.** `qa keyboard` taps Done above the iOS keyboard. On Android it presses Back only
  while the keyboard shows (Appium's word for it, or a page shorter than the window): with the
  keyboard up Back only closes the keyboard; without it Back would reach the page and close its
  dialog or go back, so it then does nothing and says `no on-screen keyboard was open`. An open
  dialog stays open either way.
- **Browser prompts on Android.** Chrome draws its own questions (`... wants to see text and images
  copied to the clipboard`, Block / Allow; location, notifications, Android's own permission
  dialogs) outside the page, so the page's screenshot misses them. When the page loses the focus
  after an action, and on every `qa look` and `qa shot`, the driver looks at the native screen: a
  prompt is named in the output (`native prompt over the page: "..." [Block] [Allow]`) and that
  action's screenshot shows the whole screen with it. Answer it as the step needs with `qa prompt
  allow` or `qa prompt block` (it presses the prompt's own button); `qa prompt` says so when no
  prompt is open. On iOS the screenshot shows Safari's prompts anyway, and `qa prompt` refuses there.
- **Keys.** Phones and tablets have no hardware modifier keys: `qa key` refuses a combination there.
  Use what a person would, the app's own on-screen key bar (tap its Ctrl, Tab, Esc ... keys with
  `qa tap`), or the control the shortcut stands for.
- **Finding controls.** A tap takes the control a person sees: exact names before partial ones, then
  one visible on top (an open menu's item before the same text further down). `--role` narrows to a
  role (button, link, textbox, ...), `--exact` to the exact name, `--any` also matches plain text (a
  heading, a paragraph). `--css` alone taps what the selector matches; with a text it is the area the
  text is searched in. `--nth <n>` (from 1) picks among the matches `tap` lists. A control another
  element covers at its centre is reported (`covered by ...`) instead of tapped; `--force` taps it
  anyway. To check that a control is there without setting anything off, use `qa find`.
- **Orientation.** `tesla` stands in for a car's screen: a Galaxy Tab S9 that starts in landscape. Keep
  it in landscape unless a step rotates it; rotate it back after such a step. The other phones and
  tablets start in portrait.
- **Screenshots** of iOS show the whole screen with Safari's bars; Android and desktop show the page
  viewport. Desktop windows are maximised at 1920x1080.
- **Limits.** BrowserStack ends a session after 2 h; from 100 min on every action warns.
- **A broken session.** A command that hangs past two minutes or answers `Session not started or
  terminated` lost its BrowserStack session (Appium sometimes hangs when it switches between the
  page and the native layer). `qa stop` and `qa start` give a new device, but on a new page: judge
  what you saw, and when the cell cannot go on, abort it.
- **Idle.** Without a command for 20 minutes the session is closed; `qa start` starts a new one.

## Secrets

The driver reads the BrowserStack account and the logins itself. Sign in to the test instance with
`qa type --secret QA_SOCRATES_PASSWORD --into Password` (add `--native` on a phone or tablet); only
`QA_*` names can be typed, never the BrowserStack account. Never print, paste or type the password
yourself, never put it into a command, a judgement or a summary, and never read the vault file.
Recorded URLs have their credential values (`token=`, `code=`, ...) cut.
