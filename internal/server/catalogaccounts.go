package server

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// catalogAccounts is the accounts as the catalogue asks about them
// (catalog.Accounts): OpenCode lists its models once as every one of its
// accounts, because what each signed in to - its auth.json, its key - is what
// decides which providers, and so which models, there are.
type catalogAccounts struct{ s *Server }

var _ catalog.Accounts = catalogAccounts{}

// Envs is every OpenCode account's listing environment, in the person's
// order. An account whose environment cannot be made is left out, and no
// account at all leaves the listing to the service's own environment.
func (c catalogAccounts) Envs(agentName string) [][]string {
	if agentName != accounts.OpenCode {
		return nil
	}
	list, err := c.s.store.ListAccounts()
	if err != nil {
		log.Printf("catalog: list the accounts: %v", err)
		return nil
	}
	var envs [][]string
	for i := range list {
		a := &list[i]
		if a.Agent != accounts.OpenCode {
			continue
		}
		env, err := c.env(a)
		if err != nil {
			log.Printf("catalog: the models of %s: %v", a.ID, err)
			continue
		}
		envs = append(envs, env)
	}
	return envs
}

func (c catalogAccounts) env(a *store.Account) ([]string, error) {
	if err := accounts.Prepare(c.s.dataDir, a); err != nil {
		return nil, err
	}
	env := accounts.Env(c.s.dataDir, a, os.Environ())
	if len(a.Secret) > 0 {
		keys, err := c.s.accounts.keys()
		if err != nil {
			return nil, err
		}
		key, err := keys.Open(a.ID, a.Secret)
		if err != nil {
			return nil, fmt.Errorf("the account's key cannot be opened: %w", err)
		}
		env = append(env, accounts.SecretEnv(a.Agent, a.Provider, key)...)
	}
	return accounts.OpenCodeModelsEnv(c.s.dataDir, a.ID, env)
}

// Fingerprint is the OpenCode accounts and the state of the files their
// listings depend on: an account added, removed or given a new key, a
// sign-in in OpenCode's TUI, and the TUI's recent models all change it.
func (c catalogAccounts) Fingerprint() string {
	list, err := c.s.store.ListAccounts()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, a := range list {
		if a.Agent != accounts.OpenCode {
			continue
		}
		fmt.Fprintf(&b, "%s %s %s %d;", a.ID, a.State, a.Provider, len(a.Secret))
		for _, path := range accounts.OpenCodeModelsWatched(c.s.dataDir, a.ID) {
			if fi, err := os.Stat(path); err == nil {
				fmt.Fprintf(&b, "%d/%d,", fi.ModTime().UnixNano(), fi.Size())
			} else {
				b.WriteString("-,")
			}
		}
	}
	return b.String()
}
