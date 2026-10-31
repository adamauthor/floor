// Package llm — обращение к Claude. Основная реализация вызывает `claude -p`
// (подписка через официальный CLI), при любой ошибке используется статический fallback.
// Подробности — docs/SPEC.md, раздел 7.
package llm

import "context"

// Client — минимальный интерфейс к языковой модели.
// TODO(M3): реализовать ClaudeCLI и Fallback.
type Client interface {
	Ask(ctx context.Context, system, prompt string) (string, error)
}
