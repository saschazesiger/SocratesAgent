package server

// The usage reader deliberately uses the files and endpoint the two CLIs use
// themselves. The shapes follow the small, proven readers in
// ArrivaRUS/claude-codex-limits and Stargod-0812/starline; keeping the parser
// here avoids installing either application or adding a runtime dependency.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

const usageTailBytes int64 = 2 << 20

type usageWindow struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

type usageLimits struct {
	Windows []usageWindow `json:"windows,omitempty"`
}

type sessionUsageView struct {
	Windows       []usageWindow `json:"windows,omitempty"`
	CostUSD       *float64      `json:"cost_usd,omitempty"`
	CostEstimated bool          `json:"cost_estimated,omitempty"`
}

func (s *Server) handleSessionUsage(w http.ResponseWriter, r *http.Request) {
	row, err := s.store.GetSession(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	view := sessionUsageView{}
	switch row.Harness {
	case "claude":
		view.Windows = s.claudeLimits(r.Context()).Windows
		view.CostUSD = claudeSessionCost(row.CLISessionID)
	case "codex":
		path := codexRollout(row.CLISessionID)
		view.Windows, view.CostUSD = codexUsage(path, row.Model)
		view.CostEstimated = view.CostUSD != nil
	}
	writeJSON(w, http.StatusOK, view)
}

// claudeLimits caches one account-wide reading for a minute. A missing or
// expired login simply yields no windows; Claude Code refreshes its own login
// the next time it works, so Socrates never races it by writing credentials.
func (s *Server) claudeLimits(ctx context.Context) usageLimits {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if time.Since(s.claudeUsageAt) < time.Minute {
		return s.claudeUsage
	}

	var creds struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	raw, err := readClaudeCredentials(ctx)
	if err != nil || json.Unmarshal(raw, &creds) != nil || creds.OAuth.AccessToken == "" ||
		(creds.OAuth.ExpiresAt > 0 && creds.OAuth.ExpiresAt <= time.Now().UnixMilli()) {
		s.claudeUsageAt = time.Now()
		return s.claudeUsage
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.usageURL, nil)
	if err != nil {
		return s.claudeUsage
	}
	req.Header.Set("Authorization", "Bearer "+creds.OAuth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := s.usageHTTP.Do(req)
	if err != nil {
		return s.claudeUsage
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s.claudeUsage
	}
	var payload struct {
		FiveHour struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    string   `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    string   `json:"resets_at"`
		} `json:"seven_day"`
		Limits []struct {
			Kind     string  `json:"kind"`
			Percent  float64 `json:"percent"`
			ResetsAt string  `json:"resets_at"`
			Scope    struct {
				Model struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			} `json:"scope"`
		} `json:"limits"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload) != nil {
		return s.claudeUsage
	}
	limits := usageLimits{}
	if payload.FiveHour.Utilization != nil {
		limits.Windows = append(limits.Windows, usageWindow{Label: "5h", UsedPercent: *payload.FiveHour.Utilization, ResetsAt: payload.FiveHour.ResetsAt})
	}
	if payload.SevenDay.Utilization != nil {
		limits.Windows = append(limits.Windows, usageWindow{Label: "7d", UsedPercent: *payload.SevenDay.Utilization, ResetsAt: payload.SevenDay.ResetsAt})
	}
	for _, limit := range payload.Limits {
		if limit.Kind == "weekly_scoped" && strings.Contains(strings.ToLower(limit.Scope.Model.DisplayName), "fable") {
			limits.Windows = append(limits.Windows, usageWindow{
				Label: "Fable", UsedPercent: limit.Percent, ResetsAt: limit.ResetsAt,
			})
			break
		}
	}
	s.claudeUsage, s.claudeUsageAt = limits, time.Now()
	return limits
}

func readClaudeCredentials(ctx context.Context) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(harnesses.ClaudeConfigDir(), ".credentials.json"))
	if err == nil || runtime.GOOS != "darwin" {
		return raw, err
	}
	// Claude Code uses the login keychain on macOS. `security` is part of the
	// OS and prints the same JSON document Linux keeps in .credentials.json.
	return exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
}

