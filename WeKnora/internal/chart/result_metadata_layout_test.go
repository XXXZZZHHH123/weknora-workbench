package chart

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/font"
)

func TestBoundDisplayAndUnitEvidenceChangeSourceSeal(t *testing.T) {
	table := fixture(t)
	before := table.ID
	table.Columns[1].DisplayName = "Original observed quantity"
	require.NoError(t, table.Seal("query-real"))
	require.NotEqual(t, before, table.ID)
	before = table.ID
	table.Columns[1].UnitMetadata = &structuredresult.Unit{Status: "declared", Symbol: "kg", EvidenceLevel: "imported_series_metadata", BindingScope: "result_column"}
	require.NoError(t, table.Seal("query-real"))
	require.NotEqual(t, before, table.ID)
	require.Equal(t, "value", table.Columns[1].Name)
	require.Equal(t, "Original observed quantity", table.Columns[1].Label())
}

func TestLongLegendsPreserveEveryLabelAndUnitWithinCanvas(t *testing.T) {
	face, err := chartFace(22)
	require.NoError(t, err)
	defer face.Close()
	table := Table{Columns: []Column{{Name: "dimension", DisplayName: "类别"}}, Rows: [][]string{{"A"}, {"B"}}}
	selected := []int{}
	for i := 0; i < 6; i++ {
		table.Columns = append(table.Columns, Column{Name: "internal_alias", DisplayName: strings.Repeat("实际测量标签", 18), Unit: "千克"})
		table.Rows[0] = append(table.Rows[0], "1")
		table.Rows[1] = append(table.Rows[1], "2")
		selected = append(selected, i+1)
	}
	require.NoError(t, table.Seal("synthetic-long-labels"))
	items, bottom := layoutLegend(face, table, selected)
	require.Len(t, items, 6)
	require.Greater(t, bottom, ImageHeight)
	for i, item := range items {
		require.Equal(t, table.Columns[selected[i]].DisplayName+"（千克）", strings.Join(item.lines, ""))
		for _, line := range item.lines {
			require.LessOrEqual(t, item.x+40+font.MeasureString(face, line).Ceil(), ImageWidth-32)
		}
		require.LessOrEqual(t, item.y+(len(item.lines)-1)*30, bottom)
	}
	out, err := Render(table, Spec{Type: "line", X: 0, Y: selected, XType: "category"})
	require.NoError(t, err)
	decoded, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, ImageWidth, decoded.Bounds().Dx())
	require.Greater(t, decoded.Bounds().Dy(), ImageHeight)
}

func TestV2UnknownMixedAndLiteralMeasurementsAreNotSilentlyCombined(t *testing.T) {
	table := fixture(t)
	table.Columns[1].Unit = ""
	table.Columns[1].UnitMetadata = &structuredresult.Unit{Status: "unknown"}
	_, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time"})
	require.NoError(t, err)
	_, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1, 2}, XType: "time"})
	require.ErrorContains(t, err, "unconfirmed")
	table.Columns[1].UnitMetadata.Status = "mixed"
	_, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time"})
	require.ErrorContains(t, err, "mixed")
	table.Columns[1].UnitMetadata = nil
	for _, kind := range []string{"constant_label", "mixed_constant_values", "source_metadata", "unknown"} {
		table.Columns[1].DependencyKind = kind
		_, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time"})
		require.ErrorContains(t, err, "source-value dependency")
	}
}
