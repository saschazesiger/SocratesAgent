package backup

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The objects of a backup, under its prefix:
//
//	meta.json                         plaintext: format, KDF, salt, check value
//	chunks/<name>                     sealed: up to ChunkSize bytes of one file
//	snapshots/<host>/<time>.json      sealed: one snapshot's manifest
//
// meta.json is the only plaintext object. It is what makes the key in the
// first place, and its check value is what tells a wrong passphrase apart
// before anything is read or written.
//
// A chunk's name is HMAC-SHA256 of its plaintext under a key derived from the
// passphrase, not a bare hash: equal content is stored once, but the bucket's
// owner cannot tell which well-known file a chunk is by hashing it. Its name
// is also what a restore checks the plaintext against, on top of the GCM tag.
const (
	metaObject  = "meta.json"
	chunkDir    = "chunks/"
	snapshotDir = "snapshots/"

	metaFormat = 1
	metaKind   = "socrates-backup"

	// The labels the two keys are derived from the stretched passphrase
	// under (vault.Subkey).
	sealLabel = "socrates backup v1: seal"
	nameLabel = "socrates backup v1: chunk names"

	// snapshotTime is how a snapshot's moment is spelled in its object name:
	// UTC, sortable, to the millisecond.
	snapshotTime = "20060102T150405.000Z"
)

// The two encodings of a chunk's or a manifest's plaintext inside its seal.
const (
	encodingRaw     byte = 0
	encodingDeflate byte = 1
)

// maxSealed bounds what one object of the backup may make a reader take in:
// a chunk and its envelope, with room to spare. A manifest is bounded by
// maxManifest.
const (
	maxSealed   = ChunkSize + 1<<16
	maxManifest = 256 << 20
)

var (
	// ErrWrongPassphrase is a passphrase that does not open the backup at
	// this place.
	ErrWrongPassphrase = errors.New("the passphrase does not open the backup in this bucket")
	// ErrNoBackup is a place with no backup at all.
	ErrNoBackup = errors.New("there is no backup at this place")
	// ErrMetaMissing is a place that holds a backup's objects but not its
	// meta.json. A new meta.json there would make those objects unreadable
	// for good, so nothing is written until it is put back or the prefix is
	// emptied.
	ErrMetaMissing = errors.New("this place holds backup objects but no meta.json; put it back, or choose another prefix")
	// ErrCorrupt is an object that opened but is not what its name says.
	ErrCorrupt = errors.New("a backup object is damaged")
)

