// Package habits хранит список привычек и их якорей: что развивается и что запускает
// действие. Источник правды — файл HABITS_FILE, правится руками (docs/HABITS.md).
package habits

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxActive — предел активных привычек. Намеренно мал: новое поведение строится
// по одному действию в одном контексте, а не списком из десяти пунктов.
const MaxActive = 5

// Habit — одна привычка: «когда <Anchor> → <Action>».
type Habit struct {
	ID         string `json:"id"`
	Emoji      string `json:"emoji,omitempty"`
	Title      string `json:"title"`
	Anchor     string `json:"anchor"` // контекст-триггер; {wake} заменяется на WAKE_ANCHOR
	Action     string `json:"action"`
	FloorLabel string `json:"floor_label,omitempty"` // непусто — входит в пол дня
	Since      string `json:"since"`                 // YYYY-MM-DD, от неё считаются недели
	Paused     bool   `json:"paused,omitempty"`
}

// File — содержимое habits.json.
type File struct {
	Habits []Habit `json:"habits"`
}

// Defaults — стартовый набор: пол дня из ТЗ плюс подъём, у каждого свой якорь.
// Улица и вещь идут цепочкой: окончание одного действия запускает следующее.
func Defaults(today string) File {
	return File{Habits: []Habit{
		{
			ID: "wake", Emoji: "⏰", Title: "Подъём", Since: today,
			Anchor: "звонит будильник в {wake}",
			Action: "сразу встаю, ноги на пол",
		},
		{
			ID: "teeth", Emoji: "🦷", Title: "Зубы", Since: today,
			Anchor:     "встал с кровати",
			Action:     "зубы до телефона",
			FloorLabel: "зубы",
		},
		{
			ID: "outside", Emoji: "🚶", Title: "Улица", Since: today,
			Anchor:     "закрыл ноутбук после работы",
			Action:     "куртка и 20 минут на улице",
			FloorLabel: "выйти на улицу",
		},
		{
			ID: "tidy", Emoji: "📦", Title: "Вещь на место", Since: today,
			Anchor:     "вернулся с улицы",
			Action:     "кладу одну вещь на место",
			FloorLabel: "одна вещь на место",
		},
	}}
}

// Parse разбирает и проверяет содержимое файла.
func Parse(raw []byte) (File, error) {
	var f File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // опечатка в имени поля не должна молча терять якорь
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("разбор JSON: %w", err)
	}
	return f, f.Validate()
}

// Validate проверяет файл и возвращает все ошибки сразу.
func (f File) Validate() error {
	var errs []error
	seen := make(map[string]bool)
	active := 0
	for i, h := range f.Habits {
		label := h.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
			errs = append(errs, fmt.Errorf("%s: нет id", label))
		}
		if seen[h.ID] {
			errs = append(errs, fmt.Errorf("%s: id повторяется", label))
		}
		seen[h.ID] = true
		if strings.TrimSpace(h.Title) == "" {
			errs = append(errs, fmt.Errorf("%s: нет title", label))
		}
		if strings.TrimSpace(h.Anchor) == "" {
			errs = append(errs, fmt.Errorf("%s: нет anchor — привычка без якоря не запускается", label))
		}
		if strings.TrimSpace(h.Action) == "" {
			errs = append(errs, fmt.Errorf("%s: нет action", label))
		}
		if _, err := time.Parse(time.DateOnly, h.Since); err != nil {
			errs = append(errs, fmt.Errorf("%s: since должен быть в виде YYYY-MM-DD", label))
		}
		if !h.Paused {
			active++
		}
	}
	if active == 0 {
		errs = append(errs, errors.New("нет ни одной активной привычки"))
	}
	if active > MaxActive {
		errs = append(errs, fmt.Errorf("активных привычек %d, предел %d: лишние отметь paused", active, MaxActive))
	}
	return errors.Join(errs...)
}

// Load читает файл. Если его нет — создаёт из Defaults, чтобы было что править.
func Load(path, today string) ([]byte, File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		f := Defaults(today)
		raw, err = Encode(f)
		if err != nil {
			return nil, File{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, File{}, fmt.Errorf("создание каталога: %w", err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, File{}, fmt.Errorf("запись файла: %w", err)
		}
		return raw, f, nil
	}
	if err != nil {
		return nil, File{}, fmt.Errorf("чтение файла: %w", err)
	}
	f, err := Parse(raw)
	return raw, f, err
}

// Encode форматирует файл для чтения человеком.
func Encode(f File) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// Active — привычки без паузы, в порядке файла.
func (f File) Active() []Habit {
	out := make([]Habit, 0, len(f.Habits))
	for _, h := range f.Habits {
		if !h.Paused {
			out = append(out, h)
		}
	}
	return out
}

// FloorLabels — состав пола дня для вечернего сообщения.
func (f File) FloorLabels() []string {
	var out []string
	for _, h := range f.Active() {
		if h.FloorLabel != "" {
			out = append(out, h.FloorLabel)
		}
	}
	return out
}

// AnchorText подставляет время подъёма в текст якоря.
func (h Habit) AnchorText(wake string) string {
	return strings.ReplaceAll(h.Anchor, "{wake}", wake)
}

// Week — номер недели с начала привычки. Это возраст, а не стрик: пропуски его не сбрасывают.
func (h Habit) Week(today string) int {
	since, err1 := time.Parse(time.DateOnly, h.Since)
	now, err2 := time.Parse(time.DateOnly, today)
	if err1 != nil || err2 != nil || now.Before(since) {
		return 1
	}
	return int(now.Sub(since).Hours()/24)/7 + 1
}
