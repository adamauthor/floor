package metrics

import (
	"math"
	"sort"
	"time"
)

// SleepRegularity — индекс регулярности сна от −100 до 100 (Windred et al. 2024): как часто
// человек в одно и то же время суток в соседние дни в одном состоянии — спит или нет.
// Считается приблизительно: ночь — отрезок [подъём − длительность сна, подъём], дневной
// сон и пробуждения внутри ночи не учитываются. false — меньше четырёх ночей с данными.
func SleepRegularity(days []Day) (int, bool) {
	type span struct{ from, to time.Time }
	var nights []span
	for _, d := range days {
		if d.WakeAt != nil && d.SleepMinutes != nil && *d.SleepMinutes > 0 {
			nights = append(nights, span{d.WakeAt.Add(-time.Duration(*d.SleepMinutes) * time.Minute), *d.WakeAt})
		}
	}
	if len(nights) < 4 {
		return 0, false
	}
	sort.Slice(nights, func(i, j int) bool { return nights[i].to.Before(nights[j].to) })
	asleep := func(t time.Time) bool {
		for _, n := range nights {
			if !t.Before(n.from) && t.Before(n.to) {
				return true
			}
		}
		return false
	}
	// Состояние известно в сутках до каждого подъёма, о которых есть данные.
	known := func(t time.Time) bool {
		for _, n := range nights {
			if !t.Before(n.to.Add(-24*time.Hour)) && t.Before(n.to) {
				return true
			}
		}
		return false
	}
	const step = 5 * time.Minute
	agree, total := 0, 0
	end := nights[len(nights)-1].to
	for t := nights[0].to.Add(-24 * time.Hour); t.Add(24 * time.Hour).Before(end); t = t.Add(step) {
		next := t.Add(24 * time.Hour)
		if !known(t) || !known(next) {
			continue
		}
		total++
		if asleep(t) == asleep(next) {
			agree++
		}
	}
	if total == 0 {
		return 0, false
	}
	return int(math.Round(200*float64(agree)/float64(total) - 100)), true
}

// Returns — сколько раз привычку сделали хотя бы на пол на следующий день после пропуска
// (Milkman et al. 2021: возвращение важнее серии). marks: дата → id привычки → уровень,
// 0 — пропуск. Считаются возвращения в дни [from, to].
func Returns(marks map[string]map[string]int, from, to string) int {
	n := 0
	for date := from; date <= to; date = AddDays(date, 1) {
		prev := marks[AddDays(date, -1)]
		for id, level := range marks[date] {
			if was, ok := prev[id]; ok && was == 0 && level > 0 {
				n++
			}
		}
	}
	return n
}
