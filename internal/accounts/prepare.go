package accounts

import (
	"fmt"
	"os"

	"github.com/saschazesiger/SocratesAgent/internal/agent/claude"
	"github.com/saschazesiger/SocratesAgent/internal/agent/codex"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Prepare makes an account's folder what its agent expects before the
// account's first run, and again before every launch (each step is
// idempotent): the folders every account of the agent shares are relative
// symlinks into <data>/agents/<agent>, so a conversation moves between them.
// A login already in the folder is left exactly where it is.
func Prepare(dataDir string, a *store.Account) error {
	dir := Folder(dataDir, a.Agent, a.ID)
	switch a.Agent {
	case Claude:
		return claude.PrepareConfigDir(dir, SharedFolder(dataDir, Claude))
	case Codex:
		return codex.PrepareHome(dir, SharedFolder(dataDir, Codex))
	case OpenCode:
		return prepareOpenCode(dataDir, a.ID)
	case Fake:
		return os.MkdirAll(dir, 0o700)
	}
	return fmt.Errorf("%q has no accounts", a.Agent)
}
