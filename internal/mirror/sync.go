package mirror

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// parallel runs fn over items with at most n at a time and returns the first
// error. The rest of the items still run: an upload round that stopped at
// the first failure would leave the objects after it waiting a whole backoff
// for nothing.
func parallel[T any](ctx context.Context, n int, items []T, fn func(context.Context, T) error) error {
	if len(items) == 0 {
		return nil
	}
	var (
		mu    sync.Mutex
		first error
		wg    sync.WaitGroup
	)
	queue := make(chan T)
	for i := 0; i < min(n, len(items)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				if err := fn(ctx, item); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		queue <- item
	}
	close(queue)
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return first
}

// minWindow is how much of a lease confirmation's window an upload wants left
// before it starts: half. Less than that and the lease is confirmed again
// first, so no write has to be cut short by the end of the window.
func (m *Mirror) minWindow() time.Duration { return m.opts.VerifyEvery / 2 }

// leaseError reports an error that says the lease cannot be relied on for
// the next write: taken over, expired, or not held. An upload round stops
// dispatching at the first one; everything not yet written stays pending.
func leaseError(err error) bool {
	return errors.Is(err, ErrFenced) || errors.Is(err, errLeaseExpired) || errors.Is(err, errNotHeld)
}

// uploadPending uploads the current local value of every pending object, or
// deletes the object when its row is gone, and clears each pending mark once
// its object is in the bucket - unless the object changed again meanwhile, in
// which case the mark stays for the next round. The caller holds the lease
// (and the operation lock).
//
// Every write is bounded by the lease (lease.go): it starts only with at
// least minWindow of the last confirmation left - confirming again when
// there is not - and it is cut off when the window ends, so nothing this
// Socrates writes can land after the point a new holder has waited for.
func (m *Mirror) uploadPending(ctx context.Context) error {
	pending, err := m.st.ListPending()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	b := m.bucket()
	m.mu.Lock()
	key := m.key
	m.mu.Unlock()
	var halt atomic.Bool
	err = parallel(ctx, m.opts.Workers, pending, func(ctx context.Context, p store.Pending) error {
		if halt.Load() {
			return nil
		}
		kind, name, perr := store.ParseObject(p.Object)
		if perr != nil || (kind == store.ObjectKV && !store.MirroredKV(name)) {
			// Nothing this build can upload. Keeping the mark would retry
			// it for ever and hold the whole queue in "pending".
			m.logf("dropping %q from the upload queue: not an object this Socrates stores", p.Object)
			_, err := m.st.ClearPending(p.Object, p.At)
			return err
		}
		data, ok, err := m.st.ExportObject(p.Object)
		if err != nil {
			return fmt.Errorf("read %s: %w", p.Object, err)
		}
		var sealed []byte
		if ok {
			if sealed, err = vault.Seal(key, p.Object, data); err != nil {
				return err
			}
		}
		left, err := m.lease.ensure(ctx, m.minWindow())
		if err != nil {
			if leaseError(err) {
				halt.Store(true)
			}
			return classify(err)
		}
		wctx, cancel := context.WithTimeout(ctx, left)
		defer cancel()
		if ok {
			_, err = b.put(wctx, p.Object, sealed, nil)
		} else {
			err = b.delete(wctx, p.Object)
		}
		if err != nil {
			return classify(err)
		}
		m.wrote.Store(true)
		_, err = m.st.ClearPending(p.Object, p.At)
		return err
	})
	return err
}

// keepAlive renews the lease every heartbeat while the startup work of
// Connect and Attach runs - a hydrate of a large installation, or the first
// upload of all of it, can take longer than the lease lives, and the flusher
// that renews it normally only starts once the server is built. Without it a
// second container would find the lease expired half way through and start
// from a half uploaded bucket.
//
// The returned context ends when the lease is lost, so the work stops at
// once; stop ends the renewals and reports ErrFenced when that is what
// happened.
func (m *Mirror) keepAlive(ctx context.Context) (context.Context, func() error) {
	hctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(m.opts.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				if err := m.lease.renew(hctx); errors.Is(err, ErrFenced) {
					cancel(ErrFenced)
					return
				}
				// Anything else is a request that failed; the lease is
				// still ours for as long as the TTL says, and the writes
				// themselves stop when their confirmation runs out.
			}
		}
	}()
	return hctx, func() error {
		cause := context.Cause(hctx)
		cancel(nil)
		<-done
		if errors.Is(cause, ErrFenced) {
			return ErrFenced
		}
		return nil
	}
}

