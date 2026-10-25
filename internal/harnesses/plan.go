// Package harnesses is the five programs a session can be: what the
// catalogue asks of each - its name, its binary, its version and its models -
// and, for the two that run in a terminal (a Shell and VS Code), everything
// needed to start one: the argv, the environment and the configuration files.
//
// This file holds the launch types alone. The harnesses and the registry that
// offers them live beside it.
package harnesses

import (
	"io/fs"

	"github.com/saschazesiger/SocratesAgent/internal/config"
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
}

// PlanRequest is what a plan is built from.
type PlanRequest struct {
	SessionID string
	Cwd       string
	Settings  config.Settings
	DataDir   string
}
