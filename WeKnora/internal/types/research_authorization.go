package types

import (
	"context"
	"strings"
)

const ResearchAuthorizationContextKey ContextKey = "research_user_authorization"

// Keep a verified bearer out of generic JSON and context diagnostics.
type researchAuthorization struct{ value string }

func (researchAuthorization) String() string { return "[redacted]" }

func WithResearchAuthorization(ctx context.Context, authorization string) context.Context {
	if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) <= 7 || len(authorization) > 8192 || strings.ContainsAny(authorization, "\r\n") {
		return ctx
	}
	return context.WithValue(ctx, ResearchAuthorizationContextKey, researchAuthorization{value: authorization})
}

func ResearchAuthorizationFromContext(ctx context.Context) string {
	value, _ := ctx.Value(ResearchAuthorizationContextKey).(researchAuthorization)
	return value.value
}
