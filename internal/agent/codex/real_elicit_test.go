//go:build !windows

package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// elicitServer is the test binary as an MCP server (`mcp-elicit`) whose tool
// deploy asks the person where to deploy through an elicitation, and returns
// what came back.
func elicitServer() int {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	write := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Println(string(raw))
	}
	for in.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": "2025-06-18",
				"capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "lab", "version": "1"}}})
		case "tools/list":
			write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"tools": []any{map[string]any{
				"name": "deploy", "description": "Deploys after asking where.", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			call := msg.ID
			write(map[string]any{"jsonrpc": "2.0", "id": "e1", "method": "elicitation/create", "params": map[string]any{
				"message": "Deploy where?", "requestedSchema": map[string]any{"type": "object", "required": []string{"target"},
					"properties": map[string]any{"target": map[string]any{"type": "string", "enum": []string{"staging", "production"}}}}}})
			for in.Scan() {
				var resp struct {
					ID     json.RawMessage `json:"id"`
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if json.Unmarshal(in.Bytes(), &resp) == nil && string(resp.ID) == `"e1"` {
					text := string(resp.Result) + string(resp.Error)
					write(map[string]any{"jsonrpc": "2.0", "id": call, "result": map[string]any{
						"content": []any{map[string]any{"type": "text", "text": "elicited: " + text}}}})
					break
				}
			}
		case "":
		default:
			if len(msg.ID) > 0 {
				write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "no " + msg.Method}})
			}
		}
	}
	return 0
}

// An MCP server's elicitation reaches the person and its answer the server:
// the approval policy lets elicitations through (with "never" codex declines
// them itself, which this test saw before the policy changed).
func TestRealElicitationIsAsked(t *testing.T) {
	lab := newRealLab(t)
	home := lab.home("a", "", "gpt-6-astra")
	cfg, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(cfg, "\n[mcp_servers.lab]\ncommand = %q\nargs = [\"mcp-elicit\"]\ntool_timeout_sec = 600\n", os.Args[0])
	cfg.Close()
	r := newRecorder()
	a := lab.start(lab.spec(home, filepath.Join(lab.root, "state"), ""), r)
	_, n := send(t, a, r, "CALL: mcp__lab__deploy {}")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range r.since(n) {
			if ev.Type == agent.TypeAsk {
				t.Logf("ask %s", ev.Payload)
				_ = a.Answer(t.Context(), ev.AskID, json.RawMessage(`{"answers":{"target":{"selected":["staging"]}}}`))
				r.turnEnd(t, n, 30*time.Second)
				t.Logf("text %q", lastTurnText(r.projection()))
				if !strings.Contains(lastTurnText(r.projection()), "staging") {
					t.Fatalf("the server got %q\n%s", lastTurnText(r.projection()), dump(r.since(n)))
				}
				return
			}
			if ev.Type == agent.TypeTurnEnded {
				t.Fatalf("no elicitation reached the person: %q\n%s", lastTurnText(r.projection()), dump(r.since(n)))
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out\n%s", dump(r.since(n)))
}
