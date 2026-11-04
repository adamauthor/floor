package render

import (
	"strings"
	"testing"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
)

var rules = Rules{WakeTolerance: 60, StepsFloor: 7000, StepsGoal: 10000}

func ptr(n int) *int { return &n }

func wake(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestNumber(t *testing.T) {
	tests := map[int]string{0: "0", 999: "999", 1000: "1 000", 7812: "7 812", 10000: "10 000", 1234567: "1 234 567"}
	for in, want := range tests {
		if got := Number(in); got != want {
			t.Errorf("Number(%d) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	tests := map[int]string{0: "0 мин", 45: "45 мин", 60: "1 ч", 400: "6 ч 40 мин"}
	for in, want := range tests {
		if got := Duration(in); got != want {
			t.Errorf("Duration(%d) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestMorning(t *testing.T) {
	const step = "Шаг на сегодня: когда закроешь ноутбук → куртка."
	tests := []struct {
		name string
		in   MorningData
		want string
	}{
		{
			name: "пример из ТЗ",
			in: MorningData{
				Date: "2026-10-31", WakeAt: wake("2026-10-31T07:24:00+05:00"), WakeDeltaMin: ptr(24),
				SleepMinutes: ptr(400), StepsYesterday: ptr(7812), Step: step,
			},
			want: "☀️ Суббота, 31 октября\n\nПодъём 07:24 — в пределах часа от якоря ✅\nСон 6 ч 40 мин\nШаги вчера 7 812 — пол взят ✅ (цель 10 000)\n\n" + step,
		},
		{
			name: "позже якоря и ниже пола, без оценок",
			in: MorningData{
				Date: "2026-10-31", WakeAt: wake("2026-10-31T08:40:00+05:00"), WakeDeltaMin: ptr(100),
				SleepMinutes: ptr(300), StepsYesterday: ptr(4812), Step: step,
			},
			want: "☀️ Суббота, 31 октября\n\nПодъём 08:40 — на 1 ч 40 мин позже якоря\nСон 5 ч\nШаги вчера 4 812 (пол 7 000)\n\n" + step,
		},
		{
			name: "раньше якоря и цель взята",
			in: MorningData{
				Date: "2026-11-01", WakeAt: wake("2026-11-01T05:30:00+05:00"), WakeDeltaMin: ptr(-90),
				StepsYesterday: ptr(10400), Step: step,
			},
			want: "☀️ Воскресенье, 1 ноября\n\nПодъём 05:30 — на 1 ч 30 мин раньше якоря\nШаги вчера 10 400 — цель взята ✅\n\n" + step,
		},
		{
			name: "данных нет",
			in:   MorningData{Date: "2026-10-31", Step: step},
			want: "☀️ Суббота, 31 октября\n\n" + NoDataText + "\n\n" + step,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Morning(tt.in, rules); got != tt.want {
				t.Errorf("\nполучено:\n%s\nожидалось:\n%s", got, tt.want)
			}
		})
	}
}

func TestMorningEscapesHTML(t *testing.T) {
	got := Morning(MorningData{Date: "2026-10-31", Step: "Шаг на сегодня: когда a<b → c&d"}, rules)
	if !strings.Contains(got, "a&lt;b → c&amp;d") {
		t.Errorf("нет экранирования: %s", got)
	}
}

func TestEveningAndReplies(t *testing.T) {
	hs := habits.Defaults("2026-10-31").Habits
	got := Evening(hs, "07:00")
	for _, want := range []string{"🌙 <b>Как прошёл день?</b>", "🦷 Зубы\nпол: зубы · цель: зубы до телефона", "· → пол → цель"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в:\n%s", want, got)
		}
	}
	if MarkButton(hs[1], 0, false) != "🦷 Зубы · " || MarkButton(hs[1], 1, true) != "🦷 Зубы · пол" ||
		MarkButton(hs[1], 2, true) != "🦷 Зубы · цель ✅" || MarkButton(hs[1], 0, true) != "🦷 Зубы · ✕" {
		t.Error("подписи кнопок")
	}
	reply := MarksReply(hs, map[string]int{"wake": 2, "teeth": 1, "outside": 0, "tidy": 0}, map[string]bool{"outside": true})
	for _, want := range []string{"Ок. Завтра просто пол.", "Цель: ⏰ Подъём", "Пол: 🦷 Зубы", "Мимо: 🚶 Улица · 📦 Вещь на место",
		"🚶 Улица — второй день подряд. Завтра только пол: выйти за дверь."} {
		if !strings.Contains(reply, want) {
			t.Errorf("нет %q в:\n%s", want, reply)
		}
	}
	if got := MarksReply(hs, map[string]int{"wake": 1, "teeth": 1, "outside": 2, "tidy": 1}, nil); !strings.HasPrefix(got, "Пол дня взят ✅") {
		t.Errorf("всё на полу: %q", got)
	}
	if got := ReturnStep(hs[2], "07:00"); got != "Шаг на сегодня: когда закрыл ноутбук после работы → выйти за дверь. Вчера мимо, сегодня хватит минимума." {
		t.Errorf("ReturnStep = %q", got)
	}
	tests := []struct {
		status string
		two    bool
		want   string
	}{
		{"yes", false, "Засчитано."},
		{"yes", true, "Засчитано."},
		{"no", false, "Ок. Завтра просто пол."},
		{"no", true, "Ок. Завтра просто пол.\nДва дня подряд — завтра делаем только пол, ничего сверху."},
	}
	for _, tt := range tests {
		if got := FloorReply(tt.status, tt.two); got != tt.want {
			t.Errorf("FloorReply(%s, %v) = %q", tt.status, tt.two, got)
		}
	}
}

func weekDays() []metrics.Day {
	return []metrics.Day{
		{Date: "2026-10-26", WakeDeltaMin: ptr(10), Steps: ptr(4100), FloorStatus: "yes"},
		{Date: "2026-10-27", WakeDeltaMin: ptr(-20), Steps: ptr(5600), FloorStatus: "yes"},
		{Date: "2026-10-28", Steps: ptr(2300), FloorStatus: "no"},
		{Date: "2026-10-29", WakeDeltaMin: ptr(30), Steps: ptr(5400), FloorStatus: "yes"},
		{Date: "2026-10-30", WakeDeltaMin: ptr(90), Steps: ptr(7812), FloorStatus: "yes"},
		{Date: "2026-10-31", FloorStatus: "none"},
		{Date: "2026-11-01", WakeDeltaMin: ptr(0), Steps: ptr(900), FloorStatus: "none"},
	}
}

func TestWeekTable(t *testing.T) {
	hs := habits.Defaults("2026-10-26").Habits[1:3]
	marks := map[string]map[string]int{
		"2026-10-26": {"teeth": 2, "outside": 1}, "2026-10-28": {"teeth": 0, "outside": 0},
	}
	got := weekTable(weekDays(), "2026-11-01", rules, hs, marks)
	want := strings.Join([]string{
		"        Пн  Вт  Ср  Чт  Пт  Сб  Вс",
		"Подъём  ✅  ✅  ·   ✅  ·   ·   ✅",
		"Шаги    4к  6к  2к  5к  8к  ·   …",
		"Пол     ✅  ✅  ❌  ✅  ✅  ·   …",
		"Зубы    ●   ·   ✕   ·   ·   ·   …",
		"Улица   ○   ·   ✕   ·   ·   ·   …",
		"",
		"● цель  ○ пол  ✕ мимо",
	}, "\n")
	if got != want {
		t.Errorf("\nполучено:\n%s\nожидалось:\n%s", got, want)
	}
}

func TestWeekly(t *testing.T) {
	hs := habits.Defaults("2026-10-26").Habits
	got := Weekly(WeeklyData{
		Monday: "2026-10-26", Today: "2026-11-01", Days: weekDays(), Habits: hs, Wake: "07:00",
		Summary: "Получилось: a<b\nПросело: x\nНа следующую неделю: y",
		Returns: 2, SRI: ptr(71),
	}, rules)
	for _, want := range []string{
		"📅 Неделя 26 окт — 1 ноя",
		"<pre>        Пн",
		"Подъём в норме: 4 из 7, разброс 1 ч 50 мин",
		"Шаги: пол взят 1 из 5",
		"Регулярность сна: 71 из 100",
		"Вернулся после пропуска: 2 раза",
		"Якоря на неделю:",
		"когда звонит будильник в 07:00 → сразу встаю, ноги на пол",
		"когда закрыл ноутбук после работы → куртка и 20 минут на улице",
		"Получилось: a&lt;b",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в:\n%s", want, got)
		}
	}
	if strings.Contains(got, "подряд") {
		t.Errorf("стрики в сводке запрещены:\n%s", got)
	}
}

func TestWeekTitleAcrossYear(t *testing.T) {
	if got := weekTitle("2026-12-28"); got != "📅 Неделя 28 дек — 3 янв" {
		t.Errorf("got %q", got)
	}
}

func TestHabitCard(t *testing.T) {
	f := habits.Defaults("2026-10-31")
	f.Habits[3].Auto = true
	got := HabitCard(f, "2026-11-08", "07:00")
	for _, want := range []string{
		"📌 <b>Привычки и якоря</b>",
		"⏰ <b>Подъём</b> · неделя 2 из ~10",
		"когда звонит будильник в 07:00 → сразу встаю, ноги на пол",
		"🦷 <b>Зубы</b>",
		"когда закрыл ноутбук после работы → куртка и 20 минут на улице\nпол: выйти за дверь",
		"На автомате: 📦 Вещь на место",
		"Пропуск не обнуляет. Не пропускай дважды.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в:\n%s", want, got)
		}
	}
}

func TestHabitCardAfterPlateauAndEscaping(t *testing.T) {
	hs := []habits.Habit{{Title: "A<B", Anchor: "x & y", Action: "z", Floor: "f", Since: "2026-01-01"}}
	got := HabitCard(habits.File{Habits: hs}, "2026-10-31", "07:00")
	if !strings.Contains(got, "<b>A&lt;B</b> · неделя 44\n") || !strings.Contains(got, "когда x &amp; y → z") {
		t.Errorf("карточка:\n%s", got)
	}
	if strings.Contains(got, "На автомате") {
		t.Errorf("без выпущенных строки быть не должно:\n%s", got)
	}
	if plain := StripTags(got); strings.Contains(plain, "<b>") || !strings.Contains(plain, "A<B") {
		t.Errorf("StripTags:\n%s", plain)
	}
}

func TestContextTable(t *testing.T) {
	got := ContextTable([]metrics.Day{
		{Date: "2026-10-31", WakeAt: wake("2026-10-31T07:24:00+05:00"), WakeDeltaMin: ptr(24), SleepMinutes: ptr(400), Steps: ptr(7812), FloorStatus: "yes"},
		{Date: "2026-11-01"},
	}, habits.Defaults("2026-10-31").Habits[1:3], map[string]map[string]int{"2026-10-31": {"teeth": 2, "outside": 0}})
	want := "дата | подъём | Δ мин | сон | шаги | пол дня | привычки\n" +
		"2026-10-31 | 07:24 | +24 | 6:40 | 7812 | yes | Зубы: цель, Улица: мимо\n" +
		"2026-11-01 | - | - | - | - | none | -\n"
	if got != want {
		t.Errorf("\n%s\nожидалось\n%s", got, want)
	}
}

func TestHabitsContext(t *testing.T) {
	got := HabitsContext(habits.Defaults("2026-10-31").Active(), "2026-10-31", "07:00")
	if !strings.Contains(got, "Подъём | когда звонит будильник в 07:00 → сразу встаю, ноги на пол | пол: сел на кровати | неделя 1\n") {
		t.Errorf("got:\n%s", got)
	}
}

func TestDateTitleBadInput(t *testing.T) {
	if got := DateTitle("мусор"); got != "мусор" {
		t.Errorf("got %q", got)
	}
	if got := weekTitle("мусор"); got != "📅 Неделя" {
		t.Errorf("got %q", got)
	}
}
