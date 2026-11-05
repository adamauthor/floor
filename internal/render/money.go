package render

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"floor/internal/money"
)

// MoneyUnclear — ответ, когда сверку не удалось разобрать.
const MoneyUnclear = "Не понял, напиши как «рбк 142к, подушка 900к»."

// DayMonth — «20 декабря».
func DayMonth(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%d %s", d.Day(), months[d.Month()-1])
}

func signed(n int64) string {
	if n < 0 {
		return "−" + Number(int(-n))
	}
	return "+" + Number(int(n))
}

func round(n, step int64) int64 {
	return int64(math.Round(float64(n)/float64(step))) * step
}

func daysAgo(from, to string) int {
	a, err1 := time.Parse(time.DateOnly, from)
	b, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}

// MoneyConfirm — распознанная сверка до подтверждения.
func MoneyConfirm(s money.Snapshot, f money.File, prevDate string) string {
	var parts []string
	carried := fmt.Sprintf(" (с прошлой сверки, %d дн.)", daysAgo(prevDate, s.Date))
	for _, a := range f.Accounts {
		v, ok := s.Balances[a.ID]
		if !ok {
			continue
		}
		p := html.EscapeString(a.Name()) + " " + Number(int(v))
		if s.Carried[a.ID] {
			p += carried
		}
		parts = append(parts, p)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Сверка на %s:\n%s", DayMonth(s.Date), strings.Join(parts, " · "))
	if s.DebtTotal != nil {
		fmt.Fprintf(&b, "\nДолг %s", Number(int(*s.DebtTotal)))
		if s.Carried[money.DebtKey] {
			b.WriteString(carried)
		}
	}
	return b.String()
}

// Money — ответ на сверку: всё — код, кроме последней строки bottleneck (Claude или пусто).
func Money(r money.Report, f money.File, bottleneck string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "💰 <b>Деньги — %s</b>\n", DayMonth(r.Date))
	if d := r.Debt; d != nil {
		b.WriteString("\n")
		switch {
		case d.Closed:
			fmt.Fprintf(&b, "Долги: закрыты ✅ · на %s остаётся %s ₸\n", accountName(f, money.RoleDebtFund), Number(int(-d.Net)))
		default:
			fmt.Fprintf(&b, "Долги: осталось закрыть %s ₸ · план %s %s", Number(int(d.Net)), Number(int(d.Plan)), d.Status.Mark())
			if d.Status != money.OnTrack {
				b.WriteString(" " + signed(d.Net-d.Plan))
			}
			if d.Margin >= 0 {
				fmt.Fprintf(&b, "\n→ при переводах по плану закрыты %s, к %s запас ~%s\n",
					DayMonth(d.ClosesOn), DayMonth(d.Goal.By), Number(int(round(d.Margin, 10_000))))
			} else {
				fmt.Fprintf(&b, "\n→ к %s не хватит %s\n", DayMonth(d.Goal.By), Number(int(-d.Margin)))
			}
		}
	}
	for _, l := range r.Balances {
		fmt.Fprintf(&b, "\n%s: %s из %s · план %s %s", html.EscapeString(l.Goal.Title),
			Number(int(l.Actual)), Number(int(l.Goal.Target)), Number(int(l.Plan)), l.Status.Mark())
		if l.Status != money.OnTrack {
			b.WriteString(" " + signed(l.Actual-l.Plan))
		}
		fmt.Fprintf(&b, "\n→ к %s будет %s", DayMonth(l.Goal.By), Number(int(l.AtBy)))
		if l.Shortfall > 0 {
			fmt.Fprintf(&b, ", не хватит %s", Number(int(l.Shortfall)))
			if l.Until != "" {
				fmt.Fprintf(&b, ": переводы до %s", DayMonth(l.Until))
			}
		}
		b.WriteString("\n")
	}

	life := r.Life
	mark := "✅"
	if !life.OK {
		mark = "⚠️"
	}
	fmt.Fprintf(&b, "\nНа жизнь до %s: %s ₸ → %s в день (бюджет %s) %s\n", DayMonth(life.Until),
		Number(int(life.Available)), Number(int(round(life.PerDay, 500))), Number(int(round(life.BudgetDay, 500))), mark)
	fmt.Fprintf(&b, "%s — %s месяца жизни\n", accountName(f, money.RoleEmergency), strings.Replace(strconv.FormatFloat(r.Months, 'f', 1, 64), ".", ",", 1))
	fmt.Fprintf(&b, "Чистый капитал %s ₸", signed(r.NetWorth))
	if r.PrevWorth != nil {
		fmt.Fprintf(&b, " (%s к прошлой сверке)", signed(r.NetWorth-*r.PrevWorth))
	}
	for _, c := range r.Countdown {
		if c.Days == 0 {
			fmt.Fprintf(&b, "\n%s — сегодня", html.EscapeString(c.Goal.Title))
		} else {
			fmt.Fprintf(&b, "\n%s — через %d %s", html.EscapeString(c.Goal.Title), c.Days, plural(c.Days, "день", "дня", "дней"))
		}
	}
	if bottleneck != "" {
		b.WriteString("\n\n" + html.EscapeString(bottleneck))
	}
	return b.String()
}

