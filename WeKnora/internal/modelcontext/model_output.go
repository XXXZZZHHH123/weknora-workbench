package modelcontext

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	modelWebSearchEvidenceMaxRunes = 1500
	modelWebFetchSummaryMaxRunes   = 4000
	modelWebFetchContentMaxRunes   = 8000
	modelWebFetchTotalMaxRunes     = 16000
)

// ModelOutput returns a compact, source-centric representation for the LLM.
// The canonical ToolResult.Output remains untouched for UI, logs and storage.
func (r *sourceRegistry) ModelOutput(result *types.ToolResult) string {
	if result == nil {
		return ""
	}
	// Legacy-format output from a current source tool is evidence too. Replay
	// registration alone must never grant this eligibility.
	if result.Success {
		r.registerLegacyToolReferences(result.Output, true)
		copyResult := *result
		copyResult.Output = r.CompactPublicCitations(result.Output, true)
		result = &copyResult
	}
	displayType := stringValue(result.Data, "display_type")
	if displayType == "web_fetch_results" {
		return r.modelWebFetchOutput(mapsValue(result.Data["results"]), result.Output)
	}
	if !result.Success {
		return failedToolModelText(result.Output, result.Error)
	}
	switch displayType {
	case "grep_results":
		return r.modelKnowledgeOutput("keyword", mapsValue(result.Data["chunk_results"]), result.Output)
	case "search_results":
		// search_knowledge reports the mode it actually used; legacy
		// knowledge_search payloads carry none and were always semantic.
		mode := stringValue(result.Data, "mode")
		if mode == "" {
			mode = "semantic"
		}
		output := r.modelKnowledgeOutput(mode, mapsValue(result.Data["results"]), result.Output)
		if scope := r.documentScopeFooter(result.Data); scope != "" && strings.HasSuffix(output, "</retrieval>") {
			output = strings.TrimSuffix(output, "</retrieval>") + scope + "</retrieval>"
		}
		return annotateSearchNotes(r.annotateModeFallbacks(output, result.Data), result.Data)
	case "knowledge_chunks_list":
		output := r.modelKnowledgeChunksOutput(result.Data, result.Output)
		if scope, ok := result.Data["query_scope"].(map[string]any); ok {
			if encoded, err := json.Marshal(scope); err == nil {
				output += "\n<query_scope>" + escapeText(string(encoded)) + "</query_scope>"
			}
		}
		return r.withDocumentImageEvidence(output, result.Data)
	case "document_info":
		return r.modelDocumentInfoOutput(result.Data, result.Output)
	case "graph_query_results":
		return annotateGraphResult(
			r.modelKnowledgeOutput("graph", mapsValue(result.Data["results"]), result.Output), result.Data)
	case "web_search_results":
		return annotateWebSearchNotes(
			r.modelWebSearchOutput(mapsValue(result.Data["results"]), result.Output), result.Data)
	case "database_query":
		return r.modelDatabaseQueryOutput(mapsValue(result.Data["rows"]), result.Output)
	default:
		r.registerLabeledReferences(result.Output)
		r.registerStructuredReferences(result.Output)
		return r.CompactKnownText(result.Output)
	}
}

// The source codec rebuilds document replies from structured rows, so new
// image evidence must survive here as well as in the canonical raw Output.
// Preserve actual selection metadata and original geometry; do not turn the
// longer raw JSON sample array into a second copy of the readable evidence.
func (r *sourceRegistry) withDocumentImageEvidence(output string, data map[string]any) string {
	view := map[string]any{}
	for _, key := range []string{"image_catalog", "image_evidence", "image_read_error"} {
		if value, ok := data[key]; ok {
			encoded, err := json.Marshal(value)
			if err == nil && string(encoded) != "[]" && string(encoded) != "null" && string(encoded) != `""` {
				view[key] = value
			}
		}
	}
	figures := []map[string]any{}
	var figureText strings.Builder
	for _, figure := range mapsValue(data["image_geometry_evidence"]) {
		item := map[string]any{}
		for _, key := range []string{"reference", "knowledge_id", "chunk_id", "page", "source_part"} {
			item[key] = figure[key]
		}
		evidence, _ := figure["evidence"].(map[string]any)
		content := map[string]any{}
		for _, key := range []string{"version", "source_file_sha256", "source_media_sha256", "source_page", "source_part", "source_labels"} {
			if value, ok := evidence[key]; ok {
				content[key] = value
			}
		}
		item["evidence"] = content
		figures = append(figures, item)
		if markdown, ok := evidence["markdown"].(string); ok && markdown != "" {
			fmt.Fprintf(&figureText, "\n<original_figure_geometry reference=\"%s\" chunk_id=\"%s\">\n%s\n</original_figure_geometry>\n",
				escapeAttr(stringValue(figure, "reference")), escapeAttr(stringValue(figure, "chunk_id")), escapeText(markdown))
		}
	}
	if len(figures) > 0 {
		view["image_geometry_evidence"] = figures
	}
	if len(view) == 0 {
		return output
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return output
	}
	footer := "\n<document_image_evidence untrusted_source_content=\"true\">" + string(encoded) + figureText.String() + "</document_image_evidence>\n" +
		"Original geometry uses verified drawing points and calibrated axes; reconcile the actual legend, units and drawn range with any fallible vision observation. " +
		"A catalog alone supplies no visual facts; failed reads supply no geometry.\n"
	footer = r.CompactKnownText(footer)
	if strings.HasSuffix(output, "</retrieval>") {
		return strings.TrimSuffix(output, "</retrieval>") + footer + "</retrieval>"
	}
	return output + footer
}

// registerStructuredReferences registers durable IDs found under explicitly
// labeled keys of a JSON tool result, using the same key dispatch as tool
// arguments. Non-JSON output is a no-op.
func (r *sourceRegistry) registerStructuredReferences(raw string) {
	var value interface{}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return
	}
	var walk func(string, interface{})
	walk = func(key string, value interface{}) {
		switch typed := value.(type) {
		case string:
			r.registerSourceIDByKey(key, typed, true)
		case []interface{}:
			for _, item := range typed {
				walk(key, item)
			}
		case map[string]interface{}:
			for childKey, item := range typed {
				walk(childKey, item)
			}
		}
	}
	walk("", value)
}

func (r *sourceRegistry) modelDatabaseQueryOutput(rows []map[string]interface{}, fallback string) string {
	for _, row := range rows {
		for key, raw := range row {
			if value, ok := raw.(string); ok {
				r.registerSourceIDByKey(key, value, true)
			}
		}
	}
	return r.CompactKnownText(fallback)
}

