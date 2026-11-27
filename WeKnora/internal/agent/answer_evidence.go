package agent

import (
	"context"
	"encoding/json"
	"slices"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

func (e *AgentEngine) answerNumericEvidenceInScope(ctx context.Context, answer string, steps []types.AgentStep, groups ...[]chat.Message) []chat.Message {
	evidence := e.answerNumericEvidence(answer, steps, groups...)
	if !slices.ContainsFunc(steps, func(step types.AgentStep) bool {
		return slices.ContainsFunc(step.ToolCalls, func(call types.ToolCall) bool {
			return call.Result != nil && call.Result.Success && coverageHasLastValid(call.Result.Data)
		})
	}) {
		return evidence
	}
	record, _ := e.terminalCoverageRecord(ctx, &types.AgentState{RoundSteps: steps})
	return appendCoverageLastValidNumbers(evidence, record)
}

func appendCoverageLastValidNumbers(evidence []chat.Message, record coverageRecord) []chat.Message {
	observed := chat.NumericObservations{WorkbookRows: make(map[string]bool)}
	for _, item := range record.Evidence {
		if !coverageProtocolLastValid(item) || !coverageCurrentEvidence(record, item) {
			continue
		}
		if receipt, ok := coverageEndpointForGap(record, item); !ok || receipt.LastValidStatus != "observed" {
			continue
		}
		if value, ok := coverageFiniteNumber(item.Raw); ok {
			observed.Values = append(observed.Values, value)
			if item.Unit == "%" || item.Unit == "％" {
				observed.Percents = append(observed.Percents, value)
			}
			_, valueRow := coverageExcelCell(item.Cell)
			_, dateRow := coverageExcelCell(item.DateCell)
			observed.WorkbookRows[valueRow], observed.WorkbookRows[dateRow] = true, true
		}
	}
	if len(observed.Values) > 0 {
		evidence = append(evidence, chat.Message{Role: "tool", NumericObservations: &observed})
	}
	return evidence
}

// Verification uses durable executed results as well as the model transcript.
// A lossy display view must not erase provenance. Drafts are never promoted to
// evidence here; callers exclude the review envelope from transcript groups.
func answerNumericEvidence(answer string, steps []types.AgentStep, groups ...[]chat.Message) []chat.Message {
	return answerNumericEvidenceWithDocumentReads(answer, steps, steps, groups...)
}

// Reusing a successful read must still respect a later scope/page rebind. The
// registry check is independent of whether the answer happens to cite it.
func (e *AgentEngine) answerNumericEvidence(answer string, steps []types.AgentStep, groups ...[]chat.Message) []chat.Message {
	documentSteps := make([]types.AgentStep, 0, len(steps))
	for _, step := range steps {
		current := types.AgentStep{}
		for _, call := range step.ToolCalls {
			if e.toolRegistry != nil && call.ExecutionName() == "search_knowledge" && call.Result != nil && call.Result.Success {
				// Preserve each currently selected source in multi-document
				// search results without treating the whole result as one doc.
				result := *call.Result
				result.Data = make(map[string]any, len(call.Result.Data))
				for key, value := range call.Result.Data {
					result.Data[key] = value
				}
				for _, field := range []string{"chunks", "results"} {
					encoded, _ := json.Marshal(result.Data[field])
					var rows []map[string]any
					if json.Unmarshal(encoded, &rows) != nil {
						delete(result.Data, field)
						continue
					}
					kept := []map[string]any{}
					for _, row := range rows {
						if agenttools.IsCurrentDocumentSearchChunk(e.toolRegistry, call.Result, citationString(row["knowledge_id"])) {
							kept = append(kept, row)
						}
					}
					result.Data[field] = kept
				}
				call.Result = &result
				current.ToolCalls = append(current.ToolCalls, call)
				continue
			}
			if e.toolRegistry == nil || agenttools.IsCurrentDocumentRead(e.toolRegistry, call.Result) {
				current.ToolCalls = append(current.ToolCalls, call)
			}
		}
		documentSteps = append(documentSteps, current)
	}
	evidence := answerNumericEvidenceWithDocumentReads(answer, steps, documentSteps, groups...)
	if e.evidenceCompletionEnabled() {
		rows := map[string]bool{}
		for _, step := range steps {
			for _, call := range step.ToolCalls {
				analyzedSourceRows(call, rows)
			}
		}
		if len(rows) > 0 {
			evidence = append(evidence, chat.Message{Role: "tool", NumericObservations: &chat.NumericObservations{WorkbookRows: rows}})
		}
	}
	return evidence
}

func answerNumericEvidenceWithDocumentReads(answer string, steps, documentSteps []types.AgentStep, groups ...[]chat.Message) []chat.Message {
	var evidence []chat.Message
	for _, group := range groups {
		for _, message := range group {
			// A prior validation pass is not a current document read. Rebuild
			// the private observations from this invocation's qualified steps.
			message.NumericObservations = nil
			evidence = append(evidence, message)
		}
	}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result != nil && call.Result.Success {
				name := call.Name
				toolCallID := ""
				if coverageRequestedLastValid(call) || coverageHasLastValid(call.Result.Data) {
					toolCallID = call.ID
				}
				if executed := call.ExecutionName(); executed == "read_document" || executed == "search_knowledge" {
					// A provider alias must not turn document-embedded JSON into
					// a generic tool's executed numeric result.
					name = executed
					toolCallID = call.ID
				}
				evidence = append(evidence, chat.Message{Role: "tool", Name: name, ToolCallID: toolCallID, Content: call.Result.Output})
			}
		}
	}
	if values := scopedCitedDocumentNumbers(answer, documentSteps); len(values) > 0 {
		raw, _ := json.Marshal(map[string]any{"cited_document_values": values})
		evidence = append(evidence, chat.Message{Role: "tool", Content: string(raw)})
	}
	// Original prose values are observations even when markup is disabled or
	// omitted by the author. Only current native reads with complete source
	// identities qualify here; this never executes or infers a derived number.
	values, percentages := currentReadDocumentNumbers(documentSteps)
	if len(values) > 0 {
		evidence = append(evidence, chat.Message{Role: "tool", NumericObservations: &chat.NumericObservations{Values: values, Percents: percentages}})
	}
	return evidence
}
