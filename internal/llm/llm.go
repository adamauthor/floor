// Package llm — обращение к Claude. Основная реализация вызывает `claude -p`
// (подписка через официальный CLI), при любой ошибке используется статический fallback.
// Подробности — docs/SPEC.md, раздел 7.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client — минимальный интерфейс к языковой модели.
type Client interface {
	Ask(ctx context.Context, system, prompt string) (string, error)
}

// ClaudeCLI запускает `claude -p`. Флаги сверены с Claude Code 2.1:
// без инструментов, без настроек и CLAUDE.md пользователя, без сохранения сессии,
// потому что в промпте лежат данные о здоровье.
type ClaudeCLI struct {
	Bin        string
	Timeout    time.Duration
	RetryDelay time.Duration
	Log        *slog.Logger
}

// Ask делает запрос с одним повтором.
func (c *ClaudeCLI) Ask(ctx context.Context, system, prompt string) (string, error) {
	text, err := c.once(ctx, system, prompt)
	if err == nil {
		return text, nil
	}
	c.Log.Warn("llm_retry", "error", err)
	select {
	case <-time.After(c.RetryDelay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return c.once(ctx, system, prompt)
}

func (c *ClaudeCLI) once(ctx context.Context, system, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	// Пустая рабочая папка: Claude не должен видеть файлы проекта.
	dir, err := os.MkdirTemp("", "floor-llm-")
	if err != nil {
		return "", fmt.Errorf("временная папка: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cmd := exec.CommandContext(ctx, c.Bin,
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--tools", "",
		"--setting-sources", "",
		"--strict-mcp-config",
		"--append-system-prompt", system,
	)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("запуск claude: %w (stderr: %d байт)", err, stderr.Len())
	}
	return ParseResult(stdout.Bytes())
}

// ParseResult достаёт текст из ответа `--output-format json`.
func ParseResult(raw []byte) (string, error) {
	var r struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("разбор ответа claude: %w", err)
	}
	if r.IsError {
		return "", errors.New("claude вернул ошибку")
	}
	text := strings.TrimSpace(r.Result)
	if text == "" {
		return "", errors.New("пустой ответ claude")
	}
	return text, nil
}
