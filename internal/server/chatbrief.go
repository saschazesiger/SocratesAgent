package server

// A chat's brief: what the server keeps of a chat whose history it does not
// hold in memory. Home, the badge, the push and the pump decide by a handful
// of facts - the activity, whether a turn runs, the line and the question -
// and a server with a few hundred chats that read every journal into memory
// to know them would start slowly and hold every conversation for good
// (AGENT-HOST.md §10). So the facts are kept apart, in the brief, and the
// history is read only for what needs it: an open chat, a command, a turn
// running, a name to be found, a turn to be judged. A chat nobody has looked
// at for a while gives its history back.
//
// The brief is kept on disk beside the journal (chats/<id>/summary.json) with
// the seq it is true at. It is only ever believed when the journal still ends
// at that seq: the host is the journal's only writer, and a chat whose
// history the server does not hold has no host, so a journal that moved on
// is one a host wrote while this server was not running - and is read again.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

const (
	// defaultChatUnloadAfter is how long a chat nobody uses keeps its history
	// in memory: going back to a chat one was just in is not a read of its
	// journal.
	defaultChatUnloadAfter = 10 * time.Minute
	// chatJanitorEvery is how often the chats are looked over for one to
	// unload.
	chatJanitorEvery = time.Minute
	// chatLineGap is the least time between two readings of one chat that
	// differ only in the words of its line: a turn that streams changes it
	// four times a second, and every page would be told each time.
	chatLineGap = time.Second
)

// chatBrief is a chat in a few facts, true as of Seq.
type chatBrief struct {
	Seq     int64         `json:"seq"`
	Summary agent.Summary `json:"summary"`
	// Line is what Home says the chat is doing (chatLine), and Question
	// what it asks, while it asks (chatQuestion).
	Line     string          `json:"line,omitempty"`
	Question *digestQuestion `json:"question,omitempty"`
}

// briefOf is the brief of a projection.
func briefOf(p *agent.Projection) chatBrief {
	return chatBrief{Seq: p.LastSeq, Summary: p.Summary(), Line: chatLine(p), Question: chatQuestion(waitingAsk(p))}
}

func briefPath(dataDir, id string) string {
	return filepath.Join(agenthost.ChatDir(dataDir, id), paths.ChatSummary)
}

// readBrief is the brief kept for a chat, and whether it is still the
// journal's: one that ends at another seq, or is not there, is no brief.
func readBrief(dataDir, id string) (chatBrief, bool) {
	raw, err := os.ReadFile(briefPath(dataDir, id))
	if err != nil {
		return chatBrief{}, false
	}
	var b chatBrief
	if json.Unmarshal(raw, &b) != nil || b.Seq <= 0 {
		return chatBrief{}, false
	}
	last, err := agent.LastSeq(agenthost.JournalPath(dataDir, id))
	if err != nil || last != b.Seq {
		return chatBrief{}, false
	}
	return b, true
}

