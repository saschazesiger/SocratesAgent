package backup

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// The bucket mirror's objects, under the prefix the backup's was made from
// (socrates/), and the objects beside them that it did not write.
var (
	mirrorObjects = []string{
		"socrates/meta.json", "socrates/lease.json", "socrates/logins.json",
		"socrates/kv/settings", "socrates/kv/push.subscriptions", "socrates/kv/activity%2Funread",
		"socrates/sessions/s1.json", "socrates/drafts/s1.json", "socrates/costs/claude/conv-1.json",
	}
	notTheMirrors = []string{
		"socrates/notes.txt", "socrates/sessions/s1.txt", "socrates/sessions/a/b.json", "socrates/costs/claude.json",
		"socrates/kv/a/b", "socrates/backup/meta.json", "socrates/backup/kv/settings", "elsewhere/meta.json",
		"meta.json",
	}
)

// The mirror's objects are offered for deletion once the backup has a
// snapshot of this machine, and deleting them takes exactly those: nothing
// the mirror did not write, and nothing of any backup.
func TestTheMirrorsObjectsGoOnceThereIsASnapshot(t *testing.T) {
	clk := newClock()
	b := newBucket(t, clk)
	for _, key := range append(append([]string{}, mirrorObjects...), notTheMirrors...) {
		b.fake.PutObject("backups", key, []byte("old"))
	}
	dir := t.TempDir()
	e := engine(t, dir, b, clk, "vm-1")
	ctx := context.Background()

	got, err := e.LegacyMirror(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prefix != "socrates/" || got.Objects != len(mirrorObjects) || got.Bytes != int64(3*len(mirrorObjects)) || got.Snapshot {
		t.Fatalf("before the first snapshot: %+v", got)
	}
	if _, err := e.DeleteLegacyMirror(ctx); !errors.Is(err, ErrNoSnapshotYet) {
		t.Fatalf("a delete before the first snapshot: %v", err)
	}
	if keys := b.fake.Keys("backups", "socrates/backup/"); len(keys) != 2 {
		t.Fatalf("asking opened a backup: %v", keys)
	}

	populate(t, dir)
	if _, err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := e.LegacyMirror(ctx); err != nil || !got.Snapshot || got.Objects != len(mirrorObjects) {
		t.Fatalf("after the first snapshot: %+v %v", got, err)
	}
	backupKeys := b.fake.Keys("backups", "socrates/backup/v1/")
	n, err := e.DeleteLegacyMirror(ctx)
	if err != nil || n != len(mirrorObjects) {
		t.Fatalf("deleted %d: %v", n, err)
	}
	for _, key := range mirrorObjects {
		if _, ok := b.fake.Object("backups", key); ok {
			t.Fatalf("%s is still there", key)
		}
	}
	for _, key := range append(append([]string{}, notTheMirrors...), backupKeys...) {
		if _, ok := b.fake.Object("backups", key); !ok {
			t.Fatalf("%s was deleted", key)
		}
	}
	if got, err := e.LegacyMirror(ctx); err != nil || got.Objects != 0 {
		t.Fatalf("after the delete: %+v %v", got, err)
	}
	if list, err := e.Snapshots(ctx); err != nil || len(list) != 1 {
		t.Fatalf("the backup after the delete: %v %v", list, err)
	}
}

// A backup whose prefix was not made from a mirror's has no mirror beside it
// to offer.
func TestABackupOfItsOwnHasNoMirror(t *testing.T) {
	for prefix, want := range map[string]bool{
		"socrates/backup/v1/": true, "backup/v1": true, "/backup/v1/": true,
		"elsewhere/": false, "socrates/backup/v2/": false,
	} {
		if _, ok := legacyPrefix(prefix); ok != want {
			t.Errorf("%q: %v", prefix, ok)
		}
	}
	if inside, _ := legacyPrefix("backup/v1/"); inside != "" {
		t.Errorf("the top of the bucket is %q", inside)
	}
	for _, rel := range []string{"meta.json", "kv/x", "sessions/a.json", "drafts/a.json", "costs/codex/c.json"} {
		if !legacyObject(rel) {
			t.Errorf("%s is the mirror's", rel)
		}
	}
	for _, rel := range []string{"backup/v1/meta.json", "kv/", "kv/a/b", "sessions/a", "costs/c.json", "costs/x/y/z.json",
		"chunks/ab", "snapshots/vm-1/x.json", "", "kv/.."} {
		if legacyObject(rel) || slices.Contains(mirrorObjects, rel) {
			t.Errorf("%q is not the mirror's", rel)
		}
	}
}