// meta is meta.json.
type meta struct {
	Format     int    `json:"format"`
	Kind       string `json:"kind"`
	CreatedAt  int64  `json:"created_at"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Check      string `json:"check"`
}

// repo is one backup: a bucket, a prefix, and the two keys its meta.json
// and the passphrase make.
type repo struct {
	c      *s3.Client
	prefix string
	salt   string
	seal   *vault.Key
	names  []byte
}

// opTimeout bounds one request for a small object, chunkTimeout one for a
// chunk, and listTimeout a whole listing.
const (
	opTimeout    = time.Minute
	chunkTimeout = 5 * time.Minute
	listTimeout  = 10 * time.Minute
)

// openRepo opens the backup at cfg's place and proves the passphrase. With
// create, a place without a backup gets a new meta.json; without it, such a
// place is ErrNoBackup. iterations is the PBKDF2 count of a new meta.json
// (zero is vault.Iterations); an existing one says its own.
func openRepo(ctx context.Context, cfg Config, create bool, iterations int) (*repo, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c, err := s3.New(cfg.S3())
	if err != nil {
		return nil, err
	}
	r := &repo{c: c, prefix: NormalizePrefix(cfg.Prefix)}
	m, err := r.readMeta(ctx)
	switch {
	case errors.Is(err, s3.ErrNotFound) && !create:
		if found, lerr := r.hasObjects(ctx); lerr != nil {
			return nil, lerr
		} else if found {
			return nil, ErrMetaMissing
		}
		return nil, ErrNoBackup
	case errors.Is(err, s3.ErrNotFound):
		m, err = r.createMeta(ctx, cfg.Passphrase, iterations)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	if err := r.unlock(m, cfg.Passphrase); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *repo) key(name string) string { return r.prefix + name }

func (r *repo) readMeta(ctx context.Context) (meta, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	obj, err := r.c.Get(ctx, r.key(metaObject))
	if err != nil {
		return meta{}, err
	}
	var m meta
	if err := json.Unmarshal(obj.Body, &m); err != nil {
		return meta{}, fmt.Errorf("meta.json is not valid JSON: %w", err)
	}
	switch {
	case m.Kind != metaKind:
		return meta{}, fmt.Errorf("meta.json at %s is not a Socrates backup's", r.prefix)
	case m.Format != metaFormat:
		return meta{}, fmt.Errorf("meta.json has format %d, and this Socrates only knows format %d", m.Format, metaFormat)
	case m.KDF != vault.KDF:
		return meta{}, fmt.Errorf("meta.json derives its key with %q, which this Socrates does not know", m.KDF)
	}
	return m, nil
}

// hasObjects reports whether anything of a backup is at this place besides
// meta.json.
func (r *repo) hasObjects(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	for _, dir := range []string{chunkDir, snapshotDir} {
		page, err := r.c.ListPage(ctx, r.key(dir), "", 1)
		if err != nil {
			return false, err
		}
		if len(page.Objects) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// createMeta writes the meta.json of a new backup. It is written only if
// there is still none, so two machines starting one backup at the same moment
// agree on one salt; a store that does not enforce the condition simply
// overwrites, which is why what is there is read back.
func (r *repo) createMeta(ctx context.Context, passphrase string, iterations int) (meta, error) {
	if found, err := r.hasObjects(ctx); err != nil {
		return meta{}, err
	} else if found {
		return meta{}, ErrMetaMissing
	}
	if iterations == 0 {
		iterations = vault.Iterations
	}
	salt, err := vault.NewSalt()
	if err != nil {
		return meta{}, err
	}
	seal, _, err := deriveKeys(passphrase, salt, iterations)
	if err != nil {
		return meta{}, err
	}
	check, err := vault.MakeCheck(seal)
	if err != nil {
		return meta{}, err
	}
	m := meta{
		Format: metaFormat, Kind: metaKind, CreatedAt: time.Now().UnixMilli(),
		KDF: vault.KDF, Iterations: iterations,
		Salt:  base64.StdEncoding.EncodeToString(salt),
		Check: base64.StdEncoding.EncodeToString(check),
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return meta{}, err
	}
	putCtx, cancel := context.WithTimeout(ctx, opTimeout)
	_, err = r.c.Put(putCtx, r.key(metaObject), raw, &s3.PutOptions{IfNoneMatch: "*", ContentType: "application/json"})
	cancel()
	if err != nil && !errors.Is(err, s3.ErrPreconditionFailed) {
		return meta{}, err
	}
	return r.readMeta(ctx)
}

// deriveKeys stretches the passphrase and derives the sealing key and the
// key chunk names are made with.
func deriveKeys(passphrase string, salt []byte, iterations int) (*vault.Key, []byte, error) {
	secret, err := vault.DeriveSecret(passphrase, salt, iterations)
	if err != nil {
		return nil, nil, err
	}
	seal, err := vault.NewKey(vault.Subkey(secret, sealLabel))
	if err != nil {
		return nil, nil, err
	}
	return seal, vault.Subkey(secret, nameLabel), nil
}

// unlock derives the keys of m from the passphrase and proves them against
// its check value.
func (r *repo) unlock(m meta, passphrase string) error {
	salt, err := base64.StdEncoding.DecodeString(m.Salt)
	if err != nil {
		return fmt.Errorf("meta.json has an unreadable salt: %w", err)
	}
	check, err := base64.StdEncoding.DecodeString(m.Check)
	if err != nil {
		return fmt.Errorf("meta.json has an unreadable check value: %w", err)
	}
	seal, names, err := deriveKeys(passphrase, salt, m.Iterations)
	if err != nil {
		return err
	}
	if err := vault.VerifyCheck(seal, check); err != nil {
		if errors.Is(err, vault.ErrWrongPassphrase) {
			return ErrWrongPassphrase
		}
		return err
	}
	r.salt, r.seal, r.names = m.Salt, seal, names
	return nil
}

// chunkName is the name of a chunk with this plaintext.
func (r *repo) chunkName(plain []byte) string {
	mac := hmac.New(sha256.New, r.names)
	mac.Write(plain)
	return hex.EncodeToString(mac.Sum(nil))
}

// encode is a plaintext as it goes into a seal: deflated when that is
// smaller, raw when it is not (a chunk of a compressed file).
func encode(plain []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(encodingDeflate)
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	if _, err := w.Write(plain); err == nil && w.Close() == nil && buf.Len() < len(plain)+1 {
		return buf.Bytes()
	}
	out := make([]byte, 1+len(plain))
	out[0] = encodingRaw
	copy(out[1:], plain)
	return out
}

// decode reverses encode, reading at most limit bytes of plaintext.
func decode(payload []byte, limit int64) ([]byte, error) {
	if len(payload) == 0 {
		return nil, ErrCorrupt
	}
	switch payload[0] {
	case encodingRaw:
		return payload[1:], nil
	case encodingDeflate:
		plain, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(payload[1:])), limit+1))
		if err != nil || int64(len(plain)) > limit {
			return nil, ErrCorrupt
		}
		return plain, nil
	}
	return nil, ErrCorrupt
}

// putChunk uploads one chunk and returns how many bytes went over the wire.
func (r *repo) putChunk(ctx context.Context, name string, plain []byte) (int, error) {
	sealed, err := vault.Seal(r.seal, chunkDir+name, encode(plain))
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, chunkTimeout)
	defer cancel()
	if _, err := r.c.Put(ctx, r.key(chunkDir+name), sealed, nil); err != nil {
		return 0, err
	}
	return len(sealed), nil
}

// getChunk downloads one chunk and proves it is the plaintext its name says:
// the seal authenticates it as written by this backup's key under this name,
// and the name is checked against the HMAC of what came out.
func (r *repo) getChunk(ctx context.Context, name string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, chunkTimeout)
	defer cancel()
	body, _, err := r.c.GetStream(ctx, r.key(chunkDir+name))
	if err != nil {
		return nil, err
	}
	defer body.Close()
	sealed, err := io.ReadAll(io.LimitReader(body, maxSealed+1))
	if err != nil {
		return nil, fmt.Errorf("chunk %s: %w", short(name), err)
	}
	if len(sealed) > maxSealed {
		return nil, fmt.Errorf("chunk %s: %w (too large)", short(name), ErrCorrupt)
	}
	payload, err := vault.Open(r.seal, chunkDir+name, sealed)
	if err != nil {
		return nil, fmt.Errorf("chunk %s: %w (%v)", short(name), ErrCorrupt, err)
	}
	plain, err := decode(payload, ChunkSize)
	if err != nil {
		return nil, fmt.Errorf("chunk %s: %w", short(name), err)
	}
	if !hmac.Equal([]byte(r.chunkName(plain)), []byte(name)) {
		return nil, fmt.Errorf("chunk %s: %w (its content does not match its name)", short(name), ErrCorrupt)
	}
	return plain, nil
}

func short(name string) string {
	if len(name) > 12 {
		return name[:12]
	}
	return name
}

// snapshotKey is the object name of a snapshot, relative to the prefix.
func snapshotKey(id string) string { return snapshotDir + id + ".json" }

// snapshotID is a snapshot's id: its host and its moment.
func snapshotID(host string, at time.Time) string {
	return host + "/" + at.UTC().Format(snapshotTime)
}

// parseSnapshotID splits an id into its host and its moment.
func parseSnapshotID(id string) (string, time.Time, error) {
	host, stamp, ok := strings.Cut(id, "/")
	if !ok || !validHostID.MatchString(host) {
		return "", time.Time{}, fmt.Errorf("%q is not a snapshot id", id)
	}
	at, err := time.Parse(snapshotTime, stamp)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%q is not a snapshot id", id)
	}
	return host, at, nil
}

// putManifest seals and uploads a snapshot's manifest.
func (r *repo) putManifest(ctx context.Context, m *Manifest) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	name := snapshotKey(m.ID)
	sealed, err := vault.Seal(r.seal, name, encode(plain))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, chunkTimeout)
	defer cancel()
	_, err = r.c.Put(ctx, r.key(name), sealed, &s3.PutOptions{ContentType: "application/octet-stream"})
	return err
}

// getManifest downloads and opens a snapshot's manifest.
func (r *repo) getManifest(ctx context.Context, id string) (*Manifest, error) {
	name := snapshotKey(id)
	ctx, cancel := context.WithTimeout(ctx, chunkTimeout)
	defer cancel()
	body, _, err := r.c.GetStream(ctx, r.key(name))
	if err != nil {
		return nil, err
	}
	defer body.Close()
	sealed, err := io.ReadAll(io.LimitReader(body, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(sealed) > maxManifest {
		return nil, fmt.Errorf("snapshot %s: %w (too large)", id, ErrCorrupt)
	}
	payload, err := vault.Open(r.seal, name, sealed)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w (%v)", id, ErrCorrupt, err)
	}
	plain, err := decode(payload, maxManifest)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w (%v)", id, ErrCorrupt, err)
	}
	if m.ID != id {
		return nil, fmt.Errorf("snapshot %s: %w (it says it is %s)", id, ErrCorrupt, m.ID)
	}
	return &m, nil
}

// snapRef is a snapshot as the bucket listing shows it.
type snapRef struct {
	ID   string
	Host string
	At   time.Time
}

// listSnapshots lists every snapshot at this place, of every host, oldest
// first. An object under snapshots/ that is not named like one is left out.
func (r *repo) listSnapshots(ctx context.Context) ([]snapRef, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	infos, err := r.c.List(ctx, r.key(snapshotDir))
	if err != nil {
		return nil, err
	}
	out := make([]snapRef, 0, len(infos))
	for _, info := range infos {
		id, ok := strings.CutSuffix(strings.TrimPrefix(info.Key, r.key(snapshotDir)), ".json")
		if !ok {
			continue
		}
		host, at, err := parseSnapshotID(id)
		if err != nil {
			continue
		}
		out = append(out, snapRef{ID: id, Host: host, At: at})
	}
	sortRefs(out)
	return out, nil
}

// listChunks lists every chunk at this place with the moment it was written.
func (r *repo) listChunks(ctx context.Context) (map[string]time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	infos, err := r.c.List(ctx, r.key(chunkDir))
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(infos))
	for _, info := range infos {
		out[strings.TrimPrefix(info.Key, r.key(chunkDir))] = info.LastModified
	}
	return out, nil
}

// remove deletes one object, relative to the prefix.
func (r *repo) remove(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.c.Delete(ctx, r.key(name))
}
