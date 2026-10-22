package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationArtifactsSurviveStaleStopAndCompletionSnapshots(t *testing.T) {
	db := newArtifactTestDB(t)
	repo := NewMessageRepository(db)
	ctx := context.Background()
	msg, err := repo.CreateMessage(ctx, &types.Message{ID: "message", SessionID: "session", Role: "assistant", Artifacts: types.MessageArtifacts{}})
	require.NoError(t, err)
	image := testArtifact("image.png", "image-generation://execution", time.Now())
	row := types.NewMessageArtifactRecords(msg.SessionID, msg.ID, types.MessageArtifacts{image})[0]
	require.NoError(t, db.Create(&row).Error)
	// The stop handler still holds the snapshot from before API delivery.
	require.NoError(t, repo.UpdateMessage(ctx, msg))
	require.Len(t, msg.Artifacts, 1)
	require.Equal(t, image.URL, msg.Artifacts[0].URL)
	// A sandbox completion also started from that earlier empty snapshot.
	other := testArtifact("report.pdf", "/workspace/output/report.pdf", time.Now())
	stale := &types.Message{ID: msg.ID, SessionID: msg.SessionID, Artifacts: types.MessageArtifacts{other}}
	require.NoError(t, repo.UpdateMessage(ctx, stale))
	loaded, err := repo.GetMessage(ctx, msg.SessionID, msg.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Artifacts, 2)
	require.Equal(t, image.URL, loaded.Artifacts[0].URL)
	require.Equal(t, other.URL, loaded.Artifacts[1].URL)
	_, err = repo.SoftDeleteSessionArtifacts(ctx, msg.SessionID, []types.ArtifactRef{{MessageID: msg.ID, Position: 0}}, time.Now())
	require.NoError(t, err)
	require.NoError(t, repo.UpdateMessage(ctx, stale))
	loaded, err = repo.GetMessage(ctx, msg.SessionID, msg.ID)
	require.NoError(t, err)
	require.True(t, loaded.Artifacts[0].Deleted(), "a stale writer must not revive a deleted generated image")
	require.Equal(t, other.URL, loaded.Artifacts[1].URL, "stable download index must be retained")
}
