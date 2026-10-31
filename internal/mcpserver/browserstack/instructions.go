package browserstack

// instructions is what the agent is told about the server (the body of the
// qa tool's TOOL.md, with the tools for the commands).
const instructions = `The browserstack MCP server drives one real device on BrowserStack (a phone, a tablet or a desktop browser) for this chat. start names the device (devices lists them). Socrates holds the device session between your calls and keeps it alive while you think. Every action is recorded with its screenshot, and its answer shows you the screenshot: that is what your judgements rest on. With Socrates Tests on, the screenshots taken while you work a step of a test are attached to that step when you judge it (test_step).

## Tools

- start: starts the device and answers "device ready: ...". When BrowserStack queues the device (the account is busy elsewhere) or every account is in use, it answers "still queued" after about 8 minutes while Socrates keeps waiting: call start again, as often as it says so.
- open {path}: opens a page; the session's locale is put in front (/create opens /es/create), a path that names a locale or a full URL is taken as it is. open {mail_link: n} opens link n of the last mail mail read, so a magic link never passes through you.
- find {text}: scrolls a control into view without touching it: shows that it is there, whether it is disabled and what covers it.
- tap {text}: taps a control (native: a real touch on phones and tablets; force: tap one an ad covers).
- type {value, into}: types into a field (native through the device keyboard, clear first, submit presses Enter after); without into, into the focused field. Instead of value: secret types a login of the repository by name (QA_GOOGLE_PASSWORD, ...), totp the current authenticator code (google, ms), address this session's mailbox address, code the code of the last mail.
- key {name}: presses enter, tab, escape, backspace, up, down, left, right or space.
- keyboard: closes the on-screen keyboard as a person would (Done on iOS, Back on Android).
- scroll {where}: down, up (most of a screen), top or bottom. rotate {orientation}: landscape or portrait (refused on a desktop). back, forward, reload: browser navigation. wait {seconds}: waits, keeping the session alive.
- look: a screenshot and the controls on screen: role, name, state, what covers them. shot: a screenshot only, without waiting for the page.
- env: the device facts (user agent, viewport, pixel ratio, system language). address: this session's own mailbox address, fresh per session.
- mail {to?, wait?}: waits (90 s by default) for the newest mail to this session's address and shows sender, subject, code and links.
- logs: the application's WARN and ERROR lines since the device started; kept in the evidence.
- stop: closes the device session. Socrates closes it when the chat is deleted and after 20 minutes without a call; you need it only to start over with start.

Every action answers with its number, its time, "ERROR ..." when it failed, what it hit, "not settled" when the page did not come to rest, the consent dialog's answer, the page errors, the URL, the screenshot's path and the screenshot. Look at the screenshot.

## How the devices behave

- Consent and ads. The ad consent dialog (Google Funding Choices) appears late and in several variants; the driver answers it after every action with the consent policy and retries a tap the dialog covered. Ads, also sticky video players, sit in the layout: never tap one. A control covered by an ad is reported ("covered by ...") instead of tapped; force taps it anyway.
- Settling. Every action waits until the page is quiet (no DOM change outside ads for 0.8 s, no visible skeleton or aria-busy), at most 20 s; past that the action says "not settled" and what was still busy.
- Errors. BrowserStack has no console log on iOS. The driver collects page errors, rejections and console errors after each action ("page errors:"; errors during the first load are missed). logs adds the application's own lines; other sessions may use the same site at the same time, so a line may be theirs.
- Native input. On phones and tablets type with native: the driver taps the field with a real touch and types through the device keyboard (iOS) or the focused field (Android). Plain typing skips the keyboard and misses zoom, IME and viewport effects. If a tap right after typing has no effect, repeat it with native (the closing keyboard moves the page).
- Native taps go where a person's finger would: the driver marks the control in the page, finds the marks on the device screen and touches their middle, then checks which element the page received ("the touch landed on ..." is an error of the action; the page may have changed). A control the screen does not show (under the on-screen keyboard, scrolled away) is refused as "not visible": scroll, or close the keyboard with keyboard, then judge whether a person could have reached it.
- Finding controls. A tap takes the control a person sees: exact names before partial ones, then one visible on top (an open menu's link before the same link in the footer). role narrows to a role (button, link, textbox, ...), exact to the exact name, any also matches plain text (a heading, a paragraph). css alone taps what the selector matches; with a text it is the area the text is searched in. nth (from 1) picks among the matches tap lists. To check that a control is there without setting anything off (Start session), use find.
- Logins always go through the real UI with secret, totp, address and code: tokens are bound to the User-Agent, so a token from elsewhere is refused.
- Screenshots of iOS show the whole screen with Safari's bars; Android and desktop show the page viewport. Desktop windows are maximised at 1920x1080.
- Limits. BrowserStack ends a session after 2 h; from 100 min on every action warns.
- A broken session. A call that hangs past two minutes or answers "Session not started or terminated" lost its BrowserStack session (Appium sometimes hangs when it switches between the page and the native layer). stop and start give a new device, but on a new page: judge what you saw, and when a test cannot go on, judge the rest blocked.
- Idle. Without a call for 20 minutes the session is closed; start starts a new one.

## Secrets

Socrates reads the BrowserStack account and the repository's logins itself; you never see them. Type a login only with secret, totp, address or code, open mail links only with mail_link; never put a password, a token or a magic link into a call, a judgement, a test or a summary. Recorded URLs have their credential values cut.`
