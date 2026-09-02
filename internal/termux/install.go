package termux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// This file is the answer to the one question a fresh machine asks the
// dashboard: is tmux here, and if it is not, can Socrates put it there.
//
// Nothing in it is a fallback. tmux missing is a hard, explained error - the
// new-session sheet refuses to start anything - so the whole job here is to
// say what is wrong in words somebody can act on, and to offer the one
// command that fixes it, run with the output visible while it runs.

// InstallTimeout is the whole budget for one install. A package manager that
// has not finished in five minutes is waiting for something it will never get.
const InstallTimeout = 5 * time.Minute

// InstallLogLines is how much of the output is kept for the page to show after
// a reload. It is the tail, because the reason a package install failed is
// always at the end.
const InstallLogLines = 200

// Report is what the dashboard knows about tmux on this machine.
type Report struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	// OK is the only field that decides anything: it is true when tmux is
	// here *and* new enough for the generated configuration.
	OK  bool   `json:"ok"`
	Min string `json:"min"`
	// Manager is the package manager that was found, "" when there is none.
	Manager string `json:"manager"`
	// Privileged reports whether the install command can be run at all: root,
	// or a sudo that does not ask for a password.
	Privileged bool `json:"privileged"`
	// CanInstall is Manager and Privileged taken together, plus the one
	// refusal that is neither: Homebrew must never be run as root.
	CanInstall bool `json:"can_install"`
	// Command is what a person can paste into a terminal themselves. It is
	// filled in whenever a manager was found, because the case where Socrates
	// cannot run it is exactly the case where somebody has to.
	Command string `json:"command"`
	// Reason says in one sentence why OK is false, or why CanInstall is.
	Reason string `json:"reason"`
}

// managerCommands is the package-manager matrix. Only the apt-get entry has
// been executed on a machine (Ubuntu, tmux 3.6a); the others are the
// conventional invocations and have not been run here.
var managerCommands = map[string][][]string{
	"apt-get": {
		{"apt-get", "update", "-qq"},
		{"apt-get", "install", "-y", "--no-install-recommends", "tmux", "ncurses-term"},
	},
	"apk":    {{"apk", "add", "--no-cache", "tmux", "ncurses-terminfo"}},
	"dnf":    {{"dnf", "install", "-y", "--setopt=install_weak_deps=False", "tmux", "ncurses-term"}},
	"yum":    {{"yum", "install", "-y", "tmux"}},
	"pacman": {{"pacman", "-Sy", "--noconfirm", "--needed", "tmux"}},
	"zypper": {{"zypper", "--non-interactive", "install", "tmux"}},
	"brew":   {{"brew", "install", "tmux"}},
}

// managerOrder is the order the managers are looked for in: first hit wins.
var managerOrder = []string{"apt-get", "apk", "dnf", "yum", "pacman", "zypper", "brew"}

// Installer detects tmux and installs it. One of these exists per process, and
// its mutex is why two dashboards cannot start two package managers at once.
type Installer struct {
	// LookPath and Geteuid are the machine, injected so the tests can describe
	// one without changing this one. Nil means the real thing.
	LookPath func(file string) (string, error)
	Geteuid  func() int

	mu      sync.Mutex
	running bool
}

func (i *Installer) lookPath(file string) (string, error) {
	if i.LookPath != nil {
		return i.LookPath(file)
	}
	return exec.LookPath(file)
}

func (i *Installer) euid() int {
	if i.Geteuid != nil {
		return i.Geteuid()
	}
	return os.Geteuid()
}

