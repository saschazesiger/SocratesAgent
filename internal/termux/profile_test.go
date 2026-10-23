package termux

import (
	"path/filepath"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// A Claude session in a profile writes its status files into the profile's
// directory, and the activity reader has to look there rather than in the
// home directory's .claude.
func TestClaudeStatusFilesFollowTheProfile(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, filepath.Join("/home/someone", ".claude", "sessions")},
		{map[string]string{"HOME": "/elsewhere"}, filepath.Join("/elsewhere", ".claude", "sessions")},
		{map[string]string{"CLAUDE_CONFIG_DIR": "/var/lib/socrates/profiles/p-1/claude"}, filepath.Join("/var/lib/socrates/profiles/p-1/claude", "sessions")},
	}
	for _, c := range cases {
		if got := claudeSessionsDir(harnesses.LaunchPlan{Env: c.env}); got != c.want {
			t.Errorf("env %v: %q, want %q", c.env, got, c.want)
		}
	}
}

// A CLAUDE_CONFIG_DIR in Socrates' own environment is inherited by every pane,
// and Claude Code writes there whatever HOME is - unless the plan gives the
// pane one of its own, as a profile does.
func TestClaudeStatusFilesFollowAnInheritedConfigDir(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("CLAUDE_CONFIG_DIR", "/srv/claude")
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, filepath.Join("/srv/claude", "sessions")},
		{map[string]string{"HOME": "/elsewhere"}, filepath.Join("/srv/claude", "sessions")},
		{map[string]string{"CLAUDE_CONFIG_DIR": "/var/lib/socrates/profiles/p-1/claude"}, filepath.Join("/var/lib/socrates/profiles/p-1/claude", "sessions")},
	}
	for _, c := range cases {
		if got := claudeSessionsDir(harnesses.LaunchPlan{Env: c.env}); got != c.want {
			t.Errorf("env %v: %q, want %q", c.env, got, c.want)
		}
	}
}
