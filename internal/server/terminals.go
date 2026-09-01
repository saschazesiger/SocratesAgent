package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// The terminal endpoints let the browser watch a session live and, when the
// user wants to, type into it. Socrates and the person share one keyboard:
// whatever either of them sends goes to the same program.

func (s *Server) handleListTerminals(w http.ResponseWriter, r *http.Request) {
	// The list is what the dock reads on load, and it can hold a dozen
	// screens. The colours come with the live stream a moment later, so they
	// are left out of the list rather than sent a dozen times over.
	states := s.terminals.States(r.PathValue("id"))
	for i, state := range states {
		states[i] = state.Plain()
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminals": states})
}

// terminal resolves the session in the URL.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) (*term.Handle, bool) {
	handle, ok := s.terminals.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "there is no terminal session with that id")
		return nil, false
	}
	return handle, true
}

func (s *Server) handleGetTerminal(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	state, err := handle.Refresh(r.Context())
	if err != nil {
		state = handle.State()
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminal": state})
}

// handleTerminalInput is the user taking over the keyboard.
func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	var body struct {
		Text string   `json:"text"`
		Keys []string `json:"keys"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Text != "" {
		if err := handle.Type(r.Context(), body.Text); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	if len(body.Keys) > 0 {
		if err := handle.SendKeys(r.Context(), body.Keys); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTerminalResize(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	var body struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := handle.Resize(r.Context(), body.Cols, body.Rows); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTerminalClose ends a session. A program that is given its grace period
// to save its work can take several seconds to go, and on a phone that is a
// tap that appears to have done nothing, so the answer comes back the moment
// the session is out of the list and the waiting happens behind it. The
// session's event stream is what tells the browser the program is really over.
func (s *Server) handleTerminalClose(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	if err := s.terminals.CloseAsync(handle.ID(), 8*time.Second); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleOpenTerminal is the user opening a terminal for themselves, without
// asking Socrates for one: a plain login shell in the chat's working
// directory. From here on it is an ordinary session - it is listed, streamed,
// typed into and closed exactly like one the agent started.
func (s *Server) handleOpenTerminal(w http.ResponseWriter, r *http.Request) {
	chat, err := s.store.GetChat(r.PathValue("id"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	// One terminal per chat, so an existing one is not an error to puzzle over:
	// the refusal says plainly what happened and the browser fetches the list,
	// which is where the session it already has is described in full.
	for _, h := range s.terminals.List(chat.ID) {
		if h.Alive() {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "this chat already has a terminal session running",
			})
			return
		}
	}

	workdir := agent.Workspace(chat, s.Settings())
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// An empty command is the user's login shell.
	handle, err := s.terminals.Open(r.Context(), chat.ID, "terminal", term.Spec{Dir: workdir})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	state := handle.State()
	state.ID = handle.ID()
	state.Name = handle.Name()
	state.ChatID = handle.ChatID()
	writeJSON(w, http.StatusOK, map[string]any{"terminal": state.Plain()})
}

// terminalCoalesce is the shortest gap between two screens on a session
// stream. A screen changes far more often than anyone can read it, and a
// coloured screen is several times the size a plain one was; on a phone on a
// weak connection that difference is the whole experience. So the screens are
// coalesced - and the last one is never among the ones dropped, because a dock
// left showing an old screen is the one fault it cannot afford.
const terminalCoalesce = 150 * time.Millisecond

// handleTerminalEvents streams the screen of one session. The chat stream
// carries the same screens, but only once a second; this one is quick enough
// to type into.
//
// Like the chat stream it heartbeats with a real event, so a browser that lost
// the network learns within seconds that the screen in front of it has stopped
// being live, and it says goodbye explicitly when the session ends so the
// client stops reconnecting to something that is over.
func (s *Server) handleTerminalEvents(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	updates := handle.Watch()
	defer handle.Unwatch(updates)

	send := func(payload map[string]any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	sendState := func(state term.State) bool {
		return send(map[string]any{"terminal": state})
	}
	if !sendState(handle.State()) {
		return
	}
	last := time.Now()

	ping := time.NewTicker(heartbeatInterval)
	defer ping.Stop()
	// The timer only runs while a screen is being held back.
	flush := time.NewTimer(time.Hour)
	if !flush.Stop() {
		<-flush.C
	}
	defer flush.Stop()
	var pending term.State
	held, armed := false, false
	for {
		select {
		case <-r.Context().Done():
			return
		case state, open := <-updates:
			if !open {
				send(map[string]any{"type": "closed"})
				return
			}
			if !state.Running {
				// The program is gone. Its last screen goes out whatever the
				// interval says, and then the goodbye, which ends the stream on
				// purpose - something quite different from losing it.
				if !sendState(state) {
					return
				}
				send(map[string]any{"type": "closed"})
				return
			}
			if wait := terminalCoalesce - time.Since(last); wait > 0 {
				pending, held = state, true
				if !armed {
					flush.Reset(wait)
					armed = true
				}
				continue
			}
			last = time.Now()
			held = false
			if !sendState(state) {
				return
			}
		case <-flush.C:
			armed = false
			if !held {
				continue
			}
			last = time.Now()
			held = false
			if !sendState(pending) {
				return
			}
		case <-ping.C:
			if !send(map[string]any{"type": "ping"}) {
				return
			}
		}
	}
}
