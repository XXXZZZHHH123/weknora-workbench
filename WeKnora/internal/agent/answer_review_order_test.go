package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// uniqueJSONUserInput selects a request by its actual message role and strict
// JSON payload shape, rather than assuming a fixed message index.
func uniqueJSONUserInput(t *testing.T, calls [][]chat.Message, requiredField string) (int, []chat.Message, chat.Message, map[string]json.RawMessage) {
	t.Helper()
	selectedIndex := -1
	var selectedMessages []chat.Message
	var selectedUser chat.Message
	var selectedPayload map[string]json.RawMessage
	selected := 0
	for callIndex, messages := range calls {
		userCount := 0
		var matchedUser chat.Message
		var matchedPayload map[string]json.RawMessage
		matched := false
		rolesValid := true
		for _, message := range messages {
			switch message.Role {
			case "system":
			case "user":
				userCount++
				var payload map[string]json.RawMessage
				decoder := json.NewDecoder(strings.NewReader(message.Content))
				if err := decoder.Decode(&payload); err != nil || payload == nil {
					continue
				}
				if err := decoder.Decode(new(any)); err != io.EOF {
					continue
				}
				if _, exists := payload[requiredField]; exists {
					matched = true
					matchedUser = message
					matchedPayload = payload
				}
			default:
				rolesValid = false
			}
		}
		if !matched {
			continue
		}
		require.True(t, rolesValid, "an independent review request must not contain assistant or tool transcript rows")
		require.Equal(t, 1, userCount, "an independent review request has exactly one JSON user input")
		require.NotEmpty(t, matchedPayload[requiredField])
		var stringValue string
		require.NoError(t, json.Unmarshal(matchedPayload[requiredField], &stringValue), "the selected review field is a JSON string")
		selected++
		selectedIndex = callIndex
		selectedMessages, selectedUser, selectedPayload = messages, matchedUser, matchedPayload
	}
	require.Equal(t, 1, selected, "expected exactly one request with the selected strict JSON field")
	if requiredField == "unverified_draft" {
		system := strings.Builder{}
		for _, message := range selectedMessages {
			if message.Role == "system" {
				system.WriteString(message.Content)
				system.WriteByte('\n')
			}
		}
		require.Contains(t, system.String(), reviewedAnswerDeliveryMarker)
		require.Contains(t, system.String(), string(reviewedAnswerSchema), "the trusted delivery schema is present in system context")
	}
	return selectedIndex, selectedMessages, selectedUser, selectedPayload
}

func TestSourceReviewGetsNumericFeedbackBeforeCompletenessChecksFinalCandidate(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{Content: "该范围总量是999，单位依原表。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{Content: "实际结果是30，单位依原表。", Done: true, FinishReason: "stop"}}},
		completenessVerdict(true),
	}}
	engine := newTestEngine(t, model)
	engine.config.SandboxConfigID = "administrator-selected-runtime"
	raw := `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Output: raw}}}}}}
	_, err := engine.executeLoop(context.Background(), state, "请说明实际查询结果", []chat.Message{{Role: "system", Content: "Use authorized results"}, {Role: "tool", Name: "structured_analyze_table", Content: raw}}, emptyTools(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, "实际结果是30，单位依原表。", state.FinalAnswer)
	require.Len(t, model.calls, 3, "review the source draft, then check completeness of the delivered candidate only")
	_, _, _, reviewInput := uniqueJSONUserInput(t, model.calls, "unverified_draft")
	var unmatched []string
	require.NoError(t, json.Unmarshal(reviewInput["runtime_unmatched_numbers"], &unmatched))
	require.Contains(t, unmatched, "999")
	_, _, completenessUser, completenessInput := uniqueJSONUserInput(t, model.calls, "candidate_answer")
	var candidate string
	require.NoError(t, json.Unmarshal(completenessInput["candidate_answer"], &candidate))
	require.Equal(t, "实际结果是30，单位依原表。", candidate)
	require.Contains(t, completenessUser.Content, candidate)
}

func TestFetchedDocumentAnswerGetsIndependentSourceReviewWithoutTableEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{Content: "草稿中的解释尚待核对。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{Content: "原报告记载30。<kb chunk_id=\"source-chunk\"/>", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	step := documentNumbersStep("read_document", true, "chunks", []map[string]any{{"chunk_id": "source-chunk", "knowledge_id": "authorized-doc", "content": "原报告记载30。"}})
	state := &types.AgentState{RoundSteps: []types.AgentStep{step}}
	messages := []chat.Message{{Role: "system", Content: "Use authorized sources"}, {Role: "tool", Name: "read_document", Content: `{"chunks":[{"chunk_id":"source-chunk","knowledge_id":"authorized-doc","content":"原报告记载30。"}]}`}}
	require.False(t, hasStructuredEvidence(state.RoundSteps))
	_, err := engine.executeLoop(context.Background(), state, "说明这份报告的内容", messages, emptyTools(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, "原报告记载30。", state.FinalAnswer)
	require.Len(t, model.calls, 2)
	_, _, user, payload := uniqueJSONUserInput(t, model.calls, "unverified_draft")
	require.Contains(t, user.Content, "source-chunk")
	require.NotEmpty(t, payload["unverified_draft"])
}

func TestDirectoryGroupCountsTriggerReviewButNotErrorOrArbitraryMetadata(t *testing.T) {
	require.True(t, structuredResultHasEvidence(`{"series":[],"catalog_summary":{"kind":"catalog_group_counts","group_count":2}}`))
	require.False(t, structuredResultHasEvidence(`{"series":[],"catalog_summary":{"group_count":2}}`))
	require.False(t, structuredResultHasEvidence(`{"status":"error","catalog_summary":{"kind":"catalog_group_counts","group_count":2}}`))
	require.True(t, structuredResultHasEvidence(`{"kind":"source_calculation","status":"ok","calculations":[{"value":"1.25"}]}`))
	require.False(t, structuredResultHasEvidence(`{"kind":"source_calculation","status":"error"}`))
}
