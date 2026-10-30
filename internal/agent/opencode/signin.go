//go:build !windows

package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// SignInTerminal is what OpenCode's own TUI needs to sign one account in to
// its providers in a terminal (REBUILD.md §4.2): the binary as the dashboard
// configured it, and the variables that put it on the data folder's layout
// as that account, so whatever it signs in to (/connect) is written to that
// account's auth.json and nowhere else (research/06 §3.2). env is the
// account's environment (internal/accounts.Env); the database is migrated
// first, as before any serve.
func SignInTerminal(ctx context.Context, dataDir, account string, env []string, binary string) (string, map[string]string, error) {
	if account == "" {
		account = DefaultAccount
	}
	layout := NewLayout(dataDir)
	if err := layout.PrepareAccount(account); err != nil {
		return "", nil, err
	}
	bin, err := agent.ResolveBinary(env, "opencode", binary, envValue(env, BinaryEnv))
	if err != nil {
		return "", nil, fmt.Errorf("opencode: %w", err)
	}
	spawn := func(cmd *exec.Cmd) error {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		setPdeathsig(cmd.SysProcAttr)
		return cmd.Start()
	}
	if err := layout.Migrate(ctx, spawn, bin, processEnv(env, layout, account, "", nil)); err != nil {
		return "", nil, err
	}
	config, err := json.Marshal(map[string]any{"$schema": "https://opencode.ai/config.json", "share": "disabled", "autoupdate": false})
	if err != nil {
		return "", nil, err
	}
	vars := map[string]string{
		"OPENCODE_CONFIG_CONTENT": string(config),
		// Updates are socrates-update.timer's.
		"OPENCODE_DISABLE_AUTOUPDATE": "1",
	}
	for _, kv := range layout.Env(account) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	return bin, vars, nil
}

// ModelsEnv is the environment `opencode models` runs in to list what one
// account can run: the account's environment (internal/accounts.Env, with
// its key's variables for an API-key account) put on the layout as that
// account, so the providers are the ones its auth.json and key connect, and
// the TUI state read beside it (model.json) is the account's.
//
// Its database is a scratch one in the shared cache, not the chats': a
// listing migrates the database it is pointed at, and the shared one is only
// ever migrated under Migrate's lock.
func ModelsEnv(dataDir, account string, env []string) ([]string, error) {
	if account == "" {
		account = DefaultAccount
	}
	layout := NewLayout(dataDir)
	if err := layout.PrepareAccount(account); err != nil {
		return nil, err
	}
	out := processEnv(env, layout, account, "", nil)
	// processEnv is a server's; a listing wants none of the server's
	// variables, and the shared database is not its to migrate.
	kept := out[:0]
	for _, kv := range out {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "OPENCODE_SERVER_USERNAME", "OPENCODE_SERVER_PASSWORD", "OPENCODE_CONFIG_CONTENT", "OPENCODE_DB",
			// A listing is what refreshes the models.dev catalogue the chats,
			// which start offline-safe, read from the shared cache.
			"OPENCODE_DISABLE_MODELS_FETCH":
			continue
		}
		kept = append(kept, kv)
	}
	return append(kept, "OPENCODE_DB="+filepath.Join(layout.CacheHome(), "models-probe.db")), nil
}

// ModelsWatched is the files of an account whose change changes what
// ModelsEnv's listing finds: its credentials, and the TUI's model state.
func ModelsWatched(dataDir, account string) []string {
	if account == "" {
		account = DefaultAccount
	}
	layout := NewLayout(dataDir)
	return []string{layout.AuthFile(account), filepath.Join(layout.AccountHome(account), "state", "opencode", "model.json")}
}
