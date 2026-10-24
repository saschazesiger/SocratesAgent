# 07 — Spike S4: does a per-chat agent host survive a service restart?

Date: 2026-10-23. claude 2.1.309 (`--model haiku` → `claude-haiku-5-5`), systemd 259, Go 1.27.3,
kernel 7.0. Question (REBUILD.md §2.1): does a holder process `socrates agent-host`, started in its
own systemd scope the way `internal/termux/tmux.go` starts tmux, keep a `claude -p` stream-json turn
alive across `systemctl restart|stop` of the parent service, and can the restarted server catch up
without loss? Also: host death, exactly-once sends, scope hygiene, re-exec into a new binary.

Evidence labels: **VERIFIED** = observed in a lab run in this session; **INFERRED** = my conclusion,
not directly observed.

The real `socrates.service` and `socrates-tmux.scope` were not touched. Everything ran in an isolated
replica (below) that was torn down at the end: no `socrates-spike*` units are left, `/run/socrates-spike`
is gone, and the spike's claude transcripts are removed.

## 0. Answer

**Yes, VERIFIED.** Across 6 server processes and 6 host processes (plus one re-exec)
of one chat, the server's copy of the journal was **contiguous 1..1893 and byte-identical** to the
host's journal, with 0 gaps and 0 duplicate deliveries. Mid-turn restarts and a 60 s stop never
interrupted a turn. Every `client_id` reached claude exactly once, including one whose ack was lost
in a server crash. Some conditions have to hold for this, and §8 turns them into changes for §2.1:

* The CLI child needs **`Pdeathsig=SIGKILL`**, forked from a **`runtime.LockOSThread`'d main
  thread**. Without it an orphaned claude finishes its turn into a dead pipe.
* Pdeathsig does **not** reach the CLI's tool processes. A dead host's scope stays active with
  orphans in it, so recovery is `systemctl stop socrates-agents-<chat>.scope`, and only after the
  host has been confirmed dead.
* Re-exec with the claude child kept works (same PID, same claude, same context). It is fiddly,
  though, and not needed for v1: **an idle host should park and resume instead** (§7).

## 1. The replica

Lab: `/tmp/claude-0/-root-SocratesAgent/d612e239-e189-4f08-9945-19100f29fc4c/scratchpad/s4-lab/`

| file | what |
|---|---|
| `main.go`, `go.mod` | the prototype, one binary `s4` with roles `server`, `host`, `ctl`, `verify` (~1080 lines) |
| `s4`, `s4-v2` | the same code built with `-X main.version=v1` / `v2` (v2 is the re-exec target) |
| `start-server.sh` | starts the server as transient `socrates-spike.service` |
| `lib.sh`, `t5tool.sh`, `t6.sh`, `t6b.sh` | the test drivers |
| `run/hosts/<n>/journal.jsonl` | host journals (evidence); `host.log`, `claude.stderr` |
| `run/server/<n>/recv.jsonl`, `outbox.jsonl`, `acked.jsonl` | the server's durable copy / outbox (evidence) |

