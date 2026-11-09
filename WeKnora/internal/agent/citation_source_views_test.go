package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const citationViewOriginalBody = "<!-- PDF page 6 -->\nEarlier 🐢.\n<!-- PDF page 7 -->\n面积🙂用量。\n![Area sketch](resource://AbCdEfGhIjKlMnOpQrStUv)\n<!-- PDF page 7 -->\n第二段 é.\n<!-- PDF page 8 -->\nOther page.\n![Other sketch](resource://ZyXwVuTsRqPoNmLkJiHgFe)\n"

func realCitationViewResults(t *testing.T) (*types.ToolResult, *types.ToolResult, *types.ToolResult) {
	t.Helper()
	document := &types.Knowledge{ID: "contract-document", KnowledgeBaseID: "contract-library", TenantID: 7,
		Title: "Measurement handbook", FileName: "measurement-handbook.pdf", FileType: "pdf", ParseStatus: types.ParseStatusCompleted}
	repo := &citationContractChunkRepository{rows: []*types.Chunk{{ID: "view-shared", KnowledgeID: document.ID,
		KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7, ChunkType: types.ChunkTypeText, IsEnabled: true,
		Content: citationViewOriginalBody, ContentRevision: 4,
		SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 7, SourceHash: strings.Repeat("c", 64)}, {Type: types.SourceLocatorPDF, Page: 8, SourceHash: strings.Repeat("c", 64)}}}}}
	tool := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: document}, &citationContractChunkService{repo: repo},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7}})
	tool.WithImageReader(func(context.Context, *types.Knowledge, string) ([]byte, error) {
		var pixels bytes.Buffer
		err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 3, 3)))
		return pixels.Bytes(), err
	})
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "authorized-reader", Role: types.TenantRoleViewer})
	read := func(input string) *types.ToolResult {
		result, err := tool.Execute(ctx, json.RawMessage(input))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		return result
	}
	return read(`{"id":"contract-document","query":"面积"}`),
		read(`{"id":"contract-document","page":7,"images":["resource://AbCdEfGhIjKlMnOpQrStUv"]}`), read(`{"id":"contract-document","page":8,"images":["resource://ZyXwVuTsRqPoNmLkJiHgFe"]}`)
}

func citationViewState(results ...*types.ToolResult) *types.AgentState {
	state := &types.AgentState{}
	for index, result := range results {
		state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "actual-view-read-" + strings.Repeat("x", index+1), Name: agenttools.ToolReadDocument, Result: result}}})
	}
	return state
}

