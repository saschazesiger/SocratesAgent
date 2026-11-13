package server

import (
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

func TestRetryableFailure(t *testing.T) {
	for text, want := range map[string]bool{
		"Failed to authenticate. API Error: 403 Attention Required! | Cloudflare":       true,
		"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}": true,
		"API Error: 500 Internal server error":                                          true,
		"Connection error.":                                                             true,
		"The agent exited during the turn: signal: killed":                              true,
		"": true,
		"API Error: 400 prompt is too long: 210000 tokens > 200000 maximum":                               false,
		"Your credit balance is too low to access the Anthropic API.":                                     false,
		"Invalid API key · Please run /login":                                                             false,
		"API Error: 403 {\"type\":\"error\",\"error\":{\"type\":\"permission_error\",\"message\":\"…\"}}": false,
	} {
		if got := retryableFailure(text); got != want {
			t.Errorf("retryableFailure(%q) = %v, want %v", text, got, want)
		}
	}
}

// The wait doubles from retryBase up to retryCap, with at most a tenth
// more, the same for the same seed.
func TestRetryDelay(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for n, base := range want {
		got := retryDelay(n, "chat-turn")
		if got < base || got > base+base/10 {
			t.Errorf("retry %d waits %s, want %s to %s", n+1, got, base, base+base/10)
		}
		if again := retryDelay(n, "chat-turn"); again != got {
			t.Errorf("retry %d: %s, then %s", n+1, got, again)
		}
	}
	if retryDelay(100, "x") > retryCap+retryCap/10 {
		t.Error("the wait grows beyond its cap")
	}
}

// failureOf: a failed last turn nobody acted on, counted along the chain of
// retries that led to it.
func TestFailureOf(t *testing.T) {
	ended := time.Now().UnixMilli()
	fold := func(events ...agent.Event) *chatFailure {
		p := agent.NewProjection()
		for i, ev := range events {
			ev.Seq = int64(i + 1)
			p.Apply(ev)
		}
		return failureOf(p)
	}
	turn := func(msg, client, id, outcome, why string, at int64) []agent.Event {
		return []agent.Event{
			{Type: agent.TypeMessage, MessageID: msg, ClientID: client, Role: agent.RoleUser, Text: "go", At: at - 1000},
			{Type: agent.TypeTurnStarted, TurnID: id, MessageID: msg, At: at - 900},
			{Type: agent.TypeTurnEnded, TurnID: id, Outcome: outcome, Text: why, At: at},
		}
	}
	cloudflare := "API Error: 403 Attention Required! | Cloudflare"
	first := turn("u1", "c1", "t1", agent.OutcomeFailed, cloudflare, ended)
	if f := fold(first...); f == nil || f.Turn != "t1" || f.Retries != 0 || f.EndedAt != ended {
		t.Fatalf("the failure: %+v", f)
	}
	second := append(append([]agent.Event{}, first...), turn("u2", retryClientID(3, "t1"), "t2", agent.OutcomeFailed, cloudflare, ended+5000)...)
	if f := fold(second...); f == nil || f.Turn != "t2" || f.Retries != 3 {
		t.Fatalf("the failure after retry 3: %+v", f)
	}
	if f := fold(append(second, agent.Event{Type: agent.TypeMessage, MessageID: "u3", Role: agent.RoleUser, Text: "later", At: ended + 6000})...); f != nil {
		t.Fatalf("a message after the failure left %+v", f)
	}
	if f := fold(turn("u1", "c1", "t1", agent.OutcomeCompleted, "", ended)...); f != nil {
		t.Fatalf("a completed turn is a failure: %+v", f)
	}
	if f := fold(turn("u1", "c1", "t1", agent.OutcomeFailed, "prompt is too long", ended)...); f != nil {
		t.Fatalf("a permanent failure is retried: %+v", f)
	}
	if n := retryNumber(retryClientID(12, "turn-with-dashes")); n != 12 {
		t.Fatalf("retryNumber = %d", n)
	}
	if n := retryNumber("auto-continue-x"); n != 0 {
		t.Fatalf("retryNumber of another = %d", n)
	}
}