func (r *sourceRegistry) modelDocumentInfoOutput(data map[string]interface{}, fallback string) string {
	rows := mapsValue(data["documents"])
	if len(rows) == 0 {
		return r.CompactKnownText(fallback)
	}
	var b strings.Builder
	b.WriteString("<documents")
	if kbHandle := r.RegisterKnowledgeBase(stringValue(data, "knowledge_base_id")); kbHandle != "" {
		fmt.Fprintf(&b, " kb=\"%s\"", escapeAttr(kbHandle))
	}
	if total := intValue(data, "total_docs"); total > 0 {
		fmt.Fprintf(&b, " total=\"%d\"", total)
	}
	if page := intValue(data, "page"); page > 0 {
		fmt.Fprintf(&b, " page=\"%d\"", page)
	}
	if next := intValue(data, "next_page"); next > 0 {
		fmt.Fprintf(&b, " next_page=\"%d\"", next)
	}
	b.WriteString(">\n")
	count := 0
	for _, row := range rows {
		knowledgeID := stringValue(row, "knowledge_id")
		docHandle := r.RegisterDocument(knowledgeID)
		if boolValue(row, "is_faq") {
			chunkID := stringValue(row, "faq_id")
			if chunkID == "" {
				continue
			}
			title := firstNonEmpty(stringValue(row, "faq_question"), stringValue(row, "title"))
			chunkHandle := r.RegisterChunk(ChunkReference{
				ChunkID:       chunkID,
				KnowledgeID:   knowledgeID,
				DocumentTitle: title,
				ChunkType:     "faq",
			})
			fmt.Fprintf(&b, "  <document id=\"%s\" type=\"faq\">\n", escapeAttr(docHandle))
			fmt.Fprintf(&b, "    <chunk id=\"%s\" type=\"faq\">\n", escapeAttr(chunkHandle))
			if title != "" {
				fmt.Fprintf(&b, "      <question>%s</question>\n", escapeText(title))
			}
			for _, answer := range stringSliceValue(row["faq_answers"]) {
				fmt.Fprintf(&b, "      <answer>%s</answer>\n", escapeText(answer))
			}
			b.WriteString("    </chunk>\n  </document>\n")
			count++
			continue
		}

		if docHandle == "" {
			continue
		}
		fmt.Fprintf(&b, "  <document id=\"%s\"", escapeAttr(docHandle))
		if title := stringValue(row, "title"); title != "" {
			fmt.Fprintf(&b, " title=\"%s\"", escapeAttr(title))
		}
		if docType := stringValue(row, "type"); docType != "" {
			fmt.Fprintf(&b, " type=\"%s\"", escapeAttr(docType))
		}
		if fileType := stringValue(row, "file_type"); fileType != "" {
			fmt.Fprintf(&b, " file_type=\"%s\"", escapeAttr(fileType))
		}
		if chunkCount := intValue(row, "chunk_count"); chunkCount > 0 {
			fmt.Fprintf(&b, " chunk_count=\"%d\"", chunkCount)
		}
		if status := stringValue(row, "parse_status"); status != "" {
			fmt.Fprintf(&b, " parse_status=\"%s\"", escapeAttr(status))
		}
		if updated := stringValue(row, "updated_at"); len(updated) >= 10 {
			fmt.Fprintf(&b, " updated_at=\"%s\"", escapeAttr(updated[:10]))
		}
		b.WriteString(">\n")
		if fileName := stringValue(row, "file_name"); fileName != "" {
			fmt.Fprintf(&b, "    <source_file_name>%s</source_file_name>\n", escapeText(fileName))
		}
		if metadata, ok := row["metadata"].(map[string]interface{}); ok && len(metadata) > 0 {
			encoded, _ := json.Marshal(metadata)
			fmt.Fprintf(&b, "    <source_metadata>%s</source_metadata>\n", escapeText(string(encoded)))
		}
		if description := stringValue(row, "description"); description != "" {
			fmt.Fprintf(&b, "    <description>%s</description>\n", escapeText(description))
		}
		b.WriteString("  </document>\n")
		count++
	}
	if notice := stringValue(data, "notice"); notice != "" {
		fmt.Fprintf(&b, "  <hint>%s</hint>\n", escapeText(notice))
	}
	b.WriteString("</documents>")
	if count == 0 {
		return r.CompactKnownText(fallback)
	}
	return b.String()
}

type modelChunk struct {
	handle               string
	docHandle            string
	kbHandle             string
	title                string
	metadata             string
	chunkType            string
	index                int
	view                 string
	role                 string
	match                string
	content              string
	question             string
	answers              []string
	images               []map[string]interface{}
	docRealID            string
	kbRealID             string
	chunkReal            string
	inputOrder           int
	contentOffset        int
	nextContentOffset    int
	contentComplete      bool
	contentStatusPresent bool
	contentOffsetSpace   string
	page                 int
	sourceLocators       []map[string]interface{}
}

func (r *sourceRegistry) modelKnowledgeOutput(mode string, rows []map[string]interface{}, fallback string) string {
	chunks := r.modelChunksFromRows(mode, rows)
	if len(chunks) == 0 {
		return r.CompactKnownText(fallback)
	}
	return renderKnowledgeChunks(mode, chunks, nil)
}

func hasAnyKey(values map[string]interface{}, key string) bool {
	_, ok := values[key]
	return ok
}

// modelChunksFromRows registers every row's handles and converts it into the
// renderer's chunk model.
func (r *sourceRegistry) modelChunksFromRows(mode string, rows []map[string]interface{}) []modelChunk {
	chunks := make([]modelChunk, 0, len(rows))
	for idx, row := range rows {
		chunkID := firstNonEmpty(stringValue(row, "chunk_id"), stringValue(row, "faq_id"), stringValue(row, "id"))
		knowledgeID := stringValue(row, "knowledge_id")
		kbID := firstNonEmpty(stringValue(row, "knowledge_base_id"), stringValue(row, "knowledge_base"))
		title := firstNonEmpty(stringValue(row, "knowledge_title"), stringValue(row, "title"))
		if chunkID == "" {
			continue
		}
		chunkType := stringValue(row, "chunk_type")
		if stringValue(row, "faq_id") != "" && chunkType == "" {
			chunkType = "faq"
		}
		chunkIndex := intValue(row, "chunk_index")
		if chunkIndex == 0 {
			chunkIndex = intValue(row, "index")
		}
		chunkHandle := r.RegisterChunk(ChunkReference{
			ChunkID:         chunkID,
			KnowledgeID:     knowledgeID,
			KnowledgeBaseID: kbID,
			DocumentTitle:   title,
			ChunkIndex:      chunkIndex,
			ChunkType:       chunkType,
		})
		chunks = append(chunks, modelChunk{
			handle:               chunkHandle,
			docHandle:            r.RegisterDocument(knowledgeID),
			kbHandle:             r.RegisterKnowledgeBase(kbID),
			title:                title,
			metadata:             stringValue(row, "knowledge_metadata"),
			chunkType:            chunkType,
			index:                chunkIndex,
			view:                 viewForRow(row, mode),
			role:                 stringValue(row, "role"),
			match:                firstNonEmpty(stringValue(row, "match_snippet"), stringValue(row, "matched_content")),
			content:              stringValue(row, "content"),
			contentOffset:        intValue(row, "content_offset"),
			nextContentOffset:    intValue(row, "next_content_offset"),
			contentComplete:      boolValue(row, "content_complete"),
			contentStatusPresent: hasAnyKey(row, "content_complete"),
			contentOffsetSpace:   firstNonEmpty(stringValue(row, "content_offset_space"), "chunk_unicode_runes"),
			page:                 intValue(row, "page"),
			question:             firstNonEmpty(stringValue(row, "faq_question"), stringValue(row, "faq_standard_question")),
			answers:              stringSliceValue(row["faq_answers"]),
			images:               mapsValue(row["images"]),
			sourceLocators:       sourceLocatorMaps(row["source_locators"]),
			docRealID:            knowledgeID,
			kbRealID:             kbID,
			chunkReal:            chunkID,
			inputOrder:           idx,
		})
	}
	return chunks
}

