package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// A chat's draft: what its composer holds, saved after every pause in the
// typing so that nothing written is lost - a closed tab, a phone that died,
// another device that opens the chat. For a chat nobody has written to yet
// it is the first message, written in the large field the chat shows then.
//
//	GET  /api/sessions/{id}/draft   {"text","rev","updated_at"}
//	PUT  /api/sessions/{id}/draft   {"rev","updated_at"}
//	                                409 {"conflict":true,"text","rev","updated_at"}
//	                                409 for a session that is not a chat
//	                                413 over maxDraftBytes

// maxDraftBytes is the largest message a draft may hold. A draft is saved on
// every pause in the typing, and a hundred kilobytes is a long letter
// already.
const maxDraftBytes = 100 << 10

// agentHarness is whether a harness is one of the three coding agents, which
// run as chats only (REBUILD.md §1): a session of one is never a terminal.
func agentHarness(id string) bool {
	switch id {
	case string(harnesses.KindClaude), string(harnesses.KindCodex), string(harnesses.KindOpenCode):
		return true
	}
	return false
}

// readUltracode is whether a row's options ask for Ultracode.
func readUltracode(raw json.RawMessage) bool {
	var o struct {
		Ultracode bool `json:"ultracode"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o.Ultracode
}

// withOption sets or removes one key of an options object. A value of nil,
// "" or false removes it, which keeps a row's options to the choices that
// were actually made.
func withOption(raw json.RawMessage, key string, value any) json.RawMessage {
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	switch value {
	case nil, "", false:
		delete(fields, key)
	default:
		fields[key], _ = json.Marshal(value)
	}
	out, _ := json.Marshal(fields)
	return out
}

// handleGetDraft answers a chat's draft, and an empty one for a chat that
// has none.
func (s *Server) handleGetDraft(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	draft, err := s.store.GetDraft(row.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text": draft.Text, "rev": draft.Rev, "updated_at": draft.UpdatedAt,
	})
}

// handlePutDraft saves a chat's draft.
func (s *Server) handlePutDraft(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Text    string `json:"text"`
		BaseRev int64  `json:"base_rev"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Text) > maxDraftBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a message can be at most %d KB", maxDraftBytes>>10))
		return
	}
	draft, err := s.store.SaveDraft(row.ID, body.Text, body.BaseRev)
	// The list carries the draft's revision and its first line, so a save
	// is news for every other page that shows the row.
	s.events.poke()
	switch {
	case errors.Is(err, store.ErrConflict):
		// Another device saved first. Both texts exist now - theirs here,
		// this one in the browser - and only the person can choose.
		writeJSON(w, http.StatusConflict, map[string]any{
			"conflict": true, "text": draft.Text, "rev": draft.Rev, "updated_at": draft.UpdatedAt,
		})
		return
	case errors.Is(err, store.ErrNotChat):
		writeError(w, http.StatusConflict, "only a chat has a draft: a Shell's text is typed into its terminal")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "there is no session with that id")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rev": draft.Rev, "updated_at": draft.UpdatedAt,
	})
}

// terminal refuses the routes that only a terminal has - its socket, its
// scrollback, its relaunch - for a chat, which has no pane.
func (s *Server) terminal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if row, err := s.store.GetSession(r.PathValue("id")); err == nil && row.IsChat() {
			writeError(w, http.StatusConflict, "this session is a chat and has no terminal")
			return
		}
		next(w, r)
	}
}

// readyPreparedChats turns the prepared chats of an older build (PreStart,
// which is gone) into ordinary chats nobody has written to yet, as the
// server starts: their folder resolved the way a new chat's is, their
// schedule dropped, their first prompt kept as the composer's draft. Nothing
// is started; the person sends the draft when they want. A folder that cannot
// be resolved - a preset that is gone - leaves the chat in the folder it
// names, which the page offers to point elsewhere (workdir_missing), rather
// than losing the chat and its prompt.
func (s *Server) readyPreparedChats() {
	rows, err := s.store.PreparedChats()
	if err != nil {
		log.Printf("prepared chats: could not read them: %v", err)
		return
	}
	settings := s.Settings()
	for i := range rows {
		row := &rows[i]
		workdir, err := harnesses.ResolveWorkdir(settings, row.WorkdirMode, row.Workdir, row.Harness, row.ID)
		if err != nil {
			log.Printf("prepared chats: %s: its folder could not be made (%v); it keeps %q", row.ID, err, row.Workdir)
			workdir = row.Workdir
		}
		model, effort := chatModel(settings, row.Harness, row.Model, row.Effort)
		title := sessionTitle("", chatLabel(row.Harness), workdir, row.WorkdirMode)
		err = s.store.ReadyPreparedChat(row.ID, workdir, model, effort, title)
		switch {
		case errors.Is(err, store.ErrNotPrestart), errors.Is(err, store.ErrNotFound):
		case err != nil:
			log.Printf("prepared chats: %s: %v", row.ID, err)
		default:
			log.Printf("prepared chats: %s is a chat now, with its prompt as the draft", row.ID)
		}
	}
}
