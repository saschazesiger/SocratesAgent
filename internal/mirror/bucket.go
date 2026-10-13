package mirror

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The two plaintext objects. Everything else under the prefix is sealed with
// the vault key and named by internal/store (kv/…, logins.json, sessions/…,
// drafts/…).
const (
	// metaObject says what this place is: which installation (bucket_id),
	// and how its key is derived. It is plaintext because it is what makes
	// the key in the first place; the check value in it is what tells a
	// wrong passphrase apart at once.
	metaObject = "meta.json"
	// leaseObject names the one Socrates that may write here right now. It
	// holds no secret - a host name, a build, two timestamps - so that the
	// connect page of a second machine can say who is using the bucket
	// before it has been given any passphrase.
	leaseObject = "lease.json"
)

// metaFormat is the version of the layout. A bucket written by a later
// format is refused rather than half understood.
const metaFormat = 1

// Meta is meta.json.
type Meta struct {
	Format     int    `json:"format"`
	BucketID   string `json:"bucket_id"`
	CreatedAt  int64  `json:"created_at"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Check      string `json:"check"`
}

// newMeta creates the meta.json of a new place, with a fresh salt and a fresh
// installation id, and the key that goes with it.
func newMeta(passphrase string, iterations int, now time.Time) (Meta, *vault.Key, error) {
	salt, err := vault.NewSalt()
	if err != nil {
		return Meta{}, nil, err
	}
	key, err := vault.DeriveKey(passphrase, salt, iterations)
	if err != nil {
		return Meta{}, nil, err
	}
	check, err := vault.MakeCheck(key)
	if err != nil {
		return Meta{}, nil, err
	}
	return Meta{
		Format:     metaFormat,
		BucketID:   uuid.NewString(),
		CreatedAt:  now.UnixMilli(),
		KDF:        vault.KDF,
		Iterations: iterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Check:      base64.StdEncoding.EncodeToString(check),
	}, key, nil
}

// parseMeta reads meta.json and refuses one this build cannot use.
func parseMeta(raw []byte) (Meta, error) {
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("meta.json is not valid JSON: %w", err)
	}
	switch {
	case m.Format != metaFormat:
		return Meta{}, fmt.Errorf("meta.json has format %d, and this Socrates only knows format %d", m.Format, metaFormat)
	case m.KDF != vault.KDF:
		return Meta{}, fmt.Errorf("meta.json derives its key with %q, which this Socrates does not know", m.KDF)
	case m.BucketID == "":
		return Meta{}, errors.New("meta.json has no bucket_id")
	}
	return m, nil
}

// Key derives the key of this place from a passphrase and proves it against
// the check value. A wrong passphrase is ErrWrongPassphrase.
func (m Meta) Key(passphrase string) (*vault.Key, error) {
	salt, err := base64.StdEncoding.DecodeString(m.Salt)
	if err != nil {
		return nil, fmt.Errorf("meta.json has an unreadable salt: %w", err)
	}
	key, err := vault.DeriveKey(passphrase, salt, m.Iterations)
	if err != nil {
		return nil, err
	}
	if err := m.verify(key); err != nil {
		return nil, err
	}
	return key, nil
}

// verify proves a key against the check value without deriving anything.
func (m Meta) verify(key *vault.Key) error {
	check, err := base64.StdEncoding.DecodeString(m.Check)
	if err != nil {
		return fmt.Errorf("meta.json has an unreadable check value: %w", err)
	}
	if err := vault.VerifyCheck(key, check); err != nil {
		if errors.Is(err, vault.ErrWrongPassphrase) {
			return ErrWrongPassphrase
		}
		return err
	}
	return nil
}

// bucket is one place - a bucket and a prefix - with the requests the mirror
// makes of it. Every request gets its own deadline: the client has none of its
// own, and a store that stops answering must cost a retry, not a goroutine.
type bucket struct {
	c       *s3.Client
	prefix  string
	timeout time.Duration
}

func (b bucket) op(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, b.timeout)
}

func (b bucket) get(ctx context.Context, name string) (s3.Object, error) {
	ctx, cancel := b.op(ctx)
	defer cancel()
	return b.c.Get(ctx, b.prefix+name)
}

func (b bucket) put(ctx context.Context, name string, body []byte, opts *s3.PutOptions) (string, error) {
	ctx, cancel := b.op(ctx)
	defer cancel()
	return b.c.Put(ctx, b.prefix+name, body, opts)
}

func (b bucket) delete(ctx context.Context, name string) error {
	ctx, cancel := b.op(ctx)
	defer cancel()
	return b.c.Delete(ctx, b.prefix+name)
}

func (b bucket) ping(ctx context.Context) error {
	ctx, cancel := b.op(ctx)
	defer cancel()
	return b.c.Ping(ctx, b.prefix)
}

// readMeta reads meta.json; a place without one is s3.ErrNotFound.
func (b bucket) readMeta(ctx context.Context) (Meta, error) {
	obj, err := b.get(ctx, metaObject)
	if err != nil {
		return Meta{}, err
	}
	return parseMeta(obj.Body)
}

// createMeta writes the meta.json of a new place. It is written only if there
// is still none, so two machines connecting to one empty bucket at the same
// moment end up agreeing on one installation: the second is told the first
// was quicker and reads what it wrote. A store that does not enforce the
// condition simply overwrites, which is why the result is read back.
func (b bucket) createMeta(ctx context.Context, passphrase string, iterations int, now time.Time) (Meta, *vault.Key, error) {
	meta, key, err := newMeta(passphrase, iterations, now)
	if err != nil {
		return Meta{}, nil, err
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Meta{}, nil, err
	}
	_, err = b.put(ctx, metaObject, raw, &s3.PutOptions{IfNoneMatch: "*", ContentType: "application/json"})
	if err != nil && !errors.Is(err, s3.ErrPreconditionFailed) {
		return Meta{}, nil, err
	}
	stored, rerr := b.readMeta(ctx)
	if rerr != nil {
		return Meta{}, nil, rerr
	}
	if stored.BucketID == meta.BucketID {
		return meta, key, nil
	}
	// Somebody else created it first. Their passphrase may not be ours, and
	// that is the same answer as for any existing bucket.
	key, err = stored.Key(passphrase)
	if err != nil {
		return Meta{}, nil, err
	}
	return stored, key, nil
}

// listObjects lists every Socrates object at this place, by object name,
// leaving out the two plaintext objects and anything this build does not
// recognise - a bucket shared with other prefixes, or a stray upload, is not
// state to import.
func (b bucket) listObjects(ctx context.Context) (map[string]s3.ObjectInfo, error) {
	// A listing is many requests for a large bucket; it gets a longer budget
	// than one object does.
	ctx, cancel := context.WithTimeout(ctx, 4*b.timeout)
	defer cancel()
	infos, err := b.c.List(ctx, b.prefix)
	if err != nil {
		return nil, err
	}
	out := map[string]s3.ObjectInfo{}
	for _, info := range infos {
		name := strings.TrimPrefix(info.Key, b.prefix)
		if name == metaObject || name == leaseObject {
			continue
		}
		kind, key, err := store.ParseObject(name)
		if err != nil || (kind == store.ObjectKV && !store.MirroredKV(key)) {
			continue
		}
		out[name] = info
	}
	return out, nil
}

// restoreMeta puts back the meta.json of an installation whose objects are
// still there, from a copy a data directory cached. Like createMeta it only
// writes when there is still none, and reads back what is there.
func (b bucket) restoreMeta(ctx context.Context, meta Meta) (Meta, error) {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Meta{}, err
	}
	_, err = b.put(ctx, metaObject, raw, &s3.PutOptions{IfNoneMatch: "*", ContentType: "application/json"})
	if err != nil && !errors.Is(err, s3.ErrPreconditionFailed) {
		return Meta{}, err
	}
	return b.readMeta(ctx)
}
