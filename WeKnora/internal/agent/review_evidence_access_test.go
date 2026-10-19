package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func reviewEvidenceToolCall(t *testing.T, id, evidenceID, pointer string, offset, maxChars int) types.LLMToolCall {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": evidenceID, "json_pointer": pointer,
		"offset": offset, "max_chars": maxChars,
	})
	require.NoError(t, err)
	return types.LLMToolCall{
		ID: id, Type: "function",
		Function: types.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(arguments)},
	}
}

func reviewEvidenceToolResponse(call types.LLMToolCall) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		ToolCalls:    []types.LLMToolCall{call},
		Done:         true,
		FinishReason: "tool_calls",
	}}}
}

func TestSourceCriticCanReadOmittedAuthorizedEvidenceThroughBoundedTool(t *testing.T) {
	const tail = "TAIL"
	body := strings.Repeat("实", 30000) + tail
	state := sourceConsistencyState()
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	chunks := data["chunks"].([]map[string]any)
	chunks[0]["content"] = body
	data["truncated"] = true
	data["coverage"] = map[string]any{"complete": false}
	state.RoundSteps[0].ToolCalls[0].Result.Output = ""
	e := sourceConsistencyEngine(t, &mockChat{})
	initialEvidence, initialScope := e.sourceConsistencyEvidence(state, nil)
	require.False(t, initialScope.ViewComplete)
	initialJSON, err := json.Marshal(initialEvidence)
	require.NoError(t, err)
	require.NotContains(t, string(initialJSON), tail, "the fixture must place the needed value outside the bounded view")
	reader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	payloadID := reviewEvidencePayloadID(reader)
	require.NotEmpty(t, payloadID, "critic access handles must reference a real source result entry")

	model := &mockChat{responses: []mockResponse{
		reviewEvidenceToolResponse(reviewEvidenceToolCall(t, "review-read-1", payloadID, "/data/chunks/0/content", len([]rune(body))-len([]rune(tail)), len([]rune(tail)))),
		sourceAcceptedVerdict(),
	}}
	e.chatModel = model
	question := "Summarize the actual survey finding."
	candidate := "The survey reports the requested observation."
	repair, err := e.evaluateAnswerCompletenessOnce(t.Context(), question, candidate, state, 0,
		[]chat.Message{{Role: "user", Content: question}})
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 2, "the critic must make its own bounded evidence read before a verdict")
	require.Len(t, model.opts[0].Tools, 1)
	require.Equal(t, reviewEvidenceToolName, model.opts[0].Tools[0].Function.Name)
	require.Equal(t, "auto", model.opts[0].ToolChoice)
	require.Contains(t, model.calls[0][1].Content, "review_evidence_access")
	require.Contains(t, model.calls[0][1].Content, `"truncated":true`)
	require.Contains(t, model.calls[0][1].Content, `"complete":false`)
	require.Contains(t, model.calls[1][len(model.calls[1])-1].Content, tail)
	require.Len(t, model.opts[1].Tools, 1)
	require.Equal(t, reviewEvidenceToolName, model.opts[1].Tools[0].Function.Name, "the critic never sees source retrieval tools")
}

func TestReviewEvidenceReaderValidatesHandlesPointersAndPreservesLargeNumbers(t *testing.T) {
	const large = "90071992547409931234567890123456789"
	content := `{"a/b~c":[{"value":` + large + `}],"truncated":true,"scan":{"complete":false}}`
	newReader := func() *reviewEvidenceReader {
		return newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "read_document", ToolCallID: "source-call", Content: content}})
	}
	reader := newReader()
	id := reader.records[0].ID
	read := func(current *reviewEvidenceReader, pointer string) map[string]any {
		arguments, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": id, "json_pointer": pointer})
		require.NoError(t, err)
		return current.read(string(arguments))
	}

	largeNumber := read(reader, `/a~1b~0c/0/value`)
	require.Equal(t, "ok", largeNumber["status"])
	encoded, err := json.Marshal(largeNumber)
	require.NoError(t, err)
	require.Contains(t, string(encoded), large, "UseNumber must preserve exact integer source text")
	truncated := read(reader, `/truncated`)
	require.Equal(t, true, truncated["value"], "a successful review read does not erase original truncation")
	incompleteScan := read(reader, `/scan/complete`)
	require.Equal(t, false, incompleteScan["value"], "restoring a clipped view cannot turn incomplete source coverage into complete coverage")

	unknownReader := newReader()
	unknownArgs, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": "not-a-real-handle", "json_pointer": "/"})
	require.NoError(t, err)
	unknown := unknownReader.read(string(unknownArgs))
	require.Equal(t, "unknown_evidence_id", unknown["status"])
	invalidEscape := read(newReader(), `/a~2b`)
	require.Equal(t, "invalid_pointer", invalidEscape["status"])
	missingPath := read(newReader(), `/not-present`)
	require.Equal(t, "invalid_pointer", missingPath["status"])
	malformedArgs := newReader().read(`{"operation":"read","evidence_id":"` + id + `","unexpected":true}`)
	require.Equal(t, "invalid_arguments", malformedArgs["status"])
}

