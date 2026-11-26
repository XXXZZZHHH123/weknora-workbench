package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

// Delivery review receives navigation/status only, never a second source pool.
// Native registrations and private execution descriptors qualify observations;
// candidate wording, question keywords and missing numbers never do.
func (e *AgentEngine) discoveryReadObservations(state *types.AgentState) map[string]any {
	if state == nil || e.toolRegistry == nil {
		return nil
	}
	var observations []map[string]any
	omitted := 0
	readObserved := false
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			data := call.Result.Data
			var summary map[string]any
			if descriptor, ok := data["_structured_execution"].(agenttools.StructuredOperationDescriptor); ok && descriptor.EffectClass == "read" {
				switch descriptor.Operation {
				case "inspect_source", "query_series", "analyze_table", "read_pdf_table", "query_pdf_table":
					if data["status"] != "ok" {
						continue
					}
					if data["source"] == nil && data["source_file"] == nil && data["inputs"] == nil && data["rows"] == nil && data["values"] == nil {
						continue
					}
					readObserved = true
					summary = discoveryReadFields(data, "status", "kind", "source", "source_file", "dataset_id", "source_context", "hash_verified", "input_complete", "output_truncated", "returned_row_count")
				case "list_datasets", "list_series", "list_charts", "list_pdf_tables":
					if data["pending_sheets"] == nil {
						continue
					}
					summary = discoveryReadFields(data, "dataset_id", "source_context", "pending_sheets")
				default:
					continue
				}
				summary["operation"] = descriptor.Operation
			} else {
				registered, err := e.toolRegistry.GetTool(call.ExecutionName())
				if err != nil {
					continue
				}
				switch registered.(type) {
				case *agenttools.ReadDocumentTool:
					if !sourceNavigationReadObserved(data) && len(sourceNavigationRows(data["chunks"])) == 0 {
						continue
					}
					readObserved = true
					summary = discoveryReadFields(data, "knowledge_id", "knowledge_base_id", "knowledge_title", "page", "requested_pages", "page_complete", "pages_complete", "truncated", "next_offset", "fetched_chunks", "document_scope")
					summary["body_navigation_samples"] = discoveryBodyNavigation(data["chunks"])
					summary["image_catalog_navigation"] = discoveryImageNavigation(data["image_catalog"])
					summary["actual_image_read_status"] = discoveryImageNavigation(data["image_evidence"])
				case *agenttools.SearchKnowledgeTool:
					rows := sourceNavigationRows(data["results"])
					for _, row := range rows {
						if sourceNavigationString(row, "content") != "" {
							readObserved = true
						}
					}
					if len(rows) == 0 {
						continue
					}
					summary = map[string]any{"body_navigation_samples": discoveryBodyNavigation(data["results"])}
				default:
					continue
				}
				summary["tool_name"] = call.ExecutionName()
			}
			// Retain the most recent execution status in chronological order.
			// Earlier catalogs must not crowd out the actual read they led to.
			if len(observations) >= 8 {
				omitted++
				observations = observations[1:]
			}
			observations = append(observations, summary)
		}
	}
	if !readObserved {
		return nil
	}
	for {
		view := map[string]any{"observations": observations, "omitted_observations": omitted, "complete_source_values_omitted": true, "navigation_only": true, "task_coverage_established": false}
		encoded, err := json.Marshal(view)
		if err == nil && len(encoded) <= 12*1024 {
			return view
		}
		if len(observations) == 0 {
			return nil
		}
		observations = observations[1:]
		omitted++
	}
}

func discoveryReadFields(data map[string]any, keys ...string) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		if value, ok := data[key]; ok {
			// Preserve small original protocol objects exactly; oversized values
			// are explicitly omitted, never cut into invalid or misleading JSON.
			encoded, err := json.Marshal(value)
			if err != nil || len(encoded) > 1024 {
				result[key+"_omitted"] = true
				continue
			}
			result[key] = value
		}
	}
	return result
}

