package chart

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"time"
)

// Cell — клетка сетки привычек.
type Cell int8

// Клетки: нет данных (день не отмечен или ещё не наступил), пропуск, пол, цель.
const (
	None Cell = iota
	Miss
	Floor
	Goal
)

// Row — строка сетки: название и клетка на каждый день.
type Row struct {
	Name  string
	Cells []Cell
	Gap   bool // отступ после строки: итоговая строка отделяется от привычек
}

// Period — неделя или месяц: сетка привычек, шаги и подъём по дням.
type Period struct {
	Title    string
	Subtitle string
	Dates    []string // все дни периода по порядку, YYYY-MM-DD
	Rows     []Row

	Steps      []*int // по дням; nil — нет данных
	StepsFloor int

	Wake       []*int // отклонение подъёма от якоря, минуты; nil — нет данных
	WakeAnchor string // HH:MM
	Tolerance  int    // норма ± минут
}

const (
	width  = 720.0
	pad    = 28.0
	labelW = 150.0
	maxH   = 2400
)

var weekdays = [...]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}

// PNG рисует период.
func (p Period) PNG() ([]byte, error) {
	if len(p.Dates) == 0 {
		return nil, fmt.Errorf("пустой период")
	}
	c := newCanvas(width, maxH)
	y := header(c, p.Title, p.Subtitle)
	l := newLayout(len(p.Dates))

	if len(p.Rows) > 0 {
		y = p.grid(c, l, y)
	}
	if known(p.Steps) {
		y = p.steps(c, l, y+28)
	}
	if known(p.Wake) {
		y = p.wake(c, l, y+28)
	}
	return c.png(y + pad)
}

func header(c *canvas, title, sub string) float64 {
	y := pad + 20
	c.text(title, pad, y, bold, 21, ink, left)
	if sub != "" {
		y += 24
		c.text(c.fit(sub, regular, 13.5, width-2*pad), pad, y, regular, 13.5, ink2, left)
	}
	return y + 30
}

func known(v []*int) bool {
	for _, x := range v {
		if x != nil {
			return true
		}
	}
	return false
}

// layout — колонки дней под сеткой и графиками.
type layout struct {
	n          int
	x0, colW   float64
	cellW      float64
	cellH, gap float64
	dense      bool // месяц: подписи реже, без чисел над столбцами
}

func newLayout(n int) layout {
	l := layout{n: n, x0: pad + labelW}
	l.colW = (width - pad - l.x0) / float64(n)
	l.dense = n > 10
	l.gap = 4
	if l.dense {
		l.gap = 2
	}
	l.cellW = min(l.colW-l.gap, 44)
	l.cellH = min(22, l.cellW)
	return l
}

func (l layout) mid(i int) float64 { return l.x0 + l.colW*(float64(i)+0.5) }
func (l layout) right() float64    { return width - pad }

