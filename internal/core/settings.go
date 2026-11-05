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

	settingsLabel = "Floor"
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
	_, st.Watch = a.watcher(ctx)
	if token, _ := a.Store.GetKV(ctx, kvWatchToken); st.Watch == "" && token != "" {
		st.Watch = watchPending
	}
	if a.API != nil {
		if api, err := a.API(ctx); err == nil {
			st.API = api
		} else {
			a.Log.Debug("webapp_api_unavailable", "error", err)
		}
	}
	return st
}

// menuKey — что сейчас в кнопке меню: версия настроек, адрес туннеля, подпись и получатель итога.
func menuKey(st webapp.State) string {
	return "menu " + st.Rev + " " + st.API + " " + settingsLabel + " " + st.Watch
}

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
		text := "Привычки, время и шаги — кнопка «Floor» слева от поля ввода."
		if stored != "" {
			text = "Настройки переехали в кнопку «Floor» слева от поля ввода."
		}
		if err := a.TG.RemoveKeyboard(ctx, text); err != nil {
			return err
		}
	}
	return a.setMenu(ctx, st)
}

// OnSettings — команда /settings: обновить кнопку меню и подсказать, где она.
func (a *App) OnSettings(ctx context.Context) {
	a.trackIncoming(ctx, kindCommand)
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	if err := a.setMenu(ctx, a.menuState(ctx, a.currentHabits(ctx, today))); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
	if _, err := a.say(ctx, kindNotice, "Настройки — кнопка «Floor» слева от поля ввода.", nil); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
}

// OnWebAppData — настройки со старой кнопки под полем ввода (sendData).
// Окно при этом закрывается, поэтому итог уходит в чат.
func (a *App) OnWebAppData(ctx context.Context, data string) {
	a.trackIncoming(ctx, kindCommand)
	res := a.SaveSettings(ctx, data)
	if res.Invite != "" {
		return // ссылка уже в чате
	}
	if _, err := a.say(ctx, kindNotice, res.Text, nil); err != nil {
		a.Log.Warn("settings_send_failed", "error", err)
	}
}

// SaveSettings сохраняет настройки со страницы и возвращает итог вместе с новой версией:
// страница остаётся открытой, показывает «Сохранено» и может править дальше. В чат уходит
// только ссылка-приглашение — её удобнее переслать оттуда.
func (a *App) SaveSettings(ctx context.Context, data string) webapp.SaveResult {
	a.tickMu.Lock()
	text, saved, invite := a.saveSettings(ctx, data)
	a.tickMu.Unlock()
	res := webapp.SaveResult{OK: saved, Text: text}
	if invite != "" {
		res.Invite = invite
		if _, err := a.say(ctx, kindReport, "Ссылка для того, кто будет получать итог недели: "+invite, nil); err != nil {
			a.Log.Warn("settings_send_failed", "error", err)
		}
	}
	if saved {
		a.Tick(ctx, a.Now()) // карточка, расписание и кнопка меню сразу видят новое
		today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
		res.Rev = webapp.Rev(a.conf().Settings(), a.currentHabits(ctx, today))
	}
	return res
}

func (a *App) saveSettings(ctx context.Context, data string) (text string, saved bool, invite string) {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	cur := a.conf()
	f := a.currentHabits(ctx, today)

	st, err := webapp.Parse(data)
	if err != nil {
		a.Log.Warn("settings_rejected", "error", err)
		return "Не сохранил: " + escape(err.Error()), false, ""
	}
	if st.Rev != webapp.Rev(cur.Settings(), f) {
		a.Log.Info("settings_stale")
		return "Пока окно было открыто, настройки поменялись. Открой ещё раз — там уже свежие.", false, ""
	}
	next := cur
	nf := habits.File{Habits: normalize(st.Habits, today)}
	err = st.Settings.Apply(&next)
	if err == nil {
		err = nf.Validate()
	}
	if err != nil {
		a.Log.Warn("settings_rejected", "error", err)
		return "Не сохранил:\n" + escape(err.Error()), false, ""
	}

	if msg, err := a.commitSettings(ctx, next, nf); err != nil {
		a.Log.Error("settings_save_failed", "error", err)
		return msg, false, ""
	}

	changes := webapp.Changes(cur.Settings(), next.Settings(), f.Habits, nf.Habits)
	if st.Share != nil {
		line, link, err := a.applyShare(ctx, *st.Share)
		if err != nil {
			a.Log.Warn("share_failed", "error", err)
		}
		if line != "" {
			changes = append(changes, line)
		}
		invite = link
	}
	a.Log.Info("settings_saved", "changes", len(changes))
	text = "✅ Сохранено"
	if len(changes) > 0 {
		text += ": " + escape(strings.Join(changes, ", "))
	}
	return text, true, invite
}

// commitSettings записывает привычки и настройки и применяет их без перезапуска.
// При ошибке возвращает текст для владельца.
func (a *App) commitSettings(ctx context.Context, next config.Config, nf habits.File) (string, error) {
	if err := habits.Save(next.HabitsFile, nf); err != nil {
		return "Не сохранил: " + escape(err.Error()), err
	}
	raw, _ := json.Marshal(next.Settings())
	if err := a.Store.SetKV(ctx, kvSettings, string(raw)); err != nil {
		return "Привычки сохранил, а время и шаги — нет. Попробуй ещё раз.", err
	}
	a.setConf(next)
	if err := a.syncWakeDeltas(ctx); err != nil {
		a.Log.Warn("wake_deltas_failed", "error", err)
	}
	return "", nil
}

// applyShare включает или выключает итог недели второму человеку и говорит, что сделано.
func (a *App) applyShare(ctx context.Context, on bool) (line, link string, err error) {
	chat, _ := a.watcher(ctx)
	switch {
	case on && chat == 0:
		link, err := a.invite(ctx)
		if err != nil {
			return "", "", err
		}
		return "ссылка на итог недели отправлена в чат", link, nil
	case !on:
		token, err := a.Store.GetKV(ctx, kvWatchToken)
		if err != nil || (chat == 0 && token == "") {
			return "", "", err
		}
		return "итог недели больше никому не уходит", "", a.unwatch(ctx)
	}
	return "", "", nil
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
		h.Floor = strings.TrimSpace(h.Floor)
		h.OldFloor = ""
		if h.Since == "" {
			h.Since = today
		}
		out = append(out, h)
	}
	return out
}
