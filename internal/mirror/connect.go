package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// Connect is the startup path of a configured server: read meta.json (create
// it for a place that has none), prove the passphrase, take the lease,
// hydrate, and turn on the store's change tracking. The returned mirror holds
// the lease; Start runs its flusher.
//
// It fails with ErrWrongPassphrase, a *LeaseHeldError, an *UnreachableError
// (see Degraded), ErrMetaMissing, or an answer from the store such as access
// denied.
func Connect(ctx context.Context, st *store.Store, cfg Config, opts Options) (*Mirror, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	m, err := newMirror(st, cfg, opts)
	if err != nil {
		return nil, err
	}
	b := m.bucket()
	meta, err := b.readMeta(ctx)
	var key *vault.Key
	switch {
	case errors.Is(err, s3.ErrNotFound):
		meta, key, err = m.metaForEmptyPlace(ctx)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, classify(err)
	default:
		if key, err = meta.Key(m.cfg.Passphrase); err != nil {
			return nil, err
		}
	}
	m.key, m.meta = key, meta
	if err := m.probe(ctx); err != nil {
		return nil, classify(err)
	}
	took, err := m.lease.acquire(ctx, false)
	if err != nil {
		return nil, classify(err)
	}
	// From here the lease is ours, and kept alive for as long as the
	// startup work takes.
	hctx, stop := m.keepAlive(ctx)
	err = m.settleTakeover(hctx, took)
	if err == nil {
		err = m.hydrate(hctx)
	}
	if fenced := stop(); fenced != nil {
		err = fenced
	}
	if err != nil {
		// The lease goes back: a server that is not going to run must not
		// keep the next one waiting for the TTL. (Both only touch a lease
		// that is still ours.) A start that wrote nothing also gives back the
		// record of who wrote last.
		if m.wrote.Load() {
			_ = m.lease.release(context.WithoutCancel(ctx))
		} else {
			_ = m.lease.giveBack(context.WithoutCancel(ctx))
		}
		return nil, classify(err)
	}
	if err := m.cacheMeta(); err != nil {
		return nil, err
	}
	m.online = true
	m.lastOKAt = m.opts.Now().UnixMilli()
	m.track()
	m.logf("connected to %s (installation %s)", describe(m.cfg), meta.BucketID)
	return m, nil
}

// settleTakeover waits out the window of a holder whose lease was just
// replaced without it letting go - an expired lease taken by a start, or a
// live one taken over on the connect page - so the uploads it may still have
// had in flight are in the bucket before anything is read from it.
func (m *Mirror) settleTakeover(ctx context.Context, took Lease) error {
	if took.Instance == "" {
		return nil
	}
	m.logf("took the lease over from %s, which had not let go of it; waiting %s for its last uploads",
		describeLease(took), m.opts.TakeoverSettle)
	return wait(ctx, m.opts.TakeoverSettle)
}

// metaForEmptyPlace is Connect's answer to a place without meta.json.
//
// A configured place without one is new: the configuration was given on
// purpose (the connect page or the environment), so this is where the
// installation is created - but only when the place holds no Socrates
// objects. Objects without a meta.json are an installation whose meta.json
// went missing (deleted by hand, a lifecycle rule); a new meta.json would
// come with a new salt, and every object there would be sealed with a key
// nobody can derive again. When this data directory has the place's meta.json
// cached (it was synchronised with exactly this place), that is put back -
// same installation, same salt, so the objects open again. Otherwise it is
// ErrMetaMissing, and nothing is written.
func (m *Mirror) metaForEmptyPlace(ctx context.Context) (Meta, *vault.Key, error) {
	b := m.bucket()
	remote, err := b.listObjects(ctx)
	if err != nil {
		return Meta{}, nil, classify(err)
	}
	if len(remote) == 0 {
		m.logf("no Socrates state at %s yet: creating it", describe(m.cfg))
		meta, key, err := b.createMeta(ctx, m.cfg.Passphrase, m.opts.Iterations, m.opts.Now())
		if err != nil {
			return Meta{}, nil, classify(err)
		}
		return meta, key, nil
	}
	cached, ok := cachedMeta(m.st, m.cfg)
	if !ok {
		return Meta{}, nil, ErrMetaMissing
	}
	key, err := cached.Key(m.cfg.Passphrase)
	if err != nil {
		return Meta{}, nil, err
	}
	m.logf("meta.json of %s is missing, but %d objects are there and this machine has its copy: putting it back",
		describe(m.cfg), len(remote))
	meta, err := b.restoreMeta(ctx, cached)
	if err != nil {
		return Meta{}, nil, classify(err)
	}
	if meta.BucketID != cached.BucketID {
		// Somebody else created one in the meantime.
		if key, err = meta.Key(m.cfg.Passphrase); err != nil {
			return Meta{}, nil, err
		}
	}
	return meta, key, nil
}

