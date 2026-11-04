package render

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"floor/internal/habits"
)

// MoodQuestion — раз в неделю после сводки (поведенческая активация: настроение рядом с делами).
const MoodQuestion = "Как в целом было на этой неделе?\n1 — тяжело, 5 — хорошо."

// AnchorQuestion — второй шаг недельного вопроса.
func AnchorQuestion(mood int) string {
	return fmt.Sprintf("Настроение: %d из 5.\n\nКакой якорь на этой неделе не сработал?", mood)
}

// AnchorFallback — запасной план без Claude: пол привычки в любой момент до вечера.
func AnchorFallback(h habits.Habit, wake string) string {
	return html.EscapeString(fmt.Sprintf("Запасной план: если «%s» не случилось → %s в любой момент до вечера.",
		h.AnchorText(wake), h.Floor))
}

// AnchorReply — итог разбора якоря.
func AnchorReply(h habits.Habit, wake, text string) string {
	return fmt.Sprintf("%s\nсейчас: %s\n\n%s\n\nПоменять — в Floor, слева от поля ввода.",
		habitTitle(h, true), anchorLine(h, wake), text)
}

// WeekTrend — неделя для месячной сводки.
type WeekTrend struct {
	Monday    string
	Mood      int // 0 — не отвечал
	Steps     int // в среднем за день, 0 — нет данных
	FloorDays int
}

// AutoQuestion — раз в месяц: насколько привычки идут сами (Gardner et al. 2012), и
// последние четыре недели рядом. Связь не считается: четырёх точек для неё мало.
func AutoQuestion(trend []WeekTrend) string {
	var b strings.Builder
	b.WriteString("🔁 <b>Раз в месяц</b>\n")
	if len(trend) > 0 {
		b.WriteString("\n<pre>")
		head, mood, steps, floor := "        ", "Настр.  ", "Шаги    ", "Пол     "
		for _, w := range trend {
			d, err := time.Parse(time.DateOnly, w.Monday)
			label := w.Monday
			if err == nil {
				label = fmt.Sprintf("%d.%02d", d.Day(), int(d.Month()))
			}
			head += pad(label, 6)
			mood += pad(dash(w.Mood, strconv.Itoa(w.Mood)), 6)
			steps += pad(dash(w.Steps, strconv.Itoa((w.Steps+500)/1000)+"к"), 6)
			floor += pad(strconv.Itoa(w.FloorDays)+"/7", 6)
		}
		for _, l := range []string{head, mood, steps, floor} {
			b.WriteString(strings.TrimRight(l, " ") + "\n")
		}
		b.WriteString("</pre>")
	}
	b.WriteString("\nНасколько привычка идёт сама?\n1 — каждый раз через силу, 5 — делаю, не задумываясь.")
	return b.String()
}

func pad(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func dash(n int, s string) string {
	if n == 0 {
		return "·"
	}
	return s
}

// AutoSummary — итог месячного вопроса; ready — кого можно выпустить с карточки.
func AutoSummary(active []habits.Habit, answers map[string]int, ready []habits.Habit) string {
	var parts []string
	for _, h := range active {
		parts = append(parts, fmt.Sprintf("%s %d", habitTitle(h, false), answers[h.ID]))
	}
	text := "Автоматизм: " + strings.Join(parts, " · ")
	if len(ready) == 0 {
		return text + "\n\nЧерез месяц спрошу снова."
	}
	return text + "\n\nЭто уже идёт само. Можно выпустить с карточки: бот перестанет о ней спрашивать, а место освободится для следующей привычки."
}

// Released — привычка выпущена на автомат.
func Released(h habits.Habit) string {
	return habitTitle(h, false) + " — на автомате. Бот о ней больше не спрашивает. Вернуть — в Floor, в карточке привычки."
}

// PHQ-2 (Kroenke et al. 2003) — два вопроса о последних двух неделях, порог 3.
// Это скрининг, а не диагноз: при пороге бот одной фразой предлагает специалиста.
const PHQIntro = "Два вопроса раз в месяц — о последних двух неделях. Ответы видишь только ты."

// PHQQuestions — вопросы опросника.
var PHQQuestions = [2]string{
	"Как часто беспокоило: мало интереса или удовольствия от занятий?",
	"Как часто беспокоило: подавленность, тоска или безнадёжность?",
}

// PHQAnswers — варианты ответа, баллы 0–3.
var PHQAnswers = [4]string{"Ни разу", "Несколько дней", "Больше половины дней", "Почти каждый день"}

// PHQResult — ответ после второго вопроса.
func PHQResult(score int) string {
	if score >= 3 {
		return "Похоже, последние две недели было тяжело. Это стоит обсудить со специалистом — психиатром или психотерапевтом. Это не диагноз, а повод проверить: такое лечится."
	}
	return "Спасибо. Через месяц спрошу снова."
}

// DataGap — три дня без данных: что, скорее всего, сломалось.
func DataGap(source string) string {
	switch source {
	case "google":
		return "Три дня нет ни шагов, ни сна. Проверь, что Fitbit синхронизируется с телефоном. Если там всё есть — на Маке в папке floor: make google-auth"
	case "http":
		return "Три дня нет ни шагов, ни сна. Похоже, автоматизация на iPhone перестала отправлять данные: проверь её в «Командах»."
	}
	return "Три дня нет ни шагов, ни сна. Похоже, автоматизация на iPhone перестала выгружать данные: проверь её в «Командах» и файл в iCloud Drive."
}

// WeeklyShare — итог недели для второго человека: таблица и цифры, без якорей и текста Claude.
func WeeklyShare(d WeeklyData, r Rules) string {
	d.Summary = ""
	full := Weekly(d, r)
	if i := strings.Index(full, "\n\nЯкоря на неделю:"); i >= 0 {
		full = full[:i]
	}
	return full
}
