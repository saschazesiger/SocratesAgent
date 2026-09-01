package claude

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// envelope is one stdout line. Only the fields the mapping table reads are
// declared: a line whose `type` is not below is dropped and counted, never
// guessed at.
type envelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	SessionID string  `json:"session_id"`
	Parent    *string `json:"parent_tool_use_id"`
	IsReplay  bool    `json:"isReplay"`

	// system/init
	Model string `json:"model"`

	// stream_event / assistant / user
	Event         json.RawMessage `json:"event"`
	Message       json.RawMessage `json:"message"`
	ToolUseResult json.RawMessage `json:"tool_use_result"`

	// control_response
	Response *controlResponse `json:"response"`

	// result
	IsError bool `json:"is_error"`
	// NumTurns is 0 only on a result that ended nothing - which, together
	// with is_error and an init that never came, is the signature of a
	// --resume the CLI could not read (R-3, BLOCKER-1).
	NumTurns       int                   `json:"num_turns"`
	TerminalReason string                `json:"terminal_reason"`
	Result         string                `json:"result"`
	TotalCostUSD   float64               `json:"total_cost_usd"`
	Usage          *usageBlock           `json:"usage"`
	ModelUsage     map[string]modelUsage `json:"modelUsage"`
	SubagentStats  *subagentStats        `json:"subagent_stats"`
}

type controlResponse struct {
	Subtype   string `json:"subtype"`
	RequestID string `json:"request_id"`
	Error     string `json:"error"`
}

type usageBlock struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokensDetails      struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type modelUsage struct {
	ContextWindow int64 `json:"contextWindow"`
}

type subagentStats struct {
	Spawned int `json:"spawned"`
}

// streamFrame is the inner Anthropic Messages-API event carried by a
// stream_event line.
type streamFrame struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// assistantMessage is the `message` of an assistant line: one completed
// content block per line, as observed live (FK-5).
type assistantMessage struct {
	ID      string          `json:"id"`
	Content []contentBlock  `json:"content"`
	Usage   json.RawMessage `json:"usage"`
}

// userMessage is the `message` of a user line, whose content holds the
// tool_result blocks.
type userMessage struct {
	Content []contentBlock `json:"content"`
}

// contentBlock covers every block type the mapping table names. `input` and
// `content` stay raw because their shapes differ per tool.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// userLine is the one line a turn is: a user message on stdin.
func userLine(text string) ([]byte, error) {
	b, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude: encoding the turn: %w", err)
	}
	return append(b, '\n'), nil
}

// controlLine is the interrupt: the one control_request shape the research
// verified (§3).
func controlLine(requestID string) ([]byte, error) {
	b, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    map[string]any{"subtype": "interrupt"},
	})
	if err != nil {
		return nil, fmt.Errorf("claude: encoding the interrupt: %w", err)
	}
	return append(b, '\n'), nil
}

// renderContent turns a tool_result's `content` into text. It is a plain
// string on success and an array of blocks in other shapes; anything else
// renders as its compact JSON rather than being dropped.
func renderContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// toolUseResultText is the fallback for a tool_result whose `content` was
// empty: the structured companion object's stdout and stderr. It is also a
// bare string in the guardrail case ("Error: Blocked: ...").
func toolUseResultText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	switch {
	case v.Stdout != "" && v.Stderr != "":
		return v.Stdout + "\n" + v.Stderr
	case v.Stderr != "":
		return v.Stderr
	default:
		return v.Stdout
	}
}

// describe writes a tool call for a human: a title for the card's header and
// a one-line summary of what it was asked to do.
func describe(name string, input json.RawMessage) (title, oneLine string) {
	var args map[string]any
	_ = json.Unmarshal(input, &args)

	str := func(k string) string {
		s, _ := args[k].(string)
		return s
	}
	switch name {
	case "Bash":
		title, oneLine = "Ran a command", str("command")
	case "BashOutput":
		title, oneLine = "Read command output", str("bash_id")
	case "Read":
		title, oneLine = "Read "+base(str("file_path")), str("file_path")
	case "Write":
		title, oneLine = "Wrote "+base(str("file_path")), str("file_path")
	case "Edit", "NotebookEdit":
		title, oneLine = "Edited "+base(str("file_path")), str("file_path")
	case "Glob", "Grep":
		title, oneLine = "Searched the files", str("pattern")
	case "WebFetch":
		title, oneLine = "Fetched a page", str("url")
	case "WebSearch":
		title, oneLine = "Searched the web", str("query")
	case "Task":
		title = "Started a subagent"
		oneLine = strings.TrimPrefix(str("subagent_type")+": "+str("description"), ": ")
	default:
		title = name
	}
	if oneLine == "" {
		oneLine = firstOf(args, "command", "description", "prompt", "query", "pattern", "file_path", "url")
	}
	if oneLine == "" && len(args) > 0 {
		oneLine = compact(args)
	}
	return title, oneline(oneLine)
}

// firstOf returns the first of the named string keys that is set, so an
// unknown tool still gets a summary instead of a bare name.
func firstOf(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := args[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// compact renders an unknown tool's arguments in key order, which keeps the
// summary stable between two calls of the same tool.
func compact(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		b, err := json.Marshal(args[k])
		if err != nil {
			continue
		}
		parts = append(parts, k+"="+string(b))
	}
	return strings.Join(parts, " ")
}

// oneline flattens and shortens a summary so it fits on a card's one line.
func oneline(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	const limit = 200
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func base(path string) string {
	if path == "" {
		return "a file"
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return path
}

// inputJSON is the raw arguments for the expandable card, normalised to
// compact JSON so the journal does not carry the CLI's whitespace.
func inputJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}
