package termux

import (
	"fmt"
	"os"
	"os/exec"
)

// Supervisor decides how the command that starts the tmux server is run.
type Supervisor interface {
	// Wrap returns the binary and arguments to run instead of bin/args, and
	// false when it has nothing to add.
	Wrap(bin string, args []string) (string, []string, bool)
}

// SystemdScope starts the tmux server in a transient systemd scope of its own.
//
// The reason is a trap in the deployment rather than in tmux: the tmux server
// daemonizes out of Socrates' process tree but stays in its cgroup, and
// systemd's default KillMode=control-group kills the whole cgroup on
// `systemctl restart socrates`. That would turn every ordinary restart into
// the reboot path and quietly defeat the durability the product is for.
// deploy/socrates.service sets KillMode=process; this is the second line of
// defence, and neither is required for correctness.
//
// Note what is deliberately absent: Socrates installs no SIGCHLD reaper of its
// own, not even as PID 1 in a container. A Go side reaper races os/exec's Wait
// on our own children - every viewer's `tmux attach`, every discovery
// subprocess - and turns their exits into ECHILD. tini as the container
// entrypoint (or `docker run --init`) reaps exactly what it should.
type SystemdScope struct {
	// User selects the user bus. It falls back to the system bus when Socrates
	// does not run as a user unit.
	User bool
	// Unit names the transient scope; empty means one derived from our pid.
	Unit string
}

// Wrap prefixes the command with systemd-run --scope.
func (s SystemdScope) Wrap(bin string, args []string) (string, []string, bool) {
	runner, err := exec.LookPath("systemd-run")
	if err != nil {
		return "", nil, false
	}
	unit := s.Unit
	if unit == "" {
		unit = fmt.Sprintf("socrates-tmux-%d", os.Getpid())
	}
	prefix := []string{}
	if s.User {
		prefix = append(prefix, "--user")
	}
	prefix = append(prefix, "--scope", "--quiet", "--unit", unit, "--collect", "--", bin)
	return runner, append(prefix, args...), true
}

// DetectSupervisor returns a supervisor when Socrates is running under systemd
// and systemd-run is available, and nil otherwise. Anywhere else - a
// container, a terminal, a Mac - the plain exec is already the right thing.
func DetectSupervisor() Supervisor {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return nil
	}
	if os.Getenv("INVOCATION_ID") == "" {
		if _, err := os.Stat("/run/systemd/system"); err != nil {
			return nil
		}
	}
	// A user bus needs a runtime directory; without one the system bus is the
	// only one there is.
	return SystemdScope{User: os.Getenv("XDG_RUNTIME_DIR") != ""}
}
