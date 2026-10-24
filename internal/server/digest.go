package server

// Digests: Home's reading of a session - what it is doing, and what it is
// asking.
//
// Home shows every chat in one line, and a chat that is asking the person
// something as the question itself, with its options as buttons. A chat says
// both in its journal, so its reading is made from that (chatbrief.go) the
// moment the event that changed it is in. The reading the terminals had -
// a model reading an agent's screen for its summary and its question, and an
// operator typing the answer back into it - went with the agent terminals
// (REBUILD.md §7): a Shell and an editor take no turns and ask nothing.
//
// What is left here is the shape of a reading, which the browser draws
// Home, the sidebar and the notifications from, and the digester: the
// verdicts on the chats' turns (outcome.go, chatjudge.go) and the bound on
// the model calls that make them.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// What a call to the home model may cost.
const (
	// digestTimeout bounds one call once it has its place. The wait for a
	// place is not in it: a dozen turns that end in the same minute are a
	// queue, and that is no model that failed to answer.
	digestTimeout = 90 * time.Second
	// digestQueueWait bounds the wait for a place.
	digestQueueWait = 5 * time.Minute
	// digestConcurrency is how many calls may be in flight at once, for every
	// chat together: twenty chats that all finish in the same minute must not
	// open twenty calls.
	digestConcurrency = 3

	// The sanitising limits: what a dialog on a car's screen can show.
	digestSummaryRunes     = 160
	digestTextRunes        = 1000
	digestItemsMax         = 4
	digestOptionsMax       = 12
	digestLabelRunes       = 120
	digestDescriptionRunes = 300
)

// The phases of a digest.
const (
	digestPending = "pending"
	digestReady   = "ready"
	digestError   = "error"
)

// digestNoKey is the whole answer of a server with nobody to ask.
const digestNoKey = "no OpenRouter API key is configured - open /admin and add your key"

// errDigestJSON is a model that would not answer with an object twice.
var errDigestJSON = errors.New("the home model did not answer with usable JSON")

// digest is one session's reading, exactly as the browser receives it.
type digest struct {
	SessionID string `json:"session_id"`
	// Rev moves on every change of this object, so that a page that hears
	// two of them out of order keeps the later one.
	Rev      int64           `json:"rev"`
	Phase    string          `json:"phase"`
	Error    string          `json:"error"`
	State    string          `json:"state"`
	MadeAt   int64           `json:"made_at"`
	Model    string          `json:"model"`
	Summary  string          `json:"summary"`
	Context  string          `json:"context"`
	Question *digestQuestion `json:"question"`
	// News is what a notification about a chat says now (chatBrief.News):
	// its question while it asks, how its last turn ended between turns, and
	// nothing for a turn that is no news.
	News string `json:"news,omitempty"`
}

type digestQuestion struct {
	Source string       `json:"source,omitempty"`
	Kind   string       `json:"kind"`
	Detail string       `json:"detail"`
	Items  []digestItem `json:"items"`
}

type digestItem struct {
	Header   string         `json:"header"`
	Text     string         `json:"text"`
	Multi    bool           `json:"multi"`
	FreeText bool           `json:"free_text"`
	Options  []digestOption `json:"options"`
}

type digestOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
}

// clone is a copy that shares nothing, so that what was published cannot be
// changed under a frame that is still being encoded.
func (d digest) clone() digest {
	if d.Question != nil {
		q := *d.Question
		q.Items = make([]digestItem, len(d.Question.Items))
		for i, item := range d.Question.Items {
			item.Options = append([]digestOption(nil), item.Options...)
			q.Items[i] = item
		}
		d.Question = &q
	}
	return d
}

