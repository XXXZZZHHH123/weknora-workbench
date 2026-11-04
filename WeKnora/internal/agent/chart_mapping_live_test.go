package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type chartCellReadTool struct{ agenttools.BaseTool }

func (tool *chartCellReadTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return pdfCellChartFixture().Result, nil
}

// Opt-in real-model acceptance uses isolated source cells and an in-memory
// writer. No business knowledge, account conversation or model setting changes.
func TestChartMappingLiveContextualEngine(t *testing.T) {
	client := chartSourceLiveClient(t)
	writer := &chartSourceAcceptanceWriter{}
	e := newTestEngine(t, client)
	e.config.WorkbenchDefault = true
	e.config.TaskPlanningEnabled = true
	e.config.MaxIterations = 12
	e.config.MaxContextTokens = 131072
	e.config.MaxCompletionTokens = 3072
	thinking := false
	e.config.Thinking = &thinking
	e.config.ReasoningEffort = "off"
	e.toolRegistry = agenttools.NewToolRegistry()
	e.SetChartWriter(writer)
	e.toolRegistry.RegisterTool(&chartCellReadTool{BaseTool: agenttools.NewBaseTool("structured_read_pdf_table", "Read the authorized isolated Harvest measurement table from the prior conversation. Returns actual source cells, observed dates, units, source identity and coverage. The original two-row table is stored horizontally, including the dimension row. This is a read-only fixture; no file system or other data can be accessed.", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`))})
	question := "把刚才这个指标各期的变化画成折线图。用真实日期间隔，缺失保留断点，只要图。"
	history := []chat.Message{{Role: "user", Content: "请查看已授权的合成来源中Harvest的观测记录，单位kg。"}, {Role: "assistant", Content: "上轮讨论了该来源的Harvest记录，具体数值仍须重新读取真实来源确认，不能将这句说明当数值依据。"}}
	ctx, cancel := context.WithTimeout(chartContext(t), 150*time.Second)
	defer cancel()
	var state *types.AgentState
	defer func() {
		chartSourceWriteEvidence(t, "cell-map", client, writer, map[string]any{"state": state, "plan": e.taskPlan, "review": e.turnReview})
	}()
	var err error
	started := time.Now()
	state, err = e.Execute(ctx, "session", "message", question, history)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.True(t, state.IsComplete)
	require.Len(t, writer.requests, 1)
	require.Contains(t, state.FinalAnswer, chartSourceAcceptanceHandle)
	require.NotNil(t, e.taskPlan)
	require.NotNil(t, e.taskPlan.SourceDiscovery)
	require.Equal(t, "recover", e.taskPlan.SourceDiscovery.Mode)
	var successful *types.ToolResult
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			require.NotEqual(t, "search_knowledge", call.ExecutionName(), "recover must not search the short follow-up verbatim")
			if call.ExecutionName() == renderChartToolName && call.Result != nil && call.Result.Success && call.Result.Data["kind"] == "data_chart" {
				successful = call.Result
			}
		}
	}
	require.NotNil(t, successful)
	rows := successful.Data["selected_rows"]
	expected := [][]string{{"2024-02-03", "8.25"}, {"2024-02-06", ""}, {"2024-02-10", "11.5"}, {"2024-03-01", "9"}}
	actual, _ := json.Marshal(rows)
	want, _ := json.Marshal(expected)
	require.JSONEq(t, string(want), string(actual))
	columns := successful.Data["selected_columns"].([]chart.Column)
	require.Equal(t, "Period", columns[0].Name)
	require.Equal(t, "Harvest", columns[1].Name)
	spec := successful.Data["chart_spec"].(chart.Spec)
	require.Equal(t, "time", spec.XType)
	require.Equal(t, "line", spec.Type)
	require.Equal(t, candidateAccepted, e.turnReview.Last.Disposition)
	require.Equal(t, 1, e.turnReview.Checks)
	for _, call := range client.calls {
		require.False(t, call.Critic, "a verified pure-chart receipt should avoid duplicate whole-turn review")
	}
	t.Logf("model=%s elapsed_ms=%d rounds=%d checks=%d", client.GetModelName(), time.Since(started).Milliseconds(), len(state.RoundSteps), e.turnReview.Checks)
}

func TestChartMappingLiveRejectsUnrelatedAxis(t *testing.T) {
	client := chartSourceLiveClient(t)
	e := newTestEngine(t, client)
	e.config.WorkbenchDefault = true
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: "按真实观测日期画读数折线，只要图。"}}
	e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: "Use the actual observation dates as X and readings as Y."}}}
	tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}})
	ctx, cancel := context.WithTimeout(chartContext(t), 35*time.Second)
	defer cancel()
	// source_cell is a real source-location column, but it is not the requested
	// observation date. A plausible title and numeric Y must not authorize it.
	_, err := e.chartPlanReviewer("按真实观测日期画读数折线，只要图。", &types.AgentState{})(ctx, tables[0], chart.Spec{Type: "line", X: 1, Y: []int{2}, XType: "category", Title: "Reading trend"})
	require.ErrorContains(t, err, "未通过语义核验")
	chartSourceWriteEvidence(t, "wrong-axis", client, &chartSourceAcceptanceWriter{}, map[string]any{"rejected": true})
}
