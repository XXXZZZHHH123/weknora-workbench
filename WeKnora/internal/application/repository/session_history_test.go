package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSessionHistoryRequiresPersistedUserMessage(t *testing.T) {
	repo, db := newSessionRepositoryForTest(t)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &testIMChannelSession{}))
	empty := &types.Session{TenantID: 1, UserID: "alice", Title: "Draft"}
	assistantOnly := &types.Session{TenantID: 1, UserID: "alice", Title: "Draft"}
	deleted := &types.Session{TenantID: 1, UserID: "alice", Title: "Draft"}
	active := &types.Session{TenantID: 1, UserID: "alice", Title: "Draft"}
	foreign := &types.Session{TenantID: 2, UserID: "alice", Title: "Draft"}
	for _, row := range []*types.Session{empty, assistantOnly, deleted, active, foreign} {
		require.NoError(t, db.Create(row).Error)
	}
	for _, row := range []*types.Message{
		{SessionID: assistantOnly.ID, Role: "assistant", Content: "welcome"},
		{SessionID: deleted.ID, Role: "user", Content: "removed"},
		{SessionID: active.ID, Role: "user", Content: "real question"},
		{SessionID: foreign.ID, Role: "user", Content: "outside tenant"},
	} {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Where("session_id = ?", deleted.ID).Delete(&types.Message{}).Error)
	for _, keyword := range []string{"", "Draft"} {
		items, total, err := repo.QueryPaged(context.Background(), &types.SessionListQuery{TenantID: 1, UserID: "alice", Source: "web", Keyword: keyword, Page: 1, PageSize: 10})
		require.NoError(t, err)
		require.EqualValues(t, 1, total, "drafts and deleted user messages are not conversations")
		require.Len(t, items, 1)
		require.Equal(t, active.ID, items[0].ID)
	}
	all, err := repo.GetByTenantID(context.Background(), 1, "alice")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{active.ID, empty.ID, assistantOnly.ID, deleted.ID}, sessionIDsForTest(all),
		"internal cleanup enumerates drafts as well; public history uses the filtered paged queries")
	paged, total, err := repo.GetPagedByTenantID(context.Background(), 1, "alice", &types.Pagination{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, []string{active.ID}, sessionIDsForTest(paged))
	_, err = repo.Get(context.Background(), 1, "alice", empty.ID)
	require.NoError(t, err, "a draft remains openable for its first question")
}
