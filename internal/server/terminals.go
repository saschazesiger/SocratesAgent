package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// The terminal endpoints let the browser watch a session live and, when the
// user wants to, type into it. Socrates and the person share one keyboard:
// whatever either of them sends goes to the same program.

func (s *Server) handleListTerminals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"terminals": s.terminals.States(r.PathValue("id")),
	})
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
	writeJSON(w, http.StatusOK, map[string]any{"terminal": state, "running": handle.Alive()})
}

// handleTerminalInput is the user taking over the keyboard.
func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	var body struct {
		Text   string   `json:"text"`
		Keys   []string `json:"keys"`
		Submit bool     `json:"submit"`
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
	if body.Submit {
		body.Keys = append(body.Keys, "enter")
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

func (s *Server) handleTerminalClose(w http.ResponseWriter, r *http.Request) {
	handle, ok := s.terminal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.terminals.Close(ctx, handle.ID(), 8*time.Second); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTerminalEvents streams the screen of one session. The chat stream
// carries the same screens, but only once a second; this one is quick enough
// to type into.
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
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	updates := handle.Watch()
	defer handle.Unwatch(updates)

	send := func(state term.State) bool {
		payload, err := json.Marshal(map[string]any{"terminal": state})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send(handle.State()) {
		return
	}

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case state, open := <-updates:
			if !open {
				return
			}
			if !send(state) {
				return
			}
			if !state.Running {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
