package repository

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestInterruptedTranscriptRejectsStaleExecutorWrite(t *testing.T) {
	repo, _ := newMessageRepositoryForForkTest(t)
	ctx := t.Context()
	message, err := repo.CreateMessage(ctx, &types.Message{ID: "message", SessionID: "session", Role: "assistant"})
	require.NoError(t, err)
	stale := *message
	message.IsCompleted, message.RunState, message.Content = true, types.MessageRunStateInterrupted, "verified partial result"
	require.NoError(t, repo.UpdateMessage(ctx, message))
	stale.IsCompleted, stale.Content = true, "late draft"
	require.ErrorIs(t, repo.UpdateMessage(ctx, &stale), types.ErrMessageRunInterrupted)
	persisted, err := repo.GetMessage(ctx, message.SessionID, message.ID)
	require.NoError(t, err)
	require.Equal(t, types.MessageRunStateInterrupted, persisted.RunState)
	require.Equal(t, message.Content, persisted.Content)
}
