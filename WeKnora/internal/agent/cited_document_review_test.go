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
	for _, scenario := range []string{"missing", "conflicted", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			model := &mockChat{}
			e, state, candidate := directCitationReadFixture(t, model, true)
			e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
			switch scenario {
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

func TestReadDocumentReviewWithoutInlineCitationsRetainsAllViews(t *testing.T) {
	for _, tool := range []string{"read_document", "search_knowledge"} {
		t.Run(tool, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{lightReviewEdits(structuredResultEdit{"length", "covered area"})}}
			e, state, _ := directCitationReadFixture(t, model, true)
			e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
			call := &state.RoundSteps[0].ToolCalls[0]
			if tool == "search_knowledge" {
				call.Name = tool
				call.Result.Data["results"] = call.Result.Data["chunks"]
				delete(call.Result.Data, "chunks")
			}
			state.RoundSteps = append(state.RoundSteps, state.RoundSteps[0])
			before := reviewDigest(state.RoundSteps)
			d := e.checkCandidate(t.Context(), "Describe the indicator.", "The indicator measures length.", "", state, nil, false)
			require.Equal(t, candidateAccepted, d.Disposition)
			require.Equal(t, "corrected", d.StructuredResultReviewStatus)
			require.Equal(t, "The indicator measures covered area.", d.Candidate)
			require.Equal(t, before, reviewDigest(state.RoundSteps))
			require.Len(t, model.calls, 1)
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
			require.NotContains(t, input, "actual_current_cited_documents")
			groups := input["actual_current_read_documents"].([]any)
			require.Len(t, groups, 1)
			group := groups[0].(map[string]any)
			docs := group["views"].([]any)
			require.Len(t, docs, 2, "all distinct source views, including the uncited neighbor, survive")
			for _, raw := range docs {
				view := raw.(map[string]any)
				for field, value := range group["shared_fields"].(map[string]any) {
					view[field] = value
				}
				require.Len(t, view["tool_call_ids"], 2, "only identical views are deduplicated")
				require.NotContains(t, view, "citation_handle")
				require.NotEmpty(t, view["source_view"])
				require.Equal(t, "contract-document", view["knowledge_id"])
			}
			require.Contains(t, model.calls[0][0].Content, "apply all proposed replacements to the entire candidate")
		})
	}
}

func TestReadDocumentReviewRejectsUnavailableEvidenceAndDoesNotRegister(t *testing.T) {
	for _, scenario := range []string{"failed", "forged_output", "other_executor", "unregistered", "no_current_read", "malformed", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			model := &mockChat{}
			e, state, _ := directCitationReadFixture(t, model, true)
			e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
			call := &state.RoundSteps[0].ToolCalls[0]
			switch scenario {
			case "failed":
				call.Result.Success = false
			case "forged_output":
				call.Result.Data = map[string]any{}
			case "other_executor":
				call.Target = &types.ToolCallTarget{Name: "unrelated_tool"}
			case "unregistered":
				call.Result.Data["chunks"].([]map[string]any)[0]["chunk_id"] = "not-registered"
			case "no_current_read":
				state.RoundSteps = nil
			case "malformed":
				call.Result.Data["chunks"] = "not a row list"
			case "oversized":
				call.Result.Data["chunks"].([]map[string]any)[0]["content"] = strings.Repeat("source body ", structuredResultReviewInputBytes)
			}
			d := e.checkStructuredResultDelivery(t.Context(), "Explain.", candidateDecision{Disposition: candidateAccepted, Candidate: "A source-backed explanation."}, state, nil)
			require.Empty(t, model.calls)
			require.Empty(t, e.modelContext.CitableChunkHandle("not-registered"))
			if scenario == "oversized" {
				require.Equal(t, "not_checked_input_budget", d.StructuredResultReviewStatus)
			}
			if scenario == "unregistered" || scenario == "malformed" {
				require.Equal(t, "not_checked_document_omission", d.StructuredResultReviewStatus)
			}
		})
	}
}

