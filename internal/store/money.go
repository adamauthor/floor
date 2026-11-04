package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"floor/internal/money"
)

// PendingTTL — сколько живут кнопки подтверждения сверки.
const PendingTTL = 24 * time.Hour

// InsertSnapshot сохраняет сверку.
func (s *Store) InsertSnapshot(ctx context.Context, snap money.Snapshot, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("запись сверки: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	source := "text"
	if snap.Baseline {
		source = "baseline"
	}
	var debt any
	if snap.DebtTotal != nil {
		debt = *snap.DebtTotal
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO money_snapshots (date, at, debt_total, source) VALUES (?, ?, ?, ?)`,
		snap.Date, stamp(now), debt, source)
	if err != nil {
		return fmt.Errorf("запись сверки: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("запись сверки: %w", err)
	}
	for account, amount := range snap.Balances {
		carried := 0
		if snap.Carried[account] {
			carried = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO money_balances (snapshot_id, account, amount, carried) VALUES (?, ?, ?, ?)`,
			id, account, amount, carried); err != nil {
			return fmt.Errorf("запись сверки: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("запись сверки: %w", err)
	}
	return nil
}

// LastSnapshot — последняя сверка; nil — сверок нет. Несколько в один день — последняя.
func (s *Store) LastSnapshot(ctx context.Context) (*money.Snapshot, error) {
	var (
		id     int64
		snap   money.Snapshot
		debt   sql.NullInt64
		source string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, date, debt_total, source FROM money_snapshots ORDER BY date DESC, id DESC LIMIT 1`).
		Scan(&id, &snap.Date, &debt, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение сверки: %w", err)
	}
	snap.Baseline = source == "baseline"
	if debt.Valid {
		v := debt.Int64
		snap.DebtTotal = &v
	}
	snap.Balances, snap.Carried = map[string]int64{}, map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT account, amount, carried FROM money_balances WHERE snapshot_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("чтение сверки: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var account string
		var amount int64
		var carried int
		if err := rows.Scan(&account, &amount, &carried); err != nil {
			return nil, fmt.Errorf("чтение сверки: %w", err)
		}
		snap.Balances[account] = amount
		snap.Carried[account] = carried == 1
	}
	return &snap, rows.Err()
}

// SetPending запоминает распознанную сверку под id сообщения с кнопками.
func (s *Store) SetPending(ctx context.Context, msgID int, payload string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO money_pending (msg_id, created_at, payload) VALUES (?, ?, ?)`,
		msgID, stamp(now.UTC()), payload)
	if err != nil {
		return fmt.Errorf("запись сверки: %w", err)
	}
	return nil
}

// TakePending забирает и удаляет сверку; ok = false — нет или истекла.
func (s *Store) TakePending(ctx context.Context, msgID int, now time.Time) (string, bool, error) {
	var payload, created string
	err := s.db.QueryRowContext(ctx, `SELECT payload, created_at FROM money_pending WHERE msg_id = ?`, msgID).Scan(&payload, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("чтение сверки: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM money_pending WHERE msg_id = ?`, msgID); err != nil {
		return "", false, fmt.Errorf("чтение сверки: %w", err)
	}
	at, err := time.Parse(time.RFC3339, created)
	if err != nil || now.Sub(at) > PendingTTL {
		return "", false, nil
	}
	return payload, true, nil
}

// Workout — тренировка.
type Workout struct {
	ID      string
	Date    string
	Start   *time.Time
	Minutes int
	Type    string
	Source  string
}

// UpsertWorkout сохраняет тренировку; повтор с тем же id обновляет.
func (s *Store) UpsertWorkout(ctx context.Context, w Workout) error {
	var start any
	if w.Start != nil {
		start = stamp(*w.Start)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workouts (id, date, start_at, minutes, type, source) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET date = excluded.date, start_at = excluded.start_at,
			minutes = excluded.minutes, type = excluded.type`,
		w.ID, w.Date, start, w.Minutes, w.Type, w.Source)
	if err != nil {
		return fmt.Errorf("запись тренировки: %w", err)
	}
	return nil
}

// Workouts — тренировки за [from, to] не короче minMinutes (ручные засчитываются всегда), по датам.
func (s *Store) Workouts(ctx context.Context, from, to string, minMinutes int) (map[string][]Workout, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, date, start_at, minutes, type, source FROM workouts
		WHERE date BETWEEN ? AND ? AND (source = 'manual' OR minutes >= ?) ORDER BY start_at`, from, to, minMinutes)
	if err != nil {
		return nil, fmt.Errorf("чтение тренировок: %w", err)
	}
	defer rows.Close()
	out := map[string][]Workout{}
	for rows.Next() {
		var w Workout
		var start, typ sql.NullString
		var minutes sql.NullInt64
		if err := rows.Scan(&w.ID, &w.Date, &start, &minutes, &typ, &w.Source); err != nil {
			return nil, fmt.Errorf("чтение тренировок: %w", err)
		}
		w.Start, w.Minutes, w.Type = timePtr(start), int(minutes.Int64), typ.String
		out[w.Date] = append(out[w.Date], w)
	}
	return out, rows.Err()
}
