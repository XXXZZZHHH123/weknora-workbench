package chart

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const ImageWidth, ImageHeight = 1600, 1000
const plotLeft, plotTop, plotWidth, plotHeight = 165.0, 150.0, 1320.0, 565.0

//go:embed fonts/WorkbenchChartCJK-Regular.ttf
var chartFontBytes []byte
var parsedFont *opentype.Font
var fontError error
var fontOnce sync.Once

func chartFace(size float64) (font.Face, error) {
	fontOnce.Do(func() { parsedFont, fontError = opentype.Parse(chartFontBytes) })
	if fontError != nil {
		return nil, fmt.Errorf("chart font unavailable: %w", fontError)
	}
	return opentype.NewFace(parsedFont, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

var ink = color.RGBA{30, 41, 59, 255}
var grid = color.RGBA{226, 232, 240, 255}
var palette = []color.RGBA{{37, 99, 235, 255}, {217, 119, 6, 255}, {5, 150, 105, 255}, {220, 38, 38, 255}, {124, 58, 237, 255}, {8, 145, 178, 255}}

// raster owns faces per render: font.Face implementations contain mutable glyph
// caches and must not be shared by concurrent chart requests.
type raster struct {
	image              *image.RGBA
	title, axis, label font.Face
	batch              *vector.Rasterizer
}

func (r *raster) text(face font.Face, text string, x, y float64, align string, c color.Color) {
	width := font.MeasureString(face, text).Ceil()
	if align == "center" {
		x -= float64(width) / 2
	} else if align == "right" {
		x -= float64(width)
	}
	d := font.Drawer{Dst: r.image, Src: image.NewUniform(c), Face: face, Dot: fixed.P(int(math.Round(x)), int(math.Round(y)))}
	d.DrawString(text)
}
func fit(face font.Face, text string, width int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	if font.MeasureString(face, text).Ceil() <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		s := string(runes) + "…"
		if font.MeasureString(face, s).Ceil() <= width {
			return s
		}
	}
	return ""
}
func (r *raster) polygon(points [][2]float64, c color.Color) {
	if r.batch != nil {
		r.batch.MoveTo(float32(points[0][0]), float32(points[0][1]))
		for _, p := range points[1:] {
			r.batch.LineTo(float32(p[0]), float32(p[1]))
		}
		r.batch.ClosePath()
		return
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range points {
		minX = math.Min(minX, p[0])
		minY = math.Min(minY, p[1])
		maxX = math.Max(maxX, p[0])
		maxY = math.Max(maxY, p[1])
	}
	bounds := image.Rect(int(math.Floor(minX))-1, int(math.Floor(minY))-1, int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1).Intersect(r.image.Bounds())
	if bounds.Empty() {
		return
	}
	// Rasterize only the primitive's bounds; a 2000-row source must not
	// allocate a full-canvas mask for each point or segment.
	z := vector.NewRasterizer(bounds.Dx(), bounds.Dy())
	z.MoveTo(float32(points[0][0]-float64(bounds.Min.X)), float32(points[0][1]-float64(bounds.Min.Y)))
	for _, p := range points[1:] {
		z.LineTo(float32(p[0]-float64(bounds.Min.X)), float32(p[1]-float64(bounds.Min.Y)))
	}
	z.ClosePath()
	z.Draw(r.image, bounds, image.NewUniform(c), image.Point{})
}
func (r *raster) line(x1, y1, x2, y2, width float64, c color.Color) {
	dx, dy := x2-x1, y2-y1
	length := math.Hypot(dx, dy)
	if length == 0 {
		return
	}
	ox, oy := -dy/length*width/2, dx/length*width/2
	r.polygon([][2]float64{{x1 - ox, y1 - oy}, {x2 - ox, y2 - oy}, {x2 + ox, y2 + oy}, {x1 + ox, y1 + oy}}, c)
}
func (r *raster) dot(x, y, radius float64, c color.Color) {
	const segments = 20
	points := make([][2]float64, segments)
	for i := range points {
		angle := 2 * math.Pi * float64(i) / segments
		points[i] = [2]float64{x + radius*math.Cos(angle), y + radius*math.Sin(angle)}
	}
	r.polygon(points, c)
}
func (r *raster) rect(x, y, w, h float64, c color.Color) {
	r.polygon([][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, c)
}

func renderPNG(table Table, spec Spec, series [][]point, xs []float64, xmin, xmax, ymin, ymax float64) ([]byte, error) {
	title, err := chartFace(38)
	if err != nil {
		return nil, err
	}
	defer title.Close()
	axis, err := chartFace(25)
	if err != nil {
		return nil, err
	}
	defer axis.Close()
	label, err := chartFace(22)
	if err != nil {
		return nil, err
	}
	defer label.Close()
	r := raster{image: image.NewRGBA(image.Rect(0, 0, ImageWidth, ImageHeight)), title: title, axis: axis, label: label}
	draw.Draw(r.image, r.image.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	heading := spec.Title
	if strings.TrimSpace(heading) == "" {
		heading = table.Columns[spec.Y[0]].Name
	}
	// Font coverage is checked before painting: unsupported labels never silently
	// turn into tofu boxes which could misidentify a source series.
	labels := []string{heading, table.Columns[spec.X].Name, "部分数据", "单位未确认"}
	for _, c := range spec.Y {
		labels = append(labels, table.Columns[c].Name, table.Columns[c].Unit)
	}
	for _, row := range table.Rows {
		labels = append(labels, row[spec.X])
	}
	for _, s := range labels {
		for _, ch := range s {
			if unicode.IsSpace(ch) || unicode.IsControl(ch) {
				continue
			}
			if _, ok := label.GlyphAdvance(ch); !ok {
				return nil, fmt.Errorf("chart font has no glyph for U+%04X", ch)
			}
		}
	}
	r.text(title, fit(title, heading, 1400), 800, 68, "center", ink)
	if table.Truncated {
		r.text(label, "部分数据", 1485, 113, "right", ink)
	}
	px := func(x float64) float64 { return plotLeft + (x-xmin)/(xmax-xmin)*plotWidth }
	py := func(y float64) float64 { return plotTop + plotHeight - (y-ymin)/(ymax-ymin)*plotHeight }
	for i := 0; i <= 5; i++ {
		y := ymin + (ymax-ymin)*float64(i)/5
		yy := py(y)
		r.line(plotLeft, yy, plotLeft+plotWidth, yy, 1, grid)
		r.text(label, fmt.Sprintf("%.6g", y), plotLeft-18, yy+8, "right", ink)
	}
	r.line(plotLeft, plotTop, plotLeft, plotTop+plotHeight, 1.5, ink)
	r.line(plotLeft, plotTop+plotHeight, plotLeft+plotWidth, plotTop+plotHeight, 1.5, ink)
	// Keep every true data coordinate. Labels may be omitted to avoid overlap;
	// no intermediate date or observation is invented for the axis.
	previousRight := -math.MaxFloat64
	for i, row := range table.Rows {
		text := fit(label, row[spec.X], 190)
		x := px(xs[i])
		w := float64(font.MeasureString(label, text).Ceil())
		if i != len(table.Rows)-1 && x-w/2 < previousRight+18 {
			continue
		}
		if i == len(table.Rows)-1 && x-w/2 < previousRight+18 {
			continue
		}
		r.line(x, plotTop+plotHeight, x, plotTop+plotHeight+7, 1, ink)
		r.text(label, text, x, plotTop+plotHeight+40, "center", ink)
		previousRight = x + w/2
	}
	r.text(axis, fit(axis, table.Columns[spec.X].Name, 1200), plotLeft+plotWidth/2, 810, "center", ink)
	for k, col := range spec.Y {
		c := palette[k]
		// A single bounded mask per series also bounds the cost of unsorted input
		// whose many segments cross the whole plot. All subpaths use one winding.
		r.batch = vector.NewRasterizer(ImageWidth, ImageHeight)
		var previous *point
		// Width also respects real X spacing so adjacent time/numeric bars do not
		// overlap simply because the original observations have irregular gaps.
		barWidth := math.Min(38, plotWidth/float64(len(xs))/float64(len(spec.Y)+1))
		for i := 1; i < len(xs); i++ {
			distance := math.Abs(px(xs[i]) - px(xs[i-1]))
			if distance > 0 {
				barWidth = math.Min(barWidth, distance/float64(len(spec.Y)+1))
			}
		}
		for i, p := range series[k] {
			if p.missing {
				previous = nil
				continue
			}
			x, y := px(p.x), py(p.y)
			if spec.Type == "bar" {
				x += float64(k)*barWidth - float64(len(spec.Y)-1)*barWidth/2
				zero := py(0)
				r.rect(x-barWidth/2, math.Min(y, zero), barWidth, math.Abs(zero-y), c)
			} else {
				if spec.Type == "line" && previous != nil {
					r.line(px(previous.x), py(previous.y), x, y, 3, c)
				}
				r.dot(x, y, 4.5, c)
			}
			previous = &series[k][i]
		}
		r.batch.Draw(r.image, r.image.Bounds(), image.NewUniform(c), image.Point{})
		r.batch = nil
		unit := table.Columns[col].Unit
		if unit == "" {
			unit = "单位未确认"
		}
		name := table.Columns[col].Name + "（" + unit + "）"
		x, y := 180+float64(k%3)*460, 875+float64(k/3)*48
		r.line(x, y-7, x+28, y-7, 4, c)
		r.dot(x+14, y-7, 4, c)
		r.text(label, fit(label, name, 390), x+40, y, "left", ink)
	}
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&out, r.image); err != nil {
		return nil, fmt.Errorf("encode chart PNG: %w", err)
	}
	if out.Len() > MaxPNGBytes {
		return nil, fmt.Errorf("rendered chart exceeds PNG output budget")
	}
	return out.Bytes(), nil
}
