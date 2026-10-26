package server

// The chat stream: one open chat in a browser, GET /api/chats/{id}/stream.
// The client says hello{since} with the last seq it holds, the server replays
// the journal from there and then sends the live tail - one pull cursor over
// the chat's events, so nothing falls between the two and nothing comes
// twice - with the live text deltas, uncoalesced, between the events
// (AGENT-HOST.md §7). Its resilience rules are the event stream's: a
// handshake deadline, a quiet ping, the login checked on every tick and on
// revoke, and 4401 when it is gone.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

const (
	// chatSubprotocol names the framing.
	chatSubprotocol = "socrates.chat.v1"
	// chatHelloWait is how long a client has to say hello after the
	// handshake.
	chatHelloWait = 10 * time.Second
	// eventsGoingAway is the close of a stream whose server is stopping.
	eventsGoingAway = websocket.StatusGoingAway
)

// chatStream is one open stream, as the hub knows it for revoke.
type chatStream struct {
	token string
	gone  chan struct{}
	once  sync.Once

	code   websocket.StatusCode
	reason string
}

func (st *chatStream) end(code websocket.StatusCode, reason string) {
	st.once.Do(func() {
		st.code, st.reason = code, reason
		close(st.gone)
	})
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	if err := s.throttle(clientIP(r)); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	token := cookie.Value
	if !s.allowRate("chat "+loginKey(token), eventsHandshakeBurst) {
		writeError(w, http.StatusTooManyRequests, "too many chat connections; slow down")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{chatSubprotocol},
		CompressionMode: websocket.CompressionContextTakeover,
		OriginPatterns:  []string{r.Host},
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(eventsReadLimit)
	// A chat whose folder was lost while its row knows the conversation
	// gets its history back from the CLI's transcript as it is opened.
	p.rebuildIfLost(row)
	if err := s.serveChatStream(conn, token, p); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("chat stream %s: %v", p.id, err)
	}
}

