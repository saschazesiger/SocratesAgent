//go:build !windows

// fakeopencode is the fake `opencode` the OpenCode chat adapter is tested
// against: the subset of opencode 1.19.4's classic HTTP API and /event
// stream the adapter uses, with Basic auth on every route, sessions kept in
// a JSON file at OPENCODE_DB (so that a second process, a resume and a
// history rebuild find them), and the turns scripted by the text of the
// user's message (script.go).
//
//	fakeopencode --version
//	fakeopencode db "select 1"          creates or migrates the store
//	fakeopencode serve --hostname 127.0.0.1 --port 0
//
// serve refuses another address and a missing OPENCODE_SERVER_PASSWORD, so
// that the tests notice an adapter that would expose a chat's server.
//
// Like the real one, serve refuses a store that was never migrated, and its
// identity is the account's credentials: OPENCODE_AUTH_CONTENT when it is
// set, otherwise $XDG_DATA_HOME/opencode/auth.json (/whoami says which).
package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "1.19.4"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: fakeopencode serve|db|--version")
		os.Exit(2)
	}
	switch args[0] {
	case "--version", "-v":
		fmt.Println(version)
	case "db":
		if err := migrate(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("1")
	case "serve":
		if err := serve(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command", args[0])
		os.Exit(2)
	}
}

// ------------------------------------------------------------------ store

// store is the whole "database".
type store struct {
	Migrated bool                `json:"migrated"`
	Sessions map[string]*session `json:"sessions"`
}

type session struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parentID,omitempty"`
	Directory string            `json:"directory"`
	Title     string            `json:"title"`
	Revert    map[string]string `json:"revert,omitempty"`
	Messages  []*message        `json:"messages"`
}

type message struct {
	Info  map[string]any   `json:"info"`
	Parts []map[string]any `json:"parts"`
}

func dbPath() string {
	if p := os.Getenv("OPENCODE_DB"); p != "" {
		return p
	}
	return filepath.Join(os.Getenv("XDG_DATA_HOME"), "opencode", "opencode.db")
}

// withStore runs fn on the store under an exclusive lock and writes it back.
func withStore(fn func(st *store) error) error {
	path := dbPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	st := &store{Sessions: map[string]*session{}}
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, st); err != nil {
			return err
		}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]*session{}
	}
	if err := fn(st); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func migrate() error {
	return withStore(func(st *store) error {
		st.Migrated = true
		return nil
	})
}

// identity is the account this process runs as.
func identity() string {
	raw := os.Getenv("OPENCODE_AUTH_CONTENT")
	source := "env"
	if raw == "" {
		b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_DATA_HOME"), "opencode", "auth.json"))
		if err != nil {
			return "none"
		}
		raw, source = string(b), "file"
	}
	var auth map[string]struct {
		Key    string `json:"key"`
		Access string `json:"access"`
	}
	if json.Unmarshal([]byte(raw), &auth) != nil {
		return "unreadable"
	}
	for provider, a := range auth {
		return fmt.Sprintf("%s:%s (%s)", provider, a.Key+a.Access, source)
	}
	return "none"
}

// ---------------------------------------------------------------- ids

var (
	idMu    sync.Mutex
	idLast  int64
	idCount int64
)

func newID(prefix string) string {
	idMu.Lock()
	now := time.Now().UnixMilli()
	if now != idLast {
		idLast, idCount = now, 0
	}
	idCount++
	n := uint64(now)*0x1000 + uint64(idCount)
	idMu.Unlock()
	const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var r [14]byte
	_, _ = rand.Read(r[:])
	b := []byte(fmt.Sprintf("%s_%012x", prefix, n&(1<<48-1)))
	for _, v := range r {
		b = append(b, base62[int(v)%62])
	}
	return string(b)
}

// ------------------------------------------------------------------ server

// srv is one serve process.
type srv struct {
	password string
	mu       sync.Mutex
	subs     map[chan []byte]chan struct{}
	runs     map[string]*run
	pending  map[string]chan string
	// questions are the questions waiting for an answer, as GET /question
	// lists them.
	questions map[string]map[string]any
	// mcp are the local MCP servers that started, mcpConfig every one the
	// config names.
	mcp       map[string]*mcpClient
	mcpConfig map[string]mcpConfig
	// asked are the pending permissions, as GET /permission lists them.
	asked map[string]map[string]any
}

// run is a session's loop while it works.
type run struct {
	cancel chan struct{}
	once   sync.Once
	// done is closed once a prompt's loop has ended and said so.
	done chan struct{}
	// failure is the error a turn of the loop ended with, which OpenCode
	// says again after session.idle: the rejected prompt, with a stack
	// trace.
	failure map[string]any
}

func (r *run) stop() { r.once.Do(func() { close(r.cancel) }) }

