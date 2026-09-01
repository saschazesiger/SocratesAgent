// This file is the mapping half of the adapter: one SSE frame in, the
// normalised events of §2.5.3 out, plus the per-turn bookkeeping - block ids,
// coalesced increments, accumulated usage - that the mapping needs to be
// correct across two streams.
package opencode

import (
	"context"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// ------------------------------------------------------------- the mapping

// handle is one SSE frame, on the processor goroutine.
func (a *adapter) handle(f frame) {
	a.waitGate()

	if f.global {
		// The server-wide stream is read for the increments the session's own
		// stream does not carry, and for nothing else: anything durable would
		// arrive twice, and one server also hosts the internal session
		// OpenCode opens to title a chat.
		if f.Durable != nil || f.sessionID() != a.currentSession() {
			return
		}
	}

	a.mu.Lock()
	if !a.open {
		// Nothing outside a turn is ours: on a resumed session the stream
		// replays every previous turn's text and every previous
		// step.ended{finish:"stop"}, and one of those would arm the
		// confirmation poll for a turn that has not started.
		a.mu.Unlock()
		return
	}
	if f.Durable != nil {
		if f.Durable.Seq <= a.lastSeq {
			a.mu.Unlock()
			return
		}
		a.lastSeq = f.Durable.Seq
	}
	turnID := a.turnID
	a.mu.Unlock()

	switch f.Type {
	case evPrompted:
		a.startTurn()

	case evTextDelta:
		var d textDeltaData
		if !decode(f.Data, &d) {
			return
		}
		a.addDelta(blockID(d.AssistantMessageID, d.TextID), d.Delta)

	case evTextEnded:
		var d textEndedData
		if !decode(f.Data, &d) {
			return
		}
		id := blockID(d.AssistantMessageID, d.TextID)
		a.flushDeltas(id)
		// The block is finished: a late increment off the wide stream must
		// not follow its complete text.
		a.closeBlock(id)
		a.markProduced()
		a.emit(harness.Event{Kind: harness.KindText, TurnID: turnID, ID: id, Text: d.Text})

	case evReasoningEnded:
		var d reasoningEndedData
		if !decode(f.Data, &d) {
			return
		}
		id := blockID(d.AssistantMessageID, d.ReasoningID)
		a.closeBlock(id)
		if strings.TrimSpace(d.Text) == "" {
			return
		}
		a.emit(harness.Event{Kind: harness.KindReasoning, TurnID: turnID, ID: id, Text: d.Text})

	case evToolCalled:
		var d toolCalledData
		if !decode(f.Data, &d) {
			return
		}
		tool := harness.Tool{
			Name:      d.Tool,
			Title:     toolTitle(d.Tool, d.Input),
			Input:     toolSummary(d.Tool, d.Input),
			InputJSON: compact(d.Input),
		}
		a.mu.Lock()
		a.tools[d.CallID] = tool
		a.mu.Unlock()
		a.markProduced()
		a.emit(harness.Event{Kind: harness.KindToolStarted, TurnID: turnID, ID: d.CallID, Tool: &tool})

	case evToolSuccess:
		var d toolSuccessData
		if !decode(f.Data, &d) {
			return
		}
		var parts []string
		for _, c := range d.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		tool := a.finishedTool(d.CallID)
		tool.Output = harness.TruncateOutput(strings.Join(parts, "\n"))
		tool.OK = true
		if d.Structured.Exit != nil {
			tool.ExitCode = *d.Structured.Exit
		}
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: turnID, ID: d.CallID, Tool: &tool})

	case evToolFailed:
		var d toolFailedData
		if !decode(f.Data, &d) {
			return
		}
		tool := a.finishedTool(d.CallID)
		tool.Output = harness.TruncateOutput(d.Error.Message)
		tool.OK = false
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: turnID, ID: d.CallID, Tool: &tool})

	case evStepEnded:
		var d stepEndedData
		if !decode(f.Data, &d) {
			return
		}
		a.addUsage(d)
		a.emitUsage()
		if d.Finish == finishStop {
			// One step's "stop" is not the turn's end: a queued prompt can
			// start another step. The confirmation poll is what decides.
			a.armConfirm()
		}

	case evPermissionAsked:
		var d permissionAskedData
		if !decode(f.Data, &d) {
			return
		}
		a.notice("opencode asked for permission to " + orElse(d.Action, "run a tool") +
			"; Socrates answered for you, because it runs unattended")
		a.answerPermission(turnID, d.ID)

	case evSessionError:
		var d sessionErrorData
		if !decode(f.Data, &d) {
			return
		}
		d.Raw = f.Data
		msg := d.message()
		a.mu.Lock()
		a.lastErr = msg
		a.mu.Unlock()
		a.notice(msg)

	case evPromptAdmitted:
		// The admission is the HTTP response; the event carries nothing new.

	default:
		// server.connected, step.started, the tool.input.* trio, the reasoning
		// and text starts, permission.v2.replied and anything a later release
		// adds: dropped rather than guessed at.
	}
}

