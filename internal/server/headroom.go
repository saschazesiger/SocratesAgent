package server

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// Which profile a new session starts in when nobody chooses: the account with
// the most allowance left for the time until it renews.
//
// Each account has a five-hour and a seven-day window. What is left of a
// window divided by the hours until it renews is how fast it could be used
// without anything going to waste, and of two accounts the one with the
// higher rate is the one whose allowance runs out unused first. The seven-day
// window is the rate; the five-hour window scales it, because an account that
// is nearly through its five hours is about to stop whatever its week says.
// An account at its limit in either window is not chosen while another is
// not.
//
// The limits are read the way the session usage view reads them: Claude
// Code's from Anthropic's usage endpoint with the profile's own sign-in,
// Codex's from the newest rollout file in the profile. OpenCode and the shell
// keep no limits, and are not chosen for.

// profileLimits is what is known about one profile's limits, per program.
type profileLimits struct {
	Claude []usageWindow `json:"claude,omitempty"`
	Codex  []usageWindow `json:"codex,omitempty"`
}

// windowHours is how long each window is, for a reading that does not say
// when it renews.
var windowHours = map[string]float64{"5h": 5, "7d": 7 * 24}

// fillLimits reads every profile's limits at once.
func (s *Server) fillLimits(ctx context.Context, views []profileView) {
	now := time.Now()
	var wg sync.WaitGroup
	for i := range views {
		wg.Add(2)
		go func(v *profileView) {
			defer wg.Done()
			if v.Accounts.Claude.SignedIn {
				v.Limits.Claude = current(s.claudeLimits(ctx, harnesses.ClaudeConfigDirFor(s.dataDir, v.ID)).Windows, now)
			}
		}(&views[i])
		go func(v *profileView) {
			defer wg.Done()
			if v.Accounts.Codex.SignedIn {
				v.Limits.Codex = current(codexAccountLimits(harnesses.CodexHomeFor(s.dataDir, v.ID)), now)
			}
		}(&views[i])
	}
	wg.Wait()
}

// current is a reading as it stands now: a window whose renewal has passed
// since it was taken is empty again.
func current(windows []usageWindow, now time.Time) []usageWindow {
	out := make([]usageWindow, 0, len(windows))
	for _, w := range windows {
		if at, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil && !at.After(now) {
			w.UsedPercent, w.ResetsAt = 0, ""
		}
		out = append(out, w)
	}
	return out
}

// headroom is the rate described above, in percent of the week per hour. It
// is zero for an account at its limit, and known is false when there is no
// reading at all.
func headroom(windows []usageWindow, now time.Time) (rate float64, known bool) {
	left := map[string]float64{}
	hours := map[string]float64{}
	for _, w := range windows {
		length, ok := windowHours[w.Label]
		if !ok {
			continue
		}
		left[w.Label] = max(0, 100-w.UsedPercent)
		hours[w.Label] = length
		if at, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil && at.After(now) {
			hours[w.Label] = at.Sub(now).Hours()
		}
		// A renewal a few minutes away is not a reason to call the
		// allowance before it nearly infinite.
		hours[w.Label] = max(hours[w.Label], 0.25)
	}
	if len(left) == 0 {
		return 0, false
	}
	for _, l := range left {
		if l <= 0 {
			return 0, true
		}
	}
	if week, ok := left["7d"]; ok {
		rate = week / hours["7d"]
		if five, ok := left["5h"]; ok {
			rate *= five / 100
		}
		return rate, true
	}
	return left["5h"] / hours["5h"], true
}

// recommendProfiles is the profile each program should start in by default,
// by its id; a program with no entry has nothing to choose by - no account
// has a reading yet - and the sheet keeps the last profile chosen. A profile
// the program is not signed in to is never chosen, and one with no reading is
// chosen only when every account with a reading is at its limit.
func recommendProfiles(views []profileView, now time.Time) map[string]string {
	out := map[string]string{}
	if len(views) < 2 {
		return out
	}
	pick := func(signedIn func(profileView) bool, windows func(profileView) []usageWindow) (string, bool) {
		best, bestRate, unknown := "", 0.0, ""
		found, anyKnown, haveUnknown := false, false, false
		for _, v := range views {
			if !signedIn(v) {
				continue
			}
			rate, known := headroom(windows(v), now)
			anyKnown = anyKnown || known
			switch {
			case !known:
				if !haveUnknown {
					unknown, haveUnknown = v.ID, true
				}
			case rate > 0 && (!found || rate > bestRate):
				best, bestRate, found = v.ID, rate, true
			}
		}
		if found {
			return best, true
		}
		return unknown, anyKnown && haveUnknown
	}
	if id, ok := pick(func(v profileView) bool { return v.Accounts.Claude.SignedIn },
		func(v profileView) []usageWindow { return v.Limits.Claude }); ok {
		out[string(harnesses.KindClaude)] = id
	}
	if id, ok := pick(func(v profileView) bool { return v.Accounts.Codex.SignedIn },
		func(v profileView) []usageWindow { return v.Limits.Codex }); ok {
		out[string(harnesses.KindCodex)] = id
	}
	return out
}

// codexAccountLimits is the last limits Codex wrote down in a home: the rate
// limits of the newest rollout that has any. Rollouts are filed by date, so
// the newest are found by walking the tree backwards, and only a few files
// are opened.
func codexAccountLimits(home string) []usageWindow {
	if home == "" {
		return nil
	}
	for _, path := range newestRollouts(filepath.Join(home, "sessions"), 5) {
		if windows, _ := codexUsage(path, ""); len(windows) > 0 {
			return windows
		}
	}
	return nil
}

// newestRollouts is up to n rollout files under root, newest name first.
func newestRollouts(root string, n int) []string {
	var out []string
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
		for _, entry := range entries {
			if len(out) >= n {
				return
			}
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				walk(path)
			} else if filepath.Ext(entry.Name()) == ".jsonl" {
				out = append(out, path)
			}
		}
	}
	walk(root)
	return out
}
