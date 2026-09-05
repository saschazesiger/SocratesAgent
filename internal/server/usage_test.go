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

func TestClaudeLimitsAndSessionCost(t *testing.T) {
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
	got := s.claudeLimits(context.Background())
	if len(got.Windows) != 3 || got.Windows[0].Label != "5h" || got.Windows[1].Label != "7d" || got.Windows[2].Label != "Fable" {
		t.Fatalf("windows = %#v", got.Windows)
	}

	project := filepath.Join(home, "projects", "-work")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(project, "conversation.jsonl")
	if err := os.WriteFile(transcript, []byte("{\"type\":\"cost-state\",\"totalCostUSD\":1.2345}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cost := claudeSessionCost("conversation")
	if cost == nil || *cost != 1.2345 {
		t.Fatalf("cost = %v", cost)
	}
}

func TestCodexUsageReadsWeeklyWindowAndEstimatesCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := `{"type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":11,"window_minutes":300,"resets_at":1788607667},"secondary":{"used_percent":24,"window_minutes":10080,"resets_at":1788771352}}}}` + "\n" +
		`{"type":"token_usage_record","payload":{"thread_token_usage":{"input_tokens":1000000,"cached_input_tokens":400000,"cache_write_input_tokens":100000,"output_tokens":200000}}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	windows, cost := codexUsage(path, "an-old-session-model")
	if len(windows) != 2 || windows[0].Label != "5h" || windows[1].Label != "7d" || windows[1].UsedPercent != 24 {
		t.Fatalf("windows = %#v", windows)
	}
	// 500k normal input, 400k cached, 100k cache write, 200k output.
	if cost == nil || *cost != 6.66 {
		t.Fatalf("cost = %v, want 6.66", cost)
	}
}

func TestLastJSONIgnoresPartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	prefix := make([]byte, usageTailBytes)
	for i := range prefix {
		prefix[i] = 'x'
	}
	data := append(prefix, []byte("\n{\"wanted\":true}\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !lastJSON(path, func(line []byte) bool { return string(line) == `{"wanted":true}` }) {
		t.Fatal("did not find final complete line")
	}
}
