package agent

import (
	"encoding/json"
	"regexp"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
)

var privateAnswerEnvelopes = []string{
	"task_coverage", "source_coverage", "runtime_event", "runtime_context",
	"must_use", "structured_evidence_citations", "tool_execution_stopped",
}

var (
	coverageAnswerMarkup = regexp.MustCompile(`(?i)^<[[:space:]]*/?[[:space:]]*(` + strings.Join(privateAnswerEnvelopes, "|") + `)([[:space:]/>{\[]|$)`)
	coverageAnswerEndTag = func() map[string]*regexp.Regexp {
		tags := make(map[string]*regexp.Regexp, len(privateAnswerEnvelopes))
		for _, name := range privateAnswerEnvelopes {
			tags[name] = regexp.MustCompile(`(?i)<[[:space:]]*/[[:space:]]*` + name + `[[:space:]]*>`)
		}
		return tags
	}()
	coverageJSONEnvelope = regexp.MustCompile(`"(` + strings.Join(privateAnswerEnvelopes, "|") + `)"[[:space:]]*:`)
	coverageJSONRuntime  = regexp.MustCompile(`"source"[[:space:]]*:[[:space:]]*"runtime"`)
	coverageJSONType     = regexp.MustCompile(`"type"[[:space:]]*:`)
	coverageJSONGoal     = regexp.MustCompile(`"goal_index"[[:space:]]*:`)
	coverageJSONBinding  = regexp.MustCompile(`"(selection_ref|gap_id)"[[:space:]]*:`)
)

func stripCoverageAnswer(content string) string {
	stripped, _ := splitCoverageAnswer(content)
	return stripped
}

func cleanReviewedAnswerEnvelope(raw string) string {
	if _, valid := decodeReviewedAnswerEnvelope(raw); valid {
		return raw // Keep the candidate's declaration for normal validation.
	}
	clean := stripCoverageAnswer(raw)
	if _, valid := decodeReviewedAnswerEnvelope(clean); valid {
		return clean
	}
	return raw
}

func splitCoverageAnswer(content string) (answer, declaration string) {
	content = stripStandaloneModelArtifacts(content)
	var prose strings.Builder
	removed := false
	inFence := false
	for cursor := 0; cursor < len(content); {
		position := strings.IndexAny(content[cursor:], "<{[`")
		if position < 0 {
			prose.WriteString(content[cursor:])
			break
		}
		position += cursor
		prose.WriteString(content[cursor:position])
		rest := content[position:]
		if strings.HasPrefix(rest, "```") {
			// A dedicated JSON fence can enclose the private envelope. Leave
			// ordinary code and mixed Markdown fenced sections intact.
			if !inFence {
				if length := privateCoverageFence(rest); length > 0 {
					cursor = position + length
					removed = true
					continue
				}
			}
			inFence = !inFence
			prose.WriteString(rest[:3])
			cursor = position + 3
			continue
		}
		if rest[0] == '`' && !inFence {
			if length := privateInlineCoverage(rest); length > 0 {
				cursor = position + length
				removed = true
				continue
			}
		}
		if length, raw := privateCoverageMarkup(rest); length > 0 {
			if !inFence && position > 0 && content[position-1] == '\n' &&
				strings.TrimSpace(rest[length:]) == "" {
				declaration = raw
			}
			removed = true
			cursor = position + length
			continue
		}
		if rest[0] == '{' || rest[0] == '[' {
			length, clean := stripCoverageJSON(rest)
			if length > 0 {
				removed = removed || clean != rest[:length]
				prose.WriteString(clean)
				cursor = position + length
				continue
			}
		}
		prose.WriteByte(rest[0])
		cursor = position + 1
	}
	if !removed {
		return content, ""
	}
	return strings.TrimRight(prose.String(), "\r\n\t "), declaration
}