func plural(n int, one, few, many string) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return few
	}
	return many
}

func accountName(f money.File, role string) string {
	if a, ok := f.Account(f.ByRole(role)); ok {
		if role == money.RoleEmergency {
			for _, g := range f.Goals {
				if g.Account == a.ID && g.Title != "" {
					return g.Title
				}
			}
		}
		return a.Name()
	}
	return role
}

// MoneyFallback — последняя строка без Claude: худшая линия или пусто, если всё по плану.
func MoneyFallback(r money.Report) string {
	if d := r.Debt; d != nil && d.Status == money.OffTrack {
		return fmt.Sprintf("Долги: не хватит %s к %s", Number(int(-d.Margin)), DayMonth(d.Goal.By))
	}
	for _, l := range r.Balances {
		if l.Status == money.OffTrack {
			return fmt.Sprintf("%s: не хватит %s к %s", l.Goal.Title, Number(int(l.Shortfall)), DayMonth(l.Goal.By))
		}
	}
	if d := r.Debt; d != nil && d.Status == money.Lagging {
		return fmt.Sprintf("Долги: отстают от плана на %s", Number(int(d.Net-d.Plan)))
	}
	for _, l := range r.Balances {
		if l.Status == money.Lagging {
			return fmt.Sprintf("%s: отстаёт от плана на %s", l.Goal.Title, Number(int(l.Plan-l.Actual)))
		}
	}
	if !r.Life.OK {
		return fmt.Sprintf("На жизнь: %s в день при бюджете %s", Number(int(round(r.Life.PerDay, 500))), Number(int(round(r.Life.BudgetDay, 500))))
	}
	return ""
}

// MoneyLine — строка в воскресной сводке по последней сверке.
func MoneyLine(r money.Report, today string) string {
	var parts []string
	if r.Debt != nil {
		parts = append(parts, "Долги "+r.Debt.Status.Mark())
	}
	for _, l := range r.Balances {
		parts = append(parts, html.EscapeString(l.Goal.Title)+" "+l.Status.Mark())
	}
	life := "✅"
	if !r.Life.OK {
		life = "⚠️"
	}
	parts = append(parts, "Жизнь "+life)
	ago := daysAgo(r.Date, today)
	when := "сверка сегодня"
	if ago > 0 {
		when = fmt.Sprintf("сверка %d дн. назад", ago)
	}
	return "💰 " + strings.Join(parts, " · ") + " (" + when + ")"
}

