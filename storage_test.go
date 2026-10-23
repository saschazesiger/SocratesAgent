package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mirror"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func init() { newBucketIterations = 1000 }

// startup runs openStorage the way main does, with serve standing up a test
// listener over the front handler.
type startup struct {
	front  *frontHandler
	server *httptest.Server
	served chan struct{}
	done   chan *mirror.Mirror
	stop   context.CancelFunc
}

func runStartup(t *testing.T, st *store.Store, dataDir string) *startup {
	t.Helper()
	s := &startup{front: &frontHandler{}, served: make(chan struct{}), done: make(chan *mirror.Mirror, 1)}
	life, stop := context.WithCancel(context.Background())
	s.stop = stop
	t.Cleanup(stop)
	go func() {
		s.done <- openStorage(life, st, dataDir, s.front, func() {
			s.server = httptest.NewServer(s.front)
			t.Cleanup(s.server.Close)
			close(s.served)
		})
	}()
	return s
}

func (s *startup) get(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	res, err := http.Get(s.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (s *startup) post(t *testing.T, path string) int {
	t.Helper()
	res, err := http.Post(s.server.URL+path, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func (s *startup) mirror(t *testing.T) *mirror.Mirror {
	t.Helper()
	select {
	case m := <-s.done:
		if m != nil {
			t.Cleanup(func() { m.Close(context.Background()) })
		}
		return m
	case <-time.After(20 * time.Second):
		t.Fatal("the startup did not finish")
		return nil
	}
}

func (s *startup) waitServed(t *testing.T) {
	t.Helper()
	select {
	case <-s.served:
	case m := <-s.done:
		t.Fatalf("the startup did not wait in connect mode (mirror %v)", m != nil)
	case <-time.After(20 * time.Second):
		t.Fatal("connect mode was never served")
	}
}

func openMainStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func writeBucketConfig(t *testing.T, fake *s3fake.Server, dir string) mirror.Config {
	t.Helper()
	sc := fake.Config("main")
	cfg := mirror.Config{
		Endpoint: sc.Endpoint, Region: sc.Region, Bucket: "main", AccessKeyID: sc.AccessKeyID,
		SecretAccessKey: sc.SecretAccessKey, PathStyle: true, Passphrase: "main passphrase",
	}
	if err := mirror.SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A fresh data directory with no bucket serves the connect page, and answers
// the health check that scripts/update.sh asks of it.
func TestStartupWithoutStorageServesTheConnectPage(t *testing.T) {
	t.Setenv(mirror.EnvBucket, "")
	dir := t.TempDir()
	s := runStartup(t, openMainStore(t, dir), dir)
	s.waitServed(t)
	if code, out := s.get(t, "/api/health"); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("health = %d %v", code, out)
	}
	if _, out := s.get(t, "/api/state"); out["mode"] != "connect" {
		t.Fatalf("state = %v", out)
	}
	s.stop()
	if m := s.mirror(t); m != nil {
		t.Fatal("a stopped connect mode returned a mirror")
	}
}

// A configured, reachable bucket: no connect page at all, and the listener is
// left for main to open after the sessions are reconciled.
func TestStartupWithABucketConnectsWithoutServing(t *testing.T) {
	t.Setenv(mirror.EnvBucket, "")
	fake := s3fake.Start(s3fake.Options{})
	defer fake.Close()
	dir := t.TempDir()
	writeBucketConfig(t, fake, dir)
	s := runStartup(t, openMainStore(t, dir), dir)
	m := s.mirror(t)
	if m == nil || m.Status().State != "ok" {
		t.Fatalf("mirror = %v", m)
	}
	select {
	case <-s.served:
		t.Fatal("the connect page was served for a bucket that works")
	default:
	}
}

// An unreachable bucket with no local copy of it waits in connect mode and
// starts once the bucket answers; with a local copy, it starts degraded.
func TestStartupWithAnUnreachableBucket(t *testing.T) {
	t.Setenv(mirror.EnvBucket, "")
	fake := s3fake.Start(s3fake.Options{})
	defer fake.Close()
	dir := t.TempDir()
	writeBucketConfig(t, fake, dir)
	st := openMainStore(t, dir)

	fake.SetDown(http.StatusServiceUnavailable)
	s := runStartup(t, st, dir)
	s.waitServed(t)
	if code, _ := s.get(t, "/api/health"); code != http.StatusOK {
		t.Fatalf("health = %d", code)
	}
	_, out := s.get(t, "/api/state")
	connect, _ := out["connect"].(map[string]any)
	if connect["reason"] != "unreachable" || connect["configured"] != true {
		t.Fatalf("state = %v", out)
	}
	fake.SetDown(0)
	if code := s.post(t, "/api/storage/retry"); code != http.StatusOK {
		t.Fatalf("retry = %d", code)
	}
	m := s.mirror(t)
	if m == nil || m.Status().State != "ok" {
		t.Fatalf("after the bucket came back: %v", m)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Now this data directory has a copy of the bucket: the next start with
	// the bucket down runs from it.
	fake.SetDown(http.StatusServiceUnavailable)
	s = runStartup(t, st, dir)
	m = s.mirror(t)
	if m == nil || m.Status().State != "offline" {
		t.Fatalf("degraded start = %v", m)
	}
	select {
	case <-s.served:
		t.Fatal("a degraded start served the connect page")
	default:
	}
}
