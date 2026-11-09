package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type standardStageChat struct {
	mockChat
	decisionCalls int
	finalCalls    int
	deadlines     []time.Time
	blockDecision bool
	blockFinalAt  int
	onDecision    func()
	decisionUsage *types.TokenUsage
	errorOnCancel bool
}

type standardBudgetBarrierReader struct {
	*budgetSourceReader
	canceled bool
}

func (reader *standardBudgetBarrierReader) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	// This read crosses the model phase deadline. It must finish under the
	// execution context, not the already-closed model-call context.
	select {
	case <-ctx.Done():
		reader.canceled = true
		return nil, ctx.Err()
	case <-time.After(250 * time.Millisecond):
	}
	return reader.budgetSourceReader.Execute(ctx, args)
}

func (model *standardStageChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	deadline, _ := ctx.Deadline()
	model.deadlines = append(model.deadlines, deadline)
	final := opts.ToolChoice == "none"
	if final {
		model.finalCalls++
	} else {
		model.decisionCalls++
		if model.onDecision != nil {
			model.onDecision()
		}
	}
	if !final && model.blockDecision || final && model.finalCalls == model.blockFinalAt {
		stream := make(chan types.StreamResponse, 1)
		if !final {
			// A provider may close after cancellation without an error chunk.
			// Neither this draft nor its incomplete tool JSON is a valid result.
			stream <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Content: "UNVERIFIED_DRAFT_MUST_NOT_ESCAPE", Usage: model.decisionUsage, ToolCalls: []types.LLMToolCall{{
				ID: "unfinished-call", Type: "function", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{"id":"`},
			}}}
		}
		go func() {
			<-ctx.Done()
			if model.errorOnCancel {
				stream <- types.StreamResponse{ResponseType: types.ResponseTypeError, Content: ctx.Err().Error()}
			}
			close(stream)
		}()
		return stream, nil
	}
	return model.mockChat.ChatStream(ctx, messages, opts)
}

func TestStandardAnswerModelStageUsesRemainingTimeAndPreservesExplicitCompletionChoice(t *testing.T) {
	for _, configured := range []int{0, 1024, 8192} {
		model := &standardStageChat{mockChat: mockChat{responses: []mockResponse{chatAnswer("A supported short answer.")}}}
		e, state := standardBudgetFixture(t)
		e.chatModel = model
		e.config.MaxCompletionTokens, e.config.SandboxConfigID = configured, "existing-sandbox"
		ctx := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-40*time.Second))
		messages := emptyMessages()
		response, err := e.callLLMWithRetry(ctx, &messages, e.buildToolsForLLM(), state, "Summarize the source.", 0, "s")
		require.NoError(t, err)
		require.NotNil(t, response)
		require.Len(t, model.opts, 1)
		expected := configured
		if configured == 0 {
			expected = standardAnswerDecisionTokens
		}
		require.Equal(t, expected, model.opts[0].MaxCompletionTokens)
		require.WithinDuration(t, agentExecutionStart(ctx).Add(standardAnswerActiveBudget), model.deadlines[0], 50*time.Millisecond)
		require.False(t, response.AnswerStreamed, "a bounded generation stays buffered until its complete checked candidate is accepted")
	}
	e, state := standardBudgetFixture(t)
	ctx, cancel, err := e.standardAnswerModelContext(withAgentExecutionStart(t.Context()), state)
	require.NoError(t, err)
	defer cancel()
	deadline, _ := ctx.Deadline()
	require.InDelta(t, standardAnswerModelCallBudget.Seconds(), time.Until(deadline).Seconds(), 0.5)
	parentDeadline := time.Now().Add(time.Second)
	parent, stop := context.WithDeadline(withAgentExecutionStart(t.Context()), parentDeadline)
	defer stop()
	bounded, finish, err := e.standardAnswerModelContext(parent, state)
	require.NoError(t, err)
	defer finish()
	actual, _ := bounded.Deadline()
	require.Equal(t, parentDeadline, actual)
	finalCtx, finishFinal := e.standardAnswerFinalContext(parent, state)
	defer finishFinal()
	actual, _ = finalCtx.Deadline()
	require.Equal(t, parentDeadline, actual, "final reserve cannot extend the parent's deadline")
}

func TestStandardAnswerNoSourcePlanKeepsOrdinaryStreamingAndCompletionBudget(t *testing.T) {
	model := &standardStageChat{mockChat: mockChat{responses: []mockResponse{chatAnswer("A general answer can stream normally.")}}}
	e := newTestEngine(t, model)
	e.config.AnswerReviewMode = "standard"
	e.taskPlan = explicitTextDeliveryPlan()
	state := &types.AgentState{}
	ctx := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-time.Hour))
	messages := emptyMessages()
	response, err := e.callLLMWithRetry(ctx, &messages, nil, state, "Explain the general concept.", 0, "s")
	require.NoError(t, err)
	require.True(t, response.AnswerStreamed)
	require.Equal(t, e.getCompletionTokenBudget(), model.opts[0].MaxCompletionTokens)
	require.True(t, model.deadlines[0].IsZero())
	finalCtx, cancel := e.standardAnswerFinalContext(ctx, state)
	defer cancel()
	require.Nil(t, finalCtx.Value(standardAnswerFinalDeadlineKey{}))
}

