// Command socrates is a web dashboard for Claude Code, Codex and OpenCode,
// driven as chats through their own protocols, with a terminal for the
// shell, voice input and an admin dashboard.
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
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	// The chat adapters register themselves for the server and the hosts.
	_ "github.com/saschazesiger/SocratesAgent/internal/agent/claude"
	_ "github.com/saschazesiger/SocratesAgent/internal/agent/codex"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	// The OpenCode chat adapter registers itself with the agent host.
	_ "github.com/saschazesiger/SocratesAgent/internal/agent/opencode"
	"github.com/saschazesiger/SocratesAgent/internal/backup"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/sdnotify"
	"github.com/saschazesiger/SocratesAgent/internal/server"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// startupGrace is how long systemd's watchdog is fed before the listener
// serves, with nothing to prove it. A start opens the database, migrates it
// and reconciles with tmux, none of which takes minutes: a start that has not
// reached its listener in this time is not slow, it is stuck, and the
// watchdog restarting it is the way out.
const startupGrace = 15 * time.Minute

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
		case "restore":
			restoreCommand(args[1:])
			return
		case "agent-host":
			os.Exit(agenthost.Main(args[1:], version))
		case "mcp-ask":
			os.Exit(agenthost.MCPAskMain(args[1:], version))
		}
	}

	fs := flag.NewFlagSet("socrates", flag.ExitOnError)
	addr := fs.String("addr", envOr("SOCRATES_ADDR", "127.0.0.1:8080"), "address to listen on (use :8080 to make it reachable from the network)")
	dataDir := fs.String("data", config.DataDir(), "directory for the database and workspaces")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Socrates %s - a terminal harness for Shell, Claude Code, Codex and OpenCode\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n  socrates [flags]        start the web server\n"+
			"  socrates restore ...    restore a backup onto this machine (socrates restore -h)\n\nFlags:\n")
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
	// start is restarted like a crashed one. Its loop is not tied to life: a
	// shutdown is fed too, for a while.
	watchdog := sdnotify.NewWatchdog(startupGrace)
	watchdog.Start(context.Background())

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("could not create the data directory %s: %v", *dataDir, err)
	}
	unlock, err := lockDataDir(*dataDir)
	if err != nil {
		log.Fatalf("%v", err)
	}
	// A restore that was staged before this start - from the setup page,
	// which restarts the process into this - is moved into place before
	// anything opens the database it brings.
	if id, err := backup.ApplyPending(*dataDir); err != nil {
		log.Fatalf("could not finish the restore: %v", err)
	} else if id != "" {
		log.Printf("restore: snapshot %s is in place", id)
	}
	st, err := store.Open(paths.New(*dataDir).DB())
	if err != nil {
		log.Fatalf("could not open the database: %v", err)
	}
	// Once, on the first start of the build with accounts on a data directory
	// of the build before (the store marks it when it upgrades the
	// database): the agents' terminals go, and the agents' own folders and
	// the old profiles become accounts (REBUILD.md §8.1-8.3). A new data
	// directory has nothing to take over and never touches HOME. One that
	// stops half way is finished by the next start; until then the chats run
	// on what has been moved.
	migrateLogged(*dataDir, st, os.Environ())

	// life ends with SIGTERM, and with a finished restore, which is a stop
	// followed by a start of the same binary.
	life, stopLife := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopLife()
	life, restartForRestore := context.WithCancel(life)
	var restarting atomic.Bool

	srv, err := server.New(st, *dataDir)
	if err != nil {
		log.Fatalf("could not start: %v", err)
	}
	srv.SetRestart(func() {
		restarting.Store(true)
		restartForRestore()
	})
	// The backup runs beside the server and never in its way: a bucket that
	// is not configured or cannot be reached changes nothing here.
	backups, err := backup.New(backup.Options{
		DataDir: *dataDir, Version: version, Exclude: srv.BackupExclusions, WarnAfter: warnAfter(),
	})
	if err != nil {
		log.Printf("backup: %v; backups are off until it is fixed in Admin", err)
	} else {
		srv.SetBackup(backups)
		backups.Start(context.Background())
	}

	// The terminal substrate comes up before the listener accepts, so that the
	// very first session list is answered by a Socrates that has already
	// reconciled with tmux. A machine without a usable tmux still serves the
	// dashboard, which is where it says so.
	sessionCtx, stopSessions := context.WithCancel(context.Background())
	defer stopSessions()
	if err := srv.StartSessions(sessionCtx); err != nil {
		log.Printf("terminal sessions are unavailable: %v", err)
	}

	listener, err := listen(*addr)
	if err != nil {
		log.Fatalf("could not listen on %s: %v", *addr, err)
	}
	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
		// No write timeout: a terminal connection stays open for a long time.
	}
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()
	// From here a ping has to be earned: it is fed only while the server
	// answers its own health check.
	watchdog.Serving(sdnotify.HTTPProbe(listener.Addr(), "/api/health"))
	srv.SetLocalURL(localBaseURL(listener.Addr()))

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
	if restarting.Load() {
		// The data directory is about to be replaced by the restore: what
		// is here is not worth a backup, and must not become the newest
		// snapshot in the bucket it came from.
		_ = st.Close()
		unlock()
		log.Print("restore: starting again on the restored data directory")
		if err := reexec(); err != nil {
			log.Fatalf("the restore is staged, but Socrates could not restart itself (%v): start it again", err)
		}
		return
	}
	// Last, once nothing writes any more: one more backup, bounded, because
	// a bucket that does not answer must not hold the service's stop for
	// ever. What does not make it is backed up by the next start.
	if backups != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownBackup)
		if err := backups.Close(ctx); err != nil && !errors.Is(err, backup.ErrNotConfigured) && !errors.Is(err, backup.ErrNotInstalled) {
			log.Printf("backup: the last backup before the stop: %v", err)
		}
		cancel()
	}
	_ = st.Close()
	unlock()
}

// shutdownBackup bounds the backup a stop makes. socrates.service gives a
// stop 60 seconds in all.
const shutdownBackup = 40 * time.Second

// warnAfter is how long without a successful backup before the admin page
// and /api/state say so. SOCRATES_BACKUP_WARN_AFTER (a Go duration) moves
// it, for the browser suite; zero is the backup package's day.
func warnAfter() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("SOCRATES_BACKUP_WARN_AFTER")))
	if err != nil {
		return 0
	}
	return d
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
