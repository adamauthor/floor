// Package metrics считает подъём, сон, шаги и правила пола. Только чистые функции,
// правила — docs/SPEC.md, раздел 3.
package metrics

import (
	"sort"
	"time"

	"floor/internal/config"
)

// Статусы пола дня.
const (
	StatusYes  = "yes"
	StatusNo   = "no"
	StatusNone = "none"
)

const (
	mergeGap       = 60 * time.Minute // разрыв, внутри которого сегменты — один эпизод
	napMaxDuration = 3 * time.Hour
)

// Segment — сегмент сна с уже нормализованным значением.
type Segment struct {
	Start, End time.Time
	Value      string
}

// Episode — склеенные сегменты одного сна. End хранит смещение последнего сегмента.
type Episode struct {
	Start, End time.Time
	Asleep     time.Duration // объединение сегментов: наложения не считаются дважды
}

// Night — итог ночи для местного дня.
type Night struct {
	Date    string
	WakeAt  time.Time
	Minutes int
}

// Day — данные одного местного дня.
type Day struct {
	Date         string
	Steps        *int
	SleepMinutes *int
	WakeAt       *time.Time
	WakeDeltaMin *int
	FloorStatus  string
}

func isAsleep(value string) bool {
	switch value {
	case "asleep", "core", "deep", "rem", "unspecified":
		return true
	}
	return false
}

// Episodes отбрасывает inBed/awake и склеивает сегменты с разрывом меньше часа.
func Episodes(segments []Segment) []Episode {
	asleep := make([]Segment, 0, len(segments))
	for _, s := range segments {
		if isAsleep(s.Value) && s.End.After(s.Start) {
			asleep = append(asleep, s)
		}
	}
	sort.Slice(asleep, func(i, j int) bool { return asleep[i].Start.Before(asleep[j].Start) })

	var out []Episode
	var cur *Episode
	for _, s := range asleep {
		if cur == nil || s.Start.Sub(cur.End) >= mergeGap {
			out = append(out, Episode{Start: s.Start, End: s.End, Asleep: s.End.Sub(s.Start)})
			cur = &out[len(out)-1]
			continue
		}
		if !s.End.After(cur.End) {
			continue // полностью внутри уже учтённого
		}
		from := s.Start
		if cur.End.After(from) {
			from = cur.End
		}
		cur.Asleep += s.End.Sub(from)
		cur.End = s.End
	}
	return out
}

// isNap — дневной сон, не влияющий на подъём. Верхняя граница 18:00 отделяет его
// от короткой ночи: 23:00–01:30 остаётся ночью, хотя началось «после 10:00».
func isNap(e Episode) bool {
	start := e.Start.In(e.End.Location())
	return e.Asleep < napMaxDuration && start.Hour() >= 10 && start.Hour() < 18
}

// window — ночное окно дня date: с D-1 18:00 до D 14:00 в заданном поясе.
func window(date string, loc *time.Location) (from, to time.Time, ok bool) {
	d, err := time.ParseInLocation(time.DateOnly, date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return d.Add(-6 * time.Hour), d.Add(14 * time.Hour), true
}

// MainEpisode выбирает основной эпизод дня: самый длинный, пересекающий окно.
// Окно считается в смещении самого эпизода, поэтому смена пояса не двигает прошлые ночи.
func MainEpisode(episodes []Episode, date string) (Episode, bool) {
	var best Episode
	found := false
	for _, e := range episodes {
		if isNap(e) {
			continue
		}
		from, to, ok := window(date, e.End.Location())
		if !ok || !e.End.After(from) || !e.Start.Before(to) {
			continue
		}
		if !found || e.Asleep > best.Asleep {
			best, found = e, true
		}
	}
	return best, found
}

// Nights считает ночи для всех дат, которых касаются сегменты.
func Nights(segments []Segment) map[string]Night {
	episodes := Episodes(segments)
	out := make(map[string]Night)
	dates := make(map[string]bool)
	for _, e := range episodes {
		end := e.End
		dates[end.Format(time.DateOnly)] = true
		if end.Hour() >= 18 {
			dates[end.AddDate(0, 0, 1).Format(time.DateOnly)] = true
		}
	}
	for date := range dates {
		if e, ok := MainEpisode(episodes, date); ok {
			out[date] = Night{Date: date, WakeAt: e.End, Minutes: int(e.Asleep.Minutes())}
		}
	}
	return out
}

// WakeDelta — подъём минус якорь в минутах, со знаком.
func WakeDelta(wake time.Time, date string, anchor config.Clock) int {
	loc := wake.Location()
	d, err := time.ParseInLocation(time.DateOnly, date, loc)
	if err != nil {
		return 0
	}
	at := time.Date(d.Year(), d.Month(), d.Day(), anchor.Hour, anchor.Minute, 0, 0, loc)
	return int(wake.Sub(at).Minutes())
}

// InNorm — подъём в пределах допуска от якоря.
func InNorm(delta, tolerance int) bool {
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// StepsLevel — какая планка шагов взята.
type StepsLevel int

// Уровни шагов.
const (
	StepsBelowFloor StepsLevel = iota
	StepsFloorTaken
	StepsGoalTaken
)

// Level определяет уровень шагов относительно пола и цели.
func Level(steps, floor, goal int) StepsLevel {
	switch {
	case steps >= goal:
		return StepsGoalTaken
	case steps >= floor:
		return StepsFloorTaken
	}
	return StepsBelowFloor
}

// IsMiss — день считается пропуском.
func IsMiss(status string) bool { return status == StatusNo || status == StatusNone }

// AddDays сдвигает дату YYYY-MM-DD на n дней.
func AddDays(date string, n int) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, n).Format(time.DateOnly)
}

// Monday — понедельник недели, в которую входит дата.
func Monday(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	back := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -back).Format(time.DateOnly)
}

// WeekStats — сводка недели, раздел 3.3.
type WeekStats struct {
	WakeInNorm int
	WakeKnown  int
	SpreadMin  int
	StepsFloor int
	StepsKnown int
	FloorYes   int
}

// Week считает сводку. Шаги сегодняшнего дня не учитываются: день ещё не закончен.
func Week(days []Day, today string, tolerance, stepsFloor int) WeekStats {
	var s WeekStats
	minDelta, maxDelta := 0, 0
	for _, d := range days {
		if d.WakeDeltaMin != nil {
			delta := *d.WakeDeltaMin
			if s.WakeKnown == 0 || delta < minDelta {
				minDelta = delta
			}
			if s.WakeKnown == 0 || delta > maxDelta {
				maxDelta = delta
			}
			s.WakeKnown++
			if InNorm(delta, tolerance) {
				s.WakeInNorm++
			}
		}
		if d.Steps != nil && d.Date != today {
			s.StepsKnown++
			if *d.Steps >= stepsFloor {
				s.StepsFloor++
			}
		}
		if d.FloorStatus == StatusYes {
			s.FloorYes++
		}
	}
	s.SpreadMin = maxDelta - minDelta
	return s
}
