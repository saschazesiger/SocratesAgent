//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The invalid_encrypted_content fallback chain (REBUILD.md §4.3). A thread
// carries the model's encrypted reasoning, which only the account that made
// it can read back; under another account the first model call of a turn is
// refused. The chain, at most once per step and app-server:
//
//  1. the history without its reasoning and compaction items: a copy of the
//     rollout under a new thread id, resumed; the original is not touched;
//  2. a new thread whose instructions carry the visible transcript (a handoff);
//
// and the turn's messages are sent again, continuing the same journal turn.
// Each step says what it did in a notice.

// Notice codes of the chain.
const (
	NoticeHistoryStripped = "codex_reasoning_dropped"
	NoticeHandoff         = "codex_handoff"
)

// handoffLimit is how much transcript a handoff carries, from the end.
const handoffLimit = 24000

// beginRecoveryLocked takes over a turn the model refused for its encrypted
// history. The journal turn stays open; the next root turn continues it.
func (a *adapter) beginRecoveryLocked(t *turn, terr *turnError) {
	if t.grace != nil {
		t.grace.Stop()
	}
	if a.active == t {
		a.active = nil
	}
	a.retry = t
	a.recovered++
	step := a.recovered
	go a.recover(t, step, terr)
}

func (a *adapter) recover(t *turn, step int, terr *turnError) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := errors.New("no step left")
	if step == 1 {
		err = a.stripped(ctx)
		if err != nil {
			a.mu.Lock()
			a.recovered = 2
			a.mu.Unlock()
			step = 2
		}
	}
	if step == 2 {
		err = a.handoff(ctx)
	}
	if err == nil {
		err = a.resend(ctx, t)
	}
	if err != nil {
		a.mu.Lock()
		a.retry = nil
		a.closeTurnLocked(t, agent.OutcomeFailed, terr.text()+" ("+err.Error()+")")
		a.mu.Unlock()
	}
}

// stripped is step 1.
func (a *adapter) stripped(ctx context.Context) error {
	a.mu.Lock()
	thread, path := a.thread, a.path
	a.mu.Unlock()
	if path == "" {
		return errors.New("the thread has no rollout file")
	}
	id := newThreadID()
	if _, _, err := StripRollout(path, thread, id); err != nil {
		return err
	}
	if err := a.resume(ctx, id); err != nil {
		return err
	}
	a.out.Emit(
		agent.Event{Type: agent.TypeConversation, ConversationID: id},
		agent.Event{Type: agent.TypeNotice, Code: NoticeHistoryStripped,
			Text: "This account cannot read the reasoning of the earlier turns, so Codex continues without it. " +
				"The messages and tool results are all still there."})
	return nil
}

// handoff is step 2.
func (a *adapter) handoff(ctx context.Context) error {
	a.mu.Lock()
	thread := a.thread
	a.mu.Unlock()
	transcript, err := a.transcript(ctx, thread)
	if err != nil {
		transcript = ""
	}
	instructions := "This conversation continues an earlier Codex conversation that could not be resumed " +
		"under the account now in use. Carry on from where it stopped."
	if transcript != "" {
		instructions += " This is what was said so far, oldest first:\n\n" + transcript
	}
	if err := a.startThread(ctx, instructions); err != nil {
		return err
	}
	a.mu.Lock()
	id := a.thread
	a.mu.Unlock()
	a.out.Emit(
		agent.Event{Type: agent.TypeConversation, ConversationID: id},
		agent.Event{Type: agent.TypeNotice, Code: NoticeHandoff,
			Text: "This account cannot continue the earlier Codex conversation, so a new one was started with " +
				"a summary of it. Codex does not see the earlier tool results in full."})
	return nil
}

// resend gives a recovered turn its messages again.
func (a *adapter) resend(ctx context.Context, t *turn) error {
	a.mu.Lock()
	input := t.input
	t.input = nil
	a.mu.Unlock()
	for i, msg := range input {
		var err error
		if i == 0 {
			err = a.startTurn(ctx, msg)
		} else {
			err = a.send(ctx, msg)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// transcript is the visible conversation of a thread as plain text: what the
// person said and what the agent answered, cut from the front to fit.
func (a *adapter) transcript(ctx context.Context, thread string) (string, error) {
	turns, err := a.turnsOf(ctx, thread)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, ht := range turns {
		items, err := a.itemsOf(ctx, thread, ht.ID)
		if err != nil {
			return "", err
		}
		for _, raw := range items {
			var it item
			if json.Unmarshal(raw, &it) != nil {
				continue
			}
			switch it.Type {
			case "userMessage":
				if text := userEvent(it, "").Text; text != "" {
					fmt.Fprintf(&b, "User: %s\n\n", text)
				}
			case "agentMessage":
				if it.Text != "" {
					fmt.Fprintf(&b, "Assistant: %s\n\n", it.Text)
				}
			case "commandExecution":
				fmt.Fprintf(&b, "(ran: %s)\n\n", it.Command)
			case "fileChange":
				for _, c := range it.Changes {
					fmt.Fprintf(&b, "(changed %s)\n\n", c.Path)
				}
			}
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > handoffLimit {
		s = "…" + s[len(s)-handoffLimit:]
	}
	return s, nil
}