func discoveryBodyNavigation(value any) map[string]any {
	rows := sourceNavigationRows(value)
	var samples []map[string]any
	for _, row := range rows {
		if len(samples) >= 4 {
			break
		}
		sample := discoveryReadFields(row, "knowledge_id", "knowledge_base_id", "knowledge_title", "chunk_id", "source_chunk_id", "page")
		body := sourceNavigationString(row, "content")
		if body == "" {
			continue
		}
		sample["navigation_excerpt"] = taskPlanPrefix(body, 1024)
		sample["excerpt_complete"] = len(body) <= 1024
		samples = append(samples, sample)
	}
	return map[string]any{"samples": samples, "omitted_rows": len(rows) - len(samples), "navigation_only": true}
}

func discoveryImageNavigation(value any) map[string]any {
	// Native tools may return a private []documentImage, not []map. Normalize
	// only the bounded prefix, using its actual JSON tags; a persisted JSON
	// replay alone cannot test this runtime representation.
	rows := reflect.ValueOf(value)
	var samples []map[string]any
	if !rows.IsValid() || rows.Kind() != reflect.Slice {
		return map[string]any{"samples": samples, "omitted_rows": 0}
	}
	keys := []string{"reference", "knowledge_id", "chunk_id", "page", "page_hint", "page_verified", "label", "source_part", "read_args", "geometry_read_args", "geometry_selection_options", "read_success", "geometry_read_success"}
	for index := 0; index < rows.Len() && index < 4; index++ {
		value := rows.Index(index).Interface()
		row, ok := value.(map[string]any)
		if !ok {
			item := reflect.ValueOf(value)
			if item.Kind() == reflect.Pointer && !item.IsNil() {
				item = item.Elem()
			}
			if item.Kind() != reflect.Struct {
				continue
			}
			row = map[string]any{}
			for i := 0; i < item.NumField(); i++ {
				field := item.Type().Field(i)
				if field.PkgPath != "" {
					continue
				}
				tag := strings.Split(field.Tag.Get("json"), ",")
				for _, key := range keys {
					if tag[0] == key {
						if len(tag) > 1 && tag[1] == "omitempty" && item.Field(i).IsZero() {
							break
						}
						row[key] = item.Field(i).Interface()
						break
					}
				}
			}
		}
		// Project before normalization: long source_context/captions must not
		// crowd out the real selector and read arguments of a typed entry.
		projected := discoveryReadFields(row, keys...)
		encoded, err := json.Marshal(projected)
		if err != nil || len(encoded) > 4096 {
			continue
		}
		row = nil
		if json.Unmarshal(encoded, &row) != nil || row == nil {
			continue
		}
		samples = append(samples, row)
	}
	return map[string]any{"samples": samples, "omitted_rows": rows.Len() - len(samples)}
}

