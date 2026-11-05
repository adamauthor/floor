package metrics

import "sort"

// FocusShare — доля дней, с которой область считается держащейся: 5 из 7.
const FocusShare = 0.7

// Area — одна область для «где поднажать»: сколько засчитано, из скольких и сколько нужно.
type Area struct {
	Key      string
	Done, Of int
	Need     int
	Weekly   bool // тренировки: считаются разами, а не днями; дневным нужно от 3 дней данных
}

// NeedFor — сколько дней из of нужно, чтобы держалось: ⌈0,7·of⌉.
func NeedFor(of int) int {
	return (of*7 + 9) / 10
}

// Weak — области ниже пола, самые отстающие (меньшая доля засчитанного) первыми, не больше n.
func Weak(areas []Area, n int) []Area {
	var weak []Area
	for _, a := range areas {
		if a.Of < 3 && !a.Weekly || a.Of == 0 || a.Done >= a.Need {
			continue
		}
		weak = append(weak, a)
	}
	share := func(a Area) float64 { return float64(a.Done) / float64(a.Of) }
	sort.SliceStable(weak, func(i, j int) bool { return share(weak[i]) < share(weak[j]) })
	if len(weak) > n {
		weak = weak[:n]
	}
	return weak
}
