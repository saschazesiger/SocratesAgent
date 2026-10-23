// Command socrates is a web terminal for Shell, Claude Code, Codex and
// OpenCode, with voice input and an admin dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/sdnotify"
	"github.com/saschazesiger/SocratesAgent/internal/server"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// startupGrace is how long systemd's watchdog is fed before the listener
// serves, with nothing to prove it. It is a few bucket connects long: a start
// that reaches neither the server nor the connect page in that time is not
// slow, it is stuck, and the watchdog restarting it is the way out.
const startupGrace = 3 * connectTimeout

// init pins the main goroutine to the main thread in an agent host. The host
// forks its CLI with a parent-death signal, which the kernel ties to the
// forking thread rather than the process, so the fork has to come from the
// one thread that lives exactly as long as the host (spike S4 §7).
func init() {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		runtime.LockOSThread()
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			args = args[1:]
		case "journal-sink":
			journalSink(args[1:])
			return
		case "tmux-hook":
			tmuxHook(args[1:])
			return
		case "agent-host":
			os.Exit(agenthost.Main(args[1:], version))
		}
	}

	fs := flag.NewFlagSet("socrates", flag.ExitOnError)
	addr := fs.String("addr", envOr("SOCRATES_ADDR", "127.0.0.1:8080"), "address to listen on (use :8080 to make it reachable from the network)")
	dataDir := fs.String("data", config.DataDir(), "directory for the database and workspaces")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Socrates %s - a terminal harness for Shell, Claude Code, Codex and OpenCode\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n  socrates [flags]        start the web server\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	// A word that is not a flag and not a subcommand is a typo, and flag
	// parsing simply stops at it: `socrates srve` used to start the server as
	// though nothing had been said, and so did `socrates serve --data x` with
	// the flag after a stray word.
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "socrates: %q is not a command or a flag.\n\n", fs.Arg(0))
		fs.Usage()
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println(version)
		return
	}

	server.Version = version

	// The watchdog comes first, before anything that can hang, so a hung
	// start is restarted like a crashed one. A re-exec starts here again in
	// the same process, so the new image takes over the pinging at once.
	// Its loop is not tied to life: a shutdown is fed too, for a while.
	watchdog := sdnotify.NewWatchdog(startupGrace)
	watchdog.Start(context.Background())

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("could not create the data directory %s: %v", *dataDir, err)
	}
	dbPath := filepath.Join(*dataDir, "socrates.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("could not open the database: %v", err)
	}
	defer st.Close()

	// One context for the life of the process: SIGTERM ends it, whether the
	// server got as far as serving sessions or is still waiting on the
	// connect page.
	life, stopLife := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopLife()

	// The listener is opened late on an ordinary start - after the bucket
	// has been read and the sessions reconciled, so the first request is
	// answered by a server that knows the whole state - and early when the
	// bucket cannot be used, because then the connect page is the only
	// thing there is to serve. front is the handler the listener serves in
	// both cases; it is switched from the connect page to the server once
	// the storage is there.
	front := &frontHandler{}
	httpServer := &http.Server{
		Handler:           front,
		ReadHeaderTimeout: 20 * time.Second,
		// No write timeout: a terminal connection stays open for a long time.
	}
	var listener net.Listener
	serve := func() {
		ln, err := listen(*addr)
		if err != nil {
			log.Fatalf("could not listen on %s: %v", *addr, err)
		}
		listener = ln
		go func() {
			if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("server error: %v", err)
			}
		}()
		// From here a ping has to be earned: the connect page answers
		// /api/health as the server does, so the probe is the same in
		// both, and the switch from one to the other is not a gap.
		watchdog.Serving(sdnotify.HTTPProbe(ln.Addr(), "/api/health"))
	}

	// The bucket comes first: it is the store of record, and the database
	// the server is about to read has to have been made equal to it.
	m := openStorage(life, st, *dataDir, front, serve)
	if m == nil {
		log.Print("shutting down")
		watchdog.Stopping()
		shutdownHTTP(httpServer)
		return
	}

	srv, err := server.New(st, *dataDir)
	if err != nil {
		log.Fatalf("could not start: %v", err)
	}
	srv.SetMirror(m)
	m.Start(context.Background())

	// The terminal substrate comes up before the listener accepts, so that the
	// very first session list is answered by a Socrates that has already
	// reconciled with tmux. A machine without a usable tmux still serves the
	// dashboard, which is where it says so.
	sessionCtx, stopSessions := context.WithCancel(context.Background())
	defer stopSessions()
	if err := srv.StartSessions(sessionCtx); err != nil {
		log.Printf("terminal sessions are unavailable: %v", err)
	}

	if listener == nil {
		serve()
	}
	srv.SetLocalURL(localBaseURL(listener.Addr()))
	front.set(srv.Handler())

	log.Printf("Socrates %s", version)
	log.Printf("data directory: %s", *dataDir)
	log.Printf("open %s in your browser", displayURL(listener.Addr()))
	if isLoopback(listener.Addr()) {
		log.Print("listening on loopback only - reachable from this machine, and through the Cloudflare tunnel in /admin")
	} else {
		log.Printf("listening on %s - reachable from the network. Anyone who gets past the password can run commands here; "+
			"set SOCRATES_ADDR=127.0.0.1:8080 to keep it local and publish it through the Cloudflare tunnel instead", listener.Addr())
	}

	srv.StartTunnelIfEnabled()

	<-life.Done()
	log.Print("shutting down")
	watchdog.Stopping()
	srv.StopTunnel()
	// The tmux server and every session on it are deliberately left running.
	stopSessions()
	srv.StopSessions()
	shutdownHTTP(httpServer)
	// Last, once nothing can write any more: what is still pending goes up,
	// and the lease is let go so the next start - here or elsewhere - takes
	// it at once. Bounded, because a bucket that does not answer must not
	// hold the service's stop for ever; what does not make it stays pending
	// in the database for the next start.
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFlush()
	if err := m.Close(flushCtx); err != nil {
		log.Printf("storage: %v", err)
	}
}

