//go:build !windows

package opencode

import (
	"crypto/rand"
	"sync"
	"time"
)

// OpenCode's ids are a prefix, twelve hex digits of (milliseconds × 4096 +
// a counter) truncated to 48 bits, and fourteen random base-62 characters
// (packages/opencode/src/id/id.ts). The session loop orders messages by id,
// so a message id Socrates chooses itself - which is how a prompt is matched
// with the message OpenCode makes of it - must sort the way OpenCode's own
// do: made the same way, from the same clock.
const (
	idTimeBytes = 6
	idRandom    = 14
	base62      = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	idMu      sync.Mutex
	idLast    int64
	idCounter int64
)

// ascendingID is a new id with prefix ("msg", "prt") that sorts after every
// id made before it.
func ascendingID(prefix string) string {
	idMu.Lock()
	now := time.Now().UnixMilli()
	if now != idLast {
		idLast, idCounter = now, 0
	}
	idCounter++
	n := uint64(now)*0x1000 + uint64(idCounter)
	idMu.Unlock()
	const hex = "0123456789abcdef"
	b := make([]byte, 0, len(prefix)+1+2*idTimeBytes+idRandom)
	b = append(b, prefix...)
	b = append(b, '_')
	for i := 0; i < idTimeBytes; i++ {
		v := byte(n >> (40 - 8*i))
		b = append(b, hex[v>>4], hex[v&0xf])
	}
	var r [idRandom]byte
	_, _ = rand.Read(r[:])
	for _, v := range r {
		b = append(b, base62[int(v)%len(base62)])
	}
	return string(b)
}
