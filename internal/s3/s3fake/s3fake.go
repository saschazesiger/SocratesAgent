// Package s3fake is an in-memory S3 server for tests: GET, PUT, DELETE and
// HEAD on objects, ListObjectsV2 with continuation tokens, and the
// conditional PUT headers, behind real SigV4 verification.
//
// It speaks the subset internal/s3 uses, and speaks it the way the store
// Socrates is deployed against (Ceph RGW at Hetzner) was observed to:
// ETags are the quoted hex MD5 of the body, If-Match is compared without
// quotes, an If-Match on a missing key fails with 412, and a request
// without x-amz-content-sha256 is refused. Anything outside that subset is
// answered 501 so that a test can never pass by accident against an
// operation the fake only pretends to support.
//
// Two knobs let tests reach the paths a healthy store never takes:
// SetConditional emulates a store that ignores (or refuses) conditional
// writes; SetDown, FailNext and FailOp make the store unreachable or flaky.
//
// It is an ordinary http.Handler, so the e2e fake binary can serve it on a
// real port, and Start wraps it in an httptest.Server for unit tests.
package s3fake

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/s3/sigv4"
)

// Default credentials, for tests that do not care.
const (
	DefaultAccessKeyID     = "FAKEACCESSKEY"
	DefaultSecretAccessKey = "fake-secret-key"
	DefaultRegion          = "fake-1"
)

// maxObject bounds a PUT body. Socrates' largest objects are backup chunks
// of a little over 8 MiB.
const maxObject = 64 << 20

// Conditional selects how the fake treats If-Match / If-None-Match on PUT.
type Conditional int

const (
	// Honour evaluates the conditions (Ceph RGW, current AWS).
	Honour Conditional = iota
	// Ignore accepts the headers and overwrites regardless - the dangerous
	// kind of store, and the reason a new backup reads its meta.json back.
	Ignore
	// Reject answers 501 NotImplemented to any conditional PUT.
	Reject
)

// Options configure a Fake. The zero value is usable.
type Options struct {
	// AccessKeyID, SecretAccessKey and Region are what requests must be
	// signed with; empty means the Default* constants.
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	// Buckets that exist. Nil means any bucket name exists (and is created
	// on first use), which is what most tests want; a non-nil list makes
	// every other name answer NoSuchBucket.
	Buckets []string
	// VirtualHostSuffix, when set, enables virtual hosted addressing: a
	// request whose Host is "<bucket>.<suffix>" (port ignored) addresses
	// <bucket>. Requests to any other host are path style.
	VirtualHostSuffix string
	// Now is the clock signatures are checked against; nil is time.Now.
	Now func() time.Time
	// Written is the clock an object's LastModified is read off; nil is
	// Now. A test of something that ages objects - the backup's garbage
	// collection - moves it on without making every signature too old.
	Written func() time.Time
}

type object struct {
	body     []byte
	etag     string // quoted, as sent
	modified time.Time
}

// Fake is the in-memory store. It is safe for concurrent use.
type Fake struct {
	opts  Options
	creds sigv4.Credentials

	mu          sync.Mutex
	buckets     map[string]map[string]*object
	conditional Conditional
	down        int // status answered to everything, 0 when up
	failNext    []int
	failOps     map[string]int
	requests    map[string]int
}

