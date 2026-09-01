package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// readLoop is the whole mapping table (DESIGN §2.5.1): one stdout line in,
// zero or more normalised events out. It is the only goroutine that reads the
// CLI, so the event order it produces is the CLI's own order.
func (a *adapter) readLoop(stdout io.Reader) {
	sc := scan(stdout)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		a.spokeUp()
		a.handle(line)
	}
}

// spokeUp releases Start from launchGrace the first time the CLI writes.
func (a *adapter) spokeUp() {
	a.mu.Lock()
	ch := a.spoke
	a.mu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (a *adapter) handle(line []byte) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		a.badLine()
		return
	}
	parent := ""
	if env.Parent != nil {
		parent = *env.Parent
	}

	switch env.Type {
	case "system":
		a.system(env)
	case "stream_event":
		a.streamEvent(env, parent)
	case "assistant":
		a.assistant(env, parent)
	case "user":
		// The --replay-user-messages echo is the harness's own text coming
		// back. It is used only as an ack and produces no event.
		if env.IsReplay {
			return
		}
		a.userLine(env, parent)
	case "result":
		a.result(env)
	case "control_response":
		a.controlResponse(env)
	case "rate_limit_event":
		// Dropped: it says nothing a transcript needs.
	default:
		a.badLine()
	}
}

// system handles the one system subtype that matters. status fires per API
// request rather than per turn, and thinking_tokens is an estimate the real
// usage supersedes; both are dropped.
//
// init does not arrive at process start under --input-format stream-json: it
// arrives with the first turn (see launchGrace). By then Start has already
// emitted the session id Socrates owns, so this only confirms it - and emits
// a correction on the vanishingly unlikely day the CLI disagrees.
func (a *adapter) system(env envelope) {
	if env.Subtype != "init" {
		return
	}
	a.mu.Lock()
	a.session, a.resolvedModel = env.SessionID, env.Model
	a.ready = true
	owned, want, turnID := a.sessionUUID, a.spec.Model, a.turnID
	a.mu.Unlock()

	if env.SessionID != "" && env.SessionID != owned {
		a.out.emit(harness.Event{Kind: harness.KindSessionID, Session: env.SessionID})
	}
	// An alias resolves to a canonical dated id, so only a real mismatch is
	// worth a word to the user.
	if env.Model != "" && want != "" && !modelMatches(want, env.Model) {
		a.notice(turnID, "the agent is running on "+env.Model+", not the requested "+want)
	}
}

// ------------------------------------------------------------ stream events

func (a *adapter) streamEvent(env envelope, parent string) {
	var f streamFrame
	if err := json.Unmarshal(env.Event, &f); err != nil {
		a.badLine()
		return
	}
	switch f.Type {
	case "message_start":
		if f.Message == nil || f.Message.ID == "" {
			return
		}
		a.mu.Lock()
		a.streamMsg[parent] = f.Message.ID
		a.streamNext[f.Message.ID] = 0
		a.streamBlock[f.Message.ID] = map[int]int{}
		a.mu.Unlock()

	case "content_block_start":
		// S-1: blocks are numbered per message.id by arrival order, not by
		// the wire index, so that the number a streaming block gets is the
		// same number the assistant line for it will get. The wire index is
		// only kept as the key the deltas arrive under.
		a.mu.Lock()
		msg := a.streamMsg[parent]
		if msg != "" {
			idx := a.streamNext[msg]
			a.streamNext[msg] = idx + 1
			if a.streamBlock[msg] == nil {
				a.streamBlock[msg] = map[int]int{}
			}
			a.streamBlock[msg][f.Index] = idx
		}
		a.mu.Unlock()

	case "content_block_delta":
		// thinking_delta, signature_delta and input_json_delta are dropped:
		// the complete block arrives on the assistant line.
		if f.Delta == nil || f.Delta.Type != "text_delta" || f.Delta.Text == "" {
			return
		}
		a.mu.Lock()
		msg := a.streamMsg[parent]
		idx, ok := -1, false
		if msg != "" {
			idx, ok = a.streamBlock[msg][f.Index]
		}
		turnID := a.turnID
		a.mu.Unlock()
		if !ok || turnID == "" {
			return
		}
		a.textDelta(turnID, blockID(msg, idx), parent, f.Delta.Text)

	default:
		// content_block_stop, message_delta, message_stop and anything else
		// carry nothing the normalised stream needs.
	}
}

// blockID composes the id of one text or reasoning block: <message.id>:<n>. A
// bare index repeats in every API message of a tool-using turn and would
// merge two distinct blocks in the pump (F-12/S-1).
func blockID(msgID string, idx int) string {
	return msgID + ":" + strconv.Itoa(idx)
}

