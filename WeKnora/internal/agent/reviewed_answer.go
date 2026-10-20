package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

var reviewedAnswerSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string","description":"Only the requested final results in the requested language, with actual source citations and minimal necessary scope or limitations. Omit unrequested commentary, causal analysis or additional statistics. Preserve the meaning and identity of the actual source fields. Use natural prose, concise tables and normal text for amounts/units; reserve math notation for actual formulas. Use real source titles, no internal document handles, execution IDs or audit commentary in visible prose. Keep traceable citation attributes intact. No private reasoning, working draft or promises of future work."}},"required":["answer"],"additionalProperties":false}`)

// Only this buffered answer protocol changes text decoding order. Other JSON
// modes and ordinary tool-call arguments keep their existing streaming path.
func isReviewedAnswerFormat(format json.RawMessage) bool {
	return bytes.Equal(format, reviewedAnswerSchema)
}

// Parse an explicit model response contract, never detect reasoning or choose
// an answer using business keywords. Older providers can ignore JSON mode;
// their plain-text response retains the existing completeness/source checks.
func decodeReviewedAnswer(content string) string {
	answer, valid := decodeReviewedAnswerEnvelope(content)
	if valid {
		return answer
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if beginsJSONValue(trimmed) || containsReviewedAnswerEnvelope(trimmed) {
		return ""
	}
	return content
}

// decodeReviewedAnswerEnvelope accepts only the explicit, complete delivery
// envelope. It is intentionally separate from legacy prose compatibility so
// a serialization repair can never treat prose as a successful JSON repair.
func decodeReviewedAnswerEnvelope(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	if trimmed[0] != '{' {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(escapeJSONLiteralControls(trimmed)))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return "", false
	}
	key, err := decoder.Token()
	if err != nil || key != "answer" {
		return "", false
	}
	var answer *string
	if decoder.Decode(&answer) != nil || answer == nil {
		return "", false
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return "", false // Extra or duplicate fields do not satisfy this contract.
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", false
	}
	return *answer, true
}

func beginsJSONValue(content string) bool {
	if content == "" {
		return false
	}
	return content[0] == '{' || content[0] == '['
}

// This only recognizes the explicit delivery protocol, never question or
// answer semantics. It is used to reject mixed framing, not extract a guessed
// answer. Correct JSON answer strings may themselves contain example JSON.
func containsReviewedAnswerEnvelope(content string) bool {
	for offset := 0; offset < len(content); {
		index := strings.IndexByte(content[offset:], '{')
		if index < 0 {
			return false
		}
		offset += index
		decoder := json.NewDecoder(strings.NewReader(content[offset:]))
		opening, err := decoder.Token()
		if err == nil && opening == json.Delim('{') {
			key, keyErr := decoder.Token()
			if keyErr == nil && key == "answer" {
				return true // Also reject a truncated or malformed envelope.
			}
		}
		offset++
	}
	return false
}

// Some compatible providers emit literal LF/CR/TAB bytes inside a JSON string
// despite JSON output mode. Escaping only those already-delimited string
// characters is lossless: the decoded answer has the exact same characters.
// This never supplies quotes/brackets, removes payload text, chooses a prefix,
// or repairs incomplete JSON. Strict parsing remains the only acceptance gate.
func escapeJSONLiteralControls(content string) string {
	var normalized strings.Builder
	normalized.Grow(len(content))
	inString, escaped := false, false
	for index := 0; index < len(content); index++ {
		char := content[index]
		if !inString {
			normalized.WriteByte(char)
			if char == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			normalized.WriteByte(char)
			escaped = false
			continue
		}
		switch char {
		case '\\':
			normalized.WriteByte(char)
			escaped = true
		case '"':
			normalized.WriteByte(char)
			inString = false
		case '\n':
			normalized.WriteString(`\n`)
		case '\r':
			normalized.WriteString(`\r`)
		case '\t':
			normalized.WriteString(`\t`)
		default:
			normalized.WriteByte(char)
		}
	}
	return normalized.String()
}
