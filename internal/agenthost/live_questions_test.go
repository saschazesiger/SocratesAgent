//go:build linux

package agenthost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// The live question tests run a real agent - the real CLI on a real model,
// in a real agent host of the built socrates binary - through the questions'
// life: asked with ask_user, settled by a message or an answer, made moot by
// a change of plan, or still needed. They check that the agent keeps its open
// questions current (withdraw_question) the way Socrates tells it to, and
// leaves a question open that is still needed. A model is not a function, so
// each case logs what the agent did, and SOCRATES_LIVE_RUNS repeats it.
//
//	SOCRATES_LIVE_QUESTIONS=1
//	SOCRATES_LIVE_BIN        the socrates binary (go build -o … .)
//	SOCRATES_LIVE_HARNESS    claude (default), opencode or codex
//	SOCRATES_LIVE_MODEL      the model (haiku by default)
//	SOCRATES_LIVE_EFFORT     the effort, if any
//	SOCRATES_LIVE_ENV        more of the agent's environment, one KEY=value a
//	                         line (CLAUDE_CONFIG_DIR, OPENCODE_AUTH_CONTENT …)
//	SOCRATES_LIVE_CASES      the cases, comma separated (all by default)
//	SOCRATES_LIVE_RUNS       how often each case runs (1)

// liveStep is one thing the person does once the agent is idle: write a
// message, or answer the question `ask` names (a word of its text).
type liveStep struct {
	say    string
	ask    string
	avoid  string
	answer string
	note   string
}

type liveQuestionCase struct {
	name   string
	files  map[string]string
	prompt string
	// asked is a word one of the first turn's questions must have, for the
	// case to go on (the agent asked what the case is about).
	asked string
	steps []liveStep
	// check says what is wrong at the end.
	check func(r *liveQuestionRun) []string
}

var liveQuestionCases = []liveQuestionCase{
	{
		// The person writes the answer as a message instead of in the
		// dialog: every question the message settled is withdrawn.
		name:   "settled-by-message",
		files:  map[string]string{"app.py": "import json\n\nconfig = json.load(open('config.json'))\nprint(config)\n"},
		prompt: "Write config.json for this app with two keys: \"database\" (the database engine we use) and \"port\" (the port it listens on). You cannot know either, so ask me; do not guess.",
		asked:  "",
		steps:  []liveStep{{say: "Use PostgreSQL, and port 8080."}},
		check: func(r *liveQuestionRun) []string {
			var out []string
			if c := strings.ToLower(r.file("config.json")); !strings.Contains(c, "postgres") || !strings.Contains(c, "8080") {
				out = append(out, "config.json does not have the values: "+r.file("config.json"))
			}
			if open := r.open(); len(open) > 0 {
				out = append(out, "still open after the message settled them: "+strings.Join(open, "; "))
			}
			return out
		},
	},
	{
		// A message about something else leaves the question open: it is
		// still needed.
		name: "unrelated-message",
		files: map[string]string{"util.py": "def calc(items):\n    return sum(items)\n",
			"main.py": "from util import calc\n\nprint(calc([1, 2, 3]))\n"},
		prompt: "Rename the function calc in util.py, and where it is used, to the name my colleague Mara chose yesterday. I do not know it; ask me for it rather than guessing.",
		asked:  "",
		steps:  []liveStep{{say: "Meanwhile, also add a .gitignore for Python to the repository."}},
		check: func(r *liveQuestionRun) []string {
			var out []string
			if r.file(".gitignore") == "" {
				out = append(out, "no .gitignore")
			}
			open := r.open()
			if len(open) == 0 {
				out = append(out, "the question about the name was withdrawn although nothing settled it")
			}
			if len(open) > 1 {
				out = append(out, "the same thing is asked more than once: "+strings.Join(open, "; "))
			}
			if !strings.Contains(r.file("util.py"), "def calc(") {
				out = append(out, "calc was renamed without the answer: "+r.file("util.py"))
			}
			return out
		},
	},
	{
		// The plan changes: the question about the dropped plan is withdrawn.
		name:   "plan-changed",
		files:  map[string]string{"index.html": "<h1>Hello</h1>\n"},
		prompt: "Add a script deploy.sh that uploads this site to our cloud provider. Ask me which provider we use (AWS, GCP or Azure) before you write it.",
		asked:  "",
		steps:  []liveStep{{say: "Change of plan: we do not deploy this site anywhere. Instead, write a short README.md that says how to open it locally."}},
		check: func(r *liveQuestionRun) []string {
			var out []string
			if r.file("README.md") == "" {
				out = append(out, "no README.md")
			}
			if open := r.open(); len(open) > 0 {
				out = append(out, "still open after the plan changed: "+strings.Join(open, "; "))
			}
			return out
		},
	},
	{
		// An answer that settles another question too: that one is
		// withdrawn.
		name:   "answer-settles-another",
		files:  map[string]string{"tool.py": "print('tidy')\n"},
		prompt: "Before you can write the LICENSE file and pyproject.toml I need to decide two things: the license (MIT or Apache-2.0) and the project's name. Ask me each of them as a question of its own.",
		asked:  "licen",
		steps:  []liveStep{{ask: "licen", avoid: "name", answer: "MIT", note: "The project's name is tidy-tools, so you do not need to ask me that."}},
		check: func(r *liveQuestionRun) []string {
			var out []string
			if !strings.Contains(r.file("LICENSE"), "MIT") {
				out = append(out, "LICENSE is not MIT")
			}
			if !strings.Contains(r.file("pyproject.toml"), "tidy-tools") {
				out = append(out, "pyproject.toml does not name tidy-tools")
			}
			if open := r.open(); len(open) > 0 {
				out = append(out, "still open after the answer settled it: "+strings.Join(open, "; "))
			}
			return out
		},
	},
	{
		// A message that makes the question wrong, and a new one needed: the
		// old one is withdrawn and the new one asked.
		name:   "replaced-by-new-question",
		files:  map[string]string{"index.html": "<h1>Shop</h1>\n"},
		prompt: "Add a script deploy.sh that uploads this site to our cloud provider. Ask me which provider we use (AWS, GCP or Azure) before you write it.",
		asked:  "",
		steps:  []liveStep{{say: "We do not use a cloud provider: the site goes to our own server over SSH with rsync. Write deploy.sh for that."}},
		check: func(r *liveQuestionRun) []string {
			var out []string
			first := r.asks[0]
			if a := r.ask(first.ID); a == nil || !(a.Closed && a.ClosedBy == agent.AskClosedWithdrawn) {
				out = append(out, "the question about the provider is still open: "+questionOf(first.Payload))
			}
			script := r.file("deploy.sh")
			if script != "" && !strings.Contains(script, "rsync") {
				out = append(out, "deploy.sh does not use rsync")
			}
			// Which server it is, the agent cannot know: it asks, or writes
			// a placeholder it says it assumed.
			if len(r.open()) == 0 && script == "" {
				out = append(out, "neither a script nor a question about the server")
			}
			return out
		},
	},
}

