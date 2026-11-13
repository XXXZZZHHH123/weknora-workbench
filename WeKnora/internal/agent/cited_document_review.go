package agent

import (
	"encoding/json"

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
