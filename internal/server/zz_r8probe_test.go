//go:build !windows

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// PROBE r8: OpenCode question card, own words, then Up + Enter.
func TestR8ProbeOpenCodeWordsThenUpEnter(t *testing.T) {
	e := newAssistEnv(t)
	id := e.harnessSession(config.HarnessOpenCode, "r8-oc-up")
	d := e.awaitAsking(id, "/question single", "Which database should we use?", databaseJSON)
	t.Logf("screen before:\n%s", e.paneShows(id))
	e.gw.always(`{"actions":[{"text":"3"},{"text":"MariaDB"},{"key":"Up"},{"key":"Enter"}],"done":true,"summary":"Told it MariaDB."}`)
	res, payload := e.answer(id, map[string]any{"digest_rev": d.Rev,
		"answers": []any{map[string]any{"item": 0, "text": "MariaDB"}}})
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("answer: %d %#v", res.StatusCode, payload)
	}
	run := e.awaitRun(id, "ended", 60*time.Second, ended)
	time.Sleep(time.Second)
	t.Logf("run: %#v", run)
	t.Logf("screen after:\n%s", e.paneShows(id))
}
