package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
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

func TestRecommendProfiles(t *testing.T) {
	now := time.Now()
	signed := profileLogins{Claude: agentLogin{SignedIn: true}, Codex: agentLogin{SignedIn: true}}
	full := []usageWindow{{Label: "5h", UsedPercent: 100, ResetsAt: at(now, time.Hour)}}
	some := []usageWindow{{Label: "7d", UsedPercent: 50, ResetsAt: at(now, 24*time.Hour)}}
	more := []usageWindow{{Label: "7d", UsedPercent: 50, ResetsAt: at(now, 100*time.Hour)}}
	views := []profileView{
		{ID: "", Accounts: signed, Limits: profileLimits{Claude: full, Codex: more}},
		{ID: "p-a", Accounts: signed, Limits: profileLimits{Claude: more, Codex: some}},
		{ID: "p-b", Accounts: signed, Limits: profileLimits{Claude: some}},
		{ID: "p-c", Limits: profileLimits{Claude: []usageWindow{{Label: "7d", UsedPercent: 0, ResetsAt: at(now, time.Hour)}}}},
	}
	got := recommendProfiles(views, now)
	if got["claude"] != "p-b" || got["codex"] != "p-a" {
		t.Fatalf("recommended = %#v", got)
	}
	// Only accounts at their limit and one with no reading: the unknown one.
	views[1].Limits.Claude, views[2].Limits.Claude = full, nil
	if got := recommendProfiles(views, now); got["claude"] != "p-b" {
		t.Fatalf("with everything else at its limit, recommended = %#v", got)
	}
	// Nothing known at all is nothing to choose by.
	for i := range views {
		views[i].Limits = profileLimits{}
	}
	if got := recommendProfiles(views, now); len(got) != 0 {
		t.Fatalf("with no readings, recommended = %#v", got)
	}
	if got := recommendProfiles(views[:1], now); len(got) != 0 {
		t.Fatalf("one profile has nothing to choose between: %#v", got)
	}
}

func TestProfileListCarriesLimitsAndTheRecommendation(t *testing.T) {
	e := newSessionEnv(t)
	work := e.addProfile("Business")
	used := map[string]int{"home-token": 90, "work-token": 10}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")[len("Bearer "):]
		fmt.Fprintf(w, `{"five_hour":{"utilization":5,"resets_at":%q},"seven_day":{"utilization":%d,"resets_at":%q}}`,
			at(time.Now(), 2*time.Hour), used[token], at(time.Now(), 48*time.Hour))
	}))
	defer api.Close()
	e.srv.usageURL, e.srv.usageHTTP = api.URL, api.Client()
	for profile, token := range map[string]string{"": "home-token", work: "work-token"} {
		dir := harnesses.ClaudeConfigDirFor(e.srv.dataDir, profile)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			".credentials.json": fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"expiresAt":4102444800000}}`, token),
			".claude.json":      fmt.Sprintf(`{"hasCompletedOnboarding":true,"oauthAccount":{"emailAddress":"%s@example.invalid"}}`, token),
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	res, payload := e.do(t, e.client, "GET", "/api/profiles", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("profiles: %d", res.StatusCode)
	}
	recommended, _ := payload["recommended"].(map[string]any)
	if recommended["claude"] != work {
		t.Fatalf("recommended = %#v", payload["recommended"])
	}
	list, _ := payload["profiles"].([]any)
	second, _ := list[1].(map[string]any)
	limits, _ := second["limits"].(map[string]any)
	if windows, _ := limits["claude"].([]any); len(windows) != 2 {
		t.Fatalf("the profile's limits are %#v", second["limits"])
	}
}
