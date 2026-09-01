package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// The SSE event types this adapter reacts to. Everything else on the stream -
// server.connected, the tool.input.* trio, step.started, prompt.admitted - is
// dropped on purpose; the rows in §2.5.3 that say "dropped" are decisions, not
// omissions.
const (
	evPromptAdmitted  = "session.next.prompt.admitted"
	evPrompted        = "session.next.prompted"
	evStepEnded       = "session.next.step.ended"
	evTextDelta       = "session.next.text.delta"
	evTextEnded       = "session.next.text.ended"
	evReasoningEnded  = "session.next.reasoning.ended"
	evToolCalled      = "session.next.tool.called"
	evToolSuccess     = "session.next.tool.success"
	evToolFailed      = "session.next.tool.failed"
	evPermissionAsked = "permission.v2.asked"
	evSessionError    = "session.error"
)

// frame is one `data:` line of the stream. Events carrying a durable key are
// the replayable ones; the deltas have none.
//
// durable.version is deliberately not read anywhere: the server emits 2 on
// step.ended and 1 elsewhere, and no rule in this adapter depends on either.
type frame struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Durable *struct {
		AggregateID string `json:"aggregateID"`
		Seq         int64  `json:"seq"`
		Version     int    `json:"version"`
	} `json:"durable"`
	Data json.RawMessage `json:"data"`

	// global marks a frame that came off the server-wide stream rather than
	// the session's own. Only the ephemeral increments are taken from there.
	global bool
}

// sessionID is the session a frame's payload names. Every session.next.* and
// permission.* payload carries it; it is what keeps another session's frames
// on the server-wide stream out of this chat.
func (f frame) sessionID() string {
	if len(f.Data) == 0 {
		return ""
	}
	var d struct {
		SessionID string `json:"sessionID"`
	}
	_ = json.Unmarshal(f.Data, &d)
	return d.SessionID
}

// The payloads, one struct per family. Fields this adapter does not use are
// left out rather than parsed and ignored.
type (
	textDeltaData struct {
		AssistantMessageID string `json:"assistantMessageID"`
		TextID             string `json:"textID"`
		Delta              string `json:"delta"`
	}
	textEndedData struct {
		AssistantMessageID string `json:"assistantMessageID"`
		TextID             string `json:"textID"`
		Text               string `json:"text"`
	}
	reasoningEndedData struct {
		AssistantMessageID string `json:"assistantMessageID"`
		ReasoningID        string `json:"reasoningID"`
		Text               string `json:"text"`
	}
	toolCalledData struct {
		AssistantMessageID string          `json:"assistantMessageID"`
		CallID             string          `json:"callID"`
		Tool               string          `json:"tool"`
		Input              json.RawMessage `json:"input"`
	}
	toolSuccessData struct {
		CallID     string `json:"callID"`
		Structured struct {
			Exit      *int `json:"exit"`
			Truncated bool `json:"truncated"`
		} `json:"structured"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	// toolFailedData is EventSessionNextToolFailed from oc-openapi.json: error
	// is a SessionErrorUnknown object ({"type":"unknown","message":"…"}), not
	// a bare string, and there is no session.next.tool.error event in this
	// build. result and provider are ignored.
	toolFailedData struct {
		CallID string `json:"callID"`
		Error  struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	stepEndedData struct {
		AssistantMessageID string  `json:"assistantMessageID"`
		Finish             string  `json:"finish"`
		Cost               float64 `json:"cost"`
		Tokens             struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	}
	permissionAskedData struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		Action    string `json:"action"`
	}
	// sessionErrorData is EventSessionError. Every error variant in the schema
	// - ProviderAuthError, UnknownError, APIError and the rest - is
	// {"name":…,"data":{"message":…,…}}, so one shape reads all of them.
	sessionErrorData struct {
		SessionID string `json:"sessionID"`
		Error     struct {
			Name string `json:"name"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
		Raw json.RawMessage `json:"-"`
	}
)

// message is what sessionErrorData is worth to a human: the provider's own
// sentence if there is one, else the error's class name, else the raw JSON.
func (d sessionErrorData) message() string {
	if d.Error.Data.Message != "" {
		return d.Error.Data.Message
	}
	if d.Error.Name != "" {
		return d.Error.Name
	}
	if len(d.Raw) > 0 {
		return string(d.Raw)
	}
	return "the session reported an error"
}

// finish values of step.ended. Only "stop" means the turn is over; every other
// value ("tool-calls" and whatever else a provider invents) means another step
// is coming.
const finishStop = "stop"

// readStream reads one SSE connection and hands every complete data frame to
// out, marking each with which stream it came from. It returns when the
// connection ends, which is the reconnect's signal.
//
// SSE framing is minimal here on purpose: OpenCode emits one `data:` line per
// event plus `: heartbeat` comments, and no multi-line data, id or event
// fields. Comments and blank lines are skipped; a data line that is not JSON
// is dropped rather than guessed at.
func readStream(ctx context.Context, body io.Reader, global bool, out chan<- frame) {
	sc := bufio.NewScanner(body)
	// A tool result can be large, and a frame that does not fit is a frame
	// that is silently lost, so the limit is generous.
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		var f frame
		if err := json.Unmarshal([]byte(payload), &f); err != nil || f.Type == "" {
			continue
		}
		f.global = global
		select {
		case out <- f:
		case <-ctx.Done():
			return
		}
	}
}

// streamBackoff is how long the adapter waits before re-opening a stream that
// dropped. It is short because the session-scoped stream replays everything
// durable on connect, so a reconnect costs nothing but a round trip.
func streamBackoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * 100 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
