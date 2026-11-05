package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Note — заметка владельца.
type Note struct {
	ID   int
	At   time.Time
	Text string
}

// AddNote записывает заметку.
func (s *Store) AddNote(ctx context.Context, text string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO notes (created_at, text) VALUES (?, ?)`, stamp(now.UTC()), text); err != nil {
		return fmt.Errorf("запись заметки: %w", err)
	}
	return nil
}

// DeleteNote удаляет заметку.
func (s *Store) DeleteNote(ctx context.Context, id int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("удаление заметки: %w", err)
	}
	return nil
}

// Notes — все заметки по порядку.
func (s *Store) Notes(ctx context.Context) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, text FROM notes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("чтение заметок: %w", err)
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		var at string
		if err := rows.Scan(&n.ID, &at, &n.Text); err != nil {
			return nil, fmt.Errorf("чтение заметок: %w", err)
		}
		n.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetAction сохраняет изменения из чата до подтверждения.
func (s *Store) SetAction(ctx context.Context, msgID int, payload string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO pending_actions (msg_id, created_at, payload) VALUES (?, ?, ?)`,
		msgID, stamp(now.UTC()), payload)
	if err != nil {
		return fmt.Errorf("запись изменений: %w", err)
	}
	return nil
}

// TakeAction забирает и удаляет изменения; ok = false — нет или истекли (PendingTTL).
func (s *Store) TakeAction(ctx context.Context, msgID int, now time.Time) (string, bool, error) {
	var payload, created string
	err := s.db.QueryRowContext(ctx, `SELECT payload, created_at FROM pending_actions WHERE msg_id = ?`, msgID).Scan(&payload, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("чтение изменений: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pending_actions WHERE msg_id = ?`, msgID); err != nil {
		return "", false, fmt.Errorf("чтение изменений: %w", err)
	}
	at, err := time.Parse(time.RFC3339, created)
	if err != nil || now.Sub(at) > PendingTTL {
		return "", false, nil
	}
	return payload, true, nil
}
