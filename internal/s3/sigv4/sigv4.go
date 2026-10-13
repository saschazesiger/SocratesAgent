// Package sigv4 is AWS Signature Version 4 as S3 uses it: the one piece of
// the S3 protocol that has to be byte exact, kept apart so that both ends of
// it can share the same reading of the spec - the client signs with it and
// the in-memory fake (internal/s3/s3fake) verifies with it.
//
// Sharing the code means the fake cannot catch a mistake the client and the
// fake would make together; the published AWS test vectors in sigv4_test.go
// are what pins the reading itself.
//
// Only what S3 needs is here: header based signing (no presigned URLs, no
// chunked streaming payloads) with the payload hash always sent in
// x-amz-content-sha256. S3 requires that header anyway, and Ceph RGW refuses
// a signature without it rather than defaulting, so it is never optional.
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// Algorithm is the only signing algorithm this package speaks.
	Algorithm = "AWS4-HMAC-SHA256"
	// TimeFormat is the x-amz-date format (ISO 8601 basic, UTC).
	TimeFormat = "20060102T150405Z"
	// HeaderContentSHA256 carries the hex SHA-256 of the body.
	HeaderContentSHA256 = "X-Amz-Content-Sha256"
	// HeaderDate carries the signing time.
	HeaderDate = "X-Amz-Date"
	// UnsignedPayload may stand in for the body hash. This client never sends
	// it, but a verifier has to accept it.
	UnsignedPayload = "UNSIGNED-PAYLOAD"
	// EmptySHA256 is the hash of a request without a body, which is what
	// every GET, HEAD and DELETE sends as its payload hash.
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	dateFormat  = "20060102"
	terminator  = "aws4_request"
)

// Credentials are a static access key pair. Session tokens (STS) are not
// supported: Socrates is pointed at a bucket with a long lived key.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// PayloadHash is the hex SHA-256 of a body, the value of
// x-amz-content-sha256.
func PayloadHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Sign adds X-Amz-Date, X-Amz-Content-Sha256 and Authorization to req.
//
// Every header already present in req.Header is signed, plus Host. That is
// deliberate: the caller sets only headers that matter (conditions, content
// type), and signing them means nobody between here and the bucket can
// strip an If-None-Match and turn a guarded write into a blind one. Headers
// the transport adds later (User-Agent, Accept-Encoding, Content-Length) are
// not in req.Header yet and so are not signed, which S3 allows.
//
// req.URL must already carry the exact escaped path that goes on the wire:
// set it with URL.RawPath (or build it with url.Parse from an escaped
// string) using EscapePath, so that what is signed is what is sent.
func Sign(req *http.Request, creds Credentials, region, service, payloadHash string, now time.Time) {
	now = now.UTC()
	req.Header.Set(HeaderDate, now.Format(TimeFormat))
	req.Header.Set(HeaderContentSHA256, payloadHash)
	req.Header.Del("Authorization")

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	signed, canonHeaders := canonicalHeaders(req.Header, host, nil)
	creq := canonicalRequest(req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canonHeaders, signed, payloadHash)
	scope := now.Format(dateFormat) + "/" + region + "/" + service + "/" + terminator
	sts := stringToSign(now, scope, creq)
	sig := hex.EncodeToString(hmacSHA256(signingKey(creds.SecretAccessKey, now, region, service), sts))
	req.Header.Set("Authorization", Algorithm+" Credential="+creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
}

// Verify checks a signed request the way S3 would: the Authorization header
// parses, names accessKeyID, the signature over the headers it lists is
// right for secret, the date is within maxSkew of now, and - unless the
// client declared an unsigned payload - x-amz-content-sha256 matches body.
//
// It is the server half used by the fake; the client never calls it.
func Verify(r *http.Request, body []byte, creds Credentials, region, service string, now time.Time, maxSkew time.Duration) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, Algorithm+" ") {
		return errors.New("missing or non-SigV4 Authorization header")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, Algorithm+" "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return fmt.Errorf("malformed Authorization component %q", part)
		}
		fields[k] = v
	}
	cred, signedList, sig := fields["Credential"], fields["SignedHeaders"], fields["Signature"]
	if cred == "" || signedList == "" || sig == "" {
		return errors.New("Authorization lacks Credential, SignedHeaders or Signature")
	}
	payloadHash := r.Header.Get(HeaderContentSHA256)
	if payloadHash == "" {
		// The one Ceph quirk worth reproducing: RGW answers a request
		// without this header with SignatureDoesNotMatch.
		return errors.New("missing x-amz-content-sha256")
	}
	if payloadHash != UnsignedPayload && payloadHash != PayloadHash(body) {
		return errors.New("x-amz-content-sha256 does not match the body")
	}
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[4] != terminator {
		return fmt.Errorf("malformed credential scope %q", cred)
	}
	if parts[0] != creds.AccessKeyID {
		return fmt.Errorf("unknown access key %q", parts[0])
	}
	if parts[2] != region || parts[3] != service {
		return fmt.Errorf("credential scope %s/%s, want %s/%s", parts[2], parts[3], region, service)
	}
	t, err := time.Parse(TimeFormat, r.Header.Get(HeaderDate))
	if err != nil {
		return errors.New("missing or malformed x-amz-date")
	}
	if t.Format(dateFormat) != parts[1] {
		return errors.New("x-amz-date does not match the credential scope date")
	}
	if d := now.Sub(t); d > maxSkew || d < -maxSkew {
		return fmt.Errorf("request time %s is too far from %s", t, now.UTC())
	}
	signed := strings.Split(signedList, ";")
	hasHost := false
	for _, h := range signed {
		if h == "host" {
			hasHost = true
		}
	}
	if !hasHost {
		return errors.New("host is not a signed header")
	}
	_, canonHeaders := canonicalHeaders(r.Header, r.Host, signed)
	creq := canonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, canonHeaders, signed, payloadHash)
	sts := stringToSign(t, strings.Join(parts[1:], "/"), creq)
	want := hex.EncodeToString(hmacSHA256(signingKey(creds.SecretAccessKey, t, region, service), sts))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("signature does not match")
	}
	return nil
}

