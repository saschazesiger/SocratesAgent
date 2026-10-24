package accounts

import (
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// MigrateOptions is one migration (migrate.go).
type MigrateOptions struct {
	DataDir string
	Store   *store.Store
	Env     []string
	Logf    func(format string, args ...any)
}

// Migrate: Socrates runs its agents on Linux and macOS, where the folders to
// move are; a Windows build has none.
func Migrate(MigrateOptions) error { return nil }
