package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Synthetic inspect_source shape: large auxiliary chart text sorts before the
// requested cell reads alphabetically and previously consumed the budget.
func TestReviewProjectionKeepsRequestedCellsBeforeChartReferences(t *testing.T) {
	charts := make([]any, 40)
	for i := range charts {
		charts[i] = map[string]any{"title": strings.Repeat("图表标题", 20), "value_ref": "Sheet!$B$2:$B$9000", "axes": []any{"dateAx", "valAx"}}
	}
	data := map[string]any{
		"kind": "structured_source_inspection", "status": "ok",
		"chart_references": charts,
		"ranges":           []any{map[string]any{"range": "B2824", "cells": []any{map[string]any{"cell": "B2824", "value": json.Number("54.96")}}}},
	}
	value := map[string]any{"success": true, "data": data}
	projected, omitted := projectReviewValue(value, 2048, "", 0)
	encoded, err := json.Marshal(projected)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "54.96", "requested cell reads stay visible")
	require.Contains(t, strings.Join(omitted, " "), "/data/chart_references", "auxiliary chart text is the clipped part")
}
