package claude

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The instructions of the model a chat starts on are in the system prompt;
// a switch while the CLI runs puts the new model's before the next message,
// once; and the transcript read back shows the person's message without
// them.
func TestModelInstructionsFollowTheModel(t *testing.T) {
	e := newEnv(t)
	spec := e.spec("")
	spec.Instructions = map[string]string{"haiku": "NOTE-HAIKU", "opus": "NOTE-OPUS"}
	a := New(Options{})
	r := newRec()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Start(ctx, spec, r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	said := func(n int) string {
		r.turnsEnded(t, n)
		var text string
		r.view(func(p *agent.Projection) { text = partsOf(lastAssistant(p), agent.PartText)[0].Text })
		return text
	}
	send(t, a, "m1", "/argv")
	if argv := said(1); !strings.Contains(argv, "The person's instructions for haiku, the model this chat runs on:\n\nNOTE-HAIKU") ||
		strings.Contains(argv, "NOTE-OPUS") {
		t.Fatalf("the system prompt lacks haiku's instructions, or has opus's:\n%s", argv)
	}
	if err := a.SetModel(ctx, "opus", ""); err != nil {
		t.Fatal(err)
	}
	send(t, a, "m2", "hello")
	want := "you said: <model-instructions>\nThis chat now runs on opus. The person's instructions for haiku no longer apply. " +
		"The person's instructions for opus:\n\nNOTE-OPUS\n</model-instructions>\n\nhello"
	if got := said(2); got != want {
		t.Fatalf("after the switch the agent got\n%q\nwant\n%q", got, want)
	}
	send(t, a, "m3", "again")
	if got := said(3); got != "you said: again" {
		t.Fatalf("the note came twice: %q", got)
	}

	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	events, err := a.Rebuild(agent.StartSpec{Env: e.vars, ResumeID: conv})
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, m := range agent.Fold(number(events)).Messages {
		if m.Role == agent.RoleUser {
			users = append(users, m.Text)
		}
	}
	if strings.Join(users, "|") != "/argv|hello|again" {
		t.Fatalf("read back, the messages are %q", users)
	}
}
