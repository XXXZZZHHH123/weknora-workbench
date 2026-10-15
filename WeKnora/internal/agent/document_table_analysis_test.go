package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const analysisTableFixture = "Unit: kg\n| Label | Before | After |\n| --- | --- | --- |\n| Region A | 10 | 12 |\n| Region B | 4 | 3 |\n| All regions | 14 | 15 |\n"

func analysisSteps(content string) []types.AgentStep {
	return documentReviewFixture("read_document", true, map[string]any{"chunks": []any{map[string]any{"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "content": content}}})
}
func analysisReadyPlan(t *testing.T, plans ...map[string]any) string {
	t.Helper()
	if plans == nil {
		plan := tableCalculationPlan()
		plan["row_end"] = 3
		plans = []map[string]any{plan}
	}
	encoded, err := json.Marshal(map[string]any{"status": "ready", "message": "", "plans": plans})
	require.NoError(t, err)
	return string(encoded)
}
func analysisPlannerReply(content string) documentTablePlanFunc {
	return func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
		return &types.ChatResponse{Content: content, FinishReason: "stop"}, nil
	}
}
func runDocumentAnalysis(t *testing.T, tool *documentTableAnalysisTool, args string) *types.ToolResult {
	t.Helper()
	result, err := tool.Execute(t.Context(), json.RawMessage(args))
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func TestDocumentTableAnalysisPlansOnceFromTrustedFullRequestAndCompleteActualRows(t *testing.T) {
	trusted := `{"question":"比较所有原始记录的变化并保留来源。","context":["必须包含汇总记录。"],"explicit_source":"actual-document"}`
	steps := analysisSteps(analysisTableFixture)
	calls := 0
	planner := func(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
		calls++
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 20*time.Second)
		require.Len(t, messages, 2)
		require.Contains(t, messages[0].Content, string(documentTableAnalysisPlanSchema), "JSON-mode compatibility must not hide the plan contract from the isolated model")
		var input map[string]any
		require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &input))
		require.Equal(t, trusted, input["server_trusted_request"])
		require.NotContains(t, input, "technical_subtask", "a generated subtask cannot change the actual human task")
		require.Contains(t, messages[1].Content, "All regions")
		require.Contains(t, messages[1].Content, "Unit: kg")
		rows := input["table_rows"].([]any)[0].(map[string]any)["content"].(map[string]any)["rows"].([]any)
		require.Len(t, rows, 4, "actual full rows include headers and aggregate record")
		require.True(t, input["table_rows_scope"].(map[string]any)["view_complete"].(bool))
		require.Zero(t, opts.Temperature)
		require.False(t, *opts.Thinking)
		require.Equal(t, chat.ReasoningEffort("off"), opts.ReasoningEffort)
		require.Equal(t, "none", opts.ToolChoice)
		require.Empty(t, opts.Tools)
		require.JSONEq(t, string(documentTableAnalysisPlanSchema), string(opts.Format))
		return &types.ChatResponse{Content: analysisReadyPlan(t), FinishReason: "stop"}, nil
	}
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return steps }, func() string { return trusted }, planner)
	result := runDocumentAnalysis(t, tool, `{}`)
	require.True(t, result.Success, result.Output)
	require.Equal(t, "ok", result.Data["status"])
	require.Equal(t, true, result.Data["execution_complete"])
	require.Equal(t, 1, calls)
	executions := result.Data["executions"].([]map[string]any)
	require.Len(t, executions, 1)
	require.Equal(t, documentTableCalculationToolName, executions[0]["tool_name"])
	require.Equal(t, true, executions[0]["success"])
	data := executions[0]["result"].(map[string]any)
	require.Equal(t, "source_calculation", data["kind"])
	require.Equal(t, "actual-chunk", data["chunk_id"])
	require.Equal(t, 3, data["calculated_row_count"])
	rows := data["calculations"].([]any)
	require.Equal(t, "20", rows[0].(map[string]any)["value"])
	require.Equal(t, "-25", rows[1].(map[string]any)["value"])
	require.Equal(t, "All regions", rows[2].(map[string]any)["row_label"])
	require.Contains(t, rows[0].(map[string]any)["inputs"].(map[string]any), "a")
	require.NotContains(t, result.Data, "plans", "model plans are never emitted as executed evidence")
	require.NotContains(t, result.Data, "message")
}