func day(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// dayLabels — подписи дней по оси x; y — базовая линия.
func (p Period) dayLabels(c *canvas, l layout, y float64) {
	for i, d := range p.Dates {
		t := day(d)
		if !l.dense {
			c.text(weekdays[t.Weekday()], l.mid(i), y, medium, 12, ink2, center)
			c.text(strconv.Itoa(t.Day()), l.mid(i), y+15, regular, 11, ink3, center)
			continue
		}
		if t.Day() == 1 || t.Day()%5 == 0 {
			c.text(strconv.Itoa(t.Day()), l.mid(i), y, regular, 11, ink3, center)
		}
	}
}

func (l layout) labelsH() float64 {
	if l.dense {
		return 18
	}
	return 34
}

func section(c *canvas, title, note string, y float64) float64 {
	c.text(title, pad, y, bold, 15, ink, left)
	if note != "" {
		c.text(note, width-pad, y, regular, 13, ink2, right)
	}
	return y + 16
}

var cellColor = map[Cell]color.RGBA{Goal: blue, Floor: blueLight, Miss: surface2}

func (c *canvas) cell(x, y, w, h float64, v Cell) {
	r := min(4, w/4)
	if v == None {
		c.rect(x, y, w, h, r, true, true, grid)
		c.rect(x+1, y+1, w-2, h-2, max(r-1, 0), true, true, surface)
		return
	}
	c.rect(x, y, w, h, r, true, true, cellColor[v])
}

func (p Period) grid(c *canvas, l layout, y float64) float64 {
	y = section(c, "Привычки", "", y)
	p.dayLabels(c, l, y+10)
	y += l.labelsH()
	rowH := l.cellH + 8
	if l.dense {
		rowH = l.cellH + 5
	}
	for _, r := range p.Rows {
		c.text(c.fit(r.Name, regular, 13, labelW-14), pad, y+rowH/2+4.5, regular, 13, ink, left)
		for i, v := range r.Cells {
			if i >= l.n {
				break
			}
			c.cell(l.mid(i)-l.cellW/2, y+(rowH-l.cellH)/2, l.cellW, l.cellH, v)
		}
		y += rowH
		if r.Gap {
			y += 8
		}
	}
	y += 14
	x := l.x0
	for _, it := range []struct {
		v    Cell
		name string
	}{{Goal, "цель"}, {Floor, "пол"}, {Miss, "пропуск"}, {None, "нет отметки"}} {
		c.cell(x, y-10, 14, 12, it.v)
		c.text(it.name, x+20, y, regular, 12, ink2, left)
		x += 20 + c.measure(it.name, regular, 12) + 18
	}
	return y
}

// niceStep — круглый шаг сетки, чтобы до top было около n делений.
func niceStep(top float64, n int) float64 {
	raw := top / float64(n)
	if raw <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw {
			return m * mag
		}
	}
	return 10 * mag
}

func (p Period) steps(c *canvas, l layout, y float64) float64 {
	sum, n := 0, 0
	top := float64(p.StepsFloor)
	for _, s := range p.Steps {
		if s != nil {
			sum += *s
			n++
			top = max(top, float64(*s))
		}
	}
	y = section(c, "Шаги", "в среднем "+thousands(int64(sum/n)), y)
	y += 14
	const h = 120.0
	step := niceStep(top, 3)
	top = math.Ceil(top*1.05/step) * step
	base := y + h
	yOf := func(v float64) float64 { return base - v/top*h }
	for v := 0.0; v <= top+0.5; v += step {
		c.hline(l.x0, l.right(), yOf(v), grid)
		c.text(compact(int64(v)), l.x0-10, yOf(v)+4, regular, 11, ink3, right)
	}
	if p.StepsFloor > 0 {
		fy := yOf(float64(p.StepsFloor))
		c.box(l.x0, fy-0.75, l.right()-l.x0, 1.5, ink2)
		label := "пол " + thousands(int64(p.StepsFloor))
		c.text(label, pad, fy+4, medium, 11.5, ink2, left)
	}
	barW := min(l.colW*0.62, 24)
	for i, s := range p.Steps {
		if s == nil || i >= l.n {
			continue
		}
		col := blue
		if *s < p.StepsFloor {
			col = belowGray
		}
		top := yOf(float64(*s))
		c.rect(l.mid(i)-barW/2, top, barW, base-top, min(4, barW/3), true, false, col)
		if !l.dense {
			c.label(compact(int64(*s)), l.mid(i), top-6, medium, 11, ink2, center)
		}
	}
	p.dayLabels(c, l, base+18)
	y = base + 18 + l.labelsH()
	return legend(c, l.x0, y, []key{{blue, "пол взят", false}, {belowGray, "ниже пола", false}})
}

type key struct {
	col  color.Color
	name string
	dot  bool
}

func legend(c *canvas, x, y float64, keys []key) float64 {
	for _, k := range keys {
		if k.dot {
			c.circle(x+6, y-4, 5, k.col)
		} else {
			c.rect(x, y-10, 12, 12, 3, true, true, k.col)
		}
		c.text(k.name, x+20, y, regular, 12, ink2, left)
		x += 20 + c.measure(k.name, regular, 12) + 18
	}
	return y
}

