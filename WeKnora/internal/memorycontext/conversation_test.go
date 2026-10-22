package memorycontext

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func completedRows(turns int) []*types.Message {
	var rows []*types.Message
	for i := 0; i < turns; i++ {
		request := fmt.Sprintf("turn-%d", i)
		at := time.Unix(int64(i*2), 0)
		rows = append(rows,
			&types.Message{ID: fmt.Sprintf("u-%d", i), SessionID: "session", RequestID: request, Role: "user", Content: fmt.Sprintf("question-%d", i), CreatedAt: at},
			&types.Message{ID: fmt.Sprintf("a-%d", i), SessionID: "session", RequestID: request, Role: "assistant", Content: fmt.Sprintf("answer-%d", i), IsCompleted: true, CreatedAt: at.Add(time.Second)})
	}
	return rows
}

func pagedRows(rows []*types.Message) PageReader {
	return func(_ context.Context, session string, page, size int) ([]*types.Message, error) {
		start := (page - 1) * size
		if start >= len(rows) {
			return nil, nil
		}
		return rows[start:min(start+size, len(rows))], nil
	}
}

func TestCompleteMemoryConversationKeepsOldTurnsAndExistingSummaryWithoutPayloads(t *testing.T) {
	rows := completedRows(125)
	rows[0].RenderedContent = "unrelated previously injected memory and retrieved source payload"
	rows[0].Images = types.MessageImages{{URL: "data:image/png;base64,PRIVATE_IMAGE_BYTES"}}
	rows[1].AgentSteps = types.AgentSteps{{Thought: "private raw tool payload"}}
	rows[201].ContextCheckpoint = &types.ContextCheckpoint{Summary: "Existing summary of the early conversation"}
	rows = append(rows,
		&types.Message{ID: "current-user", SessionID: "session", RequestID: "current", Role: "user", Content: "current pending question", CreatedAt: time.Unix(1000, 0)},
		&types.Message{ID: "current-assistant", SessionID: "session", RequestID: "current", Role: "assistant", Content: "not finished", CreatedAt: time.Unix(1001, 0)},
		&types.Message{ID: "tool-message", SessionID: "session", RequestID: "turn-0", Role: "tool", Content: "raw tool JSON payload", CreatedAt: time.Unix(1002, 0)})
	conversation, err := LoadCompleted(t.Context(), pagedRows(rows), "session", "current-assistant", "current-user")
	require.NoError(t, err)
	require.Len(t, conversation, 251, "all 125 turns, not only the answer history tail, plus one existing summary")
	require.Equal(t, "summary", conversation[0].Role)
	require.Contains(t, conversation[0].Content, "Existing summary")
	require.Equal(t, "question-0", conversation[1].Content)
	require.Equal(t, "answer-124", conversation[len(conversation)-1].Content)
	for _, message := range conversation {
		for _, absent := range []string{"PRIVATE_IMAGE_BYTES", "private raw tool", "raw tool JSON", "previously injected", "current pending", "not finished"} {
			require.NotContains(t, message.Content, absent)
		}
	}
}

func TestCompleteMemoryConversationRejectsPartialOrForeignViews(t *testing.T) {
	for _, tc := range []struct {
		name string
		read PageReader
	}{
		{name: "authorization failure", read: func(context.Context, string, int, int) ([]*types.Message, error) { return nil, errors.New("denied") }},
		{name: "another session", read: pagedRows([]*types.Message{{ID: "foreign", SessionID: "another-session"}})},
		{name: "row budget", read: pagedRows(completedRows(MaxRows/2 + 1))},
		{name: "non advancing pages", read: func(context.Context, string, int, int) ([]*types.Message, error) {
			return completedRows(PageSize / 2), nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conversation, err := LoadCompleted(t.Context(), tc.read, "session", "", "")
			require.Error(t, err)
			require.Nil(t, conversation, "a partial prefix must not be passed off as complete context")
		})
	}
}

func TestMemoryConversationReadContextUsesCallerRatherThanSharedAgentTenant(t *testing.T) {
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	ctx = types.WithExecutionTenant(ctx, 99)
	ctx = context.WithValue(ctx, types.SessionTenantIDContextKey, uint64(55))
	readCtx, err := SessionReadContext(ctx)
	require.NoError(t, err)
	tenant, _ := types.TenantIDFromContext(readCtx)
	require.Equal(t, uint64(7), tenant)
	require.Nil(t, readCtx.Value(types.SessionTenantIDContextKey))
	require.Equal(t, "owner", types.SessionOwnerIDFromContext(readCtx))
	_, err = SessionReadContext(context.Background())
	require.Error(t, err)
}

func TestCompleteMemoryConversationKeepsCorrectionsBeforeCancelledReplies(t *testing.T) {
	rows := []*types.Message{
		{ID: "legacy-user", SessionID: "session", Role: "user", Content: "The original request", CreatedAt: time.Unix(1, 0)},
		{ID: "legacy-answer", SessionID: "session", Role: "assistant", Content: "The old completed answer", IsCompleted: true, CreatedAt: time.Unix(2, 0)},
		{ID: "correction", SessionID: "session", RequestID: "cancelled", Role: "user", Content: "Please use the revised scope from now on", CreatedAt: time.Unix(3, 0)},
		{ID: "cancelled-answer", SessionID: "session", RequestID: "cancelled", Role: "assistant", Content: "partial response must be excluded", CreatedAt: time.Unix(4, 0)},
		{ID: "current-user", SessionID: "session", RequestID: "current", Role: "user", Content: "Continue", CreatedAt: time.Unix(5, 0)},
		{ID: "current-assistant", SessionID: "session", RequestID: "current", Role: "assistant", CreatedAt: time.Unix(6, 0)},
		{ID: "future-user", SessionID: "session", RequestID: "future", Role: "user", Content: "A later request must be excluded", CreatedAt: time.Unix(7, 0)},
	}
	conversation, err := LoadCompleted(t.Context(), pagedRows(rows), "session", "current-assistant", "current-user")
	require.NoError(t, err)
	require.Len(t, conversation, 3)
	require.Equal(t, "Please use the revised scope from now on", conversation[2].Content)
	require.Equal(t, "The old completed answer", conversation[1].Content, "legacy rows do not need a RequestID")
}

func TestCompleteMemoryConversationRequiresStableCurrentTurnBoundary(t *testing.T) {
	rows := completedRows(1)
	for _, ids := range [][2]string{{"missing", ""}, {"", ""}} {
		conversation, err := LoadCompleted(t.Context(), pagedRows(rows), "session", ids[0], ids[1])
		require.Error(t, err)
		require.Nil(t, conversation)
	}
	rows = append(rows, rows[1])
	conversation, err := LoadCompleted(t.Context(), pagedRows(rows), "session", "a-0", "u-0")
	require.ErrorContains(t, err, "repeated")
	require.Nil(t, conversation, "duplicate offset entries can mean a message was missed")
}
