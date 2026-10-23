// Package mirror makes an S3 bucket the store of record for Socrates' own
// state, with the local SQLite database as its working copy.
//
// Every package keeps reading and writing internal/store exactly as before.
// The store records every committed write as a pending object name in the
// same database (mirror_pending), so a crash between a write and its upload
// loses neither, and tells this package through an observer. The mirror
// waits for the writes to settle - a second of quiet, never more than five
// seconds after the first - and then uploads the current local value of every
// pending object, sealed with the vault key, or deletes the object when its
// row is gone. A failed round keeps everything pending and backs off.
//
// At startup the direction reverses (Connect, sync.go): the local copy is
// made equal to the bucket, after uploading what this machine wrote and had
// not uploaded yet. That is what lets an installation on a new machine, with
// an empty data directory, come up with the same password, settings and
// session list.
//
// What is not in the bucket on purpose: HOME and the CLIs' logins and
// transcripts, the workspaces, tmux, journals, plan.json - the machine keeps
// those - and the few key/value rows that describe this machine only
// (store.MirroredKV).
//
// One bucket has one writer at a time, enforced with the lease (lease.go).
package mirror

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The mirror's own key/value rows. They are all "mirror.*", which
// store.MirroredKV keeps on this machine: they describe this data directory's
// relation to a bucket, not state to restore.
const (
	// kvInstance is this data directory's lease identity. It is persisted so
	// that a restart re-acquires its own lease at once instead of waiting
	// out the TTL of the lease it held a second ago.
	kvInstance = "mirror.instance"
	// kvBucketID is the installation (meta.json's bucket_id) the local copy
	// was last synchronised with. Equal to the bucket's: the local pending
	// writes are newer than the bucket and go up first. Different: the
	// bucket wins.
	kvBucketID = "mirror.bucket_id"
	// kvIdentity is the endpoint, bucket and prefix of that place, so that a
	// configuration pointed somewhere else never starts degraded from a copy
	// of another bucket.
	kvIdentity = "mirror.identity"
	// kvMeta caches meta.json, which is what a degraded start derives its
	// key from when the bucket cannot be read.
	kvMeta = "mirror.meta"
)

// PasswordKey is the key/value row of the Socrates password hash, the one
// row the mirror reads by name: taking a bucket over is proved with the
// password stored in it. It is server.PasswordKey, spelled again here because
// this package sits below the server; a server test pins the two together.
const PasswordKey = "password_hash"

