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

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// The chats' clock: the two things a chat does by itself at a time.
//
//   - A scheduled start. A chat nobody has written to yet can be given a
//     time (start_at); then its draft - what its large first field holds -
//     goes as its first message, exactly as Send would send it, and the field
//     is emptied.
//   - The automatic continue. A chat whose last turn a usage limit stopped,
//     and that nobody has done anything with since, is told to go on
//     (agenthost.ContinueText) a minute after the limit renews, unless its
//     auto_continue option is off.
//
//	PUT /api/chats/{id}/start_at      {"start_at": ms}  → {"session"}
//	                                  0 clears; a time has to be in the
//	                                  future; 409 once the chat has a message
//	PUT /api/chats/{id}/auto-continue {"on"}            → {"session"}
//
// It is a loop rather than a timer per chat because what it acts on lives
// in the rows and the chats' briefs, and both change under it from anywhere.
// Every pass reads them again; between passes it sleeps until the next due
// time, and never longer than scheduleTick, so a time written by something
// that did not poke it is still seen within seconds. Both survive a restart:
// the schedule is the row's, the limit the journal's (chatBrief.Limit), and
// a time that fell due while the server was down is due at the first pass.
//
// Each acts once. A scheduled start clears its schedule in the same step
// that claims it (store.ClaimScheduledStart), so one that fails is reported
// and left for the person; a continue goes with a client_id made from the
// turn the limit stopped, so a second one is the first one again, and once
// it is journaled the chat no longer stands at that limit.

// scheduleTick is the longest the loop sleeps between two passes.
const scheduleTick = 10 * time.Second

// scheduleMinWait keeps a pass that could not act on something due - a
// claim lost to a change made at that moment - from turning into a busy loop.
const scheduleMinWait = 500 * time.Millisecond

// autoContinueDelay is how long after a limit renews the chat goes on: the
// provider's clock and the window's own rounding are not to the second.
const autoContinueDelay = time.Minute

// The words of the notices the journal gets before what the clock sends.
const (
	autoContinuedWords  = "Continued after the limit renewed."
	scheduledStartWords = "Sent at the time it was scheduled for."
)

type scheduler struct {
	srv  *Server
	now  func() time.Time
	wake chan struct{}

	mu sync.Mutex
	// continued is the limits this run has told a chat to go on from, by
	// continueKey: a send that failed is not tried again every pass.
	continued map[string]bool
}

func newScheduler(s *Server) *scheduler {
	return &scheduler{srv: s, now: time.Now, wake: make(chan struct{}, 1), continued: map[string]bool{}}
}

// poke makes the loop look again now: a time was just set or moved.
func (sc *scheduler) poke() {
	if sc == nil {
		return
	}
	select {
	case sc.wake <- struct{}{}:
	default:
	}
}

// start runs the loop until ctx ends. The first pass runs at once.
func (sc *scheduler) start(ctx context.Context) {
	go func() {
		for {
			sc.runDue(ctx, sc.now())
			wait := scheduleTick
			if next := sc.nextDue(); next > 0 {
				until := time.UnixMilli(next).Sub(sc.now())
				wait = min(wait, max(until, scheduleMinWait))
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-sc.wake:
				timer.Stop()
			}
		}
	}()
}

// runDue does what is due at now, one chat after the other, and reports
// how many things it did.
func (sc *scheduler) runDue(ctx context.Context, now time.Time) (done int) {
	rows, err := sc.srv.store.ListSessions()
	if err != nil {
		log.Printf("chats' clock: could not read the sessions: %v", err)
		return 0
	}
	at := now.UnixMilli()
	for i := range rows {
		row := &rows[i]
		if ctx.Err() != nil {
			return done
		}
		if !row.IsChat() {
			continue
		}
		if row.StartAt > 0 && row.StartAt <= at && sc.startOne(ctx, row) {
			done++
		}
		if due, limit := sc.continueAt(row); due > 0 && due <= at && sc.continueOne(ctx, row, limit) {
			done++
		}
	}
	return done
}

// nextDue is the earliest time anything is due, or zero.
func (sc *scheduler) nextDue() int64 {
	rows, err := sc.srv.store.ListSessions()
	if err != nil {
		return 0
	}
	var next int64
	for i := range rows {
		row := &rows[i]
		if !row.IsChat() {
			continue
		}
		for _, at := range []int64{row.StartAt, first(sc.continueAt(row))} {
			if at > 0 && (next == 0 || at < next) {
				next = at
			}
		}
	}
	return next
}

func first(at int64, _ *chatLimit) int64 { return at }

/* ------------------------------------------------------ scheduled start */

// startOne starts one due chat: its draft goes as its first message. It
// reports whether it owned the start.
func (sc *scheduler) startOne(ctx context.Context, row *store.Session) bool {
	srv := sc.srv
	claimed, err := srv.store.ClaimScheduledStart(row.ID, row.StartAt)
	if err != nil {
		log.Printf("scheduled start of chat %s: %v", row.ID, err)
		return false
	}
	if !claimed {
		return false
	}
	srv.events.poke()
	p, err := srv.chats.peer(row.ID)
	if err != nil {
		sc.startFailed(row, err.Error())
		return true
	}
	p.mu.Lock()
	asked := p.brief.Summary.Asked
	p.mu.Unlock()
	if asked {
		// Somebody wrote to it in the meantime: it is started, and not by
		// this, which has nothing to announce.
		log.Printf("scheduled start of chat %s: it had started already", row.ID)
		return true
	}
	draft, err := srv.store.GetDraft(row.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		sc.startFailed(row, err.Error())
		return true
	}
	if strings.TrimSpace(draft.Text) == "" {
		sc.startFailed(row, "nothing was written")
		return true
	}
	one, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	code, answer := srv.sendMessage(one, p, &chatMessageBody{
		ClientID: fmt.Sprintf("scheduled-%s-%d", row.ID, row.StartAt), Text: draft.Text,
		Notice: agent.NoticeScheduledStart, Reason: scheduledStartWords,
	})
	if code >= 300 {
		sc.startFailed(row, fmt.Sprint(answer["error"]))
		return true
	}
	// Sent: the field is emptied, the way Send empties the composer. A
	// draft somebody changed in this very moment is theirs, and stays.
	if _, err := srv.store.SaveDraft(row.ID, "", draft.Rev); err != nil {
		log.Printf("scheduled start of chat %s: the draft was not emptied: %v", row.ID, err)
	}
	srv.events.poke()
	log.Printf("scheduled start of chat %s: sent", row.ID)
	sc.announce(row, "Started: "+row.Title, "The scheduled start ran.", "normal")
	return true
}