// MoneyContext — блок «Деньги» для промпта (SPEC_v2, 7.2).
func MoneyContext(r money.Report, f money.File) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Деньги (сверка %s, план от %s):\n", r.Date, f.Baseline.Date)
	if d := r.Debt; d != nil {
		fmt.Fprintf(&b, "чистый долг %d, план %d, статус %s", d.Net, d.Plan, d.Status.Word())
		if d.Margin >= 0 {
			fmt.Fprintf(&b, ", прогноз закрыт %s, запас к %s %d\n", d.ClosesOn, d.Goal.By, d.Margin)
		} else {
			fmt.Fprintf(&b, ", к %s не хватит %d\n", d.Goal.By, -d.Margin)
		}
	}
	for _, l := range r.Balances {
		fmt.Fprintf(&b, "%s %d/%d, план %d, статус %s, прогноз к %s %d", strings.ToLower(l.Goal.Title), l.Actual, l.Goal.Target, l.Plan, l.Status.Word(), l.Goal.By, l.AtBy)
		if l.Shortfall > 0 {
			fmt.Fprintf(&b, " (−%d)", l.Shortfall)
		}
		if l.Until != "" {
			fmt.Fprintf(&b, ", переводы до %s", l.Until)
		}
		b.WriteString("\n")
	}
	ok := "ok"
	if !r.Life.OK {
		ok = "low"
	}
	fmt.Fprintf(&b, "на жизнь %d до %s (%d дн), %d/день, бюджет %d/день, %s\n", r.Life.Available, r.Life.Until, r.Life.Days, r.Life.PerDay, r.Life.BudgetDay, ok)
	for _, c := range r.Countdown {
		fmt.Fprintf(&b, "%s через %d дн\n", c.Goal.Title, c.Days)
	}
	var purposes []string
	for _, a := range f.Accounts {
		if a.Purpose != "" {
			purposes = append(purposes, a.Name()+" — "+a.Purpose)
		}
	}
	if len(purposes) > 0 {
		b.WriteString("счета: " + strings.Join(purposes, "; ") + "\n")
	}
	for _, g := range f.Goals {
		if g.Note != "" {
			fmt.Fprintf(&b, "цель «%s»: %s\n", g.Title, g.Note)
		}
	}
	for _, n := range f.Notes {
		b.WriteString("договорённость: " + n + "\n")
	}
	return b.String()
}

// MoneyWarnings — дыры в плане (2.6), по строке на каждую.
func MoneyWarnings(ws []money.Warning, f money.File) string {
	var lines []string
	for _, w := range ws {
		switch w.Kind {
		case money.WarnAfterTransfers:
			dates := make([]string, len(w.Missing))
			for i, o := range w.Missing {
				dates[i] = DayMonth(o.Date)
			}
			need := "Нужны переводы " + strings.Join(dates, " и ")
			if len(dates) == 1 {
				need = "Нужен перевод " + dates[0]
			}
			lines = append(lines, fmt.Sprintf("%s: переводы заканчиваются %s, к %s без них будет %s из %s. %s или новый срок.",
				html.EscapeString(w.Goal.Title), DayMonth(w.Until), DayMonth(w.Goal.By), Number(int(w.AtBy)), Number(int(w.Goal.Target)), need))
		case money.WarnTrip:
			lines = append(lines, fmt.Sprintf("%s %s: откуда деньги? %s в этот момент ещё не собрана.",
				html.EscapeString(w.Goal.Title), DayMonth(w.Goal.On), accountName(f, money.RoleEmergency)))
		case money.WarnOverspend:
			lines = append(lines, fmt.Sprintf("Переводы и бюджет жизни в месяц больше дохода на %s ₸.", Number(int(w.Gap))))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "💰 В плане денег открытые вопросы:\n\n" + strings.Join(lines, "\n\n")
}

// DebtClosed — событие: чистый долг ≤ 0.
func DebtClosed(r money.Report, f money.File, freed int64) string {
	text := fmt.Sprintf("Долги закрыты 🎉 На %s остаётся %s ₸.", accountName(f, money.RoleDebtFund), Number(int(-r.Debt.Net)))
	if freed > 0 {
		text += fmt.Sprintf("\nОсвободилось %s ₸ в месяц — пора пересобрать схему.", Number(int(freed)))
	}
	return text
}

// TransfersEnded — событие: автопереводы по плану закончились.
func TransfersEnded(flow int64) string {
	return fmt.Sprintf("Автопереводы по плану закончились: свободный поток %s ₸ в месяц не распределён. Новая схема — в money.json.", Number(int(flow)))
}

// MoneyRemind — напоминание о сверке.
const MoneyRemind = "Пришли остатки, когда будет минута."
