//go:build !windows

package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"syscall"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// SignIn is a server of one account's own, outside any chat, for signing the
// account in to a provider through OpenCode's own flows (REBUILD.md §4.2):
// GET /provider/auth lists every provider's methods, and an OAuth method is
// authorized and called back on a process of the target account, so the
// result is written to that account's auth.json and nowhere else
// (research/06 §3.2). Socrates only relays the page and the code.
type SignIn struct {
	srv *server
	cl  *client
}

// AuthMethod is one way a provider signs in: "oauth" or "api", its label, and
// the questions it asks first (an enterprise URL, a deployment type).
type AuthMethod struct {
	Type    string       `json:"type"`
	Label   string       `json:"label"`
	Prompts []AuthPrompt `json:"prompts,omitempty"`
}

// AuthPrompt is one question of a method: a text with a placeholder, or a
// choice of options.
type AuthPrompt struct {
	Type        string `json:"type"`
	Key         string `json:"key"`
	Message     string `json:"message"`
	Placeholder string `json:"placeholder,omitempty"`
	Options     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
		Hint  string `json:"hint,omitempty"`
	} `json:"options,omitempty"`
	When *struct {
		Key   string `json:"key"`
		Op    string `json:"op"`
		Value string `json:"value"`
	} `json:"when,omitempty"`
}

// Authorization is an OAuth sign-in under way: the page the person opens,
// whether OpenCode finds out by itself when it is done ("auto") or waits for
// the code the page shows ("code"), and what to tell the person.
type Authorization struct {
	URL          string `json:"url"`
	Method       string `json:"method"`
	Instructions string `json:"instructions,omitempty"`
}

// StartSignIn starts a server for account on the data folder's layout, with
// env (the account's environment, internal/accounts.Env) and the binary as
// the dashboard configured it.
func StartSignIn(ctx context.Context, dataDir, account string, env []string, binary string) (*SignIn, error) {
	if account == "" {
		account = DefaultAccount
	}
	layout := NewLayout(dataDir)
	if err := layout.PrepareAccount(account); err != nil {
		return nil, err
	}
	bin, err := agent.ResolveBinary(env, "opencode", binary, envValue(env, BinaryEnv))
	if err != nil {
		return nil, fmt.Errorf("opencode: %w", err)
	}
	spawn := func(cmd *exec.Cmd) error {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd.Start()
	}
	if err := layout.Migrate(ctx, spawn, bin, processEnv(env, layout, account, "", nil)); err != nil {
		return nil, err
	}
	config, err := json.Marshal(map[string]any{"$schema": "https://opencode.ai/config.json", "share": "disabled", "autoupdate": false})
	if err != nil {
		return nil, err
	}
	srv, err := startServer(ctx, spawn, bin, layout.Root, func(pass string) []string {
		return processEnv(env, layout, account, pass, config)
	})
	if err != nil {
		return nil, err
	}
	s := &SignIn{srv: srv, cl: newClient(srv.base, srv.pass, layout.Root)}
	if err := waitHealthy(ctx, s.cl, srv); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Methods is every provider's ways to sign in, by provider id.
func (s *SignIn) Methods(ctx context.Context) (map[string][]AuthMethod, error) {
	var out map[string][]AuthMethod
	if err := s.cl.call(ctx, http.MethodGet, "/provider/auth", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Authorize starts the OAuth method at index of provider, with the answers
// to its prompts.
func (s *SignIn) Authorize(ctx context.Context, provider string, index int, inputs map[string]string) (Authorization, error) {
	var out Authorization
	if provider == "" {
		return out, errors.New("opencode: no provider to sign in to")
	}
	body := map[string]any{"method": index}
	if len(inputs) > 0 {
		body["inputs"] = inputs
	}
	err := s.cl.call(ctx, http.MethodPost, "/provider/"+url.PathEscape(provider)+"/oauth/authorize", body, &out)
	return out, err
}

// Callback finishes the OAuth method at index of provider: with the code the
// page showed for a "code" method, and with none for an "auto" one, which
// OpenCode answers only once the person is done, or has given up. ctx is
// the only bound on it.
func (s *SignIn) Callback(ctx context.Context, provider string, index int, code string) error {
	body := map[string]any{"method": index}
	if code != "" {
		body["code"] = code
	}
	var ok json.RawMessage
	if err := s.cl.long(ctx, http.MethodPost, "/provider/"+url.PathEscape(provider)+"/oauth/callback", body, &ok); err != nil {
		return err
	}
	if string(ok) == "false" {
		return errors.New("opencode: the sign-in was not accepted")
	}
	return nil
}

// Close ends the server.
func (s *SignIn) Close() { s.srv.stop(stopGrace) }
