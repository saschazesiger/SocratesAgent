package server

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The automatic retry: the chats' clock's third thing (schedule.go).
//
// A chat whose last turn failed - the model's API refused or did not answer
// (a Cloudflare challenge, a 5xx, an overloaded service, a connection that
// broke), or the agent's process died in the middle - and that nobody has
// done anything with since, is told to go on (agenthost.ContinueText) after
// a while, as after a limit. Each time it fails again, it waits twice as
// long: retryBase, doubled, at most retryCap, up to retryMax times in a row.
// Then it stops, and the person is told, as for any failed turn.
//
// A failure that will fail the same way however often it is tried - a
// prompt too long for the model, a key the provider does not know, a request
// it calls invalid - is not tried again (retryableFailure).
//
// It goes with the chat's auto_continue option: off, the chat waits for the
// person after a failure as after a limit. A retry the clock will make keeps
// the failure from being news: no push, no verdict, until the retries are
// used up (Server.willRetry).
//
// The count lives in the journal, not in memory: a retry's message has the
// client_id "auto-retry-<n>-<turn>", n being which retry it is, so the chain
// survives a restart and a turn the person started begins a new one.

const (
	// retryBase is the wait before the first retry, and retryCap the
	// longest one.
	retryBase = 30 * time.Second
	retryCap  = 30 * time.Minute
	// retryMax is how many retries in a row a chain gets: 30 s, 1, 2, 4, 8,
	// 16 min and then every 30 min, some three and a half hours in all.
	retryMax = 12
)

// retryPrefix starts the client_id of a retry's message.
const retryPrefix = "auto-retry-"

// permanentFailures are the words of a failure that the next try would meet
// again (lower case): the request itself is wrong, or the account cannot be
// used at all, which the person has to change.
var permanentFailures = []string{
	"prompt is too long",
	"context length",
	"context_length_exceeded",
	"maximum context",
	"credit balance",
	"insufficient_quota",
	"invalid api key",
	"invalid x-api-key",
	"invalid_api_key",
	"invalid_request_error",
	"not_found_error",
	"permission_error",
	"api error: 400",
	"api error: 404",
	"api error: 413",
}

// retryableFailure is whether a failed turn with this error is worth trying
// again by itself: anything but a permanentFailures.
func retryableFailure(text string) bool {
	low := strings.ToLower(text)
	for _, words := range permanentFailures {
		if strings.Contains(low, words) {
			return false
		}
	}
	return true
}

// retryDelay is the wait before retry n+1 (n retries made already), with up
// to a tenth more by seed, so that chats that failed together - one outage
// for all - do not all try again in the same second.
func retryDelay(n int, seed string) time.Duration {
	d := retryBase
	for i := 0; i < n && d < retryCap; i++ {
		d *= 2
	}
	d = min(d, retryCap)
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return d + d/10*time.Duration(h.Sum32()%1000)/1000
}

// chatFailure is the failed turn a chat stands at, which the clock may try
// again: the turn, when it ended, and how many retries in a row came before
// it.
type chatFailure struct {
	Turn    string `json:"turn"`
	EndedAt int64  `json:"ended_at"`
	Retries int    `json:"retries,omitempty"`
}

// failureOf is the failure a chat stands at: its last turn failed with an
// error worth trying again, nothing runs, and nobody has written to it
// since. Retries is the n of the retry that started the failed turn, or zero
// for a turn something else started.
func failureOf(p *agent.Projection) *chatFailure {
	t := p.LastTurn
	if p.Turn != nil || t == nil || t.Outcome != agent.OutcomeFailed || !retryableFailure(t.Error) {
		return nil
	}
	var started *agent.Message
	for _, m := range p.Messages {
		if m.Role != agent.RoleUser {
			continue
		}
		if m.ID != t.MessageID && m.At >= t.EndedAt {
			return nil
		}
		if m.ID == t.MessageID || (t.MessageID == "" && m.State != agent.MessageWithdrawn && m.State != agent.MessageCancelled) {
			started = m
		}
	}
	f := &chatFailure{Turn: t.ID, EndedAt: t.EndedAt}
	if started != nil {
		f.Retries = retryNumber(started.ClientID)
	}
	return f
}

// retryClientID is the client_id of retry n after the failed turn.
func retryClientID(n int, turn string) string {
	return fmt.Sprintf("%s%d-%s", retryPrefix, n, turn)
}

// retryNumber is the n of a retry's client_id, and zero for any other.
func retryNumber(clientID string) int {
	rest, ok := strings.CutPrefix(clientID, retryPrefix)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.SplitN(rest, "-", 2)[0])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// retryAt is when the clock tries a chat's failed turn again, and the
// failure; zero when it does not: no failure, the option off, the chat
// filed done, the retries used up, or this run already tried that turn.
func (sc *scheduler) retryAt(row *store.Session) (int64, *chatFailure) {
	if !autoContinueOn(row.Options) || row.Status == store.StatusDone {
		return 0, nil
	}
	brief, ok := sc.srv.chats.briefOfChat(row.ID)
	if !ok || brief.Failure == nil || brief.Failure.Retries >= retryMax {
		return 0, nil
	}
	f := brief.Failure
	sc.mu.Lock()
	done := sc.continued[retryKey(row.ID, f)]
	sc.mu.Unlock()
	if done {
		return 0, nil
	}
	return f.EndedAt + retryDelay(f.Retries, row.ID+f.Turn).Milliseconds(), f
}

func retryKey(id string, f *chatFailure) string {
	turn := f.Turn
	if turn == "" {
		turn = fmt.Sprint(f.EndedAt)
	}
	return "retry-" + id + "-" + turn
}

// retryOne tells a chat to go on after its failed turn. It reports whether
// it did.
func (sc *scheduler) retryOne(ctx context.Context, row *store.Session, f *chatFailure) bool {
	srv := sc.srv
	if pending, err := srv.store.Outbox(row.ID); err != nil || len(pending) > 0 {
		return false
	}
	sc.mu.Lock()
	sc.continued[retryKey(row.ID, f)] = true
	sc.mu.Unlock()
	p, err := srv.chats.peer(row.ID)
	if err != nil {
		log.Printf("chat %s: could not try again after the failed turn: %v", row.ID, err)
		return false
	}
	n := f.Retries + 1
	turn := f.Turn
	if turn == "" {
		turn = fmt.Sprint(f.EndedAt)
	}
	one, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	code, answer := srv.sendMessage(one, p, &chatMessageBody{
		ClientID: retryClientID(n, turn), Text: agenthost.ContinueText,
		Notice: agent.NoticeAutoRetried, Reason: retriedWords(n),
	})
	if code >= 300 && code != http.StatusAccepted {
		log.Printf("chat %s: could not try again after the failed turn: %v", row.ID, answer["error"])
		return false
	}
	log.Printf("chat %s: the turn failed; told it to go on (retry %d of %d)", row.ID, n, retryMax)
	return true
}

// retriedWords is the notice before retry n.
func retriedWords(n int) string {
	return fmt.Sprintf("Tried again after the turn failed (retry %d of %d).", n, retryMax)
}

// willRetry is whether the clock is going to try a chat's failed turn
// again: then the failure is no news yet.
func (s *Server) willRetry(id string) bool {
	if s.sched == nil || s.chats == nil {
		return false
	}
	row, err := s.store.GetSession(id)
	if err != nil || !row.IsChat() {
		return false
	}
	at, _ := s.sched.retryAt(row)
	return at > 0
}
