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
| `qa tap <text>` | Taps a control (`--native` for a real touch on phones and tablets, `--force` to tap one another element covers) |
| `qa type <value> --into <field>` | Types into a field (`--native` through the device keyboard, `--clear` first, `--submit` presses Enter after); without `--into` into the focused field |
| `qa type --secret QA_NAME --into <field>` | Types a login of the vault (`QA_SOCRATES_PASSWORD`) without it passing through you |
| `qa key <name>` | Presses enter, tab, escape, backspace, up, down, left, right or space; on a desktop also `ctrl+<letter>`, `ctrl+enter`, `meta+enter`, `shift+enter` and `shift+tab` |
| `qa keyboard` | Closes the on-screen keyboard as a person would (Done on iOS, Back on Android) |
| `qa scroll down\|up\|top\|bottom` | Scrolls by most of a screen, or to the top or bottom |
| `qa rotate landscape\|portrait` | Rotates a phone or tablet (refused on a desktop) |
| `qa back`, `qa forward`, `qa reload` | Browser navigation |
| `qa wait <seconds>` | Waits, keeping the session alive |
| `qa look` | Screenshot and the controls on screen: role, name, state, value (a password shows as `(hidden)`), notices and dialogs |
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
- **Settling.** Every action waits until the page is quiet (no DOM change for 0.8 s, no visible
  `.ds-skeleton` or `aria-busy="true"`), at most 20 s; past that the action says `not settled` and what
  was still busy. A page that never stops changing (a chat streaming a reply, a running terminal) is
  `not settled` after 20 s: that is the page working, not a fault by itself.
- **Errors.** BrowserStack has no console log on iOS. The driver collects page errors, rejections and
  console errors and warnings after each action (`page errors:`; errors during the first load are
  missed). `qa logs` adds the server's own lines; other cells of the run use the same test instance at
  the same time, so a line may be theirs.
- **Native input.** On phones and tablets type with `--native`: the driver taps the field with a real
  touch and types through the device keyboard (iOS) or the focused field (Android). Plain typing skips
  the keyboard and misses zoom, IME and viewport effects. If a tap right after typing has no effect,
  repeat it with `--native` (the closing keyboard moves the page).
- **Native taps** go where a person's finger would: the driver marks the control in the page, finds
  the marks on the device screen and touches their middle, then checks which element the page
  received (`the touch landed on ...` is an error of the action; the page may have changed). A control
  the screen does not show (under the on-screen keyboard, scrolled away) is refused as `not visible`:
  scroll, or close the keyboard with `qa keyboard`, then judge whether a person could have reached it.
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
