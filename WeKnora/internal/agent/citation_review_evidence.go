package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const citationReviewEvidenceBudget = 12 * 1024

// The fixed scope/omission envelope is about 350 bytes. Keep a larger floor
// so small requested budgets can still report all required status fields.
const citationReviewMinimumBudget = 512

type citationReviewChunk struct {
	tool                   string
	toolCallID             string
	evidenceID             string
	jsonPointer            string
	chunk                  map[string]any
	document               map[string]any
	sourceScope            map[string]any
	chunkID                string
	knowledgeID            string
	content                string
	contentKnown           bool
	additionalObservations []citationReviewChunk
	observationsOmitted    int
}

// citedSourceEvidenceFromSteps gives the source critic a citation-first view
// of exact chunks already fetched successfully this turn. It never performs a
// lookup and never promotes the candidate's own text into source evidence.
func citedSourceEvidenceFromSteps(steps []types.AgentStep, candidate string, budget int) map[string]any {
	return citedSourceEvidenceFromMessages(sourceResultMessagesFromSteps(steps), candidate, budget, true)
}

func sourceResultMessagesFromSteps(steps []types.AgentStep) []chat.Message {
	messages := []chat.Message{}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || (call.Name != tools.ToolReadDocument && call.Name != tools.ToolSearchKnowledge) {
				continue
			}
			native := nativeGeometryImageIndexes(call)
			payload, err := json.Marshal(sourceReviewResultPayload(call, native))
			if err != nil {
				continue
			}
			messages = append(messages, chat.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(payload)})
		}
	}
	return messages
}

// citedSourceEvidenceFromMessages intentionally accepts only JSON objects in
// actual successful source-tool messages. A source body, XML rendering, plan,
// schema or assistant citation by itself cannot establish a fetched chunk.
func citedSourceEvidenceFromMessages(messages []chat.Message, candidate string, budget int, reviewHandles bool) map[string]any {
	return citedSourceEvidenceFromMessagesWithBudget(messages, candidate, budget, reviewHandles, citationReviewEvidenceBudget)
}

