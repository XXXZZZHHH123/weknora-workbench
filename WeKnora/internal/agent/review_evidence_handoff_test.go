package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func appendHandoffRead(reader *reviewEvidenceReader, callID, evidenceID, pointer string) {
	args, _ := json.Marshal(map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": pointer})
	call := chat.ToolCall{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}
	reader.transcript = append(reader.transcript, chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{call}})
	response, _ := json.Marshal(reader.read(string(args)))
	reader.transcript = append(reader.transcript, chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(response)})
}

func appendHandoffSearch(reader *reviewEvidenceReader, callID, query, evidenceID string) {
	args, _ := json.Marshal(map[string]any{"operation": "search", "query": query, "evidence_id": evidenceID})
	call := chat.ToolCall{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}
	reader.transcript = append(reader.transcript, chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{call}})
	response, _ := json.Marshal(reader.read(string(args)))
	reader.transcript = append(reader.transcript, chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(response)})
}

func handoffSourceRecord(success bool) chat.Message {
	payload := map[string]any{
		"success": success,
		"data": map[string]any{
			"ranges": []any{map[string]any{"cells": []any{
				map[string]any{"cell": "B2824", "value": json.Number("54.96")},
				map[string]any{"cell": "F2824", "value": json.Number("58.32")},
			}}},
			"coverage": map[string]any{"complete": false}, "truncated": true,
		},
		"error": "secret failure detail",
	}
	encoded, _ := json.Marshal(payload)
	return chat.Message{Role: "tool", Name: "structured_inspect_source", ToolCallID: "source-inspect-1", Content: string(encoded)}
}

func TestReviewEvidenceHandoffCarriesOnlyPairedSuccessfulReadAndSearch(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{handoffSourceRecord(true), handoffSourceRecord(false), {Role: "tool", Name: "describe_tools", ToolCallID: "schema", Content: `{"success":true,"data":{"operation":"describe_tools"}}`}})
	successID := reviewEvidenceID(handoffSourceRecord(true))
	failureID := reviewEvidenceID(handoffSourceRecord(false))
	appendHandoffRead(reader, "read-1", successID, "/data/ranges/0/cells/0/value")
	appendHandoffSearch(reader, "search-1", "58.32", successID)
	// A failed source observation and an unpaired/hidden source record must not
	// be promoted merely because their IDs exist in the same reader.
	appendHandoffRead(reader, "read-failed", failureID, "/data/ranges/0/cells/0/value")
	reader.transcript = append(reader.transcript, chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "unpaired", Content: `{"status":"ok","evidence_id":"` + successID + `","json_pointer":"/data/ranges/0/cells/1/value","value":58.32}`})

	engine := &AgentEngine{}
	engine.captureReviewEvidence(reader)
	require.NotNil(t, engine.turnReview.ReadEvidence)
	require.Len(t, engine.turnReview.ReadEvidence.Items, 2)
	encoded, err := json.Marshal(engine.turnReview.ReadEvidence)
	require.NoError(t, err)
	packet := string(encoded)
	require.Contains(t, packet, `"exact_fragment":54.96`)
	require.Contains(t, packet, `"quote":"58.32"`)
	require.Contains(t, packet, successID)
	require.Contains(t, packet, `"complete":false`)
	require.Contains(t, packet, `"truncated":true`)
	require.NotContains(t, packet, failureID)
	require.NotContains(t, packet, "secret failure detail")
	require.NotContains(t, packet, "unpaired")
}

func TestReviewEvidenceHandoffRootReadStripsEnvelopeMetadata(t *testing.T) {
	message := handoffSourceRecord(true)
	reader := newReviewEvidenceReader([]chat.Message{message})
	appendHandoffRead(reader, "read-root", reviewEvidenceID(message), "")
	engine := &AgentEngine{}
	engine.captureReviewEvidence(reader)
	require.NotNil(t, engine.turnReview.ReadEvidence)
	require.Len(t, engine.turnReview.ReadEvidence.Items, 1)
	item := engine.turnReview.ReadEvidence.Items[0]
	require.Equal(t, "/data", item.JSONPointer)
	require.True(t, item.FragmentComplete)
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret failure detail")
	require.NotContains(t, string(encoded), `"success"`)
	require.Contains(t, string(encoded), "54.96")
}

