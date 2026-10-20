package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests hold the fake's question menus to the real ones in two halves.
// The first drives the menu model with keys and no terminal at all, which is
// where every rule verified against Claude Code 2.1.303 is pinned down one at
// a time. The second runs the built program in a tmux pane on a socket of its
// own and types into it with send-keys, the way the answer operator will,
// asserting on the screen, the status file, the transcript and FAKE_LOG.

func text(s string) []key {
	keys := make([]key, 0, len(s))
	for i := 0; i < len(s); i++ {
		keys = append(keys, key{kind: keyText, b: s[i]})
	}
	return keys
}

func pressAll(q *question, keys ...[]key) outcome {
	result := outcomeNone
	for _, group := range keys {
		for _, k := range group {
			result = q.press(k)
		}
	}
	return result
}

func one(kind keyKind) []key { return []key{{kind: kind}} }

func TestKeyDecoderReadsArrowsAndALoneEscape(t *testing.T) {
	var d keyDecoder
	var got []keyKind
	for _, b := range []byte("\x1b[A\x1bOB\x1b[1;2C\x1b[Z\x1b[Dx\r\t\x7f") {
		for _, k := range d.feed(b) {
			got = append(got, k.kind)
		}
	}
	want := []keyKind{keyUp, keyDown, keyRight, keyBackTab, keyLeft, keyText, keyEnter, keyTab, keyBackspace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %v, want %v", got, want)
	}

	// An ESC that ends a read is held: it may be the first byte of an arrow
	// whose rest is in the next read.
	if keys := d.feed(0x1b); keys != nil || !d.pending() {
		t.Fatalf("a lone ESC was decided at once: %v", keys)
	}
	if keys := d.feed('['); keys != nil {
		t.Fatalf("ESC [ was decided before its final byte: %v", keys)
	}
	if keys := d.feed('B'); len(keys) != 1 || keys[0].kind != keyDown {
		t.Fatalf("an arrow split across reads decoded as %v", keys)
	}
	// And when nothing follows, the wait running out makes it the Esc key.
	d.feed(0x1b)
	if keys := d.flush(); len(keys) != 1 || keys[0].kind != keyEscape || d.pending() {
		t.Fatalf("a lone ESC flushed as %v", keys)
	}
	// ESC followed by a byte that starts no sequence is both keys.
	d.feed(0x1b)
	if keys := d.feed('q'); len(keys) != 2 || keys[0].kind != keyEscape || keys[1].b != 'q' {
		t.Fatalf("ESC q decoded as %v", keys)
	}
}

func TestSingleQuestionKeys(t *testing.T) {
	single := func() *question { return newQuestion("single", []questionItem{databaseQuestion}, true) }

	q := single()
	if got := q.press(text("2")[0]); got != outcomeAnswered {
		t.Fatalf("a digit did not answer at once: %v", got)
	}
	if got := q.answers()[0]; !reflect.DeepEqual(got.Choices, []string{"SQLite"}) || got.Text != "" {
		t.Fatalf("answered %+v", got)
	}

	q = single()
	if got := pressAll(q, one(keyDown), one(keyEnter)); got != outcomeAnswered || q.answers()[0].line() != "SQLite" {
		t.Fatalf("Down and Enter answered %v with %q", got, q.answers()[0].line())
	}

	// The free-text row: its digit only moves the cursor there, and from then
	// on every byte is text, digits included.
	q = single()
	if got := q.press(text("3")[0]); got != outcomeRepaint || q.cursor[0] != 2 {
		t.Fatalf("the free row's digit gave %v with the cursor on %d", got, q.cursor[0])
	}
	if strings.Contains(q.claudeScreen(), "❯ 3. Type something.") == false {
		t.Fatalf("the cursor is not drawn on the free row:\n%s", q.claudeScreen())
	}
	pressAll(q, text("MySQL 89"), one(keyBackspace))
	if !strings.Contains(q.claudeScreen(), "❯ 3. MySQL 8\r\n") {
		t.Fatalf("the typed text is not drawn in place of the label:\n%s", q.claudeScreen())
	}
	if got := q.press(key{kind: keyEnter}); got != outcomeAnswered {
		t.Fatalf("Enter on the free row gave %v", got)
	}
	if got := q.answers()[0]; len(got.Choices) != 0 || got.Text != "MySQL 8" {
		t.Fatalf("answered %+v", got)
	}

	// Letters anywhere else are lost, Enter on an empty free row does
	// nothing, and the last number is "Chat about this", which leaves.
	q = single()
	if got := pressAll(q, text("abc")); got != outcomeNone {
		t.Fatalf("letters on an option row gave %v", got)
	}
	if got := pressAll(q, one(keyDown), one(keyDown), one(keyEnter)); got != outcomeNone {
		t.Fatalf("Enter on an empty free row gave %v", got)
	}
	q.press(key{kind: keyUp})
	if got := q.press(text("4")[0]); got != outcomeDeclined || !q.toChat {
		t.Fatalf("Chat about this gave %v", got)
	}

	if got := single().press(key{kind: keyEscape}); got != outcomeDeclined {
		t.Fatalf("Esc gave %v", got)
	}
}

