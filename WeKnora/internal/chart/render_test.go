package chart

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"golang.org/x/image/math/fixed"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
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
func decodeChart(t *testing.T, out []byte) image.Image {
	t.Helper()
	require.True(t, bytes.HasPrefix(out, []byte("\x89PNG\r\n\x1a\n")))
	picture, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 1600, 1000), picture.Bounds())
	return picture
}
func blueNear(picture image.Image, x, y int) bool {
	for yy := y - 4; yy <= y+4; yy++ {
		for xx := x - 4; xx <= x+4; xx++ {
			r, g, b, _ := picture.At(xx, yy).RGBA()
			if b > r*2 && b > g {
				return true
			}
		}
	}
	return false
}
func TestTimeChartUsesActualCoordinatesAndPreservesGaps(t *testing.T) {
	table := fixture(t)
	out, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time", Title: "Observed values"})
	require.NoError(t, err)
	picture := decodeChart(t, out)
	require.True(t, blueNear(picture, 165, 715))
	require.True(t, blueNear(picture, 1485, 150))
	require.False(t, blueNear(picture, 825, 432), "missing observation must break the segment, not interpolate it")
	table.Rows[1][1] = "12"
	connected, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time"})
	require.NoError(t, err)
	require.True(t, blueNear(decodeChart(t, connected), 297, 659), "one actual day maps to one tenth of the ten-day axis, not half")
}
func TestNumericAxisDoesNotReplaceUnevenSourceValuesWithCategorySpacing(t *testing.T) {
	table := Table{Columns: []Column{{Name: "position"}, {Name: "observed"}}, Rows: [][]string{{"0", "2"}, {"1", "3"}, {"10", "4"}}}
	require.NoError(t, table.Seal("numerics"))
	out, err := Render(table, Spec{Type: "scatter", X: 0, Y: []int{1}, XType: "numeric"})
	require.NoError(t, err)
	picture := decodeChart(t, out)
	require.True(t, blueNear(picture, 297, 432))
	require.False(t, blueNear(picture, 825, 432))
}
func TestChartIsDeterministicPNGWithoutHTMLOrSourcePayload(t *testing.T) {
	table := fixture(t)
	table.Truncated = true
	table.Provenance = "PRIVATE provenance must stay in artifact metadata"
	table.Notice = "PRIVATE original-data notice"
	spec := Spec{Type: "bar", X: 0, Y: []int{1}, Title: "Literal <script> labels"}
	out, err := Render(table, spec)
	require.NoError(t, err)
	decodeChart(t, out)
	again, err := Render(table, spec)
	require.NoError(t, err)
	require.Equal(t, out, again)
	// Only standard raster chunks: labels/provenance/tables are not shipped in
	// hidden HTML, text chunks, executable scripts, or a second document.
	chunks := []string{}
	for offset := 8; offset < len(out); {
		length := int(binary.BigEndian.Uint32(out[offset : offset+4]))
		chunks = append(chunks, string(out[offset+4:offset+8]))
		offset += 12 + length
	}
	for _, kind := range chunks {
		require.Contains(t, []string{"IHDR", "IDAT", "IEND"}, kind)
	}
	require.NotContains(t, string(out), "PRIVATE")
	require.NotContains(t, string(out), "<script>")
	full := table
	full.Truncated = false
	plain, err := Render(full, spec)
	require.NoError(t, err)
	require.NotEqual(t, out, plain, "partial-source badge must remain visible on raster output")
}
func TestChineseGlyphsAreRealAndMissingGlyphsFailClosed(t *testing.T) {
	face, err := chartFace(30)
	require.NoError(t, err)
	defer face.Close()
	masks := map[string]bool{}
	for _, ch := range "观测记录日期实值千克单位未确认部分数据" {
		_, mask, point, _, ok := face.Glyph(fixed.P(0, 36), ch)
		require.True(t, ok, "glyph %c", ch)
		var ink bytes.Buffer
		for y := point.Y; y < point.Y+mask.Bounds().Dy(); y++ {
			for x := point.X; x < point.X+mask.Bounds().Dx(); x++ {
				_, _, _, a := mask.At(x, y).RGBA()
				ink.WriteByte(byte(a >> 8))
			}
		}
		require.Contains(t, ink.Bytes(), byte(255))
		masks[string(ink.Bytes())] = true
	}
	require.Greater(t, len(masks), 10, "different Chinese characters must have distinct outlines, not one tofu box")
	table := fixture(t)
	_, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1}, Title: "Unsupported \U00020000"})
	require.ErrorContains(t, err, "no glyph")
}
func TestChinesePNGVisualFixture(t *testing.T) {
	table := Table{Columns: []Column{{Name: "记录日期"}, {Name: "实测值", Unit: "千克"}}, Rows: [][]string{{"2026-04-01", "12.5"}, {"2026-04-02", "15"}, {"2026-04-03", ""}, {"2026-04-04", "19.75"}, {"2026-04-08", "18"}}, Truncated: true}
	require.NoError(t, table.Seal("synthetic-visual-only"))
	out, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "time", Title: "观测值走势"})
	require.NoError(t, err)
	decodeChart(t, out)
	if directory := os.Getenv("CHART_PNG_SAMPLE_DIR"); directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "synthetic-chinese-chart.png"), out, 0600))
	}
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

func TestLargeConstantNumericXAndBoundedFullTable(t *testing.T) {
	table := Table{Columns: []Column{{Name: "X"}, {Name: "Y"}}, Rows: [][]string{{"1e308", "1"}, {"1e308", "2"}}}
	require.NoError(t, table.Seal("constant-large"))
	out, err := Render(table, Spec{Type: "scatter", X: 0, Y: []int{1}, XType: "numeric"})
	require.NoError(t, err)
	decodeChart(t, out)
	table.Rows = make([][]string, MaxRows)
	for i := range table.Rows {
		table.Rows[i] = []string{fmt.Sprint(i), fmt.Sprint(i % 100)}
	}
	require.NoError(t, table.Seal("bounded-full-table"))
	out, err = Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"})
	require.NoError(t, err)
	decodeChart(t, out)
}
func TestBarContainsPositiveAndNegativeActualObservations(t *testing.T) {
	table := Table{Columns: []Column{{Name: "Group"}, {Name: "Value", Unit: "kg"}}, Rows: [][]string{{"A", "-10"}, {"B", "10"}}}
	require.NoError(t, table.Seal("signed"))
	out, err := Render(table, Spec{Type: "bar", X: 0, Y: []int{1}})
	require.NoError(t, err)
	picture := decodeChart(t, out)
	require.True(t, blueNear(picture, 165, 575))
	require.True(t, blueNear(picture, 1485, 290))
	require.False(t, blueNear(picture, 165, 290))
	require.False(t, blueNear(picture, 1485, 575))
}

func TestConcurrentChartRenderingProducesIdenticalImages(t *testing.T) {
	table := fixture(t)
	spec := Spec{Type: "line", X: 0, Y: []int{1}, XType: "time", Title: "中文图表"}
	baseline, err := Render(table, spec)
	require.NoError(t, err)
	type result struct {
		image []byte
		err   error
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		go func() { out, err := Render(table, spec); results <- result{out, err} }()
	}
	for i := 0; i < 8; i++ {
		r := <-results
		require.NoError(t, r.err)
		require.Equal(t, baseline, r.image)
	}
}
