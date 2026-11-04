package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"floor/internal/habits"
	"floor/internal/money"
	"floor/internal/render"
)

// moneyPlan — вымышленный план в форме ТЗ 2.0.
func moneyPlan() money.File {
	f := money.Defaults("2026-11-04")
	f.Baseline = money.Baseline{Date: "2026-11-04", DebtTotal: 1_000_000, Balances: map[string]int64{"debt_fund": 100_000, "emergency": 50_000}}
	f.Income = []money.Income{{Day: 5, Amount: 500_000}, {Day: 18, Amount: 400_000}}
	f.Transfers = []money.Transfer{
		{Day: 5, Amount: 200_000, To: "debt_fund", Until: "2027-02-05"},
		{Day: 18, Amount: 200_000, To: "debt_fund", Until: "2027-02-05"},
		{Day: 5, Amount: 100_000, To: "emergency", Until: "2027-02-05"},
		{Day: 18, Amount: 50_000, To: "emergency", Until: "2027-02-05"},
	}
	f.Budget = money.Budget{LivePerMonth: 300_000}
	f.Goals = []money.Goal{
		{ID: "debt_free", Kind: money.GoalDebtZero, By: "2027-02-05", Title: "Закрыть долги"},
		{ID: "cushion", Kind: money.GoalBalance, Account: "emergency", Target: 1_000_000, By: "2027-03-05", Title: "Подушка"},
		{ID: "trip", Kind: money.GoalDate, On: "2027-02-23", Title: "Путешествие"},
	}
	return f
}

func (h *harness) withMoney(t *testing.T) {
	t.Helper()
	raw, err := money.Encode(moneyPlan())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, "money.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h.app.Cfg.MoneyFile = path
	h.app.Cfg.MoneyRemindDays = 14
}

func (h *harness) waitText(t *testing.T, sub string) sent {
	t.Helper()
	for i := 0; i < 300; i++ {
		if got := h.tg.matching(sub); len(got) > 0 {
			return got[len(got)-1]
		}
		waitShort()
	}
	t.Fatalf("нет сообщения с %q", sub)
	return sent{}
}

func TestMoneyReconcileFlow(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-12-20 12:00", plus5)
	if w := h.tg.matching("В плане денег открытые вопросы"); len(w) != 1 || !strings.Contains(w[0].text, "Нужны переводы 18 февраля и 5 марта") {
		t.Fatalf("проверки плана: %+v", w)
	}
	h.tick("2026-12-20 12:01", plus5)
	if len(h.tg.matching("открытые вопросы")) != 1 {
		t.Error("проверки плана повторились без правки файла")
	}

	calls := len(h.llm.prompts)
	h.app.OnText(ctx, "карта 100к, лайв 20 000, дф 550к, подушка 300к, долг 700 000, карта 4400 4301 1234 5678")
	confirm := h.tg.matching("Сверка на 20 декабря")
	if len(confirm) != 1 || len(confirm[0].rows[0]) != 2 || len(h.llm.prompts) != calls {
		t.Fatalf("подтверждение: %+v, вызовов Claude %d", confirm, len(h.llm.prompts)-calls)
	}
	msgs, _ := h.db.RecentMessages(ctx, 5)
	for _, m := range msgs {
		if strings.Contains(m.Text, "100к") || strings.Contains(m.Text, "4400") {
			t.Errorf("суммы или номер карты в истории: %q", m.Text)
		}
	}
	if last, _ := h.db.LastSnapshot(ctx); last == nil || !last.Baseline {
		t.Fatal("до подтверждения сохранилась не только точка отсчёта")
	}

	h.llm.reply = "Узкое место: подушка, переводы кончаются раньше срока."
	h.app.OnCallback(ctx, confirm[0].rows[0][0].Data, confirm[0].id)
	report := h.waitText(t, "💰 <b>Деньги — 20 декабря</b>")
	for _, want := range []string{
		"Долги: осталось закрыть 150 000 ₸ · план 100 000 ⚠️ +50 000",
		"Подушка: 300 000 из 1 000 000 · план 350 000 🔴 −50 000",
		"→ к 5 марта будет 550 000, не хватит 450 000: переводы до 5 февраля",
		"На жизнь до 5 января: 120 000 ₸ → 7 500 в день (бюджет 10 000) ⚠️",
		"Узкое место: подушка",
	} {
		if !strings.Contains(spaces(report.text), want) {
			t.Errorf("нет %q в:\n%s", want, report.text)
		}
	}
	if last, _ := h.db.LastSnapshot(ctx); last == nil || last.Baseline || last.Balances["emergency"] != 300_000 {
		t.Errorf("сверка: %+v", last)
	}
	// Повторное нажатие: кнопка уже израсходована.
	if ans := h.app.OnCallback(ctx, confirm[0].rows[0][0].Data, confirm[0].id); ans != render.ExpiredReply {
		t.Errorf("повтор: %q", ans)
	}

	// Воскресенье: строка денег.
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	h.tick("2026-12-20 20:00", plus5)
	if w := h.tg.matching("📅"); len(w) != 1 || !strings.Contains(w[0].text, "💰 Долги ⚠️ · Подушка 🔴 · Жизнь ⚠️ (сверка сегодня)") {
		t.Errorf("неделя: %+v", w)
	}
}

