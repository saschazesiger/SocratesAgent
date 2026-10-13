package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestObjectNamesRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		kind ObjectKind
		key  string
	}{
		{KVObject("settings"), ObjectKV, "settings"},
		{KVObject("a/b c:d"), ObjectKV, "a/b c:d"},
		{LoginsObject, ObjectLogins, ""},
		{SessionObject("abc"), ObjectSession, "abc"},
		{DraftObject("abc"), ObjectDraft, "abc"},
	}
	for _, c := range cases {
		kind, key, err := ParseObject(c.name)
		if err != nil || kind != c.kind || key != c.key {
			t.Errorf("ParseObject(%q) = %v %q %v", c.name, kind, key, err)
		}
	}
	// The layout is the bucket's, and a bucket outlives builds: the exact
	// spellings are asserted, not only that they round trip.
	if KVObject("a/b") != "kv/a%2Fb" || SessionObject("x") != "sessions/x.json" || DraftObject("x") != "drafts/x.json" {
		t.Fatal("the object names changed shape")
	}
	for _, bad := range []string{"", "meta.json", "kv/", "sessions/x", "other/x.json"} {
		if kind, _, err := ParseObject(bad); err == nil || kind != ObjectUnknown {
			t.Errorf("ParseObject(%q) accepted it", bad)
		}
	}
}

func TestLocalOnlyKeysStayLocal(t *testing.T) {
	for _, key := range []string{"agents_catalog", "rev_high_water", "resume_note:abc", "mirror.instance", "mirror.bucket_id"} {
		if MirroredKV(key) {
			t.Errorf("%s would go to the bucket", key)
		}
	}
	for _, key := range []string{"settings", "password_hash", "profiles", "profiles.order", "push.vapid", "activity.unread", "something.new"} {
		if !MirroredKV(key) {
			t.Errorf("%s would not go to the bucket", key)
		}
	}
}

// pendingNames is the pending table as a sorted list of names.
func pendingNames(t *testing.T, st *Store) []string {
	t.Helper()
	list, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, p := range list {
		out = append(out, p.Object)
	}
	sort.Strings(out)
	return out
}

// Without a bucket nothing is recorded: the table would only grow.
func TestNothingIsPendingWithoutTracking(t *testing.T) {
	st := openTest(t)
	if err := st.SetKV("settings", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	if got := pendingNames(t, st); len(got) != 0 {
		t.Fatalf("pending without a mirror: %v", got)
	}
	if st.IsBackedUp(SessionObject("a1")) {
		t.Fatal("a row with no mirror reported as backed up")
	}
}

// Every kind of committed write records exactly the objects it changed, and
// the observer hears the same names once the lock is free.
func TestEveryWriteIsRecordedAndObserved(t *testing.T) {
	st := openTest(t)
	st.EnableMirrorTracking()
	var mu sync.Mutex
	var heard []string
	st.SetObserver(func(objects ...string) {
		// The lock is free by now: reading the store back must not hang.
		if _, err := st.ListPending(); err != nil {
			t.Error(err)
		}
		mu.Lock()
		heard = append(heard, objects...)
		mu.Unlock()
	})

	steps := []struct {
		name  string
		write func() error
		want  []string
	}{
		{"kv set", func() error { return st.SetKV("settings", `{"a":1}`) }, []string{"kv/settings"}},
		{"kv local only", func() error { return st.SetKV("resume_note:x", "{}") }, nil},
		{"kv delete", func() error { return st.DeleteKV("settings") }, []string{"kv/settings"}},
		{"kv delete of nothing", func() error { return st.DeleteKV("never-there") }, nil},
		{"login", func() error { return st.CreateLogin("tok", time.Hour) }, []string{LoginsObject}},
		{"logout", func() error { return st.DeleteLogin("tok") }, []string{LoginsObject}},
		{"purge of nothing", func() error { return st.PurgeExpiredLogins() }, nil},
		{"create", func() error { return st.CreateSession(newPrestart("p1")) }, []string{"sessions/p1.json"}},
		{"update", func() error { return st.UpdateSessionTitle("p1", "x") }, []string{"sessions/p1.json"}},
		{"draft", func() error { _, err := st.SaveDraft("p1", "t", 0); return err }, []string{"drafts/p1.json"}},
		{"delete", func() error { return st.DeleteSession("p1") }, []string{"sessions/p1.json", "drafts/p1.json"}},
	}
	for _, step := range steps {
		mu.Lock()
		heard = nil
		mu.Unlock()
		if _, err := st.db.Exec(`DELETE FROM mirror_pending`); err != nil {
			t.Fatal(err)
		}
		if err := step.write(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		want := append([]string{}, step.want...)
		sort.Strings(want)
		if got := pendingNames(t, st); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%s: pending %v, want %v", step.name, got, want)
		}
		mu.Lock()
		got := append([]string{}, heard...)
		mu.Unlock()
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%s: observer heard %v, want %v", step.name, got, want)
		}
	}
}