func TestReviewEvidenceHandoffReachesCollectionAndRevisionInputsThenClears(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{handoffSourceRecord(true)})
	appendHandoffRead(reader, "read-1", reviewEvidenceID(handoffSourceRecord(true)), "/data/ranges/0/cells/0/value")
	engine := sourceConsistencyEngine(t, &mockChat{})
	engine.captureReviewEvidence(reader)
	state := &types.AgentState{CurrentRound: 1}
	guards := &loopGuards{}
	messages := []chat.Message{{Role: "system", Content: "Trusted system."}, {Role: "user", Content: "Return the observed value."}}
	reviewEnvelope := independentStructuredReview(messages, messages[1].Content, "Draft")
	guards.structuredReviewEvidence = append([]chat.Message(nil), messages...)
	guards.structuredReviewInput = reviewEnvelope[1].Content
	messages = reviewEnvelope
	engine.answerReviewFeedback = "Use the previously read source evidence."
	engine.resumeEvidenceCollection(t.Context(), state, &messages, guards, "session")
	require.Equal(t, "user", messages[len(messages)-1].Role)
	require.Contains(t, messages[len(messages)-1].Content, `"exact_fragment":54.96`)
	require.Contains(t, messages[len(messages)-1].Content, `"json_pointer":"/data/ranges/0/cells/0/value"`)
	require.Nil(t, engine.turnReview.ReadEvidence, "handoff is scoped to one candidate check")

	// A new current candidate check clears the previous handoff before it can
	// leak into a later repair cycle.
	engine.turnReview.ReadEvidence = &reviewEvidenceHandoff{Items: []reviewEvidenceHandoffItem{{Quote: "stale"}}}
	engine.checkCandidate(t.Context(), "Question", "", "", &types.AgentState{}, nil, false)
	require.Nil(t, engine.turnReview.ReadEvidence)
}

func TestReviewEvidenceHandoffIsAttachedToActualCandidateRevisionInput(t *testing.T) {
	engine := sourceConsistencyEngine(t, &mockChat{})
	call := candidateRevisionDocumentCall("source-inspect-1", "observed-chunk", "The observed source reports 58.32.")
	call.Name = "read_document"
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	reader := newReviewEvidenceReader(engine.sourceConsistencyMessages(state, nil))
	var evidenceID string
	for _, record := range reader.records {
		value, err := reviewPointerValue(record.Value, "/data/chunks/0/content")
		if err == nil && value == "The observed source reports 58.32." {
			evidenceID = record.ID
			break
		}
	}
	require.NotEmpty(t, evidenceID)
	appendHandoffRead(reader, "read-1", evidenceID, "/data/chunks/0/content")
	engine.captureReviewEvidence(reader)
	query := "Return the observed source value."
	messages := []chat.Message{{Role: "system", Content: "Use authorized evidence."}, {Role: "user", Content: query}}
	engine.beginCandidateRevision(t.Context(), state, &messages, &loopGuards{}, query, "The source reports 58.32.")
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &input))
	handoff, ok := input["previously_read_evidence_handoff"].(map[string]any)
	require.True(t, ok)
	handoffJSON, _ := json.Marshal(handoff)
	require.Contains(t, string(handoffJSON), "58.32")
	require.Contains(t, string(handoffJSON), "/data/chunks/0/content")
	require.Contains(t, string(handoffJSON), "untrusted source content")
	require.True(t, strings.Contains(messages[1].Content, "previously_read_evidence_handoff"))
	require.Nil(t, engine.turnReview.ReadEvidence)
}

func TestReviewEvidenceHandoffCannotBecomeHumanParametersOrIntent(t *testing.T) {
	packet, ok := reviewEvidenceHandoffMessage(&reviewEvidenceHandoff{Items: []reviewEvidenceHandoffItem{{
		ExactFragment: "observed text", OriginalBytes: 987654321,
	}}})
	require.True(t, ok)
	messages := []chat.Message{{Role: "user", Content: "Summarize the observed source."}, packet}
	require.Len(t, conversationThroughLastHuman(messages), 1)
	view, _ := reviewConversationView(messages, 8000, 8)
	require.Len(t, view, 1)
	require.Equal(t, messages[0].Content, view[0]["content"])
	// Runtime framing numbers must not become user-provided computation inputs.
	evidence := append(messages, chat.Message{Role: "tool", Content: `{"success":true,"data":{"value":42}}`})
	require.Contains(t, ungroundedStructuredNumbers("The computed result is 987654321.", evidence), "987654321")
}

func TestReviewEvidenceHandoffFitsLargeRevisionWithoutExpandingReadBudget(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{handoffSourceRecord(true)})
	appendHandoffRead(reader, "read", reviewEvidenceID(handoffSourceRecord(true)), "/data/ranges")
	engine := &AgentEngine{}
	engine.captureReviewEvidence(reader)
	beforeReads, beforeRemaining := reader.reads, reader.remaining
	input, _ := json.Marshal(map[string]any{"candidate_answer": strings.Repeat("x", 20000)})
	messages := []chat.Message{{Role: "system", Content: "Review"}, {Role: "user", Content: string(input)}}
	require.True(t, attachReviewEvidenceHandoff(messages, engine.turnReview.ReadEvidence))
	require.Contains(t, messages[1].Content, "previously_read_evidence_handoff")
	require.Equal(t, beforeReads, reader.reads)
	require.Equal(t, beforeRemaining, reader.remaining)
	engine.captureReviewEvidence(nil)
	require.Nil(t, engine.turnReview.ReadEvidence)
}
