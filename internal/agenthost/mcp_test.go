//go:build linux

package agenthost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
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
	h.folderMCP(h.spec.Env)
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
