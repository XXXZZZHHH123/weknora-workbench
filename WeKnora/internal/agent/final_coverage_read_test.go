package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func coverageReadFixture(t *testing.T) (*AgentEngine, *types.AgentState, *mockChat, context.Context) {
	t.Helper()
	e, state := standardBudgetFixture(t)
	e.config.WorkbenchDefault = true
	model := &mockChat{}
	e.chatModel = model
	document := &types.Knowledge{ID: "contract-document", KnowledgeBaseID: "contract-library", TenantID: 7, Title: "Source handbook", ParseStatus: types.ParseStatusCompleted}
	repo := &citationContractChunkRepository{rows: []*types.Chunk{{ID: "new-passage", KnowledgeID: document.ID, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7, ChunkType: types.ChunkTypeText, IsEnabled: true, Content: "Additional source passage explains the condition."}}}
	reader := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: document}, &citationContractChunkService{repo: repo}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7}})
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(reader)
	e.taskPlan.ToolNames = []string{agenttools.ToolReadDocument}
	state.RoundSteps = []types.AgentStep{{Iteration: 7, ToolCalls: []types.ToolCall{{ID: "prior-read", Name: agenttools.ToolReadDocument, Result: realCitationReadResult(t)}}}}
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "authorized-reader", Role: types.TenantRoleViewer})
	ctx = context.WithValue(ctx, standardAnswerFinalDeadlineKey{}, time.Now().Add(standardAnswerFinalBudget))
	return e, state, model, ctx
}

func coverageCalls(n int, args string) []types.LLMToolCall {
	var calls []types.LLMToolCall
	for i := 0; i < n; i++ {
		calls = append(calls, types.LLMToolCall{ID: fmt.Sprintf("recovery-%d", i), Type: "function", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: args}})
	}
	return calls
}

func coverageReadReply(calls []types.LLMToolCall) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{ToolCalls: calls, Done: true, FinishReason: "tool_calls"}}}
}

func coverageDecision() candidateDecision {
	return candidateDecision{Disposition: candidateNeedSource, ReasonCode: "discovery_coverage_recovery", Candidate: "The requested condition has not yet been covered."}
}

func TestFinalCoverageReadOneBatchBoundedAndUsesActualEvidence(t *testing.T) {
	e, state, model, ctx := coverageReadFixture(t)
	model.responses = []mockResponse{coverageReadReply(coverageCalls(4, `{"id":"contract-document","query":"condition"}`))}
	e.turnReview.Repairs = 1
	step, err := e.finalCoverageRead(ctx, "Explain the condition.", state, "session", "message", emptyMessages(), coverageDecision())
	require.NoError(t, err)
	require.NotNil(t, step)
	require.Len(t, step.ToolCalls, 4)
	for _, call := range step.ToolCalls {
		require.True(t, call.Result.Success, call.Result.Error)
		require.Contains(t, call.Result.Output, "Additional source passage")
	}
	require.Equal(t, 8, state.CurrentRound, "one existing iteration is consumed, never reset")
	require.Equal(t, 1, e.turnReview.Repairs)
	messages := e.appendToolResults(emptyMessages(), *step)
	require.NotEmpty(t, e.modelContext.CitableChunkHandle("new-passage"))
	require.NotEmpty(t, messages)
	_, err = e.finalCoverageRead(ctx, "Explain the condition.", state, "session", "message", messages, coverageDecision())
	require.NoError(t, err)
	require.Len(t, model.calls, 1, "even repeated review feedback cannot open another read batch")
	require.Len(t, model.opts[0].Tools, 1)
	require.Equal(t, agenttools.ToolReadDocument, model.opts[0].Tools[0].Function.Name)
}

