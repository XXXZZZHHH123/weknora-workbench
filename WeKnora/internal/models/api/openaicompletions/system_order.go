package openaicompletions

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/api"
)

// isSystemMessageOrderError is deliberately limited to an explicit protocol
// rejection. Authentication, context limits, transport failures and stream
// errors must not trigger a second completion request.
func isSystemMessageOrderError(err error) bool {
	var upstream *api.HTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusBadRequest {
		return false
	}
	message := upstream.Body
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal([]byte(message), &envelope) == nil {
		// Read error text rather than an echoed prompt or arbitrary metadata.
		message = envelope.Message
		if message == "" {
			message = envelope.Detail
		}
		if len(envelope.Error) > 0 {
			var nested struct {
				Message string `json:"message"`
			}
			var plain string
			if json.Unmarshal(envelope.Error, &nested) == nil && nested.Message != "" {
				message = nested.Message
			} else if json.Unmarshal(envelope.Error, &plain) == nil {
				message = plain
			}
		}
	}
	message = strings.ToLower(message)
	return strings.Contains(message, "system message must be at the beginning") ||
		strings.Contains(message, "system messages must be at the beginning")
}

// leadingSystemMessages coalesces only system instructions into one leading
// turn, preserving their relative order. In particular, a RuntimeNotice keeps
// its trusted role; user input and tool evidence never become instructions.
// Developer messages retain their own role and are not merged with system
// messages. No caller-owned messages or content slices are modified.
//
// Moving request-scoped instructions changes the cache prefix on this fallback
// request. Accepting endpoints keep the ordinary append-only representation.
func leadingSystemMessages(messages []wireMessage) ([]wireMessage, bool) {
	var parts []wirePart
	count := 0
	misplaced := false
	plainText := true
	for i, msg := range messages {
		if msg.Role != "system" {
			continue
		}
		// Do not silently discard unsupported instruction metadata.
		if msg.Name != "" || msg.ToolCallID != "" || len(msg.ToolCalls) > 0 ||
			msg.ReasoningContent != nil || len(msg.ReasoningDetails) > 0 {
			return nil, false
		}
		if count > 0 {
			parts = append(parts, wirePart{Type: "text", Text: "\n\n"})
		}
		switch content := msg.Content.(type) {
		case string:
			parts = append(parts, wirePart{Type: "text", Text: content})
		case []wirePart:
			plainText = false
			parts = append(parts, content...)
		case nil:
			// An empty system turn contributes no content.
		default:
			return nil, false
		}
		count++
		misplaced = misplaced || i != 0
	}
	if !misplaced {
		return nil, false
	}
	instruction := wireMessage{Role: "system", Content: parts}
	if plainText {
		var content strings.Builder
		for _, part := range parts {
			content.WriteString(part.Text)
		}
		instruction.Content = content.String()
	}
	ordered := make([]wireMessage, 0, len(messages)-count+1)
	ordered = append(ordered, instruction)
	for _, msg := range messages {
		if msg.Role != "system" {
			ordered = append(ordered, msg)
		}
	}
	return ordered, true
}
