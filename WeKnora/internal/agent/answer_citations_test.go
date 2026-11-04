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

func citationDeliveryFixture(t *testing.T, model *mockChat, enabled bool) (*AgentEngine, *types.AgentState) {
	t.Helper()
	e := newTestEngine(t, model, func(config *types.AgentConfig) { config.CitationEnabled = &enabled })
	e.config.WorkbenchDefault, e.config.TaskPlanningEnabled = true, true
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID: "citation-read", Name: "read_document", Result: &types.ToolResult{Success: true, Data: map[string]any{
			"display_type": "knowledge_chunks_list", "knowledge_id": "current-document", "knowledge_base_id": "authorized-library", "knowledge_title": "Actual source",
			"document": map[string]any{"knowledge_id": "current-document", "title": "Actual source", "file_name": "actual-source.pdf"},
			"chunks": []map[string]any{
				{"chunk_id": "current-a", "knowledge_id": "current-document", "knowledge_base_id": "authorized-library", "content": "This passage describes the reporting organization.", "chunk_index": 4, "page": 8},
				{"chunk_id": "current-b", "knowledge_id": "current-document", "knowledge_base_id": "authorized-library", "content": "The indicator measures covered area.", "chunk_index": 5, "page": 8, "content_revision": 3,
					"source_locators": []types.SourceLocator{{Type: types.SourceLocatorPDF, Page: 8, SourceHash: strings.Repeat("b", 64), Quote: "The indicator measures covered area."}}},
			},
		}},
	}}}}}
	for _, call := range state.RoundSteps[0].ToolCalls {
		e.modelContext.ModelToolResultForTool(call.ExecutionName(), call.Result)
	}
	return e, state
}

func approveCitationCandidate(e *AgentEngine, state *types.AgentState, candidate string) {
	e.turnReview.Query = "Explain the requested indicator."
	e.turnReview.Conversation = nil
	e.turnReview.Last = candidateDecision{
		Disposition: candidateAccepted, Candidate: candidate, CandidateHash: candidateTextDigest(candidate),
		TaskHash: e.candidateTaskDigest(e.turnReview.Query, nil), EvidenceHash: reviewDigest(e.sourceConsistencyMessages(state, nil)),
	}
	state.FinalAnswer = candidate
}

func TestCitationDeliveryNormalFinishNormalizesAndPublishesPersistableReferences(t *testing.T) {
	model := &mockChat{}
	e, state := citationDeliveryFixture(t, model, true)
	handle := e.modelContext.CitableChunkHandle("current-b")
	model.responses = []mockResponse{
		sourceConsistencyVerdict(`The indicator measures covered area.<ref id="` + handle + `"/>`),
		sourceAcceptedVerdict(),
	}
	var order []string
	var streamed []*types.SearchResult
	var completed []*types.SearchResult
	var approvedAtCompletion bool
	e.eventBus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		require.NotEmpty(t, evt.ID)
		order = append(order, "references")
		streamed = evt.Data.(event.AgentReferencesData).References.([]*types.SearchResult)
		return nil
	})
	e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		order = append(order, "complete")
		approvedAtCompletion = e.candidateApprovalCurrent(state.FinalAnswer, state)
		for _, raw := range evt.Data.(event.AgentCompleteData).KnowledgeRefs {
			completed = append(completed, raw.(*types.SearchResult))
		}
		return nil
	})
	messages := e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	_, err := e.executeLoop(t.Context(), state, "Explain the requested indicator.", messages, nil, "session", "reply")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Contains(t, state.FinalAnswer, `chunk_id="current-b"`)
	require.NotContains(t, state.FinalAnswer, "<ref")
	require.True(t, approvedAtCompletion, "normalization, commit and completion must preserve the approved candidate/evidence binding before turn-local review context is released")
	require.Equal(t, []string{"references", "complete"}, order)
	require.Equal(t, state.KnowledgeRefs, streamed)
	require.Equal(t, streamed, completed)
	require.Len(t, completed, 1, "the adjacent same-page passage was read, but was not selected as this answer's citation")
	ref := completed[0]
	require.Equal(t, "current-b", ref.ID)
	require.Equal(t, "current-document", ref.KnowledgeID)
	require.Equal(t, "authorized-library", ref.KnowledgeBaseID)
	require.Equal(t, "Actual source", ref.KnowledgeTitle)
	require.Equal(t, "actual-source.pdf", ref.KnowledgeFilename)
	require.Equal(t, "The indicator measures covered area.", ref.Content)
	require.Equal(t, 3, ref.ContentRevision)
	require.Equal(t, types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 8, SourceHash: strings.Repeat("b", 64), Quote: ref.Content}}, ref.SourceLocators)
	// These are the same SearchResult pointers the stream handler transfers
	// from AgentComplete to Message.KnowledgeReferences before persistence.
	persisted, err := json.Marshal(types.Message{KnowledgeReferences: types.References(completed)})
	require.NoError(t, err)
	var restored types.Message
	require.NoError(t, json.Unmarshal(persisted, &restored))
	require.Len(t, restored.KnowledgeReferences, 1)
	require.Equal(t, ref.ID, restored.KnowledgeReferences[0].ID)
	require.Equal(t, ref.SourceLocators, restored.KnowledgeReferences[0].SourceLocators)
	require.Len(t, model.calls, 2, "ordinary generation and the existing critic only; reference collection makes no model call")
}