// serve listens like `opencode serve --hostname 127.0.0.1 --port 0`. Unlike
// the real one, it refuses anything that would expose a chat's server: another
// address, or no password.
func serve(args []string) error {
	host := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--hostname" {
			host = args[i+1]
		}
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("serve must bind 127.0.0.1, not %q", host)
	}
	if os.Getenv("OPENCODE_SERVER_PASSWORD") == "" {
		return errors.New("serve without OPENCODE_SERVER_PASSWORD")
	}
	if os.Getenv("FAKEOPENCODE_REFUSE") == "1" {
		// A server that cannot start for this account.
		return errors.New("refused to serve")
	}
	var migrated bool
	if err := withStore(func(st *store) error {
		migrated = st.Migrated
		return nil
	}); err != nil {
		return err
	}
	if !migrated {
		return errors.New("Failed query: CREATE TABLE workspace (the database was never migrated)")
	}
	s := &srv{password: os.Getenv("OPENCODE_SERVER_PASSWORD"), subs: map[chan []byte]chan struct{}{},
		runs: map[string]*run{}, pending: map[string]chan string{}, questions: map[string]map[string]any{},
		asked: map[string]map[string]any{}}
	s.startMCP()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	fmt.Printf("opencode server listening on http://%s\n", ln.Addr())
	if os.Getenv("FAKEOPENCODE_IGNORE_TERM") == "1" {
		signalIgnoreTerm()
	}
	go s.heartbeat()
	return http.Serve(ln, s)
}

func (s *srv) heartbeat() {
	for range time.Tick(10 * time.Second) {
		s.publish("server.heartbeat", map[string]any{})
	}
}

// publish sends one event to every /event stream.
func (s *srv) publish(typ string, props any) {
	raw, _ := json.Marshal(map[string]any{"id": newID("evt"), "type": typ, "properties": props})
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- raw:
		default:
		}
	}
}

// dropStreams ends every open /event stream, as a disposed instance does.
func (s *srv) dropStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch, drop := range s.subs {
		close(drop)
		delete(s.subs, ch)
	}
}

