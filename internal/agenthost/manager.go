// Package agenthost runs one coding agent session in its own detached
// process, so that a turn in flight - and everything the agent spawned -
// survives a restart of Socrates.
//
// There are two processes: Socrates holds a Manager and one *Handle per live
// chat, and the host ("socrates agent-host --dir …") owns the harness.Adapter
// and serves it on a unix socket.
package agenthost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// MaxHostsPerChat is one on purpose: a chat is one conversation with one
// agent. Reaching the refusal below is a bug rather than a path the engine may
// take - the engine resolves an existing host by chat id before it ever calls
// Open - and it should read that way in the log.
const MaxHostsPerChat = 1

// readyTimeout is how long a host that has a socket gets to answer its first
// request. It is not the dial budget: a dead host fails to dial at once, and a
// host that failed to start writes final.json within milliseconds, which
// waitReady watches for. This is the time a host that is genuinely busy coming
// up - scanning a long journal, launching a CLI - is allowed to take.
const readyTimeout = 10 * time.Second

// Manager owns every agent session. Each session lives in its own directory
// below Root, so a Socrates which has just been restarted can find the hosts
// that kept running without it.
type Manager struct {
	// Root is where host directories are created.
	Root string
	// SelfPath is the Socrates binary, re-executed as the agent host.
	SelfPath string

	mu    sync.Mutex
	hosts map[string]*Handle
}

// NewManager prepares the host directory.
func NewManager(root, selfPath string) (*Manager, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create the agent directory %s: %w", root, err)
	}
	return &Manager{Root: root, SelfPath: selfPath, hosts: map[string]*Handle{}}, nil
}

func newHostID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "host_" + hex.EncodeToString(b[:])
}

