package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The interactive path is the whole point, so it must stay open. `skill` is
// what the parameter is called now; `tool` is what it used to be called and is
// still accepted, so a model working from a cached prompt is not stranded.
func TestTerminalOpenStartsASkill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	for _, param := range []string{"skill", "tool"} {
		t.Run(param, func(t *testing.T) {
			binary := fakeClaude(t)
			router := &mockRouter{responses: []string{
				sseToolCall("terminal_open", `{"`+param+`":"claude","name":"claude test"}`),
				sseText("The session is up."),
			}}
			engine, st := newTestEngine(t, router, []config.Skill{{
				ID: "claude", Name: "Claude Code", Enabled: true,
				Description:     "a stand in for Claude Code",
				Command:         binary,
				Startup:         "wait for the ready prompt",
				InteractiveOnly: true, HoldReplyWhileBusy: true,
				IdleSeconds: 1, TimeoutSeconds: 60,
			}})

			chat := &store.Chat{ID: "chat-open-" + param}
			if err := st.CreateChat(chat); err != nil {
				t.Fatal(err)
			}
			run, err := engine.Start(Turn{ChatID: chat.ID, Text: "open claude", Auto: false})
			if err != nil {
				t.Fatal(err)
			}
			waitForRun(t, st, run.ID, store.RunDone)

			payload := lastPayload(t, router, 1)
			if !strings.Contains(payload, "ready>") {
				t.Errorf("the first screen never reached the model: %s", payload)
			}
			if !strings.Contains(payload, "wait for the ready prompt") {
				t.Errorf("the skill's startup section was not handed over: %s", payload)
			}
		})
	}
}

// fakeClaude writes a stand in for the real binary. It keeps running, the way
// a TUI does, so the session is still alive while the test drives it.
func fakeClaude(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf 'ready> '\nwhile IFS= read -r line; do printf 'you said: %s\\r\\nready> ' \"$line\"; done\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary
}

// modelSkill is the stand in used by the model tests: a program with two
// models to choose from and the same application mechanism Claude Code has.
func modelSkill(command string) config.Skill {
	return config.Skill{
		ID: "claude", Name: "Claude Code", Enabled: true,
		Description: "a stand in for Claude Code",
		Command:     command,
		ModelArgs:   []string{"--model", "{model}"},
		EffortArgs:  []string{"--effort", "{effort}"},
		Applying:    "The model is passed as --model <id> and the effort as --effort <level>.",
		Models: []config.ModelChoice{
			{ID: "sonnet", Effort: config.EffortMedium, UseWhen: "the everyday one"},
			{ID: "opus", Effort: config.EffortHigh, UseWhen: "the hard ones, worth being slow for"},
		},
		InteractiveOnly: true,
		IdleSeconds:     1, TimeoutSeconds: 60,
	}
}

// terminalDetail is what the process view recorded about the one session a run
// opened, which is also where the model and effort it was started on land.
func terminalDetail(t *testing.T, st *store.Store, chatID string) map[string]any {
	t.Helper()
	steps, err := st.ListSteps(chatID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Kind != store.StepTerminal {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(steps[i].Detail, &detail); err != nil {
			t.Fatalf("terminal step detail is not JSON (%v): %s", err, steps[i].Detail)
		}
		detail["__title"] = steps[i].Title
		return detail
	}
	t.Fatal("the run opened no terminal session")
	return nil
}

// Picking the model is the second half of picking a skill, so terminal_open
// has to start the program on the one the orchestrator asked for - and on the
// first of the list when it asked for none.
func TestTerminalOpenStartsTheChosenModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	cases := []struct {
		name    string
		args    string
		command string
		title   string
	}{
		{"chosen", `{"skill":"claude","model":"opus"}`, "--model opus --effort high", "Claude Code · opus"},
		{"default", `{"skill":"claude"}`, "--model sonnet --effort medium", "Claude Code · sonnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binary := fakeClaude(t)
			router := &mockRouter{responses: []string{
				sseToolCall("terminal_open", tc.args),
				sseText("The session is up."),
			}}
			engine, st := newTestEngine(t, router, []config.Skill{modelSkill(binary)})

			chat := &store.Chat{ID: "chat-model-" + tc.name}
			if err := st.CreateChat(chat); err != nil {
				t.Fatal(err)
			}
			run, err := engine.Start(Turn{ChatID: chat.ID, Text: "open it", Auto: false})
			if err != nil {
				t.Fatal(err)
			}
			waitForRun(t, st, run.ID, store.RunDone)

			detail := terminalDetail(t, st, chat.ID)
			command, _ := detail["command"].(string)
			if !strings.Contains(command, tc.command) {
				t.Errorf("started as %q, want it to carry %q", command, tc.command)
			}
			// The chosen model is part of what the user is watching, so it is
			// named in the process view rather than hidden in the argv.
			if title, _ := detail["__title"].(string); title != tc.title {
				t.Errorf("session label = %q, want %q", title, tc.title)
			}
			if got, _ := detail["model"].(string); got == "" {
				t.Errorf("the session does not record which model it runs on: %#v", detail)
			}
		})
	}
}

