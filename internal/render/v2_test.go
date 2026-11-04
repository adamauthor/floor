package render

import (
	"strings"
	"testing"

	"floor/internal/habits"
	"floor/internal/money"
)

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

func plainText(s string) string { return strings.ReplaceAll(StripTags(s), " ", " ") }

func TestMoneyTexts(t *testing.T) {
	f := moneyPlan()
	base := f.BaselineSnapshot()
	s := money.Merge("2026-12-20", map[string]int64{"card": 100_000, "emergency": 300_000, money.DebtKey: 700_000}, &base, f)
	confirm := plainText(MoneyConfirm(s, f, base.Date))
	for _, want := range []string{"Сверка на 20 декабря:", "Карта 100 000", "На долги 100 000 (с прошлой сверки, 46 дн.)", "Подушка 300 000", "Долг 700 000"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("нет %q в:\n%s", want, confirm)
		}
	}
	s.Balances["debt_fund"], s.Balances["live"] = 550_000, 20_000
	r := f.Calc(s, &base)
	text := plainText(Money(r, f, "Узкое место: подушка"))
	for _, want := range []string{
		"💰 Деньги — 20 декабря",
		"Долги: осталось закрыть 150 000 ₸ · план 100 000 ⚠️ +50 000",
		"→ при переводах по плану закрыты 5 января, к 5 февраля запас ~450 000",
		"Подушка: 300 000 из 1 000 000 · план 350 000 🔴 −50 000",
		"На жизнь до 5 января: 120 000 ₸ → 7 500 в день (бюджет 10 000) ⚠️",
		"Подушка — 1,0 месяца жизни",
		"Чистый капитал +270 000 ₸",
		"Путешествие — через 65 дней",
		"Узкое место: подушка",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("нет %q в:\n%s", want, text)
		}
	}
	if got := plainText(MoneyFallback(r)); got != "Подушка: не хватит 450 000 к 5 марта" {
		t.Errorf("fallback: %q", got)
	}
	if got := plainText(MoneyLine(r, "2026-12-23")); got != "💰 Долги ⚠️ · Подушка 🔴 · Жизнь ⚠️ (сверка 3 дн. назад)" {
		t.Errorf("строка недели: %q", got)
	}
	ctxBlock := MoneyContext(r, f)
	for _, want := range []string{"Деньги (сверка 2026-12-20, план от 2026-11-04):", "чистый долг 150000, план 100000, статус lagging, прогноз закрыт 2027-01-05",
		"подушка 300000/1000000, план 350000, статус off_track, прогноз к 2027-03-05 550000 (−450000), переводы до 2027-02-05", "на жизнь 120000 до 2027-01-05 (16 дн), 7500/день"} {
		if !strings.Contains(ctxBlock, want) {
			t.Errorf("нет %q в:\n%s", want, ctxBlock)
		}
	}

	// Не успевает к сроку по долгам и закрытые долги.
	late := money.Merge("2027-01-20", map[string]int64{"debt_fund": 100_000, money.DebtKey: 1_000_000, "card": 300_000, "live": 0, "emergency": 900_000}, nil, f)
	rl := f.Calc(late, &s)
	if got := plainText(Money(rl, f, "")); !strings.Contains(got, "→ к 5 февраля не хватит 700 000") || !strings.Contains(got, "к прошлой сверке") {
		t.Errorf("не успевает:\n%s", got)
	}
	if got := MoneyFallback(rl); !strings.HasPrefix(got, "Долги: не хватит") {
		t.Errorf("fallback долгов: %q", got)
	}
	closed := money.Merge("2027-01-20", map[string]int64{"debt_fund": 900_000, money.DebtKey: 800_000}, &s, f)
	rc := f.Calc(closed, nil)
	if got := plainText(Money(rc, f, "")); !strings.Contains(got, "Долги: закрыты ✅ · на На долги остаётся 100 000 ₸") {
		t.Errorf("закрыты:\n%s", got)
	}
	if got := plainText(DebtClosed(rc, f, 400_000)); !strings.Contains(got, "Освободилось 400 000 ₸ в месяц") {
		t.Errorf("событие: %q", got)
	}
	// Отстаёт, но успевает — и всё по плану.
	f2 := moneyPlan()
	f2.Goals[1].Target = 400_000
	lag := money.Merge("2026-12-20", map[string]int64{"card": 500_000, "live": 0, "debt_fund": 900_000, "emergency": 320_000, money.DebtKey: 1_050_000}, nil, f2)
	if got := plainText(MoneyFallback(f2.Calc(lag, nil))); !strings.HasPrefix(got, "Долги: отстают от плана на 50 000") {
		t.Errorf("отстают: %q", got)
	}
	lagB := money.Merge("2026-12-20", map[string]int64{"card": 500_000, "live": 0, "debt_fund": 900_000, "emergency": 300_000, money.DebtKey: 1_000_000}, nil, f2)
	if got := MoneyFallback(f2.Calc(lagB, nil)); !strings.HasPrefix(got, "Подушка: отстаёт от плана") {
		t.Errorf("подушка отстаёт: %q", got)
	}
	ok := money.Merge("2026-12-20", map[string]int64{"card": 500_000, "live": 0, "debt_fund": 900_000, "emergency": 400_000, money.DebtKey: 1_000_000}, nil, f2)
	if got := MoneyFallback(f2.Calc(ok, nil)); got != "" {
		t.Errorf("всё по плану: %q", got)
	}
	poor := money.Merge("2026-12-20", map[string]int64{"card": 10_000, "live": 0, "debt_fund": 900_000, "emergency": 400_000, money.DebtKey: 1_000_000}, nil, f2)
	if got := MoneyFallback(f2.Calc(poor, nil)); !strings.HasPrefix(got, "На жизнь:") {
		t.Errorf("мало на жизнь: %q", got)
	}

	ws := plainText(MoneyWarnings(append(f.Checks(), money.Warning{Kind: money.WarnOverspend, Gap: 10_000}), f))
	for _, want := range []string{"Подушка: переводы заканчиваются 5 февраля, к 5 марта без них будет 600 000 из 1 000 000. Нужны переводы 18 февраля и 5 марта или новый срок.",
		"Путешествие 23 февраля: откуда деньги? Подушка в этот момент ещё не собрана.", "больше дохода на 10 000 ₸"} {
		if !strings.Contains(ws, want) {
			t.Errorf("нет %q в:\n%s", want, ws)
		}
	}
	if MoneyWarnings(nil, f) != "" {
		t.Error("без предупреждений — пусто")
	}
	if !strings.Contains(plainText(TransfersEnded(550_000)), "550 000 ₸") || DayMonth("мусор") != "мусор" {
		t.Error("мелочи")
	}
}

