// Package command — изменения через чат: Claude переводит просьбу в действия, код их
// проверяет, показывает «было → стало» и применяет только после «Да». Чистые функции.
package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/money"
)

// Marker — с этой строки в ответе Claude начинаются действия.
const Marker = "ДЕЙСТВИЯ:"

// Виды действий.
const (
	HabitAdd    = "habit_add"
	HabitEdit   = "habit_edit"
	HabitPause  = "habit_pause"
	HabitResume = "habit_resume"
	HabitQueue  = "habit_queue"
	HabitDelete = "habit_delete"
	Settings    = "settings"
	Mark        = "mark"
	Workout     = "workout"
	MoneyUntil  = "money_transfers"
	MoneyGoal   = "money_goal"
	MoneyBudget = "money_budget"
	Remember    = "remember"
	Forget      = "forget"
)

// MaxActions — больше за раз не принимается: просьба, скорее всего, понята неверно.
const MaxActions = 8

// Action — одно действие. Поля-указатели: nil — не менять.
type Action struct {
	Op string `json:"op"`

	// привычки
	ID      string  `json:"id,omitempty"`
	Emoji   *string `json:"emoji,omitempty"`
	Title   *string `json:"title,omitempty"`
	Anchor  *string `json:"anchor,omitempty"`
	Goal    *string `json:"goal,omitempty"` // цель привычки (поле action в habits.json)
	Floor   *string `json:"floor,omitempty"`
	Kind    string  `json:"kind,omitempty"`
	PerWeek *int    `json:"per_week,omitempty"`
	Queued  *bool   `json:"queued,omitempty"`
	Since   string  `json:"since,omitempty"`

	// настройки
	WakeAnchor *string `json:"wake_anchor,omitempty"`
	Tolerance  *int    `json:"tolerance,omitempty"`
	StepsFloor *int    `json:"steps_floor,omitempty"`
	StepsGoal  *int    `json:"steps_goal,omitempty"`
	EveningAt  *string `json:"evening_at,omitempty"`
	WeeklyAt   *string `json:"weekly_at,omitempty"`

	// отметки и тренировки
	Date  string            `json:"date,omitempty"`
	Marks map[string]string `json:"marks,omitempty"` // id привычки → floor | goal | miss

	// деньги
	To           string `json:"to,omitempty"`
	Until        string `json:"until,omitempty"`
	Target       *int64 `json:"target,omitempty"`
	By           string `json:"by,omitempty"`
	On           string `json:"on,omitempty"`
	LivePerMonth *int64 `json:"live_per_month,omitempty"`

	// заметки
	Text string `json:"text,omitempty"`
	Note int    `json:"note,omitempty"`
}

// Split отделяет текст ответа от действий. Нет маркера — действий нет.
func Split(raw string) (reply string, actions []Action, err error) {
	i := strings.LastIndex(raw, Marker)
	if i < 0 {
		return strings.TrimSpace(raw), nil, nil
	}
	reply = strings.TrimSpace(raw[:i])
	body := strings.TrimSpace(raw[i+len(Marker):])
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
	if start, end := strings.Index(body, "["), strings.LastIndex(body, "]"); start >= 0 && end > start {
		body = body[start : end+1]
	}
	actions, err = Decode(body)
	return reply, actions, err
}

// Decode читает список действий строго: неизвестные поля — ошибка.
func Decode(body string) ([]Action, error) {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var actions []Action
	if err := dec.Decode(&actions); err != nil {
		return nil, fmt.Errorf("действия не разобраны: %w", err)
	}
	if len(actions) > MaxActions {
		return nil, fmt.Errorf("слишком много изменений за раз (%d)", len(actions))
	}
	return actions, nil
}

// Encode — действия для хранения до подтверждения.
func Encode(actions []Action) string {
	raw, _ := json.Marshal(actions)
	return string(raw)
}

// NeedsConfirm — нужно ли подтверждение: заметки применяются сразу.
func NeedsConfirm(actions []Action) bool {
	for _, a := range actions {
		if a.Op != Remember && a.Op != Forget {
			return true
		}
	}
	return false
}

