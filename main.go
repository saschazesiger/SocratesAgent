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
	"strings"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/server"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

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
		}
	}

	fs := flag.NewFlagSet("socrates", flag.ExitOnError)
	addr := fs.String("addr", envOr("SOCRATES_ADDR", ":8080"), "address to listen on (use 127.0.0.1:8080 to keep it reachable from this machine only)")
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

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("could not create the data directory %s: %v", *dataDir, err)
	}
	dbPath := filepath.Join(*dataDir, "socrates.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("could not open the database: %v", err)
	}
	defer st.Close()

	srv, err := server.New(st, *dataDir)
	if err != nil {
		log.Fatalf("could not start: %v", err)
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

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("could not listen on %s: %v", *addr, err)
	}
	srv.SetLocalURL(localBaseURL(listener.Addr()))

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
		// No write timeout: a terminal connection stays open for a long time.
	}

	log.Printf("Socrates %s", version)
	log.Printf("data directory: %s", *dataDir)
	log.Printf("open %s in your browser", displayURL(listener.Addr()))
	if isLoopback(listener.Addr()) {
		log.Print("listening on loopback only - reachable from this machine, and through the Cloudflare tunnel in /admin")
	} else {
		log.Printf("listening on %s - reachable from the network. Anyone who gets past the password can run commands here; "+
			"pass -addr 127.0.0.1:8080 to keep it local and publish it through the Cloudflare tunnel instead", listener.Addr())
	}

	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	srv.StartTunnelIfEnabled()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	srv.StopTunnel()
	// The tmux server and every session on it are deliberately left running.
	stopSessions()
	srv.StopSessions()
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
