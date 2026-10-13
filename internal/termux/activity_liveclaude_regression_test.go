package termux

// TestLiveClaudeScreensAgainstOpenCodeWaitingRegression feeds real tmux
// captures from a live `claude` 2.1.258 session through scrapeScreen(KindClaude, …)
// to check that the OpenCode-only `openCodeWaiting` addition (see the diff to
// activity.go touching openCodeWaiting/scrapeScreen's KindOpenCode branch) has
// no effect on the Claude harness's classification.
//
// This is a one-off regression aid for that review, not a permanent fixture:
// it is skipped unless SOCRATES_LIVECLAUDE_FIXTURES points at a directory of
// captures (see the session's scratchpad livecheck/fixtures). It is not run
// by `go test ./...` in normal CI.
//
// Fixture naming encodes the expected classification as the file's second
// underscore-separated token: "NN_state_description.txt" with state one of
// idle|busy|waiting. A capture existing only to prove absence of waiting
// (e.g. the "Permission required" echo transcript) is named accordingly.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

func TestLiveClaudeScreensAgainstOpenCodeWaitingRegression(t *testing.T) {
	dir := os.Getenv("SOCRATES_LIVECLAUDE_FIXTURES")
	if dir == "" {
		t.Skip("SOCRATES_LIVECLAUDE_FIXTURES not set; skipping live-capture regression check")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading fixtures dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("no .txt fixtures found in %s", dir)
	}

	want := map[string]State{
		"idle":    StateIdle,
		"busy":    StateBusy,
		"waiting": StateWaiting,
	}

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.SplitN(name, "_", 3)
			if len(parts) < 2 {
				t.Fatalf("fixture name %q does not encode a state (NN_state_desc.txt)", name)
			}
			wantState, ok := want[parts[1]]
			if !ok {
				t.Fatalf("fixture name %q has unrecognised state token %q", name, parts[1])
			}
			got := scrapeScreen(harnesses.KindClaude, string(raw))
			t.Logf("fixture=%s ok=%v state=%v note=%q", name, got.ok, got.state, got.note)
			if !got.ok {
				t.Fatalf("scrapeScreen(KindClaude, …) did not recognise the screen at all, want %v", wantState)
			}
			if got.state != wantState {
				t.Errorf("scrapeScreen(KindClaude, …) = %v, want %v", got.state, wantState)
			}
		})
	}
}
