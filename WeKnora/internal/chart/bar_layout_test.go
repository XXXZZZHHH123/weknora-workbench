package chart

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type barPixelRun struct{ first, last int }

func barColorRuns(picture image.Image, row int, color color.RGBA) []barPixelRun {
	runs := []barPixelRun{}
	for x := picture.Bounds().Min.X; x < picture.Bounds().Max.X; x++ {
		r, g, b, a := picture.At(x, row).RGBA()
		if uint8(r>>8) != color.R || uint8(g>>8) != color.G || uint8(b>>8) != color.B || uint8(a>>8) != color.A {
			continue
		}
		if len(runs) > 0 && runs[len(runs)-1].last == x-1 {
			runs[len(runs)-1].last = x
		} else {
			runs = append(runs, barPixelRun{x, x})
		}
	}
	return runs
}

func barTickNear(picture image.Image, x float64) bool {
	// Five pixels below the X baseline contains ticks but neither bars nor
	// labels. A bar-only translation would leave this check at the old center.
	center := int(math.Round(x))
	for xx := center - 2; xx <= center+2; xx++ {
		r, g, b, _ := picture.At(xx, int(plotTop+plotHeight)+5).RGBA()
		if r>>8 < 170 && g>>8 < 170 && b>>8 < 170 {
			return true
		}
	}
	return false
}

func barLayoutTable(t *testing.T, xs []string, seriesCount int) Table {
	t.Helper()
	table := Table{Columns: []Column{{Name: "Group"}}}
	for i := 0; i < seriesCount; i++ {
		table.Columns = append(table.Columns, Column{Name: fmt.Sprintf("Reading %d", i+1), Unit: "kg"})
	}
	for _, x := range xs {
		row := []string{x}
		for i := 0; i < seriesCount; i++ {
			row = append(row, "10")
		}
		table.Rows = append(table.Rows, row)
	}
	require.NoError(t, table.Seal("isolated-bar-layout"))
	return table
}

func assertBarGroupsInsidePlot(t *testing.T, picture image.Image, groupCount, seriesCount int, checkTicks bool) []float64 {
	t.Helper()
	starts, ends := make([]int, groupCount), make([]int, groupCount)
	for i := range starts {
		starts[i] = ImageWidth
	}
	for series := 0; series < seriesCount; series++ {
		runs := barColorRuns(picture, 432, palette[series])
		require.Len(t, runs, groupCount, "all full-width source bars must remain separately visible")
		minWidth, maxWidth := ImageWidth, 0
		for group, run := range runs {
			require.Greater(t, run.first, int(plotLeft), "bars must not enter the Y-axis label area")
			require.Less(t, run.last, int(plotLeft+plotWidth), "the last group needs its full right half")
			starts[group] = min(starts[group], run.first)
			ends[group] = max(ends[group], run.last)
			width := run.last - run.first + 1
			minWidth, maxWidth = min(minWidth, width), max(maxWidth, width)
		}
		require.LessOrEqual(t, maxWidth-minWidth, 2, "edge bars must have the same full width as interior bars")
	}
	centers := make([]float64, groupCount)
	for group := range starts {
		centers[group] = float64(starts[group]+ends[group]) / 2
		if group > 0 {
			require.Greater(t, starts[group], ends[group-1], "groups at distinct real coordinates must not overlap")
		}
		if checkTicks && (group == 0 || group == groupCount-1) {
			// Intermediate ticks may be omitted when their true coordinates
			// are too close for readable labels; endpoints still align.
			require.True(t, barTickNear(picture, centers[group]), "the group and its source tick must share one X mapping")
		}
	}
	return centers
}

func TestBarGroupsHaveCompleteEdgesAndAlignedTicks(t *testing.T) {
	for _, test := range []struct {
		axis string
		xs   []string
	}{
		{axis: "category", xs: []string{"A"}},
		{axis: "category", xs: []string{"A", "B", "C"}},
		{axis: "numeric", xs: []string{"2"}},
		{axis: "numeric", xs: []string{"2", "8", "11"}},
		{axis: "time", xs: []string{"2030-01-01"}},
		{axis: "time", xs: []string{"2030-01-01", "2030-01-02", "2030-01-11"}},
	} {
		for _, seriesCount := range []int{1, 6} {
			t.Run(fmt.Sprintf("%s-groups-%d-series-%d", test.axis, len(test.xs), seriesCount), func(t *testing.T) {
				table := barLayoutTable(t, test.xs, seriesCount)
				y := make([]int, seriesCount)
				for i := range y {
					y[i] = i + 1
				}
				out, err := Render(table, Spec{Type: "bar", X: 0, Y: y, XType: test.axis})
				require.NoError(t, err)
				assertBarGroupsInsidePlot(t, decodeChart(t, out), len(test.xs), seriesCount, true)
			})
		}
	}
}

func TestBarPaddingPreservesActualNumericAndTimeDistanceRatios(t *testing.T) {
	for _, test := range []struct {
		name, axis string
		xs         []string
		ratio      float64
	}{
		{name: "numeric", axis: "numeric", xs: []string{"-5", "-4", "5"}, ratio: 0.1},
		{name: "time", axis: "time", xs: []string{"2030-01-01", "2030-01-02", "2030-01-11"}, ratio: 0.1},
		{name: "category", axis: "category", xs: []string{"A", "B", "C"}, ratio: 0.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			table := barLayoutTable(t, test.xs, 1)
			before := fmt.Sprint(table.Rows)
			out, err := Render(table, Spec{Type: "bar", X: 0, Y: []int{1}, XType: test.axis})
			require.NoError(t, err)
			centers := assertBarGroupsInsidePlot(t, decodeChart(t, out), 3, 1, true)
			require.InDelta(t, test.ratio, (centers[1]-centers[0])/(centers[2]-centers[0]), 0.002)
			require.Equal(t, before, fmt.Sprint(table.Rows), "layout never changes or reorders source observations")
		})
	}
}

func TestBarGroupsUseNearestRealSpacingWithoutSortingSourceRows(t *testing.T) {
	table := barLayoutTable(t, []string{"0", "10", "1"}, 6)
	before := fmt.Sprint(table.Rows)
	out, err := Render(table, Spec{Type: "bar", X: 0, Y: []int{1, 2, 3, 4, 5, 6}, XType: "numeric"})
	require.NoError(t, err)
	centers := assertBarGroupsInsidePlot(t, decodeChart(t, out), 3, 6, false)
	require.InDelta(t, 0.1, (centers[1]-centers[0])/(centers[2]-centers[0]), 0.002)
	require.Equal(t, before, fmt.Sprint(table.Rows))
}