func claudeSessionCost(id string) *float64 {
	if id == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(harnesses.ClaudeConfigDir(), "projects", "*", id+".jsonl"))
	for _, path := range matches {
		var row struct {
			Type         string  `json:"type"`
			TotalCostUSD float64 `json:"totalCostUSD"`
		}
		if lastJSON(path, func(line []byte) bool {
			if !bytes.Contains(line, []byte(`"type":"cost-state"`)) {
				return false
			}
			return json.Unmarshal(line, &row) == nil && row.Type == "cost-state"
		}) {
			cost := row.TotalCostUSD
			return &cost
		}
	}
	return nil
}

func codexRollout(id string) string {
	if id == "" {
		return ""
	}
	root := filepath.Join(harnesses.CodexHome(), "sessions")
	var found string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") && strings.Contains(entry.Name(), id) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func codexUsage(path, model string) ([]usageWindow, *float64) {
	if path == "" {
		return nil, nil
	}
	var windows []usageWindow
	var tokens struct {
		Type    string `json:"type"`
		Payload struct {
			Thread struct {
				Input      float64 `json:"input_tokens"`
				Cached     float64 `json:"cached_input_tokens"`
				CacheWrite float64 `json:"cache_write_input_tokens"`
				Output     float64 `json:"output_tokens"`
			} `json:"thread_token_usage"`
		} `json:"payload"`
	}
	lastJSON(path, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"rate_limits"`)) {
			return false
		}
		var row struct {
			Payload struct {
				RateLimits struct {
					Primary   codexWindow `json:"primary"`
					Secondary codexWindow `json:"secondary"`
				} `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return false
		}
		for _, win := range []codexWindow{row.Payload.RateLimits.Primary, row.Payload.RateLimits.Secondary} {
			if win.UsedPercent == nil {
				continue
			}
			label := "5h"
			if win.WindowMinutes >= 2*24*60 {
				label = "7d"
			}
			windows = append(windows, usageWindow{Label: label, UsedPercent: *win.UsedPercent, ResetsAt: epochTime(win.ResetsAt)})
		}
		return len(windows) > 0
	})

	if !lastJSON(path, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"type":"token_usage_record"`)) {
			return false
		}
		return json.Unmarshal(line, &tokens) == nil && tokens.Type == "token_usage_record"
	}) {
		return windows, nil
	}
	// Codex writes the effective model at the start of each turn. It may differ
	// from the model the session was created with after an in-TUI model change.
	effectiveModel := model
	lastJSON(path, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"type":"turn_context"`)) {
			return false
		}
		var turn struct {
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &turn) != nil || turn.Payload.Model == "" {
			return false
		}
		effectiveModel = turn.Payload.Model
		return true
	})
	price, ok := codexPrices[effectiveModel]
	if !ok {
		return windows, nil
	}
	u := tokens.Payload.Thread
	plain := max(0, u.Input-u.Cached-u.CacheWrite)
	cost := (plain*price.Input + u.Cached*price.Cached + u.CacheWrite*price.CacheWrite + u.Output*price.Output) / 1e6
	return windows, &cost
}

type codexWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes float64  `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"`
}

type tokenPrice struct{ Input, Cached, CacheWrite, Output float64 }

// USD per million tokens. These are the published ChatGPT Work/Codex rates;
// aliases keep old sessions useful while unknown future models omit cost.
var codexPrices = map[string]tokenPrice{
	"gpt-6-astra":   {10, 1, 12.5, 50},
	"gpt-5.6-sol":   {4, .4, 5, 20},
	"gpt-5.6":       {4, .4, 5, 20},
	"gpt-5.6-terra": {2, .2, 2.5, 12},
	"gpt-5.6-luna":  {.2, .02, .25, 1.2},
	"gpt-5.5":       {5, .5, 6.25, 30},
	"gpt-5.4":       {2.5, .25, 3.125, 15},
	"gpt-5.4-mini":  {.75, .075, .9375, 4.5},
	"gpt-5.3-codex": {1.75, .175, 2.1875, 14},
}

func epochTime(epoch int64) string {
	if epoch <= 0 {
		return ""
	}
	return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
}

// lastJSON walks complete lines from the end of a bounded tail. All fields we
// need are emitted after each answer, so old transcript history is irrelevant.
func lastJSON(path string, accept func([]byte) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	start := max(int64(0), info.Size()-usageTailBytes)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return false
	}
	lines := bytes.Split(buf, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if accept(bytes.TrimSpace(lines[i])) {
			return true
		}
	}
	return false
}
