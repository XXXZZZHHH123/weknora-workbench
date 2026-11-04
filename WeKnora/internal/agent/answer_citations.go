package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const sourceCitationPolicyBudget = 4 * 1024
const sourceCitationReasonLimit = 240

type sourceCitationCheck struct {
	Requirement string `json:"requirement"`
	Reason      string `json:"reason"`
}

// Preserve one unambiguous model decision; duplicate/unknown fields cannot
// override the runtime's citation setting or hide a required citation.
func (check *sourceCitationCheck) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("citation_check must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || (key != "requirement" && key != "reason") {
			return fmt.Errorf("duplicate/unknown citation_check field")
		}
		seen[key] = true
		destination := &check.Reason
		if key == "requirement" {
			destination = &check.Requirement
		}
		if err := decoder.Decode(destination); err != nil {
			return fmt.Errorf("citation_check fields must be strings")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("extra citation_check value")
	}
	if !seen["requirement"] || !seen["reason"] || strings.TrimSpace(check.Reason) == "" || utf8.RuneCountInString(check.Reason) > sourceCitationReasonLimit {
		return fmt.Errorf("citation_check requires a bounded reason and requirement")
	}
	switch check.Requirement {
	case "required", "not_required", "unavailable":
		return nil
	default:
		return fmt.Errorf("unknown citation_check requirement")
	}
}

// This is a model decision about the actual answer, never a rule that a
// retrieved candidate, matching title or shared page must be cited.
const sourceCitationReviewInstructions = `Source citation policy is runtime-owned and is supplied as source_citation_policy. When enabled=false, do not require citations or add source attribution. When enabled=true, judge from the complete task and actual evidence which material knowledge-source claims require an inline, clickable citation. A plain filename or table title is not a citation. A citation must identify the exact current supporting chunk; another read chunk, the same document/page, or a matching value cannot lend support. available_anchors lists only current successful source bodies with real citation handles; the directory is not proof that every anchor supports this answer. Request a minimal answer revision using the appropriate existing handle when needed, never cite every candidate. For a material PDF-source claim with no supporting chunk anchor, verified_document_reads provides navigation to actual source documents returned by authorized PDF tools: request collect_evidence for the minimum necessary read_document call using that real document identity, then judge the fetched chunk against the claim. Do not guess a table-to-chunk mapping or accept an arbitrary same-page chunk. Missing/omitted anchors are not evidence that a source is absent. If no genuine citation handle or verified read target is available, do not invent one. Pure chart/file delivery, conversation-only answers and output formats that prohibit inline markup do not require adding factual prose or breaking the requested format merely to cite a source.`

const sourceCitationDecisionInstructions = `Return citation_check={requirement,reason} in the SAME verdict, including a brief reason in the user's language. requirement=required means the candidate makes material knowledge-source claims that need inline citations backed by the actual supporting current chunk; if those citations are missing, return complete=false and next_action=revise_answer using existing evidence. Do not accept a filename/title as a citation. requirement=not_required applies when citations are disabled, or when the complete task genuinely does not call for inline knowledge citations (for example pure artifact delivery, conversation-only text or a format prohibiting markup); explain the actual task reason. requirement=unavailable means the factual answer is supported by actual evidence but no genuine supporting chunk citation can be supplied, such as structured XLSX results without chunk anchors. Other available anchors do not establish support for this claim, and their existence alone cannot make it required. A partial/omitted directory is not proof of unavailability: inspect necessary actual source pointers or request a necessary authorized read when possible. Do not use unavailable merely to excuse missing citations when the appropriate supporting handle is present. Citation classification is your semantic judgment of the complete task and evidence; runtime counts never choose the source or infer the claim. Existing citation binding/entailment checks still apply to every citation actually used.`

func (e *AgentEngine) sourceCitationReviewPolicy(state *types.AgentState, conversation []chat.Message) map[string]any {
	enabled := e != nil && e.config.CitationsEnabled()
	policy := map[string]any{
		"enabled": enabled, "available_anchors": []any{}, "verified_document_reads": []any{},
		"available_anchor_count": 0, "omitted_anchor_count": 0,
		"verified_document_read_count": 0, "omitted_document_read_count": 0,
		"projection_complete":      true,
		"conflicting_anchor_count": 0,
	}
	if !enabled || e.config == nil || e.modelContext == nil || state == nil {
		return policy
	}
	messages := e.sourceConsistencyMessages(state, conversation)
	records, conflicts := citationChunksFromMessages(messages)
	referenceRows, rowConflicts := currentCitationReferenceRows(state)
	conflicted := map[string]bool{}
	for id := range conflicts {
		conflicted[id] = true
	}
	for id := range rowConflicts {
		conflicted[id] = true
	}
	policy["conflicting_anchor_count"] = len(conflicted)
	anchors := []any{}
	for _, id := range rewriteCitationSourceIDsInMessageOrder(messages, records, conflicts) {
		record := records[id]
		handle := e.modelContext.CitableChunkHandle(id)
		if conflicts[id] || rowConflicts[id] || referenceRows[id] == nil || handle == "" || !record.contentKnown || strings.TrimSpace(record.content) == "" {
			continue
		}
		anchors = append(anchors, map[string]any{
			"citation_handle": handle, "chunk_id": id, "knowledge_id": record.knowledgeID,
			"tool_call_id": record.toolCallID, "evidence_id": record.evidenceID, "json_pointer": record.jsonPointer,
		})
	}
	reads := e.verifiedCitationDocumentReads(state)
	policy["available_anchor_count"], policy["verified_document_read_count"] = len(anchors), len(reads)
	// Counts include omissions. A bounded navigation directory never turns an
	// omitted body/anchor into evidence of absence or a guessed source link.
	for _, group := range []struct {
		field, omitted string
		items          []any
	}{{"available_anchors", "omitted_anchor_count", anchors}, {"verified_document_reads", "omitted_document_read_count", reads}} {
		kept := []any{}
		for index, item := range group.items {
			policy[group.field] = append(append([]any(nil), kept...), item)
			policy[group.omitted] = len(group.items) - index - 1
			encoded, err := json.Marshal(policy)
			if err != nil || len(encoded) > sourceCitationPolicyBudget-32 {
				break
			}
			kept = append(kept, item)
		}
		policy[group.field], policy[group.omitted] = kept, len(group.items)-len(kept)
	}
	policy["projection_complete"] = policy["omitted_anchor_count"] == 0 && policy["omitted_document_read_count"] == 0
	return policy
}

