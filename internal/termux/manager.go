package termux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// How often the manager asks tmux what is still alive, and how many answers in
// a row have to say "gone" before a session is declared lost with the machine.
//
// Two is not caution for its own sake: one busy moment, or a socket being
// replaced, must not flip forty sessions to needs_resume at once and set off a
// wave of relaunches.
const (
	PollInterval  = 2 * time.Second
	pollTolerance = 2
)

// SessionPrefix is what a Socrates tmux session is called. The name is stored
// with the row rather than recomputed, so that changing the shape of an id can
// never orphan a running session.
const SessionPrefix = "soc_"

// The environment the tmux server is given, and through it every hook we run.
// Putting the two paths here rather than in the hook command is what keeps the
// hook free of quoting: a data directory with a space, an apostrophe or a
// dollar in it would otherwise have to survive tmux's parser and then the
// shell's, in that order.
const (
	EnvSocratesBin = "SOCRATES_BIN"
	EnvHookSocket  = "SOCRATES_HOOK_SOCK"
)

// Config is everything the Manager needs to be built.
type Config struct {
	// DataDir holds the socket, the generated configuration and one directory
	// per session.
	DataDir string
	// TmuxBin and SocratesBin default to tmux on PATH and to this executable.
	TmuxBin     string
	SocratesBin string
	Conf        ConfOptions
	// WindowSize is the per window sizing policy: manual, latest or largest.
	WindowSize      string
	JournalMaxBytes int64
	JournalKeep     int
	// Supervisor wraps the command that starts the tmux server. Nil is the
	// right answer everywhere except under systemd.
	Supervisor Supervisor
	Logf       func(format string, args ...any)
	// OnExit and OnSize are how later layers hear about a pane that died and a
	// window that changed size. The frames they turn into are not this
	// package's business.
	OnExit func(sessionID string, status int)
	OnSize func(sessionID string, cols, rows int, owner string)
}

// Manager owns the Socrates tmux server: the sessions on it, the viewers
// watching them, and the reconciliation between what tmux has and what the
// database says.
type Manager struct {
	st   *store.Store
	cfg  Config
	tmux *Tmux

	tmuxPath    string
	tmuxVersion Version
	unavailable error

	mu           sync.Mutex
	live         map[string]*liveSession
	hooksSet     bool
	confFallback bool
	pollFails    int
	missed       map[string]int
	hookLn       net.Listener
	closed       bool
}

// New builds a Manager. It does not start anything, and it does not fail when
// tmux is missing or too old: the dashboard has to be able to say so.
func New(st *store.Store, cfg Config) (*Manager, error) {
	if st == nil {
		return nil, errors.New("a manager needs a store")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("a manager needs a data directory")
	}
	if cfg.WindowSize == "" {
		cfg.WindowSize = "manual"
	}
	if cfg.JournalMaxBytes <= 0 {
		cfg.JournalMaxBytes = JournalMaxBytes
	}
	if cfg.JournalKeep <= 0 {
		cfg.JournalKeep = JournalKeep
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.SocratesBin == "" {
		if exe, err := os.Executable(); err == nil {
			cfg.SocratesBin = exe
		}
	}
	m := &Manager{st: st, cfg: cfg, live: map[string]*liveSession{}, missed: map[string]int{}}

	bin := cfg.TmuxBin
	if bin == "" {
		found, err := exec.LookPath("tmux")
		if err != nil {
			m.unavailable = errors.New("tmux is not installed. Socrates needs it to keep sessions alive")
			bin = "tmux"
		} else {
			bin = found
		}
	}
	m.tmuxPath = bin
	if m.unavailable == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		v, err := BinaryVersion(ctx, bin)
		switch {
		case err != nil:
			m.unavailable = fmt.Errorf("could not run %s: %w", bin, err)
		case !v.OK():
			m.unavailable = fmt.Errorf("tmux %s is too old; Socrates needs %d.%d or newer",
				v, MinMajor, MinMinor)
		}
		m.tmuxVersion = v
	}
	m.tmux = &Tmux{
		Sock:       filepath.Join(cfg.DataDir, "tmux.sock"),
		Conf:       filepath.Join(cfg.DataDir, "tmux.conf"),
		Bin:        bin,
		Supervisor: cfg.Supervisor,
		Logf:       cfg.Logf,
	}
	return m, nil
}

