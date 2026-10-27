package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// FolderMCPFile is the file a folder names its MCP servers in, in the shape
// Claude Code and most other tools read: {"mcpServers": {<name>: {...}}}.
// It is the one source of a chat's MCP servers besides ask_user, whatever
// the harness (HARNESS-POLICY.md, "MCP servers of a folder"): each adapter
// passes them to its CLI in that CLI's own form and keeps the CLI from
// loading MCP servers from the folder by itself.
const FolderMCPFile = ".mcp.json"

// The transports of an MCP server.
const (
	MCPStdio = "stdio"
	MCPHTTP  = "http"
	MCPSSE   = "sse"
)

// MCPServer is one MCP server of a folder: a local command on stdio
// (Command, Args, Env), or a remote endpoint (URL, Headers) that speaks
// streamable HTTP or SSE.
type MCPServer struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// mcpName is a server name every harness takes as it is: Codex refuses
// anything else, and the others make it part of a tool's name.
var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// mcpFile is .mcp.json as it is read. A server entry stays raw so that one
// bad entry does not cost the others.
type mcpFile struct {
	Servers map[string]json.RawMessage `json:"mcpServers"`
}

type mcpEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// The places an MCP server of a chat comes from.
const (
	MCPFromAdmin  = "admin"  // set up in the dashboard for every chat
	MCPFromFolder = "folder" // the chat folder's .mcp.json
)

// SharedMCP is an MCP server set up in the dashboard for every chat, with
// whether a chat has it when nobody switched it on or off there.
type SharedMCP struct {
	MCPServer
	Default bool `json:"default"`
}

// MCPChoice is one MCP server a chat may have: where it comes from, whether
// it is on unless the chat says otherwise, and whether it is on in the chat.
type MCPChoice struct {
	MCPServer
	From    string `json:"from"`
	Default bool   `json:"default"`
	On      bool   `json:"on"`
}

// ChatMCP is every MCP server a chat may have, sorted by name: the shared
// ones and its folder's (ReadFolderMCP), the folder's winning a name as the
// nearer file does among .mcp.json files. Each is on as the chat chose
// (choices, by name), else as its default; a folder's server is on unless
// the chat switched it off. A shared server that cannot be started (a bad
// or reserved name, no command or URL) is left out with a problem.
func ChatMCP(shared []SharedMCP, folder []MCPServer, choices map[string]bool) (list []MCPChoice, problems []string) {
	byName := map[string]MCPChoice{}
	for _, s := range shared {
		if err := checkMCP(s.MCPServer); err != nil {
			problems = append(problems, fmt.Sprintf("The MCP server %q of Admin was left out: %v", s.Name, err))
			continue
		}
		byName[s.Name] = MCPChoice{MCPServer: s.MCPServer, From: MCPFromAdmin, Default: s.Default}
	}
	for _, s := range folder {
		byName[s.Name] = MCPChoice{MCPServer: s, From: MCPFromFolder, Default: true}
	}
	for name, c := range byName {
		c.On = c.Default
		if on, chosen := choices[name]; chosen {
			c.On = on
		}
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, problems
}

// StartMCP is the MCP servers a chat's agent starts with: the ones of
// ChatMCP the chat has on, expanded with ExpandMCP on the chat's
// environment. Problems are what was left out and why, in words for the
// chat; a server that is off is not looked at.
func StartMCP(workdir string, shared []SharedMCP, choices map[string]bool, env []string) (servers []MCPServer, problems []string) {
	folder, problems := ReadFolderMCP(workdir)
	list, more := ChatMCP(shared, folder, choices)
	problems = append(problems, more...)
	for _, c := range list {
		if c.On {
			servers = append(servers, c.MCPServer)
		}
	}
	servers, more = ExpandMCP(servers, env)
	return servers, append(problems, more...)
}

// ReadFolderMCP reads the MCP servers of workdir: its .mcp.json and those of
// the folders above it up to the root of the repository it is in (the
// nearest folder with a .git), the nearer folder's server winning a name.
// A folder in no repository has only its own. Servers come sorted by name,
// as written: ${VAR} is not expanded yet. A server that cannot be started
// (no command, an unknown transport, a bad or reserved name) is left out
// with a problem, as is a file that cannot be read.
func ReadFolderMCP(workdir string) (servers []MCPServer, problems []string) {
	if strings.TrimSpace(workdir) == "" {
		return nil, nil
	}
	byName := map[string]MCPServer{}
	dirs := mcpDirs(workdir)
	for i := len(dirs) - 1; i >= 0; i-- {
		path := filepath.Join(dirs[i], FolderMCPFile)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, fmt.Sprintf("%s could not be read: %v", path, err))
			}
			continue
		}
		var file mcpFile
		if err := json.Unmarshal(data, &file); err != nil {
			problems = append(problems, fmt.Sprintf("%s is not valid: %v", path, err))
			continue
		}
		names := make([]string, 0, len(file.Servers))
		for name := range file.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			s, err := mcpServer(name, file.Servers[name])
			if err != nil {
				problems = append(problems, fmt.Sprintf("The MCP server %q in %s was left out: %v", name, path, err))
				delete(byName, name)
				continue
			}
			byName[name] = s
		}
	}
	for _, s := range byName {
		servers = append(servers, s)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, problems
}

