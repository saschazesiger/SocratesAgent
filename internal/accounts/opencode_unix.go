//go:build !windows

package accounts

import (
	"context"

	"github.com/saschazesiger/SocratesAgent/internal/agent/opencode"
)

// OpenCodeTerminal is OpenCode's TUI for signing an account in to its
// providers in a Shell: the binary and the variables that put it on the
// account's folder (opencode.SignInTerminal).
func OpenCodeTerminal(ctx context.Context, dataDir, id string, env []string, binary string) (string, map[string]string, error) {
	return opencode.SignInTerminal(ctx, dataDir, id, env, binary)
}