// ----------------------------------------------------------------- deltas

// deltaBuf coalesces the text of one block: one event per TextDeltaFlush
// rather than one per token.
type deltaBuf struct {
	pending string
	last    time.Time
}

// textDelta buffers an increment and emits at most one event per
// TextDeltaFlush per block. Whatever is still buffered when the block
// completes is not emitted: the `text` event carries the block in full and
// the pump replaces the partial with it.
func (a *adapter) textDelta(turnID, id, parent, text string) {
	a.mu.Lock()
	b := a.deltas[id]
	if b == nil {
		b = &deltaBuf{}
		a.deltas[id] = b
	}
	b.pending += text
	now := time.Now()
	if !b.last.IsZero() && now.Sub(b.last) < harness.TextDeltaFlush {
		a.mu.Unlock()
		return
	}
	out := b.pending
	b.pending, b.last = "", now
	a.mu.Unlock()

	a.out.emit(harness.Event{
		Kind: harness.KindTextDelta, TurnID: turnID, ID: id, Parent: parent, Text: out,
	})
}

// dropDeltas forgets every block's buffer at the end of a turn. The complete
// text of each block has already been emitted as `text`.
func (a *adapter) dropDeltas() {
	a.mu.Lock()
	a.deltas = map[string]*deltaBuf{}
	a.mu.Unlock()
}

// --------------------------------------------------------- assistant lines

// assistant maps one completed content block. The line carries no index
// field; the index comes from arrival order per message.id (S-1).
func (a *adapter) assistant(env envelope, parent string) {
	var msg assistantMessage
	if err := json.Unmarshal(env.Message, &msg); err != nil {
		a.badLine()
		return
	}
	a.mu.Lock()
	turnID := a.turnID
	a.mu.Unlock()
	if turnID == "" {
		return
	}

	for _, block := range msg.Content {
		a.mu.Lock()
		idx := a.assistantN[msg.ID]
		a.assistantN[msg.ID] = idx + 1
		a.mu.Unlock()
		id := blockID(msg.ID, idx)

		switch block.Type {
		case "text":
			a.out.emit(harness.Event{
				Kind: harness.KindText, TurnID: turnID, ID: id, Parent: parent, Text: block.Text,
			})
		case "thinking":
			// haiku frequently reports an empty thinking block even when it
			// spent thinking tokens; an empty reasoning card is noise.
			if block.Thinking == "" {
				continue
			}
			a.out.emit(harness.Event{
				Kind: harness.KindReasoning, TurnID: turnID, ID: id, Parent: parent, Text: block.Thinking,
			})
		case "tool_use":
			a.toolUse(turnID, parent, block)
		}
	}
}

func (a *adapter) toolUse(turnID, parent string, block contentBlock) {
	title, summary := describe(block.Name, block.Input)
	tool := &harness.Tool{
		Name:      block.Name,
		Title:     title,
		Input:     summary,
		InputJSON: inputJSON(block.Input),
	}
	kind := harness.KindToolStarted
	isTask := block.Name == "Task"
	if isTask {
		kind = harness.KindSubagentStarted
	}
	// The finish event has to carry the name and title again, or the engine's
	// patch blanks the card's header (SHOULD-FIX-4).
	a.mu.Lock()
	a.tools[block.ID] = &harness.Tool{Name: block.Name, Title: title}
	if isTask {
		a.sawTask = true
	}
	a.mu.Unlock()
	a.out.emit(harness.Event{
		Kind: kind, TurnID: turnID, ID: block.ID, Parent: parent, Tool: tool,
	})
}

// -------------------------------------------------------------- user lines

// userLine carries the tool results. A user line without a tool_result block
// - the "[Request interrupted by user for tool use]" note, for instance - has
// no normalised meaning and is dropped.
func (a *adapter) userLine(env envelope, parent string) {
	var msg userMessage
	if err := json.Unmarshal(env.Message, &msg); err != nil {
		a.badLine()
		return
	}
	a.mu.Lock()
	turnID := a.turnID
	a.mu.Unlock()
	if turnID == "" {
		return
	}

	for _, block := range msg.Content {
		if block.Type != "tool_result" {
			continue
		}
		output := renderContent(block.Content)
		if output == "" {
			output = toolUseResultText(env.ToolUseResult)
		}
		a.mu.Lock()
		was := a.tools[block.ToolUseID]
		a.mu.Unlock()

		name, title := "", ""
		if was != nil {
			name, title = was.Name, was.Title
		}
		kind := harness.KindToolFinished
		if name == "Task" {
			kind, title = harness.KindSubagentFinished, "Subagent finished"
		}
		a.out.emit(harness.Event{
			Kind: kind, TurnID: turnID, ID: block.ToolUseID, Parent: parent,
			Tool: &harness.Tool{
				// SHOULD-FIX-4: §2.3's wire example for tool_finished carries
				// name and title, and the engine patches the card with what
				// this event says - an empty title would blank the header the
				// start event filled in.
				Name:   name,
				Title:  title,
				Output: harness.TruncateOutput(output),
				OK:     !block.IsError,
				// F-2: ExitCode stays 0. tool_use_result carries stdout,
				// stderr, interrupted, isImage and noOutputExpected and no
				// exit code, and inventing one would put a wrong "exit 1" on
				// a card.
			},
		})
	}
}

