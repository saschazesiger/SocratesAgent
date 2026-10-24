package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The account methods, as codex 0.164 answers them (verified against the
// real app-server): account/login/start for an API key (written into
// $CODEX_HOME/auth.json at once) and for a ChatGPT device code (a code to
// type at a page; the login lands FAKECODEX_LOGIN_MS later, 1500 by
// default, unless it is cancelled), account/login/cancel, account/read, and
// account/rateLimits/read (25 % of five hours, 60 % of the week, once signed
// in). Without FAKECODEX_ACCOUNT, the account the encrypted reasoning is
// bound to is the one auth.json holds, so two homes signed in to two
// accounts refuse each other's reasoning as the real model does.

// deviceEmail is the address a device-code login signs in as.
const deviceEmail = "device@example.com"

var logins sync.Map

// accountOf is the account auth.json in home is signed in to, "" for none.
func accountOf(home string) string {
	auth := readAuth(home)
	switch {
	case auth == nil:
		return ""
	case auth.Key != "":
		return "key:" + auth.Key
	default:
		return auth.Email
	}
}

type fakeAuth struct {
	Key   string
	Email string
}

func readAuth(home string) *fakeAuth {
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Key    string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	if doc.Tokens == nil {
		if doc.Key == "" {
			return nil
		}
		return &fakeAuth{Key: doc.Key}
	}
	return &fakeAuth{Email: deviceEmail}
}

func writeAuth(home string, doc any) error {
	raw, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600)
}

func (s *server) accountMethod(fr frame, p map[string]json.RawMessage) bool {
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(p[k], &v)
		return v
	}
	switch fr.Method {
	case "account/login/start":
		switch str("type") {
		case "apiKey":
			if err := writeAuth(s.home, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": str("apiKey")}); err != nil {
				s.fail(fr.ID, -32603, err.Error(), nil)
				return true
			}
			s.respond(fr.ID, map[string]any{"type": "apiKey"})
			s.notify("account/login/completed", map[string]any{"loginId": nil, "success": true, "error": nil})
			s.notify("account/updated", map[string]any{"authMode": "apikey", "planType": nil})
		case "chatgptDeviceCode":
			var b [8]byte
			_, _ = rand.Read(b[:])
			id := hex.EncodeToString(b[:])
			cancel := make(chan struct{})
			logins.Store(id, cancel)
			s.respond(fr.ID, map[string]any{"type": "chatgptDeviceCode", "loginId": id,
				"verificationUrl": "https://auth.openai.com/codex/device", "userCode": "FAKE-" + id[:4]})
			wait := 1500 * time.Millisecond
			if ms, err := strconv.Atoi(os.Getenv("FAKECODEX_LOGIN_MS")); err == nil {
				wait = time.Duration(ms) * time.Millisecond
			}
			go func() {
				select {
				case <-cancel:
					s.notify("account/login/completed", map[string]any{"loginId": id, "success": false,
						"error": "Login was not completed"})
					return
				case <-time.After(wait):
				}
				logins.Delete(id)
				claims, _ := json.Marshal(map[string]any{"email": deviceEmail,
					"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
				token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
				err := writeAuth(s.home, map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
					"tokens": map[string]any{"id_token": token, "access_token": "access-" + id, "refresh_token": "refresh-" + id,
						"account_id": "acct-" + id}, "last_refresh": time.Now().UTC().Format(time.RFC3339)})
				if err != nil {
					s.notify("account/login/completed", map[string]any{"loginId": id, "success": false, "error": err.Error()})
					return
				}
				s.notify("account/login/completed", map[string]any{"loginId": id, "success": true, "error": nil})
			}()
		default:
			s.fail(fr.ID, -32600, "fakecodex does not sign in with "+str("type"), nil)
		}
	case "account/login/cancel":
		if c, ok := logins.LoadAndDelete(str("loginId")); ok {
			close(c.(chan struct{}))
			s.respond(fr.ID, map[string]any{"status": "canceled"})
		} else {
			s.respond(fr.ID, map[string]any{"status": "notFound"})
		}
	case "account/read":
		auth := readAuth(s.home)
		switch {
		case auth == nil:
			s.respond(fr.ID, map[string]any{"account": nil, "requiresOpenaiAuth": true})
		case auth.Key != "":
			s.respond(fr.ID, map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": true})
		default:
			s.respond(fr.ID, map[string]any{"account": map[string]any{"type": "chatgpt", "email": auth.Email,
				"planType": "plus"}, "requiresOpenaiAuth": true})
		}
	case "account/rateLimits/read":
		if readAuth(s.home) == nil {
			s.fail(fr.ID, -32600, "codex account authentication required to read rate limits", nil)
			return true
		}
		now := time.Now()
		s.respond(fr.ID, map[string]any{"rateLimits": map[string]any{"limitId": "codex",
			"primary":   map[string]any{"usedPercent": 25, "windowDurationMins": 300, "resetsAt": now.Add(2 * time.Hour).Unix()},
			"secondary": map[string]any{"usedPercent": 60, "windowDurationMins": 10080, "resetsAt": now.Add(72 * time.Hour).Unix()}}})
	default:
		return false
	}
	return true
}
