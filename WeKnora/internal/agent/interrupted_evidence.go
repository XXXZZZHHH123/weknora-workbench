package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// LegacyInterruptedAnswer recognizes the two old recovery messages in stored
// transcripts and terminal replay. It never treats model text as a receipt.
func LegacyInterruptedAnswer(answer string) (deadline, legacy bool) {
	answer = strings.TrimSpace(answer)
	deadline = answer == timedOutAnswerFallback || strings.HasSuffix(answer, "\n\n"+timedOutAnswerFallback)
	return deadline, deadline || answer == interruptedAnswerFallback || strings.HasSuffix(answer, "\n\n"+interruptedAnswerFallback)
}

// InterruptedSource identifies a current-turn source whose permission and
// version the recovery worker must confirm before publishing its observation.
type InterruptedSource struct {
	KnowledgeBaseID, KnowledgeID, ChunkID, FileHash, SHA256 string
	ParentChunkID, ContentSHA256                            string
	Structured                                              bool
}

type interruptedObservation struct {
	Source     InterruptedSource
	Fact       coverageEvidence
	Comparison string `json:",omitempty"`
	Key        string
}

// A report value becomes durable only after the model's selection was checked
// against the table intersection. Save its scalar and the body hash, not OCR.
func (e *AgentEngine) checkpointPinnedInterruptedDocuments(ctx context.Context) {
	b := e.collaboration
	if b == nil || b.Child != nil || b.ParentOwner == "" || b.Closing {
		return
	}
	var receipts []json.RawMessage
	for _, result := range e.evidenceCompletion.result {
		for _, fact := range result.PinnedFacts {
			item := fact.Evidence
			if item.SourceDomain != "knowledge" || item.Kind != "body" || item.ReportedValue == "" || item.Metric == "" ||
				item.Date == "" || item.ChunkID == "" || !validFigureDigest(item.ContentHash) ||
				!e.coverageEvidenceAllowed(ctx, nil, item) {
				continue
			}
			if receipt, ok := e.interruptedDocumentFact(item); ok {
				receipts = appendInterruptedObservation(receipts, receipt)
			}
		}
	}
	if len(receipts) == 0 {
		return
	}
	digest := reviewDigest(receipts)
	if digest == e.evidenceCompletion.interruptionReceiptDigest {
		return
	}
	if err := b.RecordParentEvidence(ctx, "", receipts); err != nil {
		if ctx.Err() == nil {
			logger.Warnf(ctx, "Could not save selected parent document facts: %v", err)
		}
		return
	}
	e.evidenceCompletion.interruptionReceiptDigest = digest
}

func (e *AgentEngine) interruptedDocumentFact(item coverageEvidence) (interruptedObservation, bool) {
	receipt := interruptedDocumentObservation(item)
	receipt.Fact.Raw = item.ReportedValue
	receipt.Source.ContentSHA256 = item.ContentHash
	if strings.HasPrefix(item.JSONPointer, "/data/"+types.DocumentImageTextEvidenceKey+"/") {
		call := e.evidenceCompletion.documentReads[item.ToolCallID]
		if call.Result == nil {
			return interruptedObservation{}, false
		}
		raw, _ := json.Marshal(call.Result.Data[types.DocumentImageTextEvidenceKey])
		var annotations []any
		if json.Unmarshal(raw, &annotations) != nil {
			return interruptedObservation{}, false
		}
		value, _ := reviewPointerValue(map[string]any{"data": map[string]any{types.DocumentImageTextEvidenceKey: annotations}}, strings.TrimSuffix(item.JSONPointer, "/text"))
		annotation := coverageObject(value)
		if sourceNavigationString(annotation, "parent_chunk_id") != item.ChunkID ||
			candidateTextDigest(sourceNavigationString(annotation, "text")) != item.ContentHash {
			return interruptedObservation{}, false
		}
		receipt.Source.ChunkID = sourceNavigationString(annotation, "child_chunk_id")
		receipt.Source.ParentChunkID = item.ChunkID
	}
	return receipt, receipt.Source.ChunkID != "" && validFigureDigest(receipt.Source.ContentSHA256)
}

