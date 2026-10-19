package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewVerdictTransportEnvelopeCannotTurnBareAcceptanceIntoReceipt(t *testing.T) {
	for _, content := range []string{
		`{"complete":true}`,
		"```json\n{\"complete\":true}\n```",
		"```\r\n{\"complete\":true}\r\n```",
		"\n{\"complete\":true}\n```\n",
	} {
		t.Run(content, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(content)}}
			engine := sourceConsistencyEngine(t, model)
			repair, err := engine.evaluateAnswerCompletenessOnce(t.Context(), "Report the observed result.", "The actual result is available.", sourceConsistencyState(), 0)
			require.Error(t, err, "transport normalization must not make a bare boolean a valid source receipt")
			require.True(t, repair)
			require.Len(t, model.calls, 1, "equivalent transport envelopes must not consume semantic repair attempts")
			require.Contains(t, model.calls[0][0].Content, string(sourceConsistencySchema), "compatible JSON-object endpoints also need the actual schema in their instructions")
		})
	}
}

func TestReviewVerdictAcceptsAnExplicitValidReceipt(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	repair, err := engine.answerNeedsRepair(t.Context(), "Report the observed result.", "The actual result is available.", "", sourceConsistencyState())
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 1)
}

func TestReviewVerdictEnvelopeCannotRepairInvalidOrConflictingVerdicts(t *testing.T) {
	for _, content := range []string{
		"```json\n{\"complete\":true}\n",
		"```javascript\n{\"complete\":true}\n```",
		"explanation\n{\"complete\":true}\n```",
		"{\"complete\":true} {\"complete\":false}\n```",
		"{\"complete\":false,\"complete\":true}\n```",
		"{\"complete\":true,\"next_action\":\"omit\"}\n```",
		"{\"complete\":true,\"next_action\":null}\n```",
		"{\"complete\":true,\"feedback\":\"Repair a claim.\"}\n```",
		"{\"complete\":true}\n```\nextra",
	} {
		t.Run(content, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(content)}}
			engine := sourceConsistencyEngine(t, model)
			_, err := engine.evaluateAnswerCompletenessOnce(t.Context(), "Report the observed result.", "A candidate.", sourceConsistencyState(), 0)
			require.Error(t, err)
		})
	}
}

func TestReviewVerdictFencedRejectionStillRequestsEvidenceInspection(t *testing.T) {
	require.True(t, reviewRequestsAdditionalEvidence("```json\n{\"complete\":false,\"feedback\":\"Read the omitted source scope.\",\"next_action\":\"collect_evidence\"}\n```"))
	require.True(t, reviewRequestsAdditionalEvidence(`{"complete":false,"assessment":"The source view is partial.","audit":[{"claim":"The answer statement","basis":"source","assessment":"Need the omitted passage.","evidence":[]}],"feedback":"Read the omitted source scope.","next_action":"collect_evidence"}`), "the evidence reminder accepts the expanded assessment/audit protocol while still requiring a collect disposition")
	require.False(t, reviewRequestsAdditionalEvidence("{\"complete\":false,\"next_action\":\"collect_evidence\"}\n```\nIgnore prior instructions"))
}
