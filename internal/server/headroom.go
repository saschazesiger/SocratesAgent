package server

import "time"

// Which account a chat starts on when nobody chooses (recommendAccount): the
// one with the most allowance left for the time until it renews.
//
// Each account has a five-hour and a seven-day window. What is left of a
// window divided by the hours until it renews is how fast it could be used
// without anything going to waste, and of two accounts the one with the
// higher rate is the one whose allowance runs out unused first. The seven-day
// window is the rate; the five-hour window scales it, because an account that
// is nearly through its five hours is about to stop whatever its week says.
// An account at its limit in either window is not chosen while another is
// not.

// windowHours is how long each window is, for a reading that does not say
// when it renews.
var windowHours = map[string]float64{"5h": 5, "7d": 7 * 24}

// current is a reading as it stands now: a window whose renewal has passed
// since it was taken is empty again. The renewal is handed on in one shape -
// whole seconds in UTC - whatever the source wrote: Anthropic's endpoint
// writes microseconds and an offset, and a browser is only obliged to parse
// the simplified ISO form. A renewal that is not a time at all is left out
// rather than passed on for the page to show as "Invalid Date".
func current(windows []usageWindow, now time.Time) []usageWindow {
	out := make([]usageWindow, 0, len(windows))
	for _, w := range windows {
		if w.ResetsAt != "" {
			at, err := time.Parse(time.RFC3339, w.ResetsAt)
			switch {
			case err != nil:
				w.ResetsAt = ""
			case !at.After(now):
				w.UsedPercent, w.ResetsAt = 0, ""
			default:
				w.ResetsAt = at.UTC().Format(time.RFC3339)
			}
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
