package money

import (
	"math"
	"sort"
	"time"
)

// Snapshot — сверка: остатки по счетам и долг на местную дату.
type Snapshot struct {
	Date      string
	DebtTotal *int64
	Balances  map[string]int64
	Carried   map[string]bool // взято из прошлой сверки
	Baseline  bool
}

// Status — состояние линии плана.
type Status int

// Статусы (раздел 2.4).
const (
	OnTrack  Status = iota // ✅ по плану
	Lagging                // ⚠️ отстаёт, но к сроку успевает
	OffTrack               // 🔴 к сроку не успевает
)

// Mark — значок статуса.
func (s Status) Mark() string {
	return [...]string{"✅", "⚠️", "🔴"}[s]
}

// Word — статус для промпта.
func (s Status) Word() string {
	return [...]string{"ok", "lagging", "off_track"}[s]
}

func day(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func ymd(t time.Time) string { return t.Format(time.DateOnly) }

// onDay — дата с днём месяца d; в коротком месяце — последний день.
func onDay(year int, month time.Month, d int) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(year, month, min(d, last), 0, 0, 0, 0, time.UTC)
}

// Occurrence — один перевод по плану.
type Occurrence struct {
	Date   string
	Amount int64
	To     string
}

// Occurrences — переводы в счёт to (пусто — все) с датой в (from, to].
func (f File) Occurrences(account, from, to string) []Occurrence {
	a, b := day(from), day(to)
	var out []Occurrence
	for _, t := range f.Transfers {
		if account != "" && t.To != account {
			continue
		}
		for m := time.Date(a.Year(), a.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(b); m = m.AddDate(0, 1, 0) {
			d := onDay(m.Year(), m.Month(), t.Day)
			if !d.After(a) || d.After(b) || (t.Until != "" && d.After(day(t.Until))) {
				continue
			}
			out = append(out, Occurrence{Date: ymd(d), Amount: t.Amount, To: t.To})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func sum(os []Occurrence) int64 {
	var n int64
	for _, o := range os {
		n += o.Amount
	}
	return n
}

// LastUntil — последняя дата, когда в счёт ещё идут переводы; пусто — бессрочно или переводов нет.
func (f File) LastUntil(account string) string {
	last := ""
	for _, t := range f.Transfers {
		if t.To != account {
			continue
		}
		if t.Until == "" {
			return ""
		}
		if t.Until > last {
			last = t.Until
		}
	}
	return last
}

// BaselineNetDebt — чистый долг на точке отсчёта.
func (f File) BaselineNetDebt() int64 {
	return f.Baseline.DebtTotal - f.Baseline.Balances[f.ByRole(RoleDebtFund)]
}

// PlanNetDebt — чистый долг по плану на дату.
func (f File) PlanNetDebt(date string) int64 {
	return f.BaselineNetDebt() - sum(f.Occurrences(f.ByRole(RoleDebtFund), f.Baseline.Date, date))
}

// PlanBalance — остаток счёта по плану на дату.
func (f File) PlanBalance(account, date string) int64 {
	return f.Baseline.Balances[account] + sum(f.Occurrences(account, f.Baseline.Date, date))
}

func tolerance(target int64) int64 {
	return max(20_000, int64(math.Round(float64(target)*0.02)))
}

// DebtLine — линия долгов.
type DebtLine struct {
	Goal     Goal
	Net      int64 // чистый долг по сверке
	Plan     int64 // по плану на дату сверки
	Status   Status
	Closed   bool   // чистый долг уже ≤ 0
	ClosesOn string // прогноз: дата, когда чистый долг станет ≤ 0; пусто — до конца переводов не станет
	Margin   int64  // запас (> 0) или нехватка (< 0) к сроку цели
}

// BalanceLine — линия цели по остатку счёта.
type BalanceLine struct {
	Goal      Goal
	Actual    int64
	Plan      int64
	Status    Status
	AtBy      int64  // прогноз на срок цели
	Until     string // последний перевод в счёт, если раньше срока
	Shortfall int64  // сколько не хватит к сроку, 0 — хватит
}

// LifeLine — деньги на жизнь до зарплаты.
type LifeLine struct {
	Available int64
	Until     string // ближайший день зарплаты
	Days      int
	PerDay    int64
	BudgetDay int64
	OK        bool
}

// Report — всё, что показывает ответ на сверку.
type Report struct {
	Date      string
	Snapshot  Snapshot
	Debt      *DebtLine
	Balances  []BalanceLine
	Life      LifeLine
	Months    float64 // подушка в месяцах жизни
	NetWorth  int64
	PrevWorth *int64 // чистый капитал на прошлой сверке
	Countdown []Countdown
}

// Countdown — отсчёт до цели-даты.
type Countdown struct {
	Goal Goal
	Days int
}

// Worst — худший статус по долгам и целям.
func (r Report) Worst() Status {
	w := OnTrack
	if r.Debt != nil {
		w = max(w, r.Debt.Status)
	}
	for _, b := range r.Balances {
		w = max(w, b.Status)
	}
	return w
}

// Calc считает план против факта по сверке. prev — прошлая сверка (для изменения капитала).
func (f File) Calc(s Snapshot, prev *Snapshot) Report {
	r := Report{Date: s.Date, Snapshot: s}
	debt := int64(0)
	if s.DebtTotal != nil {
		debt = *s.DebtTotal
	}
	df := f.ByRole(RoleDebtFund)

	for _, g := range f.Goals {
		switch g.Kind {
		case GoalDebtZero:
			if s.DebtTotal == nil {
				continue
			}
			l := DebtLine{Goal: g, Net: debt - s.Balances[df], Plan: f.PlanNetDebt(s.Date)}
			future := f.Occurrences(df, s.Date, "9999-12-31")
			left := l.Net
			if left <= 0 {
				l.Closed, l.ClosesOn = true, s.Date
			}
			for _, o := range future {
				if left <= 0 {
					break
				}
				left -= o.Amount
				if left <= 0 {
					l.ClosesOn = o.Date
				}
			}
			l.Margin = -(l.Net - sum(f.Occurrences(df, s.Date, g.By)))
			l.Status = status(l.Net-l.Plan, tolerance(f.BaselineNetDebt()), l.Margin >= 0)
			r.Debt = &l
		case GoalBalance:
			l := BalanceLine{Goal: g, Actual: s.Balances[g.Account], Plan: f.PlanBalance(g.Account, s.Date)}
			l.AtBy = l.Actual + sum(f.Occurrences(g.Account, s.Date, g.By))
			if u := f.LastUntil(g.Account); u != "" && u < g.By {
				l.Until = u
			}
			l.Shortfall = max(0, g.Target-l.AtBy)
			l.Status = status(l.Plan-l.Actual, tolerance(g.Target), l.Shortfall == 0)
			r.Balances = append(r.Balances, l)
		case GoalDate:
			if left := int(day(g.On).Sub(day(s.Date)).Hours() / 24); left >= 0 {
				r.Countdown = append(r.Countdown, Countdown{Goal: g, Days: left})
			}
		}
	}

	r.Life = f.life(s)
	if f.Budget.LivePerMonth > 0 {
		r.Months = math.Round(float64(s.Balances[f.ByRole(RoleEmergency)])/float64(f.Budget.LivePerMonth)*10) / 10
	}
	r.NetWorth = worth(s)
	if prev != nil && !prev.Baseline {
		w := worth(*prev)
		r.PrevWorth = &w
	}
	return r
}

// status: worse — насколько факт хуже плана; reaches — прогноз к сроку дотягивает.
func status(worse, tol int64, reaches bool) Status {
	switch {
	case !reaches:
		return OffTrack
	case worse > tol:
		return Lagging
	}
	return OnTrack
}

func worth(s Snapshot) int64 {
	var n int64
	for _, v := range s.Balances {
		n += v
	}
	if s.DebtTotal != nil {
		n -= *s.DebtTotal
	}
	return n
}

// NextIncome — ближайший день зарплаты строго после даты.
func (f File) NextIncome(date string) string {
	d := day(date)
	best := ""
	for _, in := range f.Income {
		for _, m := range []time.Time{d, d.AddDate(0, 1, 0)} {
			c := onDay(m.Year(), m.Month(), in.Day)
			if c.After(d) && (best == "" || ymd(c) < best) {
				best = ymd(c)
			}
		}
	}
	return best
}

func (f File) life(s Snapshot) LifeLine {
	l := LifeLine{Until: f.NextIncome(s.Date)}
	for _, a := range f.Accounts {
		if a.Role == RoleSpend || a.Role == RoleLive {
			l.Available += s.Balances[a.ID]
		}
	}
	l.Days = int(day(l.Until).Sub(day(s.Date)).Hours() / 24)
	if l.Days > 0 {
		l.PerDay = l.Available / int64(l.Days)
	}
	budget := float64(f.Budget.LivePerMonth) / 30.4
	l.BudgetDay = int64(math.Round(budget))
	l.OK = float64(l.PerDay) >= budget*0.9
	return l
}

// MonthlyTransfers — сумма переводов в месяц, ещё действующих на дату; account пусто — все.
func (f File) MonthlyTransfers(account, date string) int64 {
	var n int64
	for _, t := range f.Transfers {
		if (account == "" || t.To == account) && (t.Until == "" || t.Until >= date) {
			n += t.Amount
		}
	}
	return n
}

// MonthlyIncome — доход в месяц.
func (f File) MonthlyIncome() int64 {
	var n int64
	for _, in := range f.Income {
		n += in.Amount
	}
	return n
}

// TransfersEnd — последняя дата until среди всех переводов; пусто — есть бессрочные.
func (f File) TransfersEnd() string {
	last := ""
	for _, t := range f.Transfers {
		if t.Until == "" {
			return ""
		}
		if t.Until > last {
			last = t.Until
		}
	}
	return last
}

// Merge — сверка из значений сообщения; отсутствующее берётся из прошлой и помечается.
func Merge(date string, values map[string]int64, prev *Snapshot, f File) Snapshot {
	s := Snapshot{Date: date, Balances: map[string]int64{}, Carried: map[string]bool{}}
	for _, a := range f.Accounts {
		if v, ok := values[a.ID]; ok {
			s.Balances[a.ID] = v
		} else if prev != nil {
			if v, ok := prev.Balances[a.ID]; ok {
				s.Balances[a.ID] = v
				s.Carried[a.ID] = true
			}
		}
	}
	if v, ok := values[DebtKey]; ok {
		s.DebtTotal = &v
	} else if prev != nil && prev.DebtTotal != nil {
		v := *prev.DebtTotal
		s.DebtTotal = &v
		s.Carried[DebtKey] = true
	}
	return s
}

// BaselineSnapshot — точка отсчёта как сверка.
func (f File) BaselineSnapshot() Snapshot {
	s := Snapshot{Date: f.Baseline.Date, Balances: map[string]int64{}, Baseline: true}
	for k, v := range f.Baseline.Balances {
		s.Balances[k] = v
	}
	d := f.Baseline.DebtTotal
	s.DebtTotal = &d
	return s
}
