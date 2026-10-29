//go:build !windows

package codex

import (
	"context"
	"encoding/json"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// The chat tools in Codex: dynamic tools of a thread started for a chat
// that has them (StartSpec.ChatTools), beside ask_user; in a test session
// the role's test tools instead of both (chattools.Endpoint.Tools). A
// thread keeps the tools it was started with, so a chat's thread from
// before has none.

// chatToolSpecs is the endpoint's tools as DynamicToolSpecs.
func chatToolSpecs(e chattools.Endpoint) []json.RawMessage {
	var out []json.RawMessage
	for _, d := range e.Tools() {
		raw, _ := json.Marshal(map[string]any{"type": "function", "name": d.Name,
			"description": d.Description, "inputSchema": d.Schema})
		out = append(out, raw)
	}
	return out
}

// withoutTool is specs without the DynamicToolSpec of that name.
func withoutTool(specs []json.RawMessage, name string) []json.RawMessage {
	var out []json.RawMessage
	for _, raw := range specs {
		var spec struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &spec) == nil && spec.Name == name {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// chatToolCall makes the call through the chat's endpoint and gives the
// model what the server said.
func chatToolCall(ctx context.Context, c *Call) (any, error) {
	var p struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(c.Params, &p)
	args := p.Arguments
	var s string
	if json.Unmarshal(args, &s) == nil {
		// Arguments that arrive as a JSON string hold the object.
		args = json.RawMessage(s)
	}
	c.a.mu.Lock()
	endpoint := c.a.spec.ChatTools
	c.a.mu.Unlock()
	res := endpoint.Call(ctx, p.Tool, args)
	return toolResult(res.Text, !res.Error), nil
}
