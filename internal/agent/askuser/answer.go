package askuser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Answer is what the model gets back (REBUILD.md §5.2): one entry per
// question, a note for all of them, and a question the person asks back
// instead of answering.
type Answer struct {
	Answers     map[string]QuestionAnswer `json:"answers"`
	GeneralNote *string                   `json:"general_note"`
	FollowUp    *string                   `json:"follow_up"`
}

// QuestionAnswer is the answer to one question. Selected is option ids
// (single, multi) or "yes"/"no" (confirm); Other is the person's own words;
// Value is the number of a number question and the boolean of a confirm one.
type QuestionAnswer struct {
	Selected  []string        `json:"selected"`
	Other     *string         `json:"other"`
	Note      *string         `json:"note"`
	Delegated bool            `json:"delegated"`
	Skipped   bool            `json:"skipped"`
	Value     json.RawMessage `json:"value,omitempty"`
}

// answered says whether a question got an answer of any kind.
func (a QuestionAnswer) answered() bool {
	return a.Delegated || len(a.Selected) > 0 || a.Other != nil || len(a.Value) > 0
}

// rawAnswer is an answer as a browser may send it: loosely typed.
type rawAnswer struct {
	Selected  any `json:"selected"`
	Other     any `json:"other"`
	Note      any `json:"note"`
	Delegated any `json:"delegated"`
	Skipped   any `json:"skipped"`
	Value     any `json:"value"`
}

// ReadAnswer is the person's answer to p in the normalised shape: every
// question has an entry (an unanswered one is skipped), selections name
// only the question's options, a confirm answer is "yes" or "no" with its
// boolean value, a number answer carries its value, and empty strings are
// null. A bare JSON string is taken as the general note. It never fails:
// whatever cannot be read is left out, because the answer has already been
// given and recorded.
func ReadAnswer(p *Payload, raw json.RawMessage) Answer {
	out := Answer{Answers: map[string]QuestionAnswer{}}
	var doc struct {
		Answers     map[string]json.RawMessage `json:"answers"`
		GeneralNote any                        `json:"general_note"`
		FollowUp    any                        `json:"follow_up"`
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		out.GeneralNote = optional(s)
	} else if json.Unmarshal(raw, &doc) == nil {
		out.GeneralNote, out.FollowUp = optionalAny(doc.GeneralNote), optionalAny(doc.FollowUp)
	}
	if p == nil {
		return out
	}
	for i := range p.Questions {
		q := &p.Questions[i]
		var ra rawAnswer
		if v, ok := doc.Answers[q.ID]; ok && json.Unmarshal(v, &ra) != nil {
			ra = bareAnswer(q, v)
		}
		out.Answers[q.ID] = readQuestion(q, ra)
	}
	return out
}

// bareAnswer reads an answer given as a plain value rather than an object:
// the option ids of a choice (or the person's own words when they name
// none), the yes or no of a confirm, the number, the text.
func bareAnswer(q *Question, raw json.RawMessage) rawAnswer {
	var v any
	_ = json.Unmarshal(raw, &v)
	switch q.Kind {
	case KindSingle, KindMulti:
		for _, s := range listOf(v) {
			if q.option(s) != nil {
				return rawAnswer{Selected: v}
			}
		}
		return rawAnswer{Other: v}
	case KindConfirm, KindNumber:
		return rawAnswer{Value: v}
	}
	return rawAnswer{Other: v}
}

func readQuestion(q *Question, ra rawAnswer) QuestionAnswer {
	a := QuestionAnswer{Selected: []string{}, Note: optionalAny(ra.Note), Delegated: truthy(ra.Delegated)}
	if a.Delegated {
		a.Skipped = false
		return a
	}
	a.Other = optionalAny(ra.Other)
	switch q.Kind {
	case KindSingle, KindMulti:
		for _, s := range listOf(ra.Selected) {
			if o := q.option(s); o != nil && !contains(a.Selected, o.ID) {
				a.Selected = append(a.Selected, o.ID)
			}
		}
		if q.Kind == KindSingle && len(a.Selected) > 1 {
			a.Selected = a.Selected[:1]
		}
	case KindConfirm:
		yes, ok := confirmOf(ra)
		if ok {
			a.Selected = []string{"no"}
			if yes {
				a.Selected = []string{"yes"}
			}
			a.Value = jsonOf(yes)
		}
	case KindNumber:
		if n, ok := numberOf(ra.Value); ok {
			a.Value = jsonOf(n)
		} else if a.Other != nil {
			if n, ok := numberOf(*a.Other); ok {
				a.Value = jsonOf(n)
			}
		}
		if len(a.Value) > 0 {
			// The number is the answer; Other only repeated it.
			if a.Other != nil {
				if n, ok := numberOf(*a.Other); ok && string(jsonOf(n)) == string(a.Value) {
					a.Other = nil
				}
			}
		}
	}
	a.Skipped = !a.answered()
	return a
}

// confirmOf reads a yes or no from value or selected.
func confirmOf(ra rawAnswer) (bool, bool) {
	if b, ok := boolOf(ra.Value); ok {
		return b, true
	}
	if b, ok := boolOf(ra.Selected); ok {
		return b, true
	}
	for _, s := range listOf(ra.Selected) {
		if b, ok := boolOf(s); ok {
			return b, true
		}
	}
	return false, false
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// listOf is a selection as strings: a list, or a single value.
func listOf(v any) []string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func truthy(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true" || b == "yes"
	}
	return false
}

