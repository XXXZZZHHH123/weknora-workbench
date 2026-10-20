package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func reviewSearchWiringCall(t *testing.T, callID, evidenceID, query, pointer string) types.LLMToolCall {
	t.Helper()
	args := map[string]any{"operation": "search", "query": query}
	if evidenceID != "" {
		args["evidence_id"] = evidenceID
	}
	if pointer != "" {
		args["json_pointer"] = pointer
	}
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	return types.LLMToolCall{ID: callID, Type: "function", Function: types.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(encoded)}}
}

func reviewSearchWiringResponse(call *types.LLMToolCall, content string, promptTokens, completionTokens int) mockResponse {
	chunk := types.StreamResponse{
		ResponseType: types.ResponseTypeAnswer,
		Content:      content,
		Usage:        &types.TokenUsage{PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens},
		Done:         true,
		FinishReason: "stop",
	}
	if call != nil {
		chunk.ToolCalls = []types.LLMToolCall{*call}
		chunk.FinishReason = "tool_calls"
	}
	return mockResponse{chunks: []types.StreamResponse{chunk}}
}

func reviewSearchWiringAcceptedResponse(promptTokens, completionTokens int) mockResponse {
	response := sourceAcceptedVerdict()
	response.sourceReviewUsage = &types.TokenUsage{
		PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens,
	}
	return response
}

func reviewSearchWiringState(body string) *types.AgentState {
	data := map[string]any{
		"query":     "watershed sampling record",
		"truncated": false,
		"chunks": []any{map[string]any{
			"chunk_id":         "watershed-observation",
			"knowledge_id":     "watershed-report",
			"title":            "Watershed sampling report",
			"content":          body,
			"content_offset":   0,
			"content_complete": true,
		}},
	}
	return &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID:     "watershed-read-call",
		Name:   "read_document",
		Args:   map[string]any{"id": "watershed-report", "query": "watershed sampling record"},
		Target: &types.ToolCallTarget{Name: "read_document", Args: map[string]any{"id": "watershed-report", "query": "watershed sampling record"}},
		Result: &types.ToolResult{Success: true, Data: data},
	}}}}}
}

func reviewSearchWiringToolMessage(t *testing.T, messages []chat.Message) string {
	t.Helper()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" && messages[i].Name == reviewEvidenceToolName {
			return messages[i].Content
		}
	}
	require.FailNow(t, "review evidence tool result must be forwarded to the next model call")
	return ""
}