// Receipt-aligned operands also survive when a valid model plan and actual
// reads exist but an optional explicit table association could not be parsed.
func (e *AgentEngine) checkpointInterruptedComparison(ctx context.Context, state *types.AgentState, candidate string) {
	b := e.collaboration
	if b == nil || b.Child != nil || b.Closing || b.ParentOwner == "" || state == nil || ctx.Err() != nil {
		return
	}
	var receipts []json.RawMessage
	for _, target := range e.evidenceCompletionTargets() {
		if !tableComparison(target) {
			continue
		}
		record := e.coverageRecordForTarget(ctx, target, e.evidenceCompletion.record)
		facts, aligned := e.tableReceiptReadings(target, record, candidate)
		if len(facts) == 0 {
			continue
		}
		pair := make([]interruptedObservation, 0, 2)
		for _, fact := range facts {
			item := fact.Evidence
			if !e.coverageEvidenceAllowed(ctx, &target, item) {
				continue
			}
			if item.SourceDomain == "knowledge" {
				if receipt, ok := e.interruptedDocumentFact(item); ok {
					pair = append(pair, receipt)
				}
				continue
			}
			for _, step := range state.RoundSteps[min(e.turnReview.TerminalStartStep, len(state.RoundSteps)):] {
				for _, call := range step.ToolCalls {
					if call.ID != item.ToolCallID || call.Result == nil || !call.Result.Success {
						continue
					}
					for _, receipt := range interruptedStructuredObservations(call, item.Operation, call.ExecutionArgs()) {
						if coverageEvidenceFactKey(receipt.Fact) == coverageEvidenceFactKey(item) {
							receipt.Fact = item
							pair = append(pair, receipt)
							break
						}
					}
				}
			}
		}
		comparison := ""
		if aligned && len(pair) == 2 {
			comparison = reviewDigest([]any{coverageEvidenceFactKey(pair[0].Fact), coverageEvidenceFactKey(pair[1].Fact)})
		}
		for _, receipt := range pair {
			receipt.Comparison = comparison
			receipts = appendInterruptedObservation(receipts, receipt)
		}
	}
	if len(receipts) > 0 {
		if err := b.RecordParentEvidence(ctx, "", receipts); err != nil && ctx.Err() == nil {
			logger.Warnf(ctx, "Could not save a receipt-aligned parent comparison: %v", err)
		}
	}
}

// Persist native scoped reads before their tool events are published. Do not
// persist full payloads, exploratory cell windows, or the candidate answer.
func (e *AgentEngine) checkpointInterruptedEvidence(ctx context.Context, calls []types.ToolCall, messageID string) {
	b := e.collaboration
	if b == nil || b.Child != nil || b.ParentOwner == "" || b.Closing || messageID == "" || e.toolRegistry == nil {
		return
	}
	var receipts []json.RawMessage
	for _, call := range calls {
		if call.Result == nil || !call.Result.Success || call.Result.PreflightRejected || call.ID == "" {
			continue
		}
		if call.ExecutionName() == agenttools.ToolReadDocument && call.Target == nil {
			if registered, err := e.toolRegistry.GetTool(call.Name); err != nil {
				continue
			} else if _, native := registered.(*agenttools.ReadDocumentTool); !native {
				continue
			}
			for _, item := range e.terminalDocumentReadReceipt(ctx, coverageAttempt{Operation: call.Name, Success: true, result: call.Result}) {
				item.ChunkID = sourceNavigationString(call.Result.Data, "source_chunk_id")
				item.ToolCallID = call.ID
				receipts = appendInterruptedObservation(receipts, interruptedDocumentObservation(item))
			}
			continue
		}
		argsJSON, err := json.Marshal(call.ExecutionArgs())
		if err != nil {
			continue
		}
		descriptor, args, native := e.toolRegistry.StructuredInvocation(call.ExecutionName(), argsJSON)
		if !native || descriptor.EffectClass != "read" {
			continue
		}
		for _, receipt := range interruptedStructuredObservations(call, descriptor.Operation, args) {
			if e.coverageEvidenceAllowed(ctx, nil, receipt.Fact) {
				receipts = appendInterruptedObservation(receipts, receipt)
			}
		}
	}
	if len(receipts) > 0 {
		if err := b.RecordParentEvidence(ctx, messageID, receipts); err != nil && ctx.Err() == nil {
			logger.Warnf(ctx, "Could not save current parent source receipts: %v", err)
		}
	}
}

