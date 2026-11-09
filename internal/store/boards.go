package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// A board is a project folder's notes: columns of cards, each card with a
// title, a subtitle and one of the folder's named colours (server boards.go
// says what a document may hold). There is one per folder, keyed by the
// folder's path, and it lives in the kv table under boardPrefix: a few
// kilobytes of JSON that is always read and written whole, which a table of
// its own would only split up again. Being in the database, it is in every
// backup of the data directory (internal/backup) and comes back with a
// restore.

// boardPrefix starts the kv key of a folder's board.
const boardPrefix = "board:"

// Board is a folder's board as it is stored.
type Board struct {
	Folder string `json:"folder"`
	// Doc is the board itself, already checked by the server.
	Doc json.RawMessage `json:"board"`
	// Rev counts saves, starting at one; a folder without a board is at
	// zero. A save names the revision it was based on, and only a save
	// based on the current one is taken, as with a draft.
	Rev       int64 `json:"rev"`
	UpdatedAt int64 `json:"updated_at"`
}

// GetBoard returns a folder's board, or ErrNotFound when it has none yet.
func (s *Store) GetBoard(folder string) (Board, error) {
	return getBoard(s.db, folder)
}

func getBoard(q interface {
	QueryRow(string, ...any) *sql.Row
}, folder string) (Board, error) {
	var raw string
	err := q.QueryRow(`SELECT value FROM kv WHERE key = ?`, boardPrefix+folder).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Board{Folder: folder}, ErrNotFound
	}
	if err != nil {
		return Board{Folder: folder}, err
	}
	var b Board
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return Board{Folder: folder}, err
	}
	b.Folder = folder
	return b, nil
}

// SaveBoard stores a folder's board, if baseRev is the revision it was
// edited from, and returns what was stored. A save from an older revision is
// refused with ErrConflict and the current board: another device saved in
// between, and the page shows what is there now rather than overwriting it.
func (s *Store) SaveBoard(folder string, doc json.RawMessage, baseRev int64) (Board, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Board{}, err
	}
	defer tx.Rollback()
	current, err := getBoard(tx, folder)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Board{}, err
	}
	if baseRev != current.Rev {
		return current, ErrConflict
	}
	next := Board{Folder: folder, Doc: doc, Rev: current.Rev + 1, UpdatedAt: now()}
	raw, err := json.Marshal(next)
	if err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(`INSERT INTO kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, boardPrefix+folder, string(raw)); err != nil {
		return Board{}, err
	}
	if err := tx.Commit(); err != nil {
		return Board{}, err
	}
	return next, nil
}
