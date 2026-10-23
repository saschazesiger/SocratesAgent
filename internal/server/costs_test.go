package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// A pass reads every profile's transcripts into the ledger, names a
// conversation after the session it ran in, keeps that name once the session
// is gone, and the page gets all of it in one answer.
func TestCostsLedgerAndPage(t *testing.T) {
	env := adminEnv(t)
	empty := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(empty, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(empty, "codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(empty, "xdg"))

	root := env.srv.Settings().Workspace.Root
	dataDir := env.srv.dataDir
	writeTranscript := func(profile, conv, cwd string) {
		path := filepath.Join(dataDir, "profiles", profile, "claude", "projects", "-x", conv+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"assistant","timestamp":"2026-10-20T10:15:00Z","cwd":"` + cwd + `","message":{"id":"m-` + conv +
			`","model":"claude-opus-5-5","usage":{"input_tokens":0,"output_tokens":1000000}}}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTranscript("p-one", "conv-a", "/root/project")
	writeTranscript("p-gone", "conv-b", filepath.Join(root, "claude-20261020-x"))

	row := &store.Session{ID: "s1", Title: "Build the thing", Harness: "claude", Workdir: "/root/project",
		WorkdirMode: store.WorkdirPreset, State: store.StateExited}
	if err := env.store.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetSessionCLI("s1", "conv-a", store.CLIKnown); err != nil {
		t.Fatal(err)
	}

	env.srv.costs.pass()
	ledger, err := env.store.GetCostLedger("claude/conv-a")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Title != "Build the thing" || ledger.SessionID != "s1" || ledger.Profile != "p-one" ||
		len(ledger.Rows) != 1 || ledger.Rows[0].CostUSD != 20 {
		t.Fatalf("ledger = %+v", ledger)
	}

	// The session goes; its conversation keeps the name it had.
	if err := env.store.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	env.srv.costs.pass()
	if ledger, _ := env.store.GetCostLedger("claude/conv-a"); ledger.Title != "Build the thing" {
		t.Fatalf("the name went with the session: %+v", ledger)
	}

	res, body := env.do(t, env.client, "GET", "/api/costs", "")
	if res.StatusCode != 200 {
		t.Fatalf("GET /api/costs = %d", res.StatusCode)
	}
	convs, _ := body["conversations"].([]any)
	rows, _ := body["rows"].([]any)
	if len(convs) != 2 || len(rows) != 2 {
		t.Fatalf("answer = %v", body)
	}
	projects := map[string]bool{}
	for _, c := range convs {
		conv := c.(map[string]any)
		projects[conv["project"].(string)] = true
		if conv["live"] == true {
			t.Errorf("a deleted session's conversation is live: %v", conv)
		}
	}
	if !projects["/root/project"] || !projects[moreProject] {
		t.Fatalf("projects = %v", projects)
	}
	if res, _ := env.do(t, env.anon, "GET", "/api/costs", ""); res.StatusCode != 401 {
		t.Fatalf("an anonymous GET /api/costs = %d", res.StatusCode)
	}
}
