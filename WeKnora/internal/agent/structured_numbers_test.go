package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredNumbersRejectUncomputedMedianAndPermitRoundingDatesAndCells(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","rows":[[17207.5,582.5,-2.43626]],"inputs":{"n":16}}`}}
	require.Empty(t, ungroundedStructuredNumbers("中位数17,207.50，差582.50，跌幅2.44%。2023-09-01，B4690。", messages))
	require.Equal(t, []string{"17252.50", "537.50"}, ungroundedStructuredNumbers("中位数17252.50，差537.50", messages))
}
func TestStructuredNumberEvidenceIsNotTakenFromAssistantDrafts(t *testing.T) {
	messages := []chat.Message{{Role: "assistant", Content: `{"value":12345}`}, {Role: "tool", Content: `{"value":8}`}}
	require.Equal(t, []string{"12345"}, ungroundedStructuredNumbers("12345", messages))
}

func TestStructuredNumbersIgnoreOpaqueCitationIDsButStillRejectWrongProseValue(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","status":"ok","rows":[[789]]}`}}
	citation := `<kb doc="验收文档" chunk_id="eadedf42-820a-4581-bfa9-d0c78bd8a68f" kb_id="9cd1dcbd-556b-4a9d-86df-4533ab1174f0" />`
	require.Empty(t, ungroundedStructuredNumbers("最大值789。"+citation, messages))
	require.Equal(t, []string{"999"}, ungroundedStructuredNumbers("最大值999。"+citation, messages))
}

func TestStructuredNumbersIgnoreSourceFilenamesAndFormatPercentRatios(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"source":{"file":"棉花数据库(2).xlsx"},"aggregate":334158.1435,"valid_count":46,"ratio":0.5625,"actual_last_date":"2023-10-31"}`}}
	require.Empty(t, ungroundedStructuredNumbers("来自棉花数据库(2).xlsx，数量334158.1435；比例56.25%，10月31日。", messages))
}

func TestStructuredNumbersTreatShortCalendarDatesAsDates(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"rows":[[17790]],"valid_count":16}`}}
	require.Empty(t, ungroundedStructuredNumbers("9/25–9/30无记录；09-27无记录。共16条，最高17,790。", messages))
	require.Equal(t, []string{"27"}, ungroundedStructuredNumbers("共27条，最高17,790。", messages))
}

func TestStructuredNumbersValidateFullCalendarDatesWithoutMaskingDecimalsOrVersions(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","rows":[[54.96,58.32]]}`}}
	require.Empty(t, ungroundedStructuredNumbers("日期2024.3.31；数值54.96和58.32。", messages))
	require.Empty(t, ungroundedStructuredNumbers("日期2024-3-31；数值54.96和58.32。", messages))
	require.Empty(t, ungroundedStructuredNumbers("日期2024/3/31；数值54.96和58.32。", messages))
	require.Empty(t, ungroundedStructuredNumbers("日期2024年3月31日；数值54.96和58.32。", messages))
	require.Contains(t, ungroundedStructuredNumbers("数值54.97。", messages), "54.97", "ordinary decimal values remain checked")
}

func TestStructuredNumbersDoNotPartiallyMaskInvalidDatesOrVersionTokens(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","rows":[[54.96]]}`}}
	for _, candidate := range []string{
		"日期2023-2-29。", // valid month/day, but invalid in this year
		"日期2023年2月29日。",
		"日期2024.2.30。",
		"版本v2024.3.31。",
		"版本2024.3.31.2。",
	} {
		t.Run(candidate, func(t *testing.T) {
			require.NotEmpty(t, ungroundedStructuredNumbers(candidate, messages), "invalid dates and version-shaped values must remain subject to numeric checking")
		})
	}
}