// citedSourceEvidenceFromMessagesWithBudget lets the answer reviewer trade
// duplicate views for a larger citation-first projection without changing the
// legacy budget used by structured evidence and other callers.
func citedSourceEvidenceFromMessagesWithBudget(messages []chat.Message, candidate string, budget int, reviewHandles bool, maxBudget int) map[string]any {
	if maxBudget < citationReviewMinimumBudget {
		maxBudget = citationReviewMinimumBudget
	}
	if budget < citationReviewMinimumBudget {
		budget = citationReviewMinimumBudget
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	ids := uniqueCandidateCitationIDs(candidate)
	view := map[string]any{
		"schema_version":                 "citation_source_evidence.v2",
		"view_source":                    "actual_successful_document_tool_chunk_objects",
		"budget_bytes":                   budget,
		"citation_count":                 len(documentAnswerCitation.FindAllStringSubmatch(candidate, -1)),
		"unique_citation_count":          len(ids),
		"items":                          []any{},
		"unknown_citation_ids":           []string{},
		"unknown_citation_ids_omitted":   0,
		"ambiguous_citation_ids":         []string{},
		"ambiguous_citation_ids_omitted": 0,
		"omitted_citation_items":         0,
		// Reserve the wider false encoding while admitting identities and bodies.
		// The exact complete value is restored only after final projection checks.
		"view_complete":    false,
		"reader_available": reviewHandles,
		"source_results":   []any{},
		"source_pointers":  []any{},
		"scopes":           []any{},
		"documents":        []any{},
		"body_notice":      "content is an exact prefix segment of the fetched chunk when body_state=review_prefix; source_content_complete and source offsets describe the original tool result. A review-budget omission is not source absence. Follow source_pointer_ref with the tables above to restore the immutable successful result path.",
	}
	items := []any{}
	unknown := []string{}
	ambiguous := []string{}
	identityComplete := true
	records, conflicts := citationChunksFromMessages(messages)
	registry := newCitationReviewRegistry(view)
	for _, id := range ids {
		if len(id) > 256 {
			switch {
			case conflicts[id]:
				view["ambiguous_citation_ids_omitted"] = citationInt(view["ambiguous_citation_ids_omitted"]) + 1
			case records[id].chunkID != "":
				view["omitted_citation_items"] = citationInt(view["omitted_citation_items"]) + 1
			default:
				view["unknown_citation_ids_omitted"] = citationInt(view["unknown_citation_ids_omitted"]) + 1
			}
			view["view_complete"] = false
			identityComplete = false
			continue
		}
		if conflicts[id] {
			if next, ok := appendCitationIDWithinBudget(view, "ambiguous_citation_ids", id, ambiguous, items, unknown, ambiguous, budget); ok {
				ambiguous = next
			} else {
				view["ambiguous_citation_ids_omitted"] = citationInt(view["ambiguous_citation_ids_omitted"]) + 1
			}
			view["view_complete"] = false
			identityComplete = false
			continue
		}
		record, ok := records[id]
		if !ok {
			if next, ok := appendCitationIDWithinBudget(view, "unknown_citation_ids", id, unknown, items, unknown, ambiguous, budget); ok {
				unknown = next
			} else {
				view["unknown_citation_ids_omitted"] = citationInt(view["unknown_citation_ids_omitted"]) + 1
			}
			view["view_complete"] = false
			identityComplete = false
			continue
		}
		if record.observationsOmitted > 0 {
			view["view_complete"] = false
			identityComplete = false
		}
		snapshot := registry.snapshot()
		base := citationReviewItem(record, 0, reviewHandles, registry)
		baseItems := append(append([]any(nil), items...), base)
		view["items"] = baseItems
		if citationReviewViewSize(view) > budget {
			view["items"] = items
			registry.restore(snapshot)
			view["omitted_citation_items"] = citationInt(view["omitted_citation_items"]) + 1
			view["view_complete"] = false
			identityComplete = false
			continue
		}
		items = baseItems
	}
	// Keep every retained identity/reader pointer first, then allocate a
	// rune-safe prefix to each body at an equal per-body quota. This avoids
	// candidate order deciding which cited source gets its body projected.
	items = allocateCitationBodiesFairly(view, items, records, budget, reviewHandles, registry, identityComplete)
	view["items"] = items
	view["unknown_citation_ids"] = unknown
	view["ambiguous_citation_ids"] = ambiguous
	trimCitationReviewView(view, budget)
	return view
}

type citationReviewRegistry struct {
	view          map[string]any
	results       []any
	resultIndex   map[string]int
	pointers      []any
	pointerIndex  map[string]int
	scopes        []any
	scopeIndex    map[string]int
	documents     []any
	documentIndex map[string]int
}

func newCitationReviewRegistry(view map[string]any) *citationReviewRegistry {
	return &citationReviewRegistry{view: view, resultIndex: map[string]int{}, pointerIndex: map[string]int{}, scopeIndex: map[string]int{}, documentIndex: map[string]int{}}
}

func citationCanonicalKey(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(body)
}

func (r *citationReviewRegistry) resultRef(record citationReviewChunk) int {
	key := strings.Join([]string{record.tool, record.toolCallID, record.evidenceID}, "\x00")
	if index, ok := r.resultIndex[key]; ok {
		return index
	}
	index := len(r.results)
	r.results = append(r.results, []any{record.tool, record.toolCallID, record.evidenceID})
	r.resultIndex[key] = index
	r.view["source_results"] = r.results
	return index
}

func (r *citationReviewRegistry) pointerRef(record citationReviewChunk) int {
	if record.evidenceID == "" || record.jsonPointer == "" {
		return -1
	}
	result := r.resultRef(record)
	key := fmt.Sprintf("%d\x00%s", result, record.jsonPointer)
	if index, ok := r.pointerIndex[key]; ok {
		return index
	}
	index := len(r.pointers)
	r.pointers = append(r.pointers, []any{result, record.jsonPointer})
	r.pointerIndex[key] = index
	r.view["source_pointers"] = r.pointers
	return index
}

type citationReviewRegistrySnapshot struct {
	results, pointers, scopes, documents                 int
	resultIndex, pointerIndex, scopeIndex, documentIndex map[string]int
}

func (r *citationReviewRegistry) snapshot() citationReviewRegistrySnapshot {
	copyIndex := func(source map[string]int) map[string]int {
		result := make(map[string]int, len(source))
		for key, value := range source {
			result[key] = value
		}
		return result
	}
	return citationReviewRegistrySnapshot{len(r.results), len(r.pointers), len(r.scopes), len(r.documents), copyIndex(r.resultIndex), copyIndex(r.pointerIndex), copyIndex(r.scopeIndex), copyIndex(r.documentIndex)}
}

func (r *citationReviewRegistry) restore(snapshot citationReviewRegistrySnapshot) {
	r.results = r.results[:snapshot.results]
	r.pointers = r.pointers[:snapshot.pointers]
	r.scopes = r.scopes[:snapshot.scopes]
	r.documents = r.documents[:snapshot.documents]
	r.resultIndex = snapshot.resultIndex
	r.pointerIndex = snapshot.pointerIndex
	r.scopeIndex = snapshot.scopeIndex
	r.documentIndex = snapshot.documentIndex
	r.view["source_results"] = r.results
	r.view["source_pointers"] = r.pointers
	r.view["scopes"] = r.scopes
	r.view["documents"] = r.documents
}

func (r *citationReviewRegistry) metadataRef(value map[string]any, index map[string]int, values *[]any, field string) any {
	if value == nil {
		return nil
	}
	key := citationCanonicalKey(value)
	if i, ok := index[key]; ok {
		return i
	}
	i := len(*values)
	*values = append(*values, value)
	index[key] = i
	r.view[field] = *values
	return i
}

func (r *citationReviewRegistry) scopeRef(value map[string]any) any {
	return r.metadataRef(value, r.scopeIndex, &r.scopes, "scopes")
}

func (r *citationReviewRegistry) documentRef(value map[string]any) any {
	return r.metadataRef(value, r.documentIndex, &r.documents, "documents")
}

func allocateCitationBodiesFairly(view map[string]any, items []any, records map[string]citationReviewChunk, budget int, reviewHandles bool, registry *citationReviewRegistry, identityComplete bool) []any {
	base := append([]any(nil), items...)
	for i, raw := range base {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := citationString(item["chunk_id"])
		record := records[id]
		base[i] = citationReviewItem(record, 0, reviewHandles, registry)
	}
	view["items"] = base
	maxRunes := 0
	for _, raw := range base {
		item, ok := raw.(map[string]any)
		if !ok || item["content_known"] != true {
			continue
		}
		id := citationString(item["chunk_id"])
		n := utf8.RuneCountInString(records[id].content)
		if n > maxRunes {
			maxRunes = n
		}
	}
	if maxRunes == 0 {
		allBodiesKnown := true
		for _, raw := range base {
			item, ok := raw.(map[string]any)
			if !ok || item["content_known"] != true {
				allBodiesKnown = false
				break
			}
		}
		view["view_complete"] = identityComplete && allBodiesKnown
		return base
	}
	// Keep false during byte-fit decisions. Switching false to true only at the
	// end can shrink JSON by a byte; the reverse transition used to overflow a
	// full identity projection and make trimming discard its last source item.
	view["view_complete"] = false
	low, high := 0, maxRunes
	best := append([]any(nil), base...)
	for low <= high {
		mid := low + (high-low)/2
		trial := make([]any, len(base))
		for i, raw := range base {
			item := raw.(map[string]any)
			id := citationString(item["chunk_id"])
			trial[i] = citationReviewItem(records[id], mid, reviewHandles, registry)
		}
		view["items"] = trial
		if citationReviewViewSize(view) <= budget {
			best = trial
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	view["items"] = best
	if citationReviewViewSize(view) > budget {
		view["items"] = base
		view["view_complete"] = false
		return base
	}
	allBodiesComplete := true
	for _, raw := range best {
		item, _ := raw.(map[string]any)
		if item == nil || item["content_known"] != true || item["content_state"] == "review_prefix" || item["content_state"] == "review_omitted" {
			allBodiesComplete = false
		}
	}
	view["view_complete"] = identityComplete && allBodiesComplete
	return best
}

func citationRunePrefix(value string, runes int) string {
	if runes <= 0 {
		return ""
	}
	count, end := 0, 0
	for index := range value {
		if count == runes {
			break
		}
		count++
		end = index
	}
	if count < runes {
		return value
	}
	_, size := utf8.DecodeRuneInString(value[end:])
	return value[:end+size]
}

func uniqueCandidateCitationIDs(answer string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, match := range documentAnswerCitation.FindAllStringSubmatch(answer, -1) {
		if len(match) != 2 || match[1] == "" || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		ids = append(ids, match[1])
	}
	return ids
}

func citationChunksFromMessages(messages []chat.Message) (map[string]citationReviewChunk, map[string]bool) {
	records := map[string]citationReviewChunk{}
	conflicts := map[string]bool{}
	for _, message := range messages {
		if message.Role != "tool" || (message.Name != tools.ToolReadDocument && message.Name != tools.ToolSearchKnowledge) {
			continue
		}
		payload, err := decodeCitationJSON(message.Content)
		if err != nil {
			continue
		}
		data, pointerPrefix, success := citationPayloadData(payload)
		if !success {
			continue
		}
		for _, field := range []string{"chunks", "results"} {
			rows, ok := data[field].([]any)
			if !ok {
				continue
			}
			for index, raw := range rows {
				chunk, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				id, _ := chunk["chunk_id"].(string)
				knowledgeID, _ := chunk["knowledge_id"].(string)
				content, contentOK := chunk["content"].(string)
				if id == "" || knowledgeID == "" || !contentOK {
					continue
				}
				doc := citationDocumentMetadata(data, knowledgeID)
				item := citationReviewChunk{
					tool: message.Name, toolCallID: message.ToolCallID, evidenceID: reviewEvidenceID(message),
					jsonPointer: fmt.Sprintf("%s/%s/%d/content", pointerPrefix, field, index),
					chunk:       citationChunkMetadata(chunk, content), document: doc, sourceScope: citationSourceScope(data),
					chunkID: id, knowledgeID: knowledgeID, content: content, contentKnown: true,
				}
				if conflicts[id] {
					continue
				}
				if prior, exists := records[id]; exists {
					if prior.knowledgeID != item.knowledgeID || prior.content != item.content || citationMapString(prior.chunk, "content_offset") != citationMapString(item.chunk, "content_offset") {
						delete(records, id)
						conflicts[id] = true
					} else {
						// The same body can be fetched through different scopes (for
						// example a search and then an exact physical-page read).
						// Preserve those real observations independently; never let
						// the first search erase a later page/coverage observation.
						if len(prior.additionalObservations) < 7 {
							prior.additionalObservations = append(prior.additionalObservations, item)
						} else {
							prior.observationsOmitted++
						}
						records[id] = prior
					}
					continue // Body once, actual scope observations separately.
				}
				records[id] = item
			}
		}
	}
	return records, conflicts
}

func decodeCitationJSON(content string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON value")
	}
	return value, nil
}

func citationPayloadData(payload map[string]any) (map[string]any, string, bool) {
	if success, hasSuccess := payload["success"].(bool); hasSuccess {
		if !success {
			return nil, "", false
		}
		data, ok := payload["data"].(map[string]any)
		return data, "/data", ok
	}
	if message, hasError := payload["error"]; hasError && message != nil && fmt.Sprint(message) != "" {
		return nil, "", false
	}
	// A tool-role JSON message may itself be the exact source result Data.
	// Require actual chunk arrays; arbitrary JSON text is not source evidence.
	if _, hasChunks := payload["chunks"].([]any); hasChunks {
		return payload, "", true
	}
	if _, hasResults := payload["results"].([]any); hasResults {
		return payload, "", true
	}
	return nil, "", false
}

func citationDocumentMetadata(data map[string]any, knowledgeID string) map[string]any {
	if document, ok := data["document"].(map[string]any); ok && citationString(document["knowledge_id"]) == knowledgeID {
		return selectCitationFields(document, []string{"knowledge_id", "title", "file_name", "file_type", "parse_status", "page_count", "chunk_count", "file_size", "updated_at"})
	}
	if documents, ok := data["documents"].([]any); ok {
		for _, raw := range documents {
			if document, ok := raw.(map[string]any); ok && citationString(document["knowledge_id"]) == knowledgeID {
				return selectCitationFields(document, []string{"knowledge_id", "title", "file_name", "file_type", "parse_status", "page_count", "chunk_count", "file_size", "updated_at"})
			}
		}
	}
	return nil
}

func citationChunkMetadata(chunk map[string]any, content string) map[string]any {
	metadata := selectCitationFields(chunk, []string{"chunk_id", "knowledge_id", "chunk_index", "chunk_type", "start_at", "end_at", "parent_chunk_id", "page", "physical_page", "source_page", "content_offset", "content_offset_space", "content_complete", "next_content_offset", "source_locators"})
	locators, ok := metadata["source_locators"].([]any)
	if !ok {
		return metadata
	}
	compact := make([]any, len(locators))
	for index, raw := range locators {
		locator, ok := raw.(map[string]any)
		if !ok {
			compact[index] = raw
			continue
		}
		copy := make(map[string]any, len(locator)+1)
		for key, value := range locator {
			copy[key] = value
		}
		quote, isQuote := locator["quote"].(string)
		if isQuote && citationWhitespaceFoldContains(content, quote) {
			delete(copy, "quote")
			copy["quote_omitted"] = true
		}
		compact[index] = copy
	}
	metadata["source_locators"] = compact
	return metadata
}

// Locator quotes are sometimes whitespace-normalized while chunk content keeps
// its original line breaks. Fold Unicode whitespace only before checking exact
// substring containment; this is representation normalization, not fuzzy text
// matching.
func citationWhitespaceFoldContains(content, quote string) bool {
	fold := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	foldedQuote := fold(quote)
	return foldedQuote != "" && strings.Contains(fold(content), foldedQuote)
}

func citationSourceScope(data map[string]any) map[string]any {
	scope := selectCitationFields(data, []string{"knowledge_id", "knowledge_base_id", "total_chunks", "fetched_chunks", "total_docs", "page", "page_size", "offset", "offset_space", "truncated", "next_offset", "content_complete", "content_offset", "next_content_offset", "document_scope", "requires_source_scope", "query_scope", "page_complete", "page_chunks"})
	if scope == nil {
		scope = map[string]any{}
	}
	if coverage, ok := data["coverage"].(map[string]any); ok {
		if selected := selectCitationFields(coverage, []string{"complete", "truncated", "next_offset", "total", "returned", "page", "page_size", "offset", "content_complete", "next_content_offset"}); len(selected) > 0 {
			scope["coverage"] = selected
		} else {
			scope["coverage_omitted"] = true
		}
	} else if _, exists := data["coverage"]; exists {
		scope["coverage_omitted"] = true
	}
	if value, exists := scope["document_scope"]; exists {
		if object, ok := value.(map[string]any); ok {
			selected := selectCitationFields(object, []string{"matched", "knowledge_id", "page", "plan_validated", "has_visual_evidence"})
			if len(selected) > 0 {
				scope["document_scope"] = selected
			} else {
				delete(scope, "document_scope")
			}
			if len(selected) < len(object) {
				scope["document_scope_omitted"] = true
			}
		} else if _, ok := value.(string); !ok {
			delete(scope, "document_scope")
			scope["document_scope_omitted"] = true
		}
	}
	return scope
}

func selectCitationFields(source map[string]any, fields []string) map[string]any {
	selected := map[string]any{}
	for _, key := range fields {
		if value, ok := source[key]; ok {
			selected[key] = value
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

// exact observation/source mapping is held in the shared top-level tables.
func citationReviewItem(record citationReviewChunk, prefixRunes int, reviewHandles bool, registry *citationReviewRegistry) map[string]any {
	segment := citationRunePrefix(record.content, prefixRunes)
	if prefixRunes == 0 {
		segment = ""
	}
	chunkMetadata := make(map[string]any, len(record.chunk))
	for key, value := range record.chunk {
		if key == "chunk_id" || key == "knowledge_id" || key == "content_offset" || key == "content_complete" || key == "next_content_offset" {
			continue
		}
		chunkMetadata[key] = value
	}
	item := map[string]any{
		"chunk_id": record.chunkID, "knowledge_id": record.knowledgeID,
		"chunk_metadata": chunkMetadata,
		"document_ref":   registry.documentRef(record.document), "source_scope_ref": registry.scopeRef(record.sourceScope),
		"content_bytes": len(record.content), "content_included": len(segment) > 0,
		"content_known": record.contentKnown,
		"content_state": "review_omitted", "content_omitted_bytes": len(record.content),
		"review_segment_rune_start": 0, "review_segment_rune_end": 0, "review_segment_complete": record.contentKnown && len(record.content) == 0,
	}
	if len(segment) > 0 {
		item["content"] = segment
		item["review_segment_rune_end"] = utf8.RuneCountInString(segment)
		item["content_omitted_bytes"] = len(record.content) - len(segment)
		item["review_segment_complete"] = len(segment) == len(record.content)
		item["content_state"] = "review_prefix"
		if len(segment) == len(record.content) {
			item["content_state"] = "source_body"
		}
	}
	if pointerRef := registry.pointerRef(record); pointerRef >= 0 {
		item["source_pointer_ref"] = pointerRef
		if !reviewHandles {
			item["reader_available"] = false
		}
	} else {
		item["source_pointer_unavailable"] = true
		item["reader_available"] = false
	}
	if complete, ok := record.chunk["content_complete"]; ok {
		item["source_content_complete"] = complete
	}
	if offset, ok := record.chunk["content_offset"]; ok {
		item["source_content_offset"] = offset
	}
	if next, ok := record.chunk["next_content_offset"]; ok {
		item["source_next_content_offset"] = next
	}
	if len(record.additionalObservations) > 0 {
		observations := []any{}
		for _, observed := range record.additionalObservations {
			observation := map[string]any{
				"chunk_metadata":   compactCitationChunkMetadata(observed.chunk),
				"document_ref":     registry.documentRef(observed.document),
				"source_scope_ref": registry.scopeRef(observed.sourceScope),
			}
			if complete, ok := observed.chunk["content_complete"]; ok {
				observation["source_content_complete"] = complete
			}
			if offset, ok := observed.chunk["content_offset"]; ok {
				observation["source_content_offset"] = offset
			}
			if next, ok := observed.chunk["next_content_offset"]; ok {
				observation["source_next_content_offset"] = next
			}
			if pointerRef := registry.pointerRef(observed); pointerRef >= 0 {
				observation["source_pointer_ref"] = pointerRef
			} else {
				observation["source_pointer_unavailable"] = true
			}
			observations = append(observations, observation)
		}
		item["additional_source_observations"] = observations
	}
	if record.observationsOmitted > 0 {
		item["source_observations_omitted"] = record.observationsOmitted
	}
	if !record.contentKnown {
		item["content_state"] = "content_unknown"
		item["content_included"] = false
		item["review_segment_complete"] = false
	} else if strings.TrimSpace(record.content) == "" {
		item["content_empty"] = true
		item["content_state"] = "source_empty"
		item["content_included"] = false
		item["review_segment_complete"] = true
	}
	return item
}

func compactCitationChunkMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if key == "chunk_id" || key == "knowledge_id" || key == "content_offset" || key == "content_complete" || key == "next_content_offset" {
			continue
		}
		result[key] = value
	}
	return result
}

func appendCitationIDWithinBudget(view map[string]any, field, id string, current []string, items []any, unknown, ambiguous []string, budget int) ([]string, bool) {
	if len(id) > 256 || len(current) >= 24 {
		return current, false
	}
	candidate := append(append([]string(nil), current...), id)
	view["items"] = items
	view["unknown_citation_ids"] = unknown
	view["ambiguous_citation_ids"] = ambiguous
	view[field] = candidate
	if citationReviewViewSize(view) <= budget {
		return candidate, true
	}
	view[field] = current
	return current, false
}

// Remove lower-priority ID detail before source items, but always preserve
// explicit omission counts. The final fallback keeps the fixed envelope under
// the advertised budget even if unusual metadata grows unexpectedly.
func trimCitationReviewView(view map[string]any, budget int) {
	unknown, _ := view["unknown_citation_ids"].([]string)
	ambiguous, _ := view["ambiguous_citation_ids"].([]string)
	items, _ := view["items"].([]any)
	for citationReviewViewSize(view) > budget && len(unknown) > 0 {
		unknown = unknown[:len(unknown)-1]
		view["unknown_citation_ids"] = unknown
		view["unknown_citation_ids_omitted"] = citationInt(view["unknown_citation_ids_omitted"]) + 1
		view["view_complete"] = false
	}
	for citationReviewViewSize(view) > budget && len(ambiguous) > 0 {
		ambiguous = ambiguous[:len(ambiguous)-1]
		view["ambiguous_citation_ids"] = ambiguous
		view["ambiguous_citation_ids_omitted"] = citationInt(view["ambiguous_citation_ids_omitted"]) + 1
		view["view_complete"] = false
	}
	for citationReviewViewSize(view) > budget && len(items) > 0 {
		items = items[:len(items)-1]
		view["items"] = items
		view["omitted_citation_items"] = citationInt(view["omitted_citation_items"]) + 1
		view["view_complete"] = false
	}
	if citationReviewViewSize(view) > budget {
		view["unknown_citation_ids_omitted"] = citationInt(view["unknown_citation_ids_omitted"]) + len(unknown)
		view["ambiguous_citation_ids_omitted"] = citationInt(view["ambiguous_citation_ids_omitted"]) + len(ambiguous)
		view["omitted_citation_items"] = citationInt(view["omitted_citation_items"]) + len(items)
		view["unknown_citation_ids"] = []string{}
		view["ambiguous_citation_ids"] = []string{}
		view["items"] = []any{}
		view["view_complete"] = false
	}
	if citationReviewViewSize(view) > budget {
		minimal := map[string]any{
			"schema_version":                 "citation_source_evidence.v2",
			"budget_bytes":                   budget,
			"citation_count":                 view["citation_count"],
			"items":                          []any{},
			"unknown_citation_ids":           []string{},
			"unknown_citation_ids_omitted":   view["unknown_citation_ids_omitted"],
			"ambiguous_citation_ids":         []string{},
			"ambiguous_citation_ids_omitted": view["ambiguous_citation_ids_omitted"],
			"omitted_citation_items":         view["omitted_citation_items"],
			"reader_available":               view["reader_available"],
			"source_results":                 []any{},
			"source_pointers":                []any{},
			"scopes":                         []any{},
			"documents":                      []any{},
			"notice":                         "Evidence omitted by budget; omission does not establish source absence.",
			"view_complete":                  false,
			"budget_fallback":                true,
			"omitted_evidence":               true,
		}
		for key := range view {
			delete(view, key)
		}
		for key, value := range minimal {
			view[key] = value
		}
		// The minimum accepted budget is chosen so this fixed fallback fits.
		// Keep a last-resort compact envelope in case future fields change it.
		if citationReviewViewSize(view) > budget {
			compact := map[string]any{
				"schema_version":   "citation_source_evidence.v2",
				"budget_bytes":     budget,
				"view_complete":    false,
				"budget_fallback":  true,
				"omitted_evidence": true,
				"notice":           "Evidence omitted by budget; absence is unknown.",
			}
			for key := range view {
				delete(view, key)
			}
			for key, value := range compact {
				view[key] = value
			}
		}
	}
}

func citationReviewViewSize(view map[string]any) int {
	encoded, err := json.Marshal(view)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return len(encoded)
}

func citationInt(value any) int {
	maxInt := ^uint(0) >> 1
	switch number := value.(type) {
	case int:
		if number >= 0 {
			return number
		}
	case int64:
		if number >= 0 && uint64(number) <= uint64(maxInt) {
			return int(number)
		}
	case uint:
		if number <= maxInt {
			return int(number)
		}
	case uint64:
		if number <= uint64(maxInt) {
			return int(number)
		}
	case json.Number:
		parsed, err := number.Int64()
		if err == nil && parsed >= 0 && uint64(parsed) <= uint64(maxInt) {
			return int(parsed)
		}
	}
	return 0
}

func citationString(value any) string {
	text, _ := value.(string)
	return text
}

func citationMapString(value map[string]any, key string) string {
	encoded, _ := json.Marshal(value[key])
	return string(encoded)
}

// hasCurrentSourceMetadataEvidence recognizes actual successful catalog data,
// not a tool schema, plan, failed call or assistant prose. It enables the same
// source critic for answers based only on current document/dataset metadata.
func hasCurrentSourceMetadataEvidence(steps []types.AgentStep) bool {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || call.Result.Data == nil {
				continue
			}
			switch call.Name {
			case tools.ToolListDocuments:
				if actualMetadataRows(call.Result.Data["documents"], "knowledge_id") > 0 || actualEmptyDocumentDirectoryScope(call.Result.Data) {
					return true
				}
			case tools.ToolBindDocumentScope:
				if actualMetadataRows(call.Result.Data["documents"], "knowledge_id") > 0 {
					return true
				}
			case tools.ToolStructuredData:
				operation, _ := call.ExecutionArgs()["operation"].(string)
				if operation == "list_datasets" && (actualMetadataRows(call.Result.Data["datasets"], "dataset_id") > 0 || actualEmptyArray(call.Result.Data["datasets"])) {
					return true
				}
			}
		}
	}
	return false
}

func actualEmptyDocumentDirectoryScope(data map[string]interface{}) bool {
	if !actualEmptyArray(data["documents"]) {
		return false
	}
	knowledgeBaseID, ok := data["knowledge_base_id"].(string)
	if !ok || strings.TrimSpace(knowledgeBaseID) == "" {
		return false
	}
	total, ok := data["total_docs"]
	if !ok || !nonNegativeMetadataInteger(total) {
		return false
	}
	page, pageOK := data["page"]
	pageSize, pageSizeOK := data["page_size"]
	return pageOK && pageSizeOK && positiveMetadataInteger(page) && positiveMetadataInteger(pageSize)
}

func actualEmptyArray(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var rows []any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if decoder.Decode(&rows) != nil {
		return false
	}
	return rows != nil && len(rows) == 0
}

func positiveMetadataInteger(value any) bool {
	integer, ok := metadataInteger(value)
	return ok && integer > 0
}

func nonNegativeMetadataInteger(value any) bool {
	integer, ok := metadataInteger(value)
	return ok && integer >= 0
}

func metadataInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		if uint64(number) <= uint64(^uint64(0)>>1) {
			return int64(number), true
		}
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number <= uint64(^uint64(0)>>1) {
			return int64(number), true
		}
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	}
	return 0, false
}

func actualMetadataRows(value any, idFields ...string) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	var rows []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&rows) != nil {
		return 0
	}
	count := 0
	for _, row := range rows {
		for _, field := range idFields {
			if id, ok := row[field].(string); ok && strings.TrimSpace(id) != "" {
				count++
				break
			}
		}
	}
	return count
}
