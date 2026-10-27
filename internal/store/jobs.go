package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Job is a call whose scheduler jobs the task card does not show (a rig, a
// reskin, a Bang plan or split): the tool that sent it records them so later
// calls can follow them.
type Job struct {
	ID     int64
	UUID   string            // the task
	Kind   string            // rig | reskin | bang_plan | bang_generate
	Key    string            // what it works on: the rig source, or the geometry version
	Detail map[string]string // what the call was of (FBX file, part, ...)
	Jobs   []string          // scheduler job ids
	State  string            // JobRunning until an end was read: succeeded | failed | canceled
	At     time.Time
}

// JobRunning is the state of a recorded call whose end was not read yet.
const JobRunning = "running"

// ClaimBound is how long a claimed call (ClaimJob) may stay without its
// scheduler jobs: the send it was claimed for lasts at most two API calls
// of 60 s and a token refresh. A claim older than that belongs to a process
// that ended mid-send; it is left out of every read and blocks nothing.
const ClaimBound = 5 * time.Minute

// live is the condition of the rows that count: every row with jobs, and the
// claims younger than ClaimBound (the first parameter: now - ClaimBound).
const live = `(jobs <> '[]' OR at_ms >= ?)`

// ErrBusy is a ClaimJob refused: a call of a blocking kind runs.
var ErrBusy = errors.New("a call of the same kind is running")

// ClaimJob records a call about to be sent, as running and without jobs, in
// the same write transaction that checks that no call of the kinds in
// blocking with the same task and key is running (rows other than except).
// So of two sessions sending at once, one records and sends, the other gets
// ErrBusy with the running call. SetJobs fills the claim after the reply;
// DropJob removes it when the send failed.
func (s *Store) ClaimJob(j Job, blocking []string, except int64) (int64, Job, error) {
	detail, err := json.Marshal(j.Detail)
	if err != nil {
		return 0, Job{}, err
	}
	tx, err := s.db.Begin() // BEGIN IMMEDIATE (_txlock): the write lock is held from here
	if err != nil {
		return 0, Job{}, err
	}
	defer tx.Rollback()
	now := nowMS()
	for _, kind := range blocking {
		row := tx.QueryRow(`SELECT id, uuid, kind, key, detail, jobs, state, at_ms FROM jobs
			WHERE uuid = ? AND kind = ? AND key = ? AND state = ? AND id <> ? AND `+live+` ORDER BY at_ms DESC, id DESC LIMIT 1`,
			j.UUID, kind, j.Key, JobRunning, except, now-ClaimBound.Milliseconds())
		busy, err := scanJob(row)
		if err == nil {
			return 0, busy, ErrBusy
		}
		if !errors.Is(err, ErrNotFound) {
			return 0, Job{}, err
		}
	}
	res, err := tx.Exec(`INSERT INTO jobs (uuid, kind, key, detail, jobs, state, at_ms) VALUES (?, ?, ?, ?, '[]', ?, ?)`,
		j.UUID, j.Kind, j.Key, string(detail), JobRunning, now)
	if err != nil {
		return 0, Job{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, Job{}, err
	}
	return id, Job{}, tx.Commit()
}

// SetJobs records the scheduler jobs of a claimed call, and detail values
// the reply gave (merged into the claim's).
func (s *Store) SetJobs(id int64, jobs []string, detail map[string]string) error {
	if len(jobs) == 0 {
		return errors.New("no scheduler job to record")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRow(`SELECT detail FROM jobs WHERE id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	d := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return err
	}
	for k, v := range detail {
		d[k] = v
	}
	db, err := json.Marshal(d)
	if err != nil {
		return err
	}
	jb, err := json.Marshal(jobs)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE jobs SET jobs = ?, detail = ? WHERE id = ?`, string(jb), string(db), id); err != nil {
		return err
	}
	return tx.Commit()
}

// DropJob removes a claimed call whose send failed or named no job.
func (s *Store) DropJob(id int64) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return err
}

// EndJob records the end a call's jobs were read to have.
func (s *Store) EndJob(id int64, state string) error {
	_, err := s.db.Exec(`UPDATE jobs SET state = ? WHERE id = ?`, state, id)
	return err
}

// LatestJob returns the newest recorded call of kind on task uuid whose key
// is key ("" matches any key), or ErrNotFound. A claim past ClaimBound
// without jobs is left out.
func (s *Store) LatestJob(uuid, kind, key string) (Job, error) {
	return scanJob(s.db.QueryRow(`SELECT id, uuid, kind, key, detail, jobs, state, at_ms FROM jobs
		WHERE uuid = ? AND kind = ? AND (? = '' OR key = ?) AND `+live+` ORDER BY at_ms DESC, id DESC LIMIT 1`,
		uuid, kind, key, key, nowMS()-ClaimBound.Milliseconds()))
}

func scanJob(row *sql.Row) (Job, error) {
	var j Job
	var detail, jobs string
	var ms int64
	err := row.Scan(&j.ID, &j.UUID, &j.Kind, &j.Key, &detail, &jobs, &j.State, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	j.At = time.UnixMilli(ms)
	if err := json.Unmarshal([]byte(detail), &j.Detail); err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal([]byte(jobs), &j.Jobs); err != nil {
		return Job{}, err
	}
	return j, nil
}
