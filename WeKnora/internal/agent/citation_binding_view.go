package agent

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

const citationBindingViewBudget = 8 * 1024
const citationBindingBeforeRunes = 160
const citationBindingAfterRunes = 80
const citationBindingOmissionDetailLimit = 64
const citationBindingMaxKeyIDBytes = 256

type citationBindingOccurrence struct {
	index, startRune, endRune, startByte, endByte, key int
}

// Compact protocol v2 stores a key once per distinct citation and an
// occurrence row for each included use. Keys that do not fit remain omitted,
// with aggregate counts and bounded positional details; they are never
// silently treated as absent or supported.
func citationBindingView(candidate string, sourceMessages []chat.Message, citationEvidence map[string]any, readerAvailable bool, requestedBudget int) map[string]any {
	return citationBindingViewWithContext(candidate, sourceMessages, citationEvidence, readerAvailable, requestedBudget, true)
}

// citationBindingViewPositionsOnly keeps exact occurrence/key identity while
// relying on candidate_answer for surrounding prose already sent to review.
// This avoids serializing a second copy of that prose for every citation.
func citationBindingViewPositionsOnly(candidate string, sourceMessages []chat.Message, citationEvidence map[string]any, readerAvailable bool, requestedBudget int) map[string]any {
	return citationBindingViewWithContext(candidate, sourceMessages, citationEvidence, readerAvailable, requestedBudget, false)
}

func citationBindingViewWithContext(candidate string, sourceMessages []chat.Message, citationEvidence map[string]any, readerAvailable bool, requestedBudget int, includeContext bool) map[string]any {
	if requestedBudget <= 0 {
		return nil
	}
	budget := min(citationBindingViewBudget, requestedBudget)
	matches := documentAnswerCitation.FindAllStringSubmatchIndex(candidate, -1)
	records, conflicts := citationChunksFromMessages(sourceMessages)
	itemIndexes := citationBindingItemIndexes(citationEvidence)
	ids := uniqueCandidateCitationIDs(candidate)

	resolved, ambiguous, unknown := 0, 0, 0
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		id := candidate[match[2]:match[3]]
		switch {
		case conflicts[id]:
			ambiguous++
		case records[id].chunkID != "":
			resolved++
		default:
			unknown++
		}
	}
	view := map[string]any{
		"schema_version": "citation_binding_view.v2", "budget_bytes": budget,
		"protocol":            "keys[n] is a unique key row. [resolved, source_item_index] points into citation_source_evidence.items. [resolved_source_omitted, chunk_id, knowledge_id, tool, tool_call_id, evidence_id, json_pointer] retains a real immutable reader handle when its item did not fit. Other unresolved rows preserve only their status and bounded ID. occurrences[n] is [key_index, candidate_start_rune, candidate_end_rune, context_before_start_rune, context_before, context_before_clipped, context_after_end_rune, context_after, context_after_clipped, context_included].",
		"projection_complete": true, "occurrence_count": len(matches), "included_occurrence_count": 0,
		"omitted_occurrence_count": 0, "unique_citation_count": len(ids),
		"resolved_occurrence_count": resolved, "ambiguous_occurrence_count": ambiguous, "unknown_occurrence_count": unknown,
		// Reserve the maximum serialized width before admission decisions. The
		// exact count is written after context selection; it can only shrink, so
		// finalization cannot unexpectedly push a boundary-sized view over budget.
		"context_truncated_count": len(matches), "context_truncated_scope": "retained_occurrences_only",
		"reader_available": readerAvailable, "keys": []any{}, "occurrences": []any{},
		"omitted_occurrences": []any{}, "omitted_occurrence_details": 0,
		"notice": "Each retained occurrence references one exact key. A key is identity resolution, not semantic support. Source pointers are obtained only from the linked source item or an explicit resolved_source_omitted key. Omitted IDs or rows remain unknown; local answer text is bounded navigation context, not source evidence.",
	}
	if citationBindingViewSize(view) > budget {
		return nil
	}

	keys := []any{}
	rows := []any{}
	omittedRows := []any{}
	keyIndexes := map[string]int{}
	retained := make([]citationBindingOccurrence, 0, len(matches))
	lastByte, lastRune := 0, 0
	for index, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		startRune := lastRune + utf8.RuneCountInString(candidate[lastByte:match[0]])
		endRune := startRune + utf8.RuneCountInString(candidate[match[0]:match[1]])
		lastByte, lastRune = match[1], endRune
		id := candidate[match[2]:match[3]]

		keyIndex, existing := keyIndexes[id]
		addedKey := false
		if !existing {
			if len(id) > citationBindingMaxKeyIDBytes {
				view["included_occurrence_count"] = len(rows)
				view["omitted_occurrence_count"] = len(matches) - len(rows)
				appendBindingOmission(view, &omittedRows, index, -1, startRune, budget)
				continue
			}
			key := citationBindingKey(id, records, conflicts, itemIndexes)
			trialKeys := append(append([]any(nil), keys...), key)
			view["keys"] = trialKeys
			if citationBindingViewSize(view) > budget {
				view["keys"] = keys
				appendBindingOmission(view, &omittedRows, index, -1, startRune, budget)
				continue
			}
			keyIndex = len(keys)
			keys = trialKeys
			keyIndexes[id] = keyIndex
			addedKey = true
		}

		occurrence := citationBindingOccurrence{index: index, startRune: startRune, endRune: endRune, startByte: match[0], endByte: match[1], key: keyIndex}
		trialRows := append(append([]any(nil), rows...), citationBindingRow(candidate, occurrence, 0, 0))
		view["keys"], view["occurrences"] = keys, trialRows
		view["included_occurrence_count"] = len(trialRows)
		view["omitted_occurrence_count"] = len(matches) - len(trialRows)
		view["projection_complete"] = len(trialRows) == len(matches)
		if citationBindingViewSize(view) <= budget {
			rows = trialRows
			retained = append(retained, occurrence)
			continue
		}
		view["occurrences"] = rows
		if addedKey {
			keys = keys[:len(keys)-1]
			delete(keyIndexes, id)
			view["keys"] = keys
		}
		view["included_occurrence_count"] = len(rows)
		view["omitted_occurrence_count"] = len(matches) - len(rows)
		view["projection_complete"] = false
		appendBindingOmission(view, &omittedRows, index, func() int {
			if addedKey {
				return -1
			}
			return keyIndex
		}(), startRune, budget)
	}

	view["keys"], view["occurrences"] = keys, rows
	view["omitted_occurrences"] = omittedRows
	view["included_occurrence_count"] = len(rows)
	view["omitted_occurrence_count"] = len(matches) - len(rows)
	view["projection_complete"] = len(rows) == len(matches)
	if includeContext {
		for _, widths := range [][2]int{{32, 16}, {64, 32}, {96, 48}, {citationBindingBeforeRunes, citationBindingAfterRunes}} {
			trial := make([]any, len(retained))
			for i, occurrence := range retained {
				trial[i] = citationBindingRow(candidate, occurrence, widths[0], widths[1])
			}
			old := view["occurrences"]
			view["occurrences"] = trial
			if citationBindingViewSize(view) <= budget {
				rows = trial
			} else {
				view["occurrences"] = old
				break
			}
		}
	}
	view["occurrences"] = rows
	view["context_truncated_count"] = compactBindingTruncatedCount(rows)
	if citationBindingViewSize(view) > budget {
		return nil
	}
	return view
}

