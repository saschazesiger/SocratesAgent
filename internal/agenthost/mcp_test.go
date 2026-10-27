//go:build linux

package agenthost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The host hands the adapter its folder's MCP servers, expanded on the
// chat's environment, and says once in the chat what it had to leave out.
func TestTheHostGivesTheAgentItsFoldersMCPServers(t *testing.T) {
	recordings.reset()
	h := newInProcessHost(t, "acct-a")
	h.spec.Env = append(h.spec.Env, "DB_TOKEN=t0k")
	mcp := `{"mcpServers":{"db":{"command":"db-mcp","env":{"TOKEN":"${DB_TOKEN}"}},"web":{"url":"https://${NOPE}/mcp"}}}`
	if err := os.WriteFile(filepath.Join(h.spec.Workdir, agent.FolderMCPFile), []byte(mcp), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.listen(); err != nil {
		t.Fatal(err)
	}
	h.start()
	deadline := time.Now().Add(10 * time.Second)
	for len(recordings.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	started := recordings.all()
	if len(started) != 1 {
		t.Fatalf("%d adapters started", len(started))
	}
	got := started[0].spec.MCPServers
	if len(got) != 1 || got[0].Name != "db" || got[0].Command != "db-mcp" || got[0].Env["TOKEN"] != "t0k" {
		t.Fatalf("the adapter got %+v", got)
	}
	// A second start (an account switch) with the same folder says nothing new.
	h.chatMCP(h.spec.Env)
	notices := 0
	for _, ev := range h.rec.Feed().After(0) {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeMCP {
			notices++
			if !strings.Contains(ev.Text, `"web"`) || !strings.Contains(ev.Text, "NOPE") {
				t.Errorf("the notice says %q", ev.Text)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("%d notices about the folder's MCP servers", notices)
	}
}

// The dashboard's MCP servers reach the agent sealed for the chat, on or off
// as the chat chose or else by their default, and a folder's server the chat
// switched off stays off.
func TestTheHostGivesTheAgentTheDashboardsMCPServers(t *testing.T) {
	recordings.reset()
	h := newInProcessHost(t, "acct-a")
	h.spec.Env = append(h.spec.Env, "GH_TOKEN=gh1")
	mcp := `{"mcpServers":{"db":{"command":"db-mcp"},"notes":{"command":"notes-mcp"}}}`
	if err := os.WriteFile(filepath.Join(h.spec.Workdir, agent.FolderMCPFile), []byte(mcp), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadOrCreateKeyFile(paths.New(h.opts.DataDir).SecretsKey())
	if err != nil {
		t.Fatal(err)
	}
	shared := []agent.SharedMCP{
		{MCPServer: agent.MCPServer{Name: "github", Transport: agent.MCPHTTP, URL: "https://api.example/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${GH_TOKEN}"}}, Default: true},
		{MCPServer: agent.MCPServer{Name: "search", Transport: agent.MCPStdio, Command: "search-mcp"}},
		{MCPServer: agent.MCPServer{Name: "browser", Transport: agent.MCPStdio, Command: "browser-mcp"}, Default: true},
	}
	if h.spec.SharedMCP, err = SealMCP(key, h.opts.ChatID, shared); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.spec.SharedMCP, "Bearer") {
		t.Fatal("the sealed servers show their header")
	}
	h.spec.MCPChoices = map[string]bool{"search": true, "browser": false, "notes": false}
	if err := h.listen(); err != nil {
		t.Fatal(err)
	}
	h.start()
	deadline := time.Now().Add(10 * time.Second)
	for len(recordings.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	started := recordings.all()
	if len(started) != 1 {
		t.Fatalf("%d adapters started", len(started))
	}
	var names []string
	for _, s := range started[0].spec.MCPServers {
		names = append(names, s.Name)
		if s.Name == "github" && s.Headers["Authorization"] != "Bearer gh1" {
			t.Errorf("github's header is %q", s.Headers["Authorization"])
		}
	}
	if strings.Join(names, ",") != "db,github,search" {
		t.Fatalf("the adapter got %v", names)
	}
	// Sealed for another chat, they do not open here: the chat says so and
	// starts with its folder's servers.
	if h.spec.SharedMCP, err = SealMCP(key, "another-chat", shared); err != nil {
		t.Fatal(err)
	}
	got := h.chatMCP(h.spec.Env)
	if len(got) != 1 || got[0].Name != "db" {
		t.Fatalf("with another chat's servers the agent got %+v", got)
	}
	said := false
	for _, ev := range h.rec.Feed().After(0) {
		said = said || (ev.Type == agent.TypeNotice && strings.Contains(ev.Text, "MCP servers of Admin"))
	}
	if !said {
		t.Fatal("the chat was not told the dashboard's servers were left out")
	}
}
