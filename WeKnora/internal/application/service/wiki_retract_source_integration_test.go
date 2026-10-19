// Integration tests for source projection and commit-boundary validation.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type wikiRetractIntegrationProjectionChat struct {
	t           *testing.T
	sourceQuote string
	pageQuote   string
	afterCall   func(call int)
	calls       [][]chat.Message
}

func (m *wikiRetractIntegrationProjectionChat) Chat(_ context.Context, messages []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.calls = append(m.calls, append([]chat.Message(nil), messages...))
	call := len(m.calls)
	if len(messages) != 2 {
		return nil, fmt.Errorf("unexpected projection message count: %d", len(messages))
	}
	var input struct {
		Windows []wikiRetractProjectionWindow `json:"source_windows"`
	}
	if err := json.Unmarshal([]byte(messages[1].Content), &input); err != nil {
		return nil, err
	}
	selections := map[string][]wikiRetractWindowQuoteSelection{}
	for _, window := range input.Windows {
		quotes := []wikiRetractWindowQuoteSelection{}
		if m.sourceQuote != "" && strings.Contains(window.Content, m.sourceQuote) {
			quotes = append(quotes, wikiRetractProjectionSelection(window, m.sourceQuote, m.pageQuote))
		}
		selections[window.ID] = quotes
	}
	response := wikiRetractProjectionBatchResponse(m.t, input.Windows, selections)
	if m.afterCall != nil {
		m.afterCall(call)
	}
	return response, nil
}

func (*wikiRetractIntegrationProjectionChat) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	return nil, errors.New("streaming is not used by source projection integration tests")
}
func (*wikiRetractIntegrationProjectionChat) GetModelName() string {
	return "wiki-retract-integration-test"
}
func (*wikiRetractIntegrationProjectionChat) GetModelID() string {
	return "wiki-retract-integration-test"
}

type wikiRetractIntegrationChunkRepository struct {
	retractTestChunkRepository
	byID map[string]*types.Chunk
}

func (r wikiRetractIntegrationChunkRepository) ListChunksByID(_ context.Context, _ uint64, ids []string) ([]*types.Chunk, error) {
	out := make([]*types.Chunk, 0, len(ids))
	for _, id := range ids {
		if chunk := r.byID[id]; chunk != nil {
			out = append(out, chunk)
		}
	}
	return out, nil
}

func TestLoadWikiRetractSourceEvidenceWithModelProjectsLongSourceThroughRealWindows(t *testing.T) {
	const sourceQuote = "The field study found that the restored marsh reduced peak flood damage."
	const pageQuote = "Restored wetlands reduced flood risk."
	body := strings.Repeat("Coastal study methods and observations. ", 950) + sourceQuote + strings.Repeat(" Additional site notes. ", 30)
	chunk := retractTestChunk("chunk-live-long", "live", 0, body)
	service := &wikiIngestService{
		knowledgeSvc: retractTestKnowledgeService{rows: map[string]*types.Knowledge{"live": retractTestKnowledge("live")}},
		chunkRepo:    retractTestChunkRepository{rows: map[string][]*types.Chunk{"live": {chunk}}},
	}
	page := &types.WikiPage{
		Slug: "concept/coastal-resilience", Title: "Coastal Resilience",
		Summary: "The page describes wetland flood protection.", Content: "# Coastal resilience\n" + pageQuote,
		SourceRefs: types.StringArray{"live|Coastal study", "removed|Old report"},
		ChunkRefs:  types.StringArray{"chunk-live-long"},
	}
	model := &wikiRetractIntegrationProjectionChat{t: t, sourceQuote: sourceQuote, pageQuote: pageQuote}

	evidence, ids, err := service.loadWikiRetractSourceEvidenceWithModel(
		retractTestCtx(), model, "kb-test", 7, page, map[string]bool{"removed": true},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"live"}, ids)
	require.Len(t, evidence, 1)
	require.Equal(t, "page_chunk_refs", evidence[0].EvidenceScope)
	require.Equal(t, []string{"chunk-live-long"}, evidence[0].ChunkIDs)
	require.NotNil(t, evidence[0].Projection)
	require.True(t, evidence[0].Projection.ScanComplete)
	require.Greater(t, evidence[0].Projection.ReviewedWindows, 1)
	require.Greater(t, len(model.calls), 0)
	require.Contains(t, evidence[0].Content, sourceQuote)
	require.NotContains(t, evidence[0].Content, strings.Repeat("Coastal study methods and observations. ", 20),
		"editor/reviewer projection should receive verified selected quotations, not the full long source")
	require.Contains(t, evidence[0].Projection.Notice, "not a complete copy or proof of semantic absence")
	for _, receipt := range evidence[0].Projection.Receipts {
		require.Equal(t, "live", receipt.SourceID)
		require.Equal(t, "chunk-live-long", receipt.ChunkID)
		require.LessOrEqual(t, receipt.EndRune-receipt.StartRune, wikiRetractProjectionWindowRunes)
		require.Equal(t, wikiRetractTextDigest(body), receipt.ChunkSHA256)
	}
}

