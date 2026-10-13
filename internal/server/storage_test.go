package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mirror"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The mirror reads the password hash out of the bucket by this name to prove
// a takeover; the two spellings must never drift apart.
func TestMirrorKnowsThePasswordKey(t *testing.T) {
	if mirror.PasswordKey != PasswordKey {
		t.Fatalf("mirror.PasswordKey = %q, server.PasswordKey = %q", mirror.PasswordKey, PasswordKey)
	}
}

func fakeBucket(t *testing.T) *s3fake.Server {
	t.Helper()
	fake := s3fake.Start(s3fake.Options{})
	t.Cleanup(fake.Close)
	return fake
}

func bucketConfig(fake *s3fake.Server, prefix string) mirror.Config {
	sc := fake.Config("bucket")
	return mirror.Config{
		Endpoint: sc.Endpoint, Region: sc.Region, Bucket: "bucket", Prefix: prefix,
		AccessKeyID: sc.AccessKeyID, SecretAccessKey: sc.SecretAccessKey, PathStyle: true,
		Passphrase: "a long passphrase",
	}
}

func mirrorOptions(t *testing.T, instance, dataDir string, src mirror.Source) mirror.Options {
	return mirror.Options{
		Instance: instance, Host: "host-" + instance, Version: "test", DataDir: dataDir, Source: src,
		Iterations: 1000, Debounce: 10 * time.Millisecond, MaxLatency: 50 * time.Millisecond,
		Heartbeat: time.Hour, Settle: time.Millisecond, TakeoverSettle: time.Millisecond, VerifyEvery: 2 * time.Second,
		Logf: t.Logf,
	}
}

// connectEnv is a server in connect mode.
type connectEnv struct {
	server   *httptest.Server
	cs       *ConnectServer
	st       *store.Store
	dataDir  string
	restarts atomic.Int32
	retries  atomic.Int32
}

func newConnectEnv(t *testing.T, state ConnectState, cfg mirror.Config, src mirror.Source) *connectEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := &connectEnv{st: st, dataDir: dir}
	e.cs = NewConnect(ConnectOptions{
		Store: st, DataDir: dir, Mirror: mirrorOptions(t, "conn", dir, src), Config: cfg,
		Restart: func() { e.restarts.Add(1) },
		Retry:   func() { e.retries.Add(1) },
	}, state)
	e.server = httptest.NewServer(e.cs.Handler())
	t.Cleanup(func() {
		e.server.Close()
		st.Close()
	})
	return e
}