func TestCitationRealPageViewsUseFullCurrentCanonicalBodyThroughNormalFinish(t *testing.T) {
	for _, pageFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "full_then_page", true: "page_then_full"}[pageFirst], func(t *testing.T) {
			full, page, _ := realCitationViewResults(t)
			state := citationViewState(full, page)
			if pageFirst {
				state = citationViewState(page, full)
			}
			before := reviewDigest(state.RoundSteps)
			model := &mockChat{}
			e := sourceConsistencyEngine(t, model)
			e.taskPlan = explicitTextDeliveryPlan()
			e.taskPlan.ReasoningDepth = "synthesis"
			var client map[string]any
			e.eventBus.On(event.EventAgentToolResult, func(_ context.Context, evt event.Event) error {
				client = evt.Data.(event.AgentToolResultData).Data
				return nil
			})
			messages := []chat.Message{{Role: "system", Content: "Use the actual current source."}, {Role: "user", Content: "Explain the measurement on the requested page."}}
			for _, step := range state.RoundSteps {
				e.emitToolOutcome(t.Context(), step.ToolCalls[0], 0, "session")
				require.NotContains(t, client, "chunks")
				messages = e.appendToolResults(messages, step)
			}
			stored := agenttools.SanitizeAgentStepsForStorage(state.RoundSteps)
			for _, step := range stored {
				require.NotContains(t, step.ToolCalls[0].Result.Data, "chunks")
			}
			require.Equal(t, before, reviewDigest(state.RoundSteps), "registration and client/history projections cannot change actual body/ranges")
			handle := e.modelContext.CitableChunkHandle("view-shared")
			require.NotEmpty(t, handle)
			answer := `面积🙂用量。<ref id="` + handle + `"/>`
			model.responses = []mockResponse{chatAnswer(answer), acceptedCitationVerdict("required", "The current full source and its exact page presentation support this adjacent claim.")}
			policy := e.sourceCitationReviewPolicy(state, messages)
			require.Equal(t, 0, policy["conflicting_anchor_count"])
			require.Equal(t, 1, policy["available_anchor_count"])
			var completed event.AgentCompleteData
			approved := false
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				completed = evt.Data.(event.AgentCompleteData)
				approved = e.candidateApprovalCurrent(state.FinalAnswer, state)
				return nil
			})
			_, err := e.executeLoop(t.Context(), state, "Explain the measurement on the requested page.", messages, emptyTools(), "session", "message")
			require.NoError(t, err)
			require.True(t, approved, "the real completion event publishes only the current approved canonical answer")
			// executeLoop releases its semantic conversation after Complete.
			// Check approval at that actual delivery boundary, and separately
			// prove neither the normalized answer nor source snapshot drifted.
			require.Equal(t, e.turnReview.Last.CandidateHash, candidateTextDigest(state.FinalAnswer))
			require.Equal(t, e.turnReview.Last.EvidenceHash, reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation)))
			require.Len(t, model.calls, 2, "the existing semantic critic still judges the adjacent claim")
			require.Len(t, state.KnowledgeRefs, 1)
			ref := state.KnowledgeRefs[0]
			require.Equal(t, "view-shared", ref.ID)
			require.Equal(t, citationViewOriginalBody, ref.Content)
			require.Equal(t, 4, ref.ContentRevision)
			require.False(t, ref.ContentRewritten)
			require.Len(t, ref.SourceLocators, 2)
			require.Len(t, completed.KnowledgeRefs, 1)
			require.Same(t, ref, completed.KnowledgeRefs[0])
			input := candidateReviewInput(t, model, 1, "candidate_answer")
			evidence := input["citation_source_evidence"].(map[string]any)
			items := evidence["items"].([]any)
			require.Len(t, items, 1)
			item := items[0].(map[string]any)
			require.Equal(t, true, item["presentation_ranges_verified_against_full_body"])
			require.Equal(t, citationViewOriginalBody, e.modelContext.DecodeOutputText(item["content"].(string)), "model resource handles round-trip to the exact verified source body")
			observations := item["additional_source_observations"].([]any)
			require.NotEmpty(t, observations)
			metadata := observations[0].(map[string]any)["chunk_metadata"].(map[string]any)
			require.EqualValues(t, 7, metadata["page"])
			require.Contains(t, metadata, "content_source_view")
			require.Contains(t, observations[0], "source_pointer_ref", "the page observation retains its own immutable reader pointer")
			imageViews := input["document_image_observations"].([]any)
			require.Len(t, imageViews, 1)
			imageView := imageViews[0].(map[string]any)["content"].(map[string]any)
			require.Equal(t, "view-shared", imageView["chunk_id"])
			require.EqualValues(t, 7, imageView["page"])
			require.Equal(t, true, imageView["original_pixels_verified"])
			require.Equal(t, "resource://AbCdEfGhIjKlMnOpQrStUv", e.modelContext.DecodeOutputText(imageView["reference"].(string)))
			replayBytes, err := json.Marshal(types.Message{KnowledgeReferences: types.References(state.KnowledgeRefs)})
			require.NoError(t, err)
			var replay types.Message
			require.NoError(t, json.Unmarshal(replayBytes, &replay))
			require.Equal(t, ref.Content, replay.KnowledgeReferences[0].Content)
			require.Equal(t, ref.SourceLocators, replay.KnowledgeReferences[0].SourceLocators)
		})
	}
}

