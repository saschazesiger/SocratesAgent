package server

// Chats: agent sessions driven through their protocols by a per-chat agent
// host (internal/agenthost), not run in a terminal. A chat is a session row of
// kind chat, so it is listed, filed, read and deleted like any other; this
// file is everything that is different: the host each chat is talked to
// through, the outbox that makes a command arrive once, and the routes.
//
// The server is a client of its hosts and holds nothing a restart may lose:
// the journal is on disk and written by the host, the outbox is in the store,
// and on start every live host is reattached from the last event the journal
// file already has (AGENT-HOST.md §6).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The chat manager's clock.
const (
	// defaultChatAckWait is how long a request waits for the host to
	// acknowledge its command before it answers "queued": the command is in
	// the outbox either way and reaches the host once.
	defaultChatAckWait = 10 * time.Second
	// chatDialWait bounds one connect to a host.
	chatDialWait = 5 * time.Second
	// chatLaunchWait bounds bringing a host up.
	chatLaunchWait = 30 * time.Second
	// chatRetryMax is the longest wait between two tries to reach a host
	// that should be there.
	chatRetryMax = 5 * time.Second
	// chatStopWait is how long a delete waits for its host to go.
	chatStopWait = 5 * time.Second
)

// chatHub is every chat this server knows, and the hosts it talks to.
type chatHub struct {
	srv      *Server
	launcher *agenthost.Launcher
	// parkAfter overrides the setting when it is set, and ackWait is how long
	// a request waits for an ack: fields so that a test can run them fast.
	parkAfter time.Duration
	ackWait   time.Duration

	mu      sync.Mutex
	chats   map[string]*chatPeer
	streams map[*chatStream]struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	pumps   sync.WaitGroup
}

func newChatHub(s *Server) *chatHub {
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &chatHub{
		srv: s,
		launcher: &agenthost.Launcher{DataDir: s.dataDir, Binary: bin, Scope: agenthost.ServiceScope(),
			Logf: log.Printf},
		ackWait: defaultChatAckWait,
		chats:   map[string]*chatPeer{},
		streams: map[*chatStream]struct{}{},
		ctx:     ctx,
		cancel:  cancel,
	}
}

// start reattaches to every host that is still running, and recovers the
// chats whose host died while it had work: a turn running, or a command not
// yet acknowledged. Every other chat stays parked until it is sent something.
func (h *chatHub) start() {
	rows, err := h.srv.store.ListSessions()
	if err != nil {
		log.Printf("chats: could not read the sessions: %v", err)
		return
	}
	for i := range rows {
		if !rows[i].IsChat() {
			continue
		}
		p, err := h.peer(rows[i].ID)
		if err != nil {
			log.Printf("chats: %s: %v", rows[i].ID, err)
			continue
		}
		p.report()
		if p.wantsPump() {
			p.kick()
		}
	}
}

// close lets go of every host without stopping one: they keep running, and
// the next server reattaches to them.
func (h *chatHub) close() {
	h.cancel()
	h.mu.Lock()
	peers := make([]*chatPeer, 0, len(h.chats))
	for _, p := range h.chats {
		peers = append(peers, p)
	}
	streams := make([]*chatStream, 0, len(h.streams))
	for st := range h.streams {
		streams = append(streams, st)
	}
	h.mu.Unlock()
	for _, p := range peers {
		p.drop()
	}
	for _, st := range streams {
		st.end(eventsGoingAway, "the server is stopping")
	}
	h.pumps.Wait()
}

func (h *chatHub) parkAfterFor() time.Duration {
	if h.parkAfter > 0 {
		return h.parkAfter
	}
	return time.Duration(h.srv.Settings().Chats.ParkAfterMinutes) * time.Minute
}

// peer is the chat with this id, loaded from its journal the first time.
func (h *chatHub) peer(id string) (*chatPeer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.chats[id]; p != nil {
		return p, nil
	}
	if !agenthost.ValidChatID(id) {
		return nil, errors.New("not a chat id")
	}
	events, err := agent.ReadJournal(agenthost.JournalPath(h.srv.dataDir, id))
	if err != nil {
		return nil, err
	}
	p := &chatPeer{hub: h, id: id, feed: agent.NewFeed(events), proj: agent.Fold(events),
		waiters: map[string][]chan chatReply{}}
	h.chats[id] = p
	return p, nil
}

