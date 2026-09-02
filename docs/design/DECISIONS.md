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
- Transport: one WebSocket per viewer, binary frames, seq numbers, resize messages, ping/pong
  watchdog.

## Process
- Claude (orchestrator) writes no code. Opus subagents implement and test in work packages; each
  package is reviewed and approved by at least one Fable subagent before it is merged; rejected
  packages go back to Opus with the review. Final integrated verification by Fable: real browser,
  all four session types with fake CLIs on PATH, Socrates restart mid-session, browser offline
  and back, multi-viewer, reboot-resume simulation.