// cachedMeta is the meta.json this data directory cached for this very place
// (installation, endpoint, bucket and prefix), if it has one.
func cachedMeta(st *store.Store, cfg Config) (Meta, bool) {
	if !CanDegrade(st, cfg) {
		return Meta{}, false
	}
	raw, err := st.GetKV(kvMeta)
	if err != nil {
		return Meta{}, false
	}
	meta, err := parseMeta([]byte(raw))
	return meta, err == nil
}

// track turns on the store's pending record and hooks the flusher to it.
func (m *Mirror) track() {
	m.st.EnableMirrorTracking()
	m.st.SetObserver(m.notify)
}

func (m *Mirror) cacheMeta() error {
	raw, err := json.Marshal(m.meta)
	if err != nil {
		return err
	}
	return m.st.SetKV(kvMeta, string(raw))
}

func describe(cfg Config) string {
	where := cfg.Bucket
	if cfg.Prefix != "" {
		where += "/" + strings.TrimSuffix(cfg.Prefix, "/")
	}
	return where
}

// CanDegrade reports whether a server whose bucket cannot be reached may
// start from its local copy: the copy must have been synchronised with this
// very place before (installation, endpoint, bucket and prefix), and the key
// material to keep sealing uploads for it must be cached. A configuration
// pointed anywhere else gets the connect page and its error instead.
func CanDegrade(st *store.Store, cfg Config) bool {
	cfg.Normalize()
	id, err := st.GetKV(kvBucketID)
	if err != nil || id == "" {
		return false
	}
	identity, err := st.GetKV(kvIdentity)
	if err != nil || identity != cfg.Identity() {
		return false
	}
	raw, err := st.GetKV(kvMeta)
	if err != nil {
		return false
	}
	meta, err := parseMeta([]byte(raw))
	return err == nil && meta.BucketID == id
}

// Degraded starts a mirror from the local copy alone, for a server whose
// bucket was unreachable at startup (cause). Every write is recorded as
// pending as usual; the flusher keeps trying to reach the bucket, and once it
// does - meta.json still this installation, the lease free or this machine's
// own - it takes the lease and uploads the queue. A lease that names anybody
// else by then fences it.
func Degraded(st *store.Store, cfg Config, opts Options, cause error) (*Mirror, error) {
	if !CanDegrade(st, cfg) {
		return nil, errors.New("this data directory was never synchronised with this bucket")
	}
	raw, err := st.GetKV(kvMeta)
	if err != nil {
		return nil, err
	}
	meta, err := parseMeta([]byte(raw))
	if err != nil {
		return nil, err
	}
	m, err := newMirror(st, cfg, opts)
	if err != nil {
		return nil, err
	}
	key, err := meta.Key(m.cfg.Passphrase)
	if err != nil {
		return nil, err
	}
	m.key, m.meta = key, meta
	if cause == nil {
		cause = ErrNotConnected
	}
	m.noteError(cause)
	m.track()
	m.logf("starting DEGRADED from the local copy: %v. Changes are kept here and uploaded once the bucket answers.", cause)
	return m, nil
}

// ----------------------------------------------------------- connect page

