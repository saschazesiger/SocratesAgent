package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// maxBody bounds a JSON answer of an HTTP server.
	maxBody = 32 << 20
	// closeTimeout bounds the DELETE that ends an HTTP session.
	closeTimeout = 5 * time.Second
)

// AuthRequiredError is returned when an HTTP server answers 401, or 403
// with insufficient_scope: it needs an OAuth access token.
type AuthRequiredError struct {
	// WWWAuthenticate is the server's WWW-Authenticate header.
	WWWAuthenticate string
	// ResourceMetadata is the protected resource metadata URL the header
	// names, if it names one.
	ResourceMetadata string
	// Scope is the scope the header asks for, if it asks for one.
	Scope string
}

func (e *AuthRequiredError) Error() string {
	if strings.Contains(e.WWWAuthenticate, "insufficient_scope") {
		if e.Scope != "" {
			return "the server requires authorization with more access (scope " + e.Scope + ")"
		}
		return "the server requires authorization with more access"
	}
	return "the server requires authorization (OAuth sign-in)"
}

// authRequired reads the answer of a server that refuses the request for
// want of authorization; it is nil for any other answer.
func authRequired(resp *http.Response) *AuthRequiredError {
	values := resp.Header.Values("WWW-Authenticate")
	params := bearerParams(values)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
	case resp.StatusCode == http.StatusForbidden && params["error"] == "insufficient_scope":
	default:
		return nil
	}
	return &AuthRequiredError{
		WWWAuthenticate:  strings.Join(values, ", "),
		ResourceMetadata: params["resource_metadata"],
		Scope:            params["scope"],
	}
}

// bearerParams are the parameters of the Bearer challenge among the
// WWW-Authenticate headers (RFC 9110 section 11.6.1), names lowercased.
func bearerParams(values []string) map[string]string {
	for _, v := range values {
		for _, ch := range parseChallenges(v) {
			if strings.EqualFold(ch.scheme, "Bearer") {
				return ch.params
			}
		}
	}
	return map[string]string{}
}

// challenge is one authentication challenge.
type challenge struct {
	scheme string
	params map[string]string
}

// parseChallenges splits a WWW-Authenticate value into its challenges: a
// scheme, then name=value or name="quoted value" parameters, separated by
// commas. A token68 is skipped.
func parseChallenges(s string) []challenge {
	var out []challenge
	i := 0
	skip := func(chars string) {
		for i < len(s) && strings.IndexByte(chars, s[i]) >= 0 {
			i++
		}
	}
	token := func() string {
		start := i
		for i < len(s) && strings.IndexByte(" \t,=\"", s[i]) < 0 {
			i++
		}
		return s[start:i]
	}
	for i < len(s) {
		skip(" \t,")
		name := token()
		if name == "" {
			if i < len(s) {
				i++ // a stray character
			}
			continue
		}
		skip(" \t")
		isParam := i < len(s) && s[i] == '=' && len(out) > 0
		if isParam {
			// name= followed by more '=' or the end is a token68 tail.
			j := i + 1
			for j < len(s) && s[j] == '=' {
				j++
			}
			if j > i+1 {
				i = j
				continue
			}
		}
		if !isParam {
			out = append(out, challenge{scheme: name, params: map[string]string{}})
			continue
		}
		i++ // '='
		skip(" \t")
		var value string
		if i < len(s) && s[i] == '"' {
			i++
			var b strings.Builder
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
				i++
			}
			i++ // closing quote
			value = b.String()
		} else {
			value = token()
		}
		out[len(out)-1].params[strings.ToLower(name)] = value
	}
	return out
}

// newHTTPClient is the client of one probe. It has no overall timeout, since
// an event stream lasts as long as the probe does; ctx ends it.
func newHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 30 * time.Second
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}

// checkURL is the server's URL, parsed, or an error that says what is
// wrong with it.
func checkURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("no URL is set")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s is not an http or https URL", raw)
	}
	return u, nil
}

// requestError explains a request that got no answer at all.
func requestError(ctx context.Context, target string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &wrapped{msg: "could not reach " + target + ": " + err.Error(), err: err}
}

// statusError explains an answer that is neither a success nor a request
// for authorization.
func statusError(resp *http.Response, target string) error {
	if auth := authRequired(resp); auth != nil {
		return auth
	}
	msg := "the server answered " + resp.Status + " at " + target
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if detail := errorDetail(body); detail != "" {
		msg += ": " + detail
	}
	return errors.New(msg)
}

