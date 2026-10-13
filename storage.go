package main

// Starting from the bucket.
//
// S3 is the store of record and it is mandatory: a Socrates that has no bucket
// it can use serves the connect page and nothing else. openStorage is the
// decision between the two, and it runs before the server is built:
//
//  1. No configuration (no s3.json, no SOCRATES_S3_BUCKET): connect mode. The
//     setup page's storage step connects a bucket, writes s3.json and
//     restarts the process into step 2.
//  2. A configuration: mirror.Connect reads meta.json (creating it for a new
//     place), proves the passphrase, takes the lease and hydrates the local
//     database. On success the server starts as it always has.
//  3. The bucket cannot be reached, and this data directory was synchronised
//     with exactly this bucket before: start DEGRADED from the local copy.
//     Writes queue up as pending and go up once the bucket answers.
//  4. Anything else - unreachable with no copy to start from, in use by
//     another Socrates, a wrong passphrase, access denied: connect mode with
//     the reason, retrying on its own (the configuration is read again before
//     every attempt, so a hand-fixed s3.json is picked up) until it works, a
//     person takes the bucket over, or the process is stopped. A configured
//     server never offers to connect a different bucket on that page.

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mirror"
	"github.com/saschazesiger/SocratesAgent/internal/server"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// connectTimeout bounds one start from the bucket: the meta, the lease and a
// full hydrate of a large installation.
const connectTimeout = 5 * time.Minute

// newBucketIterations is the PBKDF2 count a new bucket is created with; zero
// is the vault default. Tests lower it, because 600 000 rounds per start is a
// second they spend on nothing.
var newBucketIterations = 0

// openStorage returns the mirror the server runs on, serving the connect page
// for as long as it takes to get one. It returns nil only when life ends
// first. A connect or takeover made on the page does not return at all: it
// restarts the process.
func openStorage(life context.Context, st *store.Store, dataDir string, front *frontHandler, serve func()) *mirror.Mirror {
	instance, err := mirror.InstanceID(st)
	if err != nil {
		log.Fatalf("could not read this data directory's instance id: %v", err)
	}
	opts := mirror.Options{Instance: instance, Version: version, DataDir: dataDir, Iterations: newBucketIterations}

	cfg, src, cfgErr := mirror.LoadConfig(dataDir)
	opts.Source = src
	var state server.ConnectState
	switch {
	case cfgErr != nil:
		state = server.ConnectState{Reason: "config", Error: cfgErr.Error(), Configured: true, Source: src}
	case src == mirror.SourceNone:
		state = server.ConnectState{Reason: "unconfigured"}
	default:
		m, err := startMirror(life, st, cfg, opts)
		if m != nil {
			return m
		}
		state = server.StateFor(cfg, src, err)
	}
	if life.Err() != nil {
		return nil
	}
	reportConnectMode(state)

	retry := make(chan struct{}, 1)
	cs := server.NewConnect(server.ConnectOptions{
		Store: st, DataDir: dataDir, Mirror: opts, Config: cfg,
		Restart: func() { restart(st) },
		Retry: func() {
			select {
			case retry <- struct{}{}:
			default:
			}
		},
	}, state)
	front.set(cs.Handler())
	serve()

	for {
		if !state.Configured {
			// Nothing to retry: the page connects a bucket, and that
			// restarts the process.
			<-life.Done()
			return nil
		}
		wait := retryDelay(state.Reason)
		state.Retrying, state.NextRetryAt = false, time.Now().Add(wait).UnixMilli()
		cs.SetState(state)
		timer := time.NewTimer(wait)
		select {
		case <-life.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		case <-retry:
			timer.Stop()
		}

		next, nextSrc, err := mirror.LoadConfig(dataDir)
		switch {
		case err != nil:
			state = server.ConnectState{Reason: "config", Error: err.Error(), Configured: true, Source: nextSrc}
			continue
		case nextSrc == mirror.SourceNone:
			state = server.ConnectState{Reason: "unconfigured"}
			cs.SetState(state)
			continue
		}
		cfg, opts.Source = next, nextSrc
		cs.SetConfig(cfg)
		// One attempt at a time, shared with the page: a takeover the page
		// is running takes the lease by force and then waits for the old
		// holder's last uploads, and an attempt from here in that wait would
		// find the lease "its own" and hydrate before they landed. After the
		// page has committed a connect or takeover, the restart is coming
		// and nothing starts here any more.
		if !cs.BeginAttempt() {
			state.Retrying = false
			continue
		}
		state.Retrying, state.NextRetryAt = true, 0
		cs.SetState(state)
		m, err := startMirror(life, st, cfg, opts)
		if m != nil {
			// The attempt is kept for good: a takeover that arrives now,
			// before main has switched the listener to the server, must
			// not restart the process under the server being built.
			log.Print("storage: the bucket is usable now; starting")
			return m
		}
		cs.EndAttempt()
		if life.Err() != nil {
			return nil
		}
		state = server.StateFor(cfg, nextSrc, err)
		log.Printf("storage: still waiting: %s", state.Error)
	}
}

