package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeEnv selects the fake stdio server the test binary plays when it is
// started by fakeStdio.
const fakeEnv = "MCPCLIENT_FAKE_STDIO"

// fakeStdio is a stdio server that runs this test binary as the fake
// server variant.
func fakeStdio(variant string) Server {
	return Server{
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestFakeStdioServer$"},
		Env:       map[string]string{fakeEnv: variant, "MCPCLIENT_FAKE_NAME": "fake-" + variant},
	}
}

// TestFakeStdioServer is not a test: it is the fake stdio server, when the
// test binary is started by fakeStdio. It returns rather than exits, so that
// testhome removes the helper's home again.
func TestFakeStdioServer(t *testing.T) {
	variant := os.Getenv(fakeEnv)
	if variant == "" {
		t.Skip("the fake stdio server runs only as a helper process")
	}
	runFakeStdio(t, variant)
}

// runFakeStdio serves MCP on standard input and output. The testing
// package writes only to standard output, which the client ignores.
func runFakeStdio(t *testing.T, variant string) {
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
	switch variant {
	case "crash":
		fmt.Fprintln(os.Stderr, "fatal: MISSING_API_KEY is not set")
		t.FailNow() // the test binary exits with status 1
	case "hang":
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
		fmt.Fprintln(os.Stderr, "waiting for something that never comes")
		<-stop
		return
	case "junk":
		fmt.Fprintln(out, "Starting the fake server ...")
		fmt.Fprintln(out, "{not json at all")
		fmt.Fprintln(out, "[1, 2")
		fmt.Fprintln(out, `{"jsonrpc":"2.0","id":99,"result":{}}`)
		out.Flush()
	}
	fmt.Fprintln(os.Stderr, "fake server started")

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1<<20)
	next := func() *message {
		for in.Scan() {
			var m message
			if json.Unmarshal(in.Bytes(), &m) == nil {
				return &m
			}
		}
		return nil
	}
	for {
		m := next()
		if m == nil {
			return
		}
		var params struct {
			Cursor          string `json:"cursor"`
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(m.Params, &params)
		result := func(v any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": v}) }
		switch m.Method {
		case "initialize":
			fmt.Fprintf(os.Stderr, "client %s asked for %s\n", params.ClientInfo.Name, params.ProtocolVersion)
			result(map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}, "prompts": map[string]any{}, "resources": map[string]any{}, "logging": map[string]any{}},
				"serverInfo":      map[string]any{"name": os.Getenv("MCPCLIENT_FAKE_NAME"), "title": "Fake Server", "version": "1.2.3"},
				"instructions":    "Use it well.",
			})
		case "notifications/initialized":
			fmt.Fprintln(os.Stderr, "initialized")
		case "tools/list":
			if params.Cursor == "" {
				// A notification and a request of the server's own first.
				send(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info", "data": "hello"}})
				send(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "ping"})
				if r := next(); r != nil && idKey(r.ID) == "srv-1" && string(r.Result) == "{}" {
					fmt.Fprintln(os.Stderr, "ping answered")
				}
				send(map[string]any{"jsonrpc": "2.0", "id": "srv-2", "method": "roots/list"})
				if r := next(); r != nil && idKey(r.ID) == "srv-2" && r.Error != nil && r.Error.Code == -32601 {
					fmt.Fprintln(os.Stderr, "roots refused")
				}
				result(map[string]any{
					"tools": []map[string]any{
						{"name": "alpha", "description": "First.", "inputSchema": map[string]any{"type": "object"}},
						{"name": "beta", "title": "Beta", "inputSchema": map[string]any{"type": "object"}},
					},
					"nextCursor": "page-2",
				})
			} else {
				result(map[string]any{"tools": []map[string]any{
					{"name": "gamma", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"title": "Gamma", "readOnlyHint": true}},
				}})
			}
		case "prompts/list":
			result(map[string]any{"prompts": []map[string]any{{"name": "review", "description": "Review code."}}})
		case "resources/list":
			result(map[string]any{"resources": []map[string]any{{"uri": "file:///notes.txt", "name": "notes", "mimeType": "text/plain"}}})
		default:
			if m.isRequest() {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			}
		}
	}
}

