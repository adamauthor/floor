// Package chart рисует графики для чата: PNG целиком из кода, без внешних сервисов.
// Холст в логических пикселях, растр в scale раз крупнее — чтобы Telegram не мылил.
package chart

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const scale = 2

// Палитра: светлая поверхность, один синий ряд для величин, серые для служебного.
var (
	surface   = hex(0xfcfcfb)
	surface2  = hex(0xefeeea) // пропуск, дорожки
	grid      = hex(0xe4e3de)
	ink       = hex(0x0b0b0b)
	ink2      = hex(0x52514e)
	ink3      = hex(0x8a8984)
	blue      = hex(0x2a78d6) // цель, факт, норма
	blueLight = hex(0x86b6ef) // пол
	blueWash  = color.NRGBA{0x2a, 0x78, 0xd6, 0x24}
	orange    = hex(0xeb6834) // вне нормы
	planGray  = hex(0xb3b2ac)
	belowGray = hex(0xcfcec8)
)

func hex(v uint32) color.RGBA {
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

type weight int

const (
	regular weight = iota
	medium
	bold
)

var (
	fontsOnce sync.Once
	fonts     [3]*sfnt.Font
)

func loadFonts() {
	for i, src := range [][]byte{goregular.TTF, gomedium.TTF, gobold.TTF} {
		f, err := opentype.Parse(src)
		if err != nil {
			panic(err) // шрифты встроены в бинарник: ошибка — сломанная сборка
		}
		fonts[i] = f
	}
}

type faceKey struct {
	w    weight
	size float64
}

type canvas struct {
	img   *image.RGBA
	z     vector.Rasterizer
	faces map[faceKey]font.Face
	buf   sfnt.Buffer
}

func newCanvas(w, h int) *canvas {
	fontsOnce.Do(loadFonts)
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, w*scale, h*scale)), faces: map[faceKey]font.Face{}}
	draw.Draw(c.img, c.img.Bounds(), image.NewUniform(surface), image.Point{}, draw.Src)
	return c
}

// png кодирует верхние h логических пикселей холста.
func (c *canvas) png(h float64) ([]byte, error) {
	for _, f := range c.faces {
		_ = f.Close()
	}
	r := c.img.Bounds()
	r.Max.Y = min(r.Max.Y, int(math.Ceil(h))*scale)
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, c.img.SubImage(r)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// path — фигура в логических координатах; fill закрашивает её.
type path struct {
	pts  [][2]float32 // команды: move, line, cubic (по три точки)
	ops  []byte
	minX float32
	minY float32
	maxX float32
	maxY float32
}

func newPath() *path {
	return &path{minX: math.MaxFloat32, minY: math.MaxFloat32, maxX: -math.MaxFloat32, maxY: -math.MaxFloat32}
}

func (p *path) add(op byte, pts ...[2]float32) {
	p.ops = append(p.ops, op)
	for _, q := range pts {
		q = [2]float32{q[0] * scale, q[1] * scale}
		p.minX, p.minY = min(p.minX, q[0]), min(p.minY, q[1])
		p.maxX, p.maxY = max(p.maxX, q[0]), max(p.maxY, q[1])
		p.pts = append(p.pts, q)
	}
}

func (p *path) move(x, y float64) { p.add('M', [2]float32{float32(x), float32(y)}) }
func (p *path) line(x, y float64) { p.add('L', [2]float32{float32(x), float32(y)}) }
func (p *path) cubic(x1, y1, x2, y2, x, y float64) {
	p.add('C', [2]float32{float32(x1), float32(y1)}, [2]float32{float32(x2), float32(y2)}, [2]float32{float32(x), float32(y)})
}

func (c *canvas) fill(p *path, col color.Color) {
	if len(p.ops) == 0 {
		return
	}
	b := image.Rect(int(math.Floor(float64(p.minX))), int(math.Floor(float64(p.minY))),
		int(math.Ceil(float64(p.maxX)))+1, int(math.Ceil(float64(p.maxY)))+1).Intersect(c.img.Bounds())
	if b.Empty() {
		return
	}
	ox, oy := float32(b.Min.X), float32(b.Min.Y)
	c.z.Reset(b.Dx(), b.Dy())
	c.z.DrawOp = draw.Over
	i := 0
	for _, op := range p.ops {
		switch op {
		case 'M':
			c.z.MoveTo(p.pts[i][0]-ox, p.pts[i][1]-oy)
			i++
		case 'L':
			c.z.LineTo(p.pts[i][0]-ox, p.pts[i][1]-oy)
			i++
		case 'C':
			c.z.CubeTo(p.pts[i][0]-ox, p.pts[i][1]-oy, p.pts[i+1][0]-ox, p.pts[i+1][1]-oy, p.pts[i+2][0]-ox, p.pts[i+2][1]-oy)
			i += 3
		}
	}
	c.z.ClosePath()
	c.z.Draw(c.img, b, image.NewUniform(col), image.Point{})
}

const kappa = 0.5523 // дуга четверти круга кубической кривой

// rect — прямоугольник со скруглением r: сверху top, снизу bottom.
func (c *canvas) rect(x, y, w, h, r float64, top, bottom bool, col color.Color) {
	if w <= 0 || h <= 0 {
		return
	}
	r = min(r, w/2, h/2)
	rt, rb := 0.0, 0.0
	if top {
		rt = r
	}
	if bottom {
		rb = r
	}
	p := newPath()
	p.move(x+rt, y)
	p.line(x+w-rt, y)
	p.cubic(x+w-rt+rt*kappa, y, x+w, y+rt-rt*kappa, x+w, y+rt)
	p.line(x+w, y+h-rb)
	p.cubic(x+w, y+h-rb+rb*kappa, x+w-rb+rb*kappa, y+h, x+w-rb, y+h)
	p.line(x+rb, y+h)
	p.cubic(x+rb-rb*kappa, y+h, x, y+h-rb+rb*kappa, x, y+h-rb)
	p.line(x, y+rt)
	p.cubic(x, y+rt-rt*kappa, x+rt-rt*kappa, y, x+rt, y)
	c.fill(p, col)
}

func (c *canvas) box(x, y, w, h float64, col color.Color) { c.rect(x, y, w, h, 0, false, false, col) }

func (c *canvas) circle(cx, cy, r float64, col color.Color) {
	k := r * kappa
	p := newPath()
	p.move(cx+r, cy)
	p.cubic(cx+r, cy+k, cx+k, cy+r, cx, cy+r)
	p.cubic(cx-k, cy+r, cx-r, cy+k, cx-r, cy)
	p.cubic(cx-r, cy-k, cx-k, cy-r, cx, cy-r)
	p.cubic(cx+k, cy-r, cx+r, cy-k, cx+r, cy)
	c.fill(p, col)
}

// dot — точка с кольцом цвета поверхности: не сливается с линиями под ней.
func (c *canvas) dot(cx, cy, r float64, col color.Color) {
	c.circle(cx, cy, r+1.5, surface)
	c.circle(cx, cy, r, col)
}

// segment — отрезок толщины w со скруглёнными концами.
func (c *canvas) segment(x1, y1, x2, y2, w float64, col color.Color) {
	dx, dy := x2-x1, y2-y1
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	nx, ny := -dy/l*w/2, dx/l*w/2
	p := newPath()
	p.move(x1+nx, y1+ny)
	p.line(x2+nx, y2+ny)
	p.line(x2-nx, y2-ny)
	p.line(x1-nx, y1-ny)
	c.fill(p, col)
}

// polyline — ломаная толщины w со скруглёнными стыками.
func (c *canvas) polyline(pts [][2]float64, w float64, col color.Color) {
	for i := 1; i < len(pts); i++ {
		c.segment(pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1], w, col)
	}
	for _, q := range pts {
		c.circle(q[0], q[1], w/2, col)
	}
}

