package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The guard has to be sharp about coding agents and blind to everything else,
// so the table carries as many innocent command lines as guilty ones.
func TestHarnessGuardMatcher(t *testing.T) {
	rules := newHarnessRules(config.Default())

	cases := []struct {
		name     string
		command  string
		found    bool
		headless bool
	}{
		// Headless invocations, which are refused everywhere.
		{"print flag", `claude -p "fix the tests"`, true, true},
		{"long print flag", `claude --print "fix the tests"`, true, true},
		{"output format", `claude --output-format json "hi"`, true, true},
		{"output format joined", `claude --output-format=stream-json "hi"`, true, true},
		{"quoted flag", `claude "-p" x`, true, true},
		{"quoted flag value", `claude --print="x"`, true, true},
		{"quoted command", `"claude" -p x`, true, true},
		{"single quoted command", `'claude' -p x`, true, true},
		{"command quoted in the middle", `cl''aude -p x`, true, true},
		{"quoted subcommand", `codex "exec" x`, true, true},
		{"codex exec", `codex exec "explain this repo"`, true, true},
		{"opencode run", `opencode run "explain this repo"`, true, true},
		{"non interactive", `claude --non-interactive "hi"`, true, true},
		{"quiet", `opencode -q "hi"`, true, true},
		{"json", `codex --json "hi"`, true, true},
		{"piped prompt", `echo "fix it" | claude`, true, true},
		{"prompt from a file", `claude < prompt.txt`, true, true},
		{"here string", `claude <<< "fix it"`, true, true},
		{"after a successful build", `npm run build && codex exec "review"`, true, true},
		{"in a list", `cd /srv; claude -p hi`, true, true},
		{"command substitution", `answer=$(claude -p "hi")`, true, true},
		{"substitution inside a string", `result="$(claude -p x)"`, true, true},
		{"substitution in a commit message", `git commit -m "$(claude -p 'msg')"`, true, true},
		{"backticks", "note=`claude -p x`", true, true},
		{"behind sudo", `sudo -E claude --print hi`, true, true},
		{"behind env", `env FOO=1 claude -p hi`, true, true},
		{"with a leading assignment", `FOO=1 claude -p hi`, true, true},
		{"absolute path", `/usr/local/bin/claude -p hi`, true, true},
		{"through bash -c", `bash -c "claude -p x"`, true, true},
		{"through sh -lc", `sh -lc 'claude -p x'`, true, true},
		{"through eval", `eval "claude -p x"`, true, true},
		{"through xargs", `ls | xargs -n 1 claude -p`, true, true},
		{"behind timeout", `timeout 10 claude -p x`, true, true},
		{"behind nice", `nice -n 5 claude --print x`, true, true},
		{"behind a stderr redirect", `claude 2>&1 -p x`, true, true},
		{"behind a merged redirect", `claude &> log.txt -p x`, true, true},
		{"in a for loop", `for f in *; do claude -p $f; done`, true, true},
		{"in a conditional", `if true; then claude -p x; fi`, true, true},
		{"after a model flag", `codex --model o3 exec x`, true, true},
		{"after a short model flag", `codex -m o3 exec x`, true, true},
		{"after a bracket in a message", `git commit -m "add feature (wip"; claude -p x`, true, true},

		// The bare agent: fine in a terminal, refused in the shell tool.
		{"bare claude", `claude`, true, false},
		{"claude with a model", `claude --model sonnet`, true, false},
		{"codex in a subdirectory", `cd api && codex`, true, false},

		// Everything else has to pass untouched.
		{"grep for the word", `grep claude file.go`, false, false},
		{"commit message", `git commit -m "run codex"`, false, false},
		{"npm script called run", `npm run codex`, false, false},
		{"a file named after it", `cat docs/opencode.md`, false, false},
		{"print flag on another program", `ls -p`, false, false},
		{"quoted path", `rg "claude -p" internal/`, false, false},
		{"single quoted", `echo 'claude -p hi'`, false, false},
		{"a python module", `python3 -m claude_helper --print`, false, false},
		{"writing to a file named claude", `echo hi > claude`, false, false},
		{"an exec subcommand of something else", `docker exec -it box sh`, false, false},
		{"plain build", `go test ./...`, false, false},
		{"a bracket in a commit message", `git commit -m "add feature (wip)"`, false, false},
		{"an unbalanced bracket in a message", `git commit -m "add feature (wip"`, false, false},
		{"version check", `claude --version`, false, false},
		{"short version check", `codex -v`, false, false},
		{"help", `opencode --help`, false, false},
		{"short help", `claude -h`, false, false},
		{"looking it up", `command -v claude`, false, false},
		{"looking it up with type", `type claude`, false, false},
		{"looking it up with which", `which claude`, false, false},

		// Nonsense must be survived rather than parsed.
		{"empty", ``, false, false},
		{"a lone quote", `'`, false, false},
		{"an unfinished escape", `"\`, false, false},
		{"an unfinished substitution", `$(`, false, false},
		{"only separators", `;;;`, false, false},
		{"unterminated argument", `claude -p x "a`, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			use := findHarnessUse(tc.command, rules)
			if (use != nil) != tc.found {
				t.Fatalf("findHarnessUse(%q) = %+v, wanted found=%v", tc.command, use, tc.found)
			}
			shell := guardShellCommand(tc.command, rules)
			terminal := guardTerminalCommand(tc.command, rules)
			if use == nil {
				// Nothing found means both tools have to let it through.
				if shell != "" {
					t.Errorf("shell_run refused %q: %s", tc.command, shell)
				}
				if terminal != "" {
					t.Errorf("terminal_open refused %q: %s", tc.command, terminal)
				}
				return
			}
			if use.headless != tc.headless {
				t.Fatalf("findHarnessUse(%q) headless = %v, wanted %v (reason %q)",
					tc.command, use.headless, tc.headless, use.reason)
			}

			// The shell tool refuses either way; a terminal only refuses the
			// headless spelling.
			if shell == "" {
				t.Errorf("shell_run allowed %q", tc.command)
			}
			if tc.headless && terminal == "" {
				t.Errorf("terminal_open allowed the headless %q", tc.command)
			}
			if !tc.headless && terminal != "" {
				t.Errorf("terminal_open refused the interactive %q: %s", tc.command, terminal)
			}
		})
	}

	// A command that is not an agent must get past both guards.
	for _, cmd := range []string{`grep claude file.go`, `git commit -m "run codex"`} {
		if refusal := guardShellCommand(cmd, rules); refusal != "" {
			t.Errorf("shell_run refused %q: %s", cmd, refusal)
		}
	}
}