func TestReviewEvidenceReaderSlicesUnicodeAndEnforcesReadAndCharacterBudgets(t *testing.T) {
	unicodeText := "甲🛰乙丙丁"
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "source", Content: unicodeText}})
	args, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": reader.records[0].ID, "json_pointer": "", "offset": 1, "max_chars": 2})
	require.NoError(t, err)
	segment := reader.read(string(args))
	require.Equal(t, "🛰乙", segment["text"], "offsets count Unicode characters rather than UTF-8 bytes")
	require.Equal(t, "unicode_text_value", segment["offset_space"])

	long := strings.Repeat("界", reviewEvidenceRuneBudget+100)
	reader = newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "source", Content: long}})
	offset := 0
	for i := 0; i < 3; i++ {
		args, err = json.Marshal(map[string]any{"operation": "read", "evidence_id": reader.records[0].ID, "json_pointer": "", "offset": offset, "max_chars": reviewEvidenceSegmentLimit})
		require.NoError(t, err)
		segment = reader.read(string(args))
		require.Equal(t, "ok", segment["status"])
		offset += len([]rune(segment["text"].(string)))
	}
	require.Equal(t, reviewEvidenceRuneBudget, offset, "each returned source segment is charged to the aggregate Unicode budget")
	args, err = json.Marshal(map[string]any{"operation": "read", "evidence_id": reader.records[0].ID, "json_pointer": "", "offset": offset, "max_chars": 1})
	require.NoError(t, err)
	exhausted := reader.read(string(args))
	require.Equal(t, "budget_exhausted", exhausted["status"])
	require.Equal(t, 3, reader.reads, "no fourth content read is performed after the character budget is spent")

	readCountReader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "source", Content: strings.Repeat("x", 40)}})
	readCountReader.remaining = 10000
	for i := 0; i < reviewEvidenceReadLimit; i++ {
		args, err = json.Marshal(map[string]any{"operation": "read", "evidence_id": readCountReader.records[0].ID, "json_pointer": "", "max_chars": 1})
		require.NoError(t, err)
		require.Equal(t, "ok", readCountReader.read(string(args))["status"])
	}
	args, err = json.Marshal(map[string]any{"operation": "read", "evidence_id": readCountReader.records[0].ID, "json_pointer": "", "max_chars": 1})
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", readCountReader.read(string(args))["status"], "the independent action-count limit also applies when characters remain")

	listMessages := make([]chat.Message, 32)
	for i := range listMessages {
		listMessages[i] = chat.Message{Role: "tool", Name: "source", ToolCallID: strings.Repeat("x", i+1), Content: `{"value":1}`}
	}
	listReader := newReviewEvidenceReader(listMessages)
	listReader.remaining = 700
	args, err = json.Marshal(map[string]any{"operation": "list", "offset": 0, "limit": 24})
	require.NoError(t, err)
	listed := listReader.read(string(args))
	require.Equal(t, "ok", listed["status"])
	entries := listed["entries"].([]reviewEvidenceRecord)
	require.Greater(t, len(entries), 0)
	require.Less(t, len(entries), 24, "identity listing shrinks to fit the remaining Unicode budget")
	encodedList, err := json.Marshal(listed)
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(string(encodedList))), 700)
	require.GreaterOrEqual(t, listReader.remaining, 0)
}

func TestReviewEvidenceToolRejectsUnregisteredActions(t *testing.T) {
	model := &mockChat{responses: []mockResponse{reviewEvidenceToolResponse(types.LLMToolCall{
		ID: "wrong-tool", Type: "function",
		Function: types.FunctionCall{Name: "read_document", Arguments: `{"id":"source"}`},
	})}}
	e := sourceConsistencyEngine(t, model)
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "read_document", Content: `{"content":"actual source"}`}})
	_, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "system", Content: "review"}},
		&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, nil, reader)
	require.Error(t, err, "the critic cannot substitute ordinary source tools for the bounded evidence protocol")
	require.Len(t, model.calls, 1)
	require.Zero(t, reader.reads, "unregistered actions must not execute against the evidence reader")
}