// Detect answers the /api/tmux question, in the order §F.1 asks it: the binary
// and its version first, and only then - if that is not enough - what could be
// done about it.
func (i *Installer) Detect(ctx context.Context) Report {
	rep := Report{Min: fmt.Sprintf("%d.%d", MinMajor, MinMinor)}
	path, err := i.lookPath("tmux")
	if err == nil {
		rep.Installed, rep.Path = true, path
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		v, verr := BinaryVersion(probe, path)
		switch {
		case verr != nil:
			rep.Reason = fmt.Sprintf("could not run %s: %v", path, verr)
		case !v.OK():
			rep.Version = v.String()
			rep.Reason = fmt.Sprintf("tmux %s is too old; Socrates needs %s or newer", v, rep.Min)
		default:
			rep.Version, rep.OK = v.String(), true
		}
	} else {
		rep.Reason = "tmux is not installed. Socrates needs it to keep sessions alive"
	}
	if rep.OK {
		return rep
	}

	root := i.euid() == 0
	prefix := ""
	if !root {
		if sudo, err := i.lookPath("sudo"); err == nil && i.sudoWorks(ctx, sudo) {
			prefix = "sudo -n"
		}
	}
	rep.Privileged = root || prefix != ""
	for _, name := range managerOrder {
		if _, err := i.lookPath(name); err != nil {
			continue
		}
		rep.Manager = name
		break
	}
	if rep.Manager == "" {
		rep.Reason += ". No package manager Socrates knows was found, so it cannot install tmux for you"
		return rep
	}
	// Homebrew refuses to run as root and is right to; there is nothing to
	// offer on a machine where the only manager is brew and the only user is
	// root.
	if rep.Manager == "brew" {
		prefix = ""
		if root {
			rep.Reason += ". Homebrew must not be run as root, so install tmux as your own user"
			rep.Command = commandLine(managerCommands["brew"], "")
			return rep
		}
		rep.Privileged = true
	}
	rep.Command = commandLine(managerCommands[rep.Manager], prefix)
	rep.CanInstall = rep.Privileged
	if !rep.CanInstall {
		rep.Reason += ". Installing it needs root, and sudo here asks for a password. Run this yourself: " + rep.Command
	}
	return rep
}

// sudoWorks is the only honest way to ask whether sudo can be used without a
// person at the keyboard: try it, non-interactively, on something harmless.
func (i *Installer) sudoWorks(ctx context.Context, sudo string) bool {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, sudo, "-n", "true")
	cmd.Stdin = nil
	return cmd.Run() == nil
}

// commandLine renders the matrix entry as the line a person would type.
func commandLine(steps [][]string, prefix string) string {
	var parts []string
	for _, step := range steps {
		line := strings.Join(step, " ")
		if prefix != "" {
			line = prefix + " " + line
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, " && ")
}

// ErrInstalling is returned when a second install is asked for while the first
// is still running. It is a refusal and not a queue: two package managers on
// one machine is how a dpkg lock is discovered the hard way.
var ErrInstalling = errors.New("tmux is already being installed")

// Install runs the package manager for the detected system, feeding every line
// of its output - stdout and stderr interleaved, as they appeared - to emit.
//
// It returns the exit code of the last command it ran. A non-zero code is not
// an error return: the caller wants both the code and the output, and the
// output is the part a person can act on.
func (i *Installer) Install(ctx context.Context, emit func(line string)) (int, error) {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return -1, ErrInstalling
	}
	i.running = true
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		i.running = false
		i.mu.Unlock()
	}()

	rep := i.Detect(ctx)
	if rep.OK {
		emit("tmux " + rep.Version + " is already installed at " + rep.Path)
		return 0, nil
	}
	if !rep.CanInstall {
		return -1, errors.New(rep.Reason)
	}
	// Whether the command needs a wrapper is decided the same way Detect
	// decided it could be run at all: root needs none, brew must never have
	// one, and everything else goes through the sudo that answered the probe.
	sudo := i.euid() != 0 && rep.Manager != "brew"

	ctx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()

	code := 0
	for _, step := range managerCommands[rep.Manager] {
		name, args := step[0], step[1:]
		prefix := ""
		if sudo {
			name, args, prefix = "sudo", append([]string{"-n"}, step...), "sudo -n"
		}
		emit("$ " + commandLine([][]string{step}, prefix))
		exit, err := runStreaming(ctx, name, args, emit)
		code = exit
		if err != nil {
			return code, err
		}
		if code != 0 {
			return code, nil
		}
	}
	return code, nil
}

// runStreaming runs one command with both its streams joined into one pipe, so
// the log reads in the order the program actually printed it. Stdin is nil on
// purpose: a package manager that decides to ask a question must fail rather
// than wait for an answer that is never coming.
func runStreaming(ctx context.Context, name string, args []string, emit func(string)) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	cmd.Stdin = nil
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			emit(scanner.Text())
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	_ = pr.Close()

	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	default:
		return -1, err
	}
}
