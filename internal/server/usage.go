package server

// An account's usage windows, read from the endpoint Claude Code uses itself.
// The shapes follow the small, proven readers in ArrivaRUS/claude-codex-limits
// and Stargod-0812/starline; keeping the parser here avoids installing either
// application or adding a runtime dependency. Codex's come from codex itself
// (accounts.ReadCodexUsage), and every agent's from its chats' rate_limit
// events.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type usageWindow struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

type usageLimits struct {
	Windows []usageWindow `json:"windows,omitempty"`
}

// claudeLimits caches one account-wide reading per Claude Code directory - so
// per account - for a minute. A missing or expired login simply yields no
// windows; Claude Code refreshes its own login the next time it works, so
// Socrates never races it by writing credentials.
//
// The lock is held only around the cache, not across the request: the
// new-session sheet asks for every account at once.
func (s *Server) claudeLimits(ctx context.Context, dir string) usageLimits {
	s.usageMu.Lock()
	if s.claudeUsage == nil {
		s.claudeUsage = map[string]cachedLimits{}
	}
	cached := s.claudeUsage[dir]
	s.usageMu.Unlock()
	if time.Since(cached.at) < time.Minute {
		return cached.limits
	}
	remember := func(limits usageLimits) {
		s.usageMu.Lock()
		s.claudeUsage[dir] = cachedLimits{limits: limits, at: time.Now()}
		s.usageMu.Unlock()
	}

	var creds struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil || json.Unmarshal(raw, &creds) != nil || creds.OAuth.AccessToken == "" ||
		(creds.OAuth.ExpiresAt > 0 && creds.OAuth.ExpiresAt <= time.Now().UnixMilli()) {
		remember(cached.limits)
		return cached.limits
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.usageURL, nil)
	if err != nil {
		return cached.limits
	}
	req.Header.Set("Authorization", "Bearer "+creds.OAuth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := s.usageHTTP.Do(req)
	if err != nil {
		return cached.limits
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cached.limits
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
		return cached.limits
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
	remember(limits)
	return limits
}

// cachedLimits is one reading of an account's limits, and when it was taken.
type cachedLimits struct {
	limits usageLimits
	at     time.Time
}

func epochTime(epoch int64) string {
	if epoch <= 0 {
		return ""
	}
	return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
}
