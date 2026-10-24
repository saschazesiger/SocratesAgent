//go:build !windows

package opencode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// handle maps one /event frame. Frames of another session are dropped:
// /event carries the whole directory, and a subagent's session counts as the
// chat's.
func (a *Adapter) handle(gen int, ev ocEvent) {
	switch ev.Type {
	case "message.updated":
		var p struct {
			Info ocMessage `json:"info"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.handleMessage(gen, p.Info)
		}
	case "message.part.updated":
		var p struct {
			Part ocPart `json:"part"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.handlePart(gen, p.Part)
		}
	case "message.part.delta":
		var p struct {
			SessionID string `json:"sessionID"`
			PartID    string `json:"partID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil && p.Field == "text" {
			a.handleDelta(gen, p.PartID, p.Delta)
		}
	case "session.status":
		var p struct {
			SessionID string   `json:"sessionID"`
			Status    ocStatus `json:"status"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.handleStatus(gen, p.SessionID, p.Status)
		}
	case "session.idle":
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.handleIdle(gen, p.SessionID)
		}
	case "session.error":
		var p struct {
			SessionID string   `json:"sessionID"`
			Error     *ocError `json:"error"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.handleError(gen, p.SessionID, p.Error)
		}
	case "session.compacted":
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil && a.isRoot(gen, p.SessionID) {
			a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeCompacted, Text: "The conversation was compacted."})
		}
	case "permission.asked":
		var p ocPermission
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.handlePermission(gen, p)
		}
	case "question.asked":
		var q Question
		if json.Unmarshal(ev.Properties, &q) == nil {
			a.handleQuestion(gen, q)
		}
	case "question.replied", "question.rejected":
		var p struct {
			RequestID string `json:"requestID"`
		}
		if json.Unmarshal(ev.Properties, &p) == nil {
			a.questionGone(gen, p.RequestID)
		}
	}
}

func (a *Adapter) isRoot(gen int, session string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gen == gen && session != "" && session == a.session
}

func (a *Adapter) handleMessage(gen int, m ocMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || m.ID == "" {
		return
	}
	if m.SessionID != a.session {
		if _, child := a.children[m.SessionID]; child {
			switch m.Role {
			case "user":
				// The task's prompt to the subagent: the task part shows it.
				a.childPrompts[m.ID] = true
			case "assistant":
				a.childStepLocked(m)
			}
		}
		return
	}
	switch m.Role {
	case "user":
		u := a.users[m.ID]
		if u == nil {
			if a.turn != nil || a.posting {
				// OpenCode's own message inside the running turn (an automatic
				// compaction, a compaction's request).
				a.users[m.ID] = &userMsg{our: m.ID, announced: true, started: true}
				return
			}
			// A prompt from elsewhere (an attached TUI): it is shown, and it
			// is a turn.
			u = &userMsg{our: m.ID}
			a.users[m.ID] = u
		}
		a.beginTurnLocked(m.ID)
	case "assistant":
		a.assistantLocked(m)
	}
}

func (a *Adapter) assistantLocked(m ocMessage) {
	as := a.assistants[m.ID]
	if as != nil && as.done {
		return
	}
	if a.turn == nil {
		u := a.users[m.ParentID]
		if u == nil {
			u = &userMsg{our: m.ParentID, announced: true}
			a.users[m.ParentID] = u
		}
		u.started = false
		a.beginTurnLocked(m.ParentID)
	}
	t := a.turn
	if t.adopt && m.ParentID != "" {
		t.user, t.adopt = m.ParentID, false
	}
	if as == nil {
		as = &asstMsg{parent: m.ParentID}
		a.assistants[m.ID] = as
		t.assistants = append(t.assistants, m.ID)
		a.out.Emit(agent.Event{Type: agent.TypeMessage, MessageID: m.ID, Role: agent.RoleAssistant,
			State: agent.MessageStreaming, TurnID: t.id, Model: m.model()})
	}
	as.tokens, as.cost = m.Tokens, m.Cost
	if model := m.model(); model != "" {
		a.lastModel, t.model = model, model
	}
	if m.Error != nil {
		switch m.Error.Name {
		case errAborted:
			t.aborted = true
		default:
			t.failure = m.Error.text()
			if m.Error.Data.StatusCode == http.StatusTooManyRequests {
				t.limited = true
			}
		}
	}
	if m.Time.Completed == 0 {
		return
	}
	as.done = true
	// What still streams ends with the message: complete, or cut short by
	// the abort or the error the message ended with.
	end := agent.PartDone
	if m.Error != nil {
		end = agent.PartInterrupted
	}
	for id, sp := range a.parts {
		if sp.msg == m.ID && sp.parent == "" && sp.state == agent.PartStreaming {
			a.emitPartLocked(id, sp.msg, sp.parent, converted{kind: sp.kind, state: end, text: sp.text, content: sp.content})
		}
	}
	state := agent.MessageDone
	if m.Error != nil {
		state = agent.MessageInterrupted
	}
	a.out.Emit(agent.Event{Type: agent.TypeMessageState, MessageID: m.ID, State: state})
}

// childStepLocked counts a subagent's model call into the running turn:
// OpenCode keeps what a subagent used and cost with its own session.
func (a *Adapter) childStepLocked(m ocMessage) {
	if a.turn == nil {
		return
	}
	as := a.assistants[m.ID]
	if as == nil {
		as = &asstMsg{parent: m.ParentID, child: true}
		a.assistants[m.ID] = as
		a.turn.assistants = append(a.turn.assistants, m.ID)
	}
	as.tokens, as.cost = m.Tokens, m.Cost
}

func (a *Adapter) handlePart(gen int, p ocPart) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || p.ID == "" {
		return
	}
	msg, parent := p.MessageID, ""
	switch {
	case p.SessionID == a.session:
		if u := a.users[p.MessageID]; u != nil {
			a.userPartLocked(u, p)
			return
		}
	default:
		c, ok := a.children[p.SessionID]
		if !ok || a.childPrompts[p.MessageID] {
			return
		}
		msg, parent = c.msg, c.parent
	}
	if p.Type == partStepFinish {
		if p.Tokens != nil && p.SessionID == a.session {
			a.stepLocked(*p.Tokens)
		}
		return
	}
	c, ok := convertPart(p)
	if !ok {
		return
	}
	// A task part that names a subagent for the first time runs it from
	// now on, a new one or one it resumes. A part that named it before (one
	// a catch-up repeats, or one of the history) leaves it where it is.
	if sp := a.parts[p.ID]; c.child != "" && (sp == nil || sp.child != c.child) {
		var since int64
		if p.State != nil {
			since = p.State.Time.Start
		}
		a.children[c.child] = childSession{msg: msg, parent: p.ID, since: since}
	}
	a.emitPartLocked(p.ID, msg, parent, c)
	if c.child != "" {
		a.parts[p.ID].child = c.child
	}
	if c.diff != nil {
		if sp := a.parts[p.ID]; sp != nil && !sp.diff {
			sp.diff = true
			a.emitPartLocked(p.ID+diffSuffix, msg, parent, *c.diff)
		}
	}
}

// userPartLocked keeps the text of a user message the adapter announced
// itself (one that came from elsewhere) up to date.
func (a *Adapter) userPartLocked(u *userMsg, p ocPart) {
	if u.journaled || u.display != "" || p.Type != partText || p.Synthetic || strings.TrimSpace(p.Text) == "" {
		return
	}
	if !u.announced {
		return
	}
	a.out.Emit(agent.Event{Type: agent.TypeMessage, MessageID: u.our, Role: agent.RoleUser,
		State: agent.MessageDelivered, Text: p.Text})
}

// stepLocked reports the context a step left in use.
func (a *Adapter) stepLocked(tokens ocTokens) {
	usage := &agent.Usage{Context: tokens.context()}
	if a.turn != nil {
		a.turn.context = usage.Context
		usage.Window = a.windows[a.turn.model]
	}
	a.out.Emit(agent.Event{Type: agent.TypeUsage, Usage: usage})
}

// emitPartLocked writes a part when it changed since it was last written:
// text that only grew while the part streams is a delta, anything else the
// part whole, and so is a part's end. What a catch-up read is older than
// what /event may have said meanwhile: a part never goes back from its end,
// and text that lags the deltas of a part still streaming (OpenCode stores
// the text when the part ends) is not news.
func (a *Adapter) emitPartLocked(id, msg, parent string, c converted) {
	sp := a.parts[id]
	if sp != nil && !live(sp.state) && live(c.state) {
		return
	}
	if sp != nil && sp.kind == c.kind && sp.state == c.state && bytes.Equal(sp.content, c.content) {
		switch {
		case c.text == sp.text:
			return
		case !live(c.state):
		case strings.HasPrefix(c.text, sp.text):
			a.out.Delta(sp.msg, id, c.text[len(sp.text):])
			sp.text = c.text
			return
		case strings.HasPrefix(sp.text, c.text):
			return
		}
	}
	if sp == nil {
		sp = &seenPart{msg: msg, parent: parent}
		a.parts[id] = sp
	}
	sp.kind, sp.state, sp.text, sp.content = c.kind, c.state, c.text, c.content
	a.out.Emit(agent.Event{Type: agent.TypePart, MessageID: sp.msg, PartID: id, ParentPartID: sp.parent,
		Kind: c.kind, State: c.state, Text: c.text, Content: c.content})
}

// live is a part state that has not ended.
func live(state string) bool { return state == agent.PartStreaming || state == agent.PartRunning }

func (a *Adapter) handleDelta(gen int, partID, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || text == "" {
		return
	}
	sp := a.parts[partID]
	if sp == nil || sp.state != agent.PartStreaming {
		return
	}
	sp.text += text
	a.out.Delta(sp.msg, partID, text)
}

// handleStatus watches for the one status that never ends by itself: a
// retry because the account's limit is reached (research/06 §3.7). That
// turn is stopped as limited; other retries are said once per attempt.
func (a *Adapter) handleStatus(gen int, session string, st ocStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || session != a.session {
		return
	}
	if st.Type != statusRetry {
		a.retries = 0
		return
	}
	if st.Action != nil && st.Action.Reason != "" {
		if a.turn == nil || a.turn.limited {
			return
		}
		a.turn.limited, a.turn.limitSaid = true, true
		text := firstNonEmpty(st.Action.Message, st.Action.Title, st.Message)
		ev := agent.Event{Type: agent.TypeRateLimit, Window: st.Action.Reason, Used: 1}
		if st.Next > time.Now().UnixMilli()+60_000 {
			ev.ResetsAt = st.Next / 1000
		}
		a.turn.failure = firstNonEmpty(st.Action.Title, st.Message)
		a.out.Emit(ev, agent.Event{Type: agent.TypeError, Code: "rate_limit", Text: text})
		cl, s := a.cl, a.session
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), requestWait)
			defer cancel()
			if err := cl.call(ctx, http.MethodPost, "/session/"+s+"/abort", nil, nil); err != nil {
				log.Printf("opencode: abort a limited turn: %v", err)
			}
		}()
		return
	}
	if st.Attempt > a.retries {
		a.retries = st.Attempt
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeRetry,
			Text: fmt.Sprintf("Retrying (attempt %d): %s", st.Attempt, st.Message)})
	}
}

func (a *Adapter) handleIdle(gen int, session string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || session != a.session {
		return
	}
	a.retries = 0
	if a.turn != nil {
		a.endTurnLocked()
	}
}

func (a *Adapter) handleError(gen int, session string, e *ocError) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || session != a.session || e == nil {
		return
	}
	if e.Name == errAborted {
		if a.turn != nil {
			a.turn.aborted = true
		}
		return
	}
	if a.turn == nil && a.failedTurn {
		// The repeat of what the turn that just ended failed with.
		a.failedTurn = false
		return
	}
	text := e.text()
	code := "agent_error"
	switch {
	case e.Name == errProviderAuth || e.Data.StatusCode == http.StatusUnauthorized:
		code = "auth"
		text += " (the account may need to sign in again)"
	case e.Data.StatusCode == http.StatusTooManyRequests:
		code = "rate_limit"
		if a.turn != nil {
			a.turn.limited = true
		}
	}
	if a.turn != nil {
		if a.turn.failure == "" {
			a.turn.failure = text
		}
		if code == "rate_limit" && a.turn.limitSaid {
			return
		}
		a.turn.limitSaid = a.turn.limitSaid || code == "rate_limit"
	}
	a.out.Emit(agent.Event{Type: agent.TypeError, Code: code, Text: text})
}

// beginTurnLocked starts the turn of a user message OpenCode took up, once.
func (a *Adapter) beginTurnLocked(oc string) {
	u := a.users[oc]
	if u == nil || u.started {
		return
	}
	if a.turn != nil {
		a.endTurnLocked()
	}
	u.started = true
	a.failedTurn = false
	t := &turnState{id: newTurnID(), user: oc, our: u.our, adopt: u.adopt, model: a.model}
	if u.adopt {
		t.user = ""
	}
	a.turn = t
	var events []agent.Event
	switch {
	case u.journaled:
		events = append(events, agent.Event{Type: agent.TypeMessageState, MessageID: u.our, State: agent.MessageDelivered})
	case !u.announced:
		u.announced = true
		events = append(events, agent.Event{Type: agent.TypeMessage, MessageID: u.our, Role: agent.RoleUser,
			State: agent.MessageDelivered, Text: u.display})
	}
	events = append(events, agent.Event{Type: agent.TypeTurnStarted, TurnID: t.id, MessageID: u.our})
	a.out.Emit(events...)
}

// endTurnLocked closes the running turn with what it cost, and starts the
// next queued job.
func (a *Adapter) endTurnLocked() {
	t := a.turn
	if t == nil {
		return
	}
	a.turn = nil
	outcome := agent.OutcomeCompleted
	switch {
	case t.limited:
		outcome = agent.OutcomeLimited
	case t.aborted:
		outcome = agent.OutcomeInterrupted
	case t.failure != "":
		outcome = agent.OutcomeFailed
	}
	a.failedTurn = outcome != agent.OutcomeCompleted
	usage := &agent.Usage{Context: t.context, Window: a.windows[t.model]}
	var cost float64
	var events []agent.Event
	for _, id := range t.assistants {
		as := a.assistants[id]
		if as == nil {
			continue
		}
		usage.Input += as.tokens.Input
		// OpenCode counts the reasoning apart from the output; the
		// journal's output counts it in (AGENT-HOST.md §2).
		usage.Output += as.tokens.Output + as.tokens.Reasoning
		usage.Reasoning += as.tokens.Reasoning
		usage.CacheRead += as.tokens.Cache.Read
		usage.CacheWrite += as.tokens.Cache.Write
		cost += as.cost
		if !as.done && !as.child {
			as.done = true
			state := agent.MessageDone
			if outcome != agent.OutcomeCompleted {
				state = agent.MessageInterrupted
			}
			events = append(events, agent.Event{Type: agent.TypeMessageState, MessageID: id, State: state})
		}
	}
	for i := len(t.assistants) - 1; i >= 0 && usage.Context == 0; i-- {
		// The context is the chat's own, not a subagent's.
		if as := a.assistants[t.assistants[i]]; as != nil && !as.child {
			usage.Context = as.tokens.context()
			break
		}
	}
	text := ""
	if outcome != agent.OutcomeCompleted && outcome != agent.OutcomeInterrupted {
		text = t.failure
	}
	events = append(events, agent.Event{Type: agent.TypeTurnEnded, TurnID: t.id, Outcome: outcome,
		Usage: usage, CostUSD: cost, Text: text})
	a.out.Emit(events...)
	go a.next()
}

// handlePermission answers a permission OpenCode still asks for with
// "always": the config allows everything, and a question nobody sees would
// stop the chat for good.
func (a *Adapter) handlePermission(gen int, p ocPermission) {
	a.mu.Lock()
	_, child := a.children[p.SessionID]
	// A catch-up lists what /event may also have said.
	ours := a.gen == gen && (p.SessionID == a.session || child) && validID(p.ID, "per") && !a.allowed[p.ID]
	if ours {
		a.allowed[p.ID] = true
	}
	cl := a.cl
	a.mu.Unlock()
	if !ours {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestWait)
		defer cancel()
		if err := cl.call(ctx, http.MethodPost, "/permission/"+p.ID+"/reply", map[string]string{"reply": "always"}, nil); err != nil {
			log.Printf("opencode: answer permission %s: %v", p.ID, err)
		}
	}()
	what := p.Permission
	if len(p.Patterns) > 0 {
		what += " (" + strings.Join(p.Patterns, ", ") + ")"
	}
	a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticePermission,
		Text: "OpenCode asked for permission to use " + what + "; it was allowed, as every permission is."})
}

// question is one of OpenCode's own questions, asked as an ask_user payload:
// the config denies the question tool so that the model uses ask_user, but
// one that still arrives is shown the same way (REBUILD.md §5.3).
type question struct {
	id      string
	gen     int
	payload askuser.Payload
}

// handleQuestion asks one of OpenCode's own questions, once: the event and a
// reconcile after a reconnect may both bring it. A question it cannot read
// is dismissed, with a notice, so that the turn does not wait for ever.
func (a *Adapter) handleQuestion(gen int, q Question) {
	if !validID(q.ID, "que") {
		return
	}
	askID := "ask_" + q.ID
	payload, readable := askuser.FromOpenCode(q.Questions)
	a.mu.Lock()
	_, child := a.children[q.SessionID]
	ours := a.gen == gen && (q.SessionID == a.session || child) && !a.questionsSeen[q.ID]
	cl := a.cl
	turnID := ""
	if a.turn != nil {
		turnID = a.turn.id
	}
	if ours {
		a.questionsSeen[q.ID] = true
		if readable {
			a.questions[askID] = &question{id: q.ID, gen: gen, payload: payload}
		}
	}
	a.mu.Unlock()
	if !ours {
		return
	}
	if !readable {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), requestWait)
			defer cancel()
			if err := cl.call(ctx, http.MethodPost, "/question/"+q.ID+"/reject", nil, nil); err != nil {
				log.Printf("opencode: reject question %s: %v", q.ID, err)
			}
		}()
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeQuestion,
			Text: "OpenCode asked a question with its own tool that could not be read; it was dismissed."})
		return
	}
	a.out.Emit(agent.Event{Type: agent.TypeAsk, AskID: askID, TurnID: turnID, Payload: payload.JSON()})
}

// questionGone is OpenCode saying a question was answered or dismissed
// elsewhere (another client of the server, an abort): no call waits on it.
func (a *Adapter) questionGone(gen int, requestID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen {
		return
	}
	delete(a.questions, "ask_"+requestID)
}

// answerQuestion replies to one of OpenCode's own questions, one list of
// labels per question; agent.ErrAskGone when OpenCode no longer waits on it
// (a server started since does not know it).
func (a *Adapter) answerQuestion(ctx context.Context, askID string, answer json.RawMessage) error {
	a.mu.Lock()
	q := a.questions[askID]
	delete(a.questions, askID)
	gen, cl := a.gen, a.cl
	a.mu.Unlock()
	if q == nil || q.gen != gen || cl == nil {
		return agent.ErrAskGone
	}
	body := map[string]any{"answers": askuser.OpenCodeAnswers(&q.payload, answer)}
	if err := cl.call(ctx, http.MethodPost, "/question/"+q.id+"/reply", body, nil); err != nil {
		return fmt.Errorf("%w: %v", agent.ErrAskGone, err)
	}
	return nil
}

func encodeBase64(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }
