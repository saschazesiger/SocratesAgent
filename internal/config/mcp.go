package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// MCPSettings is the MCP servers set up in the dashboard for every chat, on
// top of those a chat's folder names in its .mcp.json (HARNESS-POLICY.md,
// "MCP servers of a folder"). Each is on or off in a chat by its Default
// until the chat is switched otherwise (the chat's options, "mcp").
type MCPSettings struct {
	Servers []MCPServerSetting `json:"servers"`
	// Builtin is whether chats have each MCP server built into Socrates
	// (internal/mcpserver) unless they say otherwise; a server not listed is
	// off.
	Builtin []MCPBuiltinSetting `json:"builtin,omitempty"`
}

// MCPBuiltinSetting is Admin's choice for one MCP server built into
// Socrates.
type MCPBuiltinSetting struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// BuiltinMCPNames is the names of the MCP servers built into Socrates, which
// no server of Admin may take; internal/mcpserver/builtin sets it.
var BuiltinMCPNames []string

// The ways a remote MCP server of Admin is signed in to.
const (
	MCPAuthNone  = ""
	MCPAuthOAuth = "oauth"
)

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
	// Description is a note on what the server is for, shown in Admin and
	// beside its switch in a chat.
	Description string `json:"description,omitempty"`
	// Auth is how a remote server is signed in to: none (its headers carry
	// what it needs), or OAuth, which Socrates does once in Admin and whose
	// token it adds to every request of an agent (MCPAuthOAuth).
	Auth string `json:"auth,omitempty"`
	// Scope is the OAuth scope asked for; empty asks for the server's own.
	Scope string `json:"scope,omitempty"`
	// ToolTimeout is how long one call of a tool may take, in seconds; 0
	// leaves it to the agent.
	ToolTimeout int `json:"tool_timeout,omitempty"`
	// DisabledTools are tools of the server no agent is given.
	DisabledTools []string `json:"disabled_tools,omitempty"`
}

// mcpServerName is a server name every agent takes as it is (agent.mcpName).
var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ReservedMCPName is the name of Socrates' own MCP server (ask_user), which
// no other server may take.
const ReservedMCPName = "socrates"

// Normalize is normalize for a server tested before it is saved.
func (m *MCPSettings) Normalize() { m.normalize() }

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
		s.Description = strings.TrimSpace(s.Description)
		s.Auth = strings.ToLower(strings.TrimSpace(s.Auth))
		s.Scope = strings.TrimSpace(s.Scope)
		if s.ToolTimeout < 0 {
			s.ToolTimeout = 0
		}
		var disabled []string
		for _, t := range s.DisabledTools {
			if t = strings.TrimSpace(t); t != "" && !slices.Contains(disabled, t) {
				disabled = append(disabled, t)
			}
		}
		sort.Strings(disabled)
		s.DisabledTools = disabled
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
			s.URL, s.Headers, s.Auth, s.Scope = "", nil, "", ""
		case MCPHTTP, MCPSSE:
			s.Command, s.Args, s.Env = "", nil, nil
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	m.Servers = out
	var builtin []MCPBuiltinSetting
	seen := map[string]bool{}
	for _, b := range m.Builtin {
		if b.Name = strings.TrimSpace(b.Name); b.Name != "" && !seen[b.Name] {
			seen[b.Name] = true
			builtin = append(builtin, b)
		}
	}
	sort.Slice(builtin, func(i, j int) bool { return builtin[i].Name < builtin[j].Name })
	m.Builtin = builtin
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
		case slices.Contains(BuiltinMCPNames, s.Name):
			return fmt.Errorf("the MCP server name %q is taken by the server built into Socrates", s.Name)
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
			if s.Auth != MCPAuthNone && s.Auth != MCPAuthOAuth {
				return fmt.Errorf("the MCP server %q signs in with %q; it takes none or oauth", s.Name, s.Auth)
			}
		default:
			return fmt.Errorf("the MCP server %q has the type %q; it takes stdio, http or sse", s.Name, s.Type)
		}
	}
	return nil
}
