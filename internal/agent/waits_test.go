package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// emits is an Emitter that only keeps the events.
type emits struct {
	mu     sync.Mutex
	events []Event
}

func (e *emits) Emit(events ...Event) {
	e.mu.Lock()
	e.events = append(e.events, events...)
	e.mu.Unlock()
}
func (e *emits) Delta(string, string, string) {}
func (e *emits) Exited(error)                 {}

func (e *emits) asks() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

// waitAsked waits until a call has emitted its ask.
func waitAsked(t *testing.T, out *emits, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for out.asks() < n {
		if time.Now().After(deadline) {
			t.Fatal("the call did not ask")
		}
		time.Sleep(time.Millisecond)
	}
}

const answer = `{"answers":{}}`

// An answer and the end of its call that race are decided once: Answer
// reports the answer delivered exactly when the call returned it.
func TestAnAnswerRacingTheCallsEndIsDecidedOnce(t *testing.T) {
	var w Waits
	out := &emits{}
	for i := range 300 {
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan error, 1)
		go func() {
			_, err := w.Wait(ctx, out, Event{Type: TypeAsk, AskID: "a", TurnID: "t"}, "")
			got <- err
		}()
		waitAsked(t, out, i+1)
		var wg sync.WaitGroup
		wg.Add(2)
		var err error
		go func() { defer wg.Done(); cancel() }()
		go func() { defer wg.Done(); err = w.Answer("a", json.RawMessage(answer)) }()
		wg.Wait()
		waitErr := <-got
		delivered := err == nil && waitErr == nil
		gone := errors.Is(err, ErrAskGone) && errors.Is(waitErr, ErrAskGone) && errors.Is(waitErr, context.Canceled)
		if !delivered && !gone {
			t.Fatalf("round %d: Answer said %v, the call ended with %v", i, err, waitErr)
		}
	}
}

// A withdrawn call takes no answer, though it waits until its context ends;
// a call withdrawn before it waited asks nothing.
func TestAWithdrawnCallTakesNoAnswer(t *testing.T) {
	var w Waits
	out := &emits{}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := w.Wait(ctx, out, Event{Type: TypeAsk, AskID: "a", TurnID: "t1"}, "p1")
		got <- err
	}()
	waitAsked(t, out, 1)
	w.Withdraw(func(turn, part string) bool { return turn == "t2" })
	if err := w.Answer("a", json.RawMessage(answer)); err != nil {
		t.Fatalf("a call of another turn: %v", err)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := w.Wait(ctx, out, Event{Type: TypeAsk, AskID: "b", TurnID: "t1"}, "p2")
		got <- err
	}()
	waitAsked(t, out, 2)
	w.Withdraw(func(turn, part string) bool { return turn == "t1" && part == "p2" })
	if err := w.Answer("b", json.RawMessage(answer)); !errors.Is(err, ErrAskGone) {
		t.Fatalf("an answer to a withdrawn call: %v", err)
	}
	select {
	case err := <-got:
		t.Fatalf("the withdrawn call returned before its context ended: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-got; !errors.Is(err, ErrAskGone) {
		t.Fatal(err)
	}
	if _, err := w.Wait(ctx, out, Event{Type: TypeAsk, AskID: "c"}, ""); !errors.Is(err, ErrAskGone) || out.asks() != 2 {
		t.Fatalf("a call withdrawn before it waited: %v, %d asks", err, out.asks())
	}
	if err := w.Answer("nobody", json.RawMessage(answer)); !errors.Is(err, ErrAskGone) {
		t.Fatalf("an unknown ask: %v", err)
	}
}

// An elicitation's call takes an answer with words its form has no place
// for, and reports it deferred, so the host sends it as a message as well.
func TestAnAnswerAFormCannotCarryIsDeferred(t *testing.T) {
	var w Waits
	out := &emits{}
	form := json.RawMessage(`{"source":"elicitation","questions":[{"id":"colour","question":"Colour?","kind":"single",` +
		`"options":[{"id":"red","label":"Red"}]}]}`)
	got := make(chan json.RawMessage, 1)
	go func() {
		a, _ := w.Wait(context.Background(), out, Event{Type: TypeAsk, AskID: "f", Payload: form}, "")
		got <- a
	}()
	waitAsked(t, out, 1)
	with := json.RawMessage(`{"answers":{"colour":{"selected":["red"]}},"general_note":"the dark one"}`)
	if err := w.Answer("f", with); !errors.Is(err, ErrAskDeferred) {
		t.Fatalf("answer %v", err)
	}
	if a := <-got; string(a) != string(with) {
		t.Fatalf("the call got %s", a)
	}
}