// Options are the mirror's knobs. The zero value of each duration is the
// production default; tests shrink them.
type Options struct {
	// Instance is the lease identity; InstanceID gives the persisted one.
	Instance string
	// Host and Version are written into the lease, for the connect page of
	// another machine.
	Host    string
	Version string
	// DataDir is where s3.json lives, for a change of credentials; Source is
	// where the configuration came from.
	DataDir string
	Source  Source
	// Now is the clock of the lease timestamps.
	Now func() time.Time
	// HTTPClient reaches the store; nil is the s3 package's default.
	HTTPClient *http.Client
	// Iterations is the PBKDF2 count of a new bucket (vault.Iterations).
	Iterations int

	Debounce   time.Duration // quiet before an upload round (1 s)
	MaxLatency time.Duration // longest a write waits for its upload (5 s)
	MaxBackoff time.Duration // longest wait between failed rounds (60 s)
	Heartbeat  time.Duration // lease renewal (30 s)
	LeaseTTL   time.Duration // lease validity without a heartbeat (90 s)
	// VerifyEvery is how long a confirmation of the lease lets writes go on
	// (10 s): every write starts and ends within it, or confirms again first.
	VerifyEvery time.Duration
	Settle      time.Duration // read-back delay without conditional writes (1 s)
	// TakeoverSettle is the wait after taking over a lease another instance
	// held and had not released (VerifyEvery + 2 s): the longest the old
	// holder can still be writing, plus a margin for a request it sent and
	// stopped waiting for.
	TakeoverSettle time.Duration
	OpTimeout      time.Duration // one request (30 s)
	Workers        int           // parallel requests in a round (4)

	// Logf reports what the mirror does; nil is log.Printf.
	Logf func(format string, args ...any)
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&o.Debounce, time.Second)
	set(&o.MaxLatency, 5*time.Second)
	set(&o.MaxBackoff, 60*time.Second)
	set(&o.Heartbeat, 30*time.Second)
	set(&o.LeaseTTL, 90*time.Second)
	set(&o.VerifyEvery, 10*time.Second)
	set(&o.Settle, time.Second)
	set(&o.TakeoverSettle, o.VerifyEvery+2*time.Second)
	set(&o.OpTimeout, 30*time.Second)
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Iterations <= 0 {
		o.Iterations = vault.Iterations
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Host == "" {
		o.Host, _ = os.Hostname()
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
}

// InstanceID is this data directory's lease identity, created on first use.
func InstanceID(st *store.Store) (string, error) {
	id, err := st.GetKV(kvInstance)
	if err == nil && strings.TrimSpace(id) != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	id = uuid.NewString()
	return id, st.SetKV(kvInstance, id)
}

// Mirror is a connected (or degraded) mirror of one bucket.
type Mirror struct {
	st   *store.Store
	opts Options

	// op serialises every sequence of requests that has to see a consistent
	// lease: an upload round, a heartbeat, a reconnect, the final flush.
	op sync.Mutex

	mu     sync.Mutex
	cfg    Config
	client *s3.Client
	key    *vault.Key
	meta   Meta
	probed bool

	lease  *leaser
	fenced atomic.Bool

	// status, guarded by mu.
	online      bool
	lastFlushAt int64
	lastOKAt    int64
	lastError   string
	errorSince  int64

	wake    chan struct{}
	stop    context.CancelFunc
	done    chan struct{}
	started atomic.Bool
	closed  atomic.Bool
	// wrote: this mirror has written an object to the bucket (see
	// leaser.giveBack).
	wrote atomic.Bool
}

func newMirror(st *store.Store, cfg Config, opts Options) (*Mirror, error) {
	opts.defaults()
	cfg.Normalize()
	if opts.Instance == "" {
		id, err := InstanceID(st)
		if err != nil {
			return nil, err
		}
		opts.Instance = id
	}
	sc := cfg.S3()
	sc.HTTPClient = opts.HTTPClient
	client, err := s3.New(sc)
	if err != nil {
		return nil, err
	}
	m := &Mirror{st: st, opts: opts, cfg: cfg, client: client, wake: make(chan struct{}, 1)}
	m.lease = &leaser{
		b: m.bucket, instance: opts.Instance, host: opts.Host, version: opts.Version,
		now: opts.Now, ttl: opts.LeaseTTL, settle: opts.Settle, window: opts.VerifyEvery,
	}
	return m, nil
}

// bucket is the place as currently configured. A change of credentials swaps
// the client under a running mirror, so it is read under the lock every time.
func (m *Mirror) bucket() bucket {
	m.mu.Lock()
	defer m.mu.Unlock()
	return bucket{c: m.client, prefix: m.cfg.Prefix, timeout: m.opts.OpTimeout}
}

func (m *Mirror) logf(format string, args ...any) { m.opts.Logf("storage: "+format, args...) }

// probe finds out once whether the store enforces conditional writes. The
// scratch object is per instance and deleted again, so the bucket root stays
// empty apart from Socrates' own objects.
func (m *Mirror) probe(ctx context.Context) error {
	m.mu.Lock()
	done := m.probed
	m.mu.Unlock()
	if done {
		return nil
	}
	b := m.bucket()
	ctx, cancel := context.WithTimeout(ctx, 4*b.timeout)
	defer cancel()
	cond, err := s3.ProbeConditional(ctx, b.c, b.prefix+"probe-"+m.opts.Instance)
	if err != nil {
		return err
	}
	m.lease.mu.Lock()
	m.lease.cond = cond
	m.lease.mu.Unlock()
	m.mu.Lock()
	m.probed = true
	m.mu.Unlock()
	if !cond.Both() {
		m.logf("this store does not enforce conditional writes (if-none-match %v, if-match %v); "+
			"the lease falls back to write and read back", cond.IfNoneMatch, cond.IfMatch)
	}
	return nil
}

// ------------------------------------------------------------------ status

// LeaseStatus is the lease as the status page shows it.
type LeaseStatus struct {
	Instance    string `json:"instance"`
	Host        string `json:"host"`
	Version     string `json:"version"`
	AcquiredAt  int64  `json:"acquired_at"`
	HeartbeatAt int64  `json:"heartbeat_at"`
	// Own: this Socrates holds it.
	Own bool `json:"own"`
}

// Status is the mirror as the admin card and the session page's banner see
// it. It never carries the secret key or the passphrase.
type Status struct {
	Configured bool `json:"configured"`
	// State is "ok" (the bucket answers and nothing is failing), "offline"
	// (the last request failed; writes queue up locally) or "fenced"
	// (another Socrates took the bucket over; nothing is uploaded).
	State     string `json:"state"`
	Connected bool   `json:"connected"`
	Fenced    bool   `json:"fenced"`
	// Pending is how many objects wait for an upload.
	Pending     int    `json:"pending"`
	LastFlushAt int64  `json:"last_flush_at"`
	LastOKAt    int64  `json:"last_ok_at"`
	LastError   string `json:"last_error"`
	ErrorSince  int64  `json:"error_since"`
	// Lease is the lease this Socrates holds, or the one that fenced it.
	Lease             *LeaseStatus `json:"lease"`
	Storage           Public       `json:"storage"`
	BucketID          string       `json:"bucket_id"`
	ConditionalWrites bool         `json:"conditional_writes"`
	Instance          string       `json:"instance"`
}

// Status reports the mirror's state.
func (m *Mirror) Status() Status {
	pending, _ := m.st.PendingCount()
	lease, held, _, other := m.lease.state()
	cond := m.lease.conditional()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{
		Configured: true, Connected: m.online && held, Fenced: m.fenced.Load(),
		Pending: pending, LastFlushAt: m.lastFlushAt, LastOKAt: m.lastOKAt,
		LastError: m.lastError, ErrorSince: m.errorSince,
		Storage: m.cfg.Public(m.opts.Source), BucketID: m.meta.BucketID,
		ConditionalWrites: cond.Both(), Instance: m.opts.Instance,
	}
	switch {
	case st.Fenced:
		st.State = "fenced"
		if other.Instance != "" {
			st.Lease = leaseStatus(other, false)
		}
	case m.online && held:
		st.State = "ok"
	default:
		st.State = "offline"
	}
	if st.Lease == nil && held {
		st.Lease = leaseStatus(lease, true)
	}
	return st
}

func leaseStatus(l Lease, own bool) *LeaseStatus {
	return &LeaseStatus{Instance: l.Instance, Host: l.Host, Version: l.Version,
		AcquiredAt: l.AcquiredAt, HeartbeatAt: l.HeartbeatAt, Own: own}
}

// noteOK records a request round that worked.
func (m *Mirror) noteOK() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online && m.errorSince != 0 {
		m.logf("the bucket answers again")
	}
	m.online = true
	m.lastOKAt = m.opts.Now().UnixMilli()
	m.lastError, m.errorSince = "", 0
}

