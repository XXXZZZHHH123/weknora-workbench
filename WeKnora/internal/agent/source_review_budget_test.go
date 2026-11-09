package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type reviewBudgetChat struct {
	mockChat
	deadlines []time.Time
	purposes  []string
}

func (model *reviewBudgetChat) ChatStream(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	deadline, _ := ctx.Deadline()
	model.deadlines = append(model.deadlines, deadline)
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	model.purposes = append(model.purposes, purpose)
	return model.mockChat.ChatStream(ctx, messages, options)
}

func TestSourceReviewProtocolRetriesKeepOneDeadlineCompletionBudgetAndPurpose(t *testing.T) {
	invalid := reviewRetryUsage(sourceConsistencyVerdict(`{"complete":true,"feedback":"protocol conflict"}`), 11, 8000)
	valid := reviewRetryUsage(sourceAcceptedVerdict(), 13, 200)
	model := &reviewBudgetChat{mockChat: mockChat{responses: []mockResponse{invalid, valid}}}
	engine := sourceConsistencyEngine(t, &model.mockChat)
	engine.chatModel = model
	engine.config.SourceReviewReasoningEffort = "auto"
	engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "synthesis"}
	state := sourceConsistencyState()
	started := time.Now()
	repair, err := engine.answerNeedsRepair(t.Context(), "State the observed scope and original unit.", "The source reports the observed scope and unit.", "", state)
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 2)
	require.Equal(t, model.deadlines[0], model.deadlines[1], "a malformed verdict cannot replenish the review deadline")
	require.LessOrEqual(t, model.deadlines[0].Sub(started), sourceReviewTotalTimeout+250*time.Millisecond)
	require.Equal(t, []string{"source_review", "source_review"}, model.purposes)
	require.Equal(t, sourceReviewTotalCompletionTokens-8000, model.opts[1].MaxCompletionTokens)
	require.Equal(t, 6144+sourceReviewReasoningTokenReserve, model.opts[0].MaxCompletionTokens)
	for _, options := range model.opts {
		require.Equal(t, sourceCriticReasoning, options.ReasoningEffort, "the resource cap does not change configured reasoning strength")
		require.JSONEq(t, string(sourceConsistencySchema), string(options.Format))
	}
	require.Equal(t, model.calls[0][1], model.calls[1][1], "the same complete task, candidate and immutable evidence are rechecked")
	require.Equal(t, 8200, state.TurnUsage.CompletionTokens, "billing includes invalid verdicts")
	require.Equal(t, 24, state.TurnUsage.PromptTokens)
}

func TestSourceReviewReaderAndProtocolRetriesSpendTheSameCompletionAllowance(t *testing.T) {
	state := sourceConsistencyState()
	body := strings.Repeat("source-evidence-", 2200) + "TAIL"
	chunks := state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)
	chunks[0]["content"] = body
	engine := sourceConsistencyEngine(t, &mockChat{})
	seed := newReviewEvidenceReader(engine.sourceConsistencyMessages(state, nil))
	payloadID := reviewEvidencePayloadID(seed)
	require.NotEmpty(t, payloadID)
	read := reviewRetryUsage(reviewEvidenceToolResponse(reviewEvidenceToolCall(t, "budget-read", payloadID, "/data/chunks/0/content", len([]rune(body))-4, 4)), 1, 1000)
	invalid := reviewRetryUsage(sourceConsistencyVerdict(`{"complete":true,"feedback":"protocol conflict"}`), 2, 1000)
	rejection := reviewRetryUsage(sourceConsistencyVerdict(`{"complete":false,"feedback":"The material conclusion contradicts the observed source."}`), 3, 100)
	model := &reviewBudgetChat{mockChat: mockChat{responses: []mockResponse{read, invalid, rejection}}}
	engine.chatModel = model
	budget := &reviewGenerationBudget{purpose: "source_review", completionLimit: 2300}
	ctx := context.WithValue(t.Context(), reviewGenerationBudgetKey{}, budget)
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	repair, err := engine.evaluateAnswerCompleteness(ctx, "Summarize the actual source.", "The candidate claims a different conclusion.", state)
	require.NoError(t, err)
	require.True(t, repair, "a valid source rejection still uses the existing semantic repair path")
	require.Len(t, model.calls, 3)
	for index, expected := range []int{2300, 1300, 300} {
		require.Equal(t, expected, model.opts[index].MaxCompletionTokens)
		require.Equal(t, model.deadlines[0], model.deadlines[index])
	}
	_, firstRead, found := findReviewTranscriptPair(model.calls[1], "budget-read")
	require.True(t, found)
	require.Contains(t, firstRead.Content, "TAIL", "the reader must inspect evidence actually omitted from the snapshot")
	_, retriedRead, found := findReviewTranscriptPair(model.calls[2], "budget-read")
	require.True(t, found)
	require.Equal(t, firstRead.Content, retriedRead.Content, "budgeting never throws away already inspected evidence")
	require.Equal(t, 2100, state.TurnUsage.CompletionTokens)
	require.Equal(t, 200, budget.remaining())
}

