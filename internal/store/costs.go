package store

import (
	"database/sql"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// The cost ledger is what every conversation a CLI had on this machine cost:
// one row per conversation (cost_convs) and its spend by UTC hour and model
// (cost_hours). It outlives the transcripts it was read from: a CLI that
// prunes its old sessions does not take their cost with it.

// CostKey is the ledger's name for one conversation of one CLI.
func CostKey(harness, convID string) string { return harness + "/" + convID }

// CostConversation is one conversation's row: which CLI, which account, which
// folder, and the session it ran in if Socrates started it.
type CostConversation struct {
	Harness   string `json:"harness"`
	ConvID    string `json:"conv_id"`
	Profile   string `json:"profile,omitempty"`
	Workdir   string `json:"workdir,omitempty"`
	Title     string `json:"title,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	FirstAt   int64  `json:"first_at"`
	LastAt    int64  `json:"last_at"`
}

// Key is the conversation's ledger key.
func (c CostConversation) Key() string { return CostKey(c.Harness, c.ConvID) }

// CostRow is one hour's spend on one model. Hour is the Unix time divided by
// 3600, so a page can put it into whatever day its own time zone says.
// Unpriced counts the requests whose model had no known price.
type CostRow struct {
	Hour       int64   `json:"hour"`
	Model      string  `json:"model"`
	CostUSD    float64 `json:"cost_usd"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Requests   int64   `json:"requests"`
	Unpriced   int64   `json:"unpriced,omitempty"`
}

// CostLedger is a conversation with its hours.
type CostLedger struct {
	CostConversation
	Rows []CostRow `json:"rows"`
}

func (l *CostLedger) normalise() {
	sort.Slice(l.Rows, func(i, j int) bool {
		if l.Rows[i].Hour != l.Rows[j].Hour {
			return l.Rows[i].Hour < l.Rows[j].Hour
		}
		return l.Rows[i].Model < l.Rows[j].Model
	})
	for i := range l.Rows {
		// Sub-microdollar noise is not a change worth a write.
		l.Rows[i].CostUSD = math.Round(l.Rows[i].CostUSD*1e6) / 1e6
	}
	if l.Rows == nil {
		l.Rows = []CostRow{}
	}
}

// GetCostLedger reads one conversation's ledger.
func (s *Store) GetCostLedger(key string) (CostLedger, error) {
	return getCostLedger(s.db, key)
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func getCostLedger(q queryer, key string) (CostLedger, error) {
	var l CostLedger
	err := q.QueryRow(`SELECT harness, conv_id, profile, workdir, title, session_id, first_at, last_at
		FROM cost_convs WHERE key = ?`, key).Scan(&l.Harness, &l.ConvID, &l.Profile, &l.Workdir,
		&l.Title, &l.SessionID, &l.FirstAt, &l.LastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	rows, err := q.Query(`SELECT hour, model, cost_usd, input, output, cache_read, cache_write, requests, unpriced
		FROM cost_hours WHERE key = ? ORDER BY hour, model`, key)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	for rows.Next() {
		var r CostRow
		if err := rows.Scan(&r.Hour, &r.Model, &r.CostUSD, &r.Input, &r.Output, &r.CacheRead,
			&r.CacheWrite, &r.Requests, &r.Unpriced); err != nil {
			return l, err
		}
		l.Rows = append(l.Rows, r)
	}
	l.normalise()
	return l, rows.Err()
}

// PutCostLedger replaces a conversation's ledger with l, and reports whether
// that changed anything. An unchanged ledger is not written, so a scan that
// finds nothing new changes nothing.
func (s *Store) PutCostLedger(l CostLedger) (bool, error) {
	if l.Harness == "" || l.ConvID == "" || strings.Contains(l.Harness, "/") {
		return false, errors.New("a cost ledger needs a harness and a conversation id")
	}
	l.normalise()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := l.Key()
	before, err := getCostLedger(s.db, key)
	if err == nil && reflect.DeepEqual(before, l) {
		return false, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err := writeCostLedger(s.db, l); err != nil {
		return false, err
	}
	return true, nil
}

func writeCostLedger(db *sql.DB, l CostLedger) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := l.Key()
	if _, err := tx.Exec(`INSERT INTO cost_convs(key, harness, conv_id, profile, workdir, title, session_id, first_at, last_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET harness = excluded.harness, conv_id = excluded.conv_id,
		profile = excluded.profile, workdir = excluded.workdir, title = excluded.title,
		session_id = excluded.session_id, first_at = excluded.first_at, last_at = excluded.last_at`,
		key, l.Harness, l.ConvID, l.Profile, l.Workdir, l.Title, l.SessionID, l.FirstAt, l.LastAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cost_hours WHERE key = ?`, key); err != nil {
		return err
	}
	for _, r := range l.Rows {
		if _, err := tx.Exec(`INSERT INTO cost_hours(key, hour, model, cost_usd, input, output, cache_read, cache_write, requests, unpriced)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(key, hour, model) DO UPDATE SET cost_usd = cost_usd + excluded.cost_usd,
			input = input + excluded.input, output = output + excluded.output,
			cache_read = cache_read + excluded.cache_read, cache_write = cache_write + excluded.cache_write,
			requests = requests + excluded.requests, unpriced = unpriced + excluded.unpriced`,
			key, r.Hour, r.Model, r.CostUSD, r.Input, r.Output, r.CacheRead, r.CacheWrite, r.Requests, r.Unpriced); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteCostLedger removes a conversation's ledger, and reports whether there
// was one: a conversation another source counts now (a chat's journal) must
// not be counted twice.
func (s *Store) DeleteCostLedger(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM cost_convs WHERE key = ?`, key)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM cost_hours WHERE key = ?`, key); err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// CostKeysWithPrefix is the key of every ledger whose key begins with
// prefix: the ledgers one source writes under names of its own (a chat's,
// one per account), so that it can take away the ones it no longer writes.
func (s *Store) CostKeysWithPrefix(prefix string) ([]string, error) {
	// SQLite's substr counts characters, not bytes.
	rows, err := s.db.Query(`SELECT key FROM cost_convs WHERE substr(key, 1, ?) = ? ORDER BY key`,
		utf8.RuneCountInString(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// ListCostLedgers returns every conversation and its hours, the whole ledger.
func (s *Store) ListCostLedgers() ([]CostLedger, error) {
	rows, err := s.db.Query(`SELECT key, harness, conv_id, profile, workdir, title, session_id, first_at, last_at
		FROM cost_convs ORDER BY key`)
	if err != nil {
		return nil, err
	}
	var out []CostLedger
	index := map[string]int{}
	for rows.Next() {
		var key string
		var l CostLedger
		if err := rows.Scan(&key, &l.Harness, &l.ConvID, &l.Profile, &l.Workdir, &l.Title,
			&l.SessionID, &l.FirstAt, &l.LastAt); err != nil {
			rows.Close()
			return nil, err
		}
		index[key] = len(out)
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hours, err := s.db.Query(`SELECT key, hour, model, cost_usd, input, output, cache_read, cache_write, requests, unpriced
		FROM cost_hours ORDER BY key, hour, model`)
	if err != nil {
		return nil, err
	}
	defer hours.Close()
	for hours.Next() {
		var key string
		var r CostRow
		if err := hours.Scan(&key, &r.Hour, &r.Model, &r.CostUSD, &r.Input, &r.Output, &r.CacheRead,
			&r.CacheWrite, &r.Requests, &r.Unpriced); err != nil {
			return nil, err
		}
		if i, ok := index[key]; ok {
			out[i].Rows = append(out[i].Rows, r)
		}
	}
	return out, hours.Err()
}

// CostFingerprints is the state of the transcripts each conversation's ledger
// was last computed from, by key.
func (s *Store) CostFingerprints() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, fingerprint FROM cost_seen`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, fp string
		if err := rows.Scan(&key, &fp); err != nil {
			return nil, err
		}
		out[key] = fp
	}
	return out, rows.Err()
}

// SetCostFingerprint records the transcripts a ledger was computed from. It is
// about this disk only: no revision.
func (s *Store) SetCostFingerprint(key, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO cost_seen(key, fingerprint) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET fingerprint = excluded.fingerprint`, key, fingerprint)
	return err
}

// SetCostMeta brings a conversation's folder, title and session up to date
// with the session it ran in, and reports whether that changed anything. A
// conversation the ledger does not have is left alone.
func (s *Store) SetCostMeta(key, workdir, title, sessionID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE cost_convs SET workdir = ?, title = ?, session_id = ?
		WHERE key = ? AND (workdir <> ? OR title <> ? OR session_id <> ?)`,
		workdir, title, sessionID, key, workdir, title, sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	return true, nil
}