func (p Period) wake(c *canvas, l layout, y float64) float64 {
	in, n, lim := 0, 0, p.Tolerance+30
	for _, d := range p.Wake {
		if d == nil {
			continue
		}
		n++
		if abs(*d) <= p.Tolerance {
			in++
		}
		lim = max(lim, abs(*d)+15)
	}
	lim = min(lim, 720)
	tick := 30 // не больше шести делений на всю высоту
	for _, t := range []int{30, 60, 120, 180, 240} {
		tick = t
		if 2*((lim+t-1)/t) <= 6 {
			break
		}
	}
	lim = (lim + tick - 1) / tick * tick
	y = section(c, "Подъём", fmt.Sprintf("в норме %d из %d", in, n), y)
	y += 14
	const h = 130.0
	mid := y + h/2
	yOf := func(d int) float64 {
		d = max(-lim, min(lim, d))
		return mid + float64(d)/float64(lim)*h/2
	}
	c.box(l.x0, yOf(-p.Tolerance), l.right()-l.x0, yOf(p.Tolerance)-yOf(-p.Tolerance), blueWash)
	anchor, _ := time.Parse("15:04", p.WakeAnchor)
	for d := -lim; d <= lim; d += tick {
		col := grid
		if d == 0 {
			col = planGray
		}
		c.hline(l.x0, l.right(), yOf(d), col)
		c.text(anchor.Add(time.Duration(d)*time.Minute).Format("15:04"), l.x0-10, yOf(d)+4, regular, 11, ink3, right)
	}
	r := 5.5
	if l.dense {
		r = 4
	}
	var pts [][2]float64
	flush := func() {
		if len(pts) > 1 {
			c.polyline(pts, 1.5, planGray)
		}
		pts = pts[:0]
	}
	for i, d := range p.Wake {
		if d == nil {
			flush()
			continue
		}
		pts = append(pts, [2]float64{l.mid(i), yOf(*d)})
	}
	flush()
	for i, d := range p.Wake {
		if d == nil || i >= l.n {
			continue
		}
		col := blue
		if abs(*d) > p.Tolerance {
			col = orange
		}
		c.dot(l.mid(i), yOf(*d), r, col)
	}
	base := y + h
	p.dayLabels(c, l, base+18)
	y = base + 18 + l.labelsH()
	return legend(c, l.x0, y, []key{{blue, "в норме", true}, {orange, fmt.Sprintf("дальше ±%d мин", p.Tolerance), true}, {blueWash, "окно нормы", false}})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// thousands — 12 345 с неразрывным пробелом.
func thousands(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v, 10)
	var out []byte
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	if neg {
		return "−" + string(out)
	}
	return string(out)
}

// compact — 8,2 тыс, 1,75 млн; до тысячи — как есть.
func compact(v int64) string {
	a := math.Abs(float64(v))
	sign := ""
	if v < 0 {
		sign = "−"
	}
	var s string
	switch {
	case a >= 1e6:
		s = trim(strconv.FormatFloat(a/1e6, 'f', 2, 64)) + " млн"
	case a >= 1e4:
		s = strconv.FormatFloat(math.Round(a/1e3), 'f', 0, 64) + " тыс"
	case a >= 1e3:
		s = trim(strconv.FormatFloat(a/1e3, 'f', 1, 64)) + " тыс"
	default:
		s = strconv.FormatInt(int64(a), 10)
	}
	return sign + s
}

func trim(s string) string {
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	s = trimSuffix(s, ".")
	for i := range len(s) {
		if s[i] == '.' {
			return s[:i] + "," + s[i+1:]
		}
	}
	return s
}

func trimSuffix(s, suf string) string {
	if len(s) >= len(suf) && s[len(s)-len(suf):] == suf {
		return s[:len(s)-len(suf)]
	}
	return s
}