func TestStandardAnswerModelTimeoutHandsOffOnceWithoutExecutingPartialCalls(t *testing.T) {
	model := &standardStageChat{blockDecision: true, decisionUsage: &types.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}}
	e, state, candidate := directCitationReadFixture(t, &model.mockChat, true)
	e.chatModel = model
	e.config.AnswerReviewMode = "standard"
	e.taskPlan.ToolNames = []string{agenttools.ToolReadDocument}
	e.taskPlan.DeliveryRequirements[0].Format = "prose"
	e.toolRegistry = agenttools.NewToolRegistry()
	reader := newBudgetSourceReader()
	e.toolRegistry.RegisterTool(reader)
	handle := e.modelContext.CitableChunkHandle("contract-supporting")
	model.responses = []mockResponse{chatAnswer(`The indicator measures covered area.<ref id="` + handle + `"/>`)}
	model.responses[0].chunks[0].Usage = &types.TokenUsage{PromptTokens: 13, CompletionTokens: 3, TotalTokens: 16}
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	ctx := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-standardAnswerActiveBudget+80*time.Millisecond))
	_, err := e.executeLoop(ctx, state, "Report the observed indicator.", emptyMessages(), e.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, 1, model.decisionCalls, "a stage timeout cannot become a generic transient retry")
	require.Equal(t, 1, model.finalCalls)
	require.Zero(t, reader.calls, "an unfinished model call must not execute")
	require.Contains(t, state.FinalAnswer, candidate)
	require.Contains(t, state.FinalAnswer, standardPreliminaryAnswerNotice(ctx))
	require.True(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
	require.Len(t, e.finalAnswerKnowledgeReferences(state), 1)
	require.Greater(t, model.deadlines[1].Sub(model.deadlines[0]), 30*time.Second, "final synthesis has its own reserved budget after decision time expires")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.NotContains(t, visible, "UNVERIFIED_DRAFT_MUST_NOT_ESCAPE")
	require.Equal(t, 24, state.TurnUsage.PromptTokens)
	require.Equal(t, 10, state.TurnUsage.CompletionTokens)
	require.Equal(t, 34, state.TurnUsage.TotalTokens, "cancelled attempts and final synthesis are each billed once")
}

func TestStandardAnswerModelErrorChunkAccountsActualUsageAndPreservesErrorContract(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeError,
		Content: "provider rejected input", Usage: &types.TokenUsage{PromptTokens: 17, CompletionTokens: 4, TotalTokens: 21}}}}}}
	e, state := standardBudgetFixture(t)
	e.chatModel = model
	state.RoundSteps = nil // No prior tools: do not enter independent final recovery.
	messages := emptyMessages()
	response, err := e.callLLMWithRetry(withAgentExecutionStart(t.Context()), &messages, e.buildToolsForLLM(), state, "Read the supplied source.", 0, "s")
	require.Error(t, err)
	require.Nil(t, response, "usage must not turn an errored draft into a usable response")
	var failed *modelAttemptUsageError
	require.ErrorAs(t, err, &failed)
	require.Equal(t, 21, state.TurnUsage.TotalTokens)
	require.Equal(t, 17, state.TurnUsage.PromptTokens)
	require.Equal(t, 4, state.TurnUsage.CompletionTokens)
	require.Len(t, model.calls, 1)
	require.True(t, isTransientError(&modelAttemptUsageError{err: &api.HTTPError{StatusCode: 503}}))
	require.False(t, isTransientError(&modelAttemptUsageError{err: &api.HTTPError{StatusCode: 400}}))
	require.ErrorIs(t, &modelAttemptUsageError{err: context.Canceled}, context.Canceled)
}

