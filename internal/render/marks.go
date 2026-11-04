package render

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"floor/internal/habits"
	"floor/internal/metrics"
)

// Отметки привычки за день, как в store: 0 — пропуск, 1 — пол, 2 — цель.
const (
	levelMiss  = 0
	levelFloor = 1
	levelGoal  = 2
)

// Evening — вечерняя отметка: у каждой привычки её пол и цель.
func Evening(active []habits.Habit, wake string) string {
	var b strings.Builder
	b.WriteString("🌙 <b>Как прошёл день?</b>\n")
	for _, h := range active {
		fmt.Fprintf(&b, "\n%s\nпол: %s · цель: %s\n", habitTitle(h, false),
			html.EscapeString(h.Floor), html.EscapeString(h.Action))
	}
	b.WriteString("\nЖми на привычку: · → пол → цель.")
	return b.String()
}

// HabitName — «🦷 Зубы» без разметки, для кнопок.
func HabitName(h habits.Habit) string {
	if h.Emoji != "" {
		return h.Emoji + " " + h.Title
	}
	return h.Title
}

// MarkButton — подпись кнопки привычки с текущей отметкой.
func MarkButton(h habits.Habit, level int, marked bool) string {
	name := HabitName(h)
	switch {
	case !marked:
		return name + " · "
	case level == levelGoal:
		return name + " · цель ✅"
	case level == levelFloor:
		return name + " · пол"
	}
	return name + " · ✕"
}

// MarksReply — чем заменяется вечернее сообщение после «Готово».
// again — привычки, пропущенные второй день подряд.
func MarksReply(active []habits.Habit, marks map[string]int, again map[string]bool) string {
	var goal, floor, miss []string
	var lines []string
	for _, h := range active {
		switch marks[h.ID] {
		case levelGoal:
			goal = append(goal, habitTitle(h, false))
		case levelFloor:
			floor = append(floor, habitTitle(h, false))
		default:
			miss = append(miss, habitTitle(h, false))
			if again[h.ID] {
				lines = append(lines, fmt.Sprintf("%s — второй день подряд. Завтра только пол: %s.",
					habitTitle(h, false), html.EscapeString(h.Floor)))
			}
		}
	}
	var b strings.Builder
	if len(miss) == 0 {
		b.WriteString("Пол дня взят ✅")
	} else {
		b.WriteString("Ок. Завтра просто пол.")
	}
	if len(goal) > 0 {
		b.WriteString("\nЦель: " + strings.Join(goal, " · "))
	}
	if len(floor) > 0 {
		b.WriteString("\nПол: " + strings.Join(floor, " · "))
	}
	if len(miss) > 0 {
		b.WriteString("\nМимо: " + strings.Join(miss, " · "))
	}
	for _, l := range lines {
		b.WriteString("\n\n" + l)
	}
	return b.String()
}

// ReturnStep — шаг дня после пропуска: самая маленькая версия пропущенного с его якорем.
func ReturnStep(h habits.Habit, wake string) string {
	return fmt.Sprintf("Шаг на сегодня: когда %s → %s. Вчера мимо, сегодня хватит минимума.", h.AnchorText(wake), h.Floor)
}

// habitRows — строки недельной таблицы по привычкам: ● цель, ○ пол, ✕ мимо, · нет отметки.
func habitRows(days []metrics.Day, today string, hs []habits.Habit, marks map[string]map[string]int) []string {
	var out []string
	for _, h := range hs {
		label := h.Title
		if utf8.RuneCountInString(label) > 7 {
			label = string([]rune(label)[:6]) + "."
		}
		row := label + strings.Repeat(" ", 8-utf8.RuneCountInString(label))
		for _, d := range days {
			level, ok := marks[d.Date][h.ID]
			switch {
			case !ok && d.Date == today:
				row += cell("…")
			case !ok:
				row += cell("·")
			case level == levelGoal:
				row += cell("●")
			case level == levelFloor:
				row += cell("○")
			default:
				row += cell("✕")
			}
		}
		out = append(out, html.EscapeString(row))
	}
	if len(hs) > 0 {
		out = append(out, "", "● цель  ○ пол  ✕ мимо")
	}
	return out
}