func TestMultiSelectKeys(t *testing.T) {
	q := newQuestion("multi", []questionItem{featuresQuestion}, true)
	pressAll(q, text("13"))
	// The free row's digit ticks it and leaves the cursor where it is, so
	// what is typed next is lost: the gotcha of the real menu.
	pressAll(q, text("4"), text("lost"))
	if q.cursor[0] != 0 || q.free[0] != "" || !q.checked[0][3] {
		t.Fatalf("the free row's digit: cursor %d, text %q, ticked %v", q.cursor[0], q.free[0], q.checked[0][3])
	}
	pressAll(q, text("4"), one(keyDown), one(keyDown), one(keyDown), text("Offline sync v2"))
	screen := q.claudeScreen()
	for _, want := range []string{"  1. [✔] Dark mode\r\n", "  2. [ ] Search\r\n", "  3. [✔] Export\r\n",
		"❯ 4. [✔] Offline sync v2\r\n", "\r\n     Submit\r\n", "  5. Chat about this"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the multi-select tab lacks %q:\n%s", want, screen)
		}
	}
	if got := pressAll(q, one(keyDown), one(keyEnter)); got != outcomeAnswered {
		t.Fatalf("Submit gave %v", got)
	}
	if got := q.answers()[0].line(); got != "Dark mode, Export, Offline sync v2" {
		t.Fatalf("answered %q", got)
	}
}

func TestSeveralQuestionsOnTabs(t *testing.T) {
	q := newQuestion("multiq", []questionItem{colorQuestion, featuresQuestion}, true)
	if !strings.Contains(q.claudeScreen(), "Tab/Arrow keys to navigate") {
		t.Fatalf("the tabbed menu has the single-question hint:\n%s", q.claudeScreen())
	}
	if got := q.press(text("2")[0]); got != outcomeRepaint || q.tab != 1 {
		t.Fatalf("a digit on the first tab gave %v on tab %d", got, q.tab)
	}
	pressAll(q, text("1"), one(keyDown), one(keyDown), one(keyDown), one(keyDown), one(keyEnter))
	if q.tab != 2 {
		t.Fatalf("Submit on the last question left the menu on tab %d", q.tab)
	}
	screen := q.claudeScreen()
	for _, want := range []string{"☒ Color", "☒ Features", "Review your answers",
		" ● Which color should the button be?\r\n   → Green", " ● Which features do you want?\r\n   → Dark mode",
		"Ready to submit your answers?", "❯ 1. Submit answers", "  2. Cancel"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the review tab lacks %q:\n%s", want, screen)
		}
	}
	// Left goes back a tab and Tab comes forward again.
	pressAll(q, one(keyLeft))
	if q.tab != 1 {
		t.Fatalf("Left from the review is tab %d", q.tab)
	}
	pressAll(q, one(keyTab))
	if got := q.press(text("1")[0]); got != outcomeAnswered {
		t.Fatalf("1 on the review gave %v", got)
	}
	lines := []string{}
	for _, a := range q.answers() {
		lines = append(lines, a.line())
	}
	if !reflect.DeepEqual(lines, []string{"Green", "Dark mode"}) {
		t.Fatalf("answered %q", lines)
	}

	q = newQuestion("multiq", []questionItem{colorQuestion, featuresQuestion}, true)
	pressAll(q, one(keyTab), one(keyTab))
	if !strings.Contains(q.claudeScreen(), "You have not answered all questions") {
		t.Fatalf("an empty review does not say so:\n%s", q.claudeScreen())
	}
	if got := q.press(text("2")[0]); got != outcomeDeclined {
		t.Fatalf("Cancel gave %v", got)
	}
}

