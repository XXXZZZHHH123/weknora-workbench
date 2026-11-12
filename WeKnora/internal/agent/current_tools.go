package agent

import (
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

// Send only the current request's callable names, after task projection. A
// previous assistant answer may describe tools that were disabled in that turn.
// The snapshot neither changes user messages nor accumulates stale capability
// lists in persisted conversation history.
func currentToolAvailabilityNotice(messages []chat.Message, tools []chat.Tool) string {
	// A first request has no prior answer to confuse with the current runtime.
	// Keep that common path (and its provider message ordering) unchanged.
	priorAssistant, laterUser := false, false
	for _, message := range messages {
		if message.Role == "assistant" {
			priorAssistant = true
		}
		if message.Role == "user" && priorAssistant {
			laterUser = true
			break
		}
	}
	if !laterUser {
		return ""
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}
	notice := struct {
		Source    string   `json:"source"`
		Type      string   `json:"type"`
		Scope     string   `json:"scope"`
		ToolNames []string `json:"available_tool_names"`
		Message   string   `json:"message"`
	}{
		Source: "runtime", Type: "current_tool_availability", Scope: "current_model_request",
		ToolNames: names,
		Message:   "Use the attached tool definitions as the current callable capabilities. Earlier assistant claims or tool lists may describe a different turn and do not establish current availability. Before reporting a needed capability unavailable, check these definitions and any available capability-discovery tool. Availability does not establish successful access to a source or override user restrictions, permissions, or safety requirements. Use tools only when needed for the current task; this snapshot is not source evidence.",
	}
	encoded, _ := json.Marshal(notice) // All fields are strings or a string slice.
	return "<runtime_event>" + string(encoded) + "</runtime_event>"
}

func withCurrentToolAvailability(messages []chat.Message, tools []chat.Tool) []chat.Message {
	notice := currentToolAvailabilityNotice(messages, tools)
	if notice == "" {
		return messages
	}
	// Keep a single leading system message for compatible local templates.
	// Copy before appending so the live transcript and its stable prefix remain
	// unchanged; every model request receives a freshly computed snapshot.
	result := append([]chat.Message(nil), messages...)
	if len(result) > 0 && result[0].Role == "system" {
		result[0].Content += "\n\n" + notice
		return result
	}
	return append([]chat.Message{{Role: "system", Content: notice}}, result...)
}
