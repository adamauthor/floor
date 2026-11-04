// Package webapp — обмен с страницей настроек (Telegram Mini App, docs/app/).
//
// Сервера у страницы нет: текущее состояние бот кладёт в адрес кнопки после #
// (фрагмент не уходит на хостинг), а изменения страница возвращает через
// Telegram.WebApp.sendData — они приходят боту обычным сообщением владельца.
package webapp

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"floor/internal/config"
	"floor/internal/habits"
)

// Version — версия формата обмена; страница отвергает незнакомую.
const Version = 1

// State — то, что страница показывает и возвращает.
type State struct {
	V        int             `json:"v"`
	Rev      string          `json:"rev"` // версия, от которой начата правка
	Settings config.Settings `json:"s"`
	Habits   []habits.Habit  `json:"h"`
}

// Rev — короткий отпечаток настроек: правка, начатая со старой версии, не перезапишет новую.
func Rev(s config.Settings, f habits.File) string {
	raw, _ := json.Marshal(State{V: Version, Settings: s, Habits: f.Habits})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

// NewState собирает состояние для страницы.
func NewState(s config.Settings, f habits.File) State {
	return State{V: Version, Rev: Rev(s, f), Settings: s, Habits: f.Habits}
}

// URL — адрес страницы с состоянием во фрагменте: JSON, сжатый deflate, в base64url.
func URL(base string, st State) (string, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return strings.SplitN(base, "#", 2)[0] + "#z" + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// Parse разбирает данные от страницы.
func Parse(data string) (State, error) {
	var st State
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return State{}, fmt.Errorf("разбор данных: %w", err)
	}
	if st.V != Version {
		return State{}, fmt.Errorf("версия %d, ожидается %d: обнови страницу", st.V, Version)
	}
	if st.Rev == "" {
		return State{}, errors.New("нет версии настроек")
	}
	return st, nil
}

// Changes — человеческий список отличий: что увидит владелец после сохранения.
func Changes(oldS, newS config.Settings, oldH, newH []habits.Habit) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if oldS.WakeAnchor != newS.WakeAnchor {
		add("подъём %s → %s", oldS.WakeAnchor, newS.WakeAnchor)
	}
	if oldS.Tolerance != newS.Tolerance {
		add("допуск ±%d → ±%d мин", oldS.Tolerance, newS.Tolerance)
	}
	if oldS.StepsFloor != newS.StepsFloor || oldS.StepsGoal != newS.StepsGoal {
		add("шаги: пол %d, цель %d", newS.StepsFloor, newS.StepsGoal)
	}
	if oldS.EveningAt != newS.EveningAt {
		add("вечерний вопрос %s → %s", oldS.EveningAt, newS.EveningAt)
	}
	if oldS.WeeklyAt != newS.WeeklyAt {
		add("итог недели %s → %s", oldS.WeeklyAt, newS.WeeklyAt)
	}

	before := make(map[string]habits.Habit, len(oldH))
	for _, h := range oldH {
		before[h.ID] = h
	}
	after := make(map[string]bool, len(newH))
	for _, h := range newH {
		after[h.ID] = true
		prev, ok := before[h.ID]
		switch {
		case !ok:
			add("новая привычка «%s»", h.Title)
		case prev.Paused != h.Paused && h.Paused:
			add("«%s» на паузе", h.Title)
		case prev.Paused != h.Paused:
			add("«%s» снова в деле", h.Title)
		case prev != h:
			add("«%s» изменена", h.Title)
		}
	}
	for _, h := range oldH {
		if !after[h.ID] {
			add("«%s» удалена", h.Title)
		}
	}
	if len(out) == 0 && !sameOrder(oldH, newH) {
		add("порядок привычек")
	}
	return out
}

func sameOrder(a, b []habits.Habit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