func TestMoneyCancelExpireAndNotReconcile(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-12-20 12:00", plus5)

	h.llm.reply = "Ответ Claude."
	h.app.OnText(ctx, "купил кофе за 1500")
	if len(h.tg.matching("Сверка на")) != 0 || len(h.tg.matching("Ответ Claude.")) != 1 {
		t.Fatal("обычный текст принят за сверку")
	}

	h.app.OnText(ctx, "карта 100к, подушка 300к")
	c := h.tg.matching("Сверка на")[0]
	if !strings.Contains(c.text, "(с прошлой сверки, 46 дн.)") {
		t.Errorf("счета из прошлой сверки не помечены: %s", c.text)
	}
	h.app.OnCallback(ctx, c.rows[0][1].Data, c.id)
	if !strings.Contains(h.tg.lastEdit().text, "отменена") {
		t.Errorf("отмена: %+v", h.tg.lastEdit())
	}
	h.app.OnText(ctx, "карта 100к, подушка 300к")
	c = h.tg.matching("Сверка на")[1]
	h.now = h.now.Add(25 * time.Hour)
	if ans := h.app.OnCallback(ctx, c.rows[0][0].Data, c.id); ans != render.ExpiredReply {
		t.Errorf("через 25 часов: %q", ans)
	}
	if last, _ := h.db.LastSnapshot(ctx); !last.Baseline {
		t.Error("отмена или истечение что-то сохранили")
	}
}

func TestMoneyParsedByClaude(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-12-20 12:00", plus5)
	h.llm.reply = "```json\n{\"card\": 50000, \"live\": 50000, \"debt_fund\": null, \"emergency\": null, \"debt_total\": 600000}\n```"
	h.app.OnText(ctx, "остатки: карта и лайв по 50к, долги 600к")
	c := h.tg.matching("Сверка на")
	if len(c) != 1 || !strings.Contains(spaces(c[0].text), "Карта 50 000 · Жизнь 50 000") || !strings.Contains(spaces(c[0].text), "Долг 600 000") {
		t.Fatalf("разбор Claude: %+v", c)
	}
	if last := h.llm.prompts[len(h.llm.prompts)-1]; !strings.Contains(last, "debt_total") {
		t.Errorf("промпт: %s", last)
	}
	h.llm.reply = "не знаю"
	h.app.OnText(ctx, "💰 подушка как обычно")
	if len(h.tg.matching(render.MoneyUnclear)) != 1 {
		t.Error("нет просьбы о формате")
	}
}

