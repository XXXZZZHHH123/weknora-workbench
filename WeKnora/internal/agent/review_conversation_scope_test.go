package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSemanticReviewUsesRealHumanRequestsAndPersistedSteerOnly(t *testing.T) {
	humanJSON := `{"request":"Compare the watershed survey results.","scope":"north basin"}`
	initial := []chat.Message{
		{Role: "system", Content: "Use authorized source evidence."},
		{Role: "user", Content: humanJSON},
		{Role: "assistant", Content: "CURRENT_ASSISTANT_PLAN"},
	}
	e := sourceConsistencyEngine(t, &mockChat{})
	e.reviewConversation = conversationThroughLastHuman(initial)
	e.reviewConversationReady = true
	live := append([]chat.Message(nil), initial...)
	live = append(live, chat.Message{Role: "tool", Name: "read_document", ToolCallID: "read-1", Content: `{"source_chunk_id":"watershed-source-1","content":"Observed coverage: 24 hectares."}`})
	e.SetSteerSink(&fakeSteerSink{queued: []map[string]any{steerEntry("persisted-scope", "Include the appendix table.")}})
	require.Equal(t, 1, e.drainSteerMessages(t.Context(), &types.AgentState{}, &live, "session", "message"))

	semantic := e.semanticReviewConversation(live)
	review := independentStructuredReviewWithConversation(live, semantic, humanJSON, "unverified draft")
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &input))
	rows := input["recent_conversation"].([]any)
	require.Len(t, rows, 2)
	humanRow := rows[0].(map[string]any)
	require.Equal(t, "user", humanRow["role"])
	require.Equal(t, humanJSON, humanRow["content"], "JSON-shaped user content remains the exact human request, not a runtime envelope")
	encodedRows, err := json.Marshal(rows)
	require.NoError(t, err)
	require.Contains(t, string(encodedRows), "Include the appendix table.", "persisted steer extends the same turn's real human context")
	require.NotContains(t, string(encodedRows), "CURRENT_ASSISTANT_PLAN", "assistant plans do not become user intent")
	require.Contains(t, review[1].Content, "watershed-source-1", "full runtime tool evidence remains available independently of the human-only context")
	require.Contains(t, review[1].Content, "Observed coverage: 24 hectares.")
}

func TestUnpersistedSteerDoesNotEnterSemanticReviewContext(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	e.reviewConversation = []chat.Message{{Role: "user", Content: "Current research request."}}
	e.reviewConversationReady = true
	e.SetSteerSink(&fakeSteerSink{queued: []map[string]any{steerEntry("not-persisted", "Do not include this correction.")}, failPersist: true})
	live := []chat.Message{{Role: "user", Content: "Current research request."}}
	require.Zero(t, e.drainSteerMessages(t.Context(), &types.AgentState{}, &live, "session", "message"))
	require.Len(t, live, 1)
	require.Len(t, e.semanticReviewConversation(live), 1)
	require.NotContains(t, e.semanticReviewConversation(live)[0].Content, "Do not include")
}

func TestFinalAnswerRestorationKeepsRealJSONAndRuntimeToolPairs(t *testing.T) {
	humanJSON := `{"question":"What does the survey report conclude?"}`
	original := []chat.Message{
		{Role: "system", Content: "Answer from authorized sources."},
		{Role: "user", Content: humanJSON},
		{Role: "assistant", Content: "Read the report."},
		{Role: "tool", Name: "read_document", ToolCallID: "read-original", Content: `{"source_chunk_id":"source-original","content":"Original report excerpt."}`},
	}
	review := independentStructuredReview(original, humanJSON, "an unverified draft")
	live := []chat.Message{
		review[0],
		review[1],
		{Role: "assistant", Content: "Read the appendix."},
		{Role: "tool", Name: "read_document", ToolCallID: "read-tail", Content: `{"source_chunk_id":"source-tail","content":"Appendix excerpt."}`},
		{Role: "user", Content: "Also include the limitations section."},
	}
	guards := &loopGuards{structuredReviewEvidence: original, structuredReviewInput: review[1].Content}
	restored := restoreForFinalAnswer(live, guards)
	require.Equal(t, original, restored[:len(original)])
	require.Equal(t, live[2:], restored[len(original):], "final-answer and max-iteration paths preserve live tool pairs and human steer")
	require.Equal(t, "user", restored[1].Role)
	require.Equal(t, humanJSON, restored[1].Content, "JSON-shaped human content remains unchanged while the exact runtime envelope is removed")
	encoded, err := json.Marshal(restored)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "source-tail")
	require.Contains(t, string(encoded), "limitations section")
	require.NotContains(t, string(encoded), review[1].Content)
	require.NotContains(t, string(encoded), review[0].Content)
}

func TestExecuteLoopClearsSemanticConversationAtTurnEnd(t *testing.T) {
	query := `{"task":"Summarize the field observations."}`
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer, Content: "The observations describe the surveyed area.", Done: true,
	}}}}}
	e := newTestEngine(t, model)
	messages := []chat.Message{{Role: "system", Content: "Answer clearly."}, {Role: "user", Content: query}}
	state := &types.AgentState{}
	_, err := e.executeLoop(t.Context(), state, query, messages, emptyTools(), "session", "message")
	require.NoError(t, err)
	require.False(t, e.reviewConversationReady, "the semantic transcript must not leak across turns")
	require.Empty(t, e.reviewConversation)
	fallback := []chat.Message{{Role: "user", Content: "A later direct reviewer call."}}
	got := e.semanticReviewConversation(fallback)
	require.Equal(t, fallback, got, "outside an active turn the compatibility fallback remains usable")
	require.NotSame(t, &fallback[0], &got[0])
	require.True(t, strings.Contains(got[0].Content, "later direct reviewer"))
}
