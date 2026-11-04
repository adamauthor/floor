// Command floor — персональный бот «пола». Описание — README.md и docs/SPEC.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"floor/internal/bot"
	"floor/internal/config"
	"floor/internal/core"
	"floor/internal/habits"
	"floor/internal/health"
	"floor/internal/llm"
	"floor/internal/render"
	"floor/internal/schedule"
	"floor/internal/store"
	"floor/internal/webapp"
)

var version = "dev"

const usage = `floor — бот «пола»

  floor                    запустить бота
  floor debug-day [дата]   метрики дня из базы (YYYY-MM-DD, по умолчанию сегодня)
  floor habits             привычки и якоря, проверка habits.json
  floor google-auth        войти в Google Health (для HEALTH_SOURCE=google)
  floor version            версия`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dispatch(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "floor:", err)
		os.Exit(1)
	}
}

func dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return run(ctx)
	}
	switch args[0] {
	case "debug-day":
		return debugDay(ctx, args[1:])
	case "habits":
		return showHabits(ctx)
	case "google-auth":
		return googleAuth(ctx)
	case "version":
		fmt.Println(version)
		return nil
	}
	fmt.Println(usage)
	return fmt.Errorf("неизвестная команда %q", args[0])
}

func newLogger(cfg config.Config) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level(cfg.LogLevel)}))
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("конфигурация:\n%w", err)
	}
	log := newLogger(cfg)
	log.Info("start",
		"version", version,
		"health_source", cfg.HealthSource,
		"wake_anchor", cfg.WakeAnchor.String(),
		"default_tz", cfg.DefaultTZ.String(),
	)

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	tg, err := bot.New(cfg.TelegramToken, cfg.TelegramOwnerID, cfg.TelegramAPIURL, log)
	if err != nil {
		return err
	}
	app := &core.App{
		Cfg: cfg, Store: db, TG: tg, Log: log, Now: time.Now,
		LLM: &llm.ClaudeCLI{Bin: cfg.ClaudeBin, Timeout: cfg.LLMTimeout, RetryDelay: 5 * time.Second, Log: log},
	}
	if cfg.WebAppAddr != "" {
		tunnel := webapp.Tunnel{API: cfg.NgrokAPI, Local: cfg.WebAppAddr}
		app.API = tunnel.PublicURL
	}
	if err := app.LoadSettings(ctx); err != nil {
		return err
	}
	go func() {
		if err := tg.Setup(ctx); err != nil {
			log.Warn("telegram_setup_failed", "error", err)
		}
	}()

	src := source(ctx, cfg, log, tg)
	var wg sync.WaitGroup
	start := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervise(ctx, log, name, fn)
		}()
	}
	start("health", func(ctx context.Context) error { return consume(ctx, src, app, log) })
	start("telegram", func(ctx context.Context) error {
		tg.Start(ctx, app)
		return nil
	})
	if cfg.WebAppAddr != "" {
		srv := &webapp.Server{
			Token: cfg.TelegramToken, Owner: cfg.TelegramOwnerID, Origin: webapp.Origin(cfg.WebAppURL),
			Save: app.SaveSettings, Log: log, Now: time.Now,
		}
		start("webapp", func(ctx context.Context) error { return serve(ctx, cfg.WebAppAddr, srv) })
	}
	start("schedule", func(ctx context.Context) error {
		schedule.Run(ctx, time.Minute, app.Tick)
		return nil
	})

	wg.Wait()
	log.Info("stop")
	return nil
}

// serve держит HTTP-сервер до отмены ctx.
func serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func source(ctx context.Context, cfg config.Config, log *slog.Logger, tg core.Messenger) health.Source {
	switch cfg.HealthSource {
	case "http":
		return &health.HTTPSource{Addr: cfg.HTTPAddr, Token: cfg.IngestToken, Log: log}
	case "google":
		src := googleSource(ctx, cfg, log)
		src.Alert = func(ctx context.Context, err error) {
			text := "Не могу забрать шаги и сон из Google Health. Запусти на Маке в папке floor:\nmake google-auth"
			if _, err := tg.Send(ctx, text, nil); err != nil {
				log.Warn("google_alert_failed", "error", err)
			}
		}
		return src
	}
	return &health.FileSource{Path: cfg.HealthFile, Log: log}
}

