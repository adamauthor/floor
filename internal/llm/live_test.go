package llm

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveClaude ходит в настоящий `claude -p` и тратит подписку, поэтому включается вручную:
// FLOOR_LIVE_CLAUDE=1 go test -run TestLiveClaude -v ./internal/llm
func TestLiveClaude(t *testing.T) {
	if os.Getenv("FLOOR_LIVE_CLAUDE") == "" {
		t.Skip("нужен FLOOR_LIVE_CLAUDE=1")
	}
	c := &ClaudeCLI{Bin: "claude", Timeout: 120 * time.Second, RetryDelay: time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	table := "дата | подъём | Δ мин | сон | шаги | пол\n" +
		"2026-10-29 | 07:40 | +40 | 6:50 | 8100 | yes\n" +
		"2026-10-30 | 09:10 | +130 | 5:20 | 3900 | no\n" +
		"2026-10-31 | 07:24 | +24 | 7:21 | 1203 | none\n"
	habits := "Подъём | когда звонит будильник в 07:00 → сразу встаю, ноги на пол | неделя 1\n" +
		"Улица | когда закрыл ноутбук после работы → куртка и 20 минут на улице | неделя 1\n"
	ctx := context.Background()

	raw, err := c.Ask(ctx, System, MorningPrompt(table, habits))
	if err != nil {
		t.Fatal(err)
	}
	step, ok := CleanStep(raw)
	t.Logf("шаг: %s", step)
	if !ok || !strings.HasPrefix(step, stepPrefix) || !strings.Contains(step, "→") {
		t.Errorf("шаг не в нужной форме: %q", raw)
	}

	raw, err = c.Ask(ctx, System, WeeklyPrompt(table, habits))
	if err != nil {
		t.Fatal(err)
	}
	week, ok := CleanWeekly(raw)
	t.Logf("неделя:\n%s", week)
	if !ok {
		t.Errorf("неделя не в нужной форме: %q", raw)
	}

	raw, err = c.Ask(ctx, System, FreePrompt(table, habits, []Turn{{"user", "какие у меня сейчас привычки и якоря?"}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ответ:\n%s", raw)
	if !strings.Contains(raw, "ноутбук") {
		t.Errorf("ответ не использует контекст привычек: %q", raw)
	}
}
