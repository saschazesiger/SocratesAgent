package engine

import "github.com/saschazesiger/SocratesAgent/internal/store"

// Event is the envelope pushed to the browser over SSE. chat.js switches on
// exactly these types, and the offline story - "highest revision seen means
// everything up to here is mine" - is built on them.
//
// Type is one of "step", "step_removed", "message", "run", "chat", plus the
// two the HTTP layer synthesises ("ready", "resync") and the heartbeat
// ("ping"). There are no new types.
type Event struct {
	Type    string         `json:"type"`
	Step    *store.Step    `json:"step,omitempty"`
	StepID  string         `json:"step_id,omitempty"`
	Run     *store.Run     `json:"run,omitempty"`
	Message *store.Message `json:"message,omitempty"`
	Chat    *store.Chat    `json:"chat,omitempty"`
}
