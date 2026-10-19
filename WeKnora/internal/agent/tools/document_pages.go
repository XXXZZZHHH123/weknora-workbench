package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Markitdown's slide comment is the canonical PPT source boundary. Headings
// support other readers, but a bare mention of a page in body text is not one.
var documentPageMarker = regexp.MustCompile(`(?im)^\s*(?:<!--\s*(?:slide number|page)\s*:\s*([1-9][0-9]*)\s*-->|#{1,6}\s*(?:第\s*([1-9][0-9]*)\s*页|(?:slide|page)\s+([1-9][0-9]*))[^\n]*)`)

var imageSourcePageMarker = regexp.MustCompile(`(?im)(?:^\s*|来源[:：]\s*)第\s*([1-9][0-9]*)\s*页`)

func pageNumberInMarker(match []string) int {
	for _, value := range match[1:] {
		if value != "" {
			page, _ := strconv.Atoi(value)
			return page
		}
	}
	return 0
}

// pageText propagates the last explicit page boundary across chunk splits,
// while clipping a chunk containing multiple pages to the requested page.
func pageText(content string, current, requested int) (string, int) {
	markers := documentPageMarker.FindAllStringSubmatchIndex(content, -1)
	start := 0
	var selected strings.Builder
	for _, marker := range markers {
		if current == requested {
			selected.WriteString(content[start:marker[0]])
		}
		markerText := content[marker[0]:marker[1]]
		current = pageNumberInMarker(documentPageMarker.FindStringSubmatch(markerText))
		// Canonical HTML slide/page comments are parser boundary metadata and
		// must not create a marker-only pseudo-row. Markdown headings also carry
		// page numbers, but their remaining text is a real source title, so keep
		// the full heading in the selected page body.
		if strings.HasPrefix(strings.TrimSpace(markerText), "<!--") {
			start = marker[1]
		} else {
			start = marker[0]
		}
	}
	if current == requested {
		selected.WriteString(content[start:])
	}
	return strings.TrimSpace(selected.String()), current
}

func chunkMetadataPage(c *types.Chunk) int {
	metadata, err := c.Metadata.Map()
	if err != nil {
		return 0
	}
	for _, key := range []string{"page_number", "slide_number", "page"} {
		if value, ok := metadata[key]; ok {
			page, _ := strconv.Atoi(fmt.Sprint(value))
			if page > 0 {
				return page
			}
		}
	}
	return 0
}

// Child images may be attached to a parent spanning several pages. Keep only
// images whose parsed page label or actual URL belongs to the clipped segment.
func pageImages(info, content string, page int, clipped bool) string {
	var images []types.ImageInfo
	if json.Unmarshal([]byte(info), &images) != nil {
		return ""
	}
	selected := make([]types.ImageInfo, 0, len(images))
	for _, image := range images {
		// A URL in the clipped page is authoritative. Body text such as
		// "参见第13页" is not the owning page of a figure on page 12.
		mentioned := image.URL != "" && strings.Contains(content, image.URL) || image.OriginalURL != "" && strings.Contains(content, image.OriginalURL)
		label := imageSourcePageMarker.FindStringSubmatch(image.Caption + "\n" + image.OCRText)
		labelPage := 0
		if len(label) > 0 {
			labelPage = pageNumberInMarker(label)
		}
		if !mentioned && labelPage != 0 && labelPage != page {
			continue
		}
		if clipped && labelPage != page && !mentioned {
			continue
		}
		selected = append(selected, image)
	}
	encoded, _ := json.Marshal(selected)
	return string(encoded)
}

