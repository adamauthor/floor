package llm

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseResult(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"успех", `{"type":"result","is_error":false,"result":" Привет "}`, "Привет", false},
		{"ошибка", `{"is_error":true,"result":"limit"}`, "", true},
		{"пусто", `{"result":"  "}`, "", true},
		{"не JSON", `oops`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResult([]byte(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %q, err %v", got, err)
			}
		})
	}
}

func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func cli(bin string) *ClaudeCLI {
	return &ClaudeCLI{Bin: bin, Timeout: 5 * time.Second, RetryDelay: time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestClaudeCLIPassesPromptAndFlags(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen")
	bin := fakeClaude(t, `pwd > "`+out+`.cwd"; echo "$@" > "`+out+`.args"; cat > "`+out+`.stdin"; echo '{"result":"ответ"}'`)
	got, err := cli(bin).Ask(context.Background(), "СИСТЕМА", "ПРОМПТ")
	if err != nil || got != "ответ" {
		t.Fatalf("got %q, err %v", got, err)
	}
	args, _ := os.ReadFile(out + ".args")
	for _, want := range []string{"-p", "--output-format json", "--no-session-persistence", "--tools", "--setting-sources", "--strict-mcp-config", "--append-system-prompt СИСТЕМА"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("нет %q в %s", want, args)
		}
	}
	if stdin, _ := os.ReadFile(out + ".stdin"); string(stdin) != "ПРОМПТ" {
		t.Errorf("stdin = %q", stdin)
	}
	if cwd, _ := os.ReadFile(out + ".cwd"); !strings.Contains(string(cwd), "floor-llm-") {
		t.Errorf("запуск не в пустой папке: %s", cwd)
	}
}

func TestClaudeCLIRetriesOnce(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	bin := fakeClaude(t, `if [ ! -f "`+marker+`" ]; then touch "`+marker+`"; exit 1; fi; echo '{"result":"со второго раза"}'`)
	got, err := cli(bin).Ask(context.Background(), "s", "p")
	if err != nil || got != "со второго раза" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestClaudeCLIFailsAfterRetry(t *testing.T) {
	if _, err := cli(fakeClaude(t, "exit 1")).Ask(context.Background(), "s", "p"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if _, err := cli("/нет/такого/бинаря").Ask(context.Background(), "s", "p"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

func TestClaudeCLITimeout(t *testing.T) {
	c := cli(fakeClaude(t, "exec sleep 30"))
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := c.Ask(context.Background(), "s", "p"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("таймаут не сработал: %v", time.Since(start))
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in    string
		limit int
		want  string
	}{
		{"коротко", 100, "коротко"},
		{"Раз. Два. Три четыре пять", 12, "Раз. Два."},
		{"слово другое третье", 13, "слово другое"},
		{"оченьдлинноеслово", 5, "очень"},
	}
	for _, tt := range tests {
		if got := Truncate(tt.in, tt.limit); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, ожидалось %q", tt.in, tt.limit, got, tt.want)
		}
	}
}

func TestCleanStep(t *testing.T) {
	long := "Шаг на сегодня: когда закрою ноутбук → " + strings.Repeat("иду дальше ", 30)
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"как просили", "Шаг на сегодня: когда встану → зубы.", "Шаг на сегодня: когда встану → зубы.", true},
		{"с разметкой и вступлением", "Вот шаг:\n\n**Шаг на сегодня: когда встану → зубы.**", "Шаг на сегодня: когда встану → зубы.", true},
		{"без префикса", "когда встану → зубы", "Шаг на сегодня: когда встану → зубы", true},
		{"пусто", " \n ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CleanStep(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("got %q %v", got, ok)
			}
		})
	}
	got, ok := CleanStep(long)
	if !ok || utf8.RuneCountInString(got) > MaxStepRunes {
		t.Errorf("длина = %d", utf8.RuneCountInString(got))
	}
}

func TestCleanWeekly(t *testing.T) {
	good := "Получилось: а\nУзкое место: б\nНа следующую неделю: в"
	if got, ok := CleanWeekly("Итог:\n**Получилось: а**\n\nУзкое место: б\nНа следующую неделю: в\nПока!"); !ok || got != good {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := CleanWeekly("Получилось: а\nУзкое место: б"); ok {
		t.Error("две строки не должны проходить")
	}
}

func TestFallbackStep(t *testing.T) {
	a := FallbackStep("2026-10-31")
	if a != FallbackStep("2026-10-31") {
		t.Error("выбор должен быть детерминированным")
	}
	if !strings.HasPrefix(a, stepPrefix+" когда ") || !strings.Contains(a, "→") {
		t.Errorf("форма шага: %q", a)
	}
	seen := make(map[string]bool)
	for d := 1; d <= 28; d++ {
		seen[FallbackStep("2026-11-"+string(rune('0'+d/10))+string(rune('0'+d%10)))] = true
	}
	if len(seen) < 5 {
		t.Errorf("слишком мало разных шагов: %d", len(seen))
	}
	for _, s := range fallbackSteps {
		if utf8.RuneCountInString(stepPrefix+" "+s) > MaxStepRunes {
			t.Errorf("шаг длиннее лимита: %q", s)
		}
	}
}

func TestPrompts(t *testing.T) {
	m := MorningPrompt("ТАБЛИЦА", "ЯКОРЯ")
	w := WeeklyPrompt("ТАБЛИЦА", "ЯКОРЯ")
	f := FreePrompt("ТАБЛИЦА", "ЯКОРЯ", []Turn{{"user", "вопрос"}, {"assistant", "ответ"}})
	for _, p := range []string{m, w, f} {
		if !strings.Contains(p, "ТАБЛИЦА") || !strings.Contains(p, "ЯКОРЯ") {
			t.Errorf("нет контекста: %s", p)
		}
	}
	if !strings.Contains(f, "Адам: вопрос") || !strings.Contains(f, "Floor: ответ") {
		t.Errorf("нет диалога: %s", f)
	}
}