const (
	modelSourceLocatorSummaryMaxBytes = 2048
	modelSourceLocatorSummaryMaxItems = 8
	modelSourceLocatorFieldMaxBytes   = 128
	modelSourceLocatorWrapperReserve  = 192
	modelSourceLocatorNoticeReserve   = 144
)

type sourceLocatorSummary struct {
	total   int
	shown   int
	omitted int
	items   []map[string]interface{}
}

type sourceLocatorProjection struct {
	summaries       map[int]sourceLocatorSummary
	omittedLocators int
	omittedChunks   int
}

// sourceLocatorMaps accepts typed parser results and JSON-shaped adapter values
// without exposing arbitrary row fields to the model.
func sourceLocatorMaps(value interface{}) []map[string]interface{} {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var locators []map[string]interface{}
	if err := json.Unmarshal(encoded, &locators); err != nil {
		return nil
	}
	return locators
}

// boundedSourceLocatorSummaries preserves source-position metadata only. It
// omits quote/body text and caps the actual rendered summary plus metadata
// overhead to a small fixed budget.
func boundedSourceLocatorSummaries(chunks []modelChunk) sourceLocatorProjection {
	projection := sourceLocatorProjection{summaries: map[int]sourceLocatorSummary{}}
	remainingBytes := modelSourceLocatorSummaryMaxBytes - modelSourceLocatorNoticeReserve
	remainingItems := modelSourceLocatorSummaryMaxItems
	for _, chunk := range chunks {
		locators := chunk.sourceLocators
		if len(locators) == 0 {
			continue
		}
		summary := sourceLocatorSummary{total: len(locators), items: []map[string]interface{}{}}
		wrapperReserved := false
		for _, raw := range locators {
			projected := projectSourceLocator(raw)
			item := renderSourceLocatorItem(projected)
			if projected == nil || remainingItems <= 0 {
				summary.omitted++
				projection.omittedLocators++
				continue
			}
			reserve := 0
			if !wrapperReserved {
				reserve = modelSourceLocatorWrapperReserve
			}
			if len(item)+reserve > remainingBytes {
				summary.omitted++
				projection.omittedLocators++
				continue
			}
			summary.items = append(summary.items, projected)
			summary.shown++
			wrapperReserved = true
			remainingItems--
			remainingBytes -= len(item) + reserve
		}
		if summary.shown == 0 {
			projection.omittedChunks++
		}
		projection.summaries[chunk.inputOrder] = summary
	}
	return projection
}

func projectSourceLocator(raw map[string]interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}
	fields := []string{"type", "mapping", "partial", "source_hash", "source_id", "page", "slide", "block", "sheet", "row_start", "row_end", "start", "end", "start_ms", "end_ms", "section", "title"}
	out := map[string]interface{}{}
	omittedFields := 0
	for _, key := range fields {
		value, ok := raw[key]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok && len(text) > modelSourceLocatorFieldMaxBytes {
			omittedFields++
			continue
		}
		switch value.(type) {
		case string, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			out[key] = value
		default:
			omittedFields++
		}
	}
	if omittedFields > 0 {
		out["omitted_fields"] = omittedFields
	}
	if len(out) == 0 {
		out["omitted_fields"] = len(raw)
	}
	return out
}