func TestCitationPageViewProofCannotMergeConflictingOrUnwitnessedSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong-body", func(row map[string]any) { row["content"] = "A different actual body." }},
		{"wrong-hash", func(row map[string]any) {
			row["content_source_view"].(map[string]any)["origin_sha256"] = strings.Repeat("0", 64)
		}},
		{"wrong-origin-revision", func(row map[string]any) { row["content_source_view"].(map[string]any)["origin_revision"] = 5 }},
		{"wrong-source-revision", func(row map[string]any) { row["content_revision"] = 5 }},
		{"wrong-locators", func(row map[string]any) { row["source_locators"].([]any)[0].(map[string]any)["page"] = 9 }},
		{"wrong-library", func(row map[string]any) { row["knowledge_base"] = "another-library" }},
		{"wrong-source-start", func(row map[string]any) { row["start_at"] = 99 }},
		{"wrong-source-end", func(row map[string]any) { row["end_at"] = 99 }},
		{"wrong-chunk-type", func(row map[string]any) { row["chunk_type"] = "image_ocr" }},
		{"wrong-parent", func(row map[string]any) { row["parent_chunk_id"] = "different-parent" }},
		{"wrong-chunk-index", func(row map[string]any) { row["chunk_index"] = 99 }},
		{"wrong-origin-length", func(row map[string]any) { row["content_source_view"].(map[string]any)["origin_rune_count"] = 999 }},
		{"wrong-range", func(row map[string]any) {
			row["content_source_view"].(map[string]any)["ranges"].([]any)[0].(map[string]any)["start"] = 0
		}},
		{"overlapping-ranges", func(row map[string]any) {
			row["content_source_view"].(map[string]any)["ranges"].([]any)[1].(map[string]any)["start"] = 0
		}},
		{"negative-range", func(row map[string]any) {
			row["content_source_view"].(map[string]any)["ranges"].([]any)[0].(map[string]any)["start"] = -1
		}},
		{"out-of-bounds-range", func(row map[string]any) {
			row["content_source_view"].(map[string]any)["ranges"].([]any)[0].(map[string]any)["end"] = 9999
		}},
		{"rewrite", func(row map[string]any) { row["content_rewritten"] = true }},
		{"unknown-proof-fields", func(row map[string]any) { row["content_source_view"].(map[string]any)["trusted"] = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			full, page, _ := realCitationViewResults(t)
			encoded, err := json.Marshal(page)
			require.NoError(t, err)
			var changed *types.ToolResult
			require.NoError(t, json.Unmarshal(encoded, &changed))
			row := changed.Data["chunks"].([]any)[0].(map[string]any)
			test.change(row)
			state := citationViewState(full, changed)
			e := sourceConsistencyEngine(t, &mockChat{})
			for _, step := range state.RoundSteps {
				e.appendToolResults(emptyMessages(), step)
			}
			messages := e.sourceConsistencyMessages(state, nil)
			_, conflicts := citationChunksFromMessages(messages)
			require.True(t, conflicts["view-shared"])
			require.Empty(t, e.currentCandidateKnowledgeReferences(`<kb chunk_id="view-shared" />`, state, nil))
		})
	}
	full, page, otherPage := realCitationViewResults(t)
	for _, results := range [][]*types.ToolResult{{page, otherPage}, {page, otherPage, page}} {
		e := sourceConsistencyEngine(t, &mockChat{})
		state := citationViewState(results...)
		for _, step := range state.RoundSteps {
			e.appendToolResults(emptyMessages(), step)
		}
		_, conflicts := citationChunksFromMessages(e.sourceConsistencyMessages(state, nil))
		require.True(t, conflicts["view-shared"], "self-reported equal origin hashes cannot merge two partial views without a full current witness")
		require.Empty(t, e.currentCandidateKnowledgeReferences(`<kb chunk_id="view-shared" />`, state, nil))
	}
	// The same observed literal value does not erase a real source version change.
	page.Data["chunks"].([]map[string]any)[0]["content_revision"] = 5
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(full, page)
	for _, step := range state.RoundSteps {
		e.appendToolResults(emptyMessages(), step)
	}
	require.Empty(t, e.currentCandidateKnowledgeReferences(`<kb chunk_id="view-shared" />`, state, nil))
}

func TestCitationVerifiedPageViewDoesNotTransferImageSupportAcrossChunkOrPage(t *testing.T) {
	for _, field := range []string{"chunk_id", "page"} {
		t.Run(field, func(t *testing.T) {
			full, page, _ := realCitationViewResults(t)
			encoded, err := json.Marshal(page)
			require.NoError(t, err)
			var changed *types.ToolResult
			require.NoError(t, json.Unmarshal(encoded, &changed))
			read := changed.Data["image_evidence"].([]any)[0].(map[string]any)
			if field == "chunk_id" {
				read[field] = "another-chunk"
			} else {
				read[field] = 8
			}
			state := citationViewState(full, changed)
			e := sourceConsistencyEngine(t, &mockChat{})
			for _, step := range state.RoundSteps {
				e.appendToolResults(emptyMessages(), step)
			}
			require.Len(t, e.currentCandidateKnowledgeReferences(`<kb chunk_id="view-shared" />`, state, nil), 1, "body identity qualification cannot stand in for an image-support decision")
			views, _ := documentImageConsistencySummariesWithSources(state, e.sourceConsistencyMessages(state, nil))
			require.Empty(t, views, "a same-body proof must not authorize another chunk or page's returned image")
		})
	}
}

type citationFullReadLookup struct{ *citationContractChunkService }

func (service *citationFullReadLookup) GetChunkByIDOnly(_ context.Context, id string) (*types.Chunk, error) {
	for _, chunk := range service.repo.rows {
		if chunk.ID == id {
			return chunk, nil
		}
	}
	return nil, fmt.Errorf("chunk not found")
}

type citationFullReadIndex struct {
	*sourceDiscoveryKnowledgeService
	chunk *types.Chunk
}

func (index *citationFullReadIndex) HybridSearch(context.Context, string, types.SearchParams) ([]*types.SearchResult, error) {
	return []*types.SearchResult{{ID: index.chunk.ID, KnowledgeID: index.chunk.KnowledgeID,
		KnowledgeBaseID: index.chunk.KnowledgeBaseID, ChunkType: index.chunk.ChunkType, Content: index.chunk.Content,
		ContentRevision: index.chunk.ContentRevision, Score: 0.99}}, nil
}

