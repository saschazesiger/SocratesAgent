package s3

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinels to test an *Error against with errors.Is. They exist so that a
// caller never compares status numbers or code strings: what "not found"
// or "lost the race" looks like differs between S3 implementations, and
// that knowledge belongs here.
var (
	// ErrNotFound is a missing object: 404 with NoSuchKey or (for HEAD,
	// which has no body) no code at all. A missing bucket is NOT this; it
	// is ErrNoSuchBucket, because "the object is not there yet" and "the
	// configuration points nowhere" call for very different reactions.
	ErrNotFound = errors.New("s3: object not found")
	// ErrPreconditionFailed is a conditional write that lost: 412
	// PreconditionFailed, or 409 ConditionalRequestConflict which AWS sends
	// when two conditional writes to the same key race.
	ErrPreconditionFailed = errors.New("s3: precondition failed")
	// ErrNoSuchBucket is a bucket that does not exist.
	ErrNoSuchBucket = errors.New("s3: no such bucket")
	// ErrAccessDenied covers wrong credentials and missing permissions:
	// 403 AccessDenied, InvalidAccessKeyId, SignatureDoesNotMatch.
	ErrAccessDenied = errors.New("s3: access denied")
)

// Error is a non-2xx answer from the server.
type Error struct {
	Method     string
	Key        string // "" for a bucket level request
	StatusCode int
	// Code and Message come from the XML error body when there is one
	// (HEAD responses never have one).
	Code      string
	Message   string
	RequestID string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "s3: %s %s: %d", e.Method, opName(e.Key), e.StatusCode)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	} else {
		b.WriteString(" " + http.StatusText(e.StatusCode))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" (request " + e.RequestID + ")")
	}
	return b.String()
}

// Is maps the error onto the sentinels above.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound && (e.Code == "" || e.Code == "NoSuchKey")
	case ErrNoSuchBucket:
		return e.Code == "NoSuchBucket"
	case ErrPreconditionFailed:
		return e.StatusCode == http.StatusPreconditionFailed || e.Code == "PreconditionFailed" ||
			e.Code == "ConditionalRequestConflict"
	case ErrAccessDenied:
		return e.StatusCode == http.StatusForbidden || e.Code == "AccessDenied" ||
			e.Code == "InvalidAccessKeyId" || e.Code == "SignatureDoesNotMatch"
	}
	return false
}

func newError(method, key string, resp *http.Response, body []byte) *Error {
	e := &Error{Method: method, Key: key, StatusCode: resp.StatusCode}
	var x struct {
		Code      string
		Message   string
		RequestID string `xml:"RequestId"`
	}
	if len(body) > 0 && xml.Unmarshal(body, &x) == nil {
		e.Code, e.Message, e.RequestID = x.Code, x.Message, x.RequestID
	}
	if e.RequestID == "" {
		e.RequestID = resp.Header.Get("X-Amz-Request-Id")
	}
	return e
}
