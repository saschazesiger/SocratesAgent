package harnesses

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// Every test in this package builds argv and files. None of them starts a
// program: the binaries on PATH are one-line shell scripts that exist only so
// that the launchers have an absolute path to put in argv[0], and the version
// probe something to ask.

func fakePATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex", "opencode", "code-server", "env"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+name+" 0.0.0-test\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// lab is one session's worth of scaffolding: a home nothing real can see, a
// data directory, and settings straight out of the defaults.
type lab struct {
	t        *testing.T
	home     string
	data     string
	cwd      string
	settings config.Settings
}

func newLab(t *testing.T) *lab {
	t.Helper()
	fakePATH(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return &lab{t: t, home: home, data: t.TempDir(), cwd: cwd, settings: config.Default()}
}

func (l *lab) req() PlanRequest {
	return PlanRequest{
		SessionID: "abcdef0123456789",
		Cwd:       l.cwd,
		Settings:  l.settings,
		DataDir:   l.data,
	}
}

func (l *lab) plan(h Terminal) LaunchPlan {
	l.t.Helper()
	plan, err := h.Plan(context.Background(), l.req())
	if err != nil {
		l.t.Fatalf("%s could not be planned: %v", h.Kind(), err)
	}
	return plan
}

// has reports whether argv carries a flag with that value, as two adjacent
// elements - which is how tmux receives them, with no shell in between.
func has(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}

func carries(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------- shared

// Every terminal says the background is white, whichever program it runs: a
// program that reads COLORFGBG decides from this, the others from the OSC 11
// answer.
func TestEnvAlwaysCarriesWhite(t *testing.T) {
	l := newLab(t)
	for _, h := range []Terminal{Shell{}, VSCode{}} {
		plan := l.plan(h)
		if plan.Env["COLORFGBG"] != "0;15" {
			t.Errorf("%s: COLORFGBG = %q", h.Kind(), plan.Env["COLORFGBG"])
		}
		if plan.Env["COLORTERM"] != "truecolor" {
			t.Errorf("%s: COLORTERM = %q", h.Kind(), plan.Env["COLORTERM"])
		}
		if plan.Env["TERM"] == "" {
			t.Errorf("%s: no TERM", h.Kind())
		}
		if plan.Env["SOCRATES_SESSION"] != "abcdef0123456789" || plan.Env["SOCRATES"] != "1" {
			t.Errorf("%s: the session marks are missing: %v", h.Kind(), plan.Env)
		}
		if value, set := plan.Env["TMUX"]; !set || value != "" {
			t.Errorf("%s: TMUX = %q (%v)", h.Kind(), value, set)
		}
		if !filepath.IsAbs(plan.Argv[0]) {
			t.Errorf("%s: argv[0] = %q is not absolute", h.Kind(), plan.Argv[0])
		}
		if plan.Cwd != l.cwd {
			t.Errorf("%s: cwd = %q", h.Kind(), plan.Cwd)
		}
	}
}

// The registry is a fixed order, and every id in it is one config knows.
// Only the Shell and VS Code run in a terminal: the coding agents are in the
// catalogue for their versions and models, and run as chats.
func TestRegistryOrder(t *testing.T) {
	want := []string{config.HarnessShell, config.HarnessClaude, config.HarnessCodex, config.HarnessOpenCode, config.HarnessVSCode}
	got := Registry()
	if len(got) != len(want) {
		t.Fatalf("the registry has %d harnesses", len(got))
	}
	for i, h := range got {
		if string(h.Kind()) != want[i] {
			t.Fatalf("registry[%d] = %s, want %s", i, h.Kind(), want[i])
		}
		if _, ok := Get(want[i]); !ok {
			t.Fatalf("%s cannot be looked up", want[i])
		}
	}
	if _, ok := Get("nothing"); ok {
		t.Fatal("an id nobody ships was found")
	}
	for id, terminal := range map[string]bool{
		config.HarnessShell: true, config.HarnessVSCode: true,
		config.HarnessClaude: false, config.HarnessCodex: false, config.HarnessOpenCode: false,
	} {
		if _, ok := TerminalOf(id); ok != terminal {
			t.Errorf("%s runs in a terminal: %v, want %v", id, ok, terminal)
		}
	}
}

// A shell is a login shell unless the settings say otherwise.
func TestShellIsALoginShell(t *testing.T) {
	l := newLab(t)
	plan := l.plan(Shell{})
	if !carries(plan.Argv, "-l") {
		t.Errorf("the default is a login shell: %v", plan.Argv)
	}
	if len(plan.Files) != 0 {
		t.Errorf("a shell writes files: %v", plan.Files)
	}
}

// -------------------------------------------------------------- working dirs

func TestResolveWorkdir(t *testing.T) {
	root := t.TempDir()
	settings := config.Default()
	settings.Workspace.Root = root
	settings.Workspace.AllowCustom = true

	dir, err := ResolveWorkdir(settings, WorkdirDynamic, "", "claude", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), "claude-") || !strings.HasSuffix(dir, "-01234567") {
		t.Errorf("a dynamic directory is named %q", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the dynamic directory was not created: %v", err)
	}
	if err := insideRoot(root, dir); err != nil {
		t.Fatalf("a dynamic directory left its root: %v", err)
	}

	// A preset has to be one of the dashboard's own, and it has to still be
	// there: a preset that is gone is a configuration that no longer describes
	// this machine, and creating it silently would hide that.
	preset := filepath.Join(root, "preset")
	if err := os.MkdirAll(preset, 0o755); err != nil {
		t.Fatal(err)
	}
	settings.Workspace.Presets = []config.PresetDir{{Label: "Preset", Path: preset}}
	if got, err := ResolveWorkdir(settings, WorkdirPreset, preset, "shell", "id"); err != nil || got != preset {
		t.Fatalf("preset = %q, %v", got, err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirPreset, filepath.Join(root, "elsewhere"), "shell", "id"); err == nil {
		t.Fatal("a path that is not a preset was accepted")
	}
	if err := os.Remove(preset); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirPreset, preset, "shell", "id"); err == nil {
		t.Fatal("a preset that is gone was accepted")
	}

	// Custom: created, but never one of the places that are never an answer,
	// and never at all when the dashboard has not allowed it.
	custom := filepath.Join(t.TempDir(), "typed", "in")
	if got, err := ResolveWorkdir(settings, WorkdirCustom, custom, "codex", "id"); err != nil || got != custom {
		t.Fatalf("custom = %q, %v", got, err)
	}
	for _, blocked := range []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot",
		"/proc", "/proc/1", "/sys", "/sys/kernel", "/dev", "/dev/shm/work"} {
		if _, err := ResolveWorkdir(settings, WorkdirCustom, blocked, "codex", "id"); err == nil {
			t.Errorf("%s was accepted as a working directory", blocked)
		}
	}

	// The rule is what a path resolves to. MkdirAll follows a symlink, so a
	// harmless-looking name pointing at /etc has to be refused by the name it
	// ends up at, not the one it was typed as.
	links := t.TempDir()
	trap := filepath.Join(links, "innocent")
	if err := os.Symlink("/etc", trap); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirCustom, trap, "codex", "id"); err == nil {
		t.Errorf("%s -> /etc was accepted", trap)
	}
	// The resolution works on the longest part of the path that exists, so a
	// directory that is not there yet is still judged by where its parent
	// really is.
	sysLink := filepath.Join(links, "harmless")
	if err := os.Symlink("/sys", sysLink); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkdir(settings, WorkdirCustom, filepath.Join(sysLink, "workspace"), "codex", "id"); err == nil {
		t.Errorf("%s/workspace, which is under /sys, was accepted", sysLink)
	}
	settings.Workspace.AllowCustom = false
	if _, err := ResolveWorkdir(settings, WorkdirCustom, custom, "codex", "id"); err == nil {
		t.Fatal("a typed path was accepted where the dashboard forbids it")
	}
	settings.Workspace.AllowCustom = true

	// The shape rules hold for every mode.
	for _, bad := range []string{"", "relative/path", "/tmp/../etc"} {
		if _, err := ResolveWorkdir(settings, WorkdirCustom, bad, "codex", "id"); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ResolveWorkdir(settings, "sideways", "", "codex", "id"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}