// Open starts a new agent session and returns a handle to it.
func (m *Manager) Open(ctx context.Context, chatID string, spec harness.Spec) (*Handle, error) {
	// Opening is the natural moment to clear away what has long since ended.
	m.Prune()
	running := 0
	for _, h := range m.List(chatID) {
		if h.Alive() {
			running++
		}
	}
	if running >= MaxHostsPerChat {
		return nil, fmt.Errorf("chat %s already has an agent session running, and the engine should have "+
			"found it before opening another - this is a bug, not a limit you have hit", chatID)
	}
	if m.SelfPath == "" {
		return nil, fmt.Errorf("the path of the Socrates binary is unknown, cannot start an agent session")
	}
	if _, ok := harness.Get(spec.Agent); !ok {
		return nil, fmt.Errorf("there is no adapter for %q", spec.Agent)
	}

	id := newHostID()
	dir := filepath.Join(m.Root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sock, err := SocketPath(id)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	spec.ChatID = chatID
	spec.Dir = dir
	on := HostSpec{ID: id, Spec: spec, Socket: sock, Created: time.Now().UnixMilli()}
	if err := writeSpec(dir, on); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	logFile, err := os.OpenFile(filepath.Join(dir, fileLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.Command(m.SelfPath, "agent-host", "--dir", dir)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the agent host: %w", err)
	}
	// The host is deliberately not waited for: it outlives this process.
	go func() { _ = cmd.Wait() }()

	handle, err := m.connect(ctx, dir, on, 10*time.Second)
	if err != nil {
		// The host may have failed before it could listen; its log says why.
		if detail := hostFailure(dir); detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	m.mu.Lock()
	m.hosts[id] = handle
	m.mu.Unlock()
	return handle, nil
}

// connect waits for the host's socket to appear and attaches to it.
func (m *Manager) connect(ctx context.Context, dir string, spec HostSpec, wait time.Duration) (*Handle, error) {
	sockPath := spec.Socket
	if sockPath == "" {
		sockPath = filepath.Join(dir, fileSock)
	}
	deadline := time.Now().Add(wait)
	for {
		// A host that failed to start writes final.json within milliseconds
		// and says why. Checking for it first is what keeps a broken adapter
		// from costing the full dial budget before the error reaches the run.
		if final, ok := readFinal(dir); ok && final.Status.Error != "" {
			return nil, errors.New(final.Status.Error)
		}
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			handle := newHandle(spec.ID, spec.ChatID(), dir, conn)
			if err := handle.waitReady(ctx, dir, readyTimeout); err != nil {
				handle.Detach()
				return nil, err
			}
			return handle, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the agent session did not come up: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// hostFailure explains a failed start. final.json is preferred: it is the
// host's own account of what went wrong, in a sentence written for a person,
// whereas the log is whatever the process printed on its way out.
func hostFailure(dir string) string {
	if final, ok := readFinal(dir); ok && final.Status.Error != "" {
		return strings.TrimSpace(final.Status.Error)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileLog))
	if err != nil || len(raw) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	for i, line := range lines {
		lines[i] = stripLogTimestamp(line)
	}
	return strings.TrimSpace(strings.Join(lines, " · "))
}

// stripLogTimestamp removes the "2006/01/02 15:04:05 " the standard logger
// puts in front of every line, which has no business on a run row.
func stripLogTimestamp(line string) string {
	const stamp = len("2006/01/02 15:04:05 ")
	if len(line) < stamp {
		return line
	}
	head := line[:stamp]
	for i, r := range head {
		switch i {
		case 4, 7:
			if r != '/' {
				return line
			}
		case 10, 19:
			if r != ' ' {
				return line
			}
		case 13, 16:
			if r != ':' {
				return line
			}
		default:
			if r < '0' || r > '9' {
				return line
			}
		}
	}
	return line[stamp:]
}

// Restore reconnects to the hosts that were running before this process
// started, and clears away the directories of the ones that are finished.
func (m *Manager) Restore(ctx context.Context) int {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		return 0
	}
	restored := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.Root, entry.Name())
		spec, ok := readSpec(dir)
		if !ok {
			// The host never got as far as writing a spec, or the file is from
			// a version that spoke a different protocol. Either way there is
			// nothing here to talk to.
			m.removeHost(dir, entry.Name())
			continue
		}
		m.mu.Lock()
		_, known := m.hosts[spec.ID]
		m.mu.Unlock()
		if known {
			continue
		}
		handle, err := m.connect(ctx, dir, spec, 400*time.Millisecond)
		if err != nil {
			// No host listening: the session is over. Keep it around briefly so
			// a fatal can still be read.
			if final, ok := readFinal(dir); ok && time.Since(time.UnixMilli(final.EndedAt)) < 24*time.Hour {
				continue
			}
			m.removeHost(dir, spec.ID)
			continue
		}
		// Every host that answers is tracked, including one whose adapter has
		// died: it is still this chat's host, it still holds the session, and
		// the next send restarts the adapter inside it. A host that is merely
		// lingering after an explicit close drops its listener within 30
		// seconds, and its handle goes closed on its own.
		m.mu.Lock()
		m.hosts[spec.ID] = handle
		m.mu.Unlock()
		restored++
	}
	if restored > 0 {
		log.Printf("agents: reconnected to %d session(s) that survived the restart", restored)
	}
	return restored
}

// Reconnect redials a host whose connection was dropped while the session
// behind it kept running - the write deadline firing on a peer that froze, a
// socket closed by hand. It replaces the dead handle rather than adding a
// second one, so callers that were holding the old one find the new one where
// they expect it.
func (m *Manager) Reconnect(ctx context.Context, dir string) (*Handle, error) {
	spec, ok := readSpec(dir)
	if !ok {
		return nil, fmt.Errorf("there is no agent session in %s", dir)
	}
	handle, err := m.connect(ctx, dir, spec, 2*time.Second)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if old, exists := m.hosts[spec.ID]; exists && old != handle {
		old.Detach()
	}
	m.hosts[spec.ID] = handle
	m.mu.Unlock()
	return handle, nil
}

// removeHost drops a host directory and the socket that lives outside it.
func (m *Manager) removeHost(dir, id string) {
	_ = os.RemoveAll(dir)
	if sock, err := SocketPath(id); err == nil {
		_ = os.Remove(sock)
	}
}

func writeSpec(dir string, spec HostSpec) error {
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	// Atomic: a host reading spec.json while it is rewritten - which happens
	// exactly once, when the native session id becomes known - must see one
	// version or the other, never half of each.
	tmp := filepath.Join(dir, fileSpec+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, fileSpec))
}

