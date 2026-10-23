package sdnotify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShouldPing(t *testing.T) {
	const startup, stop = 15 * time.Minute, time.Minute
	failed := errors.New("no answer")
	cases := []struct {
		name    string
		phase   Phase
		inPhase time.Duration
		probe   error
		want    bool
	}{
		{"starting, early", Starting, time.Second, nil, true},
		{"starting, inside the grace", Starting, 14 * time.Minute, nil, true},
		{"starting for ever", Starting, 15 * time.Minute, nil, false},
		{"serving and healthy", Serving, 3 * time.Hour, nil, true},
		{"serving, probe failed", Serving, time.Second, failed, false},
		{"stopping, early", Stopping, 10 * time.Second, nil, true},
		{"stopping for ever", Stopping, 2 * time.Minute, nil, false},
	}
	for _, c := range cases {
		if got := shouldPing(c.phase, c.inPhase, c.probe, startup, stop); got != c.want {
			t.Errorf("%s: shouldPing = %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeWatchdog is a Watchdog on a clock the test moves, recording what it
// would have told systemd and what it logged.
type fakeWatchdog struct {
	*Watchdog
	mu    sync.Mutex
	clock time.Time
	sent  []string
	logs  []string
}

func newFakeWatchdog() *fakeWatchdog {
	f := &fakeWatchdog{clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	w := NewWatchdog(15 * time.Minute)
	w.now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }
	w.notify = func(s string) error { f.mu.Lock(); defer f.mu.Unlock(); f.sent = append(f.sent, s); return nil }
	w.logf = func(format string, args ...any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logs = append(f.logs, fmt.Sprintf(format, args...))
	}
	w.since = w.now()
	f.Watchdog = w
	return f
}

func (f *fakeWatchdog) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

// take returns what was sent since the last call.
func (f *fakeWatchdog) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := f.sent
	f.sent = nil
	return sent
}

func (f *fakeWatchdog) logged() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.logs, "\n")
}

func TestWatchdogStartupGraceRunsOut(t *testing.T) {
	f := newFakeWatchdog()
	ctx := context.Background()

	f.tick(ctx)
	if got := f.take(); len(got) != 1 || got[0] != "WATCHDOG=1" {
		t.Fatalf("first tick sent %q, want a ping", got)
	}
	f.advance(14 * time.Minute)
	f.tick(ctx)
	if got := f.take(); len(got) != 1 {
		t.Fatalf("inside the startup grace sent %q, want a ping", got)
	}
	f.advance(2 * time.Minute)
	f.tick(ctx)
	f.tick(ctx)
	if got := f.take(); len(got) != 0 {
		t.Fatalf("after the startup grace sent %q, want nothing", got)
	}
	if n := strings.Count(f.logged(), "still not serving"); n != 1 {
		t.Fatalf("the stuck start was logged %d times, want once:\n%s", n, f.logged())
	}
}

func TestWatchdogServingPingsOnlyWhenTheProbeAnswers(t *testing.T) {
	f := newFakeWatchdog()
	ctx := context.Background()
	var healthy bool
	var probed int
	// Serving long after the startup grace would have run out: serving is
	// judged by the probe alone.
	f.advance(time.Hour)
	f.Serving(func(ctx context.Context) error {
		probed++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the probe has no deadline")
		}
		if healthy {
			return nil
		}
		return errors.New("connection refused")
	})
	if got := f.take(); len(got) != 1 || got[0] != "READY=1" {
		t.Fatalf("Serving sent %q, want READY=1", got)
	}

	f.tick(ctx)
	f.tick(ctx)
	if got := f.take(); len(got) != 0 {
		t.Fatalf("a failing probe still sent %q", got)
	}
	if !strings.Contains(f.logged(), "connection refused") {
		t.Fatalf("the failed probe was not logged:\n%s", f.logged())
	}
	healthy = true
	f.tick(ctx)
	if got := f.take(); len(got) != 1 || got[0] != "WATCHDOG=1" {
		t.Fatalf("a healthy probe sent %q, want a ping", got)
	}
	if probed != 3 {
		t.Fatalf("probed %d times, want 3", probed)
	}
	if n := strings.Count(f.logged(), "systemd:"); n != 2 {
		t.Fatalf("logged %d lines, want the failure and the recovery:\n%s", n, f.logged())
	}
}

