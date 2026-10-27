package config

import (
	"strings"
	"testing"
)

// The dashboard's MCP servers are tidied on save: a transport is filled in
// as a .mcp.json would assume it, what it does not use goes, an empty row
// goes, and the list is sorted by name.
func TestMCPServersAreNormalized(t *testing.T) {
	s := Default()
	s.MCP.Servers = []MCPServerSetting{
		{Name: " web ", URL: " https://w.example/mcp ", Args: []string{"x"}, Headers: map[string]string{" A ": " b ", " ": "x"}},
		{},
		{Name: "db", Command: "db-mcp", Args: []string{"", "--rw"}, URL: "https://ignored", Default: true},
	}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	got := s.MCP.Servers
	if len(got) != 2 || got[0].Name != "db" || got[0].Type != MCPStdio || got[0].URL != "" ||
		len(got[0].Args) != 1 || !got[0].Default {
		t.Fatalf("db: %+v", got)
	}
	if got[1].Name != "web" || got[1].Type != MCPHTTP || got[1].Args != nil || got[1].Headers["A"] != "b" || len(got[1].Headers) != 1 {
		t.Fatalf("web: %+v", got[1])
	}
}

// A server no agent could start is refused with the reason.
func TestMCPServersThatCannotStartAreRefused(t *testing.T) {
	for _, tc := range []struct {
		server MCPServerSetting
		want   string
	}{
		{MCPServerSetting{Command: "x"}, "no name"},
		{MCPServerSetting{Name: "a b", Command: "x"}, "letters, digits"},
		{MCPServerSetting{Name: "socrates", Command: "x"}, "Socrates' own"},
		{MCPServerSetting{Name: "a", Type: "stdio"}, "no command"},
		{MCPServerSetting{Name: "a", Type: "sse"}, "no URL"},
		{MCPServerSetting{Name: "a", Type: "ws", URL: "x"}, "stdio, http or sse"},
	} {
		s := Default()
		s.MCP.Servers = []MCPServerSetting{tc.server}
		s.Normalize()
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.server, err)
		}
	}
	s := Default()
	s.MCP.Servers = []MCPServerSetting{{Name: "a", Command: "x"}, {Name: "a", URL: "y"}}
	s.Normalize()
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "two MCP servers") {
		t.Errorf("twice: %v", err)
	}
}
