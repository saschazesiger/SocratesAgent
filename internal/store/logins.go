package store

import "time"

// A login is a signed in browser: a random token in a cookie and the moment it
// stops being accepted. The table used to be called `sessions`; it gave the
// name up to the terminals, which is what a session means in this app now. The
// cookie is still called socrates_session, because that is a cookie and not a
// table, and renaming it would sign everybody out for no gain.

// CreateLogin stores a login token.
func (s *Store) CreateLogin(token string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO logins(token, created_at, expires_at) VALUES(?, ?, ?)`,
		token, ts, ts+ttl.Milliseconds())
	return err
}

// ValidLogin reports whether a token is known and not expired.
func (s *Store) ValidLogin(token string) bool {
	if token == "" {
		return false
	}
	var exp int64
	if err := s.db.QueryRow(`SELECT expires_at FROM logins WHERE token = ?`, token).Scan(&exp); err != nil {
		return false
	}
	return exp > now()
}

// DeleteLogin logs a token out.
func (s *Store) DeleteLogin(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM logins WHERE token = ?`, token)
	return err
}

// DeleteAllLogins invalidates every login, which is what changing the password
// has to do.
func (s *Store) DeleteAllLogins() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM logins`)
	return err
}

// PurgeExpiredLogins removes stale rows.
func (s *Store) PurgeExpiredLogins() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM logins WHERE expires_at <= ?`, now())
	return err
}