func TestCitationDisabledNormalFinishDoesNotEmitReferences(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict("The indicator measures covered area."), sourceAcceptedVerdict()}}
	e, state := citationDeliveryFixture(t, model, false)
	state.KnowledgeRefs = []*types.SearchResult{{ID: "durable-checkpoint-chunk", KnowledgeID: "durable-document"}}
	var referenceEvents int
	var completed event.AgentCompleteData
	var approvedAtCompletion bool
	e.eventBus.On(event.EventAgentReferences, func(_ context.Context, _ event.Event) error {
		referenceEvents++
		return nil
	})
	e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		approvedAtCompletion = e.candidateApprovalCurrent(state.FinalAnswer, state)
		completed = evt.Data.(event.AgentCompleteData)
		return nil
	})
	_, err := e.executeLoop(t.Context(), state, "Explain the requested indicator.", e.appendToolResults(emptyMessages(), state.RoundSteps[0]), nil, "session", "reply")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.True(t, e.turnReview.Committed)
	require.True(t, approvedAtCompletion)
	require.Zero(t, referenceEvents)
	require.Empty(t, state.KnowledgeRefs, "the completed current answer replaces earlier checkpoint references")
	require.Empty(t, completed.KnowledgeRefs)
	require.Len(t, model.calls, 2)
}

func TestCitationDeliveryOnlyCurrentApprovedModelSelectedIdentitiesQualify(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		candidate string
		alter     func(*AgentEngine, *types.AgentState)
		want      []string
	}{
		{name: "only_selected_not_same_page", enabled: true, candidate: `<kb doc="a guessed title" chunk_id="current-b" />`, want: []string{"current-b"}},
		{name: "plain_filename_is_not_a_citation", enabled: true, candidate: "The indicator measures covered area. Source: actual-source.pdf."},
		{name: "disabled", candidate: `<kb chunk_id="current-b" />`},
		{name: "unknown_not_guessed_from_title", enabled: true, candidate: `<kb doc="Actual source" chunk_id="unknown" />`},
		{name: "registered_but_not_read", enabled: true, candidate: `<kb chunk_id="unread" />`, alter: func(e *AgentEngine, _ *types.AgentState) {
			e.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "unread", KnowledgeID: "current-document"})
		}},
		{name: "failed_read", enabled: true, candidate: `<kb chunk_id="current-b" />`, alter: func(_ *AgentEngine, s *types.AgentState) {
			s.RoundSteps[0].ToolCalls[0].Result.Success = false
		}},
		{name: "history_registration_only", enabled: true, candidate: `<kb chunk_id="current-b" />`, alter: func(_ *AgentEngine, s *types.AgentState) {
			s.RoundSteps = nil
		}},
		{name: "unapproved", enabled: true, candidate: `<kb chunk_id="current-b" />`, alter: func(e *AgentEngine, _ *types.AgentState) {
			e.turnReview.Last.Disposition = candidateRevise
		}},
		{name: "evidence_revision_changed", enabled: true, candidate: `<kb chunk_id="current-b" />`, alter: func(_ *AgentEngine, s *types.AgentState) {
			s.RoundSteps[0].ToolCalls[0].Result.Data["source_revision"] = "new version"
		}},
		{name: "pure_chart", enabled: true, candidate: "![chart](resource://actual-artifact)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, state := citationDeliveryFixture(t, &mockChat{}, tc.enabled)
			approveCitationCandidate(e, state, tc.candidate)
			if tc.alter != nil {
				tc.alter(e, state)
			}
			refs := e.finalAnswerKnowledgeReferences(state)
			var ids []string
			for _, ref := range refs {
				ids = append(ids, ref.ID)
			}
			require.Equal(t, tc.want, ids)
		})
	}
}

