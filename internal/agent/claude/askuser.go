//go:build !windows

package claude

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// AskTool is the name the model knows ask_user by in Claude Code: an MCP
// tool of the in-process server askuser.ServerName.
const AskTool = "mcp__" + askuser.ServerName + "__" + askuser.Name

// askGuidance is the system-prompt text every chat gets (REBUILD.md §5.3).
// Claude Code defers MCP tools behind ToolSearch, which the model finds on
// its own; saying so saves it a turn of looking.
var askGuidance = askuser.Guidance(AskTool) +
	" If the tool is not loaded yet, load it with ToolSearch (select:" + AskTool + ") first."

// askServerConfig is the server's sdkMcpServerConfigs entry. An in-process
// server has no idle timeout, only the wall-clock one, which is set as high
// as the CLI takes it (2^31-1 ms, almost 25 days), so that a question waits
// for as long as the person takes (verified 20 minutes, research/03 §4.4).
// disableAutoBackground keeps the CLI from moving a waiting call into the
// background: "meant for a server whose calls wait on a person".
var askServerConfig = json.RawMessage(`{"timeout":2147483647,"disableAutoBackground":true}`)

// askServer is ask_user as the chat's in-process MCP server: the CLI reaches
// it over the control protocol (mcp_message), a call becomes an ask event
// and blocks until Answer brings the person's answer, which the model gets
// in the shape of REBUILD.md §5.2.
type askServer struct {
	a   *Adapter
	mcp *askuser.MCP

	mu   sync.Mutex
	asks map[string]*waitingAsk
}

// waitingAsk is one call waiting for its answer.
type waitingAsk struct {
	ctx    context.Context
	answer chan json.RawMessage
}

func newAskServer(a *Adapter) *askServer {
	s := &askServer{a: a, asks: map[string]*waitingAsk{}}
	s.mcp = &askuser.MCP{Ask: s.ask}
	return s
}

func (s *askServer) Name() string            { return askuser.ServerName }
func (s *askServer) Config() json.RawMessage { return askServerConfig }

func (s *askServer) Handle(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
	return s.mcp.Handle(ctx, message), nil
}

// ask puts one call to the person. Its id is the tool call's, so that a view
// can draw the ask with the tool part it belongs to.
func (s *askServer) ask(ctx context.Context, call askuser.Call) (json.RawMessage, error) {
	id := "ask_" + randomID()
	if call.ToolUseID != "" {
		id = "ask_" + call.ToolUseID
		call.Payload.PartID = call.ToolUseID
	}
	return s.wait(ctx, id, call.Payload.JSON())
}

// wait emits an ask and blocks until its answer or the end of ctx: the CLI
// withdrew the call (an interrupt, a timeout) or exited.
func (s *askServer) wait(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	w := &waitingAsk{ctx: ctx, answer: make(chan json.RawMessage, 1)}
	s.mu.Lock()
	s.asks[id] = w
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.asks[id] == w {
			delete(s.asks, id)
		}
		s.mu.Unlock()
	}()
	s.a.mu.Lock()
	turnID, out := "", s.a.out
	if s.a.st != nil && s.a.st.turn != nil {
		turnID = s.a.st.turn.id
	}
	s.a.mu.Unlock()
	out.Emit(agent.Event{Type: agent.TypeAsk, AskID: id, TurnID: turnID, Payload: payload})
	select {
	case answer := <-w.answer:
		return answer, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Answer hands an answer to the call waiting on it, and reports
// agent.ErrAskGone when none is.
func (s *askServer) Answer(_ context.Context, askID string, answer json.RawMessage) error {
	s.mu.Lock()
	w := s.asks[askID]
	if w != nil {
		delete(s.asks, askID)
	}
	s.mu.Unlock()
	if w == nil || w.ctx.Err() != nil {
		return agent.ErrAskGone
	}
	w.answer <- answer
	return nil
}

// elicit is an MCP elicitation the CLI forwards (a form a server wants the
// person to fill in), asked as an ask_user payload and answered in the
// elicitation's own shape.
func (s *askServer) elicit(ctx context.Context, id string, req controlRequest) map[string]any {
	var message string
	_ = json.Unmarshal(req.Message, &message)
	p, ok := askuser.FromElicitation(req.MCPServerName, message, req.RequestedSchema, req.PropertyOrder)
	if !ok || req.Mode != "" && req.Mode != "form" {
		return map[string]any{"action": "decline"}
	}
	answer, err := s.wait(ctx, "ask_elicit_"+id, p.JSON())
	if err != nil {
		return nil
	}
	action, content := askuser.ElicitationResult(&p, req.RequestedSchema, answer)
	if content == nil {
		return map[string]any{"action": action}
	}
	return map[string]any{"action": action, "content": content}
}
