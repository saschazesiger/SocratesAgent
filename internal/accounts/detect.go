package accounts

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Login is what an account's folder says about its sign-in, read and never
// written: whether there is one, and who it is.
type Login struct {
	SignedIn bool
	Email    string
	Plan     string
}

// Detect reads an account's login. A subscription is signed in when the
// agent's own file is there (Claude Code's .credentials.json or the key its
// console login keeps, Codex's auth.json, an OpenCode provider in auth.json);
// an API-key account when its key is kept (and, for Codex, logged in into its
// home). The files are read, never written: a login belongs to the agent.
func Detect(dataDir string, a *store.Account) Login {
	dir := Folder(dataDir, a.Agent, a.ID)
	switch a.Agent {
	case Claude:
		if a.Kind == APIKey {
			return Login{SignedIn: len(a.Secret) > 0}
		}
		return claudeLogin(dir)
	case Codex:
		return codexLogin(dir)
	case OpenCode:
		if a.Kind == APIKey {
			return Login{SignedIn: len(a.Secret) > 0 && a.Provider != "", Plan: a.Provider}
		}
		providers := openCodeProviders(filepath.Join(dir, "opencode", "auth.json"))
		return Login{SignedIn: len(providers) > 0, Plan: strings.Join(providers, ", ")}
	case Fake:
		// The scripted agent has nothing to sign in to.
		return Login{SignedIn: a.Kind == Subscription || len(a.Secret) > 0}
	}
	return Login{}
}

// claudeLogin is Claude Code's login in a config dir: the OAuth credentials
// (with the plan they are for) or the key a console login keeps, and the
// account's address from .claude.json.
func claudeLogin(dir string) Login {
	var out Login
	var creds struct {
		OAuth *struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, ".credentials.json")); err == nil && json.Unmarshal(raw, &creds) == nil &&
		creds.OAuth != nil && (creds.OAuth.AccessToken != "" || creds.OAuth.RefreshToken != "") {
		out.SignedIn, out.Plan = true, creds.OAuth.SubscriptionType
	}
	var global struct {
		PrimaryAPIKey string `json:"primaryApiKey"`
		OAuthAccount  *struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, ".claude.json")); err == nil && json.Unmarshal(raw, &global) == nil {
		if global.PrimaryAPIKey != "" {
			out.SignedIn = true
		}
		if out.SignedIn && global.OAuthAccount != nil {
			out.Email = strings.TrimSpace(global.OAuthAccount.EmailAddress)
		}
	}
	return out
}

// codexLogin is Codex's login in a home: ChatGPT tokens, whose id token
// names the account and its plan (read, never verified or sent), or an API
// key.
func codexLogin(home string) Login {
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return Login{}
	}
	var doc struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return Login{}
	}
	if doc.Tokens == nil {
		return Login{SignedIn: strings.TrimSpace(doc.APIKey) != ""}
	}
	out := Login{SignedIn: true}
	parts := strings.Split(doc.Tokens.IDToken, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Email string `json:"email"`
				Auth  struct {
					Plan string `json:"chatgpt_plan_type"`
				} `json:"https://api.openai.com/auth"`
			}
			if json.Unmarshal(payload, &claims) == nil {
				out.Email, out.Plan = strings.TrimSpace(claims.Email), claims.Auth.Plan
			}
		}
	}
	return out
}

// openCodeProviders is the providers an OpenCode auth.json has credentials
// for, by name.
func openCodeProviders(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	names := make([]string, 0, len(doc))
	for name := range doc {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