func (s *srv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "opencode" || pass != s.password {
		w.Header().Set("WWW-Authenticate", `Basic realm="opencode"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	switch {
	case r.Method == http.MethodGet && path == "global/health":
		writeJSON(w, 200, map[string]any{"healthy": true, "version": version})
	case r.Method == http.MethodGet && path == "event":
		s.events(w, r)
	case r.Method == http.MethodGet && path == "command":
		writeJSON(w, 200, commands)
	case r.Method == http.MethodGet && path == "config/providers":
		writeJSON(w, 200, map[string]any{"providers": []any{map[string]any{"id": "fake", "models": map[string]any{
			"model": map[string]any{"limit": map[string]any{"context": 100000}}}}}, "default": map[string]any{"fake": "model"}})
	case r.Method == http.MethodGet && path == "session/status":
		s.status(w)
	case r.Method == http.MethodGet && path == "question":
		s.mu.Lock()
		list := []map[string]any{}
		for _, q := range s.questions {
			list = append(list, q)
		}
		s.mu.Unlock()
		writeJSON(w, 200, list)
	case r.Method == http.MethodGet && path == "permission":
		s.mu.Lock()
		list := []any{}
		for _, p := range s.asked {
			list = append(list, p)
		}
		s.mu.Unlock()
		writeJSON(w, 200, list)
	case r.Method == http.MethodPost && path == "session":
		s.create(w, r)
	case len(parts) >= 2 && parts[0] == "session":
		s.sessionRoute(w, r, parts[1], parts[2:])
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "permission" && parts[2] == "reply":
		var body struct {
			Reply string `json:"reply"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.resolve(w, parts[1], body.Reply)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "question":
		var body struct {
			Answers [][]string `json:"answers"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answer := "rejected"
		if parts[2] == "reply" {
			raw, _ := json.Marshal(body.Answers)
			answer = string(raw)
		}
		s.resolve(w, parts[1], answer)
	default:
		// What the real server does for a path it has no route for.
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		fmt.Fprint(w, "<!doctype html><title>OpenCode</title>")
	}
}

func (s *srv) resolve(w http.ResponseWriter, id, answer string) {
	s.mu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	delete(s.questions, id)
	delete(s.asked, id)
	s.mu.Unlock()
	if ch == nil {
		notFound(w, "request "+id)
		return
	}
	ch <- answer
	writeJSON(w, 200, true)
}

func (s *srv) events(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("FAKEOPENCODE_EXIT_ON_EVENT") == "1" {
		// A server that dies before its event stream opens.
		os.Exit(4)
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	ch, drop := make(chan []byte, 4096), make(chan struct{})
	s.mu.Lock()
	s.subs[ch] = drop
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	fmt.Fprintf(w, "data: {\"id\":\"%s\",\"type\":\"server.connected\",\"properties\":{}}\n\n", newID("evt"))
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-drop:
			return
		case raw := <-ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *srv) status(w http.ResponseWriter) {
	s.mu.Lock()
	out := map[string]any{}
	for id := range s.runs {
		out[id] = map[string]any{"type": "busy"}
	}
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *srv) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title    string `json:"title"`
		ParentID string `json:"parentID"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sess := &session{ID: newID("ses"), ParentID: body.ParentID, Directory: r.URL.Query().Get("directory"),
		Title: body.Title, Messages: []*message{}}
	if sess.Title == "" {
		sess.Title = "New session - " + time.Now().UTC().Format(time.RFC3339)
	}
	if err := withStore(func(st *store) error {
		st.Sessions[sess.ID] = sess
		return nil
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.publish("session.created", map[string]any{"sessionID": sess.ID, "info": sessionInfo(sess)})
	writeJSON(w, 200, sessionInfo(sess))
}

func sessionInfo(sess *session) map[string]any {
	info := map[string]any{"id": sess.ID, "directory": sess.Directory, "title": sess.Title, "version": version}
	if sess.ParentID != "" {
		info["parentID"] = sess.ParentID
	}
	if len(sess.Revert) > 0 {
		info["revert"] = sess.Revert
	}
	return info
}

func loadSession(id string) (*session, error) {
	var out *session
	err := withStore(func(st *store) error {
		out = st.Sessions[id]
		return nil
	})
	return out, err
}

func (s *srv) sessionRoute(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	if !strings.HasPrefix(id, "ses_") {
		writeJSON(w, 500, map[string]any{"name": "UnknownError", "data": map[string]any{"message": "Unexpected server error."}})
		return
	}
	sess, err := loadSession(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if sess == nil {
		notFound(w, "Session not found: "+id)
		return
	}
	op := ""
	if len(rest) > 0 {
		op = rest[0]
	}
	switch {
	case r.Method == http.MethodGet && op == "":
		writeJSON(w, 200, sessionInfo(sess))
	case r.Method == http.MethodGet && op == "message" && len(rest) == 1:
		writeJSON(w, 200, sess.Messages)
	case r.Method == http.MethodGet && op == "message" && len(rest) == 2:
		for _, m := range sess.Messages {
			if m.Info["id"] == rest[1] {
				writeJSON(w, 200, m)
				return
			}
		}
		notFound(w, "Message not found: "+rest[1])
	case r.Method == http.MethodPost && op == "prompt_async":
		var body promptBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]any{"name": "BadRequest", "data": map[string]any{"message": err.Error()}})
			return
		}
		r := s.prompt(id, body)
		if len(body.Parts) > 0 && body.Parts[0].Text == "/beforeack" && r.done != nil {
			// The turn is over, and said so on /event, before the post is
			// answered.
			<-r.done
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && op == "abort":
		s.mu.Lock()
		run := s.runs[id]
		s.mu.Unlock()
		if run != nil {
			run.stop()
		}
		writeJSON(w, 200, true)
	case r.Method == http.MethodPost && op == "shell":
		s.shell(w, r, id)
	case r.Method == http.MethodPost && op == "command":
		s.command(w, r, id)
	case r.Method == http.MethodPost && op == "summarize":
		s.summarize(w, id)
	case r.Method == http.MethodPost && op == "revert":
		var body struct {
			MessageID string `json:"messageID"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.setRevert(w, id, map[string]string{"messageID": body.MessageID})
	case r.Method == http.MethodPost && op == "unrevert":
		s.setRevert(w, id, nil)
	case r.Method == http.MethodPost && op == "fork":
		var body struct {
			MessageID string `json:"messageID"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.fork(w, sess, body.MessageID)
	default:
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!doctype html><title>OpenCode</title>")
	}
}

func (s *srv) setRevert(w http.ResponseWriter, id string, revert map[string]string) {
	var info map[string]any
	_ = withStore(func(st *store) error {
		st.Sessions[id].Revert = revert
		info = sessionInfo(st.Sessions[id])
		return nil
	})
	s.publish("session.updated", map[string]any{"sessionID": id, "info": info})
	writeJSON(w, 200, info)
}

func (s *srv) fork(w http.ResponseWriter, from *session, upTo string) {
	sess := &session{ID: newID("ses"), Directory: from.Directory, Title: from.Title + " (fork #1)"}
	for _, m := range from.Messages {
		if upTo != "" && m.Info["id"] == upTo {
			break
		}
		sess.Messages = append(sess.Messages, m)
	}
	_ = withStore(func(st *store) error {
		st.Sessions[sess.ID] = sess
		return nil
	})
	writeJSON(w, 200, sessionInfo(sess))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter, msg string) {
	writeJSON(w, 404, map[string]any{"name": "NotFoundError", "data": map[string]any{"message": msg}})
}