// State — что меняют действия.
type State struct {
	Today    string
	Habits   habits.File
	Settings config.Settings
	Money    *money.File // nil — плана денег нет
	Notes    map[int]string
	Floor    func(date string) bool // окно отметки ещё открыто
}

// Plan — проверенный итог: новое состояние и что произойдёт словами.
type Plan struct {
	Habits       habits.File
	HabitsChange bool
	Settings     config.Settings
	Money        *money.File
	MoneyChange  bool
	Marks        map[string]map[string]int // дата → id → уровень
	Workouts     []string                  // даты ручных тренировок
	Remember     []string
	Forget       []int
	Lines        []string
}

var idRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func q(s string) string { return "«" + s + "»" }

func name(h habits.Habit) string {
	if h.Emoji != "" {
		return h.Emoji + " " + h.Title
	}
	return h.Title
}

func levelOf(s string) (int, bool) {
	switch s {
	case "miss":
		return 0, true
	case "floor":
		return 1, true
	case "goal":
		return 2, true
	}
	return 0, false
}

var levelWord = map[int]string{0: "мимо", 1: "пол", 2: "цель"}

// Build применяет действия к копии состояния и проверяет результат целиком.
func Build(s State, actions []Action) (Plan, error) {
	p := Plan{
		Habits:   habits.File{Habits: append([]habits.Habit(nil), s.Habits.Habits...)},
		Settings: s.Settings,
		Marks:    map[string]map[string]int{},
	}
	if s.Money != nil {
		m := *s.Money
		m.Transfers = append([]money.Transfer(nil), m.Transfers...)
		m.Goals = append([]money.Goal(nil), m.Goals...)
		p.Money = &m
	}
	if len(actions) == 0 {
		return p, errors.New("нет действий")
	}
	var errs []error
	for _, a := range actions {
		if err := p.apply(s, a); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return p, err
	}
	if p.HabitsChange {
		if err := p.Habits.Validate(); err != nil {
			return p, err
		}
	}
	if p.Settings != s.Settings {
		var c config.Config
		if err := p.Settings.Apply(&c); err != nil {
			return p, err
		}
	}
	if p.MoneyChange {
		if err := p.Money.Validate(); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (p *Plan) find(id string) (int, error) {
	for i, h := range p.Habits.Habits {
		if h.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("нет привычки %q", id)
}

func (p *Plan) line(format string, args ...any) { p.Lines = append(p.Lines, fmt.Sprintf(format, args...)) }

func (p *Plan) apply(s State, a Action) error {
	switch a.Op {
	case HabitAdd:
		return p.habitAdd(s, a)
	case HabitEdit:
		return p.habitEdit(a)
	case HabitPause, HabitResume, HabitQueue, HabitDelete:
		return p.habitState(s, a)
	case Settings:
		return p.settings(a)
	case Mark:
		return p.mark(s, a)
	case Workout:
		date := a.Date
		if date == "" {
			date = s.Today
		}
		if !validDate(date) || date > s.Today {
			return fmt.Errorf("тренировка: плохая дата %q", date)
		}
		p.Workouts = append(p.Workouts, date)
		p.line("🏋️ Тренировка %s засчитана вручную", date)
	case MoneyUntil, MoneyGoal, MoneyBudget:
		return p.money(a)
	case Remember:
		text := strings.TrimSpace(a.Text)
		if text == "" || len([]rune(text)) > 500 {
			return errors.New("заметка: пустая или длиннее 500 символов")
		}
		p.Remember = append(p.Remember, text)
		p.line("📝 Запомнить: %s", text)
	case Forget:
		text, ok := s.Notes[a.Note]
		if !ok {
			return fmt.Errorf("нет заметки #%d", a.Note)
		}
		p.Forget = append(p.Forget, a.Note)
		p.line("🗑 Забыть: %s", text)
	default:
		return fmt.Errorf("неизвестное действие %q", a.Op)
	}
	return nil
}

func str(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func (p *Plan) habitAdd(s State, a Action) error {
	h := habits.Habit{
		ID: a.ID, Emoji: str(a.Emoji), Title: str(a.Title), Anchor: str(a.Anchor),
		Action: str(a.Goal), Floor: str(a.Floor), Since: a.Since, Kind: a.Kind,
	}
	if a.PerWeek != nil {
		h.PerWeek = *a.PerWeek
	}
	if a.Queued != nil {
		h.Queued = *a.Queued
	}
	if h.Since == "" {
		h.Since = s.Today
	}
	if !validDate(h.Since) {
		return fmt.Errorf("новая привычка: плохая дата %q", h.Since)
	}
	if h.ID == "" || !idRe.MatchString(h.ID) {
		h.ID = fmt.Sprintf("h%d", len(p.Habits.Habits)+1)
	}
	for base, n := h.ID, 2; ; n++ {
		if _, err := p.find(h.ID); err != nil {
			break
		}
		h.ID = fmt.Sprintf("%s%d", base, n)
	}
	p.Habits.Habits = append(p.Habits.Habits, h)
	p.HabitsChange = true
	extra := ""
	switch {
	case h.Queued:
		extra = " · в очереди"
	case h.Since > s.Today:
		extra = " · с " + h.Since
	}
	if h.Kind == habits.KindWorkout {
		extra += fmt.Sprintf(" · %d раз в неделю", h.PerWeek)
	}
	p.line("➕ Новая привычка %s: когда %s → %s · пол: %s%s", name(h), h.Anchor, h.Action, h.Floor, extra)
	return nil
}

func (p *Plan) habitEdit(a Action) error {
	i, err := p.find(a.ID)
	if err != nil {
		return err
	}
	h := &p.Habits.Habits[i]
	was := name(*h)
	set := func(field string, dst *string, v *string) {
		if v == nil || str(v) == *dst {
			return
		}
		p.line("✏️ %s — %s: %s → %s", was, field, q(*dst), q(str(v)))
		*dst = str(v)
		p.HabitsChange = true
	}
	set("название", &h.Title, a.Title)
	set("эмодзи", &h.Emoji, a.Emoji)
	set("якорь", &h.Anchor, a.Anchor)
	set("цель", &h.Action, a.Goal)
	set("пол", &h.Floor, a.Floor)
	if a.PerWeek != nil && *a.PerWeek != h.PerWeek {
		p.line("✏️ %s — раз в неделю: %d → %d", was, h.PerWeek, *a.PerWeek)
		h.PerWeek = *a.PerWeek
		p.HabitsChange = true
	}
	return nil
}

func (p *Plan) habitState(s State, a Action) error {
	i, err := p.find(a.ID)
	if err != nil {
		return err
	}
	h := &p.Habits.Habits[i]
	p.HabitsChange = true
	switch a.Op {
	case HabitPause:
		h.Paused = true
		p.line("⏸ %s — на паузу", name(*h))
	case HabitResume:
		h.Paused, h.Queued, h.Auto = false, false, false
		if h.Since > s.Today {
			h.Since = s.Today
		}
		p.line("▶️ %s — снова в работе", name(*h))
	case HabitQueue:
		h.Queued, h.Paused = true, false
		p.line("🪜 %s — в очередь", name(*h))
	case HabitDelete:
		p.line("🗑 %s — удалить", name(*h))
		p.Habits.Habits = append(p.Habits.Habits[:i], p.Habits.Habits[i+1:]...)
	}
	return nil
}

func (p *Plan) settings(a Action) error {
	before := p.Settings
	st := &p.Settings
	if v := str(a.WakeAnchor); v != "" {
		st.WakeAnchor = v
	}
	if a.Tolerance != nil {
		st.Tolerance = *a.Tolerance
	}
	if a.StepsFloor != nil {
		st.StepsFloor = *a.StepsFloor
	}
	if a.StepsGoal != nil {
		st.StepsGoal = *a.StepsGoal
	}
	if v := str(a.EveningAt); v != "" {
		st.EveningAt = v
	}
	if v := str(a.WeeklyAt); v != "" {
		st.WeeklyAt = v
	}
	if before.WakeAnchor != st.WakeAnchor {
		p.line("⚙️ Подъём: %s → %s", before.WakeAnchor, st.WakeAnchor)
	}
	if before.Tolerance != st.Tolerance {
		p.line("⚙️ Допуск подъёма: ±%d → ±%d мин", before.Tolerance, st.Tolerance)
	}
	if before.StepsFloor != st.StepsFloor {
		p.line("⚙️ Пол шагов: %d → %d", before.StepsFloor, st.StepsFloor)
	}
	if before.StepsGoal != st.StepsGoal {
		p.line("⚙️ Цель шагов: %d → %d", before.StepsGoal, st.StepsGoal)
	}
	if before.EveningAt != st.EveningAt {
		p.line("⚙️ Вечерний вопрос: %s → %s", before.EveningAt, st.EveningAt)
	}
	if before.WeeklyAt != st.WeeklyAt {
		p.line("⚙️ Итог недели: %s → %s", before.WeeklyAt, st.WeeklyAt)
	}
	return nil
}

func (p *Plan) mark(s State, a Action) error {
	date := a.Date
	if date == "" {
		date = s.Today
	}
	if !validDate(date) || date > s.Today || (s.Floor != nil && !s.Floor(date)) {
		return fmt.Errorf("отметка за %s уже закрыта", date)
	}
	if len(a.Marks) == 0 {
		return errors.New("отметка: не сказано, что отметить")
	}
	daily := s.Habits.Daily(date)
	known := map[string]habits.Habit{}
	for _, h := range daily {
		known[h.ID] = h
	}
	if p.Marks[date] == nil {
		p.Marks[date] = map[string]int{}
	}
	for id, word := range a.Marks {
		level, ok := levelOf(word)
		if !ok {
			return fmt.Errorf("отметка %q: нужно floor, goal или miss", word)
		}
		targets := []habits.Habit{known[id]}
		if id == "*" {
			targets = daily
		} else if _, ok := known[id]; !ok {
			return fmt.Errorf("нет ежедневной привычки %q за %s", id, date)
		}
		for _, h := range targets {
			if _, set := p.Marks[date][h.ID]; set && id == "*" {
				continue // точная отметка важнее «остальное»
			}
			p.Marks[date][h.ID] = level
		}
	}
	for _, h := range daily {
		if level, ok := p.Marks[date][h.ID]; ok {
			p.line("✅ %s · %s — %s", date, name(h), levelWord[level])
		}
	}
	return nil
}

func (p *Plan) money(a Action) error {
	if p.Money == nil {
		return errors.New("плана денег нет")
	}
	m := p.Money
	switch a.Op {
	case MoneyUntil:
		if !validDate(a.Until) {
			return fmt.Errorf("переводы: плохая дата %q", a.Until)
		}
		n := 0
		for i := range m.Transfers {
			if m.Transfers[i].To == a.To {
				m.Transfers[i].Until = a.Until
				n++
			}
		}
		if n == 0 {
			return fmt.Errorf("нет переводов на %q", a.To)
		}
		acc, _ := m.Account(a.To)
		p.line("💰 Переводы на %s — до %s", acc.Name(), a.Until)
	case MoneyGoal:
		i := -1
		for j, g := range m.Goals {
			if g.ID == a.ID {
				i = j
			}
		}
		if i < 0 {
			return fmt.Errorf("нет цели %q", a.ID)
		}
		g := &m.Goals[i]
		if a.Target != nil && *a.Target != g.Target {
			p.line("💰 %s: сумма %d → %d", g.Title, g.Target, *a.Target)
			g.Target = *a.Target
		}
		if a.By != "" && a.By != g.By {
			p.line("💰 %s: срок %s → %s", g.Title, g.By, a.By)
			g.By = a.By
		}
		if a.On != "" && a.On != g.On {
			p.line("💰 %s: дата %s → %s", g.Title, g.On, a.On)
			g.On = a.On
		}
	case MoneyBudget:
		if a.LivePerMonth == nil {
			return errors.New("бюджет: нет суммы")
		}
		p.line("💰 На жизнь в месяц: %d → %d", m.Budget.LivePerMonth, *a.LivePerMonth)
		m.Budget.LivePerMonth = *a.LivePerMonth
	}
	p.MoneyChange = true
	return nil
}
