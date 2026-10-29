package driver

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// base32Decode decodes an authenticator key as Google and Microsoft show it
// (groups, lower case, padding allowed).
func base32Decode(key string) []byte {
	var out []byte
	var buffer uint32
	bits := 0
	for _, c := range strings.ToUpper(key) {
		v := strings.IndexRune(base32Alphabet, c)
		if v < 0 {
			continue
		}
		buffer = buffer<<5 | uint32(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(buffer>>bits))
			buffer &= 1<<bits - 1
		}
	}
	return out
}

// TOTP is the current authenticator code of a base32 key (RFC 6238 with
// SHA-1, 6 digits and 30 s steps, what Google and Microsoft use) and the
// seconds it stays valid.
func TOTP(key string, now time.Time) (code string, secondsLeft int) {
	return totpOf(base32Decode(key), now)
}

func totpOf(secret []byte, now time.Time) (string, int) {
	seconds := now.UnixMilli() / 1000
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(seconds/30))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 15
	value := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), int(30 - seconds%30)
}
