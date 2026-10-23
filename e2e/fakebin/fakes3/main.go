// fakes3 is the bucket the browser suite backs up into.
//
// A real bucket would make the suite depend on the network, on credentials and
// on somebody cleaning up after it, so the harness starts this program the
// first time a scenario wants a bucket - the backup and restore scenarios -
// and hands each of them a prefix of its own on it.
//
// It is internal/s3/s3fake - the same in-memory store the backup's unit tests
// use, with real SigV4 verification - served on a real port. Everything lives
// in memory and dies with the process: a run's state is meant to be thrown
// away, and a store that outlived the run would only be one more thing that
// can leak into the next.
//
// One thing a store never offers is a way to break it on purpose, and that is
// what the backup scenario needs: /_fakes3/down?status=503 makes every S3
// request fail with that status until /_fakes3/up. These two paths are not
// signed - nothing about the fake is secret - and cannot collide with S3,
// because a bucket name never starts with an underscore.
//
// It prints exactly one line on stdout once it is listening,
//
//	fakes3 listening on http://127.0.0.1:<port>
//
// which is how the harness learns the port when it asked for :0.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("fakes3: %v", err)
	}
	// The default credentials and region: the harness reads them from the
	// same constants' values, and nothing about a fake is secret.
	fake := s3fake.New(s3fake.Options{})
	fmt.Printf("fakes3 listening on http://%s\n", ln.Addr())

	// The harness kills the process at the end of a run; a signal is the
	// normal way out, not an error.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		os.Exit(0)
	}()
	if err := http.Serve(ln, control(fake)); err != nil {
		log.Fatalf("fakes3: %v", err)
	}
}

// control answers the two switches and hands everything else to the fake.
func control(fake *s3fake.Fake) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/_fakes3/") {
			fake.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/_fakes3/down":
			status, err := strconv.Atoi(r.URL.Query().Get("status"))
			if err != nil || status < 400 || status > 599 {
				status = http.StatusServiceUnavailable
			}
			fake.SetDown(status)
		case "/_fakes3/up":
			fake.SetDown(0)
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
