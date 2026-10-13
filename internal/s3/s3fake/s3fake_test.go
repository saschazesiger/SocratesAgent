package s3fake

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3/sigv4"
)

// The fake must refuse what Ceph refuses, or a client bug would pass here
// and fail in production. These requests are built by hand, below the
// client.
func TestFakeRefusesUnsigned(t *testing.T) {
	srv := Start(Options{})
	defer srv.Close()
	creds := sigv4.Credentials{AccessKeyID: DefaultAccessKeyID, SecretAccessKey: DefaultSecretAccessKey}

	send := func(method, path, body string, prep func(*http.Request)) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		prep(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// No Authorization at all.
	if code, _ := send("GET", "/b/k", "", func(*http.Request) {}); code != 403 {
		t.Errorf("unsigned: %d", code)
	}

	// Signed, but x-amz-content-sha256 removed afterwards: the Ceph quirk.
	code, body := send("PUT", "/b/k", "v", func(r *http.Request) {
		sigv4.Sign(r, creds, DefaultRegion, "s3", sigv4.PayloadHash([]byte("v")), time.Now())
		r.Header.Del(sigv4.HeaderContentSHA256)
	})
	if code != 403 || !strings.Contains(body, "SignatureDoesNotMatch") || !strings.Contains(body, "x-amz-content-sha256") {
		t.Errorf("missing content hash: %d %s", code, body)
	}

	// Body hash that does not match.
	code, _ = send("PUT", "/b/k", "v", func(r *http.Request) {
		sigv4.Sign(r, creds, DefaultRegion, "s3", sigv4.PayloadHash([]byte("other")), time.Now())
	})
	if code != 403 {
		t.Errorf("wrong content hash: %d", code)
	}

	// Properly signed works; an unsupported sub-resource is 501, not a
	// silent success.
	code, _ = send("PUT", "/b/k", "v", func(r *http.Request) {
		sigv4.Sign(r, creds, DefaultRegion, "s3", sigv4.PayloadHash([]byte("v")), time.Now())
	})
	if code != 200 {
		t.Errorf("signed put: %d", code)
	}
	code, _ = send("GET", "/b/k?acl", "", func(r *http.Request) {
		sigv4.Sign(r, creds, DefaultRegion, "s3", sigv4.EmptySHA256, time.Now())
	})
	if code != 501 {
		t.Errorf("sub-resource: %d", code)
	}
	code, _ = send("GET", "/b?list-type=2&delimiter=/", "", func(r *http.Request) {
		sigv4.Sign(r, creds, DefaultRegion, "s3", sigv4.EmptySHA256, time.Now())
	})
	if code != 501 {
		t.Errorf("delimiter: %d", code)
	}
	code, _ = send("GET", "/b", "", func(r *http.Request) {
		sigv4.Sign(r, creds, DefaultRegion, "s3", sigv4.EmptySHA256, time.Now())
	})
	if code != 501 {
		t.Errorf("ListObjects v1: %d", code)
	}
	if got, ok := srv.Object("b", "k"); !ok || string(got) != "v" {
		t.Errorf("object %q %v", got, ok)
	}
}

func TestFakeDirectAccess(t *testing.T) {
	f := New(Options{Buckets: []string{"b"}})
	f.PutObject("b", "x/1", []byte("one"))
	f.PutObject("b", "x/2", []byte("two"))
	f.PutObject("b", "y", []byte("y"))
	if got := strings.Join(f.Keys("b", "x/"), ","); got != "x/1,x/2" {
		t.Errorf("Keys %s", got)
	}
	f.DeleteObject("b", "x/1")
	if _, ok := f.Object("b", "x/1"); ok {
		t.Error("deleted object still there")
	}
	got, _ := f.Object("b", "x/2")
	got[0] = 'X' // a copy: the stored body must not change
	if again, _ := f.Object("b", "x/2"); string(again) != "two" {
		t.Errorf("Object returned the stored slice: %q", again)
	}
}
