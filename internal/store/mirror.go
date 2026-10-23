package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
)

// This file is the store's half of the bucket mirror: the names the bucket
// knows Socrates' state by, the record of what has changed since it was last
// uploaded, and the two directions a single object travels in - out of the
// database as bytes, and back into it from a download.
//
// The mirror itself (internal/mirror) owns everything else: the uploads, the
// retries, the encryption, the lease. The split is deliberate. SQLite stays
// the working copy every other package reads and writes exactly as before,
// and the only thing those packages had to learn about S3 is nothing at all:
// every write already goes through this package, so this package is where a
// write is noticed.
//
// The object names are the bucket layout, and a bucket outlives the build that
// wrote it, so they are spelled out here once and never derived anywhere else:
//
//	kv/<url.PathEscape(key)>   one row of the key/value table, value verbatim
//	logins.json                the whole logins table, as one JSON array
//	sessions/<id>.json         one session row, the hidden fields included
//	drafts/<id>.json           one prepared session's prompt draft
//	costs/<harness>/<conv>.json one conversation's cost ledger (costs.go)

// LoginsObject is the one object the whole logins table is stored as. A
// handful of rows that are always read together are cheaper as one object
// than as a listing.
const LoginsObject = "logins.json"

// The prefixes of the per-row objects.
const (
	kvPrefix      = "kv/"
	sessionPrefix = "sessions/"
	draftPrefix   = "drafts/"
	objectSuffix  = ".json"
)

// KVObject is the object a key/value row is stored as. The key is escaped
// because keys are free text - `resume_note:<id>`, `profiles.order` - and a
// slash in one would otherwise be a directory in the bucket.
func KVObject(key string) string { return kvPrefix + url.PathEscape(key) }

// SessionObject is the object a session row is stored as.
func SessionObject(id string) string { return sessionPrefix + url.PathEscape(id) + objectSuffix }

// DraftObject is the object a prepared session's draft is stored as.
func DraftObject(id string) string { return draftPrefix + url.PathEscape(id) + objectSuffix }

// ObjectKind says which of the five kinds an object name is.
type ObjectKind int

const (
	ObjectUnknown ObjectKind = iota
	ObjectKV
	ObjectLogins
	ObjectSession
	ObjectDraft
	ObjectCost
)

// ParseObject reads an object name back into its kind and the key or id it
// names. A name this build does not recognise is ObjectUnknown with an error,
// and the mirror leaves such an object alone rather than guessing.
func ParseObject(name string) (ObjectKind, string, error) {
	unescape := func(raw string) (string, error) {
		v, err := url.PathUnescape(raw)
		if err != nil || v == "" {
			return "", fmt.Errorf("%q is not a Socrates object name", name)
		}
		return v, nil
	}
	switch {
	case name == LoginsObject:
		return ObjectLogins, "", nil
	case strings.HasPrefix(name, kvPrefix):
		key, err := unescape(strings.TrimPrefix(name, kvPrefix))
		if err != nil {
			return ObjectUnknown, "", err
		}
		return ObjectKV, key, nil
	case strings.HasPrefix(name, sessionPrefix) && strings.HasSuffix(name, objectSuffix):
		id, err := unescape(strings.TrimSuffix(strings.TrimPrefix(name, sessionPrefix), objectSuffix))
		if err != nil {
			return ObjectUnknown, "", err
		}
		return ObjectSession, id, nil
	case strings.HasPrefix(name, draftPrefix) && strings.HasSuffix(name, objectSuffix):
		id, err := unescape(strings.TrimSuffix(strings.TrimPrefix(name, draftPrefix), objectSuffix))
		if err != nil {
			return ObjectUnknown, "", err
		}
		return ObjectDraft, id, nil
	case strings.HasPrefix(name, costPrefix):
		if key, ok := parseCostObject(name); ok {
			return ObjectCost, key, nil
		}
	}
	return ObjectUnknown, "", fmt.Errorf("%q is not a Socrates object name", name)
}

