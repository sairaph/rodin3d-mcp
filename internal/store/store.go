// Package store owns rodin.db: the task index, the last card of every task,
// the stage transitions seen, local files, and the sync state.
//
// The database is the shared state. The daemon is the only syncer; tool
// calls read it and write only their own rows (files they downloaded, and
// from phase P2 on, submits and the ledger). WAL mode and a busy timeout make
// that safe across processes.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

// ErrNotFound is a task the store does not hold.
var ErrNotFound = errors.New("not found")

// Store is an open database.
type Store struct {
	db *sql.DB
}

// Open opens (creating it when needed) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	// Pragmas go in the DSN so every pooled connection gets them.
	// The path is a URI path: %, ? and # in it are escaped, or the driver
	// would read them as an escape, the query or a fragment.
	dsn := "file:" + uriPath(path) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
  uuid         TEXT PRIMARY KEY,
  prompt       TEXT NOT NULL DEFAULT '',
  step         TEXT NOT NULL DEFAULT '',
  user_state   TEXT NOT NULL DEFAULT '',
  mode         TEXT NOT NULL DEFAULT '',   -- gen-2.5 | gen-1.5
  tier         TEXT NOT NULL DEFAULT '',   -- Gen-2.5 tier or Gen-1.5 mode
  views        TEXT NOT NULL DEFAULT '',   -- image_label joined by commas
  private      INTEGER NOT NULL DEFAULT 0,
  pending_jobs INTEGER NOT NULL DEFAULT 0,
  created      TEXT NOT NULL DEFAULT '',   -- list time (RFC 3339)
  updated      TEXT NOT NULL DEFAULT '',   -- card generate_time
  seen_ms      INTEGER NOT NULL DEFAULT 0  -- last time the list showed it
);
CREATE INDEX IF NOT EXISTS tasks_by_created ON tasks(created DESC);