// liveQuestionRun is one case run once.
type liveQuestionRun struct {
	t    *testing.T
	work string
	c    *Conn
	evs  []agent.Event
	// asks is every ask, in the order asked.
	asks []*agent.Ask
	log  strings.Builder
}

func (r *liveQuestionRun) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.work, name))
	return string(b)
}

func (r *liveQuestionRun) fold() *agent.Projection { return agent.Fold(r.evs) }

func (r *liveQuestionRun) ask(id string) *agent.Ask {
	for _, a := range r.fold().Asks {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// open is every open question, in words.
func (r *liveQuestionRun) open() []string {
	var out []string
	for _, a := range r.fold().OpenAsks() {
		out = append(out, a.ID+": "+questionOf(a.Payload))
	}
	return out
}

func questionOf(raw json.RawMessage) string {
	p := askuser.Decode(raw)
	if p == nil || len(p.Questions) == 0 {
		return string(raw)
	}
	var parts []string
	for _, q := range p.Questions {
		parts = append(parts, q.Question)
	}
	return strings.Join(parts, " / ")
}

// read takes events until one says the turn ended, and logs what the agent
// did in it.
func (r *liveQuestionRun) turn(timeout time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			r.t.Fatalf("no turn end in %s\n%s", timeout, r.log.String())
		}
		f, err := r.c.Next()
		if err != nil {
			r.t.Fatalf("connection: %v\n%s", err, r.log.String())
		}
		if f.Fail != nil {
			r.t.Fatalf("refused: %+v", f.Fail)
		}
		if f.Event == nil {
			continue
		}
		ev := *f.Event
		r.evs = append(r.evs, ev)
		switch ev.Type {
		case agent.TypeAsk:
			fmt.Fprintf(&r.log, "  ASK %s: %s\n", ev.AskID, questionOf(ev.Payload))
		case agent.TypeAskClosed:
			fmt.Fprintf(&r.log, "  CLOSED %s (%s) %s\n", ev.AskID, ev.Code, ev.Text)
		case agent.TypeTurnEnded:
			p := r.fold()
			var said []string
			var tools []string
			if p.LastTurn != nil {
				for _, m := range p.Messages {
					if m.Role != agent.RoleAssistant || m.TurnID != p.LastTurn.ID {
						continue
					}
					for _, part := range m.Parts {
						switch part.Kind {
						case agent.PartText:
							if t := strings.TrimSpace(part.Text); t != "" {
								said = append(said, t)
							}
						case agent.PartTool:
							var c struct {
								Tool, Name string
							}
							_ = json.Unmarshal(part.Content, &c)
							tools = append(tools, c.Tool+c.Name)
						}
					}
				}
			}
			fmt.Fprintf(&r.log, "  TOOLS %s\n", strings.Join(tools, ", "))
			if len(said) > 0 {
				last := said[len(said)-1]
				if len(last) > 600 {
					last = last[:600] + "…"
				}
				fmt.Fprintf(&r.log, "  SAID (last) %s\n", strings.ReplaceAll(last, "\n", "\n    "))
			}
			r.asks = p.Asks
			return
		}
	}
}