// writeBrief keeps a brief, atomically. A chat with nothing in its journal
// has nothing to keep.
func writeBrief(dataDir, id string, b chatBrief) error {
	if b.Seq <= 0 {
		return nil
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	path := briefPath(dataDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadLocked brings the chat's history into memory, its journal read and
// folded, if it is not there. Called with the lock held.
func (p *chatPeer) loadLocked() error {
	p.used = time.Now()
	if p.proj != nil {
		return nil
	}
	events, err := agent.ReadJournal(agenthost.JournalPath(p.hub.srv.dataDir, p.id))
	if err != nil {
		return err
	}
	p.hub.folds.Add(1)
	p.feed, p.proj = agent.NewFeed(events), agent.Fold(events)
	p.brief = briefOf(p.proj)
	return nil
}

// view runs fn on the chat's projection, reading the history first if it is
// not in memory.
func (p *chatPeer) view(fn func(*agent.Projection)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.loadLocked(); err != nil {
		return err
	}
	fn(p.proj)
	return nil
}

// subscribe is a reader of the chat's events, with the history in memory for
// as long as it reads.
func (p *chatPeer) subscribe() (*agent.Feed, *agent.Sub, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.loadLocked(); err != nil {
		return nil, nil, err
	}
	return p.feed, p.feed.Subscribe(), nil
}

// idleLocked is whether the chat's history can go: nothing uses it - no
// host connection or pump, no request waiting for an ack, no stream, no line
// waiting to be told - and nobody has for a while. Called with the lock held.
func (p *chatPeer) idleLocked(now time.Time) bool {
	return p.proj != nil && !p.pumping && p.conn == nil && len(p.waiters) == 0 && p.lineTimer == nil &&
		p.feed.Subscribers() == 0 && now.Sub(p.used) >= p.hub.unloadAfter
}

// unloadLocked keeps the brief and lets the history go. Called with the lock
// held.
func (p *chatPeer) unloadLocked() {
	p.saveBriefLocked()
	p.feed, p.proj = nil, nil
}

// saveBriefLocked keeps the brief on disk, unless the chat was deleted.
// Called with the lock held, which a delete takes before it removes the
// chat's folder, so a brief is never written into a folder that is gone.
func (p *chatPeer) saveBriefLocked() {
	if p.gone {
		return
	}
	if err := writeBrief(p.hub.srv.dataDir, p.id, p.brief); err != nil {
		log.Printf("chats: %s: could not keep its summary: %v", p.id, err)
	}
}

// janitor unloads the chats nobody uses, until the hub closes.
func (h *chatHub) janitor() {
	t := time.NewTicker(chatJanitorEvery)
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
			h.unloadIdle(time.Now())
		}
	}
}

// unloadIdle lets go of the history of every chat nobody uses.
func (h *chatHub) unloadIdle(now time.Time) {
	for _, p := range h.peers() {
		p.mu.Lock()
		if p.idleLocked(now) {
			p.unloadLocked()
		}
		p.mu.Unlock()
	}
}

// peers is every chat the hub knows.
func (h *chatHub) peers() []*chatPeer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*chatPeer, 0, len(h.chats))
	for _, p := range h.chats {
		out = append(out, p)
	}
	return out
}

// loaded is how many chats hold their history in memory.
func (h *chatHub) loaded() int {
	n := 0
	for _, p := range h.peers() {
		p.mu.Lock()
		if p.proj != nil {
			n++
		}
		p.mu.Unlock()
	}
	return n
}

/* --------------------------------------------------------- Home's reading */

// readingLocked is the chat as Home's digests say it: its line as the
// summary, its question, and the activity they are true in. Called with the
// lock held.
func (p *chatPeer) readingLocked(rev int64, at time.Time) digest {
	return digest{SessionID: p.id, Rev: rev, Phase: digestReady, State: p.brief.Summary.Activity,
		MadeAt: at.UnixMilli(), Summary: p.brief.Line, Question: p.brief.Question}
}

// sameReading is whether two readings say the same thing.
func sameReading(a, b digest) bool {
	return a.Summary == b.Summary && a.State == b.State && reflect.DeepEqual(a.Question, b.Question)
}

// publishReadingLocked tells every page when what the chat's reading says
// has changed. A new question or a new state goes at once; a line whose
// words changed while the rest stayed goes at most once a chatLineGap, and
// the last one always does. Called with the lock held, so two readings reach
// every page in the order they were made.
func (p *chatPeer) publishReadingLocked() {
	if p.gone {
		return
	}
	now := time.Now()
	next := p.readingLocked(p.shown.Rev, now)
	if p.shown.Rev != 0 && sameReading(p.shown, next) {
		return
	}
	if wait := chatLineGap - now.Sub(p.shownAt); p.shown.Rev != 0 && wait > 0 && next.State == p.shown.State &&
		reflect.DeepEqual(next.Question, p.shown.Question) {
		if p.lineTimer == nil {
			p.lineTimer = time.AfterFunc(wait, func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.lineTimer = nil
				p.publishReadingLocked()
			})
		}
		return
	}
	if p.lineTimer != nil {
		p.lineTimer.Stop()
		p.lineTimer = nil
	}
	next.Rev = p.hub.nextRev()
	p.shown, p.shownAt = next, now
	p.hub.srv.events.publish(map[string]any{"t": "digest", "id": p.id, "digest": next.clone()})
}

// reading is the chat's reading as it was last told, for a hello.
func (p *chatPeer) reading() digest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shown.Rev == 0 {
		p.shown, p.shownAt = p.readingLocked(p.hub.nextRev(), time.Now()), time.Now()
	}
	return p.shown.clone()
}

