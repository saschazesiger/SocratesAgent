package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

const (
	toolDelegate = "delegate_to_agent"
	toolAsk      = "ask_user"
)

// buildTools describes the orchestrator's capabilities to the model.
func buildTools(s config.Settings) []openrouter.Tool {
	tools := []openrouter.Tool{}

	enabled := s.EnabledBackends()
	if len(enabled) > 0 {
		ids := make([]string, 0, len(enabled))
		var desc strings.Builder
		desc.WriteString("Hand a complete task to a specialised coding agent that runs on the user's machine " +
			"and wait for it to finish. The agent has its own file system tools and cannot see this conversation, " +
			"so the task must be self contained. Available agents:\n")
		for _, b := range enabled {
			ids = append(ids, b.ID)
			fmt.Fprintf(&desc, "- %s (%s): %s\n", b.ID, b.Name, strings.TrimSpace(b.Description))
		}
		enumJSON, _ := json.Marshal(ids)
		params := fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "agent": {"type": "string", "enum": %s, "description": "Which agent should do the work."},
    "task": {"type": "string", "description": "The complete brief. State the goal, the constraints and what the answer should contain."},
    "title": {"type": "string", "description": "Short label (max 8 words) shown to the user while the agent works."}
  },
  "required": ["agent", "task"],
  "additionalProperties": false
}`, string(enumJSON))
		tools = append(tools, openrouter.Tool{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        toolDelegate,
				Description: desc.String(),
				Parameters:  json.RawMessage(params),
			},
		})
	}

	tools = append(tools, openrouter.Tool{
		Type: "function",
		Function: openrouter.ToolFunction{
			Name: toolAsk,
			Description: "Ask the user a question and wait for their answer. Use it when a decision is genuinely " +
				"theirs to make, and offer 2 to 4 short options. The question and the options may be read out loud.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The question, one sentence."},
    "options": {
      "type": "array",
      "maxItems": 4,
      "description": "Selectable answers. Keep every label to a few words.",
      "items": {
        "type": "object",
        "properties": {
          "label": {"type": "string"},
          "description": {"type": "string", "description": "Optional one line explanation."}
        },
        "required": ["label"],
        "additionalProperties": false
      }
    },
    "allow_free_text": {"type": "boolean", "description": "Also let the user type their own answer. Default true."}
  },
  "required": ["question"],
  "additionalProperties": false
}`),
		},
	})

	return tools
}

// execTool runs one tool call and returns the string handed back to the model.
func (e *Engine) execTool(ctx context.Context, chat *store.Chat, run *store.Run, call openrouter.ToolCall) string {
	switch call.Function.Name {
	case toolDelegate:
		return e.execDelegate(ctx, chat, run, call)
	case toolAsk:
		return e.execAsk(ctx, run, call)
	default:
		return fmt.Sprintf("Unknown tool %q. Use one of: %s, %s.", call.Function.Name, toolDelegate, toolAsk)
	}
}

func (e *Engine) execDelegate(ctx context.Context, chat *store.Chat, run *store.Run, call openrouter.ToolCall) string {
	var args struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(orDefault(call.Function.Arguments, "{}")), &args); err != nil {
		return fmt.Sprintf("Could not parse the arguments (%v). Send valid JSON with `agent` and `task`.", err)
	}
	settings := e.Settings()
	backend, ok := settings.Backend(strings.TrimSpace(args.Agent))
	if !ok || !backend.Enabled {
		names := []string{}
		for _, b := range settings.EnabledBackends() {
			names = append(names, b.ID)
		}
		return fmt.Sprintf("There is no enabled agent called %q. Available: %s.", args.Agent, strings.Join(names, ", "))
	}
	if strings.TrimSpace(args.Task) == "" {
		return "The `task` argument was empty. Describe the complete job for the agent."
	}

	text, err := e.runDelegate(ctx, chat, run, backend, args.Task, args.Title)
	if err != nil {
		if ctx.Err() != nil {
			return "The run was stopped by the user."
		}
		return fmt.Sprintf("The %s agent failed: %v\nDecide whether to retry with a different brief, use another agent, or report the problem to the user.", backend.Name, err)
	}
	return fmt.Sprintf("%s finished. Its final report:\n\n%s", backend.Name, truncateMiddle(text, 24000))
}

func (e *Engine) execAsk(ctx context.Context, run *store.Run, call openrouter.ToolCall) string {
	var args struct {
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		AllowFreeText *bool `json:"allow_free_text"`
	}
	if err := json.Unmarshal([]byte(orDefault(call.Function.Arguments, "{}")), &args); err != nil {
		return fmt.Sprintf("Could not parse the arguments (%v). Send valid JSON with a `question`.", err)
	}
	if strings.TrimSpace(args.Question) == "" {
		return "The `question` argument was empty."
	}
	options := make([]store.Option, 0, len(args.Options))
	for _, o := range args.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			continue
		}
		options = append(options, store.Option{Value: label, Label: label, Description: strings.TrimSpace(o.Description)})
	}
	freeText := true
	if args.AllowFreeText != nil {
		freeText = *args.AllowFreeText
	}
	answer, err := e.Ask(ctx, run, "", "ask", strings.TrimSpace(args.Question), options, freeText)
	if err != nil {
		if ctx.Err() != nil {
			return "The run was stopped by the user."
		}
		return fmt.Sprintf("Could not ask the user: %v", err)
	}
	return fmt.Sprintf("The user answered: %s", answer)
}