func (t *ReadDocumentTool) readPhysicalPage(ctx context.Context, knowledge *types.Knowledge, requested, offset, limit int, segment *chunkContentSegment) (*types.ToolResult, error) {
	var selected []*types.Chunk
	var total int64
	current := 0
	for page := 1; ; page++ {
		chunks, count, err := t.listChunks(ctx, knowledge, page, readDocumentScanPageSize)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, err
		}
		total = count
		enrichChunkImageInfo(ctx, t.chunkService.GetRepository(), t.tenantFor(knowledge), chunks)
		for _, c := range chunks {
			if metadataPage := chunkMetadataPage(c); metadataPage > 0 {
				current = metadataPage
			}
			content, nextPage := pageText(c.Content, current, requested)
			current = nextPage
			if content == "" {
				continue
			}
			copy := *c
			copy.Content = content
			copy.ImageInfo = pageImages(c.ImageInfo, content, requested, content != strings.TrimSpace(c.Content))
			selected = append(selected, &copy)
		}
		if len(chunks) == 0 || int64(page*readDocumentScanPageSize) >= total {
			break
		}
	}
	if len(selected) == 0 {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("指定文档第 %d 页未找到可读取的页码标记或内容。需要补齐该文档的页码解析；不能把第 %d 个分块当作第 %d 页，也不能引用其他报告的同名图。", requested, requested, requested), Data: map[string]interface{}{"knowledge_id": knowledge.ID, "requested_page": requested}}, nil
	}
	pageTotal := len(selected)
	if segment != nil {
		var target *types.Chunk
		for _, c := range selected {
			if c.ID == segment.chunkID {
				target = c
				break
			}
		}
		if target == nil {
			return &types.ToolResult{Success: false, Error: "the selected chunk does not contain content on the requested physical page"}, nil
		}
		row, err := sliceChunkContent(ctx, target, segment.offset, segment.limit)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		row.role = "requested_page"
		selected = []*types.Chunk{row.chunk}
		rows := []readChunkRow{row}
		data := t.buildData(knowledge, total, rows)
		data["page"] = requested
		data["page_chunks"] = pageTotal
		data["offset"] = offset
		data["offset_space"] = "physical_page_chunks"
		data["page_complete"] = row.contentComplete && row.contentOffset == 0 && pageTotal == 1 && offset == 0
		data["content_offset"] = row.contentOffset
		data["content_complete"] = row.contentComplete
		if !row.contentComplete {
			data["next_content_offset"] = row.nextContentOffset
		}
		data["document_scope"] = map[string]interface{}{"matched": true, "knowledge_id": knowledge.ID, "page": requested, "plan_validated": t.documentConstraint != nil}
		data["chunks"].([]map[string]interface{})[0]["page"] = requested
		data["chunks"].([]map[string]interface{})[0]["content_offset"] = row.contentOffset
		data["chunks"].([]map[string]interface{})[0]["content_complete"] = row.contentComplete
		data["chunks"].([]map[string]interface{})[0]["content_offset_space"] = "physical_page_chunk_unicode_runes"
		if !row.contentComplete {
			data["chunks"].([]map[string]interface{})[0]["next_content_offset"] = row.nextContentOffset
		}
		return &types.ToolResult{Success: true, Output: t.buildOutput(knowledge, total, rows, ""), Data: data}, nil
	}
	if offset >= pageTotal {
		return &types.ToolResult{Success: false, Error: "offset is outside the requested physical page", Data: map[string]interface{}{"page": requested, "page_chunks": pageTotal}}, nil
	}
	end := offset + limit
	if end > pageTotal {
		end = pageTotal
	}
	selected = fitChunksToBudget(selected[offset:end], OutputBudget(ctx)*4/5)
	rows := make([]readChunkRow, 0, len(selected))
	visual := false
	for _, c := range selected {
		rows = append(rows, readChunkRow{chunk: c, role: "requested_page"})
		if len(chunkImageList(c.ImageInfo)) > 0 || strings.Contains(c.Content, "![") {
			visual = true
		}
	}
	rows = boundChunkRowsContent(ctx, rows)
	data := t.buildData(knowledge, total, rows)
	data["page"] = requested
	data["page_chunks"] = pageTotal
	data["offset"] = offset
	pageComplete := offset == 0 && len(rows) == pageTotal
	for _, row := range rows {
		if !row.contentComplete && row.nextContentOffset > 0 {
			pageComplete = false
		}
	}
	data["page_complete"] = pageComplete
	data["offset_space"] = "physical_page_chunks"
	annotateContentPagination(data, rows)
	data["document_scope"] = map[string]interface{}{"matched": true, "knowledge_id": knowledge.ID, "page": requested, "has_visual_evidence": visual, "plan_validated": t.documentConstraint != nil}
	if next := offset + len(rows); next < pageTotal {
		data["next_offset"] = next
		data["truncated"] = true
	}
	for _, row := range data["chunks"].([]map[string]interface{}) {
		row["page"] = requested
	}
	output := fmt.Sprintf("指定文档《%s》第 %d 页（真实页码，非分块序号）。只使用本页证据；图表数值为估读时须说明。\n", knowledge.Title, requested) +
		"保留原图标签与明确的原文定义。业务年度、日期和统计口径必须有实际来源依据，不能从标签外形猜测。" +
		"各曲线自身绘制的最右端可能属于不同日期；比较这些端点时必须说各自端点，不能称为同日或同期的准确排名。" +
		"分别核对图例总条数和排除所问曲线后的其他条数；没有读到的年份或时点不补编。\n" + t.buildOutput(knowledge, total, rows, "")
	return &types.ToolResult{Success: true, Output: output, Data: data}, nil
}
