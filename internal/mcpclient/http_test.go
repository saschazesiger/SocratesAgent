package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHTTP is a fake MCP server's state, shared by its HTTP handlers.
type fakeHTTP struct {
	mu       sync.Mutex
	seen     []string // methods, and "answer <id>" for the client's answers
	deleted  []string // the sessions ended with DELETE
	problems []string // what the server found wrong with a request
}

func (f *fakeHTTP) note(list *[]string, s string) {
	f.mu.Lock()
	*list = append(*list, s)
	f.mu.Unlock()
}

func (f *fakeHTTP) check(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.problems {
		t.Error(p)
	}
}

// answer is the fake server's result for a request.
func (f *fakeHTTP) answer(m *message) any {
	switch m.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}, "completions": map[string]any{}},
			"serverInfo":      map[string]any{"name": "remote", "version": "0.9"},
		}
	case "tools/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Cursor == "" {
			return map[string]any{"tools": []map[string]any{{"name": "search", "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": "c2"}
		}
		return map[string]any{"tools": []map[string]any{{"name": "fetch", "inputSchema": map[string]any{"type": "object"}}}}
	}
	return nil
}

// streamable is a streamable HTTP MCP server that answers as JSON, or as
// an event stream when sse is set.
func streamable(t *testing.T, sse bool) (*httptest.Server, *fakeHTTP) {
	f := &fakeHTTP{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			f.note(&f.deleted, r.Header.Get("Mcp-Session-Id"))
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "no", http.StatusMethodNotAllowed)
			return
		}
		if a := r.Header.Get("Accept"); !strings.Contains(a, "application/json") || !strings.Contains(a, "text/event-stream") {
			f.note(&f.problems, "Accept "+a)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Api-Key") != "secret" {
			f.note(&f.problems, fmt.Sprintf("headers %v", r.Header))
		}
		var m message
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if m.Method == "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "" || r.Header.Get("MCP-Protocol-Version") != "" {
				f.note(&f.problems, "initialize came with session headers")
			}
			w.Header().Set("Mcp-Session-Id", "sess-1")
		} else if r.Header.Get("Mcp-Session-Id") != "sess-1" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
			f.note(&f.problems, fmt.Sprintf("%s without session headers: %v", m.Method, r.Header))
		}
		if !m.isRequest() {
			if m.Method != "" {
				f.note(&f.seen, m.Method)
			} else {
				f.note(&f.seen, "answer "+idKey(m.ID)+" "+string(m.Result))
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		f.note(&f.seen, m.Method)
		resp := map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": f.answer(&m)}
		b, _ := json.Marshal(resp)
		if !sse {
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		if m.Method == "tools/list" {
			fmt.Fprint(w, "id: 1\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\n")
			fmt.Fprint(w, "data: \"method\":\"ping\"}\n\n")
		}
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func TestProbeStreamableHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			srv, f := streamable(t, sse)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			info, err := Probe(ctx, Server{Transport: "http", URL: srv.URL + "/mcp", Headers: map[string]string{"X-Api-Key": "secret"}})
			if err != nil {
				t.Fatal(err)
			}
			f.check(t)
			if info.ProtocolVersion != "2025-06-18" || info.ServerName != "remote" || info.ServerVersion != "0.9" {
				t.Errorf("info %+v", info)
			}
			if !slices.Equal(info.Capabilities, []string{"completions", "tools"}) {
				t.Errorf("capabilities %v", info.Capabilities)
			}
			if len(info.Tools) != 2 || info.Tools[0].Name != "search" || info.Tools[1].Name != "fetch" {
				t.Errorf("tools %+v", info.Tools)
			}
			want := []string{"initialize", "notifications/initialized", "tools/list", "tools/list"}
			if sse {
				want = []string{"initialize", "notifications/initialized", "tools/list", "answer srv-1 {}", "tools/list", "answer srv-1 {}"}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if !slices.Equal(f.seen, want) {
				t.Errorf("the server saw %q, want %q", f.seen, want)
			}
			if !slices.Equal(f.deleted, []string{"sess-1"}) {
				t.Errorf("deleted sessions %v", f.deleted)
			}
		})
	}
}

