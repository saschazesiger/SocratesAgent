package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

// The lease is how one bucket ends up with exactly one Socrates writing to it.
//
// Two containers writing through to one bucket would each upload their own
// session rows over the other's, and the bucket would end up a patchwork
// that neither of them holds. So a Socrates writes nothing before it holds
// lease.json, keeps it fresh with a heartbeat, and stops writing the moment
// it sees the lease name somebody else - it is then "fenced".
//
// A lease is valid while its heartbeat is younger than the TTL (90 s, three
// missed heartbeats). An expired lease, or one this very data directory
// wrote (a restart), is simply taken; a fresh one of another machine is only
// taken when a person asks for it on the connect page and proves it with the
// Socrates password.
//
// The writes are conditional where the store enforces conditions - If-None-
// Match "*" to create, If-Match on the ETag last seen to replace - which makes
// two racing writers impossible. A store that ignores the headers gets the
// fallback the spec allows: write, wait a moment, read back, and only believe
// a lease that still names us. That narrows the race to the settle window
// rather than closing it, which is the best a store without conditions
// allows, and the heartbeat's own read-back fences the loser within one beat.
//
// Fencing has a window of its own: between another machine taking the lease
// and this one noticing, this one may still upload. That window is bounded,
// not just narrowed. Every write to the bucket - each object of an upload
// round, the final flush, the startup upload - starts and has to finish
// within Options.VerifyEvery of the last confirmation of the lease
// (confirmedAt, taken before the read or write that confirmed it): a write
// that would run past it confirms the lease again first, and one that cannot
// is not made and stays pending. A confirmation only counts while the lease
// stays fresh for that whole window, so a holder whose heartbeats fail stops
// writing before its lease can expire and be taken. A machine that takes a
// lease over - by force on the connect page, or because it had expired while
// still naming another instance - waits Options.TakeoverSettle (VerifyEvery
// plus a margin for a request whose answer was not waited for) before it
// reads anything, so whatever the old holder still managed to write is in
// the bucket before the new holder hydrates.
//
// The lease is also the record of who wrote to the bucket last. A graceful
// release does not delete lease.json; it zeroes the heartbeat and keeps the
// instance, so the next starter takes it at once and still learns whose it
// was. A start that finds the lease last held by another instance knows the
// bucket has been written by somebody else since this data directory last
// uploaded, and the writes this directory still has pending are older than
// the bucket, not newer (see lastWriterWasUs and hydrate).

// Lease is lease.json.
type Lease struct {
	Instance    string `json:"instance"`
	Host        string `json:"host"`
	Version     string `json:"version"`
	AcquiredAt  int64  `json:"acquired_at"`
	HeartbeatAt int64  `json:"heartbeat_at"`
}

// Fresh reports whether the lease is still valid at now.
func (l Lease) Fresh(now time.Time, ttl time.Duration) bool {
	return l.Instance != "" && l.HeartbeatAt > 0 && now.Sub(time.UnixMilli(l.HeartbeatAt)) < ttl
}

// errLeaseExpired: the lease still names this instance, but its heartbeat is
// older than the TTL - the renewals have been failing. Anybody may take it
// now, so nothing is written until a renewal has made it fresh again.
var errLeaseExpired = errors.New("this Socrates' own lease has expired (the heartbeats have not reached the bucket)")

// errNotHeld: a write was asked of a leaser that holds nothing.
var errNotHeld = errors.New("this Socrates does not hold the lease")

// leaser holds and renews the lease of one place. op serialises its requests:
// the flusher, the heartbeat, the startup keepalive and an upload confirming
// the lease again can all call it, and a renewal must never interleave with
// a verify. mu only guards the fields other goroutines read.
type leaser struct {
	b        func() bucket
	instance string
	host     string
	version  string
	now      func() time.Time
	ttl      time.Duration
	settle   time.Duration
	// window is how long a confirmation lets writes go on (VerifyEvery).
	window time.Duration

	op sync.Mutex

	mu sync.Mutex
	// cond is what the probe found. It is written once, before the lease is
	// first taken, and under mu for the status page that reads it from
	// another goroutine.
	cond        s3.Conditional
	held        bool
	etag        string
	current     Lease
	confirmedAt time.Time
	// other is the lease that fenced this one, for the banner.
	other Lease
	// prev is what acquire found in the bucket before it wrote, and
	// prevExists whether there was anything: who held the lease last.
	prev       Lease
	prevExists bool
}

