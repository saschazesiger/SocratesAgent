//go:build !windows

package accounts

import "github.com/saschazesiger/SocratesAgent/internal/agent/opencode"

// prepareOpenCode makes the account's data home on OpenCode's layout.
func prepareOpenCode(dataDir, id string) error {
	return opencode.NewLayout(dataDir).PrepareAccount(id)
}
