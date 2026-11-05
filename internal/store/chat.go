package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DeleteWindow — сколько Telegram даёт на удаление сообщения. Позже его уже не убрать.
const DeleteWindow = 48 * time.Hour

// ChatMessage — запись журнала сообщений.
type ChatMessage struct {
	ID     int
	Kind   string
	SentAt time.Time
}

func stampPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(t.UTC())
}

// TrackMessage записывает сообщение. deleteAt nil — сообщение остаётся в чате.
func (s *Store) TrackMessage(ctx context.Context, msgID int, kind string, sentAt time.Time, deleteAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_messages (msg_id, kind, sent_at, delete_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (msg_id) DO UPDATE SET kind = excluded.kind, delete_at = excluded.delete_at`,
		msgID, kind, stamp(sentAt.UTC()), stampPtr(deleteAt))
	if err != nil {
		return fmt.Errorf("журнал сообщений: %w", err)
	}
	return nil
}

// MessageKind — вид сообщения из журнала; ok = false — его там нет.
func (s *Store) MessageKind(ctx context.Context, msgID int) (string, bool, error) {
	var kind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM chat_messages WHERE msg_id = ? AND deleted = 0`, msgID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("журнал сообщений: %w", err)
	}
	return kind, true, nil
}

// ScheduleDelete убирает сообщение не позже at: более ранний срок сохраняется.
func (s *Store) ScheduleDelete(ctx context.Context, msgID int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE chat_messages SET delete_at = ? WHERE msg_id = ? AND deleted = 0 AND (delete_at IS NULL OR delete_at > ?)`,
		stamp(at.UTC()), msgID, stamp(at.UTC()))
	if err != nil {
		return fmt.Errorf("журнал сообщений: %w", err)
	}
	return nil
}

// ScheduleKind убирает не позже at все сообщения вида kind, кроме keep.
func (s *Store) ScheduleKind(ctx context.Context, kind string, keep int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE chat_messages SET delete_at = ?
		WHERE kind = ? AND msg_id != ? AND deleted = 0 AND (delete_at IS NULL OR delete_at > ?)`,
		stamp(at.UTC()), kind, keep, stamp(at.UTC()))
	if err != nil {
		return fmt.Errorf("журнал сообщений: %w", err)
	}
	return nil
}

// DueMessages — сообщения, которые пора убрать, старые первыми.
func (s *Store) DueMessages(ctx context.Context, now time.Time, limit int) ([]ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT msg_id, kind, sent_at FROM chat_messages
		WHERE deleted = 0 AND delete_at IS NOT NULL AND delete_at <= ? ORDER BY delete_at LIMIT ?`,
		stamp(now.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("журнал сообщений: %w", err)
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var sent string
		if err := rows.Scan(&m.ID, &m.Kind, &sent); err != nil {
			return nil, fmt.Errorf("журнал сообщений: %w", err)
		}
		m.SentAt, _ = time.Parse(time.RFC3339, sent)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkDeleted отмечает, что сообщения в чате больше нет.
func (s *Store) MarkDeleted(ctx context.Context, msgID int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE chat_messages SET deleted = 1 WHERE msg_id = ?`, msgID); err != nil {
		return fmt.Errorf("журнал сообщений: %w", err)
	}
	return nil
}
