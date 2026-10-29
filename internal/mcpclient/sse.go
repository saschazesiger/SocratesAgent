package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// sseEvent is one server-sent event.
type sseEvent struct {
	event string
	data  string
}

// readSSE reads server-sent events from r and hands each to fn until fn
// returns false or the stream ends. An event may be at most maxLine bytes.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var ev sseEvent
	var data strings.Builder
	hasData := false
	dispatch := func() bool {
		if !hasData {
			ev = sseEvent{}
			return true
		}
		ev.data = data.String()
		more := fn(ev)
		ev, hasData = sseEvent{}, false
		data.Reset()
		return more
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !dispatch() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.event = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	// A last event the server did not end with a blank line still counts.
	dispatch()
	return nil
}

// sseTransport is the legacy HTTP+SSE transport (protocol 2024-11-05): the
// server's messages come on one event stream, and the client posts its own
// to the endpoint the stream names first.
type sseTransport struct {
	*streamConn
	url      *url.URL
	headers  map[string]string
	client   *http.Client
	endpoint string
	cancel   context.CancelFunc
	body     io.ReadCloser

	errMu     sync.Mutex
	streamErr error
}

// openSSE opens the event stream and waits for the endpoint event.
func openSSE(ctx context.Context, s Server) (*sseTransport, error) {
	u, err := checkURL(s.URL)
	if err != nil {
		return nil, err
	}
	t := &sseTransport{url: u, headers: s.Headers, client: newHTTPClient()}
	t.streamConn = newStreamConn(t.write, t.failure)

	streamCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return nil, explain(ctx, "the event stream", requestError(ctx, u.String(), err))
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		cancel()
		return nil, statusError(resp, u.String())
	}
	if mt := mediaType(resp); mt != "text/event-stream" {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("the server at %s answered with %s instead of an event stream", u, mt)
	}
	t.body = resp.Body

	endpoint := make(chan string, 1)
	go func() {
		defer close(t.done)
		named := false
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			switch ev.event {
			case "endpoint":
				if !named {
					named = true
					endpoint <- ev.data
				}
			case "", "message":
				for _, m := range parseMessages([]byte(ev.data)) {
					t.deliver(m)
				}
			}
			return true
		})
		t.errMu.Lock()
		t.streamErr = err
		t.errMu.Unlock()
	}()

	select {
	case raw := <-endpoint:
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			t.close()
			return nil, fmt.Errorf("the server named a message endpoint that is not a URL: %q", raw)
		}
		ep := u.ResolveReference(ref)
		if ep.Scheme != u.Scheme || ep.Host != u.Host {
			t.close()
			return nil, fmt.Errorf("the server named a message endpoint on another host (%s)", ep.Redacted())
		}
		t.endpoint = ep.String()
		return t, nil
	case <-t.done:
		t.close()
		if ctx.Err() != nil {
			return nil, explain(ctx, "the endpoint event", ctx.Err())
		}
		return nil, errors.New("the server at " + u.String() + " closed the event stream before it named its message endpoint")
	case <-ctx.Done():
		t.close()
		return nil, explain(ctx, "the endpoint event", ctx.Err())
	}
}

// write posts one message to the endpoint.
func (t *sseTransport) write(ctx context.Context, b []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return requestError(ctx, t.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return statusError(resp, t.endpoint)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// failure says why the stream ended.
func (t *sseTransport) failure() error {
	t.errMu.Lock()
	err := t.streamErr
	t.errMu.Unlock()
	if err != nil {
		return fmt.Errorf("the event stream from %s broke off before the server answered: %v", t.url, err)
	}
	return errors.New("the server closed the event stream at " + t.url.String() + " before it answered")
}

func (t *sseTransport) setProtocolVersion(string) {}

// close ends the event stream.
func (t *sseTransport) close() {
	t.cancel()
	if t.body != nil {
		_ = t.body.Close()
	}
	<-t.done
	t.client.CloseIdleConnections()
}
