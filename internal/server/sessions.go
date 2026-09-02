package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// createTimeout is how long one create may take. It is generous because it
// covers a tmux server being started, a generated configuration being written
// and a program being exec'd, and none of those is on a network.
const createTimeout = 60 * time.Second

// journalDownloadMax is how much of a session's journal a download carries.
// The sink rotates at sixty-four megabytes and keeps one older file, so the
// whole of it can be a hundred and twenty-eight; what a person opening
// "download scrollback" wants is the recent past, and sixteen megabytes of a
// terminal is more of it than any editor will open.
const journalDownloadMax = 16 << 20

// The shapes every route in this file answers with, in one place because
// D.9 lists the routes and not their bodies:
//
//	GET    /api/sessions            {"sessions":[<session>…], "rev":<int>}
//	POST   /api/sessions            201 {"session":<session>}
//	                                201 {"session":<session>,"error":"…"} when the
//	                                    tmux commands failed: the row exists, it
//	                                    is `failed` and carries tmux's own words
//	                                200 {"session":<session>} for a repeat of a
//	                                    request that already made one
//	GET    /api/sessions/{id}       {"session":<session>}
//	PATCH  /api/sessions/{id}       {"session":<session>}
//	DELETE /api/sessions/{id}       {"ok":true,"workdir":"…","workdir_kept":true}
//	POST   …/archive, …/resume,
//	       …/restart, …/ack-resume,
//	       …/read                   {"session":<session>}
//	                                409 {"error":"…"} when a restart was asked
//	                                    for on a session that is still running
//	GET    …/journal                the raw bytes, as an attachment
//	every route                     4xx/5xx {"error":"…"}
//
// <session> is store.Session as it serialises, plus the fields sessionView
// adds: cli_session_state, activity, and after a resume resume_fresh and
// resumed_from.
//
// handleListSessions answers the session list. scope=all includes the archived
// ones, which is what the list's own switch asks for.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("scope") == "all"
	sessions, err := s.store.ListSessions(includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.views(sessions), "rev": s.store.Rev()})
}

// sessionView is a session as the API hands it over: the stored row, plus
// what the browser needs and the row does not serialise.
//
// cli_session_state is here because "this conversation is gone" is something
// the session list shows in its technical detail, and resume_fresh because a
// resume that had to start a new conversation is otherwise byte for byte a
// resume that continued the old one - and §C.8 requires the banner to tell
// them apart.
type sessionView struct {
	*store.Session
	CLISessionState string `json:"cli_session_state"`
	ResumeFresh     bool   `json:"resume_fresh,omitempty"`
	ResumedFrom     string `json:"resumed_from,omitempty"`
	// Activity is busy/idle/waiting and the unread mark. It is here as well as
	// on the WebSocket because a browser with no socket open - the list on
	// first load, a tab that has been asleep - has to see the sidebar the same
	// way, and the poll is the catch-up path for exactly that.
	Activity termux.Activity `json:"activity"`
}

func (s *Server) view(row *store.Session) sessionView {
	v := sessionView{Session: row, CLISessionState: row.CLISessionState}
	v.Activity = s.manager.ActivityOf(row.ID)
	if row.Resumed {
		if note, ok := s.manager.ResumeNoteOf(row.ID); ok {
			v.ResumeFresh, v.ResumedFrom = note.Fresh, note.From
		}
	}
	return v
}

func (s *Server) views(rows []store.Session) []sessionView {
	out := make([]sessionView, 0, len(rows))
	for i := range rows {
		out = append(out, s.view(&rows[i]))
	}
	return out
}

