package mirror

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// TestIntegration runs the mirror against a real bucket. It is opt in, like
// internal/s3's: SOCRATES_S3_IT=1 plus the SOCRATES_S3_* variables the server
// reads. It works under devtest/<random>/ whatever SOCRATES_S3_PREFIX says,
// and deletes everything it wrote, so it can be pointed at the production
// bucket without touching production state.
//
// It walks the life of an installation: the first connection uploads an
// existing database, a second machine is refused while the first holds the
// lease, a graceful stop releases it, and the second machine then restores
// everything.
func TestIntegration(t *testing.T) {
	if os.Getenv("SOCRATES_S3_IT") != "1" {
		t.Skip("set SOCRATES_S3_IT=1 and SOCRATES_S3_* to run against a real bucket")
	}
	t.Setenv(EnvPrefix, "")
	cfg, src, err := LoadConfig(t.TempDir())
	if err != nil || src != SourceEnv {
		t.Fatalf("the SOCRATES_S3_* environment is incomplete: %v", err)
	}
	if cfg.Passphrase == "" {
		cfg.Passphrase = "integration passphrase"
	}
	rnd := make([]byte, 6)
	rand.Read(rnd)
	cfg.Prefix = "devtest/mirror-" + hex.EncodeToString(rnd) + "/"
	client, err := s3.New(cfg.S3())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("endpoint %s, prefix %s", client.Endpoint(), cfg.Prefix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		objs, err := client.List(ctx, cfg.Prefix)
		if err != nil {
			t.Errorf("cleanup list: %v", err)
			return
		}
		for _, o := range objs {
			if !strings.HasPrefix(o.Key, "devtest/") {
				t.Errorf("refusing to delete %s", o.Key)
				continue
			}
			if err := client.Delete(ctx, o.Key); err != nil {
				t.Errorf("cleanup %s: %v", o.Key, err)
			}
		}
		if left, _ := client.List(ctx, cfg.Prefix); len(left) != 0 {
			t.Errorf("%d objects left under %s", len(left), cfg.Prefix)
		}
	})
	opts := func(instance string) Options {
		return Options{Instance: instance, Host: "it-" + instance, Version: "it", Iterations: 1000,
			Debounce: 50 * time.Millisecond, MaxLatency: 200 * time.Millisecond, Logf: t.Logf}
	}
	ctx := context.Background()

	a := openStore(t, t.TempDir())
	seed(t, a)
	ma, err := Connect(ctx, a, cfg, opts("a"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("conditional writes: %v", ma.Status().ConditionalWrites)
	ma.Start(ctx)
	if err := a.SetKV("settings", `{"from":"a"}`); err != nil {
		t.Fatal(err)
	}

	b := openStore(t, t.TempDir())
	var held *LeaseHeldError
	if _, err := Connect(ctx, b, cfg, opts("b")); !errors.As(err, &held) || held.Lease.Host != "it-a" {
		t.Fatalf("a second machine was not refused: %v", err)
	}
	if err := ma.Close(ctx); err != nil {
		t.Fatal(err)
	}

	mb, err := Connect(ctx, b, cfg, opts("b"))
	if err != nil {
		t.Fatal(err)
	}
	defer mb.Close(ctx)
	if v, _ := b.GetKV("settings"); v != `{"from":"a"}` {
		t.Fatalf("settings on b = %q", v)
	}
	if row, err := b.GetSession("run1"); err != nil || row.CLISessionID != "cli-abc" {
		t.Fatalf("session on b = %+v %v", row, err)
	}
	if d, err := b.GetDraft("pre1"); err != nil || !strings.HasPrefix(d.Text, "write the tests") {
		t.Fatalf("draft on b = %+v %v", d, err)
	}
	if !b.ValidLogin("token-1") {
		t.Fatal("the sign-in did not come across")
	}
	if err := b.DeleteSession("pre1"); err != nil {
		t.Fatal(err)
	}
	if err := mb.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Head(ctx, cfg.Prefix+store.SessionObject("pre1")); !errors.Is(err, s3.ErrNotFound) {
		t.Fatalf("a deleted row's object is still there: %v", err)
	}
}