// errorDetail is the message in an error body: a JSON-RPC or OAuth error,
// or short plain text. HTML pages give none.
func errorDetail(body []byte) string {
	body = trimSpace(body)
	if len(body) == 0 {
		return ""
	}
	if body[0] == '{' {
		var e struct {
			Error            json.RawMessage `json:"error"`
			ErrorDescription string          `json:"error_description"`
			Message          string          `json:"message"`
		}
		if json.Unmarshal(body, &e) != nil {
			return ""
		}
		var rpc rpcError
		var code string
		switch {
		case json.Unmarshal(e.Error, &rpc) == nil && rpc.Message != "":
			return oneLine(rpc.Message, 300)
		case json.Unmarshal(e.Error, &code) == nil && code != "":
			if e.ErrorDescription != "" {
				return oneLine(code+": "+e.ErrorDescription, 300)
			}
			return code
		}
		return oneLine(e.Message, 300)
	}
	if body[0] == '<' {
		return ""
	}
	return oneLine(string(body), 200)
}

// mediaType is the media type of the answer, lowercased, without parameters.
func mediaType(resp *http.Response) string {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	}
	return mt
}

// httpTransport is the streamable HTTP transport: every message is a POST,
// and the answer comes as JSON or as an event stream.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu       sync.Mutex
	session  string
	protocol string
}

// openHTTP prepares the streamable HTTP transport. It sends nothing yet.
func openHTTP(s Server) (*httpTransport, error) {
	u, err := checkURL(s.URL)
	if err != nil {
		return nil, err
	}
	return &httpTransport{url: u.String(), headers: s.Headers, client: newHTTPClient()}, nil
}

// request builds a request with the configured headers and the session's.
func (t *httpTransport) request(ctx context.Context, method string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.url, r)
	if err != nil {
		return nil, err
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	if t.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", t.protocol)
	}
	t.mu.Unlock()
	return req, nil
}

// post sends one message and returns the server's successful answer.
func (t *httpTransport) post(ctx context.Context, msg *message) (*http.Response, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := t.request(ctx, http.MethodPost, body)
	if err != nil {
		return nil, err
	}
	hadSession := req.Header.Get("Mcp-Session-Id") != ""
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, requestError(ctx, t.url, err)
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.mu.Lock()
		t.session = id
		t.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && hadSession {
			return nil, errors.New("the server ended the session (404 Not Found at " + t.url + ")")
		}
		return nil, statusError(resp, t.url)
	}
	return resp, nil
}

func (t *httpTransport) roundTrip(ctx context.Context, req *message) (*message, error) {
	resp, err := t.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	key := idKey(req.ID)
	switch mt := mediaType(resp); {
	case mt == "text/event-stream":
		var answer *message
		err := readSSE(resp.Body, func(ev sseEvent) bool {
			if ev.event != "" && ev.event != "message" {
				return true
			}
			for _, m := range parseMessages([]byte(ev.data)) {
				switch {
				case m.isResponse() && idKey(m.ID) == key:
					answer = m
					return false
				case m.isRequest():
					t.reply(ctx, answerFor(m))
				}
			}
			return true
		})
		if answer != nil {
			return answer, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("the event stream from %s broke off before the server answered: %v", t.url, err)
		}
		return nil, errors.New("the server closed the event stream at " + t.url + " before it answered")
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("could not read the answer from %s: %v", t.url, err)
		}
		if len(body) > maxBody {
			return nil, fmt.Errorf("the answer from %s is larger than %d MiB", t.url, maxBody>>20)
		}
		for _, m := range parseMessages(body) {
			if m.isResponse() && idKey(m.ID) == key {
				return m, nil
			}
		}
		return nil, errors.New("the server's answer at " + t.url + " is not a JSON-RPC response to " + req.Method)
	case resp.StatusCode == http.StatusAccepted || mt == "":
		return nil, errors.New("the server at " + t.url + " accepted " + req.Method + " but sent no answer")
	default:
		return nil, fmt.Errorf("the server at %s answered with %s instead of JSON or an event stream", t.url, mt)
	}
}

// reply answers a request the server made in an event stream.
func (t *httpTransport) reply(ctx context.Context, m *message) {
	ctx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	_ = t.send(ctx, m)
}

func (t *httpTransport) send(ctx context.Context, msg *message) error {
	resp, err := t.post(ctx, msg)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	return resp.Body.Close()
}

func (t *httpTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.protocol = v
	t.mu.Unlock()
}

// close ends the session with a DELETE, when the server gave one.
func (t *httpTransport) close() {
	defer t.client.CloseIdleConnections()
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	if session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	req, err := t.request(ctx, http.MethodDelete, nil)
	if err != nil {
		return
	}
	if resp, err := t.client.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
}
