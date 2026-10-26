//go:build linux

package opencode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An account signs in in OpenCode's own TUI, run on the layout as that
// account: its data home is made, the database migrated, and what the TUI
// signs in to lands in that account's auth.json, never the default one's.
func TestASignInTerminalIsOnTheAccountsOwnFolder(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bin, vars, err := SignInTerminal(ctx, r.data, "acct", r.env, "")
	if err != nil {
		t.Fatal(err)
	}
	layout := NewLayout(r.data)
	if !filepath.IsAbs(bin) || filepath.Base(bin) != "opencode" {
		t.Fatalf("binary %q", bin)
	}
	if vars["XDG_DATA_HOME"] != layout.AccountHome("acct") || vars["OPENCODE_DB"] != layout.DB() ||
		vars["XDG_CONFIG_HOME"] != layout.ConfigHome() || vars["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Fatalf("vars %v", vars)
	}
	if _, ok := vars["OPENCODE_SERVER_PASSWORD"]; ok {
		t.Fatalf("a TUI is given a server's password: %v", vars)
	}
	if fi, err := os.Stat(filepath.Dir(layout.AuthFile("acct"))); err != nil || !fi.IsDir() {
		t.Fatalf("the account's data home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Root, migrateStamp)); err != nil {
		t.Fatalf("the database was not migrated: %v", err)
	}
	if _, err := os.Stat(layout.AuthFile(DefaultAccount)); err == nil {
		t.Fatal("the default account was touched")
	}
}