// legacySSE is an MCP server on the legacy HTTP+SSE transport.
func legacySSE(t *testing.T) (*httptest.Server, *fakeHTTP) {
	f := &fakeHTTP{}
	events := make(chan []byte, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("X-Api-Key") != "secret" {
			f.note(&f.problems, fmt.Sprintf("GET headers %v", r.Header))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": hello\n\nevent: endpoint\ndata: messages?sessionId=abc\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case b := <-events:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sessionId") != "abc" || r.Header.Get("X-Api-Key") != "secret" {
			f.note(&f.problems, "POST "+r.URL.String())
		}
		var m message
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.note(&f.seen, m.Method)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "Accepted")
		if m.isRequest() {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": f.answer(&m)})
			events <- b
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func TestProbeLegacySSE(t *testing.T) {
	srv, f := legacySSE(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := Probe(ctx, Server{Transport: "sse", URL: srv.URL + "/sse", Headers: map[string]string{"X-Api-Key": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	f.check(t)
	if info.ServerName != "remote" || len(info.Tools) != 2 {
		t.Errorf("info %+v", info)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []string{"initialize", "notifications/initialized", "tools/list", "tools/list"}; !slices.Equal(f.seen, want) {
		t.Errorf("the server saw %q, want %q", f.seen, want)
	}
}

func TestProbeAuthRequired(t *testing.T) {
	const header = `Bearer error="invalid_token", resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="files:read files:write"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", header)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	for _, transport := range []string{"http", "sse"} {
		_, err := Probe(context.Background(), Server{Transport: transport, URL: srv.URL + "/mcp"})
		var auth *AuthRequiredError
		if !errors.As(err, &auth) {
			t.Fatalf("%s: error %v, want an AuthRequiredError", transport, err)
		}
		if auth.WWWAuthenticate != header || auth.ResourceMetadata != "https://mcp.example.com/.well-known/oauth-protected-resource/mcp" || auth.Scope != "files:read files:write" {
			t.Errorf("%s: %+v", transport, auth)
		}
	}
}

func TestProbeInsufficientScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="admin"`)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := Probe(context.Background(), Server{Transport: "http", URL: srv.URL})
	var auth *AuthRequiredError
	if !errors.As(err, &auth) || auth.Scope != "admin" {
		t.Fatalf("error %v", err)
	}
}

func TestProbeHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rpc-error":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"Bad Request: No valid session ID provided"}}`)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html></html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	for path, want := range map[string]string{
		"/missing":   "the server answered 404 Not Found at " + srv.URL + "/missing: 404 page not found",
		"/rpc-error": "the server answered 400 Bad Request at " + srv.URL + "/rpc-error: Bad Request: No valid session ID provided",
		"/html":      "the server at " + srv.URL + "/html answered with text/html instead of JSON or an event stream",
	} {
		_, err := Probe(context.Background(), Server{Transport: "http", URL: srv.URL + path})
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", path, err, want)
		}
	}
	srv.Close()
	_, err := Probe(context.Background(), Server{Transport: "http", URL: srv.URL})
	if err == nil || !strings.HasPrefix(err.Error(), "could not reach "+srv.URL) {
		t.Errorf("closed server: error %v", err)
	}
}

func TestParseChallenges(t *testing.T) {
	got := parseChallenges(`Basic realm="a, b", Bearer realm="x\"y", Scope="s1 s2", resource_metadata=https://h/p, Negotiate abc==`)
	if len(got) != 3 || got[0].scheme != "Basic" || got[1].scheme != "Bearer" || got[2].scheme != "Negotiate" {
		t.Fatalf("challenges %+v", got)
	}
	if got[0].params["realm"] != "a, b" {
		t.Errorf("basic %+v", got[0].params)
	}
	if p := got[1].params; p["realm"] != `x"y` || p["scope"] != "s1 s2" || p["resource_metadata"] != "https://h/p" {
		t.Errorf("bearer %+v", p)
	}
}

func TestReadSSE(t *testing.T) {
	var got []sseEvent
	err := readSSE(strings.NewReader("retry: 5\n: comment\nevent: a\ndata: 1\ndata:2\n\ndata: last"), func(ev sseEvent) bool {
		got = append(got, ev)
		return true
	})
	if err != nil || len(got) != 2 || got[0] != (sseEvent{"a", "1\n2"}) || got[1] != (sseEvent{"", "last"}) {
		t.Errorf("events %+v, error %v", got, err)
	}
}

func TestLegacySSERefusesForeignEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: endpoint\ndata: https://elsewhere.example.com/messages\n\n")
	}))
	defer srv.Close()
	_, err := Probe(context.Background(), Server{Transport: "sse", URL: srv.URL + "/sse"})
	if err == nil || !strings.Contains(err.Error(), "on another host") {
		t.Fatalf("error %v", err)
	}
}

func TestProbeHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Probe(ctx, Server{Transport: "http", URL: srv.URL})
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "the server did not answer initialize in time" {
		t.Fatalf("error %v", err)
	}
}
