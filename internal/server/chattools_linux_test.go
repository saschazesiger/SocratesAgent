//go:build linux

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

	if res := call(ep, chattools.Send, map[string]any{"chat_id": web}); !res.Error || !strings.Contains(res.Text, `{"chat": "the id list_chats gives", "text":`) {
		t.Fatalf("a call without the text says how to call it: %+v", res)
	}
	// An agent that calls the tool before it has loaded it guesses the
	// names of its arguments; the usual guesses are taken.
	res = call(ep, chattools.Send, map[string]any{"chat_id": web, "message": "please check the login route"})
	if res.Error || !strings.Contains(res.Text, "to “Web” (Scripted agent).") || !strings.Contains(res.Text, "end your turn now") {
		t.Fatalf("send: %+v", res)
	}
	evs := c.waitJournal(t, web, 15*time.Second, "the message answered", turnsEnded(2))
	notice, message := false, false
	for _, ev := range evs {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeChatMessage && strings.Contains(ev.Text, "“API”") {
			notice = true
		}
		// The journal has the message as it was written, the chat it came
		// from, and the note its agent got with it apart.
		if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser && ev.Text == "please check the login route" &&
			ev.Sender != nil && ev.Sender.Chat == api && ev.Sender.Title == "API" && ev.Sender.Harness == agent.FakeHarness &&
			strings.Contains(ev.Note, "not from your person: the agent of the chat “API” (Scripted agent), id "+api) {
			message = true
		}
	}
	if !notice || !message {
		t.Fatalf("no notice or message from API in %v", evs)
	}
	// The agent is given the note with the text, and answers the text alone
	// (the scripted agent's /note tells the note of the message before).
	c.send(t, web, "note-web", "/note")
	evs = c.waitJournal(t, web, 15*time.Second, "the note told", turnsEnded(3))
	told, echoed := false, false
	for _, ev := range evs {
		if ev.Type != agent.TypePart || ev.Kind != agent.PartText || ev.State != agent.PartDone {
			continue
		}
		told = told || strings.HasPrefix(ev.Text, "note: This message is from another agent, not from your person: the agent of the chat “API”")
		echoed = echoed || strings.HasPrefix(ev.Text, "you said: please check the login route (turn 2")
	}
	if !told || !echoed {
		t.Fatalf("the note for the agent (%v) or the echo of the text alone (%v) in %v", told, echoed, evs)
	}

	res = call(ep, chattools.Read, map[string]any{"chat": web, "count": 5})
	if res.Error || !strings.Contains(res.Text, "A message from the agent of the chat “API” (id "+api+"):\nplease check the login route") ||
		!strings.Contains(res.Text, "Its agent:\nyou said: please check the login route") {
		t.Fatalf("read: %+v", res)
	}

	// While the agent's own question to its person is open, it does not
	// answer the other chat for them.
	c.send(t, web, "ask-web", "/asknext")
	c.waitJournal(t, web, 15*time.Second, "the question", turnsEnded(4))
	// The server's own view of the chat follows the journal.
	webPeer, err := c.srv.chats.peer(web)
	if err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(10 * time.Second); ; {
		open := false
		_ = webPeer.view(func(proj *agent.Projection) { open = awaitsAnswer(proj) })
		if open {
			break
		}
		if time.Now().After(until) {
			t.Fatal("the server never saw the question open")
		}
		time.Sleep(20 * time.Millisecond)
	}
	webEP, err := c.srv.chatToolsEndpoint(web)
	if err != nil {
		t.Fatal(err)
	}
	if res := call(webEP, chattools.Send, map[string]any{"chat": api, "text": "Yes"}); !res.Error ||
		!strings.Contains(res.Text, "Your person has not answered your question yet") {
		t.Fatalf("a send while a question is open: %+v", res)
	}

	// A message of the person's own tells the agent which chats work beside
	// it; a shell line goes as it is.
	self, err := c.srv.chats.peer(api)
	if err != nil {
		t.Fatal(err)
	}
	if code, res := c.srv.sendMessage(context.Background(), self, &chatMessageBody{ClientID: "own-1", Text: "go on"}); code >= 300 {
		t.Fatalf("a message of the person's: %d %v", code, res)
	}
	if code, res := c.srv.sendMessage(context.Background(), self, &chatMessageBody{ClientID: "own-2", Text: "!ls"}); code >= 300 {
		t.Fatalf("a shell line: %d %v", code, res)
	}
	evs = c.waitJournal(t, api, 15*time.Second, "both messages", func(evs []agent.Event) bool {
		n := 0
		for _, ev := range evs {
			if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser {
				n++
			}
		}
		return n >= 2
	})
	for _, ev := range evs {
		if ev.Type != agent.TypeMessage || ev.Role != agent.RoleUser {
			continue
		}
		switch ev.Text {
		case "go on":
			if ev.Sender != nil || !strings.Contains(ev.Note, "Other chats work in this repository right now") ||
				!strings.Contains(ev.Note, "- “Web” (Scripted agent), id "+web) || strings.Contains(ev.Note, other) {
				t.Fatalf("the person's message: %+v", ev)
			}
		case "!ls":
			if ev.Note != "" {
				t.Fatalf("a shell line got a note: %+v", ev)
			}
		}
	}
}