func TestReviewEvidenceStreamingPreservesProviderStateAndAggregatesUsage(t *testing.T) {
	state := &types.AgentState{}
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "read_document", Content: `{"content":"verified source"}`}})
	metadata := types.ProviderMetadata{"provider_state": json.RawMessage(`{"opaque":"reasoning-state"}`)}
	callMetadata := types.ToolCallMetadata{"provider_state": json.RawMessage(`{"opaque":"call-state"}`)}
	call := reviewEvidenceToolCall(t, "read-tail", reader.records[0].ID, "/content", 0, 100)
	call.ProviderMetadata = callMetadata
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{
			{ResponseType: types.ResponseTypeThinking, Content: "private reasoning"},
			{ResponseType: types.ResponseTypeAnswer, ToolCalls: []types.LLMToolCall{call}, Data: map[string]interface{}{
				"reasoning_signature": "signed-reasoning",
				"reasoning_metadata":  metadata,
			}, Usage: &types.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14}, Done: true, FinishReason: "tool_calls"},
		}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: `{"complete":true}`, Usage: &types.TokenUsage{PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9}, Done: true, FinishReason: "stop"}}},
	}}
	e := sourceConsistencyEngine(t, model)
	result, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "system", Content: "review"}},
		&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, state, reader)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, `{"complete":true}`, result.Content)
	require.Len(t, model.calls, 2)
	var assistant *chat.Message
	for i := range model.calls[1] {
		if model.calls[1][i].Role == "assistant" && len(model.calls[1][i].ToolCalls) > 0 {
			assistant = &model.calls[1][i]
			break
		}
	}
	require.NotNil(t, assistant, "the tool continuation must include the original assistant action message")
	require.Equal(t, "private reasoning", assistant.ReasoningContent)
	require.Equal(t, "signed-reasoning", assistant.ReasoningSignature)
	require.Equal(t, metadata, assistant.ReasoningMetadata)
	require.Len(t, assistant.ToolCalls, 1)
	require.Equal(t, callMetadata, assistant.ToolCalls[0].ProviderMetadata)
	require.Equal(t, 18, state.TurnUsage.PromptTokens)
	require.Equal(t, 5, state.TurnUsage.CompletionTokens)
	require.Equal(t, 23, state.TurnUsage.TotalTokens, "usage from each streaming round contributes to the turn total")
}

func TestReviewEvidenceBudgetExhaustionForcesToolChoiceNoneForVerdict(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer, Content: `{"complete":false,"next_action":"revise_answer"}`,
		Done: true, FinishReason: "stop",
	}}}}}
	e := sourceConsistencyEngine(t, model)
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "read_document", Content: `{"content":"actual source"}`}})
	reader.reads = reviewEvidenceReadLimit
	_, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "system", Content: "review"}},
		&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, nil, reader)
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	require.Empty(t, model.opts[0].Tools)
	require.Equal(t, "none", model.opts[0].ToolChoice, "once the bounded access budget is exhausted, the model must return a verdict without more tool calls")
}

func TestReviewEvidenceUnknownHandleReturnsNoFactsAndAllowsVerdict(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		reviewEvidenceToolResponse(reviewEvidenceToolCall(t, "unknown-handle", "not-authorized", "/content", 0, 100)),
		sourceConsistencyVerdict(`{"complete":true}`),
	}}
	e := sourceConsistencyEngine(t, model)
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "read_document", Content: `{"content":"actual source"}`}})
	result, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "system", Content: "review"}},
		&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, nil, reader)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, `{"complete":true}`, result.Content)
	require.Len(t, model.calls, 2)
	require.Contains(t, model.calls[1][len(model.calls[1])-1].Content, `"status":"unknown_evidence_id"`)
	require.NotContains(t, model.calls[1][len(model.calls[1])-1].Content, "actual source", "unknown IDs reveal no evidence")
}

func reviewEvidencePayloadID(reader *reviewEvidenceReader) string {
	for _, record := range reader.records {
		if value, ok := record.Value.(map[string]any); ok {
			if _, hasData := value["data"]; hasData {
				return record.ID
			}
		}
	}
	return ""
}

func TestReviewEvidenceRootPointerDoesNotAliasAnEmptyObjectKey(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "source", Content: `{"":"a real empty-key field","value":17}`}})
	id := reader.records[0].ID
	root := reader.read(`{"operation":"read","evidence_id":"` + id + `","json_pointer":""}`)
	require.IsType(t, map[string]any{}, root["value"])
	field := reader.read(`{"operation":"read","evidence_id":"` + id + `","json_pointer":"/"}`)
	require.Equal(t, "a real empty-key field", field["text"])
	_, omissions := projectReviewValue(strings.Repeat("x", 100), 12, "", 0)
	require.Equal(t, []string{" (text suffix omitted)"}, omissions, "omission metadata must refer to the same RFC 6901 root as the read protocol")
}

func TestReviewEvidenceRejectsReusedCallIDsAcrossReviewRounds(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "source", Content: `{"value":17}`}})
	call := reviewEvidenceToolCall(t, "reused-call", reader.records[0].ID, "/value", 0, 100)
	model := &mockChat{responses: []mockResponse{reviewEvidenceToolResponse(call), reviewEvidenceToolResponse(call)}}
	e := sourceConsistencyEngine(t, model)
	_, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "user", Content: "review"}}, &chat.ChatOptions{}, nil, reader)
	require.ErrorContains(t, err, "identity")
	require.Equal(t, 1, reader.reads, "an invalid repeated protocol identity cannot trigger another read")
}
