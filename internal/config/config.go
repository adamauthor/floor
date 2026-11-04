// Package config читает конфигурацию Floor из переменных окружения.
// Полный список переменных — docs/SPEC.md, раздел 10.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config — вся конфигурация процесса.
type Config struct {
	TelegramToken   string
	TelegramOwnerID int64
	TelegramAPIURL  string // пусто — api.telegram.org; задаётся в тестах

	WakeAnchor       Clock
	WakeToleranceMin int
	StepsFloor       int
	StepsGoal        int
	EveningAt        Clock
	WeeklyAt         Clock
	DefaultTZ        *time.Location

	HabitsFile string

	MoneyFile         string // план денег (SPEC_v2, 2.2)
	MoneyRemindDays   int    // через сколько дней без сверки напомнить
	LearningFile      string // программа финграмотности; нет файла — встроенная
	LearningStart     string // понедельник первой недели программы
	WorkoutMinMinutes int    // минимальная длительность тренировки
	WebAppURL         string // страница настроек (Telegram Mini App)
	WebAppAddr        string // где бот принимает сохранение страницы; пусто — не принимает
	NgrokAPI          string // локальный API ngrok, у которого бот узнаёт публичный адрес

	HealthSource string // "file" | "http" | "google"
	HealthFile   string
	HTTPAddr     string
	IngestToken  string

	GoogleClientID     string
	GoogleClientSecret string
	GoogleTokenFile    string
	GooglePoll         time.Duration

	DBPath            string
	ClaudeBin         string
	LLMTimeout        time.Duration
	LLMMaxCallsPerDay int
	LogLevel          string
}

// Clock — время суток без даты, например 07:00.
type Clock struct{ Hour, Minute int }

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// ParseClock разбирает строку вида "HH:MM".
func ParseClock(s string) (Clock, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return Clock{}, fmt.Errorf("ожидается HH:MM, получено %q", s)
	}
	return Clock{Hour: t.Hour(), Minute: t.Minute()}, nil
}

// Load читает конфигурацию из окружения и проверяет её целиком: для запуска бота.
func Load() (Config, error) {
	c, err := LoadLocal()
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	if c.TelegramToken == "" {
		errs = append(errs, errors.New("TELEGRAM_TOKEN обязателен"))
	}
	if c.TelegramOwnerID == 0 {
		errs = append(errs, errors.New("TELEGRAM_OWNER_ID обязателен"))
	}
	if c.HealthSource == "http" && c.IngestToken == "" {
		errs = append(errs, errors.New("INGEST_TOKEN обязателен при HEALTH_SOURCE=http"))
	}
	return c, errors.Join(errs...)
}

// LoadLocal читает конфигурацию без требований к секретам: для команд вроде debug-day.
func LoadLocal() (Config, error) {
	var errs []error
	r := reader{errs: &errs}

	c := Config{
		TelegramToken:   r.str("TELEGRAM_TOKEN", ""),
		TelegramOwnerID: r.int64("TELEGRAM_OWNER_ID", 0),
		TelegramAPIURL:  r.str("TELEGRAM_API_URL", ""),

		WakeAnchor:       r.clock("WAKE_ANCHOR", "07:00"),
		WakeToleranceMin: r.int("WAKE_TOLERANCE_MIN", 60),
		StepsFloor:       r.int("STEPS_FLOOR", 7000),
		StepsGoal:        r.int("STEPS_GOAL", 10000),
		EveningAt:        r.clock("EVENING_AT", "21:00"),
		WeeklyAt:         r.clock("WEEKLY_AT", "20:00"),
		DefaultTZ:        r.location("DEFAULT_TZ", "Asia/Almaty"),

		HabitsFile: expandHome(r.str("HABITS_FILE", "~/.floor/habits.json")),

		MoneyFile:         expandHome(r.str("MONEY_FILE", "~/.floor/money.json")),
		MoneyRemindDays:   r.int("MONEY_REMIND_DAYS", 14),
		LearningFile:      expandHome(r.str("LEARNING_FILE", "~/.floor/learning.json")),
		LearningStart:     r.str("LEARNING_START", "2026-11-09"),
		WorkoutMinMinutes: r.int("WORKOUT_MIN_MINUTES", 20),
		WebAppURL:         r.str("WEBAPP_URL", "https://adamauthor.github.io/floor/app/"),
		WebAppAddr:        r.str("WEBAPP_ADDR", "127.0.0.1:8791"),
		NgrokAPI:          r.str("NGROK_API", "http://127.0.0.1:4040"),

		HealthSource: r.str("HEALTH_SOURCE", "file"),
		HealthFile:   expandHome(r.str("HEALTH_FILE", "~/Library/Mobile Documents/com~apple~CloudDocs/Floor/health.json")),
		HTTPAddr:     r.str("HTTP_ADDR", ":8080"),
		IngestToken:  r.str("INGEST_TOKEN", ""),

		GoogleClientID:     r.str("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: r.str("GOOGLE_CLIENT_SECRET", ""),
		GoogleTokenFile:    expandHome(r.str("GOOGLE_TOKEN_FILE", "~/.floor/google-token.json")),
		GooglePoll:         r.duration("GOOGLE_POLL", 15*time.Minute),

		DBPath:            expandHome(r.str("DB_PATH", "~/.floor/floor.db")),
		ClaudeBin:         r.str("CLAUDE_BIN", "claude"),
		LLMTimeout:        r.duration("LLM_TIMEOUT", 90*time.Second),
		LLMMaxCallsPerDay: r.int("LLM_MAX_CALLS_PER_DAY", 30),
		LogLevel:          r.str("LOG_LEVEL", "info"),
	}

	switch c.HealthSource {
	case "file", "http":
	case "google":
		if c.GoogleClientID == "" || c.GoogleClientSecret == "" {
			errs = append(errs, errors.New("GOOGLE_CLIENT_ID и GOOGLE_CLIENT_SECRET обязательны при HEALTH_SOURCE=google"))
		}
		if c.GooglePoll < time.Minute {
			errs = append(errs, errors.New("GOOGLE_POLL: не чаще раза в минуту"))
		}
	default:
		errs = append(errs, fmt.Errorf("HEALTH_SOURCE: ожидается file, http или google, получено %q", c.HealthSource))
	}
	if c.StepsFloor > c.StepsGoal {
		errs = append(errs, errors.New("STEPS_FLOOR не может быть больше STEPS_GOAL"))
	}

	return c, errors.Join(errs...)
}

type reader struct{ errs *[]error }

func (r reader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (r reader) int(key string, def int) int {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: ожидается число, получено %q", key, v))
	}
	return n
}

func (r reader) int64(key string, def int64) int64 {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: ожидается число, получено %q", key, v))
	}
	return n
}

func (r reader) clock(key, def string) Clock {
	c, err := ParseClock(r.str(key, def))
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: %w", key, err))
	}
	return c
}

func (r reader) duration(key string, def time.Duration) time.Duration {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: ожидается длительность вида 90s, получено %q", key, v))
	}
	return d
}

func (r reader) location(key, def string) *time.Location {
	name := r.str(key, def)
	loc, err := time.LoadLocation(name)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: неизвестный часовой пояс %q", key, name))
		return time.UTC
	}
	return loc
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}