// remove stops a chat's host and forgets everything about the chat but the
// row, which the caller deletes.
func (h *chatHub) remove(ctx context.Context, id string) {
	p, err := h.peer(id)
	if err == nil {
		p.stopHost(ctx)
		p.drop()
	}
	h.mu.Lock()
	delete(h.chats, id)
	h.mu.Unlock()
	h.launcher.Stop(ctx, id)
	_ = h.srv.store.ClearOutbox(id)
	_ = os.Remove(agenthost.SocketPath(h.srv.dataDir, id))
	if err := os.RemoveAll(agenthost.ChatDir(h.srv.dataDir, id)); err != nil {
		log.Printf("chats: could not remove the directory of %s: %v", id, err)
	}
}

// park asks a chat's host to park as soon as it is idle: what filing a chat
// as done does instead of closing a terminal.
func (h *chatHub) park(id string) {
	p, err := h.peer(id)
	if err != nil {
		return
	}
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		_ = conn.Send(agenthost.Command{Op: agenthost.OpPark})
	}
}

// revoke ends every chat stream whose login is gone, as eventHub.revoke does
// for the event streams.
func (h *chatHub) revoke() {
	h.mu.Lock()
	streams := make([]*chatStream, 0, len(h.streams))
	for st := range h.streams {
		streams = append(streams, st)
	}
	h.mu.Unlock()
	for _, st := range streams {
		if !h.srv.store.ValidLogin(st.token) {
			st.end(eventsSignedOut, "signed out")
		}
	}
}

// chatReply is what the host said to one command.
type chatReply struct {
	ack  *agenthost.Ack
	fail *agenthost.Fail
}

// chatPeer is one chat as this server holds it: the journal in memory, its
// projection, and the connection to its host while there is one.
type chatPeer struct {
	hub *chatHub
	id  string

	mu      sync.Mutex
	feed    *agent.Feed
	proj    *agent.Projection
	conn    *agenthost.Conn
	pumping bool
	waiters map[string][]chan chatReply
	// reported is the activity last handed to the detector, so that a
	// stream of deltas is not a stream of reports.
	reported string
	asked    bool
	// hostErr is why the last try to reach or start the host failed.
	hostErr string
	// warm asks the pump to start a host although the chat has nothing for
	// it yet: a new chat, whose first message should find the agent up.
	warm bool
}

func (p *chatPeer) summary() agent.Summary {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.proj.Summary()
}

// needsHost is whether the chat has work a host must be there for: a command
// not yet acknowledged, or a turn the journal says is running.
func (p *chatPeer) needsHost() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.needsHostLocked()
}

func (p *chatPeer) needsHostLocked() bool {
	if p.proj.Turn != nil {
		return true
	}
	pending, err := p.hub.srv.store.Outbox(p.id)
	return err == nil && len(pending) > 0
}

// wantsPump is whether the chat should be connected: its host is alive -
// and may start a turn of its own, or report a background task, at any time -
// or it has work a host must be there for.
func (p *chatPeer) wantsPump() bool {
	if alive, _ := p.hub.launcher.Alive(p.id); alive {
		return true
	}
	return p.needsHost()
}

// kick starts the pump unless it runs.
func (p *chatPeer) kick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kickLocked()
}

func (p *chatPeer) kickLocked() {
	if p.pumping || p.hub.ctx.Err() != nil {
		return
	}
	p.pumping = true
	p.hub.pumps.Add(1)
	go p.pump()
}

