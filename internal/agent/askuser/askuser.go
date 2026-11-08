// Package askuser is Socrates' own question tool, ask_user, defined once for
// every agent (REBUILD.md §5): the JSON Schema the model fills in, the check
// and normalisation of a call, the answer the model gets back, the text an
// answer becomes when it has to arrive as a user message, and the converters
// from the agents' own question mechanisms (Codex's request_user_input, async
// questions and MCP elicitations, OpenCode's question tool) into the same
// payload and back.
//
// It depends on nothing else in Socrates: the adapters carry the calls (an
// in-process MCP server for Claude Code, a dynamic tool for Codex, the
// `socrates mcp-ask` MCP server for OpenCode) and the agent package renders
// answers with it.
package askuser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Name is the tool's name. The agents prefix it with the server's name when
// it comes over MCP: mcp__socrates__ask_user in Claude Code,
// socrates_ask_user in OpenCode.
const Name = "ask_user"

// ServerName is the MCP server ask_user is served by.
const ServerName = "socrates"

// The limits of a call (REBUILD.md §5.1).
const (
	MaxQuestions = 20
	MaxOptions   = 12
	MaxHeader    = 24
	MaxSteps     = 20
	MaxSections  = 10
)

// Section tones: how a section of the agent's own text stands out.
const (
	ToneNote    = "note"
	ToneInfo    = "info"
	ToneWarning = "warning"
	ToneSuccess = "success"
)

// Question kinds.
const (
	KindSingle  = "single"
	KindMulti   = "multi"
	KindText    = "text"
	KindConfirm = "confirm"
	KindNumber  = "number"
)

// Sources of a payload that did not come from ask_user itself: the question
// mechanisms of the agents, converted into the same shape.
const (
	SourceUserInput   = "request_user_input"
	SourceAsync       = "async_question"
	SourceElicitation = "elicitation"
	SourceQuestion    = "question"
)

// Payload is an ask_user call as the person sees it: the ask event's payload
// (AGENT-HOST.md §2). Every field is normalised by Parse.
type Payload struct {
	Title   string `json:"title,omitempty"`
	Context string `json:"context,omitempty"`
	// Steps is what the person has to do before they can answer, in order
	// (a command to run, a login, a check on a device), and Sections the
	// agent's further texts, each under a title of its own.
	Steps     []string   `json:"steps,omitempty"`
	Sections  []Section  `json:"sections,omitempty"`
	Questions []Question `json:"questions"`
	// PartID is the tool part the call belongs to, when the adapter knows
	// it, so that a chat view can draw the two together.
	PartID string `json:"part_id,omitempty"`
	// Source is set when the questions came from one of the agent's own
	// mechanisms rather than ask_user (the Source* constants).
	Source string `json:"source,omitempty"`
}

// Question is one question of a call.
type Question struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
	// Steps and Sections are a question's own, as the payload's are the
	// batch's.
	Steps    []string  `json:"steps,omitempty"`
	Sections []Section `json:"sections,omitempty"`
	Kind     string    `json:"kind"`
	Options  []Option  `json:"options,omitempty"`
	// Min and Max bound how many options a multi question takes, or the
	// value of a number question.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// AllowOther lets the person answer in their own words, AllowDelegate
	// leave the question to the agent ("You decide").
	AllowOther    bool `json:"allow_other"`
	AllowDelegate bool `json:"allow_delegate"`
	// Default is what is preselected: an option id (single), a list of them
	// (multi), a boolean (confirm), a number (number) or a text (text).
	Default  json.RawMessage `json:"default,omitempty"`
	Required bool            `json:"required"`
	// Secret marks an answer the agent treats as a secret (Codex's
	// isSecret); a view does not echo it.
	Secret bool `json:"secret,omitempty"`
}

// Section is a text of the agent's own beside the questions: what it tried,
// a log, the risks, a summary of its changes. Open shows it unfolded; a
// warning always is.
type Section struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tone  string `json:"tone,omitempty"`
	Open  bool   `json:"open,omitempty"`
}

