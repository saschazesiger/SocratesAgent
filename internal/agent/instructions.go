package agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// A favourite model can carry instructions of the person's own, per harness
// (Admin; config.ModelPick.Instructions): what a chat on that model is to
// be told, and only while it runs on that model. Each adapter gives the agent
// the instructions of the model the chat runs on in the agent's own
// instructions where the CLI takes them anew: Claude Code's system prompt at
// every start, OpenCode's system on every prompt, Codex's developer
// instructions when a thread is started (a thread keeps those for good; a
// resume cannot change them). Where they cannot change - a switch of model
// while Claude Code runs, any change to a Codex thread - the next message
// carries the new instructions in a note (ModelNotes.Switch).

// InstructionsFor is what StartSpec.Instructions holds for model, "" for none.
func (s StartSpec) InstructionsFor(model string) string {
	return strings.TrimSpace(s.Instructions[strings.TrimSpace(model)])
}

// ModelPrompt is the instructions of model as part of an agent's own
// instructions, "" when there are none.
func ModelPrompt(model, text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	return "The person's instructions for " + model + ", the model this chat runs on:\n\n" + text
}

// The tags a note is wrapped in, so that a transcript read back
// (StripModelNote) shows the person's message without it.
const (
	modelNoteOpen  = "<model-instructions>"
	modelNoteClose = "</model-instructions>"
)

// modelNotesFile is where LoadModelNotes keeps, in the adapter's state
// directory, which instructions the conversation has.
const modelNotesFile = "model-instructions.json"

// ModelNotes is which model's instructions an agent has been given.
type ModelNotes struct {
	all  map[string]string
	path string // where they are kept, "" for not at all
	// Model is the model whose instructions the agent has, and Text those
	// instructions, "" for none.
	Model string `json:"model"`
	Text  string `json:"text"`
}

// NewModelNotes is the instructions of a CLI that takes its own anew at
// every start (Claude Code): the agent has those of spec.Model, which the
// adapter put into the system prompt.
func NewModelNotes(spec StartSpec) *ModelNotes {
	m := strings.TrimSpace(spec.Model)
	return &ModelNotes{all: spec.Instructions, Model: m, Text: spec.InstructionsFor(m)}
}

// LoadModelNotes is the instructions of a CLI whose conversation keeps the
// ones it was started with (Codex), kept in spec.StateDir across starts. A
// resumed conversation has what was kept, none when nothing was (a chat from
// before there were any); a new one gets its own with Fresh.
func LoadModelNotes(spec StartSpec) *ModelNotes {
	n := &ModelNotes{all: spec.Instructions}
	if spec.StateDir != "" {
		n.path = filepath.Join(spec.StateDir, modelNotesFile)
	}
	if spec.ResumeID != "" && n.path != "" {
		if raw, err := os.ReadFile(n.path); err == nil {
			_ = json.Unmarshal(raw, n)
		}
	}
	return n
}

// Fresh is the part of a new conversation's own instructions the
// instructions of model are, "" for none; the agent has them from here on.
func (n *ModelNotes) Fresh(model string) string {
	if n == nil {
		return ""
	}
	model = strings.TrimSpace(model)
	n.set(model, strings.TrimSpace(n.all[model]))
	return ModelPrompt(n.Model, n.Text)
}

// Switch is the note the next message carries when the chat runs on model:
// the model's instructions, or that the ones the agent has no longer apply.
// It is "" when model's instructions are the ones the agent already has; a
// model "" (the CLI's own choice) is no change. The agent counts as told
// from here on.
func (n *ModelNotes) Switch(model string) string {
	model = strings.TrimSpace(model)
	if n == nil || model == "" {
		return ""
	}
	text := strings.TrimSpace(n.all[model])
	old, oldText := n.Model, n.Text
	if model == old && text == oldText {
		return ""
	}
	n.set(model, text)
	if text == oldText {
		return ""
	}
	var b strings.Builder
	b.WriteString(modelNoteOpen + "\n")
	switch {
	case model != old:
		b.WriteString("This chat now runs on " + model + ".")
		if oldText != "" {
			b.WriteString(" The person's instructions for " + old + " no longer apply.")
		}
		if text != "" {
			b.WriteString(" The person's instructions for " + model + ":\n\n" + text)
		}
	case text != "":
		b.WriteString("The person changed their instructions for " + model + ", the model this chat runs on. " +
			"They are now:\n\n" + text)
	default:
		b.WriteString("The person withdrew their instructions for " + model + ", the model this chat runs on: " +
			"they no longer apply.")
	}
	b.WriteString("\n" + modelNoteClose)
	return b.String()
}

func (n *ModelNotes) set(model, text string) {
	if n.Model == model && n.Text == text {
		return
	}
	n.Model, n.Text = model, text
	if n.path == "" {
		return
	}
	raw, _ := json.Marshal(n)
	err := os.MkdirAll(filepath.Dir(n.path), 0o700)
	if err == nil {
		err = os.WriteFile(n.path, raw, 0o600)
	}
	if err != nil {
		log.Printf("model instructions: %v", err)
	}
}

// WithNote is a message's text with a note before it.
func WithNote(note, text string) string {
	if note == "" {
		return text
	}
	return note + "\n\n" + text
}

// StripModelNote is a user message's text as read back from a CLI's
// transcript, without the note an adapter put before it.
func StripModelNote(text string) string {
	if !strings.HasPrefix(text, modelNoteOpen) {
		return text
	}
	end := strings.Index(text, modelNoteClose)
	if end < 0 {
		return text
	}
	return strings.TrimLeft(text[end+len(modelNoteClose):], "\n")
}