func TestStructuredEvidenceRequiresAnExecutedResultNotAClarificationOrError(t *testing.T) {
	require.False(t, structuredResultHasEvidence(`{"kind":"structured_analysis","status":"error","error":"invalid SQL"}`))
	require.False(t, structuredResultHasEvidence(`{"kind":"structured_field_resolution","status":"needs_clarification","fields":[]}`))
	require.True(t, structuredResultHasEvidence(`{"kind":"structured_analysis","status":"ok","rows":[[30]]}`))
	require.True(t, structuredResultHasEvidence(`{"status":"no_data","actual_first_date":null,"valid_count":0}`))
}

func TestIndependentReviewPreservesUserAndToolFactsButNotAssistantRationalization(t *testing.T) {
	messages := []chat.Message{
		{Role: "system", Content: "只使用授权来源"},
		{Role: "user", Content: "按自然周统计全部已有记录"},
		{Role: "assistant", Content: "我打算自行排除不完整周"},
		{Role: "tool", Content: `{"kind":"structured_analysis","status":"ok","rows":[[17790],[17355]]}`},
	}
	review := independentStructuredReview(messages, "哪周最高？", "17355最高")
	require.Len(t, review, 2)
	require.NotContains(t, review[1].Content, "我打算自行排除")
	require.Contains(t, review[1].Content, "按自然周统计全部已有记录")
	require.Contains(t, review[1].Content, "17790")
	require.Contains(t, review[1].Content, "unverified_draft")
}

func TestIndependentReviewDuplicateResultsDoNotEvictDistinctSourceEvidence(t *testing.T) {
	first := `{"source":"first-required-period","padding":"` + strings.Repeat("x", 21000) + `"}`
	repeated := `{"source":"last-required-period","padding":"` + strings.Repeat("y", 20000) + `"}`
	messages := []chat.Message{{Role: "system", Content: "Source checks"}, {Role: "tool", Content: first},
		{Role: "tool", Content: repeated}, {Role: "tool", Content: repeated}}
	review := independentStructuredReview(messages, "compare both periods", "draft")
	var envelope struct {
		Evidence []reviewToolEvidence `json:"source_tool_results"`
	}
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &envelope))
	require.Len(t, envelope.Evidence, 2)
	for i, expected := range []string{first, repeated} {
		require.True(t, envelope.Evidence[i].ViewComplete)
		require.JSONEq(t, expected, string(mustJSON(t, envelope.Evidence[i].Content)))
	}
}

// This is the exact final model draft from the local cotton regression, with
// only its executed aggregate results and the 48 raw source cells retained.
// A correct statistic must not be discarded because its SHA or row labels are
// interpreted as additional statistics.
func TestStructuredNumbersAcceptActualSourceFollowupAndStillRejectAlteredTotal(t *testing.T) {
	fixtureBytes, err := os.ReadFile("testdata/structured_number_source_followup.json")
	require.NoError(t, err)
	var fixture struct {
		Answer      string           `json:"answer"`
		UserQuery   string           `json:"user_query"`
		ToolResults []map[string]any `json:"tool_results"`
	}
	require.NoError(t, json.Unmarshal(fixtureBytes, &fixture))
	messages := []chat.Message{{Role: "user", Content: fixture.UserQuery}}
	for _, result := range fixture.ToolResults {
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		messages = append(messages, chat.Message{Role: "tool", Content: string(encoded)})
	}
	require.Empty(t, ungroundedStructuredNumbers(fixture.Answer, messages))
	altered := strings.ReplaceAll(fixture.Answer, "777.0080629647059", "778")
	require.Contains(t, ungroundedStructuredNumbers(altered, messages), "778")
}

