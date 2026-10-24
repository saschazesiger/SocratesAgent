package harnesses

import (
	"os"
	"path/filepath"
	"strings"
)

// Where each coding agent keeps its own state when nothing points it
// elsewhere: the folders in HOME a CLI run by hand writes its conversations
// into. Socrates' chats never use them - every account has a folder of its
// own in the data directory (REBUILD.md §6.1) - but the cost ledger still
// reads them, so that a conversation had on this machine outside Socrates is
// on the costs page too, and the accounts migration moves them in once.

// ClaudeConfigDir is the directory Claude Code keeps its transcripts in.
func ClaudeConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// CodexHome is where Codex keeps its rollout files and its state database.
func CodexHome() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// OpenCodeDataDir is OpenCode's own data directory: its database and its
// session store. An empty string means this machine has no home directory to
// look in.
func OpenCodeDataDir() string {
	xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if xdg == "" {
		home := homeDir()
		if home == "" {
			return ""
		}
		xdg = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdg, "opencode")
}
