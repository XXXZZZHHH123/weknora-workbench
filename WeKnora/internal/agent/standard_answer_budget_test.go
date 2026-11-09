package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type budgetSourceReader struct {
	*agenttools.BaseTool
	calls int
}

func newBudgetSourceReader() *budgetSourceReader {
	base := agenttools.NewBaseTool(agenttools.ToolReadDocument, "Read a synthetic current source", json.RawMessage(`{"type":"object"}`))
	return &budgetSourceReader{BaseTool: &base}
}

func (tool *budgetSourceReader) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	data := map[string]any{"document": map[string]any{"knowledge_id": "observed-document", "title": "Observed source"},
		"chunks": []any{map[string]any{"chunk_id": fmt.Sprintf("observed-chunk-%d", tool.calls), "knowledge_id": "observed-document", "content": "The source describes the observed scope."}}}
	raw, _ := json.Marshal(data)
	return &types.ToolResult{Success: true, Data: data, Output: string(raw)}, nil
}

func standardBudgetFixture(t *testing.T) (*AgentEngine, *types.AgentState) {
	t.Helper()
	e := newTestEngine(t, &mockChat{})
	e.config.AnswerReviewMode, e.config.MaxIterations = "standard", 20
	e.taskPlan = explicitTextDeliveryPlan()
	e.taskPlan.ToolNames = []string{agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(newBudgetSourceReader())
	e.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolBindDocumentScope))
	state := &types.AgentState{CurrentRound: 7}
	for i := 0; i < standardAnswerToolRounds; i++ {
		state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: i, ToolCalls: []types.ToolCall{{Name: agenttools.ToolReadDocument, Result: &types.ToolResult{Success: true, OutputFiles: []string{}}}}})
	}
	return e, state
}