func TestMoneyEvents(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-11-10 12:00", plus5)
	// Напоминание: 14 дней с точки отсчёта (4 ноября) → ближайший перевод 18 ноября → 19-го в 12:00.
	h.tick("2026-11-19 11:00", plus5)
	if len(h.tg.matching(render.MoneyRemind)) != 0 {
		t.Fatal("рано: до 12:00")
	}
	h.tick("2026-11-19 12:00", plus5)
	h.tick("2026-11-20 12:00", plus5)
	if n := len(h.tg.matching(render.MoneyRemind)); n != 1 {
		t.Errorf("напоминаний: %d", n)
	}

	// Долг закрыт — одно сообщение.
	for i := 0; i < 2; i++ {
		h.app.OnText(ctx, "дф 900к, долг 800к")
		c := h.tg.matching("Сверка на")
		h.app.OnCallback(ctx, c[len(c)-1].rows[0][0].Data, c[len(c)-1].id)
		h.waitText(t, "Долги: закрыты ✅")
	}
	if n := len(h.tg.matching("Долги закрыты 🎉")); n != 1 {
		t.Errorf("«Долги закрыты» %d раз", n)
	}
	// Переводы закончились — одно сообщение.
	h.tick("2027-02-06 12:00", plus5)
	h.tick("2027-02-06 12:30", plus5)
	if got := h.tg.matching("Автопереводы по плану закончились"); len(got) != 1 || !strings.Contains(spaces(got[0].text), "550 000") {
		t.Errorf("конец переводов: %+v", got)
	}
	// Цель-дата — строкой утром.
	h.tick("2027-02-23 10:00", plus5)
	if m := h.tg.matching("☀️"); !strings.Contains(m[len(m)-1].text, "🎯 Сегодня: Путешествие") {
		t.Errorf("утро цели: %s", m[len(m)-1].text)
	}
}

