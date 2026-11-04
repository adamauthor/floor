package webapp

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"floor/internal/config"
	"floor/internal/habits"
)

var settings = config.Settings{WakeAnchor: "10:00", Tolerance: 60, StepsFloor: 7000, StepsGoal: 10000, EveningAt: "21:00", WeeklyAt: "20:00"}

func TestURLRoundTrip(t *testing.T) {
	f := habits.Defaults("2026-11-04")
	st := NewState(settings, f)
	u, err := URL("https://example.test/app/#old", st)
	if err != nil {
		t.Fatal(err)
	}
	base, frag, _ := strings.Cut(u, "#")
	if base != "https://example.test/app/" || frag[0] != 'z' {
		t.Fatalf("адрес %s", u)
	}
	packed, err := base64.RawURLEncoding.DecodeString(frag[1:])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(packed)))
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if back.Rev != Rev(settings, f) || back.Settings != settings || len(back.Habits) != 4 {
		t.Errorf("состояние после обмена: %+v", back)
	}
	if len(u) > 1500 {
		t.Errorf("адрес слишком длинный: %d", len(u))
	}
}

func TestParseRejects(t *testing.T) {
	for _, data := range []string{`{`, `{"v":2,"rev":"x"}`, `{"v":1}`, `{"v":1,"rev":"x","extra":1}`} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s принят", data)
		}
	}
}

func TestRevChangesWithContent(t *testing.T) {
	f := habits.Defaults("2026-11-04")
	r := Rev(settings, f)
	s2 := settings
	s2.StepsGoal = 9000
	f2 := habits.Defaults("2026-11-04")
	f2.Habits[0].Action = "встаю"
	if Rev(s2, f) == r || Rev(settings, f2) == r || Rev(settings, habits.Defaults("2026-11-04")) != r {
		t.Error("версия не отражает содержимое")
	}
}

func TestChanges(t *testing.T) {
	old := habits.Defaults("2026-11-04").Habits
	next := append([]habits.Habit(nil), old[1:]...)
	next[0].Paused = true
	next[1].Action = "выхожу"
	next = append(next, habits.Habit{ID: "n", Title: "Новая"})
	s2 := settings
	s2.WakeAnchor, s2.Tolerance = "09:00", 30
	got := strings.Join(Changes(settings, s2, old, next), "; ")
	want := "подъём 10:00 → 09:00; допуск ±60 → ±30 мин; «Зубы» на паузе; «Улица» изменена; новая привычка «Новая»; «Подъём» удалена"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	swapped := []habits.Habit{old[1], old[0], old[2], old[3]}
	if got := Changes(settings, settings, old, swapped); len(got) != 1 || got[0] != "порядок привычек" {
		t.Errorf("перестановка: %v", got)
	}
	if got := Changes(settings, settings, old, old); len(got) != 0 {
		t.Errorf("без правок: %v", got)
	}
}
