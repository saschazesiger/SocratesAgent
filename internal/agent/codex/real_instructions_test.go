//go:build !windows

package codex

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A model's instructions are in the developer instructions of a new thread.
// The thread keeps those for good, so a resume on another model, and a
// switch while codex runs, give the new model's in a note before the next
// message, once.
func TestRealModelInstructionsFollowTheModel(t *testing.T) {
	lab := newRealLab(t)
	home := lab.home("a", "", "gpt-5.5")
	state := filepath.Join(lab.root, "state")
	notes := map[string]string{"gpt-5.5": "NOTE-ALPHA", "gpt-5.4": "NOTE-BETA"}
	spec := lab.spec(home, state, "")
	spec.Model, spec.Instructions = "gpt-5.5", notes
	r := newRecorder()
	a := lab.start(spec, r)
	thread := r.waitFor(t, 0, "conversation", isType(agent.TypeConversation), time.Second).ConversationID
	_, n := send(t, a, r, "SAY first")
	r.turnEnd(t, n, 30*time.Second)
	if got := lab.lastDeveloperCount(t, "NOTE-ALPHA"); got != 1 {
		t.Fatalf("a new thread has its model's instructions %d times\n%s", got, lab.modelLog(t))
	}
	park(t, a)

	beta := "<model-instructions>\nThis chat now runs on gpt-5.4. The person's instructions for gpt-5.5 no longer apply. " +
		"The person's instructions for gpt-5.4:\n\nNOTE-BETA\n</model-instructions>\n\nSAY resumed"
	for i := 0; i < 2; i++ {
		// Twice: the second start knows the thread was told already.
		spec = lab.spec(home, state, thread)
		spec.Model, spec.Instructions = "gpt-5.4", notes
		r2 := newRecorder()
		b := lab.start(spec, r2)
		_, n = send(t, b, r2, "SAY resumed")
		r2.turnEnd(t, n, 30*time.Second)
		if got := lab.lastRequestCount(t, beta); got != 1 {
			t.Fatalf("start %d on the other model: the note is there %d times\n%s", i+1, got, lab.modelLog(t))
		}
		if i == 0 {
			park(t, b)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.SetModel(ctx, "gpt-5.5", ""); err != nil {
			t.Fatal(err)
		}
		_, n = send(t, b, r2, "SAY switched")
		r2.turnEnd(t, n, 30*time.Second)
		alpha := "<model-instructions>\nThis chat now runs on gpt-5.5. The person's instructions for gpt-5.4 no longer apply. " +
			"The person's instructions for gpt-5.5:\n\nNOTE-ALPHA\n</model-instructions>\n\nSAY switched"
		if got := lab.lastRequestCount(t, alpha); got != 1 {
			t.Fatalf("the message after the switch has the note %d times\n%s", got, lab.modelLog(t))
		}
	}
}
