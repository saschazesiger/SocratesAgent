package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/paths"

	_ "modernc.org/sqlite"
)

// ChunkSize is the length of every chunk of a file but its last. A file that
// only grows at its end - a journal, a transcript - therefore changes only its
// last chunk, and only that one is uploaded again.
const ChunkSize = 8 << 20

// uploaders is how many chunks are uploaded at once.
const uploaders = 4

// sqliteMagic starts every SQLite database file. Databases are recognised by
// it rather than by name: the agents keep theirs under names of their own.
var sqliteMagic = []byte("SQLite format 3\x00")

// sqliteCompanions are the files SQLite keeps beside a database while it is
// in use. They are never backed up: the database is copied consistently
// instead, with everything they hold in it.
var sqliteCompanions = []string{"-wal", "-shm", "-journal"}

// RunInfo is what one backup run did.
type RunInfo struct {
	// ID is the snapshot written, or for an unchanged run the one that is
	// still current.
	ID string `json:"id"`
	At int64  `json:"at"`
	// Size and Files are the snapshot's: the data directory as backed up.
	Size  int64 `json:"size"`
	Files int   `json:"files"`
	// Uploaded and Chunks are this run's own work: the bytes it sent and
	// the chunks they were.
	Uploaded int64 `json:"uploaded"`
	Chunks   int   `json:"chunks"`
	// Unchanged is a run that found nothing the last snapshot does not
	// already say, and wrote no new one.
	Unchanged bool `json:"unchanged,omitempty"`
	// Skipped are databases that could not be copied and are not in this
	// snapshot; Kept are those whose earlier copy stands in for them.
	Skipped []string `json:"skipped,omitempty"`
	Kept    []string `json:"kept,omitempty"`
}

// progress is a run as it goes, read by Status while it runs.
type progress struct {
	phase    atomic.Value
	files    atomic.Int64
	bytes    atomic.Int64
	uploaded atomic.Int64
	chunks   atomic.Int64
}

func (p *progress) setPhase(phase string) { p.phase.Store(phase) }

func (p *progress) getPhase() string {
	s, _ := p.phase.Load().(string)
	return s
}

type chunkJob struct {
	name string
	data []byte
}

// snapshotter is one walk over the data directory: it reads what changed,
// uploads the chunks the bucket does not have, and makes the manifest.
type snapshotter struct {
	layout  paths.Layout
	repo    *repo
	idx     *index
	exclude []string
	tmp     string
	prog    *progress

	// mu guards idx.remote and queued, which the uploaders write.
	mu     sync.Mutex
	queued map[string]bool
	jobs   chan chunkJob
	errMu  sync.Mutex
	err    error
	cancel context.CancelFunc

	buf     []byte
	files   map[string]fileState
	entries []Entry
	skipped []string
	kept    []string
	copies  int
}

// walk reads the whole data directory into entries, and returns once every
// chunk it needed is in the bucket. Any failure is the run's failure: a
// manifest is only ever written over chunks that are all there.
func (s *snapshotter) walk(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)
	defer s.cancel()
	s.queued = map[string]bool{}
	s.files = map[string]fileState{}
	s.jobs = make(chan chunkJob, uploaders)
	s.buf = make([]byte, ChunkSize)

	var wg sync.WaitGroup
	for i := 0; i < uploaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.upload(ctx)
		}()
	}
	err := filepath.WalkDir(s.layout.Root, func(p string, d fs.DirEntry, err error) error {
		return s.visit(ctx, p, d, err)
	})
	close(s.jobs)
	wg.Wait()
	if failed := s.failure(); failed != nil {
		return failed
	}
	return err
}

func (s *snapshotter) fail(err error) {
	s.errMu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.errMu.Unlock()
	s.cancel()
}

func (s *snapshotter) failure() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// upload is one uploader: it takes chunks off the queue until it is closed.
// After a failure it only drains the queue, so the walk is never left
// blocked on a full one.
func (s *snapshotter) upload(ctx context.Context) {
	for job := range s.jobs {
		if ctx.Err() != nil {
			continue
		}
		n, err := s.repo.putChunk(ctx, job.name, job.data)
		if err != nil {
			s.fail(fmt.Errorf("upload: %w", err))
			continue
		}
		s.mu.Lock()
		s.idx.remote[job.name] = true
		s.mu.Unlock()
		s.prog.uploaded.Add(int64(n))
		s.prog.chunks.Add(1)
	}
}

