package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func candidateRevisionInput(t *testing.T, messages []chat.Message) map[string]any {
	t.Helper()
	require.Len(t, messages, 2)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &input))
	return input
}

func candidateRevisionDocumentCall(callID, chunkID, content string) types.ToolCall {
	return types.ToolCall{
		ID: callID, Name: "read_document", Args: map[string]interface{}{"knowledge_id": "document"},
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			"knowledge_id": "document",
			"document":     map[string]interface{}{"knowledge_id": "document", "title": "Observed document"},
			"chunks": []map[string]interface{}{{
				"chunk_id": chunkID, "knowledge_id": "document", "content": content,
				"content_complete": true, "chunk_type": "text", "page": 1,
			}},
		}},
	}
}

func TestBeginCandidateRevisionUsesLatestDraftAndRestoresOriginalPlusLiveTail(t *testing.T) {
	const query = "Summarize the retrieved source."
	const firstDraft = "First unverified candidate."
	const latestDraft = "Second unverified candidate."
	firstCall := candidateRevisionDocumentCall("initial-read", "initial-chunk", "A generic source body.")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{firstCall}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	engine.taskPlan = &taskEvidencePlan{Goals: []string{"Summarize the source."}, CompletionCriteria: []string{"Answer the requested summary."}}
	messages := []chat.Message{
		{Role: "system", Content: "Use real current-turn evidence."},
		{Role: "user", Content: query},
		{Role: "assistant", Content: "Earlier delivered context."},
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "initial-read", Function: chat.FunctionCall{Name: "read_document"}}}},
		{Role: "tool", Name: "read_document", ToolCallID: "initial-read", Content: `{"success":true,"data":{"chunk_id":"initial-chunk"}}`},
	}
	guards := &loopGuards{}

	engine.beginCandidateRevision(t.Context(), state, &messages, guards, query, firstDraft)
	firstEnvelope := messages[1].Content
	require.Contains(t, candidateRevisionInput(t, messages), "advisory_task_plan")
	require.Equal(t, firstEnvelope, guards.structuredReviewInput)
	require.Equal(t, firstDraft, candidateRevisionInput(t, messages)["unverified_draft"])

	tailCall := candidateRevisionDocumentCall("tail-read", "tail-chunk", "A newly fetched generic source body.")
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{tailCall}})
	tailAssistant := chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "tail-read", Function: chat.FunctionCall{Name: "read_document"}}}}
	tailTool := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "tail-read", Content: `{"success":true,"data":{"chunk_id":"tail-chunk"}}`}
	messages = append(messages, tailAssistant, tailTool)

	engine.beginCandidateRevision(t.Context(), state, &messages, guards, query, latestDraft)
	input := candidateRevisionInput(t, messages)
	require.Contains(t, input, "advisory_task_plan")
	require.Equal(t, messages[1].Content, guards.structuredReviewInput)
	require.Equal(t, latestDraft, input["unverified_draft"], "the second review must audit the latest candidate")
	require.NotContains(t, messages[1].Content, firstEnvelope, "the prior synthetic reviewer envelope must not nest in the next one")
	require.NotContains(t, messages[1].Content, firstDraft, "the prior draft must not leak into the rebuilt review payload")
	require.Equal(t, tailAssistant, guards.structuredReviewEvidence[len(guards.structuredReviewEvidence)-2])
	require.Equal(t, tailTool, guards.structuredReviewEvidence[len(guards.structuredReviewEvidence)-1])
	joined, err := json.Marshal(guards.structuredReviewEvidence)
	require.NoError(t, err)
	require.Contains(t, string(joined), query, "the original human request remains recoverable")
	require.NotContains(t, string(joined), firstEnvelope, "saved restoration state contains the real transcript, not a reviewer envelope")
	require.Contains(t, string(joined), "tail-chunk", "the real tool tail remains recoverable")
	require.Contains(t, input["current_user_request"], query)
}

func TestBeginCandidateRevisionKeepsLargeCitedBodyWhenGenericViewIsTruncated(t *testing.T) {
	const query = "Summarize the cited source."
	const chunkID = "generic-chunk"
	body := strings.Repeat("source evidence ", 1400)
	call := candidateRevisionDocumentCall("large-read", chunkID, body)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	candidate := "The retrieved source states this. <kb chunk_id=\"" + chunkID + "\">source</kb>"
	messages := []chat.Message{{Role: "system", Content: "Use actual source evidence."}, {Role: "user", Content: query}}
	guards := &loopGuards{}

	engine.beginCandidateRevision(t.Context(), state, &messages, guards, query, candidate)
	input := candidateRevisionInput(t, messages)
	generic, ok := input["source_tool_results"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, generic)
	require.Equal(t, false, input["evidence_scope"].(map[string]any)["view_complete"], "the bounded generic projection should disclose its truncation")
	citationView, ok := input["citation_source_evidence"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, citationView["view_complete"], "the citation-focused projection should retain the full cited body")
	items, ok := citationView["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	require.Equal(t, chunkID, item["chunk_id"])
	require.Equal(t, body, item["content"])
	require.Equal(t, true, item["source_content_complete"])
}

func TestBeginCandidateRevisionAfterCompactionKeepsSavedTranscript(t *testing.T) {
	engine := sourceConsistencyEngine(t, &mockChat{})
	state := sourceConsistencyState()
	messages := []chat.Message{{Role: "system", Content: "Trusted base."}, {Role: "user", Content: "Summarize actual evidence."}}
	guards := &loopGuards{}
	engine.beginCandidateRevision(t.Context(), state, &messages, guards, "Summarize actual evidence.", "First draft.")
	saved := append([]chat.Message(nil), guards.structuredReviewEvidence...)
	envelope := guards.structuredReviewInput
	messages = []chat.Message{messages[0], {Role: "assistant", Content: "Compacted conversation summary."}}
	engine.beginCandidateRevision(t.Context(), state, &messages, guards, "Summarize actual evidence.", "Latest rejected draft.")
	require.Equal(t, saved, guards.structuredReviewEvidence)
	require.Equal(t, envelope, guards.structuredReviewInput)
	require.Len(t, messages, 3)
	require.Equal(t, chat.Message{Role: "assistant", Content: "Latest rejected draft."}, messages[2])
	require.Equal(t, 1, strings.Count(messages[0].Content, "你现在在独立复核上下文中"))
}
