package claude

import (
	"encoding/json"
	"strings"
)

// frame is one line of `claude -p --output-format stream-json`, with the
// fields any of the frame types this adapter reads carry. The shapes are
// docs/design/research/03-claude-code.md §1.3 and 09-claude-subagents-
// workflows.md, observed against claude 2.1.309.
type frame struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	UUID    string `json:"uuid"`
	Session string `json:"session_id"`

	// assistant and user
	Message         json.RawMessage `json:"message"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	AgentID         string          `json:"agent_id"`
	IsReplay        bool            `json:"isReplay"`
	IsSynthetic     bool            `json:"isSynthetic"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	Origin          *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	LocalCommandRun *struct {
		Command string `json:"command"`
		Args    string `json:"args"`
	} `json:"local_command_run"`
	UserMessageUUID  string   `json:"user_message_uuid"`
	UserMessageUUIDs []string `json:"user_message_uuids"`
	ThinkingMS       int64    `json:"thinking_duration_ms"`
	Error            string   `json:"error"`

	// stream_event
	Event        *streamEvent `json:"event"`
	APIMessageID string       `json:"api_message_id"`

	// command_lifecycle
	CommandUUID string `json:"command_uuid"`
	State       string `json:"state"`

	// system/init
	Model         string   `json:"model"`
	Tools         []string `json:"tools"`
	SlashCommands []string `json:"slash_commands"`
	TermCommands  []string `json:"terminal_slash_commands"`
	Version       string   `json:"claude_code_version"`
	Mode          string   `json:"permissionMode"`

	// system/status
	Status        *string `json:"status"`
	CompactResult string  `json:"compact_result"`

	// system/compact_boundary
	CompactMetadata *struct {
		Trigger    string `json:"trigger"`
		PreTokens  int64  `json:"pre_tokens"`
		PostTokens int64  `json:"post_tokens"`
	} `json:"compact_metadata"`

	// system/api_retry
	Attempt     int   `json:"attempt"`
	MaxRetries  int   `json:"max_retries"`
	RetryDelay  int64 `json:"retry_delay_ms"`
	ErrorStatus *int  `json:"error_status"`

	// system/informational
	Content string `json:"content"`
	Level   string `json:"level"`

	// system/task_*, background_tasks_changed
	TaskID           string            `json:"task_id"`
	ToolUseID        string            `json:"tool_use_id"`
	TaskType         string            `json:"task_type"`
	Description      string            `json:"description"`
	SubagentType     string            `json:"subagent_type"`
	Prompt           string            `json:"prompt"`
	WorkflowName     string            `json:"workflow_name"`
	Backgrounded     bool              `json:"is_backgrounded"`
	Summary          string            `json:"summary"`
	LastToolName     string            `json:"last_tool_name"`
	Patch            *taskPatch        `json:"patch"`
	TaskUsage        *taskUsage        `json:"usage"`
	WorkflowProgress []json.RawMessage `json:"workflow_progress"`
	Tasks            []struct {
		TaskID string `json:"task_id"`
	} `json:"tasks"`

	// rate_limit_event
	RateLimit *rateLimitInfo `json:"rate_limit_info"`

	// result
	IsError     bool                  `json:"is_error"`
	Result      string                `json:"result"`
	TerminalRsn string                `json:"terminal_reason"`
	TotalCost   float64               `json:"total_cost_usd"`
	ResultUsage *apiUsage             `json:"-"`
	ModelUsage  map[string]modelUsage `json:"modelUsage"`
	Errors      []string              `json:"errors"`
	APIError    *int                  `json:"api_error_status"`
	NumTurns    int                   `json:"num_turns"`
	SinceLast   map[string]modelUsage `json:"model_usage_since_last_result"`

	// control_request, control_response, control_cancel_request
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  *controlResult  `json:"response"`
}

// decodeFrame reads one stdout line. A result's usage shares its key with
// the task frames' usage, so it is read on its own.
func decodeFrame(line []byte) (*frame, error) {
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		return nil, err
	}
	if f.Type == "result" {
		var u struct {
			Usage *apiUsage `json:"usage"`
		}
		if json.Unmarshal(line, &u) == nil {
			f.ResultUsage = u.Usage
		}
		f.TaskUsage = nil
	}
	return &f, nil
}

type taskPatch struct {
	Status string `json:"status"`
}

type taskUsage struct {
	TotalTokens int64 `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMS  int64 `json:"duration_ms"`
}

type rateLimitInfo struct {
	Status         string  `json:"status"`
	ResetsAt       int64   `json:"resetsAt"`
	RateLimitType  string  `json:"rateLimitType"`
	Utilization    float64 `json:"utilization"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

type apiUsage struct {
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
	Details     *struct {
		Thinking int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	Iterations []struct {
		Input       int64 `json:"input_tokens"`
		Output      int64 `json:"output_tokens"`
		CacheRead   int64 `json:"cache_read_input_tokens"`
		CacheCreate int64 `json:"cache_creation_input_tokens"`
	} `json:"iterations"`
}

type modelUsage struct {
	ContextWindow int64   `json:"contextWindow"`
	CostUSD       float64 `json:"costUSD"`
}

type streamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock *block          `json:"content_block"`
	Delta        *streamDelta    `json:"delta"`
	Message      json.RawMessage `json:"message"`
}

type streamDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// message is an assistant or user message body.
type message struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// block is one content block of a message.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// blocks is a message's content as blocks; a string content is one text
// block.
func (m *message) blocks() []block {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	var out []block
	_ = json.Unmarshal(m.Content, &out)
	return out
}

// text is a message's content when it is plain text: a string, or text
// blocks only.
func (m *message) text() (string, bool) {
	var parts []string
	for _, b := range m.blocks() {
		if b.Type != "text" {
			return "", false
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

// resultText is a tool_result's content as text, and the images in it.
func resultText(content json.RawMessage) (string, []block) {
	if len(content) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s, nil
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return string(content), nil
	}
	var text []string
	var images []block
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "image":
			images = append(images, b)
		case "tool_reference":
			text = append(text, b.Name)
		}
	}
	return strings.Join(text, "\n"), images
}

// controlResult is the body of a control_response.
type controlResult struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

// controlRequest is the body of a control_request the CLI sends.
type controlRequest struct {
	Subtype    string          `json:"subtype"`
	ToolName   string          `json:"tool_name"`
	Input      json.RawMessage `json:"input"`
	ToolUseID  string          `json:"tool_use_id"`
	ServerName string          `json:"server_name"`
	// Message is the JSON-RPC message of an mcp_message, and the text of an
	// elicitation.
	Message json.RawMessage `json:"message"`
	// An elicitation's server, mode and form (research/03 §4.3).
	MCPServerName   string          `json:"mcp_server_name"`
	Mode            string          `json:"mode"`
	RequestedSchema json.RawMessage `json:"requested_schema"`
	PropertyOrder   []string        `json:"property_order"`
}
