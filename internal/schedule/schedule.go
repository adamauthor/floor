// Package schedule решает «пора ли» по местному времени и крутит тикер (docs/SPEC.md, раздел 9).
package schedule

import (
	"context"
	"time"

	"floor/internal/config"
	"floor/internal/health"
	"floor/internal/metrics"
)

// Окна из раздела 8: после них сообщение не досылается.
const (
	morningWindow    = 7 * time.Hour // утро ждёт до якоря + 7 часов: при 07:00 — до 14:00
	eveningCloseHour = 3
	morningDelay     = 30 * time.Minute // пауза после подъёма перед сводкой
	morningFallback  = 3 * time.Hour    // сводка без данных: якорь + 3 часа
)

// Location — текущий пояс: последний с телефона, иначе значение по умолчанию.
func Location(offset string, def *time.Location) *time.Location {
	if loc, ok := health.OffsetLocation(offset); ok {
		return loc
	}
	return def
}

func at(now time.Time, c config.Clock) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), c.Hour, c.Minute, 0, 0, now.Location())
}

// morningClose — когда утреннее окно закрывается: не позже конца суток.
func morningClose(now time.Time, anchor config.Clock) time.Time {
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, now.Location())
	if c := at(now, anchor).Add(morningWindow); c.Before(end) {
		return c
	}
	return end
}

// MorningDue — пора ли слать утреннюю сводку. now — местное время.
// wakeAt учитывается, только если подъём случился сегодня по местной дате.
func MorningDue(now time.Time, anchor config.Clock, wakeAt *time.Time) bool {
	if !now.Before(morningClose(now, anchor)) {
		return false
	}
	if wakeAt != nil {
		wake := wakeAt.In(now.Location())
		if wake.Format(time.DateOnly) == now.Format(time.DateOnly) && now.Sub(wake) >= morningDelay {
			return true
		}
	}
	return !now.Before(at(now, anchor).Add(morningFallback))
}

// EveningDate — за какую дату пора слать вечернее сообщение. До 03:00 ещё открыто
// окно вчерашнего вечера: бот мог быть выключен или Мак мог спать.
func EveningDate(now time.Time, evening config.Clock) (string, bool) {
	if !now.Before(at(now, evening)) {
		return now.Format(time.DateOnly), true
	}
	if now.Hour() < eveningCloseHour {
		return now.AddDate(0, 0, -1).Format(time.DateOnly), true
	}
	return "", false
}

// WeeklyKey — ключ недели, за которую пора слать сводку: воскресенье после WEEKLY_AT.
func WeeklyKey(now time.Time, weekly config.Clock) (string, bool) {
	if now.Weekday() != time.Sunday || now.Before(at(now, weekly)) {
		return "", false
	}
	return metrics.Monday(now.Format(time.DateOnly)), true
}

// FloorExpired — истекло ли окно отметки: кнопка живёт до 12:00 следующего местного дня.
func FloorExpired(now time.Time, date string) bool {
	d, err := time.ParseInLocation(time.DateOnly, date, now.Location())
	if err != nil {
		return true
	}
	return !now.Before(time.Date(d.Year(), d.Month(), d.Day()+1, 12, 0, 0, 0, now.Location()))
}

// Backoff — экспоненциальная пауза при недоступном Telegram, до 5 минут.
type Backoff struct {
	next  time.Time
	delay time.Duration
}

const (
	backoffMin = time.Minute
	backoffMax = 5 * time.Minute
)

// Ready — можно ли пробовать снова.
func (b *Backoff) Ready(now time.Time) bool { return !now.Before(b.next) }

// Fail увеличивает паузу после неудачи.
func (b *Backoff) Fail(now time.Time) {
	switch {
	case b.delay == 0:
		b.delay = backoffMin
	case b.delay*2 > backoffMax:
		b.delay = backoffMax
	default:
		b.delay *= 2
	}
	b.next = now.Add(b.delay)
}

// Reset сбрасывает паузу после успеха.
func (b *Backoff) Reset() { *b = Backoff{} }

// Run вызывает fn сразу и затем с заданным интервалом до отмены ctx.
func Run(ctx context.Context, interval time.Duration, fn func(context.Context, time.Time)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	fn(ctx, time.Now())
	for {
		select {
		case t := <-ticker.C:
			fn(ctx, t)
		case <-ctx.Done():
			return
		}
	}
}
