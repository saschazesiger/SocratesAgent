//go:build linux

package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// recordingHarness is an adapter that keeps the spec each of its instances
// was started with and what each was sent, for a host run in this process.
const recordingHarness = "recording-test"

// recordings is what the recording adapters of the running test did.
var recordings = &startLog{}

type startLog struct {
	mu sync.Mutex
	// refuse are the accounts on which Start fails, as a signed-out account
	// or a refused key does.
	refuse  map[string]bool
	started []*recording
}

// reset forgets the previous test's adapters and refuses accounts.
func (l *startLog) reset(refuse ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started, l.refuse = nil, map[string]bool{}
	for _, a := range refuse {
		l.refuse[a] = true
	}
}

// all is every adapter that started, in order.
func (l *startLog) all() []*recording {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*recording(nil), l.started...)
}

// sentTo is what an adapter was sent.
func (l *startLog) sentTo(r *recording) []agent.Outgoing {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]agent.Outgoing(nil), r.sent...)
}

func init() {
	agent.Register(recordingHarness, func() agent.Adapter { return &recording{} })
}

type recording struct {
	spec agent.StartSpec
	sent []agent.Outgoing
}

func (a *recording) Start(_ context.Context, spec agent.StartSpec, _ agent.Emitter) error {
	recordings.mu.Lock()
	defer recordings.mu.Unlock()
	if recordings.refuse[spec.Account] {
		return errors.New("the account is signed out")
	}
	a.spec = spec
	recordings.started = append(recordings.started, a)
	if spec.First != nil {
		a.sent = append(a.sent, *spec.First)
	}
	return nil
}

func (a *recording) Send(_ context.Context, msg agent.Outgoing) error {
	recordings.mu.Lock()
	defer recordings.mu.Unlock()
	a.sent = append(a.sent, msg)
	return nil
}

func (a *recording) Interrupt(context.Context) error                       { return nil }
func (a *recording) Answer(context.Context, string, json.RawMessage) error { return agent.ErrAskGone }
func (a *recording) SetModel(context.Context, string, string) error        { return nil }
func (a *recording) Command(context.Context, string, string) error         { return nil }
func (a *recording) Park(context.Context) error                            { return nil }
func (a *recording) Stop() error                                           { return nil }
func (a *recording) Background() int                                       { return 0 }
func (a *recording) PID() int                                              { return 0 }

// newInProcessHost is a host of the recording harness on account, run in
// this process on a journal of its own, not yet booted.
func newInProcessHost(t *testing.T, account string) *host {
	t.Helper()
	dir := t.TempDir()
	journal, events, err := agent.OpenJournal(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h := &host{
		opts:      Options{DataDir: dir, ChatID: "c1", Version: "test"},
		spec:      Spec{ChatID: "c1", Harness: recordingHarness, Workdir: dir, Account: account, Env: []string{"PATH=" + os.Getenv("PATH")}},
		dir:       dir,
		sock:      filepath.Join(dir, "h.sock"),
		rec:       agent.NewRecorder(journal, events, 0),
		spawns:    make(chan spawnReq),
		conns:     map[*hostConn]struct{}{},
		tools:     map[string]*hostConn{},
		parkAfter: time.Hour,
		done:      make(chan struct{}),
	}
	t.Cleanup(func() {
		h.finish()
		if h.ln != nil {
			h.ln.Close()
		}
		h.rec.Close()
	})
	return h
}

// A command a client sends the moment the host's socket accepts waits for the
// agent to be up. The socket accepts before boot has run, and boot used to
// take the command lock only once its goroutine got going: a command read in
// between - a server sends its outbox as soon as it connects - found no agent
// and was refused "the agent is not running", which a server keeps for the
// next host and does not send this one again, so the message stayed
// undelivered while the host ran (TestALostChatFolderIsRebuiltFromTheTranscript
// under load).
func TestACommandSentAsTheHostStartsWaitsForTheAgent(t *testing.T) {
	recordings.reset()
	h := newInProcessHost(t, "acct-a")
	if err := h.listen(); err != nil {
		t.Fatal(err)
	}
	free := make(chan bool, 1)
	bootHook = func() {
		// Boot has just been scheduled: a command handled now finds no agent.
		took := h.cmdMu.TryLock()
		if took {
			h.cmdMu.Unlock()
		}
		free <- took
	}
	t.Cleanup(func() { bootHook = nil })
	h.start()
	if <-free {
		t.Fatal("the command lock is free while the host boots: a command sent now is refused as 'the agent is not running'")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, h.sock, NoEvents)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(Command{Op: OpSend, ClientID: "m1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := c.Next()
		if err != nil {
			t.Fatal(err)
		}
		if f.Fail != nil {
			t.Fatalf("the message sent as the host started was refused: %+v", f.Fail)
		}
		if f.Ack != nil && f.Ack.ClientID == "m1" {
			break
		}
	}
	started := recordings.all()
	if len(started) != 1 {
		t.Fatalf("%d adapters started", len(started))
	}
	if sent := recordings.sentTo(started[0]); len(sent) != 1 || sent[0].ClientID != "m1" {
		t.Fatalf("the agent was sent %+v", sent)
	}
}
