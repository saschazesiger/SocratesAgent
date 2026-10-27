package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClaudeLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{
  "claudeAiOauth":{"accessToken":"secret","expiresAt":4102444800000}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{
  "five_hour":{"utilization":21,"resets_at":"2026-09-05T10:00:00Z"},
  "seven_day":{"utilization":3,"resets_at":"2026-09-11T18:00:00Z"},
  "limits":[{"kind":"weekly_scoped","percent":2,"resets_at":"2026-09-11T18:00:00Z","scope":{"model":{"display_name":"Fable"}}}]
}`)
	}))
	defer api.Close()
	s := &Server{usageHTTP: api.Client(), usageURL: api.URL}
	got := s.claudeLimits(context.Background(), home)
	if len(got.Windows) != 3 || got.Windows[0].Label != "5h" || got.Windows[1].Label != "7d" || got.Windows[2].Label != "Fable" {
		t.Fatalf("windows = %#v", got.Windows)
	}
}

// A Team seat's reading has no week (seven_day is null), only a week for
// Fable; and a reading whose named fields are empty is read from its list.
func TestClaudeLimitsFromTheList(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []usageWindow
	}{
		{"a Team seat", `{
  "five_hour":{"utilization":1.0,"resets_at":"2026-10-27T14:19:59.966219+00:00"},
  "seven_day":null,
  "limits":[
    {"kind":"session","group":"session","percent":1,"resets_at":"2026-10-27T14:19:59.966219+00:00","scope":null},
    {"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2026-11-01T06:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"}}}
  ]}`, []usageWindow{
			{Label: "5h", UsedPercent: 1, ResetsAt: "2026-10-27T14:19:59.966219+00:00"},
			{Label: "Fable", UsedPercent: 0, ResetsAt: "2026-11-01T06:00:00+00:00"},
		}},
		{"only the list", `{
  "five_hour":null,"seven_day":null,
  "limits":[
    {"kind":"session","percent":40,"resets_at":"2026-10-27T11:20:00Z","scope":null},
    {"kind":"weekly_all","percent":11,"resets_at":"2026-11-01T06:00:00Z","scope":null}
  ]}`, []usageWindow{
			{Label: "5h", UsedPercent: 40, ResetsAt: "2026-10-27T11:20:00Z"},
			{Label: "7d", UsedPercent: 11, ResetsAt: "2026-11-01T06:00:00Z"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"secret"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer api.Close()
			s := &Server{usageHTTP: api.Client(), usageURL: api.URL}
			if got := s.claudeLimits(context.Background(), home).Windows; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("windows = %#v", got)
			}
		})
	}
}
