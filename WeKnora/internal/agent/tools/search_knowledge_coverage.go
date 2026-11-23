package tools

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
)

type searchOrigin struct {
	*types.SearchResult
	navigationOnly bool
}

type searchOriginIdentity struct {
	kb, document, chunk string
	revision            int
}

func searchOriginID(result *types.SearchResult) searchOriginIdentity {
	return searchOriginIdentity{result.KnowledgeBaseID, result.KnowledgeID, result.ID, result.ContentRevision}
}

// filterScopedSearchRows verifies each returned identity before passing any
// passage or its aliases to the reranker or model. Querying the same document's
// metadata once suffices to check the file revision for each physical location.
func (t *SearchKnowledgeTool) filterScopedSearchRows(ctx context.Context, targets types.SearchTargets, rows []*searchResultWithMeta) []*searchResultWithMeta {
	type documentKey struct{ kb, id string }
	permissions := make(map[documentKey]bool)
	documents := make(map[documentKey]*types.Knowledge)
	filtered := rows[:0]
	for _, row := range rows {
		key := documentKey{row.KnowledgeBaseID, row.KnowledgeID}
		allowed, checked := permissions[key]
		if !checked {
			allowed, _ = searchTargetsAllowKnowledgeID(ctx, targets, key.id, key.kb, t.knowledgeService)
			permissions[key] = allowed
		}
		if !allowed {
			continue
		}
		if chunk := row.SourceChunk; chunk != nil &&
			(!chunk.IsEnabled || chunk.ID != row.ID || chunk.KnowledgeID != row.KnowledgeID ||
				chunk.KnowledgeBaseID != row.KnowledgeBaseID || chunk.ContentRevision != row.ContentRevision ||
				(targets.GetTenantIDForKB(key.kb) != 0 && chunk.TenantID != targets.GetTenantIDForKB(key.kb))) {
			continue
		}
		fresh := true
		for _, locator := range row.SourceLocators {
			if locator.SourceHash == "" || t.knowledgeService == nil {
				continue
			}
			doc, loaded := documents[key]
			if !loaded {
				doc, _ = t.knowledgeService.GetKnowledgeByIDOnly(ctx, key.id)
				documents[key] = doc
			}
			if documentIsRetired(doc) || doc.ID != key.id || doc.KnowledgeBaseID != key.kb ||
				(doc.FileHash != "" && doc.FileHash != locator.SourceHash) {
				fresh = false
				break
			}
		}
		if fresh {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// Each same-text document keeps a representative for the reranker, which
// also sees the title. Show the body once after ranking. Byte equality
// preserves line breaks, whitespace-sensitive cells, numbers and dates.
func (t *SearchKnowledgeTool) deduplicateResultsForRerank(results []*searchResultWithMeta) []*searchResultWithMeta {
	return deduplicateSearchRows(results, true)
}

func (t *SearchKnowledgeTool) deduplicateResults(results []*searchResultWithMeta) []*searchResultWithMeta {
	return deduplicateSearchRows(results, false)
}

func deduplicateSearchRows(results []*searchResultWithMeta, beforeRerank bool) []*searchResultWithMeta {
	type contentKey struct{ document, content, image string }
	type identity struct{ kb, document, chunk string }
	byContent := make(map[contentKey]*searchResultWithMeta)
	byID := make(map[identity]*searchResultWithMeta)
	ordered := make([]*searchResultWithMeta, 0, len(results))
	for _, result := range results {
		if result != nil && result.SearchResult != nil {
			ordered = append(ordered, result)
		}
	}
	sortSearchRows(ordered)
	unique := ordered[:0]
	for _, row := range ordered {
		id := identity{row.KnowledgeBaseID, row.KnowledgeID, row.ID}
		if kept := byID[id]; kept != nil && row.ID != "" {
			if kept.Content == row.Content && kept.ContentRevision == row.ContentRevision {
				mergeSearchOrigins(kept, row)
			}
			continue
		}

		key := contentKey{content: row.Content}
		image := row.ImageInfo != "" || searchutil.IsImageEvidence(row.SearchResult)
		if beforeRerank || image {
			key.document = row.KnowledgeBaseID + "/" + row.KnowledgeID
		}
		if image {
			key.image = row.ImageInfo
		}
		// Image evidence can share a row only for the same known image in
		// the same document. FAQ, Wiki and other derived rows keep their
		// independent payload even when the visible text happens to match.
		canShare := canShareSearchText(row) ||
			(!beforeRerank && !isSearchWikiResult(row) && key.image != "" && strings.TrimSpace(key.content) != "" && len(row.MatchedImages) <= 1)
		if kept := byContent[key]; canShare && kept != nil && shareSearchImage(kept, row) {
			mergeSearchOrigins(kept, row)
			byID[id] = kept
			continue
		}
		if canShare {
			byContent[key] = row
		}
		byID[id] = row
		unique = append(unique, row)
	}
	return unique
}

func canShareSearchText(row *searchResultWithMeta) bool {
	text := row.ChunkType == "" || row.ChunkType == string(types.ChunkTypeText)
	return text && strings.TrimSpace(row.Content) != "" && row.ImageInfo == "" &&
		!searchutil.IsImageEvidence(row.SearchResult) && row.KnowledgeBaseType != types.KnowledgeBaseTypeFAQ && !isSearchWikiResult(row)
}

func isSearchWikiResult(row *searchResultWithMeta) bool {
	return row.ChunkType == string(types.ChunkTypeWikiPage) || strings.HasPrefix(row.ID, "wp-")
}

// Capture the authorized identical-text family before the vendor cap and
// threshold remove individuals. Only a qualified result contributes current
// evidence and a reserved slot. Other members can guide an exact follow-up
// in their own document to resolve a gap.
func searchCandidateOrigins(rows []*searchResultWithMeta) map[string][]searchOrigin {
	families := make(map[string][]searchOrigin)
	for _, row := range rows {
		if canShareSearchText(row) {
			families[row.Content] = append(families[row.Content], searchOrigin{SearchResult: row.SearchResult, navigationOnly: true})
			for _, origin := range row.Origins {
				families[row.Content] = append(families[row.Content], searchOrigin{SearchResult: origin.SearchResult, navigationOnly: true})
			}
		}
	}
	return families
}

func retainSearchCandidateOrigins(rows []*searchResultWithMeta, candidates map[string][]searchOrigin) {
	for _, row := range rows {
		family := candidates[row.Content]
		if len(family) < 2 || !canShareSearchText(row) {
			continue
		}
		seen := map[searchOriginIdentity]bool{searchOriginID(row.SearchResult): true}
		for _, origin := range row.Origins {
			seen[searchOriginID(origin.SearchResult)] = true
		}
		for _, origin := range family {
			key := searchOriginID(origin.SearchResult)
			if !seen[key] {
				row.Origins = append(row.Origins, origin)
				seen[key] = true
			}
		}
	}
}

func shareSearchImage(a, b *searchResultWithMeta) bool {
	if len(a.MatchedImages) == 0 || len(b.MatchedImages) == 0 {
		return true // The content key already compares both image descriptions.
	}
	return len(a.MatchedImages) == 1 && len(b.MatchedImages) == 1 &&
		a.MatchedImages[0].URL == b.MatchedImages[0].URL
}

func mergeSearchOrigins(kept, other *searchResultWithMeta) {
	searchutil.InheritImageEvidence(kept.SearchResult, other.SearchResult)
	for _, origin := range append([]searchOrigin{{SearchResult: other.SearchResult}}, other.Origins...) {
		key := searchOriginID(origin.SearchResult)
		if searchOriginID(kept.SearchResult) == key {
			continue
		}
		index := -1
		for i, prior := range kept.Origins {
			if searchOriginID(prior.SearchResult) == key {
				index = i
				break
			}
		}
		if index < 0 {
			kept.Origins = append(kept.Origins, origin)
		} else if kept.Origins[index].navigationOnly && !origin.navigationOnly {
			kept.Origins[index] = origin
		}
	}
}

func verifiedSearchOrigin(row *types.SearchResult) bool {
	chunk := row.SourceChunk
	return chunk != nil && chunk.IsEnabled && chunk.ID != "" && chunk.ID == row.ID &&
		chunk.KnowledgeID == row.KnowledgeID && chunk.KnowledgeBaseID == row.KnowledgeBaseID &&
		chunk.ContentRevision == row.ContentRevision && chunk.Content == row.Content && !row.ContentRewritten &&
		reflect.DeepEqual(chunk.SourceLocators, row.SourceLocators)
}

func appendSearchSourceIdentity(row map[string]any, result *types.SearchResult) {
	row["chunk_id"] = result.ID
	row["chunk_index"] = result.ChunkIndex
	row["chunk_type"] = result.ChunkType
	row["content_revision"] = result.ContentRevision
	row["start_at"] = result.StartAt
	row["end_at"] = result.EndAt
	row["parent_chunk_id"] = result.ParentChunkID
	if len(result.SourceLocators) > 0 {
		row["source_locators"] = result.SourceLocators
	}
	if result.ContentRewritten {
		row["content_rewritten"] = true
	}
	row["content_offset"] = 0
	row["content_offset_space"] = "chunk_unicode_runes"
	row["content_complete"] = true
	appendSearchChunkProjection(row, result)
}

func appendSearchOriginLocations(out *strings.Builder, rows []map[string]any) {
	for _, row := range rows {
		fmt.Fprintf(out, "<origin title=\"%s\">", xmlEscape(fmt.Sprint(row["knowledge_title"])))
		if locators, ok := row["source_locators"].(types.SourceLocators); ok {
			for _, locator := range locators {
				if locator.Mapping == "exact" && locator.Page > 0 {
					out.WriteString("第" + strconv.Itoa(locator.Page) + "页 ")
				}
				if locator.Mapping == "exact" && locator.Slide > 0 {
					out.WriteString("第" + strconv.Itoa(locator.Slide) + "页 ")
				}
			}
		}
		out.WriteString("</origin>\n")
	}
}

func sortSearchRows(rows []*searchResultWithMeta) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.KnowledgeID != b.KnowledgeID {
			return a.KnowledgeID < b.KnowledgeID
		}
		return a.ID < b.ID
	})
}