// ClearPending clears an upload only when nothing changed while it was in
// flight: a change made during the PUT must stay pending.
func TestClearPendingKeepsAChangeMadeDuringTheUpload(t *testing.T) {
	st := openTest(t)
	st.EnableMirrorTracking()
	if err := st.SetKV("settings", "one"); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListPending()
	if len(list) != 1 {
		t.Fatalf("pending = %#v", list)
	}
	uploading := list[0]
	// The flusher has read "one" and is uploading it; meanwhile:
	if err := st.SetKV("settings", "two"); err != nil {
		t.Fatal(err)
	}
	if cleared, err := st.ClearPending(uploading.Object, uploading.At); err != nil || cleared {
		t.Fatalf("an upload of an old value cleared the newer change: %v %v", cleared, err)
	}
	if !st.IsPending("kv/settings") || st.IsBackedUp("kv/settings") {
		t.Fatal("the newer change is no longer pending")
	}
	list, _ = st.ListPending()
	if list[0].At <= uploading.At {
		t.Fatalf("the stamp did not move: %d then %d", uploading.At, list[0].At)
	}
	if cleared, err := st.ClearPending(list[0].Object, list[0].At); err != nil || !cleared {
		t.Fatalf("the current upload did not clear: %v %v", cleared, err)
	}
	if !st.IsBackedUp("kv/settings") {
		t.Fatal("an uploaded object is not backed up")
	}
	if n, _ := st.PendingCount(); n != 0 {
		t.Fatalf("pending count = %d", n)
	}
}

// Writing a key the value it already holds is not a change: nothing is
// queued and nobody is told, so the settings the server writes back on every
// start do not go up to the bucket every time.
func TestRewritingTheSameValueIsNotAChange(t *testing.T) {
	st := openTest(t)
	st.EnableMirrorTracking()
	var seen []string
	st.SetObserver(func(objects ...string) { seen = append(seen, objects...) })
	if err := st.SetKV("settings", "same"); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListPending()
	if len(list) != 1 || len(seen) != 1 {
		t.Fatalf("the first write was not recorded: pending %#v, observed %v", list, seen)
	}
	if cleared, err := st.ClearPending(list[0].Object, list[0].At); err != nil || !cleared {
		t.Fatalf("clear: %v %v", cleared, err)
	}
	if err := st.SetKV("settings", "same"); err != nil {
		t.Fatal(err)
	}
	if st.IsPending("kv/settings") || len(seen) != 1 {
		t.Fatalf("an unchanged value was queued again: pending %v, observed %v", st.IsPending("kv/settings"), seen)
	}
	if v, err := st.GetKV("settings"); err != nil || v != "same" {
		t.Fatalf("value = %q, %v", v, err)
	}
	if err := st.SetKV("settings", "different"); err != nil {
		t.Fatal(err)
	}
	if !st.IsPending("kv/settings") || len(seen) != 2 {
		t.Fatalf("a real change was not queued: pending %v, observed %v", st.IsPending("kv/settings"), seen)
	}
	if v, _ := st.GetKV("settings"); v != "different" {
		t.Fatalf("value = %q", v)
	}
}

