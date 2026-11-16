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

func chartAxisReviewResponse(t *testing.T, axis, assessment, issue string) *types.ChatResponse {
	t.Helper()
	issues := []string{}
	if issue != "" {
		issues = append(issues, issue)
	}
	content, err := json.Marshal(map[string]any{"chart_issues": issues, "pending_deliverables": []string{}, "required_x_type": axis, "assessment": assessment})
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

func TestChartReviewPreservesChartIssuesForRecovery(t *testing.T) {
	feedback := "The title claims the entire range, but only the initial observations were read."
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The axes are valid; the stated range still needs correction.", feedback)}
	e := newTestEngine(t, model)
	reviewer := e.chartPlanReviewer("Show the observed readings over their available range.", &types.AgentState{})
	table := chartAxisReviewTable(t)
	spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"}
	verdict, err := reviewer(chartContext(t), table, spec)
	require.Nil(t, verdict, "outstanding chart corrections cannot authorize delivery")
	require.ErrorContains(t, err, feedback, "the executing model must receive the actionable correction")
	model.response = chartAxisReviewResponse(t, "numeric", "The title accurately states the available range.", "")
	verdict, err = reviewer(chartContext(t), table, spec)
	require.NoError(t, err)
	require.NotNil(t, verdict)
	require.Len(t, model.seen, 2, "a rejected review must not be cached as approval")
}

func TestChartReviewPendingAnalysisCannotWaiveChartIssues(t *testing.T) {
	ctx := chartContext(t)
	issue := "The selected series uses the wrong source unit."
	content, err := json.Marshal(map[string]any{
		"required_x_type": "time", "assessment": "The image still needs a source mapping correction.",
		"chart_issues": []string{issue}, "pending_deliverables": []string{"Explain the trend in the final answer."},
	})
	require.NoError(t, err)
	model := &chartAxisReviewModel{response: &types.ChatResponse{Content: string(content), FinishReason: "stop"}}
	e := newTestEngine(t, model)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, writer, "session", "message")
	tool.reviewPlan = e.chartPlanReviewer("Draw the requested observations and explain the trend.", state)
	sources, _ := chartSources(state.RoundSteps)
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": sources[0].ID, "chart_type": "line", "x_type": "time", "x_column": 0, "y_columns": []int{2}})
	require.False(t, result.Success)
	require.Contains(t, result.Error, issue)
	require.NotContains(t, result.Error, "Explain the trend")
	require.Empty(t, writer.requests)
	require.Empty(t, tool.receipts)
	require.Empty(t, tool.renderReceipts)
}

func TestChartReviewRequiresBoundedSeparateIssueLists(t *testing.T) {
	for _, field := range []string{"chart_issues", "pending_deliverables"} {
		for _, test := range []struct {
			name  string
			items any
		}{
			{"null", nil},
			{"blank-item", []string{" "}},
			{"too-many-items", []string{"a", "b", "c", "d", "e"}},
			{"aggregate-length", []string{strings.Repeat("核", chartReviewFeedbackLimit), "改"}},
			{"wrong-type", "Not an array"},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				payload := map[string]any{"chart_issues": []string{}, "pending_deliverables": []string{}, "required_x_type": "numeric", "assessment": "Checked the actual source observations."}
				payload[field] = test.items
				content, err := json.Marshal(payload)
				require.NoError(t, err)
				model := &chartAxisReviewModel{response: &types.ChatResponse{Content: string(content), FinishReason: "stop"}}
				e := newTestEngine(t, model)
				validation, err := e.chartPlanReviewer("Draw actual observations.", &types.AgentState{})(chartContext(t), chartAxisReviewTable(t), chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"})
				require.Nil(t, validation)
				require.ErrorContains(t, err, "chart_review_invalid_protocol")
			})
		}
	}
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
	for _, name := range []string{"chart_issues", "pending_deliverables"} {
		list := properties[name].(map[string]any)
		require.Equal(t, float64(chartReviewIssueLimit), list["maxItems"])
		require.Equal(t, float64(chartReviewFeedbackLimit), list["items"].(map[string]any)["maxLength"])
	}
	require.NotContains(t, properties, "accepted")
	require.NotContains(t, properties, "complete_delivery")
	require.NotContains(t, properties, "feedback")
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