func TestWatchdogStoppingIsBounded(t *testing.T) {
	f := newFakeWatchdog()
	ctx := context.Background()
	f.Serving(func(context.Context) error { return errors.New("closed") })
	f.Stopping()
	if got := f.take(); len(got) != 2 || got[1] != "STOPPING=1" {
		t.Fatalf("sent %q, want READY=1 then STOPPING=1", got)
	}
	// The listener is closed by now, but a stop is not judged by it.
	f.tick(ctx)
	if got := f.take(); len(got) != 1 {
		t.Fatalf("an early stop sent %q, want a ping", got)
	}
	f.advance(2 * time.Minute)
	f.tick(ctx)
	if got := f.take(); len(got) != 0 {
		t.Fatalf("a stop past its grace sent %q, want nothing", got)
	}
}

// TestWatchdogStartPings runs the real loop against a real socket.
func TestWatchdogStartPings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn := listenNotify(t, path)
	t.Setenv("NOTIFY_SOCKET", path)
	t.Setenv("WATCHDOG_USEC", "30000") // pings every 10ms
	t.Setenv("WATCHDOG_PID", fmt.Sprint(os.Getpid()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !NewWatchdog(time.Minute).Start(ctx) {
		t.Fatal("Start did not start with a watchdog configured")
	}
	for i := 0; i < 3; i++ {
		if got := receive(t, conn); got != "WATCHDOG=1" {
			t.Fatalf("received %q, want WATCHDOG=1", got)
		}
	}
}

func TestWatchdogStartWithoutSystemd(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	if NewWatchdog(time.Minute).Start(context.Background()) {
		t.Fatal("Start started without WATCHDOG_USEC")
	}
}

func TestProbeTarget(t *testing.T) {
	tcp := func(s string) net.Addr {
		a, err := net.ResolveTCPAddr("tcp", s)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cases := []struct {
		addr    net.Addr
		network string
		target  string
	}{
		{tcp("0.0.0.0:8080"), "tcp", "127.0.0.1:8080"},
		{tcp(":8080"), "tcp", "127.0.0.1:8080"},
		{tcp("[::]:8080"), "tcp", "[::1]:8080"},
		{tcp("127.0.0.1:9000"), "tcp", "127.0.0.1:9000"},
		{tcp("[::1]:9000"), "tcp", "[::1]:9000"},
		{tcp("192.0.2.7:8080"), "tcp", "192.0.2.7:8080"},
		{&net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 80, Zone: "eth0"}, "tcp", "[fe80::1%eth0]:80"},
		{&net.UnixAddr{Name: "/run/socrates.sock", Net: "unix"}, "unix", "/run/socrates.sock"},
	}
	for _, c := range cases {
		network, target, _ := probeTarget(c.addr)
		if network != c.network || target != c.target {
			t.Errorf("probeTarget(%v) = %s %s, want %s %s", c.addr, network, target, c.network, c.target)
		}
	}
}

func TestHTTPProbe(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	// On every interface, as SOCRATES_ADDR=:8080 has it: the probe has to
	// find its way to loopback.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	ctx := context.Background()
	probe := HTTPProbe(ln.Addr(), "/api/health")
	if err := probe(ctx); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	healthy.Store(false)
	if err := probe(ctx); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unhealthy server: %v, want a 503", err)
	}

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := HTTPProbe(ln.Addr(), "/slow")(short); err == nil {
		t.Fatal("a server that does not answer passed the probe")
	}

	addr := ln.Addr()
	srv.Close()
	if err := HTTPProbe(addr, "/api/health")(ctx); err == nil {
		t.Fatal("a closed listener passed the probe")
	}
}

func TestHTTPProbeOverUnixSocket(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "http.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	if err := HTTPProbe(ln.Addr(), "/api/health")(context.Background()); err != nil {
		t.Fatalf("unix socket probe: %v", err)
	}
}
