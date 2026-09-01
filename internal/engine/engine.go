// Package engine turns what an agent says into what the browser shows. It owns
// the run lifecycle, the ordering guarantee the offline story rests on, and
// the two reconciliations that make a restart invisible: adopting a turn that
// kept running, and finding a chat's host again when the chat row lost track
// of it.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// ErrBusy is returned when a chat already has a run in flight. It is the one
// refusal the browser retries on its own, so nothing permanent may share it.
var ErrBusy = errors.New("this chat is still working on the previous message")

// ErrNoAgent is a chat from before Socrates talked to agents directly.
var ErrNoAgent = errors.New("this chat has no agent")

// ErrShuttingDown is returned while Socrates is letting go of its hosts. The
// HTTP server is still draining for a few seconds, and a message that arrived
// in that window must wait for the restart rather than start a second host
// beside a turn that is still running.
var ErrShuttingDown = errors.New("Socrates is restarting")

// Engine owns all running conversations.
type Engine struct {
	Store    *store.Store
	Bus      *Bus
	Settings func() config.Settings
	Hosts    *agenthost.Manager

	// commitMu makes writing a row and announcing it one indivisible step. The
	// browser treats the highest revision it has seen as "everything up to
	// here is mine", and that is only true if events reach it in the order the
	// revisions were handed out.
	commitMu sync.Mutex

	mu     sync.Mutex
	active map[string]*runHandle
	// detaching is set on the way out, before the host connections are
	// dropped, so a subscription that ends because we let go of the host is
	// not mistaken for a turn that died.
	detaching bool
}

type runHandle struct {
	id     string
	cancel context.CancelFunc
	seq    atomic.Int64
}

// Turn is one user message on its way into a chat: what was typed, whether
// the browser is in hands free mode, and the key that makes sending it safe to
// repeat over a connection that may drop mid request.
type Turn struct {
	ChatID   string
	Text     string
	Auto     bool
	ClientID string
}

// New creates an engine.
func New(st *store.Store, bus *Bus, settings func() config.Settings, hosts *agenthost.Manager) *Engine {
	return &Engine{
		Store:    st,
		Bus:      bus,
		Settings: settings,
		Hosts:    hosts,
		active:   map[string]*runHandle{},
	}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (e *Engine) publish(chatID string, ev Event) { e.Bus.Publish(chatID, ev) }

// commitStep writes a step and publishes it without letting another writer in
// between, so revision order and delivery order are the same thing.
func (e *Engine) commitStep(st *store.Step) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.Store.PutStep(st); err != nil {
		return err
	}
	e.publish(st.ChatID, Event{Type: "step", Step: copyStep(st)})
	return nil
}

// commitMessage does the same for a visible chat bubble.
func (e *Engine) commitMessage(m *store.Message) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.Store.AddMessage(m); err != nil {
		return err
	}
	e.publish(m.ChatID, Event{Type: "message", Message: m})
	return nil
}

// commitStepRemoval retires a step. A deletion carries no revision of its own,
// which is why a reconnecting client is also told which steps still exist.
func (e *Engine) commitStepRemoval(chatID, stepID string) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.Store.DeleteStep(stepID); err != nil {
		return err
	}
	e.publish(chatID, Event{Type: "step_removed", StepID: stepID})
	return nil
}

// copyStep clones because the caller keeps patching its own pointer while the
// previous version is still being encoded for SSE.
func copyStep(st *store.Step) *store.Step {
	clone := *st
	return &clone
}

// Busy reports whether a chat has an active run.
func (e *Engine) Busy(chatID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.active[chatID]
	return ok
}

// Stop cancels the active run of a chat, which reaches the agent as an
// interrupt.
func (e *Engine) Stop(chatID string) bool {
	e.mu.Lock()
	h, ok := e.active[chatID]
	e.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel()
	return true
}