// noteError records a round that failed. The first failure of a run is
// logged; the same failure repeating every backoff is not.
func (m *Mirror) noteError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg := Explain(err)
	if m.errorSince == 0 {
		m.errorSince = m.opts.Now().UnixMilli()
		m.logf("%s", msg)
	}
	if !errors.Is(err, ErrFenced) {
		m.online = false
	}
	m.lastError = msg
}

// Fenced reports whether another Socrates took the bucket over.
func (m *Mirror) Fenced() bool { return m.fenced.Load() }

// setFenced stops every write to the bucket for the rest of this run.
func (m *Mirror) setFenced(err error) {
	m.st.SetMirrorFenced(true)
	if m.fenced.Swap(true) {
		return
	}
	// The fence outlives this run: the local copy no longer belongs to the
	// bucket's installation in the sense that matters - somebody else is
	// writing it - so the next start takes the bucket's state rather than
	// uploading what was written here meanwhile, and a bucket that cannot
	// be reached then is not a reason to start degraded from this copy.
	if derr := m.st.DeleteKV(kvBucketID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
		m.logf("could not record the fence in the local copy: %v", derr)
	}
	_, _, _, other := m.lease.state()
	host := other.Host
	if host == "" {
		host = "another machine"
	}
	m.logf("FENCED: %s (%s). Nothing is uploaded from here any more; local changes stay on this machine.", err, host)
}

