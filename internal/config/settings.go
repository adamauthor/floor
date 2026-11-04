package config

import (
	"errors"
	"fmt"
)

// Settings — то, что меняется из настроек в Telegram. Сохранённые настройки
// перекрывают значения из окружения.
type Settings struct {
	WakeAnchor string `json:"wake"`
	Tolerance  int    `json:"tol"`
	StepsFloor int    `json:"floor"`
	StepsGoal  int    `json:"goal"`
	EveningAt  string `json:"evening"`
	WeeklyAt   string `json:"weekly"`
}

// Settings возвращает текущие значения конфигурации.
func (c Config) Settings() Settings {
	return Settings{
		WakeAnchor: c.WakeAnchor.String(),
		Tolerance:  c.WakeToleranceMin,
		StepsFloor: c.StepsFloor,
		StepsGoal:  c.StepsGoal,
		EveningAt:  c.EveningAt.String(),
		WeeklyAt:   c.WeeklyAt.String(),
	}
}

// Apply проверяет настройки и переносит их в конфигурацию. При ошибке c не меняется.
func (s Settings) Apply(c *Config) error {
	var errs []error
	clock := func(name, v string) Clock {
		cl, err := ParseClock(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		return cl
	}
	wake := clock("подъём", s.WakeAnchor)
	evening := clock("вечерний вопрос", s.EveningAt)
	weekly := clock("итог недели", s.WeeklyAt)
	if s.Tolerance < 15 || s.Tolerance > 180 {
		errs = append(errs, errors.New("допуск подъёма: от 15 до 180 минут"))
	}
	if s.StepsFloor < 500 || s.StepsGoal > 50000 {
		errs = append(errs, errors.New("шаги: пол от 500, цель до 50 000"))
	}
	if s.StepsFloor > s.StepsGoal {
		errs = append(errs, errors.New("пол шагов не может быть больше цели"))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	c.WakeAnchor, c.WakeToleranceMin = wake, s.Tolerance
	c.StepsFloor, c.StepsGoal = s.StepsFloor, s.StepsGoal
	c.EveningAt, c.WeeklyAt = evening, weekly
	return nil
}
