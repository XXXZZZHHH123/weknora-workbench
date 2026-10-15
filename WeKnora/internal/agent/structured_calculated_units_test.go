package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceCalculationNumericGuardKeepsVerifiedPercentageUnits(t *testing.T) {
	baseline := []chat.Message{{Role: "tool", Name: "structured_analyze_table", Content: `{"kind":"structured_analysis","status":"ok","rows":[[13]]}`}}
	for _, test := range []struct {
		status, unit, value, accepted, rejected string
	}{
		{"unknown", "", "2", "原值2，单位未知", "变化200%"},
		{"unknown", "", "0.2", "原值0.2，单位未知", "变化20%"},
		{"known", "kg", "2", "原值2kg", "变化200%"},
		{"known", "%", "2", "变化2%", "变化200%"},
		{"dimensionless", "", "0.02", "变化2%", "变化0.02%"},
	} {
		t.Run(test.status+"/"+test.unit+"/"+test.value, func(t *testing.T) {
			data := map[string]any{"kind": "source_calculation", "status": "ok", "calculations": []any{
				map[string]any{"value": test.value, "exact_value": test.value, "unit": test.unit, "unit_status": test.status,
					"row_index": 777, "quote": "888", "source_count": 999},
			}}
			raw, err := json.Marshal(data)
			require.NoError(t, err)
			for _, name := range []string{"calculate_source_values", "calculate_document_table"} {
				steps := []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: name, Result: &types.ToolResult{Success: true, Data: data, Output: string(raw)}}}}}
				require.Empty(t, ungroundedStructuredNumbers(test.accepted, baseline, steps), name)
				require.NotEmpty(t, ungroundedStructuredNumbers(test.rejected, baseline, steps), name)
				for _, claim := range []string{"合计777", "合计888", "合计999"} {
					require.NotEmpty(t, ungroundedStructuredNumbers(claim, baseline, steps), "provenance metadata cannot authorize statistics")
				}
				messages := append(append([]chat.Message(nil), baseline...), chat.Message{Role: "tool", Name: name, Content: string(raw)})
				require.Empty(t, ungroundedStructuredNumbers(test.accepted, messages))
				require.NotEmpty(t, ungroundedStructuredNumbers(test.rejected, messages))
			}
		})
	}
}

func TestSourceCalculationNumericGuardDoesNotPromoteUnitlessInputsToPercent(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Name: "calculate_source_values", Content: `{"kind":"source_calculation","status":"ok","calculations":[{"value":"2","exact_value":"2","unit":"","unit_status":"unknown","inputs":{"a":{"value":"12","unit":"","quote":"12"},"b":{"value":"6","unit":"","quote":"6"}}}]}`}}
	for _, claim := range []string{"结果2%", "结果200%", "输入1200%", "输入600%", "输入12%", "输入6%"} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, messages), claim)
	}
	require.Empty(t, ungroundedStructuredNumbers("原始输入12与6，计算结果2，单位未知。", messages))
	messages[0].Content = `{"kind":"source_calculation","status":"ok","calculations":[{"value":"0.25","exact_value":"1/4","unit":"","unit_status":"dimensionless","inputs":{"p":{"value":"125","unit":"%","quote":"125%"}}}]}`
	require.Empty(t, ungroundedStructuredNumbers("原报告比例125%；计算变化25%。", messages))
	require.NotEmpty(t, ungroundedStructuredNumbers("原报告比例12500%。", messages))
}

func TestExistingStructuredSQLPercentageEvidenceRetainsItsProtocol(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Name: "structured_analyze_table", Content: `{"kind":"structured_analysis","status":"ok","rows":[[0.5625,-2.43626]]}`}}
	require.Empty(t, ungroundedStructuredNumbers("比例56.25%，下降2.44%。", messages))
	require.NotEmpty(t, ungroundedStructuredNumbers("比例57%。", messages))
}

func TestDocumentTableExecutionCoverageCountsAreEvidenceWithoutPercentPermission(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Name: "calculate_document_table", Content: `{"kind":"source_calculation","status":"ok","selected_row_count":18,"calculated_row_count":17,"table_index":77,"row_start":88,"row_end":99,"calculations":[{"value":"0.2","unit":"","unit_status":"unknown"}]}`}}
	require.Empty(t, ungroundedStructuredNumbers("选定18行，17行完成计算。原值0.2，单位未知。", messages))
	for _, claim := range []string{"变化1800%", "变化1700%", "合计77", "合计88", "合计99"} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, messages), claim)
	}
}
