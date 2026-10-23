//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/testhome"
)

// fakeBin is the directory with the fake `opencode` the tests run, built
// once by TestMain.
var fakeBin string

// init and TestMain also make the test binary stand in for `socrates
// agent-host`, for the tests that run a real host.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		runtime.LockOSThread()
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		os.Exit(agenthost.Main(os.Args[2:], "test"))
	}
	dir, err := os.MkdirTemp("", "fakeopencode-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "opencode"), "../../../e2e/fakebin/fakeopencode")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build fakeopencode:", err)
		os.Exit(1)
	}
	fakeBin = dir
	code := testhome.Main(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

// recorder is an Emitter that keeps everything, numbered like a journal, and
// folds it into a projection.
type recorder struct {
	mu     sync.Mutex
	events []agent.Event
	deltas int
	proj   *agent.Projection
	seq    int64
	exited chan error
}

func newRecorder() *recorder {
	return &recorder{proj: agent.NewProjection(), exited: make(chan error, 1)}
}

func (r *recorder) Emit(events ...agent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range events {
		r.seq++
		ev.Seq, ev.At = r.seq, time.Now().UnixMilli()
		r.events = append(r.events, ev)
		r.proj.Apply(ev)
	}
}

func (r *recorder) Delta(messageID, partID, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deltas++
	part := r.proj.Part(partID)
	offset := 0
	if part != nil {
		offset = agent.UTF16Len(part.Text)
	}
	r.seq++
	ev := agent.Event{Seq: r.seq, At: time.Now().UnixMilli(), Type: agent.TypePartDelta, MessageID: messageID,
		PartID: partID, Offset: offset, Text: text}
	r.events = append(r.events, ev)
	r.proj.Apply(ev)
}

func (r *recorder) Exited(err error) {
	select {
	case r.exited <- err:
	default:
	}
}

func (r *recorder) view(fn func(p *agent.Projection, events []agent.Event)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.proj, r.events)
}

// count is how many events of type match.
func (r *recorder) count(typ string, match func(agent.Event) bool) int {
	n := 0
	r.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == typ && (match == nil || match(ev)) {
				n++
			}
		}
	})
	return n
}

func (r *recorder) first(typ string, match func(agent.Event) bool) (agent.Event, bool) {
	var out agent.Event
	found := false
	r.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == typ && (match == nil || match(ev)) {
				out, found = ev, true
				return
			}
		}
	})
	return out, found
}

// turns is the outcomes of the turns that ended, in order.
func (r *recorder) turns() []string {
	var out []string
	r.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == agent.TypeTurnEnded {
				out = append(out, ev.Outcome)
			}
		}
	})
	return out
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// rig is one chat's adapter on the fake.
type rig struct {
	t    *testing.T
	data string
	work string
	env  []string
	rec  *recorder
	a    *Adapter
	n    int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	data := t.TempDir()
	work := filepath.Join(data, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + data}
	return &rig{t: t, data: data, work: work, env: env}
}

func spawnPG(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func (r *rig) spec(resume string) agent.StartSpec {
	return agent.StartSpec{ChatID: "c1", Workdir: r.work, StateDir: filepath.Join(r.data, "chats", "c1", "agent"),
		DataDir: r.data, Env: r.env, Model: "fake/model", ResumeID: resume, Spawn: spawnPG}
}

// start starts a fresh adapter (a new host, as far as the adapter knows).
func (r *rig) start(spec agent.StartSpec) error {
	r.t.Helper()
	r.rec = newRecorder()
	r.a = New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := r.a.Start(ctx, spec, r.rec)
	if err == nil {
		a := r.a
		r.t.Cleanup(func() { _ = a.Stop() })
	}
	return err
}

func (r *rig) mustStart(resume string) {
	r.t.Helper()
	if err := r.start(r.spec(resume)); err != nil {
		r.t.Fatalf("start: %v", err)
	}
}

// send sends text as a journaled message would be, and returns its id.
func (r *rig) send(text string) string {
	r.t.Helper()
	r.n++
	id := fmt.Sprintf("m_%d_%d", time.Now().UnixNano(), r.n)
	r.rec.Emit(agent.Event{Type: agent.TypeMessage, MessageID: id, Role: agent.RoleUser, State: agent.MessagePending, Text: text})
	if err := r.a.Send(context.Background(), agent.Outgoing{MessageID: id, ClientID: "c_" + id, Text: text}); err != nil {
		r.t.Fatalf("send %q: %v", text, err)
	}
	return id
}

// waitTurns waits until n turns have ended.
func (r *rig) waitTurns(n int) []string {
	r.t.Helper()
	eventually(r.t, 20*time.Second, fmt.Sprintf("%d turns", n), func() bool { return len(r.rec.turns()) >= n })
	return r.rec.turns()
}

// lastText is the text of the last text part of the chat.
func (r *rig) lastText() string {
	text := ""
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Kind == agent.PartText {
					text = part.Text
				}
			}
		}
	})
	return text
}

func (r *rig) conversation() string {
	id := ""
	r.rec.view(func(p *agent.Projection, _ []agent.Event) { id = p.ConversationID })
	return id
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