// TmuxPath is the binary the manager runs.
func (m *Manager) TmuxPath() string { return m.tmuxPath }

// TmuxVersion is what that binary reported.
func (m *Manager) TmuxVersion() Version { return m.tmuxVersion }

// Socket is the path of the tmux server socket.
func (m *Manager) Socket() string { return m.tmux.Sock }

// ConfPath is the generated configuration file.
func (m *Manager) ConfPath() string { return m.tmux.Conf }

// Available reports why sessions cannot be created, or nil.
func (m *Manager) Available() error { return m.unavailable }

func (m *Manager) logf(format string, args ...any) { m.cfg.Logf(format, args...) }

// Start writes the generated configuration and opens the hook socket. It does
// not start the tmux server: the first session does that, with -f, which is
// the only way to be sure the user's own tmux.conf never loads.
func (m *Manager) Start(ctx context.Context) error {
	if err := os.MkdirAll(m.cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := WriteConf(m.tmux.Conf, m.cfg.Conf); err != nil {
		return err
	}
	return m.listenHooks()
}

// Close lets go of everything Socrates owns and nothing tmux owns. The whole
// point of the substrate is that the sessions outlive us.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	ln := m.hookLn
	var viewers []*Viewer
	for _, live := range m.live {
		viewers = append(viewers, live.viewers...)
	}
	m.mu.Unlock()

	for _, v := range viewers {
		_ = v.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

// ---------------------------------------------------------------- creating

// Spec is a request for a new session: the row it becomes and the plan that
// starts it.
type Spec struct {
	ID          string
	ClientID    string
	Title       string
	Harness     string
	Model       string
	Effort      string
	Workdir     string
	WorkdirMode string
	Options     json.RawMessage
	Cols, Rows  int
	Plan        harnesses.LaunchPlan
}

// NewID returns a session id: a uuid with its dashes removed, which is safe as
// a tmux session name where a dot or a colon would not be.
func NewID() string { return strings.ReplaceAll(uuid.NewString(), "-", "") }

// TmuxName is the tmux session name for a Socrates session id.
func TmuxName(id string) string { return SessionPrefix + id }

// SessionID is the inverse of TmuxName, and reports false for a tmux session
// that is not one of ours.
func SessionID(tmuxName string) (string, bool) {
	if !strings.HasPrefix(tmuxName, SessionPrefix) {
		return "", false
	}
	return strings.TrimPrefix(tmuxName, SessionPrefix), true
}

// Create starts a session and returns the row that records it.
//
// The row is written before any tmux command runs, and that order is the point
// of it: a crash between a tmux session appearing and a row existing would
// leave a terminal running that Socrates knows nothing about. Written first,
// that window is empty - and Adopt picks up anything that still slips through
// rather than killing it.
func (m *Manager) Create(ctx context.Context, spec Spec) (*store.Session, error) {
	if err := m.unavailable; err != nil {
		return nil, err
	}
	if len(spec.Plan.Argv) == 0 {
		return nil, errors.New("a session needs a program to run")
	}
	if !filepath.IsAbs(spec.Plan.Cwd) {
		return nil, fmt.Errorf("the working directory %q is not absolute", spec.Plan.Cwd)
	}
	if spec.ID == "" {
		spec.ID = NewID()
	}
	if spec.Cols <= 0 {
		spec.Cols = store.DefaultCols
	}
	if spec.Rows <= 0 {
		spec.Rows = store.DefaultRows
	}

	row := &store.Session{
		ID:              spec.ID,
		ClientID:        spec.ClientID,
		Title:           spec.Title,
		Harness:         spec.Harness,
		Model:           spec.Model,
		Effort:          spec.Effort,
		Workdir:         spec.Plan.Cwd,
		WorkdirMode:     spec.WorkdirMode,
		Options:         spec.Options,
		TmuxName:        TmuxName(spec.ID),
		CLISessionID:    spec.Plan.CLISession,
		CLISessionState: cliStateFor(spec.Plan),
		State:           store.StateStarting,
		Cols:            spec.Cols,
		Rows:            spec.Rows,
	}
	if err := m.st.CreateSession(row); err != nil {
		return nil, err
	}
	if row.ID != spec.ID {
		// The client had already created this one over a link that dropped.
		return row, nil
	}

	if err := m.launch(ctx, row, spec.Plan); err != nil {
		_ = m.st.SetSessionState(row.ID, store.StateFailed, -1, Stderr(err))
		row.State, row.FailReason = store.StateFailed, Stderr(err)
		return row, err
	}
	if err := m.st.SetSessionState(row.ID, store.StateRunning, -1, ""); err != nil {
		return row, err
	}
	row.State = store.StateRunning
	return row, nil
}

func cliStateFor(plan harnesses.LaunchPlan) string {
	switch {
	case plan.CLISession != "":
		return store.CLIKnown
	case plan.Discover != "" && plan.Discover != harnesses.DiscoverNone:
		return store.CLIPending
	default:
		return store.CLINone
	}
}

// Relaunch starts a session's program again in the tmux session it already
// has a row for - the reboot case, where the row survived and the tmux server
// did not.
func (m *Manager) Relaunch(ctx context.Context, row *store.Session, plan harnesses.LaunchPlan) error {
	if err := m.unavailable; err != nil {
		return err
	}
	if err := m.launch(ctx, row, plan); err != nil {
		_ = m.st.SetSessionState(row.ID, store.StateFailed, -1, Stderr(err))
		return err
	}
	return m.st.SetSessionState(row.ID, store.StateRunning, -1, "")
}

// launch does the tmux half of creating a session: the files, the session
// itself, the per window size policy and the journal.
func (m *Manager) launch(ctx context.Context, row *store.Session, plan harnesses.LaunchPlan) error {
	if err := m.writeSessionFiles(row, plan); err != nil {
		return err
	}
	if err := m.newSession(ctx, row, plan); err != nil {
		return err
	}
	if err := m.applySizePolicy(ctx, row.TmuxName, row.Cols, row.Rows); err != nil {
		return err
	}
	if err := m.attachJournal(ctx, row.ID, row.TmuxName); err != nil {
		return err
	}
	m.mu.Lock()
	m.live[row.ID] = &liveSession{id: row.ID, tmuxName: row.TmuxName, cols: row.Cols, rows: row.Rows}
	delete(m.missed, row.ID)
	m.mu.Unlock()
	return nil
}

func (m *Manager) writeSessionFiles(row *store.Session, plan harnesses.LaunchPlan) error {
	dir := SessionDir(m.cfg.DataDir, row.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// plan.json is diagnostic - the row is what Socrates acts on - but it can
	// carry a generated password, so it is not world readable.
	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), planJSON, 0o600); err != nil {
		return err
	}
	journal, err := os.OpenFile(JournalPath(m.cfg.DataDir, row.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := journal.Close(); err != nil {
		return err
	}
	for _, f := range plan.Files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			return err
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(f.Path, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

// ensureServer makes sure a tmux server is listening, and that the two global
// hooks are set on it.
//
// The server is started on its own rather than by the first session, so that
// the hooks are in place before any pane can die - a program that exits at
// once would otherwise die before anything was listening for it. This is also
// the only command that passes -f, which is what keeps the user's own
// tmux.conf out of our server.
func (m *Manager) ensureServer(ctx context.Context) error {
	if m.tmux.Running(ctx) {
		m.ensureHooks(ctx)
		return nil
	}
	m.setServerEnv()
	if _, err := m.tmux.RunStart(ctx, "start-server"); err != nil {
		return err
	}
	m.mu.Lock()
	m.hooksSet = false
	m.mu.Unlock()
	m.ensureHooks(ctx)
	return nil
}

// newSession runs the create, with the one guard that keeps a bad generated
// option from making sessions impossible for ever: if the server refuses to
// start, or dies as the session is made, the configuration is logged verbatim,
// replaced with a minimal one and tried once more.
func (m *Manager) newSession(ctx context.Context, row *store.Session, plan harnesses.LaunchPlan) error {
	args := []string{"new-session", "-d", "-s", row.TmuxName,
		"-x", strconv.Itoa(row.Cols), "-y", strconv.Itoa(row.Rows), "-c", plan.Cwd}
	for _, kv := range sortedEnv(plan.Env) {
		args = append(args, "-e", kv)
	}
	args = append(args, "--")
	args = append(args, plan.Argv...)

	if err := m.ensureServer(ctx); err != nil && !m.serverRefusedToStart(err) {
		return err
	}
	_, err := m.tmux.RunStart(ctx, args...)
	if err == nil {
		return nil
	}
	if !m.serverRefusedToStart(err) {
		return err
	}
	if conf, readErr := os.ReadFile(m.tmux.Conf); readErr == nil {
		m.logf("tmux refused to start with the generated configuration:\n%s", conf)
	}
	if writeErr := writeConf(m.tmux.Conf, MinimalConf(m.cfg.Conf)); writeErr != nil {
		return err
	}
	m.logf("retrying with a minimal tmux configuration")
	if serverErr := m.ensureServer(ctx); serverErr != nil {
		return err
	}
	if _, retryErr := m.tmux.RunStart(ctx, args...); retryErr != nil {
		return retryErr
	}
	return nil
}

// serverRefusedToStart reports whether this failure looks like the server
// dying on the way up, and whether the fallback has already been tried.
func (m *Manager) serverRefusedToStart(err error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.confFallback {
		return false
	}
	if !serverGone(err) {
		return false
	}
	m.confFallback = true
	return true
}

// sortedEnv renders an environment map as KEY=VALUE, in a stable order so that
// two identical plans produce two identical command lines.
func sortedEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// applySizePolicy sets the sizing policy on one window, and never globally.
//
// This is the single most surprising fact about tmux in this program: a global
// `window-size manual` segfaults the server on the *next* new-session, in a
// configuration file or set live, on 3.6 and on 3.3a. Set live it is worse
// than it sounds, because the first session created afterwards works and the
// second takes the server, and every session on it, down. Per window it is
// well behaved and idempotent.
func (m *Manager) applySizePolicy(ctx context.Context, tmuxName string, cols, rows int) error {
	policy := m.cfg.WindowSize
	if policy == "" {
		policy = "manual"
	}
	if _, err := m.tmux.Run(ctx, "setw", "-t", tmuxName, "window-size", policy); err != nil {
		return err
	}
	if policy != "manual" || cols <= 0 || rows <= 0 {
		return nil
	}
	_, err := m.tmux.Run(ctx, "resize-window", "-t", tmuxName,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
	return err
}

// attachJournal points the pane's output at the rotating sink.
//
// Without -o, deliberately: -o is a toggle rather than a "toggle on", so
// calling it twice would silently close the journal. Without it a command
// always reopens the pipe.
func (m *Manager) attachJournal(ctx context.Context, id, tmuxName string) error {
	if m.cfg.SocratesBin == "" {
		return nil
	}
	cmd := PipeCommand(m.cfg.SocratesBin, JournalPath(m.cfg.DataDir, id),
		m.cfg.JournalMaxBytes, m.cfg.JournalKeep)
	_, err := m.tmux.Run(ctx, "pipe-pane", "-t", tmuxName, cmd)
	if err != nil && strings.Contains(strings.ToLower(Stderr(err)), "pane has exited") {
		// The program ended before the pipe could be attached. There is
		// nothing left to journal, and a session whose program exited at once
		// is an exit to report, not a session that failed to start.
		m.logf("session %s ended before its journal could be attached", id)
		return nil
	}
	return err
}

// ---------------------------------------------------------------- viewers

// Attach opens one browser's window onto a session: a tmux client of our own
// on a pseudo terminal, whose output goes into a replay ring.
func (m *Manager) Attach(ctx context.Context, sessionID, viewerID string, cols, rows int) (*Viewer, error) {
	if err := m.unavailable; err != nil {
		return nil, err
	}
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if cols <= 0 {
		cols = row.Cols
	}
	if rows <= 0 {
		rows = row.Rows
	}
	if viewerID == "" {
		viewerID = NewID()
	}

	cmd := exec.Command(m.tmuxPath, "-S", m.tmux.Sock, "attach", "-t", row.TmuxName)
	cmd.Env = append(os.Environ(), "TMUX=")
	master, tty, err := startPTY(cmd, cols, rows)
	if err != nil {
		return nil, err
	}
	v := &Viewer{
		ID: viewerID, SessionID: sessionID,
		m: m, cmd: cmd, master: master, tty: tty,
		ring: NewRing(RingSize),
		cols: cols, rows: rows,
		done: make(chan struct{}),
	}
	go v.pump(&Responder{Foreground: DefaultForeground, Background: DefaultBackground, W: master})

	if err := m.waitForClient(ctx, row.TmuxName, tty); err != nil {
		_ = v.Close()
		return nil, err
	}
	if err := m.own(ctx, v); err != nil {
		_ = v.Close()
		return nil, err
	}
	_ = m.st.NoteAttach(sessionID)
	return v, nil
}

// waitForClient waits until tmux agrees that our pseudo terminal is one of its
// clients, which is also how we learn that the attach worked at all.
func (m *Manager) waitForClient(ctx context.Context, tmuxName, tty string) error {
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for {
		out, err := m.tmux.Run(ctx, "list-clients", "-t", tmuxName, "-F", "#{client_tty}")
		if err == nil {
			for _, line := range Lines(out) {
				if line == tty {
					return nil
				}
			}
			last = fmt.Errorf("tmux did not report a client on %s", tty)
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// own makes v the viewer the session is sized to, and resizes the window to
// its size. Attaching and resizing are both explicit acts; typing is not, and
// under the manual policy typing never moves the window.
func (m *Manager) own(ctx context.Context, v *Viewer) error {
	cols, rows := v.Size()
	m.mu.Lock()
	live := m.live[v.SessionID]
	if live == nil {
		live = &liveSession{id: v.SessionID, tmuxName: TmuxName(v.SessionID)}
		m.live[v.SessionID] = live
	}
	live.promote(v)
	changed := live.cols != cols || live.rows != rows
	live.cols, live.rows = cols, rows
	name := live.tmuxName
	m.mu.Unlock()

	if m.cfg.WindowSize != "manual" && m.cfg.WindowSize != "" {
		// Under latest or largest the window is tmux's to decide, and issuing
		// resize-window would fight it.
		return nil
	}
	if _, err := m.tmux.Run(ctx, "resize-window", "-t", name,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)); err != nil {
		return err
	}
	_ = m.st.SetSessionSize(v.SessionID, cols, rows)
	if changed && m.cfg.OnSize != nil {
		m.cfg.OnSize(v.SessionID, cols, rows, v.ID)
	}
	return nil
}

// forget drops a viewer and hands the size on.
//
// Ownership moves when the socket is lost, not when a grace period expires: a
// phone that drove out of coverage must not pin a laptop's window to 60x20 for
// a minute and a half. With no viewers left the window keeps the size it has.
func (m *Manager) forget(v *Viewer) {
	m.mu.Lock()
	live := m.live[v.SessionID]
	if live == nil {
		m.mu.Unlock()
		return
	}
	wasOwner := live.owner() == v
	live.remove(v)
	next := live.owner()
	m.mu.Unlock()

	if !wasOwner || next == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.own(ctx, next); err != nil {
		m.logf("could not hand the window size over to viewer %s: %v", next.ID, err)
	}
}

// Viewers is who is watching a session.
func (m *Manager) Viewers(sessionID string) []*Viewer {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := m.live[sessionID]
	if live == nil {
		return nil
	}
	return append([]*Viewer(nil), live.viewers...)
}

// ---------------------------------------------------------------- deleting

// Delete kills a session, and is the only thing in Socrates that ever does.
// The working directory is left alone: work the user did is not ours to throw
// away.
func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return err
	}
	for _, v := range m.Viewers(sessionID) {
		_ = v.Close()
	}
	if m.unavailable == nil && row.TmuxName != "" {
		// A bare pipe-pane closes the journal; then the session goes.
		_, _ = m.tmux.Run(ctx, "pipe-pane", "-t", row.TmuxName)
		if _, err := m.tmux.Run(ctx, "kill-session", "-t", row.TmuxName); err != nil && !serverGone(err) &&
			!strings.Contains(strings.ToLower(Stderr(err)), "can't find session") {
			return err
		}
	}
	if err := m.st.DeleteSession(sessionID); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.live, sessionID)
	delete(m.missed, sessionID)
	m.mu.Unlock()
	return os.RemoveAll(SessionDir(m.cfg.DataDir, sessionID))
}
