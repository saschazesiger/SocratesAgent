package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The outcome of a turn: how a chat's turn left the work, when
// settings.openrouter.outcomes is on.
//
// A chat's journal says that a turn ended; it cannot say whether the task the
// turn was given is done. So the end of every turn is judged by the home model
// as well (chatjudge.go) - complete, partly done, waiting on the person, or
// blocked - from the turn as the journal recorded it.
//
// An outcome belongs to one idle: the Since of the commit it judges. It is
// pending from the moment that idle is committed - before the activity frame
// goes out, so that every page knows its notification is to wait for it - and
// it is gone the moment the chat leaves that idle for anything but the
// detector losing sight of it for a tick. It is kept on this machine (kv),
// so that a restart shows it again while the chat is still at the idle it
// was made for.
//
// It is an object of its own beside the digest rather than a field in it: a
// verdict arriving, or going, must not tell somebody answering a question
// that the question has changed.

const (
	// outcomesKey is the kv row the outcomes are kept in.
	outcomesKey = "outcomes"
	// outcomeWait is the longest a notification waits for an outcome. A
	// verdict takes a few seconds; a model that is slower, or down, costs the
	// verdict, not the notification.
	outcomeWait = 45 * time.Second
	// outcomeReasonRunes and outcomeTodoRunes bound what the model says: a
	// sentence, and a short line per task still open.
	outcomeReasonRunes = 160
	outcomeTodoRunes   = 100
	// outcomeTodos is how many open tasks an outcome lists.
	outcomeTodos = 5
	// outcomeMaxTokens is a ceiling, not a target. The answer is a short
	// object, but a model that thinks before it answers spends its thinking
	// out of the same budget.
	outcomeMaxTokens = 2000
	// turnTaskRunes bounds one line of the task list a judge is shown.
	turnTaskRunes = 120
)

// The kinds of an outcome.
const (
	outcomeComplete   = "complete"
	outcomePartial    = "partial"
	outcomeNeedsInput = "needs_input"
	outcomeBlocked    = "blocked"
)

