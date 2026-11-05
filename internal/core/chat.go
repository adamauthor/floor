package core

import (
	"context"
	"errors"
	"time"

	"floor/internal/store"
)

// Чат — витрина, а не хранилище: всё нужное для сводок лежит в базе (дни, отметки, ответы,
// сверки, тренировки), поэтому служебные сообщения бот убирает сам. Остаются карточка,
// итоги недели и месяца, денежные отчёты и предупреждения по плану.
//
// Telegram даёт удалить сообщение только в первые 48 часов, поэтому всё, что не остаётся
// навсегда, убирается не позже maxLife.
const (
	kindCard    = "card"    // закреплённая карточка привычек — не трогать
	kindReport  = "report"  // итоги недели и месяца, деньги, предупреждения — остаются
	kindMorning = "morning" // утро — до следующего утра
	kindEvening = "evening" // вечерняя отметка — до следующего вечера
	kindPrompt  = "prompt"  // вопрос с кнопками — через promptDone после ответа
	kindRemind  = "remind"  // напоминание о сверке — до сверки
	kindAlert   = "alert"   // сбой данных или файла — до maxLife
	kindNotice  = "notice"  // подсказки, ошибки, «не понял» — через час
	kindChat    = "chat"    // ответ Claude — через сутки
	kindUser    = "user"    // сообщение владельца — через сутки
	kindCommand = "command" // /start, /settings — сразу
	kindSecret  = "secret"  // сообщение с остатками — сразу после разбора

	maxLife    = 46 * time.Hour
	promptDone = 12 * time.Hour
	sweepBatch = 30
)

var lifetime = map[string]time.Duration{
	kindNotice:  time.Hour,
	kindChat:    24 * time.Hour,
	kindUser:    24 * time.Hour,
	kindCommand: 0,
	kindSecret:  0,
}

// replaced — виды, где новое сообщение убирает прошлые.
var replaced = map[string]bool{kindMorning: true, kindEvening: true, kindRemind: true}

type incomingKey struct{}

// WithIncoming кладёт в контекст id входящего сообщения владельца: ядро решит, когда его убрать.
func WithIncoming(ctx context.Context, msgID int) context.Context {
	return context.WithValue(ctx, incomingKey{}, msgID)
}

func incoming(ctx context.Context) int {
	id, _ := ctx.Value(incomingKey{}).(int)
	return id
}

// say отправляет сообщение и записывает его в журнал.
func (a *App) say(ctx context.Context, kind, text string, buttons []Button) (int, error) {
	id, err := a.TG.Send(ctx, text, buttons)
	if err == nil {
		a.track(ctx, id, kind)
	}
	return id, err
}

// sayRows — то же с кнопками в несколько рядов.
func (a *App) sayRows(ctx context.Context, kind, text string, rows [][]Button) (int, error) {
	id, err := a.TG.SendRows(ctx, text, rows)
	if err == nil {
		a.track(ctx, id, kind)
	}
	return id, err
}

// track записывает сообщение и срок, когда его убрать.
func (a *App) track(ctx context.Context, msgID int, kind string) {
	if msgID == 0 {
		return
	}
	now := a.Now()
	var at *time.Time
	if kind != kindCard && kind != kindReport {
		life, ok := lifetime[kind]
		if !ok {
			life = maxLife
		}
		t := now.Add(life)
		at = &t
	}
	if err := a.Store.TrackMessage(ctx, msgID, kind, now, at); err != nil {
		a.Log.Warn("chat_track_failed", "error", err)
		return
	}
	if replaced[kind] {
		a.dropKind(ctx, kind, msgID)
	}
}

// trackIncoming записывает входящее сообщение владельца из контекста.
func (a *App) trackIncoming(ctx context.Context, kind string) {
	a.track(ctx, incoming(ctx), kind)
}

// dropKind убирает прошлые сообщения вида kind на ближайшем тике.
func (a *App) dropKind(ctx context.Context, kind string, keep int) {
	if err := a.Store.ScheduleKind(ctx, kind, keep, a.Now()); err != nil {
		a.Log.Warn("chat_track_failed", "error", err)
	}
}

// answered — у вопроса пропали кнопки: через promptDone он больше не нужен.
func (a *App) answered(ctx context.Context, msgID int) {
	kind, ok, err := a.Store.MessageKind(ctx, msgID)
	if err != nil || !ok || kind != kindPrompt {
		return
	}
	if err := a.Store.ScheduleDelete(ctx, msgID, a.Now().Add(promptDone)); err != nil {
		a.Log.Warn("chat_track_failed", "error", err)
	}
}

// keep оставляет сообщение в чате: например, совет обратиться к специалисту.
func (a *App) keep(ctx context.Context, msgID int) {
	if err := a.Store.TrackMessage(ctx, msgID, kindReport, a.Now(), nil); err != nil {
		a.Log.Warn("chat_track_failed", "error", err)
	}
}

// sweep убирает из чата сообщения, срок которых прошёл.
func (a *App) sweep(ctx context.Context, now time.Time) {
	due, err := a.Store.DueMessages(ctx, now, sweepBatch)
	if err != nil {
		a.Log.Warn("chat_sweep_failed", "error", err)
		return
	}
	removed := 0
	for _, m := range due {
		if now.Sub(m.SentAt) < store.DeleteWindow-time.Minute {
			if err := a.TG.Delete(ctx, m.ID); err != nil && !errors.Is(err, ErrMessageGone) {
				a.Log.Warn("chat_delete_failed", "kind", m.Kind, "error", err)
				return // Telegram недоступен — попробуем на следующем тике
			}
			removed++
		}
		if err := a.Store.MarkDeleted(ctx, m.ID); err != nil {
			a.Log.Warn("chat_sweep_failed", "error", err)
			return
		}
	}
	if removed > 0 {
		a.Log.Info("chat_swept", "count", removed)
	}
}
