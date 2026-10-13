// Package s3 is the small S3 client Socrates keeps its state of record with:
// get, put (optionally conditional), delete, head and ListObjectsV2 against
// any S3 compatible store - AWS, Hetzner Object Storage (Ceph RGW),
// Cloudflare R2, MinIO.
//
// It is hand written on the standard library, like internal/webpush. The
// AWS SDK is a large dependency tree for six requests, and the only part of
// the protocol that is subtle - Signature Version 4 - lives in the sigv4
// subpackage, pinned by the AWS test vectors.
//
// Every object Socrates keeps is small (a session row, a settings value),
// so bodies are plain byte slices: no streaming, no multipart.
//
// Retries are the caller's business. The mirror that writes through to the
// bucket already keeps a queue of pending objects and backs off on its own;
// a client that also retried underneath would only make its timing harder
// to reason about. Timeouts come from the context.
package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3/sigv4"
)

// maxBody bounds what a single response may make us read. Socrates' own
// objects are kilobytes; a bucket answering with gigabytes is wrong, not a
// reason to run out of memory.
const maxBody = 256 << 20

// Config says where the bucket is and how to sign for it.
type Config struct {
	// Endpoint is the service URL, e.g. "https://hel1.your-objectstorage.com".
	// A bare host name means https. Empty means AWS: s3.<region>.amazonaws.com.
	Endpoint string
	// Region is the SigV4 signing region ("hel1", "eu-central-1", "auto" for
	// R2). Empty means us-east-1.
	Region string
	Bucket string
	// PathStyle addresses the bucket as endpoint/bucket/key rather than
	// bucket.endpoint/key. Ceph, MinIO and most self hosted stores want it;
	// AWS prefers virtual hosted names.
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	// HTTPClient is used for every request; nil means a client with sane
	// connection timeouts. The overall deadline comes from the context.
	HTTPClient *http.Client
	// Now is the clock the signature is dated with; nil means time.Now.
	Now func() time.Time
}

// Client talks to one bucket. It is safe for concurrent use.
type Client struct {
	cfg    Config
	base   *url.URL // scheme and host, with the bucket in it when virtual hosted
	prefix string   // "/bucket" when path style, "" otherwise
	hc     *http.Client
	now    func() time.Time
}