// hydrate makes the local copy and the bucket agree at startup, with the
// lease held. Which way the state flows depends on what the local copy was
// last synchronised with (kvBucketID) and on who held the bucket since. (The
// connect page's explicit "overwrite the bucket" is pushAll, run by Attach
// before the restart.)
//
//   - the same installation, and nobody else has held the lease since this
//     data directory did: what was written here and not uploaded is newer
//     than the bucket and goes up first; then everything comes down, so the
//     local copy is exactly the bucket.
//   - a bucket without state, whatever the local copy belongs to: this is
//     the first connection of an existing installation (the upgrade of a
//     machine that ran without a bucket). Everything goes up.
//   - another installation, or none, or the same one written by another
//     Socrates since - a crash here and a start elsewhere, a takeover, a
//     rollback to an old volume: the bucket wins. What this machine had
//     and never uploaded to it is dropped, because it is older than what
//     the other Socrates wrote.
func (m *Mirror) hydrate(ctx context.Context) error {
	local, err := m.st.GetKV(kvBucketID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	same := local == m.meta.BucketID
	if same && !m.lease.lastWriterWasUs() {
		prev := m.lease.previous()
		m.logf("another Socrates (%s) has held this bucket since this machine last wrote to it: "+
			"its state wins, and what was not uploaded from here is dropped", describeLease(prev))
		same = false
	}
	if same {
		// Up first, and listed afterwards: a row created here and never
		// uploaded is not in a listing taken before its upload, and the
		// download would take its absence for a delete.
		if err := m.uploadPending(ctx); err != nil {
			return err
		}
	}
	remote, err := m.bucket().listObjects(ctx)
	if err != nil {
		return classify(err)
	}
	switch {
	case len(remote) == 0:
		// Never the other way round: a bucket with no state does not
		// empty a local copy that has some, whoever that copy belongs to.
		m.logf("the bucket holds no state yet: uploading this installation")
		// The marks before the id: a crash between the two must leave a
		// copy that still thinks it belongs elsewhere, never one that
		// thinks it is in sync with a bucket it never uploaded to - that
		// one would be overwritten by the empty bucket on the next start.
		if err := m.markAll(); err != nil {
			return err
		}
		if err := m.remember(); err != nil {
			return err
		}
		return m.uploadPending(ctx)
	case same:
		bodies, err := m.fetch(ctx, remote)
		if err != nil {
			return err
		}
		if err := m.apply(remote, bodies, true); err != nil {
			return err
		}
		return m.remember()
	default:
		m.logf("restoring this installation from the bucket (%d objects)", len(remote))
		// Everything is fetched and opened before anything local is
		// touched: an object that cannot be opened - a wrong passphrase
		// for this one, a damaged object - must leave the local rows and
		// their pending marks exactly as they were.
		bodies, err := m.fetch(ctx, remote)
		if err != nil {
			return err
		}
		pending, err := m.st.ListPending()
		if err != nil {
			return err
		}
		for _, p := range pending {
			if _, err := m.st.ClearPending(p.Object, p.At); err != nil {
				return err
			}
		}
		if err := m.apply(remote, bodies, false); err != nil {
			return err
		}
		return m.remember()
	}
}

// describeLease names the holder of a lease for the log.
func describeLease(l Lease) string {
	switch {
	case l.Host != "":
		return l.Host
	case l.Instance != "":
		return "instance " + l.Instance
	}
	return "unknown"
}

// markAll marks every local object as pending.
func (m *Mirror) markAll() error {
	all, err := m.st.AllObjects()
	if err != nil {
		return err
	}
	m.st.MarkPending(all...)
	return nil
}

// remember records that the local copy is now in step with this bucket.
func (m *Mirror) remember() error {
	m.mu.Lock()
	id, identity := m.meta.BucketID, m.cfg.Identity()
	m.mu.Unlock()
	if err := m.st.SetKV(kvBucketID, id); err != nil {
		return err
	}
	return m.st.SetKV(kvIdentity, identity)
}

// pushAll makes the bucket hold exactly this installation. The order is the
// safe one for a crash at any point: mark and remember first, so that the
// next start uploads everything rather than restoring a half emptied bucket
// over the local copy; delete the bucket's extra objects next; upload last.
func (m *Mirror) pushAll(ctx context.Context, remote map[string]s3.ObjectInfo) error {
	m.logf("overwriting the bucket with this installation")
	if err := m.markAll(); err != nil {
		return err
	}
	if err := m.remember(); err != nil {
		return err
	}
	local, err := m.st.AllObjects()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, name := range local {
		have[name] = true
	}
	var extra []string
	for name := range remote {
		if !have[name] {
			extra = append(extra, name)
		}
	}
	b := m.bucket()
	if err := parallel(ctx, m.opts.Workers, extra, func(ctx context.Context, name string) error {
		m.wrote.Store(true)
		return classify(b.delete(ctx, name))
	}); err != nil {
		return err
	}
	return m.uploadPending(ctx)
}

// fetch downloads and opens every object of a listing. It changes nothing
// locally: it is the half of a download that can fail for reasons that say
// the bucket is not what this machine can use (an object sealed with another
// key, a damaged one), and it runs to the end before anything is replaced.
func (m *Mirror) fetch(ctx context.Context, remote map[string]s3.ObjectInfo) (map[string][]byte, error) {
	names := make([]string, 0, len(remote))
	for name := range remote {
		names = append(names, name)
	}
	b := m.bucket()
	m.mu.Lock()
	key := m.key
	m.mu.Unlock()
	var mu sync.Mutex
	bodies := map[string][]byte{}
	err := parallel(ctx, 2*m.opts.Workers, names, func(ctx context.Context, name string) error {
		obj, err := b.get(ctx, name)
		if errors.Is(err, s3.ErrNotFound) {
			return nil // deleted since the listing
		}
		if err != nil {
			return classify(err)
		}
		plain, err := vault.Open(key, name, obj.Body)
		if err != nil {
			return fmt.Errorf("%s cannot be opened with this passphrase (%w); it was altered, or written by another installation", name, err)
		}
		mu.Lock()
		bodies[name] = plain
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bodies, nil
}

// ErrNoPassword: the bucket holds sessions or sign-ins but no Socrates
// password. No installation gets there by itself - the password comes before
// the first session - so the object was deleted, and starting from such a
// bucket would open the first-run setup page to whoever asks first.
var ErrNoPassword = errors.New("the bucket holds sessions but no Socrates password (kv/" + PasswordKey +
	"); it was altered. Restore that object, or connect this machine to another place")

// apply makes the local copy equal to what fetch brought: rows the bucket
// does not have are deleted first (a row that is gone can hold a tmux name or
// a client key that a row coming down now holds, and the unique indexes
// refuse two), then every object is imported.
//
// same says the local copy belongs to this installation, and this data
// directory wrote to it last. Then an object that is pending again - written
// here after the upload that preceded this - is newer than the bucket and is
// left alone.
//
// The Socrates password is never taken away by a download. It is only ever
// replaced, so a bucket without one that has state is a bucket somebody
// tampered with; deleting the local hash would put the server into first-run
// setup, where the first visitor picks the password. A local copy of this
// installation keeps its hash and puts it back into the bucket; any other
// start from such a bucket is refused (ErrNoPassword).
func (m *Mirror) apply(remote map[string]s3.ObjectInfo, bodies map[string][]byte, same bool) error {
	present := map[string]bool{}
	names := make([]string, 0, len(remote))
	for name := range remote {
		present[name] = true
		names = append(names, name)
	}
	sort.Strings(names)

	pw := store.KVObject(PasswordKey)
	if _, ok := bodies[pw]; !ok {
		hash, err := m.st.GetKV(PasswordKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		switch {
		case same && strings.TrimSpace(hash) != "":
			m.logf("the bucket has no %s, but this installation has a password: keeping it and uploading it again", pw)
			present[pw] = true
			m.st.MarkPending(pw)
		case bucketHasState(remote):
			return ErrNoPassword
		}
	}

	deleted, err := m.st.DeleteLocalAbsent(present)
	if err != nil {
		return err
	}
	if len(deleted) > 0 {
		m.logf("removed %d local rows the bucket does not have", len(deleted))
	}
	if !present[store.LoginsObject] && !same {
		// The sign-ins of another installation are not sign-ins of this
		// one.
		if err := m.st.DeleteObject(store.LoginsObject); err != nil {
			return err
		}
	}

	failed := 0
	for _, name := range names {
		plain, ok := bodies[name]
		if !ok {
			continue
		}
		if same && m.st.IsPending(name) {
			continue
		}
		if err := m.st.ImportObject(name, plain); err != nil {
			// One unreadable row must not keep the server from starting
			// with all the others; it is said, and the row stays as the
			// bucket has it for the next start to try again.
			failed++
			m.logf("could not restore %s: %v", name, err)
		}
	}
	if failed > 0 {
		m.logf("%d of %d objects could not be restored", failed, len(names))
	}
	return nil
}

// bucketHasState reports whether a listing holds anything that only exists
// once somebody has signed in: a session, a draft, the sign-ins.
func bucketHasState(remote map[string]s3.ObjectInfo) bool {
	for name := range remote {
		kind, _, err := store.ParseObject(name)
		if err != nil {
			continue
		}
		if kind == store.ObjectSession || kind == store.ObjectDraft || name == store.LoginsObject {
			return true
		}
	}
	return false
}
