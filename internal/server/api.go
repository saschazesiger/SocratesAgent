package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/engine"
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
		Agent     string `json:"agent"`
		Model     string `json:"model"`
		Effort    string `json:"effort"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// A browser on a flaky connection cannot tell a lost reply from a lost
	// request, so it repeats the call. The key it sends makes that safe: the
	// second attempt gets back the chat the first one already created, and it
	// does so before any validation runs - a chat that exists is an answer,
	// whatever the settings have since become.
	clientID := strings.TrimSpace(body.ClientID)
	if existing, err := s.store.ChatByClientID(clientID); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"chat": existing})
		return
	}
	agent, model, effort, err := s.resolveBinding(r.Context(),
		strings.TrimSpace(body.Agent), strings.TrimSpace(body.Model), strings.TrimSpace(body.Effort))
	if err != nil {
		// Never 409: the browser retries that one until it succeeds, and a
		// permanent refusal retried forever is a message the person loses.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	chat := &store.Chat{
		ID:        newChatID(),
		Title:     strings.TrimSpace(body.Title),
		Workspace: strings.TrimSpace(body.Workspace),
		ClientID:  clientID,
		Agent:     agent,
		Model:     model,
		Effort:    effort,
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
	settings := s.Settings()
	// The chat's own folder under the workspace root is where it works unless
	// it was pointed somewhere else. Both are sent: the effective directory is
	// what the page shows, and the default is what it falls back to when the
	// directory is later cleared, without another round trip.
	defaultWorkspace := engine.Workspace(settings, chat)
	workspace := chat.Workspace
	if workspace == "" {
		workspace = defaultWorkspace
	}
	agentLabel, modelLabel, agentOK := s.describeBinding(chat)
	writeJSON(w, http.StatusOK, map[string]any{
		"chat":                chat,
		"messages":            messages,
		"steps":               steps,
		"rev":                 s.store.Rev(),
		"busy":                s.engine.Busy(id),
		"effective_workspace": workspace,
		"default_workspace":   defaultWorkspace,
		"agent_label":         agentLabel,
		"model_label":         modelLabel,
		"agent_ok":            agentOK,
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
		Model     *string `json:"model"`
		Effort    *string `json:"effort"`
		Agent     *string `json:"agent"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Agent != nil {
		// A different agent is a different conversation.
		writeError(w, http.StatusBadRequest, "the agent of a chat cannot be changed - start a new chat instead")
		return
	}
	title, workspace := chat.Title, chat.Workspace
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
	}
	if body.Workspace != nil {
		workspace = strings.TrimSpace(*body.Workspace)
	}
	if body.Model != nil || body.Effort != nil {
		if !s.changeModel(w, r, chat, body.Model, body.Effort) {
			return
		}
	}
	if err := s.store.UpdateChat(id, title, workspace); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	chat.Title, chat.Workspace = title, workspace
	s.bus.Publish(id, engine.Event{Type: "chat", Chat: chat})
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
		// flight is cancelled and the agent session - which outlives a run on
		// purpose - is ended here, exactly as a deletion would end it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.engine.CloseChat(ctx, id)
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
	s.bus.Publish(id, engine.Event{Type: "chat", Chat: chat})
	writeJSON(w, http.StatusOK, map[string]any{"chat": chat})
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// An agent session outlives a run on purpose, so deleting the chat it
	// belongs to is what actually ends it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.engine.CloseChat(ctx, id)
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
	// The idempotency key is looked at first, as it is in the engine: a send
	// that already landed is answered with the run it produced, even if the
	// agent has been switched off since. Refusing a retry of a message that is
	// already in the transcript would be a permanent error the person did
	// nothing to earn.
	clientID := strings.TrimSpace(body.ClientID)
	if _, err := s.store.MessageByClientID(id, clientID); err != nil {
		if reason, ok := s.agentUnavailable(id); !ok {
			writeError(w, http.StatusUnprocessableEntity, reason)
			return
		}
	}
	run, err := s.engine.Start(engine.Turn{
		ChatID:   id,
		Text:     text,
		Auto:     body.Auto,
		ClientID: clientID,
	})
	// The status code matters more here than anywhere else in the API: the
	// browser turns a 409 on this endpoint into a retry with backoff that runs
	// until it succeeds. That is right for ErrBusy - the message waits behind
	// the running turn and client_id delivers it exactly once - and
	// catastrophic for a permanent refusal, which would retry forever behind
	// the false message that Socrates is still finishing the previous one. So
	// 409 is reserved for refusals that pass on their own; every permanent one
	// uses 422, which the Outbox marks failed and offers a retry for.
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrBusy):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, engine.ErrShuttingDown):
			writeError(w, http.StatusServiceUnavailable,
				"Socrates is restarting - your message will be sent in a moment")
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
			if !send(engine.Event{Type: "step", Step: &steps[i]}) {
				return
			}
		}
	}
	if run, err := s.store.ActiveRun(id); err == nil {
		send(engine.Event{Type: "run", Run: run})
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
			if !send(map[string]any{"type": "ping"}) {
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
