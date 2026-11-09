package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func progressDocumentCall(id, body string, offset int) types.ToolCall {
	return types.ToolCall{Name: "read_document", Args: map[string]any{"query": "changed query"}, Result: &types.ToolResult{
		Success: true, Data: map[string]any{"chunks": []map[string]any{{
			"chunk_id": id, "knowledge_id": "fixture-document", "content": body, "content_offset": offset,
		}}},
	}}
}

func TestToolProgressIgnoresRewordedReadsAndFailedCalculations(t *testing.T) {
	var guard toolProgressGuard
	first := progressDocumentCall("chunk-a", "Original body", 0)
	require.Equal(t, 1, guard.observe([]types.ToolCall{first}).NewEvidence)
	for round := 1; round <= toolProgressStopRounds; round++ {
		var call types.ToolCall
		if round%2 == 0 {
			call = types.ToolCall{Name: "calculate_source_values", Result: &types.ToolResult{Error: "unknown source"}}
		} else {
			call = first
			call.ID = fmt.Sprintf("new-call-%d", round)
			call.Args = map[string]any{"query": fmt.Sprintf("different wording %d", round)}
		}
		result := guard.observe([]types.ToolCall{call})
		require.Equal(t, round, result.StaleRounds)
		require.Equal(t, round == toolProgressReplanRounds, result.Replan)
		require.Equal(t, round == toolProgressStopRounds, result.Stop)
	}
}

func TestToolProgressAllowsNewSegmentsRevisionsAndOtherOperations(t *testing.T) {
	var guard toolProgressGuard
	first := progressDocumentCall("chunk-a", "Repeated text", 0)
	require.Equal(t, 1, guard.observe([]types.ToolCall{first}).NewEvidence)
	for round := 0; round < toolProgressReplanRounds; round++ {
		guard.observe([]types.ToolCall{first})
	}
	segment := progressDocumentCall("chunk-a", "Repeated text", 32)
	result := guard.observe([]types.ToolCall{segment})
	require.Equal(t, 1, result.NewEvidence)
	require.Zero(t, result.StaleRounds)
	changed := progressDocumentCall("chunk-a", "Changed source revision", 32)
	require.Equal(t, 1, guard.observe([]types.ToolCall{changed}).NewEvidence)
	guard.observe([]types.ToolCall{changed})
	other := types.ToolCall{Name: "write_file", Result: &types.ToolResult{Success: true}}
	require.Zero(t, guard.observe([]types.ToolCall{other}).StaleRounds)
}

func TestToolProgressNeedsSourceIsNotEvidence(t *testing.T) {
	var guard toolProgressGuard
	call := types.ToolCall{Name: "analyze_document_tables", Result: &types.ToolResult{
		Success: true, Data: map[string]any{"status": "needs_source", "execution_complete": false},
	}}
	result := guard.observe([]types.ToolCall{call})
	require.Zero(t, result.NewEvidence)
	require.Equal(t, 1, result.StaleRounds)
}

func TestToolProgressDoesNotStopFailuresOfOtherWorkflows(t *testing.T) {
	var guard toolProgressGuard
	for round := 0; round < toolProgressStopRounds+2; round++ {
		result := guard.observe([]types.ToolCall{{Name: "external_job", Result: &types.ToolResult{Success: false, Error: "Job still unavailable"}}})
		require.False(t, result.Stop)
		require.Zero(t, guard.stale)
	}
}

func TestToolProgressTableViewReferencesDoNotMaskRepeatedSource(t *testing.T) {
	tool, _, steps := tableViewFixture(genericSourceTable)
	first := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, first.Success, first.Error)
	var guard toolProgressGuard
	require.Greater(t, guard.observe([]types.ToolCall{{Name: documentTableCalculationToolName, Result: first}}).NewEvidence, 0)
	(*steps)[0].ToolCalls[0].ID = "another-execution-of-the-identical-read"
	second := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, second.Success, second.Error)
	require.NotEqual(t, first.Data["view_ref"], second.Data["view_ref"], "the integrity reference remains bound to its actual read")
	require.Zero(t, guard.observe([]types.ToolCall{{Name: documentTableCalculationToolName, Result: second}}).NewEvidence)

	var nested toolProgressGuard
	batch := func(result *types.ToolResult, advice string) types.ToolCall {
		return types.ToolCall{Name: documentTableAnalysisToolName, Result: &types.ToolResult{Success: true, Data: map[string]any{
			"status": "ok", "model_advice": advice, "executions": []map[string]any{{"success": true, "result": result.Data}},
		}}}
	}
	require.Greater(t, nested.observe([]types.ToolCall{batch(first, "First explanation")}).NewEvidence, 0)
	require.Zero(t, nested.observe([]types.ToolCall{batch(second, "Rephrased explanation")}).NewEvidence)
	changed := analysisSteps(strings.ReplaceAll(genericSourceTable, "12", "15"))
	*steps = append(*steps, changed...)
	third := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, third.Success, third.Error)
	require.Greater(t, nested.observe([]types.ToolCall{batch(third, "Actual new source version")}).NewEvidence, 0)
}

