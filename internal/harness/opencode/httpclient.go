package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/proc"
)

// What the adapter puts in the server's environment.
//
// The password is generated per adapter start and lives in memory only: a
// restart starts a new server with a new password, so there is nothing to
// persist. OPENCODE_PERMISSION is what actually makes the session unattended
// (F-10) - its value is JSON, so "allow" is four characters plus its quotes -
// and the permission.v2.asked auto-reply in sse.go is only the second line of
// defence for the case where the variable does not take.
const (
	envPassword   = "OPENCODE_SERVER_PASSWORD"
	envUsername   = "OPENCODE_SERVER_USERNAME"
	envPermission = "OPENCODE_PERMISSION"

	serverUsername  = "socrates"
	permissionAllow = `"allow"`
)

// Timeouts. listenWait covers a cold start (the binary loads its config and
// its provider list before it binds); healthWait is the readiness poll the
// design pins at 10 s; requestTimeout is every other call, which is a local
// unix-loopback request against a server that is already answering.
const (
	listenWait     = 60 * time.Second
	healthWait     = 10 * time.Second
	requestTimeout = 30 * time.Second
)

// listeningLine is the one line `opencode serve` prints on stdout when it is
// up: "opencode server listening on http://127.0.0.1:4096". The port in it is
// authoritative - the adapter asks for --port 0 and lets the operating system
// choose, so this is the only place the real port is known.
var listeningLine = regexp.MustCompile(`listening on (https?://[^\s]+)`)

// server is one `opencode serve` process owned by one adapter (or, briefly, by
// Discover).
type server struct {
	cmd    *exec.Cmd
	base   string // http://127.0.0.1:<port>
	pass   string
	output *ring

	exited  chan struct{}
	exitErr error
}

