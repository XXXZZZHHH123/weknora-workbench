package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChartReviewDecisionKeepsIndependentAxisAndRebindsActualSpec(t *testing.T) {
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The source positions are real distances with a missing measurement.", "")}
	e := newTestEngine(t, model)
	table := chartAxisReviewTable(t)
	reviewer := e.chartPlanReviewer("Preserve actual distances and missing readings.", &types.AgentState{})
	spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "category", Title: "Measured readings"}
	_, err := reviewer(chartContext(t), table, spec)
	require.ErrorContains(t, err, "独立核验要求numeric")
	require.Len(t, model.seen, 1)
	// A second incompatible model response must never reverse a decision whose
	// full task, actual source and non-axis spec have not changed.
	model.response = chartAxisReviewResponse(t, "category", "An opposite response would reverse the same decision.", "")
	spec.XType = "numeric"
	validation, err := reviewer(chartContext(t), table, spec)
	require.NoError(t, err)
	require.Len(t, model.seen, 1)
	require.Equal(t, reviewDigest(spec), validation.SpecHash)
	wrong := spec
	wrong.XType = "category"
	require.NotEqual(t, reviewDigest(wrong), validation.SpecHash)
	require.Equal(t, reviewDigest([]any{table, table.Rows}), validation.EvidenceHash)
	require.True(t, validation.FullTaskView)
	require.True(t, table.Truncated, "source limitations do not mean the task view was omitted")
	// Returned public metadata is not the stored private decision.
	validation.RequiredXType = "category"
	validation.EligibleXTypes[0] = "forged"
	validation.Assessment = "changed by caller"
	again, err := reviewer(chartContext(t), table, spec)
	require.NoError(t, err)
	require.Equal(t, "numeric", again.RequiredXType)
	require.Equal(t, []string{"category", "numeric"}, again.EligibleXTypes)
	require.NotEqual(t, validation.Assessment, again.Assessment)
	require.Len(t, model.seen, 1)
}

func TestChartReviewDecisionBindsFullTaskTableAndNonAxisSpec(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*AgentEngine, *chart.Table, *chart.Spec)
	}{
		{"source-id", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) { table.ID += "-new" }},
		{"source-version", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) {
			table.Provenance = `{"source_version":"synthetic-version-b"}`
		}},
		{"mapping-provenance", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) {
			table.Provenance += " changed original coordinates"
		}},
		{"selected-value-without-reseal", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) { table.Rows[1][1] = "7" }},
		{"unselected-last-row-cell-without-reseal", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) {
			table.Rows[2][2] = "different unselected source text"
		}},
		{"unselected-column-without-reseal", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) { table.Columns[2].Unit = "metres" }},
		{"selected-unit", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) { table.Columns[1].Unit = "tonnes" }},
		{"notice", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) {
			table.Notice += " Additional actual coverage limitation."
		}},
		{"partial", func(_ *AgentEngine, table *chart.Table, _ *chart.Spec) { table.Truncated = !table.Truncated }},
		{"title", func(_ *AgentEngine, _ *chart.Table, spec *chart.Spec) {
			spec.Title = "A different requested comparison"
		}},
		{"chart-type", func(_ *AgentEngine, _ *chart.Table, spec *chart.Spec) { spec.Type = "scatter" }},
		{"x-selection", func(_ *AgentEngine, _ *chart.Table, spec *chart.Spec) { spec.X, spec.Y = 1, []int{2} }},
		{"y-selection", func(_ *AgentEngine, _ *chart.Table, spec *chart.Spec) { spec.Y = []int{2} }},
		{"human-steering", func(e *AgentEngine, _ *chart.Table, _ *chart.Spec) {
			e.reviewConversation = append(e.reviewConversation, chat.Message{Role: "user", Content: "Use the updated comparison and complete original scope."})
		}},
		{"delivery-plan", func(e *AgentEngine, _ *chart.Table, _ *chart.Spec) {
			e.taskPlan.DeliveryRequirements[0].Criteria = "Also show the revised required scope."
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The actual positions and readings satisfy this source-bound task.", "")}
			e := newTestEngine(t, model)
			e.reviewConversationReady = true
			e.reviewConversation = []chat.Message{{Role: "user", Content: "Chart the measured positions and readings."}}
			e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: "Show the measured positions."}}}
			table := chartAxisReviewTable(t)
			table.Columns = append(table.Columns, chart.Column{Name: "Unselected source", Unit: "kg"})
			for i := range table.Rows {
				table.Rows[i][1] = []string{"4", "6", "12"}[i]
				table.Rows[i] = append(table.Rows[i], []string{"17", "18", "19"}[i])
			}
			require.NoError(t, table.Seal("synthetic-cache-full-source"))
			spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric", Title: "Readings"}
			reviewer := e.chartPlanReviewer("Chart actual positions and readings.", &types.AgentState{})
			_, err := reviewer(chartContext(t), table, spec)
			require.NoError(t, err)
			require.Len(t, model.seen, 1)
			test.change(e, &table, &spec)
			validation, err := reviewer(chartContext(t), table, spec)
			require.NoError(t, err)
			require.Len(t, model.seen, 2, "changed input requires an actual fresh model decision even if selected values or the source ID did not change")
			require.Equal(t, reviewDigest([]any{table, table.Rows}), validation.EvidenceHash)
			require.Equal(t, reviewDigest(spec), validation.SpecHash)
		})
	}
}