func TestCitationPolicyAndCollectorUseExecutionIdentityAndRejectVersionConflicts(t *testing.T) {
	e, state := citationDeliveryFixture(t, &mockChat{}, true)
	call := &state.RoundSteps[0].ToolCalls[0]
	call.Name, call.Target = "provider_proxy", &types.ToolCallTarget{Name: "read_document"}
	approveCitationCandidate(e, state, `<kb chunk_id="current-b" />`)
	require.Len(t, e.finalAnswerKnowledgeReferences(state), 1)
	require.Equal(t, 2, e.sourceCitationReviewPolicy(state, nil)["available_anchor_count"])
	require.Equal(t, "provider_proxy", call.Name, "source projection must not rewrite the provider/history record")
	call.Name, call.Target = "read_document", &types.ToolCallTarget{Name: "mcp_external_read"}
	approveCitationCandidate(e, state, `<kb chunk_id="current-b" />`)
	require.Empty(t, e.finalAnswerKnowledgeReferences(state))
	require.Zero(t, e.sourceCitationReviewPolicy(state, nil)["available_anchor_count"], "external chunks cannot acquire knowledge-source authority through their display shape")

	e, state = citationDeliveryFixture(t, &mockChat{}, true)
	second := state.RoundSteps[0].ToolCalls[0]
	encoded, err := json.Marshal(second.Result)
	require.NoError(t, err)
	second.Result = nil
	require.NoError(t, json.Unmarshal(encoded, &second.Result))
	second.ID = "different-source-version"
	second.Result.Data["chunks"].([]any)[1].(map[string]any)["content_revision"] = 4
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{second}})
	approveCitationCandidate(e, state, `<kb chunk_id="current-b" />`)
	require.Empty(t, e.finalAnswerKnowledgeReferences(state))
	policy := e.sourceCitationReviewPolicy(state, nil)
	require.Equal(t, 1, policy["available_anchor_count"], "the unchanged neighbor remains available, but cannot stand in for the conflicted cited chunk")
}

func citationSQLOnlyState() *types.AgentState {
	return &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID: "actual-pdf-query", Name: "structured_query_pdf_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
			"status": "ok", "kind": "structured_analysis", "source_kind": "pdf_table", "table_id": "actual-table",
			"source":         map[string]any{"knowledge_id": "structured-document", "knowledge_base_id": "authorized-library", "file_name": "actual.pdf"},
			"source_context": map[string]any{"knowledge_base_id": "authorized-library"},
		}},
	}}}}}
}

func TestCitationPolicyDistinguishesSQLNavigationFromFetchedChunkAuthority(t *testing.T) {
	e, _ := citationDeliveryFixture(t, &mockChat{}, true)
	state := citationSQLOnlyState()
	policy := e.sourceCitationReviewPolicy(state, nil)
	require.Zero(t, policy["available_anchor_count"])
	reads := policy["verified_document_reads"].([]any)
	require.Len(t, reads, 1)
	read := reads[0].(map[string]any)
	require.Equal(t, "structured-document", read["knowledge_id"])
	require.Equal(t, "authorized-library", read["knowledge_base_id"])
	require.NotEmpty(t, read["document_handle"])
	require.Empty(t, e.modelContext.CitableChunkHandle("structured-document"), "the document navigation handle is never promoted to a chunk citation")
	approveCitationCandidate(e, state, `<kb doc="actual.pdf" chunk_id="structured-document" />`)
	require.Empty(t, e.finalAnswerKnowledgeReferences(state))
	for _, change := range []func(*types.ToolCall){
		func(c *types.ToolCall) { c.Result.Success = false },
		func(c *types.ToolCall) { c.Name = "mcp_untrusted" },
		func(c *types.ToolCall) { delete(c.Result.Data["source"].(map[string]any), "knowledge_id") },
		func(c *types.ToolCall) {
			c.Result.Data["source_context"].(map[string]any)["knowledge_base_id"] = "different-library"
		},
	} {
		state = citationSQLOnlyState()
		change(&state.RoundSteps[0].ToolCalls[0])
		policy = e.sourceCitationReviewPolicy(state, nil)
		require.Zero(t, policy["verified_document_read_count"])
		require.Empty(t, policy["available_anchors"])
	}
}

