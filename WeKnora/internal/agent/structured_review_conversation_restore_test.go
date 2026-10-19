package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestRestoreStructuredReviewConversationWithoutGuardsReturnsLiveCopy(t *testing.T) {
	live := []chat.Message{{Role: "user", Content: "Summarize the report."}, {Role: "assistant", Content: "Draft."}}
	restored, matched := restoreStructuredReviewConversation(live, nil)
	require.False(t, matched)
	require.Equal(t, live, restored)
	restored[0].Content = "changed copy"
	require.Equal(t, "Summarize the report.", live[0].Content)
}

func TestRestoreStructuredReviewConversationRestoresHumanTranscriptAndLiveTail(t *testing.T) {
	original := []chat.Message{
		{Role: "system", Content: "Answer from authorized sources."},
		{Role: "user", Content: "Compare the field report and the lab note."},
		{Role: "tool", Name: "read_document", ToolCallID: "read-1", Content: `{"chunk_id":"c1","content":"first source excerpt"}`},
	}
	review := independentStructuredReview(original, original[1].Content, "Draft under review.")
	envelope := review[1].Content
	live := []chat.Message{
		review[0],
		review[1],
		{Role: "assistant", Content: "intermediate note"},
		{Role: "tool", Name: "read_document", ToolCallID: "read-2", Content: `{"chunk_id":"c2","content":"second source excerpt"}`},
		{Role: "user", Content: "Please include the appendix too."}, // A real steer after the envelope.
	}
	guards := &loopGuards{structuredReviewEvidence: original, structuredReviewInput: envelope}

	restored, matched := restoreStructuredReviewConversation(live, guards)
	require.True(t, matched)
	require.Equal(t, original, restored[:len(original)])
	require.Equal(t, live[2:], restored[len(original):])
	require.Equal(t, "Please include the appendix too.", restored[len(restored)-1].Content)
	// Restoring yields independent slice storage for both the live tail and saved transcript.
	restored[len(original)].Content = "changed output"
	require.Equal(t, "intermediate note", live[2].Content)
	require.Equal(t, "Answer from authorized sources.", original[0].Content)
}

func TestRestoreStructuredReviewConversationKeepsLiveTranscriptOnEnvelopeMismatch(t *testing.T) {
	original := []chat.Message{{Role: "user", Content: "Older human request."}}
	review := independentStructuredReview(original, "Older human request.", "Draft under review.")
	expectedEnvelope := review[1].Content
	live := []chat.Message{
		review[0],
		{Role: "user", Content: `{"current_user_question":"newer steer"}`},
		{Role: "user", Content: "Please use the newer scope."},
	}
	before := append([]chat.Message(nil), live...)
	guards := &loopGuards{structuredReviewEvidence: original, structuredReviewInput: expectedEnvelope}

	restored, matched := restoreStructuredReviewConversation(live, guards)
	require.False(t, matched)
	require.Equal(t, before, restored)
	restored[0].Content = "changed output"
	require.Equal(t, before, live, "the helper does not mutate the live input slice")
	require.Equal(t, "Older human request.", original[0].Content)
}
