package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/render"
	"floor/internal/webapp"
)

// waitEdit ждёт правку с подстрокой: разбор якоря идёт в фоне.
func (h *harness) waitEdit(t *testing.T, sub string) sent {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if e := h.tg.lastEdit(); strings.Contains(e.text, sub) {
			return e
		}
	}
	t.Fatalf("нет правки с %q, последняя: %q", sub, h.tg.lastEdit().text)
	return sent{}
}

func TestWeeklyReviewMoodThenAnchor(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-11-01 19:00", plus5)
	if err := h.db.SetMark(ctx, "2026-10-27", "outside", 0, h.now); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SetMark(ctx, "2026-10-28", "outside", 1, h.now); err != nil {
		t.Fatal(err)
	}
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	h.tick("2026-11-01 20:00", plus5)

	weekly := h.tg.matching("📅")
	if len(weekly) != 1 || !strings.Contains(weekly[0].text, "Вернулся после пропуска: 1 раз") ||
		!strings.Contains(weekly[0].text, "Улица   ·   ✕   ○") {
		t.Fatalf("неделя: %+v", weekly)
	}
	review := h.tg.matching(render.MoodQuestion)
	if len(review) != 1 || len(review[0].rows[0]) != 5 {
		t.Fatalf("вопрос недели: %+v", review)
	}

	h.app.OnCallback(ctx, review[0].rows[0][3].Data, review[0].id)
	if mood, _ := h.db.Answers(ctx, answerMood, "2026-10-26"); mood[""] != 4 {
		t.Errorf("настроение: %v", mood)
	}
	e := h.tg.lastEdit()
	if !strings.Contains(e.text, "Виделся с друзьями на неделе?") || len(e.rows[0]) != 2 {
		t.Fatalf("второй шаг: %+v", e)
	}
	h.app.OnCallback(ctx, e.rows[0][0].Data, review[0].id)
	if social, _ := h.db.Answers(ctx, answerSocial, "2026-10-26"); social[""] != 1 {
		t.Errorf("люди: %v", social)
	}
	e = h.tg.lastEdit()
	if !strings.Contains(e.text, "Какой якорь на этой неделе не сработал?") || len(e.rows) != 5 {
		t.Fatalf("третий шаг: %+v", e)
	}

	h.llm.reply = "Запасной план: если не закрыл ноутбук до 19:00 → выйти за дверь в 19:00."
	h.app.OnCallback(ctx, e.rows[2][0].Data, review[0].id) // улица
	got := h.waitEdit(t, "Запасной план: если не закрыл ноутбук")
	if !strings.Contains(got.text, "🚶 <b>Улица</b>") || len(got.rows) != 0 {
		t.Errorf("разбор: %+v", got)
	}
	if last := h.llm.prompts[len(h.llm.prompts)-1]; !strings.Contains(last, "не сработал якорь привычки «Улица»") {
		t.Errorf("промпт якоря:\n%s", last)
	}
}

func TestAnchorReviewWithoutClaude(t *testing.T) {
	h := newHarness(t)
	h.llm.err = errFake
	h.now = at("2026-11-03 10:00", plus5)
	h.app.OnCallback(ctx, "w:2026-11-02:a:outside", 7)
	h.waitEdit(t, "Запасной план: если «закрыл ноутбук после работы» не случилось → выйти за дверь")
}

func TestMonthlyAutomatismAndRelease(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-01 10:00", plus5) // привычки с 1 октября: к декабрю им девятая неделя
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	// Прошлый месяц: зубы уже шли сами.
	if err := h.db.SetAnswer(ctx, answerAuto, "2026-11", "teeth", 4, h.now); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-12-06 20:00", plus5) // первое воскресенье декабря

	q := h.tg.matching("🔁")
	if len(q) != 1 || len(q[0].rows) != 4 || len(q[0].rows[0]) != 6 {
		t.Fatalf("месячный вопрос: %+v", q)
	}
	if !strings.Contains(q[0].text, "Настр.") || !strings.Contains(q[0].text, "Пол") {
		t.Errorf("таблица недель:\n%s", q[0].text)
	}
	if phq := h.tg.matching(render.PHQIntro); len(phq) != 1 || len(phq[0].buttons) != 4 {
		t.Fatalf("опросник: %+v", phq)
	}
	// Ответы: зубы 4 (второй месяц подряд), остальные 2.
	for i, n := range []int{2, 4, 2, 2} {
		h.app.OnCallback(ctx, q[0].rows[i][n].Data, q[0].id)
	}
	e := h.tg.lastEdit()
	if !strings.Contains(e.text, "Автоматизм: ⏰ Подъём 2 · 🦷 Зубы 4") || len(e.rows) != 2 ||
		e.rows[0][0].Text != "Выпустить: Зубы" {
		t.Fatalf("итог: %+v", e)
	}
	h.app.OnCallback(ctx, e.rows[0][0].Data, q[0].id)
	if !strings.Contains(h.tg.lastEdit().text, "🦷 Зубы — на автомате") {
		t.Errorf("выпуск: %q", h.tg.lastEdit().text)
	}
	_, f, _ := habits.Load(h.app.Cfg.HabitsFile, "2026-12-06")
	if teeth, _ := f.Find("teeth"); !teeth.Auto {
		t.Error("привычка не выпущена в файле")
	}
	// Вечером о ней уже не спрашивают.
	h.tick("2026-12-06 21:00", plus5)
	ev := h.tg.matching("🌙")
	if len(ev) != 1 || strings.Contains(ev[0].text, "Зубы") {
		t.Errorf("вечер после выпуска: %+v", ev)
	}
	// В тот же месяц второй раз не спрашивают.
	h.tick("2026-12-06 20:30", plus5)
	if len(h.tg.matching("🔁")) != 1 {
		t.Error("месячный вопрос продублирован")
	}
}

