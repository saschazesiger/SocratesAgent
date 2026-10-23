package sdnotify

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Phase is how far the process has come, which decides what a ping has to
// prove. systemd's watchdog only sees pings; what they mean is up to us.
type Phase int

const (
	// Starting is everything before the listener serves: opening the
	// database, reaching the bucket, reconciling the sessions. There is
	// nothing to probe yet, so the ping is unconditional - but only for the
	// startup grace, so a start that hangs for ever is restarted too.
	Starting Phase = iota
	// Serving is a listener that accepts, whether it serves the server or
	// the connect page. A ping now means the process answered its own
	// health check, which is what "not hung" means for a web server.
	Serving
	// Stopping is the shutdown. Pings carry on for a bounded while, so a
	// slow but orderly stop is not killed half way through its flush.
	Stopping
)

// Probe asks the running server whether it answers. nil is healthy.
type Probe func(ctx context.Context) error

// shouldPing is the whole policy: given the phase, how long the process has
// been in it and - while serving - what the probe said, is it alive?
func shouldPing(phase Phase, inPhase time.Duration, probeErr error, startupGrace, stopGrace time.Duration) bool {
	switch phase {
	case Starting:
		return inPhase < startupGrace
	case Serving:
		return probeErr == nil
	case Stopping:
		return inPhase < stopGrace
	}
	return false
}

// Watchdog keeps systemd's watchdog fed for as long as the process is doing
// its job, and stops feeding it when it is not, so that systemd restarts a
// Socrates that hangs rather than only one that crashes. It also sends
// READY=1 and STOPPING=1 at the matching moments, with or without a watchdog.
type Watchdog struct {
	// StartupGrace bounds the Starting phase, measured from NewWatchdog.
	StartupGrace time.Duration
	// StopGrace bounds the Stopping phase.
	StopGrace time.Duration
	// ProbeTimeout bounds one health probe; a server that takes longer to
	// say it is healthy is not.
	ProbeTimeout time.Duration

	notify func(string) error
	now    func() time.Time
	logf   func(format string, args ...any)

	mu      sync.Mutex
	phase   Phase
	since   time.Time
	probe   Probe
	pinging bool // what the last tick decided, so the log says only changes

	notifyFailed bool // the loop's own: a socket that is gone is said once
}

// NewWatchdog returns a watchdog in the Starting phase, its startup grace
// counted from now.
func NewWatchdog(startupGrace time.Duration) *Watchdog {
	w := &Watchdog{
		StartupGrace: startupGrace,
		StopGrace:    time.Minute,
		ProbeTimeout: 10 * time.Second,
		notify:       Notify,
		now:          time.Now,
		logf:         log.Printf,
		pinging:      true,
	}
	w.since = w.now()
	return w
}

// Start runs the watchdog loop in the background when systemd asked for a
// watchdog, and reports whether it did. Pings go out at a third of the
// timeout, so one slow probe is never the difference.
func (w *Watchdog) Start(ctx context.Context) bool {
	timeout, ok := WatchdogInterval()
	if !ok {
		return false
	}
	go w.run(ctx, max(timeout/3, time.Millisecond))
	return true
}

func (w *Watchdog) run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Serving moves to the Serving phase, with the probe that proves it, and
// tells systemd the service is ready.
func (w *Watchdog) Serving(probe Probe) {
	w.mu.Lock()
	w.phase, w.since, w.probe, w.pinging = Serving, w.now(), probe, true
	w.mu.Unlock()
	if err := w.notify("READY=1"); err != nil {
		w.logf("systemd: could not report ready: %v", err)
	}
}

// Stopping moves to the Stopping phase and tells systemd the service is on
// its way down.
func (w *Watchdog) Stopping() {
	w.mu.Lock()
	w.phase, w.since, w.pinging = Stopping, w.now(), true
	w.mu.Unlock()
	if err := w.notify("STOPPING=1"); err != nil {
		w.logf("systemd: could not report stopping: %v", err)
	}
}

// tick is one round: probe when there is something to probe, decide, ping.
func (w *Watchdog) tick(ctx context.Context) {
	w.mu.Lock()
	phase, since, probe := w.phase, w.since, w.probe
	w.mu.Unlock()

	var probeErr error
	if phase == Serving && probe != nil {
		pctx, cancel := context.WithTimeout(ctx, w.ProbeTimeout)
		probeErr = probe(pctx)
		cancel()
	}
	// The clock is read after the probe, which may have taken a while.
	inPhase := w.now().Sub(since)
	ping := shouldPing(phase, inPhase, probeErr, w.StartupGrace, w.StopGrace)
	if ping {
		err := w.notify("WATCHDOG=1")
		if err != nil && !w.notifyFailed {
			w.logf("systemd: could not ping the watchdog: %v", err)
		}
		w.notifyFailed = err != nil
	}

	// A phase that changed while this tick ran starts its own account.
	w.mu.Lock()
	changed := ping != w.pinging && w.phase == phase
	if changed {
		w.pinging = ping
	}
	w.mu.Unlock()
	if !changed {
		return
	}
	switch {
	case ping:
		w.logf("systemd: the health check answers again; pinging the watchdog")
	case phase == Starting:
		w.logf("systemd: still not serving after %s; no longer pinging the watchdog, so systemd restarts Socrates", w.StartupGrace)
	case phase == Serving:
		w.logf("systemd: the health check failed (%v); not pinging the watchdog until it answers", probeErr)
	case phase == Stopping:
		w.logf("systemd: the shutdown has taken more than %s; no longer pinging the watchdog", w.StopGrace)
	}
}

// HTTPProbe is a probe that GETs path from the server listening on addr,
// over a fresh connection every time, so that it proves the accept loop and
// a handler both still run. A wildcard address is asked on loopback.
func HTTPProbe(addr net.Addr, path string) Probe {
	network, target, host := probeTarget(addr)
	client := &http.Client{
		Transport: &http.Transport{
			// The listener's own address whatever the URL says, and never
			// through a proxy the environment may name.
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, target)
			},
			Proxy:             nil,
			DisableKeepAlives: true,
		},
		// The health check answers itself; a redirect would mean something
		// else is answering.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	u := (&url.URL{Scheme: "http", Host: host, Path: path}).String()
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%s answered %s", path, res.Status)
		}
		return nil
	}
}

// probeTarget is where a probe of the listener on addr connects, and the
// host it names in the request. A TCP listener on every interface is asked
// on the loopback address of its own family; anything else - a specific
// address, a unix socket - is asked where it is.
func probeTarget(addr net.Addr) (network, target, host string) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return addr.Network(), addr.String(), "localhost"
	}
	ip, zone := tcp.IP, tcp.Zone
	switch {
	case len(ip) == 0 || ip.Equal(net.IPv4zero):
		ip, zone = net.IPv4(127, 0, 0, 1), ""
	case ip.IsUnspecified():
		ip, zone = net.IPv6loopback, ""
	}
	hostport := (&net.TCPAddr{IP: ip, Port: tcp.Port, Zone: zone}).String()
	return "tcp", hostport, hostport
}
