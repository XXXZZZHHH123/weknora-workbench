package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const structuredQueryRowReviewBudget = 12 * 1024

type structuredQueryRowOmission struct {
	JSONPointer       string `json:"json_pointer"`
	StartIndex        int    `json:"start_index"`
	EndIndexExclusive int    `json:"end_index_exclusive"`
	NextRowOffset     int    `json:"next_row_offset"`
}

type structuredQueryRowCompanionEntry struct {
	EvidenceID            string                       `json:"evidence_id"`
	Tool                  string                       `json:"tool"`
	ToolCallID            string                       `json:"tool_call_id,omitempty"`
	DuplicateObservations int                          `json:"duplicate_observation_count,omitempty"`
	SourceJSONPointer     string                       `json:"source_json_pointer"`
	Metadata              map[string]any               `json:"metadata"`
	RowEncoding           string                       `json:"row_encoding"`
	ConstantFields        map[string]any               `json:"constant_fields,omitempty"`
	Columns               []string                     `json:"columns"`
	Rows                  any                          `json:"rows"`
	SourceRowCount        int                          `json:"source_row_count"`
	RetainedRowCount      int                          `json:"retained_row_count"`
	ProjectionComplete    bool                         `json:"projection_complete"`
	SourceTruncationKnown bool                         `json:"source_truncation_known"`
	SourceResultTruncated any                          `json:"source_result_truncated,omitempty"`
	OmittedRowRanges      []structuredQueryRowOmission `json:"omitted_row_ranges,omitempty"`
}

type structuredQueryRowCompanion struct {
	BudgetBytes         int                                `json:"budget_bytes"`
	ProjectionComplete  bool                               `json:"projection_complete"`
	SourceResultCount   int                                `json:"source_result_count"`
	IncludedResultCount int                                `json:"included_result_count"`
	OmittedResultCount  int                                `json:"omitted_result_count"`
	Entries             []structuredQueryRowCompanionEntry `json:"entries"`
}

type structuredQueryRowCandidate struct {
	entry     structuredQueryRowCompanionEntry
	rows      []any
	rowValues []any
}