func TestSourceCriticCanSearchOmittedDocumentEvidenceAfterProvisionalCollectVerdict(t *testing.T) {
	const query = "下游水位读数"
	prefix := strings.Repeat("上游水文采样记录，", 4200) + "🌧️记录点："
	body := prefix + "下游水位读数为47.25厘米，采样完整。"
	state := reviewSearchWiringState(body)
	model := &mockChat{responses: []mockResponse{
		reviewSearchWiringResponse(nil, `{"complete":false,"feedback":"还需核对已读取的原始记录。","next_action":"collect_evidence"}`, 11, 3),
		reviewSearchWiringResponse(func() *types.LLMToolCall {
			// An unscoped search has no evidence ID yet, so it must not claim a
			// JSON pointer. The returned match supplies the canonical ID/pointer
			// pair for the follow-up exact read.
			call := reviewSearchWiringCall(t, "search-water-result", "", query, "")
			return &call
		}(), "", 7, 2),
		reviewSearchWiringAcceptedResponse(5, 4),
	}}
	engine := sourceConsistencyEngine(t, model)
	question := "What downstream measurement does the watershed record report?"
	candidate := "The report includes a downstream measurement."
	repair, err := engine.evaluateAnswerCompletenessOnce(context.Background(), question, candidate, state, 0,
		[]chat.Message{{Role: "user", Content: question}})
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 3, "one provisional collect verdict, one search action, then the final verdict")
	require.Equal(t, 23, state.TurnUsage.PromptTokens)
	require.Equal(t, 9, state.TurnUsage.CompletionTokens)
	require.Equal(t, 32, state.TurnUsage.TotalTokens, "usage from the provisional verdict, search round and final verdict is aggregated")

	searchSchema := model.opts[0].Tools
	require.Len(t, searchSchema, 1, "review access exposes only its bounded local reader")
	require.Equal(t, reviewEvidenceToolName, searchSchema[0].Function.Name)
	require.Equal(t, "auto", model.opts[0].ToolChoice)
	toolDefinition, err := json.Marshal(searchSchema[0])
	require.NoError(t, err)
	require.Contains(t, string(toolDefinition), `"search"`)
	require.NotContains(t, string(toolDefinition), `"read_document"`)
	require.NotContains(t, string(toolDefinition), `"search_knowledge"`, "critic search must not grant generator source-retrieval capabilities")
	initialInput := documentWiringReviewInput(t, model.calls[0])
	require.Contains(t, initialInput, "review_evidence_access")
	require.Equal(t, false, initialInput["document_retrieval_companion"].(map[string]any)["projection_complete"], "the fixture should trigger review of a genuinely omitted long document body")
	require.Contains(t, model.calls[1][len(model.calls[1])-1].Content, "Your verdict requests additional evidence")
	require.Contains(t, model.calls[1][len(model.calls[1])-1].Content, "literal you choose")

	sourceMessages := engine.sourceConsistencyMessages(state, []chat.Message{{Role: "user", Content: question}})
	reader := newReviewEvidenceReader(sourceMessages)
	payloadID := reviewEvidencePayloadID(reader)
	require.NotEmpty(t, payloadID)
	var searchOutput map[string]any
	require.NoError(t, json.Unmarshal([]byte(reviewSearchWiringToolMessage(t, model.calls[2])), &searchOutput))
	require.Equal(t, "ok", searchOutput["status"])
	require.Equal(t, true, searchOutput["scan_complete"])
	require.Greater(t, searchOutput["checked_runes"].(float64), float64(16000))
	require.LessOrEqual(t, searchOutput["checked_runes"].(float64), float64(reviewEvidenceSearchRuneBudget))
	matches, ok := searchOutput["matches"].([]any)
	require.True(t, ok)
	require.Len(t, matches, 1)
	match := matches[0].(map[string]any)
	require.Equal(t, payloadID, match["evidence_id"])
	require.Equal(t, "read_document", match["tool"])
	require.Equal(t, "watershed-read-call", match["tool_call_id"])
	require.Equal(t, "/data/chunks/0/content", match["json_pointer"])
	require.Equal(t, float64(utf8.RuneCountInString(prefix)), match["match_offset"], "offset counts Unicode characters in the original source string")
	require.Equal(t, float64(utf8.RuneCountInString(query)), match["match_length"])
	contextText := match["context_text"].(string)
	contextOffset := int(match["content_offset"].(float64))
	queryInContext := strings.Index(contextText, query)
	require.GreaterOrEqual(t, queryInContext, 0)
	require.Equal(t, 0, utf8.RuneCountInString(contextText[:queryInContext])+contextOffset-int(match["match_offset"].(float64)))
	require.Contains(t, contextText, query)

	readArgs, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": match["evidence_id"], "json_pointer": match["json_pointer"],
		"offset": int(match["match_offset"].(float64)), "max_chars": int(match["match_length"].(float64)),
	})
	require.NoError(t, err)
	readResult := reader.read(string(readArgs))
	require.Equal(t, "ok", readResult["status"])
	require.Equal(t, query, readResult["text"], "the search result's real handle, pointer and Unicode offset must resolve to the exact source text")
	require.Equal(t, match["json_pointer"], readResult["json_pointer"])
	require.Equal(t, int(match["match_offset"].(float64)), readResult["content_offset"])
	require.LessOrEqual(t, reader.reads, reviewEvidenceReadLimit)
	require.GreaterOrEqual(t, reader.remaining, 0)
}

