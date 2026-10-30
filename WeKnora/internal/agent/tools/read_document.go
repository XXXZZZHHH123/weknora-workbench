package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	readDocumentDefaultLimit    = 20
	readDocumentMaxLimit        = 100
	readDocumentMaxContext      = 5
	readDocumentMaxMatches      = 20
	readDocumentScanPageSize    = 100
	readDocumentMaxContentRunes = 12000
	// readDocumentChunkOverhead reserves room for the model view's chunk tags,
	// content-completeness state, and continuation guidance.
	readDocumentChunkOverhead = 240
)

var readDocumentTool = BaseTool{
	name: ToolReadDocument,
	description: "Read a document from the knowledge bases in scope: its metadata plus chunks in document order.\n" +
		"id is a doc://N document handle (reads a page of chunks starting at offset) or a chunk://N chunk handle (reads that " +
		"chunk; set context to include neighbouring chunks on each side).\n" +
		"For a user-specified physical page or PPT slide, use page (1-based). It reads only that page's explicit parser markers; offset is never a physical page number. Missing page markers are reported, never guessed.\n" +
		"Printed page labels and table-of-contents ranges are not physical PDF pages or proof that a listed section is present in this file. Verify physical pages with parser source_locators or page reads; chunk counts and the largest observed locator do not establish total pages.\n" +
		"The result includes an image_catalog of actual stored figures, even for older text-only imports. " +
		"To inspect a figure, repeat the same authorized document/page read with images=[its exact reference], detail=full and focus describing the complete user request. " +
		"Choose the figure from its actual source labels/context; text labels and image URLs alone do not reveal curves or their colors. " +
		"Returned originals are delivered to a vision model or the configured separate image reader; report estimates and unclear overlaps honestly.\n" +
		"query finds passages inside the document: matching chunks come back with one chunk of context on each " +
		"side. By default query is case-insensitive and split on whitespace: a chunk matches when it contains " +
		"every word, in any order. Set regex=true to match query as one POSIX regular expression instead. " +
		"When a query result is truncated, repeat it with offset=next_offset to get the following matches.\n" +
		"Page through long documents with offset and limit: offset counts chunks in reading order from 0 and is " +
		"not a chunk index, so continue with the returned next_offset, and use id=chunk://N with context to read around " +
		"a specific chunk. A page holds fewer than limit chunks when they would exceed the output size budget; " +
		"next_offset always points at the first chunk not yet returned. If a returned chunk body is partial, " +
		"continue with id=chunk://N and content_offset=next_content_offset; content offsets count Unicode characters " +
		"within that chunk. Reading a long document end to end " +
		"costs many calls, so prefer query or search_knowledge to locate the relevant part first.",
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "doc://N document handle or chunk://N chunk handle from retrieval results or the runtime context",
      "minLength": 1
    },
    "offset": {
      "type": "integer",
      "description": "Position in reading order to start from (default 0); continue with next_offset",
      "minimum": 0
    },
    "content_offset": {
      "type": "integer",
      "description": "Unicode-character offset within a real chunk://N chunk's returned source text; on a physical-page read it is relative to that page-clipped text",
      "minimum": 0
    },
    "content_limit": {
      "type": "integer",
      "description": "Maximum Unicode characters to read from a real chunk://N chunk (default is bounded by output budget)",
      "minimum": 1,
      "maximum": 12000
    },
    "page": {
      "type": "integer",
      "description": "Exact 1-based document page / PPT slide; requires explicit parsed page markers, never chunk offset",
      "minimum": 1
    },
    "limit": {
      "type": "integer",
      "description": "Chunks per page (default 20, max 100)",
      "minimum": 1,
      "maximum": 100
    },
    "query": {
      "type": "string",
      "description": "Words a chunk must all contain; returns matching chunks with context instead of a page"
    },
    "regex": {
      "type": "boolean",
      "description": "Match query as one POSIX regular expression (default false: each word matched literally)"
    },
    "context": {
      "type": "integer",
      "description": "Neighbouring chunks to include on each side of a chunk://N chunk (default 0, max 5)",
      "minimum": 0,
      "maximum": 5
    },
    "images": {
      "type": "array",
      "items": {"type": "string"},
      "maxItems": 4,
      "description": "Exact image references from this document/page's image_catalog; omit to list figures without reading their pixels"
    },
    "detail": {
      "type": "string", "enum": ["brief", "full"],
      "description": "Image inspection detail; defaults to full when images are selected"
    },
    "image_series": {
      "type": "array", "items": {"type": "string"}, "maxItems": 32,
      "description": "For supported vector charts, explicit curve LEGEND identifiers are required to read numeric geometry. Select the labels relevant to the full question; a chart title or metric name is not a legend. If omitted or invalid, use the returned actual legend options and retry. To inspect every curve explicitly select all actual options. Omit for non-chart images. Never supply guessed labels or expected values."
    },
    "focus": {
      "type": "string",
      "description": "The complete user question and required visual evidence; no expected answer or guessed values"
    }
  },
  "required": ["id"]
}`),
}

// ReadDocumentInput defines the input parameters for read_document.
type ReadDocumentInput struct {
	ID            string             `json:"id"`
	Offset        int                `json:"offset,omitempty"`
	ContentOffset int                `json:"content_offset,omitempty"`
	ContentLimit  int                `json:"content_limit,omitempty"`
	Limit         int                `json:"limit,omitempty"`
	Query         string             `json:"query,omitempty"`
	Regex         bool               `json:"regex,omitempty"`
	Context       int                `json:"context,omitempty"`
	Page          int                `json:"page,omitempty"`
	SourceScope   *DocumentScopePlan `json:"source_scope,omitempty"`
	Images        []string           `json:"images,omitempty"`
	ImageSeries   []string           `json:"image_series,omitempty"`
	Detail        string             `json:"detail,omitempty"`
	Focus         string             `json:"focus,omitempty"`
}

// ReadDocumentTool reads document metadata and chunks by page, by chunk
// handle, or by an in-document text search.
type ReadDocumentTool struct {
	BaseTool
	knowledgeService   interfaces.KnowledgeService
	chunkService       interfaces.ChunkService
	searchTargets      types.SearchTargets
	documentConstraint *RequestedDocumentConstraint
	imageReader        DocumentImageReader
	sourceImageReader  DocumentSourceImageReader
	figureReader       DocumentFigureReader
	wikiPageService    interfaces.WikiPageService
}

// WithImageReader wires the existing authorized document-image storage reader.
func (t *ReadDocumentTool) WithImageReader(reader DocumentImageReader) *ReadDocumentTool {
	t.imageReader = reader
	return t
}

func (t *ReadDocumentTool) WithDocumentConstraint(constraint *RequestedDocumentConstraint) *ReadDocumentTool {
	t.documentConstraint = constraint
	return t
}

func (t *ReadDocumentTool) WithWikiPageService(service interfaces.WikiPageService) *ReadDocumentTool {
	t.wikiPageService = service
	return t
}

// NewReadDocumentTool creates a new read_document tool.
func NewReadDocumentTool(
	knowledgeService interfaces.KnowledgeService,
	chunkService interfaces.ChunkService,
	searchTargets types.SearchTargets,
) *ReadDocumentTool {
	return &ReadDocumentTool{
		BaseTool:         withDocumentScopeParameter(readDocumentTool),
		knowledgeService: knowledgeService,
		chunkService:     chunkService,
		searchTargets:    searchTargets,
	}
}

// readChunkRow is one chunk in the rendered result together with its role
// (page entry, search match, or context around a match).
type readChunkRow struct {
	chunk             *types.Chunk
	role              string // "", "match", "context_before", "context_after", "focus"
	snippet           string
	contentOffset     int
	contentComplete   bool
	nextContentOffset int
}

type chunkContentSegment struct {
	chunkID string
	offset  int
	limit   int
}

// Execute resolves the id, then dispatches to page, chunk or query reading.
func (t *ReadDocumentTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var input ReadDocumentInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("Failed to parse args: %v", err)}, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "id is required (a doc://N document handle or a chunk://N chunk handle)",
		}, fmt.Errorf("missing id")
	}
	if t.knowledgeService == nil || t.chunkService == nil {
		return &types.ToolResult{Success: false, Error: "document services are unavailable"},
			fmt.Errorf("services unavailable")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = readDocumentDefaultLimit
	}
	if limit > readDocumentMaxLimit {
		limit = readDocumentMaxLimit
	}
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}
	contextChunks := input.Context
	if contextChunks < 0 {
		contextChunks = 0
	}
	if contextChunks > readDocumentMaxContext {
		contextChunks = readDocumentMaxContext
	}
	query := strings.TrimSpace(input.Query)
	if input.ContentOffset < 0 || input.ContentLimit < 0 {
		return &types.ToolResult{Success: false, Error: "content_offset and content_limit must not be negative"}, nil
	}

	knowledge, chunk, err := t.resolveTarget(ctx, id)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, err
	}
	if documentIsRetired(knowledge) {
		return &types.ToolResult{Success: false, Error: "指定文档已删除、正在删除或已归档，不能作为查询来源"}, nil
	}
	if input.SourceScope != nil {
		if t.documentConstraint == nil {
			t.documentConstraint = NewDocumentScopeState()
		}
		if _, err := t.documentConstraint.bind(ctx, *input.SourceScope, t.knowledgeService, t.searchTargets); err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
	}
	if constraint := t.documentConstraint; constraint != nil {
		selected, err := constraint.selection(knowledge.ID)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		if selected.Page > 0 {
			if input.Page > 0 && input.Page != selected.Page {
				return &types.ToolResult{Success: false, Error: "当前问题指定了不同页码，不能用其他页替代"}, nil
			}
			input.Page = selected.Page
		}
	}
	if input.Page < 0 {
		return &types.ToolResult{Success: false, Error: "page must be a positive physical page number"}, nil
	}
	if input.Page > 0 {
		var segment *chunkContentSegment
		if input.ContentLimit > 0 || input.ContentOffset > 0 {
			if chunk == nil {
				return &types.ToolResult{Success: false, Error: "content segmentation requires a real chunk handle (chunk://N)"}, nil
			}
			segment = &chunkContentSegment{chunkID: chunk.ID, offset: input.ContentOffset, limit: input.ContentLimit}
		}
		result, err := t.readPhysicalPage(ctx, knowledge, input.Page, offset, limit, segment)
		return t.finishImageRead(ctx, knowledge, input, result, err)
	}
	if input.ContentLimit > 0 || input.ContentOffset > 0 {
		if chunk == nil {
			return &types.ToolResult{Success: false, Error: "content segmentation requires a real chunk handle (chunk://N)"}, nil
		}
		result, err := t.readChunkContent(ctx, knowledge, chunk, input.ContentOffset, input.ContentLimit)
		return t.finishImageRead(ctx, knowledge, input, result, err)
	}

	var matchers []*regexp.Regexp
	switch {
	case query == "":
	case input.Regex:
		compiled, cerr := regexp.Compile("(?i)" + query)
		if cerr != nil {
			return &types.ToolResult{
				Success: false, Error: fmt.Sprintf("invalid regex query %q: %v", query, cerr),
			}, cerr
		}
		matchers = []*regexp.Regexp{compiled}
	default:
		// Models write keyword queries ("sunflower f(n,k)"), not exact
		// phrases, so each word is matched on its own and all must appear.
		for _, term := range strings.Fields(query) {
			matchers = append(matchers, regexp.MustCompile("(?i)"+regexp.QuoteMeta(term)))
		}
	}

	// A query always searches the owning document, even when id named a
	// chunk: silently returning the single chunk would read as "no match".
	var result *types.ToolResult
	switch {
	case len(matchers) > 0:
		result, err = t.readByQuery(ctx, knowledge, query, matchers, offset, input.Regex)
	case chunk != nil:
		result, err = t.readAroundChunk(ctx, knowledge, chunk, contextChunks)
	default:
		result, err = t.readPage(ctx, knowledge, offset, limit)
	}
	if t.documentConstraint != nil && result != nil && result.Success && result.Data != nil {
		result.Data["document_scope"] = map[string]interface{}{"matched": true, "knowledge_id": knowledge.ID, "plan_validated": true}
	}
	return t.finishImageRead(ctx, knowledge, input, result, err)
}

// resolveTarget accepts either a document id or a chunk id. Documents are
// tried first because they are the common case; a chunk id falls through to
// the chunk lookup. Scope violations are reported as such rather than being
// masked as "not found".
func (t *ReadDocumentTool) resolveTarget(ctx context.Context, id string) (*types.Knowledge, *types.Chunk, error) {
	if knowledge, err := t.knowledgeService.GetKnowledgeByIDOnly(ctx, id); err == nil && knowledge != nil {
		authorized, authErr := authorizeLoadedKnowledge(ctx, t.searchTargets, knowledge, t.knowledgeService)
		if authErr != nil {
			return nil, nil, fmt.Errorf("document is not accessible: %w", authErr)
		}
		return authorized, nil, nil
	}
	chunk, err := t.chunkService.GetChunkByIDOnly(ctx, id)
	if err != nil || chunk == nil {
		return nil, nil, fmt.Errorf(
			"id %s is neither a document (doc://N) nor a chunk (chunk://N) in the current scope", id)
	}
	authorizedChunk, authErr := authorizeChunkInSearchTargets(
		ctx, t.searchTargets, id, t.chunkService, t.knowledgeService,
	)
	if authErr != nil {
		return nil, nil, fmt.Errorf("chunk is not accessible: %w", authErr)
	}
	if authorizedChunk.ChunkType == types.ChunkTypeWikiPage || strings.HasPrefix(authorizedChunk.ID, "wp-") {
		const wikiChunkPrefix = "wp-"
		if t.wikiPageService == nil || !strings.HasPrefix(authorizedChunk.ID, wikiChunkPrefix) || len(authorizedChunk.ID) == len(wikiChunkPrefix) {
			return nil, nil, errors.New("Wiki page chunks must be read through the Wiki page tools")
		}
		pageID := strings.TrimPrefix(authorizedChunk.ID, wikiChunkPrefix)
		page, pageErr := t.wikiPageService.GetPageByID(ctx, pageID)
		if pageErr != nil || page == nil || page.ID != pageID ||
			page.KnowledgeBaseID != authorizedChunk.KnowledgeBaseID || page.TenantID != authorizedChunk.TenantID {
			return nil, nil, errors.New("Wiki page chunk identity could not be verified; read the current Wiki page instead")
		}
		state := types.ReadWikiRetractionState(page)
		if len(state.PendingSourceIDs) > 0 || len(state.FailedSourceIDs) > 0 {
			return nil, nil, errors.New("Wiki page chunk is withheld while source retraction review is pending or failed; consult current source documents")
		}
		return nil, nil, errors.New("Wiki page chunks must be read through the Wiki page tools")
	}
	knowledge, err := t.knowledgeService.GetKnowledgeByIDOnly(ctx, authorizedChunk.KnowledgeID)
	if err != nil || knowledge == nil {
		// The chunk is authorized; a missing parent row only costs the header.
		knowledge = &types.Knowledge{
			ID:              authorizedChunk.KnowledgeID,
			TenantID:        authorizedChunk.TenantID,
			KnowledgeBaseID: authorizedChunk.KnowledgeBaseID,
		}
	}
	return knowledge, authorizedChunk, nil
}

func (t *ReadDocumentTool) tenantFor(knowledge *types.Knowledge) uint64 {
	if knowledge.TenantID != 0 {
		return knowledge.TenantID
	}
	return t.searchTargets.GetTenantIDForKB(knowledge.KnowledgeBaseID)
}

// listChunks fetches one page of text/FAQ chunks in document order.
func (t *ReadDocumentTool) listChunks(
	ctx context.Context, knowledge *types.Knowledge, page, pageSize int,
) ([]*types.Chunk, int64, error) {
	enabled := true
	chunks, total, err := t.chunkService.GetRepository().ListPagedChunksByKnowledgeID(
		ctx, t.tenantFor(knowledge), knowledge.ID,
		&types.Pagination{Page: page, PageSize: pageSize},
		[]types.ChunkType{types.ChunkTypeText, types.ChunkTypeFAQ},
		nil, "", "", "", "", &enabled,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list chunks: %w", err)
	}
	return chunks, total, nil
}

// countChunks returns the number of readable chunks without loading them.
func (t *ReadDocumentTool) countChunks(ctx context.Context, knowledge *types.Knowledge) int64 {
	_, total, err := t.listChunks(ctx, knowledge, 1, 1)
	if err != nil {
		return 0
	}
	return total
}

// fetchWindow returns chunks [offset, offset+limit) in document order. The
// repository pages by page number, so a window that does not start on a page
// boundary is assembled from the two pages that cover it.
func (t *ReadDocumentTool) fetchWindow(
	ctx context.Context, knowledge *types.Knowledge, offset, limit int,
) ([]*types.Chunk, int64, error) {
	if limit <= 0 {
		return nil, 0, nil
	}
	firstPage := offset/limit + 1
	skip := offset % limit
	chunks, total, err := t.listChunks(ctx, knowledge, firstPage, limit)
	if err != nil {
		return nil, 0, err
	}
	if skip > 0 {
		if skip < len(chunks) {
			chunks = chunks[skip:]
		} else {
			chunks = nil
		}
		// The next page exists whenever the first page did not already reach
		// the end of the document, regardless of how far the window extends.
		if int64(firstPage*limit) < total {
			next, _, err := t.listChunks(ctx, knowledge, firstPage+1, limit)
			if err != nil {
				return nil, 0, err
			}
			chunks = append(chunks, next...)
		}
	}
	if len(chunks) > limit {
		chunks = chunks[:limit]
	}
	return chunks, total, nil
}

// readPage renders metadata plus chunks [offset, offset+limit).
func (t *ReadDocumentTool) readPage(
	ctx context.Context, knowledge *types.Knowledge, offset, limit int,
) (*types.ToolResult, error) {
	chunks, total, err := t.fetchWindow(ctx, knowledge, offset, limit)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, err
	}
	if len(chunks) == 0 && total > 0 && int64(offset) >= total {
		suggested := int(total) - limit
		if suggested < 0 {
			suggested = 0
		}
		return &types.ToolResult{
			Success: false,
			Error: fmt.Sprintf(
				"offset %d is out of range: document has %d chunks (valid offsets 0..%d). Retry with offset=%d.",
				offset, total, total-1, suggested),
			Data: map[string]interface{}{
				"knowledge_id":     knowledge.ID,
				"total_chunks":     total,
				"requested_offset": offset,
				"requested_limit":  limit,
				"suggested_offset": suggested,
			},
		}, nil
	}
	chunks = fitChunksToBudget(chunks, OutputBudget(ctx)*4/5)
	enrichChunkImageInfo(ctx, t.chunkService.GetRepository(), t.tenantFor(knowledge), chunks)

	rows := make([]readChunkRow, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, readChunkRow{chunk: c})
	}
	rows = boundChunkRowsContent(ctx, rows)
	data := t.buildData(knowledge, total, rows)
	data["offset"] = offset
	data["page_size"] = limit
	data["page"] = offset/limit + 1
	data["offset_space"] = "document_chunks"
	annotateContentPagination(data, rows)
	if next := offset + len(chunks); int64(next) < total && len(chunks) > 0 {
		data["next_offset"] = next
	}
	return &types.ToolResult{
		Success: true,
		Output:  t.buildOutput(knowledge, total, rows, ""),
		Data:    data,
	}, nil
}

// readAroundChunk renders a single chunk with optional neighbours.
func (t *ReadDocumentTool) readAroundChunk(
	ctx context.Context, knowledge *types.Knowledge, focus *types.Chunk, contextChunks int,
) (*types.ToolResult, error) {
	total := t.countChunks(ctx, knowledge)
	rows := []readChunkRow{{chunk: focus, role: "focus"}}
	if contextChunks > 0 && focus.ChunkType != types.ChunkTypeFAQ {
		// Neighbours are resolved by chunk_index, not by list position:
		// parent, summary and image chunks share the index sequence, so a
		// position computed from ChunkIndex would drift away from the focus.
		neighbours, err := t.chunkService.GetRepository().ListChunkNeighbors(
			ctx, t.tenantFor(knowledge), knowledge.ID, focus.ChunkIndex, contextChunks, contextChunks,
			[]types.ChunkType{types.ChunkTypeText, types.ChunkTypeFAQ},
		)
		if err != nil {
			return &types.ToolResult{
				Success: false, Error: fmt.Sprintf("failed to list neighbouring chunks: %v", err),
			}, err
		}
		rows = rows[:0]
		placed := false
		for _, c := range neighbours {
			if !placed && c.ChunkIndex > focus.ChunkIndex {
				rows = append(rows, readChunkRow{chunk: focus, role: "focus"})
				placed = true
			}
			role := "context_after"
			if c.ChunkIndex < focus.ChunkIndex {
				role = "context_before"
			}
			rows = append(rows, readChunkRow{chunk: c, role: role})
		}
		if !placed {
			rows = append(rows, readChunkRow{chunk: focus, role: "focus"})
		}
	}
	chunks := make([]*types.Chunk, 0, len(rows))
	for _, r := range rows {
		chunks = append(chunks, r.chunk)
	}
	enrichChunkImageInfo(ctx, t.chunkService.GetRepository(), t.tenantFor(knowledge), chunks)
	rows = boundChunkRowsContent(ctx, rows)
	rows, omittedContext := fitChunkRowsToBudget(ctx, rows, focus.ID)

	data := t.buildData(knowledge, total, rows)
	annotateContentPagination(data, rows)
	data["single_chunk"] = len(rows) == 1
	data["focus_chunk_id"] = focus.ID
	if omittedContext > 0 {
		data["context_chunks_omitted_for_budget"] = omittedContext
	}
	if q := faqStandardQuestion(focus); q != "" {
		data["faq_id"] = focus.ID
		data["faq_question"] = q
	}
	return &types.ToolResult{
		Success: true,
		Output:  t.buildOutput(knowledge, total, rows, ""),
		Data:    data,
	}, nil
}

func maxChunkContentRunes(ctx context.Context) int {
	return max(1, min(readDocumentMaxContentRunes, OutputBudget(ctx)*3/4-readDocumentChunkOverhead*2))
}

func sliceChunkContent(ctx context.Context, chunk *types.Chunk, offset, requestedLimit int) (readChunkRow, error) {
	runes := []rune(chunk.Content)
	if offset < 0 || offset > len(runes) {
		return readChunkRow{}, fmt.Errorf("content_offset %d is outside chunk content length %d", offset, len(runes))
	}
	limit := requestedLimit
	if limit <= 0 || limit > readDocumentMaxContentRunes {
		limit = maxChunkContentRunes(ctx)
	}
	limit = min(limit, maxChunkContentRunes(ctx))
	end := min(len(runes), offset+limit)
	copy := *chunk
	copy.Content = string(runes[offset:end])
	row := readChunkRow{chunk: &copy, role: "focus", contentOffset: offset, contentComplete: end == len(runes)}
	if !row.contentComplete {
		row.nextContentOffset = end
	}
	return row, nil
}

// boundChunkRowsContent keeps one oversized source chunk readable and gives
// the caller a stable chunk://N + Unicode offset to retrieve its remaining source text.
func boundChunkRowsContent(ctx context.Context, rows []readChunkRow) []readChunkRow {
	limit := maxChunkContentRunes(ctx)
	for i, row := range rows {
		if row.chunk == nil || utf8.RuneCountInString(row.chunk.Content) <= limit {
			continue
		}
		segment, err := sliceChunkContent(ctx, row.chunk, 0, limit)
		if err != nil {
			continue
		}
		segment.role = row.role
		segment.snippet = row.snippet
		rows[i] = segment
	}
	return rows
}

func fitChunkRowsToBudget(ctx context.Context, rows []readChunkRow, focusID string) ([]readChunkRow, int) {
	budget := OutputBudget(ctx) * 4 / 5
	used := 0
	kept := make([]readChunkRow, 0, len(rows))
	keep := make([]bool, len(rows))
	focusIndex := -1
	for i, row := range rows {
		if row.chunk != nil && row.chunk.ID == focusID {
			focusIndex = i
			break
		}
	}
	if focusIndex >= 0 {
		row := rows[focusIndex]
		used = utf8.RuneCountInString(row.chunk.Content) + readDocumentChunkOverhead
		keep[focusIndex] = true
	}
	for i, row := range rows {
		if i == focusIndex || row.chunk == nil {
			continue
		}
		size := utf8.RuneCountInString(row.chunk.Content) + readDocumentChunkOverhead
		if used+size > budget {
			continue
		}
		used += size
		keep[i] = true
	}
	for i, row := range rows {
		if keep[i] {
			kept = append(kept, row)
		}
	}
	return kept, len(rows) - len(kept)
}

func annotateContentPagination(data map[string]interface{}, rows []readChunkRow) {
	if _, ok := data["offset_space"]; !ok {
		data["offset_space"] = "document_chunks"
	}
	_, pageScoped := data["page_chunks"]
	for i, row := range rows {
		chunks, _ := data["chunks"].([]map[string]interface{})
		if i >= len(chunks) {
			continue
		}
		chunks[i]["content_offset"] = row.contentOffset
		chunks[i]["content_complete"] = row.contentComplete || (row.contentOffset == 0 && row.nextContentOffset == 0)
		if pageScoped {
			chunks[i]["content_offset_space"] = "physical_page_chunk_unicode_runes"
		} else {
			chunks[i]["content_offset_space"] = "chunk_unicode_runes"
		}
		if row.nextContentOffset > 0 {
			chunks[i]["next_content_offset"] = row.nextContentOffset
		}
	}
}

func (t *ReadDocumentTool) readChunkContent(
	ctx context.Context, knowledge *types.Knowledge, chunk *types.Chunk, offset, limit int,
) (*types.ToolResult, error) {
	row, err := sliceChunkContent(ctx, chunk, offset, limit)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	enrichChunkImageInfo(ctx, t.chunkService.GetRepository(), t.tenantFor(knowledge), []*types.Chunk{row.chunk})
	rows := []readChunkRow{row}
	data := t.buildData(knowledge, t.countChunks(ctx, knowledge), rows)
	data["single_chunk"] = true
	data["offset_space"] = "chunk_unicode_runes"
	annotateContentPagination(data, rows)
	data["content_complete"] = row.contentComplete
	data["content_offset"] = row.contentOffset
	if !row.contentComplete {
		data["next_content_offset"] = row.nextContentOffset
	}
	return &types.ToolResult{Success: true, Data: data, Output: t.buildOutput(knowledge, int64(data["total_chunks"].(int64)), rows, "")}, nil
}

// readByQuery scans the document in order from offset and returns chunks
// matched by every matcher, with one chunk of context on each side, capped at
// readDocumentMaxMatches and the output budget. A truncated result carries
// next_offset, the reading-order position of the first match it left out;
// without it matches past the cap could only be reached by paging the whole
// document.
func (t *ReadDocumentTool) readByQuery(
	ctx context.Context, knowledge *types.Knowledge, query string, matchers []*regexp.Regexp, offset int, regexMode bool,
) (*types.ToolResult, error) {
	var rows []readChunkRow
	emitted := make(map[string]bool)
	matchCount := 0
	truncated := false
	var total int64
	var prev *types.Chunk
	forceNext := false
	// Start one chunk before offset so a match at offset still gets its
	// preceding context, also when offset is the first chunk of a page.
	page := max(offset-1, 0)/readDocumentScanPageSize + 1
	nextOffset := -1
	tenantID := t.tenantFor(knowledge)
	// Stop collecting before the registry's head/tail truncation would cut
	// rows in the middle; the header and tags take the remaining share.
	budget := OutputBudget(ctx) * 4 / 5
	used := 0
	emit := func(row readChunkRow) {
		rows = append(rows, row)
		emitted[row.chunk.ID] = true
		used += utf8.RuneCountInString(row.chunk.Content) + readDocumentChunkOverhead
	}

scan:
	for {
		chunks, pageTotal, err := t.listChunks(ctx, knowledge, page, readDocumentScanPageSize)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, err
		}
		total = pageTotal
		if len(chunks) == 0 {
			break
		}
		enrichChunkImageInfo(ctx, t.chunkService.GetRepository(), tenantID, chunks)
		for i, c := range chunks {
			position := (page-1)*readDocumentScanPageSize + i
			if position < offset {
				// Before the requested start: only its text as context.
				prev = c
				continue
			}
			haystack := enrichChunkContent(c)
			if q := faqStandardQuestion(c); q != "" {
				haystack += "\n" + q
			}
			if matchesAll(matchers, haystack) {
				if matchCount >= readDocumentMaxMatches || used >= budget {
					truncated = true
					nextOffset = position
					break scan
				}
				matchCount++
				if prev != nil && !emitted[prev.ID] {
					emit(readChunkRow{chunk: prev, role: "context_before"})
				}
				if !emitted[c.ID] {
					emit(readChunkRow{chunk: c, role: "match", snippet: extractChunkMatchSnippet(c, matchers)})
				}
				forceNext = true
			} else if forceNext {
				if !emitted[c.ID] && used < budget {
					emit(readChunkRow{chunk: c, role: "context_after"})
				}
				forceNext = false
			}
			prev = c
		}
		if int64(page*readDocumentScanPageSize) >= total {
			break
		}
		page++
	}

	data := t.buildData(knowledge, total, rows)
	rows = boundChunkRowsContent(ctx, rows)
	data = t.buildData(knowledge, total, rows)
	annotateContentPagination(data, rows)
	data["query"] = query
	mode := "literal_all_terms_in_one_chunk"
	scope := map[string]any{"mode": mode, "offset": offset, "scan_complete": !truncated, "document_scan_complete": offset == 0 && !truncated,
		"notice": "Matches describe this text query only, not semantic coverage of the topic. No match does not prove the document lacks the requested information; terms may be worded differently or occur in different chunks. Choose another query or read actual source ranges when needed."}
	if regexMode {
		scope["mode"] = "regular_expression_in_one_chunk"
	} else {
		scope["literal_terms"] = strings.Fields(query)
	}
	data["query_scope"] = scope
	data["match_count"] = matchCount
	data["truncated"] = truncated
	if nextOffset >= 0 {
		data["next_offset"] = nextOffset
	}
	scopeJSON, _ := json.Marshal(scope)
	return &types.ToolResult{
		Success: true,
		Output:  t.buildOutput(knowledge, total, rows, query) + "\n<query_scope>" + xmlEscape(string(scopeJSON)) + "</query_scope>",
		Data:    data,
	}, nil
}

// fitChunksToBudget keeps the leading chunks whose rendered size fits in
// budget runes. The model sees the result rendered from Data, which the
// registry's Output truncation never reaches, so a page must be bounded here;
// the first chunk is always kept so paging makes progress.
func fitChunksToBudget(chunks []*types.Chunk, budget int) []*types.Chunk {
	used := 0
	for i, c := range chunks {
		used += utf8.RuneCountInString(c.Content) + readDocumentChunkOverhead
		if used > budget && i > 0 {
			return chunks[:i]
		}
	}
	return chunks
}

func matchesAll(matchers []*regexp.Regexp, s string) bool {
	for _, m := range matchers {
		if !m.MatchString(s) {
			return false
		}
	}
	return true
}

// documentInfo is the metadata header shared by every read mode.
func documentInfo(knowledge *types.Knowledge, total int64) map[string]interface{} {
	info := map[string]interface{}{
		"knowledge_id": knowledge.ID,
		"title":        knowledge.Title,
		"type":         knowledge.Type,
		"source":       formatSource(knowledge.Type, knowledge.Source),
		"parse_status": knowledge.ParseStatus,
		"chunk_count":  total,
	}
	if knowledge.Description != "" {
		info["description"] = knowledge.Description
	}
	if knowledge.FileName != "" {
		info["file_name"] = knowledge.FileName
		info["file_type"] = knowledge.FileType
		info["file_size"] = formatFileSize(knowledge.FileSize)
	}
	if knowledge.Metadata != nil {
		if metadata, err := knowledge.Metadata.Map(); err == nil && len(metadata) > 0 {
			info["metadata"] = metadata
		}
	}
	return info
}

func (t *ReadDocumentTool) buildData(
	knowledge *types.Knowledge, total int64, rows []readChunkRow,
) map[string]interface{} {
	formatted := make([]map[string]interface{}, 0, len(rows))
	chunkIDs := make([]string, 0, len(rows))
	sourceChunkID := ""
	for i, r := range rows {
		chunkIDs = append(chunkIDs, r.chunk.ID)
		if sourceChunkID == "" && len(r.chunk.SourceLocators) > 0 {
			sourceChunkID = r.chunk.ID
		}
		row := chunkDataMap(i+1, r.chunk)
		if row["knowledge_id"] == "" {
			row["knowledge_id"] = knowledge.ID
		}
		if r.role != "" {
			row["role"] = r.role
		}
		if r.snippet != "" {
			row["match_snippet"] = r.snippet
		}
		formatted = append(formatted, row)
	}
	return map[string]interface{}{
		"display_type":      "knowledge_chunks_list",
		"knowledge_id":      knowledge.ID,
		"knowledge_base_id": knowledge.KnowledgeBaseID,
		"knowledge_title":   strings.TrimSpace(knowledge.Title),
		// Keep citation identities when chunks are omitted from SSE/history.
		"chunk_ids":       chunkIDs,
		"source_chunk_id": sourceChunkID,
		"total_chunks":    total,
		"fetched_chunks":  len(formatted),
		"chunks":          formatted,
		"document":        documentInfo(knowledge, total),
	}
}

func (t *ReadDocumentTool) buildOutput(
	knowledge *types.Knowledge, total int64, rows []readChunkRow, query string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<document knowledge_id=\"%s\" title=\"%s\" total_chunks=\"%d\" fetched=\"%d\"",
		xmlEscape(knowledge.ID), xmlEscape(strings.TrimSpace(knowledge.Title)), total, len(rows))
	if knowledge.Type != "" {
		fmt.Fprintf(&b, " source=\"%s\"", xmlEscape(formatSource(knowledge.Type, knowledge.Source)))
	}
	if knowledge.FileType != "" {
		fmt.Fprintf(&b, " file_type=\"%s\"", xmlEscape(knowledge.FileType))
	}
	if knowledge.ParseStatus != "" {
		fmt.Fprintf(&b, " parse_status=\"%s\"", xmlEscape(formatParseStatus(knowledge.ParseStatus)))
	}
	b.WriteString(">\n")
	if knowledge.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>\n", xmlEscape(knowledge.Description))
	}
	if knowledge.Metadata != nil {
		if metadata, err := knowledge.Metadata.Map(); err == nil && len(metadata) > 0 {
			b.WriteString("<metadata>")
			first := true
			for key, value := range metadata {
				if !first {
					b.WriteString("; ")
				}
				first = false
				fmt.Fprintf(&b, "%s: %v", xmlEscape(key), value)
			}
			b.WriteString("</metadata>\n")
		}
	}
	if query != "" {
		fmt.Fprintf(&b, "<query>%s</query>\n", xmlEscape(query))
	}
	for _, r := range rows {
		extra := ""
		if r.role != "" {
			extra = fmt.Sprintf(" role=\"%s\"", r.role)
		}
		if r.snippet != "" {
			fmt.Fprintf(&b, "<match_snippet>%s</match_snippet>\n", xmlEscape(r.snippet))
		}
		writeChunkXML(&b, r.chunk, extra)
	}
	b.WriteString("</document>")
	return b.String()
}

func formatSource(knowledgeType, source string) string {
	switch knowledgeType {
	case "file":
		return "File Upload"
	case "url":
		return fmt.Sprintf("URL: %s", source)
	case "passage":
		return "Text Input"
	default:
		return knowledgeType
	}
}

func formatFileSize(size int64) string {
	if size == 0 {
		return "Unknown"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatParseStatus(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "processing":
		return "Processing"
	case "completed", "success":
		return "Completed"
	case "failed":
		return "Failed"
	default:
		return status
	}
}