// hline — волосяная горизонталь.
func (c *canvas) hline(x1, x2, y float64, col color.Color) { c.box(x1, y-0.5, x2-x1, 1, col) }

func (c *canvas) face(w weight, size float64) font.Face {
	k := faceKey{w, size}
	if f, ok := c.faces[k]; ok {
		return f
	}
	f, err := opentype.NewFace(fonts[w], &opentype.FaceOptions{Size: size * scale, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	c.faces[k] = f
	return f
}

// clean убирает символы, которых нет в шрифте (эмодзи, селекторы вариантов).
func (c *canvas) clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		if i, err := fonts[regular].GlyphIndex(&c.buf, r); err == nil && i != 0 || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

type align int

const (
	left align = iota
	center
	right
)

// measure — ширина строки в логических пикселях.
func (c *canvas) measure(s string, w weight, size float64) float64 {
	return float64(font.MeasureString(c.face(w, size), c.clean(s))) / 64 / scale
}

// text пишет строку; y — базовая линия.
func (c *canvas) text(s string, x, y float64, w weight, size float64, col color.Color, a align) {
	s = c.clean(s)
	f := c.face(w, size)
	width := float64(font.MeasureString(f, s)) / 64
	px := x * scale
	switch a {
	case center:
		px -= width / 2
	case right:
		px -= width
	}
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: f,
		Dot: fixed.Point26_6{X: fixed.Int26_6(px * 64), Y: fixed.Int26_6(y * scale * 64)}}
	d.DrawString(s)
}

// label — подпись на подложке цвета поверхности: не сливается с линиями под ней.
func (c *canvas) label(s string, x, y float64, w weight, size float64, col color.Color, a align) {
	tw := c.measure(s, w, size)
	bx := x
	switch a {
	case center:
		bx -= tw / 2
	case right:
		bx -= tw
	}
	c.rect(bx-3, y-size*0.82, tw+6, size*1.08, 3, true, true, surface)
	c.text(s, x, y, w, size, col, a)
}

// fit обрезает строку многоточием, чтобы она влезла в width.
func (c *canvas) fit(s string, w weight, size, width float64) string {
	s = c.clean(s)
	if c.measure(s, w, size) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if t := strings.TrimSpace(string(r)) + "…"; c.measure(t, w, size) <= width {
			return t
		}
	}
	return "…"
}
