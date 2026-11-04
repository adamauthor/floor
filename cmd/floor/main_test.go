package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"floor/internal/store"
	"floor/internal/tgtest"
)

const owner = int64(42)

// TestEndToEnd запускает весь процесс: настоящая библиотека Telegram против поддельного
// сервера, настоящий SQLite, файл выгрузки и подмена бинаря claude скриптом.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	srv := tgtest.New(t)

	claude := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >> \"" + dir + "/prompts\"\necho '{\"result\":\"Ответ Claude.\"}'\n"
	if err := os.WriteFile(claude, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	healthFile := filepath.Join(dir, "health.json")
	raw, err := os.ReadFile("../../internal/health/testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(healthFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "floor.db")

	t.Setenv("TELEGRAM_TOKEN", "1:TEST")
	t.Setenv("TELEGRAM_OWNER_ID", strconv.FormatInt(owner, 10))
	t.Setenv("TELEGRAM_API_URL", srv.URL)
	t.Setenv("HEALTH_SOURCE", "file")
	t.Setenv("HEALTH_FILE", healthFile)
	t.Setenv("HABITS_FILE", filepath.Join(dir, "habits.json"))
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("CLAUDE_BIN", claude)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("WEBAPP_ADDR", "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	// Первый тик закрепляет карточку привычек.
	pins := srv.WaitCalls(t, "pinChatMessage", 1)
	if pins[0].Params["disable_notification"] != "true" {
		t.Errorf("pin: %v", pins[0].Params)
	}
	var card string
	for _, c := range srv.Calls("sendMessage") {
		if strings.Contains(c.Params["text"], "Привычки и якоря") {
			card = c.Params["text"]
		}
	}
	if !strings.Contains(card, "когда закрыл ноутбук после работы → куртка и 20 минут на улице") {
		t.Errorf("карточка: %q", card)
	}

	// Свободный текст доходит до claude и обратно.
	srv.PushText(owner, "какие у меня якоря?")
	srv.WaitText(t, "Ответ Claude.")
	prompts, err := os.ReadFile(filepath.Join(dir, "prompts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompts), "какие у меня якоря?") || !strings.Contains(string(prompts), "когда вернулся с улицы") {
		t.Errorf("промпты: %s", prompts)
	}

	// Голосовое и чужой.
	srv.PushText(owner+1, "чужой")
	srv.PushVoice(owner)
	srv.WaitText(t, "Пока только текст.")

	// Кнопка: сегодняшняя дата по поясу из выгрузки.
	today := time.Now().In(time.FixedZone("", 5*3600)).Format(time.DateOnly)
	srv.PushCallback(owner, 500, "floor:"+today+":yes")
	srv.WaitCalls(t, "answerCallbackQuery", 1)
	edits := srv.WaitCalls(t, "editMessageText", 1)
	if edits[0].Params["text"] != "Засчитано." || edits[0].Params["message_id"] != "500" {
		t.Errorf("правка: %v", edits[0].Params)
	}

	// Выгрузка пишется своим воркером: дождаться её, прежде чем гасить процесс.
	waitDB(t, dbPath, "2026-10-31")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("процесс не завершился по сигналу")
	}

	// Данные выгрузки и отметка лежат в базе и переживают закрытие.
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d, err := db.GetDay(context.Background(), "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if d.WakeAt == nil || d.WakeAt.Format("15:04") != "07:24" {
		t.Errorf("день из выгрузки: %+v", d)
	}
	marked, _ := db.GetDay(context.Background(), today)
	if marked.FloorStatus != "yes" {
		t.Errorf("отметка: %+v", marked)
	}
}

func waitDB(t *testing.T, path, date string) {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if d, err := db.GetDay(context.Background(), date); err == nil && d.WakeAt != nil {
			return
		}
	}
	t.Fatal("выгрузка не записалась")
}

func TestDispatchCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HABITS_FILE", filepath.Join(dir, "habits.json"))
	t.Setenv("DB_PATH", filepath.Join(dir, "floor.db"))
	t.Setenv("HEALTH_FILE", "../../internal/health/testdata/export.json")
	t.Setenv("HEALTH_SOURCE", "file")
	ctx := context.Background()

	out := capture(t, func() {
		if err := dispatch(ctx, []string{"debug-day", "2026-10-31"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "подъём       07:24") || !strings.Contains(out, "шаги         1203") {
		t.Errorf("debug-day:\n%s", out)
	}

	out = capture(t, func() {
		if err := dispatch(ctx, []string{"habits"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "Привычки и якоря") || strings.Contains(out, "<b>") {
		t.Errorf("habits:\n%s", out)
	}

	if err := dispatch(ctx, []string{"debug-day", "вчера"}); err == nil {
		t.Error("плохая дата должна давать ошибку")
	}
	if err := os.WriteFile(filepath.Join(dir, "habits.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(ctx, []string{"habits"}); err == nil {
		t.Error("битый файл должен давать ошибку")
	}
	capture(t, func() {
		if err := dispatch(ctx, []string{"что-то"}); err == nil {
			t.Error("неизвестная команда должна давать ошибку")
		}
		if err := dispatch(ctx, []string{"version"}); err != nil {
			t.Error(err)
		}
	})
}

func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestSuperviseRestartsAfterPanic(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervise(ctx, log, "тест", func(ctx context.Context) error {
			if runs.Add(1) == 1 {
				panic("бум")
			}
			cancel()
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("нет перезапуска")
	}
	if runs.Load() != 2 {
		t.Errorf("запусков: %d", runs.Load())
	}
}

func TestSafelyWrapsReturn(t *testing.T) {
	boom := errors.New("ошибка")
	if err := safely(context.Background(), func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if err := safely(context.Background(), func(context.Context) error { return nil }); err == nil {
		t.Error("завершение воркера без ошибки тоже требует перезапуска")
	}
}

func TestLevel(t *testing.T) {
	if level("debug") != slog.LevelDebug || level("мусор") != slog.LevelInfo {
		t.Error("уровень логов")
	}
}
