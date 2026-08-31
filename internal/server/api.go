package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	chats, err := s.store.ListChats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string `json:"title"`
		Workspace string `json:"workspace"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	chat := &store.Chat{
		ID:        newChatID(),
		Title:     strings.TrimSpace(body.Title),
		Workspace: strings.TrimSpace(body.Workspace),
	}
	if err := s.store.CreateChat(chat); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": chat})
}

func (s *Server) handleGetChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chat, err := s.store.GetChat(id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	messages, err := s.store.ListMessages(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	steps, err := s.store.ListSteps(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs, err := s.store.ListRuns(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	questions, err := s.store.ListQuestions(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings := s.Settings()
	workspace := chat.Workspace
	if workspace == "" {
		workspace = filepath.Join(settings.Agent.WorkspaceRoot, chat.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chat":                chat,
		"messages":            messages,
		"steps":               steps,
		"runs":                runs,
		"questions":           questions,
		"rev":                 s.store.Rev(),
		"busy":                s.engine.Busy(id),
		"effective_workspace": workspace,
	})
}

func (s *Server) handleUpdateChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chat, err := s.store.GetChat(id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	var body struct {
		Title     *string `json:"title"`
		Workspace *string `json:"workspace"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	title, workspace := chat.Title, chat.Workspace
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
	}
	if body.Workspace != nil {
		workspace = strings.TrimSpace(*body.Workspace)
	}
	if err := s.store.UpdateChat(id, title, workspace); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	chat.Title, chat.Workspace = title, workspace
	s.bus.Publish(id, agent.Event{Type: "chat", Chat: chat})
	writeJSON(w, http.StatusOK, map[string]any{"chat": chat})
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.engine.Stop(id)
	// Terminal sessions outlive a run on purpose, so deleting the chat they
	// belong to is what actually ends them.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.terminals.CloseChat(ctx, id)
	if err := s.store.DeleteChat(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
		Auto bool   `json:"auto"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "the message is empty")
		return
	}
	run, err := s.engine.Start(id, text, body.Auto)
	if err != nil {
		switch {
		case errors.Is(err, agent.ErrBusy):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "chat not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, map[string]any{"stopped": s.engine.Stop(id)})
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Value string `json:"value"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Value) == "" {
		writeError(w, http.StatusBadRequest, "the answer is empty")
		return
	}
	if err := s.engine.Answer(id, strings.TrimSpace(body.Value)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "question not found")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEvents is the SSE stream that drives the live process view.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetChat(id); err != nil {
		s.notFound(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	rev, _ := strconv.ParseInt(r.URL.Query().Get("rev"), 10, 64)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	subID, ch := s.bus.Subscribe(id)
	defer s.bus.Unsubscribe(id, subID)

	send := func(v any) bool {
		payload, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Replay everything that changed while the client was away.
	if steps, err := s.store.StepsSince(id, rev); err == nil {
		for i := range steps {
			if !send(agent.Event{Type: "step", Step: &steps[i]}) {
				return
			}
		}
	}
	if run, err := s.store.ActiveRun(id); err == nil {
		send(agent.Event{Type: "run", Run: run})
	}
	if q, err := s.store.PendingQuestion(id); err == nil {
		send(agent.Event{Type: "question", Question: q})
	}
	recent := []store.Message{}
	if messages, err := s.store.ListMessages(id); err == nil && len(messages) > 0 {
		if len(messages) > 10 {
			messages = messages[len(messages)-10:]
		}
		recent = messages
	}
	if !send(map[string]any{
		"type":     "ready",
		"rev":      s.store.Rev(),
		"busy":     s.engine.Busy(id),
		"messages": recent,
	}) {
		return
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-ch:
			if !open {
				// The client fell behind; ask it to reconnect and catch up.
				send(map[string]any{"type": "resync"})
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) notFound(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func newChatID() string {
	return fmt.Sprintf("chat_%d_%s", time.Now().UnixMilli()%1000000, randomHex(4))
}
