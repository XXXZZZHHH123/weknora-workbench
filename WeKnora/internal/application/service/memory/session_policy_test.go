package memory

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSessionMemoryPolicySurvivesReloadAndDelayedExtraction(t *testing.T) {
	svc, tenants, messages, models, queue := newExtractionHarness(t)
	// This is a real persisted session policy, not a request-context switch.
	_, db, _ := newMemoryHarness(t)
	require.NoError(t, db.AutoMigrate(&types.Session{}, &types.Message{}))
	for _, s := range []*types.Session{
		{ID: "private-conversation", TenantID: 7, UserID: "alice", MemoryDisabled: true},
		{ID: "ordinary-conversation", TenantID: 7, UserID: "alice"},
	} {
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(s).Error)
	}
	svc.sessionRepo = repository.NewSessionRepository(db)
	ctx := enabledCtx(t, tenants, 7, "alice")
	ctx = context.WithValue(ctx, types.UserIDContextKey, "alice")
	private, err := svc.sessionRepo.Get(ctx, 7, "alice", "private-conversation")
	require.NoError(t, err)
	require.True(t, private.MemoryDisabled)
	_, err = svc.sessionRepo.Get(ctx, 7, "bob", private.ID)
	require.Error(t, err, "another owner cannot load the policy through an interactive lookup")

	recall := svc.RecallForTurn(ctx, interfaces.MemoryRecallRequest{
		SessionID: private.ID, Query: "a private task", ModelID: "model",
	})
	require.Empty(t, recall.Items)
	_, err = svc.Remember(ctx, types.MemoryItem{Kind: types.MemoryKindFact,
		Content: "must stay in this conversation", SourceSessionID: private.ID})
	require.ErrorIs(t, err, ErrMemoryDisabled)
	svc.ScheduleExtraction(ctx, private.ID, "message", "model")
	require.Empty(t, queue.tasks)

	// A bare worker context cannot regain access by losing the HTTP marker.
	// This also covers a delayed/duplicated task after a browser refresh.
	messages.set(private.ID, []*types.Message{{ID: "private-message", SessionID: private.ID,
		Role: "user", Content: "private material must not become a memory"}})
	models.response = `{"memories":[{"action":"add","kind":"fact","content":"must not store","source":1}]}`
	err = svc.Handle(context.Background(), extractTask(t, types.MemoryExtractPayload{
		TenantID: 7, SubjectID: "web_user:alice", SessionID: private.ID,
		MessageID: "private-message", ChatModelID: "model",
	}))
	require.NoError(t, err)
	require.Zero(t, models.callCount())
	_, total, err := svc.ListItems(ctx, types.MemoryStatusActive, 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)

	// Ordinary conversations retain the prior scheduling behavior.
	svc.ScheduleExtraction(ctx, "ordinary-conversation", "message", "model")
	require.Len(t, queue.tasks, 1)
}

func TestSessionMemoryPolicyFailsClosedAcrossTenantOrMissingSession(t *testing.T) {
	svc, db, tenants := newMemoryHarness(t)
	require.NoError(t, db.AutoMigrate(&types.Session{}, &types.Message{}))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Session{
		ID: "owned", TenantID: 9, UserID: "alice",
	}).Error)
	svc.sessionRepo = repository.NewSessionRepository(db)
	ctx := context.WithValue(enabledCtx(t, tenants, 7, "alice"), types.UserIDContextKey, "alice")
	require.False(t, svc.sessionAllowsMemory(ctx, "owned"))
	require.False(t, svc.sessionAllowsMemory(ctx, "missing"))
}
