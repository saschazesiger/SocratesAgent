# Socrates rewrite: decisions fixed with the product owner (2026-09-02)

These are binding. The design spec must implement them; it may add detail but not contradict.

## Product
- Socrates is no longer a web chat. A "chat" (rename to "session" in code; UI label "Session") is an
  interactive terminal running one of: **Shell** (user's login shell / bash), **Claude Code**,
  **Codex**, **OpenCode**.
- New-session flow: pick harness -> pick working directory -> pick model (model step hidden for Shell)
  -> terminal opens full-area, white background (light theme), and is driven interactively.
- Working directory choices: "Dynamic" (default; creates a fresh directory per session under the
  workspace root) plus admin-defined preset directories for quick selection, plus a free-form path.
- Multiple browsers may attach to the same session at once (phone + laptop); all see the same screen.
- Session titles: default "<harness> · <dir name or date>", renameable in the UI.
- Everything shipped (UI copy, code, docs, commits) in English. Repo stays the same; clean cut, no legacy.

## Durability (the core requirement)
- **tmux is the substrate.** Every session is a tmux session in a Socrates-owned tmux server
  (own socket, e.g. `-L socrates` or `-S <data>/tmux.sock`), so it survives Socrates restarts,
  crashes, upgrades and lost connections. Socrates attaches to it via a PTY (`tmux attach`) or
  `control mode` — the spec must choose and justify (control mode `-CC` vs a per-viewer
  `tmux attach -t` PTY; consider resize semantics with multiple viewers: use
  `window-size latest`/`aggressive-resize`).
- Socrates never kills a tmux session unless the user explicitly deletes the Socrates session.
- Output journal: Socrates keeps its own append-only byte log per session (for reconnect replay and
  scrollback) AND relies on tmux for the live screen. On (re)attach the browser must show the
  correct current screen immediately (tmux redraw), not a replay of megabytes.
- Machine reboot: tmux sessions are gone. Socrates stores the CLI's own session/thread id per
  session (Claude `--session-id`/`--resume`, Codex `resume <id>`, OpenCode `--session`) and the
  working directory, and on next open restarts the CLI with resume in the same directory.
  For Shell, it just starts a new shell in the same directory. UI must show "resumed after
  restart" clearly.
- Network loss on the phone: the client shows a visible disconnected state, buffers typed input
  locally, and on reconnect resumes the byte stream from the last acknowledged sequence number
  (no duplicated or lost keystrokes; use seq-numbered frames both ways). Keep net.js style
  status bar and sw.js app shell.
- tmux missing: Socrates tries to install it automatically (apt-get / apk / dnf / brew, with sudo if
  needed and available), shows progress and result in setup/admin; Dockerfile installs tmux.
  If install fails, a clear error with manual instructions. No PTY fallback.

## Admin (everything configurable)
- Per harness, expose every meaningful launch option as a setting (research in
  ../research/*.md is the source of truth): default model, effort/reasoning, permission mode /
  dangerously-skip-permissions / approval policy / sandbox, remote control on/off, extra
  flags, extra env vars, extra directories, theme (light), etc. Settings are stored in the kv
  store and applied at session start (flags + env + generated config where a flag does not exist).
- Global: workspace root, preset directories list, default harness, tmux status/installer,
  tunnel (keep), voice (keep), password (keep).
- Per-session overrides of the harness options at creation time are allowed via an "advanced"
  disclosure but not required in v1 beyond model + directory.

## Keep
- Auth/login/setup, admin, cloudflared tunnel, SQLite store (chats->sessions, kv, sessions),
  binary discovery per CLI, service worker, net.js connection status, e2e Playwright runner,
  voice: dictation must be fully working (mic button transcribes via OpenRouter and types the
  text into the terminal input field), TTS/piper stays in the codebase for a future integration
  (not wired into the terminal UI, but must still build and be configurable in admin).
  - **Superseded 2026-09-02** (the dictation clause only): there is no terminal input field to
    type into. The microphone is in the chat panel and its transcript is sent as a question. TTS
    is wired to that chat — a dictated question is answered out loud — and still not to the pane.
    See "Revised with the owner" below.
- Design rules: all-white, agent marks (logos.js), technical detail hover-only, subtle motion.

## Delete
- internal/harness protocol adapters (claude/codex/opencode event normalisation) and their fakes,
  internal/engine, messages/runs/steps tables, chat.js transcript UI, SSE chat transport,
  agent-host in its current form (replace by tmux management).

## Frontend
- xterm.js vendored into internal/web/static (no CDN; app must load offline), with fit addon,
  unicode11, web-links; WebGL renderer optional with canvas/DOM fallback. Light theme.
- Mobile: key bar (Esc, Tab, Ctrl, Alt, arrows, Enter, Ctrl-C, Ctrl-D, paste) plus a line input
  field that sends whole lines (fights autocorrect) with dictation button; direct typing in the
  terminal also works.
  - **Superseded 2026-09-02**: the line input and its dictation button are removed on every
    device, and the key bar is off until the session menu asks for it. Direct typing in the
    terminal is unchanged. See "Revised with the owner" below.
- Transport: one WebSocket per viewer, binary frames, seq numbers, resize messages, ping/pong
  watchdog.

## Process
- Claude (orchestrator) writes no code. Opus subagents implement and test in work packages; each
  package is reviewed and approved by at least one Fable subagent before it is merged; rejected
  packages go back to Opus with the review. Final integrated verification by Fable: real browser,
  all four session types with fake CLIs on PATH, Socrates restart mid-session, browser offline
  and back, multi-viewer, reboot-resume simulation.

## Revised with the owner (2026-09-02, after the first build was used on a phone)

Binding like the rest. These supersede the bullets marked **Superseded 2026-09-02** above; the
superseded text stays where it is, because the history is the reason.

- **No line input under the terminal.** The composer, its microphone, its Send button, the pending
  lines and the drafts it kept are gone on every device. It existed to fight autocorrect by
  holding a whole line, and it cost the page a second field, a second microphone and a promise
  about text nobody had sent yet. Single keys go into the pane; a whole sentence goes to the chat.
- **The key bar is off by default on every device, and turned on from the ⋯ session menu**
  ("Show key bar" / "Hide key bar"), with the answer remembered per device. No media query, no
  viewport width, no platform string and no keystroke seen decides it: guessing was wrong on a
  tablet in a case and on a laptop with a touch screen. Touch/keyboard device detection is
  removed with it.
- **One chat for everyone, with dictation in it.** One input row: text field, microphone, Send.
  A question that was dictated is answered out loud; a typed one is answered in writing. Any
  answer can be read out afterwards by double-tapping it, and the same gesture stops it. A running
  recording shows exactly the two endings it has: **Send recording** and **Discard recording**.
  While a recording is open nothing is spoken, and starting one silences a voice already reading.
- **Auto mode is gone** — the switch, the audio bar, and the summary spoken on every transition
  out of busy. Being read to is a property of a question, not a mode a device is in, and unasked
  speech in a car is worse than silence. The header keeps two buttons: **Summarize this session**
  and **Ask the agent**.
- **Notifications and sound: two header toggles, per device.** When any session goes from busy to
  idle or waiting, a chime (Web Audio, no audio file to fetch) and a browser notification. Sound
  defaults on; notifications default off, because they cannot be honoured without asking and
  asking unprompted is how a page gets blocked for good.
- **Speaking is a pill and listening is a switch (2026-09-02).** In the chat header: a **Speak**
  pill (outlined, 44px, microphone and the word) that opens a recording sheet with a live level
  meter, the clock, and two 56px buttons — **Send** and **Cancel**; Escape or the backdrop is
  Cancel, and closing the sheet for any reason gives the microphone back. The input row is a field
  and a Send, with no microphone in it. Beside the pill, a speaker toggle that decides whether
  answers are read out loud: **off by default**, remembered per device, and **switched on by the
  first successful dictation** — it can be switched off again by hand. It replaces the old rule
  that a spoken question earned exactly one spoken answer, which went silent on the first typed
  follow-up in a car. The toggle's state at send time is what goes to the server as `auto`, so an
  answer that will be read out is phrased for the ear.
- **The session list is grouped by day** — Today, Yesterday, This week, This month, Older — by
  when each session was last used, in the browser's own local calendar rather than the server's.

Detail: `docs/design/DESIGN.md` §E.6 and §E.8 (revision 4), and `docs/design/ACTIVITY.md` §D.

## Revision 5 — the shell, 2026-09-02

Four changes to the frame around the terminal. Binding like the rest.

- **A notice puts itself away.** Every line above the pane — `resumed`, `resized`, `desync`, the
  answer **Status** puts there — fades after 6 s, and a new line replaces whatever is up. The
  timer and the close button are the same decision, so the `resumed` line still acknowledges
  itself on the way out. A line carrying a control the person has yet to press (the **Cancel** of
  a running run) is not news and stays. The close button never goes away.
- **The selected cell of a `.seg-row` hugs the corner.** The 1px accent ring is drawn inside a
  square cell, and a square corner inside an 11px row was clipped: the two cells that touch a
  corner now carry the row's own inner radius, so the ring, the hover fill and the clip follow one
  curve at 1× and at 2×.
- **The working directory is a dropdown, not a row of cells.** The same strict `combobox` the
  model is picked with: `Dynamic`, one entry per admin preset (its name, with the full path as
  sub-text) and `Custom path…`. A machine with a dozen preset directories on it was a dozen cells
  three characters wide. Nothing about what is POSTed changes.
- **The sidebar collapses to a rail.** 264px of names, or 56px of marks, from one hairline button
  beside the brand, remembered per device. On a phone the sidebar is already a drawer, so the
  control is not there and the rail is never entered.

Detail: `docs/design/DESIGN.md` §E.2, §E.3, §E.7 and §E.8 (revision 5).