func renderSourceLocatorItem(locator map[string]interface{}) string {
	if locator == nil {
		return ""
	}
	keys := make([]string, 0, len(locator))
	for key := range locator {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("        <locator")
	for _, key := range keys {
		fmt.Fprintf(&b, " %s=\"%s\"", key, escapeAttr(fmt.Sprint(locator[key])))
	}
	b.WriteString(" />\n")
	return b.String()
}

func writeSourceLocatorSummary(b *strings.Builder, summary sourceLocatorSummary) {
	if summary.total == 0 || summary.shown == 0 {
		return
	}
	fmt.Fprintf(b, "      <source_locators total=\"%d\" shown=\"%d\" omitted=\"%d\" payload_budget_bytes=\"%d\">\n",
		summary.total, summary.shown, summary.omitted, modelSourceLocatorSummaryMaxBytes)
	for _, locator := range summary.items {
		b.WriteString(renderSourceLocatorItem(locator))
	}
	b.WriteString("      </source_locators>\n")
}

func writeSourceLocatorOmission(b *strings.Builder, projection sourceLocatorProjection) {
	if projection.omittedLocators == 0 {
		return
	}
	fmt.Fprintf(b, "  <source_locator_omission omitted_locators=\"%d\" omitted_chunks=\"%d\" payload_budget_bytes=\"%d\" />\n",
		projection.omittedLocators, projection.omittedChunks, modelSourceLocatorSummaryMaxBytes)
}

// renderPhysicalPageScope emits only an explicit page-level scope from
// successful read_document Data. A bare page number or user request cannot
// create page scope.
func renderPhysicalPageScope(data map[string]interface{}) string {
	complete, hasComplete := data["page_complete"].(bool)
	offsetSpace, hasOffsetSpace := data["offset_space"].(string)
	if !hasComplete && (!hasOffsetSpace || offsetSpace != "physical_page_chunks") {
		return ""
	}
	var b strings.Builder
	b.WriteString("<page_scope")
	if hasComplete {
		fmt.Fprintf(&b, " complete=\"%t\"", complete)
	}
	if hasOffsetSpace && offsetSpace != "" {
		fmt.Fprintf(&b, " offset_space=\"%s\"", escapeAttr(offsetSpace))
	}
	if _, exists := data["offset"]; exists {
		fmt.Fprintf(&b, " offset=\"%d\"", intValue(data, "offset"))
	}
	if _, exists := data["page_chunks"]; exists {
		fmt.Fprintf(&b, " page_chunks=\"%d\"", intValue(data, "page_chunks"))
	}
	if page := intValue(data, "page"); page > 0 && offsetSpace == "physical_page_chunks" {
		fmt.Fprintf(&b, " physical_page=\"%d\"", page)
	}
	if _, exists := data["next_offset"]; exists {
		fmt.Fprintf(&b, " next_offset=\"%d\"", intValue(data, "next_offset"))
	}
	b.WriteString(" />")
	return b.String()
}

// annotateModeFallbacks adds the requested mode and one <mode_fallback> per
// knowledge base that was searched with a different retrieval path.
func (r *sourceRegistry) annotateModeFallbacks(output string, data map[string]interface{}) string {
	fallbacks := mapsValue(data["mode_fallbacks"])
	requested := stringValue(data, "requested_mode")
	if len(fallbacks) == 0 || !strings.HasSuffix(output, "</retrieval>") {
		return output
	}
	if requested != "" {
		output = strings.Replace(output, "<retrieval ",
			fmt.Sprintf("<retrieval requested_mode=\"%s\" ", escapeAttr(requested)), 1)
	}
	var b strings.Builder
	for _, fb := range fallbacks {
		fmt.Fprintf(&b, "  <mode_fallback kb=\"%s\" mode=\"%s\" reason=\"%s\" />\n",
			escapeAttr(r.RegisterKnowledgeBase(stringValue(fb, "knowledge_base_id"))),
			escapeAttr(stringValue(fb, "mode")), escapeAttr(stringValue(fb, "reason")))
	}
	return strings.TrimSuffix(output, "</retrieval>") + b.String() + "</retrieval>"
}

// matchAddsToContent reports whether a match snippet tells the model
// anything the chunk's content does not: it does when there is no content
// (a snippet-only view), or when the snippet is not an excerpt of it.
// Rendering an excerpt next to the full content repeated up to 800
// characters per row.
func matchAddsToContent(match, content string) bool {
	if content == "" {
		return true
	}
	excerpt := strings.TrimSpace(match)
	for _, marker := range []string{"...", "…"} {
		excerpt = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(excerpt, marker), marker))
	}
	return excerpt != "" && !strings.Contains(content, excerpt)
}

// annotateGraphResult adds the graph relations, any per-knowledge-base
// failures, and any cap the graph tool hit to a graph query's chunk view. The
// first two lived only in Output, which the model never sees once there are
// chunk rows to render; the same goes for a truncated relation list, which
// without a marker reads as the entity's whole neighbourhood.
func annotateGraphResult(output string, data map[string]interface{}) string {
	relations := mapsValue(data["relations"])
	failures := stringSliceValue(data["errors"])
	truncation := graphTruncationNote(data, len(relations))
	if (len(relations) == 0 && len(failures) == 0 && truncation == "") ||
		!strings.HasSuffix(output, "</retrieval>") {
		return output
	}
	var b strings.Builder
	for _, rel := range relations {
		fmt.Fprintf(&b, "  <relation source=\"%s\" type=\"%s\" target=\"%s\" />\n",
			escapeAttr(stringValue(rel, "source")), escapeAttr(stringValue(rel, "type")),
			escapeAttr(stringValue(rel, "target")))
	}
	for _, failure := range failures {
		fmt.Fprintf(&b, "  <error>%s</error>\n", escapeText(failure))
	}
	b.WriteString(truncation)
	return strings.TrimSuffix(output, "</retrieval>") + b.String() + "</retrieval>"
}

// graphTruncationNote states inside the model's view the caps the graph query
// tool hit. shownRelations is how many relations that view carries; the tool
// reports the totals under relations_total / graph_chunks_total /
// query_terms_total. A validation-limit marker also travels independently of
// those counts, including when no valid evidence was found.
func graphTruncationNote(data map[string]interface{}, shownRelations int) string {
	totalRelations := intValue(data, "relations_total")
	totalChunks := intValue(data, "graph_chunks_total")
	fetchedChunks := totalChunks - intValue(data, "graph_chunks_omitted")
	totalTerms := intValue(data, "query_terms_total")
	validationLimited := boolValue(data, "graph_validation_truncated")
	if !validationLimited && totalRelations <= shownRelations && totalChunks <= fetchedChunks && totalTerms <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("  <graph_truncated")
	if validationLimited {
		fmt.Fprintf(&b, " validation_stopped=\"true\" candidates_validated_total=\"%d\" validation_limit_per_kb=\"%d\"",
			intValue(data, "graph_candidates_validated_total"), intValue(data, "graph_validation_limit_per_kb"))
	}
	if totalRelations > shownRelations || validationLimited {
		totalAttribute := "relations_total"
		if boolValue(data, "relations_total_is_lower_bound") {
			totalAttribute = "relations_total_at_least"
		}
		fmt.Fprintf(&b, " relations_shown=\"%d\" %s=\"%d\"", shownRelations, totalAttribute, totalRelations)
	}
	if totalChunks > fetchedChunks || validationLimited {
		totalAttribute := "chunks_total"
		if boolValue(data, "graph_chunks_total_is_lower_bound") {
			totalAttribute = "chunks_total_at_least"
		}
		fmt.Fprintf(&b, " chunks_fetched=\"%d\" %s=\"%d\"", fetchedChunks, totalAttribute, totalChunks)
	}
	if totalTerms > 0 {
		fmt.Fprintf(&b, " terms_shown=\"%d\" terms_total=\"%d\"",
			totalTerms-intValue(data, "query_terms_omitted"), totalTerms)
	}
	b.WriteString(">This graph query stopped at a result cap, so it is not the complete picture: ")
	if validationLimited {
		fmt.Fprintf(&b, "evidence validation stopped at %d candidates per knowledge base; "+
			"some candidate evidence was not checked. Found at least %d valid in-scope evidence chunks "+
			"and at least %d supported relations. ",
			intValue(data, "graph_validation_limit_per_kb"), totalChunks, totalRelations)
	}
	if totalRelations > shownRelations {
		b.WriteString("the relations listed are a subset of these entities' relations, ")
	}
	if totalChunks > fetchedChunks {
		b.WriteString("some of their source chunks are missing, ")
	}
	if totalTerms > 0 {
		b.WriteString("and some words of the query were never matched against entity names, ")
	}
	b.WriteString("so a relation may be absent only because it was dropped. Narrow the query to a single entity " +
		"name before concluding that a relation does not exist.</graph_truncated>\n")
	return b.String()
}