func (e *connectEnv) do(t *testing.T, method, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A fresh volume with no bucket: the health check answers, everything else
// points at the setup page, and the storage step connects a new bucket in
// two steps (the passphrase is confirmed) and restarts.
func TestConnectModeConnectsANewBucket(t *testing.T) {
	fake := fakeBucket(t)
	e := newConnectEnv(t, ConnectState{Reason: "unconfigured"}, mirror.Config{}, mirror.SourceNone)

	res, health := e.do(t, "GET", "/api/health", "")
	if res.StatusCode != http.StatusOK || health["ok"] != true {
		t.Fatalf("health in connect mode = %d %v", res.StatusCode, health)
	}
	res, _ = e.do(t, "GET", "/", "")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/setup" {
		t.Fatalf("/ = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, page := range []string{"/login", "/admin"} {
		if res, _ := e.do(t, "GET", page, ""); res.Header.Get("Location") != "/setup" {
			t.Errorf("%s does not lead to the setup page in connect mode", page)
		}
	}
	if res, _ := e.do(t, "GET", "/setup", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("/setup = %d", res.StatusCode)
	}
	if res, _ := e.do(t, "GET", "/api/sessions", ""); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("the API in connect mode = %d", res.StatusCode)
	}
	_, state := e.do(t, "GET", "/api/state", "")
	if state["mode"] != "connect" || state["connect"].(map[string]any)["reason"] != "unconfigured" {
		t.Fatalf("state = %v", state)
	}

	cfg := bucketConfig(fake, "fresh/")
	body := func(extra map[string]any) string {
		m := map[string]any{
			"endpoint": cfg.Endpoint, "region": cfg.Region, "bucket": cfg.Bucket, "prefix": "fresh",
			"access_key_id": cfg.AccessKeyID, "secret_access_key": cfg.SecretAccessKey, "passphrase": cfg.Passphrase,
		}
		for k, v := range extra {
			m[k] = v
		}
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	res, out := e.do(t, "POST", "/api/storage/connect", body(nil))
	if res.StatusCode != http.StatusOK || out["new_bucket"] != true {
		t.Fatalf("first connect = %d %v", res.StatusCode, out)
	}
	if _, ok := fake.Object("bucket", "fresh/meta.json"); ok {
		t.Fatal("meta.json was created before the passphrase was confirmed")
	}
	res, _ = e.do(t, "POST", "/api/storage/connect", body(map[string]any{"passphrase_confirm": "something else"}))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched confirmation = %d", res.StatusCode)
	}
	res, out = e.do(t, "POST", "/api/storage/connect", body(map[string]any{"passphrase_confirm": cfg.Passphrase}))
	if res.StatusCode != http.StatusOK || out["restarting"] != true || out["created"] != true {
		t.Fatalf("confirmed connect = %d %v", res.StatusCode, out)
	}
	if _, ok := fake.Object("bucket", "fresh/meta.json"); !ok {
		t.Fatal("no meta.json after connecting")
	}
	saved, src, err := mirror.LoadConfig(e.dataDir)
	if err != nil || src != mirror.SourceFile || saved.Bucket != "bucket" || saved.Prefix != "fresh/" || saved.Passphrase != cfg.Passphrase {
		t.Fatalf("s3.json = %+v %q %v", saved, src, err)
	}
	if info, _ := os.Stat(mirror.ConfigPath(e.dataDir)); info.Mode().Perm() != 0o600 {
		t.Fatalf("s3.json mode %v", info.Mode())
	}
	waitFor(t, "the restart", func() bool { return e.restarts.Load() == 1 })

	// The restart is an ordinary start from the bucket.
	m, err := mirror.Connect(context.Background(), e.st, saved, mirrorOptions(t, "conn", e.dataDir, mirror.SourceFile))
	if err != nil {
		t.Fatalf("the start after connecting: %v", err)
	}
	m.Close(context.Background())
}

