package agent

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	// The cited-source view gets the room previously consumed by repeated
	// generic/document bodies. The combined review projection remains below its
	// former independent maxima instead of increasing the reviewer prompt.
	sourceReviewSharedProjectionBudget = 64 * 1024
	sourceReviewToolResultsBudget      = 4 * 1024
	sourceReviewDocumentBudget         = 4 * 1024
	sourceReviewBindingBudget          = 4 * 1024
	sourceReviewCatalogBudget          = 2 * 1024
	sourceReviewToolDirectoryBudget    = 2 * 1024
	sourceReviewCitationBudget         = 48 * 1024
)

// All modality adapters derive from one current-turn result snapshot. The
// serialized view and reader remain fixed across protocol retries; neither a
// malformed verdict nor another adapter grants a fresh inspection allowance.
type sourceReviewSnapshot struct {
	input         map[string]any
	reader        *reviewEvidenceReader
	sourceRecords *reviewEvidenceReader
	hasImages     bool
}

func (e *AgentEngine) buildSourceReviewSnapshot(ctx context.Context, content string, state *types.AgentState, messages []chat.Message) *sourceReviewSnapshot {
	inputData := map[string]any{}
	if plan := e.taskReviewPlan(); plan != nil {
		inputData["advisory_task_plan"] = plan
	}
	var evidenceReader *reviewEvidenceReader
	sourceMessages := e.sourceConsistencyMessages(state, messages)
	sourceRecords := newReviewEvidenceReader(sourceMessages)
	hasImageObservations := false
	evidence, scope := boundedReviewEvidence(sourceMessages, sourceReviewToolResultsBudget)
	inputData["source_tool_results"] = evidence
	inputData["evidence_scope"] = scope
	rowEvidence := structuredQueryRowReviewEvidence(state, sourceMessages, structuredQueryRowReviewBudget)
	if rowEvidence != nil {
		inputData["structured_query_row_companion"] = rowEvidence
	}
	documentEvidence := documentRetrievalCompanionWithBudget(sourceMessages, sourceReviewDocumentBudget)
	if documentEvidence != nil {
		inputData["document_retrieval_companion"] = documentEvidence
		inputData["document_retrieval_companion_notice"] = documentRetrievalCompanionInstructions
	}
	catalogEvidence := documentCatalogReviewEvidence(sourceMessages, sourceReviewCatalogBudget, true)
	inputData["source_catalog_companion"] = catalogEvidence
	rowEvidenceIncomplete := rowEvidence != nil && rowEvidence["projection_complete"] != true
	documentEvidenceIncomplete := documentEvidence != nil && documentEvidence["projection_complete"] != true
	imageObservations, imageScope := documentImageConsistencySummariesWithSources(state, sourceMessages)
	hasImageObservations = len(imageObservations) > 0
	inputData["document_image_observations"] = imageObservations
	inputData["document_image_observation_scope"] = imageScope
	tableExecutions, tableExecutionScope := documentTableConsistencySummaries(state)
	inputData["document_table_execution_summary"] = tableExecutions
	inputData["document_table_execution_scope"] = tableExecutionScope
	inputData["available_tool_directory"] = compactSourceReviewToolDirectory(e.sourceReviewToolDirectory(), sourceReviewToolDirectoryBudget)
	inputData["evidence_notice"] = "This bounded view contains only this turn's real tool results and runtime access metadata. view_complete does not prove the original source is complete. Respect original truncation, pagination, coverage, and omitted_paths. A missing projected field is unknown, not absent. Plans, schemas, directory candidates, assistant prose and failed calls are not source facts."
	citationBudget := sourceReviewCitationBudgetForBase(inputData)
	citationEvidence := citedSourceEvidenceFromMessagesWithBudget(sourceMessages, content, citationBudget, true, sourceReviewCitationBudget)
	inputData["citation_source_evidence"] = citationEvidence
	if !scope.ViewComplete || citationEvidence["view_complete"] != true || catalogEvidence["view_complete"] != true || rowEvidenceIncomplete || documentEvidenceIncomplete || !imageScope.ViewComplete || !tableExecutionScope.ViewComplete {
		evidenceReader = sourceRecords
		inputData["review_evidence_access"] = "Use evidence_id handles with read_review_evidence to restore omitted real result paths."
	}
	bindingEvidence := citationBindingViewPositionsOnly(content, sourceMessages, citationEvidence, evidenceReader != nil, sourceReviewBindingBudget)
	if bindingEvidence != nil && bindingEvidence["projection_complete"] != true && evidenceReader == nil {
		evidenceReader = sourceRecords
		inputData["review_evidence_access"] = "Use evidence_id handles with read_review_evidence to restore omitted real result paths."
		bindingEvidence = citationBindingViewPositionsOnly(content, sourceMessages, citationEvidence, true, sourceReviewBindingBudget)
	}
	if bindingEvidence != nil {
		inputData["citation_binding_view"] = bindingEvidence
	}
	packetScope := map[string]any{
		"budget_target_bytes": sourceReviewSharedProjectionBudget,
		"within_target":       true,
		"serialized_bytes":    0,
		"note":                "The target applies to this serialized source packet. Image and table adapters retain their existing independent bounds; exceeding the target does not trim their mappings or imply evidence completeness.",
	}
	inputData["source_review_packet_scope"] = packetScope
	// The size field itself changes the serialized length; converge in case
	// crossing a decimal boundary changes the final count.
	for i := 0; i < 4; i++ {
		size := sourceReviewProjectionSize(inputData)
		if packetScope["serialized_bytes"] == size {
			break
		}
		packetScope["serialized_bytes"] = size
		packetScope["within_target"] = size <= sourceReviewSharedProjectionBudget
	}
	// Record projection state without source text, identities or draft
	// excerpts, so an evidence omission can be distinguished from a model
	// verdict without increasing the model's evidence or authority.
	citationItems, _ := citationEvidence["items"].([]any)
	unknownCitations, _ := citationEvidence["unknown_citation_ids"].([]string)
	ambiguousCitations, _ := citationEvidence["ambiguous_citation_ids"].([]string)
	common.PipelineInfo(ctx, "Agent", "source_review_projection", map[string]interface{}{
		"snapshot_build": true, "source_view_complete": scope.ViewComplete,
		"citation_view_complete":         citationEvidence["view_complete"],
		"unique_citations":               citationInt(citationEvidence["unique_citation_count"]),
		"included_citation_items":        len(citationItems),
		"omitted_citation_items":         citationInt(citationEvidence["omitted_citation_items"]),
		"unknown_citations":              len(unknownCitations) + citationInt(citationEvidence["unknown_citation_ids_omitted"]),
		"ambiguous_citations":            len(ambiguousCitations) + citationInt(citationEvidence["ambiguous_citation_ids_omitted"]),
		"reader_available":               evidenceReader != nil,
		"document_projection_available":  documentEvidence != nil,
		"document_projection_incomplete": documentEvidenceIncomplete,
		"document_source_results":        citationInt(documentEvidence["source_result_count"]),
		"document_source_observations":   citationInt(documentEvidence["source_observation_count"]),
		"document_included_observations": citationInt(documentEvidence["included_observation_count"]),
		"document_omitted_observations":  citationInt(documentEvidence["omitted_observation_count"]),
		"document_unique_bodies":         citationInt(documentEvidence["unique_body_count"]),
		"document_included_bodies":       citationInt(documentEvidence["included_body_count"]),
		"document_omitted_bodies":        citationInt(documentEvidence["omitted_body_count"]),
		"citation_binding_occurrences":   citationInt(bindingEvidence["occurrence_count"]),
		"citation_binding_included":      citationInt(bindingEvidence["included_occurrence_count"]),
		"citation_binding_omitted":       citationInt(bindingEvidence["omitted_occurrence_count"]),
		"citation_binding_complete":      bindingEvidence["projection_complete"],
		"packet_serialized_bytes":        packetScope["serialized_bytes"],
		"packet_budget_target_bytes":     packetScope["budget_target_bytes"],
		"packet_within_target":           packetScope["within_target"],
	})
	return &sourceReviewSnapshot{input: inputData, reader: evidenceReader, sourceRecords: sourceRecords, hasImages: hasImageObservations}
}

