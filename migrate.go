package main

import (
	"context"
	"log"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// cutTimeout bounds stopping one agent terminal: a kill-session and the
// removal of its folder, which take moments on a tmux that answers.
const cutTimeout = 30 * time.Second

// migrate runs the migration of the build before accounts and chats
// (REBUILD.md §8.1-8.3) on a data directory that is due for it, and does
// nothing on any other. The agents' terminals are stopped through the
// terminal substrate the server itself uses, so a terminal goes exactly as a
// delete would take it: its tmux session killed on the data directory's own
// socket, its row and its folder removed. A Shell is left running.
func migrate(dataDir string, st *store.Store, env []string, logf func(string, ...any)) error {
	terminals, err := termux.New(st, termux.Config{DataDir: dataDir, Logf: logf})
	if err != nil {
		return err
	}
	defer terminals.Close()
	return accounts.Migrate(accounts.MigrateOptions{
		DataDir: dataDir, Store: st, Env: env, Logf: logf,
		StopTerminal: func(id string) error {
			ctx, cancel := context.WithTimeout(context.Background(), cutTimeout)
			defer cancel()
			return terminals.Delete(ctx, id)
		},
	})
}

// migrateLogged is migrate as a start runs it: a migration that did not
// finish is said, and tried again by the next start.
func migrateLogged(dataDir string, st *store.Store, env []string) {
	if err := migrate(dataDir, st, env, log.Printf); err != nil {
		log.Printf("rebuild: the migration did not finish (%v); the next start tries again", err)
	}
}