func (s *snapshotter) visit(ctx context.Context, p string, d fs.DirEntry, err error) error {
	if err != nil {
		// Something that went away between the listing and the look -
		// a temporary file, a socket - is simply not in the snapshot.
		if errors.Is(err, fs.ErrNotExist) && p != s.layout.Root {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == s.layout.Root {
		return nil
	}
	rel, ok := s.layout.Rel(p)
	if !ok || s.excluded(rel) {
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	info, err := d.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case d.IsDir():
		s.entries = append(s.entries, Entry{Path: rel, Type: TypeDir, Mode: unixMode(info.Mode()), MTime: info.ModTime().UnixNano()})
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		s.entries = append(s.entries, Entry{Path: rel, Type: TypeSymlink, MTime: info.ModTime().UnixNano(), Target: target})
	case d.Type().IsRegular():
		if s.companion(p) {
			return nil
		}
		entry, err := s.file(ctx, p, rel, info)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if entry != nil {
			s.entries = append(s.entries, *entry)
			s.prog.files.Add(1)
			s.prog.bytes.Add(entry.Size)
		}
	}
	// Sockets, pipes and devices are this machine's and are left out.
	return nil
}

// excluded reports whether rel is left out: one of paths.NotBackedUp, or
// inside a folder the caller excludes (a workspace inside the data
// directory).
func (s *snapshotter) excluded(rel string) bool {
	if paths.Excluded(rel) {
		return true
	}
	for _, x := range s.exclude {
		if rel == x || strings.HasPrefix(rel, x+"/") {
			return true
		}
	}
	return false
}

// companion reports whether p is the write-ahead log, shared memory or
// rollback journal of a database beside it.
func (s *snapshotter) companion(p string) bool {
	for _, suffix := range sqliteCompanions {
		if base, ok := strings.CutSuffix(p, suffix); ok && isSQLite(base) {
			return true
		}
	}
	return false
}

// file backs up one regular file and returns its entry, or nil for a
// database that could not be copied and has no earlier copy to stand in.
func (s *snapshotter) file(ctx context.Context, p, rel string, info fs.FileInfo) (*Entry, error) {
	now := fileState{Size: info.Size(), MTime: info.ModTime().UnixNano()}
	entry := &Entry{Path: rel, Type: TypeFile, Mode: unixMode(info.Mode()), MTime: now.MTime}
	prev, known := s.idx.Files[rel]
	// A file whose size and time are what they were is what it was; only a
	// changed one is read, and only to see whether it is a database now.
	sameStat := known && prev.Size == now.Size && prev.MTime == now.MTime
	if sameStat {
		now.SQLite = prev.SQLite
	} else {
		now.SQLite = isSQLite(p)
	}
	if now.SQLite {
		now.WALSize, now.WALMTime = walStat(p)
	}
	if sameStat && prev.WALSize == now.WALSize && prev.WALMTime == now.WALMTime && s.remoteHas(prev.Chunks) {
		s.files[rel] = prev
		entry.Size, entry.Chunks = prev.Stored, prev.Chunks
		return entry, nil
	}

	src := p
	if now.SQLite {
		s.copies++
		copyPath := filepath.Join(s.tmp, fmt.Sprintf("copy-%d.db", s.copies))
		if err := copySQLite(ctx, p, copyPath); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// A database that will not copy must not stop every other file
			// from being backed up. Its last good copy stands in for it;
			// without one it is left out, and the run says so.
			if known && prev.SQLite && s.remoteHas(prev.Chunks) {
				s.kept = append(s.kept, rel)
				s.files[rel] = prev
				entry.Size, entry.Chunks = prev.Stored, prev.Chunks
				return entry, nil
			}
			s.skipped = append(s.skipped, rel)
			return nil, nil
		}
		defer os.Remove(copyPath)
		src = copyPath
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	chunks, n, err := s.chunks(ctx, f)
	if err != nil {
		return nil, err
	}
	now.Stored, now.Chunks = n, chunks
	s.files[rel] = now
	entry.Size, entry.Chunks = n, chunks
	return entry, nil
}

// chunks reads r to its end in ChunkSize pieces, queues every piece the
// bucket does not hold, and returns their names and the length read.
func (s *snapshotter) chunks(ctx context.Context, r io.Reader) ([]string, int64, error) {
	chunks := []string{}
	var total int64
	for {
		n, err := io.ReadFull(r, s.buf)
		if n > 0 {
			name := s.repo.chunkName(s.buf[:n])
			chunks = append(chunks, name)
			total += int64(n)
			if qerr := s.queue(ctx, name, s.buf[:n]); qerr != nil {
				return nil, 0, qerr
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return chunks, total, nil
		}
		if err != nil {
			return nil, 0, err
		}
	}
}

// queue hands a chunk to the uploaders, unless the bucket has it or it is
// already on its way.
func (s *snapshotter) queue(ctx context.Context, name string, data []byte) error {
	s.mu.Lock()
	if s.idx.remote[name] || s.queued[name] {
		s.mu.Unlock()
		return nil
	}
	s.queued[name] = true
	s.mu.Unlock()
	select {
	case s.jobs <- chunkJob{name: name, data: bytes.Clone(data)}:
		return nil
	case <-ctx.Done():
		if failed := s.failure(); failed != nil {
			return failed
		}
		return ctx.Err()
	}
}

func (s *snapshotter) remoteHas(chunks []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idx.allRemote(chunks)
}

// isSQLite reports whether the file at p is an SQLite database.
func isSQLite(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, sqliteMagic)
}

// walStat is the size and modification time of a database's write-ahead
// log, zero when it has none.
func walStat(p string) (int64, int64) {
	info, err := os.Stat(p + "-wal")
	if err != nil {
		return 0, 0
	}
	return info.Size(), info.ModTime().UnixNano()
}

// copySQLite makes a consistent copy of a database that may be written to
// while it is read: VACUUM INTO over a read-only connection, which reads one
// transaction's view of the database, write-ahead log included, and writes it
// out whole. A raw copy of the file could pair half of one write with half of
// another.
func copySQLite(ctx context.Context, src, dst string) error {
	dsn := (&url.URL{Scheme: "file", Path: src, RawQuery: "mode=ro&_pragma=busy_timeout(30000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// unixMode is the permission bits of a mode, with setuid, setgid and sticky
// in their Unix places.
func unixMode(m fs.FileMode) uint32 {
	out := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		out |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		out |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		out |= 0o1000
	}
	return out
}

// fileMode reverses unixMode.
func fileMode(u uint32) fs.FileMode {
	m := fs.FileMode(u & 0o777)
	if u&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if u&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if u&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// snapshotMoment is when a snapshot is taken, never the same millisecond as
// the one before it.
func snapshotMoment(now, last time.Time) time.Time {
	t := now.UTC().Truncate(time.Millisecond)
	if !t.After(last) {
		t = last.Add(time.Millisecond)
	}
	return t
}
