package types

import "context"

type tenantOwnerManagementKey struct{}

// WithTenantOwnerManagement preserves the configured cross-tenant operator
// exception without promoting the caller's role or persisting ownership.
func WithTenantOwnerManagement(ctx context.Context) context.Context {
	return context.WithValue(ctx, tenantOwnerManagementKey{}, true)
}

func CanManageTenantOwners(ctx context.Context) bool {
	allowed, _ := ctx.Value(tenantOwnerManagementKey{}).(bool)
	return CallerFromContext(ctx).Role == TenantRoleOwner || allowed
}