// createSessionBody is what the new-session sheet posts.
type createSessionBody struct {
	// ClientID is the browser's idempotency key. A create retried over a link
	// that dropped finds the session it already made instead of making a
	// second one, which is the whole of "start a session in a tunnel and come
	// back to one session".
	ClientID    string          `json:"client_id"`
	Harness     string          `json:"harness"`
	Model       string          `json:"model"`
	Effort      string          `json:"effort"`
	WorkdirMode string          `json:"workdir_mode"`
	Workdir     string          `json:"workdir"`
	Title       string          `json:"title"`
	Options     json.RawMessage `json:"options"`
	// Cols and Rows are the size the sheet is about to open the terminal at.
	// Absent, the session is created at the default size and the first attach
	// corrects it a moment later.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionBody
	if !readJSON(w, r, &body) {
		return
	}
	// The idempotency check comes before everything else, including whether
	// sessions can be created at all: a repeat of a request that worked must
	// answer with the session it made, whatever the machine looks like now.
	if clientID := strings.TrimSpace(body.ClientID); clientID != "" {
		if existing, err := s.store.SessionByClientID(clientID); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"session": s.view(existing)})
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.manager.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	settings := s.Settings()
	harnessID := strings.TrimSpace(body.Harness)
	if harnessID == "" {
		harnessID = settings.Workspace.DefaultHarness
	}
	h, ok := harnesses.Get(harnessID)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%q is not one of the harnesses", harnessID))
		return
	}
	if entry, known := settings.Harnesses.Entry(harnessID); known && !entry.Enabled {
		writeError(w, http.StatusBadRequest, h.Label()+" is switched off in the dashboard")
		return
	}

	if err := checkSize(body.Cols, body.Rows); err != nil {
		// Before the row, before the directory: a size that cannot be a
		// terminal is the client's mistake, and it must not cost a failed
		// session and an empty workspace directory to find that out.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id := termux.NewID()
	// The workspace rules are enforced here and not in the sheet: hiding a
	// control is presentation, and this is the boundary.
	workdir, err := harnesses.ResolveWorkdir(settings, body.WorkdirMode, body.Workdir, harnessID, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := strings.TrimSpace(body.WorkdirMode)
	if mode == "" {
		mode = harnesses.WorkdirDynamic
	}

	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	plan, err := h.Plan(ctx, harnesses.PlanRequest{
		SessionID: id,
		Title:     body.Title,
		Cwd:       workdir,
		Model:     strings.TrimSpace(body.Model),
		Effort:    strings.TrimSpace(body.Effort),
		Settings:  settings,
		DataDir:   s.dataDir,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	row, err := s.manager.Create(ctx, termux.Spec{
		ID:          id,
		ClientID:    strings.TrimSpace(body.ClientID),
		Title:       sessionTitle(body.Title, h.Label(), workdir, mode),
		Harness:     harnessID,
		Model:       strings.TrimSpace(body.Model),
		Effort:      strings.TrimSpace(body.Effort),
		Workdir:     workdir,
		WorkdirMode: mode,
		Options:     body.Options,
		Cols:        body.Cols,
		Rows:        body.Rows,
		Plan:        plan,
	})
	if err != nil {
		if row == nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// The row exists and carries tmux's own words in fail_reason. The
		// session belongs in the list with its failure showing and a Try again
		// button, rather than disappearing into a status code.
		writeJSON(w, http.StatusCreated, map[string]any{"session": s.view(row), "error": err.Error()})
		return
	}
	if row.ID != id {
		// Two identical requests raced and this one lost: the store answered
		// with the session the other made, and nothing was created here.
		writeJSON(w, http.StatusOK, map[string]any{"session": s.view(row)})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": s.view(row)})
}

// The bounds a client-supplied terminal size has to be inside. Zero means the
// sheet did not measure one and the stored default applies; anything else has
// to be a size tmux will accept, because a window it refuses is a session that
// fails at creation with the client's own number in it.
const (
	minTerminalSize = 10
	maxTerminalSize = 1000
)

func checkSize(cols, rows int) error {
	for _, v := range [2]int{cols, rows} {
		if v == 0 {
			continue
		}
		if v < minTerminalSize || v > maxTerminalSize {
			return fmt.Errorf("a terminal of %dx%d is not a size; each side has to be between %d and %d",
				cols, rows, minTerminalSize, maxTerminalSize)
		}
	}
	return nil
}

// sessionTitle is the name a session is listed under. The browser sends one;
// this is what a client that did not send one gets, so that no session is ever
// nameless.
func sessionTitle(title, label, workdir, mode string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if mode == harnesses.WorkdirDynamic {
		// The basename of a dynamic directory is a generated name, and nobody
		// reads one. The time it was made is what tells two of them apart.
		return label + " · " + time.Now().Format("2 Jan 15:04")
	}
	return label + " · " + filepath.Base(workdir)
}

// session loads the row a request names, and answers 404 itself when there is
// none. The boolean is whether the caller should carry on.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	row, err := s.store.GetSession(r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "there is no session with that id")
		return nil, false
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return row, true
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s.view(row)})
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "a session needs a name")
		return
	}
	if err := s.store.UpdateSessionTitle(row.ID, title); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.answerWithSession(w, row.ID)
}