// New returns a fake with no objects.
func New(opts Options) *Fake {
	if opts.AccessKeyID == "" {
		opts.AccessKeyID = DefaultAccessKeyID
	}
	if opts.SecretAccessKey == "" {
		opts.SecretAccessKey = DefaultSecretAccessKey
	}
	if opts.Region == "" {
		opts.Region = DefaultRegion
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Written == nil {
		opts.Written = opts.Now
	}
	f := &Fake{
		opts:     opts,
		creds:    sigv4.Credentials{AccessKeyID: opts.AccessKeyID, SecretAccessKey: opts.SecretAccessKey},
		buckets:  map[string]map[string]*object{},
		requests: map[string]int{},
	}
	for _, b := range opts.Buckets {
		f.buckets[b] = map[string]*object{}
	}
	return f
}

// Server is a Fake served by an httptest.Server.
type Server struct {
	*Fake
	*httptest.Server
}

// Start serves a new Fake on a loopback port. Close it when done.
func Start(opts Options) *Server {
	f := New(opts)
	return &Server{Fake: f, Server: httptest.NewServer(f)}
}

// Config is an s3.Config that reaches bucket on this server, path style,
// with the fake's credentials.
func (s *Server) Config(bucket string) s3.Config {
	return s.Fake.Config(s.URL, bucket)
}

// S3Client is s3.New(s.Config(bucket)), for tests. (Client is taken: it is
// httptest.Server's plain HTTP client.)
func (s *Server) S3Client(bucket string) *s3.Client {
	c, err := s3.New(s.Config(bucket))
	if err != nil {
		panic(err) // the config is ours; failing here is a bug in the fake
	}
	return c
}

// Config is an s3.Config that reaches bucket on a fake served at endpoint.
func (f *Fake) Config(endpoint, bucket string) s3.Config {
	return s3.Config{
		Endpoint:        endpoint,
		Region:          f.opts.Region,
		Bucket:          bucket,
		PathStyle:       true,
		AccessKeyID:     f.opts.AccessKeyID,
		SecretAccessKey: f.opts.SecretAccessKey,
	}
}

// SetConditional switches how conditional PUTs are treated.
func (f *Fake) SetConditional(c Conditional) {
	f.mu.Lock()
	f.conditional = c
	f.mu.Unlock()
}

// SetDown makes every request fail with status (e.g. 503) until called
// with 0.
func (f *Fake) SetDown(status int) {
	f.mu.Lock()
	f.down = status
	f.mu.Unlock()
}

// FailNext makes the next len(statuses) requests fail with those statuses,
// in order.
func (f *Fake) FailNext(statuses ...int) {
	f.mu.Lock()
	f.failNext = append(f.failNext, statuses...)
	f.mu.Unlock()
}

// FailOp makes every request of one operation ("GET", "PUT", "DELETE", "HEAD"
// or "LIST") fail with status until called again with 0 - a store that
// reads but refuses writes, say.
func (f *Fake) FailOp(op string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOps == nil {
		f.failOps = map[string]int{}
	}
	if status == 0 {
		delete(f.failOps, op)
		return
	}
	f.failOps[op] = status
}

// Requests reports how many requests of an operation were served
// successfully or not; op is "GET", "PUT", "DELETE", "HEAD" or "LIST".
func (f *Fake) Requests(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[op]
}

// Object returns a copy of an object's body, for assertions.
func (f *Fake) Object(bucket, key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.buckets[bucket][key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), o.body...), true
}

// PutObject stores an object directly, bypassing HTTP - a stand-in for
// another writer.
func (f *Fake) PutObject(bucket, key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.buckets[bucket]
	if b == nil {
		b = map[string]*object{}
		f.buckets[bucket] = b
	}
	b[key] = newObject(body, f.opts.Written())
}

// DeleteObject removes an object directly.
func (f *Fake) DeleteObject(bucket, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.buckets[bucket], key)
}

