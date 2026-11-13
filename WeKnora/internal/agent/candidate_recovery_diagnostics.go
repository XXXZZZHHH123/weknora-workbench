package agent

import (
	"encoding/json"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Failure categories are safe for normal logs. Source identities and suggested
// navigation stay in the bounded author-facing diagnostic, never public logs.
func (e *AgentEngine) candidateCitationRecovery(candidate string, state *types.AgentState, conversation []chat.Message) (string, map[string]int, bool) {
	counts := map[string]int{}
	if !e.config.CitationsEnabled() {
		return "本轮引用显示已关闭。保留已验证的正文结果，移除引用标记，不要为此重新读取来源。", map[string]int{"citations_disabled": 1}, false
	}
	issues := []map[string]any{}
	records, conflicts := citationChunksFromMessages(e.sourceConsistencyMessages(state, conversation))
	rows, rowConflicts := currentCitationReferenceRows(state)
	views := currentCitationSourceViews(state)
	needsRead := false
	for _, id := range uniqueCandidateCitationIDs(candidate) {
		reason := ""
		record, observed := records[id]
		ref := rows[id]
		switch {
		case conflicts[id] || rowConflicts[id]:
			reason = "conflicting_source_views"
		case !observed || ref == nil:
			reason = "source_not_observed"
		case e.modelContext == nil || e.modelContext.CitableChunkHandle(id) == "":
			reason = "source_not_citable"
		case !record.contentKnown || strings.TrimSpace(record.content) == "":
			reason = "source_body_missing"
		case record.knowledgeID != ref.KnowledgeID || record.content != ref.Content:
			reason = "source_reference_mismatch"
		}
		if reason == "" {
			continue
		}
		issue := map[string]any{"reason_code": reason, "citation_id": taskPlanPrefix(id, 256)}
		// A page-spanning chunk can have two legitimate but distinct page views.
		// Do not combine their claimed origin hashes into approval. Obtain a real
		// full current witness so the existing hash/range/version checks can verify
		// both views. Re-reading the same physical pages cannot provide that witness.
		if reason == "conflicting_source_views" && partialCitationViewsNeedFullWitness(views[id]) && e.modelContext != nil {
			if handle := e.modelContext.CitableChunkHandle(id); handle != "" {
				reason = "partial_views_need_full_chunk"
				issue["reason_code"] = reason
				issue["read_document_args"] = map[string]any{"id": handle, "content_offset": 0}
				needsRead = true
			}
		}
		counts[reason]++
		if len(issues) < 8 {
			issues = append(issues, issue)
		}
	}
	if len(uniqueCandidateEvidenceIDs(candidate)) != len(e.currentCandidateEvidenceReferences(candidate, state)) {
		counts["structured_reference_invalid"]++
	}
	payload, _ := json.Marshal(map[string]any{"kind": "citation_validation_diagnostic", "candidate_sha256": candidateTextDigest(candidate), "issues": issues, "issue_counts": counts, "notice": "Runtime identity diagnostics, not source facts. For partial_views_need_full_chunk read the issued chunk handle WITHOUT page/pages/query to obtain its full current body; follow actual content pagination if needed. Existing version, hash, scope and citation checks remain mandatory. Do not repeat the same physical-page reads or invent a replacement citation. Other missing or conflicting sources require genuine authorized evidence; do not delete requested results."})
	return "答复引用尚未通过本轮来源身份校验。按下列具体原因修复；反馈不是新事实。" + string(payload), counts, needsRead
}

func currentCitationSourceViews(state *types.AgentState) map[string][]citationSourceView {
	views := map[string][]citationSourceView{}
	if state == nil {
		return views
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || (call.ExecutionName() != "read_document" && call.ExecutionName() != "search_knowledge") {
				continue
			}
			for _, field := range []string{"chunks", "results"} {
				raw, err := json.Marshal(call.Result.Data[field])
				if err != nil {
					continue
				}
				var batch []map[string]any
				if json.Unmarshal(raw, &batch) != nil {
					continue
				}
				for _, row := range batch {
					if id := citationString(row["chunk_id"]); id != "" {
						views[id] = append(views[id], citationSourceView{row: row, data: call.Result.Data})
					}
				}
			}
		}
	}
	return views
}

func partialCitationViewsNeedFullWitness(views []citationSourceView) bool {
	if len(views) < 2 {
		return false
	}
	identity := ""
	hasProjection := false
	for _, view := range views {
		current, ok := citationViewIdentity(view)
		if !ok || citationViewIsFull(view) || identity != "" && identity != current {
			return false
		}
		identity = current
		hasProjection = hasProjection || view.row["content_source_view"] != nil
	}
	return hasProjection
}

func repeatedUnsupportedTokens(current, previous []string) []string {
	seen := map[string]bool{}
	for _, token := range previous {
		seen[token] = true
	}
	repeated := []string{}
	for _, token := range current {
		if seen[token] {
			repeated = append(repeated, token)
			delete(seen, token)
		}
	}
	return repeated
}
