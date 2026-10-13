package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
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
//	POST   …/file,
//	       …/resume, …/restart,
//	       …/ack-resume, …/read     {"session":<session>}
//	                                409 {"error":"…"} when a restart was asked
//	                                    for on a session that is still running,
//	                                    or a resume on a session that is done
//	GET    …/journal                the raw bytes, as an attachment
//	GET    …/usage                  {"windows":[…],"cost_usd":…}
//	every route                     4xx/5xx {"error":"…"}
//
// <session> is store.Session as it serialises, plus the fields sessionView
// adds: cli_session_state, activity, and after a resume resume_fresh and
// resumed_from.
//
// handleListSessions answers the session list: every session, whatever its
// status, because the list files them itself.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions()
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
	// DeleteAt is when a done session is deleted, so the page can say so
	// without knowing how long done sessions are kept.
	DeleteAt int64 `json:"delete_at,omitempty"`
	// DraftRev and DraftPreview are a prepared session's draft as the list
	// shows it: the revision, so a device whose panel is open notices that
	// another one saved, and the first line, for the sidebar. The text itself
	// is fetched on its own; it can be a hundred kilobytes per session.
	DraftRev     int64  `json:"draft_rev,omitempty"`
	DraftPreview string `json:"draft_preview,omitempty"`
}

func (s *Server) view(row *store.Session) sessionView {
	var metas map[string]store.DraftMeta
	if row.State == store.StatePrestart {
		if meta, ok := s.store.DraftMetaOf(row.ID); ok {
			metas = map[string]store.DraftMeta{row.ID: meta}
		}
	}
	return s.viewWith(row, metas)
}

// viewWith is view with the drafts already read, which is how the list reads
// them: once, for every row.
func (s *Server) viewWith(row *store.Session, drafts map[string]store.DraftMeta) sessionView {
	v := sessionView{Session: row, CLISessionState: row.CLISessionState}
	if meta, ok := drafts[row.ID]; ok && row.State == store.StatePrestart {
		v.DraftRev, v.DraftPreview = meta.Rev, meta.Preview
	}
	v.Activity = s.manager.ActivityOf(row.ID)
	if row.Status == store.StatusDone {
		v.DeleteAt = row.StatusAt + DoneRetention.Milliseconds()
	}
	if row.Resumed {
		if note, ok := s.manager.ResumeNoteOf(row.ID); ok {
			v.ResumeFresh, v.ResumedFrom = note.Fresh, note.From
		}
	}
	return v
}

func (s *Server) views(rows []store.Session) []sessionView {
	drafts, err := s.store.DraftMetas()
	if err != nil {
		// The list without previews is still the list.
		log.Printf("sessions: could not read the drafts: %v", err)
	}
	out := make([]sessionView, 0, len(rows))
	for i := range rows {
		out = append(out, s.viewWith(&rows[i], drafts))
	}
	return out
}

