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
	// The sidebar shows the active chats and can be switched to showing every
	// one of them. Anything but "all" means the active ones, so a client that
	// asks for nothing in particular is never handed the archive.
	includeArchived := r.URL.Query().Get("scope") == "all"
	chats, err := s.store.ListChats(includeArchived)
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
		ClientID  string `json:"client_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// A browser on a flaky connection cannot tell a lost reply from a lost
	// request, so it repeats the call. The key it sends makes that safe: the
	// second attempt gets back the chat the first one already created.
	clientID := strings.TrimSpace(body.ClientID)
	if existing, err := s.store.ChatByClientID(clientID); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"chat": existing})
		return
	}
	chat := &store.Chat{
		ID:        newChatID(),
		Title:     strings.TrimSpace(body.Title),
		Workspace: strings.TrimSpace(body.Workspace),
		ClientID:  clientID,
	}
	if err := s.store.CreateChat(chat); err != nil {
		// Two attempts that raced each other: the unique key kept the second
		// one out, so hand back the one that won.
		if existing, lookupErr := s.store.ChatByClientID(clientID); lookupErr == nil {
			writeJSON(w, http.StatusOK, map[string]any{"chat": existing})
			return
		}
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
	settings := s.Settings()
	// The chat's own folder under the workspace root is where it works unless
	// it was pointed somewhere else. Both are sent: the effective directory is
	// what the page shows, and the default is what it falls back to when the
	// directory is later cleared, without another round trip.
	defaultWorkspace := filepath.Join(settings.Agent.WorkspaceRoot, chat.ID)
	workspace := chat.Workspace
	if workspace == "" {
		workspace = defaultWorkspace
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chat":                chat,
		"messages":            messages,
		"steps":               steps,
		"runs":                runs,
		"rev":                 s.store.Rev(),
		"busy":                s.engine.Busy(id),
		"effective_workspace": workspace,
		"default_workspace":   defaultWorkspace,
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

// handleArchiveChat and handleUnarchiveChat put a chat away and bring it back.
// Archiving is the gentler half of deleting: the transcript stays, but nothing
// of the chat is left running.
func (s *Server) handleArchiveChat(w http.ResponseWriter, r *http.Request) {
	s.setChatArchived(w, r, true)
}

func (s *Server) handleUnarchiveChat(w http.ResponseWriter, r *http.Request) {
	s.setChatArchived(w, r, false)
}

func (s *Server) setChatArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	id := r.PathValue("id")
	if _, err := s.store.GetChat(id); err != nil {
		s.notFound(w, err)
		return
	}
	if archived {
		// An archived chat owns nothing that is still running: the turn in
		// flight is cancelled and the terminal sessions - which outlive a run
		// on purpose - are ended here, exactly as a deletion would end them.
		s.engine.Stop(id)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.terminals.CloseChat(ctx, id)
	}
	if err := s.store.SetChatArchived(id, archived); err != nil {
		s.notFound(w, err)
		return
	}
	chat, err := s.store.GetChat(id)
	if err != nil {
		s.notFound(w, err)
		return
	}
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
		Text     string `json:"text"`
		Auto     bool   `json:"auto"`
		ClientID string `json:"client_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "the message is empty")
		return
	}
	run, err := s.engine.Start(agent.Turn{
		ChatID:   id,
		Text:     text,
		Auto:     body.Auto,
		ClientID: strings.TrimSpace(body.ClientID),
	})
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

// handleEvents is the SSE stream that drives the live process view.
//
// It is written for a browser on a bad connection: every reconnect names the
// revision it last saw, and the stream answers with everything that changed
// since - steps, messages, the run - plus the ids of the steps that still
// exist, so rows deleted during the outage disappear instead
// of lingering as stale truth. A heartbeat goes out as a real event rather
// than an SSE comment, because a comment is invisible to EventSource and the
// client needs something it can actually time out on.
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
	// Last-Event-ID is what the browser resends on its own automatic retry, so
	// it is honoured as an equal alternative to the explicit query parameter.
	if resume, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Last-Event-ID")), 10, 64); err == nil && resume > rev {
		rev = resume
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Subscribing before reading the database is what closes the gap: anything
	// written from here on is buffered for this client, so the replay below can
	// only ever repeat an event, never lose one.
	subID, ch := s.bus.Subscribe(id)
	defer s.bus.Unsubscribe(id, subID)
	snapshotRev := s.store.Rev()

	// A client that reconnects a second after dropping should not wait for the
	// browser's own default backoff.
	if _, err := fmt.Fprint(w, "retry: 1000\n\n"); err != nil {
		return
	}
	flusher.Flush()

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

	// A reconnect gets exactly the messages it missed, however long it was
	// gone. A first connection gets the tail of the transcript instead: the
	// page has usually loaded it already and duplicates are ignored, but a
	// chat that was created moments ago may have published its first message
	// before this subscription existed, and that message must not be lost.
	missed := []store.Message{}
	if rev > 0 {
		if messages, err := s.store.MessagesSince(id, rev); err == nil {
			missed = messages
		}
	} else if messages, err := s.store.ListMessages(id); err == nil && len(messages) > 0 {
		if len(messages) > 10 {
			messages = messages[len(messages)-10:]
		}
		missed = messages
	}
	stepIDs, err := s.store.StepIDs(id)
	if err != nil {
		stepIDs = nil
	}
	ready := map[string]any{
		"type":     "ready",
		"rev":      snapshotRev,
		"busy":     s.engine.Busy(id),
		"messages": missed,
		"now":      time.Now().UnixMilli(),
	}
	// Only a reconnect needs the reconciliation set; a fresh page already has
	// the authoritative list and sending it again would be noise.
	if rev > 0 && stepIDs != nil {
		ready["step_ids"] = stepIDs
	}
	if !send(ready) {
		return
	}

	ticker := time.NewTicker(heartbeatInterval)
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
			if !send(map[string]any{"type": "ping", "now": time.Now().UnixMilli()}) {
				return
			}
		}
	}
}

// heartbeatInterval is how often an idle stream proves it is still alive. It
// has to be well under the idle timeout of anything in between - a mobile
// network, a Cloudflare tunnel - and short enough that the browser notices a
// dead connection in seconds rather than minutes.
const heartbeatInterval = 10 * time.Second

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
