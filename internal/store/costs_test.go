package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func sampleLedger() CostLedger {
	return CostLedger{
		CostConversation: CostConversation{
			Harness: "claude", ConvID: "5eb24284-182f", Profile: "p-1", Workdir: "/root/x",
			Title: "Costs", SessionID: "s1", FirstAt: 1000, LastAt: 2000,
		},
		Rows: []CostRow{
			{Hour: 497000, Model: "claude-opus-5-5", CostUSD: 1.25, Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, Requests: 2},
			{Hour: 496999, Model: "claude-haiku-5-5", CostUSD: 0.001, Requests: 1, Unpriced: 1},
		},
	}
}

func TestCostObjectNames(t *testing.T) {
	if got := CostObject(CostKey("claude", "a/b")); got != "costs/claude/a%2Fb.json" {
		t.Fatalf("CostObject = %q", got)
	}
	kind, key, err := ParseObject("costs/claude/a%2Fb.json")
	if err != nil || kind != ObjectCost || key != "claude/a/b" {
		t.Fatalf("ParseObject = %v %q %v", kind, key, err)
	}
	for _, bad := range []string{"costs/", "costs/x.json", "costs/claude/.json", "costs/claude/x"} {
		if kind, _, err := ParseObject(bad); err == nil || kind != ObjectUnknown {
			t.Errorf("ParseObject(%q) accepted it", bad)
		}
	}
}

// A ledger is written once, an unchanged one is not written again, and it
// travels through the bucket's export and import whole.
func TestCostLedgerRoundTrip(t *testing.T) {
	src := openTest(t)
	src.EnableMirrorTracking()
	l := sampleLedger()
	changed, err := src.PutCostLedger(l)
	if err != nil || !changed {
		t.Fatalf("first put: %v %v", changed, err)
	}
	object := CostObject(l.Key())
	if !reflect.DeepEqual(pendingNames(t, src), []string{object}) {
		t.Fatalf("pending = %v", pendingNames(t, src))
	}
	src.ClearPending(object, mustPendingAt(t, src, object))
	if changed, err := src.PutCostLedger(sampleLedger()); err != nil || changed {
		t.Fatalf("an unchanged ledger was written again: %v %v", changed, err)
	}
	if len(pendingNames(t, src)) != 0 {
		t.Fatal("an unchanged ledger is pending upload")
	}

	names, err := src.AllObjects()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names {
		found = found || n == object
	}
	if !found {
		t.Fatalf("%s missing from %v", object, names)
	}

	data, ok, err := src.ExportObject(object)
	if err != nil || !ok {
		t.Fatalf("export: %v %v", ok, err)
	}
	dst := openTest(t)
	dst.EnableMirrorTracking()
	if err := dst.SetCostFingerprint(l.Key(), "old"); err != nil {
		t.Fatal(err)
	}
	if err := dst.ImportObject(object, data); err != nil {
		t.Fatal(err)
	}
	got, err := dst.GetCostLedger(l.Key())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := src.GetCostLedger(l.Key())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imported %+v, want %+v", got, want)
	}
	if fps, _ := dst.CostFingerprints(); fps[l.Key()] != "" {
		t.Fatal("an imported ledger kept the fingerprint of the transcripts on this disk")
	}
	if len(pendingNames(t, dst)) != 0 {
		t.Fatal("a download is pending upload")
	}
	all, err := dst.ListCostLedgers()
	if err != nil || len(all) != 1 || len(all[0].Rows) != 2 {
		t.Fatalf("list = %+v %v", all, err)
	}

	if err := dst.DeleteObject(object); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.GetCostLedger(l.Key()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func mustPendingAt(t *testing.T, st *Store, object string) int64 {
	t.Helper()
	list, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Object == object {
			return p.At
		}
	}
	t.Fatalf("%s is not pending", object)
	return 0
}

// A schema 8 database gains the ledger's tables and keeps its sessions.
func TestMigrationToSchema9(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socrates.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE cost_convs`, `DROP TABLE cost_hours`, `DROP TABLE cost_seen`, `PRAGMA user_version = 8`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("opening a schema 8 database: %v", err)
	}
	defer st.Close()
	if _, err := st.GetSession("a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCostLedger(sampleLedger()); err != nil {
		t.Fatalf("the ledger after the migration: %v", err)
	}
}
