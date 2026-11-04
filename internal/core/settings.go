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

// sendSettings отправляет сообщение с кнопкой настроек, в адресе которой текущее состояние.
func (a *App) sendSettings(ctx context.Context, text string, f habits.File) error {
	st := webapp.NewState(a.conf().Settings(), f)
	url, err := webapp.URL(a.conf().WebAppURL, st)
	if err != nil {
		return err
	}
	if _, err := a.TG.SendWebApp(ctx, text, settingsLabel, url); err != nil {
		return fmt.Errorf("кнопка настроек: %w", err)
	}
	return a.Store.SetKV(ctx, kvSettingsRev, st.Rev)
}

// syncSettingsButton обновляет кнопку, когда настройки поменялись мимо неё: правкой
// habits.json руками или первым запуском. Иначе страница откроется со старыми данными.
func (a *App) syncSettingsButton(ctx context.Context, local time.Time) error {
	if a.conf().WebAppURL == "" {
		return nil
	}
	f := a.currentHabits(ctx, local.Format(time.DateOnly))
	stored, err := a.Store.GetKV(ctx, kvSettingsRev)
	if err != nil || stored == webapp.Rev(a.conf().Settings(), f) {
		return err
	}
	text := "⚙️ Настройки обновлены."
	if stored == "" {
		text = "⚙️ Привычки, время и шаги теперь меняются кнопкой «Настройки» под полем ввода."
	}
	return a.sendSettings(ctx, text, f)
}

// OnSettings — команда /settings: прислать свежую кнопку.
func (a *App) OnSettings(ctx context.Context) {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	if err := a.sendSettings(ctx, "Кнопка «⚙️ Настройки» — под полем ввода.", a.currentHabits(ctx, today)); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
}

// OnWebAppData сохраняет настройки, присланные страницей.
func (a *App) OnWebAppData(ctx context.Context, data string) {
	a.tickMu.Lock()
	saved := a.saveSettings(ctx, data)
	a.tickMu.Unlock()
	if saved {
		a.Tick(ctx, a.Now()) // карточка и расписание сразу видят новое
	}
}

func (a *App) saveSettings(ctx context.Context, data string) bool {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	cur := a.conf()
	f := a.currentHabits(ctx, today)
	reply := func(text string) {
		if err := a.sendSettings(ctx, text, a.currentHabits(ctx, today)); err != nil {
			a.Log.Warn("settings_send_failed", "error", err)
		}
	}

	st, err := webapp.Parse(data)
	if err != nil {
		a.Log.Warn("settings_rejected", "error", err)
		reply("Не сохранил: " + escape(err.Error()))
		return false
	}
	if st.Rev != webapp.Rev(cur.Settings(), f) {
		a.Log.Info("settings_stale")
		reply("Пока окно было открыто, настройки поменялись. Открой ещё раз — там уже свежие.")
		return false
	}
	next := cur
	nf := habits.File{Habits: normalize(st.Habits, today)}
	err = st.Settings.Apply(&next)
	if err == nil {
		err = nf.Validate()
	}
	if err != nil {
		a.Log.Warn("settings_rejected", "error", err)
		reply("Не сохранил:\n" + escape(err.Error()))
		return false
	}

	if err := habits.Save(cur.HabitsFile, nf); err != nil {
		a.Log.Error("settings_save_failed", "error", err)
		reply("Не сохранил: " + escape(err.Error()))
		return false
	}
	raw, _ := json.Marshal(next.Settings())
	if err := a.Store.SetKV(ctx, kvSettings, string(raw)); err != nil {
		a.Log.Error("settings_save_failed", "error", err)
		reply("Привычки сохранил, а время и шаги — нет. Попробуй ещё раз.")
		return false
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
	reply(text)
	return true
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