// createSessionBody is what the new-session sheet posts.
type createSessionBody struct {
	// ClientID is the browser's idempotency key. A create retried over a link
	// that dropped finds the session it already made instead of making a
	// second one, which is the whole of "start a session in a tunnel and come
	// back to one session".
	ClientID       string `json:"client_id"`
	Harness        string `json:"harness"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	ShellExecution string `json:"shell_execution"`
	// Profile is the sign-in profile the session runs in; empty is the
	// default one.
	Profile string `json:"profile"`
	// Ultracode starts a Claude Code session with dynamic workflows on every
	// task. Off unless asked for, and kept for the session's whole life.
	Ultracode   bool            `json:"ultracode"`
	WorkdirMode string          `json:"workdir_mode"`
	Workdir     string          `json:"workdir"`
	Title       string          `json:"title"`
	Options     json.RawMessage `json:"options"`
	// Cols and Rows are the size the sheet is about to open the terminal at.
	// Absent, the session is created at the default size and the first attach
	// corrects it a moment later.
	Cols int `json:"cols"`
	Rows int `json:"rows"`

	// Prestart prepares the session instead of starting it: the row is
	// stored with these settings, and nothing else happens until Start. See
	// prestart.go. Prompt is its first draft, ProfileAuto asks for the
	// profile with the most headroom to be chosen when it starts, and StartAt
	// is when it starts by itself.
	Prestart    bool   `json:"prestart"`
	Prompt      string `json:"prompt"`
	ProfileAuto bool   `json:"profile_auto"`
	StartAt     int64  `json:"start_at"`
}

// launchSpec is one session to be started, wherever its settings came from:
// the sheet, for a new session, or the stored row, for a prepared one. It is
// the input of launch, which is the one path from settings to a running
// program - "Start" on a prepared session is the same launch a new session
// gets, and it is the same because it is the same code.
type launchSpec struct {
	ID             string
	ClientID       string
	Harness        string
	Model          string
	Effort         string
	ShellExecution string
	Profile        string
	Ultracode      bool
	WorkdirMode    string
	Workdir        string
	// Title is the name the person gave the session, and "" when they gave
	// none. It is also what Claude Code is told the session is called.
	Title   string
	Options json.RawMessage
	Cols    int
	Rows    int
	// Prompt is sent as the program's first prompt; see harnesses/prompt.go.
	Prompt string
}

// launchError is a refusal of a launch's settings, with the status it is
// answered with. It is a type of its own because a start of a prepared
// session records it on the row rather than only answering it.
type launchError struct {
	status int
	msg    string
}

func (e *launchError) Error() string { return e.msg }

func refuse(status int, format string, args ...any) *launchError {
	return &launchError{status: status, msg: fmt.Sprintf(format, args...)}
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
	if body.Prestart {
		// Preparing a session starts nothing, so it does not need tmux to be
		// there: a draft can be written on a machine whose terminal engine is
		// still being installed.
		s.createPrestart(w, body)
		return
	}
	if err := s.manager.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	id := termux.NewID()
	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	row, err := s.launch(ctx, launchSpec{
		ID:             id,
		ClientID:       strings.TrimSpace(body.ClientID),
		Harness:        body.Harness,
		Model:          body.Model,
		Effort:         body.Effort,
		ShellExecution: body.ShellExecution,
		Profile:        body.Profile,
		Ultracode:      body.Ultracode,
		WorkdirMode:    body.WorkdirMode,
		Workdir:        body.Workdir,
		Title:          body.Title,
		Options:        body.Options,
		Cols:           body.Cols,
		Rows:           body.Rows,
	}, nil)
	var refused *launchError
	if errors.As(err, &refused) {
		writeError(w, refused.status, refused.msg)
		return
	}
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

// checkLaunch is every rule a session's settings have to pass, and it changes
// nothing on the machine: it is also what a prepared session's settings are
// held to each time they are saved, long before anything starts. It fills in
// what the settings leave to defaults - the harness, the shell's machine -
// and folds the choices that have to survive a relaunch into the options.
//
// The rules are enforced here and not in the sheet: hiding a control is
// presentation, and this is the boundary.
func (s *Server) checkLaunch(spec *launchSpec, settings config.Settings) (harnesses.Harness, *launchError) {
	spec.Harness = strings.TrimSpace(spec.Harness)
	if spec.Harness == "" {
		spec.Harness = settings.Workspace.DefaultHarness
	}
	h, ok := harnesses.Get(spec.Harness)
	if !ok {
		return nil, refuse(http.StatusBadRequest, "%q is not one of the harnesses", spec.Harness)
	}
	if spec.Harness == config.HarnessVSCode {
		// A folder has one editor, and it is opened from the folder: see
		// handleFolderIDE.
		return nil, refuse(http.StatusBadRequest, "VS Code is opened from a folder in the sidebar, not as a new session")
	}
	if entry, known := settings.Harnesses.Entry(spec.Harness); known && !entry.Enabled {
		return nil, refuse(http.StatusBadRequest, "%s is switched off in the dashboard", h.Label())
	}
	if spec.ShellExecution != "" && spec.Harness != string(harnesses.KindShell) {
		return nil, refuse(http.StatusBadRequest, "shell_execution is only valid for Shell sessions")
	}
	if spec.Harness == string(harnesses.KindShell) {
		if spec.ShellExecution == "" {
			spec.ShellExecution = harnesses.DefaultShellExecution(settings.Harnesses.Shell.Binary)
		}
		switch spec.ShellExecution {
		case harnesses.ShellExecutionContainer:
		case harnesses.ShellExecutionHost:
			if !harnesses.HostShellAvailable() {
				return nil, refuse(http.StatusBadRequest, "host shell is unavailable on this installation")
			}
		default:
			return nil, refuse(http.StatusBadRequest, "shell_execution must be host or container")
		}
	}
	spec.Profile = strings.TrimSpace(spec.Profile)
	if status, msg := s.checkProfile(spec.Harness, h.Label(), spec.ShellExecution, spec.Profile); status != 0 {
		return nil, refuse(status, "%s", msg)
	}
	if spec.Ultracode && spec.Harness != string(harnesses.KindClaude) {
		return nil, refuse(http.StatusBadRequest, "ultracode is only valid for Claude Code sessions")
	}
	spec.Model, spec.Effort = strings.TrimSpace(spec.Model), strings.TrimSpace(spec.Effort)
	// What the session has to be relaunched with is kept in its options, next
	// to whatever the client put there.
	choices := map[string]any{"shell_execution": spec.ShellExecution, "profile": spec.Profile, "ultracode": spec.Ultracode}
	if spec.ShellExecution != "" || spec.Profile != "" || spec.Ultracode {
		fields := map[string]json.RawMessage{}
		if len(spec.Options) > 0 && string(spec.Options) != "null" {
			if err := json.Unmarshal(spec.Options, &fields); err != nil || fields == nil {
				return nil, refuse(http.StatusBadRequest, "options must be a JSON object")
			}
		}
		for key, value := range choices {
			if value != "" && value != false {
				fields[key], _ = json.Marshal(value)
			}
		}
		spec.Options, _ = json.Marshal(fields)
	}
	if err := checkSize(spec.Cols, spec.Rows); err != nil {
		// Before the row, before the directory: a size that cannot be a
		// terminal is the client's mistake, and it must not cost a failed
		// session and an empty workspace directory to find that out.
		return nil, refuse(http.StatusBadRequest, "%s", err.Error())
	}
	return h, nil
}

// launch starts a session's program: the checks, the working directory, the
// plan and the manager, in that order and in no other place. prepared is nil
// for a new session, which the manager creates; for a prepared one it is the
// row, and the manager starts that row instead of making another. Everything
// between the two is shared, which is the guarantee that a prepared session
// starts exactly as the same settings would have started from the sheet.
//
// A *launchError back is a refusal of the settings and nothing was made. Any
// other error comes with the row when there is one, carrying the reason.
func (s *Server) launch(ctx context.Context, spec launchSpec, prepared *store.Session) (*store.Session, error) {
	settings := s.Settings()
	h, refused := s.checkLaunch(&spec, settings)
	if refused != nil {
		return nil, refused
	}
	workdir, err := harnesses.ResolveWorkdir(settings, spec.WorkdirMode, spec.Workdir, spec.Harness, spec.ID)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "%s", err.Error())
	}
	mode := strings.TrimSpace(spec.WorkdirMode)
	if mode == "" {
		mode = harnesses.WorkdirDynamic
	}

	plan, err := h.Plan(ctx, harnesses.PlanRequest{
		SessionID:      spec.ID,
		Title:          spec.Title,
		Cwd:            workdir,
		Model:          spec.Model,
		Effort:         spec.Effort,
		ShellExecution: spec.ShellExecution,
		Profile:        spec.Profile,
		Ultracode:      spec.Ultracode,
		InitialPrompt:  spec.Prompt,
		Settings:       settings,
		DataDir:        s.dataDir,
		Logf:           log.Printf,
	})
	if err != nil {
		if mode == harnesses.WorkdirDynamic {
			// The directory was made for this launch a moment ago and nothing
			// has run in it; os.Remove only removes it while that is true.
			_ = os.Remove(workdir)
		}
		return nil, refuse(http.StatusBadRequest, "%s", err.Error())
	}

	launch := termux.Spec{
		ID:          spec.ID,
		ClientID:    spec.ClientID,
		Title:       sessionTitle(spec.Title, h.Label(), workdir, mode),
		TitleSource: titleSource(spec.Title),
		Harness:     spec.Harness,
		Model:       spec.Model,
		Effort:      spec.Effort,
		Workdir:     workdir,
		WorkdirMode: mode,
		Options:     spec.Options,
		Cols:        spec.Cols,
		Rows:        spec.Rows,
		Plan:        plan,
	}
	if prepared == nil {
		return s.manager.Create(ctx, launch)
	}
	return s.manager.StartPrestart(ctx, launch)
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

// titleSource records whether the name came from the person creating the
// session. A name they typed is theirs, and the automatic title leaves it
// alone for the life of the session.
func titleSource(title string) string {
	if strings.TrimSpace(title) != "" {
		return store.TitleUser
	}
	return ""
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

// handleSetStatus files a session under a status. Done is the one status
// that does something to the machine: the terminal is closed, so a finished
// session stops costing anything, and the session itself is kept until
// DoneRetention has passed. Any other status is the row and nothing more; a
// session taken out of done is resumed the next time it is opened, like one
// a reboot took away.
func (s *Server) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !store.ValidStatus(body.Status) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a session status", body.Status))
		return
	}
	// The status is written before the terminal is closed, so that a viewer
	// opening the session in between is refused rather than resuming it.
	if err := s.store.SetSessionStatus(row.ID, body.Status); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Status == store.StatusDone {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.park(ctx, row.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
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
	if err := s.deleteSession(ctx, row.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	case errors.Is(err, termux.ErrStillRunning), errors.Is(err, termux.ErrDone), errors.Is(err, termux.ErrNotStarted):
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