func (e *AgentEngine) verifiedCitationDocumentReads(state *types.AgentState) []any {
	reads := []any{}
	seen := map[string]bool{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			operation := call.ExecutionName()
			if operation == "structured_data" {
				operation, _ = call.ExecutionArgs()["operation"].(string)
			}
			if operation != "structured_read_pdf_table" && operation != "structured_query_pdf_table" && operation != "read_pdf_table" && operation != "query_pdf_table" {
				continue
			}
			data := call.Result.Data
			if data["status"] != "ok" || data["source_kind"] != "pdf_table" || (data["kind"] != "structured_analysis" && data["kind"] != "structured_source_inspection") {
				continue
			}
			source, _ := data["source"].(map[string]any)
			scope, _ := data["source_context"].(map[string]any)
			document, kb := citationString(source["knowledge_id"]), citationString(scope["knowledge_base_id"])
			if document == "" || kb == "" || seen[document] || (citationString(source["knowledge_base_id"]) != "" && citationString(source["knowledge_base_id"]) != kb) {
				continue
			}
			seen[document] = true
			// A verified document ID grants navigation only, never a chunk's
			// evidence/citation status. The authorized read still has to run.
			reads = append(reads, map[string]any{
				"document_handle": e.modelContext.RegisterDocument(document), "knowledge_id": document,
				"knowledge_base_id": kb, "tool_call_id": call.ID,
			})
		}
	}
	return reads
}

// Completion/persistence gets only the final model-selected canonical cites.
// Syntactic IDs never establish meaning: the existing source critic owns that
// judgement, while this intersection preserves current identity/version/ACL
// evidence. No filename/page/number can select a source here.
func (e *AgentEngine) finalAnswerKnowledgeReferences(state *types.AgentState) []*types.SearchResult {
	refs := []*types.SearchResult{}
	if e == nil || e.config == nil || !e.config.CitationsEnabled() || e.modelContext == nil || state == nil {
		return refs
	}
	approved := e.candidateApprovalCurrent(state.FinalAnswer, state)
	if !approved {
		_, approved = e.numericWarningDelivery(state.FinalAnswer, state)
	}
	if !approved {
		return refs
	}
	return e.currentCandidateKnowledgeReferences(state.FinalAnswer, state, e.turnReview.Conversation)
}