// pump keeps the chat connected to a host for as long as it has one or needs
// one: connect (launching or recovering a host when there is none), serve the
// connection until it ends, and decide whether to go on.
func (p *chatPeer) pump() {
	defer p.hub.pumps.Done()
	ctx := p.hub.ctx
	wait := 200 * time.Millisecond
	for {
		conn, err := p.connect(ctx)
		if ctx.Err() != nil {
			p.stopPumping()
			return
		}
		if err != nil {
			if !errors.Is(err, errNoHost) {
				log.Printf("chats: %s: %v", p.id, err)
				p.mu.Lock()
				p.hostErr = err.Error()
				p.mu.Unlock()
			}
			if p.settle(true) {
				return
			}
			if sleepFor(ctx, wait) != nil {
				p.stopPumping()
				return
			}
			wait = min(wait*2, chatRetryMax)
			continue
		}
		wait = 200 * time.Millisecond
		p.serve(conn)
		if ctx.Err() != nil {
			p.stopPumping()
			return
		}
		if p.settle(false) {
			return
		}
	}
}

func (p *chatPeer) stopPumping() {
	p.mu.Lock()
	p.pumping = false
	p.mu.Unlock()
}

// settle decides, under the lock that kick takes, whether the pump stops: it
// does when the host is gone and the chat needs none, and when its last host
// could not start the agent - then the next command tries again, rather than
// a loop that starts a failing CLI every few seconds. A host that is alive
// keeps its pump, whatever dropped the connection: what it does next must
// be heard.
func (p *chatPeer) settle(failedToConnect bool) bool {
	alive, _ := p.hub.launcher.Alive(p.id)
	p.mu.Lock()
	defer p.mu.Unlock()
	failed := p.proj.Process == agent.ProcessFailed && p.proj.Turn == nil
	if (!alive && !p.needsHostLocked()) || (failed && !failedToConnect) {
		p.pumping = false
		return true
	}
	return false
}

// connect reaches the chat's host, starting one when there is none. A host
// that holds the chat's lock but does not answer is left alone and tried
// again: only a host that is provably dead is replaced.
func (p *chatPeer) connect(ctx context.Context) (*agenthost.Conn, error) {
	sock := agenthost.SocketPath(p.hub.srv.dataDir, p.id)
	dctx, cancel := context.WithTimeout(ctx, chatDialWait)
	conn, err := agenthost.Dial(dctx, sock, p.feed.Last())
	cancel()
	if err == nil {
		return conn, nil
	}
	alive, aerr := p.hub.launcher.Alive(p.id)
	if aerr != nil {
		return nil, aerr
	}
	if alive {
		return nil, errors.New("the agent host is alive and does not answer yet")
	}
	// A host that parked or exited is not started again for nothing: only
	// for work, or for a new chat's warm start.
	p.mu.Lock()
	start := p.warm || p.needsHostLocked()
	p.warm = false
	p.mu.Unlock()
	if !start {
		return nil, errNoHost
	}
	if err := p.launch(ctx); err != nil {
		return nil, err
	}
	dctx, cancel = context.WithTimeout(ctx, chatDialWait)
	defer cancel()
	return agenthost.Dial(dctx, sock, p.feed.Last())
}

// errNoHost is a connect that found no host and no reason to start one.
var errNoHost = errors.New("the chat has no agent host and nothing for one to do")

// launch starts a host for the chat. One that died with a turn running is
// recovered: its scope is stopped, which ends the tool processes it left, and
// the row says the chat resumed. The new host records the turn as
// interrupted and resumes the conversation.
func (p *chatPeer) launch(ctx context.Context) error {
	row, err := p.hub.srv.store.GetSession(p.id)
	if err != nil {
		return err
	}
	if err := p.hub.launcher.Reap(ctx, p.id); err != nil {
		return err
	}
	p.mu.Lock()
	busy := p.proj.Turn != nil
	resume := ""
	if p.proj.ConversationID == "" && row.CLISessionState == store.CLIKnown {
		resume = row.CLISessionID
	}
	model, effort := row.Model, row.Effort
	if p.proj.Model != "" {
		model, effort = p.proj.Model, p.proj.Effort
	}
	p.mu.Unlock()
	if busy {
		log.Printf("chats: %s: the agent host died during a turn; starting a new one", p.id)
		_ = p.hub.srv.store.NoteResume(p.id)
	}
	lctx, cancel := context.WithTimeout(ctx, chatLaunchWait)
	defer cancel()
	return p.hub.launcher.Launch(lctx, agenthost.Spec{
		ChatID: p.id, Harness: row.Harness, Workdir: row.Workdir, Model: model, Effort: effort,
		ResumeID: resume, ParkAfterMS: p.hub.parkAfterFor().Milliseconds(),
	})
}

