package harnesses

import (
	"context"
	"os"
	"os/exec"
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
// The host comes first because a container that can reach it was started to
// reach it - see hostShell - and because the alternative is the more
// surprising of the two: a shell confined to a container that shares the
// host's processes anyway. A container that cannot reach the host is
// unaffected, and so is a binary path set in the dashboard.
func (Shell) DefaultBinary() string {
	if bin := hostShell(); bin != "" {
		return bin
	}
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
func (s Shell) Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	opts := req.Settings.Harnesses.Shell
	bin, err := resolveBinary(opts.Binary, s.DefaultBinary())
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
// Nobody has to switch this on. A container started with --pid=host and the
// right to enter namespaces is a container whose operator asked for the host;
// one started without them cannot reach it whatever any setting said, and
// answering "" here is the difference between that being a plain shell and
// being a pane that dies on a permission error. The binary path in the
// dashboard still overrides both.
func hostShell() string {
	hostShellOnce.Do(func() { hostShellBin = findHostShell(hostShellPath, hostReachable) })
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
	return exec.CommandContext(ctx, "nsenter", "-t", "1", "-m", "/bin/sh", "-c", ":").Run() == nil
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
