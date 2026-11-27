package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

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

// Literal source labels can recall a table even when no individual text chunk
// contains every word. The result only gives the model locations to inspect.
func (t *ReadDocumentTool) appendDocumentTableCandidates(ctx context.Context, k *types.Knowledge, pages []int, matchers []*regexp.Regexp, offset, limit int, inventory documentTableInventory, result *types.ToolResult) {
	scope := map[string]any{"offset": offset, "offset_space": "document_table_candidates", "navigation_only": true, "scan_complete": false}
	if len(pages) > 0 {
		scope["pages"] = pages
	}
	result.Data["table_candidate_scope"] = scope
	unavailable := func() {
		scope["status"] = "unavailable"
		scope["notice"] = "The table inventory could not be verified. Actual page and chunk reads remain available."
		appendDocumentTableOutput(result, "table_candidate_scope", scope)
	}
	defer RecoverGoroutine(ctx, "same-document table recall", unavailable)
	locations, err := t.collectDocumentTableCandidates(ctx, k, pages, inventory)
	if err != nil {
		unavailable()
		return
	}
	locations = slices.DeleteFunc(locations, func(item documentTableLocation) bool {
		fields := item.Title + "\n" + item.SectionTitle + "\n" + strings.Join(item.ColumnHeaders, "\n") + "\n" + strings.Join(item.RowLabels, "\n")
		return !slices.ContainsFunc(matchers, func(matcher *regexp.Regexp) bool { return matcher.MatchString(fields) })
	})
	scope["status"], scope["total"], scope["scan_complete"] = "available", len(locations), true
	start := min(offset, len(locations))
	end := min(start+min(limit, documentTableCandidateLimit), len(locations))
	candidates := locations[start:end]
	for i := range candidates {
		candidates[i].ReadArgs = &ReadDocumentInput{ID: k.ID, Page: candidates[i].Page}
	}
	candidates, _ = boundDocumentTableLocations(candidates, OutputBudget(ctx)/3)
	end = start + len(candidates)
	scope["truncated"] = end < len(locations)
	if end < len(locations) {
		if end == start {
			scope["status"] = "output_budget_exhausted"
		} else {
			scope["next_offset"] = end
		}
	}
	if len(candidates) > 0 {
		result.Data["table_candidates"] = candidates
		appendDocumentTableOutput(result, "table_candidates", candidates)
	}
	appendDocumentTableOutput(result, "table_candidate_scope", scope)
}

type documentTableChunkPage struct {
	chunks []*types.Chunk
	total  int64
}

type documentTableInventory map[int]documentTableChunkPage

func (inventory documentTableInventory) remember(page int, chunks []*types.Chunk, total int64) {
	if inventory != nil {
		inventory[page] = documentTableChunkPage{chunks, total}
	}
}

func (t *ReadDocumentTool) collectDocumentTableCandidates(ctx context.Context, k *types.Knowledge, pages []int, recalled documentTableInventory) ([]documentTableLocation, error) {
	var total int64
	var inventory []*types.Chunk
	seen := make(map[string]bool)
	tenantID := t.tenantFor(k)
	for page := 1; ; page++ {
		prior, ok := recalled[page]
		chunks, count := prior.chunks, prior.total
		if !ok {
			var err error
			chunks, count, err = t.listChunks(ctx, k, page, readDocumentScanPageSize)
			if err != nil {
				return nil, err
			}
		}
		expected := min(int64(readDocumentScanPageSize), max(int64(0), count-int64((page-1)*readDocumentScanPageSize)))
		if page > 1 && count != total || int64(len(chunks)) != expected {
			return nil, fmt.Errorf("document inventory changed during table recall")
		}
		total = count
		for _, chunk := range chunks {
			if chunk == nil || chunk.ID == "" || seen[chunk.ID] || !chunk.IsEnabled ||
				chunk.KnowledgeID != k.ID || chunk.KnowledgeBaseID != k.KnowledgeBaseID || chunk.TenantID != tenantID ||
				(chunk.ChunkType != types.ChunkTypeText && chunk.ChunkType != types.ChunkTypeFAQ) {
				return nil, fmt.Errorf("document table recall received an invalid source chunk")
			}
			seen[chunk.ID] = true
			inventory = append(inventory, chunk)
		}
		if int64(page*readDocumentScanPageSize) >= total {
			break
		}
	}
	slices.SortFunc(inventory, func(a, b *types.Chunk) int {
		return cmp.Or(cmp.Compare(a.ChunkIndex, b.ChunkIndex), cmp.Compare(a.ID, b.ID))
	})

	var chunks []documentTableText
	current := 0
	for _, chunk := range inventory {
		if page := chunkMetadataPage(chunk); page > 0 {
			current = page
		}
		physical, next := documentTableChunkPages(chunk, current)
		for _, page := range physical {
			if len(pages) > 0 && !slices.Contains(pages, page) {
				continue
			}
			body, _ := pageText(chunk.Content, current, page)
			if body != "" {
				chunks = append(chunks, documentTableText{page: page, index: chunk.ChunkIndex, id: chunk.ID, text: body})
			}
		}
		current = next
	}

	grids, anchors := documentTableSections(chunks)
	parentPages := make(map[storedDocumentImage][]int)
	parentIDs := make(map[string]bool)
	for anchor := range anchors {
		key := storedDocumentImage{anchor.parent, anchor.ref}
		parentPages[key] = append(parentPages[key], anchor.page)
		parentIDs[key.chunkID] = true
	}
	ids := slices.Sorted(maps.Keys(parentIDs))
	var images []documentTableSource
	for parents := range slices.Chunk(ids, readDocumentScanPageSize) {
		children, err := t.chunkService.GetRepository().ListChunksByParentIDs(ctx, tenantID, parents)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child == nil || !child.IsEnabled || child.ID == "" || child.TenantID != tenantID ||
				child.KnowledgeID != k.ID || child.KnowledgeBaseID != k.KnowledgeBaseID ||
				child.ChunkType != types.ChunkTypeImageOCR || !slices.Contains(parents, child.ParentChunkID) {
				continue
			}
			var info []types.ImageInfo
			if json.Unmarshal([]byte(child.ImageInfo), &info) != nil || len(info) != 1 {
				continue
			}
			ref := info[0].URL
			if ref == "" {
				ref = info[0].OriginalURL
			}
			key := storedDocumentImage{child.ParentChunkID, ref}
			for _, page := range parentPages[key] {
				images = append(images, documentTableSource{
					documentTableText: documentTableText{page: page, id: child.ParentChunkID, text: child.Content},
					ocrID:             child.ID, anchor: documentTableAnchor{page, child.ParentChunkID, ref},
				})
			}
		}
	}
	return documentTableLocations(k, chunks, grids, anchors, images), nil
}
