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

type completedReviewJSONRead struct {
	EvidenceID  string
	JSONPointer string
	Value       any
}

type reviewJSONReadArgs struct {
	Operation  string `json:"operation"`
	EvidenceID string `json:"evidence_id"`
	Pointer    string `json:"json_pointer"`
	Offset     int    `json:"offset"`
	MaxChars   int    `json:"max_chars"`
	Limit      int    `json:"limit"`
	Query      string `json:"query"`
	StartIndex *int   `json:"start_index"`
	MaxItems   int    `json:"max_items"`
}

type reviewJSONReadSegment struct {
	start int
	end   int
	text  string
}

type reviewJSONReadGroup struct {
	evidenceID string
	pointer    string
	length     int
	expected   string
	segments   []reviewJSONReadSegment
}

// completedReviewJSONReads reconstructs only complete JSON values exposed by
// contiguous, exact Unicode-serialized read_review_evidence pages. It does
// not expand the reader budget or turn a partial projection into source data.
func completedReviewJSONReads(reader *reviewEvidenceReader) []completedReviewJSONRead {
	groups := verifiedReviewJSONReadGroups(reader)
	completed := make([]completedReviewJSONRead, 0, len(groups))
	for _, group := range groups {
		if value, ok := completeReviewJSONReadValue(group); ok {
			completed = append(completed, completedReviewJSONRead{EvidenceID: group.evidenceID, JSONPointer: group.pointer, Value: value})
		}
	}
	return completed
}

func verifiedReviewJSONReadGroups(reader *reviewEvidenceReader) []*reviewJSONReadGroup {
	if reader == nil || reader.reads > reviewEvidenceReadLimit || reader.remaining < 0 || reader.remaining > reviewEvidenceRuneBudget {
		return nil
	}
	spent := reviewEvidenceRuneBudget - reader.remaining
	if spent > reviewEvidenceRuneBudget {
		return nil
	}

	type pairedCall struct {
		args  reviewJSONReadArgs
		valid bool
	}
	calls := map[string]pairedCall{}
	callCounts := map[string]int{}
	toolCallCount := 0
	readCallCount := 0
	for _, message := range reader.transcript {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name != reviewEvidenceToolName {
				continue
			}
			toolCallCount++
			if call.ID == "" {
				continue
			}
			callCounts[call.ID]++
			var args reviewJSONReadArgs
			decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
			decoder.DisallowUnknownFields()
			valid := len(call.Function.Arguments) <= 4096 && decoder.Decode(&args) == nil && decoder.Decode(new(any)) == io.EOF
			if valid && args.Operation == "read" {
				readCallCount++
			}
			calls[call.ID] = pairedCall{args: args, valid: valid}
		}
	}
	if toolCallCount > reviewEvidenceReadLimit || readCallCount > reviewEvidenceReadLimit {
		return nil
	}

	responses := map[string][]chat.Message{}
	for _, message := range reader.transcript {
		if message.Role == "tool" && message.Name == reviewEvidenceToolName && message.ToolCallID != "" {
			responses[message.ToolCallID] = append(responses[message.ToolCallID], message)
		}
	}

	groups := map[string]*reviewJSONReadGroup{}
	pageRunes := 0
	for callID, call := range calls {
		if callCounts[callID] != 1 || !call.valid || call.args.Operation != "read" ||
			call.args.EvidenceID == "" || len(call.args.Pointer) > 1024 ||
			call.args.Offset < 0 || call.args.MaxChars < 0 || call.args.MaxChars > reviewEvidenceSegmentLimit ||
			call.args.Limit < 0 || call.args.Limit > 24 || call.args.Query != "" || len(responses[callID]) != 1 {
			continue
		}
		response := responses[callID][0]
		var result map[string]any
		decoder := json.NewDecoder(strings.NewReader(response.Content))
		decoder.UseNumber()
		if decoder.Decode(&result) != nil || result == nil || decoder.Decode(new(any)) != io.EOF || result["status"] != "ok" {
			continue
		}
		if result["evidence_id"] != call.args.EvidenceID || result["json_pointer"] != call.args.Pointer ||
			result["offset_space"] != "unicode_serialized_json" {
			continue
		}
		responseOffset, offsetOK := exactReviewIndex(result["content_offset"])
		contentLength, lengthOK := exactReviewIndex(result["content_length"])
		requestedMax := call.args.MaxChars
		if requestedMax == 0 {
			requestedMax = 4000
		}
		segment, segmentOK := result["serialized_json_segment"].(string)
		segmentRunes := []rune(segment)
		if !offsetOK || !lengthOK || responseOffset != call.args.Offset || contentLength <= 0 ||
			!segmentOK || len(segmentRunes) == 0 ||
			len(segmentRunes) > requestedMax || len(segmentRunes) > reviewEvidenceSegmentLimit ||
			responseOffset > contentLength || len(segmentRunes) > contentLength-responseOffset {
			continue
		}
		end := responseOffset + len(segmentRunes)
		segmentComplete, completeOK := result["segment_complete"].(bool)
		viewComplete, viewOK := result["view_complete"].(bool)
		if !completeOK || !viewOK || segmentComplete != (end == contentLength) ||
			viewComplete != (responseOffset == 0 && end == contentLength) {
			continue
		}
		if end < contentLength {
			next, nextOK := exactReviewIndex(result["next_content_offset"])
			if !nextOK || next != end {
				continue
			}
		} else if _, hasNext := result["next_content_offset"]; hasNext {
			continue
		}

		record, found := reviewRecordByID(reader, call.args.EvidenceID)
		if !found || result["tool"] != record.Tool || result["tool_call_id"] != record.ToolCallID || !reviewReceiptEligibleResult(record) {
			continue
		}
		payload, ok := record.Value.(map[string]any)
		if !ok || payload["success"] != true {
			continue
		}
		rawValue, err := reviewPointerValue(record.Value, call.args.Pointer)
		if err != nil {
			continue
		}
		if _, plainText := rawValue.(string); plainText {
			continue
		}
		encodedSource, err := json.Marshal(rawValue)
		if err != nil {
			continue
		}
		sourceRunes := []rune(string(encodedSource))
		if len(sourceRunes) != contentLength || string(sourceRunes[responseOffset:end]) != segment {
			continue
		}
		pageRunes += len(segmentRunes)
		if pageRunes > reviewEvidenceRuneBudget || pageRunes > spent {
			return nil
		}

		key := call.args.EvidenceID + "\x00" + call.args.Pointer
		group := groups[key]
		if group == nil {
			group = &reviewJSONReadGroup{evidenceID: call.args.EvidenceID, pointer: call.args.Pointer,
				length: contentLength, expected: string(encodedSource)}
			groups[key] = group
		}
		if group.length != contentLength || group.expected != string(encodedSource) {
			continue
		}
		group.segments = append(group.segments, reviewJSONReadSegment{start: responseOffset, end: end, text: segment})
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	verified := make([]*reviewJSONReadGroup, 0, len(groups))
	for _, key := range keys {
		verified = append(verified, groups[key])
	}
	return verified
}