// annotateSearchNotes tells the model what a search result does not show:
// how many lower-ranked results were left out to fit the tool output budget
// (so it narrows the query or lowers the limit instead of concluding nothing
// else matched), how much of the ranked candidate pool the rendered passages
// stand for, and which knowledge bases could not be searched (so a failure is
// not read as an absence of evidence).
func annotateSearchNotes(output string, data map[string]interface{}) string {
	omitted := intValue(data, "omitted_for_budget")
	withheldWiki := intValue(data, "withheld_retracted_or_unverified_wiki_chunks")
	failures := stringSliceValue(data["partial_failures"])
	// retrieval_candidates is the candidate pool of the first ranked-filter
	// stage that dropped anything (rerank, merge or FILTER_TOP_K), not the
	// number of matches in the knowledge base, which retrieval never computes
	// at that depth; retrieval_shown is how many knowledge passages this view
	// renders out of that pool. Both count passages.
	shown := intValue(data, "retrieval_shown")
	candidates := intValue(data, "retrieval_candidates")
	truncated := shown > 0 && candidates > shown
	if (omitted <= 0 && withheldWiki <= 0 && len(failures) == 0 && !truncated) || !strings.HasSuffix(output, "</retrieval>") {
		return output
	}
	var b strings.Builder
	if withheldWiki > 0 {
		fmt.Fprintf(&b, "  <withheld_wiki count=\"%d\" reason=\"retraction_review\">Wiki page evidence was withheld because its current status could not be verified or review is pending. Consult current source documents; this does not prove the requested information is absent.</withheld_wiki>\n", withheldWiki)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "  <omitted count=\"%d\" reason=\"output_budget\">Lower-ranked results were left out to "+
			"fit the output size. Narrow the query or lower limit to see them.</omitted>\n", omitted)
	}
	if truncated {
		// The caveat is conditional on purpose: a truncated view is the norm
		// for a ranked retrieval, so an unconditional "the answer may be
		// incomplete" would add a disclaimer to ordinary factual answers that
		// the subset does not affect. Only counting and exhaustive-list
		// questions have to report the scope of what was read.
		fmt.Fprintf(&b, "  <subset shown=\"%d\" candidates=\"%d\">If the question asks for a count or an "+
			"exhaustive list, state that the provided context contains only the top %d of %d candidate "+
			"passages at this filtering stage, and do not treat these passage counts as the requested "+
			"total; otherwise answer normally without adding a disclaimer solely because of this "+
			"truncation.</subset>\n", shown, candidates, shown, candidates)
	}
	for _, failure := range failures {
		fmt.Fprintf(&b, "  <partial_failure>%s — these knowledge bases were not searched.</partial_failure>\n",
			escapeText(failure))
	}
	return strings.TrimSuffix(output, "</retrieval>") + b.String() + "</retrieval>"
}

// annotateWebSearchNotes restates a clamped count inside the retrieval block.
// web_search also writes the note into ToolResult.Output, but once there is at
// least one row the model only sees this rendered view, so the note has to
// travel in Data — the same reason annotateSearchNotes reads Data, not Output.
func annotateWebSearchNotes(output string, data map[string]interface{}) string {
	if !boolValue(data, "count_clamped") || !strings.HasSuffix(output, "</retrieval>") {
		return output
	}
	requested := intValue(data, "count_requested")
	maximum := intValue(data, "count_maximum")
	if requested < 1 || maximum < 1 {
		return output
	}
	note := fmt.Sprintf("  <count_clamped requested=\"%d\" maximum=\"%d\">The requested count is "+
		"outside 1-%d and was clamped to %d. Ask for a value in range, or omit count to use the "+
		"maximum.</count_clamped>\n", requested, maximum, maximum, maximum)
	return strings.TrimSuffix(output, "</retrieval>") + note + "</retrieval>"
}

func viewForRow(row map[string]interface{}, mode string) string {
	if stringValue(row, "content") != "" {
		return "full"
	}
	if mode == "deep_read" {
		return "full"
	}
	return "match"
}

func (r *sourceRegistry) modelKnowledgeChunksOutput(data map[string]interface{}, fallback string) string {
	rows := mapsValue(data["chunks"])
	title := stringValue(data, "knowledge_title")
	knowledgeID := stringValue(data, "knowledge_id")
	for _, row := range rows {
		if stringValue(row, "knowledge_id") == "" {
			row["knowledge_id"] = knowledgeID
		}
		if stringValue(row, "knowledge_title") == "" {
			row["knowledge_title"] = title
		}
	}
	var info map[string]interface{}
	if raw, ok := data["document"].(map[string]interface{}); ok {
		info = raw
	}
	if len(rows) == 0 {
		// A document that has no chunks (or a query with no matches) still
		// deserves its header so the model learns what it read.
		if info == nil {
			return r.CompactKnownText(fallback)
		}
		docHandle := r.RegisterDocument(knowledgeID)
		var b strings.Builder
		b.WriteString("<retrieval type=\"knowledge\" mode=\"deep_read\">\n")
		fmt.Fprintf(&b, "  <document id=\"%s\" title=\"%s\">\n", escapeAttr(docHandle), escapeAttr(title))
		writeDocumentInfo(&b, info)
		if pageScope := renderPhysicalPageScope(data); pageScope != "" {
			b.WriteString("    " + pageScope + "\n")
		}
		if query := stringValue(data, "query"); query != "" {
			fmt.Fprintf(&b, "    <matches query=\"%s\" count=\"0\" />\n", escapeAttr(query))
			b.WriteString("    <hint>No chunk contains every word of the query. Retry with fewer or different " +
				"words (the document's own language and terms), or read a chunk from search_knowledge " +
				"results with id=chunk://N and context.</hint>\n")
		}
		b.WriteString("  </document>\n</retrieval>")
		return b.String()
	}
	chunks := r.modelChunksFromRows("deep_read", rows)
	if len(chunks) == 0 {
		return r.CompactKnownText(fallback)
	}
	output := renderKnowledgeChunks("deep_read", chunks, info)
	if info == nil {
		// Legacy list_knowledge_chunks payloads carry no document header.
		remaining := intValue(data, "total_chunks") - intValue(data, "fetched_chunks")
		if remaining > 0 {
			output = strings.TrimSuffix(output, "</retrieval>")
			output += fmt.Sprintf("  <pagination remaining=\"%d\" page=\"%d\" page_size=\"%d\" />\n</retrieval>",
				remaining, intValue(data, "page"), intValue(data, "page_size"))
		}
		return output
	}
	var footer strings.Builder
	footer.WriteString(renderPhysicalPageScope(data))
	footer.WriteString(r.documentScopeFooter(data))
	if omitted := intValue(data, "context_chunks_omitted_for_budget"); omitted > 0 {
		fmt.Fprintf(&footer, "  <context_truncated omitted_chunks=\"%d\">Only the focus chunk and fitting neighbours are shown; do not treat omitted neighbours as read.</context_truncated>\n", omitted)
	}
	if query := stringValue(data, "query"); query != "" {
		fmt.Fprintf(&footer, "  <matches query=\"%s\" count=\"%d\"", escapeAttr(query), intValue(data, "match_count"))
		if boolValue(data, "truncated") {
			footer.WriteString(" truncated=\"true\"")
			if _, ok := data["next_offset"]; ok {
				fmt.Fprintf(&footer, " next_offset=\"%d\"", intValue(data, "next_offset"))
			}
		}
		footer.WriteString(" />\n")
	} else if _, ok := data["next_offset"]; ok {
		remaining := intValue(data, "total_chunks") - (intValue(data, "offset") + intValue(data, "fetched_chunks"))
		if pageTotal := intValue(data, "page_chunks"); pageTotal > 0 {
			remaining = pageTotal - (intValue(data, "offset") + intValue(data, "fetched_chunks"))
		}
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(&footer, "  <pagination next_offset=\"%d\" remaining=\"%d\" />\n",
			intValue(data, "next_offset"), remaining)
	}
	if footer.Len() > 0 {
		output = strings.TrimSuffix(output, "</retrieval>") + footer.String() + "</retrieval>"
	}
	return output
}