func TestWorkoutAndLearningTexts(t *testing.T) {
	h := habits.Habit{ID: "training", Emoji: "🏋️", Title: "Тренировка", Kind: habits.KindWorkout, PerWeek: 3}
	if WorkoutDone(h, 52) != "🏋️ Тренировка засчитана: 52 мин" || WorkoutMorning(h, 1, false) != "🏋️ Тренировки на неделе: 1 из 3" ||
		WorkoutMorning(h, 0, true) != "🏋️ Новая неделя: 0 из 3" || HabitEmoji(habits.Habit{}) != "•" {
		t.Error("строки тренировок")
	}
	for done, want := range map[int]string{3: "3/3", 2: "2/3 (пол)", 1: "1/3"} {
		if got := weekResult(h, done); got != want {
			t.Errorf("%d → %q", done, got)
		}
	}
	days := weekDays()
	rows := habitRows(days, "2026-11-01", []habits.Habit{h}, nil, map[string]int{"2026-10-26": 40, "2026-10-29": 30})
	if rows[0] != "Тренир. ●   ·   ·   ●   ·   ·   ·  2/3 (пол)" {
		t.Errorf("строка недели: %q", rows[0])
	}
	f := habits.File{Habits: []habits.Habit{h, {ID: "eng", Title: "Английский", Queued: true}, {ID: "new", Title: "Свет", Since: "2026-11-09", Anchor: "a", Action: "b", Floor: "c"}}}
	f.Habits[0].Since, f.Habits[0].Anchor, f.Habits[0].Action, f.Habits[0].Floor = "2026-10-01", "a", "b", "c"
	card := HabitCard(f, "2026-11-04", "10:00", map[string]int{"training": 2})
	for _, want := range []string{"🏋️ <b>Тренировка</b> · 2 из 3 на этой неделе · неделя 5 из ~10", "<b>Свет</b> · с 9 ноября", "Дальше: Английский"} {
		if !strings.Contains(card, want) {
			t.Errorf("нет %q в карточке:\n%s", want, card)
		}
	}
	if got := LearningBlock(1, 13, "Долги", "Таблица", true); got != "📚 Неделя 2 из 13 · Долги (повтор)\nДействие: Таблица" {
		t.Errorf("блок: %q", got)
	}
	if !strings.HasPrefix(LearningQuestion("Долги", "Таблица"), "📚 Долги") || SocialLine(true) != "👥 Люди: были" || SocialLine(false) != "👥 Люди: не было" {
		t.Error("вопросы")
	}
	if got := QueueOffer(h, 11, 14); got != "Пол держится 11 из 14 дней. Запускаем «🏋️ Тренировка» с понедельника?" {
		t.Errorf("очередь: %q", got)
	}
	m := Morning(MorningData{Date: "2026-11-09", Step: "Шаг", Extra: []string{"📚 блок"}}, rules)
	if !strings.HasSuffix(m, "Шаг\n\n📚 блок") {
		t.Errorf("утро: %q", m)
	}
	ctx := ContextTable(days[:1], nil, nil, map[string]int{"2026-10-26": 40})
	if !strings.Contains(ctx, "тренировка 40 мин") {
		t.Errorf("контекст: %s", ctx)
	}
}