// The pending record survives a restart, and the stamps keep rising across it.
func TestPendingSurvivesARestart(t *testing.T) {
	path := t.TempDir() + "/test.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.EnableMirrorTracking()
	if err := st.SetKV("settings", "x"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.ListPending()
	// Pretend the clock ran far ahead in the last run.
	if _, err := st.db.Exec(`UPDATE mirror_pending SET at = ?`, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.EnableMirrorTracking()
	after, _ := st.ListPending()
	if len(before) != 1 || len(after) != 1 || after[0].Object != "kv/settings" {
		t.Fatalf("pending across a restart: %v then %v", before, after)
	}
	if err := st.SetKV("settings", "y"); err != nil {
		t.Fatal(err)
	}
	again, _ := st.ListPending()
	if again[0].At <= after[0].At {
		t.Fatalf("a stamp went backwards across the restart: %d then %d", after[0].At, again[0].At)
	}
}

// What ExportObject writes, ImportObject reads back into an empty database
// unchanged - the hidden fields of a session included, because a row
// restored without its conversation id can never be resumed.
func TestExportImportRoundTrip(t *testing.T) {
	src := openTest(t)
	if err := src.SetKV("settings", `{"voice":{"language":"de"}}`); err != nil {
		t.Fatal(err)
	}
	if err := src.SetKV("password_hash", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := src.SetKV("resume_note:a1", `{"fresh":true}`); err != nil {
		t.Fatal(err)
	}
	if err := src.CreateLogin("tok", time.Hour); err != nil {
		t.Fatal(err)
	}
	row := newSession("a1")
	row.ClientID = "client"
	row.Options = json.RawMessage(`{"profile":"p-1"}`)
	if err := src.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if err := src.SetSessionCLI("a1", "conv-1", CLIVerified); err != nil {
		t.Fatal(err)
	}
	if err := src.NoteResume("a1"); err != nil {
		t.Fatal(err)
	}
	prepared := newPrestart("p1")
	prepared.StartAt = 4242
	if err := src.CreateSession(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := src.SaveDraft("p1", "line one\n`$x` \"quoted\" ünïcode", 0); err != nil {
		t.Fatal(err)
	}

	names, err := src.AllObjects()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"drafts/p1.json", "kv/password_hash", "kv/settings", "logins.json", "sessions/a1.json", "sessions/p1.json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("objects = %v, want %v", names, want)
	}

	dst := openTest(t)
	dst.EnableMirrorTracking()
	observed := 0
	dst.SetObserver(func(objects ...string) { observed += len(objects) })
	revBefore := dst.Rev()
	for _, name := range names {
		data, ok, err := src.ExportObject(name)
		if err != nil || !ok {
			t.Fatalf("export %s: %v %v", name, ok, err)
		}
		if err := dst.ImportObject(name, data); err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
	}
	if observed != 0 {
		t.Fatalf("a download told the observer about %d changes", observed)
	}
	if got := pendingNames(t, dst); len(got) != 0 {
		t.Fatalf("a download is pending upload: %v", got)
	}
	if dst.Rev() <= revBefore {
		t.Fatal("the revision did not move, so no page would reload")
	}

	for _, id := range []string{"a1", "p1"} {
		a, _ := src.GetSession(id)
		b, err := dst.GetSession(id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("session %s changed on the way:\n%#v\n%#v", id, a, b)
		}
	}
	if b, _ := dst.GetSession("a1"); b.CLISessionID != "conv-1" || b.CLISessionState != CLIVerified || b.TmuxName != "soc_a1" {
		t.Fatalf("the hidden fields were lost: %#v", b)
	}
	if v, _ := dst.GetKV("settings"); v != `{"voice":{"language":"de"}}` {
		t.Fatalf("settings = %q", v)
	}
	if !dst.ValidLogin("tok") {
		t.Fatal("the login did not come across")
	}
	a, _ := src.GetDraft("p1")
	b, _ := dst.GetDraft("p1")
	if a != b {
		t.Fatalf("draft %#v became %#v", a, b)
	}

	// A local-only key is neither exported nor accepted.
	if _, _, err := src.ExportObject(KVObject("resume_note:a1")); err == nil {
		t.Fatal("a local-only key was exported")
	}
	if err := dst.ImportObject(KVObject("mirror.bucket_id"), []byte("x")); err == nil {
		t.Fatal("a local-only key was imported")
	}
	// A record under the wrong name is refused.
	data, _, _ := src.ExportObject(SessionObject("a1"))
	if err := dst.ImportObject(SessionObject("other"), data); err == nil {
		t.Fatal("a session record was imported under another id")
	}
}

func TestExportOfAMissingRowSaysSo(t *testing.T) {
	st := openTest(t)
	for _, name := range []string{KVObject("settings"), SessionObject("gone"), DraftObject("gone")} {
		if _, ok, err := st.ExportObject(name); ok || err != nil {
			t.Errorf("export %s = %v %v, want not there", name, ok, err)
		}
	}
	data, ok, err := st.ExportObject(LoginsObject)
	if err != nil || !ok || string(data) != "[]" {
		t.Fatalf("empty logins = %s %v %v", data, ok, err)
	}
}

// DeleteLocalAbsent makes the local rows equal to a bucket listing: what the
// bucket does not have goes, local-only keys and the logins table stay.
func TestDeleteLocalAbsent(t *testing.T) {
	st := openTest(t)
	st.EnableMirrorTracking()
	for _, kv := range [][2]string{{"settings", "{}"}, {"stale", "x"}, {"mirror.instance", "me"}} {
		if err := st.SetKV(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateLogin("tok", time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"keep", "gone"} {
		if err := st.CreateSession(newPrestart(id)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveDraft(id, "t", 0); err != nil {
			t.Fatal(err)
		}
	}
	present := map[string]bool{
		KVObject("settings"): true, SessionObject("keep"): true, DraftObject("keep"): true,
	}
	deleted, err := st.DeleteLocalAbsent(present)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(deleted)
	want := []string{"drafts/gone.json", "kv/stale", "sessions/gone.json"}
	if !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
	if _, err := st.GetSession("gone"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the absent session is still here")
	}
	if v, _ := st.GetKV("mirror.instance"); v != "me" {
		t.Fatal("a local-only key was deleted")
	}
	if !st.ValidLogin("tok") {
		t.Fatal("the logins were deleted")
	}
	for _, name := range want {
		if st.IsPending(name) {
			t.Errorf("%s is pending after being deleted for the bucket's sake", name)
		}
	}
}

func TestMarkPendingRecordsEvenWithoutTracking(t *testing.T) {
	st := openTest(t)
	heard := 0
	st.SetObserver(func(objects ...string) { heard += len(objects) })
	st.MarkPending("kv/settings", LoginsObject)
	if got := pendingNames(t, st); !reflect.DeepEqual(got, []string{"kv/settings", LoginsObject}) {
		t.Fatalf("pending = %v", got)
	}
	if heard != 2 {
		t.Fatalf("the observer heard %d", heard)
	}
}
