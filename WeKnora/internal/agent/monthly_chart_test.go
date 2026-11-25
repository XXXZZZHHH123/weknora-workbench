package agent

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Match the nine reported monthly averages and the actual analysis envelope.
func monthlyChartSource() types.ToolCall {
	return types.ToolCall{ID: "monthly-query", Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"kind": "structured_analysis", "status": "ok", "columns": []string{"月份", "月均收盘价"},
		"rows": [][]any{{"2023-01", 14521.5625}, {"2023-02", 14572.25}, {"2023-03", 14266.521739130434},
			{"2023-04", 14919.736842105263}, {"2023-05", 15734.25}, {"2023-06", 16611.25},
			{"2023-07", 17025.0}, {"2023-08", 17211.739130434784}, {"2023-09", 17280.625}},
		"source":           map[string]any{"file": "fixture.xlsx", "sha256": strings.Repeat("ab", 32)},
		"sql":              `SELECT strftime(date, '%Y-%m') AS "月份", avg(value) AS "月均收盘价" FROM data GROUP BY 1 ORDER BY 1`,
		"output_truncated": false, "input_complete": true, "returned_row_count": 9, "input_count": 178,
		"requested_range": map[string]any{"start_date": "2023-01-01", "end_date": "2023-12-31"},
		"inputs": map[string]any{"fixture-series": map[string]any{
			"actual_first_date": "2023-01-03", "actual_last_date": "2023-09-22", "valid_last_date": "2023-09-22",
			"observation_coverage_scope": "loaded_analysis_inputs_before_user_sql",
		}},
	}}}
}

func monthlyRenderTool(t *testing.T, ctx context.Context, source types.ToolCall) (*renderChartTool, *chartWriterFixture, *types.AgentState, map[string]any) {
	t.Helper()
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{source}}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, writer, "session", "message")
	tables, _ := chartSources(state.RoundSteps)
	require.Len(t, tables, 1)
	return tool, writer, state, map[string]any{"operation": "render", "source_id": tables[0].ID, "chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{1}, "title": "月均收盘价"}
}

func TestMonthlyCategoryChartSavesNinePointsAndDeliversCoverage(t *testing.T) {
	const question = "画今年的月均收盘价。"
	const caption = "这份来源的有效记录截至2023年9月22日，2023年10—12月没有有效记录。"
	const pending = "说明九月的月均值只使用截至当日的记录。"
	for _, needsNote := range []bool{true, false} {
		name := "caption completes chart-only delivery"
		notes := "[]"
		if needsNote {
			name, notes = "coverage note pending", `["`+pending+`"]`
		}
		t.Run(name, func(t *testing.T) {
			ctx := chartContext(t)
			tool, writer, state, args := monthlyRenderTool(t, ctx, monthlyChartSource())
			model := &chartAxisReviewModel{response: &types.ChatResponse{Content: `{"required_x_type":"category","assessment":"各点为相同系列的连续月均值，范围已有图注。","chart_issues":[],"pending_deliverables":` + notes + `}`, FinishReason: "stop"}}
			e := newTestEngine(t, model)
			e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: question}}}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(tool)
			tool.reviewPlan = e.chartPlanReviewer(question, state)
			result := chartRun(t, tool, ctx, args)
			require.True(t, result.Success, result.Error)
			require.Len(t, writer.requests, 1)
			_, err := png.Decode(bytes.NewReader(writer.requests[0].PNG))
			require.NoError(t, err)
			rows := result.Data["selected_rows"].([][]string)
			require.Len(t, rows, 9)
			require.Equal(t, []string{"2023-03", "14266.521739130434"}, rows[2])
			require.Equal(t, []string{"2023-09", "17280.625"}, rows[8])
			require.Equal(t, caption, result.Data["coverage_caption"])
			require.Contains(t, result.Output, caption)
			require.Contains(t, model.seen[0][1].Content, caption)
			require.Contains(t, model.seen[0][0].Content, "逐月比较或展示月度走势可用category")
			require.Contains(t, model.seen[0][0].Content, "准确标题、图注或随图文字")
			validation := result.Data["semantic_validation"].(*chartSemanticValidation)
			require.Equal(t, !needsNote, validation.CompleteDelivery)
			step := types.AgentStep{ToolCalls: []types.ToolCall{{ID: "chart-call", Name: renderChartToolName, Args: args, Result: result}}}
			if needsNote {
				require.Equal(t, []string{pending}, validation.PendingDeliverables)
				require.Contains(t, result.Output, pending)
				nextMessages := e.appendToolResults(nil, step)
				require.Contains(t, nextMessages[len(nextMessages)-1].Content, pending)
				require.Empty(t, tool.receipts)
				return
			}
			state.RoundSteps = append(state.RoundSteps, step)
			candidate, ok := e.narrowVerifiedChartCandidate(question, "![图表](resource://abcdefghijklmnopqrstuv)", state, nil)
			require.True(t, ok)
			require.Equal(t, result.Output, candidate)
			require.True(t, e.isVerifiedChartDelivery(question, candidate, state, nil))
		})
	}
}

