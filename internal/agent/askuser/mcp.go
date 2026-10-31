package askuser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// ProgressEvery is how often a waiting call reports progress to an MCP
// client that asked for it. A progress notification resets the client's
// timeout (OpenCode's, research/05 §4.3; Claude Code's idle clock,
// research/03 §4.4), so a question can wait for as long as the person takes.
const ProgressEvery = 30 * time.Second

// progressEvery is ProgressEvery, shorter in tests.
var progressEvery = ProgressEvery

// protocolVersions are the MCP versions the server speaks, newest first.
// OpenCode 1.19.4 and Claude Code 2.1.309 ask for 2025-11-25.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

// Call is one ask_user call that passed Parse.
type Call struct {
	Payload Payload
	// ToolUseID is the agent's id of the tool call when its MCP client says
	// it (Claude Code's _meta "claudecode/toolUseId").
	ToolUseID string
}

// MCP is ask_user as an MCP server: it answers the JSON-RPC messages of the
// Model Context Protocol one at a time, whatever carries them (the control
// protocol of Claude Code, the stdio of `socrates mcp-ask`). Handle may be
// called from many goroutines at once: a tools/call blocks until the person
// answers. The carrier ends a call's context when the client withdraws it
// (notifications/cancelled, which Cancelled reads), because only the carrier
// reads the client's messages in order: a cancel that follows its call at
// once is then never missed, as it could be by a call still on its way into
// Handle.
type MCP struct {
	// Version is the server's own version in initialize's serverInfo, "1"
	// when empty.
	Version string
	// Instructions are the server's instructions in initialize, which an
	// agent puts into its system prompt (OpenCode does); empty for an agent
	// that gets the guidance another way.
	Instructions string
	// Ask puts a call to the person and returns their answer. An error is
	// what the model is told instead (the call is gone, the chat is not
	// reachable); a context cancelled by the client is not reported at all.
	Ask func(ctx context.Context, call Call) (json.RawMessage, error)
	// Progress, when set, is told every ProgressEvery while a call waits, for
	// a client that sent a progress token, and never once Handle has
	// returned the call's response.
	Progress func(token json.RawMessage, progress int)
	// Chats is where the chat tools (list_chats, read_chat, send_to_chat)
	// reach the server; the server offers them only when it is usable.
	Chats chattools.Endpoint
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Handle answers one message: the response, or nil for a notification and
// for a call the client cancelled (whose context the carrier ended).
func (m *MCP) Handle(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return rpcError(nil, -32700, "the message is not JSON")
	}
	notification := len(msg.ID) == 0 || string(msg.ID) == "null"
	switch msg.Method {
	case "initialize":
		return rpcResult(msg.ID, m.initialize(msg.Params))
	case "ping":
		return rpcResult(msg.ID, map[string]any{})
	case "tools/list":
		tools := []any{Tool()}
		if m.Chats.Usable() {
			for _, d := range m.Chats.Tools() {
				tools = append(tools, map[string]any{"name": d.Name, "title": d.Title, "description": d.Description, "inputSchema": d.Schema})
			}
		}
		return rpcResult(msg.ID, map[string]any{"tools": tools})
	case "tools/call":
		return m.call(ctx, msg)
	}
	if notification {
		return nil
	}
	return rpcError(msg.ID, -32601, "socrates does not offer "+msg.Method)
}

// Tool is ask_user as tools/list describes it.
func Tool() map[string]any {
	return map[string]any{"name": Name, "title": "Ask the person", "description": Description, "inputSchema": Schema()}
}

func (m *MCP) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := protocolVersions[0]
	for _, v := range protocolVersions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	own := m.Version
	if own == "" {
		own = "1"
	}
	out := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": ServerName, "version": own},
	}
	if m.Instructions != "" {
		out["instructions"] = m.Instructions
	}
	return out
}

func (m *MCP) call(ctx context.Context, msg rpcMessage) json.RawMessage {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
			ToolUseID     string          `json:"claudecode/toolUseId"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return rpcError(msg.ID, -32602, "unreadable tools/call parameters")
	}
	if p.Name != Name {
		if m.Chats.Usable() && m.Chats.Has(p.Name) {
			res := m.Chats.Call(ctx, p.Name, p.Arguments)
			return rpcResult(msg.ID, toolResult(res.Text, res.Error))
		}
		return rpcError(msg.ID, -32602, "socrates has no tool "+p.Name)
	}
	payload, err := Parse(p.Arguments)
	if err != nil {
		return rpcResult(msg.ID, toolResult(err.Error(), true))
	}
	if m.Ask == nil {
		return rpcResult(msg.ID, toolResult("The person cannot be asked from here.", true))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if m.Progress != nil && len(p.Meta.ProgressToken) > 0 && string(p.Meta.ProgressToken) != "null" {
		ticking := make(chan struct{})
		go func() {
			defer close(ticking)
			m.progress(ctx, p.Meta.ProgressToken)
		}()
		// No progress after the response: the ticks stop, and one being
		// written is done, before the response is returned.
		defer func() {
			cancel()
			<-ticking
		}()
	}
	answer, err := m.Ask(ctx, Call{Payload: payload, ToolUseID: p.Meta.ToolUseID})
	switch {
	case err == nil:
		return rpcResult(msg.ID, toolResult(ToolResult(&payload, answer), false))
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		// The client withdrew the call; it expects no answer.
		return nil
	}
	return rpcResult(msg.ID, toolResult("The question could not be put to the person: "+err.Error(), true))
}

func (m *MCP) progress(ctx context.Context, token json.RawMessage) {
	tick := time.NewTicker(progressEvery)
	defer tick.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if ctx.Err() != nil {
				return
			}
			m.Progress(token, n)
		}
	}
}

// RequestID is the id of a JSON-RPC request, a message with a method that
// expects a response, in a form two messages can be compared by; false for
// a notification or a response.
func RequestID(raw json.RawMessage) (string, bool) {
	var msg rpcMessage
	if json.Unmarshal(raw, &msg) != nil || msg.Method == "" {
		return "", false
	}
	return idKey(msg.ID)
}

// Cancelled is the id of the request a notifications/cancelled withdraws, in
// RequestID's form; false for any other message.
func Cancelled(raw json.RawMessage) (string, bool) {
	var msg rpcMessage
	if json.Unmarshal(raw, &msg) != nil || msg.Method != "notifications/cancelled" {
		return "", false
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return "", false
	}
	return idKey(p.RequestID)
}

// idKey is a JSON-RPC id as a map key: compact JSON, so that 7 and "7" stay
// apart and spacing does not matter.
func idKey(id json.RawMessage) (string, bool) {
	var b bytes.Buffer
	if len(id) == 0 || string(id) == "null" || json.Compact(&b, id) != nil {
		return "", false
	}
	return b.String(), true
}

// ProgressNotification is the notifications/progress message for a token.
func ProgressNotification(token json.RawMessage, progress int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress",
		"params": map[string]any{"progressToken": token, "progress": progress,
			"message": "Waiting for the person to answer."}})
	return raw
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}

func rpcResult(id json.RawMessage, result any) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return raw
}

func rpcError(id json.RawMessage, code int, message string) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	return raw
}
