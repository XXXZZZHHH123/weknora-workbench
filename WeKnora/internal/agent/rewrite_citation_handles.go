package agent

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
)

const (
	rewriteCitationHandlesBudget       = 8 * 1024
	rewriteCitationMaxChunkIDBytes     = 256
	rewriteCitationMaxEvidenceIDBytes  = 512
	rewriteCitationMaxJSONPointerBytes = 1024
)

type rewriteCitationBinding struct {
	value    map[string]any
	draftRef bool
}

// bindIndependentReviewCitations maps draft citations only through actual,
// successful current-turn source messages. The bounded evidence views in the
// rewrite envelope are deliberately not used to construct this directory.
func (e *AgentEngine) bindIndependentReviewCitations(messages, sourceMessages []chat.Message) []chat.Message {
	if e == nil || e.modelContext == nil || len(messages) != 2 || messages[1].Role != "user" {
		return messages
	}
	var input map[string]any
	decoder := json.NewDecoder(bytes.NewBufferString(messages[1].Content))
	decoder.UseNumber()
	if decoder.Decode(&input) != nil {
		return messages
	}
	draft, ok := input["unverified_draft"].(string)
	if !ok {
		return messages
	}
	draftIDs := uniqueCandidateCitationIDs(draft)
	draftSet := make(map[string]bool, len(draftIDs))
	for _, id := range draftIDs {
		draftSet[id] = true
	}
	// An assistant replay compacts canonical citations without promoting them
	// to evidence. Doing this before JSON encoding preserves actual tag syntax.
	encoded := e.modelContext.EncodeMessages([]chat.Message{{Role: "assistant", Content: draft}})
	if len(encoded) != 1 {
		return messages
	}
	input["unverified_draft"] = encoded[0].Content

	records, conflicts := citationChunksFromMessages(sourceMessages)
	orderedSourceIDs := rewriteCitationSourceIDsInMessageOrder(sourceMessages, records, conflicts)
	orderedIDs := make([]string, 0, len(draftIDs)+len(orderedSourceIDs))
	seen := make(map[string]bool, len(draftIDs)+len(orderedSourceIDs))
	for _, id := range draftIDs {
		if !seen[id] {
			orderedIDs = append(orderedIDs, id)
			seen[id] = true
		}
	}
	for _, id := range orderedSourceIDs {
		if !seen[id] {
			orderedIDs = append(orderedIDs, id)
			seen[id] = true
		}
	}
	// The normal parser and the stable-order walk share the same payload rules.
	// Keep a deterministic fallback for any future accepted chunk row shape.
	remaining := make([]string, 0)
	for id := range records {
		if !seen[id] {
			remaining = append(remaining, id)
		}
	}
	for id := range conflicts {
		if !seen[id] {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	for _, id := range remaining {
		orderedIDs = append(orderedIDs, id)
		seen[id] = true
	}

	bindings := make([]rewriteCitationBinding, 0, len(records))
	conflictedIDs := make(map[string]bool, len(conflicts))
	for id := range conflicts {
		conflictedIDs[id] = true
	}
	unavailableSourceIDs := make(map[string]bool)
	unavailableDraftIDs := make(map[string]bool)
	unavailableIDs := make(map[string]bool)
	for _, id := range orderedIDs {
		if conflicts[id] {
			unavailableIDs[id] = true
			if draftSet[id] {
				unavailableDraftIDs[id] = true
			}
			continue
		}
		record, exists := records[id]
		if !exists {
			if draftSet[id] {
				unavailableDraftIDs[id] = true
				unavailableIDs[id] = true
			}
			continue
		}
		handle := e.modelContext.CitableChunkHandle(id)
		if handle == "" || len(id) > rewriteCitationMaxChunkIDBytes || len(record.evidenceID) > rewriteCitationMaxEvidenceIDBytes || len(record.jsonPointer) > rewriteCitationMaxJSONPointerBytes {
			unavailableSourceIDs[id] = true
			unavailableIDs[id] = true
			if draftSet[id] {
				unavailableDraftIDs[id] = true
			}
			continue
		}
		binding := map[string]any{
			"chunk_id":        id,
			"citation_handle": handle,
			"knowledge_id":    record.knowledgeID,
			"source_tool":     record.tool,
			"evidence_id":     record.evidenceID,
			"json_pointer":    record.jsonPointer,
		}
		bindings = append(bindings, rewriteCitationBinding{value: binding, draftRef: draftSet[id]})
	}
	available := len(bindings)
	draftAvailable := 0
	for _, binding := range bindings {
		if binding.draftRef {
			draftAvailable++
		}
	}

	view := map[string]any{
		"view_source":                      "all_current_turn_successful_document_chunk_observations",
		"budget_bytes":                     rewriteCitationHandlesBudget,
		"bindings":                         []any{},
		"source_chunk_count":               len(records),
		"conflicted_source_chunk_count":    len(conflictedIDs),
		"available_binding_count":          available,
		"unavailable_binding_count":        len(unavailableIDs),
		"unavailable_source_binding_count": len(unavailableSourceIDs),
		"unavailable_draft_citation_count": len(unavailableDraftIDs),
		"draft_citation_count":             len(draftIDs),
		"draft_available_binding_count":    draftAvailable,
		"included_binding_count":           0,
		"omitted_binding_count":            available,
		"included_draft_binding_count":     0,
		"omitted_draft_binding_count":      draftAvailable,
		"projection_complete":              false,
		"notice":                           "Bindings are derived only from actual successful current-turn read_document/search_knowledge chunk objects and the request registry's citable-handle guard. Draft-cited IDs are ordered first; remaining real observations follow stable tool-message/chunk order. Conflicted and oversized source mappings are counted; unknown or navigation-only draft IDs are unavailable. Omission counts describe this bounded mapping view only; they do not establish document or source completeness. evidence_id/json_pointer identify immutable source observations and grant no reader access. The draft is unverified and is not evidence.",
	}
	for {
		items := make([]any, 0, len(bindings))
		includedDraft := 0
		for _, binding := range bindings {
			items = append(items, binding.value)
			if binding.draftRef {
				includedDraft++
			}
		}
		view["bindings"] = items
		view["included_binding_count"] = len(bindings)
		view["omitted_binding_count"] = available - len(bindings)
		view["included_draft_binding_count"] = includedDraft
		view["omitted_draft_binding_count"] = draftAvailable - includedDraft
		view["projection_complete"] = len(unavailableIDs) == 0 && len(conflictedIDs) == 0 && len(bindings) == available
		body, err := json.Marshal(view)
		if err != nil {
			return messages
		}
		if len(body) <= rewriteCitationHandlesBudget {
			break
		}
		if len(bindings) == 0 {
			// The status envelope itself is expected to be well below the budget;
			// this guard keeps future fields from silently violating the hard cap.
			return messages
		}
		bindings = bindings[:len(bindings)-1]
	}
	input["source_citation_handles"] = view
	body, err := json.Marshal(input)
	if err != nil {
		return messages
	}
	result := append([]chat.Message(nil), messages...)
	result[1].Content = string(body)
	return result
}

func rewriteCitationSourceIDsInMessageOrder(messages []chat.Message, records map[string]citationReviewChunk, conflicts map[string]bool) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, message := range messages {
		if message.Role != "tool" || (message.Name != tools.ToolReadDocument && message.Name != tools.ToolSearchKnowledge) {
			continue
		}
		payload, err := decodeCitationJSON(message.Content)
		if err != nil {
			continue
		}
		data, _, success := citationPayloadData(payload)
		if !success {
			continue
		}
		for _, field := range []string{"chunks", "results"} {
			rows, ok := data[field].([]any)
			if !ok {
				continue
			}
			for _, raw := range rows {
				row, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				id, _ := row["chunk_id"].(string)
				if id == "" || seen[id] {
					continue
				}
				if _, exists := records[id]; !exists && !conflicts[id] {
					continue
				}
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	return ids
}