func TestPlanApprovalKeys(t *testing.T) {
	q := newQuestion("plan", nil, true)
	if got := q.press(text("2")[0]); got != outcomeAnswered || q.answers()[0].Choices[0] != "Yes, manually approve edits" {
		t.Fatalf("2 gave %v with %+v", got, q.answers())
	}

	q = newQuestion("plan", nil, true)
	pressAll(q, text("3"), text("Use bye instead of hi"))
	if !strings.Contains(q.claudePlanScreen(), "   ❯ 3. Use bye instead of hi\r\n") {
		t.Fatalf("the feedback is not drawn in place:\n%s", q.claudePlanScreen())
	}
	if got := q.press(key{kind: keyEnter}); got != outcomeRevise {
		t.Fatalf("Enter with feedback gave %v", got)
	}
	if got := q.answers()[0]; got.Text != "Use bye instead of hi" || got.Choices[0] != "Tell Claude what to change" {
		t.Fatalf("the plan was sent back with %+v", got)
	}
	q.revise()
	if !strings.Contains(q.claudePlanScreen(), "Changed as asked: Use bye instead of hi") || q.cursor[0] != 0 {
		t.Fatalf("the plan asked again is not the revised one:\n%s", q.claudePlanScreen())
	}

	q = newQuestion("plan", nil, true)
	if got := pressAll(q, text("3"), text("ok"), one(keyBackTab)); got != outcomeAnswered || q.answers()[0].Text != "ok" {
		t.Fatalf("Shift+Tab with feedback gave %v with %+v", got, q.answers())
	}
}

// A permission prompt whose last row declines with words of one's own: an
// option's digit takes it, and that row's digit, its Enter and Esc close the
// prompt for the words to follow; Codex's "y" is its first row.
func TestPermitKeys(t *testing.T) {
	permit := func(harness string) *question {
		item := claudePermit
		if harness == "codex" {
			item = codexPermit
		}
		q := newQuestion("permit", []questionItem{item}, false)
		q.harness = harness
		return q
	}
	q := permit("claude")
	if got := q.press(text("2")[0]); got != outcomeAnswered || q.answers()[0].Choices[0] != "Yes, and don't ask again for example.com" {
		t.Fatalf("2 gave %v with %+v", got, q.answers())
	}
	for _, keys := range [][]key{text("3"), one(keyEscape), append(append(one(keyDown), one(keyDown)...), one(keyEnter)...)} {
		if got := pressAll(permit("claude"), keys); got != outcomeFeedback {
			t.Fatalf("%v gave %v", keys, got)
		}
	}
	screen := permit("claude").permitScreen()
	for _, want := range []string{" ❯ 1. Yes\r\n", "   3. No, and tell Claude what to do differently (esc)\r\n",
		" Host: example.com\r\n"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the prompt has no %q:\n%s", want, screen)
		}
	}
	// permit2b's second prompt asks for another host, by one digit.
	again := permit("claude")
	again.host = "example1.com"
	if screen := again.permitScreen(); !strings.Contains(screen, " Host: example1.com\r\n") {
		t.Fatalf("the second prompt of permit2b is\n%s", screen)
	}

	q = permit("codex")
	if got := q.press(text("y")[0]); got != outcomeAnswered || q.answers()[0].Choices[0] != "Yes, proceed" {
		t.Fatalf("y gave %v with %+v", got, q.answers())
	}
	if got := pressAll(permit("codex"), text("2")); got != outcomeFeedback {
		t.Fatalf("2 gave %v", got)
	}
	if screen := permit("codex").permitScreen(); !strings.Contains(screen, "  › 1. Yes, proceed (y)\r\n") ||
		!strings.Contains(screen, "    2. No, and tell Codex what to do differently (esc)\r\n") {
		t.Fatalf("the prompt is\n%s", screen)
	}
}

// --- the program in a pane

type pane struct {
	t      *testing.T
	tmux   string
	sock   string
	name   string
	config string
	log    string
}

