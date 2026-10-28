//go:build !windows

package codex

import (
	"context"
	"encoding/json"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// The chat tools in Codex: dynamic tools of a thread started for a chat
// that has them (StartSpec.ChatTools), beside ask_user. A thread keeps the
// tools it was started with, so a chat's thread from before has none.

// chatsGuidance is what the developer instructions say about them.
var chatsGuidance = chattools.Guidance(chattools.List)

// chatToolSpecs is the chat tools as DynamicToolSpecs.
func chatToolSpecs() []json.RawMessage {
	var out []json.RawMessage
	for _, d := range chattools.Tools() {
		raw, _ := json.Marshal(map[string]any{"type": "function", "name": d.Name,
			"description": d.Description, "inputSchema": d.Schema})
		out = append(out, raw)
	}
	return out
}

// isChatTool is whether a dynamic tool call is one of the chat tools.
func isChatTool(name string) bool {
	for _, d := range chattools.Tools() {
		if d.Name == name {
			return true
		}
	}
	return false
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
