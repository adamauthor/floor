package core

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
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

// lastMenu — адрес последней поставленной кнопки меню.
func (h *harness) lastMenu(t *testing.T) string {
	t.Helper()
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	if len(h.tg.menus) == 0 {
		t.Fatal("кнопка меню не поставлена")
	}
	return h.tg.menus[len(h.tg.menus)-1].buttons[0].Data
}

func (h *harness) menuCount() int {
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	return len(h.tg.menus)
}

// lastText — текст последнего отправленного сообщения.
func (h *harness) lastText(t *testing.T) string {
	t.Helper()
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	if len(h.tg.msgs) == 0 {
		t.Fatal("сообщений нет")
	}
	return h.tg.msgs[len(h.tg.msgs)-1].text
}

func TestSettingsSavedFromWebApp(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.WebAppURL = "https://example.test/app/"
	api := "https://one.ngrok.test"
	h.app.API = func(context.Context) (string, error) { return api, nil }
	h.tick("2026-11-04 08:00", plus5) // карточка и первая кнопка
	if got := h.lastMenu(t); !strings.HasPrefix(got, "https://example.test/app/#z") {
		t.Fatalf("первая кнопка: %s", got)
	}
	h.tg.mu.Lock()
	removed := len(h.tg.removes) == 1 && strings.Contains(h.tg.removes[0], "слева от поля ввода")
	h.tg.mu.Unlock()
	if !removed {
		t.Error("нет первого сообщения про кнопку меню")
	}

	data := h.submit(t, func(st *webapp.State) {
		st.Settings.WakeAnchor = "09:30"
		st.Settings.StepsFloor = 5000
		st.Habits = append(st.Habits, habits.Habit{ID: "read", Emoji: "📖", Title: "Чтение", Anchor: "лёг", Action: "страница книги", Floor: "открыть книгу"})
	})
	before := len(h.tg.matching("Сохранено"))
	res := h.app.SaveSettings(ctx, data)
	reply := res.Text
	if !res.OK || res.Rev == "" {
		t.Fatalf("ответ %+v", res)
	}
	// Новая версия годится для следующей правки без перезапуска страницы.
	again2 := h.submit(t, func(st *webapp.State) { st.Rev = res.Rev; st.Settings.StepsGoal = 12000 })
	if !h.app.SaveSettings(ctx, again2).OK {
		t.Error("вторая правка с новой версией не прошла")
	}
	if len(h.tg.matching("Сохранено")) != before {
		t.Error("итог сохранения из Mini App ушёл в чат, хватает уведомления на странице")
	}
	for _, want := range []string{"✅ Сохранено", "подъём 07:00 → 09:30", "пол 5000", "новая привычка «Чтение»"} {
		if !strings.Contains(reply, want) {
			t.Errorf("нет %q в %q", want, reply)
		}
	}
	if c := h.app.conf(); c.WakeAnchor != (config.Clock{Hour: 9, Minute: 30}) || c.StepsFloor != 5000 || c.StepsGoal != 12000 {
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

	// Тик после сохранения не трогает кнопку: версия и адрес те же.
	n := h.menuCount()
	h.tick("2026-11-04 08:05", plus5)
	if h.menuCount() != n {
		t.Error("лишнее обновление кнопки")
	}
	// Туннель переподнялся с новым адресом — кнопка обновляется.
	api = "https://two.ngrok.test"
	h.tick("2026-11-04 08:06", plus5)
	if h.menuCount() != n+1 {
		t.Error("кнопка не узнала новый адрес туннеля")
	}
	if st := decodeMenu(t, h.lastMenu(t)); st.API != api {
		t.Errorf("api в кнопке: %q", st.API)
	}
}

func decodeMenu(t *testing.T, url string) webapp.State {
	t.Helper()
	i := strings.Index(url, "#z")
	raw, err := base64.RawURLEncoding.DecodeString(url[i+2:])
	if err != nil {
		t.Fatal(err)
	}
	var st webapp.State
	if err := json.NewDecoder(flate.NewReader(bytes.NewReader(raw))).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStaleSettingsRejected(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-04 08:00", plus5)
	data := h.submit(t, func(st *webapp.State) {
		st.Rev = "old"
		st.Settings.WakeAnchor = "05:00"
	})
	h.app.OnWebAppData(ctx, data)
	if got := h.lastText(t); !strings.Contains(got, "поменялись") {
		t.Errorf("ответ: %q", got)
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
				st.Habits = append(st.Habits, habits.Habit{ID: "x" + string(rune('a'+i)), Title: "x", Anchor: "x", Action: "x", Floor: "x"})
			}
		},
	} {
		h.app.OnWebAppData(ctx, h.submit(t, edit))
		if got := h.lastText(t); !strings.HasPrefix(got, "Не сохранил") {
			t.Errorf("%s: ответ %q", name, got)
		}
	}
	h.app.OnWebAppData(ctx, "не json")
	if got := h.lastText(t); !strings.HasPrefix(got, "Не сохранил") {
		t.Errorf("мусор: ответ %q", got)
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
	n := h.menuCount()
	h.tick("2026-11-04 08:01", plus5)
	if h.menuCount() != n+1 || !strings.Contains(h.lastMenu(t), "#z") {
		t.Error("кнопка не обновилась после правки файла")
	}
}

func TestLegacyKeyboardReplaced(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.WebAppURL = "https://example.test/app/"
	if err := h.db.SetKV(ctx, kvSettingsRev, "abcdef012345"); err != nil { // версия от старой кнопки
		t.Fatal(err)
	}
	h.tick("2026-11-04 08:00", plus5)
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	if len(h.tg.removes) != 1 || !strings.Contains(h.tg.removes[0], "переехали") || len(h.tg.menus) != 1 {
		t.Errorf("removes=%v menus=%d", h.tg.removes, len(h.tg.menus))
	}
}

// Со старой кнопки под полем ввода окно закрывается, поэтому итог приходит в чат.
func TestWebAppDataRepliesInChat(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-04 08:00", plus5)
	h.app.OnWebAppData(ctx, h.submit(t, func(st *webapp.State) { st.Settings.StepsGoal = 11000 }))
	if len(h.tg.matching("✅ Сохранено")) != 1 {
		t.Error("нет итога в чате после sendData")
	}
}