func appendInterruptedObservation(receipts []json.RawMessage, receipt interruptedObservation) []json.RawMessage {
	if receipt.Source.KnowledgeBaseID == "" || receipt.Source.KnowledgeID == "" ||
		!receipt.Source.Structured && receipt.Source.ChunkID == "" {
		return receipts
	}
	receipt.Fact.Receipts = nil // Its rebound source and cell are the durable witness.
	if receipt.Fact.Kind == "" && !receipt.Source.Structured {
		receipt.Key = reviewDigest([]any{receipt.Source, receipt.Fact.Page})
	} else {
		receipt.Key = reviewDigest([]any{receipt.Source, coverageEvidenceFactKey(receipt.Fact), receipt.Comparison})
	}
	if raw, err := json.Marshal(receipt); err == nil {
		return append(receipts, raw)
	}
	return receipts
}

func interruptedDocumentObservation(item coverageEvidence) interruptedObservation {
	return interruptedObservation{
		Source: InterruptedSource{KnowledgeBaseID: item.KnowledgeBaseID, KnowledgeID: item.SourceID, ChunkID: item.ChunkID},
		Fact:   item,
	}
}

func interruptedStructuredObservations(call types.ToolCall, operation string, args map[string]any) []interruptedObservation {
	data := call.Result.Data
	if data["execution_started"] == false || (operation != "query_series" && operation != "analyze_table") {
		return nil
	}
	descriptor := coverageObject(data["_structured_execution"])
	if descriptor == nil { // Newly executed native descriptors may be typed.
		if raw, err := json.Marshal(data["_structured_execution"]); err == nil {
			descriptor, _ = decodeCitationJSON(string(raw))
		}
	}
	if descriptor["canonical_operation"] != operation || descriptor["effect_class"] != "read" ||
		descriptor["scope_mode"] != "knowledge_base" || descriptor["tool_name"] != call.ExecutionName() {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	data, err = decodeCitationJSON(string(raw))
	if err != nil {
		return nil
	}
	source := coverageObject(data["source"])
	identity := InterruptedSource{
		KnowledgeBaseID: sourceNavigationString(source, "knowledge_base_id"),
		KnowledgeID:     sourceNavigationString(source, "knowledge_id"),
		FileHash:        sourceNavigationString(source, "file_hash"),
		SHA256:          sourceNavigationString(source, "sha256"),
		Structured:      true,
	}
	if identity.KnowledgeID == "" || !validFigureDigest(identity.SHA256) {
		return nil
	}
	base := coverageEvidence{SourceDomain: "structured", Operation: operation, ToolCallID: call.ID,
		EvidenceID: reviewDigest([]any{call.ID, operation, data}), Status: sourceNavigationString(data, "status"), NextOffset: coverageOffset(data["next_offset"])}
	var receipts []interruptedObservation
	validCount, scalar := citationViewRevision(data["valid_count"])
	for _, item := range coverageStructuredEvidence(base, args, data, operation == "analyze_table") {
		if !item.Bound || item.KnowledgeID != identity.KnowledgeID || item.KnowledgeBaseID != identity.KnowledgeBaseID {
			continue
		}
		switch item.Kind {
		case "gap", "derived":
			if !item.Complete || !coverageExactRange(item) {
				continue
			}
		case "last_valid":
			if !coverageProtocolLastValid(item) || item.PeriodTo == "" {
				continue
			}
		case "cell":
			if item.Metric == "" || item.Date == "" || item.Cell == "" ||
				!item.LatestInScope && item.Extremum == "" && !coverageProtocolLastValid(item) && (validCount != 1 || !scalar || !item.RangeComplete) {
				continue
			}
		default:
			continue
		}
		receipts = append(receipts, interruptedObservation{Source: identity, Fact: item})
	}
	return receipts
}

// InterruptedEvidenceFromSteps supports assistant rows written before receipt
// checkpoints existed. Only native persisted execution metadata can enter.
func InterruptedEvidenceFromSteps(steps []types.AgentStep) []json.RawMessage {
	var receipts []json.RawMessage
	var boundDocuments map[string]bool
	for _, step := range steps {
		if len(step.UserMessagesBefore) > 0 {
			receipts = nil // A user steer replaced the preceding task's context.
			boundDocuments = nil
		}
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || call.Result.PreflightRejected || call.ID == "" {
				continue
			}
			if call.Target == nil && call.Name == agenttools.ToolBindDocumentScope && call.Result.Data["source_plan_validated"] == true {
				boundDocuments = make(map[string]bool)
				for _, document := range sourceNavigationRows(call.Result.Data["documents"]) {
					boundDocuments[sourceNavigationString(document, "knowledge_id")] = true
				}
				continue
			}
			if call.Name == agenttools.ToolReadDocument && call.Target == nil {
				data := call.Result.Data
				page, _ := citationViewRevision(data["page"])
				count, _ := citationViewRevision(data["fetched_chunks"])
				if count > 0 && data["offset_space"] == "physical_page_chunks" {
					receipts = appendInterruptedObservation(receipts, interruptedDocumentObservation(coverageEvidence{
						Operation: call.Name, SourceDomain: "knowledge", SourceID: sourceNavigationString(data, "knowledge_id"),
						SourceTitle: sourceNavigationString(data, "knowledge_title"), KnowledgeBaseID: sourceNavigationString(data, "knowledge_base_id"),
						ChunkID: sourceNavigationString(data, "source_chunk_id"), Page: page, ToolCallID: call.ID,
					}))
				}
				continue
			}
			if call.Target == nil || call.Target.ServiceName != "structured_data" || call.Name != call.Target.Name ||
				call.Name != "structured_"+call.Target.ToolName {
				continue
			}
			for _, receipt := range interruptedStructuredObservations(call, call.Target.ToolName, call.Target.Args) {
				receipts = appendInterruptedObservation(receipts, receipt)
			}
		}
	}
	if len(boundDocuments) > 0 {
		receipts = slices.DeleteFunc(receipts, func(raw json.RawMessage) bool {
			var observation interruptedObservation
			return json.Unmarshal(raw, &observation) != nil || !observation.Source.Structured && !boundDocuments[observation.Source.KnowledgeID]
		})
	}
	return receipts
}

