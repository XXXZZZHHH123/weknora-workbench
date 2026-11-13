package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

// This describes only the operation actually executed. It never claims that
// other channels were searched, chooses a business source, or blocks a call.
func (t *StructuredDataTool) annotateDiscoveryCoverage(ctx context.Context, operation string, arguments map[string]json.RawMessage, scope structuredKnowledgeScope, result *types.ToolResult) *types.ToolResult {
	if result == nil || !result.Success || result.Data == nil {
		return result
	}
	var representation string
	switch operation {
	case "list_datasets":
		representation = "structured_dataset_catalog"
	case "list_series":
		representation = "structured_field_catalog"
	case "list_charts":
		representation = "structured_chart_catalog"
	case "list_pdf_tables":
		representation = "published_pdf_table_catalog"
	default:
		return result
	}
	coverage := map[string]any{
		"operation": operation, "representation": representation,
		"document_body_searched_by_this_operation": false,
		"document_images_read_by_this_operation":   false,
		"semantic_absence_established":             false,
		"notice":                                   "This result covers only the executed structured catalog and its actual filters/pagination. It is not a search of document text or original figures. No matching field does not establish missing knowledge. Decide from the complete task whether to revise the query, continue a cursor, or discover/read authorized documents using the current tool directory; expand_task_tools can expose omitted capabilities. Do not repeat unchanged reads without a reason.",
	}
	if scope.KnowledgeBaseID != "" {
		coverage["knowledge_base_id"] = scope.KnowledgeBaseID
	}
	// Retain actual pagination/completeness instead of inventing a global
	// complete flag from a successful service response.
	for _, key := range []string{"offset", "next_offset", "truncated", "output_truncated", "catalog_completion_scope", "dataset_catalog_status", "knowledge_base_catalogs_truncated", "not_queried_knowledge_base_count", "partial_knowledge_base_count", "unavailable_knowledge_base_count"} {
		if value, ok := result.Data[key]; ok {
			coverage[key] = value
		}
	}
	// Only immutable, authenticated, same-turn reads are compared. A changed
	// query, page, protocol generation or result is not a repeated read.
	question, generation := t.structuredSourceQuestion()
	key, comparable := t.structuredReadCacheKey(ctx, types.CallerFromContext(ctx), scope, operation, arguments, question, generation)
	if comparable {
		stable := make(map[string]any, len(result.Data))
		for k, v := range result.Data {
			if k != "_execution_cache" && k != "_structured_execution" && k != "discovery_coverage" {
				stable[k] = v
			}
		}
		encoded, err := json.Marshal(stable)
		if err == nil {
			fingerprint := fmt.Sprintf("%s:%x", key, sha256.Sum256(encoded))
			t.readCache.mu.Lock()
			if t.readCache.generation == generation {
				if t.readCache.discoveryObservations == nil {
					t.readCache.discoveryObservations = map[string]int{}
				}
				if len(t.readCache.discoveryObservations) < 128 || t.readCache.discoveryObservations[fingerprint] > 0 {
					t.readCache.discoveryObservations[fingerprint]++
					coverage["identical_observation_count"] = t.readCache.discoveryObservations[fingerprint]
					coverage["unchanged_repeat"] = t.readCache.discoveryObservations[fingerprint] > 1
				}
			}
			t.readCache.mu.Unlock()
		}
	}
	copy := *result
	copy.Data = make(map[string]any, len(result.Data)+1)
	for k, v := range result.Data {
		copy.Data[k] = v
	}
	copy.Data["discovery_coverage"] = coverage
	return &copy
}