func TestLoadWikiRetractSourceEvidenceRejectsChunkMutationDuringWindowReview(t *testing.T) {
	const sourceQuote = "The field study found that the restored marsh reduced peak flood damage."
	const pageQuote = "Restored wetlands reduced flood risk."
	original := strings.Repeat("Coastal study methods and observations. ", 950) + sourceQuote
	chunk := retractTestChunk("chunk-live-long", "live", 0, original)
	repo := retractTestChunkRepository{rows: map[string][]*types.Chunk{"live": {chunk}}}
	service := &wikiIngestService{
		knowledgeSvc: retractTestKnowledgeService{rows: map[string]*types.Knowledge{"live": retractTestKnowledge("live")}},
		chunkRepo:    repo,
	}
	page := &types.WikiPage{
		Slug: "concept/coastal-resilience", Title: "Coastal Resilience", Summary: "Wetland flood protection.",
		Content:    "# Coastal resilience\n" + pageQuote,
		SourceRefs: types.StringArray{"live|Coastal study", "removed|Old report"},
		ChunkRefs:  types.StringArray{"chunk-live-long"},
	}
	model := &wikiRetractIntegrationProjectionChat{t: t, sourceQuote: sourceQuote, pageQuote: pageQuote}
	model.afterCall = func(call int) {
		if call == 1 {
			chunk.Content += " Concurrently appended source revision."
		}
	}

	evidence, sourceIDs, err := service.loadWikiRetractSourceEvidenceWithModel(
		retractTestCtx(), model, "kb-test", 7, page, map[string]bool{"removed": true},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "source snapshot changed")
	require.NotEqual(t, original, chunk.Content, "the live chunk must actually mutate during the projection call")
	require.Empty(t, evidence, "a changed source snapshot must not yield evidence usable by the caller")
	require.Empty(t, sourceIDs, "a changed source snapshot must not yield validated source identities")
	require.NotEmpty(t, model.calls, "the mutation should occur during an actual window review")
}

