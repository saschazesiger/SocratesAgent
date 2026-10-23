package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

const (
	testBucket     = "socrates-test"
	testPassphrase = "correct horse battery staple"
)

// env is one fake bucket that several "machines" (stores) talk to.
type env struct {
	t    *testing.T
	fake *s3fake.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	fake := s3fake.Start(s3fake.Options{})
	t.Cleanup(fake.Close)
	return &env{t: t, fake: fake}
}

func (e *env) cfg(prefix string) Config {
	sc := e.fake.Config(testBucket)
	return Config{
		Endpoint: sc.Endpoint, Region: sc.Region, Bucket: testBucket, Prefix: prefix,
		AccessKeyID: sc.AccessKeyID, SecretAccessKey: sc.SecretAccessKey,
		PathStyle: true, Passphrase: testPassphrase,
	}
}

// opts are the knobs of a test mirror: a cheap key derivation and timings in
// milliseconds. The heartbeat is an hour, so that only the test renews.
func (e *env) opts(instance string) Options {
	return Options{
		Instance: instance, Host: "host-" + instance, Version: "test",
		Iterations: 1000, Debounce: 20 * time.Millisecond, MaxLatency: 100 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond, Heartbeat: time.Hour, Settle: time.Millisecond,
		TakeoverSettle: time.Millisecond, OpTimeout: 5 * time.Second,
		Logf: e.t.Logf,
	}
}

// object reads and opens one object from the fake bucket.
func (e *env) object(prefix, name string, key *vault.Key) ([]byte, bool) {
	e.t.Helper()
	raw, ok := e.fake.Object(testBucket, prefix+name)
	if !ok {
		return nil, false
	}
	plain, err := vault.Open(key, name, raw)
	if err != nil {
		e.t.Fatalf("%s does not open: %v", name, err)
	}
	return plain, true
}

func (e *env) has(prefix, name string) bool {
	_, ok := e.fake.Object(testBucket, prefix+name)
	return ok
}

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seed fills a store with one of everything a restore has to bring back,
// plus the rows that must stay local.
func seed(t *testing.T, st *store.Store) (running, prepared string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetKV(PasswordKey, "pbkdf2-sha256$1$c2FsdA$aGFzaA"))
	must(st.SetKV("settings", `{"voice":{"language":"de"}}`))
	must(st.SetKV("profiles.order", `["a","b"]`))
	must(st.SetKV("agents_catalog", `{"cached":true}`))
	must(st.SetKV("resume_note:x", `{}`))
	must(st.CreateLogin("token-1", time.Hour))
	row := &store.Session{
		ID: "run1", Title: "Running one", Harness: "claude", Workdir: "/w/a", WorkdirMode: store.WorkdirCustom,
		TmuxName: "socrates-run1", CLISessionID: "cli-abc", CLISessionState: store.CLIKnown,
		State: store.StateRunning, Options: json.RawMessage(`{"profile":"p1"}`),
	}
	must(st.CreateSession(row))
	pre := &store.Session{
		ID: "pre1", Title: "Prepared", Harness: "codex", Workdir: "", WorkdirMode: store.WorkdirDynamic,
		State: store.StatePrestart, StartAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	must(st.CreateSession(pre))
	if _, err := st.SaveDraft("pre1", "write the tests\nsecond line", 0); err != nil {
		t.Fatal(err)
	}
	return row.ID, pre.ID
}

