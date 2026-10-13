package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Conditional reports which conditional writes a store really enforces.
type Conditional struct {
	// IfNoneMatch: a Put with If-None-Match "*" on an existing key fails
	// with ErrPreconditionFailed (and succeeds on a missing one).
	IfNoneMatch bool
	// IfMatch: a Put with a stale If-Match fails with ErrPreconditionFailed
	// (and succeeds with the current ETag).
	IfMatch bool
}

// Both is true when the store enforces both conditions, which is what a
// lease needs to be race free without a read-back.
func (c Conditional) Both() bool { return c.IfNoneMatch && c.IfMatch }

// ProbeConditional finds out by experiment whether the store honours
// conditional PUTs, using key as scratch space (it is overwritten and
// deleted).
//
// Asking is the only way to know. AWS added If-None-Match in 2024 and
// If-Match later; Ceph RGW has supported both for years; other stores, and
// older versions of all of them, accept the headers and silently ignore
// them - a "conditional" write that overwrites whatever is there. A store
// that refuses the header outright (400/501) is treated as not supporting
// it. Network and permission errors are returned as errors, not as "no".
func ProbeConditional(ctx context.Context, c *Client, key string) (Conditional, error) {
	var res Conditional
	if err := c.Delete(ctx, key); err != nil {
		return res, err
	}
	defer c.Delete(context.WithoutCancel(ctx), key)

	// Create with If-None-Match on a key that is not there: must succeed
	// where the header is understood.
	etag, err := c.Put(ctx, key, []byte("probe 1"), &PutOptions{IfNoneMatch: "*"})
	createRefused := false
	if err != nil {
		if !refusedHeader(err) {
			return res, fmt.Errorf("s3: probing conditional writes: %w", err)
		}
		createRefused = true
		if etag, err = c.Put(ctx, key, []byte("probe 1"), nil); err != nil {
			return res, fmt.Errorf("s3: probing conditional writes: %w", err)
		}
	}

	// Now it exists: If-None-Match must fail.
	_, err = c.Put(ctx, key, []byte("probe 2"), &PutOptions{IfNoneMatch: "*"})
	switch {
	case errors.Is(err, ErrPreconditionFailed):
		res.IfNoneMatch = !createRefused
	case err == nil:
		// Ignored, and the object was overwritten: refresh the ETag.
		h, herr := c.Head(ctx, key)
		if herr != nil {
			return res, fmt.Errorf("s3: probing conditional writes: %w", herr)
		}
		etag = h.ETag
	case refusedHeader(err):
	default:
		return res, fmt.Errorf("s3: probing conditional writes: %w", err)
	}

	// A stale If-Match must fail ...
	_, err = c.Put(ctx, key, []byte("probe 3"), &PutOptions{IfMatch: `"00000000000000000000000000000000"`})
	staleRejected := errors.Is(err, ErrPreconditionFailed)
	if err != nil && !staleRejected && !refusedHeader(err) {
		return res, fmt.Errorf("s3: probing conditional writes: %w", err)
	}
	if err == nil {
		h, herr := c.Head(ctx, key)
		if herr != nil {
			return res, fmt.Errorf("s3: probing conditional writes: %w", herr)
		}
		etag = h.ETag
	}
	// ... and the current one must pass. Only both together is support.
	if staleRejected {
		_, err = c.Put(ctx, key, []byte("probe 4"), &PutOptions{IfMatch: etag})
		switch {
		case err == nil:
			res.IfMatch = true
		case errors.Is(err, ErrPreconditionFailed) || refusedHeader(err):
		default:
			return res, fmt.Errorf("s3: probing conditional writes: %w", err)
		}
	}
	return res, nil
}

// refusedHeader is a store saying it does not do conditional writes at all,
// as opposed to evaluating the condition.
func refusedHeader(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.StatusCode == http.StatusNotImplemented || e.Code == "NotImplemented" ||
		(e.StatusCode == http.StatusBadRequest && !errors.Is(err, ErrAccessDenied))
}
