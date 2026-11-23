package agent

import (
	"encoding/json"
	"fmt"
	"sort"
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
	available, omitted := e.boundedCitationRecoveryAnchors(state, conversation)
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
			if len(views[id]) > 0 && !citationRecoveryIdentityKnown(views[id]) {
				reason = "source_identity_unknown"
			}
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
		issue := citationRecoveryIssue("kb", id, reason, available)
		issue["identity_unknown_view_count"] = citationRecoveryUnknownViewCount(views[id])
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
		issues = append(issues, issue)
	}
	// A real chunk ID cannot excuse a different library/document asserted in
	// the tag, or a duplicate identity attribute. Diagnose each actual offending
	// label separately so the generator can repair its exact protocol fields.
	validChunks := map[string]bool{}
	for _, ref := range e.currentCandidateKnowledgeReferences(candidate, state, conversation) {
		validChunks[ref.ID] = true
	}
	for _, location := range documentAnswerCitation.FindAllStringSubmatchIndex(candidate, -1) {
		tag := candidate[location[0]:location[1]]
		id := candidate[location[2]:location[3]]
		if !validChunks[id] || citationWarningTagScopeSafe(tag, views[id]) {
			continue
		}
		issue := citationRecoveryIssue("kb", id, "citation_scope_invalid", available)
		issue["citation_label"] = tag
		issue["candidate_start_byte"] = location[0]
		issue["explanation"] = "引用标签包含与本轮真实来源不一致、重复或不支持的身份属性。使用实际已读来源的现有句柄重新生成该标签；标签不能扩大知识库或文档权限。"
		issues = append(issues, issue)
		counts["citation_scope_invalid"]++
	}
	validEvidence := map[string]bool{}
	for _, ref := range e.currentCandidateEvidenceReferences(candidate, state) {
		validEvidence[ref.ID] = true
	}
	for _, id := range uniqueCandidateEvidenceIDs(candidate) {
		if !validEvidence[id] {
			counts["structured_reference_invalid"]++
			issues = append(issues, citationRecoveryIssue("evidence", id, "structured_reference_invalid", available))
		}
	}
	payload, _ := json.Marshal(map[string]any{"kind": "citation_validation_diagnostic", "candidate_sha256": candidateTextDigest(candidate), "issues": issues, "issue_counts": counts, "available_chunks": available, "omitted_available_chunk_count": omitted, "notice": "Runtime identity diagnostics, not source facts. For partial_views_need_full_chunk read the issued chunk handle WITHOUT page/pages/query to obtain its full current body; follow actual content pagination if needed. Existing version, hash, scope and citation checks remain mandatory. Do not repeat the same physical-page reads or invent a replacement citation. available_chunks are identity-qualified actual current reads, not a semantic recommendation: choose a chunk only after checking that its body supports the adjacent claim and entire stated scope. Each issue lists available_chunk_ids; omitted navigation entries are unknown, not evidence of absence. An empty set requires genuine authorized evidence. Never borrow support from another period or source. Other missing or conflicting sources require genuine authorized evidence; do not delete requested results."})
	return "答复引用尚未通过本轮来源身份校验。按下列具体原因修复；反馈不是新事实。" + string(payload), counts, needsRead
}

// This directory is an identity-qualified navigation set, not a decision about
// which business source supports a statement. Keep ordering stable and never
// add a historical, failed, unknown or conflicted observation.
func (e *AgentEngine) citationRecoveryAnchors(state *types.AgentState, conversation []chat.Message) []map[string]any {
	anchors := []map[string]any{}
	if e == nil || e.modelContext == nil || state == nil {
		return anchors
	}
	records, conflicts := citationChunksFromMessages(e.sourceConsistencyMessages(state, conversation))
	rows, rowConflicts := currentCitationReferenceRows(state)
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record, ref := records[id], rows[id]
		handle := e.modelContext.CitableChunkHandle(id)
		_, identityKnown := citationViewIdentity(record.sourceView)
		if !identityKnown || conflicts[id] || rowConflicts[id] || ref == nil || handle == "" || !record.contentKnown || strings.TrimSpace(record.content) == "" ||
			record.knowledgeID != ref.KnowledgeID || record.content != ref.Content {
			continue
		}
		anchors = append(anchors, map[string]any{"chunk_id": id, "citation_handle": handle, "knowledge_id": record.knowledgeID,
			"tool_call_id": record.toolCallID, "evidence_id": record.evidenceID, "json_pointer": record.jsonPointer,
			"content_complete": record.sourceView.row["content_complete"], "content_source_view": record.sourceView.row["content_source_view"]})
	}
	return anchors
}