// New checks cfg and returns a client. It does not touch the network; Ping
// does.
func New(cfg Config) (*Client, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3: no bucket")
	}
	if strings.ContainsAny(cfg.Bucket, "/ ") {
		return nil, fmt.Errorf("s3: invalid bucket name %q", cfg.Bucket)
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("s3: access key id and secret are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = "https://s3." + cfg.Region + ".amazonaws.com"
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3: endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("s3: endpoint scheme %q is neither http nor https", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("s3: endpoint %q has no host", cfg.Endpoint)
	}
	if u.Path != "" || u.RawQuery != "" {
		// A path on the endpoint would have to be part of every canonical
		// URI; no S3 service needs one and it is almost always a pasted
		// bucket URL, which is better refused than half understood.
		return nil, fmt.Errorf("s3: endpoint %q must not have a path; give the bucket separately", cfg.Endpoint)
	}
	c := &Client{cfg: cfg, hc: cfg.HTTPClient, now: cfg.Now}
	c.base = &url.URL{Scheme: u.Scheme, Host: u.Host}
	if cfg.PathStyle {
		c.prefix = "/" + cfg.Bucket
	} else {
		c.base.Host = cfg.Bucket + "." + u.Host
	}
	if c.hc == nil {
		c.hc = defaultHTTPClient
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

var defaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   8,
	},
}

// Bucket is the bucket the client talks to.
func (c *Client) Bucket() string { return c.cfg.Bucket }

// Endpoint is the URL requests go to (with the bucket in the host name when
// virtual hosted), for status pages and error messages.
func (c *Client) Endpoint() string { return c.base.String() + c.prefix }

// Object is a stored object as Get returns it.
type Object struct {
	Body []byte
	// ETag is the entity tag exactly as the server sent it, quotes included,
	// which is the form If-Match wants back.
	ETag         string
	LastModified time.Time
}

// ObjectInfo describes an object without its body (Head, List).
type ObjectInfo struct {
	Key          string
	ETag         string
	Size         int64
	LastModified time.Time
}

// PutOptions are the conditions a Put can carry.
//
// Conditional writes are how a single writer is enforced cheaply: create the
// lease only if absent (IfNoneMatch "*"), replace it only if it is still the
// version we read (IfMatch). Not every S3 compatible store honours them, and
// one that does not simply ignores the header and overwrites. Use
// ProbeConditional to find out which kind of store you are talking to.
type PutOptions struct {
	// IfMatch, when set, makes the write succeed only if the current ETag
	// equals it. Pass the ETag as Get/Head/Put returned it; it is sent
	// without its quotes (see bareETag).
	//
	// A missing object fails the condition too, but how depends on the
	// store: Ceph (and RFC 9110) answer 412, AWS answers 404 NoSuchKey. A
	// caller must treat ErrNotFound like ErrPreconditionFailed here.
	IfMatch string
	// IfNoneMatch, when "*", makes the write succeed only if no object
	// exists under the key.
	IfNoneMatch string
	// ContentType defaults to application/octet-stream.
	ContentType string
}

// Get fetches an object. A missing key is an error matching ErrNotFound.
func (c *Client) Get(ctx context.Context, key string) (Object, error) {
	resp, body, err := c.do(ctx, "GET", key, nil, nil, nil)
	if err != nil {
		return Object{}, err
	}
	return Object{Body: body, ETag: resp.Header.Get("ETag"), LastModified: lastModified(resp.Header)}, nil
}

// Head fetches an object's metadata. A missing key matches ErrNotFound.
func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	resp, _, err := c.do(ctx, "HEAD", key, nil, nil, nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return ObjectInfo{Key: key, ETag: resp.Header.Get("ETag"), Size: size, LastModified: lastModified(resp.Header)}, nil
}

// Put stores body under key and returns the new ETag. opts may be nil. A
// failed condition is an error matching ErrPreconditionFailed.
func (c *Client) Put(ctx context.Context, key string, body []byte, opts *PutOptions) (string, error) {
	if key == "" {
		return "", errors.New("s3: empty key")
	}
	h := http.Header{}
	ct := "application/octet-stream"
	if opts != nil {
		if opts.ContentType != "" {
			ct = opts.ContentType
		}
		if opts.IfMatch != "" {
			h.Set("If-Match", bareETag(opts.IfMatch))
		}
		if opts.IfNoneMatch != "" {
			h.Set("If-None-Match", opts.IfNoneMatch)
		}
	}
	h.Set("Content-Type", ct)
	if body == nil {
		body = []byte{}
	}
	resp, _, err := c.do(ctx, "PUT", key, nil, h, body)
	if err != nil {
		return "", err
	}
	return resp.Header.Get("ETag"), nil
}

// Delete removes an object. Deleting a key that does not exist is not an
// error (S3 answers 204 either way, and so does this).
func (c *Client) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("s3: empty key")
	}
	_, _, err := c.do(ctx, "DELETE", key, nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		// Some stores answer 404 for a missing key; the outcome the caller
		// wanted - no object - holds either way.
		return nil
	}
	return err
}

// ListPage is one page of ListObjectsV2.
type ListPage struct {
	Objects []ObjectInfo
	// NextToken continues the listing; empty when this was the last page.
	NextToken string
}

