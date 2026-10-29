package memory

import (
	"context"
<<<<<<< a4a0a229ee25fcab7ff4301c95bec5df121485a4
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type memoryScopeAudit struct {
	interfaces.MemoryRepository
	mu      sync.Mutex
	scopes  []interfaces.MemoryScope
	touched chan interfaces.MemoryScope
}

func (r *memoryScopeAudit) record(scope interfaces.MemoryScope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, scope)
}
func (r *memoryScopeAudit) GetSubject(ctx context.Context, scope interfaces.MemoryScope) (*types.MemorySubject, error) {
	r.record(scope)
	return r.MemoryRepository.GetSubject(ctx, scope)
}
func (r *memoryScopeAudit) ListActiveResident(ctx context.Context, scope interfaces.MemoryScope, limit int) ([]*types.MemoryItem, error) {
	r.record(scope)
	return r.MemoryRepository.ListActiveResident(ctx, scope, limit)
}
func (r *memoryScopeAudit) ListActiveByKinds(ctx context.Context, scope interfaces.MemoryScope, kinds []string, limit int) ([]*types.MemoryItem, error) {
	r.record(scope)
	return r.MemoryRepository.ListActiveByKinds(ctx, scope, kinds, limit)
}
func (r *memoryScopeAudit) GetItem(ctx context.Context, scope interfaces.MemoryScope, id string) (*types.MemoryItem, error) {
	r.record(scope)
	return r.MemoryRepository.GetItem(ctx, scope, id)
}
func (r *memoryScopeAudit) SearchItemsByVector(ctx context.Context, scope interfaces.MemoryScope, q interfaces.MemoryVectorQuery) ([]interfaces.MemoryVectorHit, error) {
	r.record(scope)
	return r.MemoryRepository.SearchItemsByVector(ctx, scope, q)
}
func (r *memoryScopeAudit) TouchUsed(ctx context.Context, scope interfaces.MemoryScope, ids []string) error {
	r.record(scope)
	err := r.MemoryRepository.TouchUsed(ctx, scope, ids)
	r.touched <- scope
	return err
}

func TestSharedAgentUsesCallerMemoryAndExecutionChatModel(t *testing.T) {
	svc, _, tenants, models := newVectorHarnessWithDB(t)
	callerCtx := enabledCtx(t, tenants, 1, "alice")
	ownerCtx := enabledCtx(t, tenants, 2, "alice")
	callerItem, err := svc.Remember(callerCtx, types.MemoryItem{Kind: types.MemoryKindProfile, Content: "Caller personal inventory preference"})
	require.NoError(t, err)
	ownerItem, err := svc.Remember(ownerCtx, types.MemoryItem{Kind: types.MemoryKindProfile, Content: "OWNER private inventory preference"})
	require.NoError(t, err)
	// A disabled owner workspace must not disable the authenticated caller's memory.
	tenants.set(2, &types.MemoryConfig{Enabled: false, EmbeddingModelID: "owner-embed"})
	audit := &memoryScopeAudit{MemoryRepository: svc.repo, touched: make(chan interfaces.MemoryScope, 4)}
	svc.repo = audit
	shared := types.WithExecutionTenant(types.WithCaller(callerCtx, types.Caller{TenantID: 1, UserID: "alice"}), 2)
	resolved, err := ResolveScope(shared)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resolved.TenantID)
	result := svc.RecallForTurn(shared, testTurn("inventory preference"))
	require.Len(t, result.Items, 1)
	require.Equal(t, callerItem.ID, result.Items[0].ID)
	require.NotContains(t, result.Prompt, ownerItem.Content)
	require.NotContains(t, models.lastPrompt, ownerItem.Content)
	require.Equal(t, uint64(2), models.requestedMetadataTenant)
	require.Equal(t, uint64(2), models.requestedModelTenant)
	require.Equal(t, uint64(1), models.requestedEmbedTenant)
	require.Equal(t, "embed-1", models.requestedEmbedID)
	select {
	case used := <-audit.touched:
		require.Equal(t, resolved, used)
	case <-time.After(time.Second):
		t.Fatal("usage was not recorded")
	}
	search := svc.SearchMemory(interfaces.WithMemoryRecallRequest(shared, testTurn("inventory preference")), "inventory", 10)
	require.Len(t, search.Items, 1)
	require.Equal(t, callerItem.ID, search.Items[0].ID)
	select {
	case used := <-audit.touched:
		require.Equal(t, resolved, used)
	case <-time.After(time.Second):
		t.Fatal("search usage was not recorded")
	}
	items, total, err := svc.ListItems(shared, types.MemoryStatusActive, 10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, callerItem.ID, items[0].ID)
	audit.mu.Lock()
	defer audit.mu.Unlock()
	for _, scope := range audit.scopes {
		require.Equal(t, resolved, scope, "owner tenant memory must never be queried")
	}
}

func TestSharedAgentModelMetadataCannotSwitchToCallerOrThirdWorkspace(t *testing.T) {
	for _, tenant := range []uint64{0, 1, 3} {
		t.Run(string(rune('a'+tenant)), func(t *testing.T) {
			svc, ctx, models, _ := relevanceFixture(t)
			ctx = types.WithExecutionTenant(types.WithCaller(ctx, types.Caller{TenantID: 1, UserID: "alice"}), 2)
			models.metadata = &types.Model{ID: "relevance-test", TenantID: tenant, Type: types.ModelTypeKnowledgeQA, Status: types.ModelStatusActive}
			require.Empty(t, svc.RecallForTurn(ctx, testTurn("inventory")).Prompt)
			require.Zero(t, models.relevanceCalls)
			require.Equal(t, uint64(2), models.requestedMetadataTenant)
		})
	}
}

func TestMemoryScopeDoesNotFallBackFromMissingAuthenticatedCaller(t *testing.T) {
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(2))
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: "alice"})
	ctx = types.WithCaller(ctx, types.Caller{})
	_, err := ResolveScope(ctx)
=======
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Memory follows the person who is chatting, not the tenant the turn executes
// in: a shared agent borrows its owner's workspace for models and knowledge,
// while the caller's memories stay in the caller's own workspace.
func TestResolveScopeUsesCallerWorkspaceAcrossExecutionTenant(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: "alice"})
	ctx = types.WithExecutionTenant(ctx, 42)

	scope, err := ResolveScope(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(7), scope.TenantID)
	require.Equal(t, "web_user:alice", scope.SubjectID)
}

// Contexts that never carried a Caller (embed visitors, background jobs built
// from a bare tenant) keep resolving against the execution tenant.
func TestResolveScopeFallsBackToExecutionTenantWithoutCaller(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalEmbedVisitor, ID: "visitor-1"})

	scope, err := ResolveScope(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(42), scope.TenantID)
	require.Equal(t, "embed_visitor:visitor-1", scope.SubjectID)
}

func TestResolveScopeRequiresTenantAndPrincipal(t *testing.T) {
	_, err := ResolveScope(context.Background())
>>>>>>> c153fcd099a85b248ca75337b98a9265b951e7f0
	require.ErrorIs(t, err, ErrNoMemoryScope)
}
