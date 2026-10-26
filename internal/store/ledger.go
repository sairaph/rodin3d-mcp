package store

import (
	"database/sql"
	"time"
)

// LedgerEntry is one paid or changing call: what it was quoted, what the
// caller expected, and the wallet (credits) around it. A row is written
// before the call is sent and finished after, so a call that never returned
// still leaves its row with result "sent".
type LedgerEntry struct {
	ID           int64
	At           time.Time
	Op           string
	UUID         string   // the task acted on; for a submit or fork, the new task once known
	Quoted       *float64 // nil: the price was not known
	Expected     *float64
	FrozenBefore float64
	FrozenAfter  *float64
	// Balance is the wallet's available credits (prepaid_available).
	BalanceBefore float64
	BalanceAfter  *float64
	Result        string // sent | ok | error: <message>
}

// AddLedger writes the row of a call about to be sent and returns its id.
func (s *Store) AddLedger(e LedgerEntry) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO ledger (at_ms, op, uuid, quoted, expected, frozen_before, balance_before, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nowMS(), e.Op, e.UUID, nullFloat(e.Quoted), nullFloat(e.Expected), e.FrozenBefore, e.BalanceBefore, "sent")
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishLedger records the outcome of a call: the task (when the call
// created one), the wallet after it and the result.
func (s *Store) FinishLedger(id int64, uuid string, frozenAfter, balanceAfter *float64, result string) error {
	_, err := s.db.Exec(`UPDATE ledger SET uuid = CASE WHEN ? = '' THEN uuid ELSE ? END,
		frozen_after = ?, balance_after = ?, result = ? WHERE id = ?`,
		uuid, uuid, nullFloat(frozenAfter), nullFloat(balanceAfter), result, id)
	return err
}

// LedgerFor returns the rows of a task, oldest first.
func (s *Store) LedgerFor(uuid string) ([]LedgerEntry, error) {
	rows, err := s.db.Query(`SELECT id, at_ms, op, uuid, quoted, expected, frozen_before, frozen_after, balance_before, balance_after, result
		FROM ledger WHERE uuid = ? ORDER BY at_ms, id`, uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		var ms int64
		var quoted, expected, frozenBefore, frozenAfter, balanceBefore, balanceAfter sql.NullFloat64
		if err := rows.Scan(&e.ID, &ms, &e.Op, &e.UUID, &quoted, &expected, &frozenBefore, &frozenAfter, &balanceBefore, &balanceAfter, &e.Result); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(ms)
		e.Quoted, e.Expected = floatPtr(quoted), floatPtr(expected)
		e.FrozenBefore, e.BalanceBefore = frozenBefore.Float64, balanceBefore.Float64
		e.FrozenAfter, e.BalanceAfter = floatPtr(frozenAfter), floatPtr(balanceAfter)
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
