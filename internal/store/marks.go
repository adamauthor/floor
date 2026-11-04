package store

import (
	"context"
	"fmt"
	"time"
)

// Уровни отметки привычки за день.
const (
	LevelMiss  = 0
	LevelFloor = 1
	LevelGoal  = 2
)

// Marks — отметки дня: id привычки → уровень. Неотмеченных в карте нет.
type Marks map[string]int

// SetMark сохраняет уровень привычки за день; level < 0 снимает отметку.
func (s *Store) SetMark(ctx context.Context, date, habitID string, level int, now time.Time) error {
	var err error
	if level < 0 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM habit_marks WHERE date = ? AND habit_id = ?`, date, habitID)
	} else {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO habit_marks (date, habit_id, level, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (date, habit_id) DO UPDATE SET level = excluded.level, updated_at = excluded.updated_at`,
			date, habitID, level, stamp(now))
	}
	if err != nil {
		return fmt.Errorf("отметка привычки: %w", err)
	}
	return nil
}

// MarksRange — отметки за [from, to]: дата → отметки дня.
func (s *Store) MarksRange(ctx context.Context, from, to string) (map[string]Marks, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date, habit_id, level FROM habit_marks WHERE date BETWEEN ? AND ?`, from, to)
	if err != nil {
		return nil, fmt.Errorf("чтение отметок: %w", err)
	}
	defer rows.Close()
	out := make(map[string]Marks)
	for rows.Next() {
		var date, id string
		var level int
		if err := rows.Scan(&date, &id, &level); err != nil {
			return nil, fmt.Errorf("чтение отметок: %w", err)
		}
		if out[date] == nil {
			out[date] = Marks{}
		}
		out[date][id] = level
	}
	return out, rows.Err()
}

// SetAnswer сохраняет ответ; повтор перезаписывает.
func (s *Store) SetAnswer(ctx context.Context, kind, period, habitID string, value int, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO answers (kind, period, habit_id, value, at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kind, period, habit_id) DO UPDATE SET value = excluded.value, at = excluded.at`,
		kind, period, habitID, value, stamp(now))
	if err != nil {
		return fmt.Errorf("запись ответа: %w", err)
	}
	return nil
}

// Answers — ответы вида kind за период: id привычки ("" для общих) → значение.
func (s *Store) Answers(ctx context.Context, kind, period string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT habit_id, value FROM answers WHERE kind = ? AND period = ?`, kind, period)
	if err != nil {
		return nil, fmt.Errorf("чтение ответов: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, fmt.Errorf("чтение ответов: %w", err)
		}
		out[id] = v
	}
	return out, rows.Err()
}

// AnswerHistory — значения вида kind для привычки по периодам, от старых к новым.
func (s *Store) AnswerHistory(ctx context.Context, kind, habitID string, limit int) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT value FROM (SELECT period, value FROM answers WHERE kind = ? AND habit_id = ?
		ORDER BY period DESC LIMIT ?) ORDER BY period`, kind, habitID, limit)
	if err != nil {
		return nil, fmt.Errorf("чтение ответов: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("чтение ответов: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AnswersByPeriod — все ответы вида kind без привязки к привычке: период → значение.
func (s *Store) AnswersByPeriod(ctx context.Context, kind string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT period, value FROM answers WHERE kind = ? AND habit_id = ''`, kind)
	if err != nil {
		return nil, fmt.Errorf("чтение ответов: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var p string
		var v int
		if err := rows.Scan(&p, &v); err != nil {
			return nil, fmt.Errorf("чтение ответов: %w", err)
		}
		out[p] = v
	}
	return out, rows.Err()
}
