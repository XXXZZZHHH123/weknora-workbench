package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func compactCalculationFixture(content string) (*sourceCalculationTool, *[]types.AgentStep) {
	steps := documentReviewFixture("read_document", true, map[string]any{"chunks": []any{map[string]any{
		"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "content": content,
	}}})
	return newSourceCalculationTool(func() []types.AgentStep { return steps }), &steps
}

func runCompactCalculation(t *testing.T, tool *sourceCalculationTool, request any) *types.ToolResult {
	t.Helper()
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	// Exercise the real registry schema's normalizer too: inspect must not
	// inherit legacy's globally required calculations field.
	normalized, err := modelcontext.NormalizeToolArguments(string(raw), tool.Parameters())
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), json.RawMessage(normalized))
	require.NoError(t, err)
	return result
}

func inspectCompactCalculation(t *testing.T, tool *sourceCalculationTool, extra map[string]any) *types.ToolResult {
	t.Helper()
	request := map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"}
	for key, value := range extra {
		request[key] = value
	}
	result := runCompactCalculation(t, tool, request)
	require.True(t, result.Success, result.Error)
	return result
}

func compactCatalogRows(result *types.ToolResult) []any { return result.Data["refs"].([]any) }

func compactOneFormula(ref, unit, expression, outputUnit string) map[string]any {
	return map[string]any{"operation": "calculate", "refs": map[string]any{"a": map[string]any{"ref": ref, "unit": unit}},
		"calculations": []any{map[string]any{"expression": expression, "result_unit": outputUnit}}}
}

func TestSourceCalculationCompactCatalogKeepsExactUTF8TokensAndSourcePositions(t *testing.T) {
	content := "原始质量🙂 1,234.5000 kg；另值 −0.25 kg；重复 1,234.5000 kg。\n" +
		"日期 2025-02-03，2025年2月3日，12/31；比例 128/256，非法日期 2025-99-99。\n" +
		"id42 code_73 1e6 https://source.example/987 [标签 31](https://example/654) <kb chunk_id=\"doc-876\" />\n" +
		"`inline 765` ``literal ` code 764``\n```text\nembedded 763\n```\n{\"value\":762}\n<tool_result>761</tool_result>"
	tool, _ := compactCalculationFixture(content)
	first := inspectCompactCalculation(t, tool, nil)
	rows := compactCatalogRows(first)
	values := []string{}
	for _, raw := range rows {
		row := raw.(map[string]any)
		values = append(values, row["value"].(string))
		start, end := row["start_rune"].(int), row["end_rune"].(int)
		require.Equal(t, row["source_value"], string([]rune(content)[start:end]))
		require.True(t, utf8.ValidString(row["context"].(string)))
	}
	require.Equal(t, []string{"1234.5000", "-0.25", "1234.5000", "128", "256", "2025", "31"}, values)
	require.NotEqual(t, rows[0].(map[string]any)["ref"], rows[2].(map[string]any)["ref"], "equal values retain distinct original positions")
	second := inspectCompactCalculation(t, tool, nil)
	require.Equal(t, first.Output, second.Output, "same actual observation and span reuses its issued short refs")
	require.LessOrEqual(t, len(first.Output), sourceReferenceCatalogBudget)
}

func TestSourceCalculationCompactSharedRefsComputeMoreThanLegacyBatchWithoutQuoteTranscription(t *testing.T) {
	tool, _ := compactCalculationFixture("Current 120 kg; previous 100 kg; unrelated 55 kg.")
	rows := compactCatalogRows(inspectCompactCalculation(t, tool, nil))
	refs := map[string]any{}
	for index, name := range []string{"a", "b", "spare"} {
		refs[name] = map[string]any{"ref": rows[index].(map[string]any)["ref"], "unit": "kg"}
	}
	formulas := []any{}
	for index := 0; index < 68; index++ {
		formulas = append(formulas, map[string]any{"expression": "a/b-1", "result_unit": "%"})
	}
	request := map[string]any{"operation": "calculate", "refs": refs, "calculations": formulas}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "quote")
	require.NotContains(t, string(raw), "value")
	result := runCompactCalculation(t, tool, request)
	require.True(t, result.Success, result.Error)
	calculations := result.Data["calculations"].([]any)
	require.Len(t, calculations, 68)
	require.Equal(t, 1, result.Data["unused_ref_count"])
	require.Len(t, result.Data["source_quotes"], 1, "the server stores one exact shared original quote")
	for _, raw := range calculations {
		calculation := raw.(map[string]any)
		require.Equal(t, "20", calculation["value"])
		require.Equal(t, "a/b-1", calculation["expression"])
		proofs := calculation["inputs"].(map[string]any)
		require.Len(t, proofs, 2)
		require.NotContains(t, proofs, "spare")
		require.Equal(t, "actual-document", proofs["a"].(map[string]any)["knowledge_id"])
		require.Equal(t, refs["a"].(map[string]any)["ref"], proofs["a"].(map[string]any)["source_ref"])
	}
}

