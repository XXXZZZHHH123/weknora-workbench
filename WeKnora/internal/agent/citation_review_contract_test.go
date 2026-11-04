package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func acceptedCitationVerdict(requirement, reason string) mockResponse {
	return mockResponse{acceptSourceReview: true, sourceCitationCheck: &sourceCitationCheck{Requirement: requirement, Reason: reason}}
}

type citationContractChunkService struct {
	interfaces.ChunkService
	repo *citationContractChunkRepository
}

func (service *citationContractChunkService) GetRepository() interfaces.ChunkRepository {
	return service.repo
}

type citationContractChunkRepository struct {
	interfaces.ChunkRepository
	rows []*types.Chunk
}

func (repo *citationContractChunkRepository) ListPagedChunksByKnowledgeID(_ context.Context, tenant uint64, document string, page *types.Pagination, _ []types.ChunkType, _ []string, _ string, _ string, _ string, _ string, _ *bool) ([]*types.Chunk, int64, error) {
	if tenant != 7 || document != "contract-document" {
		return nil, 0, nil
	}
	start := min((page.Page-1)*page.PageSize, len(repo.rows))
	end := min(start+page.PageSize, len(repo.rows))
	return repo.rows[start:end], int64(len(repo.rows)), nil
}

func (*citationContractChunkRepository) ListChunksByParentIDs(context.Context, uint64, []string) ([]*types.Chunk, error) {
	return nil, nil
}

