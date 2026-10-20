package harnesses

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// This file drives the e2e suite's fake CLI through a real tmux pane, under
// each of its three names, with argv and environment built by the real
// launchers.
//
// It is the one test in this package that starts a process, and it is here
// because the fake and the launchers are two halves of one claim: that what
// Socrates builds is what a terminal program actually receives. Everything the
// pane is told - the flags, the environment, the white background - is
// asserted from the other side of the PTY.

// The generated tmux configuration, in the shape termux writes it. It is
// repeated here rather than imported because termux is built on this package
// and cannot be imported back; what matters for this test is the window style,
// which is what answers the pane's colour query with nobody attached.
const fakeTUIConf = `set -g  status off
set -sg escape-time 0
set -g  default-terminal "screen-256color"
set -g  history-limit 2000
set -g  mouse on
setw -g aggressive-resize on
set -g  remain-on-exit on
set -g  destroy-unattached off
set -g  exit-empty off
set -g  allow-passthrough on
set -s  set-clipboard on
set -g  set-titles off
set -g  focus-events on
set -s  extended-keys on
set -as terminal-features 'xterm*:extkeys:clipboard'
set -g  remain-on-exit-format ''
set -g  window-style        'fg=#17181b,bg=#ffffff'
set -g  window-active-style 'fg=#17181b,bg=#ffffff'
`

var (
	fakeTUIOnce sync.Once
	fakeTUIDir  string
	fakeTUIErr  error
)

// fakeBinDir builds e2e/fakebin/faketui once per test run and links it under
// the three names the launchers look for.
func fakeBinDir(t *testing.T) string {
	t.Helper()
	fakeTUIOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			fakeTUIErr = err
			return
		}
		dir, err := os.MkdirTemp("", "faketui-")
		if err != nil {
			fakeTUIErr = err
			return
		}
		exe := filepath.Join(dir, "faketui")
		build := exec.Command("go", "build", "-o", exe, "./e2e/fakebin/faketui")
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			fakeTUIErr = err
			t.Logf("building the fake CLI: %s", out)
			return
		}
		for _, name := range []string{"claude", "codex", "opencode"} {
			if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
				fakeTUIErr = err
				return
			}
		}
		fakeTUIDir = dir
	})
	if fakeTUIErr != nil {
		t.Skipf("the fake CLI could not be built: %v", fakeTUIErr)
	}
	return fakeTUIDir
}

// tmuxLab is a tmux server on a socket of its own, killed when the test ends.
// It never touches the machine's own tmux.
type tmuxLab struct {
	t    *testing.T
	bin  string
	sock string
}

func newTmuxLab(t *testing.T) *tmuxLab {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed; skipping the fake CLI's pane test")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(conf, []byte(fakeTUIConf), 0o644); err != nil {
		t.Fatal(err)
	}
	lab := &tmuxLab{t: t, bin: bin, sock: filepath.Join(dir, "tmux.sock")}
	lab.run("-f", conf, "-u", "start-server")
	t.Cleanup(func() { _ = exec.Command(bin, "-S", lab.sock, "kill-server").Run() })
	return lab
}

