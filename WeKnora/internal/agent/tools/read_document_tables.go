package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

const documentTableCandidateLimit = 12

type documentTableLocation struct {
	SectionTitle      string   `json:"section_title,omitempty"`
	Title             string   `json:"title,omitempty"`
	Page              int      `json:"page"`
	ObjectID          string   `json:"object_id"`
	FigureID          string   `json:"figure_id,omitempty"`
	TitleChunkID      string   `json:"title_chunk_id,omitempty"`
	BodyChunkID       string   `json:"body_chunk_id"`
	OCRChunkID        string   `json:"ocr_chunk_id,omitempty"`
	GridIndex         int      `json:"grid_index"`
	ColumnHeaders     []string `json:"column_headers"`
	RowLabels         []string `json:"row_labels"`
	RowPeriodLabels   []string `json:"row_period_labels"`
	GridComplete      bool     `json:"grid_complete"`
	MetadataTruncated bool     `json:"metadata_truncated,omitempty"`
	TitlePointer      string   `json:"title_pointer,omitempty"`
	BodyPointer       string   `json:"body_pointer,omitempty"`
	Unit              string   `json:"unit,omitempty"`
	UnitPointer       string   `json:"unit_pointer,omitempty"`
	DisclosureDate    string   `json:"disclosure_date,omitempty"`
	DisclosureLabel   string   `json:"disclosure_label,omitempty"`
	DisclosureChunkID string   `json:"disclosure_chunk_id,omitempty"`
	DisclosurePage    int      `json:"disclosure_page,omitempty"`
	DisclosurePointer string   `json:"disclosure_pointer,omitempty"`
	CoverDate         string   `json:"cover_date,omitempty"`
	CoverDateLabel    string   `json:"cover_date_label,omitempty"`
	CoverDateChunkID  string   `json:"cover_date_chunk_id,omitempty"`
	CoverDatePage     int      `json:"cover_date_page,omitempty"`
	CoverDatePointer  string   `json:"cover_date_pointer,omitempty"`
	titleLine         int
	ReadArgs          *ReadDocumentInput `json:"read_args,omitempty"`
}

type documentTableText struct {
	page, index       int
	id, text, pointer string
}

type documentTableLabel struct {
	text, chunkID, pointer string
	page                   int
	line                   int
}

type documentTableAnchor struct {
	page        int
	parent, ref string
}

type documentTableSection struct {
	label     documentTableLabel
	readBound bool
}

type documentTableSource struct {
	documentTableText
	ocrID  string
	anchor documentTableAnchor
}

type documentTableGridSource struct {
	source documentTableSource
	title  documentTableLabel
	grid   DocumentTableGrid
	index  int
}

var (
	documentTableDateToken   = `[0-9]{4}[-/.年][0-9]{1,2}[-/.月][0-9]{1,2}日?`
	documentDisclosureLabel  = regexp.MustCompile(`(?i)^(?:发布日期|披露日期|发布时间|publication date|disclosure date|published(?: on| at)?)\s*[:：]\s*(` + documentTableDateToken + `)\s*$`)
	documentDatedCover       = regexp.MustCompile(`^(.{1,120})[ \t]+(` + documentTableDateToken + `)\s*$`)
	documentTablePeriodLabel = regexp.MustCompile(`^(?:[0-9]{4}(?:年|[-/.年](?:0?[1-9]|1[0-2])月?(?:[-/.月](?:0?[1-9]|[12][0-9]|3[01])日?)?)?|[0-9]{2,4}[/–-][0-9]{2,4}(?:年度?)?|(?:0?[1-9]|1[0-2])月)$`)
	documentTableUnitLabel   = regexp.MustCompile(`(?i)^(?:单位|units?)\s*[:：]\s*(\S(?:.{0,62}\S)?)\s*$`)
)