func (s *Server) handleArchiveSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Archived bool `json:"archived"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// Archiving does not touch tmux. An archived session keeps running, which
	// is the point of it: it is put away, not stopped.
	if err := s.store.SetSessionArchived(row.ID, body.Archived); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.answerWithSession(w, row.ID)
}

// handleDeleteSession is the only path in Socrates that kills a tmux session.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.manager.Delete(ctx, row.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	harnesses.ForgetOpenCodeAccess(row.ID)
	// The working directory is kept, in every mode. Work the user did is not
	// ours to throw away, and the answer says so because the dialog does.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"workdir":      row.Workdir,
		"workdir_kept": true,
	})
}

// handleResumeSession is what a viewer calls before it opens a terminal: it
// brings a session that a reboot took away back on its own conversation, and
// leaves a running one alone.
func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	s.ensure(w, r, false)
}

// handleRestartSession is the exit overlay's button, which starts the program
// again whatever state it left behind.
func (s *Server) handleRestartSession(w http.ResponseWriter, r *http.Request) {
	s.ensure(w, r, true)
}

func (s *Server) ensure(w http.ResponseWriter, r *http.Request, restart bool) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	if err := s.manager.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	var next *store.Session
	var err error
	if restart {
		next, err = s.manager.Restart(ctx, row.ID)
	} else {
		next, err = s.manager.Ensure(ctx, row.ID)
	}
	switch {
	case errors.Is(err, termux.ErrStillRunning):
		// Not a fault: the terminal the caller asked to replace is working,
		// and its row was left exactly as it was.
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil && next != nil:
		// The relaunch failed and the row says why. It belongs in the list
		// with its overlay, the same as a create that could not start.
		writeJSON(w, http.StatusOK, map[string]any{"session": s.view(next), "error": err.Error()})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s.view(next)})
}

// handleAckResume lowers the "resumed after restart" flag once the banner has
// been seen.
func (s *Server) handleAckResume(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	if err := s.store.ClearResumedFlag(row.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The banner and what it said go together.
	s.manager.ClearResumeNote(row.ID)
	s.answerWithSession(w, row.ID)
}

// handleMarkRead lowers the unread mark of one session. It is the REST half of
// the `read` control frame, for a page with no socket open on that session -
// which, since the mark is cleared for rows the user is *not* looking at, is
// the ordinary case.
func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	s.manager.MarkRead(row.ID)
	s.answerWithSession(w, row.ID)
}

// handleJournal hands over the raw pane stream as a download. It is the
// scrollback, the export and the audit trail, and it is never on the hot path:
// a reconnect repaints from tmux instead.
func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	data, err := termux.TailJournal(s.dataDir, row.ID, journalDownloadMax)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "socrates-"+row.ID+".raw"))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.Printf("session %s: could not send the journal: %v", row.ID, err)
	}
}

// answerWithSession reloads a row after a write, so that the browser is
// answered with what was stored rather than with what it sent.
func (s *Server) answerWithSession(w http.ResponseWriter, id string) {
	row, err := s.store.GetSession(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s.view(row)})
}
