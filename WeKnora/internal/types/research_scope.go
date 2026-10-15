package types

import "context"

const ResearchWorkbenchScopeContextKey ContextKey = "research_workbench_scope"

// ResearchWorkbenchScope is installed after resolving the session's immutable
// workbench binding. Models and request bodies cannot select this authority.
type ResearchWorkbenchScope struct {
	ID                    string
	TenantID              uint64
	UserID                string
	KnowledgeBaseID       string
	KnowledgeBaseTenantID uint64
}

func WithResearchWorkbenchScope(ctx context.Context, scope ResearchWorkbenchScope) context.Context {
	return context.WithValue(ctx, ResearchWorkbenchScopeContextKey, scope)
}

func ResearchWorkbenchScopeFromContext(ctx context.Context) (ResearchWorkbenchScope, bool) {
	scope, ok := ctx.Value(ResearchWorkbenchScopeContextKey).(ResearchWorkbenchScope)
	return scope, ok
}