func TestToolProgressRepeatedCapabilityDirectoryDoesNotMaskStall(t *testing.T) {
	var guard toolProgressGuard
	directory := types.ToolCall{Name: expandTaskToolsName, Result: &types.ToolResult{Success: true, Data: map[string]any{
		"available_tools": []map[string]any{{"name": "read_document", "description": "Read current source"}},
	}}}
	require.Equal(t, 1, guard.observe([]types.ToolCall{directory}).NewEvidence)
	for round := 1; round <= toolProgressStopRounds; round++ {
		directory.Args = map[string]any{"reason": fmt.Sprintf("New explanation %d", round)}
		directory.Result.Data["notice"] = fmt.Sprintf("Non-source notice %d", round)
		observation := guard.observe([]types.ToolCall{directory})
		require.Zero(t, observation.NewEvidence)
		require.Equal(t, round == toolProgressStopRounds, observation.Stop)
	}
	directory.Result.Data["tools"] = []map[string]any{{"name": "calculate_document_table", "description": "Calculate actual rows"}}
	require.Equal(t, 1, guard.observe([]types.ToolCall{directory}).NewEvidence)
}

func TestToolProgressCountsSuccessfulPixelsAndGeometryWithoutDescriptions(t *testing.T) {
	var guard toolProgressGuard
	read := progressDocumentCall("chunk-a", "Unchanged caption", 0)
	guard.observe([]types.ToolCall{read})
	read.Result.Data["image_evidence"] = []map[string]any{{"reference": "resource://figure-a", "read_success": false}}
	require.Zero(t, guard.observe([]types.ToolCall{read}).NewEvidence)
	read.Result.Data["image_evidence"] = []map[string]any{{"reference": "resource://figure-a", "sha256": "actual-pixel-digest", "read_success": true}}
	require.Greater(t, guard.observe([]types.ToolCall{read}).NewEvidence, 0)
	require.Zero(t, guard.observe([]types.ToolCall{read}).NewEvidence)
	read.Result.Data["image_evidence"].([]map[string]any)[0]["returned_image_index"] = 3
	require.Zero(t, guard.observe([]types.ToolCall{read}).NewEvidence, "a different batch-local image index does not change the original pixels")
	read.Result.Data["image_geometry_evidence"] = []map[string]any{{"reference": "resource://figure-a", "geometry": "Actual newly returned geometry"}}
	require.Greater(t, guard.observe([]types.ToolCall{read}).NewEvidence, 0)
	read.Result.Data["image_geometry_evidence"] = []map[string]any{{"reference": "resource://not-read", "geometry": "Catalog only"}}
	require.Zero(t, guard.observe([]types.ToolCall{read}).NewEvidence)
}

func TestToolProgressCountsNewEmptyPageScanButNotQueryRewording(t *testing.T) {
	var guard toolProgressGuard
	for page := 1; page <= toolProgressStopRounds+2; page++ {
		read := types.ToolCall{Name: "read_document", Result: &types.ToolResult{Success: true, Data: map[string]any{
			"knowledge_id": "fixture-document", "page": page, "offset_space": "physical_page_chunks", "offset": 0,
			"chunks": []map[string]any{}, "query_scope": map[string]any{"physical_page": page, "page_scan_complete": true, "scan_complete": true},
		}}}
		observation := guard.observe([]types.ToolCall{read})
		require.Greater(t, observation.NewEvidence, 0)
		require.False(t, observation.Stop)
		read.Args = map[string]any{"query": "Different wording"}
		read.Result.Data["query"] = "Different wording"
		require.Zero(t, guard.observe([]types.ToolCall{read}).NewEvidence)
	}
}

