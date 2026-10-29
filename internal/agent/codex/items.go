package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// item is the part of a ThreadItem the adapter reads. Fields a type does not
// have are absent from its JSON.
type item struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// userMessage
	ClientID string            `json:"clientId"`
	Content  []json.RawMessage `json:"content"`

	// agentMessage, plan
	Text      string          `json:"text"`
	Phase     *string         `json:"phase"`
	Delivery  *string         `json:"delivery"`
	Questions json.RawMessage `json:"questions"`

	// reasoning
	Summary []json.RawMessage `json:"summary"`

	// commandExecution
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	Source           string          `json:"source"`
	Status           string          `json:"status"`
	CommandActions   json.RawMessage `json:"commandActions"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	DurationMs       *int64          `json:"durationMs"`
	ProcessID        *string         `json:"processId"`

	// fileChange
	Changes []struct {
		Path string `json:"path"`
		Kind struct {
			Type     string  `json:"type"`
			MovePath *string `json:"move_path"`
		} `json:"kind"`
		Diff string `json:"diff"`
	} `json:"changes"`

	// mcpToolCall, dynamicToolCall, collabAgentToolCall
	Server            string          `json:"server"`
	Tool              string          `json:"tool"`
	Namespace         *string         `json:"namespace"`
	Arguments         json.RawMessage `json:"arguments"`
	Result            json.RawMessage `json:"result"`
	Error             json.RawMessage `json:"error"`
	ContentItems      json.RawMessage `json:"contentItems"`
	Success           *bool           `json:"success"`
	SenderThreadID    string          `json:"senderThreadId"`
	ReceiverThreadIDs []string        `json:"receiverThreadIds"`
	Prompt            *string         `json:"prompt"`
	Model             string          `json:"model"`
	ReasoningEffort   string          `json:"reasoningEffort"`
	AgentsStates      json.RawMessage `json:"agentsStates"`

	// subAgentActivity
	Kind          string `json:"kind"`
	AgentThreadID string `json:"agentThreadId"`
	AgentPath     string `json:"agentPath"`

	// imageView, enteredReviewMode, exitedReviewMode
	Path   string `json:"path"`
	Review string `json:"review"`

	// imageGeneration
	SavedPath     *string         `json:"savedPath"`
	RevisedPrompt *string         `json:"revisedPrompt"`
	Failure       json.RawMessage `json:"failure"`

	// hookPrompt
	Fragments []struct {
		Text string `json:"text"`
	} `json:"fragments"`
}

// The codes of the notices the adapter writes of its own, in a part
// ({code, detail}, chatparts.js noticePart) or a line.
const (
	// noticeGoal: the thread's goal was set or changed status, or a turn
	// codex started by itself goes on toward it.
	noticeGoal = "goal"
	// noticeSelf: a turn codex started by itself, for no goal the adapter
	// knows of.
	noticeSelf = "self_started"
	// noticeHook: a hook blocked, failed or said something, or added to the
	// conversation.
	noticeHook = "hook"
)

// mapped is what one item becomes: a part of some kind, in some state, with
// its text (nil: keep what the deltas built) and its content.
type mapped struct {
	skip    bool
	kind    string
	state   string
	text    *string
	content json.RawMessage
}

// itemState is an item status as a part state.
func itemState(status string, completed bool) string {
	switch status {
	case "failed", "declined":
		return agent.PartError
	case "interrupted":
		return agent.PartInterrupted
	case "completed":
		return agent.PartDone
	}
	if completed {
		return agent.PartDone
	}
	return agent.PartRunning
}

func jsonOf(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func strp(s string) *string { return &s }

// mapItem is the table of AGENT-HOST.md §5 (Codex): which part each item is,
// and the content shape the chat view renders it from.
func mapItem(it item, raw json.RawMessage, completed bool) mapped {
	streamState := agent.PartStreaming
	if completed {
		streamState = agent.PartDone
	}
	switch it.Type {
	case "userMessage", "subAgentActivity":
		return mapped{skip: true}
	case "agentMessage":
		m := mapped{kind: agent.PartText, state: streamState}
		if completed || it.Text != "" {
			m.text = strp(it.Text)
		}
		if it.Phase != nil || it.Delivery != nil || len(it.Questions) > 0 && string(it.Questions) != "null" {
			m.content = jsonOf(map[string]any{"phase": it.Phase, "delivery": it.Delivery, "questions": it.Questions})
		}
		return m
	case "reasoning":
		m := mapped{kind: agent.PartReasoning, state: streamState}
		if completed {
			text := joinTexts(it.Summary)
			if body := joinTexts(it.Content); body != "" {
				if text != "" {
					text += "\n\n"
				}
				text += body
			}
			if text != "" {
				m.text = strp(text)
			}
		}
		return m
	case "plan":
		m := mapped{kind: agent.PartPlan, state: streamState}
		if completed || it.Text != "" {
			m.text = strp(it.Text)
		}
		return m
	case "commandExecution":
		m := mapped{kind: agent.PartTool, state: itemState(it.Status, completed)}
		if it.AggregatedOutput != nil && *it.AggregatedOutput != "" {
			m.text = it.AggregatedOutput
		}
		m.content = jsonOf(map[string]any{"tool": "command", "command": it.Command, "cwd": it.Cwd, "source": it.Source,
			"actions": it.CommandActions, "status": it.Status, "exit_code": it.ExitCode, "duration_ms": it.DurationMs,
			"process_id": it.ProcessID})
		return m
	case "fileChange":
		changes := make([]map[string]any, 0, len(it.Changes))
		for _, c := range it.Changes {
			changes = append(changes, map[string]any{"path": c.Path, "kind": c.Kind.Type, "move_path": c.Kind.MovePath, "diff": c.Diff})
		}
		return mapped{kind: agent.PartDiff, state: itemState(it.Status, completed),
			content: jsonOf(map[string]any{"tool": "file_change", "changes": changes, "status": it.Status})}
	case "mcpToolCall":
		return mapped{kind: agent.PartTool, state: itemState(it.Status, completed),
			content: jsonOf(map[string]any{"tool": "mcp", "server": it.Server, "name": it.Tool, "arguments": it.Arguments,
				"result": it.Result, "error": it.Error, "status": it.Status, "duration_ms": it.DurationMs})}
	case "dynamicToolCall":
		return mapped{kind: agent.PartTool, state: itemState(it.Status, completed),
			content: jsonOf(map[string]any{"tool": "dynamic", "namespace": it.Namespace, "name": it.Tool, "arguments": it.Arguments,
				"content_items": it.ContentItems, "success": it.Success, "status": it.Status, "duration_ms": it.DurationMs})}
	case "collabAgentToolCall":
		return mapped{kind: agent.PartTool, state: itemState(it.Status, completed),
			content: jsonOf(map[string]any{"tool": "collab", "name": it.Tool, "prompt": it.Prompt, "sender": it.SenderThreadID,
				"receivers": it.ReceiverThreadIDs, "model": it.Model, "effort": it.ReasoningEffort, "agents": it.AgentsStates,
				"status": it.Status})}
	case "imageView":
		return mapped{kind: agent.PartImage, state: agent.PartDone, content: jsonOf(map[string]any{"path": it.Path})}
	case "imageGeneration":
		return imageGeneration(it, completed)
	case "enteredReviewMode":
		return mapped{kind: agent.PartNotice, state: agent.PartDone, text: strp("Reviewing: " + it.Review),
			content: jsonOf(map[string]any{"notice": "review_started"})}
	case "exitedReviewMode":
		return mapped{kind: agent.PartText, state: agent.PartDone, text: strp(it.Review),
			content: jsonOf(map[string]any{"review": true})}
	case "contextCompaction":
		m := mapped{kind: agent.PartNotice, state: streamState, content: jsonOf(map[string]any{"notice": "compacted"})}
		if completed {
			m.text = strp("The conversation was compacted.")
		} else {
			m.text = strp("Compacting the conversation…")
		}
		return m
	case "webSearch":
		return mapped{kind: agent.PartTool, state: streamState, content: jsonOf(map[string]any{"tool": "web_search", "item": raw})}
	case "sleep":
		// The sleep tool: the row says for how long, as the call asked.
		summary := ""
		if it.DurationMs != nil && *it.DurationMs > 0 {
			summary = "for " + spoken(time.Duration(*it.DurationMs)*time.Millisecond)
		}
		return mapped{kind: agent.PartTool, state: itemState("", completed),
			content: jsonOf(map[string]any{"tool": "sleep", "summary": summary, "item": raw})}
	case "hookPrompt":
		// What a hook added to the conversation (a Stop hook that sends the
		// model on, a prompt's extra context): a notice of the hook's.
		var texts []string
		for _, f := range it.Fragments {
			if t := strings.TrimSpace(f.Text); t != "" {
				texts = append(texts, t)
			}
		}
		detail := strings.Join(texts, "\n\n")
		return mapped{kind: agent.PartNotice, state: agent.PartDone, text: strp(withFirstLine("A hook added to the conversation", detail)),
			content: jsonOf(map[string]any{"code": noticeHook, "detail": detail})}
	}
	return mapped{kind: agent.PartTool, state: itemState(it.Status, completed),
		content: jsonOf(map[string]any{"tool": it.Type, "item": raw})}
}

// imageGeneration is an image the model made: the file codex saved it to,
// which the chat shows, and the prompt it was made from. One the account's
// limit refused fails, with what the failure says.
func imageGeneration(it item, completed bool) mapped {
	c := map[string]any{"tool": "image_generation", "status": it.Status}
	if it.SavedPath != nil && *it.SavedPath != "" {
		c["path"] = *it.SavedPath
	}
	if it.RevisedPrompt != nil && *it.RevisedPrompt != "" {
		c["prompt"] = *it.RevisedPrompt
	}
	state := itemState(it.Status, completed)
	if len(it.Failure) > 0 && string(it.Failure) != "null" {
		c["failure"] = it.Failure
		state = agent.PartError
	}
	return mapped{kind: agent.PartImage, state: state, content: jsonOf(c)}
}

// spoken is a length of time the way a line says it: "45 s", "2 min",
// "1 h 5 min".
func spoken(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", max(1, int(d.Round(time.Second)/time.Second)))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	}
	h, m := int(d/time.Hour), int((d%time.Hour).Round(time.Minute)/time.Minute)
	if m == 60 {
		h, m = h+1, 0
	}
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// lineMax is how long a notice's line may be; its detail has all of it.
const lineMax = 200

// firstLine is the first line of s, trimmed and cut to lineMax characters.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > lineMax {
		s = string(r[:lineMax-1]) + "…"
	}
	return s
}

// withFirstLine is "lead: <the first line of detail>", or "lead." when
// detail says nothing.
func withFirstLine(lead, detail string) string {
	if first := firstLine(detail); first != "" {
		return lead + ": " + first
	}
	return lead + "."
}

// joinTexts reads a reasoning summary or content array, whose elements are
// strings in a ThreadItem and {text} objects in a rollout.
func joinTexts(parts []json.RawMessage) string {
	var out []string
	for _, p := range parts {
		var s string
		if json.Unmarshal(p, &s) != nil {
			var obj struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(p, &obj) != nil {
				continue
			}
			s = obj.Text
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}
