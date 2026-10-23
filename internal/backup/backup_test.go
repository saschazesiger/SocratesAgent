package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"

	_ "modernc.org/sqlite"
)

const testPassphrase = "correct horse battery staple"

// clock is a test's time, shared by the engine and the fake bucket.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// bucket is a fake S3 with a switch that fails chunk uploads after a number
// of them, for the run that is cut short.
type bucket struct {
	fake *s3fake.Fake
	url  string
	// failAfter, when positive, is how many more chunk PUTs succeed.
	failAfter atomic.Int64
	puts      atomic.Int64
}

func newBucket(t *testing.T, clk *clock) *bucket {
	t.Helper()
	b := &bucket{fake: s3fake.New(s3fake.Options{Written: clk.Now})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			b.puts.Add(1)
			if strings.Contains(r.URL.Path, "/"+chunkDir) && b.failAfter.Load() > 0 && b.failAfter.Add(-1) == 0 {
				http.Error(w, "injected", http.StatusServiceUnavailable)
				return
			}
		}
		b.fake.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	b.url = srv.URL
	return b
}

func (b *bucket) config(host string) Config {
	cfg := Config{
		Endpoint: b.url, Region: s3fake.DefaultRegion, Bucket: "backups", Prefix: "socrates/backup/v1",
		AccessKeyID: s3fake.DefaultAccessKeyID, SecretAccessKey: s3fake.DefaultSecretAccessKey,
		PathStyle: true, Passphrase: testPassphrase, HostID: host,
	}
	cfg.Normalize()
	return cfg
}

func (b *bucket) keys(prefix string) []string {
	return b.fake.Keys("backups", "socrates/backup/v1/"+prefix)
}

// install makes dataDir an installation, as a server that has been set up
// leaves it: a database with a password. A backup leaves a data directory
// without one alone.
func install(t *testing.T, dataDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dataDir, paths.DB)); err == nil {
		return
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, filepath.Join(dataDir, paths.DB), false)
	if _, err := db.Exec(`CREATE TABLE kv(key TEXT PRIMARY KEY, value TEXT); CREATE TABLE sessions(id TEXT);
		INSERT INTO kv VALUES ('password_hash', 'pbkdf2-sha256$1$a$b')`); err != nil {
		t.Fatal(err)
	}
}