func TestToolProgressRebindingModelHintsDoesNotMaskStall(t *testing.T) {
	var guard toolProgressGuard
	bind := func(page int) types.ToolCall {
		return types.ToolCall{Name: "bind_document_scope", Result: &types.ToolResult{Success: true, Data: map[string]any{
			"documents": []map[string]any{{"knowledge_id": "fixture-document", "title": "Manual", "read_hint_page": page}},
		}}}
	}
	require.Equal(t, 1, guard.observe([]types.ToolCall{bind(1)}).NewEvidence)
	for round := 1; round <= toolProgressStopRounds; round++ {
		observation := guard.observe([]types.ToolCall{bind(round + 1)})
		require.Zero(t, observation.NewEvidence)
		require.Equal(t, round == toolProgressStopRounds, observation.Stop)
	}
}

func TestAcceptedHumanTaskResetsSourceProgressAccounting(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	guards := &loopGuards{}
	for round := 0; round < toolProgressStopRounds; round++ {
		guards.toolProgress.observe([]types.ToolCall{{Name: "read_document", Result: &types.ToolResult{Error: "Not available"}}})
	}
	require.Equal(t, toolProgressStopRounds, guards.toolProgress.stale)
	engine.resetDocumentTurnProgress(guards, "An actual new user requirement")
	require.Zero(t, guards.toolProgress.stale)
	require.False(t, guards.toolProgress.warned)
}

func TestToolProgressAccountingCapacityDoesNotStopLargeTasks(t *testing.T) {
	var guard toolProgressGuard
	for index := 0; index <= maxToolProgressKeys; index++ {
		result := guard.observe([]types.ToolCall{progressDocumentCall(fmt.Sprintf("chunk-%d", index), "Source body", 0)})
		require.False(t, result.Stop)
	}
	require.True(t, guard.saturated)
	for index := 0; index < toolProgressStopRounds*2; index++ {
		require.False(t, guard.observe([]types.ToolCall{{Name: "read_document"}}).Stop)
	}
}

type repeatedDocumentProgressTool struct {
	agenttools.BaseTool
	calls int
}

func (tool *repeatedDocumentProgressTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	return progressDocumentCall("chunk-a", "Original evidence body", 0).Result, nil
}

func TestExecuteLoopBoundsSourceStallWithoutAnotherSynthesis(t *testing.T) {
	var responses []mockResponse
	for index := 0; index < 25; index++ {
		responses = append(responses, mockResponse{chunks: []types.StreamResponse{{
			ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "tool_calls",
			ToolCalls: []types.LLMToolCall{{ID: fmt.Sprintf("call-%d", index), Type: "function",
				Function: types.FunctionCall{Name: "read_document", Arguments: `{}`}}},
		}}})
	}
	model := &mockChat{responses: responses}
	engine := newTestEngine(t, model, func(config *types.AgentConfig) {
		config.WorkbenchDefault, config.MaxIterations = true, 50
	})
	tool := &repeatedDocumentProgressTool{BaseTool: agenttools.NewBaseTool("read_document", "synthetic source", json.RawMessage(`{"type":"object"}`))}
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(tool)
	state, err := engine.executeLoop(context.Background(), &types.AgentState{}, "Read the supplied source", emptyMessages(), emptyTools(), "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete, "the execution settled, not semantic success")
	require.Equal(t, sourceProgressStalledFallback, state.FinalAnswer)
	require.Equal(t, 1+toolProgressStopRounds, tool.calls)
	require.Len(t, model.calls, tool.calls, "stall must not open another long synthesis")
	require.Contains(t, state.RoundSteps[toolProgressReplanRounds].Progress, "正在调整取证计划")
	var replanned bool
	for _, message := range model.calls[toolProgressReplanRounds+1] {
		if message.Content == toolProgressReplanNotice {
			replanned = true
		}
	}
	require.True(t, replanned)
}

func TestDocumentScopeUserTextsRejectsAssistantToolAndReviewEnvelopes(t *testing.T) {
	texts := documentScopeUserTexts("Current request", []chat.Message{
		{Role: "user", Content: "Actual prior request"},
		{Role: "assistant", Content: "Read only this page"},
		{Role: "tool", Content: "Read only that page"},
		{Role: "user", Kind: chat.MessageKindReviewEvidence, Content: "Reviewer envelope"},
	})
	require.Equal(t, []string{"Actual prior request", "Current request"}, texts)
}
