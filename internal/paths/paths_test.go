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
		"vscode/ab12/User":          false,
		"accounts/a1/run":           false,
		"chats/run":                 false,
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
}
