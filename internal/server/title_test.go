//go:build !windows

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The title of a terminal and of a chat nobody has written to yet, against
// the same stand in gateway the Status button is tested with: a Shell is
// never named on its own, and one is named from its screen when somebody
// asks. A chat naming itself from its first message is chattitle_test.go's.

// prompted drives the one edge the title run listens for: a session that
// somebody has typed at going busy. A real pane produces it through the
// detector; a test produces it here, so that what is tested is the naming and
// not the timing of a program.
func (e *assistEnv) prompted(id string) {
	e.srv.titles.observe(id, termux.Activity{State: termux.StateIdle, Asked: true})
	e.srv.titles.observe(id, termux.Activity{State: termux.StateBusy, Asked: true})
}

// titleFrames is the phases of every title frame one client has heard about
// one session, in order.
func titleFrames(client *wsClient, id string) []map[string]any {
	var out []map[string]any
	for _, frame := range client.ctrl {
		if frame["t"] == "title" && frame["id"] == id {
			out = append(out, frame)
		}
	}
	return out
}

// A shell has no subject. Its screen is a prompt and a directory, and naming
// it after `cd` would be worse than the name it has.
func TestTitleRunSkipsTheShell(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("Listing a directory")

	e.prompted(e.id)
	time.Sleep(700 * time.Millisecond)
	if n := e.gw.count(); n != 0 {
		t.Fatalf("the gateway saw %d calls for a shell session", n)
	}
}

// What comes back from a model is not a title until this has been done to it.
func TestCleanTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Refactoring the parser tests", "Refactoring the parser tests"},
		{"  \"Fixing the login redirect.\"  ", "Fixing the login redirect"},
		{"**Adding the retry loop**", "Adding the retry loop"},
		{"```json\nDebugging the tmux socket\n```", "Debugging the tmux socket"},
		{"Reviewing\nthe second line is not a title", "Reviewing"},
		{"Deutsche  Übersetzung   der Doku!", "Deutsche Übersetzung der Doku"},
		{"", ""},
		{"``` ```", ""},
	}
	for _, c := range cases {
		if got := cleanTitle(c.in); got != c.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("verylongword ", 12)
	if got := cleanTitle(long); len([]rune(got)) > titleMaxRunes {
		t.Errorf("cleanTitle left %d runes: %q", len([]rune(got)), got)
	}
}

/* --------------------------------------------------- a naming asked for */

// retitle asks for a new name the way the page does.
func (e *assistEnv) retitle(id string, body map[string]any) (*http.Response, map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	return e.do(e.t, e.client, "POST", "/api/sessions/"+id+"/title", string(raw))
}