// rows are already ranked inside each document. Fill the vendor batch in
// passes over the selected documents, before the shared rerank stage can cut
// on global retrieval score.
func roundRobinSearchCandidates(rows []*searchResultWithMeta, documents []string, limit int) []*searchResultWithMeta {
	limit = min(len(rows), limit)
	queues := make(map[string][]*searchResultWithMeta, len(documents))
	for _, row := range rows {
		queues[row.KnowledgeID] = append(queues[row.KnowledgeID], row)
	}
	chosen := make([]*searchResultWithMeta, 0, limit)
	for rank := 0; len(chosen) < limit; rank++ {
		before := len(chosen)
		for _, doc := range documents {
			queue := queues[doc]
			if rank < len(queue) {
				chosen = append(chosen, queue[rank])
				if len(chosen) == limit {
					return chosen
				}
			}
		}
		if len(chosen) == before {
			break
		}
	}
	return chosen
}

func searchRowDocuments(row *searchResultWithMeta) map[string]bool {
	docs := make(map[string]bool)
	if strings.TrimSpace(row.Content) != "" {
		docs[row.KnowledgeID] = true
		for _, origin := range row.Origins {
			if !origin.navigationOnly && verifiedSearchOrigin(origin.SearchResult) {
				docs[origin.KnowledgeID] = true
			}
		}
	}
	return docs
}