// serve runs one connection: every unacknowledged command goes again, then
// the host's frames are applied until the connection ends.
func (p *chatPeer) serve(conn *agenthost.Conn) {
	p.mu.Lock()
	p.conn, p.hostErr = conn, ""
	pending, err := p.hub.srv.store.Outbox(p.id)
	if err != nil {
		log.Printf("chats: %s: could not read the outbox: %v", p.id, err)
	}
	for _, e := range pending {
		var cmd agenthost.Command
		if json.Unmarshal(e.Command, &cmd) == nil {
			_ = conn.Send(cmd)
		}
	}
	p.mu.Unlock()
	for {
		f, err := conn.Next()
		if err != nil {
			break
		}
		switch {
		case f.Event != nil:
			p.apply(*f.Event)
		case f.Delta != nil:
			p.feed.Delta(*f.Delta)
		case f.Ack != nil:
			p.reply(f.Ack.ClientID, chatReply{ack: f.Ack}, true)
		case f.Fail != nil:
			p.reply(f.Fail.ClientID, chatReply{fail: f.Fail}, !keepForNextHost(f.Fail))
		}
	}
	p.mu.Lock()
	if p.conn == conn {
		p.conn = nil
	}
	p.mu.Unlock()
	conn.Close()
}

// keepForNextHost is whether a refused command stays in the outbox for the
// next host: a message or an answer refused because the host was not running
// or was going away. Everything else leaves - a refusal that would be repeated,
// and an interrupt or a model change, which mean nothing to a host that starts
// later.
func keepForNextHost(f *agenthost.Fail) bool {
	if f.Code != agenthost.CodeNotRunning && f.Code != agenthost.CodeStopping {
		return false
	}
	return f.Op == agenthost.OpSend || f.Op == agenthost.OpAnswer
}

// drop closes the connection to the host, which keeps running.
func (p *chatPeer) drop() {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// apply takes one event from the host. The host's journal is the record, and
// this copy only ever moves forward along it: an event this server already has
// is not applied twice.
func (p *chatPeer) apply(ev agent.Event) {
	p.mu.Lock()
	last := p.feed.Last()
	if ev.Seq <= last {
		p.mu.Unlock()
		return
	}
	if ev.Seq != last+1 {
		log.Printf("chats: %s: events %d to %d are missing", p.id, last+1, ev.Seq-1)
	}
	p.feed.Append(ev)
	p.proj.Apply(ev)
	p.mu.Unlock()
	if ev.Type == agent.TypeConversation && ev.ConversationID != "" {
		if err := p.hub.srv.store.SetChatConversation(p.id, ev.ConversationID); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("chats: %s: could not record the conversation id: %v", p.id, err)
		}
	}
	p.report()
}

// report hands the chat's activity to the detector when it has changed:
// busy while a turn runs, waiting while a question is open, idle otherwise.
func (p *chatPeer) report() {
	p.mu.Lock()
	s := p.proj.Summary()
	if s.Activity == p.reported && s.Asked == p.asked {
		p.mu.Unlock()
		return
	}
	p.reported, p.asked = s.Activity, s.Asked
	p.mu.Unlock()
	note := ""
	if s.Activity == agent.ActivityWaiting {
		note = "question"
	}
	p.hub.srv.manager.ReportActivity(p.id, termux.State(s.Activity), note, s.Asked)
}

func (p *chatPeer) reply(clientID string, r chatReply, done bool) {
	if clientID == "" {
		return
	}
	if done {
		if err := p.hub.srv.store.DeleteOutbox(p.id, clientID); err != nil {
			log.Printf("chats: %s: could not clear %s from the outbox: %v", p.id, clientID, err)
		}
	}
	p.mu.Lock()
	waiters := p.waiters[clientID]
	delete(p.waiters, clientID)
	p.mu.Unlock()
	for _, ch := range waiters {
		ch <- r
	}
}