func TestChartReviewDecisionDoesNotCacheRejectedOrIncompleteResponses(t *testing.T) {
	valid := chartAxisReviewResponse(t, "numeric", "The actual source positions and measurements match.", "")
	for _, test := range []struct {
		name     string
		response *types.ChatResponse
		err      error
	}{
		{"rejected", &types.ChatResponse{Content: `{"accepted":false,"complete_delivery":false,"required_x_type":"numeric","assessment":"The selected measure is not the requested one.","feedback":"Select the requested source measure."}`, FinishReason: "stop"}, nil},
		{"feedback-not-cleared", chartAxisReviewResponse(t, "numeric", "The source still needs a revision.", "Retain the actual missing-data gap."), nil},
		{"invalid-json", &types.ChatResponse{Content: `{"accepted":true`, FinishReason: "stop"}, nil},
		{"missing-complete-delivery", &types.ChatResponse{Content: `{"accepted":true,"required_x_type":"numeric","assessment":"Actual measurements.","feedback":""}`, FinishReason: "stop"}, nil},
		{"ineligible-axis", chartAxisReviewResponse(t, "time", "The actual source has numeric positions.", ""), nil},
		{"output-limit", &types.ChatResponse{Content: valid.Content, FinishReason: "length"}, nil},
		{"model-error", valid, errors.New("isolated model failure")},
		{"empty-response", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &chartAxisReviewModel{response: test.response, err: test.err}
			e := newTestEngine(t, model)
			reviewer := e.chartPlanReviewer("Keep actual measured positions.", &types.AgentState{})
			table := chartAxisReviewTable(t)
			spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"}
			_, err := reviewer(chartContext(t), table, spec)
			require.Error(t, err)
			require.Len(t, model.seen, 1)
			model.response, model.err = valid, nil
			_, err = reviewer(chartContext(t), table, spec)
			require.NoError(t, err)
			require.Len(t, model.seen, 2)
		})
	}
}