func TestStructuredNumbersOnlyPermitExactRealOpaqueIdentifiers(t *testing.T) {
	const digest = "108b08e3f40b1f2cb081ca406fdc4bd02415dce5ffd2c7426d078c79573bd1fe"
	const dataset = "424aed70d6a68ef7b97a067e"
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","status":"ok","dataset_id":"` + dataset + `","source":{"sha256":"` + digest + `"},"series":{"series_id":"chart_2023_source"},"rows":[[777.0080629647059]]}`}}
	require.Empty(t, ungroundedStructuredNumbers("合计777.0081；dataset_id="+dataset+"；SHA-256 `"+digest+"`；指标chart_2023_source。", messages))
	forged := strings.Repeat("a", 64)
	require.Contains(t, ungroundedStructuredNumbers("SHA256 "+forged+"；合计777.0081", messages), forged)
	require.Contains(t, ungroundedStructuredNumbers("原值777.0081，另一个统计778", messages), "778")
	// A source field named *_id may never grant a made-up decimal value.
	messages = append(messages, chat.Message{Role: "tool", Content: `{"dataset_id":"778","series_id":"779","sha256":"780","id":781}`})
	for _, claim := range []string{"778", "779", "780", "781"} {
		require.Contains(t, ungroundedStructuredNumbers("合计"+claim, messages), claim)
	}
}

func TestStructuredNumbersSourceRowLabelsDoNotPermitUncomputedStatistics(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_source_inspection","status":"ok","hash_verified":true,"sha256":"108b08e3f40b1f2cb081ca406fdc4bd02415dce5ffd2c7426d078c79573bd1fe","ranges":[{"cells":[{"cell":"C74","value":650}]}]}`}}
	require.Empty(t, ungroundedStructuredNumbers("| 行 | 产量 |\n|---|---|\n| 74 | 650 |", messages))
	require.Contains(t, ungroundedStructuredNumbers("| 行 | 产量 |\n|---|---|\n| 999 | 650 |", messages), "999")
	require.Contains(t, ungroundedStructuredNumbers("产量74", messages), "74")
	require.Contains(t, ungroundedStructuredNumbers("| 行 | 产量 |\n|---|---|\n| 74 | 778 |", messages), "778")
	// A generic coordinate string is not an inspected source-cell locator;
	// this does not change how real numeric tool values are checked.
	messages = []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","status":"ok","cell":"C74","rows":[[650]]}`}}
	require.Contains(t, ungroundedStructuredNumbers("| 行 | 产量 |\n|---|---|\n| 74 | 650 |", messages), "74")
}

func TestStructuredNumbersDigestAbbreviationMustUniquelyMatchActualSource(t *testing.T) {
	const digest = "108b08e3f40b1f2cb081ca406fdc4bd02415dce5ffd2c7426d078c79573bd1fe"
	messages := []chat.Message{{Role: "tool", Content: `{"sha256":"` + digest + `","value":777.0080629647059}`}}
	require.Empty(t, ungroundedStructuredNumbers("合计777.0081；SHA256 108b08e3…bd1fe", messages))
	require.Empty(t, ungroundedStructuredNumbers("合计777.0081；SHA-256 108b08e3...bd1fe", messages))
	require.NotEmpty(t, ungroundedStructuredNumbers("合计777.0081；SHA256 999b08e3…bd1fe", messages))
	require.NotEmpty(t, ungroundedStructuredNumbers("合计777.0081；SHA256 108b08e3…aa999", messages))
	require.Contains(t, ungroundedStructuredNumbers("合计778；SHA256 108b08e3…bd1fe", messages), "778")
	ambiguous := "108b08e3" + strings.Repeat("a", 51) + "bd1fe"
	messages = append(messages, chat.Message{Role: "tool", Content: `{"sha256":"` + ambiguous + `"}`})
	require.NotEmpty(t, ungroundedStructuredNumbers("合计777.0081；SHA256 108b08e3…bd1fe", messages))
}