// read fetches lease.json. exists is false when there is none.
func (l *leaser) read(ctx context.Context) (Lease, string, bool, error) {
	obj, err := l.b().get(ctx, leaseObject)
	if errors.Is(err, s3.ErrNotFound) {
		return Lease{}, "", false, nil
	}
	if err != nil {
		return Lease{}, "", false, err
	}
	var lease Lease
	if err := json.Unmarshal(obj.Body, &lease); err != nil {
		// A lease nobody can read is a lease nobody holds. Treating it as
		// held would lock the bucket for good over a damaged object.
		return Lease{}, obj.ETag, true, nil
	}
	return lease, obj.ETag, true, nil
}

// peek is the lease as it is in the bucket, for a page that has to say who
// holds it.
func (l *leaser) peek(ctx context.Context) (Lease, bool, error) {
	lease, _, exists, err := l.read(ctx)
	return lease, exists, err
}

func (l *leaser) write(ctx context.Context, lease Lease, opts *s3.PutOptions) (string, error) {
	raw, err := json.Marshal(lease)
	if err != nil {
		return "", err
	}
	if opts == nil {
		opts = &s3.PutOptions{}
	}
	opts.ContentType = "application/json"
	return l.b().put(ctx, leaseObject, raw, opts)
}

// conditionFailed reports a conditional write that lost. On AWS an If-Match
// against a key that is gone is a 404 rather than a 412; both mean the same
// thing here.
func conditionFailed(err error, opts *s3.PutOptions) bool {
	if errors.Is(err, s3.ErrPreconditionFailed) {
		return true
	}
	return opts != nil && opts.IfMatch != "" && errors.Is(err, s3.ErrNotFound)
}

// wait sleeps for d or until ctx ends.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// acquire takes the lease: when there is none, when it has expired, when it
// is this instance's own, or - with force - whatever it says. A fresh lease of
// somebody else is a *LeaseHeldError.
//
// It answers the lease it replaced when that one named another instance and
// was not released (its heartbeat is not zero): that holder may still have
// writes in flight, and the caller waits TakeoverSettle before reading.
func (l *leaser) acquire(ctx context.Context, force bool) (Lease, error) {
	l.op.Lock()
	defer l.op.Unlock()
	for attempt := 0; attempt < 4; attempt++ {
		cur, etag, exists, err := l.read(ctx)
		if err != nil {
			return Lease{}, err
		}
		now := l.now()
		own := exists && cur.Instance == l.instance
		if exists && !own && !force && cur.Fresh(now, l.ttl) {
			return Lease{}, &LeaseHeldError{Lease: cur}
		}
		next := Lease{
			Instance: l.instance, Host: l.host, Version: l.version,
			AcquiredAt: now.UnixMilli(), HeartbeatAt: now.UnixMilli(),
		}
		if own && cur.AcquiredAt > 0 {
			next.AcquiredAt = cur.AcquiredAt
		}
		var opts *s3.PutOptions
		switch {
		case exists && l.cond.IfMatch && etag != "":
			opts = &s3.PutOptions{IfMatch: etag}
		case !exists && l.cond.IfNoneMatch:
			opts = &s3.PutOptions{IfNoneMatch: "*"}
		}
		conditional := opts != nil
		newETag, err := l.write(ctx, next, opts)
		if conditionFailed(err, opts) {
			// Somebody wrote between our read and our write. Look again:
			// it may have been an expired holder's last heartbeat, or a
			// rival that is now the holder.
			continue
		}
		if err != nil {
			return Lease{}, err
		}
		if !conditional {
			won, theirs, readETag, err := l.readBack(ctx, next)
			if err != nil {
				return Lease{}, err
			}
			if !won {
				if force {
					continue
				}
				return Lease{}, &LeaseHeldError{Lease: theirs}
			}
			newETag = readETag
		}
		l.mu.Lock()
		l.held, l.etag, l.current, l.confirmedAt = true, newETag, next, now
		l.other = Lease{}
		l.prev, l.prevExists = cur, exists
		l.mu.Unlock()
		var took Lease
		if exists && !own && cur.HeartbeatAt != 0 {
			took = cur
		}
		return took, nil
	}
	return Lease{}, errors.New("the lease kept changing while it was being taken; another Socrates is starting on this bucket")
}

