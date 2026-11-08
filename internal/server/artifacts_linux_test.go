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

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	artifactsmcp "github.com/saschazesiger/SocratesAgent/internal/mcpserver/artifacts"
)

// Socrates Artifacts is one of a chat's MCP servers, off until switched on;
// its agent makes pages through it on the tools socket, the chat's page
// lists and deletes them through the API, and a page is served sandboxed
// at its own address, to the signed-in person, its files to whoever has
// the address.
func TestAChatsArtifactsAreMadeByItsAgentAndServedSandboxed(t *testing.T) {
	e := signedIn(t)
	workdir := t.TempDir()
	chat := mcpChat(t, e, workdir)
	_, body := e.do(t, e.client, "GET", "/api/sessions/"+chat+"/mcp", "")
	var found map[string]any
	for _, s := range body["servers"].([]any) {
		if m := s.(map[string]any); m["name"] == artifactsmcp.Name {
			found = m
		}
	}
	if found == nil || found["on"] != false || found["title"] != "Socrates Artifacts" {
		t.Fatalf("the built-in server: %v", body)
	}
	res, body := e.do(t, e.client, "GET", "/api/chats/"+chat+"/artifacts", "")
	if res.StatusCode != http.StatusOK || len(body["artifacts"].([]any)) != 0 || body["on"] != false {
		t.Fatalf("no artifacts yet: %d %v", res.StatusCode, body)
	}
	if _, err := e.srv.store.UpdateChatOptions(chat, func(raw json.RawMessage) json.RawMessage {
		return withOption(raw, "mcp", map[string]bool{artifactsmcp.Name: true})
	}); err != nil {
		t.Fatal(err)
	}
	call := func(tool, args string) mcpserver.Result {
		t.Helper()
		var r mcpserver.Result
		if code := socketPost(t, e, chat, mcpserver.Request{Server: artifactsmcp.Name, Method: "call", Tool: tool, Arguments: json.RawMessage(args)}, &r); code != http.StatusOK {
			t.Fatalf("%s: %d", tool, code)
		}
		return r
	}
	if err := os.WriteFile(filepath.Join(workdir, "chart.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := call(artifactsmcp.Create, `{"title":"Flow","content":"<h1>Flow</h1><img src=\"chart.svg\">","files":["chart.svg"]}`); r.IsError {
		t.Fatalf("create: %+v", r)
	}
	if r := call(artifactsmcp.Update, `{"artifact":"A1","edits":[{"old":"<h1>Flow</h1>","new":"<h1>Request flow</h1>"}],"note":"Heading"}`); r.IsError {
		t.Fatalf("update: %+v", r)
	}

	res, body = e.do(t, e.client, "GET", "/api/chats/"+chat+"/artifacts", "")
	list := body["artifacts"].([]any)
	if res.StatusCode != http.StatusOK || len(list) != 1 || body["on"] != true {
		t.Fatalf("the list: %d %v", res.StatusCode, body)
	}
	a := list[0].(map[string]any)
	url, _ := a["url"].(string)
	if a["id"] != "A1" || a["version"] != float64(2) || len(a["versions"].([]any)) != 2 || !strings.HasPrefix(url, "/artifacts/"+chat+"/A1/") {
		t.Fatalf("the artifact: %v", a)
	}

	get := func(c *http.Client, path string) (*http.Response, string) {
		t.Helper()
		res, err := c.Get(e.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	// The page: in the kit, sandboxed, the latest version or the one asked.
	res, text := get(e.client, url)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox allow-scripts") ||
		strings.Contains(res.Header.Get("Content-Security-Policy"), "allow-same-origin") ||
		!strings.Contains(text, "<h1>Request flow</h1>") || !strings.Contains(text, "/artifact-kit/kit.css?v=") {
		t.Fatalf("the page: %d %v %s", res.StatusCode, res.Header, text)
	}
	if _, text := get(e.client, url+"?v=1"); !strings.Contains(text, "<h1>Flow</h1>") {
		t.Fatalf("version 1: %s", text)
	}
	// Signed out, the page is refused, and its file is not.
	anon := &http.Client{}
	if res, _ := get(anon, url); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the page signed out: %d", res.StatusCode)
	}
	res, _ = get(anon, url+"chart.svg")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/svg+xml" || res.Header.Get("Content-Security-Policy") != "sandbox" ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("the file: %d %v", res.StatusCode, res.Header)
	}
	// A wrong key, a wrong id, a name it has not: nothing.
	wrong := strings.Replace(url, "/A1/", "/A1/0", 1)
	for _, bad := range []string{wrong, wrong + "chart.svg", "/artifacts/" + chat + "/a1/" + strings.Split(url, "/")[4] + "/", url + "artifacts.json", url + "..%2F..%2Fartifacts.json"} {
		if res, _ := get(e.client, bad); res.StatusCode == http.StatusOK {
			t.Fatalf("%s was served", bad)
		}
	}
	// The kit, to any origin.
	res, text = get(anon, "/artifact-kit/kit.js")
	if res.StatusCode != http.StatusOK || res.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(text, "marked.esm.js") {
		t.Fatalf("the kit: %d %v", res.StatusCode, res.Header)
	}
	if res, _ := get(anon, "/artifact-kit/nothing.js"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a file not in the kit: %d", res.StatusCode)
	}

	// The person deletes it.
	if res, _ := e.do(t, e.client, "DELETE", "/api/chats/"+chat+"/artifacts/A1", ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if res, _ := get(e.client, url); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a deleted page: %d", res.StatusCode)
	}
	if res, _ := e.do(t, e.client, "DELETE", "/api/chats/"+chat+"/artifacts/A1", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again: %d", res.StatusCode)
	}
}
