package core

import (
	"encoding/json"
	"strings"
	"testing"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/webapp"
)

// submit собирает ответ страницы от текущего состояния с правкой.
func (h *harness) submit(t *testing.T, edit func(*webapp.State)) string {
	t.Helper()
	_, f, err := habits.Load(h.app.Cfg.HabitsFile, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	st := webapp.NewState(h.app.conf().Settings(), f)
	edit(&st)
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (h *harness) lastWebApp(t *testing.T) sent {
	t.Helper()
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	if len(h.tg.webapps) == 0 {
		t.Fatal("кнопка настроек не отправлена")
	}
	return h.tg.webapps[len(h.tg.webapps)-1]
}

func TestSettingsSavedFromWebApp(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.WebAppURL = "https://example.test/app/"
	h.tick("2026-11-04 08:00", plus5) // карточка и первая кнопка
	if got := h.lastWebApp(t); !strings.Contains(got.text, "под полем ввода") ||
		!strings.HasPrefix(got.buttons[0].Data, "https://example.test/app/#z") {
		t.Fatalf("первая кнопка: %+v", got)
	}

	data := h.submit(t, func(st *webapp.State) {
		st.Settings.WakeAnchor = "09:30"
		st.Settings.StepsFloor = 5000
		st.Habits = append(st.Habits, habits.Habit{ID: "read", Emoji: "📖", Title: "Чтение", Anchor: "лёг", Action: "страница книги"})
	})
	h.app.OnWebAppData(ctx, data)

	got := h.lastWebApp(t)
	for _, want := range []string{"✅ Сохранено", "подъём 07:00 → 09:30", "пол 5000", "новая привычка «Чтение»"} {
		if !strings.Contains(got.text, want) {
			t.Errorf("нет %q в %q", want, got.text)
		}
	}
	if c := h.app.conf(); c.WakeAnchor != (config.Clock{Hour: 9, Minute: 30}) || c.StepsFloor != 5000 {
		t.Errorf("конфигурация не обновилась: %+v", c.Settings())
	}
	_, f, err := habits.Load(h.app.Cfg.HabitsFile, "2026-11-04")
	if err != nil || len(f.Habits) != 5 || f.Habits[4].Since != "2026-11-04" {
		t.Fatalf("файл: %+v, %v", f, err)
	}
	h.tg.mu.Lock()
	edited := len(h.tg.edits) > 0 && strings.Contains(h.tg.edits[len(h.tg.edits)-1].text, "Чтение")
	h.tg.mu.Unlock()
	if !edited {
		t.Error("карточка привычек не обновилась сразу")
	}

	// Перезапуск: настройки из базы перекрывают окружение.
	again := h.build()
	if err := again.LoadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if again.conf().WakeAnchor.String() != "09:30" {
		t.Errorf("после перезапуска подъём %s", again.conf().WakeAnchor)
	}

	// Тик после сохранения не шлёт лишнюю кнопку: версия совпадает.
	n := len(h.tg.webapps)
	h.tick("2026-11-04 08:05", plus5)
	if len(h.tg.webapps) != n {
		t.Error("лишнее сообщение с кнопкой")
	}
}

func TestStaleSettingsRejected(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-04 08:00", plus5)
	data := h.submit(t, func(st *webapp.State) {
		st.Rev = "old"
		st.Settings.WakeAnchor = "05:00"
	})
	h.app.OnWebAppData(ctx, data)
	if got := h.lastWebApp(t); !strings.Contains(got.text, "поменялись") {
		t.Errorf("ответ: %q", got.text)
	}
	if h.app.conf().WakeAnchor.Hour != 7 {
		t.Error("устаревшая правка применилась")
	}
}

func TestInvalidSettingsRejected(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-04 08:00", plus5)
	for name, edit := range map[string]func(*webapp.State){
		"пол больше цели": func(st *webapp.State) { st.Settings.StepsFloor = 20000 },
		"без якоря":       func(st *webapp.State) { st.Habits[0].Anchor = "  " },
		"шесть привычек": func(st *webapp.State) {
			for i := range 2 {
				st.Habits = append(st.Habits, habits.Habit{ID: "x" + string(rune('a'+i)), Title: "x", Anchor: "x", Action: "x"})
			}
		},
	} {
		h.app.OnWebAppData(ctx, h.submit(t, edit))
		if got := h.lastWebApp(t); !strings.HasPrefix(got.text, "Не сохранил") {
			t.Errorf("%s: ответ %q", name, got.text)
		}
	}
	h.app.OnWebAppData(ctx, "не json")
	if got := h.lastWebApp(t); !strings.HasPrefix(got.text, "Не сохранил") {
		t.Errorf("мусор: ответ %q", got.text)
	}
	_, f, _ := habits.Load(h.app.Cfg.HabitsFile, "2026-11-04")
	if len(f.Habits) != 4 || h.app.conf().StepsFloor != 7000 {
		t.Error("неверная правка что-то поменяла")
	}
}

func TestAnchorChangeRewritesWakeDeltas(t *testing.T) {
	h := newHarness(t)
	h.apply(t, exportJSON("2026-11-04T12:00:00+05:00", "+05:00", "",
		`{"start":"2026-11-04T01:00:00+05:00","end":"2026-11-04T09:00:00+05:00","value":"core"}`))
	h.tick("2026-11-04 12:00", plus5)
	if d, _ := h.db.GetDay(ctx, "2026-11-04"); *d.WakeDeltaMin != 120 {
		t.Fatalf("Δ = %d", *d.WakeDeltaMin)
	}
	h.app.OnWebAppData(ctx, h.submit(t, func(st *webapp.State) { st.Settings.WakeAnchor = "09:30" }))
	if d, _ := h.db.GetDay(ctx, "2026-11-04"); *d.WakeDeltaMin != -30 {
		t.Errorf("Δ после смены якоря = %d", *d.WakeDeltaMin)
	}
}

func TestHandEditedHabitsRefreshButton(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.WebAppURL = "https://example.test/app/"
	h.tick("2026-11-04 08:00", plus5)
	f := habits.Defaults("2026-11-04")
	f.Habits[0].Action = "встаю"
	if err := habits.Save(h.app.Cfg.HabitsFile, f); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-11-04 08:01", plus5)
	if got := h.lastWebApp(t); got.text != "⚙️ Настройки обновлены." {
		t.Errorf("ответ: %q", got.text)
	}
}
