package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func reviewRetryUsage(response mockResponse, prompt, completion int) mockResponse {
	if response.acceptSourceReview {
		response.sourceReviewUsage = &types.TokenUsage{
			PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
		}
		return response
	}
	response.chunks = append([]types.StreamResponse(nil), response.chunks...)
	response.chunks[0].Usage = &types.TokenUsage{
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
	}
	return response
}

func findReviewTranscriptPair(messages []chat.Message, callID string) (chat.Message, chat.Message, bool) {
	var assistant chat.Message
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == callID {
				assistant = message
				for _, toolMessage := range messages {
					if toolMessage.Role == "tool" && toolMessage.Name == reviewEvidenceToolName && toolMessage.ToolCallID == callID {
						return assistant, toolMessage, true
					}
				}
				return assistant, chat.Message{}, false
			}
		}
	}
	return chat.Message{}, chat.Message{}, false
}

func TestSourceReviewProtocolRetriesShareReaderTranscriptBudgetAndUsage(t *testing.T) {
	const tail = "TAIL"
	body := strings.Repeat("source-evidence-", 2200) + tail
	state := sourceConsistencyState()
	chunks := state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)
	chunks[0]["content"] = body
	e := sourceConsistencyEngine(t, &mockChat{})
	readerSeed := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	payloadID := reviewEvidencePayloadID(readerSeed)
	require.NotEmpty(t, payloadID)

	firstRead := reviewEvidenceToolResponse(reviewEvidenceToolCall(
		t, "review-read-first", payloadID, "/data/chunks/0/content", len([]rune(body))-len([]rune(tail)), len([]rune(tail)),
	))
	retryReads := make([]types.LLMToolCall, reviewEvidenceReadLimit)
	for i := range retryReads {
		retryReads[i] = reviewEvidenceToolCall(t, "review-read-retry-"+strconv.Itoa(i), payloadID, "/data/chunks/0/content", 0, 1)
	}
	readBatch := mockResponse{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		ToolCalls:    retryReads,
		Done:         true,
		FinishReason: "tool_calls",
	}}}
	invalid := sourceConsistencyVerdict(`{"complete":true,"feedback":"protocol conflict"}`)
	valid := sourceAcceptedVerdict()
	model := &mockChat{responses: []mockResponse{
		reviewRetryUsage(firstRead, 1, 1),
		reviewRetryUsage(invalid, 2, 1),
		reviewRetryUsage(readBatch, 3, 1),
		reviewRetryUsage(invalid, 4, 1),
		reviewRetryUsage(valid, 5, 1),
	}}
	e.chatModel = model
	question := "Summarize the fetched source."
	repair, err := e.evaluateAnswerCompleteness(t.Context(), question, "The source supports the requested statement.", state,
		[]chat.Message{{Role: "user", Content: question}})
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 5, "three protocol attempts include the two evidence-tool continuations")

	firstAssistant, firstTool, found := findReviewTranscriptPair(model.calls[1], "review-read-first")
	require.True(t, found, "the first read must be an actual assistant/tool pair")
	require.Contains(t, firstTool.Content, tail, "the first review reads the omitted source tail")
	for _, attemptStart := range []int{2, 4} {
		assistant, toolMessage, found := findReviewTranscriptPair(model.calls[attemptStart], "review-read-first")
		require.True(t, found, "each protocol retry must receive the prior reader transcript")
		require.Equal(t, firstAssistant.ToolCalls, assistant.ToolCalls)
		require.Equal(t, firstTool.Content, toolMessage.Content)
	}
	toolOutputs, successfulReads, exhaustedReads := 0, 0, 0
	for _, message := range model.calls[4] {
		if message.Role != "tool" || message.Name != reviewEvidenceToolName {
			continue
		}
		toolOutputs++
		var output map[string]any
		require.NoError(t, json.Unmarshal([]byte(message.Content), &output))
		switch output["status"] {
		case "ok":
			successfulReads++
		case "budget_exhausted":
			exhaustedReads++
		}
	}
	require.Equal(t, reviewEvidenceReadLimit, successfulReads, "all three attempts share the four-read allowance")
	require.Equal(t, 1, exhaustedReads, "the retry cannot replenish the exhausted reader")
	require.Equal(t, reviewEvidenceReadLimit+1, toolOutputs, "transcript retains every attempted read result")
	require.NotEmpty(t, model.opts[2].Tools, "the second attempt may use the remaining shared reader allowance")
	require.Empty(t, model.opts[3].Tools, "the reader is exhausted before the second invalid verdict")
	require.Empty(t, model.opts[4].Tools, "the third attempt does not receive a fresh reader allowance")

	require.Equal(t, 15, state.TurnUsage.PromptTokens)
	require.Equal(t, 5, state.TurnUsage.CompletionTokens)
	require.Equal(t, 20, state.TurnUsage.TotalTokens, "usage accumulates across tool rounds and all protocol retries")
}
