package harnesses

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// Kind is which of the five programs a session runs. The set is closed and it
// matches config's harness ids: one name for one thing, everywhere.
type Kind string

const (
	KindVSCode   Kind = Kind(config.HarnessVSCode)
	KindShell    Kind = Kind(config.HarnessShell)
	KindClaude   Kind = Kind(config.HarnessClaude)
	KindCodex    Kind = Kind(config.HarnessCodex)
	KindOpenCode Kind = Kind(config.HarnessOpenCode)
)

// ErrNoModels is what a harness with no model list answers. It is not a
// failure - the shell has no models - and the catalogue treats it as one.
var ErrNoModels = errors.New("this harness has no model list")

// Harness is one of the five programs, as the catalogue sees it: what it is
// called, where its binary is found, and what it can be run on.
type Harness interface {
	Kind() Kind
	Label() string
	// DefaultBinary is the name looked up on PATH when the user set no
	// override.
	DefaultBinary() string
	VersionArgs() []string
	// DiscoverModels fills the model picker, and answers ErrNoModels when
	// there is nothing to pick.
	DiscoverModels(ctx context.Context, bin string) (Catalog, error)
}

// Terminal is a harness that runs in a terminal of its own: a Shell, and VS
// Code, which rides the same substrate. A resume after a reboot is a fresh
// start in the same folder, so one plan is all either needs.
type Terminal interface {
	Harness
	// Plan builds the argv, environment and generated files of a session.
	Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error)
}

// Registry is the five harnesses in the order the picker offers them.
func Registry() []Harness {
	return []Harness{Shell{}, Claude{}, Codex{}, OpenCode{}, VSCode{}}
}

// Get returns one harness by its id.
func Get(id string) (Harness, bool) {
	for _, h := range Registry() {
		if string(h.Kind()) == id {
			return h, true
		}
	}
	return nil, false
}

// TerminalOf returns the harness of an id when it is one that runs in a
// terminal, and false for the coding agents, which run as chats.
func TerminalOf(id string) (Terminal, bool) {
	h, ok := Get(id)
	if !ok {
		return nil, false
	}
	t, ok := h.(Terminal)
	return t, ok
}

// sessionDir is where everything Socrates generates for one session lives. It
// is the same directory termux.SessionDir names; the two are apart because
// termux builds on this package and cannot be imported back.
func sessionDir(dataDir, id string) string {
	return filepath.Join(dataDir, "sessions", id)
}

// SessionFile is the path of one generated file inside a session's directory.
func SessionFile(dataDir, id, name string) string {
	return filepath.Join(sessionDir(dataDir, id), name)
}

// ---------------------------------------------------------------- terminal

var (
	terminalOnce sync.Once
	terminalName string
)

// defaultTerm is the TERM a pane sees. tmux sets it from `default-terminal` in
// the generated configuration; the harnesses set it too, so the value is the
// same whichever of the two got there first. It mirrors termux.DefaultTerminal,
// which this package cannot call without an import cycle.
func defaultTerm() string {
	terminalOnce.Do(func() {
		terminalName = "screen-256color"
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "infocmp", "tmux-256color").Run(); err == nil {
			terminalName = "tmux-256color"
		}
	})
	return terminalName
}

// baseEnv is the terminal environment every terminal gets, whichever program
// it runs.
//
// COLORFGBG says the background is white (the last field, 15), which is how
// a program run in the shell - an editor, a pager - that reads it decides
// light from dark. The others decide from the OSC 11 answer, which tmux gives
// them from the window style with nobody attached - but a wrong palette on a
// white page is unreadable rather than merely ugly, so both levers are
// pulled.
//
// TMUX is blanked so that a program which refuses to run "inside tmux" - and
// several check exactly this variable - runs anyway. It is Socrates' tmux, and
// the program is the pane, not a nested client.
func baseEnv(req PlanRequest) map[string]string {
	return map[string]string{
		"TERM":             defaultTerm(),
		"COLORTERM":        "truecolor",
		"COLORFGBG":        "0;15",
		"TMUX":             "",
		"SOCRATES_SESSION": req.SessionID,
		"SOCRATES":         "1",
	}
}

// ---------------------------------------------------------------- binaries

// resolveBinary turns the user's override, or the harness's default name, into
// an absolute path. argv[0] is always absolute: tmux starts the program with
// no shell in the way, and a relative name would be resolved against whatever
// PATH the tmux server happens to carry.
func resolveBinary(override, fallback string) (string, error) {
	name := strings.TrimSpace(override)
	if name == "" {
		name = fallback
	}
	if name == "" {
		return "", errors.New("no program to run")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is not on this machine: %w", name, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// homeDir is where a CLI keeps its own state. It reads HOME and nothing else:
// the service runs with the HOME of the account it runs as, and the e2e suite
// and the tests point it at a scratch directory.
// A fallback to the user database would quietly ignore both.
func homeDir() string { return strings.TrimSpace(os.Getenv("HOME")) }