// Source identity/version qualification is also needed before acceptance.
// This does not approve the candidate or select any source the model omitted.
func (e *AgentEngine) currentCandidateKnowledgeReferences(candidate string, state *types.AgentState, conversation []chat.Message) []*types.SearchResult {
	refs := []*types.SearchResult{}
	if e == nil || state == nil || e.modelContext == nil {
		return refs
	}
	records, conflicts := citationChunksFromMessages(e.sourceConsistencyMessages(state, conversation))
	rows, rowConflicts := currentCitationReferenceRows(state)
	for _, id := range uniqueCandidateCitationIDs(candidate) {
		record, ok := records[id]
		ref := rows[id]
		if !ok || conflicts[id] || rowConflicts[id] || ref == nil || e.modelContext.CitableChunkHandle(id) == "" ||
			!record.contentKnown || strings.TrimSpace(record.content) == "" || record.knowledgeID != ref.KnowledgeID || record.content != ref.Content {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// The model decides whether this task needs a citation. The runtime only
// checks that its declaration can actually be delivered with current sources.
// Existing evidence/receipt policy and ordinary revision budgets stay intact.
func (e *AgentEngine) validateCitationCheck(check *sourceCitationCheck, candidate string, state *types.AgentState, conversation []chat.Message, accepted bool) (bool, error) {
	if check == nil {
		if accepted {
			return false, fmt.Errorf("accepted source review requires citation_check")
		}
		return false, nil // A rejection cannot bypass acceptance; preserve its existing recovery protocol.
	}
	if !e.config.CitationsEnabled() && check.Requirement != "not_required" {
		return false, fmt.Errorf("citation_check contradicts runtime citations-disabled policy")
	}
	ids := uniqueCandidateCitationIDs(candidate)
	refs := e.currentCandidateKnowledgeReferences(candidate, state, conversation)
	invalid := len(ids) != len(refs)
	missing := check.Requirement == "required" && len(refs) == 0
	if accepted && (invalid || missing || (!e.config.CitationsEnabled() && len(ids) != 0)) {
		e.answerReviewFeedback = "请按审核判定补齐本轮知识事实所需的可点击来源引用：只使用真实已读且支持相邻断言的 chunk 句柄，保留原答案范围，不用文件名、同页其他片段或未读身份代替。没有实际支持句柄时须如实限定引用不可用，不能编造。"
		e.answerReviewAction = reviewReviseAnswer
		return true, nil
	}
	return false, nil
}

func currentCitationReferenceRows(state *types.AgentState) (map[string]*types.SearchResult, map[string]bool) {
	rows, conflicts, revisions := map[string]*types.SearchResult{}, map[string]bool{}, map[string]string{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if (call.ExecutionName() != "read_document" && call.ExecutionName() != "search_knowledge") || call.Result == nil || !call.Result.Success {
				continue
			}
			data := call.Result.Data
			for _, field := range []string{"chunks", "results"} {
				encoded, err := json.Marshal(data[field])
				var batch []map[string]any
				if err != nil {
					continue
				}
				decoder := json.NewDecoder(bytes.NewReader(encoded))
				decoder.UseNumber()
				if decoder.Decode(&batch) != nil {
					continue
				}
				for _, row := range batch {
					selected := selectCitationFields(row, []string{"content", "knowledge_id", "knowledge_base_id", "knowledge_title", "knowledge_filename", "knowledge_source", "knowledge_channel", "chunk_index", "chunk_type", "start_at", "end_at", "parent_chunk_id", "image_info", "matched_images", "source_locators", "chunk_metadata"})
					encoded, _ := json.Marshal(selected)
					var ref types.SearchResult
					if json.Unmarshal(encoded, &ref) != nil {
						continue
					}
					ref.ID = citationString(row["chunk_id"])
					if ref.ID == "" || ref.KnowledgeID == "" || strings.TrimSpace(ref.Content) == "" {
						continue
					}
					ref.ContentRevision = citationInt(row["content_revision"])
					ref.ContentRewritten = citationInt(row["content_offset"]) > 0 || row["content_complete"] == false
					if citationString(data["knowledge_id"]) == ref.KnowledgeID {
						if ref.KnowledgeBaseID == "" {
							ref.KnowledgeBaseID = citationString(data["knowledge_base_id"])
						}
						if ref.KnowledgeTitle == "" {
							ref.KnowledgeTitle = citationString(data["knowledge_title"])
						}
					}
					if document, ok := data["document"].(map[string]any); ok && citationString(document["knowledge_id"]) == ref.KnowledgeID {
						if ref.KnowledgeTitle == "" {
							ref.KnowledgeTitle = citationString(document["title"])
						}
						if ref.KnowledgeFilename == "" {
							ref.KnowledgeFilename = citationString(document["file_name"])
						}
					}
					revision := reviewDigest([]any{ref.KnowledgeID, ref.KnowledgeBaseID, ref.Content, ref.SourceLocators, ref.ContentRevision, row["content_offset"]})
					if prior, exists := revisions[ref.ID]; exists && prior != revision {
						conflicts[ref.ID] = true
					}
					revisions[ref.ID], rows[ref.ID] = revision, &ref
				}
			}
		}
	}
	return rows, conflicts
}

func (e *AgentEngine) publishFinalKnowledgeReferences(ctx context.Context, state *types.AgentState, sessionID string) {
	// Completion is also the checkpoint/stop event. Only replace collected
	// durable state after this run actually committed its checked answer; an
	// interrupted, suspended or failed run must retain its resumable references.
	if state == nil || !state.IsComplete || state.Suspended != nil || !e.turnReview.Committed {
		return
	}
	approved := e.candidateApprovalCurrent(state.FinalAnswer, state)
	if !approved {
		_, approved = e.numericWarningDelivery(state.FinalAnswer, state)
	}
	if !approved {
		return
	}
	state.KnowledgeRefs = e.finalAnswerKnowledgeReferences(state)
	common.PipelineInfo(ctx, "Agent", "final_citation_delivery", map[string]interface{}{
		"enabled": e.config.CitationsEnabled(), "canonical_citation_count": len(uniqueCandidateCitationIDs(state.FinalAnswer)),
		"published_reference_count": len(state.KnowledgeRefs),
	})
	if len(state.KnowledgeRefs) == 0 || e.eventBus == nil {
		return
	}
	_ = e.eventBus.Emit(ctx, event.Event{
		ID: generateEventID("references"), Type: event.EventAgentReferences, SessionID: sessionID,
		Data: event.AgentReferencesData{References: state.KnowledgeRefs},
	})
}
