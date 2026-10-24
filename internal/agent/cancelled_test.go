package agent

import "testing"

// A message the agent dropped unread is neither delivered nor owed to the
// next host.
func TestACancelledMessageIsNotHandedOverAgain(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeMessage, MessageID: "m1", Role: RoleUser, State: MessagePending},
		{Seq: 2, Type: TypeMessage, MessageID: "m2", Role: RoleUser, State: MessagePending},
		{Seq: 3, Type: TypeMessageState, MessageID: "m1", State: MessageQueued},
		{Seq: 4, Type: TypeMessageState, MessageID: "m1", State: MessageCancelled},
	})
	if u := p.Undelivered(); len(u) != 1 || u[0].ID != "m2" {
		t.Fatalf("undelivered = %+v", u)
	}
	if s := p.Summary(); s.Queued != 1 {
		t.Fatalf("queued = %d", s.Queued)
	}
}
