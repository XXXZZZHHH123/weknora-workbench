package collaboration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExecutionScopeConcurrentStartsShareOneHeartbeatAndCleanup(t *testing.T) {
	store, scope := testStore(t)
	r := New(store, nil)
	ctx, finish := BeginExecution(t.Context())
	defer finish()
	b, err := r.AcquireParent(ctx, scope)
	require.NoError(t, err)
	results := make(chan context.Context, 16)
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			started, stop := b.Start(ctx)
			stop()
			results <- started
		}()
	}
	workers.Wait()
	close(results)
	for started := range results {
		require.NoError(t, started.Err())
	}
	owner := ctx.Value(executionKey{}).(*executionScope)
	owner.mu.Lock()
	count := len(owner.leases)
	owner.mu.Unlock()
	require.Equal(t, 1, count, "one execution owns exactly one heartbeat for this authority")
	finish()
	require.Error(t, ctx.Err())
	require.NotEqual(t, "active", read(t, store, scope).Status)
}

func TestParentLeaseStartSynchronouslyRenewsValidNearExpiryOwner(t *testing.T) {
	store, scope := testStore(t)
	r := New(store, nil)
	r.Lease, r.Poll = 900*time.Millisecond, 5*time.Millisecond
	b, err := r.AcquireParent(t.Context(), scope)
	require.NoError(t, err)
	require.NoError(t, store.Change(t.Context(), scope, func(g *Group) error {
		g.ParentLease = time.Now().Add(150 * time.Millisecond)
		return nil
	}))
	before := read(t, store, scope)
	ctx, stop := b.Start(t.Context())
	defer stop()
	require.NoError(t, ctx.Err())
	after := read(t, store, scope)
	require.True(t, after.ParentLease.After(before.ParentLease), "renewal must commit before Start returns, without waiting for its first tick")
	require.Equal(t, b.ParentOwner, after.ParentOwner)
	time.Sleep(200 * time.Millisecond) // beyond the old deadline, before the first 300ms tick
	require.NoError(t, r.CheckParent(ctx, scope, b.ParentOwner))
}

func TestExecutionScopeCoversFactoryAndPreEnginePreparationWithoutDuplicateLease(t *testing.T) {
	store, scope := testStore(t)
	r := New(store, nil)
	r.Lease, r.Poll = 400*time.Millisecond, 5*time.Millisecond
	before := read(t, store, scope)
	ctx, finish := BeginExecution(t.Context())
	defer finish()
	require.Equal(t, before.Revision, read(t, store, scope).Revision, "a scope does not acquire or mutate authority")
	b, err := r.AcquireParent(ctx, scope)
	require.NoError(t, err)
	require.NoError(t, StartExecutionLease(ctx, b))
	initialLease := read(t, store, scope).ParentLease
	// Session preparation uses the original scope context, before the engine
	// receives the derived heartbeat context. The factory owns the same lease.
	inner, stopInner := b.Start(ctx)
	require.NoError(t, inner.Err())
	nested, stopNested := b.Start(inner)
	stopNested()
	stopInner()
	require.NoError(t, nested.Err(), "nested cleanup cannot stop the execution scope's heartbeat")
	deadline := time.Now().Add(3 * r.Lease)
	for time.Now().Before(deadline) {
		r.Tick(ctx)
		require.NoError(t, ctx.Err())
		require.NoError(t, r.CheckParent(ctx, scope, b.ParentOwner))
		time.Sleep(20 * time.Millisecond)
	}
	g := read(t, store, scope)
	require.True(t, g.ParentLease.After(initialLease))
	require.Equal(t, "active", g.Status)
	finish()
	finish() // failure and outer defers can both request cleanup
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	g = read(t, store, scope)
	require.Equal(t, "completed", g.Status, "a prepared turn with no delegated work is settled")
	revision := g.Revision
	time.Sleep(r.Lease)
	require.Equal(t, revision, read(t, store, scope).Revision, "cleanup joins its heartbeat; it cannot renew afterward")
}

func TestExecutionScopePreparationFailureAndCancellationReleaseOwnership(t *testing.T) {
	for _, mode := range []string{"factory_error", "preparation_error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, scope := testStore(t)
			r := New(store, nil)
			r.Lease, r.Poll = 400*time.Millisecond, 5*time.Millisecond
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			var b *Binding
			failure := func() error {
				ctx, finish := BeginExecution(parent)
				defer finish()
				var err error
				b, err = r.AcquireParent(ctx, scope)
				if err != nil {
					return err
				}
				if err = StartExecutionLease(ctx, b); err != nil {
					return err
				}
				if mode == "cancel" {
					cancel()
					return ctx.Err()
				}
				return errors.New(mode)
			}
			require.Error(t, failure())
			g := read(t, store, scope)
			require.NotEqual(t, "active", g.Status)
			lease, revision := g.ParentLease, g.Revision
			time.Sleep(r.Lease)
			g = read(t, store, scope)
			require.Equal(t, lease, g.ParentLease)
			require.Equal(t, revision, g.Revision)
			require.Error(t, r.CheckParent(context.Background(), scope, b.ParentOwner))
		})
	}
}

func TestParentLeaseStartRejectsExpiredReplacedClosedAndPreservesClosingFence(t *testing.T) {
	for _, mode := range []string{"expired", "replaced", "closed", "closing", "closing_expired", "inherited_owner_replaced"} {
		t.Run(mode, func(t *testing.T) {
			store, scope := testStore(t)
			r := New(store, nil)
			ctx := t.Context()
			b, err := r.AcquireParent(ctx, scope)
			require.NoError(t, err)
			if mode == "inherited_owner_replaced" {
				var stop func()
				ctx, stop = b.Start(ctx)
				defer stop()
			}
			require.NoError(t, store.Change(context.Background(), scope, func(g *Group) error {
				switch mode {
				case "expired":
					g.ParentLease = time.Now().Add(-time.Second)
				case "replaced", "inherited_owner_replaced":
					g.ParentOwner = "other-owner"
				case "closed":
					g.Status = "cancelled"
				case "closing", "closing_expired":
					g.Status, g.ParentClosePending, b.Closing = "cancelling", true, true
					if mode == "closing_expired" {
						g.ParentLease = time.Now().Add(-time.Second)
					}
				}
				return nil
			}))
			before := read(t, store, scope)
			started, stop := b.Start(ctx)
			defer stop()
			if mode == "closing" {
				require.NoError(t, started.Err())
				require.NoError(t, r.CheckParentFinalization(started, scope, b.ParentOwner))
				require.Error(t, r.CheckParent(started, scope, b.ParentOwner), "closing cannot run model/tools")
				b.End(false)
				require.True(t, read(t, store, scope).ParentClosePending, "ordinary cleanup cannot consume a closing outbox")
				return
			}
			require.ErrorIs(t, context.Cause(started), ErrConflict)
			after := read(t, store, scope)
			require.Equal(t, before.Revision, after.Revision, "Start must not extend or replace invalid authority")
			require.Equal(t, before.ParentLease, after.ParentLease)
			require.Error(t, r.CheckParentFinalization(started, scope, b.ParentOwner))
			_, err = r.Finish(b.Context(context.Background()), scope, 0)
			require.Error(t, err, "a stale owner cannot finalize a live replacement")
		})
	}
}
