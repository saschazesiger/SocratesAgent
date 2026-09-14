package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

func TestRehomePath(t *testing.T) {
	const from, to = "/root/.socrates", "/data"
	cases := []struct {
		in    string
		want  string
		moved bool
	}{
		{"/root/.socrates/workspaces", "/data/workspaces", true},
		{"/root/.socrates", "/data", true},
		{"/root/.socrates/a/b/c", "/data/a/b/c", true},
		// A sibling whose name only starts the same way is not inside it.
		{"/root/.socrates-old/workspaces", "", false},
		{"/root/browser", "", false},
		{"/srv/work", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, moved := rehomePath(c.in, from, to)
		if moved != c.moved || (moved && got != c.want) {
			t.Errorf("rehomePath(%q) = (%q, %v), want (%q, %v)", c.in, got, moved, c.want, c.moved)
		}
	}
}

// A migration carries the directories of one machine into another, and the
// rule is the whole of what makes it safe: what lived in the data directory
// follows the data directory, and what did not is left to say out loud that it
// needs a mount.
func TestRehomeSettings(t *testing.T) {
	s := config.Settings{
		Workspace: config.WorkspaceSettings{
			Root: "/root/.socrates/workspaces",
			Presets: []config.PresetDir{
				{Label: "Scratch", Path: "/root/.socrates/workspaces/scratch"},
				{Label: "Socrates", Path: "/root/SocratesAgent"},
			},
		},
	}
	moves := rehomeSettings(&s, "/root/.socrates", "/data")
	if len(moves) != 2 {
		t.Fatalf("expected two moves, got %d: %v", len(moves), moves)
	}
	if s.Workspace.Root != "/data/workspaces" {
		t.Errorf("workspace root = %q", s.Workspace.Root)
	}
	if s.Workspace.Presets[0].Path != "/data/workspaces/scratch" {
		t.Errorf("preset inside the data directory = %q", s.Workspace.Presets[0].Path)
	}
	if s.Workspace.Presets[1].Path != "/root/SocratesAgent" {
		t.Errorf("preset outside the data directory was rewritten to %q", s.Workspace.Presets[1].Path)
	}
}

// Importing into the directory it was exported from is a no-op, not a rewrite
// of every path onto itself.
func TestRehomeSettingsSameDirectory(t *testing.T) {
	s := config.Settings{Workspace: config.WorkspaceSettings{Root: "/data/workspaces"}}
	if moves := rehomeSettings(&s, "/data", "/data"); moves != nil {
		t.Errorf("expected no moves, got %v", moves)
	}
	if s.Workspace.Root != "/data/workspaces" {
		t.Errorf("workspace root = %q", s.Workspace.Root)
	}
}

// The notes an import prints are the only warning that a preset names a
// directory the new machine cannot see, so a preset that is there has to stay
// quiet and one that is not has to speak.
func TestOffMachinePaths(t *testing.T) {
	dir := t.TempDir()
	here := filepath.Join(dir, "present")
	if err := os.MkdirAll(here, 0o755); err != nil {
		t.Fatal(err)
	}
	s := config.Settings{
		Workspace: config.WorkspaceSettings{
			Root: filepath.Join(dir, "workspaces"),
			Presets: []config.PresetDir{
				{Label: "Here", Path: here},
				{Label: "Gone", Path: filepath.Join(dir, "gone")},
			},
		},
	}
	notes := offMachinePaths(s, dir)
	if len(notes) != 1 {
		t.Fatalf("expected one note, got %d: %v", len(notes), notes)
	}
	if want := "Gone"; !strings.Contains(notes[0], want) {
		t.Errorf("the note does not name the missing preset: %q", notes[0])
	}
}
