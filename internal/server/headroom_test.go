//go:build !windows

package server

import (
	"testing"
	"time"
)

func at(now time.Time, d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }

func TestHeadroomPrefersTheAllowanceThatRenewsFirst(t *testing.T) {
	now := time.Now()
	// 20% of the week left with ten hours to go is used faster than 70% left
	// with six days to go, so it is the one that would go to waste.
	soon := []usageWindow{{Label: "5h", UsedPercent: 0, ResetsAt: at(now, 3*time.Hour)}, {Label: "7d", UsedPercent: 80, ResetsAt: at(now, 10*time.Hour)}}
	late := []usageWindow{{Label: "5h", UsedPercent: 0, ResetsAt: at(now, 3*time.Hour)}, {Label: "7d", UsedPercent: 30, ResetsAt: at(now, 140*time.Hour)}}
	a, _ := headroom(soon, now)
	b, _ := headroom(late, now)
	if a <= b {
		t.Fatalf("headroom(soon) = %v, headroom(late) = %v", a, b)
	}
	// A five-hour window that is nearly used up holds an account back.
	tight := []usageWindow{{Label: "5h", UsedPercent: 95, ResetsAt: at(now, time.Hour)}, {Label: "7d", UsedPercent: 80, ResetsAt: at(now, 10*time.Hour)}}
	if c, _ := headroom(tight, now); c >= b {
		t.Fatalf("headroom(tight) = %v, not below %v", c, b)
	}
	if rate, known := headroom([]usageWindow{{Label: "5h", UsedPercent: 100}, {Label: "7d", UsedPercent: 10}}, now); !known || rate != 0 {
		t.Fatalf("an account at its limit has headroom %v, %v", rate, known)
	}
	if _, known := headroom(nil, now); known {
		t.Fatal("no reading is known")
	}
	// A window whose renewal has passed is empty again.
	renewed := current([]usageWindow{{Label: "5h", UsedPercent: 100, ResetsAt: at(now, -time.Minute)}}, now)
	if renewed[0].UsedPercent != 0 {
		t.Fatalf("a renewed window reads %v", renewed[0].UsedPercent)
	}
}

func TestCurrentNormalizesTheRenewal(t *testing.T) {
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	got := current([]usageWindow{
		// Anthropic's shape: microseconds and an offset.
		{Label: "5h", UsedPercent: 40, ResetsAt: "2026-09-05T12:30:00.123456+02:00"},
		{Label: "7d", UsedPercent: 12, ResetsAt: "not a time"},
		{Label: "Fable", UsedPercent: 5},
		{Label: "5h", UsedPercent: 90, ResetsAt: "2026-09-05T07:59:59Z"},
	}, now)
	if got[0].ResetsAt != "2026-09-05T10:30:00Z" || got[0].UsedPercent != 40 {
		t.Fatalf("a future renewal = %#v", got[0])
	}
	if got[1].ResetsAt != "" || got[1].UsedPercent != 12 {
		t.Fatalf("an unreadable renewal = %#v", got[1])
	}
	if got[2].ResetsAt != "" || got[2].UsedPercent != 5 {
		t.Fatalf("no renewal = %#v", got[2])
	}
	if got[3].ResetsAt != "" || got[3].UsedPercent != 0 {
		t.Fatalf("a passed renewal = %#v", got[3])
	}
}
