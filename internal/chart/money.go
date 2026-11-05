package chart

import (
	"fmt"
	"math"
	"time"
)

// Point — значение на дату.
type Point struct {
	Date  string
	Value int64
}

// Panel — одна линия плана денег: план по дням и факт по сверкам.
type Panel struct {
	Title  string
	Note   string  // справа от заголовка
	Plan   []Point // ступеньками: переводы приходят в конкретные дни
	Actual []Point
	Target *int64 // цель по сумме; nil — нет
	By     string // срок цели; пусто — нет
}

// Money — графики плана денег, по панели на линию (одна шкала на панель).
type Money struct {
	Title    string
	Subtitle string
	From, To string
	Panels   []Panel
}

var monthsShort = [...]string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

// PNG рисует план денег.
func (m Money) PNG() ([]byte, error) {
	if len(m.Panels) == 0 || m.From >= m.To {
		return nil, fmt.Errorf("нечего рисовать")
	}
	c := newCanvas(width, maxH)
	y := header(c, m.Title, m.Subtitle)
	y = legendLines(c, pad, y-6)
	for _, p := range m.Panels {
		y = m.panel(c, p, y+30)
	}
	return c.png(y + pad)
}

func legendLines(c *canvas, x, y float64) float64 {
	c.segment(x, y-4, x+18, y-4, 2, planGray)
	c.text("план", x+26, y, regular, 12, ink2, left)
	x += 26 + c.measure("план", regular, 12) + 22
	c.segment(x, y-4, x+18, y-4, 2, blue)
	c.dot(x+9, y-4, 4, blue)
	c.text("факт по сверкам", x+26, y, regular, 12, ink2, left)
	return y
}

func (m Money) panel(c *canvas, p Panel, y float64) float64 {
	y = section(c, p.Title, p.Note, y)
	y += 16
	const h = 150.0
	x0, x1 := pad+70, width-pad-8
	from, to := day(m.From), day(m.To)
	span := to.Sub(from).Hours()
	xOf := func(d string) float64 {
		return x0 + day(d).Sub(from).Hours()/span*(x1-x0)
	}
	top := 0.0
	for _, s := range [][]Point{p.Plan, p.Actual} {
		for _, q := range s {
			top = max(top, float64(q.Value))
		}
	}
	if p.Target != nil {
		top = max(top, float64(*p.Target))
	}
	if top <= 0 {
		top = 1
	}
	step := niceStep(top, 4)
	top = math.Ceil(top*1.04/step) * step
	base := y + h
	yOf := func(v int64) float64 { return base - float64(max(v, 0))/top*h }

	for v := 0.0; v <= top+0.5; v += step {
		c.hline(x0, x1, yOf(int64(v)), grid)
		c.text(compact(int64(v)), x0-10, yOf(int64(v))+4, regular, 11, ink3, right)
	}
	// Месяцы по оси x.
	for t := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !t.After(to); t = t.AddDate(0, 1, 0) {
		if t.Before(from) {
			continue
		}
		x := xOf(t.Format(time.DateOnly))
		c.box(x-0.5, base, 1, 5, planGray)
		c.text(monthsShort[t.Month()-1], x+4, base+18, regular, 11, ink3, left)
	}
	if p.By != "" && p.By >= m.From && p.By <= m.To {
		x := xOf(p.By)
		c.box(x-0.5, y-4, 1, h+4, planGray)
		t := day(p.By)
		c.text(fmt.Sprintf("срок %d %s", t.Day(), monthsShort[t.Month()-1]), x-6, y+8, medium, 11, ink2, right)
	}
	if p.Target != nil {
		ty := yOf(*p.Target)
		c.box(x0, ty-0.75, x1-x0, 1.5, ink2)
		c.label("цель "+compact(*p.Target), x0+6, ty-6, medium, 11.5, ink2, left)
	}

	// План — ступеньки по дням.
	var pts [][2]float64
	for i, q := range p.Plan {
		x, yy := xOf(q.Date), yOf(q.Value)
		if i > 0 && pts[len(pts)-1][1] != yy {
			pts = append(pts, [2]float64{x, pts[len(pts)-1][1]})
		}
		pts = append(pts, [2]float64{x, yy})
	}
	c.polyline(pts, 2, planGray)

	pts = pts[:0]
	for _, q := range p.Actual {
		pts = append(pts, [2]float64{xOf(q.Date), yOf(q.Value)})
	}
	if len(pts) > 1 {
		c.polyline(pts, 2.5, blue)
	}
	for _, q := range pts {
		c.dot(q[0], q[1], 4.5, blue)
	}
	if n := len(p.Actual); n > 0 {
		last := pts[n-1]
		label := compact(p.Actual[n-1].Value)
		w := c.measure(label, bold, 12.5)
		x, a := last[0]+10, left
		if x+w > width-pad {
			x, a = last[0]-10, right
		}
		ly := last[1] - 10
		if ly < y+4 {
			ly = last[1] + 20
		}
		c.label(label, x, ly, bold, 12.5, ink, a)
	}
	return base + 18
}
