package agent

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

type reviewToolEvidence struct {
	EvidenceID    string   `json:"evidence_id,omitempty"`
	Tool          string   `json:"tool"`
	ToolCallID    string   `json:"tool_call_id,omitempty"`
	Content       any      `json:"content"`
	OriginalBytes int      `json:"original_bytes"`
	ViewComplete  bool     `json:"view_complete"`
	OmittedPaths  []string `json:"omitted_paths,omitempty"`
}

type reviewEvidenceScope struct {
	DistinctResults int  `json:"distinct_results"`
	IncludedResults int  `json:"included_results"`
	OmittedResults  int  `json:"omitted_results"`
	ViewComplete    bool `json:"view_complete"`
}

// Keep a bounded view of every distinct result instead of dropping a whole
// large result. This is structural projection, never a query-dependent source
// ranking or a model-generated summary. The complete durable tool results
// remain the numeric guard's evidence; these views only inform model review.
func boundedReviewEvidence(messages []chat.Message, budget int) ([]reviewToolEvidence, reviewEvidenceScope) {
	names := map[string]string{}
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			names[call.ID] = call.Function.Name
		}
	}
	results := []chat.Message{}
	seen := map[string]bool{}
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		if msg.Name == "" {
			msg.Name = names[msg.ToolCallID]
		}
		key := msg.Name + "\x00" + msg.ToolCallID + "\x00" + msg.Content
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, msg)
	}
	scope := reviewEvidenceScope{DistinctResults: len(results), ViewComplete: true}
	// Bound envelope overhead as well as payload. If even result identities
	// exceed this budget, expose that omission explicitly and retain recent IDs.
	maxResults := min(96, max(1, budget/384))
	if len(results) > maxResults {
		scope.OmittedResults = len(results) - maxResults
		scope.ViewComplete = false
		results = results[len(results)-maxResults:]
	}
	scope.IncludedResults = len(results)
	// A large result must not lose source facts merely because a later small
	// result received an equal reservation. If the complete envelope fits,
	// retain it verbatim. Bound this preflight by raw input size so it cannot
	// materialize an unbounded collection before the normal projection path.
	rawBytes := 0
	for _, msg := range results {
		rawBytes += len(msg.Content)
	}
	if rawBytes <= budget {
		complete := make([]reviewToolEvidence, 0, len(results))
		for _, msg := range results {
			var value any
			decoder := json.NewDecoder(strings.NewReader(msg.Content))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
				value = msg.Content
			}
			complete = append(complete, reviewToolEvidence{EvidenceID: reviewEvidenceID(msg), Tool: msg.Name, ToolCallID: msg.ToolCallID,
				Content: value, OriginalBytes: len(msg.Content), ViewComplete: true})
		}
		encoded, _ := json.Marshal(complete)
		if len(encoded) <= budget {
			return complete, scope
		}
	}
	evidence := make([]reviewToolEvidence, 0, len(results))
	remaining := budget - 2 // JSON array brackets.
	for i, msg := range results {
		share := max(0, remaining/(len(results)-i)-1)
		entry := reviewToolEvidence{EvidenceID: reviewEvidenceID(msg), Tool: msg.Name, ToolCallID: msg.ToolCallID,
			OriginalBytes: len(msg.Content), ViewComplete: true}
		var value any
		decoder := json.NewDecoder(strings.NewReader(msg.Content))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
			value = msg.Content
		}
		entry.Content = value
		encoded, _ := json.Marshal(entry)
		if len(encoded) > share {
			entry.Content = nil
			entry.ViewComplete = false
			overhead, _ := json.Marshal(entry)
			// Reserve room for omission paths in addition to the JSON fragment.
			limit := max(0, share-len(overhead)-min(1024, share/4))
			for {
				entry.Content, entry.OmittedPaths = projectReviewValue(value, limit, "", 0)
				if len(entry.OmittedPaths) == 0 {
					entry.OmittedPaths = []string{""}
				}
				encoded, _ = json.Marshal(entry)
				if len(encoded) <= share || limit == 0 {
					break
				}
				limit = max(0, limit-(len(encoded)-share)-64)
			}
			scope.ViewComplete = false
		}
		if len(encoded) > share {
			scope.OmittedResults++
			scope.IncludedResults--
			scope.ViewComplete = false
			continue
		}
		evidence = append(evidence, entry)
		remaining -= len(encoded) + 1
	}
	return evidence, scope
}