func (r *liveQuestionRun) send(id, text string) {
	r.t.Helper()
	fmt.Fprintf(&r.log, "PERSON: %s\n", text)
	if err := r.c.Send(Command{Op: OpSend, ClientID: id, Text: text}); err != nil {
		r.t.Fatal(err)
	}
}

func TestLiveQuestionsStayCurrent(t *testing.T) {
	if os.Getenv("SOCRATES_LIVE_QUESTIONS") != "1" {
		t.Skip("set SOCRATES_LIVE_QUESTIONS=1 and SOCRATES_LIVE_BIN to run a real agent")
	}
	bin := os.Getenv("SOCRATES_LIVE_BIN")
	harness := firstOf(os.Getenv("SOCRATES_LIVE_HARNESS"), "claude")
	model := firstOf(os.Getenv("SOCRATES_LIVE_MODEL"), "haiku")
	cli, err := exec.LookPath(harness)
	if err != nil || bin == "" {
		t.Fatalf("SOCRATES_LIVE_BIN %q, %s: %v", bin, harness, err)
	}
	runs, _ := strconv.Atoi(os.Getenv("SOCRATES_LIVE_RUNS"))
	runs = max(runs, 1)
	only := map[string]bool{}
	for _, name := range strings.Split(os.Getenv("SOCRATES_LIVE_CASES"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			only[name] = true
		}
	}
	home, _ := os.UserHomeDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	for _, line := range strings.Split(os.Getenv("SOCRATES_LIVE_ENV"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			env = append(env, line)
		}
	}
	for _, c := range liveQuestionCases {
		if len(only) > 0 && !only[c.name] {
			continue
		}
		for n := 1; n <= runs; n++ {
			t.Run(fmt.Sprintf("%s/%d", c.name, n), func(t *testing.T) {
				runLiveQuestionCase(t, c, bin, harness, cli, model, env)
			})
		}
	}
}

func runLiveQuestionCase(t *testing.T, c liveQuestionCase, bin, harness, cli, model string, env []string) {
	data := t.TempDir()
	work := filepath.Join(data, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range c.files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("sh", "-c", "git init -q -b main && git config user.email t@t && git config user.name t && git add -A && git commit -qm init")
	git.Dir = work
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	chat := "live" + strconv.FormatInt(time.Now().UnixNano(), 36)
	l := &Launcher{DataDir: data, Binary: bin, Logf: func(string, ...any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := l.Launch(ctx, Spec{ChatID: chat, Harness: harness, Workdir: work, Account: "live", Model: model,
		Effort: os.Getenv("SOCRATES_LIVE_EFFORT"), Binary: cli, Env: env, ParkAfterMS: 3600000}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })
	conn, err := Dial(context.Background(), SocketPath(data, chat), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	r := &liveQuestionRun{t: t, work: work, c: conn}
	defer func() { t.Logf("%s %s %s\n%s", harness, model, c.name, r.log.String()) }()

	r.send("m1", c.prompt)
	r.turn(8 * time.Minute)
	if len(r.asks) == 0 {
		t.Fatalf("the first turn asked nothing with ask_user")
	}
	if c.asked != "" && !strings.Contains(strings.ToLower(questionOf(r.asks[0].Payload)), c.asked) {
		found := false
		for _, a := range r.asks {
			found = found || strings.Contains(strings.ToLower(questionOf(a.Payload)), c.asked)
		}
		if !found {
			t.Fatalf("no question about %q", c.asked)
		}
	}
	for i, s := range c.steps {
		id := "s" + strconv.Itoa(i)
		if s.say != "" {
			r.send(id, s.say)
		} else {
			var target *agent.Ask
			for _, a := range r.fold().OpenAsks() {
				q := strings.ToLower(questionOf(a.Payload))
				if strings.Contains(q, s.ask) && (s.avoid == "" || !strings.Contains(q, s.avoid)) {
					target = a
					break
				}
			}
			if target == nil {
				t.Fatalf("no open question about %q to answer", s.ask)
			}
			p := askuser.Decode(target.Payload)
			answers := map[string]any{}
			for _, q := range p.Questions {
				one := map[string]any{"other": s.answer}
				for _, o := range q.Options {
					if strings.Contains(strings.ToLower(o.Label+" "+o.ID), strings.ToLower(s.answer)) {
						one = map[string]any{"selected": []string{o.ID}}
						break
					}
				}
				answers[q.ID] = one
			}
			raw, _ := json.Marshal(map[string]any{"answers": answers, "general_note": s.note})
			fmt.Fprintf(&r.log, "PERSON ANSWERS %s: %s (note: %s)\n", target.ID, s.answer, s.note)
			if err := conn.Send(Command{Op: OpAnswer, ClientID: id, AskID: target.ID, Answer: raw, Device: "live"}); err != nil {
				t.Fatal(err)
			}
		}
		r.turn(8 * time.Minute)
	}
	for _, problem := range c.check(r) {
		t.Errorf("%s", problem)
	}
}
