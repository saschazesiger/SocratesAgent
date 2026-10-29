package chattools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// A call reaches the server's socket with the chat's token and the tool's
// arguments, and what the server answers is what the model is told; a
// server that is not there is an error the model is told, not a failure.
func TestACallReachesTheServer(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "t.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	var auth string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(Response{Text: "2 other chats", Error: false})
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ep := Endpoint{Socket: sock, Token: "c1.seal"}
	res := ep.Call(context.Background(), List, json.RawMessage(`{"x":1}`))
	if res.Error || res.Text != "2 other chats" || auth != "Bearer c1.seal" || got.Tool != List || string(got.Arguments) != `{"x":1}` {
		t.Fatalf("res %+v, auth %q, request %+v", res, auth, got)
	}
	if res := (Endpoint{}).Call(context.Background(), List, nil); !res.Error {
		t.Fatalf("no endpoint: %+v", res)
	}
	gone := Endpoint{Socket: filepath.Join(t.TempDir(), "none.sock"), Token: "x"}
	if res := gone.Call(context.Background(), List, nil); !res.Error || res.Text == "" {
		t.Fatalf("no server: %+v", res)
	}
	if env := ep.Env(); env[EnvSocket] != sock || env[EnvToken] != "c1.seal" || (Endpoint{}).Env() != nil {
		t.Fatalf("env %v", env)
	}
}

// A test session's endpoint has its role's tools instead of the chat tools,
// its protocol instead of their guidance, carries its kind to `socrates
// mcp-ask`, and does not talk of other chats when it cannot be reached.
func TestATestEndpointHasItsRolesTools(t *testing.T) {
	names := func(e Endpoint) string {
		var out []string
		for _, d := range e.Tools() {
			out = append(out, d.Name)
		}
		return strings.Join(out, ",")
	}
	chat := Endpoint{Socket: "/s", Token: "c.x"}
	if chat.Test() || names(chat) != "list_chats,read_chat,send_to_chat" || !chat.Has(Send) || chat.Has(testtools.Judge) {
		t.Fatalf("a chat's tools: %s", names(chat))
	}
	if g := chat.Guidance("p_", true); !strings.HasPrefix(g, Guidance("p_")) || !strings.Contains(g, "select:p_list_chats,p_read_chat,p_send_to_chat") {
		t.Fatalf("a chat's guidance: %s", g)
	}
	cell := Endpoint{Socket: "/s", Token: "c.x", Kind: testtools.KindCell}
	if !cell.Test() || names(cell) != "test_steps,test_judge,test_end,test_abort" || !cell.Has(testtools.Judge) || cell.Has(List) {
		t.Fatalf("a cell's tools: %s", names(cell))
	}
	if cell.Guidance("p_", false) != testtools.Guidance(testtools.KindCell, "p_", false) {
		t.Fatal("a cell's guidance is not its role's")
	}
	if env := cell.Env(); env[EnvKind] != testtools.KindCell {
		t.Fatalf("env %v", env)
	}
	if _, ok := chat.Env()[EnvKind]; ok {
		t.Fatal("a chat's environment names a kind")
	}
	t.Setenv(EnvSocket, "/s")
	t.Setenv(EnvToken, "c.x")
	t.Setenv(EnvKind, testtools.KindReviewer)
	if got := FromEnv(); got != (Endpoint{Socket: "/s", Token: "c.x", Kind: testtools.KindReviewer}) {
		t.Fatalf("from the environment: %+v", got)
	}
	gone := Endpoint{Socket: filepath.Join(t.TempDir(), "none.sock"), Token: "x", Kind: testtools.KindCell}
	if res := gone.Call(context.Background(), testtools.Steps, nil); !res.Error || strings.Contains(res.Text, "chats") {
		t.Fatalf("no server: %+v", res)
	}
}
