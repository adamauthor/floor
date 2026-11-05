package command

import (
	"strings"
	"testing"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/money"
)

func state() State {
	m := money.Defaults("2026-11-04")
	m.Transfers = []money.Transfer{{Day: 5, Amount: 100, To: "emergency", Until: "2027-02-05"}}
	m.Goals = []money.Goal{{ID: "cushion", Kind: money.GoalBalance, Account: "emergency", Target: 1000, By: "2027-03-05", Title: "Подушка"}}
	return State{
		Today:    "2026-11-05",
		Habits:   habits.Defaults("2026-10-26"),
		Settings: config.Settings{WakeAnchor: "07:00", Tolerance: 60, StepsFloor: 7000, StepsGoal: 10000, EveningAt: "21:00", WeeklyAt: "20:00"},
		Money:    &m,
		Notes:    map[int]string{3: "премия 15 ноября"},
		Floor:    func(date string) bool { return date >= "2026-11-04" },
	}
}

func ptr[T any](v T) *T { return &v }

func TestSplit(t *testing.T) {
	reply, acts, err := Split("Предлагаю так.\nДЕЙСТВИЯ: ```json\n[{\"op\":\"remember\",\"text\":\"x\"}]\n```")
	if err != nil || reply != "Предлагаю так." || len(acts) != 1 || acts[0].Text != "x" {
		t.Fatalf("%q %+v %v", reply, acts, err)
	}
	if reply, acts, err := Split("просто ответ"); err != nil || acts != nil || reply != "просто ответ" {
		t.Errorf("без действий: %q %v %v", reply, acts, err)
	}
	if _, _, err := Split(`ДЕЙСТВИЯ: [{"op":"remember","lol":1}]`); err == nil {
		t.Error("неизвестное поле принято")
	}
	if _, _, err := Split("ДЕЙСТВИЯ: [" + strings.Repeat(`{"op":"remember","text":"x"},`, MaxActions) + `{"op":"remember","text":"x"}]`); err == nil {
		t.Error("слишком много действий принято")
	}
	if !NeedsConfirm([]Action{{Op: Remember}, {Op: Mark}}) || NeedsConfirm([]Action{{Op: Remember}, {Op: Forget}}) {
		t.Error("NeedsConfirm")
	}
}