// startServer launches one `opencode serve --port 0 --hostname 127.0.0.1`,
// waits for its listening line and then for /api/health, and returns it with a
// client already holding its Basic-auth credentials.
//
// The port is 0 on purpose: the design's alternative - bind 127.0.0.1:0, read
// the port, close the listener and hand the number to the child - has a window
// in which something else takes the port back, which is why it needed a retry
// loop. Letting the child bind and print what it got has no such window.
func startServer(ctx context.Context, bin, cwd string, extraEnv, extraArgs []string) (*server, *client, error) {
	pass, err := randomPassword()
	if err != nil {
		return nil, nil, err
	}

	args := append([]string{"serve", "--port", "0", "--hostname", "127.0.0.1"}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		envPassword+"="+pass,
		envUsername+"="+serverUsername,
		envPermission+"="+permissionAllow,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	// Own process group, so the server and everything its tools spawn can be
	// signalled as one tree.
	proc.Configure(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("opencode: stdout pipe: %w", err)
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("opencode: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("opencode: start %s: %w", bin, err)
	}

	s := &server{cmd: cmd, pass: pass, output: newRing(8 << 10), exited: make(chan struct{})}
	go s.drain(errPipe)

	// The listening line comes first; everything after it is log noise that is
	// kept for a death message and otherwise thrown away.
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		found := false
		for sc.Scan() {
			line := sc.Text()
			s.output.write(line + "\n")
			if !found {
				if m := listeningLine.FindStringSubmatch(line); m != nil {
					found = true
					select {
					case lines <- m[1]:
					default:
					}
				}
			}
		}
		close(lines)
	}()

	go func() {
		s.exitErr = cmd.Wait()
		close(s.exited)
	}()

	select {
	case base, ok := <-lines:
		if !ok {
			s.stop()
			return nil, nil, fmt.Errorf("opencode: the server exited before it was listening: %s", s.tail())
		}
		s.base = strings.TrimRight(base, "/")
	case <-s.exited:
		return nil, nil, fmt.Errorf("opencode: the server exited before it was listening: %s", s.tail())
	case <-time.After(listenWait):
		s.stop()
		return nil, nil, fmt.Errorf("opencode: the server did not report a listening address within %s: %s", listenWait, s.tail())
	case <-ctx.Done():
		s.stop()
		return nil, nil, ctx.Err()
	}

	c := newClient(s.base, pass)
	if err := c.waitHealthy(ctx, healthWait); err != nil {
		s.stop()
		return nil, nil, err
	}
	return s, c, nil
}

// drain keeps the child's stderr from filling its pipe buffer, and keeps the
// tail of everything it printed - stdout and stderr both - for the message a
// death is reported with.
func (s *server) drain(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.output.write(string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (s *server) tail() string { return s.output.string() }

// serverLogLine matches the prefix OpenCode puts on a log line:
// "[17:40:15.802] ERROR (#16699): ".
var serverLogLine = regexp.MustCompile(`^\[[0-9:.]+\]\s+(ERROR|FATAL|WARN)\s*(\(#\d+\))?:\s*`)

// errorLine is the last thing the server complained about, stripped of its
// timestamp. It is the only "why" available on the failure paths that never
// reach the wire: a model the server cannot run makes the turn stop with no
// event at all, and prints
//
//	[17:40:15.802] ERROR (#16699): Failed to drain Session
//	SessionRunnerModel.ModelUnavailableError: Model unavailable: opencode/does-not-exist
//
// on its own output, followed by a stack trace. The trace lines are skipped;
// the message is not.
func (s *server) errorLine() string {
	lines := strings.Split(s.output.string(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "at ") {
			continue
		}
		m := serverLogLine.FindString(line)
		if m == "" {
			continue
		}
		msg := strings.TrimSpace(line[len(m):])
		if msg == "" {
			continue
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return msg
	}
	return ""
}

// stop asks the process group to go away and, if it will not, kills it.
func (s *server) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = proc.Terminate(s.cmd)
	select {
	case <-s.exited:
	case <-time.After(3 * time.Second):
		_ = s.cmd.Process.Kill()
	}
}

// stopWithin is stop with the caller's grace period.
func (s *server) stopWithin(ctx context.Context, grace time.Duration) {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = proc.Terminate(s.cmd)
	select {
	case <-s.exited:
	case <-time.After(grace):
		_ = s.cmd.Process.Kill()
	case <-ctx.Done():
		_ = s.cmd.Process.Kill()
	}
}

func randomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("opencode: generate a server password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------- the client

// client speaks the subset of OpenCode's HTTP API this adapter needs. Every
// request carries HTTP Basic auth: OPENCODE_SERVER_PASSWORD turns authentication
// on for every route, /api/health included, and an Authorization: Bearer header
// is not accepted.
type client struct {
	base string
	user string
	pass string
	hc   *http.Client
	// stream has no timeout of its own: an SSE connection is meant to stay
	// open for the life of the session.
	stream *http.Client
}

func newClient(base, pass string) *client {
	return &client{
		base:   strings.TrimRight(base, "/"),
		user:   serverUsername,
		pass:   pass,
		hc:     &http.Client{Timeout: requestTimeout},
		stream: &http.Client{},
	}
}

func (c *client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// do runs one request and decodes a successful body into out (which may be
// nil). A non-2xx answer becomes an error carrying the server's own message,
// because that message - "Unsupported API for openrouter/..." - is exactly
// what the user has to read.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("opencode: %s %s: %s: %s", method, path, resp.Status, serverMessage(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("opencode: %s %s: decode the answer: %w", method, path, err)
	}
	return nil
}

// serverMessage pulls the human half out of an error body
// ({"_tag":"UnauthorizedError","message":"Authentication required"}) and falls
// back to the raw bytes.
func serverMessage(raw []byte) string {
	var body struct {
		Tag     string `json:"_tag"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Message != "" {
		if body.Tag != "" {
			return body.Tag + ": " + body.Message
		}
		return body.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 512 {
		s = s[:512] + "…"
	}
	return s
}

func (c *client) health(ctx context.Context) error {
	var out struct {
		Healthy bool `json:"healthy"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/health", nil, &out); err != nil {
		return err
	}
	if !out.Healthy {
		return fmt.Errorf("opencode: the server reports itself unhealthy")
	}
	return nil
}

// waitHealthy polls /api/health until it answers or the deadline passes.
func (c *client) waitHealthy(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error
	for {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.health(reqCtx)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("opencode: the server was not healthy within %s: %w", within, last)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// modelRef is POST /session/{id}/model's body. The key is id, not modelID -
// that is the mistake this adapter exists to not make again - and variant is
// the reasoning effort, omitted when there is none.
type modelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func (c *client) createSession(ctx context.Context, dir string) (string, error) {
	body := map[string]any{"location": map[string]any{"directory": dir}}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	// A top-level "directory" is silently ignored by the server; only
	// location.directory decides where the agent's tools run.
	if err := c.do(ctx, http.MethodPost, "/api/session", body, &out); err != nil {
		return "", err
	}
	if out.Data.ID == "" {
		return "", fmt.Errorf("opencode: the server created a session without an id")
	}
	return out.Data.ID, nil
}

func (c *client) setModel(ctx context.Context, session string, m modelRef) error {
	return c.do(ctx, http.MethodPost, "/api/session/"+session+"/model",
		map[string]any{"model": m}, nil)
}

// prompt sends one user message. delivery is "queue", not the default
// "steer": the engine never sends into a live turn, and queue is the shape
// with strict FIFO semantics.
//
// The returned admittedSeq is the durable seq of this turn's prompt.admitted
// event, which is the baseline everything replayed on the SSE stream is
// measured against (F-9).
func (c *client) prompt(ctx context.Context, session, text string) (int64, error) {
	body := map[string]any{
		"prompt":   map[string]any{"text": text},
		"delivery": "queue",
	}
	var out struct {
		Data struct {
			AdmittedSeq int64  `json:"admittedSeq"`
			ID          string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/session/"+session+"/prompt", body, &out); err != nil {
		return 0, err
	}
	return out.Data.AdmittedSeq, nil
}

func (c *client) interrupt(ctx context.Context, session string) error {
	return c.do(ctx, http.MethodPost, "/api/session/"+session+"/interrupt", nil, nil)
}

// replyPermission answers a permission.v2.asked. With OPENCODE_PERMISSION set
// this never fires, but an ask left unanswered blocks the turn forever.
func (c *client) replyPermission(ctx context.Context, session, request, reply string) error {
	return c.do(ctx, http.MethodPost,
		"/api/session/"+session+"/permission/"+request+"/reply",
		map[string]any{"reply": reply}, nil)
}

// active reports whether this session is running a turn. Sessions absent from
// the answer are idle; a dead server makes this error rather than return an
// empty map, which is the difference the turn-end logic turns on.
func (c *client) active(ctx context.Context, session string) (bool, error) {
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session/active", nil, &out); err != nil {
		return false, err
	}
	_, running := out.Data[session]
	return running, nil
}

// modelEntry is one model as the server describes it, on GET /api/model and
// inside GET /config/providers alike.
type modelEntry struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"providerID"`
	Name       string          `json:"name"`
	Family     string          `json:"family"`
	Status     string          `json:"status"` // "active", "deprecated", ... ; empty on /api/model
	Variants   json.RawMessage `json:"variants"`
	Cost       struct {
		Input  float64 `json:"input"` // dollars per million input tokens
		Output float64 `json:"output"`
	} `json:"cost"`
	Limit struct {
		Context int64 `json:"context"`
	} `json:"limit"`
}

// providerEntry is one connected provider on GET /config/providers. The
// answer also carries each provider's options - the resolved API key among
// them - which is why this struct has no field for them: the decoder drops
// what it is not asked for, and the key never gets past this function.
type providerEntry struct {
	ID     string                `json:"id"`
	Name   string                `json:"name"`
	Models map[string]modelEntry `json:"models"`
}

// providers asks GET /config/providers: every provider that resolved
// credentials, with its whole model list. This is the list OpenCode's own
// picker is built from. GET /api/model is not it - measured against 1.17.13
// it names only the models of the free "opencode" provider, whatever else is
// connected.
func (c *client) providers(ctx context.Context) ([]providerEntry, error) {
	var out struct {
		Providers []providerEntry `json:"providers"`
	}
	if err := c.do(ctx, http.MethodGet, "/config/providers", nil, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

func (c *client) models(ctx context.Context) ([]modelEntry, error) {
	var out struct {
		Data []modelEntry `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/model", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// errNoRoute says the server does not have this endpoint, so there is no point
// reconnecting to it.
var errNoRoute = errors.New("opencode: no such endpoint")

// openStream opens the session-scoped SSE stream. It replays the session's
// whole durable history on every connect, which is what the seq baseline in
// sse.go is for, and it is the stream every decision is made on.
//
// Measured against opencode 1.17.13: this endpoint carries **only** durable
// events. The ephemeral *.delta frames never appear on it - not text.delta,
// not reasoning.delta, not tool.input.delta - which is why openDeltaStream
// below exists.
func (c *client) openStream(ctx context.Context, session string) (io.ReadCloser, error) {
	return c.openSSE(ctx, "/api/session/"+session+"/event")
}

// openDeltaStream opens the server-wide stream, which is the only place the
// ephemeral *.delta frames are published. It replays nothing, so it is used
// for the streaming increments alone: everything a decision rests on comes off
// the session-scoped stream, which replays.
//
// One server runs per chat, so "server-wide" here is this chat plus whatever
// internal session OpenCode opens to title it; the adapter still filters by
// session id, and it ignores anything durable to avoid seeing it twice.
func (c *client) openDeltaStream(ctx context.Context) (io.ReadCloser, error) {
	return c.openSSE(ctx, "/api/event")
}

func (c *client) openSSE(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errNoRoute
		}
		return nil, fmt.Errorf("opencode: open %s: %s: %s", path, resp.Status, serverMessage(raw))
	}
	return resp.Body, nil
}

// ------------------------------------------------------------------ the ring

// ring keeps the last n bytes written to it, so a death can be reported with
// what the server said on its way out without holding a whole build log.
type ring struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) write(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, s...)
	if len(r.buf) > r.max {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.max:]...)
	}
}

func (r *ring) string() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}
