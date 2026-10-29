package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is the kv table in memory; plainSealer marks what it seals.
type memStore struct {
	mu sync.Mutex
	kv map[string]string
}

func (m *memStore) GetKV(k string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.kv[k], nil
}

func (m *memStore) SetKV(k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[k] = v
	return nil
}

type plainSealer struct{}

func (plainSealer) Seal(object string, plain []byte) ([]byte, error) {
	return append([]byte(object+"|"), plain...), nil
}

func (plainSealer) Open(object string, sealed []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(string(sealed), object+"|")
	if !ok {
		return nil, errors.New("sealed for another object")
	}
	return []byte(rest), nil
}

// echo is a server with one tool and accounts.
type echo struct{ calls []Session }

func (e *echo) Name() string         { return "echo" }
func (e *echo) Title() string        { return "Echo" }
func (e *echo) Description() string  { return "Says it back." }
func (e *echo) Instructions() string { return "Use say." }
func (e *echo) Tools() []Tool {
	return []Tool{{Name: "say", Description: "Says it back.", Schema: map[string]any{"type": "object"}}}
}

func (e *echo) Call(ctx context.Context, s Session, tool string, args json.RawMessage) Result {
	e.calls = append(e.calls, s)
	var a struct {
		Text string `json:"text"`
		Wait int    `json:"wait"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Wait > 0 {
		select {
		case <-time.After(time.Duration(a.Wait) * time.Millisecond):
		case <-ctx.Done():
			return Error("withdrawn")
		}
	}
	return Text(s.Holder() + ": " + a.Text)
}

func (e *echo) AccountFields() []Field {
	return []Field{{Name: "user", Label: "User"}, {Name: "key", Label: "Key", Secret: true}}
}
func (e *echo) AccountLabel(v map[string]string) string { return v["user"] }
func (e *echo) CheckAccount(_ context.Context, v map[string]string) Check {
	switch v["key"] {
	case "bad":
		return Check{State: AccountInvalid, Detail: "refused"}
	case "offline":
		return Check{State: AccountUnknown, Detail: "no answer"}
	}
	return Check{State: AccountValid, Detail: "fine"}
}

func TestAccountsAreCheckedSealedAndKept(t *testing.T) {
	st := &memStore{kv: map[string]string{}}
	r := NewRegistry(st, plainSealer{})
	r.Register(&echo{})
	if _, err := r.AddAccount(context.Background(), "echo", map[string]string{"user": "jane"}); err == nil || !strings.Contains(err.Error(), "Key is missing") {
		t.Fatalf("a missing field: %v", err)
	}
	if _, err := r.AddAccount(context.Background(), "echo", map[string]string{"user": "jane", "key": "bad"}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refused account: %v", err)
	}
	a, err := r.AddAccount(context.Background(), "echo", map[string]string{"user": " jane ", "key": "good"})
	if err != nil || a.Check.State != AccountValid || a.Values["user"] != "jane" {
		t.Fatalf("added: %+v %v", a, err)
	}
	if _, err := r.AddAccount(context.Background(), "echo", map[string]string{"user": "jane", "key": "other"}); err == nil {
		t.Fatal("a second jane was kept")
	}
	b, err := r.AddAccount(context.Background(), "echo", map[string]string{"user": "joe", "key": "offline"})
	if err != nil || b.Check.State != AccountUnknown {
		t.Fatalf("an account that could not be checked is kept: %+v %v", b, err)
	}
	raw := st.kv["mcp.echo.accounts"]
	if raw == "" || strings.Contains(raw, "good") {
		t.Fatalf("stored in the clear or not at all: %q", raw)
	}
	// A registry that starts again reads them back.
	again := NewRegistry(st, plainSealer{})
	again.Register(&echo{})
	list := again.Accounts("echo")
	if len(list) != 2 || list[0].Values["key"] != "good" || list[1].Values["user"] != "joe" {
		t.Fatalf("read back: %+v", list)
	}
	if err := again.RemoveAccount("echo", a.ID); err != nil || len(again.Accounts("echo")) != 1 {
		t.Fatalf("remove: %v", err)
	}
	if _, err := again.CheckAccount(context.Background(), "echo", b.ID); err != nil {
		t.Fatal(err)
	}
}

// serve answers the requests of `socrates mcp-server` on a Unix socket as
// the Socrates server does.
func serve(t *testing.T, r *Registry) *Endpoint {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "t.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix socket: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+EndpointPath, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer chat-1.token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var q Request
		_ = json.NewDecoder(req.Body).Decode(&q)
		w.Header().Set("Content-Type", "application/json")
		if q.Method == "describe" {
			_ = json.NewEncoder(w).Encode(Describe(r.Get(q.Server)))
			return
		}
		_ = json.NewEncoder(w).Encode(r.Call(req.Context(), q.Server, Session{ID: "chat-1"}, q.Tool, q.Arguments))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &Endpoint{Socket: sock, Token: "chat-1.token"}
}

func TestStdioHandsEveryCallToTheServer(t *testing.T) {
	r := NewRegistry(nil, nil)
	e := &echo{}
	r.Register(e)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeStdio(context.Background(), serve(t, r), "echo", "test", inR, outW) }()
	lines := bufio.NewScanner(outR)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(msg string) { _, _ = io.WriteString(inW, msg+"\n") }
	read := func() map[string]any {
		t.Helper()
		if !lines.Scan() {
			t.Fatal("no answer")
		}
		var m map[string]any
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	init := read()["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || init["instructions"] != "Use say." {
		t.Fatalf("initialize: %v", init)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := read()["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "say" {
		t.Fatalf("tools: %v", tools)
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"say","arguments":{"text":"hi"}}}`)
	res := read()["result"].(map[string]any)
	if text := res["content"].([]any)[0].(map[string]any)["text"]; text != "chat-1: hi" {
		t.Fatalf("call: %v", res)
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"shout","arguments":{}}}`)
	if res := read()["result"].(map[string]any); res["isError"] != true {
		t.Fatalf("an unknown tool: %v", res)
	}
	// A call the client withdraws gets no answer; the next one does.
	send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"say","arguments":{"text":"slow","wait":5000}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`)
	send(`{"jsonrpc":"2.0","id":6,"method":"ping"}`)
	if m := read(); m["id"].(float64) != 6 {
		t.Fatalf("after the cancel: %v", m)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
