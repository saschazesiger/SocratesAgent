// Package tests is Socrates Tests, an MCP server built into Socrates
// (docs/design/CHAT-TESTS.md): the tools a chat's agent writes, runs and
// documents its own tests with. The tests are the chat's (internal/chattests)
// and its page shows them, live; the agent works every step with the tools
// it has anyway (a device on BrowserStack, a browser, a shell) and judges it
// here, with what it observed and the files that show it.
//
// It needs no account and no configuration, and like every built-in server
// it is off in a chat until Admin or the chat switches it on.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/chattests"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// Name is the server's name in a chat.
const Name = "socrates-tests"

// The tools, bare.
const (
	List   = "tests_list"
	Create = "test_create"
	Update = "test_update"
	Delete = "test_delete"
	Start  = "test_start"
	Step   = "test_step"
	Attach = "test_attach"
	Finish = "test_finish"
	Read   = "test_read"
)

// Deps is what the server needs of the Socrates server.
type Deps struct {
	// Store keeps every chat's tests; nil while there is none (the names
	// of the built-in servers are read before anything runs).
	Store *chattests.Store
	// Recorders is the servers that record a chat's actions as evidence
	// (BrowserStack), whose screenshots a judged step takes; nil for none.
	Recorders func() []NamedRecorder
}

// NamedRecorder is a recording server with its name, which the evidence
// it gives is credited to.
type NamedRecorder struct {
	Name string
	mcpserver.Recorder
}

// Server is Socrates Tests.
type Server struct{ deps Deps }

// New is the server.
func New(d Deps) *Server { return &Server{deps: d} }

func (s *Server) Name() string  { return Name }
func (s *Server) Title() string { return "Socrates Tests" }
func (s *Server) Description() string {
	return "Lets the agent write, run and document its own tests, step by step with screenshots, shown live on the chat's page."
}