// submit sends a command exactly once, however often it is submitted: it is
// in the outbox before it is sent, the host acts on its client_id once, and
// it leaves the outbox only when the host has journaled it. The answer is the
// host's, or queued when the host has not answered within ackWait - which is
// not a failure: the command goes to the host as soon as there is one.
func (p *chatPeer) submit(ctx context.Context, cmd agenthost.Command) (chatReply, bool, error) {
	raw, err := json.Marshal(cmd)
	if err != nil {
		return chatReply{}, false, err
	}
	if _, err := p.hub.srv.store.PutOutbox(p.id, cmd.ClientID, raw); err != nil {
		return chatReply{}, false, err
	}
	ch := make(chan chatReply, 1)
	p.mu.Lock()
	p.waiters[cmd.ClientID] = append(p.waiters[cmd.ClientID], ch)
	if p.conn != nil {
		_ = p.conn.Send(cmd)
	}
	p.kickLocked()
	p.mu.Unlock()
	defer p.unwait(cmd.ClientID, ch)
	timer := time.NewTimer(p.hub.ackWait)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r, false, nil
	case <-timer.C:
		return chatReply{}, true, nil
	case <-ctx.Done():
		return chatReply{}, true, nil
	}
}

func (p *chatPeer) unwait(clientID string, ch chan chatReply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list := p.waiters[clientID]
	for i, c := range list {
		if c == ch {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(p.waiters, clientID)
	} else {
		p.waiters[clientID] = list
	}
}

// stopHost ends the chat's host and its CLI, for a delete.
func (p *chatPeer) stopHost(ctx context.Context) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		_ = conn.Send(agenthost.Command{Op: agenthost.OpStop})
	}
	deadline := time.Now().Add(chatStopWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if alive, _ := p.hub.launcher.Alive(p.id); !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ------------------------------------------------------------- the routes

// The chat routes and what they answer (AGENT-HOST.md §7):
//
//	POST /api/chats                          201 {"session":…,"chat":<summary>}
//	                                         200 the same for a repeated client_id
//	GET  /api/chats/{id}                     {"session":…,"chat":<summary>,"asks":[…]}
//	                                         ?history=1 adds "messages"
//	POST /api/chats/{id}/messages            200 {"client_id","seq","dup"}
//	                                         202 {"client_id","queued":true}
//	POST /api/chats/{id}/interrupt           the same
//	POST /api/chats/{id}/asks/{ask}/answer   the same, or
//	                                         409 {"answered_elsewhere":true,"by_device"}
//	GET  /api/chats/{id}/stream              WebSocket socrates.chat.v1 (chatstream.go)

// createChatBody is a new chat.
type createChatBody struct {
	ClientID    string `json:"client_id"`
	Harness     string `json:"harness"`
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	WorkdirMode string `json:"workdir_mode"`
	Workdir     string `json:"workdir"`
	Title       string `json:"title"`
	// Message is the first message, sent once the chat exists.
	Message *chatMessageBody `json:"message"`
}

// chatMessageBody is one message from the composer.
type chatMessageBody struct {
	ClientID    string             `json:"client_id"`
	Text        string             `json:"text"`
	Attachments []agent.Attachment `json:"attachments"`
}

func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	var body createChatBody
	if !readJSON(w, r, &body) {
		return
	}
	defer s.events.poke()
	clientID := strings.TrimSpace(body.ClientID)
	if clientID != "" {
		if existing, err := s.store.SessionByClientID(clientID); err == nil {
			s.answerChat(w, http.StatusOK, existing, nil)
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	settings := s.Settings()
	harness := strings.TrimSpace(body.Harness)
	if _, ok := agent.Lookup(harness); !ok {
		writeError(w, http.StatusBadRequest, "there is no chat for "+quoteOr(harness, "no harness")+" yet")
		return
	}
	if entry, known := settings.Harnesses.Entry(harness); known && !entry.Enabled {
		writeError(w, http.StatusBadRequest, harness+" is switched off in the dashboard")
		return
	}
	if body.Message != nil {
		if status, msg := checkMessage(body.Message); status != 0 {
			writeError(w, status, msg)
			return
		}
	}
	id := termux.NewID()
	workdir, err := harnesses.ResolveWorkdir(settings, body.WorkdirMode, body.Workdir, harness, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := strings.TrimSpace(body.WorkdirMode)
	if mode == "" {
		mode = harnesses.WorkdirDynamic
	}
	label := harness
	if h, ok := harnesses.Get(harness); ok {
		label = h.Label()
	}
	row := &store.Session{
		ID: id, ClientID: clientID, Title: sessionTitle(body.Title, label, workdir, mode),
		TitleSource: titleSource(body.Title), Harness: harness, Model: strings.TrimSpace(body.Model),
		Effort: strings.TrimSpace(body.Effort), Workdir: workdir, WorkdirMode: mode,
		Kind: store.KindChat, State: store.StateRunning,
	}
	if err := s.store.CreateSession(row); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row.ID != id {
		// Two identical requests raced, and the other one made the chat.
		s.answerChat(w, http.StatusOK, row, nil)
		return
	}
	p, err := s.chats.peer(row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.report()
	var sent map[string]any
	if body.Message != nil {
		_, sent = s.sendMessage(r.Context(), p, body.Message)
	} else {
		// The host comes up now, so the first message finds the agent warm.
		p.mu.Lock()
		p.warm = true
		p.kickLocked()
		p.mu.Unlock()
	}
	s.answerChat(w, http.StatusCreated, row, sent)
}

func quoteOr(s, empty string) string {
	if s == "" {
		return empty
	}
	return `"` + s + `"`
}

// answerChat writes a chat's row and summary.
func (s *Server) answerChat(w http.ResponseWriter, code int, row *store.Session, message map[string]any) {
	body := map[string]any{"session": s.view(row)}
	if p, err := s.chats.peer(row.ID); err == nil {
		body["chat"] = p.summary()
		p.mu.Lock()
		if p.hostErr != "" {
			body["host_error"] = p.hostErr
		}
		p.mu.Unlock()
	}
	if message != nil {
		body["message"] = message
	}
	writeJSON(w, code, body)
}

// chatOf loads the chat a request names, answering 404 itself.
func (s *Server) chatOf(w http.ResponseWriter, r *http.Request) (*store.Session, *chatPeer, bool) {
	row, ok := s.session(w, r)
	if !ok {
		return nil, nil, false
	}
	if !row.IsChat() {
		writeError(w, http.StatusNotFound, "that session is a terminal, not a chat")
		return nil, nil, false
	}
	p, err := s.chats.peer(row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}
	return row, p, true
}

func (s *Server) handleGetChat(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	body := map[string]any{"session": s.view(row)}
	p.mu.Lock()
	body["chat"] = p.proj.Summary()
	body["asks"] = p.proj.OpenAsks()
	if r.URL.Query().Get("history") == "1" {
		raw, err := json.Marshal(p.proj)
		if err == nil {
			body["messages"] = json.RawMessage(raw)
		}
	}
	if p.hostErr != "" {
		body["host_error"] = p.hostErr
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, body)
}

// checkMessage is the rules a message is held to before it is queued.
func checkMessage(m *chatMessageBody) (int, string) {
	m.ClientID = strings.TrimSpace(m.ClientID)
	if m.ClientID == "" {
		return http.StatusBadRequest, "a message needs a client_id"
	}
	if strings.TrimSpace(m.Text) == "" && len(m.Attachments) == 0 {
		return http.StatusBadRequest, "a message needs text or an attachment"
	}
	for _, a := range m.Attachments {
		if strings.TrimSpace(a.Name) == "" {
			return http.StatusBadRequest, "an attachment needs a name"
		}
		if a.Path != "" && (!filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path) {
			return http.StatusBadRequest, "an attachment's path must be absolute and clean"
		}
	}
	return 0, ""
}

func (s *Server) handleChatMessage(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body chatMessageBody
	if !readJSON(w, r, &body) {
		return
	}
	if status, msg := checkMessage(&body); status != 0 {
		writeError(w, status, msg)
		return
	}
	code, answer := s.sendMessage(r.Context(), p, &body)
	writeJSON(w, code, answer)
}

// sendMessage submits one message and turns the host's answer into a
// response. Sending a message is having seen the chat, as typing at a
// terminal is.
func (s *Server) sendMessage(ctx context.Context, p *chatPeer, m *chatMessageBody) (int, map[string]any) {
	s.manager.MarkRead(p.id)
	return p.command(ctx, agenthost.Command{Op: agenthost.OpSend, ClientID: m.ClientID, Text: m.Text, Attachments: m.Attachments})
}

// command submits a command and words the outcome. A client_id this chat's
// journal already has is answered from it, without a host.
func (p *chatPeer) command(ctx context.Context, cmd agenthost.Command) (int, map[string]any) {
	p.mu.Lock()
	seq, seen := p.proj.Seen(cmd.ClientID)
	p.mu.Unlock()
	if seen {
		return http.StatusOK, map[string]any{"client_id": cmd.ClientID, "seq": seq, "dup": true}
	}
	reply, queued, err := p.submit(ctx, cmd)
	switch {
	case err != nil:
		return http.StatusInternalServerError, map[string]any{"error": err.Error()}
	case queued:
		return http.StatusAccepted, map[string]any{"client_id": cmd.ClientID, "queued": true}
	case reply.fail != nil:
		status := http.StatusConflict
		switch reply.fail.Code {
		case agenthost.CodeBadRequest:
			status = http.StatusBadRequest
		case agenthost.CodeUnknownAsk:
			status = http.StatusNotFound
		case agenthost.CodeNotRunning, agenthost.CodeStopping:
			return http.StatusAccepted, map[string]any{"client_id": cmd.ClientID, "queued": true, "detail": reply.fail.Msg}
		}
		return status, map[string]any{"error": reply.fail.Msg, "code": reply.fail.Code}
	case reply.ack.AnsweredElsewhere:
		return http.StatusConflict, map[string]any{"error": "Answered on another device",
			"answered_elsewhere": true, "by_device": reply.ack.ByDevice}
	}
	return http.StatusOK, map[string]any{"client_id": cmd.ClientID, "seq": reply.ack.Seq, "dup": reply.ack.Dup}
}

func (s *Server) handleChatInterrupt(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.ClientID = strings.TrimSpace(body.ClientID)
	if body.ClientID == "" {
		writeError(w, http.StatusBadRequest, "an interrupt needs a client_id")
		return
	}
	// Nothing is running and no host is there: there is nothing to stop, and
	// starting a host to tell it so would be the opposite of an interrupt.
	p.mu.Lock()
	idle := p.proj.Turn == nil && p.conn == nil
	p.mu.Unlock()
	if idle {
		writeJSON(w, http.StatusOK, map[string]any{"client_id": body.ClientID, "idle": true})
		return
	}
	code, answer := p.command(r.Context(), agenthost.Command{Op: agenthost.OpInterrupt, ClientID: body.ClientID})
	writeJSON(w, code, answer)
}

func (s *Server) handleChatAnswer(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		ClientID string          `json:"client_id"`
		Answer   json.RawMessage `json:"answer"`
		Device   string          `json:"device"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.ClientID = strings.TrimSpace(body.ClientID)
	if body.ClientID == "" || len(body.Answer) == 0 {
		writeError(w, http.StatusBadRequest, "an answer needs a client_id and an answer")
		return
	}
	askID := r.PathValue("ask")
	p.mu.Lock()
	ask := p.proj.Ask(askID)
	var answered bool
	var by, answeredBy string
	if ask != nil {
		answered, by, answeredBy = ask.Answered, ask.ByDevice, ask.ClientID
	}
	p.mu.Unlock()
	switch {
	case ask == nil:
		writeError(w, http.StatusNotFound, "this chat never asked that question")
		return
	case answered && answeredBy != body.ClientID:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Answered on another device",
			"answered_elsewhere": true, "by_device": by})
		return
	}
	code, answer := p.command(r.Context(), agenthost.Command{Op: agenthost.OpAnswer, ClientID: body.ClientID,
		AskID: askID, Answer: body.Answer, Device: strings.TrimSpace(body.Device)})
	writeJSON(w, code, answer)
}
