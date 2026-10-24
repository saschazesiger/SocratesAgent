package accounts

import (
	"encoding/json"
	"errors"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// Keyring seals and opens the API keys of the accounts with the data
// directory's own key (paths.SecretsKey). A sealed key is bound to its
// account: moved onto another row it does not open.
type Keyring struct {
	key *vault.Key
}

// OpenKeyring is the keyring of a data directory, making its key the first
// time.
func OpenKeyring(dataDir string) (*Keyring, error) {
	key, err := vault.LoadOrCreateKeyFile(paths.New(dataDir).SecretsKey())
	if err != nil {
		return nil, err
	}
	return &Keyring{key: key}, nil
}

// Key is the data directory's key, for sealing a chat's environment
// (agenthost.SealEnv).
func (k *Keyring) Key() *vault.Key { return k.key }

func secretObject(accountID string) string { return "account:" + accountID }

// Seal seals an account's API key for the store.
func (k *Keyring) Seal(accountID, apiKey string) ([]byte, error) {
	if apiKey == "" {
		return nil, errors.New("there is no key to keep")
	}
	return vault.Seal(k.key, secretObject(accountID), []byte(apiKey))
}

// Open is the API key of an account, from what Seal made of it; "" for an
// account without one.
func (k *Keyring) Open(accountID string, sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	plain, err := vault.Open(k.key, secretObject(accountID), sealed)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// jsonObject is {"name": v} encoded.
func jsonObject(name string, v any) (string, error) {
	raw, err := json.Marshal(map[string]any{name: v})
	return string(raw), err
}