// lastWriterWasUs reports whether the lease the last acquire replaced was
// this instance's own - or there was none at all, a bucket nobody has held
// yet. False means another Socrates held the bucket after this data
// directory last did, and may have written to it: what is pending here is
// then older than the bucket.
//
// A lease that cannot be read says nothing about who wrote, and counts as
// somebody else's: the bucket is the record, and keeping it is the side that
// never undoes another machine's work.
func (l *leaser) lastWriterWasUs() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.prevExists || (l.prev.Instance != "" && l.prev.Instance == l.instance)
}

// readBack is the fallback for a store without conditional writes: wait for a
// rival's write to land, then believe the lease only if it is still ours,
// with the heartbeat we wrote.
func (l *leaser) readBack(ctx context.Context, wrote Lease) (bool, Lease, string, error) {
	if err := wait(ctx, l.settle); err != nil {
		return false, Lease{}, "", err
	}
	cur, etag, exists, err := l.read(ctx)
	if err != nil {
		return false, Lease{}, "", err
	}
	if !exists || cur.Instance != wrote.Instance || cur.HeartbeatAt != wrote.HeartbeatAt {
		return false, cur, "", nil
	}
	return true, cur, etag, nil
}

// fence records that the lease is somebody else's now.
func (l *leaser) fence(other Lease) error {
	l.mu.Lock()
	l.held = false
	l.other = other
	l.mu.Unlock()
	return ErrFenced
}

// renew is the heartbeat. A lease that is no longer ours fences this mirror
// (ErrFenced); a request that fails is just an error, and the lease stays
// ours for as long as the TTL says.
func (l *leaser) renew(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	return l.renewLocked(ctx)
}

func (l *leaser) renewLocked(ctx context.Context) error {
	l.mu.Lock()
	held, etag, current := l.held, l.etag, l.current
	l.mu.Unlock()
	if !held {
		return ErrFenced
	}
	for attempt := 0; attempt < 2; attempt++ {
		now := l.now()
		next := current
		next.HeartbeatAt = now.UnixMilli()
		if l.cond.IfMatch && etag != "" {
			opts := &s3.PutOptions{IfMatch: etag}
			newETag, err := l.write(ctx, next, opts)
			if conditionFailed(err, opts) {
				cur, curETag, exists, rerr := l.read(ctx)
				if rerr != nil {
					return rerr
				}
				if exists && cur.Instance == l.instance {
					// Ours after all: a write of ours whose answer was lost
					// moved the ETag. Carry on from what is there.
					etag, current = curETag, cur
					continue
				}
				return l.fence(cur)
			}
			if err != nil {
				return err
			}
			l.mu.Lock()
			l.etag, l.current, l.confirmedAt = newETag, next, now
			l.mu.Unlock()
			return nil
		}
		cur, _, exists, err := l.read(ctx)
		if err != nil {
			return err
		}
		if !exists || cur.Instance != l.instance {
			return l.fence(cur)
		}
		if _, err := l.write(ctx, next, nil); err != nil {
			return err
		}
		won, theirs, newETag, err := l.readBack(ctx, next)
		if err != nil {
			return err
		}
		if !won {
			return l.fence(theirs)
		}
		l.mu.Lock()
		l.etag, l.current, l.confirmedAt = newETag, next, now
		l.mu.Unlock()
		return nil
	}
	return fmt.Errorf("the lease changed twice during one heartbeat")
}

// verify is the cheap check before writing: one read, and a fence when the
// lease names somebody else or has gone. A confirmation is only given for a
// lease that stays fresh for the whole window it covers; one that would
// expire within it is renewed instead, and one that has expired already
// confirms nothing (errLeaseExpired) - anybody may be taking it right now.
func (l *leaser) verify(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	return l.verifyLocked(ctx)
}

