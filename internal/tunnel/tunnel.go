// Package tunnel supervises a Cloudflare tunnel so Socrates can be reached
// from the internet without opening a port.
//
// The tunnel is a child process (`cloudflared`) that dials out to Cloudflare
// and forwards traffic to the local listener. Socrates keeps serving on its
// local address at all times, which is what you point the tunnel at and what
// you use to configure everything in the first place.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/proc"
)

// States of the managed tunnel.
const (
	StateStopped    = "stopped"
	StateInstalling = "installing"
	StateStarting   = "starting"
	StateRunning    = "running"
	StateFailed     = "failed"
)

// quickURL matches the hostname cloudflared prints for a free quick tunnel.
var quickURL = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com`)

// readyAfter is how long the process has to stay alive before the tunnel counts
// as up, for the case where the log wording changes between cloudflared versions.
const readyAfter = 4 * time.Second

// Status is the snapshot the admin dashboard renders.
type Status struct {
	State      string   `json:"state"`
	Mode       string   `json:"mode"`
	URL        string   `json:"url"`
	LocalURL   string   `json:"local_url"`
	Error      string   `json:"error"`
	Since      int64    `json:"since"`
	Restarts   int      `json:"restarts"`
	Installed  bool     `json:"installed"`
	Version    string   `json:"version"`
	Command    string   `json:"command"`
	Path       string   `json:"path"`
	Managed    bool     `json:"managed"`
	CanInstall bool     `json:"can_install"`
	Logs       []string `json:"logs"`
}

// Manager owns at most one cloudflared process.
type Manager struct {
	settings  func() config.Settings
	localURL  func() string
	installer *Installer

	mu         sync.Mutex
	state      string
	installing bool
	url        string
	errMsg     string
	since      time.Time
	restarts   int
	logs       *ring
	cancel     context.CancelFunc
	done       chan struct{}
	gen        int
}

// New creates a manager. localURL reports the address the tunnel should
// publish, for example http://127.0.0.1:8080. installDir is where Socrates
// keeps a cloudflared it downloaded itself.
func New(settings func() config.Settings, localURL func() string, installDir string) *Manager {
	return &Manager{
		settings:  settings,
		localURL:  localURL,
		installer: NewInstaller(installDir),
		state:     StateStopped,
		logs:      newRing(200),
	}
}

// Start launches the tunnel and keeps it alive until Stop is called. Calling it
// while a tunnel is already running restarts it with the current settings.
func (m *Manager) Start() error {
	cfg := m.settings().Tunnel
	if cfg.Mode == config.TunnelToken && strings.TrimSpace(cfg.Token) == "" {
		return errors.New("a named tunnel needs its tunnel token")
	}

	m.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.gen++
	gen := m.gen
	m.cancel = cancel
	m.done = make(chan struct{})
	done := m.done
	m.state = StateStarting
	m.errMsg = ""
	m.url = cfg.PublicURL()
	m.restarts = 0
	m.since = time.Now()
	m.logs.add("starting " + cfg.Command)
	m.mu.Unlock()

	go m.supervise(ctx, gen, done)
	return nil
}

// Stop shuts the tunnel down and waits briefly for the process to go away.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	}
	m.mu.Lock()
	if m.state != StateFailed {
		m.state = StateStopped
	}
	m.url = ""
	m.logs.add("tunnel stopped")
	m.mu.Unlock()
}

// Status reports what the tunnel is doing right now.
func (m *Manager) Status() Status {
	cfg := m.settings().Tunnel
	m.mu.Lock()
	state := m.state
	if m.installing && state == StateStopped {
		// A download started from the dashboard, without a tunnel running.
		state = StateInstalling
	}
	status := Status{
		State:    state,
		Mode:     cfg.Mode,
		URL:      m.url,
		Error:    m.errMsg,
		Restarts: m.restarts,
		Command:  cfg.Command,
		Logs:     m.logs.snapshot(),
	}
	if !m.since.IsZero() {
		status.Since = m.since.UnixMilli()
	}
	m.mu.Unlock()
	if status.URL == "" {
		status.URL = cfg.PublicURL()
	}
	if m.localURL != nil {
		status.LocalURL = m.localURL()
	}
	status.Installed, status.Version, status.Path = m.Probe()
	status.Managed = status.Path != "" && status.Path == m.installer.Path()
	status.CanInstall = Supported()
	return status
}

// Running reports whether a tunnel process is supposed to be up.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

// supervise restarts cloudflared with a backoff for as long as it is wanted.
func (m *Manager) supervise(ctx context.Context, gen int, done chan struct{}) {
	defer close(done)
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil || m.generation() != gen {
			return
		}
		started := time.Now()
		err := m.runOnce(ctx, gen)
		if ctx.Err() != nil || m.generation() != gen {
			return
		}

		m.mu.Lock()
		m.state = StateFailed
		if err != nil {
			m.errMsg = err.Error()
			m.logs.add("cloudflared stopped: " + err.Error())
		} else {
			m.errMsg = "cloudflared exited unexpectedly"
			m.logs.add("cloudflared exited")
		}
		m.restarts++
		m.mu.Unlock()

		if time.Since(started) > 2*time.Minute {
			backoff = 2 * time.Second // it ran fine for a while, retry quickly
		}
		m.mu.Lock()
		m.logs.add(fmt.Sprintf("restarting in %s", backoff))
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func (m *Manager) generation() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen
}

// Resolve returns the cloudflared to use: an explicit path, one found in PATH,
// or the copy Socrates manages - downloading it when it is not there yet.
func (m *Manager) Resolve(ctx context.Context) (string, error) {
	cfg := m.settings().Tunnel
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = "cloudflared"
	}
	// An explicit path or a binary on PATH always wins.
	if path, err := exec.LookPath(command); err == nil {
		return path, nil
	}
	// The configured command is not there. Remote access is supposed to just
	// work, so fall back to the copy Socrates manages instead of stopping -
	// and say so, in case the path was a typo the operator wants to fix.
	if command != "cloudflared" {
		m.mu.Lock()
		m.logs.add(command + " was not found, using the cloudflared Socrates manages instead")
		m.mu.Unlock()
	}
	if m.installer.Installed() {
		return m.installer.Path(), nil
	}
	if !Supported() {
		_, _, err := assetFor(runtime.GOOS, runtime.GOARCH)
		return "", err
	}

	m.mu.Lock()
	m.installing = true
	if m.state != StateStopped {
		m.state = StateInstalling
	}
	m.logs.add("cloudflared is not installed, fetching it")
	m.mu.Unlock()

	path, err := m.installer.Ensure(ctx, func(line string) {
		m.mu.Lock()
		m.logs.add(line)
		m.mu.Unlock()
	})

	m.mu.Lock()
	m.installing = false
	m.mu.Unlock()

	if err != nil {
		return "", err
	}
	return path, nil
}

// Install downloads cloudflared without starting a tunnel.
func (m *Manager) Install(ctx context.Context) (string, error) {
	return m.Resolve(ctx)
}

// Probe reports whether a usable cloudflared exists and which version it is.
func (m *Manager) Probe() (bool, string, string) {
	command := strings.TrimSpace(m.settings().Tunnel.Command)
	if command == "" {
		command = "cloudflared"
	}
	if path, err := exec.LookPath(command); err == nil {
		_, version := probe(path)
		return true, version, path
	}
	if m.installer.Installed() {
		_, version := probe(m.installer.Path())
		return true, version, m.installer.Path()
	}
	return false, "", ""
}

// runOnce runs cloudflared until it exits or the context is cancelled.
func (m *Manager) runOnce(ctx context.Context, gen int) error {
	cfg := m.settings().Tunnel
	binary, err := m.Resolve(ctx)
	if err != nil {
		return err
	}
	local := "http://127.0.0.1:8080"
	if m.localURL != nil {
		if v := m.localURL(); v != "" {
			local = v
		}
	}

	args := []string{"tunnel", "--no-autoupdate"}
	switch cfg.Mode {
	case config.TunnelToken:
		args = append(args, "run")
	default:
		args = append(args, "--url", local)
	}
	args = append(args, cfg.ExtraArgs...)

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = os.Environ()
	if cfg.Mode == config.TunnelToken {
		// The token goes through the environment so it never shows up in the
		// process list of the machine.
		cmd.Env = append(cmd.Env, "TUNNEL_TOKEN="+cfg.Token)
	}
	proc.Configure(cmd)
	cmd.Cancel = func() error { return proc.Terminate(cmd) }
	cmd.WaitDelay = 6 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	m.mu.Lock()
	m.state = StateStarting
	m.since = time.Now()
	m.logs.add(binary + " " + strings.Join(redact(args), " "))
	m.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.consume(stdout, gen) }()
	go func() { defer wg.Done(); m.consume(stderr, gen) }()

	// Nothing in the log tells us reliably that the tunnel is up across all
	// versions, so a process that simply keeps running counts as connected.
	ready := time.AfterFunc(readyAfter, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.gen == gen && m.state == StateStarting {
			m.state = StateRunning
		}
	})
	defer ready.Stop()

	// Drain the output before Wait closes the pipes underneath the readers.
	wg.Wait()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return fmt.Errorf("%s: %w", filepath.Base(binary), waitErr)
	}
	return nil
}

// consume reads cloudflared output, keeps a log tail and picks up the public URL.
func (m *Manager) consume(r io.Reader, gen int) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 32*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		m.mu.Lock()
		if m.gen != gen {
			m.mu.Unlock()
			return
		}
		m.logs.add(line)
		if match := quickURL.FindString(line); match != "" {
			m.url = match
			m.state = StateRunning
		}
		if strings.Contains(line, "Registered tunnel connection") {
			m.state = StateRunning
			if m.url == "" {
				m.url = m.settings().Tunnel.PublicURL()
			}
		}
		m.mu.Unlock()
	}
}

// probeCache keeps the result of "cloudflared --version" for a while: the admin
// dashboard polls the status regularly and spawning a process each time would
// be wasteful.
var probeCache sync.Map // command -> *probeResult

type probeResult struct {
	installed bool
	version   string
	at        time.Time
}

const probeTTL = 30 * time.Second

// invalidateProbe forgets a cached lookup, used right after an install.
func invalidateProbe(command string) { probeCache.Delete(command) }

// probe reports whether cloudflared is installed and which version it is.
func probe(command string) (bool, string) {
	if strings.TrimSpace(command) == "" {
		command = "cloudflared"
	}
	if cached, ok := probeCache.Load(command); ok {
		if result := cached.(*probeResult); time.Since(result.at) < probeTTL {
			return result.installed, result.version
		}
	}
	result := &probeResult{at: time.Now()}
	if path, err := exec.LookPath(command); err == nil {
		result.installed = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput(); err == nil {
			result.version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		}
	}
	probeCache.Store(command, result)
	return result.installed, result.version
}

// Probe is the exported form used by the setup wizard and diagnostics.
func Probe(command string) (bool, string) { return probe(command) }

// redact keeps secrets out of the log tail shown in the browser.
func redact(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			out = append(out, "***")
			skip = false
			continue
		}
		if a == "--token" {
			out = append(out, a)
			skip = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// ring is a fixed size log tail.
type ring struct {
	items []string
	size  int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) add(line string) {
	if len(line) > 500 {
		line = line[:500] + "…"
	}
	r.items = append(r.items, time.Now().Format("15:04:05")+"  "+line)
	if len(r.items) > r.size {
		r.items = r.items[len(r.items)-r.size:]
	}
}

func (r *ring) snapshot() []string {
	out := make([]string, len(r.items))
	copy(out, r.items)
	return out
}