// unsentChat stores a chat nobody has written to yet, with a name somebody
// gave it (or none) and a draft of its first message (or none). Nothing runs
// for it: there is nothing to send.
func (e *assistEnv) unsentChat(title, draft string) string {
	e.t.Helper()
	id := termux.NewID()
	row := &store.Session{ID: id, Title: "Claude Code · work", Harness: "claude", Kind: store.KindChat,
		State: store.StateRunning, Workdir: e.t.TempDir(), WorkdirMode: "custom"}
	if err := e.srv.store.CreateSession(row); err != nil {
		e.t.Fatal(err)
	}
	if title != "" {
		if err := e.srv.store.UpdateSessionTitle(id, title); err != nil {
			e.t.Fatal(err)
		}
	}
	if draft != "" {
		if _, err := e.srv.store.SaveDraft(id, draft, 0); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

// A chat nobody has written to yet is named after the draft of its first
// message the first time its field is left - from the field's text, which can
// be ahead of the saved draft - and only that once; the arrow names it again
// whenever it is pressed.
func TestRetitleNamesAnUnsentChatAfterItsDraft(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("Flaky login test")
	id := e.unsentChat("", "an older draft")

	res, payload := e.retitle(id, map[string]any{"unnamed": true, "prompt": "Fix the flaky login test in auth"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retitle: %d %#v", res.StatusCode, payload)
	}
	session, _ := payload["session"].(map[string]any)
	if session["title"] != "Flaky login test" || session["title_source"] != store.TitleAuto {
		t.Fatalf("the session is %#v", session)
	}
	calls := e.gw.seen()
	if len(calls) != 1 {
		t.Fatalf("the gateway saw %d calls", len(calls))
	}
	if prompt := calls[0].last(); !strings.Contains(prompt, "First message:\nFix the flaky login test in auth") ||
		strings.Contains(prompt, "older draft") {
		t.Fatalf("the namer was not shown the field's text alone:\n%s", prompt)
	}

	// Named once: leaving the field again asks nothing.
	if res, _ := e.retitle(id, map[string]any{"unnamed": true, "prompt": "something else"}); res.StatusCode != http.StatusNoContent {
		t.Fatalf("a second first naming: %d, want 204", res.StatusCode)
	}
	if n := e.gw.count(); n != 1 {
		t.Fatalf("the gateway saw %d calls, want the one", n)
	}

	// The arrow replaces any name, a typed one included.
	if res, payload := e.do(t, e.client, "PATCH", "/api/sessions/"+id, `{"title":"Typed by hand"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %#v", res.StatusCode, payload)
	}
	e.gw.always("Login test repair")
	res, payload = e.retitle(id, map[string]any{"prompt": "Repair the login test"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retitle: %d %#v", res.StatusCode, payload)
	}
	if row, _ := e.srv.store.GetSession(id); row.Title != "Login test repair" || row.TitleSource != store.TitleAuto {
		t.Fatalf("after the arrow the session is %q (%q)", row.Title, row.TitleSource)
	}
}

// A name somebody gave a chat is not replaced by leaving its field; and with
// nothing written there is nothing to name it after.
func TestRetitleLeavesAGivenNameWhenTheFieldIsLeft(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("Made up")
	named := e.unsentChat("Mine", "do it")
	if res, _ := e.retitle(named, map[string]any{"unnamed": true, "prompt": "do it"}); res.StatusCode != http.StatusNoContent {
		t.Fatalf("leaving the field of a named session: %d, want 204", res.StatusCode)
	}
	empty := e.unsentChat("", "")
	if res, payload := e.retitle(empty, map[string]any{}); res.StatusCode != http.StatusConflict {
		t.Fatalf("naming a session with no prompt: %d %#v, want 409", res.StatusCode, payload)
	}
	if n := e.gw.count(); n != 0 {
		t.Fatalf("the gateway saw %d calls", n)
	}
	if row, _ := e.srv.store.GetSession(named); row.Title != "Mine" {
		t.Fatalf("the given name became %q", row.Title)
	}
}

// A running session is named again from its screen, and every browser hears
// of it: the row pulses, then takes the name.
func TestRetitleNamesARunningSessionFromItsScreen(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("Probing the shell")
	e.typeLine(e.id, "echo retitle-probe-7")
	e.waitForPane(e.id, "retitle-probe-7")
	client := e.dialWS(e.id, "")
	client.hello()

	res, payload := e.retitle(e.id, map[string]any{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retitle: %d %#v", res.StatusCode, payload)
	}
	if prompt := e.gw.seen()[0].last(); !strings.Contains(prompt, "retitle-probe-7") || strings.Contains(prompt, "First prompt:") {
		t.Fatalf("the namer was not shown the screen:\n%s", prompt)
	}
	client.await("a finished title frame", 10*time.Second, func() bool {
		frames := titleFrames(client, e.id)
		return len(frames) == 2 && frames[0]["phase"] == titlePhaseStart &&
			frames[1]["phase"] == titlePhaseDone && frames[1]["title"] == "Probing the shell"
	})
}

// Without a key the arrow says why nothing happens; leaving a field does not.
func TestRetitleWithoutAKey(t *testing.T) {
	e := newAssistEnv(t)
	e.configureOpenRouter(t, map[string]any{"api_key": ""})
	id := e.unsentChat("", "do it")
	if res, _ := e.retitle(id, map[string]any{"unnamed": true}); res.StatusCode != http.StatusNoContent {
		t.Fatalf("leaving the field: %d, want 204", res.StatusCode)
	}
	if res, payload := e.retitle(id, map[string]any{}); res.StatusCode != http.StatusConflict ||
		!strings.Contains(fmt.Sprint(payload["error"]), "OpenRouter") {
		t.Fatalf("the arrow: %d %#v, want 409 naming the key", res.StatusCode, payload)
	}
}
