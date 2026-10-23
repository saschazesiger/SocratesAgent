//go:build !windows

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// MCPServer is one entry of OpenCode's `mcp` config: a local command on
// stdio, or a remote streamable-HTTP endpoint. Timeout is per call in
// milliseconds; a progress notification resets it (research/05 §4.3).
type MCPServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     int64             `json:"timeout,omitempty"`
}

// MCPServers is the hook for the MCP servers a chat's OpenCode gets, by name.
// WP6 sets it to add `socrates` (the ask_user tool: `socrates mcp-ask
// --socket <host socket>`, a 24 h timeout, progress notifications). Nil, or a
// nil map, is none.
var MCPServers func(spec agent.StartSpec) map[string]MCPServer

// permission is the answer to everything OpenCode asks about (HARNESS-POLICY:
// always allow), on top of OpenCode's own defaults, which already allow
// "*". Only the defaults that ask are overridden: a "*" here would come last
// and also lift the plan agent's edit deny, which is what plan mode is.
// question is denied because questions go through ask_user (REBUILD §5.3).
func permission() map[string]any {
	return map[string]any{
		"external_directory": "allow",
		"doom_loop":          "allow",
		"read":               "allow",
		"question":           "deny",
	}
}

// configContent is OPENCODE_CONFIG_CONTENT: merged last of every source, so
// it is the one that reliably wins.
func configContent(spec agent.StartSpec) ([]byte, error) {
	doc := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"share":      "disabled",
		"autoupdate": false,
		"permission": permission(),
	}
	if spec.Model != "" {
		doc["model"] = spec.Model
	}
	if MCPServers != nil {
		if servers := MCPServers(spec); len(servers) > 0 {
			doc["mcp"] = servers
		}
	}
	return json.Marshal(doc)
}

// BinaryEnv names the opencode binary in a chat's environment, for a
// harness setting that points elsewhere than PATH. It is not passed on.
const BinaryEnv = "SOCRATES_OPENCODE_BIN"

// serverUser is the Basic-auth user OpenCode defaults to.
const serverUser = "opencode"

// findBinary is the opencode executable: BinaryEnv, else PATH as the chat's
// environment has it, else this process's PATH.
func findBinary(env []string) (string, error) {
	if bin := envValue(env, BinaryEnv); bin != "" {
		return bin, nil
	}
	if path := envValue(env, "PATH"); path != "" {
		for _, dir := range filepath.SplitList(path) {
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, "opencode")
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	return lookPath("opencode")
}

// processEnv is the serve process's whole environment: the chat's (built for
// its account), without what would point OpenCode elsewhere than this chat's
// layout, plus the layout, the server password, the config and the switches
// that keep a server headless and offline-safe.
func processEnv(base []string, layout Layout, account, password string, config []byte) []string {
	keep := func(key string) bool {
		switch {
		case key == BinaryEnv:
			return false
		case key == "OPENCODE_AUTH_CONTENT":
			// An API-key account keeps its key off disk this way (research/06
			// §3.2); never an OAuth account, whose refreshes it would lose.
			return true
		case strings.HasPrefix(key, "OPENCODE_"), strings.HasPrefix(key, "XDG_"):
			return false
		}
		return true
	}
	env := make([]string, 0, len(base)+16)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if keep(key) {
			env = append(env, kv)
		}
	}
	env = append(env, layout.Env(account)...)
	return append(env,
		"OPENCODE_SERVER_USERNAME="+serverUser,
		"OPENCODE_SERVER_PASSWORD="+password,
		"OPENCODE_CONFIG_CONTENT="+string(config),
		// Updates are socrates-update.timer's, never a running chat's.
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		// What lets a server start with no network, as in a car.
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		// Skills from ~/.claude and ~/.agents would turn up as commands.
		"OPENCODE_DISABLE_EXTERNAL_SKILLS=1",
	)
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// splitModel is "provider/model" as OpenCode's prompt body wants it. The
// model part may itself contain slashes (openrouter/nvidia/…).
func splitModel(model string) (provider, id string, ok bool) {
	provider, id, ok = strings.Cut(strings.TrimSpace(model), "/")
	if !ok || provider == "" || id == "" {
		return "", "", false
	}
	return provider, id, true
}