// nextRev numbers the readings. It starts from the clock in microseconds, as
// the digester's revs do, so that a page that held a reading from before a
// restart never takes a new one for an older.
func (h *chatHub) nextRev() int64 {
	for {
		prev := h.rev.Load()
		next := max(prev+1, time.Now().UnixMicro())
		if h.rev.CompareAndSwap(prev, next) {
			return next
		}
	}
}

// readings is every chat's reading, for a hello.
func (h *chatHub) readings() map[string]digest {
	out := map[string]digest{}
	for _, p := range h.peers() {
		out[p.id] = p.reading()
	}
	return out
}

// briefOfChat is a chat's brief, and false for a chat the hub does not know.
func (h *chatHub) briefOfChat(id string) (chatBrief, bool) {
	h.mu.Lock()
	p := h.chats[id]
	h.mu.Unlock()
	if p == nil {
		return chatBrief{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.brief, true
}

// rejudge is "Read again" for a chat: its reading, which its journal keeps
// current already, and the verdict on its last turn judged again
// (rejudgeChat).
func (h *chatHub) rejudge(id string) digest {
	p, err := h.peer(id)
	if err != nil {
		return digest{SessionID: id}
	}
	h.srv.digests.rejudgeChat(id)
	return p.reading()
}

/* -------------------------------------------------------- push and badge */

// askWords is what a chat asks, in one line, while it asks: what a push about
// the question says.
func (h *chatHub) askWords(id string) string {
	b, ok := h.briefOfChat(id)
	if !ok {
		return ""
	}
	return firstQuestion(b.Question)
}

// turnWords is what a push about a chat's ended turn says, and false for a
// turn that is no news (turnNews).
func (h *chatHub) turnWords(id string) (string, bool) {
	p, err := h.peer(id)
	if err != nil {
		return "Finished", true
	}
	words, news := "Finished", true
	if err := p.view(func(proj *agent.Projection) { words, news = turnNews(proj) }); err != nil {
		return "Finished", true
	}
	return words, news
}

// waiting is whether a chat waits for the person: an open ask it is stopped
// on, by its brief.
func (h *chatHub) waiting(id string) bool {
	b, ok := h.briefOfChat(id)
	return ok && b.Summary.Activity == agent.ActivityWaiting
}

/* ------------------------------------------------- the namer and Status */

// firstExchange is a chat's first message, with what the agent has answered
// to it so far: what the chat is named after.
func (h *chatHub) firstExchange(id string) (first, answer string) {
	p, err := h.peer(id)
	if err != nil {
		return "", ""
	}
	_ = p.view(func(proj *agent.Projection) {
		for _, m := range proj.Messages {
			if m.Role != agent.RoleUser || m.AnswerTo != "" {
				continue
			}
			first = messageWords(m)
			for _, t := range []*agent.Turn{proj.Turn, proj.LastTurn} {
				if t != nil && t.MessageID == m.ID {
					answer = clipRunes(turnAnswer(proj, t.ID), chatPromptRunes)
				}
			}
			return
		}
	})
	return first, answer
}

// latestExchange is the last thing the person said to a chat and what the
// agent answered, in words a model reads.
func (h *chatHub) latestExchange(id string) string {
	p, err := h.peer(id)
	if err != nil {
		return ""
	}
	text := ""
	_ = p.view(func(proj *agent.Projection) {
		t := proj.Turn
		if t == nil {
			t = proj.LastTurn
		}
		if t == nil {
			return
		}
		turn := readChatTurn(proj, t)
		if turn.Prompt != "" {
			text = "The user: " + turn.Prompt + "\n"
		}
		if turn.Answer != "" {
			text += "The answer: " + turn.Answer
		}
	})
	return strings.TrimSpace(text)
}

// statusOf is a chat as the spoken Status is told it (chatNow), and the
// activity it is in.
func (h *chatHub) statusOf(id string) (string, termux.State) {
	p, err := h.peer(id)
	if err != nil {
		return "", termux.StateUnknown
	}
	text, state := "", termux.StateUnknown
	_ = p.view(func(proj *agent.Projection) {
		text, state = chatNow(proj), termux.State(proj.Summary().Activity)
	})
	return text, state
}