// ------------------------------------------------------------------ result

// result is the turn's end (research §2: after it, nothing arrives until the
// next stdin line).
func (a *adapter) result(env envelope) {
	a.mu.Lock()
	turnID, interrupted, sawTask := a.turnID, a.interruptSent, a.sawTask
	// BLOCKER-1 / R-3: the signature of a --resume the CLI could not read is
	// an error result that ended no turn (num_turns 0) on a resumed process
	// that never saw init. It arrives unprompted 1.3-3.9 s after launch, so
	// it is recorded here whether or not a turn is open, and eof() - which
	// alone knows the process is really gone - arms the one relaunch.
	if a.resumed && !a.ready && env.IsError && env.NumTurns == 0 {
		a.resumeFailed = true
		a.mu.Unlock()
		return
	}
	if turnID != "" {
		a.pendingUsage = usageOf(env)
	}
	a.mu.Unlock()
	if turnID == "" {
		return
	}

	if env.SubagentStats != nil && env.SubagentStats.Spawned > 0 && !sawTask {
		// The background-task case: subagents ran but no Task tool_use was
		// ever seen, so the transcript would be silently incomplete.
		n := strconv.Itoa(env.SubagentStats.Spawned)
		word := " subagents"
		if env.SubagentStats.Spawned == 1 {
			word = " subagent"
		}
		a.notice(turnID, "the agent ran "+n+word+" in the background; their work is not shown here")
	}

	outcome, errText := harness.OutcomeOK, ""
	switch {
	case interrupted:
		// Checked first: the aborted_tools shape was only observed with a
		// Bash tool actually executing, and an interrupt during text
		// streaming must not be reported as a failure of the user's own
		// cancel (F-3).
		outcome = harness.OutcomeInterrupted
	case env.Subtype == "error_during_execution" && env.TerminalReason == "aborted_tools":
		// The secondary signal, for a turn whose interrupt flag was lost with
		// a restarted adapter.
		outcome = harness.OutcomeInterrupted
	case env.IsError:
		// result.subtype is not a success discriminator: "success" can carry
		// is_error:true (the bad-model shape). is_error is.
		outcome = harness.OutcomeError
		errText = firstNonEmpty(env.Result, env.TerminalReason, env.Subtype, "the turn failed")
	}
	a.closeTurn(outcome, errText)
}

func usageOf(env envelope) *harness.Usage {
	u := &harness.Usage{CostUSD: env.TotalCostUSD}
	if env.Usage != nil {
		u.Input = env.Usage.InputTokens
		u.Output = env.Usage.OutputTokens
		u.Cached = env.Usage.CacheReadInputTokens
		u.Reasoning = env.Usage.OutputTokensDetails.ThinkingTokens
		u.Total = u.Input + u.Output
	}
	// modelUsage keys by the resolved model id and is the only place the real
	// context window is reported. A turn touches one model in practice; the
	// largest window is taken so a mixed turn does not understate it.
	for _, m := range env.ModelUsage {
		if m.ContextWindow > u.Context {
			u.Context = m.ContextWindow
		}
	}
	if u.Input == 0 && u.Output == 0 && u.CostUSD == 0 && u.Context == 0 {
		return nil
	}
	return u
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --------------------------------------------------------- control replies

func (a *adapter) controlResponse(env envelope) {
	if env.Response == nil {
		return
	}
	a.mu.Lock()
	ch := a.controls[env.Response.RequestID]
	delete(a.controls, env.Response.RequestID)
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// badLine counts a line that is not valid JSON or whose type this mapping
// does not know. One notice is emitted once the count reaches the per-turn
// cap, so a protocol change is visible without flooding a transcript.
func (a *adapter) badLine() {
	a.mu.Lock()
	a.badLines++
	n, turnID := a.badLines, a.turnID
	a.mu.Unlock()
	if n == harness.MaxNoticesPerTurn {
		a.notice(turnID, "the agent sent "+strconv.Itoa(n)+" lines this adapter does not understand")
	}
}
