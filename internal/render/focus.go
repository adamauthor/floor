package render

import (
	"fmt"
	"html"
	"strings"
)

// FocusItem — область, где стоит поднажать.
type FocusItem struct {
	Title    string // «🚶 Улица», «Шаги», «Подъём»
	Done, Of int
	Unit     string // «пол» — дни на полу, «тренировки» — разы
	Note     string // готовая строка вместо «пол N из M», например для денег
}

func (f FocusItem) text() string {
	if f.Note != "" {
		return html.EscapeString(f.Title) + " — " + f.Note
	}
	if f.Unit == "тренировки" {
		return fmt.Sprintf("%s — %d из %d", html.EscapeString(f.Title), f.Done, f.Of)
	}
	return fmt.Sprintf("%s — пол %d из %d", html.EscapeString(f.Title), f.Done, f.Of)
}

// Focus — блок «где поднажать» для сводок. Пусто — всё держится.
func Focus(items []FocusItem) string {
	if len(items) == 0 {
		return "🎯 Всё держится на полу — поднажимать не нужно."
	}
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = "· " + it.text()
	}
	return "🎯 <b>Поднажать</b>\n" + strings.Join(lines, "\n")
}

// FocusMorning — строка утра понедельника: главное отставание прошлой недели.
func FocusMorning(it FocusItem) string {
	return "🎯 На этой неделе поднажать: " + it.text() + " на прошлой"
}

// MonthData — сводка месяца по базе.
type MonthData struct {
	Month     string // YYYY-MM
	Days      int    // дней в периоде с данными
	FloorDays int    // пол дня взят
	Wake      FocusItem
	Steps     FocusItem
	StepsAvg  int
	Habits    []FocusItem
	Training  []FocusItem
	Money     string
	Focus     []FocusItem
}

var monthsNom = [...]string{"", "Январь", "Февраль", "Март", "Апрель", "Май", "Июнь", "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}

// Month — сводка месяца: цифры по каждой области и где поднажать.
func Month(d MonthData) string {
	var b strings.Builder
	var m int
	fmt.Sscanf(d.Month[5:], "%d", &m)
	fmt.Fprintf(&b, "📊 <b>%s %s</b>\n\n", monthsNom[m], d.Month[:4])
	fmt.Fprintf(&b, "Пол дня: %d из %d\n", d.FloorDays, d.Days)
	if d.Wake.Of > 0 {
		fmt.Fprintf(&b, "Подъём в норме: %d из %d\n", d.Wake.Done, d.Wake.Of)
	}
	if d.Steps.Of > 0 {
		fmt.Fprintf(&b, "Шаги: пол %d из %d, в среднем %s\n", d.Steps.Done, d.Steps.Of, Number(d.StepsAvg))
	}
	for _, h := range d.Habits {
		fmt.Fprintf(&b, "%s: пол %d из %d\n", html.EscapeString(h.Title), h.Done, h.Of)
	}
	for _, t := range d.Training {
		fmt.Fprintf(&b, "%s: %d из %d за месяц\n", html.EscapeString(t.Title), t.Done, t.Of)
	}
	if d.Money != "" {
		b.WriteString(d.Money + "\n")
	}
	b.WriteString("\n" + Focus(d.Focus))
	return b.String()
}