func TestReadDocumentFallbackDoesNotDisableExistingEditorEvidence(t *testing.T) {
	for _, kind := range []string{"structured", "geometry"} {
		t.Run(kind, func(t *testing.T) {
			e, state, model := lightStructuredFixture(t, lightReviewReply("keep", ""))
			if kind == "geometry" {
				e, state, model = lightGeometryEngine(t, lightReviewReply("keep", ""))
			}
			result := realCitationReadResult(t)
			result.Data["chunks"].([]map[string]any)[0]["content"] = strings.Repeat("body", structuredResultReviewInputBytes)
			state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "large-read", Name: "read_document", Result: result}}})
			e.appendToolResults(emptyMessages(), state.RoundSteps[len(state.RoundSteps)-1])
			d := e.checkStructuredResultDelivery(t.Context(), "Describe the source.", candidateDecision{Disposition: candidateAccepted, Candidate: "The source has an observation."}, state, nil)
			require.Equal(t, "kept", d.StructuredResultReviewStatus)
			require.Len(t, model.calls, 1)
			require.NotContains(t, model.calls[0][1].Content, "actual_current_read_documents")
		})
	}
}

func TestReadDocumentReviewRetainsDifferentAuthorizedViewsWithoutExpandingScope(t *testing.T) {
	full, page, other := realCitationViewResults(t)
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(full, page, other)
	for _, step := range state.RoundSteps {
		e.appendToolResults(emptyMessages(), step)
	}
	documents, complete := e.currentReadDocumentReviewEvidence(state, nil)
	require.True(t, complete)
	require.Len(t, documents, 3, "distinct views are not sampled or replaced by the full chunk")
	for index, result := range []*types.ToolResult{full, page, other} {
		row := result.Data["chunks"].([]map[string]any)[0]
		require.Equal(t, row["content"], documents[index]["content"])
		require.Equal(t, row["content_complete"], documents[index]["source_view"].(map[string]any)["content_complete"])
		want, err := json.Marshal(row["content_source_view"])
		require.NoError(t, err)
		got, err := json.Marshal(documents[index]["source_view"].(map[string]any)["content_source_view"])
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got))
	}
}

func TestReadDocumentReviewOmitsOnlyLocatorQuotesAndSharesMetadataLosslessly(t *testing.T) {
	model := &mockChat{}
	e, state, _ := directCitationReadFixture(t, model, true)
	row := state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)[1]
	row["source_locators"] = []map[string]any{{"type": "pdf", "page": 8, "source_hash": strings.Repeat("b", 64), "partial": true,
		"bbox": []float64{0.1, 0.2, 0.7, 0.8}, "quote": "A locator overlap, possibly outside this bounded body.", "future_position": "preserved"}}
	before := reviewDigest(state.RoundSteps)
	views, complete := e.currentReadDocumentReviewEvidence(state, nil)
	require.True(t, complete)
	require.Len(t, views, 2)
	locators := views[1]["source_locators"].([]map[string]any)
	require.Len(t, locators, 1)
	require.NotContains(t, locators[0], "quote")
	require.Equal(t, true, views[1]["locator_quotes_omitted"])
	require.Equal(t, "preserved", locators[0]["future_position"])
	require.Equal(t, float64(8), locators[0]["page"])
	require.Equal(t, row["content"], views[1]["content"])
	require.Equal(t, before, reviewDigest(state.RoundSteps), "source validators retain original locator quotes and all metadata")
	views[0]["nullable"] = nil
	groups := compactCurrentReadDocuments(views)
	require.Len(t, groups, 1)
	shared := groups[0]["shared_fields"].(map[string]any)
	require.NotContains(t, shared, "nullable", "missing is not explicit null")
	decoded := []map[string]any{}
	for _, raw := range groups[0]["views"].([]map[string]any) {
		view := map[string]any{}
		for field, value := range shared {
			view[field] = value
		}
		for field, value := range raw {
			view[field] = value
		}
		decoded = append(decoded, view)
	}
	require.Equal(t, views, decoded, "metadata factoring is lossless for every retained body, identity and scope")
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Contains(t, structuredResultReviewInstructions, "never refute a claim merely because a locator snippet is not shown")
}