The server is a transient service with **every** `[Service]`/`[Unit]` property of
`deploy/socrates.service` (systemd's resulting values VERIFIED with `systemctl show`):

```sh
systemd-run --unit=socrates-spike.service -p StartLimitIntervalSec=0 -p KillMode=control-group \
  -p Restart=always -p RestartSec=3 -p WatchdogSec=120 -p NotifyAccess=main \
  -p LimitNOFILE=65536:524288 -p OOMPolicy=continue -p TimeoutStopSec=60 \
  -p WorkingDirectory=/root -E HOME=/root -E LANG=C.UTF-8 -E PATH=/root/.local/bin:… \
  …/s4 server --dir …/run
```

The server pings `WATCHDOG=1` every 40 s like Socrates does. A `launch` creates the host the same
way `startScoped`/`scopeArgs` create tmux, and only under the same `INVOCATION_ID` + `ppid==1` guard
as `serviceScope()`:

```
systemd-run --scope --quiet --collect --unit=socrates-spike-agent-<n> --description=… -p OOMPolicy=continue \
            …/s4 host --dir run/hosts/<n> --sock /run/socrates-spike/<n>.sock --cwd run/work [--resume auto]
```

The host runs `claude -p --input-format stream-json --output-format stream-json --verbose
--include-partial-messages --replay-user-messages --setting-sources project,local --strict-mcp-config
--dangerously-skip-permissions --model haiku [--resume <sid>]` with a scrubbed env (no `ANTHROPIC_*`,
`CLAUDE_CODE_*`, `NOTIFY_SOCKET`, `WATCHDOG_*`, `INVOCATION_ID`; `IS_SANDBOX=1`). It appends every
stdout line to `journal.jsonl` as `{seq, at, kind:"cli"|"sent"|"proc", data}` and serves the socket.

Protocol (JSON lines): client `hello{since}` → host `hello{proto, version, host_pid, claude_pid,
last_seq, session_id}`, then `ev{seq,…}` for every seq > since, replay and live tail with no seam.
Commands: `send{client_id, text}` → `ack{client_id, seq, dup}`; `reexec{binary}`; `stop`.

The long turn used throughout: *"Write the numbers from 1 to 300, one per line, each followed by a
space and the number written out in English words. No other text."* That is about 320 journal
events (mostly `stream_event` deltas) over roughly 8 s.

## 2. Test 1 — `systemctl restart` mid-turn (VERIFIED)

| | |
|---|---|
| before restart | journal at 102 events, turn streaming, server PID 550266, host 550332, claude 550339 |
| restart | old server got SIGTERM and exited 0 (`KillMode=control-group` killed only the service cgroup); new server PID 550754 up **~20 ms** later |
| after | host 550332 and claude 550339 unchanged, still in `socrates-spike-agent-1.scope` |
| reconnect | `peer 1: resume from seq 103` → `hello since=103` → `first event after hello: seq 104` |
| turn | completed (`result`, last line "300 three hundred") ~6 s after the restart |
| check | `verify`: journal 421 = server copy 421, **contiguous 1..421, byte-identical: true**, dups 0, gaps 0 |

## 3. Test 2 — `systemctl stop` for 60 s mid-turn, then start (VERIFIED)

* Stopped at journal 487 (turn running). The host and claude kept running; the **turn finished
  while no server existed** (journal reached 817 at +20 s, with no further change by +60 s).
* A transient service is **unloaded on stop** (`LoadState=not-found`), so `systemctl start` fails
  with "Unit not found". The "start" was therefore the same `systemd-run` again. This is an artefact
  of the replica only; the real unit file stays loaded.
* New server: `resume from seq 489` (the last seq in its own `recv.jsonl`, read at startup)
  → `hello since=489` → first event 490 → **328 events caught up**. `verify`: 817 = 817,
  contiguous, byte-identical.

## 4. Test 3 — SIGKILL of the host mid-turn (VERIFIED)

### 4.1 With `Pdeathsig=SIGKILL` (the prototype's default)

```
PID     PGID    SID     PPID    COMMAND
550332  550332  550332  1       s4 host        ← reparented to PID 1: its launching server is long gone
550339  550339  550332  550332  claude         ← own process group (Setpgid)
== SIGKILL host at 12:29:08.53, journal=1339
claude alive after 0.3s? no
socrates-spike-agent-1.scope: Deactivated successfully.   (--collect: unit gone)
```

The socket **file stays behind** (`/run/socrates-spike/1.sock`), and the server's dials fail with
`connect: connection refused`, retried every 1 s. That error, plus the host PID being gone, is the
"host is dead" signal.

### 4.2 Without Pdeathsig (`--no-pdeathsig`, host 2): the "or not?"

Claude **does not die with the host**. It is reparented to PID 1, keeps generating, and **finishes the
whole turn writing into a broken stdout pipe**. The final text "…300 three hundred" is in claude's
own transcript at 12:29:56, but our journal stops at seq 56 at 12:29:48. It then exits on stdin EOF
(the research §1.2/exp13 behaviour), 4 to 8 s after the host died. Everything it produced in between
is lost to Socrates, and it could have kept running tools unobserved. **Pdeathsig is required.**

### 4.3 Tool processes are not covered by Pdeathsig

A host killed while claude runs a Bash tool (`python3 -c "import time; time.sleep(45)"`):

```
== scope before kill:              == after SIGKILL of host 560335: scope=active
560335 s4 host                     560673 ppid=1 /bin/bash -c source …/shell-snapshots/…
560343 claude -p …                 560674 ppid=560673 python3 -c import time; time.sleep(45)…
560673 /bin/bash -c …              (TasksCurrent=2)
560674 python3 -c …
```

Claude dies, and its tool's bash and python are orphaned to PID 1. They **stay in the scope, so the
scope stays active.** On relaunch the server sees the stale scope and runs `systemctl stop` on it,
which kills exactly those orphans, and then starts a new host under the same unit name (VERIFIED,
log `stale socrates-spike-agent-1.scope is active, stopping it`). A per-chat scope is what makes
this cleanup precise. In one shared scope the orphans could not be told apart from other chats.

The first attempt at this test hit a bug in my test script, and that exposed a real hazard: the same
stale-scope stop also **kills a live, healthy host** (VERIFIED). The real launcher must first prove
the host is dead: socket refuses or is missing, **and** the host PID from the journal is gone.

### 4.4 Resume with a new host (VERIFIED, matches research §5.2)

`launch {resume:"auto"}`: the new host reads the session id from its journal's `system/init`, then
runs `claude --resume <sid>`. The journal keeps numbering (`proc 1340: started … resume=cbf4c446…`).
The server reconnected with `since=1339` and got 1340 onward.

* Same session id. Claude's transcript now holds the user message of the killed turn followed by
  `assistant <synthetic> "No response requested."`. **The partial text (≈175 numbers) is in
  Socrates' journal but not in the model's context.** Asked "what was the last number you wrote in
  your previous answer?", it said: *"My immediately previous reply was just 'No response requested,'
  not the list; the last full list I wrote ended at '300 three hundred'"* (that is, the turn before).
* Re-sending an **old** `client_id` (`t4b`) to the *new* host gave `ack seq 833 dup=true`. The dedupe
  map is rebuilt from the journal, so exactly-once survives host restarts.
* Cold resume cost (tiny 1.9k-event chat): first `stream_event` 1229 ms after the send, against
  669 to 1048 ms for a message to a warm process. That is roughly **+0.2 to 0.5 s** (VERIFIED for
  this size; it grows with transcript size, INFERRED).

## 5. Test 4 — exactly-once send across a lost ack (VERIFIED)

The server writes the send to `outbox.jsonl` **before** sending. On every (re)connect it re-sends
every outbox entry that has no ack in `acked.jsonl`, with the same `client_id`. The ctl flag
`die_before_ack_ms` makes the server `os.Exit(1)` right after writing the send, and
`Restart=always` brings it back 3 s later.

| case | what happened | result |
|---|---|---|
| 4a: die 300 ms after the write | the server's reader goroutine had already consumed and stored the ack (`ack t4a seq 818`), so no resend was needed | once |
| **4b: die right after the write** | host journaled `sent` seq 833 and fed claude; server died; restarted server: `resume from seq 832, 1 unacked in outbox` → `re-sent unacked t4b` → `ack t4b seq 833 dup=true` | **once** |

Proof of "claude received it once": per `client_id`, exactly 1 `sent` journal event and exactly 1
`--replay-user-messages` echo (`isReplay:true` with the uuid derived from the `client_id`). Claude's
own transcript has exactly 1 `user` line with that uuid and 1 reply.

Remaining window (INFERRED, not tested): the host dies *between* journaling `sent` and writing it to
claude's stdin. The resend is then acked `dup` but claude never saw it. The fix is to treat the
replay echo (`isReplay` with that uuid) as delivery. A new host that finds a `sent` with no echo
after it re-delivers it to the resumed CLI.

## 6. Test 5 — the scope (VERIFIED)

`systemctl show` of `socrates-tmux.scope` (live, read-only) and `socrates-spike-agent-4.scope` are
**identical** in every placement and OOM property:

```
Slice=system.slice   ControlGroup=/system.slice/<unit>   OOMPolicy=continue   memory.oom.group=0
CollectMode=inactive-or-failed   KillMode=control-group   KillSignal=15   SendSIGKILL=yes
TimeoutStopUSec=1min 30s   Delegate=no   TasksMax=37194   MemoryMax=infinity
Requires/After=system.slice   Conflicts/Before=shutdown.target   PartOf/BindsTo= (none)
oom_score_adj: host 0, tmux 0, socrates main 0
```

So nothing ties the scope to `socrates.service`; only shutdown stops it.

* `systemctl status` of the scope lists the host and claude (and tool children) under
  `/system.slice/socrates-spike-agent-<n>.scope`, with per-chat Tasks, Memory and CPU. On exit:
  `Deactivated successfully. Consumed 3.728s CPU time over 2min 57.726s wall clock time, 166.7M
  memory peak.` Per-chat accounting comes for free.
* **Cleanup:** a host exiting normally (`stop` → stdin closed → claude exits 0 → host exits) leaves
  no unit behind (`--collect`), and the host unlinks its socket. SIGKILL leaves the socket file
  behind (§4.1), and a tool-orphan keeps the scope alive (§4.3).
* `systemd-run --scope` **execs** into the host, so the host is a *child of the server process*. The
  server reaps it with `go cmd.Wait()`; after a server restart PID 1 reaps it. No zombies were seen
  at cleanup.
* The host's env carries **`INVOCATION_ID=<the scope's own invocation id>`** (systemd-run sets it),
  and after a server restart its ppid is 1. That is exactly the `serviceScope()` test
  (`INVOCATION_ID != "" && ppid == 1`). The host must never reach that code path, and it must strip
  the variable from the CLI's env. `NOTIFY_SOCKET`/`WATCHDOG_*` must be stripped as well (the
  prototype does; `NotifyAccess=main` would also refuse them).

## 7. Test 6 — re-exec an idle host into a new binary, keeping claude (VERIFIED)

How the prototype does it:

1. Refuses while busy (`err: busy: a turn is running`, VERIFIED).
2. Stops the stdout reader with `SetReadDeadline(now)`, so the reader hands back any partial line
   (`leftover`, base64 in the handover).
3. Takes the journal lock for good and `fsync`s.
4. Clears `FD_CLOEXEC` on the two pipe fds, obtained through `SyscallConn().Control` rather than
   `Fd()`, which would flip them to blocking mode.
5. Closes the listener, which unlinks the socket; clients reconnect with `since`.
6. Calls `syscall.Exec(new binary, same args, env + S4_HANDOVER={fds, pid, leftover, busy})`.

The new image `os.NewFile`s the fds (they are still non-blocking, so the poller and deadlines work),
reloads the journal, waits on the child with `os.FindProcess(pid).Wait()`, and appends
`proc{state:"reexeced", from:"v1", to:"v2"}`.

```
== before: host=560761 exe=s4    claude=560768
host[560761 v1] 12:34:29.807160 re-exec into …/s4-v2 requested
host[560761 v2] 12:34:29.818606 adopted claude pid 560768 after re-exec from v1      ← 11 ms
== after:  host=560761 exe=s4-v2 claude=560768 claude-ppid=560761   fds 8,9 → the same pipe inodes
server: disconnected at seq 1843 → connected (host v2 …) hello since=1843 → first event 1844   ← 200 ms (retry tick)
"Which word did I ask you to remember?" → "walnut"      (asked before the exec: same live process, same context)
verify: contiguous 1..1860, byte-identical
SIGKILL of the re-exec'd host → claude alive=no         (Pdeathsig still armed after execve)
```

**The pitfall, verified with a negative control:** an `execve` issued from a thread *other* than the
one that forked claude kills claude. `de_thread` ends the forking thread, and Pdeathsig fires on
*thread* death: `proc 33 reexeced` → `proc 34 exited "signal: killed"` 15 ms later. The rule is
"fork and exec on the same OS thread", which means `runtime.LockOSThread()` in `init()` for the host
role, and doing both from the main goroutine.

**Recommendation:** feasible, but **do not build fd handover for v1**. Use the park path:

* The handover has to carry all the adapter's in-memory state (the `initialize` exchange and its SDK
  MCP registration, pending `control_request`s, partial-message assembly, `ask_user` bookkeeping),
  not just two fds. The prototype has none of that, and every such field is a re-exec bug waiting
  to happen.
* What it saves is ~0.2 to 0.5 s on the next message of an idle chat, once per update.
* The cost is a permanent constraint on the host's threading model (§4.1, §7).
* Parking has to exist anyway (§2.1) and is the same path as reboot recovery. "Re-exec an idle host"
  becomes **"park an idle host now"**: close stdin, claude exits 0, the host exits and the scope is
  collected (VERIFIED, `exited exit status 0`). The next message starts a new-binary host with
  `--resume`. Nothing is lost: the session continues and the journal keeps its seq.
* A running host keeps executing its old binary even after `/usr/local/bin/socrates` is replaced
  (the inode stays open), so nothing forces an exec.

One thing this changes in §2.1: **"idle" must mean** no turn running, no pending `ask_user`, **and**
no claude background task (Bash `run_in_background`, monitors) still running. Parking kills those
(INFERRED from process ownership; not tested). A turn blocked on `ask_user` is *busy*, and REBUILD
says it may wait ~27.8 h. So **hosts of the previous binary can live for a day after an update**,
and the server must keep speaking the previous host protocol version (N-1), not just "finish the
turn first".

## 8. Recommendation for REBUILD.md §2.1

Keep the design. It works as specified. Make these points explicit:

1. **Scope per chat**, `systemd-run --scope --quiet --collect --unit=socrates-agents-<chat>
   -p OOMPolicy=continue` (same OOMPolicy fallback as `startScoped`). §2's diagram says
   `socrates-agents.scope` (one shared scope); it should be per-chat, because per-chat scopes give
   per-chat accounting and exact orphan cleanup (§4.3, §6).
2. **CLI child:** `Pdeathsig=SIGKILL` + `Setpgid`, forked from the host's main goroutine, locked with
   `runtime.LockOSThread()` in `init()` (§4.1, §4.2, §7). The host's own stdout/stderr go to a file
   in the chat directory, not the service's journald stream. The host runs with `Setsid`.
3. **Dead-host recovery:** dial refused or socket missing **and** host PID gone → `systemctl stop
   socrates-agents-<chat>.scope` (reaps tool orphans) → remove the stale socket → if the last turn
   has no `result`, journal `turn.ended{outcome:"interrupted"}` (the UI keeps the partial text; the
   model has a synthetic "No response requested.", §4.4) → start a host with `--resume`. Never stop
   a scope whose host answers `hello` (§4.3).
4. **Server resume point** = the last seq it has *durably stored* (read back from its own copy at startup, §3), not one held in memory. The host journal
   is the source of truth. Replay and live tail are one pull cursor over the journal, with no
   per-subscriber buffers and no drop logic (§9 code).
5. **Exactly-once:** the server outbox is durable before the send and re-sent on every connect. The
   host journals `sent` and writes stdin under one lock, and rebuilds its dedupe map from the
   journal (works across host generations, §4.4). Add `--replay-user-messages` and treat the
   `isReplay` echo of the `uuid` (derived from `client_id`) as the CLI's delivery receipt, which
   closes the crash window in §5.
6. **Updates:** an idle host (no turn, no pending ask, no background task) is **parked**, not
   re-exec'd. A busy host keeps its old binary until it goes idle, which can be ~a day with an open
   `ask_user`, so the host protocol is versioned and the server supports N-1. fd-handover re-exec is
   a verified, documented optimisation for later (§7), not a v1 feature.
7. **Env hygiene:** strip `INVOCATION_ID`, `NOTIFY_SOCKET` and `WATCHDOG_*` from host and CLI env.
   A reparented host otherwise passes the `serviceScope()` test (§6).
8. **Socket path:** `sun_path` is 108 bytes. `<data>/run/chats/<32 hex>.sock` fits for a normal data
   dir, but check it at startup and fall back to `/run/socrates/chats/` (the scratchpad path of this
   lab was too long, which is why it used `/run/socrates-spike/`).

## 9. Key code paths of the prototype (reference)

Fork on the locked main thread (host role):

```go
func init() {
	if len(os.Args) > 1 && os.Args[1] == "host" {
		runtime.LockOSThread() // Pdeathsig is per *thread*; fork and execve must share it
	}
}
…
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
cmd.Stdin, cmd.Stdout = inR, outW // own os.Pipe()s, no exec.Cmd copy goroutines
cmd.Start()                       // called from the main goroutine
```

Replay + live as one cursor (no gap, no dup, a slow client only slows itself):

```go
next := hello.Since + 1
for {
	h.mu.Lock()
	for int64(len(h.events)) < next { /* exit if conn closed */ h.cond.Wait() }
	batch := h.events[next-1:] // append-only, elements immutable
	h.mu.Unlock()
	for _, ev := range batch { if write(ev) != nil { c.Close(); return } }
	next += int64(len(batch))
}
```

Idempotent send:

```go
h.mu.Lock(); defer h.mu.Unlock()
if seq, ok := h.sent[clientID]; ok { return seq, true, nil } // dup: ack again, never re-deliver
seq := h.appendLocked("sent", {client_id, uuid: uuidFor(clientID), text}) // journal first
h.stdin.Write(userLine(uuid, text))
```

Server side: `outbox.jsonl` append → write `send`. On every connect: `hello{since: last durably
stored seq}`, then re-send all un-acked entries. An `ack` is appended to `acked.jsonl` and removed
from the outbox. An event with `seq <= last` is counted as a duplicate, `seq != last+1` as a gap
(both stayed 0 in every run).

Re-exec handover (§7): `SetReadDeadline(now)` → leftover bytes; lock + fsync journal; `fcntl(fd,
F_SETFD, 0)` on both pipe fds via `SyscallConn().Control`; `ln.Close()`; `syscall.Exec(bin, args,
env+S4_HANDOVER)` from the main goroutine. New image: `os.NewFile(fd)`, `os.FindProcess(pid).Wait()`.

Launch (server):

```go
args := []string{"--scope", "--quiet", "--collect", "--unit=" + unit, "--description=…",
	"-p", "OOMPolicy=continue", bin, "host", "--dir", hostDir, "--sock", sock, …}
cmd := exec.Command("systemd-run", args...)
cmd.Stdout, cmd.Stderr = hostLog, hostLog
cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
cmd.Env = withoutNotifySocketAndWatchdog(os.Environ())
cmd.Start(); go cmd.Wait() // systemd-run execs into the host: reap it while we live
```
