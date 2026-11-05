package chart

import (
	"fmt"
	"math"
	"strings"
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
	if len(spec.Title) > 512 || len(table.Rows) == 0 || len(table.Rows) > MaxRows || spec.X < 0 || spec.X >= len(table.Columns) || len(spec.Y) < 1 || len(spec.Y) > len(palette) {
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
		column := table.Columns[col]
		if column.DependencyKind == "constant_label" || column.DependencyKind == "mixed_constant_values" || column.DependencyKind == "source_metadata" || column.DependencyKind == "unknown" {
			return nil, fmt.Errorf("Y column has no verified source-value dependency")
		}
		if column.UnitMetadata != nil && (column.UnitMetadata.Status == "mixed" || len(spec.Y) > 1 && column.UnitMetadata.Status == "unknown") {
			return nil, fmt.Errorf("mixed or unconfirmed result units require separate source selections; no inferred shared scale")
		}
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
	xValues := make([]string, len(table.Rows))
	for i, row := range table.Rows {
		if len(row) != len(table.Columns) || strings.TrimSpace(row[spec.X]) == "" {
			return nil, fmt.Errorf("missing X or inconsistent source row at %d", i)
		}
		xValues[i] = row[spec.X]
	}
	xs, xmin, xmax, err := xAxisCoordinates(xValues, spec.XType)
	if err != nil {
		return nil, err
	}
	series := make([][]point, len(spec.Y))
	ymin, ymax := math.Inf(1), math.Inf(-1)
	valid := 0
	for j, col := range spec.Y {
		for i, row := range table.Rows {
			p := point{x: xs[i]}
			text := strings.TrimSpace(row[col])
			if text == "" {
				p.missing = true
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
	if ymax == ymin {
		pad := math.Max(math.Abs(ymin)*0.05, 1)
		ymin -= pad
		ymax += pad
	}
	// Reject overflow rather than producing invalid pixel coordinates.
	if math.IsInf(ymax-ymin, 0) {
		return nil, fmt.Errorf("numeric range exceeds renderer limits")
	}
	return renderPNG(table, spec, series, xs, xmin, xmax, ymin, ymax)
}