// A tool renamed in the admin dashboard is still guarded, because the guard
// looks at the command it runs rather than at its id.
func TestHarnessGuardFollowsTheConfiguredCommand(t *testing.T) {
	settings := config.Default()
	settings.Tools = []config.Tool{{
		ID: "implementer", Name: "Implementer", Enabled: true, Command: "/opt/bin/claude",
	}}
	rules := newHarnessRules(settings)

	refusal := guardShellCommand(`claude -p hi`, rules)
	if !strings.Contains(refusal, `tool: "implementer"`) {
		t.Errorf("the refusal did not point at the configured tool: %s", refusal)
	}
}

// The shell tool is the hole the model kept reaching through, so it has to
// refuse before anything runs and say what to do instead.
func TestShellRunRefusesAHeadlessCodingAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("shell_run", `{"command":"claude -p hello"}`),
		sseText("Understood, opening a session instead."),
	}}
	engine, st := newTestEngine(t, router, nil)

	chat := &store.Chat{ID: "chat-guard"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "ask claude something", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	payload := lastPayload(t, router, 1)
	for _, want := range []string{"Refusing to run claude", "terminal_open", `tool: \"claude\"`} {
		if !strings.Contains(payload, want) {
			t.Errorf("the refusal is missing %q: %s", want, payload)
		}
	}

	// Nothing may actually have run.
	steps, err := st.ListSteps(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.Kind == store.StepShell {
			t.Fatalf("the command was run anyway: %#v", s)
		}
	}
}

