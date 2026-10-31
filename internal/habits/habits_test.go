package habits

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const today = "2026-10-31"

func TestDefaultsValid(t *testing.T) {
	f := Defaults(today)
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.FloorLabels(), " · ")
	if got != "зубы · выйти на улицу · одна вещь на место" {
		t.Errorf("пол дня = %q", got)
	}
}

func TestParseRejects(t *testing.T) {
	ok := `{"id":"a","title":"А","anchor":"когда","action":"что","since":"2026-10-31"}`
	tests := []struct{ name, in, want string }{
		{"не JSON", `{`, "разбор JSON"},
		{"опечатка в поле", `{"habits":[{"id":"a","title":"А","anhor":"x","action":"y","since":"2026-10-31"}]}`, "разбор JSON"},
		{"нет якоря", `{"habits":[{"id":"a","title":"А","action":"что","since":"2026-10-31"}]}`, "без якоря"},
		{"нет действия", `{"habits":[{"id":"a","title":"А","anchor":"x","since":"2026-10-31"}]}`, "нет action"},
		{"нет названия", `{"habits":[{"id":"a","anchor":"x","action":"y","since":"2026-10-31"}]}`, "нет title"},
		{"нет id", `{"habits":[{"title":"А","anchor":"x","action":"y","since":"2026-10-31"}]}`, "нет id"},
		{"плохая дата", `{"habits":[{"id":"a","title":"А","anchor":"x","action":"y","since":"вчера"}]}`, "since"},
		{"повтор id", `{"habits":[` + ok + `,` + ok + `]}`, "повторяется"},
		{"пусто", `{"habits":[]}`, "ни одной"},
		{"слишком много", `{"habits":[` + many(MaxActive+1) + `]}`, "предел"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, ожидалось %q", err, tt.want)
			}
		})
	}
}

func many(n int) string {
	var parts []string
	for i := range n {
		parts = append(parts, `{"id":"h`+string(rune('a'+i))+`","title":"А","anchor":"x","action":"y","since":"2026-10-31"}`)
	}
	return strings.Join(parts, ",")
}

func TestPausedDoesNotCount(t *testing.T) {
	raw := `{"habits":[` + many(MaxActive) + `,{"id":"p","title":"П","anchor":"x","action":"y","since":"2026-10-31","paused":true,"floor_label":"п"}]}`
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Active()) != MaxActive || len(f.FloorLabels()) != 0 {
		t.Errorf("active = %d, labels = %v", len(f.Active()), f.FloorLabels())
	}
}

func TestLoadCreatesDefaultsThenReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "habits.json")
	_, f, err := Load(path, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Habits) != 4 || f.Habits[0].Since != today {
		t.Errorf("f = %+v", f)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("права = %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "закрыл ноутбук") {
		t.Errorf("русский текст должен остаться читаемым: %s", raw)
	}

	// Позже файл не перетирается и since не сдвигается.
	_, f2, err := Load(path, "2026-12-01")
	if err != nil {
		t.Fatal(err)
	}
	if f2.Habits[0].Since != today {
		t.Errorf("since сдвинулся: %s", f2.Habits[0].Since)
	}
}

func TestLoadBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "habits.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path, today); err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

func TestWeek(t *testing.T) {
	h := Habit{Since: "2026-10-31"}
	tests := map[string]int{
		"2026-10-31": 1, "2026-11-06": 1, "2026-11-07": 2, "2027-01-09": 11, "2026-10-01": 1, "мусор": 1,
	}
	for date, want := range tests {
		if got := h.Week(date); got != want {
			t.Errorf("Week(%s) = %d, ожидалось %d", date, got, want)
		}
	}
}

func TestAnchorText(t *testing.T) {
	h := Habit{Anchor: "звонит будильник в {wake}"}
	if got := h.AnchorText("06:30"); got != "звонит будильник в 06:30" {
		t.Errorf("got %q", got)
	}
}