func citationBindingKey(id string, records map[string]citationReviewChunk, conflicts map[string]bool, itemIndexes map[string]int) []any {
	if conflicts[id] {
		return []any{"ambiguous", id}
	}
	if index, ok := itemIndexes[id]; ok {
		return []any{"resolved", index}
	}
	record, ok := records[id]
	if !ok {
		return []any{"unknown", id}
	}
	if record.evidenceID != "" && record.jsonPointer != "" && record.knowledgeID != "" {
		return []any{"resolved_source_omitted", id, record.knowledgeID, record.tool, record.toolCallID, record.evidenceID, record.jsonPointer}
	}
	return []any{"source_item_omitted_no_reader", id}
}

func appendBindingOmission(view map[string]any, rows *[]any, index, keyIndex, startRune, budget int) {
	view["projection_complete"] = false
	if len(*rows) >= citationBindingOmissionDetailLimit {
		view["omitted_occurrence_details"] = citationInt(view["omitted_occurrence_details"]) + 1
		return
	}
	trial := append(append([]any(nil), (*rows)...), []any{index, keyIndex, startRune})
	view["omitted_occurrences"] = trial
	if citationBindingViewSize(view) <= budget {
		*rows = trial
	} else {
		view["omitted_occurrences"] = *rows
		view["omitted_occurrence_details"] = citationInt(view["omitted_occurrence_details"]) + 1
	}
}

func citationBindingItemIndexes(evidence map[string]any) map[string]int {
	indexes := map[string]int{}
	items, _ := evidence["items"].([]any)
	for i, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			if id := citationString(item["chunk_id"]); id != "" {
				indexes[id] = i
			}
		}
	}
	return indexes
}

func citationBindingRow(candidate string, o citationBindingOccurrence, beforeRunes, afterRunes int) []any {
	before, after, beforeStart, afterEnd := citationBindingContext(candidate, o.startByte, o.endByte, beforeRunes, afterRunes)
	startByteRune := utf8.RuneCountInString(candidate[:o.startByte])
	endByteRune := startByteRune + utf8.RuneCountInString(candidate[o.startByte:o.endByte])
	beforeStartRune := startByteRune - utf8.RuneCountInString(before)
	afterEndRune := endByteRune + utf8.RuneCountInString(after)
	return []any{o.key, o.startRune, o.endRune, beforeStartRune, before, beforeStart > 0, afterEndRune, after, afterEnd < len(candidate), beforeRunes > 0 || afterRunes > 0}
}

func citationBindingContext(candidate string, start, end, beforeRunes, afterRunes int) (string, string, int, int) {
	beforeStart := start
	for n := 0; n < beforeRunes && beforeStart > 0; n++ {
		_, size := utf8.DecodeLastRuneInString(candidate[:beforeStart])
		if size <= 0 {
			break
		}
		beforeStart -= size
	}
	afterEnd := end
	for n := 0; n < afterRunes && afterEnd < len(candidate); n++ {
		_, size := utf8.DecodeRuneInString(candidate[afterEnd:])
		if size <= 0 {
			break
		}
		afterEnd += size
	}
	return candidate[beforeStart:start], candidate[end:afterEnd], beforeStart, afterEnd
}

func citationBindingViewSize(view map[string]any) int {
	encoded, err := json.Marshal(view)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return len(encoded)
}

func compactBindingTruncatedCount(rows []any) int {
	count := 0
	for _, raw := range rows {
		row, ok := raw.([]any)
		if !ok || len(row) < 10 || row[9] != true || row[5] == true || row[8] == true {
			count++
		}
	}
	return count
}
