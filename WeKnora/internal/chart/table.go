// Package chart renders bounded source tables without network access or code execution.
package chart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const MaxHTMLBytes = 1024 * 1024
const MaxRows = 2000
const MaxColumns = 40
const MaxSources = 32
const MaxSourceBytes = 1024 * 1024

type SaveRequest struct {
	SessionID, MessageID, ToolCallID, FileName, SourceID string
	HTML                                                 []byte
}
type Delivery struct {
	Handle   string `json:"handle"`
	Index    int    `json:"index"`
	FileName string `json:"file_name"`
}
type Writer interface {
	SaveChart(context.Context, SaveRequest) (*Delivery, error)
}

type Column struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Unit    string `json:"unit,omitempty"`
	Numeric bool   `json:"numeric"`
}
type Table struct {
	ID         string     `json:"source_id"`
	Columns    []Column   `json:"columns"`
	Rows       [][]string `json:"-"`
	Provenance string     `json:"provenance"`
	Notice     string     `json:"notice"`
	Truncated  bool       `json:"source_truncated"`
}

// Seal binds a source handle to exact server-observed rows and provenance.
func (t *Table) Seal(identity string) error {
	if len(t.Rows) == 0 || len(t.Rows) > MaxRows || len(t.Columns) < 2 || len(t.Columns) > MaxColumns || len(t.Provenance) > 16384 || len(t.Notice) > 4096 {
		return fmt.Errorf("source table exceeds chart limits or has no rows")
	}
	for i := range t.Columns {
		t.Columns[i].Index = i
		t.Columns[i].Numeric = false
		if len(t.Columns[i].Name) > 512 || len(t.Columns[i].Unit) > 128 {
			return fmt.Errorf("source column exceeds limits")
		}
	}
	for _, row := range t.Rows {
		if len(row) != len(t.Columns) {
			return fmt.Errorf("source table has inconsistent columns")
		}
		for i, value := range row {
			if len(value) > 2048 {
				return fmt.Errorf("source cell exceeds limits")
			}
			if _, ok := Number(value); ok {
				t.Columns[i].Numeric = true
			}
		}
	}
	data, err := json.Marshal(t.Rows)
	if err != nil || len(data) > MaxSourceBytes {
		return fmt.Errorf("source rows exceed chart budget")
	}
	meta, _ := json.Marshal([]any{identity, t.Columns, t.Rows, t.Provenance, t.Notice, t.Truncated})
	sum := sha256.Sum256(meta)
	t.ID = "chart-source-" + hex.EncodeToString(sum[:16])
	return nil
}

var numeric = regexp.MustCompile(`^[+\-−]?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?(?:[eE][+\-]?[0-9]+)?$`)

func Number(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	if !numeric.MatchString(text) {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(text, ",", ""), "−", "-"), 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Cell preserves strings and finite numeric primitives. Null remains a gap;
// booleans, objects and arrays are not numeric observations.
func Cell(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case json.Number:
		if _, ok := Number(v.String()); !ok {
			return "", fmt.Errorf("invalid numeric cell")
		}
		return v.String(), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("invalid numeric cell")
		}
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported source cell type")
	}
}