func (r *sourceRegistry) documentScopeFooter(data map[string]interface{}) string {
	scope, ok := data["document_scope"].(map[string]interface{})
	if !ok || !boolValue(scope, "matched") {
		return ""
	}
	document := escapeAttr(r.RegisterDocument(stringValue(scope, "knowledge_id")))
	page := intValue(scope, "page")
	var footer strings.Builder
	fmt.Fprintf(&footer, "  <document_scope document=\"%s\" matched=\"true\"", document)
	if page > 0 {
		fmt.Fprintf(&footer, " physical_page=\"%d\" visual_evidence=\"%t\"", page, boolValue(scope, "has_visual_evidence"))
	}
	footer.WriteString(" />\n")
	if page <= 0 {
		footer.WriteString("  <hint>Use only the requested document. Do not substitute evidence from another report with a similar title or chart name. No physical page was selected.</hint>\n")
		return footer.String()
	}
	footer.WriteString("  <hint>Use only the requested document and physical page. Do not replace this chart with a same-name chart from another report. If values are estimated from a chart, label them as approximate.</hint>\n")
	footer.WriteString("  <chart_interpretation>Preserve exact legend labels. A confirmed futures YYMM code describes contract year and delivery month; it does not establish a cross-year crop year, fiscal year or reporting period. Do not invent such a year range. Curves' own rightmost endpoints may have different dates: compare them as individual endpoints, never as an exact same-date or same-period ranking without source evidence. Count all legend lines separately from other lines after excluding the requested one. Answer each requested item only from the chart evidence; do not invent missing dates, years or values.</chart_interpretation>\n")
	if _, more := data["next_offset"]; more {
		fmt.Fprintf(&footer, "  <hint>This physical page is not fully read. Continue read_document(id=%s, page=%d, offset=%d); chart legends, tables or endpoint evidence may be in the remaining chunks. Do not treat omitted content as absent.</hint>\n", document, page, intValue(data, "next_offset"))
	}
	return footer.String()
}

// writeDocumentInfo renders the read_document metadata header as one
// <info> element under the document.
func writeDocumentInfo(b *strings.Builder, info map[string]interface{}) {
	if len(info) == 0 {
		return
	}
	// This header is rendered from Data, so a notice present only in the tool's
	// plain Output would never reach the model. Keep numbering systems distinct
	// without deriving physical coverage from source text or a partial read.
	b.WriteString("    <page_numbering>Chunk indexes, chunk_count and pagination offsets count text chunks, not physical pages. " +
		"Printed page numbers and table-of-contents ranges are source labels, which may differ from physical PDF pages. " +
		"A contents entry establishes only what the contents list; it does not establish that the referenced section or range is present in this file. " +
		"Use parser source_locators or an actual read_document page read to identify physical pages; describe directory-only evidence as a contents listing. " +
		"Use an explicit parser-provided physical page total when available; never infer the file's page count or physical range from chunk counts, printed labels, contents entries or the largest observed locator.</page_numbering>\n")
	b.WriteString("    <info")
	for _, key := range []string{"source", "file_name", "file_type", "file_size", "parse_status"} {
		if value := stringValue(info, key); value != "" {
			fmt.Fprintf(b, " %s=\"%s\"", key, escapeAttr(value))
		}
	}
	if count := intValue(info, "chunk_count"); count > 0 {
		fmt.Fprintf(b, " chunk_count=\"%d\"", count)
	}
	description := stringValue(info, "description")
	metadata, _ := info["metadata"].(map[string]interface{})
	if description == "" && len(metadata) == 0 {
		b.WriteString(" />\n")
		return
	}
	b.WriteString(">\n")
	if description != "" {
		fmt.Fprintf(b, "      <description>%s</description>\n", escapeText(description))
	}
	if len(metadata) > 0 {
		keys := make([]string, 0, len(metadata))
		for key := range metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("      <metadata>")
		for i, key := range keys {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(b, "%s: %v", escapeText(key), metadata[key])
		}
		b.WriteString("</metadata>\n")
	}
	b.WriteString("    </info>\n")
}

