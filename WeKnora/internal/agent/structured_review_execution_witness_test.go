package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func executionWitnessReviewInput(t *testing.T, views []reviewToolEvidence) map[string]any {
	t.Helper()
	var input map[string]any
	require.NoError(t, json.Unmarshal(mustJSON(t, map[string]any{"source_tool_results": views}), &input))
	return input
}

func TestReviewProjectionExposesCanonicalExecutionWitnessWithoutPromotingFailure(t *testing.T) {
	message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "failed-source-plan",
		Content: `{"success":false,"error":"source service timed out","data":{"status":"error","error":"source service timed out","value":999}}`}
	views, scope := boundedReviewEvidence([]chat.Message{message}, 4096)
	require.True(t, scope.ViewComplete)
	require.Len(t, views, 1)
	witness := views[0].ExecutionWitness
	require.NotNil(t, witness)
	require.Equal(t, reviewEvidenceID(message), witness.EvidenceID)
	require.Equal(t, "/error", witness.JSONPointer)
	require.Equal(t, "source service timed out", witness.Quote)
	require.Equal(t, "execution", witness.Kind)
	reader := newReviewEvidenceReader([]chat.Message{message})
	input := executionWitnessReviewInput(t, views)
	require.NoError(t, reviewWitnessVisible(input, reader, *witness))
	var wire reviewSourceWitness
	require.NoError(t, json.Unmarshal(mustJSON(t, witness), &wire), "the provided locator already satisfies the existing receipt schema")
	require.NoError(t, reviewWitnessVisible(input, reader, wire))
	asSource := *witness
	asSource.Kind = "source"
	require.ErrorContains(t, reviewWitnessVisible(input, reader, asSource), "successful tool result")
	for _, kind := range []string{"source", "execution"} {
		require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
			EvidenceID: witness.EvidenceID, JSONPointer: "/data/value", Quote: "999", Kind: kind,
		}))
	}
	wrongPath := *witness
	wrongPath.JSONPointer = "/data/error"
	require.Error(t, reviewWitnessVisible(input, reader, wrongPath), "only the canonical execution envelope is a receipt, not failed business data")
	wrongID := *witness
	wrongID.EvidenceID = "another-result"
	require.Error(t, reviewWitnessVisible(input, reader, wrongID))
}

func TestReviewExecutionWitnessDoesNotExposeHiddenOrTruncatedError(t *testing.T) {
	errorText := strings.Repeat("actual error context ", 1000) + "private error suffix"
	message := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "long-failure",
		Content: string(mustJSON(t, map[string]any{"success": false, "error": errorText}))}
	views, scope := boundedReviewEvidence([]chat.Message{message}, 1024)
	require.False(t, scope.ViewComplete)
	require.Len(t, views, 1)
	require.NotNil(t, views[0].ExecutionWitness)
	require.Equal(t, "/success", views[0].ExecutionWitness.JSONPointer)
	require.Equal(t, "false", views[0].ExecutionWitness.Quote)
	require.NotContains(t, string(mustJSON(t, views)), "private error suffix")
	require.LessOrEqual(t, len(mustJSON(t, views)), 1024, "locator shares the existing projection budget")
	reader := newReviewEvidenceReader([]chat.Message{message})
	input := executionWitnessReviewInput(t, views)
	require.NoError(t, reviewWitnessVisible(input, reader, *views[0].ExecutionWitness))
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: reviewEvidenceID(message), JSONPointer: "/error", Quote: "private error suffix", Kind: "execution",
	}), "the status locator does not make an omitted error visible")
}

func TestReviewExecutionWitnessRequiresActualVisibleFailureStatus(t *testing.T) {
	raw := map[string]any{"success": false, "error": "real failure"}
	entry := reviewToolEvidence{EvidenceID: "real-result", Tool: "read_document"}
	for _, content := range []any{nil, map[string]any{"error": "real failure"}, map[string]any{"success": true, "error": "real failure"}} {
		entry.Content = content
		require.Nil(t, visibleReviewExecutionWitness(entry, raw))
	}
	entry.Content = map[string]any{"success": false, "error": "synthetic replacement"}
	witness := visibleReviewExecutionWitness(entry, raw)
	require.Equal(t, "/success", witness.JSONPointer)
	require.Equal(t, "false", witness.Quote)
	entry.Content = raw
	entry.Tool = "plan_task"
	require.Nil(t, visibleReviewExecutionWitness(entry, raw))
	entry.Tool = "read_document"
	require.Nil(t, visibleReviewExecutionWitness(entry, map[string]any{"success": true, "error": "real failure"}))
}

func TestReviewSuccessfulSourceDoesNotGainExecutionFailureLocator(t *testing.T) {
	message := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "successful-source", Content: `{"success":true,"data":{"value":42}}`}
	views, _ := boundedReviewEvidence([]chat.Message{message}, 4096)
	require.Nil(t, views[0].ExecutionWitness)
	require.NotContains(t, string(mustJSON(t, views)), "execution_witness")
	reader := newReviewEvidenceReader([]chat.Message{message})
	require.NoError(t, reviewWitnessVisible(executionWitnessReviewInput(t, views), reader, reviewSourceWitness{
		EvidenceID: reviewEvidenceID(message), JSONPointer: "/data/value", Quote: "42", Kind: "source",
	}))
}
