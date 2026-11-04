package money

// Warning — дыра в плане (раздел 2.6). Не ошибка файла, а открытый вопрос.
type Warning struct {
	Kind    string // WarnAfterTransfers, WarnTrip, WarnOverspend
	Goal    Goal
	Until   string       // последний перевод в счёт цели
	AtBy    int64        // сколько будет к сроку без новых переводов
	Missing []Occurrence // переводы, которых не хватает между Until и сроком
	Gap     int64        // на сколько расходы в месяц больше дохода
}

// Виды предупреждений.
const (
	WarnAfterTransfers = "after_transfers"
	WarnTrip           = "trip"
	WarnOverspend      = "overspend"
)

// Checks ищет дыры в плане от точки отсчёта.
func (f File) Checks() []Warning {
	var out []Warning
	var cushionBy string
	for _, g := range f.Goals {
		if g.Kind != GoalBalance {
			continue
		}
		if a, ok := f.Account(g.Account); ok && a.Role == RoleEmergency && (cushionBy == "" || g.By > cushionBy) {
			cushionBy = g.By
		}
		until := f.LastUntil(g.Account)
		if until == "" || until >= g.By {
			continue
		}
		w := Warning{Kind: WarnAfterTransfers, Goal: g, Until: until, AtBy: f.PlanBalance(g.Account, g.By)}
		if w.AtBy >= g.Target {
			continue
		}
		// Те же переводы, продлённые до срока: какие даты нужны.
		ext := f
		ext.Transfers = nil
		for _, t := range f.Transfers {
			if t.To == g.Account {
				t.Until = g.By
				ext.Transfers = append(ext.Transfers, t)
			}
		}
		w.Missing = ext.Occurrences(g.Account, until, g.By)
		out = append(out, w)
	}
	if f.ByRole(RoleTrip) == "" && cushionBy != "" {
		for _, g := range f.Goals {
			if g.Kind == GoalDate && g.On < cushionBy {
				out = append(out, Warning{Kind: WarnTrip, Goal: g})
			}
		}
	}
	if gap := f.MonthlyTransfers("", f.Baseline.Date) + f.Budget.LivePerMonth - f.MonthlyIncome(); gap > 0 {
		out = append(out, Warning{Kind: WarnOverspend, Gap: gap})
	}
	return out
}