// Option is one choice of a single or multi question.
type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Details     string `json:"details,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// Description is what the model reads about the tool. It carries the rules
// that matter most, because a tool description reaches the model in every
// agent, a system prompt not always.
const Description = "Ask the person one or more questions and wait for the answers. " +
	"Use this tool whenever you need a decision, a piece of information or an action from the person " +
	"(a login, a command only they can run, a check on their device, an approval), and whenever you are unsure " +
	"how to go on and a wrong guess would cost work; never ask in plain text and never use another question tool: " +
	"a reply that would end with a question to the person is this call instead. " +
	"The person often answers from a notification or a list of questions without the chat in front of them, " +
	"so every call must stand on its own: put everything they need to answer into the call itself " +
	"(what you found, file paths, commands, errors, the options with their trade-offs, your recommendation) " +
	"and never refer to earlier messages (\"as above\", \"the error I mentioned\"). " +
	"context is the background for all questions; steps is what the person has to do before they can answer, in order " +
	"(each step with the exact command, page or setting; answering is not a step); sections are further texts of your own, each under its own " +
	"title (what you tried, a log, risks; tone warning for what they must not miss); each question has its own " +
	"context, steps and sections too. " +
	"Put everything you need to know into one call (up to 20 questions). " +
	"The call blocks until the person answers, which can take minutes or hours; then continue from the answers. " +
	"The person may skip a question, leave it to you (delegated: decide yourself and say what you chose), " +
	"add a note, or ask a question back (follow_up: answer it first)."

// Guidance is the system-prompt text that steers the model to ask_user. tool
// is the name the model knows the tool by in that agent.
func Guidance(tool string) string {
	return "When you need a decision, any information or an action from the person, call the " + tool + " tool: " +
		"also when you are unsure how to go on and a wrong guess would cost work, and when only the person can do " +
		"something (log in, run a command on their machine, test on a device, approve a change). " +
		"Never ask questions in plain text, and never use another question tool. " +
		"This holds at the end of your work too: when you stop because something needs the person's decision or answer, " +
		"the last thing you do is call " + tool + "; a reply that ends with a question or a choice for the person " +
		"(\"Should I …?\", \"Which do you want?\") is wrong. " +
		"Each call must stand on its own: the person may answer it from a notification without reading the chat, " +
		"so put all the context the questions need into the call (its context, steps and sections, and each question's " +
		"context and options), never \"see above\". " +
		"What the person has to do before they can answer goes into steps, in order and exact (answering is not a step). " +
		"Ask everything you need in one call, with the context and the options that make each question " +
		"easy to answer, and mark the option you recommend. " +
		"The person always answers, possibly much later: wait for the answer and continue from it; " +
		"do not stop the work to ask in a message instead."
}

// schema is the input schema of the tool (REBUILD.md §5.1). The steps and
// sections of a call and of a question are the same (stepsSchema,
// sectionsSchema).
var schema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "What this batch of questions is about, in a few words."},
    "context": {"type": "string", "description": "Markdown background for all questions, complete enough to answer without the chat: what you found, what is at stake, the paths, commands and errors involved."},
    "steps": ` + stepsSchema + `,
    "sections": ` + sectionsSchema + `,
    "questions": {
      "type": "array", "minItems": 1, "maxItems": 20,
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "A stable id; the answer is keyed by it."},
          "header": {"type": "string", "maxLength": 24, "description": "A short label shown as a chip, at most 24 characters."},
          "question": {"type": "string", "description": "The question, as one sentence."},
          "context": {"type": "string", "description": "Markdown: why it matters, trade-offs, code, links."},
          "steps": ` + stepsSchema + `,
          "sections": ` + sectionsSchema + `,
          "kind": {"type": "string", "enum": ["single", "multi", "text", "confirm", "number"],
            "description": "single: pick one option; multi: pick several; text: a free answer; confirm: yes or no; number: a number."},
          "options": {
            "type": "array", "maxItems": 12, "description": "The choices of a single or multi question.",
            "items": {
              "type": "object",
              "properties": {
                "id": {"type": "string", "description": "A stable id; the answer names the chosen ids."},
                "label": {"type": "string", "description": "A few words."},
                "description": {"type": "string", "description": "One line."},
                "details": {"type": "string", "description": "Markdown shown when the option is opened: snippets, diffs, pros and cons."},
                "recommended": {"type": "boolean", "description": "The option you recommend."}
              },
              "required": ["id", "label"]
            }
          },
          "min": {"type": "number", "description": "multi: the fewest options to pick; number: the smallest value."},
          "max": {"type": "number", "description": "multi: the most options to pick; number: the largest value."},
          "allow_other": {"type": "boolean", "description": "Whether the person may answer in their own words instead. Default true."},
          "allow_delegate": {"type": "boolean", "description": "Whether the person may answer \"You decide\". Default true."},
          "default": {
            "description": "Preselected: an option id (single), a list of option ids (multi), true or false (confirm), a number (number), a text (text).",
            "anyOf": [{"type": "string"}, {"type": "number"}, {"type": "boolean"}, {"type": "array", "items": {"type": "string"}}]
          },
          "required": {"type": "boolean", "description": "Whether you cannot go on without this answer. Default true."}
        },
        "required": ["id", "question"]
      }
    }
  },
  "required": ["questions"]
}`

