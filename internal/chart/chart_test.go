package chart

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ip(v int) *int { return &v }

func dates(from string, n int) []string {
	t := day(from)
	out := make([]string, n)
	for i := range out {
		out[i] = t.AddDate(0, 0, i).Format(time.DateOnly)
	}
	return out
}

func samplePeriod(n int) Period {
	p := Period{
		Title: "Неделя 2–8 ноября", Subtitle: "Пол удержан 5 из 7 дней · 2 возвращения после пропуска",
		Dates: dates("2026-11-02", n), StepsFloor: 7000, WakeAnchor: "07:00", Tolerance: 60,
	}
	names := []string{"Пол дня", "⏰ Подъём без телефона", "🚶 Улица", "📦 Вещь на место", "🦷 Зубы вечером с очень длинным названием", "🏋️ Тренировка"}
	for k, name := range names {
		r := Row{Name: name, Gap: k == 0}
		for i := range n {
			r.Cells = append(r.Cells, Cell((i*7+k*3)%4))
		}
		p.Rows = append(p.Rows, r)
	}
	for i := range n {
		if i%9 == 5 {
			p.Steps, p.Wake = append(p.Steps, nil), append(p.Wake, nil)
			continue
		}
		p.Steps = append(p.Steps, ip(3000+(i*2731)%9000))
		p.Wake = append(p.Wake, ip((i*37)%150-70))
	}
	return p
}

func sampleMoney() Money {
	m := Money{Title: "Деньги", Subtitle: "Сверка 20 декабря · по плану", From: "2026-11-04", To: "2027-03-10"}
	var debt, cushion []Point
	d, cu := int64(1_695_384), int64(200_000)
	for t := day(m.From); !t.After(day("2027-02-05")); t = t.AddDate(0, 0, 1) {
		switch t.Day() {
		case 5, 18:
			d -= 300_000
			cu += 249_000
		}
		debt = append(debt, Point{t.Format(time.DateOnly), max(d, 0)})
		cushion = append(cushion, Point{t.Format(time.DateOnly), cu})
	}
	target := int64(1_950_000)
	m.Panels = []Panel{
		{Title: "Чистый долг", Note: "закроется 5 фев", Plan: debt, By: "2027-02-05",
			Actual: []Point{{"2026-11-04", 1_695_384}, {"2026-11-20", 1_150_000}, {"2026-12-20", 640_000}}},
		{Title: "Подушка", Note: "к сроку 1,95 млн", Plan: cushion, Target: &target, By: "2027-03-05",
			Actual: []Point{{"2026-11-04", 200_000}, {"2026-11-20", 440_000}, {"2026-12-20", 900_000}}},
	}
	return m
}

func TestPNG(t *testing.T) {
	out := os.Getenv("CHART_OUT")
	for name, f := range map[string]func() ([]byte, error){
		"week.png":  samplePeriod(7).PNG,
		"month.png": samplePeriod(30).PNG,
		"money.png": sampleMoney().PNG,
	} {
		b, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w := img.Bounds().Dx(); w != width*scale {
			t.Errorf("%s: ширина %d", name, w)
		}
		if h := img.Bounds().Dy(); h < 300 || h > maxH*scale {
			t.Errorf("%s: высота %d", name, h)
		}
		if out != "" {
			if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := (Period{}).PNG(); err == nil {
		t.Error("пустой период нарисован")
	}
	if _, err := (Money{}).PNG(); err == nil {
		t.Error("пустые деньги нарисованы")
	}
}

func TestFormat(t *testing.T) {
	for v, want := range map[int64]string{0: "0", 950: "950", 8240: "8,2 тыс", 7000: "7 тыс", 12345: "12 тыс", 600_000: "600 тыс", 1_750_000: "1,75 млн", 2_000_000: "2 млн", -1500: "−1,5 тыс"} {
		if got := compact(v); got != want {
			t.Errorf("compact(%d) = %q, want %q", v, got, want)
		}
	}
	if got := thousands(1234567); got != "1 234 567" {
		t.Errorf("thousands = %q", got)
	}
	c := newCanvas(100, 100)
	if got := c.clean("🚶 Улица ✨"); got != "Улица" {
		t.Errorf("clean = %q", got)
	}
}
