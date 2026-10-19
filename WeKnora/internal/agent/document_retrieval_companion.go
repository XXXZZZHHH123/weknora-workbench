package agent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
)

const documentRetrievalCompanionBudget = 12 * 1024
const documentRetrievalCompanionOmissionDetailLimit = 64

type documentRetrievalCompanionView struct {
	BudgetBytes                int                            `json:"budget_bytes"`
	ProjectionComplete         bool                           `json:"projection_complete"`
	SourceResultCount          int                            `json:"source_result_count"`
	IncludedSourceResultCount  int                            `json:"included_source_result_count"`
	OmittedSourceResultCount   int                            `json:"omitted_source_result_count"`
	SourceObservationCount     int                            `json:"source_observation_count"`
	UniqueObservationCount     int                            `json:"unique_observation_count"`
	IncludedObservationCount   int                            `json:"included_observation_count"`
	OmittedObservationCount    int                            `json:"omitted_observation_count"`
	UniqueBodyCount            int                            `json:"unique_body_count"`
	IncludedBodyCount          int                            `json:"included_body_count"`
	OmittedBodyCount           int                            `json:"omitted_body_count"`
	Scopes                     []documentRetrievalScope       `json:"scopes"`
	Bodies                     []documentRetrievalBody        `json:"bodies"`
	Observations               []documentRetrievalObservation `json:"observations"`
	OmittedSourceResults       []map[string]string            `json:"omitted_source_results,omitempty"`
	OmittedObservations        []map[string]string            `json:"omitted_observations,omitempty"`
	OmittedBodies              []map[string]string            `json:"omitted_bodies,omitempty"`
	OmittedSourceResultDetails int                            `json:"omitted_source_result_details,omitempty"`
	OmittedObservationDetails  int                            `json:"omitted_observation_details,omitempty"`
	OmittedBodyDetails         int                            `json:"omitted_body_details,omitempty"`
	CoverageNote               string                         `json:"coverage_note"`
}

type documentRetrievalScope struct {
	EvidenceID       string         `json:"evidence_id"`
	JSONPointer      string         `json:"json_pointer"`
	Tool             string         `json:"tool"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Metadata         map[string]any `json:"metadata"`
	MetadataComplete bool           `json:"metadata_complete"`
	MetadataPointer  string         `json:"metadata_pointer,omitempty"`
	FullMetadata     map[string]any `json:"-"`
	ExecutionPointer string         `json:"execution_scope_json_pointer,omitempty"`
}

type documentRetrievalBody struct {
	BodyID    string `json:"body_id"`
	Content   string `json:"content"`
	UTF8Bytes int    `json:"utf8_bytes"`
}

type documentRetrievalObservation struct {
	EvidenceID       string         `json:"evidence_id"`
	JSONPointer      string         `json:"json_pointer"`
	RowJSONPointer   string         `json:"row_json_pointer"`
	Tool             string         `json:"tool"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	KnowledgeBaseID  string         `json:"knowledge_base_id,omitempty"`
	KnowledgeID      string         `json:"knowledge_id"`
	ChunkID          string         `json:"chunk_id"`
	BodyID           string         `json:"body_id"`
	BodyIncluded     bool           `json:"body_included"`
	DuplicateCount   int            `json:"duplicate_observation_count,omitempty"`
	ContentUTF8Bytes int            `json:"content_utf8_bytes"`
	SourceMetadata   map[string]any `json:"source_metadata"`
	MetadataComplete bool           `json:"metadata_complete"`
	MetadataPointer  string         `json:"metadata_pointer,omitempty"`
}

type documentRetrievalCandidate struct {
	observation  documentRetrievalObservation
	content      string
	fullMetadata map[string]any
}

// documentRetrievalCompanion gives source review a mechanically
// balanced view of actual successful document retrieval rows. It does not use
// the question, candidate answer, row text, or citation selection to rank rows.
// Body deduplication is only an exact UTF-8 encoding optimization; source
// identities, scopes, offsets, and immutable reader pointers remain per row.
func documentRetrievalCompanion(messages []chat.Message, budget int) map[string]any {
	return documentRetrievalCompanionWithBudget(messages, budget)
}