// Paths are JSON Pointers into the original result. Retained values are exact
// source values; arrays preserve their original order and index prefix. Never
// insert ellipses or synthetic rows into source data.
func projectReviewValue(value any, budget int, path string, depth int) (any, []string) {
	encoded, _ := json.Marshal(value)
	if len(encoded) <= budget {
		return value, nil
	}
	if budget < 8 || depth >= 24 {
		return nil, []string{reviewPointer(path)}
	}
	switch data := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		// Identity, status, coverage and pagination scalars precede large
		// containers. Ordering is independent of user wording and values.
		sort.Slice(keys, func(i, j int) bool {
			a, b := reviewFieldPriority(keys[i], data[keys[i]]), reviewFieldPriority(keys[j], data[keys[j]])
			if a != b {
				return a < b
			}
			return keys[i] < keys[j]
		})
		view := map[string]any{}
		omitted := []string{}
		remaining := budget - 2
		for _, key := range keys {
			keyJSON, _ := json.Marshal(key)
			fieldBudget := remaining - len(keyJSON) - 2
			pointer := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			if fieldBudget < 8 {
				omitted = appendReviewOmission(omitted, pointer)
				continue
			}
			child, missing := projectReviewValue(data[key], fieldBudget, pointer, depth+1)
			childJSON, _ := json.Marshal(child)
			view[key] = child
			remaining -= len(keyJSON) + len(childJSON) + 2
			for _, item := range missing {
				omitted = appendReviewOmission(omitted, item)
			}
		}
		return view, omitted
	case []any:
		view := []any{}
		remaining := budget - 2
		omitted := []string{}
		for i, child := range data {
			if remaining < 8 {
				omitted = appendReviewOmission(omitted, reviewPointer(path)+" (remaining array items omitted)")
				break
			}
			projected, missing := projectReviewValue(child, remaining-1, path+"/"+strconv.Itoa(i), depth+1)
			view = append(view, projected)
			childJSON, _ := json.Marshal(projected)
			remaining -= len(childJSON) + 1
			for _, item := range missing {
				omitted = appendReviewOmission(omitted, item)
			}
		}
		return view, omitted
	case string:
		lo, hi := 0, len(data)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			prefix, _ := json.Marshal(data[:mid])
			if len(prefix) <= budget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		for lo > 0 && !utf8.ValidString(data[:lo]) {
			lo--
		}
		return data[:lo], []string{reviewPointer(path) + " (text suffix omitted)"}
	default:
		return nil, []string{reviewPointer(path)}
	}
}

func reviewFieldPriority(key string, value any) int {
	// These are source protocol metadata, not business fields or query terms.
	// Keep the original coverage/scope contract ahead of long content arrays.
	switch key {
	case "coverage", "scan", "document_scope", "pagination", "requested_range", "columns":
		return -1
	}
	switch item := value.(type) {
	case map[string]any, []any:
		return 2
	case string:
		if len(item) > 256 {
			return 3
		}
	}
	return 0
}

func appendReviewOmission(paths []string, path string) []string {
	if len(paths) < 24 {
		return append(paths, path)
	}
	if len(paths) == 24 {
		return append(paths, " (additional omitted paths not listed)")
	}
	return paths
}

func reviewPointer(path string) string {
	// RFC 6901 uses the empty string for the root; "/" names an empty key.
	return path
}
