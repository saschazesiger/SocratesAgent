package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The agents of the chats in one repository reach each other through the
// chat tools, with the token of their own chat: list_chats names the other
// chats of the repository and no chat outside it, send_to_chat puts a
// message into one - marked as another chat's, and its agent answers it -
// and read_chat gives back what was said there. A chat in the background,
// done or not started yet is not there for them at all.
func TestTheChatsOfARepositoryReachEachOther(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	next := c.srv.Settings()
	next.Workspace.AllowCustom = true
	if err := c.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, dir := range []string{".git", "api", "web"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := t.TempDir()
	create := func(name, dir string) string {
		code, body := c.post(t, "/api/chats", map[string]any{"client_id": "create-" + name, "harness": "fake",
			"title": name, "workdir_mode": "custom", "workdir": dir})
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, body)
		}
		return body["session"].(map[string]any)["id"].(string)
	}
	api := create("API", filepath.Join(repo, "api"))
	web := create("Web", filepath.Join(repo, "web"))
	other := create("Other", elsewhere)
	draft := create("Draft", filepath.Join(repo, "web"))
	service := create("Service", repo)
	for _, id := range []string{web, service} {
		c.send(t, id, "start-"+id, "hello")
		c.waitJournal(t, id, 15*time.Second, "the first turn", turnsEnded(1))
	}
	if err := c.srv.store.SetSessionStatus(service, store.StatusBackground); err != nil {
		t.Fatal(err)
	}

	ep, err := c.srv.chatToolsEndpoint(api)
	if err != nil || !ep.Usable() || ep.Socket != agenthost.ToolsSocketPath(c.data) {
		t.Fatalf("the endpoint: %+v %v", ep, err)
	}
	call := func(ep chattools.Endpoint, tool string, args any) chattools.Response {
		raw, _ := json.Marshal(args)
		return ep.Call(context.Background(), tool, raw)
	}

	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/login-fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := call(ep, chattools.List, map[string]any{})
	if res.Error || !strings.Contains(res.Text, "id "+web+": “Web” (Scripted agent)") || strings.Contains(res.Text, other) ||
		!strings.Contains(res.Text, "in the same working tree as this chat, on the branch login-fix:") ||
		strings.Contains(res.Text, api) || !strings.Contains(res.Text, "folder web") || !strings.Contains(res.Text, "("+repo+")") ||
		!strings.Contains(res.Text, "1 other chat(s)") || strings.Contains(res.Text, draft) || strings.Contains(res.Text, service) {
		t.Fatalf("list: %+v", res)
	}
	for _, id := range []string{draft, service} {
		if res := call(ep, chattools.Send, map[string]any{"chat": id, "text": "hi"}); !res.Error || !strings.Contains(res.Text, "no chat") {
			t.Fatalf("a chat not started or in the background: %+v", res)
		}
		if res := call(ep, chattools.Read, map[string]any{"chat": id}); !res.Error {
			t.Fatalf("reading a chat not started or in the background: %+v", res)
		}
	}
	if res := call(ep, chattools.Send, map[string]any{"chat": other, "text": "hi"}); !res.Error || !strings.Contains(res.Text, "no chat") {
		t.Fatalf("a chat outside the repository: %+v", res)
	}
	if res := call(ep, chattools.Send, map[string]any{"chat": api, "text": "hi"}); !res.Error {
		t.Fatalf("itself: %+v", res)
	}
	if res := call(ep, chattools.Send, map[string]any{"chat": web, "text": "  "}); !res.Error {
		t.Fatalf("an empty message: %+v", res)
	}
	forged := chattools.Endpoint{Socket: ep.Socket, Token: web + "." + strings.SplitN(ep.Token, ".", 2)[1]}
	if res := call(forged, chattools.List, map[string]any{}); !res.Error || !strings.Contains(res.Text, "not known") {
		t.Fatalf("another chat's id with this chat's seal: %+v", res)
	}

	res = call(ep, chattools.Send, map[string]any{"chat": web, "text": "please check the login route"})
	if res.Error || !strings.Contains(res.Text, "Sent to “Web”") {
		t.Fatalf("send: %+v", res)
	}
	evs := c.waitJournal(t, web, 15*time.Second, "the message answered", turnsEnded(2))
	notice := false
	for _, ev := range evs {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeChatMessage && strings.Contains(ev.Text, "“API”") {
			notice = true
		}
	}
	if !notice {
		t.Fatalf("no notice in %v", evs)
	}

	res = call(ep, chattools.Read, map[string]any{"chat": web, "count": 5})
	if res.Error || !strings.Contains(res.Text, "The person:\n[A message from the chat “API” (Scripted agent), id "+api) ||
		!strings.Contains(res.Text, "please check the login route") || !strings.Contains(res.Text, "Its agent:\nyou said:") {
		t.Fatalf("read: %+v", res)
	}
}

// A chat sends at most chatSendsPerHour messages an hour.
func TestAChatsSendsAreCounted(t *testing.T) {
	tools := &chatToolServer{sends: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < chatSendsPerHour; i++ {
		if !tools.allowSend("a", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("send %d refused", i+1)
		}
	}
	if tools.allowSend("a", now.Add(time.Minute)) {
		t.Fatal("one more than the hour allows went")
	}
	if !tools.allowSend("b", now.Add(time.Minute)) {
		t.Fatal("another chat's send was refused")
	}
	if !tools.allowSend("a", now.Add(time.Hour+time.Second)) {
		t.Fatal("an hour on, the chat still could not send")
	}
}

// The branch is read from the working tree's HEAD, a linked worktree's too,
// and there is none for a detached HEAD or a folder that is no repository.
func TestGitBranch(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	if got := gitBranch(main); got != "main" {
		t.Fatalf("main: %q", got)
	}
	wt := t.TempDir()
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n")
	write(filepath.Join(main, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/feature/x\n")
	if got := gitBranch(wt); got != "feature/x" {
		t.Fatalf("worktree: %q", got)
	}
	write(filepath.Join(main, ".git", "HEAD"), "4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
	if got := gitBranch(main); got != "" {
		t.Fatalf("detached: %q", got)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Fatalf("no repository: %q", got)
	}
}

// The repository of a folder is the nearest folder with a .git, else the
// folder itself.
func TestChatScope(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := chatScope(filepath.Join(repo, "a", "b")); got != repo {
		t.Fatalf("below a repository: %s", got)
	}
	plain := t.TempDir()
	if got := chatScope(plain); got != plain {
		t.Fatalf("no repository: %s", got)
	}
	if chatScope("") != "" {
		t.Fatal("no folder has a scope")
	}
}