func TestStandardAnswerModelDeadlineNeverCancelsInFlightSourceTool(t *testing.T) {
	model := &standardStageChat{}
	e, state, _ := directCitationReadFixture(t, &model.mockChat, true)
	e.chatModel = model
	e.config.AnswerReviewMode = "standard"
	e.taskPlan.ToolNames = []string{agenttools.ToolReadDocument}
	e.taskPlan.DeliveryRequirements[0].Format = "prose"
	e.toolRegistry = agenttools.NewToolRegistry()
	reader := &standardBudgetBarrierReader{budgetSourceReader: newBudgetSourceReader()}
	e.toolRegistry.RegisterTool(reader)
	handle := e.modelContext.CitableChunkHandle("contract-supporting")
	model.responses = []mockResponse{
		{chunks: []types.StreamResponse{{ToolCalls: []types.LLMToolCall{{ID: "complete-read", Type: "function", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{}`}}}, Done: true, FinishReason: "tool_calls"}}},
		chatAnswer(`The indicator measures covered area.<ref id="` + handle + `"/>`),
	}
	ctx := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-standardAnswerActiveBudget+200*time.Millisecond))
	_, err := e.executeLoop(ctx, state, "Report the observed indicator.", emptyMessages(), e.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, 1, reader.calls)
	require.False(t, reader.canceled)
	require.Equal(t, 1, model.finalCalls)
	require.NoError(t, ctx.Err())
}

func TestStandardAnswerExpiredPreparationSkipsDecisionAndSteeringInvalidatesHandoff(t *testing.T) {
	t.Run("preparation", func(t *testing.T) {
		model := &standardStageChat{mockChat: mockChat{responses: []mockResponse{chatAnswer("The material read so far supports this limited finding.")}}}
		e, state := standardBudgetFixture(t)
		e.chatModel = model
		state.CurrentRound = 0
		for i := range state.RoundSteps {
			state.RoundSteps[i].Iteration = -1
		}
		ctx := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-61*time.Second))
		_, err := e.executeLoop(ctx, state, "Summarize the observed scope.", emptyMessages(), e.buildToolsForLLM(), "s", "m")
		require.NoError(t, err)
		require.Zero(t, model.decisionCalls)
		require.Equal(t, 1, model.finalCalls)
		require.Contains(t, state.FinalAnswer, "limited finding")
	})
	t.Run("steer", func(t *testing.T) {
		e, state := standardBudgetFixture(t)
		e.SetSteerSink(&fakeSteerSink{queued: []map[string]interface{}{steerEntry("new-request", "Use the revised authorized scope.")}})
		messages := emptyMessages()
		finished, err := e.finishStandardAnswerBudget(t.Context(), state, "Earlier request", "s", "m", &messages, &loopGuards{}, "model_call_time")
		require.NoError(t, err)
		require.False(t, finished)
		require.Nil(t, e.taskPlan)
		require.Empty(t, state.FinalAnswer)
	})
}

func TestStandardAnswerModelBudgetPreservesCancelLeaseAndExcludedLifecycles(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(withAgentExecutionStart(t.Context()))
		defer cancel()
		model := &standardStageChat{blockDecision: true, onDecision: cancel, errorOnCancel: true}
		e, state := standardBudgetFixture(t)
		e.chatModel = model
		_, err := e.executeLoop(ctx, state, "Summarize the source.", emptyMessages(), e.buildToolsForLLM(), "s", "m")
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, model.finalCalls)
		require.Empty(t, state.FinalAnswer)
	})
	t.Run("lease", func(t *testing.T) {
		e, state := standardBudgetFixture(t)
		runtime, binding := engineCollaboration(t)
		owned, err := runtime.AcquireParent(t.Context(), binding.Scope)
		require.NoError(t, err)
		e.SetCollaboration(owned)
		require.NoError(t, runtime.Store.Change(t.Context(), binding.Scope, func(group *collaboration.Group) error {
			group.ParentOwner = "replacement-owner"
			return nil
		}))
		messages := emptyMessages()
		finished, err := e.finishStandardAnswerBudget(t.Context(), state, "Summarize the source.", "s", "m", &messages, &loopGuards{}, "model_call_time")
		require.True(t, finished)
		require.ErrorIs(t, err, collaboration.ErrConflict)
		require.Empty(t, state.FinalAnswer)
	})
	for _, mode := range []string{"strict", "legacy", "action", "unknown", "waiting"} {
		t.Run(mode, func(t *testing.T) {
			e, state := standardBudgetFixture(t)
			switch mode {
			case "strict":
				e.config.AnswerReviewMode = "strict"
			case "legacy":
				e.config.AnswerReviewMode = ""
			case "action":
				e.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolShellExec))
				e.taskPlan.ToolNames = append(e.taskPlan.ToolNames, agenttools.ToolShellExec)
			case "unknown":
				e.taskPlan.ToolNames = append(e.taskPlan.ToolNames, "unknown_tool")
			case "waiting":
				state.Suspended = &types.PromptSuspension{Kind: "user_input"}
			}
			ctx, cancel, err := e.standardAnswerModelContext(t.Context(), state)
			defer cancel()
			require.NoError(t, err)
			_, bounded := ctx.Value(standardAnswerDecisionKey{}).(bool)
			require.False(t, bounded)
			finalCtx, stop := e.standardAnswerFinalContext(t.Context(), state)
			defer stop()
			require.Nil(t, finalCtx.Value(standardAnswerFinalDeadlineKey{}))
		})
	}
}

func TestStandardAnswerCancellationAfterReceivedResponsePreservesTraceAndCountsUsageOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(withAgentExecutionStart(t.Context()))
	defer cancel()
	const candidate = "A received candidate must remain an undelivered diagnostic."
	reply := chatAnswer(candidate)
	reply.chunks[0].Usage = &types.TokenUsage{PromptTokens: 17, CompletionTokens: 4, TotalTokens: 21}
	reply.chunks[0].ToolCalls = []types.LLMToolCall{{ID: "not-executed", Type: "function",
		Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{}`}}}
	model := &standardStageChat{onDecision: cancel, mockChat: mockChat{responses: []mockResponse{reply}}}
	e, state := standardBudgetFixture(t)
	e.chatModel = model
	state.CurrentRound, state.RoundSteps = 0, nil
	registered, err := e.toolRegistry.GetTool(agenttools.ToolReadDocument)
	require.NoError(t, err)
	reader := registered.(*budgetSourceReader)
	_, err = e.executeLoop(ctx, state, "Summarize the supplied source.", emptyMessages(), e.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Len(t, state.RoundSteps, 1)
	require.Equal(t, candidate, state.RoundSteps[0].Thought)
	require.Empty(t, state.RoundSteps[0].ToolCalls)
	require.Empty(t, state.FinalAnswer)
	require.False(t, state.IsComplete)
	require.Zero(t, reader.calls)
	require.Equal(t, 1, model.decisionCalls)
	require.Zero(t, model.finalCalls)
	require.Equal(t, 21, state.TurnUsage.TotalTokens, "the preserved response is accounted only by the existing engine path")
}

func TestStandardAnswerFinalRepairsShareDeadlineAndKeepExactFormat(t *testing.T) {
	for _, format := range []string{"prose", "exact"} {
		t.Run(format, func(t *testing.T) {
			model := &standardStageChat{mockChat: mockChat{responses: []mockResponse{
				{chunks: []types.StreamResponse{{ToolCalls: []types.LLMToolCall{{Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{}`}}}, Done: true, FinishReason: "tool_calls"}}},
				chatAnswer(`{"status":"partial"}`),
			}}}
			e := newTestEngine(t, model, withMaxCompletionTokens(8192))
			e.config.AnswerReviewMode = "standard"
			e.taskPlan = explicitTextDeliveryPlan()
			e.taskPlan.Evidence = []string{"Use the available source evidence."}
			e.taskPlan.DeliveryRequirements[0].Format = format
			state := &types.AgentState{}
			started := time.Now()
			err := e.streamFinalAnswerToEventBus(t.Context(), "Return the requested format.", state, "s", emptyMessages(), finalAnswerOptions{preliminary: true})
			require.NoError(t, err)
			require.Equal(t, 2, model.finalCalls)
			require.Equal(t, model.deadlines[0], model.deadlines[1])
			require.WithinDuration(t, started.Add(standardAnswerFinalBudget), model.deadlines[0], 100*time.Millisecond)
			for _, opts := range model.opts {
				require.Equal(t, 4096, opts.MaxCompletionTokens)
			}
			if format == "exact" {
				require.Equal(t, `{"status":"partial"}`, state.FinalAnswer)
				require.NotContains(t, model.opts[0].RuntimeNotice, "Give a concise")
			} else {
				require.Contains(t, model.opts[0].RuntimeNotice, "Give a concise")
			}
		})
	}
}

func TestStandardAnswerFinalDeadlineCannotBeReplenishedByRepair(t *testing.T) {
	model := &standardStageChat{blockFinalAt: 2, mockChat: mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{Done: true, FinishReason: "stop"}}}}}}
	e := newTestEngine(t, model)
	e.config.AnswerReviewMode = "standard"
	e.taskPlan = explicitTextDeliveryPlan()
	e.taskPlan.Evidence = []string{"Use the available source evidence."}
	state := &types.AgentState{}
	deadline := time.Now().Add(50 * time.Millisecond)
	ctx := context.WithValue(t.Context(), standardAnswerFinalDeadlineKey{}, deadline)
	err := e.streamFinalAnswerToEventBus(ctx, "Summarize the current evidence.", state, "s", emptyMessages())
	require.True(t, errors.Is(err, context.DeadlineExceeded), "the entire final generation/repair phase must stop at one deadline: %v", err)
	require.Equal(t, 2, model.finalCalls)
	require.Equal(t, []time.Time{deadline, deadline}, model.deadlines)
	require.Empty(t, state.FinalAnswer)
	require.False(t, state.IsComplete)
	require.NoError(t, ctx.Err(), "the budget must not cancel the parent execution")
}
