package core

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func (h *harness) waitPhotos(t *testing.T, n int) []sent {
	t.Helper()
	for i := 0; i < 300; i++ {
		if got := h.tg.photos(); len(got) >= n {
			return got
		}
		waitShort()
	}
	t.Fatalf("картинок меньше %d: %d", n, len(h.tg.photos()))
	return nil
}

func savePhoto(t *testing.T, name string, m sent) {
	t.Helper()
	if _, err := png.Decode(bytes.NewReader(m.photo)); err != nil {
		t.Fatalf("%s: не PNG: %v", name, err)
	}
	if dir := os.Getenv("CHART_OUT"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name), m.photo, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChartsAfterReports(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 10:30", plus5)
	h.tick("2026-11-01 20:00", plus5)
	got := h.waitPhotos(t, 1)
	savePhoto(t, "core-week.png", got[0])
	if id := got[0].id; h.tg.matching("📅")[0].id > id {
		t.Error("картинка раньше текста сводки")
	}

	h.withMoney(t)
	h.tick("2026-11-20 12:00", plus5)
	h.app.OnText(ctx, "карта 100к, лайв 20 000, дф 550к, подушка 300к, долг 1 500 000")
	c := h.tg.matching("Сверка на")
	h.app.OnCallback(ctx, c[len(c)-1].rows[0][0].Data, c[len(c)-1].id)
	got = h.waitPhotos(t, 2)
	savePhoto(t, "core-money.png", got[1])
}

func TestChartOnRequest(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-05 10:30", plus5)
	h.llm.reply = "Держи месяц.\nДЕЙСТВИЯ: [{\"op\":\"chart\",\"what\":\"month\"}]"
	h.app.OnText(ctx, "покажи график за месяц")
	got := h.waitPhotos(t, 1)
	savePhoto(t, "core-month.png", got[0])
	if len(h.tg.matching("Держи месяц.")) != 1 || len(h.tg.matching("ДЕЙСТВИЯ")) != 0 {
		t.Error("текст ответа")
	}
	for _, m := range h.tg.msgs {
		for _, b := range m.buttons {
			if b.Data == callbackDo+"ok" {
				t.Error("график спрашивает «Да»")
			}
		}
	}

	// График вместе с изменением: картинка сразу, изменение ждёт «Да».
	h.llm.reply = "Предлагаю.\nДЕЙСТВИЯ: [{\"op\":\"chart\",\"what\":\"week\"},{\"op\":\"settings\",\"steps_floor\":6000}]"
	h.app.OnText(ctx, "пол шагов 6000 и покажи неделю")
	h.waitPhotos(t, 2)
	if len(h.tg.matching("Пол шагов")) != 1 {
		t.Error("нет подтверждения изменения")
	}

	h.llm.reply = "Не то.\nДЕЙСТВИЯ: [{\"op\":\"chart\",\"what\":\"year\"}]"
	h.app.OnText(ctx, "покажи год")
	if len(h.tg.matching("нет такого графика")) != 1 || len(h.tg.photos()) != 2 {
		t.Error("неизвестный график не отклонён")
	}
}