func optional(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}

func optionalAny(v any) *string {
	switch s := v.(type) {
	case string:
		return optional(s)
	case float64:
		return optional(strconv.FormatFloat(s, 'f', -1, 64))
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// JSON is the answer as the model gets it.
func (a Answer) JSON() []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(a)
	return bytes.TrimSpace(b.Bytes())
}

// ToolResult is the text ask_user returns to the model: the answer as JSON,
// and a line on what to do with what it holds besides answers.
func ToolResult(p *Payload, raw json.RawMessage) string {
	a := ReadAnswer(p, raw)
	var b strings.Builder
	b.Write(a.JSON())
	for _, hint := range hints(a) {
		b.WriteString("\n")
		b.WriteString(hint)
	}
	return b.String()
}

// hints are the sentences that tell the model how to read the parts of an
// answer that are not answers.
func hints(a Answer) []string {
	var out []string
	delegated, skipped := false, false
	for _, qa := range a.Answers {
		delegated = delegated || qa.Delegated
		skipped = skipped || qa.Skipped
	}
	if a.FollowUp != nil {
		out = append(out, "The person asked a question back (follow_up): answer it first, then ask again with "+Name+" what is still open.")
	}
	if delegated {
		out = append(out, "The questions marked delegated are left to you: decide them yourself and say what you chose.")
	}
	if skipped && a.FollowUp == nil {
		out = append(out, "The questions marked skipped were not answered: go on without them, or ask again only if you cannot.")
	}
	return out
}

// MessageText is the user message an answer becomes when the call that
// asked has returned or is gone (AGENT-HOST.md §4.4): what was asked and what
// the person answered, readable, and then the answer exactly as ask_user
// would have returned it. A plan card's answer reads as the decision; a
// payload that is neither is passed on as it came.
func MessageText(payload, raw json.RawMessage) string {
	var plan struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(payload, &plan) == nil && plan.Kind == "plan" {
		return planText(raw)
	}
	p := Decode(payload)
	if p == nil {
		var b strings.Builder
		b.WriteString("Answer to your earlier question:\n")
		var s string
		if json.Unmarshal(raw, &s) == nil {
			b.WriteString(s)
		} else {
			b.Write(raw)
		}
		return b.String()
	}
	a := ReadAnswer(p, raw)
	var b strings.Builder
	b.WriteString("Answer to your earlier question")
	if p.Title != "" {
		fmt.Fprintf(&b, " (%s)", p.Title)
	}
	b.WriteString(":\n")
	for i, q := range p.Questions {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, q.Question, readable(&q, a.Answers[q.ID]))
	}
	if a.GeneralNote != nil {
		fmt.Fprintf(&b, "\nNote from the person: %s\n", *a.GeneralNote)
	}
	if a.FollowUp != nil {
		fmt.Fprintf(&b, "\nThe person asks back: %s\n", *a.FollowUp)
	}
	for _, hint := range hints(a) {
		b.WriteString("\n")
		b.WriteString(hint)
	}
	b.WriteString("\n\nThe answer as " + Name + " returns it:\n")
	b.Write(a.JSON())
	return b.String()
}

// readable is one answer in words.
func readable(q *Question, a QuestionAnswer) string {
	var parts []string
	switch {
	case a.Delegated:
		parts = append(parts, "You decide.")
	case a.Skipped:
		parts = append(parts, "(not answered)")
	default:
		var picked []string
		for _, id := range a.Selected {
			if o := q.option(id); o != nil {
				picked = append(picked, fmt.Sprintf("%s [%s]", o.Label, o.ID))
			} else {
				picked = append(picked, id)
			}
		}
		if q.Kind == KindNumber && len(a.Value) > 0 {
			picked = append(picked, string(a.Value))
		}
		if len(picked) > 0 {
			parts = append(parts, strings.Join(picked, ", "))
		}
		if a.Other != nil {
			if q.Kind == KindText {
				parts = append(parts, *a.Other)
			} else {
				parts = append(parts, "In their own words: "+*a.Other)
			}
		}
	}
	if a.Note != nil {
		parts = append(parts, "Note: "+*a.Note)
	}
	return strings.Join(parts, " ")
}

// planText is a plan card's answer in words.
func planText(raw json.RawMessage) string {
	var s string
	var body struct {
		Decision string `json:"decision"`
		Approve  *bool  `json:"approve"`
		Note     string `json:"note"`
	}
	if json.Unmarshal(raw, &s) == nil {
		body.Decision = s
	} else {
		_ = json.Unmarshal(raw, &body)
	}
	approve := body.Decision == "approve" || body.Approve != nil && *body.Approve
	note := strings.TrimSpace(body.Note)
	if approve {
		text := "Answer to your plan: approved. Go ahead with it."
		if note != "" {
			text += "\nNote: " + note
		}
		return text
	}
	text := "Answer to your plan: keep planning; do not start on it yet."
	if note != "" {
		text += "\n" + note
	}
	return text
}