func documentRetrievalCompanionWithBudget(messages []chat.Message, requestedBudget int) map[string]any {
	if requestedBudget <= 0 {
		return nil
	}
	budget := min(documentRetrievalCompanionBudget, requestedBudget)
	view := documentRetrievalCompanionView{
		BudgetBytes: budget, ProjectionComplete: true,
		Scopes: []documentRetrievalScope{}, Bodies: []documentRetrievalBody{}, Observations: []documentRetrievalObservation{},
		OmittedSourceResults: []map[string]string{}, OmittedObservations: []map[string]string{}, OmittedBodies: []map[string]string{},
		CoverageNote: "Completeness covers returned rows/metadata only, not full-source retrieval or semantic absence. Equal text is encoded once, not as evidence of source or underlying-data equivalence. Scope records join observations by evidence_id; preserve original truncation/pagination.",
	}

	seenResults := map[string]bool{}
	allScopes := []documentRetrievalScope{}
	candidates := []documentRetrievalCandidate{}
	unprojectableRows := []map[string]string{}
	groupOrder := []string{}
	groups := map[string][]int{}
	bodyIDs := map[string]string{}
	deduplicatedObservations := map[string]int{}
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
		evidenceID := reviewEvidenceID(message)
		if seenResults[evidenceID] {
			continue
		}
		seenResults[evidenceID] = true
		executionScope, hasExecutionScope := payload["execution_scope"]
		metadata := documentRetrievalScopeMetadata(data)
		if hasExecutionScope {
			metadata["execution_scope"] = executionScope
		}
		fullScopeMetadata := documentRetrievalScopeFullMetadata(data)
		executionPointer := ""
		if hasExecutionScope {
			fullScopeMetadata["execution_scope"] = executionScope
			executionPointer = "/execution_scope"
		}
		allScopes = append(allScopes, documentRetrievalScope{EvidenceID: evidenceID, JSONPointer: pointerPrefix, Tool: message.Name, ToolCallID: message.ToolCallID, Metadata: metadata, MetadataComplete: false, MetadataPointer: pointerPrefix, FullMetadata: fullScopeMetadata, ExecutionPointer: executionPointer})
		scopeKey := documentRetrievalScopeFullMetadata(data)
		if hasExecutionScope {
			scopeKey["execution_scope"] = executionScope
		} else {
			// Without the actual invocation arguments, two calls cannot be
			// assumed to represent the same query even if their returned rows match.
			scopeKey["unavailable_execution_scope_evidence_id"] = evidenceID
		}
		scopeEncoding, _ := json.Marshal(scopeKey)

		for _, field := range []string{"chunks", "results"} {
			rows, ok := data[field].([]any)
			if !ok {
				continue
			}
			for index, raw := range rows {
				view.SourceObservationCount++
				rowPointer := fmt.Sprintf("%s/%s/%d", pointerPrefix, field, index)
				row, ok := raw.(map[string]any)
				if !ok {
					unprojectableRows = append(unprojectableRows, map[string]string{"evidence_id": evidenceID, "json_pointer": rowPointer})
					continue
				}
				chunkID, _ := row["chunk_id"].(string)
				knowledgeID, _ := row["knowledge_id"].(string)
				content, contentOK := row["content"].(string)
				if chunkID == "" || knowledgeID == "" || !contentOK {
					unprojectableRows = append(unprojectableRows, map[string]string{"evidence_id": evidenceID, "json_pointer": rowPointer})
					continue
				}
				knowledgeBaseID, _ := row["knowledge_base_id"].(string)
				groupIdentity := []string{knowledgeBaseID, knowledgeID}
				if knowledgeBaseID == "" {
					groupIdentity = append(groupIdentity, evidenceID)
				}
				groupKeyBytes, _ := json.Marshal(groupIdentity)
				groupKey := string(groupKeyBytes)
				if _, exists := groups[groupKey]; !exists {
					groupOrder = append(groupOrder, groupKey)
				}
				sourceMetadata := cloneDocumentRetrievalCriticalMetadata(row)
				fullRowMetadata := cloneDocumentRetrievalRowMetadata(row)
				candidate := documentRetrievalCandidate{
					observation: documentRetrievalObservation{
						EvidenceID: evidenceID, JSONPointer: rowPointer + "/content", RowJSONPointer: rowPointer,
						Tool: message.Name, ToolCallID: message.ToolCallID,
						KnowledgeBaseID: knowledgeBaseID, KnowledgeID: knowledgeID, ChunkID: chunkID,
						ContentUTF8Bytes: len(content), SourceMetadata: sourceMetadata,
						MetadataComplete: false, MetadataPointer: rowPointer,
					},
					content: content, fullMetadata: fullRowMetadata,
				}
				if _, ok := bodyIDs[content]; !ok {
					bodyIDs[content] = fmt.Sprintf("body-%d", len(bodyIDs)+1)
				}
				candidate.observation.BodyID = bodyIDs[content]
				rowEncoding, _ := json.Marshal(fullRowMetadata)
				observationKeyBytes, keyErr := json.Marshal([]any{message.Name, json.RawMessage(scopeEncoding), groupKey, chunkID, content, json.RawMessage(rowEncoding)})
				observationKey := string(observationKeyBytes)
				if keyErr != nil {
					observationKey = "unmergeable:" + evidenceID + ":" + candidate.observation.RowJSONPointer
				}
				if existing, ok := deduplicatedObservations[observationKey]; ok {
					candidates[existing].observation.DuplicateCount++
					continue
				}
				deduplicatedObservations[observationKey] = len(candidates)
				candidates = append(candidates, candidate)
				groups[groupKey] = append(groups[groupKey], len(candidates)-1)
			}
		}
	}
	view.SourceResultCount = len(allScopes)
	view.UniqueObservationCount = len(candidates)
	view.UniqueBodyCount = len(bodyIDs)
	if len(candidates) == 0 && len(allScopes) == 0 {
		return nil
	}

	// Round-robin candidate order. First reserve one compact identity per
	// document; then each later pass adds another identity together with its
	// body/metadata, preventing identity-only rows from consuming the budget.
	order := []int{}
	maxRows := 0
	for _, key := range groupOrder {
		maxRows = max(maxRows, len(groups[key]))
	}
	for row := 0; row < maxRows; row++ {
		for _, key := range groupOrder {
			if row < len(groups[key]) {
				order = append(order, groups[key][row])
			}
		}
	}
	includedCandidate := map[int]int{}
	omittedCandidate := map[int]bool{}
	includedBodies := map[string]bool{}
	addIdentity := func(candidateIndex int) bool {
		candidate := candidates[candidateIndex]
		candidate.observation.BodyIncluded = false
		view.Observations = append(view.Observations, candidate.observation)
		if !documentRetrievalFits(view, budget) {
			view.Observations = view.Observations[:len(view.Observations)-1]
			return false
		}
		view.IncludedObservationCount++
		includedCandidate[candidateIndex] = len(view.Observations) - 1
		return true
	}
	for _, key := range groupOrder {
		if len(groups[key]) == 0 {
			continue
		}
		index := groups[key][0]
		if !addIdentity(index) {
			omittedCandidate[index] = true
			view.ProjectionComplete = false
			candidate := candidates[index]
			appendDocumentRetrievalOmission(&view.OmittedObservations, &view.OmittedObservationCount, &view.OmittedObservationDetails,
				map[string]string{"evidence_id": candidate.observation.EvidenceID, "json_pointer": candidate.observation.RowJSONPointer}, &view, budget)
		}
	}
	for _, candidateIndex := range order {
		candidate := candidates[candidateIndex]
		obsIndex, retained := includedCandidate[candidateIndex]
		if !retained {
			if omittedCandidate[candidateIndex] {
				continue
			}
			if !addIdentity(candidateIndex) {
				omittedCandidate[candidateIndex] = true
				view.ProjectionComplete = false
				appendDocumentRetrievalOmission(&view.OmittedObservations, &view.OmittedObservationCount, &view.OmittedObservationDetails,
					map[string]string{"evidence_id": candidate.observation.EvidenceID, "json_pointer": candidate.observation.RowJSONPointer}, &view, budget)
				continue
			}
			obsIndex = includedCandidate[candidateIndex]
		}
		if includedBodies[candidate.content] {
			view.Observations[obsIndex].BodyIncluded = true
		} else {
			view.Bodies = append(view.Bodies, documentRetrievalBody{BodyID: candidate.observation.BodyID, Content: candidate.content, UTF8Bytes: len(candidate.content)})
			view.Observations[obsIndex].BodyIncluded = true
			if documentRetrievalFits(view, budget) {
				includedBodies[candidate.content] = true
			} else {
				view.Bodies = view.Bodies[:len(view.Bodies)-1]
				view.Observations[obsIndex].BodyIncluded = false
			}
		}
		// Enrich the compact source identity with every original row field except
		// content. If the complete metadata does not fit, retain the real pointer.
		previous := view.Observations[obsIndex].SourceMetadata
		view.Observations[obsIndex].SourceMetadata = candidate.fullMetadata
		view.Observations[obsIndex].MetadataComplete = true
		view.Observations[obsIndex].MetadataPointer = ""
		if !documentRetrievalFits(view, budget) {
			view.Observations[obsIndex].SourceMetadata = previous
			view.Observations[obsIndex].MetadataComplete = false
			view.Observations[obsIndex].MetadataPointer = candidate.observation.RowJSONPointer
			view.ProjectionComplete = false
		}
		if !view.Observations[obsIndex].BodyIncluded {
			view.ProjectionComplete = false
		}
	}
	view.IncludedBodyCount = len(includedBodies)
	for candidateIndex, observationIndex := range includedCandidate {
		view.Observations[observationIndex].BodyIncluded = includedBodies[candidates[candidateIndex].content]
	}
	omittedBodies := map[string]bool{}
	for _, candidate := range candidates {
		if includedBodies[candidate.content] || omittedBodies[candidate.content] {
			continue
		}
		omittedBodies[candidate.content] = true
		view.ProjectionComplete = false
		appendDocumentRetrievalOmission(&view.OmittedBodies, &view.OmittedBodyCount, &view.OmittedBodyDetails,
			map[string]string{"evidence_id": candidate.observation.EvidenceID, "json_pointer": candidate.observation.JSONPointer}, &view, budget)
	}
	// Preserve scope metadata where it fits; unknown original fields remain
	// reachable from the actual payload pointer.
	for _, scope := range allScopes {
		view.Scopes = append(view.Scopes, scope)
		index := len(view.Scopes) - 1
		view.Scopes[index].Metadata = scope.FullMetadata
		view.Scopes[index].MetadataComplete = true
		view.Scopes[index].MetadataPointer = ""
		if !documentRetrievalFits(view, budget) {
			view.Scopes[index].Metadata = scope.Metadata
			view.Scopes[index].MetadataComplete = false
			view.Scopes[index].MetadataPointer = scope.JSONPointer
			view.ProjectionComplete = false
		}
		if !documentRetrievalFits(view, budget) {
			view.Scopes = view.Scopes[:len(view.Scopes)-1]
			view.ProjectionComplete = false
			omission := map[string]string{"evidence_id": scope.EvidenceID, "json_pointer": scope.JSONPointer}
			if scope.ExecutionPointer != "" {
				omission["execution_scope_json_pointer"] = scope.ExecutionPointer
			}
			appendDocumentRetrievalOmission(&view.OmittedSourceResults, &view.OmittedSourceResultCount, &view.OmittedSourceResultDetails, omission, &view, budget)
		}
	}
	view.IncludedSourceResultCount = len(view.Scopes)
	for _, omission := range unprojectableRows {
		view.ProjectionComplete = false
		appendDocumentRetrievalOmission(&view.OmittedObservations, &view.OmittedObservationCount, &view.OmittedObservationDetails, omission, &view, budget)
	}
	result := documentRetrievalCompanionMap(view)
	return result
}

