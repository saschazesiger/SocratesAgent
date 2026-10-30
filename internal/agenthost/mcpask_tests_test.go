package agenthost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// `socrates mcp-ask` (OpenCode's socrates server) offers a chat whose
// repository has a test catalog the test tools after the chat tools, and
// tells its agent when to use them; the endpoint reaches it through the
// environment OpenCode starts it with.
func TestMCPAskOffersAChatTheTestTools(t *testing.T) {
	chat := chattools.Endpoint{Socket: "/nonexistent/chat-tools.sock", Token: "c.x", Tests: true}
	for k, v := range chat.Env() {
		t.Setenv(k, v)
	}
	if got := chattools.FromEnv(); got != chat {
		t.Fatalf("from the environment: %+v", got)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_tests","arguments":{"tests":["web/a"]}}}` + "\n")
	var out strings.Builder
	if err := ServeMCPAsk(context.Background(), "/nonexistent.sock", "test", chattools.FromEnv(), in, &out); err != nil {
		t.Fatal(err)
	}
	byID := map[int]json.RawMessage{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var v struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		byID[v.ID] = v.Result
	}
	prefix := askuser.ServerName + "_"
	var init struct {
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(byID[0], &init)
	if want := askuser.Guidance(MCPAskTool) + "\n\n" + chattools.Guidance(prefix) + "\n\n" + chattools.TestGuidance(prefix) + "\n\n" + agent.TodoGuidance("todowrite"); init.Instructions != want {
		t.Fatalf("instructions:\n%s\nwant\n%s", init.Instructions, want)
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(byID[1], &list)
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "ask_user,list_chats,read_chat,send_to_chat,tests_format,tests_check,run_tests,test_results" {
		t.Fatalf("tools: %s", got)
	}
	if !strings.Contains(string(byID[2]), "Socrates' tests could not be reached") || !strings.Contains(string(byID[2]), `"isError":true`) {
		t.Fatalf("run_tests: %s", byID[2])
	}
}