func privateCoverageMarkup(content string) (length int, declaration string) {
	if content[0] != '<' {
		return 0, ""
	}
	match := coverageAnswerMarkup.FindStringSubmatchIndex(content)
	if match == nil {
		// A provider can stop inside either the opening or the closing tag.
		prefix := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content[1:]), "/"))
		if len(prefix) >= len("task") && len(prefix) <= len("structured_evidence_citations") && privateEnvelopePrefix(prefix) {
			return len(content), ""
		}
		return 0, ""
	}
	name := strings.ToLower(content[match[2]:match[3]])
	afterName := content[match[3]:]
	start := match[3]
	closing := strings.Contains(content[:match[2]], "/")
	if end := strings.IndexAny(afterName, ">{[<\r\n"); end >= 0 && afterName[end] == '>' {
		start += end + 1
		if closing || content[start-2] == '/' {
			return start, ""
		}
	} else if closing {
		return start, ""
	}
	body := content[start:]
	endTag := coverageAnswerEndTag[name]
	// A quoted span can itself contain tag text. A complete JSON value
	// defines the earliest possible closing tag in that case.
	jsonEnd := 0
	trimmed := strings.TrimLeft(body, " \r\n\t")
	taggedJSON := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	if taggedJSON {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		var raw json.RawMessage
		if decoder.Decode(&raw) == nil {
			jsonEnd = len(body) - len(trimmed) + int(decoder.InputOffset())
		}
	}
	if end := coverageClosingTag(body[jsonEnd:], endTag, taggedJSON && jsonEnd == 0); end != nil {
		left, right := jsonEnd+end[0], jsonEnd+end[1]
		raw := body[:left]
		if strings.HasPrefix(content, "<task_coverage>") && body[left:right] == coverageAnswerClose &&
			len(raw) <= 8192 && strings.HasPrefix(strings.TrimSpace(raw), "[") {
			declaration = raw
		}
		return start + right, declaration
	}
	// The closing tag can be absent even when the JSON is complete. Retain
	// following prose if the decoder can establish the envelope boundary.
	if jsonEnd > 0 {
		end := start + jsonEnd
		if tail := strings.TrimLeft(content[end:], " \r\n\t"); strings.HasPrefix(tail, "</") {
			if closeLength, _ := privateCoverageMarkup(tail); closeLength > 0 {
				end = len(content) - len(tail) + closeLength
			}
		}
		return end, ""
	}
	return len(content), ""
}

func coverageClosingTag(body string, tag *regexp.Regexp, malformedJSON bool) []int {
	if !malformedJSON {
		return tag.FindStringIndex(body)
	}
	var quoted, escaped bool
	for scanned := 0; scanned < len(body); {
		end := tag.FindStringIndex(body[scanned:])
		if end == nil {
			return nil
		}
		left, right := scanned+end[0], scanned+end[1]
		for _, character := range body[scanned:left] {
			switch {
			case escaped:
				escaped = false
			case character == '\\' && quoted:
				escaped = true
			case character == '"':
				quoted = !quoted
			}
		}
		if !quoted {
			return []int{left, right}
		}
		scanned, escaped = right, false
	}
	return nil
}

func privateCoverageFence(content string) int {
	afterFence := content[3:]
	line, payload, hasNewline := strings.Cut(afterFence, "\n")
	if hasNewline {
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "json", "text":
			// Continue with the enclosed payload.
		default:
			return 0
		}
	} else {
		payload = afterFence
	}
	block, close, closed := strings.Cut(payload, "```")
	if privateCoverageBlock(block) {
		if !closed {
			return len(content)
		}
		return len(content) - len(close)
	}
	return 0
}

func privateInlineCoverage(content string) int {
	delimiter := "`"
	if strings.HasPrefix(content, "``") {
		delimiter = "``"
	}
	block, tail, _ := strings.Cut(content[len(delimiter):], delimiter)
	if privateCoverageBlock(block) {
		return len(content) - len(tail)
	}
	return 0
}

func privateCoverageBlock(block string) bool {
	body := strings.TrimSpace(block)
	if body == "" {
		return false
	}
	if body[0] == '<' {
		length, _ := privateCoverageMarkup(body)
		return length == len(body)
	}
	length, clean := stripCoverageJSON(body)
	return clean == "" && length == len(body)
}

