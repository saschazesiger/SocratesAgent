//go:build !windows

package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The automatic title, against the same stand in gateway the Status button and
// the operator loop are tested with. What has to be proven is that a session
// names itself once, from what is on its screen, and never over a name a
// person chose.

// codingSession is a session of a harness that has something to be about,
// which is the only kind the title run happens for.
func (e *assistEnv) codingSession(dir string) string {
	e.t.Helper()
	work := filepath.Join(e.work, dir)
	if err := os.MkdirAll(work, 0o755); err != nil {
		e.t.Fatal(err)
	}
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q}`, work))
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(e.t, session)
	e.waitForPane(id, "FAKE claude")
	return id
}

// answered drives the one edge the title run listens for: a session that was
// working and now is not. A real pane produces it through the detector; a test
// produces it here, so that what is tested is the naming and not the timing of
// a harness.
func (e *assistEnv) answered(id string) {
	e.srv.titles.observe(id, termux.StateBusy)
	e.srv.titles.observe(id, termux.StateIdle)
}

// titleOf waits for the stored name to become what it should be. The run is a
// goroutine nobody waits for, which is the whole point of it.
func (e *assistEnv) titleOf(id, want string, within time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(within)
	var last string
	for time.Now().Before(deadline) {
		row, err := e.srv.store.GetSession(id)
		if err != nil {
			e.t.Fatalf("session %s: %v", id, err)
		}
		last = row.Title
		if last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("the session was never named %q; it is called %q", want, last)
}

// The feature itself: the first answer a session gives names it, the name
// reaches every open browser, and it happens exactly once.
func TestTitleRunNamesASessionOnItsFirstAnswer(t *testing.T) {
	e := newAssistEnv(t)
	id := e.codingSession("named")
	e.gw.always("```\n**\"Refactoring the parser tests\"**.\n```")

	client := e.dialWS(id, "")
	// The socket is only listening once its handshake has been answered, and
	// the naming is fast enough to beat it.
	client.hello()

	e.answered(id)
	e.titleOf(id, "Refactoring the parser tests", 20*time.Second)

	row, err := e.srv.store.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.TitleSource != store.TitleAuto {
		t.Fatalf("title_source = %q, want the automatic one", row.TitleSource)
	}

	// Every browser renames its sidebar without asking for anything.
	client.await("a title frame", 10*time.Second, func() bool {
		for _, frame := range client.ctrl {
			if frame["t"] == "title" && frame["id"] == id &&
				frame["title"] == "Refactoring the parser tests" {
				return true
			}
		}
		return false
	})

	calls := e.gw.seen()
	if len(calls) != 1 {
		t.Fatalf("the gateway saw %d calls, want one", len(calls))
	}
	if calls[0].Model != "test/agent-model" {
		t.Fatalf("the namer used %q", calls[0].Model)
	}
	if calls[0].MaxTokens != titleMaxTokens {
		t.Fatalf("max_tokens = %d", calls[0].MaxTokens)
	}
	if calls[0].Temperature == nil || *calls[0].Temperature != 0.3 {
		t.Fatalf("temperature = %#v", calls[0].Temperature)
	}
	if prompt := calls[0].last(); !strings.Contains(prompt, "FAKE claude") ||
		!strings.Contains(prompt, "3 to 7 words") {
		t.Fatalf("the namer was not shown the screen and the instruction:\n%s", prompt)
	}

	// A second turn finishing is not a second name.
	e.answered(id)
	time.Sleep(500 * time.Millisecond)
	if n := e.gw.count(); n != 1 {
		t.Fatalf("the gateway saw %d calls after a second turn, want one", n)
	}
	if row, _ := e.srv.store.GetSession(id); row.Title != "Refactoring the parser tests" {
		t.Fatalf("the session was renamed a second time, to %q", row.Title)
	}
}

// A name somebody typed is theirs. Neither the one given at creation nor a
// rename is ever replaced by a model's idea of what the session is about.
func TestTitleRunLeavesANameThePersonChose(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("Something the model made up")

	work := filepath.Join(e.work, "mine")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q,"title":"My own name"}`, work))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	named := sessionID(t, session)
	e.waitForPane(named, "FAKE claude")

	// And the other half: created nameless, renamed since.
	renamed := e.codingSession("renamed")
	res, payload := e.do(t, e.client, "PATCH", "/api/sessions/"+renamed, `{"title":"I named this"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %#v", res.StatusCode, payload)
	}

	e.answered(named)
	e.answered(renamed)
	time.Sleep(700 * time.Millisecond)

	if n := e.gw.count(); n != 0 {
		t.Fatalf("the gateway saw %d calls for sessions that already have names", n)
	}
	for id, want := range map[string]string{named: "My own name", renamed: "I named this"} {
		if row, _ := e.srv.store.GetSession(id); row.Title != want {
			t.Fatalf("session %s is called %q, want %q", id, row.Title, want)
		}
	}
}

// With no key there is nothing to ask, and the placeholder name stays. It is
// silent on purpose: nobody pressed anything.
func TestTitleRunIsSilentWithoutAKey(t *testing.T) {
	e := newAssistEnv(t)
	id := e.codingSession("nokey")
	before, _ := e.srv.store.GetSession(id)
	e.configureOpenRouter(t, map[string]any{"api_key": ""})

	e.answered(id)
	time.Sleep(700 * time.Millisecond)

	if n := e.gw.count(); n != 0 {
		t.Fatalf("the gateway saw %d calls with no key configured", n)
	}
	row, _ := e.srv.store.GetSession(id)
	if row.Title != before.Title {
		t.Fatalf("the session was renamed to %q", row.Title)
	}
	// Nothing was spent, so the session may still be named once a key is set.
	if row.TitleSource != "" {
		t.Fatalf("title_source = %q, want it still open", row.TitleSource)
	}
}

// A shell has no subject. Its screen is a prompt and a directory, and naming
// it after `cd` would be worse than the name it has.
func TestTitleRunSkipsTheShell(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("Listing a directory")

	e.answered(e.id)
	time.Sleep(700 * time.Millisecond)
	if n := e.gw.count(); n != 0 {
		t.Fatalf("the gateway saw %d calls for a shell session", n)
	}
}

// A model that answers with nothing usable has still had its turn: the name
// stays as it was, and it is not asked again.
func TestTitleRunSpendsItsOneTurnOnGarbage(t *testing.T) {
	e := newAssistEnv(t)
	id := e.codingSession("garbage")
	before, _ := e.srv.store.GetSession(id)
	e.gw.always("   \n  ")

	e.answered(id)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if row, _ := e.srv.store.GetSession(id); row.TitleSource == store.TitleAuto {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	row, _ := e.srv.store.GetSession(id)
	if row.TitleSource != store.TitleAuto {
		t.Fatalf("title_source = %q, want the turn spent", row.TitleSource)
	}
	if row.Title != before.Title {
		t.Fatalf("the session was renamed to %q on an empty answer", row.Title)
	}

	e.answered(id)
	time.Sleep(500 * time.Millisecond)
	if n := e.gw.count(); n != 1 {
		t.Fatalf("the gateway saw %d calls, want the one", n)
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