func (t *ReadDocumentTool) appendDocumentTableEvidence(ctx context.Context, k *types.Knowledge, input ReadDocumentInput, pages []int, matchers []*regexp.Regexp, limit int, inventory documentTableInventory, result *types.ToolResult) {
	if result == nil || !result.Success || result.Data == nil {
		return
	}
	if len(matchers) > 0 {
		t.appendDocumentTableCandidates(ctx, k, pages, matchers, input.TableCandidateOffset, limit, inventory, result)
		return
	}
	rows, _ := result.Data["chunks"].([]map[string]any)
	chunks := make([]documentTableText, 0, len(rows))
	for i, row := range rows {
		page, _ := row["page"].(int)
		index, _ := row["chunk_index"].(int)
		id, _ := row["chunk_id"].(string)
		body, _ := row["content"].(string)
		start, _ := row["content_offset"].(int)
		if page == 0 || len(pages) > 0 && !slices.Contains(pages, page) || id == "" || row["knowledge_id"] != k.ID ||
			row["knowledge_base"] != k.KnowledgeBaseID || row["content_complete"] == false || start != 0 {
			continue
		}
		chunks = append(chunks, documentTableText{page, index, id, body, fmt.Sprintf("/data/chunks/%d/content", i)})
	}
	grids, anchors := documentTableSections(chunks)
	var sources []documentTableSource
	ocr, _ := result.Data[types.DocumentImageTextEvidenceKey].([]documentImageTextEvidence)
	for i, text := range ocr {
		// A current source-bound OCR pointer cannot be created from the
		// catalog, image captions, or a truncated response.
		anchor := documentTableAnchor{text.Page, text.ParentChunkID, text.StoredRef}
		if _, ok := anchors[anchor]; !ok || text.Kind != types.ChunkTypeImageOCR ||
			text.TextTruncated || text.KnowledgeID != k.ID || text.ChildChunkID == "" {
			continue
		}
		sources = append(sources, documentTableSource{
			documentTableText: documentTableText{page: text.Page, id: text.ChunkID, text: text.Text,
				pointer: fmt.Sprintf("/data/%s/%d/text", types.DocumentImageTextEvidenceKey, i)},
			ocrID: text.ChildChunkID, anchor: anchor,
		})
	}
	locations := documentTableLocations(k, chunks, grids, anchors, sources)
	type section struct {
		page, line     int
		pointer, label string
	}
	titles := map[section]int{}
	for _, record := range locations {
		if record.GridComplete {
			titles[section{record.Page, record.titleLine, record.TitlePointer, record.Title}]++
		}
	}
	incompleteImages := result.Data["image_text_evidence_truncated"] == true || len(input.Images) > 0
	anchorCounts := map[section]map[string]bool{}
	if incompleteImages {
		for anchor, association := range anchors {
			label := association.label
			key := section{label.page, label.line, label.pointer, label.text}
			if anchorCounts[key] == nil {
				anchorCounts[key] = map[string]bool{}
			}
			anchorCounts[key][anchor.ref] = true
		}
	}
	locations = slices.DeleteFunc(locations, func(record documentTableLocation) bool {
		key := section{record.Page, record.titleLine, record.TitlePointer, record.Title}
		return !record.GridComplete || record.TitlePointer == "" || titles[key] != 1 || len(anchorCounts[key]) > 1
	})
	locations, truncated := boundDocumentTableLocations(locations, OutputBudget(ctx)/3)
	if len(locations) > 0 {
		result.Data["table_reads"] = locations
		appendDocumentTableOutput(result, "table_reads", locations)
	}
	if truncated {
		result.Data["table_reads_truncated"] = true
	}
}

// Recognize explicit parser page boundaries and verified page metadata. The
// same source chunk can contribute distinct text to distinct physical pages.
func documentTableChunkPages(chunk *types.Chunk, current int) ([]int, int) {
	pages := []int{}
	if current > 0 {
		pages = append(pages, current)
	}
	for _, match := range documentPageMarker.FindAllStringSubmatch(chunk.Content, -1) {
		current = pageNumberInMarker(match)
		if !slices.Contains(pages, current) {
			pages = append(pages, current)
		}
	}
	return pages, current
}

func documentTableSections(chunks []documentTableText) ([]documentTableGridSource, map[documentTableAnchor]documentTableSection) {
	var grids []documentTableGridSource
	anchors := make(map[documentTableAnchor]documentTableSection)
	slices.SortStableFunc(chunks, func(a, b documentTableText) int {
		return cmp.Or(cmp.Compare(a.page, b.page), cmp.Compare(a.index, b.index), cmp.Compare(a.id, b.id))
	})
	var title documentTableLabel
	var titleAnchors map[string]bool
	last := documentTableText{}
	for _, chunk := range chunks {
		if chunk.page != last.page || chunk.index != last.index+1 {
			title, titleAnchors = documentTableLabel{}, nil
		}
		lines := strings.Split(chunk.text, "\n")
		tables := ParseDocumentTableGrids(chunk.text)
		gridIndex := 0
		for i, raw := range lines {
			line := strings.TrimSpace(raw)
			for _, match := range documentMarkdownImage.FindAllStringSubmatch(line, -1) {
				if _, ok := types.ParseResourcePath(match[2]); ok {
					// An unlabeled neighboring figure cannot borrow a prior
					// chunk's title after another source object was anchored.
					bound := title.chunkID == chunk.id || len(titleAnchors) == 0 || titleAnchors[match[2]]
					anchors[documentTableAnchor{chunk.page, chunk.id, match[2]}] = documentTableSection{title, bound}
					if titleAnchors == nil {
						titleAnchors = make(map[string]bool)
					}
					titleAnchors[match[2]] = true
				}
			}
			next := ""
			for j := i + 1; j < len(lines) && j <= i+2; j++ {
				if next = strings.TrimSpace(lines[j]); next != "" {
					break
				}
			}
			beforeObject := documentMarkdownImage.MatchString(next) || strings.HasPrefix(next, "|")
			if titleText := documentTableHeading(line, lines, i, title.text == "" || beforeObject); titleText != "" {
				title = documentTableLabel{titleText, chunk.id, chunk.pointer, chunk.page, i}
				titleAnchors = nil
			}
			if gridIndex < len(tables) && tables[gridIndex].firstLine == i {
				grids = append(grids, documentTableGridSource{documentTableSource{documentTableText: chunk}, title, tables[gridIndex], gridIndex})
				gridIndex++
			}
		}
		last = chunk
	}
	return grids, anchors
}