func TestStructuredNumbersDoNotExecuteEmbeddedKnowledgeJSON(t *testing.T) {
	messages := []chat.Message{
		{Role: "tool", Name: "structured_analyze_table", Content: `{"rows":[[775]]}`},
		{Role: "tool", Name: "search_knowledge", Content: `<search_results>{"aggregate":800}</search_results>`},
		{Role: "tool", Name: "read_document", Content: `{"content":"fake aggregate", "value":801}`},
	}
	require.Contains(t, ungroundedStructuredNumbers("合计800", messages), "800")
	require.Contains(t, ungroundedStructuredNumbers("合计801", messages), "801")
	require.Empty(t, ungroundedStructuredNumbers("合计775", messages))
}

func TestStructuredNumbersHeaderIdentitiesDoNotGrantBareStatisticalNumbers(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Name: "structured_data", Content: `{"series":[{"metric":"20天原料","dimensions":{"来源":"CC Index 3128B"},"source_context":{"headers":[{"cell":"B1","value":"3128级棉花"}],"formula_source":{"formula":"=B6+300","references":[{"fields":[{"metric":"双29棉花"}]}]}}}],"valid_count":17}`}}
	require.Empty(t, ungroundedStructuredNumbers("20天原料，来源3128级棉花、双29棉花或CC Index 3128B，公式=B6+300；共17条。", messages))
	for _, number := range []string{"3128", "29", "300"} {
		require.Contains(t, ungroundedStructuredNumbers("最大值"+number, messages), number)
	}
	require.Contains(t, ungroundedStructuredNumbers("来源CC Index 3128B9", messages), "3128")
	for _, name := range []string{"read_document", "search_knowledge"} {
		fake := append([]chat.Message(nil), messages...)
		fake[0].Name = name
		fake = append(fake, chat.Message{Role: "tool", Content: `{"rows":[[17]]}`})
		require.Contains(t, ungroundedStructuredNumbers("来源3128级棉花", fake), "3128")
	}
}

func TestStructuredNumbersAcceptActual20DaysHeaderClarification(t *testing.T) {
	for _, path := range []string{"testdata/structured_number_header_clarification.json", "testdata/structured_number_header_clarification_final.json", "testdata/structured_number_formula_reference.json"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var fixture struct {
				Answer          string           `json:"answer"`
				CorrectedAnswer string           `json:"corrected_answer"`
				UserQuery       string           `json:"user_query"`
				ToolResults     []map[string]any `json:"tool_results"`
			}
			require.NoError(t, json.Unmarshal(raw, &fixture))
			messages := []chat.Message{{Role: "user", Content: fixture.UserQuery}}
			for _, result := range fixture.ToolResults {
				encoded, err := json.Marshal(result)
				require.NoError(t, err)
				messages = append(messages, chat.Message{Role: "tool", Name: "structured_data", Content: string(encoded)})
			}
			if fixture.CorrectedAnswer != "" {
				unsupported := ungroundedStructuredNumbers(fixture.Answer, messages)
				require.Contains(t, unsupported, "3128", "an invented shorthand must not become a numeric value")
				require.NotContains(t, unsupported, "+300", "an actual source formula may be rendered with spaces around equals")
				require.Empty(t, ungroundedStructuredNumbers(fixture.CorrectedAnswer, messages))
			} else {
				require.Empty(t, ungroundedStructuredNumbers(fixture.Answer, messages))
			}
			require.Contains(t, ungroundedStructuredNumbers(fixture.Answer+"最大值3128。", messages), "3128")
			if strings.Contains(path, "formula_reference") {
				require.Contains(t, ungroundedStructuredNumbers(fixture.Answer+"最大值80。", messages), "80")
			}
		})
	}
}