// InterruptedProgress describes the saved selection and verified-step boundary.
type InterruptedProgress struct {
	SelectedLibraries, SelectedDocuments, SelectedTagScopes, SavedToolCalls int
	Known                                                                   bool
}

func InterruptedProgressFromMessage(message *types.Message) InterruptedProgress {
	if message == nil {
		return InterruptedProgress{}
	}
	progress := InterruptedProgress{
		SelectedLibraries: len(message.ExecutionContext.KnowledgeBaseIDs),
		SelectedDocuments: len(message.ExecutionContext.KnowledgeIDs),
		SelectedTagScopes: len(message.ExecutionContext.TagScopes),
		Known:             true,
	}
	for _, step := range message.AgentSteps {
		progress.SavedToolCalls += len(step.ToolCalls)
	}
	return progress
}

// RenderInterruptedEvidence shares the terminal rendering rules while keeping
// interruption status distinct from a successful or conclusive answer.
func RenderInterruptedEvidence(ctx context.Context, receipts []json.RawMessage, current func(InterruptedSource) bool, deadline bool, progress InterruptedProgress) string {
	d := terminalCoverageDelivery{chinese: strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")}
	end := d.text("本轮执行中断，查证未完成。", "This run was interrupted before verification completed.")
	if deadline {
		end = d.text("本轮超过执行时限，查证未完成。", "This run reached its deadline before verification completed.")
	}
	seen := make(map[string]bool)
	seenCalls := make(map[string]bool)
	comparisons := make(map[string][]coverageFact)
	for _, raw := range receipts {
		var receipt interruptedObservation
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber() // Keep the original verified numeric lexeme intact.
		if decoder.Decode(&receipt) != nil {
			continue
		}
		// Count persisted checkpoint calls even when the process stopped before
		// the full agent-step timeline could be written.
		if receipt.Fact.ToolCallID != "" {
			seenCalls[receipt.Fact.ToolCallID] = true
		}
		if current == nil || !current(receipt.Source) {
			continue
		}
		item := receipt.Fact
		if !receipt.Source.Structured && item.ReportedValue == "" {
			d.readReceipts = append(d.readReceipts, item)
			continue
		}
		key := coverageEvidenceFactKey(item)
		if receipt.Comparison != "" && !slices.ContainsFunc(comparisons[receipt.Comparison], func(fact coverageFact) bool {
			return coverageEvidenceFactKey(fact.Evidence) == key
		}) {
			comparisons[receipt.Comparison] = append(comparisons[receipt.Comparison], coverageFact{Role: "scoped_value", Evidence: item})
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		role := "scoped_value"
		if item.Kind == "gap" {
			role = "coverage_boundary"
		} else if item.Kind == "last_valid" {
			role = "last_valid_value"
		}
		d.facts = append(d.facts, terminalCoverageFact{coverageFact{role, item}, true})
		d.record.Evidence = append(d.record.Evidence, item)
		if d.record.Snapshots == nil {
			d.record.Snapshots = make(map[string]string)
		}
		coverageRememberSnapshot(d.record.Snapshots, item)
	}
	progress.SavedToolCalls = max(progress.SavedToolCalls, len(seenCalls))
	if len(d.facts) > 0 {
		comparisonIDs := make([]string, 0, len(comparisons))
		for id := range comparisons {
			comparisonIDs = append(comparisonIDs, id)
		}
		slices.Sort(comparisonIDs)
		for _, id := range comparisonIDs {
			d.addComparison(comparisons[id], true)
		}
	}
	var notes []string
	for _, receipt := range d.readReceipts {
		if slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
			item := fact.Evidence
			return item.ReportedValue != "" && comparisonPhysicalSource(item) == comparisonPhysicalSource(receipt)
		}) {
			continue
		}
		title := terminalEvidenceText(receipt.SourceTitle)
		if title == "" {
			continue
		}
		if receipt.Page > 0 {
			notes = append(notes, fmt.Sprintf(d.text("已读取 %[1]s 第 %[2]d 页。", "Read page %[2]d of %[1]s."), title, receipt.Page))
		} else {
			notes = append(notes, fmt.Sprintf(d.text("已读取 %s 返回的片段。", "Read passages returned from %s."), title))
		}
	}
	slices.Sort(notes)
	notes = slices.Compact(notes)
	if len(d.facts) != 0 {
		return strings.Join(append(append([]string{d.renderFallback(terminalEmpty)}, notes...), end), "\n\n")
	}
	if len(notes) == 0 {
		if !progress.Known {
			return end + " " + d.text("旧的终止事件未记录查证范围或来源观测。", "The stored terminal event lacks the verification scope and source observations.")
		}
		scope := fmt.Sprintf(d.text("本轮选择了 %d 个知识库、%d 份文档和 %d 组标签范围。", "This turn selected %d knowledge bases, %d documents, and %d tag scopes."), progress.SelectedLibraries, progress.SelectedDocuments, progress.SelectedTagScopes)
		status := fmt.Sprintf(d.text("执行记录已保存 %d 次工具调用和 %d 条来源观测，其中可重新核验的观测为 0 条。", "The execution record contains %d tool calls and %d source observations. Zero observations could be reverified."), progress.SavedToolCalls, len(receipts))
		return strings.Join([]string{end, scope, status}, " ")
	}
	return strings.Join(append(notes, end), "\n\n")
}
