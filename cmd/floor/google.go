package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"golang.org/x/oauth2"

	"floor/internal/config"
	"floor/internal/health"
)

// googleAuth проводит вход в Google через браузер и сохраняет refresh token.
// Клиент типа «Desktop app» разрешает возврат на 127.0.0.1 с любым портом.
func googleAuth(ctx context.Context) error {
	cfg, err := config.LoadLocal()
	if err != nil {
		return fmt.Errorf("конфигурация:\n%w", err)
	}
	if cfg.GoogleClientID == "" || cfg.GoogleClientSecret == "" {
		return errors.New("заполни GOOGLE_CLIENT_ID и GOOGLE_CLIENT_SECRET в .env (docs/GOOGLE_HEALTH.md)")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	conf := health.GoogleOAuth(cfg.GoogleClientID, cfg.GoogleClientSecret)
	conf.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	state := randomHex()
	verifier := oauth2.GenerateVerifier()
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))

	codes := make(chan string, 1)
	errs := make(chan error, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state не совпал", http.StatusBadRequest)
			return
		case q.Get("error") != "":
			fmt.Fprintln(w, "Вход отменён. Можно закрыть вкладку.")
			errs <- fmt.Errorf("google: %s", q.Get("error"))
			return
		}
		fmt.Fprintln(w, "Готово, можно закрыть вкладку и вернуться в терминал.")
		codes <- q.Get("code")
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Println("Открываю браузер для входа в Google. Если не открылся, перейди по ссылке:")
	fmt.Println(authURL)
	_ = exec.Command("open", authURL).Start()

	var code string
	select {
	case code = <-codes:
	case err := <-errs:
		return err
	case <-time.After(5 * time.Minute):
		return errors.New("за 5 минут вход не завершён")
	case <-ctx.Done():
		return ctx.Err()
	}

	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("обмен кода на токен: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("нет refresh token от Google: отзови доступ на myaccount.google.com/permissions и повтори")
	}
	if err := health.SaveToken(cfg.GoogleTokenFile, tok); err != nil {
		return err
	}
	fmt.Println("Токен сохранён:", cfg.GoogleTokenFile)

	// Сразу проверяем, что данные действительно читаются.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	exp, err := googleSource(ctx, cfg, log).Fetch(ctx)
	if err != nil {
		return fmt.Errorf("вход прошёл, но данные не читаются: %w", err)
	}
	fmt.Printf("Проверка: дней с шагами — %d, сегментов сна — %d.\n", len(exp.Steps), len(exp.Sleep))
	for _, s := range exp.Steps {
		fmt.Printf("  %s  %d шагов\n", s.Date, s.Count)
	}
	return nil
}

func randomHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
