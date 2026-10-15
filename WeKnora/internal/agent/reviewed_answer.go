package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

var reviewedAnswerSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string","description":"Only the requested final results in the requested language, with actual source citations and minimal necessary scope or limitations. Omit unrequested commentary, causal analysis or additional statistics. Preserve the meaning and identity of the actual source fields. No private reasoning, working draft or promises of future work."}},"required":["answer"],"additionalProperties":false}`)

// Only this buffered answer protocol changes text decoding order. Other JSON
// modes and ordinary tool-call arguments keep their existing streaming path.
func isReviewedAnswerFormat(format json.RawMessage) bool {
	return bytes.Equal(format, reviewedAnswerSchema)
}

// Parse an explicit model response contract, never detect reasoning or choose
// an answer using business keywords. Older providers can ignore JSON mode;
// their plain-text response retains the existing completeness/source checks.
func decodeReviewedAnswer(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		// Legacy prose remains supported, but a provider that mixes prose and
		// an explicit answer envelope has failed the delivery contract. Never
		// expose that envelope or pick one of the two competing answers.
		if containsReviewedAnswerEnvelope(trimmed) {
			return ""
		}
		return content
	}
	decoder := json.NewDecoder(strings.NewReader(escapeJSONLiteralControls(trimmed)))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ""
	}
	key, err := decoder.Token()
	if err != nil || key != "answer" {
		return ""
	}
	var answer *string
	if decoder.Decode(&answer) != nil || answer == nil {
		return ""
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return "" // Extra or duplicate fields do not satisfy this contract.
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ""
	}
	return *answer
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
