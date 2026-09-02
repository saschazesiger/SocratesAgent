package termux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// The installer is the one part of Socrates that changes the machine it runs
// on, so nothing here is allowed to. The "already installed" path is answered
// by the real tmux on this box, and every install path is answered by a script
// in a temporary directory that prints what a package manager would print and
// installs nothing at all.

// fakeBin writes an executable script and returns the directory it is in.
func fakeBin(t *testing.T, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the installer shells out, which this test cannot do on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// asRoot is an installer that believes it is root, so no test ever probes for
// sudo or runs one.
func asRoot(lookPath func(string) (string, error)) *Installer {
	return &Installer{LookPath: lookPath, Geteuid: func() int { return 0 }}
}

var errNotOnPath = errors.New("not found")

func TestDetectFindsTheTmuxOnThisMachine(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on this machine")
	}
	report := (&Installer{}).Detect(context.Background())
	if !report.Installed {
		t.Fatalf("tmux is on PATH but the report says otherwise: %#v", report)
	}
	if !report.OK {
		t.Fatalf("this machine's tmux is not accepted: %s", report.Reason)
	}
	if report.Version == "" || report.Path == "" {
		t.Fatalf("an installed tmux reported no version or path: %#v", report)
	}
	if report.Min != "3.3" {
		t.Fatalf("the reported minimum is %q, want 3.3", report.Min)
	}
	// Nothing about installing is offered for a tmux that is already fine.
	if report.CanInstall || report.Command != "" {
		t.Fatalf("a working tmux was offered an install: %#v", report)
	}
}

// 3.2a is what Ubuntu 22.04 ships and is exactly the version that must not
// pass: the generated conf errors at load and `new-session -e` is rejected.
func TestDetectCallsTmux32aTooOld(t *testing.T) {
	dir := fakeBin(t, "tmux", "#!/bin/sh\necho 'tmux 3.2a'\n")
	report := asRoot(func(file string) (string, error) {
		if file == "tmux" {
			return filepath.Join(dir, "tmux"), nil
		}
		return "", errNotOnPath
	}).Detect(context.Background())

	if !report.Installed {
		t.Fatalf("the fake tmux was not seen: %#v", report)
	}
	if report.OK {
		t.Fatal("tmux 3.2a was accepted; it must be reported as too old")
	}
	if report.Version != "3.2a" {
		t.Fatalf("version is %q, want 3.2a", report.Version)
	}
	if !strings.Contains(report.Reason, "too old") {
		t.Fatalf("the reason does not say it is too old: %q", report.Reason)
	}
}

func TestDetectPicksThePackageManagerAndTheCommand(t *testing.T) {
	dir := fakeBin(t, "apt-get", "#!/bin/sh\nexit 0\n")
	report := asRoot(func(file string) (string, error) {
		if file == "apt-get" {
			return filepath.Join(dir, "apt-get"), nil
		}
		return "", errNotOnPath
	}).Detect(context.Background())

	if report.Installed || report.OK {
		t.Fatalf("tmux is not on this fake machine: %#v", report)
	}
	if report.Manager != "apt-get" {
		t.Fatalf("manager is %q, want apt-get", report.Manager)
	}
	if !report.Privileged || !report.CanInstall {
		t.Fatalf("root was not treated as able to install: %#v", report)
	}
	// Root needs no wrapper, and the command is the one a person could paste.
	if strings.Contains(report.Command, "sudo") {
		t.Fatalf("the command asks for sudo as root: %q", report.Command)
	}
	for _, want := range []string{"apt-get update", "apt-get install", "ncurses-term"} {
		if !strings.Contains(report.Command, want) {
			t.Fatalf("the command %q does not contain %q", report.Command, want)
		}
	}
}

func TestDetectSaysSoWhenThereIsNoManager(t *testing.T) {
	report := asRoot(func(string) (string, error) { return "", errNotOnPath }).
		Detect(context.Background())
	if report.CanInstall || report.Command != "" {
		t.Fatalf("an install was offered with no package manager: %#v", report)
	}
	if !strings.Contains(report.Reason, "package manager") {
		t.Fatalf("the reason does not mention the missing package manager: %q", report.Reason)
	}
}