func TestFinalCoverageReadRejectsWholeExpandedOrUnknownBatch(t *testing.T) {
	for _, args := range []string{`{"id":"unknown-document"}`, `{"id":"contract-document","source_scope":null}`, `{"id":"contract-document","SOURCE_SCOPE":{"sources":[]}}`, `{"id":"contract-document","images":["some-image"]}`, `{"id":"contract-document","image_mode":"geometry"}`, `{"id":"chunk-id"}`, `null`} {
		t.Run(args, func(t *testing.T) {
			require.False(t, validFinalCoverageCalls(coverageCalls(1, args), map[string]bool{"contract-document": true}))
		})
	}
	for _, scenario := range []string{"too_many", "other_tool", "unresolved_handle", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			e, state, model, ctx := coverageReadFixture(t)
			calls := coverageCalls(2, `{"id":"contract-document"}`)
			switch scenario {
			case "too_many":
				calls = coverageCalls(5, `{"id":"contract-document"}`)
			case "other_tool":
				calls[1].Function.Name = agenttools.ToolBindDocumentScope
			case "unresolved_handle":
				calls[1].Function.Arguments = `{"id":"doc://999"}`
			}
			reply := coverageReadReply(calls)
			if scenario == "truncated" {
				reply.chunks[0].FinishReason = "length"
			}
			model.responses = []mockResponse{reply}
			step, err := e.finalCoverageRead(ctx, "Explain.", state, "s", "m", emptyMessages(), coverageDecision())
			require.NoError(t, err)
			require.Nil(t, step)
			require.Len(t, state.RoundSteps, 1, "no partial batch executed")
		})
	}
}

func TestFinalCoverageReadRespectsDeadlineIterationsAndNativeDiscovery(t *testing.T) {
	for _, scenario := range []string{"little_time", "outer_deadline", "no_final_budget", "iteration_limit", "failed_discovery", "strict", "already_used", "non_discovery_failure"} {
		t.Run(scenario, func(t *testing.T) {
			e, state, model, ctx := coverageReadFixture(t)
			decision := coverageDecision()
			switch scenario {
			case "little_time":
				ctx = context.WithValue(ctx, standardAnswerFinalDeadlineKey{}, time.Now().Add(time.Second))
			case "outer_deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			case "no_final_budget":
				ctx = t.Context()
			case "iteration_limit":
				e.config.MaxIterations = state.CurrentRound + 1
			case "failed_discovery":
				state.RoundSteps[0].ToolCalls[0].Result.Success = false
			case "strict":
				e.config.AnswerReviewMode = "strict"
			case "already_used":
				e.turnReview.FinalCoverageReadAttempted = true
			case "non_discovery_failure":
				decision.ReasonCode = "citation_identity"
			}
			step, err := e.finalCoverageRead(ctx, "Explain.", state, "s", "m", emptyMessages(), decision)
			require.NoError(t, err)
			require.Nil(t, step)
			require.Empty(t, model.calls)
		})
	}
}

func TestFinalSynthesisCoverageRecoveryReadsThenAlwaysFinishesWithoutTools(t *testing.T) {
	e, state, model, ctx := coverageReadFixture(t)
	messages := e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	model.responses = []mockResponse{
		chatAnswer("The remaining condition has not been read."),
		discoveryReply("recover", "The requested condition remains unanswered; read the already discovered handbook passage."),
		coverageReadReply(coverageCalls(1, `{"id":"contract-document","query":"condition"}`)),
		chatAnswer("The additional source passage explains the condition."),
	}
	stage := &standardStageChat{mockChat: *model}
	e.chatModel = stage
	model = &stage.mockChat
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	err := e.streamFinalAnswerToEventBus(ctx, "Explain the condition.", state, "session", messages, finalAnswerOptions{preliminary: true, assistantMessageID: "message"})
	require.NoError(t, err)
	require.Len(t, model.calls, 4)
	require.Equal(t, stage.deadlines[0], stage.deadlines[3], "synthesis and post-read repair retain the original final deadline")
	require.LessOrEqual(t, time.Until(stage.deadlines[2]), finalCoverageReadBudget, "read planning and execution share at most ten seconds")
	require.Len(t, state.RoundSteps, 2)
	require.Equal(t, "none", model.opts[3].ToolChoice)
	require.Empty(t, model.opts[3].Tools)
	require.Contains(t, state.FinalAnswer, "additional source passage")
	found := false
	for _, message := range model.calls[3] {
		found = found || message.Role == "tool" && message.Name == agenttools.ToolReadDocument && strings.Contains(message.Content, "Additional source passage")
	}
	require.True(t, found, "final synthesis sees the actual recovery read as a tool result")
	require.Equal(t, 1, e.turnReview.Repairs, "coverage recovery spends the existing candidate repair")
	done, _ := recorder.snapshot()
	require.Equal(t, 1, done)
	data, _ := json.Marshal(state.RoundSteps[1].ToolCalls[0].Result.Data)
	require.Contains(t, string(data), "content_revision")
}
