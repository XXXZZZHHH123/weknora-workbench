package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

const readDocumentRecallLimit = 5

// recallWithinDocument is navigation recovery, never a coverage decision. The
// original model query is used unchanged, and the normal search pipeline checks
// the reader's server-owned scope and source binding again before retrieval.
func (t *ReadDocumentTool) recallWithinDocument(ctx context.Context, knowledge *types.Knowledge, query string) (recalled []readChunkRow, scope map[string]any) {
	unavailable := func(reason string) ([]readChunkRow, map[string]any) {
		return nil, map[string]any{
			"fallback_status": "unavailable",
			"fallback_notice": fmt.Sprintf("Zero literal matches; same-document semantic recall is unavailable (%s). This does not prove the topic is absent. Use search_knowledge with document_ids or read actual pages/ranges.", reason),
		}
	}
	// Optional recall must not turn a completed literal scan into a failed read
	// when a retrieval adapter is unavailable or panics.
	defer RecoverGoroutine(ctx, "same-document semantic recall", func() {
		recalled, scope = unavailable("retrieval adapter failed")
	})
	if t.documentSearch == nil || t.documentSearch.knowledgeBaseService == nil {
		return unavailable("retrieval service is not configured")
	}
	// Copy configuration rather than changing the separately registered search
	// tool. Its wider scope, image settings or binding must not affect this read.
	search := *t.documentSearch
	search.searchTargets = t.searchTargets
	search.knowledgeService = t.knowledgeService
	search.chunkService = t.chunkService
	search.documentConstraint = t.documentConstraint
	search.wikiPageService = t.wikiPageService
	search.contextImages = false
	search.patternFilter = nil
	args, _ := json.Marshal(SearchKnowledgeInput{
		Query: query, Mode: SearchModeSemantic,
		DocumentIDs: []string{knowledge.ID}, Limit: readDocumentRecallLimit,
	})
	var rows []readChunkRow
	result, err := search.execute(ctx, args, func(results []*searchResultWithMeta) {
		seen := make(map[string]bool)
		for _, result := range results {
			if result == nil || result.SearchResult == nil {
				continue
			}
			chunk := result.SourceChunk
			// Use the actual original record from the search batch, not a
			// reconstructed chunk or a rewritten retrieval presentation. A
			// provider that omits this witness cannot supply normal read evidence.
			if chunk == nil || chunk.ID == "" || seen[chunk.ID] || !chunk.IsEnabled ||
				chunk.ID != result.ID || chunk.KnowledgeID != knowledge.ID || result.KnowledgeID != knowledge.ID ||
				chunk.KnowledgeBaseID != knowledge.KnowledgeBaseID || result.KnowledgeBaseID != knowledge.KnowledgeBaseID ||
				chunk.TenantID != t.tenantFor(knowledge) || chunk.ContentRevision != result.ContentRevision ||
				(chunk.ChunkType != types.ChunkTypeText && chunk.ChunkType != types.ChunkTypeFAQ) {
				continue
			}
			seen[chunk.ID] = true
			rows = append(rows, readChunkRow{chunk: chunk, role: "semantic_recall"})
			if len(rows) == readDocumentRecallLimit {
				break
			}
		}
	})
	if err != nil || result == nil || !result.Success {
		return unavailable("retrieval failed or its scope could not be verified")
	}
	if mode, _ := result.Data["mode"].(string); mode != SearchModeSemantic {
		return unavailable("the document has no usable semantic index")
	}
	if len(rows) == 0 {
		if count, _ := result.Data["count"].(int); count > 0 {
			return unavailable("retrieval returned no verified original chunk records")
		}
		return nil, map[string]any{
			"fallback": "semantic_within_document", "fallback_status": "no_results", "fallback_count": 0,
			"fallback_notice": "Zero literal matches; same-document semantic recall returned no verified chunks. Neither result proves that the topic is absent. Read actual pages/ranges if needed.",
		}
	}
	// Preserve the normal read output and character-continuation limits. Only
	// returned body segments become evidence; the whole scan is not a full read.
	rows = boundChunkRowsContent(ctx, rows)
	rows, _ = fitChunkRowsToBudget(ctx, rows, rows[0].chunk.ID)
	return rows, map[string]any{
		"fallback": "semantic_within_document", "fallback_status": "recalled", "fallback_count": len(rows),
		"fallback_notice": "Zero literal matches. Returned semantic neighbours use the unchanged query within this document only; they are actual read chunks, not literal matches or proof of topic/document coverage. Inspect their text and continue with real pages or chunk handles as needed.",
	}
}
