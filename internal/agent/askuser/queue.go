package askuser

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ask_user never waits (REBUILD.md §5.3): a call puts its question into the
// person's queue and returns at once with the question's id, and the answer
// arrives later as a message. check_questions lists the questions and
// withdraw_question takes one back. This file has what the three say to the
// model.

// NewID is the id of a new ask_user question: the ask's id in the journal,
// and what the model names the question by. Short, because a model copies it
// into withdraw_question.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "ask_" + hex.EncodeToString(b[:])
}

// QueuedText is what an ask_user call returns at once: the question's id and
// what to do now (REBUILD.md §5.2).
func QueuedText(id string, p *Payload) string {
	waits := ""
	if p != nil && p.Blocks != "" {
		waits = " (waiting for it: " + p.Blocks + ")"
	}
	return "Question " + id + " is now with the person. Their answer arrives by itself as a message, maybe hours from now: " +
		"do not wait for it and do not check for it. Now do all the work that does not depend on it" + waits + ". " +
		"Do not build on a guessed answer, except for a small decision that is easy to undo: then go on with the option you " +
		"recommended and say that you assumed it. When only work that waits for open questions is left, end your turn with a " +
		"short summary of what is done and what waits for which question."
}

// SubagentText is what a subagent's ask_user call returns: nothing was
// asked, because an answer would reach only the main agent.
const SubagentText = "Nothing was asked: only the main agent can ask the person, because the answer reaches only the main agent. " +
	"Put this question into your result for the main agent instead, and go on with what does not depend on it."

// The states of a question as check_questions names them.
const (
	StateOpen      = "open"
	StateAnswered  = "answered"
	StateWithdrawn = "withdrawn"
	StateClosed    = "closed"
)

// Asked is one question of the chat as check_questions reads it.
type Asked struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
	State   string          `json:"state"`
	// Answer is the person's answer of an answered question; Reason the
	// agent's reason of a withdrawn one.
	Answer json.RawMessage `json:"answer,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// MaxChecked is how many questions check_questions lists, the newest.
const MaxChecked = 20

// QuestionsText is check_questions' answer: the questions, newest last, and
// what to do while some are open.
func QuestionsText(list []Asked) string {
	if len(list) == 0 {
		return "You have asked the person no questions in this chat."
	}
	var b strings.Builder
	skipped := 0
	if len(list) > MaxChecked {
		skipped = len(list) - MaxChecked
		list = list[skipped:]
	}
	b.WriteString("Your questions in this chat, newest last")
	if skipped > 0 {
		fmt.Fprintf(&b, " (the %d before them are left out)", skipped)
	}
	b.WriteString(":\n")
	open := 0
	for _, q := range list {
		p := Decode(q.Payload)
		question := ""
		if p != nil && len(p.Questions) > 0 {
			parts := make([]string, 0, len(p.Questions))
			for _, one := range p.Questions {
				parts = append(parts, one.Question)
			}
			question = strings.Join(parts, " / ")
		}
		fmt.Fprintf(&b, "\n- %s [%s] %s", q.ID, q.State, question)
		if p != nil && p.Blocks != "" && q.State == StateOpen {
			fmt.Fprintf(&b, "\n  Waiting for it: %s", p.Blocks)
		}
		switch q.State {
		case StateOpen:
			open++
		case StateAnswered:
			if p != nil {
				a := ReadAnswer(p, q.Answer)
				for _, one := range p.Questions {
					fmt.Fprintf(&b, "\n  Answer: %s", readable(&one, a.Answers[one.ID]))
				}
				if a.GeneralNote != nil {
					fmt.Fprintf(&b, "\n  Note: %s", *a.GeneralNote)
				}
				if a.FollowUp != nil {
					fmt.Fprintf(&b, "\n  The person asks back: %s", *a.FollowUp)
				}
			}
		case StateWithdrawn:
			if q.Reason != "" {
				fmt.Fprintf(&b, "\n  Withdrawn because: %s", q.Reason)
			}
		}
	}
	if open > 0 {
		fmt.Fprintf(&b, "\n\n%d open. Their answers arrive by themselves as messages: do not call %s again to wait. "+
			"Go on with the work that does not depend on them, or end your turn when only such work is left.", open, CheckName)
	}
	return b.String()
}

// WithdrawSchema is withdraw_question's input schema, and CheckSchema
// check_questions'.
var (
	WithdrawSchema = json.RawMessage(`{"type":"object","properties":{` +
		`"question_id":{"type":"string","description":"The question's id, as ask_user or check_questions named it (ask_…)."},` +
		`"reason":{"type":"string","description":"Why it no longer matters, in one sentence; the person sees it."}},` +
		`"required":["question_id","reason"]}`)
	CheckSchema = json.RawMessage(`{"type":"object","properties":{}}`)
)

// WithdrawArgs reads withdraw_question's arguments: the question's id and the
// reason, or the text that tells the model what is wrong.
func WithdrawArgs(raw json.RawMessage) (id, reason, problem string) {
	var args struct {
		ID     any `json:"question_id"`
		Alt    any `json:"id"`
		Reason any `json:"reason"`
	}
	if json.Unmarshal(raw, &args) != nil {
		return "", "", "The arguments are not a JSON object. Call " + WithdrawName + " with question_id and reason."
	}
	if args.ID == nil {
		args.ID = args.Alt
	}
	id, _ = args.ID.(string)
	reason, _ = args.Reason.(string)
	id, reason = strings.TrimSpace(id), strings.TrimSpace(reason)
	if id == "" {
		return "", "", "question_id is missing: give the id of the question (ask_…), as " + Name + " or " + CheckName + " named it."
	}
	return id, reason, ""
}

// WithdrawnText is withdraw_question's answer for a question it took back.
func WithdrawnText(id string) string {
	return "Question " + id + " is withdrawn: the person can no longer answer it."
}

// NotWithdrawnText is withdraw_question's answer for an id that is not an
// open question of this chat.
func NotWithdrawnText(id string) string {
	return "Question " + id + " is not an open question of this chat (it was answered, withdrawn or never asked). " +
		"Call " + CheckName + " to see your questions."
}

// Tools is check_questions and withdraw_question as tools/list describes
// them.
func Tools() []map[string]any {
	return []map[string]any{
		{"name": CheckName, "title": "Check your questions", "description": CheckDescription, "inputSchema": CheckSchema},
		{"name": WithdrawName, "title": "Withdraw a question", "description": WithdrawDescription, "inputSchema": WithdrawSchema},
	}
}
