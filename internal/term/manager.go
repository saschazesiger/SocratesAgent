package term

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
)

// MaxSessionsPerChat is one on purpose: a chat has a single terminal, the one
// the user is watching. Socrates and the person share that keyboard, and a
// second screen nobody is looking at is how abandoned programs pile up. To
// start something else, end the session first and open a new one.
const MaxSessionsPerChat = 1

// Manager owns every terminal session. Each session lives in its own directory
// below Root, with the socket of its host process, so that a Socrates which
// has just been restarted can find the sessions that kept running without it.
type Manager struct {
	// Root is where session directories are created.
	Root string
	// SelfPath is the Socrates binary, re-executed as the session host.
	SelfPath string

	mu       sync.Mutex
	sessions map[string]*Handle
}

// NewManager prepares the session directory.
func NewManager(root, selfPath string) (*Manager, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create the terminal directory %s: %w", root, err)
	}
	return &Manager{Root: root, SelfPath: selfPath, sessions: map[string]*Handle{}}, nil
}

func newSessionID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "term_" + hex.EncodeToString(b[:])
}

// Open starts a new session and returns a handle to it.
func (m *Manager) Open(ctx context.Context, chatID, name string, spec Spec) (*Handle, error) {
	// Opening is the natural moment to clear away what has long since ended.
	m.Prune()
	running := 0
	for _, h := range m.List(chatID) {
		if h.Alive() {
			running++
		}
	}
	if running >= MaxSessionsPerChat {
		return nil, fmt.Errorf("this chat already has a terminal session running, and there is only ever one - " +
			"end it with terminal_close and then call terminal_open again to start over")
	}
	if m.SelfPath == "" {
		return nil, fmt.Errorf("the path of the Socrates binary is unknown, cannot start a terminal session")
	}

	id := newSessionID()
	dir := filepath.Join(m.Root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	if strings.TrimSpace(name) == "" {
		name = "terminal"
	}
	on := hostSpec{
		ID: id, Name: name, ChatID: chatID,
		Command: spec.Command, Args: spec.Args, Dir: spec.Dir, Env: spec.Env,
		Cols: cols, Rows: rows, Meta: spec.Meta, Created: time.Now().UnixMilli(),
	}
	raw, err := json.Marshal(on)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, fileSpec), raw, 0o600); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(filepath.Join(dir, fileLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.Command(m.SelfPath, "term-host", "--dir", dir)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the terminal host: %w", err)
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
	m.sessions[id] = handle
	m.mu.Unlock()
	return handle, nil
}

// connect waits for the host's socket to appear and attaches to it.
func (m *Manager) connect(ctx context.Context, dir string, spec hostSpec, wait time.Duration) (*Handle, error) {
	sockPath := filepath.Join(dir, fileSock)
	deadline := time.Now().Add(wait)
	for {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			handle := newHandle(spec.ID, spec.Name, spec.ChatID, spec.Dir, spec.Meta, conn)
			if err := handle.waitReady(ctx, 10*time.Second); err != nil {
				handle.Detach()
				return nil, err
			}
			return handle, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the terminal session did not come up: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// hostFailure reads the tail of a host's log, used to explain a failed start.
func hostFailure(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, fileLog))
	if err != nil || len(raw) == 0 {
		if final, ok := readFinal(dir); ok && final.Output != "" {
			return strings.TrimSpace(final.Output)
		}
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.TrimSpace(strings.Join(lines, " · "))
}

// Restore reconnects to the sessions that were running before this process
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
			_ = os.RemoveAll(dir)
			continue
		}
		m.mu.Lock()
		_, known := m.sessions[spec.ID]
		m.mu.Unlock()
		if known {
			continue
		}
		handle, err := m.connect(ctx, dir, spec, 400*time.Millisecond)
		if err != nil {
			// No host listening: the session is over. Keep finished sessions
			// around briefly so their transcript can still be read.
			if final, ok := readFinal(dir); ok && time.Since(time.UnixMilli(final.EndedAt)) < 24*time.Hour {
				continue
			}
			_ = os.RemoveAll(dir)
			continue
		}
		// A host lingers for a while after its program has stopped, so a
		// successful connection is not by itself proof that anything is
		// still running.
		if !handle.State().Running {
			handle.Detach()
			continue
		}
		m.mu.Lock()
		m.sessions[spec.ID] = handle
		m.mu.Unlock()
		restored++
	}
	if restored > 0 {
		log.Printf("terminal: reconnected to %d session(s) that survived the restart", restored)
	}
	return restored
}

