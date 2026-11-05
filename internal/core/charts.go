package core

import (
	"context"
	"fmt"
	"time"

	"floor/internal/chart"
	"floor/internal/command"
	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/money"
	"floor/internal/render"
	"floor/internal/store"
)

// Графики рисует код по базе (internal/chart), Claude только просит нужный.
// Картинка идёт после текста: сбой графика не мешает сводке.

// periodChart — сетка привычек, шаги и подъём за [from, to].
func (a *App) periodChart(ctx context.Context, from, to, today, title string, f habits.File) (chart.Period, error) {
	conf := a.conf()
	p := chart.Period{Title: title, StepsFloor: conf.StepsFloor, WakeAnchor: conf.WakeAnchor.String(), Tolerance: conf.WakeToleranceMin}
	days, err := a.Store.Days(ctx, from, to)
	if err != nil {
		return p, err
	}
	marks, err := a.Store.MarksRange(ctx, from, to)
	if err != nil {
		return p, err
	}
	trained := a.trainingDays(ctx, from, to)
	byDate := map[string]store.Day{}
	for _, d := range days {
		byDate[d.Date] = d
	}
	floor := chart.Row{Name: "Пол дня", Gap: true}
	held, judged := 0, 0
	daily := f.Daily(to)
	rows := make([]chart.Row, len(daily))
	weekly := f.WeeklyHabits()
	train := make([]chart.Row, len(weekly))
	for date := from; date <= to; date = metrics.AddDays(date, 1) {
		p.Dates = append(p.Dates, date)
		d := byDate[date]
		switch d.FloorStatus {
		case metrics.StatusYes:
			floor.Cells = append(floor.Cells, chart.Floor)
			held++
			judged++
		case metrics.StatusNo:
			floor.Cells = append(floor.Cells, chart.Miss)
			judged++
		default:
			floor.Cells = append(floor.Cells, chart.None)
		}
		for i, h := range daily {
			rows[i].Name = render.HabitName(h)
			level, ok := marks[date][h.ID]
			rows[i].Cells = append(rows[i].Cells, markCell(level, ok && date >= h.Since && date <= today))
		}
		for i, h := range weekly {
			train[i].Name = render.HabitName(h)
			c := chart.None
			if trained[date] > 0 {
				c = chart.Goal
			}
			train[i].Cells = append(train[i].Cells, c)
		}
		var steps, wake *int
		if d.Date != "" && date < today {
			steps = d.Steps
		}
		if d.Date != "" {
			wake = d.WakeDeltaMin
		}
		p.Steps, p.Wake = append(p.Steps, steps), append(p.Wake, wake)
	}
	p.Rows = append(append([]chart.Row{floor}, rows...), train...)
	if judged > 0 {
		p.Subtitle = fmt.Sprintf("Пол удержан %d из %d %s", held, judged, render.Plural(judged, "дня", "дней", "дней"))
	}
	return p, nil
}

func markCell(level int, ok bool) chart.Cell {
	switch {
	case !ok:
		return chart.None
	case level >= store.LevelGoal:
		return chart.Goal
	case level >= store.LevelFloor:
		return chart.Floor
	}
	return chart.Miss
}

// weekChart — неделя с понедельника monday.
func (a *App) weekChart(ctx context.Context, monday, today string) ([]byte, error) {
	sunday := metrics.AddDays(monday, 6)
	p, err := a.periodChart(ctx, monday, sunday, today, render.WeekRange(monday), a.currentHabits(ctx, today))
	if err != nil {
		return nil, err
	}
	return p.PNG()
}

// monthChart — месяц, в который входит date.
func (a *App) monthChart(ctx context.Context, date, today string) ([]byte, error) {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return nil, err
	}
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	from, to := first.Format(time.DateOnly), first.AddDate(0, 1, -1).Format(time.DateOnly)
	p, err := a.periodChart(ctx, from, to, today, render.MonthName(from), a.currentHabits(ctx, today))
	if err != nil {
		return nil, err
	}
	return p.PNG()
}

