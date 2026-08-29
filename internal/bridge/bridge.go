// Package bridge implements the "socrates bridge" subcommand: a minimal MCP
// server over stdio that a delegate agent launches when it runs in interactive
// approval mode. Every permission request it receives is forwarded to the
// Socrates server, which shows it in the web UI and waits for the user.
package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Run serves the MCP protocol on stdin/stdout until the parent closes the pipe.
func Run() error { return RunWith(os.Stdin, os.Stdout) }

// RunWith is Run with explicit streams, which makes it testable.
func RunWith(stdin io.Reader, stdout io.Writer) error {
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8<<20)
	out := bufio.NewWriter(stdout)
	defer out.Flush()

	write := func(resp rpcResponse) {
		resp.JSONRPC = "2.0"
		b, err := json.Marshal(resp)
		if err != nil {
			return
		}
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		isNotification := len(req.ID) == 0
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			version := params.ProtocolVersion
			if version == "" {
				version = "2024-11-05"
			}
			write(rpcResponse{ID: req.ID, Result: map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "socrates-permission-bridge", "version": "1"},
			}})
		case "notifications/initialized", "notifications/cancelled":
			// nothing to do
		case "ping":
			if !isNotification {
				write(rpcResponse{ID: req.ID, Result: map[string]any{}})
			}
		case "tools/list":
			write(rpcResponse{ID: req.ID, Result: map[string]any{"tools": []any{
				map[string]any{
					"name": "permission_prompt",
					"description": "Ask the human operator, through the Socrates web interface, whether this " +
						"tool call may run.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"tool_name":   map[string]any{"type": "string"},
							"input":       map[string]any{"type": "object"},
							"tool_use_id": map[string]any{"type": "string"},
						},
						"required": []string{"tool_name", "input"},
					},
				},
			}}})
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			text := decide(params.Arguments)
			write(rpcResponse{ID: req.ID, Result: map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
			}})
		default:
			if !isNotification {
				write(rpcResponse{ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
			}
		}
	}
	return in.Err()
}

// decide asks the Socrates server and renders the answer in the shape Claude
// Code expects from a permission prompt tool.
func decide(arguments json.RawMessage) string {
	var args struct {
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(arguments, &args)

	allow, message, err := ask(args.ToolName, args.Input)
	if err != nil {
		// Failing closed would deadlock the agent on an unreachable server, so
		// explain the problem instead and let the agent continue without the tool.
		return marshal(map[string]any{
			"behavior": "deny",
			"message":  fmt.Sprintf("The approval bridge is unavailable (%v).", err),
		})
	}
	if allow {
		input := args.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return marshal(map[string]any{"behavior": "allow", "updatedInput": json.RawMessage(input)})
	}
	if message == "" {
		message = "The user denied this action."
	}
	return marshal(map[string]any{"behavior": "deny", "message": message})
}

func ask(toolName string, input json.RawMessage) (bool, string, error) {
	url := os.Getenv("SOCRATES_BRIDGE_URL")
	if url == "" {
		return false, "", fmt.Errorf("SOCRATES_BRIDGE_URL is not set")
	}
	payload := map[string]any{
		"run_id":     os.Getenv("SOCRATES_RUN_ID"),
		"step_id":    os.Getenv("SOCRATES_STEP_ID"),
		"agent_name": os.Getenv("SOCRATES_AGENT_NAME"),
		"tool_name":  toolName,
		"input":      json.RawMessage(orEmptyObject(input)),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, "", err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Socrates-Bridge-Token", os.Getenv("SOCRATES_BRIDGE_TOKEN"))

	// The user may take a while to answer; the server ends the wait itself.
	client := &http.Client{Timeout: 6 * time.Hour}
	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return false, "", fmt.Errorf("server replied %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Allow   bool   `json:"allow"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "", err
	}
	return out.Allow, out.Message, nil
}

func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"behavior":"deny","message":"internal bridge error"}`
	}
	return string(b)
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
