package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func canonicalDocumentWiringCall(name, id string, modelArgs, executionArgs map[string]any, data map[string]any) types.ToolCall {
	return types.ToolCall{
		ID: id, Name: name, Args: modelArgs,
		Target: &types.ToolCallTarget{Name: name, Args: executionArgs},
		Result: &types.ToolResult{Success: true, Data: data},
	}
}

func documentWiringRow(chunkID, documentID, title, content string, extra map[string]any) map[string]any {
	row := map[string]any{
		"chunk_id": chunkID, "knowledge_id": documentID, "knowledge_base_id": "watershed-kb",
		"title": title, "content": content, "content_offset": 0, "content_complete": true,
	}
	for key, value := range extra {
		row[key] = value
	}
	return row
}

func documentWiringReviewInput(t *testing.T, messages []chat.Message) map[string]any {
	t.Helper()
	require.NotEmpty(t, messages)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(messages[len(messages)-1].Content), &input))
	return input
}

func TestCanonicalDocumentReviewScopeUsesResolvedExecutionArgsAndSamePayloadHandle(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
	}{
		{
			name: tools.ToolSearchKnowledge,
			data: map[string]any{
				"results": []any{documentWiringRow("river-search", "survey-north", "North watershed survey", "North station recorded stable flow.", nil)},
				"query":   "renderer alias", "mode": "keyword", "truncated": false,
			},
		},
		{
			name: tools.ToolReadDocument,
			data: map[string]any{
				"chunks": []any{documentWiringRow("river-page", "survey-south", "South watershed survey", "South station recorded rainfall.", map[string]any{"physical_page": 8})},
				"query":  "renderer alias", "query_scope": map[string]any{"physical_page": 8, "page_complete": true}, "truncated": false,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelArgs := map[string]any{"query": "model alias", "mode": "alias-mode"}
			executionArgs := map[string]any{"query": "actual executed watershed query", "mode": "literal", "knowledge_base_ids": []any{"watershed-kb"}}
			call := canonicalDocumentWiringCall(tc.name, "actual-"+tc.name, modelArgs, executionArgs, tc.data)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			engine := sourceConsistencyEngine(t, &mockChat{})

			criticMessages := engine.sourceConsistencyMessages(state, nil)
			require.Len(t, criticMessages, 2, "the canonical source result follows its runtime-access summary")
			var criticPayload map[string]any
			require.NoError(t, json.Unmarshal([]byte(criticMessages[1].Content), &criticPayload))
			require.Equal(t, executionArgs, criticPayload["execution_scope"], "scope must come from resolved execution arguments rather than model aliases")
			require.NotEqual(t, modelArgs, criticPayload["execution_scope"])

			stepMessages := sourceResultMessagesFromSteps(state.RoundSteps)
			require.Len(t, stepMessages, 1)
			require.Equal(t, criticMessages[1].Content, stepMessages[0].Content, "critic and step projection must serialize the same canonical source payload")
			require.Equal(t, reviewEvidenceID(criticMessages[1]), reviewEvidenceID(stepMessages[0]), "both projections must address the same immutable payload")

			companion := documentRetrievalCompanion(criticMessages, documentRetrievalCompanionBudget)
			require.NotNil(t, companion)
			scopes := companion["scopes"].([]any)
			require.Len(t, scopes, 1)
			metadata := scopes[0].(map[string]any)["metadata"].(map[string]any)
			require.Equal(t, executionArgs, metadata["execution_scope"], "document companion must carry the same resolved source scope")
		})
	}
}

