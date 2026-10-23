// Package vault encrypts what Socrates puts in its bucket.
//
// The bucket is somebody else's computer. It holds the backup of the whole
// data directory - the password hash, the login cookies, push keys, every
// session and every agent's logins and transcripts - and none of that should
// be readable, or quietly replaceable, by whoever can read the bucket. So
// every object is sealed on this side with AES-256-GCM under a key derived
// from a passphrase that is never written to the bucket.
//
// Each sealed object binds its own object key as additional authenticated
// data. Without that, someone with write access to the bucket could not
// read anything but could still move ciphertext around - put one session's
// row where another's belongs, or an old login list over the current one -
// and it would decrypt cleanly. With it, an object only opens under the name
// it was sealed for.
//
// What this does not stop: deleting objects, or rolling one object back to
// an older version of itself under the same name. The bucket's owner can
// always destroy data; detecting rollback would need a signed index, which
// is more machinery than the threat (a hostile storage provider replaying
// a stale settings row) is worth.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

const (
	// Iterations is the PBKDF2 work factor for new buckets: OWASP's 2023
	// figure for PBKDF2-HMAC-SHA256. A derivation takes a fraction of a
	// second and happens once per process start.
	Iterations = 600000
	// KDF names the derivation in meta.json.
	KDF = "pbkdf2-sha256"
	// SaltSize is the length of a new salt.
	SaltSize = 16
	// KeySize is the AES-256 key length.
	KeySize = 32
	// NonceSize is the GCM nonce length. Nonces are random: a bucket sees
	// far fewer than the 2^32 seals per key at which random 96-bit nonces
	// start to be a concern.
	NonceSize = 12

	// maxIterations bounds what a meta.json can make us spend. The
	// iteration count is read from the bucket, and a bucket that asks for
	// a billion would hang every start for minutes.
	maxIterations = 10_000_000
	minIterations = 1000

	// CheckObject and CheckPlaintext make the check value kept in
	// meta.json: Seal(key, CheckObject, CheckPlaintext). Opening it proves a
	// passphrase right before anything else is touched.
	CheckObject    = "meta-check"
	CheckPlaintext = "socrates"
)

// magic starts every sealed object and names the format version, so that a
// future format can be told apart and a plaintext object is recognised
// rather than fed to GCM.
var magic = []byte("SOC1")

var (
	// ErrNotSealed is data that is not in the sealed format at all (no
	// magic, too short): a plaintext object, or a different format version.
	ErrNotSealed = errors.New("vault: not a sealed object")
	// ErrAuth is a sealed object that fails authentication: the wrong key
	// (passphrase), an object stored under a different name, or tampering.
	// GCM cannot tell these apart, and neither can this.
	ErrAuth = errors.New("vault: wrong key, or the object was altered or moved")
	// ErrWrongPassphrase is what VerifyCheck reports when the check value
	// does not open: at that point the only plausible cause is the
	// passphrase.
	ErrWrongPassphrase = errors.New("vault: wrong passphrase")
)

// Key is a derived encryption key, ready to use. The zero value is not.
type Key struct {
	aead cipher.AEAD
}

// NewSalt returns a fresh random salt for a new bucket.
func NewSalt() ([]byte, error) {
	salt := make([]byte, SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, nil
}

// DeriveKey stretches a passphrase into a key with PBKDF2-HMAC-SHA256.
// iterations is the count recorded in meta.json (Iterations for a new
// bucket).
func DeriveKey(passphrase string, salt []byte, iterations int) (*Key, error) {
	raw, err := DeriveSecret(passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}
	return NewKey(raw)
}

// DeriveSecret is DeriveKey's stretched passphrase as raw bytes, for a caller
// that derives more than one key from it (Subkey) rather than using it as
// the sealing key itself.
func DeriveSecret(passphrase string, salt []byte, iterations int) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("vault: empty passphrase")
	}
	if len(salt) < 8 {
		return nil, fmt.Errorf("vault: salt of %d bytes is too short", len(salt))
	}
	if iterations < minIterations || iterations > maxIterations {
		return nil, fmt.Errorf("vault: %d iterations is outside %d..%d", iterations, minIterations, maxIterations)
	}
	raw, err := pbkdf2.Key(sha256.New, passphrase, salt, iterations, KeySize)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return raw, nil
}

// Subkey derives an independent key of KeySize bytes from a secret for one
// purpose, named by label: HMAC-SHA256(secret, label). Two labels give two
// keys that say nothing about each other or about the secret.
func Subkey(secret []byte, label string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// NewKey wraps 32 raw key bytes. DeriveKey is the normal way to get a Key;
// this is for tests and for a key kept elsewhere.
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeySize {
		return nil, fmt.Errorf("vault: key must be %d bytes, not %d", KeySize, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead}, nil
}

// Seal encrypts plaintext for the object named objectKey (relative to the
// bucket prefix, so a whole prefix can be moved without re-encrypting):
// "SOC1" || 12-byte random nonce || AES-GCM ciphertext and tag, with
// objectKey as additional data.
func Seal(k *Key, objectKey string, plaintext []byte) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, errors.New("vault: no key")
	}
	out := make([]byte, len(magic)+NonceSize, len(magic)+NonceSize+len(plaintext)+k.aead.Overhead())
	copy(out, magic)
	nonce := out[len(magic):]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(out, nonce, plaintext, []byte(objectKey)), nil
}

// Open reverses Seal. It fails with ErrNotSealed for data that is not in
// the format and ErrAuth for anything that does not authenticate.
func Open(k *Key, objectKey string, sealed []byte) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, errors.New("vault: no key")
	}
	if len(sealed) < len(magic)+NonceSize+k.aead.Overhead() || !bytes.HasPrefix(sealed, magic) {
		return nil, ErrNotSealed
	}
	nonce := sealed[len(magic) : len(magic)+NonceSize]
	plain, err := k.aead.Open(nil, nonce, sealed[len(magic)+NonceSize:], []byte(objectKey))
	if err != nil {
		return nil, ErrAuth
	}
	return plain, nil
}

// MakeCheck seals the check value for meta.json.
func MakeCheck(k *Key) ([]byte, error) {
	return Seal(k, CheckObject, []byte(CheckPlaintext))
}

// VerifyCheck opens a check value made by MakeCheck. A key from the wrong
// passphrase fails with ErrWrongPassphrase; a check that is not in the
// sealed format at all fails with ErrNotSealed (a damaged meta.json, not a
// typo in the passphrase).
func VerifyCheck(k *Key, check []byte) error {
	plain, err := Open(k, CheckObject, check)
	switch {
	case errors.Is(err, ErrAuth):
		return ErrWrongPassphrase
	case err != nil:
		return err
	case string(plain) != CheckPlaintext:
		// Authenticated, so sealed with this key, but not the check value:
		// a meta.json written by something else.
		return errors.New("vault: the check value has unexpected contents")
	}
	return nil
}
