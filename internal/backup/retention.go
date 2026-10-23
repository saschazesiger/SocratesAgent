package backup

import (
	"context"
	"fmt"
	"time"
)

// Retention: every snapshot of the last 48 hours, the newest of each day for
// 30 days, and the newest of each week for 26 weeks. The newest snapshot of a
// machine is kept whatever its age, so a machine that stopped backing up long
// ago still has something to restore from.
//
// Each machine prunes only its own snapshots - another machine's are its
// business, and it may be running - but garbage collection weighs every
// snapshot in the bucket, because chunks are shared between all of them.
const (
	keepAll   = 48 * time.Hour
	keepDays  = 30 * 24 * time.Hour
	keepWeeks = 26 * 7 * 24 * time.Hour
)

// DefaultGCGrace is how old a chunk no snapshot refers to must be before it
// is deleted. A run uploads its chunks before its manifest, so for a while
// every chunk of a running snapshot - this machine's or another's - is
// referred to by nothing; the grace is far longer than any run.
const DefaultGCGrace = 24 * time.Hour

// gcEvery is how often a successful run is followed by pruning and garbage
// collection.
const gcEvery = 6 * time.Hour

// retain picks the snapshots to keep out of times (one machine's), by index.
func retain(times []time.Time, now time.Time) map[int]bool {
	keep := map[int]bool{}
	newest := -1
	day := map[string]int{}
	week := map[string]int{}
	for i, t := range times {
		if newest < 0 || t.After(times[newest]) {
			newest = i
		}
		age := now.Sub(t)
		if age <= keepAll {
			keep[i] = true
		}
		if age <= keepDays {
			d := t.UTC().Format("2006-01-02")
			if j, ok := day[d]; !ok || t.After(times[j]) {
				day[d] = i
			}
		}
		if age <= keepWeeks {
			y, w := t.UTC().ISOWeek()
			k := fmt.Sprintf("%d-%02d", y, w)
			if j, ok := week[k]; !ok || t.After(times[j]) {
				week[k] = i
			}
		}
	}
	for _, i := range day {
		keep[i] = true
	}
	for _, i := range week {
		keep[i] = true
	}
	if newest >= 0 {
		keep[newest] = true
	}
	return keep
}

// maintenance is what pruning and garbage collection did.
type maintenance struct {
	Pruned  int
	Deleted int
}

// prune deletes this machine's snapshots retention does not keep, and then
// every chunk that no snapshot left in the bucket refers to and that is older
// than grace. A manifest that cannot be read stops the collection before
// anything is deleted: a chunk is only ever deleted on a complete picture of
// what refers to it.
func (e *Engine) prune(ctx context.Context, r *repo, idx *index, host string) (maintenance, error) {
	var done maintenance
	refs, err := r.listSnapshots(ctx)
	if err != nil {
		return done, err
	}
	var own []snapRef
	var times []time.Time
	for _, ref := range refs {
		if ref.Host == host {
			own = append(own, ref)
			times = append(times, ref.At)
		}
	}
	keep := retain(times, e.now())
	gone := map[string]bool{}
	for i, ref := range own {
		if keep[i] {
			continue
		}
		if err := r.remove(ctx, snapshotKey(ref.ID)); err != nil {
			return done, err
		}
		gone[ref.ID] = true
		done.Pruned++
	}

	referenced := map[string]bool{}
	left := map[string]bool{}
	for _, ref := range refs {
		if gone[ref.ID] {
			continue
		}
		m, err := e.manifest(ctx, r, ref.ID)
		if err != nil {
			return done, fmt.Errorf("garbage collection needs snapshot %s: %w", ref.ID, err)
		}
		m.chunkSet(referenced)
		left[ref.ID] = true
	}
	forgetManifests(e.opts.DataDir, left)

	chunks, err := r.listChunks(ctx)
	if err != nil {
		return done, err
	}
	now := e.now()
	remote := make(map[string]bool, len(chunks))
	for name, written := range chunks {
		if referenced[name] || now.Sub(written) < e.opts.GCGrace {
			remote[name] = true
			continue
		}
		if err := r.remove(ctx, chunkDir+name); err != nil {
			return done, err
		}
		done.Deleted++
	}
	idx.remote = remote
	idx.listed = true
	idx.LastGC = now.UnixMilli()
	return done, nil
}