// AutoStartAllowed is the gate of the server's scheduled starts. Only the
// Socrates that holds the bucket starts anything by itself, because the
// schedules are part of the bucket's state, and two machines running from
// one bucket would otherwise both start the same prepared session with the
// same prompt:
//
//   - a fenced Socrates never does: the schedules belong to the one that
//     holds the bucket now;
//   - a degraded one, that has not reached the bucket in this run, does not
//     yet: whether another machine took the bucket meanwhile is exactly what
//     it cannot know. Its overdue schedules run once reconnect takes the
//     lease (the scheduler asks again every pass);
//   - one whose lease has not been confirmed for a whole TTL - the bucket
//     stopped answering - waits too, because by now somebody else may have
//     taken the expired lease.
func (m *Mirror) AutoStartAllowed() error {
	if m.fenced.Load() {
		return ErrFenced
	}
	_, held, confirmed, _ := m.lease.state()
	if !held {
		return fmt.Errorf("%w: the lease has not been taken in this run", ErrNotConnected)
	}
	if !confirmed.IsZero() && m.opts.Now().Sub(confirmed) >= m.opts.LeaseTTL {
		return fmt.Errorf("%w: the lease was last confirmed %s ago", ErrNotConnected,
			m.opts.Now().Sub(confirmed).Round(time.Second))
	}
	return nil
}

// IsClean reports whether the bucket holds the current local value of an
// object - the "backed up" mark beside a draft.
func (m *Mirror) IsClean(object string) bool {
	return m.st.IsBackedUp(object)
}

// ------------------------------------------------------------------ loop

