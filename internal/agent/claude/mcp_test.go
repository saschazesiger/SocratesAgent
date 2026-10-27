package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The folder's servers reach Claude Code as a --mcp-config file only the
// chat's user can read, in Claude Code's shape; with none the file goes.
func TestFolderMCPServersAreWrittenForMCPConfig(t *testing.T) {
	state := t.TempDir()
	spec := agent.StartSpec{StateDir: state, MCPServers: []agent.MCPServer{
		{Name: "db", Transport: agent.MCPStdio, Command: "db-mcp", Env: map[string]string{"K": "v"}},
		{Name: "docs", Transport: agent.MCPSSE, URL: "https://d.example/sse", Headers: map[string]string{"A": "b"}},
	}}
	path, err := writeMCPConfig(spec)
	if err != nil || path != filepath.Join(state, mcpConfigFile) {
		t.Fatalf("path %q, %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", info.Mode(), err)
	}
	raw, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"mcpServers": map[string]any{
		"db":   map[string]any{"type": "stdio", "command": "db-mcp", "args": []any{}, "env": map[string]any{"K": "v"}},
		"docs": map[string]any{"type": "sse", "url": "https://d.example/sse", "headers": map[string]any{"A": "b"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config\n got %v\nwant %v", got, want)
	}

	spec.MCPServers = nil
	if path, err := writeMCPConfig(spec); path != "" || err != nil {
		t.Fatalf("with none: %q, %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(state, mcpConfigFile)); !os.IsNotExist(err) {
		t.Fatalf("the earlier file stayed: %v", err)
	}
}