func TestMonthlyCategoryChartStillBlocksSemanticIssuesAndMixedUnits(t *testing.T) {
	for _, test := range []struct {
		name, issue string
	}{
		{"mixed contracts", "月均值把多个交割合约合在一起。"},
		{"wrong aggregation", "图画的是月末值，用户要求月均值。"},
		{"interior gap", "四月缺失，折线跨过缺口且没有说明。"},
		{"overstated coverage", "标题声称覆盖全年，来源只覆盖部分月份。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := chartContext(t)
			source := monthlyChartSource()
			data := source.Result.Data
			rows := data["rows"].([][]any)
			switch test.name {
			case "mixed contracts":
				inputs := data["inputs"].(map[string]any)
				inputs["another-contract"] = inputs["fixture-series"]
			case "wrong aggregation":
				data["columns"] = []string{"月份", "月末收盘价"}
				data["sql"] = `SELECT strftime(date, '%Y-%m') AS "月份", arg_max(value, date) AS "月末收盘价" FROM data GROUP BY 1 ORDER BY 1`
				lastClose := []int{15100, 14365, 14325, 15485, 15500, 16660, 17440, 17660, 17110}
				for i, row := range rows {
					row[1] = lastClose[i]
				}
			case "interior gap":
				data["rows"] = append(rows[:3], rows[4:]...)
				data["returned_row_count"] = 8
			}
			tool, writer, state, args := monthlyRenderTool(t, ctx, source)
			if test.name == "overstated coverage" {
				args["title"] = "包含全年十二个月的月均收盘价"
			}
			model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "category", "检查图中的所选数据与用户要求。", test.issue)}
			tool.reviewPlan = newTestEngine(t, model).chartPlanReviewer("画全年的月均收盘价。", state)
			result := chartRun(t, tool, ctx, args)
			require.False(t, result.Success)
			require.Contains(t, result.Error, test.issue)
			require.Empty(t, writer.requests)
			require.Len(t, model.seen, 1, "semantic issues do not retry")
		})
	}
	t.Run("different series units", func(t *testing.T) {
		ctx := chartContext(t)
		source := chartLongFixture()
		source.Result.Data["rows"] = [][]any{{"2023-01", "原字段甲", "元", 2}, {"2023-01", "原字段乙", "吨", 3}, {"2023-02", "原字段甲", "元", 5}}
		tool, writer, _, args := monthlyRenderTool(t, ctx, source)
		args["y_columns"], args["series_column"], args["unit_column"] = []int{3}, 1, 2
		tool.reviewPlan = func(context.Context, chart.Table, chart.Spec) (*chartSemanticValidation, error) {
			t.Fatal("mixed units cannot reach the reviewer")
			return nil, nil
		}
		result := chartRun(t, tool, ctx, args)
		require.False(t, result.Success)
		require.Contains(t, result.Error, "显式单位不同")
		require.Empty(t, writer.requests)
	})
}

