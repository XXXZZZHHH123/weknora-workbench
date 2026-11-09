package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type physicalPageEvidence struct {
	chunks []*types.Chunk
	views  map[string]*types.ChunkContentView
}

// Collect all requested physical pages in one ordered scan. It does not infer
// source pages from chunk positions or invent a total physical-page count.
func (t *ReadDocumentTool) collectPhysicalPages(ctx context.Context, knowledge *types.Knowledge, requested []int) (map[int]*physicalPageEvidence, int64, error) {
	pages := map[int]*physicalPageEvidence{}
	for _, number := range requested {
		pages[number] = &physicalPageEvidence{views: map[string]*types.ChunkContentView{}}
	}
	var total int64
	current := 0
	for scanPage := 1; ; scanPage++ {
		chunks, count, err := t.listChunks(ctx, knowledge, scanPage, readDocumentScanPageSize)
		if err != nil {
			return nil, 0, err
		}
		if scanPage > 1 && count != total {
			return nil, 0, fmt.Errorf("document chunk inventory changed during physical-page scan; retry against a stable document version")
		}
		total = count
		expected := min(int64(readDocumentScanPageSize), max(int64(0), total-int64((scanPage-1)*readDocumentScanPageSize)))
		if int64(len(chunks)) != expected {
			return nil, 0, fmt.Errorf("physical-page scan is incomplete: source reports %d readable chunks but scan page %d returned %d instead of %d; retry before claiming page completeness", total, scanPage, len(chunks), expected)
		}
		for _, chunk := range chunks {
			if chunk == nil || chunk.ID == "" || chunk.KnowledgeID != knowledge.ID || chunk.KnowledgeBaseID != knowledge.KnowledgeBaseID || chunk.TenantID != t.tenantFor(knowledge) {
				return nil, 0, fmt.Errorf("physical-page scan returned a chunk outside the authorized document identity")
			}
		}
		enrichChunkImageInfo(ctx, t.chunkService.GetRepository(), t.tenantFor(knowledge), chunks)
		for _, chunk := range chunks {
			if metadataPage := chunkMetadataPage(chunk); metadataPage > 0 {
				current = metadataPage
			}
			nextPage := current
			for _, number := range requested {
				content, next, ranges := pageTextWithRanges(chunk.Content, current, number)
				nextPage = next
				if content == "" {
					continue
				}
				copy := *chunk
				copy.Content = content
				copy.ImageInfo = pageImages(chunk.ImageInfo, content, number, content != strings.TrimSpace(chunk.Content))
				pages[number].views[chunk.ID] = chunkContentView(chunk, ranges)
				pages[number].chunks = append(pages[number].chunks, &copy)
			}
			current = nextPage
		}
		if len(chunks) == 0 || int64(scanPage*readDocumentScanPageSize) >= total {
			break
		}
	}
	return pages, total, nil
}

func (t *ReadDocumentTool) readPhysicalPages(ctx context.Context, knowledge *types.Knowledge, requested []int, offset, limit int, segment *chunkContentSegment, query string, matchers []*regexp.Regexp, regexMode bool) (*types.ToolResult, error) {
	if err := validateDocumentPages(requested, readDocumentMaxPages); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	// Keep enough budget for each page's wrapper and continuation state. Asking
	// for fewer pages is preferable to returning malformed or silently lost rows.
	if len(requested) > 1 && OutputBudget(ctx) < len(requested)*700 {
		return &types.ToolResult{Success: false, Error: "output budget is too small for this page batch; request fewer physical pages"}, nil
	}
	pages, total, err := t.collectPhysicalPages(ctx, knowledge, requested)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, err
	}
	results := make([]*types.ToolResult, 0, len(requested))
	pageBudget := OutputBudget(ctx)
	if len(requested) > 1 {
		pageBudget = (pageBudget - 300) / len(requested)
	}
	for _, number := range requested {
		evidence := pages[number]
		pageCtx := WithOutputBudget(ctx, pageBudget)
		var result *types.ToolResult
		if query != "" && len(evidence.chunks) > 0 {
			result = t.readPhysicalPageQuery(pageCtx, knowledge, number, evidence, total, offset, limit, query, matchers, regexMode)
		} else {
			result, err = t.renderPhysicalPage(pageCtx, knowledge, number, offset, limit, segment, evidence.chunks, evidence.views, total)
		}
		if err != nil {
			return result, err
		}
		results = append(results, result)
	}
	if len(results) == 1 {
		return results[0], nil
	}
	data := t.buildData(knowledge, total, nil)
	rows := []map[string]interface{}{}
	chunkIDs := []string{}
	statuses := []map[string]interface{}{}
	matchedPages := []int{}
	allComplete, anySuccess, allSuccess := true, false, true
	var output strings.Builder
	for i, result := range results {
		status := map[string]interface{}{"page": requested[i], "success": result.Success}
		if result.Success {
			anySuccess = true
			matchedPages = append(matchedPages, requested[i])
			pageRows, _ := result.Data["chunks"].([]map[string]interface{})
			rows = append(rows, pageRows...)
			ids, _ := result.Data["chunk_ids"].([]string)
			chunkIDs = append(chunkIDs, ids...)
			for _, key := range []string{"page_chunks", "page_complete", "offset", "offset_space", "next_offset", "truncated", "query_scope", "match_count", "content_offset", "content_complete", "next_content_offset"} {
				if value, ok := result.Data[key]; ok {
					status[key] = value
				}
			}
			if result.Data["page_complete"] != true {
				allComplete = false
			}
		} else {
			allComplete, allSuccess = false, false
			status["error"] = result.Error
		}
		statuses = append(statuses, status)
		fmt.Fprintf(&output, "<physical_page page=\"%d\" success=\"%t\">\n%s\n</physical_page>\n", requested[i], result.Success, result.Output)
	}
	data["chunks"], data["chunk_ids"], data["fetched_chunks"] = rows, chunkIDs, len(rows)
	if len(chunkIDs) > 0 {
		data["source_chunk_id"] = chunkIDs[0]
	}
	data["requested_pages"], data["page_results"] = append([]int(nil), requested...), statuses
	data["pages_complete"], data["all_pages_succeeded"] = allComplete, allSuccess
	data["document_scope"] = map[string]interface{}{"matched": anySuccess, "knowledge_id": knowledge.ID, "pages": matchedPages, "plan_validated": t.documentConstraint != nil}
	statusJSON, _ := json.Marshal(statuses)
	output.WriteString("<page_results>" + string(statusJSON) + "</page_results>")
	result := &types.ToolResult{Success: anySuccess, Output: output.String(), Data: data}
	if !anySuccess {
		result.Error = "none of the requested physical pages could be read; inspect page_results"
	}
	return result, nil
}