func (s *Server) Instructions() string {
	return "Socrates Tests: this chat's own tests. Write a test for a flow before you test it, run it with what you " +
		"have (a device on BrowserStack, a browser, a shell, the code), judge every step on evidence, and the person " +
		"sees each test, step and screenshot live on the chat's page.\n" +
		"- Write: " + Create + " with a title, the goal and context (preconditions, edge cases), the environment " +
		"(device, browser, site, build) and steps. A step is what a person wants done (\"open the plans from the " +
		"header\", not \"tap Upgrade\") and expect: what must be seen when it worked. One step per thing a person " +
		"would check. " + Update + " changes a test (skip: a reason sets it aside, \"\" brings it back), " + Delete +
		" deletes one that is no longer wanted.\n" +
		"- Run: " + Start + ", then work the steps in order and judge each with " + Step + " as soon as it is done: " +
		"pass (everything expected, nothing wrong), issue (works, but something a person would notice is wrong), " +
		"fail (cannot be completed, wrong result, data lost, hangs, an error), blocked (not the product's fault: " +
		"infrastructure, a third party, test data, not reached), na (does not apply here). Describe what was on " +
		"screen, not what should have been. Attach evidence to every judgement of something visible: files (your " +
		"screenshots, logs) or output (a command's output), and more later with " + Attach + ". Screenshots BrowserStack took while you worked a step " +
		"are attached to it by themselves (to every test with a run open, so run one at a time). End with " + Finish + ": a summary of one or two sentences and, when " +
		"there are any, findings in Markdown (bugs with what you suspect causes them, suggestions); steps not judged " +
		"become blocked. Run a test again after a fix: every run is kept.\n" +
		"- Read: " + List + " for every test and how it stands, " + Read + " for one in full with its evidence " +
		"(absolute paths you can open). The person may change a test, set its status (an override, with a note) or " +
		"ask for a run again: read what they changed and take their word as final.\n" +
		"- What the tests found is yours to use as the work calls for: say it in the chat, fix it, test it again. " +
		"Never put a secret into a test, a judgement or a file."
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var (
	testArg  = str("The test's id (T3) or its exact title.")
	stepsArg = map[string]any{"type": "array", "minItems": 1, "maxItems": chattests.MaxSteps,
		"description": "The steps in order, each what a person wants done and what must be seen when it worked.",
		"items": obj(map[string]any{
			"action": str("What to do, as the person's goal (open the plans from the header)."),
			"expect": str("What must be seen when it worked."),
		}, "action", "expect")}
	filesArg = strs("Files to attach: paths absolute or relative to the chat's folder (screenshots, logs, text, JSON, HTML, video).")
)

func (s *Server) Tools() []mcpserver.Tool {
	none := obj(map[string]any{})
	return []mcpserver.Tool{
		{Name: List, Title: "List the tests", Description: "Every test of this chat with its status, steps, latest run " +
			"(verdict, when, the first step that went wrong), and what the person set (an override, a skip).", Schema: none},
		{Name: Create, Title: "Write a test", Description: "Writes a new test (a draft until it runs) and answers its id.",
			Schema: obj(map[string]any{
				"title":       str("What the test checks, in a line."),
				"goal":        str("Markdown: why, the preconditions, the context and edge cases a run should know."),
				"environment": str("Where it runs: device, browser, site, build (iPhone 15 · Safari, beta)."),
				"tags":        strs("Short words to group tests by (auth, checkout)."),
				"steps":       stepsArg,
			}, "title", "steps")},
		{Name: Update, Title: "Change a test", Description: "Changes what it names. A run keeps the steps it was started " +
			"with; changed steps show the test as changed since its last run.",
			Schema: obj(map[string]any{
				"test": testArg, "title": str("A new title."), "goal": str("A new goal (Markdown)."),
				"environment": str("A new environment."), "tags": strs("The new tags (all of them)."), "steps": stepsArg,
				"skip": str("A reason sets the test aside (skipped); \"\" brings it back."),
			}, "test")},
		{Name: Delete, Title: "Delete a test", Description: "Deletes the test with every run and file of it.",
			Schema: obj(map[string]any{"test": testArg}, "test")},
		{Name: Start, Title: "Start a run", Description: "Starts a run of the test and answers its steps. A run of it " +
			"still open is finished first, its rest blocked.",
			Schema: obj(map[string]any{"test": testArg, "environment": str("Where this run happens, when not the test's environment.")}, "test")},
		{Name: Step, Title: "Judge a step", Description: "Judges a step of the open run with what was observed and the " +
			"evidence. Judging a step again replaces the verdict and keeps the evidence.",
			Schema: obj(map[string]any{
				"test":     testArg,
				"step":     map[string]any{"type": "integer", "minimum": 1, "description": "The step's number, from 1."},
				"verdict":  map[string]any{"type": "string", "enum": []string{"pass", "issue", "fail", "blocked", "na"}},
				"observed": str("What was on screen or came back, as it was."),
				"note":     str("Anything else worth knowing: a suspected cause, a workaround, what to look at."),
				"files":    filesArg,
				"output":   str("Text evidence, such as a command's output; kept as a file."),
			}, "test", "step", "verdict", "observed")},
		{Name: Attach, Title: "Attach files", Description: "Attaches files to a step of the open run, or to the run itself.",
			Schema: obj(map[string]any{
				"test": testArg, "files": filesArg,
				"step":    map[string]any{"type": "integer", "minimum": 1, "description": "The step's number; the run itself when left out."},
				"caption": str("What the files show."),
			}, "test", "files")},
		{Name: Finish, Title: "Finish the run", Description: "Ends the open run: steps not judged become blocked (not " +
			"reached), and the run takes its worst verdict.",
			Schema: obj(map[string]any{
				"test":     testArg,
				"summary":  str("One or two sentences: what works and what does not."),
				"findings": str("Markdown, free-form: the bugs found with their suspected causes, suggestions, anything to follow up."),
			}, "test", "summary")},
		{Name: Read, Title: "Read a test", Description: "A test in full: its steps, its runs with every judgement and the " +
			"evidence as absolute paths, what the person set and its history.",
			Schema: obj(map[string]any{"test": testArg,
				"run": map[string]any{"type": "integer", "minimum": 1, "description": "Only this run; every run when left out."}}, "test")},
	}
}

func (s *Server) Call(ctx context.Context, sess mcpserver.Session, tool string, args json.RawMessage) mcpserver.Result {
	if s.deps.Store == nil {
		return mcpserver.Error("Socrates Tests is not running.")
	}
	if sess.ID == "" {
		return mcpserver.Error("Socrates Tests works for a chat.")
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	text, err := (&call{s: s, sess: sess, st: s.deps.Store}).do(tool, args)
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	return mcpserver.Text(text)
}

type call struct {
	s    *Server
	sess mcpserver.Session
	st   *chattests.Store
}

type stepArg struct {
	Action string `json:"action"`
	Expect string `json:"expect"`
}

func steps(in []stepArg) []chattests.Step {
	out := make([]chattests.Step, len(in))
	for i, s := range in {
		out[i] = chattests.Step{Action: s.Action, Expect: s.Expect}
	}
	return out
}

func (c *call) do(tool string, args json.RawMessage) (string, error) {
	chat := c.sess.ID
	switch tool {
	case List:
		if err := decode(args, &struct{}{}); err != nil {
			return "", err
		}
		tests, err := c.st.List(chat)
		if err != nil {
			return "", err
		}
		return chattests.ListText(tests), nil
	case Create:
		var a struct {
			Title       string    `json:"title"`
			Goal        string    `json:"goal"`
			Environment string    `json:"environment"`
			Tags        []string  `json:"tags"`
			Steps       []stepArg `json:"steps"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		t, err := c.st.Create(chat, chattests.Fields{Title: a.Title, Goal: a.Goal, Environment: a.Environment, Tags: a.Tags,
			Steps: steps(a.Steps)}, chattests.ByAgent)
		if err != nil {
			return "", err
		}
		return chattests.CreatedText(t), nil
	case Update:
		var a struct {
			Test        string     `json:"test"`
			Title       *string    `json:"title"`
			Goal        *string    `json:"goal"`
			Environment *string    `json:"environment"`
			Tags        *[]string  `json:"tags"`
			Steps       *[]stepArg `json:"steps"`
			Skip        *string    `json:"skip"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		p := chattests.Patch{Title: a.Title, Goal: a.Goal, Environment: a.Environment, Tags: a.Tags, Skip: a.Skip}
		if a.Steps != nil {
			s := steps(*a.Steps)
			p.Steps = &s
		}
		t, err := c.st.Update(chat, a.Test, p, chattests.ByAgent)
		if err != nil {
			return "", err
		}
		return chattests.UpdatedText(t), nil
	case Delete:
		var a struct {
			Test string `json:"test"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		t, err := c.st.Get(chat, a.Test)
		if err != nil {
			return "", err
		}
		if err := c.st.Delete(chat, t.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted %s “%s” with its runs and files.", t.ID, t.Title), nil
	case Start:
		var a struct {
			Test        string `json:"test"`
			Environment string `json:"environment"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		t, r, err := c.st.Start(chat, a.Test, a.Environment, c.cursors())
		if err != nil {
			return "", err
		}
		return chattests.StartText(t, r), nil
	case Step:
		var a struct {
			Test     string   `json:"test"`
			Step     int      `json:"step"`
			Verdict  string   `json:"verdict"`
			Observed string   `json:"observed"`
			Note     string   `json:"note"`
			Files    []string `json:"files"`
			Output   string   `json:"output"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		files, err := c.files(a.Files, "")
		if err != nil {
			return "", err
		}
		j := chattests.Judgement{Step: a.Step, Verdict: a.Verdict, Observed: a.Observed, Note: a.Note, Files: files,
			Output: a.Output, Recorded: c.recorded}
		t, _, added, err := c.st.Judge(chat, a.Test, j)
		if err != nil {
			return "", err
		}
		return chattests.JudgedText(t, a.Step, added, c.st.FilesDir(chat, t.ID)), nil
	case Attach:
		var a struct {
			Test    string   `json:"test"`
			Files   []string `json:"files"`
			Step    int      `json:"step"`
			Caption string   `json:"caption"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		if len(a.Files) == 0 {
			return "", errors.New("files names no file to attach")
		}
		files, err := c.files(a.Files, a.Caption)
		if err != nil {
			return "", err
		}
		t, added, err := c.st.Attach(chat, a.Test, a.Step, files)
		if err != nil {
			return "", err
		}
		return chattests.AttachText(t, a.Step, added, c.st.FilesDir(chat, t.ID)), nil
	case Finish:
		var a struct {
			Test     string `json:"test"`
			Summary  string `json:"summary"`
			Findings string `json:"findings"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		t, r, err := c.st.Finish(chat, a.Test, a.Summary, a.Findings)
		if err != nil {
			return "", err
		}
		return chattests.FinishText(t, r), nil
	case Read:
		var a struct {
			Test string `json:"test"`
			Run  int    `json:"run"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		t, err := c.st.Get(chat, a.Test)
		if err != nil {
			return "", err
		}
		return chattests.TestText(t, a.Run, c.st.FilesDir(chat, t.ID))
	}
	return "", fmt.Errorf("Socrates Tests has no tool %s", tool)
}

// files is the paths an agent named as files to attach: relative ones are
// the chat folder's.
func (c *call) files(paths []string, caption string) ([]chattests.Attach, error) {
	var out []chattests.Attach
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if c.sess.Workdir == "" {
				return nil, fmt.Errorf("%s: give an absolute path, the chat has no folder", p)
			}
			p = filepath.Join(c.sess.Workdir, p)
		}
		out = append(out, chattests.Attach{Path: filepath.Clean(p), Caption: caption, Source: string(chattests.ByAgent)})
	}
	return out, nil
}

// cursors is where every recording server's actions of the chat stand now:
// a run takes only what was recorded after it started.
func (c *call) cursors() map[string]int {
	out := map[string]int{}
	for _, r := range c.recorders() {
		out[r.Name] = len(readActions(r.EvidenceDir(c.sess.ID)))
	}
	return out
}

// recorded is the screenshots the recording servers took for the chat
// since the cursors, as files to attach, and where the cursors stand after
// them.
func (c *call) recorded(from map[string]int) ([]chattests.Attach, map[string]int) {
	var files []chattests.Attach
	cursors := map[string]int{}
	for _, r := range c.recorders() {
		dir := r.EvidenceDir(c.sess.ID)
		lines := readActions(dir)
		start := from[r.Name]
		if start > len(lines) {
			// The recording was started over (the chat's folder was
			// emptied): everything in it is new.
			start = 0
		}
		for _, l := range lines[start:] {
			if l.Screenshot == "" || strings.ContainsAny(l.Screenshot, `/\`) {
				continue
			}
			files = append(files, chattests.Attach{Path: filepath.Join(dir, l.Screenshot), Caption: l.Summary, Source: r.Name})
		}
		cursors[r.Name] = len(lines)
	}
	return files, cursors
}

func (c *call) recorders() []NamedRecorder {
	if c.s.deps.Recorders == nil {
		return nil
	}
	return c.s.deps.Recorders()
}

// action is the part of a line of actions.jsonl evidence takes.
type action struct {
	Summary    string `json:"summary"`
	Screenshot string `json:"screenshot"`
}

// readActions is every whole line of a recording's actions.jsonl, in
// order: a line still being written (no newline yet) is left for the next
// read, and a line that cannot be read counts, as nothing.
func readActions(dir string) []action {
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	if err != nil {
		return nil
	}
	if i := bytes.LastIndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i]
	} else {
		return nil
	}
	var out []action
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var a action
		_ = json.Unmarshal(line, &a)
		out = append(out, a)
	}
	return out
}

// decode reads a tool's arguments, refusing a field the tool does not have.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the arguments could not be read: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}
