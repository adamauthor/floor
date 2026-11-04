// Package health описывает контракт выгрузки с iPhone и источники данных.
// Контракт — docs/SPEC.md, раздел 4.
package health

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion — единственная поддерживаемая версия контракта.
const SchemaVersion = 1

// ErrSchema — выгрузка другой версии контракта, данные не применяются.
var ErrSchema = errors.New("неподдерживаемая версия schema")

// Нормализованные значения стадий сна. В сон засчитываются только Asleep-значения.
const (
	ValueAsleep      = "asleep"
	ValueCore        = "core"
	ValueDeep        = "deep"
	ValueREM         = "rem"
	ValueUnspecified = "unspecified"
	ValueInBed       = "inbed"
	ValueAwake       = "awake"
	ValueUnknown     = "unknown"
)

// Export — одна выгрузка из Команды iOS (schema = 1).
type Export struct {
	Schema      int
	GeneratedAt time.Time
	TZOffset    string
	Steps       []StepsDay
	Sleep       []SleepSample
	Raw         []byte // исходное тело: хранится для идемпотентности и пересчёта

	// Complete — выгрузка полная за даты From..To (Google Health): сон в ней
	// не склеивается с прошлыми выгрузками, а ночь без данных очищается.
	// Команда iOS видит только «последние 2 дня» и полной не бывает.
	Complete bool
	From, To string
}

// SHA256 — ключ идемпотентности выгрузки.
func (e Export) SHA256() string {
	sum := sha256.Sum256(e.Raw)
	return hex.EncodeToString(sum[:])
}

// StepsDay — сумма шагов за местную дату.
type StepsDay struct {
	Date  string // YYYY-MM-DD
	Count int
}

// SleepSample — один сегмент сна из Apple Health.
type SleepSample struct {
	Start time.Time
	End   time.Time
	Value string
}

// Source поставляет выгрузки. Реализации: FileSource (Mac), HTTPSource (Fly.io).
type Source interface {
	// Updates возвращает канал выгрузок; закрывается при отмене ctx.
	Updates(ctx context.Context) (<-chan Export, error)
}

// flexInt принимает числа в том виде, в каком их отдаёт Команда iOS:
// 4812, 4812.0, "4812", "4 812", "4812,5".
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	s = strings.NewReplacer(" ", "", " ", "", " ", "", ",", ".").Replace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("ожидается число, получено %q", string(b))
	}
	*f = flexInt(v)
	return nil
}

type rawExport struct {
	Schema      flexInt `json:"schema"`
	GeneratedAt string  `json:"generated_at"`
	TZOffset    string  `json:"tz_offset"`
	Source      string  `json:"source"`
	RangeFrom   string  `json:"range_from"`
	RangeTo     string  `json:"range_to"`
	Steps       []struct {
		Date  string  `json:"date"`
		Count flexInt `json:"count"`
	} `json:"steps"`
	Sleep []struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Value string `json:"value"`
	} `json:"sleep"`
}

// Parse разбирает и нормализует выгрузку. Неизвестные поля игнорируются,
// отдельные битые записи пропускаются: один плохой сегмент не должен стоить всего дня.
func Parse(raw []byte) (Export, error) {
	var r rawExport
	if err := json.Unmarshal(raw, &r); err != nil {
		return Export{}, fmt.Errorf("разбор JSON: %w", err)
	}
	if int(r.Schema) != SchemaVersion {
		return Export{}, fmt.Errorf("%w: %d", ErrSchema, int(r.Schema))
	}
	generated, err := time.Parse(time.RFC3339, strings.TrimSpace(r.GeneratedAt))
	if err != nil {
		return Export{}, fmt.Errorf("generated_at: %w", err)
	}

	e := Export{Schema: int(r.Schema), GeneratedAt: generated, Raw: raw}
	if r.Source == "google" {
		from, err1 := time.Parse(time.DateOnly, r.RangeFrom)
		to, err2 := time.Parse(time.DateOnly, r.RangeTo)
		if err1 == nil && err2 == nil && !to.Before(from) && to.Sub(from) <= 31*24*time.Hour {
			e.Complete, e.From, e.To = true, r.RangeFrom, r.RangeTo
		}
	}

	e.TZOffset = NormalizeOffset(r.TZOffset)
	if e.TZOffset == "" {
		e.TZOffset = generated.Format("-07:00")
	}

	for _, s := range r.Steps {
		if _, err := time.Parse(time.DateOnly, s.Date); err != nil || s.Count < 0 {
			continue
		}
		e.Steps = append(e.Steps, StepsDay{Date: s.Date, Count: int(s.Count)})
	}

	for _, s := range r.Sleep {
		start, err1 := time.Parse(time.RFC3339, strings.TrimSpace(s.Start))
		end, err2 := time.Parse(time.RFC3339, strings.TrimSpace(s.End))
		if err1 != nil || err2 != nil || !end.After(start) {
			continue
		}
		e.Sleep = append(e.Sleep, SleepSample{Start: start, End: end, Value: NormalizeSleepValue(s.Value)})
	}
	return e, nil
}

var sleepValues = map[string]string{
	"asleep": ValueAsleep, "сон": ValueAsleep, "спит": ValueAsleep,
	"core": ValueCore, "asleepcore": ValueCore, "основной": ValueCore, "основной сон": ValueCore,
	"deep": ValueDeep, "asleepdeep": ValueDeep, "глубокий": ValueDeep, "глубокий сон": ValueDeep,
	"rem": ValueREM, "asleeprem": ValueREM, "rem-сон": ValueREM, "быстрый сон": ValueREM, "быстрый": ValueREM,
	"unspecified": ValueUnspecified, "asleepunspecified": ValueUnspecified, "не указано": ValueUnspecified,
	"inbed": ValueInBed, "in bed": ValueInBed, "в кровати": ValueInBed, "в постели": ValueInBed,
	"awake": ValueAwake, "бодрствование": ValueAwake, "не спит": ValueAwake,
}

// NormalizeSleepValue приводит название стадии (в том числе русское из iOS) к английскому.
func NormalizeSleepValue(v string) string {
	if n, ok := sleepValues[strings.ToLower(strings.TrimSpace(v))]; ok {
		return n
	}
	return ValueUnknown
}

var offsetRe = regexp.MustCompile(`^([+-])(\d{2}):?(\d{2})$`)

// NormalizeOffset приводит смещение к виду +05:00. Пустая строка — смещение не распознано.
func NormalizeOffset(s string) string {
	s = strings.TrimSpace(s)
	if s == "Z" {
		return "+00:00"
	}
	m := offsetRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1] + m[2] + ":" + m[3]
}

// OffsetLocation превращает смещение в часовой пояс.
func OffsetLocation(offset string) (*time.Location, bool) {
	t, err := time.Parse("-07:00", NormalizeOffset(offset))
	if err != nil {
		return nil, false
	}
	_, sec := t.Zone()
	return time.FixedZone(offset, sec), true
}