func sourceReviewCitationBudgetForBase(input map[string]any) int {
	available := sourceReviewSharedProjectionBudget - sourceReviewProjectionSize(input) - sourceReviewBindingBudget - 512
	if available < citationReviewMinimumBudget {
		return citationReviewMinimumBudget
	}
	return min(sourceReviewCitationBudget, available)
}

func compactSourceReviewToolDirectory(directory map[string]any, budget int) map[string]any {
	tools, _ := directory["available_tools"].([]taskToolCapability)
	names := make([]string, 0, len(tools))
	for _, capability := range tools {
		names = append(names, capability.Name)
	}
	view := map[string]any{
		"scope": directory["scope"], "directory_complete": directory["directory_complete"],
		"complete_within_scope":    directory["complete_within_scope"],
		"directory_is_source_fact": false, "available_tools": names,
		"notice": "Registered tool names only; capability descriptions are omitted. This directory is not evidence that a tool ran or a source was read.",
	}
	for budget > 0 && sourceReviewProjectionSize(map[string]any{"directory": view}) > budget && len(names) > 0 {
		names = names[:len(names)-1]
		view["available_tools"] = names
		view["directory_complete"] = false
		view["omitted_tool_names"] = len(tools) - len(names)
	}
	return view
}

func sourceReviewProjectionSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return len(encoded)
}