func documentRetrievalScopeMetadata(data map[string]any) map[string]any {
	metadata := make(map[string]any)
	for _, key := range []string{
		"query", "queries", "mode", "requested_mode", "knowledge_base_ids", "document_ids",
		"count", "total", "fetched_chunks", "omitted_for_budget", "partial_failures", "search_failures",
		"truncated", "offset", "next_offset", "query_scope", "withheld_retracted_or_unverified_wiki_chunks",
	} {
		if value, ok := data[key]; ok {
			metadata[key] = value
		}
	}
	return metadata
}

func documentRetrievalScopeFullMetadata(data map[string]any) map[string]any {
	metadata := make(map[string]any, len(data))
	for key, value := range data {
		if key == "chunks" || key == "results" {
			continue
		}
		metadata[key] = value
	}
	return metadata
}

func cloneDocumentRetrievalRowMetadata(row map[string]any) map[string]any {
	metadata := make(map[string]any, len(row)-1)
	for key, value := range row {
		if key != "content" {
			metadata[key] = value
		}
	}
	return metadata
}

func cloneDocumentRetrievalCriticalMetadata(row map[string]any) map[string]any {
	metadata := make(map[string]any)
	for _, key := range []string{
		"content_offset", "content_offset_space", "content_limit", "content_complete", "next_content_offset",
		"role", "content_status", "offset_space",
	} {
		if value, ok := row[key]; ok {
			metadata[key] = value
		}
	}
	return metadata
}

