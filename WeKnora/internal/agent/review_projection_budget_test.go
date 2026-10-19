package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceReviewCitationProjectionKeepsTwentySixFullBodiesWithinSharedBudget(t *testing.T) {
	const sourceCount = 26
	rows := make([]any, 0, sourceCount)
	contents := make(map[string]string, sourceCount)
	answer := strings.Builder{}
	for i := 0; i < sourceCount; i++ {
		id := fmt.Sprintf("chunk-%02d", i)
		size := 827
		switch i {
		case 0:
			size = 293
		case 1:
			size = 1070
		}
		content := strings.Repeat(string(rune('a'+i%26)), size)
		contents[id] = content
		rows = append(rows, map[string]any{
			"chunk_id": id, "knowledge_id": "document-1", "chunk_index": i,
			"chunk_type": "text", "physical_page": i + 1, "page": i + 1,
			"content_offset": 0, "content_offset_space": "unicode_rune",
			"content_complete": true, "content": content,
			"source_locators": []any{map[string]any{"physical_page": i + 1, "type": "slide", "slide": i + 1}},
		})
		fmt.Fprintf(&answer, "claim-%d <kb chunk_id=\"%s\">citation</kb>\n", i, id)
	}
	// Repeated occurrences must still bind to their exact unique source item.
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&answer, "repeat <kb chunk_id=\"chunk-%02d\">citation</kb>\n", i)
	}
	data := map[string]any{
		"knowledge_id": "document-1", "knowledge_base_id": "authorized-kb",
		"knowledge_title": "Observed document", "total_chunks": sourceCount,
		"fetched_chunks": sourceCount, "page": 1, "page_size": sourceCount,
		"truncated": false,
		"document":  map[string]any{"knowledge_id": "document-1", "title": "Observed document", "file_type": "pptx"},
		"chunks":    rows,
	}
	payload, err := json.Marshal(map[string]any{
		"success": true, "data": data,
		"execution_scope": map[string]any{"knowledge_id": "document-1", "page": 1, "page_size": sourceCount, "scope_validated": true},
	})
	require.NoError(t, err)
	messages := []chat.Message{{Role: "tool", Name: "read_document", ToolCallID: "read-call-1", Content: string(payload)}}

	base := map[string]any{
		"source_tool_results":                 map[string]any{"items": []any{map[string]any{"content": strings.Repeat("g", 512)}}},
		"document_retrieval_companion":        map[string]any{"observations": []any{map[string]any{"content": strings.Repeat("d", 512)}}},
		"source_catalog_companion":            map[string]any{"items": []any{map[string]any{"title": strings.Repeat("c", 200)}}},
		"available_tool_directory":            map[string]any{"available_tools": []string{strings.Repeat("t", 200)}},
		"document_image_observations":         []any{map[string]any{"description": strings.Repeat("i", 6*1024), "page": 3, "image_geometry_evidence": map[string]any{"width": 1280, "height": 720}}},
		"structured_query_row_companion":      map[string]any{"rows": []any{map[string]any{"cells": strings.Repeat("r", 3*1024), "row_index": 4}}},
		"document_table_execution_summary":    []any{map[string]any{"table": strings.Repeat("x", 2*1024), "page": 5}},
		"document_image_observation_scope":    map[string]any{"projection_complete": true, "observed": 1},
		"document_table_execution_scope":      map[string]any{"projection_complete": true, "observed": 1},
		"document_retrieval_companion_notice": documentRetrievalCompanionInstructions,
		"evidence_notice":                     "Bounded evidence notice retained.",
	}

	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "read-call-1", Name: "read_document", Result: &types.ToolResult{Success: true, Data: data}}}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	snapshot := engine.buildSourceReviewSnapshot(t.Context(), answer.String(), state, nil)
	actualCitation := snapshot.input["citation_source_evidence"].(map[string]any)
	require.Equal(t, sourceCount, len(actualCitation["items"].([]any)))
	require.Equal(t, true, actualCitation["view_complete"])
	require.LessOrEqual(t, sourceReviewProjectionSize(snapshot.input), sourceReviewSharedProjectionBudget)
	citationBudget := sourceReviewCitationBudgetForBase(base)
	evidence := citedSourceEvidenceFromMessagesWithBudget(messages, answer.String(), citationBudget, true, sourceReviewCitationBudget)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationBudget)
	require.Equal(t, sourceCount, evidence["unique_citation_count"])
	require.Equal(t, 0, evidence["omitted_citation_items"])
	require.Equal(t, true, evidence["view_complete"])
	items := evidence["items"].([]any)
	require.Len(t, items, sourceCount)
	for i, raw := range items {
		item := raw.(map[string]any)
		id := fmt.Sprintf("chunk-%02d", i)
		require.Equal(t, id, item["chunk_id"])
		require.Equal(t, contents[id], item["content"])
		require.Equal(t, "source_body", item["content_state"])
		require.Equal(t, true, item["source_content_complete"])
		metadata := item["chunk_metadata"].(map[string]any)
		require.Equal(t, i+1, citationInt(metadata["physical_page"]))
		require.NotNil(t, item["document_ref"])
		require.NotNil(t, item["source_scope_ref"])
		require.NotNil(t, item["source_pointer_ref"])
	}

	binding := citationBindingViewPositionsOnly(answer.String(), messages, evidence, true, sourceReviewBindingBudget)
	require.Equal(t, true, binding["projection_complete"])
	require.Equal(t, 38, binding["included_occurrence_count"])
	require.LessOrEqual(t, citationBindingViewSize(binding), sourceReviewBindingBudget)
	packet := make(map[string]any, len(base)+2)
	for key, value := range base {
		packet[key] = value
	}
	packet["citation_source_evidence"] = evidence
	packet["citation_binding_view"] = binding
	require.LessOrEqual(t, sourceReviewProjectionSize(packet), sourceReviewSharedProjectionBudget)
	require.Equal(t, sourceReviewCitationBudgetForBase(base), citationBudget)
}

func TestSourceReviewCitationBudgetUsesActualModalityAndRowBytes(t *testing.T) {
	base := map[string]any{
		"document_image_observations":      []any{map[string]any{"description": strings.Repeat("image-observation", 1200), "page": 7}},
		"structured_query_row_companion":   map[string]any{"rows": []any{map[string]any{"cells": strings.Repeat("cell", 400)}}},
		"document_table_execution_summary": []any{map[string]any{"table": strings.Repeat("table-cell", 150)}},
	}
	budget := sourceReviewCitationBudgetForBase(base)
	require.Less(t, budget, sourceReviewCitationBudget)
	require.LessOrEqual(t, sourceReviewProjectionSize(base)+budget+sourceReviewBindingBudget+512, sourceReviewSharedProjectionBudget)
}

func TestCitationReviewUnknownEmptyBodyIsNotCompleteSourceEvidence(t *testing.T) {
	view := map[string]any{}
	registry := newCitationReviewRegistry(view)
	item := citationReviewItem(citationReviewChunk{chunkID: "unknown-empty", contentKnown: false}, 0, false, registry)
	require.Equal(t, false, item["content_known"])
	require.Equal(t, "content_unknown", item["content_state"])
	require.Equal(t, false, item["review_segment_complete"])
}
