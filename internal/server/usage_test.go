package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