func TestStandardAnswerBudgetEightSourceRoundsUseOneCheckedFinalSynthesis(t *testing.T) {
	model := &mockChat{}
	e, state, candidate := directCitationReadFixture(t, model, true)
	e.config.AnswerReviewMode, e.config.MaxIterations = "standard", 20
	state.RoundSteps[0].Iteration = -1 // Actual preparation evidence does not consume the eight rounds.
	e.taskPlan.ToolNames = []string{agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument}
	e.toolRegistry = agenttools.NewToolRegistry()
	reader := newBudgetSourceReader()
	e.toolRegistry.RegisterTool(reader)
	e.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolBindDocumentScope))
	for i := 0; i < standardAnswerToolRounds; i++ {
		model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{ToolCalls: []types.LLMToolCall{{
			ID: fmt.Sprintf("read-%d", i), Type: "function", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{}`},
		}}, Done: true, FinishReason: "tool_calls"}}})
	}
	handle := e.modelContext.CitableChunkHandle("contract-supporting")
	model.responses = append(model.responses, chatAnswer(`The indicator measures covered area.<ref id="`+handle+`"/> The remaining scope has not been covered by the material read in this run.`))
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	_, err := e.executeLoop(withAgentExecutionStart(t.Context()), state, "Report the observed indicator.", emptyMessages(), e.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, standardAnswerToolRounds, reader.calls)
	require.Len(t, model.calls, standardAnswerToolRounds+1, "only one synthesis follows the eight real tool barriers")
	require.Contains(t, state.FinalAnswer, candidate)
	require.Contains(t, model.opts[len(model.opts)-1].RuntimeNotice, standardAnswerBudgetNotice)
	require.Equal(t, "none", model.opts[len(model.opts)-1].ToolChoice)
	require.True(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
	require.Len(t, e.finalAnswerKnowledgeReferences(state), 1)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
}

func TestStandardAnswerBudgetExcludesNonSourceActionsWaitingAndOtherDeliveries(t *testing.T) {
	for _, mutation := range []string{"strict", "legacy", "file", "unknown planned", "MCP planned", "action planned", "unknown executed", "action executed", "proxy target", "pending result", "approval", "files", "child", "held answer", "smaller max"} {
		t.Run(mutation, func(t *testing.T) {
			e, state := standardBudgetFixture(t)
			call := &state.RoundSteps[0].ToolCalls[0]
			switch mutation {
			case "strict":
				e.config.AnswerReviewMode = "strict"
			case "legacy":
				e.config.AnswerReviewMode = ""
			case "file":
				e.taskPlan.DeliveryRequirements = append(e.taskPlan.DeliveryRequirements, taskDeliveryRequirement{Kind: "file"})
			case "unknown planned", "MCP planned", "action planned":
				name := map[string]string{"unknown planned": "unknown_reader", "MCP planned": agenttools.ToolCallMCPTool, "action planned": agenttools.ToolShellExec}[mutation]
				e.toolRegistry.RegisterTool(newCountingTool(name))
				e.taskPlan.ToolNames = append(e.taskPlan.ToolNames, name)
			case "unknown executed", "action executed":
				call.Name = map[string]string{"unknown executed": "unknown_reader", "action executed": agenttools.ToolWriteSandboxFile}[mutation]
				e.toolRegistry.RegisterTool(newCountingTool(call.Name))
			case "proxy target":
				call.Target = &types.ToolCallTarget{ServiceName: "external-action"}
			case "pending result":
				call.Result = nil
			case "approval":
				call.Result.Suspend = &types.PromptSuspension{Kind: "tool_approval"}
			case "files":
				call.Result.OutputFiles = []string{"resource://generated"}
			case "child":
				e.collaboration = &collaboration.Binding{Child: &collaboration.Work{}}
			case "held answer":
				e.holdCollaborationAnswer = true
			case "smaller max":
				e.config.MaxIterations = 4
			}
			require.Empty(t, e.standardAnswerBudgetReason(t.Context(), state, time.Now().Add(-time.Hour)))
		})
	}
	t.Run("active delegated child", func(t *testing.T) {
		e, state := standardBudgetFixture(t)
		runtime, binding := engineCollaboration(t)
		_, err := runtime.Spawn(t.Context(), binding.Scope, "spawn", "read_child", "", "{}", "Read the assigned source.")
		require.NoError(t, err)
		e.SetCollaboration(binding)
		require.Empty(t, e.standardAnswerBudgetReason(t.Context(), state, time.Now().Add(-time.Hour)))
	})
}

func TestStandardAnswerBudgetUsesActiveIntervalAndActualLazyProtocol(t *testing.T) {
	e, state := standardBudgetFixture(t)
	state.RoundSteps, state.CurrentRound = state.RoundSteps[:1], 0
	require.Empty(t, e.standardAnswerBudgetReason(t.Context(), state, time.Now()))
	require.Equal(t, "active_time", e.standardAnswerBudgetReason(t.Context(), state, time.Now().Add(-61*time.Second)))
	old := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-time.Hour))
	resumed := withAgentExecutionStart(old)
	require.Empty(t, e.standardAnswerBudgetReason(resumed, state, agentExecutionStart(resumed)), "a fresh resume interval excludes time awaiting human input")
	for i := range state.RoundSteps {
		state.RoundSteps[i].Iteration = -1
	}
	require.Empty(t, e.standardAnswerBudgetReason(t.Context(), state, time.Now().Add(-time.Hour)), "preparation alone is never a tool barrier")
	registry := nativeStructuredRegistry(t, func(http.ResponseWriter, *http.Request) { t.Error("eligibility must not negotiate or read data") })
	require.True(t, registry.PrepareStructuredProtocol())
	e.toolRegistry = registry
	e.taskPlan.ToolNames = []string{agenttools.ToolStructuredCapabilities}
	state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: agenttools.ToolStructuredCapabilities,
		Target: &types.ToolCallTarget{ServiceName: "structured_data", ToolName: "describe_tools"}, Result: &types.ToolResult{Success: true, OutputFiles: []string{}}}}}}
	require.Equal(t, "active_time", e.standardAnswerBudgetReason(t.Context(), state, time.Now().Add(-61*time.Second)), "the real lazy bootstrap is a recognized read-only protocol")
}

func TestStandardAnswerBudgetFinalSynthesisStillRejectsUnsupportedNumbersAndReferences(t *testing.T) {
	t.Run("numbers", func(t *testing.T) {
		bad := "The measured value is 138.67 and an unsupported total is 999."
		model := &mockChat{responses: []mockResponse{chatAnswer(bad), chatAnswer(bad), chatAnswer(bad)}}
		e := numericRecoveryTestEngine(t, model)
		e.config.AnswerReviewMode = "standard"
		e.taskPlan = explicitTextDeliveryPlan()
		state, messages, query := numericRecoveryFixture()
		require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), query, state, "s", messages, finalAnswerOptions{preliminary: true}))
		require.NotContains(t, state.FinalAnswer, "999")
		require.Equal(t, finalNumericFailureFallback, state.FinalAnswer)
	})
	t.Run("references", func(t *testing.T) {
		bad := `A source claim.<kb chunk_id="invented-source" />`
		model := &mockChat{responses: []mockResponse{chatAnswer(bad), chatAnswer(bad), chatAnswer(bad)}}
		e, state, _ := directCitationReadFixture(t, model, true)
		e.config.AnswerReviewMode = "standard"
		require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), "Report the indicator.", state, "s", emptyMessages(), finalAnswerOptions{preliminary: true}))
		require.NotContains(t, state.FinalAnswer, "invented-source")
		require.Empty(t, e.finalAnswerKnowledgeReferences(state))
	})
}
