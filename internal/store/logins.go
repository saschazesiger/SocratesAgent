package store

import "time"

// A login is a signed in browser: a random token in a cookie and the moment it
// stops being accepted. The table used to be called `sessions`; it gave the
// name up to the terminals, which is what a session means in this app now. The
// cookie is still called socrates_session, because that is a cookie and not a
// table, and renaming it would sign everybody out for no gain.
//
// The whole table is one object in the bucket (LoginsObject), so every write
// below marks that one name: a browser signed in on this machine stays signed
// in on the machine that restores the bucket.

// CreateLogin stores a login token.
func (s *Store) CreateLogin(token string, ttl time.Duration) error {
	s.lock()
	defer s.unlock()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO logins(token, created_at, expires_at) VALUES(?, ?, ?)`,
		token, ts, ts+ttl.Milliseconds())
	if err == nil {
		s.touch(LoginsObject)
	}
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
	return s.deleteLogins(`DELETE FROM logins WHERE token = ?`, token)
}

// DeleteAllLogins invalidates every login, which is what changing the password
// has to do.
func (s *Store) DeleteAllLogins() error {
	return s.deleteLogins(`DELETE FROM logins`)
}

// PurgeExpiredLogins removes stale rows.
func (s *Store) PurgeExpiredLogins() error {
	return s.deleteLogins(`DELETE FROM logins WHERE expires_at <= ?`, now())
}

// deleteLogins runs one delete and marks the table changed only when it
// removed something. The purge runs on a timer, and a timer that found
// nothing to purge must not cost an upload every time it fires.
func (s *Store) deleteLogins(query string, args ...any) error {
	s.lock()
	defer s.unlock()
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		s.touch(LoginsObject)
	}
	return nil
}
