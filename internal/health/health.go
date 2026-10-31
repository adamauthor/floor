// Package health описывает контракт выгрузки с iPhone и источники данных.
// Контракт — docs/SPEC.md, раздел 4.
package health

import (
	"context"
	"time"
)

// Export — одна выгрузка из Команды iOS (schema = 1).
type Export struct {
	Schema      int           `json:"schema"`
	GeneratedAt time.Time     `json:"generated_at"`
	TZOffset    string        `json:"tz_offset"`
	Steps       []StepsDay    `json:"steps"`
	Sleep       []SleepSample `json:"sleep"`
}

// StepsDay — сумма шагов за местную дату.
type StepsDay struct {
	Date  string `json:"date"` // YYYY-MM-DD
	Count int    `json:"count"`
}

// SleepSample — один сегмент сна из Apple Health.
type SleepSample struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Value string    `json:"value"`
}

// Source поставляет выгрузки. Реализации: FileSource (Mac), HTTPSource (Fly.io).
// TODO(M1): реализовать FileSource; TODO(Fly): HTTPSource.
type Source interface {
	// Updates возвращает канал выгрузок; закрывается при отмене ctx.
	Updates(ctx context.Context) (<-chan Export, error)
}