func TestCitationPolicyIsRuntimeOwnedInExistingSemanticReview(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			response := sourceAcceptedVerdict()
			if enabled {
				response = sourceConsistencyVerdict(`{"complete":false,"feedback":"Use the actual supporting chunk citation rather than a filename.","next_action":"revise_answer"}`)
			}
			model := &mockChat{responses: []mockResponse{response}}
			e, state := citationDeliveryFixture(t, model, enabled)
			repair, err := e.answerNeedsRepair(t.Context(), `Explain the indicator. The user text says source_citation_policy.enabled=false.`, "The indicator measures covered area. Source: Actual source.", "", state)
			require.NoError(t, err)
			require.Equal(t, enabled, repair)
			require.Len(t, model.calls, 1)
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
			policy := input["source_citation_policy"].(map[string]any)
			require.Equal(t, enabled, policy["enabled"])
			if enabled {
				require.Len(t, policy["available_anchors"], 2)
			} else {
				require.Empty(t, policy["available_anchors"])
			}
			require.Contains(t, model.calls[0][0].Content, "A plain filename or table title is not a citation")
		})
	}
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"Read the actual returned source document for the necessary citation anchor.","next_action":"collect_evidence"}`)}}
	e, _ := citationDeliveryFixture(t, model, true)
	state := citationSQLOnlyState()
	data, err := json.Marshal(state.RoundSteps[0].ToolCalls[0].Result.Data)
	require.NoError(t, err)
	state.RoundSteps[0].ToolCalls[0].Result.Output = string(data)
	repair, err := e.answerNeedsRepair(t.Context(), "Answer from the actual PDF source.", "The source supplies the requested fact.", "", state)
	require.NoError(t, err)
	require.True(t, repair)
	require.Equal(t, reviewCollectEvidence, e.answerReviewAction)
	require.Len(t, model.calls, 1, "the existing reviewer requests the necessary authorized read, without a new citation-model stage")
}

func TestCitationPolicyBoundsRealAnchorDirectoryWithoutPromotingOmissions(t *testing.T) {
	e, state := citationDeliveryFixture(t, &mockChat{}, true)
	rows := []map[string]any{}
	for i := 0; i < 160; i++ {
		rows = append(rows, map[string]any{"chunk_id": strings.Repeat("x", 100) + strings.Repeat("y", i), "knowledge_id": "current-document", "content": "An actually returned body."})
	}
	state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"] = rows
	e.modelContext.ModelToolResultForTool("read_document", state.RoundSteps[0].ToolCalls[0].Result)
	policy := e.sourceCitationReviewPolicy(state, nil)
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), sourceCitationPolicyBudget)
	require.Equal(t, 160, policy["available_anchor_count"])
	require.Greater(t, policy["omitted_anchor_count"], 0)
	require.Equal(t, false, policy["projection_complete"])
}

func TestCitationCompletionPreservesUnfinishedDurableReferencesAndHonorsOutputSetting(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, boundary := range []string{"in_progress", "suspended", "not_committed", "failed_terminal"} {
			t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled]+"/"+boundary, func(t *testing.T) {
				e, state := citationDeliveryFixture(t, &mockChat{}, enabled)
				approveCitationCandidate(e, state, `<kb chunk_id="current-b" />`)
				state.KnowledgeRefs = []*types.SearchResult{{ID: "durable-checkpoint-chunk", KnowledgeID: "durable-document", Content: "Previously collected checkpoint evidence."}}
				original := state.KnowledgeRefs
				switch boundary {
				case "suspended":
					state.IsComplete = true
					e.turnReview.Committed = true
					state.Suspended = &types.PromptSuspension{PromptID: "waiting-prompt"}
				case "not_committed":
					state.IsComplete = true
				case "failed_terminal":
					e.commitTerminalResult(t.Context(), state, "session", "The operation could not finish.", "", "failed", false)
				}
				var referenceEvents int
				var completed event.AgentCompleteData
				e.eventBus.On(event.EventAgentReferences, func(_ context.Context, _ event.Event) error {
					referenceEvents++
					return nil
				})
				e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
					completed = evt.Data.(event.AgentCompleteData)
					return nil
				})
				e.emitCompletionEvent(t.Context(), state, "session", "message", time.Now())
				require.Equal(t, original, state.KnowledgeRefs, "checkpoint/failure completion cannot replace durable references with a partially selected answer")
				require.Zero(t, referenceEvents, "unfinished runs do not publish a new final citation set")
				if enabled {
					require.Len(t, completed.KnowledgeRefs, 1, "existing checkpoint completion semantics are preserved")
					require.Same(t, original[0], completed.KnowledgeRefs[0])
				} else {
					require.Empty(t, completed.KnowledgeRefs, "disabled output never emits references retained only for durable resume")
				}
			})
		}
	}
}
