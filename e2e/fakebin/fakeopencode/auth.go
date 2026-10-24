//go:build !windows

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The provider sign-in of opencode 1.19.4 (verified against the real
// server): GET /provider/auth lists each provider's methods, and an OAuth
// method is authorized and called back. Provider "fake" offers a device-code
// style method ("auto": the callback answers once the person is done, here
// FAKEOPENCODE_LOGIN_MS later, 800 by default), one that takes the code the
// page shows ("code": "good" is the only good one), and an API key. A
// completed sign-in is written into the process's own
// $XDG_DATA_HOME/opencode/auth.json, as OpenCode does.

func (s *srv) providerAuth(w http.ResponseWriter, r *http.Request, parts []string) bool {
	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "auth":
		writeJSON(w, 200, map[string]any{"fake": []any{
			map[string]any{"type": "oauth", "label": "Fake (device code)"},
			map[string]any{"type": "oauth", "label": "Fake (paste a code)", "prompts": []any{
				map[string]any{"type": "text", "key": "workspace", "message": "Workspace", "placeholder": "acme"}}},
			map[string]any{"type": "api", "label": "Manually enter API Key"},
		}})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[2] == "oauth" && parts[3] == "authorize":
		var body struct {
			Method int `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Method == 0 {
			writeJSON(w, 200, map[string]any{"url": "https://example.com/device", "method": "auto",
				"instructions": "Enter code: FAKE-1234"})
		} else {
			writeJSON(w, 200, map[string]any{"url": "https://example.com/authorize", "method": "code",
				"instructions": "Paste the code the page shows."})
		}
	case r.Method == http.MethodPost && len(parts) == 4 && parts[2] == "oauth" && parts[3] == "callback":
		var body struct {
			Method int    `json:"method"`
			Code   string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Method == 0 {
			wait := 800 * time.Millisecond
			if ms, err := strconv.Atoi(os.Getenv("FAKEOPENCODE_LOGIN_MS")); err == nil {
				wait = time.Duration(ms) * time.Millisecond
			}
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
				return true
			}
		} else if body.Code != "good" {
			writeJSON(w, 400, map[string]any{"name": "ProviderAuthOauthCallbackFailed", "data": map[string]any{"message": "bad code"}})
			return true
		}
		if err := writeOAuth(parts[1]); err != nil {
			writeJSON(w, 500, map[string]any{"name": "UnknownError", "data": map[string]any{"message": err.Error()}})
			return true
		}
		writeJSON(w, 200, true)
	default:
		return false
	}
	return true
}

// writeOAuth puts an OAuth login of provider into this process's auth.json,
// keeping the others.
func writeOAuth(provider string) error {
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "opencode", "auth.json")
	auth := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &auth)
	}
	auth[provider] = map[string]any{"type": "oauth", "refresh": "refresh-fake", "access": "access-fake",
		"expires": time.Now().Add(time.Hour).UnixMilli()}
	raw, _ := json.MarshalIndent(auth, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