// MirroredKV reports whether a key/value row belongs in the bucket.
//
// It is a deny-list on purpose. The rows that must stay on this machine are a
// cache (the harness catalogue), the revision reservation (which is about this
// database file and nothing else), the resume notices and the moments the
// sessions entered their states (which describe tmux sessions on this
// machine; the second changes on every committed state), the verdicts on the
// sessions' last turns (written at every turn's end, and about panes on this
// machine as much as the moments are) and the mirror's own bookkeeping.
// Everything else -
// settings, the password hash, profiles, push keys, the unread marks - is
// state a restore has to bring back, and a key added next year is far more
// likely to be that than a cache, so a new key persists unless somebody says
// otherwise.
func MirroredKV(key string) bool {
	switch {
	case key == "agents_catalog", key == revKey, key == "activity.since", key == "outcomes":
		return false
	case strings.HasPrefix(key, "resume_note:"), strings.HasPrefix(key, "mirror."):
		return false
	}
	return key != ""
}

// --------------------------------------------------------------- tracking

// lock and unlock are the write lock, with one thing added on the way out: the
// observer hears about the objects the write changed only once the lock is
// released, so an observer that reads the store back - which is what a
// flusher does - can never deadlock against the write that woke it.
func (s *Store) lock() { s.mu.Lock() }

func (s *Store) unlock() {
	changed := s.changed
	s.changed = nil
	s.mu.Unlock()
	if len(changed) == 0 {
		return
	}
	if fn := s.observer.Load(); fn != nil {
		(*fn)(changed...)
	}
}

// touch records that a committed write changed these objects. It is called
// with the write lock held, straight after the statement that made the
// change, so the record and the change are made under one lock and nothing
// can slip in between them.
//
// The pending row is only written while mirroring is on. Without a bucket
// there is nobody to upload to, and a table that grows by one row per write
// and is never cleared is a leak rather than a record.
func (s *Store) touch(objects ...string) {
	if len(objects) == 0 {
		return
	}
	if s.tracking.Load() {
		s.recordPending(objects)
	}
	s.changed = append(s.changed, objects...)
}

// recordPending writes the pending rows. Every row of one write gets the same
// stamp, and every write a stamp larger than the last, which is what lets
// ClearPending tell "uploaded" from "changed again while uploading".
func (s *Store) recordPending(objects []string) {
	at := now()
	if at <= s.pendingAt {
		at = s.pendingAt + 1
	}
	s.pendingAt = at
	for _, object := range objects {
		if _, err := s.db.Exec(`INSERT INTO mirror_pending(object, at) VALUES(?, ?)
			ON CONFLICT(object) DO UPDATE SET at = excluded.at`, object, at); err != nil {
			// The write itself has committed; losing its pending row only
			// delays its upload to the next full sync, so it is said out loud
			// rather than turned into a failed write.
			log.Printf("store: could not record %s as changed: %v", object, err)
		}
	}
}

// seedPendingAt starts the pending stamps above every stamp already in the
// table, so a clock that went backwards across a restart cannot hand out a
// stamp that an upload in flight would mistake for its own.
func seedPendingAt(db *sql.DB) int64 {
	var newest sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(at) FROM mirror_pending`).Scan(&newest); err == nil && newest.Valid {
		return newest.Int64
	}
	return 0
}

// EnableMirrorTracking turns on the pending record. From here on every
// committed write leaves its objects in mirror_pending until the mirror says
// it has uploaded them. It is a switch rather than a constructor argument
// because the store is opened before anybody knows whether there is a bucket.
func (s *Store) EnableMirrorTracking() { s.tracking.Store(true) }

// MirrorTracking reports whether the pending record is on.
func (s *Store) MirrorTracking() bool { return s.tracking.Load() }

// SetObserver installs the function told about every committed write, by the
// object names it changed. It is called after the write lock is released, on
// the writer's goroutine, so it must be quick: the mirror's only signals a
// channel. nil removes it.
func (s *Store) SetObserver(fn func(objects ...string)) {
	if fn == nil {
		s.observer.Store(nil)
		return
	}
	s.observer.Store(&fn)
}

// Pending is one object that has changed locally and not been uploaded.
type Pending struct {
	Object string
	// At is the stamp of the last change, and the token ClearPending needs.
	At int64
}

// ListPending returns every pending object, oldest change first.
func (s *Store) ListPending() ([]Pending, error) {
	rows, err := s.db.Query(`SELECT object, at FROM mirror_pending ORDER BY at, object`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pending{}
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.Object, &p.At); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingCount is how many objects are waiting for an upload.
func (s *Store) PendingCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mirror_pending`).Scan(&n)
	return n, err
}

