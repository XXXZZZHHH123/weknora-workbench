package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type chartAxisReviewModel struct {
	mockChat
	response *types.ChatResponse
	err      error
	seen     [][]chat.Message
	options  []*chat.ChatOptions
}

func (m *chartAxisReviewModel) Chat(_ context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	m.seen = append(m.seen, append([]chat.Message(nil), messages...))
	m.options = append(m.options, options)
	return m.response, m.err
}

func chartAxisReviewResponse(t *testing.T, axis, assessment, feedback string) *types.ChatResponse {
	t.Helper()
	content, err := json.Marshal(map[string]any{"accepted": true, "complete_delivery": true, "required_x_type": axis, "assessment": assessment, "feedback": feedback})
	require.NoError(t, err)
	return &types.ChatResponse{Content: string(content), FinishReason: "stop"}
}

func chartAxisReviewTable(t *testing.T) chart.Table {
	t.Helper()
	table := chart.Table{Columns: []chart.Column{{Name: "Position"}, {Name: "Reading", Unit: "kg"}}, Rows: [][]string{{"2", "4"}, {"8", ""}, {"11", "12"}}, Provenance: `{"kind":"isolated_source","source_version":"synthetic-version-a"}`, Notice: "Actual isolated source coordinates; a missing reading remains a gap.", Truncated: true}
	require.NoError(t, table.Seal("isolated-axis-source"))
	return table
}

func TestChartReviewSelectsAxisWithoutCandidateType(t *testing.T) {
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "Actual positions are continuous distances; preserve their unequal spacing and the missing reading.", "")}
	e := newTestEngine(t, model)
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: "Compare the measured readings over the actual observation positions."}, {Role: "user", Content: "Keep the true distances and missing measurements; only return a chart."}}
	table := chartAxisReviewTable(t)
	reviewer := e.chartPlanReviewer("Plot the change over the actual positions.", &types.AgentState{})
	for _, candidate := range []string{"category", "time", "numeric"} {
		spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: candidate, Title: "Measured readings"}
		verdict, err := reviewer(chartContext(t), table, spec)
		if candidate != "numeric" {
			require.ErrorContains(t, err, "独立核验要求numeric")
			require.Nil(t, verdict)
		} else {
			require.NoError(t, err)
			require.Equal(t, "numeric", verdict.RequiredXType)
			require.Equal(t, reviewDigest(spec), verdict.SpecHash)
			require.Equal(t, reviewDigest([]any{table, table.Rows}), verdict.EvidenceHash)
			require.True(t, verdict.FullTaskView)
		}
	}
	require.Len(t, model.seen, 1)
	// Changing only the candidate's axis reuses the independently accepted
	// decision, while the runtime still compares and binds the actual spec.
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.seen[0][1].Content), &input))
	spec := input["chart_spec"].(map[string]any)
	require.NotContains(t, spec, "x_type")
	require.Equal(t, "line", spec["chart_type"])
	require.Equal(t, float64(0), spec["x_column"])
	require.Equal(t, []any{float64(1)}, spec["y_columns"])
	rows, err := json.Marshal(input["selected_rows"])
	require.NoError(t, err)
	want, err := json.Marshal(table.Rows)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(rows))
	require.Equal(t, table.Provenance, input["source_provenance"])
	require.Equal(t, table.Notice, input["source_notice"])
	require.Equal(t, true, input["source_partial"])
	require.Contains(t, model.seen[0][1].Content, "Keep the true distances and missing measurements")
	require.Contains(t, model.seen[0][1].Content, `"unit":"kg"`)
}

func TestChartReviewAllowsActualCategoryComparison(t *testing.T) {
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "category", "The requested regions are discrete categories; compare their observed readings in the original order.", "")}
	e := newTestEngine(t, model)
	table := chartAxisReviewTable(t)
	table.Rows = [][]string{{"north", "4"}, {"south", "8"}, {"east", "12"}}
	require.NoError(t, table.Seal("isolated-region-source"))
	verdict, err := e.chartPlanReviewer("Compare the observed readings across these regions.", &types.AgentState{})(chartContext(t), table, chart.Spec{Type: "bar", X: 0, Y: []int{1}, XType: "category"})
	require.NoError(t, err)
	require.Equal(t, "category", verdict.RequiredXType)
	require.True(t, verdict.CompleteDelivery)
}