func readSpec(dir string) (hostSpec, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, fileSpec))
	if err != nil {
		return hostSpec{}, false
	}
	var spec hostSpec
	if err := json.Unmarshal(raw, &spec); err != nil || spec.ID == "" {
		return hostSpec{}, false
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

// SetMeta attaches a value to a session and writes it next to the socket, so
// it is still there after Socrates has been restarted.
func (m *Manager) SetMeta(id, key, value string) error {
	m.mu.Lock()
	h, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("there is no terminal session called %q", id)
	}
	meta := h.setMeta(key, value)

	dir := filepath.Join(m.Root, id)
	spec, ok := readSpec(dir)
	if !ok {
		return nil
	}
	spec.Meta = meta
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fileSpec), raw, 0o600)
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Handle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[id]
	return h, ok
}

// List returns the sessions of a chat, newest first. An empty chatID lists
// every session.
func (m *Manager) List(chatID string) []*Handle {
	m.mu.Lock()
	out := make([]*Handle, 0, len(m.sessions))
	for _, h := range m.sessions {
		if chatID == "" || h.ChatID() == chatID {
			out = append(out, h)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].State().Started > out[j].State().Started
	})
	return out
}

// States snapshots the sessions of a chat for the web UI.
func (m *Manager) States(chatID string) []State {
	handles := m.List(chatID)
	out := make([]State, 0, len(handles))
	for _, h := range handles {
		state := h.State()
		state.ID = h.ID()
		state.Name = h.Name()
		state.ChatID = h.ChatID()
		out = append(out, state)
	}
	return out
}

// take removes a session from the list and hands over its handle.
func (m *Manager) take(id string) (*Handle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	return h, ok
}

// Close ends one session and forgets it.
func (m *Manager) Close(ctx context.Context, id string, grace time.Duration) error {
	h, ok := m.take(id)
	if !ok {
		return fmt.Errorf("there is no terminal session called %q", id)
	}
	err := h.Close(ctx, grace)
	_ = os.RemoveAll(filepath.Join(m.Root, id))
	return err
}

// CloseAsync takes the session out of the list at once and then lets it shut
// down on its own goroutine. A program given its grace period can take the
// better part of a minute to go, which is far too long to keep a phone waiting
// on an HTTP response; the session's event stream says when it is really over.
func (m *Manager) CloseAsync(id string, grace time.Duration) error {
	h, ok := m.take(id)
	if !ok {
		return fmt.Errorf("there is no terminal session called %q", id)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := h.Close(ctx, grace)
		_ = os.RemoveAll(filepath.Join(m.Root, id))
		if err != nil {
			log.Printf("terminal: closing %s: %v", id, err)
		}
	}()
	return nil
}

// CloseChat ends every session of one chat, used when a chat is deleted.
func (m *Manager) CloseChat(ctx context.Context, chatID string) {
	for _, h := range m.List(chatID) {
		if err := m.Close(ctx, h.ID(), 3*time.Second); err != nil {
			log.Printf("terminal: closing %s: %v", h.ID(), err)
		}
	}
}

// Detach drops every connection but leaves the programs running, so that a
// restart of Socrates does not interrupt work in progress.
func (m *Manager) Detach() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.sessions {
		h.Detach()
	}
	m.sessions = map[string]*Handle{}
}

// FinishedRetention is how long a session that has already exited stays in the
// list, so its last screen can still be read.
const FinishedRetention = 30 * time.Minute

// Prune forgets sessions that finished a while ago and removes their
// directories. Sessions that are still running are never touched.
func (m *Manager) Prune() {
	cutoff := time.Now().Add(-FinishedRetention).UnixMilli()
	m.mu.Lock()
	var stale []string
	for id, h := range m.sessions {
		state := h.State()
		if h.Alive() || state.Running {
			continue
		}
		ended := state.Ended
		if ended == 0 {
			ended = state.Started
		}
		if ended != 0 && ended < cutoff {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, id := range stale {
		_ = os.RemoveAll(filepath.Join(m.Root, id))
	}
}