// EscapePath percent-encodes an object path the way S3's canonical request
// wants it: every byte except the unreserved set A-Z a-z 0-9 - _ . ~ is
// encoded, '/' is kept as the separator, and nothing is encoded twice (S3,
// unlike every other AWS service, does not double-encode the path).
func EscapePath(p string) string {
	return escape(p, false)
}

// EscapeQuery encodes one query name or value for the canonical query
// string: like EscapePath, but '/' is encoded too.
func EscapeQuery(s string) string {
	return escape(s, true)
}

func escape(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// canonicalQuery re-encodes a raw query: names and values decoded, encoded
// the SigV4 way, sorted by name then value, a bare "lifecycle" becoming
// "lifecycle=".
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type pair struct{ k, v string }
	var pairs []pair
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		// A value that fails to decode is signed as sent; the server will
		// then disagree, which is the right outcome for a malformed query.
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		pairs = append(pairs, pair{EscapeQuery(k), EscapeQuery(v)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// canonicalHeaders lists the headers to sign and renders them. With only
// nil it signs host plus every header in h; otherwise exactly the names in
// only (which a verifier takes from the Authorization header).
func canonicalHeaders(h http.Header, host string, only []string) ([]string, string) {
	values := map[string]string{"host": host}
	for name, vs := range h {
		lower := strings.ToLower(name)
		if lower == "authorization" {
			continue
		}
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			// Trim and collapse runs of spaces, as the spec says.
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		values[lower] = strings.Join(trimmed, ",")
	}
	names := only
	if names == nil {
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(values[name])
		b.WriteByte('\n')
	}
	return names, b.String()
}

func canonicalRequest(method, escapedPath, rawQuery, canonHeaders string, signed []string, payloadHash string) string {
	if escapedPath == "" {
		escapedPath = "/"
	}
	return method + "\n" + escapedPath + "\n" + canonicalQuery(rawQuery) + "\n" +
		canonHeaders + "\n" + strings.Join(signed, ";") + "\n" + payloadHash
}

func stringToSign(t time.Time, scope, creq string) string {
	sum := sha256.Sum256([]byte(creq))
	return Algorithm + "\n" + t.UTC().Format(TimeFormat) + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

func signingKey(secret string, t time.Time, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), t.UTC().Format(dateFormat))
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, terminator)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
