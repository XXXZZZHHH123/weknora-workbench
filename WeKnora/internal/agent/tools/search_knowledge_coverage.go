package tools

import (
	"context"
	"fmt"
	"reflect"
	"slices"
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
// also sees the title. Keep those document boundaries after ranking. Byte equality
// preserves line breaks, whitespace-sensitive cells, numbers and dates.
func (t *SearchKnowledgeTool) deduplicateResultsForRerank(results []*searchResultWithMeta) []*searchResultWithMeta {
	return deduplicateSearchRows(results, true)
}

func (t *SearchKnowledgeTool) deduplicateResults(results []*searchResultWithMeta) []*searchResultWithMeta {
	return deduplicateSearchRows(results, false)
}

func deduplicateSearchRows(results []*searchResultWithMeta, beforeRerank bool) []*searchResultWithMeta {

	type identity struct{ kb, document, chunk string }
	byContent := make(map[searchContentKey]*searchResultWithMeta)
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

		key := searchRowContentKey(row.SearchResult)
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

type searchContentKey struct{ kb, document, content, image string }

func searchRowContentKey(row *types.SearchResult) searchContentKey {
	return searchContentKey{row.KnowledgeBaseID, row.KnowledgeID, row.Content, row.ImageInfo}
}

func sameSearchDocument(a, b *types.SearchResult) bool {
	return a != nil && b != nil && a.KnowledgeID != "" && a.KnowledgeBaseID != "" && a.KnowledgeID == b.KnowledgeID &&
		a.KnowledgeBaseID == b.KnowledgeBaseID
}

func searchCandidateOrigins(rows []*searchResultWithMeta) map[searchContentKey][]searchOrigin {
	families := make(map[searchContentKey][]searchOrigin)
	for _, row := range rows {
		if canShareSearchText(row) {
			key := searchRowContentKey(row.SearchResult)
			families[key] = append(families[key], searchOrigin{SearchResult: row.SearchResult, navigationOnly: true})
			for _, origin := range row.Origins {
				if sameSearchDocument(row.SearchResult, origin.SearchResult) {
					families[key] = append(families[key], searchOrigin{SearchResult: origin.SearchResult, navigationOnly: true})
				}
			}
		}
	}
	return families
}

func retainSearchCandidateOrigins(rows []*searchResultWithMeta, candidates map[searchContentKey][]searchOrigin) {
	for _, row := range rows {
		family := candidates[searchRowContentKey(row.SearchResult)]
		if len(family) < 2 || !canShareSearchText(row) {
			continue
		}
		seen := map[searchOriginIdentity]bool{searchOriginID(row.SearchResult): true}
		for _, origin := range row.Origins {
			seen[searchOriginID(origin.SearchResult)] = true
		}
		for _, origin := range family {
			key := searchOriginID(origin.SearchResult)
			if !seen[key] && sameSearchDocument(row.SearchResult, origin.SearchResult) {
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
	if !sameSearchDocument(kept.SearchResult, other.SearchResult) {
		return
	}
	searchutil.InheritImageEvidence(kept.SearchResult, other.SearchResult)
	for _, origin := range append([]searchOrigin{{SearchResult: other.SearchResult}}, other.Origins...) {
		if !sameSearchDocument(kept.SearchResult, origin.SearchResult) {
			continue
		}
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
		if row["navigation_only"] == true {
			continue
		}
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

type searchLocation struct {
	kb, document string
	page         int
}

func searchLocations(requirements []DocumentCoverageRequirement) []searchLocation {
	var locations []searchLocation
	seen := map[searchLocation]bool{}
	for _, requirement := range requirements {
		pages := requirement.Pages
		if len(pages) == 0 {
			pages = []int{0}
		}
		for _, page := range pages {
			key := searchLocation{requirement.KnowledgeBaseID, requirement.KnowledgeID, page}
			if !seen[key] {
				locations = append(locations, key)
				seen[key] = true
			}
		}
	}
	return locations
}

func searchRowHasLocation(row *searchResultWithMeta, location searchLocation) bool {
	if row.KnowledgeID != location.document || row.KnowledgeBaseID != location.kb || strings.TrimSpace(row.Content) == "" {
		return false
	}
	if location.page == 0 {
		return true
	}
	hasPage := func(result *types.SearchResult) bool {
		for _, locator := range result.SourceLocators {
			if locator.Mapping == "exact" &&
				(locator.Type == types.SourceLocatorPDF && locator.Page == location.page ||
					locator.Type == types.SourceLocatorSlide && locator.Slide == location.page) {
				return true
			}
		}
		return false
	}
	if hasPage(row.SearchResult) {
		return true
	}
	for _, origin := range row.Origins {
		if !origin.navigationOnly && sameSearchDocument(row.SearchResult, origin.SearchResult) &&
			verifiedSearchOrigin(origin.SearchResult) && hasPage(origin.SearchResult) {
			return true
		}
	}
	return false
}

func coveredSearchLocations(rows []*searchResultWithMeta, locations []searchLocation) map[searchLocation]bool {
	covered := make(map[searchLocation]bool, len(locations))
	for _, location := range locations {
		covered[location] = slices.ContainsFunc(rows, func(row *searchResultWithMeta) bool {
			return searchRowHasLocation(row, location)
		})
	}
	return covered
}

// Reserve placements before ranking fills the remaining slots. One verified
// passage may serve several physical pages from the same document.
func reserveSearchLocations(rows []*searchResultWithMeta, locations []searchLocation, limit int) (reserved, remaining []*searchResultWithMeta) {
	if len(locations) == 0 {
		return nil, rows
	}
	remaining = slices.Clone(rows)
	covered := map[searchLocation]bool{}
	for len(reserved) < limit && len(covered) < len(locations) {
		best, gain := -1, 0
		for i, row := range remaining {
			count := 0
			for _, location := range locations {
				if !covered[location] && searchRowHasLocation(row, location) {
					count++
				}
			}
			if count > gain {
				best, gain = i, count
			}
		}
		if best == -1 {
			break
		}
		row := remaining[best]
		for _, location := range locations {
			if searchRowHasLocation(row, location) {
				covered[location] = true
			}
		}
		reserved = append(reserved, row)
		remaining = slices.Delete(remaining, best, best+1)
	}
	return reserved, remaining
}

// Rows are already ranked inside each document. Give explicitly compared
// documents turns, then use retrieval order for any remaining slots.
func roundRobinSearchCandidates(rows []*searchResultWithMeta, documents []string, limit int, locations ...searchLocation) []*searchResultWithMeta {
	limit = min(len(rows), limit)
	chosen, remaining := reserveSearchLocations(rows, locations, limit)
	queues := make(map[string][]*searchResultWithMeta, len(documents))
	start := make(map[string]int, len(documents))
	for _, row := range chosen {
		start[row.KnowledgeID]++
	}
	for _, row := range remaining {
		queues[row.KnowledgeID] = append(queues[row.KnowledgeID], row)
	}
	for rank := 0; len(chosen) < limit && len(documents) > 0; rank++ {
		pending := false
		for _, doc := range documents {
			queue := queues[doc]
			if rank < start[doc] {
				pending = true
				continue
			}
			if pos := rank - start[doc]; pos < len(queue) {
				pending = true
				chosen = append(chosen, queue[pos])
				if len(chosen) == limit {
					return chosen
				}
			}
		}
		if !pending {
			break
		}
	}
	for _, row := range remaining {
		if len(chosen) == limit {
			break
		}
		if !slices.Contains(documents, row.KnowledgeID) {
			chosen = append(chosen, row)
		}
	}
	return chosen
}

func coveredSearchDocuments(rows []*searchResultWithMeta) map[string]bool {
	docs := make(map[string]bool)
	for _, row := range rows {
		if strings.TrimSpace(row.Content) != "" {
			docs[row.KnowledgeID] = true
		}
	}
	return docs
}

func selectSearchRows(ctx context.Context, rows []*searchResultWithMeta, limit int, documents []string, locations ...searchLocation) []*searchResultWithMeta {
	if len(rows) == 0 {
		return rows
	}
	selected, remaining := reserveSearchLocations(rows, locations, limit)
	seen := coveredSearchDocuments(selected)
	var fill []*searchResultWithMeta
	for _, row := range remaining {
		if len(selected) < limit && slices.Contains(documents, row.KnowledgeID) && !seen[row.KnowledgeID] &&
			strings.TrimSpace(row.Content) != "" {
			selected = append(selected, row)
			seen[row.KnowledgeID] = true
		} else {
			fill = append(fill, row)
		}
	}
	extra := selectMMR(ctx, fill, min(len(fill), limit-len(selected)))
	if len(documents) == 0 && len(locations) == 0 {
		sortSearchRows(extra)
		return extra
	}
	return append(selected, extra...)
}

// Prefer dropping the lowest-ranked redundant passage over dropping the
// only shown passage from a document.
func leastCoverageLoss(rows []*searchResultWithMeta, documents []string, locations []searchLocation) int {
	locationCounts := make(map[searchLocation]int)
	for _, location := range locations {
		for _, row := range rows {
			if searchRowHasLocation(row, location) {
				locationCounts[location]++
			}
		}
	}
	counts := make(map[string]int)
	for _, row := range rows {
		if slices.Contains(documents, row.KnowledgeID) && strings.TrimSpace(row.Content) != "" {
			counts[row.KnowledgeID]++
		}
	}
	bestIndex, bestLoss, biggest := len(rows)-1, (len(counts)+1)*(len(locations)+1)+1, 0
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		loss := 0
		if counts[row.KnowledgeID] == 1 && strings.TrimSpace(row.Content) != "" {
			loss++
		}
		for _, location := range locations {
			if locationCounts[location] == 1 && searchRowHasLocation(row, location) {
				loss += len(counts) + 1
			}
		}
		size := len(row.Content)
		if loss < bestLoss || len(locations) > 0 && loss == bestLoss && size > biggest {
			bestIndex, bestLoss, biggest = i, loss, size
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

func locationSearchCoverage(requirements []DocumentCoverageRequirement, searches map[string]bool, locations []searchLocation, stages ...[]*searchResultWithMeta) []map[string]any {
	if len(requirements) == 0 {
		return nil
	}
	covered := make([]map[searchLocation]bool, len(stages))
	for i, stage := range stages {
		covered[i] = coveredSearchLocations(stage, locations)
	}
	statuses := []string{"no_match", "candidate_truncated", "below_threshold", "limit_truncated", "budget_truncated", "hit"}
	var rows []map[string]any
	for _, requirement := range requirements {
		for _, location := range searchLocations([]DocumentCoverageRequirement{requirement}) {
			status := "failed"
			if searches[location.document] {
				status = statuses[0]
				for i := range covered {
					if !covered[i][location] {
						break
					}
					status = statuses[i+1]
				}
			}
			rows = append(rows, map[string]any{
				"knowledge_id": location.document, "knowledge_base_id": location.kb, "page": location.page,
				"target_index": requirement.TargetIndex, "location_index": requirement.LocationIndex,
				"period": requirement.Period, "fact": requirement.Fact, "status": status,
			})
		}
	}
	return rows
}
