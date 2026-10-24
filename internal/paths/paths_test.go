package paths

import "testing"

func TestExcluded(t *testing.T) {
	for rel, want := range map[string]bool{
		"run":                       true,
		"cache":                     true,
		"backup.json":               true,
		"s3.json":                   true,
		"backup.migrated":           true,
		"tmux.sock":                 true,
		"updates.log":               true,
		"update-state":              true,
		"restore.pending":           true,
		".restore-123":              true,
		"socrates.db.pre-v9.bak":    true,
		"sessions/s1/journal.raw":   true,
		"sessions/s1/journal.raw.1": true,
		"vscode/ab12/logs":          true,
		"socrates.db":               false,
		"sessions/s1/plan.json":     false,
		"chats/c1/events.jsonl":     false,
		"agents/claude/projects":    false,
		"agents/opencode/cache":     true,
		"agents/opencode/config":    false,
		"agents/opencode/accounts":  false,
		// An OpenCode account's data and state homes keep only OpenCode's
		// own folder: the rest is the tools'.
		"agents/opencode/accounts/a1":                        false,
		"agents/opencode/accounts/a1/opencode":               false,
		"agents/opencode/accounts/a1/opencode/auth.json":     false,
		"agents/opencode/accounts/a1/state":                  false,
		"agents/opencode/accounts/a1/state/opencode":         false,
		"agents/opencode/accounts/a1/state/opencode/kv.json": false,
		"agents/opencode/accounts/a1/uv":                     true,
		"agents/opencode/accounts/a1/pipx":                   true,
		"agents/opencode/accounts/a1/state/nvim":             true,
		"accounts/a1/uv":                                     false,
		"vscode/ab12/User":                                   false,
		"accounts/a1/run":                                    false,
		"accounts/a1/log":                                    true,
		"accounts/a1/logs_2.sqlite":                          true,
		"accounts/a1/state_5.sqlite":                         false,
		"accounts/a1/.credentials.json":                      false,
		"secrets.key":                                        false,
		"accounts.migrated":                                  false,
		"chats/run":                                          false,
	} {
		if got := Excluded(rel); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestOwn(t *testing.T) {
	for rel, want := range map[string]bool{
		"chats": true, "chats/c1": true, "socrates.db": true, "agents/claude": true, "vscode-extensions/x": true,
		"workspaces/w1": false, "projects/p": false, "chatsx": false,
	} {
		if got := Own(rel); got != want {
			t.Errorf("Own(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestRel(t *testing.T) {
	l := New("/var/lib/socrates/")
	for p, want := range map[string]string{
		"/var/lib/socrates/chats/c1":   "chats/c1",
		"/var/lib/socrates/workspaces": "workspaces",
		"/var/lib/socrates":            "",
		"/var/lib/socrates-other/x":    "",
		"/root/x":                      "",
	} {
		got, ok := l.Rel(p)
		if got != want || ok != (want != "") {
			t.Errorf("Rel(%q) = %q %v, want %q", p, got, ok, want)
		}
	}
	if l.DB() != "/var/lib/socrates/socrates.db" || l.Path(Lock) != "/var/lib/socrates/run/socrates.lock" {
		t.Errorf("paths: %s %s", l.DB(), l.Path(Lock))
	}
	if l.PreRebuildBackup() != "/var/lib/socrates.pre-rebuild.tar" || l.SecretsKey() != "/var/lib/socrates/secrets.key" {
		t.Errorf("paths: %s %s", l.PreRebuildBackup(), l.SecretsKey())
	}
}