func TestSourceReviewBudgetExhaustionCannotProduceAnAcceptedVerdict(t *testing.T) {
	state := sourceConsistencyState()
	body := strings.Repeat("source-evidence-", 2200) + "TAIL"
	chunks := state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)
	chunks[0]["content"] = body
	engine := sourceConsistencyEngine(t, &mockChat{})
	seed := newReviewEvidenceReader(engine.sourceConsistencyMessages(state, nil))
	payloadID := reviewEvidencePayloadID(seed)
	read := reviewRetryUsage(reviewEvidenceToolResponse(reviewEvidenceToolCall(t, "last-budget-read", payloadID, "/data/chunks/0/content", len([]rune(body))-4, 4)), 1, 400)
	model := &mockChat{responses: []mockResponse{read, sourceAcceptedVerdict()}}
	engine.chatModel = model
	budget := &reviewGenerationBudget{purpose: "source_review", completionLimit: 400}
	ctx := context.WithValue(t.Context(), reviewGenerationBudgetKey{}, budget)
	decision := engine.checkCandidate(ctx, "State the actual source's scope.", "The source establishes the candidate's scope.", "", state, nil, false)
	require.Equal(t, candidateProtocolFailure, decision.Disposition)
	require.ErrorIs(t, decision.Err, errSourceReviewBudget)
	require.Len(t, model.calls, 1, "neither a reader continuation nor a protocol retry receives a fresh budget")
	require.Empty(t, state.FinalAnswer)
	require.Empty(t, engine.answerReviewFeedback)
	require.Equal(t, 400, state.TurnUsage.CompletionTokens)
	require.False(t, engine.candidateApprovalCurrent(decision.Candidate, state))
}

type deadlineReviewChat struct {
	mockChat
	attempts int
}

func (model *deadlineReviewChat) ChatStream(ctx context.Context, _ []chat.Message, _ *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	model.attempts++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSourceReviewDeadlineStopsRetriesWithoutApprovingTheCandidate(t *testing.T) {
	model := &deadlineReviewChat{}
	engine := sourceConsistencyEngine(t, &model.mockChat)
	engine.chatModel = model
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	state := sourceConsistencyState()
	decision := engine.checkCandidate(ctx, "Summarize the actual source.", "The candidate describes the observed result.", "", state, nil, false)
	require.Equal(t, candidateProtocolFailure, decision.Disposition)
	require.ErrorIs(t, decision.Err, context.DeadlineExceeded)
	require.Equal(t, 1, model.attempts)
	require.Empty(t, state.FinalAnswer)
	require.False(t, engine.candidateApprovalCurrent(decision.Candidate, state))
	require.Empty(t, engine.answerReviewFeedback)
}

func TestSourceReviewMissingUsageAndProviderOverrunDoNotBypassBudget(t *testing.T) {
	for _, withUsage := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing usage", true: "reported overrun"}[withUsage], func(t *testing.T) {
			response := sourceConsistencyVerdict(`{"complete":true,"unrecognized":"` + strings.Repeat("x", 300) + `"}`)
			if withUsage {
				response = reviewRetryUsage(response, 1, 201)
			}
			model := &mockChat{responses: []mockResponse{response, sourceAcceptedVerdict()}}
			engine := sourceConsistencyEngine(t, model)
			budget := &reviewGenerationBudget{purpose: "source_review", completionLimit: 200}
			ctx := context.WithValue(t.Context(), reviewGenerationBudgetKey{}, budget)
			repair, err := engine.answerNeedsRepair(ctx, "Summarize the actual source.", "A candidate to verify against the source.", "", sourceConsistencyState())
			require.True(t, repair)
			require.ErrorIs(t, err, errSourceReviewBudget)
			require.Len(t, model.calls, 1)
			require.Empty(t, engine.answerReviewFeedback)
		})
	}
}
