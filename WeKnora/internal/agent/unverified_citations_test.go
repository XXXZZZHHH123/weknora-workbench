package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func rememberUnverifiedCitationFixture(t *testing.T, e *AgentEngine, state *types.AgentState, candidate string) {
	t.Helper()
	e.turnReview.Query = "Report the selected observation."
	d := candidateDecision{Disposition: candidateProtocolFailure, Candidate: candidate, SourceReviewAttempted: true,
		CandidateHash: candidateTextDigest(candidate), TaskHash: e.candidateTaskDigest(e.turnReview.Query, nil),
		EvidenceHash: reviewDigest(e.sourceConsistencyMessages(state, nil)), SourceScopeHash: e.unverifiedSourceScopeDigest(t.Context())}
	e.turnReview.Last = d
	e.rememberUnverifiedCandidate(candidate, d)
}

func unverifiedRawCitationFixture(t *testing.T) (*AgentEngine, *types.AgentState, string) {
	t.Helper()
	e, state := rawCitationFixture(t)
	require.NotEmpty(t, e.currentStructuredCitationView(state.RoundSteps[0].ToolCalls[0]))
	id, _ := selectRawCitation(t, e, 1, 1)
	rememberUnverifiedCitationFixture(t, e, state, `The selected observation is 12.500 kg.<evidence evidence_id="`+id+`" />`)
	return e, state, id
}

func TestUnverifiedCitationStrictCriticFailureRetainsNavigationWithoutApproval(t *testing.T) {
	e, state, id := unverifiedRawCitationFixture(t)
	model := &mockChat{responses: []mockResponse{
		sourceConsistencyVerdict(`{'complete': true}`),
		sourceConsistencyVerdict(`{"answer":"not a verdict"}`),
		sourceConsistencyVerdict(`{"complete":true}`),
	}}
	e.chatModel = model
	candidate := e.turnReview.UnverifiedCandidate
	d := e.checkCandidate(t.Context(), e.turnReview.Query, candidate, "", state, nil, true)
	require.Equal(t, candidateProtocolFailure, d.Disposition)
	require.Error(t, d.Err)
	require.Empty(t, d.Unsupported, "a semantic/protocol failure must not waive numeric grounding")
	require.Len(t, model.calls, 3, "the original strict review budget remains bounded")
	var completed event.AgentCompleteData
	e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		completed = evt.Data.(event.AgentCompleteData)
		return nil
	})
	e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
	e.emitCompletionEvent(t.Context(), state, "session", "message", time.Now())
	require.Contains(t, completed.FinalAnswer, "未能完成自动核验")
	require.Contains(t, completed.FinalAnswer, `evidence_id="`+id+`"`)
	require.Equal(t, state.FinalAnswer, completed.FinalAnswer)
	refs := types.EvidenceReferencesFromInternal(completed.EvidenceRefs)
	require.Len(t, refs, 1)
	require.Equal(t, "unverified", refs[0].ReviewStatus)
	require.Equal(t, candidateTextDigest(completed.FinalAnswer), refs[0].CandidateHash)
	require.NoError(t, refs[0].ValidateStored())
	require.Contains(t, string(refs[0].ClaimBindings), `"navigation_only":true`)
	require.Contains(t, string(refs[0].ClaimBindings), `"protocol":"unverified_navigation_v1"`)
	require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
	require.Empty(t, e.finalAnswerEvidenceReferences(state), "unverified navigation never grants normal approval or automatic chart delivery")
	raw, err := json.Marshal(refs.PublicSummaries())
	require.NoError(t, err)
	require.Contains(t, string(raw), `"review_status":"unverified"`)
	require.NotContains(t, string(raw), "navigation_only")
	require.NotContains(t, string(raw), "source_cells")
}

func TestUnverifiedCitationFinalCompletionRechecksTaskSourceScopeAndExactBody(t *testing.T) {
	for _, mutation := range []string{"task", "source", "scope", "candidate", "base", "warning", "decision", "client", "reference"} {
		t.Run(mutation, func(t *testing.T) {
			e, state, id := unverifiedRawCitationFixture(t)
			e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
			original := state.FinalAnswer
			switch mutation {
			case "task":
				e.turnReview.Query = "A changed task."
			case "source":
				state.RoundSteps[0].ToolCalls[0].Result.Data["changed_after_review"] = true
			case "scope":
				e.config.KnowledgeIDs = []string{"another-authorized-document"}
			case "candidate":
				state.FinalAnswer = "Later unreviewed prose " + original
			case "base":
				e.turnReview.UnverifiedCandidate += " Changed base."
			case "warning":
				e.turnReview.UnverifiedUserNotice = "A later caution."
			case "decision":
				e.turnReview.UnverifiedDecision.CandidateHash = strings.Repeat("f", 64)
			case "client":
				e.config.EvidenceReferencesEnabled = false
			case "reference":
				o := e.turnReview.StructuredCitations[id]
				o.Reference.Manifest = json.RawMessage(`{"changed":true}`)
				e.turnReview.StructuredCitations[id] = o
			}
			var completed event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				completed = evt.Data.(event.AgentCompleteData)
				return nil
			})
			e.emitCompletionEvent(t.Context(), state, "session", "message", time.Now())
			require.Equal(t, filterFallbackCitationTags(original, nil, nil), completed.FinalAnswer,
				"completion/persistence must receive the reconciled server warning body, never a stale local answer or later substituted prose")
			require.Equal(t, state.FinalAnswer, completed.FinalAnswer)
			require.Empty(t, completed.EvidenceRefs)
			require.Empty(t, completed.KnowledgeRefs)
			require.Contains(t, completed.FinalAnswer, "未能完成自动核验")
			require.NotContains(t, completed.FinalAnswer, "<evidence")
			require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
		})
	}
}