func TestChartReviewDecisionRequiresCompleteTaskViewAndFreshTurn(t *testing.T) {
	for _, test := range []struct {
		name     string
		messages []chat.Message
	}{
		{"omitted-image", []chat.Message{{Role: "user", Content: "Use the actual reference.", Images: []string{"https://example.invalid/reference.png"}}}},
		{"cropped-text", []chat.Message{{Role: "user", Content: strings.Repeat("x", 24001)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The visible positions match the task.", "")}
			e := newTestEngine(t, model)
			e.reviewConversationReady, e.reviewConversation = true, test.messages
			reviewer := e.chartPlanReviewer("Chart actual measurements.", &types.AgentState{})
			for i := 0; i < 2; i++ {
				validation, err := reviewer(chartContext(t), chartAxisReviewTable(t), chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"})
				require.NoError(t, err)
				require.False(t, validation.FullTaskView)
			}
			require.Len(t, model.seen, 2)
		})
	}
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The actual positions match the task.", "")}
	e := newTestEngine(t, model)
	for _, query := range []string{"Chart actual measurements.", "Chart actual measurements.", "Use the revised task."} {
		reviewer := e.chartPlanReviewer(query, &types.AgentState{})
		_, err := reviewer(chartContext(t), chartAxisReviewTable(t), chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"})
		require.NoError(t, err)
	}
	require.Len(t, model.seen, 3, "even an identical request gets no decision from an earlier turn's closure")
}

func TestChartReviewDecisionRejectsCanceledCacheHitsAndEvictsChangedInput(t *testing.T) {
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The actual source positions match.", "")}
	e := newTestEngine(t, model)
	reviewer := e.chartPlanReviewer("Chart actual measurements.", &types.AgentState{})
	table := chartAxisReviewTable(t)
	spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "numeric"}
	_, err := reviewer(chartContext(t), table, spec)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(chartContext(t))
	cancel()
	_, err = reviewer(canceled, table, spec)
	require.ErrorContains(t, err, "chart_review_canceled")
	expired, expire := context.WithDeadline(chartContext(t), time.Now().Add(-time.Second))
	defer expire()
	_, err = reviewer(expired, table, spec)
	require.ErrorContains(t, err, "chart_review_timeout")
	require.Len(t, model.seen, 1)
	changed := table
	changed.Notice += " Changed actual source limitation."
	model.response = &types.ChatResponse{Content: `{"accepted":false,"complete_delivery":false,"required_x_type":"numeric","assessment":"The changed scope is insufficient.","feedback":"Resolve the actual scope limitation."}`, FinishReason: "stop"}
	_, err = reviewer(chartContext(t), changed, spec)
	require.Error(t, err)
	model.response = chartAxisReviewResponse(t, "numeric", "The restored actual source matches.", "")
	_, err = reviewer(chartContext(t), table, spec)
	require.NoError(t, err)
	require.Len(t, model.seen, 3, "the single-entry cache cannot retain an old decision across a changed input")
}

func TestChartReviewCachedAxisDoesNotPromoteIncompleteDelivery(t *testing.T) {
	ctx := chartContext(t)
	model := &chartAxisReviewModel{response: &types.ChatResponse{Content: `{"accepted":true,"complete_delivery":false,"required_x_type":"time","assessment":"These actual dates and readings are valid, but another requested deliverable is still needed.","feedback":""}`, FinishReason: "stop"}}
	e := newTestEngine(t, model)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, writer, "session", "message")
	tool.reviewPlan = e.chartPlanReviewer("Draw the actual readings and also produce the other required deliverable.", state)
	sources, _ := chartSources(state.RoundSteps)
	require.Len(t, sources, 1)
	args := map[string]any{"operation": "render", "source_id": sources[0].ID, "chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{2}}
	require.False(t, chartRun(t, tool, ctx, args).Success)
	args["x_type"] = "time"
	result := chartRun(t, tool, ctx, args)
	require.True(t, result.Success, result.Error)
	require.Len(t, model.seen, 1)
	require.Len(t, writer.requests, 1)
	require.False(t, result.Data["semantic_validation"].(*chartSemanticValidation).CompleteDelivery)
	require.Empty(t, tool.receipts, "an accepted axis decision cannot certify the missing deliverable")
}

func TestChartReviewCachedDecisionStillRequiresCurrentMappedSourceAndPrivateDelivery(t *testing.T) {
	ctx := chartContext(t)
	query := "Draw the readings over their actual observation dates; return only the chart."
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "time", "The selected original dates, readings, units and missing value satisfy this task.", "")}
	e := newTestEngine(t, model)
	e.toolRegistry = agenttools.NewToolRegistry()
	e.config.WorkbenchDefault = true
	e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: query}}}
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: query}}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{pdfCellChartFixture()}}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, writer, "session", "message")
	tool.reviewPlan = e.chartPlanReviewer(query, state)
	e.toolRegistry.RegisterTool(tool)
	inputs := pdfChartInputSources(state.RoundSteps)
	require.Len(t, inputs, 1)
	mapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": inputs[0].ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{1, 2, 3, 4}, "label_index": 0, "unit_index": 1}})
	require.True(t, mapped.Success, mapped.Error)
	id := mapped.Data["source_id"].(string)
	args := map[string]any{"operation": "render", "source_id": id, "chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{1}, "title": "Measured observations"}
	failed := chartRun(t, tool, ctx, args)
	require.False(t, failed.Success)
	require.Contains(t, failed.Error, "独立核验要求time")
	require.Empty(t, writer.requests)
	require.Empty(t, tool.receipts)
	require.Len(t, model.seen, 1)
	args["x_type"] = "time"
	// None of these execution failures can borrow the valid axis decision.
	args["source_id"] = "chart-source-forged"
	require.False(t, chartRun(t, tool, ctx, args).Success)
	args["source_id"] = id
	originalSteps := state.RoundSteps
	state.RoundSteps = nil
	require.False(t, chartRun(t, tool, ctx, args).Success)
	state.RoundSteps = originalSteps
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	record := data["record_sha256"]
	data["record_sha256"] = strings.Repeat("ef", 32)
	require.False(t, chartRun(t, tool, ctx, args).Success)
	data["record_sha256"] = record
	otherOwner := context.WithValue(ctx, types.UserIDContextKey, "another-user")
	otherOwner = types.WithPrincipal(otherOwner, types.Principal{Type: types.PrincipalWebUser, ID: "another-user"})
	require.NotEqual(t, types.SessionOwnerIDFromContext(ctx), types.SessionOwnerIDFromContext(otherOwner))
	require.False(t, chartRun(t, tool, otherOwner, args).Success)
	args["x_type"] = "numeric"
	require.False(t, chartRun(t, tool, ctx, args).Success, "the renderer must still reject an ineligible encoding before consulting the cached decision")
	args["x_type"] = "time"
	require.Empty(t, writer.requests)
	require.Empty(t, tool.receipts)
	require.Len(t, model.seen, 1)
	model.response = chartAxisReviewResponse(t, "category", "An opposite response must not reverse the unchanged accepted decision.", "")
	corrected := chartRun(t, tool, ctx, args)
	require.True(t, corrected.Success, corrected.Error)
	require.Len(t, model.seen, 1)
	require.Len(t, writer.requests, 1)
	require.Len(t, tool.receipts, 1)
	require.Equal(t, mapped.Data["rows"], corrected.Data["selected_rows"])
	validation := corrected.Data["semantic_validation"].(*chartSemanticValidation)
	require.Equal(t, "time", validation.RequiredXType)
	require.Equal(t, reviewDigest(corrected.Data["chart_spec"]), validation.SpecHash)
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "chart-call", Name: renderChartToolName, Args: args, Result: corrected}}})
	require.True(t, e.isVerifiedChartDelivery(query, corrected.Output, state, nil))
	require.False(t, e.isVerifiedChartDelivery("Changed human task.", corrected.Output, state, nil))
	decision := e.checkCandidate(ctx, query, corrected.Output, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.True(t, e.candidateApprovalCurrent(corrected.Output, state))
	require.Len(t, model.seen, 1, "final delivery uses the exact runtime receipt, not another semantic call")
}
