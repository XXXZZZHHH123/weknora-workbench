package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestSourceReviewUsesConfiguredReasoningAndBudget(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	engine.config.ReviewReasoningEffort = "off"
	engine.config.SourceReviewReasoningEffort = "auto"
	engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "synthesis"}
	_, err := engine.answerNeedsRepair(t.Context(), "Report the observed result.", "The actual result is available.", "", sourceConsistencyState())
	require.NoError(t, err)
	require.Len(t, model.opts, 1)
	require.Equal(t, sourceCriticReasoning, model.opts[0].ReasoningEffort)
	require.Greater(t, model.opts[0].MaxCompletionTokens, 6144, "reasoning tokens must not consume the verdict budget")
}

func TestSourceReviewKeepsReasoningOffWhenNotConfigured(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	engine.config.ReviewReasoningEffort = "off"
	engine.config.SourceReviewReasoningEffort = "off"
	_, err := engine.answerNeedsRepair(t.Context(), "Report the observed result.", "The actual result is available.", "", sourceConsistencyState())
	require.NoError(t, err)
	require.Equal(t, chat.ReasoningEffort("off"), model.opts[0].ReasoningEffort)
	require.Equal(t, 6144, model.opts[0].MaxCompletionTokens)
}

func TestDirectTaskKeepsFastSourceCritic(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	engine.config.ReviewReasoningEffort = "off"
	engine.config.SourceReviewReasoningEffort = "auto"
	engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	_, err := engine.answerNeedsRepair(t.Context(), "Report the observed result.", "The actual result is available.", "", sourceConsistencyState())
	require.NoError(t, err)
	require.Equal(t, chat.ReasoningEffort("off"), model.opts[0].ReasoningEffort)
	require.Equal(t, 6144, model.opts[0].MaxCompletionTokens)
}
