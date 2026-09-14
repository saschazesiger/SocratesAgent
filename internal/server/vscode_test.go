package server

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func TestVSCodeProxy(t *testing.T) {
	e := newEnv(t)
	e.do(t, e.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	dir, err := os.MkdirTemp("", "soc-ide-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e.srv.dataDir = dir
	row := &store.Session{ID: "editor", Harness: "vscode", Workdir: dir, WorkdirMode: "custom", State: store.StateRunning}
	if err := e.store.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	sock := harnesses.VSCodeSocket(dir, row.ID)
	if err := os.MkdirAll(filepath.Dir(sock), 0700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/socket" {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			kind, data, err := conn.Read(r.Context())
			if err == nil {
				conn.Write(r.Context(), kind, data)
			}
			return
		}
		w.Header().Set("X-Upstream-Path", r.URL.RequestURI())
		io.Copy(w, r.Body)
	})}
	defer upstream.Close()
	go upstream.Serve(ln)
	for _, tc := range []struct {
		name, method, origin string
		client               *http.Client
		status               int
	}{
		{"anonymous", "GET", "", e.anon, 401},
		{"assets", "GET", "", e.client, 200},
		{"write", "POST", e.server.URL, e.client, 200},
		{"cross-origin websocket", "GET", "https://other.example", e.client, 403},
		{"cross-origin write", "POST", "https://other.example", e.client, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, e.server.URL+"/ide/editor/static/file.js?q=one", nil)
			req.Header.Set("Origin", tc.origin)
			res, err := tc.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("status %d", res.StatusCode)
			}
			if tc.status == 200 && res.Header.Get("X-Upstream-Path") != "/static/file.js?q=one" {
				t.Fatal("proxy lost path or query")
			}
		})
	}
	t.Run("websocket round trip", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		target := "ws" + strings.TrimPrefix(e.server.URL, "http") + "/ide/editor/socket"
		conn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: e.client, HTTPHeader: http.Header{"Origin": {e.server.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		if err := conn.Write(ctx, websocket.MessageText, []byte("IDE input")); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil || string(data) != "IDE input" {
			t.Fatalf("echo %q: %v", data, err)
		}
	})
	if err := e.store.CreateSession(&store.Session{ID: "shell", Harness: "shell", Workdir: dir, WorkdirMode: "custom", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"shell", "missing"} {
		res, err := e.client.Get(e.server.URL + "/ide/" + target + "/")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("%s: %d", target, res.StatusCode)
		}
	}

}