func TestReviewTexts(t *testing.T) {
	hs := habits.Defaults("2026-10-01").Habits
	if !strings.Contains(SocialQuestion(4), "Настроение: 4 из 5.") || AnchorQuestion() == "" {
		t.Error("вопросы недели")
	}
	if got := AnchorFallback(hs[2], "07:00"); !strings.Contains(got, "если «закрыл ноутбук после работы» не случилось → выйти за дверь") {
		t.Errorf("запасной план: %q", got)
	}
	if got := AnchorReply(hs[2], "07:00", "текст"); !strings.Contains(got, "🚶 <b>Улица</b>") || !strings.Contains(got, "текст") {
		t.Errorf("разбор: %q", got)
	}
	trend := []WeekTrend{{Monday: "2026-11-02", Mood: 3, Steps: 6400, FloorDays: 5}, {Monday: "2026-11-09"}}
	if got := AutoQuestion(trend); !strings.Contains(got, "2.11") || !strings.Contains(got, "6к") || !strings.Contains(got, "5/7") {
		t.Errorf("месяц:\n%s", got)
	}
	answers := map[string]int{"wake": 2, "teeth": 5}
	if got := AutoSummary(hs[:2], answers, nil); !strings.Contains(got, "Через месяц") {
		t.Errorf("итог: %q", got)
	}
	if got := AutoSummary(hs[:2], answers, hs[1:2]); !strings.Contains(got, "Можно выпустить") || !strings.Contains(Released(hs[1]), "на автомате") {
		t.Errorf("выпуск: %q", got)
	}
	if !strings.Contains(PHQResult(3), "специалистом") || !strings.Contains(PHQResult(2), "Спасибо") {
		t.Error("PHQ")
	}
	for _, src := range []string{"google", "http", "file"} {
		if !strings.HasPrefix(DataGap(src), "Три дня нет") {
			t.Errorf("%s: %q", src, DataGap(src))
		}
	}
	share := WeeklyShare(WeeklyData{Monday: "2026-10-26", Today: "2026-11-01", Days: weekDays(), Habits: hs, Wake: "07:00", Summary: "секрет"}, rules)
	if strings.Contains(share, "секрет") || strings.Contains(share, "Якоря") {
		t.Errorf("итог второму человеку:\n%s", share)
	}
	if times(1) != "1 раз" || times(3) != "3 раза" || times(12) != "12 раз" || plural(21, "день", "дня", "дней") != "день" || plural(5, "день", "дня", "дней") != "дней" {
		t.Error("склонения")
	}
}