func (t *ReadDocumentTool) readPhysicalPageQuery(ctx context.Context, knowledge *types.Knowledge, number int, evidence *physicalPageEvidence, total int64, offset, limit int, query string, matchers []*regexp.Regexp, regexMode bool) *types.ToolResult {
	var rows []readChunkRow
	emitted := map[string]bool{}
	used, matches, next := 0, 0, -1
	budget := OutputBudget(ctx) * 3 / 5
	emit := func(index int, role string) {
		if index < 0 || index >= len(evidence.chunks) {
			return
		}
		chunk := evidence.chunks[index]
		if emitted[chunk.ID] {
			if role == "match" {
				for i := range rows {
					if rows[i].chunk.ID == chunk.ID {
						rows[i].role = role
						break
					}
				}
			}
			return
		}
		cost := utf8.RuneCountInString(chunk.Content) + readDocumentChunkOverhead
		if role != "match" && used+cost > budget {
			return
		}
		emitted[chunk.ID] = true
		rows = append(rows, readChunkRow{chunk: chunk, contentView: evidence.views[chunk.ID], role: role})
		used += utf8.RuneCountInString(chunk.Content) + readDocumentChunkOverhead
	}
	for index := offset; index < len(evidence.chunks); index++ {
		chunk := evidence.chunks[index]
		if !matchesAll(matchers, enrichChunkContent(chunk)) {
			continue
		}
		if matches >= min(limit, readDocumentMaxMatches) || used >= budget {
			next = index
			break
		}
		matches++
		// The actual match is retained before optional context consumes budget.
		emit(index, "match")
		if used < budget {
			emit(index-1, "context_before")
		}
		if used < budget {
			emit(index+1, "context_after")
		}
	}
	positions := make(map[string]int, len(evidence.chunks))
	for i, chunk := range evidence.chunks {
		positions[chunk.ID] = i
	}
	sort.SliceStable(rows, func(i, j int) bool { return positions[rows[i].chunk.ID] < positions[rows[j].chunk.ID] })
	rows = boundChunkRowsContent(ctx, rows)
	data := t.buildData(knowledge, total, rows)
	data["page"], data["page_chunks"], data["page_complete"] = number, len(evidence.chunks), false
	data["offset"], data["offset_space"] = offset, "physical_page_chunks"
	data["query"], data["match_count"], data["truncated"] = query, matches, next >= 0
	if next >= 0 {
		data["next_offset"] = next
	}
	mode := "literal_all_terms_in_one_chunk"
	if regexMode {
		mode = "regular_expression_in_one_chunk"
	}
	scope := map[string]interface{}{"mode": mode, "physical_page": number, "offset": offset, "scan_complete": next < 0, "page_scan_complete": offset == 0 && next < 0, "document_scan_complete": false,
		"notice": "This literal query only searched the selected physical page. No match does not prove the page or document lacks the requested information; read actual source ranges or revise the query using evidence."}
	if !regexMode {
		scope["literal_terms"] = strings.Fields(query)
	}
	data["query_scope"] = scope
	data["document_scope"] = map[string]interface{}{"matched": true, "knowledge_id": knowledge.ID, "page": number, "plan_validated": t.documentConstraint != nil}
	annotateContentPagination(data, rows)
	for _, row := range data["chunks"].([]map[string]interface{}) {
		row["page"] = number
	}
	scopeJSON, _ := json.Marshal(scope)
	return &types.ToolResult{Success: true, Data: data, Output: t.buildOutput(knowledge, total, rows, query) + "\n<query_scope>" + xmlEscape(string(scopeJSON)) + "</query_scope>"}
}
