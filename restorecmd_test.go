//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/backup"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// `socrates restore`, as the README's SSH procedure runs it: the old
// machine's backup.json copied over, the snapshots listed, the newest
// restored. It is refused while a Socrates holds the data directory and on
// one that holds an installation, and the restored machine backs up under a
// host id of its own.
func TestRestoreCommandRestoresAsAMachineOfItsOwn(t *testing.T) {
	fake := s3fake.Start(s3fake.Options{Buckets: []string{"backups"}})
	t.Cleanup(fake.Close)
	cfg := backup.Config{
		Endpoint: fake.URL, Region: s3fake.DefaultRegion, Bucket: "backups",
		AccessKeyID: s3fake.DefaultAccessKeyID, SecretAccessKey: s3fake.DefaultSecretAccessKey,
		PathStyle: true, Passphrase: "the passphrase", HostID: "old-machine-abc123",
	}

	old := t.TempDir()
	st, err := store.Open(paths.New(old).DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("password_hash", "pbkdf2-sha256$1$a$b"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(old, "chats/c1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "chats/c1/events.jsonl"), []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.SaveConfig(old, cfg); err != nil {
		t.Fatal(err)
	}
	e, err := backup.New(backup.Options{DataDir: old, Iterations: 1000})
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := restoreMain(args, &out, &out)
		return code, out.String()
	}
	fresh := t.TempDir()
	if code, out := run("-data", fresh, "-list"); code != 1 || !strings.Contains(out, "no backup.json") {
		t.Fatalf("a listing without backup.json: %d %s", code, out)
	}
	raw, err := os.ReadFile(paths.New(old).BackupConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.New(fresh).BackupConfig(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := run("-data", fresh, "-list"); code != 0 || !strings.Contains(out, info.ID) {
		t.Fatalf("the listing: %d %s", code, out)
	}

	unlock, err := lockDataDir(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := run("-data", fresh, "-snapshot", "latest"); code != 1 || !strings.Contains(out, "another Socrates is running") {
		t.Fatalf("a restore under a running Socrates: %d %s", code, out)
	}
	unlock()
	if backup.RestorePending(fresh) {
		t.Fatal("the refused restore staged something")
	}

	// A restore that fails has already made this a machine of its own: a
	// Socrates started on it anyway never backs up as the old one.
	if code, out := run("-data", fresh, "-snapshot", "old-machine-abc123/20200101T000000.000Z"); code != 1 || !strings.Contains(out, "no snapshot") {
		t.Fatalf("a restore of a snapshot that is not there: %d %s", code, out)
	}
	if failed, _ := backup.LoadConfig(fresh); failed == nil || failed.HostID == cfg.HostID {
		t.Fatalf("after a failed restore the host id is still the old machine's: %+v", failed)
	}

	if code, out := run("-data", fresh, "-snapshot", "latest"); code != 0 || !strings.Contains(out, "Restored "+info.ID) {
		t.Fatalf("the restore: %d %s", code, out)
	}
	if got, err := os.ReadFile(filepath.Join(fresh, "chats/c1/events.jsonl")); err != nil || string(got) != "history\n" {
		t.Fatalf("restored: %q %v", got, err)
	}
	restored, err := backup.LoadConfig(fresh)
	if err != nil || restored == nil || restored.HostID == "" || restored.HostID == cfg.HostID || restored.Bucket != "backups" {
		t.Fatalf("the restored machine's backup.json: %+v %v", restored, err)
	}

	// Restored, it is an installation: a second restore is refused before
	// anything changes, its host id included.
	if code, out := run("-data", fresh, "-snapshot", "latest"); code != 1 || !strings.Contains(out, "already holds a Socrates installation") {
		t.Fatalf("a restore onto an installation: %d %s", code, out)
	}
	if again, _ := backup.LoadConfig(fresh); again == nil || again.HostID != restored.HostID {
		t.Fatalf("a refused restore changed the host id: %+v", again)
	}
	if code, _ := run("-data", fresh); code != 2 {
		t.Fatalf("no snapshot named: %d", code)
	}
}