func TestBadMoneyFileKeepsBot(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-12-20 12:00", plus5)
	if err := os.WriteFile(h.app.Cfg.MoneyFile, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-12-20 12:01", plus5)
	h.tick("2026-12-20 12:02", plus5)
	if n := len(h.tg.matching("ошибка, считаю по прошлой версии")); n != 1 {
		t.Errorf("сообщений об ошибке: %d", n)
	}
	h.app.OnText(ctx, "карта 100к, подушка 300к")
	if len(h.tg.matching("Сверка на")) != 1 {
		t.Error("с битым файлом сверка не идёт по прошлой версии")
	}
}

func workoutHabit(f *habits.File) {
	f.Habits = append(f.Habits, habits.Habit{ID: "training", Emoji: "🏋️", Title: "Тренировка", Kind: habits.KindWorkout, PerWeek: 3,
		Anchor: "закрыл ноутбук в пн, ср, пт", Action: "тренировка 45+ минут", Floor: "20 минут любой тренировки", Since: "2026-10-26"})
}

func TestWorkouts(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.WorkoutMinMinutes = 20
	h.tick("2026-10-31 08:00", plus5)
	writeHabits(t, h, workoutHabit)
	h.apply(t, []byte(`{"schema":1,"generated_at":"2026-10-31T19:00:00+05:00","tz_offset":"+05:00","steps":[],"sleep":[],
		"workouts":[{"start":"2026-10-31T18:00:00+05:00","end":"2026-10-31T18:52:00+05:00","type":"бег"},
		{"start":"2026-10-30T18:00:00+05:00","end":"2026-10-30T18:10:00+05:00","type":"ходьба"}]}`))

	h.tick("2026-10-31 21:00", plus5)
	ev := h.tg.matching("🌙")
	if len(ev) != 1 || !strings.Contains(ev[0].text, "🏋️ Тренировка засчитана: 52 мин") || len(ev[0].rows) != 5 {
		t.Fatalf("вечер с тренировкой: %+v", ev)
	}
	// Пол дня — только ежедневные привычки.
	h.now = at("2026-10-31 21:05", plus5)
	h.app.OnCallback(ctx, ev[0].rows[4][0].Data, ev[0].id)
	if d, _ := h.db.GetDay(ctx, "2026-10-31"); d.FloorStatus != "yes" {
		t.Errorf("пол дня: %s", d.FloorStatus)
	}

	// Карточка: одна тренировка на неделе (короткая ходьба не считается).
	h.tick("2026-10-31 21:10", plus5)
	h.tg.mu.Lock()
	card := ""
	for _, e := range h.tg.edits {
		if strings.Contains(e.text, "📌") {
			card = e.text
		}
	}
	h.tg.mu.Unlock()
	if !strings.Contains(card, "🏋️ <b>Тренировка</b> · 1 из 3 на этой неделе") {
		t.Errorf("карточка:\n%s", card)
	}

	// Без тренировки — кнопка «Была без часов», ровно одна запись за день.
	h.tick("2026-11-01 21:00", plus5)
	ev = h.tg.matching("🌙")
	gym := ev[len(ev)-1].rows[4][0]
	if !strings.Contains(gym.Text, "Была без часов") {
		t.Fatalf("кнопка: %+v", ev[len(ev)-1].rows)
	}
	h.now = at("2026-11-01 21:05", plus5)
	h.app.OnCallback(ctx, gym.Data, ev[len(ev)-1].id)
	h.app.OnCallback(ctx, gym.Data, ev[len(ev)-1].id)
	if ws := h.app.workouts(ctx, "2026-11-01", "2026-11-01"); len(ws["2026-11-01"]) != 1 {
		t.Errorf("ручных: %+v", ws)
	}
	if e := h.tg.lastEdit(); !strings.Contains(spaces(e.text), "Тренировка засчитана: 20 мин") {
		t.Errorf("после кнопки: %q", e.text)
	}

	// Утро понедельника: новая неделя. Неделя закрылась 2/3 — пол.
	h.tick("2026-11-02 10:00", plus5)
	if m := h.tg.matching("☀️"); !strings.Contains(m[len(m)-1].text, "🏋️ Новая неделя: 0 из 3") {
		t.Errorf("утро: %s", m[len(m)-1].text)
	}
}

func TestWorkoutTwoWeeksBelowFloor(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.WorkoutMinMinutes = 20
	h.tick("2026-10-20 08:00", plus5)
	writeHabits(t, h, func(f *habits.File) {
		workoutHabit(f)
		f.Habits[len(f.Habits)-1].Since = "2026-10-19"
	})
	h.tick("2026-11-02 10:00", plus5)
	m := h.tg.matching("☀️")
	if !strings.Contains(m[len(m)-1].text, "Шаг на сегодня: когда закрыл ноутбук в пн, ср, пт → 20 минут любой тренировки") {
		t.Errorf("утро после двух недель ниже пола: %s", m[len(m)-1].text)
	}
}

func TestQueueOffer(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-01 08:00", plus5)
	writeHabits(t, h, func(f *habits.File) {
		workoutHabit(f)
		f.Habits[len(f.Habits)-1].Queued = true
	})
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	// Пол взят 9 из 14 — рано.
	for i := 0; i < 9; i++ {
		if err := h.db.SetFloor(ctx, addDays("2026-11-01", -i), "+05:00", "yes", h.now); err != nil {
			t.Fatal(err)
		}
	}
	h.tick("2026-11-01 20:00", plus5)
	if len(h.tg.matching("Запускаем")) != 0 {
		t.Fatal("предложение при 9 из 14")
	}
	if err := h.db.SetFloor(ctx, "2026-11-08", "+05:00", "yes", h.now); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 6; i++ {
		_ = h.db.SetFloor(ctx, addDays("2026-11-08", -i), "+05:00", "yes", h.now)
	}
	h.tick("2026-11-08 20:00", plus5)
	offer := h.tg.matching("Запускаем «🏋️ Тренировка» с понедельника?")
	if len(offer) != 1 {
		t.Fatalf("предложение: %+v", h.tg.matching("Пол держится"))
	}
	h.app.OnCallback(ctx, offer[0].rows[0][0].Data, offer[0].id)
	_, f, _ := habits.Load(h.app.Cfg.HabitsFile, "2026-11-08")
	if tr, _ := f.Find("training"); tr.Queued || tr.Since != "2026-11-09" {
		t.Errorf("запуск: %+v", tr)
	}
	// Через неделю новое предложение не приходит: не чаще раза в две недели.
	writeHabits(t, h, func(f *habits.File) {
		f.Habits = append(f.Habits, habits.Habit{ID: "eng", Title: "Английский", Anchor: "после обеда", Action: "30 минут", Floor: "5 минут", Since: "2026-11-01", Queued: true})
	})
	for i := 0; i < 7; i++ {
		_ = h.db.SetFloor(ctx, addDays("2026-11-15", -i), "+05:00", "yes", h.now)
	}
	h.tick("2026-11-15 20:00", plus5)
	if len(h.tg.matching("Запускаем «Английский»")) != 0 {
		t.Error("вторая привычка раньше двух недель")
	}
}

func TestLearning(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.LearningStart = "2026-11-09"
	h.tick("2026-11-09 10:00", plus5)
	m := h.tg.matching("☀️")
	if len(m) != 1 || !strings.Contains(m[0].text, "📚 Неделя 1 из 13 · Куда уходят деньги\nДействие:") {
		t.Fatalf("понедельник: %+v", m)
	}
	h.tick("2026-11-10 10:00", plus5)
	if m := h.tg.matching("📚 Неделя"); len(m) != 1 {
		t.Error("блок темы не только в понедельник")
	}

	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	h.tick("2026-11-15 20:00", plus5)
	q := h.tg.matching(render.MoodQuestion)[0]
	h.app.OnCallback(ctx, q.rows[0][2].Data, q.id)
	h.app.OnCallback(ctx, h.tg.lastEdit().rows[0][1].Data, q.id) // друзей не было
	h.app.OnCallback(ctx, "w:2026-11-09:a:*", q.id)
	e := h.tg.lastEdit()
	if !strings.Contains(e.text, "📚 Куда уходят деньги") || e.rows[0][1].Text != "Перенести" {
		t.Fatalf("шаг финграмотности: %+v", e)
	}
	h.app.OnCallback(ctx, e.rows[0][1].Data, q.id)
	if !strings.Contains(h.tg.lastEdit().text, "тема повторится") {
		t.Errorf("перенос: %q", h.tg.lastEdit().text)
	}
	h.tick("2026-11-16 10:00", plus5)
	if m := h.tg.matching("📚 Неделя"); !strings.Contains(m[len(m)-1].text, "Неделя 1 из 13 · Куда уходят деньги (повтор)") {
		t.Errorf("после переноса: %s", m[len(m)-1].text)
	}
	// Люди — строкой в следующей сводке.
	h.tick("2026-11-22 20:00", plus5)
	if w := h.tg.matching("📅"); !strings.Contains(w[len(w)-1].text, "👥 Люди: не было") {
		t.Errorf("сводка: %s", w[len(w)-1].text)
	}
	// Вопросы прошлой недели протухают в понедельник в 12:00.
	h.now = at("2026-11-16 12:00", plus5)
	if ans := h.app.OnCallback(ctx, "w:2026-11-09:mood:3", q.id); ans != render.ExpiredReply {
		t.Errorf("старая кнопка: %q", ans)
	}
}

func addDays(date string, n int) string {
	d, _ := parseDate(date)
	return d.AddDate(0, 0, n).Format("2006-01-02")
}

func parseDate(s string) (t0 time.Time, err error) { return time.Parse("2006-01-02", s) }

func waitShort() { time.Sleep(10 * time.Millisecond) }

// spaces заменяет неразрывные пробелы из render.Number на обычные.
func spaces(s string) string { return strings.ReplaceAll(s, "\u00a0", " ") }
