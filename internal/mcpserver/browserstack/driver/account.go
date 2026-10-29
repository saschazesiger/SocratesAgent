package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// accountsSecret is the pool's secret: every account as user:key, comma
// separated.
const accountsSecret = "BROWSERSTACK_ACCOUNTS"

// Account is a BrowserStack account of the pool: member n is the n-th entry
// of BROWSERSTACK_ACCOUNTS.
type Account struct {
	Member int
	User   string
	Key    string
}

// Plan is an account's Automate plan and its running sessions.
type Plan struct {
	Running, Max, Queued int
}

// ErrUnauthorized is PlanOf's answer when BrowserStack refused the user and
// key (HTTP 401 or 403).
var ErrUnauthorized = errors.New("BrowserStack refused the account's user and key")

// ParseAccount reads one user:key entry; member is its 1-based place in the
// pool.
func ParseAccount(entry string, member int) (Account, error) {
	value := strings.TrimSpace(entry)
	at := strings.Index(value, ":")
	if at <= 0 || at == len(value)-1 {
		return Account{}, fmt.Errorf("%s entry %d is not user:key", accountsSecret, member)
	}
	return Account{Member: member, User: value[:at], Key: value[at+1:]}, nil
}

// ParseAccounts reads user:key,user:key,...; empty entries are skipped.
func ParseAccounts(value string) ([]Account, error) {
	var entries []string
	for _, entry := range strings.Split(value, ",") {
		if strings.TrimSpace(entry) != "" {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s names no account", accountsSecret)
	}
	accounts := make([]Account, 0, len(entries))
	for i, entry := range entries {
		a, err := ParseAccount(entry, i+1)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// BusyMembers are the members whose every parallel session is taken; an
// account without a plan (nil) counts as free.
func BusyMembers(accounts []Account, plans []*Plan) []int {
	var busy []int
	for i, a := range accounts {
		if i < len(plans) && plans[i] != nil && plans[i].Running >= plans[i].Max {
			busy = append(busy, a.Member)
		}
	}
	return busy
}

func basicAuth(a Account) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(a.User+":"+a.Key))
}

// PlanOf asks BrowserStack for the account's plan: ErrUnauthorized when it
// refuses the account, another error when it cannot be reached or answers
// something else.
func PlanOf(ctx context.Context, a Account) (*Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, PlanAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", basicAuth(a))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("BrowserStack's plan API did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("BrowserStack's plan API did not answer: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("BrowserStack's plan API answered http %d", resp.StatusCode)
	}
	var plan struct {
		Running *int `json:"parallel_sessions_running"`
		Max     *int `json:"parallel_sessions_max_allowed"`
		Queued  *int `json:"queued_sessions"`
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		return nil, fmt.Errorf("BrowserStack's plan API answered no plan: %w", err)
	}
	p := &Plan{Max: 1}
	if plan.Running != nil {
		p.Running = *plan.Running
	}
	if plan.Max != nil {
		p.Max = *plan.Max
	}
	if plan.Queued != nil {
		p.Queued = *plan.Queued
	}
	return p, nil
}
