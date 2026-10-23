//go:build !windows

package opencode

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"
)

var lookPath = exec.LookPath

// The process's clock.
const (
	// listenWait covers a cold start: the binary loads its config and its
	// providers before it binds (about 2 s warm, research/06 §3.5).
	listenWait = 60 * time.Second
	// stopGrace is how long SIGTERM gets before SIGKILL. A stop that catches
	// OpenCode's first-start npm install can hang for minutes otherwise.
	stopGrace = 5 * time.Second
)

// listening is the line `opencode serve` prints once it is up. With --port 0
// it tries 4096 first and then any free port, so this line is the only place
// the port is known.
var listening = regexp.MustCompile(`listening on (http://127\.0\.0\.1:[0-9]+)`)

// server is one `opencode serve`.
type server struct {
	cmd  *exec.Cmd
	base string
	pass string
	out  *ring
	done chan struct{}
	err  error
}

// startServer starts `opencode serve` in dir with env (without the password,
// which it adds) through spawn, and returns once the server listens.
func startServer(ctx context.Context, spawn func(*exec.Cmd) error, bin, dir string, env func(password string) []string) (*server, error) {
	pass, err := newPassword()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "serve", "--hostname", "127.0.0.1", "--port", "0")
	cmd.Dir = dir
	cmd.Env = env(pass)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &server{cmd: cmd, pass: pass, out: &ring{max: 8 << 10}, done: make(chan struct{})}
	cmd.Stderr = s.out
	if err := spawn(cmd); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			_, _ = s.out.Write([]byte(line + "\n"))
			if m := listening.FindStringSubmatch(line); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	wait, cancel := context.WithTimeout(ctx, listenWait)
	defer cancel()
	select {
	case s.base = <-found:
		return s, nil
	case <-s.done:
		return nil, fmt.Errorf("opencode serve exited before it listened (%v): %s", s.err, s.out.String())
	case <-wait.Done():
		s.stop(0)
		return nil, fmt.Errorf("opencode serve did not listen within %s: %s", listenWait, s.out.String())
	}
}

func (s *server) pid() int {
	if s == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// stop ends the process group: SIGTERM, and SIGKILL once grace has passed.
func (s *server) stop(grace time.Duration) {
	if s == nil || s.cmd.Process == nil {
		return
	}
	pgid := s.cmd.Process.Pid
	select {
	case <-s.done:
		// The leader is gone; what its tools left in the group goes too.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	default:
	}
	if grace > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-s.done:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return
		case <-time.After(grace):
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-s.done
}

func (s *server) exited() <-chan struct{} { return s.done }

// newPassword is 32 bytes of entropy per process. It lives in this process's
// memory and the child's environment, nowhere else.
func newPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ring keeps the last max bytes written to it: the server's output, for the
// message when it dies.
type ring struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.max:]...)
	}
	return len(p), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

var errStopped = errors.New("the OpenCode server is stopped")
