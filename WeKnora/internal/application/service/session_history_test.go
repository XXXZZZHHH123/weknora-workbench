package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSessionHistoryListAndCountTrackUserMessages(t *testing.T) {
	svc, db := newTestSessionService(t)
	require.NoError(t, db.AutoMigrate(&testListSessionsIMChannelSession{}))
	ctx := testSessionScopeContext(1, "alice")
	draft := &types.Session{TenantID: 1, UserID: "alice", Title: "Research draft", IsPinned: true}
	require.NoError(t, db.Create(draft).Error)
	for _, keyword := range []string{"", "Research"} {
		page, err := svc.ListSessions(ctx, &types.SessionListQuery{Source: "web", Keyword: keyword})
		require.NoError(t, err)
		require.Zero(t, page.Total)
	}
	count, err := svc.CountSessionsBySource(ctx, &types.SessionListQuery{Source: "web"})
	require.NoError(t, err)
	require.Zero(t, count)
	detail, err := svc.GetSession(ctx, draft.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.HasUserMessage)
	require.False(t, *detail.HasUserMessage)
	message := &types.Message{SessionID: draft.ID, Role: "user", Content: "A real question"}
	require.NoError(t, db.Create(message).Error)
	count, err = svc.CountSessionsBySource(ctx, &types.SessionListQuery{Source: "web"})
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	detail, err = svc.GetSession(ctx, draft.ID)
	require.NoError(t, err)
	require.True(t, *detail.HasUserMessage)
	require.NoError(t, db.Delete(message).Error)
	count, err = svc.CountSessionsBySource(ctx, &types.SessionListQuery{Source: "web"})
	require.NoError(t, err)
	require.Zero(t, count)
	_, err = svc.GetSession(ctx, draft.ID)
	require.NoError(t, err, "clearing messages does not delete the reusable conversation")
}

func TestWorkspaceProjectDraftDoesNotCountUntilQuestionPersists(t *testing.T) {
	svc, _, _ := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	project, err := svc.CreateProject(ctx, WorkspaceInput{Name: workspaceString("Research")})
	require.NoError(t, err)
	require.NoError(t, svc.db.Exec("INSERT INTO sessions(id,tenant_id,user_id,title) VALUES(?,?,?,?)", "draft", 1, "alice", "Draft").Error)
	require.NoError(t, svc.AssignSession(ctx, project.ID, "draft", false))
	check := func(want int64) {
		t.Helper()
		rows, total, err := svc.ProjectSessions(ctx, project.ID, 1, 20)
		require.NoError(t, err)
		require.Equal(t, want, total)
		require.Len(t, rows, int(want))
		loaded, err := svc.GetProject(ctx, project.ID)
		require.NoError(t, err)
		require.Equal(t, want, loaded.SessionCount)
		projects, err := svc.ListProjects(ctx)
		require.NoError(t, err)
		require.Equal(t, want, projects[0].SessionCount)
	}
	check(0)
	message := &types.Message{SessionID: "draft", Role: "user", Content: "Question"}
	require.NoError(t, svc.db.WithContext(context.Background()).Create(message).Error)
	check(1)
	require.NoError(t, svc.db.Delete(message).Error)
	check(0)
}
