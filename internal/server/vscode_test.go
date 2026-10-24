//go:build !windows

package server

import (
	"context"
	"fmt"
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

// TestFolderIDE holds a folder to one editor: the sheet cannot make one, the
// folder's mark makes it once and finds it after that, and a folder no
// session works in is not a folder the route will open.
func TestFolderIDE(t *testing.T) {
	e := newSessionEnv(t)
	res, payload := e.do(t, e.client, "PUT", "/api/settings", `{"settings":{"harnesses":{"vscode":{"binary":"/bin/sh"}}}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %#v", res.StatusCode, payload)
	}
	project := filepath.Join(e.work, "project")
	res, session := e.create(fmt.Sprintf(`{"harness":"vscode","workdir_mode":"custom","workdir":%q}`, project))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("VS Code must not be a new session, got %d %#v", res.StatusCode, session)
	}

	open := func(folder string) (*http.Response, map[string]any) {
		t.Helper()
		res, payload := e.do(t, e.client, "POST", "/api/ide", fmt.Sprintf(`{"workdir":%q}`, folder))
		session, _ := payload["session"].(map[string]any)
		return res, session
	}
	if res, _ := open(project); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a folder no session works in should be refused, got %d", res.StatusCode)
	}
	res, shell := e.create(fmt.Sprintf(`{"harness":"shell","workdir_mode":"custom","workdir":%q}`, project))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("shell: %d %#v", res.StatusCode, shell)
	}
	res, first := open(project + "/")
	if res.StatusCode != http.StatusCreated || first["harness"] != "vscode" || first["workdir"] != project || first["workdir_mode"] != "custom" {
		t.Fatalf("first open: %d %#v", res.StatusCode, first)
	}
	res, again := open(project)
	if res.StatusCode != http.StatusOK || sessionID(t, again) != sessionID(t, first) {
		t.Fatalf("a folder has one editor: %d %v and %v", res.StatusCode, again["id"], first["id"])
	}
	// Closing the editor and opening it again gives the same id, which is
	// the address the browser keeps the workbench's memory under.
	if sessionID(t, first) != harnesses.VSCodeSessionID(project) {
		t.Fatalf("the editor's id is not the folder's: %v", first["id"])
	}
	if res, _ := e.do(t, e.client, "DELETE", "/api/sessions/"+sessionID(t, first), ""); res.StatusCode != http.StatusOK {
		t.Fatalf("close: %d", res.StatusCode)
	}
	res, reopened := open(project)
	if res.StatusCode != http.StatusCreated || sessionID(t, reopened) != sessionID(t, first) {
		t.Fatalf("a reopened editor must keep its address: %d %v", res.StatusCode, reopened["id"])
	}
}

func TestVSCodeDataIsPerFolder(t *testing.T) {
	data := t.TempDir()
	if harnesses.VSCodeDataDir(data, "/srv/a/") != harnesses.VSCodeDataDir(data, "/srv/a") {
		t.Fatal("two spellings of one folder must share an editor state")
	}
	if harnesses.VSCodeDataDir(data, "/srv/a") == harnesses.VSCodeDataDir(data, "/srv/b") {
		t.Fatal("two folders must not share an editor state")
	}
}

// TestBlankIDE: the empty editor is made once, found after that, works in a
// directory of its own, and comes back under the same address once closed.
func TestBlankIDE(t *testing.T) {
	e := newSessionEnv(t)
	res, payload := e.do(t, e.client, "PUT", "/api/settings", `{"settings":{"harnesses":{"vscode":{"binary":"/bin/sh"}}}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d %#v", res.StatusCode, payload)
	}
	open := func() (*http.Response, map[string]any) {
		t.Helper()
		res, payload := e.do(t, e.client, "POST", "/api/ide/blank", "")
		session, _ := payload["session"].(map[string]any)
		return res, session
	}
	res, first := open()
	if res.StatusCode != http.StatusCreated || first["harness"] != "vscode" || first["workdir_mode"] != "dynamic" {
		t.Fatalf("first open: %d %#v", res.StatusCode, first)
	}
	if sessionID(t, first) != harnesses.VSCodeBlankID {
		t.Fatalf("the empty editor's id is not fixed: %v", first["id"])
	}
	if info, err := os.Stat(first["workdir"].(string)); err != nil || !info.IsDir() {
		t.Fatalf("the empty editor has no directory: %v", err)
	}
	res, again := open()
	if res.StatusCode != http.StatusOK || sessionID(t, again) != sessionID(t, first) {
		t.Fatalf("there is one empty editor: %d %v and %v", res.StatusCode, again["id"], first["id"])
	}
	if res, _ := e.do(t, e.client, "DELETE", "/api/sessions/"+sessionID(t, first), ""); res.StatusCode != http.StatusOK {
		t.Fatalf("close: %d", res.StatusCode)
	}
	res, reopened := open()
	if res.StatusCode != http.StatusCreated || sessionID(t, reopened) != sessionID(t, first) {
		t.Fatalf("a reopened empty editor must keep its address: %d %v", res.StatusCode, reopened["id"])
	}
}
