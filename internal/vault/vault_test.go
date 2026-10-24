package vault

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fastKey derives with a low iteration count so the tests stay quick; the
// production count has its own test below.
func fastKey(t *testing.T, pass string, salt []byte) *Key {
	t.Helper()
	k, err := DeriveKey(pass, salt, minIterations)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpen(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k := fastKey(t, "correct horse", salt)
	for _, plain := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte("session row "), 10000)} {
		sealed, err := Seal(k, "sessions/abc.json", plain)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(sealed, []byte("SOC1")) || len(sealed) != 4+NonceSize+len(plain)+16 {
			t.Fatalf("format: %d bytes for %d", len(sealed), len(plain))
		}
		// Only a plaintext long enough not to turn up in random bytes by
		// chance says anything: a one-byte "x" is somewhere in the 33 bytes
		// of nonce, ciphertext and tag about one run in eight.
		if len(plain) >= 16 && bytes.Contains(sealed, plain) {
			t.Fatal("plaintext visible in sealed data")
		}
		got, err := Open(k, "sessions/abc.json", sealed)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("Open: %v", err)
		}
	}

	// Random nonces: sealing twice gives different bytes.
	a, _ := Seal(k, "o", []byte("same"))
	b, _ := Seal(k, "o", []byte("same"))
	if bytes.Equal(a, b) {
		t.Error("two seals are identical")
	}
}

func TestOpenRefuses(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k := fastKey(t, "correct horse", salt)
	sealed, _ := Seal(k, "kv/theme", []byte("dark"))

	// Moved to another name: the AAD binds it.
	if _, err := Open(k, "kv/other", sealed); !errors.Is(err, ErrAuth) {
		t.Errorf("moved: %v", err)
	}
	// Wrong passphrase.
	if _, err := Open(fastKey(t, "wrong horse", salt), "kv/theme", sealed); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong key: %v", err)
	}
	// Same passphrase, other salt: another key.
	if _, err := Open(fastKey(t, "correct horse", []byte("fedcba9876543210")), "kv/theme", sealed); !errors.Is(err, ErrAuth) {
		t.Errorf("other salt: %v", err)
	}
	// Every single flipped byte is caught, the magic as ErrNotSealed.
	for i := range sealed {
		bad := append([]byte(nil), sealed...)
		bad[i] ^= 0x01
		_, err := Open(k, "kv/theme", bad)
		want := ErrAuth
		if i < 4 {
			want = ErrNotSealed
		}
		if !errors.Is(err, want) {
			t.Errorf("flip byte %d: %v", i, err)
		}
	}
	// Truncated, plaintext, empty.
	for _, bad := range [][]byte{sealed[:len(sealed)-1], sealed[:20], []byte(`{"theme":"dark"}`), nil} {
		_, err := Open(k, "kv/theme", bad)
		if err == nil {
			t.Errorf("opened %q", bad)
		}
	}
	if _, err := Open(k, "kv/theme", []byte("{}")); !errors.Is(err, ErrNotSealed) {
		t.Errorf("plaintext: %v", err)
	}
	if _, err := Open(nil, "x", sealed); err == nil {
		t.Error("nil key opened")
	}
	if _, err := Seal(&Key{}, "x", nil); err == nil {
		t.Error("zero key sealed")
	}
}

func TestCheck(t *testing.T) {
	salt, err := NewSalt()
	if err != nil || len(salt) != SaltSize {
		t.Fatal(err)
	}
	k := fastKey(t, "passphrase", salt)
	check, err := MakeCheck(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheck(k, check); err != nil {
		t.Fatalf("right passphrase: %v", err)
	}
	if err := VerifyCheck(fastKey(t, "Passphrase", salt), check); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if err := VerifyCheck(k, []byte("garbage")); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("garbage: %v", err)
	}
	// Sealed with the right key under the check name, wrong contents.
	other, _ := Seal(k, CheckObject, []byte("not it"))
	if err := VerifyCheck(k, other); err == nil || errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("other contents: %v", err)
	}
	// A real object cannot pass as the check value.
	obj, _ := Seal(k, "kv/x", []byte(CheckPlaintext))
	if err := VerifyCheck(k, obj); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("object as check: %v", err)
	}
}

func TestDeriveKey(t *testing.T) {
	salt := []byte("0123456789abcdef")
	for _, tc := range []struct {
		pass  string
		salt  []byte
		iters int
	}{
		{"", salt, Iterations},
		{"p", []byte("short"), Iterations},
		{"p", salt, 10},
		{"p", salt, maxIterations + 1},
	} {
		if _, err := DeriveKey(tc.pass, tc.salt, tc.iters); err == nil {
			t.Errorf("DeriveKey(%q, %q, %d) accepted", tc.pass, tc.salt, tc.iters)
		}
	}
	if _, err := NewKey(make([]byte, 16)); err == nil {
		t.Error("16-byte key accepted")
	}

	// The production work factor, and that DeriveKey is plain PBKDF2: a key
	// derived independently with the same parameters opens what it sealed.
	k, err := DeriveKey("correct horse battery staple", salt, Iterations)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := pbkdf2.Key(sha256.New, "correct horse battery staple", salt, Iterations, KeySize)
	same, _ := NewKey(raw)
	sealed, _ := Seal(k, "o", []byte("v"))
	if got, err := Open(same, "o", sealed); err != nil || string(got) != "v" {
		t.Fatalf("independent key: %v", err)
	}
}

// TestPBKDF2Vector pins the KDF to a published PBKDF2-HMAC-SHA256 test
// vector (RFC 7914 section 11), so that a key derived here is the key any
// other implementation derives - the passphrase is the only way back into
// a bucket, and it must not depend on this code staying the same.
func TestPBKDF2Vector(t *testing.T) {
	raw, err := pbkdf2.Key(sha256.New, "passwd", []byte("salt"), 1, 64)
	if err != nil {
		t.Fatal(err)
	}
	want := "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc" +
		"49ca9cccf179b645991664b39d77ef317c71b845b1e30bd509112041d3a19783"
	if hex.EncodeToString(raw) != want {
		t.Fatalf("PBKDF2 vector: %x", raw)
	}
}

func TestAKeyFileIsMadeOnceAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if _, err := LoadKeyFile(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing key file: %v", err)
	}
	first, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != KeySize {
		t.Fatalf("the key file: %v %v", fi, err)
	}
	sealed, err := Seal(first, "account:a-1", []byte("sk-test"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(again, "account:a-1", sealed)
	if err != nil || string(plain) != "sk-test" {
		t.Fatalf("the key read again does not open what the first sealed: %q %v", plain, err)
	}
	if _, err := Open(again, "account:a-2", sealed); !errors.Is(err, ErrAuth) {
		t.Fatalf("a secret opened under another name: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeyFile(path); err == nil {
		t.Fatal("a damaged key file was taken, or replaced")
	}
}
