package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MCPSettings is the MCP servers set up in the dashboard for every chat, on
// top of those a chat's folder names in its .mcp.json (HARNESS-POLICY.md,
// "MCP servers of a folder"). Each is on or off in a chat by its Default
// until the chat is switched otherwise (the chat's options, "mcp").
type MCPSettings struct {
	Servers []MCPServerSetting `json:"servers"`
}

// The transports of an MCP server, as a .mcp.json names them.
const (
	MCPStdio = "stdio"
	MCPHTTP  = "http"
	MCPSSE   = "sse"
)

// MCPServerSetting is one MCP server of the dashboard: a local command on
// stdio (Command, Args, Env), or a remote endpoint (URL, Headers) that speaks
// streamable HTTP or SSE. ${VAR} and ${VAR:-default} in it are expanded on a
// chat's environment when its agent starts, as in a .mcp.json.
type MCPServerSetting struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Default is whether a chat has the server when nobody switched it on or
	// off in that chat.
	Default bool `json:"default"`
}

// mcpServerName is a server name every agent takes as it is (agent.mcpName).
var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ReservedMCPName is the name of Socrates' own MCP server (ask_user), which
// no other server may take.
const ReservedMCPName = "socrates"

// normalize trims what was typed, fills in the transport a .mcp.json would
// assume (stdio with a command, http with only a URL), drops what the
// transport does not use, and sorts the servers by name. A row with no name
// and nothing else is a form left empty and goes; one with only a name stays
// for validate to refuse.
func (m *MCPSettings) normalize() {
	var out []MCPServerSetting
	for _, s := range m.Servers {
		s.Name = strings.TrimSpace(s.Name)
		s.Type = strings.ToLower(strings.TrimSpace(s.Type))
		s.Command = strings.TrimSpace(s.Command)
		s.URL = strings.TrimSpace(s.URL)
		s.Env = trimMap(s.Env)
		s.Headers = trimMap(s.Headers)
		var args []string
		for _, a := range s.Args {
			if a != "" {
				args = append(args, a)
			}
		}
		s.Args = args
		if s.Name == "" && s.Command == "" && s.URL == "" {
			continue
		}
		if s.Type == "" {
			if s.Command == "" && s.URL != "" {
				s.Type = MCPHTTP
			} else {
				s.Type = MCPStdio
			}
		}
		switch s.Type {
		case MCPStdio:
			s.URL, s.Headers = "", nil
		case MCPHTTP, MCPSSE:
			s.Command, s.Args, s.Env = "", nil, nil
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	m.Servers = out
}

func trimMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validate refuses a server no agent could start, in words for the person
// who typed it.
func (m MCPSettings) validate() error {
	seen := map[string]bool{}
	for _, s := range m.Servers {
		switch {
		case s.Name == "":
			return fmt.Errorf("an MCP server has no name")
		case !mcpServerName.MatchString(s.Name):
			return fmt.Errorf("the MCP server name %q may only have letters, digits, - and _", s.Name)
		case s.Name == ReservedMCPName:
			return fmt.Errorf("the MCP server name %q is Socrates' own", s.Name)
		case seen[s.Name]:
			return fmt.Errorf("there are two MCP servers named %q", s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case MCPStdio:
			if s.Command == "" {
				return fmt.Errorf("the MCP server %q has no command", s.Name)
			}
		case MCPHTTP, MCPSSE:
			if s.URL == "" {
				return fmt.Errorf("the MCP server %q has no URL", s.Name)
			}
		default:
			return fmt.Errorf("the MCP server %q has the type %q; it takes stdio, http or sse", s.Name, s.Type)
		}
	}
	return nil
}
