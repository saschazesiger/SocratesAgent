//go:build !windows

package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
