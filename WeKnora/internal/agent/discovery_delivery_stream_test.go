package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryReviewBuffersSourceAliasesAndQuotedTitlesAcrossStreamChunks(t *testing.T) {
	feedback := `Consider the permitted source "Quarter \"draft\".pdf" and <ref id="chunk://1" /> before deciding whether the original goal is unmet.`
	encoded, err := json.Marshal(map[string]string{"action": "recover", "feedback": feedback})
	require.NoError(t, err)
	for _, split := range []int{1, strings.Index(string(encoded), "chunk://1") + 7, strings.Index(string(encoded), "draft") + 2, len(encoded) - 1} {
		t.Run(string(rune('A'+split%26)), func(t *testing.T) {
			response := mockResponse{chunks: []types.StreamResponse{
				{ResponseType: types.ResponseTypeAnswer, Content: string(encoded[:split])},
				{ResponseType: types.ResponseTypeAnswer, Content: string(encoded[split:]), Done: true, FinishReason: "stop"},
			}}
			e, state, model := discoveryReviewFixture(t, response)
			e.modelContext = modelcontext.NewRegistry(true)
			handle := e.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-source", KnowledgeID: "authorized-document", KnowledgeBaseID: "authorized-library", DocumentTitle: `Quarter "draft".pdf`})
			require.Equal(t, "chunk://1", handle)
			original := candidateDecision{Disposition: candidateAccepted, Candidate: "The current representation has no result."}
			got := e.checkDiscoveryOnlyDelivery(t.Context(), "Explain the available source pattern.", original, state, nil)
			require.Len(t, model.calls, 1, "valid JSON must not spend the protocol retry")
			require.Equal(t, candidateNeedSource, got.Disposition)
			require.Equal(t, original.Candidate, got.Candidate)
			require.Equal(t, feedback, e.turnReview.DiscoveryReview.Feedback)
			require.False(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
			require.Empty(t, e.turnReview.DiscoveryReview.FailureCode)
			require.Contains(t, e.answerReviewFeedback, `<ref id="chunk://1" />`)
			messages := e.modelContext.EncodeMessages([]chat.Message{{Role: "system", Content: e.answerReviewFeedback}})
			require.Contains(t, messages[0].Content, "chunk://1")
			require.Equal(t, handle, e.modelContext.CitableChunkHandle("actual-source"))
			require.Empty(t, e.modelContext.CitableChunkHandle("unregistered-source"))
		})
	}
}

func TestDiscoveryReviewJSONBufferPreservesRawEnvelope(t *testing.T) {
	e, _, _ := discoveryReviewFixture(t)
	e.modelContext = modelcontext.NewRegistry(true)
	e.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-source", KnowledgeID: "actual-document", DocumentTitle: `Report "revision"`})
	// Unicode escaped quotes exercise the same JSON boundary as provider output.
	raw := `{"action":"recover","feedback":"Read <ref id=\u0022chunk://1\u0022 /> from \u0022Report \\\u0022revision\\\u0022\u0022."}`
	_, _, valid := decodeDiscoveryDeliveryReview(raw)
	require.True(t, valid)
	e.chatModel = &mockChat{responses: []mockResponse{chatAnswer(raw)}}
	result, err := e.streamLLMToEventBusWithModel(t.Context(), e.chatModel, nil, &chat.ChatOptions{Format: discoveryDeliveryReviewSchema}, nil)
	require.NoError(t, err)
	require.Equal(t, raw, result.Content)
}

func TestDiscoveryReviewJSONBufferKeepsProtocolArtifactGuard(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
		{ResponseType: types.ResponseTypeAnswer, Content: "authorized="},
		{ResponseType: types.ResponseTypeAnswer, Content: "true</arg_value:abcdef12>", Done: true, FinishReason: "stop"},
	}}}}
	e := newTestEngine(t, model)
	_, err := e.streamLLMToEventBusWithModel(t.Context(), model, nil, &chat.ChatOptions{Format: discoveryDeliveryReviewSchema}, nil)
	require.ErrorIs(t, err, agenttools.ErrProtocolArtifact)
}

func TestDiscoveryReviewSchemaFailureClassificationIsDeterministic(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"action":`, "json_invalid"},
		{`{"action":"keep","feedback":"","extra":true}`, "object_fields"},
		{`{"action":"keep","action":"recover","feedback":""}`, "object_fields"},
		{`[]`, "object_fields"},
		{`{"action":"keep"}`, "missing_or_null_fields"},
		{`{"action":null,"feedback":""}`, "missing_or_null_fields"},
		{`{"action":"keep","feedback":{}}`, "field_type"},
		{`{"action":"other","feedback":""}`, "action_enum"},
		{`{"action":"keep","feedback":"not empty"}`, "keep_feedback_nonempty"},
		{`{"action":"recover","feedback":" "}`, "recover_feedback_empty"},
		{`{"action":"recover","feedback":"` + strings.Repeat("文", 1201) + `"}`, "feedback_limit"},
	} {
		_, _, failure := decodeDiscoveryDeliveryReviewDetailed(tc.body)
		require.Equal(t, tc.code, failure)
	}
	e, state, model := discoveryReviewFixture(t, chatAnswer(`{"action":"recover","feedback":{}}`), discoveryReply("keep", ""))
	e.checkDiscoveryOnlyDelivery(t.Context(), "Report mapping status.", candidateDecision{Disposition: candidateAccepted, Candidate: "Actual status."}, state, nil)
	require.Len(t, model.calls, 2)
	require.Contains(t, model.calls[1][3].Content, "Program validation code: field_type")
	require.Equal(t, "field_type", e.turnReview.DiscoveryReview.InitialSchemaFailure)
	require.Empty(t, e.turnReview.DiscoveryReview.SchemaFailure)
}
