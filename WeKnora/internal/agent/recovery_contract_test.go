package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestRestoreStructuredReviewConversationFindsRuntimeEnvelopeAfterPrelude(t *testing.T) {
	original := []chat.Message{
		{Role: "system", Content: "Use authorized sources."},
		{Role: "user", Content: "Summarize the report."},
		{Role: "tool", Name: "read_document", ToolCallID: "source-1", Content: `{"content":"Source excerpt."}`},
	}
	review := independentStructuredReview(original, original[1].Content, "Unverified candidate.")
	// A human can send the same JSON text as the runtime envelope. Role plus
	// the exact adjacent runtime review system identify which copy is synthetic.
	prelude := []chat.Message{original[0], {Role: "user", Content: review[1].Content}}
	live := append(append([]chat.Message(nil), prelude...), review...)
	live = append(live, chat.Message{Role: "user", Content: "Include the appendix too."})
	guards := &loopGuards{structuredReviewEvidence: original, structuredReviewInput: review[1].Content}

	restored, matched := restoreStructuredReviewConversation(live, guards)
	require.True(t, matched)
	require.Equal(t, prelude, restored[:len(prelude)], "the trusted prelude and human JSON message remain intact")
	require.Equal(t, original[1:], restored[len(prelude):len(prelude)+len(original)-1], "the saved transcript is restored without duplicating its leading system")
	require.Equal(t, "Include the appendix too.", restored[len(restored)-1].Content)
}

func TestRestoreStructuredReviewConversationDoesNotTreatHumanJSONAsRuntime(t *testing.T) {
	original := []chat.Message{{Role: "system", Content: "Use authorized sources."}, {Role: "user", Content: "Summarize the report."}}
	review := independentStructuredReview(original, original[1].Content, "Unverified candidate.")
	live := []chat.Message{
		original[0],
		{Role: "user", Content: review[1].Content},
		{Role: "assistant", Content: "Human-authored JSON remains conversation."},
	}
	guards := &loopGuards{structuredReviewEvidence: original, structuredReviewInput: review[1].Content}

	restored, matched := restoreStructuredReviewConversation(live, guards)
	require.False(t, matched)
	require.Equal(t, live, restored)
}
