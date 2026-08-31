package agent

import (
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
