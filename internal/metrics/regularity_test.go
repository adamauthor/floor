package metrics

import (
	"testing"
	"time"
)

func night(date, wake string, minutes int) Day {
	t, _ := time.Parse(time.RFC3339, date+"T"+wake+":00+05:00")
	return Day{Date: date, WakeAt: &t, SleepMinutes: &minutes}
}

func TestSleepRegularity(t *testing.T) {
	same := []Day{night("2026-11-01", "08:00", 480), night("2026-11-02", "08:00", 480),
		night("2026-11-03", "08:00", 480), night("2026-11-04", "08:00", 480)}
	if got, ok := SleepRegularity(same); !ok || got != 100 {
		t.Errorf("одинаковый сон: %d, %v", got, ok)
	}
	shifted := []Day{night("2026-11-01", "08:00", 480), night("2026-11-02", "14:00", 480),
		night("2026-11-03", "08:00", 480), night("2026-11-04", "14:00", 480)}
	if got, ok := SleepRegularity(shifted); !ok || got >= 50 {
		t.Errorf("сдвиг на 6 часов через день должен заметно снижать индекс: %d", got)
	}
	if _, ok := SleepRegularity(same[:3]); ok {
		t.Error("трёх ночей мало")
	}
}

func TestReturns(t *testing.T) {
	marks := map[string]map[string]int{
		"2026-10-31": {"a": 0},
		"2026-11-01": {"a": 1, "b": 0},
		"2026-11-02": {"a": 0, "b": 2},
		"2026-11-03": {"a": 0},
	}
	if got := Returns(marks, "2026-11-01", "2026-11-03"); got != 2 {
		t.Errorf("возвращений %d, ожидалось 2", got)
	}
}