func TestProbeStdio(t *testing.T) {
	for _, variant := range []string{"ok", "junk"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			info, err := Probe(ctx, fakeStdio(variant))
			if err != nil {
				t.Fatalf("Probe: %v (stderr %q)", err, info.Stderr)
			}
			if info.ProtocolVersion != "2025-06-18" || info.ServerName != "fake-"+variant || info.ServerTitle != "Fake Server" || info.ServerVersion != "1.2.3" || info.Instructions != "Use it well." {
				t.Errorf("server info: %+v", info)
			}
			if want := []string{"logging", "prompts", "resources", "tools"}; !slices.Equal(info.Capabilities, want) {
				t.Errorf("capabilities %v, want %v", info.Capabilities, want)
			}
			var names []string
			for _, tool := range info.Tools {
				names = append(names, tool.Name)
			}
			if !slices.Equal(names, []string{"alpha", "beta", "gamma"}) {
				t.Fatalf("tools %v, want every page", names)
			}
			if info.Tools[1].Title != "Beta" || info.Tools[2].Title != "Gamma" || info.Tools[2].Annotations["readOnlyHint"] != true {
				t.Errorf("tool titles or annotations: %+v", info.Tools)
			}
			if string(info.Tools[0].InputSchema) != `{"type":"object"}` {
				t.Errorf("input schema %s", info.Tools[0].InputSchema)
			}
			if len(info.Prompts) != 1 || info.Prompts[0].Name != "review" {
				t.Errorf("prompts %+v", info.Prompts)
			}
			if len(info.Resources) != 1 || info.Resources[0].URI != "file:///notes.txt" || info.Resources[0].MimeType != "text/plain" {
				t.Errorf("resources %+v", info.Resources)
			}
			if info.Latency <= 0 {
				t.Errorf("latency %v", info.Latency)
			}
			for _, want := range []string{"client socrates asked for " + ProtocolVersion, "initialized", "ping answered", "roots refused"} {
				if !strings.Contains(info.Stderr, want) {
					t.Errorf("stderr %q lacks %q", info.Stderr, want)
				}
			}
		})
	}
}

func TestProbeStdioCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := Probe(ctx, fakeStdio("crash"))
	if err == nil {
		t.Fatal("Probe of a crashing server succeeded")
	}
	want := "the process exited (status 1) before it answered: fatal: MISSING_API_KEY is not set"
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if info == nil || !strings.Contains(info.Stderr, "MISSING_API_KEY") {
		t.Errorf("info %+v lacks the stderr", info)
	}
}

func TestProbeStdioTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	info, err := Probe(ctx, fakeStdio("hang"))
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "the server did not answer initialize in time" {
		t.Fatalf("error %v, want a timeout", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("Probe took %v to give up", took)
	}
	if info == nil || !strings.Contains(info.Stderr, "never comes") {
		t.Errorf("info %+v lacks the stderr", info)
	}
}

func TestProbeStdioNotFound(t *testing.T) {
	_, err := Probe(context.Background(), Server{Transport: "stdio", Command: "socrates-no-such-command"})
	if err == nil || err.Error() != "the command socrates-no-such-command was not found" {
		t.Fatalf("error %v", err)
	}
	_, err = Probe(context.Background(), Server{Transport: "stdio", Command: os.Args[0], Dir: "/no/such/folder"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error %v", err)
	}
}

func TestTailBuffer(t *testing.T) {
	var b tailBuffer
	b.Write([]byte(strings.Repeat("a", 3000)))
	b.Write([]byte(strings.Repeat("ü", 1000))) // 2000 bytes
	s := b.String()
	if len(s) > stderrKeep || !strings.HasSuffix(s, "üü") || strings.HasPrefix(s, "\xbc") {
		t.Errorf("tail of %d bytes starts %q", len(s), s[:4])
	}
	b.Write([]byte(strings.Repeat("x", 5000)))
	if b.String() != strings.Repeat("x", stderrKeep) {
		t.Error("a long write did not replace the tail")
	}
}
