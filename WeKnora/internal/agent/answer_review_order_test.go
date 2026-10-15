package agent

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceReviewGetsNumericFeedbackBeforeCompletenessChecksFinalCandidate(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{Content: "该范围总量是999，单位依原表。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{Content: "实际结果是30，单位依原表。", Done: true, FinishReason: "stop"}}},
		completenessVerdict(true),
	}}
	engine := newTestEngine(t, model)
	engine.config.SandboxConfigID = "administrator-selected-runtime"
	raw := `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Output: raw}}}}}}
	_, err := engine.executeLoop(context.Background(), state, "请说明实际查询结果", []chat.Message{{Role: "system", Content: "Use authorized results"}, {Role: "tool", Name: "structured_analyze_table", Content: raw}}, emptyTools(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, "实际结果是30，单位依原表。", state.FinalAnswer)
	require.Len(t, model.calls, 3, "review the source draft, then check completeness of the delivered candidate only")
	require.Contains(t, model.calls[1][1].Content, `"runtime_unmatched_numbers":["999"]`)
	require.Contains(t, model.calls[1][1].Content, `"unverified_draft"`)
	require.Contains(t, model.calls[2][1].Content, `"candidate_answer":"实际结果是30，单位依原表。"`)
}

func TestFetchedDocumentAnswerGetsIndependentSourceReviewWithoutTableEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{Content: "草稿中的解释尚待核对。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{Content: "原报告记载30。<kb chunk_id=\"source-chunk\"/>", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	step := documentNumbersStep("read_document", true, "chunks", []map[string]any{{"chunk_id": "source-chunk", "knowledge_id": "authorized-doc", "content": "原报告记载30。"}})
	state := &types.AgentState{RoundSteps: []types.AgentStep{step}}
	messages := []chat.Message{{Role: "system", Content: "Use authorized sources"}, {Role: "tool", Name: "read_document", Content: `{"chunks":[{"chunk_id":"source-chunk","knowledge_id":"authorized-doc","content":"原报告记载30。"}]}`}}
	require.False(t, hasStructuredEvidence(state.RoundSteps))
	_, err := engine.executeLoop(context.Background(), state, "说明这份报告的内容", messages, emptyTools(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, "原报告记载30。", state.FinalAnswer)
	require.Len(t, model.calls, 2)
	require.Contains(t, model.calls[1][1].Content, "source-chunk")
	require.Contains(t, model.calls[1][1].Content, "unverified_draft")
}

func TestDirectoryGroupCountsTriggerReviewButNotErrorOrArbitraryMetadata(t *testing.T) {
	require.True(t, structuredResultHasEvidence(`{"series":[],"catalog_summary":{"kind":"catalog_group_counts","group_count":2}}`))
	require.False(t, structuredResultHasEvidence(`{"series":[],"catalog_summary":{"group_count":2}}`))
	require.False(t, structuredResultHasEvidence(`{"status":"error","catalog_summary":{"kind":"catalog_group_counts","group_count":2}}`))
	require.True(t, structuredResultHasEvidence(`{"kind":"source_calculation","status":"ok","calculations":[{"value":"1.25"}]}`))
	require.False(t, structuredResultHasEvidence(`{"kind":"source_calculation","status":"error"}`))
}