func completeReviewJSONReadValue(group *reviewJSONReadGroup) (any, bool) {
	if group == nil {
		return nil, false
	}
	sort.Slice(group.segments, func(i, j int) bool { return group.segments[i].start < group.segments[j].start })
	cursor := 0
	var joined strings.Builder
	valid := len(group.segments) > 0
	for _, part := range group.segments {
		if part.start != cursor || part.end <= part.start || part.end > group.length {
			valid = false
			break
		}
		joined.WriteString(part.text)
		cursor = part.end
	}
	if !valid || cursor != group.length || joined.String() != group.expected {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(joined.String()))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, false
	}
	encodedValue, err := json.Marshal(value)
	if err != nil || string(encodedValue) != group.expected {
		return nil, false
	}
	return value, true
}

// reviewWitnessVisibleInJSONPages proves a child value was fully exposed by
// verified serialized-JSON pages, even when the containing JSON value was not
// fully read. The child span is derived by traversing JSON structure; repeated
// equal text at another path cannot satisfy this check.
func reviewWitnessVisibleInJSONPages(reader *reviewEvidenceReader, witness reviewSourceWitness) bool {
	if reader == nil || witness.EvidenceID == "" || witness.JSONPointer == "" || witness.Quote == "" {
		return false
	}
	record, ok := reviewRecordByID(reader, witness.EvidenceID)
	if !ok || !reviewReceiptEligibleResult(record) {
		return false
	}
	payload, ok := record.Value.(map[string]any)
	if !ok || payload["success"] != true {
		return false
	}
	childValue, err := reviewPointerValue(record.Value, witness.JSONPointer)
	if err != nil || !reviewQuoteMatchesValue(childValue, witness.Quote) {
		return false
	}

	for _, group := range verifiedReviewJSONReadGroups(reader) {
		if group.evidenceID != witness.EvidenceID {
			continue
		}
		parentValue, err := reviewPointerValue(record.Value, group.pointer)
		if err != nil {
			continue
		}
		relative, descendant := relativeReviewJSONPointer(group.pointer, witness.JSONPointer)
		if !descendant {
			continue
		}
		start, end, exactChild, found := reviewJSONValueRuneSpan(parentValue, relative)
		if !found || !reviewQuoteMatchesValue(exactChild, witness.Quote) || start < 0 || end <= start || end > group.length {
			continue
		}
		if reviewJSONReadRangeCovered(group, start, end) {
			return true
		}
	}
	return false
}

