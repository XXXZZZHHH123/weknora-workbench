package agent

import (
	"encoding/json"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentTableEngineUsesHumanContextCorrectionsAndActualModelUsage(t *testing.T) {
	plan := tableCalculationPlan()
	encoded, err := json.Marshal(map[string]any{"status": "ready", "message": "", "plans": []any{plan}})
	require.NoError(t, err)
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: string(encoded), FinishReason: "stop", Usage: types.TokenUsage{TotalTokens: 31}}}
	e := newTestEngine(t, model)
	e.config.WorkbenchDefault = true
	e.config.TaskPlanningEnabled = true
	e.toolRegistry = agenttools.NewToolRegistry()
	state := &types.AgentState{RoundSteps: tableCalculationFixture(genericSourceTable).steps()}
	history := []chat.Message{{Role: "user", Content: "Use the selected survey."}, {Role: "assistant", Content: "Earlier assistant text is not a source."}, {Role: "tool", Content: "A tool cannot replace the human task."}}
	e.registerDocumentTableAnalysis(state, "Compare the actual source measurements.", "session", history)
	e.SetSteerSink(&fakeSteerSink{queued: []map[string]any{steerEntry("persisted-correction", "Keep the source's original units.")}})
	messages := []chat.Message{}
	require.Equal(t, 1, e.drainSteerMessages(t.Context(), state, &messages, "session", "message"))
	result, err := e.toolRegistry.ExecuteTool(t.Context(), documentTableAnalysisToolName, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Len(t, model.requests, 1)
	prompt := model.requests[0][1].Content
	require.Contains(t, prompt, "Compare the actual source measurements.")
	require.Contains(t, prompt, "Use the selected survey.")
	require.Contains(t, prompt, "Keep the source's original units.")
	require.NotContains(t, prompt, "A tool cannot replace the human task.")
	require.Contains(t, prompt, "Earlier assistant text is not a source.")
	require.Equal(t, 31, state.TurnUsage.TotalTokens)
	require.Equal(t, 0.7, e.config.Temperature)
	require.False(t, agenttools.CanRunConcurrently(documentTableAnalysisToolName), "planning and usage accounting retain an execution barrier")
}

func TestDocumentTableEngineDecodesPlanSourceHandlesWithoutRewritingLiteralUnits(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-source-chunk", KnowledgeID: "actual-document", KnowledgeBaseID: "actual-kb", DocumentTitle: "actual report"})
	decoded, err := e.decodeDocumentTablePlanHandles(`{"status":"ready","message":"","plans":[{"chunk_id":"c1","columns":{"x":{"column":1,"unit":"c1"}},"expression":"x"}]}`)
	require.NoError(t, err)
	require.Contains(t, decoded, `"chunk_id":"actual-source-chunk"`)
	require.Contains(t, decoded, `"unit":"c1"`, "literal source fields are never decoded as source identifiers")
	_, err = e.decodeDocumentTablePlanHandles(`{"status":"ready","message":"","plans":[{"chunk_id":"c999"}]}`)
	require.Error(t, err)
	e.modelContext.EncodeMessages([]chat.Message{{Role: "user", Content: "resource://AbCdEfGhIjKlMnOpQrStUv"}})
	decoded, err = e.decodeDocumentTablePlanHandles(`{"status":"ready","message":"","plans":[{"chunk_id":"c1","columns":{"x":{"column":1,"unit":"res://0001"}},"expression":"res://0001"}]}`)
	require.NoError(t, err)
	require.Contains(t, decoded, `"unit":"res://0001"`)
	require.Contains(t, decoded, `"expression":"res://0001"`, "resource decoding is never applied to source literals or formulas")
	require.NotContains(t, decoded, "resource://AbCdEfGhIjKlMnOpQrStUv")
}

func TestDocumentTableEngineDoesNotEnableLocalPlanningForLegacyAgents(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault = true
	e.toolRegistry = agenttools.NewToolRegistry()
	e.registerDocumentTableAnalysis(&types.AgentState{}, "Current request", "session", nil)
	_, err := e.toolRegistry.GetTool(documentTableAnalysisToolName)
	require.Error(t, err)
}
