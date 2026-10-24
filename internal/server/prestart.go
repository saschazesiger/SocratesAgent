package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// Prepared sessions ("PreStart"): a chat whose settings and first message
// are written before anything runs, and which starts - by a button, or by
// itself at a time - exactly as a new chat with those settings would, its
// draft the first message (REBUILD.md §7). A Shell has nothing to send a
// first message to, and VS Code is opened from a folder.
//
//	POST  /api/sessions {"prestart":true, …}   201 {"session":<session>}
//	PATCH /api/sessions/{id}/prestart          {"session":<session>}
//	                                           409 once it has started
//	GET   /api/sessions/{id}/draft             {"text","rev","updated_at"}
//	PUT   /api/sessions/{id}/draft             {"rev","updated_at"}
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

// maxDraftBytes is the largest first message a draft may hold. A draft is
// saved on every pause in the typing, and a hundred kilobytes is a long
// letter already.
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

// createPrestart stores a prepared chat: the checks a start would make,
// minus everything that touches the machine.
func (s *Server) createPrestart(w http.ResponseWriter, body createSessionBody) {
	settings := s.Settings()
	harnessID := strings.TrimSpace(body.Harness)
	if harnessID == "" {
		harnessID = settings.Workspace.DefaultHarness
	}
	if !chatAgent(harnessID) {
		writeError(w, http.StatusBadRequest, "only an agent's chat can be prepared: a Shell has nothing to send a first message to")
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
	// A chat is bound by its agent, folder, model and effort, its account,
	// and a Claude Code chat by Ultracode.
	if refused := checkChatHarness(harnessID, settings); refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	if refused := s.checkChatAccount(harnessID, body.Account); refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	options := withOption(body.Options, "ultracode", chatUltracode(harnessID, body.Ultracode))
	options = withChatAccount(options, body.Account, body.AutoSwitch)
	mode := strings.TrimSpace(body.WorkdirMode)
	if mode == "" {
		mode = harnesses.WorkdirDynamic
	}
	workdir, err := harnesses.CheckWorkdir(settings, mode, body.Workdir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	row := &store.Session{
		ID:          termux.NewID(),
		ClientID:    strings.TrimSpace(body.ClientID),
		Title:       sessionTitle(body.Title, chatLabel(harnessID), workdir, mode),
		TitleSource: titleSource(body.Title),
		Harness:     harnessID,
		Model:       strings.TrimSpace(body.Model),
		Effort:      strings.TrimSpace(body.Effort),
		Workdir:     workdir,
		WorkdirMode: mode,
		Options:     options,
		State:       store.StatePrestart,
		StartAt:     body.StartAt,
		Kind:        store.KindChat,
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
	Ultracode   *bool   `json:"ultracode"`
	WorkdirMode *string `json:"workdir_mode"`
	Workdir     *string `json:"workdir"`
	StartAt     *int64  `json:"start_at"`
	Account     *string `json:"account"`
	AutoSwitch  *bool   `json:"auto_switch"`
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

	if !row.IsChat() {
		// A prepared terminal of an agent is from before the agents were
		// chats; the migration made it a chat or took it away (§8.3).
		writeError(w, http.StatusConflict, "this prepared session is not a chat, and only a chat can be prepared now")
		return
	}

	harnessID, model, effort := row.Harness, row.Model, row.Effort
	mode, path := row.WorkdirMode, row.Workdir
	ultracode, startAt := readUltracode(row.Options), row.StartAt
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

	settings := s.Settings()
	// A prepared chat stays a chat: it can change to another agent that has
	// one, and to nothing else.
	if refused := checkChatHarness(harnessID, settings); refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	opts := readChatOptions(row.Options)
	if body.Account != nil {
		opts.Account = strings.TrimSpace(*body.Account)
	}
	if body.AutoSwitch != nil {
		opts.AutoSwitch = *body.AutoSwitch
	}
	if harnessID != row.Harness && body.Account == nil {
		// An account is one agent's: another agent starts on its own.
		opts.Account = ""
	}
	if refused := s.checkChatAccount(harnessID, opts.Account); refused != nil {
		writeError(w, refused.status, refused.msg)
		return
	}
	options := withOption(stripChoices(row.Options), "ultracode", chatUltracode(harnessID, ultracode))
	options = withChatAccount(options, opts.Account, opts.AutoSwitch)
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
	if row.TitleSource == "" && (harnessID != row.Harness || mode != row.WorkdirMode || workdir != row.Workdir) {
		title = sessionTitle("", chatLabel(harnessID), workdir, mode)
	}
	err = s.store.UpdatePrestart(id, store.PrestartSettings{
		Title:   title,
		Harness: harnessID, Model: strings.TrimSpace(model), Effort: strings.TrimSpace(effort),
		WorkdirMode: mode, Workdir: workdir,
		Options: options,
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

// stripChoices is a prepared row's options without the choices a change
// writes back from its settings, so that a choice taken away is not kept by
// the old options - and without the profile a prepared terminal of an agent
// was given before the agents were chats.
func stripChoices(raw json.RawMessage) json.RawMessage {
	for _, key := range []string{"profile", "profile_auto", "ultracode", "account", "auto_switch"} {
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
	})
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	// Started or put back, the row has moved.
	defer s.events.poke()
	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	next, status, err := s.startPrepared(ctx, row.ID, false)
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

// startPrepared starts a prepared chat, from its row and its draft
// (startPreparedChat). scheduled is the scheduler's call, which claims the
// schedule under the same lock before it starts anything.
//
// It answers the row as it is afterwards, an HTTP status, and the error. A
// session that is not prepared any more is not an error: somebody started it,
// and the answer is the session they started.
func (s *Server) startPrepared(ctx context.Context, id string, scheduled bool) (*store.Session, int, error) {
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

	if !row.IsChat() {
		return row, http.StatusConflict, errors.New("this prepared session is not a chat, and only a chat can be started from one now")
	}
	return s.startPreparedChat(ctx, row)
}

// errNotDue is the scheduler being told it lost its claim.
var errNotDue = errors.New("the schedule was changed before it was due")

// answerWithSessionCode is answerWithSession with a status of its own, and
// tells every other open page the same way.
func (s *Server) answerWithSessionCode(w http.ResponseWriter, code int, id string) {
	s.events.poke()
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
