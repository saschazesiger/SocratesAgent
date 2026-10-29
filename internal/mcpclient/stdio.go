package mcpclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// stderrKeep is how much of a stdio server's standard error is kept.
	stderrKeep = 4096
	// stderrInError is how much of it an error message quotes.
	stderrInError = 1024
	// maxLine is the longest line a stdio server may write.
	maxLine = 32 << 20
	// exitGrace is how long the probe waits for a process to end on its
	// own (after its standard input is closed), and again after it was
	// asked to stop, before it is killed.
	exitGrace = time.Second
	// outputGrace is how long the output of a process that ended is still
	// read, for the answer or the error it wrote just before.
	outputGrace = 500 * time.Millisecond
)

// stdioTransport talks to a server process over its standard input and
// output, one JSON-RPC message per line.
type stdioTransport struct {
	*streamConn
	cmd  *exec.Cmd
	name string

	writeMu sync.Mutex
	stdin   io.WriteCloser
	stdout  *os.File
	stderr  *os.File

	tail       tailBuffer
	stderrDone chan struct{}
	stdoutDone chan struct{}
	stdoutErr  error

	exited chan struct{}
}

// openStdio starts the server's process.
func openStdio(s Server) (*stdioTransport, error) {
	if strings.TrimSpace(s.Command) == "" {
		return nil, errors.New("no command is set")
	}
	if s.Dir != "" {
		if st, err := os.Stat(s.Dir); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("the working folder %s does not exist", s.Dir)
		}
	}
	cmd := exec.Command(s.Command, s.Args...)
	if cmd.Err != nil {
		return nil, startError(s.Command, cmd.Err)
	}
	cmd.Dir = s.Dir
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+s.Env[k])
	}
	configureProcess(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, startError(s.Command, err)
	}

	t := &stdioTransport{
		cmd: cmd, name: s.Command, stdin: stdin, stdout: outR, stderr: errR,
		stderrDone: make(chan struct{}), stdoutDone: make(chan struct{}), exited: make(chan struct{}),
	}
	t.streamConn = newStreamConn(t.write, t.failure)

	go func() {
		_ = cmd.Wait()
		close(t.exited)
	}()
	go func() {
		_, _ = io.Copy(&t.tail, errR)
		close(t.stderrDone)
	}()
	go t.read()
	go func() {
		// No more answers can come once the output is closed, or a moment
		// after the process ended (a child it left behind may still hold
		// its output open).
		select {
		case <-t.stdoutDone:
		case <-t.exited:
			select {
			case <-t.stdoutDone:
			case <-time.After(outputGrace):
			}
		}
		close(t.done)
	}()
	return t, nil
}

// startError explains why a command could not be started.
func startError(command string, err error) error {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return &wrapped{msg: "the command " + command + " was not found", err: err}
	case errors.Is(err, exec.ErrDot):
		return &wrapped{msg: "the command " + command + " was not found (it is only in the current folder; give its path)", err: err}
	case errors.Is(err, fs.ErrPermission):
		return &wrapped{msg: "the command " + command + " cannot be run: permission denied", err: err}
	}
	return &wrapped{msg: "the command " + command + " could not be started: " + err.Error(), err: err}
}

// read reads the server's messages from its standard output. A line that is
// not JSON-RPC (a banner, a log line) is ignored.
func (t *stdioTransport) read() {
	defer close(t.stdoutDone)
	sc := bufio.NewScanner(t.stdout)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		for _, m := range parseMessages(sc.Bytes()) {
			t.deliver(m)
		}
	}
	if err := sc.Err(); errors.Is(err, bufio.ErrTooLong) {
		t.stdoutErr = fmt.Errorf("the server wrote a line longer than %d MiB", maxLine>>20)
	}
}

// write sends one message as a line on the process's standard input.
func (t *stdioTransport) write(ctx context.Context, b []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	line := append(append(make([]byte, 0, len(b)+1), b...), '\n')
	errc := make(chan error, 1)
	go func() {
		_, err := t.stdin.Write(line)
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			return nil
		}
		// The process is gone, most likely: say why, if it can be told.
		select {
		case <-t.done:
			return t.failure()
		case <-time.After(exitGrace):
		}
		return fmt.Errorf("could not write to the process: %v", err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// failure says why no answer can come any more.
func (t *stdioTransport) failure() error {
	select {
	case <-t.stdoutDone:
		if t.stdoutErr != nil {
			return t.stdoutErr
		}
	default:
	}
	select {
	case <-t.exited:
	case <-time.After(exitGrace):
		return errors.New("the process closed its output before it answered" + t.quoteStderr())
	}
	select {
	case <-t.stderrDone:
	case <-time.After(outputGrace):
	}
	how := "ended"
	if st := t.cmd.ProcessState; st != nil {
		if code := st.ExitCode(); code >= 0 {
			how = fmt.Sprintf("exited (status %d)", code)
		} else {
			how = "ended (" + st.String() + ")"
		}
	}
	return errors.New("the process " + how + " before it answered" + t.quoteStderr())
}

// quoteStderr is ": " and the end of the process's standard error, or
// nothing when it wrote none.
func (t *stdioTransport) quoteStderr() string {
	tail := strings.TrimSpace(t.tail.String())
	if tail == "" {
		return ""
	}
	if len(tail) > stderrInError {
		cut := len(tail) - stderrInError
		for cut < len(tail) && !utf8Start(tail[cut]) {
			cut++
		}
		tail = "..." + tail[cut:]
	}
	return ": " + tail
}

func (t *stdioTransport) setProtocolVersion(string) {}

func (t *stdioTransport) stderrTail() string { return t.tail.String() }

// close closes the process's standard input, gives it a moment to end, and
// then stops its whole process tree.
func (t *stdioTransport) close() {
	t.writeMu.Lock()
	_ = t.stdin.Close()
	t.writeMu.Unlock()
	select {
	case <-t.exited:
	case <-time.After(exitGrace):
		if terminateTree(t.cmd) {
			select {
			case <-t.exited:
			case <-time.After(exitGrace):
			}
		}
	}
	running := true
	select {
	case <-t.exited:
		running = false
	default:
	}
	killTree(t.cmd, running)
	select {
	case <-t.exited:
	case <-time.After(5 * time.Second):
	}
	select {
	case <-t.stdoutDone:
	case <-time.After(outputGrace):
	}
	select {
	case <-t.stderrDone:
	case <-time.After(outputGrace):
	}
	_ = t.stdout.Close()
	_ = t.stderr.Close()
}

// tailBuffer keeps the last stderrKeep bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= stderrKeep {
		b.buf = append(b.buf[:0], p[len(p)-stderrKeep:]...)
		return n, nil
	}
	if over := len(b.buf) + len(p) - stderrKeep; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

// String is the kept text, starting on a whole character.
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf
	if len(s) >= stderrKeep {
		for len(s) > 0 && !utf8Start(s[0]) {
			s = s[1:]
		}
	}
	return string(s)
}
