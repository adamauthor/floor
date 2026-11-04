package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/render"
	"floor/internal/schedule"
	"floor/internal/store"
)

// Префиксы кнопок. Telegram ограничивает данные кнопки 64 байтами.
const (
	callbackNoop  = "noop"
	callbackMark  = "m:"  // m:<дата>:<id>, m:<дата>:*all, m:<дата>:*done
	callbackWeek  = "w:"  // w:<понедельник>:mood:<1-5>, w:<понедельник>:a:<id|*>
	callbackMonth = "mo:" // mo:<YYYY-MM>:a:<id>:<1-5>, mo:<YYYY-MM>:p<1|2>:<0-3>, mo:<YYYY-MM>:rel:<id>, mo:<YYYY-MM>:keep

	markAll  = "*all"
	markDone = "*done"
)

// levels переводит отметки из хранилища в простые карты для render и metrics.
func levels(in map[string]store.Marks) map[string]map[string]int {
	out := make(map[string]map[string]int, len(in))
	for date, m := range in {
		out[date] = m
	}
	return out
}

// markRows — кнопки вечерней отметки: по привычке в ряд и ряд «Всё на полу · Готово».
func markRows(date string, active []habits.Habit, marks store.Marks) [][]Button {
	rows := make([][]Button, 0, len(active)+1)
	for _, h := range active {
		level, ok := marks[h.ID]
		rows = append(rows, []Button{{Text: render.MarkButton(h, level, ok), Data: callbackMark + date + ":" + h.ID}})
	}
	return append(rows, []Button{
		{Text: "Всё на полу", Data: callbackMark + date + ":" + markAll},
		{Text: "Готово", Data: callbackMark + date + ":" + markDone},
	})
}

// onMark — нажатие в вечерней отметке: привычка по кругу · → пол → цель → ·, или итог.
func (a *App) onMark(ctx context.Context, rest string, msgID int) string {
	date, id, ok := strings.Cut(rest, ":")
	if _, err := time.Parse(time.DateOnly, date); !ok || err != nil || id == "" {
		a.Log.Warn("callback_invalid")
		return ""
	}
	local := a.Now().In(a.location(ctx))
	if schedule.FloorExpired(local, date) {
		return render.ExpiredReply
	}
	a.markMu.Lock()
	defer a.markMu.Unlock()

	active := a.currentHabits(ctx, date).Active()
	all, err := a.Store.MarksRange(ctx, metrics.AddDays(date, -1), date)
	if err != nil {
		a.Log.Error("mark_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	marks := all[date]
	if marks == nil {
		marks = store.Marks{}
	}

	switch id {
	case markAll, markDone:
		for _, h := range active {
			if _, ok := marks[h.ID]; !ok {
				marks[h.ID] = store.LevelMiss
				if id == markAll {
					marks[h.ID] = store.LevelFloor
				}
			}
		}
		return a.finishMarks(ctx, local, date, msgID, active, marks, all[metrics.AddDays(date, -1)])
	}

	level, marked := marks[id]
	next := store.LevelFloor
	switch {
	case !marked:
	case level == store.LevelFloor:
		next = store.LevelGoal
	default:
		next = -1 // снова без отметки
	}
	if err := a.Store.SetMark(ctx, date, id, next, local); err != nil {
		a.Log.Error("mark_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	if next < 0 {
		delete(marks, id)
	} else {
		marks[id] = next
	}
	text := render.Evening(active, a.conf().WakeAnchor.String())
	if err := a.TG.EditRows(ctx, msgID, text, markRows(date, active, marks)); err != nil && !errors.Is(err, ErrMessageGone) {
		a.Log.Warn("callback_edit_failed", "error", err)
	}
	return ""
}

// finishMarks сохраняет итог дня: неотмеченное — мимо, пол дня взят, если всё хотя бы на полу.
func (a *App) finishMarks(ctx context.Context, local time.Time, date string, msgID int,
	active []habits.Habit, marks, yesterday store.Marks) string {
	status := metrics.StatusYes
	again := make(map[string]bool)
	for _, h := range active {
		if err := a.Store.SetMark(ctx, date, h.ID, marks[h.ID], local); err != nil {
			a.Log.Error("mark_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		if marks[h.ID] == store.LevelMiss {
			status = metrics.StatusNo
			if was, ok := yesterday[h.ID]; ok && was == store.LevelMiss {
				again[h.ID] = true
			}
		}
	}
	if err := a.Store.SetFloor(ctx, date, local.Format("-07:00"), status, local); err != nil {
		a.Log.Error("callback_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	a.Log.Info("floor_marked", "date", date, "status", status)
	if err := a.TG.Edit(ctx, msgID, render.MarksReply(active, marks, again)); err != nil && !errors.Is(err, ErrMessageGone) {
		a.Log.Warn("callback_edit_failed", "error", err)
	}
	return ""
}

// returnStep — шаг дня после пропуска: пол первой привычки, пропущенной вчера.
// Пусто — вчера пропусков не было, шаг подбирает Claude.
func (a *App) returnStep(ctx context.Context, date string) string {
	yesterday := metrics.AddDays(date, -1)
	all, err := a.Store.MarksRange(ctx, yesterday, yesterday)
	if err != nil {
		a.Log.Warn("return_step_failed", "error", err)
		return ""
	}
	for _, h := range a.currentHabits(ctx, date).Active() {
		if level, ok := all[yesterday][h.ID]; ok && level == store.LevelMiss {
			return render.ReturnStep(h, a.conf().WakeAnchor.String())
		}
	}
	return ""
}
