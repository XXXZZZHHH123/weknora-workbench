package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const documentCatalogReviewBudget = 8 * 1024

// documentCatalogReviewEvidence projects only real document identity and the
// scope returned by successful directory tools. It never treats a returned
// page or bound selection as the full directory unless the source metadata
// explicitly establishes that coverage.
func documentCatalogReviewEvidence(messages []chat.Message, budget int, readerAvailable bool) map[string]any {
	if budget < 512 {
		budget = 512
	}
	view := map[string]any{
		"view_source":                     "successful_current_turn_document_directory_results",
		"budget_bytes":                    budget,
		"catalogs":                        []any{},
		"catalog_result_count":            0,
		"omitted_catalog_results":         0,
		"omitted_catalog_handles":         []any{},
		"omitted_catalog_handles_omitted": 0,
		"omitted_documents":               0,
		"view_complete":                   true,
		"notice":                          "Only returned directory identities/scope; not body evidence. A complete projection is not full-directory coverage. Omitted rows are unknown; only an unfiltered first page covering total_docs establishes directory coverage.",
	}
	catalogs := []any{}
	omittedDocuments := 0
	for _, message := range messages {
		if message.Role != "tool" || message.ToolCallID == "" ||
			(message.Name != tools.ToolListDocuments && message.Name != tools.ToolBindDocumentScope) {
			continue
		}
		payload, err := decodeCitationJSON(message.Content)
		if err != nil {
			continue
		}
		success, ok := payload["success"].(bool)
		if !ok || !success {
			continue
		}
		data, ok := payload["data"].(map[string]any)
		if !ok {
			continue
		}
		documents, ok := data["documents"].([]any)
		if !ok {
			continue
		}
		view["catalog_result_count"] = citationInt(view["catalog_result_count"]) + 1
		entry := map[string]any{
			"tool":                    message.Name,
			"tool_call_id":            message.ToolCallID,
			"evidence_id":             reviewEvidenceID(message),
			"json_pointer":            "/data/documents",
			"reader_available":        readerAvailable,
			"returned_document_count": len(documents),
			"documents":               []any{},
			"omitted_document_count":  0,
			"companion_complete":      true,
			"directory_coverage":      "returned_subset_only",
		}
		if message.Name == tools.ToolListDocuments {
			entry["scope_kind"] = "authorized_directory_page"
			for _, key := range []string{"knowledge_base_id", "total_docs", "page", "page_size", "next_page", "keyword", "requires_source_scope", "truncated", "coverage"} {
				if value, exists := data[key]; exists {
					entry[key] = compactCatalogScopeValue(key, value)
					if marker, ok := entry[key].(map[string]any); ok && marker["omitted"] == true {
						marker["json_pointer"] = "/data/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
					}
				}
			}
			entry["directory_coverage"] = directoryCoverage(data, len(documents))
		} else {
			entry["scope_kind"] = "verified_selected_sources"
			entry["selected_count"] = len(documents)
		}
		catalogs = append(catalogs, entry)
		view["catalogs"] = catalogs
		if documentCatalogViewSize(view) > budget {
			catalogs = catalogs[:len(catalogs)-1]
			view["catalogs"] = catalogs
			view["omitted_catalog_results"] = citationInt(view["omitted_catalog_results"]) + 1
			omittedDocuments += len(documents)
			view["omitted_documents"] = omittedDocuments
			handles, _ := view["omitted_catalog_handles"].([]any)
			handle := map[string]any{"tool": message.Name, "tool_call_id": message.ToolCallID,
				"evidence_id": reviewEvidenceID(message), "json_pointer": "/data/documents", "reader_available": readerAvailable}
			candidate := append(append([]any(nil), handles...), handle)
			view["omitted_catalog_handles"] = candidate
			if documentCatalogViewSize(view) > budget {
				view["omitted_catalog_handles"] = handles
				view["omitted_catalog_handles_omitted"] = citationInt(view["omitted_catalog_handles_omitted"]) + 1
			}
			view["view_complete"] = false
			continue
		}

		projected := []any{}
		for index, raw := range documents {
			document, ok := raw.(map[string]any)
			if !ok || citationString(document["knowledge_id"]) == "" {
				entry["omitted_document_count"] = citationInt(entry["omitted_document_count"]) + 1
				entry["companion_complete"] = false
				omittedDocuments++
				view["omitted_documents"] = omittedDocuments
				view["view_complete"] = false
				continue
			}
			projectedDocument := map[string]any{
				"knowledge_id":   document["knowledge_id"],
				"source_pointer": fmt.Sprintf("/data/documents/%d", index),
			}
			for _, key := range []string{"title", "file_name", "file_type", "type"} {
				if value, exists := document[key]; exists {
					if text, ok := value.(string); ok {
						projectedDocument[key] = text
					}
				}
			}
			projected = append(projected, projectedDocument)
			entry["documents"] = projected
			if documentCatalogViewSize(view) > budget {
				projected = projected[:len(projected)-1]
				entry["documents"] = projected
				entry["omitted_document_count"] = citationInt(entry["omitted_document_count"]) + 1
				entry["companion_complete"] = false
				omittedDocuments++
				view["omitted_documents"] = omittedDocuments
				view["view_complete"] = false
			}
		}
	}
	view["catalogs"] = catalogs
	if documentCatalogViewSize(view) > budget {
		// The fixed envelope is deliberately small. Keep the advertised bound
		// even for malformed or exceptionally large source metadata.
		for _, rawCatalog := range catalogs {
			if catalog, ok := rawCatalog.(map[string]any); ok {
				if included, ok := catalog["documents"].([]any); ok {
					omittedDocuments += len(included)
				}
			}
		}
		view["catalogs"] = []any{}
		view["omitted_catalog_results"] = citationInt(view["catalog_result_count"])
		view["omitted_catalog_handles"] = []any{}
		view["omitted_catalog_handles_omitted"] = citationInt(view["catalog_result_count"])
		view["omitted_documents"] = omittedDocuments
		view["view_complete"] = false
	}
	return compactDocumentCatalogViewToBudget(view, budget)
}

