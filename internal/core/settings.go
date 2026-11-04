package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/metrics"
	"floor/internal/webapp"
)

const (
	kvSettings    = "settings"
	kvSettingsRev = "settings_button_rev"
	kvDeltaAnchor = "wake_delta_anchor"

	settingsLabel = "⚙️ Настройки"
)

// conf — текущая конфигурация: настройки из Telegram меняют её на ходу.
func (a *App) conf() config.Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.Cfg
}

func (a *App) setConf(c config.Config) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	a.Cfg = c
}

// LoadSettings применяет сохранённые настройки поверх окружения. Вызывается до запуска.
func (a *App) LoadSettings(ctx context.Context) error {
	raw, err := a.Store.GetKV(ctx, kvSettings)
	if err != nil {
		return err
	}
	if raw != "" {
		var s config.Settings
		c := a.conf()
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			a.Log.Warn("settings_invalid", "error", err)
		} else if err := s.Apply(&c); err != nil {
			a.Log.Warn("settings_invalid", "error", err)
		} else {
			a.setConf(c)
			a.Log.Info("settings_loaded", "wake_anchor", c.WakeAnchor.String())
		}
	}
	return a.syncWakeDeltas(ctx)
}

// syncWakeDeltas пересчитывает отклонения подъёма, если они посчитаны от другого якоря.
func (a *App) syncWakeDeltas(ctx context.Context) error {
	anchor := a.conf().WakeAnchor
	done, err := a.Store.GetKV(ctx, kvDeltaAnchor)
	if err != nil || done == anchor.String() {
		return err
	}
	err = a.Store.RewriteWakeDeltas(ctx, func(date string, wake time.Time) int {
		return metrics.WakeDelta(wake, date, anchor)
	})
	if err != nil {
		return err
	}
	a.Log.Info("wake_deltas_rewritten", "wake_anchor", anchor.String())
	return a.Store.SetKV(ctx, kvDeltaAnchor, anchor.String())
}

// menuState — состояние для кнопки меню вместе с текущим адресом туннеля.
func (a *App) menuState(ctx context.Context, f habits.File) webapp.State {
	st := webapp.NewState(a.conf().Settings(), f)
	if a.API != nil {
		if api, err := a.API(ctx); err == nil {
			st.API = api
		} else {
			a.Log.Debug("webapp_api_unavailable", "error", err)
		}
	}
	return st
}

// menuKey — что сейчас в кнопке меню: версия настроек и адрес туннеля.
func menuKey(st webapp.State) string { return "menu " + st.Rev + " " + st.API }

// setMenu ставит кнопку меню с состоянием в адресе.
func (a *App) setMenu(ctx context.Context, st webapp.State) error {
	url, err := webapp.URL(a.conf().WebAppURL, st)
	if err != nil {
		return err
	}
	if err := a.TG.SetMenuApp(ctx, settingsLabel, url); err != nil {
		return fmt.Errorf("кнопка настроек: %w", err)
	}
	return a.Store.SetKV(ctx, kvSettingsRev, menuKey(st))
}

// syncSettingsButton обновляет кнопку меню, когда поменялись настройки (в том числе
// правкой habits.json руками) или адрес туннеля. Иначе страница откроется со старыми
// данными или не сможет сохранить.
func (a *App) syncSettingsButton(ctx context.Context, local time.Time) error {
	if a.conf().WebAppURL == "" {
		return nil
	}
	st := a.menuState(ctx, a.currentHabits(ctx, local.Format(time.DateOnly)))
	stored, err := a.Store.GetKV(ctx, kvSettingsRev)
	if err != nil || stored == menuKey(st) {
		return err
	}
	if !strings.HasPrefix(stored, "menu ") {
		// Первый запуск или переезд с кнопки под полем ввода: её надо убрать.
		text := "⚙️ Привычки, время и шаги — кнопка «Настройки» слева от поля ввода."
		if stored != "" {
			text = "⚙️ Настройки переехали в кнопку слева от поля ввода."
		}
		if err := a.TG.RemoveKeyboard(ctx, text); err != nil {
			return err
		}
	}
	return a.setMenu(ctx, st)
}

