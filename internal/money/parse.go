package money

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Номера карт и счетов: 12+ цифр подряд или четыре группы по четыре. Не сохраняются нигде.
var longDigits = regexp.MustCompile(`\d{4}[ \-]?\d{4}[ \-]?\d{4}(?:[ \-]?\d{1,7})?|\d{12,}`)

// Число: с разделителями тысяч (142 000, 142.000, 1 120 000) или с дробью (1,12), и множитель.
var numberRe = regexp.MustCompile(`(\d{1,3}(?:[  .]\d{3})+|\d+(?:[.,]\d+)?)\s?(млн|тыс|к|k|м|m)?`)

var groupedRe = regexp.MustCompile(`^\d{1,3}(?:[  .]\d{3})+$`)

// Scrub убирает из текста номера карт и счетов. Всё, что дальше уходит в Claude или
// в логи, проходит через него.
func Scrub(text string) string {
	return longDigits.ReplaceAllString(text, "…")
}

func normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
}

type span struct {
	from, to int
	key      string
	value    int64
}

func isLetter(r rune) bool { return unicode.IsLetter(r) || r == '_' }

// bounded — совпадение не внутри другого слова.
func bounded(s string, from, to int) bool {
	if from > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:from]); isLetter(r) {
			return false
		}
	}
	if to < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[to:]); isLetter(r) {
			return false
		}
	}
	return true
}

// aliasSpans — найденные в тексте алиасы, самые длинные побеждают.
func (f File) aliasSpans(text string) []span {
	type alias struct{ text, key string }
	var all []alias
	for _, a := range f.Accounts {
		for _, al := range a.Aliases {
			all = append(all, alias{normalize(al), a.ID})
		}
	}
	for _, al := range f.DebtAliases {
		all = append(all, alias{normalize(al), DebtKey})
	}
	sort.SliceStable(all, func(i, j int) bool { return len(all[i].text) > len(all[j].text) })

	taken := make([]bool, len(text))
	var out []span
	for _, al := range all {
		if al.text == "" {
			continue
		}
		for start := 0; start < len(text); {
			i := strings.Index(text[start:], al.text)
			if i < 0 {
				break
			}
			from, to := start+i, start+i+len(al.text)
			start = to
			free := true
			for k := from; k < to; k++ {
				free = free && !taken[k]
			}
			if !free || !bounded(text, from, to) {
				continue
			}
			for k := from; k < to; k++ {
				taken[k] = true
			}
			out = append(out, span{from: from, to: to, key: al.key})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

func numberSpans(text string) []span {
	var out []span
	for _, m := range numberRe.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(text[:m[0]]); isLetter(r) || unicode.IsDigit(r) {
				continue // часть слова или кода вроде «rbk2»
			}
		}
		raw, to := text[m[2]:m[3]], m[1]
		suffix := ""
		if m[4] >= 0 {
			suffix = text[m[4]:m[5]]
			if r, _ := utf8.DecodeRuneInString(text[m[5]:]); m[5] < len(text) && isLetter(r) {
				suffix, to = "", m[3] // «10 минут»: «м» — начало слова, а не миллион
			}
		}
		mult := 1.0
		switch suffix {
		case "к", "k", "тыс":
			mult = 1e3
		case "м", "m", "млн":
			mult = 1e6
		}
		grouped := groupedRe.MatchString(raw)
		if grouped && mult > 1 && strings.Contains(raw, ".") {
			grouped = false // «1.125 млн» — дробь, а не тысячи
		}
		var v float64
		if grouped {
			v, _ = strconv.ParseFloat(strings.NewReplacer(" ", "", " ", "", " ", "", ".", "").Replace(raw), 64)
		} else {
			v, _ = strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64)
		}
		out = append(out, span{from: m[0], to: to, value: int64(math.Round(v * mult))})
	}
	return out
}

// Parsed — итог разбора сообщения.
type Parsed struct {
	Values    map[string]int64 // id счёта или DebtKey → сумма
	Explicit  bool             // начинается с 💰 или «остатки»
	Ambiguous bool             // одно число претендует на два счёта
}

// IsReconcile — сообщение похоже на сверку (раздел 2.3).
func (p Parsed) IsReconcile() bool { return p.Explicit || len(p.Values) >= 2 }

// NeedsHelp — код не справился, нужен разбор Claude.
func (p Parsed) NeedsHelp() bool { return p.Ambiguous || len(p.Values) < 2 }

// ParseText ищет у каждого алиаса ближайшее число справа, до следующего алиаса.
func (f File) ParseText(text string) Parsed {
	clean := normalize(Scrub(text))
	p := Parsed{Values: map[string]int64{}}
	p.Explicit = strings.HasPrefix(clean, "💰") || strings.HasPrefix(clean, "остатки")
	aliases := f.aliasSpans(clean)
	numbers := numberSpans(clean)
	for i, a := range aliases {
		end := len(clean)
		if i+1 < len(aliases) {
			end = aliases[i+1].from
		}
		found := false
		for _, n := range numbers {
			if n.from >= a.to && n.from < end {
				p.Values[a.key] = n.value
				found = true
				break
			}
		}
		if !found && i+1 < len(aliases) {
			// «рбк и лайв по 100к»: у этого алиаса числа нет, а у следующего есть.
			for _, n := range numbers {
				if n.from >= aliases[i+1].to {
					p.Ambiguous = true
					break
				}
			}
		}
	}
	return p
}

// Valid — проверка значений от Claude: только известные ключи и неотрицательные суммы.
func (f File) Valid(values map[string]int64) bool {
	for k, v := range values {
		if _, ok := f.Account(k); !ok && k != DebtKey {
			return false
		}
		if v < 0 || len(strconv.FormatInt(v, 10)) >= 12 {
			return false
		}
	}
	return len(values) > 0
}

// Keys — ключи для промпта разбора: id счёта и его алиасы.
func (f File) Keys() map[string][]string {
	out := map[string][]string{DebtKey: f.DebtAliases}
	for _, a := range f.Accounts {
		out[a.ID] = a.Aliases
	}
	return out
}
