package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountsRoundTrip(t *testing.T) {
	st := openTest(t)
	a := &Account{ID: "a-1", Agent: "claude", Label: "Work", Kind: "subscription", State: "needs_sign_in",
		Secret: []byte("sealed")}
	b := &Account{ID: "a-2", Agent: "codex", Label: "Home", Kind: "api_key", State: "signed_in"}
	for _, acct := range []*Account{a, b} {
		if err := st.CreateAccount(acct); err != nil {
			t.Fatal(err)
		}
	}
	if a.Order != 1 || b.Order != 2 {
		t.Fatalf("orders %d, %d: a new account goes last", a.Order, b.Order)
	}
	if err := st.SetAccountIdentity("a-1", "me@example.com", "max"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountIdentity("a-1", "", "team"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountLimits("a-1", json.RawMessage(`[{"label":"5h","used_percent":40}]`), 7); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAccount("a-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "me@example.com" || got.Plan != "team" || got.LimitsAt != 7 || string(got.Secret) != "sealed" {
		t.Fatalf("read back %+v", got)
	}
	// The sealed key never leaves the store through JSON.
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "sealed") {
		t.Fatalf("the secret was encoded: %s", raw)
	}
	if err := st.ReorderAccounts([]string{"a-2", "a-1"}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListAccounts()
	if err != nil || len(list) != 2 || list[0].ID != "a-2" {
		t.Fatalf("list after reorder: %+v %v", list, err)
	}
	if err := st.ReorderAccounts([]string{"a-2"}); !errors.Is(err, ErrOrderMismatch) {
		t.Fatalf("an order missing an account: %v", err)
	}
	if err := st.ReorderAccounts([]string{"a-2", "a-2"}); !errors.Is(err, ErrOrderMismatch) {
		t.Fatalf("an order naming one twice: %v", err)
	}
	if err := st.DeleteAccount("a-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAccount("a-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted account is still there: %v", err)
	}
	if err := st.RenameAccount("a-1", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renaming a deleted account: %v", err)
	}
}

func TestAnAccountOfOneOriginIsMadeOnce(t *testing.T) {
	st := openTest(t)
	first := &Account{ID: "a-first", Agent: "claude", Label: "Default", Kind: "subscription", Origin: "home:claude"}
	if err := st.CreateAccount(first); err != nil {
		t.Fatal(err)
	}
	again := &Account{ID: "a-second", Agent: "claude", Label: "Default", Kind: "subscription", Origin: "home:claude"}
	if err := st.CreateAccount(again); err != nil {
		t.Fatal(err)
	}
	if again.ID != "a-first" {
		t.Fatalf("a second account was made for one origin: %s", again.ID)
	}
	list, _ := st.ListAccounts()
	if len(list) != 1 {
		t.Fatalf("%d accounts", len(list))
	}
}

// TestADatabaseOfTodaysMainIsUpgraded opens a database written by the build
// every installation runs today (schema 9, with the mirror's queue and the
// profiles in kv) and checks that the whole chain up to this build's schema
// keeps what it had and adds the accounts.
func TestADatabaseOfTodaysMainIsUpgraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socrates.db")
	schema, err := os.ReadFile(filepath.Join("testdata", "schema-v9.sql"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		string(schema),
		`INSERT INTO kv(key, value) VALUES('profiles', '[{"id":"p-0123456789ab","name":"Team Sascha","created_at":1}]')`,
		`INSERT INTO kv(key, value) VALUES('mirror.instance', 'x')`,
		`INSERT INTO mirror_pending(object, at) VALUES('kv/settings', 1)`,
		`INSERT INTO sessions(id, harness, workdir, workdir_mode, options, created_at, updated_at)
		 VALUES('s1', 'claude', '/srv', 'custom', '{"profile":"p-0123456789ab"}', 1, 1)`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("user_version %d (%v), want %d", version, err, SchemaVersion)
	}
	row, err := st.GetSession("s1")
	if err != nil || row.Kind != KindTerminal || string(row.Options) != `{"profile":"p-0123456789ab"}` {
		t.Fatalf("the session after the upgrade: %+v %v", row, err)
	}
	var profiles string
	if err := st.db.QueryRow(`SELECT value FROM kv WHERE key = 'profiles'`).Scan(&profiles); err != nil {
		t.Fatalf("the profiles are gone: %v", err)
	}
	if ok, _ := hasTable(st.db, "mirror_pending"); ok {
		t.Fatal("the mirror's queue survived")
	}
	if ok, _ := hasTable(st.db, "accounts"); !ok {
		t.Fatal("there is no accounts table")
	}
	if err := st.CreateAccount(&Account{ID: "a-1", Agent: "claude", Label: "Team Sascha", Kind: "subscription"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".pre-v12.bak"); err != nil {
		t.Fatalf("the upgrade was not backed up: %v", err)
	}
	if pending, err := st.AccountsMigrationPending(); err != nil || !pending {
		t.Fatalf("the accounts migration is not marked as due: %v %v", pending, err)
	}
	if err := st.FinishAccountsMigration(); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.AccountsMigrationPending(); pending {
		t.Fatal("the finished migration is still due")
	}
}

// A database this build makes is never one the accounts migration takes
// HOME's folders into, and neither is one that is already at the schema of
// accounts: only the upgrade from a build before them marks it.
func TestOnlyAnUpgradeMarksTheAccountsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socrates.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := st.AccountsMigrationPending(); err != nil || pending {
		t.Fatalf("a new database is marked for the migration: %v %v", pending, err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if pending, _ := st.AccountsMigrationPending(); pending {
		t.Fatal("a second open marked it")
	}
}
