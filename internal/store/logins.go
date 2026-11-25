package store

// A login was a signed in browser before the Z-Dash server kept the sessions
// (internal/server/app.go): a random token in a cookie and the moment it
// stopped being accepted. The table is only read now, once, to carry the
// logins of that build over; nothing writes it.

// Login is one row of the old logins table.
type Login struct {
	Token     string
	ExpiresAt int64 // Unix ms
}

// Logins lists the logins of the build before the Z-Dash server that have not
// expired.
func (s *Store) Logins() ([]Login, error) {
	rows, err := s.db.Query(`SELECT token, expires_at FROM logins WHERE expires_at > ?`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Login
	for rows.Next() {
		var l Login
		if err := rows.Scan(&l.Token, &l.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AddLogin writes a row of the old logins table, for the tests of carrying
// them over.
func (s *Store) AddLogin(token string, expiresAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO logins(token, created_at, expires_at) VALUES(?, ?, ?)`, token, now(), expiresAt)
	return err
}
