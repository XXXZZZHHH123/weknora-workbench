package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUnverifiedCitationSearchTargetScopeCannotChangeBeforeOrAfterWarningBinding(t *testing.T) {
	for _, phase := range []string{"before_warning", "after_warning"} {
		for _, mutation := range []string{"tenant", "physical_tags", "logical_tags", "resolved_documents"} {
			t.Run(phase+"/"+mutation, func(t *testing.T) {
				e, state, _ := unverifiedRawCitationFixture(t)
				e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, TenantID: 7, KnowledgeBaseID: "actual-library",
					KnowledgeIDs: []string{"actual-document"}, TagIDs: []string{"physical-tag"}, ScopeTagIDs: []string{"logical-tag"}}}
				candidate := e.turnReview.UnverifiedCandidate
				rememberUnverifiedCitationFixture(t, e, state, candidate)
				originalKBs, originalDocs := e.config.KnowledgeBases, e.config.KnowledgeIDs
				originalEvidence := reviewDigest(state.RoundSteps[0].ToolCalls[0].Result.Data)
				if phase == "after_warning" {
					e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
				}
				target := e.config.SearchTargets[0]
				switch mutation {
				case "tenant":
					target.TenantID = 8
				case "physical_tags":
					target.TagIDs = []string{"another-physical-tag"}
				case "logical_tags":
					target.ScopeTagIDs = []string{"another-logical-tag"}
				case "resolved_documents":
					target.KnowledgeIDs = []string{"another-document"}
				}
				require.Equal(t, originalKBs, e.config.KnowledgeBases)
				require.Equal(t, originalDocs, e.config.KnowledgeIDs)
				require.Equal(t, originalEvidence, reviewDigest(state.RoundSteps[0].ToolCalls[0].Result.Data), "unchanged source bytes do not authorize a changed runtime target")
				if phase == "before_warning" {
					e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
				}
				e.publishFinalKnowledgeReferences(t.Context(), state, "session")
				require.NotContains(t, state.FinalAnswer, "<evidence")
				require.Empty(t, state.EvidenceRefs)
				require.Empty(t, state.KnowledgeRefs)
				if phase == "before_warning" {
					require.Equal(t, answerReviewUnavailableFallback, state.FinalAnswer, "a changed authorization scope cannot deliver an earlier answer body under a warning")
				} else {
					require.Contains(t, state.FinalAnswer, "未能完成自动核验", "an already committed warning retains its body while stale navigation is removed")
				}
			})
		}
	}
}
