package agent

import (
	"context"
	"time"
)

type agentExecutionStartKey struct{}

// Each active Execute/Resume interval includes preparation and planning. A
// resumed run starts a fresh interval so time awaiting human input is excluded.
func withAgentExecutionStart(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentExecutionStartKey{}, time.Now())
}

func agentExecutionStart(ctx context.Context) time.Time {
	if started, ok := ctx.Value(agentExecutionStartKey{}).(time.Time); ok && !started.IsZero() {
		return started
	}
	return time.Now()
}