func TestCriticAndIndependentRewriteRetainUncitedDocumentAndHumanConversation(t *testing.T) {
	const citedBody = "North gauge survey: flow remained stable after rainfall."
	const uncitedBody = "South gauge field note: the downstream sensor recorded a delayed rise."
	search := canonicalDocumentWiringCall(tools.ToolSearchKnowledge, "water-search", map[string]any{"query": "alias"},
		map[string]any{"query": "two watershed sites", "mode": "keyword", "knowledge_base_ids": []any{"watershed-kb"}},
		map[string]any{"query": "two watershed sites", "mode": "keyword", "knowledge_base_ids": []any{"watershed-kb"}, "truncated": false,
			"results": []any{documentWiringRow("north-chunk", "survey-north", "North watershed survey", citedBody, nil)}})
	read := canonicalDocumentWiringCall(tools.ToolReadDocument, "water-read", map[string]any{"id": "alias-document"},
		map[string]any{"id": "survey-south", "query": "downstream sensor", "physical_page": 4},
		map[string]any{"document": map[string]any{"knowledge_id": "survey-south", "title": "South watershed survey"}, "query": "downstream sensor",
			"query_scope": map[string]any{"physical_page": 4, "page_complete": true}, "truncated": false,
			"chunks": []any{documentWiringRow("south-chunk", "survey-south", "South watershed survey", uncitedBody, map[string]any{"physical_page": 4})}})
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{search, read}}}}
	question := "Compare the upstream survey with the downstream site note."
	conversation := []chat.Message{
		{Role: "user", Content: "Earlier context: the study concerns two watershed gauges."},
		{Role: "assistant", Content: "A prior assistant speculated that a storm caused the rise; treat that as unverified."},
		{Role: "user", Content: question},
		{Role: "assistant", Content: "CURRENT_INTERNAL_PLAN must not be treated as a source."},
	}
	draft := `The north gauge stayed stable <kb doc="North watershed survey" chunk_id="north-chunk"/>. Draft-only conclusion: a storm caused the downstream rise.`

	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	_, err := engine.evaluateAnswerCompletenessOnce(t.Context(), question, draft, state, 0, conversation)
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	criticInput := documentWiringReviewInput(t, model.calls[0])
	criticCompanion := criticInput["document_retrieval_companion"].(map[string]any)
	criticBodies := criticCompanion["bodies"].([]any)
	require.Len(t, criticBodies, 2, "the document companion retains every fetched body, not only the draft citation")
	var hasNorth, hasSouth bool
	for _, raw := range criticBodies {
		body := raw.(map[string]any)["content"].(string)
		hasNorth = hasNorth || body == citedBody
		hasSouth = hasSouth || body == uncitedBody
	}
	require.True(t, hasNorth)
	require.True(t, hasSouth, "the real but uncited downstream source must remain available to critic review")
	criticCitations := criticInput["citation_source_evidence"].(map[string]any)["items"].([]any)
	require.Len(t, criticCitations, 1, "citation-first evidence stays limited to the draft's actual citation")
	require.Equal(t, "north-chunk", criticCitations[0].(map[string]any)["chunk_id"])
	require.Contains(t, criticInput["conversation_notice"], "prior assistant claims are not source facts")
	require.NotContains(t, fmt.Sprint(criticInput["source_tool_results"]), "storm caused the rise")
	require.NotContains(t, fmt.Sprint(criticInput["document_retrieval_companion"]), "storm caused the rise")
	require.Contains(t, criticInput["candidate_answer"], "Draft-only conclusion")
	require.NotContains(t, fmt.Sprint(criticInput["source_tool_results"]), "Draft-only conclusion")
	require.NotContains(t, fmt.Sprint(criticInput["document_retrieval_companion"]), "Draft-only conclusion")
	require.NotContains(t, fmt.Sprint(criticInput["recent_conversation"]), "CURRENT_INTERNAL_PLAN")
	require.Contains(t, fmt.Sprint(criticInput["recent_conversation"]), "Earlier context")
	require.Contains(t, fmt.Sprint(criticInput["recent_conversation"]), question)

	sourceMessages := engine.sourceConsistencyMessages(state, conversationThroughLastHuman(conversation))
	documentCompanion := documentRetrievalCompanion(sourceMessages, documentRetrievalCompanionBudget)
	rewriteMessages := append([]chat.Message{{Role: "system", Content: "Independent source review."}}, sourceMessages...)
	rewrite := independentStructuredReviewWithSourceCompanions(
		rewriteMessages, conversationThroughLastHuman(conversation), nil, nil, documentCompanion, question, draft)
	rewriteInput := documentWiringReviewInput(t, rewrite)
	rewriteCompanion := rewriteInput["document_retrieval_companion"].(map[string]any)
	require.Len(t, rewriteCompanion["observations"].([]any), 2, "independent rewriting also keeps uncited source observations")
	require.Contains(t, rewriteInput["document_retrieval_companion_notice"], "No read_review_evidence reader is installed")
	require.NotContains(t, rewriteInput, "review_evidence_access", "provenance handles do not imply a JIT reader in rewrite context")
	require.Contains(t, fmt.Sprint(rewriteInput["recent_user_requests"]), "Earlier context")
	require.Contains(t, fmt.Sprint(rewriteInput["recent_user_requests"]), question)
	require.NotContains(t, fmt.Sprint(rewriteInput["source_tool_results"]), "CURRENT_INTERNAL_PLAN")
	require.Contains(t, rewriteInput["unverified_draft"], "Draft-only conclusion")
	require.NotContains(t, fmt.Sprint(rewriteInput["source_tool_results"]), "Draft-only conclusion")
	require.NotContains(t, fmt.Sprint(rewriteInput["document_retrieval_companion"]), "Draft-only conclusion")

	results := rewriteInput["source_tool_results"].([]any)
	knownHandles := map[string]bool{}
	for _, raw := range results {
		entry := raw.(map[string]any)
		knownHandles[entry["evidence_id"].(string)] = true
	}
	for _, raw := range rewriteCompanion["observations"].([]any) {
		observation := raw.(map[string]any)
		require.True(t, knownHandles[observation["evidence_id"].(string)], "every rewrite companion handle must resolve to the canonical source_tool_results entry")
	}
}

