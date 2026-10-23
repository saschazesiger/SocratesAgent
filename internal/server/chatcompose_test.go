//go:build linux

package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// upload posts files to a chat's attachments route the way the composer does.
func (c *chatEnv) upload(t *testing.T, id string, files map[string]string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, body := range files {
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(body))
	}
	_ = mw.Close()
	req, err := http.NewRequest("POST", c.server.URL+"/api/chats/"+id+"/attachments", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decoded)
	return res.StatusCode, decoded
}

// A message carries only files this chat's upload route handed out: the
// agent gets them, a copy sits beside the code, and a path anywhere else is
// refused.
func TestChatAttachmentsAreTheServersOwn(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	code, body := c.upload(t, id, map[string]string{"notes v2.txt": "hello"})
	if code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, body)
	}
	files := body["files"].([]any)
	file := files[0].(map[string]any)
	path := file["path"].(string)
	if filepath.Dir(path) != c.srv.chatAttachments(id) || file["name"] != "notes-v2.txt" || file["mime"] != "text/plain; charset=utf-8" {
		t.Fatalf("the upload answered %v", file)
	}
	if data, err := os.ReadFile(filepath.Join(c.data, ".socrates", "uploads", "notes-v2.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("the copy beside the code: %q %v", data, err)
	}

	for _, bad := range []string{"/etc/passwd", filepath.Join(c.srv.chatAttachments(id), "..", "events.jsonl"),
		filepath.Join(c.srv.chatAttachments(id), "missing.txt")} {
		code, body := c.post(t, "/api/chats/"+id+"/messages", map[string]any{"client_id": "bad-" + bad, "text": "x",
			"attachments": []map[string]any{{"name": "x", "path": bad}}})
		if code != http.StatusBadRequest {
			t.Fatalf("an attachment at %s was taken: %d %v", bad, code, body)
		}
	}

	// The browser's name and size are not believed; the file's are.
	code, body = c.post(t, "/api/chats/"+id+"/messages", map[string]any{"client_id": "m2", "text": "look",
		"attachments": []map[string]any{{"name": "evil", "path": path, "size": 999, "mime": "image/png"}}})
	if code != http.StatusOK {
		t.Fatalf("send with the attachment: %d %v", code, body)
	}
	events := c.waitJournal(t, id, 10*time.Second, "the reply", func(evs []agent.Event) bool {
		return strings.Contains(lastText(evs), "attachment")
	})
	if got := lastText(events); !strings.Contains(got, "with 1 attachment(s): notes-v2.txt") {
		t.Fatalf("the agent got %q", got)
	}
	p := agent.Fold(events)
	var sent *agent.Message
	for _, m := range p.Messages {
		if m.ClientID == "m2" {
			sent = m
		}
	}
	if sent == nil || len(sent.Attachments) != 1 || sent.Attachments[0].Size != 5 || sent.Attachments[0].Name != "notes-v2.txt" {
		t.Fatalf("the message journaled %+v", sent)
	}
	// A new chat has nothing uploaded that its first message could name.
	code, _ = c.post(t, "/api/chats", map[string]any{"client_id": "new-with-file", "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data,
		"message": map[string]any{"client_id": "first", "text": "x", "attachments": []map[string]any{{"name": "a", "path": path}}}})
	if code != http.StatusBadRequest {
		t.Fatalf("a first message with another chat's file: %d", code)
	}
}

// The model and the slash commands go through the outbox like a message: once
// by their client_id, and an unknown command says so.
func TestChatModelAndCommands(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "hello")
	c.waitJournal(t, id, 10*time.Second, "the first turn", turnsEnded(1))

	code, body := c.post(t, "/api/chats/"+id+"/model", map[string]any{"client_id": "mod1", "model": "fake-large", "effort": "high"})
	if code != http.StatusOK {
		t.Fatalf("model: %d %v", code, body)
	}
	code, body = c.post(t, "/api/chats/"+id+"/model", map[string]any{"client_id": "mod1", "model": "fake-large", "effort": "high"})
	if code != http.StatusOK || body["dup"] != true {
		t.Fatalf("the model change again: %d %v", code, body)
	}
	events := c.waitJournal(t, id, 5*time.Second, "the model change", func(evs []agent.Event) bool {
		return count(evs, isEv(agent.TypeModelChanged)) > 0
	})
	if p := agent.Fold(events); p.Model != "fake-large" || p.Effort != "high" {
		t.Fatalf("model %q effort %q", p.Model, p.Effort)
	}
	if n := count(events, isEv(agent.TypeModelChanged)); n != 1 {
		t.Fatalf("%d model changes", n)
	}
	if code, body := c.post(t, "/api/chats/"+id+"/model", map[string]any{"client_id": "mod2"}); code != http.StatusBadRequest {
		t.Fatalf("a change with no model: %d %v", code, body)
	}

	code, body = c.post(t, "/api/chats/"+id+"/command", map[string]any{"client_id": "cmd1", "name": "/compact"})
	if code != http.StatusOK {
		t.Fatalf("compact: %d %v", code, body)
	}
	c.waitJournal(t, id, 5*time.Second, "the compaction", func(evs []agent.Event) bool {
		return count(evs, func(ev agent.Event) bool { return ev.Type == agent.TypeNotice && ev.Code == "compacted" }) == 1
	})
	code, body = c.post(t, "/api/chats/"+id+"/command", map[string]any{"client_id": "cmd2", "name": "nonsense"})
	if code != http.StatusConflict || body["code"] != "unsupported" {
		t.Fatalf("an unknown command: %d %v", code, body)
	}
	p := agent.Fold(c.journal(t, id))
	var info struct {
		Commands []agent.CommandInfo `json:"commands"`
	}
	if err := json.Unmarshal(p.Agent, &info); err != nil || len(info.Commands) == 0 || info.Commands[0].Name != "compact" {
		t.Fatalf("the agent's commands: %s %v", p.Agent, err)
	}
}

