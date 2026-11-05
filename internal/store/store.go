// Package store — SQLite: дни, выгрузки, сообщения, состояние (docs/SPEC.md, раздел 6).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // драйвер без CGO

	"floor/internal/metrics"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Сроки хранения.
const (
	ExportsRetention  = 30 * 24 * time.Hour
	MessagesRetention = 60 * 24 * time.Hour
)

// Store — доступ к базе. Безопасен для одновременного использования.
type Store struct {
	db *sql.DB
}

// Day — строка таблицы days.
type Day struct {
	metrics.Day
	Exists        bool
	TZOffset      string
	MorningSentAt *time.Time
	EveningSentAt *time.Time
	EveningMsgID  *int
}

// Message — реплика свободного диалога.
type Message struct {
	Role string
	Text string
}

// Open открывает базу и применяет миграции. path ":memory:" не поддерживается:
// WAL требует файла, в тестах используется временный каталог.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("каталог базы: %w", err)
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{
		"_pragma": {"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(ON)"},
	}.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("открытие базы: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("права базы: %w", err)
	}
	return s, nil
}

// Close закрывает базу.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("версия схемы: %w", err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for i, entry := range entries {
		n := i + 1
		if n <= version {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("миграция %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("миграция %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("миграция %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func stamp(t time.Time) string { return t.Format(time.RFC3339) }

const dayColumns = `date, tz_offset, steps, sleep_minutes, wake_at, wake_delta_min,
	floor_status, morning_sent_at, evening_sent_at, evening_msg_id`

type scanner interface{ Scan(dest ...any) error }

func scanDay(row scanner) (Day, error) {
	var (
		d                       Day
		steps, sleep, delta, id sql.NullInt64
		wake, morning, evening  sql.NullString
	)
	err := row.Scan(&d.Date, &d.TZOffset, &steps, &sleep, &wake, &delta, &d.FloorStatus, &morning, &evening, &id)
	if err != nil {
		return Day{}, err
	}
	d.Exists = true
	d.Steps = intPtr(steps)
	d.SleepMinutes = intPtr(sleep)
	d.WakeDeltaMin = intPtr(delta)
	d.EveningMsgID = intPtr(id)
	d.WakeAt = timePtr(wake)
	d.MorningSentAt = timePtr(morning)
	d.EveningSentAt = timePtr(evening)
	return d, nil
}

func intPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func timePtr(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v.String)
	if err != nil {
		return nil
	}
	return &t
}

// GetDay возвращает день; для отсутствующего — пустой Day со статусом none и Exists = false.
func (s *Store) GetDay(ctx context.Context, date string) (Day, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+dayColumns+` FROM days WHERE date = ?`, date)
	d, err := scanDay(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Day{Day: metrics.Day{Date: date, FloorStatus: metrics.StatusNone}}, nil
	}
	if err != nil {
		return Day{}, fmt.Errorf("чтение дня %s: %w", date, err)
	}
	return d, nil
}

// Days возвращает дни диапазона [from, to] по порядку, включая отсутствующие в базе.
func (s *Store) Days(ctx context.Context, from, to string) ([]Day, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+dayColumns+` FROM days WHERE date BETWEEN ? AND ?`, from, to)
	if err != nil {
		return nil, fmt.Errorf("чтение дней: %w", err)
	}
	defer rows.Close()
	found := make(map[string]Day)
	for rows.Next() {
		d, err := scanDay(rows)
		if err != nil {
			return nil, fmt.Errorf("чтение дней: %w", err)
		}
		found[d.Date] = d
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение дней: %w", err)
	}
	var out []Day
	for date := from; date <= to; date = metrics.AddDays(date, 1) {
		if d, ok := found[date]; ok {
			out = append(out, d)
		} else {
			out = append(out, Day{Day: metrics.Day{Date: date, FloorStatus: metrics.StatusNone}})
		}
	}
	return out, nil
}

// UpsertSteps сохраняет шаги; значение только растёт (суммы растут в течение дня).
func (s *Store) UpsertSteps(ctx context.Context, date, tz string, steps int, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO days (date, tz_offset, steps, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (date) DO UPDATE SET
			steps = MAX(COALESCE(steps, 0), excluded.steps),
			tz_offset = excluded.tz_offset,
			updated_at = excluded.updated_at`, date, tz, steps, stamp(now))
	if err != nil {
		return fmt.Errorf("запись шагов: %w", err)
	}
	return nil
}

// SetSleep сохраняет итог ночи.
func (s *Store) SetSleep(ctx context.Context, date, tz string, minutes int, wake time.Time, delta int, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO days (date, tz_offset, sleep_minutes, wake_at, wake_delta_min, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (date) DO UPDATE SET
			sleep_minutes = excluded.sleep_minutes,
			wake_at = excluded.wake_at,
			wake_delta_min = excluded.wake_delta_min,
			tz_offset = excluded.tz_offset,
			updated_at = excluded.updated_at`, date, tz, minutes, stamp(wake), delta, stamp(now))
	if err != nil {
		return fmt.Errorf("запись сна: %w", err)
	}
	return nil
}

