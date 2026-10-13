package sigv4

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The four worked examples from the AWS documentation "Signature
// Calculations for the Authorization Header: Transferring Payload in a
// Single Chunk (AWS Signature Version 4)". They are the only outside
// reference for the canonicalisation rules; the fake server shares this
// code, so these vectors are what keeps the shared reading honest.
var (
	exampleCreds = Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	exampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
)

func TestAWSVectors(t *testing.T) {
	cases := []struct {
		name, method, url string
		headers           map[string]string
		body              string
		signed, signature string
	}{
		{
			name:      "GET object with Range",
			method:    "GET",
			url:       "https://examplebucket.s3.amazonaws.com/test.txt",
			headers:   map[string]string{"Range": "bytes=0-9"},
			signed:    "host;range;x-amz-content-sha256;x-amz-date",
			signature: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name:   "PUT object",
			method: "PUT",
			url:    "https://examplebucket.s3.amazonaws.com/" + EscapePath("test$file.text"),
			headers: map[string]string{
				"Date":                "Fri, 24 May 2013 00:00:00 GMT",
				"X-Amz-Storage-Class": "REDUCED_REDUNDANCY",
			},
			body:      "Welcome to Amazon S3.",
			signed:    "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			signature: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name:      "GET bucket lifecycle",
			method:    "GET",
			url:       "https://examplebucket.s3.amazonaws.com/?lifecycle",
			signed:    "host;x-amz-content-sha256;x-amz-date",
			signature: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name:      "list objects",
			method:    "GET",
			url:       "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			signed:    "host;x-amz-content-sha256;x-amz-date",
			signature: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, c.url, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			hash := PayloadHash([]byte(c.body))
			if c.body == "" && hash != EmptySHA256 {
				t.Fatalf("empty hash %s", hash)
			}
			Sign(req, exampleCreds, "us-east-1", "s3", hash, exampleTime)
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
				"SignedHeaders=" + c.signed + ", Signature=" + c.signature
			if got := req.Header.Get("Authorization"); got != want {
				t.Errorf("Authorization\n got %s\nwant %s", got, want)
			}

			// The same request, seen by a server, verifies.
			srv := serverSide(t, req, c.body)
			if err := Verify(srv, []byte(c.body), exampleCreds, "us-east-1", "s3", exampleTime, 15*time.Minute); err != nil {
				t.Errorf("Verify: %v", err)
			}
		})
	}
}

// serverSide turns a client request into what a handler would receive: the
// path parsed back from the wire form, Host moved out of the headers.
func serverSide(t *testing.T, req *http.Request, body string) *http.Request {
	t.Helper()
	srv := httptest.NewRequest(req.Method, req.URL.RequestURI(), strings.NewReader(body))
	srv.Host = req.URL.Host
	for k, v := range req.Header {
		srv.Header[k] = v
	}
	return srv
}

func TestVerifyRejects(t *testing.T) {
	sign := func() (*http.Request, string) {
		body := "payload"
		req, _ := http.NewRequest("PUT", "https://h.example/bucket/"+EscapePath("a b/ü+=.txt")+"?x=1", strings.NewReader(body))
		req.Header.Set("If-None-Match", "*")
		Sign(req, exampleCreds, "hel1", "s3", PayloadHash([]byte(body)), exampleTime)
		return serverSide(t, req, body), body
	}
	verify := func(r *http.Request, body string) error {
		return Verify(r, []byte(body), exampleCreds, "hel1", "s3", exampleTime.Add(time.Minute), 15*time.Minute)
	}

	r, body := sign()
	if err := verify(r, body); err != nil {
		t.Fatalf("good request: %v", err)
	}

	r, body = sign()
	r.Header.Del("If-None-Match") // a stripped condition must break the signature
	if verify(r, body) == nil {
		t.Error("removed signed header verified")
	}

	r, _ = sign()
	if verify(r, "other body") == nil {
		t.Error("changed body verified")
	}

	r, body = sign()
	r.Header.Del(HeaderContentSHA256)
	if err := verify(r, body); err == nil || !strings.Contains(err.Error(), "x-amz-content-sha256") {
		t.Errorf("missing content hash: %v", err)
	}

	r, body = sign()
	if Verify(r, []byte(body), Credentials{"AKIAIOSFODNN7EXAMPLE", "wrong"}, "hel1", "s3", exampleTime, time.Minute) == nil {
		t.Error("wrong secret verified")
	}

	r, body = sign()
	if Verify(r, []byte(body), exampleCreds, "hel1", "s3", exampleTime.Add(time.Hour), 15*time.Minute) == nil {
		t.Error("stale request verified")
	}

	r, body = sign()
	r.URL.RawQuery = "x=2"
	if verify(r, body) == nil {
		t.Error("changed query verified")
	}
}

func TestEscape(t *testing.T) {
	if got := EscapePath("/kv/a b+c~d/é"); got != "/kv/a%20b%2Bc~d/%C3%A9" {
		t.Errorf("EscapePath: %s", got)
	}
	if got := EscapeQuery("devtest/x y"); got != "devtest%2Fx%20y" {
		t.Errorf("EscapeQuery: %s", got)
	}
	if got := canonicalQuery("prefix=a%2Fb&list-type=2&lifecycle&max-keys=1"); got != "lifecycle=&list-type=2&max-keys=1&prefix=a%2Fb" {
		t.Errorf("canonicalQuery: %s", got)
	}
}