func stripCoverageJSON(content string) (length int, clean string) {
	if content == "" {
		return 0, ""
	}
	prefix := strings.TrimLeft(content[1:], " \r\n\t")
	switch content[0] {
	case '{':
		if !strings.HasPrefix(prefix, "\"") {
			return 0, ""
		}
	case '[':
		if prefix == "" || !strings.ContainsRune("{[\"-tfn0123456789]", rune(prefix[0])) {
			return 0, ""
		}
	default:
		return 0, ""
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		if (content[0] == '{' || strings.HasPrefix(prefix, "{")) &&
			(coverageJSONEnvelope.MatchString(content) || coverageJSONRuntime.MatchString(content) && coverageJSONType.MatchString(content) ||
				coverageJSONGoal.MatchString(content) && coverageJSONBinding.MatchString(content)) {
			return len(content), ""
		}
		return 0, ""
	}
	length = int(decoder.InputOffset())
	if public, changed, private := sanitizeCoverageJSON(value); changed {
		if private {
			return length, ""
		}
		// Keep ordinary JSON byte-for-byte. Re-encode only the affected
		// value, so a removed nested entry cannot leave invalid commas.
		var encoded strings.Builder
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(public)
		return length, strings.TrimSuffix(encoded.String(), "\n")
	}
	return length, content[:length]
}

func sanitizeCoverageJSON(value any) (public any, changed, private bool) {
	switch data := value.(type) {
	case map[string]any:
		if privateRuntimeEvent(data) || privateResultReview(data) {
			return nil, true, true
		}
		if coverageJSONObject(data) {
			if _, hasPublicAnswer := data["answer"]; !hasPublicAnswer {
				return nil, true, true
			}
			for _, key := range privateAnswerEnvelopes {
				delete(data, key)
			}
			for _, key := range []string{"selection_ref", "gap_id", "goal_index"} {
				delete(data, key)
			}
			changed = true
		}
		for key, item := range data {
			clean, edited, internal := sanitizeCoverageJSON(item)
			changed = changed || edited
			if internal {
				delete(data, key)
			} else if edited {
				data[key] = clean
			}
		}
		return data, changed, false
	case []any:
		publicItems := data[:0]
		for _, item := range data {
			clean, edited, internal := sanitizeCoverageJSON(item)
			changed = changed || edited
			if !internal {
				publicItems = append(publicItems, clean)
			}
		}
		return publicItems, changed, changed && len(publicItems) == 0
	case string:
		sanitized := stripCoverageAnswer(data)
		return sanitized, sanitized != data, false
	}
	return value, false, false
}

func coverageJSONObject(object map[string]any) bool {
	for _, key := range privateAnswerEnvelopes {
		if _, ok := object[key]; ok {
			return true
		}
	}
	// An unwrapped declaration must still have the distinct identifiers of
	// the answer protocol. A regular JSON answer may also use goal_index.
	_, hasGoal := object["goal_index"]
	_, hasSelection := object["selection_ref"]
	_, hasGap := object["gap_id"]
	return hasGoal && (hasSelection || hasGap)
}

func privateRuntimeEvent(object map[string]any) bool {
	eventType, _ := object["type"].(string)
	return object["source"] == "runtime" && eventType != ""
}

func privateResultReview(object map[string]any) bool {
	action, _ := object["action"].(string)
	if len(object) != 2 || object["edits"] == nil || action != "keep" && action != "correct" {
		return false
	}
	raw, _ := json.Marshal(object)
	_, _, valid := decodeStructuredResultReview(string(raw))
	return valid
}

func privateEnvelopePrefix(prefix string) bool {
	prefix = strings.ToLower(prefix)
	for _, tag := range privateAnswerEnvelopes {
		if strings.HasPrefix(tag, prefix) {
			return true
		}
	}
	return false
}