func (e *AgentEngine) coverageDocumentBodies(call types.ToolCall, base coverageEvidence, data map[string]any, search bool) []coverageEvidence {
	var evidence []coverageEvidence
	key := "chunks"
	if search {
		key = "results"
	}
	coverageRows(data, key, func(row map[string]any, path string) {
		item := base
		item.Kind, item.JSONPointer = "navigation", "/data"+path
		item.SourceID = sourceNavigationString(row, "knowledge_id")
		item.KnowledgeBaseID = sourceNavigationString(row, "knowledge_base_id")
		item.SourceTitle = sourceNavigationString(row, "knowledge_title")
		if item.SourceID == data["knowledge_id"] {
			if item.KnowledgeBaseID == "" {
				item.KnowledgeBaseID = sourceNavigationString(data, "knowledge_base_id")
			}
			if item.SourceTitle == "" {
				item.SourceTitle = sourceNavigationString(data, "knowledge_title")
			}
		}
		item.ChunkID = sourceNavigationString(row, "chunk_id")
		if item.SourceID == "" || item.KnowledgeBaseID == "" || !e.coverageScopeAllows(item.KnowledgeBaseID, item.SourceID) {
			return
		}
		item.SnapshotHash = coverageDocumentSnapshot(row, data)
		item.Page = coverageReadPage(row, data)
		pageRead := !search && item.Page > 0
		if !pageRead {
			item.Page = coverageLocatorPage(row)
		}
		if next := coverageOffset(row["next_content_offset"]); next != nil {
			item.NextOffset = next
		}
		item.SpanStart = coverageOffset(row["content_offset"])
		item.Complete = citationViewIsFull(citationSourceView{row: row, data: data})
		body := sourceNavigationString(row, "content")
		bound := e.taskCoverageDocumentCurrent(item, call)
		// Search may return an exact current chunk as well as a highlighted
		// snippet. Navigation alone, or a rewritten/partial search view, is
		// insufficient. An explicit page read can return an exact page view.
		if pageRead {
			item.Complete = row["content_rewritten"] != true && row["content_complete"] == true && item.SpanStart != nil && *item.SpanStart == 0
		}
		if bound && item.SnapshotHash != "" && body != "" && item.Complete {
			item.Kind, item.Bound, item.JSONPointer, item.Raw = "body", true, "/data"+path+"/content", body
			item.SourceDate = agenttools.CurrentDocumentSourceDate(e.toolRegistry, item.SourceID)
			item.ContentHash, item.CitationID = candidateTextDigest(body), item.ChunkID
			stop := *item.SpanStart + utf8.RuneCountInString(body)
			item.SpanEnd = &stop
		}
		evidence = append(evidence, item)
	})
	return evidence
}

func (e *AgentEngine) taskCoverageDocumentCurrent(observation coverageEvidence, call types.ToolCall) bool {
	if call.Result == nil || !call.Result.Success || !e.coverageScopeAllows(observation.KnowledgeBaseID, observation.SourceID) {
		return false
	}
	data := call.Result.Data
	if call.ExecutionName() == agenttools.ToolSearchKnowledge {
		return agenttools.IsCurrentDocumentSearchChunk(e.toolRegistry, call.Result, observation.SourceID) &&
			coverageContainsString(data["document_ids"], observation.SourceID)
	}
	return call.ExecutionName() == agenttools.ToolReadDocument &&
		observation.SourceID == data["knowledge_id"] && agenttools.IsCurrentDocumentRead(e.toolRegistry, call.Result)
}

func coverageContainsString(value any, want string) bool {
	if list, ok := value.([]string); ok {
		for _, item := range list {
			if item == want {
				return true
			}
		}
		return false
	}
	list, _ := value.([]any)
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func coverageLocatorPage(row map[string]any) int {
	// A full chunk may span several PDF pages. Only one actual parser page
	// locator can safely qualify the whole body with a physical page.
	var page int
	for _, locator := range sourceNavigationRows(row["source_locators"]) {
		first := citationInt(locator["page"])
		if locator["type"] == types.SourceLocatorSlide {
			first = citationInt(locator["slide"])
		}
		if first == 0 || locator["mapping"] != "exact" || locator["partial"] == true || page != 0 && page != first {
			return 0
		}
		page = first
	}
	return page
}

func coverageReadPage(row, data map[string]any) int {
	page := citationInt(row["page"])
	if page == 0 {
		return 0
	}
	if data["offset_space"] == "physical_page_chunks" && page == citationInt(data["page"]) {
		return page
	}
	for _, result := range sourceNavigationRows(data["page_results"]) {
		if page == citationInt(result["page"]) && result["success"] == true && result["offset_space"] == "physical_page_chunks" {
			return page
		}
	}
	return 0
}

func coverageDocumentSnapshot(row, data map[string]any) string {
	identity, ok := citationViewIdentity(citationSourceView{row: row, data: data})
	if !ok {
		return ""
	}
	hash := ""
	for _, locator := range sourceNavigationRows(row["source_locators"]) {
		if current := sourceNavigationString(locator, "source_hash"); current != "" {
			if hash != "" && hash != current {
				return ""
			}
			hash = current
		}
	}
	if hash != "" {
		return reviewDigest([]any{hash, row["content_revision"]})
	}
	return identity
}