func googleSource(ctx context.Context, cfg config.Config, log *slog.Logger) *health.GoogleSource {
	conf := health.GoogleOAuth(cfg.GoogleClientID, cfg.GoogleClientSecret)
	return &health.GoogleSource{
		Client:   health.GoogleClient(ctx, conf, cfg.GoogleTokenFile),
		Loc:      cfg.DefaultTZ,
		Interval: cfg.GooglePoll,
		Log:      log,
	}
}

func consume(ctx context.Context, src health.Source, app *core.App, log *slog.Logger) error {
	updates, err := src.Updates(ctx)
	if err != nil {
		return err
	}
	for exp := range updates {
		if err := app.Apply(ctx, exp); err != nil {
			log.Error("export_failed", "error", err)
		}
	}
	return nil
}

// supervise перезапускает горутину после паники или ошибки: сбой одной части
// не должен ронять процесс (docs/SPEC.md, раздел 13).
func supervise(ctx context.Context, log *slog.Logger, name string, fn func(context.Context) error) {
	for ctx.Err() == nil {
		err := safely(ctx, fn)
		if ctx.Err() != nil {
			return
		}
		log.Error("worker_restart", "worker", name, "error", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
	}
}

func safely(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("паника: %v\n%s", r, debug.Stack())
		}
	}()
	if err := fn(ctx); err != nil {
		return err
	}
	return errors.New("завершилась без ошибки")
}

// debugDay печатает метрики дня, предварительно подтянув свежие данные, если они доступны.
func debugDay(ctx context.Context, args []string) error {
	cfg, err := config.LoadLocal()
	if err != nil {
		return fmt.Errorf("конфигурация:\n%w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	app := &core.App{Cfg: cfg, Store: db, Log: log, Now: time.Now}
	if err := app.LoadSettings(ctx); err != nil {
		return err
	}

	switch cfg.HealthSource {
	case "google":
		exp, err := googleSource(ctx, cfg, log).Fetch(ctx)
		if err != nil {
			log.Warn("google_fetch_failed", "error", err)
		} else if err := app.Apply(ctx, exp); err != nil {
			return err
		}
	case "file":
		if raw, err := os.ReadFile(cfg.HealthFile); err == nil {
			exp, err := health.Parse(raw)
			if err != nil {
				log.Warn("export_invalid", "error", err)
			} else if err := app.Apply(ctx, exp); err != nil {
				return err
			}
		}
	}

	date := time.Now().In(cfg.DefaultTZ).Format(time.DateOnly)
	if len(args) > 0 {
		date = args[0]
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return fmt.Errorf("дата должна быть в виде YYYY-MM-DD, получено %q", date)
		}
	}
	text, err := app.DebugDay(ctx, date)
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func showHabits(ctx context.Context) error {
	cfg, err := config.LoadLocal()
	if err != nil {
		return fmt.Errorf("конфигурация:\n%w", err)
	}
	// время подъёма могло поменяться в настройках Telegram
	if db, err := store.Open(ctx, cfg.DBPath); err == nil {
		app := &core.App{Cfg: cfg, Store: db, Log: slog.New(slog.DiscardHandler), Now: time.Now}
		if err := app.LoadSettings(ctx); err == nil {
			cfg = app.Cfg
		}
		db.Close()
	}
	today := time.Now().In(cfg.DefaultTZ).Format(time.DateOnly)
	_, f, err := habits.Load(cfg.HabitsFile, today)
	if err != nil {
		return fmt.Errorf("%s:\n%w", cfg.HabitsFile, err)
	}
	card := render.HabitCard(f, today, cfg.WakeAnchor.String())
	fmt.Println(render.StripTags(card))
	fmt.Println("\nФайл:", cfg.HabitsFile)
	return nil
}

func level(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}
