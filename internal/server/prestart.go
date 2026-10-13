package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// Prepared sessions ("preStart"): a session whose settings and first prompt
// are written before anything runs, and which starts - by a button, or by
// itself at a time - exactly as a new session with those settings would.
//
//	POST  /api/sessions {"prestart":true, …}   201 {"session":<session>}
//	PATCH /api/sessions/{id}/prestart          {"session":<session>}
//	                                           409 once it has started
//	GET   /api/sessions/{id}/draft             {"text","rev","updated_at","backed_up"}
//	PUT   /api/sessions/{id}/draft             {"rev","updated_at","backed_up"}
//	                                           409 {"conflict":true,"text","rev","updated_at"}
//	                                           409 {"error","started":true}
//	                                           413 over maxDraftBytes
//	POST  /api/sessions/{id}/start             {"session":<session>}
//	                                           4xx/5xx {"session":<session>,"error":"…"}
//	                                           when it could not start; the row
//	                                           is still prepared and says why
//
// Start is idempotent: a session that is no longer prepared is answered with
// its row, whoever started it.

// maxDraftBytes is the largest prompt a draft may hold. It is well under the
// kernel's 128 KB limit on one argument, which is where the prompt ends up.
const maxDraftBytes = 100 << 10

// prestartHarnesses are the harnesses a session can be prepared for: the
// three that take a prompt. A shell has nothing to send one to, and VS Code
// is opened from a folder.
func prestartHarness(id string) bool {
	switch id {
	case string(harnesses.KindClaude), string(harnesses.KindCodex), string(harnesses.KindOpenCode):
		return true
	}
	return false
}

// prestartOptions is what a prepared row keeps in its options beyond what a
// running session keeps: whether the profile is to be chosen at start.
type prestartOptions struct {
	Profile     string `json:"profile"`
	ProfileAuto bool   `json:"profile_auto"`
	Ultracode   bool   `json:"ultracode"`
}

