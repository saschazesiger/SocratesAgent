// Package backup keeps an encrypted, incremental backup of the data
// directory in an S3 bucket, and restores one onto a new machine.
//
// The data directory is the store of record (internal/paths). The bucket is
// only ever written by a backup run and read by a restore; nothing Socrates
// does while it runs waits on it, and a Socrates without a bucket - or with
// one that cannot be reached - works exactly as one with it.
//
// A run walks the data directory, minus what a new machine makes again by
// itself (paths.NotBackedUp, and a workspace that happens to lie inside it),
// and splits every file into chunks of ChunkSize. A chunk is sealed with the
// vault (AES-256-GCM under a key from the passphrase) and named by an HMAC of
// its content, so a chunk the bucket already holds is never uploaded again:
// a file that did not change costs nothing, and one that grew at its end
// costs its last chunk. Every SQLite database - recognised by its header,
// whatever it is called, because the agents keep their own - is copied
// consistently first, never read raw while it may be written. The snapshot
// itself is a manifest of every path with its chunks, mode, time and link
// target, sealed too, under snapshots/<host>/<time>.json, and it is written
// last: a run that fails or is cut short leaves chunks nothing refers to, and
// never a manifest that refers to a chunk that is not there.
//
// Runs happen every Config.Interval while anything changed, once at a clean
// shutdown, and when somebody asks; one at a time. Old snapshots are pruned
// (retention.go) and chunks no snapshot refers to any more are deleted after
// a grace period.
//
// What this does not stop: a second machine's garbage collection deleting,
// at the very moment this one writes a manifest, an old chunk the manifest
// reuses and no other snapshot refers to. Each machine collects only after
// its own runs and only chunks older than DefaultGCGrace, which makes this a
// matter of two machines backing up the same rare content into one bucket
// within the same few seconds.
package backup