func TestCitationRealSearchThenUncutReadKeepsCanonicalReference(t *testing.T) {
	for _, mode := range []string{"full_offset", "full_physical_page"} {
		t.Run(mode, func(t *testing.T) {
			document := &types.Knowledge{ID: "contract-document", KnowledgeBaseID: "contract-library", TenantID: 7,
				Title: "Measurement notes", FileName: "measurement-notes.txt", FileType: "txt", ParseStatus: types.ParseStatusCompleted}
			chunk := &types.Chunk{ID: "plain-current-chunk", KnowledgeID: document.ID, KnowledgeBaseID: document.KnowledgeBaseID,
				TenantID: 7, ChunkType: types.ChunkTypeText, IsEnabled: true, Content: "# Page 7\nThe measurement describes area.\n# Page 7\nA second original line."}
			repository := &citationContractChunkRepository{rows: []*types.Chunk{chunk}}
			chunks := &citationFullReadLookup{&citationContractChunkService{repo: repository}}
			knowledge := &plannedDocumentKnowledge{document: document}
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7}}
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "authorized-reader", Role: types.TenantRoleViewer})
			search := agenttools.NewSearchKnowledgeTool(&citationFullReadIndex{sourceDiscoveryKnowledgeService: &sourceDiscoveryKnowledgeService{}, chunk: chunk}, knowledge, nil, targets, nil, nil)
			searched, err := search.Execute(ctx, json.RawMessage(`{"query":"measurement","mode":"keyword"}`))
			require.NoError(t, err)
			require.True(t, searched.Success, searched.Error)
			searchRow := searched.Data["results"].([]map[string]any)[0]
			require.NotContains(t, searchRow, "content_revision", "exercise the real search format without inventing a missing version")
			require.NotContains(t, searchRow, "source_locators")
			model := &mockChat{}
			e := sourceConsistencyEngine(t, model)
			e.taskPlan = explicitTextDeliveryPlan()
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "actual-plain-search", Name: agenttools.ToolSearchKnowledge, Result: searched}}}}}
			messages := e.appendToolResults([]chat.Message{{Role: "user", Content: "Explain the measurement."}}, state.RoundSteps[0])
			handle := e.modelContext.CitableChunkHandle(chunk.ID)
			require.NotEmpty(t, handle)
			readArgs := map[string]any{"id": handle, "content_offset": 0, "content_limit": 12000}
			if mode == "full_physical_page" {
				readArgs = map[string]any{"id": handle, "page": 7}
			}
			args, err := json.Marshal(readArgs)
			require.NoError(t, err)
			calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: string(args)}}}
			e.modelContext.DecodeToolCalls(calls)
			read := agenttools.NewReadDocumentTool(knowledge, chunks, targets)
			fetched, err := read.Execute(ctx, json.RawMessage(calls[0].Function.Arguments))
			require.NoError(t, err)
			require.True(t, fetched.Success, fetched.Error)
			readRow := fetched.Data["chunks"].([]map[string]any)[0]
			require.Equal(t, chunk.ID, readRow["chunk_id"])
			require.Equal(t, chunk.Content, readRow["content"])
			require.NotContains(t, readRow, "content_source_view", "an uncut full read needs no partial-view proof or new version reconciliation")
			state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "actual-plain-full-read", Name: agenttools.ToolReadDocument, Result: fetched}}})
			messages = e.appendToolResults(messages, state.RoundSteps[1])
			candidate := e.modelContext.DecodeOutputText(`The measurement describes area.<ref id="` + handle + `"/>`)
			require.Len(t, e.currentCandidateKnowledgeReferences(candidate, state, messages), 1)
			model.responses = []mockResponse{chatAnswer(`The measurement describes area.<ref id="` + handle + `"/>`), sourceAcceptedVerdict()}
			var complete event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				complete = evt.Data.(event.AgentCompleteData)
				require.True(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
				return nil
			})
			_, err = e.executeLoop(ctx, state, "Explain the measurement.", messages, emptyTools(), "session", "message")
			require.NoError(t, err)
			require.Len(t, model.calls, 2, "the source-bound answer receives the same final semantic coverage review")
			require.Len(t, complete.KnowledgeRefs, 1)
			require.Len(t, state.KnowledgeRefs, 1)
			require.Equal(t, chunk.ID, state.KnowledgeRefs[0].ID)
			require.Equal(t, chunk.Content, state.KnowledgeRefs[0].Content)
			readRow["content_revision"] = 1
			require.Empty(t, e.currentCandidateKnowledgeReferences(candidate, state, nil), "a true version change remains a conflict; missing search metadata was never promoted to a verified origin version")
		})
	}
}
