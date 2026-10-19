package agent

import (
	"fmt"
	"strings"
)

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