func shutdownHTTP(httpServer *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

// journalSink is the sink tmux pipes a pane's output into. It is a subcommand
// rather than `cat >> file` because a terminal user interface that redraws
// continuously writes megabytes an hour, and the session it writes for is
// never restarted: rotation has to happen while it runs or it never happens.
func journalSink(args []string) {
	fs := flag.NewFlagSet("journal-sink", flag.ExitOnError)
	path := fs.String("path", "", "file to append the pane output to")
	maxBytes := fs.Int64("max-bytes", termux.JournalMaxBytes, "rotate once the file passes this size")
	keep := fs.Int("keep", termux.JournalKeep, "how many rotated files to keep")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "socrates journal-sink: -path is required")
		os.Exit(2)
	}
	if err := termux.RunJournalSink(*path, *maxBytes, *keep); err != nil {
		fmt.Fprintf(os.Stderr, "socrates journal-sink: %v\n", err)
		os.Exit(1)
	}
}

// tmuxHook carries one tmux event to the running Socrates. It is deliberately
// silent and best effort: the server polls as well, so a hook that never
// arrives costs a couple of seconds and never correctness.
func tmuxHook(args []string) {
	fs := flag.NewFlagSet("tmux-hook", flag.ExitOnError)
	sock := fs.String("sock", "", "the Socrates hook socket")
	event := fs.String("event", "", "the tmux hook that fired")
	session := fs.String("session", "", "the tmux session it fired for")
	status := fs.String("status", "", "the exit status, for pane-died")
	signal := fs.String("signal", "", "the signal that killed the pane, when it was killed")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *sock == "" || *event == "" {
		os.Exit(2)
	}
	_ = termux.SendHook(*sock, termux.Hook{
		Event: *event, Session: *session, Status: *status, Signal: *signal,
	})
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// localBaseURL is the loopback address of this server. The Cloudflare tunnel
// publishes it, so it stays on 127.0.0.1 even when the listener is bound to
// every interface.
func localBaseURL(addr net.Addr) string {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		port = "8080"
	}
	return fmt.Sprintf("http://127.0.0.1:%s", port)
}

// isLoopback reports whether the listener only accepts local connections.
func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func displayURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "localhost"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}
