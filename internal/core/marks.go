package core

import (
	"context"
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

// eveningView — текст и кнопки вечерней отметки. Недельные привычки — строкой, если
// тренировка уже записана, иначе кнопкой «Была без часов».
func (a *App) eveningView(ctx context.Context, date string, f habits.File, marks store.Marks) (string, [][]Button) {
	daily := f.Daily(date)
	text := render.Evening(daily, a.conf().WakeAnchor.String())
	var gym []habits.Habit
	if weekly := f.WeeklyHabits(); len(weekly) > 0 {
		minutes := a.trainingDays(ctx, date, date)[date]
		for _, h := range weekly {
			if minutes > 0 {
				text += "\n" + render.WorkoutDone(h, minutes)
			} else {
				gym = append(gym, h)
			}
		}
	}
	return text, markRows(date, daily, marks, gym)
}

// markRows — кнопки вечерней отметки: по привычке в ряд, «Была без часов» и ряд «Всё на полу · Готово».
func markRows(date string, active []habits.Habit, marks store.Marks, gym []habits.Habit) [][]Button {
	rows := make([][]Button, 0, len(active)+2)
	for _, h := range active {
		level, ok := marks[h.ID]
		rows = append(rows, []Button{{Text: render.MarkButton(h, level, ok), Data: callbackMark + date + ":" + h.ID}})
	}
	for _, h := range gym {
		rows = append(rows, []Button{{Text: render.HabitEmoji(h) + " Была без часов", Data: callbackMark + date + ":" + markGym}})
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

	f := a.currentHabits(ctx, date)
	active := f.Daily(date)
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
	case markGym:
		if err := a.manualWorkout(ctx, date, local); err != nil {
			a.Log.Error("mark_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		a.Log.Info("workout_manual", "date", date)
		text, rows := a.eveningView(ctx, date, f, marks)
		a.edit(ctx, msgID, text, rows)
		return ""
	case markAll, markDone:
		for _, h := range active {
			if _, ok := marks[h.ID]; !ok {
				marks[h.ID] = store.LevelMiss
				if id == markAll {
					marks[h.ID] = store.LevelFloor
				}
			}
		}
		return a.finishMarks(ctx, local, date, msgID, f, marks, all[metrics.AddDays(date, -1)])
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
	text, rows := a.eveningView(ctx, date, f, marks)
	a.edit(ctx, msgID, text, rows)
	return ""
}

// finishMarks сохраняет итог дня: неотмеченное — мимо, пол дня взят, если всё хотя бы на полу.
func (a *App) finishMarks(ctx context.Context, local time.Time, date string, msgID int,
	f habits.File, marks, yesterday store.Marks) string {
	active := f.Daily(date)
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
	text := render.MarksReply(active, marks, again)
	if minutes := a.trainingDays(ctx, date, date)[date]; minutes > 0 {
		for _, h := range f.WeeklyHabits() {
			text += "\n" + render.WorkoutDone(h, minutes)
		}
	}
	a.edit(ctx, msgID, text, nil)
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
	f := a.currentHabits(ctx, date)
	if step := a.workoutReturnStep(ctx, date, f); step != "" {
		return step
	}
	for _, h := range f.Daily(yesterday) {
		if level, ok := all[yesterday][h.ID]; ok && level == store.LevelMiss {
			return render.ReturnStep(h, a.conf().WakeAnchor.String())
		}
	}
	return ""
}