// notify is the store's observer: a write happened. It only signals.
func (m *Mirror) notify(objects ...string) {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Start runs the flusher and the heartbeat until Close.
func (m *Mirror) Start(ctx context.Context) {
	if m.started.Swap(true) {
		return
	}
	ctx, m.stop = context.WithCancel(ctx)
	m.done = make(chan struct{})
	go m.loop(ctx)
}

// loop is the one goroutine that talks to the bucket on its own.
//
// It keeps two deadlines. flushAt is when the next upload round runs: a
// write sets it a debounce from now, but never later than MaxLatency after
// the first write that is still waiting, so a stream of writes cannot hold
// its own upload back for ever; a failed round sets it a backoff from now
// instead. beatAt is the next heartbeat - or, while the bucket has not been
// reached, the next reconnect attempt.
func (m *Mirror) loop(ctx context.Context) {
	defer close(m.done)
	var flushAt, firstDirty time.Time
	failures := 0
	beatAt := time.Now().Add(m.firstBeat())
	if n, _ := m.st.PendingCount(); n > 0 {
		flushAt = time.Now()
	}
	for {
		now := time.Now()
		next := beatAt
		if !flushAt.IsZero() && flushAt.Before(next) {
			next = flushAt
		}
		timer := time.NewTimer(max(next.Sub(now), 0))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.wake:
			timer.Stop()
			if m.fenced.Load() {
				continue
			}
			now = time.Now()
			if firstDirty.IsZero() {
				firstDirty = now
			}
			if failures > 0 && !flushAt.IsZero() {
				// Backing off: a new write does not cut the wait short.
				continue
			}
			flushAt = now.Add(m.opts.Debounce)
			if latest := firstDirty.Add(m.opts.MaxLatency); flushAt.After(latest) {
				flushAt = latest
			}
		case <-timer.C:
			now = time.Now()
			if !flushAt.IsZero() && !now.Before(flushAt) {
				err := m.Sync(ctx)
				switch {
				case err == nil:
					failures, flushAt, firstDirty = 0, time.Time{}, time.Time{}
					// Changed again while uploading: go round again soon.
					if n, _ := m.st.PendingCount(); n > 0 {
						firstDirty = now
						flushAt = now.Add(m.opts.Debounce)
					}
				case m.fenced.Load() || ctx.Err() != nil:
					flushAt = time.Time{}
				default:
					failures++
					flushAt = now.Add(backoff(failures, m.opts.MaxBackoff))
				}
			}
			if !now.Before(beatAt) {
				m.beat(ctx)
				beatAt = time.Now().Add(m.opts.Heartbeat)
				// A beat that reached the bucket again (a degraded start,
				// an outage) leaves the queue to go up now rather than
				// with the next write, which may be a long time coming.
				if n, _ := m.st.PendingCount(); n > 0 && flushAt.IsZero() && !m.fenced.Load() {
					flushAt = time.Now()
				}
			}
		}
	}
}

// firstBeat is how long the loop waits before its first heartbeat. The
// lease was written when it was taken - by Connect, which may have been a
// while ago by the time the server around it is built and Start runs - and
// it is that write the heartbeat has to follow, not the start of the loop.
func (m *Mirror) firstBeat() time.Duration {
	lease, held, _, _ := m.lease.state()
	if !held || lease.HeartbeatAt == 0 {
		return m.opts.Heartbeat
	}
	since := m.opts.Now().Sub(time.UnixMilli(lease.HeartbeatAt))
	return max(m.opts.Heartbeat-since, 0)
}

// backoff is 1 s, 2 s, 4 s … up to the cap.
func backoff(failures int, limit time.Duration) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// beat renews the lease, or tries to reach the bucket when it has not been
// reached yet.
func (m *Mirror) beat(ctx context.Context) {
	if m.fenced.Load() {
		return
	}
	m.op.Lock()
	defer m.op.Unlock()
	_, held, _, _ := m.lease.state()
	var err error
	if held {
		err = m.lease.renew(ctx)
	} else {
		err = m.reconnect(ctx)
	}
	m.settle(err)
}

// settle records the outcome of a round, fencing on ErrFenced.
func (m *Mirror) settle(err error) {
	switch {
	case err == nil:
		m.noteOK()
	case errors.Is(err, ErrFenced), errors.Is(err, ErrForeignBucket):
		m.setFenced(err)
		m.noteError(err)
	case errors.Is(err, context.Canceled):
	default:
		m.noteError(err)
	}
}

// Sync uploads everything pending now: the admin page's "Sync now", and each
// round of the flusher.
func (m *Mirror) Sync(ctx context.Context) error {
	if m.fenced.Load() {
		return ErrFenced
	}
	m.op.Lock()
	defer m.op.Unlock()
	err := m.syncLocked(ctx)
	m.settle(err)
	if err == nil {
		m.mu.Lock()
		m.lastFlushAt = m.opts.Now().UnixMilli()
		m.mu.Unlock()
	}
	return err
}