// buildFake builds this package once per test into a directory of its own,
// under the three names, so a test never runs a fake some other build left.
func buildFake(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain to build the fake with")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "faketui")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("building the fake: %v\n%s", err, out)
	}
	for _, name := range []string{"claude", "codex", "opencode"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// startPane runs the fake under a name in a tmux server on a socket of the
// test's own, never the machine's tmux. The socket gets a directory with a
// short name, because a socket path is limited to about a hundred bytes and
// t.TempDir spells the whole test name out.
func startPane(t *testing.T, bin, name string, args ...string) *pane {
	t.Helper()
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	sockDir, err := os.MkdirTemp("", "fq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	dir := t.TempDir()
	p := &pane{t: t, tmux: tmux, sock: filepath.Join(sockDir, "tmux.sock"), name: "fake",
		config: filepath.Join(dir, "claude"), log: filepath.Join(dir, "fake.log")}
	conf := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(conf, []byte("set -g status off\nset -sg escape-time 0\nset -g remain-on-exit on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", p.sock, "kill-server").Run() })
	cmd := []string{"-f", conf, "-S", p.sock, "new-session", "-d", "-s", p.name, "-x", "120", "-y", "40",
		"-c", dir, "-e", "CLAUDE_CONFIG_DIR=" + p.config, "-e", "FAKE_LOG=" + p.log,
		"-e", "XDG_DATA_HOME=" + filepath.Join(dir, "xdg"), "-e", "HOME=" + dir,
		filepath.Join(bin, name)}
	p.run(append(cmd, args...)...)
	// The line prompt is written with the banner, so the banner is enough.
	p.waitFor("FAKE " + name)
	return p
}

func (p *pane) run(args ...string) string {
	p.t.Helper()
	if args[0] != "-f" {
		args = append([]string{"-S", p.sock}, args...)
	}
	out, err := exec.Command(p.tmux, args...).CombinedOutput()
	if err != nil {
		p.t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// keys sends tmux key names (Enter, Down, Escape, BTab, a digit) one command
// each, and type sends literal text.
func (p *pane) keys(names ...string) {
	p.t.Helper()
	for _, name := range names {
		p.run("send-keys", "-t", p.name, name)
	}
}

func (p *pane) typeText(s string) {
	p.t.Helper()
	p.run("send-keys", "-t", p.name, "-l", s)
}

func (p *pane) line(s string) {
	p.t.Helper()
	p.typeText(s)
	p.keys("Enter")
}

func (p *pane) screen() string { return p.run("capture-pane", "-p", "-t", p.name) }

func (p *pane) waitFor(wants ...string) string {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	screen := ""
	for time.Now().Before(deadline) {
		screen = p.screen()
		found := true
		for _, want := range wants {
			found = found && strings.Contains(screen, want)
		}
		if found {
			return screen
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("the pane never showed %q; it shows:\n%s", wants, screen)
	return ""
}

// status is Claude's session status file, the one the fake's pid named.
func (p *pane) status() map[string]any {
	matches, _ := filepath.Glob(filepath.Join(p.config, "sessions", "*.json"))
	if len(matches) != 1 {
		return nil
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return nil
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	return doc
}

func (p *pane) waitStatus(status, waitingFor string) {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var doc map[string]any
	for time.Now().Before(deadline) {
		doc = p.status()
		if doc != nil && doc["status"] == status && (waitingFor == "" || doc["waitingFor"] == waitingFor) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("the status file never said %s/%s; it says %v", status, waitingFor, doc)
}

type loggedAnswer struct {
	Event    string   `json:"event"`
	Name     string   `json:"name"`
	Harness  string   `json:"harness"`
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Via      string   `json:"via"`
	Declined bool     `json:"declined"`
	Chat     bool     `json:"chat"`
	Approved *bool    `json:"approved"`
	Revision int      `json:"revision"`
	Answers  []answer `json:"answers"`
}

// answers is every answer record in FAKE_LOG, waiting until there are n.
func (p *pane) answers(n int) []loggedAnswer {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var found []loggedAnswer
	for time.Now().Before(deadline) {
		found = nil
		raw, _ := os.ReadFile(p.log)
		for _, line := range strings.Split(string(raw), "\n") {
			var rec loggedAnswer
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Event == "answer" {
				if rec.Name != "" {
					p.t.Fatalf("an answer record carries a name, which launch readers select by: %s", line)
				}
				found = append(found, rec)
			}
		}
		if len(found) >= n {
			return found
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("FAKE_LOG holds %d answers, want %d", len(found), n)
	return nil
}

// transcript is the content blocks of Claude's conversation file, in order.
func (p *pane) transcript() []map[string]any {
	p.t.Helper()
	matches, _ := filepath.Glob(filepath.Join(p.config, "projects", "*", "*.jsonl"))
	if len(matches) != 1 {
		p.t.Fatalf("found transcripts %v", matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		p.t.Fatal(err)
	}
	var blocks []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		var rec struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if list, ok := rec.Message.Content.([]any); ok {
			for _, block := range list {
				if m, ok := block.(map[string]any); ok {
					blocks = append(blocks, m)
				}
			}
		}
	}
	return blocks
}

func TestClaudeQuestionsInAPane(t *testing.T) {
	bin := buildFake(t)

	t.Run("single by digit", func(t *testing.T) {
		p := startPane(t, bin, "claude", "--session-id", uuid.NewString())
		p.line("/question")
		p.waitFor(" ☐ DB", "Which database should we use?", "❯ 1. Postgres", "     Relational, robust",
			"  3. Type something.", "  4. Chat about this", "Enter to select · ↑/↓ to navigate · Esc to cancel")
		p.waitStatus("waiting", "input needed")
		blocks := p.transcript()
		use := blocks[len(blocks)-1]
		if use["type"] != "tool_use" || use["name"] != "AskUserQuestion" {
			t.Fatalf("the transcript ends with %v", use)
		}
		p.keys("1")
		p.waitFor("● User answered Claude's questions:", "  ⎿  · Which database should we use? → Postgres")
		got := p.answers(1)[0]
		if got.Harness != "claude" || got.Kind != "single" || got.Via != "keys" || got.Declined ||
			!reflect.DeepEqual(got.Answers, []answer{{"Which database should we use?", []string{"Postgres"}, ""}}) {
			t.Fatalf("logged %+v", got)
		}
		blocks = p.transcript()
		result := blocks[len(blocks)-1]
		if result["type"] != "tool_result" || result["tool_use_id"] != use["id"] ||
			!strings.Contains(result["content"].(string), `"Which database should we use?"="Postgres"`) {
			t.Fatalf("the transcript ends with %v", result)
		}
		p.waitStatus("idle", "")
		p.waitFor("✻ Worked for 3s · done", "⏵⏵ auto mode on")
		// Back in line mode: an ordinary line is an ordinary turn.
		p.line("hello")
		p.waitFor("you said: hello")
	})

	t.Run("single by arrow and enter", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/question single")
		p.waitFor("❯ 1. Postgres")
		p.keys("Down")
		p.waitFor("❯ 2. SQLite")
		p.keys("Up", "Down", "Enter")
		p.waitFor("→ SQLite")
		if got := p.answers(1)[0].Answers[0].Choices; !reflect.DeepEqual(got, []string{"SQLite"}) {
			t.Fatalf("logged %v", got)
		}
	})

	t.Run("single with an answer of ones own", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/question")
		p.waitFor("❯ 1. Postgres")
		p.keys("3")
		p.waitFor("❯ 3. Type something.")
		p.typeText("MySQL 8")
		p.waitFor("❯ 3. MySQL 8")
		p.keys("Enter")
		p.waitFor("→ MySQL 8")
		if got := p.answers(1)[0].Answers[0]; len(got.Choices) != 0 || got.Text != "MySQL 8" {
			t.Fatalf("logged %+v", got)
		}
	})

	t.Run("escape declines", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/question")
		p.waitFor("❯ 1. Postgres")
		p.keys("Escape")
		p.waitFor("● User declined to answer questions")
		p.waitStatus("idle", "")
		if got := p.answers(1)[0]; !got.Declined || len(got.Answers) != 0 {
			t.Fatalf("logged %+v", got)
		}
		blocks := p.transcript()
		if last := blocks[len(blocks)-1]; last["is_error"] != true {
			t.Fatalf("the declined question ends the transcript with %v", last)
		}
	})

	t.Run("multi select", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/question multi")
		p.waitFor(" ☐ Features", "❯ 1. [ ] Dark mode", "         Add a dark color theme", "     Submit")
		p.keys("1", "3")
		p.waitFor("❯ 1. [✔] Dark mode", "  3. [✔] Export")
		p.keys("Down", "Down", "Down")
		p.waitFor("❯ 4. [ ] Type something.")
		p.typeText("Offline sync v2")
		p.waitFor("❯ 4. [✔] Offline sync v2")
		p.keys("Down")
		p.waitFor("❯    Submit")
		p.keys("Enter")
		p.waitFor("→ Dark mode, Export, Offline sync v2")
		got := p.answers(1)[0].Answers[0]
		if !reflect.DeepEqual(got.Choices, []string{"Dark mode", "Export"}) || got.Text != "Offline sync v2" {
			t.Fatalf("logged %+v", got)
		}
	})

	t.Run("several questions", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/question multiq")
		p.waitFor("☐ Color  ☐ Features  ✔ Submit  →", "Which color should the button be?",
			"Enter to select · Tab/Arrow keys to navigate · Esc to cancel")
		p.keys("2")
		p.waitFor("Which features do you want?", "☒ Color")
		p.keys("1", "Down", "Down", "Down", "Down")
		p.waitFor("❯    Submit")
		p.keys("Enter")
		p.waitFor("Review your answers", "   → Green", "   → Dark mode", "❯ 1. Submit answers")
		p.keys("1")
		p.waitFor("  ⎿  · Which color should the button be? → Green", "     · Which features do you want? → Dark mode")
		got := p.answers(1)[0]
		if got.Kind != "multiq" || len(got.Answers) != 2 || got.Answers[0].Choices[0] != "Green" {
			t.Fatalf("logged %+v", got)
		}
	})

	t.Run("plan asked again", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/question plan")
		p.waitFor("   Ready to code?", "   Here is Claude's plan:", "   ❯ 1. Yes, and use auto mode",
			"     3. Tell Claude what to change", "        shift+tab to approve with this feedback")
		p.waitStatus("waiting", "permission prompt")
		blocks := p.transcript()
		if last := blocks[len(blocks)-1]; last["name"] != "ExitPlanMode" {
			t.Fatalf("the transcript ends with %v", last)
		}
		p.keys("3")
		p.waitFor("   ❯ 3. Tell Claude what to change")
		p.typeText("Use bye instead of hi")
		p.waitFor("   ❯ 3. Use bye instead of hi")
		p.keys("Enter")
		p.waitFor("Changed as asked: Use bye instead of hi", "   ❯ 1. Yes, and use auto mode")
		p.waitStatus("waiting", "permission prompt")
		sentBack := p.answers(1)[0]
		if sentBack.Approved == nil || *sentBack.Approved || sentBack.Answers[0].Text != "Use bye instead of hi" {
			t.Fatalf("logged %+v", sentBack)
		}
		p.keys("1")
		p.waitFor("● User approved Claude's plan")
		approved := p.answers(2)[1]
		if approved.Approved == nil || !*approved.Approved || approved.Revision != 1 ||
			approved.Answers[0].Choices[0] != "Yes, and use auto mode" {
			t.Fatalf("logged %+v", approved)
		}
		p.waitStatus("idle", "")
	})

	t.Run("a question in prose", func(t *testing.T) {
		p := startPane(t, bin, "claude")
		p.line("/textq")
		p.waitFor("● Which color should the button be? Red, green or blue?", "⏵⏵ auto mode on")
		p.waitStatus("idle", "")
		p.line("blue")
		p.waitFor("you said: blue")
		got := p.answers(1)[0]
		if got.Kind != "text" || got.Answers[0].Text != "blue" {
			t.Fatalf("logged %+v", got)
		}
	})
}

func TestCodexQuestionInAPane(t *testing.T) {
	p := startPane(t, buildFake(t), "codex")
	p.line("/question plan")
	p.waitFor("codex asks a single question only")
	p.line("/question")
	p.waitFor("  › 1. Postgres  Relational, robust", "  Press enter to confirm or esc to cancel")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(p.run("display-message", "-p", "-t", p.name, "#{pane_title}"), "Action Required") {
		if time.Now().After(deadline) {
			t.Fatal("the title never said Action Required")
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.keys("Down", "Enter")
	p.waitFor("• Answered", "  └ Which database should we use? → SQLite")
	if got := p.answers(1)[0]; got.Harness != "codex" || got.Answers[0].Choices[0] != "SQLite" {
		t.Fatalf("logged %+v", got)
	}
}

func TestOpenCodeQuestionOverHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	p := startPane(t, buildFake(t), "opencode", "--port", strconv.Itoa(port))
	p.line("/question")
	p.waitFor("  ┃  △ Question", "  ┃  1. Postgres  Relational, robust")

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp, err := http.Get(base + "/question")
	if err != nil {
		t.Fatal(err)
	}
	var pending []struct {
		ID        string `json:"id"`
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	err = json.NewDecoder(resp.Body).Decode(&pending)
	_ = resp.Body.Close()
	if err != nil || len(pending) != 1 || pending[0].Questions[0].Question != "Which database should we use?" {
		t.Fatalf("GET /question answered %+v (%v)", pending, err)
	}
	body := bytes.NewBufferString(`{"answers":[["SQLite"]]}`)
	resp, err = http.Post(base+"/question/"+pending[0].ID+"/reply", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the reply was answered %d", resp.StatusCode)
	}
	p.waitFor("  ┃  · Which database should we use? → SQLite")
	if got := p.answers(1)[0]; got.Via != "http" || got.ID != pending[0].ID || got.Answers[0].Choices[0] != "SQLite" {
		t.Fatalf("logged %+v", got)
	}
	resp, err = http.Post(base+"/question/"+pending[0].ID+"/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a question already answered was rejected with %d", resp.StatusCode)
	}
}

func TestKeyDecoderReadsHomeEndAndDelete(t *testing.T) {
	var d keyDecoder
	var got []keyKind
	for _, b := range []byte("\x1b[1~\x1b[4~\x1b[H\x1b[F\x1bOH\x1bOF\x1b[7~\x1b[8~\x1b[3~\x1b[5~") {
		for _, k := range d.feed(b) {
			got = append(got, k.kind)
		}
	}
	want := []keyKind{keyHome, keyEnd, keyHome, keyEnd, keyHome, keyEnd, keyHome, keyEnd, keyDelete}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %v, want %v", got, want)
	}
}

func TestOpenCodePermissionCardKeys(t *testing.T) {
	card := func() *question {
		q := newQuestion("permit", []questionItem{openCodePermit}, false)
		q.harness = "opencode"
		return q
	}
	for _, c := range []struct {
		keys  []key
		label string
		reply string
	}{
		{one(keyEnter), "Allow once", "once"},
		{append(one(keyRight), one(keyEnter)...), "Allow always", "always"},
		{append(append(one(keyRight), one(keyRight)...), one(keyEnter)...), "Reject", "reject"},
		// The highlight stops at either end.
		{append(append(append(one(keyRight), one(keyRight)...), one(keyRight)...), one(keyEnter)...), "Reject", "reject"},
		{append(append(one(keyLeft), one(keyRight)...), one(keyEnter)...), "Allow always", "always"},
		{one(keyEscape), "Reject", "reject"},
		// A digit takes nothing: the card numbers no option.
		{append(text("2"), one(keyEnter)...), "Allow once", "once"},
	} {
		q := card()
		if got := pressAll(q, c.keys); got != outcomeAnswered || q.answers()[0].Choices[0] != c.label || q.permitReply() != c.reply {
			t.Fatalf("%v gave %v with %+v (%s)", c.keys, got, q.answers(), q.permitReply())
		}
	}
	q := card()
	q.press(one(keyRight)[0])
	screen := q.openCodePermitScreen()
	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(screen, "")
	if !strings.Contains(plain, "  ┃   Allow once   Allow always   Reject          ctrl+f fullscreen") ||
		!strings.Contains(screen, "\x1b[48;5;238mAllow always\x1b[49m") || strings.ContainsAny(plain, "›❯>") {
		t.Fatalf("the card is\n%q", screen)
	}
}

func TestOpenCodeCardsShowTheCursorInColourOnly(t *testing.T) {
	q := newQuestion("single", []questionItem{databaseQuestion}, false)
	q.press(one(keyDown)[0])
	screen := q.openCodeScreen()
	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(screen, "")
	for _, want := range []string{"  ┃  1. Postgres  Relational, robust\r\n", "  ┃  2. SQLite  Embedded file\r\n", "  ┃  3. Type something.\r\n"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the card has no %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "›") || !strings.Contains(screen, "\x1b[48;5;238m2. SQLite  Embedded file\x1b[49m") {
		t.Fatalf("the cursor is not drawn in colour only:\n%q", screen)
	}
}

func TestOpenCodePermissionCardInAPane(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	p := startPane(t, buildFake(t), "opencode", "--port", strconv.Itoa(port))
	p.line("/question permit")
	p.waitFor("  ┃  △ Permission required", "  ┃   Allow once   Allow always   Reject")
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/permission", port))
	if err != nil {
		t.Fatal(err)
	}
	var pending []map[string]any
	err = json.NewDecoder(resp.Body).Decode(&pending)
	_ = resp.Body.Close()
	if err != nil || len(pending) != 1 {
		t.Fatalf("GET /permission answered %v (%v)", pending, err)
	}
	p.keys("Right", "Right", "Enter")
	p.waitFor("  ┃  ✗ Permission rejected")
	if got := p.answers(1)[0]; got.Harness != "opencode" || got.Kind != "permit" || got.Answers[0].Choices[0] != "Reject" {
		t.Fatalf("logged %+v", got)
	}
	p.line("/question permit")
	p.waitFor("  ┃  △ Permission required")
	p.keys("Right", "Enter")
	p.waitFor("  ┃  ✓ Permission allowed always")
	if got := p.answers(2)[1]; got.Answers[0].Choices[0] != "Allow always" {
		t.Fatalf("logged %+v", got)
	}
}

// The prompt's line is edited where the cursor is, and Up brings the line
// sent before back - which Enter sends again.
func TestThePromptLineIsEditedAndRecalled(t *testing.T) {
	p := startPane(t, buildFake(t), "claude")
	p.line("echo first")
	p.waitFor("you said: echo first")
	p.keys("Up")
	p.waitFor("> echo first")
	p.keys("Enter")
	p.waitFor("> echo first\nyou said: echo first\n> echo first\nyou said: echo first")
	p.typeText("Not blue")
	p.keys("Home", "Right", "Right", "Right", "BSpace", "BSpace", "BSpace", "End")
	p.typeText("!")
	p.keys("Enter")
	p.waitFor(">  blue!\nyou said: blue!")
	p.typeText("abd")
	p.keys("Left", "Left")
	p.typeText("x")
	p.keys("DC", "Enter")
	p.waitFor("you said: axd")
	p.keys("Up", "Up", "Down", "Enter")
	p.waitFor("> axd\nyou said: axd\n> axd\nyou said: axd")
	// A character of several bytes is drawn whole, as typed: one key is one
	// byte, and the echo of each on its own would be mojibake.
	p.typeText("Grün ❤️ 青色")
	p.waitFor("> Grün ❤️ 青色")
	p.keys("Enter")
	p.waitFor("you said: Grün ❤️ 青色")
}

// /question cache asks the database question about something else: the same
// rows under other words, on the screen and in the transcript's record.
func TestTheCacheQuestionDiffersOnlyByItsWords(t *testing.T) {
	p := startPane(t, buildFake(t), "claude", "--session-id", uuid.NewString())
	p.line("/question cache")
	p.waitFor(" ☐ Cache", "Which database should the cache use?", "❯ 1. Postgres", "  2. SQLite", "  3. Type something.")
	blocks := p.transcript()
	if use := blocks[len(blocks)-1]; !strings.Contains(fmt.Sprint(use["input"]), "Which database should the cache use?") {
		t.Fatalf("the transcript ends with %v", use)
	}
	p.keys("2")
	p.waitFor("→ SQLite")
	if got := p.answers(1)[0]; got.Kind != "single" || got.Answers[0].Question != "Which database should the cache use?" {
		t.Fatalf("logged %+v", got)
	}
}

// A mouse report and the marks of a bracketed paste are swallowed whole: only
// what was pasted is text.
func TestKeyDecoderSwallowsMouseReportsAndPasteMarks(t *testing.T) {
	var d keyDecoder
	var got []byte
	for _, b := range []byte("\x1b[<0;12;5M\x1b[<0;12;5m\x1b[200~hi\x1b[201~") {
		for _, k := range d.feed(b) {
			if k.kind != keyText {
				t.Fatalf("decoded a %v", k.kind)
			}
			got = append(got, k.b)
		}
	}
	if string(got) != "hi" || d.pending() {
		t.Fatalf("decoded %q", got)
	}
}
