package chart

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

type Spec struct {
	Type  string `json:"chart_type"`
	X     int    `json:"x_column"`
	Y     []int  `json:"y_columns"`
	XType string `json:"x_type"`
	Title string `json:"title"`
}
type point struct {
	x, y    float64
	missing bool
}

var colors = []string{"#2563eb", "#d97706", "#059669", "#dc2626", "#7c3aed", "#0891b2"}

func Render(table Table, spec Spec) ([]byte, error) {
	if spec.Type != "line" && spec.Type != "bar" && spec.Type != "scatter" {
		return nil, fmt.Errorf("chart_type must be line, bar or scatter")
	}
	if spec.XType == "" {
		spec.XType = "category"
	}
	if spec.XType != "category" && spec.XType != "numeric" && spec.XType != "time" {
		return nil, fmt.Errorf("x_type must be category, numeric or time")
	}
	if len(spec.Title) > 512 || len(table.Rows) == 0 || len(table.Rows) > MaxRows || spec.X < 0 || spec.X >= len(table.Columns) || len(spec.Y) < 1 || len(spec.Y) > len(colors) {
		return nil, fmt.Errorf("invalid chart columns, title or row count")
	}
	if table.ID == "" {
		return nil, fmt.Errorf("chart requires a sealed source")
	}
	seen := map[int]bool{}
	unit := ""
	knownUnit := false
	for _, col := range spec.Y {
		if col < 0 || col >= len(table.Columns) || col == spec.X || seen[col] {
			return nil, fmt.Errorf("select distinct real X/Y columns")
		}
		seen[col] = true
		u := table.Columns[col].Unit
		if len(spec.Y) > 1 {
			if !knownUnit {
				unit = u
				knownUnit = true
			} else if unit != u {
				return nil, fmt.Errorf("different source units require separate charts; no automatic conversion")
			}
		}
	}
	n := len(table.Rows)
	xs := make([]float64, n)
	xmin, xmax := math.Inf(1), math.Inf(-1)
	for i, row := range table.Rows {
		if len(row) != len(table.Columns) || strings.TrimSpace(row[spec.X]) == "" {
			return nil, fmt.Errorf("missing X or inconsistent source row at %d", i)
		}
		x := float64(i)
		switch spec.XType {
		case "numeric":
			var ok bool
			x, ok = Number(row[spec.X])
			if !ok {
				return nil, fmt.Errorf("X column is not numeric at row %d", i)
			}
		case "time":
			date, err := time.Parse("2006-01-02", row[spec.X])
			if err != nil {
				return nil, fmt.Errorf("time X requires actual ISO dates; no inferred dates (row %d)", i)
			}
			x = float64(date.Unix())
		}
		xs[i] = x
		xmin = math.Min(xmin, x)
		xmax = math.Max(xmax, x)
	}
	series := make([][]point, len(spec.Y))
	ymin, ymax := math.Inf(1), math.Inf(-1)
	valid, missing := 0, 0
	for j, col := range spec.Y {
		for i, row := range table.Rows {
			p := point{x: xs[i]}
			text := strings.TrimSpace(row[col])
			if text == "" {
				p.missing = true
				missing++
			} else {
				var ok bool
				p.y, ok = Number(text)
				if !ok {
					return nil, fmt.Errorf("Y column contains a nonnumeric value at row %d column %d; nothing was silently dropped", i, col)
				}
				ymin = math.Min(ymin, p.y)
				ymax = math.Max(ymax, p.y)
				valid++
			}
			series[j] = append(series[j], p)
		}
	}
	if valid == 0 {
		return nil, fmt.Errorf("selected Y columns contain no numeric observations")
	}
	if spec.Type == "bar" {
		ymin = math.Min(ymin, 0)
		ymax = math.Max(ymax, 0)
	}
	if xmax == xmin {
		xmin -= 0.5
		xmax += 0.5
	}
	if ymax == ymin {
		pad := math.Max(math.Abs(ymin)*0.05, 1)
		ymin -= pad
		ymax += pad
	}
	// Reject overflow rather than producing invalid SVG coordinates.
	if math.IsInf(xmax-xmin, 0) || math.IsInf(ymax-ymin, 0) {
		return nil, fmt.Errorf("numeric range exceeds renderer limits")
	}
	const left = 95.0
	const top = 90.0
	const width = 850.0
	const height = 350.0
	px := func(x float64) float64 { return left + (x-xmin)/(xmax-xmin)*width }
	py := func(y float64) float64 { return top + height - (y-ymin)/(ymax-ymin)*height }
	var b bytes.Buffer
	b.WriteString(`<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src 'none'; base-uri 'none'; form-action 'none'"><title>`)
	b.WriteString(html.EscapeString(spec.Title))
	b.WriteString(`</title><style>body{font-family:system-ui,sans-serif;margin:20px;color:#172033;background:white}svg{width:100%;max-width:1100px;height:auto}text{font-family:system-ui,sans-serif;fill:#334155}table{border-collapse:collapse;font-size:13px}th,td{border:1px solid #cbd5e1;padding:5px 9px}pre{white-space:pre-wrap;overflow-wrap:anywhere}summary{cursor:pointer} .notice{padding:12px;background:#f1f5f9;max-width:1040px}</style></head><body><h1>`)
	b.WriteString(html.EscapeString(spec.Title))
	b.WriteString(`</h1><p class="notice">依据下列来源数据确定性绘图；并非原文原图或 AI 生成的数值。未知单位不补造，空值留空、不补零。绘图范围仅为本次工具实际返回的记录。</p>`)
	if table.Truncated {
		b.WriteString(`<p class="notice"><strong>来源结果已截断：本图仅显示已返回部分，不代表完整范围或完整走势。</strong></p>`)
	}
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1080 570" role="img"><title>`)
	b.WriteString(html.EscapeString(spec.Title))
	b.WriteString(`</title><rect width="1080" height="570" fill="white"/>`)
	fmt.Fprintf(&b, `<path d="M %.2f %.2f V %.2f H %.2f" fill="none" stroke="#64748b"/>`, left, top, top+height, left+width)
	for i := 0; i <= 5; i++ {
		y := ymin + (ymax-ymin)*float64(i)/5
		yy := py(y)
		fmt.Fprintf(&b, `<line x1="%.2f" x2="%.2f" y1="%.2f" y2="%.2f" stroke="#e2e8f0"/><text x="85" y="%.2f" text-anchor="end" font-size="12">%s</text>`, left, left+width, yy, yy, yy+4, html.EscapeString(fmt.Sprintf("%.6g", y)))
	}
	for k, col := range spec.Y {
		color := colors[k]
		var segment strings.Builder
		flush := func() {
			if segment.Len() > 0 {
				fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2"/>`, segment.String(), color)
				segment.Reset()
			}
		}
		for i, p := range series[k] {
			if p.missing {
				flush()
				continue
			}
			x, y := px(p.x), py(p.y)
			tooltip := fmt.Sprintf("%s: %s | %s: %s", table.Columns[spec.X].Name, table.Rows[i][spec.X], table.Columns[col].Name, table.Rows[i][col])
			switch spec.Type {
			case "line":
				command := "L"
				if segment.Len() == 0 {
					command = "M"
				}
				fmt.Fprintf(&segment, "%s %.2f %.2f ", command, x, y)
			case "bar":
				w := math.Min(36, width/float64(n)/float64(len(spec.Y)+1))
				x += float64(k)*w - float64(len(spec.Y)-1)*w/2
				zero := py(0)
				fmt.Fprintf(&b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"><title>%s</title></rect>`, x-w/2, math.Min(y, zero), w, math.Abs(zero-y), color, html.EscapeString(tooltip))
			}
			if spec.Type != "bar" {
				fmt.Fprintf(&b, `<circle cx="%.2f" cy="%.2f" r="3" fill="%s"><title>%s</title></circle>`, x, y, color, html.EscapeString(tooltip))
			}
		}
		flush()
		label := table.Columns[col].Name
		u := table.Columns[col].Unit
		if u == "" {
			u = "单位未确认"
		}
		label += " [" + u + "]"
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="12" height="12" fill="%s"/><text x="%d" y="%d" font-size="13">%s</text>`, 100+(k%3)*310, 500+(k/3)*27, color, 118+(k%3)*310, 511+(k/3)*27, html.EscapeString(label))
	}
	stride := max(1, (n+7)/8)
	for i, row := range table.Rows {
		if i%stride != 0 && i != n-1 {
			continue
		}
		fmt.Fprintf(&b, `<text x="%.2f" y="460" text-anchor="middle" font-size="11">%s</text>`, px(xs[i]), html.EscapeString(row[spec.X]))
	}
	fmt.Fprintf(&b, `<text x="520" y="483" text-anchor="middle" font-size="13">%s</text></svg><p>实际返回 %d 行；所选 Y 列有效数值 %d 个，空值 %d 个。原始记录顺序保持不变；仅 time/numeric 轴使用真实坐标间距。</p>`, html.EscapeString(table.Columns[spec.X].Name), n, valid, missing)
	b.WriteString(`<h2>来源与范围</h2><pre>`)
	b.WriteString(html.EscapeString(table.Provenance + "\n" + table.Notice + "\nsource_id: " + table.ID))
	b.WriteString(`</pre><details><summary>核对绘图数据（真实返回值）</summary><table><thead><tr>`)
	selected := make([]int, len(table.Columns))
	for i := range selected {
		selected[i] = i
	}
	for _, col := range selected {
		b.WriteString("<th>" + html.EscapeString(table.Columns[col].Name) + "</th>")
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, row := range table.Rows {
		b.WriteString("<tr>")
		for _, col := range selected {
			b.WriteString("<td>" + html.EscapeString(row[col]) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString(`</tbody></table></details></body></html>`)
	if b.Len() > MaxHTMLBytes {
		return nil, fmt.Errorf("rendered chart exceeds output budget")
	}
	return b.Bytes(), nil
}
