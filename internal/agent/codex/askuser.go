//go:build !windows

package codex

import (
	"context"
	"encoding/json"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// ask_user in Codex (REBUILD.md §5.3): a dynamic tool on every thread that
// puts its question and returns at once, check_questions and
// withdraw_question beside it, and codex's own ways of asking the person -
// Plan mode's request_user_input and MCP elicitations - asked as the same
// payload, waited for and answered in each one's own shape.

func init() {
	RegisterTool(Tool{Name: askuser.Name, Spec: askToolSpec(), Handle: askUserTool})
	RegisterTool(Tool{Name: askuser.CheckName, Spec: toolSpec(askuser.CheckName, askuser.CheckDescription, askuser.CheckSchema), Handle: questionTool})
	RegisterTool(Tool{Name: askuser.WithdrawName, Spec: toolSpec(askuser.WithdrawName, askuser.WithdrawDescription, askuser.WithdrawSchema), Handle: questionTool})
	HandleRequest("item/tool/requestUserInput", requestUserInput)
	HandleRequest("mcpServer/elicitation/request", elicitationRequest)
}

// AskedText is what ask_user returns to a model that asks again in a turn
// what it has already put: a small model that does not end its turn
// otherwise asks, sleeps and asks again, a question each time.
func AskedText(id string) string {
	return "This question is already with the person as " + id + " and still open. Do not ask it again: go on with " +
		"the work that does not depend on it, or end your turn; the answer arrives by itself as a message."
}

// askGuidance begins the developer instructions of every thread the adapter
// starts: codex's equivalent of the system prompt (REBUILD.md §5.3). A thread
// started before has only the tool's description, which says the same in
// short.
var askGuidance = askuser.Guidance(askuser.Name, "")

// todoGuidance is how a thread keeps its task list: Codex's plan
// (agent.TodoGuidance).
var todoGuidance = agent.TodoGuidance("update_plan")

// askToolSpec is ask_user as a DynamicToolSpec.
func askToolSpec() json.RawMessage {
	return toolSpec(askuser.Name, askuser.Description, askuser.Schema())
}

// toolSpec is a DynamicToolSpec.
func toolSpec(name, description string, schema json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"type": "function", "name": name, "description": description, "inputSchema": schema})
	return raw
}

// toolResult is a DynamicToolCallResponse with one text.
func toolResult(text string, success bool) map[string]any {
	return map[string]any{"contentItems": []any{map[string]any{"type": "inputText", "text": text}}, "success": success}
}

// arguments is a dynamic tool call's arguments; arguments that arrive as a
// JSON string hold the object.
func arguments(c *Call) json.RawMessage {
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(c.Params, &p)
	args := p.Arguments
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = json.RawMessage(s)
	}
	return args
}

// askUserTool is the dynamic tool: a call that passes askuser.Parse is put
// into the person's queue and returns at once, its answer to arrive as a
// message; one that does not goes back to the model to fix, and a subagent
// thread's is refused.
func askUserTool(ctx context.Context, c *Call) (any, error) {
	payload, err := askuser.Parse(arguments(c))
	if err != nil {
		return toolResult(err.Error(), false), nil
	}
	c.a.mu.Lock()
	sub := c.ThreadID != "" && c.a.thread != "" && c.ThreadID != c.a.thread
	c.a.mu.Unlock()
	if sub {
		return toolResult(askuser.SubagentText, false), nil
	}
	asked := c.Turn + "\x00" + string(payload.JSON())
	c.a.mu.Lock()
	id := c.a.deferred[asked]
	c.a.mu.Unlock()
	if id != "" {
		return toolResult(AskedText(id), true), nil
	}
	payload.PartID = c.PartID
	id = c.Defer(payload.JSON())
	c.a.mu.Lock()
	if c.a.deferred == nil {
		c.a.deferred = map[string]string{}
	}
	c.a.deferred[asked] = id
	q := c.a.spec.Questions
	c.a.mu.Unlock()
	return toolResult(askuser.Queued(ctx, q, id, &payload), true), nil
}

// questionTool is check_questions and withdraw_question.
func questionTool(ctx context.Context, c *Call) (any, error) {
	var p struct {
		Tool string `json:"tool"`
	}
	_ = json.Unmarshal(c.Params, &p)
	c.a.mu.Lock()
	q := c.a.spec.Questions
	c.a.mu.Unlock()
	if q == nil {
		return toolResult("The questions cannot be reached from here.", false), nil
	}
	text, failed := askuser.RunQuestionTool(ctx, q, p.Tool, arguments(c))
	return toolResult(text, !failed), nil
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
