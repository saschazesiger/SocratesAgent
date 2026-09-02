// Package harnesses turns a request for a session into everything needed to
// start one: the argv of the program, the environment it runs in, the
// configuration files it reads, and what is known about the session id the
// program keeps for itself.
//
// This file holds the types alone. The four harnesses and the registry that
// offers them live beside it.
package harnesses

import (
	"io/fs"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// DiscoverMode says how the program's own session id is found, which is what
// makes a conversation survive a reboot.
type DiscoverMode string

const (
	// DiscoverNone is a program with no conversation to resume, such as a
	// shell.
	DiscoverNone DiscoverMode = "none"
	// DiscoverPreSet is the happy case: we chose the id and passed it in, so
	// there is nothing to find out.
	DiscoverPreSet DiscoverMode = "preset"
	// DiscoverCodexRollout watches the rollout files Codex writes, because
	// Codex has no flag for setting the id and does not print it.
	DiscoverCodexRollout DiscoverMode = "codex_rollout"
	// DiscoverOpenCodeAPI asks the HTTP server the OpenCode TUI runs.
	DiscoverOpenCodeAPI DiscoverMode = "opencode_api"
)

// GenFile is a configuration file written before the program starts. The mode
// matters: some of them carry a generated secret.
type GenFile struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// LaunchPlan is everything the terminal substrate needs in order to start a
// pane, fully resolved. No settings are consulted after it has been built, so
// a session keeps the configuration it was started with even if the dashboard
// changes underneath it.
type LaunchPlan struct {
	// Argv[0] is an absolute path, and the whole of Argv is passed to tmux as
	// separate arguments - there is no shell in the way, and therefore no
	// quoting question.
	Argv []string
	// Env is applied with `tmux new-session -e K=V`, one flag per entry,
	// rather than inherited from the Socrates process.
	Env   map[string]string
	Cwd   string
	Files []GenFile
	// CLISession is the program's own session id when we were able to choose
	// it, and empty when it has to be discovered.
	CLISession string
	Discover   DiscoverMode
	// Port is the loopback port of the OpenCode TUI's HTTP server, and zero
	// for every other harness.
	Port int
}

// PlanRequest is what a plan is built from.
type PlanRequest struct {
	SessionID  string
	Title      string
	Cwd        string
	Model      string
	Effort     string
	CLISession string
	Settings   config.Settings
	DataDir    string
	// Logf is the caller's log, for the one thing a plan does that can fail
	// without stopping the launch: pinning Claude Code's global config, whose
	// failure costs the session its trust entry and therefore its prompt. It
	// may be nil - the tests build requests by hand - so it is only ever
	// reached through PlanRequest.logf.
	Logf func(format string, args ...any)
}

// logf is the nil-safe form. A plan built without a log is silent, which is
// what a test wants and what production never is.
func (r PlanRequest) logf(format string, args ...any) {
	if r.Logf == nil {
		return
	}
	r.Logf(format, args...)
}
