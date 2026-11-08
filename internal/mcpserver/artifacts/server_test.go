package artifacts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/artifacts"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

type env struct {
	t     *testing.T
	srv   *Server
	store *artifacts.Store
	sess  mcpserver.Session
}

func newEnv(t *testing.T) *env {
	data := t.TempDir()
	st := artifacts.New(func(chat string) string { return filepath.Join(data, chat) })
	return &env{t: t, srv: New(Deps{Store: st}), store: st, sess: mcpserver.Session{ID: "c1", Workdir: t.TempDir()}}
}

func (e *env) call(tool, args string, wantErr bool) string {
	e.t.Helper()
	r := e.srv.Call(context.Background(), e.sess, tool, json.RawMessage(args))
	if len(r.Content) != 1 || r.IsError != wantErr {
		e.t.Fatalf("%s %s: error %v, want %v: %+v", tool, args, r.IsError, wantErr, r)
	}
	return r.Content[0].Text
}

func TestEveryToolIsDescribedAndNamedInTheInstructions(t *testing.T) {
	s := New(Deps{})
	seen := map[string]bool{}
	for _, tool := range s.Tools() {
		if tool.Description == "" || tool.Schema["type"] != "object" || seen[tool.Name] {
			t.Fatalf("tool %+v", tool)
		}
		seen[tool.Name] = true
		if !strings.Contains(s.Instructions(), tool.Name) {
			t.Errorf("the instructions do not name %s", tool.Name)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("%d tools", len(seen))
	}
}

func TestToolsEndToEnd(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(filepath.Join(e.sess.Workdir, "chart.png"), []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.call(List, `{}`, false); !strings.Contains(got, "no artifacts yet") {
		t.Fatal(got)
	}
	got := e.call(Create, `{"title":"Flow","format":"markdown","content":"# Flow\n\n`+"```mermaid\\nflowchart LR\\nA-->B\\n```"+`","files":["chart.png"]}`, false)
	if !strings.Contains(got, "Created A1 “Flow” (Markdown, version 1)") || !strings.Contains(got, "chart.png") {
		t.Fatal(got)
	}
	got = e.call(Update, `{"artifact":"Flow","edits":[{"old":"A-->B","new":"A-->B-->C"}],"note":"Add C"}`, false)
	if !strings.Contains(got, "version 2 saved") {
		t.Fatal(got)
	}
	got = e.call(Read, `{"artifact":"a1"}`, false)
	if !strings.Contains(got, "A-->B-->C") || !strings.Contains(got, "2: ") || !strings.Contains(got, filepath.Join("files", "A1", "chart.png")) {
		t.Fatal(got)
	}
	e.call(Update, `{"artifact":"A1","edits":[{"old":"nope","new":"x"}]}`, true)
	e.call(Update, `{"artifact":"A1","bogus":1}`, true)
	e.call(Create, `{"title":"x"}`, true)
	got = e.call(Delete, `{"artifact":"A1"}`, false)
	if !strings.Contains(got, "Deleted A1") {
		t.Fatal(got)
	}
	e.call(Read, `{"artifact":"A1"}`, true)
}

func TestNeedsAChat(t *testing.T) {
	e := newEnv(t)
	e.sess.ID = ""
	e.call(List, `{}`, true)
	if r := New(Deps{}).Call(context.Background(), mcpserver.Session{ID: "c"}, List, nil); !r.IsError {
		t.Fatal("ran without a store")
	}
}