func readSpec(dir string) (HostSpec, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, fileSpec))
	if err != nil {
		return HostSpec{}, false
	}
	var spec HostSpec
	if err := json.Unmarshal(raw, &spec); err != nil || spec.ID == "" || spec.Spec.Agent == "" {
		return HostSpec{}, false
	}
	return spec, true
}

func readFinal(dir string) (Final, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, fileFinal))
	if err != nil {
		return Final{}, false
	}
	var final Final
	if err := json.Unmarshal(raw, &final); err != nil {
		return Final{}, false
	}
	return final, true
}

// Get returns a session by its directory, which is what the chat row stores.
func (m *Manager) Get(dir string) (*Handle, bool) {
	if dir == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.hosts {
		if h.Dir() == dir {
			return h, true
		}
	}
	return nil, false
}

// List returns the sessions of a chat, newest first. An empty chatID lists
// every session.
func (m *Manager) List(chatID string) []*Handle {
	m.mu.Lock()
	out := make([]*Handle, 0, len(m.hosts))
	for _, h := range m.hosts {
		if chatID == "" || h.ChatID() == chatID {
			out = append(out, h)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Status().Started > out[j].Status().Started
	})
	return out
}

// take removes a session from the list and hands over its handle.
func (m *Manager) take(id string) (*Handle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[id]
	if ok {
		delete(m.hosts, id)
	}
	return h, ok
}

// Close ends one session and forgets it.
func (m *Manager) Close(ctx context.Context, id string, grace time.Duration) error {
	h, ok := m.take(id)
	if !ok {
		return fmt.Errorf("there is no agent session called %q", id)
	}
	err := h.Close(ctx, grace)
	m.removeHost(filepath.Join(m.Root, id), id)
	return err
}

// CloseAsync takes the session out of the list at once and then lets it shut
// down on its own goroutine. A coding agent given its grace period can take
// the better part of a minute to go, which is far too long to keep a phone
// waiting on an HTTP response.
func (m *Manager) CloseAsync(id string, grace time.Duration) error {
	h, ok := m.take(id)
	if !ok {
		return fmt.Errorf("there is no agent session called %q", id)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := h.Close(ctx, grace)
		m.removeHost(filepath.Join(m.Root, id), id)
		if err != nil {
			log.Printf("agents: closing %s: %v", id, err)
		}
	}()
	return nil
}

// CloseChat ends every session of one chat, used when a chat is archived,
// deleted, or moved to a different model.
func (m *Manager) CloseChat(ctx context.Context, chatID string) {
	for _, h := range m.List(chatID) {
		if err := m.Close(ctx, h.ID(), 3*time.Second); err != nil {
			log.Printf("agents: closing %s: %v", h.ID(), err)
		}
	}
}

// Detach drops every connection but leaves the hosts running, so that a
// restart of Socrates does not interrupt work in progress. The engine has to
// be told first, or every subscription closing under it looks like a turn that
// died.
func (m *Manager) Detach() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.hosts {
		h.Detach()
	}
	m.hosts = map[string]*Handle{}
}

// FinishedRetention is how long a session whose adapter has ended stays in the
// list, so what happened to it can still be read.
const FinishedRetention = 30 * time.Minute

// Prune forgets sessions whose host process is gone and removes their
// directories. A host that is still reachable is never touched, whatever its
// adapter is doing: it may have no turn running, or a dead CLI waiting to be
// restarted by the next send, and removing spec.json from under a living
// process would leave it running with nothing left to identify it.
func (m *Manager) Prune() {
	cutoff := time.Now().Add(-FinishedRetention).UnixMilli()
	m.mu.Lock()
	var stale []string
	for id, h := range m.hosts {
		st := h.Status()
		if h.Alive() {
			continue
		}
		ended := st.Ended
		if ended == 0 {
			ended = st.Started
		}
		if ended != 0 && ended < cutoff {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		delete(m.hosts, id)
	}
	m.mu.Unlock()
	for _, id := range stale {
		m.removeHost(filepath.Join(m.Root, id), id)
	}
}