// outcome is one session's verdict on its last turn, exactly as the browser
// receives it.
type outcome struct {
	SessionID string `json:"session_id"`
	// Rev moves on every change, so that a page that hears two frames out of
	// order keeps the later one.
	Rev int64 `json:"rev"`
	// Since is the Since of the idle the outcome is for. A page shows an
	// outcome only while the session's activity has the same Since.
	Since  int64    `json:"since"`
	Phase  string   `json:"phase"`
	Kind   string   `json:"kind,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Todos  []string `json:"todos,omitempty"`
	Error  string   `json:"error,omitempty"`
	// Due is when a page stops waiting for the verdict with its
	// notification, in unix milliseconds.
	Due    int64  `json:"due,omitempty"`
	MadeAt int64  `json:"made_at,omitempty"`
	Model  string `json:"model,omitempty"`
	// prior is the Since the outcome was for before a tick of unknown
	// carried it onto the same prompt in a new commit: a read made in that
	// commit is still a read of this idle.
	prior int64
}

func (o outcome) clone() outcome {
	o.Todos = append([]string(nil), o.Todos...)
	return o
}

// outcomeAnswer is the model's verdict before it is sanitised.
type outcomeAnswer struct {
	Kind   string   `json:"kind"`
	Reason string   `json:"reason"`
	Todos  []string `json:"todos"`
}

// outcomesOn is whether a server checks its turns: switched on, and with a
// key to ask with.
func outcomesOn(s *Server) bool {
	settings := s.Settings()
	return settings.OpenRouter.Outcomes && strings.TrimSpace(settings.OpenRouter.APIKey) != ""
}

// endsTurn is whether a committed change is the end of a turn an outcome is
// made for: the idle reached from work, from background work, or from a
// question - and only in a chat somebody has sent something, so that an
// agent's own start-up is not judged.
func endsTurn(prev, next termux.Activity) bool {
	if next.State != termux.StateIdle || !next.Asked {
		return false
	}
	switch prev.State {
	case termux.StateBusy, termux.StateTasks, termux.StateWaiting:
		return true
	}
	return false
}

// loadOutcomes reads the outcomes a previous process kept. Each is shown again
// only if the session turns out to be at the idle it was made for
// (outcomeEdge), and every one of them starts this process's revs.
func (d *digester) loadOutcomes() {
	if d.srv.store == nil {
		return
	}
	var kept map[string]outcome
	if err := d.srv.store.GetJSON(outcomesKey, &kept); err != nil {
		return
	}
	for id, o := range kept {
		if o.Phase != digestReady && o.Phase != digestError {
			continue
		}
		o.SessionID, o.Rev, o.Due = id, d.epoch, 0
		d.outcomes[id] = &o
	}
}

// saveOutcomesLocked keeps the outcomes there are, the finished ones: a
// pending verdict cannot be finished by another process. Called with the lock
// held, so that two saves cannot land in the wrong order.
func (d *digester) saveOutcomesLocked() {
	if d.srv.store == nil {
		return
	}
	kept := map[string]outcome{}
	for id, o := range d.outcomes {
		if o.Phase == digestReady || o.Phase == digestError {
			kept[id] = o.clone()
		}
	}
	_ = d.srv.store.SetJSON(outcomesKey, kept)
}

// publishOutcomeLocked tells every socket and every stream about a changed
// outcome, or one that is gone (o nil). Called with the lock held, so that
// two changes reach every page in the order they were made.
func (d *digester) publishOutcomeLocked(id string, o *outcome) {
	frame := map[string]any{"t": "outcome", "id": id, "outcome": nil}
	if o != nil {
		o.Rev++
		frame["outcome"] = o.clone()
	}
	d.srv.broadcastAll(func(*termViewer) any { return frame })
	d.srv.events.publish(frame)
}

// dropOutcomeLocked takes a session's outcome away. Called with the lock held.
func (d *digester) dropOutcomeLocked(id string) {
	if _, ok := d.outcomes[id]; !ok {
		return
	}
	delete(d.outcomes, id)
	d.publishOutcomeLocked(id, nil)
	d.saveOutcomesLocked()
}

// outcomeEdge is fed every committed activity change, before the activity
// frame goes out: a turn that has just ended is put on pending here, so that a
// page hears that its verdict is coming before it hears that the turn ended.
// It hands back the judge that answers it (judgeChat), which the caller
// starts once the pusher has seen the change too: a judge that answers at
// once could otherwise come back before the "Finished" it is meant to hold
// was put on the wait, and the push would go out without the verdict.
func (d *digester) outcomeEdge(id string, a termux.Activity) (judge func()) {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	last, seen := d.oseen[id]
	d.mu.Unlock()
	starts := seen && endsTurn(last, a) && outcomesOn(d.srv)
	if starts {
		// The row is read without the lock: a store read under it would hold
		// up every frame the digester publishes. Only a chat takes turns, and
		// a turn the person stopped themselves gets no verdict
		// (chatHub.judgeable).
		row, err := d.srv.store.GetSession(id)
		starts = err == nil && row.IsChat() && row.State == store.StateRunning &&
			(d.srv.chats == nil || d.srv.chats.judgeable(id))
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone[id] {
		return nil
	}
	prev, had := d.oseen[id]
	d.oseen[id] = a
	known := d.oknown[id]
	if a.State != termux.StateUnknown {
		d.oknown[id] = a
	}
	o := d.outcomes[id]
	switch {
	case starts && had && prev.State != a.State:
		now := time.Now()
		next := &outcome{SessionID: id, Rev: d.epoch, Since: a.Since, Phase: digestPending,
			Due: now.Add(outcomeWait).UnixMilli()}
		if o != nil {
			next.Rev = o.Rev
		}
		d.outcomes[id] = next
		d.publishOutcomeLocked(id, next)
		d.saveOutcomesLocked()
		judge = func() { d.judgeChat(id, a) }
	case o == nil:
	case a.State == termux.StateUnknown:
		// The detector lost sight of the session for a moment, or has not
		// found it yet after a restart: nothing has been said about the idle
		// the outcome is for.
	case a.State != termux.StateIdle:
		// A new turn, a prompt, background work: the turn the outcome was
		// for is over.
		d.dropOutcomeLocked(id)
	case a.Since == o.Since:
	case known.State == termux.StateIdle && known.Since == o.Since:
		// Back on the very prompt after a tick of unknown, in a new commit:
		// the verdict is still the verdict on it.
		o.prior, o.Since = o.Since, a.Since
		d.publishOutcomeLocked(id, o)
		d.saveOutcomesLocked()
	default:
		// Kept from before a restart for an idle the session is no longer
		// at.
		d.dropOutcomeLocked(id)
	}
	return judge
}

// outcomePending is whether the outcome of the idle at since is still being
// read: what a notification of that turn's end waits for.
func (d *digester) outcomePending(id string, since int64) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.outcomes[id]
	return o != nil && o.Phase == digestPending && o.Since == since
}

// outcomesAll is every outcome there is, for a hello.
func (d *digester) outcomesAll() map[string]outcome {
	out := map[string]outcome{}
	if d == nil {
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, o := range d.outcomes {
		out[id] = o.clone()
	}
	return out
}

// outcomeOf is one session's outcome, and false when it has none.
func (d *digester) outcomeOf(id string) (outcome, bool) {
	if d == nil {
		return outcome{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.outcomes[id]
	if o == nil {
		return outcome{}, false
	}
	return o.clone(), true
}

// clearOutcomes is the setting switched off: every verdict goes, and every
// notification that was waiting for one goes out as it would have without it.
func (d *digester) clearOutcomes() {
	if d == nil {
		return
	}
	d.mu.Lock()
	for id := range d.outcomes {
		delete(d.outcomes, id)
		d.publishOutcomeLocked(id, nil)
	}
	d.saveOutcomesLocked()
	d.mu.Unlock()
	d.srv.push.releaseFinished()
}

// judgedLocked ends the outcome a judge was asked for, with the model's
// verdict or with why there is none. in is the activity the judge was started
// in. Called with the lock held.
func (d *digester) judgedLocked(id string, in termux.Activity, answer *outcomeAnswer, model, why string) {
	o := d.outcomes[id]
	if o == nil || o.Phase == digestReady || in.State != termux.StateIdle || (o.Since != in.Since && o.prior != in.Since) {
		return
	}
	if now, ok := d.lastSeenLocked(id); ok && (now.State != termux.StateIdle || now.Since != o.Since) {
		// The session left the idle while the model was thinking; the
		// change that took it away dropped the outcome already.
		return
	}
	kind, reason, todos := "", "", []string(nil)
	if why == "" {
		kind, reason, todos, why = sanitizeOutcome(answer)
	}
	o.Model, o.MadeAt = model, time.Now().UnixMilli()
	if why != "" {
		o.Phase, o.Error = digestError, why
	} else {
		o.Phase, o.Error, o.Kind, o.Reason, o.Todos = digestReady, "", kind, reason, todos
	}
	d.publishOutcomeLocked(id, o)
	d.saveOutcomesLocked()
	d.srv.push.judged(id, o.clone())
}

// lastSeenLocked is a session's newest committed activity the digester has
// been told of: outcomeEdge hears every commit before observe does, and a
// verdict that is ready in between (a chat's judge, which outcomeEdge
// starts, can be) is checked against the commit it was started for rather
// than the one before it. Called with the lock held.
func (d *digester) lastSeenLocked(id string) (termux.Activity, bool) {
	now, ok := d.seen[id]
	if edge, had := d.oseen[id]; had && (!ok || edge.Since > now.Since) {
		return edge, true
	}
	return now, ok
}

// sanitizeOutcome is the model's verdict as Socrates shows it, or why there is
// none.
func sanitizeOutcome(a *outcomeAnswer) (kind, reason string, todos []string, why string) {
	if a == nil {
		return "", "", nil, "the home model gave no outcome"
	}
	kind = strings.ToLower(strings.TrimSpace(a.Kind))
	switch kind {
	case outcomeComplete, outcomePartial, outcomeNeedsInput, outcomeBlocked:
	default:
		return "", "", nil, "the home model gave no outcome it could be read as: " + oneLine(a.Kind, 40)
	}
	reason = oneLine(a.Reason, outcomeReasonRunes)
	if kind == outcomePartial {
		for _, todo := range a.Todos {
			if todo = oneLine(todo, outcomeTodoRunes); todo != "" {
				todos = append(todos, todo)
			}
			if len(todos) == outcomeTodos {
				break
			}
		}
	}
	return kind, reason, todos, ""
}

// outcomeRules is how a verdict is judged.
func outcomeRules() string {
	return "Judge the outcome against the last instruction the person gave the program:\n" +
		"- complete: everything that was asked is done and nothing is left over.\n" +
		"- partial: this round is done but the task is not: open tasks, next steps the program " +
		"names itself, or parts of the request it did not do.\n" +
		"- needs_input: the program is waiting on the person - a question, a decision, an " +
		"approval, or something only the person can do, such as logging in or testing by hand.\n" +
		"- blocked: it could not go on - an error it could not fix, a build or test it left " +
		"failing, a refusal, or it gave up.\n" +
		"reason: one sentence of at most " + strconv.Itoa(outcomeReasonRunes) + " characters, " +
		"in English whatever the language above, saying why.\n" +
		"todos: for partial, up to " + strconv.Itoa(outcomeTodos) + " short English lines naming " +
		"what is still open; [] for every other kind.\n"
}

// outcomeWords is a finished outcome as a sentence fragment, for a model that
// is told about it (statusPrompt), or "" for one that has no verdict.
func outcomeWords(o outcome) string {
	if o.Phase != digestReady {
		return ""
	}
	words := map[string]string{
		outcomeComplete:   "the task is complete",
		outcomePartial:    "this round is done but the task is not",
		outcomeNeedsInput: "it is waiting on the person",
		outcomeBlocked:    "it is blocked",
	}[o.Kind]
	if words == "" {
		return ""
	}
	if o.Reason != "" {
		words += " (" + o.Reason + ")"
	}
	if len(o.Todos) > 0 {
		words += "; still open: " + strings.Join(o.Todos, "; ")
	}
	return words
}
