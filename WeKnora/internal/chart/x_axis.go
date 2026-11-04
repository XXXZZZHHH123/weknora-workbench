package chart

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// EligibleXTypes reports encodings the renderer can represent for every
// selected original X value. It does not select business-axis semantics or
// infer dates, missing values, or new observations.
func EligibleXTypes(values []string) []string {
	var eligible []string
	for _, axisType := range []string{"category", "numeric", "time"} {
		if _, _, _, err := xAxisCoordinates(values, axisType); err == nil {
			eligible = append(eligible, axisType)
		}
	}
	return eligible
}

// Rendering and review eligibility share exactly the same value parsers,
// missing-value rules and X-range checks; neither path may invent a date.
func xAxisCoordinates(values []string, axisType string) ([]float64, float64, float64, error) {
	if len(values) == 0 || len(values) > MaxRows {
		return nil, 0, 0, fmt.Errorf("X observations are missing or exceed renderer limits")
	}
	if axisType != "category" && axisType != "numeric" && axisType != "time" {
		return nil, 0, 0, fmt.Errorf("x_type must be category, numeric or time")
	}
	xs := make([]float64, len(values))
	xmin, xmax := math.Inf(1), math.Inf(-1)
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, 0, 0, fmt.Errorf("missing X or inconsistent source row at %d", i)
		}
		x := float64(i)
		switch axisType {
		case "numeric":
			var ok bool
			x, ok = Number(value)
			if !ok {
				return nil, 0, 0, fmt.Errorf("X column is not numeric at row %d", i)
			}
		case "time":
			date, err := time.Parse("2006-01-02", value)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("time X requires actual ISO dates; no inferred dates (row %d)", i)
			}
			x = float64(date.Unix())
		}
		xs[i] = x
		xmin = math.Min(xmin, x)
		xmax = math.Max(xmax, x)
	}
	if xmax == xmin {
		pad := math.Max(math.Abs(xmin)*0.05, 0.5)
		xmin -= pad
		xmax += pad
	}
	if math.IsInf(xmax-xmin, 0) {
		return nil, 0, 0, fmt.Errorf("numeric range exceeds renderer limits")
	}
	return xs, xmin, xmax, nil
}