// RewriteWakeDeltas пересчитывает отклонение подъёма во всех днях: якорь сменился.
func (s *Store) RewriteWakeDeltas(ctx context.Context, delta func(date string, wake time.Time) int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT date, wake_at FROM days WHERE wake_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("чтение подъёмов: %w", err)
	}
	type wake struct {
		date string
		at   time.Time
	}
	var wakes []wake
	for rows.Next() {
		var date, raw string
		if err := rows.Scan(&date, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("чтение подъёмов: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			wakes = append(wakes, wake{date, t})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("чтение подъёмов: %w", err)
	}
	for _, w := range wakes {
		if _, err := s.db.ExecContext(ctx, `UPDATE days SET wake_delta_min = ? WHERE date = ?`,
			delta(w.date, w.at), w.date); err != nil {
			return fmt.Errorf("запись подъёма: %w", err)
		}
	}
	return nil
}

// ClearSleep убирает итог ночи, которой больше нет в полной выгрузке.
func (s *Store) ClearSleep(ctx context.Context, date string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE days SET sleep_minutes = NULL, wake_at = NULL, wake_delta_min = NULL, updated_at = ?
		WHERE date = ? AND (sleep_minutes IS NOT NULL OR wake_at IS NOT NULL)`, stamp(now), date)
	if err != nil {
		return fmt.Errorf("очистка сна: %w", err)
	}
	return nil
}

// SetFloor сохраняет отметку пола; повторная отметка перезаписывает прошлую.
func (s *Store) SetFloor(ctx context.Context, date, tz, status string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO days (date, tz_offset, floor_status, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (date) DO UPDATE SET
			floor_status = excluded.floor_status,
			updated_at = excluded.updated_at`, date, tz, status, stamp(now))
	if err != nil {
		return fmt.Errorf("запись пола: %w", err)
	}
	return nil
}

// MarkMorningSent отмечает отправку утреннего сообщения.
func (s *Store) MarkMorningSent(ctx context.Context, date, tz string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO days (date, tz_offset, morning_sent_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (date) DO UPDATE SET
			morning_sent_at = excluded.morning_sent_at,
			updated_at = excluded.updated_at`, date, tz, stamp(now), stamp(now))
	if err != nil {
		return fmt.Errorf("отметка утра: %w", err)
	}
	return nil
}

// MarkEveningSent отмечает отправку вечернего сообщения.
func (s *Store) MarkEveningSent(ctx context.Context, date, tz string, msgID int, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO days (date, tz_offset, evening_sent_at, evening_msg_id, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (date) DO UPDATE SET
			evening_sent_at = excluded.evening_sent_at,
			evening_msg_id = excluded.evening_msg_id,
			updated_at = excluded.updated_at`, date, tz, stamp(now), msgID, stamp(now))
	if err != nil {
		return fmt.Errorf("отметка вечера: %w", err)
	}
	return nil
}

// InsertExport сохраняет выгрузку; false — такая уже применялась.
func (s *Store) InsertExport(ctx context.Context, sha string, payload []byte, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO exports (received_at, sha256, payload) VALUES (?, ?, ?)
		ON CONFLICT (sha256) DO NOTHING`, now.UTC().Format(time.RFC3339), sha, string(payload))
	if err != nil {
		return false, fmt.Errorf("запись выгрузки: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("запись выгрузки: %w", err)
	}
	return n > 0, nil
}

// ExportsSince возвращает сырые выгрузки новее заданного момента.
func (s *Store) ExportsSince(ctx context.Context, since time.Time) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload FROM exports WHERE received_at >= ? ORDER BY id`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("чтение выгрузок: %w", err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("чтение выгрузок: %w", err)
		}
		out = append(out, []byte(p))
	}
	return out, rows.Err()
}

// AddMessage сохраняет реплику диалога.
func (s *Store) AddMessage(ctx context.Context, role, text string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (at, role, text) VALUES (?, ?, ?)`, now.UTC().Format(time.RFC3339), role, text)
	if err != nil {
		return fmt.Errorf("запись сообщения: %w", err)
	}
	return nil
}

// RecentMessages возвращает последние n реплик в хронологическом порядке.
func (s *Store) RecentMessages(ctx context.Context, n int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role, text FROM messages ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("чтение сообщений: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Text); err != nil {
			return nil, fmt.Errorf("чтение сообщений: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение сообщений: %w", err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// GetKV читает значение; пустая строка — ключа нет.
func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("чтение %s: %w", key, err)
	}
	return v, nil
}

// SetKV записывает значение.
func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("запись %s: %w", key, err)
	}
	return nil
}

const llmCallsPrefix = "llm_calls:"

// TakeLLMCall занимает один вызов Claude из дневного лимита; false — лимит исчерпан.
func (s *Store) TakeLLMCall(ctx context.Context, date string, limit int) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO kv (key, value) VALUES (?, '1')
		ON CONFLICT (key) DO UPDATE SET value = CAST(value AS INTEGER) + 1
		RETURNING value`, llmCallsPrefix+date).Scan(&value)
	if err != nil {
		return false, fmt.Errorf("лимит вызовов: %w", err)
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return false, fmt.Errorf("лимит вызовов: %w", err)
	}
	return n <= limit, nil
}

// Cleanup удаляет устаревшие выгрузки, сообщения и счётчики вызовов.
func (s *Store) Cleanup(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM exports WHERE received_at < ?`,
		now.Add(-ExportsRetention).UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("очистка выгрузок: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE at < ?`,
		now.Add(-MessagesRetention).UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("очистка сообщений: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM money_pending WHERE created_at < ?`,
		now.Add(-PendingTTL).UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("очистка сверок: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM chat_messages WHERE sent_at < ?`,
		now.Add(-MessagesRetention).UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("очистка журнала чата: %w", err)
	}
	cutoff := llmCallsPrefix + now.AddDate(0, 0, -2).Format(time.DateOnly)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key LIKE ? AND key < ?`,
		llmCallsPrefix+"%", cutoff); err != nil {
		return fmt.Errorf("очистка счётчиков: %w", err)
	}
	return nil
}