func TestStructuredLiteralCatalogIdentityAndInspectedRowReferencesAreExact(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"datasets":[{"literal_fields":[{"metric":"20天原料","dimensions":{"规格":"3128B"}}]}],"valid_count":17}`}, {Role: "tool", Content: `{"kind":"structured_source_inspection","status":"ok","hash_verified":true,"ranges":[{"cells":[{"cell":"C2823","value":"3128级棉花"},{"cell":"C26","value":17}]}]}`}}
	require.Empty(t, ungroundedStructuredNumbers("字段20天原料，规格3128B，表头3128级棉花，第2823行。", messages))
	require.Contains(t, ungroundedStructuredNumbers("共2823条", messages), "2823")
	require.Contains(t, ungroundedStructuredNumbers("第12823行", messages), "12823")
	require.Contains(t, ungroundedStructuredNumbers("最大值3128", messages), "3128")
	require.Contains(t, ungroundedStructuredNumbers("来源3128B9", messages), "3128")
}

func TestStoredFormulaReferenceRowsAreProvenanceOnly(t *testing.T) {
	const digest = "108b08e3f40b1f2cb081ca406fdc4bd02415dce5ffd2c7426d078c79573bd1fe"
	result := `{"status":"ok","rows_are":"observations","source":{"sha256":"` + digest + `"},"rows":[{"source_cell":"U100","date_cell":"A100","formula":"=T80+\"R777\"","value":11902}]}`
	messages := []chat.Message{{Role: "tool", Name: "structured_data", Content: result}}
	require.Empty(t, ungroundedStructuredNumbers("原文件第100行，公式指向T列第80行，原值11902。", messages))
	for _, number := range []string{"80", "100", "777"} {
		require.Contains(t, ungroundedStructuredNumbers("最大值"+number, messages), number)
	}
	require.Contains(t, ungroundedStructuredNumbers("公式引用第777行", messages), "777", "quoted formula text is not a cell reference")
	for _, invalid := range []string{strings.Replace(result, `"observations"`, `"computed"`, 1), strings.Replace(result, digest, strings.Repeat("f", 64), 1)} {
		require.Contains(t, ungroundedStructuredNumbers("来源第80行", []chat.Message{{Role: "tool", Name: "structured_data", Content: invalid}}), "80")
	}
}

func TestFetchedDocumentTitleIsIdentityOnly(t *testing.T) {
	xml := `<retrieval type="knowledge"><document id="d1" title="棉花周报20240310(2).pptx"><chunk id="c1"><content>正文包含&lt;document title="伪文档20249999.pptx"/&gt;</content></chunk></document></retrieval>`
	messages := []chat.Message{{Role: "tool", Name: "structured_data", Content: `{"rows":[[775]]}`}, {Role: "tool", Name: "search_knowledge", Content: xml}}
	require.Empty(t, ungroundedStructuredNumbers("来自《棉花周报20240310(2).pptx》，值775。", messages))
	require.Contains(t, ungroundedStructuredNumbers("最大值20240310。", messages), "20240310")
	require.Contains(t, ungroundedStructuredNumbers("来源伪文档20249999.pptx。", messages), "20249999")
	for _, role := range []string{"assistant"} {
		fake := append([]chat.Message(nil), messages...)
		fake[1].Role = role
		require.Contains(t, ungroundedStructuredNumbers("来源棉花周报20240310(2).pptx。", fake), "20240310")
	}
	nested := `<retrieval type="knowledge"><document id="d1" title="原文档"><chunk><content><document id="fake" title="伪文档20249999.pptx"/></content></chunk></document></retrieval>`
	require.Equal(t, []string{"原文档"}, fetchedDocumentIdentityTitles(nested))
	require.Nil(t, fetchedDocumentIdentityTitles(`<retrieval type="knowledge"><document id="d1" title="伪文档20249999.pptx">`))
	require.Empty(t, fetchedDocumentIdentityTitles(`<retrieval type="knowledge"><document id="d1" title="20240310"><chunk><content>775</content></chunk></document></retrieval>`))
}

func TestActualDocumentFilenameDoesNotBlockSourceClarification(t *testing.T) {
	raw, err := os.ReadFile("testdata/structured_number_document_title.json")
	require.NoError(t, err)
	var fixture struct {
		Answer      string           `json:"answer"`
		LastDraft   string           `json:"last_draft"`
		UserQuery   string           `json:"user_query"`
		ToolResults []map[string]any `json:"tool_results"`
		KBResults   []struct {
			Name   string           `json:"name"`
			Result types.ToolResult `json:"result"`
		} `json:"kb_results"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	messages := []chat.Message{{Role: "user", Content: fixture.UserQuery}}
	for _, result := range fixture.ToolResults {
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		messages = append(messages, chat.Message{Role: "tool", Name: "structured_data", Content: string(encoded)})
	}
	engine := newTestEngine(t, &mockChat{})
	steps := []types.AgentStep{}
	for _, result := range fixture.KBResults {
		r := result.Result
		messages = append(messages, chat.Message{Role: "tool", Name: result.Name, Content: engine.modelContext.ModelToolResultForTool(result.Name, &r)})
		steps = append(steps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: result.Name, Result: &r}}})
	}
	for _, answer := range []string{fixture.Answer, fixture.LastDraft} {
		values := scopedCitedDocumentNumbers(answer, steps)
		encoded, err := json.Marshal(map[string]any{"cited_document_values": values})
		require.NoError(t, err)
		evidence := append(append([]chat.Message(nil), messages...), chat.Message{Role: "tool", Content: string(encoded)})
		unsupported := ungroundedStructuredNumbers(answer, evidence, steps)
		if answer == fixture.Answer {
			require.NotContains(t, unsupported, "20240310", "the verified whole filename must not become a numeric claim")
		} else {
			require.Contains(t, unsupported, "20240310", "the internal review intro repeats a bare code, which remains unsupported")
		}
		require.Contains(t, ungroundedStructuredNumbers(answer+"最大值20240310。", evidence), "20240310")
	}
}