func (m *Mirror) syncLocked(ctx context.Context) error {
	if m.fenced.Load() {
		return ErrFenced
	}
	_, held, _, _ := m.lease.state()
	if !held {
		if err := m.reconnect(ctx); err != nil {
			return err
		}
	} else if _, err := m.lease.ensure(ctx, m.minWindow()); err != nil {
		// Even a round with nothing to upload confirms a lease whose
		// window is running out: it is how a takeover is noticed between
		// heartbeats.
		return err
	}
	return m.uploadPending(ctx)
}

// reconnect is a degraded mirror reaching its bucket for the first time in
// this run. The bucket must still be the installation the local copy belongs
// to, and the lease must be free or this machine's own: a lease that names
// anybody else, even an expired one, means somebody else has written here
// since, and uploading this machine's queue over that would undo their work.
// Such a mirror fences itself and the next restart hydrates properly.
func (m *Mirror) reconnect(ctx context.Context) error {
	b := m.bucket()
	meta, err := b.readMeta(ctx)
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return ErrForeignBucket
		}
		return classify(err)
	}
	m.mu.Lock()
	key, want := m.key, m.meta.BucketID
	m.mu.Unlock()
	if meta.BucketID != want {
		return ErrForeignBucket
	}
	if err := meta.verify(key); err != nil {
		return err
	}
	if err := m.probe(ctx); err != nil {
		return classify(err)
	}
	// A fence of an earlier run is remembered by the local copy having no
	// installation any more (setFenced); a degraded start never gets here
	// without one, but the copy may have been fenced since.
	if id, _ := m.st.GetKV(kvBucketID); id != want {
		return ErrForeignBucket
	}
	// A lease that names anybody else - even one released since, which
	// keeps its instance for exactly this - or that cannot be read at all
	// is a sign that this copy is no longer the last word.
	cur, exists, err := m.lease.peek(ctx)
	if err != nil {
		return classify(err)
	}
	if exists && cur.Instance != m.opts.Instance {
		return m.lease.fence(cur)
	}
	if _, err := m.lease.acquire(ctx, false); err != nil {
		var held *LeaseHeldError
		if errors.As(err, &held) {
			return m.lease.fence(held.Lease)
		}
		return classify(err)
	}
	if !m.lease.lastWriterWasUs() {
		// Somebody took and let go of the lease between the look above and
		// the write. The lease is ours now and is left to expire: letting
		// it go would make this copy look like the last writer.
		return m.lease.fence(m.lease.previous())
	}
	m.logf("reached the bucket again; uploading what was written meanwhile")
	return nil
}

// Close stops the loop, uploads what is still pending and lets go of the
// lease, all within ctx: a shutdown waits for the bucket for a bounded time
// and then leaves the rest pending for the next start.
func (m *Mirror) Close(ctx context.Context) error {
	if m.closed.Swap(true) {
		return nil
	}
	m.st.SetObserver(nil)
	if m.stop != nil {
		m.stop()
		<-m.done
	}
	if m.fenced.Load() {
		return nil
	}
	m.op.Lock()
	defer m.op.Unlock()
	_, held, _, _ := m.lease.state()
	if !held {
		return nil
	}
	// The final round is an upload like any other: it may only go to a
	// bucket this Socrates still holds. A takeover that happened since the
	// last check is found here, and then nothing is written at all.
	if err := m.lease.verify(ctx); err != nil {
		m.settle(err)
		if errors.Is(err, ErrFenced) {
			return nil
		}
		return err
	}
	err := m.uploadPending(ctx)
	if err != nil {
		// The lease is kept, and expires by itself after the TTL. Let go
		// now, the bucket would look free to the next machine while this
		// one still has changes it never uploaded; kept, only this data
		// directory takes it at once again (its own lease), and uploads
		// them first.
		m.logf("could not upload everything before shutting down (%v); it stays pending for the next start, "+
			"and the lease is kept until then (or until it expires)", err)
		return err
	}
	if rerr := m.lease.release(ctx); rerr != nil {
		m.logf("could not release the lease: %v", rerr)
		if err == nil {
			err = rerr
		}
	}
	return err
}

