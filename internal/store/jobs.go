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

// AddJob records a call's jobs as running and returns its id.
func (s *Store) AddJob(j Job) (int64, error) {
	detail, err := json.Marshal(j.Detail)
	if err != nil {
		return 0, err
	}
	jobs, err := json.Marshal(j.Jobs)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO jobs (uuid, kind, key, detail, jobs, state, at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.UUID, j.Kind, j.Key, string(detail), string(jobs), JobRunning, nowMS())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EndJob records the end a call's jobs were read to have.
func (s *Store) EndJob(id int64, state string) error {
	_, err := s.db.Exec(`UPDATE jobs SET state = ? WHERE id = ?`, state, id)
	return err
}

// LatestJob returns the newest recorded call of kind on task uuid whose key
// is key ("" matches any key), or ErrNotFound.
func (s *Store) LatestJob(uuid, kind, key string) (Job, error) {
	var j Job
	var detail, jobs string
	var ms int64
	err := s.db.QueryRow(`SELECT id, uuid, kind, key, detail, jobs, state, at_ms FROM jobs
		WHERE uuid = ? AND kind = ? AND (? = '' OR key = ?) ORDER BY at_ms DESC, id DESC LIMIT 1`, uuid, kind, key, key).
		Scan(&j.ID, &j.UUID, &j.Kind, &j.Key, &detail, &jobs, &j.State, &ms)
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
