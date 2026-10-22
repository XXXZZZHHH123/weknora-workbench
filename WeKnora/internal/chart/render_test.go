package chart

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) Table {
	t.Helper()
	table := Table{Columns: []Column{{Name: "date"}, {Name: "value", Unit: "kg"}, {Name: "source_cell"}}, Rows: [][]string{{"2025-01-01", "10", "B2"}, {"2025-01-02", "", "B3"}, {"2025-01-11", "30", "B4"}}, Provenance: "file: actual.xlsx; sha256: original-source-hash", Notice: "actual observations"}
	require.NoError(t, table.Seal("query-real"))
	return table
}
func TestTimeChartUsesActualCoordinatesPreservesGapsAndSourceCells(t *testing.T) {
	table := fixture(t)
	document, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time", Title: "Observed values"})
	require.NoError(t, err)
	text := string(document)
	require.Contains(t, text, `cx="95.00"`)
	require.Contains(t, text, `cx="945.00"`)
	require.Contains(t, text, `M 95.00 440.00 `)
	require.Contains(t, text, `M 945.00 90.00 `)
	require.NotContains(t, text, `L 945.00`, "a missing row breaks the line rather than bridging or filling it")
	require.Contains(t, text, "空值 1 个")
	require.Contains(t, text, "B3")
	require.Contains(t, text, "kg")
	require.NotContains(t, text, "<script")
	require.Contains(t, text, "default-src 'none'")
}
func TestNumericAxisDoesNotReplaceUnevenSourceValuesWithCategorySpacing(t *testing.T) {
	table := Table{Columns: []Column{{Name: "position"}, {Name: "observed"}}, Rows: [][]string{{"0", "2"}, {"1", "3"}, {"10", "4"}}}
	require.NoError(t, table.Seal("numerics"))
	out, err := Render(table, Spec{Type: "scatter", X: 0, Y: []int{1}, XType: "numeric"})
	require.NoError(t, err)
	require.Contains(t, string(out), `cx="180.00"`)
}
func TestChartEscapesUntrustedLabelsAndDisclosesPartialScope(t *testing.T) {
	table := fixture(t)
	table.Truncated = true
	table.Columns[1].Name = `<img src=x onerror=alert(1)>`
	table.Provenance = `</pre><script>attack()</script>`
	out, err := Render(table, Spec{Type: "bar", X: 0, Y: []int{1}, Title: `</title><script>bad()</script>`})
	require.NoError(t, err)
	text := string(out)
	require.NotContains(t, text, "<script>")
	require.NotContains(t, text, "<img src=")
	require.Contains(t, text, "&lt;script&gt;")
	require.Contains(t, text, "来源结果已截断")
	require.Contains(t, text, `<rect`)
}
func TestChartRejectsInvalidNumbersColumnsTypesAndInventedDates(t *testing.T) {
	for name, spec := range map[string]Spec{"unknown_type": {Type: "python", X: 0, Y: []int{1}}, "unknown_column": {Type: "line", X: 0, Y: []int{10}}, "same_axis": {Type: "line", X: 0, Y: []int{0}}, "repeated": {Type: "line", X: 0, Y: []int{1, 1}}, "date_as_number": {Type: "line", X: 0, Y: []int{1}, XType: "numeric"}} {
		t.Run(name, func(t *testing.T) { _, err := Render(fixture(t), spec); require.Error(t, err) })
	}
	table := fixture(t)
	table.Rows[1][1] = "not a measurement"
	_, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}})
	require.ErrorContains(t, err, "nothing was silently dropped")
	table = fixture(t)
	table.Rows[0][0] = "Jan"
	_, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time"})
	require.ErrorContains(t, err, "no inferred dates")
	table = fixture(t)
	table.Columns[2].Unit = "different"
	_, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1, 2}})
	require.ErrorContains(t, err, "different source units")
}
func TestChartHandlesAreBoundToExactEvidenceAndBudgets(t *testing.T) {
	first := fixture(t)
	second := fixture(t)
	require.Equal(t, first.ID, second.ID)
	second.Rows[0][1] = "11"
	require.NoError(t, second.Seal("query-real"))
	require.NotEqual(t, first.ID, second.ID)
	second = fixture(t)
	second.Rows = make([][]string, MaxRows+1)
	require.Error(t, second.Seal("too-many"))
	second = fixture(t)
	second.Rows[0][1] = strings.Repeat("1", 2049)
	require.Error(t, second.Seal("too-large"))
	_, err := Cell(math.NaN())
	require.Error(t, err)
	_, ok := Number("NaN")
	require.False(t, ok)
	_, ok = Number("1,23")
	require.False(t, ok)
	value, ok := Number("−1,234.5")
	require.True(t, ok)
	require.Equal(t, -1234.5, value)
}