// structuredQueryRowReviewEvidence adds a lossless, mechanically encoded row
// view for successful structured query executions. It reads the same immutable
// source-tool message used by read_review_evidence, so every handle resolves to
// the exact original JSON result. No question, candidate answer, or row value
// is used to select or reorder records.
func structuredQueryRowReviewEvidence(state *types.AgentState, sourceMessages []chat.Message, budget int) map[string]any {
	if state == nil || budget <= 0 {
		return nil
	}
	messageByCallID := make(map[string]chat.Message)
	for _, message := range sourceMessages {
		if message.Role == "tool" && message.ToolCallID != "" {
			// Keep the result payload, not the separate runtime-access entry.
			if structuredQueryMessageData(message.Content) != nil {
				messageByCallID[message.ToolCallID] = message
			}
		}
	}

	candidates := make([]structuredQueryRowCandidate, 0)
	seen := map[string]int{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || !isStructuredQueryExecution(call) {
				continue
			}
			message, ok := messageByCallID[call.ID]
			if !ok || message.Name != call.Name {
				continue
			}
			// Collapse only byte-identical executions of the same tool result.
			// Distinct filters, sources, or results remain separate. Keep the
			// first actual immutable handle for the exact duplicate observation.
			duplicateKey := message.Name + "\x00" + message.Content
			executionArgs, argsErr := json.Marshal(call.ExecutionArgs())
			if argsErr == nil {
				duplicateKey += "\x00" + call.ExecutionName() + "\x00" + string(executionArgs)
			} else {
				// Unserializable execution metadata cannot establish that two
				// observations had identical scope. Keep their identities apart.
				duplicateKey += "\x00call:" + call.ID
			}
			if existing, ok := seen[duplicateKey]; ok {
				candidates[existing].entry.DuplicateObservations++
				continue
			}
			payload := structuredQueryMessageData(message.Content)
			if payload == nil {
				continue
			}
			data, ok := payload["data"].(map[string]any)
			if !ok {
				continue
			}
			actualData, err := json.Marshal(call.Result.Data)
			messageData, messageDataErr := json.Marshal(data)
			if err != nil || messageDataErr != nil || !bytes.Equal(actualData, messageData) {
				continue
			}
			if status, ok := data["status"].(string); !ok || status != "ok" {
				continue
			}
			rows, ok := data["rows"].([]any)
			if !ok {
				continue
			}
			candidate, ok := newStructuredQueryRowCandidate(message, data, rows)
			if ok {
				seen[duplicateKey] = len(candidates)
				candidates = append(candidates, candidate)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	view := structuredQueryRowCompanion{BudgetBytes: budget, ProjectionComplete: true, SourceResultCount: len(candidates), IncludedResultCount: len(candidates), Entries: make([]structuredQueryRowCompanionEntry, len(candidates))}
	for i := range candidates {
		entry := candidates[i].entry
		entry.Rows = []any{}
		entry.RetainedRowCount = 0
		entry.ProjectionComplete = len(candidates[i].rows) == 0
		entry.OmittedRowRanges = rowOmissionRanges(entry.SourceJSONPointer, 0, entry.SourceRowCount)
		candidates[i].entry = entry
		view.Entries[i] = entry
	}
	view.ProjectionComplete = allStructuredRowEntriesComplete(view.Entries)
	if !structuredRowViewFits(view, budget) {
		// Preserve a bounded explicit notice when non-row metadata alone is too
		// large. The immutable full results and their review handles remain in
		// source_tool_results and can still be read through the existing reader.
		notice := map[string]any{
			"budget_bytes": budget, "projection_complete": false,
			"source_result_count": len(candidates), "included_result_count": 0,
			"omitted_result_count": len(candidates),
			"omitted_results":      structuredQuerySourceOmissions(candidates),
			"notice":               "Row companion metadata exceeded its independent budget; consult the exact source_tool_results evidence handles.",
		}
		return boundedStructuredQueryNotice(notice, budget)
	}

	// Grow only contiguous prefixes, round-robin by source result. This keeps
	// multiple query results distinct and avoids favoring one large result.
	blocked := make([]bool, len(candidates))
	for {
		progress := false
		for i := range candidates {
			candidate := &candidates[i]
			if blocked[i] || candidate.entry.RetainedRowCount >= candidate.entry.SourceRowCount {
				continue
			}
			idx := candidate.entry.RetainedRowCount
			oldRows := candidate.entry.Rows
			oldRetained := candidate.entry.RetainedRowCount
			oldComplete := candidate.entry.ProjectionComplete
			oldRanges := candidate.entry.OmittedRowRanges
			values := appendStructuredRow(candidate, idx)
			candidate.entry.Rows = values
			candidate.entry.RetainedRowCount++
			candidate.entry.ProjectionComplete = candidate.entry.RetainedRowCount == candidate.entry.SourceRowCount
			candidate.entry.OmittedRowRanges = rowOmissionRanges(candidate.entry.SourceJSONPointer, candidate.entry.RetainedRowCount, candidate.entry.SourceRowCount)
			view.Entries[i] = candidate.entry
			view.ProjectionComplete = allStructuredRowEntriesComplete(view.Entries)
			if structuredRowViewFits(view, budget) {
				progress = true
				continue
			}
			candidate.entry.Rows = oldRows
			candidate.entry.RetainedRowCount = oldRetained
			candidate.entry.ProjectionComplete = oldComplete
			candidate.entry.OmittedRowRanges = oldRanges
			view.Entries[i] = candidate.entry
			blocked[i] = true
		}
		if !progress {
			break
		}
	}
	view.IncludedResultCount = len(view.Entries)
	view.OmittedResultCount = view.SourceResultCount - view.IncludedResultCount
	view.ProjectionComplete = allStructuredRowEntriesComplete(view.Entries)
	if !structuredRowViewFits(view, budget) {
		notice := map[string]any{
			"budget_bytes": budget, "projection_complete": false,
			"source_result_count": len(candidates), "included_result_count": 0,
			"omitted_result_count": len(candidates),
			"omitted_results":      structuredQuerySourceOmissions(candidates),
			"notice":               "Row companion exceeded its independent budget; consult the exact source_tool_results evidence handles.",
		}
		return boundedStructuredQueryNotice(notice, budget)
	}
	result := map[string]any{
		"budget_bytes": view.BudgetBytes, "projection_complete": view.ProjectionComplete,
		"source_result_count": view.SourceResultCount, "included_result_count": view.IncludedResultCount,
		"omitted_result_count": view.OmittedResultCount, "entries": view.Entries,
	}
	return boundedStructuredQueryNotice(result, budget)
}

func structuredQuerySourceOmissions(candidates []structuredQueryRowCandidate) []map[string]string {
	omissions := make([]map[string]string, 0, len(candidates))
	for _, candidate := range candidates {
		omissions = append(omissions, map[string]string{"evidence_id": candidate.entry.EvidenceID, "json_pointer": candidate.entry.SourceJSONPointer})
	}
	return omissions
}

func boundedStructuredQueryNotice(value map[string]any, budget int) map[string]any {
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= budget {
		return value
	}
	minimal := map[string]any{"projection_complete": false, "notice": "Structured row companion exceeded its independent byte budget; consult source_tool_results handles."}
	encoded, err = json.Marshal(minimal)
	if err == nil && len(encoded) <= budget {
		return minimal
	}
	return nil
}

func structuredRowViewFits(view structuredQueryRowCompanion, budget int) bool {
	encoded, err := json.Marshal(view)
	return err == nil && len(encoded) <= budget
}

func allStructuredRowEntriesComplete(entries []structuredQueryRowCompanionEntry) bool {
	for _, entry := range entries {
		if !entry.ProjectionComplete {
			return false
		}
	}
	return true
}

func isStructuredQueryExecution(call types.ToolCall) bool {
	switch call.ExecutionName() {
	case "structured_query_series":
		return true
	case "structured_data":
		operation, _ := call.ExecutionArgs()["operation"].(string)
		return operation == "query_series"
	default:
		return false
	}
}

func structuredQueryMessageData(content string) map[string]any {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF || payload == nil {
		return nil
	}
	if success, ok := payload["success"].(bool); !ok || !success {
		return nil
	}
	if _, ok := payload["data"].(map[string]any); !ok {
		return nil
	}
	return payload
}

func newStructuredQueryRowCandidate(message chat.Message, data map[string]any, rows []any) (structuredQueryRowCandidate, bool) {
	if message.Role != "tool" || message.Name == "" || message.ToolCallID == "" {
		return structuredQueryRowCandidate{}, false
	}
	metadata := make(map[string]any, len(data)-1)
	for key, value := range data {
		if key != "rows" {
			metadata[key] = value
		}
	}
	truncated, truncationKnown := data["truncated"]
	columns, constantFields, homogeneous := structuredRowShape(rows)
	encoding := "objects"
	values := append([]any(nil), rows...)
	if homogeneous {
		encoding = "positional"
		values = make([]any, len(rows))
		for i, row := range rows {
			object := row.(map[string]any)
			positional := make([]any, len(columns))
			for j, column := range columns {
				positional[j] = object[column]
			}
			values[i] = positional
		}
	}
	return structuredQueryRowCandidate{
		entry: structuredQueryRowCompanionEntry{
			EvidenceID: reviewEvidenceID(message), Tool: message.Name, ToolCallID: message.ToolCallID,
			SourceJSONPointer: "/data/rows", Metadata: metadata, RowEncoding: encoding,
			ConstantFields: constantFields, Columns: columns, Rows: []any{}, SourceRowCount: len(rows),
			SourceTruncationKnown: truncationKnown, SourceResultTruncated: truncated,
		},
		rows: rows, rowValues: values,
	}, true
}

func structuredRowShape(rows []any) ([]string, map[string]any, bool) {
	if len(rows) == 0 {
		return []string{}, map[string]any{}, true
	}
	first, ok := rows[0].(map[string]any)
	if !ok {
		return []string{}, nil, false
	}
	allColumns := make([]string, 0, len(first))
	for key := range first {
		allColumns = append(allColumns, key)
	}
	sort.Strings(allColumns)
	for _, raw := range rows[1:] {
		object, ok := raw.(map[string]any)
		if !ok || len(object) != len(allColumns) {
			return []string{}, nil, false
		}
		for _, column := range allColumns {
			if _, exists := object[column]; !exists {
				return []string{}, nil, false
			}
		}
	}
	constantFields := make(map[string]any)
	columns := make([]string, 0, len(allColumns))
	for _, column := range allColumns {
		constant := true
		for _, raw := range rows[1:] {
			object := raw.(map[string]any)
			if !sameStructuredJSONValue(first[column], object[column]) {
				constant = false
				break
			}
		}
		if constant {
			constantFields[column] = first[column]
		} else {
			columns = append(columns, column)
		}
	}
	return columns, constantFields, true
}

func sameStructuredJSONValue(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func appendStructuredRow(candidate *structuredQueryRowCandidate, index int) any {
	rows, _ := candidate.entry.Rows.([]any)
	rows = append(rows, candidate.rowValues[index])
	return rows
}

func rowOmissionRanges(pointer string, start, end int) []structuredQueryRowOmission {
	if start >= end {
		return nil
	}
	return []structuredQueryRowOmission{{JSONPointer: pointer, StartIndex: start, EndIndexExclusive: end, NextRowOffset: start}}
}
