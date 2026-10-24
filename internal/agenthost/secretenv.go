package agenthost

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// An API-key account's key reaches the CLI's environment without ever being
// written in the clear: the server seals the variables that carry it with the
// data directory's own key (paths.SecretsKey), for one chat, and puts only
// the sealed form into host.json (Spec.SecretEnv) or a switch_account command
// (Command.SecretEnv). The host opens it with the same key file when it
// starts the CLI. A sealed environment opens only for the chat it was made
// for, so one copied into another chat's spec is refused.

// secretEnvObject is the name a chat's sealed environment is bound to.
func secretEnvObject(chatID string) string { return "chat-env:" + chatID }

// SealEnv seals env, a list of KEY=value entries, for chatID.
func SealEnv(key *vault.Key, chatID string, env []string) (string, error) {
	if len(env) == 0 {
		return "", nil
	}
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return "", errors.New("a secret variable has no name")
		}
	}
	plain, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	sealed, err := vault.Seal(key, secretEnvObject(chatID), plain)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// OpenEnv is SealEnv undone.
func OpenEnv(key *vault.Key, chatID, sealed string) ([]string, error) {
	if sealed == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("the sealed environment is not base64: %w", err)
	}
	plain, err := vault.Open(key, secretEnvObject(chatID), raw)
	if err != nil {
		return nil, err
	}
	var env []string
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("the sealed environment is damaged: %w", err)
	}
	return env, nil
}

// openSecretEnv opens a sealed environment for this host's chat with the data
// directory's key.
func (h *host) openSecretEnv(sealed string) ([]string, error) {
	if sealed == "" {
		return nil, nil
	}
	key, err := vault.LoadKeyFile(paths.New(h.opts.DataDir).SecretsKey())
	if err != nil {
		return nil, fmt.Errorf("the key that opens the account's secret: %w", err)
	}
	return OpenEnv(key, h.opts.ChatID, sealed)
}

// joinEnv is base with extra after it, each variable of extra replacing one
// of the same name in base.
func joinEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	names := map[string]bool{}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		names[k] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !names[k] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
