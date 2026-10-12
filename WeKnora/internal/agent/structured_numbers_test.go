package agent

import (
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