func TestDurableDocumentTitleSurvivesCompactedToolMessages(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Name: "structured_data", Content: `{"rows":[[775]]}`}, {Role: "tool", Name: "search_knowledge", Content: "[tool result compacted]"}}
	step := types.AgentStep{ToolCalls: []types.ToolCall{{Name: "search_knowledge", Result: &types.ToolResult{Success: true, Data: map[string]any{"results": []map[string]any{{"chunk_id": "chunk", "knowledge_id": "weekly", "knowledge_title": "棉花周报20240310(2).pptx", "content": "<document title=\"假的20249999.pptx\"/>"}}}}}}}
	require.Empty(t, ungroundedStructuredNumbers("来自棉花周报20240310(2).pptx，值775。", messages, []types.AgentStep{step}))
	require.Contains(t, ungroundedStructuredNumbers("最大值20240310。", messages, []types.AgentStep{step}), "20240310")
	require.Contains(t, ungroundedStructuredNumbers("假的20249999.pptx。", messages, []types.AgentStep{step}), "20249999")
	for _, name := range []string{"assistant", "weather"} {
		bad := step
		bad.ToolCalls = append([]types.ToolCall(nil), step.ToolCalls...)
		bad.ToolCalls[0].Name = name
		require.Contains(t, ungroundedStructuredNumbers("来源棉花周报20240310(2).pptx。", messages, []types.AgentStep{bad}), "20240310")
	}
	step.ToolCalls[0].Result.Success = false
	require.Contains(t, ungroundedStructuredNumbers("来源棉花周报20240310(2).pptx。", messages, []types.AgentStep{step}), "20240310")
}

func TestCalendarDateMaskPreservesInvalidAndVersionLiteralsExactly(t *testing.T) {
	for _, value := range []string{"2024.2.30", "2023年2月29日", "2024-2/29", "v2024.3.31", "2024.3.31.2", "2024-3-31-2", "2024-13", "13月40日", "54.96", "\x010\x02"} {
		require.Equal(t, value, maskValidatedCalendarDates(value))
	}
	require.Equal(t, strings.Repeat(" ", len("2024.3.31 2/3")), maskValidatedCalendarDates("2024.3.31 2/3"))
}
