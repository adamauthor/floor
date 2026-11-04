// Package learning — программа финансовой грамотности: одна тема и одно действие в неделю
// (docs/SPEC_v2.md, раздел 5). Чистые функции, кроме Load.
package learning

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// MaxPostpones — сколько раз подряд тему можно перенести; следующий перенос закрывает её.
const MaxPostpones = 2

// Topic — тема недели.
type Topic struct {
	Title  string `json:"title"`
	Action string `json:"action"`
}

// File — содержимое LEARNING_FILE.
type File struct {
	Topics []Topic `json:"topics"`
}

// Default — встроенная программа на 13 недель.
func Default() []Topic {
	return []Topic{
		{"Куда уходят деньги", "Выгрузить траты зарплатной карты за 3 месяца, разбить на 5–7 категорий"},
		{"Долги: ставка, ГЭСВ, порядок погашения", "Таблица всех долгов со ставками; решить, что гасить досрочно первым"},
		{"Подушка: где хранить, депозиты и гарантия КФГД", "Проверить, приносит ли подушка процент; если нет — найти вариант"},
		{"Инфляция и валюта", "Решить, какая доля подушки и будущих накоплений в долларах"},
		{"Классы активов: акции, облигации, недвижимость, кэш", "Записать свою целевую структуру на после подушки"},
		{"Сложный процент и Freedom number", "Посчитать свой Freedom number и срок при разных суммах в месяц"},
		{"Индексные фонды и ETF, комиссии", "Выбрать 1–2 фонда на весь мир"},
		{"Лёгкая неделя: итоги года в деньгах", "Сравнить сверки ноября и декабря, одна мысль на следующий год"},
		{"Брокеры в Казахстане и налоги на инвестиции", "Сравнить 2–3 брокера: доступ к ETF, комиссии, налоги"},
		{"Поведение: просадки, FOMO, паника", "Записать правила «что делаю, когда рынок упал на 30 %»"},
		{"Ипотека и 7-20-25 на своих цифрах", "Посчитать платёж, переплату и что это делает с планом"},
		{"Страхование: что защищает от катастрофы", "Проверить, что закрыто, а что нет"},
		{"Схема после долгов", "Распределить освободившиеся деньги в месяц: подушка, путешествие, инвестиции. Обновить money.json"},
	}
}

// Load читает программу; нет файла — встроенная.
func Load(path string) ([]Topic, error) {
	raw, err := os.ReadFile(path)
	if path == "" || errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("чтение %s: %w", path, err)
	}
	var f File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Default(), fmt.Errorf("разбор %s: %w", path, err)
	}
	for i, t := range f.Topics {
		if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Action) == "" {
			return Default(), fmt.Errorf("%s: у темы %d нужны title и action", path, i+1)
		}
	}
	if len(f.Topics) == 0 {
		return Default(), fmt.Errorf("%s: нет тем", path)
	}
	return f.Topics, nil
}

// Week — тема на неделю.
type Week struct {
	Index     int // 0-based номер темы
	Total     int
	Topic     Topic
	Repeat    bool // тема повторяется после переноса
	Postpones int
}

// Answers — ответы по понедельникам недель: true — сделано, false — перенос.
type Answers map[string]bool

func monday(date string) time.Time {
	d, _ := time.Parse(time.DateOnly, date)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// Current — тема недели, в которую входит дата. false — программа ещё не началась или закончилась.
// Переносы сдвигают всю оставшуюся программу; MaxPostpones+1-й перенос подряд закрывает тему.
func Current(topics []Topic, start, date string, answers Answers) (Week, bool) {
	if _, err := time.Parse(time.DateOnly, start); err != nil || len(topics) == 0 {
		return Week{}, false
	}
	first := monday(start)
	target := monday(date)
	if target.Before(first) {
		return Week{}, false
	}
	idx, streak := 0, 0
	for m := first; m.Before(target); m = m.AddDate(0, 0, 7) {
		done, answered := answers[m.Format(time.DateOnly)]
		if answered && !done && streak < MaxPostpones {
			streak++
			continue
		}
		idx++
		streak = 0
		if idx >= len(topics) {
			return Week{}, false
		}
	}
	return Week{Index: idx, Total: len(topics), Topic: topics[idx], Repeat: streak > 0, Postpones: streak}, true
}