type monthlyReviewModel struct {
	chartAxisReviewModel
	deadlines  []time.Time
	afterFirst func()
	recovery   *types.ChatResponse
}

func (m *monthlyReviewModel) Chat(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	deadline, _ := ctx.Deadline()
	m.deadlines = append(m.deadlines, deadline)
	response, err := m.chartAxisReviewModel.Chat(ctx, messages, options)
	if len(m.seen) == 1 && m.afterFirst != nil {
		m.afterFirst()
	}
	if len(m.seen) == 2 && m.recovery != nil {
		return m.recovery, nil
	}
	return response, err
}

func TestMonthlyChartProtocolRetryAndProgressUseTheSameFailureCounts(t *testing.T) {
	invalid := &types.ChatResponse{Content: `{"chart_issues":`, FinishReason: "stop"}
	for _, mode := range []string{"recover", "always invalid", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			deadline := time.Now().Add(5 * time.Second)
			ctx, cancel := context.WithDeadline(chartContext(t), deadline)
			defer cancel()
			tool, writer, state, args := monthlyRenderTool(t, ctx, monthlyChartSource())
			model := &monthlyReviewModel{chartAxisReviewModel: chartAxisReviewModel{response: invalid}}
			if mode == "recover" {
				model.recovery = chartAxisReviewResponse(t, "category", "月度汇总与覆盖说明符合请求。", "")
			} else if mode == "canceled" {
				model.afterFirst = cancel
			}
			tool.reviewPlan = newTestEngine(t, model).chartPlanReviewer("画月均收盘价。", state)
			var guard toolProgressGuard
			render := func() (*types.ToolResult, toolProgressObservation) {
				result := chartRun(t, tool, ctx, args)
				progress := guard.observe([]types.ToolCall{{Name: renderChartToolName, Args: args, Result: result}})
				require.Equal(t, tool.renderFailures, guard.renderFailures)
				require.Equal(t, tool.reviewProtocolFailures, guard.reviewProtocolFailures)
				return result, progress
			}
			result, progress := render()
			require.False(t, progress.RenderLimit)
			if mode == "canceled" {
				require.False(t, result.Success)
				require.Len(t, model.seen, 1)
				require.Empty(t, writer.requests)
				return
			}
			require.Len(t, model.seen, 2)
			require.Equal(t, model.seen[0], model.seen[1])
			require.Equal(t, []time.Time{deadline, deadline}, model.deadlines)
			require.Zero(t, tool.renderFailures)
			if mode == "recover" {
				require.True(t, result.Success, result.Error)
				require.Equal(t, 1, tool.reviewProtocolFailures)
				require.Len(t, writer.requests, 1)
				require.Equal(t, reviewDigest(result.Data), tool.renderReceipts["resource://abcdefghijklmnopqrstuv"].DataHash)
				return
			}
			require.False(t, result.Success)
			require.Equal(t, 2, tool.reviewProtocolFailures)
			// A valid protocol with a semantic rejection spends only its own allowance.
			model.response = chartAxisReviewResponse(t, "category", "所选业务口径不符。", "请绘制指定系列。")
			for i := 1; i <= maxChartRenderFailures; i++ {
				result, progress = render()
				require.Equal(t, i, tool.renderFailures)
				require.Equal(t, i == maxChartRenderFailures, progress.RenderLimit)
			}
			require.Equal(t, chartRenderFailureLimit, result.Data["terminal_reason"])
			calls := len(model.seen)
			_, progress = render()
			require.True(t, progress.RenderLimit)
			require.Len(t, model.seen, calls)
			require.Empty(t, writer.requests)
		})
	}
}
