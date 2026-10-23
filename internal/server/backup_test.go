package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/backup"
	"github.com/saschazesiger/SocratesAgent/internal/s3/s3fake"
)

// backupForm is the Backup card's form for a bucket on the fake.
func fakeBackupForm(url, passphrase string) string {
	return fmt.Sprintf(`{"endpoint":%q,"region":%q,"bucket":"backups","prefix":"","access_key_id":%q,
		"secret_access_key":%q,"path_style":true,"passphrase":%q,"interval_minutes":10}`,
		url, s3fake.DefaultRegion, s3fake.DefaultAccessKeyID, s3fake.DefaultSecretAccessKey, passphrase)
}

func withBackup(t *testing.T, env *testEnv) *backup.Engine {
	t.Helper()
	e, err := backup.New(backup.Options{DataDir: env.srv.dataDir, Iterations: 1000, Exclude: env.srv.BackupExclusions})
	if err != nil {
		t.Fatal(err)
	}
	env.srv.SetBackup(e)
	return e
}

func signIn(t *testing.T, env *testEnv) {
	t.Helper()
	if res, _ := env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", res.StatusCode)
	}
}

// The Backup card configures a bucket, backs up on demand and lists the
// snapshot; the secrets never come back; a new machine lists the same
// snapshot from the setup page and restores it, and gets a backup.json of its
// own and a restart.
func TestBackupCardAndRestoreFromTheSetupPage(t *testing.T) {
	fake := s3fake.Start(s3fake.Options{})
	t.Cleanup(fake.Close)

	a := newEnvWith(t, true)
	withBackup(t, a)
	signIn(t, a)
	if err := os.MkdirAll(filepath.Join(a.srv.dataDir, "chats/c1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.srv.dataDir, "chats/c1/events.jsonl"), []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, payload := a.do(t, a.client, "GET", "/api/backup", "")
	if res.StatusCode != http.StatusOK || payload["configured"] != false {
		t.Fatalf("before: %d %#v", res.StatusCode, payload)
	}
	if res, _ := a.do(t, a.client, "POST", "/api/backup/run", "{}"); res.StatusCode != http.StatusConflict {
		t.Fatalf("a run with nothing configured: %d", res.StatusCode)
	}
	res, payload = a.do(t, a.client, "POST", "/api/backup/test", fakeBackupForm(fake.URL, "pp-one"))
	if result, _ := payload["result"].(map[string]any); res.StatusCode != http.StatusOK || result["ok"] != true || result["found"] != false {
		t.Fatalf("test: %d %#v", res.StatusCode, payload)
	}
	res, payload = a.do(t, a.client, "PUT", "/api/backup/config", fakeBackupForm(fake.URL, "pp-one"))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config: %d %#v", res.StatusCode, payload)
	}
	status, _ := payload["status"].(map[string]any)
	cfg, _ := status["config"].(map[string]any)
	if cfg["bucket"] != "backups" || cfg["prefix"] != backup.DefaultPrefix || cfg["has_passphrase"] != true ||
		cfg["passphrase"] != nil || cfg["secret_access_key"] != nil {
		t.Fatalf("the status shows %#v", cfg)
	}
	// The form sends the secrets empty: they stay.
	res, _ = a.do(t, a.client, "PUT", "/api/backup/config", `{"endpoint":"`+fake.URL+`","region":"`+s3fake.DefaultRegion+
		`","bucket":"backups","access_key_id":"`+s3fake.DefaultAccessKeyID+`","path_style":true,"interval_minutes":15}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("saving without the secrets: %d", res.StatusCode)
	}
	if res, _ := a.do(t, a.client, "PUT", "/api/backup/config", fakeBackupForm(fake.URL, "pp-two")); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("another passphrase for the same backup: %d", res.StatusCode)
	}
	if res, _ := a.do(t, a.client, "PUT", "/api/backup/config", `{"bucket":"b","access_key_id":"x","interval_minutes":2}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an interval of two minutes: %d", res.StatusCode)
	}

	if res, _ := a.do(t, a.client, "POST", "/api/backup/run", "{}"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("run: %d", res.StatusCode)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, payload = a.do(t, a.client, "GET", "/api/backup", "")
		if payload["running"] == false && payload["last_success"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never finished: %#v", payload)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, payload = a.do(t, a.client, "GET", "/api/backup/snapshots", "")
	snaps, _ := payload["snapshots"].([]any)
	if len(snaps) != 1 {
		t.Fatalf("snapshots: %#v", payload)
	}
	snap, _ := snaps[0].(map[string]any)
	if snap["own"] != true || snap["files"] == float64(0) {
		t.Fatalf("snapshot: %#v", snap)
	}
	if _, state := a.do(t, a.client, "GET", "/api/state", ""); state["backup"] == nil {
		t.Fatalf("state carries no backup: %#v", state)
	}
	if res, _ := a.do(t, a.anon, "POST", "/api/restore/snapshots", fakeBackupForm(fake.URL, "pp-one")); res.StatusCode != http.StatusConflict {
		t.Fatalf("a restore onto a machine that is set up: %d", res.StatusCode)
	}
	// Turned off, the machine has no backup.json; the bucket keeps its
	// snapshot for the new machine below.
	res, payload = a.do(t, a.client, "DELETE", "/api/backup/config", "")
	if res.StatusCode != http.StatusOK || payload["status"].(map[string]any)["configured"] != false {
		t.Fatalf("turning backups off: %d %#v", res.StatusCode, payload)
	}
	if _, err := os.Stat(filepath.Join(a.srv.dataDir, "backup.json")); !os.IsNotExist(err) {
		t.Fatalf("backup.json after turning backups off: %v", err)
	}

	b := newEnv(t)
	restarted := make(chan struct{})
	b.srv.SetRestart(func() { close(restarted) })
	if _, state := b.do(t, b.anon, "GET", "/api/state", ""); state["restore_available"] != true {
		t.Fatalf("a new machine offers no restore: %#v", state)
	}
	if res, _ := b.do(t, b.anon, "POST", "/api/restore/snapshots", fakeBackupForm(fake.URL, "wrong")); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong passphrase: %d", res.StatusCode)
	}
	res, payload = b.do(t, b.anon, "POST", "/api/restore/snapshots", fakeBackupForm(fake.URL, "pp-one"))
	if list, _ := payload["snapshots"].([]any); res.StatusCode != http.StatusOK || len(list) != 1 {
		t.Fatalf("listed: %d %#v", res.StatusCode, payload)
	}
	body := fakeBackupForm(fake.URL, "pp-one")
	body = body[:len(body)-1] + fmt.Sprintf(`,"snapshot":%q}`, snap["id"])
	if res, _ := b.do(t, b.anon, "POST", "/api/restore", body); res.StatusCode != http.StatusAccepted {
		t.Fatalf("restore: %d", res.StatusCode)
	}
	select {
	case <-restarted:
	case <-time.After(30 * time.Second):
		_, payload = b.do(t, b.anon, "GET", "/api/restore", "")
		t.Fatalf("no restart: %#v", payload)
	}
	if res, _ := b.do(t, b.anon, "POST", "/api/setup", `{"password":"a-good-password"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a password over a restore: %d", res.StatusCode)
	}
	if !backup.RestorePending(b.srv.dataDir) {
		t.Fatal("nothing is staged")
	}
	if _, err := backup.ApplyPending(b.srv.dataDir); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(b.srv.dataDir, "chats/c1/events.jsonl")); err != nil || string(raw) != "history\n" {
		t.Fatalf("restored: %q %v", raw, err)
	}
	restoredCfg, err := backup.LoadConfig(b.srv.dataDir)
	if err != nil || restoredCfg == nil || restoredCfg.Bucket != "backups" || restoredCfg.HostID == "" ||
		restoredCfg.HostID == cfg["host_id"] {
		t.Fatalf("the restored machine's backup.json: %+v %v (the first machine is %v)", restoredCfg, err, cfg["host_id"])
	}
}