func TestDocumentBodyPoolPreservesIndependentSourcesAndEveryByte(t *testing.T) {
	body := "The same source text.\n汉字🙂\n"
	views := []map[string]any{
		{"content": body, "citation_handle": "first", "knowledge_id": "document-a", "content_revision": 2, "source_view": map[string]any{"content_offset": 0, "content_complete": true}, "source_locators": []map[string]any{{"page": 4, "quote": body}}},
		{"content": body, "citation_handle": "second", "knowledge_id": "document-b", "content_revision": 7, "source_view": map[string]any{"content_offset": 6, "content_complete": false}, "source_locators": []map[string]any{{"page": 9, "quote": body}}},
		{"content": body + " ", "citation_handle": "third", "knowledge_id": "document-b", "content_revision": 8, "source_view": map[string]any{"content_offset": 6, "content_complete": false}, "source_locators": []map[string]any{{"page": 9}}},
	}
	before := reviewDigest(views)
	groups, bodies, complete := compactDocumentReviewBodies(views)
	require.True(t, complete)
	require.Len(t, bodies, 1)
	require.Equal(t, body, bodies["body_1"])
	decoded := map[string]map[string]any{}
	for _, group := range groups {
		for _, raw := range group["views"].([]map[string]any) {
			view := map[string]any{}
			for field, value := range group["shared_fields"].(map[string]any) {
				view[field] = value
			}
			for field, value := range raw {
				view[field] = value
			}
			if ref, ok := view["content_ref"].(string); ok {
				content, exists := bodies[ref]
				require.True(t, exists, "every body reference must resolve")
				view["content"] = content
				delete(view, "content_ref")
			}
			decoded[view["citation_handle"].(string)] = view
		}
	}
	require.Len(t, decoded, len(views), "equal content never merges source entries")
	for _, original := range views {
		want := map[string]any{}
		for field, value := range original {
			want[field] = value
		}
		locators, ok := readReviewLocatorPositions(original["source_locators"])
		require.True(t, ok)
		want["source_locators"], want["locator_quotes_omitted"] = locators, true
		require.Equal(t, want, decoded[original["citation_handle"].(string)], "body, source, revision and scope survive without reinterpretation")
	}
	require.Equal(t, before, reviewDigest(views))
}

func TestCitedDocumentReviewRecoversBudgetOnlyWithCompleteEvidence(t *testing.T) {
	for _, repeated := range []bool{true, false} {
		t.Run(map[bool]string{true: "identical", false: "different"}[repeated], func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{lightReviewReply("keep", "")}}
			e, state, _ := directCitationReadFixture(t, model, true)
			e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
			rows := state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)
			body := strings.Repeat("A source observation. ", 750)
			rows[0]["content"], rows[1]["content"] = body, body
			if !repeated {
				rows[1]["content"] = body + " "
			}
			e.appendToolResults(emptyMessages(), state.RoundSteps[0])
			candidate := `Both source passages were read.<kb chunk_id="contract-neighbor" /><kb chunk_id="contract-supporting" />`
			before := reviewDigest(state.RoundSteps)
			d := e.checkStructuredResultDelivery(t.Context(), "Compare the sources.", candidateDecision{Disposition: candidateAccepted, Candidate: candidate}, state, nil)
			require.Equal(t, before, reviewDigest(state.RoundSteps))
			if !repeated {
				require.Equal(t, "not_checked_input_budget", d.StructuredResultReviewStatus)
				require.Empty(t, model.calls, "different full bodies cannot be sampled or normalized to fit")
				return
			}
			require.Equal(t, "kept", d.StructuredResultReviewStatus)
			require.Len(t, model.calls, 1)
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
			require.Equal(t, body, input["shared_content_bodies"].(map[string]any)["body_1"])
			groups := input["actual_current_cited_documents"].([]any)
			require.Len(t, groups, 1)
			group := groups[0].(map[string]any)
			require.Len(t, group["views"], 2)
			require.Equal(t, true, group["shared_fields"].(map[string]any)["locator_quotes_omitted"])
			require.Contains(t, model.calls[0][0].Content, "every reference must be resolved")
		})
	}
}

func TestCitedDocumentBodyPoolCannotResolveConflictingVersions(t *testing.T) {
	model := &mockChat{}
	e, state, candidate := directCitationReadFixture(t, model, true)
	e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
	call := state.RoundSteps[0].ToolCalls[0]
	raw, err := json.Marshal(call.Result)
	require.NoError(t, err)
	call.Result = &types.ToolResult{}
	require.NoError(t, json.Unmarshal(raw, call.Result))
	call.ID = "different-version-same-text"
	call.Result.Data["chunks"].([]any)[1].(map[string]any)["content_revision"] = 99
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
	d := e.checkStructuredResultDelivery(t.Context(), "Describe the indicator.", candidateDecision{Disposition: candidateAccepted, Candidate: candidate}, state, nil)
	require.Equal(t, "not_checked_document_omission", d.StructuredResultReviewStatus)
	require.Empty(t, model.calls, "identical text cannot qualify a conflicting source version")
}