// stepsSchema is the steps of a call or a question.
const stepsSchema = `{
  "type": "array", "maxItems": 20, "items": {"type": "string"},
  "description": "What the person has to do before they can answer, in order, one Markdown step each: the exact command, page, setting or device. Shown as a numbered list above the questions. Answering is not a step."
}`

// sectionsSchema is the sections of a call or a question.
const sectionsSchema = `{
  "type": "array", "maxItems": 10,
  "description": "Further texts of your own, each under its own title: what you already tried, a log, the risks, a summary of the changes, links.",
  "items": {
    "type": "object",
    "properties": {
      "title": {"type": "string", "description": "The section's heading, a few words."},
      "body": {"type": "string", "description": "Markdown."},
      "tone": {"type": "string", "enum": ["note", "info", "warning", "success"], "description": "note (default) and info fold away under their title; warning always shows, for what the person must not miss; success for what already works."},
      "open": {"type": "boolean", "description": "Show the section unfolded rather than under its title. Default false."}
    },
    "required": ["title", "body"]
  }
}`

// Schema is the tool's input schema, as JSON.
func Schema() json.RawMessage {
	var b bytes.Buffer
	_ = json.Compact(&b, []byte(schema))
	return b.Bytes()
}

// ErrInvalid is wrapped by every error Parse reports about a call. Its text
// is written for the model, which reads it as the tool's result and calls
// again.
var ErrInvalid = errors.New("ask_user was called with problems, so nothing was asked")

// rawQuestion is a question as the model may write it: loosely typed, so
// that a call with a small mistake gets a precise error instead of a decode
// failure.
type rawQuestion struct {
	ID            any             `json:"id"`
	Header        any             `json:"header"`
	Question      any             `json:"question"`
	Context       any             `json:"context"`
	Steps         json.RawMessage `json:"steps"`
	Sections      json.RawMessage `json:"sections"`
	Kind          any             `json:"kind"`
	Options       json.RawMessage `json:"options"`
	Min           any             `json:"min"`
	Max           any             `json:"max"`
	AllowOther    any             `json:"allow_other"`
	AllowDelegate any             `json:"allow_delegate"`
	Default       json.RawMessage `json:"default"`
	Required      any             `json:"required"`
	// AllowFreeText is what some models call allow_other.
	AllowFreeText any `json:"allow_free_text"`
}