func (l *tmuxLab) run(args ...string) string {
	l.t.Helper()
	full := append([]string{"-S", l.sock}, args...)
	if len(args) > 0 && args[0] == "-f" {
		full = append([]string{args[0], args[1], "-S", l.sock}, args[2:]...)
	}
	out, err := exec.Command(l.bin, full...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("tmux %s: %v: %s", strings.Join(full, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// start creates a session running exactly what a LaunchPlan says to run: argv
// as separate arguments, environment one -e at a time.
func (l *tmuxLab) start(name string, plan LaunchPlan) {
	l.t.Helper()
	args := []string{"new-session", "-d", "-s", name, "-x", "120", "-y", "40", "-c", plan.Cwd}
	for key, value := range plan.Env {
		args = append(args, "-e", key+"="+value)
	}
	args = append(args, plan.Argv...)
	l.run(args...)
}

func (l *tmuxLab) capture(name string) string {
	l.t.Helper()
	return l.run("capture-pane", "-p", "-t", name)
}

// waitForPane polls the pane until it says what the test is waiting for.
func (l *tmuxLab) waitForPane(name, want string) string {
	l.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	screen := ""
	for time.Now().Before(deadline) {
		screen = l.capture(name)
		if strings.Contains(screen, want) {
			return screen
		}
		time.Sleep(100 * time.Millisecond)
	}
	l.t.Fatalf("the pane never showed %q; it shows:\n%s", want, screen)
	return ""
}

// TestFakeTUIRunsUnderEveryName is the end-to-end check of this package: the
// three launchers build a command line, tmux runs it, and the program on the
// other side reports the flags and the background it was actually given.
func TestFakeTUIRunsUnderEveryName(t *testing.T) {
	bin := fakeBinDir(t)
	lab := newTmuxLab(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	// The fakes go first on PATH; the rest of it stays, because this test
	// still needs tmux and the toolchain.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	data := t.TempDir()
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeLog := filepath.Join(data, "fake.log")

	for _, h := range []Harness{Claude{}, Codex{}, OpenCode{}} {
		name := string(h.Kind())
		t.Run(name, func(t *testing.T) {
			settings := config.Default()
			req := PlanRequest{
				SessionID: "session" + name,
				Title:     "a session",
				Cwd:       cwd,
				Model:     "a-model",
				Settings:  settings,
				DataDir:   data,
			}
			plan, err := h.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			// The generated files are normally written by the substrate; here
			// the test stands in for it, because a session whose config file
			// is missing is not what is under test.
			for _, f := range plan.Files {
				if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.Path, f.Data, f.Mode); err != nil {
					t.Fatal(err)
				}
			}
			plan.Env["FAKE_LOG"] = fakeLog

			lab.start(name, plan)
			screen := lab.waitForPane(name, "FAKE "+name)

			// The white background, from the window style, with nobody
			// attached: this is the whole light-theme mechanism seen from
			// inside the pane.
			if !strings.Contains(screen, "theme=light") {
				t.Errorf("the pane was told the background is not white:\n%s", screen)
			}
			if !strings.Contains(strings.ReplaceAll(screen, "\n", ""), "cwd="+cwd) {
				t.Errorf("the program started somewhere else:\n%s", screen)
			}

			launch := lastLaunch(t, fakeLog, name)
			if got := strings.Join(launch.Argv, " "); got != strings.Join(plan.Argv[1:], " ") {
				t.Fatalf("the program received\n  %s\nand the launcher built\n  %s", got, strings.Join(plan.Argv[1:], " "))
			}
			for _, key := range []string{"COLORFGBG", "COLORTERM", "SOCRATES_SESSION"} {
				if launch.Env[key] != plan.Env[key] {
					t.Errorf("%s reached the program as %q, want %q", key, launch.Env[key], plan.Env[key])
				}
			}
			if name == "opencode" && launch.Env["OPENCODE_SERVER_PASSWORD"] != plan.Env["OPENCODE_SERVER_PASSWORD"] {
				t.Error("the server password did not reach the program")
			}
		})
	}
}

// fakeLaunch is one line of the fake's own log.
type fakeLaunch struct {
	Name string            `json:"name"`
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
}

func lastLaunch(t *testing.T, path, name string) fakeLaunch {
	t.Helper()
	var found fakeLaunch
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var launch fakeLaunch
				if json.Unmarshal([]byte(line), &launch) == nil && launch.Name == name {
					found = launch
				}
			}
		}
		if found.Name != "" {
			return found
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never wrote a launch record to %s", name, path)
	return found
}

// TestFakeTUIRefusesAReusedClaudeSessionID is the other half of
// TestClaudeRestartNeverReusesSessionID: the launcher never builds that
// command line, and this is what would happen if it did.
func TestFakeTUIRefusesAReusedClaudeSessionID(t *testing.T) {
	bin := fakeBinDir(t)
	lab := newTmuxLab(t)
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	id := "11111111-2222-3333-4444-555555555555"

	plan := LaunchPlan{
		Argv: []string{filepath.Join(bin, "claude"), "--session-id", id},
		Env:  map[string]string{"CLAUDE_CONFIG_DIR": configDir},
		Cwd:  home,
	}
	lab.start("first", plan)
	lab.waitForPane("first", "FAKE claude")

	lab.start("second", plan)
	lab.waitForPane("second", "Error: Session ID "+id+" is already in use.")
}

// bigPrompt is about fifty kilobytes of everything a shell would love to get
// wrong: quotes of both kinds, dollars, backticks, backslashes, a leading
// dash, a line that is only "--", and text in three scripts.
func bigPrompt() string {
	var b strings.Builder
	b.WriteString("-first line starts with a dash and says 'hi' \"there\"\n")
	for i := 0; b.Len() < 50<<10; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(" $HOME $(id) `whoami` \\n \\\\ ' \" ; | & > < * ? [a] {b} ~ # % ü ß 日本語 🙂\n")
		if i%100 == 0 {
			b.WriteString("--\n")
		}
	}
	b.WriteString("last line\n")
	return b.String()
}

// TestFakeTUIReceivesABigInitialPrompt sends a prompt far over tmux's sixteen
// kilobyte command limit through a real pane, under each of the three names,
// and checks the program received it byte for byte - which is the whole claim
// of the wrapper in prompt.go.
func TestFakeTUIReceivesABigInitialPrompt(t *testing.T) {
	bin := fakeBinDir(t)
	lab := newTmuxLab(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	data := t.TempDir()
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeLog := filepath.Join(data, "fake.log")
	prompt := bigPrompt()
	// Command substitution drops trailing newlines, and only those.
	want := strings.TrimRight(prompt, "\n")

	for _, h := range []Harness{Claude{}, Codex{}, OpenCode{}} {
		name := string(h.Kind())
		t.Run(name, func(t *testing.T) {
			req := PlanRequest{
				SessionID: "big" + name, Title: "a session", Cwd: cwd,
				Settings: config.Default(), DataDir: data, InitialPrompt: prompt,
			}
			plan, err := h.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range plan.Files {
				if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.Path, f.Data, f.Mode); err != nil {
					t.Fatal(err)
				}
			}
			plan.Env["FAKE_LOG"] = fakeLog

			lab.start(name, plan)
			lab.waitForPane(name, "prompt: -first line starts with a dash")

			raw, err := os.ReadFile(fakeLog)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Name   string `json:"name"`
				Prompt string `json:"prompt"`
			}
			for _, line := range strings.Split(string(raw), "\n") {
				var rec struct {
					Name   string `json:"name"`
					Prompt string `json:"prompt"`
				}
				if json.Unmarshal([]byte(line), &rec) == nil && rec.Name == name {
					got = rec
				}
			}
			if got.Prompt != want {
				t.Fatalf("%s received a prompt of %d bytes, want %d; they first differ at %d",
					name, len(got.Prompt), len(want), firstDiff(got.Prompt, want))
			}
			if _, err := os.Stat(SessionFile(data, req.SessionID, InitialPromptFile)); !os.IsNotExist(err) {
				t.Fatalf("the prompt file outlived the start: %v", err)
			}
			// exec, not a child: the pane's process is the program itself.
			if cmd := lab.run("display-message", "-p", "-t", name, "#{pane_current_command}"); cmd == "sh" {
				t.Fatalf("the pane is still the wrapper shell")
			}
		})
	}
}

func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestFakeTUIAsksTheWayClaudeCodeAsks launches the fake through the real
// Claude launcher and opens its question menu: the transcript it writes the
// question into is the one VerifyCLISession finds for the session id the
// launcher minted, which is how the server looks for a pending question too,
// and a digit typed into the pane is the answer FAKE_LOG and the transcript
// record.
func TestFakeTUIAsksTheWayClaudeCodeAsks(t *testing.T) {
	bin := fakeBinDir(t)
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	lab := newTmuxLab(t)

	data := t.TempDir()
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	req := PlanRequest{SessionID: "asking", Title: "a session", Cwd: cwd, Settings: config.Default(), DataDir: data}
	plan, err := Claude{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.Path, f.Data, f.Mode); err != nil {
			t.Fatal(err)
		}
	}
	plan.Argv[0] = filepath.Join(bin, "claude")
	fakeLog := filepath.Join(data, "fake.log")
	plan.Env["FAKE_LOG"] = fakeLog
	plan.Env["CLAUDE_CONFIG_DIR"] = configDir

	lab.start("asking", plan)
	lab.waitForPane("asking", "FAKE claude")
	lab.run("send-keys", "-t", "asking", "-l", "/question")
	lab.run("send-keys", "-t", "asking", "Enter")
	lab.waitForPane("asking", "Which database should we use?")

	req.CLISession = plan.CLISession
	if found, err := (Claude{}).VerifyCLISession(context.Background(), req); err != nil || !found {
		t.Fatalf("the transcript of %s is not where VerifyCLISession looks: %v %v", plan.CLISession, found, err)
	}
	transcript, err := filepath.Glob(filepath.Join(configDir, "projects", "*", plan.CLISession+".jsonl"))
	if err != nil || len(transcript) != 1 {
		t.Fatalf("transcripts %v: %v", transcript, err)
	}
	use := lastBlock(t, transcript[0])
	input, _ := json.Marshal(use["input"])
	if use["type"] != "tool_use" || use["name"] != "AskUserQuestion" || !strings.Contains(string(input), `"label":"SQLite"`) {
		t.Fatalf("the pending question was recorded as %v", use)
	}

	lab.run("send-keys", "-t", "asking", "2")
	lab.waitForPane("asking", "· Which database should we use? → SQLite")

	result := lastBlock(t, transcript[0])
	if result["type"] != "tool_result" || result["tool_use_id"] != use["id"] {
		t.Fatalf("the answer was recorded as %v, for %v", result, use["id"])
	}
	raw, err := os.ReadFile(fakeLog)
	if err != nil {
		t.Fatal(err)
	}
	var answered struct {
		Event   string `json:"event"`
		Answers []struct {
			Choices []string `json:"choices"`
		} `json:"answers"`
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var rec struct {
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Event == "answer" {
			_ = json.Unmarshal([]byte(line), &answered)
		}
	}
	if len(answered.Answers) != 1 || strings.Join(answered.Answers[0].Choices, ",") != "SQLite" {
		t.Fatalf("FAKE_LOG holds the answer %+v", answered)
	}
	// The launch reader still finds the launch, with an answer logged after it.
	if launch := lastLaunch(t, fakeLog, "claude"); len(launch.Argv) == 0 {
		t.Fatalf("the launch record was lost behind the answer: %+v", launch)
	}
}

// lastBlock is the last content block of a Claude transcript.
func lastBlock(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var last map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		var rec struct {
			Message struct {
				Content []map[string]any `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && len(rec.Message.Content) > 0 {
			last = rec.Message.Content[len(rec.Message.Content)-1]
		}
	}
	return last
}
