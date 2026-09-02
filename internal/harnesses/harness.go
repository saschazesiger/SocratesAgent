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

// Kind is which of the four programs a session runs. The set is closed and it
// matches config's harness ids: one name for one thing, everywhere.
type Kind string

const (
	KindShell    Kind = Kind(config.HarnessShell)
	KindClaude   Kind = Kind(config.HarnessClaude)
	KindCodex    Kind = Kind(config.HarnessCodex)
	KindOpenCode Kind = Kind(config.HarnessOpenCode)
)

// ErrNoResume is what a harness with nothing to resume answers. A shell is the
// only one: it is started again in the same directory, which is the whole of
// what "resuming a shell" could mean.
var ErrNoResume = errors.New("this harness starts fresh rather than resuming")

// ErrNoModels is what a harness with no model list answers. It is not a
// failure - the shell has no models - and the catalogue treats it as one.
var ErrNoModels = errors.New("this harness has no model list")

// Harness is one of the four programs, seen as everything that has to be
// decided before it starts.
type Harness interface {
	Kind() Kind
	Label() string
	// DefaultBinary is the name looked up on PATH when the user set no
	// override.
	DefaultBinary() string
	VersionArgs() []string
	// Plan builds the argv, environment and generated files of a fresh
	// session.
	Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error)
	// ResumePlan builds the same for continuing req.CLISession. It answers
	// ErrNoResume when this harness cannot resume, and the caller uses Plan.
	ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error)
	// VerifyCLISession reports whether the stored id can still be resumed. A
	// false with a nil error means the conversation is gone; an error means
	// the question could not be answered, which is a different thing and the
	// callers treat it differently.
	VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error)
	// DiscoverModels fills the model picker, and answers ErrNoModels when
	// there is nothing to pick.
	DiscoverModels(ctx context.Context, bin string) (Catalog, error)
}

// Registry is the four harnesses in the order the picker offers them.
func Registry() []Harness {
	return []Harness{Shell{}, Claude{}, Codex{}, OpenCode{}}
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

// baseEnv is the terminal environment every session gets, whichever program it
// runs.
//
// COLORFGBG is the load-bearing one: Claude Code decides light from dark by
// reading the last field of it, and 15 is white. The others decide from the
// OSC 11 answer, which tmux gives them from the window style with nobody
// attached - but a wrong palette on a white page is unreadable rather than
// merely ugly, so both levers are pulled for all four.
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

// resolvedCwd is the working directory as the kernel - and therefore every CLI
// that asks for its own - reports it: with the symlinks gone.
//
// Both trust mechanisms key a stored entry by the working directory, and both
// programs look it up under the name they read back from the process rather
// than the one Socrates handed them. A workspace root reached through a link -
// /tmp on macOS, a linked projects directory, a home that is a symlink - would
// otherwise be trusted under a name neither program ever asks about, and the
// session would open on the trust prompt with the entry sitting in the file.
//
// A path that cannot be resolved is returned unchanged: the caller is about to
// start a program in it, and the unresolved name is a better guess than none.
func resolvedCwd(cwd string) string {
	if cwd == "" {
		return cwd
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return cwd
	}
	return real
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

// ---------------------------------------------------------------- argv bits

// flag appends "--name value" when the value is not empty, and nothing at all
// when it is. Nearly every option of every harness is optional, so this is the
// shape most of the argv building takes.
func flag(argv []string, name, value string) []string {
	if strings.TrimSpace(value) == "" {
		return argv
	}
	return append(argv, name, value)
}

// homeDir is where a CLI keeps its own state. It reads the environment rather
// than the user database because the e2e suite and the tests point HOME at a
// scratch directory, and a harness that ignored that would write into the real
// one.
func homeDir() string {
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
