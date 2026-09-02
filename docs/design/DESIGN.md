# Socrates rewrite — binding implementation specification

**Revision 4** (2026-09-02). Supersedes revisions 1, 2 and 3 in full.

- **rev 4** (2026-09-02) — the line composer under the pane is gone, the key bar is off by default
  on every device and turned on from the session menu, one chat with dictation replaces Auto mode,
  and the session list is grouped by day with a chime and a notification when a session stops
  working. Passages changed by it carry `<!-- rev 4 -->`; §E.6 here and §D of
  `docs/design/ACTIVITY.md` are the detail.

Status: **binding**. Written 2026-09-02 for Opus implementer agents who have not seen the
conversation that produced it, and for a Fable reviewer who holds every work package against it.

Sources of truth, in order:

1. `scratchpad/design/DECISIONS.md` — product decisions. This spec implements them and may add
   detail; where this spec appears to contradict DECISIONS.md, DECISIONS.md wins and the
   contradiction is a bug in this spec, to be reported rather than silently resolved.
2. `scratchpad/research/tmux-xterm.md`, `claude-options.md`, `codex-options.md`,
   `opencode-options.md` — verified facts about the substrate and the three CLIs. Facts tagged
   `[V]` / `✅` / `[HELP]` / `[TEST]` are verified on this machine; `[A]` / `❓` / `[MEM]` are not.
   **Never build a load-bearing mechanism on an unverified fact without verifying it first.**
3. This document.

Repo: `/root/SocratesAgent`, module `github.com/saschazesiger/SocratesAgent`, Go 1.25 (toolchain
1.26 present). Everything shipped — code, comments, UI copy, docs, commit messages — is in
**English**.

## Contents