func TestReviewEvidenceSearchBudgetAndInvalidActionsStayBounded(t *testing.T) {
	body := strings.Repeat("上游水文日志，", 2100) + "🌊下游水位报告为47.25厘米。"
	state := reviewSearchWiringState(body)
	e := sourceConsistencyEngine(t, &mockChat{})
	reader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	payloadID := reviewEvidencePayloadID(reader)
	require.NotEmpty(t, payloadID)
	searchArgs, err := json.Marshal(map[string]any{
		"operation": "search", "evidence_id": payloadID, "json_pointer": "/data/chunks/0/content", "query": "下游水位报告",
	})
	require.NoError(t, err)
	search := reader.read(string(searchArgs))
	require.Equal(t, "ok", search["status"])
	require.Equal(t, true, search["scan_complete"])
	encodedSearch, err := json.Marshal(search)
	require.NoError(t, err)
	expectedRemaining := reviewEvidenceRuneBudget - utf8.RuneCount(encodedSearch)
	require.Equal(t, expectedRemaining, reader.remaining, "search response bytes consume the same aggregate review-output budget as exact reads")

	matches := reviewSearchMatches(t, search)
	require.Len(t, matches, 1)
	match := matches[0]
	readArgs, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": match.EvidenceID, "json_pointer": match.JSONPointer,
		"offset": match.MatchOffset, "max_chars": match.MatchLength,
	})
	require.NoError(t, err)
	read := reader.read(string(readArgs))
	require.Equal(t, "下游水位报告", read["text"])
	require.Equal(t, reviewEvidenceReadLimit, 4, "the bounded protocol keeps the four-action ceiling")
	require.Equal(t, 2, reader.reads, "search and exact pointer read share the same action budget")
	require.Equal(t, expectedRemaining-match.MatchLength, reader.remaining, "search and read debit one shared 16K Unicode response budget")

	unknownReader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	unknownArgs, err := json.Marshal(map[string]any{
		"operation": "search", "evidence_id": "forged-handle", "query": "下游水位报告",
	})
	require.NoError(t, err)
	unknown := unknownReader.read(string(unknownArgs))
	require.Equal(t, "unknown_evidence_id", unknown["status"])
	require.NotContains(t, fmt.Sprint(unknown), "47.25", "a forged handle cannot disclose source text")

	nonSearchReader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	nonSearchOperation, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": payloadID, "json_pointer": "/data/chunks/0/content", "query": "下游水位报告",
	})
	require.NoError(t, err)
	require.Equal(t, "invalid_arguments", nonSearchReader.read(string(nonSearchOperation))["status"], "literal queries are accepted only for the search operation")

	invalidOffsetReader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	invalidOffset, err := json.Marshal(map[string]any{
		"operation": "search", "evidence_id": payloadID, "json_pointer": "/data/chunks/0/content", "query": "下游水位报告", "offset": 1,
	})
	require.NoError(t, err)
	require.Equal(t, "invalid_arguments", invalidOffsetReader.read(string(invalidOffset))["status"], "search cannot be used to bypass exact read pagination")

	limitReader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	for i := 0; i < reviewEvidenceReadLimit; i++ {
		require.Equal(t, "ok", limitReader.read(string(searchArgs))["status"])
	}
	require.Equal(t, 4, limitReader.reads)
	require.Equal(t, "budget_exhausted", limitReader.read(string(searchArgs))["status"], "the fifth evidence action is rejected even when output characters remain")
}

func TestReviewEvidenceCollectReminderIsSingleAndRequiresStrictCollectVerdict(t *testing.T) {
	cases := []struct {
		name      string
		responses []mockResponse
		reader    bool
		wantCalls int
		reminders int
	}{
		{"accept", []mockResponse{reviewSearchWiringResponse(nil, `{"complete":true}`, 1, 1)}, true, 1, 0},
		{"no_reader", []mockResponse{reviewSearchWiringResponse(nil, `{"complete":false,"next_action":"collect_evidence"}`, 1, 1)}, false, 1, 0},
		{"revise_verifies_once", []mockResponse{
			reviewSearchWiringResponse(nil, `{"complete":false,"next_action":"revise_answer"}`, 1, 1),
			reviewSearchWiringResponse(nil, `{"complete":false,"next_action":"revise_answer"}`, 1, 1),
		}, true, 2, 1},
		{"invalid_verdict", []mockResponse{reviewSearchWiringResponse(nil, `{"complete":false,"next_action":"collect_evidence","extra":true}`, 1, 1)}, true, 1, 0},
		{"one_collect_reminder_only", []mockResponse{
			reviewSearchWiringResponse(nil, `{"complete":false,"next_action":"collect_evidence"}`, 1, 1),
			reviewSearchWiringResponse(nil, `{"complete":false,"next_action":"collect_evidence"}`, 1, 1),
		}, true, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChat{responses: tc.responses}
			e := sourceConsistencyEngine(t, model)
			var reader *reviewEvidenceReader
			if tc.reader {
				reader = newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "water-source", map[string]any{"content": "watershed source"})})
			}
			result, err := e.streamReviewWithEvidence(context.Background(), model, []chat.Message{{Role: "system", Content: "review"}},
				&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, nil, reader)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, model.calls, tc.wantCalls)
			reminders := 0
			for _, call := range model.calls {
				for _, message := range call {
					if strings.Contains(message.Content, "Your verdict requests additional evidence") || strings.Contains(message.Content, "Your verdict rejects the candidate") {
						reminders++
					}
				}
			}
			require.Equal(t, tc.reminders, reminders)
		})
	}
}