func (l *leaser) verifyLocked(ctx context.Context) error {
	start := l.now()
	cur, etag, exists, err := l.read(ctx)
	if err != nil {
		return err
	}
	if !exists || cur.Instance != l.instance {
		return l.fence(cur)
	}
	if !cur.Fresh(start, l.ttl) {
		return errLeaseExpired
	}
	l.mu.Lock()
	l.etag, l.current = etag, cur
	l.mu.Unlock()
	if start.Sub(time.UnixMilli(cur.HeartbeatAt))+l.window >= l.ttl {
		return l.renewLocked(ctx)
	}
	l.mu.Lock()
	l.confirmedAt = start
	l.mu.Unlock()
	return nil
}

// remaining is how much of the window of the last confirmation is left, and
// whether the lease is held at all.
func (l *leaser) remaining() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return 0, false
	}
	return l.confirmedAt.Add(l.window).Sub(l.now()), true
}

// ensure makes sure at least need of the window is left before a write,
// confirming the lease again when it is not, and answers what is left. Many
// workers may ask at once; one of them confirms, and the others find the
// fresh confirmation when they get the lock.
func (l *leaser) ensure(ctx context.Context, need time.Duration) (time.Duration, error) {
	if rem, held := l.remaining(); !held {
		return 0, errNotHeld
	} else if rem >= need {
		return rem, nil
	}
	l.op.Lock()
	defer l.op.Unlock()
	if rem, held := l.remaining(); !held {
		return 0, errNotHeld
	} else if rem >= need {
		return rem, nil
	}
	if err := l.verifyLocked(ctx); err != nil {
		return 0, err
	}
	rem, held := l.remaining()
	if !held {
		return 0, errNotHeld
	}
	if rem <= 0 {
		return 0, errors.New("the bucket answered too slowly to confirm the lease in time")
	}
	return rem, nil
}

// release lets go of the lease on a graceful shutdown, so the next Socrates
// - this one restarted, or another machine - can take it at once instead of
// waiting out the TTL. It only touches a lease that is still ours, and it
// does not delete it: the heartbeat goes to zero, which makes it free, and
// the instance stays, which keeps the record of who wrote last.
func (l *leaser) release(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.mu.Lock()
	held, etag, current := l.held, l.etag, l.current
	l.held = false
	l.mu.Unlock()
	if !held {
		return nil
	}
	next := current
	next.HeartbeatAt = 0
	if l.cond.IfMatch && etag != "" {
		opts := &s3.PutOptions{IfMatch: etag}
		_, err := l.write(ctx, next, opts)
		if !conditionFailed(err, opts) {
			return err
		}
		// Somebody wrote since our last look - a write of ours whose answer
		// was lost, or a takeover. Fall through to look.
	}
	cur, _, exists, err := l.read(ctx)
	if err != nil {
		return err
	}
	if !exists || cur.Instance != l.instance {
		return nil
	}
	cur.HeartbeatAt = 0
	_, err = l.write(ctx, cur, nil)
	return err
}

// conditional is what the probe found, for the status page.
func (l *leaser) conditional() s3.Conditional {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cond
}

// state is what the status page shows: the lease as it was last seen, and
// whether it is ours.
func (l *leaser) state() (lease Lease, held bool, confirmedAt time.Time, other Lease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.current, l.held, l.confirmedAt, l.other
}

// previous is the lease the last acquire replaced, for the log.
func (l *leaser) previous() Lease {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.prev
}

// giveBack lets go of a lease that this instance took without writing
// anything to the bucket under it - a start that failed before it uploaded.
// The record of the last writer goes back to what it was: the lease the
// acquire replaced, released, or none at all. Otherwise a start that only
// looked would count as the last writer, and the machine that really was
// would take its own pending writes for stale.
func (l *leaser) giveBack(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	l.mu.Lock()
	held, prev, prevExists := l.held, l.prev, l.prevExists
	l.held = false
	l.mu.Unlock()
	if !held {
		return nil
	}
	cur, _, exists, err := l.read(ctx)
	if err != nil {
		return err
	}
	if !exists || cur.Instance != l.instance {
		return nil
	}
	if !prevExists {
		return l.b().delete(ctx, leaseObject)
	}
	prev.HeartbeatAt = 0
	_, err = l.write(ctx, prev, nil)
	return err
}
