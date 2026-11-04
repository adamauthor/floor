package core

import (
	"context"
	"strings"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/render"
)

const (
	callbackQueue = "q:" // q:<id>:go, q:<id>:wait

	kvQueueLaunched = "queue_launched"
	kvQueueOffered  = "queue_offered"

	// Условия предложения (SPEC_v2, раздел 4).
	queueFloorDays  = 10
	queueWindowDays = 14
	queueGapDays    = 14
)

// queueOffer — в воскресенье после вопросов: есть место и пол держится — предложить
// первую привычку из очереди. Не чаще одной новой привычки в две недели.
func (a *App) queueOffer(ctx context.Context, local time.Time, f habits.File) {
	queue := f.Queue()
	if len(queue) == 0 || len(f.Active()) >= habits.MaxActive {
		return
	}
	today := local.Format(time.DateOnly)
	monday := metrics.Monday(today)
	if offered, _ := a.Store.GetKV(ctx, kvQueueOffered); offered == monday {
		return
	}
	if last, _ := a.Store.GetKV(ctx, kvQueueLaunched); last != "" && metrics.AddDays(last, queueGapDays) > today {
		return
	}
	days, err := a.Store.Days(ctx, metrics.AddDays(today, 1-queueWindowDays), today)
	if err != nil {
		a.Log.Warn("queue_failed", "error", err)
		return
	}
	held := 0
	for _, d := range days {
		if d.FloorStatus == metrics.StatusYes {
			held++
		}
	}
	if held < queueFloorDays {
		return
	}
	h := queue[0]
	rows := [][]Button{{
		{Text: "Запускаем", Data: callbackQueue + h.ID + ":go"},
		{Text: "Ещё неделю", Data: callbackQueue + h.ID + ":wait"},
	}}
	if _, err := a.TG.SendRows(ctx, render.QueueOffer(h, held, queueWindowDays), rows); err != nil {
		a.Log.Warn("queue_failed", "error", err)
		return
	}
	if err := a.Store.SetKV(ctx, kvQueueOffered, monday); err != nil {
		a.Log.Warn("queue_failed", "error", err)
	}
	a.Log.Info("queue_offered", "habit", h.ID)
}

// onQueue — ответ на предложение из очереди.
func (a *App) onQueue(ctx context.Context, rest string, msgID int) string {
	id, action, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	if action != "go" {
		a.edit(ctx, msgID, "Ок, спрошу через неделю.", nil)
		return ""
	}
	local := a.Now().In(a.location(ctx))
	today := local.Format(time.DateOnly)
	a.markMu.Lock()
	defer a.markMu.Unlock()
	f := a.currentHabits(ctx, today)
	h, found := f.Find(id)
	if !found || !h.Queued {
		a.edit(ctx, msgID, "Эта привычка уже не в очереди.", nil)
		return ""
	}
	if len(f.Active()) >= habits.MaxActive {
		return "Уже пять активных привычек."
	}
	start := metrics.AddDays(metrics.Monday(today), 7)
	for i := range f.Habits {
		if f.Habits[i].ID == id {
			f.Habits[i].Queued = false
			f.Habits[i].Since = start
		}
	}
	if err := habits.Save(a.conf().HabitsFile, f); err != nil {
		a.Log.Error("queue_failed", "error", err)
		return "Не получилось, попробуй ещё раз."
	}
	if err := a.Store.SetKV(ctx, kvQueueLaunched, today); err != nil {
		a.Log.Warn("queue_failed", "error", err)
	}
	a.Log.Info("queue_launched", "habit", id)
	a.edit(ctx, msgID, "Запускаем «"+escape(render.HabitName(h))+"» с понедельника, "+render.DayMonth(start)+". Карточка обновлена.", nil)
	go a.Tick(context.WithoutCancel(ctx), a.Now())
	return ""
}
