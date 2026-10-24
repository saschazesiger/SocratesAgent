package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

// The objects the bucket mirror of the build before left (REBUILD.md §8.4).
// A backup carried over from the mirror writes under the mirror's prefix and
// DefaultPrefix (MigrateConfig), so the mirror's own objects are still in the
// bucket beside it, untouched: until the backup has a snapshot they are the
// only copy of this machine anywhere else. Once it has one, the Backup card
// offers to delete them - exactly the names the mirror wrote, and never
// anything under backup/, where every backup lives.

// ErrNoSnapshotYet is a delete of the mirror's objects asked for before the
// backup has a snapshot of this machine.
var ErrNoSnapshotYet = errors.New("the backup has no snapshot of this machine yet; the mirror's objects stay until it has one")

// LegacyMirror is what is left of the mirror in the backup's bucket.
type LegacyMirror struct {
	// Prefix is where the mirror wrote: the backup's prefix without
	// DefaultPrefix, "" for the top of the bucket.
	Prefix  string `json:"prefix"`
	Objects int    `json:"objects"`
	Bytes   int64  `json:"bytes"`
	// Snapshot is whether the backup has a snapshot of this machine, which
	// is when deleting the mirror's objects is offered.
	Snapshot bool `json:"snapshot"`
}

// legacyPrefix is where the mirror wrote for a backup at prefix, and false
// for a backup whose prefix was not made from a mirror's (MigrateConfig ends
// every carried-over one with DefaultPrefix).
func legacyPrefix(prefix string) (string, bool) {
	p := NormalizePrefix(prefix)
	inside, ok := strings.CutSuffix(p, DefaultPrefix)
	return inside, ok
}

// legacyObject reports whether rel - a key with the mirror's prefix taken
// off - is a name the mirror wrote:
//
//	meta.json, lease.json, logins.json
//	kv/<key>
//	sessions/<id>.json, drafts/<id>.json
//	costs/<harness>/<conversation>.json
//
// Every name the mirror gave a key, an id or a conversation was escaped, so
// each is one segment. Nothing under backup/ is one of them.
func legacyObject(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	switch len(parts) {
	case 1:
		return rel == "meta.json" || rel == "lease.json" || rel == "logins.json"
	case 2:
		switch parts[0] {
		case "kv":
			return true
		case "sessions", "drafts":
			return strings.HasSuffix(parts[1], ".json")
		}
	case 3:
		return parts[0] == "costs" && strings.HasSuffix(parts[2], ".json")
	}
	return false
}

// LegacyMirror lists what the mirror left in the configured bucket, and
// whether the backup has a snapshot of this machine yet.
func (e *Engine) LegacyMirror(ctx context.Context) (LegacyMirror, error) {
	cfg := e.Config()
	if cfg == nil {
		return LegacyMirror{}, ErrNotConfigured
	}
	prefix, ok := legacyPrefix(cfg.Prefix)
	if !ok {
		return LegacyMirror{}, nil
	}
	objects, err := legacyObjects(ctx, *cfg, prefix)
	if err != nil {
		return LegacyMirror{}, err
	}
	out := LegacyMirror{Prefix: prefix, Objects: len(objects)}
	for _, o := range objects {
		out.Bytes += o.Size
	}
	if len(objects) > 0 {
		if out.Snapshot, err = e.hasOwnSnapshot(ctx, *cfg); err != nil {
			return LegacyMirror{}, err
		}
	}
	return out, nil
}

// DeleteLegacyMirror deletes the mirror's objects, and only once the backup
// has a snapshot of this machine. It answers how many it deleted; one that
// could not be deleted stops it, and what is left is listed again by the
// next LegacyMirror.
func (e *Engine) DeleteLegacyMirror(ctx context.Context) (int, error) {
	cfg := e.Config()
	if cfg == nil {
		return 0, ErrNotConfigured
	}
	prefix, ok := legacyPrefix(cfg.Prefix)
	if !ok {
		return 0, nil
	}
	if own, err := e.hasOwnSnapshot(ctx, *cfg); err != nil {
		return 0, err
	} else if !own {
		return 0, ErrNoSnapshotYet
	}
	objects, err := legacyObjects(ctx, *cfg, prefix)
	if err != nil {
		return 0, err
	}
	c, err := s3.New(cfg.S3())
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, o := range objects {
		opCtx, cancel := context.WithTimeout(ctx, opTimeout)
		err := c.Delete(opCtx, o.Key)
		cancel()
		if err != nil && !errors.Is(err, s3.ErrNotFound) {
			return deleted, fmt.Errorf("%s: %w", o.Key, err)
		}
		deleted++
	}
	return deleted, nil
}

// legacyObjects is every object under prefix that the mirror wrote.
func legacyObjects(ctx context.Context, cfg Config, prefix string) ([]s3.ObjectInfo, error) {
	c, err := s3.New(cfg.S3())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	all, err := c.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []s3.ObjectInfo
	for _, o := range all {
		if rel, ok := strings.CutPrefix(o.Key, prefix); ok && legacyObject(rel) {
			out = append(out, o)
		}
	}
	return out, nil
}

// hasOwnSnapshot reports whether the backup holds a snapshot of this
// machine. A snapshot's object is written last, once everything it refers
// to is in the bucket, and its name says whose it is, so the listing is the
// whole answer: nothing is opened, and a backup not made yet is not started
// by asking.
func (e *Engine) hasOwnSnapshot(ctx context.Context, cfg Config) (bool, error) {
	if cfg.HostID == "" {
		return false, nil
	}
	c, err := s3.New(cfg.S3())
	if err != nil {
		return false, err
	}
	r := &repo{c: c, prefix: NormalizePrefix(cfg.Prefix)}
	refs, err := r.listSnapshots(ctx)
	if err != nil {
		return false, err
	}
	for _, ref := range refs {
		if ref.Host == cfg.HostID {
			return true, nil
		}
	}
	return false, nil
}
