//go:build !windows

package termux

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRingReplay(t *testing.T) {
	r := NewRing(16)
	if seq := r.Append([]byte("hello")); seq != 0 {
		t.Fatalf("the first append should start at 0, got %d", seq)
	}
	if seq := r.Append([]byte(" world")); seq != 5 {
		t.Fatalf("the second append should start at 5, got %d", seq)
	}
	got, ok := r.Since(0)
	if !ok || string(got) != "hello world" {
		t.Fatalf("Since(0) = %q, %v", got, ok)
	}
	got, ok = r.Since(5)
	if !ok || string(got) != " world" {
		t.Fatalf("Since(5) = %q, %v; want exactly the missing bytes", got, ok)
	}
	if got, ok := r.Since(r.Head()); !ok || len(got) != 0 {
		t.Fatalf("Since(head) = %q, %v; want nothing and no error", got, ok)
	}

	// Push the beginning out of the ring: a viewer that asks for it must be
	// told no rather than handed a hole.
	r.Append([]byte(strings.Repeat("x", 20)))
	if _, ok := r.Since(0); ok {
		t.Fatal("Since should refuse a sequence older than the ring still holds")
	}
	if base := r.Base(); base != r.Head()-16 {
		t.Fatalf("the ring should hold its full size: base %d, head %d", base, r.Head())
	}
	if got, ok := r.Since(r.Base()); !ok || len(got) != 16 {
		t.Fatalf("Since(base) = %d bytes, %v; want the whole ring", len(got), ok)
	}
}

func TestRingWaitWakesOnAppend(t *testing.T) {
	r := NewRing(1024)
	woke := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		woke <- r.Wait(ctx, 0)
	}()
	time.Sleep(20 * time.Millisecond)
	r.Append([]byte("x"))
	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("Wait returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not wake on Append")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Wait(ctx, r.Head()); err == nil {
		t.Fatal("Wait should end with the context when nothing is appended")
	}
}

// TestSlowViewerDoesNotGrowMemory pins the property the whole transport rests
// on: the reader only ever appends to a fixed ring, so a browser that stops
// reading costs nothing and stalls nothing.
func TestSlowViewerDoesNotGrowMemory(t *testing.T) {
	r := NewRing(64 * 1024)
	chunk := []byte(strings.Repeat("y", 4096))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 4096; i++ { // 16 MiB through a 64 KiB ring
		r.Append(chunk)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)

	if got := r.Head(); got != 4096*4096 {
		t.Fatalf("the ring lost track of the stream: head %d", got)
	}
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 1<<20 {
		t.Fatalf("a viewer that never read grew the heap by %d bytes", grew)
	}
	if _, ok := r.Since(0); ok {
		t.Fatal("the ring should have forgotten the beginning of the stream")
	}
}