func connect(t *testing.T, e *env, st *store.Store, prefix, instance string) *Mirror {
	t.Helper()
	m, err := Connect(context.Background(), st, e.cfg(prefix), e.opts(instance))
	if err != nil {
		t.Fatalf("connect %s: %v", instance, err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	return m
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- config

func TestConfigFromTheEnvironmentWinsOverTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{Bucket: "from-file", AccessKeyID: "a", SecretAccessKey: "s", Passphrase: "p", Prefix: "x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ConfigPath(dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("s3.json must be 0600: %v %v", info.Mode(), err)
	}
	cfg, src, err := loadConfig(dir, func(string) string { return "" })
	if err != nil || src != SourceFile || cfg.Bucket != "from-file" || cfg.Prefix != "x/" || cfg.Region != "us-east-1" {
		t.Fatalf("file config = %+v %q %v", cfg, src, err)
	}
	env := map[string]string{
		EnvBucket: "from-env", EnvPrefix: "/a/b//", EnvAccessKey: "k", EnvSecretKey: "s", EnvPassphrase: "p",
	}
	cfg, src, err = loadConfig(dir, func(k string) string { return env[k] })
	if err != nil || src != SourceEnv || cfg.Bucket != "from-env" || cfg.Prefix != "a/b/" || !cfg.PathStyle {
		t.Fatalf("env config = %+v %q %v", cfg, src, err)
	}
	env[EnvPathStyle] = "false"
	cfg, _, _ = loadConfig(dir, func(k string) string { return env[k] })
	if cfg.PathStyle {
		t.Fatal("SOCRATES_S3_PATH_STYLE=false was ignored")
	}
	env[EnvPathStyle] = "maybe"
	if _, _, err := loadConfig(dir, func(k string) string { return env[k] }); err == nil {
		t.Fatal("a nonsense path style was accepted")
	}
	if _, src, err := loadConfig(t.TempDir(), func(string) string { return "" }); src != SourceNone || err != nil {
		t.Fatalf("an empty data dir is unconfigured, got %q %v", src, err)
	}
	for in, want := range map[string]string{"": "", "/": "", "a": "a/", "a/b/": "a/b/", " /a/b// ": "a/b/"} {
		if got := NormalizePrefix(in); got != want {
			t.Errorf("NormalizePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// ------------------------------------------------------- migration upload

// The upgrade path of an existing installation: an existing database, a
// bucket with nothing in it. Everything goes up, sealed, and nothing that
// is local-only does.
func TestMigrationUploadsAnExistingDatabase(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	running, prepared := seed(t, st)
	m := connect(t, e, st, "", "a")

	if !e.has("", metaObject) || !e.has("", leaseObject) {
		t.Fatal("meta.json and lease.json must exist")
	}
	raw, _ := e.fake.Object(testBucket, metaObject)
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.BucketID == "" || meta.Iterations != 1000 || meta.KDF != vault.KDF {
		t.Fatalf("meta.json = %s (%v)", raw, err)
	}
	key, err := meta.Key(testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := e.object("", store.KVObject(PasswordKey), key); !ok || !strings.HasPrefix(string(v), "pbkdf2") {
		t.Fatalf("password hash = %q %v", v, ok)
	}
	if v, ok := e.object("", store.KVObject("profiles.order"), key); !ok || string(v) != `["a","b"]` {
		t.Fatalf("profiles.order = %q", v)
	}
	for _, local := range []string{"agents_catalog", "resume_note:x", kvInstance, kvBucketID} {
		if e.has("", store.KVObject(local)) {
			t.Errorf("%s is local-only and must not be uploaded", local)
		}
	}
	v, ok := e.object("", store.SessionObject(running), key)
	if !ok || !strings.Contains(string(v), `"cli_session_id":"cli-abc"`) || !strings.Contains(string(v), `"tmux_name":"socrates-run1"`) {
		t.Fatalf("session row = %s", v)
	}
	if v, ok := e.object("", store.DraftObject(prepared), key); !ok || !strings.Contains(string(v), "write the tests") {
		t.Fatalf("draft = %s", v)
	}
	if v, ok := e.object("", store.LoginsObject, key); !ok || !strings.Contains(string(v), "token-1") {
		t.Fatalf("logins = %s", v)
	}
	// Sealed with the object name as additional data: an object moved to
	// another name does not open.
	sealed, _ := e.fake.Object(testBucket, store.KVObject(PasswordKey))
	if _, err := vault.Open(key, store.KVObject("settings"), sealed); err == nil {
		t.Fatal("an object opened under another name")
	}
	if n, _ := st.PendingCount(); n != 0 {
		t.Fatalf("%d objects still pending after the migration", n)
	}
	if got, _ := st.GetKV(kvBucketID); got != meta.BucketID {
		t.Fatalf("mirror.bucket_id = %q, want %q", got, meta.BucketID)
	}
	if s := m.Status(); s.State != "ok" || !s.Connected || s.Fenced || s.BucketID != meta.BucketID {
		t.Fatalf("status = %+v", s)
	}
}

// --------------------------------------------------------------- hydrate

// A new machine with an empty data directory and the same bucket comes up
// with everything: password, settings, sign-ins, every session row with its
// hidden fields, and the prepared session's draft.
func TestHydrateIntoAnEmptyDataDirReproducesTheState(t *testing.T) {
	e := newEnv(t)
	a := openStore(t, t.TempDir())
	running, prepared := seed(t, a)
	ma := connect(t, e, a, "inst/", "a")
	if err := ma.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var released Lease
	raw, _ := e.fake.Object(testBucket, "inst/"+leaseObject)
	if err := json.Unmarshal(raw, &released); err != nil || released.Instance != "a" || released.HeartbeatAt != 0 {
		t.Fatalf("a graceful close must release the lease (heartbeat zero, instance kept): %s", raw)
	}

	b := openStore(t, t.TempDir())
	connect(t, e, b, "inst/", "b")

	for _, key := range []string{PasswordKey, "settings", "profiles.order"} {
		want, _ := a.GetKV(key)
		if got, err := b.GetKV(key); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", key, got, err, want)
		}
	}
	if _, err := b.GetKV("agents_catalog"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a local-only row came down from the bucket")
	}
	if !b.ValidLogin("token-1") {
		t.Error("the sign-in did not come across")
	}
	want, _ := a.GetSession(running)
	got, err := b.GetSession(running)
	if err != nil {
		t.Fatal(err)
	}
	if got.TmuxName != want.TmuxName || got.CLISessionID != "cli-abc" || got.CLISessionState != store.CLIKnown ||
		got.State != store.StateRunning || got.Title != want.Title || string(got.Options) != string(want.Options) ||
		got.CreatedAt != want.CreatedAt {
		t.Fatalf("restored row = %+v, want %+v", got, want)
	}
	pre, err := b.GetSession(prepared)
	if err != nil || pre.State != store.StatePrestart || pre.StartAt == 0 {
		t.Fatalf("prepared row = %+v %v", pre, err)
	}
	draft, err := b.GetDraft(prepared)
	if err != nil || draft.Text != "write the tests\nsecond line" || draft.Rev != 1 {
		t.Fatalf("draft = %+v %v", draft, err)
	}
	if n, _ := b.PendingCount(); n != 0 {
		t.Fatalf("a hydrate left %d objects pending: a download is not a change", n)
	}
}

// The bucket wins over a local copy of another installation: rows the bucket
// does not have are removed, and so are that installation's sign-ins.
func TestHydrateReplacesTheStateOfAnotherInstallation(t *testing.T) {
	e := newEnv(t)
	a := openStore(t, t.TempDir())
	seed(t, a)
	connect(t, e, a, "", "a").Close(context.Background())

	other := openStore(t, t.TempDir())
	if err := other.SetKV(PasswordKey, "other-hash"); err != nil {
		t.Fatal(err)
	}
	if err := other.CreateLogin("other-token", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := other.CreateSession(&store.Session{ID: "stray", Harness: "shell", Workdir: "/", WorkdirMode: store.WorkdirCustom,
		TmuxName: "socrates-run1", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	connect(t, e, other, "", "other")
	if v, _ := other.GetKV(PasswordKey); v == "other-hash" {
		t.Fatal("the local password survived a restore from the bucket")
	}
	if _, err := other.GetSession("stray"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a row the bucket does not have survived")
	}
	if other.ValidLogin("other-token") || !other.ValidLogin("token-1") {
		t.Fatal("the sign-ins are not the bucket's")
	}
	if _, err := other.GetSession("run1"); err != nil {
		t.Fatalf("the bucket's row (with the same tmux name as the stray one) is missing: %v", err)
	}
}

// Rows restored on another machine name tmux sessions that do not exist there.
// The ordinary reconcile turns them into sessions to resume, and a resume of
// a conversation whose transcript stayed on the old machine is a fresh one:
// the transcript check says it is gone.
func TestHydratedRowsBecomeNeedsResumeAndAMissingTranscriptIsLost(t *testing.T) {
	e := newEnv(t)
	a := openStore(t, t.TempDir())
	running, _ := seed(t, a)
	connect(t, e, a, "", "a").Close(context.Background())

	// A short directory: tmux sockets have a path limit.
	dir, err := os.MkdirTemp("", "mir")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := openStore(t, dir)
	connect(t, e, b, "", "b")

	mgr, err := termux.New(b, termux.Config{DataDir: dir, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	if mgr.Available() != nil {
		t.Skipf("tmux is not usable here: %v", mgr.Available())
	}
	if err := mgr.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, _ := b.GetSession(running)
	if row.State != store.StateNeedsResume {
		t.Fatalf("a restored running row is %q, want needs_resume", row.State)
	}
	if pre, _ := b.GetSession("pre1"); pre.State != store.StatePrestart {
		t.Fatalf("the reconcile touched the prepared row: %q", pre.State)
	}
	h, _ := harnesses.Get(row.Harness)
	alive, err := h.VerifyCLISession(context.Background(), harnesses.PlanRequest{
		SessionID: row.ID, CLISession: row.CLISessionID, DataDir: dir,
	})
	if err != nil || alive {
		t.Fatalf("a transcript that is not on this machine verified as alive=%v (%v)", alive, err)
	}
}

// ----------------------------------------------------- pending and crashes

// A write that was committed and not uploaded survives a crash: the pending
// mark is in the database, and the next start uploads it before it downloads
// anything - the local value is the newer one.
func TestPendingSurvivesACrash(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	seed(t, st)
	m, err := Connect(context.Background(), st, e.cfg(""), e.opts("a"))
	if err != nil {
		t.Fatal(err)
	}
	// No flusher runs: this write is committed and never uploaded.
	if err := st.SetKV("settings", `{"after":"crash"}`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionTitle("run1", "Renamed before the crash"); err != nil {
		t.Fatal(err)
	}
	// A row that never reached the bucket at all.
	if err := st.CreateSession(&store.Session{ID: "new1", Harness: "shell", Workdir: "/", WorkdirMode: store.WorkdirCustom,
		TmuxName: "socrates-new1", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	_ = m // the crash: no Close, no release
	st.Close()

	st2 := openStore(t, dir)
	pending, err := st2.ListPending()
	if err != nil || len(pending) != 3 {
		t.Fatalf("pending after the crash = %+v %v", pending, err)
	}
	m2 := connect(t, e, st2, "", "a") // same instance: its own lease, taken at once
	key := m2.key
	if v, _ := e.object("", store.KVObject("settings"), key); string(v) != `{"after":"crash"}` {
		t.Fatalf("the bucket has settings %s, want the write from before the crash", v)
	}
	if v, _ := st2.GetKV("settings"); v != `{"after":"crash"}` {
		t.Fatalf("the download undid the local write: %s", v)
	}
	if row, _ := st2.GetSession("run1"); row.Title != "Renamed before the crash" {
		t.Fatalf("title = %q", row.Title)
	}
	if _, err := st2.GetSession("new1"); err != nil {
		t.Fatalf("a row that had never been uploaded was lost by the start after the crash: %v", err)
	}
	if !e.has("", store.SessionObject("new1")) {
		t.Fatal("the never-uploaded row is not in the bucket")
	}
	if n, _ := st2.PendingCount(); n != 0 {
		t.Fatalf("%d still pending", n)
	}
}

// A bucket whose objects are gone (meta.json still there) never empties the
// local copy: its state goes up again instead.
func TestAnEmptiedBucketDoesNotEmptyTheLocalCopy(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	connect(t, e, st, "", "a").Close(context.Background())
	for _, key := range e.fake.Keys(testBucket, "") {
		if key != metaObject {
			e.fake.DeleteObject(testBucket, key)
		}
	}
	connect(t, e, st, "", "a")
	if _, err := st.GetSession("run1"); err != nil {
		t.Fatalf("the local copy lost a row to an emptied bucket: %v", err)
	}
	if !e.has("", store.SessionObject("run1")) || !e.has("", store.KVObject(PasswordKey)) {
		t.Fatal("the local state was not uploaded again")
	}
}

// The running flusher uploads writes, coalesces a burst into one round, and
// turns a deleted row into a deleted object.
func TestTheFlusherUploadsAndDeletes(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	m := connect(t, e, st, "p/", "a")
	m.Start(context.Background())
	key := m.key

	puts := e.fake.Requests("PUT")
	for i := 0; i < 20; i++ {
		if err := st.SetKV("burst", strings.Repeat("x", i)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the burst to be uploaded", func() bool {
		v, ok := e.object("p/", store.KVObject("burst"), key)
		return ok && len(v) == 19
	})
	if n := e.fake.Requests("PUT") - puts; n > 4 {
		t.Errorf("twenty writes in a burst took %d uploads; they should coalesce", n)
	}

	if err := st.DeleteSession("pre1"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteKV("profiles.order"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the deletes to reach the bucket", func() bool {
		return !e.has("p/", store.SessionObject("pre1")) && !e.has("p/", store.DraftObject("pre1")) &&
			!e.has("p/", store.KVObject("profiles.order"))
	})
	eventually(t, "nothing pending", func() bool { n, _ := st.PendingCount(); return n == 0 })
	if !m.IsClean(store.SessionObject("run1")) {
		t.Error("an uploaded row is not reported as backed up")
	}
	if s := m.Status(); s.LastFlushAt == 0 || s.State != "ok" {
		t.Errorf("status = %+v", s)
	}
}

// A store that fails keeps the writes pending and the error visible, and the
// writes go up once it answers again.
func TestAFailingStoreKeepsWritesPending(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	m := connect(t, e, st, "", "a")
	m.Start(context.Background())

	e.fake.SetDown(http.StatusServiceUnavailable)
	if err := st.SetKV("settings", `{"while":"down"}`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the error to show", func() bool { s := m.Status(); return s.State == "offline" && s.LastError != "" })
	if s := m.Status(); s.Pending == 0 || s.ErrorSince == 0 || s.Fenced {
		t.Fatalf("status while down = %+v", s)
	}
	e.fake.SetDown(0)
	eventually(t, "the write to go up", func() bool {
		v, ok := e.object("", store.KVObject("settings"), m.key)
		return ok && string(v) == `{"while":"down"}`
	})
	eventually(t, "the status to recover", func() bool { s := m.Status(); return s.State == "ok" && s.LastError == "" })
}

// ------------------------------------------------------------------ lease

func newLeaser(e *env, instance string, cond s3.Conditional, now func() time.Time) *leaser {
	client := e.fake.S3Client(testBucket)
	return &leaser{
		b:        func() bucket { return bucket{c: client, prefix: "l/", timeout: 5 * time.Second} },
		instance: instance, host: "host-" + instance, version: "test",
		now: now, ttl: 90 * time.Second, settle: time.Millisecond, cond: cond,
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// The whole life of a lease, once on a store that enforces conditional
// writes and once on one that ignores them (the read-back fallback).
func TestLeaseLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode s3fake.Conditional
		cond s3.Conditional
	}{
		{"conditional", s3fake.Honour, s3.Conditional{IfNoneMatch: true, IfMatch: true}},
		{"read-back", s3fake.Ignore, s3.Conditional{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.fake.SetConditional(tc.mode)
			clk := &clock{t: time.Unix(1_800_000_000, 0)}
			a := newLeaser(e, "a", tc.cond, clk.now)
			b := newLeaser(e, "b", tc.cond, clk.now)
			ctx := context.Background()

			// Acquire an absent lease.
			if _, err := a.acquire(ctx, false); err != nil {
				t.Fatal(err)
			}
			// A fresh lease of somebody else is refused, and says who.
			var held *LeaseHeldError
			if _, err := b.acquire(ctx, false); !errors.As(err, &held) || held.Lease.Host != "host-a" {
				t.Fatalf("b took a fresh lease: %v", err)
			}
			// Renew moves the heartbeat.
			clk.add(30 * time.Second)
			if err := a.renew(ctx); err != nil {
				t.Fatal(err)
			}
			cur, _, _, _ := a.read(ctx)
			if cur.HeartbeatAt != clk.now().UnixMilli() || cur.Instance != "a" {
				t.Fatalf("after renew: %+v", cur)
			}
			// Still fresh 89 s later; expired at 90.
			clk.add(89 * time.Second)
			if _, err := b.acquire(ctx, false); !errors.As(err, &held) {
				t.Fatalf("b took a lease 89 s old: %v", err)
			}
			clk.add(time.Second)
			// a never renewed, so its own verify no longer confirms anything:
			// an expired lease is one anybody may be taking right now.
			if err := a.verify(ctx); !errors.Is(err, errLeaseExpired) {
				t.Fatalf("verify of an expired own lease: %v", err)
			}
			took, err := b.acquire(ctx, false)
			if err != nil {
				t.Fatalf("b could not take an expired lease: %v", err)
			}
			// a never let go of it: b is told, to wait for a's last writes.
			if took.Instance != "a" || b.lastWriterWasUs() {
				t.Fatalf("b took %+v (last writer us: %v)", took, b.lastWriterWasUs())
			}
			// a comes back: its heartbeat finds the lease gone to b and fences.
			if err := a.renew(ctx); !errors.Is(err, ErrFenced) {
				t.Fatalf("a renewed a lease b holds: %v", err)
			}
			if _, held, _, other := a.state(); held || other.Instance != "b" {
				t.Fatalf("a's state after fencing: held=%v other=%+v", held, other)
			}
			if err := a.verify(ctx); !errors.Is(err, ErrFenced) {
				t.Fatalf("verify: %v", err)
			}
			// a takes it over by force (the connect page with the password),
			// and b is fenced at its next verify.
			if _, err := a.acquire(ctx, true); err != nil {
				t.Fatal(err)
			}
			if err := b.verify(ctx); !errors.Is(err, ErrFenced) {
				t.Fatalf("b did not notice the takeover: %v", err)
			}
			// A restart of the same instance takes its own fresh lease.
			a2 := newLeaser(e, "a", tc.cond, clk.now)
			if took, err := a2.acquire(ctx, false); err != nil || took.Instance != "" || !a2.lastWriterWasUs() {
				t.Fatalf("own lease: %+v %v", took, err)
			}
			// Release touches only a lease that is ours.
			if err := b.release(ctx); err != nil {
				t.Fatal(err)
			}
			if _, exists, _ := a2.peek(ctx); !exists {
				t.Fatal("b released a's lease")
			}
			if err := a2.release(ctx); err != nil {
				t.Fatal(err)
			}
			// A released lease is free - the heartbeat is zero - and still
			// says whose it was: the record of the last writer.
			cur, exists, _ := a2.peek(ctx)
			if !exists || cur.Instance != "a" || cur.HeartbeatAt != 0 || cur.Fresh(clk.now(), 90*time.Second) {
				t.Fatalf("after release: %+v exists=%v", cur, exists)
			}
			// b takes it at once, with nothing to wait for, and learns that
			// somebody else wrote last; a restarted a learns it was itself.
			if took, err := b.acquire(ctx, false); err != nil || took.Instance != "" || b.lastWriterWasUs() {
				t.Fatalf("b after a released: %+v %v us=%v", took, err, b.lastWriterWasUs())
			}
			if err := b.release(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := a2.acquire(ctx, false); err != nil || a2.lastWriterWasUs() {
				t.Fatalf("a after b released: %v us=%v", err, a2.lastWriterWasUs())
			}
		})
	}
}

// Two instances racing for an absent lease on a store that enforces
// conditions: exactly one wins.
func TestConditionalLeaseRaceHasOneWinner(t *testing.T) {
	e := newEnv(t)
	cond := s3.Conditional{IfNoneMatch: true, IfMatch: true}
	var wins, losses int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := newLeaser(e, string(rune('a'+i)), cond, time.Now)
			_, err := l.acquire(context.Background(), false)
			mu.Lock()
			defer mu.Unlock()
			var held *LeaseHeldError
			switch {
			case err == nil:
				wins++
			case errors.As(err, &held):
				losses++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || losses != 7 {
		t.Fatalf("wins=%d losses=%d", wins, losses)
	}
}

// A running mirror whose bucket is taken over fences itself: no more
// uploads, a status that says so, and no scheduled starts.
func TestARunningMirrorIsFencedByATakeover(t *testing.T) {
	for _, mode := range []s3fake.Conditional{s3fake.Honour, s3fake.Ignore} {
		e := newEnv(t)
		e.fake.SetConditional(mode)
		st := openStore(t, t.TempDir())
		seed(t, st)
		m := connect(t, e, st, "", "a")
		if mode == s3fake.Ignore && m.Status().ConditionalWrites {
			t.Fatal("the probe missed that the store ignores conditions")
		}

		other := openStore(t, t.TempDir())
		res, err := Attach(context.Background(), other, e.cfg(""), e.opts("b"), AttachRequest{
			Password: "secret", Resolve: ResolveBucket,
			VerifyPassword: func(hash, pw string) bool { return pw == "secret" && strings.HasPrefix(hash, "pbkdf2") },
		})
		if err != nil || !res.TookOver {
			t.Fatalf("takeover: %+v %v", res, err)
		}
		if err := st.SetKV("settings", `{"after":"takeover"}`); err != nil {
			t.Fatal(err)
		}
		// Force the verify that precedes an upload round.
		m.lease.mu.Lock()
		m.lease.confirmedAt = time.Time{}
		m.lease.mu.Unlock()
		if err := m.Sync(context.Background()); !errors.Is(err, ErrFenced) {
			t.Fatalf("sync after a takeover: %v", err)
		}
		s := m.Status()
		if !s.Fenced || s.State != "fenced" || s.Lease == nil || s.Lease.Host != "host-b" || s.Lease.Own {
			t.Fatalf("status = %+v lease=%+v", s, s.Lease)
		}
		if m.AutoStartAllowed() == nil {
			t.Fatal("a fenced mirror allows scheduled starts")
		}
		if m.IsClean(store.SessionObject("run1")) || st.IsBackedUp(store.SessionObject("run1")) {
			t.Fatal("a fenced mirror reports a row as backed up")
		}
		if v, _ := e.object("", store.KVObject("settings"), m.key); strings.Contains(string(v), "takeover") {
			t.Fatal("a fenced mirror uploaded")
		}
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if cur, _, _ := m.lease.peek(context.Background()); cur.Instance != "b" {
			t.Fatalf("a fenced mirror's close touched the new holder's lease: %+v", cur)
		}
	}
}

// ---------------------------------------------------- passphrase, degraded

func TestWrongPassphraseIsRefused(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	connect(t, e, st, "", "a").Close(context.Background())

	cfg := e.cfg("")
	cfg.Passphrase = "not the passphrase"
	if _, err := Connect(context.Background(), openStore(t, t.TempDir()), cfg, e.opts("b")); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Connect with a wrong passphrase: %v", err)
	}
	if Transient(ErrWrongPassphrase) {
		t.Fatal("a wrong passphrase must not count as unreachable")
	}
	if _, err := Attach(context.Background(), openStore(t, t.TempDir()), cfg, e.opts("b"), AttachRequest{}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Attach with a wrong passphrase: %v", err)
	}
}

// A server whose bucket is down at startup starts from its local copy when
// that copy was synchronised with this very bucket; its writes queue up and
// go up once the bucket answers.
func TestDegradedStart(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	st := openStore(t, dir)
	seed(t, st)
	connect(t, e, st, "d/", "a").Close(context.Background())

	e.fake.SetDown(http.StatusServiceUnavailable)
	_, err := Connect(context.Background(), st, e.cfg("d/"), e.opts("a"))
	if !Transient(err) {
		t.Fatalf("a 503 at startup is not transient: %v", err)
	}
	if !CanDegrade(st, e.cfg("d/")) {
		t.Fatal("the copy of this bucket may start degraded")
	}
	if CanDegrade(st, e.cfg("elsewhere/")) || CanDegrade(openStore(t, t.TempDir()), e.cfg("d/")) {
		t.Fatal("a copy of another place, or no copy, must not start degraded")
	}
	bad := e.cfg("d/")
	bad.Passphrase = "wrong"
	if _, err := Degraded(st, bad, e.opts("a"), err); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("a degraded start with a wrong passphrase: %v", err)
	}
	m, err := Degraded(st, e.cfg("d/"), e.opts("a"), err)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	if s := m.Status(); s.State != "offline" || s.Connected || s.LastError == "" {
		t.Fatalf("degraded status = %+v", s)
	}
	// Degraded, it cannot know whether another machine took the bucket
	// meanwhile: the schedules wait until the lease is taken in this run.
	if m.AutoStartAllowed() == nil {
		t.Fatal("a degraded server that never reached its bucket starts scheduled sessions")
	}
	m.Start(context.Background())
	if err := st.SetKV("settings", `{"written":"offline"}`); err != nil {
		t.Fatal(err)
	}
	if !st.IsPending(store.KVObject("settings")) {
		t.Fatal("a write while degraded is not pending")
	}
	e.fake.SetDown(0)
	eventually(t, "the queued write to go up", func() bool {
		v, ok := e.object("d/", store.KVObject("settings"), m.key)
		return ok && string(v) == `{"written":"offline"}`
	})
	eventually(t, "the lease to be held", func() bool { return m.Status().State == "ok" })
	if err := m.AutoStartAllowed(); err != nil {
		t.Fatalf("scheduled starts are still held back once the lease is taken: %v", err)
	}
}

// A degraded server that reaches its bucket after somebody else used it does
// not upload its queue over their work: it is fenced.
func TestDegradedServerFindsTheBucketTakenAndFences(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	connect(t, e, st, "", "a").Close(context.Background())

	m, err := Degraded(st, e.cfg(""), e.opts("a"), errors.New("down"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	connect(t, e, openStore(t, t.TempDir()), "", "b") // someone else, holding the lease
	if err := st.SetKV("settings", `{"stale":true}`); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(context.Background()); !errors.Is(err, ErrFenced) {
		t.Fatalf("sync: %v", err)
	}
	if !m.Fenced() {
		t.Fatal("not fenced")
	}
}

// ---------------------------------------------------------- connect page

func TestAttach(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	verify := func(hash, pw string) bool { return pw == "secret" }

	// A place without state: nothing is created until the passphrase was
	// confirmed.
	local := openStore(t, t.TempDir())
	seed(t, local)
	if _, err := Attach(ctx, local, e.cfg("new/"), e.opts("a"), AttachRequest{}); !errors.Is(err, ErrNewBucket) {
		t.Fatalf("unconfirmed new bucket: %v", err)
	}
	if e.has("new/", metaObject) {
		t.Fatal("meta.json was created without a confirmation")
	}
	res, err := Attach(ctx, local, e.cfg("new/"), e.opts("a"), AttachRequest{ConfirmNew: true})
	if err != nil || !res.Created {
		t.Fatalf("confirmed new bucket: %+v %v", res, err)
	}
	// The restart that follows uploads the local installation.
	m := connect(t, e, local, "new/", "a")
	if !e.has("new/", store.SessionObject("run1")) {
		t.Fatal("the local installation was not uploaded to the new bucket")
	}
	m.Close(ctx)

	// Another machine with its own installation: a conflict, until it says
	// which side wins.
	mine := openStore(t, t.TempDir())
	if err := mine.SetKV(PasswordKey, "mine"); err != nil {
		t.Fatal(err)
	}
	if err := mine.CreateSession(&store.Session{ID: "mine1", Harness: "shell", Workdir: "/", WorkdirMode: store.WorkdirCustom, State: store.StateExited}); err != nil {
		t.Fatal(err)
	}
	var conflict *ConflictError
	_, err = Attach(ctx, mine, e.cfg("new/"), e.opts("b"), AttachRequest{})
	if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) || conflict.BucketSessions != 2 || conflict.LocalSessions != 1 {
		t.Fatalf("conflict: %v %+v", err, conflict)
	}
	res, err = Attach(ctx, mine, e.cfg("new/"), e.opts("b"), AttachRequest{Resolve: ResolveOverwrite})
	if err != nil || !res.Overwrote {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	if e.has("new/", store.SessionObject("run1")) || !e.has("new/", store.SessionObject("mine1")) {
		t.Fatalf("the bucket was not replaced: %v", e.fake.Keys(testBucket, "new/"))
	}
	mb := connect(t, e, mine, "new/", "b")
	if v, _ := mine.GetKV(PasswordKey); v != "mine" {
		t.Fatalf("after overwrite the local password is %q", v)
	}

	// While b runs, a third machine is told who holds the bucket, and
	// needs the right password to take it.
	third := openStore(t, t.TempDir())
	var held *LeaseHeldError
	if _, err := Attach(ctx, third, e.cfg("new/"), e.opts("c"), AttachRequest{}); !errors.As(err, &held) || held.Lease.Host != "host-b" {
		t.Fatalf("lease held: %v", err)
	}
	if lease, ok, err := Peek(ctx, e.cfg("new/"), e.opts("c")); err != nil || !ok || lease.Host != "host-b" {
		t.Fatalf("peek = %+v %v %v", lease, ok, err)
	}
	if _, err := Attach(ctx, third, e.cfg("new/"), e.opts("c"), AttachRequest{Password: "guess", VerifyPassword: verify}); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	res, err = Attach(ctx, third, e.cfg("new/"), e.opts("c"), AttachRequest{Password: "secret", VerifyPassword: verify})
	if err != nil || !res.TookOver {
		t.Fatalf("takeover: %+v %v", res, err)
	}
	if err := mb.Sync(ctx); !errors.Is(err, ErrFenced) {
		// The verify window has not passed in this test; force it.
		mb.lease.mu.Lock()
		mb.lease.confirmedAt = time.Time{}
		mb.lease.mu.Unlock()
		if err := mb.Sync(ctx); !errors.Is(err, ErrFenced) {
			t.Fatalf("b after the takeover: %v", err)
		}
	}
}

// Credentials can change for the same bucket; pointing them at another
// installation is refused, and so is changing an environment configuration.
func TestUpdateCredentials(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	st := openStore(t, dir)
	opts := e.opts("a")
	opts.DataDir, opts.Source = dir, SourceFile
	m, err := Connect(context.Background(), st, e.cfg("c/"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	cfg := e.cfg("c/")

	bad := Credentials{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKeyID: "nobody", SecretAccessKey: "nothing", PathStyle: true}
	if err := m.UpdateCredentials(context.Background(), bad); err == nil {
		t.Fatal("credentials the store refuses were saved")
	}
	if _, err := os.Stat(ConfigPath(dir)); !os.IsNotExist(err) {
		t.Fatal("s3.json was written for refused credentials")
	}
	good := Credentials{Endpoint: cfg.Endpoint + "/", Region: cfg.Region, AccessKeyID: cfg.AccessKeyID, PathStyle: true}
	if err := m.UpdateCredentials(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	saved, src, err := LoadConfig(dir)
	if err != nil || src != SourceFile || saved.SecretAccessKey != cfg.SecretAccessKey || saved.Prefix != "c/" || saved.Passphrase != testPassphrase {
		t.Fatalf("saved = %+v %q %v", saved, src, err)
	}
	if !CanDegrade(st, saved) {
		t.Fatal("the local identity did not follow the new endpoint")
	}

	m.opts.Source = SourceEnv
	if err := m.UpdateCredentials(context.Background(), good); !errors.Is(err, ErrEnvConfig) {
		t.Fatalf("env config: %v", err)
	}
	if res := m.Test(context.Background()); !res.OK || res.Lease == nil || !res.Lease.Own {
		t.Fatalf("test = %+v", res)
	}
}

// ------------------------------------------------- who wrote the bucket last

// A machine that crashed with a write pending, while another one took the
// bucket, wrote to it and stopped cleanly, must not upload its stale write
// over theirs when it comes back: the lease says somebody else held the
// bucket since, so the bucket wins and the stale write is dropped.
func TestAnOlderWriterDoesNotUploadOverANewerOne(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dirA := t.TempDir()
	stA, err := store.Open(filepath.Join(dirA, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	seed(t, stA)
	mA, err := Connect(ctx, stA, e.cfg(""), e.opts("a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := stA.SetKV("settings", "A1"); err != nil {
		t.Fatal(err)
	}
	if err := mA.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := stA.SetKV("settings", "A-stale"); err != nil {
		t.Fatal(err)
	}
	stA.Close() // the crash: A-stale is pending, the lease still names a

	// Two minutes later another machine finds the lease expired.
	optsB := e.opts("b")
	optsB.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	stB := openStore(t, t.TempDir())
	mB, err := Connect(ctx, stB, e.cfg(""), optsB)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := stB.GetKV("settings"); v != "A1" {
		t.Fatalf("b restored settings %q", v)
	}
	if err := stB.SetKV("settings", "B-new"); err != nil {
		t.Fatal(err)
	}
	if err := mB.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mB.Close(ctx); err != nil {
		t.Fatal(err)
	}

	stA2 := openStore(t, dirA)
	if !stA2.IsPending(store.KVObject("settings")) {
		t.Fatal("the crash should have left the write pending")
	}
	mA2 := connect(t, e, stA2, "", "a")
	if v, _ := e.object("", store.KVObject("settings"), mA2.key); string(v) != "B-new" {
		t.Fatalf("the bucket has settings %q after the old writer came back, want b's", v)
	}
	if v, _ := stA2.GetKV("settings"); v != "B-new" {
		t.Fatalf("the old writer kept %q instead of taking the bucket's state", v)
	}
	if n, _ := stA2.PendingCount(); n != 0 {
		t.Fatalf("%d objects still pending", n)
	}
}

// A machine that was fenced, kept writing locally, and restarts after the new
// holder stopped cleanly takes the bucket's state rather than uploading its
// own; the fence is remembered across the restart.
func TestAFencedWriterDoesNotUploadAfterTheHolderStops(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dirA := t.TempDir()
	stA := openStore(t, dirA)
	seed(t, stA)
	mA := connect(t, e, stA, "", "a")

	stB := openStore(t, t.TempDir())
	res, err := Attach(ctx, stB, e.cfg(""), e.opts("b"), AttachRequest{
		Password: "secret", Resolve: ResolveBucket,
		VerifyPassword: func(hash, pw string) bool { return pw == "secret" },
	})
	if err != nil || !res.TookOver {
		t.Fatalf("takeover: %+v %v", res, err)
	}
	mB := connect(t, e, stB, "", "b")

	if err := stA.SetKV("settings", "A-fenced"); err != nil {
		t.Fatal(err)
	}
	mA.lease.mu.Lock()
	mA.lease.confirmedAt = time.Time{}
	mA.lease.mu.Unlock()
	if err := mA.Sync(ctx); !errors.Is(err, ErrFenced) {
		t.Fatalf("a after the takeover: %v", err)
	}
	if CanDegrade(stA, e.cfg("")) {
		t.Fatal("a fenced copy may still start degraded")
	}
	if err := stB.SetKV("settings", "B-setting"); err != nil {
		t.Fatal(err)
	}
	if err := mB.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mB.Close(ctx); err != nil {
		t.Fatal(err)
	}
	mA.Close(ctx)

	mA2 := connect(t, e, stA, "", "a")
	if v, _ := e.object("", store.KVObject("settings"), mA2.key); string(v) != "B-setting" {
		t.Fatalf("the bucket has settings %q, want the holder's", v)
	}
	if v, _ := stA.GetKV("settings"); v != "B-setting" {
		t.Fatalf("a kept %q", v)
	}
}

// A degraded server whose bucket was used by somebody else - who has let go
// of the lease since - fences rather than uploading its queue: the released
// lease still names the other machine.
func TestDegradedServerFencesOnAReleasedForeignLease(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	connect(t, e, st, "", "a").Close(context.Background())

	m, err := Degraded(st, e.cfg(""), e.opts("a"), errors.New("down"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	other := connect(t, e, openStore(t, t.TempDir()), "", "b")
	if err := other.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("settings", `{"stale":true}`); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(context.Background()); !errors.Is(err, ErrFenced) {
		t.Fatalf("sync: %v", err)
	}
	if v, _ := e.object("", store.KVObject("settings"), m.key); strings.Contains(string(v), "stale") {
		t.Fatal("the degraded server uploaded over the other machine's state")
	}
}

// A shutdown that cannot upload everything keeps the lease: the bucket must
// not look free while this machine holds changes it never uploaded.
func TestCloseKeepsTheLeaseWhenTheFinalUploadFails(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	m := connect(t, e, st, "", "a")
	if err := st.SetKV("settings", "unflushed"); err != nil {
		t.Fatal(err)
	}
	e.fake.FailOp("PUT", http.StatusInternalServerError)
	err := m.Close(context.Background())
	e.fake.FailOp("PUT", 0)
	if err == nil {
		t.Fatal("a close that could not upload reported success")
	}
	var lease Lease
	raw, _ := e.fake.Object(testBucket, leaseObject)
	if err := json.Unmarshal(raw, &lease); err != nil || lease.Instance != "a" || lease.HeartbeatAt == 0 {
		t.Fatalf("the lease was let go with changes still pending: %s", raw)
	}
	// The same data directory takes its own lease at once and uploads.
	m2 := connect(t, e, st, "", "a")
	if v, _ := e.object("", store.KVObject("settings"), m2.key); string(v) != "unflushed" {
		t.Fatalf("settings in the bucket = %q", v)
	}
}

// ------------------------------------------------------ a damaged bucket

// meta.json gone over sealed objects: no new installation (and salt) is made
// over them. A machine that has the place's meta.json cached puts it back;
// one that has not is refused, with nothing written and nothing local lost.
func TestAMissingMetaOverObjectsIsNotANewInstallation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := openStore(t, t.TempDir())
	seed(t, a)
	ma := connect(t, e, a, "", "a")
	key := ma.key
	ma.Close(ctx)
	orig, _ := e.fake.Object(testBucket, metaObject)
	e.fake.DeleteObject(testBucket, metaObject)

	fresh := openStore(t, t.TempDir())
	if err := fresh.CreateSession(&store.Session{ID: "mine", Harness: "shell", Workdir: "/", WorkdirMode: store.WorkdirCustom,
		State: store.StateExited}); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(ctx, fresh, e.cfg(""), e.opts("b")); !errors.Is(err, ErrMetaMissing) || Transient(err) {
		t.Fatalf("connect over objects without meta.json: %v", err)
	}
	if e.has("", metaObject) {
		t.Fatal("a new meta.json was created over existing objects")
	}
	if _, err := fresh.GetSession("mine"); err != nil {
		t.Fatal("the refused start removed a local row")
	}
	if _, err := Attach(ctx, fresh, e.cfg(""), e.opts("b"), AttachRequest{ConfirmNew: true}); !errors.Is(err, ErrMetaMissing) {
		t.Fatalf("attach over objects without meta.json: %v", err)
	}
	if e.has("", metaObject) {
		t.Fatal("the connect page created a meta.json over existing objects")
	}

	// The machine that knows the place puts its meta.json back, and every
	// object opens with it again.
	ma2 := connect(t, e, a, "", "a")
	now, ok := e.fake.Object(testBucket, metaObject)
	if !ok || string(now) == "" {
		t.Fatal("meta.json was not put back")
	}
	var was, is Meta
	_ = json.Unmarshal(orig, &was)
	_ = json.Unmarshal(now, &is)
	if is.BucketID != was.BucketID || is.Salt != was.Salt {
		t.Fatalf("meta.json came back as another installation: %+v, was %+v", is, was)
	}
	if _, ok := e.object("", store.SessionObject("run1"), key); !ok {
		t.Fatal("the objects do not open any more")
	}
	if _, err := a.GetSession("run1"); err != nil {
		t.Fatal(err)
	}
	_ = ma2
}

// A download fetches and opens everything before it touches anything: an
// object that cannot be opened leaves the local rows and their pending marks
// as they were.
func TestAnUnreadableObjectChangesNothingLocally(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	connectSeeded := openStore(t, t.TempDir())
	seed(t, connectSeeded)
	connect(t, e, connectSeeded, "", "a").Close(ctx)
	e.fake.PutObject(testBucket, store.KVObject("settings"), []byte("SOC1 not a sealed object at all"))

	local := openStore(t, t.TempDir())
	if err := local.CreateSession(&store.Session{ID: "mine", Harness: "shell", Workdir: "/", WorkdirMode: store.WorkdirCustom,
		State: store.StateExited}); err != nil {
		t.Fatal(err)
	}
	local.MarkPending(store.SessionObject("mine"))
	if _, err := Connect(ctx, local, e.cfg(""), e.opts("b")); err == nil {
		t.Fatal("a bucket with an object that does not open was restored")
	}
	if _, err := local.GetSession("mine"); err != nil {
		t.Fatal("a failed restore deleted a local row")
	}
	if !local.IsPending(store.SessionObject("mine")) {
		t.Fatal("a failed restore cleared a pending mark")
	}
}

// Deleting kv/password_hash from the bucket does not reset the password: the
// installation that has it keeps it and puts it back, and a new machine
// refuses to start from a bucket with sessions but no password (it would
// open the first-run setup page to anybody).
func TestADeletedPasswordInTheBucketIsNotAReset(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := openStore(t, t.TempDir())
	seed(t, a)
	connect(t, e, a, "", "a").Close(ctx)
	e.fake.DeleteObject(testBucket, store.KVObject(PasswordKey))

	if _, err := Connect(ctx, openStore(t, t.TempDir()), e.cfg(""), e.opts("b")); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("a new machine started from a bucket without a password: %v", err)
	}
	m := connect(t, e, a, "", "a")
	if v, _ := a.GetKV(PasswordKey); v == "" {
		t.Fatal("the local password was deleted")
	}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.has("", store.KVObject(PasswordKey)) {
		t.Fatal("the password was not put back into the bucket")
	}
}

// A holder whose heartbeats have not reached the bucket for a whole TTL
// stops writing: its lease is one anybody may take by now, and a write that
// lands after they did would undo their work.
func TestAnExpiredOwnLeaseStopsUploads(t *testing.T) {
	e := newEnv(t)
	st := openStore(t, t.TempDir())
	seed(t, st)
	clk := &clock{t: time.Now()}
	opts := e.opts("a")
	opts.Now = clk.now
	m, err := Connect(context.Background(), st, e.cfg(""), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	if err := st.SetKV("settings", "late"); err != nil {
		t.Fatal(err)
	}
	clk.add(2 * time.Minute)
	if err := m.Sync(context.Background()); !errors.Is(err, errLeaseExpired) {
		t.Fatalf("sync on an expired own lease: %v", err)
	}
	if v, _ := e.object("", store.KVObject("settings"), m.key); string(v) == "late" {
		t.Fatal("an expired holder uploaded")
	}
	if !st.IsPending(store.KVObject("settings")) {
		t.Fatal("the write is not pending any more")
	}
	if m.AutoStartAllowed() == nil {
		t.Fatal("a holder that has not confirmed its lease for a TTL starts scheduled sessions")
	}
}

// A start that takes over a lease another instance held and never let go of
// - it expired - waits TakeoverSettle before reading, for the old holder's
// last writes; a released lease is taken without waiting.
func TestTakingAnExpiredForeignLeaseSettles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := openStore(t, t.TempDir())
	seed(t, a)
	if _, err := Connect(ctx, a, e.cfg(""), e.opts("a")); err != nil { // never closed: a crash
		t.Fatal(err)
	}
	opts := e.opts("b")
	opts.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	opts.TakeoverSettle = 300 * time.Millisecond
	start := time.Now()
	mb, err := Connect(ctx, openStore(t, t.TempDir()), e.cfg(""), opts)
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < opts.TakeoverSettle {
		t.Fatalf("took an expired foreign lease after %s, without settling", waited)
	}
	if err := mb.Close(ctx); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	c := openStore(t, t.TempDir())
	mc, err := Connect(ctx, c, e.cfg(""), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer mc.Close(ctx)
	if waited := time.Since(start); waited >= opts.TakeoverSettle {
		t.Fatalf("a released lease was waited out (%s)", waited)
	}
}