// A configured server waiting on its bucket never offers another one, and a
// bucket another Socrates holds is taken over only with its password.
func TestConnectModeOfAConfiguredServer(t *testing.T) {
	fake := fakeBucket(t)
	cfg := bucketConfig(fake, "")

	// Another machine holds the bucket, with a password.
	holderDir := t.TempDir()
	holder, err := store.Open(filepath.Join(holderDir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	hash, _ := hashPassword("the-password")
	if err := holder.SetKV(PasswordKey, hash); err != nil {
		t.Fatal(err)
	}
	hm, err := mirror.Connect(context.Background(), holder, cfg, mirrorOptions(t, "holder", holderDir, mirror.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	defer hm.Close(context.Background())

	_, err = mirror.Connect(context.Background(), openTestStore(t), cfg, mirrorOptions(t, "conn", t.TempDir(), mirror.SourceFile))
	var held *mirror.LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected the lease to be held: %v", err)
	}
	state := StateFor(cfg, mirror.SourceFile, err)
	if state.Reason != "leased" || state.Lease == nil || state.Lease.Host != "host-holder" || !state.Configured {
		t.Fatalf("state = %+v", state)
	}
	e := newConnectEnv(t, state, cfg, mirror.SourceFile)
	_, out := e.do(t, "GET", "/api/state", "")
	connect := out["connect"].(map[string]any)
	if connect["reason"] != "leased" || connect["storage"].(map[string]any)["bucket"] != "bucket" {
		t.Fatalf("state = %v", out)
	}
	if strings.Contains(mustJSON(t, out), cfg.SecretAccessKey) || strings.Contains(mustJSON(t, out), cfg.Passphrase) {
		t.Fatal("the state leaks a secret")
	}

	other := bucketConfig(fake, "elsewhere/")
	raw, _ := json.Marshal(map[string]any{"bucket": other.Bucket, "prefix": other.Prefix, "endpoint": other.Endpoint,
		"access_key_id": other.AccessKeyID, "secret_access_key": other.SecretAccessKey, "passphrase": "x"})
	if res, _ := e.do(t, "POST", "/api/storage/connect", string(raw)); res.StatusCode != http.StatusConflict {
		t.Fatalf("a configured server accepted another bucket: %d", res.StatusCode)
	}
	if res, _ := e.do(t, "POST", "/api/storage/retry", "{}"); res.StatusCode != http.StatusOK || e.retries.Load() != 1 {
		t.Fatalf("retry = %d (%d)", res.StatusCode, e.retries.Load())
	}
	if res, _ := e.do(t, "POST", "/api/storage/takeover", `{"password":"wrong"}`); res.StatusCode != http.StatusForbidden {
		t.Fatalf("takeover with a wrong password = %d", res.StatusCode)
	}
	res, out := e.do(t, "POST", "/api/storage/takeover", `{"password":"the-password"}`)
	if res.StatusCode != http.StatusOK || out["took_over"] != true {
		t.Fatalf("takeover = %d %v", res.StatusCode, out)
	}
	waitFor(t, "the restart", func() bool { return e.restarts.Load() == 1 })
}

func mustJSON(t *testing.T, v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// The admin card's endpoints on a server running from a bucket, and the
// summary /api/state carries for the banner.
func TestStorageEndpoints(t *testing.T) {
	fake := fakeBucket(t)
	env := newEnv(t)
	dir := t.TempDir()
	cfg := bucketConfig(fake, "s/")
	m, err := mirror.Connect(context.Background(), env.store, cfg, mirrorOptions(t, "srv", dir, mirror.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	m.Start(context.Background())
	t.Cleanup(func() { m.Close(context.Background()) })
	env.srv.SetMirror(m)

	if res, _ := env.do(t, env.anon, "GET", "/api/storage", ""); res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous storage status = %d", res.StatusCode)
	}
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	waitFor(t, "the password to reach the bucket", func() bool {
		_, ok := fake.Object("bucket", "s/"+store.KVObject(PasswordKey))
		return ok
	})

	_, status := env.do(t, env.client, "GET", "/api/storage", "")
	if status["state"] != "ok" || status["configured"] != true || status["storage"].(map[string]any)["bucket"] != "bucket" {
		t.Fatalf("status = %v", status)
	}
	if strings.Contains(mustJSON(t, status), cfg.SecretAccessKey) || strings.Contains(mustJSON(t, status), cfg.Passphrase) {
		t.Fatal("the status leaks a secret")
	}
	_, state := env.do(t, env.client, "GET", "/api/state", "")
	if state["mode"] != "normal" || state["storage"].(map[string]any)["state"] != "ok" {
		t.Fatalf("state = %v", state)
	}
	if _, anonState := env.do(t, env.anon, "GET", "/api/state", ""); anonState["storage"] != nil {
		t.Fatal("the storage summary is shown to a page that is not signed in")
	}

	res, out := env.do(t, env.client, "POST", "/api/storage/test", "{}")
	if res.StatusCode != http.StatusOK || out["result"].(map[string]any)["ok"] != true {
		t.Fatalf("test = %d %v", res.StatusCode, out)
	}
	res, out = env.do(t, env.client, "POST", "/api/storage/sync", "{}")
	if res.StatusCode != http.StatusOK || out["ok"] != true {
		t.Fatalf("sync = %d %v", res.StatusCode, out)
	}

	// Credentials: refused ones are not saved; good ones are.
	res, _ = env.do(t, env.client, "PUT", "/api/storage/credentials",
		`{"endpoint":"`+cfg.Endpoint+`","region":"`+cfg.Region+`","access_key_id":"nobody","secret_access_key":"nothing","path_style":true}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("refused credentials = %d", res.StatusCode)
	}
	res, _ = env.do(t, env.client, "PUT", "/api/storage/credentials",
		`{"endpoint":"`+cfg.Endpoint+`","region":"`+cfg.Region+`","access_key_id":"`+cfg.AccessKeyID+`","path_style":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("good credentials = %d", res.StatusCode)
	}
	if saved, _, err := mirror.LoadConfig(dir); err != nil || saved.SecretAccessKey != cfg.SecretAccessKey {
		t.Fatalf("s3.json after a change = %+v %v", saved, err)
	}
}

// A fenced mirror shuts the scheduler's gate, and the storage endpoints say
// so.
func TestAFencedMirrorHoldsScheduledStartsBack(t *testing.T) {
	fake := fakeBucket(t)
	env := newEnv(t)
	cfg := bucketConfig(fake, "")
	m, err := mirror.Connect(context.Background(), env.store, cfg, mirrorOptions(t, "srv", t.TempDir(), mirror.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	env.srv.SetMirror(m)
	if env.srv.sched.blocked() != nil {
		t.Fatal("a mirror that holds its bucket blocks scheduled starts")
	}
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	if err := m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err = mirror.Attach(context.Background(), openTestStore(t), cfg, mirrorOptions(t, "thief", t.TempDir(), mirror.SourceFile),
		mirror.AttachRequest{Password: "a-good-password", VerifyPassword: verifyPassword, Resolve: mirror.ResolveBucket})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the mirror to notice the takeover", func() bool {
		_ = m.Sync(context.Background())
		return m.Fenced()
	})
	if env.srv.sched.blocked() == nil {
		t.Fatal("a fenced mirror lets the scheduler start sessions")
	}
	res, out := env.do(t, env.client, "POST", "/api/storage/sync", "{}")
	if res.StatusCode != http.StatusConflict || out["status"].(map[string]any)["state"] != "fenced" {
		t.Fatalf("sync while fenced = %d %v", res.StatusCode, out)
	}
}

// A data directory that already holds an installation with a password - an
// upgrade with no bucket configured yet - is not a fresh volume: the
// unauthenticated connect page must not upload it to, or restore it from,
// a bucket of anybody's choosing. It takes the password it already has.
func TestConnectingAnExistingInstallationTakesItsPassword(t *testing.T) {
	fake := fakeBucket(t)
	e := newConnectEnv(t, ConnectState{Reason: "unconfigured"}, mirror.Config{}, mirror.SourceNone)
	hash, _ := hashPassword("the-local-password")
	if err := e.st.SetKV(PasswordKey, hash); err != nil {
		t.Fatal(err)
	}
	_, state := e.do(t, "GET", "/api/state", "")
	if state["connect"].(map[string]any)["password_required"] != true {
		t.Fatalf("the page is not told a password is needed: %v", state)
	}

	cfg := bucketConfig(fake, "theirs/")
	body := func(extra map[string]any) string {
		m := map[string]any{
			"endpoint": cfg.Endpoint, "region": cfg.Region, "bucket": cfg.Bucket, "prefix": cfg.Prefix,
			"access_key_id": cfg.AccessKeyID, "secret_access_key": cfg.SecretAccessKey,
			"passphrase": cfg.Passphrase, "passphrase_confirm": cfg.Passphrase,
		}
		for k, v := range extra {
			m[k] = v
		}
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  int
	}{
		{"no password", nil, http.StatusUnauthorized},
		{"wrong password", map[string]any{"local_password": "guess"}, http.StatusForbidden},
		{"wrong password, bucket wins", map[string]any{"local_password": "guess", "resolve": "bucket"}, http.StatusForbidden},
		{"no password, overwrite", map[string]any{"resolve": "overwrite"}, http.StatusUnauthorized},
		// The takeover password is the bucket's, not this installation's.
		{"bucket password only", map[string]any{"password": "the-local-password"}, http.StatusUnauthorized},
	} {
		res, out := e.do(t, "POST", "/api/storage/connect", body(tc.extra))
		if res.StatusCode != tc.want {
			t.Fatalf("%s: %d %v", tc.name, res.StatusCode, out)
		}
		if keys := fake.Keys("bucket", "theirs/"); len(keys) != 0 {
			t.Fatalf("%s: the bucket was written: %v", tc.name, keys)
		}
		if _, src, _ := mirror.LoadConfig(e.dataDir); src != mirror.SourceNone {
			t.Fatalf("%s: s3.json was written", tc.name)
		}
	}
	res, out := e.do(t, "POST", "/api/storage/connect", body(map[string]any{"local_password": "the-local-password"}))
	if res.StatusCode != http.StatusOK || out["restarting"] != true {
		t.Fatalf("with the password: %d %v", res.StatusCode, out)
	}
	waitFor(t, "the restart", func() bool { return e.restarts.Load() == 1 })

	// Committed: nothing else is taken until the restart, and the waiting
	// loop may not start either.
	other := bucketConfig(fake, "other/")
	res, _ = e.do(t, "POST", "/api/storage/connect", strings.Replace(body(map[string]any{"local_password": "the-local-password"}),
		`"theirs/"`, `"`+other.Prefix+`"`, 1))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a second connect after a committed one = %d", res.StatusCode)
	}
	if e.cs.BeginAttempt() {
		t.Fatal("the waiting loop may start after a committed connect")
	}
	if saved, _, _ := mirror.LoadConfig(e.dataDir); saved.Prefix != "theirs/" {
		t.Fatalf("s3.json was rewritten to %q", saved.Prefix)
	}
	_, state = e.do(t, "GET", "/api/state", "")
	if c := state["connect"].(map[string]any); c["restarting"] != true || c["configured"] != true {
		t.Fatalf("state after the commit = %v", state)
	}
}

// The page's attempts and the waiting loop's are one at a time.
func TestTheWaitingLoopAndThePageShareOneAttempt(t *testing.T) {
	e := newConnectEnv(t, ConnectState{Reason: "leased", Configured: true}, mirror.Config{}, mirror.SourceFile)
	if !e.cs.BeginAttempt() {
		t.Fatal("the loop could not claim an attempt")
	}
	if res, _ := e.do(t, "POST", "/api/storage/takeover", `{"password":"x"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a takeover while the loop runs an attempt = %d", res.StatusCode)
	}
	if e.cs.BeginAttempt() {
		t.Fatal("two attempts at once")
	}
	e.cs.EndAttempt()
	if !e.cs.BeginAttempt() {
		t.Fatal("an attempt given back cannot be claimed again")
	}
}

// A server that started degraded - its bucket unreachable, so whether another
// machine holds it now is unknown - starts no scheduled session until it has
// taken the lease in this run.
func TestADegradedMirrorHoldsScheduledStartsBack(t *testing.T) {
	fake := fakeBucket(t)
	env := newEnv(t)
	cfg := bucketConfig(fake, "")
	opts := mirrorOptions(t, "srv", t.TempDir(), mirror.SourceFile)
	m, err := mirror.Connect(context.Background(), env.store, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.SetDown(http.StatusServiceUnavailable)
	dm, err := mirror.Degraded(env.store, cfg, opts, errors.New("down"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dm.Close(context.Background()) })
	env.srv.SetMirror(dm)
	if env.srv.sched.blocked() == nil {
		t.Fatal("a degraded mirror lets the scheduler start sessions")
	}
	// The loop is told so, and waits a whole tick instead of spinning on an
	// overdue schedule it may not start.
	if _, blocked := env.srv.sched.pass(context.Background(), time.Now()); !blocked {
		t.Fatal("a pass through a shut gate does not say it was blocked")
	}
	fake.SetDown(0)
	if err := dm.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.sched.blocked(); err != nil {
		t.Fatalf("still held back once the lease is taken: %v", err)
	}
}
