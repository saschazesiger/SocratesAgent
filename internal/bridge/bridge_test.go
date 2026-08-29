package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runProtocol(t *testing.T, lines []string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := RunWith(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		responses = append(responses, decoded)
	}
	return responses
}

func TestInitializeAndToolsList(t *testing.T) {
	responses := runProtocol(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	})
	if len(responses) != 2 {
		t.Fatalf("expected two responses, got %d: %#v", len(responses), responses)
	}
	result := responses[0]["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocol version = %v", result["protocolVersion"])
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "permission_prompt" {
		t.Fatalf("unexpected tool list: %#v", tools)
	}
}

func TestPermissionAllow(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Socrates-Bridge-Token") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allow":true}`))
	}))
	defer server.Close()

	t.Setenv("SOCRATES_BRIDGE_URL", server.URL)
	t.Setenv("SOCRATES_BRIDGE_TOKEN", "secret")
	t.Setenv("SOCRATES_RUN_ID", "run-1")

	responses := runProtocol(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"permission_prompt","arguments":{"tool_name":"Bash","input":{"command":"ls"}}}}`,
	})
	content := responses[0]["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	var decision map[string]any
	if err := json.Unmarshal([]byte(text), &decision); err != nil {
		t.Fatalf("decision is not JSON: %q", text)
	}
	if decision["behavior"] != "allow" {
		t.Fatalf("expected allow, got %#v", decision)
	}
	updated, ok := decision["updatedInput"].(map[string]any)
	if !ok || updated["command"] != "ls" {
		t.Fatalf("input was not passed through: %#v", decision)
	}
	if received["run_id"] != "run-1" || received["tool_name"] != "Bash" {
		t.Fatalf("server received %#v", received)
	}
}

func TestPermissionDeny(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allow":false,"message":"nope"}`))
	}))
	defer server.Close()
	t.Setenv("SOCRATES_BRIDGE_URL", server.URL)

	responses := runProtocol(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"permission_prompt","arguments":{"tool_name":"Write","input":{}}}}`,
	})
	text := responses[0]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"behavior":"deny"`) || !strings.Contains(text, "nope") {
		t.Fatalf("unexpected decision %q", text)
	}
}

func TestPermissionServerUnreachableDeniesWithReason(t *testing.T) {
	t.Setenv("SOCRATES_BRIDGE_URL", "")
	responses := runProtocol(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"permission_prompt","arguments":{"tool_name":"Bash","input":{}}}}`,
	})
	text := responses[0]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "deny") || !strings.Contains(text, "unavailable") {
		t.Fatalf("unexpected decision %q", text)
	}
}

func TestUnknownMethod(t *testing.T) {
	responses := runProtocol(t, []string{`{"jsonrpc":"2.0","id":9,"method":"nonsense/thing"}`})
	if responses[0]["error"] == nil {
		t.Fatalf("expected a JSON-RPC error, got %#v", responses[0])
	}
}
