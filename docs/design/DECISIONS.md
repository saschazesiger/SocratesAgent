# Socrates rewrite: decisions fixed with the product owner (2026-09-02)

These are binding. The design spec must implement them; it may add detail but not contradict.

> **Superseded (2026-09-02, branch `google-tts`):** the local Piper voice and
> `internal/piper` are gone. Reading an answer out loud is now Google Cloud
> Text-to-Speech (`internal/googletts`), called with an API key from
> Admin → Voice; `GET /api/voice/status` was replaced by `POST
> /api/voice/check`. Passages below that describe the local engine are
> history.

## Product

> **Superseded for the agents (2026-10-23) by REBUILD.md §1 and §7:** Claude Code, Codex and
> OpenCode run as chats through their own protocols; only the Shell (and a folder's VS Code) is a
> terminal, and an agent's terminal is refused since WP10.

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
- *The agents' part superseded by REBUILD.md §2.1 (a chat resumes through its agent host); the
  Shell's holds.* Machine reboot: tmux sessions are gone. Socrates stores the CLI's own session/thread id per
  session (Claude `--session-id`/`--resume`, Codex `resume <id>`, OpenCode `--session`) and the
  working directory, and on next open restarts the CLI with resume in the same directory.
  For Shell, it just starts a new shell in the same directory. UI must show "resumed after
  restart" clearly.
- *For a chat superseded by REBUILD.md §2.2: what is sent waits in the page's outbox and goes
  once, and the chat stream replays the journal from the last event the page had.* Network loss
  on the phone: the client shows a visible disconnected state, buffers typed input
  locally, and on reconnect resumes the byte stream from the last acknowledged sequence number
  (no duplicated or lost keystrokes; use seq-numbered frames both ways). Keep net.js style
  status bar and sw.js app shell.
- tmux missing: Socrates tries to install it automatically (apt-get / apk / dnf / brew, with sudo if
  needed and available), shows progress and result in setup/admin; scripts/install.sh installs tmux.
  If install fails, a clear error with manual instructions. No PTY fallback.

## Admin (everything configurable)
- **Superseded 2026-09-02 by "Admin (the basics only)" below.** Kept for the record.
- Per harness, expose every meaningful launch option as a setting (research in
  ../research/*.md is the source of truth): default model, effort/reasoning, permission mode /
  dangerously-skip-permissions / approval policy / sandbox, remote control on/off, extra
  flags, extra env vars, extra directories, theme (light), etc. Settings are stored in the kv
  store and applied at session start (flags + env + generated config where a flag does not exist).
- Global: workspace root, preset directories list, default harness, tmux status/installer,
  tunnel (keep), voice (keep), password (keep).
- Per-session overrides of the harness options at creation time are allowed via an "advanced"
  disclosure but not required in v1 beyond model + directory.

## Admin (the basics only) — 2026-09-02, supersedes the section above

> The settings hold. The fixed policy for the agents is the chats' since REBUILD.md §3: `claude -p`
> over its control protocol, `codex app-server` and `opencode serve`, still with every permission
> allowed; it lives in the adapters (`internal/agent/*`, AGENT-HOST.md §5), and HARNESS-POLICY.md's
> terminal launches are history.
- The dashboard exposes **basic settings only**. Per harness: whether it is offered, the binary
  path (discovered, overridable), the model short list, the default model, and the default
  reasoning effort for Claude Code and Codex. Nothing else.
- Everything that used to be a control — permission mode, dangerously-skip, sandbox, approval,
  network access, writable roots, bypass, remote control and its name/prefix/address/token,
  extra flags, extra env, extra directories, theme, the `CLAUDE_CODE_*` / `CODEX_*` /
  `OPENCODE_*` toggles, the permission JSON, max thinking tokens, the settings and config
  textareas — is **hard-coded** with opinionated defaults. The Go structs shrank accordingly and
  the argv builders became fixed policy.
- The fixed policy, and the binary-verified source of every flag in it, is
  [HARNESS-POLICY.md](HARNESS-POLICY.md). In short: Claude Code always starts with
  `--dangerously-skip-permissions` and Remote Control off (never `--remote-control`, plus
  `disableRemoteControl` in the generated settings and `remoteControlAtStartup: false` in the
  global config); Codex always `--dangerously-bypass-approvals-and-sandbox` and never `--remote`;
  OpenCode allows every permission through `OPENCODE_PERMISSION` and the generated config;
  Shell is a login shell.
- Global settings are unchanged: workspace root, preset directories, default harness, tmux
  status/installer, terminal behaviour, tunnel, voice, password, setup check, OpenRouter key and
  models for dictation/Status/Agent.
- Migration is "ignore the old keys": a stored document keeps decoding, the dead keys go nowhere,
  and the next save drops them. No save is refused for a value that is no longer a setting.

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
  - **Restored 2026-09-04**, in the terms it was first asked for: the agent and the chat are gone,
    the microphone is a pill in the session's top bar, and its transcript is typed into the pane
    itself — on the prompt, without the `⏎`, because a transcript is a guess. TTS is now only
    **Status**, which reads a summary of the screen; nothing else is read out loud.
  - *For the chats (REBUILD.md §7, WP9):* a chat has a composer again, and the microphone's
    transcript goes into it, still for the person to send; **Status** reads a chat from its
    journal, a Shell from its screen.
- Design rules: all-white, agent marks (logos.js), technical detail hover-only, subtle motion.

## Delete

> **Reversed for the agents (2026-10-23) by REBUILD.md §2 and §3:** they run through protocol
> adapters again (`internal/agent/claude`, `codex`, `opencode`), each in an agent host of its own
> (`internal/agenthost`) with a journal per chat. Neither is the code deleted here: both were
> written new for the rebuild.

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

- *A chat has a composer (REBUILD.md §7); what follows holds for the Shell's terminal.*
  **No line input under the terminal.** The composer, its microphone, its Send button, the pending
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
  and **Ask the agent**. — *Superseded 2026-09-04: the agent is gone, and the second button is
  the **Speak** pill.*
- **Notifications and sound: two header toggles, per device.** When any session goes from busy to
  idle or waiting, a chime (Web Audio, no audio file to fetch) and a browser notification. Sound
  defaults on; notifications default off, because they cannot be honoured without asking and
  asking unprompted is how a page gets blocked for good.
- **Speaking is a pill and listening is a switch (2026-09-02).** *Half superseded 2026-09-04: the
  pill moved to the top bar and its transcript goes into the pane, so the speaker toggle and the
  `auto` phrasing went with the chat.* In the chat header: a **Speak**
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

## Revision 6 — the bucket, prepared sessions and the scheduled start, 2026-10-13

> **Superseded (2026-10-23) by REBUILD.md §6.** S3 is no longer the store of record: the
> data directory is the only truth, and the bucket holds an encrypted, incremental backup of it
> (`internal/backup`) that nothing reads while Socrates runs. The mirror, the lease, connect
> mode and the storage banner are gone. What follows is history.

Binding like the rest. Detail: `docs/design/DESIGN.md` §K and §L.

- **S3 is the store of record, and it is mandatory.** Socrates' own state — settings, the password
  hash, sign-ins, profiles and their order, push keys and subscriptions, unread state, every
  session row, every prepared prompt — lives in one bucket under an optional prefix. Without a
  usable bucket the server serves the connect page and `/api/health`, and nothing else. SQLite
  stays as the local working copy, so no request waits on the network and the rest of the code
  barely changed; the data directory keeps what belongs to the machine and the CLIs (`HOME`,
  transcripts, workspaces, tmux, journals, `plan.json`, VS Code). *Why:* a machine is
  replaceable, and the state that makes a Socrates *this* Socrates should come back on a new
  machine from an empty data directory.
- **Encrypted on this side, with a passphrase the bucket never sees.** AES-256-GCM, PBKDF2-SHA256
  with 600 000 rounds, the object name as AAD. Losing the passphrase loses the bucket's contents;
  the README says so in bold. Only `meta.json` and `lease.json` are plain.
- **Exactly one active Socrates per bucket and prefix**, enforced by a lease object (90 s, renewed
  every 30 s, released on a clean stop). A second one shows who holds it and can take it over
  with the Socrates password; the first one is then fenced — it stops uploading and auto-starting
  and says so in a banner. No merging of two writers: one of them is always wrong.
- **Bootstrap from the setup page or the environment.** The page writes `<data>/s3.json` (0600) and
  re-execs; `SOCRATES_S3_BUCKET` in the environment switches the environment on and it wins over
  the file. A configured server never lets the connect page point it at a different bucket.
- **The upgrade is the empty-bucket case.** A bucket with no state takes the local database as it is;
  a bucket with state wins over a data directory that belongs to another installation, unless the
  connect page was explicitly told to overwrite it. An emptied bucket never empties a machine.
- **The S3 client is hand written**, standard library only, like `webpush`. The deployed store
  (Hetzner, Ceph RGW) needs `x-amz-content-sha256` on every request and compares `If-Match` against
  the unquoted ETag; both are pinned.
- *PreStart holds (REBUILD.md §7): a prepared chat's prompt is its first message, sent as any
  message is, and no prompt file is written.* **PreStart: a session can be prepared before anything runs** — Claude Code, Codex and OpenCode
  only, switched on in the new-session sheet (off every time it opens). Its panel shows the sheet's
  own settings (one form builder for both) and a large prompt field that saves itself and is backed
  up through the bucket. **Start is exactly a new session's start** — one server function for both —
  with the prompt as the program's first message, passed through a file rather than through tmux,
  which caps a command at ~16 KB. Prepared sessions are their own group at the top of the sidebar.
- **A prepared session can start by itself at a date and time** (added by the owner while this
  revision was being built).
  The server does it, with no browser open, through the same start path; a schedule missed while the
  server was down runs once when it is back; a failed start is reported by push and not retried;
  a fenced or unconnected server never starts anything by itself. The time is set in the panel, not
  in the sheet.
- **The lease is also the record of who wrote the bucket last.** A clean stop zeroes the heartbeat
  instead of deleting `lease.json`, and a start only treats its own pending writes as newer than
  the bucket when the lease it replaced was its own. *Why:* a machine that crashed with a write
  pending, while another took the bucket and stopped cleanly, used to come back and upload its
  stale write over the other one's work; the bucket id alone cannot tell the two apart. A fence is
  remembered across a restart for the same reason.
- **Every write is bounded by a lease confirmation.** It has to start and end within 10 s of a
  confirmation taken while the lease stays fresh for that whole window; taking over a lease that
  was not released waits that window out (plus a margin). *Why:* a fence that only narrows the
  window still lets a slow upload of the old holder land after the new holder hydrated.
- **Connecting a data directory that already has a password takes that password.** The connect
  page is unauthenticated on a fresh data directory, like first-run setup; on an existing installation it
  would upload its sign-ins and keys to, or replace its state from, a bucket of anybody's choosing.
- **Damage to the bucket fails closed.** A missing `meta.json` over existing objects is never a new
  installation (a cached copy is put back, or the start is refused); a missing password hash over a
  bucket with sessions never opens first-run setup; an object that does not open changes nothing
  locally.
- **Every schema upgrade is backed up** (`socrates.db.pre-v8.bak`), and `scripts/update.sh` copies
  the database before the swap and puts it back with the previous binary when the new one does not
  answer.
  *Why:* the old build refuses a migrated database, so a rollback needs the file as it was.

## Revision 7 — the options menu, 2026-10-16

- **The top bar's `⋮` is this device's options, not the session's menu.** Key bar, touch mode,
  sound and notifications are switches in it, and it stays open while they are pressed. *Why:* the
  four are facts about this phone or this browser, two of them were buttons crowding a phone's bar
  and the key bar was hidden in a menu about something else; everything about a session is
  already in its row's `…`.
- **Touch mode can be set by hand, per device, and the setting wins.** *Why:* the guess is right
  for a phone and wrong for a tablet in a keyboard case or a browser that hides its finger, and
  the mistake was not one the person could correct.
- *The limits are an account's since REBUILD.md §4 (WP7), shown in a chat's account picker and the
  new-session sheet; the usage strip went with the profiles.* **The limits say when they renew,
  and a tap says exactly when.** *Why:* whether 80% matters
  depends on whether the window renews in ten minutes, and on a phone a tooltip does not exist.

## Revision 8 — the outcome of a turn, 2026-10-20

> The verdict is a chat's alone since WP10 (REBUILD.md §7): it is read from the chat's journal,
> and no screen is read for it any more.

- **A finished turn can be judged: complete, partly done, waiting on you, blocked.** Optional,
  off by default, one switch in Admin. *Why:* the detector says a session is at its prompt, not
  whether the task it was given is done, and that is the question a person glancing at Home has.
- **It is the digest's read, not a second call.** *Why:* the digest already reads every turn's
  end, and one answer cannot say "complete" next to an open question; a question makes it
  "Waiting on you".
- **While it is read the session shows the hourglass and sits under Background tasks, and its
  notification, chime and announcement wait up to 45 s.** A turn that goes on before the verdict
  is not announced. *Why:* the turn is over and something about it is still going on, and a
  notification that says how the work was left is worth a few seconds.
- **Never on a start, a restart or a resume; only after somebody typed.** *Why:* nothing was
  asked, so there is nothing to judge.
- **It files nothing.** Where a session is filed stays the person's. *Why:* the 2026-09-23 rule
  that a turn's end files nothing as review stands.
- **Claude's transcript is read for the instruction, the answer and the task list; the others
  give the screen.** The reason is always English. Verdicts are kept on this machine, not in the
  bucket.

## Revision 9 — an installation that keeps itself working, 2026-10-22

- **Every tool comes from its vendor, in a directory of its own version, and is switched by a
  link.** Go and Node from their official tarballs (checksums verified), Claude Code and Codex
  from their native installers, OpenCode and code-server from their release archives, all under
  `/opt/socrates/<tool>/<version>` or the vendor's own versions directory. *Why:* the
  distribution's Go (1.22) cannot build a `go 1.25` module and its Node (18) is past its end; and
  `npm install -g` rewrites a package in place under the sessions running it, which is how a
  Claude Code session dies of a bus error.
- **A version is deleted only when nothing runs from it.** The newest two are kept otherwise.
  *Why:* a VS Code or agent session that started before an update still loads files from its
  version for as long as it runs.
- **Everything updates itself, nightly and after every boot** (`socrates-update.timer`, from a
  copy of the scripts in `/usr/local/lib/socrates`). `SOCRATES_AUTO_UPDATE` is `all`, `deps` or
  `off` (repair only). *Why:* an agent CLI a few weeks old is a different program, and the boot
  run is also what puts back a tool that went missing. The copy, not a checkout, because a
  checkout is something people edit.
- **A new Socrates has to prove itself twice before it stays:** started once on a scratch
  directory before the swap, and answering `/api/health` with its own version - and not restarting
  - after it. Otherwise the previous binary and database are put back and the commit is written
  to `update-state/bad-commits`, so it is not tried again until a newer one arrives. *Why:* a
  build that fails only as the service (seen in testing: a database schema it does not know) would
  otherwise be retried, and rolled back, every night.
- **The update runs as a unit of its own,** never as a child of the shell that asked. *Why:* run
  from a Socrates session, the shell is in the scope and its terminal goes with the service's
  restart; the unit finishes regardless.
- **One tool failing is reported, not fatal; nothing is changed without network.** Each tool runs
  in a subshell of its own and records what it found in `updates.json`; the admin page's Updates
  card and the setup check read it.
- **The service has a watchdog, fed only while Socrates answers its own health check.** A start
  is given 15 minutes (three bucket connects), a stop one. `StartLimitIntervalSec=0`. *Why:*
  `Restart=always` restarts a process that exits, not one that hangs, and the start limit would
  otherwise park a crash loop in `failed` for good.
- **tmux stays held, and is upgraded by the updater when no tmux server runs at all.** *Why:* a
  tmux upgraded under a running server cannot talk to it, and a hold forever is a hold on its
  security fixes.
- **The tmux scope is retried under a unique name, and has `OOMPolicy=continue`.** *Why:* a
  leftover scope used to send the server into the service, where the next update killed every
  session; one runaway agent must not stop the scope.
- **Codex starts with `check_for_update_on_startup=false`; OpenCode's own updater stays off;
  Claude Code's stays on.** *Why:* Codex's prompt would block an unattended session and OpenCode
  replaces its one binary in place, while Claude Code's native updater switches versions
  atomically and is the fastest way to a fix.