// engine is an Engine on dataDir, an installation, backing up into b.
func engine(t *testing.T, dataDir string, b *bucket, clk *clock, host string) *Engine {
	t.Helper()
	install(t, dataDir)
	if err := SaveConfig(dataDir, b.config(host)); err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{DataDir: dataDir, Version: "test", Now: clk.Now, Iterations: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func random(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// openDB opens a database the way the store does: WAL, one connection.
func openDB(t *testing.T, path string, ro bool) *sql.DB {
	t.Helper()
	q := "_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	if ro {
		q = "mode=ro&_pragma=busy_timeout(10000)"
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q}).String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// populate fills a data directory with one of everything a backup meets.
func populate(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "agents/claude/projects/p/a.jsonl"), []byte(`{"a":1}`+"\n"), 0o600)
	write(t, filepath.Join(dir, "agents/codex/sessions/big.bin"), random(t, 2*ChunkSize+12345), 0o640)
	write(t, filepath.Join(dir, "chats/c1/events.jsonl"), bytes.Repeat([]byte(`{"seq":1,"type":"message"}`+"\n"), 1000), 0o600)
	write(t, filepath.Join(dir, "chats/c1/attachments/empty"), nil, 0o644)
	write(t, filepath.Join(dir, "accounts/a1/run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o750)
	if err := os.MkdirAll(filepath.Join(dir, "accounts/a1/empty-dir"), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "accounts/a1/empty-dir"), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../agents/claude/projects", filepath.Join(dir, "accounts/a1/projects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/absolute", filepath.Join(dir, "accounts/a1/dangling")); err != nil {
		t.Fatal(err)
	}
	// Left out: regenerable, this machine's, or a workspace.
	write(t, filepath.Join(dir, "run/x.pid"), []byte("1"), 0o600)
	write(t, filepath.Join(dir, "cache/catalog.json"), []byte("{}"), 0o600)
	write(t, filepath.Join(dir, "sessions/s1/journal.raw"), []byte("screen bytes"), 0o600)
	write(t, filepath.Join(dir, "sessions/s1/plan.json"), []byte(`{"argv":["claude"]}`), 0o600)
	write(t, filepath.Join(dir, "updates.json"), []byte("{}"), 0o600)
	write(t, filepath.Join(dir, "socrates.db.pre-v9.bak"), []byte("old"), 0o600)
	write(t, filepath.Join(dir, "workspaces/w1/code.go"), []byte("package main"), 0o644)
	// An SQLite database under a name of an agent's own.
	if err := os.MkdirAll(filepath.Join(dir, "agents/opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, filepath.Join(dir, "agents/opencode/opencode.db"), false)
	if _, err := db.Exec(`CREATE TABLE msg(id INTEGER PRIMARY KEY, text TEXT); INSERT INTO msg(text) VALUES ('hello'), ('world')`); err != nil {
		t.Fatal(err)
	}
	// A socket, which no backup can hold.
	if ln, err := listenUnix(filepath.Join(dir, "hook.sock")); err == nil {
		t.Cleanup(func() { ln.Close() })
	}
}

func listenUnix(path string) (interface{ Close() error }, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// excludeWorkspaces is what the server passes: the workspace root inside the
// data directory.
func excludeWorkspaces(dir string) func() []string {
	return func() []string { return []string{filepath.Join(dir, "workspaces")} }
}

// restoreInto restores the newest snapshot into a new data directory and
// applies it.
func restoreInto(t *testing.T, b *bucket, snapshot string) string {
	t.Helper()
	dest := t.TempDir()
	if _, err := Restore(context.Background(), RestoreOptions{Config: b.config(""), Snapshot: snapshot, DataDir: dest}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := ApplyPending(dest); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return dest
}

type node struct {
	mode   fs.FileMode
	data   string
	target string
	mtime  int64
}

// tree reads a directory the way a restore must reproduce it.
func tree(t *testing.T, root string, skip func(rel string) bool) map[string]node {
	t.Helper()
	out := map[string]node{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if skip(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		n := node{mode: info.Mode()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			n.target, _ = os.Readlink(p)
		case info.Mode().IsRegular():
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			n.data = string(raw)
			n.mtime = info.ModTime().UnixNano()
		case info.IsDir():
		default:
			return nil
		}
		out[rel] = n
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// copiedDB is a database the backup copies rather than reads, and which
// therefore does not come back byte for byte; it is checked by its rows.
func copiedDB(rel string) bool {
	return strings.HasPrefix(rel, "agents/opencode/opencode.db") || strings.HasPrefix(rel, paths.DB)
}

func notRestored(rel string) bool {
	return paths.Excluded(rel) || rel == "hook.sock" || rel == "workspaces" || strings.HasPrefix(rel, "workspaces/") ||
		copiedDB(rel)
}

// The first snapshot holds everything that is not left out, and a restore of
// it into an empty directory is the same tree byte for byte: modes, times,
// relative and dangling links, empty files and directories, and a database
// copied while it was open.
func TestFirstSnapshotRestoresByteIdentical(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	populate(t, src)
	e := engine(t, src, b, clk, "host-a")
	e.opts.Exclude = excludeWorkspaces(src)

	info, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Unchanged || info.Files == 0 || info.Chunks < 4 {
		t.Fatalf("first run = %+v", info)
	}
	if got := b.keys(snapshotDir); len(got) != 1 || !strings.Contains(got[0], "host-a/") {
		t.Fatalf("snapshots = %v", got)
	}
	if meta, ok := b.fake.Object("backups", "socrates/backup/v1/meta.json"); !ok || !bytes.Contains(meta, []byte(`"kind": "socrates-backup"`)) {
		t.Fatalf("meta.json = %s", meta)
	}
	for _, key := range b.keys("") {
		obj, _ := b.fake.Object("backups", key)
		if strings.HasSuffix(key, "meta.json") {
			continue
		}
		if bytes.Contains(obj, []byte("hello")) || bytes.Contains(obj, []byte(`"seq"`)) || bytes.Contains(obj, []byte("big.bin")) {
			t.Fatalf("%s holds plaintext", key)
		}
	}

	dest := restoreInto(t, b, "latest")
	sameTree(t, src, dest)
	// Looked for before the restored database is opened, which makes a
	// write-ahead log of its own.
	for _, left := range []string{"restore.pending", "agents/opencode/opencode.db-wal"} {
		if _, err := os.Lstat(filepath.Join(dest, left)); err == nil {
			t.Errorf("%s is left in the restored directory", left)
		}
	}
	if entries, _ := filepath.Glob(filepath.Join(dest, ".restore-*")); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}
	db := openDB(t, filepath.Join(dest, "agents/opencode/opencode.db"), true)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM msg`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("the restored database: %d rows, %v", n, err)
	}
}

// sameTree compares a backed up data directory with its restore: every path
// that is not left out is there, with its mode, content, link target and
// time, and nothing else is.
func sameTree(t *testing.T, src, dest string) {
	t.Helper()
	want := tree(t, src, notRestored)
	got := tree(t, dest, copiedDB)
	if len(want) < 15 {
		t.Fatalf("only %d paths to compare: %v", len(want), want)
	}
	for rel, w := range want {
		g, ok := got[rel]
		if !ok {
			t.Errorf("%s was not restored", rel)
			continue
		}
		if g.mode != w.mode || g.data != w.data || g.target != w.target || g.mtime != w.mtime {
			t.Errorf("%s: restored %v %q→%q at %d, want %v %q→%q at %d", rel, g.mode, short(g.data), g.target, g.mtime,
				w.mode, short(w.data), w.target, w.mtime)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("%s was restored but is not backed up", rel)
		}
	}
}

// A data directory that is a symbolic link to where it really is - moved
// onto a volume of its own - is backed up whole rather than as an empty
// snapshot, and a workspace named through the link is still left out.
func TestASymlinkedDataDirectoryIsBackedUpWhole(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	real := t.TempDir()
	populate(t, real)
	link := filepath.Join(t.TempDir(), "socrates")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	e := engine(t, link, b, clk, "host-a")
	e.opts.Exclude = excludeWorkspaces(link)
	info, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Files < 7 {
		t.Fatalf("the snapshot of a linked data directory holds %d files: %+v", info.Files, info)
	}
	sameTree(t, real, restoreInto(t, b, "latest"))
}

// A machine that holds no installation yet - new, with the old machine's
// backup.json copied over to restore from, or with a restore waiting to be
// moved into place - writes nothing into the bucket, fails no run, and takes
// a host id of its own; once it is set up it backs up under that id.
func TestNothingIsBackedUpBeforeTheMachineIsSetUp(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	fresh := t.TempDir()
	if err := SaveConfig(fresh, b.config("old-machine")); err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{DataDir: fresh, Now: clk.Now, Iterations: 1000})
	if err != nil {
		t.Fatal(err)
	}
	host := e.Config().HostID
	if host == "old-machine" || host == "" {
		t.Fatalf("a machine with nothing installed kept the host id %q", host)
	}
	if saved, _ := LoadConfig(fresh); saved == nil || saved.HostID != host {
		t.Fatalf("the new host id was not saved: %+v", saved)
	}
	if _, err := e.Run(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a run on a machine with nothing installed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Close(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("the last run at a stop: %v", err)
	}
	if n := b.puts.Load(); n != 0 {
		t.Fatalf("%d objects were written for a machine with nothing installed", n)
	}
	if st := e.Status(); st.LastError != "" || st.LastAttempt != 0 || st.LastSuccess != nil {
		t.Fatalf("a run that waits for an installation is recorded: %+v", st)
	}

	install(t, fresh)
	write(t, paths.New(fresh).Path(paths.RestorePending), []byte(`{"staging":".restore-x"}`), 0o600)
	if _, err := e.Run(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a run while a restore waits to be moved into place: %v", err)
	}
	if err := os.Remove(paths.New(fresh).Path(paths.RestorePending)); err != nil {
		t.Fatal(err)
	}
	info, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.ID, host+"/") {
		t.Fatalf("the set up machine's snapshot is %s, not under %s", info.ID, host)
	}
	if again, err := New(Options{DataDir: fresh, Now: clk.Now, Iterations: 1000}); err != nil || again.Config().HostID != host {
		t.Fatalf("an installed machine's host id changed: %v %v", again.Config(), err)
	}
}

// A snapshot without the database is no installation: a run that would
// write one fails instead, and a restore of "latest" passes over one an
// earlier build wrote for the newest real snapshot; asked for by name, it is
// refused.
func TestASnapshotWithoutTheDatabaseIsNeitherWrittenNorRestored(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/events.jsonl"), []byte("real\n"), 0o600)
	e := engine(t, src, b, clk, "host-a")
	real, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	clk.Add(10 * time.Minute)
	write(t, filepath.Join(src, "chats/c1/events.jsonl"), []byte("real, and more\n"), 0o600)
	e.opts.Exclude = func() []string { return []string{filepath.Join(src, paths.DB)} }
	if _, err := e.Run(context.Background()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("a run that did not take the database: %v", err)
	}
	if got := b.keys(snapshotDir); len(got) != 1 {
		t.Fatalf("snapshots after a run without the database: %v", got)
	}
	if st := e.Status(); !strings.Contains(st.LastError, "database") {
		t.Fatalf("the failed run says %q", st.LastError)
	}

	r, err := openRepo(context.Background(), b.config(""), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	empty := &Manifest{Format: manifestFormat, ID: snapshotID("host-new", clk.Now().Add(time.Hour)), Host: "host-new",
		CreatedAt: clk.Now().Add(time.Hour).UnixMilli(), Entries: []Entry{}}
	if err := r.putManifest(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	list, err := List(context.Background(), b.config(""), t.TempDir())
	if err != nil || len(list) != 2 || list[0].ID != empty.ID || !list[0].Empty || list[1].Empty {
		t.Fatalf("the list: %+v %v", list, err)
	}
	dest := restoreInto(t, b, "latest")
	if raw, err := os.ReadFile(filepath.Join(dest, "chats/c1/events.jsonl")); err != nil || string(raw) != "real\n" {
		t.Fatalf("latest restored %q %v, not the snapshot %s", raw, err, real.ID)
	}
	if _, err := os.Stat(filepath.Join(dest, paths.DB)); err != nil {
		t.Fatalf("latest restored no database: %v", err)
	}
	other := t.TempDir()
	if _, err := Restore(context.Background(), RestoreOptions{Config: b.config(""), Snapshot: empty.ID, DataDir: other}); err == nil ||
		!strings.Contains(err.Error(), "holds no database") {
		t.Fatalf("a restore of the empty snapshot by name: %v", err)
	}
	if RestorePending(other) {
		t.Fatal("the refused restore left a marker")
	}
}

// A second run over an unchanged directory writes nothing; one after a file
// grew uploads only that file's last chunk and the manifest.
func TestIncrementalUploadsOnlyTheChangedChunk(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	journal := filepath.Join(src, "chats/c1/events.jsonl")
	write(t, journal, random(t, 2*ChunkSize+100), 0o600)
	write(t, filepath.Join(src, "chats/c2/events.jsonl"), []byte("other"), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	chunks := len(b.keys(chunkDir))

	clk.Add(10 * time.Minute)
	before := b.puts.Load()
	info, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Unchanged || b.puts.Load() != before || len(b.keys(snapshotDir)) != 1 {
		t.Fatalf("an unchanged run: %+v, %d puts, %d snapshots", info, b.puts.Load()-before, len(b.keys(snapshotDir)))
	}

	clk.Add(10 * time.Minute)
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("appended line\n"))
	f.Close()
	before = b.puts.Load()
	info, err = e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Unchanged || info.Chunks != 1 || b.puts.Load()-before != 2 {
		t.Fatalf("after an append: %+v, %d puts", info, b.puts.Load()-before)
	}
	if got := len(b.keys(chunkDir)); got != chunks+1 {
		t.Fatalf("%d chunks after an append, want %d", got, chunks+1)
	}
	if got := len(b.keys(snapshotDir)); got != 2 {
		t.Fatalf("%d snapshots", got)
	}

	// The index is a cache: lost, the next run reads the files again and
	// still uploads nothing the bucket has.
	if err := os.RemoveAll(filepath.Join(src, "cache")); err != nil {
		t.Fatal(err)
	}
	e2, err := New(Options{DataDir: src, Now: clk.Now, Iterations: 1000})
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(10 * time.Minute)
	before = b.puts.Load()
	info, err = e2.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Chunks != 0 || b.puts.Load()-before != 1 {
		t.Fatalf("with the index lost: %+v, %d puts", info, b.puts.Load()-before)
	}
}

// A database written to while it is backed up comes back whole: every
// restored copy passes the integrity check and holds whole transactions only.
func TestSQLiteWrittenDuringTheBackupCopiesConsistently(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	path := filepath.Join(src, "agents/codex/state_5.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, path, false)
	if _, err := db.Exec(`CREATE TABLE ledger(id INTEGER PRIMARY KEY, amount INTEGER, pad TEXT)`); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pad := strings.Repeat("x", 2000)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Every transaction adds a pair that sums to zero.
			tx, err := db.Begin()
			if err != nil {
				t.Error(err)
				return
			}
			tx.Exec(`INSERT INTO ledger(amount, pad) VALUES (?, ?)`, i+1, pad)
			tx.Exec(`INSERT INTO ledger(amount, pad) VALUES (?, ?)`, -(i + 1), pad)
			if err := tx.Commit(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	e := engine(t, src, b, clk, "host-a")
	var ids []string
	for i := 0; i < 3; i++ {
		clk.Add(time.Minute)
		info, err := e.Run(context.Background())
		if err != nil {
			close(stop)
			t.Fatal(err)
		}
		ids = append(ids, info.ID)
	}
	close(stop)
	wg.Wait()
	for _, id := range ids {
		dest := restoreInto(t, b, id)
		copyDB := openDB(t, filepath.Join(dest, "agents/codex/state_5.sqlite"), true)
		var check string
		if err := copyDB.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
			t.Fatalf("%s: integrity %q %v", id, check, err)
		}
		var rows, sum int
		if err := copyDB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(amount), 0) FROM ledger`).Scan(&rows, &sum); err != nil {
			t.Fatal(err)
		}
		if rows == 0 || rows%2 != 0 || sum != 0 {
			t.Fatalf("%s holds half a transaction: %d rows summing to %d", id, rows, sum)
		}
	}
}

// A database that grew in its first table goes up as the chunks its new and
// changed pages lie in: the copy keeps the database's page order, so the
// pages of the table after it - and their chunks - are the ones the bucket
// already holds.
func TestADatabaseThatGrewUploadsOnlyItsChangedChunks(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	path := filepath.Join(src, "agents/opencode/opencode.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, path, false)
	// Half a MiB of messages, then 16 MiB of parts after them: the second
	// of the three chunks holds parts only.
	if _, err := db.Exec(`CREATE TABLE message(id INTEGER PRIMARY KEY, body BLOB);
		CREATE TABLE part(id INTEGER PRIMARY KEY, body BLOB);
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 120)
		INSERT INTO message(body) SELECT randomblob(4000) FROM n;
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 4100)
		INSERT INTO part(body) SELECT randomblob(4000) FROM n;
		PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	e := engine(t, src, b, clk, "host-a")
	first, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 30)
		INSERT INTO message(body) SELECT randomblob(4000) FROM n`); err != nil {
		t.Fatal(err)
	}
	clk.Add(10 * time.Minute)
	second, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chunksOf := func(id string) []string {
		m := cachedManifest(src, id)
		if m == nil {
			t.Fatalf("no manifest %s", id)
		}
		for _, entry := range m.Entries {
			if entry.Path == "agents/opencode/opencode.db" {
				return entry.Chunks
			}
		}
		t.Fatalf("%s holds no opencode.db", id)
		return nil
	}
	before, after := chunksOf(first.ID), chunksOf(second.ID)
	if len(before) != 3 || len(after) != 3 {
		t.Fatalf("the database is %d chunks, then %d", len(before), len(after))
	}
	if after[1] != before[1] || second.Chunks != 2 {
		t.Fatalf("after 30 rows in the first table the middle chunk changed (%v) and %d chunks went up",
			after[1] != before[1], second.Chunks)
	}
}

// A wrong passphrase is refused before anything is written or restored.
func TestAWrongPassphraseIsRefused(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/events.jsonl"), []byte("x"), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrong := b.config("host-b")
	wrong.Passphrase = "not it"

	before := b.puts.Load()
	other := t.TempDir()
	install(t, other)
	if err := SaveConfig(other, wrong); err != nil {
		t.Fatal(err)
	}
	e2, err := New(Options{DataDir: other, Now: clk.Now, Iterations: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.Run(context.Background()); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("a run with the wrong passphrase: %v", err)
	}
	if err := e2.SetConfig(context.Background(), wrong); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("saving the wrong passphrase: %v", err)
	}
	if res := e2.Test(context.Background(), wrong); res.OK || !strings.Contains(res.Error, "passphrase") {
		t.Fatalf("testing the wrong passphrase: %+v", res)
	}
	if b.puts.Load() != before {
		t.Fatalf("%d objects were written with the wrong passphrase", b.puts.Load()-before)
	}
	dest := t.TempDir()
	if _, err := Restore(context.Background(), RestoreOptions{Config: wrong, DataDir: dest}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("a restore with the wrong passphrase: %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("a refused restore wrote %v", entries)
	}
	if _, err := List(context.Background(), wrong, dest); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("a listing with the wrong passphrase: %v", err)
	}
}

// Retention keeps everything of two days, a day's newest for thirty days, a
// week's newest for twenty-six weeks, and the newest whatever its age.
func TestRetainKeepsTheSchedule(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	var times []time.Time
	for h := 0; h < 24*400; h += 6 {
		times = append(times, now.Add(-time.Duration(h)*time.Hour))
	}
	keep := retain(times, now)
	days, weeks := map[string]int{}, map[string]int{}
	for i, at := range times {
		age := now.Sub(at)
		switch {
		case age <= keepAll:
			if !keep[i] {
				t.Errorf("%v (%v old) is within two days and was not kept", at, age)
			}
		case age <= keepDays:
			if keep[i] {
				days[at.Format("2006-01-02")]++
			}
		case age <= keepWeeks:
			if keep[i] {
				y, w := at.ISOWeek()
				weeks[fmt.Sprintf("%d-%02d", y, w)]++
			}
		default:
			if keep[i] {
				t.Errorf("%v is older than 26 weeks and was kept", at)
			}
		}
	}
	for d, n := range days {
		if n > 1 {
			t.Errorf("%d snapshots of %s were kept", n, d)
		}
	}
	if len(days) < 27 {
		t.Errorf("only %d days are kept", len(days))
	}
	for w, n := range weeks {
		if n > 1 {
			t.Errorf("%d snapshots of week %s were kept", n, w)
		}
	}
	if len(weeks) < 20 {
		t.Errorf("only %d weeks are kept", len(weeks))
	}
	if old := retain([]time.Time{now.Add(-400 * 24 * time.Hour)}, now); !old[0] {
		t.Error("the only snapshot was not kept")
	}
}

// Pruning deletes this machine's snapshots retention drops - never another
// machine's - and garbage collection deletes the chunks nothing refers to
// once they are older than the grace, never a chunk a running snapshot has
// just uploaded.
func TestPruneAndCollectWithGrace(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	file := filepath.Join(src, "chats/c1/events.jsonl")
	e := engine(t, src, b, clk, "host-a")

	// A snapshot every 12 hours for six days, each with new content.
	for i := 0; i < 12; i++ {
		write(t, file, random(t, 1000), 0o600)
		if _, err := e.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.Add(12 * time.Hour)
	}
	// Another machine's old snapshot, which this one must leave alone.
	otherDir := t.TempDir()
	write(t, filepath.Join(otherDir, "chats/x/events.jsonl"), random(t, 500), 0o600)
	other := engine(t, otherDir, b, clk, "host-b")
	if _, err := other.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	otherSnap := b.keys(snapshotDir + "host-b/")
	if len(otherSnap) != 1 {
		t.Fatalf("host-b snapshots: %v", otherSnap)
	}

	// A chunk a run uploaded a moment ago and has not written its manifest
	// for yet: referred to by nothing, and young.
	r, err := openRepo(context.Background(), b.config("host-a"), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	young := random(t, 300)
	youngName := r.chunkName(young)
	if _, err := r.putChunk(context.Background(), youngName, young); err != nil {
		t.Fatal(err)
	}

	write(t, file, random(t, 1000), 0o600)
	clk.Add(7 * time.Hour)
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	snaps := b.keys(snapshotDir + "host-a/")
	// Kept: everything of the last 48 hours (the last 5 at 12 h spacing,
	// counting the one just written), and the newest of each older day.
	if len(snaps) >= 13 || len(snaps) < 7 {
		t.Fatalf("%d snapshots of host-a after pruning: %v", len(snaps), snaps)
	}
	if got := b.keys(snapshotDir + "host-b/"); len(got) != 1 {
		t.Fatalf("host-b's snapshot was pruned: %v", got)
	}
	if _, ok := b.fake.Object("backups", "socrates/backup/v1/"+chunkDir+youngName); !ok {
		t.Fatal("a young unreferenced chunk was collected")
	}
	// Every snapshot left still restores.
	refs, err := r.listSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, k := range b.keys(chunkDir) {
		present[strings.TrimPrefix(k, "socrates/backup/v1/"+chunkDir)] = true
	}
	referenced := map[string]bool{}
	for _, ref := range refs {
		m, err := r.getManifest(context.Background(), ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		m.chunkSet(referenced)
	}
	for c := range referenced {
		if !present[c] {
			t.Fatalf("chunk %s of a kept snapshot was collected", short(c))
		}
	}
	unreferenced := 0
	for c := range present {
		if !referenced[c] {
			unreferenced++
		}
	}
	// The pruned snapshots' chunks are younger than the grace still.
	if unreferenced == 0 {
		t.Fatal("expected the pruned snapshots' chunks to wait out the grace")
	}

	// A day on, past the grace, the next collection takes them.
	clk.Add(25 * time.Hour)
	write(t, file, random(t, 1000), 0o600)
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	referenced = map[string]bool{}
	refs, _ = r.listSnapshots(context.Background())
	for _, ref := range refs {
		m, err := r.getManifest(context.Background(), ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		m.chunkSet(referenced)
	}
	for _, k := range b.keys(chunkDir) {
		if c := strings.TrimPrefix(k, "socrates/backup/v1/"+chunkDir); !referenced[c] {
			t.Fatalf("chunk %s outlived the grace unreferenced", short(c))
		}
	}
}

// A restore never lands on an installation: a password, sessions, or a
// folder an installation fills.
func TestRestoreIsRefusedIntoAStore(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/events.jsonl"), []byte("x"), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	withPassword := t.TempDir()
	db := openDB(t, filepath.Join(withPassword, paths.DB), false)
	if _, err := db.Exec(`CREATE TABLE kv(key TEXT PRIMARY KEY, value TEXT); CREATE TABLE sessions(id TEXT);
		INSERT INTO kv VALUES ('password_hash', 'pbkdf2-sha256$1$a$b')`); err != nil {
		t.Fatal(err)
	}
	withChats := t.TempDir()
	write(t, filepath.Join(withChats, "chats/c9/events.jsonl"), []byte("mine"), 0o600)
	fresh := t.TempDir()
	freshDB := openDB(t, filepath.Join(fresh, paths.DB), false)
	if _, err := freshDB.Exec(`CREATE TABLE kv(key TEXT PRIMARY KEY, value TEXT); CREATE TABLE sessions(id TEXT);
		INSERT INTO kv VALUES ('settings', '{}')`); err != nil {
		t.Fatal(err)
	}

	for name, dir := range map[string]string{"a password": withPassword, "chats": withChats} {
		_, err := Restore(context.Background(), RestoreOptions{Config: b.config(""), DataDir: dir})
		if !errors.Is(err, ErrNotEmpty) {
			t.Fatalf("a restore into a directory with %s: %v", name, err)
		}
		if RestorePending(dir) {
			t.Fatalf("a refused restore into %s left a marker", name)
		}
	}
	// A database a new server has only just made is not an installation.
	if _, err := Restore(context.Background(), RestoreOptions{Config: b.config(""), DataDir: fresh}); err != nil {
		t.Fatalf("a restore into a new server's directory: %v", err)
	}
	freshDB.Close()
	if _, err := ApplyPending(fresh); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(fresh, "chats/c1/events.jsonl")); err != nil || string(raw) != "x" {
		t.Fatalf("restored: %q %v", raw, err)
	}
}

// A start that died while it moved a staged restore into place finishes the
// move the next time: what had been moved stays, the rest follows, the new
// server's own files (its socket, run/, backup.json) are left alone, and a
// third start has nothing left to do.
func TestApplyPendingFinishesAMoveCutShort(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/events.jsonl"), []byte("one"), 0o600)
	write(t, filepath.Join(src, "chats/c2/events.jsonl"), []byte("two"), 0o600)
	write(t, filepath.Join(src, "agents/claude/projects/p.jsonl"), []byte("claude"), 0o600)
	write(t, filepath.Join(src, "agents/codex/history.jsonl"), []byte("codex"), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	write(t, filepath.Join(dest, paths.TmuxSocket), []byte("socket"), 0o600)
	write(t, filepath.Join(dest, "run/socrates.lock"), []byte("1"), 0o600)
	if err := SaveConfig(dest, b.config("")); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), RestoreOptions{Config: b.config(""), DataDir: dest}); err != nil {
		t.Fatal(err)
	}
	stages, _ := filepath.Glob(filepath.Join(dest, paths.RestoreStagingPrefix+"*"))
	if len(stages) != 1 {
		t.Fatalf("staging directories: %v", stages)
	}
	// The first start moved chats/ whole and agents/claude into a fresh
	// agents/, and died.
	if err := os.Rename(filepath.Join(stages[0], "chats"), filepath.Join(dest, "chats")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dest, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(stages[0], "agents/claude"), filepath.Join(dest, "agents/claude")); err != nil {
		t.Fatal(err)
	}

	id, err := ApplyPending(dest)
	if err != nil || id == "" {
		t.Fatalf("the second start: %q %v", id, err)
	}
	for rel, want := range map[string]string{
		"chats/c1/events.jsonl": "one", "chats/c2/events.jsonl": "two", "agents/claude/projects/p.jsonl": "claude",
		"agents/codex/history.jsonl": "codex", paths.TmuxSocket: "socket", "run/socrates.lock": "1",
	} {
		if raw, err := os.ReadFile(filepath.Join(dest, rel)); err != nil || string(raw) != want {
			t.Errorf("%s: %q %v, want %q", rel, raw, err, want)
		}
	}
	if cfg, err := LoadConfig(dest); err != nil || cfg == nil || cfg.Bucket != "backups" {
		t.Errorf("backup.json after the move: %+v %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(dest, paths.DB)); err != nil {
		t.Errorf("the database was not moved: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dest, paths.RestoreStagingPrefix+"*")); len(left) != 0 || RestorePending(dest) {
		t.Fatalf("left behind: %v, marker %v", left, RestorePending(dest))
	}
	if id, err := ApplyPending(dest); err != nil || id != "" {
		t.Fatalf("a third start: %q %v", id, err)
	}
}

// A run cut short - the bucket refusing a chunk half way - writes no
// manifest, so every snapshot in the bucket still has all its chunks; the
// next run finishes the work without sending again what did arrive.
func TestARunCutShortLeavesNoManifestWithMissingChunks(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/a"), random(t, 100), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(b.keys(chunkDir))
	write(t, filepath.Join(src, "agents/codex/big"), random(t, 3*ChunkSize), 0o600)
	b.failAfter.Store(2)
	clk.Add(10 * time.Minute)
	if _, err := e.Run(context.Background()); err == nil {
		t.Fatal("a run whose upload failed succeeded")
	}
	if got := len(b.keys(snapshotDir)); got != 1 {
		t.Fatalf("%d snapshots after a failed run", got)
	}
	if st := e.Status(); st.LastError == "" || st.LastSuccess == nil {
		t.Fatalf("status after a failed run: %+v", st)
	}
	r, err := openRepo(context.Background(), b.config(""), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := r.listSnapshots(context.Background())
	for _, ref := range refs {
		m, err := r.getManifest(context.Background(), ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range m.Entries {
			for _, c := range e.Chunks {
				if _, ok := b.fake.Object("backups", "socrates/backup/v1/"+chunkDir+c); !ok {
					t.Fatalf("snapshot %s refers to missing chunk %s", ref.ID, short(c))
				}
			}
		}
	}
	// The run after uploads exactly the big file's chunks the failed run did
	// not get to, and makes the second snapshot.
	uploaded := len(b.keys(chunkDir)) - before
	info, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Chunks != 3-uploaded || len(b.keys(snapshotDir)) != 2 {
		t.Fatalf("the run after: %+v, with %d of the big file's chunks uploaded before it", info, uploaded)
	}
	if st := e.Status(); st.LastError != "" {
		t.Fatalf("the error outlived a good run: %q", st.LastError)
	}
}

// The mirror's s3.json becomes backup.json, in the same bucket under a
// prefix of its own, and so does a SOCRATES_S3_* environment.
func TestTheMirrorsConfigurationIsCarriedOver(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, paths.LegacyS3Config), []byte(`{"endpoint":"https://hel1.example.com","region":"hel1",
		"bucket":"b1","prefix":"socrates/","access_key_id":"AK","secret_access_key":"SK","path_style":true,"passphrase":"pp"}`), 0o600)
	migrated, err := MigrateConfig(dir, func(string) string { return "" })
	if err != nil || !migrated {
		t.Fatalf("migrate: %v %v", migrated, err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil || cfg == nil {
		t.Fatalf("load: %v %v", cfg, err)
	}
	if cfg.Bucket != "b1" || cfg.Prefix != "socrates/backup/v1/" || cfg.SecretAccessKey != "SK" || cfg.Passphrase != "pp" ||
		cfg.HostID == "" || cfg.IntervalMinutes != 10 || !cfg.PathStyle {
		t.Fatalf("carried over: %+v", cfg)
	}
	if info, err := os.Stat(filepath.Join(dir, paths.BackupConfig)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup.json: %v %v", info, err)
	}
	if again, err := MigrateConfig(dir, func(string) string { return "" }); err != nil || again {
		t.Fatalf("a second migration: %v %v", again, err)
	}

	env := map[string]string{"SOCRATES_S3_BUCKET": "b2", "SOCRATES_S3_ACCESS_KEY_ID": "AK2",
		"SOCRATES_S3_SECRET_ACCESS_KEY": "SK2", "SOCRATES_S3_PASSPHRASE": "pp2"}
	dir2 := t.TempDir()
	if migrated, err := MigrateConfig(dir2, func(k string) string { return env[k] }); err != nil || !migrated {
		t.Fatalf("migrate from the environment: %v %v", migrated, err)
	}
	cfg, _ = LoadConfig(dir2)
	if cfg.Bucket != "b2" || cfg.Prefix != DefaultPrefix || cfg.Passphrase != "pp2" {
		t.Fatalf("from the environment: %+v", cfg)
	}
	if migrated, _ := MigrateConfig(t.TempDir(), func(string) string { return "" }); migrated {
		t.Fatal("a directory with nothing to carry over got a backup.json")
	}
}

// Status warns once no backup has succeeded for a day, and the warning goes
// with the next good run.
func TestStatusWarnsAfterADayWithoutABackup(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/a"), []byte("a"), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if w := e.Warning(); w != "" {
		t.Fatalf("a new engine warns: %q", w)
	}
	clk.Add(25 * time.Hour)
	if w := e.Warning(); !strings.Contains(w, "No backup has succeeded") {
		t.Fatalf("after a day with no backup: %q", w)
	}
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := e.Status(); st.Warning != "" || st.LastSuccess == nil {
		t.Fatalf("after a good run: %+v", st)
	}
	b.fake.SetDown(http.StatusServiceUnavailable)
	clk.Add(25 * time.Hour)
	write(t, filepath.Join(src, "chats/c1/a"), []byte("b"), 0o600)
	if _, err := e.Run(context.Background()); err == nil {
		t.Fatal("a run against a bucket that is down succeeded")
	}
	st := e.Status()
	if !strings.Contains(st.Warning, "last successful backup was 25 hours ago") || st.LastError == "" {
		t.Fatalf("a day after the last good run: %+v", st)
	}
}

// Only one run at a time: a second one asked for meanwhile is ErrBusy, and
// Close waits for the one that runs.
func TestOneRunAtATime(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "agents/codex/big"), random(t, 2*ChunkSize), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if err := e.RunNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Running {
		t.Fatal("RunNow returned before Status said it runs")
	}
	if _, err := e.Run(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second run: %v", err)
	}
	if err := e.RunNow(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second RunNow: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.Status(); st.Running || st.LastSuccess == nil {
		t.Fatalf("after Close: %+v", st)
	}
}

// A chat's journal and launch spec are backed up, the spec without the
// environment its account put there; the host's lock, log and socket are not.
func TestAChatIsBackedUpWithoutItsSecrets(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	src := t.TempDir()
	write(t, filepath.Join(src, "chats/c1/events.jsonl"), []byte(`{"seq":1,"type":"turn.started"}`+"\n"), 0o600)
	write(t, filepath.Join(src, "chats/c1/host.json"), []byte(`{"chat_id":"c1","harness":"claude","env":["ANTHROPIC_API_KEY=sk-ant-secret"]}`), 0o600)
	write(t, filepath.Join(src, "chats/c1/host.lock"), []byte("4242"), 0o600)
	write(t, filepath.Join(src, "chats/c1/host.log"), []byte("serving c1"), 0o600)
	write(t, filepath.Join(src, "chats/c1/agent/state.json"), []byte(`{"thread":"t1"}`), 0o600)
	write(t, filepath.Join(src, "run/chats/c1.sock"), []byte(""), 0o600)
	e := engine(t, src, b, clk, "host-a")
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, key := range b.keys("") {
		if obj, _ := b.fake.Object("backups", key); bytes.Contains(obj, []byte("sk-ant-secret")) {
			t.Fatalf("%s holds the key", key)
		}
	}
	dest := restoreInto(t, b, "latest")
	spec, err := os.ReadFile(filepath.Join(dest, "chats/c1/host.json"))
	if err != nil || bytes.Contains(spec, []byte("sk-ant-secret")) || !bytes.Contains(spec, []byte(`"harness": "claude"`)) {
		t.Fatalf("the restored spec: %s %v", spec, err)
	}
	for rel, want := range map[string]bool{
		"chats/c1/events.jsonl": true, "chats/c1/agent/state.json": true,
		"chats/c1/host.lock": false, "chats/c1/host.log": false, "run": false,
	} {
		if _, err := os.Stat(filepath.Join(dest, rel)); (err == nil) != want {
			t.Errorf("%s restored: %v, want %v", rel, err == nil, want)
		}
	}
}