// Detach is the shutdown path: mark that we are letting go, then drop the
// socket connections. The hosts, and the turns inside them, keep running.
//
// The order matters and is the whole of guard B-1. Dropping the sockets first
// would close every subscription, and a run loop falling through that would
// write "interrupted" to the run row milliseconds before the process exits -
// so on restart there is no active run, Adopt has nothing to claim, and a turn
// that is still working happily inside its host is orphaned with its answer in
// the journal and nothing left to commit it. That is every systemctl restart.
func (e *Engine) Detach() {
	e.mu.Lock()
	e.detaching = true
	e.mu.Unlock()
	e.Hosts.Detach()
}

// IsShuttingDown is the same answer isDetaching gives the run loop, for the
// HTTP layer: a model change that lands in the drain window has to wait for
// the restart, exactly as a message does.
func (e *Engine) IsShuttingDown() bool { return e.isDetaching() }

func (e *Engine) isDetaching() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.detaching
}

// Start records the user message and begins a turn. A turn that carries a
// ClientID is idempotent: sending it again - because the phone lost signal
// before the response came back - returns the run that already exists instead
// of saying the same thing twice.
func (e *Engine) Start(turn Turn) (*store.Run, error) {
	// First, and before anything is written. When a run loop returns on
	// detach its deferred delete runs, so for the seconds the HTTP server is
	// still draining Busy is false and the busy check would let a send sail
	// past it and open a second host beside a turn that is still running.
	if e.isDetaching() {
		return nil, ErrShuttingDown
	}
	chatID, text := turn.ChatID, turn.Text
	chat, err := e.Store.GetChat(chatID)
	if err != nil {
		return nil, err
	}
	if chat.Agent == "" {
		return nil, ErrNoAgent
	}
	if existing, err := e.Store.MessageByClientID(chatID, turn.ClientID); err == nil {
		if run, err := e.Store.GetRun(existing.RunID); err == nil {
			return run, nil
		}
		return &store.Run{ID: existing.RunID, ChatID: chatID, Status: store.RunDone}, nil
	}
	e.mu.Lock()
	if _, busy := e.active[chatID]; busy {
		e.mu.Unlock()
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &store.Run{ID: newID("run"), ChatID: chatID, Status: store.RunRunning, Auto: turn.Auto}
	h := &runHandle{id: run.ID, cancel: cancel}
	e.active[chatID] = h
	e.mu.Unlock()

	fail := func(err error) (*store.Run, error) {
		cancel()
		e.mu.Lock()
		delete(e.active, chatID)
		e.mu.Unlock()
		return nil, err
	}

	msg := &store.Message{
		ID: newID("msg"), ChatID: chatID, RunID: run.ID,
		Role: "user", Content: text, ClientID: turn.ClientID,
	}
	if err := e.Store.CreateRun(run); err != nil {
		return fail(err)
	}
	if err := e.commitMessage(msg); err != nil {
		return fail(err)
	}
	_ = e.Store.TouchChat(chatID)
	// Talking to an archived chat is what brings it back.
	if chat.Archived {
		if err := e.Store.SetChatArchived(chatID, false); err == nil {
			chat.Archived, chat.ArchivedAt = false, 0
			e.publish(chatID, Event{Type: "chat", Chat: chat})
		}
	}
	e.publish(chatID, Event{Type: "run", Run: run})

	if strings.TrimSpace(chat.Title) == "" {
		go e.generateTitle(chat.ID, text)
	}
	go e.run(ctx, chat, run, h, text)
	return run, nil
}

// run is one turn, from opening a host to the assistant message at the end.
func (e *Engine) run(ctx context.Context, chat *store.Chat, run *store.Run, h *runHandle, text string) {
	// The defer that removes the chat from active on every return path is what
	// makes Busy, Stop and idempotency safe even on a panic.
	defer func() {
		e.mu.Lock()
		if cur, ok := e.active[chat.ID]; ok && cur == h {
			delete(e.active, chat.ID)
		}
		e.mu.Unlock()
	}()

	p := newPump(e, chat, run, h)

	handle, err := e.ensureHost(ctx, chat)
	if err != nil {
		p.fatal(err.Error())
		return
	}
	seq, err := handle.Send(ctx, run.ID, text)
	if err != nil {
		p.fatal(err.Error())
		return
	}
	// Written once per turn, before a single event of it is consumed: this is
	// where the turn begins in the host's journal, and it is what an adopt
	// after a restart replays from.
	if err := e.Store.SetChatHostSeq(chat.ID, seq); err != nil {
		log.Printf("engine: could not record the turn position: %v", err)
	}

	frames, unsubscribe := handle.Subscribe(seq)
	defer unsubscribe()
	go func() {
		<-ctx.Done()
		// Stop and archive both land here. On shutdown the run contexts are
		// simply abandoned with the process - Detach never cancels them - so
		// this cannot fire on the way out and cancel a turn that should have
		// survived it.
		ictx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = handle.Interrupt(ictx, 10*time.Second)
	}()

	e.consume(frames, p, run, seq, false)
	e.recordSession(handle, p)
}

// recordSession copies the agent's own session id onto the chat row once the
// turn is over.
//
// It is not read off the event stream, and that is not an oversight: the
// adapter emits session_id before the first turn_started, so on a first turn
// it sits below the journal position this turn began at and the subscription
// never replays it. The status frame that closes a replay carries it, and
// picks it up for a long turn - but a turn that finished before the engine
// subscribed ends at turn_finished before that frame is read. Asking once,
// afterwards, is the one moment where the answer is always there.
func (e *Engine) recordSession(handle *agenthost.Handle, p *pump) {
	if handle == nil {
		return
	}
	if st := handle.Status(); st.SessionID != "" {
		p.noteSession(st.SessionID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := handle.Refresh(ctx)
	if err != nil {
		return
	}
	p.noteSession(st.SessionID)
}

// consume applies one subscription's frames to the pump.
//
// floor is where this turn began in the journal; adopting is true when this is
// a turn that was already running before the process started.
func (e *Engine) consume(frames <-chan agenthost.Frame, p *pump, run *store.Run, floor int64, adopting bool) {
	for f := range frames {
		if f.CaughtUp != nil {
			// The journal has run out. Everything it held has been applied
			// above - reaching this frame is the proof of that, which is why
			// it travels on the same channel as the events - so if we are
			// still here the turn has no end event in it.
			st := *f.CaughtUp
			// The status is also where the native session id comes from on a
			// first turn: the adapter emits it before the turn begins, so the
			// event itself is below this turn's floor and is filtered out.
			p.noteSession(st.SessionID)
			if adopting && (!st.Busy || st.TurnID != run.ID) {
				p.finish(harness.OutcomeInterrupted,
					"Socrates restarted and this message did not reach the agent, "+
						"or the agent stopped before answering - send it again")
				return
			}
			continue // live mode: nothing to do
		}
		ev := *f.Event
		if ev.Seq <= floor {
			continue // older than this turn
		}
		if ev.TurnID != "" && ev.TurnID != run.ID {
			continue // some other turn
		}
		p.apply(ev)
		if ev.Kind == harness.KindTurnFinished {
			p.finish(ev.Outcome, ev.Error)
			return
		}
		if ev.Kind == harness.KindFatal {
			p.fatal(ev.Error)
			return
		}
	}
	// The channel closed: the host went away - or we are letting go of it.
	if e.isDetaching() {
		return // leave the run running; the next process adopts it
	}
	// A connection can be dropped by the host's write deadline while the turn
	// behind it is perfectly healthy, so a close is a hiccup before it is a
	// lost turn. Replay is idempotent - deterministic ids, upserts - so
	// re-applying the turn so far patches the same rows and changes nothing
	// the browser has not already seen.
	if again, ok := e.resubscribe(p, run, floor); ok {
		e.consume(again, p, run, floor, adopting)
		return
	}
	p.finish(harness.OutcomeInterrupted, "")
}

// resubscribe redials the chat's host once and subscribes from the same floor.
func (e *Engine) resubscribe(p *pump, run *store.Run, floor int64) (<-chan agenthost.Frame, bool) {
	handle := e.liveHost(p.chat.HostDir, run.ChatID)
	if handle == nil && p.chat.HostDir != "" {
		// The connection is what went away, not the session behind it.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		again, err := e.Hosts.Reconnect(ctx, p.chat.HostDir)
		cancel()
		if err == nil {
			handle = again
		}
	}
	if handle == nil || !handle.Alive() {
		return nil, false
	}
	frames, unsubscribe := handle.Subscribe(floor)
	p.addCleanup(unsubscribe)
	return frames, true
}

// liveHost is the chat's host if it is attached and usable.
func (e *Engine) liveHost(dir, chatID string) *agenthost.Handle {
	if h, ok := e.Hosts.Get(dir); ok && h.Alive() {
		return h
	}
	for _, h := range e.Hosts.List(chatID) {
		if h.Alive() {
			return h
		}
	}
	return nil
}

// ensureHost resolves the chat's agent host, by the chat row first and by the
// manager second.
func (e *Engine) ensureHost(ctx context.Context, chat *store.Chat) (*agenthost.Handle, error) {
	// The chat's own record first - the common case, and free.
	if h, ok := e.Hosts.Get(chat.HostDir); ok && h.Alive() {
		return h, nil
	}
	// The manager is the authority on what is actually running for this chat.
	// A host spawned by a Socrates that died before it could write host_dir is
	// still restored, still alive, and still counts against MaxHostsPerChat -
	// so a host_dir-only lookup would call Open, be refused, and leave the
	// chat permanently unable to send.
	for _, h := range e.Hosts.List(chat.ID) {
		if !h.Alive() {
			continue
		}
		st := h.Status()
		if st.Model != chat.Model || st.Effort != chat.Effort {
			// This host is running something the chat no longer asks for - a
			// model change whose CloseChat did not reach it. Close it and open
			// a new one rather than quietly answering on the old model.
			_ = e.Hosts.CloseAsync(h.ID(), 3*time.Second)
			continue
		}
		if err := e.Store.SetChatHost(chat.ID, h.Dir()); err != nil {
			log.Printf("engine: could not record the host directory: %v", err)
		}
		chat.HostDir, chat.HostSeq = h.Dir(), 0
		return h, nil
	}
	h, err := e.Hosts.Open(ctx, chat.ID, e.specFor(chat))
	if err != nil {
		return nil, err
	}
	if err := e.Store.SetChatHost(chat.ID, h.Dir()); err != nil {
		log.Printf("engine: could not record the host directory: %v", err)
	}
	chat.HostDir, chat.HostSeq = h.Dir(), 0
	return h, nil
}

// specFor is everything the adapter needs, assembled from the chat row and the
// settings.
func (e *Engine) specFor(chat *store.Chat) harness.Spec {
	settings := e.Settings()
	entry, _ := settings.Agents.Entry(chat.Agent)
	return harness.Spec{
		Agent:     chat.Agent,
		Model:     chat.Model,
		Effort:    chat.Effort,
		Cwd:       e.workspace(chat),
		ChatID:    chat.ID,
		ChatTitle: chat.Title,
		SessionID: chat.AgentSession,
		Binary:    entry.Binary,
		ExtraArgs: entry.ExtraArgs,
	}
}

// Workspace is where this chat's agent works: its own folder below the
// workspace root, unless it was pinned somewhere else.
func (e *Engine) workspace(chat *store.Chat) string {
	if w := strings.TrimSpace(chat.Workspace); w != "" {
		return w
	}
	return Workspace(e.Settings(), chat)
}

// CloseChat stops the run and ends the chat's agent session. One call for
// archive, delete and a model change.
func (e *Engine) CloseChat(ctx context.Context, chatID string) {
	e.Stop(chatID)
	e.Hosts.CloseChat(ctx, chatID)
	if err := e.Store.SetChatHost(chatID, ""); err != nil {
		log.Printf("engine: could not clear the host of %s: %v", chatID, err)
	}
}

// Adopt takes over the turns that were running when this process started. It
// is called once, after Hosts.Restore, and returns the run ids it claimed so
// the caller can keep RecoverRuns off them.
func (e *Engine) Adopt(ctx context.Context) []string {
	var claimed []string
	byChat := map[string]*agenthost.Handle{}
	for _, h := range e.Hosts.List("") {
		if h.ChatID() == "" {
			continue
		}
		if _, seen := byChat[h.ChatID()]; !seen {
			byChat[h.ChatID()] = h
		}
	}

	chats, err := e.Store.ListChats(true)
	if err != nil {
		log.Printf("engine: could not list chats to adopt: %v", err)
		return nil
	}
	for i := range chats {
		chat := chats[i]
		run, err := e.Store.ActiveRun(chat.ID)
		if err != nil {
			// No run in flight. The host, if there is one, stays as it is: the
			// next message lands in the same session.
			continue
		}
		handle := byChat[chat.ID]
		if handle != nil && handle.Alive() {
			st := handle.Status()
			if st.Model != chat.Model || st.Effort != chat.Effort {
				// The chat asks for something this host is not running, so it
				// is not this chat's host any more.
				_ = e.Hosts.CloseAsync(handle.ID(), 3*time.Second)
				handle = nil
			}
		}
		if handle == nil || !handle.Alive() {
			e.interrupt(&chat, run, "Socrates restarted and the agent that was working on this is gone")
			continue
		}
		if chat.HostDir != handle.Dir() {
			// A host spawned by a Socrates that died before it could record
			// the directory is still this chat's host.
			if err := e.Store.SetChatHost(chat.ID, handle.Dir()); err != nil {
				log.Printf("engine: could not record the host of %s: %v", chat.ID, err)
			}
			// SetChatHost zeroes host_seq, and replaying this host's journal
			// from the old host's floor would skip the beginning of a turn it
			// never wrote.
			if fresh, err := e.Store.GetChat(chat.ID); err == nil {
				chat = *fresh
			}
		}

		ctxRun, cancel := context.WithCancel(context.Background())
		h := &runHandle{id: run.ID, cancel: cancel}
		e.mu.Lock()
		if _, busy := e.active[chat.ID]; busy {
			e.mu.Unlock()
			cancel()
			continue
		}
		e.active[chat.ID] = h
		e.mu.Unlock()
		claimed = append(claimed, run.ID)

		// A browser that connected before Adopt finished was told busy:false.
		// This one line corrects it without moving the listener.
		e.publish(chat.ID, Event{Type: "run", Run: run})

		go e.adopt(ctxRun, chat, run, h, handle)
	}
	return claimed
}

func (e *Engine) adopt(ctx context.Context, chat store.Chat, run *store.Run, h *runHandle, handle *agenthost.Handle) {
	defer func() {
		e.mu.Lock()
		if cur, ok := e.active[chat.ID]; ok && cur == h {
			delete(e.active, chat.ID)
		}
		e.mu.Unlock()
	}()
	p := newPump(e, &chat, run, h)
	frames, unsubscribe := handle.Subscribe(chat.HostSeq)
	defer unsubscribe()
	go func() {
		<-ctx.Done()
		ictx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = handle.Interrupt(ictx, 10*time.Second)
	}()
	e.consume(frames, p, run, chat.HostSeq, true)
	e.recordSession(handle, p)
}

// interrupt ends a run that has no host left to answer it.
func (e *Engine) interrupt(chat *store.Chat, run *store.Run, reason string) {
	if err := e.Store.SetRunStatus(run.ID, store.RunInterrupted, reason); err != nil {
		log.Printf("engine: could not interrupt %s: %v", run.ID, err)
		return
	}
	run.Status, run.Error = store.RunInterrupted, reason
	e.publish(chat.ID, Event{Type: "run", Run: run})
}
