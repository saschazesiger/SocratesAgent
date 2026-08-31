package agent

import (
	"context"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/term"
)

func TestNormaliseScreenIgnoresAnimation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		a, b    string
		wantSam bool
	}{
		{"a braille spinner turning", "⠋ Thinking (3s · ↓ 120 tokens)", "⠙ Thinking (4s · ↓ 340 tokens)", true},
		{"an ascii spinner turning", "| building", "/ building", true},
		{"a circle spinner turning", "◐ working", "◓ working", true},
		{"an elapsed counter ticking", "esc to interrupt (12s)", "esc to interrupt (1m 4s)", true},
		{"a bare number that is output", "found 12 s files", "found 40 s files", false},
		{"a progress bar filling", "■■⬝⬝⬝ working", "■■■■⬝ working", true},
		{"a token counter running", "⠋ Thinking (3s · ↓ 120 tokens)", "⠙ Thinking (5s · ↓ 900 tokens)", true},
		{"a version number", "opencode v1.2.3s", "opencode v1.2.4s", false},
		{"a line count growing", "+12 lines", "+40 lines", false},
		{"a file being read", "Read 120 lines", "Read 300 lines", false},
		{"trailing whitespace and blank lines", "ready>\n\n   ", "ready>", true},
		{"real output arriving", "⠋ Thinking (3s)", "⠙ Wrote main.go (4s)", false},
		{"a prompt appearing", "⠋ working", "⠙ working\nDone. Anything else?", false},
		{"a claude status line", "✶ Marinating… (2s · ↓ 2 tokens)", "✻ Simmering… (9s · ↓ 40 tokens)", false},
		{"a path is not a spinner", "src/main.go", "srcmain.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			same := normaliseScreen(tc.a) == normaliseScreen(tc.b)
			if same != tc.wantSam {
				t.Fatalf("normalised equal = %v, want %v\n a: %q -> %q\n b: %q -> %q",
					same, tc.wantSam, tc.a, normaliseScreen(tc.a), tc.b, normaliseScreen(tc.b))
			}
		})
	}
}

// openScript starts a shell script in a real session, the way a skill would be
// started, and returns its handle.
func openScript(t *testing.T, engine *Engine, script string) *term.Handle {
	t.Helper()
	handle, err := engine.Terminals.Open(context.Background(), "chat-wait", "fake", term.Spec{
		Command: "sh", Args: []string{"-c", script}, Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Terminals.Close(ctx, handle.ID(), time.Second)
	})
	return handle
}

// A program that pauses mid-generation is the whole bug: it prints nothing for
// seconds while it thinks, and the old wait called that "finished".
const pausingScript = `printf 'thinking\r\nesc to interrupt\r\n'; sleep 4; ` +
	`printf '\033[2J\033[Hall done\r\nready>\r\n'; sleep 60`

func TestWaitQuietKeepsWaitingWhileTheProgramSaysItIsWorking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	engine, _ := newTestEngine(t, &mockRouter{}, nil)
	handle := openScript(t, engine, pausingScript)

	busy := regexp.MustCompile("esc to interrupt")
	start := time.Now()
	result := waitQuiet(context.Background(), handle, busy, time.Second, 30*time.Second)
	elapsed := time.Since(start)

	if result.Label != waitIdle {
		t.Fatalf("wait ended as %q after %s, want idle", result.Label, elapsed)
	}
	if elapsed < 4*time.Second {
		t.Fatalf("returned idle after %s, before the program stopped saying it was working", elapsed)
	}
	if screen := handle.State().Screen; !strings.Contains(screen, "all done") {
		t.Fatalf("idle was reported on a half finished screen: %q", screen)
	}
}

// Without a busy pattern nothing changes: a quiet program is a finished
// program, which is what every ad hoc command relies on.
func TestWaitQuietWithoutABusyPatternReturnsOnSilence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	engine, _ := newTestEngine(t, &mockRouter{}, nil)
	handle := openScript(t, engine, pausingScript)

	start := time.Now()
	result := waitQuiet(context.Background(), handle, nil, time.Second, 30*time.Second)
	elapsed := time.Since(start)
	if result.Label != waitIdle {
		t.Fatalf("wait ended as %q, want idle", result.Label)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("took %s to call a silent program idle", elapsed)
	}
}

func TestWaitQuietReportsStillWorkingOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	engine, _ := newTestEngine(t, &mockRouter{}, nil)
	handle := openScript(t, engine, `printf 'esc to interrupt\r\n'; sleep 60`)

	busy := regexp.MustCompile("esc to interrupt")
	result := waitQuiet(context.Background(), handle, busy, time.Second, 2*time.Second)
	if result.Label != waitBusy {
		t.Fatalf("wait ended as %q, want %q", result.Label, waitBusy)
	}
	if result.Pattern != "esc to interrupt" {
		t.Fatalf("the busy pattern was not reported back: %q", result.Pattern)
	}
}

