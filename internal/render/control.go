package render

import (
	"fmt"
	"html"
	"strings"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/money"
)

// ControlContext — для Claude: что можно поменять из чата и под какими id.
func ControlContext(f habits.File, s config.Settings, m *money.File) string {
	var b strings.Builder
	b.WriteString("привычки (id | название | состояние | якорь → цель | пол):\n")
	for _, h := range f.Habits {
		state := "активна"
		switch {
		case h.Paused:
			state = "на паузе"
		case h.Queued:
			state = "в очереди"
		case h.Auto:
			state = "на автомате"
		}
		if h.Weekly() {
			state += fmt.Sprintf(", тренировки %d в неделю", h.PerWeek)
		}
		if h.Since != "" {
			state += ", с " + h.Since
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s → %s | %s\n", h.ID, HabitName(h), state, h.Anchor, h.Action, h.Floor)
	}
	fmt.Fprintf(&b, "настройки: подъём %s ±%d мин, шаги пол %d цель %d, вечерний вопрос %s, итог недели %s\n",
		s.WakeAnchor, s.Tolerance, s.StepsFloor, s.StepsGoal, s.EveningAt, s.WeeklyAt)
	if m != nil {
		var accs []string
		for _, a := range m.Accounts {
			accs = append(accs, a.ID+" ("+a.Name()+")")
		}
		fmt.Fprintf(&b, "деньги: счета %s; бюджет на жизнь %d в месяц\n", strings.Join(accs, ", "), m.Budget.LivePerMonth)
		for _, t := range m.Transfers {
			fmt.Fprintf(&b, "перевод %d-го %d → %s до %s\n", t.Day, t.Amount, t.To, t.Until)
		}
		for _, g := range m.Goals {
			switch g.Kind {
			case money.GoalBalance:
				fmt.Fprintf(&b, "цель %s: %s, %d на %s к %s\n", g.ID, g.Title, g.Target, g.Account, g.By)
			case money.GoalDate:
				fmt.Fprintf(&b, "цель %s: %s, дата %s\n", g.ID, g.Title, g.On)
			default:
				fmt.Fprintf(&b, "цель %s: %s к %s\n", g.ID, g.Title, g.By)
			}
		}
	}
	return b.String()
}

// ActionsConfirm — вопрос перед изменениями из чата.
func ActionsConfirm(reply string, lines []string) string {
	text := "Сделать так?\n" + escapeLines(lines)
	if reply != "" {
		text = html.EscapeString(reply) + "\n\n" + text
	}
	return text
}

// ActionsDone — изменения применены.
func ActionsDone(lines []string) string { return "Готово ✅\n" + escapeLines(lines) }

// ActionsRejected — Claude предложил то, что не прошло проверку.
func ActionsRejected(reply string, err error) string {
	text := "Изменение не применил: " + html.EscapeString(err.Error())
	if reply != "" {
		text = html.EscapeString(reply) + "\n\n" + text
	}
	return text
}

func escapeLines(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = html.EscapeString(l)
	}
	return strings.Join(out, "\n")
}
