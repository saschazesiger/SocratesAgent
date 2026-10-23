package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

func TestMain(m *testing.M) { os.Exit(testhome.Main(m)) }

// fullCall is the call of REBUILD.md §5.1 with one question of every kind.
const fullCall = `{
  "title": "Deployment setup",
  "context": "We deploy **the API**.",
  "questions": [
    {"id": "db", "header": "Database", "question": "Which database should the service use?",
     "context": "Both work.", "kind": "single", "default": "pg",
     "options": [
       {"id": "pg", "label": "PostgreSQL", "description": "The default", "details": "**Pros**", "recommended": true},
       {"id": "sqlite", "label": "SQLite"}
     ]},
    {"id": "features", "header": "Features", "question": "Which features ship first?", "kind": "multi",
     "min": 1, "max": 9, "options": [{"id": "dark", "label": "Dark mode"}, {"id": "search", "label": "Search"}]},
    {"id": "name", "question": "What should the service be called?", "kind": "text", "required": false},
    {"id": "migrate", "header": "Migrations", "question": "Run the migrations on every deploy?", "kind": "confirm", "default": true},
    {"id": "replicas", "header": "Replicas", "question": "How many replicas?", "kind": "number", "min": 1, "max": 10,
     "default": 2, "allow_other": false, "allow_delegate": false}
  ]
}`

