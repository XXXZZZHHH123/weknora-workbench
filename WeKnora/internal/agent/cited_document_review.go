package agent

import (
	"encoding/json"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// This optional editor receives only the candidate's actual current references.
// The existing citation path checks their identity, version, registration and
// source views. Do not silently select a subset, trim bodies, use historical
// transcripts as current evidence, or upgrade a partial view to a full chunk. The entire authorized current view
// is retained; a bounded source view is not missing reviewer input.
func (e *AgentEngine) currentCitedDocumentReviewEvidence(candidate string, state *types.AgentState, conversation []chat.Message) ([]map[string]any, bool) {
	ids := uniqueCandidateCitationIDs(candidate)
	if len(ids) == 0 {
		return nil, true
	}
	refs := e.currentCandidateKnowledgeReferences(candidate, state, conversation)
	if len(refs) != len(ids) {
		return nil, false
	}
	views := map[string][]map[string]any{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if (call.ExecutionName() != "read_document" && call.ExecutionName() != "search_knowledge") || call.Result == nil || !call.Result.Success {
				continue
			}
			for _, field := range []string{"chunks", "results"} {
				raw, err := json.Marshal(call.Result.Data[field])
				var rows []map[string]any
				if err != nil || json.Unmarshal(raw, &rows) != nil {
					continue
				}
				for _, row := range rows {
					views[citationString(row["chunk_id"])] = append(views[citationString(row["chunk_id"])], row)
				}
			}
		}
	}
	documents := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		var scope map[string]any
		for _, row := range views[ref.ID] {
			if citationString(row["content"]) != ref.Content {
				continue
			}
			scope = selectCitationFields(row, []string{"content_offset", "content_offset_space", "content_complete", "content_source_view", "content_rewritten"})
			if citationViewIsFull(citationSourceView{row: row}) {
				break
			}
		}
		if scope == nil {
			return nil, false
		}
		documents = append(documents, map[string]any{
			"citation_handle": e.modelContext.CitableChunkHandle(ref.ID),
			"document_title":  ref.KnowledgeTitle, "content": ref.Content,
			"content_revision": ref.ContentRevision, "source_locators": ref.SourceLocators,
			"source_view": scope,
			"scope":       "entire current cited view; source_view may bound a partial chunk; not a complete document",
		})
	}
	return documents, true
}

// No citation markup is not evidence that no document was read. This fallback
// retains every distinct current native read/search view, without manufacturing
// citations or upgrading its scope. Qualification uses the same current source
// identity/version/registry checks as citations; uncertainty skips the optional
// editor rather than silently omitting an actual body.
func (e *AgentEngine) currentReadDocumentReviewEvidence(state *types.AgentState, conversation []chat.Message) ([]map[string]any, bool) {
	if e == nil || state == nil || e.modelContext == nil {
		return nil, true
	}
	records, conflicts := citationChunksFromMessages(e.sourceConsistencyMessages(state, conversation))
	refs, rowConflicts := currentCitationReferenceRows(state)
	documents := []map[string]any{}
	seen := map[string]int{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if (call.ExecutionName() != "read_document" && call.ExecutionName() != "search_knowledge") || call.Result == nil || !call.Result.Success {
				continue
			}
			for _, field := range []string{"chunks", "results"} {
				raw, err := json.Marshal(call.Result.Data[field])
				var rows []map[string]any
				if err != nil || json.Unmarshal(raw, &rows) != nil {
					return nil, false
				}
				for _, row := range rows {
					if strings.TrimSpace(citationString(row["content"])) == "" {
						continue
					}
					id := citationString(row["chunk_id"])
					record, exists := records[id]
					ref := refs[id]
					if !exists || conflicts[id] || rowConflicts[id] || ref == nil || e.modelContext.CitableChunkHandle(id) == "" ||
						!record.contentKnown || record.content != ref.Content || record.knowledgeID != ref.KnowledgeID ||
						citationString(row["knowledge_id"]) != ref.KnowledgeID {
						return nil, false
					}
					// Locator quotes are viewer alignment snippets, not additional
					// read bodies. Keep all positioning metadata, while making their
					// omission explicit. Original Data remains the validator input.
					locators, ok := readReviewLocatorPositions(row["source_locators"])
					if !ok {
						return nil, false
					}
					view := map[string]any{
						"chunk_id": id, "knowledge_id": ref.KnowledgeID, "knowledge_base_id": ref.KnowledgeBaseID,
						"document_title": ref.KnowledgeTitle, "content": row["content"], "content_revision": row["content_revision"],
						"source_locators": locators, "locator_quotes_omitted": true, "document_scope": call.Result.Data["document_scope"],
						"source_identity": selectCitationFields(row, []string{"chunk_index", "chunk_type", "start_at", "end_at", "parent_chunk_id"}),
						"source_view":     selectCitationFields(row, []string{"content_offset", "content_offset_space", "content_complete", "content_source_view", "content_rewritten"}),
						"scope":           "entire current authorized read/search view; source_view may bound a partial chunk; not a complete document or a new citation",
					}
					key := reviewDigest(view)
					if index, exists := seen[key]; exists {
						documents[index]["tool_call_ids"] = append(documents[index]["tool_call_ids"].([]string), call.ID)
						continue
					}
					seen[key] = len(documents)
					view["tool_call_ids"] = []string{call.ID}
					documents = append(documents, view)
				}
			}
		}
	}
	return documents, true
}

func readReviewLocatorPositions(value any) (any, bool) {
	if value == nil {
		return nil, true
	}
	raw, err := json.Marshal(value)
	var locators []map[string]any
	if err != nil || json.Unmarshal(raw, &locators) != nil {
		return nil, false
	}
	for _, locator := range locators {
		delete(locator, "quote")
	}
	return locators, true
}

// Share identical document/scope metadata without dropping any body or view.
// A field is inherited only when present and exactly equal in every group row;
// absent fields and explicit nulls remain distinct.
func compactCurrentReadDocuments(views []map[string]any) []map[string]any {
	groups := []map[string]any{}
	indexes := map[string]int{}
	for _, view := range views {
		key := reviewDigest(selectCitationFields(view, []string{"knowledge_id", "knowledge_base_id", "document_title", "document_scope"}))
		index, ok := indexes[key]
		if !ok {
			index = len(groups)
			indexes[key] = index
			groups = append(groups, map[string]any{"views": []map[string]any{}})
		}
		row := map[string]any{}
		for field, value := range view {
			row[field] = value
		}
		groups[index]["views"] = append(groups[index]["views"].([]map[string]any), row)
	}
	for _, group := range groups {
		rows := group["views"].([]map[string]any)
		shared := map[string]any{}
		for field, value := range rows[0] {
			if field == "content" || field == "chunk_id" {
				continue
			}
			same := true
			for _, row := range rows[1:] {
				other, present := row[field]
				if !present || reviewDigest(other) != reviewDigest(value) {
					same = false
					break
				}
			}
			if same {
				shared[field] = value
			}
		}
		for _, row := range rows {
			for field := range shared {
				delete(row, field)
			}
		}
		group["shared_fields"] = shared
	}
	return groups
}