-- The last card of each task, with signed addresses stripped of their
-- signatures: settings and asset names stay, nothing fetchable does.
CREATE TABLE IF NOT EXISTS cards (
  uuid       TEXT PRIMARY KEY,
  fetched_ms INTEGER NOT NULL,
  json       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  id         INTEGER PRIMARY KEY,
  uuid       TEXT NOT NULL,
  at_ms      INTEGER NOT NULL,
  step       TEXT NOT NULL,
  user_state TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_by_task ON events(uuid, at_ms);

-- Credit ledger of paid calls (written from phase P2 on).
CREATE TABLE IF NOT EXISTS ledger (
  id             INTEGER PRIMARY KEY,
  at_ms          INTEGER NOT NULL,
  op             TEXT NOT NULL,
  uuid           TEXT NOT NULL DEFAULT '',
  quoted         REAL,
  expected       REAL,
  frozen_before  REAL,
  frozen_after   REAL,
  balance_before REAL,
  balance_after  REAL,
  result         TEXT NOT NULL DEFAULT ''
);

-- Files on disk: cached stage images and downloaded pack files.
CREATE TABLE IF NOT EXISTS files (
  uuid       TEXT NOT NULL,
  kind       TEXT NOT NULL,          -- cache:<asset> | download
  path       TEXT NOT NULL,
  bytes      INTEGER NOT NULL,
  sha256     TEXT NOT NULL,
  source_key TEXT NOT NULL DEFAULT '', -- hash of the source address without its signature
  validator  TEXT NOT NULL DEFAULT '', -- ETag or Last-Modified of the fetched file
  at_ms      INTEGER NOT NULL,
  PRIMARY KEY (uuid, kind, path)
);

-- Input image sets arranged in the viewer (phase P2v).
CREATE TABLE IF NOT EXISTS input_sets (
  id         TEXT PRIMARY KEY,
  created_ms INTEGER NOT NULL,
  json       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_state (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  phase        TEXT NOT NULL DEFAULT 'idle',
  last_error   TEXT NOT NULL DEFAULT '',
  last_sync_ms INTEGER NOT NULL DEFAULT 0,
  user_uuid    TEXT NOT NULL DEFAULT '',
  updated_ms   INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO sync_state (id) VALUES (1);
`

func (s *Store) migrate() error {
	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("the database schema is version %d but this build understands %d; update rodin3d-mcp", current, schemaVersion)
	}
	if current == schemaVersion {
		return nil
	}
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}

func nowMS() int64 { return time.Now().UnixMilli() }

// Task is one row of the task index.
type Task struct {
	UUID        string
	Prompt      string
	Step        string
	UserState   string
	Mode        string
	Tier        string
	Views       []string
	Private     bool
	PendingJobs int
	Created     string
	Updated     string
}

// ListRow is what the history list says about a task.
type ListRow struct {
	UUID        string
	Prompt      string
	Step        string
	Private     bool
	PendingJobs int
	Created     string
}

// UpsertListRow records a list row and reports whether the task's card
// should be fetched: the task is new, its step changed, it has pending jobs,
// or no card is stored.
func (s *Store) UpsertListRow(r ListRow) (needsCard bool, err error) {
	var step string
	var hasCard int
	err = s.db.QueryRow(`SELECT t.step, EXISTS(SELECT 1 FROM cards c WHERE c.uuid = t.uuid) FROM tasks t WHERE t.uuid = ?`, r.UUID).Scan(&step, &hasCard)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		needsCard = true
	case err != nil:
		return false, err
	default:
		needsCard = step != r.Step || hasCard == 0 || r.PendingJobs > 0
	}
	_, err = s.db.Exec(`INSERT INTO tasks (uuid, prompt, step, private, pending_jobs, created, seen_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET prompt = excluded.prompt, private = excluded.private,
		  created = excluded.created, seen_ms = excluded.seen_ms,
		  step = CASE WHEN tasks.step = '' THEN excluded.step ELSE tasks.step END,
		  pending_jobs = excluded.pending_jobs`,
		r.UUID, r.Prompt, r.Step, b2i(r.Private), r.PendingJobs, r.Created, nowMS())
	return needsCard, err
}

// CardSummary is what SaveCard stores from a card next to its JSON.
type CardSummary struct {
	UUID        string
	Prompt      string
	Step        string
	UserState   string
	Mode        string
	Tier        string
	Views       []string
	Private     bool
	PendingJobs int
	Created     string
	Updated     string
}

// SaveCard stores a card (JSON with signatures already stripped) and its
// summary, and appends an event when the step or user_state changed. It
// reports whether they changed (true for a task seen the first time).
func (s *Store) SaveCard(c CardSummary, cardJSON []byte) (changed bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var step, state string
	err = tx.QueryRow(`SELECT step, user_state FROM tasks WHERE uuid = ?`, c.UUID).Scan(&step, &state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		changed = true
	case err != nil:
		return false, err
	default:
		changed = step != c.Step || state != c.UserState
	}
	now := nowMS()
	_, err = tx.Exec(`INSERT INTO tasks (uuid, prompt, step, user_state, mode, tier, views, private, pending_jobs, created, updated, seen_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET prompt = excluded.prompt, step = excluded.step, user_state = excluded.user_state,
		  mode = excluded.mode, tier = excluded.tier, views = excluded.views, private = excluded.private,
		  pending_jobs = excluded.pending_jobs, updated = excluded.updated,
		  created = CASE WHEN excluded.created = '' THEN tasks.created ELSE excluded.created END`,
		c.UUID, c.Prompt, c.Step, c.UserState, c.Mode, c.Tier, strings.Join(c.Views, ","), b2i(c.Private), c.PendingJobs, c.Created, c.Updated, now)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO cards (uuid, fetched_ms, json) VALUES (?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET fetched_ms = excluded.fetched_ms, json = excluded.json`, c.UUID, now, string(cardJSON)); err != nil {
		return false, err
	}
	if changed {
		if _, err := tx.Exec(`INSERT INTO events (uuid, at_ms, step, user_state) VALUES (?, ?, ?, ?)`, c.UUID, now, c.Step, c.UserState); err != nil {
			return false, err
		}
	}
	return changed, tx.Commit()
}

const taskColumns = `uuid, prompt, step, user_state, mode, tier, views, private, pending_jobs, created, updated`

func scanTask(sc interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var views string
	var private int
	err := sc.Scan(&t.UUID, &t.Prompt, &t.Step, &t.UserState, &t.Mode, &t.Tier, &views, &private, &t.PendingJobs, &t.Created, &t.Updated)
	if views != "" {
		t.Views = strings.Split(views, ",")
	}
	t.Private = private != 0
	return t, err
}

// Tasks returns every task, newest first.
func (s *Store) Tasks() ([]Task, error) {
	rows, err := s.db.Query(`SELECT ` + taskColumns + ` FROM tasks ORDER BY created DESC, uuid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Task returns one task.
func (s *Store) Task(uuid string) (Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE uuid = ?`, uuid))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// ResolveID finds the task whose uuid is id or starts with id (at least 8
// characters). It returns ErrNotFound, or an *AmbiguousError.
func (s *Store) ResolveID(id string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if t, err := s.Task(id); err == nil {
		return t.UUID, nil
	}
	if len(id) < 8 {
		return "", ErrNotFound
	}
	rows, err := s.db.Query(`SELECT uuid FROM tasks WHERE uuid LIKE ? ESCAPE '\' LIMIT 5`, escapeLike(id)+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return "", err
		}
		ids = append(ids, u)
	}
	switch len(ids) {
	case 0:
		return "", ErrNotFound
	case 1:
		return ids[0], nil
	}
	return "", &AmbiguousError{Prefix: id, Matches: ids}
}

// AmbiguousError is an id prefix that matches several tasks.
type AmbiguousError struct {
	Prefix  string
	Matches []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches %d tasks: %s", e.Prefix, len(e.Matches), strings.Join(e.Matches, ", "))
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Card returns the stored card JSON of a task.
func (s *Store) Card(uuid string) ([]byte, time.Time, error) {
	var js string
	var ms int64
	err := s.db.QueryRow(`SELECT json, fetched_ms FROM cards WHERE uuid = ?`, uuid).Scan(&js, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	return []byte(js), time.UnixMilli(ms), err
}

// Cards returns every stored card JSON keyed by task.
func (s *Store) Cards() (map[string][]byte, error) {
	rows, err := s.db.Query(`SELECT uuid, json FROM cards`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var u, js string
		if err := rows.Scan(&u, &js); err != nil {
			return nil, err
		}
		out[u] = []byte(js)
	}
	return out, rows.Err()
}

// InFlight returns the tasks a job is working on.
func (s *Store) InFlight() ([]string, error) {
	rows, err := s.db.Query(`SELECT uuid FROM tasks WHERE user_state IN ('Generating', '') OR step = 'Created' OR pending_jobs > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Event is one stage transition seen.
type Event struct {
	At        time.Time
	Step      string
	UserState string
}

// Events returns the transitions of a task, oldest first.
func (s *Store) Events(uuid string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT at_ms, step, user_state FROM events WHERE uuid = ? ORDER BY at_ms, id`, uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ms int64
		if err := rows.Scan(&ms, &e.Step, &e.UserState); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(ms)
		out = append(out, e)
	}
	return out, rows.Err()
}

// File is one file on disk.
type File struct {
	UUID      string
	Kind      string
	Path      string
	Bytes     int64
	SHA256    string
	SourceKey string
	Validator string // ETag or "lm:"+Last-Modified of a cached file
	At        time.Time
}

// RecordFile stores (or replaces) a file row.
func (s *Store) RecordFile(f File) error {
	_, err := s.db.Exec(`INSERT INTO files (uuid, kind, path, bytes, sha256, source_key, validator, at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid, kind, path) DO UPDATE SET bytes = excluded.bytes, sha256 = excluded.sha256,
		  source_key = excluded.source_key, validator = excluded.validator, at_ms = excluded.at_ms`,
		f.UUID, f.Kind, f.Path, f.Bytes, f.SHA256, f.SourceKey, f.Validator, nowMS())
	return err
}

// Files returns the files of a task; kind filters by exact kind or, ending
// in ":", by prefix ("" returns all).
func (s *Store) Files(uuid, kind string) ([]File, error) {
	q := `SELECT uuid, kind, path, bytes, sha256, source_key, validator, at_ms FROM files WHERE uuid = ?`
	args := []any{uuid}
	switch {
	case strings.HasSuffix(kind, ":"):
		q += ` AND kind LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(kind)+"%")
	case kind != "":
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	rows, err := s.db.Query(q+` ORDER BY kind, path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		var ms int64
		if err := rows.Scan(&f.UUID, &f.Kind, &f.Path, &f.Bytes, &f.SHA256, &f.SourceKey, &f.Validator, &ms); err != nil {
			return nil, err
		}
		f.At = time.UnixMilli(ms)
		out = append(out, f)
	}
	return out, rows.Err()
}

// DownloadDirs lists the folders download_model has saved into, so a sweep
// can reach the temporaries of a caller-chosen output_dir too.
func (s *Store) DownloadDirs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT path FROM files WHERE kind = 'download'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if dir := filepath.Dir(p); !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out, rows.Err()
}

// SaveInputSet stores (or replaces) the input set id: its images, order,
// view tags and weights as JSON, written by the viewer.
func (s *Store) SaveInputSet(id string, data []byte) error {
	_, err := s.db.Exec(`INSERT INTO input_sets (id, created_ms, json) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET json = excluded.json`, id, nowMS(), string(data))
	return err
}

// InputSet returns the JSON of input set id, or ErrNotFound.
func (s *Store) InputSet(id string) ([]byte, error) {
	var data string
	err := s.db.QueryRow(`SELECT json FROM input_sets WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return []byte(data), err
}

// Sync phases.
const (
	PhaseIdle       = "idle"
	PhaseSyncing    = "syncing"
	PhaseWatching   = "watching"
	PhaseNeedsLogin = "needs_login"
	PhaseError      = "error"
)

// SyncState is the daemon's progress as stored.
type SyncState struct {
	Phase     string
	LastError string
	LastSync  time.Time // zero: never completed a history sync
	UserUUID  string    // the account the stored tasks belong to
	Updated   time.Time
}

// State reads the sync state.
func (s *Store) State() (SyncState, error) {
	var st SyncState
	var last, updated int64
	err := s.db.QueryRow(`SELECT phase, last_error, last_sync_ms, user_uuid, updated_ms FROM sync_state WHERE id = 1`).
		Scan(&st.Phase, &st.LastError, &last, &st.UserUUID, &updated)
	if last > 0 {
		st.LastSync = time.UnixMilli(last)
	}
	if updated > 0 {
		st.Updated = time.UnixMilli(updated)
	}
	return st, err
}

// SetPhase records the daemon's phase and last error ("" clears it).
func (s *Store) SetPhase(phase, lastError string) error {
	_, err := s.db.Exec(`UPDATE sync_state SET phase = ?, last_error = ?, updated_ms = ? WHERE id = 1`, phase, lastError, nowMS())
	return err
}

// MarkSynced records a completed history sync at t.
func (s *Store) MarkSynced(t time.Time) error {
	_, err := s.db.Exec(`UPDATE sync_state SET last_sync_ms = ?, last_error = '', updated_ms = ? WHERE id = 1`, t.UnixMilli(), nowMS())
	return err
}

// ClaimAccount makes userUUID the owner of the stored tasks. When they
// belonged to another account, its tasks, cards and events are removed (the
// files on disk stay) and the sync starts over; reset reports that.
func (s *Store) ClaimAccount(userUUID string) (reset bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRow(`SELECT user_uuid FROM sync_state WHERE id = 1`).Scan(&current); err != nil {
		return false, err
	}
	if current == userUUID {
		return false, nil
	}
	if current != "" {
		for _, q := range []string{`DELETE FROM tasks`, `DELETE FROM cards`, `DELETE FROM events`} {
			if _, err := tx.Exec(q); err != nil {
				return false, err
			}
		}
		reset = true
	}
	if _, err := tx.Exec(`UPDATE sync_state SET user_uuid = ?, last_sync_ms = 0, updated_ms = ? WHERE id = 1`, userUUID, nowMS()); err != nil {
		return false, err
	}
	return reset, tx.Commit()
}

// Prune removes the tasks (with their cards and events) that keep does not
// hold: the account no longer lists them. Files on disk stay. It returns how
// many tasks were removed.
func (s *Store) Prune(keep map[string]bool) (int, error) {
	tasks, err := s.Tasks()
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, t := range tasks {
		if keep[t.UUID] {
			continue
		}
		for _, q := range []string{`DELETE FROM tasks WHERE uuid = ?`, `DELETE FROM cards WHERE uuid = ?`, `DELETE FROM events WHERE uuid = ?`} {
			if _, err := tx.Exec(q, t.UUID); err != nil {
				return 0, err
			}
		}
		n++
	}
	return n, tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// uriPath escapes a file path for a SQLite file: URI.
func uriPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(path))
}