// moneyChart — план против факта по каждой цели с суммой.
func (a *App) moneyChart(ctx context.Context, today string) ([]byte, error) {
	f, ok := a.currentMoney(ctx, today)
	if !ok {
		return nil, fmt.Errorf("плана денег нет")
	}
	snaps, err := a.Store.Snapshots(ctx, f.Baseline.Date)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 || !snaps[0].Baseline {
		snaps = append([]money.Snapshot{f.BaselineSnapshot()}, snaps...)
	}
	return moneyPicture(f, snaps).PNG()
}

func moneyPicture(f money.File, snaps []money.Snapshot) chart.Money {
	last := snaps[len(snaps)-1]
	r := f.Calc(last, nil)
	m := chart.Money{Title: "Деньги", From: f.Baseline.Date, To: last.Date}
	if last.Baseline {
		m.Subtitle = "Сверок ещё не было — показан план от точки отсчёта"
	} else {
		m.Subtitle = fmt.Sprintf("Сверка %s · %s", render.DayMonth(last.Date), statusWord(r.Worst()))
	}
	for _, g := range f.Goals {
		if (g.Kind == money.GoalDebtZero || g.Kind == money.GoalBalance) && g.By > m.To {
			m.To = g.By
		}
	}
	m.To = metrics.AddDays(m.To, 7)
	df := f.ByRole(money.RoleDebtFund)
	for _, g := range f.Goals {
		var pn chart.Panel
		switch g.Kind {
		case money.GoalDebtZero:
			pn = chart.Panel{Title: titleOr(g.Title, "Чистый долг"), By: g.By}
			for date := m.From; date <= m.To; date = metrics.AddDays(date, 1) {
				pn.Plan = append(pn.Plan, chart.Point{Date: date, Value: max(f.PlanNetDebt(date), 0)})
			}
			for _, s := range snaps {
				if s.DebtTotal != nil {
					pn.Actual = append(pn.Actual, chart.Point{Date: s.Date, Value: *s.DebtTotal - s.Balances[df]})
				}
			}
			if r.Debt != nil && r.Debt.ClosesOn != "" {
				pn.Note = "закроется " + render.DayMonth(r.Debt.ClosesOn)
			}
		case money.GoalBalance:
			target := g.Target
			pn = chart.Panel{Title: titleOr(g.Title, g.Account), By: g.By, Target: &target}
			for date := m.From; date <= m.To; date = metrics.AddDays(date, 1) {
				pn.Plan = append(pn.Plan, chart.Point{Date: date, Value: f.PlanBalance(g.Account, date)})
			}
			for _, s := range snaps {
				if v, ok := s.Balances[g.Account]; ok {
					pn.Actual = append(pn.Actual, chart.Point{Date: s.Date, Value: v})
				}
			}
			for _, b := range r.Balances {
				if b.Goal.ID == g.ID {
					pn.Note = "к сроку " + render.Number(int(b.AtBy))
				}
			}
		default:
			continue
		}
		m.Panels = append(m.Panels, pn)
	}
	return m
}

func titleOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func statusWord(s money.Status) string {
	return [...]string{"по плану", "отстаёт, к сроку успевает", "к сроку не успевает"}[s]
}

// Chart рисует график what (week, month, money) для даты date.
func (a *App) Chart(ctx context.Context, what, date string) ([]byte, error) {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	switch what {
	case command.ChartWeek:
		return a.weekChart(ctx, metrics.Monday(date), today)
	case command.ChartMonth:
		return a.monthChart(ctx, date, today)
	case command.ChartMoney:
		return a.moneyChart(ctx, today)
	}
	return nil, fmt.Errorf("нет графика %q", what)
}

// sendChart рисует и отправляет график; ошибка только в журнал — текст уже ушёл.
func (a *App) sendChart(ctx context.Context, kind, what, date string) {
	png, err := a.Chart(ctx, what, date)
	if err == nil {
		_, err = a.sayPhoto(ctx, kind, png, "")
	}
	if err != nil {
		a.Log.Warn("chart_failed", "chart", what, "error", err)
		return
	}
	a.Log.Info("chart_sent", "chart", what)
}