// AttachRequest is what the connect page asks for, beyond the configuration.
type AttachRequest struct {
	// ConfirmNew says the person has typed the passphrase twice. Without it
	// a place with no Socrates state answers ErrNewBucket and nothing is
	// created.
	ConfirmNew bool
	// Resolve answers ErrConflict: "bucket" keeps the bucket and discards
	// this machine's state on the next start, "overwrite" replaces the
	// bucket's state with this machine's now.
	Resolve string
	// Password is the Socrates password, which takes over a bucket whose
	// lease another live Socrates holds. VerifyPassword checks it against
	// the hash stored in the bucket.
	Password       string
	VerifyPassword func(hash, password string) bool
}

// Resolutions of a conflict.
const (
	ResolveBucket    = "bucket"
	ResolveOverwrite = "overwrite"
)

// AttachResult says what Attach did.
type AttachResult struct {
	// Created: there was no state at this place, and meta.json now exists.
	Created bool `json:"created"`
	// Overwrote: the bucket's state was replaced with this machine's.
	Overwrote bool `json:"overwrote"`
	// TookOver: the lease of another live Socrates was taken.
	TookOver bool `json:"took_over"`
}

// ConflictError is ErrConflict with what each side holds, so the connect page
// can say what "discard" would discard.
type ConflictError struct {
	BucketSessions int `json:"bucket_sessions"`
	LocalSessions  int `json:"local_sessions"`
}

func (e *ConflictError) Error() string { return ErrConflict.Error() }
func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

// localHasData reports whether this data directory holds an installation
// worth keeping: a password, or a session.
func localHasData(st *store.Store) (bool, int, error) {
	rows, err := st.ListSessions()
	if err != nil {
		return false, 0, err
	}
	hash, err := st.GetKV(PasswordKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, 0, err
	}
	return strings.TrimSpace(hash) != "" || len(rows) > 0, len(rows), nil
}

// Attach is the connect page's commit: it proves a configuration against the
// bucket and prepares the bucket for the restart that follows, which then
// runs Connect like any other start. It does not keep a mirror: the server
// that called it is about to be replaced.
//
//   - The bucket must answer and the passphrase must open its meta.json; a
//     place without one is created, once the person confirmed (ErrNewBucket).
//   - This machine's state and the bucket's must not both exist and differ
//     (a *ConflictError) unless req.Resolve says which wins.
//   - The lease must be free, expired or this machine's own, or be taken
//     over with the Socrates password (a *LeaseHeldError, ErrWrongPassword).
//     A lease another instance held and had not let go of is waited out for
//     TakeoverSettle, so the old holder's last uploads land before anything
//     is read; and when another instance held it at all, the restart is told
//     to take the bucket's state rather than upload what is pending here.
//   - "overwrite" replaces the bucket's state with this machine's here and
//     now, under the lease.
func Attach(ctx context.Context, st *store.Store, cfg Config, opts Options, req AttachRequest) (AttachResult, error) {
	var res AttachResult
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return res, err
	}
	m, err := newMirror(st, cfg, opts)
	if err != nil {
		return res, err
	}
	b := m.bucket()
	if err := b.ping(ctx); err != nil {
		return res, classify(err)
	}
	meta, err := b.readMeta(ctx)
	var remote map[string]s3.ObjectInfo
	switch {
	case errors.Is(err, s3.ErrNotFound):
		// Objects without a meta.json are an installation that lost it, not
		// a new place: a new meta.json (a new salt) would make them
		// unreadable for good. Only an explicit overwrite, which deletes
		// them, may start a new installation over them.
		if remote, err = b.listObjects(ctx); err != nil {
			return res, classify(err)
		}
		if len(remote) > 0 && req.Resolve != ResolveOverwrite {
			return res, ErrMetaMissing
		}
		if !req.ConfirmNew {
			return res, ErrNewBucket
		}
		meta, m.key, err = b.createMeta(ctx, m.cfg.Passphrase, m.opts.Iterations, m.opts.Now())
		if err != nil {
			return res, classify(err)
		}
		res.Created = true
	case err != nil:
		return res, classify(err)
	default:
		if m.key, err = meta.Key(m.cfg.Passphrase); err != nil {
			return res, err
		}
	}
	m.meta = meta

	if remote == nil {
		if remote, err = b.listObjects(ctx); err != nil {
			return res, classify(err)
		}
	}
	hasLocal, localSessions, err := localHasData(st)
	if err != nil {
		return res, err
	}
	localID, _ := st.GetKV(kvBucketID)
	if len(remote) > 0 && hasLocal && localID != meta.BucketID && req.Resolve == "" {
		n := 0
		for name := range remote {
			if kind, _, _ := store.ParseObject(name); kind == store.ObjectSession {
				n++
			}
		}
		return res, &ConflictError{BucketSessions: n, LocalSessions: localSessions}
	}

	if err := m.probe(ctx); err != nil {
		return res, classify(err)
	}
	took, err := m.lease.acquire(ctx, false)
	var held *LeaseHeldError
	if errors.As(err, &held) {
		if req.Password == "" {
			return res, err
		}
		if err := m.checkPassword(ctx, req); err != nil {
			return res, err
		}
		if took, err = m.lease.acquire(ctx, true); err != nil {
			return res, classify(err)
		}
		res.TookOver = true
	} else if err != nil {
		return res, classify(err)
	}
	// The lease is ours from here, kept alive while the rest runs.
	hctx, stop := m.keepAlive(ctx)
	err = m.attachHeld(hctx, req, remote, took, &res)
	if fenced := stop(); fenced != nil {
		err = fenced
	}
	if err != nil {
		return res, err
	}
	// The lease stays: it names this data directory, and the restart that
	// follows takes it again as its own without waiting.
	return res, nil
}

