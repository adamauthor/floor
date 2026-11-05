package core

import (
	"os"
	"strings"
	"testing"

	"floor/internal/habits"
	"floor/internal/money"
)

func TestChatChangesHabitAfterYes(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-05 08:00", plus5)

	h.llm.reply = "Предлагаю пол поменьше.\nДЕЙСТВИЯ: [{\"op\":\"habit_edit\",\"id\":\"outside\",\"floor\":\"выйти на балкон\"}]"
	h.app.OnText(ctx, "сделай пол улицы — выйти на балкон")
	if p := h.llm.prompts[len(h.llm.prompts)-1]; !strings.Contains(p, "outside | 🚶 Улица | активна") || !strings.Contains(p, "ДЕЙСТВИЯ:") {
		t.Fatalf("в промпте нет блока изменений:\n%s", p)
	}
	q := h.tg.matching("Сделать так?")
	if len(q) != 1 || !strings.Contains(q[0].text, "Предлагаю пол поменьше.") || !strings.Contains(q[0].text, "пол: «выйти за дверь» → «выйти на балкон»") {
		t.Fatalf("вопрос: %+v", q)
	}
	if strings.Contains(q[0].text, "ДЕЙСТВИЯ") {
		t.Error("служебный блок виден в чате")
	}
	if _, f, _ := habits.Load(h.app.Cfg.HabitsFile, "2026-11-05"); f.Habits[2].Floor != "выйти за дверь" {
		t.Fatal("применено до «Да»")
	}

	h.app.OnCallback(ctx, q[0].rows[0][0].Data, q[0].id)
	if _, f, _ := habits.Load(h.app.Cfg.HabitsFile, "2026-11-05"); f.Habits[2].Floor != "выйти на балкон" {
		t.Fatalf("не применено: %+v", f.Habits[2])
	}
	h.tg.mu.Lock()
	card, done := "", ""
	for _, e := range h.tg.edits {
		if strings.Contains(e.text, "📌") {
			card = e.text
		}
		if e.id == q[0].id {
			done = e.text
		}
	}
	h.tg.mu.Unlock()
	if !strings.HasPrefix(done, "Готово ✅") {
		t.Errorf("итог: %q", done)
	}
	if !strings.Contains(card, "пол: выйти на балкон") {
		t.Errorf("карточка не обновилась:\n%s", card)
	}
	if ans := h.app.OnCallback(ctx, q[0].rows[0][0].Data, q[0].id); ans == "" {
		t.Error("повторное «Да» прошло")
	}
	msgs, _ := h.db.RecentMessages(ctx, 5)
	if last := msgs[len(msgs)-1].Text; !strings.HasPrefix(last, "Применено:") {
		t.Errorf("история: %q", last)
	}
}

func TestChatNoAndRejected(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-05 08:00", plus5)
	h.llm.reply = "Ок.\nДЕЙСТВИЯ: [{\"op\":\"settings\",\"wake_anchor\":\"08:30\"}]"
	h.app.OnText(ctx, "подъём на 8:30")
	q := h.tg.matching("Сделать так?")[0]
	h.app.OnCallback(ctx, q.rows[0][1].Data, q.id)
	if h.app.conf().WakeAnchor.String() != "07:00" || h.tg.lastEdit().text != "Не меняю." {
		t.Error("«Нет» применило изменения")
	}

	h.llm.reply = "Сделаю.\nДЕЙСТВИЯ: [{\"op\":\"habit_edit\",\"id\":\"nope\",\"floor\":\"x\"}]"
	h.app.OnText(ctx, "поменяй пол")
	if r := h.tg.matching("Изменение не применил"); len(r) != 1 || !strings.Contains(r[0].text, "нет привычки") || len(r[0].rows[0]) != 0 {
		t.Errorf("отказ: %+v", r)
	}
	h.llm.reply = "Сделаю.\nДЕЙСТВИЯ: это не json"
	h.app.OnText(ctx, "поменяй пол")
	if len(h.tg.matching("Изменение не применил")) != 2 {
		t.Error("битый блок не отклонён")
	}
}

func TestChatRememberAndContext(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-11-05 08:00", plus5)
	h.llm.reply = "Понял про премию.\nДЕЙСТВИЯ: [{\"op\":\"remember\",\"text\":\"15 ноября возможна премия около 600 000\"}]"
	h.app.OnText(ctx, "15-го может быть премия 600к")
	if m := h.tg.matching("📝 Запомнить: 15 ноября"); len(m) != 1 || len(m[0].rows[0]) != 0 {
		t.Fatalf("заметка: %+v", m)
	}
	notes, _ := h.db.Notes(ctx)
	if len(notes) != 1 {
		t.Fatalf("заметок: %d", len(notes))
	}

	h.llm.reply = "Ответ."
	h.app.OnText(ctx, "что по деньгам?")
	p := h.llm.prompts[len(h.llm.prompts)-1]
	for _, want := range []string{"#1 (2026-11-05): 15 ноября возможна премия около 600 000", "Сверок ещё не было", "Деньги (сверка 2026-11-04",
		"цель cushion: Подушка, 1000000 на emergency к 2027-03-05"} {
		if !strings.Contains(p, want) {
			t.Errorf("нет %q в промпте", want)
		}
	}
}

func TestChatMoneyMarksWorkout(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-11-05 08:00", plus5)
	h.now = at("2026-11-05 22:00", plus5)
	h.llm.reply = `Предлагаю.
ДЕЙСТВИЯ: [{"op":"money_transfers","to":"emergency","until":"2027-03-05"},
{"op":"mark","marks":{"outside":"miss","*":"floor"}},{"op":"workout"}]`
	h.app.OnText(ctx, "продли переводы, отметь день: улицы не было, остальное пол, и была тренировка")
	q := h.tg.matching("Сделать так?")[0]
	h.app.OnCallback(ctx, q.rows[0][0].Data, q.id)

	raw, _ := os.ReadFile(h.app.Cfg.MoneyFile)
	f, err := money.Parse(raw)
	if err != nil || f.LastUntil("emergency") != "2027-03-05" {
		t.Errorf("план денег: %v %s", err, f.LastUntil("emergency"))
	}
	if d, _ := h.db.GetDay(ctx, "2026-11-05"); d.FloorStatus != "no" {
		t.Errorf("пол дня: %q", d.FloorStatus)
	}
	if ws := h.app.workouts(ctx, "2026-11-05", "2026-11-05"); len(ws["2026-11-05"]) != 1 {
		t.Errorf("тренировка: %+v", ws)
	}
}
