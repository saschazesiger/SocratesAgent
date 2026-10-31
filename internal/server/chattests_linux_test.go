//go:build linux

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/chattests"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Socrates Tests is one of a chat's MCP servers, off until switched on; its
// agent writes and runs tests through it on the tools socket, and the
// chat's page reads, edits, overrides and deletes them, and opens their
// files, through the API.
func TestAChatsTestsAreWrittenByItsAgentAndShownToThePerson(t *testing.T) {
	e := signedIn(t)
	workdir := t.TempDir()
	chat := mcpChat(t, e, workdir)
	_, body := e.do(t, e.client, "GET", "/api/sessions/"+chat+"/mcp", "")
	var found map[string]any
	for _, s := range body["servers"].([]any) {
		if m := s.(map[string]any); m["name"] == tests.Name {
			found = m
		}
	}
	if found == nil || found["on"] != false || found["title"] != "Socrates Tests" {
		t.Fatalf("the built-in server: %v", body)
	}
	res, body := e.do(t, e.client, "GET", "/api/chats/"+chat+"/tests", "")
	if res.StatusCode != http.StatusOK || len(body["tests"].([]any)) != 0 || body["on"] != false {
		t.Fatalf("no tests yet: %d %v", res.StatusCode, body)
	}
	if _, err := e.srv.store.UpdateChatOptions(chat, func(raw json.RawMessage) json.RawMessage {
		return withOption(raw, "mcp", map[string]bool{tests.Name: true})
	}); err != nil {
		t.Fatal(err)
	}

	call := func(tool, args string) mcpserver.Result {
		t.Helper()
		var r mcpserver.Result
		if code := socketPost(t, e, chat, mcpserver.Request{Server: tests.Name, Method: "call", Tool: tool, Arguments: json.RawMessage(args)}, &r); code != http.StatusOK {
			t.Fatalf("%s: %d", tool, code)
		}
		return r
	}
	if r := call(tests.Create, `{"title":"Checkout","steps":[{"action":"Open the cart","expect":"The items"},{"action":"Pay","expect":"A receipt"}]}`); r.IsError {
		t.Fatalf("create: %+v", r)
	}
	if err := os.WriteFile(filepath.Join(workdir, "cart.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "page.html"), []byte("<script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	call(tests.Start, `{"test":"T1"}`)
	if r := call(tests.Step, `{"test":"T1","step":1,"verdict":"pass","observed":"Two items","files":["cart.png","page.html"]}`); r.IsError {
		t.Fatalf("step: %+v", r)
	}

	res, body = e.do(t, e.client, "GET", "/api/chats/"+chat+"/tests", "")
	list := body["tests"].([]any)
	if res.StatusCode != http.StatusOK || len(list) != 1 || body["on"] != true {
		t.Fatalf("the list: %d %v", res.StatusCode, body)
	}
	first := list[0].(map[string]any)
	if first["id"] != "T1" || first["status"] != "running" {
		t.Fatalf("the test: %v", first)
	}
	evidence := first["latest"].(map[string]any)["steps"].([]any)[0].(map[string]any)["evidence"].([]any)
	if len(evidence) != 2 {
		t.Fatalf("evidence: %v", evidence)
	}
	file := func(i int) string { return evidence[i].(map[string]any)["file"].(string) }

	// The files: an image as one, a page as text, never as a page.
	get := func(path string) (*http.Response, string) {
		t.Helper()
		res, err := e.client.Get(e.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	res, _ = get("/api/chats/" + chat + "/tests/T1/files/" + file(0))
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the image: %d %v", res.StatusCode, res.Header)
	}
	res, text := get("/api/chats/" + chat + "/tests/T1/files/" + file(1))
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(text, "<script>") {
		t.Fatalf("the page: %d %v", res.StatusCode, res.Header)
	}
	// A path out of the test's files is cleaned by the router (a redirect
	// to where nothing is) or refused.
	noRedirect := *e.client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, bad := range []string{"../tests.json", "..%2Ftests.json", "r1/../../../tests.json", "nothing.png", "r1"} {
		res, err := noRedirect.Get(e.server.URL + "/api/chats/" + chat + "/tests/T1/files/" + bad)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK || res.StatusCode >= 500 {
			t.Fatalf("%s: %d", bad, res.StatusCode)
		}
	}

	// The person edits it, overrides it and deletes it.
	res, body = e.do(t, e.client, "PATCH", "/api/chats/"+chat+"/tests/T1", `{"title":"Checkout with a card"}`)
	if res.StatusCode != http.StatusOK || body["test"].(map[string]any)["title"] != "Checkout with a card" {
		t.Fatalf("edit: %d %v", res.StatusCode, body)
	}
	if res, _ := e.do(t, e.client, "PATCH", "/api/chats/"+chat+"/tests/T1", `{"steps":[]}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an edit without steps: %d", res.StatusCode)
	}
	call(tests.Finish, `{"test":"T1","summary":"Paying hangs."}`)
	res, body = e.do(t, e.client, "POST", "/api/chats/"+chat+"/tests/T1/override", `{"status":"passed","note":"Pay is a stub here"}`)
	if res.StatusCode != http.StatusOK || body["test"].(map[string]any)["status"] != "passed" {
		t.Fatalf("override: %d %v", res.StatusCode, body)
	}
	if r := call(tests.List, `{}`); !strings.Contains(r.Content[0].Text, "Pay is a stub here") {
		t.Fatalf("the agent does not see the override: %s", r.Content[0].Text)
	}
	if res, _ := e.do(t, e.client, "POST", "/api/chats/"+chat+"/tests/T1/override", `{"status":"running"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an override of running: %d", res.StatusCode)
	}
	res, body = e.do(t, e.client, "GET", "/api/chats/"+chat+"/tests/T1", "")
	if res.StatusCode != http.StatusOK || len(body["test"].(map[string]any)["events"].([]any)) < 3 {
		t.Fatalf("one test: %d %v", res.StatusCode, body)
	}
	if res, _ := e.do(t, e.client, "DELETE", "/api/chats/"+chat+"/tests/T1", ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if res, _ := e.do(t, e.client, "GET", "/api/chats/"+chat+"/tests/T1", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a deleted test: %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(agenthost.ChatDir(e.srv.dataDir, chat), "tests", "files", "T1")); !os.IsNotExist(err) {
		t.Fatalf("the files of a deleted test: %v", err)
	}
	if res, _ := e.do(t, e.client, "GET", "/api/chats/nochat/tests", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("no chat: %d", res.StatusCode)
	}
}

// The person's "Run again" says which test, and the override it had.
func TestRunAgainNamesTheTest(t *testing.T) {
	e := signedIn(t)
	chat := mcpChat(t, e, t.TempDir())
	if _, err := e.srv.chatTests.Create(chat, chattestsFields("Search"), "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.chatTests.SetOverride(chat, "T1", "blocked", "Needs the index"); err != nil {
		t.Fatal(err)
	}
	test, _ := e.srv.chatTests.Get(chat, "T1")
	if got := rerunText(test); got != "Run test T1 “Search” again. I had set it to Blocked: Needs the index" {
		t.Fatalf("the message: %q", got)
	}
}

// What test mode left behind goes when the server starts: its sessions and
// its folders, and nothing else.
func TestTestModeIsForgotten(t *testing.T) {
	e := signedIn(t)
	for _, row := range []*store.Session{
		{ID: "cell1", Title: "A cell", Harness: "fake", Kind: store.KindChat, State: store.StateRunning, Options: json.RawMessage(`{"test":{"run":"r-1"}}`)},
		{ID: "chat1", Title: "A chat", Harness: "fake", Kind: store.KindChat, State: store.StateRunning, Options: json.RawMessage(`{"mcp":{"x":true}}`)},
		{ID: "chat2", Title: "Another", Harness: "fake", Kind: store.KindChat, State: store.StateRunning},
	} {
		if err := e.srv.store.CreateSession(row); err != nil {
			t.Fatal(err)
		}
	}
	layout := paths.New(e.srv.dataDir)
	for _, rel := range []string{paths.TestMode + "/shop-1/runs", paths.TestModeRuntime + "/r-1", paths.TestModeCache + "/tools"} {
		if err := os.MkdirAll(layout.Path(rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.srv.forgetTestMode(t.Context())
	if _, err := e.srv.store.GetSession("cell1"); err == nil {
		t.Fatal("test mode's session is still there")
	}
	for _, id := range []string{"chat1", "chat2"} {
		if _, err := e.srv.store.GetSession(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	for _, rel := range []string{paths.TestMode, paths.TestModeRuntime, paths.TestModeCache} {
		if _, err := os.Stat(layout.Path(rel)); !os.IsNotExist(err) {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	if _, err := os.Stat(layout.Path(paths.Run)); err != nil {
		t.Fatalf("run/ went too: %v", err)
	}
}

func chattestsFields(title string) chattests.Fields {
	return chattests.Fields{Title: title, Steps: []chattests.Step{{Action: "Search for a shoe", Expect: "Shoes"}}}
}