func readPrestartOptions(raw json.RawMessage) prestartOptions {
	var o prestartOptions
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o
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

// checkStartAt holds a schedule to "in the future, or none". A time in the
// past would be a start that happens the moment it is saved, which is what
// the Start button is for; refusing it says that the clock or the field is
// wrong.
func checkStartAt(at int64, now time.Time) *launchError {
	if at < 0 || (at > 0 && at <= now.UnixMilli()) {
		return refuse(http.StatusBadRequest, "the start time has to be in the future")
	}
	return nil
}

// createPrestart stores a prepared session: the checks a launch would make,
// minus everything that touches the machine.
func (s *Server) createPrestart(w http.ResponseWriter, body createSessionBody) {
	settings := s.Settings()
	harnessID := strings.TrimSpace(body.Harness)
	if harnessID == "" {
		harnessID = settings.Workspace.DefaultHarness
	}
	if !prestartHarness(harnessID) {
		writeError(w, http.StatusBadRequest, "only Claude Code, Codex and OpenCode sessions can be prepared")
		return
	}
	if len(body.Prompt) > maxDraftBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a prompt can be at most %d KB", maxDraftBytes>>10))
		return
	}
	if refused := checkStartAt(body.StartAt, time.Now()); refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	spec := launchSpec{
		Harness: harnessID, Model: body.Model, Effort: body.Effort, ShellExecution: body.ShellExecution,
		Profile: body.Profile, Ultracode: body.Ultracode, Options: body.Options, Cols: body.Cols, Rows: body.Rows,
	}
	h, refused := s.checkLaunch(&spec, settings)
	if refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	mode := strings.TrimSpace(body.WorkdirMode)
	if mode == "" {
		mode = harnesses.WorkdirDynamic
	}
	workdir, err := harnesses.CheckWorkdir(settings, mode, body.Workdir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The profile is chosen at start only when nobody chose one now.
	options := withOption(spec.Options, "profile_auto", body.ProfileAuto && spec.Profile == "")

	row := &store.Session{
		ID:          termux.NewID(),
		ClientID:    strings.TrimSpace(body.ClientID),
		Title:       sessionTitle(body.Title, h.Label(), workdir, mode),
		TitleSource: titleSource(body.Title),
		Harness:     spec.Harness,
		Model:       spec.Model,
		Effort:      spec.Effort,
		Workdir:     workdir,
		WorkdirMode: mode,
		Options:     options,
		State:       store.StatePrestart,
		Cols:        spec.Cols,
		Rows:        spec.Rows,
		StartAt:     body.StartAt,
	}
	id := row.ID
	if err := s.store.CreateSession(row); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row.ID != id {
		// A repeat of a create that already worked.
		writeJSON(w, http.StatusOK, map[string]any{"session": s.view(row)})
		return
	}
	if body.Prompt != "" {
		if _, err := s.store.SaveDraft(id, body.Prompt, 0); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.sched.poke()
	s.answerWithSessionCode(w, http.StatusCreated, id)
}

// prestartPatch is a change to a prepared session's settings. Every field is
// optional; what is absent keeps its stored value.
type prestartPatch struct {
	Harness     *string `json:"harness"`
	Model       *string `json:"model"`
	Effort      *string `json:"effort"`
	Profile     *string `json:"profile"`
	ProfileAuto *bool   `json:"profile_auto"`
	Ultracode   *bool   `json:"ultracode"`
	WorkdirMode *string `json:"workdir_mode"`
	Workdir     *string `json:"workdir"`
	StartAt     *int64  `json:"start_at"`
}

func (s *Server) handlePatchPrestart(w http.ResponseWriter, r *http.Request) {
	var body prestartPatch
	if !readJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	// One change at a time per session, and never in the middle of its start.
	unlock := s.starts.lock(id)
	defer unlock()
	row, err := s.store.GetSession(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "there is no session with that id")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row.State != store.StatePrestart {
		writeError(w, http.StatusConflict, "this session has already been started; its settings are the ones it runs with")
		return
	}

	options := readPrestartOptions(row.Options)
	harnessID, model, effort := row.Harness, row.Model, row.Effort
	mode, path := row.WorkdirMode, row.Workdir
	profile, auto, ultracode, startAt := options.Profile, options.ProfileAuto, options.Ultracode, row.StartAt
	if body.Harness != nil {
		harnessID = strings.TrimSpace(*body.Harness)
		if harnessID != row.Harness && body.Ultracode == nil && harnessID != string(harnesses.KindClaude) {
			// Ultracode is Claude Code's; switching away from it switches it
			// off rather than refusing the switch.
			ultracode = false
		}
	}
	if body.Model != nil {
		model = *body.Model
	}
	if body.Effort != nil {
		effort = *body.Effort
	}
	if body.Profile != nil {
		profile = strings.TrimSpace(*body.Profile)
		if profile != "" && body.ProfileAuto == nil {
			auto = false
		}
	}
	if body.ProfileAuto != nil {
		auto = *body.ProfileAuto
	}
	if auto {
		profile = ""
	}
	if body.Ultracode != nil {
		ultracode = *body.Ultracode
	}
	if body.WorkdirMode != nil {
		mode = strings.TrimSpace(*body.WorkdirMode)
		if mode == "" {
			mode = harnesses.WorkdirDynamic
		}
	}
	if body.Workdir != nil {
		path = *body.Workdir
	}
	if body.StartAt != nil {
		if refused := checkStartAt(*body.StartAt, time.Now()); refused != nil {
			writeError(w, refused.status, refused.msg)
			return
		}
		startAt = *body.StartAt
	}

	if !prestartHarness(harnessID) {
		writeError(w, http.StatusBadRequest, "only Claude Code, Codex and OpenCode sessions can be prepared")
		return
	}
	settings := s.Settings()
	spec := launchSpec{
		Harness: harnessID, Model: model, Effort: effort, Profile: profile, Ultracode: ultracode,
		Options: stripChoices(row.Options),
	}
	h, refused := s.checkLaunch(&spec, settings)
	if refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	workdir, err := harnesses.CheckWorkdir(settings, mode, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A row nobody named is listed under what it will run, the way a
	// session made from the sheet is: a change of harness or folder renames
	// the placeholder too. (The store keeps a name somebody gave, even one
	// given a moment ago from another device.)
	var title string
	if row.TitleSource == "" && (spec.Harness != row.Harness || mode != row.WorkdirMode || workdir != row.Workdir) {
		title = sessionTitle("", h.Label(), workdir, mode)
	}
	err = s.store.UpdatePrestart(id, store.PrestartSettings{
		Title:   title,
		Harness: spec.Harness, Model: spec.Model, Effort: spec.Effort,
		WorkdirMode: mode, Workdir: workdir,
		Options: withOption(spec.Options, "profile_auto", auto),
		StartAt: startAt,
	})
	switch {
	case errors.Is(err, store.ErrNotPrestart):
		writeError(w, http.StatusConflict, "this session has already been started; its settings are the ones it runs with")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.sched.poke()
	s.answerWithSession(w, id)
}

// stripChoices is a prepared row's options without the choices that
// checkLaunch writes back from the settings, so that a choice taken away is
// not kept by the old options.
func stripChoices(raw json.RawMessage) json.RawMessage {
	for _, key := range []string{"profile", "profile_auto", "ultracode", "shell_execution"} {
		raw = withOption(raw, key, nil)
	}
	if string(raw) == "{}" {
		return nil
	}
	return raw
}

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
		"backed_up": s.store.IsBackedUp(store.DraftObject(row.ID)),
	})
}

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
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a prompt can be at most %d KB", maxDraftBytes>>10))
		return
	}
	// The start lock, around the save only (a slow request body must not
	// hold a start up). A start reads the draft long before it moves the
	// row out of prepared - the directory, the plan and tmux come in
	// between - and a save landing in that gap would be answered "saved"
	// for a text the program never got. Under the lock a save made during
	// a start waits for it: a start that worked turns it into the 409
	// "started" below, one that was put back lets it through.
	unlock := s.starts.lock(row.ID)
	draft, err := s.store.SaveDraft(row.ID, body.Text, body.BaseRev)
	unlock()
	switch {
	case errors.Is(err, store.ErrConflict):
		// Another device saved first. Both texts exist now - theirs here,
		// this one in the browser - and only the person can choose.
		writeJSON(w, http.StatusConflict, map[string]any{
			"conflict": true, "text": draft.Text, "rev": draft.Rev, "updated_at": draft.UpdatedAt,
		})
		return
	case errors.Is(err, store.ErrNotPrestart):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this session has already been started with its prompt", "started": true,
		})
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
		"backed_up": s.store.IsBackedUp(store.DraftObject(row.ID)),
	})
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
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
	if err := checkSize(body.Cols, body.Rows); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	next, status, err := s.startPrepared(ctx, row.ID, body.Cols, body.Rows, false)
	if err != nil {
		if next == nil {
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, status, map[string]any{"session": s.view(next), "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s.view(next)})
}

// startPrepared starts a prepared session, from its row and its draft, through
// launch - the same path a new session takes. cols and rows are the size the
// page measured; zero keeps the row's own. scheduled is the scheduler's call,
// which claims the schedule under the same lock before it starts anything.
//
// It answers the row as it is afterwards, an HTTP status, and the error. A
// session that is not prepared any more is not an error: somebody started it,
// and the answer is the session they started.
func (s *Server) startPrepared(ctx context.Context, id string, cols, rows int, scheduled bool) (*store.Session, int, error) {
	unlock := s.starts.lock(id)
	defer unlock()
	row, err := s.store.GetSession(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, http.StatusNotFound, errors.New("there is no session with that id")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if row.State != store.StatePrestart {
		if scheduled {
			// Somebody else started it - the Start button, between the
			// scheduler's read of the list and this lock. The scheduler
			// owns nothing here, and must not announce a start it did not
			// make.
			return row, http.StatusConflict, errNotDue
		}
		return row, http.StatusOK, nil
	}
	if scheduled {
		// Cleared before anything is tried, in the same step that decides
		// this caller owns the start: a start that fails is not tried again
		// by itself, and a schedule somebody moved in the meantime is not
		// this one.
		claimed, err := s.store.ClaimScheduledStart(id, row.StartAt)
		if err != nil {
			return row, http.StatusInternalServerError, err
		}
		if !claimed || row.StartAt <= 0 {
			return row, http.StatusConflict, errNotDue
		}
	}

	failed := func(status int, err error) (*store.Session, int, error) {
		if recordErr := s.store.SetPrestartFailure(id, err.Error()); recordErr != nil &&
			!errors.Is(recordErr, store.ErrNotPrestart) {
			log.Printf("session %s: could not record why it did not start: %v", id, recordErr)
		}
		current, getErr := s.store.GetSession(id)
		if getErr != nil {
			return nil, status, err
		}
		return current, status, err
	}
	if err := s.manager.Available(); err != nil {
		return failed(http.StatusServiceUnavailable, err)
	}
	spec, err := s.prestartSpec(ctx, row, cols, rows)
	if err != nil {
		return failed(http.StatusInternalServerError, err)
	}
	next, err := s.launch(ctx, spec, row)
	var refused *launchError
	switch {
	case errors.As(err, &refused):
		return failed(refused.status, refused)
	case errors.Is(err, store.ErrNotPrestart):
		// Started by somebody else between the read above and the manager's
		// own check - which the lock makes impossible from this server, and
		// which a second process on one database is not.
		if scheduled {
			// That start was not this one: no "Started" for it.
			return next, http.StatusConflict, errNotDue
		}
		return next, http.StatusOK, nil
	case err != nil && next != nil:
		if next.State == store.StatePrestart {
			// Nothing ran, and the manager has already put the reason on
			// the row.
			return next, http.StatusInternalServerError, err
		}
		// The program was started and did not get far: a failed session
		// like any other, with its restart button.
		return next, http.StatusOK, err
	case err != nil:
		return failed(http.StatusInternalServerError, err)
	}
	return next, http.StatusOK, nil
}

// errNotDue is the scheduler being told it lost its claim.
var errNotDue = errors.New("the schedule was changed before it was due")

// prestartSpec is the launch a prepared row stands for: its settings, its
// draft as the first prompt, and the profile with the most headroom when it
// asked for that.
func (s *Server) prestartSpec(ctx context.Context, row *store.Session, cols, rows int) (launchSpec, error) {
	options := readPrestartOptions(row.Options)
	spec := launchSpec{
		ID:          row.ID,
		Harness:     row.Harness,
		Model:       row.Model,
		Effort:      row.Effort,
		Profile:     options.Profile,
		Ultracode:   options.Ultracode,
		WorkdirMode: row.WorkdirMode,
		Workdir:     row.Workdir,
		Options:     stripChoices(row.Options),
		Cols:        row.Cols,
		Rows:        row.Rows,
	}
	// The name is passed on only when somebody gave one, which is what a
	// session created from the sheet does: the placeholder a prepared row
	// was listed under is not a name.
	if row.TitleSource == store.TitleUser {
		spec.Title = row.Title
	}
	if cols > 0 && rows > 0 {
		spec.Cols, spec.Rows = cols, rows
	}
	if options.ProfileAuto {
		spec.Profile = s.autoProfile(ctx, row.Harness, options.Profile)
	}
	draft, err := s.store.GetDraft(row.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return spec, err
	}
	spec.Prompt = draft.Text
	return spec, nil
}

// autoProfile is the profile a session asked to be started "wherever there is
// most left" runs in: the recommendation the sheet shows, made here at the
// moment of the start, because a scheduled start has no sheet. A harness
// with nothing to choose by - no readings yet, or no limits at all, like
// OpenCode - keeps fallback.
func (s *Server) autoProfile(ctx context.Context, harnessID, fallback string) string {
	views, err := s.profileViews()
	if err != nil {
		log.Printf("could not read the profiles to choose one: %v", err)
		return fallback
	}
	s.fillLimits(ctx, views)
	if id, ok := recommendProfiles(views, time.Now())[harnessID]; ok {
		return id
	}
	return fallback
}

// answerWithSessionCode is answerWithSession with a status of its own.
func (s *Server) answerWithSessionCode(w http.ResponseWriter, code int, id string) {
	row, err := s.store.GetSession(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, code, map[string]any{"session": s.view(row)})
}

// started refuses a request that needs a terminal when the session has only
// been prepared: attaching, resuming, restarting, the journal and uploads.
// It is one wrapper at the routes rather than a check in each handler, so a
// route added later cannot forget it by being written elsewhere.
func (s *Server) started(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if row, err := s.store.GetSession(r.PathValue("id")); err == nil && row.State == store.StatePrestart {
			writeError(w, http.StatusConflict, "this session has not been started yet")
			return
		}
		next(w, r)
	}
}

// keyedLocks is one mutex per session id, made when it is first wanted and
// dropped when nobody holds or waits for it.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	sync.Mutex
	refs int
}

func (k *keyedLocks) lock(id string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l := k.locks[id]
	if l == nil {
		l = &keyedLock{}
		k.locks[id] = l
	}
	l.refs++
	k.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, id)
		}
		k.mu.Unlock()
	}
}
