//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// recorder is an Emitter that keeps everything, stamped the way the journal
// stamps it, so a projection can be folded from it.
type recorder struct {
	mu     sync.Mutex
	events []agent.Event
	deltas []agent.Delta
	exited []error
	wake   chan struct{}
}

func newRecorder() *recorder { return &recorder{wake: make(chan struct{}, 1)} }

func (r *recorder) Emit(events ...agent.Event) {
	r.mu.Lock()
	for _, ev := range events {
		ev.Seq = int64(len(r.events) + 1)
		ev.At = time.Now().UnixMilli()
		r.events = append(r.events, ev)
	}
	r.mu.Unlock()
	r.poke()
}

func (r *recorder) Delta(messageID, partID, text string) {
	r.mu.Lock()
	r.deltas = append(r.deltas, agent.Delta{MessageID: messageID, PartID: partID, Text: text})
	r.mu.Unlock()
	r.poke()
}

func (r *recorder) Exited(err error) {
	r.mu.Lock()
	r.exited = append(r.exited, err)
	r.mu.Unlock()
	r.poke()
}

func (r *recorder) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *recorder) all() []agent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.Event(nil), r.events...)
}

// since is every event after the first n.
func (r *recorder) since(n int) []agent.Event {
	all := r.all()
	if n > len(all) {
		return nil
	}
	return all[n:]
}

func (r *recorder) projection() *agent.Projection { return agent.Fold(r.all()) }

// waitFor waits for the first event after the first `after` that matches.
func (r *recorder) waitFor(t *testing.T, after int, what string, match func(agent.Event) bool, timeout time.Duration) agent.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		for _, ev := range r.since(after) {
			if match(ev) {
				return ev
			}
		}
		select {
		case <-r.wake:
		case <-deadline:
			t.Fatalf("timed out waiting for %s; events:\n%s", what, dump(r.since(after)))
		}
	}
}

// turnEnd waits for the next turn.ended after `after`.
func (r *recorder) turnEnd(t *testing.T, after int, timeout time.Duration) agent.Event {
	t.Helper()
	return r.waitFor(t, after, "turn.ended", func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded }, timeout)
}

func dump(events []agent.Event) string {
	var b strings.Builder
	for _, ev := range events {
		raw, _ := json.Marshal(ev)
		if len(raw) > 400 {
			raw = append(raw[:400], "…"...)
		}
		fmt.Fprintf(&b, "  %s\n", raw)
	}
	return b.String()
}

func isType(typ string) func(agent.Event) bool {
	return func(ev agent.Event) bool { return ev.Type == typ }
}

func isNotice(code string) func(agent.Event) bool {
	return func(ev agent.Event) bool { return ev.Type == agent.TypeNotice && ev.Code == code }
}

// lastTurnText is the text parts of the newest assistant message, joined.
func lastTurnText(p *agent.Projection) string {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		m := p.Messages[i]
		if m.Role != agent.RoleAssistant {
			continue
		}
		var parts []string
		for _, pt := range m.Parts {
			if pt.Kind == agent.PartText {
				parts = append(parts, pt.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// partsOf is every part of the newest assistant message, nested ones too.
func partsOf(p *agent.Projection) []*agent.Part {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		if p.Messages[i].Role == agent.RoleAssistant {
			var out []*agent.Part
			var walk func([]*agent.Part)
			walk = func(ps []*agent.Part) {
				for _, pt := range ps {
					out = append(out, pt)
					walk(pt.Children)
				}
			}
			walk(p.Messages[i].Parts)
			return out
		}
	}
	return nil
}

func contentOf(pt *agent.Part) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(pt.Content, &m)
	return m
}

// userMessage journals a user message the way the host does and returns it
// for Send, with its place among the turns (agent.Outgoing.AfterTurn).
func userMessage(r *recorder, text string) agent.Outgoing {
	id := newID("m_")
	r.Emit(agent.Event{Type: agent.TypeMessage, MessageID: id, Role: agent.RoleUser, State: agent.MessagePending, Text: text})
	return agent.Outgoing{MessageID: id, ClientID: "c" + id, Text: text, AfterTurn: r.projection().Message(id).AfterTurn}
}

func send(t *testing.T, a agent.Adapter, r *recorder, text string) (agent.Outgoing, int) {
	t.Helper()
	n := len(r.all())
	msg := userMessage(r, text)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Send(ctx, msg); err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
	return msg, n
}

func messageState(p *agent.Projection, id string) string {
	if m := p.Message(id); m != nil {
		return m.State
	}
	return ""
}
