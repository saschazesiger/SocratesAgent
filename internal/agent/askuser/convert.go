package askuser

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// The agents' own question mechanisms, converted into an ask_user payload so
// that the person sees one kind of question (REBUILD.md §5.3), and the answer
// converted back into what each mechanism expects. Options keep their label
// as their id, because every one of these mechanisms answers with labels.
// A converted payload keeps every question and choice its mechanism sent:
// the limits of a call (MaxQuestions, MaxOptions) are what the model is held
// to, and a question cut short could not be answered the way its mechanism
// requires (a required field, a value beyond the twelfth).

// delegatedText is what a delegated question becomes where an answer can
// only be text.
const delegatedText = "You decide: the person leaves this to you."

// labels is one question's answer as the texts a label-based mechanism
// takes: the chosen labels, the person's own words, a delegation, a note.
func labels(q *Question, a QuestionAnswer) []string {
	var out []string
	if a.Delegated {
		out = append(out, delegatedText)
	}
	for _, id := range a.Selected {
		if o := q.option(id); o != nil {
			out = append(out, o.Label)
		} else {
			out = append(out, id)
		}
	}
	if q.Kind == KindNumber && len(a.Value) > 0 {
		out = append(out, string(a.Value))
	}
	if a.Other != nil {
		out = append(out, *a.Other)
	}
	if a.Note != nil {
		out = append(out, "Note: "+*a.Note)
	}
	return out
}

// extras is what an answer says besides the answers, as texts.
func extras(a Answer) []string {
	var out []string
	if a.GeneralNote != nil {
		out = append(out, "Note from the person: "+*a.GeneralNote)
	}
	if a.FollowUp != nil {
		out = append(out, "The person asks back: "+*a.FollowUp)
	}
	return out
}

// labelOptions makes options of labels and descriptions, the label as id.
func labelOptions(list []labelled) []Option {
	var out []Option
	seen := map[string]bool{}
	for _, l := range list {
		label := strings.TrimSpace(l.Label)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, Option{ID: label, Label: label, Description: strings.TrimSpace(l.Description),
			Recommended: strings.Contains(strings.ToLower(label), "(recommended)")})
	}
	return out
}

type labelled struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// finish fills a converted question the way Parse fills an ask_user one.
func finish(q *Question, n int) {
	q.Question = strings.TrimSpace(q.Question)
	q.Header = strings.TrimSpace(q.Header)
	if q.ID == "" {
		q.ID = fmt.Sprintf("q%d", n)
	}
	if q.Question == "" {
		q.Question = q.Header
	}
	if q.Header == "" {
		q.Header = q.Question
	}
	q.Header = clip(q.Header, MaxHeader)
	if q.Kind == "" {
		q.Kind = KindText
		if len(q.Options) > 0 {
			q.Kind = KindSingle
		}
	}
}

// ------------------------------------------------- Codex request_user_input