func (sc *scheduler) startFailed(row *store.Session, reason string) {
	log.Printf("scheduled start of chat %s failed: %s", row.ID, reason)
	sc.announce(row, "Scheduled start failed: "+row.Title, reason, "high")
}

/* -------------------------------------------------- the automatic continue */

// continueAt is when a chat goes on by itself from the limit it stands at,
// and the limit; zero when it does not: no limit, no time it renews, the
// option off, the chat filed done, or this run already acted on that limit.
func (sc *scheduler) continueAt(row *store.Session) (int64, *chatLimit) {
	if !autoContinueOn(row.Options) || row.Status == store.StatusDone {
		return 0, nil
	}
	brief, ok := sc.srv.chats.briefOfChat(row.ID)
	if !ok || brief.Limit == nil || brief.Limit.RenewsAt <= 0 {
		return 0, nil
	}
	sc.mu.Lock()
	done := sc.continued[continueKey(row.ID, brief.Limit)]
	sc.mu.Unlock()
	if done {
		return 0, nil
	}
	return brief.Limit.RenewsAt + autoContinueDelay.Milliseconds(), brief.Limit
}

func continueKey(id string, l *chatLimit) string {
	turn := l.Turn
	if turn == "" {
		turn = fmt.Sprint(l.EndedAt)
	}
	return id + "-" + turn
}

// continueOne tells a chat to go on from its limit. It reports whether it
// did.
func (sc *scheduler) continueOne(ctx context.Context, row *store.Session, limit *chatLimit) bool {
	srv := sc.srv
	// Something the person sent is on its way: that is them acting, and
	// once it is journaled the chat stands at no limit.
	if pending, err := srv.store.Outbox(row.ID); err != nil || len(pending) > 0 {
		return false
	}
	key := continueKey(row.ID, limit)
	sc.mu.Lock()
	sc.continued[key] = true
	sc.mu.Unlock()
	p, err := srv.chats.peer(row.ID)
	if err != nil {
		log.Printf("chat %s: could not go on after the limit: %v", row.ID, err)
		return false
	}
	one, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	code, answer := srv.sendMessage(one, p, &chatMessageBody{
		ClientID: "auto-continue-" + key, Text: agenthost.ContinueText,
		Notice: agent.NoticeAutoContinued, Reason: autoContinuedWords,
	})
	if code >= 300 && code != http.StatusAccepted {
		log.Printf("chat %s: could not go on after the limit: %v", row.ID, answer["error"])
		return false
	}
	log.Printf("chat %s: the limit renewed; told it to go on", row.ID)
	if dup, _ := answer["dup"].(bool); !dup {
		sc.announce(row, "Continued: "+row.Title, "The limit renewed, and the chat goes on.", "normal")
	}
	return true
}

// autoContinueOn is a chat's auto_continue option: on unless it is false.
func autoContinueOn(raw json.RawMessage) bool {
	var o struct {
		AutoContinue *bool `json:"auto_continue"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o.AutoContinue == nil || *o.AutoContinue
}

// withAutoContinue is options with the auto_continue option: absent for on,
// which is the default, and false for off.
func withAutoContinue(raw json.RawMessage, on bool) json.RawMessage {
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	if on {
		delete(fields, "auto_continue")
	} else {
		fields["auto_continue"] = json.RawMessage("false")
	}
	out, _ := json.Marshal(fields)
	return out
}

// announce is a Web Push about one chat, on the chat's topic like the
// others. A chat filed background is there so as not to interrupt anyone,
// and is not announced.
func (sc *scheduler) announce(row *store.Session, title, body, urgency string) {
	if row.Status == store.StatusBackground {
		return
	}
	note := pushNote{Title: title, Body: body, ID: row.ID, Kind: pushStarted}
	go sc.srv.push.broadcast(note, webpush.Message{TTL: pushTTL, Urgency: urgency, Topic: pushTopic(row.ID)})
}

/* ------------------------------------------------------------ the routes */

func (s *Server) handleChatStartAt(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		StartAt int64 `json:"start_at"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.StartAt < 0 || (body.StartAt > 0 && body.StartAt <= s.sched.now().UnixMilli()) {
		writeError(w, http.StatusBadRequest, "the start time has to be in the future")
		return
	}
	if body.StartAt > 0 {
		p.mu.Lock()
		asked := p.brief.Summary.Asked
		p.mu.Unlock()
		if asked {
			writeError(w, http.StatusConflict, "this chat has started already")
			return
		}
	}
	if err := s.store.SetStartAt(row.ID, body.StartAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.sched.poke()
	s.answerWithSession(w, row.ID)
}

func (s *Server) handleChatAutoContinue(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, err := s.store.UpdateChatOptions(row.ID, func(raw json.RawMessage) json.RawMessage {
		return withAutoContinue(raw, body.On)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.sched.poke()
	s.answerWithSession(w, row.ID)
}