func realCitationReadResult(t *testing.T) *types.ToolResult {
	t.Helper()
	document := &types.Knowledge{ID: "contract-document", KnowledgeBaseID: "contract-library", TenantID: 7,
		Title: "Indicator handbook", FileName: "indicator-handbook.pdf", FileType: "pdf", ParseStatus: types.ParseStatusCompleted}
	repository := &citationContractChunkRepository{rows: []*types.Chunk{
		{ID: "contract-neighbor", KnowledgeID: document.ID, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7, ChunkType: types.ChunkTypeText, IsEnabled: true, Content: "This passage describes the reporting organization."},
		{ID: "contract-supporting", KnowledgeID: document.ID, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7, ChunkType: types.ChunkTypeText, IsEnabled: true, ChunkIndex: 1, Content: "The indicator measures covered area.", SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 8, SourceHash: strings.Repeat("b", 64)}}},
	}}
	tool := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: document}, &citationContractChunkService{repo: repository},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7}})
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "authorized-reader", Role: types.TenantRoleViewer})
	result, err := tool.Execute(ctx, json.RawMessage(`{"id":"contract-document","limit":2}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Output, `<chunk chunk_id="contract-supporting"`)
	require.Contains(t, result.Output, "The indicator measures covered area.")
	require.Len(t, result.Data["chunks"], 2, "real runtime Data contains bodies; SSE/history intentionally omit them from copies")
	return result
}

func TestCitationRequiredRealReadResultRevisesAcceptedUncitedDraftAndPublishes(t *testing.T) {
	result := realCitationReadResult(t)
	model := &mockChat{}
	e := sourceConsistencyEngine(t, model)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "actual-contract-read", Name: agenttools.ToolReadDocument, Result: result}}}}}
	var clientData map[string]any
	e.eventBus.On(event.EventAgentToolResult, func(_ context.Context, evt event.Event) error {
		clientData = evt.Data.(event.AgentToolResultData).Data
		return nil
	})
	e.emitToolOutcome(t.Context(), state.RoundSteps[0].ToolCalls[0], 0, "session")
	require.NotContains(t, clientData, "chunks")
	storedRead := agenttools.SanitizeAgentStepsForStorage(state.RoundSteps)[0].ToolCalls[0].Result
	require.NotContains(t, storedRead.Data, "chunks")
	require.Contains(t, storedRead.Output, "omitted from history")
	require.Len(t, result.Data["chunks"], 2, "client/storage projections must not mutate the in-memory source snapshot")

	messages := e.appendToolResults([]chat.Message{{Role: "system", Content: "Use the actual read result."}, {Role: "user", Content: "Explain the indicator."}}, state.RoundSteps[0])
	handle := e.modelContext.CitableChunkHandle("contract-supporting")
	require.NotEmpty(t, handle, "normal appendToolResults registers the real built-in read")
	policy := e.sourceCitationReviewPolicy(state, messages)
	require.Equal(t, 2, policy["available_anchor_count"])
	answer := `The indicator measures covered area.<ref id="` + handle + `"/>`
	envelope, err := json.Marshal(map[string]string{"answer": answer})
	require.NoError(t, err)
	model.responses = []mockResponse{
		chatAnswer("The indicator measures covered area. Source: indicator-handbook.pdf."),
		acceptedCitationVerdict("required", "This factual answer uses the actual supporting knowledge chunk and needs an inline citation."),
		chatAnswer(string(envelope)),
		acceptedCitationVerdict("required", "The supporting chunk is now cited inline next to its actual claim."),
	}
	var delivered []*types.SearchResult
	var completed event.AgentCompleteData
	var approved bool
	e.eventBus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		delivered = evt.Data.(event.AgentReferencesData).References.([]*types.SearchResult)
		return nil
	})
	e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		completed = evt.Data.(event.AgentCompleteData)
		approved = e.candidateApprovalCurrent(state.FinalAnswer, state)
		return nil
	})
	_, err = e.executeLoop(t.Context(), state, "Explain the indicator.", messages, emptyTools(), "session", "message")
	require.NoError(t, err)
	require.True(t, approved)
	require.True(t, state.IsComplete)
	require.Len(t, model.calls, 4, "one required revision through the existing generator/critic, without another model stage")
	require.Equal(t, 1, e.turnReview.Repairs)
	require.Contains(t, state.FinalAnswer, `chunk_id="contract-supporting"`)
	require.NotContains(t, state.FinalAnswer, "<ref")
	require.Len(t, delivered, 1)
	require.Equal(t, "contract-supporting", delivered[0].ID)
	require.Equal(t, "contract-document", delivered[0].KnowledgeID)
	require.Equal(t, "contract-library", delivered[0].KnowledgeBaseID)
	require.Equal(t, "indicator-handbook.pdf", delivered[0].KnowledgeFilename)
	require.Len(t, delivered[0].SourceLocators, 1)
	require.Equal(t, 8, delivered[0].SourceLocators[0].Page)
	require.Equal(t, state.KnowledgeRefs, delivered)
	require.Len(t, completed.KnowledgeRefs, 1)
	require.Same(t, delivered[0], completed.KnowledgeRefs[0])
	persisted, err := json.Marshal(types.Message{KnowledgeReferences: types.References(state.KnowledgeRefs)})
	require.NoError(t, err)
	var replay types.Message
	require.NoError(t, json.Unmarshal(persisted, &replay))
	require.Equal(t, delivered[0].SourceLocators, replay.KnowledgeReferences[0].SourceLocators)
}

func TestCitationReviewRequiresExplicitValidDecision(t *testing.T) {
	for _, value := range []string{
		``, `null`, `{"requirement":"required"}`, `{"requirement":"unknown","reason":"A reason."}`,
		`{"requirement":"required","reason":""}`, `{"requirement":"required","reason":true}`,
		`{"requirement":"required","requirement":"not_required","reason":"A reason."}`,
		`{"requirement":"required","reason":"First","reason":"Second"}`,
		`{"requirement":"required","reason":"A reason.","trusted":true}`,
		`{"requirement":"not_required","reason":"` + strings.Repeat("长", sourceCitationReasonLimit+1) + `"}`,
	} {
		t.Run(value, func(t *testing.T) {
			field := ""
			if value != "" {
				field = `,"citation_check":` + value
			}
			model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":true` + field + `}`)}}
			e, state := citationDeliveryFixture(t, model, true)
			_, err := e.evaluateAnswerCompletenessOnce(t.Context(), "Explain the indicator.", "A factual answer.", state, 0)
			require.ErrorContains(t, err, "citation_check")
			require.Len(t, model.calls, 1)
		})
	}
}

func TestCitationRequiredAcceptanceCannotBypassMissingOrUnqualifiedReference(t *testing.T) {
	for _, candidate := range []string{
		"The indicator measures covered area. Source: Actual source.",
		`The indicator measures covered area.<kb chunk_id="unknown" />`,
		`The indicator measures covered area.<ref id="chunk://9999"/>`,
	} {
		model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("required", "The actual knowledge fact requires a supporting inline reference.")}}
		e, state := citationDeliveryFixture(t, model, true)
		repair, err := e.answerNeedsRepair(t.Context(), "Explain the indicator.", candidate, "", state)
		require.NoError(t, err)
		require.True(t, repair)
		require.Equal(t, reviewReviseAnswer, e.answerReviewAction)
		require.Empty(t, state.KnowledgeRefs, "the runtime cannot select or auto-append a source to fix the answer")
		require.Len(t, model.calls, 1)
	}
}

