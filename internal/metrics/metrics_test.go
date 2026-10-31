package metrics

import (
	"testing"
	"time"

	"floor/internal/config"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func seg(start, end, value string) Segment {
	return Segment{Start: ts(start), End: ts(end), Value: value}
}

func TestNights(t *testing.T) {
	tests := []struct {
		name        string
		segments    []Segment
		date        string
		wantFound   bool
		wantWake    string
		wantMinutes int
	}{
		{
			name: "пример из контракта: сон через полночь с коротким разрывом",
			segments: []Segment{
				seg("2026-10-30T23:51:00+05:00", "2026-10-31T03:10:00+05:00", "asleep"),
				seg("2026-10-31T03:22:00+05:00", "2026-10-31T07:24:00+05:00", "core"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T07:24:00+05:00", wantMinutes: 199 + 242,
		},
		{
			name: "inBed и awake игнорируются",
			segments: []Segment{
				seg("2026-10-30T22:00:00+05:00", "2026-10-31T09:00:00+05:00", "inbed"),
				seg("2026-10-31T00:00:00+05:00", "2026-10-31T06:00:00+05:00", "deep"),
				seg("2026-10-31T06:00:00+05:00", "2026-10-31T08:30:00+05:00", "awake"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T06:00:00+05:00", wantMinutes: 360,
		},
		{
			name: "разрыв ровно час разделяет эпизоды, основной — длиннейший",
			segments: []Segment{
				seg("2026-10-31T00:00:00+05:00", "2026-10-31T06:00:00+05:00", "core"),
				seg("2026-10-31T07:00:00+05:00", "2026-10-31T08:00:00+05:00", "rem"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T06:00:00+05:00", wantMinutes: 360,
		},
		{
			name: "разрыв 59 минут склеивается",
			segments: []Segment{
				seg("2026-10-31T00:00:00+05:00", "2026-10-31T06:00:00+05:00", "core"),
				seg("2026-10-31T06:59:00+05:00", "2026-10-31T08:00:00+05:00", "rem"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T08:00:00+05:00", wantMinutes: 421,
		},
		{
			name: "дневной сон не влияет на подъём",
			segments: []Segment{
				seg("2026-10-31T00:00:00+05:00", "2026-10-31T02:00:00+05:00", "core"),
				seg("2026-10-31T13:00:00+05:00", "2026-10-31T15:30:00+05:00", "asleep"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T02:00:00+05:00", wantMinutes: 120,
		},
		{
			name: "только дневной сон — ночи нет",
			segments: []Segment{
				seg("2026-10-31T13:00:00+05:00", "2026-10-31T15:30:00+05:00", "asleep"),
			},
			date: "2026-10-31", wantFound: false,
		},
		{
			name: "короткая ночь после полуночи не считается дневным сном",
			segments: []Segment{
				seg("2026-10-30T23:00:00+05:00", "2026-10-31T01:30:00+05:00", "asleep"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T01:30:00+05:00", wantMinutes: 150,
		},
		{
			name: "наложение двух источников не считается дважды",
			segments: []Segment{
				seg("2026-10-31T00:00:00+05:00", "2026-10-31T05:00:00+05:00", "core"),
				seg("2026-10-31T04:00:00+05:00", "2026-10-31T07:00:00+05:00", "asleep"),
				seg("2026-10-31T01:00:00+05:00", "2026-10-31T02:00:00+05:00", "deep"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T07:00:00+05:00", wantMinutes: 420,
		},
		{
			name: "смена пояса: ночь считается в смещении самого сна",
			segments: []Segment{
				seg("2026-10-31T00:30:00+07:00", "2026-10-31T07:15:00+07:00", "asleep"),
			},
			date: "2026-10-31", wantFound: true,
			wantWake: "2026-10-31T07:15:00+07:00", wantMinutes: 405,
		},
		{
			name: "неизвестные значения не засчитываются",
			segments: []Segment{
				seg("2026-10-31T00:00:00+05:00", "2026-10-31T06:00:00+05:00", "unknown"),
			},
			date: "2026-10-31", wantFound: false,
		},
		{name: "нет сегментов", date: "2026-10-31", wantFound: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := Nights(tt.segments)[tt.date]
			if ok != tt.wantFound {
				t.Fatalf("найдено = %v, ожидалось %v (%+v)", ok, tt.wantFound, n)
			}
			if !ok {
				return
			}
			if got := n.WakeAt.Format(time.RFC3339); got != tt.wantWake {
				t.Errorf("подъём = %s, ожидалось %s", got, tt.wantWake)
			}
			if n.Minutes != tt.wantMinutes {
				t.Errorf("минут сна = %d, ожидалось %d", n.Minutes, tt.wantMinutes)
			}
		})
	}
}

func TestWakeDelta(t *testing.T) {
	anchor := config.Clock{Hour: 7}
	tests := []struct {
		wake string
		want int
	}{
		{"2026-10-31T07:24:00+05:00", 24},
		{"2026-10-31T06:30:00+05:00", -30},
		{"2026-10-31T07:00:00+07:00", 0},
		{"2026-10-31T08:01:00+05:00", 61},
	}
	for _, tt := range tests {
		if got := WakeDelta(ts(tt.wake), "2026-10-31", anchor); got != tt.want {
			t.Errorf("WakeDelta(%s) = %d, ожидалось %d", tt.wake, got, tt.want)
		}
	}
}

func TestInNorm(t *testing.T) {
	tests := []struct {
		delta int
		want  bool
	}{{0, true}, {60, true}, {-60, true}, {61, false}, {-61, false}}
	for _, tt := range tests {
		if got := InNorm(tt.delta, 60); got != tt.want {
			t.Errorf("InNorm(%d) = %v", tt.delta, got)
		}
	}
}

func TestLevel(t *testing.T) {
	tests := []struct {
		steps int
		want  StepsLevel
	}{
		{0, StepsBelowFloor}, {6999, StepsBelowFloor}, {7000, StepsFloorTaken},
		{9999, StepsFloorTaken}, {10000, StepsGoalTaken}, {25000, StepsGoalTaken},
	}
	for _, tt := range tests {
		if got := Level(tt.steps, 7000, 10000); got != tt.want {
			t.Errorf("Level(%d) = %v, ожидалось %v", tt.steps, got, tt.want)
		}
	}
}

func TestIsMiss(t *testing.T) {
	for status, want := range map[string]bool{StatusYes: false, StatusNo: true, StatusNone: true} {
		if got := IsMiss(status); got != want {
			t.Errorf("IsMiss(%s) = %v", status, got)
		}
	}
}

func TestDates(t *testing.T) {
	if got := AddDays("2026-11-01", -1); got != "2026-10-31" {
		t.Errorf("AddDays = %s", got)
	}
	tests := map[string]string{
		"2026-10-26": "2026-10-26", // понедельник
		"2026-10-31": "2026-10-26",
		"2026-11-01": "2026-10-26", // воскресенье
		"2026-11-02": "2026-11-02",
	}
	for in, want := range tests {
		if got := Monday(in); got != want {
			t.Errorf("Monday(%s) = %s, ожидалось %s", in, got, want)
		}
	}
	if got := AddDays("плохая дата", 1); got != "плохая дата" {
		t.Errorf("AddDays на плохой дате = %s", got)
	}
	if got := Monday("плохая дата"); got != "плохая дата" {
		t.Errorf("Monday на плохой дате = %s", got)
	}
}

func ptr(n int) *int { return &n }

func TestWeek(t *testing.T) {
	days := []Day{
		{Date: "2026-10-26", WakeDeltaMin: ptr(10), Steps: ptr(8000), FloorStatus: StatusYes},
		{Date: "2026-10-27", WakeDeltaMin: ptr(-20), Steps: ptr(5000), FloorStatus: StatusNo},
		{Date: "2026-10-28", WakeDeltaMin: ptr(90), FloorStatus: StatusNone},
		{Date: "2026-10-29", FloorStatus: StatusYes},
		{Date: "2026-11-01", WakeDeltaMin: ptr(0), Steps: ptr(100), FloorStatus: StatusNone},
	}
	got := Week(days, "2026-11-01", 60, 7000)
	want := WeekStats{WakeInNorm: 3, WakeKnown: 4, SpreadMin: 110, StepsFloor: 1, StepsKnown: 2, FloorYes: 2}
	if got != want {
		t.Errorf("Week = %+v, ожидалось %+v", got, want)
	}
}
