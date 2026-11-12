package config

import (
	"reflect"
	"testing"
)

func overlaid(t *testing.T, s Settings, doc string) Settings {
	t.Helper()
	if err := s.Overlay([]byte(doc)); err != nil {
		t.Fatalf("Overlay(%s): %v", doc, err)
	}
	return s
}

// A server deleted in Admin takes its token, its sign-in and its note with
// it: the one that moves into its place in the list does not inherit them.
func TestOverlayDeletedServerKeepsNothingBehind(t *testing.T) {
	live := Default()
	live.MCP.Servers = []MCPServerSetting{
		{Name: "a", Type: MCPHTTP, URL: "https://a.example/mcp", Headers: map[string]string{"Authorization": "Bearer a"}, Auth: MCPAuthOAuth, Description: "A", DisabledTools: []string{"x"}},
		{Name: "b", Type: MCPHTTP, URL: "https://b.example/mcp"},
	}
	next := overlaid(t, live, `{"mcp":{"servers":[{"name":"b","type":"http","url":"https://b.example/mcp","headers":{},"default":false}]}}`)
	want := []MCPServerSetting{{Name: "b", Type: MCPHTTP, URL: "https://b.example/mcp", Headers: map[string]string{}}}
	if !reflect.DeepEqual(next.MCP.Servers, want) {
		t.Fatalf("servers = %+v, want %+v", next.MCP.Servers, want)
	}
	// The live settings share their lists with the copy; they are not
	// touched.
	if live.MCP.Servers[0].Headers["Authorization"] != "Bearer a" || live.MCP.Servers[1].Name != "b" || len(live.MCP.Servers) != 2 {
		t.Fatalf("the live servers changed: %+v", live.MCP.Servers)
	}
}

// A header cleared in Admin goes, and so does a model's instructions.
func TestOverlayReplacesMaps(t *testing.T) {
	live := Default()
	live.MCP.Servers = []MCPServerSetting{{Name: "a", Type: MCPHTTP, URL: "https://a.example/mcp", Headers: map[string]string{"Authorization": "Bearer a", "X": "1"}}}
	live.Harnesses.Claude.Models = []ModelPick{{ID: "opus", Instructions: "be brief"}, {ID: "sonnet"}}
	next := overlaid(t, live, `{"mcp":{"servers":[{"name":"a","type":"http","url":"https://a.example/mcp","headers":{"X":"2"}}]},
		"harnesses":{"claude":{"models":[{"id":"sonnet"}]}}}`)
	if got := next.MCP.Servers[0].Headers; !reflect.DeepEqual(got, map[string]string{"X": "2"}) {
		t.Fatalf("headers = %v", got)
	}
	if got := next.Harnesses.Claude.Models; !reflect.DeepEqual(got, []ModelPick{{ID: "sonnet"}}) {
		t.Fatalf("models = %+v", got)
	}
	if live.MCP.Servers[0].Headers["Authorization"] != "Bearer a" {
		t.Fatal("the live headers changed")
	}
}

// What the document leaves out keeps its value: a whole section, a field of
// a section, a field of an embedded struct, and a scalar sent as null.
func TestOverlayKeepsWhatIsLeftOut(t *testing.T) {
	live := Default()
	live.OpenRouter.APIKey = "key"
	live.OpenRouter.TitleModel = "title"
	live.Harnesses.Claude.Binary = "/opt/claude"
	live.Harnesses.Claude.DefaultModel = "opus"
	live.MCP.Servers = []MCPServerSetting{{Name: "a", Type: MCPHTTP, URL: "https://a.example/mcp"}}
	next := overlaid(t, live, `{"openrouter":{"title_model":"other","api_key":null},"harnesses":{"claude":{"enabled":false}}}`)
	if next.OpenRouter.APIKey != "key" || next.OpenRouter.TitleModel != "other" {
		t.Fatalf("openrouter = %+v", next.OpenRouter)
	}
	c := next.Harnesses.Claude
	if c.Enabled || c.Binary != "/opt/claude" || c.DefaultModel != "opus" {
		t.Fatalf("claude = %+v", c)
	}
	if !next.Harnesses.Shell.Enabled {
		t.Fatal("shell was switched off by a document that did not mention it")
	}
	if len(next.MCP.Servers) != 1 {
		t.Fatalf("servers = %+v", next.MCP.Servers)
	}
	// A list sent as null is emptied, as encoding/json does.
	if next = overlaid(t, live, `{"mcp":{"servers":null}}`); next.MCP.Servers != nil {
		t.Fatalf("servers = %+v", next.MCP.Servers)
	}
}

// Keys match without regard to case, as in encoding/json, and a value of the
// wrong type is an error.
func TestOverlayKeysAndErrors(t *testing.T) {
	next := overlaid(t, Default(), `{"OpenRouter":{"API_KEY":"k"},"unknown":{"x":1}}`)
	if next.OpenRouter.APIKey != "k" {
		t.Fatalf("api key = %q", next.OpenRouter.APIKey)
	}
	s := Default()
	for _, doc := range []string{`[]`, `{"chats":{"park_after_minutes":"ten"}}`, `{"mcp":{"servers":{}}}`, `{"mcp":"x"}`} {
		if err := s.Overlay([]byte(doc)); err == nil {
			t.Errorf("Overlay(%s) took it", doc)
		}
	}
}