func TestUnverifiedCitationSanitizesUnknownMalformedAndWarningChosenSources(t *testing.T) {
	e, state, id := unverifiedRawCitationFixture(t)
	valid := `<evidence evidence_id="` + id + `" />`
	base := "12.500 kg." + valid + `<evidence evidence_id="ev_unknown" /><evidence evidence_id='` + id + `' />` +
		`<kb chunk_id="unknown"/><kb bogus="x" /><EVIDENCE evidence_id="` + id + `" /></kb><evidence`
	rememberUnverifiedCitationFixture(t, e, state, base)
	e.turnReview.UnverifiedUserNotice = "Check the source. " + valid + `<kb chunk_id="unknown" />`
	e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
	e.publishFinalKnowledgeReferences(t.Context(), state, "session")
	require.Len(t, state.EvidenceRefs, 1)
	require.Equal(t, id, state.EvidenceRefs[0].ID)
	require.Equal(t, 1, strings.Count(state.FinalAnswer, "<evidence"), "only the base's actual canonical current source survives")
	require.NotContains(t, state.FinalAnswer, "<kb")
	require.NotContains(t, state.FinalAnswer, "<EVIDENCE")
	require.NotContains(t, state.FinalAnswer, "ev_unknown")
	require.Contains(t, state.FinalAnswer, "Check the source.")
	require.Contains(t, state.FinalAnswer, "部分引用无法提供核查入口")
	require.Equal(t, candidateTextDigest(state.FinalAnswer), state.EvidenceRefs[0].CandidateHash)
}

func TestUnverifiedCitationNoPrivateDecisionOrChangedBeforeDeliveryCannotNavigate(t *testing.T) {
	for _, mutation := range []string{"unbound", "not_reviewed", "scope", "source", "numeric_failure", "unsupported", "public_only"} {
		t.Run(mutation, func(t *testing.T) {
			e, state, id := unverifiedRawCitationFixture(t)
			switch mutation {
			case "unbound":
				e.turnReview.UnverifiedDecision.SourceScopeHash = ""
			case "not_reviewed":
				e.turnReview.UnverifiedDecision.SourceReviewAttempted = false
			case "scope":
				e.config.KnowledgeBases = []string{"another-library"}
			case "source":
				state.RoundSteps[0].ToolCalls[0].Result.Success = false
			case "numeric_failure":
				e.turnReview.UnverifiedDecision.Disposition = candidateNumericFailure
			case "unsupported":
				e.turnReview.UnverifiedDecision.Unsupported = []string{"999"}
			case "public_only":
				e.turnReview.StructuredCitations = nil
				e.modelContext.RegisterEvidenceCitation(modelcontext.EvidenceCitation{ID: id})
				state.EvidenceRefs = types.EvidenceReferences{{ID: id, ReviewStatus: "unverified"}}
			}
			e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
			e.publishFinalKnowledgeReferences(t.Context(), state, "session")
			require.Contains(t, state.FinalAnswer, "未能完成自动核验")
			require.NotContains(t, state.FinalAnswer, "<evidence")
			require.Empty(t, state.EvidenceRefs)
			require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
		})
	}
}

func TestUnverifiedCitationOversizedBindingsRemoveTagsInsteadOfSilentStorageDrop(t *testing.T) {
	e, state, id := unverifiedRawCitationFixture(t)
	base := strings.Repeat(`The selected observation is 12.500 kg.<evidence evidence_id="`+id+`" />`, 2000)
	rememberUnverifiedCitationFixture(t, e, state, base)
	e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
	e.publishFinalKnowledgeReferences(t.Context(), state, "session")
	require.NotContains(t, state.FinalAnswer, "<evidence")
	require.Empty(t, state.EvidenceRefs)
	require.Contains(t, state.FinalAnswer, "未能完成自动核验")
	require.Contains(t, state.FinalAnswer, "部分引用无法提供核查入口")
}

func TestUnverifiedLegacyCitationKeepsOnlyActualCurrentReadAndVersion(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "changed"}[changed], func(t *testing.T) {
			e, state := citationDeliveryFixture(t, &mockChat{}, true)
			rememberUnverifiedCitationFixture(t, e, state, `The indicator measures covered area.<kb chunk_id="current-b" />`)
			if changed {
				state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)[1]["content"] = "A replaced current passage."
			}
			e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
			e.publishFinalKnowledgeReferences(t.Context(), state, "session")
			require.Empty(t, state.EvidenceRefs)
			require.Contains(t, state.FinalAnswer, "未能完成自动核验")
			if changed {
				require.Empty(t, state.KnowledgeRefs)
				require.NotContains(t, state.FinalAnswer, "<kb")
			} else {
				require.Len(t, state.KnowledgeRefs, 1)
				require.Equal(t, "current-b", state.KnowledgeRefs[0].ID)
				require.Equal(t, 3, state.KnowledgeRefs[0].ContentRevision)
				require.NotEmpty(t, state.KnowledgeRefs[0].SourceLocators)
			}
			require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
		})
	}
}

func TestUnverifiedCitationDoesNotChangeNormalApprovedReferenceStatus(t *testing.T) {
	e, state, _ := unverifiedRawCitationFixture(t)
	approveCitationCandidate(e, state, e.turnReview.UnverifiedCandidate)
	e.turnReview.Committed, state.IsComplete = true, true
	e.publishFinalKnowledgeReferences(t.Context(), state, "session")
	require.Len(t, state.EvidenceRefs, 1)
	require.Empty(t, state.EvidenceRefs[0].ReviewStatus)
	require.NotContains(t, string(state.EvidenceRefs[0].ClaimBindings), "unverified_navigation")
	require.True(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
}
