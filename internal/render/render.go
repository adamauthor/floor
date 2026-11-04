// Package render формирует тексты сообщений. Чистые функции: цифры в сообщениях
// считает код, а не модель (docs/SPEC.md, разделы 2 и 7).
package render

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
)

// HabitPlateauWeeks — ориентир «из ~N» на карточке: медиана до автоматизма около
// 66 дней (Lally et al., 2010), то есть примерно 10 недель. Разброс большой, поэтому «~».
const HabitPlateauWeeks = 10

// Rules — пороги, нужные для текстов.
type Rules struct {
	WakeTolerance int
	StepsFloor    int
	StepsGoal     int
}

// NoDataText — утро без данных с телефона.
const NoDataText = "Данных пока нет — открой Telegram на телефоне, они подтянутся сами."

// WeeklyFallback — неделя без Claude.
const WeeklyFallback = "Сводка от Claude недоступна, цифры выше."

// FreeTextFallback — ответ на свободный текст, когда Claude недоступен.
const FreeTextFallback = "Claude сейчас недоступен. Цифры и шаг — утром, привычки и якоря — в закреплённом сообщении."

// NonTextReply — ответ на голосовое и всё, что не текст.
const NonTextReply = "Пока только текст."

// ExpiredReply — ответ на нажатие устаревшей кнопки.
const ExpiredReply = "Это сообщение устарело."

