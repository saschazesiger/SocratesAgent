package termux

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Journal defaults. Sixty-four megabytes is a long afternoon of a busy TUI,
// and one old file is enough to keep the boundary from losing anything a user
// would go looking for.
const (
	JournalMaxBytes int64 = 64 << 20
	JournalKeep           = 1
)

// SessionDir is where everything Socrates generates for one session lives.
func SessionDir(dataDir, id string) string {
	return filepath.Join(dataDir, "sessions", id)
}

// JournalPath is the current journal file of a session.
func JournalPath(dataDir, id string) string {
	return filepath.Join(SessionDir(dataDir, id), "journal.raw")
}

// rotatedPath is the nth older journal file, counting from one.
func rotatedPath(path string, n int) string {
	ext := filepath.Ext(path)
	return path[:len(path)-len(ext)] + "." + strconv.Itoa(n) + ext
}

// PipeCommand is the shell command tmux runs as a pane's `pipe-pane` sink.
//
// It is a Socrates subcommand rather than `cat >> file` because a TUI that
// redraws continuously writes megabytes an hour and the user this is built for
// never restarts a session, so rotating "on session start" would never happen.
// Both paths are shell quoted: tmux runs this string through /bin/sh, and both
// of them were chosen by whoever installed Socrates.
//
// It begins with `exec` so that the shell tmux spawns becomes the sink instead
// of waiting on it. That is one process per session rather than two, and it
// makes the sink a direct child of the tmux server, which is what
// WatchParentProcess below reads to know that its server has gone.
func PipeCommand(socratesBin, journal string, maxBytes int64, keep int) string {
	return fmt.Sprintf("exec %s journal-sink --path %s --max-bytes %d --keep %d",
		ShellQuote(socratesBin), ShellQuote(journal), maxBytes, keep)
}

// RunJournalSink copies stdin into path, rotating it when it grows past
// maxBytes and keeping that many older files.
//
// It opens for append and tolerates being started against a file that already
// has content, because that is the normal case: `pipe-pane` is issued without
// -o (with it, a second call would silently close the journal instead of
// reopening it), so a re-issued pipe replaces a running sink mid-file.
func RunJournalSink(path string, maxBytes int64, keep int) error {
	stop := WatchParentProcess(sinkOrphanCheck, func() {
		// Nothing is buffered - every read is written straight through - so
		// there is nothing to flush and nothing to lose.
		os.Exit(0)
	})
	defer close(stop)
	return journalSink(os.Stdin, path, maxBytes, keep)
}

// sinkOrphanCheck is how often an orphaned sink notices. A journal writer is
// idle almost all of the time, so a second costs nothing and bounds how long a
// sink can outlive the server that started it.
const sinkOrphanCheck = time.Second

// WatchParentProcess calls gone when this process is reparented, and returns a
// channel that stops the watch when it is closed.
//
// The sink's stdin is a socket pair whose other end the tmux server holds, so
// the ordinary end of a session - the pane closing, the pipe being replaced,
// the server being killed - reaches it as an end of file and it stops on its
// own. This is the backstop for the case where it does not: a sink that
// outlives its server holds a journal open and a copy of the Socrates binary
// resident for as long as the machine is up, and enough of them fill a tmpfs.
// A process whose parent has gone is reparented to init, or to whichever
// ancestor claimed the subreaper role, so the test is that the parent is no
// longer the one that started us rather than that it is process 1.
func WatchParentProcess(every time.Duration, gone func()) chan struct{} {
	return watchParent(every, os.Getppid, gone)
}

func watchParent(every time.Duration, ppid func() int, gone func()) chan struct{} {
	started := ppid()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if ppid() != started {
					gone()
					return
				}
			}
		}
	}()
	return stop
}

func journalSink(r io.Reader, path string, maxBytes int64, keep int) error {
	if maxBytes <= 0 {
		maxBytes = JournalMaxBytes
	}
	if keep < 0 {
		keep = 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, size, err := openJournal(path)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := r.Read(buf)
		// A chunk that straddles the limit is split rather than allowed to
		// overshoot: a single pane write can be large, and the point of the
		// limit is that it holds while the session runs.
		for data := buf[:n]; len(data) > 0; {
			room := maxBytes - size
			if room <= 0 {
				if err := f.Close(); err != nil {
					return err
				}
				if err := rotate(path, keep); err != nil {
					return err
				}
				if f, size, err = openJournal(path); err != nil {
					return err
				}
				continue
			}
			take := int64(len(data))
			if take > room {
				take = room
			}
			if _, err := f.Write(data[:take]); err != nil {
				return err
			}
			size += take
			data = data[take:]
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func openJournal(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// rotate moves the journal aside, discarding whatever falls off the end.
func rotate(path string, keep int) error {
	if keep <= 0 {
		return os.Remove(path)
	}
	_ = os.Remove(rotatedPath(path, keep))
	for i := keep; i > 1; i-- {
		_ = os.Rename(rotatedPath(path, i-1), rotatedPath(path, i))
	}
	return os.Rename(path, rotatedPath(path, 1))
}

// TailJournal returns at most maxBytes from the end of a session's journal,
// reaching into the rotated file when the current one is shorter than that.
func TailJournal(dataDir, id string, maxBytes int64) ([]byte, error) {
	path := JournalPath(dataDir, id)
	current, err := tailFile(path, maxBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(current)) >= maxBytes {
		return current, nil
	}
	older, err := tailFile(rotatedPath(path, 1), maxBytes-int64(len(current)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return append(older, current...), nil
}

func tailFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size > maxBytes {
		if _, err := f.Seek(size-maxBytes, io.SeekStart); err != nil {
			return nil, err
		}
		size = maxBytes
	}
	out := make([]byte, size)
	n, err := io.ReadFull(f, out)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out[:n], nil
}