// FromUserInput converts Codex's item/tool/requestUserInput (Plan mode's
// request_user_input: up to three questions of up to three options, each
// with an id, a short header and an "Other" flag). It reports false when
// there is nothing to ask.
func FromUserInput(params json.RawMessage) (Payload, bool) {
	var in struct {
		Questions []struct {
			ID       string     `json:"id"`
			Header   string     `json:"header"`
			Question string     `json:"question"`
			IsOther  bool       `json:"isOther"`
			IsSecret bool       `json:"isSecret"`
			Options  []labelled `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(params, &in) != nil || len(in.Questions) == 0 {
		return Payload{}, false
	}
	p := Payload{Source: SourceUserInput}
	for i, rq := range in.Questions {
		q := Question{ID: strings.TrimSpace(rq.ID), Header: rq.Header, Question: rq.Question,
			Options: labelOptions(rq.Options), AllowOther: rq.IsOther || len(rq.Options) == 0,
			AllowDelegate: true, Required: true, Secret: rq.IsSecret}
		finish(&q, i+1)
		p.Questions = append(p.Questions, q)
	}
	return p, true
}

// UserInputResult is the answer to a request_user_input, as codex takes it:
// {"answers": {<id>: {"answers": [<texts>]}}}. What the answer says besides
// the answers goes with the last question.
func UserInputResult(p *Payload, raw json.RawMessage) map[string]any {
	a := ReadAnswer(p, raw)
	answers := map[string]any{}
	for i := range p.Questions {
		q := &p.Questions[i]
		texts := labels(q, a.Answers[q.ID])
		if i == len(p.Questions)-1 {
			texts = append(texts, extras(a)...)
		}
		if texts == nil {
			texts = []string{}
		}
		answers[q.ID] = map[string]any{"answers": texts}
	}
	return map[string]any{"answers": answers}
}

// ------------------------------------------------------ Codex async questions

// FromAsync converts the questions of a Codex agent message delivered
// "async" (request_user_input_async, offered to the code-mode models): each a
// title with optional option labels. The answer goes to the agent as the next
// user message.
func FromAsync(questions json.RawMessage) (Payload, bool) {
	var in []struct {
		Title    string   `json:"title"`
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if json.Unmarshal(questions, &in) != nil || len(in) == 0 {
		return Payload{}, false
	}
	p := Payload{Source: SourceAsync}
	for i, rq := range in {
		var opts []labelled
		for _, o := range rq.Options {
			opts = append(opts, labelled{Label: o})
		}
		text := rq.Title
		if text == "" {
			text = rq.Question
		}
		q := Question{Question: text, Options: labelOptions(opts), AllowOther: true, AllowDelegate: true, Required: true}
		finish(&q, i+1)
		p.Questions = append(p.Questions, q)
	}
	return p, true
}

// ------------------------------------------------------------ MCP elicitation

// elicitField is one property of an elicitation's form (MCP 2025-11-25
// ElicitRequestFormParams: flat, primitive properties).
type elicitField struct {
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Enum        []string   `json:"enum"`
	EnumNames   []string   `json:"enumNames"`
	OneOf       []constOpt `json:"oneOf"`
	Items       *struct {
		Enum  []string   `json:"enum"`
		AnyOf []constOpt `json:"anyOf"`
	} `json:"items"`
	Minimum  *float64        `json:"minimum"`
	Maximum  *float64        `json:"maximum"`
	MinItems *float64        `json:"minItems"`
	MaxItems *float64        `json:"maxItems"`
	Default  json.RawMessage `json:"default"`
}

type constOpt struct {
	Const string `json:"const"`
	Title string `json:"title"`
}

type elicitForm struct {
	Properties map[string]elicitField `json:"properties"`
	Required   []string               `json:"required"`
	// Order is the properties' order where the sender gives one (Claude
	// Code's property_order); otherwise they are sorted by name.
	Order []string `json:"-"`
}

func readForm(schema json.RawMessage, order []string) (elicitForm, bool) {
	var f elicitForm
	if len(schema) > 0 && json.Unmarshal(schema, &f) != nil {
		return f, false
	}
	seen := map[string]bool{}
	for _, name := range order {
		if _, ok := f.Properties[name]; ok && !seen[name] {
			f.Order, seen[name] = append(f.Order, name), true
		}
	}
	var rest []string
	for name := range f.Properties {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	f.Order = append(f.Order, rest...)
	return f, true
}

// confirmID is the one question of an elicitation without fields: whether to
// go ahead at all.
const confirmID = "confirm"

// FromElicitation converts an MCP elicitation form (a message and a flat
// JSON Schema of primitive fields) into one question per field; a form with
// no fields is one yes-or-no question, accept or decline. The answer has to
// be a value the server can check, so delegating is not offered and a field
// with choices takes only them. It reports false for a schema it cannot
// read.
func FromElicitation(server, message string, schema json.RawMessage, order []string) (Payload, bool) {
	f, ok := readForm(schema, order)
	if !ok {
		return Payload{}, false
	}
	required := map[string]bool{}
	for _, r := range f.Required {
		required[r] = true
	}
	p := Payload{Title: strings.TrimSpace(message), Source: SourceElicitation}
	if server != "" {
		p.Context = "Asked by the MCP server " + server + "."
	}
	if len(f.Order) == 0 {
		q := Question{ID: confirmID, Header: "Go ahead?", Question: p.Title, Kind: KindConfirm, Required: true}
		finish(&q, 1)
		p.Questions = append(p.Questions, q)
		return p, true
	}
	for i, name := range f.Order {
		fd := f.Properties[name]
		q := Question{ID: name, Header: fd.Title, Question: fd.Description, Required: required[name]}
		if q.Header == "" {
			q.Header = name
		}
		if q.Question == "" {
			q.Question = q.Header
		}
		switch {
		case fd.Type == "boolean":
			q.Kind = KindConfirm
		case fd.Type == "number" || fd.Type == "integer":
			q.Kind, q.Min, q.Max = KindNumber, fd.Minimum, fd.Maximum
		case fd.Type == "array" && fd.Items != nil:
			q.Kind, q.Min, q.Max = KindMulti, fd.MinItems, fd.MaxItems
			q.Options = constOptions(fd.Items.Enum, nil, fd.Items.AnyOf)
		case len(fd.Enum) > 0 || len(fd.OneOf) > 0:
			q.Kind = KindSingle
			q.Options = constOptions(fd.Enum, fd.EnumNames, fd.OneOf)
		default:
			q.Kind, q.AllowOther = KindText, true
		}
		if len(fd.Default) > 0 && string(fd.Default) != "null" {
			q.Default = fd.Default
		}
		finish(&q, i+1)
		p.Questions = append(p.Questions, q)
	}
	return p, true
}

// constOptions are a field's choices: the value is the id, its title the
// label.
func constOptions(enum, names []string, titled []constOpt) []Option {
	var out []Option
	for _, c := range titled {
		label := c.Title
		if label == "" {
			label = c.Const
		}
		out = append(out, Option{ID: c.Const, Label: label})
	}
	for i, v := range enum {
		label := v
		if i < len(names) && names[i] != "" {
			label = names[i]
		}
		out = append(out, Option{ID: v, Label: label})
	}
	return out
}

// ElicitationResult is the answer to an elicitation: "accept" with a value
// for every field the person answered, typed as the form says, or "decline"
// when they answered none (for a form without fields: yes or no). A form has
// no place for the person's own words; SaysMore tells when they wrote some,
// which then reach the agent as a message.
func ElicitationResult(p *Payload, schema json.RawMessage, raw json.RawMessage) (string, map[string]any) {
	f, _ := readForm(schema, nil)
	a := ReadAnswer(p, raw)
	if len(f.Properties) == 0 {
		if qa := a.Answers[confirmID]; string(qa.Value) == "true" {
			return "accept", map[string]any{}
		}
		return "decline", nil
	}
	content := map[string]any{}
	for i := range p.Questions {
		q := &p.Questions[i]
		qa := a.Answers[q.ID]
		fd := f.Properties[q.ID]
		switch q.Kind {
		case KindConfirm:
			var b bool
			if json.Unmarshal(qa.Value, &b) == nil && len(qa.Value) > 0 {
				content[q.ID] = b
			}
		case KindNumber:
			var n float64
			if json.Unmarshal(qa.Value, &n) == nil && len(qa.Value) > 0 {
				if fd.Type == "integer" {
					content[q.ID] = int64(math.Round(n))
				} else {
					content[q.ID] = n
				}
			}
		case KindMulti:
			if len(qa.Selected) > 0 {
				content[q.ID] = qa.Selected
			}
		case KindSingle:
			if len(qa.Selected) > 0 {
				content[q.ID] = qa.Selected[0]
			}
		default:
			if qa.Other != nil {
				content[q.ID] = *qa.Other
			}
		}
	}
	if len(content) == 0 {
		return "decline", nil
	}
	return "accept", content
}

// SaysMore reports whether an answer says more than the mechanism its payload
// was converted from can carry back. An MCP elicitation's form takes only
// its fields' values, so a note, a general note, a question back, a choice
// in the person's own words or a "You decide" would be lost in it: the form
// gets what it can take, and the whole answer goes to the agent as a message
// as well (AGENT-HOST.md §4.4). Every other mechanism carries all of it.
func SaysMore(payload, raw json.RawMessage) bool {
	p := Decode(payload)
	if p == nil || p.Source != SourceElicitation {
		return false
	}
	a := ReadAnswer(p, raw)
	if a.GeneralNote != nil || a.FollowUp != nil {
		return true
	}
	for i := range p.Questions {
		q := &p.Questions[i]
		qa := a.Answers[q.ID]
		if qa.Note != nil || qa.Delegated || qa.Other != nil && q.Kind != KindText {
			return true
		}
	}
	return false
}

// ------------------------------------------------------ OpenCode question tool

// FromOpenCode converts the questions of OpenCode's own question tool
// (question.asked): a question, a header of up to 30 characters, labelled
// options, multiple choice or not, and free text allowed unless custom is
// false.
func FromOpenCode(questions json.RawMessage) (Payload, bool) {
	var in []struct {
		Question string     `json:"question"`
		Header   string     `json:"header"`
		Options  []labelled `json:"options"`
		Multiple bool       `json:"multiple"`
		Custom   *bool      `json:"custom"`
	}
	if json.Unmarshal(questions, &in) != nil || len(in) == 0 {
		return Payload{}, false
	}
	p := Payload{Source: SourceQuestion}
	for i, rq := range in {
		q := Question{Header: rq.Header, Question: rq.Question, Options: labelOptions(rq.Options),
			AllowOther: rq.Custom == nil || *rq.Custom, AllowDelegate: true, Required: true}
		if rq.Multiple && len(q.Options) > 0 {
			q.Kind = KindMulti
		}
		finish(&q, i+1)
		p.Questions = append(p.Questions, q)
	}
	return p, true
}

// OpenCodeAnswers is the answer to OpenCode's question tool: one list of
// texts per question, in order, which OpenCode takes as the chosen labels
// (free text included). What the answer says besides the answers goes with
// the last question.
func OpenCodeAnswers(p *Payload, raw json.RawMessage) [][]string {
	a := ReadAnswer(p, raw)
	out := make([][]string, 0, len(p.Questions))
	for i := range p.Questions {
		q := &p.Questions[i]
		texts := labels(q, a.Answers[q.ID])
		if i == len(p.Questions)-1 {
			texts = append(texts, extras(a)...)
		}
		if texts == nil {
			texts = []string{}
		}
		out = append(out, texts)
	}
	return out
}
