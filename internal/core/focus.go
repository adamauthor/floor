package core

import (
	"context"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/money"
	"floor/internal/render"
	"floor/internal/store"
)

// Где поднажать считает код по базе, а не Claude: дни, отметки, тренировки и сверки.
// Область ниже пола — меньше 5 из 7 дней (тренировки — меньше пола недели).

const (
	focusWeek  = 2
	focusMonth = 3
	kvMonthSum = "month_summary_sent"
)

// period — цифры за [from, to] по каждой области.
type period struct {
	days, floorDays int
	wake, steps     metrics.Area
	stepsSum        int
	habits          []metrics.Area
	training        []metrics.Area
	titles          map[string]string
	money           *render.FocusItem
	moneyLine       string
}

func (a *App) period(ctx context.Context, from, to, today string, f habits.File) (period, error) {
	p := period{titles: map[string]string{"wake": "⏰ Подъём", "steps": "👣 Шаги"}}
	days, err := a.Store.Days(ctx, from, to)
	if err != nil {
		return p, err
	}
	marks, err := a.Store.MarksRange(ctx, from, to)
	if err != nil {
		return p, err
	}
	p.wake.Key, p.steps.Key = "wake", "steps"
	for _, d := range days {
		if d.FloorStatus == metrics.StatusYes || d.FloorStatus == metrics.StatusNo {
			p.days++
			if d.FloorStatus == metrics.StatusYes {
				p.floorDays++
			}
		}
		if d.WakeDeltaMin != nil {
			p.wake.Of++
			if metrics.InNorm(*d.WakeDeltaMin, a.conf().WakeToleranceMin) {
				p.wake.Done++
			}
		}
		if d.Steps != nil && d.Date != today {
			p.steps.Of++
			p.stepsSum += *d.Steps
			if *d.Steps >= a.conf().StepsFloor {
				p.steps.Done++
			}
		}
	}
	p.wake.Need, p.steps.Need = metrics.NeedFor(p.wake.Of), metrics.NeedFor(p.steps.Of)

	for _, h := range f.Daily(to) {
		ar := metrics.Area{Key: h.ID}
		for date := from; date <= to; date = metrics.AddDays(date, 1) {
			if date < h.Since || len(marks[date]) == 0 {
				continue
			}
			ar.Of++
			if level, ok := marks[date][h.ID]; ok && level >= store.LevelFloor {
				ar.Done++
			}
		}
		ar.Need = metrics.NeedFor(ar.Of)
		p.titles[h.ID] = render.HabitName(h)
		p.habits = append(p.habits, ar)
	}

	trained := a.trainingDays(ctx, from, to)
	for _, h := range f.WeeklyHabits() {
		ar := metrics.Area{Key: h.ID, Weekly: true}
		for monday := metrics.Monday(from); monday <= to; monday = metrics.AddDays(monday, 7) {
			sunday := metrics.AddDays(monday, 6)
			if monday < from || sunday > to || sunday < h.Since {
				continue // считаются только целые недели
			}
			n := 0
			for date := monday; date <= sunday; date = metrics.AddDays(date, 1) {
				if trained[date] > 0 {
					n++
				}
			}
			ar.Done += min(n, h.PerWeek)
			ar.Of += h.PerWeek
			ar.Need += h.WeekFloor()
		}
		p.titles[h.ID] = render.HabitName(h)
		p.training = append(p.training, ar)
	}

	if r, _ := a.lastReport(ctx, today); r != nil {
		p.moneyLine = render.MoneyLine(*r, today)
		if r.Worst() == money.OffTrack {
			p.money = &render.FocusItem{Title: "💰 Деньги", Note: "план не держится 🔴, разбор — в последней сверке"}
		}
	}
	return p, nil
}

func (p period) item(ar metrics.Area) render.FocusItem {
	unit := "пол"
	if ar.Weekly {
		unit = "тренировки"
	}
	return render.FocusItem{Title: p.titles[ar.Key], Done: ar.Done, Of: ar.Of, Unit: unit}
}

// known — хватает ли данных, чтобы судить хоть об одной области.
func (p period) known() bool {
	for _, ar := range append(append([]metrics.Area{p.wake, p.steps}, p.habits...), p.training...) {
		if ar.Of >= 3 || ar.Weekly && ar.Of > 0 {
			return true
		}
	}
	return false
}

// focus — области ниже пола, самые отстающие первыми; деньги — последней строкой.
func (p period) focus(n int) []render.FocusItem {
	areas := append([]metrics.Area{p.wake, p.steps}, p.habits...)
	areas = append(areas, p.training...)
	var out []render.FocusItem
	for _, ar := range metrics.Weak(areas, n) {
		out = append(out, p.item(ar))
	}
	if p.money != nil {
		out = append(out, *p.money)
	}
	return out
}

// weekFocus — «поднажать» за неделю с понедельника monday; ok = false — данных мало.
func (a *App) weekFocus(ctx context.Context, monday, today string, f habits.File) ([]render.FocusItem, bool) {
	p, err := a.period(ctx, monday, metrics.AddDays(monday, 6), today, f)
	if err != nil {
		a.Log.Warn("focus_failed", "error", err)
		return nil, false
	}
	return p.focus(focusWeek), p.known()
}

// mondayFocus — строка утра понедельника: главное отставание прошлой недели.
func (a *App) mondayFocus(ctx context.Context, date string, f habits.File) string {
	items, _ := a.weekFocus(ctx, metrics.AddDays(date, -7), date, f)
	if len(items) == 0 {
		return ""
	}
	return render.FocusMorning(items[0])
}

// monthSummary — в первое воскресенье месяца: цифры прошлого месяца и где поднажать.
// Если данных за месяц нет (бот только запущен), сводка не приходит.
func (a *App) monthSummary(ctx context.Context, local time.Time, f habits.File) {
	month := local.Format("2006-01")
	if local.Day() > 7 {
		return
	}
	if sent, err := a.Store.GetKV(ctx, kvMonthSum); err != nil || sent == month {
		return
	}
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
	from := first.AddDate(0, -1, 0).Format(time.DateOnly)
	to := first.AddDate(0, 0, -1).Format(time.DateOnly)
	today := local.Format(time.DateOnly)
	p, err := a.period(ctx, from, to, today, f)
	if err != nil {
		a.Log.Warn("month_summary_failed", "error", err)
		return
	}
	if err := a.Store.SetKV(ctx, kvMonthSum, month); err != nil {
		a.Log.Warn("month_summary_failed", "error", err)
		return
	}
	if !p.known() {
		return
	}
	d := render.MonthData{
		Month: from[:7], Days: p.days, FloorDays: p.floorDays,
		Wake: p.item(p.wake), Steps: p.item(p.steps), Money: p.moneyLine, Focus: p.focus(focusMonth),
	}
	if p.steps.Of > 0 {
		d.StepsAvg = p.stepsSum / p.steps.Of
	}
	for _, ar := range p.habits {
		d.Habits = append(d.Habits, p.item(ar))
	}
	for _, ar := range p.training {
		if ar.Of > 0 {
			d.Training = append(d.Training, p.item(ar))
		}
	}
	if _, err := a.say(ctx, kindReport, render.Month(d), nil); err != nil {
		a.Log.Warn("month_summary_failed", "error", err)
		return
	}
	a.Log.Info("month_summary_sent", "month", from[:7])
}
