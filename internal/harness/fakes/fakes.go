// Package fakes builds the three fake agent binaries used by the hermetic
// adapter tests and by the Playwright e2e suite.
//
// # The binaries
//
// Three Go programs live beside this file:
//
//	fakeclaude    a stream-json speaker on stdin/stdout   -> installed as "claude"
//	fakecodex     app-server JSON-RPC 2.0 over stdio      -> installed as "codex"
//	fakeopencode  an HTTP + SSE server behind Basic auth  -> installed as "opencode"
//
// [Build] compiles all three into one temporary directory under the names the
// adapters look for on PATH, so a test is just:
//
//	dir := fakes.Build(t)
//	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
//	t.Setenv("FAKE_SCRIPT", `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
//
// The same directory works outside `go test` — [BuildDir] puts the binaries
// anywhere, which is how the e2e harness gets a PATH for `socrates serve`.
//
// # FAKE_SCRIPT
//
// All three fakes are driven by one env var, FAKE_SCRIPT, holding a JSON array
// of steps, so a test describes the turn it wants instead of needing a new
// binary:
//
//	[{"do":"text","text":"Looking at it."},
//	 {"do":"tool","name":"Bash","input":"go test ./...","output":"ok\n","exit":0},
//	 {"do":"sleep","ms":200},
//	 {"do":"text","text":"All tests pass."},
//	 {"do":"end","outcome":"ok"}]
//
// The steps:
//
//	{"do":"text","text":…}                       one assistant text block, streamed in
//	                                             three chunks and then completed
//	{"do":"reason","text":…}                     one reasoning block
//	{"do":"tool","name":…,"input":…,
//	 "output":…,"exit":N}                        one tool call: started -> output -> finished
//	{"do":"subagent","name":…,"input":…,
//	 "output":…}                                 one subagent call: claude's Task tool, codex's
//	                                             subAgentActivity item. OpenCode reports no
//	                                             subagents on its stream, so fakeopencode emits a
//	                                             plain `task` tool call instead.
//	{"do":"ask"}                                 provoke the approval path: codex's
//	                                             item/commandExecution/requestApproval ServerRequest
//	                                             (FK-14) and opencode's permission.v2.asked, both of
//	                                             which block the script until the client answers.
//	                                             fakeclaude emits nothing — there is no approval
//	                                             path under --permission-mode bypassPermissions.
//	{"do":"sleep","ms":N}                        wait, emitting nothing
//	{"do":"end","outcome":"ok"|"error"|"retry",
//	 "error":…,"twice":…,"subagents":N}          end the turn
//	{"do":"hang"}                                never end the turn: keep serving interrupts,
//	                                             RPCs and HTTP, emit nothing else
//	{"do":"die","code":N}                        flush everything written so far, then exit(N)
//
// The script runs on *every* turn, from the first one (FK-1), so a two-turn
// test sets one script, sends twice and expects the same sequence twice.
//
// "outcome":"retry" is codex-only: it emits an error notification with
// willRetry:true followed by a normal turn/completed, so an adapter's
// "remember, do not end" rule is testable. "twice":true makes codex emit
// turn/completed twice and opencode emit step.ended{finish:"stop"} twice, 200
// ms apart, after the session has already left /api/session/active — two late
// triggers for closeTurn's sync.Once to swallow. "subagents":N overrides the subagent count claude
// reports in result.subagent_stats.spawned (it defaults to the number of
// subagent steps the turn ran).
//
// Each fake answers `--version` (and the `-v` / `-V` aliases) with a fixed
// line in its CLI's real format and exits 0, so the agent catalogue's
// discovery probe and the admin diagnostics row read sensibly:
//
//	claude    2.1.252-fake (Claude Code)
//	codex     codex-cli 0.152.0-fake
//	opencode  1.17.13-fake
//
// # fakeopencode's two event streams
//
// The real server splits its SSE traffic, and so does the fake:
//
//	GET /api/session/{id}/event   the session's durable events, replayed in
//	                              full on every connect. It NEVER carries a
//	                              *.delta frame.
//	GET /api/event                every session on the server, no replay.
//	                              This is where text.delta, reasoning.delta
//	                              and tool.input.delta are published, and it
//	                              also carries traffic for the server's own
//	                              internal title session (ses_internal0001),
//	                              so a client has to filter on
//	                              data.sessionID.
//
// Both are behind the same Basic auth and use the same framing.
//
// # fakeclaude's own env vars
//
// Two behaviours of the real CLI cannot be scripted, because they happen
// before or instead of a turn. Both are pinned by DESIGN.md rev 10 §10.1:
//
//	FAKE_INIT_AT_START=1   write system/init at process start. By DEFAULT the
//	                       fake writes nothing at all until the first user
//	                       line and init is the first line of the first turn,
//	                       which is what the real CLI does under
//	                       --input-format stream-json (E-1). The opt-in exists
//	                       so a test can prove an adapter tolerates both
//	                       orders rather than depending on either.
//	FAKE_GHOST_RESUME=1    make a --resume launch reproduce the measured
//	                       failure of resuming a session that does not exist
//	                       (E-2): no system/init, then after ~200 ms one
//	                       unprompted
//	                       result{subtype:"error_during_execution",
//	                       is_error:true, num_turns:0,
//	                       terminal_reason:"api_error"} whose result field and
//	                       whose stderr line are both "No conversation found
//	                       with session ID: <uuid>", then exit 1. num_turns:0
//	                       with no init before it is the discriminator.
//	FAKE_STATE_DIR=<dir>   where the consumed-ghost marker is written. The
//	                       ghost flag is consumed on first use per session id,
//	                       so the adapter's one-shot relaunch succeeds and the
//	                       whole R-3 path — relaunch, notice, replayed turn
//	                       with no second turn_started — is testable. With
//	                       this unset the flag is never consumed and every
//	                       --resume dies.
//
// # FAKE_ARGV_FILE
//
// When FAKE_ARGV_FILE names a file, every fake appends one JSON array of
// strings to it per recorded event, so a test can assert on what it was
// launched with:
//
//	fakeclaude    ["claude","-p","--output-format","stream-json",…]  (its full argv)
//	fakecodex     ["codex","app-server",…]                           (its full argv)
//	              ["turn/start","model=gpt-5.4","effort=medium"]     (once per turn, F-5)
//	fakeopencode  ["opencode","serve",…,"OPENCODE_PERMISSION=\"allow\""]  (F-10)
//	              ["POST /api/session/ses_x/model","{…the body verbatim…}"] (FK-23)
//	              ["POST /api/session/ses_x/permission/per_y/reply","{…}"]  (the ask reply)
package fakes

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Binaries maps the name each fake is installed under on PATH to the Go
// package directory it is built from.
var Binaries = map[string]string{
	"claude":   "fakeclaude",
	"codex":    "fakecodex",
	"opencode": "fakeopencode",
}