func TestYoungHabitNotReleased(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-30 10:00", plus5) // привычкам неделя
	for _, id := range []string{"wake", "teeth", "outside", "tidy"} {
		h.app.OnCallback(ctx, "mo:2026-12:a:"+id+":5", 3)
	}
	if e := h.tg.lastEdit(); len(e.rows) != 0 || !strings.Contains(e.text, "Через месяц спрошу снова") {
		t.Errorf("молодые привычки не выпускаются: %+v", e)
	}
}

func TestPHQ(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want string
	}{
		{"1", "1", "Спасибо"},
		{"2", "1", "стоит обсудить со специалистом"},
	} {
		h := newHarness(t)
		h.now = at("2026-12-06 20:00", plus5)
		h.app.OnCallback(ctx, "mo:2026-12:p1:"+tt.a, 4)
		if !strings.Contains(h.tg.lastEdit().text, render.PHQQuestions[1]) {
			t.Fatalf("второй вопрос: %q", h.tg.lastEdit().text)
		}
		h.app.OnCallback(ctx, "mo:2026-12:p2:"+tt.b, 4)
		if got := h.tg.lastEdit().text; !strings.Contains(got, tt.want) {
			t.Errorf("%s+%s: %q", tt.a, tt.b, got)
		}
	}
}

func TestDataGapAlertOnce(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-11-01 10:00", plus5)
	if err := h.db.UpsertSteps(ctx, "2026-11-01", "+05:00", 3000, h.now); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-11-02 12:00", plus5) // данные были вчера
	h.tick("2026-11-04 12:00", plus5) // два дня без данных — рано
	if len(h.tg.matching("Три дня нет")) != 0 {
		t.Fatal("рано")
	}
	h.tick("2026-11-05 12:00", plus5)
	h.tick("2026-11-05 13:00", plus5)
	h.tick("2026-11-06 12:00", plus5)
	if n := len(h.tg.matching("Три дня нет ни шагов, ни сна")); n != 1 {
		t.Fatalf("предупреждений: %d", n)
	}
	// Данные вернулись и пропали снова — новое предупреждение.
	if err := h.db.UpsertSteps(ctx, "2026-11-06", "+05:00", 2000, h.now); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-11-07 12:00", plus5)
	h.tick("2026-11-10 12:00", plus5)
	if n := len(h.tg.matching("Три дня нет")); n != 2 {
		t.Errorf("после возврата данных: %d", n)
	}
}

func TestNoDataGapOnFreshInstall(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-05 12:00", plus5)
	if len(h.tg.matching("Три дня нет")) != 0 {
		t.Error("на свежей установке данных ещё не было")
	}
}

func TestWatchInviteFlow(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 08:00", plus5)
	on := true
	res := h.app.SaveSettings(ctx, h.submit(t, func(st *webappState) { st.Share = &on }))
	msgs := h.tg.matching("https://t.me/floor_test_bot?start=w")
	if !res.OK || len(msgs) != 1 || !strings.HasPrefix(res.Invite, "https://t.me/floor_test_bot?start=w") {
		t.Fatalf("приглашение: %+v / %v", res, msgs)
	}
	link := res.Invite[strings.Index(res.Invite, "start=")+len("start="):]

	h.app.OnWatch(ctx, 555, "Мама", "wчужой")
	if chat, _ := h.app.watcher(ctx); chat != 0 {
		t.Fatal("чужая ссылка принята")
	}
	h.app.OnWatch(ctx, 555, "Мама", link)
	if chat, name := h.app.watcher(ctx); chat != 555 || name != "Мама" {
		t.Fatalf("получатель: %d %q", chat, name)
	}
	h.app.OnWatch(ctx, 777, "Чужой", link) // ссылка одноразовая
	if chat, _ := h.app.watcher(ctx); chat != 555 {
		t.Fatal("ссылку использовали повторно")
	}
	if len(h.tg.matching("Мама теперь получает итог недели")) != 1 {
		t.Error("владельцу не сообщили")
	}

	h.llm.reply = "Получилось: секрет\nУзкое место: b\nНа следующую неделю: c"
	h.tick("2026-11-01 20:00", plus5)
	h.tg.mu.Lock()
	shared := append([]sent(nil), h.tg.shared...)
	h.tg.mu.Unlock()
	var weekly *sent
	for i := range shared {
		if strings.Contains(shared[i].text, "📅") {
			weekly = &shared[i]
		}
	}
	if weekly == nil || weekly.chat != 555 || strings.Contains(weekly.text, "секрет") || strings.Contains(weekly.text, "Якоря") {
		t.Fatalf("итог второму человеку: %+v", shared)
	}

	off := false
	h.app.SaveSettings(ctx, h.submit(t, func(st *webappState) { st.Share = &off }))
	if chat, _ := h.app.watcher(ctx); chat != 0 {
		t.Error("получатель не отключён")
	}
}

func TestSleepRegularityInWeekly(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-11-01 19:00", plus5)
	for i := range 7 {
		date := metrics.AddDays("2026-10-26", i)
		wake := at(date+" 08:00", plus5)
		if err := h.db.SetSleep(ctx, date, "+05:00", 480, wake, 60, h.now); err != nil {
			t.Fatal(err)
		}
	}
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	h.tick("2026-11-01 20:00", plus5)
	if w := h.tg.matching("📅"); len(w) != 1 || !strings.Contains(w[0].text, "Регулярность сна: 100 из 100") {
		t.Fatalf("неделя: %+v", w)
	}
}

type webappState = webapp.State

var errFake = errors.New("claude недоступен")