func mustParse(t *testing.T, call string) Payload {
	t.Helper()
	p, err := Parse(json.RawMessage(call))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

// A call of every kind is normalised: defaults applied, the header filled
// from the question, a multi's max kept within its options, every default in
// its kind's own type.
func TestParseNormalisesAFullCall(t *testing.T) {
	p := mustParse(t, fullCall)
	if p.Title != "Deployment setup" || len(p.Questions) != 5 {
		t.Fatalf("payload %+v", p)
	}
	db := p.Questions[0]
	if db.Kind != KindSingle || !db.AllowOther || !db.AllowDelegate || !db.Required || string(db.Default) != `"pg"` ||
		!db.Options[0].Recommended || db.Options[0].Details != "**Pros**" {
		t.Fatalf("db %+v", db)
	}
	if f := p.Questions[1]; *f.Max != 2 || *f.Min != 1 {
		t.Fatalf("features min %v max %v", *f.Min, *f.Max)
	}
	if n := p.Questions[2]; n.Header != "What should the…" || n.Required {
		t.Fatalf("name %+v", n)
	}
	if m := p.Questions[3]; string(m.Default) != "true" {
		t.Fatalf("migrate default %s", m.Default)
	}
	if r := p.Questions[4]; r.AllowOther || r.AllowDelegate || string(r.Default) != "2" || *r.Min != 1 {
		t.Fatalf("replicas %+v", r)
	}
	// The payload as the ask event carries it reads back the same.
	if back := Decode(p.JSON()); back == nil || back.Questions[4].Header != "Replicas" {
		t.Fatalf("decoded %+v", back)
	}
}

// The shorthands models write are taken: options as plain labels or with a
// value for an id, a kind inferred from the options or named another way,
// allow_free_text for allow_other; a default that fits nothing is dropped.
func TestParseTakesTheShorthandsModelsWrite(t *testing.T) {
	p := mustParse(t, `{"questions":[
	  {"question":"Deploy now?","options":["Yes, now","Later"],"default":"tomorrow"},
	  {"question":"Which?","kind":"multiselect","options":[{"value":"a","label":"A"},{"value":"b","label":"B"}],"default":["b","x"]},
	  {"question":"Sure?","kind":"boolean","default":"yes"},
	  {"question":"Free","allow_free_text":false}]}`)
	q := p.Questions
	if q[0].ID != "q1" || q[0].Kind != KindSingle || q[0].Options[0].ID != "yes-now" || q[0].Default != nil {
		t.Fatalf("q1 %+v", q[0])
	}
	if q[1].Kind != KindMulti || q[1].Options[1].ID != "b" || string(q[1].Default) != `["b"]` {
		t.Fatalf("q2 %+v", q[1])
	}
	if q[2].Kind != KindConfirm || string(q[2].Default) != "true" {
		t.Fatalf("q3 %+v", q[2])
	}
	if q[3].Kind != KindText || q[3].AllowOther {
		t.Fatalf("q4 %+v", q[3])
	}
}

// A call that cannot be asked is refused with everything that is wrong with
// it, so the model fixes it in one retry.
func TestParseSaysEverythingThatIsWrong(t *testing.T) {
	many := make([]string, 13)
	for i := range many {
		many[i] = `"o` + string(rune('a'+i)) + `"`
	}
	_, err := Parse(json.RawMessage(`{"questions":[
	  {"id":"a","question":"One","kind":"single"},
	  {"id":"a","question":"Two","kind":"text"},
	  {"id":"c","question":"Three","kind":"dropdown"},
	  {"id":"d","question":"Four","options":[` + strings.Join(many, ",") + `]},
	  {"id":"e","kind":"text"},
	  {"id":"f","question":"Six","kind":"confirm","options":["x"]}]}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{`kind "single" needs options`, `the id "a" is used by another`, `kind "dropdown" is not one of`,
		"has 13 options", `question 5 ("e") has no question text`, `kind "confirm" takes no options`, "call ask_user again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
	for _, call := range []string{`{}`, `{"questions":[]}`, `[1]`, `{"questions":"x"}`} {
		if _, err := Parse(json.RawMessage(call)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err %v", call, err)
		}
	}
	qs := make([]string, MaxQuestions+1)
	for i := range qs {
		qs[i] = `{"question":"Q"}`
	}
	if _, err := Parse(json.RawMessage(`{"questions":[` + strings.Join(qs, ",") + `]}`)); err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Fatalf("21 questions: %v", err)
	}
}

// The schema the model sees is JSON and has the limits of §5.1.
func TestTheSchemaHasTheLimits(t *testing.T) {
	var s struct {
		Properties struct {
			Questions struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					Properties map[string]struct {
						MaxLength int `json:"maxLength"`
						MaxItems  int `json:"maxItems"`
						Enum      []string
					} `json:"properties"`
				} `json:"items"`
			} `json:"questions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Schema(), &s); err != nil {
		t.Fatal(err)
	}
	q := s.Properties.Questions
	if q.MaxItems != MaxQuestions || q.Items.Properties["header"].MaxLength != MaxHeader ||
		q.Items.Properties["options"].MaxItems != MaxOptions || len(q.Items.Properties["kind"].Enum) != 5 {
		t.Fatalf("schema %+v", q)
	}
}

// The browser's answer (askdialog.js) reads back as §5.2: every question has
// an entry, selections name only the question's options, a confirm is a yes
// or no with its value, a number its value.
func TestReadAnswerNormalisesTheDialogsAnswer(t *testing.T) {
	p := mustParse(t, fullCall)
	a := ReadAnswer(&p, json.RawMessage(`{"answers":{
	  "db":{"selected":["sqlite","nope"],"other":null,"note":" keep it small ","delegated":false,"skipped":false},
	  "features":{"selected":[],"other":"Exports","note":null,"delegated":false,"skipped":false},
	  "migrate":{"selected":["no"],"other":null,"note":null,"delegated":false,"skipped":false},
	  "replicas":{"selected":[],"other":"3","value":3,"note":null,"delegated":false,"skipped":false}},
	  "general_note":"  ","follow_up":"Why SQLite at all?"}`))
	db := a.Answers["db"]
	if strings.Join(db.Selected, ",") != "sqlite" || *db.Note != "keep it small" || db.Skipped {
		t.Fatalf("db %+v", db)
	}
	if f := a.Answers["features"]; *f.Other != "Exports" || f.Skipped {
		t.Fatalf("features %+v", f)
	}
	if n := a.Answers["name"]; !n.Skipped || n.Selected == nil {
		t.Fatalf("name %+v", n)
	}
	if m := a.Answers["migrate"]; m.Selected[0] != "no" || string(m.Value) != "false" {
		t.Fatalf("migrate %+v", m)
	}
	if r := a.Answers["replicas"]; string(r.Value) != "3" || r.Other != nil {
		t.Fatalf("replicas %+v", r)
	}
	if a.GeneralNote != nil || *a.FollowUp != "Why SQLite at all?" {
		t.Fatalf("notes %+v", a)
	}
	// What the model gets: the JSON and what to do with the follow-up.
	result := ToolResult(&p, json.RawMessage(`{"answers":{"db":{"delegated":true}},"follow_up":"Why?"}`))
	if !strings.HasPrefix(result, `{"answers":{`) || !strings.Contains(result, "answer it first") ||
		!strings.Contains(result, "decide them yourself") {
		t.Fatalf("tool result %s", result)
	}
	var back Answer
	if err := json.Unmarshal([]byte(strings.SplitN(result, "\n", 2)[0]), &back); err != nil || !back.Answers["db"].Delegated {
		t.Fatalf("the result's JSON %v %+v", err, back)
	}
}

// Plain values are read as the answer itself, and a bare string as a note.
func TestReadAnswerTakesPlainValues(t *testing.T) {
	p := mustParse(t, fullCall)
	a := ReadAnswer(&p, json.RawMessage(`{"answers":{"db":"pg","features":["dark","search"],"name":"Atlas","migrate":true,"replicas":"4"}}`))
	if s := a.Answers["db"]; strings.Join(s.Selected, ",") != "pg" || s.Other != nil {
		t.Fatalf("db %+v", s)
	}
	if s := a.Answers["features"]; len(s.Selected) != 2 {
		t.Fatalf("features %+v", s)
	}
	if s := a.Answers["name"]; *s.Other != "Atlas" {
		t.Fatalf("name %+v", s)
	}
	if s := a.Answers["migrate"]; s.Selected[0] != "yes" {
		t.Fatalf("migrate %+v", s)
	}
	if s := a.Answers["replicas"]; string(s.Value) != "4" {
		t.Fatalf("replicas %+v", s)
	}
	if a := ReadAnswer(&p, json.RawMessage(`"just do it"`)); *a.GeneralNote != "just do it" || !a.Answers["db"].Skipped {
		t.Fatalf("bare %+v", a)
	}
}

// The message an answer becomes when the call is gone says what was asked,
// what was answered, and carries the answer as the tool would have returned
// it.
func TestMessageTextReadsAsTheAnswer(t *testing.T) {
	p := mustParse(t, fullCall)
	text := MessageText(p.JSON(), json.RawMessage(`{"answers":{"db":{"selected":["pg"],"note":"16"},
	  "replicas":{"delegated":true}},"general_note":"ship it"}`))
	for _, want := range []string{"Answer to your earlier question (Deployment setup)", "1. Which database should the service use?",
		"PostgreSQL [pg] Note: 16", "You decide.", "(not answered)", "Note from the person: ship it", `"general_note":"ship it"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not say %q:\n%s", want, text)
		}
	}
	plan := MessageText(json.RawMessage(`{"kind":"plan","plan":"x"}`), json.RawMessage(`{"decision":"keep_planning","note":"smaller"}`))
	if !strings.Contains(plan, "keep planning") || !strings.Contains(plan, "smaller") {
		t.Fatalf("plan %s", plan)
	}
	if other := MessageText(json.RawMessage(`{"x":1}`), json.RawMessage(`"blue"`)); !strings.HasSuffix(other, "blue") {
		t.Fatalf("other %s", other)
	}
}

// Codex's request_user_input becomes a payload and its answer the shape codex
// takes: the chosen labels, the person's words, a delegation, notes.
func TestRequestUserInputRoundTrip(t *testing.T) {
	p, ok := FromUserInput(json.RawMessage(`{"threadId":"t","turnId":"u","itemId":"call_1","questions":[
	  {"id":"color","header":"Color","question":"Which color should the button be?","isOther":true,"isSecret":false,
	   "options":[{"label":"Blue (Recommended)","description":"Matches the brand"},{"label":"Red","description":"Stands out"}]},
	  {"id":"token","header":"Token","question":"Paste the token","isOther":false,"isSecret":true,"options":null}],
	  "isBlocking":true}`))
	if !ok || p.Source != SourceUserInput || len(p.Questions) != 2 {
		t.Fatalf("payload %+v", p)
	}
	c := p.Questions[0]
	if c.Kind != KindSingle || !c.Options[0].Recommended || c.Options[0].ID != "Blue (Recommended)" || !c.AllowOther {
		t.Fatalf("color %+v", c)
	}
	if tok := p.Questions[1]; tok.Kind != KindText || !tok.Secret || !tok.AllowOther {
		t.Fatalf("token %+v", tok)
	}
	got, _ := json.Marshal(UserInputResult(&p, json.RawMessage(`{"answers":{
	  "color":{"selected":["Red"],"note":"darker"},"token":{"other":"s3cret"}},"general_note":"thanks"}`)))
	want := `{"answers":{"color":{"answers":["Red","Note: darker"]},"token":{"answers":["s3cret","Note from the person: thanks"]}}}`
	if string(got) != want {
		t.Fatalf("result\n got %s\nwant %s", got, want)
	}
	if _, ok := FromUserInput(json.RawMessage(`{"questions":[]}`)); ok {
		t.Fatal("an empty request was taken")
	}
}

func TestAsyncQuestionsBecomeAPayload(t *testing.T) {
	p, ok := FromAsync(json.RawMessage(`[{"title":"Should I also update the docs?","options":["Yes","No"]},{"title":"Anything else?"}]`))
	if !ok || p.Source != SourceAsync || p.Questions[0].Kind != KindSingle || p.Questions[0].Options[1].Label != "No" ||
		p.Questions[1].Kind != KindText || p.Questions[1].ID != "q2" {
		t.Fatalf("payload %+v", p)
	}
}

// An elicitation form becomes one question per field in its order, and the
// answer a value of the field's type for each one answered.
func TestElicitationRoundTrip(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
	  "colour":{"type":"string","title":"Colour","description":"Your pick","enum":["red","green"],"enumNames":["Red","Green"]},
	  "count":{"type":"integer","minimum":1,"maximum":5},
	  "sure":{"type":"boolean","title":"Sure?"},
	  "tags":{"type":"array","items":{"anyOf":[{"const":"a","title":"A"},{"const":"b","title":"B"}]},"maxItems":2},
	  "notes":{"type":"string","title":"Notes"}},"required":["colour"]}`)
	p, ok := FromElicitation("lab", "Pick a colour.", schema, []string{"colour", "notes"})
	if !ok || p.Source != SourceElicitation || p.Title != "Pick a colour." || !strings.Contains(p.Context, "lab") {
		t.Fatalf("payload %+v", p)
	}
	var ids []string
	for _, q := range p.Questions {
		ids = append(ids, q.ID+":"+q.Kind)
	}
	if strings.Join(ids, ",") != "colour:single,notes:text,count:number,sure:confirm,tags:multi" {
		t.Fatalf("questions %v", ids)
	}
	if c := p.Questions[0]; !c.Required || c.AllowDelegate || c.Options[1].Label != "Green" || c.Options[1].ID != "green" {
		t.Fatalf("colour %+v", c)
	}
	action, content := ElicitationResult(&p, schema, json.RawMessage(`{"answers":{"colour":{"selected":["green"]},
	  "count":{"value":3.0},"sure":{"selected":["yes"]},"tags":{"selected":["b"]},"notes":{"other":"from the phone"}}}`))
	got, _ := json.Marshal(content)
	if action != "accept" || string(got) != `{"colour":"green","count":3,"notes":"from the phone","sure":true,"tags":["b"]}` {
		t.Fatalf("%s %s", action, got)
	}
	if action, _ := ElicitationResult(&p, schema, json.RawMessage(`{"answers":{},"follow_up":"why?"}`)); action != "decline" {
		t.Fatalf("nothing answered: %s", action)
	}
	if _, ok := FromElicitation("lab", "x", json.RawMessage(`{"type":"object","properties":{}}`), nil); ok {
		t.Fatal("an empty form was taken")
	}
}

func TestOpenCodeQuestionRoundTrip(t *testing.T) {
	p, ok := FromOpenCode(json.RawMessage(`[
	  {"question":"What is your favourite colour?","header":"Colour","options":[{"label":"Green","description":"g"},{"label":"Blue"}],"multiple":false},
	  {"question":"Which fruits do you like?","header":"Fruits","options":[{"label":"Apple"},{"label":"Pear"}],"multiple":true,"custom":false}]`))
	if !ok || p.Questions[0].Kind != KindSingle || p.Questions[1].Kind != KindMulti || p.Questions[1].AllowOther ||
		!p.Questions[0].AllowOther {
		t.Fatalf("payload %+v", p)
	}
	got := OpenCodeAnswers(&p, json.RawMessage(`{"answers":{"q1":{"selected":["Green"]},"q2":{"selected":["Apple"],"other":"mango"}},"follow_up":"Why?"}`))
	b, _ := json.Marshal(got)
	if string(b) != `[["Green"],["Apple","mango","The person asks back: Why?"]]` {
		t.Fatalf("answers %s", b)
	}
}

// handle sends one message to m and decodes the response.
func handle(t *testing.T, m *MCP, ctx context.Context, msg string) map[string]any {
	t.Helper()
	raw := m.Handle(ctx, json.RawMessage(msg))
	if raw == nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response %s: %v", raw, err)
	}
	return out
}

// The MCP server answers the handshake, lists ask_user, and a call blocks
// until the person answers; the model then gets §5.2 as text.
func TestMCPAsksAndWaits(t *testing.T) {
	asked := make(chan Call, 1)
	answer := make(chan json.RawMessage, 1)
	m := &MCP{Version: "test", Instructions: Guidance("socrates_ask_user"),
		Ask: func(ctx context.Context, c Call) (json.RawMessage, error) {
			asked <- c
			select {
			case a := <-answer:
				return a, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
	init := handle(t, m, context.Background(), `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" || !strings.Contains(res["instructions"].(string), "socrates_ask_user") {
		t.Fatalf("initialize %v", init)
	}
	if handle(t, m, context.Background(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`) != nil {
		t.Fatal("a notification was answered")
	}
	list := handle(t, m, context.Background(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != Name {
		t.Fatalf("tools/list %v", list)
	}
	done := make(chan map[string]any, 1)
	go func() {
		done <- handle(t, m, context.Background(), `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_user",`+
			`"arguments":{"questions":[{"id":"go","question":"Go?","kind":"confirm"}]},"_meta":{"claudecode/toolUseId":"toolu_1","progressToken":2}}}`)
	}()
	c := <-asked
	if c.ToolUseID != "toolu_1" || c.Payload.Questions[0].ID != "go" {
		t.Fatalf("call %+v", c)
	}
	select {
	case r := <-done:
		t.Fatalf("returned before the answer: %v", r)
	case <-time.After(50 * time.Millisecond):
	}
	answer <- json.RawMessage(`{"answers":{"go":{"selected":["yes"]}}}`)
	r := <-done
	result := r["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if result["isError"] != false || !strings.Contains(text, `"go":{"selected":["yes"]`) {
		t.Fatalf("result %v", r)
	}
	// A bad call is the model's to fix: a tool error, nothing asked.
	bad := handle(t, m, context.Background(), `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_user","arguments":{}}}`)
	if res := bad["result"].(map[string]any); res["isError"] != true || len(asked) != 0 {
		t.Fatalf("bad call %v", bad)
	}
	if unknown := handle(t, m, context.Background(), `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`); unknown["error"] == nil {
		t.Fatalf("unknown method %v", unknown)
	}
}

// notifications/cancelled ends the call it names, which then answers nothing,
// and a waiting call reports progress.
func TestMCPCancelAndProgress(t *testing.T) {
	var mu sync.Mutex
	var progress []int
	m := &MCP{
		Ask: func(ctx context.Context, c Call) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Progress: func(token json.RawMessage, n int) {
			mu.Lock()
			progress = append(progress, n)
			mu.Unlock()
		},
	}
	old := progressEvery
	progressEvery = 20 * time.Millisecond
	defer func() { progressEvery = old }()
	done := make(chan json.RawMessage, 1)
	go func() {
		done <- m.Handle(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":"c7","method":"tools/call",`+
			`"params":{"name":"ask_user","arguments":{"questions":[{"question":"Q"}]},"_meta":{"progressToken":"p"}}}`))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(progress)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no progress while waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if m.Handle(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"c7","reason":"aborted"}}`)) != nil {
		t.Fatal("the cancel was answered")
	}
	select {
	case r := <-done:
		if r != nil {
			t.Fatalf("a cancelled call answered %s", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancel did not end the call")
	}
	note := ProgressNotification(json.RawMessage(`"p"`), 3)
	if !strings.Contains(string(note), `"method":"notifications/progress"`) || !strings.Contains(string(note), `"progressToken":"p"`) {
		t.Fatalf("notification %s", note)
	}
}
