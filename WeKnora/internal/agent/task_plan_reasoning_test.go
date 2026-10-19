package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTaskPlanReasoningDepthIsOptionalAndBounded(t *testing.T) {
	base := `{"goals":["g"],"evidence":[],"tool_names":[],"completion_criteria":["c"]`
	plan, err := decodeTaskEvidencePlan(base+`}`, nil, 0)
	require.NoError(t, err)
	require.Empty(t, plan.ReasoningDepth)
	plan, err = decodeTaskEvidencePlan(base+`,"reasoning_depth":"synthesis"}`, nil, 0)
	require.NoError(t, err)
	require.Equal(t, "synthesis", plan.ReasoningDepth)
	_, err = decodeTaskEvidencePlan(base+`,"reasoning_depth":"maximum"}`, nil, 0)
	require.Error(t, err)
}

func TestSynthesisReasoningUpgradesOnlyOffAndCapableModels(t *testing.T) {
	on := true
	for _, tc := range []struct {
		name       string
		depth      string
		capability string
		effort     string
		thinking   *bool
		want       chat.ReasoningEffort
	}{
		{"synthesis upgrades default off", "synthesis", "auto", "", nil, "auto"},
		{"synthesis upgrades explicit off", "synthesis", "auto", "off", nil, "auto"},
		{"direct keeps off", "direct", "auto", "off", nil, ""},
		{"no plan depth keeps off", "", "auto", "off", nil, ""},
		{"model cannot reason", "synthesis", "off", "off", nil, ""},
		{"never lowers a configured level", "synthesis", "auto", "high", nil, ""},
		{"thinking already on", "synthesis", "auto", "", &on, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &AgentEngine{config: &types.AgentConfig{SourceReviewReasoningEffort: tc.capability, ReasoningEffort: tc.effort, Thinking: tc.thinking},
				taskPlan: &taskEvidencePlan{ReasoningDepth: tc.depth}}
			require.Equal(t, tc.want, e.synthesisReasoningEffort())
		})
	}
	require.Equal(t, chat.ReasoningEffort(""), (&AgentEngine{config: &types.AgentConfig{SourceReviewReasoningEffort: "auto"}}).synthesisReasoningEffort(), "no plan this turn")
}

func TestSynthesisAnswerRoundReservesReasoningHeadroom(t *testing.T) {
	e := &AgentEngine{config: &types.AgentConfig{MaxCompletionTokens: 4096, MaxContextTokens: 200000}}
	require.Equal(t, 4096+synthesisReasoningHeadroom, e.clampCompletionBudgetToContextWithHeadroom(1000, synthesisReasoningHeadroom))
	e.config.MaxContextTokens = 20000
	require.LessOrEqual(t, e.clampCompletionBudgetToContextWithHeadroom(15000, synthesisReasoningHeadroom), 20000-15000, "still bounded by the remaining context window")
}
