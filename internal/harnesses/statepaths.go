package harnesses

import "path/filepath"

// Where each coding agent keeps the state Socrates depends on but does not
// own. The launchers work these out for themselves; these three exist so the
// dashboard's diagnostics can name the same paths rather than a second guess
// at them, which is how the two would eventually disagree.

// ClaudeConfigDir is the directory Claude Code keeps its transcripts in, and
// the one Socrates has to be able to write for the light theme to pin.
func ClaudeConfigDir() string { return claudeConfigDir() }

// CodexHome is where Codex keeps its rollout files and its state database.
func CodexHome() string { return codexHome() }

// OpenCodeDBPath is OpenCode's own database, which is where a session id is
// looked up when its HTTP server can no longer be reached. An empty string
// means this machine has no home directory to look in.
func OpenCodeDBPath() string {
	dir := openCodeDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "opencode.db")
}
