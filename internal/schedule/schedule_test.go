package schedule

import (
	"context"
	"testing"
	"time"

	"floor/internal/config"
)

var (
	almaty = time.FixedZone("+05:00", 5*3600)
	anchor = config.Clock{Hour: 7}
)

func local(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, almaty)
	if err != nil {
		panic(err)
	}
	return t
}

func TestLocation(t *testing.T) {
	if loc := Location("+07:00", time.UTC); loc == time.UTC {
		t.Error("смещение проигнорировано")
	}
	if loc := Location("", time.UTC); loc != time.UTC {
		t.Error("без смещения нужен пояс по умолчанию")
	}
}

func TestMorningDue(t *testing.T) {
	wake := local("2026-10-31 07:24")
	lateWake := local("2026-10-31 09:50")
	yesterday := local("2026-10-30 21:00")
	tests := []struct {
		name string
		now  string
		wake *time.Time
		want bool
	}{
		{"рано и данных нет", "2026-10-31 09:59", nil, false},
		{"якорь + 3 часа без данных", "2026-10-31 10:00", nil, true},
		{"виден подъём, прошло меньше 30 минут", "2026-10-31 07:53", &wake, false},
		{"виден подъём, прошло 30 минут", "2026-10-31 07:54", &wake, true},
		{"поздний подъём: fallback раньше данных", "2026-10-31 10:05", &lateWake, true},
		{"окно закрылось", "2026-10-31 14:00", &wake, false},
		{"подъём вчерашней датой не считается", "2026-10-31 00:30", &yesterday, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MorningDue(local(tt.now), anchor, tt.wake); got != tt.want {
				t.Errorf("MorningDue = %v, ожидалось %v", got, tt.want)
			}
		})
	}
}

func TestEveningDate(t *testing.T) {
	evening := config.Clock{Hour: 21}
	tests := []struct {
		now, date string
		ok        bool
	}{
		{"2026-10-31 20:59", "", false},
		{"2026-10-31 21:00", "2026-10-31", true},
		{"2026-10-31 23:59", "2026-10-31", true},
		{"2026-11-01 00:30", "2026-10-31", true},
		{"2026-11-01 02:59", "2026-10-31", true},
		{"2026-11-01 03:00", "", false},
	}
	for _, tt := range tests {
		date, ok := EveningDate(local(tt.now), evening)
		if date != tt.date || ok != tt.ok {
			t.Errorf("EveningDate(%s) = %q %v", tt.now, date, ok)
		}
	}
}

func TestWeeklyKey(t *testing.T) {
	weekly := config.Clock{Hour: 20}
	tests := []struct {
		now, key string
		ok       bool
	}{
		{"2026-11-01 19:59", "", false},
		{"2026-11-01 20:00", "2026-10-26", true},
		{"2026-11-01 23:00", "2026-10-26", true},
		{"2026-10-31 21:00", "", false},
		{"2026-11-02 00:10", "", false},
	}
	for _, tt := range tests {
		key, ok := WeeklyKey(local(tt.now), weekly)
		if key != tt.key || ok != tt.ok {
			t.Errorf("WeeklyKey(%s) = %q %v", tt.now, key, ok)
		}
	}
}

func TestFloorExpired(t *testing.T) {
	tests := []struct {
		now  string
		want bool
	}{
		{"2026-10-31 21:00", false},
		{"2026-11-01 11:59", false},
		{"2026-11-01 12:00", true},
		{"2026-11-03 09:00", true},
	}
	for _, tt := range tests {
		if got := FloorExpired(local(tt.now), "2026-10-31"); got != tt.want {
			t.Errorf("FloorExpired(%s) = %v", tt.now, got)
		}
	}
	if !FloorExpired(local("2026-10-31 21:00"), "мусор") {
		t.Error("битая дата должна считаться устаревшей")
	}
}

func TestBackoff(t *testing.T) {
	var b Backoff
	now := local("2026-10-31 10:00")
	if !b.Ready(now) {
		t.Fatal("новый backoff должен быть готов")
	}
	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		b.Fail(now)
		if b.Ready(now.Add(want - time.Second)) {
			t.Errorf("готов раньше %v", want)
		}
		if !b.Ready(now.Add(want)) {
			t.Errorf("не готов через %v", want)
		}
	}
	b.Reset()
	if !b.Ready(now) {
		t.Error("после Reset должен быть готов")
	}
}

func TestRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	Run(ctx, time.Millisecond, func(context.Context, time.Time) {
		calls++
		if calls == 3 {
			cancel()
		}
	})
	if calls != 3 {
		t.Errorf("вызовов = %d", calls)
	}
}

func TestMorningWindowFollowsAnchor(t *testing.T) {
	loc := time.FixedZone("+05:00", 5*3600)
	at := func(s string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc)
		return v
	}
	late := config.Clock{Hour: 12}
	if !MorningDue(at("2026-11-04 15:00"), late, nil) {
		t.Error("при якоре 12:00 утро без данных должно прийти в 15:00")
	}
	if MorningDue(at("2026-11-04 19:00"), late, nil) {
		t.Error("окно при якоре 12:00 закрывается в 19:00")
	}
	if !MorningDue(at("2026-11-04 23:30"), config.Clock{Hour: 20}, nil) {
		t.Error("окно не должно переходить через полночь, но до 23:59 открыто")
	}
}
