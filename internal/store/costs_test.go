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

// A ledger is written once, an unchanged one is not written again, and it
// reads back whole.
func TestCostLedgerRoundTrip(t *testing.T) {
	st := openTest(t)
	l := sampleLedger()
	changed, err := st.PutCostLedger(l)
	if err != nil || !changed {
		t.Fatalf("first put: %v %v", changed, err)
	}
	if changed, err := st.PutCostLedger(sampleLedger()); err != nil || changed {
		t.Fatalf("an unchanged ledger was written again: %v %v", changed, err)
	}
	got, err := st.GetCostLedger(l.Key())
	if err != nil {
		t.Fatal(err)
	}
	want := sampleLedger()
	want.normalise()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read %+v, want %+v", got, want)
	}
	all, err := st.ListCostLedgers()
	if err != nil || len(all) != 1 || len(all[0].Rows) != 2 {
		t.Fatalf("list = %+v %v", all, err)
	}
	if _, err := st.GetCostLedger("claude/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing ledger: %v", err)
	}
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

// A schema 9 database loses the bucket mirror's queue and its records, and
// keeps everything else.
func TestMigrationToSchema10(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socrates.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE mirror_pending (object TEXT PRIMARY KEY, at INTEGER NOT NULL)`,
		`INSERT INTO mirror_pending VALUES ('kv/settings', 1)`,
		`INSERT INTO kv VALUES ('mirror.bucket_id', 'b-1'), ('mirror.meta', '{}'), ('settings', '{}')`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("opening a schema 9 database: %v", err)
	}
	defer st.Close()
	if ok, err := hasTable(st.db, "mirror_pending"); err != nil || ok {
		t.Fatalf("mirror_pending is still there: %v %v", ok, err)
	}
	for _, key := range []string{"mirror.bucket_id", "mirror.meta"} {
		if _, err := st.GetKV(key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s survived the migration: %v", key, err)
		}
	}
	if _, err := st.GetKV("settings"); err != nil {
		t.Fatalf("the settings did not survive the migration: %v", err)
	}
	if _, err := st.GetSession("a1"); err != nil {
		t.Fatal(err)
	}
}