// Keys lists a bucket's keys starting with prefix, sorted.
func (f *Fake) Keys(bucket, prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.buckets[bucket] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func newObject(body []byte, now time.Time) *object {
	sum := md5.Sum(body)
	return &object{
		body:     append([]byte(nil), body...),
		etag:     `"` + hex.EncodeToString(sum[:]) + `"`,
		modified: now.UTC().Truncate(time.Second),
	}
}

// ServeHTTP implements the S3 subset.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxObject+1))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	if len(body) > maxObject {
		writeError(w, r, http.StatusBadRequest, "EntityTooLarge", "object larger than the fake allows")
		return
	}

	bucket, key := f.route(r)
	op := r.Method
	if op == http.MethodGet && key == "" {
		op = "LIST"
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[op]++
	if f.down != 0 {
		writeError(w, r, f.down, "ServiceUnavailable", "the fake is down")
		return
	}
	if status := f.failOps[op]; status != 0 {
		writeError(w, r, status, "InternalError", "injected failure")
		return
	}
	if len(f.failNext) > 0 {
		status := f.failNext[0]
		f.failNext = f.failNext[1:]
		writeError(w, r, status, "InternalError", "injected failure")
		return
	}

	if err := sigv4.Verify(r, body, f.creds, f.opts.Region, "s3", f.opts.Now(), 15*time.Minute); err != nil {
		writeError(w, r, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	if bucket == "" {
		writeError(w, r, http.StatusNotImplemented, "NotImplemented", "service level requests are not supported")
		return
	}
	objects, ok := f.buckets[bucket]
	if !ok {
		if f.opts.Buckets != nil {
			writeError(w, r, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
			return
		}
		objects = map[string]*object{}
		f.buckets[bucket] = objects
	}

	switch op {
	case "LIST":
		f.list(w, r, objects)
	case http.MethodGet, http.MethodHead:
		if len(r.URL.RawQuery) > 0 {
			writeError(w, r, http.StatusNotImplemented, "NotImplemented", "sub-resources are not supported")
			return
		}
		o, ok := objects[key]
		if !ok {
			writeError(w, r, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		h := w.Header()
		h.Set("ETag", o.etag)
		h.Set("Last-Modified", o.modified.Format(http.TimeFormat))
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Length", strconv.Itoa(len(o.body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(o.body)
		}
	case http.MethodPut:
		if key == "" || len(r.URL.RawQuery) > 0 || r.Header.Get("X-Amz-Copy-Source") != "" {
			writeError(w, r, http.StatusNotImplemented, "NotImplemented", "only plain object PUTs are supported")
			return
		}
		if status, code := f.checkConditions(r, objects[key]); status != 0 {
			writeError(w, r, status, code, "At least one of the pre-conditions you specified did not hold")
			return
		}
		o := newObject(body, f.opts.Written())
		objects[key] = o
		w.Header().Set("ETag", o.etag)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if key == "" || len(r.URL.RawQuery) > 0 {
			writeError(w, r, http.StatusNotImplemented, "NotImplemented", "only plain object DELETEs are supported")
			return
		}
		delete(objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, r, http.StatusNotImplemented, "NotImplemented", r.Method+" is not supported")
	}
}

// checkConditions returns a non-zero status (and S3 code) when a PUT's
// conditions forbid it. Called with f.mu held.
func (f *Fake) checkConditions(r *http.Request, cur *object) (int, string) {
	ifMatch, ifNoneMatch := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if ifMatch == "" && ifNoneMatch == "" {
		return 0, ""
	}
	switch f.conditional {
	case Ignore:
		return 0, ""
	case Reject:
		return http.StatusNotImplemented, "NotImplemented"
	}
	if ifNoneMatch != "" {
		if ifNoneMatch != "*" {
			return http.StatusNotImplemented, "NotImplemented"
		}
		if cur != nil {
			return http.StatusPreconditionFailed, "PreconditionFailed"
		}
	}
	if ifMatch != "" {
		// Like Ceph: a missing object fails with 412, and the comparison
		// is against the bare digest. Unlike Ceph, a quoted value is also
		// accepted, as AWS does - the client sends the bare form either way.
		if cur == nil || strings.Trim(ifMatch, `"`) != strings.Trim(cur.etag, `"`) {
			return http.StatusPreconditionFailed, "PreconditionFailed"
		}
	}
	return 0, ""
}

// list implements ListObjectsV2. Called with f.mu held.
func (f *Fake) list(w http.ResponseWriter, r *http.Request, objects map[string]*object) {
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		writeError(w, r, http.StatusNotImplemented, "NotImplemented", "only ListObjectsV2 (list-type=2) is supported")
		return
	}
	if q.Has("delimiter") || q.Has("encoding-type") || q.Has("fetch-owner") {
		writeError(w, r, http.StatusNotImplemented, "NotImplemented", "delimiter, encoding-type and fetch-owner are not supported")
		return
	}
	prefix := q.Get("prefix")
	maxKeys := 1000
	if s := q.Get("max-keys"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "InvalidArgument", "bad max-keys")
			return
		}
		maxKeys = min(n, 1000)
	}
	after := q.Get("start-after")
	if tok := q.Get("continuation-token"); tok != "" {
		k, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "InvalidArgument", "The continuation token provided is incorrect")
			return
		}
		after = string(k)
	}
	var keys []string
	for k := range objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	type content struct {
		Key          string
		LastModified string
		ETag         string
		Size         int
		StorageClass string
	}
	res := struct {
		XMLName               xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
		Name                  string
		Prefix                string
		KeyCount              int
		MaxKeys               int
		IsTruncated           bool
		ContinuationToken     string `xml:",omitempty"`
		NextContinuationToken string `xml:",omitempty"`
		Contents              []content
	}{Name: r.URL.Path, Prefix: prefix, MaxKeys: maxKeys, ContinuationToken: q.Get("continuation-token")}
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
		res.IsTruncated = true
		res.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(keys[len(keys)-1]))
	}
	for _, k := range keys {
		o := objects[k]
		res.Contents = append(res.Contents, content{
			Key: k, LastModified: o.modified.Format("2006-01-02T15:04:05.000Z"),
			ETag: o.etag, Size: len(o.body), StorageClass: "STANDARD",
		})
	}
	res.KeyCount = len(res.Contents)
	out, err := xml.Marshal(res)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, xml.Header)
	w.Write(out)
}

// route splits a request into bucket and key, path style or virtual
// hosted.
func (f *Fake) route(r *http.Request) (bucket, key string) {
	path := r.URL.Path // decoded
	if f.opts.VirtualHostSuffix != "" {
		host := r.Host
		if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
			host = host[:i]
		}
		if b, ok := strings.CutSuffix(host, "."+f.opts.VirtualHostSuffix); ok {
			return b, strings.TrimPrefix(path, "/")
		}
	}
	path = strings.TrimPrefix(path, "/")
	bucket, key, _ = strings.Cut(path, "/")
	return bucket, key
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("X-Amz-Request-Id", fmt.Sprintf("fake-%d", time.Now().UnixNano()))
	if r.Method == http.MethodHead {
		// HEAD responses carry no body, so a client sees only the status.
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	type errorBody struct {
		XMLName xml.Name `xml:"Error"`
		Code    string
		Message string
	}
	out, _ := xml.Marshal(errorBody{Code: code, Message: msg})
	io.WriteString(w, xml.Header)
	w.Write(out)
}
