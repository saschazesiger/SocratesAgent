---
title: Socrates web interface (the test instance)
tools: [browserstack]
columns: device
dimensions:
  device:
    values:
      iphone: iPhone 15 · Safari
      ipad: iPad 10th · Safari
      pixel: Pixel 9 · Chrome
      tesla: Galaxy Tab S9 landscape · Chrome (car screen)
      win-chrome: Windows · Chrome
      mac-safari: macOS · Safari
    extra:
      iphone-old: iPhone 13 · iOS 16
      moto: Moto G71 · Chrome
      samsung: Galaxy S25 · Samsung Internet
      win-firefox: Windows · Firefox
      win-edge: Windows · Edge
    default: all
params:
  base_url: https://socrates-qa.zesiger.net
  service: socrates-qa
evidence: screenshot
timeout: 45m
---
The whole web interface of Socrates (Home, sessions, chats, the Shell, Admin, Costs, Tests) on the
test instance, on real BrowserStack devices through the `qa` tool (TOOL.md). The device ids above are
the tool's (`src/devices.ts`; a unit test of the tool keeps both the same). `tesla` stands in for the
owner's Tesla Model 3 screen: a large touch screen in landscape running Chromium.

## Working a cell

Start the device with `qa start` (run it again while it says "still queued"). Every test starts on a
fresh device, signed out: unless the test is about signing in, step 1 begins with signing in (`qa open
/`, which lands on the sign-in page, then `qa type --secret QA_SOCRATES_PASSWORD --into Password`, with
`--native` on phones and tablets, then Sign in); the sign-in is part of step 1's evidence, and a sign-in
that does not work fails step 1.

Then, for each step in turn:

1. Carry it out with as many actions as it takes (`qa open`, `tap`, `type`, `scroll`, `rotate`,
   `look`, ...). Gestures are there as a person makes them: `qa swipe` (one finger; the content
   follows it, swipe down to see what is above), `qa wheel` (desktops), `qa hold` (a long press),
   `qa tap --double`, `qa drag`, aimed with `--css` and `--at` at a point inside a box such as a
   terminal (`--css .xterm-screen --at 5%,40%`); `qa pick` sets a date or time field with the
   device's own picker; `qa native look` / `qa native tap` reach what the browser draws outside the
   page (a file chooser, a picker), TOOL.md says how. Each action prints its screenshot path: read the screenshot, and `qa look` where
   controls matter. A step names the person's goal: reach it the way this device offers it (the
   Sessions button and the modal drawer on a phone, the docked list on a desktop, an action sheet
   instead of a menu on a phone); it is `na` only when it cannot exist on the device (rotation on a
   desktop, a key bar on a desktop without touch).
2. Judge it with `test_judge`: the step, the verdict and what is on screen. The actions since the
   previous judgement become the step's evidence, so judge each step right after carrying it out. A
   pass, issue or fail needs a screenshot among them; never judge what you have not seen. Describe
   what is on screen, not what the step should have done. Judging a step again replaces its verdict.
3. A step that cannot be carried out is `blocked` (or `na` by design); when the rest of the test is
   unreachable, judge the rest `blocked` with `test_judge` (`rest`) and an observation "not reached:
   ...".

Judge only what you have seen. A step that expects something of the whole page (every card, nothing
cut off) needs the whole page: scroll through it and read each screenshot; otherwise say which part
you checked. A claim about a label, a count or a message must be on a screenshot or in `qa look`.

`test_steps` shows the verdicts so far and the next step. For a cell with a step that is not `pass`,
run `qa logs` before you end it and name the server's lines that belong to the problem in the
observation or the summary. End with `test_end` once every step is judged: the summary is the
conclusion in one or two sentences, what works and what does not on this device. A wrong tap is
harmless: it stays in the step's evidence, the verdict is what counts.

When the device session broke (TOOL.md, "A broken session") and the cell cannot go on, call
`test_abort` with the reason: the steps not judged become `blocked`, and Socrates retries the cell.

## What every step also checks: the layout

Besides what a step expects, look at every screenshot as a person on this device would. These are
`issue`s (or `fail`s when they stop the step):

- The page scrolls sideways, or something sticks out of the screen, is cut off or overlaps.
- Text is clipped where it should wrap, too small to read, or a label is truncated so it cannot be
  told what it means (an ellipsis on a long session title in a list is by design).
- A control is too small or too close to another to hit with a finger on a touch device (touch mode
  gives them 48 px), or sits under a bar, the notch, the home indicator or the on-screen keyboard.
