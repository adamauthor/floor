package config

import "testing"

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
	if c.WakeAnchor != (Clock{7, 0}) || c.StepsFloor != 4000 || c.StepsGoal != 7000 {
		t.Errorf("неожиданные значения по умолчанию: %+v", c)
	}
	if c.DefaultTZ.String() != "Asia/Almaty" {
		t.Errorf("DefaultTZ = %s", c.DefaultTZ)
	}
}