// The tool result is what the model actually reads, so the labels matter as
// much as the timing.
func TestTerminalWaitLabelsAStillWorkingProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"skill":"busy-fake"}`),
		sseToolCall("terminal_wait", `{"session":"SESSION","seconds":3,"quiet_seconds":1}`),
		sseText("I waited."),
	}}
	router.rewriteSession = true
	engine, st := newTestEngine(t, router, []config.Skill{busySkill(`printf 'esc to interrupt\r\n'; sleep 60`)})

	chat := &store.Chat{ID: "chat-wait-label"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "drive it"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	payload := lastPayload(t, router, 2)
	for _, want := range []string{"Status: still working", "esc to interrupt", "call terminal_wait again"} {
		if !strings.Contains(payload, want) {
			t.Errorf("the wait result is missing %q: %s", want, payload)
		}
	}
	if strings.Contains(payload, "Status: idle") {
		t.Errorf("a busy program was reported as idle: %s", payload)
	}
}

func busyText(p string) *string { return &p }

// busySkill is a skill whose program says it is working the way the real
// coding agents do.
func busySkill(script string) config.Skill {
	return config.Skill{
		ID: "busy-fake", Name: "Busy Fake", Enabled: true,
		Description: "a program that says when it is working",
		Command:     "sh", Args: []string{"-c", script},
		BusyPattern: busyText("esc to interrupt"),
		IdleSeconds: 1, TimeoutSeconds: 60,
	}
}

// The guard the user asked for: an answer produced while the program is still
// generating is not sent, the model is told to wait, and the run goes on.
func TestPrematureAnswerIsHeldWhileTheProgramWorks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"skill":"busy-fake"}`),
		sseText("It is finished, here is the result."),
		sseText("Still finished, honestly."),
		sseText("Really, it is done."),
		sseText("Fine - here is what I have."),
	}}
	router.rewriteSession = true
	engine, st := newTestEngine(t, router, []config.Skill{busySkill(`printf 'esc to interrupt\r\n'; sleep 60`)})

	chat := &store.Chat{ID: "chat-hold"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "ask it something"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	messages, err := st.ListMessages(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	var answers []string
	for _, m := range messages {
		if m.Role == "assistant" {
			answers = append(answers, m.Content)
		}
	}
	if len(answers) != 1 {
		t.Fatalf("the chat got %d assistant messages, want 1: %q", len(answers), answers)
	}
	if answers[0] != "Fine - here is what I have." {
		t.Fatalf("the held answer reached the user: %q", answers[0])
	}
	// The model was told why, in a way it can act on.
	payload := lastPayload(t, router, 2)
	if !strings.Contains(payload, "is still working") || !strings.Contains(payload, "terminal_wait") {
		t.Fatalf("the model was not sent back to the terminal: %s", payload)
	}
	if router.calls() != 5 {
		t.Fatalf("the model was called %d times, want 5 (one open, three holds, one answer)", router.calls())
	}
}

// A program with no busy pattern, sitting at its prompt, must not be held: the
// guard may never turn an ordinary answer into a stuck run.
func TestAnswerIsNotHeldWhenTheProgramIsQuiet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"skill":"quiet-fake"}`),
		sseText("The program is ready."),
	}}
	router.rewriteSession = true
	engine, st := newTestEngine(t, router, []config.Skill{{
		ID: "quiet-fake", Name: "Quiet Fake", Enabled: true,
		Description: "a program that just waits",
		Command:     "sh", Args: []string{"-c", `printf 'ready> '; sleep 60`},
		IdleSeconds: 1, TimeoutSeconds: 60,
	}})

	chat := &store.Chat{ID: "chat-noholds"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "start it"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	messages, _ := st.ListMessages(chat.ID)
	last := messages[len(messages)-1]
	if last.Role != "assistant" || last.Content != "The program is ready." {
		t.Fatalf("the answer was held for a quiet program: %#v", last)
	}
	if router.calls() != 2 {
		t.Fatalf("the model was called %d times, want 2", router.calls())
	}
}

// The shipped busy patterns are what makes this work out of the box, and the
// screens they have to survive are the ones captured from the real programs:
// Codex's trust dialog explains that "Working with untrusted contents comes
// with higher risk", and with --no-alt-screen its transcript keeps every line
// the model ever printed.
func TestPresetBusyPatterns(t *testing.T) {
	idle := map[string][]string{
		"claude": {
			"❯ Try \"edit <filepath> to...\"\n⏵⏵ auto mode on (shift+tab to cycle)          ◐ medium · /effort",
		},
		"codex": {
			"> You are in /tmp/project\n\n  Do you trust the contents of this directory? Working with untrusted\n" +
				"  contents comes with higher risk of prompt injection.\n\n› 1. Yes, continue\n  2. No, quit",
			"› Working on the refactor now.\n\n› \ngpt-5.6-sol high · /tmp/project",
			"• Working tree is clean.\n\n› \ngpt-5.6-sol high · /tmp/project",
		},
		"opencode": {
			"Ask anything...\nBuild · Big Pickle · OpenCode Zen\ntab agents   ctrl+p commands",
		},
	}
	working := map[string][]string{
		"claude": {
			"✶ Marinating… (12s · ↓ 340 tokens)\n\n❯ \n" +
				"⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt          ◐ medium · /effort\n/rc",
		},
		"codex":    {"• Working (12s • Esc to interrupt)\n\n› \ngpt-5.6-sol high · /tmp/project"},
		"opencode": {"⬝⬝■■■■■■  esc interrupt                     tab agents  ctrl+p commands"},
	}
	for _, preset := range config.Presets() {
		busy := preset.Busy()
		if busy == nil {
			t.Errorf("preset %s has no usable busy pattern (%q)", preset.ID, preset.BusyText())
			continue
		}
		for _, screen := range idle[preset.ID] {
			if busyNow(screen, busy) {
				t.Errorf("preset %s calls an idle screen busy: %q matches\n%s", preset.ID, busy, screen)
			}
		}
		for _, screen := range working[preset.ID] {
			if !busyNow(screen, busy) {
				t.Errorf("preset %s misses its own busy screen: %q does not match\n%s", preset.ID, busy, screen)
			}
		}
	}
}

// The hint lives in a footer, so only the bottom of the screen is searched
// for it. A program printing a file that happens to contain the words is not
// working - it is quoting.
func TestBusyPatternOnlyLooksAtTheFooter(t *testing.T) {
	busy := regexp.MustCompile("esc to interrupt")
	quoted := "❯ show me docs/driving.md\n\n" +
		"  The footer says \"esc to interrupt\" while it is generating.\n" +
		"  Line 2 of the file\n  Line 3 of the file\n  Line 4 of the file\n" +
		"  Line 5 of the file\n  Line 6 of the file\n  Line 7 of the file\n" +
		"  Line 8 of the file\n  Line 9 of the file\n\n❯ \n" +
		"⏵⏵ auto mode on (shift+tab to cycle)          ◐ medium · /effort"
	if busyNow(quoted, busy) {
		t.Errorf("quoted file content was read as a busy footer:\n%s", quoted)
	}
	// Two lines up from the bottom is still a footer.
	live := quoted[:len(quoted)-len("          ◐ medium · /effort")] + " · esc to interrupt\n/rc"
	if !busyNow(live, busy) {
		t.Errorf("the real footer hint was missed:\n%s", live)
	}
}

// A busy hint over a screen that has not moved in a long time is far more
// likely to be a dialog than a program that is thinking, and a wait must not
// sit on it until the timeout.
func TestWaitQuietGivesUpOnAFrozenBusyScreen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	engine, _ := newTestEngine(t, &mockRouter{}, nil)
	handle := openScript(t, engine, `printf 'esc to interrupt\r\n'; sleep 60`)

	previous := busyFreeze
	busyFreeze = 2 * time.Second
	t.Cleanup(func() { busyFreeze = previous })

	start := time.Now()
	result := waitQuiet(context.Background(), handle, regexp.MustCompile("esc to interrupt"),
		time.Second, 30*time.Second)
	if result.Label != waitBusy {
		t.Fatalf("wait ended as %q, want %q", result.Label, waitBusy)
	}
	if result.Frozen < 2*time.Second {
		t.Fatalf("the freeze was not reported: %#v", result)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("waited %s on a frozen screen instead of giving up", elapsed)
	}
}

// A wait shorter than the skill's quiet window used to be a guaranteed "still
// working"; the quiet window follows the wait instead.
func TestShortWaitCanStillReportIdle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	engine, st := newTestEngine(t, &mockRouter{}, nil)
	chat := &store.Chat{ID: "chat-clamp"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	handle, err := engine.Terminals.Open(context.Background(), chat.ID, "quiet", term.Spec{
		Command: "sh", Args: []string{"-c", `printf 'ready> '; sleep 60`}, Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Terminals.Close(ctx, handle.ID(), time.Second)
	})
	time.Sleep(time.Second)

	out := engine.execTerminalWait(context.Background(), chat,
		`{"session":"`+handle.ID()+`","seconds":2,"quiet_seconds":30}`)
	if !strings.Contains(out, "Status: idle") {
		t.Fatalf("a two second wait could not report idle: %s", out)
	}
}

// A dialog that keeps the busy hint in its footer - Claude Code does exactly
// that while asking for permission - must not hold the answer back forever.
func TestFrozenScreenDoesNotHoldTheAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	engine, _ := newTestEngine(t, &mockRouter{}, nil)
	handle := openScript(t, engine, `printf 'esc to interrupt\r\n'; sleep 60`)
	busy := regexp.MustCompile("esc to interrupt")
	time.Sleep(time.Second)

	if working, _ := stillWorking(handle, busy, time.Now(), time.Second); !working {
		t.Fatal("a busy screen that just changed was not treated as working")
	}
	long := time.Now().Add(-2 * busyFreeze)
	if working, _ := stillWorking(handle, busy, long, time.Second); working {
		t.Fatal("a screen frozen for minutes was still treated as working")
	}
	if working, _ := stillWorking(handle, nil, time.Now(), time.Second); working {
		t.Fatal("a session with no busy pattern was treated as working")
	}
}
