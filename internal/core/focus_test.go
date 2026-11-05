package core

import (
	"strings"
	"testing"

	"floor/internal/metrics"
	"floor/internal/store"
)

// fillWeek — неделя с понедельника: шаги, отметки и итог дня.
func (h *harness) fillWeek(t *testing.T, monday string, steps []int, outside []int) {
	t.Helper()
	for i := 0; i < 7; i++ {
		date := metrics.AddDays(monday, i)
		if i < len(steps) {
			if err := h.db.UpsertSteps(ctx, date, "+05:00", steps[i], h.now); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"wake", "teeth", "tidy"} {
			if err := h.db.SetMark(ctx, date, id, store.LevelFloor, h.now); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.db.SetMark(ctx, date, "outside", outside[i], h.now); err != nil {
			t.Fatal(err)
		}
		status := "yes"
		if outside[i] == store.LevelMiss {
			status = "no"
		}
		if err := h.db.SetFloor(ctx, date, "+05:00", status, h.now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFocusWeeklyAndMonday(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-26 08:00", plus5)
	h.fillWeek(t, "2026-10-26", []int{4100, 9600, 2300, 8400, 7812, 3000}, []int{1, 0, 0, 2, 0, 1, 0})
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"

	h.tick("2026-11-01 20:00", plus5)
	w := h.tg.matching("📅")
	if len(w) != 1 {
		t.Fatalf("недельных: %d", len(w))
	}
	want := "🎯 <b>Поднажать</b>\n· 🚶 Улица — пол 3 из 7\n· 👣 Шаги — пол 3 из 6"
	if !strings.Contains(w[0].text, want) {
		t.Errorf("нет блока:\n%s\nв\n%s", want, w[0].text)
	}

	h.tick("2026-11-02 10:30", plus5)
	m := h.tg.matching("☀️")
	if !strings.Contains(m[len(m)-1].text, "🎯 На этой неделе поднажать: 🚶 Улица — пол 3 из 7 на прошлой") {
		t.Errorf("утро понедельника:\n%s", m[len(m)-1].text)
	}
}

func TestFocusAllGoodAndNoData(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	// Без данных блока нет вовсе.
	h.tick("2026-11-01 20:00", plus5)
	if w := h.tg.matching("📅"); len(w) != 1 || strings.Contains(w[0].text, "🎯") {
		t.Errorf("без данных: %+v", w)
	}
	h.fillWeek(t, "2026-11-02", []int{9000, 9000, 9000, 9000, 9000, 9000}, []int{1, 1, 1, 1, 1, 1, 1})
	h.tick("2026-11-08 20:00", plus5)
	if w := h.tg.matching("📅"); !strings.Contains(w[len(w)-1].text, "🎯 Всё держится на полу") {
		t.Errorf("всё держится: %s", w[len(w)-1].text)
	}
}

func TestMonthSummary(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-01 08:00", plus5)
	for _, monday := range []string{"2026-11-02", "2026-11-09", "2026-11-16", "2026-11-23"} {
		h.fillWeek(t, monday, []int{8000, 8000, 8000, 8000, 8000, 8000, 8000}, []int{1, 0, 0, 1, 0, 1, 0})
	}
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"
	h.tick("2026-12-06 20:00", plus5)
	m := h.tg.matching("📊")
	if len(m) != 1 {
		t.Fatalf("сводок месяца: %d", len(m))
	}
	for _, want := range []string{"📊 <b>Ноябрь 2026</b>", "Пол дня: 12 из 28", "Шаги: пол 28 из 28, в среднем 8 000",
		"🚶 Улица: пол 12 из 28", "🎯 <b>Поднажать</b>\n· 🚶 Улица — пол 12 из 28"} {
		if !strings.Contains(spaces(m[0].text), want) {
			t.Errorf("нет %q в\n%s", want, m[0].text)
		}
	}
	h.tick("2026-12-06 20:30", plus5)
	if len(h.tg.matching("📊")) != 1 {
		t.Error("сводка месяца повторилась")
	}
}

func TestWeak(t *testing.T) {
	got := metrics.Weak([]metrics.Area{
		{Key: "a", Done: 6, Of: 7, Need: 5},
		{Key: "b", Done: 1, Of: 7, Need: 5},
		{Key: "c", Done: 3, Of: 7, Need: 5},
		{Key: "d", Done: 0, Of: 2, Need: 2},               // мало данных
		{Key: "t", Done: 1, Of: 3, Need: 2, Weekly: true}, // тренировки ниже пола
	}, 3)
	if len(got) != 3 || got[0].Key != "b" || got[1].Key != "t" || got[2].Key != "c" {
		t.Errorf("слабые: %+v", got)
	}
	if metrics.NeedFor(7) != 5 || metrics.NeedFor(6) != 5 || metrics.NeedFor(30) != 21 {
		t.Error("NeedFor")
	}
}