func TestReduceMixedAdditionAndRetractionIncludesNewChunksInReviewEvidence(t *testing.T) {
	candidate := "SUMMARY: Example page\n# Example\nThe new study reports the added finding."
	reviewPass := `{"schema_version":1,"complete":true,"reviewed_source_ids":["added","live"],"issues":[]}`
	svc, _, model, ctx, batchCtx, updates := newRetractReduceFixture([]string{candidate, reviewPass}, nil)

	addedKnowledge := retractTestKnowledge("added")
	svc.knowledgeSvc = retractTestKnowledgeService{rows: map[string]*types.Knowledge{
		"live": retractTestKnowledge("live"), "added": addedKnowledge,
	}}
	addedChunk := retractTestChunk("chunk-added", "added", 0, "New addition passage: the study reports the added finding.")
	liveChunk := retractTestChunk("chunk-live", "live", 0, "Evidence from the surviving document.")
	chunkRepo := wikiRetractIntegrationChunkRepository{
		retractTestChunkRepository: retractTestChunkRepository{rows: map[string][]*types.Chunk{
			"live": {liveChunk}, "added": {addedChunk},
		}},
		byID: map[string]*types.Chunk{"chunk-live": liveChunk, "chunk-added": addedChunk},
	}
	svc.chunkRepo = chunkRepo
	updates = append(updates, SlugUpdate{
		Slug: "concept/example", Type: string(types.WikiPageTypeConcept), KnowledgeID: "added",
		SourceRef: "added|New research note", DocTitle: "New research note", SourceChunks: []string{"chunk-added"},
		Item: extractedItem{
			Name: "New finding", Slug: "concept/example", Description: "A new research finding.",
			Details: "The new study reports the added finding.", SourceChunks: []string{"chunk-added"},
		},
	})

	changed, _, _, _, err := svc.reduceSlugUpdates(ctx, model, "kb-test", "concept/example", updates, 7, batchCtx, nil)
	require.NoError(t, err)
	require.True(t, changed)
	require.GreaterOrEqual(t, len(model.prompts), 2)
	require.Contains(t, model.prompts[0], "New addition passage") // editor input
	require.Contains(t, model.prompts[1], `"source_id":"added"`)  // independent review dossier
	require.Contains(t, model.prompts[1], "chunk-added")
	require.Contains(t, model.prompts[1], "New addition passage")
}

func TestWikiRetractSnapshotSubsetCannotMasqueradeAsFullDocumentCoverage(t *testing.T) {
	chunkA := retractTestChunk("chunk-a", "live", 0, "First source passage.")
	chunkB := retractTestChunk("chunk-b", "live", 1, "Second source passage.")
	svc := &wikiIngestService{
		knowledgeSvc: retractTestKnowledgeService{rows: map[string]*types.Knowledge{"live": retractTestKnowledge("live")}},
		chunkRepo:    retractTestChunkRepository{rows: map[string][]*types.Chunk{"live": {chunkA, chunkB}}},
	}
	ctx := retractTestCtx()
	digestA := wikiRetractChunkDigest{SourceID: "live", ChunkID: "chunk-a", Index: 0, RuneLen: utf8.RuneCountInString(chunkA.Content), SHA256: wikiRetractTextDigest(chunkA.Content)}

	fullFallback := []wikiRetractSourceEvidence{{
		SourceID: "live", EvidenceScope: "full_document_fallback", ChunkIDs: []string{"chunk-a"},
		Snapshot: []wikiRetractChunkDigest{digestA},
	}}
	err := svc.validateWikiRetractEvidenceSnapshot(ctx, "kb-test", 7, fullFallback)
	require.Error(t, err)
	require.Contains(t, err.Error(), "full source chunk set changed")

	// A page_chunk_refs projection may legitimately be a subset, but it must
	// stay explicitly scoped and must never be promoted to whole-document
	// coverage by the validator or serialized dossier.
	pageScoped := []wikiRetractSourceEvidence{{
		SourceID: "live", EvidenceScope: "page_chunk_refs", ChunkIDs: []string{"chunk-a"},
		Snapshot: []wikiRetractChunkDigest{digestA},
	}}
	require.NoError(t, svc.validateWikiRetractEvidenceSnapshot(ctx, "kb-test", 7, pageScoped))
	encoded, err := json.Marshal(pageScoped)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"evidence_scope":"page_chunk_refs"`)
	require.NotContains(t, string(encoded), `"evidence_scope":"full_document_fallback"`)
	require.False(t, wikiRetractDigestsMatchEvidence([]wikiRetractChunkDigest{digestA, {
		SourceID: "live", ChunkID: "chunk-b", Index: 1, RuneLen: utf8.RuneCountInString(chunkB.Content), SHA256: wikiRetractTextDigest(chunkB.Content),
	}}, pageScoped), "a larger or different live set must not silently validate against a subset snapshot")
}
