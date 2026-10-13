package s3_test

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

// TestIntegration runs against a real bucket. It is opt in: set
// SOCRATES_S3_IT=1 plus the same SOCRATES_S3_* variables the server reads
// (ENDPOINT, REGION, BUCKET, ACCESS_KEY_ID, SECRET_ACCESS_KEY, PATH_STYLE).
// Everything happens under devtest/<random>/ whatever SOCRATES_S3_PREFIX
// says, and is deleted at the end, so it can be pointed at the production
// bucket without touching production data.
//
// Besides checking the basic operations it reports, with -v, how the store
// treats conditional writes and what its ETags look like - the facts the
// lease code depends on.
func TestIntegration(t *testing.T) {
	if os.Getenv("SOCRATES_S3_IT") != "1" {
		t.Skip("set SOCRATES_S3_IT=1 and SOCRATES_S3_* to run against a real bucket")
	}
	c, err := s3.New(s3.Config{
		Endpoint:        os.Getenv("SOCRATES_S3_ENDPOINT"),
		Region:          os.Getenv("SOCRATES_S3_REGION"),
		Bucket:          os.Getenv("SOCRATES_S3_BUCKET"),
		AccessKeyID:     os.Getenv("SOCRATES_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("SOCRATES_S3_SECRET_ACCESS_KEY"),
		PathStyle:       os.Getenv("SOCRATES_S3_PATH_STYLE") != "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rnd := make([]byte, 6)
	rand.Read(rnd)
	prefix := "devtest/" + hex.EncodeToString(rnd) + "/"
	t.Logf("endpoint %s, prefix %s", c.Endpoint(), prefix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		objs, err := c.List(ctx, prefix)
		if err != nil {
			t.Errorf("cleanup list: %v", err)
			return
		}
		for _, o := range objs {
			if err := c.Delete(ctx, o.Key); err != nil {
				t.Errorf("cleanup delete %s: %v", o.Key, err)
			}
		}
		if left, _ := c.List(ctx, prefix); len(left) != 0 {
			t.Errorf("cleanup left %d objects under %s", len(left), prefix)
		}
	})

	if err := c.Ping(ctx, prefix); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Round trip, including keys that need escaping.
	keys := []string{"plain.json", "kv/" + "a%20b", "sp ace/ü+=&$!'()*,;:@.json", "sessions/x~y_z-1.json"}
	for i, k := range keys {
		body := []byte(fmt.Sprintf("body %d %s", i, k))
		etag, err := c.Put(ctx, prefix+k, body, nil)
		if err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
		sum := md5.Sum(body)
		if i == 0 {
			t.Logf("ETag format: %s (quoted md5 hex = %v)", etag, etag == `"`+hex.EncodeToString(sum[:])+`"`)
		}
		got, err := c.Get(ctx, prefix+k)
		if err != nil || string(got.Body) != string(body) || got.ETag != etag {
			t.Fatalf("Get %q: %v %q etag %s/%s", k, err, got.Body, got.ETag, etag)
		}
		h, err := c.Head(ctx, prefix+k)
		if err != nil || h.Size != int64(len(body)) || h.ETag != etag {
			t.Fatalf("Head %q: %v %+v", k, err, h)
		}
	}

	// Pagination with a small page size.
	var listed []string
	token := ""
	pages := 0
	for {
		page, err := c.ListPage(ctx, prefix, token, 2)
		if err != nil {
			t.Fatalf("ListPage: %v", err)
		}
		pages++
		for _, o := range page.Objects {
			listed = append(listed, strings.TrimPrefix(o.Key, prefix))
		}
		if token = page.NextToken; token == "" {
			break
		}
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if strings.Join(listed, "|") != strings.Join(want, "|") || pages != 2 {
		t.Fatalf("listed %q in %d pages, want %q in 2", listed, pages, want)
	}
	all, err := c.List(ctx, prefix)
	if err != nil || len(all) != len(keys) {
		t.Fatalf("List: %v, %d objects", err, len(all))
	}

	// Missing objects.
	if _, err := c.Get(ctx, prefix+"missing"); !errors.Is(err, s3.ErrNotFound) {
		t.Errorf("Get missing: %v", err)
	}
	if _, err := c.Head(ctx, prefix+"missing"); !errors.Is(err, s3.ErrNotFound) {
		t.Errorf("Head missing: %v", err)
	}
	if err := c.Delete(ctx, prefix+"missing"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}

	// Conditional writes, observed raw.
	ck := prefix + "cond"
	report := func(what string, err error) {
		var e *s3.Error
		switch {
		case err == nil:
			t.Logf("%-46s -> success", what)
		case errors.As(err, &e):
			t.Logf("%-46s -> %d %s (precondition failed: %v)", what, e.StatusCode, e.Code, errors.Is(err, s3.ErrPreconditionFailed))
		default:
			t.Logf("%-46s -> %v", what, err)
		}
	}
	etag1, err := c.Put(ctx, ck, []byte("v1"), &s3.PutOptions{IfNoneMatch: "*"})
	report("If-None-Match:* on a missing key", err)
	_, err = c.Put(ctx, ck, []byte("v2"), &s3.PutOptions{IfNoneMatch: "*"})
	report("If-None-Match:* on an existing key", err)
	if err == nil {
		t.Error("If-None-Match:* overwrote an existing object")
	}
	// Put sends If-Match without its quotes (see bareETag in s3.go): this
	// Ceph refuses the quoted current ETag with 412.
	_, err = c.Put(ctx, ck, []byte("v3"), &s3.PutOptions{IfMatch: `"0123456789abcdef0123456789abcdef"`})
	report("If-Match stale ETag", err)
	if !errors.Is(err, s3.ErrPreconditionFailed) {
		t.Error("stale If-Match was not refused")
	}
	cur, _ := c.Head(ctx, ck)
	_, err = c.Put(ctx, ck, []byte("v4"), &s3.PutOptions{IfMatch: cur.ETag})
	report("If-Match current ETag", err)
	if err != nil {
		t.Error("current If-Match was refused")
	}
	_, err = c.Put(ctx, prefix+"cond-missing", []byte("x"), &s3.PutOptions{IfMatch: etag1})
	report("If-Match on a missing key", err)
	if err == nil {
		t.Error("If-Match on a missing key created it")
	}
	got, _ := c.Get(ctx, ck)
	t.Logf("final body of cond: %q", got.Body)
	if string(got.Body) != "v4" {
		t.Errorf("final body %q, want v4", got.Body)
	}

	cond, err := s3.ProbeConditional(ctx, c, prefix+"probe")
	if err != nil {
		t.Fatalf("ProbeConditional: %v", err)
	}
	t.Logf("ProbeConditional: IfNoneMatch=%v IfMatch=%v", cond.IfNoneMatch, cond.IfMatch)

	for _, k := range keys {
		if err := c.Delete(ctx, prefix+k); err != nil {
			t.Errorf("Delete %q: %v", k, err)
		}
		if _, err := c.Get(ctx, prefix+k); !errors.Is(err, s3.ErrNotFound) {
			t.Errorf("Get after delete %q: %v", k, err)
		}
	}
}
