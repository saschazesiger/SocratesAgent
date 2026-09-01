package engine

import (
	"encoding/json"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Workspace is where a chat's agent works: its own folder below the workspace
// root unless the chat was pinned somewhere else. The server shows the same
// path, so it lives here rather than in two places.
func Workspace(settings config.Settings, chat *store.Chat) string {
	return filepath.Join(settings.Agent.WorkspaceRoot, chat.ID)
}

// draftFlush is how often the answer being written is committed while it
// streams. Every commit is a revision and an SSE frame, so this is the pace at
// which a phone on a bad connection has to keep up.
const draftFlush = 500 * time.Millisecond

// pump turns one turn's harness events into steps and one assistant message.
//
// It is idempotent over replay, because adopting a turn that was partly
// applied before a restart re-applies every event of it. Two things make that
// true: no id is ever generated here - every step id is derived from the run
// id and the event's own id - and PutStep is an upsert, so a re-applied event
// patches the row it wrote the first time.
type pump struct {
	engine *Engine
	chat   *store.Chat
	run    *store.Run
	handle *runHandle

	// completed is the finished text blocks, keyed by block id and kept in
	// arrival order. Keying rather than appending is what keeps "applying an
	// event twice is a no-op with the same result" true for text: a slice that
	// appends would put the same paragraph into the answer twice.
	order     []string
	completed map[string]string
	// partial is the blocks still streaming, and currentID is the one the last
	// delta belonged to.
	partial   map[string]string
	currentID string

	// release lets the chat go before the run's last event is published. The
	// browser sends the next message the moment it sees a run turn done, and a
	// chat still registered as active at that instant answers ErrBusy for a
	// turn that is over.
	release func()
	// lastSeq is the highest journal position this pump has applied. The same
	// event can reach it twice - a live push and the replay that follows it on
	// a reconnect, or the redial after a dropped connection - and every kind
	// but one is idempotent. tool_output is the exception: it carries a delta
	// with no identity, so two deliveries append the same bytes twice. This is
	// what makes that impossible rather than merely unlikely.
	lastSeq   int64
	draftAt   time.Time
	draftLive bool
	toolAt    map[string]time.Time
	done      bool
	cleanup   []func()
}

func newPump(e *Engine, chat *store.Chat, run *store.Run, h *runHandle) *pump {
	return &pump{
		engine: e, chat: chat, run: run, handle: h,
		completed: map[string]string{},
		partial:   map[string]string{},
		toolAt:    map[string]time.Time{},
	}
}

// addCleanup remembers an unsubscribe from a resubscription, so a turn that
// hiccuped twice does not leak the first two.
func (p *pump) addCleanup(fn func()) { p.cleanup = append(p.cleanup, fn) }

func (p *pump) stepID(kind, id string) string { return p.run.ID + ":" + kind + ":" + id }

// nextSeq orders the steps of a run. The counter belongs to the run handle and
// was seeded from the chat's transcript when the run started or was adopted,
// so a step written after a restart still sorts after everything before it. A
// step that already exists keeps its own seq; that is done in put.
func (p *pump) nextSeq() int64 { return p.handle.seq.Add(1) }

// put writes a step, keeping the sequence number a previous version of it had
// so that a replay does not reorder the transcript.
func (p *pump) put(st *store.Step) {
	if existing, err := p.engine.Store.GetStep(st.ID); err == nil {
		st.Seq, st.CreatedAt = existing.Seq, existing.CreatedAt
	} else {
		st.Seq = p.nextSeq()
	}
	st.RunID, st.ChatID = p.run.ID, p.chat.ID
	if err := p.engine.commitStep(st); err != nil {
		log.Printf("engine: commit step: %v", err)
	}
}

func detail(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// noteSession records the agent's own session id on the chat row, which is
// what a resume after a restart is built on.
func (p *pump) noteSession(session string) {
	if session == "" || session == p.chat.AgentSession {
		return
	}
	if err := p.engine.Store.SetChatSession(p.chat.ID, session); err != nil {
		log.Printf("engine: could not record the session id: %v", err)
		return
	}
	p.chat.AgentSession = session
}

// seen reports whether this journal position has already been applied, and
// records it when it has not.
func (p *pump) seen(seq int64) bool {
	if seq != 0 && seq <= p.lastSeq {
		return true
	}
	if seq > p.lastSeq {
		p.lastSeq = seq
	}
	return false
}

// apply is one harness event.
func (p *pump) apply(ev harness.Event) {
	switch ev.Kind {
	case harness.KindTurnStarted:
		// The run event already went out at Start.

	case harness.KindTextDelta:
		p.partial[ev.ID] = p.partial[ev.ID] + ev.Text
		p.currentID = ev.ID
		p.patchDraft(false)

	case harness.KindText:
		if _, seen := p.completed[ev.ID]; !seen {
			p.order = append(p.order, ev.ID)
		}
		p.completed[ev.ID] = ev.Text
		delete(p.partial, ev.ID)
		if p.currentID == ev.ID {
			p.currentID = ""
		}
		p.patchDraft(true)

	case harness.KindReasoning:
		if strings.TrimSpace(ev.Text) == "" {
			return
		}
		p.put(&store.Step{
			ID: p.stepID("reasoning", ev.ID), Kind: store.StepReasoning,
			Title: "Reasoning", Body: ev.Text, Status: store.StatusDone,
		})

	case harness.KindToolStarted, harness.KindSubagentStarted:
		kind, prefix := store.StepTool, "tool"
		if ev.Kind == harness.KindSubagentStarted {
			kind, prefix = store.StepSubagent, "subagent"
		}
		tool := ev.Tool
		if tool == nil {
			tool = &harness.Tool{}
		}
		p.put(&store.Step{
			ID: p.stepID(prefix, ev.ID), Kind: kind, Title: tool.Title, Body: tool.Output,
			Detail: detail(map[string]any{
				"name": tool.Name, "input": tool.Input, "input_json": tool.InputJSON,
			}),
			Status: store.StatusRunning,
		})

	case harness.KindToolOutput:
		if ev.Tool == nil {
			return
		}
		p.appendToolOutput(ev)

	case harness.KindToolFinished, harness.KindSubagentFinished:
		kind, prefix := store.StepTool, "tool"
		if ev.Kind == harness.KindSubagentFinished {
			kind, prefix = store.StepSubagent, "subagent"
		}
		tool := ev.Tool
		if tool == nil {
			tool = &harness.Tool{}
		}
		status := store.StatusDone
		if !tool.OK {
			status = store.StatusFailed
		}
		id := p.stepID(prefix, ev.ID)
		body := tool.Output
		title := tool.Title
		name, input, inputJSON := tool.Name, tool.Input, tool.InputJSON
		// A finish event is a patch, not a replacement. Not every adapter
		// repeats what it already said when a tool call ends, and a card whose
		// title, command or name disappears the moment it completes is worse
		// than one that never showed them.
		if existing, err := p.engine.Store.GetStep(id); err == nil {
			was := decodeDetail(existing.Detail)
			if title == "" {
				title = existing.Title
			}
			if name == "" {
				name = was["name"]
			}
			if input == "" {
				input = was["input"]
			}
			if inputJSON == "" {
				inputJSON = was["input_json"]
			}
			if body == "" {
				body = existing.Body
			}
		}
		p.put(&store.Step{
			ID: id, Kind: kind, Title: title, Body: harness.TruncateOutput(body),
			Detail: detail(map[string]any{
				"name": name, "input": input, "input_json": inputJSON,
				"exit_code": tool.ExitCode, "ok": tool.OK,
			}),
			Status: status,
		})

	case harness.KindUsage:
		if ev.Usage == nil {
			return
		}
		p.put(&store.Step{
			ID: p.run.ID + ":usage", Kind: store.StepUsage, Title: "Usage",
			Detail: detail(ev.Usage), Status: store.StatusDone,
		})

	case harness.KindNotice:
		p.put(&store.Step{
			ID: p.run.ID + ":notice:" + strconv.FormatInt(ev.Seq, 10), Kind: store.StepNotice,
			Title: "Note", Body: ev.Error, Status: store.StatusDone,
		})

	case harness.KindSessionID:
		p.noteSession(ev.Session)
	}
}

// appendToolOutput is the one event kind that cannot be made harmless on its
// own: a tool_output carries a delta with no identity, so two deliveries of
// the same delta append the same bytes twice whatever the accumulator is keyed
// by. It relies entirely on the host's no-duplicate guarantee, which is one
// more reason that rule is a guarantee and not an optimisation.
func (p *pump) appendToolOutput(ev harness.Event) {
	id := p.stepID("tool", ev.ID)
	existing, err := p.engine.Store.GetStep(id)
	if err != nil {
		return
	}
	body := harness.TruncateOutput(existing.Body + ev.Tool.Output)
	if body == existing.Body {
		return
	}
	existing.Body = body
	// Throttled: a chatty build would otherwise write a revision per line.
	if last, ok := p.toolAt[id]; ok && time.Since(last) < draftFlush {
		return
	}
	p.toolAt[id] = time.Now()
	p.put(existing)
}

func decodeDetail(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var any map[string]any
	if err := json.Unmarshal(raw, &any); err != nil {
		return out
	}
	for k, v := range any {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// draftBody is the answer as it stands: the completed blocks in order, plus
// whatever the block that is still streaming has so far.
func (p *pump) draftBody() string {
	parts := make([]string, 0, len(p.order)+1)
	for _, id := range p.order {
		if text := p.completed[id]; text != "" {
			parts = append(parts, text)
		}
	}
	if p.currentID != "" {
		if text := p.partial[p.currentID]; text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// patchDraft writes the live answer, at most once per draftFlush unless the
// caller says this one matters.
func (p *pump) patchDraft(force bool) {
	if !force && time.Since(p.draftAt) < draftFlush {
		return
	}
	body := p.draftBody()
	if body == "" {
		return
	}
	p.draftAt = time.Now()
	p.draftLive = true
	p.put(&store.Step{
		ID: p.run.ID + ":draft", Kind: store.StepDraft, Body: body, Status: store.StatusRunning,
	})
}

// answer is what the assistant message will say: every completed block, plus a
// partial one left over, so an interrupted turn still shows what had been
// written.
func (p *pump) answer() string {
	parts := make([]string, 0, len(p.order)+len(p.partial))
	for _, id := range p.order {
		if text := p.completed[id]; text != "" {
			parts = append(parts, text)
		}
	}
	if p.currentID != "" {
		if text := p.partial[p.currentID]; text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// finish ends the turn. It re-reads the run row first and returns at once if
// it is no longer running, so a double finish - a replay, a race between two
// end triggers - is a no-op rather than a second answer.
func (p *pump) finish(outcome, errText string) {
	for _, fn := range p.cleanup {
		fn()
	}
	p.cleanup = nil
	if p.done {
		return
	}
	p.done = true
	current, err := p.engine.Store.GetRun(p.run.ID)
	if err == nil && current.Status != store.RunRunning {
		return
	}

	if answer := p.answer(); answer != "" {
		msg := &store.Message{
			// Deterministic, so a replayed turn patches the answer it already
			// wrote instead of saying it twice.
			ID: p.run.ID + ":assistant", ChatID: p.chat.ID, RunID: p.run.ID,
			Role: "assistant", Content: answer,
		}
		if err := p.engine.commitMessage(msg); err != nil {
			log.Printf("engine: commit answer: %v", err)
		}
	}
	// A turn that did not end well leaves cards behind: an agent that is
	// interrupted mid-command sends no completion for the item it was running,
	// and Codex in particular sends nothing at all for anything in flight
	// after turn/interrupt. Without this the card spins for good.
	if outcome != harness.OutcomeOK {
		p.closeOpenSteps(outcome)
	}
	if p.draftLive {
		if err := p.engine.commitStepRemoval(p.chat.ID, p.run.ID+":draft"); err != nil {
			log.Printf("engine: remove draft: %v", err)
		}
		p.draftLive = false
	}

	status := store.RunDone
	switch outcome {
	case harness.OutcomeError:
		status = store.RunFailed
	case harness.OutcomeInterrupted:
		status = store.RunInterrupted
	}
	if status != store.RunFailed {
		errText = strings.TrimSpace(errText)
	}
	if err := p.engine.Store.SetRunStatus(p.run.ID, status, errText); err != nil {
		log.Printf("engine: set run status: %v", err)
	}
	p.run.Status, p.run.Error = status, errText
	// Let go of the chat first, then say the turn is over. The other order is
	// a race the user loses: they see the answer, they type, and the send is
	// refused for a turn that has already finished.
	if p.release != nil {
		p.release()
	}
	p.engine.publish(p.chat.ID, Event{Type: "run", Run: p.run})
}

// finished reports whether this turn has already been ended.
func (p *pump) finished() bool { return p.done }

// closeOpenSteps settles every tool or subagent card of this run that is still
// running, with the outcome of the turn they belonged to. The store is asked
// rather than an in-memory set, because a turn adopted after a restart has
// cards this pump did not open.
func (p *pump) closeOpenSteps(outcome string) {
	status := store.StatusInterrupted
	if outcome == harness.OutcomeError {
		status = store.StatusFailed
	}
	steps, err := p.engine.Store.ListSteps(p.chat.ID)
	if err != nil {
		log.Printf("engine: could not settle the open steps of %s: %v", p.run.ID, err)
		return
	}
	for i := range steps {
		st := steps[i]
		if st.RunID != p.run.ID || st.Status != store.StatusRunning {
			continue
		}
		if st.Kind != store.StepTool && st.Kind != store.StepSubagent {
			continue
		}
		st.Status = status
		if err := p.engine.commitStep(&st); err != nil {
			log.Printf("engine: settle step %s: %v", st.ID, err)
		}
	}
}

// replayFailed ends a turn whose event stream could not be read to the end.
// It is not a failure of the agent - the turn may well have finished inside
// the host - so the run is interrupted rather than failed, and the sentence
// says which of the two things happened.
func (p *pump) replayFailed(err error) {
	body := "the agent host could not be reached"
	if err != nil && strings.Contains(err.Error(), agenthost.ErrReplayWindow.Error()) {
		body = "the transcript of this turn was too long to replay after a restart"
	}
	if p.done {
		return
	}
	p.put(&store.Step{
		ID: p.run.ID + ":error", Kind: store.StepError,
		Title: "This turn could not be followed to the end", Body: body, Status: store.StatusFailed,
	})
	p.finish(harness.OutcomeInterrupted, body)
}

// fatal writes an error step and ends the turn with it. It is what the user
// sees when the agent could not be started at all, or died before answering.
func (p *pump) fatal(errText string) {
	if p.done {
		return
	}
	if strings.TrimSpace(errText) == "" {
		errText = "the agent stopped without saying why"
	}
	p.put(&store.Step{
		ID: p.run.ID + ":error", Kind: store.StepError,
		Title: "The agent could not answer", Body: errText, Status: store.StatusFailed,
	})
	p.finish(harness.OutcomeError, errText)
}
