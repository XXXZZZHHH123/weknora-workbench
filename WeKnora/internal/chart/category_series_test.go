package chart

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCategoryChartsRequireAnExplicitSeriesDimension(t *testing.T) {
	table := Table{
		Columns: []Column{{Name: "period"}, {Name: "total", Unit: "kg"}, {Name: "region"}},
		Rows:    [][]string{{"P1", "4", "north"}, {"P2", "6", "north"}, {"P1", "7", "south"}, {"P2", "9", "south"}},
	}
	require.NoError(t, table.Seal("isolated-observations"))
	for _, kind := range []string{"bar", "line"} {
		_, err := Render(table, Spec{Type: kind, X: 0, Y: []int{1}, XType: "category"})
		require.ErrorContains(t, err, "repeated X categories")
	}
	// Replicate measurements remain valid scatter samples.
	_, err := Render(table, Spec{Type: "scatter", X: 0, Y: []int{1}, XType: "category"})
	require.NoError(t, err)
	// A source-derived pivot retains every business series under its name.
	pivot := Table{Columns: []Column{{Name: "period"}, {Name: "north", Unit: "kg"}, {Name: "south", Unit: "kg"}}, Rows: [][]string{{"P1", "4", "7"}, {"P2", "6", "9"}}}
	require.NoError(t, pivot.Seal("isolated-pivot"))
	_, err = Render(pivot, Spec{Type: "bar", X: 0, Y: []int{1, 2}, XType: "category"})
	require.NoError(t, err)
}

func TestNumericRepeatedPositionsRemainActualSamples(t *testing.T) {
	table := Table{Columns: []Column{{Name: "position"}, {Name: "value", Unit: "kg"}}, Rows: [][]string{{"2", "4"}, {"2", "7"}, {"9", "10"}}}
	require.NoError(t, table.Seal("isolated-replicate-measurements"))
	_, err := Render(table, Spec{Type: "scatter", X: 0, Y: []int{1}, XType: "numeric"})
	require.NoError(t, err)
}
