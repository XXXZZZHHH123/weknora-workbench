package access

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// Context grants contain values, not mutable KB objects. Deriving a context
// copies the grant slice so parallel search branches cannot widen each other.
type kbGrant struct {
	caller     types.Caller
	kbID       string
	tenantID   uint64
	permission types.OrgMemberRole
	task       bool
}

func hasKBTaskGrant(ctx context.Context, kbID string, tenantID uint64, required types.OrgMemberRole) bool {
	caller := types.CallerFromContext(ctx)
	grants, _ := ctx.Value(types.KBGrantsContextKey).([]kbGrant)
	for _, grant := range grants {
		if grant.task && grant.caller == caller && grant.kbID == kbID && grant.tenantID == tenantID && grant.permission.HasPermission(required) {
			return true
		}
	}
	return false
}

type agentGrant struct {
	caller types.Caller
	scope  types.SharedAgentKBScope
}

// WithSharedAgent scopes execution after the caller has authorized this exact
// agent. Only its configured KBs receive read access; other source-tenant KBs
// and the source tenant's organization memberships are not inherited.
func WithSharedAgent(ctx context.Context, agent *types.CustomAgent) context.Context {
	grant := agentGrant{caller: types.CallerFromContext(ctx), scope: types.NewSharedAgentKBScope(agent)}
	ctx = context.WithValue(ctx, types.SharedAgentGrantContextKey, grant)
	if agent == nil {
		return ctx
	}
	return types.WithExecutionTenant(ctx, agent.TenantID)
}

// HasKBGrant checks only previously resolved resource access. It never infers
// ownership from the execution tenant, and always reapplies API-key scope.
func HasKBGrant(ctx context.Context, kbID string, tenantID uint64, required types.OrgMemberRole) bool {
	if !required.IsValid() || types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, kbID) != nil {
		return false
	}
	caller := types.CallerFromContext(ctx)
	if kbID == "" || tenantID == 0 {
		return false
	}
	grants, _ := ctx.Value(types.KBGrantsContextKey).([]kbGrant)
	for _, grant := range grants {
		if (caller.TenantID != 0 || grant.task) && grant.caller == caller && grant.kbID == kbID &&
			grant.tenantID == tenantID &&
			grant.permission.HasPermission(required) {
			return true
		}
	}
	grant, ok := ctx.Value(types.SharedAgentGrantContextKey).(agentGrant)
	return ok && caller.TenantID != 0 && required == types.OrgRoleViewer && grant.caller == caller &&
		grant.scope.Allows(kbID, tenantID)
}

// KBPermissions combines caller ownership, exact context grants and organization
// shares for one service operation. Pass nil shares when that entry point does
// not permit organization expansion (for example, a caller without a user).
type KBPermissions struct {
	ctx      context.Context
	caller   types.Caller
	shares   *KBSharePermissions
	lookup   KBPolicyLookup
	policies map[string]kbPolicyResult
}

type kbPolicyResult struct {
	kb  *types.KnowledgeBase
	err error
}

type KBPolicyLookup interface {
	LookupKnowledgeBaseAccess(context.Context, string) (*types.KnowledgeBase, error)
}

// NewKBPermissions resolves reads for one caller and operation.
func NewKBPermissions(ctx context.Context, shares KBShareLookup, policyLookups ...KBPolicyLookup) *KBPermissions {
	caller := types.CallerFromContext(ctx)
	lookup, _ := shares.(KBPolicyLookup)
	if len(policyLookups) > 0 && policyLookups[0] != nil {
		lookup = policyLookups[0]
	}
	return &KBPermissions{
		ctx:      ctx,
		caller:   caller,
		shares:   NewKBSharePermissions(ctx, shares, caller.TenantID, caller.Role),
		lookup:   lookup,
		policies: make(map[string]kbPolicyResult),
	}
}

// Check combines caller ownership, exact grants and organization sharing.
func (p *KBPermissions) Check(kbID string, ownerTenantID uint64, required types.OrgMemberRole) (bool, error) {
	if kbID == "" || ownerTenantID == 0 || !required.IsValid() {
		return false, nil
	}
	if err := types.AuthorizeTenantAPIKeyKnowledgeBases(p.ctx, kbID); err != nil {
		return false, err
	}
	if hasKBTaskGrant(p.ctx, kbID, ownerTenantID, required) {
		return true, nil
	}
	if p.lookup != nil {
		result, found := p.policies[kbID]
		if !found {
			result.kb, result.err = p.lookup.LookupKnowledgeBaseAccess(p.ctx, kbID)
			p.policies[kbID] = result
		}
		kb, err := result.kb, result.err
		if err != nil {
			return false, err
		}
		if kb == nil || kb.TenantID != ownerTenantID {
			return false, nil
		}
		if kb.AccessPolicy != nil && p.caller.TenantID == ownerTenantID {
			permission := LocalKBPermission(p.ctx, p.caller, kb)
			return permission.IsValid() && permission.HasPermission(required), nil
		}
		if kb.AccessPolicy != nil && kb.AccessPolicy.SpacePermission == types.ResourcePermissionNone {
			return p.shares.Check(kbID, required)
		}
	}
	if (p.caller.TenantID == ownerTenantID && required == types.OrgRoleViewer) ||
		HasKBGrant(p.ctx, kbID, ownerTenantID, required) {
		return true, nil
	}
	if p.caller.TenantID == 0 || p.caller.TenantID == ownerTenantID {
		return false, nil
	}
	return p.shares.Check(kbID, required)
}

func (p *KBPermissions) CheckKB(kb *types.KnowledgeBase, required types.OrgMemberRole) (bool, error) {
	if kb == nil {
		return false, nil
	}
	if err := types.AuthorizeTenantAPIKeyKnowledgeBases(p.ctx, kb.ID); err != nil {
		return false, err
	}
	if hasKBTaskGrant(p.ctx, kb.ID, kb.TenantID, required) {
		return true, nil
	}
	if kb.AccessPolicy != nil && p.caller.TenantID == kb.TenantID {
		permission := LocalKBPermission(p.ctx, p.caller, kb)
		return permission.IsValid() && permission.HasPermission(required), nil
	}
	if kb.AccessPolicy != nil && kb.AccessPolicy.SpacePermission == types.ResourcePermissionNone {
		return p.shares.Check(kb.ID, required)
	}
	return p.Check(kb.ID, kb.TenantID, required)
}
