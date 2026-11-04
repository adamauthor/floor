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
	for _, h := range f.Habits {
		if h.Floor == "" || h.Action == "" {
			t.Errorf("%s: у привычки должны быть цель и пол", h.ID)
		}
	}
}

func TestParseRejects(t *testing.T) {
	ok := `{"id":"a","title":"А","anchor":"когда","action":"что","floor":"п","since":"2026-10-31"}`
	tests := []struct{ name, in, want string }{
		{"не JSON", `{`, "разбор JSON"},
		{"опечатка в поле", `{"habits":[{"id":"a","title":"А","anhor":"x","action":"y","floor":"п","since":"2026-10-31"}]}`, "разбор JSON"},
		{"нет якоря", `{"habits":[{"id":"a","title":"А","action":"что","floor":"п","since":"2026-10-31"}]}`, "без якоря"},
		{"нет действия", `{"habits":[{"id":"a","title":"А","anchor":"x","floor":"п","since":"2026-10-31"}]}`, "нет action"},
		{"нет пола", `{"habits":[{"id":"a","title":"А","anchor":"x","action":"y","since":"2026-10-31"}]}`, "нет floor"},
		{"нет названия", `{"habits":[{"id":"a","anchor":"x","action":"y","floor":"п","since":"2026-10-31"}]}`, "нет title"},
		{"нет id", `{"habits":[{"title":"А","anchor":"x","action":"y","floor":"п","since":"2026-10-31"}]}`, "нет id"},
		{"плохая дата", `{"habits":[{"id":"a","title":"А","anchor":"x","action":"y","floor":"п","since":"вчера"}]}`, "since"},
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
		parts = append(parts, `{"id":"h`+string(rune('a'+i))+`","title":"А","anchor":"x","action":"y","floor":"п","since":"2026-10-31"}`)
	}
	return strings.Join(parts, ",")
}

func TestPausedDoesNotCount(t *testing.T) {
	raw := `{"habits":[` + many(MaxActive) + `,{"id":"p","title":"П","anchor":"x","action":"y","floor":"п","since":"2026-10-31","paused":true},` +
		`{"id":"q","title":"Q","anchor":"x","action":"y","floor":"п","since":"2026-10-31","auto":true}]}`
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Active()) != MaxActive || len(f.Automatic()) != 1 || f.Automatic()[0].ID != "q" {
		t.Errorf("active = %d, auto = %v", len(f.Active()), f.Automatic())
	}
}

func TestOldFloorLabelMigrates(t *testing.T) {
	f, err := Parse([]byte(`{"habits":[{"id":"a","title":"А","anchor":"x","action":"y","floor_label":"минимум","since":"2026-10-31"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := Encode(f)
	if f.Habits[0].Floor != "минимум" || strings.Contains(string(raw), "floor_label") {
		t.Errorf("floor = %q, файл: %s", f.Habits[0].Floor, raw)
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

func TestSaveAtomicAndValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "habits.json")
	f := Defaults("2026-11-04")
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	_, back, err := Load(path, "2026-11-04")
	if err != nil || len(back.Habits) != 4 {
		t.Fatalf("%+v, %v", back, err)
	}
	f.Habits[0].Anchor = ""
	if err := Save(path, f); err == nil {
		t.Error("неверный файл записан")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("остались временные файлы: %v", entries)
	}
}
