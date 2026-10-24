//go:build !linux

package main

// The rehearsal of the migration runs on Linux (rehearsal_test.go), where the
// agents' folders it moves are exchanged in one step; elsewhere its child
// start is never asked for.
const rehearsalChild = "SOCRATES_REHEARSAL_CHILD"

func rehearsalStart(string) int { return 1 }
