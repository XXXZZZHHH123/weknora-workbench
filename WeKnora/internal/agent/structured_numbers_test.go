package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
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