func (e *AgentEngine) boundedCitationRecoveryAnchors(state *types.AgentState, conversation []chat.Message) ([]map[string]any, int) {
	anchors := e.citationRecoveryAnchors(state, conversation)
	const maxAnchors = 32
	if len(anchors) > maxAnchors {
		return anchors[:maxAnchors], len(anchors) - maxAnchors
	}
	return anchors, 0
}

func citationRecoveryIssue(kind, id, reason string, available []map[string]any) map[string]any {
	ids := make([]string, 0, len(available))
	for _, anchor := range available {
		ids = append(ids, citationString(anchor["chunk_id"]))
	}
	attribute := "chunk_id"
	if kind == "evidence" {
		attribute = "evidence_id"
	}
	return map[string]any{"citation_id": id, "citation_label": fmt.Sprintf(`<%s %s=%q />`, kind, attribute, id),
		"reason_code": reason, "available_chunk_ids": ids}
}

func citationRecoveryIdentityKnown(views []citationSourceView) bool {
	return len(views) > citationRecoveryUnknownViewCount(views)
}

func citationRecoveryUnknownViewCount(views []citationSourceView) int {
	count := 0
	for _, view := range views {
		if _, known := citationViewIdentity(view); !known {
			count++
		}
	}
	return count
}

type citationSemanticIssue struct {
	CitationID  string `json:"citation_id"`
	ReasonCode  string `json:"reason_code"`
	Explanation string `json:"explanation"`
}

// Semantic categories are supplied by the reviewing model from the complete
// claim and actual evidence. The runtime validates protocol and real IDs only;
// it must never infer scope or business intent from words in the question.
func (e *AgentEngine) citationSemanticRecovery(candidate string, state *types.AgentState, conversation []chat.Message, reported []citationSemanticIssue) (string, error) {
	if len(reported) == 0 {
		return "", nil
	}
	if len(reported) > 16 {
		return "", fmt.Errorf("citation_issues exceeds protocol limit")
	}
	selected := map[string]bool{}
	for _, id := range uniqueCandidateCitationIDs(candidate) {
		selected[id] = true
	}
	for _, id := range uniqueCandidateEvidenceIDs(candidate) {
		selected[id] = true
	}
	available, omitted := e.boundedCitationRecoveryAnchors(state, conversation)
	issues := []map[string]any{}
	for _, reported := range reported {
		if !selected[reported.CitationID] || len([]rune(reported.Explanation)) > 240 || strings.TrimSpace(reported.Explanation) == "" {
			return "", fmt.Errorf("citation_issues requires a current candidate citation and bounded explanation")
		}
		switch reported.ReasonCode {
		case "citation_scope_unsupported", "citation_claim_unsupported", "citation_claim_contradicted":
		default:
			return "", fmt.Errorf("unknown semantic citation issue category")
		}
		kind := "kb"
		for _, id := range uniqueCandidateEvidenceIDs(candidate) {
			if id == reported.CitationID {
				kind = "evidence"
			}
		}
		issue := citationRecoveryIssue(kind, reported.CitationID, reported.ReasonCode, available)
		issue["explanation"] = reported.Explanation
		issues = append(issues, issue)
	}
	payload, _ := json.Marshal(map[string]any{"kind": "citation_semantic_diagnostic", "candidate_sha256": candidateTextDigest(candidate),
		"issues": issues, "available_chunks": available, "omitted_available_chunk_count": omitted, "notice": "Issue categories and explanations are reviewer judgments, not new source facts. available_chunk_ids are actual current identity-qualified reads, not chosen business sources. Inspect each exact body and choose only evidence supporting the complete adjacent claim; do not borrow a different period or source. Omitted navigation entries are unknown, not evidence of absence. Keep requested supported results and state genuine evidence gaps."})
	return string(payload), nil
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
				for _, row := range citationResultRows(batch) {
					if id := citationString(row["chunk_id"]); id != "" {
						views[id] = append(views[id], citationSourceView{row: row, data: call.Result.Data, result: call.Result, tool: call.ExecutionName()})
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
