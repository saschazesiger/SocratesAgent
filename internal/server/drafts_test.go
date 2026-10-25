//go:build linux

package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func waitForCond(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The prepared chats of a build that had PreStart become chats nobody has
// written to yet as the server starts: in a folder of their own, their first
// prompt kept as the draft, nothing sent and nothing started. A preset that
// is gone leaves the chat where it was, said missing, rather than losing it;
// a name somebody gave stays; a prepared terminal is the clean cut's.
func TestPreparedChatsBecomeChatsWithTheirDraft(t *testing.T) {
	e := workspaceEnv(t)
	gone := filepath.Join(e.home, "gone")
	rows := []*store.Session{
		{ID: "dyn", Title: "Claude Code · new folder", Harness: "claude", WorkdirMode: store.WorkdirDynamic,
			Kind: store.KindChat, State: store.StatePrestart, Options: json.RawMessage(`{"ultracode":true}`)},
		{ID: "lost", Title: "Claude Code · gone", Harness: "claude", WorkdirMode: "preset", Workdir: gone,
			Kind: store.KindChat, State: store.StatePrestart},
		{ID: "named", Title: "x", Harness: "codex", WorkdirMode: store.WorkdirDynamic,
			Kind: store.KindChat, State: store.StatePrestart},
		{ID: "old", Title: "old", Harness: "claude", WorkdirMode: store.WorkdirCustom, Workdir: e.home,
			Kind: store.KindTerminal, State: store.StatePrestart},
	}
	for _, row := range rows {
		if err := e.store.CreateSession(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.store.SaveDraft("dyn", "the first prompt", 0); err != nil {
		t.Fatal(err)
	}
	if err := e.store.UpdateSessionTitle("named", "Mine"); err != nil {
		t.Fatal(err)
	}

	e.srv.readyPreparedChats()

	dyn, _ := e.store.GetSession("dyn")
	if dyn.State != store.StateRunning || !dyn.IsChat() || dyn.Workdir == "" ||
		!strings.HasPrefix(dyn.Workdir, filepath.Join(e.home, "workspaces")) || !readUltracode(dyn.Options) {
		t.Fatalf("the prepared chat became %#v", dyn)
	}
	if info, err := os.Stat(dyn.Workdir); err != nil || !info.IsDir() {
		t.Fatalf("its folder: %v", err)
	}
	if draft, err := e.store.GetDraft("dyn"); err != nil || draft.Text != "the first prompt" {
		t.Fatalf("its draft: %#v %v", draft, err)
	}
	if pending, err := e.store.Outbox("dyn"); err != nil || len(pending) != 0 {
		t.Fatalf("something was sent for it: %v %v", pending, err)
	}
	if alive, _ := e.srv.chats.launcher.Alive("dyn"); alive {
		t.Fatal("a host was started for it")
	}

	lost, _ := e.store.GetSession("lost")
	if lost.State != store.StateRunning || lost.Workdir != gone {
		t.Fatalf("the chat with a gone preset became %#v", lost)
	}
	res, payload := e.do(t, e.client, "GET", "/api/sessions/lost", "")
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK || session["workdir_missing"] != true {
		t.Fatalf("the chat with a gone preset: %d %#v", res.StatusCode, payload)
	}

	if named, _ := e.store.GetSession("named"); named.State != store.StateRunning || named.Title != "Mine" {
		t.Fatalf("the named chat became %#v", named)
	}
	if old, _ := e.store.GetSession("old"); old.State != store.StatePrestart {
		t.Fatalf("the prepared terminal became %s", old.State)
	}

	// The list carries a chat's draft, for the sidebar and the other devices.
	res, payload = e.do(t, e.client, "GET", "/api/sessions/dyn", "")
	if session, _ := payload["session"].(map[string]any); res.StatusCode != http.StatusOK ||
		session["draft_preview"] != "the first prompt" || session["draft_rev"] != float64(1) {
		t.Fatalf("the chat's view: %d %#v", res.StatusCode, payload)
	}
	// Run again, it finds nothing to do.
	e.srv.readyPreparedChats()
	if again, _ := e.store.GetSession("dyn"); again.Workdir != dyn.Workdir {
		t.Fatalf("a second run moved it to %q", again.Workdir)
	}
}
