package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// Waits are an adapter's calls that wait for the person (ask_user, and the
// CLI's own questions converted to it), by ask id: Wait emits a call's ask
// and blocks, Answer hands it the answer from the host. The two sides agree
// on every answer: Answer reports it delivered only when the call took it
// while the CLI still waited, and ErrAskGone when the call had ended or been
// withdrawn first, so that an answer is never journaled as delivered through
// the tool and then dropped (the host sends a gone call's answer as a message
// instead).
type Waits struct {
	mu    sync.Mutex
	calls map[string]*waitingCall
}

// waitingCall is one call in Wait.
type waitingCall struct {
	ctx        context.Context
	turn, part string
	payload    json.RawMessage
	// withdrawn is set by Withdraw: the CLI no longer waits, though ctx has
	// not ended yet.
	withdrawn bool
	// hand takes an answer to the waiting call, which says on taken whether
	// it returns it to the CLI; done is closed when Wait returns.
	hand chan handoff
	done chan struct{}
}

type handoff struct {
	answer json.RawMessage
	taken  chan bool
}

// Wait emits ask (a TypeAsk event) and blocks until the answer comes or ctx
// ends, which is the CLI withdrawing the call or exiting; the error then
// wraps both ErrAskGone and ctx's error. A call withdrawn before it got here
// asks nothing. part is the tool part of the call, "" when the adapter does
// not know it, which Withdraw may match on.
func (w *Waits) Wait(ctx context.Context, out Emitter, ask Event, part string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAskGone, err)
	}
	c := &waitingCall{ctx: ctx, turn: ask.TurnID, part: part, payload: ask.Payload,
		hand: make(chan handoff), done: make(chan struct{})}
	w.mu.Lock()
	if w.calls == nil {
		w.calls = map[string]*waitingCall{}
	}
	w.calls[ask.AskID] = c
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.calls[ask.AskID] == c {
			delete(w.calls, ask.AskID)
		}
		w.mu.Unlock()
		close(c.done)
	}()
	out.Emit(ask)
	for {
		select {
		case h := <-c.hand:
			// The one place an answer is decided: once taken here, it is
			// returned to the CLI at once.
			w.mu.Lock()
			ok := ctx.Err() == nil && !c.withdrawn
			w.mu.Unlock()
			h.taken <- ok
			if ok {
				return h.answer, nil
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrAskGone, ctx.Err())
		}
	}
}

// Answer hands an answer to the call waiting on askID. It is ErrAskGone when
// no call waits there any more (or never did), and ErrAskDeferred when the
// call took the answer but its mechanism cannot carry all of it
// (askuser.SaysMore: an elicitation form has no place for the person's own
// words), so that the host sends the whole answer as a message as well.
func (w *Waits) Answer(askID string, answer json.RawMessage) error {
	w.mu.Lock()
	c := w.calls[askID]
	gone := c == nil || c.withdrawn
	w.mu.Unlock()
	if gone {
		return ErrAskGone
	}
	h := handoff{answer: answer, taken: make(chan bool, 1)}
	select {
	case c.hand <- h:
	case <-c.done:
		return ErrAskGone
	}
	if !<-h.taken {
		return ErrAskGone
	}
	if askuser.SaysMore(c.payload, answer) {
		return ErrAskDeferred
	}
	return nil
}

// Withdraw marks every waiting call match picks as gone, by its turn and
// part: the CLI has withdrawn it (an interrupt it acknowledged) though its
// context has not ended yet. From now on an answer to it is ErrAskGone.
func (w *Waits) Withdraw(match func(turn, part string) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.calls {
		if match(c.turn, c.part) {
			c.withdrawn = true
		}
	}
}
