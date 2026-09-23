package harnesses

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Shell is a plain login shell in a working directory. It is the harness with
// nothing to configure and nothing to resume, and it is the one that proves
// the terminal works without any agent in the way.
type Shell struct{}

func (Shell) Kind() Kind            { return KindShell }
func (Shell) Label() string         { return "Shell" }
func (Shell) VersionArgs() []string { return []string{"--version"} }

// DefaultBinary is the shell on the host machine when there is one to reach,
// then the user's own shell, then the two that are on nearly every machine. A
// shell is never "not installed": there is always something to fall back to.
//
// The host comes first for sessions without an explicit execution choice,
// preserving the behavior of shells created before the toggle existed. A
// container that cannot reach the host keeps its local shell.
func (Shell) DefaultBinary() string {
	if bin := hostShell(); bin != "" {
		return bin
	}
	return containerShellBinary()
}

const (
	ShellExecutionHost      = "host"
	ShellExecutionContainer = "container"
)

// HostShellAvailable is the actual capability used by the session picker and
// by Plan. A host choice is never offered on a plain container installation.
func HostShellAvailable() bool { return hostShell() != "" }

// DefaultShellExecution describes what an omitted choice will do, including
// an admin binary override that already selected a container-local program.
func DefaultShellExecution(binary string) string {
	if HostShellAvailable() && (strings.TrimSpace(binary) == "" || isHostShellBinary(binary)) {
		return ShellExecutionHost
	}
	return ShellExecutionContainer
}

func containerShellBinary() string {
	for _, candidate := range []string{strings.TrimSpace(os.Getenv("SHELL")), "/bin/bash", "/bin/sh"} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

// Plan is the whole of the shell launcher. If the resolved program is not one
// of the shells this app has heard of it is still run: the user configured it,
// and they know what they configured.
func (Shell) Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	opts := req.Settings.Harnesses.Shell
	requested, err := shellBinaryFor(req.ShellExecution, opts.Binary, hostShell(), containerShellBinary())
	if err != nil {
		return LaunchPlan{}, err
	}
	bin, err := resolveBinary(requested, "")
	if err != nil {
		return LaunchPlan{}, err
	}
	// A login shell reads the profile that sets up PATH and the prompt, which
	// is what makes the pane look like the machine's own terminal. It is not a
	// setting: a shell that did not read the profile is a shell whose PATH is
	// missing the very programs the person opened it for.
	argv := []string{bin, "-l"}

	env := baseEnv(req)

	return LaunchPlan{
		Argv:     argv,
		Env:      env,
		Cwd:      req.Cwd,
		Discover: DiscoverNone,
	}, nil
}

// shellBinaryFor makes an explicit choice authoritative while keeping the
// previous default for old sessions with no stored execution choice.
func shellBinaryFor(execution, configured, host, container string) (string, error) {
	switch execution {
	case "":
		if strings.TrimSpace(configured) != "" {
			return configured, nil
		}
		if host != "" {
			return host, nil
		}
		return container, nil
	case ShellExecutionHost:
		if host == "" {
			return "", fmt.Errorf("host shell is unavailable; start Socrates with host namespace access or choose container")
		}
		return host, nil
	case ShellExecutionContainer:
		if strings.TrimSpace(configured) != "" && !isHostShellBinary(configured) {
			return configured, nil
		}
		return container, nil
	default:
		return "", fmt.Errorf("unknown shell execution %q", execution)
	}
}

// isHostShellBinary recognizes the shipped wrapper even when Admin names it
// through PATH or a symlink. The Container choice must not accidentally run
// that wrapper through an old binary override.
func isHostShellBinary(binary string) bool {
	name := strings.TrimSpace(binary)
	if name == hostShellPath {
		return true
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return false
	}
	if path == hostShellPath {
		return true
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == hostShellPath
}

// ResumePlan says there is nothing to resume. A shell restarted in the same
// directory is the same shell as far as anybody is concerned.
func (Shell) ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	return LaunchPlan{}, ErrNoResume
}

// VerifyCLISession answers "gone", because a shell never had one.
func (Shell) VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error) {
	return false, nil
}

// DiscoverModels answers that there is no model step for a shell.
func (Shell) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return Catalog{}, ErrNoModels
}

// ------------------------------------------------------------- host shell

// hostShellPath is where the image keeps the wrapper that starts a login shell
// on the host machine instead of in the container. It is a fixed path rather
// than a setting: the setting is the container's own run flags, and the file is
// either reachable or it is not.
const hostShellPath = "/usr/local/bin/socrates-host-shell"

// hostShellTimeout bounds the one probe that asks whether the host is
// reachable. It runs once per process, and a hang here would be a session that
// never starts.
const hostShellTimeout = 5 * time.Second

var (
	hostShellOnce sync.Once
	hostShellBin  string
)

// hostShell is the wrapper's path when a shell on the host is both shipped and
// reachable, and "" when a pane should simply be a shell in this container.
//
// A container without the required namespace access cannot use the Host
// choice. Answering "" here lets the picker disable it and makes an explicit
// request fail before a pane is started.
func hostShell() string {
	hostShellOnce.Do(func() { hostShellBin = findHostShell(hostShellPath, hostReachableOnce) })
	return hostShellBin
}

// findHostShell is hostShell with its two questions passed in, so that both
// answers can be tested on a machine that is neither.
func findHostShell(path string, reachable func() bool) string {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	if !reachable() {
		return ""
	}
	return path
}

// hostReachable reports whether there is a host to jump to and whether this
// process is allowed to jump.
//
// The first half is why this is not a check for "am I in a container": a
// process that already shares PID 1's mount namespace has nowhere else to go,
// and a host installation is precisely that - entering the namespace it is
// already in would gain nothing and would force a different HOME on it. The
// second half cannot be answered by reading anything: it is a capability, and
// the only honest test is to try.
func hostReachable() bool {
	if !initElsewhere() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostShellTimeout)
	defer cancel()
	// Probe every namespace the wrapper will enter. Testing only the mount
	// namespace can offer Host even when its actual shell would be refused.
	return exec.CommandContext(ctx, "nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "/bin/sh", "-c", ":").Run() == nil
}

// initElsewhere reports whether PID 1 lives in a mount namespace other than
// this process's. With --pid=host that is the host's init and the answer is
// yes; in a normal container PID 1 is the container's own and the answer is no.
func initElsewhere() bool {
	self, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false
	}
	init, err := os.Readlink("/proc/1/ns/mnt")
	if err != nil {
		return false
	}
	return self != init
}
