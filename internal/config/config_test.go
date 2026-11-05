package config

import (
	"reflect"
	"testing"
)

func TestParseClock(t *testing.T) {
	tests := []struct {
		in      string
		want    Clock
		wantErr bool
	}{
		{"07:00", Clock{7, 0}, false},
		{"21:30", Clock{21, 30}, false},
		{" 06:05 ", Clock{6, 5}, false},
		{"7", Clock{}, true},
		{"25:00", Clock{}, true},
	}
	for _, tt := range tests {
		got, err := ParseClock(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseClock(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseClock(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestLoadRequiresTelegram(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_OWNER_ID", "")
	if _, err := Load(); err == nil {
		t.Fatal("ожидалась ошибка без TELEGRAM_TOKEN и TELEGRAM_OWNER_ID")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "x")
	t.Setenv("TELEGRAM_OWNER_ID", "42")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WakeAnchor != (Clock{7, 0}) || c.StepsFloor != 7000 || c.StepsGoal != 10000 {
		t.Errorf("неожиданные значения по умолчанию: %+v", c)
	}
	if c.DefaultTZ.String() != "Asia/Almaty" {
		t.Errorf("DefaultTZ = %s", c.DefaultTZ)
	}
}

func TestLoadValidates(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"часы", "WAKE_ANCHOR", "семь"},
		{"число", "WAKE_TOLERANCE_MIN", "час"},
		{"длительность", "LLM_TIMEOUT", "долго"},
		{"пояс", "DEFAULT_TZ", "Марс/Олимп"},
		{"источник", "HEALTH_SOURCE", "ftp"},
		{"owner", "TELEGRAM_OWNER_ID", "abc"},
		{"http без токена", "HEALTH_SOURCE", "http"},
		{"google без клиента", "HEALTH_SOURCE", "google"},
		{"пол выше цели", "STEPS_FLOOR", "20000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TELEGRAM_TOKEN", "x")
			t.Setenv("TELEGRAM_OWNER_ID", "42")
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil {
				t.Fatalf("ожидалась ошибка для %s=%s", tt.key, tt.value)
			}
		})
	}
}

func TestLoadLocalWithoutSecrets(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_OWNER_ID", "")
	if _, err := LoadLocal(); err != nil {
		t.Fatalf("LoadLocal не должен требовать секреты: %v", err)
	}
}

func TestSettingsApply(t *testing.T) {
	c := Config{}
	s := Settings{WakeAnchor: "09:30", Tolerance: 45, StepsFloor: 5000, StepsGoal: 8000, EveningAt: "22:15", WeeklyAt: "19:00"}
	if err := s.Apply(&c); err != nil {
		t.Fatal(err)
	}
	if c.Settings() != s {
		t.Errorf("туда-обратно: %+v", c.Settings())
	}
	for _, bad := range []Settings{
		{WakeAnchor: "9", Tolerance: 45, StepsFloor: 5000, StepsGoal: 8000, EveningAt: "22:15", WeeklyAt: "19:00"},
		{WakeAnchor: "09:30", Tolerance: 5, StepsFloor: 5000, StepsGoal: 8000, EveningAt: "22:15", WeeklyAt: "19:00"},
		{WakeAnchor: "09:30", Tolerance: 45, StepsFloor: 9000, StepsGoal: 8000, EveningAt: "22:15", WeeklyAt: "19:00"},
	} {
		before := c
		if err := bad.Apply(&c); err == nil || !reflect.DeepEqual(c, before) {
			t.Errorf("%+v: err = %v, конфигурация изменилась: %v", bad, err, !reflect.DeepEqual(c, before))
		}
	}
}

func TestWorkoutTypes(t *testing.T) {
	if got := SplitList(" a, ,b ,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("SplitList: %q", got)
	}
	if len(SplitList(DefaultWorkoutTypes)) < 10 {
		t.Error("мало типов по умолчанию")
	}
}