func documentTableHeading(line string, lines []string, i int, allowStandalone bool) string {
	if line == "" || documentPageMarker.MatchString(line) || strings.HasPrefix(line, "<") ||
		strings.ContainsAny(line, "|\t") || documentDisclosureLabel.MatchString(line) || documentTableUnitLabel.MatchString(line) {
		return ""
	}
	if text := strings.TrimLeft(line, "#"); len(text) < len(line) && strings.HasPrefix(text, " ") {
		return strings.Trim(strings.TrimSpace(text), "*_ ")
	}
	// PPTX and historical text imports use a standalone slide title, so
	// accept the first separate source paragraph after a physical boundary.
	if allowStandalone && utf8.RuneCountInString(line) <= 160 &&
		(i == 0 || strings.TrimSpace(lines[i-1]) == "") &&
		(i+1 == len(lines) || strings.TrimSpace(lines[i+1]) == "") &&
		!strings.HasPrefix(line, "![") && !strings.HasPrefix(line, "来源：") && !strings.HasPrefix(line, "来源:") {
		return strings.Trim(line, "*_ ")
	}
	return ""
}

func documentTableLocations(k *types.Knowledge, chunks []documentTableText, grids []documentTableGridSource, anchors map[documentTableAnchor]documentTableSection, images []documentTableSource) []documentTableLocation {
	disclosure, coverDate := documentTableDisclosure(k, chunks)
	var locations []documentTableLocation
	add := func(source documentTableSource, title documentTableLabel, grid DocumentTableGrid, index int) {
		if len(grid.Rows) < 2 {
			return
		}
		sectionTitle := ""
		lines := strings.Split(source.text, "\n")
		if source.ocrID != "" {
			// An OCR grid-local label takes precedence over the enclosing
			// page section. The exact figure anchor still identifies the object.
			for i := grid.firstLine - 1; i >= max(0, grid.firstLine-2); i-- {
				line := strings.TrimSpace(lines[i])
				if line == "" || documentTableUnitLabel.MatchString(line) {
					continue
				}
				if !strings.ContainsAny(line, "|\t") && utf8.RuneCountInString(line) <= 160 {
					sectionTitle = title.text
					title = documentTableLabel{line, source.id, source.pointer, source.page, i}
				}
				break
			}
		}
		if title.text == "" || source.pointer != "" && source.ocrID != "" &&
			title.pointer != source.pointer && !anchors[source.anchor].readBound {
			return
		}
		objectID := source.anchor.ref
		if objectID == "" {
			objectID = fmt.Sprintf("%s#table-%d", source.id, index)
		}
		// An explicit table-local unit is independent of the period labels.
		unit := ""
		for i := grid.firstLine - 1; i >= max(0, grid.firstLine-2); i-- {
			if match := documentTableUnitLabel.FindStringSubmatch(strings.TrimSpace(lines[i])); len(match) > 0 {
				unit = match[1]
				break
			}
			if strings.TrimSpace(lines[i]) != "" {
				break
			}
		}
		location := documentTableLocation{
			Title: title.text, SectionTitle: sectionTitle, Page: source.page, FigureID: source.anchor.ref, ObjectID: objectID,
			TitleChunkID: title.chunkID, TitlePointer: title.pointer, titleLine: title.line, BodyChunkID: source.id, OCRChunkID: source.ocrID,
			GridIndex: index, BodyPointer: source.pointer, GridComplete: grid.ParseComplete,
			ColumnHeaders: grid.Rows[0], Unit: unit,
			RowLabels: []string{}, RowPeriodLabels: []string{},
			DisclosureDate: documentTableDate(disclosure.text), DisclosureLabel: disclosure.text,
			DisclosureChunkID: disclosure.chunkID, DisclosurePage: disclosure.page, DisclosurePointer: disclosure.pointer,
			CoverDate: documentTableDate(coverDate.text), CoverDateLabel: coverDate.text,
			CoverDateChunkID: coverDate.chunkID, CoverDatePage: coverDate.page, CoverDatePointer: coverDate.pointer,
		}
		if unit != "" {
			location.UnitPointer = source.pointer
		}
		for _, row := range grid.Rows[1:] {
			if len(row) > 0 && row[0] != "" {
				location.RowLabels = append(location.RowLabels, row[0])
				if documentTablePeriodLabel.MatchString(row[0]) {
					location.RowPeriodLabels = append(location.RowPeriodLabels, row[0])
				}
			}
		}
		locations = append(locations, location)
	}
	for _, grid := range grids {
		add(grid.source, grid.title, grid.grid, grid.index)
	}
	images = uniqueDocumentTableImages(images)
	for _, image := range images {
		for index, grid := range ParseDocumentTableGrids(image.text) {
			add(image, anchors[image.anchor].label, grid, index)
		}
	}
	slices.SortFunc(locations, func(a, b documentTableLocation) int {
		return cmp.Or(cmp.Compare(a.Page, b.Page), cmp.Compare(a.ObjectID, b.ObjectID), cmp.Compare(a.GridIndex, b.GridIndex), cmp.Compare(a.BodyChunkID, b.BodyChunkID))
	})
	return locations
}

