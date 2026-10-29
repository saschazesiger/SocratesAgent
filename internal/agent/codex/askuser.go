//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// ask_user in Codex (REBUILD.md §5.3): a dynamic tool on every thread, and
// codex's own ways of asking the person - Plan mode's request_user_input,
// the async questions of the code-mode models and MCP elicitations - asked
// as the same payload and answered in each one's own shape.

func init() {
	RegisterTool(Tool{Name: askuser.Name, Spec: askToolSpec(), Handle: askUserTool})
	HandleRequest("item/tool/requestUserInput", requestUserInput)
	HandleRequest("mcpServer/elicitation/request", elicitationRequest)
}

// DeferredText is what ask_user returns at once to a code-mode model, whose
// tool calls run inside a script it would otherwise poll every half minute
// for as long as the person takes (research/04 §3.1). The answer starts the
// next turn.
const DeferredText = "Questions delivered. End your turn now; the answers arrive as the next message."

// askGuidance begins the developer instructions of every thread the adapter
// starts: codex's equivalent of the system prompt (REBUILD.md §5.3). A thread
// started before has only the tool's description, which says the same in
// short.
var askGuidance = askuser.Guidance(askuser.Name)

// askToolSpec is ask_user as a DynamicToolSpec.
func askToolSpec() json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"type": "function", "name": askuser.Name,
		"description": askuser.Description, "inputSchema": askuser.Schema()})
	return raw
}

// codeMode reports whether a model runs its tools in code mode: the gpt-6
// models get exec and wait, and every other tool inside exec's script
// (research/04 §3.0).
func codeMode(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-6")
}

// toolResult is a DynamicToolCallResponse with one text.
func toolResult(text string, success bool) map[string]any {
	return map[string]any{"contentItems": []any{map[string]any{"type": "inputText", "text": text}}, "success": success}
}

// askUserTool is the dynamic tool: a call that passes askuser.Parse is asked
// and waited for, or on a code-mode model delivered and answered as the next
// message; one that does not goes back to the model to fix. A test session's
// thread is not offered it, and has nobody to ask should it call it anyway.
func askUserTool(ctx context.Context, c *Call) (any, error) {
	c.a.mu.Lock()
	test := c.a.spec.ChatTools.Test()
	c.a.mu.Unlock()
	if test {
		return toolResult(askuser.TestRefusal, false), nil
	}
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(c.Params, &p)
	args := p.Arguments
	var s string
	if json.Unmarshal(args, &s) == nil {
		// Arguments that arrive as a JSON string hold the object.
		args = json.RawMessage(s)
	}
	payload, err := askuser.Parse(args)
	if err != nil {
		return toolResult(err.Error(), false), nil
	}
	payload.PartID = c.PartID
	if codeMode(c.Model) {
		c.Defer(payload.JSON())
		return toolResult(DeferredText, true), nil
	}
	answer, err := c.Ask(ctx, payload.JSON())
	if err != nil {
		// Codex no longer waits for this call; the answer goes as a message.
		return toolResult("The question was withdrawn before it was answered.", false), nil
	}
	return toolResult(askuser.ToolResult(&payload, answer), true), nil
}

// requestUserInput is Plan mode's request_user_input: asked, and answered as
// {answers: {<id>: {answers: [...]}}}.
func requestUserInput(ctx context.Context, c *Call) (any, error) {
	payload, ok := askuser.FromUserInput(c.Params)
	if !ok {
		return map[string]any{"answers": map[string]any{}}, nil
	}
	payload.PartID = c.PartID
	answer, err := c.Ask(ctx, payload.JSON())
	if err != nil {
		return nil, err
	}
	return askuser.UserInputResult(&payload, answer), nil
}

// elicitationRequest accepts an MCP tool-call approval (a permission, and
// every permission is allowed) and asks the person a form; a URL or a device
// verification is declined, with a notice, because it needs a browser on
// the machine.
func elicitationRequest(ctx context.Context, c *Call) (any, error) {
	var p struct {
		Meta struct {
			Kind string `json:"codex_approval_kind"`
		} `json:"_meta"`
		ServerName      string          `json:"serverName"`
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	_ = json.Unmarshal(c.Params, &p)
	if p.Meta.Kind != "" {
		return map[string]any{"action": "accept", "content": map[string]any{}, "_meta": nil}, nil
	}
	payload, ok := askuser.FromElicitation(p.ServerName, p.Message, p.RequestedSchema, nil)
	if !ok || (p.Mode != "form" && p.Mode != "openai/form" && p.Mode != "openaiForm") {
		c.Notice("elicitation_declined", "The MCP server "+p.ServerName+" asked for something Socrates cannot show, so it was declined: "+p.Message)
		return map[string]any{"action": "decline", "content": nil, "_meta": nil}, nil
	}
	answer, err := c.Ask(ctx, payload.JSON())
	if err != nil {
		return nil, err
	}
	action, content := askuser.ElicitationResult(&payload, p.RequestedSchema, answer)
	return map[string]any{"action": action, "content": content, "_meta": nil}, nil
}