func relativeReviewJSONPointer(parent, child string) (string, bool) {
	if child == parent {
		return "", true
	}
	if parent == "" {
		return child, strings.HasPrefix(child, "/")
	}
	if strings.HasPrefix(child, parent+"/") {
		return strings.TrimPrefix(child, parent), true
	}
	return "", false
}

func reviewJSONPointerTokens(pointer string) ([]string, bool) {
	if pointer == "" {
		return nil, true
	}
	if !strings.HasPrefix(pointer, "/") || len(pointer) > 1024 {
		return nil, false
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		var decoded strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				decoded.WriteByte(part[j])
				continue
			}
			if j+1 >= len(part) || (part[j+1] != '0' && part[j+1] != '1') {
				return nil, false
			}
			j++
			if part[j] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[i] = decoded.String()
	}
	return parts, true
}

// reviewJSONValueRuneSpan returns the serialized-JSON rune interval for a
// pointer relative to root. Map offsets follow encoding/json's sorted-key
// serialization and array offsets follow original array order.
func reviewJSONValueRuneSpan(root any, pointer string) (int, int, any, bool) {
	tokens, ok := reviewJSONPointerTokens(pointer)
	if !ok {
		return 0, 0, nil, false
	}
	return reviewJSONValueRuneSpanAt(root, tokens, 0)
}

func reviewJSONValueRuneSpanAt(value any, tokens []string, base int) (int, int, any, bool) {
	if len(tokens) == 0 {
		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, 0, nil, false
		}
		return base, base + utf8.RuneCount(encoded), value, true
	}
	switch current := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		cursor := 1 // opening brace
		for index, key := range keys {
			if index > 0 {
				cursor++ // comma
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return 0, 0, nil, false
			}
			child := current[key]
			encodedValue, err := json.Marshal(child)
			if err != nil {
				return 0, 0, nil, false
			}
			keyRunes := utf8.RuneCount(encodedKey)
			valueRunes := utf8.RuneCount(encodedValue)
			valueStart := cursor + keyRunes + 1 // key plus colon
			if key == tokens[0] {
				return reviewJSONValueRuneSpanAt(child, tokens[1:], base+valueStart)
			}
			cursor = valueStart + valueRunes
		}
	case []any:
		index, err := strconv.Atoi(tokens[0])
		if err != nil || index < 0 || strconv.Itoa(index) != tokens[0] || index >= len(current) {
			return 0, 0, nil, false
		}
		cursor := 1 // opening bracket
		for i := 0; i < index; i++ {
			encoded, err := json.Marshal(current[i])
			if err != nil {
				return 0, 0, nil, false
			}
			cursor += utf8.RuneCount(encoded) + 1 // element plus comma
		}
		return reviewJSONValueRuneSpanAt(current[index], tokens[1:], base+cursor)
	}
	return 0, 0, nil, false
}

func reviewJSONReadRangeCovered(group *reviewJSONReadGroup, start, end int) bool {
	if group == nil || start < 0 || end <= start || end > group.length {
		return false
	}
	segments := append([]reviewJSONReadSegment(nil), group.segments...)
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].start != segments[j].start {
			return segments[i].start < segments[j].start
		}
		return segments[i].end < segments[j].end
	})
	cursor := start
	for _, segment := range segments {
		if segment.end <= cursor {
			continue
		}
		if segment.start > cursor {
			return false
		}
		cursor = segment.end
		if cursor >= end {
			return true
		}
	}
	return false
}
