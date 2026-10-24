package s3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
)

func startFake(t *testing.T, opts s3fake.Options) (*s3fake.Server, *s3.Client) {
	t.Helper()
	srv := s3fake.Start(opts)
	t.Cleanup(srv.Close)
	return srv, srv.S3Client("bucket")
}

func TestRoundTrip(t *testing.T) {
	srv, c := startFake(t, s3fake.Options{})
	ctx := context.Background()

	// Keys that exercise the path escaping: what is signed must be what
	// is sent, and the fake verifies the signature over what it received.
	keys := []string{
		"meta.json",
		"kv/" + "push.vapid",
		"kv/a%2Fb",
		"sessions/s 1/ü+=&$!'()*,;:@~.json",
		"dir//double",
	}
	for i, k := range keys {
		body := []byte(fmt.Sprintf("value %d", i))
		etag, err := c.Put(ctx, k, body, nil)
		if err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
		if !strings.HasPrefix(etag, `"`) || len(etag) != 34 {
			t.Errorf("ETag %q is not a quoted md5", etag)
		}
		if got, ok := srv.Object("bucket", k); !ok || string(got) != string(body) {
			t.Fatalf("fake holds %q under %q (%v)", got, k, ok)
		}
		obj, err := c.Get(ctx, k)
		if err != nil || string(obj.Body) != string(body) || obj.ETag != etag || obj.LastModified.IsZero() {
			t.Fatalf("Get %q: %v %+v", k, err, obj)
		}
		info, err := c.Head(ctx, k)
		if err != nil || info.Size != int64(len(body)) || info.ETag != etag {
			t.Fatalf("Head %q: %v %+v", k, err, info)
		}
	}

	objs, err := c.List(ctx, "")
	if err != nil || len(objs) != len(keys) {
		t.Fatalf("List: %v %d", err, len(objs))
	}
	objs, err = c.List(ctx, "sessions/")
	if err != nil || len(objs) != 1 || objs[0].Key != keys[3] || objs[0].Size != 7 || objs[0].ETag == "" {
		t.Fatalf("List sessions/: %v %+v", err, objs)
	}

	// An empty body is a valid object.
	if _, err := c.Put(ctx, "empty", nil, nil); err != nil {
		t.Fatal(err)
	}
	if obj, err := c.Get(ctx, "empty"); err != nil || len(obj.Body) != 0 {
		t.Fatalf("empty: %v %q", err, obj.Body)
	}

	for _, k := range append(keys, "empty") {
		if err := c.Delete(ctx, k); err != nil {
			t.Fatalf("Delete %q: %v", k, err)
		}
	}
	if left := srv.Keys("bucket", ""); len(left) != 0 {
		t.Fatalf("left: %q", left)
	}
}

// GetStream hands the body over unread and reports what Head would, and a
// missing key is ErrNotFound with nothing left open.
func TestGetStream(t *testing.T) {
	_, c := startFake(t, s3fake.Options{})
	ctx := context.Background()
	body := []byte(strings.Repeat("chunk ", 100000))
	etag, err := c.Put(ctx, "chunks/a", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc, info, err := c.GetStream(ctx, "chunks/a")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}
	if info.Size != int64(len(body)) || info.ETag != etag || info.LastModified.IsZero() {
		t.Fatalf("info = %+v", info)
	}
	if rc, _, err := c.GetStream(ctx, "chunks/missing"); !errors.Is(err, s3.ErrNotFound) || rc != nil {
		t.Fatalf("missing: %v %v", rc, err)
	}
}