- [0. What Socrates becomes](#0-what-socrates-becomes)
- [A. Architecture and process model](#a-architecture-and-process-model) — processes, the verified OSC/window-style experiment, the tmux server, create/attach/adopt/delete, Socrates-owned sizing, supervision
- [B. Data model](#b-data-model) — the `sessions` name clash, the clean-cut migration, schema, kv keys, the journal
- [C. Harness launchers](#c-harness-launchers) — the white-background strategy per CLI, working directories, argv/env/config per harness, session-id discovery, resume, the full admin option catalogue
- [D. Transport protocol](#d-transport-protocol) — frames, sequence numbers, PTY reuse and takeover, the ring-pulling writer, exactly-once input and its bound, auth
- [E. Frontend](#e-frontend) — files, vendored xterm.js, page structure, the terminal theme, the key bar, service worker, design rules as acceptance criteria
- [F. Admin](#f-admin) — tmux status and installer, workspace, terminal behaviour, per-harness option groups, voice, tunnel, diagnostics, password
- [G. Deletion and keep lists](#g-deletion-and-keep-lists)
- [H. Testing](#h-testing) — Go tests against real tmux, the PTY fake CLIs, 20 e2e scenarios, CI
- [I. Work packages](#i-work-packages) — WP1…WP10 (WP9 split into 9a/9b) with scope, acceptance and dependencies
- [J. Risks and mitigations](#j-risks-and-mitigations)
- [Appendix A: reviewer notes not adopted](#appendix-a-reviewer-notes-not-adopted)
- [Appendix B: quick reference](#appendix-b-quick-reference-for-an-implementer)

---

## 0. What Socrates becomes

Socrates stops being a web chat over normalised agent protocols. It becomes a **web terminal
harness**: a browser front-end for durable interactive terminal sessions, each running one of

| harness id | label | program |
|---|---|---|
| `shell` | Shell | the user's login shell (`$SHELL`, else `/bin/bash`, else `/bin/sh`) |
| `claude` | Claude Code | `claude` |
| `codex` | Codex | `codex` |
| `opencode` | OpenCode | `opencode` |

Every session is a **tmux session** in a Socrates-owned tmux server. Socrates may be restarted,
upgraded or crash; the session keeps running. The machine may reboot; the session is recreated by
resuming the CLI's own session id in the same directory.

The three hard requirements that shape everything:

- **Durability** — nothing the user is running dies because Socrates or the network did.
- **Mobile resilience** — the app is used from a phone in a moving car. A lost connection is
  always visible, never faked; typed input is delivered exactly once; the app shell loads offline.
- **White** — every surface is pure white, including the terminal. The three CLIs all default to
  dark palettes and must be forced light by verified means (§C.1).

---

# A. Architecture and process model

## A.1 Processes

```
                          browser (one or more viewers per session)
                             │  WebSocket /api/sessions/{id}/ws
                             ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ socrates (one process)                                                   │
│   net/http server ── internal/server                                     │
│   session manager ── internal/termux   (tmux + PTY ownership)            │
│   store          ── internal/store     (SQLite)                          │
│                                                                          │
│   per live session:                                                      │
│     N viewer PTYs  : `tmux attach -t soc_<id>`  (one per WebSocket)      │
│     0 when nobody is looking — the session still runs                    │
└──────────────────────────────────────────────────────────────────────────┘
                             │ PTY (ptmx)
                             ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ tmux server  -S <data>/tmux.sock  -f <data>/tmux.conf                    │
│   session soc_<id> ── window 0 ── pane 0 ── the CLI, exec'd directly     │
│   pipe-pane -t soc_<id> '<socrates> journal-sink …'                      │
└──────────────────────────────────────────────────────────────────────────┘
                             │
                             └── started under `systemd-run --scope` where
                                 available, so a service restart does not
                                 kill it (§A.12)
```

**Decision: per-viewer `tmux attach` inside a Go-owned PTY.** Not control mode (`-CC`), not one
PTY fanned out to many viewers. Justification, from `tmux-xterm.md` §2, all verified:

- E1 [V]: attaching produces a complete, correctly-sized repaint (769 bytes for a 100×30 client)
  within ~700 ms, including the body of an alternate-screen TUI. DECISIONS.md's "on (re)attach the
  browser must show the correct current screen immediately (tmux redraw), not a replay of
  megabytes" is therefore satisfied **by construction**, with no `capture-pane` seeding.
- E2 [V]: two simultaneous clients at 100×30 and 60×20 each receive a stream sized for their own
  terminal. tmux does the per-client padding. A single fanned-out PTY would force one size on all
  viewers and would require a full VT emulator in Go to answer "what does the screen look like
  now" for a joining viewer.
- Control mode was rejected empirically: on attach it delivers **zero screen content** [V], its
  `%output` is octal-escaped line-oriented text (4 ASCII bytes per control byte — a 2–4× bandwidth
  tax on a TUI redrawing at 30 fps, on a mobile link), and it is tagged per pane so we would have
  to reimplement layout composition. Control mode is built for iTerm2, which is a terminal
  emulator; xterm.js is the terminal emulator here, and it wants a plain byte stream.

**There is no server-owned "anchor" client, no rendezvous, and no generated launch script.** The
CLI is the `new-session` command, exec'd directly with its argv passed as separate arguments. The
white background is a property of the tmux **window style**, not of an attached client — see A.2,
which is the experiment that makes this possible.

## A.2 Verified experiment: the white background needs no client at all

Run on 2026-09-02 in `scratchpad/osclab/` (tmux 3.6, isolated `-S` socket, no CLI sessions, no
paid API calls). A Python program inside the pane put its tty in raw mode, wrote a query and read
the reply with a timeout.

| # | case | result |
|---|---|---|
| 1 | plain conf, a client attached that answers `OSC 11` white | pane receives `\x1b]11;rgb:ffff/ffff/ffff\x1b\\` **[V]** |
| 2 | plain conf, a client attached that stays silent | pane receives **nothing** **[V]** |
| 3 | plain conf, **no client attached** | pane receives **nothing** **[V]** |
| 4 | `set -g window-style 'fg=#17181b,bg=#ffffff'`, **no client attached** | pane receives `]11;rgb:ffff/ffff/ffff` and `]10;rgb:1717/1818/1b1b` **[V]** |
| 5 | same style, **plus** an attached client that answers **black** | pane still receives **white** — the style wins, the client's answer is irrelevant **[V]** |
| 6 | `ESC [ ? 996 n` (theme-mode report) with the style set | pane receives `\x1b[?997;2n`, i.e. **light** **[V]** |
| 7 | `ESC [ c` (DA1) | answered by **tmux itself** (`\x1b[?1;2;4c`), never proxied **[V]** |

Every row was reproduced on **tmux 3.3a** (Debian bookworm, the Docker base image) as well as on
3.6, with one difference: 3.3a does **not** answer `ESC[?996n`. Nothing depends on that — Codex and
OpenCode both read the OSC 11 reply, which 3.3a answers correctly (§C.1.1).

Rows 4–6 are the decisive ones. **With an explicit `window-style` background, tmux answers the
colour and theme-mode queries itself, deterministically, with zero clients attached, and its
answer beats any attached client's.** Codex and OpenCode both decide light-vs-dark from the OSC 11
reply, so this makes their palette a property of our generated config rather than of who happens
to be looking. It removes, in one stroke: a process per live session, a start-up rendezvous, a
generated shell script and its quoting risk, and the whole class of "the CLI started before
anybody was attached, so it went dark" bugs.

Consequence for the viewer stream: because the window has an explicit style, tmux emits
`38;2;…`/`48;2;255;255;255` SGR for default cells. That is invisible on a white xterm.js and, if
anything, more deterministic than relying on the terminal's own default.

The OSC responder in `internal/termux/osc.go` survives, but with a smaller job: it runs on
**viewer** PTYs only, and it exists to intercept the queries **tmux sends to a client on attach**
(`OSC 10;?`, `OSC 11;?`, `?996n`) so that xterm.js never sees them and no answer has to make a
mobile round trip. It is a latency and determinism measure, not the mechanism. See §C.1.

## A.3 The tmux server

Socket: **`<data>/tmux.sock`** via `-S`, never `-L`. `-L name` resolves to `/tmp/tmux-$UID/name`,
which the system tmp cleaner may remove, differs per uid, and collides with a user's own
`-L socrates`. `<data>` is the existing data directory (`~/.socrates` by default, `-data` flag),
mode `0700`; tmux creates the socket itself with mode 0600 [V].

Config: **`<data>/tmux.conf`, generated by Socrates on every start**, passed with `-f` on the
command that starts the server (the first `new-session`). This guarantees `~/.tmux.conf` and
`~/.config/tmux/tmux.conf` never load [V]. Subsequent commands on the same socket do not need
`-f`. Also pass `-u` (force UTF-8) and set `TMUX=""` in the child env so a nested tmux does not
refuse.

Generated file — `internal/termux/conf.go`, function `WriteConf(path string, o ConfOptions) error`.
Note the **scopes**; getting them wrong is the usual reason a tmux conf silently does nothing [V].

```tmux
# Generated by Socrates. Do not edit; it is rewritten on every start.
set -g  status off                       # session option
set -sg escape-time 0                    # SERVER option
set -g  default-terminal "{{.DefaultTerminal}}"   # SERVER option (-s), read with `show -s`
set -g  history-limit {{.HistoryLimit}}  # session option, default 20000
set -g  mouse {{.Mouse}}                 # session option, default on
setw -g aggressive-resize on             # WINDOW option
set -g  remain-on-exit on                # WINDOW option
set -g  destroy-unattached off           # session option
set -g  exit-empty off                   # SERVER option
set -g  allow-passthrough on             # required by Claude Code's docs for notifications
set -g  set-titles off
set -g  focus-events on
set -s  extended-keys {{.ExtendedKeys}}  # default off; see §F.3
set -as terminal-features 'xterm*:extkeys'
set -g  remain-on-exit-format ''         # we render the exit overlay ourselves (§A.9)
set -g  window-style        'fg=#17181b,bg=#ffffff'   # THE white-background mechanism (§A.2)
set -g  window-active-style 'fg=#17181b,bg=#ffffff'
```

### `window-size` is a per-window option here, never a global one

**A global `window-size manual` segfaults the tmux server on the next `new-session`** — in a conf
file *or* set live on a running server, on tmux 3.6 and on 3.3a. Verified both ways [V]:

- conf file containing `set -g window-size manual` → the first `new-session` reports
  `server exited unexpectedly`, no server survives, `dmesg` shows
  `tmux: server[…]: segfault at 208`;
- global left alone at start, then `tmux set -g window-size manual` on the running server (which
  succeeds) → the **next** `new-session` takes the server down, and with it every session on it.

The second form is the dangerous one, because the first session created after the option is set
works fine. A design that sets the global live would ship, pass a one-session test, and then lose
every running session the moment a user created a second one.

**Therefore the global `window-size` is never touched at all.** tmux's built-in default (`latest`)
stays where it is, and the policy is applied **per window, immediately after each session is
created**:

```
tmux -S sock setw -t soc_<id> window-size manual
tmux -S sock resize-window -t soc_<id> -x <cols> -y <rows>
```

Verified on 3.6 and 3.3a [V]: three sessions created in a row are all alive,
`show -w -t soc_<id> window-size` reports `manual` for each, `show -gw window-size` still reports
`latest`, `resize-window` is obeyed, and typing on an attached client does not move the window.
`Adopt` re-applies the per-window option to every adopted session; `setw` is idempotent.

The same is true of the other policies: `latest` and `largest` are set with `setw -t soc_<id>` too.
No code path anywhere writes a global `window-size`, and a grep for `set -g window-size` in the
tree must find nothing.

**Start-up guard.** If the first `new-session` fails with `server exited unexpectedly`, Socrates
logs the generated conf verbatim, rewrites it with a minimal fallback (`status off`,
`default-terminal`, `remain-on-exit on`, the two `window-style` lines) and retries **once**. If
that also fails, session creation is reported as unavailable with the conf in the error. A bad
generated option can never permanently prevent sessions from being created.

`DefaultTerminal` is `tmux-256color` when `infocmp tmux-256color` succeeds, else
`screen-256color`. Probe once at start-up and cache; a slim Debian image without `ncurses-term`
has no `tmux-256color` entry.

### Hooks are global and set once

```
tmux -S sock set-hook -g pane-died \
  "run-shell -b <Q><socratesBin> tmux-hook --sock <sock> --event pane-died --session #{session_name} --status #{pane_dead_status}<Q>"
tmux -S sock set-hook -g session-closed \
  "run-shell -b <Q><socratesBin> tmux-hook --sock <sock> --event session-closed --session #{hook_session_name}<Q>"
```

(`<Q>` is a single quote produced by `termux.ShellQuote`, §A.11.)

Set **once, globally, at server start and again in `Adopt`** — not per session. Verified [V]:

- A **global** hook fires for every session and the format variables identify which:
  `pane-died` → `DIED /g2 status=5`, `session-closed` → `CLOSED g1`.
- `#{hook_session_name}` is populated for `session-closed` (the session is already gone by then,
  so `#{session_name}` is empty there) and **empty** for `pane-died` (use `#{session_name}`).
- A **session-scoped** `session-closed` hook (`set-hook -t <sess> session-closed`) **never fires**;
  a session-scoped `pane-died` does. `set-hook -g -t <sess>` sets the *global* hook and ignores
  `-t`, so each call replaces the previous one. Per-session hooks are therefore both unnecessary
  and a trap.

Hook delivery is best-effort: the manager also polls (§A.9), so a lost hook costs latency, never
correctness.

`internal/termux` never shells out to `tmux` without `-S <sock>`. There is exactly one helper:

```go
// internal/termux/tmux.go
type Tmux struct{ Sock, Conf, Bin string }
func (t *Tmux) Run(ctx context.Context, args ...string) (stdout string, err error)
func (t *Tmux) RunStart(ctx context.Context, args ...string) (string, error) // adds -f Conf -u
```

`Run` prefixes `-S t.Sock`; `RunStart` prefixes `-f t.Conf -S t.Sock -u` and is used only for the
command that may start the server. Every `exec.Cmd` gets `Env` with `TMUX` blanked and
`cmd.Stdin = nil`.

## A.4 Session naming

tmux session name: **`soc_<id>`** where `<id>` is the Socrates session id — a lowercase hex id from
`uuid.NewString()` with dashes removed, 32 chars. tmux session names may not contain `.` or `:`;
hex is safe. The mapping is stored (`sessions.tmux_name`) rather than recomputed, so a future id
format change cannot orphan a running session.

## A.5 Creating a session

`internal/termux.Manager.Create(ctx, spec Spec) (*Session, error)`:

1. Resolve the working directory (§C.2), creating it for `dynamic` mode.
2. Build the launch plan for the harness (§C): argv, env, generated config files, and the CLI
   session id if it can be pre-set.
3. **Persist the store row first**, with `state='starting'` and `tmux_name='soc_<id>'`, *before*
   any tmux command runs. A crash between the tmux session appearing and the row existing would
   otherwise produce a running session that Socrates does not know about; the row-first order makes
   that window empty, and `Adopt` adopts anything that still slips through (§A.8).
4. Write `<data>/sessions/<id>/`:
   - `plan.json` (mode 0600) — the resolved `LaunchPlan`: argv, env, cwd, harness, model, the
     options snapshot, and any generated secret (the OpenCode server password, §C.7). Diagnostic
     and audit; the store is authoritative. It is 0600 because it carries a secret.
   - `journal.raw` — created empty.
   - the harness's generated config files (`claude-settings.json`, `tui.json`, …).
5. Create the session, with the CLI's argv passed as **separate arguments** — there is no shell,
   no script and therefore no quoting question:
   ```
   tmux -f <conf> -S <sock> -u new-session -d -s soc_<id> \
        -x <cols> -y <rows> -c <cwd> -e K=V … -- <argv[0]> <argv[1]> …
   ```
   - `-e K=V` is verified to set the session environment, including values containing spaces,
     quotes and apostrophes, verbatim [V]. Use it for every env var in the plan; do **not** rely on
     inheriting Socrates' own environment for anything the plan names.
   - Initial size: the optional `cols`/`rows` in the `POST /api/sessions` body, else 120×40.
     Creation is a REST call, not a WebSocket, so there is no viewer to ask; the sheet sends the
     size it is about to open the terminal at, and the first attach corrects it anyway.
6. Apply the sizing policy (§A.7) **per window** — never globally (§A.3):
   ```
   tmux -S sock setw -t soc_<id> window-size manual
   tmux -S sock resize-window -t soc_<id> -x <cols> -y <rows>
   ```
7. Start the journal sink (§B.6):
   ```
   tmux -S sock pipe-pane -t soc_<id> <Q><socratesBin> journal-sink --path <journal> --max-bytes 67108864 --keep 1<Q>
   ```
   **Without `-o`.** Verified [V]: `-o` is a *toggle*, not a "toggle-on" — calling it once, twice
   and three times gives `#{pane_pipe}` = 1, **0**, 1, so a retried create would silently close the
   journal. Without `-o`, a command always (re)opens the pipe; three calls give 1, 1, 1 [V].
8. Set `state='running'`.

**If any tmux command in steps 5–7 fails**, the row already exists (step 3), so the failure has a
home: set `state='failed'` with tmux's stderr verbatim in `fail_reason`. The session appears in the
list with the `failed` overlay (§E.7) and a **Try again** button, rather than vanishing or leaving
a half-created row in `starting` forever.

There is no `wait-for` rendezvous and no anchor: by §A.2 the CLI's colour queries are answered by
the window style from the moment the pane exists.

Steps 5–8 are one function and are covered by Go tests against real tmux (§H.1).

## A.6 Attaching a viewer

Per WebSocket, `Manager.Attach(ctx, sessionID string, cols, rows int) (*Viewer, error)`:

- `pty.StartWithSize(exec.Command(tmuxBin, "-S", sock, "attach", "-t", "soc_"+id), &pty.Winsize{...})`
- Read its client tty from `list-clients -t soc_<id> -F '#{client_tty} #{client_width}x#{client_height}'`
  and match against `ptsname(master)`. `client_tty` equals the slave name [V] — that is how we
  address our own client precisely.
- Output: read the master, run it through the OSC responder filter (§C.1.1), frame it, send.
- Input: write bytes to the master.
- Resize: set the PTY's winsize with `pty.Setsize` **and** move the window with
  `tmux resize-window` (§A.7). The PTY winsize keeps the tmux client honest about what it can
  paint; `resize-window` is what actually decides the window geometry under the `manual` policy.
- Detach: `tmux -S sock detach-client -t <client_tty>`, then `cmd.Wait()`, then close the master.
  `detach-client` makes the client exit **0**; closing the master alone makes it exit **1**, though
  the session survives either way [V].

## A.7 Multi-viewer sizing — Socrates owns the size

**Decision: `window-size manual`, with Socrates issuing every `resize-window`.**

The obvious choice, `latest`, does not mean what its name suggests. Verified [V]: with a laptop
client at 100×30 and a phone client at 60×20 both attached, the window follows **whichever client
last typed**:

| event | window |
|---|---|
| A attaches at 100×30, then B at 60×20 | 60×20 |
| a keystroke on A | **100×30** |
| a keystroke on B | **60×20** |
| a keystroke on A | **100×30** |

Under `latest` the window therefore flips on every alternation between two devices, the TUI
re-lays out each time, and any "another viewer resized this session" notice would fire on
essentially every keystroke. `latest` also makes the size follow whichever client last attached,
so a transient connection changes the geometry for everyone.

Under `manual` — set **per window** with `setw -t soc_<id>`, never globally (§A.3) — the window
follows `resize-window` **only**, verified on 3.6 and 3.3a [V]: `resize-window -x 90 -y 28` gives
90×28, it stays 90×28 after `send-keys`, and a later `resize-window -x 60 -y 20` is obeyed. That
gives Socrates a policy it can state and test:

> **The window is sized to the most recently *connected or explicitly resized* viewer.** Typing
> never changes it.

`Manager` keeps, per session, `sizeOwner viewerID` and `cols,rows`. It calls `resize-window` when:

- a viewer attaches (it becomes the owner);
- the owning viewer reports a `resize` control frame (window follows it);
- a non-owning viewer reports a `resize` **and** becomes the owner (the user rotated their phone or
  opened the keyboard — an explicit act, so it takes ownership);
- the owning viewer's **socket** is lost, in which case ownership passes immediately to the
  remaining viewer that most recently attached or resized, and the window is resized to its size.
  Ownership moves on socket loss, **not** on expiry of the 90 s PTY grace: a phone that drops out
  of coverage must not pin a laptop's window to 60×20 for a minute and a half. The dropped viewer's
  PTY stays in its grace and simply is not the owner any more; if it reconnects, it re-takes
  ownership like any attaching viewer. With no viewers left, the window keeps its size.

Because Socrates issues every resize, it knows the size at all times. **There is no size poll.**
The `size` control frame is sent to all viewers exactly when the manager changes the size, carrying
`by: "self" | "other"`, and the notice in §E.7 therefore fires once per real change.

Admin may choose `latest` or `largest` instead (§F.3) — also set per window with `setw -t` — each
with a hint stating what it does:
`latest` follows typing; `largest` never truncates but makes a small viewer pan over the window
(`#{window_bigger}`, `#{window_offset_x/y}`) [V]. Under those policies the manager stops issuing
`resize-window`, restores the 2 s size poll for the notice, and the hint says the notice may be
chatty. `manual` is the default and the only policy with a test that pins its semantics.

## A.8 Server restart — re-adoption

Socrates holds no state that a restart may lose. On start-up, `Manager.Adopt(ctx)`:

1. `tmux -S sock list-sessions -F '#{session_name}|#{session_created}|#{session_attached}|#{window_width}x#{window_height}'`
   — if the socket does not exist or the command fails with "no server running", there are no live
   sessions; that is not an error.
2. Re-apply the two global hooks (§A.3). The `window-size` policy is **not** a global setting and
   is re-applied per adopted window in step 4 with `setw -t soc_<id>`.
3. `tmux -S sock list-panes -a -F '#{session_name}|#{pane_dead}|#{pane_dead_status}|#{pane_pipe}|#{pane_current_path}'`
   [V — every one of these formats verified].
4. For every store row with `state IN ('running','starting')`:
   - tmux session present, pane alive → adopt: re-apply `setw -t soc_<id> window-size <policy>`
     (idempotent), re-establish `pipe-pane` **only if `#{pane_pipe}` is 0** (a `pipe-pane` without
     `-o` replaces a running sink, so re-issuing it needlessly would restart the journal writer),
     set `state='running'`.
   - tmux session present, pane dead → `state='exited'`, `exit_status=<pane_dead_status>`.
   - tmux session **absent** → the reboot case: `state='needs_resume'`. Nothing is restarted
     eagerly; the CLI is relaunched when the user opens the session (§C.8). This is deliberate: a
     reboot with 40 stored sessions must not start 40 CLIs.
5. **Every `soc_*` tmux session with no store row is adopted, never killed.** DECISIONS.md is
   explicit: *"Socrates never kills a tmux session unless the user explicitly deletes the Socrates
   session."* A restored or replaced `socrates.db`, a failed migration, or a crash in the tiny
   window of §A.5 step 3 must not destroy running work. Socrates inserts a row:
   `harness='shell'`, `title='Recovered session'`, `state='running'`,
   `workdir=<#{pane_current_path}>`, `workdir_mode='custom'`, `cli_session_state='none'`,
   `tmux_name` from the listing. The start-up log names each recovered session and the Diagnostics
   panel lists them, so the user can see what happened and delete them deliberately.

`Adopt` runs synchronously before the HTTP listener accepts, and completes in well under a second
for a realistic number of sessions.

## A.9 Crash and exit surfacing

Three independent detectors, by design:

1. **The global `pane-died` hook** → `socrates tmux-hook --event pane-died --session soc_<id>
   --status N`. The subcommand connects to `<data>/hook.sock` (a unix socket the running server
   listens on, mode 0600) and writes one JSON line. The server marks the row `state='exited'`,
   `exit_status=N`, and pushes an `exit` control frame to every viewer. Verified that the hook
   fires with `#{pane_dead_status}` correctly populated [V]. A forged hook is harmless: the socket
   is 0600 and the poll below is the authority.
2. **Poll**, every 2 s, one `list-panes -a -F` call for *all* sessions. Catches a hook that never
   arrived (e.g. Socrates was down when it fired).
3. **The viewer PTY reading EOF** — reconciled by re-running `Adopt` for that one session.

**Declaring the reboot case requires evidence, not one failed command.** A session is moved to
`needs_resume` only when either the socket file is absent, or `list-panes` has failed (or reported
the session missing) on **two consecutive polls 2 s apart**. A momentary failure — the server busy,
the socket being recreated — must not flip every session to `needs_resume` and trigger a wave of
resumes.

With `remain-on-exit on`, a dead pane keeps the session in `list-sessions` with `#{pane_dead}=1`
and `#{pane_dead_status}` set [V]. We set `remain-on-exit-format ''` because tmux's own
"Pane is dead (status 7, …)" line otherwise **replaces the pane content** for any client that
attaches afterwards — verified: with the default format a newly attached client sees only that
line; with `''` it sees the last screen [V]. The exit status is rendered by our own overlay
(§E.7) over the screen the user was actually looking at.

## A.10 Deleting a session

Only on explicit user delete (`DELETE /api/sessions/{id}`):

1. Detach and close every viewer PTY.
2. `tmux -S sock pipe-pane -t soc_<id>` (a bare `pipe-pane` with no command closes it), then
   `tmux -S sock kill-session -t soc_<id>`.
3. Delete the store row.
4. `os.RemoveAll(<data>/sessions/<id>)` — the journal goes with it.
5. The working directory is **not** deleted, even in `dynamic` mode. Work the user did is not
   Socrates' to throw away. The API response says so; the UI says so in the confirm dialog.

Archiving (`POST /api/sessions/{id}/archive`) sets `archived_at` and does **not** touch tmux: an
archived session keeps running. Unarchive clears it.

## A.11 Package layout

```
internal/termux/          tmux + PTY ownership. Knows nothing about HTTP or the store.
  tmux.go                 Tmux runner, list/parse helpers
  conf.go                 WriteConf, ConfOptions, terminfo probe, the start-up guard
  manager.go              Manager: Create, Attach, Adopt, Ensure, Kill, Resize, Poll
  session.go              Session, Viewer, state machine, sizeOwner
  pty_unix.go             PTY start/resize/close  (build tag !windows)
  pty_windows.go          stubs returning termux.ErrUnsupported
  journal.go              pipe-pane control; the `journal-sink` subcommand's implementation
  osc.go                  OSC 10/11/996 responder filter for viewer PTYs (§C.1.1)
  ring.go                 the per-viewer replay ring (§D.4)
  shellquote.go           ShellQuote, for the two strings that do go through a shell
  supervise.go            systemd-run --scope detection and wrapping (§A.12)
  install.go              tmux detection + auto-install (§F.1)
internal/harnesses/       one file per harness: argv/env/config generation, id discovery, resume
  plan.go                 LaunchPlan, GenFile, PlanRequest
  harness.go              Harness interface, registry, Options schema types
  shell.go  claude.go  codex.go  opencode.go
internal/server/          HTTP + WebSocket
  sessions.go  ws.go  harnesses.go  admin.go  auth.go  voice.go  tunnel.go  server.go
internal/store/           SQLite
internal/web/             embedded static assets
deploy/socrates.service   the systemd unit (§A.12)
```

`internal/termux` imports `internal/harnesses` for `LaunchPlan` only, never the reverse.

### Shell quoting

Passing the CLI's argv to `new-session` as separate arguments removes the biggest quoting surface
— and it is a real one: argv legitimately contains
`-c projects."/a b/c.d".trust_level="trusted"`, free-text `--append-system-prompt` values and
admin-supplied `extra_args`. Two strings still go through `/bin/sh` because tmux runs them that
way: the `pipe-pane` command and the two `run-shell` hook bodies. Both embed
`os.Executable()` and `<data>`, which are user-chosen paths.

```go
// internal/termux/shellquote.go
// ShellQuote wraps s in single quotes, rendering any embedded single quote as
// '\'' — the only form that is safe in every POSIX shell.
func ShellQuote(s string) string
```

Every path interpolated into a `pipe-pane` or `run-shell` string goes through it.
`TestShellQuoteRoundTrip` feeds `'`, a space, `$`, a backtick and a newline through
`sh -c` and asserts the value comes back byte-identical.

## A.12 Supervision — a Socrates restart must not be a reboot

The tmux server daemonizes out of Socrates' process tree, but it stays in Socrates' **cgroup**.
Under systemd's default `KillMode=control-group`, `systemctl restart socrates` kills the tmux
server too — which would turn every ordinary restart into the reboot path and quietly defeat the
whole design. Nothing in the current repo says otherwise.

**(a) systemd.** Ship `deploy/socrates.service`:

```ini
[Unit]
Description=Socrates
After=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/socrates -addr 127.0.0.1:8080
Restart=on-failure
# The tmux server is deliberately outside this unit's lifecycle: sessions
# survive a restart of Socrates itself.
KillMode=process
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target
```

and, belt and braces, start the tmux server outside the service cgroup when possible:
`internal/termux/supervise.go` detects `systemd-run` on `PATH` **and** that Socrates is running
under systemd (`$INVOCATION_ID` is set, or `/run/systemd/system` exists), and then prefixes the
server-starting command with

```
systemd-run --user --scope --quiet --unit socrates-tmux-<pid> --collect -- tmux -f … -S … new-session …
```

falling back to the system bus when not running as a user unit, and to a plain exec when
`systemd-run` fails for any reason. A failure here is logged and never fatal: the worst case is
the behaviour we have today.

**(b) Docker.** The container's lifecycle *is* the machine's: a container restart is a reboot and
takes the resume path. The README says so plainly. The Dockerfile gains **`tini` as the
entrypoint** (equivalently, the image is documented to need `docker run --init`) so a reparented
tmux process cannot become a zombie when Socrates is PID 1.

**Socrates must not install its own `SIGCHLD` reaper.** A Go-side reaper at PID 1 races
`os/exec`'s `Wait` on Socrates' *own* children — every `tmux attach` viewer, the installer, every
discovery subprocess — and turns their exits into `ECHILD` errors. `tini` reaps only what it should
and is the whole answer.

**(c) Test.** `TestSessionSurvivesManagerKill`: create a session, `SIGKILL` the process that owns
the Manager, assert from a new process that the tmux session and its pane are still alive and that
`Adopt` re-adopts them. Verified as plausible by experiment: closing a viewer's PTY master abruptly
leaves the client exiting 1 and the session alive [V].

The test's comment must say what it does **not** prove: it demonstrates survival of a *process*
kill, which is the crash and the `SIGTERM` case. It says nothing about a **cgroup** kill, which is
what `systemctl restart` does by default and what `KillMode=process` plus `systemd-run --scope`
exist to prevent. That path is verified by hand on a machine with systemd, not by `go test`.

---

# B. Data model

## B.1 The name clash, and how it is resolved

The current `store` has a table called `sessions` that holds **login cookies**. The new world has
terminal sessions and needs that noun. **Decision: rename the auth table to `logins`.** All auth
code moves with it: `store.CreateSession/ValidSession/DeleteSession/DeleteAllSessions/
PurgeExpiredSessions` become `store.CreateLogin/ValidLogin/DeleteLogin/DeleteAllLogins/
PurgeExpiredLogins`. The cookie name stays `socrates_session` — it is a cookie, not a table, and
changing it would sign every user out for no gain.

## B.2 Migration strategy — clean cut

DECISIONS.md says clean cut, no legacy. Concretely, in `store.Open`:

```go
const schemaVersionKey = "schema_version"
const schemaVersion    = 3   // 3 == the terminal-harness schema
```

`migrate(db)`:

1. Read `PRAGMA user_version`. If it is already 3, done.
2. If the database contains a `chats` table (i.e. it is a pre-rewrite database), run the **cut**
   inside one transaction:
   ```sql
   DROP TABLE IF EXISTS steps;
   DROP TABLE IF EXISTS runs;
   DROP TABLE IF EXISTS messages;
   DROP TABLE IF EXISTS chats;
   ALTER TABLE sessions RENAME TO logins;   -- if `sessions` exists with a `token` column
   ```
   **Old chats are dropped, not converted.** A converted chat would be an archived row that can
   never be opened: its transcript lived in tables we are deleting, its agent-host is gone, and
   its tmux session never existed. An empty list is more honest than a list of things that do
   nothing. The `kv` table survives, so the password, tunnel and voice settings survive; the
   `agents` sub-document of the settings JSON is replaced by `harnesses` in place (§B.5).
3. Create the new schema.
4. `PRAGMA user_version = 3`.

A backup is written first: `<data>/socrates.db.pre-v3.bak` (a plain file copy while no
connection is open, or `VACUUM INTO`). Log its path. This costs nothing and makes the cut
reversible by hand.

## B.3 Schema

```sql
CREATE TABLE IF NOT EXISTS kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS logins (
  token      TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id             TEXT PRIMARY KEY,   -- 32 hex chars, uuid without dashes
  client_id      TEXT NOT NULL DEFAULT '',  -- idempotency key from the browser
  title          TEXT NOT NULL DEFAULT '',
  harness        TEXT NOT NULL,      -- 'shell' | 'claude' | 'codex' | 'opencode'
  model          TEXT NOT NULL DEFAULT '',
  effort         TEXT NOT NULL DEFAULT '',
  workdir        TEXT NOT NULL,      -- absolute path, always set
  workdir_mode   TEXT NOT NULL,      -- 'dynamic' | 'preset' | 'custom'
  options        TEXT NOT NULL DEFAULT '{}',  -- JSON: the resolved per-harness options snapshot
  tmux_name      TEXT NOT NULL DEFAULT '',    -- 'soc_<id>'
  cli_session_id TEXT NOT NULL DEFAULT '',    -- Claude uuid | Codex uuidv7 | OpenCode ses_…
  cli_session_state TEXT NOT NULL DEFAULT 'none',
      -- 'none'      nothing to resume (shell, or not discovered yet)
      -- 'pending'   we are watching for it (codex/opencode after launch)
      -- 'known'     discovered/pre-set, not yet proven resumable
      -- 'verified'  checked against the CLI's own store since the last launch
      -- 'lost'      it was known and is provably gone; resume must not be attempted
  state          TEXT NOT NULL DEFAULT 'starting',
      -- 'starting' | 'running' | 'exited' | 'needs_resume' | 'failed'
  exit_status    INTEGER NOT NULL DEFAULT -1,  -- -1 = unknown/not exited
  fail_reason    TEXT NOT NULL DEFAULT '',
  resumed        INTEGER NOT NULL DEFAULT 0,   -- 1 while the "resumed after restart" notice is unread
  resume_count   INTEGER NOT NULL DEFAULT 0,
  cols           INTEGER NOT NULL DEFAULT 120,
  rows           INTEGER NOT NULL DEFAULT 40,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  last_attached  INTEGER NOT NULL DEFAULT 0,
  archived_at    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_client ON sessions(client_id) WHERE client_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_tmux ON sessions(tmux_name) WHERE tmux_name <> '';
```

All timestamps are `time.Now().UnixMilli()`, as today.

`client_id` keeps the existing idempotency property: a create request retried over a bad mobile
link produces one session, not two. Reuse the current pattern from `CreateChat` — insert, and on
a unique-constraint violation return the existing row.

## B.4 Store API

`internal/store/sessions.go`:

```go
type Session struct {
    ID, ClientID, Title, Harness, Model, Effort   string
    Workdir, WorkdirMode                          string
    Options                                       json.RawMessage
    TmuxName, CLISessionID, CLISessionState       string
    State                                         string
    ExitStatus                                    int
    FailReason                                    string
    Resumed                                       bool
    ResumeCount, Cols, Rows                       int
    CreatedAt, UpdatedAt, LastAttached, ArchivedAt int64
}

func (s *Store) CreateSession(sess *Session) error          // idempotent on ClientID
func (s *Store) GetSession(id string) (*Session, error)
func (s *Store) SessionByClientID(cid string) (*Session, error)
func (s *Store) ListSessions(includeArchived bool) ([]Session, error)
func (s *Store) UpdateSessionTitle(id, title string) error
func (s *Store) SetSessionState(id, state string, exitStatus int, failReason string) error
func (s *Store) SetSessionCLI(id, cliID, cliState string) error
func (s *Store) SetSessionSize(id string, cols, rows int) error
func (s *Store) SetSessionArchived(id string, archived bool) error
func (s *Store) NoteAttach(id string) error                 // last_attached = now
func (s *Store) NoteResume(id string) error                 // resume_count++, resumed = 1
func (s *Store) ClearResumedFlag(id string) error
func (s *Store) DeleteSession(id string) error
```

Every write bumps `updated_at` and `Store.rev`. Keep the existing `s.mu` write serialisation.

## B.5 kv settings keys

Settings remain **one JSON document** in `kv` under the existing key, decoded into
`config.Settings`. The document gains and loses fields:

| kv key | shape | note |
|---|---|---|
| `settings` (existing) | `config.Settings` JSON | see below |
| `password_hash` (existing) | string | unchanged |
| `tmux_install_log` | JSON `{lines:[]string, exit:int, at:int64, ok:bool}` | last auto-install run, so the admin page can show it after a reload |
| `schema_version` | via `PRAGMA user_version` | not a kv key |

`config.Settings` after the rewrite:

```go
type Settings struct {
    OpenRouter OpenRouterSettings `json:"openrouter"` // kept verbatim (dictation + titles)
    Voice      VoiceSettings      `json:"voice"`      // kept verbatim (TTS stays configurable)
    Tunnel     TunnelSettings     `json:"tunnel"`     // kept verbatim
    Workspace  WorkspaceSettings  `json:"workspace"`  // new
    Terminal   TerminalSettings   `json:"terminal"`   // new
    Harnesses  HarnessSettings    `json:"harnesses"`  // new; replaces Agents
}

type WorkspaceSettings struct {
    Root           string        `json:"root"`             // default <data>/workspaces
    DefaultHarness string        `json:"default_harness"`  // default "claude"
    Presets        []PresetDir   `json:"presets"`          // admin-defined quick picks
    AllowCustom    bool          `json:"allow_custom"`     // default true
}
type PresetDir struct {
    Label string `json:"label"`
    Path  string `json:"path"`
}

type TerminalSettings struct {
    WindowSize   string `json:"window_size"`    // "manual" (default) | "latest" | "largest"
    HistoryLimit int    `json:"history_limit"`  // default 20000
    Mouse        bool   `json:"mouse"`          // default true
    ExtendedKeys bool   `json:"extended_keys"`  // default false
    Scrollback   int    `json:"scrollback"`     // xterm.js scrollback, default 2000
    FontSize     int    `json:"font_size"`      // default 14
    WebGL        bool   `json:"webgl"`          // default true, with DOM fallback
}

type HarnessSettings struct {
    Shell    ShellOptions    `json:"shell"`
    Claude   ClaudeOptions   `json:"claude"`
    Codex    CodexOptions    `json:"codex"`
    OpenCode OpenCodeOptions `json:"opencode"`
}
```

The per-harness option structs are specified field by field in §C.7–C.10, together with their
storage key, control type, validation and the flag/env/config they map to.

Common to all four harness option structs (embedded as `Common`):

```go
type Common struct {
    Enabled   bool     `json:"enabled"`    // default true
    Binary    string   `json:"binary"`     // "" = look up on PATH
    ExtraArgs []string `json:"extra_args"` // raw, appended verbatim, start-only
    ExtraEnv  []string `json:"extra_env"`  // raw "KEY=VALUE", start-only
    Models    []ModelPick `json:"models"`  // the short list offered in the new-session sheet
}
```

`config.ModelPick`, `config.KnownEfforts` and `config.NormalizeEffort` are kept as they are.

## B.6 The journal

`<data>/sessions/<id>/journal.raw` — the raw pane byte stream, produced by tmux's `pipe-pane`.

**What it is for, and what it is not for.** DECISIONS.md asks for a journal "for reconnect replay
and scrollback". In this design reconnect replay comes from the in-memory ring (§D.4) and, when the
ring cannot serve, from a fresh tmux attach — because attaching is verified to repaint the current
screen in one go (E1 [V]), which is exactly what "show the correct current screen immediately, not
a replay of megabytes" asks for. The journal therefore serves **scrollback, export and audit**.
The consequence is worth stating: after a Socrates restart the ring is gone, so the first reconnect
is a redraw rather than a replay. That still satisfies the requirement, and it is the reason the
journal never has to be read on the hot path.

**The sink is a Go program, not `cat`.** `pipe-pane 'cat >> journal.raw'` appends every pane byte
forever; a TUI that redraws continuously emits megabytes per hour, and DECISIONS' phone-in-a-car
user never restarts a session, so rotation "on session start" never happens. Instead:

```
tmux -S sock pipe-pane -t soc_<id> \
  '<socratesBin> journal-sink --path <journal> --max-bytes 67108864 --keep 1'
```

`socrates journal-sink` copies stdin to the file and, when it crosses `--max-bytes`, renames it to
`journal.1.raw` (keeping `--keep` old files) and continues into a fresh one without dropping a
byte. It rotates **while the session runs**, which is the only time that matters.

Two details that are easy to get wrong, both verified:

- The `pipe-pane` command is written **without `-o`**. `-o` is a *toggle*: calling it once, twice
  and three times leaves `#{pane_pipe}` at 1, **0**, 1 [V], so a retried create or a second `Adopt`
  would silently close the journal. Without `-o` a command always (re)opens the pipe: three calls
  give 1, 1, 1 [V].
- The command string goes through `/bin/sh`, so `<socratesBin>` and `<journal>` — both user-chosen
  paths — are wrapped with `termux.ShellQuote` (§A.11).

`internal/termux/journal.go`:

```go
func JournalPath(dataDir, id string) string
func PipeCommand(socratesBin, journal string, maxBytes int64, keep int) string // shell-quoted
func RunJournalSink(path string, maxBytes int64, keep int) error               // the subcommand
func TailJournal(dataDir, id string, maxBytes int64) ([]byte, error)
```

`GET /api/sessions/{id}/journal` streams the current file (and the rotated one before it) as
`application/octet-stream` with
`Content-Disposition: attachment; filename="socrates-<id>.raw"`, for the "download scrollback"
action in the session menu.

---

# C. Harness launchers

## C.0 The interface

```go
// internal/harnesses/harness.go
package harnesses

type Kind string
const (KindShell Kind = "shell"; KindClaude = "claude"; KindCodex = "codex"; KindOpenCode = "opencode")

// LaunchPlan is everything termux needs to start a pane. It is fully resolved:
// no settings lookups happen after this struct is built.
type LaunchPlan struct {
    Argv       []string          // argv[0] is an absolute path
    Env        map[string]string // applied with `tmux new-session -e K=V`
    Cwd        string
    Files      []GenFile         // config files written before launch
    CLISession string            // pre-set id, or "" when it must be discovered
    Discover   DiscoverMode      // None | PreSet | WatchCodexRollout | OpenCodeAPI
    Port       int               // OpenCode TUI HTTP port, 0 otherwise
}
type GenFile struct { Path string; Mode os.FileMode; Data []byte }

type Harness interface {
    Kind() Kind
    Label() string
    DefaultBinary() string
    VersionArgs() []string
    // Plan builds the argv/env/config for a fresh session.
    Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error)
    // ResumePlan builds the argv for continuing sess.CLISessionID. It returns
    // ErrNoResume when this harness cannot resume (shell), in which case the
    // caller uses Plan instead.
    ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error)
    // VerifyCLISession reports whether the stored id can still be resumed.
    // A false with a nil error means "gone"; an error means "could not tell".
    VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error)
    // DiscoverModels populates the model picker. May be nil.
    DiscoverModels(ctx context.Context, bin string) (Catalog, error)
}

type PlanRequest struct {
    SessionID  string
    Title      string
    Cwd        string
    Model      string
    Effort     string
    CLISession string
    Settings   config.Settings
    DataDir    string
}
```

`Registry()` returns the four in a fixed order: `shell, claude, codex, opencode`.

## C.1 White background — the verified strategy, per CLI

This is the single most-likely-to-be-fudged part of the spec, so the mechanism is stated once and
then applied per CLI.

**The mechanism is the tmux window style, and it needs nobody attached.** §A.2 verified that with
`set -g window-style 'fg=#17181b,bg=#ffffff'` in the generated conf, tmux answers a pane's
`OSC 11;?` with `rgb:ffff/ffff/ffff`, its `OSC 10;?` with the style's foreground, and its
`ESC[?996n` with `ESC[?997;2n` (light) — **with zero clients attached**, and its answer beats a
client that says something else [V]. Codex and OpenCode both decide light-vs-dark from the OSC 11
reply, so their palette is now a property of our configuration rather than of who is looking.
Claude Code does not use OSC 11 at all and needs `COLORFGBG` instead (§C.1.2).

On top of that, each CLI also gets a **pinned theme configuration**. Both are applied. If either
alone were reliable we would still apply both, because a wrong palette on a white page is
unreadable, not merely ugly.

### C.1.1 The OSC responder on viewer PTYs

`internal/termux/osc.go` still exists, but its job is narrow. When a client attaches, tmux sends
*the client* a batch of terminal queries — `OSC 10;?`, `OSC 11;?`, `ESC[?996n`, plus DA1/DA2,
XTVERSION and `ESC[18t`/`ESC[14t` [V]. The responder answers the colour and theme-mode ones on the
PTY and removes them from the stream, so xterm.js never sees them.

```go
// Responder scans a viewer PTY's output for the terminal colour queries tmux
// sends to a client, answers them, and returns the stream with them removed.
type Responder struct {
    Foreground [3]uint16 // {0x1717,0x1818,0x1b1b}
    Background [3]uint16 // {0xffff,0xffff,0xffff}
    W          io.Writer // the PTY master
}
// Filter is stateful across calls: a query may straddle a read boundary, so it
// carries at most 16 bytes over. It answers each query exactly once.
func (r *Responder) Filter(p []byte) []byte
```

| query | answer |
|---|---|
| `ESC ] 10 ; ? ST` | `ESC ] 10 ; rgb:1717/1818/1b1b ESC \` |
| `ESC ] 11 ; ? ST` | `ESC ] 11 ; rgb:ffff/ffff/ffff ESC \` |
| `ESC [ ? 996 n` | `ESC [ ? 997 ; 2 n` (2 = light) |

Both `ESC \` (ST) and `BEL` terminations are recognised. DA1, DA2, XTVERSION and the `18t`/`14t`
window-size reports are **passed through**: tmux answers DA1 itself [V] and xterm.js answers the
rest.

Why answer server-side rather than let xterm.js do it: determinism and latency. The browser's reply
would have to cross a mobile link, and although the *pane's* palette no longer depends on it, a
client that answers late or not at all makes tmux's own bookkeeping inconsistent between viewers.
Answering in Go is one place, zero round-trips, and the same answer for every viewer.

### C.1.2 Claude Code

Claude does **not** use OSC 11. Its detection is `COLORFGBG`, and the exact parser was read out of
the v2.1.258 binary [BIN]: take the last `;`-separated field, require an integer 0–15;
`0–6` and `8` ⇒ dark, `7` and `9–15` ⇒ light.

- **Detection:** `COLORFGBG=0;15` in the session env (via `tmux new-session -e`). Verified that
  `-e COLORFGBG='0;15'` reaches the session environment [V].
- **Pin:** there is **no `--theme` flag** in 2.1.258 [HELP] and `theme` is not a documented
  `settings.json` key. The theme is a global preference believed to live in `~/.claude.json` as
  `"theme": "light"` [MEM — *not verified*]. Therefore:
  1. Always set `COLORFGBG=0;15`.
  2. Additionally write `"theme": "light"` into `$CLAUDE_CONFIG_DIR/.claude.json` **only if** the
     file already contains a `theme` key or the implementer has verified the key by running
     `/theme` once and diffing the file. **WP4 must perform that verification and record the
     result in a comment in `claude.go`.** If it turns out the key lives elsewhere, drop this
     step and rely on `COLORFGBG` plus `minimumContrastRatio` (§E.4); do not guess.
  3. Never merge-write the whole of `~/.claude.json` — read, set one key, write atomically
     (temp file + rename in the same directory), and only when the user enabled
     "Pin the Claude Code theme to light" in admin (default **on**).
- Also set `CLAUDE_CODE_TMUX_TRUECOLOR=1`, and `TERM`/`COLORTERM` as in C.1.5.

### C.1.3 Codex

Codex queries OSC 10 then OSC 11 at start-up; the OSC 11 reply decides the palette ✅.

- **Detection:** the window style answers white, from tmux itself, with no client attached [V].
- **Pin:** `-c tui.theme="light-gray"` on the command line. Theme names are **not validated at
  config load** ✅ — a wrong name fails silently later. Names found in the binary:
  `light-gray`, `gruvbox-light`, `ocean-light`, `light-dark`, `gruvbox-dark`. Admin offers those
  as a dropdown with `light-gray` as the default and a free-text escape hatch. **WP10's `lighttheme` scenario must confirm by eye (a screenshot of a real Codex session) that the
  chosen name actually applies** — WP5 has no browser.
- There is no boolean "light mode" key; `tui.light_theme` is not a real key ✅.

### C.1.4 OpenCode

OpenCode queries OSC 10 **and** OSC 11 with `QUERY_TIMEOUT_MS = 250`; both replies are needed and
the background luminance decides. It does **not** consult `COLORFGBG` [V].

- **Detection:** the window style answers both, from tmux itself, with no client attached and no
  round trip — comfortably inside OpenCode's 250 ms window.
- **Pin:** write a `tui.json` and point `OPENCODE_TUI_CONFIG` at it:
  ```json
  { "$schema": "https://opencode.ai/tui.json",
    "theme": "github",
    "mouse": true,
    "attention": { "enabled": false } }
  ```
  written to `<data>/sessions/<id>/tui.json`. Light backgrounds are a per-theme **mode**, not a
  separate theme name [V] — every built-in theme carries `{dark,light}` pairs — so the theme name
  chooses the palette family and the OSC 11 answer chooses the mode. Admin offers the verified
  built-in list (`github`, `solarized`, `tokyonight`, `everforest`, `ayu`, `catppuccin`,
  `gruvbox`, `kanagawa`, `nord`, `one-dark`, `rosepine`, `flexoki`, `vercel`, `system`, …) with
  `github` as the default.
  The keybinds `theme_switch_mode` and `theme_mode_lock` exist and default to `none` [V]; the
  persisted `theme_mode` / `theme_mode_lock` state is **not** written by Socrates — it is the
  user's, and OSC 11 is the deterministic lever.

### C.1.5 Shared terminal env for all four

Applied via `tmux new-session -e` on every session:

```
TERM=<default-terminal from the conf: tmux-256color | screen-256color>
COLORTERM=truecolor
COLORFGBG=0;15
TMUX=                      (blank, so a nested tmux does not refuse)
SOCRATES_SESSION=<id>
SOCRATES=1
```

Note `TERM` inside a tmux pane is set by tmux from `default-terminal`; setting it via `-e` is
belt and braces: it is the value the CLI sees whether tmux set it or we did.

## C.2 Working directories

`workdir_mode` is one of:

| mode | behaviour |
|---|---|
| `dynamic` | **default.** Create `<workspace.root>/<harness>-<yyyymmdd-hhmmss>-<id[:8]>` with `os.MkdirAll(dir, 0o755)`. The name is human-readable in a shell prompt and unique. |
| `preset` | One of `workspace.presets`. The path must still exist at launch; if it does not, the create fails with `the preset directory <path> is gone`. |
| `custom` | A free-form absolute path from the new-session sheet. Only offered when `workspace.allow_custom`. Created if missing, with `os.MkdirAll(dir, 0o755)`, after a confirmation in the sheet ("Create <path>?"). |

Validation, in `harnesses.ResolveWorkdir(settings, mode, path, harness, id) (string, error)`:

- must be absolute after `filepath.Clean`;
- must not contain a `..` element after cleaning;
- for `dynamic`, must be inside `workspace.root` (a symlink-resolved `filepath.EvalSymlinks`
  prefix check);
- for `custom`, no containment requirement — the user asked for it explicitly — but the path is
  rejected if it resolves to `/`, `/etc`, `/usr`, `/bin`, `/sbin`, `/boot`, or `/proc`.

`workspace.root` defaults to `<data>/workspaces` and honours the existing
`SOCRATES_WORKSPACE_ROOT` environment variable (the e2e harness sets it).

**These rules are enforced on the server, not in the sheet.** `POST /api/sessions` returns 400 when
`workdir_mode=custom` and `workspace.allow_custom` is false, and when `workdir_mode=preset` and the
path is not one of `workspace.presets`. Hiding a control in the UI is a presentation choice; the
API is the boundary. A WP4 test covers both refusals.

## C.3 Binary discovery — reuse

Keep the current mechanism from `internal/catalog`: settings `Binary` override, else
`exec.LookPath(defaultName)`, then `probeVersion` with a 5 s timeout, cached in kv under
`agents_catalog` with a schema version and a 30-minute TTL, refreshable from admin. Rename the
package to **`internal/catalog`** (unchanged) and change only:

- the four ids (`shell` is always "installed"; its "binary" is `$SHELL` → `/bin/bash` → `/bin/sh`);
- `Discover` per harness (§C.11);
- the `Agent` struct's `Notes` text.

`catalog.Agent` keeps `ID, Label, Enabled, Installed, Path, Version, Error, Notes, Static,
Models, DefaultModel, DefaultEffort, HasEffort` so that `agents.js` and the admin card keep
working with minimal changes.

## C.4 Shell

```
Plan:   argv = [<shell>, "-l"]      (login shell, so the user's rc files run)
        env  = shared terminal env only
Resume: ErrNoResume — a shell is restarted fresh in the same directory.
CLI id: none; cli_session_state stays 'none'.
```

`<shell>` resolution order: `settings.Harnesses.Shell.Binary` → `$SHELL` → `/bin/bash` →
`/bin/sh`. If the resolved shell is not `bash`/`zsh`/`fish`/`sh`, still run it; the user knows
what they configured. `-l` is omitted when `settings.Harnesses.Shell.Login` is false.

## C.5 Claude Code

### Launch argv

```
claude --session-id <uuid>            \
       --name "<session title>"       \
       --model <model>                \   (omitted when empty)
       --effort <effort>              \   (omitted when empty)
       --permission-mode <mode>       \   (omitted when 'unset')
       [--dangerously-skip-permissions]|[--allow-dangerously-skip-permissions] \
       [--add-dir <dir>]…             \
       --settings <data>/sessions/<id>/claude-settings.json \
       [--setting-sources <list>]     \
       [--restricted] [--safe-mode] [--bare] \
       [--verbose] [--debug-file <data>/sessions/<id>/claude-debug.log] \
       [--remote-control [<name>]]    \
       [--mcp-config <path>] [--strict-mcp-config] \
       [--agent <name>] [--autocompact <auto|N>] \
       [--append-system-prompt <text>] \
       [--disable-slash-commands] [--no-chrome] \
       <extra_args…>
```

**`--session-id <uuid>` is the whole reboot-resume story for Claude** [HELP]. Socrates generates
the UUID with `uuid.NewString()` (dashes kept — Claude requires a valid UUID) and stores it in
`cli_session_id` with `cli_session_state='known'` **before** the launch. Nothing has to be
discovered.

**A session id may never be reused with `--session-id`.** The 2.1.258 binary carries the string
`Error: Session ID ${id} is already in use.`, so relaunching a session with the same
`--session-id` fails outright. The §C.8 flow already avoids this — a restart either resumes with
`--resume <uuid>` or starts with a *new* uuid — and `TestClaudeRestartNeverReusesSessionID` pins
it so a future simplification cannot reintroduce the crash.

Do **not** pass: `--fallback-model` (print-mode only per the binary's own help), `--tmux`
(requires `--worktree` and would create a second tmux), `--ide`, `--print` and the whole
print-mode family, `--bg`/`--background` (Socrates is the supervisor), `--cloud`/`--teleport`.

### Generated settings file

`<data>/sessions/<id>/claude-settings.json` — written fresh on every launch and passed with
`--settings`, which sits just under managed settings in precedence [DOCS]. This is the "one
auditable artefact per session" recommended by the research and it side-steps flag-vs-setting
precedence questions.

**Only verified keys are shipped by default.** The file contains exactly:

```json
{
  "skipDangerousModePermissionPrompt": true,
  "skipAutoPermissionPrompt": true,
  "cleanupPeriodDays": 90,
  "permissions": { "defaultMode": "...", "additionalDirectories": [...] },
  "env": { ... extra_env, plus the terminal env ... }
}
```

- The two `skip*` keys are **required for an unattended PTY launch** into bypass or auto mode —
  without them the session blocks on a confirmation dialog before the user can type — and they are
  written **only when the chosen `permission_mode` actually triggers those prompts**.
- `cleanupPeriodDays` is raised so a transcript survives long enough to be resumed after a long
  offline period. Admin field, default 90.

`askUserQuestionTimeout` and `autoContinueAtUsageLimit` were in an earlier draft of this spec and
are **not** shipped. They are documented keys that do not appear anywhere in the 2.1.258 binary's
strings, and `askUserQuestionTimeout: 0` might plausibly mean "dismiss every question instantly",
which would break `AskUserQuestion` in every session. They exist as admin fields defaulting to
**unset**, and WP3's manual check includes one `AskUserQuestion` round-trip with the generated file
to prove nothing auto-dismisses. Anything else a user wants goes through `settings_overrides`
(§C.10), which is deep-merged and is the user's own responsibility.

`attribution` is likewise **not** written: the key exists, but what Socrates would put in it is a
product decision nobody has made. It is an admin field, unset by default.

### Resume argv

Identical to the launch argv, with `--session-id <uuid>` replaced by `--resume <uuid>`, plus
`--fork-session` if the admin option `resume_mode` is `fork`. Prefer `--resume <uuid>` over
`-c/--continue`: `-c` is cwd-relative and picks "most recent", which races when several sessions
share a directory.

`--system-prompt-snapshot`: if the admin sets an `--append-system-prompt`, the default `on` means
a *changed* append on a later launch is ignored until compaction [HELP]. Admin therefore exposes
`system_prompt_snapshot` as `on|off` with a hint saying exactly that.

### Verifying the id before resume

`VerifyCLISession` checks that
`$CLAUDE_CONFIG_DIR/projects/<slug>/<uuid>.jsonl` exists, where `<slug>` is the transcript
directory. **Do not reimplement the slug** — the mangling has non-obvious double-dash cases
(`/root/.socrates/workspaces/chat-38325` → `-root--socrates-workspaces-chat-38325-…`) [TEST].
Instead glob `projects/*/<uuid>.jsonl`; the uuid is unique. Found ⇒ `true`. Not found ⇒ `false`
(and `cli_session_state='lost'`, so the session is relaunched fresh with a **new** uuid and the
UI says "the previous conversation could not be resumed"). Glob error ⇒ error ⇒ resume is
attempted anyway.

## C.6 Codex

### Launch argv

```
codex --strict-config                                 \
      -C <cwd>                                        \
      -m <model>                                      \   (omitted when empty)
      -c model_reasoning_effort="<effort>"             \   (omitted when empty)
      -s <read-only|workspace-write|danger-full-access> \
      -a <on-request|never>                            \   or -c approval_policy="on-failure"
      -c projects."<cwd>".trust_level="trusted"        \   ALWAYS
      -c tui.theme="<theme>"                           \
      [--no-alt-screen]                                \
      [--add-dir <dir>]…                               \
      [--search]                                       \
      [-c sandbox_workspace_write.network_access=true] \
      [-c hide_agent_reasoning=…] [-c show_raw_agent_reasoning=…] \
      [--enable <feature>]… [--disable <feature>]…     \
      [--dangerously-bypass-approvals-and-sandbox]     \
      [-c <raw key=value>]…                            \
      <extra_args…>
```

Three things are **mandatory** and a review must reject a package that omits them:

1. **`-c projects."<cwd>".trust_level="trusted"`.** Verified live ✅: launching codex in an
   untrusted directory shows a blocking *"Do you trust the contents of this directory?"* picker
   before the TUI, and the first keystroke Socrates sends is eaten by it. Since `dynamic` mode
   creates a brand-new directory for every session, this would fire every single time.
   Quote the path correctly: the key is `projects."<abs path>".trust_level`; the path goes inside
   double quotes in the TOML dotted key. Write a unit test with a path containing a space and a
   dot.
2. **`--strict-config`.** Without it, unknown `-c` keys are silently ignored ✅. With it, a typo
   in a generated override is a loud failure at launch instead of a setting that quietly does
   nothing. This is what makes the admin page trustworthy.
3. **`CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates`** in the env ✅ — it stamps `originator` into
   the rollout `session_meta` and is one of the three discriminators used to find the session id.

Do **not** pass: `--full-auto`, `--yolo`, `--skip-git-repo-check`, `--no-project-doc`, `--color`,
`--model-provider`, `--json` — **none of them exist in 0.152.0** ✅ and each errors out with
`unexpected argument`. Do not offer `-a untrusted` (removed) or `-a on-failure` / `-a granular`
(rejected by the *flag*; `on-failure` is available via `-c approval_policy`, `granular` is a
table, not a string) ✅.

### Discovering the session id

There is **no `--session-id` flag** and `CODEX_SESSION_ID` is an *output*, not an input — verified
live: setting it had no effect and `/status` reported a different id ✅.

`Discover = WatchCodexRollout`. `internal/harnesses/codex_discover.go`:

```go
func WatchRollout(ctx context.Context, codexHome, cwd string, since time.Time) (string, error)
```

- Poll `$CODEX_HOME/sessions/**/rollout-*.jsonl` (default `~/.codex`) every 2 s for up to
  **15 minutes**, considering only files with `mtime >= since`.
- Parse line 0 as `{"type":"session_meta","payload":{"session_id","cwd","originator",…}}` and
  accept the file whose `payload.cwd == cwd` **and** `payload.originator == "socrates"`.
- The uuid in the filename equals `payload.session_id` ✅, so once cwd is confirmed the filename
  is enough.
- **Timing caveat (verified ✅): neither a rollout file nor a `threads` row exists until a real
  user turn has happened.** The watcher must therefore be patient and must not treat "not yet"
  as an error. While it is waiting, `cli_session_state='pending'` and the UI shows nothing
  special — the session works; only reboot-resume is not yet armed.
- Secondary source, used by `VerifyCLISession` and as a cross-check:
  `SELECT id, rollout_path, updated_at FROM threads WHERE cwd = ? ORDER BY updated_at DESC LIMIT 1`
  against `$CODEX_HOME/state_5.sqlite`, opened **read-only** (`file:…?mode=ro&_pragma=query_only(1)`)
  with the `modernc.org/sqlite` driver that is already a dependency. Never write to it.

### Resume argv

```
codex resume <uuid> --strict-config -C <cwd> -m … -s … -a … \
      -c projects."<cwd>".trust_level="trusted" -c tui.theme="…" …
```
i.e. the same option block with `resume <uuid>` inserted ✅ (the identical option block is
accepted by `codex`, `codex resume`, `codex fork` and `codex queue`). `--last` is **not** used —
it is cwd-filtered by default and picks "most recent", which is a race.

`VerifyCLISession`, in this order:

1. **Primary: glob `$CODEX_HOME/sessions/**/rollout-*-<uuid>.jsonl`.** The uuid is in the file name
   ✅, so this is a filesystem check with no schema dependency. Found ⇒ `true`.
2. **Secondary: `SELECT 1 FROM threads WHERE id = ?` against `state_5.sqlite`**, read-only.

The order matters: `state_5.sqlite` and its `threads` table are **version-stamped names**. The `_5`
is a schema generation, and the next bump renames the file, at which point a SQLite-first verify
would answer "could not tell" for every session forever, silently. The rollout files are the
durable record and their naming has been stable. Neither found and both readable ⇒ `false` ⇒ launch
fresh. Both unreadable ⇒ error ⇒ try the resume anyway; a failed resume shows in the terminal,
which is honest.

## C.7 OpenCode

### Launch argv

```
opencode --port <port> --hostname 127.0.0.1 \
         [-m <provider/model>] [--agent <name>] \
         [--auto] [--pure] [--mini] \
         [--log-level <LEVEL>] \
         <extra_args…> \
         <cwd>                       # positional project path, LAST
```

`--print-logs` is **never** passed for the TUI: it writes to stderr and destroys the rendering [V].

`<port>` is allocated by Socrates: bind `127.0.0.1:0`, read the port, close, pass it. Store it in
memory keyed by session id (not in the store — it is only valid while the process lives). Retry
once on a bind race.

**The port must be password-protected.** The TUI serves the *full* opencode API on it —
`POST /session/{id}/shell`, `/tui/submit-prompt`, `/tui/execute-command` and 160 more paths — and
by default with no authentication at all, so any local process or any browser tab on the same
machine could drive the agent. The binary carries a Basic-auth layer keyed on
`OPENCODE_SERVER_PASSWORD` (username from `OPENCODE_SERVER_USERNAME`, default `opencode`) and logs
`Warning: OPENCODE_SERVER_PASSWORD is not set; server is unsecured.` when it is absent [V].

Socrates therefore generates 32 random bytes per session
(`base64.RawURLEncoding.EncodeToString(crypto/rand)`), passes them as
`OPENCODE_SERVER_PASSWORD`, and sends `Authorization: Basic opencode:<pw>` from the discoverer.
The password lives in memory and in `plan.json` (mode 0600); it is never in the store, never in a
log line and never in an API response. The tunnel never proxies this port.

### Env

```
OPENCODE_TUI_CONFIG=<data>/sessions/<id>/tui.json        # theme + mouse + attention:false
OPENCODE_CONFIG_CONTENT=<inline JSON>                    # model, share, autoupdate, permission…
OPENCODE_PERMISSION=<inline JSON>                        # when the admin sets a permission block
OPENCODE_DISABLE_AUTOUPDATE=1
OPENCODE_DISABLE_TERMINAL_TITLE=1
OPENCODE_DISABLE_MODELS_FETCH=1                          # admin toggle, default on: offline resilience
OPENCODE_DISABLE_PROJECT_CONFIG=1                        # admin toggle, default off
OPENCODE_DISABLE_MOUSE=1                                 # admin toggle, default off
```

`OPENCODE_CONFIG_CONTENT` is merged **last** of the file sources [V][D], so it is the reliable
lever. Its default content:

```json
{ "$schema": "https://opencode.ai/config.json",
  "share": "disabled", "autoupdate": false,
  "model": "<provider/model or omitted>",
  "small_model": "<or omitted>",
  "permission": { … from admin … },
  "enabled_providers": [ … from admin, omitted when empty … ] }
```

### Discovering the session id

`Discover = OpenCodeAPI`. There is no `--session-id` flag [V], but the TUI **is** the opencode
HTTP server, verified end to end [V]:

```
GET  http://127.0.0.1:<port>/session                       → array of sessions
POST http://127.0.0.1:<port>/session?directory=<abs cwd>    → {"id":"ses_…", …}
```

Recipe, in `internal/harnesses/opencode_discover.go`:

1. Poll `GET /session` **with the Basic-auth header** (2 s interval, 60 s budget) until the server
   answers at all. A 401 means the password did not match and is a launch failure, not a retry.
2. Take the newest entry whose `directory == cwd`. Session ids are **descending**-ordered [V], so
   "newest" is the lexicographically **smallest** id — do not assume the usual direction; sort by
   `time.created` instead, which is unambiguous.
3. If no such session exists yet (the user has not started a conversation), leave
   `cli_session_state='pending'` and keep polling in the background for 15 minutes.
   Do **not** `POST /session` to force one into existence: it would create an empty session the
   TUI is not showing, and `--session <that id>` on the next boot would open an empty screen.
4. Fallback if the HTTP server is unreachable (a future version might not serve it): read
   `~/.local/share/opencode/opencode.db` **read-only** —
   `SELECT id FROM session WHERE directory = ? ORDER BY time_created DESC LIMIT 1` [V].
   Honour `XDG_DATA_HOME`.

### Resume argv

```
opencode --port <newport> --session <ses_…> [--fork] [-m …] … <cwd>
```

**`--session` with an unknown id is a hard failure**: `Error: Session not found: ses_…` and the
process exits immediately [V]. `VerifyCLISession` is therefore **mandatory before resume**, not
optional: query `opencode.db` read-only for `SELECT 1 FROM session WHERE id = ?`. Missing ⇒
launch fresh and tell the user. Unreadable ⇒ launch fresh as well; for OpenCode "could not tell"
must degrade to a fresh session, because guessing wrong costs the user a crashed pane.

## C.8 Reboot detection and the resume flow

Reboot is detected in `Adopt` (§A.8): **the store says `running` and the tmux session is
absent**. That is the definition; there is no uptime check, no boot-id file, nothing else.

`Manager.Ensure(ctx, id)` — called whenever a viewer attaches to a session:

```
switch state {
case running:                 attach, done
case starting:                wait up to 10 s for running, else treat as failed
case exited:                  attach anyway (the pane is dead; the exit overlay is shown)
case needs_resume:            resume()
case failed:                  attach nothing; show the failure and a Restart button
}
```

`resume()`:

1. `h.VerifyCLISession(...)`. `false` ⇒ set `cli_session_state='lost'`, clear `cli_session_id`,
   and fall through to a fresh `Plan` (with a **new** uuid for Claude).
2. `h.ResumePlan(...)`, or `Plan(...)` for shell / a lost id.
3. Create the tmux session exactly as in §A.5 — row first, argv passed as separate arguments,
   `resize-window` to the current size, journal sink attached.
4. `store.NoteResume(id)` — `resume_count++`, `resumed = 1`, `state='running'`.
5. Push a `notice` control frame to viewers: `{kind:"resumed", resumed_from:"<cli id or empty>",
   fresh:<bool>}`. The UI renders the banner in §E.7 and `POST /api/sessions/{id}/ack-resume`
   clears it.

A restart triggered from the exit overlay is the same path with `state` forced to
`needs_resume` first.

## C.9 Admin option catalogue — Shell

| storage key (`harnesses.shell.*`) | control | validation | maps to | when |
|---|---|---|---|---|
| `enabled` | switch | — | offer in the picker | live |
| `binary` | text | must be executable if set | argv[0] | start |
| `login` | switch (default on) | — | `-l` | start |
| `extra_args` | text list | — | appended argv | start |
| `extra_env` | key=value list | `KEY` matches `[A-Za-z_][A-Za-z0-9_]*` | `-e KEY=VALUE` | start |

## C.10 Admin option catalogue — Claude Code

All keys under `harnesses.claude.*`. Every one is **start-only** unless marked otherwise.

**Group: Model & effort**

| key | control | values | maps to |
|---|---|---|---|
| `models[]` | model short-list editor (existing UI) | `{id, effort}` | the new-session picker |
| `default_model` | combobox | discovered ids + free text | `--model` |
| `default_effort` | segmented | `''`,`low`,`medium`,`high`,`xhigh`,`max` | `--effort` |
| `advisor` | combobox (optional) | model id | `--advisor` — **[DOCS]+[TEST] only, not in `--help`**; hide behind "Advanced" and validate by launching once |
| `autocompact` | text | `auto` or `100k`…`1M` | `--autocompact` |
| `max_thinking_tokens` | number | 0 = unset | env `MAX_THINKING_TOKENS` |

**Group: Permissions & sandbox**

| key | control | values | maps to |
|---|---|---|---|
| `permission_mode` | select | `unset`,`manual`,`acceptEdits`,`auto`,`plan`,`dontAsk`,`bypassPermissions` | `--permission-mode` (omitted for `unset`) |
| `skip_permissions` | select | `off`, `allow` , `force` | `off` ⇒ nothing; `allow` ⇒ `--allow-dangerously-skip-permissions`; `force` ⇒ `--dangerously-skip-permissions`. Guarded by a confirm-twice dialog. |
| `allowed_tools` | text list | | `--allowedTools` |
| `disallowed_tools` | text list | | `--disallowedTools` |
| `tools` | text | `''`, `default`, or a list | `--tools` |
| `add_dirs` | path list | absolute paths | `--add-dir` (repeatable) and `permissions.additionalDirectories` |
| `restricted` | switch | | `--restricted` |
| `safe_mode` | switch | | `--safe-mode` |
| `bare` | switch | | `--bare` — note it forces API-key auth; hint says so |
| `setting_sources` | multi | `user`,`project`,`local` | `--setting-sources` |
| `cleanup_period_days` | number (default 90) | 1–3650 | settings `cleanupPeriodDays` |

**Group: Remote control**

| key | control | maps to |
|---|---|---|
| `remote_control` | switch (default off) | `--remote-control` |
| `remote_control_name` | text | `--remote-control <name>` |
| `remote_control_prefix` | text | env `CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX` |

**Group: Session & prompt**

| key | control | maps to |
|---|---|---|
| `resume_mode` | select `continue`\|`fork` | `--resume` vs `--resume --fork-session` |
| `agent` | text | `--agent` |
| `append_system_prompt` | textarea | `--append-system-prompt` |
| `system_prompt_snapshot` | select `on`\|`off` | `--system-prompt-snapshot` |
| `exclude_dynamic_prompt_sections` | switch | `--exclude-dynamic-system-prompt-sections` |
| `disable_slash_commands` | switch | `--disable-slash-commands` |

**Group: Extensions**

| key | control | maps to |
|---|---|---|
| `mcp_config` | path list | `--mcp-config` |
| `strict_mcp_config` | switch | `--strict-mcp-config` |
| `plugin_dirs` | path list | `--plugin-dir` (repeatable) |

**Group: Theme & terminal**

| key | control | default | maps to |
|---|---|---|---|
| `pin_light_theme` | switch | **on** | writes `"theme":"light"` into `~/.claude.json` — see the C.1.2 caveat |
| `disable_terminal_title` | switch | on | env `CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1` |
| `disable_mouse` | switch | off | env `CLAUDE_CODE_DISABLE_MOUSE=1` |
| `no_flicker` | switch | on | env `CLAUDE_CODE_NO_FLICKER=1` |
| `force_sync_output` | switch | off | env `CLAUDE_CODE_FORCE_SYNC_OUTPUT=1` |

**Group: Diagnostics**

`verbose` (switch → `--verbose`), `debug_filter` (text → `-d <filter>`), `debug_file` (switch →
`--debug-file <data>/sessions/<id>/claude-debug.log`).

**Group: Advanced (raw)**

`extra_args` (text list, appended verbatim), `extra_env` (key=value list), `settings_overrides`
(JSON textarea, deep-merged into the generated `claude-settings.json`; invalid JSON is a save-time
error, not a launch-time surprise).

## C.11 Admin option catalogue — Codex

All keys under `harnesses.codex.*`, all start-only.

**Model & effort**: `models[]`, `default_model` (→ `-m`), `default_effort`
(→ `-c model_reasoning_effort=…`; documented values `minimal|low|medium|high|xhigh`, **not
validated at config load** ✅ so the UI must offer a closed list),
`model_reasoning_summary` (`auto|concise|detailed|none` ✅ → `-c`), `model_verbosity`
(`low|medium|high` ✅), `personality` (`none|friendly|pragmatic` ✅), `review_model` (text).

**Permissions & sandbox**: `sandbox` (select `read-only|workspace-write|danger-full-access` ✅ →
`-s`), `approval` (select `on-request|never` → `-a`; plus `on-failure` which is emitted as
`-c approval_policy="on-failure"` because the *flag* rejects it ✅; `untrusted` and `granular`
are **not offered**), `network_access` (switch → `-c sandbox_workspace_write.network_access=true`),
`writable_roots` (path list → `-c sandbox_workspace_write.writable_roots=[…]`),
`add_dirs` (path list → `--add-dir`), `approve_for_me` (switch → `--approve-for-me`),
`bypass` (switch, confirm-twice → `--dangerously-bypass-approvals-and-sandbox`),
`trust_workdir` (switch, **default on, disabling it shows a red warning**; when off, the
mandatory `projects.…trust_level` override is omitted and the session will block on the trust
picker — the option exists only so a user who understands it can turn it off).

**Remote control**: `remote_addr` (text → `--remote <ws://…>`), `remote_auth_token_env` (text →
`--remote-auth-token-env`; note this is the *name* of an env var, not the token — the hint says
so).

**Extra dirs**: covered by `add_dirs` and `writable_roots` above.

**Theme**: `tui_theme` (select from `light-gray`, `gruvbox-light`, `ocean-light`, `light-dark`,
`gruvbox-dark` + free text; default `light-gray` → `-c tui.theme="…"`),
`no_alt_screen` (switch, default **on** → `--no-alt-screen`; the hint explains it preserves
scrollback in a web terminal), `disable_keyboard_enhancement` (switch → env
`CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT=1`, for when keys misbehave through tmux).

**Tools & features**: `web_search` (switch → `--search`), `features_enable` / `features_disable`
(text lists → `--enable` / `--disable`; the admin card links to `codex features list` output
because `features.<anything>` passes `--strict-config` ✅ and cannot be validated by codex),
`hide_agent_reasoning` / `show_raw_agent_reasoning` (switches → `-c`).

**Config overrides (raw)**: `config_overrides` (list of `key=value` strings → one `-c` each),
`extra_args`, `extra_env`. Because `--strict-config` is always on, a bad key here fails the
launch loudly; the session goes to `state='failed'` with the CLI's stderr in `fail_reason`.

**Always applied, not user-visible**: `--strict-config`,
`-c projects."<cwd>".trust_level="trusted"` (unless `trust_workdir` is off), `-C <cwd>`,
`CODEX_INTERNAL_ORIGINATOR_OVERRIDE=socrates`.

## C.12 Admin option catalogue — OpenCode

All keys under `harnesses.opencode.*`, all start-only.

**Model & agent**: `models[]`, `default_model` (→ `-m provider/model`; note the model id itself
may contain slashes — everything after the *first* `/` is the model id [V]),
`small_model` (→ config `small_model`), `default_agent` (→ `--agent`; an invalid value silently
falls back to `build` [S], so the UI offers a closed list from `opencode agent list` when
available).

**Permissions**: `auto` (switch → `--auto`, "auto-approve everything not explicitly denied",
confirm-twice), `permission_json` (JSON textarea → `OPENCODE_PERMISSION`, validated as JSON at
save time; the shape is `{"*":"ask","bash":{"git *":"allow"},…}` with actions
`ask|allow|deny` [S]).

**Providers**: `enabled_providers` (text list → config `enabled_providers`, an allowlist),
`disabled_providers` (text list).

**Isolation**: `pure` (switch → `--pure`), `disable_project_config` (switch → env
`OPENCODE_DISABLE_PROJECT_CONFIG=1`), `disable_models_fetch` (switch, **default on** → env
`OPENCODE_DISABLE_MODELS_FETCH=1`; the hint says this is what makes OpenCode start without
network).

**Theme & terminal**: `tui_theme` (select from the verified built-in list, default `github`),
`mini` (switch → `--mini`; hint: line-oriented instead of alt-screen, easier on a phone),
`no_replay` / `replay_limit` (mini only; the UI greys them out unless `mini` is on),
`disable_mouse` (switch → env `OPENCODE_DISABLE_MOUSE=1`), `mouse` (switch → `tui.json` `mouse`),
`attention` (switch, default **off** → `tui.json` `attention.enabled`; a server harness must not
try to make desktop notification sounds).

**Session**: `resume_mode` (select `continue|fork` → `--session <id>` vs `--session <id> --fork`),
`share` (select `manual|auto|disabled`, default **disabled** → config `share`).

**Diagnostics**: `log_level` (select `DEBUG|INFO|WARN|ERROR` → `--log-level`). `--print-logs` is
deliberately **not** offered.

**Advanced (raw)**: `config_content` (JSON textarea, deep-merged into `OPENCODE_CONFIG_CONTENT`),
`tui_config` (JSON textarea, deep-merged into the generated `tui.json`), `extra_args`,
`extra_env`.

## C.13 Model discovery per harness

| harness | discovery | cost |
|---|---|---|
| `shell` | none; no model step in the sheet | — |
| `claude` | the existing static curated list in `internal/harness/claude/discover.go`, moved to `internal/harnesses/claude_models.go`. Keep `Catalog.Static = true`. | free |
| `codex` | `codex debug models --json`, a plain subprocess with a 20 s timeout ✅ — **replaces** the current `codex app-server` JSON-RPC handshake, which is far heavier and exists only because the old adapter needed the app-server anyway. | one short subprocess |
| `opencode` | `opencode models --json` if it supports `--json`, else parse `opencode models` [V]. 20 s timeout. | one short subprocess |

`internal/harness/{claude,codex,opencode}` — the protocol adapters — are deleted (§G). Only the
model lists survive, as plain functions.

---

# D. Transport protocol

## D.0 Library

**`github.com/coder/websocket` v1.8.15.** Zero dependencies, `context.Context` on every call,
built-in `permessage-deflate` — which matters materially here, because a terminal stream over a
mobile link is highly repetitive escape-sequence text. Reject `golang.org/x/net/websocket` (its own
docs say it is not actively developed) and `nhooyr.io/websocket` (the retired vanity path for the
same code). `gorilla/websocket` is an acceptable second choice if coder/websocket ever becomes a
problem, but it is not what we ship.

New direct dependencies for the whole rewrite: **two** — `github.com/coder/websocket` and
`github.com/creack/pty` v1.1.24 (MIT, zero deps). The repo goes from 2 to 4 direct modules.

## D.1 Endpoint

```
GET /api/sessions/{id}/ws?cols=<n>&rows=<n>&since=<seq>&viewer=<uuid>
Upgrade: websocket
```

- `cols`/`rows`: the viewer's terminal size at connect time. Required, 1–1000.
- `viewer`: a **stable per-tab id** the client generates once and keeps in `sessionStorage`. A
  reconnect from the same tab carries the same `viewer`, which is what lets the server find the
  replay ring and the input-dedupe state (§D.6).
- `since`: the last output sequence number the client rendered. Omitted or `0` on a first connect.

`Accept` options: `Subprotocols: []string{"socrates.term.v1"}`,
`CompressionMode: websocket.CompressionContextTakeover`, `OriginPatterns: []string{r.Host}`.
Read limit: 1 MiB.

## D.2 Frame formats

**Server → client.**

*Binary* — terminal output:

```
byte 0      : 0x01  (kind = output)
bytes 1..8  : uint64 big-endian, the sequence number of the FIRST byte in this frame's payload
bytes 9..   : raw terminal bytes
```

The sequence number counts **bytes of pane output delivered to this viewer**, starting at 1 for the
first byte after attach. It is per viewer, not per session, because each viewer's stream is a
different byte stream (tmux renders per client, §A.6). This is why `since` is meaningful only
together with `viewer`.

*Text (JSON)* — control frames. They carry no sequence number of their own: they are delivered in
order on the same socket, they are idempotent, and a counter nothing consumes is a counter that
rots.

```json
{"t":"hello",  "session":{…}, "size":{"cols":120,"rows":40},
               "replay_from":0, "input_ack":0, "viewer_fresh":true}
{"t":"size",   "cols":80, "rows":24, "by":"other"}     // "self" | "other"
{"t":"state",  "state":"running"}
{"t":"exit",   "status":7, "at":1788331699000}
{"t":"notice", "kind":"resumed", "fresh":false, "cli":"…"}
{"t":"input_ack", "seq":42}
{"t":"pong",   "id":9}
{"t":"error",  "message":"…", "fatal":true}
```

**Client → server.**

*Binary* — terminal input:

```
byte 0      : 0x02  (kind = input)
bytes 1..8  : uint64 big-endian, the client's monotonically increasing input sequence number
bytes 9..   : raw bytes to write to the PTY
```

*Text (JSON)* — control:

```json
{"t":"resize", "cols":100, "rows":30}
{"t":"lag",    "seq":123456}   // the last OUTPUT byte seq rendered; diagnostics only, once per second
{"t":"ping",   "id":9}
{"t":"bye"}                    // clean detach
```

`lag` is the client's acknowledgement of what it has actually rendered: the last output byte it
put on the screen. It is sent at most once a second and nothing in the transport depends on it.
The admin "viewer lag" row it was originally proposed for is **dropped** - WP8 built Diagnostics
out of checks that can fail, and a number that reads zero on every local network is not one - but
the frame stays, because nothing else tells the server what a viewer has actually drawn.

One writer goroutine per connection (coder/websocket allows one concurrent writer).

## D.3 Reconnect: reuse the PTY, and take over on a duplicate handshake

**Decision: on a WebSocket drop the server keeps the viewer's `tmux attach` PTY alive for
`viewerGrace = 90 s`.** A reconnect carrying the same `viewer` id adopts it, replays the missing
bytes from the ring, and continues. Only after the grace expires is `detach-client` run and the PTY
closed; a later reconnect then gets a fresh attach and a full tmux redraw.

Why, concretely:

- A fresh attach means a fresh tmux redraw. That is *correct* (E1 [V]) but it is 700–4000 bytes of
  full-screen repaint, and it resets the client's alt-screen and mouse-mode state, which makes the
  terminal visibly flash. On a phone in a car the connection drops several times a minute; a flash
  per drop is the difference between "the app is fine" and "the app is broken".
- Reusing the PTY makes the reconnect a *pure byte-stream gap*, which is exactly what a sequence
  number and a replay ring close perfectly. Nothing is re-rendered, nothing flashes, and the
  guarantee is provable: byte `since+1` onward is delivered, in order, once.
- The cost is one idle tmux client per recently-dropped viewer for at most 90 s, bounded absolutely
  by `maxViewersPerSession = 8`.

**Takeover.** The normal mobile failure is not a clean close: the old socket is half-open and the
server does not know yet. A second handshake carrying the **same `viewer` id** therefore **takes
over** the PTY immediately and closes the previous socket with code 1012 (Service Restart). Without
this the new socket would wait for the ping watchdog to condemn the old one — up to ~40 s of a
terminal that looks connected and does nothing. Takeover is synchronous: the new connection does not
send `hello` until the old writer goroutine has stopped.

If `since` is older than the ring can serve, the server closes the reused PTY, opens a fresh
`tmux attach`, and sends `hello` with `replay_from: 0`, which tells the client to `term.reset()`
before rendering. That path is correct and rare.

## D.4 Replay ring

Per viewer, in memory: `internal/termux/ring.go`

```go
type Ring struct { buf []byte; base, head uint64; mu sync.Mutex; cond *sync.Cond }
func NewRing(size int) *Ring                    // size = 1 MiB
func (r *Ring) Append(p []byte) uint64          // returns the seq of the first byte appended
func (r *Ring) Since(seq uint64) ([]byte, bool) // false when seq is older than base
func (r *Ring) Head() uint64
func (r *Ring) Wait(ctx context.Context, seq uint64) error // blocks until head > seq
```

1 MiB holds several seconds of a busy TUI redraw and many minutes of ordinary output. Eight viewers
cost at most 8 MiB. The ring is a fixed allocation; nothing trims it.

## D.5 Backpressure — the writer pulls from the ring

A stalled mobile client must not grow the server without bound, and must not stall tmux.

**The PTY reader's only job is `ring.Append`.** It never blocks on a socket. The writer goroutine
then loops: `ring.Wait(ctx, sent)`, take `ring.Since(sent)`, write it as one frame, advance `sent`.
A slow client simply falls behind in the ring and catches up by reading a larger slice next time —
its frames get bigger, not more numerous, which is exactly the right behaviour for a compressed
socket.

This design makes holes **structurally impossible** for a viewer that stays within the ring, so
there is no coalescing, no drop counter, and no "resync after we lost bytes" path. The one
remaining failure is a client so far behind that `sent` falls out of the ring; then
`ring.Since` returns `false` and the server resyncs the only way that is actually correct: it closes
the PTY, opens a fresh `tmux attach`, and sends `hello` with `replay_from: 0`.

**`refresh-client` is not a resync.** Measured on tmux 3.6: a `refresh-client -t <tty>` produced
201 bytes with no `ESC[2J`, no `?1049h` and no mode resets — it is a dirty-region refresh [V]. A
viewer that lost bytes may have the wrong alt-screen, mouse or bracketed-paste state, and a dirty
refresh cannot repair that. Resync is always a fresh attach.

If a viewer's write blocks for more than 30 s, the connection is closed with code 1013 and the PTY
enters the 90 s grace. tmux buffers per client and can grow; this is the guard.

Input is never dropped: the client-to-server direction is tiny.

## D.6 Exactly-once input, and the bound on the promise

The requirement from `mobile-connection-constraint` is *nothing the user sent may be lost, and
nothing may arrive twice.* The second half is absolute. The first half has a bound, and the bound
is stated here rather than glossed over.

### Client side

- Every input frame carries `input_seq`, a per-viewer counter. **It is not persisted anywhere.**
  The server is the authority on what it has seen, and it tells the client on every `hello`.
- Unacknowledged frames are held in an in-memory list. <!-- rev 4 --> Nothing outlives the page:
  the line composer that kept its text in `localStorage` (`socrates.term.<sessionId>.draft`) until
  it was acked went with the composer itself (§E.6), and a half-typed keystroke was never worth
  persisting across a page kill.
- **On every `hello`, the client resets its counter to `input_ack` and renumbers its held frames
  contiguously from `input_ack + 1`**, in their original order, before sending anything. Frames
  with an old `seq <= input_ack` are dropped first, so what remains is exactly the set the server
  has not written. Renumbering is safe because the content is what matters — the numbers exist only
  for dedupe, and `hello` carries the server's *current* `lastInputSeq`, not a stale batched ack.

  This rule is what makes a **page reload with frames in flight** work, which is the ordinary
  phone case: iOS kills the tab, the client comes back with an empty held list, and any counter it
  had remembered would now be ahead of the server's by however many frames were in flight. The
  first keystroke would be a gap, the server would ack its own lower number, the client would have
  nothing to resend from, and the terminal would be dead until the tab was closed. Anchoring to
  `input_ack` on every connect removes the gap by construction, and removes the need for the
  persisted counter along with it.
- What then remains is the *loss* question, answered by `viewer_fresh`:
  - **`viewer_fresh: false`** — the server still has this viewer's state. The renumbered held
    frames are resent in order before anything new. Lossless.
  - **`viewer_fresh: true`** — the server has no memory of this viewer (the grace expired, or
    Socrates restarted), so `input_ack` is 0 and it cannot tell a resend from new input. The client
    **must not** blindly resend: held raw keystrokes are **discarded** with a toast —
    *"N keystrokes may not have been delivered."* <!-- rev 4 --> The toast is the whole of it:
    there is no composer left to put an unacked line back into. Nothing is duplicated; the user is
    told.

### Server side

- Per viewer: `lastInputSeq uint64`, living in the viewer entry and therefore surviving the 90 s
  grace and a takeover. `hello` reports it as `input_ack`, and reports `viewer_fresh` for whether
  the entry was just created.
- **A fresh viewer entry accepts the first input frame at any `input_seq`** and sets
  `lastInputSeq = seq - 1`. Combined with the client's reset-to-`input_ack` rule this is
  belt-and-braces, but it costs one line and it means a client that gets the anchoring wrong
  degrades to "input works" rather than "input is dead".
- Afterwards: `input_seq <= lastInputSeq` ⇒ **discard** (a resend of something already written).
  `input_seq == lastInputSeq+1` ⇒ write to the PTY and ack. `input_seq > lastInputSeq+1` ⇒
  **reject**, reply `{"t":"input_ack","seq":<lastInputSeq>}` and let the client re-anchor. Gaps
  cannot occur on one TCP stream, but silently writing out-of-order keystrokes into a shell is
  worse than refusing them.
- Acks are batched: at most one `input_ack` per 100 ms, carrying the highest accepted `input_seq`.
  `hello`'s `input_ack` is never batched — it is read straight from `lastInputSeq`.

### The bound, stated plainly

Within a viewer's lifetime — including any number of network drops shorter than 90 s — input is
**exactly once**. Across a Socrates crash or an outage longer than the grace, in-flight bytes are
**at most once**: they may be lost, they are never duplicated, and the user is told. A stronger
promise would require persisting every keystroke before writing it to the PTY, which buys a
guarantee nobody can perceive at a cost everybody can.

The `offlineonce` e2e scenario (§H.3) proves the first half end to end.

## D.7 Auth, CSRF and origin

- The handshake is authenticated by the **same `socrates_session` cookie** as every other API call,
  through the existing `s.auth(...)` wrapper.
- Cookies are sent cross-origin on WebSocket handshakes, so **an origin check is mandatory**.
  `OriginPatterns` is set to exactly `r.Host` and `InsecureSkipVerify` is never set. Verified
  against coder/websocket v1.8.15's `authenticateOrigin`: an absent `Origin` is accepted
  (non-browser clients), otherwise the origin's host is compared case-insensitively to `r.Host` and
  then against `OriginPatterns` [V]. This matches the existing `sameOrigin(r)` helper in
  `server.go`; extend that helper rather than writing a second one.
- No second CSRF token: it adds nothing over the origin check for a same-site cookie and adds a
  failure mode on reconnect. Instead, **raise the session cookie to `SameSite=Strict`** in WP5. The
  only user-visible effect is that following an external link lands on the login page.
- Rate limit: at most 20 handshakes per minute per IP, reusing the existing `throttle(ip)`.

## D.8 Ping/pong watchdog

- Server: `conn.Ping(ctx)` every 15 s with a 10 s deadline. Two consecutive failures close the
  connection (and start the PTY grace).
- Client: `{"t":"ping","id":n}` every 15 s, offset by 7 s from the server's, expecting a `pong`
  within 10 s. A miss marks the connection degraded through net.js (§E.5) and triggers a reconnect.
- Neither side treats silence as failure on its own: a terminal is legitimately silent for hours.
  Only the ping/pong is a liveness signal.

## D.9 REST endpoints

```
GET    /api/sessions                       list (query: scope=active|all)
POST   /api/sessions                       create  {client_id, harness, model, effort,
                                                    workdir_mode, workdir, title?, options?}
GET    /api/sessions/{id}                  one session
PATCH  /api/sessions/{id}                  {title}
DELETE /api/sessions/{id}                  kill + delete (see A.10)
POST   /api/sessions/{id}/archive          {archived:bool}
POST   /api/sessions/{id}/restart          force a resume/restart (from the exit overlay)
POST   /api/sessions/{id}/ack-resume       clears the "resumed after restart" banner
GET    /api/sessions/{id}/journal          the raw byte journal, as a download
GET    /api/sessions/{id}/ws               the WebSocket

GET    /api/harnesses                      the catalogue — replaces BOTH /api/agents and
                                           /api/models; the per-harness model lists are fields
                                           of its response, so handleModels is deleted
POST   /api/harnesses/refresh
GET    /api/state, POST /api/setup, /api/login, /api/logout       unchanged
GET/PUT /api/settings, POST /api/settings/password                unchanged shape, new fields
GET    /api/tmux                           {installed, path, version, ok, manager, log}
POST   /api/tmux/install                   starts the installer
GET    /api/tmux/events                    SSE, installer progress (see §F.1)
GET    /api/tunnel, POST /api/tunnel/*     unchanged
POST   /api/voice/transcribe, /speak, GET /api/voice/status        unchanged
GET    /api/health                         unchanged
GET    /api/preferences, /api/diagnostics                          kept, updated content
```

`POST /api/sessions` is idempotent on `client_id`, exactly as `POST /api/chats` is today. That is
what makes "start a session while offline, come back, it exists once" work.

**Server-side enforcement of the workspace rules** (the UI is not a security boundary):
`POST /api/sessions` returns 400 when `workdir_mode=custom` and `workspace.allow_custom` is false,
and when `workdir_mode=preset` and the path is not one of `workspace.presets`. A WP4 test covers
both.

The installer's progress stream is **SSE**, not a WebSocket: `net.js`'s `LiveStream` already
implements exactly this — an `EventSource` with backoff, a self-watchdog and connection-status
reporting — and the admin page already uses it. A second transport for one progress log would be
new code that does worse what existing code does well.

---

# E. Frontend

## E.1 Files

```
internal/web/static/
  index.html                 the session page (sidebar + terminal)
  admin.html                 the dashboard
  login.html  setup.html     unchanged apart from copy
  sw.js                      updated SHELL list
  css/app.css                keeps the palette; gains terminal + key-bar sections
  img/favicon.png  img/logo.png
  js/
    net.js                   kept, plus one new export (§E.5)
    api.js                   kept verbatim (infoTip, placeBubble, el, toast, confirmDialog…)
    logos.js                 kept, plus a 'shell' mark
    combobox.js              kept
    harnesses.js             was agents.js: catalogue + the new-session sheet
    session.js               NEW: the page entry — sidebar, terminal, transport
    term.js                  NEW: xterm.js wiring, theme, addons, fit
    keybar.js                NEW: the key bar, off until the session menu asks for it
    voice.js                 kept verbatim
    admin.js                 rewritten for the new sections
    markdown.js              DELETED (no transcript any more)
    models.js                DELETED (folded into harnesses.js)
    chat.js                  DELETED
  vendor/
    VERSIONS                 pinned versions, one `name@version` per line
    xterm.js                 @xterm/xterm 6.0.0        lib/xterm.js
    xterm.css                @xterm/xterm 6.0.0        css/xterm.css
    addon-fit.js             @xterm/addon-fit 0.11.0
    addon-unicode11.js       @xterm/addon-unicode11 0.9.0
    addon-web-links.js       @xterm/addon-web-links 0.12.0
    addon-webgl.js           @xterm/addon-webgl 0.19.0
    addon-clipboard.js       @xterm/addon-clipboard 0.2.0
    LICENSE-xterm            the MIT text from @xterm/xterm
```

All seven are **MIT** and ship as **UMD bundles that define a browser global** (verified by
reading the first bytes: `…define([],t):e.FitAddon=t()`), so plain `<script>` tags work with no
bundler [V]. **Do not ship the `.js.map` files** — 1.9 MB for xterm alone, and the service worker
has to cache everything we ship.

The addons carry **no `peerDependencies` field** [V], so npm will not warn about a mismatch; the
pairing is by minor version (the beta line shows `addon-fit 0.12.0-beta.301` requiring
`@xterm/xterm ^6.1.0-beta.304`). Pin all seven explicitly in `vendor/VERSIONS` and re-vendor as a
set.

**`make vendor-xterm`** downloads and extracts them:

```make
vendor-xterm: ## re-download the pinned xterm.js bundle set into internal/web/static/vendor
	@bash scripts/vendor-xterm.sh
```

`scripts/vendor-xterm.sh` reads `vendor/VERSIONS`, curls
`https://registry.npmjs.org/@xterm/<p>/-/<p>-<v>.tgz`, extracts only the two or three files we
keep, and refuses to run if a checksum file `vendor/SHA256SUMS` exists and does not match. The
downloaded files are **committed**; the target exists for upgrades, and CI never runs it.

`THIRD_PARTY_LICENSES.md` gains a new section, **"Shipped in the web assets"**, listing all six
`@xterm/*` packages with version, licence (MIT), copyright line and the registry URL, and
recording that `vendor/LICENSE-xterm` carries the text. This is a legal obligation, not
bookkeeping: shipping the binary ships those files.

<!-- rev 4 --> Four modules have joined that directory since, all of them ACTIVITY.md's and all of
them in the service-worker `SHELL`: `assist.js` (the status and the ticker), `chat.js` (the panel,
which is why the `DELETED` above no longer holds), `daygroups.js` (the sidebar's day buckets) and
`notify.js` (the chime and the notification). The README carries the measured precache size.

## E.2 Page structure — index.html

```html
<body>
  <div class="app">
    <aside class="sidebar" id="sidebar">
      <div class="side-top">
        <!-- rev 5: the brand, and the control that takes the column to a rail. -->
        <div class="brand-row">
          <div class="brand">… <span class="brand-name">Socrates</span></div>
          <button class="icon-btn side-toggle" id="sideCollapse">…</button>
        </div>
        <button class="btn primary" id="newSession">New session</button>
        <div class="seg-row" id="sessionScope">
          <button class="seg on" data-scope="active">Active</button>
          <button class="seg"    data-scope="all">All</button>
        </div>
      </div>
      <div class="chat-list" id="sessionList"></div>
    </aside>
    <div class="nav-scrim" id="navScrim"></div>

    <main class="main">
      <header class="top">
        <button class="icon-btn" id="menuBtn">…</button>
        <span class="agent-badge" id="sessionHarness"></span>
        <h1 class="chat-title" id="sessionTitle"></h1>
        <span class="term-size" id="termSize"></span>       <!-- 120×40, hover-only detail -->
        <button class="icon-btn" id="sessionMenu">…</button>
      </header>

      <div class="term-wrap" id="termWrap">
        <div id="term"></div>
        <div class="term-overlay" id="termOverlay" hidden></div>   <!-- exit / failed / resuming -->
        <div class="term-notice" id="termNotice" hidden></div>     <!-- resumed / resized-by-other -->
      </div>

      <!-- rev 4: hidden until the session menu asks for it, on every device. -->
      <div class="keybar" id="keybar" hidden></div>
    </main>
  </div>

  <dialog class="sheet" id="newSessionSheet"> … </dialog>
  <div class="toasts" id="toasts"></div>

  <link rel="stylesheet" href="/static/vendor/xterm.css">
  <script src="/static/vendor/xterm.js"></script>
  <script src="/static/vendor/addon-fit.js"></script>
  <script src="/static/vendor/addon-unicode11.js"></script>
  <script src="/static/vendor/addon-web-links.js"></script>
  <script src="/static/vendor/addon-clipboard.js"></script>
  <script src="/static/vendor/addon-webgl.js"></script>
  <script type="module" src="/static/js/session.js"></script>
</body>
```

The classic `<script>` tags come **before** the module script, so the globals (`Terminal`,
`FitAddon`, `Unicode11Addon`, `WebLinksAddon`, `ClipboardAddon`, `WebglAddon`) exist when
`term.js` runs. They are not ESM; do not try to `import` them.

<!-- rev 4 --> **There is nothing under the pane but the key bar.** The `<form class="composer">`
above — `#lineInput`, `#micBtn`, `#recTime`, `#sendLine` — is removed on every device (§E.6). What
the header and the stage have gained instead belongs to ACTIVITY.md: `#statusBtn` and `#agentBtn`
beside the title (§D.2), `#soundBtn` and `#notifyBtn` for this device (§D.6), and `#chatPanel`
beside the terminal (§D.3).

`stampedRef` in `embed.go` rewrites `src="/static/…"` and `href="/static/…"` to carry `?v=`, so
the vendor files get the same immutable caching as everything else, with no change to `embed.go`.
Verify this in `embed_test.go`.

## E.3 The new-session sheet

Three steps in one sheet, revealed progressively; `harnesses.js` owns it.

1. **Harness** — a `.seg-row` of four `button.seg.with-mark` cells, each with `agentMark(id, 22)`
   from `logos.js` and an `infoTip` carrying the version and binary path (`bubbleClass:'mono'`).
   `shell` gets a new mark in `logos.js`: a `>_` glyph, tile style, `#131010`.
   Ids: `#nsHarness`, cells `data-value="shell|claude|codex|opencode"`.
2. **Directory** — `#nsDirField`: <!-- rev 5 --> a `combobox` (`combobox.js`, `strict:true`) over
   `Dynamic` (default, sub-text *a fresh directory for every session*), one entry per admin preset
   (label = the directory's name, sub-text = its full path) and `Custom path…`. It was a
   `.seg-row`; a machine with a dozen presets on it made that a dozen cells three characters wide.
   Strict because the list is the whole of what the server allows — unlike the model catalogue,
   which is open. Choosing `Custom path…` reveals `#nsDirPath`, a text input with a hint showing
   what will be created. Dynamic shows the path that *will* be created, greyed:
   `<root>/claude-20260902-064615-a1b2c3d4`. What is POSTed is unchanged: `workdir_mode` and
   `workdir`.
3. **Model** — `#nsModelField`, hidden entirely for `shell`. A `combobox` over
   `harnesses.modelItems(id)`, exactly as today. Effort is a `.seg-row` (`#nsEffort`) rebuilt per
   model, hidden when the model reports none.

Then `#nsStart` / `#nsCancel`. An `<details class="advanced">` disclosure holds per-session
overrides of the harness options; in v1 it contains only the options marked "per-session" in §C
(none are required), so it may render an empty state saying the defaults from the dashboard
apply. Keep the element and the disclosure so WP9a can fill it without a layout change.

Preserve, verbatim, the two hard-won behaviours from `agents.js`:

- Backdrop-click detection uses the **sheet's bounding rectangle vs `event.clientX/Y`**, not
  `event.target`, and ignores `event.detail === 0`. `combobox.js` commits on `mousedown` and hides
  its list, which retargets the subsequent click to the dialog.
- All per-opening handlers share one `AbortController` and are aborted on finish, so re-opening
  the same `<dialog>` cannot resolve an earlier promise.

Default title: `"<Harness label> · <basename of workdir>"`, e.g. `Claude Code · socrates`. For a
dynamic directory whose basename is the generated name, use the date instead:
`Claude Code · 2 Sep 06:46`. Renameable inline in the header (`#sessionTitle` becomes
`contenteditable` on click, commit on Enter/blur, `PATCH /api/sessions/{id}`).

## E.4 Terminal — `term.js`

```js
export function createTerm(host, opts)   // returns { term, fit, dispose, write, reset, focus }
export const LIGHT_THEME
```

```js
const term = new Terminal({
  allowProposedApi: true,           // REQUIRED: unicode11 throws without it
  fontFamily: 'var(--mono) resolved to a real stack',
  fontSize: opts.fontSize || 14,    // >= 14 on phones or iOS zooms on focus
  lineHeight: 1.15,
  scrollback: opts.scrollback || 2000,
  cursorBlink: true,
  cursorStyle: 'bar',
  convertEol: false,
  screenReaderMode: false,          // builds a parallel DOM live region; real cost on phones
  minimumContrastRatio: 4.5,        // THE most important setting for the white design
  smoothScrollDuration: 0,
  macOptionIsMeta: true,
  windowOptions: { getWinSizeChars: true, getWinSizePixels: true },
  theme: LIGHT_THEME,
});
```

`minimumContrastRatio: 4.5` is load-bearing: the CLIs emit dim greys that assume a dark
background and become invisible on white; this option re-derives colours at draw time.

`LIGHT_THEME` — white ground, palette drawn from the app's own tokens:

```js
export const LIGHT_THEME = {
  background: '#ffffff', foreground: '#17181b',
  cursor: '#17181b', cursorAccent: '#ffffff',
  selectionBackground: '#d7e3ff', selectionForeground: '#17181b',
  black: '#17181b', red: '#cf3f3f', green: '#1a9a63', yellow: '#b8811a',
  blue: '#2f6df6', magenta: '#8a4fd0', cyan: '#0e8a94', white: '#dcdde1',
  brightBlack: '#63666d', brightRed: '#e05555', brightGreen: '#22b273',
  brightYellow: '#cf9526', brightBlue: '#4c82ff', brightMagenta: '#a066e0',
  brightCyan: '#22a4ae', brightWhite: '#9b9ea6',
};
```

Every colour here is checked against `#ffffff` for ≥ 4.5:1 in a unit-testable JS function
`contrast(a, b)` that the `design` e2e scenario asserts over the whole palette. `white` and
`brightWhite` are deliberately greys, not white: a CLI that prints "white on default" must remain
readable on a white page. This is the same reasoning as `minimumContrastRatio`, applied to the
base palette so the runtime correction has less to do.

Addons, in this order:

```js
term.loadAddon(new Unicode11Addon.Unicode11Addon()); term.unicode.activeVersion = '11';
const fit = new FitAddon.FitAddon(); term.loadAddon(fit);
term.loadAddon(new WebLinksAddon.WebLinksAddon());
term.loadAddon(new ClipboardAddon.ClipboardAddon());   // OSC 52
term.open(host);
if (opts.webgl) {
  try {
    const gl = new WebglAddon.WebglAddon();
    gl.onContextLoss(() => { gl.dispose(); });         // iOS drops WebGL when a tab backgrounds
    term.loadAddon(gl);
  } catch { /* DOM renderer; the canvas renderer no longer exists in 6.x */ }
}
```

Fitting: call `fit.fit()` on `ResizeObserver` of `#termWrap` **and** on `visualViewport`
`resize`/`scroll` (not `window.resize`) so the phone keyboard opening re-fits. Debounce 80 ms.
After every fit, send `{"t":"resize","cols","rows"}` if the size changed.

The hidden textarea gets `autocorrect="off" autocapitalize="none" autocomplete="off"
spellcheck="false"` — set them on `term.textarea` right after `term.open()`.

`term.onData(d => transport.sendInput(d))` — a single path for keystrokes, mouse reports, and the
xterm.js replies to DA1/DA2 that tmux asks for on attach. **The input path must be wired before
the first output frame is rendered**, because those replies travel browser → WS → PTY.

## E.5 Transport client and connection status

`session.js` owns one `TermSocket`:

```js
class TermSocket {
  constructor({ sessionId, viewerId, onOutput, onControl, onStatus })
  connect(); close(); sendInput(bytes); resize(cols, rows); lag(seq);
}
```

It implements §D: input numbering anchored to the server's `input_ack` on every `hello` (nothing
persisted), renumber-and-resend of held frames, the `viewer_fresh` discard-and-toast path,
exponential backoff with jitter (`base 700 ms, max 15 s` — the same numbers `LiveStream` uses), and a
ping/pong watchdog.

**net.js needs one new export**, because `setConnection` is module-private today and a WebSocket
is not a `LiveStream`:

```js
// net.js
export function connectionSource({ global = true } = {}) {
  // Returns { report(status, extra), release() } where status is
  // 'live' | 'connecting' | 'offline' | 'idle'. Internally it registers a
  // pseudo-stream in liveStreams/globalStreams so streamsHealthy() and the
  // window offline/online listeners keep working unchanged.
}
```

`TermSocket` holds one `connectionSource({global:true})` and reports `'connecting'` on every
reconnect attempt (with `retryAt`), `'live'` when the `hello` frame arrives, `'offline'` when
`navigator.onLine === false`. Everything else — the bar, `body.conn-lost`, `--conn-bar-h`,
`body.stale`, the 1800 ms grace — keeps working with **no change**.

This is a strict addition. `LiveStream` (the existing `EventSource` wrapper, with its backoff,
self-watchdog and status reporting) stays exactly as it is and remains the transport for the one
other stream in the app: the tmux installer's progress log over SSE (§F.1). Two transports is the
right number here — a terminal wants a bidirectional binary socket, a progress log wants an
`EventSource` and already has a working implementation.

While `body.conn-lost` is set, `#termWrap` gets `.stale`: opacity 0.55 and a
`cursor: not-allowed`. The terminal is **not** made read-only — typed characters are queued and
delivered on reconnect (§D.6) — but the user must see that what is on screen is old.

## E.6 The key bar — `keybar.js`

```js
export function mountKeyBar(host, term, socket)
export function keyBarWanted()
export function setKeyBarWanted(on)
```

<!-- rev 4 --> **It is off until it is asked for, on every device.** `keyBarWanted()` reads
`localStorage['socrates.term.keybar']`, which is absent until the session menu's **Show key bar**
writes it, and `setKeyBarWanted()` is the only writer; the menu item reads **Hide key bar** while
the bar is up. Nothing else decides — no `matchMedia('(pointer: coarse)')`, no viewport width, no
`hover`/`pointer: fine` pair, no platform string and no keystroke seen. A bar that guessed was
wrong on the two devices that matter most, a tablet in a case and a laptop with a touch screen,
and being wrong about it means either a row of buttons nobody wanted or the missing keys nowhere
to be found. One tap in the ⋯ menu is cheaper than either, and the answer is remembered for this
device.

Keys, one row, horizontally scrollable, each `button.key[data-send]`:

| label | sends |
|---|---|
| `Esc` | `\x1b` |
| `Tab` | `\t` |
| `Ctrl` | sticky modifier (see below) |
| `Alt` | sticky modifier |
| `←` `↓` `↑` `→` | `\x1b[D` `\x1b[B` `\x1b[A` `\x1b[C` |
| `⏎` | `\r` |
| `^C` | `\x03` |
| `^D` | `\x04` |
| `^Z` | `\x1a` |
| `Paste` | `navigator.clipboard.readText()` then send, bracketed if the term asked for it |
| `⌨` | `term.textarea.focus()` **synchronously inside the touchend/click handler** |

Sticky `Ctrl`/`Alt`: tapping arms the modifier (`.on`), the next key press is transformed
(`Ctrl+x` → `String.fromCharCode(code & 0x1f)`; `Alt+x` → `\x1b` + x) and disarms it. A second tap
locks it (`.lock`) until tapped again.

**The `⌨` button must call `term.textarea.focus()` synchronously in a `touchend`/`click`
listener, never after an `await`** — iOS will not raise the keyboard otherwise. It is the whole of
how a phone raises a keyboard now, so the synchronous call is load-bearing rather than a
convenience.

<!-- rev 4 --> **There is no line composer.** `#lineInput`, `#micBtn`, its Send button, the
pending-line list, the `socrates.term.<sessionId>.draft` drafts and the `viewer_fresh`
line-restore (§D.6) are removed on every device. It existed to fight iOS autocorrect, which
rewrites characters that have already been sent one at a time, and it cost the page a second
field, a second microphone and a second promise about text nobody had sent yet. What a phone
actually wants to say to a session it now says in words: the chat beside the terminal, with one
input row for everyone — a field and a Send (ACTIVITY.md §D.3). Typing into the pane is the
terminal's own path, unchanged, and the key bar supplies the keys a touch keyboard has not got.

**Dictation lives in the chat, not here.** `voice.js` is still the recorder — `new Recorder()`,
`start()`, `stop()` → `{base64, format, seconds}` → `api('/api/voice/transcribe', {method:'POST',
attempts:3, timeout:60000, body:{audio, format}})` → `{text}` — but it is `chat.js` that calls it,
through `dictateOnce`. <!-- rev 5 --> The way in is a **`#chatMic` pill in `.chat-head`** —
outlined, 999px, microphone plus the word "Speak", ≥44px tall — not an icon in the input row; it
opens `#chatRecSheet`, a `<dialog class="sheet">` with a live level meter (`#chatRecMeter`, drawn
from the recording's own `AnalyserNode`, which `dictateOnce` hands to `onReady` alongside `stop`
and `cancel`; one filling bar under `prefers-reduced-motion`), the elapsed clock `#chatRecTime`,
and the two endings as 56px buttons: **Send** and **Cancel**. Escape and the backdrop are Cancel,
closing the sheet for any reason releases the microphone and stops the meter, and while the
transcript is in flight the pill reads "Transcribing…" and is disabled. `describeMicError` still
provides the failure sentence.

<!-- rev 5 --> **TTS is wired in, and one switch decides it.** `#chatSpeak` in `.chat-head` — the
same on/off drawing as `#soundBtn`, `aria-pressed` — is whether assistant answers are read out
loud. It is **off by default**, remembered per device in `localStorage` under
`socrates.chat.speak`, and **turned on by the first successful dictation**; it can be turned off
again by hand. With it on, every arriving assistant answer is read (`ctx.say`) and the question is
posted with `auto: true`, which is what asks `chat.go` to phrase the answer for the ear; with it
off nothing is read and `auto` is `false`. Any answer is still readable on a double-tap, and the
same gesture stops it. `speak()` refuses while a recording is open and a starting recording
silences it, so the microphone and the voice are never both live (ACTIVITY.md §D.3). It is still never given the pane — a pane is a program, not an answer.
`internal/server/voice.go` and `internal/piper` are untouched, `voice.js` stays in the
service-worker SHELL list and `embed_test.go` keeps asserting it.

## E.7 Overlays and notices

`#termOverlay` (blocks input, centred card on white with a hairline):

| state | content |
|---|---|
| `exited` | `The session ended.` + `Exit status <n>` behind an `infoTip` + buttons **Restart** (`POST /restart`) and **Delete** |
| `failed` | `The session could not start.` + the `fail_reason` in an `infoTip` (`bubbleClass:'mono'`) + **Try again** |
| `resuming` | a small spinner + `Resuming after a restart…` |
| `needs_resume` | `This session is not running.` + **Open** (which triggers the resume) |

`#termNotice` (a thin line at the top of the terminal, dismissible, never blocking):

| kind | text |
|---|---|
| `resumed` | `Resumed after a restart.` — plus `The previous conversation could not be resumed, so this one starts fresh.` when `fresh` is true. Dismissing calls `POST /ack-resume`. |
| `resized` | `Another viewer resized this session to 60×20.` (§A.7) |
| `desync` | `Reconnected — the screen was redrawn.` — after a `replay_from:0` hello |

All three follow the design rules: white ground, hairline border, no second background shade, the
technical part (exit status, stderr, the other viewer's size) behind an `infoTip`.

<!-- rev 5 --> **A notice puts itself away.** Every line above the pane fades out after
`NOTICE_LINGER` (6 s, the same number as `TICKER_LINGER` in `assist.js`, because the two lines are
stacked over the same pane) and a new one replaces whatever is up, so the space is free for the
next. Going on the timer is the same decision the close button makes — `onDismiss` runs either
way, so the `resumed` line still `POST`s `/ack-resume`. The one exception is a line carrying a
control the person still has to press (`extra`, the **Cancel** of a run in progress): that is not
news, and it stays until the run does. The manual close button never goes away, and under
`prefers-reduced-motion` the fade is instant.

## E.8 Session list

Each row: `agentMark(harness, 18)`, the title, and a state dot. The dot is the only colour:
`--green` running, `--text-faint` needs_resume, `--red` failed, `--amber` exited. A row's
technical detail (workdir, model, CLI session id, resume count) is behind an `infoTip` with
`bubbleClass:'mono'` — never in visible text.

Row actions in an overflow menu: Rename, Archive/Unarchive, Download scrollback, Delete.
Delete uses `confirmDialog({danger:true})` and its body says the working directory is kept.

Motion: rows fade in over 120 ms with `--ease`; the state dot pulses only while `running` and
only when `!body.stale` (the existing `body.stale` rule already pauses `.chat-item .dot`).

<!-- rev 5 --> **The sidebar collapses to a rail.** `#sideCollapse`, a hairline `icon-btn` beside
the brand, takes the column from 264px to 56px (`--side-w` on `.app`, a 160 ms width transition,
none under `prefers-reduced-motion`) and back. Collapsed, the rail is marks and no words: the logo,
a square **New session**, one `agentMark` per session with the activity ring it already had and the
session's name in its `title`, and Admin / Sign out as icons. The scope switch and the day-group
words go; the group's hairline stays. The answer is kept per device in `localStorage`
(`socrates.sidebar`), and the terminal is refitted once the stage has stopped moving. Below the
drawer breakpoint the control is not on the page at all and the rail is never entered — a drawer
that is also a rail is a drawer with nothing in it to read.

<!-- rev 4 --> The rows are **grouped by day** — Today, Yesterday, This week, This month, Older,
by `updated_at` in the browser's own local calendar (`daygroups.js`), a group with nothing in it
not drawn at all — because a phone's call list is read that way: what is being worked on today,
and history under it. A session that finishes while nobody is looking at the list says so instead
(ACTIVITY.md §D.6): a chime and a browser notification, each behind its own header switch for this
device.

## E.9 Service worker

`sw.js` keeps its whole mechanism — network-first, per-build cache name
`socrates-shell-<VERSION>`, `hasWholeShell()`, `dropOtherBuilds()`, the navigate fallback to `/`.
Only `SHELL` changes:

```js
const SHELL = [
  '/',
  '/static/css/app.css',
  '/static/vendor/xterm.css',
  '/static/vendor/xterm.js',
  '/static/vendor/addon-fit.js',
  '/static/vendor/addon-unicode11.js',
  '/static/vendor/addon-web-links.js',
  '/static/vendor/addon-clipboard.js',
  '/static/vendor/addon-webgl.js',
  '/static/js/net.js',
  '/static/js/api.js',
  '/static/js/session.js',
  '/static/js/term.js',
  '/static/js/keybar.js',
  '/static/js/harnesses.js',
  '/static/js/logos.js',
  '/static/js/combobox.js',
  '/static/js/voice.js',
  '/favicon.png',
  '/static/img/logo.png',
].map((path) => (path === '/' ? path : path + '?v=' + VERSION));
```

Total precache: **about 807 KB uncompressed, 206 KB over the wire** with the gzip the server
already applies. Measured from the real tarballs: `xterm.js` 488,663 B, `addon-webgl.js`
247,535 B, `addon-unicode11.js` 52,489 B, `xterm.css` 7,112 B, `addon-clipboard.js` 6,384 B,
`addon-web-links.js` 3,100 B, `addon-fit.js` 1,521 B. (The multi-megabyte figures in the research
table are *tarball* sizes, which are dominated by the `.js.map` files we do not ship.) That is a
comfortable app shell; every addon stays in `SHELL`. WP7 records the measured number in the
README so a future addition is noticed.

`internal/web/embed_test.go` keeps `TestServiceWorkerShellHoldsTheWholeChatImportGraph`, retargeted
at `session.js`, and gains an assertion that every `/static/vendor/*` file that `index.html`
references with a `<script>`/`<link>` tag is in `SHELL`.

## E.10 Design rules, restated as acceptance criteria

Every UI package is reviewed against these; a package that breaks one is rejected.

1. **Every surface is pure white.** `--bg` and `--bg-soft` are `#ffffff`; the terminal background
   is `#ffffff`; the sidebar is `#ffffff`. Structure comes from `--line` hairlines and spacing.
   The only permitted non-white fills are `--bg-hover`/`--bg-sunken` on interactive affordances,
   the accent-filled primary button, the toast pill, and the connection bar.
2. **Agent marks everywhere a harness is named** — `agentMark()` from `logos.js`, in the sheet,
   the header badge, the session rows, the admin cards and the diagnostics rows.
3. **Technical strings are hover-only** — version, binary path, exit status, stderr, workdir, CLI
   session id, tmux session name all live behind `infoTip`, never in visible text.
4. **Motion is subtle** — 120–200 ms, `--ease`, and everything is disabled under
   `prefers-reduced-motion`. The terminal itself has `smoothScrollDuration: 0`.
5. **Everything in English.**
6. The `design` e2e scenario asserts 1, 2, 3 and 4 by measurement, not by screenshot.

---

# F. Admin

`admin.html` + `admin.js`. Sections, in order, each a `<section class="card">` with an `<h2>` and
a `<p class="card-sub">`, using the existing `.field > label + .input|.select + .hint` and
`label.switch` controls. All white, hairline-separated, no boxes.

## F.1 Terminal engine (tmux) — `#tmuxCard`

Shows: `#tmuxStatus` (a `.state-dot` + `.state-label`), the version, and the binary path behind an
`infoTip`. States: `ok` (green), `too old` (amber, `< 3.3`), `missing` (red).

`GET /api/tmux` → `{installed, path, version, ok, min:"3.3", manager, privileged, log}`.

**Detection** (`internal/termux/install.go`), in order:

1. `exec.LookPath("tmux")`; if found, `tmux -V`, parse `tmux (\d+)\.(\d+)`. Require **≥ 3.3**.

   The floor is 3.3, not 3.0, and the reason is the generated conf: `allow-passthrough` and
   `remain-on-exit-format` arrived in **3.3**, `extended-keys`, `terminal-features` and
   `new-session -e` in **3.2**. On tmux 3.2a — which is what Ubuntu 22.04 ships — the conf errors
   at load and `-e` is rejected outright, at which point the start-up guard (§A.3) would quietly
   fall back to a minimal conf and the installer would cheerfully report 3.2a as "ok" while
   sessions launched without their environment. Verified that 3.3a (Debian bookworm, the Docker
   base) loads the conf and does everything load-bearing [V]. Local machine has 3.6 [V].
2. If missing, detect the package manager by `LookPath`, first hit wins:
   `apt-get` → `apk` → `dnf` → `yum` → `pacman` → `zypper` → `brew`.
3. Privilege: `os.Geteuid() == 0` ⇒ no wrapper. Else `LookPath("sudo")` and probe with
   `sudo -n true`; only on exit 0 use `sudo -n`. Otherwise report "tmux is missing and cannot be
   installed without a password" together with the exact copy-paste command.

**Command matrix** (`{}` = the privilege prefix, empty or `sudo -n`):

| manager | command |
|---|---|
| apt-get | `{} apt-get update -qq` then `{} apt-get install -y --no-install-recommends tmux ncurses-term`, with `DEBIAN_FRONTEND=noninteractive` |
| apk | `{} apk add --no-cache tmux ncurses-terminfo` |
| dnf | `{} dnf install -y --setopt=install_weak_deps=False tmux ncurses-term` |
| yum | `{} yum install -y tmux` |
| pacman | `{} pacman -Sy --noconfirm --needed tmux` |
| zypper | `{} zypper --non-interactive install tmux` |
| brew | `brew install tmux` — **never** with sudo; refuse when euid is 0 |

Verified for this box: `apt-cache policy tmux` → candidate `3.6a-2ubuntu0.1` [V]. The others are
conventional and were not executed [A]; the implementer must not claim otherwise in a comment.

**Streamed progress.** `POST /api/tmux/install` starts it; `GET /api/tmux/events` is an **SSE**
stream carrying one `line` event per output line and a final `done` event
(`{"exit":N,"ok":bool}`). The admin page consumes it with the existing `LiveStream` from `net.js`,
which already gives backoff, a self-watchdog and connection-status reporting — a second WebSocket
for a progress log would be new code doing worse what existing code does well.

```go
cmd := exec.CommandContext(ctx, name, args...)
cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
cmd.Stdin = nil                                        // never let a prompt block us
pr, pw := io.Pipe(); cmd.Stdout, cmd.Stderr = pw, pw   // interleave both streams
```

Scan `pr` line by line. Whole run wrapped in `context.WithTimeout(5*time.Minute)` and guarded by a
process-wide mutex so two admins cannot install at once. Persist the last 200 lines and the exit
code in kv under `tmux_install_log` so the page shows the result after a reload.
On failure, surface stderr verbatim **plus** the manual command for the detected manager.

**There is no PTY fallback.** Per DECISIONS.md, tmux missing is a hard, explained error: the
new-session sheet's Start button is disabled with the sentence
*"Socrates needs tmux to keep sessions alive. Install it in the dashboard."*

## F.2 Workspace — `#workspaceCard`

- `#workspaceRoot` — text, absolute path, default `<data>/workspaces`. Validated on save:
  absolute, creatable (`os.MkdirAll` dry run via `os.Stat` + `MkdirAll` on save).
- `#defaultHarness` — a `.seg-row` of the four, with marks.
- `#allowCustomDir` — switch.
- `#presetDirs` — a repeating row editor (`.preset-row`: label input, path input, remove button;
  `#presetAdd` appends). Each path is checked for existence on save; a missing one is saved with
  an amber warning rather than refused, because a mount may not be up yet.

## F.3 Terminal behaviour — `#terminalCard`

`#windowSize` (select `manual|latest|largest`), `#historyLimit` (number, 1000–200000),
`#mouseOn`, `#extendedKeys` (switch, hint: *"lets Shift+Enter reach the CLIs; may confuse older
terminals"*), `#scrollback`, `#fontSize`, `#webgl`.

`window_size` offers `manual` (default), `latest` and `largest`, each with a one-line hint:
*manual — Socrates sizes the window to the viewer that last connected or resized; typing never
changes it*; *latest — the window follows whichever viewer last typed or attached*; *largest — the
window is as large as the biggest viewer, and smaller viewers pan over it*.

Changing `history_limit`, `mouse` or `extended_keys` rewrites `<data>/tmux.conf` **and** applies
what can be applied live with `tmux set -g …`. **`window-size` is applied live only and is never
written to the conf file** — a conf containing `window-size manual` segfaults tmux 3.6 on start-up
(§A.3). The hint says which settings only take effect for **new** sessions (`default-terminal`,
`remain-on-exit`, `window-style`).

## F.4 Harnesses — `#harnessCards`

One `.card` per harness, each headed by `agentMark(id, 22)` + the label, with `infoTip` carrying
version and path (this is the existing `agentFacts` pattern from `agents.js`/`admin.js`, kept).

Inside each card, the option groups from §C.9–C.12 as `<details class="group">` disclosures:
**Model & effort**, **Permissions & sandbox**, **Remote control**, **Extra directories**,
**Theme & terminal**, **Extra flags (raw)**, **Extra env (raw)**, **Config overrides (raw)**.
Every control carries a `.hint` naming the flag/env/config key it maps to — that is technical, so
it goes in the hint text (which is already visible, small and grey by design) rather than in an
`infoTip`; `infoTip` is reserved for *state* (versions, paths), not for *documentation*.

Each field carries `data-startonly="1"` when it only affects new sessions; the card shows one
line at the bottom: *"These apply to sessions started from now on."*

The model short-list editor (`.model-list`, `.model-add`, `.model-row`) is kept from the current
`admin.js` with no functional change.

`POST /api/harnesses/refresh` re-runs discovery (binary lookup + version + model list) with the
existing 2-minute budget and a spinner.

## F.5 Voice — `#voiceCard`

Unchanged from today: `#voiceStatus`, `#voiceLanguage` (`en`/`de`), `#sttPrompt`, `#ttsRate`,
`#speakAuto`, `#speakChat`, `#testVoice`, plus the OpenRouter key and models
(`#orKey`, `#orTranscribe`, `#orTitle`). The two "speak" switches keep their storage keys and keep
working; their hint gains one sentence: *"Reading answers out loud is not wired into the terminal
yet."* Dictation must be fully working — that is a DECISIONS.md requirement and an e2e scenario.

## F.6 Remote access — `#tunnelCard`

Unchanged: `#tunnelStatus`, `#tunnelMode`, `#tunnelHostname`, `#tunnelToken`, `#tunnelCommand`,
`#tunnelArgs`, `#tunnelStart`, `#tunnelStop`, `#tunnelInstall`, `#tunnelLogToggle`, `#tunnelLog`.
`internal/tunnel` is untouched.

## F.7 Diagnostics — `#checksCard`

`#runChecks` → `POST /api/diagnostics`. Checks, each a row with a mark where a harness is named:

- tmux present, version, socket reachable, session count
- workspace root writable
- each harness: binary found, version, model list reachable
- for Claude: `$CLAUDE_CONFIG_DIR` writable (needed for the theme pin)
- for Codex: `$CODEX_HOME/state_5.sqlite` readable
- for OpenCode: `~/.local/share/opencode/opencode.db` readable
- OpenRouter key present and the transcription model reachable
- disk space under `<data>`

## F.8 Password — `#passwordCard`

Unchanged (`#pwCurrent`, `#pwNext`, `#changePw`).

---

# G. Deletion and keep lists

## G.1 Delete — packages and files

```
internal/engine/                      (whole package)
internal/agenthost/                   (whole package: client.go, host.go, journal.go,
                                       manager.go, protocol.go, sockpath.go, detach_*.go,
                                       hosttest/, and their tests)
internal/harness/harness.go           Adapter/Spec/Descriptor/registry
internal/harness/event.go             + event_test.go
internal/harness/claude/              claude.go, stream.go, wire.go, resume_test.go,
                                       live_test.go, claude_test.go
internal/harness/codex/               codex.go, rpc.go, notify.go, live_test.go, codex_test.go
internal/harness/opencode/            opencode.go, httpclient.go, sse.go, mapping.go,
                                       wording.go, live_test.go, opencode_test.go
internal/harness/fakes/               fakes.go, fakes_test.go, script/, fakeclaude/,
                                       fakecodex/, fakeopencode/
                                       (script/ backs harness.mjs's SCRIPT and LONG_SCRIPT
                                        exports, which go with it — see §H.2)
internal/web/static/js/chat.js
internal/web/static/js/markdown.js
internal/web/static/js/models.js
internal/server/agents.go             (replaced by harnesses.go)
internal/server/api.go                (chat/message/run/step endpoints; replaced by sessions.go)
internal/server/agents.go             including handleModels and the GET /api/models route,
                                       which is folded into GET /api/harnesses
```

Kept from the deleted tree, moved:

- `internal/harness/claude/discover.go` → `internal/harnesses/claude_models.go` (the curated
  static model list only).
- `internal/harness/codex/discover.go` → replaced by a `codex debug models --json` call in
  `internal/harnesses/codex_models.go`. The JSON-RPC machinery goes.
- `internal/harness/opencode/discover.go` → `internal/harnesses/opencode_models.go`.
- `internal/harness/catalog.go` (`Catalog`, `Model`, `OrderEfforts`) → `internal/harnesses/catalog.go`.

Store: drop `Chat`, `Message`, `Run`, `Step` and every method on them; drop the `chats`,
`messages`, `runs`, `steps` tables (§B.2). `RecoverRuns` goes.

`main.go`: the `agent-host` subcommand goes; `tmux-hook` (§A.3) and `journal-sink` (§B.6) arrive.
`srv.ResumeAgents()` / `srv.DetachAgents()` become `srv.AdoptSessions()` / `srv.DetachViewers()`.

## G.2 Keep — and what "keep" means

| kept | note |
|---|---|
| `internal/store` | schema replaced, `kv` and the auth table (renamed `logins`) survive |
| `internal/config` | `OpenRouter`, `Voice`, `Tunnel` verbatim; `Agent`/`Agents` replaced by `Workspace`/`Terminal`/`Harnesses` |
| `internal/catalog` | binary discovery, version probe, kv cache, TTL, refresh — retargeted at the four harnesses |
| `internal/openrouter` | untouched |
| `internal/piper` | untouched |
| `internal/tunnel` | untouched |
| `internal/proc` | untouched; still needed for the tunnel and the installer |
| `internal/server/auth.go` | untouched apart from the `logins` rename and `SameSite=Strict` |
| `internal/server/voice.go` | untouched |
| `internal/server/tunnel.go` | untouched |
| `internal/web/embed.go` | untouched; the stamping already covers `/static/vendor/*` |
| `internal/web/static/js/net.js` | kept + one new export (`connectionSource`, §E.5) |
| `internal/web/static/js/api.js` | verbatim |
| `internal/web/static/js/logos.js` | + a `shell` mark |
| `internal/web/static/js/combobox.js` | verbatim |
| `internal/web/static/js/voice.js` | verbatim |
| `internal/web/static/sw.js` | mechanism verbatim, `SHELL` list updated |
| `internal/web/static/css/app.css` | palette and layout laws verbatim; chat-specific rules removed, terminal/key-bar rules added |
| `e2e/harness.mjs` | the boot/assert/report machinery kept; the fake-CLI build replaced (§H.2) |
| `docs/screenshot-tunnel.png` | hand-made and not regenerated by `shots.mjs`; keep the file |

**One warning for any package that deletes by pattern:** the repository contains git-ignored
`.claude/worktrees/agent-*` checkouts of itself. A `find`-based delete must exclude `.claude/`, or
it will delete files out of another agent's working tree.

---

# H. Testing

## H.1 Go unit tests — against real tmux, with isolated sockets

**Decision: no fake tmux.** A fake tmux would test our idea of tmux, and every load-bearing fact in
this spec (the window-style colour answer, per-client sizing, `manual` sizing, `pane_dead_status`,
`pipe-pane` semantics, hook scoping) is a fact about the real one — and several of them contradict
what the names suggest. Tests use the real binary on an isolated socket and skip when it is absent.

```go
// internal/termux/testing_test.go
func requireTmux(t *testing.T) string {
    t.Helper()
    bin, err := exec.LookPath("tmux")
    if err != nil { t.Skip("tmux is not installed; skipping the substrate tests") }
    if v, ok := parseVersion(run(bin, "-V")); !ok || v.Less(3, 3) {
        t.Skipf("tmux %s is older than 3.3", v)
    }
    return bin
}
// newLab returns a Manager on a private socket under t.TempDir(), and
// t.Cleanup kills that server. Never touches the user's tmux.
func newLab(t *testing.T) *Manager
```

Required tests in `internal/termux`:

| test | asserts |
|---|---|
| `TestConfIsApplied` | `show -s escape-time`, `show -sg default-terminal`, `show -gw remain-on-exit`, `show -g history-limit`, `show -g window-style` match the generated conf. Guards the option-scope trap. |
| `TestManualPolicyIsPerWindow` | under the `manual` policy, create **three** sessions in a row; all three are alive afterwards, `show -w -t soc_<id> window-size` is `manual` for each, and `show -gw window-size` is still `latest`. Three, not one: a global `window-size manual` lets the *first* session succeed and kills the server on the second, so a one-session test would pass while the product crashes. |
| `TestNoGlobalWindowSizeAnywhere` | a grep of the built tree finds no `set -g window-size` / `set-window-option -g window-size` command; the Manager only ever issues `setw -t <session>` |
| `TestBadConfFallsBack` | a deliberately poisoned conf makes the first `new-session` fail; the guard rewrites a minimal conf and the retry succeeds |
| `TestCreateAttachEcho` | create a `sh` session, attach a viewer, write `echo hello\n`, read `hello` back within 3 s |
| `TestAttachRedrawsCurrentScreen` | write output, detach, attach a second viewer, assert its first 4 KiB contains the text — the "no replay of megabytes" guarantee |
| `TestTwoViewersDifferentSizes` | 100×30 and 60×20 attach; each gets its own scroll region (`ESC[1;30r` / `ESC[1;20r`) |
| `TestManualSizingIgnoresTyping` | under the `manual` policy, keystrokes on either viewer never change `#{window_width}x#{window_height}`; only `Manager.Resize` does |
| `TestSizeOwnerHandover` | the owning viewer disconnects; the window resizes to the remaining viewer; with no viewers the size is unchanged |
| `TestOSCAnsweredWithNoClient` | a pane program queries OSC 11 with **`list-clients` empty** and receives `rgb:ffff/ffff/ffff` from the window style. This is the test the white background rests on. |
| `TestOSCRespondsWhite` | the same query with a viewer attached still yields white, and `?996n` yields `?997;2n` |
| `TestPaneDeathIsReported` | `sh -c 'exit 7'` ⇒ `state='exited'`, `exit_status == 7`, via the global hook |
| `TestDeadPaneKeepsLastScreen` | with `remain-on-exit-format ''`, a client attaching after the pane died sees the last screen, not "Pane is dead" |
| `TestPipePaneWithoutToggle` | calling the journal `pipe-pane` three times leaves `#{pane_pipe}` = 1 every time (the `-o` toggle trap) |
| `TestJournalRotatesWhileRunning` | a session emitting more than `--max-bytes` rotates in place, keeps one old file, and loses no bytes across the boundary |
| `TestJournalSinkRestartAppends` | a sink killed and re-attached mid-session appends rather than truncating (`O_APPEND`), because a `pipe-pane` without `-o` replaces a running sink |
| `TestCreateFailureMarksRowFailed` | a `new-session` that fails leaves the row at `state='failed'` with tmux's stderr in `fail_reason`, never in `starting` |
| `TestAdoptAfterRestart` | build a Manager, create a session, drop it, build a new one; `Adopt` finds it and marks it running |
| `TestAdoptDetectsReboot` | kill the tmux server behind the Manager's back; after two consecutive failed polls `Adopt`/the poller marks the row `needs_resume` |
| `TestTransientPollFailureDoesNotResume` | one failed poll followed by a good one leaves every session `running` |
| `TestOrphanSessionIsAdopted` | a `soc_*` session with no store row gets a "Recovered session" row and is **not** killed |
| `TestSessionSurvivesManagerKill` | `SIGKILL` the owning process; the session and pane are alive afterwards and `Adopt` re-adopts them. The comment must state that this proves survival of a **process** kill only, not of a cgroup kill — that is what `KillMode=process` and `systemd-run --scope` are for, and it is verified by hand on a systemd machine (§A.12). |
| `TestDetachClientExitsZero` | `detach-client` ⇒ exit 0, session alive |
| `TestGlobalHooksFireForEverySession` | `pane-died` carries `#{session_name}`, `session-closed` carries `#{hook_session_name}`, for a session created after the hooks were set |
| `TestShellQuoteRoundTrip` | `'`, space, `$`, backtick, newline survive `sh -c` byte-identically |
| `TestRingReplay` | `Ring.Since` returns exactly the missing bytes; an over-old seq returns `false`; `Wait` wakes on `Append` |
| `TestSlowViewerDoesNotGrowMemory` | a viewer that never reads leaves the ring at its fixed size and does not block the PTY reader |
| `TestResponderStraddle` | an OSC query split across two `Filter` calls is answered exactly once |

`internal/harnesses` tests need **no** tmux and **no** CLI:

| test | asserts |
|---|---|
| `TestClaudeArgvHasSessionID` | `--session-id <uuid>` present, `--fallback-model` absent |
| `TestClaudeResumeArgv` | `--resume <uuid>`, and `--session-id` absent |
| `TestClaudeRestartNeverReusesSessionID` | a restart whose transcript exists emits `--resume`; one whose transcript is gone emits `--session-id` with a **new** uuid, never the old one. Claude refuses a reused id outright (`Error: Session ID … is already in use.`), so this is a crash the test must prevent. |
| `TestClaudeSettingsOnlyVerifiedKeys` | the generated settings JSON contains only the keys in §C.5 and never `askUserQuestionTimeout` or `autoContinueAtUsageLimit` unless the admin set them |
| `TestCodexTrustLevelQuoting` | a cwd containing a space and a dot produces `-c projects."/a b/c.d".trust_level="trusted"` |
| `TestCodexArgvHasStrictConfig` | present, and none of the removed flags (`--full-auto`, `--yolo`, `--json`, `--color`, `--model-provider`, `--skip-git-repo-check`) is ever emitted |
| `TestCodexApprovalOnFailureGoesThroughConfig` | `on-failure` becomes `-c approval_policy=…`, never `-a on-failure` |
| `TestCodexVerifyPrefersRolloutGlob` | with the rollout file present but no sqlite, verify returns true |
| `TestOpenCodePositionalCwdIsLast` | the project path is the final argv element, after every flag |
| `TestOpenCodeNeverPassesPrintLogs` | |
| `TestOpenCodeServerPasswordIsSet` | `OPENCODE_SERVER_PASSWORD` is present, 32 bytes of entropy, different per session, and the discoverer sends the matching `Authorization` header |
| `TestGeneratedFilesAreValid` | the Claude settings JSON, the OpenCode `tui.json` and `OPENCODE_CONFIG_CONTENT` parse, and the deep-merge of the raw override applies |
| `TestResolveWorkdir` | dynamic containment, `..` rejection, the blocked-path list |
| `TestEnvAlwaysCarriesWhite` | `COLORFGBG=0;15`, `COLORTERM=truecolor` for all four |

`internal/store`: `TestMigrationDropsChatsAndRenamesSessions` (build a v2 database by hand,
migrate, assert `chats` is gone, `logins` exists with its rows, `kv` survived, `user_version == 3`)
and the `client_id` idempotency.

`internal/server`: the lifecycle tests in WP4, the workspace-rule rejections, and the WebSocket
tests in WP5.

`internal/web/embed_test.go`: keep every existing invariant, retargeted; add the vendor-in-SHELL
assertion (§E.9).

Everything must pass under `go test -race ./...`.

## H.2 Fake CLIs for e2e

The existing `internal/harness/fakes` binaries speak the old protocols and are deleted. The new
fakes are **PTY TUIs**, not protocol speakers, and they need no API access at all.

Location: `e2e/fakebin/faketui/main.go` — one Go program installed under three names. The Shell
harness needs no fake: the real `/bin/sh` is the thing under test.

`faketui` behaves like an interactive TUI:

1. On start it writes the OSC probes its real counterpart writes (`OSC 10;?`, `OSC 11;?`) and
   **records what came back** within 300 ms. It prints one banner line:
   `FAKE <name> theme=<light|dark|unknown> cwd=<cwd> model=<--model value> argv=<n>`.
   The `theme=` token is what the light-theme scenario asserts — it proves the window-style
   mechanism works end to end, through tmux, the PTY and the WebSocket.
2. It writes its argv and full environment to `$FAKE_LOG` as one JSON object per launch, so a
   scenario can assert the exact flags the launcher built without parsing a screen.
3. It reads lines from stdin and echoes each as `you said: <line>`.
4. Special commands: `/exit <n>` (exit with that status), `/spin` (200 lines fast, for the ring),
   `/alt` (enter the alternate screen and paint a box), `/id` (print its session id).
5. Session-id emulation, per name:
   - as **claude**: reads `--session-id <uuid>` from argv, refuses with
     `Error: Session ID <id> is already in use.` if a transcript for it already exists (mirroring
     the real binary), and otherwise writes `$CLAUDE_CONFIG_DIR/projects/fake/<uuid>.jsonl`.
     With `--resume <uuid>` it requires that file to exist.
   - as **codex**: after the first line of input, writes
     `$CODEX_HOME/sessions/2026/09/02/rollout-<ts>-<uuid>.jsonl` whose line 0 is a real
     `session_meta` record with the right `cwd` and `originator:"socrates"`, plus a
     `state_5.sqlite` with a `threads` row. This exercises the real watcher, including the
     verified "nothing exists until a user turn" timing.
   - as **opencode**: binds the `--port` it was given, requires HTTP Basic auth against
     `OPENCODE_SERVER_PASSWORD` (401 + `WWW-Authenticate` without it), serves `GET /session` and
     `POST /session`, and writes an `opencode.db` with a `session` table so `VerifyCLISession`
     works. `--session <unknown>` exits non-zero immediately, like the real one.

This is more work than an echo script, and that is the point: **the discovery and resume paths are
the parts most likely to be wrong, and they are unreachable without a fake that tells the truth
about where a CLI writes its state and how it refuses.**

`harness.mjs` changes:

- `binaries()` builds `faketui` once and links it as `claude`, `codex`, `opencode` in a temp bin
  dir prepended to `PATH`. The Go-overlay trick (`ADAPTER_SRC`, `overlay.json`) is **deleted**:
  there is no adapter registry any more, so `socrates` is built plainly. The `SCRIPT` /
  `LONG_SCRIPT` exports go with `internal/harness/fakes/script/`.
- `spawnServer` additionally sets `CODEX_HOME=<data>/codex`, `XDG_DATA_HOME=<data>/xdg`,
  `CLAUDE_CONFIG_DIR=<data>/claude`, `FAKE_LOG=<data>/fake.log`, and keeps
  `SOCRATES_WORKSPACE_ROOT`, `SOCRATES_PIPER_DIR`, `HOME=<data>`.
- `s.stop()`'s leak assertion changes from "no agent-host process" to **"no `soc_*` tmux session
  and no tmux server on `<data>/tmux.sock`"**, checked with `tmux -S <data>/tmux.sock
  list-sessions` (a non-zero exit meaning "no server" is the pass). The backstop becomes
  `tmux -S <data>/tmux.sock kill-server`.
- New export `mockOpenRouter(context, {text})` — a `context.route('**/api.openrouter.ai/**')`
  handler returning a fixed transcription, for the dictation scenario. The old suite had no
  OpenRouter mocking.

## H.3 e2e scenarios

`e2e/run.mjs` keeps its shape: `ALL` is a table of `[name, title, fn, opts?]`, bare scenario names
on the command line select a subset, `finish()` prints the table. Always
`waitUntil: 'domcontentloaded'`, never `networkidle`.

| # | name | what it proves | WP |
|---|---|---|---|
| 1 | `createshell` | the sheet creates a Shell session; the prompt appears; `echo hi` shows `hi` | WP6 |
| 2 | `typeandsee` | keystrokes reach the pane and output comes back; the journal holds the same bytes | WP6 |
| 3 | `reloadkeepsscreen` | type, reload, the same screen returns without a full clear | WP6 |
| 4 | `pages` | `/`, `/admin`, `/login`, `/setup` are clean (no console errors) at 390×844 and 1280×720 | WP6 |
| 5 | `keybar` | <!-- rev 4 --> no device gets the bar unasked; **Show key bar** in the session menu puts it up and the answer survives a reload; `Esc`, `^C`, arrows and the sticky `Ctrl` send the right bytes | WP7 |
| 6 | `chat-dictate` | <!-- rev 5 --> with `mockOpenRouter`, the **Speak** pill in `.chat-head` opens the recording sheet with its level meter and its two 56px endings, **Send** and **Cancel**; a sent one is transcribed, submitted with `auto:true` and answered out loud, and it turns the loudspeaker switch on; a cancelled one costs nothing; the switch turned off by hand makes the next question `auto:false` and silent. Replaces `dictation`, which typed into a field that no longer exists | ACTIVITY.md |
| 7 | `offlineonce` | offline, type 20 characters, online: each appears **exactly once** in the pane and in `journal.raw` | WP7 |
| 8 | `sigtermreattach` | `SIGTERM` the server mid-session, restart on the same port and data dir, reattach; the pane still holds what was typed and the state is `running` | WP7 |
| 9 | `takeover` | open the same session in a second tab with the same `viewer` id; the first socket closes within 1 s and the second one works | WP7 |
| 10 | `exitoverlay` | `/exit 7` → the overlay, the status behind the `infoTip`, and **Restart** brings the session back | WP7 |
| 11 | `adminoptions` | set one option in every group of every harness, save, reload, values round-trip; start a session and `FAKE_LOG` carries the corresponding flags | WP8 |
| 12 | `tmuxinstaller` | with a stubbed package manager on PATH, the installer streams lines into the admin page and the result survives a reload | WP8 |
| 13 | `createclaude` | `FAKE_LOG` shows the right cwd and model and `--session-id <uuid>` matching the store | WP9a |
| 14 | `createcodex` | `FAKE_LOG` shows `--strict-config` and the `trust_level` override; after one input line `cli_session_id` becomes non-empty within 20 s | WP9a |
| 15 | `createopencode` | `--port` was passed, `OPENCODE_SERVER_PASSWORD` was set, and the discoverer found the id over authenticated HTTP | WP9a |
| 16 | `rebootresume` | `tmux -S <data>/tmux.sock kill-server` behind the server's back, reload; the state went to `needs_resume`, the session came back with a **resume** argv in `FAKE_LOG`, and the banner is shown and dismissible | WP9a |
| 17 | `twoviewers` | two pages on one session: both see the same output; the second one's attach shows the resize notice on the first, **once** | WP9b |
| 18 | `backpressure` | `/spin`, then the pane's final content matches the fake's own record of what it printed — no holes | WP9b |
| 19 | `deletekeepsdir` | deleting kills the tmux session, removes the row, and **leaves the working directory** | WP9b |
| 20 | `recoveredsession` | create a `soc_*` session by hand on the socket, restart Socrates, assert it appears as "Recovered session" and was **not** killed | WP9b |
| 21 | `lighttheme` | the fake's banner reads `theme=light`; the terminal's computed background is `rgb(255,255,255)`; every colour in `LIGHT_THEME` has ≥ 4.5:1 contrast against white; a screenshot of a real Codex session is inspected by eye to confirm `tui.theme` applied | WP10 |
| 22 | `design` | white surfaces, one `agentMark` per harness reference, every version/path string only inside a `.tip-bubble`, and no animation restarting on a re-render (`getAnimations()[0].currentTime` across a state change) | WP10 |

<!-- rev 4 --> The table is this revision's twenty-two; `ALL` in `e2e/run.mjs` is the live list,
and the scenarios ACTIVITY.md added — `daygroups`, `notify`, `chat-text`, `chat-dictate`,
`no-overlap`, `session-title`, the `activity-*` set, `status-*` and `agent-run` — are specified
there. `audio-mode` and `dictation` were deleted with the controls they exercised.

`liveclaude` is kept, gated on `SOCRATES_LIVE_AGENTS=1`, and becomes `livesession`: start a real
Claude Code session, type `/status`, assert something rendered, delete it. It must not spend tokens
on a model turn.

## H.4 CI, image and deployment

`.github/workflows/ci.yml`:

- **`test` job**, matrix `[ubuntu-latest, macos-latest]`, plus a new step before `Test`:
  ```yaml
  - name: Install tmux
    run: |
      if [ "$RUNNER_OS" = "Linux" ]; then
        sudo apt-get update -qq
        sudo apt-get install -y --no-install-recommends tmux ncurses-term
      else
        brew list tmux >/dev/null 2>&1 || brew install tmux
      fi
      tmux -V
  ```
- **`windows` job** stays **build-only** (`go vet ./...`, `go build ./...`). The terminal feature is
  **unsupported at runtime on Windows**: `internal/termux/pty_windows.go` returns
  `termux.ErrUnsupported`, the server answers
  `503 Terminal sessions need tmux, which this build does not support on Windows.` on
  `POST /api/sessions` and the WebSocket, and `main.go` logs one clear line at start-up. Every
  `internal/termux` test carries `//go:build !windows`; the harness-argv tests do not and must pass
  on Windows.
- **No `e2e` job.** `make e2e` needs a Chromium that CI does not cache and would triple CI time; the
  suite stays a local gate, as today, and the README says so. What CI gains is the tmux-backed
  `internal/termux` tests, which are the substrate's real regression net.

`Makefile`: `check` keeps `fmt-check vet tidy-check race build`; `e2e` keeps its guard; add
`vendor-xterm` (§E.1).

`Dockerfile`: add `tmux` and `ncurses-term` to the existing apt line —
`ca-certificates curl git ripgrep tmux ncurses-term` — so the auto-installer never runs in the
image, and add `tini` as the entrypoint (or document `--init`) so a reparented tmux cannot become a
zombie when Socrates is PID 1 (§A.12).

`deploy/socrates.service` ships with `KillMode=process` (§A.12) and the README explains, in one
short section, the two deployment shapes: under systemd a Socrates restart keeps sessions alive; in
Docker a container restart is a reboot and takes the resume path.

---

# I. Work packages

Eleven packages. Each must leave `go build ./... && go vet ./...` green and its own tests passing,
and is reviewed by a Fable agent against this document before it is merged. A package that does not
build is rejected without further review.

Each package's scope names the files it owns. **A package must not edit a file owned by a later
package**; if it needs to, that is a dependency error to report rather than work around.

---

### WP1 — Store, config and the migration
**Depends on:** nothing.
**Scope**
- `internal/store/store.go`: new schema (§B.3), `migrate()` with the backup and the v2→v3 cut,
  `PRAGMA user_version`.
- `internal/store/sessions.go`: the `Session` type and its methods (§B.4).
- `internal/store/logins.go`: the renamed auth methods.
- Delete `Chat`/`Message`/`Run`/`Step` and `RecoverRuns`.
- `internal/config/config.go`: `WorkspaceSettings`, `TerminalSettings`, `HarnessSettings` and the
  four option structs (§C.9–C.12) with their defaults; delete `AgentSettings`/`AgentsSettings`;
  keep `OpenRouter`, `Voice`, `Tunnel`, `ModelPick`, `KnownEfforts`, `NormalizeEffort` verbatim.
  `TerminalSettings` has **no** `fixed_cols` / `fixed_rows`: under the `manual` policy Socrates
  sizes the window to the current viewer (§A.7), so a fixed size is a setting with no meaning. The
  fallback when nothing is connecting is the 120×40 constant in §A.5.
- To keep the tree building, WP1 also deletes `internal/server/api.go`,
  `internal/server/agents.go` (including `handleModels` and the `/api/models` route),
  `internal/engine`, `internal/agenthost` (including `internal/agenthost/hosttest/`) and the
  `agent-host` subcommand in `main.go`.
**After WP1** the app serves login, setup, admin settings and health, and nothing else.
**Acceptance**
- `go build ./... && go vet ./... && go test -race ./...` green.
- `TestMigrationDropsChatsAndRenamesSessions` passes against a hand-built v2 database.
- Starting the binary against an old data directory logs the backup path and comes up.

---

### WP2 — The tmux substrate
**Depends on:** WP1.
**Scope:** `internal/termux/` in full — `tmux.go`, `conf.go`, `manager.go`, `session.go`,
`pty_unix.go`, `pty_windows.go`, `journal.go` (including the `journal-sink` subcommand in
`main.go`), `osc.go`, `ring.go`, `shellquote.go`, `supervise.go`, plus tests.
`internal/harnesses/plan.go` (types only). Add `github.com/creack/pty` to `go.mod`.
Ship `deploy/socrates.service`.
**Acceptance**
- Every `internal/termux` test in §H.1 passes with real tmux, and skips cleanly when tmux is absent
  (verify with a `PATH` that hides it).
- `TestOSCAnsweredWithNoClient`, `TestManualPolicyIsPerWindow`, `TestNoGlobalWindowSizeAnywhere`,
  `TestManualSizingIgnoresTyping`, `TestPipePaneWithoutToggle`, `TestOrphanSessionIsAdopted` and
  `TestSessionSurvivesManagerKill` pass. These encode the facts that contradict the obvious reading
  of tmux's documentation and are the package's reason to exist.
- No test touches `~/.tmux.conf` or a socket outside `t.TempDir()`.

**Checklist — easy to get wrong, all verified:**
- [ ] The Manager owns the sizing policy (`setw -t` + `resize-window`) and its tests; WP5 owns the
      `size` frame and WP9b the client notice. Do not build the frame here.
- [ ] **No code path writes a global `window-size`**, in the conf or live. `setw -t soc_<id>` only.
- [ ] `requireTmux` skips below **3.3**, not 3.0 (§F.1 explains why).
- [ ] `journal-sink` opens with `O_APPEND` and tolerates a mid-file restart; `Adopt` re-issues
      `pipe-pane` only when `#{pane_pipe}` is 0.
- [ ] `pipe-pane` is issued **without** `-o` (it is a toggle).
- [ ] A failed `new-session` sets `state='failed'` with tmux's stderr in `fail_reason`.
- [ ] `supervise.go` uses `tini`/`--init` for the PID-1 case and installs **no** in-process
      `SIGCHLD` reaper — one would race `os/exec`'s `Wait` on our own children.
- [ ] `TestSessionSurvivesManagerKill`'s comment says it does not prove the cgroup case.

---

### WP3 — Harness launchers and the e2e fakes
**Depends on:** WP1, WP2.
**Scope:** `internal/harnesses/` — `harness.go`, `shell.go`, `claude.go`, `codex.go`,
`opencode.go`, `codex_discover.go`, `opencode_discover.go`, `workdir.go`, `catalog.go`,
`claude_models.go`, `codex_models.go`, `opencode_models.go`; retarget `internal/catalog`.
Delete `internal/harness/` entirely (including `fakes/` and `fakes/script/`).
**Also in scope, because it mirrors the launchers and every later package needs it:**
`e2e/fakebin/faketui/main.go` and the `harness.mjs` changes in §H.2 (fake build, env, the tmux leak
assertion, `mockOpenRouter`).
**Acceptance**
- Every `internal/harnesses` test in §H.1 passes; no test starts a real CLI.
- `TestCodexTrustLevelQuoting`, `TestClaudeRestartNeverReusesSessionID` and
  `TestOpenCodeServerPasswordIsSet` pass.
- `faketui` under each of its three names is driven from a Go test through a real tmux pane, and
  `FAKE_LOG` shows the expected argv.
- Two manual checks, recorded in the PR description: (a) run each real CLI once in a scratch tmux
  session with the generated argv and confirm it starts without a blocking prompt; (b) run one
  Claude `AskUserQuestion` round-trip with the generated settings file and confirm it is not
  auto-dismissed. **(c) Verify where Claude persists its theme preference** by running `/theme`
  once and diffing `~/.claude.json`; record the result in a comment in `claude.go` and enable or
  drop the pin accordingly (§C.1.2).
- Unauthenticated `GET /session` against a running `faketui`-as-opencode returns 401; authenticated
  returns 200. (Confirmed against the *real* OpenCode TUI as well: no password → 401 +
  `WWW-Authenticate: Basic realm="Secure Area"`, wrong password → 401, right password → 200, and
  an unauthenticated `POST /tui/submit-prompt` → 401 [V].)

**Checklist:**
- [ ] The Claude theme-key verification (§C.1.2) belongs to **this** package, not WP4.
- [ ] `faketui` decides `theme=light` from the **OSC 11** reply alone. It must not require an
      `ESC[?996n` answer: tmux 3.3a, the Docker base image, does not send one.

---

### WP4 — Session HTTP API
**Depends on:** WP1–WP3.
**Scope:** `internal/server/sessions.go`, `internal/server/harnesses.go`, the routes in `server.go`,
the `Adopt` call in `main.go`, the `tmux-hook` subcommand, `<data>/hook.sock`.
**Includes `Manager.Ensure` and the resume flow (§C.8) and `POST /api/sessions/{id}/restart`** —
without them the API cannot open a `needs_resume` session, so they cannot wait for a UI package.
No WebSocket yet.
**Acceptance**
- Go tests drive the full lifecycle over `httptest`: create each of the four kinds against the
  fakes on PATH, list, rename, archive, delete; the tmux session appears and disappears.
- Idempotency: two `POST /api/sessions` with the same `client_id` produce one row.
- Workspace rules are enforced server-side (custom refused when disallowed; a preset outside the
  list refused).
- Adopt: restart the `Server` in-process and assert a running session is re-adopted, and that a
  hand-made `soc_*` session is recovered rather than killed.
- `Ensure` on a `needs_resume` session relaunches with the resume argv and sets `resumed`.

**Checklist:**
- [ ] `POST /api/sessions` accepts optional `cols`/`rows` in the body and passes them to `Create`;
      absent, the 120×40 constant applies.
- [ ] A `Create` that fails surfaces as a `failed` row with tmux's stderr, not as a 500 with no
      trace.

---

### WP5 — The WebSocket transport
**Depends on:** WP4.
**Scope:** `internal/server/ws.go`, the ring-pulling writer, viewer state and the 90 s grace,
takeover, input dedupe, ping/pong, the origin check, `SameSite=Strict`.
Add `github.com/coder/websocket` to `go.mod`.
**Acceptance**
- A Go test using `websocket.Dial` against `httptest.NewServer`: connect, receive `hello`, type,
  read the echo, drop the connection, reconnect with the same `viewer` and a `since`; the gap
  arrives exactly once and no redraw was sent.
- **Reconnect after the grace expired, with a persisted `input_seq` of 500, must let the user
  type**: `hello` carries `viewer_fresh:true`, the first frame at seq 501 is accepted, and the
  bytes reach the pane.
- A second handshake with the same `viewer` closes the first with 1012 within 1 s.
- Resending an already-acked `input_seq` writes nothing twice (checked in `journal.raw`); a gap
  produces an `input_ack` at the last accepted value and no write.
- A viewer that never reads leaves memory flat and does not block the PTY reader.
- **Reload with frames in flight**: with two unacked frames outstanding, reload the page; the next
  keystroke is delivered and nothing is duplicated. This is the ordinary phone case and the one the
  naive persisted-counter design dead-locks.
- A foreign `Origin` is refused with 403.

**Checklist:**
- [ ] The client persists **no** input counter; it anchors to `hello`'s `input_ack` every time.
- [ ] Held frames are renumbered contiguously from `input_ack + 1` before being resent.
- [ ] Size ownership passes on **socket loss**, not on PTY-grace expiry — a dropped phone must not
      pin a laptop's window for 90 s (§A.7).

---

### WP6 — Frontend shell: page, terminal, transport
**Depends on:** WP5.
**Scope:** `internal/web/static/index.html`, `js/session.js`, `js/term.js`, `js/harnesses.js`,
`js/logos.js` (+ shell mark), `js/net.js` (+ `connectionSource`), `sw.js`, the vendored xterm files
and `scripts/vendor-xterm.sh`, `Makefile` `vendor-xterm`, `THIRD_PARTY_LICENSES.md`,
`css/app.css` (terminal and session-list sections; remove the chat rules).
Delete `chat.js`, `markdown.js`, `models.js`.
**Also in scope:** the `e2e/run.mjs` skeleton (the `ALL` table, flags, `finish()`) and scenarios
1–4.
**Acceptance**
- `internal/web/embed_test.go` green, including the new vendor-in-SHELL assertion.
- Scenarios `createshell`, `typeandsee`, `reloadkeepsscreen`, `pages` pass.
- The terminal renders white and the vendored files load with `?v=` and `immutable`.

**Checklist:**
- [ ] `TermSocket`'s diagnostic method is `lag(seq)`, matching the `lag` frame in §D.2. There is no
      `ack(seq)`.

---

### WP7 — Mobile, offline and resilience
**Depends on:** WP6.
**Scope:** <!-- rev 4 --> `js/keybar.js` and its `localStorage` preference, the overlays
(`#termOverlay`), the `.stale` treatment, the `viewer_fresh` client behaviour and its toast, the
precache measurement. The composer, the line input, the dictation wiring in it and the draft
persistence were in this package's scope in revisions 1–3 and are now removed (§E.6); dictation
belongs to the chat panel (ACTIVITY.md §D.3).
**Acceptance**
- Scenarios `keybar`, `offlineonce`, `sigtermreattach`, `takeover`, `exitoverlay` pass, and
  `chat-dictate` in place of `dictation`.
- `offlineonce` is the centrepiece: it must prove exactly-once delivery, not merely that something
  arrived.
- The precache size is measured and written into the README.

---

### WP8 — Admin
**Depends on:** WP4 (settings shape), WP6 (page conventions).
**Scope:** `admin.html`, `js/admin.js`, `internal/server/admin.go`, `internal/termux/install.go`,
`/api/tmux` and the SSE installer stream, the diagnostics list, the live application of
`window-size`/`history-limit`/`mouse`.
**Acceptance**
- Scenarios `adminoptions` and `tmuxinstaller` pass.
- Every option in §C.9–C.12 is present, saves, reloads and is reflected in `FAKE_LOG` when a session
  starts.
- The installer's output survives a page reload (it is in kv), and the stream uses the existing
  `LiveStream`.

**Checklist:**
- [ ] The tmux minimum shown and enforced is **3.3**; 3.2a must report "too old", not "ok".
- [ ] The terminal card has no fixed-size controls; `window_size` is a three-way select only.

---

### WP9a — Harness lifecycle in the browser
**Depends on:** WP5, WP7.
**Scope:** the resumed banner and `POST /ack-resume`, the `needs_resume` overlay, the session
overflow menu (rename / archive / download scrollback / delete), the advanced disclosure in the
sheet.
**Acceptance:** scenarios `createclaude`, `createcodex`, `createopencode`, `rebootresume` pass.
`rebootresume` must show the banner and prove the **resume** argv, not just that a session came
back.

---

### WP9b — Multi-viewer and recovery
**Depends on:** WP9a.
**Scope:** the **client half** of the size policy — rendering the `size` control frame as the
resize notice — plus the ring-overflow resync path in the client and the recovered-session
presentation. The policy itself lives in the Manager (WP2) and the frame in the transport (WP5);
this package makes it visible and proves it end to end.
**Acceptance:** scenarios `twoviewers`, `backpressure`, `deletekeepsdir`, `recoveredsession` pass.
`twoviewers` must show the notice **once** per real size change and never on a keystroke.

---

### WP10 — Integration, docs, image, CI
**Depends on:** all of the above.
**Scope:** `README.md` (rewritten around sessions, tmux and the two deployment shapes),
`e2e/README.md`, `docs/screenshot-*.png` regenerated via `e2e/shots.mjs` (keep
`screenshot-tunnel.png`), `Dockerfile` (apt line + `tini` as the entrypoint, and **no** in-process
reaper), `.github/workflows/ci.yml` (tmux step,
the Windows note), `Makefile`, `THIRD_PARTY_LICENSES.md` final pass, the `design` and `lighttheme`
scenarios, and a full run of every scenario.
**Acceptance**
- `make check` green on Linux and macOS in CI; the Windows job builds.
- `node e2e/run.mjs` — every scenario passes, `SOCRATES_E2E_STRICT=1`.
- `lighttheme` includes the by-eye confirmation that Codex's `tui.theme` name actually applied
  (theme names are not validated at config load, so only a screenshot proves it).
- The final verification from DECISIONS.md, performed in a real browser and recorded: all four
  session types with fake CLIs on PATH, a Socrates restart mid-session, browser offline and back,
  two viewers, and a reboot-resume simulation.

---

## I.1 Dependency graph

```
WP1 ─ WP2 ─ WP3 ─ WP4 ─ WP5 ─ WP6 ─┬─ WP7 ─┬─ WP9a ─ WP9b ─ WP10
                                    └─ WP8 ─┘
```

WP7 and WP8 are independent of each other and may run in parallel once WP6 lands. WP9a needs WP7's
overlay conventions.

---

# J. Risks and mitigations

| # | risk | mitigation |
|---|---|---|
| J1 | **The Claude `~/.claude.json` theme key is unverified [MEM].** Writing a wrong key does nothing; writing the file badly could corrupt a user's global config. | WP3 verifies by running `/theme` once and diffing, and records the result in a comment. Until verified, the pin is not written. `COLORFGBG=0;15` (verified parser, [BIN]) plus `minimumContrastRatio: 4.5` are the primary defences and do not depend on it. Writes are atomic (temp + rename) and touch exactly one key. |
| J2 | **Codex's session id does not exist until a real user turn** ✅. A reboot before the first message loses the conversation. | There is nothing to lose before the first turn: the conversation is empty. The watcher runs for 15 minutes, the state is `pending` meanwhile, and the session tooltip says "not yet resumable" so the behaviour is visible rather than surprising. |
| J3 | **OpenCode `--session <unknown id>` exits immediately** [V]. A stale id turns "open the session" into a crashed pane. | `VerifyCLISession` is mandatory before an OpenCode resume, and "could not tell" degrades to a fresh session (§C.7). Covered by a test with a deliberately bogus id, and by `faketui`, which refuses the same way. |
| J4 | **`window-size latest` follows typing, not attachment** [V] — the naive policy would re-lay out the TUI on every alternation between two devices. | The default is `manual` with Socrates issuing every `resize-window` (§A.7), which is verified to ignore keystrokes. `latest` and `largest` remain admin choices with hints saying what they do. `TestManualSizingIgnoresTyping` pins it. |
| J5 | **A global `window-size manual` segfaults the tmux server on the *next* `new-session`** — in a conf file or set live, on 3.6 and 3.3a [V]. Set live it is especially treacherous: the first session works and the second takes every session down with it. | The global is never touched; the policy is a **per-window** option applied with `setw -t soc_<id>` after each create and re-applied in `Adopt` (§A.3, §A.7). `TestManualPolicyIsPerWindow` creates **three** sessions in a row so a one-session pass cannot hide the crash, `TestNoGlobalWindowSizeAnywhere` forbids the global command outright, and `TestBadConfFallsBack` keeps the start-up guard honest. |
| J6 | **`tmux-256color` is absent in a slim image** [A]. | Probe with `infocmp tmux-256color` at start-up and fall back to `screen-256color`; the Dockerfile installs `ncurses-term` and the installer does too. |
| J19 | **A too-old tmux passes as healthy.** The conf needs 3.3 (`allow-passthrough`, `remain-on-exit-format`) and 3.2 (`-e`, `extended-keys`); on 3.2a the conf errors, the start-up guard falls back to a minimal conf, and sessions launch without their environment while the admin page says "ok". | The floor is **3.3** in `requireTmux`, the `/api/tmux` response and the admin state (§F.1), with the reason recorded there. 3.3a is verified to do everything load-bearing [V]. |
| J7 | **A systemd or Docker restart kills the tmux server** with it, turning every restart into a reboot. | `deploy/socrates.service` with `KillMode=process`, `systemd-run --scope` where available, a SIGCHLD reaper at PID 1, `tini` in the image, and the README stating that a container restart is the reboot path. `TestSessionSurvivesManagerKill` proves the survival property (§A.12). |
| J8 | **xterm.js behaviour was never tested in a browser during research**; everything in `tmux-xterm.md` §5 beyond versions and file layout is [A]. | WP6's acceptance is browser-level, and `lighttheme` measures the computed background rather than trusting an option name. `allowProposedApi: true` is required for unicode11 and its absence throws loudly. |
| J9 | **iOS will not raise the keyboard** unless focus happens inside a real user-gesture handler [A]. | <!-- rev 4 --> The `⌨` button of the key bar is the path, and its `focus()` call is synchronous inside `touchend`/`click`. A code comment says why, so a future refactor cannot make it async. The line input that used to be the primary path on phones is gone (§E.6); what a phone would have typed into it, it asks the chat instead. |
| J10 | **iOS drops WebGL contexts** when a tab backgrounds [A]. | `onContextLoss` disposes the addon and falls back to the DOM renderer; the addon is loaded in a `try/catch`. |
| J11 | **`extended-keys` / `modifyOtherKeys` interop between tmux 3.6 and xterm.js 6 is untested.** | Default `extended_keys` to **off**; it is an admin switch with a warning, not a default. `CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT=1` is the escape hatch for Codex. |
| J12 | **The clean cut destroys existing chats.** | A `socrates.db.pre-v3.bak` copy is written first and its path logged. DECISIONS.md chose the clean cut explicitly; this makes it reversible by hand. |
| J13 | **Two new direct dependencies** in a repo that guards its dependency list. | Both are MIT-class and zero-dependency; `creack/pty`'s `go.mod` is three lines [V]. A verified Linux-only fallback exists for it (`golang.org/x/sys/unix` with `/dev/ptmx` + `TIOCSPTLCK`/`TIOCGPTN`, proven working). Both are recorded in `THIRD_PARTY_LICENSES.md`. |
| J14 | **The macOS/Alpine/dnf install commands were never executed** [A]. | The apt path is verified and is what the image and the CI runner use. The others show their exact command in the UI on failure. Do not claim verification in a comment. |
| J15 | **OpenCode's embedded HTTP server drives the agent with no auth by default** — any local process could `POST /session/{id}/shell`. | A per-session 32-byte `OPENCODE_SERVER_PASSWORD` (§C.7), Basic auth from the discoverer, an ephemeral loopback port, and `plan.json` at mode 0600. The tunnel never proxies that port. |
| J16 | **A CLI upgrade changes flags under us.** Codex 0.152.0 already removed `--full-auto`, `--yolo` and four more. | The admin card is built from the *installed* binary's discovery, the version is shown behind the `infoTip`, and `--strict-config` makes a stale Codex override fail loudly at launch with the message in `fail_reason` and the `failed` overlay. |
| J17 | **A transient tmux failure could mass-flip sessions to `needs_resume`** and trigger a wave of resumes. | The reboot case requires the socket to be absent or two consecutive failed polls 2 s apart (§A.9). `TestTransientPollFailureDoesNotResume` covers it. |
| J18 | **Input is not exactly-once across a Socrates crash.** | Stated as a bound rather than hidden (§D.6): within a viewer's lifetime it is exactly-once; across a crash or a >90 s outage it is at-most-once and the user is told with a toast. Nothing is ever duplicated. |
| J20 | **A page reload with frames in flight could dead-lock input.** A client that remembered its own counter would come back ahead of the server, and every subsequent keystroke would be a gap it could not resend from. | The client persists no counter and re-anchors to `hello`'s `input_ack` on every connect, renumbering held frames from there (§D.6). WP5's acceptance includes the reload-with-two-frames-in-flight case explicitly. |

---

## Appendix A: reviewer notes not adopted

Everything in Review 1 was adopted, with two deliberate narrowings, both recorded here so a later
reviewer does not read them as oversights.

1. **Finding 12's stronger form was adopted, its weaker form dropped.** The review offered either
   "resync by fresh attach instead of `refresh-client`" or "make the writer pull from the ring so
   holes cannot occur". §D.5 takes the second and keeps the first as the ring-overflow path, so the
   coalescing writer, the drop counter and the resync-after-loss branch are gone entirely rather
   than merely corrected.
2. **Finding 13's output `ack` is kept, not deleted, but is now purely diagnostic.** It is renamed
   `lag` and sent once a second. The admin "viewer lag" row it was proposed for was dropped after
   WP8 (§D.2); the frame itself stays, because it is the client's acknowledgement of what it has
   rendered and the server has nowhere else to learn that. `cseq` is gone.

## Appendix B: quick reference for an implementer

```
data directory layout
  <data>/socrates.db                 SQLite
  <data>/socrates.db.pre-v3.bak      one-time migration backup
  <data>/tmux.sock                   the Socrates-owned tmux server socket (0600)
  <data>/tmux.conf                   generated on every start; NEVER contains window-size
  <data>/hook.sock                   tmux-hook IPC, mode 0600
  <data>/workspaces/                 default workspace root (dynamic directories)
  <data>/sessions/<id>/plan.json     the resolved LaunchPlan, mode 0600 (carries a secret)
  <data>/sessions/<id>/journal.raw   the rotating output journal (+ journal.1.raw)
  <data>/sessions/<id>/claude-settings.json | tui.json | claude-debug.log
  <data>/voice/, <data>/bin/         unchanged

the commands that matter
  create  tmux -f <conf> -S <sock> -u new-session -d -s soc_<id> -x C -y R -c <cwd> -e K=V … -- <argv…>
  size    tmux -S <sock> setw -t soc_<id> window-size manual       # PER WINDOW, never global
          tmux -S <sock> resize-window -t soc_<id> -x C -y R
  attach  PTY(cols,rows) <- tmux -S <sock> attach -t soc_<id>
  journal tmux -S <sock> pipe-pane -t soc_<id> '<socrates> journal-sink …'   # NO -o
  hooks   tmux -S <sock> set-hook -g pane-died|session-closed …    # global, once, at start
  release tmux -S <sock> detach-client -t <client_tty>             # exit 0, session alive
  delete  tmux -S <sock> kill-session -t soc_<id>                  # only on explicit user delete

minimum tmux: 3.3 (allow-passthrough, remain-on-exit-format). 3.2a loads the conf with errors.

the six facts that contradict the obvious reading
  1. A GLOBAL window-size manual segfaults the server on the NEXT
     new-session — in a conf file or set live, on 3.6 and 3.3a.     → setw -t <session>, never -g.
  2. window-size latest follows TYPING, not attachment.           → manual, Socrates owns the size.
  3. pipe-pane -o is a TOGGLE, not a "toggle-on".                 → call pipe-pane without -o.
  4. A session-scoped session-closed hook NEVER fires.            → set both hooks globally, once.
  5. An explicit window-style makes tmux answer OSC 10/11 itself,
     with ZERO clients, beating any client's answer.              → that is the white background.
  6. Codex blocks on a directory-trust picker, and OpenCode
     exits on an unknown --session id.                            → pre-trust; verify before resume.
```