func documentRetrievalFits(view documentRetrievalCompanionView, budget int) bool {
	encoded, err := json.Marshal(view)
	return err == nil && len(encoded) <= budget
}

func documentRetrievalCompanionMap(view documentRetrievalCompanionView) map[string]any {
	encoded, err := json.Marshal(view)
	if err == nil && len(encoded) > view.BudgetBytes {
		// Final omission counters can add keys/digits after the last successful
		// row fit. Prefer shortening this redundant protocol notice and removing
		// optional omission details to discarding all retained source identities.
		view.CoverageNote = "Returned results only; omissions remain unknown. Join scopes by evidence_id."
		encoded, err = json.Marshal(view)
		for err == nil && len(encoded) > view.BudgetBytes {
			switch {
			case len(view.OmittedBodies) > 0:
				view.OmittedBodies = view.OmittedBodies[:len(view.OmittedBodies)-1]
				view.OmittedBodyDetails++
			case len(view.OmittedObservations) > 0:
				view.OmittedObservations = view.OmittedObservations[:len(view.OmittedObservations)-1]
				view.OmittedObservationDetails++
			case len(view.OmittedSourceResults) > 0:
				view.OmittedSourceResults = view.OmittedSourceResults[:len(view.OmittedSourceResults)-1]
				view.OmittedSourceResultDetails++
			default:
				goto finalized
			}
			encoded, err = json.Marshal(view)
		}
	}
finalized:
	if err != nil || len(encoded) > view.BudgetBytes {
		minimal := map[string]any{"budget_bytes": view.BudgetBytes, "projection_complete": false,
			"source_result_count": view.SourceResultCount, "included_source_result_count": 0,
			"omitted_source_result_count": view.SourceResultCount, "source_observation_count": view.SourceObservationCount,
			"unique_observation_count":   view.UniqueObservationCount,
			"included_observation_count": 0, "omitted_observation_count": view.OmittedObservationCount + view.IncludedObservationCount,
			"unique_body_count": view.UniqueBodyCount, "included_body_count": 0, "omitted_body_count": view.UniqueBodyCount,
			"notice": "Document retrieval companion exceeded its independent byte budget; consult source_tool_results evidence handles."}
		minimalBytes, _ := json.Marshal(minimal)
		if len(minimalBytes) <= view.BudgetBytes {
			return minimal
		}
		return nil
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&result) != nil {
		return nil
	}
	return result
}

func appendDocumentRetrievalOmission(target *[]map[string]string, omittedCount, omittedDetailCount *int, omission map[string]string, view *documentRetrievalCompanionView, budget int) {
	(*omittedCount)++
	if len(*target) < documentRetrievalCompanionOmissionDetailLimit {
		*target = append(*target, omission)
		if !documentRetrievalFits(*view, budget) {
			*target = (*target)[:len(*target)-1]
			(*omittedDetailCount)++
		}
	} else {
		(*omittedDetailCount)++
	}
}
