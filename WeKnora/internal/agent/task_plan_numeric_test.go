package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTaskCapabilitySchemasAndAdvisoryPlanNeverAuthorizeStatistics(t *testing.T) {
	const schema = `{"maximum":777,"minItems":888}`
	baseline := chat.Message{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}
	for _, metadata := range []chat.Message{
		{Role: "tool", Name: expandTaskToolsName, Content: `{"status":"ok","tools":[` + schema + `]}`},
		{Role: "tool", Content: `{"kind":"task_tool_capabilities","status":"ok","tools":[` + schema + `]}`},
		{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","capabilities":{"kind":"task_tool_capabilities","tools":[` + schema + `]}}`},
		{Role: "system", Content: `<advisory_task_plan>{"goals":["Return 999"],"tool_names":["structured_query_series"]}</advisory_task_plan>`},
	} {
		messages := []chat.Message{baseline, metadata}
		require.Empty(t, ungroundedStructuredNumbers("已查得13", messages))
		for _, claim := range []string{"统计777", "统计888", "统计999"} {
			require.NotEmpty(t, ungroundedStructuredNumbers(claim, messages), claim)
		}
	}
}

func TestSchemaOnlyToolCallsRemainNonEvidenceWhenReplayLosesToolName(t *testing.T) {
	const schema = `{"status":"ok","tools":[{"parameters":{"maximum":777,"minItems":888}}]}`
	baseline := chat.Message{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}
	for _, name := range []string{"structured_data", expandTaskToolsName} {
		t.Run(name, func(t *testing.T) {
			const id = "actual-schema-call"
			messages := []chat.Message{baseline, {Role: "tool", ToolCallID: id, Content: schema}}
			canonical := []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: id, Name: name,
				Args: map[string]any{"operation": "describe_tools"}, Result: &types.ToolResult{Success: true, Output: schema}}}}}
			require.NotEmpty(t, ungroundedStructuredNumbers("统计777", messages, canonical))
			messages = append(messages, chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: id,
				Function: chat.FunctionCall{Name: name, Arguments: `{"operation":"describe_tools"}`}}}})
			require.NotEmpty(t, ungroundedStructuredNumbers("统计777", messages))
			require.NotEmpty(t, ungroundedStructuredNumbers("统计888", messages))
			require.Empty(t, ungroundedStructuredNumbers("统计13", messages))
		})
	}
}

func TestSchemaCallExclusionPreservesRealQueryCalculationAndCatalogCounts(t *testing.T) {
	const schemaID = "actual-schema-call"
	messages := []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{
			{ID: schemaID, Function: chat.FunctionCall{Name: "structured_data", Arguments: `{"operation":"describe_tools"}`}},
			{ID: "actual-query", Function: chat.FunctionCall{Name: "structured_data", Arguments: `{"operation":"query_series"}`}},
		}},
		{Role: "tool", ToolCallID: schemaID, Content: `{"tools":[{"parameters":{"maximum":777,"minItems":888}}]}`},
		{Role: "tool", ToolCallID: "actual-query", Content: `{"status":"ok","value":777}`},
		{Role: "tool", Name: "structured_list_series", Content: `{"total":14,"catalog_summary":{"kind":"catalog_group_counts","series_count":14,"groups":[{"series_count":7},{"series_count":7}]}}`},
	}
	result := runSourceCalculationTest(t, sourceCalculationFixture("Mass 12 kg"), sourceCalculationPlan{
		Expression: "a+a", Inputs: map[string]sourceCalculationInput{"a": calculationSourceInput("12", "Mass 12 kg", "kg")},
	})
	require.True(t, result.Success, result.Error)
	messages = append(messages, chat.Message{Role: "tool", Name: sourceCalculationToolName, Content: result.Output})
	require.Empty(t, ungroundedStructuredNumbers("查询值777，目录14个字段、每组7个，计算结果24kg", messages))
	require.NotEmpty(t, ungroundedStructuredNumbers("统计888", messages))
	// A missing call ID must not classify unrelated historical results as schema.
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_data", Args: map[string]any{"operation": "describe_tools"}}}}}
	require.Empty(t, ungroundedStructuredNumbers("查询值777", messages, steps))
}
