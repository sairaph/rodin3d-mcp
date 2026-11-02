package store

import (
	"database/sql"
	"errors"
	"time"
)

// MarkToggle records that a task/set_private on task uuid that may have
// reached Hyper3D ended now.
func (s *Store) MarkToggle(uuid string) error {
	_, err := s.db.Exec(`INSERT INTO toggles (uuid, at_ms) VALUES (?, ?)
		ON CONFLICT(uuid) DO UPDATE SET at_ms = excluded.at_ms`, uuid, nowMS())
	return err
}

// LastToggle returns when MarkToggle last ran for task uuid: the zero time
// when it never did.
func (s *Store) LastToggle(uuid string) (time.Time, error) {
	var ms int64
	err := s.db.QueryRow(`SELECT at_ms FROM toggles WHERE uuid = ?`, uuid).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}
