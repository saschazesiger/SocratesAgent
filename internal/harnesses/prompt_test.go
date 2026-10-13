package harnesses

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// promptPATH puts a stand-in for every CLI on PATH, so that the plans resolve a
// binary without a real one on this machine.
func promptPATH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex", "opencode"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
}

func promptRequest(t *testing.T, prompt string) PlanRequest {
	return PlanRequest{
		SessionID: "s1", Title: "t", Cwd: t.TempDir(), Settings: config.Default(),
		DataDir: t.TempDir(), InitialPrompt: prompt,
	}
}

// A fresh plan with a prompt runs the wrapper with the prompt's file, puts the
// marker where the program takes a prompt, and writes the file 0600 without
// recording its contents. Without a prompt nothing changes.
func TestFreshPlansCarryThePrompt(t *testing.T) {
	promptPATH(t)
	ctx := context.Background()
	for _, h := range []Harness{Claude{}, Codex{}, OpenCode{}} {
		t.Run(string(h.Kind()), func(t *testing.T) {
			req := promptRequest(t, "do the thing")
			plan, err := h.Plan(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			path := SessionFile(req.DataDir, req.SessionID, InitialPromptFile)
			if len(plan.Argv) < 6 || plan.Argv[0] != "/bin/sh" || plan.Argv[1] != "-c" ||
				plan.Argv[3] != "sh" || plan.Argv[4] != path {
				t.Fatalf("argv = %q", plan.Argv)
			}
			program := plan.Argv[5:]
			if filepath.Base(program[0]) != string(h.Kind()) {
				t.Fatalf("the wrapper does not exec the program: %q", program)
			}
			switch h.Kind() {
			case KindOpenCode:
				if program[len(program)-1] != req.Cwd || program[len(program)-2] != "--prompt="+promptMarker {
					t.Fatalf("opencode argv = %q", program)
				}
			default:
				if program[len(program)-2] != "--" || program[len(program)-1] != promptMarker {
					t.Fatalf("argv = %q", program)
				}
			}
			var file *GenFile
			for i := range plan.Files {
				if plan.Files[i].Path == path {
					file = &plan.Files[i]
				}
			}
			if file == nil || file.Mode != 0o600 || !file.Transient || !strings.HasPrefix(string(file.Data), "do the thing") {
				t.Fatalf("prompt file = %#v", file)
			}

			req.InitialPrompt = ""
			bare, err := h.Plan(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if bare.Argv[0] == "/bin/sh" {
				t.Fatalf("a plan without a prompt was wrapped: %q", bare.Argv)
			}
			for _, f := range bare.Files {
				if f.Transient {
					t.Fatalf("a plan without a prompt has a prompt file: %#v", f)
				}
			}
		})
	}
}

// A resume continues a conversation, and a prompt is the start of one: even
// a request that carries a prompt does not put it into a resume.
func TestResumePlansNeverCarryThePrompt(t *testing.T) {
	promptPATH(t)
	ctx := context.Background()
	for _, h := range []Harness{Claude{}, Codex{}, OpenCode{}} {
		req := promptRequest(t, "do the thing")
		req.CLISession = "11111111-2222-3333-4444-555555555555"
		plan, err := h.ResumePlan(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", h.Kind(), err)
		}
		if plan.Argv[0] == "/bin/sh" || strings.Contains(strings.Join(plan.Argv, " "), promptMarker) {
			t.Fatalf("%s resume carries the prompt: %q", h.Kind(), plan.Argv)
		}
	}
}

func TestClaudeSingleWordPromptIsNotASubcommand(t *testing.T) {
	if got := guardSingleWord("update"); got != "update " {
		t.Fatalf("guard(update) = %q", got)
	}
	if got := guardSingleWord("update the readme"); got != "update the readme" {
		t.Fatalf("guard changed a sentence: %q", got)
	}
	if got := guardSingleWord(""); got != "" {
		t.Fatalf("guard invented a prompt: %q", got)
	}
	// The wrapper's $(cat) strips trailing newlines, so a word followed by
	// Enter reaches the program as the bare word and has to be guarded too.
	for in, want := range map[string]string{
		"update\n":   "update ",
		"doctor\n\n": "doctor ",
		"fix this\n": "fix this\n",
		"\n":         "\n",
		"mcp \n":     "mcp \n",
	} {
		if got := guardSingleWord(in); got != want {
			t.Errorf("guard(%q) = %q, want %q", in, got, want)
		}
	}
}

// The wrapper itself, run by a real /bin/sh without tmux: every argument
// survives, the marker is replaced in both of its forms, and the file is gone
// afterwards.
func TestPromptScriptReplacesTheMarker(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p")
	prompt := "it's \"quoted\" $HOME `id` \\ \n-- second line ü 🙂"
	if err := os.WriteFile(file, []byte(prompt+"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// printf prints each argument on a line of its own, NUL separated so a
	// newline inside one is not a separator.
	args := []string{"-c", promptScript, "sh", file, "printf", `%s\0`, "a b", promptMarker, "--prompt=" + promptMarker, "$x"}
	out, err := exec.Command("/bin/sh", args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	want := []string{"a b", prompt, "--prompt=" + prompt, "$x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the prompt file is still there: %v", err)
	}
}