// sourceDir is the directory holding this file, so BuildDir works from any
// working directory.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(file)
}

// BuildDir compiles the three fakes into dir under the names the adapters look
// for on PATH: claude, codex and opencode.
func BuildDir(dir string) error {
	src := sourceDir()
	for name, pkg := range Binaries {
		out := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, "./"+pkg)
		cmd.Dir = src
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
)

// Build compiles the three fakes once per test binary (FK-3) and returns the
// directory holding them. Eighteen adapter tests must not pay for eighteen
// `go build` invocations, so the work is behind a package-level sync.Once.
//
// The directory is a fresh os.MkdirTemp. It is removed by [Main]; a test
// package that does not install [Main] as its TestMain leaves it behind for
// the operating system to clean up.
func Build(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "socrates-fakes-")
		if buildErr != nil {
			return
		}
		buildErr = BuildDir(buildDir)
	})
	if buildErr != nil {
		t.Fatalf("building the fake agent binaries: %v", buildErr)
	}
	return buildDir
}

// Main is a TestMain helper: it runs the tests and removes the directory
// [Build] compiled into.
//
//	func TestMain(m *testing.M) { fakes.Main(m) }
func Main(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// PathWith returns dir prepended to the current PATH, ready for t.Setenv.
func PathWith(dir string) string {
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}