func TestSourceCalculationCompactPaginationIssuesOnlyReturnedRefsAndRejectsOtherRuntime(t *testing.T) {
	tool, _ := compactCalculationFixture("First 10 kg; second 20 kg; third 30 kg.")
	page := inspectCompactCalculation(t, tool, map[string]any{"limit": 1})
	row := compactCatalogRows(page)[0].(map[string]any)
	issued := row["ref"].(string)
	guessed := issued[:strings.LastIndex(issued, ":")+1] + "2"
	require.False(t, runCompactCalculation(t, tool, compactOneFormula(guessed, "kg", "a", "")).Success)
	page = inspectCompactCalculation(t, tool, map[string]any{"offset": page.Data["next_offset"], "limit": 1, "source_fingerprint": page.Data["source_fingerprint"]})
	require.Equal(t, guessed, compactCatalogRows(page)[0].(map[string]any)["ref"])
	require.True(t, runCompactCalculation(t, tool, compactOneFormula(guessed, "kg", "a", "")).Success)
	other, _ := compactCalculationFixture("First 10 kg; second 20 kg; third 30 kg.")
	inspectCompactCalculation(t, other, nil)
	require.False(t, runCompactCalculation(t, other, compactOneFormula(issued, "kg", "a", "")).Success, "a prior runtime's nonce is never a current issued ref")
}

