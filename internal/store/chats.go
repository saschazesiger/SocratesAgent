package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// OutboxEntry is one command on its way to a chat's agent host.
type OutboxEntry struct {
	ChatID   string
	ClientID string
	Command  json.RawMessage
	At       int64
}

// PutOutbox records a command before it is sent. A command already there under
// the same client_id is left as it is, and inserted says which happened: a
// request repeated by a browser is the same command, not a second one.
func (s *Store) PutOutbox(chatID, clientID string, command json.RawMessage) (inserted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`INSERT OR IGNORE INTO chat_outbox(chat_id, client_id, command, created_at) VALUES(?, ?, ?, ?)`,
		chatID, clientID, string(command), now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Outbox is every command a chat's host has not acknowledged, oldest first:
// what a (re)connected server sends again.
func (s *Store) Outbox(chatID string) ([]OutboxEntry, error) {
	rows, err := s.db.Query(`SELECT chat_id, client_id, command, created_at FROM chat_outbox
		WHERE chat_id = ? ORDER BY created_at, rowid`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var cmd string
		if err := rows.Scan(&e.ChatID, &e.ClientID, &cmd, &e.At); err != nil {
			return nil, err
		}
		e.Command = json.RawMessage(cmd)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteOutbox drops a command the host acknowledged (or refused for good).
func (s *Store) DeleteOutbox(chatID, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM chat_outbox WHERE chat_id = ? AND client_id = ?`, chatID, clientID)
	return err
}

// ClearOutbox drops every command of a chat, which is what deleting it does.
func (s *Store) ClearOutbox(chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM chat_outbox WHERE chat_id = ?`, chatID)
	return err
}

// UpdateChatOptions changes a chat's options - Ultracode, its account and its
// automatic switch: what its next host is started with. change is handed the
// options as the row holds them now and answers them changed; they are read
// and written under the write lock, so that two changes of different options
// made at once - a toggle of Ultracode and the account an automatic switch
// moved the chat to - never write one back over the other. It is not a status
// change and does not move the row; options that come back unchanged are not
// written, and changed says which happened.
func (s *Store) UpdateChatOptions(id string, change func(json.RawMessage) json.RawMessage) (changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current string
	err = s.db.QueryRow(`SELECT options FROM sessions WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	next := change(json.RawMessage(current))
	value := "{}"
	if len(next) > 0 {
		if !json.Valid(next) {
			return false, errors.New("session options are not valid JSON")
		}
		value = string(next)
	}
	if value == current {
		return false, nil
	}
	res, err := s.db.Exec(`UPDATE sessions SET options = ?, updated_at = ? WHERE id = ?`, value, now(), id)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return false, ErrNotFound
	}
	s.bump()
	return true, nil
}

// SetChatConversation records the agent's own conversation id of a chat, as
// its host reported it. It is not a status change and does not move the row.
func (s *Store) SetChatConversation(id, conversationID string) error {
	return s.update(id, `cli_session_id = ?, cli_session_state = ?`, conversationID, CLIKnown)
}