// IsPending reports whether an object has changed since its last upload.
func (s *Store) IsPending(object string) bool {
	var at int64
	return s.db.QueryRow(`SELECT at FROM mirror_pending WHERE object = ?`, object).Scan(&at) == nil
}

// IsBackedUp reports whether the bucket holds the current local value of an
// object: mirroring is on and nothing about it is waiting. Without a mirror
// nothing is backed up, whatever the table says.
func (s *Store) IsBackedUp(object string) bool {
	return s.tracking.Load() && !s.fenced.Load() && !s.IsPending(object)
}

// SetMirrorFenced records that the mirror may no longer upload: another
// Socrates took the bucket over. From then on nothing written here reaches
// the bucket, so nothing is reported as backed up, whatever the pending
// record says.
func (s *Store) SetMirrorFenced(fenced bool) { s.fenced.Store(fenced) }

// ClearPending removes an object from the pending record once it has been
// uploaded as it was at stamp `at`. It removes nothing when the object was
// changed again in the meantime - the stamp no longer matches - and reports
// whether it cleared anything, so the flusher knows to go round again.
func (s *Store) ClearPending(object string, at int64) (bool, error) {
	s.lock()
	defer s.unlock()
	res, err := s.db.Exec(`DELETE FROM mirror_pending WHERE object = ? AND at = ?`, object, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkPending records objects as changed whether or not tracking is on, and
// tells the observer. It is the mirror's way of asking for a full upload - the
// first connection of an existing installation - in a form that survives a
// crash half way through it.
func (s *Store) MarkPending(objects ...string) {
	if len(objects) == 0 {
		return
	}
	s.lock()
	defer s.unlock()
	s.recordPending(objects)
	s.changed = append(s.changed, objects...)
}

// --------------------------------------------------------------- exporting

// sessionRecord is a session row as the bucket stores it. It is the browser's
// shape plus the three fields that shape hides, because a row restored on
// another machine without its conversation id is a conversation that can never
// be resumed.
type sessionRecord struct {
	Session
	TmuxName        string `json:"tmux_name"`
	CLISessionID    string `json:"cli_session_id"`
	CLISessionState string `json:"cli_session_state"`
}

// loginRecord is one row of the logins table as the bucket stores it.
type loginRecord struct {
	Token     string `json:"token"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// ExportObject is the current local value of one object, as the bytes the
// bucket stores. ok is false when there is no such row any more, which is
// the mirror's cue to delete the object rather than upload it. The logins
// table always exists, so an empty one exports as `[]`.
func (s *Store) ExportObject(name string) (data []byte, ok bool, err error) {
	kind, key, err := ParseObject(name)
	if err != nil {
		return nil, false, err
	}
	switch kind {
	case ObjectKV:
		if !MirroredKV(key) {
			return nil, false, fmt.Errorf("%s is kept on this machine only", key)
		}
		value, err := s.GetKV(key)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return []byte(value), true, nil
	case ObjectLogins:
		logins, err := s.listLogins()
		if err != nil {
			return nil, false, err
		}
		data, err := json.Marshal(logins)
		return data, err == nil, err
	case ObjectSession:
		row, err := s.GetSession(key)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		data, err := json.Marshal(sessionRecord{
			Session: *row, TmuxName: row.TmuxName,
			CLISessionID: row.CLISessionID, CLISessionState: row.CLISessionState,
		})
		return data, err == nil, err
	case ObjectDraft:
		draft, err := s.GetDraft(key)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		data, err := json.Marshal(draft)
		return data, err == nil, err
	case ObjectCost:
		ledger, err := s.GetCostLedger(key)
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		data, err := json.Marshal(ledger)
		return data, err == nil, err
	}
	return nil, false, fmt.Errorf("%q is not a Socrates object name", name)
}

func (s *Store) listLogins() ([]loginRecord, error) {
	rows, err := s.db.Query(`SELECT token, created_at, expires_at FROM logins ORDER BY created_at, token`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []loginRecord{}
	for rows.Next() {
		var l loginRecord
		if err := rows.Scan(&l.Token, &l.CreatedAt, &l.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AllObjects names every object the local database would put in the bucket
// right now, sorted. It is the list a full upload walks, and the local side of
// the comparison a hydrate makes.
func (s *Store) AllObjects() ([]string, error) {
	out := []string{LoginsObject}
	collect := func(query string, name func(string) string, keep func(string) bool) error {
		rows, err := s.db.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			if keep == nil || keep(v) {
				out = append(out, name(v))
			}
		}
		return rows.Err()
	}
	if err := collect(`SELECT key FROM kv`, KVObject, MirroredKV); err != nil {
		return nil, err
	}
	if err := collect(`SELECT id FROM sessions`, SessionObject, nil); err != nil {
		return nil, err
	}
	if err := collect(`SELECT session_id FROM drafts`, DraftObject, nil); err != nil {
		return nil, err
	}
	if err := collect(`SELECT key FROM cost_convs`, CostObject, nil); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// --------------------------------------------------------------- importing

// ImportObject makes the local database hold what the bucket holds for one
// object: a key/value row, the whole logins table, a session row with its
// hidden fields, a draft, or a conversation's cost ledger.
//
// It is a download, not a change, so nothing is recorded as pending and the
// observer is not told - uploading what was just downloaded would be a round
// trip for nothing, and on a fenced mirror a write to a bucket somebody else
// owns. Any pending record of the object is dropped instead, because the local
// value now is the bucket's. The revision moves, so every open page reloads
// the list.
func (s *Store) ImportObject(name string, data []byte) error {
	kind, key, err := ParseObject(name)
	if err != nil {
		return err
	}
	s.lock()
	defer s.unlock()
	switch kind {
	case ObjectKV:
		if !MirroredKV(key) {
			return fmt.Errorf("%s is kept on this machine only and is not taken from the bucket", key)
		}
		_, err = s.db.Exec(`INSERT INTO kv(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(data))
	case ObjectLogins:
		err = s.importLogins(data)
	case ObjectSession:
		err = s.importSession(key, data)
	case ObjectDraft:
		err = s.importDraft(key, data)
	case ObjectCost:
		err = s.importCostLedger(key, data)
	default:
		err = fmt.Errorf("%q is not a Socrates object name", name)
	}
	if err != nil {
		return fmt.Errorf("import %s: %w", name, err)
	}
	if _, err := s.db.Exec(`DELETE FROM mirror_pending WHERE object = ?`, name); err != nil {
		return err
	}
	s.bump()
	return nil
}

func (s *Store) importLogins(data []byte) error {
	var logins []loginRecord
	if err := json.Unmarshal(data, &logins); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM logins`); err != nil {
		return err
	}
	for _, l := range logins {
		if l.Token == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO logins(token, created_at, expires_at) VALUES(?, ?, ?)
			ON CONFLICT(token) DO UPDATE SET created_at = excluded.created_at, expires_at = excluded.expires_at`,
			l.Token, l.CreatedAt, l.ExpiresAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// importSession upserts a whole row. Every column is written, the ones the
// browser never sees included; a row that exists here under the same id is
// overwritten, because the bucket is the truth.
func (s *Store) importSession(id string, data []byte) error {
	var rec sessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	row := rec.Session
	if row.ID != id {
		return fmt.Errorf("the object holds session %q", row.ID)
	}
	if row.Harness == "" || row.State == "" {
		return errors.New("the session record is incomplete")
	}
	options := "{}"
	if len(row.Options) > 0 && string(row.Options) != "null" {
		if !json.Valid(row.Options) {
			return errors.New("the session options are not valid JSON")
		}
		options = string(row.Options)
	}
	cliState := rec.CLISessionState
	if cliState == "" {
		cliState = CLINone
	}
	resumed := 0
	if row.Resumed {
		resumed = 1
	}
	cols := strings.Fields(strings.ReplaceAll(sessionCols, ",", " "))
	set := make([]string, 0, len(cols)-1)
	for _, c := range cols[1:] {
		set = append(set, c+" = excluded."+c)
	}
	_, err := s.db.Exec(`INSERT INTO sessions(`+sessionCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET `+strings.Join(set, ", "),
		row.ID, row.ClientID, row.Title, row.TitleSource, row.Harness, row.Model, row.Effort,
		row.Workdir, row.WorkdirMode, options, rec.TmuxName, rec.CLISessionID,
		cliState, row.State, row.ExitStatus, row.FailReason, resumed,
		row.ResumeCount, row.Cols, row.Rows, row.CreatedAt, row.UpdatedAt, row.LastAttached, row.ActiveAt,
		row.Status, row.StatusAt, row.StartAt)
	return err
}

func (s *Store) importDraft(id string, data []byte) error {
	var d Draft
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO drafts(session_id, text, rev, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET text = excluded.text, rev = excluded.rev,
		updated_at = excluded.updated_at`, id, d.Text, d.Rev, d.UpdatedAt)
	return err
}

// DeleteObject removes the local row behind one object, for an object the
// bucket no longer has. Like ImportObject it is not a change to upload: no
// pending record, no observer, and any pending record of it is dropped. The
// logins object stands for the whole table, so deleting it signs everybody
// out.
func (s *Store) DeleteObject(name string) error {
	kind, key, err := ParseObject(name)
	if err != nil {
		return err
	}
	s.lock()
	defer s.unlock()
	switch kind {
	case ObjectKV:
		if !MirroredKV(key) {
			return fmt.Errorf("%s is kept on this machine only", key)
		}
		_, err = s.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
	case ObjectLogins:
		_, err = s.db.Exec(`DELETE FROM logins`)
	case ObjectSession:
		_, err = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, key)
	case ObjectDraft:
		_, err = s.db.Exec(`DELETE FROM drafts WHERE session_id = ?`, key)
	case ObjectCost:
		err = s.deleteCostLedger(key)
	}
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM mirror_pending WHERE object = ?`, name); err != nil {
		return err
	}
	s.bump()
	return nil
}

// DeleteLocalAbsent deletes every local row whose object is not in present,
// which is the second half of making the local database equal to the bucket:
// ImportObject brings what the bucket has, and this removes what it does not.
// It returns the names it deleted, so the caller can say what a hydrate
// dropped.
//
// The logins table is left alone. It is one object that always exists once
// anything has been uploaded, and "absent" for it would mean a bucket that
// never had a login - which the caller handles by importing an empty list if
// that is what it means.
//
// It should run before the imports, not after: a row that is gone from the
// bucket can hold a tmux name or a client key that a row coming from the
// bucket now holds, and the unique indexes refuse two of them at once.
func (s *Store) DeleteLocalAbsent(present map[string]bool) ([]string, error) {
	all, err := s.AllObjects()
	if err != nil {
		return nil, err
	}
	var deleted []string
	for _, name := range all {
		if name == LoginsObject || present[name] {
			continue
		}
		if err := s.DeleteObject(name); err != nil {
			return deleted, err
		}
		deleted = append(deleted, name)
	}
	return deleted, nil
}
