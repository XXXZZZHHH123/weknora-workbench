package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitedDocumentReviewCorrectsContradictionWithoutReplacingCore(t *testing.T) {
	model := &mockChat{responses: []mockResponse{lightReviewEdits(structuredResultEdit{"The indicator measures length.", "The indicator measures covered area."})}}
	e, state, citation := directCitationReadFixture(t, model, true)
	e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
	candidate := citation + " The indicator measures length."
	before := reviewDigest(state.RoundSteps)
	d := e.checkCandidate(t.Context(), "Describe the indicator.", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, d.Disposition)
	require.Equal(t, "corrected", d.StructuredResultReviewStatus)
	require.Equal(t, citation+" The indicator measures covered area.", d.Candidate)
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Zero(t, e.turnReview.Repairs)
	require.False(t, d.SourceReviewAttempted)
	require.Len(t, model.calls, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	docs := input["actual_current_cited_documents"].([]any)
	require.Len(t, docs, 1)
	require.Equal(t, "The indicator measures covered area.", docs[0].(map[string]any)["content"])
	require.NotContains(t, model.calls[0][1].Content, "reporting organization")
	e.checkCandidate(t.Context(), "Describe the indicator.", d.Candidate, "", state, nil, true)
	require.Len(t, model.calls, 1, "document evidence shares the existing one-call budget")
}

func TestCitedDocumentReviewRetainsEntireAuthorizedPartialView(t *testing.T) {
	_, page, _ := realCitationViewResults(t)
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(page)
	e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	views, complete := e.currentCitedDocumentReviewEvidence(`<kb chunk_id="view-shared" />`, state, nil)
	require.True(t, complete)
	require.Len(t, views, 1)
	row := page.Data["chunks"].([]map[string]any)[0]
	require.Equal(t, row["content"], views[0]["content"])
	expectedView, err := json.Marshal(row["content_source_view"])
	require.NoError(t, err)
	actualView, err := json.Marshal(views[0]["source_view"].(map[string]any)["content_source_view"])
	require.NoError(t, err)
	require.JSONEq(t, string(expectedView), string(actualView))
	require.Contains(t, views[0]["scope"], "partial chunk")
	require.Contains(t, structuredResultReviewInstructions, "never treat omitted surrounding text as missing facts")
}

func TestCitedDocumentReviewDoesNotReadUncitedOrInvalidBodies(t *testing.T) {
	for _, scenario := range []string{"uncited", "missing", "conflicted", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			model := &mockChat{}
			e, state, candidate := directCitationReadFixture(t, model, true)
			e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
			switch scenario {
			case "uncited":
				candidate = "A general explanation."
			case "missing":
				candidate = `Unknown.<kb chunk_id="unread" />`
			case "conflicted":
				call := state.RoundSteps[0].ToolCalls[0]
				raw, _ := json.Marshal(call.Result)
				call.Result = &types.ToolResult{}
				require.NoError(t, json.Unmarshal(raw, call.Result))
				call.Result.Data["chunks"].([]any)[1].(map[string]any)["content"] = "Different current version."
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
			case "oversized":
				candidate += strings.Repeat("x", structuredResultReviewAnswerBytes)
			}
			d := candidateDecision{Disposition: candidateAccepted, Candidate: candidate}
			result := e.checkStructuredResultDelivery(t.Context(), "Explain.", d, state, nil)
			require.Equal(t, candidate, result.Candidate)
			require.Empty(t, model.calls)
			if scenario == "missing" || scenario == "conflicted" {
				require.Equal(t, "not_checked_document_omission", result.StructuredResultReviewStatus)
			}
		})
	}
}
