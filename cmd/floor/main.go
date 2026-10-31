// Command floor — персональный бот «пола». Описание — README.md и docs/SPEC.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"floor/internal/config"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "floor:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("конфигурация:\n%w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level(cfg.LogLevel)}))
	log.Info("start",
		"version", version,
		"health_source", cfg.HealthSource,
		"wake_anchor", cfg.WakeAnchor.String(),
		"default_tz", cfg.DefaultTZ.String(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// TODO(M1–M3): собрать store, health.Source, scheduler, bot, llm и запустить их здесь.
	log.Info("каркас v1: компоненты ещё не подключены, см. docs/SPEC.md раздел 15")

	<-ctx.Done()
	log.Info("stop")
	return nil
}

func level(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}