func renderKnowledgeChunks(mode string, chunks []modelChunk, info map[string]interface{}) string {
	type docGroup struct {
		handle   string
		realID   string
		kbHandle string
		title    string
		metadata string
		chunks   []modelChunk
		order    int
	}
	groupsByKey := make(map[string]*docGroup)
	var groups []*docGroup
	for _, chunk := range chunks {
		key := chunk.docHandle
		if key == "" {
			key = "chunk:" + chunk.handle
		}
		group := groupsByKey[key]
		if group == nil {
			group = &docGroup{
				handle: chunk.docHandle, realID: chunk.docRealID, kbHandle: chunk.kbHandle, title: chunk.title,
				metadata: chunk.metadata, order: chunk.inputOrder,
			}
			groupsByKey[key] = group
			groups = append(groups, group)
		} else if group.metadata == "" {
			group.metadata = chunk.metadata
		}
		group.chunks = append(group.chunks, chunk)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].order < groups[j].order })

	locatorProjection := boundedSourceLocatorSummaries(chunks)
	var b strings.Builder
	fmt.Fprintf(&b, "<retrieval type=\"knowledge\" mode=\"%s\">\n", escapeAttr(mode))
	for _, group := range groups {
		b.WriteString("  <document")
		if group.handle != "" {
			fmt.Fprintf(&b, " id=\"%s\"", escapeAttr(group.handle))
		}
		if group.kbHandle != "" {
			fmt.Fprintf(&b, " kb=\"%s\"", escapeAttr(group.kbHandle))
		}
		if group.title != "" {
			fmt.Fprintf(&b, " title=\"%s\"", escapeAttr(group.title))
		}
		b.WriteString(">\n")
		if group.metadata != "" {
			fmt.Fprintf(&b, "    <metadata>%s</metadata>\n", escapeText(group.metadata))
		}
		if info != nil && stringValue(info, "knowledge_id") == group.realID {
			writeDocumentInfo(&b, info)
		}
		for _, chunk := range group.chunks {
			fmt.Fprintf(&b, "    <chunk id=\"%s\" index=\"%d\" view=\"%s\"", chunk.handle, chunk.index, chunk.view)
			if chunk.chunkType != "" {
				fmt.Fprintf(&b, " type=\"%s\"", escapeAttr(chunk.chunkType))
			}
			if chunk.role != "" {
				fmt.Fprintf(&b, " role=\"%s\"", escapeAttr(chunk.role))
			}
			b.WriteString(">\n")
			if chunk.question != "" {
				fmt.Fprintf(&b, "      <question>%s</question>\n", escapeText(chunk.question))
			}
			// Deep reads keep the snippet: it locates the hit inside a long
			// chunk. Search rows are chunk-sized, so an excerpt of the content
			// shown beside it only repeats it.
			if chunk.match != "" && (mode == "deep_read" || matchAddsToContent(chunk.match, chunk.content)) {
				fmt.Fprintf(&b, "      <match>%s</match>\n", escapeText(chunk.match))
			}
			if chunk.content != "" {
				fmt.Fprintf(&b, "      <content>%s</content>\n", escapeText(chunk.content))
			}
			if chunk.contentStatusPresent {
				fmt.Fprintf(&b, "      <content_status complete=\"%t\" offset_space=\"%s\" offset=\"%d\"",
					chunk.contentComplete, escapeAttr(chunk.contentOffsetSpace), chunk.contentOffset)
				if chunk.page > 0 {
					fmt.Fprintf(&b, " physical_page=\"%d\"", chunk.page)
				}
				if !chunk.contentComplete {
					fmt.Fprintf(&b, " next_content_offset=\"%d\"", chunk.nextContentOffset)
				}
				b.WriteString(" />\n")
				if !chunk.contentComplete {
					fmt.Fprintf(&b, "      <continuation>Read the same source chunk id=%s with content_offset=%d",
						chunk.handle, chunk.nextContentOffset)
					if chunk.page > 0 {
						fmt.Fprintf(&b, " and page=%d", chunk.page)
					}
					b.WriteString(" to continue the original text; do not treat this excerpt as the complete chunk.</continuation>\n")
				}
			}
			if summary, exists := locatorProjection.summaries[chunk.inputOrder]; exists {
				writeSourceLocatorSummary(&b, summary)
			}
			for _, answer := range chunk.answers {
				fmt.Fprintf(&b, "      <answer>%s</answer>\n", escapeText(answer))
			}
			for _, image := range chunk.images {
				imageURL := stringValue(image, "url")
				if imageURL == "" {
					continue
				}
				caption := stringValue(image, "caption")
				fmt.Fprintf(&b, "      ![%s](%s)\n", escapeText(caption), escapeText(imageURL))
				if ocr := stringValue(image, "ocr_text"); ocr != "" {
					fmt.Fprintf(&b, "      <image_ocr>%s</image_ocr>\n", escapeText(ocr))
				}
			}
			b.WriteString("    </chunk>\n")
		}
		b.WriteString("  </document>\n")
	}
	writeSourceLocatorOmission(&b, locatorProjection)
	b.WriteString("</retrieval>")
	return b.String()
}

func (r *sourceRegistry) modelWebSearchOutput(rows []map[string]interface{}, fallback string) string {
	if len(rows) == 0 {
		return r.CompactKnownText(fallback)
	}
	var b strings.Builder
	b.WriteString("<retrieval type=\"web\" mode=\"search\" trust=\"untrusted\">\n")
	evidenceFields := 2
	for _, row := range rows {
		if boolValue(row, "page_verified") {
			evidenceFields = 3
			break
		}
	}
	perEvidence := min(modelWebSearchEvidenceMaxRunes, 16000/max(1, len(rows)*evidenceFields))
	count := 0
	for _, row := range rows {
		rawURL := stringValue(row, "url")
		if rawURL == "" {
			continue
		}
		handle := r.RegisterWeb(rawURL, stringValue(row, "title"))
		fmt.Fprintf(&b, "  <page id=\"%s\" title=\"%s\">\n", handle, escapeAttr(stringValue(row, "title")))
		b.WriteString("    <evidence type=\"search_summary\" verified=\"false\" />\n")
		if u, err := url.Parse(rawURL); err == nil {
			fmt.Fprintf(&b, "    <domain>%s</domain>\n", escapeText(u.Hostname()))
		}
		if snippet := stringValue(row, "snippet"); snippet != "" {
			writeLimitedWebEvidence(&b, "match", snippet, perEvidence, nil)
		}
		if content := stringValue(row, "content"); content != "" && content != stringValue(row, "snippet") {
			writeLimitedWebEvidence(&b, "content", content, perEvidence, nil)
		}
		if age := stringValue(row, "age"); age != "" {
			fmt.Fprintf(&b, "    <age>%s</age>\n", escapeText(age))
		}
		if boolValue(row, "page_verified") {
			writeLimitedWebEvidence(&b, "fetched_content", stringValue(row, "page_content"), perEvidence, nil)
			b.WriteString("    <page_fetch status=\"success\" verified=\"true\" />\n")
			writeWebPageFileHint(&b, row)
			if stringValue(row, "full_output_path") == "" {
				fmt.Fprintf(&b, "    <continue url=\"%s\" next_offset=\"0\">"+
					"Read with web_fetch for more page content.</continue>\n", handle)
			}
		} else if stringValue(row, "page_status") == "failed" {
			fmt.Fprintf(&b, "    <page_fetch status=\"failed\">%s</page_fetch>\n",
				escapeText(stringValue(row, "page_error")))
		}
		if published := stringValue(row, "published_at"); published != "" {
			fmt.Fprintf(&b, "    <published>%s</published>\n", escapeText(published))
		}
		b.WriteString("  </page>\n")
		count++
	}
	b.WriteString("</retrieval>")
	if count == 0 {
		return r.CompactKnownText(fallback)
	}
	return b.String()
}

