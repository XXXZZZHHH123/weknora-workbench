package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func pdfSourceTextFixture() map[string]any {
	data := pdfQueryEvidence()
	data["kind"] = "structured_source_inspection"
	data["rows"] = []any{map[string]any{"values": map[string]any{"quantity": "234.56", "independent_quantity": "9"}}}
	data["fragments"] = []any{map[string]any{
		"id": "fragment-fixture", "page": 83,
		"caption":   "27. Sample category",
		"footnotes": []any{"Note: Category applies from age 47 onward.", "注：每组含有58个样本。"},
		"bbox":      []any{0.913, 0.827},
	}}
	data["coverage"] = map[string]any{"quality_issues": []any{map[string]any{"message": "Note: Hidden warning has 69 records."}}}
	return data
}

func TestPDFSourceTextRequiresCompleteActuallyReadCaptionOrFootnote(t *testing.T) {
	data := pdfSourceTextFixture()
	steps := pdfEvidenceSteps("read_pdf_table", data)
	require.Empty(t, ungroundedStructuredNumbers("Original caption: “27. Sample category”. Footnote: “Note: Category applies from age 47 onward.” 引用：“注：每组含有58个样本。” 结果234.56。", nil, steps))
	for _, claim := range []string{
		"Statistic 47.", "Statistic 58.", "Statistic 27.",
		"Category applies from age 47 onward.", "每组含有58个样本。",
		"Note: Category applies from age 48 onward.",
		"Note: Hidden warning has 69 records.",
		"Source page 83.", "Coordinate 0.913.",
		"Caption 27. Sample category9", "9Note: Category applies from age 47 onward.",
	} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, nil, steps), claim)
	}
	// A successful quote never authorizes the same number in a separate claim.
	require.Contains(t, ungroundedStructuredNumbers("Note: Category applies from age 47 onward. Total 47.", nil, steps), "47")
}

func TestPDFSourceTextRejectsUnboundFailedAndNonReadMetadata(t *testing.T) {
	const quote = "Note: Category applies from age 47 onward."
	for _, mutation := range []string{"failed_call", "error_status", "missing_digest", "missing_fragment_id", "query_metadata", "directory_metadata", "top_level_footnote"} {
		t.Run(mutation, func(t *testing.T) {
			data := pdfSourceTextFixture()
			operation := "read_pdf_table"
			switch mutation {
			case "error_status":
				data["status"] = "error"
			case "missing_digest":
				delete(data["source"].(map[string]any), "sha256")
			case "missing_fragment_id":
				delete(data["fragments"].([]any)[0].(map[string]any), "id")
			case "query_metadata":
				data["kind"] = "structured_analysis"
				data["rows"] = []any{[]any{"234.56"}}
				operation = "query_pdf_table"
			case "directory_metadata":
				data["kind"] = "structured_pdf_tables"
				operation = "list_pdf_tables"
			case "top_level_footnote":
				delete(data, "fragments")
				data["footnotes"] = []any{quote}
			}
			steps := pdfEvidenceSteps(operation, data)
			if mutation == "failed_call" {
				steps[0].ToolCalls[0].Result.Success = false
			}
			messages := []chat.Message{{Role: "tool", Content: `{"value":234.56}`}}
			require.Contains(t, ungroundedStructuredNumbers(quote, messages, steps), "47")
		})
	}
}
