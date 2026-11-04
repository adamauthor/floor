package core

import (
	"context"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/render"
	"floor/internal/store"
)

const markGym = "*gym" // m:<дата>:*gym — «Была без часов»

// workouts — тренировки за [from, to] по датам, не короче WORKOUT_MIN_MINUTES.
func (a *App) workouts(ctx context.Context, from, to string) map[string][]store.Workout {
	ws, err := a.Store.Workouts(ctx, from, to, a.conf().WorkoutMinMinutes)
	if err != nil {
		a.Log.Warn("workouts_failed", "error", err)
		return nil
	}
	return ws
}

// trainingDays — дни с тренировкой за [from, to]: дата → минуты.
func (a *App) trainingDays(ctx context.Context, from, to string) map[string]int {
	out := map[string]int{}
	for date, ws := range a.workouts(ctx, from, to) {
		for _, w := range ws {
			out[date] += w.Minutes
		}
	}
	return out
}

// weekCount — дней с тренировкой на неделе, в которую входит дата.
func (a *App) weekCount(ctx context.Context, date string) int {
	monday := metrics.Monday(date)
	return len(a.trainingDays(ctx, monday, metrics.AddDays(monday, 6)))
}

// workoutLines — строки утра про недельные привычки.
func (a *App) workoutLines(ctx context.Context, date string, f habits.File) []string {
	weekly := f.WeeklyHabits()
	if len(weekly) == 0 {
		return nil
	}
	n := a.weekCount(ctx, date)
	monday := metrics.Monday(date) == date
	var out []string
	for _, h := range weekly {
		out = append(out, render.WorkoutMorning(h, n, monday))
	}
	return out
}

// workoutReturnStep — в понедельник после двух недель ниже пола шаг дня — пол тренировки.
func (a *App) workoutReturnStep(ctx context.Context, date string, f habits.File) string {
	if metrics.Monday(date) != date {
		return ""
	}
	for _, h := range f.WeeklyHabits() {
		below := 0
		for w := 1; w <= 2; w++ {
			monday := metrics.AddDays(date, -7*w)
			if monday < h.Since {
				break
			}
			if len(a.trainingDays(ctx, monday, metrics.AddDays(monday, 6))) < h.WeekFloor() {
				below++
			}
		}
		if below == 2 {
			return render.ReturnStep(h, a.conf().WakeAnchor.String())
		}
	}
	return ""
}

// weekCounts — дней с тренировкой на текущей неделе для каждой недельной привычки.
func (a *App) weekCounts(ctx context.Context, date string, f habits.File) map[string]int {
	weekly := f.WeeklyHabits()
	if len(weekly) == 0 {
		return nil
	}
	n := a.weekCount(ctx, date)
	out := map[string]int{}
	for _, h := range weekly {
		out[h.ID] = n
	}
	return out
}

// manualWorkout — «Была без часов»: одна ручная тренировка за день.
func (a *App) manualWorkout(ctx context.Context, date string, local time.Time) error {
	start := local
	return a.Store.UpsertWorkout(ctx, store.Workout{
		ID: "manual-" + date, Date: date, Start: &start, Minutes: a.conf().WorkoutMinMinutes, Type: "manual", Source: "manual",
	})
}