// ------------------------------------------------------------- admin bits

// TestResult is the admin card's "Test connection".
type TestResult struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Latency int64  `json:"latency_ms"`
	// Lease is the lease as the bucket has it right now.
	Lease *LeaseStatus `json:"lease,omitempty"`
}

// Test asks the bucket the questions a connection has to answer: can it be
// listed, is meta.json the installation this mirror belongs to, and who holds
// the lease. It writes nothing.
func (m *Mirror) Test(ctx context.Context) TestResult {
	start := time.Now()
	b := m.bucket()
	fail := func(err error) TestResult {
		return TestResult{Error: Explain(err), Latency: time.Since(start).Milliseconds()}
	}
	if err := b.ping(ctx); err != nil {
		return fail(err)
	}
	meta, err := b.readMeta(ctx)
	if err != nil {
		return fail(err)
	}
	m.mu.Lock()
	want, key := m.meta.BucketID, m.key
	m.mu.Unlock()
	if meta.BucketID != want {
		return fail(ErrForeignBucket)
	}
	if err := meta.verify(key); err != nil {
		return fail(err)
	}
	res := TestResult{OK: true, Latency: time.Since(start).Milliseconds()}
	if cur, exists, err := m.lease.peek(ctx); err == nil && exists {
		res.Lease = leaseStatus(cur, cur.Instance == m.opts.Instance)
	}
	return res
}

// Credentials are what may change about a connection without it becoming
// a different one: the endpoint, the region, the key pair, the addressing.
// The bucket, the prefix and the passphrase are the installation itself.
type Credentials struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PathStyle       bool   `json:"path_style"`
}

// ErrEnvConfig: the configuration comes from the environment, which the
// next start would read again whatever was saved.
var ErrEnvConfig = errors.New("the storage is configured by SOCRATES_S3_* environment variables; change them there")

// UpdateCredentials switches the running mirror to new credentials for the
// same bucket and prefix. They are proved first - meta.json must be read with
// them, be this installation and open with this mirror's key - and only then
// saved to s3.json. An empty secret keeps the current one.
func (m *Mirror) UpdateCredentials(ctx context.Context, c Credentials) error {
	if m.opts.Source == SourceEnv {
		return ErrEnvConfig
	}
	m.mu.Lock()
	next := m.cfg
	want, key := m.meta.BucketID, m.key
	m.mu.Unlock()
	next.Endpoint, next.Region, next.PathStyle = c.Endpoint, c.Region, c.PathStyle
	next.AccessKeyID = c.AccessKeyID
	if strings.TrimSpace(c.SecretAccessKey) != "" {
		next.SecretAccessKey = c.SecretAccessKey
	}
	next.Normalize()
	if err := next.Validate(); err != nil {
		return err
	}
	sc := next.S3()
	sc.HTTPClient = m.opts.HTTPClient
	client, err := s3.New(sc)
	if err != nil {
		return err
	}
	b := bucket{c: client, prefix: next.Prefix, timeout: m.opts.OpTimeout}
	meta, err := b.readMeta(ctx)
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return fmt.Errorf("there is no Socrates state at %s with these settings", next.Bucket)
		}
		return err
	}
	if meta.BucketID != want {
		return ErrForeignBucket
	}
	if err := meta.verify(key); err != nil {
		return err
	}
	if m.opts.DataDir == "" {
		return errors.New("this mirror does not know where its configuration file is")
	}
	if err := SaveConfig(m.opts.DataDir, next); err != nil {
		return err
	}
	m.op.Lock()
	m.mu.Lock()
	m.cfg, m.client = next, client
	m.mu.Unlock()
	m.op.Unlock()
	if err := m.st.SetKV(kvIdentity, next.Identity()); err != nil {
		return err
	}
	m.logf("credentials changed (endpoint %s)", client.Endpoint())
	m.notify()
	return nil
}
