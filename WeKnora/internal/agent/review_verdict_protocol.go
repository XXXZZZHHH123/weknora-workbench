package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxReviewProtocolResponseBytes = 8192

type reviewProtocolResponseKey struct{}

// This is private feedback to the same evaluator, never source evidence,
// author-facing repair advice or a reusable review approval.
type reviewProtocolResponse struct {
	Text      string `json:"text"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Complete  bool   `json:"complete_view"`
	JSONValid bool   `json:"json_valid"`
}

type reviewProtocolResponseError struct {
	cause    error
	response reviewProtocolResponse
}

func (err *reviewProtocolResponseError) Error() string { return err.cause.Error() }
func (err *reviewProtocolResponseError) Unwrap() error { return err.cause }

func reviewErrorWithActualResponse(cause error, content string) error {
	if cause == nil {
		return nil
	}
	digest := sha256.Sum256([]byte(content))
	limit := min(len(content), maxReviewProtocolResponseBytes)
	for limit > 0 && !utf8.ValidString(content[:limit]) {
		limit--
	}
	return &reviewProtocolResponseError{cause: cause, response: reviewProtocolResponse{
		Text: strings.Clone(content[:limit]), SHA256: hex.EncodeToString(digest[:]),
		Bytes: len(content), Complete: limit == len(content), JSONValid: json.Valid([]byte(content)),
	}}
}

// normalizeReviewVerdictEnvelope removes only a Markdown transport envelope.
// The caller must still validate the complete JSON value, schema and verdict.
// Some compatible endpoints omit an opening fence but retain its closing line.
// Neither prose, extra JSON values nor invalid schema fields are repaired here.
func normalizeReviewVerdictEnvelope(content string) (string, error) {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		header, body, ok := strings.Cut(text, "\n")
		header = strings.TrimSpace(header)
		if !ok || (header != "```" && !strings.EqualFold(header, "```json")) {
			return "", fmt.Errorf("invalid review JSON fence header")
		}
		body = strings.TrimSpace(body)
		if !strings.HasSuffix(body, "\n```") {
			return "", fmt.Errorf("unterminated review JSON fence")
		}
		return strings.TrimSpace(strings.TrimSuffix(body, "\n```")), nil
	}
	if strings.HasSuffix(text, "\n```") {
		text = strings.TrimSpace(strings.TrimSuffix(text, "\n```"))
	}
	return text, nil
}
