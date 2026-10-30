package agent

import "strings"

// Sender is the chat a user message came from when the agent of another chat
// sent it (chattools send_to_chat), as that chat was called then. A message
// with a Sender is not the person's: the chat shows it as the other agent's.
type Sender struct {
	Chat    string `json:"chat"`
	Title   string `json:"title,omitempty"`
	Harness string `json:"harness,omitempty"`
}

// The tags Socrates' note to the agent is wrapped in (AgentText), so that a
// transcript read back (StripModelNote) shows the message without it.
const (
	agentNoteOpen  = "<socrates-note>"
	agentNoteClose = "</socrates-note>"
)

// AgentText is a user message as its agent is given it: Socrates' note for
// the agent alone (Message.Note: who sent it, which other chats work beside
// it) before the text. The journal and the chat keep the text without it.
func AgentText(note, text string) string {
	if note = strings.TrimSpace(note); note == "" {
		return text
	}
	return WithNote(agentNoteOpen+"\n"+note+"\n"+agentNoteClose, text)
}
