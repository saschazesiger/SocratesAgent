//go:build !windows

package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A thread keeps the ask_user guidance in its developer instructions after
// it was parked and resumed by a new codex, and has it once, not once per
// resume.
func TestRealAResumedThreadKeepsTheGuidance(t *testing.T) {
	lab := newRealLab(t)
	home := lab.home("a", "", "gpt-5.5")
	state := filepath.Join(lab.root, "state")
	r := newRecorder()
	a := lab.start(lab.spec(home, state, ""), r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), time.Second).ConversationID
	_, n := send(t, a, r, "SAY first")
	r.turnEnd(t, n, 30*time.Second)
	park(t, a)
	for i := 0; i < 2; i++ {
		r2 := newRecorder()
		b := lab.start(lab.spec(home, state, thread), r2)
		_, n = send(t, b, r2, "SAY resumed")
		r2.turnEnd(t, n, 30*time.Second)
		park(t, b)
	}
	if got := lab.lastDeveloperCount(t, askGuidance); got != 1 {
		t.Fatalf("the last request has the guidance %d times\n%s", got, lab.modelLog(t))
	}
}

// lastDeveloperCount is how many developer messages of the last model
// request hold text.
func (l *realLab) lastDeveloperCount(t *testing.T, text string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(l.root, "model.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	count := -1
	for _, line := range strings.Split(string(raw), "\n") {
		var req struct {
			Body struct {
				Input []struct {
					Role    string `json:"role"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			} `json:"body"`
		}
		if json.Unmarshal([]byte(line), &req) != nil || len(req.Body.Input) == 0 {
			continue
		}
		count = 0
		for _, in := range req.Body.Input {
			for _, c := range in.Content {
				if in.Role == "developer" && strings.Contains(c.Text, text) {
					count++
				}
			}
		}
	}
	return count
}