// Homebrew refuses to run as root and is right to, so Socrates must not be the
// thing that tries.
func TestDetectNeverRunsBrewAsRoot(t *testing.T) {
	dir := fakeBin(t, "brew", "#!/bin/sh\nexit 0\n")
	report := asRoot(func(file string) (string, error) {
		if file == "brew" {
			return filepath.Join(dir, "brew"), nil
		}
		return "", errNotOnPath
	}).Detect(context.Background())

	if report.Manager != "brew" {
		t.Fatalf("manager is %q, want brew", report.Manager)
	}
	if report.CanInstall {
		t.Fatal("brew was offered as an install to run as root")
	}
	if !strings.Contains(report.Reason, "root") {
		t.Fatalf("the reason does not explain the refusal: %q", report.Reason)
	}
}

func TestInstallIsANoOpWhenTmuxIsAlreadyThere(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on this machine")
	}
	var lines []string
	exit, err := (&Installer{}).Install(context.Background(), func(line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit is %d, want 0", exit)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "already installed") {
		t.Fatalf("an install with nothing to do printed %#v", lines)
	}
}

// The whole install path, against a package manager that prints like apt-get
// and installs nothing. Both of its commands have to run, in order, with
// stdout and stderr interleaved into one log.
func TestInstallRunsTheFakeAptGet(t *testing.T) {
	dir := fakeBin(t, "apt-get", `#!/bin/sh
if [ "$1" = "update" ]; then
  echo "Reading package lists..."
  exit 0
fi
echo "Setting up tmux (3.6a-2) ..."
echo "warning: this is a stand-in" >&2
exit 0
`)
	t.Setenv("PATH", dir)
	inst := asRoot(func(file string) (string, error) {
		if file == "tmux" {
			return "", errNotOnPath
		}
		return exec.LookPath(file)
	})

	var mu sync.Mutex
	var lines []string
	exit, err := inst.Install(context.Background(), func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit is %d, want 0", exit)
	}
	log := strings.Join(lines, "\n")
	for _, want := range []string{
		"$ apt-get update",
		"Reading package lists...",
		"$ apt-get install",
		"Setting up tmux",
		"warning: this is a stand-in",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("the log does not contain %q:\n%s", want, log)
		}
	}
}

// A package manager that fails hands back its exit code and its output, and is
// not an error: the caller wants both, and the output is the actionable half.
func TestInstallReportsAFailingManager(t *testing.T) {
	dir := fakeBin(t, "apk", "#!/bin/sh\necho 'ERROR: unable to lock database' >&2\nexit 99\n")
	t.Setenv("PATH", dir)
	inst := asRoot(func(file string) (string, error) {
		if file == "tmux" {
			return "", errNotOnPath
		}
		return exec.LookPath(file)
	})

	var lines []string
	exit, err := inst.Install(context.Background(), func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatalf("a failing package manager is not an error here: %v", err)
	}
	if exit != 99 {
		t.Fatalf("exit is %d, want 99", exit)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "unable to lock database") {
		t.Fatalf("the manager's own words were lost: %#v", lines)
	}
}

// Two dashboards must not start two package managers on one machine.
func TestInstallRefusesASecondRun(t *testing.T) {
	dir := fakeBin(t, "yum", "#!/bin/sh\nsleep 1\nexit 0\n")
	t.Setenv("PATH", dir)
	inst := asRoot(func(file string) (string, error) {
		if file == "tmux" {
			return "", errNotOnPath
		}
		return exec.LookPath(file)
	})

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = inst.Install(context.Background(), func(string) {})
	}()
	// The first run has to be inside Install before the second is attempted,
	// and the only thing it prints before running anything is the command.
	go func() {
		for {
			inst.mu.Lock()
			running := inst.running
			inst.mu.Unlock()
			if running {
				close(started)
				return
			}
		}
	}()
	<-started
	if _, err := inst.Install(context.Background(), func(string) {}); !errors.Is(err, ErrInstalling) {
		t.Fatalf("the second install was not refused: %v", err)
	}
	<-done
}
