package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type leasePreparationChat struct {
	*mockChat
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func TestCollaborationResumePreparationFailureStopsLease(t *testing.T) {
	r, seed := engineCollaboration(t)
	r.Lease, r.Poll = 400*time.Millisecond, 5*time.Millisecond
	b, err := r.AcquireParent(t.Context(), seed.Scope)
	require.NoError(t, err)
	model := &mockChat{}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.SetCollaboration(b)
	state, err := engine.Resume(t.Context(), "s", "m", nil, json.RawMessage(`{"incomplete":`), nil)
	require.Error(t, err)
	require.Nil(t, state)
	require.Zero(t, model.callCount)
	g, err := r.Store.Read(t.Context(), b.Scope)
	require.NoError(t, err)
	require.NotEqual(t, "active", g.Status, "invalid preparation must not leave a renewable execution")
	lease, revision := g.ParentLease, g.Revision
	time.Sleep(r.Lease)
	g, err = r.Store.Read(t.Context(), b.Scope)
	require.NoError(t, err)
	require.Equal(t, lease, g.ParentLease)
	require.Equal(t, revision, g.Revision)
}

func (m *leasePreparationChat) Chat(ctx context.Context, _ []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.once.Do(func() { close(m.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.release:
		// An unavailable optional planner retains normal tools and execution.
		return nil, errors.New("fixture planner unavailable")
	}
}

// This regression uses only APIs present before the fix, so it can be overlaid
// on the previous source to prove that real preparation outlives its lease.
func TestCollaborationParentLeaseCoversSlowSourcePreparation(t *testing.T) {
	r, seed := engineCollaboration(t)
	r.Lease, r.Poll = 400*time.Millisecond, 5*time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	owned, err := r.AcquireParent(ctx, seed.Scope)
	require.NoError(t, err)
	var recovered atomic.Int32
	r.WakeParent = func(ctx context.Context, b *collaboration.Binding, _ []byte) error {
		recovered.Add(1)
		return r.Cancel(b.Context(ctx), b.Scope)
	}
	model := &leasePreparationChat{
		mockChat: &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
			ResponseType: types.ResponseTypeAnswer, Content: "Ready.", Done: true, FinishReason: "stop",
		}}}}},
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	var release sync.Once
	defer release.Do(func() { close(model.release) })
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(newCountingTool("lookup_fixture"))
	engine.SetCollaboration(owned)
	r.Start(ctx)
	defer func() {
		release.Do(func() { close(model.release) })
		stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		require.NoError(t, r.Stop(stopCtx))
	}()
	type outcome struct {
		state *types.AgentState
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		state, err := engine.Execute(ctx, "s", "m", "Explain the requested concept.", nil)
		result <- outcome{state, err}
	}()
	select {
	case <-model.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("task preparation did not enter its model request")
	}
	// The scheduler is running throughout a real blocked planner call, for
	// three complete original leases. A late Start at executeLoop cannot pass.
	<-time.After(3 * r.Lease)
	g, err := r.Store.Read(ctx, owned.Scope)
	require.NoError(t, err)
	require.Zero(t, recovered.Load(), "live preparation must not be classified as a restart")
	require.Equal(t, "active", g.Status)
	require.Equal(t, owned.ParentOwner, g.ParentOwner)
	require.True(t, time.Now().Before(g.ParentLease))
	release.Do(func() { close(model.release) })
	select {
	case got := <-result:
		require.NoError(t, got.err)
		require.NotNil(t, got.state)
		require.True(t, got.state.IsComplete)
		require.Equal(t, "Ready.", got.state.FinalAnswer)
	case <-time.After(3 * time.Second):
		t.Fatal("execution did not finish after preparation was released")
	}
	g, err = r.Store.Read(ctx, owned.Scope)
	require.NoError(t, err)
	require.Equal(t, "completed", g.Status)
}

func TestCollaborationPreparationCancellationOrOwnerLossCannotDeliver(t *testing.T) {
	for _, mode := range []string{"cancel", "replace_owner", "expired_before_execute", "closing"} {
		t.Run(mode, func(t *testing.T) {
			r, seed := engineCollaboration(t)
			r.Lease, r.Poll = 400*time.Millisecond, 5*time.Millisecond
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			owned, err := r.AcquireParent(ctx, seed.Scope)
			require.NoError(t, err)
			model := &leasePreparationChat{mockChat: &mockChat{}, entered: make(chan struct{}), release: make(chan struct{})}
			defer close(model.release)
			engine := newTestEngine(t, model)
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.toolRegistry = agenttools.NewToolRegistry()
			engine.toolRegistry.RegisterTool(newCountingTool("lookup_fixture"))
			engine.SetCollaboration(owned)
			if mode == "expired_before_execute" || mode == "closing" {
				require.NoError(t, r.Store.Change(ctx, owned.Scope, func(g *collaboration.Group) error {
					if mode == "closing" {
						g.ParentClosePending, g.Status, owned.Closing = true, "cancelling", true
					} else {
						g.ParentLease = time.Now().Add(-time.Second)
					}
					return nil
				}))
				state, err := engine.Execute(ctx, "s", "m", "Explain the requested concept.", nil)
				require.Error(t, err)
				require.Nil(t, state)
				select {
				case <-model.entered:
					t.Fatal("invalid execution authority reached a model")
				default:
				}
				return
			}
			result := make(chan error, 1)
			go func() {
				state, err := engine.Execute(ctx, "s", "m", "Explain the requested concept.", nil)
				if state != nil && (state.IsComplete || state.FinalAnswer != "") {
					err = errors.New("lost execution authority published a final answer")
				}
				result <- err
			}()
			select {
			case <-model.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("preparation never started")
			}
			if mode == "cancel" {
				cancel()
			} else {
				require.NoError(t, r.Store.Change(ctx, owned.Scope, func(g *collaboration.Group) error {
					g.ParentOwner = "replacement-owner"
					return nil
				}))
			}
			select {
			case err := <-result:
				require.Error(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("preparation ignored canceled or replaced authority")
			}
			model.mu.Lock()
			require.Zero(t, model.callCount, "no answer model request is allowed after lost preparation authority")
			model.mu.Unlock()
		})
	}
}