- The on-screen keyboard hides the field being typed in or the button that sends it.
- A dialog or sheet cannot be closed, or its buttons are off screen.
- After a rotation the layout did not follow, or a typed text or an open dialog was lost.
- Something is slow enough for a person to notice (say so with the time), or flickers.

## How Socrates adapts to the device (by design)

- **Touch mode** is on by itself on phones, tablets and the car screen: bigger targets, the key bar on
  a terminal, and **Enter adds a new line in the composer; Send sends**. On a desktop Enter sends, and
  Ctrl+Enter (Cmd+Enter on a Mac) sends everywhere.
- **Up to 900 px wide** the session list is a modal drawer opened by the **Sessions** button (top
  left) over a scrim; wider, it is docked and **Hide the session list** collapses it.
- **Up to 600 px wide** sheets (the create sheet, ...) take the whole width and rise from the bottom,
  ⋮ menus become an action sheet at the bottom, and Copy, Paste and Upload of a terminal move into its
  ⋮ menu; ⋮ menus are an action sheet on a touch screen 500 px tall or less too. The question dialog
  is full screen up to 900 px wide, and in touch mode at any width (the car screen, an iPad).
- On a phone in landscape (500 px tall or less) the drawer's footer shows icons only and the logo in
  the top bar is hidden.
- In touch mode the top bar steps aside while a field of the page is being typed in.
- There is no dark theme.

## Verdicts on Socrates

- `issue`: the step works, but something is wrong a person would notice: the layout rules above, a
  confusing or English-only-by-mistake message, slowness.
- `fail`: the step cannot be completed, gives a wrong result, loses a message or a typed text, hangs,
  shows an error the person did not cause, or a page error that broke something.
- `blocked`: BrowserStack, the device, a native browser dialog the tool cannot answer, an agent
  account that is not signed in, an agent provider that is down or out of quota, a step never reached.
- `na`: the step does not exist on this device by design (rotation on a desktop).
- `not settled` after an action is an observation: a streaming chat or a running terminal never comes
  to rest. Wait (`qa wait`) and look again before judging.
- A tap right after typing that has no effect is repeated with `--native` before it is called a bug
  (the closing keyboard moves the page).
- A focus ring, a tooltip or another effect after a tap the output calls `simulated click, not a
  finger` is repeated without `--simulated` before it is called a bug: a finger may not cause it.
- A `native prompt over the page` in an action's output is the browser's own question (Chrome's or
  Android's on Android, Safari's alert on iOS: the clipboard, the microphone, notifications), shown
  in that action's screenshot, not a bug of Socrates. Answer it as a person would with `qa prompt
  allow` (or `block`) and go on with the step; it is `blocked` only when `qa prompt` cannot answer it.
- On a desktop the browser's prompts are out of reach. A step that needs the microphone or
  notifications there starts the device with them given in advance: `qa start --grant microphone`
  (or `notifications`) on Windows Chrome, then judge what follows and say the permission was given in
  advance (the browser asked nothing). Safari on the Mac takes no such grant: that step is `blocked`.
- A `NOTE:` line in an action's output is something the driver did as a person would, or had to do
  instead: read it before judging. The first touch on a field the page focused itself raising no
  keyboard on iOS (the driver then touched it again) is something a person meets: an `issue` when
  it is the step's field. A clipboard read refused on iOS (the driver keeps Safari's Paste callout
  from hanging the device) is the tool's doing: judge the page's answer to it (a clear message is
  fine), and say Safari's callout itself was not tested.
- `NOT THE DEVICE'S PICKER` in `qa pick` means the value was set by script: judge the field, the hint
  and what follows, and say the picker itself could not be used.
- A swipe, wheel or hold names where it went (CSS px and screenshot pixels): if it missed the word or
  the line, aim again with `--at` before calling it a bug.

## What is not a bug here

- **The devices speak English**, and so does Socrates (there is no other interface language); what
  the browser draws itself (validation bubbles, permission prompts, the date and time picker, Safari's
  and Chrome's bars) follows the device.
- **Screenshots** of iOS show the whole screen with Safari's bars; Android and desktop show the page
  viewport. Desktop windows are maximised at 1920x1080.
- **Other cells' sessions** in the lists and on Home, and counts that change while you look: the
  instance is shared (TESTS.md).
- An unknown path (`/does-not-exist`) answers with a plain-text "404 page not found": that is the
  server's default, not a page of the app.
- Notifications that need the page to be added to the Home Screen on iOS say so; that is by design.