// digester owns the verdicts on the chats' turns and the calls that make
// them.
type digester struct {
	srv *Server
	sem chan struct{}

	mu sync.Mutex
	// seen is the last committed activity per session, which a verdict that
	// is read again is checked against: the callback carries the new state and
	// not the old.
	seen map[string]termux.Activity
	// gone is every session that was forgotten. A read of the row that
	// happened before the delete must not bring its verdict back afterwards,
	// and ids are never reused, so the set only ever holds the dead.
	gone map[string]bool
	// outcomes is the verdict on every chat's last turn (outcome.go); oseen
	// is the last committed activity outcomeEdge was given, and oknown the
	// last one that was not unknown, which is how a verdict is carried across
	// a tick of unknown back onto the same idle.
	outcomes map[string]*outcome
	oseen    map[string]termux.Activity
	oknown   map[string]termux.Activity
	// epoch is where every verdict's rev starts: the clock in microseconds
	// when this process started, so that a rev from before a restart is never
	// the rev of a different verdict after it. It stays below 2^53, the
	// largest whole number a browser reads exactly.
	epoch int64
}

func newDigester(s *Server) *digester {
	d := &digester{
		srv:      s,
		sem:      make(chan struct{}, digestConcurrency),
		seen:     map[string]termux.Activity{},
		gone:     map[string]bool{},
		outcomes: map[string]*outcome{},
		oseen:    map[string]termux.Activity{},
		oknown:   map[string]termux.Activity{},
		epoch:    time.Now().UnixMicro(),
	}
	d.loadOutcomes()
	return d
}

// observe takes every committed activity change. It returns at once: it is
// called from the detector's tick.
func (d *digester) observe(id string, a termux.Activity) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone[id] {
		return
	}
	d.seen[id] = a
}

// forget drops a deleted session's verdict and tells every stream that it is
// gone.
func (d *digester) forget(id string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gone[id] = true
	delete(d.seen, id)
	delete(d.oseen, id)
	delete(d.oknown, id)
	d.dropOutcomeLocked(id)
}

/* ------------------------------------------------------------ the call */

// slot waits for a free call, bounded by digestQueueWait, and says whether it
// has one.
func (d *digester) slot(ctx context.Context) bool {
	wait := time.NewTimer(digestQueueWait)
	defer wait.Stop()
	select {
	case d.sem <- struct{}{}:
		return true
	case <-ctx.Done():
	case <-wait.C:
	}
	return false
}

func (d *digester) release() { <-d.sem }

// digestAnswer is the model's object before it is sanitised.
type digestAnswer struct {
	Outcome *outcomeAnswer `json:"outcome"`
}

// askDigest asks once and, if the answer is not the object, exactly once more
// with the parser's complaint attached: a model that wrapped its object in
// prose usually gets it right when it is told what was wrong.
func askDigest(ctx context.Context, client *openrouter.Client, model string,
	messages []openrouter.Message, maxTokens int) (digestAnswer, error) {
	temperature := 0.0
	attempt := messages
	for try := 1; try <= 2; try++ {
		res, err := client.Chat(ctx, openrouter.ChatRequest{
			Model:       model,
			Messages:    attempt,
			Temperature: &temperature,
			MaxTokens:   maxTokens,
		}, nil)
		if err != nil {
			return digestAnswer{}, err
		}
		answer, perr := parseDigestAnswer(res.Content)
		if perr == nil {
			return answer, nil
		}
		attempt = append(append([]openrouter.Message{}, attempt...),
			openrouter.Message{Role: "assistant", Content: res.Content},
			openrouter.Message{Role: "user", Content: "That was not usable JSON: " + perr.Error() +
				". Answer with one JSON object and nothing else."})
	}
	return digestAnswer{}, errDigestJSON
}

func parseDigestAnswer(raw string) (digestAnswer, error) {
	text := stripFence(strings.TrimSpace(raw))
	var answer digestAnswer
	if text == "" {
		return answer, errors.New("the answer was empty")
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return answer, err
	}
	return answer, nil
}

// stripFence takes the code fence off an answer that was told to be one JSON
// object and came back as a markdown block of one.
func stripFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	} else {
		return text
	}
	if j := strings.LastIndex(text, "```"); j >= 0 {
		text = text[:j]
	}
	return strings.TrimSpace(text)
}

// oneLine is text with every run of white space made one space, cut to n runes.
func oneLine(text string, n int) string {
	return clipRunes(strings.Join(strings.Fields(text), " "), n)
}

// clipRunes cuts text to at most n runes, with an ellipsis where it was cut.
func clipRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return strings.TrimSpace(string(runes[:n-1])) + "…"
}
