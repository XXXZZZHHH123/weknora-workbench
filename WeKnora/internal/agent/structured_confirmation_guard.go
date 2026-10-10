package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

type fieldConfirmationTurnKey struct{}
type fieldConfirmationHumanKey struct{}
type fieldConfirmationTurn struct {
	question  string
	pendingID string
}

var fieldToolName = regexp.MustCompile(`^(?:mcp__)?(?:resolve_fields|confirm_fields)(?:_[a-z0-9]+)?$`)
var confirmToolName = regexp.MustCompile(`^(?:mcp__)?confirm_fields(?:_[a-z0-9]+)?$`)

// Only previous tool outcomes can authorize a plain-text clarification. A plan
// generated in this turn has not yet been seen or selected by the human.
func withFieldConfirmationTurn(ctx context.Context, query string, history []chat.Message) context.Context {
	turn := &fieldConfirmationTurn{question: query}
	names := map[string]string{}
	for _, msg := range history {
		for _, call := range msg.ToolCalls {
			names[call.ID] = call.Function.Name
		}
		name := msg.Name
		if name == "" {
			name = names[msg.ToolCallID]
		}
		if msg.Role != "tool" || (!fieldToolName.MatchString(name) && name != "call_mcp_tool") {
			continue
		}
		if id, status := fieldResolutionIdentity(msg.Content); id != "" {
			turn.pendingID = ""
			if status == "needs_clarification" {
				turn.pendingID = id
			}
		}
	}
	return context.WithValue(ctx, fieldConfirmationTurnKey{}, turn)
}

func fieldResolutionIdentity(content string) (string, string) {
	if len(content) > 100000 {
		return "", ""
	}
	start := strings.Index(content, "{")
	if start < 0 {
		return "", ""
	}
	var data map[string]any
	if json.NewDecoder(strings.NewReader(content[start:])).Decode(&data) != nil {
		return "", ""
	}
	if data["kind"] == "structured_field_resolution" {
		id, _ := data["resolution_id"].(string)
		status, _ := data["status"].(string)
		if regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(id) {
			return id, status
		}
	}
	for _, key := range []string{"content_items", "content"} {
		if items, ok := data[key].([]any); ok {
			for _, item := range items {
				if part, ok := item.(map[string]any); ok {
					if text, ok := part["text"].(string); ok {
						if id, status := fieldResolutionIdentity(text); id != "" {
							return id, status
						}
					}
				}
			}
		}
	}
	if output, ok := data["output"].(string); ok {
		return fieldResolutionIdentity(output)
	}
	return "", ""
}

func guardFieldConfirmation(ctx context.Context, name string, args map[string]any) error {
	turn, ok := ctx.Value(fieldConfirmationTurnKey{}).(*fieldConfirmationTurn)
	if !ok || ctx.Value(fieldConfirmationHumanKey{}) == true {
		return nil
	}
	if fieldToolName.MatchString(name) && !confirmToolName.MatchString(name) {
		proposed, _ := args["question"].(string)
		if turn.pendingID != "" && proposed != turn.question && !strings.Contains(proposed, turn.question) {
			args["question"] = proposed + "\n用户本轮原话：" + turn.question
		} else {
			args["question"] = turn.question
		}
		return nil
	}
	if !confirmToolName.MatchString(name) {
		return nil
	}

	id, _ := args["resolution_id"].(string)
	if turn.pendingID == "" || id != turn.pendingID {
		return fmt.Errorf("FIELD_CONFIRMATION_WAIT_FOR_USER: 本轮待选方案尚未收到用户选择；请结束本轮等待用户，不得替用户确认")
	}
	// A model cannot manufacture a human's selections or rewrite their note.
	// Plain-text replies are interpreted by the field service using the exact
	// human message; UI selections take the separate direct submission path.
	args["selections"] = nil
	args["note"] = turn.question
	return nil
}
