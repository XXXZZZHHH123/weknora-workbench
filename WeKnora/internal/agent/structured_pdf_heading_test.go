package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func pdfHeadingFixture() map[string]any {
	data := pdfSourceTextFixture()
	data["fragments"] = []any{map[string]any{"id": "selected-fragment", "page": 83, "caption": ""}}
	data["page_context"] = []any{map[string]any{"page": 83, "text": "Source page\n31. Observation categories\n32、观察分类\nTotal 65432 records\n42. Category 65432 records\n43. Rate %\n44. 5678\n45.6\n46. %\n47. A category 3%\n"}}
	return data
}

func TestPDFSourcePageHeadingOnlyAuthorizesCompleteBoundIdentity(t *testing.T) {
	data := pdfHeadingFixture()
	steps := pdfEvidenceSteps("read_pdf_table", data)
	require.Empty(t, ungroundedStructuredNumbers("Source “31. Observation categories” and “32、观察分类”; quantity 234.56.", nil, steps))
	for _, claim := range []string{
		"Value 31.", "Value 32.", "Observation categories 31.",
		"Source 31. Observation categories9", "Source 9Observed 31. Observation categories9",
		"Total 65432 records", "Source 42. Category 65432 records", "Source 43. Rate %",
		"Source 44. 5678", "Value 45.6", "Source 46. %", "Source 47. A category 3%",
	} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, nil, steps), claim)
	}
	require.Contains(t, ungroundedStructuredNumbers("31. Observation categories; additional statistic 31.", nil, steps), "31")
}

func TestPDFSourcePageHeadingRejectsWrongPageAndUnboundMetadata(t *testing.T) {
	const candidate = "Original heading: 31. Observation categories"
	for _, name := range []string{"other_page", "missing_page", "missing_fragment_id", "missing_digest", "failed", "error_status", "query", "directory", "text_parts_only"} {
		t.Run(name, func(t *testing.T) {
			data := pdfHeadingFixture()
			operation := "read_pdf_table"
			switch name {
			case "other_page":
				data["page_context"].([]any)[0].(map[string]any)["page"] = 84
			case "missing_page":
				delete(data["fragments"].([]any)[0].(map[string]any), "page")
			case "missing_fragment_id":
				delete(data["fragments"].([]any)[0].(map[string]any), "id")
			case "missing_digest":
				delete(data["source"].(map[string]any), "sha256")
			case "error_status":
				data["status"] = "error"
			case "query":
				operation = "query_pdf_table"
				data["kind"] = "structured_analysis"
				data["rows"] = []any{[]any{"234.56"}}
			case "directory":
				operation = "list_pdf_tables"
				data["kind"] = "structured_pdf_tables"
			case "text_parts_only":
				block := data["page_context"].([]any)[0].(map[string]any)
				block["text_parts"] = []any{block["text"]}
				delete(block, "text")
			}
			steps := pdfEvidenceSteps(operation, data)
			if name == "failed" {
				steps[0].ToolCalls[0].Result.Success = false
			}
			require.Contains(t, ungroundedStructuredNumbers(candidate, []chat.Message{{Role: "tool", Content: `{"value":234.56}`}}, steps), "31")
		})
	}
}
