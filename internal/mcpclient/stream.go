package mcpclient

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// replyTimeout bounds the client's answer to a request of the server.
const replyTimeout = 10 * time.Second

// streamConn is the half of a transport that both stdio and legacy SSE
// share: the server's messages arrive on a stream of their own, and the
// responses are matched to the waiting requests by id.
type streamConn struct {
	// write sends one encoded message.
	write func(ctx context.Context, b []byte) error
	// done is closed when no more messages can arrive, and failure says why.
	done    chan struct{}
	failure func() error

	mu      sync.Mutex
	waiters map[string]chan *message
}

func newStreamConn(write func(context.Context, []byte) error, failure func() error) *streamConn {
	return &streamConn{write: write, failure: failure, done: make(chan struct{}), waiters: map[string]chan *message{}}
}

// deliver hands a message from the server on: a response to its waiter, a
// request to answerFor. Notifications are ignored.
func (c *streamConn) deliver(m *message) {
	switch {
	case m.isResponse():
		c.mu.Lock()
		ch := c.waiters[idKey(m.ID)]
		delete(c.waiters, idKey(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case m.isRequest():
		b, _ := json.Marshal(answerFor(m))
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), replyTimeout)
			defer cancel()
			_ = c.write(ctx, b)
		}()
	}
}

func (c *streamConn) roundTrip(ctx context.Context, req *message) (*message, error) {
	key := idKey(req.ID)
	ch := make(chan *message, 1)
	c.mu.Lock()
	c.waiters[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, key)
		c.mu.Unlock()
	}()
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.write(ctx, b); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		select {
		case m := <-ch:
			return m, nil
		default:
		}
		return nil, c.failure()
	}
}

func (c *streamConn) send(ctx context.Context, msg *message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.write(ctx, b)
}
