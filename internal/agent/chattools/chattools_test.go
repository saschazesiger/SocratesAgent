package chattools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
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

// A chat's endpoint has the three chat tools and their guidance, and is read
// back from the environment it gives `socrates mcp-ask`.
func TestAnEndpointHasTheChatTools(t *testing.T) {
	var names []string
	chat := Endpoint{Socket: "/s", Token: "c.x"}
	for _, d := range chat.Tools() {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "list_chats,read_chat,send_to_chat" || !chat.Has(Send) || chat.Has("test_step") {
		t.Fatalf("a chat's tools: %v", names)
	}
	if g := chat.Guidance("p_", true); !strings.HasPrefix(g, Guidance("p_")) || !strings.Contains(g, "select:p_list_chats,p_read_chat,p_send_to_chat") {
		t.Fatalf("a chat's guidance: %s", g)
	}
	t.Setenv(EnvSocket, "/s")
	t.Setenv(EnvToken, "c.x")
	if got := FromEnv(); got != chat {
		t.Fatalf("from the environment: %+v", got)
	}
}

// A chat in a repository has open_pull_request beside the three, and the
// guidance of its own worktree instead of the shared tree's; both go through
// the environment `socrates mcp-ask` is given. A chat in a folder keeps the
// old text.
func TestARepositoryChatHasItsOwnGuidance(t *testing.T) {
	repo := &Repo{FullName: "octo/app", Branch: "socrates/fix", Base: "main", Worktree: "/w/app-abcdef12"}
	chat := Endpoint{Socket: "/s", Token: "c.x", Repo: repo}
	var names []string
	for _, d := range chat.Tools() {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "list_chats,read_chat,send_to_chat,open_pull_request" || !chat.Has(PullRequest) {
		t.Fatalf("a repository chat's tools: %v", names)
	}
	if (Endpoint{Socket: "/s", Token: "c.x"}).Has(PullRequest) {
		t.Fatal("a folder chat has open_pull_request")
	}
	g := chat.Guidance("p_", true)
	for _, want := range []string{"your own git worktree of `octo/app` at `/w/app-abcdef12`", "branch `socrates/fix`",
		"made from `main`", "p_open_pull_request", "git rebase origin/main", "git push origin HEAD:main",
		"select:p_list_chats,p_read_chat,p_send_to_chat,p_open_pull_request", "Send nothing else"} {
		if !strings.Contains(g, want) {
			t.Errorf("the repository guidance lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "shares this working tree") || strings.Contains(g, "setup failed") {
		t.Fatalf("the repository guidance speaks of a shared tree or a failed setup:\n%s", g)
	}
	if old := (Endpoint{Socket: "/s", Token: "c.x"}).Guidance("p_", false); !strings.Contains(old, "shares this working tree") {
		t.Fatalf("the folder guidance changed:\n%s", old)
	}
	failed := *repo
	failed.SetupFailure, failed.SetupLog = "exit status 1", "/data/chats/c/setup.log"
	if g := (Endpoint{Socket: "/s", Token: "c.x", Repo: &failed}).Guidance("", false); !strings.Contains(g, "setup failed when this worktree was made: exit status 1") ||
		!strings.Contains(g, "/data/chats/c/setup.log") {
		t.Fatalf("a failed setup is not named:\n%s", g)
	}
	for k, v := range chat.Env() {
		t.Setenv(k, v)
	}
	if got := FromEnv(); got.Repo == nil || *got.Repo != *repo || got.Token != "c.x" {
		t.Fatalf("from the environment: %+v", got)
	}
}