type rawOption struct {
	ID any `json:"id"`
	// Value is what some models call the id.
	Value       any `json:"value"`
	Label       any `json:"label"`
	Description any `json:"description"`
	Details     any `json:"details"`
	Recommended any `json:"recommended"`
}

// problems collects what is wrong with a call, so that the model can fix all
// of it in one retry.
type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n- %s\nFix the call and call %s again.", ErrInvalid, strings.Join(p, "\n- "), Name)
}

// Parse checks a call's arguments and returns the normalised payload: ids
// and headers filled in where the model left them out, the defaults of
// allow_other, allow_delegate and required applied, the kind inferred from
// the options, and every field the schema does not have dropped. A call that
// cannot be asked is an error wrapping ErrInvalid that says everything that
// is wrong with it.
func Parse(args json.RawMessage) (Payload, error) {
	var raw struct {
		Title     any             `json:"title"`
		Context   any             `json:"context"`
		Steps     json.RawMessage `json:"steps"`
		Sections  json.RawMessage `json:"sections"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(args), &raw); err != nil {
		return Payload{}, fmt.Errorf("%w: the arguments are not a JSON object (%v). Fix the call and call %s again.", ErrInvalid, err, Name)
	}
	var probs problems
	p := Payload{Title: text(raw.Title, "title", &probs), Context: text(raw.Context, "context", &probs),
		Steps: parseSteps("steps", raw.Steps, &probs), Sections: parseSections("sections", raw.Sections, &probs)}
	var questions []json.RawMessage
	switch {
	case len(raw.Questions) == 0 || string(raw.Questions) == "null":
		probs.add("questions is missing: give at least one question")
	case json.Unmarshal(raw.Questions, &questions) != nil:
		probs.add("questions must be a list of question objects")
	case len(questions) == 0:
		probs.add("questions is empty: give at least one question")
	case len(questions) > MaxQuestions:
		probs.add("there are %d questions; one call takes at most %d", len(questions), MaxQuestions)
	}
	seen := map[string]bool{}
	for i, rq := range questions {
		q := parseQuestion(i, rq, &probs)
		if q.ID != "" {
			if seen[q.ID] {
				probs.add("%s: the id %q is used by another question too", where(i, q.ID), q.ID)
			}
			seen[q.ID] = true
		}
		p.Questions = append(p.Questions, q)
	}
	// A question without an id is called by its place, q<n>, unless the
	// model gave another question that id.
	for i := range p.Questions {
		if p.Questions[i].ID != "" {
			continue
		}
		id := fmt.Sprintf("q%d", i+1)
		for n := 2; seen[id]; n++ {
			id = fmt.Sprintf("q%d_%d", i+1, n)
		}
		p.Questions[i].ID, seen[id] = id, true
	}
	if err := probs.err(); err != nil {
		return Payload{}, err
	}
	return p, nil
}

func where(i int, id string) string {
	if id != "" {
		return fmt.Sprintf("question %d (%q)", i+1, id)
	}
	return fmt.Sprintf("question %d", i+1)
}

// text reads an optional string field.
func text(v any, field string, probs *problems) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	}
	probs.add("%s must be a string", field)
	return ""
}

// flag reads an optional boolean field, with its default.
func flag(v any, def bool, field string, probs *problems) bool {
	switch b := v.(type) {
	case nil:
		return def
	case bool:
		return b
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "yes":
			return true
		case "false", "no":
			return false
		}
	}
	probs.add("%s must be true or false", field)
	return def
}

// number reads an optional numeric field.
func number(v any, field string, probs *problems) *float64 {
	switch n := v.(type) {
	case nil:
		return nil
	case float64:
		return &n
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return &f
		}
	}
	probs.add("%s must be a number", field)
	return nil
}

func parseQuestion(i int, raw json.RawMessage, probs *problems) Question {
	var rq rawQuestion
	if err := json.Unmarshal(raw, &rq); err != nil {
		probs.add("question %d must be an object", i+1)
		return Question{}
	}
	q := Question{ID: text(rq.ID, fmt.Sprintf("question %d: id", i+1), probs)}
	at := where(i, q.ID)
	q.Question = text(rq.Question, at+": question", probs)
	q.Header = text(rq.Header, at+": header", probs)
	q.Context = text(rq.Context, at+": context", probs)
	q.Steps = parseSteps(at+": steps", rq.Steps, probs)
	q.Sections = parseSections(at+": sections", rq.Sections, probs)
	kind := kindOf(text(rq.Kind, at+": kind", probs))
	if rq.AllowOther == nil {
		rq.AllowOther = rq.AllowFreeText
	}
	q.AllowOther = flag(rq.AllowOther, true, at+": allow_other", probs)
	q.AllowDelegate = flag(rq.AllowDelegate, true, at+": allow_delegate", probs)
	q.Required = flag(rq.Required, true, at+": required", probs)
	if q.Question == "" {
		if q.Header == "" {
			probs.add("%s has no question text", at)
		}
		q.Question = q.Header
	}
	if q.Header == "" {
		q.Header = q.Question
	}
	q.Header = clip(q.Header, MaxHeader)

	var opts []json.RawMessage
	if len(rq.Options) > 0 && string(rq.Options) != "null" {
		if err := json.Unmarshal(rq.Options, &opts); err != nil {
			probs.add("%s: options must be a list of option objects", at)
		}
	}
	if kind == "" {
		kind = KindText
		if len(opts) > 0 {
			kind = KindSingle
		}
	}
	q.Kind = kind
	switch kind {
	case KindSingle, KindMulti:
		if len(opts) == 0 {
			probs.add("%s: kind %q needs options; use kind \"text\" for a free answer", at, kind)
		}
		if len(opts) > MaxOptions {
			probs.add("%s has %d options; a question takes at most %d", at, len(opts), MaxOptions)
		}
		q.Options = parseOptions(at, opts, probs)
	case KindText, KindConfirm, KindNumber:
		if len(opts) > 0 {
			probs.add("%s: kind %q takes no options; use kind \"single\" or \"multi\" to offer choices", at, kind)
		}
	default:
		probs.add("%s: kind %q is not one of single, multi, text, confirm, number", at, kind)
	}
	q.Min, q.Max = number(rq.Min, at+": min", probs), number(rq.Max, at+": max", probs)
	switch kind {
	case KindMulti:
		checkCount(at, &q, probs)
	case KindNumber:
		if q.Min != nil && q.Max != nil && *q.Min > *q.Max {
			probs.add("%s: min %v is more than max %v", at, *q.Min, *q.Max)
		}
	default:
		q.Min, q.Max = nil, nil
	}
	q.Default = parseDefault(q, rq.Default)
	return q
}

// parseSteps reads a list of steps: Markdown strings, or objects that say
// their text under a name a model may choose (text, title, step,
// description, details). One string alone is one step.
func parseSteps(at string, raw json.RawMessage, probs *problems) []string {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one = strings.TrimSpace(one); one != "" {
			return []string{one}
		}
		return nil
	}
	var list []any
	if json.Unmarshal(raw, &list) != nil {
		probs.add("%s must be a list of steps, each a string", at)
		return nil
	}
	if len(list) > MaxSteps {
		probs.add("%s has %d steps; at most %d", at, len(list), MaxSteps)
		return nil
	}
	var out []string
	for i, v := range list {
		var step string
		switch t := v.(type) {
		case string:
			step = strings.TrimSpace(t)
		case map[string]any:
			var lines []string
			for _, k := range []string{"title", "text", "step", "description", "details", "command"} {
				if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
					if k == "command" {
						s = "`" + strings.TrimSpace(s) + "`"
					}
					lines = append(lines, strings.TrimSpace(s))
				}
			}
			step = strings.Join(lines, "\n\n")
		}
		if step == "" {
			probs.add("%s: step %d must be a non-empty string", at, i+1)
			continue
		}
		out = append(out, step)
	}
	return out
}

// parseSections reads a list of sections. The body may come as body, text,
// content or markdown; a tone outside the four is a note.
func parseSections(at string, raw json.RawMessage, probs *problems) []Section {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return nil
	}
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		probs.add("%s must be a list of objects with a title and a body", at)
		return nil
	}
	if len(list) > MaxSections {
		probs.add("%s has %d sections; at most %d", at, len(list), MaxSections)
		return nil
	}
	var out []Section
	for i, m := range list {
		where := fmt.Sprintf("%s: section %d", at, i+1)
		sec := Section{Title: text(m["title"], where+": title", probs)}
		for _, k := range []string{"body", "text", "content", "markdown"} {
			if v, ok := m[k]; ok && sec.Body == "" {
				sec.Body = text(v, where+": "+k, probs)
			}
		}
		if sec.Body == "" {
			probs.add("%s has no body", where)
			continue
		}
		if sec.Title == "" {
			sec.Title = "Details"
		}
		switch tone := strings.ToLower(text(m["tone"], where+": tone", probs)); tone {
		case ToneInfo, ToneWarning, ToneSuccess:
			sec.Tone = tone
		case "warn", "danger", "error", "caution", "important":
			sec.Tone = ToneWarning
		default:
			sec.Tone = ToneNote
		}
		sec.Open = flag(m["open"], false, where+": open", probs)
		out = append(out, sec)
	}
	return out
}

// kindSynonyms are the names models give the kinds when they do not use the
// schema's own, so that such a call is asked rather than sent back.
var kindSynonyms = map[string]string{
	"choice": KindSingle, "select": KindSingle, "radio": KindSingle, "one": KindSingle,
	"multiple": KindMulti, "multi_select": KindMulti, "multiselect": KindMulti, "checkbox": KindMulti, "many": KindMulti,
	"free_text": KindText, "freetext": KindText, "string": KindText, "open": KindText,
	"boolean": KindConfirm, "bool": KindConfirm, "yes_no": KindConfirm, "yesno": KindConfirm,
	"integer": KindNumber, "int": KindNumber, "float": KindNumber, "numeric": KindNumber,
}

// kindOf is a kind as the model wrote it, in the schema's words.
func kindOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if k, ok := kindSynonyms[strings.ReplaceAll(s, "-", "_")]; ok {
		return k
	}
	return s
}

// checkCount keeps a multi question's min and max whole and within its
// options.
func checkCount(at string, q *Question, probs *problems) {
	whole := func(p *float64, field string) {
		if p != nil && (*p < 0 || *p != math.Trunc(*p)) {
			probs.add("%s: %s must be a whole number of options, at least 0", at, field)
		}
	}
	whole(q.Min, "min")
	whole(q.Max, "max")
	n := float64(len(q.Options))
	if q.Min != nil && q.Max != nil && *q.Min > *q.Max {
		probs.add("%s: min %v is more than max %v", at, *q.Min, *q.Max)
	}
	if q.Min != nil && *q.Min > n && !q.AllowOther {
		probs.add("%s: min %v is more than its %v options", at, *q.Min, n)
	}
	if q.Max != nil && *q.Max > n && n > 0 {
		q.Max = &n
	}
	if q.Min != nil && *q.Min == 0 {
		q.Min = nil
	}
}

func parseOptions(at string, raws []json.RawMessage, probs *problems) []Option {
	var out []Option
	seen := map[string]bool{}
	for j, raw := range raws {
		var ro rawOption
		if err := json.Unmarshal(raw, &ro); err != nil {
			// A list of plain labels is a common shorthand.
			var label string
			if json.Unmarshal(raw, &label) != nil {
				probs.add("%s: option %d must be an object with an id and a label", at, j+1)
				continue
			}
			ro.Label = label
		}
		if ro.ID == nil {
			ro.ID = ro.Value
		}
		o := Option{
			ID:          text(ro.ID, fmt.Sprintf("%s: option %d: id", at, j+1), probs),
			Label:       text(ro.Label, fmt.Sprintf("%s: option %d: label", at, j+1), probs),
			Description: text(ro.Description, fmt.Sprintf("%s: option %d: description", at, j+1), probs),
			Details:     text(ro.Details, fmt.Sprintf("%s: option %d: details", at, j+1), probs),
			Recommended: flag(ro.Recommended, false, fmt.Sprintf("%s: option %d: recommended", at, j+1), probs),
		}
		if o.Label == "" {
			if o.ID == "" {
				probs.add("%s: option %d has no label", at, j+1)
			}
			o.Label = o.ID
		}
		if o.ID == "" {
			o.ID = slug(o.Label, j+1)
		}
		if seen[o.ID] {
			probs.add("%s: the option id %q is used twice", at, o.ID)
		}
		seen[o.ID] = true
		out = append(out, o)
	}
	return out
}

// parseDefault is a default in the kind's own type. One that does not fit its
// question (an option it does not have, a number out of range) is dropped
// rather than sent back: it only preselects, and the question can be asked
// without it.
func parseDefault(q Question, raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	switch q.Kind {
	case KindSingle:
		if list, ok := v.([]any); ok && len(list) == 1 {
			v = list[0]
		}
		if s, ok := v.(string); ok {
			if o := q.option(s); o != nil {
				return jsonOf(o.ID)
			}
		}
	case KindMulti:
		ids := []string{}
		for _, s := range listOf(v) {
			if o := q.option(s); o != nil && !contains(ids, o.ID) {
				ids = append(ids, o.ID)
			}
		}
		if len(ids) > 0 {
			return jsonOf(ids)
		}
	case KindConfirm:
		if b, ok := boolOf(v); ok {
			return jsonOf(b)
		}
	case KindNumber:
		if n, ok := numberOf(v); ok && (q.Min == nil || n >= *q.Min) && (q.Max == nil || n <= *q.Max) {
			return jsonOf(n)
		}
	case KindText:
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return jsonOf(s)
		}
	}
	return nil
}

// boolOf reads a yes or no: a boolean, or the words for one.
func boolOf(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "yes", "true", "y":
			return true, true
		case "no", "false", "n":
			return false, true
		}
	}
	return false, false
}

func jsonOf(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

// clip shortens s to at most n characters, ending a cut one with "…" on a
// word boundary when there is one close enough.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := r[:n-1]
	for i := len(cut) - 1; i > len(cut)/2; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(cut), unicode.IsSpace) + "…"
}

// slug makes an option id from its label: lower case letters and digits
// joined by dashes, or "o<n>" when nothing is left.
func slug(label string, n int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(label) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return fmt.Sprintf("o%d", n)
	}
	return b.String()
}

// JSON is the payload as the ask event carries it.
func (p Payload) JSON() json.RawMessage {
	raw, _ := json.Marshal(p)
	return raw
}

// Decode reads a payload from an ask event, nil when it is not an ask_user
// payload (a plan card, say).
func Decode(raw json.RawMessage) *Payload {
	var p Payload
	if json.Unmarshal(raw, &p) != nil || len(p.Questions) == 0 {
		return nil
	}
	return &p
}

// question is the question with this id.
func (p *Payload) question(id string) *Question {
	for i := range p.Questions {
		if p.Questions[i].ID == id {
			return &p.Questions[i]
		}
	}
	return nil
}

// option is the option with this id, or with this label.
func (q *Question) option(s string) *Option {
	for i := range q.Options {
		if q.Options[i].ID == s {
			return &q.Options[i]
		}
	}
	for i := range q.Options {
		if strings.EqualFold(q.Options[i].Label, s) {
			return &q.Options[i]
		}
	}
	return nil
}
