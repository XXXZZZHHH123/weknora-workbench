package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestStructuredNumbersMaskOnlyExactVerifiedChartLabels(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Name: "structured_list_series", Content: `{"status":"ok","series":[{"metric":"库存","chart_legends":["31/32","41/42"],"chart_titles":["示例曲线51/52"],"chart_curve_sources":[{"label":"阶段61/62"}]}],"rows":[[777]]}`}}

	require.Empty(t, ungroundedStructuredNumbers("图例31/32–41/42；图表标题示例曲线51/52；曲线阶段61/62", messages))
	require.Equal(t, []string{"31"}, ungroundedStructuredNumbers("数值31", messages))
	require.Equal(t, []string{"71", "72"}, ungroundedStructuredNumbers("未知区间71/72", messages))
}

func TestStructuredNumbersDoNotUseFailedChartLabelsAsEvidence(t *testing.T) {
	messages := []chat.Message{
		{Role: "tool", Name: "structured_analyze_table", Content: `{"kind":"structured_analysis","status":"ok","rows":[[777]]}`},
		{Role: "tool", Name: "structured_list_series", Content: `{"status":"error","series":[{"chart_legends":["31/32"],"chart_titles":["31/32"],"chart_curve_sources":[{"label":"31/32"}]}]}`},
	}

	unsupported := ungroundedStructuredNumbers("图例31/32", messages)
	require.Contains(t, unsupported, "31")
	require.Contains(t, unsupported, "32")
}

func TestStructuredNumbersDoNotUseDescribeToolsChartLikeSchemaAsEvidence(t *testing.T) {
	messages := []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "schema-call", Type: "function", Function: chat.FunctionCall{Name: "structured_data", Arguments: `{"operation":"describe_tools"}`}}}},
		{Role: "tool", Name: "structured_data", ToolCallID: "schema-call", Content: `{"operation":"describe_tools","series":[{"chart_legends":["31/32"],"chart_titles":["31/32"],"chart_curve_sources":[{"label":"31/32"}]}]}`},
		{Role: "tool", Name: "structured_analyze_table", Content: `{"kind":"structured_analysis","status":"ok","rows":[[777]]}`},
	}

	unsupported := ungroundedStructuredNumbers("图例31/32", messages)
	require.Contains(t, unsupported, "31")
	require.Contains(t, unsupported, "32")
}