func uniqueDocumentTableImages(images []documentTableSource) []documentTableSource {
	slices.SortFunc(images, func(a, b documentTableSource) int {
		return cmp.Or(cmp.Compare(a.anchor.page, b.anchor.page), cmp.Compare(a.anchor.ref, b.anchor.ref), cmp.Compare(a.ocrID, b.ocrID))
	})
	type figurePage struct {
		page int
		ref  string
	}
	seen := make(map[figurePage]string)
	for _, image := range images {
		key := figurePage{image.anchor.page, image.anchor.ref}
		if previous, ok := seen[key]; ok && previous != image.text {
			seen[key] = ""
		} else if !ok {
			seen[key] = image.text
		}
	}
	return slices.DeleteFunc(images, func(image documentTableSource) bool {
		key := figurePage{image.anchor.page, image.anchor.ref}
		keep := seen[key] != "" && seen[key] == image.text
		delete(seen, key)
		return !keep
	})
}

func documentTableDisclosure(k *types.Knowledge, chunks []documentTableText) (documentTableLabel, documentTableLabel) {
	metadata, _ := k.Metadata.Map()
	var disclosures, coverDates []documentTableLabel
	for _, key := range []string{"disclosure_date", "publication_date", "published_at"} {
		if raw, ok := metadata[key].(string); ok && documentTableDate(raw) != "" {
			disclosures = append(disclosures, documentTableLabel{text: raw, pointer: "/data/document/metadata/" + key})
		}
	}
	coverTitleSeen := false
	for _, chunk := range chunks {
		lines := strings.Split(chunk.text, "\n")
		for i, line := range lines {
			raw := strings.TrimSpace(line)
			label := strings.Trim(strings.TrimLeft(raw, "#"), "*_ ")
			if match := documentDisclosureLabel.FindStringSubmatch(label); len(match) > 0 && documentTableDate(match[1]) != "" {
				disclosures = append(disclosures, documentTableLabel{match[1], chunk.id, chunk.pointer, chunk.page, i})
			} else if chunk.page == 1 && !coverTitleSeen {
				if title := documentTableHeading(raw, lines, i, true); title != "" {
					coverTitleSeen = true
					if match := documentDatedCover.FindStringSubmatch(title); len(match) > 0 && documentTableDate(match[2]) != "" {
						coverDates = append(coverDates, documentTableLabel{match[2], chunk.id, chunk.pointer, chunk.page, i})
					}
				}
			}
		}
	}
	return uniqueDocumentTableDate(disclosures), uniqueDocumentTableDate(coverDates)
}

func uniqueDocumentTableDate(labels []documentTableLabel) documentTableLabel {
	if len(labels) == 0 {
		return documentTableLabel{}
	}
	for _, label := range labels[1:] {
		if documentTableDate(label.text) != documentTableDate(labels[0].text) {
			return documentTableLabel{}
		}
	}
	return labels[0]
}

func documentTableDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if date, err := time.Parse(time.RFC3339, raw); err == nil {
		return date.Format(time.DateOnly)
	}
	date := strings.NewReplacer("年", "-", "月", "-", "日", "", ".", "-", "/", "-").Replace(raw)
	parsed, err := time.Parse("2006-1-2", date)
	if err != nil {
		return ""
	}
	return parsed.Format(time.DateOnly)
}

func boundDocumentTableLocations(locations []documentTableLocation, budget int) ([]documentTableLocation, bool) {
	for i := range locations {
		item := &locations[i]
		for _, labels := range []*[]string{&item.ColumnHeaders, &item.RowLabels, &item.RowPeriodLabels} {
			if len(*labels) > 32 {
				*labels, item.MetadataTruncated = (*labels)[:32], true
			}
		}
		// Keep candidates atomic. Very large headers still get a navigation
		// location, with the omitted preview explicitly marked.
		encoded, _ := json.Marshal(item)
		if len(encoded) > 2000 || utf8.RuneCount(encoded)+1 > budget && i == 0 {
			item.RowLabels, item.MetadataTruncated = nil, true
			encoded, _ = json.Marshal(item)
			if utf8.RuneCount(encoded)+1 > budget || len(encoded) > 2000 {
				item.ColumnHeaders, item.RowPeriodLabels = nil, nil
				encoded, _ = json.Marshal(item)
			}
		}
		budget -= utf8.RuneCount(encoded) + 1
		if budget < 0 {
			return locations[:i], true
		}
	}
	return locations, false
}

func appendDocumentTableOutput(result *types.ToolResult, key string, value any) {
	encoded, _ := json.Marshal(value)
	result.Output += "\n<" + key + " untrusted_source_content=\"true\">" + xmlEscape(string(encoded)) + "</" + key + ">"
}

func withDocumentTableParameter(tool BaseTool) BaseTool {
	if !DocumentTableEvidenceEnabled() {
		return tool
	}
	var schema map[string]any
	if json.Unmarshal(tool.schema, &schema) != nil {
		return tool
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return tool
	}
	properties["table_candidate_offset"] = map[string]any{"type": "integer", "minimum": 0,
		"description": "Continue navigation candidates with the same query and page scope using table_candidate_scope.next_offset. Independent of chunk offset."}
	schemaJSON, err := json.Marshal(schema)
	if err == nil {
		tool.schema = schemaJSON
	}
	tool.description += "\nWith a query, table_candidates lists candidate source pages and table or figure locators using the actual titles, headers and row labels. Inspect the chosen physical page with read_args. table_reads describes grids tied to the actual returned source bodies."
	return tool
}

// Full chunk reads can already establish a physical page through a parser
// boundary, an exact locator, or contiguous content in the same read window.
func annotateDocumentTablePages(k *types.Knowledge, result *types.ToolResult) {
	rows, _ := result.Data["chunks"].([]map[string]any)
	current, previousIndex := 0, -2
	for _, row := range rows {
		if page, _ := row["page"].(int); page > 0 {
			continue
		}
		index, _ := row["chunk_index"].(int)
		if index != previousIndex+1 {
			current = 0
		}
		previousIndex = index
		offset, _ := row["content_offset"].(int)
		if row["content_complete"] == false || offset != 0 {
			current = 0
			continue
		}
		start, _ := row["table_source_page_metadata"].(int)
		if start == 0 {
			start = exactDocumentTableLocatorPage(k, row)
		}
		if start == 0 {
			start = current
		}
		body, _ := row["content"].(string)
		pages, next := documentTableChunkPages(&types.Chunk{Content: body}, start)
		current = next
		markers := documentPageMarker.FindStringIndex(body)
		if start == 0 && markers != nil && strings.TrimSpace(body[:markers[0]]) != "" {
			continue
		}
		nonempty := slices.DeleteFunc(pages, func(page int) bool {
			text, _ := pageText(body, start, page)
			return text == ""
		})
		if len(nonempty) == 1 {
			row["page"] = nonempty[0]
			row["table_source_page"] = nonempty[0]
		}
	}
}

func exactDocumentTableLocatorPage(k *types.Knowledge, row map[string]any) int {
	locators, _ := row["source_locators"].(types.SourceLocators)
	page := 0
	for _, locator := range locators {
		candidate := locator.Page
		if locator.Type == types.SourceLocatorSlide {
			candidate = locator.Slide
		}
		if candidate == 0 || locator.Mapping != "exact" || locator.Partial ||
			(locator.SourceHash != "" && locator.SourceHash != k.FileHash) || (page != 0 && page != candidate) {
			return 0
		}
		page = candidate
	}
	return page
}