// waitGate holds the processor while a Send is being admitted.
func (a *adapter) waitGate() {
	a.mu.Lock()
	gate := a.gate
	a.mu.Unlock()
	if gate == nil {
		return
	}
	select {
	case <-gate:
	case <-a.ctx.Done():
	}
}

// startTurn emits turn_started, once per turn. session.next.prompted is the
// trigger; closeTurn is the safety net for a turn whose prompted never
// arrived, because invariant 2 wants a turn_started before every
// turn_finished.
func (a *adapter) startTurn() {
	a.mu.Lock()
	once, turnID := a.startOnce, a.turnID
	a.mu.Unlock()
	if once == nil {
		return
	}
	once.Do(func() {
		a.emit(harness.Event{Kind: harness.KindTurnStarted, TurnID: turnID})
	})
}

// finishedTool is what a tool.success or tool.failed knows about its call: the
// name and title recorded when it was called, so the finished card does not
// lose the heading the started card had.
func (a *adapter) finishedTool(callID string) harness.Tool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tools[callID]
	if !ok {
		return harness.Tool{Name: "tool", Title: "Ran a tool"}
	}
	delete(a.tools, callID)
	return harness.Tool{Name: t.Name, Title: t.Title}
}

// answerPermission replies to a permission.v2.asked off the processor, so one
// slow HTTP call cannot hold up the event stream. With OPENCODE_PERMISSION
// set this never runs; an ask left unanswered blocks the turn forever, which
// is why it exists at all.
func (a *adapter) answerPermission(turnID, request string) {
	session := a.currentSession()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		if err := a.cli.replyPermission(ctx, session, request, "always"); err != nil {
			a.post(func() { a.noticeFor(turnID, "the permission reply failed: "+err.Error()) })
		}
	}()
}

// ------------------------------------------------------------- text deltas

func blockID(message, block string) string {
	// text-0 restarts in every step, and a tool-using turn has several steps,
	// so the assistant message id is what keeps two different blocks apart.
	if message == "" {
		return block
	}
	return message + ":" + block
}

func (a *adapter) addDelta(id, delta string) {
	if delta == "" {
		return
	}
	a.mu.Lock()
	if _, done := a.closedBlocks[id]; done {
		// The block's complete text is already out. This increment came off
		// the wide stream after the session stream's text.ended overtook it;
		// emitting it now would put a fragment after the finished block, and
		// the engine would append that fragment to the answer.
		a.mu.Unlock()
		return
	}
	if _, seen := a.seen[id]; !seen {
		a.seen[id] = struct{}{}
		a.blocks = append(a.blocks, id)
	}
	a.pending[id] += delta
	last := a.lastFlush[id]
	a.mu.Unlock()
	if time.Since(last) >= harness.TextDeltaFlush {
		a.flushDeltas(id)
	}
}

// flushDeltas emits the buffered increments of one block, or of every block
// when id is empty. Deltas are coalesced to one event per TextDeltaFlush per
// block, so a fast stream does not put one event per token in the journal.
func (a *adapter) flushDeltas(id string) {
	a.mu.Lock()
	turnID := a.turnID
	ids := a.blocks
	if id != "" {
		ids = []string{id}
	}
	type out struct{ id, text string }
	var flush []out
	now := time.Now()
	for _, b := range ids {
		text := a.pending[b]
		if text == "" {
			continue
		}
		delete(a.pending, b)
		a.lastFlush[b] = now
		flush = append(flush, out{b, text})
	}
	a.mu.Unlock()
	for _, f := range flush {
		a.emit(harness.Event{Kind: harness.KindTextDelta, TurnID: turnID, ID: f.id, Text: f.text})
	}
}

// closeBlock marks a block finished: its complete text has gone out, so no
// later increment for it may. Both streams feed one goroutine, so this is the
// whole of the cross-stream ordering guard.
func (a *adapter) closeBlock(id string) {
	a.mu.Lock()
	a.closedBlocks[id] = struct{}{}
	delete(a.pending, id)
	a.mu.Unlock()
}

// markProduced records that this turn has something to show for itself.
func (a *adapter) markProduced() {
	a.mu.Lock()
	a.produced = true
	a.mu.Unlock()
}

// ------------------------------------------------------------------- usage

// addUsage accumulates one step's tokens into the turn's running total.
// Invariant 5: a usage event carries the total so far, not the step's own
// numbers, and OpenCode reports per step.
func (a *adapter) addUsage(d stepEndedData) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage.Input += d.Tokens.Input
	a.usage.Output += d.Tokens.Output
	a.usage.Reasoning += d.Tokens.Reasoning
	a.usage.Cached += d.Tokens.Cache.Read
	a.usage.CostUSD += d.Cost
	a.usageDirty = true
}

func (a *adapter) emitUsage() {
	a.mu.Lock()
	if !a.usageDirty {
		a.mu.Unlock()
		return
	}
	a.usageDirty = false
	u, turnID := a.usage, a.turnID
	a.mu.Unlock()
	a.emit(harness.Event{Kind: harness.KindUsage, TurnID: turnID, Usage: &u})
}
