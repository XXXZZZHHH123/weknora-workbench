package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

type fieldConfirmationTurnKey struct{}
type fieldConfirmationHumanKey struct{}
type fieldConfirmationTurn struct {
	question     string
	previousID   string
	pendingID    string
	priorIDs     []string
	contextSheet string
	date         string
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
			turn.previousID = id
			turn.pendingID = ""
			if status == "needs_clarification" {
				turn.pendingID = id
				turn.priorIDs = nil
				turn.contextSheet = ""
				turn.date = ""
			}
			if status == "resolved" {
				data := structuredOutcome(msg.Content)
				turn.priorIDs = nil
				turn.contextSheet = ""
				turn.date = ""
				answer, _ := data["answer"].(map[string]any)
				sources, _ := answer["sources"].(map[string]any)
				seen := map[string]bool{}
				dates := map[string]bool{}
				sheets := map[string]bool{}
				for _, item := range sources {
					source, _ := item.(map[string]any)
					series, _ := source["series"].(map[string]any)
					sid, _ := series["series_id"].(string)
					if sid != "" && !seen[sid] {
						turn.priorIDs = append(turn.priorIDs, sid)
						seen[sid] = true
					}
					if sheet, _ := series["sheet"].(string); sheet != "" {
						sheets[sheet] = true
					}
					if op, _ := answer["operation"].(string); op == "latest" || op == "rows" {
						if rows, _ := source["rows"].([]any); len(rows) == 1 {
							row, _ := rows[0].(map[string]any)
							if day, _ := row["date"].(string); day != "" {
								dates[day] = true
							}
						}
					}
				}
				sort.Strings(turn.priorIDs)
				if len(dates) == 1 {
					for day := range dates {
						turn.date = day
					}
				}
				if len(sheets) == 1 {
					for sheet := range sheets {
						turn.contextSheet = sheet
					}
				}
			}
		}
	}
	return context.WithValue(ctx, fieldConfirmationTurnKey{}, turn)
}

// Decode data only from a completed tool outcome, never from user messages.
func structuredOutcome(content string) map[string]any {
	start := strings.Index(content, "{")
	if start < 0 || len(content) > 200000 {
		return nil
	}
	var data map[string]any
	if json.NewDecoder(strings.NewReader(content[start:])).Decode(&data) != nil {
		return nil
	}
	if data["kind"] == "structured_field_resolution" || data["kind"] == "structured_tool_notice" {
		return data
	}
	for _, key := range []string{"content_items", "content"} {
		if items, ok := data[key].([]any); ok {
			for _, item := range items {
				if part, ok := item.(map[string]any); ok {
					if s, ok := part["text"].(string); ok {
						if result := structuredOutcome(s); result != nil {
							return result
						}
					}
				}
			}
		}
	}
	if s, ok := data["output"].(string); ok {
		return structuredOutcome(s)
	}
	return nil
}

func (t *fieldConfirmationTurn) resolveArgs() map[string]any {
	args := map[string]any{"question": t.question}
	if t.pendingID != "" {
		args["pending_resolution_id"] = t.pendingID
	}
	if len(t.priorIDs) > 0 {
		args["prior_series_ids"] = t.priorIDs
		if t.contextSheet != "" {
			args["context_sheet"] = t.contextSheet
		}
	}
	if t.previousID != "" {
		args["previous_resolution_id"] = t.previousID
	}
	return args
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
		delete(args, "previous_resolution_id")
		if turn.previousID != "" {
			args["previous_resolution_id"] = turn.previousID
		}
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