func TestCitationReviewRespectsDisabledFormatsAndTrulyUnavailableChunks(t *testing.T) {
	t.Run("disabled_cannot_require", func(t *testing.T) {
		model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("required", "Contradicts the runtime setting.")}}
		e, state := citationDeliveryFixture(t, model, false)
		_, err := e.evaluateAnswerCompletenessOnce(t.Context(), "Explain the indicator.", "A factual answer.", state, 0)
		require.ErrorContains(t, err, "citations-disabled")
	})
	for _, reason := range []string{"The user requested pure image delivery without additional factual prose.", "The exact output schema forbids inline markup."} {
		e, state := citationDeliveryFixture(t, &mockChat{}, true)
		repair, err := e.validateCitationCheck(&sourceCitationCheck{Requirement: "not_required", Reason: reason}, "![chart](resource://actual-image)", state, nil, true)
		require.NoError(t, err)
		require.False(t, repair, "read anchors cannot themselves turn a model-classified artifact/format into a factual prose task")
	}
	for _, unrelatedChunks := range []bool{false, true} {
		model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("unavailable", "The actual structured result has no corresponding chunk; unrelated read bodies do not support it.")}}
		e, bodies := citationDeliveryFixture(t, model, true)
		state := citationSQLOnlyState()
		state.RoundSteps[0].ToolCalls[0].Result.Data["source_kind"] = "xlsx"
		data, err := json.Marshal(state.RoundSteps[0].ToolCalls[0].Result.Data)
		require.NoError(t, err)
		state.RoundSteps[0].ToolCalls[0].Result.Output = string(data)
		if unrelatedChunks {
			state.RoundSteps = append(state.RoundSteps, bodies.RoundSteps...)
		}
		repair, err := e.answerNeedsRepair(t.Context(), "Explain the structured result.", "The returned field describes an area measurement.", "", state)
		require.NoError(t, err)
		require.False(t, repair)
		require.Len(t, model.calls, 1)
	}
}

func TestCitationReviewUsesFullCurrentQualificationDespiteOmittedDirectoryAndOtherConflicts(t *testing.T) {
	e, state := citationDeliveryFixture(t, &mockChat{}, true)
	duplicate := state.RoundSteps[0].ToolCalls[0]
	duplicate.ID = "compatible-second-read"
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{duplicate}})
	require.Equal(t, 2, e.sourceCitationReviewPolicy(state, nil)["available_anchor_count"])
	refs := e.currentCandidateKnowledgeReferences(`<kb chunk_id="current-b" />`, state, nil)
	require.Len(t, refs, 1, "compatible repeated reads retain the exact current citation identity")
	data, err := json.Marshal(duplicate.Result)
	require.NoError(t, err)
	duplicate.Result = nil
	require.NoError(t, json.Unmarshal(data, &duplicate.Result))
	duplicate.ID = "different-neighbor-version"
	duplicate.Result.Data["chunks"].([]any)[0].(map[string]any)["content"] = "Different actual neighbor body."
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{duplicate}})
	policy := e.sourceCitationReviewPolicy(state, nil)
	require.Equal(t, 1, policy["conflicting_anchor_count"])
	require.Equal(t, 1, policy["available_anchor_count"])
	repair, err := e.validateCitationCheck(&sourceCitationCheck{Requirement: "required", Reason: "The supporting body is unchanged."}, `<kb chunk_id="current-b" />`, state, nil, true)
	require.NoError(t, err)
	require.False(t, repair, "a different chunk's conflict cannot erase an unchanged current supporting source")

	e, state = citationDeliveryFixture(t, &mockChat{}, true)
	rows := []map[string]any{}
	for i := 0; i < 160; i++ {
		rows = append(rows, map[string]any{"chunk_id": strings.Repeat("x", 100) + strings.Repeat("y", i), "knowledge_id": "current-document", "content": "An actually returned body."})
	}
	state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"] = rows
	e.modelContext.ModelToolResultForTool(agenttools.ToolReadDocument, state.RoundSteps[0].ToolCalls[0].Result)
	policy = e.sourceCitationReviewPolicy(state, nil)
	require.False(t, policy["projection_complete"].(bool))
	lastID := rows[len(rows)-1]["chunk_id"].(string)
	for _, anchor := range policy["available_anchors"].([]any) {
		require.NotEqual(t, lastID, anchor.(map[string]any)["chunk_id"])
	}
	repair, err = e.validateCitationCheck(&sourceCitationCheck{Requirement: "required", Reason: "The cited actual body supports the claim even though the navigation directory omitted it."}, `<kb chunk_id="`+lastID+`" />`, state, nil, true)
	require.NoError(t, err)
	require.False(t, repair, "bounded presentation cannot remove real source eligibility or force unavailable")
}