// mcpDirs is workdir and the folders above it up to its repository's root,
// nearest first; workdir alone outside a repository.
func mcpDirs(workdir string) []string {
	dir := filepath.Clean(workdir)
	dirs := []string{dir}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dirs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs[:1]
		}
		dir = parent
		dirs = append(dirs, dir)
	}
}

// mcpServer is one entry of a .mcp.json. A missing type is stdio for an
// entry with a command and streamable HTTP for one with only a URL.
func mcpServer(name string, raw json.RawMessage) (MCPServer, error) {
	var e mcpEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return MCPServer{}, err
	}
	s := MCPServer{Name: name, Command: strings.TrimSpace(e.Command), Args: e.Args, Env: e.Env,
		URL: strings.TrimSpace(e.URL), Headers: e.Headers}
	switch t := strings.ToLower(strings.TrimSpace(e.Type)); t {
	case "":
		if s.Command == "" && s.URL != "" {
			s.Transport = MCPHTTP
		} else {
			s.Transport = MCPStdio
		}
	case MCPStdio, MCPHTTP, MCPSSE:
		s.Transport = t
	default:
		return MCPServer{}, fmt.Errorf("its type %q is not one Socrates can start", e.Type)
	}
	if err := checkMCP(s); err != nil {
		return MCPServer{}, err
	}
	if s.Transport == MCPStdio {
		s.URL, s.Headers = "", nil
	} else {
		s.Command, s.Args, s.Env = "", nil, nil
	}
	return s, nil
}

// checkMCP refuses a server no harness can start: a bad or reserved name,
// a transport it does not know, no command for stdio, no URL for the
// others.
func checkMCP(s MCPServer) error {
	switch {
	case !mcpName.MatchString(s.Name):
		return fmt.Errorf("a name may only have letters, digits, - and _")
	case s.Name == askuser.ServerName:
		return fmt.Errorf("the name %q is Socrates' own", s.Name)
	case s.Transport != MCPStdio && s.Transport != MCPHTTP && s.Transport != MCPSSE:
		return fmt.Errorf("its type %q is not one Socrates can start", s.Transport)
	case s.Transport == MCPStdio && s.Command == "":
		return fmt.Errorf("it has no command")
	case s.Transport != MCPStdio && s.URL == "":
		return fmt.Errorf("it has no url")
	}
	return nil
}

// mcpVar is ${VAR} and ${VAR:-default}, as Claude Code expands them in a
// .mcp.json.
var mcpVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// ExpandMCP puts the values of env into every ${VAR} and ${VAR:-default} of
// a server's command, arguments, environment, URL and headers. A server that
// names a variable env does not have, without a default, is left out with a
// problem: started without it, it would fail in a way nobody sees.
func ExpandMCP(servers []MCPServer, env []string) (out []MCPServer, problems []string) {
	vars := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	for _, s := range servers {
		var missing []string
		expand := func(text string) string {
			return mcpVar.ReplaceAllStringFunc(text, func(m string) string {
				parts := mcpVar.FindStringSubmatch(m)
				if v, ok := vars[parts[1]]; ok {
					return v
				}
				if parts[2] != "" {
					return parts[3]
				}
				missing = append(missing, parts[1])
				return ""
			})
		}
		e := MCPServer{Name: s.Name, Transport: s.Transport, Command: expand(s.Command), URL: expand(s.URL)}
		for _, a := range s.Args {
			e.Args = append(e.Args, expand(a))
		}
		e.Env = expandMap(s.Env, expand)
		e.Headers = expandMap(s.Headers, expand)
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("The MCP server %q was left out: the variable %s is not set",
				s.Name, strings.Join(uniq(missing), ", ")))
			continue
		}
		out = append(out, e)
	}
	return out, problems
}

func expandMap(m map[string]string, expand func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = expand(v)
	}
	return out
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