var (
	weekdays = [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}
	months   = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	monthsSh = [...]string{"янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}
)

// Number форматирует число с разделением тысяч: 7 812.
func Number(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	head := len(s) % 3
	if head > 0 {
		b.WriteString(s[:head])
	}
	for i := head; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// Duration форматирует минуты: 6 ч 40 мин.
func Duration(minutes int) string {
	h, m := minutes/60, minutes%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d мин", m)
	case m == 0:
		return fmt.Sprintf("%d ч", h)
	}
	return fmt.Sprintf("%d ч %d мин", h, m)
}

func toleranceText(minutes int) string {
	if minutes == 60 {
		return "часа"
	}
	return Duration(minutes)
}

func wakeLine(wake time.Time, delta int, r Rules) string {
	clock := wake.Format("15:04")
	if metrics.InNorm(delta, r.WakeTolerance) {
		return fmt.Sprintf("Подъём %s — в пределах %s от якоря ✅", clock, toleranceText(r.WakeTolerance))
	}
	side, abs := "позже", delta
	if delta < 0 {
		side, abs = "раньше", -delta
	}
	return fmt.Sprintf("Подъём %s — на %s %s якоря", clock, Duration(abs), side)
}

func stepsLine(steps int, r Rules) string {
	switch metrics.Level(steps, r.StepsFloor, r.StepsGoal) {
	case metrics.StepsGoalTaken:
		return fmt.Sprintf("Шаги вчера %s — цель взята ✅", Number(steps))
	case metrics.StepsFloorTaken:
		return fmt.Sprintf("Шаги вчера %s — пол взят ✅ (цель %s)", Number(steps), Number(r.StepsGoal))
	}
	return fmt.Sprintf("Шаги вчера %s (пол %s)", Number(steps), Number(r.StepsFloor))
}

// DateTitle — «Суббота, 31 октября».
func DateTitle(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%s, %d %s", weekdays[d.Weekday()], d.Day(), months[d.Month()-1])
}

// MorningData — вход утреннего сообщения.
type MorningData struct {
	Date           string
	WakeAt         *time.Time
	WakeDeltaMin   *int
	SleepMinutes   *int
	StepsYesterday *int
	Step           string // уже в форме «Шаг на сегодня: …»
}

// Morning — утренняя сводка: три строки кода и шаг дня.
func Morning(d MorningData, r Rules) string {
	var lines []string
	if d.WakeAt != nil && d.WakeDeltaMin != nil {
		lines = append(lines, wakeLine(*d.WakeAt, *d.WakeDeltaMin, r))
	}
	if d.SleepMinutes != nil {
		lines = append(lines, "Сон "+Duration(*d.SleepMinutes))
	}
	if d.StepsYesterday != nil {
		lines = append(lines, stepsLine(*d.StepsYesterday, r))
	}
	body := strings.Join(lines, "\n")
	if len(lines) == 0 {
		body = NoDataText
	}
	return fmt.Sprintf("☀️ %s\n\n%s\n\n%s", DateTitle(d.Date), body, html.EscapeString(d.Step))
}

// FloorReply — чем заменяется вечернее сообщение после нажатия.
func FloorReply(status string, twoInARow bool) string {
	if status == metrics.StatusYes {
		return "Засчитано."
	}
	if twoInARow {
		return "Ок. Завтра просто пол.\nДва дня подряд — завтра делаем только пол, ничего сверху."
	}
	return "Ок. Завтра просто пол."
}

func weekTitle(monday string) string {
	from, err := time.Parse(time.DateOnly, monday)
	if err != nil {
		return "📅 Неделя"
	}
	to := from.AddDate(0, 0, 6)
	return fmt.Sprintf("📅 Неделя %d %s — %d %s",
		from.Day(), monthsSh[from.Month()-1], to.Day(), monthsSh[to.Month()-1])
}

func cell(s string) string {
	width := 4 // ширина колонки в моноширинном шрифте
	if s == "✅" || s == "❌" {
		return s + "  " // эмодзи занимает две позиции
	}
	return s + strings.Repeat(" ", width-len([]rune(s)))
}

func weekTable(days []metrics.Day, today string, r Rules, hs []habits.Habit, marks map[string]map[string]int) string {
	wake := "Подъём  "
	steps := "Шаги    "
	floor := "Пол     "
	for _, d := range days {
		switch {
		case d.WakeDeltaMin != nil && metrics.InNorm(*d.WakeDeltaMin, r.WakeTolerance):
			wake += cell("✅")
		default:
			wake += cell("·")
		}
		switch {
		case d.Date == today:
			steps += cell("…")
		case d.Steps != nil:
			steps += cell(strconv.Itoa((*d.Steps+500)/1000) + "к")
		default:
			steps += cell("·")
		}
		switch {
		case d.FloorStatus == metrics.StatusYes:
			floor += cell("✅")
		case d.FloorStatus == metrics.StatusNo:
			floor += cell("❌")
		case d.Date == today:
			floor += cell("…")
		default:
			floor += cell("·")
		}
	}
	lines := []string{"        Пн  Вт  Ср  Чт  Пт  Сб  Вс", wake, steps, floor}
	lines = append(lines, habitRows(days, today, hs, marks)...)
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// WeeklyData — вход недельной сводки.
type WeeklyData struct {
	Monday  string
	Today   string
	Days    []metrics.Day // семь дней с понедельника
	Habits  []habits.Habit
	Wake    string                    // якорь подъёма, HH:MM
	Summary string                    // три строки от Claude или WeeklyFallback
	Marks   map[string]map[string]int // дата → id привычки → уровень
	Returns int                       // возвращений после пропуска за неделю
	SRI     *int                      // регулярность сна; nil — мало данных
}

// Weekly — недельная сводка: таблица, регулярность, якоря, три строки.
func Weekly(d WeeklyData, r Rules) string {
	s := metrics.Week(d.Days, d.Today, r.WakeTolerance, r.StepsFloor)
	var b strings.Builder
	b.WriteString(weekTitle(d.Monday))
	b.WriteString("\n\n<pre>")
	b.WriteString(weekTable(d.Days, d.Today, r, d.Habits, d.Marks))
	b.WriteString("</pre>\n\n")
	fmt.Fprintf(&b, "Подъём в норме: %d из %d", s.WakeInNorm, len(d.Days))
	if s.WakeKnown > 1 {
		fmt.Fprintf(&b, ", разброс %s", Duration(s.SpreadMin))
	}
	if d.SRI != nil {
		fmt.Fprintf(&b, "\nРегулярность сна: %d из 100", *d.SRI)
	}
	fmt.Fprintf(&b, "\nШаги: пол взят %d из %d", s.StepsFloor, s.StepsKnown)
	if d.Returns > 0 {
		fmt.Fprintf(&b, "\nВернулся после пропуска: %s", times(d.Returns))
	}
	b.WriteString("\n\nЯкоря на неделю:\n")
	for _, h := range d.Habits {
		fmt.Fprintf(&b, "%s\n", anchorLine(h, d.Wake))
	}
	b.WriteString("\n")
	b.WriteString(html.EscapeString(d.Summary))
	return b.String()
}

func anchorLine(h habits.Habit, wake string) string {
	return html.EscapeString(fmt.Sprintf("когда %s → %s", h.AnchorText(wake), h.Action))
}

func weekLabel(week int) string {
	if week <= HabitPlateauWeeks {
		return fmt.Sprintf("неделя %d из ~%d", week, HabitPlateauWeeks)
	}
	return fmt.Sprintf("неделя %d", week)
}

// HabitCard — закреплённое сообщение: что развиваю, что это запускает и какой минимум.
func HabitCard(f habits.File, today, wake string) string {
	var b strings.Builder
	b.WriteString("📌 <b>Привычки и якоря</b>\n")
	for _, h := range f.Active() {
		fmt.Fprintf(&b, "\n%s · %s\n%s\nпол: %s\n", habitTitle(h, true), weekLabel(h.Week(today)),
			anchorLine(h, wake), html.EscapeString(h.Floor))
	}
	if auto := f.Automatic(); len(auto) > 0 {
		names := make([]string, len(auto))
		for i, h := range auto {
			names[i] = habitTitle(h, false)
		}
		fmt.Fprintf(&b, "\nНа автомате: %s\n", strings.Join(names, " · "))
	}
	b.WriteString("\nПол — минимум на плохой день. Пропуск не обнуляет. Не пропускай дважды.")
	return b.String()
}

// habitTitle — «🦷 Зубы», с жирным названием или без.
func habitTitle(h habits.Habit, bold bool) string {
	title := html.EscapeString(h.Title)
	if bold {
		title = "<b>" + title + "</b>"
	}
	if h.Emoji != "" {
		return html.EscapeString(h.Emoji) + " " + title
	}
	return title
}

// times — «1 раз», «3 раза», «5 раз».
func times(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return fmt.Sprintf("%d раз", n)
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return fmt.Sprintf("%d раза", n)
	}
	return fmt.Sprintf("%d раз", n)
}

// StripTags убирает разметку Telegram для вывода в терминал.
func StripTags(s string) string {
	s = strings.NewReplacer("<b>", "", "</b>", "", "<pre>", "", "</pre>", "").Replace(s)
	return html.UnescapeString(s)
}

// ContextTable — компактная таблица дней для промпта:
// дата | подъём | Δ мин | сон | шаги | пол дня | привычки.
func ContextTable(days []metrics.Day, hs []habits.Habit, marks map[string]map[string]int) string {
	var b strings.Builder
	b.WriteString("дата | подъём | Δ мин | сон | шаги | пол дня | привычки\n")
	for _, d := range days {
		wake, delta, sleep, steps := "-", "-", "-", "-"
		if d.WakeAt != nil {
			wake = d.WakeAt.Format("15:04")
		}
		if d.WakeDeltaMin != nil {
			delta = fmt.Sprintf("%+d", *d.WakeDeltaMin)
		}
		if d.SleepMinutes != nil {
			sleep = fmt.Sprintf("%d:%02d", *d.SleepMinutes/60, *d.SleepMinutes%60)
		}
		if d.Steps != nil {
			steps = strconv.Itoa(*d.Steps)
		}
		status := d.FloorStatus
		if status == "" {
			status = metrics.StatusNone
		}
		var hm []string
		for _, h := range hs {
			if level, ok := marks[d.Date][h.ID]; ok {
				hm = append(hm, h.Title+": "+[...]string{"мимо", "пол", "цель"}[min(max(level, 0), 2)])
			}
		}
		marked := "-"
		if len(hm) > 0 {
			marked = strings.Join(hm, ", ")
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s | %s | %s\n", d.Date, wake, delta, sleep, steps, status, marked)
	}
	return b.String()
}

// HabitsContext — привычки и якоря для промпта.
func HabitsContext(active []habits.Habit, today, wake string) string {
	var b strings.Builder
	for _, h := range active {
		fmt.Fprintf(&b, "%s | когда %s → %s | пол: %s | неделя %d\n", h.Title, h.AnchorText(wake), h.Action, h.Floor, h.Week(today))
	}
	return b.String()
}