func TestCriticReadsDocumentCompanionOmissionWhenGenericSourceViewIsComplete(t *testing.T) {
	const tail = "Downstream gauge reached 47.25 centimeters."
	body := strings.Repeat("Watershed field observation retained for the full sampling window. ", 240) + tail
	data := map[string]any{
		"query": "field observations", "mode": "keyword", "truncated": false,
		"results": []any{documentWiringRow("long-water-chunk", "survey-long", "Long watershed report", body, nil)},
	}
	call := canonicalDocumentWiringCall(tools.ToolSearchKnowledge, "long-water-search", map[string]any{"query": "alias"},
		map[string]any{"query": "complete watershed sampling record", "mode": "literal", "knowledge_base_ids": []any{"watershed-kb"}}, data)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	question := "What maximum water level does the report state?"
	candidate := "The report records a maximum level in centimeters."
	conversation := []chat.Message{{Role: "user", Content: question}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	sourceMessages := engine.sourceConsistencyMessages(state, conversation)
	_, genericScope := boundedReviewEvidence(sourceMessages, 24000)
	require.True(t, genericScope.ViewComplete, "the generic source-result projection should fit in its larger budget")
	companion := documentRetrievalCompanion(sourceMessages, documentRetrievalCompanionBudget)
	require.NotNil(t, companion)
	require.Equal(t, false, companion["projection_complete"], "the independent document companion should disclose its body omission")
	observations := documentRetrievalObservationMaps(companion)
	require.Len(t, observations, 1)
	require.Equal(t, false, observations[0]["body_included"])

	canonicalPayload := sourceMessages[len(sourceMessages)-1]
	evidenceID := reviewEvidenceID(canonicalPayload)
	reader := newReviewEvidenceReader(sourceMessages)
	_, readerHasCanonicalPayload := reader.byID[evidenceID]
	require.True(t, readerHasCanonicalPayload, "the reader handle must be derived from the exact canonical source payload")
	model := &mockChat{responses: []mockResponse{
		reviewEvidenceToolResponse(reviewEvidenceToolCall(t, "read-omitted-water-body", evidenceID, "/data/results/0/content", len([]rune(body))-len([]rune(tail)), len([]rune(tail)))),
		sourceAcceptedVerdict(),
	}}
	engine.chatModel = model
	repair, err := engine.evaluateAnswerCompletenessOnce(t.Context(), question, candidate, state, 0, conversation)
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 2, "the critic should JIT-read the exact omitted content path before accepting its verdict")
	require.Len(t, model.opts[0].Tools, 1)
	require.Equal(t, reviewEvidenceToolName, model.opts[0].Tools[0].Function.Name)
	firstInput := documentWiringReviewInput(t, model.calls[0])
	require.Contains(t, firstInput, "review_evidence_access")
	require.Contains(t, model.calls[0][1].Content, `"projection_complete":false`)
	lastMessage := model.calls[1][len(model.calls[1])-1]
	require.Contains(t, lastMessage.Content, tail, "the reader must return text from the canonical successful source payload")
}