// OnSettings — команда /settings: обновить кнопку меню и подсказать, где она.
func (a *App) OnSettings(ctx context.Context) {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	if err := a.setMenu(ctx, a.menuState(ctx, a.currentHabits(ctx, today))); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
	if _, err := a.TG.Send(ctx, "Настройки — кнопка «⚙️ Настройки» слева от поля ввода.", nil); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
}

// OnWebAppData — настройки со старой кнопки под полем ввода (sendData).
func (a *App) OnWebAppData(ctx context.Context, data string) { a.SaveSettings(ctx, data) }

// SaveSettings сохраняет настройки со страницы, пишет итог в чат и возвращает его.
func (a *App) SaveSettings(ctx context.Context, data string) (string, bool) {
	a.tickMu.Lock()
	text, saved := a.saveSettings(ctx, data)
	a.tickMu.Unlock()
	if _, err := a.TG.Send(ctx, text, nil); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
	if saved {
		a.Tick(ctx, a.Now()) // карточка, расписание и кнопка меню сразу видят новое
	}
	return text, saved
}

func (a *App) saveSettings(ctx context.Context, data string) (string, bool) {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	cur := a.conf()
	f := a.currentHabits(ctx, today)

	st, err := webapp.Parse(data)
	if err != nil {
		a.Log.Warn("settings_rejected", "error", err)
		return "Не сохранил: " + escape(err.Error()), false
	}
	if st.Rev != webapp.Rev(cur.Settings(), f) {
		a.Log.Info("settings_stale")
		return "Пока окно было открыто, настройки поменялись. Открой ещё раз — там уже свежие.", false
	}
	next := cur
	nf := habits.File{Habits: normalize(st.Habits, today)}
	err = st.Settings.Apply(&next)
	if err == nil {
		err = nf.Validate()
	}
	if err != nil {
		a.Log.Warn("settings_rejected", "error", err)
		return "Не сохранил:\n" + escape(err.Error()), false
	}

	if err := habits.Save(cur.HabitsFile, nf); err != nil {
		a.Log.Error("settings_save_failed", "error", err)
		return "Не сохранил: " + escape(err.Error()), false
	}
	raw, _ := json.Marshal(next.Settings())
	if err := a.Store.SetKV(ctx, kvSettings, string(raw)); err != nil {
		a.Log.Error("settings_save_failed", "error", err)
		return "Привычки сохранил, а время и шаги — нет. Попробуй ещё раз.", false
	}
	a.setConf(next)
	if err := a.syncWakeDeltas(ctx); err != nil {
		a.Log.Warn("wake_deltas_failed", "error", err)
	}

	changes := webapp.Changes(cur.Settings(), next.Settings(), f.Habits, nf.Habits)
	a.Log.Info("settings_saved", "changes", len(changes))
	text := "✅ Сохранено"
	if len(changes) > 0 {
		text += ": " + escape(strings.Join(changes, ", "))
	}
	return text, true
}

// normalize чистит пробелы и ставит дату начала новым привычкам.
func normalize(hs []habits.Habit, today string) []habits.Habit {
	out := make([]habits.Habit, 0, len(hs))
	for _, h := range hs {
		h.ID = strings.TrimSpace(h.ID)
		h.Emoji = strings.TrimSpace(h.Emoji)
		h.Title = strings.TrimSpace(h.Title)
		h.Anchor = strings.TrimSpace(h.Anchor)
		h.Action = strings.TrimSpace(h.Action)
		h.FloorLabel = strings.TrimSpace(h.FloorLabel)
		if h.Since == "" {
			h.Since = today
		}
		out = append(out, h)
	}
	return out
}
