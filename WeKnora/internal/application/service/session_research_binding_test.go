package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUpdateSessionCannotReplaceResearchWorkbenchBinding(t *testing.T) {
	for _, initial := range []string{"", "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67"} {
		t.Run(initial, func(t *testing.T) {
			svc, db := newTestSessionService(t)
			row := &types.Session{TenantID: 1, UserID: "alice", Title: "Before", ResearchWorkbenchID: initial}
			require.NoError(t, db.Create(row).Error)
			for _, attempted := range []string{"", "3bca7206-2e62-4495-a46b-2de44942493e"} {
				require.NoError(t, svc.UpdateSession(testSessionScopeContext(1, "alice"), &types.Session{
					ID: row.ID, TenantID: 1, Title: "After", ResearchWorkbenchID: attempted,
				}))
				var actual types.Session
				require.NoError(t, db.First(&actual, "id = ?", row.ID).Error)
				require.Equal(t, initial, actual.ResearchWorkbenchID, "editing a title must not change the evidence boundary")
				require.Equal(t, "After", actual.Title)
			}
		})
	}
}

func TestForkSessionPreservesResearchWorkbenchBindingAndHistory(t *testing.T) {
	for _, initial := range []string{"", "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67"} {
		t.Run(initial, func(t *testing.T) {
			history := []*types.Message{
				{ID: "first", SessionID: "src", Role: "user", Content: "Original question", CreatedAt: forkBase},
				{ID: "answer", SessionID: "src", Role: "assistant", Content: "Original answer", CreatedAt: forkBase.Add(time.Second)},
				{ID: "fork-point", SessionID: "src", Role: "user", CreatedAt: forkBase.Add(2 * time.Second)},
			}
			svc, sessions, _ := newForkFixture(t, nil, history)
			sessions.source.ResearchWorkbenchID = initial
			_, err := svc.Fork(context.Background(), 1, "u1", "src", "fork-point", "Branch")
			require.NoError(t, err)
			require.NotNil(t, sessions.created)
			require.Equal(t, initial, sessions.created.ResearchWorkbenchID, "a fork must keep the source conversation's evidence boundary")
			require.Equal(t, []string{"first", "answer"}, sessions.copiedFromIDs())
			require.Equal(t, initial, sessions.source.ResearchWorkbenchID)
		})
	}
}
