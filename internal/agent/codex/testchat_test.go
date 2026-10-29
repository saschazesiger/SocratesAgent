//go:build !windows

package codex

import (
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// A chat whose repository has a test catalog (chattools.Endpoint.Tests)
// starts its thread with the test tools as dynamic tools beside ask_user and
// the chat tools, and their guidance after the chat tools'; a call of one
// goes to the endpoint.
func TestAChatWithTestsHasTheTestToolsInCodex(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	spec := lab.spec("a", "")
	spec.ChatTools = chattools.Endpoint{Socket: "/nonexistent/chat-tools.sock", Token: "c.x", Tests: true}
	a := lab.start(spec, r)
	_, n := send(t, a, r, "/thread")
	r.turnEnd(t, n, wait)
	got := lastTurnText(r.projection())
	tools, instructions, _ := strings.Cut(got, "\n")
	for _, name := range []string{askuser.Name, chattools.List, chattools.TestsFormat, chattools.TestsCheck, chattools.RunTests, chattools.TestResults} {
		if !strings.Contains(tools, name) {
			t.Errorf("the thread lacks %s: %s", name, tools)
		}
	}
	if want := "instructions: " + askGuidance + "\n\n" + chattools.Guidance("") + "\n\n" + chattools.TestGuidance(""); instructions != want {
		t.Errorf("developer instructions\n%q\nwant\n%q", instructions, want)
	}
	_, n = send(t, a, r, "/tool "+chattools.RunTests+` {"tests":["web/checkout"]}`)
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "Socrates' tests could not be reached") {
		t.Errorf("run_tests: %q", got)
	}
}