func TestNotFoundAndErrors(t *testing.T) {
	srv, c := startFake(t, s3fake.Options{Buckets: []string{"bucket"}})
	ctx := context.Background()

	_, err := c.Get(ctx, "nope")
	if !errors.Is(err, s3.ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	var e *s3.Error
	if !errors.As(err, &e) || e.Code != "NoSuchKey" || e.StatusCode != 404 || e.Message == "" {
		t.Fatalf("error details: %#v", err)
	}
	if !strings.Contains(err.Error(), "NoSuchKey") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("message: %v", err)
	}
	if _, err := c.Head(ctx, "nope"); !errors.Is(err, s3.ErrNotFound) {
		t.Fatalf("Head missing: %v", err)
	}
	if err := c.Delete(ctx, "nope"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}

	// A missing bucket is not a missing object.
	other := srv.S3Client("other")
	_, err = other.Get(ctx, "x")
	if !errors.Is(err, s3.ErrNoSuchBucket) || errors.Is(err, s3.ErrNotFound) {
		t.Fatalf("missing bucket: %v", err)
	}
	if err := other.Ping(ctx, ""); !errors.Is(err, s3.ErrNoSuchBucket) {
		t.Fatalf("Ping missing bucket: %v", err)
	}

	// Wrong secret.
	cfg := srv.Config("bucket")
	cfg.SecretAccessKey = "wrong"
	bad, _ := s3.New(cfg)
	if err := bad.Ping(ctx, ""); !errors.Is(err, s3.ErrAccessDenied) {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := bad.Head(ctx, "x"); !errors.Is(err, s3.ErrAccessDenied) {
		t.Fatalf("wrong secret on HEAD: %v", err)
	}

	// A store that is down.
	srv.SetDown(http.StatusServiceUnavailable)
	if err := c.Ping(ctx, ""); err == nil || !errors.As(err, &e) || e.StatusCode != 503 {
		t.Fatalf("down: %v", err)
	}
	srv.SetDown(0)
	srv.FailNext(500)
	if _, err := c.Put(ctx, "x", []byte("1"), nil); err == nil {
		t.Fatal("injected failure did not fail")
	}
	if _, err := c.Put(ctx, "x", []byte("1"), nil); err != nil {
		t.Fatalf("after injected failure: %v", err)
	}
	if err := c.Ping(ctx, ""); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// The context bounds the request.
	ctx2, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Get(ctx2, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestListPagination(t *testing.T) {
	srv, c := startFake(t, s3fake.Options{})
	ctx := context.Background()
	const n = 2345 // more than two default pages of 1000
	for i := 0; i < n; i++ {
		srv.PutObject("bucket", fmt.Sprintf("p/%05d", i), []byte("x"))
	}
	srv.PutObject("bucket", "q/other", []byte("x"))

	objs, err := c.List(ctx, "p/")
	if err != nil || len(objs) != n {
		t.Fatalf("List: %v, %d objects", err, len(objs))
	}
	for i, o := range objs {
		if o.Key != fmt.Sprintf("p/%05d", i) {
			t.Fatalf("object %d is %q", i, o.Key)
		}
	}
	if got := srv.Requests("LIST"); got != 3 {
		t.Errorf("%d list requests, want 3", got)
	}

	page, err := c.ListPage(ctx, "p/", "", 2)
	if err != nil || len(page.Objects) != 2 || page.NextToken == "" {
		t.Fatalf("first page: %v %+v", err, page)
	}
	page, err = c.ListPage(ctx, "p/", page.NextToken, 2)
	if err != nil || page.Objects[0].Key != "p/00002" {
		t.Fatalf("second page: %v %+v", err, page)
	}
	page, err = c.ListPage(ctx, "q/", "", 1)
	if err != nil || len(page.Objects) != 1 || page.NextToken != "" {
		t.Fatalf("exact page: %v %+v", err, page)
	}
}

func TestConditionalPut(t *testing.T) {
	srv, c := startFake(t, s3fake.Options{})
	ctx := context.Background()

	etag, err := c.Put(ctx, "meta.json", []byte("a"), &s3.PutOptions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Put(ctx, "meta.json", []byte("b"), &s3.PutOptions{IfNoneMatch: "*"}); !errors.Is(err, s3.ErrPreconditionFailed) {
		t.Fatalf("create again: %v", err)
	}
	_, err = c.Put(ctx, "meta.json", []byte("c"), &s3.PutOptions{IfMatch: etag})
	if err != nil {
		t.Fatalf("replace with current etag: %v", err)
	}
	if _, err := c.Put(ctx, "meta.json", []byte("d"), &s3.PutOptions{IfMatch: etag}); !errors.Is(err, s3.ErrPreconditionFailed) {
		t.Fatalf("replace with stale etag: %v", err)
	}
	if _, err := c.Put(ctx, "absent", []byte("d"), &s3.PutOptions{IfMatch: etag}); !errors.Is(err, s3.ErrPreconditionFailed) {
		t.Fatalf("If-Match on missing: %v", err)
	}
	if got, _ := srv.Object("bucket", "meta.json"); string(got) != "c" {
		t.Fatalf("body %q", got)
	}

	// Ignore: the headers are accepted and do nothing.
	srv.SetConditional(s3fake.Ignore)
	if _, err := c.Put(ctx, "meta.json", []byte("e"), &s3.PutOptions{IfMatch: `"stale"`}); err != nil {
		t.Fatalf("ignored If-Match: %v", err)
	}
	if got, _ := srv.Object("bucket", "meta.json"); string(got) != "e" {
		t.Fatalf("ignored condition did not overwrite: %q", got)
	}

	// Reject: the store refuses conditional writes outright.
	srv.SetConditional(s3fake.Reject)
	_, err = c.Put(ctx, "meta.json", []byte("f"), &s3.PutOptions{IfNoneMatch: "*"})
	var e *s3.Error
	if !errors.As(err, &e) || e.StatusCode != 501 || errors.Is(err, s3.ErrPreconditionFailed) {
		t.Fatalf("rejected: %v", err)
	}
}

func TestVirtualHosted(t *testing.T) {
	srv := s3fake.Start(s3fake.Options{VirtualHostSuffix: "s3.test"})
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	// Every host name resolves to the fake.
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	cfg := srv.Config("vbucket")
	cfg.Endpoint = "http://s3.test:" + port
	cfg.PathStyle = false
	cfg.HTTPClient = hc
	c, err := s3.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Endpoint(); got != "http://vbucket.s3.test:"+port {
		t.Errorf("Endpoint %s", got)
	}
	ctx := context.Background()
	if _, err := c.Put(ctx, "a/b c", []byte("v"), nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := srv.Object("vbucket", "a/b c"); !ok || string(got) != "v" {
		t.Fatalf("stored %q %v", got, ok)
	}
	if objs, err := c.List(ctx, "a/"); err != nil || len(objs) != 1 {
		t.Fatalf("List: %v %+v", err, objs)
	}
}

func TestNewValidates(t *testing.T) {
	base := s3.Config{Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}
	for _, tc := range []struct {
		name   string
		mutate func(*s3.Config)
		ok     bool
		want   string
	}{
		{"aws default", func(c *s3.Config) {}, true, "https://b.s3.us-east-1.amazonaws.com"},
		{"bare host", func(c *s3.Config) { c.Endpoint = "hel1.your-objectstorage.com"; c.PathStyle = true }, true, "https://hel1.your-objectstorage.com/b"},
		{"trailing slash", func(c *s3.Config) { c.Endpoint = "https://x.example/"; c.PathStyle = true }, true, "https://x.example/b"},
		{"no bucket", func(c *s3.Config) { c.Bucket = "" }, false, ""},
		{"slash in bucket", func(c *s3.Config) { c.Bucket = "a/b" }, false, ""},
		{"no secret", func(c *s3.Config) { c.SecretAccessKey = "" }, false, ""},
		{"endpoint with path", func(c *s3.Config) { c.Endpoint = "https://x.example/bucket" }, false, ""},
		{"ftp", func(c *s3.Config) { c.Endpoint = "ftp://x.example" }, false, ""},
	} {
		cfg := base
		tc.mutate(&cfg)
		c, err := s3.New(cfg)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v", tc.name, err)
			continue
		}
		if tc.ok && c.Endpoint() != tc.want {
			t.Errorf("%s: endpoint %s, want %s", tc.name, c.Endpoint(), tc.want)
		}
	}
}

func TestClockSkewIsAnError(t *testing.T) {
	srv, _ := startFake(t, s3fake.Options{})
	cfg := srv.Config("bucket")
	cfg.Now = func() time.Time { return time.Now().Add(-time.Hour) }
	c, _ := s3.New(cfg)
	if err := c.Ping(context.Background(), ""); !errors.Is(err, s3.ErrAccessDenied) {
		t.Fatalf("skewed clock: %v", err)
	}
}