// A model that is not on the list would start the program on something nobody
// chose, or stop it starting at all, so it is refused with the list in hand.
func TestTerminalOpenRefusesAModelThatIsNotConfigured(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"skill":"claude","model":"gpt-9"}`),
		sseText("Could not open it."),
	}}
	engine, st := newTestEngine(t, router, []config.Skill{modelSkill("true")})
	chat := &store.Chat{ID: "chat-bad-model"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "open it", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	payload := lastPayload(t, router, 1)
	if !strings.Contains(payload, "no model called") || !strings.Contains(payload, "gpt-9") {
		t.Errorf("the model was not told what went wrong: %s", payload)
	}
	if !strings.Contains(payload, "sonnet, opus") {
		t.Errorf("the refusal did not name the models it could have had: %s", payload)
	}
	if len(engine.Terminals.List(chat.ID)) != 0 {
		t.Error("a session was opened anyway")
	}
}

// `model` names one of a skill's configured models, so it means nothing next
// to a raw command line - and saying so is better than quietly ignoring it.
func TestTerminalOpenRefusesAModelWithoutASkill(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"command":"echo hi","model":"opus"}`),
		sseText("Understood."),
	}}
	engine, st := newTestEngine(t, router, []config.Skill{modelSkill("true")})
	chat := &store.Chat{ID: "chat-model-no-skill"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "run it", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)
	if payload := lastPayload(t, router, 1); !strings.Contains(payload, "only means something together") {
		t.Errorf("the model was not told why that made no sense: %s", payload)
	}
}

// Choosing a model is a reading decision, so everything needed to make it -
// the ids, the efforts, the user's own sentence about each one, and how the
// two values reach the program - has to be in front of the orchestrator.
func TestSystemPromptAndToolDescribeTheModels(t *testing.T) {
	router := &mockRouter{responses: []string{sseText("Nothing to do.")}}
	engine, st := newTestEngine(t, router, []config.Skill{modelSkill("true")})
	chat := &store.Chat{ID: "chat-model-prompt"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "hello", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	payload := lastPayload(t, router, 0)
	for _, want := range []string{
		"**Models.**",
		"`sonnet`, effort medium - the everyday one",
		"`opus`, effort high - the hard ones, worth being slow for",
		"The model is passed as --model <id>",
		// The command line shown in the prompt is the one a session really
		// starts with, default model included.
		"--model sonnet --effort medium",
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("the system prompt is missing %q", want)
		}
	}

	// The same list has to be in the tool description, because that is what the
	// model reads when it is filling in the arguments.
	settings := engine.Settings()
	var open string
	for _, tool := range buildTools(settings) {
		if tool.Function.Name == toolTerminalOpen {
			open = tool.Function.Description
		}
	}
	for _, want := range []string{"model `opus`, effort high: the hard ones", "model `sonnet`, effort medium"} {
		if !strings.Contains(open, want) {
			t.Errorf("terminal_open does not offer %q:\n%s", want, open)
		}
	}
}
