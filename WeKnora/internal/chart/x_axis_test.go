package chart

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEligibleXTypesMatchesActualRendererForEverySelectedValue(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   []string
		eligible []string
	}{
		{"numeric-positions", []string{"2", "8", "11"}, []string{"category", "numeric"}},
		{"bare-numbers-do-not-invent-dates", []string{"2032", "2037", "2041"}, []string{"category", "numeric"}},
		{"numeric-parser-formats", []string{"−1,234.5", " 2e3 ", "+8.25"}, []string{"category", "numeric"}},
		{"iso-dates", []string{"2032-02-03", "2032-02-06", "2032-03-01"}, []string{"category", "time"}},
		{"categories", []string{"north", "south"}, []string{"category"}},
		{"mixed-numeric-and-label", []string{"2", "unknown", "11"}, []string{"category"}},
		{"mixed-number-and-date", []string{"2", "2032-03-01"}, []string{"category"}},
		{"invalid-calendar-date", []string{"2032-02-30"}, []string{"category"}},
		{"date-whitespace-is-not-rewritten", []string{" 2032-03-01 "}, []string{"category"}},
		{"not-finite-numbers", []string{"NaN", "Inf", "1e309"}, []string{"category"}},
		{"numeric-range-overflow", []string{"-1e308", "1e308"}, []string{"category"}},
		{"single-point-padding-overflow", []string{"1.7976931348623157e308"}, []string{"category"}},
		{"single-point-large-finite", []string{"1e308"}, []string{"category", "numeric"}},
		{"missing-x", []string{"2", ""}, nil},
		{"blank-x", []string{" "}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := append([]string(nil), test.values...)
			require.Equal(t, test.eligible, EligibleXTypes(test.values))
			require.Equal(t, original, test.values, "eligibility must not rewrite original source values")
			table := Table{Columns: []Column{{Name: "Dimension"}, {Name: "Reading"}}}
			for _, value := range test.values {
				table.Rows = append(table.Rows, []string{value, "3"})
			}
			require.NoError(t, table.Seal("synthetic-axis-contract"))
			for _, axisType := range []string{"category", "numeric", "time"} {
				_, err := Render(table, Spec{Type: "line", X: 0, Y: []int{1}, XType: axisType})
				eligible := false
				for _, possible := range test.eligible {
					eligible = eligible || possible == axisType
				}
				require.Equal(t, eligible, err == nil, "%s must agree with actual renderer acceptance: %v", axisType, err)
			}
		})
	}
	require.Empty(t, EligibleXTypes(nil))
	require.Empty(t, EligibleXTypes(make([]string, MaxRows+1)))
}

func TestSharedXAxisCoordinatesKeepActualNumericAndDateDistances(t *testing.T) {
	xs, minimum, maximum, err := xAxisCoordinates([]string{"2", "8", "11"}, "numeric")
	require.NoError(t, err)
	require.Equal(t, []float64{2, 8, 11}, xs)
	require.Equal(t, float64(2), minimum)
	require.Equal(t, float64(11), maximum)
	require.Equal(t, float64(2), (xs[1]-xs[0])/(xs[2]-xs[1]))
	xs, _, _, err = xAxisCoordinates([]string{"2032-02-03", "2032-02-06", "2032-02-12"}, "time")
	require.NoError(t, err)
	require.Equal(t, float64(0.5), (xs[1]-xs[0])/(xs[2]-xs[1]))
	xs, _, _, err = xAxisCoordinates([]string{"2", "8", "11"}, "category")
	require.NoError(t, err)
	require.Equal(t, []float64{0, 1, 2}, xs)
}