// startMirror is one attempt: connect, or start degraded when the bucket is
// unreachable and the local copy belongs to it.
func startMirror(life context.Context, st *store.Store, cfg mirror.Config, opts mirror.Options) (*mirror.Mirror, error) {
	ctx, cancel := context.WithTimeout(life, connectTimeout)
	defer cancel()
	m, err := mirror.Connect(ctx, st, cfg, opts)
	if err == nil {
		return m, nil
	}
	if mirror.Transient(err) && mirror.CanDegrade(st, cfg) {
		dm, derr := mirror.Degraded(st, cfg, opts, err)
		if derr == nil {
			return dm, nil
		}
		log.Printf("storage: could not start from the local copy either: %v", derr)
	}
	return nil, err
}

// retryDelay is how long connect mode waits before trying the bucket again.
// An unreachable store and a lease that may expire are worth asking often;
// a refusal will not change by itself, but s3.json may be fixed by hand.
func retryDelay(reason string) time.Duration {
	switch reason {
	case "unreachable":
		return 15 * time.Second
	case "leased":
		return 20 * time.Second
	}
	return time.Minute
}

// reportConnectMode says in the log why the server is not starting, and how
// to get it going.
func reportConnectMode(state server.ConnectState) {
	switch {
	case state.Reason == "unconfigured":
		log.Print("storage: no bucket is configured. Open the setup page to connect one, " +
			"or set SOCRATES_S3_BUCKET and the other SOCRATES_S3_* variables. Only the connect page is served until then.")
	case state.Reason == "wrong_passphrase" && state.Source == mirror.SourceEnv:
		log.Print("storage: ERROR: SOCRATES_S3_PASSPHRASE does not open this bucket. Nothing can start until the " +
			"environment carries the right passphrase; the connect page says the same.")
	default:
		log.Printf("storage: cannot start from the bucket (%s): %s. Serving the connect page and retrying.", state.Reason, state.Error)
	}
}

// restart replaces this process with a fresh start of the same binary, after
// the connect page wrote a new configuration. The database is closed first so
// the new process opens it cleanly; the listener closes on exec by itself.
func restart(st *store.Store) {
	_ = st.Close()
	if err := reexec(); err != nil {
		log.Fatalf("storage is connected, but Socrates could not restart itself (%v): start it again", err)
	}
}

// executable is the binary to start again: the running one, or - when it has
// been replaced on disk since, which an upgrade does - whatever the command
// line names.
func executable() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}
	return exec.LookPath(os.Args[0])
}

// listen opens the listener, trying again for a moment: a restart on a
// platform without exec starts the new process while the old one is still
// letting go of the port.
func listen(addr string) (net.Listener, error) {
	var err error
	for i := 0; i < 10; i++ {
		var ln net.Listener
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, err
}

// frontHandler is the listener's handler, switched from the connect page to
// the server once there is one.
type frontHandler struct{ h atomic.Pointer[http.Handler] }

func (f *frontHandler) set(h http.Handler) { f.h.Store(&h) }

func (f *frontHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := f.h.Load(); h != nil {
		(*h).ServeHTTP(w, r)
		return
	}
	http.Error(w, "Socrates is starting", http.StatusServiceUnavailable)
}
