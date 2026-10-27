package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A folder's servers are its own .mcp.json and those above it up to its
// repository's root, the nearer one winning a name; nothing above the
// repository counts, and a folder outside any repository has only its own.
func TestReadFolderMCPGoesUpToTheRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "app", "web")
	writeFile(t, filepath.Join(root, FolderMCPFile), `{"mcpServers":{"outside":{"command":"x"}}}`)
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(repo, FolderMCPFile), `{"mcpServers":{
		"docs":{"type":"http","url":"https://docs.example/mcp","headers":{"Authorization":"Bearer ${TOKEN}"}},
		"db":{"command":"db-mcp","args":["--ro"]}}}`)
	writeFile(t, filepath.Join(sub, FolderMCPFile), `{"mcpServers":{"db":{"command":"db-mcp","args":["--rw"],"env":{"A":"1"}}}}`)

	servers, problems := ReadFolderMCP(sub)
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	want := []MCPServer{
		{Name: "db", Transport: MCPStdio, Command: "db-mcp", Args: []string{"--rw"}, Env: map[string]string{"A": "1"}},
		{Name: "docs", Transport: MCPHTTP, URL: "https://docs.example/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}},
	}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("servers\n got %+v\nwant %+v", servers, want)
	}

	alone := filepath.Join(root, "alone", "deeper")
	writeFile(t, filepath.Join(alone, FolderMCPFile), `{"mcpServers":{"mine":{"command":"m"}}}`)
	servers, _ = ReadFolderMCP(alone)
	if len(servers) != 1 || servers[0].Name != "mine" {
		t.Fatalf("outside a repository: %+v", servers)
	}
	if servers, problems := ReadFolderMCP(filepath.Join(root, "nothing")); servers != nil || problems != nil {
		t.Fatalf("a folder without a file: %+v %v", servers, problems)
	}
}

// A server that cannot be started is left out with a reason, and the others
// stay; a file that is not JSON costs only its own servers.
func TestReadFolderMCPLeavesOutWhatCannotStart(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FolderMCPFile), `{"mcpServers":{
		"good":{"command":"ok"},
		"socrates":{"command":"mine"},
		"no command":{"command":"x"},
		"empty":{"type":"stdio"},
		"ws":{"type":"ws","url":"wss://x"},
		"sse":{"type":"sse"},
		"remote":{"url":"https://r.example/mcp"},
		"odd":["not an object"]}}`)
	servers, problems := ReadFolderMCP(dir)
	if len(servers) != 2 || servers[0].Name != "good" || servers[1].Name != "remote" || servers[1].Transport != MCPHTTP {
		t.Fatalf("servers %+v", servers)
	}
	text := strings.Join(problems, "\n")
	for _, want := range []string{`"socrates"`, `"no command"`, `"empty"`, `"ws"`, `"sse"`, `"odd"`, "Socrates' own",
		"no command", `type "ws"`, "no url"} {
		if !strings.Contains(text, want) {
			t.Errorf("the problems do not say %s:\n%s", want, text)
		}
	}

	writeFile(t, filepath.Join(dir, FolderMCPFile), `{"mcpServers": {`)
	if servers, problems := ReadFolderMCP(dir); servers != nil || len(problems) != 1 || !strings.Contains(problems[0], "is not valid") {
		t.Fatalf("a broken file: %+v %v", servers, problems)
	}
}

// ${VAR} and ${VAR:-default} take the chat's environment; a server that
// needs a variable the chat does not have is left out and says which.
func TestExpandMCPTakesTheChatsEnvironment(t *testing.T) {
	in := []MCPServer{
		{Name: "a", Transport: MCPStdio, Command: "${BIN:-run}", Args: []string{"--key=${KEY}", "$KEY"}, Env: map[string]string{"K": "${KEY}"}},
		{Name: "b", Transport: MCPHTTP, URL: "https://${HOST}/mcp", Headers: map[string]string{"X": "${MISSING} ${GONE}"}},
	}
	out, problems := ExpandMCP(in, []string{"KEY=s3cret", "HOST=h.example"})
	want := []MCPServer{{Name: "a", Transport: MCPStdio, Command: "run", Args: []string{"--key=s3cret", "$KEY"}, Env: map[string]string{"K": "s3cret"}}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("expanded\n got %+v\nwant %+v", out, want)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], `"b"`) || !strings.Contains(problems[0], "GONE, MISSING") {
		t.Fatalf("problems %v", problems)
	}
	if in[0].Command != "${BIN:-run}" {
		t.Fatal("the servers read were changed")
	}
}