func TestBuildHabits(t *testing.T) {
	s := state()
	p, err := Build(s, []Action{
		{Op: HabitAdd, ID: "read", Emoji: ptr("📖"), Title: ptr("Чтение"), Anchor: ptr("лёг"), Goal: ptr("10 страниц"), Floor: ptr("открыть книгу"), Queued: ptr(true)},
		{Op: HabitEdit, ID: "outside", Floor: ptr("выйти на балкон")},
		{Op: HabitPause, ID: "tidy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"➕ Новая привычка 📖 Чтение: когда лёг → 10 страниц · пол: открыть книгу · в очереди",
		"✏️ 🚶 Улица — пол: «выйти за дверь» → «выйти на балкон»", "⏸ 📦 Вещь на место — на паузу"}
	if strings.Join(p.Lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("строки:\n%s", strings.Join(p.Lines, "\n"))
	}
	if !p.HabitsChange || len(p.Habits.Habits) != 5 || s.Habits.Habits[2].Floor != "выйти за дверь" {
		t.Errorf("исходное состояние изменилось или нет новой: %+v", p.Habits.Habits)
	}

	// Повторяющийся id получает номер; без id — сгенерированный.
	p, err = Build(s, []Action{{Op: HabitAdd, ID: "wake", Title: ptr("Ещё"), Anchor: ptr("a"), Goal: ptr("b"), Floor: ptr("c"), Queued: ptr(true)},
		{Op: HabitAdd, ID: "Плохой id", Title: ptr("И ещё"), Anchor: ptr("a"), Goal: ptr("b"), Floor: ptr("c"), Queued: ptr(true)}})
	if err != nil || p.Habits.Habits[4].ID != "wake2" || p.Habits.Habits[5].ID != "h6" {
		t.Errorf("id: %v %+v", err, p.Habits.Habits)
	}

	for name, acts := range map[string][]Action{
		"нет привычки":     {{Op: HabitEdit, ID: "nope", Floor: ptr("x")}},
		"пустой пол":       {{Op: HabitAdd, Title: ptr("X"), Anchor: ptr("a"), Goal: ptr("b")}},
		"больше 5":         {{Op: HabitAdd, Title: ptr("X"), Anchor: ptr("a"), Goal: ptr("b"), Floor: ptr("c")}, {Op: HabitAdd, Title: ptr("Y"), Anchor: ptr("a"), Goal: ptr("b"), Floor: ptr("c")}},
		"неизвестное":      {{Op: "rm -rf"}},
		"пусто":            {},
		"плохая дата":      {{Op: HabitAdd, Title: ptr("X"), Anchor: ptr("a"), Goal: ptr("b"), Floor: ptr("c"), Since: "завтра"}},
		"тренировка 0/нед": {{Op: HabitAdd, Title: ptr("X"), Anchor: ptr("a"), Goal: ptr("b"), Floor: ptr("c"), Kind: habits.KindWorkout, Queued: ptr(true)}},
	} {
		if _, err := Build(s, acts); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
}

func TestBuildStates(t *testing.T) {
	s := state()
	s.Habits.Habits[3].Queued = true
	s.Habits.Habits[3].Since = "2026-11-09"
	p, err := Build(s, []Action{{Op: HabitResume, ID: "tidy"}, {Op: HabitQueue, ID: "teeth"}, {Op: HabitDelete, ID: "outside"}, {Op: HabitEdit, ID: "wake", Title: ptr("Подъём")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Habits.Habits) != 3 || p.Habits.Habits[2].Queued || p.Habits.Habits[2].Since != "2026-11-05" || !p.Habits.Habits[1].Queued {
		t.Errorf("состояния: %+v", p.Habits.Habits)
	}
	if len(p.Lines) != 3 {
		t.Errorf("без изменений строки быть не должно: %q", p.Lines)
	}
}

func TestBuildSettingsMarksMoneyNotes(t *testing.T) {
	s := state()
	p, err := Build(s, []Action{
		{Op: Settings, WakeAnchor: ptr("08:30"), StepsFloor: ptr(6000)},
		{Op: Mark, Marks: map[string]string{"outside": "goal", "*": "floor"}},
		{Op: Workout, Date: "2026-11-04"},
		{Op: MoneyUntil, To: "emergency", Until: "2027-03-05"},
		{Op: MoneyGoal, ID: "cushion", Target: ptr(int64(2000)), By: "2027-04-01"},
		{Op: MoneyBudget, LivePerMonth: ptr(int64(300))},
		{Op: Remember, Text: "премия 600к 15 ноября"},
		{Op: Forget, Note: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Settings.WakeAnchor != "08:30" || p.Settings.StepsFloor != 6000 || s.Settings.WakeAnchor != "07:00" {
		t.Errorf("настройки: %+v", p.Settings)
	}
	if m := p.Marks["2026-11-05"]; len(m) != 4 || m["outside"] != 2 || m["wake"] != 1 {
		t.Errorf("отметки: %+v", p.Marks)
	}
	if p.Money.Transfers[0].Until != "2027-03-05" || p.Money.Goals[0].Target != 2000 || p.Money.Budget.LivePerMonth != 300 || s.Money.Transfers[0].Until != "2027-02-05" {
		t.Errorf("деньги: %+v", p.Money)
	}
	if len(p.Workouts) != 1 || p.Remember[0] != "премия 600к 15 ноября" || p.Forget[0] != 3 {
		t.Errorf("остальное: %+v", p)
	}
	for _, want := range []string{"⚙️ Подъём: 07:00 → 08:30", "⚙️ Пол шагов: 7000 → 6000", "✅ 2026-11-05 · 🚶 Улица — цель",
		"💰 Переводы на", "💰 Подушка: сумма 1000 → 2000", "💰 Подушка: срок 2027-03-05 → 2027-04-01", "💰 На жизнь в месяц:",
		"📝 Запомнить: премия", "🗑 Забыть: премия 15 ноября", "🏋️ Тренировка 2026-11-04"} {
		if !strings.Contains(strings.Join(p.Lines, "\n"), want) {
			t.Errorf("нет %q в\n%s", want, strings.Join(p.Lines, "\n"))
		}
	}

	for name, acts := range map[string][]Action{
		"пол шагов больше цели": {{Op: Settings, StepsFloor: ptr(20000)}},
		"плохое время":          {{Op: Settings, EveningAt: ptr("25:00")}},
		"закрытый день":         {{Op: Mark, Date: "2026-11-01", Marks: map[string]string{"*": "floor"}}},
		"будущее":               {{Op: Mark, Date: "2026-11-06", Marks: map[string]string{"*": "floor"}}},
		"плохой уровень":        {{Op: Mark, Marks: map[string]string{"wake": "супер"}}},
		"нет привычки":          {{Op: Mark, Marks: map[string]string{"nope": "floor"}}},
		"пустая отметка":        {{Op: Mark}},
		"будущая тренировка":    {{Op: Workout, Date: "2026-12-01"}},
		"нет переводов":         {{Op: MoneyUntil, To: "live", Until: "2027-03-05"}},
		"плохая дата переводов": {{Op: MoneyUntil, To: "emergency", Until: "март"}},
		"нет цели":              {{Op: MoneyGoal, ID: "nope"}},
		"бюджет без суммы":      {{Op: MoneyBudget}},
		"бюджет 0":              {{Op: MoneyBudget, LivePerMonth: ptr(int64(0))}},
		"пустая заметка":        {{Op: Remember, Text: " "}},
		"нет заметки":           {{Op: Forget, Note: 9}},
	} {
		if _, err := Build(s, acts); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
	s.Money = nil
	if _, err := Build(s, []Action{{Op: MoneyBudget, LivePerMonth: ptr(int64(1))}}); err == nil {
		t.Error("деньги без плана")
	}
}