func TestDocumentTableAnalysisCannotReplaceTrustedQuestionOrAcceptExternalPlans(t *testing.T) {
	called := false
	planner := func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
		called = true
		return nil, nil
	}
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual full task" }, planner)
	for _, args := range []string{`{"question":"other task"}`, `{"subtask":"add an unrelated comparison"}`, `{"plans":[]}`, `{"chunk_id":"guessed"}`, `{} trailing`, `null`} {
		result := runDocumentAnalysis(t, tool, args)
		require.False(t, result.Success)
		require.Equal(t, "error", result.Data["status"])
	}
	require.False(t, called)
}

func TestDocumentTableAnalysisRetainsExistingUnitSourceRangeAndExpressionValidation(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(plan map[string]any) { plan["row_end"] = 99 },
		func(plan map[string]any) { plan["expression"] = "12/10-1" },
		func(plan map[string]any) { plan["expression"] = "a/b*100" },
		func(plan map[string]any) {
			plan["columns"] = map[string]any{"a": map[string]any{"column": 2, "unit": "invented"}, "b": map[string]any{"column": 1, "unit": "invented"}}
		},
	} {
		plan := tableCalculationPlan()
		change(plan)
		tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual question" }, analysisPlannerReply(analysisReadyPlan(t, plan)))
		result := runDocumentAnalysis(t, tool, `{}`)
		require.False(t, result.Success, result.Output)
		require.Equal(t, "error", result.Data["status"])
		executions := result.Data["executions"].([]map[string]any)
		require.Len(t, executions, 1)
		require.Equal(t, false, executions[0]["success"])
		require.Equal(t, "error", executions[0]["result"].(map[string]any)["status"])
		require.Contains(t, executions[0], "attempted_request", "failed source and unit choices remain auditable without becoming numerical evidence")
	}
	unread := tableCalculationPlan()
	unread["chunk_id"] = "unread"
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual question" }, analysisPlannerReply(analysisReadyPlan(t, unread)))
	result := runDocumentAnalysis(t, tool, `{}`)
	require.Equal(t, "needs_source", result.Data["status"])
	require.Empty(t, result.Data["executions"])
}

func TestDocumentTableAnalysisDoesNotCallPartialSelectedRowsComplete(t *testing.T) {
	content := strings.Replace(analysisTableFixture, "| Region B | 4 | 3 |", "| Region B | 4 | #N/A |", 1)
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(content) }, func() string { return "actual question" }, analysisPlannerReply(analysisReadyPlan(t)))
	result := runDocumentAnalysis(t, tool, `{}`)
	require.False(t, result.Success, result.Output)
	require.Equal(t, "partial", result.Data["status"])
	require.Equal(t, false, result.Data["execution_complete"])
	child := result.Data["executions"].([]map[string]any)[0]["result"].(map[string]any)
	require.Equal(t, 3, child["selected_row_count"])
	require.Equal(t, 2, child["calculated_row_count"])
	require.Len(t, child["failed_rows"], 1)
}

func TestDocumentTableAnalysisNeedsSourceAndClarifyCarryAdviceNotValues(t *testing.T) {
	for _, status := range []string{"needs_source", "clarify"} {
		response := `{"status":"` + status + `","message":"需要明确哪一份实际来源或补齐表头。","plans":[]}`
		tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual question" }, analysisPlannerReply(response))
		result := runDocumentAnalysis(t, tool, `{}`)
		require.Equal(t, status, result.Data["status"])
		require.Empty(t, result.Data["executions"])
		require.Contains(t, result.Data, "advice_notice")
		require.NotContains(t, result.Data, "values")
	}
	called := false
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return nil }, func() string { return "actual question" }, func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
		called = true
		return nil, nil
	})
	result := runDocumentAnalysis(t, tool, `{}`)
	require.Equal(t, "needs_source", result.Data["status"])
	require.False(t, called)
}

