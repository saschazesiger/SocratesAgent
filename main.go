// Command socrates is a web harness for Claude Code, Codex and OpenCode
// with a web interface, voice mode and an admin dashboard.
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
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"

	// The adapters register themselves, and both roles - the web server and an
	// agent host - need the registry filled before they look anything up.
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/claude"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/codex"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/opencode"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	// "socrates agent-host" owns a single agent session. Socrates starts it
	// detached, so a turn in flight - the agent and everything it spawned -
	// keeps running even while the web server is restarted.
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		hostFlags := flag.NewFlagSet("agent-host", flag.ExitOnError)
		dir := hostFlags.String("dir", "", "host directory")
		if err := hostFlags.Parse(os.Args[2:]); err != nil {
			os.Exit(2)
		}
		if err := agenthost.RunHost(*dir); err != nil {
			log.Printf("agent host: %v", err)
			os.Exit(1)
		}
		return
	}

	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("socrates", flag.ExitOnError)
	addr := fs.String("addr", envOr("SOCRATES_ADDR", ":8080"), "address to listen on (use 127.0.0.1:8080 to keep it reachable from this machine only)")
	dataDir := fs.String("data", config.DataDir(), "directory for the database and workspaces")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Socrates %s - a web harness for Claude Code, Codex and OpenCode\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n  socrates [flags]        start the web server\n  socrates agent-host     internal: hosts one agent session\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
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

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("could not listen on %s: %v", *addr, err)
	}
	srv.SetLocalURL(localBaseURL(listener.Addr()))

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
		// No write timeout: SSE streams and delegate runs stay open for a long time.
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
	// A user who upgrades in place still has term-host processes from the
	// previous version running interactive agent TUIs. Nothing in the new
	// Manager ever visits that directory, so they would keep running forever;
	// this sweeps them once and then the directory is gone.
	agenthost.SweepLegacyTerminals(*dataDir)
	// Agent hosts come back before the runs are recovered, so a turn that is
	// genuinely still running is adopted rather than marked interrupted.
	adopted := srv.ResumeAgents()
	if err := st.RecoverRuns(adopted...); err != nil {
		log.Printf("warning: could not clean up unfinished runs: %v", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	// The agent hosts keep running: whatever an agent is in the middle of is
	// still there when Socrates comes back.
	srv.DetachAgents()
	srv.StopTunnel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
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