// The interactive path is the whole point, so it must stay open.
func TestTerminalOpenStillStartsTheCodingAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	binary := fakeClaude(t)

	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"tool":"claude","name":"claude · guard"}`),
		sseText("The session is up."),
	}}
	engine, st := newTestEngine(t, router, []config.Tool{{
		ID: "claude", Name: "Claude Code", Enabled: true,
		Description: "a stand in for Claude Code",
		Command:     binary,
		Driving:     "type and press enter",
		IdleSeconds: 1, TimeoutSeconds: 60,
	}})

	chat := &store.Chat{ID: "chat-open"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "open claude", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	payload := lastPayload(t, router, 1)
	if strings.Contains(payload, "Refusing") {
		t.Fatalf("the guard blocked the interactive path: %s", payload)
	}
	if !strings.Contains(payload, "ready>") {
		t.Errorf("the first screen never reached the model: %s", payload)
	}
}

// fakeClaude writes a stand in for the real binary, named `claude` so the guard
// sees the same command it would refuse in the shell. It keeps running, the way
// a TUI does, so the session is still alive when a question is asked.
func fakeClaude(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf 'ready> '\nwhile IFS= read -r line; do printf 'you said: %s\\r\\nready> ' \"$line\"; done\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary
}

// A question relayed from a coding agent has to say so. The user sees "Claude
// Code asks:" and can tell it apart from something Socrates wants to know.
func TestAskNamesTheAsker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	tools := []config.Tool{{
		ID: "claude", Name: "Claude Code", Enabled: true,
		Description: "a stand in for Claude Code",
		Command:     fakeClaude(t),
		Driving:     "type and press enter",
		IdleSeconds: 1, TimeoutSeconds: 60,
	}}

	cases := []struct {
		name    string
		replies []string
		want    string
	}{
		{
			name: "agent",
			replies: []string{
				sseToolCall("terminal_open", `{"tool":"claude","name":"claude"}`),
				sseToolCall("ask_user", `{"question":"Which one?","options":[{"label":"Left"}]}`),
				sseText("Going left then."),
			},
			want: "Claude Code",
		},
		{
			name: "socrates",
			replies: []string{
				sseToolCall("ask_user", `{"question":"Which one?","options":[{"label":"Left"}]}`),
				sseText("Going left then."),
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &mockRouter{responses: tc.replies}
			engine, st := newTestEngine(t, router, tools)
			chat := &store.Chat{ID: "chat-source"}
			if err := st.CreateChat(chat); err != nil {
				t.Fatal(err)
			}
			run, err := engine.Start(Turn{ChatID: chat.ID, Text: "decide for me", Auto: false})
			if err != nil {
				t.Fatal(err)
			}
			waitForRun(t, st, run.ID, store.RunWaiting)

			question, err := st.PendingQuestion(chat.ID)
			if err != nil {
				t.Fatalf("pending question: %v", err)
			}
			if question.Source != tc.want {
				t.Errorf("question source = %q, wanted %q", question.Source, tc.want)
			}
			if err := engine.Answer(question.ID, "Left"); err != nil {
				t.Fatal(err)
			}
			waitForRun(t, st, run.ID, store.RunDone)

			// The attribution has to survive a reload, which reads the step.
			steps, err := st.ListSteps(chat.ID)
			if err != nil {
				t.Fatal(err)
			}
			var detail []byte
			for _, step := range steps {
				if step.Kind == store.StepQuestion {
					detail = step.Detail
				}
			}
			if len(detail) == 0 {
				t.Fatal("the question was not recorded in the process view")
			}
			var fields map[string]any
			if err := json.Unmarshal(detail, &fields); err != nil {
				t.Fatalf("step detail is not JSON: %v", err)
			}
			if got, _ := fields["source"].(string); got != tc.want {
				t.Errorf("step detail source = %q, wanted %q (detail %s)", got, tc.want, detail)
			}
		})
	}
}

// Stopping a run has to take the question panel down. Only a question event
// does that, so the cancellation is published as well as stored.
func TestStopPublishesTheCancelledQuestion(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("ask_user", `{"question":"Which one?"}`),
	}}
	engine, st := newTestEngine(t, router, nil)
	chat := &store.Chat{ID: "chat-cancel"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	id, events := engine.Bus.Subscribe(chat.ID)
	defer engine.Bus.Unsubscribe(chat.ID, id)

	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "decide for me", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunWaiting)
	if !engine.Stop(chat.ID) {
		t.Fatal("the run was not stopped")
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case raw, ok := <-events:
			if !ok {
				t.Fatal("the event stream closed without a cancelled question")
			}
			var ev struct {
				Type     string          `json:"type"`
				Question *store.Question `json:"question"`
			}
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("event is not JSON: %v", err)
			}
			if ev.Type != "question" || ev.Question == nil {
				continue
			}
			if ev.Question.Status == store.StatusCancelled {
				return
			}
		case <-deadline:
			t.Fatal("no cancelled question event was published")
		}
	}
}
