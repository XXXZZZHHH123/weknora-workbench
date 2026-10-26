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
	ScopeVersion          int
	SourcesRevision       int64
	KnowledgeBases        []ResearchWorkbenchKnowledgeScope
}

type ResearchWorkbenchKnowledgeScope struct {
	ID       string
	TenantID uint64
}

func (s ResearchWorkbenchScope) AllowsKnowledgeBase(id string) bool {
	if s.ScopeVersion != 2 {
		return id != "" && id == s.KnowledgeBaseID
	}
	for _, base := range s.KnowledgeBases {
		if base.ID == id && id != "" {
			return true
		}
	}
	return false
}

func (s ResearchWorkbenchScope) KnowledgeBaseIDs() []string {
	if s.ScopeVersion != 2 {
		if s.KnowledgeBaseID != "" {
			return []string{s.KnowledgeBaseID}
		}
		return []string{}
	}
	result := make([]string, 0, len(s.KnowledgeBases))
	for _, base := range s.KnowledgeBases {
		result = append(result, base.ID)
	}
	return result
}

func WithResearchWorkbenchScope(ctx context.Context, scope ResearchWorkbenchScope) context.Context {
	return context.WithValue(ctx, ResearchWorkbenchScopeContextKey, scope)
}

func ResearchWorkbenchScopeFromContext(ctx context.Context) (ResearchWorkbenchScope, bool) {
	scope, ok := ctx.Value(ResearchWorkbenchScopeContextKey).(ResearchWorkbenchScope)
	return scope, ok
}
