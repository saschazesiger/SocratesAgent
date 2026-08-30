package tunnel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// fakeCloudflared writes an executable stand-in for cloudflared.
func fakeCloudflared(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudflared")
	preamble := "#!/bin/sh\ncase \"$1\" in --version|-v|version) echo \"cloudflared version 2026.1.0\"; exit 0;; esac\n"
	if err := os.WriteFile(path, []byte(preamble+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newManager(t *testing.T, cfg config.TunnelSettings) *Manager {
	t.Helper()
	settings := config.Default()
	settings.Tunnel = cfg
	settings.Normalize()
	m := New(func() config.Settings { return settings }, func() string { return "http://127.0.0.1:9999" })
	t.Cleanup(m.Stop)
	return m
}

func waitFor(t *testing.T, m *Manager, want string, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var status Status
	for time.Now().Before(deadline) {
		status = m.Status()
		if status.State == want {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("tunnel never reached %q, last status: %#v", want, status)
	return status
}

func TestQuickTunnelPublishesURL(t *testing.T) {
	path := fakeCloudflared(t, `
echo "cloudflared args: $@" >&2
echo "|  https://demo-socrates-tunnel.trycloudflare.com  |" >&2
sleep 30
`)
	m := newManager(t, config.TunnelSettings{Enabled: true, Mode: config.TunnelQuick, Command: path})
	if err := m.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	status := waitFor(t, m, StateRunning, 5*time.Second)
	if status.URL != "https://demo-socrates-tunnel.trycloudflare.com" {
		t.Fatalf("url = %q", status.URL)
	}
	if !m.Running() {
		t.Error("Running() should report true")
	}
	logs := strings.Join(status.Logs, "\n")
	if !strings.Contains(logs, "--url http://127.0.0.1:9999") {
		t.Errorf("the local address should be published: %s", logs)
	}

	m.Stop()
	if m.Running() {
		t.Error("Running() should report false after Stop")
	}
	if state := m.Status().State; state != StateStopped {
		t.Errorf("state after stop = %q", state)
	}
}

func TestNamedTunnelUsesTokenFromEnvironment(t *testing.T) {
	path := fakeCloudflared(t, `
if [ -z "$TUNNEL_TOKEN" ]; then echo "no token in the environment" >&2; exit 3; fi
echo "args: $@" >&2
echo "INF Registered tunnel connection connIndex=0" >&2
sleep 30
`)
	m := newManager(t, config.TunnelSettings{
		Enabled: true, Mode: config.TunnelToken, Token: "secret-token",
		Hostname: "socrates.example.com", Command: path,
	})
	if err := m.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	status := waitFor(t, m, StateRunning, 5*time.Second)
	if status.URL != "https://socrates.example.com" {
		t.Fatalf("url = %q", status.URL)
	}
	logs := strings.Join(status.Logs, "\n")
	if strings.Contains(logs, "secret-token") {
		t.Error("the tunnel token must never end up in the log tail")
	}
	if !strings.Contains(logs, "tunnel --no-autoupdate run") {
		t.Errorf("unexpected command line: %s", logs)
	}
}

func TestTunnelRestartsAfterCrash(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	path := fakeCloudflared(t, `
echo x >> `+counter+`
echo "ERR something went wrong" >&2
exit 1
`)
	m := newManager(t, config.TunnelSettings{Enabled: true, Mode: config.TunnelQuick, Command: path})
	if err := m.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	status := waitFor(t, m, StateFailed, 5*time.Second)
	if status.Error == "" {
		t.Error("a crash should be reported")
	}

	// The supervisor waits two seconds before the first retry.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(counter)
		if err == nil && strings.Count(string(data), "x") >= 2 {
			if m.Status().Restarts < 1 {
				t.Errorf("restarts were not counted: %#v", m.Status())
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("cloudflared was never started again: %#v", m.Status())
}

func TestStartRejectsMissingBinaryAndToken(t *testing.T) {
	m := newManager(t, config.TunnelSettings{Enabled: true, Mode: config.TunnelQuick, Command: "cloudflared-does-not-exist"})
	if err := m.Start(); err == nil {
		t.Fatal("a missing binary should be reported")
	}

	path := fakeCloudflared(t, "sleep 5")
	m2 := New(func() config.Settings {
		s := config.Default()
		s.Tunnel = config.TunnelSettings{Enabled: true, Mode: config.TunnelToken, Command: path}
		return s
	}, func() string { return "http://127.0.0.1:9999" })
	t.Cleanup(m2.Stop)
	if err := m2.Start(); err == nil {
		t.Fatal("a named tunnel without a token should be reported")
	}
}

func TestStatusReportsInstallation(t *testing.T) {
	path := fakeCloudflared(t, `echo "cloudflared version 2026.1.0"`)
	m := newManager(t, config.TunnelSettings{Mode: config.TunnelQuick, Command: path})
	status := m.Status()
	if !status.Installed || !strings.Contains(status.Version, "2026.1.0") {
		t.Fatalf("status = %#v", status)
	}
	if status.State != StateStopped || status.LocalURL != "http://127.0.0.1:9999" {
		t.Fatalf("status = %#v", status)
	}
}

func TestRedactHidesToken(t *testing.T) {
	got := strings.Join(redact([]string{"tunnel", "run", "--token", "secret"}), " ")
	if strings.Contains(got, "secret") || !strings.Contains(got, "***") {
		t.Fatalf("redact produced %q", got)
	}
}