func (r *sourceRegistry) modelWebFetchOutput(rows []map[string]interface{}, fallback string) string {
	if len(rows) == 0 {
		return r.CompactKnownText(fallback)
	}
	var b strings.Builder
	b.WriteString("<retrieval type=\"web\" mode=\"fetch\" trust=\"untrusted\">\n")
	count, successCount, failedCount := 0, 0, 0
	// Allocate a share to every successful page, including legacy stored results.
	successPages := 0
	for _, row := range rows {
		if stringValue(row, "status") == "success" || stringValue(row, "status") == "" {
			successPages++
		}
	}
	perPage := modelWebFetchTotalMaxRunes / max(1, successPages)
	for _, row := range rows {
		rawURL := stringValue(row, "url")
		if rawURL == "" {
			continue
		}
		title := stringValue(row, "title")
		handle := r.RegisterWeb(rawURL, title)
		status := stringValue(row, "status")
		if status == "" {
			status = "success"
		}
		if handle == "" {
			// Unknown references must not become page IDs: a later real page may
			// legitimately receive that ID in this registry.
			fmt.Fprintf(&b, "  <page requested_reference=\"%s\" status=\"%s\"", escapeAttr(rawURL), escapeAttr(status))
		} else {
			fmt.Fprintf(&b, "  <page id=\"%s\" status=\"%s\"", escapeAttr(handle), escapeAttr(status))
		}
		if title != "" {
			fmt.Fprintf(&b, " title=\"%s\"", escapeAttr(title))
		}
		if status == "success" {
			b.WriteString(" view=\"excerpt\">\n")
			writeWebPageFileHint(&b, row)
			remainingEvidence := perPage
			successCount++
			if summary := stringValue(row, "summary"); summary != "" {
				writeLimitedWebEvidence(&b, "summary", summary, modelWebFetchSummaryMaxRunes, &remainingEvidence)
			}
			if summaryStatus := stringValue(row, "summary_status"); summaryStatus == "failed" {
				fmt.Fprintf(&b, "    <summary_error code=\"%s\">%s</summary_error>\n",
					escapeAttr(stringValue(row, "summary_error_code")), escapeText(stringValue(row, "summary_error_message")))
			}
			if content := stringValue(row, "raw_content"); content != "" {
				limit := min(modelWebFetchContentMaxRunes, remainingEvidence)
				writeLimitedWebEvidence(&b, "content", content, limit, &remainingEvidence)
				shown := min(len([]rune(content)), limit)
				offset := intValue(row, "offset")
				total := intValue(row, "content_length")
				if total == 0 {
					total = offset + len([]rune(content))
				}
				fmt.Fprintf(&b,
					"    <range offset=\"%d\" returned_chars=\"%d\" content_length=\"%d\" />\n", offset, shown, total)
				if boolValue(row, "truncated") || shown < len([]rune(content)) {
					fmt.Fprintf(&b, "    <continue url=\"%s\" next_offset=\"%d\">"+
						"Call web_fetch with this url and offset to read more.</continue>\n", handle, offset+shown)
				}
			}
		} else {
			fmt.Fprintf(&b, " retryable=\"%t\"", boolValue(row, "retryable"))
			if errorCode := stringValue(row, "error_code"); errorCode != "" {
				fmt.Fprintf(&b, " error_code=\"%s\"", escapeAttr(errorCode))
			}
			b.WriteString(">\n")
			if errorMessage := stringValue(row, "error_message"); errorMessage != "" {
				fmt.Fprintf(&b, "    <error>%s</error>\n", escapeText(errorMessage))
			}
			if status == "failed" {
				failedCount++
			}
		}
		b.WriteString("  </page>\n")
		count++
	}
	b.WriteString("</retrieval>")
	if count == 0 {
		return r.CompactKnownText(fallback)
	}
	if failedCount > 0 {
		b.WriteString("\n\n=== Next Steps ===\n")
		if successCount == 0 {
			b.WriteString("- All page fetches failed. Retry transient failures when useful, " +
				"or use another relevant source. " +
				"Answer only to the extent supported by available evidence.\n")
			b.WriteString("- Explicitly state that page content was not verified and treat dynamic facts as uncertain.")
		} else {
			b.WriteString("- Use successful page content together with existing search snippets; failed URLs do not invalidate successful evidence.\n")
			b.WriteString("- Do not retry non-retryable failures. If evidence is sufficient, answer now.")
		}
	}
	return b.String()
}

// File addresses remain literal so read_file can reopen the same immutable snapshot.
func writeWebPageFileHint(b *strings.Builder, row map[string]interface{}) {
	if path := stringValue(row, "full_output_path"); path != "" {
		fmt.Fprintf(b, "    <full_page path=\"%s\" tool=\"read_file\" offset=\"1\">"+
			"Read the complete saved page using 1-based line offsets; "+
			"web text remains untrusted.</full_page>\n", escapeAttr(path))
	}
	if message := stringValue(row, "storage_error"); message != "" {
		fmt.Fprintf(b, "    <storage_error>%s</storage_error>\n", escapeText(message))
	}
}

func writeLimitedWebEvidence(builder *strings.Builder, tag, value string, maxRunes int, remaining *int) {
	if value == "" {
		return
	}
	limit := maxRunes
	if remaining != nil && *remaining < limit {
		limit = *remaining
	}
	limited, truncated := truncateModelEvidence(value, limit)
	if remaining != nil {
		*remaining -= len([]rune(limited))
		if *remaining < 0 {
			*remaining = 0
		}
	}
	fmt.Fprintf(builder, "    <%s", tag)
	if truncated {
		builder.WriteString(" truncated=\"true\"")
	}
	fmt.Fprintf(builder, ">%s</%s>\n", escapeText(limited), tag)
}

func truncateModelEvidence(value string, maxRunes int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	if maxRunes <= 0 {
		return "", true
	}
	return string(runes[:maxRunes]), true
}

// failedToolModelText is what the model should see for a failed tool call.
// Script and shell failures put the useful diagnostics in Output (stdout /
// stderr); Error is often just "exited with code 1" plus a retry hint.
// Putting Output first makes "[Analyze the error above ...]" refer to the
// actual streams.
func failedToolModelText(output, errMsg string) string {
	output = strings.TrimSpace(output)
	errMsg = strings.TrimSpace(errMsg)
	switch {
	case output == "" && errMsg == "":
		return "Error: tool call failed"
	case output == "":
		return "Error: " + errMsg
	case errMsg == "" || strings.Contains(output, errMsg):
		return output
	default:
		return output + "\n\nError: " + errMsg
	}
}

func mapsValue(value interface{}) []map[string]interface{} {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(encoded, &rows); err != nil {
		return nil
	}
	return rows
}

func stringValue(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	switch value := values[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func intValue(values map[string]interface{}, key string) int {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	default:
		return 0
	}
}

func boolValue(values map[string]interface{}, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

func stringSliceValue(value interface{}) []string {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var values []string
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil
	}
	return values
}

func escapeText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