func TestDocumentTableAnalysisProjectionRetainsMetadataAndRefusesClippedTablePlan(t *testing.T) {
	content := analysisTableFixture + strings.Repeat("| Another detail with very long original label | 12 | 13 |\n", 3000)
	steps := analysisSteps(content)
	metadata, metaScope, rows, rowScope, eligible := documentTableAnalysisCatalog(steps)
	encoded, _ := json.Marshal(rows)
	require.LessOrEqual(t, len(encoded), 48000)
	require.False(t, rowScope.ViewComplete)
	require.Contains(t, string(encoded), "omitted_paths")
	require.True(t, metaScope.ViewComplete)
	require.Len(t, metadata, 1)
	require.Empty(t, eligible)
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return steps }, func() string { return "actual question" }, analysisPlannerReply(analysisReadyPlan(t)))
	result := runDocumentAnalysis(t, tool, `{}`)
	require.Equal(t, "needs_source", result.Data["status"])
	require.Empty(t, result.Data["executions"], "a guessed plan from clipped rows must not execute")
}

func TestDocumentTableAnalysisStrictPlanAndBoundedFailure(t *testing.T) {
	for _, response := range []string{
		`{}`, `{"status":"ready","message":"","plans":[]}`, `{"status":"ready","message":"","plans":[]} trailing`,
		`{"status":"needs_source","message":"","plans":[]}`,
		strings.Replace(analysisReadyPlan(t), `"status":"ready"`, `"status":"ready","answer":"not a plan"`, 1),
	} {
		tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual question" }, analysisPlannerReply(response))
		result := runDocumentAnalysis(t, tool, `{}`)
		require.False(t, result.Success)
		require.Equal(t, "error", result.Data["status"])
	}
	for _, planner := range []documentTablePlanFunc{
		func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
			return nil, errors.New("unavailable")
		},
		func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: analysisReadyPlan(t), FinishReason: "length"}, nil
		},
	} {
		tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual question" }, planner)
		result := runDocumentAnalysis(t, tool, `{}`)
		require.False(t, result.Success)
		require.Empty(t, result.Data["executions"])
	}
}

func TestDocumentTableAnalysisExecutesMultiplePlansWithoutAdditionalModelRounds(t *testing.T) {
	original := tableCalculationPlan()
	original["columns"] = map[string]any{"a": map[string]any{"column": 1, "unit": "kg"}}
	original["expression"] = "a*1"
	original["result_unit"] = ""
	change := tableCalculationPlan()
	calls := 0
	tool := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "Return source values and requested changes from actual records." }, func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
		calls++
		return &types.ChatResponse{Content: analysisReadyPlan(t, original, change), FinishReason: "stop"}, nil
	})
	result := runDocumentAnalysis(t, tool, `{}`)
	require.True(t, result.Success, result.Output)
	require.Equal(t, 1, calls)
	executions := result.Data["executions"].([]map[string]any)
	require.Len(t, executions, 2)
	originalRows := executions[0]["result"].(map[string]any)["calculations"].([]any)
	require.Equal(t, "10", originalRows[0].(map[string]any)["value"])
	require.Equal(t, "kg", originalRows[0].(map[string]any)["unit"])
	changeRows := executions[1]["result"].(map[string]any)["calculations"].([]any)
	require.Equal(t, "20", changeRows[0].(map[string]any)["value"])
	require.Equal(t, "%", changeRows[0].(map[string]any)["unit"])
	tooMany := newDocumentTableAnalysisTool(func() []types.AgentStep { return analysisSteps(analysisTableFixture) }, func() string { return "actual task" }, analysisPlannerReply(analysisReadyPlan(t, original, original, original, original, original)))
	failed := runDocumentAnalysis(t, tooMany, `{}`)
	require.False(t, failed.Success)
	require.Empty(t, failed.Data["executions"])
}
