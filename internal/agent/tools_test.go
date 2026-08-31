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
				Description: "a stand in for Claude Code",
				Command:     binary,
				Startup:     "wait for the ready prompt",
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
	skills := []config.Skill{{
		ID: "claude", Name: "Claude Code", Enabled: true,
		Description:  "a stand in for Claude Code",
		Command:      fakeClaude(t),
		GivingTasks:  "type and press enter",
		ReadingState: "it is done when the prompt comes back",
		IdleSeconds:  1, TimeoutSeconds: 60,
	}}

	cases := []struct {
		name    string
		replies []string
		want    string
	}{
		{
			name: "agent",
			replies: []string{
				sseToolCall("terminal_open", `{"skill":"claude","name":"claude"}`),
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
			engine, st := newTestEngine(t, router, skills)
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