// A chat keeps the composer's draft for its whole life, with the revision
// rule of a prepared session's prompt.
func TestChatDraftIsKeptWithRevisions(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	code, body := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/draft", `{"text":"half a thought","base_rev":0}`)
	if code.StatusCode != http.StatusOK || body["rev"] != float64(1) {
		t.Fatalf("save: %d %v", code.StatusCode, body)
	}
	code, body = c.do(t, c.client, "PUT", "/api/sessions/"+id+"/draft", `{"text":"other device","base_rev":0}`)
	if code.StatusCode != http.StatusConflict || body["text"] != "half a thought" {
		t.Fatalf("a stale save: %d %v", code.StatusCode, body)
	}
	_, body = c.do(t, c.client, "GET", "/api/sessions/"+id+"/draft", "")
	if body["text"] != "half a thought" {
		t.Fatalf("read back %v", body)
	}
}

// A prepared chat starts as a chat: its draft is the first message, sent once
// however often Start is pressed, and the draft is empty afterwards.
func TestPreparedChatStartsWithItsDraft(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	code, body := c.post(t, "/api/sessions", map[string]any{"client_id": "prep", "prestart": true, "harness": "fake",
		"model": "fake-small", "workdir_mode": "custom", "workdir": c.data, "prompt": "the first words"})
	if code != http.StatusCreated {
		t.Fatalf("prepare: %d %v", code, body)
	}
	row := body["session"].(map[string]any)
	id := row["id"].(string)
	if row["kind"] != store.KindChat || row["state"] != store.StatePrestart {
		t.Fatalf("prepared as %v / %v", row["kind"], row["state"])
	}
	if code, body := c.do(t, c.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"harness":"shell"}`); code.StatusCode != http.StatusBadRequest {
		t.Fatalf("a prepared chat turned into a shell: %d %v", code.StatusCode, body)
	}
	for i := 0; i < 2; i++ {
		code, body = c.post(t, "/api/sessions/"+id+"/start", map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("start %d: %d %v", i, code, body)
		}
	}
	started := body["session"].(map[string]any)
	if started["state"] != store.StateRunning || started["kind"] != store.KindChat {
		t.Fatalf("started as %v / %v", started["state"], started["kind"])
	}
	events := c.waitJournal(t, id, 10*time.Second, "the first turn", turnsEnded(1))
	users := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser })
	if users != 1 || !strings.Contains(lastText(events), "you said: the first words") {
		t.Fatalf("%d user messages, the agent said %q", users, lastText(events))
	}
	_, draft := c.do(t, c.client, "GET", "/api/sessions/"+id+"/draft", "")
	if draft["text"] != "" {
		t.Fatalf("the draft after the start: %v", draft)
	}
}

// The sheet learns which agents are chats, and the scripted one is offered
// with its models.
func TestCatalogNamesTheChats(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	_, body := c.do(t, c.client, "GET", "/api/harnesses", "")
	chats, _ := body["chats"].([]any)
	found := false
	for _, one := range chats {
		found = found || one == agent.FakeHarness
	}
	var fake map[string]any
	for _, one := range body["harnesses"].([]any) {
		if entry := one.(map[string]any); entry["id"] == agent.FakeHarness {
			fake = entry
		}
	}
	if !found || fake == nil || len(fake["models"].([]any)) != 2 {
		t.Fatalf("chats %v, the scripted agent %v", chats, fake)
	}
}