func TestChartReviewBudgetAndProtocolBounds(t *testing.T) {
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", strings.Repeat("核", chartReviewAssessmentLimit), "")}
	e := newTestEngine(t, model)
	reviewer := e.chartPlanReviewer("Use the actual observation positions.", &types.AgentState{})
	table := chartAxisReviewTable(t)
	spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"}
	_, err := reviewer(chartContext(t), table, spec)
	require.NoError(t, err)
	require.GreaterOrEqual(t, model.options[0].MaxCompletionTokens, 1024)
	require.NotNil(t, model.options[0].Thinking)
	require.False(t, *model.options[0].Thinking)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(model.options[0].Format, &schema))
	properties := schema["properties"].(map[string]any)
	require.Equal(t, float64(chartReviewAssessmentLimit), properties["assessment"].(map[string]any)["maxLength"])
	require.Equal(t, float64(chartReviewFeedbackLimit), properties["feedback"].(map[string]any)["maxLength"])
	for _, test := range []struct {
		name, axis, assessment, feedback, reason string
	}{
		{name: "long-assessment", axis: "numeric", assessment: strings.Repeat("核", chartReviewAssessmentLimit+1), reason: "chart_review_invalid_protocol"},
		{name: "long-feedback", axis: "numeric", assessment: "Check source evidence.", feedback: strings.Repeat("改", chartReviewFeedbackLimit+1), reason: "chart_review_invalid_protocol"},
		{name: "unknown-axis", axis: "invented", assessment: "The source values are known.", reason: "chart_review_invalid_axis_type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model.response = chartAxisReviewResponse(t, test.axis, test.assessment, test.feedback)
			// Exercise an actual invalid response, not the valid decision this
			// test's earlier reviewer has already retained for the same input.
			invalidReviewer := e.chartPlanReviewer("Use the actual observation positions.", &types.AgentState{})
			callsBefore := len(model.seen)
			verdict, err := invalidReviewer(chartContext(t), table, spec)
			require.Len(t, model.seen, callsBefore+1)
			require.Nil(t, verdict)
			require.ErrorContains(t, err, test.reason)
			require.ErrorContains(t, err, "绘图参数不变重试")
		})
	}
}

func TestChartReviewIncompleteFailuresDoNotSaveOrChangePlan(t *testing.T) {
	valid := chartAxisReviewResponse(t, "time", "The actual ISO observation dates and source readings match the task.", "")
	for _, test := range []struct {
		name, finish, content, reason       string
		err                                 error
		empty, toolCalls, canceled, expired bool
	}{
		{name: "token-limit", finish: "length", content: valid.Content, reason: "chart_review_output_limit"},
		{name: "provider-token-limit", finish: "max_tokens", content: valid.Content, reason: "chart_review_output_limit"},
		{name: "timeout", err: context.DeadlineExceeded, reason: "chart_review_timeout"},
		{name: "request-expired", content: valid.Content, expired: true, reason: "chart_review_timeout"},
		{name: "canceled", content: valid.Content, canceled: true, reason: "chart_review_canceled"},
		{name: "model-error", err: errors.New("PRIVATE_UPSTREAM_DIAGNOSTIC"), reason: "chart_review_model_error"},
		{name: "empty-response", empty: true, reason: "chart_review_empty_response"},
		{name: "unexpected-tools", content: valid.Content, toolCalls: true, reason: "chart_review_unexpected_tool_calls"},
		{name: "nonfinal-response", content: valid.Content, finish: "content_filter", reason: "chart_review_nonfinal_response"},
		{name: "partial-json", content: `{"accepted":true`, reason: "chart_review_invalid_protocol"},
		{name: "extra-json", content: valid.Content + `{}`, reason: "chart_review_invalid_protocol"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := chartContext(t)
			if test.canceled {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if test.expired {
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = expired
			}
			finish := test.finish
			if finish == "" {
				finish = "stop"
			}
			response := &types.ChatResponse{Content: test.content, FinishReason: finish, Usage: types.TokenUsage{CompletionTokens: chartReviewCompletionBudget}}
			if test.toolCalls {
				response.ToolCalls = []types.LLMToolCall{{ID: "unexpected-review-tool"}}
			}
			if test.empty {
				response = nil
			}
			model := &chartAxisReviewModel{response: response, err: test.err}
			e := newTestEngine(t, model)
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
			state := &types.AgentState{RoundSteps: steps}
			writer := &chartWriterFixture{}
			tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
			tool.reviewPlan = e.chartPlanReviewer("Draw using the actual observation dates and readings.", state)
			tables, _ := chartSources(steps)
			result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": tables[0].ID, "chart_type": "line", "x_type": "time", "x_column": 0, "y_columns": []int{2}})
			require.False(t, result.Success)
			require.Contains(t, result.Error, test.reason)
			require.Contains(t, result.Error, "绘图参数不变重试")
			require.Contains(t, result.Error, "不要为通过核验改变x_type")
			require.NotContains(t, result.Error, "PRIVATE_UPSTREAM_DIAGNOSTIC")
			require.Empty(t, writer.requests)
			require.Empty(t, tool.receipts)
			if response != nil && !test.canceled && !test.expired {
				require.Equal(t, chartReviewCompletionBudget, state.TurnUsage.CompletionTokens)
			} else if test.canceled || test.expired {
				require.Empty(t, model.seen)
				require.Zero(t, state.TurnUsage.CompletionTokens)
			}
		})
	}
}