// ListPage lists up to maxKeys objects whose key starts with prefix,
// continuing after token (empty for the first page). maxKeys <= 0 means the
// server's default (1000).
func (c *Client) ListPage(ctx context.Context, prefix, token string, maxKeys int) (ListPage, error) {
	q := url.Values{}
	q.Set("list-type", "2")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	if maxKeys > 0 {
		q.Set("max-keys", strconv.Itoa(maxKeys))
	}
	_, body, err := c.do(ctx, "GET", "", q, nil, nil)
	if err != nil {
		return ListPage{}, err
	}
	var res struct {
		IsTruncated           bool
		NextContinuationToken string
		Contents              []struct {
			Key          string
			LastModified string
			ETag         string
			Size         int64
		}
	}
	if err := xml.Unmarshal(body, &res); err != nil {
		return ListPage{}, fmt.Errorf("s3: list %q: decoding response: %w", prefix, err)
	}
	page := ListPage{Objects: make([]ObjectInfo, 0, len(res.Contents))}
	for _, o := range res.Contents {
		t, _ := time.Parse(time.RFC3339Nano, o.LastModified)
		page.Objects = append(page.Objects, ObjectInfo{Key: o.Key, ETag: o.ETag, Size: o.Size, LastModified: t})
	}
	if res.IsTruncated {
		if res.NextContinuationToken == "" {
			// A truncated page without a token would make List loop on the
			// first page forever.
			return ListPage{}, fmt.Errorf("s3: list %q: truncated listing without a continuation token", prefix)
		}
		page.NextToken = res.NextContinuationToken
	}
	return page, nil
}

// List returns every object whose key starts with prefix, following the
// continuation tokens.
func (c *Client) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var all []ObjectInfo
	token := ""
	for {
		page, err := c.ListPage(ctx, prefix, token, 0)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Objects...)
		if page.NextToken == "" {
			return all, nil
		}
		token = page.NextToken
	}
}

// Ping proves the bucket is reachable with these credentials: a listing of
// at most one key under prefix. It is the "test connection" button - it
// needs only list permission and reads nothing it does not have to.
func (c *Client) Ping(ctx context.Context, prefix string) error {
	_, err := c.ListPage(ctx, prefix, "", 1)
	return err
}

// do sends one signed request. key "" addresses the bucket itself. It
// returns the response (body already read and closed) and the body, or an
// error carrying the S3 error code when the status is not 2xx.
func (c *Client) do(ctx context.Context, method, key string, query url.Values, h http.Header, body []byte) (*http.Response, []byte, error) {
	path := c.prefix + "/" + key
	if key == "" && c.prefix != "" {
		path = c.prefix
	}
	u := *c.base
	u.Path = path
	u.RawPath = sigv4.EscapePath(path)
	if query != nil {
		// url.Values.Encode uses '+' for spaces; SigV4 canonicalisation
		// decodes and re-encodes, so either form is signed correctly, but
		// %20 is what S3 itself documents.
		u.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, nil, fmt.Errorf("s3: %s %s: %w", method, key, err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	sigv4.Sign(req, sigv4.Credentials{AccessKeyID: c.cfg.AccessKeyID, SecretAccessKey: c.cfg.SecretAccessKey},
		c.cfg.Region, "s3", sigv4.PayloadHash(body), c.now())

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("s3: %s %s: %w", method, opName(key), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, nil, fmt.Errorf("s3: %s %s: reading response: %w", method, opName(key), err)
	}
	if len(data) > maxBody {
		return nil, nil, fmt.Errorf("s3: %s %s: response larger than %d bytes", method, opName(key), maxBody)
	}
	if resp.StatusCode/100 != 2 {
		return nil, nil, newError(method, key, resp, data)
	}
	return resp, data, nil
}

// bareETag strips the quotes (and a weak W/ marker) off an entity tag.
//
// RFC 9110 says If-Match carries the quoted form, and AWS and MinIO accept
// either. But Ceph RGW (Hetzner Object Storage, verified 2026-10) compares
// the header against the unquoted hex digest on PUT: the quoted current
// ETag fails with 412 and the bare one succeeds, while a stale bare one
// still fails. The bare form is the one every store understands.
func bareETag(etag string) string {
	return strings.Trim(strings.TrimPrefix(strings.TrimSpace(etag), "W/"), `"`)
}

func opName(key string) string {
	if key == "" {
		return "(bucket)"
	}
	return key
}

func lastModified(h http.Header) time.Time {
	t, _ := http.ParseTime(h.Get("Last-Modified"))
	return t
}
