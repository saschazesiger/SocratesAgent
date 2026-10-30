package server

// The chat stream's slower path: GET /api/chats/{id}/events?since=N, the
// same events the stream replays (AGENT-HOST.md §7), a page at a time, for a
// browser whose WebSocket does not get through - a proxy that refuses the
// upgrade, a device whose sockets stay down while its plain requests work.
// It polls this while it keeps trying the stream; the live text deltas are
// the stream's alone, and the journal carries the same text a moment later.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

const (
	// chatEventsPage is how many of the journal's events one answer reads
	// at most, and the most a browser may ask for.
	chatEventsPage = 1000
	// chatEventsBytes is about how much one answer carries at most: a page
	// of long tool outputs stops early, after at least one event.
	chatEventsBytes = 4 << 20
)

// chatEventsAnswer is one page. Seq is the seq the browser holds once it has
// taken the page - the last event the page read, which compaction may have
// left out - and More says there is more after it. LastSeq and Base are the
// stream's hello: a browser that holds more than LastSeq, or less than Base,
// reads the chat again from 0.
type chatEventsAnswer struct {
	Chat    agent.Summary `json:"chat"`
	LastSeq int64         `json:"last_seq"`
	Base    int64         `json:"base"`
	Now     int64         `json:"now"`
	Events  []agent.Event `json:"events"`
	Seq     int64         `json:"seq"`
	More    bool          `json:"more"`
}

func (s *Server) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	since, err := queryInt(q.Get("since"), 0)
	if err != nil || since < 0 {
		writeError(w, http.StatusBadRequest, "since must be a seq, 0 or more")
		return
	}
	limit, err := queryInt(q.Get("limit"), chatEventsPage)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, "limit must be 1 or more")
		return
	}
	limit = min(limit, chatEventsPage)
	lite := q.Get("lite") == "1"

	// What opening the chat on the stream does, polling does too.
	p.rebuildIfLost(row)
	if p.wantsPump() {
		p.kick()
	}

	page, last, base, more, err := p.page(since, int(limit))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the chat could not be read")
		return
	}
	seq := since
	if len(page) > 0 {
		seq = page[len(page)-1].Seq
	}
	send := agent.Compact(page)
	if lite && len(send) > 0 {
		if err := p.view(func(proj *agent.Projection) { send = agent.Lighten(send, since, proj) }); err != nil {
			writeError(w, http.StatusInternalServerError, "the chat could not be read")
			return
		}
	}
	if send == nil {
		send = []agent.Event{}
	}
	writeJSONCompressed(w, r, http.StatusOK, chatEventsAnswer{
		Chat: p.summary(), LastSeq: last, Base: base, Now: time.Now().UnixMilli(),
		Events: send, Seq: seq, More: more,
	})
}

// page is up to limit of the chat's events after since, and no more than
// about chatEventsBytes of them, with the chat's last seq and base, and
// whether there are more.
func (p *chatPeer) page(since int64, limit int) ([]agent.Event, int64, int64, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.loadLocked(); err != nil {
		return nil, 0, 0, false, err
	}
	after := p.feed.After(since)
	n, size := 0, 0
	for n < len(after) && n < limit {
		raw, _ := json.Marshal(after[n])
		if n > 0 && size+len(raw) > chatEventsBytes {
			break
		}
		size += len(raw)
		n++
	}
	page := make([]agent.Event, n)
	copy(page, after[:n])
	return page, p.feed.Last(), p.base, n < len(after), nil
}

// queryInt reads a whole number from a query, or def for none.
func queryInt(raw string, def int64) (int64, error) {
	if raw == "" {
		return def, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}
