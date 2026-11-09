package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCollaborationAttachReleasesNewParentWhenLeaseStartFails(t *testing.T) {
	for _, mode := range []string{"canceled_after_acquire", "read_failure_after_acquire", "owner_replaced_after_acquire"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, rootCtx := collaborationService(t)
			ctx, cancel := context.WithCancel(rootCtx)
			defer cancel()
			ctx, finish := collaboration.BeginExecution(ctx)
			defer finish()
			scope := collaboration.Scope{TenantID: 7, UserID: "u", SessionID: "parent", ID: "message"}
			var acquired atomic.Bool
			var failNextRead atomic.Bool
			require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("fixture:fail_lease_start_read", func(tx *gorm.DB) {
				if failNextRead.Swap(false) {
					tx.AddError(errors.New("fixture parent lease read failed"))
				}
			}))
			// OnStatus runs after the owner acquisition has committed. Inject
			// failure before Start can read or register a cleanup with the scope.
			s.Runtime.Store.OnStatus = func(_ context.Context, g *collaboration.Group) {
				if g.ParentOwner == "" || !acquired.CompareAndSwap(false, true) {
					return
				}
				switch mode {
				case "canceled_after_acquire":
					cancel()
				case "read_failure_after_acquire":
					failNextRead.Store(true)
				case "owner_replaced_after_acquire":
					require.NoError(t, s.Runtime.Store.Change(rootCtx, scope, func(current *collaboration.Group) error {
						current.ParentOwner = "replacement-owner"
						return nil
					}))
				}
			}
			err := s.attach(ctx, &agent.AgentEngine{}, tools.NewToolRegistry(), &types.AgentConfig{}, scope.SessionID, scope.ID)
			require.Error(t, err)
			require.True(t, acquired.Load(), "fixture must fail after a real committed acquisition")
			finish()
			g, readErr := s.Runtime.Store.Read(rootCtx, scope)
			require.NoError(t, readErr)
			if mode == "owner_replaced_after_acquire" {
				require.Equal(t, "active", g.Status)
				require.Equal(t, "replacement-owner", g.ParentOwner, "failed old startup cannot finalize its replacement")
				return
			}
			require.NotEqual(t, "active", g.Status, "startup failure must not strand a newly acquired parent for its lease duration")
			retry := scope
			retry.ID = "retry-message"
			next, retryErr := s.Runtime.AcquireParent(rootCtx, retry)
			require.NoError(t, retryErr, "a fresh user turn can start immediately")
			next.End(false)
		})
	}
}

func TestCollaborationAttachFailedStartDoesNotSettleInheritedParentAuthority(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent", true: "closing"}[closing], func(t *testing.T) {
			s, _, _, rootCtx := collaborationService(t)
			scope := collaboration.Scope{TenantID: 7, UserID: "u", SessionID: "parent", ID: "message"}
			b, err := s.Runtime.AcquireParent(rootCtx, scope)
			require.NoError(t, err)
			if closing {
				b.Closing = true
				require.NoError(t, s.Runtime.Store.Change(rootCtx, scope, func(g *collaboration.Group) error {
					g.Status, g.ParentClosePending = "cancelling", true
					return nil
				}))
			}
			before, err := s.Runtime.Store.Read(rootCtx, scope)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(collaboration.WithBinding(rootCtx, b))
			ctx, finish := collaboration.BeginExecution(ctx)
			defer finish()
			cancel()
			err = s.attach(ctx, &agent.AgentEngine{}, tools.NewToolRegistry(), &types.AgentConfig{}, scope.SessionID, scope.ID)
			require.ErrorIs(t, err, context.Canceled)
			finish()
			after, err := s.Runtime.Store.Read(rootCtx, scope)
			require.NoError(t, err)
			require.Equal(t, before.Revision, after.Revision)
			require.Equal(t, before.Status, after.Status)
			require.Equal(t, before.ParentOwner, after.ParentOwner)
			require.Equal(t, before.ParentClosePending, after.ParentClosePending)
		})
	}
}