// serveChatStream runs one stream from hello to close.
func (s *Server) serveChatStream(conn *websocket.Conn, token string, p *chatPeer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hctx, stop := context.WithTimeout(ctx, chatHelloWait)
	typ, payload, err := conn.Read(hctx)
	stop()
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "no hello")
		return nil
	}
	var hello struct {
		T     string `json:"t"`
		Since int64  `json:"since"`
		// Lite is a browser that asks for what it does not draw when it
		// draws it (agent.Lighten, GET /api/chats/{id}/parts/{part}).
		Lite bool `json:"lite"`
		// Batch is a browser that takes many events in one frame (evs).
		Batch bool `json:"batch"`
	}
	if typ != websocket.MessageText || json.Unmarshal(payload, &hello) != nil || hello.T != "hello" {
		_ = conn.Close(websocket.StatusPolicyViolation, "the first frame must be a hello")
		return nil
	}
	since := max(hello.Since, 0)

	st := &chatStream{token: token, gone: make(chan struct{})}
	h := p.hub
	h.mu.Lock()
	h.streams[st] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.streams, st)
		h.mu.Unlock()
	}()
	feed, sub, base, err := p.subscribe()
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "the chat could not be read")
		return err
	}
	defer sub.Close()

	write := func(payload []byte) error {
		wctx, stop := context.WithTimeout(ctx, s.writeTimeout)
		defer stop()
		return conn.Write(wctx, websocket.MessageText, payload)
	}
	frame, _ := json.Marshal(map[string]any{
		"t": "hello", "chat": p.summary(), "last_seq": feed.Last(), "base": base, "now": time.Now().UnixMilli(),
	})
	if err := write(frame); err != nil {
		_ = conn.CloseNow()
		return nil
	}
	// A chat whose host died with a turn running is recovered the moment it
	// is opened, if nothing has done it yet.
	if p.wantsPump() {
		p.kick()
	}

	replies := make(chan []byte, eventsRepliesBuffer)
	go s.readEvents(ctx, cancel, conn, replies)
	go s.pingEvents(ctx, cancel, conn)

	tick := time.NewTicker(s.events.tickEvery)
	defer tick.Stop()
	next, synced := since, false
	var pongs [][]byte
	for {
		events, deltas := sub.Pull(next)
		// What the browser folds is sent without what later events of the
		// same batch make moot (agent.Compact): a replay of a workflow that
		// journaled its state for hours is its last state, not every copy.
		// A light browser's replay is also without what it does not draw
		// until it is opened; what is live after it goes whole.
		send := agent.Compact(events)
		if hello.Lite && !synced && len(send) > 0 {
			if err := p.view(func(proj *agent.Projection) { send = agent.Lighten(send, since, proj) }); err != nil {
				_ = conn.Close(websocket.StatusInternalError, "the chat could not be read")
				return err
			}
		}
		if hello.Batch && len(send) > 1 {
			// A browser handles a frame at a time, each a task of its own:
			// a replay of a thousand events is a few frames, not a thousand.
			for _, frame := range batchFrames(send) {
				if err := write(frame); err != nil {
					_ = conn.CloseNow()
					return nil
				}
			}
		} else {
			for _, ev := range send {
				if err := write(agenthost.EncodeEvent(ev)); err != nil {
					_ = conn.CloseNow()
					return nil
				}
			}
		}
		if len(events) > 0 {
			next = events[len(events)-1].Seq
		}
		for _, d := range deltas {
			if err := write(agenthost.EncodeDelta(d)); err != nil {
				_ = conn.CloseNow()
				return nil
			}
		}
		// A pong says that everything published before its ping has been
		// sent, so it goes after the events, as on the event stream.
		for _, pong := range pongs {
			if err := write(pong); err != nil {
				_ = conn.CloseNow()
				return nil
			}
		}
		pongs = pongs[:0]
		if !synced {
			// The replay is over: what follows is live.
			synced = true
			frame, _ := json.Marshal(map[string]any{"t": "synced", "seq": next})
			if err := write(frame); err != nil {
				_ = conn.CloseNow()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			_ = conn.CloseNow()
			return nil
		case <-st.gone:
			_ = conn.Close(st.code, st.reason)
			return nil
		case <-sub.Wake():
		case pong := <-replies:
			pongs = append(pongs, pong)
		case <-tick.C:
			if !s.store.ValidLogin(token) {
				_ = conn.Close(eventsSignedOut, "signed out")
				return nil
			}
			frame, _ := json.Marshal(map[string]any{"t": "tick", "now": time.Now().UnixMilli()})
			if err := write(frame); err != nil {
				_ = conn.CloseNow()
				return nil
			}
		}
	}
}

// handleChatPart is one part of a chat whole, and the parts nested directly
// in it light (agent.Projection.Detail): what a light stream left out of a
// part, asked for when the part is opened. seq is the chat's last seq when
// it was read, so that the browser keeps what it has heard since.
func (s *Server) handleChatPart(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var (
		part     agent.Event
		children []agent.Event
		found    bool
		seq      int64
	)
	if err := p.view(func(proj *agent.Projection) {
		part, children, found = proj.Detail(r.PathValue("part"))
		seq = proj.LastSeq
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "the chat could not be read")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "the chat has no such part")
		return
	}
	writeJSONCompressed(w, r, http.StatusOK, map[string]any{"seq": seq, "part": part, "children": children})
}

// batchSize is about how much one evs frame carries.
const batchSize = 256 << 10

// batchFrames is events as evs frames - {"t":"evs","evs":[event, ...]} - of
// about batchSize each, in order.
func batchFrames(events []agent.Event) [][]byte {
	var frames [][]byte
	var buf []byte
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if buf == nil {
			buf = append(buf, `{"t":"evs","evs":[`...)
		} else {
			buf = append(buf, ',')
		}
		buf = append(buf, raw...)
		if len(buf) >= batchSize {
			frames = append(frames, append(buf, "]}"...))
			buf = nil
		}
	}
	if buf != nil {
		frames = append(frames, append(buf, "]}"...))
	}
	return frames
}
