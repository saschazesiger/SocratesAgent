// Command fakeopencode imitates `opencode serve`: an HTTP + SSE server behind
// HTTP Basic auth.
//
// It is installed on PATH under the name "opencode" by fakes.Build. Its
// behaviour is driven entirely by FAKE_SCRIPT; see the fakes package doc.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness/fakes/script"
)

const heartbeatInterval = 2 * time.Second

type server struct {
	steps []script.Step

	user string
	pass string

	mu      sync.Mutex
	seq     map[string]int64
	log     map[string][]json.RawMessage
	subs    map[string][]chan json.RawMessage
	active  map[string]bool
	dirs    map[string]string
	sessN   int
	evtN    atomic.Int64
	msgN    atomic.Int64
	callN   atomic.Int64
	stepMsg map[string]string // session -> open step's assistantMessageID

	runMu  sync.Mutex
	turnMu sync.Mutex
	cur    map[string]*turnState
}

type turnState struct {
	once   sync.Once
	done   chan struct{}
	cancel func()
}

func main() {
	script.Record(append(append([]string{}, os.Args...),
		"OPENCODE_PERMISSION="+os.Getenv("OPENCODE_PERMISSION")))

	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "fakeopencode: expected `serve`")
		os.Exit(2)
	}

	port := value(os.Args[2:], "--port")
	host := value(os.Args[2:], "--hostname")
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "0"
	}

	s := &server{
		steps:   script.MustLoad(),
		user:    envOr("OPENCODE_SERVER_USERNAME", "opencode"),
		pass:    os.Getenv("OPENCODE_SERVER_PASSWORD"),
		seq:     map[string]int64{},
		log:     map[string][]json.RawMessage{},
		subs:    map[string][]chan json.RawMessage{},
		active:  map[string]bool{},
		dirs:    map[string]string{},
		stepMsg: map[string]string{},
		cur:     map[string]*turnState{},
	}

	// Installed before the listener so a SIGTERM that arrives the instant the
	// startup line is read is still handled by us rather than by the default
	// disposition.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sig; os.Exit(0) }()

	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeopencode:", err)
		os.Exit(1)
	}
	_, actual, _ := net.SplitHostPort(ln.Addr().String())
	fmt.Printf("opencode server listening on http://%s:%s\n", host, actual)
	os.Stdout.Sync()

	srv := &http.Server{Handler: s.routes()}
	_ = srv.Serve(ln)
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"healthy": true})
	})
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": models()})
	})
	mux.HandleFunc("POST /api/session", s.createSession)
	mux.HandleFunc("GET /api/session/active", s.activeSessions)
	mux.HandleFunc("POST /api/session/{id}/model", s.setModel)
	mux.HandleFunc("POST /api/session/{id}/prompt", s.prompt)
	mux.HandleFunc("POST /api/session/{id}/interrupt", s.interrupt)
	mux.HandleFunc("POST /api/session/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 503, map[string]any{
			"_tag":    "ServiceUnavailableError",
			"message": "Session wait is not available yet",
			"service": "session.wait",
		})
	})
	mux.HandleFunc("POST /api/session/{id}/permission/{req}/reply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/session/{id}/event", s.events)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"_tag": "NotFoundError", "message": r.URL.Path})
	})
	// FK-24: every route, /api/health included, is behind Basic auth.
	return s.auth(mux)
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.pass != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != s.user || p != s.pass {
				writeJSON(w, 401, map[string]any{
					"_tag":    "UnauthorizedError",
					"message": "Authentication required",
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- endpoints

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)

	s.mu.Lock()
	s.sessN++
	id := fmt.Sprintf("ses_fake%04d", s.sessN)
	s.dirs[id] = body.Location.Directory
	s.mu.Unlock()

	writeJSON(w, 200, map[string]any{"data": map[string]any{
		"id":       id,
		"location": map[string]any{"directory": body.Location.Directory},
		"title":    "fake session",
		"version":  "1.17.13-fake",
		"time":     map[string]any{"created": nowMS(), "updated": nowMS()},
	}})
}

// setModel is FK-23: 204 always, and the body is recorded verbatim.
func (s *server) setModel(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	script.Record([]string{"POST /api/session/" + r.PathValue("id") + "/model", string(raw)})
	w.WriteHeader(204)
}

func (s *server) activeSessions(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	s.mu.Lock()
	for id, on := range s.active {
		if on {
			out[id] = map[string]any{"type": "running"}
		}
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *server) prompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Prompt struct {
			Text string `json:"text"`
		} `json:"prompt"`
		Delivery string `json:"delivery"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)

	msgID := fmt.Sprintf("msg_fake%04d", s.msgN.Add(1))

	// F-9: admittedSeq is exactly the prompt.admitted event's durable.seq, so
	// the event has to be emitted under the lock that hands out the number.
	s.mu.Lock()
	s.active[id] = true
	admitted := s.emitLocked(id, "session.next.prompt.admitted", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "messageID": msgID,
		"prompt": map[string]any{"text": body.Prompt.Text}, "delivery": body.Delivery,
	}, true)
	s.mu.Unlock()

	writeJSON(w, 200, map[string]any{"data": map[string]any{
		"admittedSeq": admitted,
		"id":          msgID,
		"sessionID":   id,
		"prompt":      map[string]any{"text": body.Prompt.Text},
		"delivery":    body.Delivery,
		"timeCreated": nowMS(),
	}})

	go s.runTurn(id, msgID)
}

// interrupt is FK-22: active empties immediately and no step.ended follows.
func (s *server) interrupt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.WriteHeader(204)

	s.turnMu.Lock()
	t := s.cur[id]
	s.turnMu.Unlock()
	if t != nil {
		t.cancel()
	}
	s.mu.Lock()
	s.active[id] = false
	delete(s.stepMsg, id)
	s.mu.Unlock()
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

	ch := make(chan json.RawMessage, 1024)
	s.mu.Lock()
	// S-2: an unknown ses_ id is an existing, empty session.
	replay := append([]json.RawMessage{}, s.log[id]...)
	s.subs[id] = append(s.subs[id], ch)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		out := s.subs[id][:0]
		for _, c := range s.subs[id] {
			if c != ch {
				out = append(out, c)
			}
		}
		s.subs[id] = out
		s.mu.Unlock()
	}()

	// server.connected is the first frame of every connection.
	connected, _ := json.Marshal(map[string]any{
		"id": s.eventID(), "type": "server.connected", "data": map[string]any{},
	})
	fmt.Fprintf(w, "data: %s\n\n", connected)
	for _, ev := range replay {
		fmt.Fprintf(w, "data: %s\n\n", ev)
	}
	flusher.Flush()

	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", ev)
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------- the turn

func (s *server) runTurn(id, msgID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	done := make(chan struct{})
	var once sync.Once
	t := &turnState{done: done, cancel: func() { once.Do(func() { close(done) }) }}
	s.turnMu.Lock()
	s.cur[id] = t
	s.turnMu.Unlock()
	defer func() {
		s.turnMu.Lock()
		if s.cur[id] == t {
			delete(s.cur, id)
		}
		s.turnMu.Unlock()
	}()

	s.emit(id, "session.next.prompted", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "messageID": msgID,
	}, true)

	dead := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}

	for _, st := range s.steps {
		if dead() {
			return
		}
		switch st.Do {
		case script.DoText:
			s.text(id, st.Text)
		case script.DoReason:
			s.reasoning(id, st.Text)
		case script.DoTool:
			s.tool(id, st.Name, st.Input, st.Output, st.Exit)
		case script.DoSubagent:
			// OpenCode never reports subagents on this stream; a subagent
			// step is a plain tool call.
			s.tool(id, "task", st.Input, st.Output, 0)
		case script.DoAsk:
			// Approvals are an OpenCode permission event; with
			// OPENCODE_PERMISSION="allow" they never fire, so nothing is
			// emitted here.
		case script.DoSleep:
			if !sleep(done, st.MS) {
				return
			}
		case script.DoDie:
			// FK-2: flush before exiting. For an HTTP fake that means giving
			// the SSE writers a moment to put the pending frames on the wire.
			time.Sleep(100 * time.Millisecond)
			os.Stdout.Sync()
			os.Exit(st.Code)
		case script.DoHang:
			return
		case script.DoEnd:
			s.end(id, st)
			return
		}
	}
}

func (s *server) end(id string, st script.Step) {
	switch st.Outcome {
	case script.OutcomeError:
		// FK-21: session.error, no step.ended, active empties 150 ms later.
		s.emit(id, "session.error", map[string]any{
			"sessionID": id,
			"error": map[string]any{
				"name": "UnsupportedApiError",
				"data": map[string]any{"message": st.Error},
			},
		}, true)
		time.Sleep(150 * time.Millisecond)
		s.mu.Lock()
		s.active[id] = false
		delete(s.stepMsg, id)
		s.mu.Unlock()
	default:
		if st.Twice {
			// FK-22: active empties first, then step.ended arrives twice, so
			// closeTurn's sync.Once has two late triggers to swallow.
			s.mu.Lock()
			s.active[id] = false
			s.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			s.stepEnded(id, "stop")
			time.Sleep(200 * time.Millisecond)
			s.stepEnded(id, "stop")
			s.mu.Lock()
			delete(s.stepMsg, id)
			s.mu.Unlock()
			return
		}
		s.stepEnded(id, "stop")
		s.mu.Lock()
		s.active[id] = false
		delete(s.stepMsg, id)
		s.mu.Unlock()
	}
}

// step returns the open step's assistantMessageID, starting a step if none is
// open. Every step's textID counting restarts at text-0 (F-12).
func (s *server) step(id string) string {
	s.mu.Lock()
	msg := s.stepMsg[id]
	s.mu.Unlock()
	if msg != "" {
		return msg
	}
	msg = fmt.Sprintf("msg_step%04d", s.msgN.Add(1))
	s.mu.Lock()
	s.stepMsg[id] = msg
	s.mu.Unlock()
	s.emit(id, "session.next.step.started", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"agent": "build",
		"model": map[string]any{"id": "fake-model", "providerID": "opencode", "variant": "default"},
	}, true)
	return msg
}

func (s *server) stepEnded(id, finish string) {
	msg := s.step(id)
	s.emit(id, "session.next.step.ended", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"finish": finish, "cost": 0.002,
		"tokens": map[string]any{
			"input": 3693, "output": 62, "reasoning": 0,
			"cache": map[string]any{"read": 0, "write": 0},
		},
	}, true)
	s.mu.Lock()
	delete(s.stepMsg, id)
	s.mu.Unlock()
}

func (s *server) text(id, text string) {
	msg := s.step(id)
	s.emit(id, "session.next.text.started", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg, "textID": "text-0",
	}, true)
	for _, c := range script.Chunks(text, 3) {
		s.emit(id, "session.next.text.delta", map[string]any{
			"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
			"textID": "text-0", "delta": c,
		}, false)
	}
	s.emit(id, "session.next.text.ended", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"textID": "text-0", "text": text,
	}, true)
}

// reasoning is S-2: the OpenAPI schema's shape, design-chosen because the
// research never saw these events fire.
func (s *server) reasoning(id, text string) {
	msg := s.step(id)
	s.emit(id, "session.next.reasoning.started", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"reasoningID": "reasoning-0",
	}, true)
	for _, c := range script.Chunks(text, 3) {
		s.emit(id, "session.next.reasoning.delta", map[string]any{
			"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
			"reasoningID": "reasoning-0", "delta": c,
		}, false)
	}
	s.emit(id, "session.next.reasoning.ended", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"reasoningID": "reasoning-0", "text": text,
	}, true)
}

// tool is FK-19's tool half plus FK-20's result shapes. The step it belongs to
// is closed with finish "tool-calls".
func (s *server) tool(id, name, input, output string, exit int) {
	if name == "" {
		name = "bash"
	}
	msg := s.step(id)
	call := fmt.Sprintf("toolu_fake%04d", s.callN.Add(1))
	args, _ := json.Marshal(map[string]any{"command": input})

	s.emit(id, "session.next.tool.input.started", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"callID": call, "name": name,
	}, true)
	s.emit(id, "session.next.tool.input.delta", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"callID": call, "delta": string(args),
	}, false)
	s.emit(id, "session.next.tool.input.ended", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"callID": call, "text": string(args),
	}, true)
	s.emit(id, "session.next.tool.called", map[string]any{
		"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
		"callID": call, "tool": name,
		"input":    map[string]any{"command": input},
		"provider": map[string]any{"executed": false},
	}, true)

	if exit == 0 {
		s.emit(id, "session.next.tool.success", map[string]any{
			"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
			"callID":     call,
			"structured": map[string]any{"exit": exit, "truncated": false},
			"content": []any{
				map[string]any{"type": "text", "text": output},
				map[string]any{"type": "text", "text": fmt.Sprintf("Command exited with code %d.", exit)},
			},
			"outputPaths": []any{},
			"provider":    map[string]any{"executed": false},
		}, true)
	} else {
		// FK-20: design-chosen error shape, verified by WP4's first live run.
		s.emit(id, "session.next.tool.error", map[string]any{
			"timestamp": nowMS(), "sessionID": id, "assistantMessageID": msg,
			"callID": call, "error": output,
		}, true)
	}
	s.stepEnded(id, "tool-calls")
}

// ------------------------------------------------------------------- SSE

func (s *server) emit(session, typ string, data any, durable bool) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emitLocked(session, typ, data, durable)
}

func (s *server) emitLocked(session, typ string, data any, durable bool) int64 {
	ev := map[string]any{
		"id":       s.eventID(),
		"type":     typ,
		"location": map[string]any{"directory": s.dirs[session]},
		"data":     data,
	}
	var seq int64
	if durable {
		s.seq[session]++
		seq = s.seq[session]
		ev["durable"] = map[string]any{
			"aggregateID": session, "seq": seq, "version": 1,
		}
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return seq
	}
	if durable {
		s.log[session] = append(s.log[session], raw)
	}
	for _, ch := range s.subs[session] {
		select {
		case ch <- raw:
		default:
		}
	}
	return seq
}

func (s *server) eventID() string {
	return fmt.Sprintf("evt_%d", s.evtN.Add(1))
}

// ------------------------------------------------------------------- misc

func models() []any {
	return []any{
		map[string]any{
			"id": "fake-thinker", "providerID": "opencode", "family": "fake",
			"name":         "Fake Thinker",
			"capabilities": map[string]any{"tools": true},
			// F-13: the variants map, not an array.
			"variants": map[string]any{
				"low":    map[string]any{"reasoning": map[string]any{"effort": "low"}},
				"medium": map[string]any{"reasoning": map[string]any{"effort": "medium"}},
				"high":   map[string]any{"reasoning": map[string]any{"effort": "high"}},
			},
		},
		map[string]any{
			"id": "fake-plain", "providerID": "opencode", "family": "fake",
			"name":         "Fake Plain",
			"capabilities": map[string]any{"tools": true},
			"variants":     map[string]any{},
		},
	}
}

func sleep(done <-chan struct{}, ms int) bool {
	if ms <= 0 {
		return true
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-done:
		return false
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func nowMS() int64 { return time.Now().UnixMilli() }

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func value(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return a[len(name)+1:]
		}
	}
	return ""
}
