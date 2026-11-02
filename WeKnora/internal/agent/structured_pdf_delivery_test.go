package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestPDFNumericPresentationNeedsExplicitExecutedRankAndConversionFactor(t *testing.T) {
	data := pdfQueryEvidence()
	data["columns"] = []any{"region", "quantity", "converted"}
	data["rows"] = []any{
		[]any{"Region A", 9131.0, 91.31}, []any{"Region B", 8272.0, 82.72},
		[]any{"Region C", 7363.0, 73.63}, []any{"Region D", 6454.0, 64.54},
	}
	data["input_count"] = 14
	data["returned_row_count"] = 4
	data["coverage"] = map[string]any{"logical_row_count": 14}
	data["sql"] = "SELECT region, quantity, quantity/100 AS converted FROM data ORDER BY quantity DESC"
	const candidate = "| Rank | Region | Converted |\n|---|---|---|\n| 1 | Region A | 91.31 |\n| 2 | Region B | 82.72 |\n| 3 | Region C | 73.63 |\nThe conversion divides by 100."
	steps := pdfEvidenceSteps("query_pdf_table", data)
	unsupported := ungroundedStructuredNumbers(candidate, nil, steps)
	require.Contains(t, unsupported, "3", "position in a rendered table is not an executed rank")
	require.Contains(t, unsupported, "100", "an SQL operand is not a returned result")
	feedback := buildNumericValidationFeedback(candidate, unsupported)
	var view numericValidationFeedbackView
	encoded, err := json.Marshal(feedback)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &view))
	for _, token := range []string{"3", "100"} {
		found := false
		for _, issue := range view.UnsupportedTokens {
			if issue.Token == token {
				found = true
				require.NotEmpty(t, issue.CandidateOccurrences)
			}
		}
		require.True(t, found, "every unresolved presentation claim keeps its own diagnostic")
	}
	// The model chooses the SQL scope and order; only its successful execution
	// can return new ranks and the factor selected from the source's unit.
	data["columns"] = []any{"rank", "region", "quantity", "converted", "conversion_factor"}
	data["rows"] = []any{
		[]any{1, "Region A", 9131.0, 91.31, 100}, []any{2, "Region B", 8272.0, 82.72, 100},
		[]any{3, "Region C", 7363.0, 73.63, 100}, []any{4, "Region D", 6454.0, 64.54, 100},
	}
	data["sql"] = "SELECT ROW_NUMBER() OVER (ORDER BY quantity DESC) AS rank, region, quantity, quantity/100 AS converted, 100 AS conversion_factor FROM data ORDER BY quantity DESC"
	require.Empty(t, ungroundedStructuredNumbers(candidate, nil, pdfEvidenceSteps("query_pdf_table", data)))
	require.Contains(t, ungroundedStructuredNumbers(candidate+"Invented value 5555.", nil, pdfEvidenceSteps("query_pdf_table", data)), "5555")
	steps = pdfEvidenceSteps("query_pdf_table", data)
	steps[0].ToolCalls[0].Result.Success = false
	require.NotEmpty(t, ungroundedStructuredNumbers(candidate, nil, steps), "a failed query cannot authorize the requested ranks")
}

func TestPDFSourceCellLabelsAreExactIdentitiesOnly(t *testing.T) {
	data := pdfQueryEvidence()
	data["kind"] = "structured_source_inspection"
	data["rows"] = []any{map[string]any{
		"values":       map[string]any{"category": "51+", "other_category": "#62+", "value": "234.56", "independent_value": "9"},
		"raw_values":   map[string]any{"category": "73+"},
		"source_cells": map[string]any{"category": []any{map[string]any{"text": "84+", "page": 95}}},
	}}
	steps := pdfEvidenceSteps("read_pdf_table", data)
	require.Empty(t, ungroundedStructuredNumbers("The source labels are 51+ and #62+; the value is 234.56.", nil, steps))
	for _, claim := range []string{"Statistic 51.", "Statistic 62.", "Unknown category 73+.", "Unknown category 84+.", "Value 95.", "Forged #62+9.", "Forged 51+9.", "Forged 9#62+."} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, nil, steps), claim)
	}
	// Arbitrary computed SQL strings do not establish original source identities.
	data["kind"] = "structured_analysis"
	data["columns"] = []any{"model_generated_label"}
	data["rows"] = []any{[]any{"51+"}}
	require.Contains(t, ungroundedStructuredNumbers("Category 51+.", nil, pdfEvidenceSteps("query_pdf_table", data)), "51")
	data["kind"] = "structured_source_inspection"
	data["rows"] = []any{map[string]any{"values": map[string]any{"label": "51+"}}}
	data["status"] = "error"
	require.Contains(t, ungroundedStructuredNumbers("Category 51+.", []chat.Message{{Role: "tool", Content: `{"value":11}`}}, pdfEvidenceSteps("read_pdf_table", data)), "51")
}
