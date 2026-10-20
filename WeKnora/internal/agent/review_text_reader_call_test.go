package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func textAnswer(content string) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: content, Done: true, FinishReason: "stop"}}}
}

func TestReviewReaderRequestWrittenAsTextIsExecutedAsReaderCall(t *testing.T) {
	verdict := `{"complete":false,"next_action":"revise_answer"}`
	model := &mockChat{responses: []mockResponse{textAnswer(`{"operation":"list"}`), textAnswer(verdict)}}
	e := sourceConsistencyEngine(t, model)
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "read_document", Content: `{"success":true,"data":{"content":"actual source"}}`}})
	result, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "system", Content: "review"}},
		&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, nil, reader)
	require.NoError(t, err)
	require.Equal(t, verdict, result.Content)
	require.Equal(t, 1, reader.reads, "the text request ran through the bounded reader")
	require.Len(t, model.calls, 2)
}

func TestReviewTextOnlyBecomesReaderCallWhenStrictlyReaderArguments(t *testing.T) {
	for _, content := range []string{
		`{"complete":true}`,
		`{"operation":"read","evidence_id":"x","extra":1}`,
		`{"operation":"execute"}`,
		`note {"operation":"list"}`,
		`{"operation":"list"} {"operation":"list"}`,
	} {
		_, ok := reviewEvidenceArgumentsFromText(content)
		require.False(t, ok, content)
	}
	arguments, ok := reviewEvidenceArgumentsFromText(` {"operation":"read","evidence_id":"x","json_pointer":"/data/rows","start_index":3} `)
	require.True(t, ok)
	require.Contains(t, arguments, `"start_index":3`)
	// Without an offered reader tool, text is left for the verdict parser.
	model := &mockChat{responses: []mockResponse{textAnswer(`{"operation":"list"}`)}}
	e := sourceConsistencyEngine(t, model)
	reader := newReviewEvidenceReader(nil)
	reader.reads = reviewEvidenceReadLimit
	result, err := e.streamReviewWithEvidence(t.Context(), model, []chat.Message{{Role: "system", Content: "review"}},
		&chat.ChatOptions{Tools: []chat.Tool{reviewEvidenceTool()}, ToolChoice: "auto"}, nil, reader)
	require.NoError(t, err)
	require.Equal(t, `{"operation":"list"}`, result.Content)
}