// attachHeld is the part of Attach that runs with the lease held.
func (m *Mirror) attachHeld(ctx context.Context, req AttachRequest, remote map[string]s3.ObjectInfo, took Lease, res *AttachResult) error {
	// A holder that did not let go may still be uploading.
	if err := m.settleTakeover(ctx, took); err != nil {
		return err
	}
	if req.Resolve == ResolveOverwrite {
		if err := m.pushAll(ctx, remote); err != nil {
			return classify(err)
		}
		res.Overwrote = true
	} else if !m.lease.lastWriterWasUs() {
		// Another Socrates held the bucket after this data directory last
		// did, and the restart that follows would find its own lease - the
		// one just written - and take this copy's pending writes for the
		// newest. Forgetting the installation here makes that start take
		// the bucket's state instead, which is what a takeover means.
		if err := m.st.DeleteKV(kvBucketID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return m.cacheMeta()
}

// checkPassword proves a takeover with the Socrates password stored in the
// bucket. A bucket without one - set up but never given a password - has
// nothing to prove against, and anybody who holds its passphrase may take it.
func (m *Mirror) checkPassword(ctx context.Context, req AttachRequest) error {
	if req.VerifyPassword == nil {
		return errors.New("taking a bucket over needs a password check")
	}
	name := store.KVObject(PasswordKey)
	obj, err := m.bucket().get(ctx, name)
	if errors.Is(err, s3.ErrNotFound) {
		return nil
	}
	if err != nil {
		return classify(err)
	}
	hash, err := vault.Open(m.key, name, obj.Body)
	if err != nil {
		return fmt.Errorf("the stored password cannot be opened: %w", err)
	}
	if !req.VerifyPassword(string(hash), req.Password) {
		return ErrWrongPassword
	}
	return nil
}

// Peek reads the lease of a configured place without a key, for the connect
// page of a server that is waiting: it can say who holds the bucket before
// anybody has typed anything.
func Peek(ctx context.Context, cfg Config, opts Options) (Lease, bool, error) {
	opts.defaults()
	cfg.Normalize()
	sc := cfg.S3()
	sc.HTTPClient = opts.HTTPClient
	client, err := s3.New(sc)
	if err != nil {
		return Lease{}, false, err
	}
	l := &leaser{b: func() bucket {
		return bucket{c: client, prefix: cfg.Prefix, timeout: opts.OpTimeout}
	}}
	lease, exists, err := l.peek(ctx)
	return lease, exists, classify(err)
}
