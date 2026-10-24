package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Account is one login a chat can run on: one agent's subscription or API key
// (REBUILD.md §4.1). The login itself is not here. A subscription is in the
// account's folder, written by the agent's own sign-in; an API key is Secret,
// sealed by internal/accounts under the data directory's key, and nothing
// that reads an Account outside that package ever sees it opened.
type Account struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Order is the place the person dragged the account to; CreatedAt breaks
	// ties.
	Order     int   `json:"order"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
	// State is whether the account can be used (internal/accounts names the
	// values), and StateDetail why not when it cannot.
	State       string `json:"state"`
	StateDetail string `json:"state_detail,omitempty"`
	// Email and Plan are what the agent last said about the login.
	Email string `json:"email,omitempty"`
	Plan  string `json:"plan,omitempty"`
	// Provider is the OpenCode provider an API key is for.
	Provider string `json:"provider,omitempty"`
	// Origin is the folder the migration moved this account's login out of,
	// which a link there still points at; empty for an account made here.
	Origin string `json:"origin,omitempty"`
	// Limits is the last reading of the account's usage windows, LimitsAt
	// when it was taken, and LimitedUntil when a limit the account ran into
	// renews (Unix ms; zero for none).
	Limits       json.RawMessage `json:"limits,omitempty"`
	LimitsAt     int64           `json:"limits_at,omitempty"`
	LimitedUntil int64           `json:"limited_until,omitempty"`
	// Secret is the sealed API key. It is never encoded.
	Secret []byte `json:"-"`
}

const accountCols = `id, agent, label, kind, created_at, sort_order, state, state_detail, email, plan,
                     provider, origin, limits, limits_at, limited_until, secret, updated_at`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	a := &Account{}
	var limits string
	err := row.Scan(&a.ID, &a.Agent, &a.Label, &a.Kind, &a.CreatedAt, &a.Order, &a.State, &a.StateDetail,
		&a.Email, &a.Plan, &a.Provider, &a.Origin, &limits, &a.LimitsAt, &a.LimitedUntil, &a.Secret, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if limits != "" {
		a.Limits = json.RawMessage(limits)
	}
	return a, nil
}

// CreateAccount inserts an account at the end of the list. An account with
// the Origin of one already there is not made twice: the caller gets the one
// that exists, which is what lets an interrupted migration run again.
func (s *Store) CreateAccount(a *Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Origin != "" {
		existing, err := scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE origin = ?`, a.Origin))
		if err == nil {
			*a = *existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if a.CreatedAt == 0 {
		a.CreatedAt = now()
	}
	a.UpdatedAt = a.CreatedAt
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(sort_order) FROM accounts`).Scan(&last); err != nil {
		return err
	}
	a.Order = int(last.Int64) + 1
	limits := ""
	if len(a.Limits) > 0 {
		limits = string(a.Limits)
	}
	_, err := s.db.Exec(`INSERT INTO accounts(`+accountCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Agent, a.Label, a.Kind, a.CreatedAt, a.Order, a.State, a.StateDetail, a.Email, a.Plan,
		a.Provider, a.Origin, limits, a.LimitsAt, a.LimitedUntil, a.Secret, a.UpdatedAt)
	return err
}

// GetAccount loads one account.
func (s *Store) GetAccount(id string) (*Account, error) {
	return scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
}

// ListAccounts is every account in the order the person put them in.
func (s *Store) ListAccounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY sort_order, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// updateAccount runs one statement against one account and stamps it.
func (s *Store) updateAccount(id, query string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	args = append(args, now(), id)
	res, err := s.db.Exec(`UPDATE accounts SET `+query+`, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameAccount gives an account a new label.
func (s *Store) RenameAccount(id, label string) error {
	return s.updateAccount(id, `label = ?`, label)
}

// SetAccountState records whether an account can be used, and why not.
func (s *Store) SetAccountState(id, state, detail string) error {
	return s.updateAccount(id, `state = ?, state_detail = ?`, state, detail)
}

// SetAccountIdentity records the e-mail address and plan the agent reported
// for an account. An empty value keeps the one already known.
func (s *Store) SetAccountIdentity(id, email, plan string) error {
	return s.updateAccount(id, `email = CASE WHEN ? = '' THEN email ELSE ? END,
		plan = CASE WHEN ? = '' THEN plan ELSE ? END`, email, email, plan, plan)
}

// SetAccountLimits stores a reading of an account's usage windows.
func (s *Store) SetAccountLimits(id string, limits json.RawMessage, at int64) error {
	return s.updateAccount(id, `limits = ?, limits_at = ?`, string(limits), at)
}

// SetAccountLimitedUntil records when the limit an account ran into renews;
// zero clears it.
func (s *Store) SetAccountLimitedUntil(id string, until int64) error {
	return s.updateAccount(id, `limited_until = ?`, until)
}

// SetAccountSecret stores an account's sealed API key, and the provider it is
// for.
func (s *Store) SetAccountSecret(id string, sealed []byte, provider string) error {
	return s.updateAccount(id, `secret = ?, provider = ?`, sealed, provider)
}

// DeleteAccount removes an account's row.
func (s *Store) DeleteAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrOrderMismatch is an order that does not name every account exactly
// once: it was made from a list that has changed since.
var ErrOrderMismatch = errors.New("the accounts have changed")

// ReorderAccounts puts the accounts in the order given, which has to name
// every account exactly once.
func (s *Store) ReorderAccounts(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return ErrOrderMismatch
		}
		seen[id] = true
	}
	if len(seen) != count {
		return ErrOrderMismatch
	}
	stamp := now()
	for i, id := range ids {
		res, err := tx.Exec(`UPDATE accounts SET sort_order = ?, updated_at = ? WHERE id = ?`, i+1, stamp, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrOrderMismatch
		}
	}
	return tx.Commit()
}

// SetSessionOptions replaces a session's options: a chat's account and its
// automatic switch live there. It is not a status change and does not move
// the row.
func (s *Store) SetSessionOptions(id string, options json.RawMessage) error {
	if len(options) == 0 {
		options = json.RawMessage(`{}`)
	}
	if !json.Valid(options) {
		return errors.New("session options are not valid JSON")
	}
	return s.update(id, `options = ?`, string(options))
}

// CopyTo writes a consistent copy of the whole database to dest, which must
// not exist yet: what a backup of the data directory takes of it.
func (s *Store) CopyTo(dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`VACUUM INTO ?`, dest)
	return err
}
