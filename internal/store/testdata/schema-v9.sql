-- The schema of today's main (version 9), as it stood before the rebuild:
-- the database every installation upgrades from.
CREATE TABLE IF NOT EXISTS kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS logins (
  token      TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id             TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL DEFAULT '',
  title          TEXT NOT NULL DEFAULT '',
  title_source   TEXT NOT NULL DEFAULT '',
  harness        TEXT NOT NULL,
  model          TEXT NOT NULL DEFAULT '',
  effort         TEXT NOT NULL DEFAULT '',
  workdir        TEXT NOT NULL,
  workdir_mode   TEXT NOT NULL,
  options        TEXT NOT NULL DEFAULT '{}',
  tmux_name      TEXT NOT NULL DEFAULT '',
  cli_session_id TEXT NOT NULL DEFAULT '',
  cli_session_state TEXT NOT NULL DEFAULT 'none',
  state          TEXT NOT NULL DEFAULT 'starting',
  exit_status    INTEGER NOT NULL DEFAULT -1,
  fail_reason    TEXT NOT NULL DEFAULT '',
  resumed        INTEGER NOT NULL DEFAULT 0,
  resume_count   INTEGER NOT NULL DEFAULT 0,
  cols           INTEGER NOT NULL DEFAULT 120,
  rows           INTEGER NOT NULL DEFAULT 40,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  last_attached  INTEGER NOT NULL DEFAULT 0,
  active_at      INTEGER NOT NULL DEFAULT 0,
  status         TEXT NOT NULL DEFAULT 'in_progress',
  status_at      INTEGER NOT NULL DEFAULT 0,
  start_at       INTEGER NOT NULL DEFAULT 0
);

-- The prompt a prepared session will be started with. It is a table of its
-- own rather than a column because it is written on every pause in typing,
-- and each of those writes is an upload: a draft of a hundred kilobytes must
-- not drag the whole session row to the bucket with it, nor the row the draft.
-- rev is what tells two devices editing one draft apart.
CREATE TABLE IF NOT EXISTS drafts (
  session_id TEXT PRIMARY KEY,
  text       TEXT NOT NULL DEFAULT '',
  rev        INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

-- What has been written here and not yet uploaded to the bucket, by object
-- name. It is in the database, beside the writes it describes, so that a crash
-- between a write and its upload loses neither.
CREATE TABLE IF NOT EXISTS mirror_pending (
  object TEXT PRIMARY KEY,
  at     INTEGER NOT NULL
);
-- The cost ledger: what every conversation any CLI had on this machine cost,
-- by UTC hour and model (costs.go). It outlives the sessions and the
-- transcripts it was read from, which is why it is a table and an object in
-- the bucket of its own rather than something computed when it is asked for.
CREATE TABLE IF NOT EXISTS cost_convs (
  key        TEXT PRIMARY KEY,
  harness    TEXT NOT NULL,
  conv_id    TEXT NOT NULL,
  profile    TEXT NOT NULL DEFAULT '',
  workdir    TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  first_at   INTEGER NOT NULL DEFAULT 0,
  last_at    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS cost_hours (
  key         TEXT NOT NULL,
  hour        INTEGER NOT NULL,
  model       TEXT NOT NULL,
  cost_usd    REAL NOT NULL DEFAULT 0,
  input       INTEGER NOT NULL DEFAULT 0,
  output      INTEGER NOT NULL DEFAULT 0,
  cache_read  INTEGER NOT NULL DEFAULT 0,
  cache_write INTEGER NOT NULL DEFAULT 0,
  requests    INTEGER NOT NULL DEFAULT 0,
  unpriced    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (key, hour, model)
);
-- Which state of a conversation's transcripts the ledger was last computed
-- from. It describes files on this disk, so it is never mirrored.
CREATE TABLE IF NOT EXISTS cost_seen (
  key         TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL
);

-- The unique index on client_id is what makes creating a session safe to
-- retry: the same key can only ever produce one session.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_client ON sessions(client_id) WHERE client_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
-- And the one on tmux_name is what makes two rows for one tmux session
-- impossible, which is the mistake that would leave a terminal with no owner.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_tmux ON sessions(tmux_name) WHERE tmux_name <> '';
