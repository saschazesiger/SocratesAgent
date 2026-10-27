//go:build !windows

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The config gives OpenCode the folder's servers in its shape and ask_user,
// and turns off by name every server an OpenCode config file of the folder,
// or of one above it, defines - JSONC and .opencode/ included.
func TestTheConfigHasTheFolderServersAndTurnsTheProjectsOff(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "repo", "app")
	if err := os.MkdirAll(filepath.Join(work, ".opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "repo", "opencode.jsonc"):      "// the repo's\n{\"mcp\": {\"up\": {\"type\": \"local\", \"command\": [\"u\"]},},}",
		filepath.Join(work, "opencode.json"):               `{"mcp": {"db": {"type": "local", "command": ["old"]}, "socrates": {"type": "local", "command": ["x"]}}}`,
		filepath.Join(work, ".opencode", "opencode.jsonc"): "{/* dot */ \"mcp\": {\"dot\": {\"type\": \"remote\", \"url\": \"https://x//y\"}}}",
	}
	for path, text := range files {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec := agent.StartSpec{Workdir: work, AskCommand: []string{"socrates", "mcp-ask"}, MCPServers: []agent.MCPServer{
		{Name: "db", Transport: agent.MCPStdio, Command: "db-mcp", Args: []string{"--rw"}, Env: map[string]string{"K": "v"}},
		{Name: "docs", Transport: agent.MCPHTTP, URL: "https://d.example/mcp", Headers: map[string]string{"A": "b"}},
	}}
	raw, err := configContent(spec)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCP map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"up":       map[string]any{"enabled": false},
		"dot":      map[string]any{"enabled": false},
		"db":       map[string]any{"type": "local", "command": []any{"db-mcp", "--rw"}, "environment": map[string]any{"K": "v"}},
		"docs":     map[string]any{"type": "remote", "url": "https://d.example/mcp", "headers": map[string]any{"A": "b"}},
		"socrates": map[string]any{"type": "local", "command": []any{"socrates", "mcp-ask"}, "timeout": float64(askTimeout)},
	}
	if !reflect.DeepEqual(doc.MCP, want) {
		t.Fatalf("mcp\n got %v\nwant %v", doc.MCP, want)
	}
}

func TestStripJSONC(t *testing.T) {
	in := "{\n // a\n \"a\": \"x // not a comment\", /* b */ \"c\": [1, 2,],\n \"d\": \"q\\\"//\",\n}"
	var got map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(in)), &got); err != nil {
		t.Fatalf("%v: %s", err, stripJSONC([]byte(in)))
	}
	want := map[string]any{"a": "x // not a comment", "c": []any{1.0, 2.0}, "d": "q\"//"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
