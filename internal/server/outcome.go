package server

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The outcome of a turn: what the digest a turn's end starts also says about
// how the turn left the work, when settings.openrouter.outcomes is on.
//
// The detector says that a session is at its prompt; it cannot say whether the
// task it was given is done. So the read Home makes of every turn's end is
// asked one thing more - complete, partly done, waiting on the person, or
// blocked - in the same call, so that its question and its verdict are one
// answer and cannot disagree: a turn that ends by asking is waiting on the
// person, whatever else the model thought.
//
// An outcome belongs to one idle: the Since of the commit it judges. It is
// pending from the moment that idle is committed - before the activity frame
// goes out, so that every page knows its notification is to wait for it - and
// it is gone the moment the session leaves that idle for anything but the
// detector losing sight of it for a tick. It is kept on this machine (kv,
// never the bucket), so that a restart shows it again while the session is
// still at the idle it was made for.
//
// It is an object of its own beside the digest rather than a field in it: an
// answer is checked against the digest's rev and phase, and a verdict
// arriving, or going, must not tell somebody answering a question that the
// question has changed.

const (
	// outcomesKey is the kv row the outcomes are kept in.
	outcomesKey = "outcomes"
	// outcomeWait is the longest a notification waits for an outcome. A
	// read at a turn's end takes a few seconds; a model that is slower, or
	// down, costs the verdict, not the notification.
	outcomeWait = 45 * time.Second
	// outcomeReasonRunes and outcomeTodoRunes bound what the model says: a
	// sentence, and a short line per task still open.
	outcomeReasonRunes = 160
	outcomeTodoRunes   = 100
	// outcomeTodos is how many open tasks an outcome lists.
	outcomeTodos = 5
	// outcomeMaxTokens is digestMaxTokens with room for the verdict.
	outcomeMaxTokens = 2000
	// The Claude turn handed to the model: the instruction, the end of the
	// answer, and the task list, each bounded.
	turnPromptRunes = 2000
	turnAnswerRunes = 3000
	turnTasks       = 20
	turnTaskRunes   = 120
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
// made for: the prompt reached from work, from background work, or from a
// prompt that asked - and only in a session somebody has typed at, so that a
// harness's own start-up is not judged.
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
// The digest the same change starts (observe) is the read that answers it.
func (d *digester) outcomeEdge(id string, a termux.Activity) {
	if d == nil {
		return
	}
	starts, chat := false, false
	if d.srv.answers == nil || !d.srv.answers.live(id) {
		d.mu.Lock()
		prev, had := d.oseen[id]
		d.mu.Unlock()
		starts = had && endsTurn(prev, a) && outcomesOn(d.srv)
	}
	if starts {
		// The row is read without the lock: a store read under it would hold
		// up every frame the digester publishes. A chat is judged from its
		// journal, by a call of its own (judgeChat).
		row, err := d.srv.store.GetSession(id)
		chat = err == nil && row.IsChat() && row.State == store.StateRunning
		starts = err == nil && (digestible(row) || chat)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone[id] {
		return
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
		if chat {
			go d.judgeChat(id, a)
		}
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

// wantsOutcomeLocked is whether a read made in activity a is to say the
// outcome too: there is one waiting for this very idle - pending, or failed
// and being read again. Called with the lock held.
func (d *digester) wantsOutcomeLocked(id string, a termux.Activity) bool {
	o := d.outcomes[id]
	return o != nil && a.State == termux.StateIdle && (o.Since == a.Since || o.prior == a.Since) &&
		(o.Phase == digestPending || o.Phase == digestError)
}

// rejudgeLocked is a person asking for a session to be read again: the
// verdict on the idle it is at is read again with it. Called with the lock
// held, before the read is requested.
func (d *digester) rejudgeLocked(id string, a termux.Activity) {
	o := d.outcomes[id]
	if o == nil || a.State != termux.StateIdle || o.Since != a.Since || o.Phase == digestPending ||
		!outcomesOn(d.srv) {
		return
	}
	o.Phase, o.Error, o.Due = digestPending, "", 0
	d.publishOutcomeLocked(id, o)
}

// judgedLocked ends the outcome a read was asked for, with the model's verdict
// or with why there is none. in is the activity the read was made in, now the
// session's last committed one, and question what the same read found asked.
// Called with the lock held.
func (d *digester) judgedLocked(id string, in termux.Activity, answer *outcomeAnswer,
	question *digestQuestion, model, why string) {
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
		kind, reason, todos, why = sanitizeOutcome(answer, question)
	}
	o.Model, o.MadeAt = model, time.Now().UnixMilli()
	if why != "" {
		o.Phase, o.Error = digestError, why
	} else {
		o.Phase, o.Error, o.Kind, o.Reason, o.Todos = digestReady, "", kind, reason, todos
	}
	d.publishOutcomeLocked(id, o)
	d.saveOutcomesLocked()
	d.srv.push.judged(id, o.clone(), question)
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
// none. A question the same read found decides it: a turn that ends by asking
// is waiting on the person.
func sanitizeOutcome(a *outcomeAnswer, question *digestQuestion) (kind, reason string, todos []string, why string) {
	if a == nil {
		if question == nil {
			return "", "", nil, "the home model gave no outcome"
		}
		a = &outcomeAnswer{}
	}
	kind = strings.ToLower(strings.TrimSpace(a.Kind))
	switch kind {
	case outcomeComplete, outcomePartial, outcomeNeedsInput, outcomeBlocked:
	default:
		if question == nil {
			return "", "", nil, "the home model gave no outcome it could be read as: " + oneLine(a.Kind, 40)
		}
	}
	if question != nil {
		kind = outcomeNeedsInput
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

// outcomeSystemPrompt is what the digest's instructions say about the verdict,
// added only to a read that is asked for one: with the setting off, a digest
// is asked exactly what it always was.
func outcomeSystemPrompt() string {
	return "\nThis read is also asked for the outcome of the turn that just ended. Add to the object:\n" +
		`"outcome":{"kind":"complete|partial|needs_input|blocked","reason":"…","todos":["…"]}` + "\n" +
		outcomeRules() + "When a question is set, the outcome is needs_input."
}

// outcomeRules is how a verdict is judged, for a terminal's read and a
// chat's judge alike.
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

/* ---------------------------------------------------- Claude's own record */

// claudeTurn is the turn that just ended as Claude recorded it: the
// instruction the person gave, what Claude answered, and its task list. The
// screen holds the end of the answer only, and the instruction it is judged
// against may have scrolled away long before.
type claudeTurn struct {
	Prompt string
	Answer string
	Tasks  []claudeTask
}

type claudeTask struct {
	Subject string
	Status  string
}

// taskCreated is the tool's answer to a TaskCreate, which is where the id the
// TaskUpdates name comes from.
var taskCreated = regexp.MustCompile(`Task #(\d+) created`)

// readClaudeTurn reads the end of a Claude transcript for the turn that just
// ended. It reads the same tail the pending question is read from, and any
// failure to find or parse it is no turn at all: the screen is still there.
//
// The records are the ones Claude Code 2.1 writes: a prompt is a user record
// whose content is a string or text blocks; an answer is the text blocks of
// the assistant records after it; and the task list is what the TaskCreate and
// TaskUpdate tool calls made of it.
func readClaudeTurn(configDir, cliSession string) (claudeTurn, bool) {
	cliSession = strings.TrimSpace(cliSession)
	if configDir == "" || cliSession == "" || filepath.Base(cliSession) != cliSession {
		return claudeTurn{}, false
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", cliSession+".jsonl"))
	if err != nil || len(matches) == 0 {
		return claudeTurn{}, false
	}
	data, err := readTail(matches[0], digestTranscriptTail)
	if err != nil {
		return claudeTurn{}, false
	}
	return parseClaudeTurn(data), true
}

func parseClaudeTurn(data []byte) claudeTurn {
	var turn claudeTurn
	var answer []string
	type task struct {
		id      string
		subject string
		status  string
	}
	var tasks []*task
	byID := map[string]*task{}
	// creating is the TaskCreate calls whose answer, with the id in it, has
	// not been read yet.
	creating := map[string]*task{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || (entry.Type != "user" && entry.Type != "assistant") {
			continue
		}
		var text string
		if entry.Type == "user" && json.Unmarshal(entry.Message.Content, &text) == nil {
			if !entry.IsMeta && personTyped(text) {
				turn.Prompt, answer = text, nil
			}
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		var typed []string
		for _, block := range blocks {
			switch {
			case entry.Type == "user" && block.Type == "text":
				typed = append(typed, block.Text)
			case entry.Type == "user" && block.Type == "tool_result":
				t := creating[block.ToolUseID]
				if t == nil {
					continue
				}
				delete(creating, block.ToolUseID)
				if m := taskCreated.FindStringSubmatch(toolResultText(block.Content)); m != nil {
					t.id = m[1]
					byID[t.id] = t
				}
			case entry.Type == "assistant" && block.Type == "text":
				if s := strings.TrimSpace(block.Text); s != "" {
					answer = append(answer, s)
				}
			case entry.Type == "assistant" && block.Type == "tool_use" && block.Name == "TaskCreate":
				var in struct {
					Subject string `json:"subject"`
				}
				if json.Unmarshal(block.Input, &in) != nil || strings.TrimSpace(in.Subject) == "" {
					continue
				}
				t := &task{subject: in.Subject, status: "pending"}
				tasks = append(tasks, t)
				creating[block.ID] = t
			case entry.Type == "assistant" && block.Type == "tool_use" && block.Name == "TaskUpdate":
				var in struct {
					TaskID string `json:"taskId"`
					Status string `json:"status"`
				}
				if json.Unmarshal(block.Input, &in) != nil {
					continue
				}
				if t := byID[strings.TrimSpace(in.TaskID)]; t != nil && in.Status != "" {
					t.status = in.Status
				}
			}
		}
		if prompt := strings.TrimSpace(strings.Join(typed, "\n")); entry.Type == "user" && !entry.IsMeta &&
			prompt != "" && personTyped(prompt) {
			turn.Prompt, answer = prompt, nil
		}
	}
	turn.Prompt = clipRunes(strings.TrimSpace(turn.Prompt), turnPromptRunes)
	if all := strings.Join(answer, "\n\n"); all != "" {
		runes := []rune(all)
		if len(runes) > turnAnswerRunes {
			all = "…" + strings.TrimSpace(string(runes[len(runes)-turnAnswerRunes+1:]))
		}
		turn.Answer = all
	}
	for _, t := range tasks {
		if t.status == "deleted" {
			continue
		}
		turn.Tasks = append(turn.Tasks, claudeTask{Subject: oneLine(t.subject, turnTaskRunes), Status: t.status})
	}
	if len(turn.Tasks) > turnTasks {
		turn.Tasks = turn.Tasks[len(turn.Tasks)-turnTasks:]
	}
	return turn
}

// personTyped is whether a user record's text is something the person said,
// rather than what Claude Code writes in the person's name: the news of a
// background task, the output of a local command, a reminder.
func personTyped(text string) bool {
	text = strings.TrimSpace(text)
	for _, prefix := range []string{"<task-notification>", "<local-command-", "<system-reminder>",
		"[Request interrupted"} {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}
	return text != ""
}

// toolResultText is a tool_result's content as text: a string, or the text of
// its blocks.
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Text)
	}
	return b.String()
}

// turnPrompt is the part of a digest's instruction that hands the model the
// turn as Claude recorded it.
func turnPrompt(turn claudeTurn) string {
	var b strings.Builder
	if turn.Prompt != "" {
		b.WriteString("The last instruction the person gave the program:\n" + turn.Prompt + "\n")
	}
	if turn.Answer != "" {
		b.WriteString("The program's answer to it, as it recorded it:\n" + turn.Answer + "\n")
	}
	if len(turn.Tasks) > 0 {
		b.WriteString("The program's task list:\n")
		for _, t := range turn.Tasks {
			mark := "[ ]"
			switch t.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			b.WriteString(mark + " " + t.Subject + "\n")
		}
	}
	return b.String()
}