func compactDocumentCatalogViewToBudget(view map[string]any, budget int) map[string]any {
	if documentCatalogViewSize(view) <= budget {
		return view
	}
	count := citationInt(view["catalog_result_count"])
	view["catalogs"] = []any{}
	view["omitted_catalog_results"] = count
	view["omitted_catalog_handles"] = []any{}
	view["omitted_catalog_handles_omitted"] = count
	view["view_complete"] = false
	view["notice"] = "Omitted rows are unknown; only explicit full coverage means the directory is complete."
	if documentCatalogViewSize(view) <= budget {
		return view
	}
	// Preserve exact counts and explicit incompleteness if future envelope
	// fields make even the shortened form exceed the minimum budget.
	return map[string]any{
		"view_source":                     view["view_source"],
		"budget_bytes":                    budget,
		"catalogs":                        []any{},
		"catalog_result_count":            count,
		"omitted_catalog_results":         count,
		"omitted_catalog_handles":         []any{},
		"omitted_catalog_handles_omitted": count,
		"omitted_documents":               citationInt(view["omitted_documents"]),
		"view_complete":                   false,
		"notice":                          "Omitted rows are unknown.",
	}
}

func compactCatalogScopeValue(key string, value any) any {
	switch typed := value.(type) {
	case string:
		if len(typed) <= 512 {
			return typed
		}
	case json.Number, bool, float64, float32, int, int32, int64, uint, uint32, uint64:
		return value
	case map[string]any:
		if key == "coverage" {
			return selectCitationFields(typed, []string{"complete", "truncated", "total", "returned", "page", "page_size", "next_page"})
		}
	}
	return map[string]any{"omitted": true}
}

func directoryCoverage(data map[string]any, returned int) string {
	if strings.TrimSpace(citationString(data["knowledge_base_id"])) == "" {
		return "returned_page_or_filtered_subset"
	}
	keyword := ""
	if keywordValue, exists := data["keyword"]; exists {
		value, ok := keywordValue.(string)
		if !ok || strings.TrimSpace(value) != "" {
			return "returned_page_or_filtered_subset"
		}
		keyword = strings.TrimSpace(value)
	}
	if truncated, _ := data["truncated"].(bool); truncated {
		return "returned_page_or_filtered_subset"
	}
	if coverage, ok := data["coverage"].(map[string]any); ok {
		if truncated, _ := coverage["truncated"].(bool); truncated {
			return "returned_page_or_filtered_subset"
		}
		if complete, exists := coverage["complete"]; exists {
			if isComplete, ok := complete.(bool); !ok || !isComplete {
				return "returned_page_or_filtered_subset"
			}
		}
	}
	page, pageOK := metadataInteger(data["page"])
	pageSize, pageSizeOK := metadataInteger(data["page_size"])
	total, totalOK := metadataInteger(data["total_docs"])
	_, hasNext := data["next_page"]
	if keyword == "" && pageOK && page == 1 && pageSizeOK && pageSize > 0 && totalOK && total >= 0 &&
		int64(returned) == total && int64(returned) <= pageSize && !hasNext {
		return "unfiltered_directory_complete"
	}
	return "returned_page_or_filtered_subset"
}

func documentCatalogViewSize(view map[string]any) int {
	encoded, err := json.Marshal(view)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return len(encoded)
}

// documentCatalogMessagesFromSteps serializes only actual successful directory
// tool results in the same form used by sourceConsistencyMessages. This gives
// the companion's evidence_id and JSON pointer a stable path back to the real
// result; it does not query or reconstruct a directory.
func documentCatalogMessagesFromSteps(steps []types.AgentStep) []chat.Message {
	messages := []chat.Message{}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success ||
				(call.Name != tools.ToolListDocuments && call.Name != tools.ToolBindDocumentScope) {
				continue
			}
			payload := map[string]any{"success": true, "data": call.Result.Data, "error": call.Result.Error}
			if !toolOutputDuplicatesData(call.Result.Data, call.Result.Output) {
				payload["output"] = call.Result.Output
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			messages = append(messages, chat.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(encoded)})
		}
	}
	return messages
}
