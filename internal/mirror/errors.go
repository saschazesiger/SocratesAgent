package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

var (
	// ErrWrongPassphrase: the bucket's meta.json was sealed with another
	// passphrase. Nothing in the bucket can be read with this one.
	ErrWrongPassphrase = errors.New("the encryption passphrase does not match this bucket")
	// ErrNewBucket: there is no Socrates state at this place yet. The
	// connect page answers it by asking for the passphrase a second time,
	// because a typo in a passphrase that is about to be used for the first
	// time is one nobody can ever undo.
	ErrNewBucket = errors.New("this bucket holds no Socrates state yet")
	// ErrConflict: this data directory and the bucket each hold a different
	// installation, and somebody has to say which one to keep.
	ErrConflict = errors.New("this installation and the bucket hold different state")
	// ErrFenced: another Socrates took this bucket over, and this one no
	// longer writes to it.
	ErrFenced = errors.New("another Socrates took over this bucket")
	// ErrWrongPassword: the Socrates password given to take a bucket over
	// is not the one the bucket holds.
	ErrWrongPassword = errors.New("wrong password")
	// ErrForeignBucket: the bucket now holds a different installation than
	// the one this data directory was synchronised with.
	ErrForeignBucket = errors.New("the bucket now holds a different Socrates installation")
	// ErrNotConnected: an operation that needs the bucket was asked of a
	// mirror that has not reached it yet.
	ErrNotConnected = errors.New("the bucket has not been reached yet")
	// ErrMetaMissing: the place holds Socrates objects but no meta.json. A
	// new meta.json would come with a new salt and make every one of those
	// objects unreadable for good, so nothing is created over them.
	ErrMetaMissing = errors.New("this place holds Socrates objects but its meta.json is missing; " +
		"restore meta.json, or empty the prefix (or choose to overwrite it) to start a new installation there")
)

// LeaseHeldError says the bucket is in use by another, live Socrates.
type LeaseHeldError struct {
	Lease Lease
}

func (e *LeaseHeldError) Error() string {
	host := e.Lease.Host
	if host == "" {
		host = "another Socrates"
	}
	seen := time.UnixMilli(e.Lease.HeartbeatAt).UTC().Format(time.RFC3339)
	return fmt.Sprintf("this bucket is in use by %s (last seen %s)", host, seen)
}

// UnreachableError is a failure that says nothing about the bucket itself:
// the network, a timeout, a store answering 5xx. It is the one kind of
// failure a server may start degraded on, from its local copy.
type UnreachableError struct {
	Err error
}

func (e *UnreachableError) Error() string { return "could not reach the bucket: " + e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

// Transient reports whether err is a failure to reach the bucket rather than
// an answer from it. An access denied, a bucket that does not exist or a wrong
// passphrase are answers: retrying them changes nothing, and starting from the
// local copy on one of them would hide a misconfiguration behind a server that
// seems to work.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	var u *UnreachableError
	if errors.As(err, &u) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var se *s3.Error
	if errors.As(err, &se) {
		return se.StatusCode >= 500 || se.StatusCode == http.StatusTooManyRequests ||
			se.StatusCode == http.StatusRequestTimeout
	}
	if errors.Is(err, context.Canceled) {
		// The caller going away, not the bucket.
		return false
	}
	// Not an answer from S3 at all: DNS, a refused connection, TLS, a reset,
	// a body cut off half way.
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// classify wraps a transient failure as an UnreachableError and leaves an
// answer alone.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var u *UnreachableError
	if errors.As(err, &u) || !Transient(err) {
		return err
	}
	return &UnreachableError{Err: err}
}

// Explain turns a failure into the sentence the connect page and the admin
// card show. It never contains a secret: the S3 errors carry a code and a
// message from the store, and the store does not know the secret.
func Explain(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, s3.ErrNoSuchBucket):
		return "The bucket does not exist (or this endpoint and region do not have it)."
	case errors.Is(err, s3.ErrAccessDenied):
		return "The store refused the access key: check the key id, the secret and the region. (" + err.Error() + ")"
	case errors.Is(err, ErrWrongPassphrase):
		return "The encryption passphrase does not match this bucket."
	}
	return err.Error()
}