func TestSourceCalculationCompactFailedPageDoesNotIssuePendingRefs(t *testing.T) {
	tool, _ := compactCalculationFixture("First 10 kg; second 20 kg.")
	// Leave room for only the first tentative entry; exhaustion on the second
	// must not make that unreturned first ordinal available to calculate.
	for index := 0; index < 8191; index++ {
		tool.compact.issued["old:"+strconv.Itoa(index)] = sourceNumberReference{}
	}
	result := runCompactCalculation(t, tool, map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"})
	require.False(t, result.Success)
	require.Len(t, tool.compact.issued, 8191)
	require.Empty(t, tool.compact.keys)
	ref := "sv:" + tool.compact.nonce + ":8192"
	require.False(t, runCompactCalculation(t, tool, compactOneFormula(ref, "kg", "a", "")).Success)
}

func TestSourceCalculationCompactRejectsStaleChangedFailedAndAmbiguousSources(t *testing.T) {
	for _, mutation := range []string{"version", "content", "failed", "different_document", "caption"} {
		t.Run(mutation, func(t *testing.T) {
			tool, steps := compactCalculationFixture("Mass 13 kg.")
			row := (*steps)[0].ToolCalls[0].Result.Data["chunks"].([]any)[0].(map[string]any)
			if mutation == "caption" {
				row["content"], row["images"] = "Caption below.", []any{map[string]any{"url": "resource://real-image", "caption": "Mass 13 kg."}}
			}
			catalog := inspectCompactCalculation(t, tool, nil)
			ref := compactCatalogRows(catalog)[0].(map[string]any)["ref"].(string)
			if mutation == "failed" {
				(*steps)[0].ToolCalls[0].Result.Success = false
			} else {
				copyRow := map[string]any{}
				for key, value := range row {
					copyRow[key] = value
				}
				switch mutation {
				case "version":
					copyRow["content_revision"] = "new-revision"
				case "content":
					copyRow["content"] = "Mass 17 kg."
				case "different_document":
					copyRow["knowledge_id"] = "different-document"
				case "caption":
					copyRow["images"] = []any{map[string]any{"url": "resource://real-image", "caption": "Mass 17 kg."}}
				}
				*steps = append(*steps, documentReviewFixture("read_document", true, map[string]any{"chunks": []any{copyRow}})...)
			}
			result := runCompactCalculation(t, tool, compactOneFormula(ref, "kg", "a", ""))
			require.False(t, result.Success)
			require.NotContains(t, result.Data, "calculations")
		})
	}
}

func TestSourceCalculationCompactCurrentScopeRebindInvalidatesIssuedRefs(t *testing.T) {
	_, registry, steps := tableViewFixture("Mass 10 kg.")
	doc := &types.Knowledge{ID: "actual-document", Title: "Source", FileName: "source.pdf", TenantID: 7, KnowledgeBaseID: "granted-base"}
	read := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: doc}, &plannedDocumentChunks{repo: &plannedDocumentRepo{}}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-base", TenantID: 7}})
	registry.RegisterTool(read)
	agenttools.PrepareDocumentScope(registry, "Only physical page 1.")
	binder, err := registry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	bind := func(source string) {
		result, err := binder.Execute(context.Background(), json.RawMessage(`{"selection_reason":"actual source plan","sources":[`+source+`]}`))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
	}
	bind(`{"knowledge_id":"actual-document","expected_title":"Source"}`)
	(*steps)[0].ToolCalls[0].Result.Data["document_scope"] = map[string]any{"matched": true, "knowledge_id": "actual-document", "page": 2}
	tool := newSourceCalculationTool(func() []types.AgentStep { return *steps }, registry)
	ref := compactCatalogRows(inspectCompactCalculation(t, tool, nil))[0].(map[string]any)["ref"].(string)
	bind(`{"knowledge_id":"actual-document","expected_title":"Source","user_page_constraint":{"pages":[1],"user_quote":"Only physical page 1."}}`)
	require.False(t, runCompactCalculation(t, tool, compactOneFormula(ref, "kg", "a", "")).Success)
	require.False(t, runCompactCalculation(t, tool, map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"}).Success)
}

func TestSourceCalculationCompactPreservesUnitsAndRejectsInlineValuesMalformedPlansAndBudgets(t *testing.T) {
	tool, _ := compactCalculationFixture("Mass 10 kg; ratio 125%.")
	rows := compactCatalogRows(inspectCompactCalculation(t, tool, nil))
	ref := rows[0].(map[string]any)["ref"].(string)
	require.True(t, runCompactCalculation(t, tool, compactOneFormula(ref, "", "a", "")).Success)
	for _, input := range []map[string]any{
		compactOneFormula(ref, "", "a/a-1", "%"),
		compactOneFormula(ref, "invented", "a", ""),
		compactOneFormula(ref, "kg", "a+7", ""),
		compactOneFormula(ref, "kg", "missing", ""),
		compactOneFormula(ref, "kg", strings.Repeat("a+", 600)+"a", ""),
	} {
		require.False(t, runCompactCalculation(t, tool, input).Success)
	}
	percent := rows[1].(map[string]any)["ref"].(string)
	result := runCompactCalculation(t, tool, compactOneFormula(percent, "%", "a-1", "%"))
	require.Equal(t, "25", sourceCalculationFirstResult(t, result)["value"])
	request := compactOneFormula(ref, "kg", "a", "")
	request["refs"].(map[string]any)["a"].(map[string]any)["value"] = "999"
	require.False(t, runCompactCalculation(t, tool, request).Success)
	request = compactOneFormula(ref, "kg", "a", "")
	request["calculations"].([]any)[0].(map[string]any)["inputs"] = map[string]any{}
	require.False(t, runCompactCalculation(t, tool, request).Success)
	request = compactOneFormula(ref, "kg", "a", "")
	formulas := []any{}
	for index := 0; index < 129; index++ {
		formulas = append(formulas, map[string]any{"expression": "a", "result_unit": ""})
	}
	request["calculations"] = formulas
	require.False(t, runCompactCalculation(t, tool, request).Success)
	request = compactOneFormula(ref, "kg", "a", "")
	for index := 0; index < 128; index++ {
		request["refs"].(map[string]any)[fmt.Sprintf("extra%d", index)] = map[string]any{"ref": ref, "unit": "kg"}
	}
	require.False(t, runCompactCalculation(t, tool, request).Success)
}

func TestSourceCalculationCompactRegistryKeepsLegacyDecimalNormalization(t *testing.T) {
	tool, _ := compactCalculationFixture("Mass 9007199254740993.125000 kg.")
	raw := `{"calculations":[{"expression":"a","inputs":{"a":{"value":9007199254740993.125000,"chunk_id":"actual-chunk","quote":"Mass 9007199254740993.125000 kg.","unit":"kg"}},"result_unit":""}]}`
	normalized, err := modelcontext.NormalizeToolArguments(raw, tool.Parameters())
	require.NoError(t, err)
	require.Contains(t, normalized, `"value":"9007199254740993.125000"`)
	result, err := tool.Execute(context.Background(), json.RawMessage(normalized))
	require.NoError(t, err)
	require.Equal(t, "9007199254740993.125", sourceCalculationFirstResult(t, result)["value"])
}

func TestSourceCalculationCompactLongSourceQuotePreservesProseExclusionBoundaries(t *testing.T) {
	for _, excluded := range []string{"```\n" + strings.Repeat("code ", 1000) + "\n```\n", `{"payload":"` + strings.Repeat("json ", 1000) + `"}`} {
		content := strings.Repeat("Original prose. ", 1000) + excluded + "\nMass 12 kg."
		tool, _ := compactCalculationFixture(content)
		rows := compactCatalogRows(inspectCompactCalculation(t, tool, nil))
		require.Len(t, rows, 1)
		ref := rows[0].(map[string]any)["ref"].(string)
		result := runCompactCalculation(t, tool, compactOneFormula(ref, "kg", "a", ""))
		require.Equal(t, "12", sourceCalculationFirstResult(t, result)["value"])
		for _, raw := range result.Data["source_quotes"].(map[string]sourceCalculationQuote) {
			require.Contains(t, content, raw.Quote)
			require.Contains(t, documentNumericProse(content), documentNumericProse(raw.Quote))
		}
		// A different field's distant unit must not be patched into the quote.
		require.False(t, runCompactCalculation(t, tool, compactOneFormula(ref, "invented", "a", "")).Success)
	}
}