func stripStandaloneModelArtifacts(content string) string {
	if !strings.Contains(content, "</") {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	changed := false
	for i, line := range lines {
		if agenttools.IsProtocolArtifact(line) {
			lines[i] = ""
			changed = true
		}
	}
	if changed {
		return strings.Join(lines, "")
	}
	return content
}

// Structured or tagged-looking text is held until the complete response can
// be checked. A previous prose prefix can be replaced if the terminal answer
// needs re-delivery. No part of a private envelope is sent optimistically.
type coverageAnswerStream struct {
	pending        strings.Builder
	confirmed      bool
	continuingLine bool
}

func (s *coverageAnswerStream) Feed(content string) string {
	if s.pending.Len() != 0 {
		s.pending.WriteString(content)
		if s.confirmed {
			return ""
		}
		content = s.pending.String()
		s.pending.Reset()
	}
	start := coverageStreamPending(content)
	if artifact := modelArtifactPending(content, s.continuingLine); artifact >= 0 && (start < 0 || artifact < start) {
		start = artifact
	}
	if start >= 0 {
		tail := content[start:]
		s.pending.WriteString(tail)
		s.confirmed = coverageAnswerMarkup.MatchString(tail) || (tail[0] == '{' || tail[0] == '[') &&
			strings.TrimLeft(tail[1:], " \r\n\t") != ""
		content = content[:start]
	}
	if newline := strings.LastIndexByte(content, '\n'); newline >= 0 {
		s.continuingLine = strings.TrimSpace(content[newline+1:]) != ""
	} else if strings.TrimSpace(content) != "" {
		s.continuingLine = true
	}
	return content
}

func modelArtifactPending(content string, continuingLine bool) int {
	for start := 0; start < len(content); {
		line, _, complete := strings.Cut(content[start:], "\n")
		if start > 0 || !continuingLine {
			if complete {
				if agenttools.IsProtocolArtifact(line) {
					return start
				}
			} else if strings.TrimSpace(line) != "" {
				var guard agenttools.ProtocolAnswerGuard
				if guard.Feed(line) == "" {
					return start
				}
			}
		}
		start += len(line) + 1
	}
	return -1
}

func coverageStreamPending(content string) int {
	for cursor := 0; cursor < len(content); {
		start := strings.IndexAny(content[cursor:], "<{[`")
		if start < 0 {
			break
		}
		start += cursor
		rest := content[start:]
		tail := strings.TrimLeft(rest[1:], " \r\n\t")
		switch rest[0] {
		case '<':
			name := strings.TrimLeft(strings.TrimPrefix(tail, "/"), " \r\n\t")
			prefix := name[:min(len(name), len("structured_evidence_citations"))]
			if coverageAnswerMarkup.MatchString(rest) || privateEnvelopePrefix(prefix) {
				return start
			}
		case '{':
			if tail == "" {
				return start
			}
			if tail[0] == '"' {
				if length, clean := stripCoverageJSON(rest); length > 0 && clean == rest[:length] {
					cursor = start + length
					continue
				}
				return start
			}
		case '[':
			if tail == "" || strings.ContainsRune("{[\"-tfn0123456789]", rune(tail[0])) {
				if length, clean := stripCoverageJSON(rest); length > 0 && clean == rest[:length] {
					cursor = start + length
					continue
				}
				if tail == "" || tail[0] == '{' || tail[0] == '[' || tail[0] == '"' {
					return start
				}
			}
		case '`':
			for len(rest) > 1 && rest[1] == '`' {
				rest = rest[1:]
			}
			head, _, hasNewline := strings.Cut(rest[1:], "\n")
			label := strings.TrimSpace(strings.ToLower(head))
			if label == "" || strings.HasPrefix(label, "{") || strings.HasPrefix(label, "[") || strings.HasPrefix(label, "<") ||
				(!hasNewline && (strings.HasPrefix("json", label) || strings.HasPrefix("text", label))) || label == "json" || label == "text" {
				return start
			}
		}
		cursor = start + 1
	}
	return -1
}

func (s *coverageAnswerStream) Flush() (string, bool) {
	text := s.pending.String()
	s.pending.Reset()
	s.confirmed = false
	s.continuingLine = false
	return text, stripCoverageAnswer(text) != text
}