func coveredSearchDocuments(rows []*searchResultWithMeta) map[string]bool {
	docs := make(map[string]bool)
	for _, row := range rows {
		for id := range searchRowDocuments(row) {
			docs[id] = true
		}
	}
	return docs
}

func selectSearchRows(ctx context.Context, rows []*searchResultWithMeta, limit int, byDocument bool) []*searchResultWithMeta {
	if len(rows) == 0 {
		return rows
	}
	if !byDocument {
		if selected := selectMMR(ctx, rows, min(len(rows), limit)); len(selected) > 0 {
			rows = selected
		}
		sortSearchRows(rows)
		return rows
	}

	seen := make(map[string]bool)
	selected := make([]*searchResultWithMeta, 0, min(len(rows), limit))
	remaining := make([]*searchResultWithMeta, 0, len(rows))
	for _, row := range rows {
		newDocument := false
		for id := range searchRowDocuments(row) {
			newDocument = newDocument || !seen[id]
			seen[id] = true
		}
		if newDocument {
			selected = append(selected, row)
			if len(selected) == limit {
				return selected
			}
		} else {
			remaining = append(remaining, row)
		}
	}
	return append(selected, selectMMR(ctx, remaining, min(len(remaining), limit-len(selected)))...)
}

// Prefer dropping the lowest-ranked redundant passage over dropping the
// only shown passage from a document. A shared body can cover several docs.
func leastCoverageLoss(rows []*searchResultWithMeta) int {
	counts := make(map[string]int)
	documents := make([]map[string]bool, len(rows))
	for i, row := range rows {
		documents[i] = searchRowDocuments(row)
		for id := range documents[i] {
			counts[id]++
		}
	}
	bestIndex, bestLoss := len(rows)-1, len(counts)+1
	for i := len(rows) - 1; i >= 0; i-- {
		loss := 0
		for id := range documents[i] {
			if counts[id] == 1 {
				loss++
			}
		}
		if loss < bestLoss {
			bestIndex, bestLoss = i, loss
		}
	}
	return bestIndex
}

func documentSearchCoverage(ids []string, searches, qualified, selected, shown map[string]bool) []map[string]any {
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		status := "no_match"
		switch {
		case shown[id]:
			status = "hit"
		case !searches[id]:
			status = "failed"
		case selected[id]:
			status = "budget_truncated"
		case qualified[id]:
			status = "limit_truncated"
		}
		rows = append(rows, map[string]any{"knowledge_id": id, "status": status})
	}
	return rows
}
